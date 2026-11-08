// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build ignore

// item3_ci_probe.go exercises docs/test-plans/2026-11-07.md §9 Item 3:
//   - 3A: Suite `qualification:` stanza (#831), real repo `.grain/`
//     declarations (#825, #680), RC-tags + `synced-release` qualification
//     reconciler over `githubsim`, and scratch-branch child push grants (#621).
//   - 3B: Superseded `release.yml` runs & on-demand build exclusion (#775)
//     plus `github_workflow_run` review schedule prompt (#699).
//   - 3C: Slow passing completed-run branch polling behind >50 PR runs (#819)
//     and release-triage superseding ancestry boundaries (#745).
//   - 3D: Empty GitHub Actions job log host/header diagnostics and non-caching
//     across `github.RESTClient` and `ui.Server` (#828).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type appTokens struct {
	token string
	slug  string
}

func (a appTokens) TokenFor(_, _ string) *string           { return &a.token }
func (a appTokens) AppSlugFor(_, _ string) (string, error) { return a.slug, nil }

// multiRepoTransport routes GitHub API requests to per-owner/repo githubsim
// instances so downstream `synced-release` gates can read upstream tags too.
type multiRepoTransport struct {
	sims       map[string]*githubsim.Sim
	defaultSim *githubsim.Sim
}

func (m *multiRepoTransport) Request(method, reqPath string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	rawPath, rawQuery, _ := strings.Cut(reqPath, "?")
	parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
	sim := m.defaultSim
	if len(parts) >= 3 && parts[0] == "repos" {
		if s, ok := m.sims[parts[1]+"/"+parts[2]]; ok {
			sim = s
		}
	}
	if method == http.MethodGet && len(parts) == 4 && parts[0] == "repos" && parts[3] == "commits" {
		qs, _ := url.ParseQuery(rawQuery)
		if p := qs.Get("path"); p != "" && sim.BareRepo != "" {
			ref := qs.Get("sha")
			if ref == "" {
				ref = "HEAD"
			}
			out := runGit(sim.BareRepo, "log", "--format=%H", ref, "--", p)
			var items []map[string]string
			for _, line := range strings.Split(out, "\n") {
				if sha := strings.TrimSpace(line); sha != "" {
					items = append(items, map[string]string{"sha": sha})
				}
			}
			enc, _ := json.Marshal(items)
			return github.ApiResponse{Status: http.StatusOK, Body: enc}, nil
		}
	}
	return sim.Request(method, reqPath, headers, body)
}

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

func assert(cond bool, format string, args ...any) {
	if !cond {
		fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
		os.Exit(1)
	}
}

func openTempStore(ctx context.Context, root, name string) (*model.Store, func()) {
	dir := filepath.Join(root, name)
	must(os.MkdirAll(dir, 0o755), "mkdir "+dir)
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	must(err, "sqlite.Open "+dir)
	store := model.New(db)
	must(store.Init(ctx), "store.Init "+dir)
	return store, func() { _ = db.Close() }
}

func runGit(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test Bot",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Bot",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: git %v in %s: %v\n%s\n", args, dir, err, string(out))
		os.Exit(1)
	}
	return strings.TrimSpace(string(out))
}

func initBareWithWork(root, sub, defaultBranch string) (bareDir, workDir string) {
	bareDir = filepath.Join(root, sub+"-bare.git")
	workDir = filepath.Join(root, sub+"-work")
	must(os.MkdirAll(bareDir, 0o755), "mkdir bare")
	must(os.MkdirAll(workDir, 0o755), "mkdir work")
	runGit(bareDir, "init", "--bare", "--initial-branch="+defaultBranch)
	runGit(workDir, "init", "--initial-branch="+defaultBranch)
	runGit(workDir, "remote", "add", "origin", bareDir)
	must(os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# "+sub+"\n"), 0o644), "write readme")
	runGit(workDir, "add", "README.md")
	runGit(workDir, "commit", "-m", "initial commit")
	runGit(workDir, "push", "origin", defaultBranch)
	return bareDir, workDir
}

func collectSuiteTaskIDs(runs []model.SuiteRun) []string {
	var out []string
	for _, r := range runs {
		for _, t := range r.Tasks {
			out = append(out, t.TaskID)
		}
	}
	return out
}

func finishQualificationTask(ctx context.Context, store *model.Store, taskID, verdict, tag, sha string, at time.Time) {
	_, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: taskID},
		},
		Body: fmt.Sprintf("Qualification report.\n\nQualification-Verdict: %s\nQualified-Tag: %s\nQualified-Commit: %s\n",
			verdict, tag, sha),
		CreatedAt: at,
	})
	must(err, "AddComment closing note on "+taskID)
	must(store.Observe(ctx, model.Observation{TaskID: taskID, CompletedAt: &at}), "completeTask "+taskID)
}

func runGitHubCITails(ctx context.Context, deps orchestrator.Deps, now time.Time) {
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "github-ci-tails" {
			must(r.Reconcile(ctx, deps, now), "github-ci-tails reconcile")
			return
		}
	}
	assert(false, "github-ci-tails reconciler missing from orchestrator.Reconcilers()")
}

