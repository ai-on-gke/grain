//go:build ignore

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

// seed populates a grain daemon store (SQLite or PostgreSQL) and data-dir
// with >250 rows per collection for docs/test-plans/2026-11-10.md §6.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func main() {
	dataDir := flag.String("data-dir", "", "grain daemon -data-dir")
	driver := flag.String("db-driver", "sqlite", "sqlite or postgres")
	dsn := flag.String("db-dsn", "", "postgres URL or sqlite dir")
	phase := flag.String("phase", "store", "store: every collection, before the daemon's first boot; live: merge batches, once it is up")
	tokensOut := flag.String("tokens-out", "", "path outside repo to write raw test tokens JSON")
	count := flag.Int("count", 260, "rows per collection (>=255)")
	flag.Parse()

	if *dataDir == "" {
		log.Fatal("-data-dir is required")
	}
	ctx := context.Background()

	var db *sql.DB
	var err error
	switch *driver {
	case "sqlite":
		storePath := *dsn
		if storePath == "" {
			storePath = filepath.Join(*dataDir, "store")
		}
		db, err = sqlite.Open(sqlite.DefaultConfig(storePath))
	case "postgres":
		db, err = postgres.Open(postgres.Config{URL: *dsn})
	default:
		log.Fatalf("unsupported -db-driver %q", *driver)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		log.Fatalf("store.Init: %v", err)
	}

	n := *count
	if *phase == "live" {
		seedLive(ctx, store, n)
		return
	}
	baseTime := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "other", Name: "thing"}
	admin := model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
	prose := strings.Repeat("Operational context and guidance for repository automation checks. ", 10) // ~670 B

	// 1. Seed GitHub credentials on disk (<data-dir>/secrets/github): "bot" + n extra tokens.
	ghCredDir := filepath.Join(*dataDir, "secrets", "github")
	if err := os.MkdirAll(ghCredDir, 0o700); err != nil {
		log.Fatalf("mkdir github creds: %v", err)
	}
	credSet, err := gitproxy.LoadCredentialSet(ghCredDir)
	if err != nil {
		log.Fatalf("LoadCredentialSet: %v", err)
	}
	if err := credSet.SetToken("bot", "tok-walkthrough"); err != nil {
		log.Fatalf("SetToken bot: %v", err)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("gh-cred-%03d", i)
		if err := credSet.SetToken(name, fmt.Sprintf("tok-dummy-%03d", i)); err != nil {
			log.Fatalf("SetToken %s: %v", name, err)
		}
	}

	// 2. Seed encrypted secrets (<data-dir>/secrets).
	secStore, err := daemon.OpenSecretsForConfig(daemon.Config{DataDir: *dataDir}, store)
	if err != nil {
		log.Fatalf("OpenSecretsForConfig: %v", err)
	}
	for i := 0; i < n; i++ {
		secName := fmt.Sprintf("service-secret-%03d", i)
		keyName := fmt.Sprintf("api-key-%03d", i%10)
		if err := secStore.Set(secName, keyName, []byte("redacted-value")); err != nil {
			log.Fatalf("secStore.Set %s: %v", secName, err)
		}
	}

	// 3. Seed Config: GitConnections (n), ConsoleSessions (n), CapabilityServers (n), ExternalAgentAPI.
	gitConns := make([]model.TeamGitConnection, 0, n+1)
	gitConns = append(gitConns,
		model.TeamGitConnection{Credential: "bot", Scope: "acme/widgets"},
	)
	for i := 0; i < n; i++ {
		gitConns = append(gitConns, model.TeamGitConnection{
			Credential: "bot",
			Scope:      fmt.Sprintf("acme/service-%03d-component", i),
		})
	}

	consoleSessions := model.DefaultConsoleSessions()
	readsAllFalse := false
	for i := 0; i < n; i++ {
		team := "platform"
		if i%5 == 0 {
			team = "security"
		}
		consoleSessions = append(consoleSessions, model.ConsoleSession{
			ID:       fmt.Sprintf("session-%03d", i),
			Title:    fmt.Sprintf("Standing Triage Session %03d", i),
			Team:     team,
			ReadsAll: &readsAllFalse,
		})
	}

	// Only connections and console sessions are written here. This row
	// has no poll interval, worker count, GitHub host or deployment id,
	// so the daemon's first boot reads it as `grain connections import`'s
	// row (model.Config.ConnectionsOnly) and replaces it with a row seeded
	// from its flags, carrying over only what WithConnectionsOf names: git
	// connections and credential, agent backends, model settings and
	// console sessions (daemon.LoadConfig). Capability servers,
	// externalAgentApi and target repos written here were dropped by
	// design. Capability servers are added through PUT
	// /api/capability-servers once the daemon is up (seed_api.py), and
	// externalAgentApi through PUT /api/settings (env.sh).
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "bot"
		cfg.GitConnections = gitConns
		cfg.ConsoleSessions = consoleSessions
		return nil
	}); err != nil {
		log.Fatalf("UpdateConfig: %v", err)
	}

	// 4. Seed Acting Agent Tasks ("9000" full scopes, "9001" repo-limited to acme/widgets) + n regular tasks.
	allReadScopes := []string{
		"api:task-admin",
		"api:tasks:read", "api:ci:read", "api:repos:read", "api:releases:read",
		"api:schedules:read", "api:templates:read", "api:suites:read", "api:boards:read",
		"api:memory:read", "api:release-notes:read", "api:sessions:read", "api:caches:read",
		"api:settings:read", "api:access:read", "api:connections:read", "api:capabilities:read",
		"api:secrets:read", "api:deployment:read", "api:sandboxes:read", "api:dispatch:read",
	}
	var fullGrants []model.Grant
	for _, sc := range allReadScopes {
		fullGrants = append(fullGrants, model.Grant{Capability: sc})
	}
	created := baseTime.Add(-2 * time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID:        "1",
		Intent:    model.IntentImplement,
		Title:     "Primary walkthrough anchor task",
		Body:      "Anchor task carrying >250 conversation messages.",
		Target:    &widgets,
		Branch:    "grain/task-1-anchor",
		Tags:      []string{"area/paging", "walkthrough"},
		CreatedAt: &created,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: admin}, Reason: model.ReasonDirect},
		Approval:  &model.Attribution{Actor: admin},
	}); err != nil {
		log.Fatalf("PutTask 1: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:        "9000",
		Intent:    model.IntentImplement,
		Title:     "Full-scope agent caller task",
		Body:      "Used as X-Grain-Acting-Task for unrestricted agent paging checks.",
		Target:    &widgets,
		Grants:    fullGrants,
		CreatedAt: &created,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: admin}, Reason: model.ReasonDirect},
	}); err != nil {
		log.Fatalf("PutTask 9000: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "9001",
		Intent: model.IntentImplement,
		Title:  "Repo-limited agent caller task",
		Body:   "Restricted to acme/widgets only.",
		Target: &widgets,
		Grants: []model.Grant{
			{Capability: "api:tasks:read@acme/widgets"},
			{Capability: "api:repos:read@acme/widgets"},
			{Capability: "api:ci:read@acme/widgets"},
			{Capability: "api:templates:read@acme/widgets"},
			{Capability: "api:memory:read@acme/widgets"},
			{Capability: "api:suites:read@acme/widgets"},
		},
		CreatedAt: &created,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: admin}, Reason: model.ReasonDirect},
	}); err != nil {
		log.Fatalf("PutTask 9001: %v", err)
	}

	for i := 0; i < n+10; i++ {
		id := fmt.Sprintf("%d", 100+i)
		target := widgets
		if i >= n-5 {
			target = other
		}
		at := baseTime.Add(time.Duration(i) * time.Second)
		tag := "kind/backend"
		if i%7 == 0 {
			tag = "label-canary-needle"
		}
		tk := model.Task{
			ID:          id,
			Intent:      model.IntentImplement,
			Title:       fmt.Sprintf("Seeded backlog item %03d for pagination verification", i),
			Body:        fmt.Sprintf("Detailed body for task %03d. %s", i, prose[:240]),
			Target:      &target,
			Branch:      fmt.Sprintf("grain/feat-branch-%03d", i),
			Tags:        []string{tag},
			ExternalRef: fmt.Sprintf("TICKET-%03d", i),
			FiledVia:    "seed-script/1.0",
			CreatedAt:   &at,
			Origin:      model.Origin{Attribution: model.Attribution{Actor: admin}, Reason: model.ReasonDirect},
		}
		if i%3 != 0 {
			tk.Approval = &model.Attribution{Actor: admin}
		}
		if err := store.PutTask(ctx, tk); err != nil {
			log.Fatalf("PutTask %s: %v", id, err)
		}
		if i%4 == 0 {
			closedAt := at.Add(30 * time.Minute)
			_ = store.Observe(ctx, model.Observation{TaskID: id, ClosedAt: &closedAt})
		}
	}

	// 5. Seed n comments/messages on task "1" (also generates integration_event rows).
	for i := 0; i < n; i++ {
		summary := ""
		if i%5 == 0 {
			summary = fmt.Sprintf("Summary note %03d", i)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    "1",
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: fmt.Sprintf("reviewer-%02d@example.com", i%20)}},
			Summary:   summary,
			Body:      fmt.Sprintf("Conversation message %03d discussing needle-%03d.", i, i),
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("AddComment %d: %v", i, err)
		}
	}
	// Also add comments on an other/thing task so GET /api/events has events on both repos.
	otherTaskID := fmt.Sprintf("%d", 100+n)
	for i := 0; i < 25; i++ {
		_, _ = store.AddComment(ctx, model.Comment{
			TaskID:    otherTaskID,
			Author:    model.Attribution{Actor: admin},
			Body:      fmt.Sprintf("Other-repo secret event body %02d", i),
			CreatedAt: baseTime.Add(time.Duration(n+i) * time.Second),
		})
	}

	// 6. Seed n notifications (open, feeding both /api/notifications and /api/inbox).
	for i := 0; i < n; i++ {
		fam := "ci"
		if i%4 == 0 {
			fam = "quota"
		}
		if _, err := store.RaiseNotification(ctx, model.Notification{
			Subject:  fmt.Sprintf("%s:subject-%03d", fam, i),
			Family:   fam,
			Severity: model.NotificationSeverity("warning"),
			Title:    fmt.Sprintf("Alert notification %03d on subsystem", i),
			Body:     fmt.Sprintf("First line of notification %03d.\nSecond detail line.", i),
			Repo:     "acme/widgets",
			RaisedBy: "automation:seed",
			RaisedAt: baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("RaiseNotification %d: %v", i, err)
		}
	}

	// 7. Seed n branches, n repo workflows, n releases, and n candidates on release v1.0.
	var repoWfs []model.RepoWorkflow
	for i := 0; i < n; i++ {
		if _, err := store.CreateBranch(ctx, widgets, fmt.Sprintf("grain/cut-branch-%03d", i), baseTime.Add(time.Duration(i)*time.Second)); err != nil {
			log.Fatalf("CreateBranch %d: %v", i, err)
		}
		repoWfs = append(repoWfs, model.RepoWorkflow{
			Repo: widgets,
			Path: fmt.Sprintf(".grain/workflows/declared-%03d.yml", i),
			Text: fmt.Sprintf("name: Declared Workflow %03d\non: [push]\njobs:\n  check:\n    steps:\n      - run: echo ok\n", i),
		})
		relName := fmt.Sprintf("rel-%03d", i)
		rel, err := store.CreateRelease(ctx, widgets, relName, baseTime.Add(time.Duration(i)*time.Second))
		if err != nil {
			log.Fatalf("CreateRelease %d: %v", i, err)
		}
		if err := store.MarkReleaseProvisioned(ctx, rel.ID); err != nil {
			log.Fatalf("MarkReleaseProvisioned %d: %v", i, err)
		}
	}
	if err := store.PutRepoWorkflows(ctx, widgets, repoWfs); err != nil {
		log.Fatalf("PutRepoWorkflows: %v", err)
	}
	if cur, err := store.CurrentCandidateForRelease(ctx, 1); err == nil && cur != nil {
		_ = store.MarkCandidatePromoted(ctx, cur.ID, baseTime)
	}
	for i := 0; i < n; i++ {
		cand, err := store.CutCandidate(ctx, widgets, "rel-000", baseTime.Add(time.Duration(i)*time.Second))
		if err != nil {
			log.Fatalf("CutCandidate %d: %v", i, err)
		}
		if err := store.MarkCandidatePromoted(ctx, cand.ID, baseTime.Add(time.Duration(i)*time.Second)); err != nil {
			log.Fatalf("MarkCandidatePromoted %d: %v", i, err)
		}
	}

	// 8. Seed n CI runs (with shared faults across 2 branches) + n artifacts on ci-run-000.
	artDir := filepath.Join(*dataDir, "state", "ci-artifacts")
	artStore, err := ci.OpenDirArtifacts(artDir, ci.DirArtifactsOptions{})
	if err != nil {
		log.Fatalf("OpenDirArtifacts: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := artStore.Save(ctx, ci.ArtifactRef{
			Run:  "ci-run-000",
			Job:  fmt.Sprintf("job-%02d", i%10),
			Name: fmt.Sprintf("bundle-%03d", i),
		}, []byte(fmt.Sprintf("artifact payload %03d", i))); err != nil {
			log.Fatalf("artStore.Save %d: %v", i, err)
		}
	}
	// Create 260 CI runs, plus pair failures so shared faults also exceed 250!
	// If each run has 3 failing steps with distinct digests on branch A and branch B, 260 runs easily yield 260 shared faults!
	for i := 0; i < n; i++ {
		st := baseTime.Add(-time.Duration(n-i) * time.Minute)
		ft := st.Add(45 * time.Second)
		ref := fmt.Sprintf("grain/pr-branch-%03d", i)
		reqBy := fmt.Sprintf("%d", 100+i)
		// Share fault digest i/1 across two branches by pairing even/odd or adding 2 jobs:
		faultIdxA := i
		faultIdxB := (i + 1) % n
		run := model.CIRun{
			ID:          fmt.Sprintf("ci-run-%03d", i),
			Key:         fmt.Sprintf("key-%03d", i),
			Repo:        "acme/widgets",
			Ref:         ref,
			Commit:      fmt.Sprintf("%040x", i+1),
			Event:       "pull_request",
			RequestedBy: reqBy,
			StartedAt:   st,
			FinishedAt:  ft,
			Conclusion:  "failure",
			Jobs: []model.CIJob{
				{
					Workflow:     "Unit Suite",
					WorkflowPath: ".grain/workflows/unit.yml",
					JobID:        "unit",
					Name:         "unit",
					StartedAt:    st,
					FinishedAt:   ft,
					Conclusion:   "failure",
					Steps: []model.CIStep{
						{
							Name:       fmt.Sprintf("step-alpha-%03d", faultIdxA),
							Conclusion: "failure",
							ExitCode:   1,
						},
						{
							Name:       fmt.Sprintf("step-alpha-%03d", faultIdxB),
							Conclusion: "failure",
							ExitCode:   1,
						},
					},
				},
			},
		}
		if err := store.PutCIRun(ctx, run); err != nil {
			log.Fatalf("PutCIRun %d: %v", i, err)
		}
		_ = store.PutCIStepOutput(ctx, run.ID, 0, 0, fmt.Sprintf("--- FAIL: TestSharedFault_%03d (0.01s)\n    pkg/foo/bar_%03d_test.go:42: assertion failed\nFAIL\tgithub.com/acme/widgets/pkg/foo\n", faultIdxA, faultIdxA))
		_ = store.PutCIStepOutput(ctx, run.ID, 0, 1, fmt.Sprintf("--- FAIL: TestSharedFault_%03d (0.01s)\n    pkg/foo/bar_%03d_test.go:42: assertion failed\nFAIL\tgithub.com/acme/widgets/pkg/foo\n", faultIdxB, faultIdxB))
	}

	// The grain CI runs above are dated off baseTime, outside the 6 h
	// fault window (ci.DefaultFaultWindow), and GET /api/ci/faults reads at
	// most 60 of them (ciFaultRunLimit) anyway. Shared GitHub Actions
	// failures are read from the store with no such bound: n faults, each
	// failing job JobName gh-shared-NNN on two branches, completed an hour
	// before the seed ran.
	ghAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < n; i++ {
		for b, side := range []string{"a", "b"} {
			at := ghAt.Add(time.Duration(i) * time.Second)
			if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
				Repo:        "acme/widgets",
				CheckRunID:  int64(700000 + 2*i + b),
				Source:      model.GitHubCISource,
				App:         "github-actions",
				Ref:         fmt.Sprintf("grain/gh-%s-%03d", side, i),
				Commit:      fmt.Sprintf("%040x", 0x7000+2*i+b),
				Workflow:    "Tests",
				RunID:       int64(800000 + 2*i + b),
				RunAttempt:  1,
				JobName:     fmt.Sprintf("gh-shared-%03d", i),
				Conclusion:  "failure",
				StartedAt:   &at,
				CompletedAt: &at,
				FailingStep: "go test",
				FailureTail: fmt.Sprintf("--- FAIL: TestGitHubShared_%03d (0.01s)\n    pkg/gh/shared_%03d_test.go:17: want ok\nFAIL\tgithub.com/acme/widgets/pkg/gh\n", i, i),
				TailSource:  "log",
				RecordedAt:  at,
				UpdatedAt:   at,
			}); err != nil {
				log.Fatalf("PutGitHubCIJob %d%s: %v", i, side, err)
			}
		}
	}

	// 9. Seed n watched base-branch gates. Merge batches are seeded
	// after boot (-phase live).
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:               widgets,
		CIMode:             model.CIModeGrain,
		WatchDefaultBranch: true,
	}); err != nil {
		log.Fatalf("PutRepoConfig widgets: %v", err)
	}
	for i := 0; i < n; i++ {
		// A watched repository that is on grain CI and not enrolled yet
		// is not a finding: the seeder enrols one branch per cycle
		// (ui.baseBranchGateOf). A watched repository whose CI mode is
		// github has no base-branch check at all, which is the "not
		// enabled" gate, and the orchestrator does not enrol it.
		gateRepo := model.RepoRef{Owner: "acme", Name: fmt.Sprintf("watched-repo-%03d", i)}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:               gateRepo,
			CIMode:             model.CIModeGitHub,
			WatchDefaultBranch: true,
		}); err != nil {
			log.Fatalf("PutRepoConfig gate %d: %v", i, err)
		}
	}

	// 10. Seed n standing memories & n memory documents.
	if err := store.PutMemory(ctx, model.Memory{
		Text:      "Deployment-wide standing memory. " + prose,
		UpdatedAt: baseTime,
		UpdatedBy: admin,
	}); err != nil {
		log.Fatalf("PutMemory global: %v", err)
	}
	for i := 0; i < n; i++ {
		memRepo := model.RepoRef{Owner: "acme", Name: fmt.Sprintf("mem-repo-%03d", i)}
		if i == 0 {
			memRepo = widgets
		} else if i == 1 {
			memRepo = other
		}
		if err := store.PutMemory(ctx, model.Memory{
			Repo:      &memRepo,
			Text:      fmt.Sprintf("Standing memory %03d: %s", i, prose),
			UpdatedAt: baseTime.Add(time.Duration(i) * time.Second),
			UpdatedBy: admin,
		}); err != nil {
			log.Fatalf("PutMemory %d: %v", i, err)
		}

		docRepo := &widgets
		if i >= n-15 {
			docRepo = &other
		} else if i == 0 {
			docRepo = nil
		}
		if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
			Repo:      docRepo,
			Name:      fmt.Sprintf("architecture-note-%03d", i),
			Summary:   fmt.Sprintf("Summary of architecture note %03d", i),
			Text:      prose,
			UpdatedAt: baseTime.Add(time.Duration(i) * time.Second),
			UpdatedBy: admin,
		}); err != nil {
			log.Fatalf("PutMemoryDocument %d: %v", i, err)
		}
	}

	// 11. Seed n templates, n schedules (disabled), n suites, n suite runs, n boards, n release notes.
	for i := 0; i < n; i++ {
		var tmplTarget *model.RepoRef
		if i < n-20 {
			r := widgets
			tmplTarget = &r
		} else if i < n-5 {
			r := other
			tmplTarget = &r
		}
		tmplID := fmt.Sprintf("tmpl-%03d", i)
		if err := store.PutTemplate(ctx, model.Template{
			ID:     tmplID,
			Name:   fmt.Sprintf("template-slug-%03d", i),
			Title:  fmt.Sprintf("Template Title %03d", i),
			Body:   fmt.Sprintf("Template instructions %03d.\n%s", i, prose),
			Target: tmplTarget,
			// One holder per capability server seed_api.py configures,
			// for GET /api/capability-servers/usage.
			Grants:    []model.Grant{{Capability: capServerID(i) + "#run"}},
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("PutTemplate %d: %v", i, err)
		}

		suiteID := fmt.Sprintf("suite-%03d", i)
		st := model.Suite{
			ID:        suiteID,
			Name:      fmt.Sprintf("Qualification Suite %03d", i),
			Items:     []model.SuiteItem{{TemplateID: "tmpl-000"}},
			Mode:      model.SuiteCount,
			Count:     1,
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}
		if err := store.PutSuite(ctx, st); err != nil {
			log.Fatalf("PutSuite %d: %v", i, err)
		}

		srTarget := widgets
		if i >= n-10 {
			srTarget = other
		}
		if _, err := store.CreateSuiteRun(ctx, st, srTarget, "trunk", baseTime.Add(time.Duration(i)*time.Second)); err != nil {
			log.Fatalf("CreateSuiteRun %d: %v", i, err)
		}

		sched := model.Schedule{
			ID:         fmt.Sprintf("sched-%03d", i),
			Title:      fmt.Sprintf("Nightly Sweep Schedule %03d", i),
			Target:     widgets,
			Base:       "trunk",
			TemplateID: &tmplID,
			Enabled:    false,
			Recurrence: model.SystemScheduleRecurrence(fmt.Sprintf("sched-%03d", i), 3),
			CreatedAt:  baseTime.Add(time.Duration(i) * time.Second),
		}
		if err := store.PutSchedule(ctx, sched); err != nil {
			log.Fatalf("PutSchedule %d: %v", i, err)
		}

		folder := "Operations"
		if i%4 == 0 {
			folder = "Releases"
		}
		if err := store.PutBoard(ctx, model.Board{
			ID:          fmt.Sprintf("board-%03d", i),
			Name:        fmt.Sprintf("Team Board %03d", i),
			Folder:      folder,
			Kind:        model.BoardList,
			Description: fmt.Sprintf("Board %03d overview", i),
		}); err != nil {
			log.Fatalf("PutBoard %d: %v", i, err)
		}

		year := 2018 + (i / 52)
		week := (i % 52) + 1
		period := fmt.Sprintf("%04d-W%02d", year, week)
		if err := store.PutReleaseNote(ctx, model.ReleaseNote{
			Period:    period,
			Title:     fmt.Sprintf("Weekly Engineering Digest %s", period),
			Text:      fmt.Sprintf("# Highlights for %s\n\n%s", period, prose),
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt: baseTime.Add(time.Duration(i) * time.Second),
			UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: fmt.Sprintf("editor-%02d@example.com", i%10)},
		}); err != nil {
			log.Fatalf("PutReleaseNote %d: %v", i, err)
		}
	}

	// 12. Seed n secret-audit rows, 60 credential-access rows, and 280 config-audit rows (265 surface="access").
	for i := 0; i < n; i++ {
		if err := store.RecordSecretAuditEntry(ctx, model.SecretAuditEntry{
			Timestamp:  baseTime.Add(time.Duration(i) * time.Second),
			TaskID:     fmt.Sprintf("%d", 100+i),
			RunID:      fmt.Sprintf("run-%03d", i),
			SecretName: fmt.Sprintf("service-secret-%03d", i),
			Key:        fmt.Sprintf("api-key-%03d", i%10),
			Principal:  fmt.Sprintf("agent:task-%d", 100+i),
		}); err != nil {
			log.Fatalf("RecordSecretAuditEntry %d: %v", i, err)
		}
	}
	for i := 0; i < 60; i++ {
		if err := store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
			Timestamp:  baseTime.Add(time.Duration(i) * time.Second),
			TaskID:     fmt.Sprintf("%d", 100+i),
			RunID:      fmt.Sprintf("run-%03d", i),
			Sandbox:    fmt.Sprintf("sbx-%03d", i),
			Kind:       "git",
			Resource:   "acme/widgets",
			Action:     "fetch",
			Credential: "bot",
			Outcome:    "allow",
		}); err != nil {
			log.Fatalf("RecordCredentialAccess %d: %v", i, err)
		}
	}
	for i := 0; i < n+20; i++ {
		surface := "access"
		if i >= n+5 {
			surface = "settings"
		}
		if _, err := store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:      baseTime.Add(time.Duration(i) * time.Second),
			By:      model.Principal{Kind: model.PrincipalHuman, ID: fmt.Sprintf("auditor-%02d@example.com", i%15)},
			Surface: surface,
			Summary: fmt.Sprintf("Audit change %03d on %s", i, surface),
		}); err != nil {
			log.Fatalf("RecordConfigAudit %d: %v", i, err)
		}
	}

	// 13. Seed n API scope sets, n external-agent tokens (+ 2 known tokens), n allowed principals, n hosts, n GCP keys, n persistent sandboxes.
	for i := 0; i < n; i++ {
		if err := store.PutAPIScopeSet(ctx, model.APIScopeSet{
			Name:        fmt.Sprintf("ops/scope-set-%03d", i),
			Description: fmt.Sprintf("Scoped operational bundle %03d", i),
			Scopes:      []string{"tasks:read", "ci:read"},
			Version:     1,
		}); err != nil {
			log.Fatalf("PutAPIScopeSet %d: %v", i, err)
		}

		raw := fmt.Sprintf("gma_seeded_dummy_token_%04d", i)
		sum := sha256.Sum256([]byte(raw))
		if _, err := store.CreateExternalAgentToken(ctx, model.ExternalAgentToken{
			ID:        fmt.Sprintf("eat-%03d", i),
			Name:      fmt.Sprintf("workstation-bot-%03d", i),
			TokenHash: hex.EncodeToString(sum[:]),
			Prefix:    raw[:12],
			ScopeSet:  "grain/external-read",
			Owner:     fmt.Sprintf("dev-%03d@example.com", i),
			CreatedBy: "owner@example.com",
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("CreateExternalAgentToken %d: %v", i, err)
		}

		role := "grain/contributor"
		if i%10 == 0 {
			role = "grain/viewer"
		}
		if _, _, err := store.AddAllowedPrincipalWithRole(ctx,
			fmt.Sprintf("user:member-%03d@example.com", i),
			"owner@example.com",
			fmt.Sprintf("onboarded batch %03d", i%25),
			role,
			baseTime.Add(time.Duration(i)*time.Second),
		); err != nil {
			log.Fatalf("AddAllowedPrincipalWithRole %d: %v", i, err)
		}

		if _, _, err := store.CreateHostd(ctx, model.Hostd{
			Name:      fmt.Sprintf("runner-host-%03d", i),
			Owner:     fmt.Sprintf("ops-%02d@example.com", i%12),
			CreatedBy: "owner@example.com",
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("CreateHostd %d: %v", i, err)
		}

		if err := store.PutGCPKey(ctx, model.GCPKey{
			Name:                fmt.Sprintf("gcp-cred-%03d", i),
			ProjectID:           fmt.Sprintf("sample-proj-%03d", i%30),
			ServiceAccountEmail: fmt.Sprintf("sa-%03d@sample-proj.iam.gserviceaccount.com", i),
			Description:         fmt.Sprintf("Test GCP credential %03d", i),
			CreatedAt:           baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:           baseTime.Add(time.Duration(i) * time.Second),
		}); err != nil {
			log.Fatalf("PutGCPKey %d: %v", i, err)
		}

		sbRepo := "acme/widgets"
		if i%6 == 0 {
			sbRepo = "other/thing"
		}
		if err := store.PutPersistentSandbox(ctx, model.PersistentSandbox{
			Name:    fmt.Sprintf("devbox-%03d", i),
			Repo:    sbRepo,
			Command: "sleep 3600",
		}); err != nil {
			log.Fatalf("PutPersistentSandbox %d: %v", i, err)
		}
	}

	// Mint 3 deterministic external tokens (saved only to -tokens-out in /var/tmp, never committed):
	// 1) full read token across all non-internal read scopes
	// 2) repo-limited token via Repos: ["acme/widgets"]
	// 3) repo-limited token via per-scope @acme/widgets qualifiers
	mint := func(id, name string, scopes, repos []string) string {
		raw := fmt.Sprintf("gma_walk_%s_%d", id, baseTime.Unix())
		sum := sha256.Sum256([]byte(raw))
		var bareScopes []string
		for _, s := range scopes {
			bareScopes = append(bareScopes, strings.TrimPrefix(s, "api:"))
		}
		if _, err := store.CreateExternalAgentToken(ctx, model.ExternalAgentToken{
			ID:        id,
			Name:      name,
			TokenHash: hex.EncodeToString(sum[:]),
			Prefix:    raw[:12],
			Scopes:    bareScopes,
			Repos:     repos,
			// The walk pages every route to its end; the default 60
			// requests a minute would 429 it part way through.
			RateLimit: 100000,
			CreatedBy: "owner@example.com",
			CreatedAt: baseTime.Add(time.Hour),
		}); err != nil {
			log.Fatalf("mint %s: %v", id, err)
		}
		return raw
	}
	tokFull := mint("eat-walk-full", "walk-full-reader", allReadScopes, nil)
	tokWidgets := mint("eat-walk-widgets", "walk-widgets-only", []string{"tasks:read", "repos:read", "ci:read", "templates:read", "memory:read", "suites:read"}, []string{"acme/widgets"})
	tokQualifier := mint("eat-walk-qualifier", "walk-scope-qualifier", []string{"tasks:read@acme/widgets", "repos:read@acme/widgets", "ci:read@acme/widgets"}, nil)

	if *tokensOut != "" {
		b, _ := json.MarshalIndent(map[string]string{
			"full":      tokFull,
			"widgets":   tokWidgets,
			"qualifier": tokQualifier,
		}, "", "  ")
		if err := os.WriteFile(*tokensOut, b, 0o600); err != nil {
			log.Fatalf("write tokens-out: %v", err)
		}
	}

	log.Printf("seeded %d+ rows per collection into %s (%s)", n, *dataDir, *driver)
}

// capServerID is the i'th capability server seed_api.py configures and
// the i'th template grants. A configured id must be an https:// URL
// (PUT /api/capability-servers refuses "cap-srv-000").
func capServerID(i int) string {
	return fmt.Sprintf("https://tools.example.com/capabilities/cap-%03d", i)
}

// seedLive writes n merge batches into a running daemon's store.
//
// A merge batch is only ever in the store while the merge queue is
// re-checking it, and the reconciler's first cycle after boot
// (orchestrator.advanceMergeRechecks) deletes every row that is not a
// running re-check: no members, a finished conclusion, older than
// model.MaxMergeBatchAge, or a CI run the runner does not know or
// recorded as interrupted. Boot also records every unfinished CI run as
// interrupted (daemon.AdoptCIRuns). So each batch here is written after
// boot, started now, with one member and an unfinished CI run of its own,
// which is what a re-check in flight looks like to the next cycle.
func seedLive(ctx context.Context, store *model.Store, n int) {
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "other", Name: "thing"}
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < n; i++ {
		repo := widgets
		if i >= n-10 {
			repo = other
		}
		taskID := fmt.Sprintf("%d", 100+i)
		runID := fmt.Sprintf("mb-run-%03d", i)
		ref := fmt.Sprintf("grain/merge-recheck/%s", taskID)
		at := now.Add(time.Duration(i) * time.Millisecond)
		if err := store.PutCIRun(ctx, model.CIRun{
			ID:          runID,
			Key:         "mb-key-" + taskID,
			Repo:        repo.String(),
			Ref:         ref,
			Commit:      fmt.Sprintf("%040x", 0x9000+i),
			Event:       "push",
			RequestedBy: taskID,
			StartedAt:   at,
			Jobs: []model.CIJob{{
				Workflow: "Merge recheck", WorkflowPath: ".grain/workflows/unit.yml",
				JobID: "unit", Name: "unit", StartedAt: at,
			}},
		}); err != nil {
			log.Fatalf("PutCIRun %s: %v", runID, err)
		}
		if err := store.PutMergeBatch(ctx, model.MergeBatch{
			Repo:       repo,
			Branch:     "trunk",
			TaskID:     taskID,
			Ref:        ref,
			BaseCommit: fmt.Sprintf("%040x", 0xb000+i),
			Commit:     fmt.Sprintf("%040x", 0x9000+i),
			RunID:      runID,
			StartedAt:  at,
			Members: []model.MergeBatchMember{{
				TaskID:  taskID,
				Number:  1000 + i,
				HeadRef: fmt.Sprintf("grain/feat-branch-%03d", i),
				HeadSHA: fmt.Sprintf("%040x", 0xc000+i),
			}},
		}); err != nil {
			log.Fatalf("PutMergeBatch %d: %v", i, err)
		}
	}
	log.Printf("seeded %d running merge batches", n)
}
