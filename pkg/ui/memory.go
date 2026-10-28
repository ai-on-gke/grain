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

package ui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/memory"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Memory is one scope's memory as the API reports it: what grain has
// learned about working here, and when it last learned it
// (model.Memory, and model/memory.go for what memory is and why it is
// not the prompt extension beside it).
//
// Repo is empty for the deployment-wide memory every dispatch carries,
// and "owner/name" for a repo's own -- the same spelling every other
// repo-keyed row in this API uses, rather than a nested object, because
// the one thing a caller does with it is match it against a repo it
// already has as a string.
//
// UpdatedBy and UpdatedByKind are who last wrote it (model.Memory.
// UpdatedBy), spelled as the id and the model.PrincipalKind of it the
// way Comment.Author and Comment.AuthorKind already are. Both are absent
// for a row written before the author was recorded, which is "unknown"
// and is neither of the two answers -- see the model field's own doc
// comment, and MemoryPanel.jsx for what a reader is shown for one.
type Memory struct {
	Repo          string     `json:"repo,omitempty"`
	Text          string     `json:"text"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy     string     `json:"updatedBy,omitempty"`
	UpdatedByKind string     `json:"updatedByKind,omitempty"`
}

// memoryRequest is PUT /api/memory's body: which scope to write, the
// whole of what that scope should now remember, and who is saying so.
//
// Text replaces rather than appends, which is the only shape that lets a
// memory be *corrected*: a writer that could only add would leave every
// note it ever got wrong standing in every later prompt. What it costs is
// that a caller has to send back what it wants to keep -- which is why
// the current memory is in the prompt of every run that can write one.
//
// Author/AuthorKind are how a caller that knows better than the default
// says so: cmd/grain's daemonMemory fills them in with the run whose
// update_memory call this is, since a dispatched run knows its own task
// and the daemon on the other end of the hop cannot tell it from the
// browser. A body that names nobody -- which is what the Settings pane
// sends -- is recorded as this deployment's own actor, the same
// defaulting CreateTask and AddComment already do for everything else
// that comes through this API (Client.PutMemory).
//
// They are only read from a caller with no verified principal at all --
// the in-process and loopback hops ServeHTTP already trusts as the
// deployment itself, which is the one cmd/grain's daemonMemory makes. A
// signed-in person, or a request carrying its own agent principal, is
// recorded as that principal whatever the body says (Server.memoryWriter).
//
// An old client's `team` is ignored: memory has no team scope any more
// (single-tenant reset, stage 4d).
type memoryRequest struct {
	Repo       string `json:"repo,omitempty"`
	Text       string `json:"text"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"authorKind,omitempty"`
}

// memoryAuthor is the principal a request names, and the zero Principal
// for one that names nobody -- which Client.PutMemory and
// Client.PutMemoryDocument read as "record this deployment's own actor".
//
// The validating is requestPrincipal's (principal.go), shared with the
// other route an agent attributes a write through -- POST /api/upgrade's
// own requester -- so that "a kind and an id together, and a kind grain
// knows" is one rule rather than two spellings of it.
func memoryAuthor(id, kind string) (model.Principal, error) {
	return requestPrincipal("a memory's author", id, kind)
}

// errMemoryLong is how a memory too large to carry on every dispatch is
// refused -- a ValidationError, so a 400 with a sentence the caller (in
// practice a model, through update_memory) can act on in the same turn.
var errMemoryLong = validationErrorf(
	"a memory must be at most %d characters: it is added to the prompt of every run "+
		"this deployment dispatches, so keep it to the patterns worth repeating and "+
		"leave the detail in the tasks and the state repository",
	model.MaxMemoryLength)

// ListMemories is every scope that has something remembered about it,
// deployment-wide first -- what the Settings pane shows and what a caller
// checking whether last night's memory agent wrote anything reads.
//
// The pane is ui/src/components/MemoryPanel.jsx (grain/task-333), which
// unions this listing with ListMemoryDocuments' into one row per scope:
// a scope can have documents and no memory, so neither listing on its
// own is the set of scopes anything has been written about.
func (c *Client) ListMemories(ctx context.Context) ([]Memory, error) {
	stored, err := c.Store.ListMemories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(stored))
	for _, m := range stored {
		out = append(out, memoryFrom(m))
	}
	return out, nil
}

// PutMemory replaces one scope's memory: repo "" for the deployment-wide
// one, or "owner/name" for a repo's own.
//
// This exists for the memory agent, the way SetTaskActivity next door
// exists for update_status: a dispatched run reaches it through the
// update_memory tool its own MCP server exposes (pkg/mcp, wired in
// pkg/cli/mcpserver.go behind the "memory" capability), which is the
// only route a run has back to the daemon that dispatched it. Nothing
// about it is agent-specific -- a person editing a memory by hand takes
// the same path.
//
// An empty text forgets that scope rather than storing a blank one
// (model.Store.PutMemory deletes the row), so a memory agent that decides
// last month's note is no longer true has a way to say so.
//
// The repo is parsed rather than trusted, through the same parseRepo
// every other repo-shaped field in this API goes through, so a memory
// cannot be filed against "github.com/owner/name" and then never read
// back by the dispatch that looks for "owner/name".
//
// by is who the write is recorded as, and the zero Principal means
// "nobody said": that is written down as c.Config.Actor, the same
// defaulting CreateTask and AddComment do, because everything reaching
// this API without saying otherwise is this deployment's operator acting
// through the browser. A dispatched run does say otherwise -- its
// update_memory call carries its own task (cmd/grain's daemonMemory) --
// which is the whole point of recording it: memory is replaced
// wholesale, and an operator's correction being overwritten by tonight's
// memory agent left nothing anywhere saying a person had said otherwise.
//
// Who may write at all is not this method's to decide: it trusts by, as
// every Client method trusts the in-process caller it serves. Over HTTP
// that decision is Server.memoryWriter's.
func (c *Client) PutMemory(ctx context.Context, repo, text string, by model.Principal) (Memory, error) {
	m := model.Memory{
		Text:      strings.TrimSpace(text),
		UpdatedBy: c.memoryAuthorOrActor(ctx, by),
	}
	if strings.TrimSpace(repo) != "" {
		parsed, err := c.parseRepo(repo)
		if err != nil {
			return Memory{}, err
		}
		m.Repo = &parsed
	}
	if len(m.Text) > model.MaxMemoryLength {
		return Memory{}, errMemoryLong
	}
	m.UpdatedAt = time.Now().UTC()
	if err := c.Store.PutMemory(ctx, m); err != nil {
		return Memory{}, err
	}
	if m.Text == "" {
		// Forgotten: there is no row left to describe, so the answer
		// describes the scope and says it is empty rather than reading
		// back a row that PutMemory just deleted.
		return memoryFrom(model.Memory{Repo: m.Repo}), nil
	}
	return memoryFrom(m), nil
}

// memoryAuthorOrActor is the principal a write is recorded as: the one
// the caller named, or this deployment's own actor for a caller that
// named nobody.
//
// One helper for both writes, rather than the defaulting spelled twice:
// "an unattributed write is the operator" is the rule, and a second
// spelling of it is the one that eventually disagrees.
func (c *Client) memoryAuthorOrActor(ctx context.Context, by model.Principal) model.Principal {
	if by.Kind == "" && by.ID == "" {
		return c.actor(ctx)
	}
	return by
}

// memoryFrom is one stored memory as this API reports it.
func memoryFrom(m model.Memory) Memory {
	out := Memory{Text: m.Text, UpdatedBy: m.UpdatedBy.ID, UpdatedByKind: string(m.UpdatedBy.Kind)}
	if m.Repo != nil {
		out.Repo = m.Repo.String()
	}
	if !m.UpdatedAt.IsZero() {
		at := m.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

// MemoryDocument is one named note as the API reports it
// (model.MemoryDocument, and model/memory_document.go for what a
// document is and why it is a row here rather than a file in the state
// repository).
//
// Text is empty in a listing and filled by a read of one document, which
// is the whole difference between the two calls: a listing is what
// builds an index, and an index that carried every document's text would
// be the thing documents exist to avoid. Length says how long the text
// is either way, so a caller deciding whether to ask for one knows what
// it is asking for.
//
// UpdatedBy and UpdatedByKind are Memory's own pair, said about a
// document: who last wrote it, absent for a row from before the author
// was recorded.
type MemoryDocument struct {
	Repo          string     `json:"repo,omitempty"`
	Name          string     `json:"name"`
	Summary       string     `json:"summary"`
	Text          string     `json:"text,omitempty"`
	Length        int        `json:"length"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy     string     `json:"updatedBy,omitempty"`
	UpdatedByKind string     `json:"updatedByKind,omitempty"`
}

