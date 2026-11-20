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

package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/checks"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

func commitAndPushFile(t *testing.T, bareRepo, branch, relFile, content, msg string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "clone")
	for _, args := range [][]string{
		{"-c", "safe.bareRepository=all", "clone", "-q", bareRepo, work},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git clone: %v (%s)", err, out)
		}
	}
	if out, err := exec.Command("git", "-C", work, "checkout", "-q", "-B", branch).CombinedOutput(); err != nil {
		t.Fatalf("git checkout -B %s: %v (%s)", branch, err, out)
	}
	full := filepath.Join(work, relFile)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", work, "config", "user.email", "agent@example.com"},
		{"-C", work, "config", "user.name", "agent"},
		{"-C", work, "add", "."},
		{"-C", work, "commit", "-q", "-m", msg},
		{"-C", work, "push", "-q", "-f", "origin", branch},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	shaOut, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(shaOut))
}

func enableTestQA(t *testing.T, ctx context.Context, store *model.Store, rolloutPct int) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	var next model.Config
	if cfg != nil {
		next = *cfg
	}
	next.QA = model.QAConfig{
		Enabled:         true,
		FlowID:          model.QAFlowGoldenV1Minimal,
		RolloutPct:      rolloutPct,
		ShadowOnControl: model.BoolPtr(true),
		Epoch:           1,
		RegimeID:        1,
	}
	if err := store.PutConfig(ctx, next); err != nil {
		t.Fatalf("PutConfig QA: %v", err)
	}
}

func TestQATreatmentT1StagingRefPromotesAndOpensPRCleanly(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qat1", repo)

	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment: %v", err)
	}

	stageBranch := model.QAStageBranchName(task.ID)
	prBranch := task.BranchName()
	sha := commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/calc/sum.go",
		"package calc\nfunc Sum(a, b int) int { return a + b }\n",
		"Add Sum helper\n\nImplements integer addition.")

	// Before orchestrator.OpenPullRequestForTaskWithQA, the real PR branch does not exist on GitHub.
	if exists, _ := client.BranchExists(repo.Owner, repo.Name, prBranch); exists {
		t.Fatalf("expected PR branch %s not to exist before QA promotion", prBranch)
	}

	status, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, task, orchestrator.QAOpenPROptions{})
	if err != nil {
		t.Fatalf("orchestrator.OpenPullRequestForTaskWithQA: %v", err)
	}
	if status.PullRequest.Number == 0 || len(sim.PullRequests) != 1 {
		t.Fatalf("expected 1 opened PR on T1 after promotion, got status=%+v prs=%+v", status, sim.PullRequests)
	}
	head, err := client.GetBranchHead(repo.Owner, repo.Name, prBranch)
	if err != nil || head == nil || head.SHA != sha {
		t.Fatalf("promoted PR branch head = %+v (err=%v), want SHA %s", head, err, sha)
	}
	if stageStillThere, _ := client.BranchExists(repo.Owner, repo.Name, stageBranch); stageStillThere {
		t.Fatalf("expected temporary staging branch %s to be deleted after promotion", stageBranch)
	}
}

func TestQATreatmentT2AlreadyOpenPRNeverLeaksUnverifiedStageAndRunsBoundedS4(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qat2", repo)

	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment: %v", err)
	}

	dataDir := t.TempDir()
	s4Calls := 0
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
		VerifyS4: func(_ context.Context, _ model.Task, _ string) (string, int64, int64, error) {
			s4Calls++
			// Emit 1 valid grounded defect on line 4 of pkg/staterepo/loader.go + 1 theater nit.
			return `[
				{
					"file": "pkg/staterepo/loader.go",
					"line": 4,
					"category": "logic_bug",
					"trigger_scenario": "When input slice is empty, ` + "`LoadItems`" + ` panics with index 0 out of range.",
					"fix_hint": "Guard ` + "`len(items) == 0`" + ` before indexing ` + "`items[0]`" + `."
				},
				{
					"file": "pkg/staterepo/loader.go",
					"line": 2,
					"category": "nit",
					"theater_tag": "PEDANTIC_ESCALATION",
					"trigger_scenario": "Style suggestion that Inquisitor must filter out.",
					"fix_hint": "Ignore."
				}
			]`, 320, 95, nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	// Suppose PR #1 is already open on prBranch at sha1.
	prBranch := task.BranchName()
	sha1 := commitAndPushFile(t, sim.BareRepo, prBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string { return \"ok\" }\n",
		"Initial loader\n\nSafe baseline.")
	initialPR, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil || initialPR.PullRequest.Number == 0 {
		t.Fatalf("initial OpenPullRequestForTask: %v", err)
	}

	// Turn 2 pushes unverified T2 commit sha2 to grain-qa-stage/qat2 and calls orchestrator.OpenPullRequestForTaskWithQA.
	stageBranch := model.QAStageBranchName(task.ID)
	sha2 := commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string {\n\tfirst := items[0]\n\treturn first\n}\n",
		"Update loader\n\nReads first element.")

	_ = sha2
	deferredStatus, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, task, orchestrator.QAOpenPROptions{})
	if err != nil {
		t.Fatalf("orchestrator.OpenPullRequestForTaskWithQA T2 defer: %v", err)
	}
	if deferredStatus.PullRequest.Number != 0 || !strings.Contains(deferredStatus.Note, "S4 verification") {
		t.Fatalf("expected deferred S4 note without promoting PR branch, got %+v", deferredStatus)
	}
	// Live PR branch on GitHub MUST still point at sha1 (zero unverified commit leakage!).
	liveHead, _ := client.GetBranchHead(repo.Owner, repo.Name, prBranch)
	if liveHead == nil || liveHead.SHA != sha1 {
		t.Fatalf("live PR branch leaked unverified commit: got %+v, want %s", liveHead, sha1)
	}

	// Now finish/salvage runs after coding turn ends -> runs S3 + 1-round S4 + Inquisitor -> requeues 1 repair!
	now := time.Date(2026, 11, 12, 21, 0, 0, 0, time.UTC)
	salvaged, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now)
	if err != nil || !salvaged {
		t.Fatalf("SalvagePushedBranch(round 1) = (%v, %v), want (true, nil)", salvaged, err)
	}
	if s4Calls != 1 {
		t.Fatalf("s4Calls = %d, want 1", s4Calls)
	}
	// Live PR branch STILL stays at sha1 while repair is queued!
	liveHeadAfterS4, _ := client.GetBranchHead(repo.Owner, repo.Name, prBranch)
	if liveHeadAfterS4 == nil || liveHeadAfterS4.SHA != sha1 {
		t.Fatalf("live PR branch moved despite S4 defect finding: got %+v, want %s", liveHeadAfterS4, sha1)
	}
	obs, _ := store.GetObservation(ctx, task.ID)
	if obs == nil || obs.RetryRequestedAt == nil || obs.CompletedAt != nil {
		t.Fatalf("expected task queued for 1 bounded repair turn, got obs=%+v", obs)
	}

	// Repair turn pushes fixed sha3 to stageBranch and finishes: S4 does NOT run a second time (s4Calls stays 1)
	// and sha3 promotes cleanly to prBranch!
	sha3 := commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string {\n\tif len(items) == 0 { return \"\" }\n\treturn items[0]\n}\n",
		"Fix empty slice check\n\nGuards empty slice.")
	salvaged2, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now.Add(time.Minute))
	if err != nil || !salvaged2 {
		t.Fatalf("SalvagePushedBranch(round 2) = (%v, %v), want (true, nil)", salvaged2, err)
	}
	if s4Calls != 1 {
		t.Fatalf("expected hard 1-round S4 cap (s4Calls=1), got %d", s4Calls)
	}
	promotedHead, _ := client.GetBranchHead(repo.Owner, repo.Name, prBranch)
	if promotedHead == nil || promotedHead.SHA != sha3 {
		t.Fatalf("expected promoted PR branch at %s, got %+v", sha3, promotedHead)
	}
}

