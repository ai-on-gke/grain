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

// The standing sessions as an API (grain/task-415): what a deployment
// that has never configured one is told it has, and what a person may
// change about one from the pane the chat is in.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestConsoleSessionsListsTheDirectorBeforeAnythingIsConfigured(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)

	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != model.DirectorSessionID {
		t.Fatalf("ListConsoleSessions = %v, want the director -- which is what this deployment will have "+
			"as soon as a cycle runs", sessions)
	}
	if sessions[0].TaskID != "" {
		t.Errorf("TaskID = %q, want empty: no cycle has filed the task yet", sessions[0].TaskID)
	}
	if sessions[0].Title == "" {
		t.Error("the session has no title to put in the rail")
	}
}

func TestConsoleSessionsNamesTheTaskHoldingTheConversation(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	// The task a cycle would have filed: what ties it to the session is
	// the tag, and nothing else.
	task := model.Task{
		ID:     "77",
		Intent: model.IntentAnalyze,
		Title:  "Director",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "console-session"}},
			Reason:      model.ReasonConsoleSession,
		},
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Interactive: true,
		CreatedAt:   &baseTime,
		Approval:    &model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "console-session"}},
		ApprovedAt:  &baseTime,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TaskID != "77" {
		t.Fatalf("ListConsoleSessions = %v, want the director pointing at task 77", sessions)
	}

	// And the task itself says which session it belongs to, which is what
	// keeps a permanently parked session out of the inbox.
	listed, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range listed {
		if task.ID == "77" {
			found = true
			if task.ConsoleSession != model.DirectorSessionID {
				t.Errorf("consoleSession = %q, want %q", task.ConsoleSession, model.DirectorSessionID)
			}
		}
	}
	if !found {
		t.Fatal("the session's task is not in the task list at all")
	}
}

// sessionTask is the task a cycle would have filed for the director:
// what ties it to the session is the tag, and nothing else.
func sessionTask(id string) model.Task {
	principal := model.Attribution{Actor: model.ConsoleSessionPrincipal}
	return model.Task{
		ID:          id,
		Intent:      model.IntentAnalyze,
		Title:       "Director",
		Origin:      model.Origin{Attribution: principal, Reason: model.ReasonConsoleSession},
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Interactive: true,
		CreatedAt:   &baseTime,
		Approval:    &principal,
		ApprovedAt:  &baseTime,
	}
}

