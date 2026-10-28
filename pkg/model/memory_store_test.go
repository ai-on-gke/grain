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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What the store owes a memory: the two scopes stay apart, a rewrite
// replaces rather than accumulates (the whole reason update_memory sends
// whole text), and an emptied memory leaves no row behind -- forgetting
// is something the memory agent has to be able to do, and a blank row
// would be exported to the state repository and listed in the UI
// forever.

func TestMemoryRoundTripsPerScope(t *testing.T) {
	store, ctx := open(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	if err := store.PutMemory(ctx, model.Memory{Text: "the e2e suite is flaky on retry"}); err != nil {
		t.Fatalf("PutMemory(deployment): %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{Repo: &repo, Text: "run `make frontend` before `go vet`"}); err != nil {
		t.Fatalf("PutMemory(repo): %v", err)
	}

	deployment, err := store.GetMemory(ctx, nil)
	if err != nil || deployment == nil {
		t.Fatalf("GetMemory(\"\", nil) = %v, %v, want the deployment's memory", deployment, err)
	}
	if deployment.Text != "the e2e suite is flaky on retry" {
		t.Errorf("deployment memory = %q, want what was written", deployment.Text)
	}
	if deployment.UpdatedAt.IsZero() {
		t.Error("deployment memory has no UpdatedAt, so nothing can tell a fresh note from a stale one")
	}
	perRepo, err := store.GetMemory(ctx, &repo)
	if err != nil || perRepo == nil {
		t.Fatalf("GetMemory(core, %s) = %v, %v, want that repo's memory", repo, perRepo, err)
	}
	if perRepo.Text != "run `make frontend` before `go vet`" {
		t.Errorf("%s memory = %q, want what was written", repo, perRepo.Text)
	}

	// A repo nobody has written anything about is nil rather than an
	// error: every dispatch reads this, and most read nothing.
	other := model.RepoRef{Owner: "bwsalmon", Name: "kontur"}
	if got, err := store.GetMemory(ctx, &other); err != nil || got != nil {
		t.Fatalf("GetMemory(%s) = %v, %v, want nil for a scope nothing has been written for", other, got, err)
	}

	memories, err := store.ListMemories(ctx)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("ListMemories returned %d memories, want the two that were written", len(memories))
	}
	// Deployment-wide first: its key is empty strings, which is what
	// keeps a listing readable without a sort of its own.
	if memories[0].Repo != nil {
		t.Errorf("ListMemories()[0] is repo=%v, want the deployment's own first", memories[0].Repo)
	}
}

func TestMemoryIsReplacedAndForgotten(t *testing.T) {
	store, ctx := open(t)

	if err := store.PutMemory(ctx, model.Memory{Text: "first"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{Text: "second"}); err != nil {
		t.Fatalf("PutMemory again: %v", err)
	}
	got, err := store.GetMemory(ctx, nil)
	if err != nil || got == nil || got.Text != "second" {
		t.Fatalf("GetMemory = %v, %v, want only the second write to stand", got, err)
	}

	if err := store.PutMemory(ctx, model.Memory{Text: "  \n "}); err != nil {
		t.Fatalf("PutMemory(empty): %v", err)
	}
	if got, err := store.GetMemory(ctx, nil); err != nil || got != nil {
		t.Fatalf("GetMemory after forgetting = %v, %v, want no row left at all", got, err)
	}
	memories, err := store.ListMemories(ctx)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 0 {
		t.Errorf("ListMemories returned %d memories, want a forgotten scope to leave nothing behind", len(memories))
	}
}

// Who wrote a memory is stored beside it, for both kinds of row, because
// a person editing the Settings pane and the memory agent mid-run make
// the same write: once it has landed, the row is the only thing that can
// say which happened -- and memory is replaced wholesale, so an
// operator's correction being overwritten by tonight's agent left no
// trace of anyone having said otherwise.
func TestMemoryRecordsWhoWroteIt(t *testing.T) {
	store, ctx := open(t)
	person := model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"}
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	if err := store.PutMemory(ctx, model.Memory{Text: "somebody fixed this", UpdatedBy: person}); err != nil {
		t.Fatalf("PutMemory(by a person): %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{
		Repo: &repo, Text: "grain worked this out", UpdatedBy: agent,
	}); err != nil {
		t.Fatalf("PutMemory(by an agent): %v", err)
	}
	if got, err := store.GetMemory(ctx, nil); err != nil || got == nil || got.UpdatedBy != person {
		t.Fatalf("GetMemory(deployment) = (%+v, %v), want it written by %+v", got, err, person)
	}
	memories, err := store.ListMemories(ctx)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 2 || memories[0].UpdatedBy != person || memories[1].UpdatedBy != agent {
		t.Fatalf("ListMemories = %+v, want each row's own author with it", memories)
	}

	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
		Name: "release-checklist", Summary: "what a release needs", Text: "tag from main only",
		UpdatedBy: person,
	}); err != nil {
		t.Fatalf("PutMemoryDocument: %v", err)
	}
	if got, err := store.GetMemoryDocument(ctx, nil, "release-checklist"); err != nil ||
		got == nil || got.UpdatedBy != person {
		t.Fatalf("GetMemoryDocument = (%+v, %v), want it written by %+v", got, err, person)
	}
	// A listing carries it too: the index a run is given says who wrote
	// each document, so it must survive the read that does not carry the
	// text (MemoryIndex).
	docs, err := store.ListMemoryDocuments(ctx)
	if err != nil || len(docs) != 1 {
		t.Fatalf("ListMemoryDocuments = (%+v, %v), want the one document", docs, err)
	}
	if docs[0].UpdatedBy != person {
		t.Errorf("the listed document was written by %+v, want %+v", docs[0].UpdatedBy, person)
	}

	// A write that names nobody is stored as nobody, rather than the
	// store guessing: who a write should be attributed to is the API's
	// decision (ui.Client.PutMemory), and a store that filled one in
	// would make an unknown author indistinguishable from a claimed one.
	if err := store.PutMemory(ctx, model.Memory{Text: "written by nobody in particular"}); err != nil {
		t.Fatalf("PutMemory(unattributed): %v", err)
	}
	got, err := store.GetMemory(ctx, nil)
	if err != nil || got == nil {
		t.Fatalf("GetMemory after an unattributed write = (%+v, %v)", got, err)
	}
	if (got.UpdatedBy != model.Principal{}) {
		t.Errorf("an unattributed write was stored as %+v, want nobody", got.UpdatedBy)
	}
}