// memoryDocumentRequest is PUT /api/memory/documents' body: which scope,
// which document, the line the index will carry and the document itself.
//
// Text replaces rather than appends, for memoryRequest's reason, and an
// empty text deletes the document outright -- the same way an empty
// memory forgets a scope, so that a writer that decides a note is no
// longer true has one call for saying so.
//
// Author/AuthorKind are memoryRequest's own pair, meaning the same thing
// and defaulted the same way.
type memoryDocumentRequest struct {
	Repo       string `json:"repo,omitempty"`
	Name       string `json:"name"`
	Summary    string `json:"summary"`
	Text       string `json:"text"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"authorKind,omitempty"`
}

// errMemoryDocumentLong, errMemorySummaryLong and errMemoryDocumentsMany
// are how a document that would cost more than it is worth is refused --
// ValidationErrors, so a 400 with a sentence the caller (in practice a
// model, through update_memory) can act on in the same turn.
//
// Three limits, because they buy three different things: the document's
// own bound keeps one read to something a run can take in, the summary's
// keeps the index line to a line, and the count keeps the index itself
// from becoming the diary that model.MaxMemoryLength exists to prevent.
var (
	errMemoryDocumentLong = validationErrorf(
		"a memory document must be at most %d characters: past that it has stopped being a note, "+
			"and the tasks and the state repository are where the rest belongs",
		model.MaxMemoryDocumentLength)
	errMemorySummaryLong = validationErrorf(
		"a memory document's summary must be at most %d characters: it is the one line about it "+
			"carried in the prompt of every run, so it says what the document is about rather than what it says",
		model.MaxMemoryDocumentSummaryLength)
	errMemorySummaryMissing = validationErrorf(
		"a memory document needs a summary: the index in every prompt is summaries, and a document " +
			"nobody can tell the subject of is one nobody will ever read")
	errMemoryDocumentsMany = validationErrorf(
		"a scope may keep at most %d memory documents: replace one that has stopped being true, or "+
			"fold two together, rather than adding a line to the index every run carries",
		model.MaxMemoryDocumentsPerScope)
)