// What the pane's offer of a fresh conversation is shown on: the last
// exchange's own prompt, and nothing about how old the chat is. A
// session nobody has said anything to yet has no measurement and is
// reported as ordinary.
func TestAConsoleSessionIsCrowdedByItsLastPromptRatherThanItsAge(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	if err := store.PutTask(ctx, sessionTask("77")); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Crowded || sessions[0].CrowdedNote != "" {
		t.Errorf("a session with no exchange yet reports crowded=%v (%q), want nothing to offer",
			sessions[0].Crowded, sessions[0].CrowdedNote)
	}

	// One ordinary exchange. A short prompt says nothing is wrong,
	// however long the conversation has been open.
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "77", Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPrompt(ctx, "r1", "you are running as task 77\n- ada: how is it going?\n"); err != nil {
		t.Fatal(err)
	}
	sessions, err = client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Crowded {
		t.Errorf("an ordinary exchange reports crowded (%q) -- the offer is for a conversation that "+
			"costs its runs, not for one that is old", sessions[0].CrowdedNote)
	}

	// And one whose prompt has crossed model.PromptWarnTokens: a run
	// that starts with a quarter of its window already spent on this
	// conversation.
	if err := store.StartRun(ctx, model.Run{
		ID: "r2", TaskID: "77", Sandbox: "s2", Attempt: 2, StartedAt: baseTime.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", model.PromptWarnTokens*model.PromptBytesPerToken+1)
	if err := store.SetRunPrompt(ctx, "r2", big); err != nil {
		t.Fatal(err)
	}
	sessions, err = client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sessions[0].Crowded {
		t.Fatal("a prompt past the warning threshold does not report crowded -- there is nothing to offer " +
			"a fresh conversation on but this")
	}
	if !strings.Contains(sessions[0].CrowdedNote, "token") {
		t.Errorf("CrowdedNote = %q, want the measurement behind it -- a control that appears without "+
			"saying why is one nobody can judge", sessions[0].CrowdedNote)
	}
}

// The other half of the same question, and the one a size alone cannot
// answer: grain had to drop the middle of this conversation to fit the
// last prompt, which is the bound doing its job and the clearest sign
// that a fresh thread is worth offering.
func TestAConsoleSessionWhoseThreadWasCutIsCrowded(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	if err := store.PutTask(ctx, sessionTask("77")); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "77", Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPrompt(ctx, "r1", "you are running as task 77\n"+
		model.PromptThreadCutNotice+"40 message(s) from the middle of it, 30 KB of text, elided\n"); err != nil {
		t.Fatal(err)
	}

	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sessions[0].Crowded || sessions[0].CrowdedNote == "" {
		t.Fatalf("a cut thread reports crowded=%v (%q), want the offer shown with its reason",
			sessions[0].Crowded, sessions[0].CrowdedNote)
	}
}

func TestUpdatingASessionsRepoGrantsIsStored(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	reads := []string{"acme/shared-lib", "acme/docs"}
	writes := []ui.WriteTarget{{Repo: "acme/deploy", Base: "production"}}
	sessions, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{
			Reads:  &reads,
			Writes: &writes,
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Reads) != 2 || len(sessions[0].Writes) != 1 {
		t.Fatalf("the update answered with %+v", sessions)
	}
	if sessions[0].Reads[0] != "acme/shared-lib" || sessions[0].Reads[1] != "acme/docs" {
		t.Errorf("Reads = %v", sessions[0].Reads)
	}
	if sessions[0].Writes[0].Repo != "acme/deploy" || sessions[0].Writes[0].Base != "production" {
		t.Errorf("Writes = %v", sessions[0].Writes)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.ConsoleSessions) != 1 || len(cfg.ConsoleSessions[0].Reads) != 2 || len(cfg.ConsoleSessions[0].Writes) != 1 {
		t.Fatalf("grain_config holds %+v", cfg)
	}
}

func TestUpdatingASessionsCapabilitiesIsStored(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	capabilities := []string{"self-debug", "self-repair"}
	sessions, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{Capabilities: &capabilities})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !slices.Equal(sessions[0].Capabilities, capabilities) {
		t.Fatalf("the update answered with %v, want the capabilities just chosen", sessions)
	}

	// Stored on the config row, which is where the reconciler reads it
	// from on its next cycle.
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.ConsoleSessions) != 1 ||
		!slices.Equal(cfg.ConsoleSessions[0].Capabilities, capabilities) {
		t.Fatalf("grain_config holds %v, want the director with its new capabilities", cfg)
	}
	// A first save must not write off the settings whose default is not
	// their zero value, the same rule plannedConfig follows.
	if !cfg.ApprovedByDefault {
		t.Error("saving a session turned approvedByDefault off, which nobody asked for")
	}
}

func TestUpdatingASessionRefusesWhatCannotTakeEffect(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)

	unknown := []string{"telepathy"}
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{Capabilities: &unknown}); err == nil {
		t.Error("a capability this deployment does not offer was accepted")
	}
	access := "sometimes"
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{NetworkAccess: &access}); err == nil {
		t.Error("a network access grain cannot build a sandbox for was accepted")
	}
	empty := ""
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{NetworkAccess: &empty}); err != nil {
		t.Errorf("clearing the override back to this deployment's own default was refused: %v", err)
	}
}

// Inventing a session because somebody PUT to a name is how a typo
// becomes a permanent agent.
func TestUpdatingASessionThisDeploymentDoesNotHaveIsRefused(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)

	title := "Overseer"
	if _, err := client.UpdateConsoleSession(ctx, "overseer",
		ui.UpdateConsoleSessionRequest{Title: &title}); err == nil {
		t.Fatal("a session nobody configured was updated into existence")
	}
}

