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
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestADeploymentThatHasNeverChosenGetsTheDirector(t *testing.T) {
	sessions := model.ConsoleSessionsOr(nil)
	if len(sessions) != 1 || sessions[0].ID != model.DirectorSessionID {
		t.Fatalf("ConsoleSessionsOr(nil) = %v, want exactly the director", sessions)
	}
	director := sessions[0]
	if director.TasksApproved {
		t.Error("the director files approved tasks by default: a chat is a fast way to ask for a lot of work")
	}
	if director.NetworkAccess != model.NetworkOpen {
		t.Errorf("NetworkAccess = %q, want open: the shipped host sandbox cannot enforce none",
			director.NetworkAccess)
	}
	if slices.ContainsFunc(director.Capabilities, model.IsGrainHostExecGrant) {
		t.Error("the director ships holding self-repair, which can break the deployment it is talking about")
	}
}

// The distinction the whole setting rests on: an unset list is not an
// empty one, so a deployment that removed its sessions does not grow the
// director back on the next tick.
func TestAnEmptyListOfSessionsIsNotAnUnsetOne(t *testing.T) {
	if got := model.ConsoleSessionsOr([]model.ConsoleSession{}); len(got) != 0 {
		t.Fatalf("ConsoleSessionsOr([]) = %v, want none -- an empty list is a deployment choosing none", got)
	}

	unset, err := model.EncodeConsoleSessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	none, err := model.EncodeConsoleSessions([]model.ConsoleSession{})
	if err != nil {
		t.Fatal(err)
	}
	if unset == none {
		t.Fatalf("unset and empty both encode as %q, so the two cannot be told apart in the column", unset)
	}

	back, err := model.DecodeConsoleSessions(unset)
	if err != nil {
		t.Fatal(err)
	}
	if back != nil {
		t.Errorf("an unset column decodes as %v, want nil", back)
	}
	back, err = model.DecodeConsoleSessions(none)
	if err != nil {
		t.Fatal(err)
	}
	if back == nil || len(back) != 0 {
		t.Errorf("an explicit empty list decodes as %v, want an empty non-nil slice", back)
	}
}

func TestConsoleSessionsRoundTripThroughTheColumn(t *testing.T) {
	sessions := []model.ConsoleSession{{
		ID:              "director",
		Title:           "Director",
		Capabilities:    []string{"self-debug"},
		NetworkAccess:   model.NetworkNone,
		TasksApproved:   true,
		GoogleChatSpace: "spaces/AAAAAAAAAAA",
	}}
	stored, err := model.EncodeConsoleSessions(sessions)
	if err != nil {
		t.Fatal(err)
	}
	back, err := model.DecodeConsoleSessions(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(sessions, back, func(a, b model.ConsoleSession) bool {
		return a.ID == b.ID && a.Title == b.Title && a.NetworkAccess == b.NetworkAccess &&
			a.TasksApproved == b.TasksApproved && a.GoogleChatSpace == b.GoogleChatSpace &&
			slices.Equal(a.Capabilities, b.Capabilities)
	}) {
		t.Errorf("round trip gave %v, want %v", back, sessions)
	}
}

// A column that does not parse is an error rather than a silent fall
// back to the default -- the default would file a director a deployment
// may have deliberately removed.
func TestAMalformedConsoleSessionsColumnIsAnError(t *testing.T) {
	if _, err := model.DecodeConsoleSessions("{not json"); err == nil {
		t.Fatal("a malformed column decoded without complaint")
	}
}

func TestValidateConsoleSessionsRefusesWhatCannotWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions []model.ConsoleSession
	}{
		{"no id", []model.ConsoleSession{{Title: "Director"}}},
		{"two of the same id", []model.ConsoleSession{{ID: "a"}, {ID: "a"}}},
		{"an access grain cannot build", []model.ConsoleSession{{ID: "a", NetworkAccess: "sometimes"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := model.ValidateConsoleSessions(tc.sessions); err == nil {
				t.Fatalf("ValidateConsoleSessions(%v) accepted it", tc.sessions)
			}
		})
	}
	if err := model.ValidateConsoleSessions(model.DefaultConsoleSessions()); err != nil {
		t.Fatalf("the shipped default does not validate: %v", err)
	}
}

func TestASessionsTaskIsFoundByItsTag(t *testing.T) {
	tag := model.ConsoleSessionTag("director")
	if id, ok := model.ConsoleSessionIDFromTag(tag); !ok || id != "director" {
		t.Fatalf("ConsoleSessionIDFromTag(%q) = %q, %v; want the session id back", tag, id, ok)
	}
	if _, ok := model.ConsoleSessionIDFromTag("upgrade-check:main"); ok {
		t.Error("a tag that names no session was read as one")
	}
	task := model.Task{Tags: []string{"something-else", tag}}
	if id, ok := model.ConsoleSessionIDOf(task); !ok || id != "director" {
		t.Errorf("ConsoleSessionIDOf = %q, %v; want the director", id, ok)
	}
	if _, ok := model.ConsoleSessionIDOf(model.Task{}); ok {
		t.Error("an ordinary task was read as holding a session's conversation")
	}
}