func TestQASalvageS3FailureRequeuesOnceThenHoldsWithGateQA(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qahold", repo)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowDeterministicS3Only,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	stageBranch := model.QAStageBranchName(task.ID)
	badTest := "package config\nimport \"testing\"\nfunc TestParseNoAssertions(t *testing.T) {}\n"
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/config/parse_test.go", badTest, "Add assertion-free test\n\nBody.")

	now := time.Date(2026, 11, 12, 21, 10, 0, 0, time.UTC)
	// First exit without open_pull_request: requeues 1 repair without opening a GitHub PR.
	if _, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now); err != nil {
		t.Fatalf("SalvagePushedBranch 1: %v", err)
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected 0 PRs opened before repair retry, got %+v", sim.PullRequests)
	}

	// Second exit still failing S3: retry cap reached -> opens PR held by GateQA with AutoMerge=false!
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/config/parse_test.go", badTest+"\n// still no assertions\n", "Second attempt\n\nBody.")
	if _, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now.Add(time.Minute)); err != nil {
		t.Fatalf("SalvagePushedBranch 2: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected 1 held PR after retry cap, got %+v", sim.PullRequests)
	}
	reloaded, err := store.GetTask(ctx, task.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reloaded.AutoMerge {
		t.Fatal("expected AutoMerge withdrawn on unresolved GateQA hold")
	}
	obs, _ := store.GetObservation(ctx, task.ID)
	gates := model.ComputeMergeGates(*reloaded, obs, nil)
	foundQAGate := false
	for _, g := range gates {
		if g.ID == model.GateQA && g.State == model.GateStateFindings {
			foundQAGate = true
		}
	}
	if !foundQAGate {
		t.Fatalf("expected GateQA in GateStateFindings, got gates=%+v", gates)
	}
}

func TestQARunCyclePromptAndCheckoutUseStagingBranchOnTreatment(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	remoteBase := t.TempDir()
	dst := filepath.Join(remoteBase, repo.Owner, repo.Name+".git")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sim.BareRepo, dst); err != nil {
		t.Fatal(err)
	}

	task := filedTask(t, ctx, store, "qacycle", repo)
	stageBranch := model.QAStageBranchName(task.ID)

	var sawPrompt string
	var sawRequireOpenPR bool
	fw := openingFramework{agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		sawPrompt = cfg.Prompt
		sawRequireOpenPR = cfg.RequireOpenPullRequest
		wd := filepath.Join(cfg.SandboxRoot, "work")
		if err := os.WriteFile(filepath.Join(wd, "hello.txt"), []byte("clean\n"), 0o644); err != nil {
			return nil, err
		}
		for _, args := range [][]string{
			{"-C", wd, "config", "user.email", "agent@example.com"},
			{"-C", wd, "config", "user.name", "agent"},
			{"-C", wd, "add", "hello.txt"},
			{"-C", wd, "commit", "-q", "-m", "Add hello\n\nBody."},
			{"-C", wd, "push", "-q", "origin", task.BranchName() + ":" + stageBranch},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
			}
		}
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "open_pull_request"},
		}}, nil
	})}

	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Config: orchestrator.Config{
			GitRemoteBase: remoteBase,
			QAProfiler:    qa.NewProfiler(filepath.Join(t.TempDir(), "qa.jsonl")),
		},
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if !strings.Contains(sawPrompt, stageBranch) || !strings.Contains(sawPrompt, "## Pre-PR Quality Contract") {
		t.Fatalf("expected treatment prompt to name %s and include SKILL.md, got prompt excerpt: %.400s", stageBranch, sawPrompt)
	}
	if !sawRequireOpenPR {
		t.Fatal("expected RunConfig.RequireOpenPullRequest = true on treatment run")
	}
	if len(sim.PullRequests) != 1 || sim.PullRequests[0].Head != task.BranchName() {
		t.Fatalf("expected promoted PR on %s, got %+v", task.BranchName(), sim.PullRequests)
	}
}

func TestQATreatmentS3RetryCapHoldsPRAndPreservesOwnerRepoWorkOrigin(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	if got := model.ResolveQARootOrigin("acme/widgets#12", "task-99"); got != "acme/widgets#12" {
		t.Fatalf("ResolveQARootOrigin(owner/repo#N) = %q, want %q", got, "acme/widgets#12")
	}

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qacap", repo)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		tk.WorkOrigin = "acme/widgets#12"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := store.GetTask(ctx, task.ID)
	task = *fresh

	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "acme/widgets#12",
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowDeterministicS3Only,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	stageBranch := model.QAStageBranchName(task.ID)
	badTest := "package config\nimport \"testing\"\nfunc TestParseNoAssertions(t *testing.T) {}\n"
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/config/parse_test.go", badTest, "Add assertion-free test\n\nBody.")

	// Attempt < 3 (AllowHoldAfterS3Cap=false) must refuse PR creation and leave stage unpromoted.
	if _, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, task, orchestrator.QAOpenPROptions{
		AllowHoldAfterS3Cap: false,
	}); err == nil {
		t.Fatal("expected orchestrator.OpenPullRequestForTaskWithQA to fail before S3 retry cap")
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected 0 PRs before S3 retry cap, got %+v", sim.PullRequests)
	}

	// Attempt >= 3 (AllowHoldAfterS3Cap=true) promotes onto a held PR with AutoMerge stripped and GateQA findings.
	status, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, task, orchestrator.QAOpenPROptions{
		AllowHoldAfterS3Cap: true,
	})
	if err != nil || status.PullRequest.Number == 0 {
		t.Fatalf("orchestrator.OpenPullRequestForTaskWithQA(AllowHoldAfterS3Cap=true) = (%+v, %v)", status, err)
	}
	reloaded, _ := store.GetTask(ctx, task.ID)
	if reloaded.AutoMerge {
		t.Fatal("expected AutoMerge withdrawn when PR opened under S3 retry cap")
	}
	obs, _ := store.GetObservation(ctx, task.ID)
	gates := model.ComputeMergeGates(*reloaded, obs, nil)
	foundQA := false
	for _, g := range gates {
		if g.ID == model.GateQA && g.State == model.GateStateFindings {
			foundQA = true
		}
	}
	if !foundQA {
		t.Fatalf("expected GateQA in GateStateFindings on held PR, got %+v", gates)
	}
}