// The pane that edits a session's grants offers only what this
// deployment has actually configured (configuredCapabilities,
// ui/src/state.js), so the listing it draws from has to say which those
// are -- the same readiness GET /api/config carries per row. Without it
// the pane would go on offering a gemini-key no session could use, after
// every other picker in the app stopped.
func TestConsoleSessionCapabilitiesSayWhichOnesThisDeploymentCanHonour(t *testing.T) {
	t.Parallel()
	_, store, _ := testClient(t)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	rec := do(t, srv, http.MethodGet, "/api/console-sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)

	byID := make(map[string]ui.Capability, len(resp.Capabilities))
	for _, capability := range resp.Capabilities {
		byID[capability.ID] = capability
	}
	// Nothing is configured on this deployment: no GCP project, no
	// secret pasted, so the two that need one are not ready and say so.
	gemini, ok := byID["gemini-key"]
	if !ok {
		t.Fatalf("gemini-key is missing from %v", resp.Capabilities)
	}
	if gemini.Ready == nil || *gemini.Ready {
		t.Errorf("gemini-key ready = %v, want false on a deployment with no GCP project", gemini.Ready)
	}
	if len(gemini.Needs) == 0 {
		t.Error("gemini-key says it is not ready without naming a single thing it needs")
	}
	// One that needs no configuration at all stays on offer.
	debug, ok := byID["self-debug"]
	if !ok {
		t.Fatalf("self-debug is missing from %v", resp.Capabilities)
	}
	if debug.Ready == nil || !*debug.Ready {
		t.Errorf("self-debug ready = %v, want true: it needs nothing configured", debug.Ready)
	}
}

func TestUpdatingASessionsGoogleChatSpaceIsStored(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	space := "spaces/AAAAAAAAAAA"
	sessions, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{GoogleChatSpace: &space})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].GoogleChatSpace != space {
		t.Fatalf("the update answered with %+v, want GoogleChatSpace = %q", sessions, space)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.ConsoleSessions) != 1 || cfg.ConsoleSessions[0].GoogleChatSpace != space {
		t.Fatalf("grain_config holds %+v, want GoogleChatSpace = %q", cfg, space)
	}
}

func TestCreateConsoleSessionAndHTTPRoute(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	// Create via Client
	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Title: "Code Reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d, want 2 (director + code-reviewer)", len(sessions))
	}
	if sessions[1].ID != "code-reviewer" || sessions[1].Title != "Code Reviewer" {
		t.Errorf("created session = %+v, want ID 'code-reviewer', Title 'Code Reviewer'", sessions[1])
	}

	// Create duplicate ID should fail
	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "code-reviewer",
		Title: "Another Reviewer",
	}); err == nil {
		t.Error("duplicate ID was accepted")
	}

	// Neither a title nor a first message should fail
	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Title: "   ",
	}); err == nil {
		t.Error("a session with neither a title nor a message was accepted")
	}

	// The id the UI's own unsaved chat sits on is nobody's to take.
	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "new",
		Title: "Taken",
	}); err == nil {
		t.Error(`the reserved id "new" was accepted`)
	}

	// Create via POST HTTP endpoint
	body := `{"title": "Release Manager", "networkAccess": "none", "tasksApproved": true}`
	rec := do(t, srv, http.MethodPost, "/api/console-sessions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/console-sessions status = %d, want 201: %s", rec.Code, rec.Body)
	}
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if len(resp.Sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(resp.Sessions))
	}
	if resp.Sessions[2].ID != "release-manager" || resp.Sessions[2].NetworkAccess != "none" || !resp.Sessions[2].TasksApproved {
		t.Errorf("created session via HTTP = %+v", resp.Sessions[2])
	}
	if resp.Created != "release-manager" {
		t.Errorf("Created = %q, want the new session's id", resp.Created)
	}
}

