package ui_test

// The API surface of what grain remembers: the block every prompt
// carries (PutMemory) and the long notes no prompt carries
// (PutMemoryDocument and the two reads beside it). What is checked here
// is what a write is allowed to cost every later run -- because that is
// the whole difference between the two, and the daemon is where a run
// that wrote too much has to be told about it.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestMemoryIsWrittenPerScopeAndForgotten(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.PutMemory(ctx, "", "the e2e suite is flaky on retry", model.Principal{}); err != nil {
		t.Fatalf("writing the deployment's memory: %v", err)
	}
	if _, err := c.PutMemory(ctx, "acme/widgets", "run `make frontend` first", model.Principal{}); err != nil {
		t.Fatalf("writing a repo's memory: %v", err)
	}
	memories, err := c.ListMemories(ctx)
	if err != nil {
		t.Fatalf("listing memories: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("listed %d memories, want the two that were written: %+v", len(memories), memories)
	}

	if _, err := c.PutMemory(ctx, "", strings.Repeat("x", model.MaxMemoryLength+1), model.Principal{}); err == nil {
		t.Error("the daemon accepted a memory too long to carry on every dispatch")
	}
}

func TestMemoryDocumentsAreWrittenReadAndDeleted(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	text := strings.TrimSpace(strings.Repeat("why the third retry fails. ", 400))

	written, err := c.PutMemoryDocument(ctx, "acme/widgets", "flaky-e2e",
		"why the e2e suite fails on its third retry", text, model.Principal{})
	if err != nil {
		t.Fatalf("writing a document: %v", err)
	}
	if written.Repo != "acme/widgets" || written.Length != len(text) {
		t.Errorf("wrote a %d-character document for %q, want %d for acme/widgets",
			written.Length, written.Repo, len(text))
	}

	// A listing is an index: every scope's documents, and none of their
	// text -- the property the whole arrangement rests on, since the
	// alternative is the prompt this exists to keep short.
	docs, err := c.ListMemoryDocuments(ctx)
	if err != nil {
		t.Fatalf("listing documents: %v", err)
	}
	if len(docs) != 1 || docs[0].Name != "flaky-e2e" {
		t.Fatalf("listed %+v, want the one document that was written", docs)
	}
	if docs[0].Text != "" {
		t.Error("the listing carries the document's text, which is what a listing exists not to do")
	}
	if docs[0].Length != len(text) {
		t.Errorf("listed length %d, want %d", docs[0].Length, len(text))
	}

	read, err := c.GetMemoryDocument(ctx, "acme/widgets", "flaky-e2e")
	if err != nil {
		t.Fatalf("reading a document: %v", err)
	}
	if read.Text != text {
		t.Errorf("read back %d characters, want the %d that were written", len(read.Text), len(text))
	}
	if read.UpdatedAt == nil {
		t.Error("the document came back with no timestamp, which is all a reader has to judge it by")
	}

	// A name that is not there is a 404, not an empty document: "there
	// is a note and it says nothing" and "you have the wrong name" are
	// different answers to the run that asked.
	var notFound *ui.NotFoundError
	if _, err := c.GetMemoryDocument(ctx, "acme/widgets", "no-such-note"); !errors.As(err, &notFound) {
		t.Errorf("reading a document that does not exist = %v, want a not-found", err)
	}
	// And so is the same name in the wrong scope.
	if _, err := c.GetMemoryDocument(ctx, "", "flaky-e2e"); !errors.As(err, &notFound) {
		t.Errorf("reading a repo's document as the deployment's = %v, want a not-found", err)
	}

	if _, err := c.PutMemoryDocument(ctx, "acme/widgets", "flaky-e2e", "", "", model.Principal{}); err != nil {
		t.Fatalf("deleting a document: %v", err)
	}
	if docs, err := c.ListMemoryDocuments(ctx); err != nil || len(docs) != 0 {
		t.Fatalf("after deleting, listed %+v (%v), want nothing left behind", docs, err)
	}
}

