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

// A proposal recorded for filing at run end is told at the call what
// filing will do with each CI capability it names -- a repository read
// becomes reads, anything else is left off -- since the run has ended
// by the time orchestrator.relayProposedTasks does it
// (docs/test-runs/2026-11-13-proposal-admission.md, F1). A proposal
// naming no CI capability is told nothing new.
func TestRecordedProposalIsToldAboutItsCIOnlyCapabilities(t *testing.T) {
	sink := &MockSink{}
	tool := proposeTaskTool(sink, nil, "", nil)

	res := tool.Handler(context.Background(), map[string]any{
		"title": "Read gadgets", "body": "b",
		"capabilities": []any{"repo-read:acme/gadgets", "repo-write:acme/widgets"},
	})
	if res.IsError {
		t.Fatalf("propose_task refused: %s", res.Text)
	}
	for _, want := range []string{
		"Recorded (1 proposed task(s)",
		"`repo-read:acme/gadgets` is a CI capability", `reads: ["acme/gadgets"]`,
		"`repo-write:acme/widgets` will be left off this proposal", "is a CI capability, not a task grant",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result does not say %q:\n%s", want, res.Text)
		}
	}
	// What is recorded is unchanged: filing at run end decides it.
	if got := sink.ProposedTasks(); len(got) != 1 || len(got[0].Capabilities) != 2 {
		t.Fatalf("recorded = %+v, want the one proposal with both capabilities", got)
	}

	plain := tool.Handler(context.Background(), map[string]any{
		"title": "Plain", "body": "b", "capabilities": []any{"grain-ci"},
	})
	if plain.IsError || strings.Contains(plain.Text, "CI capability") || strings.Contains(plain.Text, "left off") {
		t.Errorf("a proposal with no CI-only capability was told about one:\n%s", plain.Text)
	}
}