// A chat started the way a chat app starts one: no name, just the first
// message. The session is named from it, the message is in the new
// conversation as the caller's own, and the first exchange is queued.
func TestCreateConsoleSessionFromItsFirstMessage(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	body := `{"message": "why does the merge queue stall on candidate? It has been holding the same batch all morning."}`
	rec := do(t, srv, http.MethodPost, "/api/console-sessions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/console-sessions status = %d, want 201: %s", rec.Code, rec.Body)
	}
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if resp.Created == "" {
		t.Fatalf("Created is empty: %+v", resp)
	}
	var created ui.ConsoleSessionSetting
	for _, s := range resp.Sessions {
		if s.ID == resp.Created {
			created = s
		}
	}
	if created.ID == "" {
		t.Fatalf("Created %q is not in the list %+v", resp.Created, resp.Sessions)
	}
	if want := "Why does the merge queue stall on candidate"; created.Title != want {
		t.Errorf("Title = %q, want %q (the first sentence of the message)", created.Title, want)
	}
	if created.ID != "why-does-the-merge-queue-stall-on-candidate" {
		t.Errorf("ID = %q, want the title's slug", created.ID)
	}
	if created.TaskID == "" {
		t.Fatalf("the session's task was not filed: %+v", created)
	}
	task, err := store.GetTask(ctx, created.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != created.Title {
		t.Errorf("task title = %q, want the session's %q", task.Title, created.Title)
	}
	comments, err := store.Comments(ctx, created.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var posted *model.Comment
	for i := range comments {
		if strings.HasPrefix(comments[i].Body, "why does the merge queue stall") {
			posted = &comments[i]
		}
	}
	if posted == nil {
		t.Fatalf("the first message is not in the conversation: %+v", comments)
	}
	if posted.Author.Actor.Kind != model.PrincipalHuman || posted.Author.Actor.ID != "alice" {
		t.Errorf("the first message is by %+v, want the caller (alice)", posted.Author.Actor)
	}
	state, err := store.State(ctx, created.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Errorf("state = %q, want %q: the first message should start the first exchange", state, model.StateQueued)
	}

	// A second chat opening with the same words gets its own session.
	rec = do(t, srv, http.MethodPost, "/api/console-sessions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second POST status = %d: %s", rec.Code, rec.Body)
	}
	second := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if second.Created == "" || second.Created == resp.Created {
		t.Errorf("second Created = %q, want a fresh id beside %q", second.Created, resp.Created)
	}

	// A message nothing can be read off still starts a chat, under the
	// stand-in name.
	rec = do(t, srv, http.MethodPost, "/api/console-sessions", `{"message": "`+"```\\nls\\n```"+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code-only POST status = %d: %s", rec.Code, rec.Body)
	}
	third := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	for _, s := range third.Sessions {
		if s.ID == third.Created && s.Title != model.ConsoleSessionUntitled {
			t.Errorf("code-only chat is titled %q, want %q", s.Title, model.ConsoleSessionUntitled)
		}
	}

	// A chat nobody has said anything in is not created at all.
	rec = do(t, srv, http.MethodPost, "/api/console-sessions", `{"message": "   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty-message POST status = %d, want 400: %s", rec.Code, rec.Body)
	}

	// A title given with the message wins over the derived one.
	rec = do(t, srv, http.MethodPost, "/api/console-sessions", `{"title": "Merge watch", "message": "why does the merge queue stall?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("titled POST status = %d: %s", rec.Code, rec.Body)
	}
	titled := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if titled.Created != "merge-watch" {
		t.Errorf("titled Created = %q, want merge-watch", titled.Created)
	}
}

func TestDeleteConsoleSessionAndHTTPRoute(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	// Create a session first
	_, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "assistant",
		Title: "Assistant",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delete non-existent session
	if err := client.DeleteConsoleSession(ctx, "nonexistent"); err == nil {
		t.Error("deleting non-existent session should fail")
	}

	// Delete via Client
	if err := client.DeleteConsoleSession(ctx, "assistant"); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != model.DirectorSessionID {
		t.Fatalf("after delete, sessions = %+v, want only director", sessions)
	}

	// Create another and delete via DELETE HTTP endpoint
	_, err = client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "to-delete",
		Title: "To Delete",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodDelete, "/api/console-sessions/to-delete", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/console-sessions/to-delete status = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec404 := do(t, srv, http.MethodDelete, "/api/console-sessions/to-delete", "")
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("DELETE non-existent status = %d, want 404", rec404.Code)
	}
}

