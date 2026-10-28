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
	"errors"
	"strings"
	"testing"
)

type testMergeQueueReader struct {
	report MergeQueueReport
	err    error
	repo   string
	branch string
}

func (r *testMergeQueueReader) QueryMergeQueue(ctx context.Context, repo, branch string) (MergeQueueReport, error) {
	r.repo = repo
	r.branch = branch
	return r.report, r.err
}

func TestQueryMergeQueueNilReader(t *testing.T) {
	tools := NewMergeQueueTools(nil)
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	res := tools[0].Handler(t.Context(), map[string]any{})
	if res.Text != noMergeQueueReader {
		t.Fatalf("got %q, want %q", res.Text, noMergeQueueReader)
	}
}

func TestQueryMergeQueueOutput(t *testing.T) {
	reader := &testMergeQueueReader{
		report: MergeQueueReport{
			Repo:      "bwsalmon/grain",
			Branch:    "main",
			Paused:    true,
			PauseInfo: "Hold for scheduled upgrade",
			HeldGates: []HeldGateReport{
				{
					Repo:      "bwsalmon/grain",
					Branch:    "main",
					Gate:      "grain-ci",
					Status:    "failing",
					Reason:    "base branch has failing suite",
					Broken:    true,
					Confirmed: true,
				},
			},
			Batches: []MergeBatchReport{
				{
					Repo:       "bwsalmon/grain",
					Branch:     "main",
					Ref:        "grain-merge-queue/main",
					Commit:     "1234567890abcdef",
					BaseCommit: "abcdef1234567890",
					RunID:      "run-999",
					Conclusion: "running",
					Members: []MergeBatchMemberReport{
						{
							TaskID: "task-42",
							Number: 101,
							Title:  "feat: add new feature",
							Commit: "feedbeefcafebabe",
						},
					},
				},
			},
			QueuedPRs: []QueuedPRReport{
				{
					TaskID:  "task-43",
					Number:  102,
					Title:   "fix: resolve bug",
					Repo:    "bwsalmon/grain",
					Branch:  "main",
					State:   "waiting_ci",
					Note:    "Waiting on checks for commit 12345678",
					Stalled: false,
					Blocked: false,
				},
			},
		},
	}

	tool := queryMergeQueueTool(reader)
	res := tool.Handler(t.Context(), map[string]any{"repo": "bwsalmon/grain", "branch": "main", "view": "full"})

	if reader.repo != "bwsalmon/grain" || reader.branch != "main" {
		t.Fatalf("reader query args = (%q, %q), want (bwsalmon/grain, main)", reader.repo, reader.branch)
	}

	out := res.Text
	if !strings.Contains(out, "Merge Queue Status for bwsalmon/grain (main)") {
		t.Errorf("missing title in output: %s", out)
	}
	if !strings.Contains(out, "Pipeline Status:** PAUSED") {
		t.Errorf("missing pipeline pause in output: %s", out)
	}
	if !strings.Contains(out, "Gate `grain-ci` is failing [BROKEN] [CONFIRMED]") {
		t.Errorf("missing held gate in output: %s", out)
	}
	if !strings.Contains(out, "Batches in Flight (1)") || !strings.Contains(out, "Task task-42") {
		t.Errorf("missing batch in flight in output: %s", out)
	}
	if !strings.Contains(out, "Queued Pull Requests (1)") || !strings.Contains(out, "Task task-43") {
		t.Errorf("missing queued PR in output: %s", out)
	}
}

func TestQueryMergeQueueEmpty(t *testing.T) {
	reader := &testMergeQueueReader{
		report: MergeQueueReport{
			Repo: "bwsalmon/grain",
		},
	}
	tool := queryMergeQueueTool(reader)
	res := tool.Handler(t.Context(), map[string]any{"repo": "bwsalmon/grain", "view": "full"})
	if !strings.Contains(res.Text, "The merge queue is currently idle and clear") {
		t.Fatalf("expected idle and clear output, got: %s", res.Text)
	}
}

func TestQueryMergeQueueError(t *testing.T) {
	reader := &testMergeQueueReader{
		err: errors.New("backend store offline"),
	}
	tool := queryMergeQueueTool(reader)
	res := tool.Handler(t.Context(), map[string]any{})
	if !strings.Contains(res.Text, "backend store offline") {
		t.Fatalf("expected error message in result, got: %s", res.Text)
	}
}
