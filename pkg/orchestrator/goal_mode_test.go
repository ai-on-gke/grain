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

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

type scriptedGoalFramework struct {
	passes []*agent.Result
	calls  []agent.RunConfig
	onPass func(pass int)
}

func (f *scriptedGoalFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.calls = append(f.calls, cfg)
	idx := len(f.calls)
	if f.onPass != nil {
		f.onPass(idx)
	}
	// Pass 1 is already passed in as initial result to runGoalVerifierPasses, so
	// f.Run is invoked starting at Pass 2 (index 0 in f.passes).
	if len(f.passes) == 0 {
		return &agent.Result{ResumableSession: "sess-1"}, nil
	}
	res := f.passes[0]
	f.passes = f.passes[1:]
	return res, nil
}

func (f *scriptedGoalFramework) EnforcesStopRule() bool { return true }

func TestGoalBuildPromptAndRuntimeBound(t *testing.T) {
	task := model.Task{
		ID:    "101",
		Title: "Implement goal mode",
		Body:  "Build autonomous verifier",
	}
	if err := model.SetGoalMode(&task, &model.GoalModeConfig{
		Enabled:            true,
		MaxPasses:          6,
		WallClockMinutes:   45,
		MockMissingSecrets: true,
	}); err != nil {
		t.Fatalf("SetGoalMode: %v", err)
	}

	prompt := BuildPrompt(task, "/workspace/work", "/workspace", false, 45*time.Minute, History{}, nil)
	if !strings.Contains(prompt, "Autonomous Goal Posture") {
		t.Fatal("expected BuildPrompt to include AutonomousGoalInstruction")
	}
	if !strings.Contains(prompt, "mockMissingSecrets") {
		t.Fatal("expected BuildPrompt to mention mockMissingSecrets posture")
	}
	if strings.Contains(prompt, questionSummarySection) {
		t.Fatal("expected BuildPrompt to suppress questionSummarySection under GoalMode")
	}
	if strings.Contains(prompt, personActionSection) {
		t.Fatal("expected BuildPrompt to suppress personActionSection under GoalMode")
	}

	cfg := goalModeDispatch(task, Config{MaxRunRuntime: 2 * time.Hour})
	if cfg.maxRunRuntime() != 45*time.Minute {
		t.Fatalf("goalModeDispatch maxRunRuntime = %v, want 45m", cfg.maxRunRuntime())
	}
}

func TestGoalMultiPassContinuationUntilCompleteGoalPasses(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)

	task := model.Task{
		ID:        "201",
		Title:     "Multi-pass feature",
		Body:      "Deliver feature and tests",
		CreatedAt: &now,
	}
	_ = model.SetGoalMode(&task, &model.GoalModeConfig{Enabled: true, MaxPasses: 4})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	pass1Result := &agent.Result{
		FinalText:        "First pass edit",
		ResumableSession: "sess-201",
		ToolCalls: []agent.ToolCall{
			{
				Name:      "edit_file",
				Arguments: map[string]any{"file_path": "main.go"},
			},
			{
				Name: "record_assumption",
				Arguments: map[string]any{
					"topic":        "Default timeout",
					"decision":     "Use 120 minutes",
					"rationale":    "Matches default run clock",
					"alternatives": "60 minutes",
				},
			},
		},
	}

	fw := &scriptedGoalFramework{
		passes: []*agent.Result{
			{
				FinalText:        "Verified and completed",
				ResumableSession: "sess-201",
				ToolCalls: []agent.ToolCall{
					{
						Name: "record_assumption",
						Arguments: map[string]any{
							"topic":    "Default timeout",
							"decision": "Use 120 minutes",
						},
					},
					{
						Name: "complete_goal",
						Arguments: map[string]any{
							"summary": "Implemented feature and verified tests",
							"deliverables": []any{
								map[string]any{
									"requirement": "Feature works",
									"evidence":    "go test ./... passed",
									"verified":    true,
								},
							},
						},
					},
				},
			},
		},
	}

	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, Now: func() time.Time { return now }}
	merged, err := runGoalVerifierPasses(ctx, ctx, store, cfg, fw, agent.RunConfig{TaskID: task.ID}, nil, &task, "201-1", "", nil, pass1Result, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses: %v", err)
	}
	if len(fw.calls) != 1 {
		t.Fatalf("expected 1 continuation pass call, got %d", len(fw.calls))
	}
	if !strings.Contains(fw.calls[0].ContinuePrompt, "Pass 2/4") || !strings.Contains(fw.calls[0].ContinuePrompt, "complete_goal") {
		t.Fatalf("unexpected ContinuePrompt: %s", fw.calls[0].ContinuePrompt)
	}
	if len(merged.ToolCalls) != 4 {
		t.Fatalf("merged.ToolCalls count = %d, want 4", len(merged.ToolCalls))
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: %v", err)
	}
	sc, ok := model.GoalScorecardOf(*stored)
	if !ok || sc == nil {
		t.Fatal("expected GoalScorecard on stored task")
	}
	if !sc.Verified || sc.PassesUsed != 2 || sc.MaxPasses != 4 || sc.StoppedAtCap {
		t.Fatalf("unexpected scorecard: %+v", sc)
	}
	assumptions := model.GoalAssumptionsOf(*stored)
	if len(assumptions) != 1 || assumptions[0].Topic != "Default timeout" {
		t.Fatalf("expected 1 deduplicated assumption, got %+v", assumptions)
	}
}