func TestQAStageBranchLeakIsEliminated(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qatleak", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment: %v", err)
	}

	stageBranch := model.QAStageBranchName(task.ID)
	// Simulate the agent pushing to the staging branch.
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/config/parse.go", "package config\n", "fix the thing")

	// Set the history.PushBranch which the agent would have set.

	// Promote and open PR.
	status, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, task, orchestrator.QAOpenPROptions{})
	if err != nil {
		t.Fatalf("orchestrator.OpenPullRequestForTaskWithQA: %v", err)
	}
	if status.PullRequest.Number == 0 {
		t.Fatalf("expected PR to be opened")
	}

	if len(sim.PullRequests) == 0 {
		t.Fatalf("expected PR in sim")
	}
	pr := sim.PullRequests[0]
	if strings.Contains(pr.Head, "grain-qa-stage") {
		t.Fatalf("PR head leaks grain-qa-stage: %s", pr.Head)
	}
	if strings.Contains(pr.Title, "grain-qa-stage") || strings.Contains(pr.Body, "grain-qa-stage") {
		t.Fatalf("PR metadata leaks grain-qa-stage: title=%q, body=%q", pr.Title, pr.Body)
	}

	// Also ensure no commits in the PR have grain-qa-stage.
	commits, err := client.ListPullRequestCommits(repo.Owner, repo.Name, pr.Number)
	if err != nil {
		t.Fatalf("ListPullRequestCommits: %v", err)
	}
	for _, c := range commits {
		if strings.Contains(c.Message, "grain-qa-stage") {
			t.Fatalf("Commit message leaks grain-qa-stage: %q", c.Message)
		}
	}
}

func TestQASalvagePromotesDisabledQA(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qatsalvage", repo)

	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment: %v", err)
	}

	stageBranch := model.QAStageBranchName(task.ID)
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/config/parse.go", "package config\n", "fix the thing")

	// Disable QA
	enableTestQA(t, ctx, store, 0)

	handled, err := orchestrator.SalvageQAStageIfPresent(ctx, store, client, task, time.Now())
	if err != nil {
		t.Fatalf("salvageQAStageIfPresent: %v", err)
	}
	if handled {
		t.Fatalf("expected handled=false, got true")
	}

	// Verify it was promoted.
	promotedHead, err := client.GetBranchHead(repo.Owner, repo.Name, task.BranchName())
	if err != nil || promotedHead == nil {
		t.Fatalf("expected promoted PR branch at %s, got error %v", task.BranchName(), err)
	}
}

