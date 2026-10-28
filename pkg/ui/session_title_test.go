package ui_test

// PUT /api/tasks/{id}/session-title: the hop a standing session's own
// mcpserver makes to retitle its chat from what the conversation turned
// out to be about (the rename_session tool, pkg/mcp). Like
// POST /api/tasks/{id}/activity there is nothing behind it to stand a
// fake in for -- the write is UpdateConsoleSession's, into the settings
// row the daemon already holds -- so these tests start a session from a
// first message the way the UI does and assert on what the sessions list
// then says.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// sessionFromMessage opens a chat from its first message, the way "New
// session" does, and returns the session as listed -- heuristically
// titled, with the task holding its conversation.
func sessionFromMessage(t *testing.T, client *ui.Client, message string) ui.ConsoleSessionSetting {
	t.Helper()
	before, err := client.ListConsoleSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]bool{}
	for _, s := range before {
		existing[s.ID] = true
	}
	sessions, err := client.CreateConsoleSession(t.Context(), ui.CreateConsoleSessionRequest{Message: message})
	if err != nil {
		t.Fatalf("CreateConsoleSession from a message: %v", err)
	}
	for _, s := range sessions {
		if !existing[s.ID] {
			if s.TaskID == "" {
				t.Fatalf("session %q has no task", s.ID)
			}
			return s
		}
	}
	t.Fatalf("no new session in %+v", sessions)
	return ui.ConsoleSessionSetting{}
}

// The whole point: a session opened from "hi, can you look at why the
// nightly is red" is, once its agent has seen what it is about, titled
// "Nightly CI failure" in the sessions list -- and the write is keyed by
// the task the run serves, which is all a run knows about itself.
func TestRenameConsoleSessionOfTaskRetitlesTheSession(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	session := sessionFromMessage(t, client, "hi, can you look at why the nightly is red")
	if session.Title != "Hi, can you look at why the nightly" {
		t.Fatalf("heuristic title = %q, want the opening words", session.Title)
	}

	renamed, err := client.RenameConsoleSessionOfTask(ctx, session.TaskID, "Nightly CI failure")
	if err != nil {
		t.Fatalf("RenameConsoleSessionOfTask: %v", err)
	}
	if renamed.SessionID != session.ID || renamed.Title != "Nightly CI failure" {
		t.Fatalf("renamed = %+v, want session %q titled \"Nightly CI failure\"", renamed, session.ID)
	}

	listed, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *ui.ConsoleSessionSetting
	for i := range listed {
		if listed[i].ID == session.ID {
			found = &listed[i]
		}
	}
	if found == nil {
		t.Fatalf("session %q is gone from the list: %+v", session.ID, listed)
	}
	if found.Title != "Nightly CI failure" {
		t.Errorf("listed title = %q, want the agent's", found.Title)
	}
	// Nothing else about the session moved: same id, same task, and the
	// envelope it was created with.
	if found.TaskID != session.TaskID || len(found.Capabilities) != len(session.Capabilities) ||
		found.NetworkAccess != session.NetworkAccess || found.TasksApproved != session.TasksApproved {
		t.Errorf("session after rename = %+v, want only the title changed from %+v", *found, session)
	}

	// The write is the settings row's; the task is the reconciler's to
	// carry it onto on its next cycle (orchestrator.syncConsoleSessionTask),
	// so the task still reads the old title here and the stored session
	// the new one -- the same one-writer discipline every other setting
	// follows.
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored *model.ConsoleSession
	for i := range cfg.ConsoleSessions {
		if cfg.ConsoleSessions[i].ID == session.ID {
			stored = &cfg.ConsoleSessions[i]
		}
	}
	if stored == nil || stored.Title != "Nightly CI failure" {
		t.Errorf("stored session = %+v, want the new title in the config row", stored)
	}
}

// storedSession is session id's row as the config holds it.
func storedSession(t *testing.T, store *model.Store, id string) model.ConsoleSession {
	t.Helper()
	cfg, err := store.GetConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.ConsoleSessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no stored session %q", id)
	return model.ConsoleSession{}
}