// Every limit a document write has is about what it costs later runs,
// and all of them are refused here rather than at the run that reads it.
func TestMemoryDocumentWritesAreBounded(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	var invalid *ui.ValidationError
	if _, err := c.PutMemoryDocument(ctx, "", "Flaky E2E", "the third retry", "a note", model.Principal{}); !errors.As(err, &invalid) {
		t.Errorf("a name nothing could type back = %v, want it refused", err)
	}
	if _, err := c.PutMemoryDocument(ctx, "", "flaky-e2e", "", "a note", model.Principal{}); !errors.As(err, &invalid) {
		t.Errorf("a document with no summary = %v, want it refused", err)
	}
	if _, err := c.PutMemoryDocument(ctx, "", "flaky-e2e",
		strings.Repeat("x", model.MaxMemoryDocumentSummaryLength+1), "a note", model.Principal{}); !errors.As(err, &invalid) {
		t.Errorf("a summary longer than the line every prompt carries = %v, want it refused", err)
	}
	if _, err := c.PutMemoryDocument(ctx, "", "flaky-e2e", "the third retry",
		strings.Repeat("x", model.MaxMemoryDocumentLength+1), model.Principal{}); !errors.As(err, &invalid) {
		t.Errorf("a document past its own limit = %v, want it refused", err)
	}
}

