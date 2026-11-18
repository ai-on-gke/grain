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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

func openTestStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return store, ctx
}

func TestExecutePreDispatchQAPhasesAndControlIsolation(t *testing.T) {
	store, ctx := openTestStore(t)

	prev := orchestrator.LiveQARuntime()
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(prev) })

	s0Calls, s2Calls := 0, 0
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, runPrefix, _ string, _ int) (string, int64, int64, error) {
			if strings.HasSuffix(runPrefix, "-qa-s0") {
				s0Calls++
				return "- WHEN input is empty SHALL return ErrInvalid", 42, 18, nil
			}
			if strings.HasSuffix(runPrefix, "-qa-s2") {
				s2Calls++
				return "- Update callers in pkg/ui/server.go", 65, 24, nil
			}
			return "", 0, 0, nil
		},
	})

	spec := &qa.FlowSpec{
		S0Intent:            true,
		IntentSkillText:     "Extract EARS criteria",
		S0IntentConfig:      qa.S0IntentDecl{MaxTokens: 15000, MaxSeconds: 10},
		S2PlanCritic:        true,
		PlanCriticSkillText: "Critique sibling callers",
		S2PlanCriticConfig:  qa.S2PlanCriticDecl{Complexities: []string{"low", "medium", "high"}, MaxTokens: 25000, MaxSeconds: 15},
	}

	task := model.Task{ID: "task-v3-1", Title: "Refactor auth handler", Body: "Tighten validation across packages"}

	// Control arm must NEVER invoke S0 or S2 even when a flow spec is present for shadow S3.
	ctrlOut := orchestrator.ExecutePreDispatchQAPhases(ctx, store, task, spec, "exp-1", model.QAArmControl, "sha256:v3", "base-skill")
	if ctrlOut != "base-skill" || s0Calls != 0 || s2Calls != 0 {
		t.Fatalf("control arm leaked S0/S2: out=%q s0=%d s2=%d", ctrlOut, s0Calls, s2Calls)
	}

	// Design-Item tasks must skip S0 while still allowing S2 when configured.
	designTask := model.Task{ID: "task-v3-design", Title: "Add endpoint", Body: "Design-Item: DI-42\nImplement route"}
	_ = orchestrator.ExecutePreDispatchQAPhases(ctx, store, designTask, spec, "exp-1", "golden-v3", "sha256:v3", "base-skill")
	if s0Calls != 0 || s2Calls != 1 {
		t.Fatalf("Design-Item gating mismatch: s0=%d (want 0), s2=%d (want 1)", s0Calls, s2Calls)
	}

	// Enforced treatment arm runs S0 + S2 and records telemetry rows.
	treatOut := orchestrator.ExecutePreDispatchQAPhases(ctx, store, task, spec, "exp-1", "golden-v3", "sha256:v3", "base-skill")
	if !strings.Contains(treatOut, "## EARS Intent Checklist") || !strings.Contains(treatOut, "## S2 Plan Critique") {
		t.Fatalf("expected S0 and S2 sections in prompt, got: %s", treatOut)
	}
	runs, err := store.ListQACheckRunsForTask(ctx, task.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected 2 recorded check runs (S0, S2), got %d (err=%v)", len(runs), err)
	}
}