func TestConsoleSessionTeamAssociation(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	// Director defaults to "default" team
	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Team != model.DefaultTeamID {
		t.Fatalf("director session team = %q, want %q", sessions[0].Team, model.DefaultTeamID)
	}

	// Create with explicit team
	sessions, err = client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "infra-assistant",
		Title: "Infra Assistant",
		Team:  "infra",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[1].Team != "infra" {
		t.Fatalf("created session team = %q, want 'infra'", sessions[1].Team)
	}

	// Create with empty team defaults to "default"
	sessions, err = client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "general-assistant",
		Title: "General Assistant",
		Team:  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || sessions[2].Team != model.DefaultTeamID {
		t.Fatalf("created session with empty team = %q, want %q", sessions[2].Team, model.DefaultTeamID)
	}

	// Update team via client
	newTeam := "backend"
	sessions, err = client.UpdateConsoleSession(ctx, "infra-assistant", ui.UpdateConsoleSessionRequest{
		Team: &newTeam,
	})
	if err != nil {
		t.Fatal(err)
	}
	var updated ui.ConsoleSessionSetting
	for _, s := range sessions {
		if s.ID == "infra-assistant" {
			updated = s
			break
		}
	}
	if updated.Team != "backend" {
		t.Fatalf("updated session team = %q, want 'backend'", updated.Team)
	}

	// Create via HTTP POST with team
	body := `{"title": "Security Bot", "team": "security"}`
	rec := do(t, srv, http.MethodPost, "/api/console-sessions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/console-sessions status = %d: %s", rec.Code, rec.Body)
	}
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	var secBot ui.ConsoleSessionSetting
	for _, s := range resp.Sessions {
		if s.ID == "security-bot" {
			secBot = s
			break
		}
	}
	if secBot.Team != "security" {
		t.Fatalf("HTTP created session team = %q, want 'security'", secBot.Team)
	}
}

type fakeTaskCanceller struct {
	cancelled []string
}

func (f *fakeTaskCanceller) CancelTask(_ context.Context, taskID string) bool {
	f.cancelled = append(f.cancelled, taskID)
	return true
}