func TestGoalMaxPassesCapAndLiveDowngrade(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 15, 14, 10, 0, 0, time.UTC)

	// Case 1: Reaching MaxPasses cap without verification sets StoppedAtCap: true.
	taskCap := model.Task{ID: "202", Title: "Cap test", CreatedAt: &now}
	_ = model.SetGoalMode(&taskCap, &model.GoalModeConfig{Enabled: true, MaxPasses: 2})
	if err := store.PutTask(ctx, taskCap); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	pass1 := &agent.Result{
		ResumableSession: "sess-202",
		ToolCalls:        []agent.ToolCall{{Name: "write_file", Arguments: map[string]any{"file_path": "a.go"}}},
	}
	fwCap := &scriptedGoalFramework{
		passes: []*agent.Result{
			{
				ResumableSession: "sess-202",
				ToolCalls: []agent.ToolCall{
					{
						Name: "complete_goal",
						Arguments: map[string]any{
							"summary": "Partial",
							"deliverables": []any{
								map[string]any{"requirement": "Tests", "evidence": "failing", "verified": false},
							},
						},
					},
				},
			},
		},
	}
	cfg := Config{DataDir: t.TempDir(), Now: func() time.Time { return now }}
	_, err := runGoalVerifierPasses(ctx, ctx, store, cfg, fwCap, agent.RunConfig{TaskID: taskCap.ID}, nil, &taskCap, "202-1", "", nil, pass1, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses cap: %v", err)
	}
	storedCap, _ := store.GetTask(ctx, taskCap.ID)
	scCap, ok := model.GoalScorecardOf(*storedCap)
	if !ok || scCap.Verified || !scCap.StoppedAtCap || scCap.PassesUsed != 2 {
		t.Fatalf("expected StoppedAtCap=true at pass 2, got %+v", scCap)
	}

	// Case 2: Live downgrade via store disabling GoalMode stops immediately with "switch to supervised".
	taskDowngrade := model.Task{ID: "203", Title: "Downgrade test", CreatedAt: &now}
	_ = model.SetGoalMode(&taskDowngrade, &model.GoalModeConfig{Enabled: true, MaxPasses: 5})
	if err := store.PutTask(ctx, taskDowngrade); err != nil {
		t.Fatalf("PutTask downgrade: %v", err)
	}
	// Disable GoalMode in store before runGoalVerifierPasses checks after Pass 1.
	if err := store.UpdateTask(ctx, taskDowngrade.ID, func(mt *model.Task) error {
		return model.SetGoalMode(mt, &model.GoalModeConfig{Enabled: false})
	}); err != nil {
		t.Fatalf("UpdateTask disable: %v", err)
	}
	fwDown := &scriptedGoalFramework{}
	_, err = runGoalVerifierPasses(ctx, ctx, store, cfg, fwDown, agent.RunConfig{TaskID: taskDowngrade.ID}, nil, &taskDowngrade, "203-1", "", nil, pass1, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses downgrade: %v", err)
	}
	if len(fwDown.calls) != 0 {
		t.Fatalf("expected 0 continuation passes after live downgrade, got %d", len(fwDown.calls))
	}
	storedDown, _ := store.GetTask(ctx, taskDowngrade.ID)
	scDown, ok := model.GoalScorecardOf(*storedDown)
	if !ok || scDown.StopReason != "switch to supervised" || scDown.PassesUsed != 1 {
		t.Fatalf("expected StopReason=\"switch to supervised\", got %+v", scDown)
	}
}