func runSubtest3A(ctx context.Context, tmpRoot, repoCheckout string, baseTime time.Time) {
	fmt.Println("=== 3A: Suite qualification: stanza (#831), release declarations (#825/#680) & scratch-branch push grants (#621) ===")

	grainRepo := model.RepoRef{Owner: "ai-on-gke", Name: "grain"}
	decls, err := repodecl.Load(os.DirFS(repoCheckout), grainRepo)
	must(err, "repodecl.Load("+repoCheckout+")")

	var rqSuite *model.DeclaredSuiteFile
	for i := range decls.Suites {
		if strings.HasSuffix(decls.Suites[i].Path, "/release-qualification.yml") {
			rqSuite = &decls.Suites[i]
			break
		}
	}
	assert(rqSuite != nil, "release-qualification.yml not found in repo declarations")
	assert(len(rqSuite.Suite.Items) == 4, "release-qualification items = %d, want 4", len(rqSuite.Suite.Items))
	assert(strings.HasSuffix(rqSuite.Suite.Items[3].TemplateID, ":qualification-system-smoke"),
		"last item = %q, want qualification-system-smoke", rqSuite.Suite.Items[3].TemplateID)
	assert(!rqSuite.Suite.AutoMerge && !rqSuite.Suite.AutoApproveProposals && !rqSuite.Suite.RequireApproval,
		"unexpected suite flags: autoMerge=%v autoApprove=%v requireApproval=%v",
		rqSuite.Suite.AutoMerge, rqSuite.Suite.AutoApproveProposals, rqSuite.Suite.RequireApproval)

	qualTemplates := 0
	ciReadTemplates := map[string]bool{}
	for _, tf := range decls.Templates {
		if strings.Contains(tf.Path, "/qualification-") {
			qualTemplates++
			if !strings.HasSuffix(tf.Path, "/qualification-system-smoke.yml") {
				matchedScratch, _, delScratch := tf.Push.Allows("refs/heads/qualification/42")
				matchedMain, _, _ := tf.Push.Allows("refs/heads/main")
				assert(matchedScratch && delScratch,
					"%s missing scratch push/delete grant: %+v", tf.Path, tf.Push)
				assert(!matchedMain, "%s unexpectedly allows push to main", tf.Path)
			}
		}
		for _, g := range tf.Template.Grants {
			if strings.Contains(g.Capability, "ci:read") || strings.Contains(g.Capability, "ci-read") {
				ciReadTemplates[tf.Template.Name] = true
			}
		}
	}
	assert(qualTemplates == 4, "found %d qualification-* templates, want 4", qualTemplates)
	for _, wantName := range []string{"Release qualification - Quarantine & CI diagnostics MCP tools", "Release train", "Release watchdog"} {
		assert(ciReadTemplates[wantName], "template %q missing repo-scoped ci:read grant (#825)", wantName)
	}

	foundTrain := false
	for _, sf := range decls.Schedules {
		if strings.HasSuffix(sf.Path, "/release-train.yml") {
			foundTrain = true
			assert(sf.Schedule.Recurrence.Kind == model.RecurrenceEveryNMinutes && sf.Schedule.Recurrence.EveryNMinutes == 20,
				"release-train recurrence = %+v, want every 20 minutes (#680)", sf.Schedule.Recurrence)
		}
	}
	assert(foundTrain, ".grain/schedules/release-train.yml not found")
	fmt.Println("PASS 3A.1: ai-on-gke/grain declarations verified (4 qualification items with scratch push + ci:read, 20m release-train)")

	// Invalid qualification stanzas must fail repodecl.Load (#831).
	badCases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown candidate mode",
			yaml: "templates: [item]\nqualification:\n  candidate: nightly-tags\n",
			want: "candidate",
		},
		{
			name: "synced-release with malformed upstream",
			yaml: "templates: [item]\nqualification:\n  candidate: synced-release\n  upstream: nope\n",
			want: "upstream",
		},
		{
			name: "upstream set without synced-release",
			yaml: "templates: [item]\nqualification:\n  upstream: github.com/acme/core\n",
			want: "only read",
		},
		{
			name: "empty stage1 mapping",
			yaml: "templates: [item]\nqualification:\n  stage1: {}\n",
			want: "stage1",
		},
		{
			name: "stage1 with both workflow and status",
			yaml: "templates: [item]\nqualification:\n  stage1:\n    workflow: ci.yml\n    status: ci/gate\n",
			want: "not both",
		},
	}
	for _, bc := range badCases {
		fsys := fstest.MapFS{
			".grain/templates/item.yml": &fstest.MapFile{Data: []byte("title: Item\nbody: Run.\n")},
			".grain/suites/gate.yml":    &fstest.MapFile{Data: []byte(bc.yaml)},
		}
		_, berr := repodecl.Load(fsys, model.RepoRef{Owner: "acme", Name: "widgets"})
		assert(berr != nil && strings.Contains(strings.ToLower(berr.Error()), bc.want),
			"bad stanza %q: got err=%v, want substring %q", bc.name, berr, bc.want)
	}
	fmt.Println("PASS 3A.2: invalid qualification: stanzas refused cleanly by repodecl.Load")

	// Reconcile RC-tags release-qualification suite over githubsim + RESTClient.
	storeRC, closeRC := openTempStore(ctx, tmpRoot, "store-3a-rc")
	defer closeRC()
	bareRC, workRC := initBareWithWork(tmpRoot, "rc", "main")
	rcSHA := runGit(workRC, "rev-parse", "HEAD")
	runGit(workRC, "tag", "v3.2.0-rc.1", rcSHA)
	runGit(workRC, "push", "origin", "refs/tags/v3.2.0-rc.1")

	simRC := githubsim.New("ai-on-gke", "grain", bareRC, "main")
	simRC.CheckRunApp = "grain-bot"
	clientRC := github.NewClient(simRC, appTokens{token: "sim-tok", slug: "grain-bot"})

	_, err = storeRC.SyncRepoDeclarations(ctx, grainRepo, rcSHA, decls, baseTime, time.UTC)
	must(err, "SyncRepoDeclarations RC")

	// Stage 1 not yet green -> 0 tasks filed.
	must(orchestrator.SyncReleaseQualification(ctx, storeRC, clientRC, baseTime), "SyncReleaseQualification before Stage 1")
	runsBefore, err := storeRC.SuiteRunsTagged(ctx, model.ReleaseQualificationTag(grainRepo, "v3.2.0-rc.1"))
	must(err, "SuiteRunsTagged before Stage 1")
	assert(len(runsBefore) == 0, "expected 0 suite runs before Stage 1 green, got %d", len(runsBefore))

	// Make Stage 1 (`nightly` workflow run on rcSHA) green in githubsim.
	simRC.AddWorkflowRun(githubsim.WorkflowRun{
		ID:         901,
		Name:       "nightly",
		Path:       ".github/workflows/nightly.yml",
		HeadBranch: "v3.2.0-rc.1",
		HeadSHA:    rcSHA,
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		CreatedAt:  baseTime,
		Jobs:       []githubsim.WorkflowJob{{Name: "go-long", Conclusion: "success"}},
	})
	must(orchestrator.SyncReleaseQualification(ctx, storeRC, clientRC, baseTime.Add(time.Minute)), "SyncReleaseQualification Stage 1 green")

	runsStage2, err := storeRC.SuiteRunsTagged(ctx, model.ReleaseQualificationTag(grainRepo, "v3.2.0-rc.1"))
	must(err, "SuiteRunsTagged after Stage 1")
	assert(len(runsStage2) == 1 && len(runsStage2[0].Tasks) == 3,
		"expected 1 suite run with first 3 items (holding smoke), got %+v", runsStage2)

	for _, st := range runsStage2[0].Tasks {
		assert(!strings.HasSuffix(st.TemplateID, ":qualification-system-smoke"),
			"smoke item filed prematurely before earlier items passed")
		tObj, gerr := storeRC.GetTask(ctx, st.TaskID)
		must(gerr, "GetTask "+st.TaskID)
		assert(tObj != nil && strings.Contains(tObj.Body, "v3.2.0-rc.1") && strings.Contains(tObj.Body, rcSHA[:12]),
			"task %s body missing rc_tag/rc_sha substitution", st.TaskID)
		finishQualificationTask(ctx, storeRC, st.TaskID, "PASS", "v3.2.0-rc.1", rcSHA, baseTime.Add(2*time.Minute))
	}

	// Next sync files item 4 (qualification-system-smoke) in a second SuiteRun under the same tag.
	must(orchestrator.SyncReleaseQualification(ctx, storeRC, clientRC, baseTime.Add(3*time.Minute)), "SyncReleaseQualification file smoke")
	runsWithSmoke, err := storeRC.SuiteRunsTagged(ctx, model.ReleaseQualificationTag(grainRepo, "v3.2.0-rc.1"))
	must(err, "SuiteRunsTagged smoke")
	allRCTasks := collectSuiteTaskIDs(runsWithSmoke)
	assert(len(allRCTasks) == 4,
		"expected 4 tasks after earlier items passed, got %+v", runsWithSmoke)
	smokeID := allRCTasks[3]
	finishQualificationTask(ctx, storeRC, smokeID, "PASS", "v3.2.0-rc.1", rcSHA, baseTime.Add(4*time.Minute))

	// Final sync posts completed/success check run on rcSHA.
	must(orchestrator.SyncReleaseQualification(ctx, storeRC, clientRC, baseTime.Add(5*time.Minute)), "SyncReleaseQualification final")
	crs, err := clientRC.ListCheckRuns("ai-on-gke", "grain", rcSHA)
	must(err, "ListCheckRuns rcSHA")
	foundPassCheck := false
	for _, cr := range crs {
		if cr.Name == "grain/release-qualification" && cr.App == "grain-bot" && cr.Conclusion != nil && *cr.Conclusion == "success" {
			foundPassCheck = true
		}
	}
	assert(foundPassCheck, "expected completed/success grain/release-qualification check run on %s, got %+v", rcSHA, crs)
	fmt.Println("PASS 3A.3: RC-tags release-qualification gated on Stage 1, held smoke item last, and posted grain/release-qualification=success")

	// Reconcile custom `qualification:` stanza with `candidate: synced-release` (#831) over githubsim.
	storeEd, closeEd := openTempStore(ctx, tmpRoot, "store-3a-ed")
	defer closeEd()
	bareCore, workCore := initBareWithWork(tmpRoot, "core", "main")
	coreRelSHA := runGit(workCore, "rev-parse", "HEAD")
	runGit(workCore, "tag", "v1.4.0", coreRelSHA)
	runGit(workCore, "push", "origin", "refs/tags/v1.4.0")

	bareEd, workEd := initBareWithWork(tmpRoot, "edition", "main")
	must(os.WriteFile(filepath.Join(workEd, "go.mod"),
		[]byte("module github.com/acme/edition\n\ngo 1.26\n\nrequire github.com/acme/core v1.4.0\n"), 0o644), "write synced go.mod")
	runGit(workEd, "add", "go.mod")
	runGit(workEd, "commit", "-m", "sync acme/core to v1.4.0")
	syncSHA := runGit(workEd, "rev-parse", "HEAD")

	// Follow with a trunk pseudo-version commit that is NOT a release tag: syncedReleaseCandidate must skip it and pick syncSHA!
	must(os.WriteFile(filepath.Join(workEd, "go.mod"),
		[]byte("module github.com/acme/edition\n\ngo 1.26\n\nrequire github.com/acme/core v1.4.1-0.20261107000000-deadbeefcafe\n"), 0o644), "write trunk go.mod")
	runGit(workEd, "add", "go.mod")
	runGit(workEd, "commit", "-m", "track acme/core trunk")
	runGit(workEd, "push", "origin", "main")

	simCore := githubsim.New("acme", "core", bareCore, "main")
	simEd := githubsim.New("acme", "edition", bareEd, "main")
	simEd.CheckRunApp = "grain-bot"
	edClient := github.NewClient(&multiRepoTransport{
		sims:       map[string]*githubsim.Sim{"acme/core": simCore, "acme/edition": simEd},
		defaultSim: simEd,
	}, appTokens{token: "sim-tok", slug: "grain-bot"})

	edRepo := model.RepoRef{Owner: "acme", Name: "edition"}
	edFS := fstest.MapFS{
		".grain/templates/ed-check.yml": &fstest.MapFile{Data: []byte(
			"title: Qualify {{release}}\nfields:\n  - name: rc_tag\n  - name: rc_sha\n  - name: release\nbody: |\n  Tag={{rc_tag}} Release={{release}} SHA={{rc_sha}}\n")},
		".grain/templates/ed-smoke.yml": &fstest.MapFile{Data: []byte(
			"title: Smoke {{release}}\nfields:\n  - name: rc_tag\n  - name: rc_sha\n  - name: release\nbody: |\n  Smoke={{rc_tag}}@{{rc_sha}}\n")},
		".grain/suites/edition-qualification.yml": &fstest.MapFile{Data: []byte(
			"name: Edition qualification\ntemplates:\n  - ed-check\n  - ed-smoke\nmode: until_clean\nmax-passes: 1\nrequire-approval: false\nauto-merge: false\nqualification:\n  check: grain/edition-qualification\n  candidate: synced-release\n  stage1:\n    workflow: CI\n  upstream: github.com/acme/core\n")},
	}
	edDecls, err := repodecl.Load(edFS, edRepo)
	must(err, "repodecl.Load synced-release")
	assert(len(edDecls.Suites) == 1 && edDecls.Suites[0].Qualification != nil &&
		edDecls.Suites[0].Qualification.Candidate == model.CandidateSyncedRelease,
		"expected parsed synced-release qualification stanza")
	_, err = storeEd.SyncRepoDeclarations(ctx, edRepo, syncSHA, edDecls, baseTime, time.UTC)
	must(err, "SyncRepoDeclarations edition")

	// Before Stage 1 workflow `ci.yml` passes on syncSHA -> 0 runs.
	must(orchestrator.SyncReleaseQualification(ctx, storeEd, edClient, baseTime), "SyncReleaseQualification edition pre-stage1")
	edTagKey := model.QualificationTag("edition-qualification", edRepo, "v1.4.0@"+syncSHA)
	edRuns0, err := storeEd.SuiteRunsTagged(ctx, edTagKey)
	must(err, "SuiteRunsTagged edition pre-stage1")
	assert(len(edRuns0) == 0, "expected 0 runs before ci.yml green on syncSHA, got %d", len(edRuns0))

	simEd.AddWorkflowRun(githubsim.WorkflowRun{
		ID:         950,
		Name:       "CI",
		Path:       ".github/workflows/ci.yml",
		HeadBranch: "main",
		HeadSHA:    syncSHA,
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		CreatedAt:  baseTime,
		Jobs:       []githubsim.WorkflowJob{{Name: "test", Conclusion: "success"}},
	})
	must(orchestrator.SyncReleaseQualification(ctx, storeEd, edClient, baseTime.Add(time.Minute)), "SyncReleaseQualification edition stage1 green")
	edRuns1, err := storeEd.SuiteRunsTagged(ctx, edTagKey)
	must(err, "SuiteRunsTagged edition item 1")
	assert(len(edRuns1) == 1 && len(edRuns1[0].Tasks) == 1, "expected item 1 filed on syncSHA, got %+v", edRuns1)
	edTask1, err := storeEd.GetTask(ctx, edRuns1[0].Tasks[0].TaskID)
	must(err, "GetTask edTask1")
	assert(strings.Contains(edTask1.Body, "Tag=v1.4.0 Release=v1.4.0 SHA="+syncSHA),
		"unexpected synced-release template substitution: %q", edTask1.Body)

	finishQualificationTask(ctx, storeEd, edTask1.ID, "PASS", "v1.4.0", syncSHA, baseTime.Add(2*time.Minute))
	must(orchestrator.SyncReleaseQualification(ctx, storeEd, edClient, baseTime.Add(3*time.Minute)), "SyncReleaseQualification edition smoke")
	edRuns2, err := storeEd.SuiteRunsTagged(ctx, edTagKey)
	must(err, "SuiteRunsTagged edition smoke")
	allEdTasks := collectSuiteTaskIDs(edRuns2)
	assert(len(allEdTasks) == 2, "expected smoke item filed, got %+v", edRuns2)
	finishQualificationTask(ctx, storeEd, allEdTasks[1], "PASS", "v1.4.0", syncSHA, baseTime.Add(4*time.Minute))
	must(orchestrator.SyncReleaseQualification(ctx, storeEd, edClient, baseTime.Add(5*time.Minute)), "SyncReleaseQualification edition complete")

	edChecks, err := edClient.ListCheckRuns("acme", "edition", syncSHA)
	must(err, "ListCheckRuns syncSHA")
	foundEdCheck := false
	for _, cr := range edChecks {
		if cr.Name == "grain/edition-qualification" && cr.App == "grain-bot" && cr.Conclusion != nil && *cr.Conclusion == "success" {
			foundEdCheck = true
		}
	}
	assert(foundEdCheck, "expected grain/edition-qualification=success on syncSHA %s, got %+v", syncSHA, edChecks)
	fmt.Println("PASS 3A.4: custom qualification: (synced-release + stage1.workflow=ci.yml) selected go.mod release pin and posted grain/edition-qualification=success")

	// Scratch-branch fixture child proposals via ui.Client.CreateTask (#621).
	uiClient := ui.NewClient(ui.Config{GitHubPullRequests: clientRC}, storeRC)
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, grainRepo, "qualification-mergequeue")
	parent, err := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
		Creator:    "operator@example.com",
		TemplateID: tmplID,
		Approved:   true,
		Fields:     map[string]string{"rc_tag": "v3.2.0-rc.1", "rc_sha": rcSHA},
	})
	must(err, "CreateTask qualification parent")
	parentStored, err := storeRC.GetTask(ctx, parent.ID)
	must(err, "GetTask parent")
	assert(!parentStored.AutoMerge && !parentStored.AutoApproveProposals,
		"parent should hold neither autoMerge nor autoApproveProposals")

	scratchCases := []struct {
		name       string
		base       string
		decline    bool
		wantQueued bool
		wantMerges bool
	}{
		{"own-scratch-fixture", model.QualificationScratchBranch(parent.ID), false, true, true},
		{"own-scratch-held", model.QualificationScratchBranch(parent.ID), true, true, false},
		{"default-branch-defect", "", false, false, false},
		{"foreign-scratch-branch", model.QualificationScratchBranch("9999"), false, false, false},
	}
	for _, sc := range scratchCases {
		child, cerr := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
			Title:            sc.name,
			Description:      "fixture test",
			Repo:             grainRepo.String(),
			Base:             sc.base,
			ProposedBy:       parent.ID,
			DeclineAutoMerge: sc.decline,
		})
		must(cerr, "CreateTask child "+sc.name)
		st, serr := storeRC.State(ctx, child.ID)
		must(serr, "State "+child.ID)
		gotChild, gerr := storeRC.GetTask(ctx, child.ID)
		must(gerr, "GetTask child "+child.ID)
		assert((st == model.StateQueued) == sc.wantQueued,
			"%s: state=%q, wantQueued=%v", sc.name, st, sc.wantQueued)
		assert(gotChild.AutoMerge == sc.wantMerges,
			"%s: AutoMerge=%v, want %v", sc.name, gotChild.AutoMerge, sc.wantMerges)
	}
	fmt.Println("PASS 3A.5: scratch-branch child proposals (#621) queue+auto-merge on qualification/<parent-id> and hold on main / foreign branches")
}