func TestRestartConsoleSessionWhileIdleAndWhileRunning(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	canceller := &fakeTaskCanceller{}
	client.Config.TaskCanceller = canceller
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		Capabilities:  ui.OfferedCapabilities(),
		Secrets:       secrets.New(t.TempDir()),
		TaskCanceller: canceller,
	}, store)

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "ops",
		Title: "Ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	var ops ui.ConsoleSessionSetting
	for _, s := range sessions {
		if s.ID == "ops" {
			ops = s
			break
		}
	}
	if ops.TaskID == "" {
		t.Fatalf("CreateConsoleSession did not synchronously file a task: %+v", ops)
	}
	firstTaskID := ops.TaskID

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    firstTaskID,
		Author:    model.Attribution{Actor: model.ConsoleSessionPrincipal},
		Body:      "Standing summary from first conversation.",
		CreatedAt: now,
		Handover:  true,
	}); err != nil {
		t.Fatal(err)
	}

	// Restart while idle with carryHandover: false (full reset).
	rec := do(t, srv, http.MethodPost, "/api/console-sessions/ops/restart", `{"carryHandover":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/console-sessions/ops/restart status = %d: %s", rec.Code, rec.Body)
	}
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	var restarted ui.ConsoleSessionSetting
	for _, s := range resp.Sessions {
		if s.ID == "ops" {
			restarted = s
			break
		}
	}
	if restarted.TaskID == "" || restarted.TaskID == firstTaskID {
		t.Fatalf("restarted.TaskID = %q, want a new task ID != %q", restarted.TaskID, firstTaskID)
	}
	secondTaskID := restarted.TaskID

	obs1, err := store.GetObservation(ctx, firstTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs1 == nil || obs1.ClosedAt == nil {
		t.Fatalf("first task %s was not closed on restart: obs = %+v", firstTaskID, obs1)
	}
	comments2, err := store.Comments(ctx, secondTaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments2 {
		if c.Handover || strings.Contains(c.Body, "Standing summary from first conversation.") {
			t.Fatalf("full reset carried handover onto %s: %+v", secondTaskID, comments2)
		}
	}

	// Now start a live run on secondTaskID and write a handover, then restart with carryHandover: true.
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    secondTaskID,
		Author:    model.Attribution{Actor: model.ConsoleSessionPrincipal},
		Body:      "Standing summary from second conversation.",
		CreatedAt: now.Add(time.Minute),
		Handover:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        secondTaskID + "-1",
		TaskID:    secondTaskID,
		Attempt:   1,
		StartedAt: now.Add(2 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodPost, "/api/console-sessions/ops/restart", `{"carryHandover":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/console-sessions/ops/restart while running status = %d: %s", rec.Code, rec.Body)
	}
	resp = decode[ui.ConsoleSessionSettingsResponse](t, rec)
	for _, s := range resp.Sessions {
		if s.ID == "ops" {
			restarted = s
			break
		}
	}
	if restarted.TaskID == "" || restarted.TaskID == secondTaskID {
		t.Fatalf("restarted.TaskID = %q, want a new task ID != %q", restarted.TaskID, secondTaskID)
	}
	if !slices.Contains(canceller.cancelled, secondTaskID) {
		t.Fatalf("canceller.cancelled = %v, want %s cancelled", canceller.cancelled, secondTaskID)
	}
	thirdTaskID := restarted.TaskID
	comments3, err := store.Comments(ctx, thirdTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var foundHandover bool
	for _, c := range comments3 {
		if c.Handover && strings.Contains(c.Body, "Standing summary from second conversation.") {
			foundHandover = true
		}
	}
	if !foundHandover {
		t.Fatalf("restart with carryHandover=true did not carry handover onto %s: %+v", thirdTaskID, comments3)
	}
}

func TestCancelConsoleSessionWhileRunningAndQueued(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	canceller := &fakeTaskCanceller{}
	client.Config.TaskCanceller = canceller
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		Capabilities:  ui.OfferedCapabilities(),
		Secrets:       secrets.New(t.TempDir()),
		TaskCanceller: canceller,
	}, store)

	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "ops",
		Title: "Ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	var taskID string
	for _, s := range sessions {
		if s.ID == "ops" {
			taskID = s.TaskID
			break
		}
	}

	// Queue a turn by posting a comment (un-parks the task).
	if err := client.AddComment(ctx, taskID, "please check the logs", nil); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state after comment = %q, want %q", state, model.StateQueued)
	}

	// Cancel while queued.
	rec := do(t, srv, http.MethodPost, "/api/console-sessions/ops/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/console-sessions/ops/cancel (queued) status = %d: %s", rec.Code, rec.Body)
	}
	state, err = store.State(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state after cancel (queued) = %q, want %q", state, model.StateAwaitingReply)
	}

	// Start a run and cancel while running.
	if err := client.AddComment(ctx, taskID, "run a check", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        taskID + "-1",
		TaskID:    taskID,
		Attempt:   1,
		StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodPost, "/api/console-sessions/ops/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/console-sessions/ops/cancel (running) status = %d: %s", rec.Code, rec.Body)
	}
	if !slices.Contains(canceller.cancelled, taskID) {
		t.Fatalf("canceller.cancelled = %v, want %s", canceller.cancelled, taskID)
	}
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("observation after cancel while running = %+v, want PendingQuestionCommentID set", obs)
	}
}