func TestEvaluateAndApplyQATriageEnforcesHoldOnT2AndPreservesT1AutoMerge(t *testing.T) {
	store, ctx := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "grain"}

	enforcedSpec := &qa.FlowSpec{
		ID: "golden-v3-enforced",
		S5MergeTriage: qa.TriageConfig{
			Mode:              "enforced",
			AutoMergeTiers:    []string{"T0", "T1"},
			RequireHumanTiers: []string{"T2", "T3"},
		},
	}
	assign := &resolve.Resolution{
		ExperimentID: "exp-m16",
		ArmID:        "golden-v3",
		FlowHash:     "sha256:v3enforced",
		Spec:         enforcedSpec,
		QAEngineRev:  qa.QAEngineRev,
	}

	// 1. Clean T1 leaf Go change keeps AutoMerge=true and sets no LinkQATriageHold.
	t1 := model.Task{
		ID:        "t1-leaf",
		Title:     "Add string helper",
		Target:    &repo,
		AutoMerge: true,
	}
	if err := store.PutTask(ctx, t1); err != nil {
		t.Fatalf("PutTask t1: %v", err)
	}
	orchestrator.EvaluateAndApplyQATriage(ctx, store, nil, t1, assign, "1111111111111111", []string{"pkg/util/str.go", "pkg/util/str_test.go"}, true)
	gotT1, err := store.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("GetTask t1: %v", err)
	}
	if !gotT1.AutoMerge {
		t.Fatalf("expected T1 task AutoMerge=true, got false")
	}

	// 2. T2 UI change under enforced S5 withdraws AutoMerge, sets LinkQATriageHold, and blocks agent release.
	t2 := model.Task{
		ID:        "t2-ui",
		Title:     "Update inbox toolbar",
		Target:    &repo,
		AutoMerge: true,
	}
	if err := store.PutTask(ctx, t2); err != nil {
		t.Fatalf("PutTask t2: %v", err)
	}
	orchestrator.EvaluateAndApplyQATriage(ctx, store, nil, t2, assign, "2222222222222222", []string{"ui-ng/src/app/inbox/inbox-page.component.ts"}, true)
	gotT2, err := store.GetTask(ctx, t2.ID)
	if err != nil {
		t.Fatalf("GetTask t2: %v", err)
	}
	if gotT2.AutoMerge {
		t.Fatalf("expected T2 task AutoMerge withdrawn (false), got true")
	}
	hasHold := false
	for _, l := range gotT2.Links {
		if l.Kind == model.LinkQATriageHold {
			hasHold = true
		}
	}
	if !hasHold {
		t.Fatalf("expected LinkQATriageHold on T2 task, links=%+v", gotT2.Links)
	}

	// Agent cannot release human review hold; human operator can.
	if _, err := store.ReleaseHumanReviewHold(ctx, t2.ID, model.Principal{Kind: model.PrincipalAgent, ID: "agent-1"}, "ship"); err == nil {
		t.Fatalf("expected PrincipalAgent ReleaseHumanReviewHold to be rejected")
	}
	if _, err := store.ReleaseHumanReviewHold(ctx, t2.ID, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}, "reviewed"); err != nil {
		t.Fatalf("expected PrincipalHuman ReleaseHumanReviewHold to succeed, got %v", err)
	}

	// 3. Control arm with ShadowOnControl=true must NEVER lose AutoMerge or receive LinkQATriageHold even when spec.Mode == "enforced".
	ctrlTask := model.Task{
		ID:        "t2-ctrl-shadow",
		Title:     "Control UI task under shadow scoring",
		Target:    &repo,
		AutoMerge: true,
	}
	if err := store.PutTask(ctx, ctrlTask); err != nil {
		t.Fatalf("PutTask ctrlTask: %v", err)
	}
	ctrlAssign := &resolve.Resolution{
		ArmID:       model.QAArmControl,
		Shadow:      true,
		FlowID:      "golden-v3-enforced",
		FlowHash:    assign.FlowHash,
		QAEngineRev: qa.QAEngineRev,
		Spec:        enforcedSpec,
	}
	orchestrator.EvaluateAndApplyQATriage(ctx, store, nil, ctrlTask, ctrlAssign, "3333333333333333", []string{"ui-ng/src/app/inbox/inbox-page.component.ts"}, true)
	gotCtrl, err := store.GetTask(ctx, ctrlTask.ID)
	if err != nil {
		t.Fatalf("GetTask ctrlTask: %v", err)
	}
	if !gotCtrl.AutoMerge {
		t.Fatalf("control task under ShadowOnControl unexpectedly lost AutoMerge")
	}
	for _, l := range gotCtrl.Links {
		if l.Kind == model.LinkQATriageHold {
			t.Fatalf("control task must never get LinkQATriageHold watermark")
		}
	}

	// 4. Treatment T1 task with LinkSecurityHold fails closed and withdraws AutoMerge.
	secTask := model.Task{
		ID:        "t1-sec-hold",
		Title:     "T1 task with active security hold",
		Target:    &repo,
		AutoMerge: true,
		Links:     []model.Link{{Kind: model.LinkSecurityHold, Target: "trust-boundary"}},
	}
	if err := store.PutTask(ctx, secTask); err != nil {
		t.Fatalf("PutTask secTask: %v", err)
	}
	orchestrator.EvaluateAndApplyQATriage(ctx, store, nil, secTask, assign, "4444444444444444", []string{"pkg/util/math.go"}, true)
	gotSec, err := store.GetTask(ctx, secTask.ID)
	if err != nil {
		t.Fatalf("GetTask secTask: %v", err)
	}
	if gotSec.AutoMerge {
		t.Fatalf("expected LinkSecurityHold to withdraw AutoMerge under enforced S5 triage")
	}
}