// ListMemoryDocuments is every document in every scope, without their
// text -- the index the memory agent reviews and the listing
// list_memory_documents reports.
//
// Every scope, not the caller's: a document is written by a run reading
// what a whole deployment has been doing, and the one about a repo it is
// not working in is exactly the one it might want to correct. A
// dispatch's own two-layer index is built from the store directly
// (orchestrator.resolveMemory), not from here.
//
// It is also the Settings pane's index (MemoryPanel.jsx): every scope's
// documents by name, summary, size and age, with GetMemoryDocument below
// as what opening one costs -- the same bargain the prompt makes, where
// a run is told what is knowable and reads only what it turns out to
// need.
func (c *Client) ListMemoryDocuments(ctx context.Context, scopes ...model.MemoryScope) ([]MemoryDocument, error) {
	stored, err := c.Store.ListMemoryDocuments(ctx, scopes...)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryDocument, 0, len(stored))
	for _, d := range stored {
		out = append(out, memoryDocumentFrom(d))
	}
	return out, nil
}

// GetMemoryDocument reads one document whole: repo "" for one filed
// under the deployment, or "owner/name" for one filed under a repo.
//
// A document that does not exist is a NotFoundError rather than an empty
// one, because the two are different answers to the run that asked. An
// empty document would read as "there is a note about this and it says
// nothing"; the truth is that the name was wrong, or that the note has
// been deleted since the index the caller read was written.
func (c *Client) GetMemoryDocument(ctx context.Context, repo, name string) (MemoryDocument, error) {
	scope, err := memoryDocumentScope(c, repo)
	if err != nil {
		return MemoryDocument{}, err
	}
	name = strings.TrimSpace(name)
	if err := model.ValidateMemoryDocumentName(name); err != nil {
		return MemoryDocument{}, &ValidationError{err: err}
	}
	stored, err := c.Store.GetMemoryDocument(ctx, scope, name)
	if err != nil {
		return MemoryDocument{}, err
	}
	if stored == nil {
		return MemoryDocument{}, &NotFoundError{
			message: "no memory document " + name + " for " + memoryScopeName(repo)}
	}
	return memoryDocumentFrom(*stored), nil
}