func TestDeleteConsoleSessionClosesTaskAndHandlesDirector(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	canceller := &fakeTaskCanceller{}
	client.Config.TaskCanceller = canceller

	// Seed director task first via RestartConsoleSession.
	sessions, err := client.RestartConsoleSession(ctx, model.DirectorSessionID, ui.RestartConsoleSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TaskID == "" {
		t.Fatalf("director session after restart = %+v, want a task", sessions)
	}
	directorTaskID := sessions[0].TaskID

	// Add a comment and handover on the director task, and start a run.
	now := time.Now().UTC()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    directorTaskID,
		Author:    model.Attribution{Actor: model.ConsoleSessionPrincipal},
		Body:      "Old director handover.",
		CreatedAt: now,
		Handover:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        directorTaskID + "-1",
		TaskID:    directorTaskID,
		Attempt:   1,
		StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// Delete the director session.
	if err := client.DeleteConsoleSession(ctx, model.DirectorSessionID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(canceller.cancelled, directorTaskID) {
		t.Fatalf("deleting running director did not cancel task %s", directorTaskID)
	}
	obs, err := store.GetObservation(ctx, directorTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.ClosedAt == nil {
		t.Fatalf("deleting director did not close task %s: obs = %+v", directorTaskID, obs)
	}

	// Listing console sessions after deleting director must return empty, not resurrect director.
	afterDelete, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("after deleting director, ListConsoleSessions = %+v, want empty", afterDelete)
	}

	// Recreating director starts a fresh task without old conversation or handover.
	recreated, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    model.DirectorSessionID,
		Title: "Director",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recreated) != 1 || recreated[0].TaskID == "" || recreated[0].TaskID == directorTaskID {
		t.Fatalf("recreated director = %+v, want a fresh taskId != %q", recreated, directorTaskID)
	}
	comments, err := store.Comments(ctx, recreated[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if c.Handover || strings.Contains(c.Body, "Old director handover.") {
			t.Fatalf("recreated director inherited old handover: %+v", comments)
		}
	}
}

func TestASessionsModelIsStoredAndCarriedOntoItsTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)

	// Chosen at creation: stored on the session and on the task filed
	// for its conversation, so the very first exchange runs on it.
	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Title: "Reviewer",
		Model: " claude-opus-4 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	created := sessions[len(sessions)-1]
	if created.ID != "reviewer" || created.Model != "claude-opus-4" {
		t.Fatalf("created session = %+v, want id reviewer on model claude-opus-4", created)
	}
	taskID, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.Model != "claude-opus-4" {
		t.Fatalf("the session's task = %+v, want Model claude-opus-4", task)
	}

	// Changed afterwards: stored, and "" puts it back on the default.
	next := "gemini-3-pro"
	sessions, err = client.UpdateConsoleSession(ctx, "reviewer", ui.UpdateConsoleSessionRequest{Model: &next})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions[len(sessions)-1].Model; got != next {
		t.Fatalf("after the update the session's model = %q, want %q", got, next)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := model.ConsoleSessionByID(cfg.ConsoleSessions, "reviewer")
	if !ok || stored.Model != next {
		t.Fatalf("grain_config holds %+v, want Model %q", cfg.ConsoleSessions, next)
	}
	cleared := ""
	sessions, err = client.UpdateConsoleSession(ctx, "reviewer", ui.UpdateConsoleSessionRequest{Model: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions[len(sessions)-1].Model; got != "" {
		t.Fatalf("after clearing the session's model = %q, want the team default (\"\")", got)
	}
}

func TestASessionsModelMayNotNameAConnection(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{{
		ID:        "work-claude",
		Framework: model.AgentFrameworkClaude,
		Models:    []string{"claude-opus-4"},
		Team:      model.DefaultTeamID,
	}}}); err != nil {
		t.Fatal(err)
	}
	pin := "work-claude"
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID,
		ui.UpdateConsoleSessionRequest{Model: &pin}); err == nil {
		t.Fatal("a session's model naming a connection was accepted")
	}
	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Title: "Pinned", Model: pin,
	}); err == nil {
		t.Fatal("creating a session whose model names a connection was accepted")
	}
}