// What the director is told depends on one thing a deployment chooses,
// and it has to say which way that setting is set: an agent that thinks
// its proposals are queued when they are waiting tells the person in
// front of it the wrong thing about what it just did.
//
// Which is what happened (grain/task-311): the director was told its
// tasks were filed approved, every one of them was filed `proposed`, and
// a run changed what it filed on the strength of the sentence. So this
// asserts the sentence against the predicate rather than against the
// setting -- ConsoleSession.QueuesProposals is what both filing gates
// are written from, and a claim derived from anything else is a claim
// that can outlive the permission behind it.
func TestTheDirectorIsToldWhetherItsTasksWait(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "grain-home"}
	for _, session := range []model.ConsoleSession{
		{ID: model.DirectorSessionID},
		{ID: model.DirectorSessionID, TasksApproved: true},
	} {
		body := model.DirectorSessionBody(repo, session)
		claimsQueued := strings.Contains(body, "filed approved")
		claimsWaits := strings.Contains(body, "waits for a person")
		if claimsQueued == claimsWaits {
			t.Fatalf("TasksApproved=%v: the body says its tasks are queued (%v) and that they "+
				"wait (%v) -- it has to say exactly one", session.TasksApproved, claimsQueued, claimsWaits)
		}
		if claimsQueued != session.QueuesProposals() {
			t.Errorf("TasksApproved=%v: the body claims queued=%v while the predicate both "+
				"filing gates ask says %v", session.TasksApproved, claimsQueued, session.QueuesProposals())
		}
		if !strings.Contains(body, repo.String()) {
			t.Error("the body does not name the state repository the session can read")
		}
	}
	if strings.Contains(model.DirectorSessionBody(model.RepoRef{}, model.ConsoleSession{}), "read access to") {
		t.Error("a deployment with no state repository is still told it can read one")
	}
}

func TestDirectorSessionBodyInstructsRelativeUILinks(t *testing.T) {
	body := model.DirectorSessionBody(model.RepoRef{Owner: "acme", Name: "grain-home"}, model.ConsoleSession{ID: model.DirectorSessionID})
	for _, want := range []string{
		"/tasks/<id>",
		"[Task #31](/tasks/31)",
		"[Settings → CI](/settings/ci)",
		"http://127.0.0.1:...",
		"http://localhost:...",
		"UberProxy",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("DirectorSessionBody missing %q:\n%s", want, body)
		}
	}
}

