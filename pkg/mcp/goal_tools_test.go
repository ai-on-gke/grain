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

package mcp

import (
	"context"
	"slices"
	"testing"
)

func toolNamesOf(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func TestNewGoalTools(t *testing.T) {
	sink := &MockSink{}
	tools := NewGoalTools(sink)
	got := toolNamesOf(tools)
	want := []string{"record_assumption", "complete_goal"}
	if !slices.Equal(got, want) {
		t.Fatalf("NewGoalTools names = %v, want %v", got, want)
	}
	ctx := context.Background()
	for _, tool := range tools {
		if res := tool.Handler(ctx, map[string]any{}); !res.IsError {
			t.Errorf("%s expected error for empty args", tool.Name)
		}
		if res := tool.Handler(ctx, map[string]any{"unknown": "x"}); !res.IsError {
			t.Errorf("%s expected error for unknown arg", tool.Name)
		}
	}
}

func TestNewMockToolsWithOptionsVisibility(t *testing.T) {
	sink := &MockSink{}

	defaultNames := toolNamesOf(NewMockTools(sink, nil))
	for _, want := range []string{"ask_question", "request_secret", "request_access", "comment_on_issue", "nothing_needed", "propose_task"} {
		if !slices.Contains(defaultNames, want) {
			t.Errorf("default NewMockTools missing %q; got %v", want, defaultNames)
		}
	}
	for _, unwanted := range []string{"record_assumption", "complete_goal"} {
		if slices.Contains(defaultNames, unwanted) {
			t.Errorf("default NewMockTools unexpectedly contains %q", unwanted)
		}
	}

	goalKeepSecret := toolNamesOf(NewMockToolsWithOptions(sink, nil, MockToolsOptions{
		AutonomousGoal:     true,
		MockMissingSecrets: false,
	}))
	for _, unwanted := range []string{"ask_question", "request_access"} {
		if slices.Contains(goalKeepSecret, unwanted) {
			t.Errorf("AutonomousGoal(MockMissingSecrets=false) unexpectedly contains %q", unwanted)
		}
	}
	for _, want := range []string{"request_secret", "record_assumption", "complete_goal", "comment_on_issue", "propose_task"} {
		if !slices.Contains(goalKeepSecret, want) {
			t.Errorf("AutonomousGoal(MockMissingSecrets=false) missing %q; got %v", want, goalKeepSecret)
		}
	}

	goalMockSecret := toolNamesOf(NewMockToolsWithOptions(sink, nil, MockToolsOptions{
		AutonomousGoal:     true,
		MockMissingSecrets: true,
	}))
	for _, unwanted := range []string{"ask_question", "request_access", "request_secret"} {
		if slices.Contains(goalMockSecret, unwanted) {
			t.Errorf("AutonomousGoal(MockMissingSecrets=true) unexpectedly contains %q", unwanted)
		}
	}
	for _, want := range []string{"record_assumption", "complete_goal"} {
		if !slices.Contains(goalMockSecret, want) {
			t.Errorf("AutonomousGoal(MockMissingSecrets=true) missing %q", want)
		}
	}
}

func TestGoalToolsSchemasRejectAdditionalProperties(t *testing.T) {
	sink := &MockSink{}
	tools := NewMockToolsWithOptions(sink, nil, MockToolsOptions{AutonomousGoal: true})
	for _, name := range []string{"record_assumption", "complete_goal"} {
		tool := findToolByName(t, tools, name)
		if addProps, ok := tool.InputSchema["additionalProperties"].(bool); !ok || addProps {
			t.Errorf("%s schema additionalProperties = %v, want false", name, tool.InputSchema["additionalProperties"])
		}
	}
}

func TestRecordAssumptionValidationAndSink(t *testing.T) {
	sink := &MockSink{}
	tools := NewMockToolsWithOptions(sink, nil, MockToolsOptions{AutonomousGoal: true})
	rec := findToolByName(t, tools, "record_assumption")
	ctx := context.Background()

	if res := rec.Handler(ctx, map[string]any{"topic": "", "decision": "x"}); !res.IsError {
		t.Error("expected error for empty topic")
	}
	if res := rec.Handler(ctx, map[string]any{"topic": "x", "decision": "   "}); !res.IsError {
		t.Error("expected error for whitespace decision")
	}
	if res := rec.Handler(ctx, map[string]any{"topic": "x", "decision": "y", "unknown": "z"}); !res.IsError {
		t.Error("expected error for unknown argument")
	}

	res := rec.Handler(ctx, map[string]any{
		"topic":        "API pagination",
		"decision":     "Cursor tokens",
		"rationale":    "Consistent with existing endpoints",
		"alternatives": "Page numbers",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	assumptions := sink.GoalAssumptions()
	if len(assumptions) != 1 {
		t.Fatalf("len(GoalAssumptions) = %d, want 1", len(assumptions))
	}
	if assumptions[0].Topic != "API pagination" || assumptions[0].Decision != "Cursor tokens" {
		t.Fatalf("assumption = %+v", assumptions[0])
	}
}

func TestCompleteGoalValidationAndSink(t *testing.T) {
	sink := &MockSink{}
	tools := NewMockToolsWithOptions(sink, nil, MockToolsOptions{AutonomousGoal: true})
	comp := findToolByName(t, tools, "complete_goal")
	ctx := context.Background()

	if res := comp.Handler(ctx, map[string]any{"summary": "", "deliverables": []any{}}); !res.IsError {
		t.Error("expected error for empty summary")
	}
	if res := comp.Handler(ctx, map[string]any{"summary": "done", "deliverables": []any{}}); !res.IsError {
		t.Error("expected error for empty deliverables")
	}
	if res := comp.Handler(ctx, map[string]any{
		"summary": "done",
		"deliverables": []any{
			map[string]any{"requirement": "req", "evidence": "", "verified": true},
		},
	}); !res.IsError {
		t.Error("expected error for empty deliverable evidence")
	}
	if res := comp.Handler(ctx, map[string]any{
		"summary": "done",
		"deliverables": []any{
			map[string]any{"requirement": "req", "evidence": "ok", "verified": true, "extra": 1},
		},
	}); !res.IsError {
		t.Error("expected error for unknown field on deliverable")
	}

	res := comp.Handler(ctx, map[string]any{
		"summary": "All slices implemented and tested",
		"deliverables": []any{
			map[string]any{
				"requirement": "Goal mode links",
				"evidence":    "go test ./pkg/model passed",
				"verified":    true,
			},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	got := sink.GoalCompletion()
	if got == nil || got.Summary != "All slices implemented and tested" || len(got.Deliverables) != 1 {
		t.Fatalf("GoalCompletion = %+v", got)
	}
}