// PutMemoryDocument replaces one document, or deletes it when the text
// is empty.
//
// This exists for the memory agent, the way PutMemory above does and by
// the same route: a run holding the memory capability calls
// update_memory with a document name, its MCP server asks the daemon,
// and the row lands here. Everything the write costs later runs is
// checked before it lands -- the summary that every prompt will carry,
// the text a read has to be able to return, and how many documents the
// scope's index may name -- so the run that wrote too much is told while
// it can still choose what to keep.
//
// by is who the write is recorded as, defaulted for a caller that names
// nobody exactly as PutMemory's is, and for the same reason.
func (c *Client) PutMemoryDocument(ctx context.Context, repo, name, summary, text string,
	by model.Principal) (MemoryDocument, error) {
	scope, err := memoryDocumentScope(c, repo)
	if err != nil {
		return MemoryDocument{}, err
	}
	d := model.MemoryDocument{
		Repo:      scope,
		Name:      strings.TrimSpace(name),
		Summary:   strings.TrimSpace(summary),
		Text:      strings.TrimSpace(text),
		UpdatedBy: c.memoryAuthorOrActor(ctx, by),
	}
	if err := model.ValidateMemoryDocumentName(d.Name); err != nil {
		return MemoryDocument{}, &ValidationError{err: err}
	}
	if d.Text != "" {
		switch {
		case d.Summary == "":
			return MemoryDocument{}, errMemorySummaryMissing
		case len(d.Summary) > model.MaxMemoryDocumentSummaryLength:
			return MemoryDocument{}, errMemorySummaryLong
		case len(d.Text) > model.MaxMemoryDocumentLength:
			return MemoryDocument{}, errMemoryDocumentLong
		}
		// Counted only for a document the scope does not already have:
		// replacing one is what a writer at the limit is being asked to
		// do, so the limit must not be what stops it.
		existing, err := c.Store.ListMemoryDocuments(ctx, model.MemoryScope{Repo: scope})
		if err != nil {
			return MemoryDocument{}, err
		}
		if len(existing) >= model.MaxMemoryDocumentsPerScope && !namesMemoryDocument(existing, d.Name) {
			return MemoryDocument{}, errMemoryDocumentsMany
		}
	}
	d.UpdatedAt = c.now()
	if err := c.Store.PutMemoryDocument(ctx, d); err != nil {
		return MemoryDocument{}, err
	}
	if d.Text == "" {
		// Deleted: there is no row left to describe, so the answer names
		// the scope and the document and carries nothing else.
		return memoryDocumentFrom(model.MemoryDocument{Repo: d.Repo, Name: d.Name}), nil
	}
	d.Length = len(d.Text)
	return memoryDocumentFrom(d), nil
}

// memoryDocumentScope parses the repo a document is filed under, through
// the same parseRepo every other repo-shaped field in this API goes
// through -- nil for the deployment's own scope, which is what an empty
// repo means everywhere memory is concerned.
func memoryDocumentScope(c *Client, repo string) (*model.RepoRef, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, nil
	}
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// memoryScopeName names a scope in a message, since "" would read as a
// missing repo rather than as the deployment itself.
func memoryScopeName(repo string) string {
	if repo = strings.TrimSpace(repo); repo != "" {
		return repo
	}
	return "this deployment"
}

// namesMemoryDocument reports whether a scope already keeps a document
// by this name -- the difference between a write that adds a line to
// every later prompt and one that replaces a line already there.
func namesMemoryDocument(docs []model.MemoryDocument, name string) bool {
	for _, d := range docs {
		if d.Name == name {
			return true
		}
	}
	return false
}