func runSubtest3BC(ctx context.Context, tmpRoot string, baseTime time.Time) {
	fmt.Println("=== 3B & 3C: Superseded release.yml (#775), review prompt (#699), slow branch polling (#819) & triage ancestry (#745) ===")

	store, closeStore := openTempStore(ctx, tmpRoot, "store-3bc")
	defer closeStore()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must(store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}), "PutRepoConfig acme/widgets")

	bare, work := initBareWithWork(tmpRoot, "widgets", "main")
	c0 := runGit(work, "rev-parse", "HEAD")

	// Create a diverged commit not on main's ancestry (for #745 non-ancestry triage check).
	runGit(work, "checkout", "-b", "diverged-branch")
	must(os.WriteFile(filepath.Join(work, "diverged.txt"), []byte("diverged\n"), 0o644), "write diverged")
	runGit(work, "add", "diverged.txt")
	runGit(work, "commit", "-m", "diverged commit")
	cDiverged := runGit(work, "rev-parse", "HEAD")
	runGit(work, "push", "origin", "diverged-branch")

	// Advance main: C0 -> C1 -> C2 -> C3.
	runGit(work, "checkout", "main")
	advanceMain := func(label string) string {
		must(os.WriteFile(filepath.Join(work, label+".txt"), []byte(label+"\n"), 0o644), "write "+label)
		runGit(work, "add", label+".txt")
		runGit(work, "commit", "-m", "commit "+label)
		runGit(work, "push", "origin", "main")
		return runGit(work, "rev-parse", "HEAD")
	}
	c1 := advanceMain("c1")

	sim := githubsim.New("acme", "widgets", bare, "main")
	client := github.NewClient(sim, appTokens{token: "sim-tok", slug: "grain-bot"})
	deps := orchestrator.Deps{Store: store, Client: client}

	// Declare a github_workflow_run review schedule (#699) on nightly.yml.
	schedFS := fstest.MapFS{
		".grain/schedules/review-nightly.yml": &fstest.MapFile{Data: []byte(
			"name: Review Nightly\ntrigger:\n  github_workflow_run:\n    workflow: nightly.yml\n    conclusions: [failure]\n    branches: [main]\n")},
	}
	schedDecls, err := repodecl.Load(schedFS, repo)
	must(err, "repodecl.Load review-nightly")
	assert(len(schedDecls.Schedules) == 1 && schedDecls.Schedules[0].Schedule.TemplateID != nil &&
		*schedDecls.Schedules[0].Schedule.TemplateID == model.GitHubWorkflowReviewTemplateID("nightly"),
		"expected GitHubWorkflowReviewTemplateID(nightly), got %+v", schedDecls.Schedules)
	_, err = store.SyncRepoDeclarations(ctx, repo, c1, schedDecls, baseTime, time.UTC)
	must(err, "SyncRepoDeclarations review-nightly")

	// Seed BaseBranchRelease row so repo/main is tracked as a release-polled branch,
	// plus C0's cancelled release.yml superseded by C1's in_progress release.yml (#775).
	sim.WorkflowRuns = []githubsim.WorkflowRun{
		{
			ID:         1002,
			Name:       "release",
			Path:       ".github/workflows/release.yml",
			HeadBranch: "main",
			HeadSHA:    c1,
			Event:      "push",
			Status:     "in_progress",
			RunAttempt: 1,
			CreatedAt:  baseTime.Add(2 * time.Minute),
			Jobs:       []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: ""}},
		},
		{
			ID:         1001,
			Name:       "release",
			Path:       ".github/workflows/release.yml",
			HeadBranch: "main",
			HeadSHA:    c0,
			Event:      "push",
			Status:     "completed",
			Conclusion: "cancelled",
			RunAttempt: 1,
			CreatedAt:  baseTime.Add(1 * time.Minute),
			Jobs:       []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "cancelled"}},
		},
		// Also include an on-demand workflow_dispatch failure (#775) which must NOT count as a branch release failure!
		{
			ID:           1003,
			Name:         "release",
			DisplayTitle: "release " + c0[:7] + " (on demand)",
			Path:         ".github/workflows/release.yml",
			HeadBranch:   "main",
			HeadSHA:      c1,
			Event:        "workflow_dispatch",
			Status:       "completed",
			Conclusion:   "failure",
			RunAttempt:   1,
			CreatedAt:    baseTime.Add(3 * time.Minute),
			Jobs:         []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "failure"}},
		},
		// Also conclude nightly.yml failure to fire #699 review schedule!
		{
			ID:         1004,
			Name:       "nightly",
			Path:       ".github/workflows/nightly.yml",
			HeadBranch: "main",
			HeadSHA:    c1,
			Event:      "schedule",
			Status:     "completed",
			Conclusion: "failure",
			RunAttempt: 1,
			CreatedAt:  baseTime.Add(3 * time.Minute),
			Jobs:       []githubsim.WorkflowJob{{Name: "soak", Conclusion: "failure", FailingStep: "go test ./...", Log: "FAIL: soak timeout\n"}},
		},
	}

	runGitHubCITails(ctx, deps, baseTime.Add(4*time.Minute))

	bRel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	must(err, "GetBaseBranchRelease after superseded run")
	assert(bRel != nil && bRel.Superseded() && bRel.Failures == 0,
		"expected superseded BaseBranchRelease with Failures=0, got %+v", bRel)
	bCI, err := store.GetBaseBranchCI(ctx, repo, "main")
	must(err, "GetBaseBranchCI after superseded run")
	assert(bCI == nil || !bCI.Broken(), "superseded release.yml must never mark BaseBranchCI broken: %+v", bCI)
	openTriage0, err := store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main"))
	must(err, "OpenTasksWithTag after superseded")
	assert(len(openTriage0) == 0, "expected 0 triage tasks for superseded + on-demand runs, got %v", openTriage0)
	fmt.Printf("PASS 3B.1: cancelled release.yml at %s superseded by %s (Detail=%q, Failures=0, BaseBranchCI unbroken, on-demand run ignored)\n",
		c0[:7], c1[:7], bRel.Detail)

	// Check #699 review task filed for nightly.yml failure!
	allTasks, err := store.ListTasks(ctx)
	must(err, "ListTasks after nightly failure")
	foundNightlyReview := false
	for _, t := range allTasks {
		if t.TemplateID == model.GitHubWorkflowReviewTemplateID("nightly") {
			foundNightlyReview = true
			assert(strings.Contains(t.Body, ".github/workflows") && !strings.Contains(t.Body, ".grain/workflows/nightly"),
				"expected GitHub Actions-specific workflow path in prompt (#699), got:\n%s", t.Body)
			comments, cerr := store.Comments(ctx, t.ID)
			must(cerr, "Comments on nightly review task")
			combined := t.Body
			for _, c := range comments {
				combined += "\n" + c.Body
			}
			assert(strings.Contains(combined, "1004") && strings.Contains(combined, "soak") && strings.Contains(combined, "grain ci log"),
				"expected run ID 1004, failing job soak, and grain ci log in review task context:\n%s", combined)
		}
	}
	assert(foundNightlyReview, "expected github_workflow_run review task to be filed for nightly.yml failure (#699)")
	fmt.Println("PASS 3B.2: github_workflow_run review schedule (#699) filed task with .github/workflows prompt + run/job/log details")

	// 3C: Now C1's release.yml (run 1002) and C2's release.yml (run 2001) and C2b's (run 2002) fail!
	c2 := advanceMain("c2")
	c3 := advanceMain("c3")

	// Seed an unrelated open triage task on another repo so we confirm cross-repo isolation too.
	otherRepo := model.RepoRef{Owner: "acme", Name: "other"}
	seedOpenTriage := func(id string, r model.RepoRef, sha string) {
		created := baseTime
		must(store.PutTask(ctx, model.Task{
			ID:         id,
			Intent:     model.IntentImplement,
			Title:      "Triage " + id,
			Body:       "Seeded triage",
			Origin:     model.Origin{Attribution: model.Attribution{Actor: orchestrator.BaseReleaseTriager}, Reason: model.ReasonDirect},
			Approval:   &model.Attribution{Actor: orchestrator.BaseReleaseTriager},
			ApprovedAt: &created,
			Target:     &r,
			Binding:    model.BindingDirective,
			Base:       "main",
			AutoMerge:  true,
			Tags:       []string{orchestrator.BaseReleaseTriageBranchTag(r, "main"), orchestrator.BaseReleaseTriageTag(r, "main", sha)},
			CreatedAt:  &created,
		}), "PutTask "+id)
	}
	seedOpenTriage("triage-other-repo", otherRepo, c0)

	failRun := func(id int64, sha string, minOffset int) {
		sim.WorkflowRuns = []githubsim.WorkflowRun{{
			ID:         id,
			Name:       "release",
			Path:       ".github/workflows/release.yml",
			HeadBranch: "main",
			HeadSHA:    sha,
			Event:      "push",
			Status:     "completed",
			Conclusion: "failure",
			RunAttempt: 1,
			CreatedAt:  baseTime.Add(time.Duration(minOffset) * time.Minute),
			Jobs:       []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "failure", FailingStep: "docker push"}},
		}}
		runGitHubCITails(ctx, deps, baseTime.Add(time.Duration(minOffset+1)*time.Minute))
	}
	failRun(2010, c1, 5)
	triageC1Task, err := store.OpenTaskWithTag(ctx, orchestrator.BaseReleaseTriageTag(repo, "main", c1))
	must(err, "OpenTaskWithTag C1")
	assert(triageC1Task != nil, "expected real release-triage task filed for C1 failure")

	failRun(2011, c2, 8)
	failRun(2012, c2, 11) // Run 3rd consecutive failure so streak >= 3!
	bRelFail, err := store.GetBaseBranchRelease(ctx, repo, "main")
	must(err, "GetBaseBranchRelease after 3 failures")
	assert(bRelFail != nil && bRelFail.Failures == 3 && bRelFail.FailingSince != nil,
		"expected Failures=3 and non-nil FailingSince, got %+v", bRelFail)

	// Also add an open triage task whose failing commit is cDiverged (not an ancestor of C3) so closeSupersededReleaseTriage tests both!
	seedOpenTriage("triage-non-ancestor", repo, cDiverged)

	// Test #745: older commit C0 finishes `success` AFTER C1/C2 failed (even as a newly started re-run at +15m!).
	// Wait: if C0 runs at +15m, C0 is an ancestor of C1 and C2 (`CompareRefs(c1, c0)` == "behind"),
	// so `triageC1Task` MUST STAY OPEN! Let's test older-started C0 finishing late first (+4m CreatedAt, +15m polled):
	sim.WorkflowRuns = []githubsim.WorkflowRun{{
		ID:         2099,
		Name:       "release",
		Path:       ".github/workflows/release.yml",
		HeadBranch: "main",
		HeadSHA:    c0,
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		RunAttempt: 1,
		CreatedAt:  baseTime.Add(4 * time.Minute),
		Jobs:       []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "success"}},
	}}
	runGitHubCITails(ctx, deps, baseTime.Add(16*time.Minute))
	stC1AfterOld, err := store.State(ctx, triageC1Task.ID)
	must(err, "State triageC1Task after older commit success")
	assert(stC1AfterOld == model.StateQueued, "older commit C0 finishing late must NOT close C1's triage task (#745), got state %q", stC1AfterOld)
	bRelStillRed, err := store.GetBaseBranchRelease(ctx, repo, "main")
	must(err, "GetBaseBranchRelease after older commit")
	assert(bRelStillRed.Failures == 3, "older commit C0 finishing late must not reset newer failure streak, got Failures=%d", bRelStillRed.Failures)
	fmt.Println("PASS 3C.1: older ancestor commit finishing green late keeps newer C1 triage task open and preserves failure streak (#745)")

	// Now test #819 + #745: C3 starts slow release run 5001 at +20m, 70 faster PR runs (5002..5071) complete at +21m..+40m
	// (pushing 5001 out of repo-wide CompletedWorkflowRuns limit=50), then 5001 completes `success` at +45m!
	releaseStart := baseTime.Add(20 * time.Minute)
	const prNoiseRuns = 70
	busyRuns := make([]githubsim.WorkflowRun, 0, prNoiseRuns+1)
	for i := prNoiseRuns; i >= 1; i-- {
		busyRuns = append(busyRuns, githubsim.WorkflowRun{
			ID:         int64(5001 + i),
			Name:       "Tests",
			Path:       ".github/workflows/tests.yml",
			HeadBranch: fmt.Sprintf("pr-%d", i),
			HeadSHA:    c1,
			Event:      "pull_request",
			Status:     "completed",
			Conclusion: "success",
			RunAttempt: 1,
			CreatedAt:  releaseStart.Add(time.Duration(i) * 10 * time.Second),
			Jobs:       []githubsim.WorkflowJob{{Name: "unit", Conclusion: "success"}},
		})
	}
	busyRuns = append(busyRuns, githubsim.WorkflowRun{
		ID:         5001,
		Name:       "release",
		Path:       ".github/workflows/release.yml",
		HeadBranch: "main",
		HeadSHA:    c3,
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		RunAttempt: 1,
		CreatedAt:  releaseStart,
		Jobs:       []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "success"}},
	})
	sim.WorkflowRuns = busyRuns

	// Confirm repo-wide CompletedWorkflowRuns(limit=50) does NOT contain run 5001!
	top50, err := client.CompletedWorkflowRuns("acme", "widgets", 50)
	must(err, "CompletedWorkflowRuns(50)")
	for _, r := range top50 {
		assert(r.ID != 5001, "run 5001 unexpectedly appeared in repo-wide top-50 page")
	}

	runGitHubCITails(ctx, deps, baseTime.Add(45*time.Minute))

	bRelGreen, err := store.GetBaseBranchRelease(ctx, repo, "main")
	must(err, "GetBaseBranchRelease after C3 green")
	assert(bRelGreen != nil && bRelGreen.RunID == "5001" && bRelGreen.Conclusion == "success" &&
		bRelGreen.Failures == 0 && bRelGreen.FailingSince == nil,
		"expected branch-scoped poll (#819) to record run 5001 success and reset Failures=0, got %+v", bRelGreen)

	findingsAfter, err := store.BaseBranchPublishingFindings(ctx, baseTime.Add(50*time.Minute))
	must(err, "BaseBranchPublishingFindings after C3 green")
	assert(len(findingsAfter) == 0, "expected 0 publishing findings after C3 green, got %+v", findingsAfter)

	// C1 is an ancestor of C3 -> triageC1Task MUST be closed as superseded!
	stC1AfterC3, err := store.State(ctx, triageC1Task.ID)
	must(err, "State triageC1Task after C3 green")
	assert(stC1AfterC3 == model.StateClosed, "expected C1 triage task to close as superseded after descendant C3 published, got %q", stC1AfterC3)

	// Non-ancestor `triage-non-ancestor` (cDiverged) and `triage-other-repo` MUST stay open (#745)!
	stDiverged, err := store.State(ctx, "triage-non-ancestor")
	must(err, "State triage-non-ancestor")
	assert(stDiverged == model.StateQueued, "non-ancestor triage task must stay open (#745), got %q", stDiverged)
	stOther, err := store.State(ctx, "triage-other-repo")
	must(err, "State triage-other-repo")
	assert(stOther == model.StateQueued, "other-repo triage task must stay open, got %q", stOther)
	fmt.Printf("PASS 3C.2: branch-scoped completed-run poll (#819) found run 5001 behind %d PR runs, reset Failures=3->0, superseded C1 triage %s, and kept non-ancestor/other-repo triage tasks open (#745)\n",
		prNoiseRuns, triageC1Task.ID)
}

