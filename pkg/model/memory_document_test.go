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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What the store owes a document: the scopes stay apart, a listing costs
// nothing but the names, a rewrite replaces rather than accumulates, and
// a deleted document leaves no row behind -- the same four things a
// memory owes, for the same reasons, plus the one that is new here,
// which is that a listing must not drag every document's text through
// the store to render a line that quotes none of it.

func TestMemoryDocumentsRoundTripPerScope(t *testing.T) {
	store, ctx := open(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	text := "The suite reuses the VM between retries, so the third one starts dirty."

	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
		Name: "state-repo-shape", Summary: "how the dump is laid out", Text: "one file per table",
	}); err != nil {
		t.Fatalf("PutMemoryDocument(deployment): %v", err)
	}
	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
		Repo: &repo, Name: "flaky-e2e", Summary: "why the third retry fails", Text: text,
	}); err != nil {
		t.Fatalf("PutMemoryDocument(repo): %v", err)
	}

	got, err := store.GetMemoryDocument(ctx, &repo, "flaky-e2e")
	if err != nil || got == nil {
		t.Fatalf("GetMemoryDocument = %v, %v, want the document that was written", got, err)
	}
	if got.Text != text || got.Summary != "why the third retry fails" {
		t.Errorf("read back (%q, %q), want what was written", got.Summary, got.Text)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("the document has no UpdatedAt, so nothing can tell a fresh note from a stale one")
	}

	// The same name in another scope is another document: a repo's note
	// about its own flake must not be findable as the deployment's.
	if got, err := store.GetMemoryDocument(ctx, nil, "flaky-e2e"); err != nil || got != nil {
		t.Fatalf("GetMemoryDocument(deployment) = %v, %v, want nil for a name only a repo has", got, err)
	}

	all, err := store.ListMemoryDocuments(ctx)
	if err != nil {
		t.Fatalf("ListMemoryDocuments: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListMemoryDocuments returned %d, want the two that were written", len(all))
	}
	// Deployment-wide first, as memories are and for the same reason: its
	// key is two empty strings.
	if all[0].Repo != nil || all[0].Name != "state-repo-shape" {
		t.Errorf("ListMemoryDocuments()[0] = %v/%s, want the deployment's own first", all[0].Repo, all[0].Name)
	}
	// A listing is an index, not the notes: it carries how long each one
	// is and none of what it says.
	for _, d := range all {
		if d.Text != "" {
			t.Errorf("the listing carries %s's text, which is what a listing exists not to do", d.Name)
		}
	}
	if all[1].Length != len(text) {
		t.Errorf("listed length %d, want %d -- the size is what a reader decides by", all[1].Length, len(text))
	}

	// Named scopes: what a dispatch's own index is built from, which is
	// the deployment's layer and the task's target and nothing else.
	scoped, err := store.ListMemoryDocuments(ctx, model.MemoryScope{}, model.MemoryScope{Repo: &repo})
	if err != nil {
		t.Fatalf("ListMemoryDocuments(scopes): %v", err)
	}
	if len(scoped) != 2 {
		t.Errorf("ListMemoryDocuments(deployment, %s) returned %d, want both layers", repo, len(scoped))
	}
	other := model.RepoRef{Owner: "bwsalmon", Name: "kontur"}
	if got, err := store.ListMemoryDocuments(ctx, model.MemoryScope{Repo: &other}); err != nil || len(got) != 0 {
		t.Fatalf("ListMemoryDocuments(%s) = %v, %v, want nothing for a scope nobody has written for", other, got, err)
	}
}

func TestMemoryDocumentIsReplacedAndDeleted(t *testing.T) {
	store, ctx := open(t)

	for _, text := range []string{"first", "second"} {
		if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
			Name: "flaky-e2e", Summary: "the third retry", Text: text,
		}); err != nil {
			t.Fatalf("PutMemoryDocument(%q): %v", text, err)
		}
	}
	got, err := store.GetMemoryDocument(ctx, nil, "flaky-e2e")
	if err != nil || got == nil || got.Text != "second" {
		t.Fatalf("GetMemoryDocument = %v, %v, want only the second write to stand", got, err)
	}

	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{Name: "flaky-e2e", Text: " \n "}); err != nil {
		t.Fatalf("PutMemoryDocument(empty): %v", err)
	}
	if got, err := store.GetMemoryDocument(ctx, nil, "flaky-e2e"); err != nil || got != nil {
		t.Fatalf("GetMemoryDocument after deleting = %v, %v, want no row left at all", got, err)
	}
	docs, err := store.ListMemoryDocuments(ctx)
	if err != nil {
		t.Fatalf("ListMemoryDocuments: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("ListMemoryDocuments returned %d, want a deleted document to leave nothing behind", len(docs))
	}
}