func TestActiveConsoleSessionEditableWhenStateRepoConfigured(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{
		TargetRepos: []string{"acme/widgets", "acme/docs"},
		AgentBackends: []model.AgentBackend{{
			ID:        "work-claude",
			Framework: model.AgentFrameworkClaude,
			Models:    []string{"claude-opus-4", "claude-sonnet-4"},
			Team:      model.DefaultTeamID,
		}},
		ConsoleSessions: []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug"},
			NetworkAccess: model.NetworkOpen,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	taskID, err := model.FileConsoleSessionTask(ctx, store, model.ConsoleSession{
		ID:            model.DirectorSessionID,
		Title:         "Director",
		Capabilities:  []string{"self-debug"},
		NetworkAccess: model.NetworkOpen,
	}, model.FileConsoleSessionOptions{
		StateRepo:       model.RepoRef{Owner: "acme", Name: "grain-config"},
		NetworkIsolates: true,
		Now:             now,
	})
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeStateRepo{status: ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git", Branch: "main"}}
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
		StateRepo:    fake,
		StateRepoRef: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-config"} },
	}, store)

	// 1. Edit active session via PATCH /api/tasks/{id} (including dangerous scopes, reads, writes, model, network_access).
	patchBody := `{
		"capabilities": ["self-debug", "api:tasks:admin", "api:release-manager", "https://grain.dev/capabilities/host-script#host-script={\"script\":\"cluster-autoscaler\"}"],
		"reads": ["acme/docs"],
		"writes": [{"repo": "acme/widgets", "base": "main"}],
		"model": "claude-opus-4",
		"network_access": "none",
		"priority": true,
		"keep_running": true
	}`
	rec := do(t, srv, http.MethodPatch, "/api/tasks/"+taskID, patchBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/%s status = %d, want 200: %s", taskID, rec.Code, rec.Body)
	}

	// 2. GET /api/console-sessions reflects the active task's updated parameters and capabilities.
	rec = do(t, srv, http.MethodGet, "/api/console-sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/console-sessions status = %d, want 200: %s", rec.Code, rec.Body)
	}
	listed := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if len(listed.Sessions) != 1 {
		t.Fatalf("len(listed.Sessions) = %d, want 1", len(listed.Sessions))
	}
	dir := listed.Sessions[0]
	for _, wantCap := range []string{"self-debug", "api:tasks:admin", "api:release-manager"} {
		if !slices.Contains(dir.Capabilities, wantCap) {
			t.Errorf("GET /api/console-sessions Capabilities = %v, missing %q", dir.Capabilities, wantCap)
		}
	}
	if dir.Model != "claude-opus-4" {
		t.Errorf("GET /api/console-sessions Model = %q, want claude-opus-4", dir.Model)
	}
	if dir.NetworkAccess != "none" {
		t.Errorf("GET /api/console-sessions NetworkAccess = %q, want none", dir.NetworkAccess)
	}
	if !slices.Contains(dir.Reads, "acme/docs") {
		t.Errorf("GET /api/console-sessions Reads = %v, want acme/docs", dir.Reads)
	}
	if len(dir.Writes) != 1 || dir.Writes[0].Repo != "acme/widgets" || dir.Writes[0].Base != "main" {
		t.Errorf("GET /api/console-sessions Writes = %+v, want [{acme/widgets main}]", dir.Writes)
	}

	// 3. Edit active session via PUT /api/console-sessions/director directly when StateRepo is configured.
	putBody := `{
		"capabilities": ["self-debug", "self-repair", "api:tasks:admin"],
		"model": "claude-sonnet-4",
		"networkAccess": "open",
		"tasksApproved": true
	}`
	rec = do(t, srv, http.MethodPut, "/api/console-sessions/director", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/console-sessions/director status = %d, want 200: %s", rec.Code, rec.Body)
	}
	putResp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	if len(putResp.Sessions) != 1 {
		t.Fatalf("len(putResp.Sessions) = %d, want 1", len(putResp.Sessions))
	}
	if putResp.Sessions[0].Model != "claude-sonnet-4" || putResp.Sessions[0].NetworkAccess != "open" || !putResp.Sessions[0].TasksApproved {
		t.Errorf("PUT /api/console-sessions/director response = %+v", putResp.Sessions[0])
	}
	if !slices.Contains(putResp.Sessions[0].Capabilities, "self-repair") || !slices.Contains(putResp.Sessions[0].Capabilities, "api:tasks:admin") {
		t.Errorf("PUT /api/console-sessions/director Capabilities = %v", putResp.Sessions[0].Capabilities)
	}
}