func runSubtest3D(ctx context.Context, tmpRoot string, baseTime time.Time) {
	fmt.Println("=== 3D: Empty GitHub Actions job log host diagnostic & non-caching (#828) ===")

	var servePopulated atomic.Bool
	var blobHits atomic.Int32

	blobLn, err := net.Listen("tcp", "127.0.0.1:4285")
	must(err, "listen blob 127.0.0.1:4285")
	defer blobLn.Close()
	blobSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blobHits.Add(1)
		// Ensure Authorization header was stripped across host redirect!
		if auth := r.Header.Get("Authorization"); auth != "" {
			http.Error(w, "leaked Authorization header to blob store", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Server", "Windows-Azure-Blob/1.0")
		w.Header().Set("Via", "1.1 edge.proxy")
		w.WriteHeader(http.StatusOK)
		if servePopulated.Load() {
			_, _ = io.WriteString(w, "=== STEP PASS: image pushed ===\n")
		}
	})}
	go func() { _ = blobSrv.Serve(blobLn) }()
	defer blobSrv.Close()

	forgeLn, err := net.Listen("tcp", "127.0.0.1:4284")
	must(err, "listen forge 127.0.0.1:4284")
	defer forgeLn.Close()
	nowStr := baseTime.Format(time.RFC3339)
	forgeSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/runs/42":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":42,"name":"Release","path":".github/workflows/release.yml","head_branch":"main","head_sha":"c0ffee123456","event":"push","status":"completed","conclusion":"failure","run_attempt":1,"created_at":%q,"updated_at":%q,"html_url":"http://127.0.0.1:4284/acme/widgets/actions/runs/42"}`, nowStr, nowStr)
		case "/repos/acme/widgets/actions/runs/42/jobs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"total_count":1,"jobs":[{"id":701,"run_id":42,"run_attempt":1,"name":"publish","status":"completed","conclusion":"failure","started_at":%q,"completed_at":%q,"steps":[{"number":1,"name":"push","status":"completed","conclusion":"failure"}]}]}`, nowStr, nowStr)
		case "/repos/acme/widgets/actions/jobs/701/logs":
			http.Redirect(w, r, "http://localhost:4285/container/job-701.log?sig=SUPER_SECRET_SAS_TOKEN", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = forgeSrv.Serve(forgeLn) }()
	defer forgeSrv.Close()

	ghClient := github.NewClient(&github.RealTransport{
		Host:   "127.0.0.1:4284",
		UseTLS: false,
	}, appTokens{token: "ghs_secret_forge_token", slug: "grain-bot"})

	// 1. Direct RESTClient.ActionsJobLog on 0-byte 200 OK response.
	logText, err := ghClient.ActionsJobLog("acme", "widgets", 701)
	assert(logText == "" && errors.Is(err, github.ErrEmptyJobLog),
		"expected ErrEmptyJobLog on 0-byte response, got text=%q err=%v", logText, err)
	errMsg := err.Error()
	assert(strings.Contains(errMsg, "127.0.0.1:4284") && strings.Contains(errMsg, "http://localhost:4285"),
		"expected both forge and redirect blob host in error, got: %s", errMsg)
	assert(strings.Contains(errMsg, "content-type text/plain, server Windows-Azure-Blob/1.0, via 1.1 edge.proxy"),
		"expected answeredBy header diagnostic in error, got: %s", errMsg)
	assert(!strings.Contains(errMsg, "SUPER_SECRET_SAS_TOKEN") && !strings.Contains(errMsg, "job-701.log"),
		"signed blob path/query leaked in error: %s", errMsg)
	fmt.Printf("PASS 3D.1: RESTClient.ActionsJobLog empty-body error names both hosts + headers with zero SAS leakage: %s\n", errMsg)

	// 2. Live ui.Server GET /api/ci/runs/gha:acme:widgets:42/log?job=publish must return 502 with host diagnostic
	//    AND must NOT cache the empty result so the very next read succeeds once blob store populates!
	storeUI, closeUI := openTempStore(ctx, tmpRoot, "store-3d-ui")
	defer closeUI()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must(storeUI.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}), "PutRepoConfig 3D")
	uiClient := ui.NewClient(ui.Config{
		Actions:       ghClient,
		LocalAPIToken: "test-local-token",
	}, storeUI)
	uiSrv := ui.NewServerWithClient(uiClient)
	doAuthedGet := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.RemoteAddr = "127.0.0.1:4286"
		r.Header.Set("Authorization", "Bearer test-local-token")
		rec := httptest.NewRecorder()
		uiSrv.ServeHTTP(rec, r)
		return rec
	}

	rec1 := doAuthedGet("/api/ci/runs/gha:acme:widgets:42/log?job=publish")
	body1 := rec1.Body.String()
	assert(rec1.Code == http.StatusBadGateway, "expected 502 Bad Gateway on empty job log, got %d: %s", rec1.Code, body1)
	assert(strings.Contains(body1, "127.0.0.1:4284") && strings.Contains(body1, "redirected to localhost:4285") &&
		strings.Contains(body1, "Windows-Azure-Blob/1.0") && !strings.Contains(body1, "SUPER_SECRET_SAS_TOKEN"),
		"unexpected 502 body: %s", body1)

	// Flip blob store to serve real bytes; immediately query the SAME uiSrv (inside actionsFinishedTTL).
	hitsBefore := blobHits.Load()
	servePopulated.Store(true)
	rec2 := doAuthedGet("/api/ci/runs/gha:acme:widgets:42/log?job=publish")
	assert(rec2.Code == http.StatusOK, "expected immediate retry to succeed (un-cached empty log), got %d: %s", rec2.Code, rec2.Body.String())
	var parsed ui.CIRunLogResponse
	must(json.Unmarshal(rec2.Body.Bytes(), &parsed), "unmarshal CIRunLogResponse")
	assert(parsed.Log == "=== STEP PASS: image pushed ===\n" && parsed.Bytes == 32 && blobHits.Load() > hitsBefore,
		"unexpected retry response: %+v (blobHits=%d->%d)", parsed, hitsBefore, blobHits.Load())

	// Third read SHOULD hit cache (blobHits unchanged)!
	hitsAfterPopulate := blobHits.Load()
	rec3 := doAuthedGet("/api/ci/runs/gha:acme:widgets:42/log?job=publish")
	assert(rec3.Code == http.StatusOK && blobHits.Load() == hitsAfterPopulate,
		"expected populated completed job log to be cached on subsequent reads (hits=%d->%d)", hitsAfterPopulate, blobHits.Load())
	fmt.Println("PASS 3D.2: ui.Server /api/ci/runs/{id}/log exposed 502 host diagnostic without caching empty response, then served & cached populated log")
}

func main() {
	repoCheckout := "/home/debian/work"
	if len(os.Args) > 1 && os.Args[1] != "" {
		repoCheckout = os.Args[1]
	}
	tmpRoot, err := os.MkdirTemp("", "item3-ci-probe-*")
	must(err, "MkdirTemp")
	defer os.RemoveAll(tmpRoot)

	ctx := context.Background()
	baseTime := time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)

	runSubtest3A(ctx, tmpRoot, repoCheckout, baseTime)
	runSubtest3BC(ctx, tmpRoot, baseTime)
	runSubtest3D(ctx, tmpRoot, baseTime)

	fmt.Println("ALL ITEM 3 GO PROBES PASSED")
}