// The count is the bound on the index itself: without it an agent files
// a document per observation and hands every later run a page of titles,
// which is the diary MaxMemoryLength exists to prevent, rebuilt out of
// summaries. Replacing one that is already there is what a writer at the
// limit is being asked to do, so that must still work.
func TestAScopeKeepsOnlySoManyMemoryDocuments(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	for i := 0; i < model.MaxMemoryDocumentsPerScope; i++ {
		name := "note-" + string(rune('a'+i))
		if _, err := c.PutMemoryDocument(ctx, "", name, "one of many", "a note", model.Principal{}); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	var invalid *ui.ValidationError
	if _, err := c.PutMemoryDocument(ctx, "", "one-too-many", "one of many", "a note", model.Principal{}); !errors.As(err, &invalid) {
		t.Errorf("writing past the per-scope limit = %v, want it refused", err)
	}
	if _, err := c.PutMemoryDocument(ctx, "", "note-a", "one of many", "a better note", model.Principal{}); err != nil {
		t.Errorf("replacing a document at the limit = %v, want the way out of it left open", err)
	}
	// A repo's own scope is counted on its own, since the index a
	// dispatch carries is two layers and each pays for itself.
	if _, err := c.PutMemoryDocument(ctx, "acme/widgets", "flaky-e2e", "the third retry", "a note", model.Principal{}); err != nil {
		t.Errorf("writing another scope's first document = %v, want scopes counted apart", err)
	}
}

// Who a write is recorded as, which is the whole of grain/task-394: the
// memory agent and a person editing the Settings pane send the same body
// to the same handler, so a write that says nothing about its author is
// this deployment's own operator -- the same defaulting CreateTask and
// AddComment do -- and one that names a run is recorded as that run.
func TestMemoryWritesRecordTheirAuthor(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"}

	written, err := c.PutMemory(ctx, "", "somebody typed this", model.Principal{})
	if err != nil {
		t.Fatalf("writing an unattributed memory: %v", err)
	}
	if written.UpdatedByKind != string(model.PrincipalHuman) || written.UpdatedBy != c.Config.Actor.ID {
		t.Errorf("an unattributed write reports %q/%q, want this deployment's own actor",
			written.UpdatedByKind, written.UpdatedBy)
	}
	stored, err := store.GetMemory(ctx, nil)
	if err != nil || stored == nil || stored.UpdatedBy != c.Config.Actor {
		t.Fatalf("the stored memory was written by %+v (%v), want %+v", stored, err, c.Config.Actor)
	}

	if _, err := c.PutMemory(ctx, "", "an agent noticed this", agent); err != nil {
		t.Fatalf("writing an attributed memory: %v", err)
	}
	if stored, err = store.GetMemory(ctx, nil); err != nil || stored == nil || stored.UpdatedBy != agent {
		t.Fatalf("the stored memory was written by %+v (%v), want %+v", stored, err, agent)
	}

	doc, err := c.PutMemoryDocument(ctx, "", "flaky-e2e", "why the third retry fails", "a note", agent)
	if err != nil {
		t.Fatalf("writing a document: %v", err)
	}
	if doc.UpdatedByKind != string(model.PrincipalAgent) || doc.UpdatedBy != "a1b2" {
		t.Errorf("the document reports %q/%q, want the run that wrote it", doc.UpdatedByKind, doc.UpdatedBy)
	}
	if _, err := c.PutMemoryDocument(ctx, "", "release-checklist", "what a release needs", "a note",
		model.Principal{}); err != nil {
		t.Fatalf("writing an unattributed document: %v", err)
	}
	listed, err := c.ListMemoryDocuments(ctx)
	if err != nil {
		t.Fatalf("listing documents: %v", err)
	}
	for _, d := range listed {
		if d.UpdatedByKind == "" {
			t.Errorf("the listing says nothing about who wrote %q, which is the one thing the index adds", d.Name)
		}
	}
}

// The same thing over the wire, since the two callers that differ are on
// opposite sides of it: `grain mcpserver` names the run whose
// update_memory call this is (cmd/grain's daemonMemory), and the
// Settings pane sends no author at all.
//
// A caller with no verified principal -- the trusted hop `grain
// mcpserver` makes -- is believed about who it is, but a run it names
// must hold the memory capability, and the claim must be well-formed: a
// kind grain does not know, or half a principal, is a refusal rather
// than an unreadable word in a column every reader switches on.
func TestPutMemoryTakesItsAuthorFromTheRequest(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "remember things",
		Grants: []model.Grant{{Capability: "memory"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "c3d4", Intent: model.IntentImplement, Title: "no memory here",
	}); err != nil {
		t.Fatal(err)
	}
	for _, author := range []string{"c3d4", "nonexistent"} {
		rec := do(t, srv, http.MethodPut, "/api/memory",
			`{"text":"injected","author":"`+author+`","authorKind":"agent"}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a write claiming run %s, which holds no memory capability: status = %d, want 403: %s",
				author, rec.Code, rec.Body)
		}
	}

	rec := do(t, srv, http.MethodPut, "/api/memory",
		`{"text":"an agent noticed this","author":"a1b2","authorKind":"agent"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.Memory](t, rec); got.UpdatedBy != "a1b2" || got.UpdatedByKind != "agent" {
		t.Errorf("the write reports %q/%q, want the run that claimed it", got.UpdatedByKind, got.UpdatedBy)
	}

	rec = do(t, srv, http.MethodPut, "/api/memory", `{"text":"somebody typed this"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.Memory](t, rec)
	if got.UpdatedBy != client.Config.Actor.ID || got.UpdatedByKind != string(client.Config.Actor.Kind) {
		t.Errorf("a request naming nobody reports %q/%q, want this deployment's own actor",
			got.UpdatedByKind, got.UpdatedBy)
	}

	for _, body := range []string{
		`{"text":"a note","author":"a1b2","authorKind":"wizard"}`,
		`{"text":"a note","author":"a1b2"}`,
		`{"text":"a note","authorKind":"human"}`,
	} {
		if rec := do(t, srv, http.MethodPut, "/api/memory", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
	// The document route reads the same two fields, through the same
	// helper.
	rec = do(t, srv, http.MethodPut, "/api/memory/documents",
		`{"name":"flaky-e2e","summary":"why the third retry fails","text":"a note",`+
			`"author":"bwsalmon","authorKind":"human"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if doc := decode[ui.MemoryDocument](t, rec); doc.UpdatedByKind != "human" {
		t.Errorf("the document reports %q, want the person who claimed it", doc.UpdatedByKind)
	}
}