func TestMaybeAutoDowngradeQARolloutTripsCircuitBreakerToZeroShadowMode(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 50)
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	// Below MinTreatmentSampleForAutoDowngrade (15), circuit breaker must be a no-op.
	downgraded, reasons, err := orchestrator.MaybeAutoDowngradeQARollout(ctx, store, now)
	if err != nil || downgraded || len(reasons) != 0 {
		t.Fatalf("below sample floor: got (%v, %v, %v), want (false, nil, nil)", downgraded, reasons, err)
	}

	// Seed 16 Treatment WorkOrigins and cast 14 Not-Helpful (-1) operator votes on a noisy rule
	// (Wilson 95% LB > 0.35). Under V2/V3, operator vote heuristics are notify-only until
	// blind-escape mSPRT harm (alpha = 0.01) crosses its sequential boundary.
	var treatTaskIDs []string
	for i := 0; i < 16; i++ {
		tk := filedTask(t, ctx, store, fmt.Sprintf("cb-%02d", i), repo)
		treatTaskIDs = append(treatTaskIDs, tk.ID)
		origin := fmt.Sprintf("ws:cb-%02d", i)
		if err := store.UpdateTask(ctx, tk.ID, func(task *model.Task) error {
			task.WorkOrigin = model.WorkOrigin(origin)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
			RootOrigin:         origin,
			Epoch:              1,
			RegimeID:           1,
			Arm:                model.QAArmTreatment,
			FlowID:             model.QAFlowDeterministicS3Only,
			RolloutPctAtAssign: 50,
			AssignedAt:         now.Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		var findings []model.QAFinding
		if i < 14 {
			findings = []model.QAFinding{{
				TaskID:    tk.ID,
				Stage:     "S3",
				RuleID:    "noisy-rule",
				Severity:  "error",
				FilePath:  "pkg/foo.go",
				Line:      10,
				Summary:   "synthetic false positive",
				CreatedAt: now.Add(-5 * time.Minute),
			}}
		}
		if _, err := store.RecordQACheckRun(ctx, model.QACheckRun{
			TaskID:    tk.ID,
			RunID:     fmt.Sprintf("run-%02d", i),
			CommitSHA: fmt.Sprintf("deadbeef%02d", i),
			Stage:     "S3",
			CheckID:   "noisy-rule",
			Arm:       model.QAArmTreatment,
			Shadow:    false,
			Verdict:   "fail",
			CreatedAt: now.Add(-5 * time.Minute),
		}, findings); err != nil {
			t.Fatal(err)
		}
	}
	allFindings, err := store.ListAllQAFindings(ctx)
	if err != nil || len(allFindings) != 14 {
		t.Fatalf("ListAllQAFindings = (%d, %v), want 14", len(allFindings), err)
	}
	for _, f := range allFindings {
		if err := store.VoteQAFinding(ctx, f.ID, -1, "operator@example.com", now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	// Notify-only votes alone must NOT auto-downgrade.
	downgradedVotesOnly, _, err := orchestrator.MaybeAutoDowngradeQARollout(ctx, store, now)
	if err != nil || downgradedVotesOnly {
		t.Fatalf("votes-only heuristic must not auto-downgrade, got (%v, %v)", downgradedVotesOnly, err)
	}

	// Seed 16 Control WorkOrigins (0 blind escapes) and attach post-PR blind escapes to 14/16
	// Treatment tasks so one-sided sequential mSPRT (alpha = 0.01) trips automatic downgrade.
	for i := 0; i < 16; i++ {
		ctrlTk := filedTask(t, ctx, store, fmt.Sprintf("cb-ctrl-%02d", i), repo)
		ctrlOrigin := fmt.Sprintf("ws:cb-ctrl-%02d", i)
		if err := store.UpdateTask(ctx, ctrlTk.ID, func(task *model.Task) error {
			task.WorkOrigin = model.WorkOrigin(ctrlOrigin)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
			RootOrigin:         ctrlOrigin,
			Epoch:              1,
			RegimeID:           1,
			Arm:                model.QAArmControl,
			FlowID:             model.QAFlowDeterministicS3Only,
			RolloutPctAtAssign: 50,
			AssignedAt:         now.Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if i < 14 {
			if err := store.UpdateTask(ctx, treatTaskIDs[i], func(task *model.Task) error {
				task.Links = append(task.Links, model.Link{
					Kind:   model.LinkDraftReview,
					Target: fmt.Sprintf("blind-rev-%02d", i),
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	downgraded, reasons, err = orchestrator.MaybeAutoDowngradeQARollout(ctx, store, now)
	if err != nil {
		t.Fatalf("MaybeAutoDowngradeQARollout error: %v", err)
	}
	if !downgraded || len(reasons) == 0 {
		t.Fatalf("expected auto-downgrade with reasons, got (%v, %v)", downgraded, reasons)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.QA.Enabled || cfg.QA.RolloutPct != 0 || !cfg.QA.ShadowOnControlEnabled() || cfg.QA.RegimeID < 2 {
		t.Fatalf("post-downgrade QAConfig = %+v, want Enabled=true RolloutPct=0 ShadowOnControl=true RegimeID>=2", cfg.QA)
	}
	audits, err := store.ConfigAuditOfSurface(ctx, "settings", 5)
	if err != nil || len(audits) == 0 || !strings.Contains(audits[0].Summary, "auto-downgraded quality control rollout") {
		t.Fatalf("expected config_audit entry for auto-downgrade, got %+v (err=%v)", audits, err)
	}

	// Idempotent second call once RolloutPct == 0.
	downgraded2, _, err := orchestrator.MaybeAutoDowngradeQARollout(ctx, store, now)
	if err != nil || downgraded2 {
		t.Fatalf("second MaybeAutoDowngradeQARollout = (%v, %v), want (false, nil)", downgraded2, err)
	}

	// Re-enabling rollout in a new regime (Regime 3 at 25%) within Epoch 1 must NOT be immediately
	// re-tripped by Regime 1's historical votes before Regime 3 accumulates its own 15 Treatment origins.
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		nextQA, advErr := model.AdvanceQAConfig(c.QA, model.QAConfig{
			Enabled:         true,
			FlowID:          model.QAFlowGoldenV1Minimal,
			RolloutPct:      25,
			ShadowOnControl: model.BoolPtr(true),
			Epoch:           c.QA.EpochOrDefault(),
		})
		if advErr != nil {
			return advErr
		}
		c.QA = nextQA
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	downgraded3, _, err := orchestrator.MaybeAutoDowngradeQARollout(ctx, store, now.Add(time.Minute))
	if err != nil || downgraded3 {
		t.Fatalf("fresh Regime 3 was prematurely re-downgraded by Regime 1 history: (%v, %v)", downgraded3, err)
	}
	afterReenable, err := store.GetConfig(ctx)
	if err != nil || afterReenable.QA.RolloutPct != 25 || afterReenable.QA.RegimeID < 3 {
		t.Fatalf("expected RolloutPct=25 in Regime>=3 after operator re-enable, got %+v (err=%v)", afterReenable.QA, err)
	}
}

type countingGitHubClient struct {
	github.Client
	branchHeadCalls int
}

func (c *countingGitHubClient) GetBranchHead(owner, repo, branch string) (*github.BranchHead, error) {
	c.branchHeadCalls++
	return c.Client.GetBranchHead(owner, repo, branch)
}

func TestSweepStrandedQAStagesSkipsClosedUntreatedAndAlreadySweptTasks(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)

	sim, baseClient := newSim(t, "acme", "widgets", "main")
	client := &countingGitHubClient{Client: baseClient}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// 1. 10 closed tasks + 10 open untreated/control tasks must never hit GitHub GetBranchHead.
	for i := 0; i < 10; i++ {
		closedTask := filedTask(t, ctx, store, fmt.Sprintf("closed-%d", i), repo)
		closedAt := now
		if err := store.ObserveField(ctx, closedTask.ID, now, func(obs *model.Observation) {
			obs.ClosedAt = &closedAt
		}); err != nil {
			t.Fatal(err)
		}
		_ = filedTask(t, ctx, store, fmt.Sprintf("untreated-%d", i), repo)
	}

	// 2. 1 open treatment task with a stranded staging branch promotes once when QA is disabled, then caches.
	stranded := filedTask(t, ctx, store, "stranded-treat", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(stranded.WorkOrigin, stranded.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}
	commitAndPushFile(t, sim.BareRepo, model.QAStageBranchName(stranded.ID), "pkg/a/a.go", "package a\n", "feat: staged")

	// While QA is active, sweep makes 0 GitHub GetBranchHead calls.
	if err := orchestrator.SweepStrandedQAStagesForTest(ctx, store, client, now); err != nil {
		t.Fatal(err)
	}
	if client.branchHeadCalls != 0 {
		t.Fatalf("expected 0 GetBranchHead calls while QA active, got %d", client.branchHeadCalls)
	}

	// Disable master switch -> sweep promotes the 1 stranded task and skips all 20 closed/untreated tasks.
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.QA.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SweepStrandedQAStagesForTest(ctx, store, client, now); err != nil {
		t.Fatal(err)
	}
	promotedCalls := client.branchHeadCalls
	if promotedCalls == 0 || promotedCalls > 3 {
		t.Fatalf("expected <= 3 GetBranchHead calls for the 1 stranded task (not 21+), got %d", promotedCalls)
	}
	head, err := baseClient.GetBranchHead(repo.Owner, repo.Name, stranded.BranchName())
	if err != nil || head == nil {
		t.Fatalf("expected stranded branch promoted to %s, got %v", stranded.BranchName(), err)
	}

	// Subsequent ticks make 0 additional GetBranchHead calls.
	if err := orchestrator.SweepStrandedQAStagesForTest(ctx, store, client, now); err != nil {
		t.Fatal(err)
	}
	if client.branchHeadCalls != promotedCalls {
		t.Fatalf("expected 0 additional GetBranchHead calls on second sweep tick, got %d -> %d", promotedCalls, client.branchHeadCalls)
	}
}

func TestQACustomStagesExecutionAndExecInSandboxWiring(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC)

	customStagesYAML := `schema: grain.dev/qa-flow/v4
key: v4-custom-stages
extends: golden-v2@1
label: v4 custom stages
summary: Tests custom stages and ExecInSandbox wiring
skill-steps:
  - id: skill:custom-critic
    stage: coding
    enabled: true
    text: Verify schema migrations accompany schema edits.
stages:
  custom:
    - id: custom:s3b-batch
      label: Companion migration batch
      insert-after: s3
      kind: deterministic_batch
      when-paths: ["pkg/model/**"]
      min-tier: T1
      timeout-seconds: 20
      check-ids: ["custom:schema-needs-migration"]
    - id: custom:s3c-critic
      label: Isolated critic stage
      insert-after: s3
      kind: isolated_critic
      skill-ref: skill:custom-critic
      when-paths: ["pkg/critic/**"]
      min-tier: T1
      timeout-seconds: 20
    - id: custom:s4b-human-gate
      label: Human gate on billing paths
      insert-after: s4
      kind: human_gate
      when-paths: ["pkg/billing/**"]
      min-tier: T1
      timeout-seconds: 20
checks:
  golden:mutation-hardening:
    enabled: true
    severity: warn
    params:
      mutants: 2
      max-seconds: 10
custom-checks:
  - id: custom:schema-needs-migration
    label: Schema edit requires migration
    kind: companion
    severity: blocking
    when-changed: ["pkg/model/schema.go"]
    require-changed: ["pkg/model/migrations.go"]
    message: Changing pkg/model/schema.go requires pkg/model/migrations.go.
    fixtures:
      fail:
        - name: schema alone
          changed-files: ["pkg/model/schema.go"]
      pass:
        - name: both
          changed-files: ["pkg/model/schema.go", "pkg/model/migrations.go"]
verifier:
  enabled: true
  tiers: [T2, T3]
  max-repair-rounds: 0
`
	bundle, err := qa.ResolveAnyFlowDeclaration([]byte(customStagesYAML), 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration: %v", err)
	}
	if err := store.InsertQAFlowVersion(ctx, model.QAFlowVersion{
		Hash:        bundle.Hash,
		Key:         bundle.Key,
		Version:     bundle.Version,
		Label:       bundle.Label,
		QAEngineRev: bundle.QAEngineRev,
		SourceYAML:  bundle.SourceYAML,
		SpecJSON:    bundle.SpecJSON,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("InsertQAFlowVersion: %v", err)
	}

	dataDir := t.TempDir()
	execInSandboxCalls := 0
	var criticPrompts []string
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
		ExecInSandbox: func(_ context.Context, _ string, _ []string, _ time.Duration) (string, int, error) {
			execInSandboxCalls++
			// Simulate surviving mutant (exitCode 0 on mutated test run).
			return "PASS\n", 0, nil
		},
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, runPrefix, prompt string, _ int) (string, int64, int64, error) {
			if strings.Contains(runPrefix, "custom:s3c-critic") {
				criticPrompts = append(criticPrompts, prompt)
				return `[{"file":"pkg/critic/handler.go","line":2,"category":"logic_bug","trigger_scenario":"Unchecked nil request in handler","fix_hint":"Check req != nil"}]`, 80, 30, nil
			}
			return "[]", 10, 5, nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	// 1. deterministic_batch custom stage blocks PR when custom:schema-needs-migration fails.
	taskBatch := filedTask(t, ctx, store, "qacust-batch", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(taskBatch.WorkOrigin, taskBatch.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             bundle.Key,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}
	commitAndPushFile(t, sim.BareRepo, model.QAStageBranchName(taskBatch.ID), "pkg/model/schema.go",
		"package model\nfunc Touch(a, b int) bool { return a == b }\n",
		"Update schema\n\nBody.")
	if _, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, taskBatch, orchestrator.QAOpenPROptions{}); err == nil {
		t.Fatalf("expected deterministic_batch custom stage to block PR when companion migration is missing")
	}
	if execInSandboxCalls == 0 {
		t.Fatalf("expected ExecInSandbox to be invoked by mutation hardening during runMirrorS3WithWorkspace")
	}

	// 2. isolated_critic custom stage runs RunIsolatedPrompt with SkillRef and blocks when findings are returned.
	taskCritic := filedTask(t, ctx, store, "qacust-critic", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(taskCritic.WorkOrigin, taskCritic.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             bundle.Key,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}
	commitAndPushFile(t, sim.BareRepo, model.QAStageBranchName(taskCritic.ID), "pkg/critic/handler.go",
		"package critic\nfunc Handle() string { return \"ok\" }\n",
		"Add critic handler\n\nBody.")
	if _, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, taskCritic, orchestrator.QAOpenPROptions{}); err == nil {
		t.Fatalf("expected isolated_critic custom stage to block PR on critic finding")
	}
	if len(criticPrompts) == 0 || !strings.Contains(criticPrompts[0], "Verify schema migrations accompany schema edits.") {
		t.Fatalf("expected isolated_critic prompt to include resolved SkillRef text, got %+v", criticPrompts)
	}
	criticFindings, _ := store.ListQAFindingsForTask(ctx, taskCritic.ID)
	foundCritic := false
	for _, f := range criticFindings {
		if f.Stage == "custom:s3c-critic" && strings.Contains(f.Summary, "Unchecked nil request") {
			foundCritic = true
		}
	}
	if !foundCritic {
		t.Fatalf("expected recorded QAFinding for custom:s3c-critic, got %+v", criticFindings)
	}

	// 3. human_gate custom stage sets LinkQATriageHold when WhenPaths/MinTier match.
	taskGate := filedTask(t, ctx, store, "qacust-gate", repo)
	_ = store.UpdateTask(ctx, taskGate.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	})
	freshGate, _ := store.GetTask(ctx, taskGate.ID)
	taskGate = *freshGate
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(taskGate.WorkOrigin, taskGate.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             bundle.Key,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}
	commitAndPushFile(t, sim.BareRepo, model.QAStageBranchName(taskGate.ID), "pkg/billing/invoice.go",
		"package billing\nfunc Total() int { return 42 }\n",
		"Add billing total\n\nBody.")
	if _, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, client, taskGate, orchestrator.QAOpenPROptions{}); err != nil {
		t.Fatalf("OpenPullRequestForTaskWithQA on human_gate task: %v", err)
	}
	afterGate, _ := store.GetTask(ctx, taskGate.ID)
	hasTriageHold := false
	for _, l := range afterGate.Links {
		if l.Kind == model.LinkQATriageHold {
			hasTriageHold = true
		}
	}
	if !hasTriageHold || afterGate.AutoMerge {
		t.Fatalf("expected human_gate custom stage to set LinkQATriageHold and withdraw AutoMerge, got links=%+v AutoMerge=%v",
			afterGate.Links, afterGate.AutoMerge)
	}
}

func TestQAS4VerifierSkillSecuritySkillMaxTokensAndTwoRepairRounds(t *testing.T) {
	store, ctx := openStore(t)
	enableTestQA(t, ctx, store, 100)

	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "qas4-r2", repo)
	now := time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)

	s4YAML := `schema: grain.dev/qa-flow/v4
key: v4-s4-r2
extends: golden-v2@1
label: v4 s4 two rounds
summary: Tests S4 verifier/security skills, MaxS4Tokens, and MaxS4RepairRounds=2
skills:
  verifier:
    mode: replace
    max-bytes: 1200
    text: "VERIFIER_SKILL_MARKER: verify slice bounds before indexing."
  security:
    mode: replace
    max-bytes: 1200
    text: "SECURITY_SKILL_MARKER: verify auth token audience."
verifier:
  enabled: true
  tiers: [T1, T2, T3]
  max-repair-rounds: 2
  max-tokens: 42000
`
	bundle, err := qa.ResolveAnyFlowDeclaration([]byte(s4YAML), 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration: %v", err)
	}
	if err := store.InsertQAFlowVersion(ctx, model.QAFlowVersion{
		Hash:        bundle.Hash,
		Key:         bundle.Key,
		Version:     bundle.Version,
		Label:       bundle.Label,
		QAEngineRev: bundle.QAEngineRev,
		SourceYAML:  bundle.SourceYAML,
		SpecJSON:    bundle.SpecJSON,
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task.WorkOrigin, task.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             bundle.Key,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	s4Calls := 0
	var sawPrompts []string
	var sawMaxTokens []int
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, runPrefix, prompt string, maxTokens int) (string, int64, int64, error) {
			if strings.HasSuffix(runPrefix, "-qa-s4") {
				s4Calls++
				sawPrompts = append(sawPrompts, prompt)
				sawMaxTokens = append(sawMaxTokens, maxTokens)
				return `[{
					"file": "pkg/staterepo/loader.go",
					"line": 3,
					"category": "logic_bug",
					"trigger_scenario": "Empty items slice causes index out of range panic.",
					"fix_hint": "Check len(items) == 0."
				}]`, 200, 60, nil
			}
			return "[]", 0, 0, nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	stageBranch := model.QAStageBranchName(task.ID)

	// Round 1 commit -> S4 runs (call #1) and requests repair.
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string {\n\treturn items[0]\n}\n",
		"Round 1 loader\n\nBody.")
	salvaged1, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now)
	if err != nil || !salvaged1 || s4Calls != 1 {
		t.Fatalf("round 1: salvaged=%v err=%v s4Calls=%d (want true, nil, 1)", salvaged1, err, s4Calls)
	}
	if !strings.Contains(sawPrompts[0], "VERIFIER_SKILL_MARKER") || !strings.Contains(sawPrompts[0], "SECURITY_SKILL_MARKER") {
		t.Fatalf("expected S4 prompt to include VerifierSkillText and SecuritySkillText, got:\n%s", sawPrompts[0])
	}
	if sawMaxTokens[0] != 42000 {
		t.Fatalf("expected MaxS4Tokens=42000 passed to RunIsolatedPrompt, got %d", sawMaxTokens[0])
	}

	// Round 2 commit -> S4 runs a second time (call #2) because MaxS4RepairRounds=2, and requests repair.
	commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string {\n\treturn items[0] // round 2\n}\n",
		"Round 2 loader\n\nBody.")
	salvaged2, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now.Add(time.Minute))
	if err != nil || !salvaged2 || s4Calls != 2 {
		t.Fatalf("round 2: salvaged=%v err=%v s4Calls=%d (want true, nil, 2)", salvaged2, err, s4Calls)
	}

	// Round 3 commit -> S4 repair cap (2) reached, so S4 does NOT run a 3rd time; branch promotes and opens PR!
	sha3 := commitAndPushFile(t, sim.BareRepo, stageBranch, "pkg/staterepo/loader.go",
		"package staterepo\nfunc LoadItems(items []string) string {\n\tif len(items) == 0 { return \"\" }\n\treturn items[0]\n}\n",
		"Round 3 loader\n\nBody.")
	salvaged3, err := orchestrator.SalvagePushedBranch(ctx, store, client, task, now.Add(2*time.Minute))
	if err != nil || !salvaged3 {
		t.Fatalf("round 3: salvaged=%v err=%v", salvaged3, err)
	}
	if s4Calls != 2 {
		t.Fatalf("expected S4 not to run after 2 repair rounds, got s4Calls=%d", s4Calls)
	}
	liveHead, _ := client.GetBranchHead(repo.Owner, repo.Name, task.BranchName())
	if liveHead == nil || liveHead.SHA != sha3 {
		t.Fatalf("expected round 3 commit %s promoted to %s, got %+v", sha3, task.BranchName(), liveHead)
	}
}

func TestEvaluateAndApplyQATriageSafetyFloors(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	_ = sim
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)

	enforcedSpec := &qa.FlowSpec{
		ID: "v4-triage-floors",
		S5MergeTriage: qa.TriageConfig{
			Mode:              "enforced",
			AutoMergeTiers:    []string{"T0", "T1"},
			RequireHumanTiers: []string{"T2", "T3"},
		},
	}
	assign := &resolve.Resolution{
		ExperimentID: "exp-floors",
		ArmID:        "v4-triage-floors",
		FlowHash:     "sha256:floors",
		Spec:         enforcedSpec,
		QAEngineRev:  qa.QAEngineRev,
	}

	// Sync repo merge rules (human-only on pkg/release/**) and gating path trigger (on pkg/gated/**).
	if _, err := store.SyncRepoDeclarations(ctx, repo, "base-sha", model.RepoDeclarations{
		MergeRules: &model.MergeRules{
			Path: model.MergeRulesPath,
			Rules: []model.MergeRule{
				{
					ID:          "release-human-only",
					Paths:       []string{"pkg/release/**"},
					MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever},
				},
			},
		},
		Triggers: []model.PathTrigger{
			{
				Name:     "gated-trigger",
				Path:     ".grain/triggers/gated.yml",
				Paths:    []string{"pkg/gated/**"},
				Gate:     true,
				Template: model.TemplateNone,
			},
		},
	}, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	// 1. MergeRulesRequireHuman floor blocks auto-merge even on a T1 path.
	tMergeRule := filedTask(t, ctx, store, "floor-mr", repo)
	_ = store.UpdateTask(ctx, tMergeRule.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	})
	orchestrator.EvaluateAndApplyQATriage(ctx, store, client, tMergeRule, assign, "aaaa1111", []string{"pkg/release/version.go"}, true)
	gotMR, _ := store.GetTask(ctx, tMergeRule.ID)
	if gotMR.AutoMerge {
		t.Fatalf("expected MergeRulesRequireHuman floor to withdraw AutoMerge on pkg/release/version.go")
	}

	// 2. GateTriggerMatched floor blocks auto-merge even on a T1 path.
	tGateTrig := filedTask(t, ctx, store, "floor-trig", repo)
	_ = store.UpdateTask(ctx, tGateTrig.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	})
	orchestrator.EvaluateAndApplyQATriage(ctx, store, client, tGateTrig, assign, "bbbb2222", []string{"pkg/gated/feature.go"}, true)
	gotTrig, _ := store.GetTask(ctx, tGateTrig.ID)
	if gotTrig.AutoMerge {
		t.Fatalf("expected GateTriggerMatched floor to withdraw AutoMerge on pkg/gated/feature.go")
	}

	// 3. CIRedOrPending floor blocks auto-merge when Observation.CI is failing/pending.
	tCIRed := filedTask(t, ctx, store, "floor-ci", repo)
	_ = store.UpdateTask(ctx, tCIRed.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	})
	_ = store.ObserveField(ctx, tCIRed.ID, now, func(obs *model.Observation) {
		obs.CI = &model.CIStatus{
			RunID:      "ci-run-1",
			FinishedAt: &now,
			Conclusion: "failure",
		}
	})
	orchestrator.EvaluateAndApplyQATriage(ctx, store, client, tCIRed, assign, "cccc3333", []string{"pkg/leaf/math.go"}, true)
	gotCI, _ := store.GetTask(ctx, tCIRed.ID)
	if gotCI.AutoMerge {
		t.Fatalf("expected CIRedOrPending floor to withdraw AutoMerge when CI conclusion is failure")
	}
}

type failingDefaultBranchClient struct {
	github.Client
	failDefaultBranch bool
}

func (f failingDefaultBranchClient) DefaultBranch(owner, repo string) (string, error) {
	if f.failDefaultBranch {
		return "", errors.New("forge api repos/" + owner + "/" + repo + ": context deadline exceeded")
	}
	return f.Client.DefaultBranch(owner, repo)
}

func TestQABaseResolutionMergeBaseWithoutRESTAndOperatorS3Bypass(t *testing.T) {
	store, ctx := openStore(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")

	// Enable QA with default standard-workflow (safe for generic repos).
	if err := store.PutConfig(ctx, model.Config{
		QA: model.QAConfig{
			Enabled:         true,
			FlowID:          model.QAFlowStandard,
			RolloutPct:      100,
			ShadowOnControl: model.BoolPtr(true),
			Epoch:           1,
			RegimeID:        1,
		},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	sim, baseClient := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Advance main past its initial commit so rootSHA != mainHeadSHA.
	rootOut, err := exec.Command("git", "--git-dir="+sim.BareRepo, "rev-list", "--max-parents=0", "refs/heads/main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-list root: %v (%s)", err, rootOut)
	}
	rootSHA := strings.TrimSpace(string(rootOut))
	mainHeadSHA := commitAndPushFile(t, sim.BareRepo, "main", "pkg/existing/legacy.go",
		"package existing\nfunc Legacy() string { return \"old\" }\n",
		"Baseline repo commit on main")
	if mainHeadSHA == rootSHA {
		t.Fatalf("expected mainHeadSHA (%s) != rootSHA (%s)", mainHeadSHA, rootSHA)
	}

	dataDir := t.TempDir()
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		DataDir:  dataDir,
		Profiler: qa.NewProfiler(filepath.Join(dataDir, "perf", "qa.jsonl")),
		ResolveRemote: func(_ context.Context, _ model.RepoRef) (string, string, error) {
			return sim.BareRepo, "", nil
		},
	})
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(nil) })

	// 1. Task with empty Base (""): even when REST DefaultBranch fails/times out during S3,
	// S3 resolves baseSha = merge-base(origin/main, HEAD) via git (NEVER rootSHA).
	task1 := filedTask(t, ctx, store, "s3-mb", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task1.WorkOrigin, task1.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment task1: %v", err)
	}
	stage1 := model.QAStageBranchName(task1.ID)
	commitAndPushFile(t, sim.BareRepo, stage1, "pkg/feature/new.go",
		"package feature\nfunc New() int { return 42 }\n",
		"Add New helper\n\nImplements New().")

	// Wrap client so DefaultBranch fails while S3 runs (proving S3 does not depend on REST DefaultBranch).
	flakyClient := failingDefaultBranchClient{Client: baseClient, failDefaultBranch: true}
	// Note: OpenPullRequestForTask itself needs DefaultBranch when task.Base is empty, so set Base=""
	// and verify S3's recorded BaseSHA after running with a client whose DefaultBranch only succeeds if called after S3,
	// OR first verify S3 mirror resolution runs cleanly with failDefaultBranch=true.
	_, _ = orchestrator.OpenPullRequestForTaskWithQA(ctx, store, flakyClient, task1, orchestrator.QAOpenPROptions{})

	runs1, err := store.ListQACheckRunsForTask(ctx, task1.ID)
	if err != nil || len(runs1) == 0 {
		t.Fatalf("expected recorded S3 check runs for task1, got runs=%d err=%v", len(runs1), err)
	}
	for _, r := range runs1 {
		if r.Stage == "S3" {
			if r.BaseSHA == rootSHA {
				t.Fatalf("S3 check %s fell back to initial/root commit %s; want merge-base %s", r.CheckID, rootSHA, mainHeadSHA)
			}
			if r.BaseSHA != mainHeadSHA {
				t.Fatalf("S3 check %s recorded BaseSHA = %s, want merge-base %s", r.CheckID, r.BaseSHA, mainHeadSHA)
			}
		}
	}

	// 2. When target base branch cannot be resolved at all, S3 MUST return ErrBaseResolutionFailed,
	// record an explicit qa:base-resolution error run with empty BaseSHA (never rootSHA), and NOT
	// silently diff the whole repo.
	task2 := filedTask(t, ctx, store, "s3-badbase", repo)
	if err := store.UpdateTask(ctx, task2.ID, func(tk *model.Task) error {
		tk.Base = "does-not-exist"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	task2Fresh, _ := store.GetTask(ctx, task2.ID)
	task2 = *task2Fresh
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task2.WorkOrigin, task2.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment task2: %v", err)
	}
	stage2 := model.QAStageBranchName(task2.ID)
	commitAndPushFile(t, sim.BareRepo, stage2, "pkg/feature/two.go",
		"package feature\nfunc Two() int { return 2 }\n",
		"Add Two helper\n\nBody.")

	_, err = orchestrator.OpenPullRequestForTaskWithQA(ctx, store, baseClient, task2, orchestrator.QAOpenPROptions{})
	if !errors.Is(err, checks.ErrBaseResolutionFailed) {
		t.Fatalf("expected ErrBaseResolutionFailed on missing base branch, got %v", err)
	}
	runs2, err := store.ListQACheckRunsForTask(ctx, task2.ID)
	if err != nil || len(runs2) != 1 {
		t.Fatalf("expected 1 diagnostic qa:base-resolution check run, got %+v (err=%v)", runs2, err)
	}
	if runs2[0].CheckID != model.QACheckIDBaseResolution || runs2[0].Verdict != "error" || runs2[0].BaseSHA != "" {
		t.Fatalf("unexpected diagnostic check run: %+v", runs2[0])
	}

	// 3. Operator override ("Bypass S3 gate & open PR") on a task blocked at S3 promotes
	// grain-qa-stage/<id> to task.BranchName(), opens the PR, disables AutoMerge pending
	// human review, enforces commit pinning, and records qa:s3-operator-bypass.
	task3 := filedTask(t, ctx, store, "s3-bypass", repo)
	if err := store.UpdateTask(ctx, task3.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if fresh3, _ := store.GetTask(ctx, task3.ID); fresh3 != nil {
		task3 = *fresh3
	}
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(task3.WorkOrigin, task3.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment task3: %v", err)
	}
	stage3 := model.QAStageBranchName(task3.ID)
	// Commit a blocking test-integrity violation (assertion-free Go test) so S3 blocks PR creation.
	sha3 := commitAndPushFile(t, sim.BareRepo, stage3, "pkg/feature/bad_test.go",
		"package feature\nimport \"testing\"\nfunc TestNothing(t *testing.T) {}\n",
		"Add empty test\n\nBody.")
	// Before S3 has blocked sha3, BypassS3 must be refused.
	if _, err := orchestrator.BypassS3AndOpenPullRequestForTask(ctx, store, baseClient, task3, "alice@example.com", "premature", sha3); err == nil {
		t.Fatal("expected BypassS3AndOpenPullRequestForTask to reject task before S3 has failed on sha3")
	}
	if _, err := orchestrator.OpenPullRequestForTaskWithQA(ctx, store, baseClient, task3, orchestrator.QAOpenPROptions{}); err == nil {
		t.Fatal("expected S3 test-integrity-guard to block OpenPullRequestForTaskWithQA before bypass")
	}
	if exists, _ := baseClient.BranchExists(repo.Owner, repo.Name, task3.BranchName()); exists {
		t.Fatalf("PR branch %s must not exist while blocked at S3", task3.BranchName())
	}

	// TOCTOU protection: stale expectedHead must be refused without promoting stage3.
	if _, err := orchestrator.BypassS3AndOpenPullRequestForTask(ctx, store, baseClient, task3, "alice@example.com", "stale commit", "0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("expected BypassS3AndOpenPullRequestForTask to reject stale expectedHead")
	}
	if exists, _ := baseClient.BranchExists(repo.Owner, repo.Name, task3.BranchName()); exists {
		t.Fatalf("PR branch %s must not be promoted when expectedHead mismatches", task3.BranchName())
	}

	bypassStatus, err := orchestrator.BypassS3AndOpenPullRequestForTask(ctx, store, baseClient, task3, "alice@example.com", "False positive; verified manually", sha3)
	if err != nil {
		t.Fatalf("BypassS3AndOpenPullRequestForTask: %v", err)
	}
	if bypassStatus.PullRequest.Number == 0 {
		t.Fatalf("expected opened PR after S3 bypass, got %+v", bypassStatus)
	}
	afterBypass, err := store.GetTask(ctx, task3.ID)
	if err != nil || afterBypass == nil {
		t.Fatalf("GetTask after bypass: %v", err)
	}
	if afterBypass.AutoMerge || !model.AutoMergeAwaitsPerson(*afterBypass) {
		t.Fatalf("BypassS3 must disable AutoMerge and await person review; AutoMerge=%v awaitsPerson=%v", afterBypass.AutoMerge, model.AutoMergeAwaitsPerson(*afterBypass))
	}
	prHead, err := baseClient.GetBranchHead(repo.Owner, repo.Name, task3.BranchName())
	if err != nil || prHead == nil || prHead.SHA != sha3 {
		t.Fatalf("promoted branch head = %+v (err=%v), want %s", prHead, err, sha3)
	}
	if stageStillThere, _ := baseClient.BranchExists(repo.Owner, repo.Name, stage3); stageStillThere {
		t.Fatalf("staging branch %s should be deleted after operator bypass promotion", stage3)
	}
	// Once PR is open and stage3 is deleted, BypassS3 must refuse to run again even if a new S3 failure were recorded.
	if _, err := store.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:    task3.ID,
		CommitSHA: sha3,
		Stage:     "S3",
		CheckID:   "golden:test-integrity-guard",
		Arm:       model.QAArmTreatment,
		Verdict:   "fail",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.BypassS3AndOpenPullRequestForTask(ctx, store, baseClient, *afterBypass, "alice@example.com", "try clearing open PR gate", sha3); err == nil {
		t.Fatal("expected BypassS3AndOpenPullRequestForTask to refuse bypassing on an already-open PR with no staging branch")
	}
	runs3, _ := store.ListQACheckRunsForTask(ctx, task3.ID)
	foundBypass := false
	for _, r := range runs3 {
		if r.CheckID == model.QACheckIDS3OperatorBypass && r.Verdict == "bypassed" && r.CommitSHA == sha3 {
			foundBypass = true
		}
	}
	if !foundBypass {
		t.Fatalf("expected recorded %s check run on %s, got %+v", model.QACheckIDS3OperatorBypass, sha3, runs3)
	}
}
