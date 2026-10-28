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
	"fmt"
	"strings"
	"testing"
)

type fakeMemoryStore struct {
	repo, text string
	calls      int

	document, summary, documentText string
	documentCalls                   int

	docs []MemoryDocumentEntry
	read MemoryDocumentText
	// readRepo and readName are what the last read asked for, since a
	// document is only findable if the scope travels with the name.
	readRepo, readName string
	readErr            error
}

func (f *fakeMemoryStore) WriteMemory(_ context.Context, repo, text string) error {
	f.calls++
	f.repo, f.text = repo, text
	return nil
}

func (f *fakeMemoryStore) WriteMemoryDocument(_ context.Context, repo, name, summary, text string) error {
	f.documentCalls++
	f.repo, f.document, f.summary, f.documentText = repo, name, summary, text
	return nil
}

func (f *fakeMemoryStore) ListMemoryDocuments(context.Context) ([]MemoryDocumentEntry, error) {
	return f.docs, nil
}

func (f *fakeMemoryStore) ReadMemoryDocument(_ context.Context, repo, name string) (MemoryDocumentText, error) {
	f.readRepo, f.readName = repo, name
	return f.read, f.readErr
}

// memoryTools is the roster a run holding the capability gets, indexed by
// name -- the three of them, since each agent framework's allowedTools
// enumerates exactly what this returns.
func memoryTools(t *testing.T, store MemoryStore) map[string]Tool {
	t.Helper()
	tools := NewMemoryTools(store)
	byName := map[string]Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	for _, want := range []string{"update_memory", "list_memory_documents", "read_memory_document"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("NewMemoryTools returned %v, want %s among them", tools, want)
		}
	}
	if len(tools) != len(byName) {
		t.Fatalf("NewMemoryTools returned %d tools under %d names", len(tools), len(byName))
	}
	return byName
}

func memoryTool(t *testing.T, store MemoryStore) Tool {
	t.Helper()
	return memoryTools(t, store)["update_memory"]
}

func TestUpdateMemoryWritesTheScopeItWasGiven(t *testing.T) {
	store := &fakeMemoryStore{}
	tool := memoryTool(t, store)

	res := tool.Handler(context.Background(), map[string]any{"text": " the e2e suite is flaky on retry "})
	if res.IsError {
		t.Fatalf("update_memory refused a deployment-wide write: %s", res.Text)
	}
	if store.repo != "" || store.text != "the e2e suite is flaky on retry" {
		t.Errorf("wrote (%q, %q), want the deployment scope and the trimmed text", store.repo, store.text)
	}

	res = tool.Handler(context.Background(), map[string]any{"repo": "bwsalmon/grain", "text": "needs make frontend"})
	if res.IsError {
		t.Fatalf("update_memory refused a per-repo write: %s", res.Text)
	}
	if store.repo != "bwsalmon/grain" {
		t.Errorf("wrote scope %q, want the repo the call named", store.repo)
	}
	// The answer names the scope, so an agent that meant the deployment
	// and typo'd a repo sees it now rather than in next month's prompts.
	if !strings.Contains(res.Text, "bwsalmon/grain") {
		t.Errorf("update_memory answered %q without naming the scope it wrote", res.Text)
	}
}

func TestUpdateMemoryForgetsOnAnEmptyText(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{"text": ""})
	if res.IsError {
		t.Fatalf("update_memory refused to forget a scope: %s", res.Text)
	}
	if store.calls != 1 || store.text != "" {
		t.Errorf("wrote %d times with %q, want one write of nothing at all", store.calls, store.text)
	}
	if !strings.Contains(res.Text, "empty") {
		t.Errorf("update_memory answered %q, want it to say the scope is now empty", res.Text)
	}
}