// A session still titled by the heuristic is told to name itself through
// rename_session -- once, early, with a short overview bounded by the
// same limit the heuristic title is cut to -- and that the name is final.
// The director is told none of this: its name is the deployment's.
func TestDirectorSessionBodyAsksAChatToNameItself(t *testing.T) {
	chat := model.ConsoleSession{ID: "hi-can-you-look", Title: "Hi, can you look at why the nightly",
		TitleSource: model.TitleSourceHeuristic}
	body := model.DirectorSessionBody(model.RepoRef{}, chat)
	for _, want := range []string{
		"**Name the conversation.**",
		"`rename_session`",
		"once",
		"Early in this exchange",
		"at most " + strconv.Itoa(model.ConsoleSessionTitleMaxLength) + " characters",
		"refuses any further call",
		`"Hi, can you look at why the nightly"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("a chat session's body is missing %q:\n%s", want, body)
		}
	}
	// No clause invites a retitle when the latest turn changes topic:
	// that is how sessions came to be renamed on every run.
	if strings.Contains(body, "unless the subject") {
		t.Errorf("a chat session's body still invites a rename when the subject changes:\n%s", body)
	}

	director := model.DirectorSessionBody(model.RepoRef{}, model.ConsoleSession{ID: model.DirectorSessionID, Title: "Director",
		TitleSource: model.TitleSourceHeuristic})
	if strings.Contains(director, "rename_session") {
		t.Errorf("the director is told to rename itself:\n%s", director)
	}
}

// Once an agent or a person has named the session, every later run's
// prompt leaves the naming paragraph out -- and so does one whose source
// nobody resolved, so the paragraph is never there by default.
func TestDirectorSessionBodyOmitsNamingOnceNamed(t *testing.T) {
	for _, source := range []model.ConsoleSessionTitleSource{model.TitleSourceAgent, model.TitleSourcePerson, ""} {
		chat := model.ConsoleSession{ID: "nightly", Title: "Nightly CI failure", TitleSource: source}
		body := model.DirectorSessionBody(model.RepoRef{}, chat)
		if strings.Contains(body, "rename_session") || strings.Contains(body, "Name the conversation") {
			t.Errorf("source %q: body still asks the agent to name the session:\n%s", source, body)
		}
	}
}

// A session saved before titles recorded their source is heuristic only
// if its title is exactly what its first message gives; anything else is
// read as a person's, so a name somebody chose is never overwritten.
func TestResolvedTitleSourceInfersLegacySessions(t *testing.T) {
	first := "hi, can you look at why the nightly is red"
	for _, tc := range []struct {
		name    string
		session model.ConsoleSession
		first   string
		want    model.ConsoleSessionTitleSource
	}{
		{"titled from its first message", model.ConsoleSession{ID: "hi", Title: "Hi, can you look at why the nightly"}, first, model.TitleSourceHeuristic},
		{"retitled since", model.ConsoleSession{ID: "hi", Title: "Nightly CI failure"}, first, model.TitleSourcePerson},
		{"named on the form", model.ConsoleSession{ID: "release", Title: "Release coordinator"}, "", model.TitleSourcePerson},
		{"the director", model.ConsoleSession{ID: model.DirectorSessionID, Title: model.ConsoleSessionTitleFromMessage("")}, "", model.TitleSourcePerson},
		{"recorded wins", model.ConsoleSession{ID: "hi", Title: "Hi, can you look at why the nightly", TitleSource: model.TitleSourceAgent}, first, model.TitleSourceAgent},
	} {
		if got := tc.session.ResolvedTitleSource(tc.first); got != tc.want {
			t.Errorf("%s: ResolvedTitleSource = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The first message is the first thing a person said, not grain's or an
// agent's comment.
func TestConsoleSessionFirstMessageIsAPersons(t *testing.T) {
	comments := []model.Comment{
		{Body: "grain note", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}},
		{Body: "hi there", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}},
		{Body: "later", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}},
	}
	if got := model.ConsoleSessionFirstMessage(comments); got != "hi there" {
		t.Errorf("ConsoleSessionFirstMessage = %q, want the first person's comment", got)
	}
	if got := model.ConsoleSessionFirstMessage(nil); got != "" {
		t.Errorf("ConsoleSessionFirstMessage(nil) = %q, want empty", got)
	}
}

func TestValidateConsoleSessionsRefusesUnknownTitleSource(t *testing.T) {
	if err := model.ValidateConsoleSessions([]model.ConsoleSession{{ID: "x", TitleSource: "robot"}}); err == nil {
		t.Error("a title source grain does not know was accepted")
	}
	if err := model.ValidateConsoleSessions([]model.ConsoleSession{{ID: "x", TitleSource: model.TitleSourceAgent}}); err != nil {
		t.Errorf("agent title source refused: %v", err)
	}
}

// The gate's own half of that: the question a filing path has in hand is
// about the task holding the conversation, not about the settings row,
// and it resolves to the same predicate the prompt was written from.
//
// Both gates ask it -- ui.Client.CreateTask for a proposal filed while
// the session's run is still going, which is how a session's proposals
// are actually filed, and orchestrator.relayProposedTasks for one
// replayed at the end of it.
func TestWhoseProposalsASessionsPermissionCovers(t *testing.T) {
	sessions := []model.ConsoleSession{
		{ID: model.DirectorSessionID, TasksApproved: true},
		{ID: "reviewer"},
	}
	tagged := func(id string) model.Task {
		return model.Task{Tags: []string{"something-else", model.ConsoleSessionTag(id)}}
	}
	for _, tc := range []struct {
		name string
		task model.Task
		want bool
	}{
		{"the session this deployment trusts", tagged(model.DirectorSessionID), true},
		{"a session it does not", tagged("reviewer"), false},
		{"a session it no longer configures at all", tagged("retired"), false},
		{"an ordinary task", model.Task{}, false},
	} {
		if got := model.ConsoleSessionQueuesProposals(sessions, tc.task); got != tc.want {
			t.Errorf("%s: ConsoleSessionQueuesProposals = %v, want %v", tc.name, got, tc.want)
		}
	}
	// And nothing is queued by a deployment that has configured no
	// sessions, whatever a task is tagged with.
	if model.ConsoleSessionQueuesProposals(nil, tagged(model.DirectorSessionID)) {
		t.Error("a task tagged for a session this deployment does not define had its proposals queued")
	}
}

func TestIsConsoleSessionMatchesDirectorTaskSQL(t *testing.T) {
	valid := model.Task{
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
	if !valid.IsConsoleSession() {
		t.Fatal("canonical console session task reported IsConsoleSession() = false")
	}
	for _, tc := range []struct {
		name string
		task model.Task
	}{
		{
			name: "ordinary task",
			task: model.Task{},
		},
		{
			name: "missing ReasonConsoleSession",
			task: model.Task{
				Interactive: true,
				Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
			},
		},
		{
			name: "not interactive",
			task: model.Task{
				Origin: model.Origin{Reason: model.ReasonConsoleSession},
				Tags:   []string{model.ConsoleSessionTag(model.DirectorSessionID)},
			},
		},
		{
			name: "missing console-session tag",
			task: model.Task{
				Origin:      model.Origin{Reason: model.ReasonConsoleSession},
				Interactive: true,
			},
		},
	} {
		if tc.task.IsConsoleSession() {
			t.Errorf("%s: IsConsoleSession() = true, want false", tc.name)
		}
	}
}