// A session is named by its agent once: the first rename is recorded as
// the agent's, and a later run's -- whose turn might well be on a new
// topic -- is refused with an error that tells it to stop.
func TestRenameConsoleSessionOfTaskNamesASessionOnce(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	session := sessionFromMessage(t, client, "the merge queue looks stuck again")
	if got := storedSession(t, store, session.ID).TitleSource; got != model.TitleSourceHeuristic {
		t.Fatalf("a session titled from its first message has source %q, want heuristic", got)
	}
	if _, err := client.RenameConsoleSessionOfTask(ctx, session.TaskID, "Merge queue stall"); err != nil {
		t.Fatal(err)
	}
	if got := storedSession(t, store, session.ID).TitleSource; got != model.TitleSourceAgent {
		t.Errorf("after the agent's rename, source = %q, want agent", got)
	}
	_, err := client.RenameConsoleSessionOfTask(ctx, session.TaskID, "Flaky e2e on main")
	var ve *ui.ValidationError
	if err == nil || !errors.As(err, &ve) || !strings.Contains(err.Error(), "already named") {
		t.Fatalf("second rename: err = %v, want a validation error saying it is already named", err)
	}
	if got := storedSession(t, store, session.ID).Title; got != "Merge queue stall" {
		t.Errorf("title = %q, want the first rename still standing", got)
	}
}

// A name a person typed -- on the new-session form, or in the settings
// pane -- is theirs: the agent's rename is refused. A person can still
// rename freely, including over an agent's name.
func TestRenameConsoleSessionOfTaskLeavesAPersonsName(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	// The settings pane: an agent's name, overwritten by a person, is
	// the person's from then on.
	chat := sessionFromMessage(t, client, "hi, can you look at why the nightly is red")
	if _, err := client.RenameConsoleSessionOfTask(ctx, chat.TaskID, "Nightly CI failure"); err != nil {
		t.Fatal(err)
	}
	title := "Nightly triage"
	if _, err := client.UpdateConsoleSession(ctx, chat.ID, ui.UpdateConsoleSessionRequest{Title: &title}); err != nil {
		t.Fatalf("a person's rename: %v", err)
	}
	got := storedSession(t, store, chat.ID)
	if got.Title != "Nightly triage" || got.TitleSource != model.TitleSourcePerson {
		t.Errorf("after a person's rename, session = %+v, want their title with source person", got)
	}
	if _, err := client.RenameConsoleSessionOfTask(ctx, chat.TaskID, "Nightly CI failure"); err == nil {
		t.Error("an agent overwrote the title a person set in the settings pane")
	}

	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Title: "Release coordinator", Message: "let's plan the next cut",
	}); err != nil {
		t.Fatal(err)
	}
	named := storedSession(t, store, "release-coordinator")
	if named.TitleSource != model.TitleSourcePerson {
		t.Fatalf("a session named on the form has source %q, want person", named.TitleSource)
	}
	task, err := store.OpenConsoleSessionTaskIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RenameConsoleSessionOfTask(ctx, task[named.ID], "Release planning"); err == nil ||
		!strings.Contains(err.Error(), "by a person") {
		t.Errorf("renaming a person's session: err = %v, want a refusal", err)
	}
}

// The settings pane saves every field. Ticking a capability sends the
// title the session already had, which is not a person choosing it.
func TestUpdateConsoleSessionUnchangedTitleKeepsItsSource(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	chat := sessionFromMessage(t, client, "the merge queue looks stuck again")
	title := chat.Title
	approved := true
	if _, err := client.UpdateConsoleSession(ctx, chat.ID, ui.UpdateConsoleSessionRequest{Title: &title, TasksApproved: &approved}); err != nil {
		t.Fatal(err)
	}
	if got := storedSession(t, store, chat.ID).TitleSource; got != model.TitleSourceHeuristic {
		t.Errorf("source after saving the unchanged title = %q, want heuristic still", got)
	}
}