func TestGoalStallDetectorAbortsAfterTwoEmptyPasses(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 15, 14, 20, 0, 0, time.UTC)

	task := model.Task{ID: "204", Title: "Stall test", CreatedAt: &now}
	_ = model.SetGoalMode(&task, &model.GoalModeConfig{Enabled: true, MaxPasses: 6})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	// Pass 1 has only read_file (0 mutating tool calls) and unchanged git HEAD.
	pass1 := &agent.Result{
		ResumableSession: "sess-204",
		ToolCalls:        []agent.ToolCall{{Name: "read_file", Arguments: map[string]any{"file_path": "README.md"}}},
	}
	// Pass 2 also has 0 mutating tool calls.
	fw := &scriptedGoalFramework{
		passes: []*agent.Result{
			{
				ResumableSession: "sess-204",
				ToolCalls:        []agent.ToolCall{{Name: "read_file", Arguments: map[string]any{"file_path": "README.md"}}},
			},
			{
				ResumableSession: "sess-204",
			},
		},
	}
	cfg := Config{DataDir: t.TempDir(), Now: func() time.Time { return now }}
	_, err := runGoalVerifierPasses(ctx, ctx, store, cfg, fw, agent.RunConfig{TaskID: task.ID}, nil, &task, "204-1", "", nil, pass1, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses stall: %v", err)
	}
	// Only 1 continuation call (Pass 2) should have been made before aborting!
	if len(fw.calls) != 1 {
		t.Fatalf("expected stall abort after Pass 2 (1 continuation call), got %d calls", len(fw.calls))
	}
	stored, _ := store.GetTask(ctx, task.ID)
	sc, ok := model.GoalScorecardOf(*stored)
	if !ok || !sc.StoppedAtCap || sc.StopReason != "stalled without progress" || sc.PassesUsed != 2 {
		t.Fatalf("expected StoppedAtCap=true StopReason=\"stalled without progress\" PassesUsed=2, got %+v", sc)
	}
}

func TestGoalRelayParkingCallsAndProfilerJSONL(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 15, 14, 30, 0, 0, time.UTC)

	task := model.Task{ID: "205", Title: "Parking & profiler test", CreatedAt: &now}
	_ = model.SetGoalMode(&task, &model.GoalModeConfig{
		Enabled:            true,
		MockMissingSecrets: true,
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	resWithParking := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{Name: "ask_question", Arguments: map[string]any{"question": "Which DB?", "summary": "Pick DB"}},
			{Name: "request_access", Arguments: map[string]any{"summary": "Need write", "reason": "Why", "requests": []any{map[string]any{"kind": "network", "access": "open"}}}},
			{Name: "request_secret", Arguments: map[string]any{"secret": "API_KEY", "reason": "Need key", "summary": "Set API_KEY"}},
		},
	}
	parked, err := relayParkingCalls(ctx, store, task, resWithParking, nil, now)
	if err != nil {
		t.Fatalf("relayParkingCalls: %v", err)
	}
	if parked {
		t.Fatal("expected relayParkingCalls not to park when GoalMode and MockMissingSecrets are enabled")
	}

	// Verify GoalPerfProfiler writes valid JSONL to <dataDir>/perf/goal-mode.jsonl.
	dataDir := t.TempDir()
	prof := NewGoalPerfProfiler(dataDir)
	span := prof.Start("205", "205-1", 1, "verifier-pass")
	buf := make([]byte, 4096)
	_ = buf
	sample := span.End(true)
	if sample.TaskID != "205" || sample.RunID != "205-1" || sample.Pass != 1 || !sample.Verified {
		t.Fatalf("unexpected sample: %+v", sample)
	}

	raw, err := os.ReadFile(prof.Path())
	if err != nil {
		t.Fatalf("reading profiler JSONL %s: %v", prof.Path(), err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 JSONL line, got %d", len(lines))
	}
	var decoded GoalPerfSample
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("unmarshaling JSONL line: %v", err)
	}
	if decoded.TaskID != "205" || decoded.Pass != 1 || !decoded.Verified {
		t.Fatalf("decoded JSONL sample = %+v", decoded)
	}

	// Verify Markdown rendering of scorecard and assumptions in PR body while keeping grainAuthored true.
	_ = model.AppendGoalAssumption(&task, model.GoalAssumption{
		Topic:     "Caching",
		Decision:  "In-memory LRU",
		Rationale: "Zero external dependencies",
		Pass:      1,
	})
	_ = model.SetGoalScorecard(&task, model.GoalScorecard{
		PassesUsed: 1,
		MaxPasses:  8,
		Verified:   true,
		Summary:    "Completed all deliverables",
		Deliverables: []model.GoalDeliverable{
			{Requirement: "LRU cache", Evidence: "go test ./pkg/cache passed", Verified: true},
		},
	})
	prBody := withGoalSummary(pullRequestBody("feat: add cache", task.ID, 0), FormatGoalScorecardMarkdown(task))
	if !strings.Contains(prBody, "## Autonomous Goal Scorecard & Assumptions") {
		t.Fatalf("expected scorecard section in PR body, got:\n%s", prBody)
	}
	if !grainAuthored(prBody, task.ID) {
		t.Fatalf("expected grainAuthored to remain true after withGoalSummary, got:\n%s", prBody)
	}
}