func TestPreCloneS0ClarificationHoldParksTaskWithoutClone(t *testing.T) {
	store, ctx := openTestStore(t)

	prev := orchestrator.LiveQARuntime()
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(prev) })

	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, runPrefix, _ string, _ int) (string, int64, int64, error) {
			if strings.HasSuffix(runPrefix, "-qa-s0") {
				return "status: needs_clarification\nquestion: Should expired tokens return 401 or 403?\n- WHEN token is valid SHALL forward request", 50, 22, nil
			}
			return "", 0, 0, nil
		},
	})

	// 1. Direct ExecutePreCloneQAPhase with OnClarification="ask_human" returns NeedsClarification=true and records verdict="hold".
	askSpec := &qa.FlowSpec{
		S0Intent:        true,
		IntentSkillText: "Extract EARS criteria",
		S0IntentConfig: qa.S0IntentDecl{
			Enabled:         true,
			Complexities:    []string{"low", "medium", "high"},
			MaxSeconds:      10,
			MaxTokens:       8000,
			OnClarification: "ask_human",
		},
	}
	taskAsk := model.Task{
		ID:    "task-s0-ask",
		Title: "Handle expired tokens",
		Body:  "Update auth middleware for expired tokens.",
	}
	resAsk := orchestrator.ExecutePreCloneQAPhase(ctx, store, taskAsk, askSpec, "exp-v4", "golden-v4", "sha256:v4", "base-prompt")
	if !resAsk.NeedsClarification {
		t.Fatalf("expected NeedsClarification=true for OnClarification=ask_human, got %+v", resAsk)
	}
	if resAsk.Question != "Should expired tokens return 401 or 403?" {
		t.Fatalf("unexpected Question=%q", resAsk.Question)
	}
	runsAsk, err := store.ListQACheckRunsForTask(ctx, taskAsk.ID)
	if err != nil || len(runsAsk) != 1 || runsAsk[0].Stage != "S0" || runsAsk[0].Verdict != "hold" {
		t.Fatalf("expected S0 check run with verdict=hold, got %+v (err=%v)", runsAsk, err)
	}

	// 2. ExecutePreCloneQAPhase with OnClarification="warn" records verdict="warn", NeedsClarification=false, and appends checklist.
	warnSpec := *askSpec
	warnSpec.S0IntentConfig.OnClarification = "warn"
	taskWarn := model.Task{
		ID:    "task-s0-warn",
		Title: "Handle expired tokens",
		Body:  "Update auth middleware for expired tokens.",
	}
	resWarn := orchestrator.ExecutePreCloneQAPhase(ctx, store, taskWarn, &warnSpec, "exp-v4", "golden-v4", "sha256:v4", "base-prompt")
	if resWarn.NeedsClarification {
		t.Fatalf("expected NeedsClarification=false for OnClarification=warn, got %+v", resWarn)
	}
	if !strings.Contains(resWarn.Prompt, "## EARS Intent Checklist") {
		t.Fatalf("expected checklist appended on warn mode, got %q", resWarn.Prompt)
	}
	runsWarn, err := store.ListQACheckRunsForTask(ctx, taskWarn.ID)
	if err != nil || len(runsWarn) != 1 || runsWarn[0].Verdict != "warn" {
		t.Fatalf("expected S0 check run with verdict=warn, got %+v (err=%v)", runsWarn, err)
	}

	// 3. Full RunCycle dispatch with ask_human parks task in awaiting_reply without cloning or invoking coding agent.
	sim, client := newSim(t, "acme", "widgets", "main")
	_ = sim
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	askHumanYAML := `schema: grain.dev/qa-flow/v4
key: v4-ask-human
extends: golden-v3@1
label: v4 ask human
summary: Pre-clone S0 clarification hold test
stages:
  s0-intent:
    enabled: true
    complexities: [low, medium, high]
    max-seconds: 10
    max-tokens: 8000
    on-clarification: ask_human
`
	bundle, err := qa.ResolveAnyFlowDeclaration([]byte(askHumanYAML), 1, nil)
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
	if err := store.PutConfig(ctx, model.Config{
		QA: model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowGoldenV1Minimal,
			RolloutPct: 100,
			Epoch:      1,
			RegimeID:   1,
		},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	dispatchTask := filedTask(t, ctx, store, "s0park", repo)
	if _, err := store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         model.ResolveQARootOrigin(dispatchTask.WorkOrigin, dispatchTask.ID),
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             bundle.Key,
		RolloutPctAtAssign: 100,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment: %v", err)
	}

	agentCalled := false
	fw := openingFramework{agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
		agentCalled = true
		return &agent.Result{}, nil
	})}
	sandboxDir := t.TempDir()
	// Intentionally pass an invalid GitRemoteBase so any clone attempt would fail if reached.
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(sandboxDir),
		Config: orchestrator.Config{
			GitRemoteBase: filepath.Join(t.TempDir(), "nonexistent-remotes"),
		},
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		t.Fatalf("RunCycle with pre-clone S0 ask_human hold: %v", err)
	}
	if agentCalled {
		t.Fatalf("coding agent framework must not be invoked when pre-clone S0 holds for clarification")
	}
	obs, err := store.GetObservation(ctx, dispatchTask.ID)
	if err != nil || obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("expected task parked with PendingQuestionCommentID set, got obs=%+v (err=%v)", obs, err)
	}
}