// memoryDocumentFrom is one stored document as this API reports it.
func memoryDocumentFrom(d model.MemoryDocument) MemoryDocument {
	out := MemoryDocument{
		Name: d.Name, Summary: d.Summary, Text: d.Text, Length: d.Length,
		UpdatedBy: d.UpdatedBy.ID, UpdatedByKind: string(d.UpdatedBy.Kind),
	}
	if d.Repo != nil {
		out.Repo = d.Repo.String()
	}
	if !d.UpdatedAt.IsZero() {
		at := d.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

// memoryWriter is who a memory write on r is recorded as, or the error
// refusing it.
//
// Memory is the deployment's, every scope of it (single-tenant reset,
// stage 4d): a person -- the deployment's owner, since whoever holds the
// access code is (docs/access-code.md) -- writes it by hand, and an
// agent only from a task holding the memory capability, because it goes
// into the prompt of every run this deployment dispatches.
func (s *Server) memoryWriter(r *http.Request, id, kind, repo string) (model.Principal, error) {
	p, hasPrincipal := PrincipalFromContext(r.Context())
	if hasPrincipal && p.Kind == model.PrincipalHuman {
		return p, nil
	}

	by, err := memoryAuthor(id, kind)
	if err != nil {
		return model.Principal{}, err
	}
	if hasPrincipal {
		by = p
	}
	if by.Kind == model.PrincipalAgent {
		if err := s.tasks.requireMemoryGrant(r.Context(), by.ID, repo); err != nil {
			return model.Principal{}, err
		}
	}
	return by, nil
}

// requireMemoryGrant refuses a write attributed to task id's run unless
// that task holds the memory capability. Every memory grant reaches
// every scope: there is no team memory agent writing only its own
// team's layer any more.
func (c *Client) requireMemoryGrant(ctx context.Context, id, repo string) error {
	if call, ok := agentCallFromContext(ctx); ok && call.external != nil {
		if call.grants.Has("memory:write") {
			return nil
		}
		return forbiddenErrorf("%s does not hold the memory:write scope, so it may not write memory", id)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return forbiddenErrorf("task %s does not exist, so its run may not write memory", id)
	}
	for _, g := range task.Grants {
		if g.Capability == memory.CapabilityName {
			return nil
		}
	}
	if APIGrantsOf(grantNames(task.Grants)).Allows("memory:write", strings.TrimSpace(repo)) {
		return nil
	}
	return forbiddenErrorf("task %s does not hold the %q capability, so its run may not write memory", id, memory.CapabilityName)
}

// handleListMemories answers GET /api/memory. An old client's ?team= is
// ignored: there is one deployment's memory to list.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	memories, err := s.tasks.ListMemories(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memories)
}

// handlePutMemory answers PUT /api/memory.
//
// One route for all scopes, with the repo in the body.
func (s *Server) handlePutMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryRequest
	if !readJSON(w, r, &req) {
		return
	}
	by, err := s.memoryWriter(r, req.Author, req.AuthorKind, req.Repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	mem, err := s.tasks.PutMemory(r.Context(), req.Repo, req.Text, by)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mem)
}

// handleListMemoryDocuments answers GET /api/memory/documents: every
// scope's documents without their text.
func (s *Server) handleListMemoryDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := s.tasks.ListMemoryDocuments(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// handleGetMemoryDocument answers GET /api/memory/documents/{name}, with
// the scope in ?repo=.
func (s *Server) handleGetMemoryDocument(w http.ResponseWriter, r *http.Request) {
	doc, err := s.tasks.GetMemoryDocument(r.Context(), r.URL.Query().Get("repo"), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// handlePutMemoryDocument answers PUT /api/memory/documents.
func (s *Server) handlePutMemoryDocument(w http.ResponseWriter, r *http.Request) {
	var req memoryDocumentRequest
	if !readJSON(w, r, &req) {
		return
	}
	by, err := s.memoryWriter(r, req.Author, req.AuthorKind, req.Repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	doc, err := s.tasks.PutMemoryDocument(r.Context(), req.Repo, req.Name, req.Summary, req.Text, by)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}
