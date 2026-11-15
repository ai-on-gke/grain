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
	"strings"
	"testing"
)

func commentOnIssue(t *testing.T, sink *MockSink) Tool {
	t.Helper()
	for _, tool := range NewMockTools(sink, nil) {
		if tool.Name == "comment_on_issue" {
			return tool
		}
	}
	t.Fatal("NewMockTools has no comment_on_issue")
	return Tool{}
}

const adminNote = "I didn't change any code. These release failures aren't caused by a bug.\n\n" +
	"Only someone with admin on acme/widgets can set GRAIN_MERGE_LOGINS."

// The safety net: a note that reads as needing a person, with no summary,
// is turned back once with a pointer to ask_question -- as an error, so
// it is not the call relayed -- and the same note sent again is recorded.
func TestCommentOnIssueNudgesANoteThatNeedsAPersonOnce(t *testing.T) {
	t.Parallel()
	sink := &MockSink{}
	tool := commentOnIssue(t, sink)

	res := tool.Handler(context.Background(), map[string]any{"comment": adminNote})
	if !res.IsError {
		t.Fatalf("first call = %q, want it turned back", res.Text)
	}
	for _, want := range []string{"ask_question", "summary", "numbered checklist", "Only someone with admin"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("nudge does not mention %q:\n%s", want, res.Text)
		}
	}
	if sink.Comment() != "" {
		t.Fatalf("nudged note was recorded: %q", sink.Comment())
	}

	res = tool.Handler(context.Background(), map[string]any{"comment": adminNote})
	if res.IsError {
		t.Fatalf("second call refused: %s", res.Text)
	}
	if sink.Comment() != adminNote {
		t.Fatalf("recorded %q, want the note", sink.Comment())
	}
}

func TestCommentOnIssueTakesASummaryWithoutNudging(t *testing.T) {
	t.Parallel()
	sink := &MockSink{}
	res := commentOnIssue(t, sink).Handler(context.Background(), map[string]any{
		"comment": adminNote, "summary": "Set GRAIN_MERGE_LOGINS on acme/widgets to unblock publishing",
	})
	if res.IsError || sink.Comment() != adminNote {
		t.Fatalf("call with a summary = %q (recorded %q), want it recorded", res.Text, sink.Comment())
	}
}

func TestCommentOnIssueRecordsAReportWithoutASummary(t *testing.T) {
	t.Parallel()
	sink := &MockSink{}
	res := commentOnIssue(t, sink).Handler(context.Background(), map[string]any{
		"comment": "## Infra review\n\nEverything is healthy.",
	})
	if res.IsError {
		t.Fatalf("a plain report was turned back: %s", res.Text)
	}
}

func TestCommentOnIssueRefusesASummaryThatIsNotOneLine(t *testing.T) {
	t.Parallel()
	for _, summary := range []string{"two\nlines", strings.Repeat("x", 101)} {
		res := commentOnIssue(t, &MockSink{}).Handler(context.Background(), map[string]any{
			"comment": "a report", "summary": summary,
		})
		if !res.IsError {
			t.Errorf("summary %q accepted, want refused", summary)
		}
	}
}

func TestCommentOnIssueDeclaresAnOptionalSummary(t *testing.T) {
	t.Parallel()
	tool := commentOnIssue(t, &MockSink{})
	props := tool.InputSchema["properties"].(map[string]any)
	s, ok := props["summary"].(map[string]any)
	if !ok || s["maxLength"] != 100 {
		t.Fatalf("summary property = %v, want one of at most 100 characters", props["summary"])
	}
	for _, r := range tool.InputSchema["required"].([]string) {
		if r == "summary" {
			t.Fatal("summary is required, want it optional")
		}
	}
}