func TestPostCloneWorkflowPhasesContextDecomposeCriticBouncesAndGuidance(t *testing.T) {
	store, ctx := openTestStore(t)

	prev := orchestrator.LiveQARuntime()
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(prev) })

	// Create a synthetic checkout directory with Go and TS files + sibling importers.
	worktreeDir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(worktreeDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/testrepo\n\ngo 1.22\n")
	mustWrite("pkg/orchestrator/run.go", "package orchestrator\n\nfunc RunDispatch() {}\n")
	mustWrite("pkg/daemon/server.go", "package daemon\n\nimport \"example.com/testrepo/pkg/orchestrator\"\n\nfunc Start() { orchestrator.RunDispatch() }\n")
	mustWrite("ui/src/api.ts", "export function fetchStatus() { return 'ok'; }\n")
	mustWrite("ui/src/dashboard/view.ts", "import { fetchStatus } from '../api';\nexport const v = fetchStatus();\n")

	var s2Prompts []string
	s2Calls := 0
	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, runPrefix, prompt string, _ int) (string, int64, int64, error) {
			if strings.Contains(runPrefix, "-qa-s2") {
				s2Calls++
				s2Prompts = append(s2Prompts, prompt)
				if s2Calls == 1 {
					return "REVISE_PLAN:\n- Missing update to caller pkg/daemon/server.go", 120, 40, nil
				}
				return "- Ensure pkg/daemon/server.go and pkg/orchestrator/run.go stay in sync", 130, 35, nil
			}
			return "", 0, 0, nil
		},
	})

	spec := &qa.FlowSpec{
		Context: qa.ContextDiscoveryDecl{
			Enabled:        true,
			MaxTokens:      1500,
			IncludeCallers: true,
			ExtraPaths:     []string{"ui/src/api.ts"},
		},
		Decompose: qa.DecomposeDecl{
			Enabled:         true,
			Mode:            "slice_plan",
			Complexities:    []string{"low", "medium", "high"},
			TargetDiffLines: 180,
		},
		RiskOverrides: []qa.RiskOverride{
			{
				Paths:   []string{"pkg/orchestrator/**"},
				MinTier: "T3",
				Reason:  "core orchestrator",
			},
		},
		S2PlanCritic:        true,
		PlanCriticSkillText: "Verify sibling callers",
		S2PlanCriticConfig: qa.S2PlanCriticDecl{
			Enabled:      true,
			Complexities: []string{"high"}, // task complexity is low/medium, but Tier is T3!
			Tiers:        []string{"T2", "T3"},
			MaxSeconds:   15,
			MaxTokens:    20000,
			MaxBounces:   1,
		},
		ReadabilityPacks: []qa.ReadabilityPackDecl{
			{
				ID:         "go",
				Enabled:    true,
				Activation: "auto",
			},
		},
		SkillSteps: []qa.SkillStepDecl{
			{
				ID:      "step-coding-contract",
				Stage:   "coding",
				Enabled: true,
				Text:    "Always verify error wrapping on orchestrator boundaries.",
			},
		},
		PRContract: qa.PRContractDecl{
			Enabled:          true,
			Mode:             "blocking",
			MaxSubjectChars:  72,
			RequiredSections: []string{"Summary", "Test Plan"},
		},
	}

	task := model.Task{
		ID:    "task-post-clone-1",
		Title: "Update pkg/orchestrator/run.go dispatch",
		Body:  "Touch pkg/orchestrator/run.go and keep callers working.",
	}

	out := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, task, worktreeDir, spec, "exp-v4", "golden-v4", "sha256:v4", "base-prompt")

	// Verify S1 context discovery section and sibling importers.
	if !strings.Contains(out, "## Codebase & Caller Context") || !strings.Contains(out, `<codebase-context>`) {
		t.Fatalf("expected ## Codebase & Caller Context with <codebase-context>, got:\n%s", out)
	}
	if !strings.Contains(out, "pkg/orchestrator/run.go") || !strings.Contains(out, "pkg/daemon") || !strings.Contains(out, "ui/src/dashboard") {
		t.Fatalf("expected discovered target paths and Go/TS sibling importer directories in context, got:\n%s", out)
	}

	// Verify S0b slice_plan instructions.
	if !strings.Contains(out, "## Goal Subtask Decomposition (S0b — slice_plan)") || !strings.Contains(out, "180 diff lines") {
		t.Fatalf("expected S0b slice_plan section in output, got:\n%s", out)
	}

	// Verify S2 ran with MaxBounces=1 (1 initial + 1 refinement = 2 calls) because Tier T3 matched S2PlanCriticConfig.Tiers,
	// and received the discovered <codebase-context>.
	if s2Calls != 2 {
		t.Fatalf("expected 2 S2 calls (initial REVISE_PLAN + 1 bounce), got %d", s2Calls)
	}
	if !strings.Contains(s2Prompts[0], "<codebase-context>") {
		t.Fatalf("expected S2 prompt to include discovered <codebase-context>, got:\n%s", s2Prompts[0])
	}
	if !strings.Contains(out, "## S2 Plan Critique") || !strings.Contains(out, "Ensure pkg/daemon/server.go and pkg/orchestrator/run.go stay in sync") {
		t.Fatalf("expected refined S2 Plan Critique in output, got:\n%s", out)
	}

	// Verify ActiveCodingGuidance injected ReadabilityPacks, SkillSteps, and PRContract.
	if !strings.Contains(out, "pack:go") ||
		!strings.Contains(out, "Always verify error wrapping on orchestrator boundaries.") ||
		!strings.Contains(out, "PR Contract (mode: blocking)") {
		t.Fatalf("expected ActiveCodingGuidance (ReadabilityPacks, SkillSteps, PRContract) in output, got:\n%s", out)
	}

	// Verify S1, S0b, and S2 check runs were recorded.
	runs, err := store.ListQACheckRunsForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListQACheckRunsForTask: %v", err)
	}
	seenStages := map[string]bool{}
	for _, r := range runs {
		seenStages[r.Stage+":"+r.CheckID] = true
	}
	for _, want := range []string{
		"S1:qa:context-discovery",
		"S0b:step:goal-subtask-decomposer",
		"S2:qa:plan-critic",
	} {
		if !seenStages[want] {
			t.Fatalf("missing recorded check run %q in %+v", want, runs)
		}
	}
}

