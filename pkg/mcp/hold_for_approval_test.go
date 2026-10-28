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

// hold_for_approval reaches the filer as asked, and the result says not
// only whether the proposal waits for a person but why: because the call
// asked, or because of what the filer says the permission does not cover.
func TestProposeTaskHoldForApprovalIsPassedOnAndTheReasonSaid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  map[string]any
		filed FiledProposal
		want  []string
		hold  bool
	}{
		{
			name:  "auto, covered",
			args:  map[string]any{"title": "t", "body": "b"},
			filed: FiledProposal{ID: "7", Queued: true},
			want:  []string{"filed approved", "your own task's permission covers it"},
		},
		{
			name: "auto, not covered",
			args: map[string]any{"title": "t", "body": "b"},
			filed: FiledProposal{ID: "7",
				ApprovalNote: "your permission does not cover it: your task is not an auto-merge job"},
			want: []string{"filed unapproved because your permission does not cover it: your task is not an auto-merge job"},
		},
		{
			name: "hold, covered",
			args: map[string]any{"title": "t", "body": "b", "hold_for_approval": true},
			filed: FiledProposal{ID: "7", Held: true,
				ApprovalNote: "you asked to hold it (hold_for_approval); your permission would otherwise have queued it"},
			want: []string{"filed unapproved because you asked to hold it"},
			hold: true,
		},
		{
			name:  "hold, filer gives no reason",
			args:  map[string]any{"title": "t", "body": "b", "hold_for_approval": true},
			filed: FiledProposal{ID: "7"},
			want:  []string{"filed unapproved because you asked to hold it"},
			hold:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got ProposedTask
			tool := NewLiveProposalTools(&MockSink{}, nil, "439", filerFunc(
				func(_ context.Context, p ProposedTask, _ string) (FiledProposal, error) {
					got = p
					return tc.filed, nil
				}))[0]
			res := tool.Handler(context.Background(), tc.args)
			if res.IsError {
				t.Fatalf("result = %+v", res)
			}
			if got.HoldForApproval != tc.hold {
				t.Errorf("HoldForApproval = %v, want %v", got.HoldForApproval, tc.hold)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result = %q, want it to say %q", res.Text, w)
				}
			}
		})
	}
}

// Recorded for the end-of-run relay, a held proposal is told it will
// wait for a human whatever the permission covers.
func TestRecordedProposalSaysItIsHeld(t *testing.T) {
	sink := &MockSink{}
	res := proposeTaskTool(sink, nil, "", nil).Handler(context.Background(),
		map[string]any{"title": "t", "body": "b", "hold_for_approval": true})
	if res.IsError || !strings.Contains(res.Text, "asked to hold") {
		t.Fatalf("result = %+v", res)
	}
	if got := sink.ProposedTasks(); len(got) != 1 || !got[0].HoldForApproval {
		t.Fatalf("recorded = %+v, want one held proposal", got)
	}
}