// The limit is the agent's to respect while it can still act on it: a
// memory is repeated into every prompt this deployment ever issues, so
// one that is refused after a round trip is one turn wasted.
func TestUpdateMemoryRefusesOneTooLongToCarry(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(),
		map[string]any{"text": strings.Repeat("x", MaxMemoryLength+1)})
	if !res.IsError {
		t.Fatal("update_memory accepted a memory past the limit every prompt would carry")
	}
	if store.calls != 0 {
		t.Errorf("the daemon was asked %d times, want a refusal to cost no round trip", store.calls)
	}
	// And it says where the rest goes, since that is the whole of what a
	// document is for.
	if !strings.Contains(res.Text, "document") {
		t.Errorf("update_memory answered %q without pointing at the long form", res.Text)
	}
}

// A named document is the same call with a different budget: text an
// order of magnitude longer than any prompt would carry, filed under a
// name a later run can ask for.
func TestUpdateMemoryWritesANamedDocument(t *testing.T) {
	store := &fakeMemoryStore{}
	long := strings.Repeat("why the third retry fails. ", 500)
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{
		"repo": "bwsalmon/granule", "document": "flaky-e2e",
		"summary": "why the e2e suite fails on its third retry", "text": long,
	})
	if res.IsError {
		t.Fatalf("update_memory refused a document: %s", res.Text)
	}
	if store.calls != 0 {
		t.Errorf("the injected memory was written %d times by a call that named a document", store.calls)
	}
	if store.documentCalls != 1 || store.document != "flaky-e2e" || store.documentText != strings.TrimSpace(long) {
		t.Errorf("wrote document %q (%d calls) with %d characters, want the one the call named",
			store.document, store.documentCalls, len(store.documentText))
	}
	if store.summary == "" {
		t.Error("the document was written with no summary, which is the only part any prompt carries")
	}
	if !strings.Contains(res.Text, "flaky-e2e") || !strings.Contains(res.Text, "read_memory_document") {
		t.Errorf("update_memory answered %q, want it to name the document and how it is read", res.Text)
	}
}

// The summary is the part every later prompt pays for, so a document
// with none is refused: an index line nobody can tell the subject of is
// an index line nobody acts on.
func TestUpdateMemoryRefusesADocumentWithNoSummary(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{
		"document": "flaky-e2e", "text": "a long note",
	})
	if !res.IsError {
		t.Fatal("update_memory accepted a document with nothing to index it by")
	}
	if store.documentCalls != 0 {
		t.Errorf("the daemon was asked %d times, want a refusal to cost no round trip", store.documentCalls)
	}
}

func TestUpdateMemoryRefusesADocumentPastItsOwnLimit(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{
		"document": "flaky-e2e", "summary": "why it fails",
		"text": strings.Repeat("x", MaxMemoryDocumentLength+1),
	})
	if !res.IsError {
		t.Fatal("update_memory accepted a document past the limit a run could read in a turn")
	}
	// A document is bounded far above a memory, which is the whole point
	// of having one: text this size is refused as a memory and accepted
	// as a document.
	res = memoryTool(t, store).Handler(context.Background(), map[string]any{
		"document": "flaky-e2e", "summary": "why it fails",
		"text": strings.Repeat("x", MaxMemoryLength+1),
	})
	if res.IsError {
		t.Fatalf("update_memory refused a document the size of a memory and a bit: %s", res.Text)
	}
}

func TestUpdateMemoryRefusesASummaryTooLongForEveryPrompt(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{
		"document": "flaky-e2e", "summary": strings.Repeat("x", MaxMemoryDocumentSummaryLength+1),
		"text": "a long note",
	})
	if !res.IsError {
		t.Fatal("update_memory accepted a summary longer than the line every prompt carries")
	}
	if store.documentCalls != 0 {
		t.Errorf("the daemon was asked %d times, want a refusal to cost no round trip", store.documentCalls)
	}
}

