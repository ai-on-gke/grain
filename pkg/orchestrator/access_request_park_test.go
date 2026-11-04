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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestProcessResultParksOnAnAccessRequest is request_access's half of
// the parking contract: the task parks as it does on a question, the
// conversation carries the run's reason and every item, and the request
// is recorded beside the park -- with its summary on the comment the
// inbox shows -- so the UI offers Grant and Deny rather than a reply box.
func TestProcessResultParksOnAnAccessRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	result := toolResult(agent.ToolCall{
		Name: "request_access",
		Arguments: map[string]any{
			"summary": "Grant write to acme/lib so the fix lands in both",
			"reason":  "the bug is in the shared library",
			"requests": []any{
				map[string]any{"kind": "write", "repo": "acme/lib", "base": "dev"},
				map[string]any{"kind": "grant", "capability": "gcp#gcp-key", "with": map[string]any{"permission_set": "reader"}},
			},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want awaiting_reply", st, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("conversation = %+v, want the relayed request", comments)
	}
	for _, want := range []string{"the bug is in the shared library", "write acme/lib (branch dev)", "gcp#gcp-key", "Grant or deny"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("relayed request does not say %q:\n%s", want, comments[0].Body)
		}
	}
	if comments[0].Summary != "Grant write to acme/lib so the fix lands in both" {
		t.Errorf("summary = %q, want the run's own", comments[0].Summary)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[0].ID {
		t.Fatalf("pending question = %v, want the request's comment", obs.PendingQuestionCommentID)
	}
	if obs.PendingAccess == nil || len(obs.PendingAccess.Items) != 2 || obs.PendingAccess.Items[1].With["permission_set"] != "reader" {
		t.Fatalf("pending access = %+v, want both items with their arguments", obs.PendingAccess)
	}
	if obs.PendingSecret != "" {
		t.Errorf("pending secret = %q, want none: this is not a secret request", obs.PendingSecret)
	}
}