// A session saved before titles recorded their source: renamable only if
// its title is still what its first message gives.
func TestRenameConsoleSessionOfTaskInfersLegacySessions(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	heuristic := sessionFromMessage(t, client, "hi, can you look at why the nightly is red")
	retitled := sessionFromMessage(t, client, "the merge queue looks stuck again")
	if heuristic.ID == retitled.ID {
		t.Fatalf("both chats are session %q", heuristic.ID)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.ConsoleSessions {
		switch cfg.ConsoleSessions[i].ID {
		case heuristic.ID:
			cfg.ConsoleSessions[i].TitleSource = ""
		case retitled.ID:
			cfg.ConsoleSessions[i].TitleSource = ""
			cfg.ConsoleSessions[i].Title = "Merge queue stall"
		}
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := client.RenameConsoleSessionOfTask(ctx, heuristic.TaskID, "Nightly CI failure"); err != nil {
		t.Errorf("legacy session still titled from its first message: rename refused: %v", err)
	}
	if got := storedSession(t, store, heuristic.ID).TitleSource; got != model.TitleSourceAgent {
		t.Errorf("legacy session after rename: source = %q, want agent", got)
	}
	if _, err := client.RenameConsoleSessionOfTask(ctx, retitled.TaskID, "Something else"); err == nil {
		t.Error("legacy session whose title is not its first message's was renamed")
	}
}

// Three refusals, each the caller's own mistake and so a 400 rather than
// a 500: a task that is not a session, nothing to show, and too much to
// show. The bound is the model's own, the one the heuristic title is cut
// to, so a title the agent chooses can never be longer than one grain
// derived.
func TestRenameConsoleSessionOfTaskRefusesWhatItCannotName(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	ordinary := create(t, client, ctx)
	if _, err := client.RenameConsoleSessionOfTask(ctx, ordinary.ID, "Nightly CI failure"); err == nil ||
		!strings.Contains(err.Error(), "not a standing chat session") {
		t.Errorf("renaming an ordinary task: err = %v, want a refusal naming the reason", err)
	}
	var nf *ui.NotFoundError
	if _, err := client.RenameConsoleSessionOfTask(ctx, "no-such-task", "Nightly CI failure"); err == nil ||
		!errors.As(err, &nf) {
		t.Errorf("renaming a task that does not exist: err = %v, want not found", err)
	}

	session := sessionFromMessage(t, client, "hi, can you look at why the nightly is red")
	for _, tt := range []struct {
		name  string
		title string
		want  string
	}{
		{"empty", "   \n ", "cannot be empty"},
		{"too long", strings.Repeat("x", model.ConsoleSessionTitleMaxLength+1), "at most"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.RenameConsoleSessionOfTask(ctx, session.TaskID, tt.title); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	// Nothing was written by any attempt.
	listed, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range listed {
		if s.ID == session.ID && s.Title != "Hi, can you look at why the nightly" {
			t.Errorf("title = %q, want the heuristic one still standing", s.Title)
		}
	}
}

// The route, as the mcpserver's HTTP client calls it.
func TestRenameTaskSessionHTTPRoute(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)
	session := sessionFromMessage(t, client, "hi, can you look at why the nightly is red")

	rec := do(t, srv, http.MethodPut, "/api/tasks/"+session.TaskID+"/session-title", `{"title": "Nightly CI failure"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", rec.Code, rec.Body)
	}
	renamed := decode[ui.SessionTitle](t, rec)
	if renamed.SessionID != session.ID || renamed.Title != "Nightly CI failure" {
		t.Errorf("answer = %+v, want the session and its new title", renamed)
	}
	listed, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range listed {
		if s.ID == session.ID && s.Title != "Nightly CI failure" {
			t.Errorf("listed title = %q, want the one PUT wrote", s.Title)
		}
	}

	ordinary := create(t, client, ctx)
	if rec := do(t, srv, http.MethodPut, "/api/tasks/"+ordinary.ID+"/session-title", `{"title": "Nightly CI failure"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT on an ordinary task: status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPut, "/api/tasks/"+session.TaskID+"/session-title", `{"title": ""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with no title: status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPut, "/api/tasks/no-such-task/session-title", `{"title": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT on a missing task: status = %d, want 404: %s", rec.Code, rec.Body)
	}
}