func TestUpdateMemoryDeletesADocumentOnAnEmptyText(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTool(t, store).Handler(context.Background(), map[string]any{
		"document": "flaky-e2e", "text": "",
	})
	if res.IsError {
		t.Fatalf("update_memory refused to delete a document: %s", res.Text)
	}
	if store.documentCalls != 1 || store.documentText != "" {
		t.Errorf("wrote %d times with %q, want one write of nothing at all",
			store.documentCalls, store.documentText)
	}
	if !strings.Contains(res.Text, "deleted") {
		t.Errorf("update_memory answered %q, want it to say the document is gone", res.Text)
	}
}

// A listing is every scope's, not this run's: the memory agent's whole
// job is looking at what a deployment has written down across its repos.
func TestListMemoryDocumentsNamesEveryScope(t *testing.T) {
	store := &fakeMemoryStore{docs: []MemoryDocumentEntry{
		{Name: "state-repo-shape", Summary: "how the dump is laid out", Age: "written 3 days ago", Length: 4000},
		{Repo: "bwsalmon/granule", Name: "flaky-e2e", Summary: "the third retry", Age: "written 2 months ago"},
	}}
	res := memoryTools(t, store)["list_memory_documents"].Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("list_memory_documents failed: %s", res.Text)
	}
	for _, want := range []string{"state-repo-shape", "flaky-e2e", "bwsalmon/granule",
		"written 2 months ago", "the third retry"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the listing never mentions %q:\n%s", want, res.Text)
		}
	}
}

// An empty listing is a sentence, not an empty answer: "there are none"
// and "the call went wrong" must not look the same to the run reading it.
func TestListMemoryDocumentsSaysSoWhenThereAreNone(t *testing.T) {
	res := memoryTools(t, &fakeMemoryStore{})["list_memory_documents"].Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("list_memory_documents failed on an empty deployment: %s", res.Text)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Error("list_memory_documents answered with nothing at all where there is nothing to list")
	}
}

func TestReadMemoryDocumentAsksForTheScopeItWasGiven(t *testing.T) {
	store := &fakeMemoryStore{read: MemoryDocumentText{
		Repo: "bwsalmon/granule", Name: "flaky-e2e", Summary: "the third retry",
		Age: "written 2 months ago", Text: "The suite reuses the VM.",
	}}
	res := memoryTools(t, store)["read_memory_document"].Handler(context.Background(),
		map[string]any{"repo": "bwsalmon/granule", "name": " flaky-e2e "})
	if res.IsError {
		t.Fatalf("read_memory_document failed: %s", res.Text)
	}
	if store.readRepo != "bwsalmon/granule" || store.readName != "flaky-e2e" {
		t.Errorf("read (%q, %q), want the scope and the trimmed name the call gave", store.readRepo, store.readName)
	}
	if !strings.Contains(res.Text, "The suite reuses the VM.") {
		t.Errorf("read_memory_document answered %q without the document in it", res.Text)
	}
	// How old it is, again: it is the fact that decides how much of the
	// document to believe, and the index the caller read is a turn behind.
	if !strings.Contains(res.Text, "written 2 months ago") {
		t.Errorf("read_memory_document answered %q without saying how old the note is", res.Text)
	}
}

// A note may be written up to MaxMemoryDocumentLength, which is four
// times what one answer carries, so the longest note there can be is
// still an answer a run can read -- with its heading, both ends of the
// note itself, and the one thing to do about the middle.
func TestReadMemoryDocumentBoundsALongNote(t *testing.T) {
	store := &fakeMemoryStore{read: MemoryDocumentText{
		Name: "flaky-e2e", Summary: "the third retry",
		Text: "It starts here.\n" + strings.Repeat("a line of the note\n", 4000) + "It ends here.\n",
	}}
	res := memoryTools(t, store)["read_memory_document"].Handler(context.Background(),
		map[string]any{"name": "flaky-e2e"})
	if res.IsError {
		t.Fatalf("read_memory_document failed: %s", res.Text)
	}
	if len(res.Text) > maxResultTextBytes() {
		t.Errorf("a %d-byte note answered %d bytes", len(store.read.Text), len(res.Text))
	}
	if !strings.Contains(res.Text, "It starts here.") || !strings.Contains(res.Text, "It ends here.") {
		t.Errorf("the note lost one of its ends:\n%s", firstLines(res.Text, 4))
	}
	if !strings.Contains(res.Text, "update_memory") {
		t.Errorf("the cut does not say what can be done about a note this long:\n%s", res.Text)
	}
}