func TestRunGoContainsNoInternalProxyProductName(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("ReadFile(run.go): %v", err)
	}
	if !strings.Contains(string(raw), "an authenticating reverse proxy") {
		t.Fatalf("expected generic wording 'an authenticating reverse proxy' in pkg/orchestrator/run.go")
	}
}

func TestPostCloneWorkflowPhasesConfinesSymlinksAndTraversal(t *testing.T) {
	store, ctx := openTestStore(t)

	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "secret.go")
	const secretMarker = "SUPER_SECRET_HOST_CREDENTIAL_DO_NOT_LEAK"
	if err := os.WriteFile(secretFile, []byte("package secret\nconst Token = \""+secretMarker+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideSub := filepath.Join(outsideDir, "hostpkg")
	if err := os.MkdirAll(outsideSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSub, "leak.go"), []byte("package hostpkg\nimport \"example.com/repo/pkg/safe\"\nconst K = \""+secretMarker+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	worktreeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreeDir, "pkg", "safe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "pkg", "safe", "ok.go"), []byte("package safe\nfunc OK() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlinked file and symlinked directory inside worktree pointing outside worktreeDir.
	if err := os.Symlink(secretFile, filepath.Join(worktreeDir, "pkg", "safe", "symlink_secret.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSub, filepath.Join(worktreeDir, "pkg", "linked_outside")); err != nil {
		t.Fatal(err)
	}

	spec := &qa.FlowSpec{
		Context: qa.ContextDiscoveryDecl{
			Enabled:        true,
			MaxTokens:      1500,
			IncludeCallers: true,
			ExtraPaths:     []string{"../secret.go", "/etc/passwd", "pkg/linked_outside/leak.go", "pkg/safe/symlink_secret.go"},
		},
	}
	task := model.Task{
		ID:    "task-symlink-confine",
		Title: "Inspect pkg/safe/ok.go and pkg/safe/symlink_secret.go",
		Body:  "Check ../../secret.go and ../hostpkg/leak.go and pkg/linked_outside/leak.go.",
	}

	out := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, task, worktreeDir, spec, "exp-sec", "golden-v4", "sha256:sec", "base-prompt")
	if strings.Contains(out, secretMarker) {
		t.Fatalf("symlink escape leaked host file contents into prompt:\n%s", out)
	}
	if strings.Contains(out, "symlink_secret.go") || strings.Contains(out, "linked_outside") || strings.Contains(out, "hostpkg") || strings.Contains(out, "../") {
		t.Fatalf("symlink or traversal path leaked into prompt:\n%s", out)
	}
	if !strings.Contains(out, "pkg/safe/ok.go") {
		t.Fatalf("expected legitimate in-tree file pkg/safe/ok.go in prompt, got:\n%s", out)
	}
}

func TestPreCloneS0AskHumanDoesNotReparkAfterPriorHold(t *testing.T) {
	store, ctx := openTestStore(t)

	prev := orchestrator.LiveQARuntime()
	t.Cleanup(func() { orchestrator.SetLiveQARuntime(prev) })

	orchestrator.SetLiveQARuntime(&orchestrator.QARuntime{
		RunIsolatedPrompt: func(_ context.Context, _ model.Task, _ string, _ string, _ int) (string, int64, int64, error) {
			return "STATUS: NEEDS_CLARIFICATION\nQuestion: Which endpoint should be updated?", 30, 15, nil
		},
	})

	spec := &qa.FlowSpec{
		S0Intent: true,
		S0IntentConfig: qa.S0IntentDecl{
			Enabled:         true,
			Complexities:    []string{"low", "medium", "high"},
			MaxSeconds:      10,
			MaxTokens:       8000,
			OnClarification: "ask_human",
		},
	}
	task := model.Task{ID: "task-s0-once", Title: "Update endpoint", Body: "Fix the response payload"}

	res1 := orchestrator.ExecutePreCloneQAPhase(ctx, store, task, spec, "exp-1", "golden-v4", "sha256:v4", "base")
	if !res1.NeedsClarification {
		t.Fatalf("expected first S0 pass to request clarification hold")
	}

	res2 := orchestrator.ExecutePreCloneQAPhase(ctx, store, task, spec, "exp-1", "golden-v4", "sha256:v4", "base")
	if res2.NeedsClarification {
		t.Fatalf("expected second S0 pass after prior hold not to re-park the task in an infinite loop")
	}
}

func TestPostCloneDecomposeProposeDAGCoordinatorAndStepDown(t *testing.T) {
	store, ctx := openTestStore(t)
	worktreeDir := t.TempDir()

	stdBundle, ok := qa.LookupBuiltInOrRegisteredVersion("standard-workflow@1")
	if !ok {
		t.Fatal("standard-workflow@1 not found")
	}
	spec := stdBundle.Resolved
	spec.S2PlanCritic = false // isolate S0b decomposition contract

	// 1. High-complexity root task gets the Coordinator-or-Execute propose_dag contract.
	highRoot := model.Task{
		ID:    "task-high-root",
		Title: "End-to-end schema migration and multi-surface UI rollout",
		Body: "Milestone 1: Add SQLite schema migration and store queries in pkg/model/qa.go.\n" +
			"Milestone 2: Wire HTTP routes and handlers in pkg/ui/qa_api.go and pkg/orchestrator/qa_phases.go.\n" +
			"Milestone 3: Build Angular workstream badges across ui-ng/src/app/tasks/tasks-page.component.ts and ui-ng/src/app/inbox/inbox-page.component.ts.\n" +
			"Milestone 4: Add Playwright and Go regression suites in tests/security/rules_test.go.",
	}
	outHighRoot := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, highRoot, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if !strings.Contains(outHighRoot, "(S0b — propose_dag)") || !strings.Contains(outHighRoot, "nothing_needed") {
		t.Fatalf("expected propose_dag Coordinator-or-Execute contract on high root task, got:\n%s", outHighRoot)
	}

	// 2. High-complexity child subtask (Proposer.TaskID != "") steps down to single-PR slice_plan to prevent recursive DAG fan-out.
	highChild := highRoot
	highChild.ID = "task-high-child"
	highChild.Proposer = model.TaskProposer{TaskID: "task-high-root", RunID: "run-1"}
	outHighChild := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, highChild, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if !strings.Contains(outHighChild, "(S0b — slice_plan)") || strings.Contains(outHighChild, "(S0b — propose_dag)") {
		t.Fatalf("expected child subtask to step down to slice_plan, got:\n%s", outHighChild)
	}

	// 2b. High-complexity root task filed from a Console Session / Director (SourceSession) keeps propose_dag.
	sessionRoot := highRoot
	sessionRoot.ID = "task-session-root"
	sessionRoot.WorkOrigin = model.TaskOrigin("session-director")
	sessionRoot.Proposer = model.TaskProposer{
		TaskID: "session-director",
		Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalAgent, ID: "session-director", Source: model.SourceSession},
		},
	}
	outSessionRoot := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, sessionRoot, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if !strings.Contains(outSessionRoot, "(S0b — propose_dag)") {
		t.Fatalf("expected Console/Director-filed root task (SourceSession) to keep propose_dag, got:\n%s", outSessionRoot)
	}

	// 2c. External follow-up task bound to an existing workstream root (WorkOrigin == task:<rootID>) steps down to slice_plan.
	originChild := highRoot
	originChild.ID = "task-origin-child"
	originChild.WorkOrigin = model.TaskOrigin("task-high-root")
	outOriginChild := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, originChild, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if !strings.Contains(outOriginChild, "(S0b — slice_plan)") || strings.Contains(outOriginChild, "(S0b — propose_dag)") {
		t.Fatalf("expected external workstream child task to step down to slice_plan, got:\n%s", outOriginChild)
	}

	// 3. Medium-complexity root task (score 40: Design-Item + cross-surface) steps down to single-PR slice_plan.
	medRoot := model.Task{
		ID:    "task-med-root",
		Title: "Add filter parameter to backend handler and UI table",
		Body:  "Design-Item: DI-7\nUpdate pkg/ui/qa_api.go and ui-ng/src/app/tasks/tasks-page.component.ts to pass the filter query.",
	}
	outMedRoot := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, medRoot, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if !strings.Contains(outMedRoot, "(S0b — slice_plan)") || strings.Contains(outMedRoot, "(S0b — propose_dag)") {
		t.Fatalf("expected medium root task to step down to slice_plan, got:\n%s", outMedRoot)
	}

	// 4. Low-complexity root task skips S0b decomposition altogether.
	lowRoot := model.Task{
		ID:    "task-low-root",
		Title: "Fix typo in README badge label",
		Body:  "Correct spelling in pkg/model/qa.go comment.",
	}
	outLowRoot := orchestrator.ExecutePostCloneWorkflowPhases(ctx, store, lowRoot, worktreeDir, &spec, "exp-dag", "standard-workflow", stdBundle.Hash, "base")
	if strings.Contains(outLowRoot, "(S0b —") {
		t.Fatalf("expected low root task to skip S0b decomposition, got:\n%s", outLowRoot)
	}
}
