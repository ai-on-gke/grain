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

package ui_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A brief edit or compaction from a caller the request names no one for
// -- the access code, the local API token -- is recorded as the
// deployment's own person, as a comment from the same caller is, rather
// than as a bare "human" (docs/test-runs/2026-11-11-durable-tasks.md,
// finding 2). A caller that is named keeps its own name.
func TestBriefAndCompactionNameTheOperatorForAnAnonymousCaller(t *testing.T) {
	client, _, ctx := testClient(t)
	admission := string(model.AdmissionDurable)
	tk, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "durable", Description: "the original", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateTask(ctx, tk.ID, ui.UpdateTaskRequest{Admission: &admission}); err != nil {
		t.Fatal(err)
	}
	anonymous := model.Principal{Kind: model.PrincipalHuman}
	view, err := client.SetTaskBrief(ctx, tk.ID, anonymous, ui.BriefRequest{Brief: "the brief", Reason: "narrowed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.BriefEdits) != 1 || !strings.Contains(view.BriefEdits[0].By, "alice") {
		t.Errorf("brief edits = %+v, want one by alice", view.BriefEdits)
	}
	view, err = client.CompactTask(ctx, tk.ID, anonymous, ui.CompactRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.CompactRequestedBy, "alice") {
		t.Errorf("compaction requested by %q, want alice", view.CompactRequestedBy)
	}
	bob := model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"}
	view, err = client.SetTaskBrief(ctx, tk.ID, bob, ui.BriefRequest{Brief: "bob's brief", Reason: "bob says"})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.BriefEdits[len(view.BriefEdits)-1].By; !strings.Contains(got, "bob@example.com") {
		t.Errorf("bob's brief edit is recorded as %q", got)
	}
	if _, err := client.SetTaskBrief(ctx, tk.ID, bob, ui.BriefRequest{Brief: "x", Reason: " "}); err == nil {
		t.Error("a brief edit with no reason was accepted")
	}
}