func TestReadMemoryDocumentNeedsAName(t *testing.T) {
	store := &fakeMemoryStore{}
	res := memoryTools(t, store)["read_memory_document"].Handler(context.Background(),
		map[string]any{"name": "  "})
	if !res.IsError {
		t.Fatal("read_memory_document accepted a call naming no document")
	}
	if store.readName != "" {
		t.Errorf("the daemon was asked for %q, want a refusal to cost no round trip", store.readName)
	}
}

func TestReadMemoryDocumentPassesOnWhatTheDaemonSaid(t *testing.T) {
	store := &fakeMemoryStore{readErr: fmt.Errorf("no memory document flaky-e2e for this deployment")}
	res := memoryTools(t, store)["read_memory_document"].Handler(context.Background(),
		map[string]any{"name": "flaky-e2e"})
	if !res.IsError {
		t.Fatal("read_memory_document reported success for a document that does not exist")
	}
	if !strings.Contains(res.Text, "flaky-e2e") {
		t.Errorf("read_memory_document answered %q, want the daemon's own sentence", res.Text)
	}
}

// A run with no route back to the daemon gets the tools anyway (the
// frameworks' allowedTools enumerate them unconditionally), so the one
// thing they must not do is fail silently.
func TestMemoryToolsWithoutADaemonSaySo(t *testing.T) {
	for name, tool := range memoryTools(t, nil) {
		res := tool.Handler(context.Background(), map[string]any{"text": "something", "name": "flaky-e2e"})
		if !res.IsError {
			t.Errorf("%s reported success with no daemon to ask", name)
		}
	}
}

// The listing and the read both say which notes a person wrote, because
// the run reading them is usually the memory agent deciding what to
// replace -- and "replace what has stopped being true" must not quietly
// mean "replace what an operator just fixed".
//
// A note whose author was never recorded is not marked either way: the
// mark is a claim that somebody typed it, and unknown is the absence of
// that claim rather than a claim about an agent.
func TestMemoryDocumentsSayWhichAPersonWrote(t *testing.T) {
	store := &fakeMemoryStore{docs: []MemoryDocumentEntry{
		{Name: "release-checklist", Summary: "what a release needs", Age: "written 3 days ago", ByHand: true},
		{Name: "flaky-e2e", Summary: "the third retry", Age: "written 3 days ago"},
	}}
	res := memoryTools(t, store)["list_memory_documents"].Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("list_memory_documents failed: %s", res.Text)
	}
	for _, line := range strings.Split(res.Text, "\n") {
		switch {
		case strings.Contains(line, "release-checklist") && !strings.Contains(line, "written by hand"):
			t.Errorf("the line for a note a person wrote says nothing about it:\n%s", line)
		case strings.Contains(line, "flaky-e2e") && strings.Contains(line, "written by hand"):
			t.Errorf("a note nobody typed is marked as one somebody did:\n%s", line)
		}
	}

	read := &fakeMemoryStore{read: MemoryDocumentText{
		Name: "release-checklist", Summary: "what a release needs", Text: "Tag from main only.", ByHand: true,
	}}
	got := memoryTools(t, read)["read_memory_document"].Handler(context.Background(),
		map[string]any{"name": "release-checklist"})
	if got.IsError || !strings.Contains(got.Text, "by hand") {
		t.Errorf("read_memory_document answered %q without saying a person wrote it", got.Text)
	}
}