func TestGoalNilPassResultDoesNotPanicAndStallsOnGoalMetaTools(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 16, 11, 50, 0, 0, time.UTC)

	// Subcase A: framework.Run returning (nil, nil) on Pass 2 must not panic when checking passResult.ToolCalls.
	taskNil := model.Task{ID: "206", Title: "Nil passResult test", CreatedAt: &now}
	_ = model.SetGoalMode(&taskNil, &model.GoalModeConfig{Enabled: true, MaxPasses: 4})
	if err := store.PutTask(ctx, taskNil); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	pass1 := &agent.Result{
		ResumableSession: "sess-206",
		ToolCalls:        []agent.ToolCall{{Name: "read_file", Arguments: map[string]any{"file_path": "a.go"}}},
	}
	fwNil := &scriptedGoalFramework{
		passes: []*agent.Result{nil, nil},
	}
	cfg := Config{DataDir: t.TempDir(), Now: func() time.Time { return now }}
	_, err := runGoalVerifierPasses(ctx, ctx, store, cfg, fwNil, agent.RunConfig{TaskID: taskNil.ID}, nil, &taskNil, "206-1", "", nil, pass1, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses nil passResult: %v", err)
	}
	storedNil, _ := store.GetTask(ctx, taskNil.ID)
	scNil, ok := model.GoalScorecardOf(*storedNil)
	if !ok || !scNil.StoppedAtCap || scNil.StopReason != "stalled without progress" {
		t.Fatalf("expected stalled abort without panic, got %+v", scNil)
	}

	// Subcase B: record_assumption + complete_goal alone must NOT reset stalledPasses.
	taskMeta := model.Task{ID: "207", Title: "Goal meta-tools stall test", CreatedAt: &now}
	_ = model.SetGoalMode(&taskMeta, &model.GoalModeConfig{Enabled: true, MaxPasses: 6})
	if err := store.PutTask(ctx, taskMeta); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	metaPass1 := &agent.Result{
		ResumableSession: "sess-207",
		ToolCalls:        []agent.ToolCall{{Name: "record_assumption", Arguments: map[string]any{"topic": "t", "decision": "d"}}},
	}
	fwMeta := &scriptedGoalFramework{
		passes: []*agent.Result{
			{
				ResumableSession: "sess-207",
				ToolCalls: []agent.ToolCall{
					{Name: "complete_goal", Arguments: map[string]any{
						"summary":      "partial",
						"deliverables": []any{map[string]any{"requirement": "r", "evidence": "e", "verified": false}},
					}},
				},
			},
		},
	}
	_, err = runGoalVerifierPasses(ctx, ctx, store, cfg, fwMeta, agent.RunConfig{TaskID: taskMeta.ID}, nil, &taskMeta, "207-1", "", nil, metaPass1, nil)
	if err != nil {
		t.Fatalf("runGoalVerifierPasses meta stall: %v", err)
	}
	if len(fwMeta.calls) != 1 {
		t.Fatalf("expected stall abort after 1 continuation call, got %d", len(fwMeta.calls))
	}
}