// The name is a key, a word in a tool call and a word in every prompt
// the index appears in, and the three have to be the same string.
func TestMemoryDocumentNamesAreWhatCanBeTypedBack(t *testing.T) {
	for _, name := range []string{"flaky-e2e", "e2e", "kontur-vm-boot", "task-270"} {
		if err := model.ValidateMemoryDocumentName(name); err != nil {
			t.Errorf("ValidateMemoryDocumentName(%q) = %v, want it accepted", name, err)
		}
	}
	for _, name := range []string{"", "Flaky-E2E", "flaky e2e", "flaky_e2e", "-flaky", "flaky-",
		"flaky--e2e", "notes/flaky", strings.Repeat("x", model.MaxMemoryDocumentNameLength+1)} {
		if err := model.ValidateMemoryDocumentName(name); err == nil {
			t.Errorf("ValidateMemoryDocumentName(%q) = nil, want a name a reader could not spell refused", name)
		}
	}
}

// A store write goes through the same gate, because a row written under
// a name nothing will spell the same way is a document that exists and
// cannot be read.
func TestPutMemoryDocumentRefusesAnUnreachableName(t *testing.T) {
	store, ctx := open(t)
	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
		Name: "Flaky E2E", Summary: "the third retry", Text: "a note",
	}); err == nil {
		t.Fatal("PutMemoryDocument accepted a name no read will ever match")
	}
}

// The index is what every dispatch pays for: names, scopes, ages and
// summaries, and not a word of any document.
func TestMemoryIndexNamesWithoutQuoting(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	index := model.MemoryIndex([]model.MemoryDocument{
		{Name: "state-repo-shape", Summary: "how the dump is laid out",
			Text: "one file per table", UpdatedAt: now.Add(-24 * time.Hour)},
		{Repo: &model.RepoRef{Owner: "bwsalmon", Name: "kontur"}, Name: "flaky-e2e",
			Summary: "why the third retry fails", Text: "the suite reuses the VM",
			UpdatedAt: now.Add(-90 * 24 * time.Hour)},
	}, now)

	for _, want := range []string{"state-repo-shape", "this deployment", "written yesterday",
		"how the dump is laid out", "flaky-e2e", "bwsalmon/kontur", "written 3 months ago"} {
		if !strings.Contains(index, want) {
			t.Errorf("the index never mentions %q:\n%s", want, index)
		}
	}
	if strings.Contains(index, "one file per table") || strings.Contains(index, "reuses the VM") {
		t.Errorf("the index carries a document's text:\n%s", index)
	}
	if lines := strings.Split(strings.TrimSpace(index), "\n"); len(lines) != 2 {
		t.Errorf("the index is %d lines for two documents, want one each:\n%s", len(lines), index)
	}
	if model.MemoryIndex(nil, now) != "" {
		t.Error("a deployment that has written no documents gets an index anyway")
	}
}

// Age is the whole of what a reader has to judge a note by, so the one
// thing it must not do is invent one.
func TestMemoryAgeSaysNothingItCannotKnow(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if got := model.MemoryAge(time.Time{}, now); got != "" {
		t.Errorf("MemoryAge(zero) = %q, want silence for a row with no timestamp", got)
	}
	if got := model.MemoryAge(now.Add(48*time.Hour), now); got != "" {
		t.Errorf("MemoryAge(future) = %q, want silence rather than an age from a disagreeing clock", got)
	}
	if got := model.MemoryAge(now.Add(-time.Hour), now); got != "written today" {
		t.Errorf("MemoryAge(an hour ago) = %q, want \"written today\"", got)
	}
}

// The index says which documents a person wrote, beside how old they
// are: a run weighing whether to read one, and the memory agent deciding
// whether to replace it, are both entitled to know that somebody typed
// it rather than an earlier run having inferred it.
func TestMemoryIndexMarksWhatAPersonWrote(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	index := model.MemoryIndex([]model.MemoryDocument{
		{Name: "release-checklist", Summary: "what a release needs", UpdatedAt: now.Add(-24 * time.Hour),
			UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
		{Name: "flaky-e2e", Summary: "why the third retry fails", UpdatedAt: now.Add(-24 * time.Hour),
			UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"}},
		// And one from before the author was recorded, which is unknown
		// rather than either answer.
		{Name: "vm-boot", Summary: "how the guest comes up", UpdatedAt: now.Add(-24 * time.Hour)},
	}, now)

	lines := strings.Split(strings.TrimSpace(index), "\n")
	if len(lines) != 3 {
		t.Fatalf("the index is %d lines for three documents:\n%s", len(lines), index)
	}
	if !strings.Contains(lines[0], "written by hand") {
		t.Errorf("the line for a document a person wrote says nothing about it:\n%s", lines[0])
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, "written by hand") {
			t.Errorf("a document nobody typed is marked as one somebody did:\n%s", line)
		}
	}
}
