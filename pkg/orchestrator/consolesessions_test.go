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

// The standing console sessions (grain/task-415), reached only through
// the exported RunCycle -- this package's tests never call an unexported
// reconciler directly. A cycle here needs nothing beyond a store, for
// upgradecheck_test.go's own reason: with no ready task and no pull
// request link, dispatch and sync both no-op.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// runSessions runs one cycle against a store, as a deployment with a
// state repository -- which is what a session is given read access to.
func runSessions(t *testing.T, store *model.Store, now time.Time) {
	t.Helper()
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			StateRepo: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatalf("cycle: %v", err)
	}
}

// sessionTasks is every task filed to hold a standing session's
// conversation, whichever session it belongs to.
func sessionTasks(t *testing.T, store *model.Store) []model.Task {
	t.Helper()
	all, err := store.ListTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if task.Origin.Reason == model.ReasonConsoleSession {
			out = append(out, task)
		}
	}
	return out
}

func TestTheDirectorIsFiledOnTheFirstCycleAndParkedOnItsOwnOpening(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	runSessions(t, store, baseTime)

	tasks := sessionTasks(t, store)
	if len(tasks) != 1 {
		t.Fatalf("filed %d session tasks, want exactly the director", len(tasks))
	}
	director := tasks[0]

	if !slices.Contains(director.Tags, model.ConsoleSessionTag(model.DirectorSessionID)) {
		t.Errorf("Tags = %v, want the director's own session tag -- it is how the task is found again",
			director.Tags)
	}
	if !director.Interactive {
		t.Error("the director is not Interactive: it is a chat, and that flag is what draws it as one")
	}
	if director.Approval == nil {
		t.Error("the director is not approved: a session somebody has to approve is not there when they need it")
	}
	if director.Target != nil {
		t.Errorf("Target = %v, want none: a session answers in words and files tasks", director.Target)
	}
	if len(director.Reads) != 1 || director.Reads[0].Name != "grain-home" {
		t.Errorf("Reads = %v, want the state repository", director.Reads)
	}
	if director.NetworkAccess != model.NetworkOpen {
		t.Errorf("NetworkAccess = %q, want open by default", director.NetworkAccess)
	}
	var grants []string
	for _, g := range director.Grants {
		grants = append(grants, g.Capability)
	}
	slices.Sort(grants)
	if !slices.Equal(grants, []string{"api:deployment:read", "bootstrap-playbooks", "durable", model.GrainHealthGrant, model.GrainJournalGrant, "self-debug"}) {
		t.Errorf("Grants = %v, want the shipped default set plus durable", grants)
	}
	goal, err := store.TaskGoal(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal == nil || goal.Kind != model.GoalPersonCloses {
		t.Errorf("Goal = %+v, want %q", goal, model.GoalPersonCloses)
	}

	// Parked, not queued: a session dispatched the moment it is filed is
	// an agent answering a conversation nobody has started.
	state, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Errorf("state = %q, want %q -- a fresh session waits for the person, it does not run",
			state, model.StateAwaitingReply)
	}
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("the director opens with %d comments, want exactly the one it is parked on", len(comments))
	}
}

func TestASecondCycleDoesNotFileTheDirectorAgain(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)

	runSessions(t, store, baseTime)
	runSessions(t, store, baseTime.Add(time.Minute))

	if tasks := sessionTasks(t, store); len(tasks) != 1 {
		t.Fatalf("filed %d session tasks over two cycles, want exactly 1 -- the tag is what stops the second",
			len(tasks))
	}
}

func TestClosingASessionFilesAFreshConversation(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	first := sessionTasks(t, store)[0]
	// A restored or hand-edited state repository can leave task times out
	// of order. The open conversation is still the session's attachment
	// point; chronology must not make a closed one win.
	future := baseTime.Add(24 * time.Hour)
	first.CreatedAt = &future
	if err := store.PutTask(ctx, first); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }
	if err := client.Close(ctx, first.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}

	runSessions(t, store, baseTime.Add(2*time.Minute))
	tasks := sessionTasks(t, store)
	if len(tasks) != 2 {
		t.Fatalf("filed %d session tasks, want the closed conversation and its replacement", len(tasks))
	}
	var second model.Task
	for _, task := range tasks {
		if task.ID != first.ID {
			second = task
		}
	}
	if second.ID == "" {
		t.Fatal("the closed conversation was reused")
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if id != second.ID {
		t.Fatalf("session attaches to task %s after close, want replacement %s", id, second.ID)
	}
	replyToSession(t, store, second.ID, baseTime.Add(3*time.Minute))
	state, err := store.State(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("replacement state after reply = %q, want %q", state, model.StateQueued)
	}
}

// closeSession ends a session's conversation the way a person does --
// ui.Client.Close, which is what writes Observation.ClosedAt and what
// the reconciler reads as "somebody ended this".
func closeSession(t *testing.T, store *model.Store, taskID string, now time.Time) {
	t.Helper()
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return now }
	if err := client.Close(t.Context(), taskID, ui.CloseOptions{}); err != nil {
		t.Fatalf("closing the session: %v", err)
	}
}

// sayInSession puts one message into a session's conversation, as
// whoever said it. The store rather than ui.Client because a test needs
// to write an agent's handover, and nothing a person can click writes
// one.
func sayInSession(t *testing.T, store *model.Store, taskID string, actor model.Principal,
	body string, handover bool, now time.Time) {

	t.Helper()
	if _, err := store.AddComment(t.Context(), model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: actor},
		Body:      body,
		CreatedAt: now,
		Handover:  handover,
	}); err != nil {
		t.Fatalf("saying %q in the session: %v", body, err)
	}
}

// replacementOf is the session's task after a close -- the one that is
// not the closed one.
func replacementOf(t *testing.T, store *model.Store, closed string) model.Task {
	t.Helper()
	for _, task := range sessionTasks(t, store) {
		if task.ID != closed {
			return task
		}
	}
	t.Fatalf("no replacement was filed for the closed session task %s", closed)
	return model.Task{}
}

// The one thing a fresh conversation carries over: the standing summary
// the last one's runs wrote. Everything else stays on the closed task,
// which is the whole of the bargain -- carrying more would be carrying
// exactly what the conversation bound exists to stop carrying
// (docs/implementation-notes.md, "A chat that never ends is not split
// for you").
func TestAClosedSessionsReplacementCarriesItsHandoverAndNothingElse(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	first := sessionTasks(t, store)[0]

	answered := model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}
	said := baseTime.Add(time.Minute)
	replyToSession(t, store, first.ID, said)
	sayInSession(t, store, first.ID, answered, "it is running four tasks.", false, said.Add(time.Second))
	sayInSession(t, store, first.ID, answered, "an older summary, since superseded", true, said.Add(2*time.Second))
	sayInSession(t, store, first.ID, answered, "the granule host is the one to watch", true, said.Add(3*time.Second))

	closeSession(t, store, first.ID, said.Add(time.Minute))
	runSessions(t, store, said.Add(2*time.Minute))
	second := replacementOf(t, store, first.ID)

	comments, err := store.Comments(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("the replacement opens with %d comments, want the carried handover and the opening line: %v",
			len(comments), comments)
	}
	carried := comments[0]
	if !strings.Contains(carried.Body, "the granule host is the one to watch") {
		t.Errorf("carried comment = %q, want the most recent handover of the closed conversation", carried.Body)
	}
	for _, left := range []string{"an older summary", "it is running four tasks", "what is this deployment doing?"} {
		if strings.Contains(carried.Body, left) {
			t.Errorf("carried comment = %q, want nothing of the old thread but its last handover -- %q came too",
				carried.Body, left)
		}
	}
	if !strings.Contains(carried.Body, first.ID) {
		t.Errorf("carried comment = %q, want it to name task %s, the chat it came from", carried.Body, first.ID)
	}
	if !carried.Handover {
		t.Error("the carried summary is not a handover on the new task: the one message written to survive " +
			"a cut has to go on being carried outside the head/tail budget")
	}
	if carried.Author.Actor != model.ConsoleSessionPrincipal {
		t.Errorf("carried by %v, want %v -- the new task's runs did not write it, and saying they did "+
			"puts a lie in the record", carried.Author.Actor, model.ConsoleSessionPrincipal)
	}
	if !strings.Contains(comments[1].Body, "I'm here") {
		t.Errorf("the replacement's last comment is %q, want its own opening line -- the handover comes "+
			"before the invitation to say something, and the invitation is what it is parked on",
			comments[1].Body)
	}

	// Provenance, and provenance only. A replacement that depended on
	// the conversation it replaces would never be dispatched.
	if len(second.Links) != 1 {
		t.Fatalf("Links = %v, want exactly the link back to the closed conversation", second.Links)
	}
	link := second.Links[0]
	if link.Kind != model.LinkContinues || link.Target != first.ID {
		t.Errorf("Links = %v, want %s -> %s", second.Links, model.LinkContinues, first.ID)
	}
	if link.Kind.Blocks() {
		t.Errorf("%s blocks dispatch: a session that cannot be dispatched is a session that is dead the "+
			"moment somebody splits it", link.Kind)
	}
	replyToSession(t, store, second.ID, said.Add(3*time.Minute))
	state, err := store.State(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("replacement state after reply = %q, want %q", state, model.StateQueued)
	}
}

// A session filed for the first time has no previous conversation, and
// is filed exactly as it always was: its opening line, and no link to
// anything.
func TestAFirstSessionCarriesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	runSessions(t, store, baseTime)

	director := sessionTasks(t, store)[0]
	if len(director.Links) != 0 {
		t.Errorf("Links = %v, want none -- this conversation continues nothing", director.Links)
	}
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("a first session opens with %d comments, want only the line it is parked on: %v",
			len(comments), comments)
	}
}

// Most runs never write a handover, and a session whose last
// conversation did not is refiled exactly as it was before any of this
// existed: nothing carried, and nothing said about the absence.
func TestASessionWhoseLastRunWroteNoHandoverIsRefiledAsItIs(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	first := sessionTasks(t, store)[0]

	said := baseTime.Add(time.Minute)
	replyToSession(t, store, first.ID, said)
	sayInSession(t, store, first.ID, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"},
		"four tasks, all green.", false, said.Add(time.Second))

	closeSession(t, store, first.ID, said.Add(time.Minute))
	runSessions(t, store, said.Add(2*time.Minute))
	second := replacementOf(t, store, first.ID)

	comments, err := store.Comments(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("the replacement opens with %d comments, want only its own opening line: %v",
			len(comments), comments)
	}
}

func TestAFinishedExchangeLeavesTheDirectorTalkableTo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)
	started := said.Add(time.Second)
	runID := director.ID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: director.ID, Attempt: 1, StartedAt: started,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, director.ID, lastComment(t, store, director.ID).ID, started); err != nil {
		t.Fatal(err)
	}
	done := started.Add(time.Minute)
	if err := store.FinishRun(ctx, runID, done, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.ProcessResult(ctx, store, client, director, &agent.Result{FinalText: "all green."}, runID, done); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	runSessions(t, store, done.Add(time.Minute))

	state, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state = %q after an exchange, want %q -- a completed session is a chat nobody can answer",
			state, model.StateAwaitingReply)
	}
	susps, err := store.Suspensions(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(susps) != 1 || susps[0].Kind != model.SuspendRequested || susps[0].ResumeOn != (model.ResumeOn{Message: true}).JSON() {
		t.Fatalf("Suspensions = %+v, want one requested suspension with resume_on {message: true}", susps)
	}
	if tasks := sessionTasks(t, store); len(tasks) != 1 {
		t.Errorf("filed %d session tasks, want the same one back -- an exchange does not start a new conversation",
			len(tasks))
	}
}

// grain does not ask its own session for an isolation this deployment
// cannot provide. The director ships asking for no network at all, and a
// host-directory backend refuses such a run outright -- which on a
// deployment installed without granule made every exchange fail in setup,
// in the one pane with no run rows to see it in.
func TestASessionOnHostSandboxesAsksForTheNetworkItCanHave(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	director := sessionTasks(t, store)[0]
	if director.NetworkAccess != model.NetworkOpen {
		t.Fatalf("NetworkAccess = %q, want %q on a backend that cannot withhold a network it does not "+
			"have -- otherwise every exchange fails acquiring its sandbox",
			director.NetworkAccess, model.NetworkOpen)
	}
}

// The whole point of a standing session: somebody says something and an
// agent is dispatched to answer it.
//
// Replying is what un-parks the task (ui.Client.AddComment clears the
// pending question), and this reconciler runs before dispatch in the
// same cycle -- so a cycle that parked every un-parked session would
// re-park the reply before dispatch ever saw it ready, and the director
// would answer "_Waiting for you._" to everything forever
// (grain/task-399).
func TestAReplyToASessionIsLeftForDispatchRatherThanParkedAgain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)

	runSessions(t, store, said.Add(time.Second))

	state, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q after somebody replied, want %q -- a message nobody has been dispatched for "+
			"is what a session exists to answer", state, model.StateQueued)
	}
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := comments[len(comments)-1]; last.Author.Actor.Kind != model.PrincipalHuman {
		t.Errorf("grain answered the reply itself with %q -- the agent is what answers a session", last.Body)
	}
}

// The other half of the same decision: a run that ended without
// completing the task leaves it un-parked too, and that one must be
// parked, or the session is dispatched again and again over a
// conversation nobody has added to.
func TestASessionWhoseRunEndedWithoutAnsweringIsParked(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)

	// The run that reply was dispatched for, ended the way a cancelled or
	// failed one does: no answer, and the task left neither parked nor
	// complete.
	started := said.Add(time.Second)
	if err := store.StartRun(ctx, model.Run{
		ID: director.ID + "-1", TaskID: director.ID, Attempt: 1, StartedAt: started,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(time.Minute)
	if err := store.FinishRun(ctx, director.ID+"-1", finished, "failed", "the sandbox died"); err != nil {
		t.Fatal(err)
	}

	runSessions(t, store, finished.Add(time.Second))

	state, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state = %q after a run that answered nothing, want %q -- otherwise the session is "+
			"dispatched for ever over the same message", state, model.StateAwaitingReply)
	}
	// And it says so. The chat draws no run rows, so grain's own park
	// line is the only place a person can learn that what they asked was
	// never answered, rather than answered with nothing.
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := comments[len(comments)-1].Body
	if !strings.Contains(last, "the sandbox died") {
		t.Errorf("grain parked the session on %q, want the failed run's own reason -- otherwise a person "+
			"who was never answered is told they are the one being waited for", last)
	}
}

// replyToSession says something to a session's task as a person does, through
// pkg/ui.Client -- the same path the chat pane's send button and `grain
// comment` both take, un-parking included.
func replyToSession(t *testing.T, store *model.Store, taskID string, now time.Time) {
	t.Helper()
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return now }
	if err := client.AddComment(t.Context(), taskID, "what is this deployment doing?", nil); err != nil {
		t.Fatalf("replying to the session: %v", err)
	}
}

func TestChangingASessionsCapabilitiesReachesItsTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug", "self-repair"},
			NetworkAccess: model.NetworkOpen,
			TasksApproved: true,
		}}
	})
	runSessions(t, store, baseTime.Add(time.Minute))

	updated, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var grants []string
	for _, g := range updated.Grants {
		grants = append(grants, g.Capability)
	}
	slices.Sort(grants)
	if !slices.Equal(grants, []string{"durable", "self-debug", "self-repair"}) {
		t.Errorf("Grants = %v, want what the settings now say plus durable", grants)
	}
	if updated.NetworkAccess != model.NetworkOpen {
		t.Errorf("NetworkAccess = %q, want the configured one", updated.NetworkAccess)
	}
	if !strings.Contains(updated.Body, "filed approved") {
		t.Error("the body still tells the session its tasks wait for approval, after the setting said otherwise")
	}
}

func TestChangingASessionsRepoGrantsReachesItsTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug"},
			NetworkAccess: model.NetworkOpen,
			Reads:         []model.RepoRef{{Owner: "acme", Name: "custom-read"}},
			Writes:        []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "custom-write"}, Base: "main"}},
		}}
	})
	runSessions(t, store, baseTime.Add(time.Minute))

	updated, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var readNames []string
	for _, r := range updated.Reads {
		readNames = append(readNames, r.String())
	}
	slices.Sort(readNames)
	if !slices.Equal(readNames, []string{"acme/custom-read", "acme/grain-home"}) {
		t.Errorf("Reads = %v, want state repo and custom-read", readNames)
	}
	if len(updated.Writes) != 1 || updated.Writes[0].Repo.String() != "acme/custom-write" || updated.Writes[0].Base != "main" {
		t.Errorf("Writes = %v, want custom-write", updated.Writes)
	}
}

func TestADeploymentThatConfiguresNoSessionsGetsNone(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)

	// An empty list is a deployment saying it wants none -- as opposed to
	// an unset setting, which is a deployment that has never chosen and
	// gets the director.
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{}
	})
	runSessions(t, store, baseTime)

	if tasks := sessionTasks(t, store); len(tasks) != 0 {
		t.Fatalf("filed %d session tasks for a deployment that asked for none", len(tasks))
	}
}

// What a session proposes waits for a person, like every other
// proposal -- unless this deployment has deliberately said otherwise
// about that session, in the pane the chat is in.
func TestWhatASessionProposesWaitsUnlessTheDeploymentSaysOtherwise(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		approved bool
		caps     []any
		want     model.State
	}{
		{"the default", false, nil, model.StateProposed},
		{"a deployment that trusts its director", true, nil, model.StateQueued},
		{"asking for a capability the session holds", true, []any{"self-debug"}, model.StateQueued},
		// grain#2409: the trust is in what the session may queue, not in
		// what it may grant -- a capability it never held waits for a person.
		{"asking for a capability the session lacks", true, []any{"self-repair"}, model.StateProposed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			if tc.approved {
				settings(t, store, func(cfg *model.Config) {
					sessions := model.DefaultConsoleSessions()
					sessions[0].TasksApproved = true
					cfg.ConsoleSessions = sessions
				})
			}
			runSessions(t, store, baseTime)
			director := sessionTasks(t, store)[0]

			args := map[string]any{
				"title": "widen the target repos", "body": "as we just discussed",
			}
			if tc.caps != nil {
				args["capabilities"] = tc.caps
			}
			result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: args})
			if err := orchestrator.ProcessResult(ctx, store, client, director, result,
				director.ID+"-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}

			all, err := store.ListTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var proposal *model.Task
			for i := range all {
				if all[i].ID != director.ID {
					proposal = &all[i]
				}
			}
			if proposal == nil {
				t.Fatal("the director proposed a task and nothing was filed")
			}
			state, err := store.State(ctx, proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state != tc.want {
				t.Fatalf("the proposal reads %q, want %q", state, tc.want)
			}
			noted := strings.Contains(proposal.Body, "`self-repair`")
			if want := slices.Contains(tc.caps, any("self-repair")); noted != want {
				t.Errorf("escalation note in body = %v, want %v: %q", noted, want, proposal.Body)
			}
		})
	}
}

func TestChangingASessionsGoogleChatSpaceReachesItsTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:              model.DirectorSessionID,
			Title:           "Director",
			Capabilities:    []string{"self-debug"},
			NetworkAccess:   model.NetworkOpen,
			GoogleChatSpace: "spaces/AAAAAAAAAAA",
		}}
	})
	runSessions(t, store, baseTime.Add(time.Minute))

	updated, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var chatLink *model.Link
	for _, l := range updated.Links {
		if l.Kind == model.LinkGoogleChat {
			chatLink = &l
			break
		}
	}
	if chatLink == nil || chatLink.Target != "spaces/AAAAAAAAAAA" {
		t.Fatalf("Links = %v, want LinkGoogleChat with target spaces/AAAAAAAAAAA", updated.Links)
	}
}

// TestConsoleSessionsUnderLargeTaskVolume verifies that reconcileConsoleSessions
// stays fast when there are 500+ tasks in the store.
func TestConsoleSessionsUnderLargeTaskVolume(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("backlog-%04d", i)
		tk := model.Task{
			ID:          id,
			Intent:      model.IntentImplement,
			Title:       fmt.Sprintf("Task %d", i),
			Tags:        []string{fmt.Sprintf("tag-%d", i%10)},
			Interactive: false,
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask %d: %v", i, err)
		}
	}

	// First cycle files director session.
	start := time.Now()
	runSessions(t, store, baseTime)
	dur1 := time.Since(start)

	tasks := sessionTasks(t, store)
	if len(tasks) != 1 {
		t.Fatalf("session tasks = %d, want 1", len(tasks))
	}

	// Subsequent cycle reconciles existing director session.
	start = time.Now()
	runSessions(t, store, baseTime.Add(time.Second))
	dur2 := time.Since(start)

	t.Logf("reconcileConsoleSessions under 500 tasks: cycle 1=%v, cycle 2=%v", dur1, dur2)
}

// sessionExchange is one run of a session from the reply that started it
// to the run's end, as the store records it: a person's message, the run
// dispatched for it (which records its prompt's reach, as RunDispatch
// does), and whatever is said while it is live. The run then ends the
// way an answered exchange ends -- succeeded and the task completed --
// and a cycle decides what the session does next.
type sessionExchange struct {
	t        *testing.T
	store    *model.Store
	director model.Task
	runID    string
	at       time.Time
}

func startSessionExchange(t *testing.T) *sessionExchange {
	t.Helper()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)
	started := said.Add(time.Second)
	runID := director.ID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: director.ID, Attempt: 1, StartedAt: started,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, director.ID, lastComment(t, store, director.ID).ID, started); err != nil {
		t.Fatal(err)
	}
	return &sessionExchange{t: t, store: store, director: director, runID: runID, at: started}
}

// say puts a comment into the conversation while the run is live, and
// returns its id.
func (e *sessionExchange) say(actor model.Attribution, body string) int64 {
	e.t.Helper()
	e.at = e.at.Add(time.Minute)
	id, err := e.store.AddComment(e.t.Context(), model.Comment{
		TaskID: e.director.ID, Author: actor, Body: body, CreatedAt: e.at,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// delivered is the run's mcpserver reporting it has handed over
// everything up to id, as an addendum or a chat reply.
func (e *sessionExchange) delivered(id int64) {
	e.t.Helper()
	if err := e.store.AdvanceDelivered(e.t.Context(), e.director.ID, id, e.at); err != nil {
		e.t.Fatal(err)
	}
}

// end finishes the run as answered, with outcome and detail, and runs the
// cycle after it.
func (e *sessionExchange) end(outcome, detail string) model.State {
	e.t.Helper()
	ctx := e.t.Context()
	finished := e.at.Add(time.Minute)
	if err := e.store.FinishRun(ctx, e.runID, finished, outcome, detail); err != nil {
		e.t.Fatal(err)
	}
	runSessions(e.t, e.store, finished.Add(time.Second))
	state, err := e.store.State(ctx, e.director.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return state
}

func lastComment(t *testing.T, store *model.Store, taskID string) model.Comment {
	t.Helper()
	comments, err := store.Comments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return comments[len(comments)-1]
}

var (
	aliceSays = model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	agentSays = model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "agent"},
	}
)

// A message posted while the run was finishing -- writing its last
// answer, or after its chat call had been abandoned -- reached no tool
// call, so the run never saw it. It must be dispatched for, not counted
// as answered because the run happened to still be alive when it was
// posted (grain/task-1774).
func TestAMessageTheRunNeverSawIsDispatchedForWhenItEnds(t *testing.T) {
	t.Parallel()
	e := startSessionExchange(t)
	e.say(agentSays, "here is what I found")
	unseen := e.say(aliceSays, "and one more thing")

	if state := e.end("succeeded", "the run made 3 tool call(s)"); state != model.StateQueued {
		t.Fatalf("state = %q, want %q -- the message posted before the run ended was never shown to it",
			state, model.StateQueued)
	}
	if last := lastComment(t, e.store, e.director.ID); last.ID != unseen {
		t.Errorf("grain answered the unseen message itself with %q -- it is the agent's to answer", last.Body)
	}
}

// The other side: a message the run *was* handed, as an addendum or as
// its chat call's reply, has been answered, and is not dispatched for a
// second time -- which is what put one message in front of two runs
// (grain/task-1774's second symptom).
func TestAMessageTheRunWasHandedIsNotDispatchedAgain(t *testing.T) {
	t.Parallel()
	e := startSessionExchange(t)
	e.say(agentSays, "which pane should this go in?")
	reply := e.say(aliceSays, "a new one")
	e.delivered(reply)
	e.say(agentSays, "done -- it is in a new pane")

	if state := e.end("succeeded", "the run made 3 tool call(s)"); state != model.StateAwaitingReply {
		t.Fatalf("state = %q, want %q -- the reply was delivered to the run that answered it",
			state, model.StateAwaitingReply)
	}
	if last := lastComment(t, e.store, e.director.ID).Body; last != "_Waiting for you._" {
		t.Errorf("session parked on %q, want grain's ordinary park line", last)
	}
}

// What the agent says, and what grain says about it, are never something
// to dispatch a run for -- or a session would answer itself for ever.
func TestOnlyAPersonsMessageDispatchesASession(t *testing.T) {
	t.Parallel()
	e := startSessionExchange(t)
	e.say(agentSays, "here is what I found")
	e.say(model.Attribution{Actor: model.ConsoleSessionPrincipal}, "_a note of grain's own_")

	if state := e.end("succeeded", "the run made 1 tool call(s)"); state != model.StateAwaitingReply {
		t.Fatalf("state = %q, want %q -- nobody but the session itself said anything",
			state, model.StateAwaitingReply)
	}
	// And the park line that follows is no reason to dispatch either.
	runSessions(t, e.store, e.at.Add(time.Hour))
	if state, err := e.store.State(t.Context(), e.director.ID); err != nil || state != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v) a cycle later, want it still %q", state, err, model.StateAwaitingReply)
	}
}

// A run that answered in plain text, calling no tool, answered: the
// session parks on its ordinary line rather than telling the person under
// a real answer that it "did not get an answer" (grain/task-1774's third
// symptom).
func TestASessionAnsweredInTextParksWithoutAFailureNote(t *testing.T) {
	t.Parallel()
	e := startSessionExchange(t)
	e.say(agentSays, "the answer, in plain text")

	if state := e.end("succeeded", "the agent answered in text without calling a tool"); state != model.StateAwaitingReply {
		t.Fatalf("state = %q, want %q", state, model.StateAwaitingReply)
	}
	last := lastComment(t, e.store, e.director.ID).Body
	if strings.Contains(last, "did not get an answer") || last != "_Waiting for you._" {
		t.Errorf("session parked on %q, want grain's ordinary park line", last)
	}
}

// Consecutive calls to parkConsoleSession with the same resting note
// must reuse the existing comment ID and avoid creating duplicate
// consecutive resting comments.
func TestConsecutiveParkingDoesNotDuplicateRestingComment(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	commentsBefore, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}

	// First cycle parked director on opening.
	// Now park on consoleSessionResting.
	now := baseTime.Add(time.Minute)
	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{}}
	if err := orchestrator.ParkConsoleSession(ctx, deps, director.ID, orchestrator.ConsoleSessionResting, now); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != len(commentsBefore)+1 {
		t.Fatalf("expected 1 new comment, got %d comments total", len(comments))
	}
	parkedID := comments[len(comments)-1].ID
	obs, err := store.GetObservation(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != parkedID {
		t.Fatalf("pending question = %v, want %d", obs.PendingQuestionCommentID, parkedID)
	}

	// AdvanceDelivered mark should remain intact across consecutive parking.
	if err := store.AdvanceDelivered(ctx, director.ID, parkedID, now); err != nil {
		t.Fatal(err)
	}

	// Repeated parking with the same resting note should reuse the comment and avoid duplicates.
	if err := orchestrator.ParkConsoleSession(ctx, deps, director.ID, orchestrator.ConsoleSessionResting, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	commentsAfter, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commentsAfter) != len(comments) {
		t.Fatalf("expected comment count to remain %d, got %d (duplicate resting comment created)", len(comments), len(commentsAfter))
	}
	obsAfter, err := store.GetObservation(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obsAfter == nil || obsAfter.PendingQuestionCommentID == nil || *obsAfter.PendingQuestionCommentID != parkedID {
		t.Fatalf("pending question after consecutive park = %v, want %d", obsAfter.PendingQuestionCommentID, parkedID)
	}
	if obsAfter.DeliveredCommentID == nil || *obsAfter.DeliveredCommentID != parkedID {
		t.Fatalf("delivered mark after consecutive park = %v, want %d", obsAfter.DeliveredCommentID, parkedID)
	}
}

// textAnswer is a session's agent that answers every run in plain text,
// as consolerestserver's scripted one does.
type textAnswer string

func (a textAnswer) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	return &agent.Result{FinalText: string(a), Transcript: string(a)}, nil
}

// A cycle's clock is read when it begins, and a person can post in the
// moment after: the cycle still reads their message, dispatches a run
// for it, and relays the run's answer. That answer was stamped with the
// cycle's start -- before the message it answers -- and the chat, which
// sorts by time, drew it above that message (grain/task-3782; the
// console resting-notes walkthrough's "Scripted answer 1." over "Human
// reply one").
func TestASessionsAnswerIsStampedAfterTheReplyItAnswers(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	// No capabilities, as consolerestserver's own session has none: this
	// store has no provider for the ones a session holds by default.
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	began := baseTime.Add(time.Minute)
	said := began.Add(time.Second)
	replyToSession(t, store, director.ID, said)

	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(textAnswer("the answer")),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, began); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reply, answer *model.Comment
	for i := range comments {
		switch c := &comments[i]; {
		case c.Author.Actor.Kind == model.PrincipalHuman:
			reply = c
		case c.Body == "the answer":
			answer = c
		}
	}
	if reply == nil || answer == nil {
		t.Fatalf("reply = %v, answer = %v in %+v; want both -- the reply should have dispatched a run",
			reply, answer, comments)
	}
	if answer.CreatedAt.Before(reply.CreatedAt) {
		t.Errorf("answer stamped %v, before the reply it answers (%v): the chat draws it above that reply",
			answer.CreatedAt, reply.CreatedAt)
	}
	if answer.ID < reply.ID {
		t.Errorf("answer %d was written before the reply %d it answers", answer.ID, reply.ID)
	}
}

func TestRemovingASessionClosesItsTaskAndRecreatingStartsFresh(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	first := sessionTasks(t, store)[0]

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    first.ID,
		Author:    agentSays,
		Body:      "Summary from before the session was deleted.",
		CreatedAt: baseTime.Add(time.Minute),
		Handover:  true,
	}); err != nil {
		t.Fatal(err)
	}

	// Remove all sessions (empty non-nil slice).
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{}
	})
	runSessions(t, store, baseTime.Add(2*time.Minute))

	state, err := store.State(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Fatalf("removed session's task state = %q, want %q", state, model.StateClosed)
	}

	// Re-add the director session; it must file a fresh task without carrying over the deleted session's handover.
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = model.DefaultConsoleSessions()
	})
	runSessions(t, store, baseTime.Add(3*time.Minute))

	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || id == first.ID {
		t.Fatalf("recreated session task id = %q, want a new task != %q", id, first.ID)
	}
	replacement, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(replacement.Links) != 0 {
		t.Errorf("recreated session task links = %v, want none", replacement.Links)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if c.Handover || strings.Contains(c.Body, "Summary from before the session was deleted.") {
			t.Errorf("recreated session task carried over deleted session's handover: %+v", comments)
		}
	}
}

func TestACancelledSessionTurnStaysParkedUntilTheNextMessage(t *testing.T) {
	t.Parallel()
	e := startSessionExchange(t)
	ctx := t.Context()

	// User cancels the turn while it is running/queued: a "Turn stopped by user"
	// comment is posted, PendingQuestionCommentID points at it, and DeliveredCommentID
	// is advanced to it.
	stoppedAt := e.at.Add(time.Minute)
	stopCommentID, err := e.store.AddComment(ctx, model.Comment{
		TaskID:    e.director.ID,
		Author:    aliceSays,
		Body:      "Turn stopped by user",
		CreatedAt: stoppedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.ObserveField(ctx, e.director.ID, stoppedAt, func(o *model.Observation) {
		o.PendingQuestionCommentID = &stopCommentID
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.AdvanceDelivered(ctx, e.director.ID, stopCommentID, stoppedAt); err != nil {
		t.Fatal(err)
	}
	if err := e.store.FinishRun(ctx, e.runID, stoppedAt, "cancelled", "cancelled by user"); err != nil {
		t.Fatal(err)
	}

	runSessions(t, e.store, stoppedAt.Add(time.Second))
	state, err := e.store.State(ctx, e.director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state after cancel = %q, want %q", state, model.StateAwaitingReply)
	}
}

func TestChangingASessionsModelReachesItsTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	if director.Model != "" {
		t.Fatalf("a session nobody chose a model for filed its task on %q, want the team default", director.Model)
	}

	session := func(chosen string) {
		settings(t, store, func(cfg *model.Config) {
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID:            model.DirectorSessionID,
				Title:         "Director",
				Capabilities:  []string{"self-debug"},
				NetworkAccess: model.NetworkOpen,
				Model:         chosen,
			}}
		})
	}
	session("claude-opus-4")
	runSessions(t, store, baseTime.Add(time.Minute))
	updated, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Model != "claude-opus-4" {
		t.Fatalf("Model = %q, want claude-opus-4", updated.Model)
	}

	session("")
	runSessions(t, store, baseTime.Add(2*time.Minute))
	updated, err = store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Model != "" {
		t.Fatalf("Model = %q after the session went back to the default, want \"\"", updated.Model)
	}
}

func TestASessionStartedFromItsFirstMessageIsLeftForDispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }

	listed, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Message: "why is the merge queue holding?",
	})
	if err != nil {
		t.Fatalf("CreateConsoleSession: %v", err)
	}
	var taskID string
	for _, s := range listed {
		if s.ID == "why-is-the-merge-queue-holding" {
			taskID = s.TaskID
		}
	}
	if taskID == "" {
		t.Fatalf("created session has no taskId in %+v", listed)
	}

	runSessions(t, store, baseTime.Add(time.Minute+time.Second))

	state, err := store.State(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q after reconcileConsoleSessions, want %q -- a session started from its first message must not be re-parked before dispatch",
			state, model.StateQueued)
	}
}

// A session saved before titles recorded who chose them is asked to name
// itself only while its title is still what its first message gives, and
// once an agent has named it no later run's prompt asks again.
func TestASessionIsAskedToNameItselfOnlyWhileTitledByItsFirstMessage(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }
	listed, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Message: "why is the merge queue holding?",
	})
	if err != nil {
		t.Fatalf("CreateConsoleSession: %v", err)
	}
	const id = "why-is-the-merge-queue-holding"
	var taskID string
	for _, s := range listed {
		if s.ID == id {
			taskID = s.TaskID
		}
	}
	if taskID == "" {
		t.Fatalf("created session has no taskId in %+v", listed)
	}
	body := func(when time.Duration) string {
		t.Helper()
		runSessions(t, store, baseTime.Add(when))
		task, err := store.GetTask(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		return task.Body
	}
	legacy := func(title string) {
		t.Helper()
		cfg, err := store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range cfg.ConsoleSessions {
			if cfg.ConsoleSessions[i].ID == id {
				cfg.ConsoleSessions[i].Title = title
				cfg.ConsoleSessions[i].TitleSource = ""
			}
		}
		if err := store.PutConfig(ctx, *cfg); err != nil {
			t.Fatal(err)
		}
	}

	if !strings.Contains(body(2*time.Minute), "rename_session") {
		t.Error("a session titled from its first message is not asked to name itself")
	}
	legacy("Why is the merge queue holding")
	if !strings.Contains(body(3*time.Minute), "rename_session") {
		t.Error("a legacy session still titled from its first message is not asked to name itself")
	}
	legacy("Merge queue stall")
	if strings.Contains(body(4*time.Minute), "rename_session") {
		t.Error("a legacy session with a title its first message would not give is asked to rename itself")
	}

	legacy("Why is the merge queue holding")
	if _, err := client.RenameConsoleSessionOfTask(ctx, taskID, "Merge queue hold"); err != nil {
		t.Fatalf("RenameConsoleSessionOfTask: %v", err)
	}
	if b := body(5 * time.Minute); strings.Contains(b, "rename_session") || !strings.Contains(b, "director") {
		t.Errorf("after the agent named the session its prompt still asks for a name:\n%s", b)
	}
}

type countingSandboxes struct {
	inner    orchestrator.Sandboxes
	acquires int
	releases int
}

type countingSandbox struct {
	orchestrator.Sandbox
	parent *countingSandboxes
}

func (s *countingSandbox) Release(ctx context.Context) error {
	s.parent.releases++
	return s.Sandbox.Release(ctx)
}

func (s *countingSandbox) Root() (string, error) {
	if r, ok := s.Sandbox.(interface{ Root() (string, error) }); ok {
		return r.Root()
	}
	return "", nil
}

func (c *countingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	c.acquires++
	sbx, err := c.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	return &countingSandbox{Sandbox: sbx, parent: c}, nil
}

type recordingSessionAgent struct {
	configs []agent.RunConfig
	onTurn  func(turn int, cfg agent.RunConfig) (*agent.Result, error)
}

func (a *recordingSessionAgent) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	a.configs = append(a.configs, cfg)
	turn := len(a.configs)
	if a.onTurn != nil {
		return a.onTurn(turn, cfg)
	}
	return &agent.Result{
		FinalText:        fmt.Sprintf("answer %d", turn),
		Transcript:       fmt.Sprintf("answer %d", turn),
		ResumableSession: "sess-token-1",
	}, nil
}

func TestSessionSandboxReusedWithinIdleTTLFallbackAndReclaimedAfterTTL(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			marker := filepath.Join(cfg.SandboxRoot, "marker.txt")
			if turn == 1 {
				if err := os.WriteFile(marker, []byte("kept-across-turns"), 0644); err != nil {
					t.Errorf("turn 1 WriteFile: %v", err)
				}
			} else if turn == 2 {
				got, err := os.ReadFile(marker)
				if err != nil || !strings.Contains(string(got), "kept-across-turns") {
					t.Errorf("turn 2 ReadFile(marker.txt) = %q, %v; want kept-across-turns in reused sandbox", string(got), err)
				}
			}
			return &agent.Result{
				FinalText:        fmt.Sprintf("answer %d", turn),
				Transcript:       fmt.Sprintf("answer %d", turn),
				ResumableSession: "sess-1",
			}, nil
		},
	}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	// Turn 1: user sends a message at t=1m; cycle dispatches attempt 1 and parks the session.
	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("turn 1 cycle: %v", err)
	}
	if sbx.acquires != 1 || sbx.releases != 0 {
		t.Fatalf("after turn 1: acquires=%d releases=%d, want 1 acquire and 0 releases (held warm)", sbx.acquires, sbx.releases)
	}
	warmID := orchestrator.WarmSessionSandboxID(store, director.ID)
	if warmID == "" {
		t.Fatal("expected director's sandbox to be held warm after turn 1")
	}
	if !orchestrator.IsIdleSessionSandbox(warmID) {
		t.Fatalf("expected IsIdleSessionSandbox(%q) to be true while parked", warmID)
	}

	// Turn 2: user replies within the 30m TTL (at t=10m); cycle reuses the same sandbox.
	t2 := baseTime.Add(10 * time.Minute)
	replyToSession(t, store, director.ID, t2)
	if err := orchestrator.RunCycle(ctx, deps, t2.Add(time.Second)); err != nil {
		t.Fatalf("turn 2 cycle: %v", err)
	}
	if sbx.acquires != 1 || sbx.releases != 0 {
		t.Fatalf("after turn 2 within TTL: acquires=%d releases=%d, want 1 acquire and 0 releases (reused)", sbx.acquires, sbx.releases)
	}
	if len(ag.configs) != 2 {
		t.Fatalf("agent turns = %d, want 2", len(ag.configs))
	}
	if !ag.configs[1].SandboxReused {
		t.Error("turn 2 RunConfig.SandboxReused = false, want true")
	}
	if !strings.Contains(ag.configs[1].Prompt, "The sandbox is the same one") {
		t.Errorf("turn 2 Prompt missing reused-sandbox orientation:\n%s", ag.configs[1].Prompt)
	}
	if !strings.Contains(ag.configs[1].SuspendedPrompt, "The sandbox is the same one") {
		t.Errorf("turn 2 SuspendedPrompt missing reused-sandbox orientation:\n%s", ag.configs[1].SuspendedPrompt)
	}
	if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != warmID {
		t.Fatalf("WarmSessionSandboxID after turn 2 = %q, want %q", got, warmID)
	}

	// Turn 3: 35 minutes pass with no message (past the 30m TTL), then user replies.
	tExpired := t2.Add(35 * time.Minute)
	if err := orchestrator.RunCycle(ctx, deps, tExpired); err != nil {
		t.Fatalf("expiry cycle: %v", err)
	}
	if sbx.releases != 1 {
		t.Fatalf("after TTL expiry: releases=%d, want 1", sbx.releases)
	}
	if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != "" {
		t.Fatalf("WarmSessionSandboxID after TTL expiry = %q, want empty", got)
	}

	t3 := tExpired.Add(time.Minute)
	replyToSession(t, store, director.ID, t3)
	if err := orchestrator.RunCycle(ctx, deps, t3.Add(time.Second)); err != nil {
		t.Fatalf("turn 3 cycle: %v", err)
	}
	if sbx.acquires != 2 {
		t.Fatalf("after turn 3 past TTL: acquires=%d, want 2 (new sandbox acquired)", sbx.acquires)
	}
	if ag.configs[2].SandboxReused {
		t.Error("turn 3 RunConfig.SandboxReused = true, want false after TTL reclaim")
	}
	if !strings.Contains(ag.configs[2].Prompt, "The sandbox is not the same one") {
		t.Errorf("turn 3 Prompt missing reclaimed-sandbox orientation:\n%s", ag.configs[2].Prompt)
	}
}

func TestClosingOrPausingSessionReleasesWarmSandbox(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"pause", "close"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			settings(t, store, func(cfg *model.Config) {
				cfg.SessionSandboxIdleTTL = 30 * time.Minute
				cfg.ConsoleSessions = []model.ConsoleSession{{
					ID: model.DirectorSessionID, Title: "Director",
					Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
				}}
			})
			runSessions(t, store, baseTime)
			director := sessionTasks(t, store)[0]

			sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
			deps := orchestrator.Deps{
				Store:      store,
				Sandboxes:  sbx,
				Framework:  orchestrator.StaticFramework(textAnswer("ready")),
				MaxWorkers: 2,
				Config: orchestrator.Config{
					StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
					Capabilities:          model.NewCapabilityRegistry(durable.New()),
					SessionSandboxIdleTTL: 30 * time.Minute,
				},
			}

			t1 := baseTime.Add(time.Minute)
			replyToSession(t, store, director.ID, t1)
			if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
				t.Fatalf("turn 1 cycle: %v", err)
			}
			if orchestrator.WarmSessionSandboxID(store, director.ID) == "" {
				t.Fatal("expected warm sandbox after turn 1")
			}

			tAction := t1.Add(2 * time.Minute)
			if mode == "pause" {
				if _, err := store.Pause(ctx, model.TaskPause{
					TaskID:    director.ID,
					PausedAt:  tAction,
					PausedBy:  aliceSays.Actor,
					FromState: model.StateAwaitingReply,
					Immediate: true,
				}); err != nil {
					t.Fatalf("Pause: %v", err)
				}
			} else {
				closeSession(t, store, director.ID, tAction)
			}

			if err := orchestrator.RunCycle(ctx, deps, tAction.Add(time.Second)); err != nil {
				t.Fatalf("post-%s cycle: %v", mode, err)
			}
			if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != "" {
				t.Fatalf("WarmSessionSandboxID after %s = %q, want empty", mode, got)
			}
			if sbx.releases != 1 {
				t.Fatalf("releases after %s = %d, want 1", mode, sbx.releases)
			}
		})
	}
}

func TestSessionWarmSandboxCapacityEvictionEvictsOldestIdleFirst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{
			{ID: "session-a", Title: "Session A", Capabilities: []string{}, NetworkAccess: model.NetworkOpen},
			{ID: "session-b", Title: "Session B", Capabilities: []string{}, NetworkAccess: model.NetworkOpen},
			{ID: "session-c", Title: "Session C", Capabilities: []string{}, NetworkAccess: model.NetworkOpen},
		}
	})
	runSessions(t, store, baseTime)
	sessions := sessionTasks(t, store)
	if len(sessions) != 3 {
		t.Fatalf("filed %d sessions, want 3", len(sessions))
	}
	var sessA, sessB, sessC model.Task
	for _, s := range sessions {
		switch {
		case slices.Contains(s.Tags, model.ConsoleSessionTag("session-a")):
			sessA = s
		case slices.Contains(s.Tags, model.ConsoleSessionTag("session-b")):
			sessB = s
		default:
			sessC = s
		}
	}

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	ledger := &model.Ledger{}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(textAnswer("session reply")),
		Ledger:     ledger,
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 2048, DiskGB: 10},
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	// Park session A at t=1m (oldest idle).
	tA := baseTime.Add(time.Minute)
	replyToSession(t, store, sessA.ID, tA)
	if err := orchestrator.RunCycle(ctx, deps, tA.Add(time.Second)); err != nil {
		t.Fatalf("session A cycle: %v", err)
	}
	// Park session B at t=2m (newer idle).
	tB := baseTime.Add(2 * time.Minute)
	replyToSession(t, store, sessB.ID, tB)
	if err := orchestrator.RunCycle(ctx, deps, tB.Add(time.Second)); err != nil {
		t.Fatalf("session B cycle: %v", err)
	}

	if got := orchestrator.WarmSessionSandboxCount(store); got != 2 {
		t.Fatalf("WarmSessionSandboxCount = %d, want 2", got)
	}
	if got := ledger.IdleCount(model.PoolAgent); got != 2 {
		t.Fatalf("ledger.IdleCount = %d, want 2 (held session sandboxes counted against capacity)", got)
	}

	// Reply to session C at t=3m while MaxWorkers=2 is full of the two warm session sandboxes.
	tC := baseTime.Add(3 * time.Minute)
	replyToSession(t, store, sessC.ID, tC)
	if err := orchestrator.RunCycle(ctx, deps, tC.Add(time.Second)); err != nil {
		t.Fatalf("capacity eviction cycle: %v", err)
	}

	// Session A (the oldest idle session sandbox) must be evicted first, while Session B stays warm!
	if got := orchestrator.WarmSessionSandboxID(store, sessA.ID); got != "" {
		t.Fatalf("oldest idle session A sandbox = %q, want evicted (empty)", got)
	}
	if got := orchestrator.WarmSessionSandboxID(store, sessB.ID); got == "" {
		t.Fatal("newer idle session B sandbox was evicted, want kept warm")
	}
}

func TestInProcessSessionContinuationKeepsRunAliveAcrossTurns(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	var seenPrompts []string
	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			seenPrompts = append(seenPrompts, cfg.ContinuePrompt)
			if turn == 1 {
				// While the run is about to enter its in-process idle wait after turn 1,
				// post the human's second message shortly after.
				go func() {
					time.Sleep(80 * time.Millisecond)
					// A message from another task's run arrives first: it does
					// not wake the session, but the turn a person's message
					// wakes is told it too, as not a human's.
					_, _ = store.AddComment(ctx, model.Comment{
						TaskID:    director.ID,
						Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "other-task"}},
						Body:      "a note from another task",
						CreatedAt: baseTime.Add(2 * time.Minute),
					})
					_, _ = store.AddComment(ctx, model.Comment{
						TaskID:    director.ID,
						Author:    aliceSays,
						Body:      "follow-up question in the same run",
						CreatedAt: baseTime.Add(2 * time.Minute),
					})
				}()
			}
			return &agent.Result{
				FinalText:        fmt.Sprintf("in-process answer %d", turn),
				Transcript:       fmt.Sprintf("in-process answer %d", turn),
				ResumableSession: "sess-live-1",
			}, nil
		},
	}

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 350 * time.Millisecond,
			WaitForSessionReply:   true,
			CancelPollInterval:    20 * time.Millisecond,
		},
	}

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("in-process cycle: %v", err)
	}

	// Both turns must have executed inside a single dispatch (1 sandbox acquire, 1 run row, 2 framework.Run calls).
	if len(ag.configs) != 2 {
		t.Fatalf("framework.Run calls = %d, want 2 (turn 1 + in-process continuation)", len(ag.configs))
	}
	for _, want := range []string{
		"the run of another task (task other-task), not a human",
		"a note from another task",
		"a human (alice)",
		"follow-up question in the same run",
	} {
		if !strings.Contains(seenPrompts[1], want) {
			t.Fatalf("turn 2 ContinuePrompt = %q, want it to contain %q", seenPrompts[1], want)
		}
	}
	if strings.Contains(seenPrompts[1], "in-process answer 1") {
		t.Fatalf("turn 2 ContinuePrompt = %q, want the run's own relayed answer left out", seenPrompts[1])
	}
	if ag.configs[1].ResumeSession != "sess-live-1" {
		t.Fatalf("turn 2 ResumeSession = %q, want sess-live-1", ag.configs[1].ResumeSession)
	}
	runs, err := store.Runs(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("recorded runs = %d, want 1 (both turns served by the same live attempt)", len(runs))
	}
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var gotFirst, gotSecond bool
	for _, c := range comments {
		if c.Body == "in-process answer 1" {
			gotFirst = true
		}
		if c.Body == "in-process answer 2" {
			gotSecond = true
		}
	}
	if !gotFirst || !gotSecond {
		t.Fatalf("comments = %+v, want both in-process answers relayed", comments)
	}
}

type delayedAcquireSandboxes struct {
	inner        orchestrator.Sandboxes
	acquireDelay time.Duration
}

func (d *delayedAcquireSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	if d.acquireDelay > 0 {
		time.Sleep(d.acquireDelay)
	}
	return d.inner.Acquire(ctx, name, shape)
}

func TestMeasureSessionTurnLatencyBeforeAndAfter(t *testing.T) {
	t.Parallel()
	measure := func(name string, idleTTL time.Duration, inProcess bool, acquireDelay time.Duration) time.Duration {
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = idleTTL
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		sbx := &delayedAcquireSandboxes{
			inner:        orchestrator.NewHostSandboxes(t.TempDir()),
			acquireDelay: acquireDelay,
		}
		var messagePostedAt time.Time
		var firstToolCallAt time.Time
		ag := &recordingSessionAgent{
			onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
				if turn == 2 {
					// Simulate the agent's first tool call on turn 2.
					if len(cfg.Tools) > 0 {
						_ = cfg.Tools[0].Handler(ctx, map[string]any{"path": "."})
					}
					firstToolCallAt = time.Now()
				}
				if turn == 1 && inProcess {
					go func() {
						time.Sleep(30 * time.Millisecond)
						messagePostedAt = time.Now()
						_, _ = store.AddComment(ctx, model.Comment{
							TaskID:    director.ID,
							Author:    aliceSays,
							Body:      "second turn message",
							CreatedAt: baseTime.Add(2 * time.Minute),
						})
					}()
				}
				return &agent.Result{
					FinalText:        fmt.Sprintf("answer %d", turn),
					Transcript:       fmt.Sprintf("answer %d", turn),
					ResumableSession: "sess-measure-1",
				}, nil
			},
		}
		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  sbx,
			Framework:  orchestrator.StaticFramework(ag),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New()),
				SessionSandboxIdleTTL: idleTTL,
				WaitForSessionReply:   inProcess,
				CancelPollInterval:    5 * time.Millisecond,
			},
		}

		t1 := baseTime.Add(time.Minute)
		replyToSession(t, store, director.ID, t1)
		if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
			t.Fatalf("%s turn 1: %v", name, err)
		}
		if !inProcess {
			t2 := baseTime.Add(2 * time.Minute)
			messagePostedAt = time.Now()
			replyToSession(t, store, director.ID, t2)
			if err := orchestrator.RunCycle(ctx, deps, t2.Add(time.Second)); err != nil {
				t.Fatalf("%s turn 2: %v", name, err)
			}
		}
		d := firstToolCallAt.Sub(messagePostedAt)
		t.Logf("%s: time from message to first tool call = %v", name, d)
		return d
	}

	cold := measure("before (cold sandbox acquire + setup)", -1*time.Second, false, 200*time.Millisecond)
	warm := measure("after (warm sandbox reuse)", 30*time.Minute, false, 200*time.Millisecond)
	inProc := measure("after (in-process session continuation)", 350*time.Millisecond, true, 200*time.Millisecond)

	if warm >= cold {
		t.Errorf("warm sandbox reuse (%v) was not faster than cold sandbox acquire (%v)", warm, cold)
	}
	if inProc >= warm {
		t.Errorf("in-process continuation (%v) was not faster than warm dispatch (%v)", inProc, warm)
	}
}

// A warm session sandbox is reused only for a turn that would have been
// given the same sandbox anyway: a task whose sandbox shape changed between
// turns (here, its memory) gets a fresh one built to its new shape, never
// the old one. sessionSandboxKey's own test covers grants and the target.
func TestSessionSandboxNotReusedWhenTheTaskChangedBetweenTurns(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	var shapes []orchestrator.Shape
	recording := &shapeRecordingSandboxes{countingSandboxes: sbx, shapes: &shapes}
	ag := &recordingSessionAgent{}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  recording,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("turn 1 cycle: %v", err)
	}
	if orchestrator.WarmSessionSandboxID(store, director.ID) == "" {
		t.Fatal("expected director's sandbox to be held warm after turn 1")
	}

	if err := store.UpdateTask(ctx, director.ID, func(task *model.Task) error {
		task.SandboxMemoryMB = 4096
		return nil
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	t2 := baseTime.Add(10 * time.Minute)
	replyToSession(t, store, director.ID, t2)
	if err := orchestrator.RunCycle(ctx, deps, t2.Add(time.Second)); err != nil {
		t.Fatalf("turn 2 cycle: %v", err)
	}
	if sbx.acquires != 2 || sbx.releases < 1 {
		t.Fatalf("after a changed turn 2 (%d agent turns): acquires=%d releases=%d, want a second acquire and the warm one released",
			len(ag.configs), sbx.acquires, sbx.releases)
	}
	if len(ag.configs) != 2 || ag.configs[1].SandboxReused {
		t.Fatalf("turn 2 reused the warm sandbox built for a different shape (turns=%d)", len(ag.configs))
	}
	if got := shapes[len(shapes)-1].MemoryMB; got != 4096 {
		t.Errorf("turn 2 sandbox MemoryMB = %d, want 4096", got)
	}
}

type shapeRecordingSandboxes struct {
	*countingSandboxes
	shapes *[]orchestrator.Shape
}

func (s *shapeRecordingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	*s.shapes = append(*s.shapes, shape)
	// Recorded, then built unsized: a host-directory sandbox has no
	// shape of its own to give it.
	shape.CPUs, shape.MemoryMB, shape.DiskGB = 0, 0, 0
	return s.countingSandboxes.Acquire(ctx, name, shape)
}

// A grant changed while a session's sandbox is held warm releases the
// sandbox on the next cycle, before any reply: what the old grants
// placed in it must not sit there until a turn or the TTL ends it.
func TestWarmSessionSandboxReleasedWhenItsGrantsChange(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	ag := &recordingSessionAgent{}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("turn 1 cycle: %v", err)
	}
	if orchestrator.WarmSessionSandboxID(store, director.ID) == "" {
		t.Fatal("expected director's sandbox to be held warm after turn 1")
	}
	releases := sbx.releases

	// Unchanged grants: a cycle keeps it warm.
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Minute)); err != nil {
		t.Fatalf("idle cycle: %v", err)
	}
	if orchestrator.WarmSessionSandboxID(store, director.ID) == "" {
		t.Fatal("an idle cycle with unchanged grants released the warm sandbox")
	}

	if err := store.UpdateTask(ctx, director.ID, func(task *model.Task) error {
		task.Grants = append(task.Grants, model.Grant{Capability: "gcp-key", Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(2*time.Minute)); err != nil {
		t.Fatalf("cycle after the grant change: %v", err)
	}
	if id := orchestrator.WarmSessionSandboxID(store, director.ID); id != "" {
		t.Fatalf("warm sandbox %s still held after the task's grants changed", id)
	}
	if sbx.releases <= releases {
		t.Fatalf("releases = %d, want the warm sandbox released (was %d)", sbx.releases, releases)
	}
}

func TestActiveConsoleSessionOperatorEditsPreservedAcrossReconciles(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.TargetRepos = []string{"acme/widgets", "acme/docs", "acme/infra"}
		cfg.AgentBackends = []model.AgentBackend{{
			ID:        "claude-main",
			Framework: model.AgentFrameworkClaude,
			Models:    []string{"claude-opus-4", "claude-sonnet-4"},
			Team:      model.DefaultTeamID,
		}}
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug"},
			NetworkAccess: model.NetworkOpen,
			TasksApproved: false,
			Reads:         []model.RepoRef{{Owner: "acme", Name: "docs"}},
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	// Configure a UI client where grain_config is managed by a state repo (grain/config).
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		StateRepoRef: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }

	// 1. Operator edits the active console session task via PATCH /api/tasks/{id}
	// even though a state repo (grain/config) is configured.
	hostScriptCap := `https://grain.dev/capabilities/host-script#host-script={"script":"cluster-autoscaler"}`
	caps := []string{"self-debug", "api:tasks:admin", "api:release-manager", hostScriptCap}
	reads := []string{"acme/docs", "acme/widgets"}
	writes := []ui.WriteTarget{{Repo: "acme/widgets", Base: "main"}}
	chosenModel := "claude-opus-4"
	netNone := "none"
	approved := true
	prio := true
	keepRunning := true
	if _, err := client.UpdateTask(ctx, director.ID, ui.UpdateTaskRequest{
		Capabilities:         &caps,
		Reads:                &reads,
		Writes:               &writes,
		Model:                &chosenModel,
		NetworkAccess:        &netNone,
		AutoApproveProposals: &approved,
		PriorityDispatch:     &prio,
		KeepRunning:          &keepRunning,
	}); err != nil {
		t.Fatalf("UpdateTask on active console session with state repo configured: %v", err)
	}

	// 2. Subsequent reconcileConsoleSessions cycles must preserve the operator's changes
	// on the active session task instead of overwriting them back to the config defaults.
	runSessions(t, store, baseTime.Add(2*time.Minute))
	runSessions(t, store, baseTime.Add(3*time.Minute))

	updated, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, wantCap := range append(caps, "priority-dispatch", model.DurableCapability) {
		if !slices.ContainsFunc(updated.Grants, func(g model.Grant) bool { return g.Capability == wantCap }) {
			t.Errorf("active session task Grants = %v after reconcile, missing operator grant %q", updated.Grants, wantCap)
		}
	}
	if !slices.Contains(updated.Reads, model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Errorf("active session task Reads = %v after reconcile, want acme/widgets preserved", updated.Reads)
	}
	if len(updated.Writes) != 1 || updated.Writes[0].Repo != (model.RepoRef{Owner: "acme", Name: "widgets"}) || updated.Writes[0].Base != "main" {
		t.Errorf("active session task Writes = %v after reconcile, want [{acme/widgets main}] preserved", updated.Writes)
	}
	if updated.Model != "claude-opus-4" {
		t.Errorf("active session task Model = %q after reconcile, want claude-opus-4 preserved", updated.Model)
	}
	if updated.NetworkAccess != model.NetworkNone {
		t.Errorf("active session task NetworkAccess = %q after reconcile, want none preserved", updated.NetworkAccess)
	}
	if !model.ConsoleSessionQueuesProposals([]model.ConsoleSession{{ID: model.DirectorSessionID, TasksApproved: false}}, *updated) {
		t.Errorf("ConsoleSessionQueuesProposals = false after reconcile, want true from operator override")
	}

	// 3. When a new additive capability or read repo is added to console_sessions in grain_config,
	// reconcileConsoleSessions merges the additive config default while preserving operator changes.
	settings(t, store, func(cfg *model.Config) {
		cfg.TargetRepos = []string{"acme/widgets", "acme/docs", "acme/infra"}
		cfg.AgentBackends = []model.AgentBackend{{
			ID:        "claude-main",
			Framework: model.AgentFrameworkClaude,
			Models:    []string{"claude-opus-4", "claude-sonnet-4"},
			Team:      model.DefaultTeamID,
		}}
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug", "self-repair"},
			NetworkAccess: model.NetworkOpen,
			TasksApproved: false,
			Reads: []model.RepoRef{
				{Owner: "acme", Name: "docs"},
				{Owner: "acme", Name: "infra"},
			},
		}}
	})
	runSessions(t, store, baseTime.Add(4*time.Minute))

	merged, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, wantCap := range append(caps, "self-repair") {
		if !slices.ContainsFunc(merged.Grants, func(g model.Grant) bool { return g.Capability == wantCap }) {
			t.Errorf("merged Grants = %v, missing %q", merged.Grants, wantCap)
		}
	}
	if !slices.Contains(merged.Reads, model.RepoRef{Owner: "acme", Name: "infra"}) {
		t.Errorf("merged Reads = %v, missing additive config default acme/infra", merged.Reads)
	}
	if !slices.Contains(merged.Reads, model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Errorf("merged Reads = %v, lost operator read acme/widgets", merged.Reads)
	}
	if merged.Model != "claude-opus-4" || merged.NetworkAccess != model.NetworkNone {
		t.Errorf("merged Model=%q NetworkAccess=%q, want claude-opus-4 and none preserved", merged.Model, merged.NetworkAccess)
	}
}

func TestActiveConsoleSessionPermissionChangesApplyImmediatelyToProposals(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.TargetRepos = []string{"acme/widgets"}
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{},
			NetworkAccess: model.NetworkOpen,
			TasksApproved: false,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		StateRepoRef: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
	}, store)

	hostScriptCap := `https://grain.dev/capabilities/host-script#host-script={"script":"cluster-autoscaler"}`
	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			// Mid-run, the operator grants the capability, writes scope, and tasksApproved
			// directly on the active session via PUT /api/console-sessions/director.
			caps := []string{"api:tasks:admin", hostScriptCap}
			writes := []ui.WriteTarget{{Repo: "acme/widgets", Base: "main"}}
			approved := true
			if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID, ui.UpdateConsoleSessionRequest{
				Capabilities:  &caps,
				Writes:        &writes,
				TasksApproved: &approved,
			}); err != nil {
				t.Errorf("mid-run UpdateConsoleSession: %v", err)
			}
			return &agent.Result{
				FinalText:  "filed proposal",
				Transcript: "filed proposal",
				ToolCalls: []agent.ToolCall{{
					Name: "propose_task",
					Arguments: map[string]any{
						"title":        "Scale cluster",
						"body":         "Run cluster-autoscaler.",
						"repo":         "acme/widgets",
						"capabilities": []any{hostScriptCap},
					},
				}},
			}, nil
		},
	}

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	allTasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposed *model.Task
	for i := range allTasks {
		if allTasks[i].Title == "Scale cluster" {
			proposed = &allTasks[i]
			break
		}
	}
	if proposed == nil {
		t.Fatalf("expected proposed task 'Scale cluster' to be filed, got tasks: %+v", allTasks)
	}
	state, err := store.State(ctx, proposed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("proposed task state = %q, want %q (mid-run session permission change should apply immediately to propose_task)", state, model.StateQueued)
	}
}

// TestFirstReplyWakesConsoleSessionRestingWithRequestedSuspension verifies
// that after a session rests via ParkConsoleSessionTask (which sets both
// PendingQuestionCommentID and a requested suspension deferral on {message: true}),
// the very first human reply via ui.Client.AddComment clears both markers so
// the session never requires the operator to send their message twice.
func TestFirstReplyWakesConsoleSessionRestingWithRequestedSuspension(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	var calls int
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			calls++
			return &agent.Result{
				ToolCalls: []agent.ToolCall{{
					Name:      "comment_on_issue",
					Arguments: map[string]any{"comment": fmt.Sprintf("reply-%d", calls)},
				}},
			}, nil
		})),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
	}

	// Exchange 1: reply -> dispatch -> rest (which calls ParkConsoleSessionTask with ConsoleSessionResting).
	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("exchange 1 dispatch cycle: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(2*time.Second)); err != nil {
		t.Fatalf("exchange 1 rest cycle: %v", err)
	}

	// Explicitly call ParkConsoleSessionTask with ConsoleSessionResting to ensure
	// both PendingQuestionCommentID != nil and DeferredAt != nil are active simultaneously.
	if err := model.ParkConsoleSessionTask(ctx, store, director.ID, model.ConsoleSessionResting, t1.Add(3*time.Second)); err != nil {
		t.Fatalf("ParkConsoleSessionTask: %v", err)
	}

	// Now send ONE user reply via ui.Client.AddComment. It must wake the session immediately!
	t2 := t1.Add(10 * time.Second)
	replyToSession(t, store, director.ID, t2)
	st, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state after single reply = %q, want %q (must not stay deferred)", st, model.StateQueued)
	}
	if err := orchestrator.RunCycle(ctx, deps, t2.Add(time.Second)); err != nil {
		t.Fatalf("exchange 2 dispatch cycle: %v", err)
	}
	if calls != 2 {
		t.Fatalf("agent calls = %d, want 2 after single follow-up message", calls)
	}
}

type testOrchestratorStateRepo struct {
	repo   *staterepo.Repo
	store  *model.Store
	status ui.StateRepoStatus
}

func (s *testOrchestratorStateRepo) Status(context.Context) (ui.StateRepoStatus, error) {
	return s.status, nil
}
func (s *testOrchestratorStateRepo) ImportSecretsKey(context.Context, string) (ui.StateRepoStatus, error) {
	return s.status, nil
}
func (s *testOrchestratorStateRepo) SetRemote(context.Context, ui.StateRepoRemoteRequest) (ui.StateRepoStatus, error) {
	return s.status, nil
}
func (s *testOrchestratorStateRepo) SetSecretsRepo(context.Context, ui.SecretsRepoRequest) (ui.StateRepoStatus, error) {
	return s.status, nil
}
func (s *testOrchestratorStateRepo) DismissResetNotices(context.Context) (ui.StateRepoStatus, error) {
	return s.status, nil
}
func (s *testOrchestratorStateRepo) Sync(ctx context.Context) (ui.StateRepoStatus, error) {
	if _, err := staterepo.Apply(ctx, s.repo, s.store.DB(), model.SchemaVersion); err != nil {
		return ui.StateRepoStatus{}, err
	}
	if _, err := staterepo.Sync(ctx, s.repo, s.store.DB(), model.SchemaVersion); err != nil {
		return ui.StateRepoStatus{}, err
	}
	return s.status, nil
}
func (s *testOrchestratorStateRepo) SyncAfterWrite(ctx context.Context) error {
	_, err := s.Sync(ctx)
	return err
}
func (s *testOrchestratorStateRepo) Tree(context.Context) (ui.StateRepoTree, error) {
	return ui.StateRepoTree{}, nil
}
func (s *testOrchestratorStateRepo) File(context.Context, string, int64) (ui.StateRepoFile, error) {
	return ui.StateRepoFile{}, nil
}

func TestUIEditsWithStateRepoUnlockedPersistAcrossOrchestratorAndStateRepoCycles(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(*model.Config) {})

	stateDir := filepath.Join(t.TempDir(), "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: stateDir})
	if err != nil {
		t.Fatalf("staterepo.Open: %v", err)
	}
	if err := staterepo.Load(ctx, repo, store.DB(), model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Load: %v", err)
	}

	sr := &testOrchestratorStateRepo{
		repo:   repo,
		store:  store,
		status: ui.StateRepoStatus{Branch: "main"},
	}
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		StateRepo:    sr,
	}, store)
	client.Now = func() time.Time { return baseTime }

	// Edit console_sessions, default_capabilities, repo_config, schedules, and templates via UI client while settings_locked is false (default).
	capID := ui.OfferedCapabilities()[0].ID
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		DefaultCapabilities: &[]string{capID},
	}); err != nil {
		t.Fatalf("UpdateSettings(defaultCapabilities): %v", err)
	}
	if _, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "ops-chat",
		Title: "Ops Chat",
	}); err != nil {
		t.Fatalf("CreateConsoleSession: %v", err)
	}
	promptExt := "Always verify tests"
	if _, err := client.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{
		PromptExtension: &promptExt,
	}); err != nil {
		t.Fatalf("SetRepoSettings: %v", err)
	}
	if _, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title:      "Hourly sweep",
		Repo:       "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "everyNHours", EveryNHours: 1},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:  "triage",
		Title: "Triage bug",
	}); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}

	// Sync to state repo and run both state-repo reconcile and orchestrator RunCycle.
	if err := sr.SyncAfterWrite(ctx); err != nil {
		t.Fatalf("SyncAfterWrite: %v", err)
	}
	runSessions(t, store, baseTime.Add(time.Minute))
	if _, err := sr.Sync(ctx); err != nil {
		t.Fatalf("second staterepo Sync: %v", err)
	}
	runSessions(t, store, baseTime.Add(2*time.Minute))

	// Verify the console session task for ops-chat was created and survived state-repo sync cycles.
	sessions := sessionTasks(t, store)
	if !slices.ContainsFunc(sessions, func(tk model.Task) bool {
		return slices.Contains(tk.Tags, model.ConsoleSessionTag("ops-chat"))
	}) {
		t.Fatalf("expected ops-chat session task to be reconciled and preserved across staterepo sync, got %+v", sessions)
	}
}

// declareDirector stores a director declaring reads and writes on the
// acme repositories this file's restart tests use.
func declareDirector(t *testing.T, store *model.Store, reads []model.RepoRef, writes []model.WriteTarget) {
	t.Helper()
	settings(t, store, func(cfg *model.Config) {
		cfg.TargetRepos = []string{"acme/widgets", "acme/docs", "acme/infra"}
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			NetworkAccess: model.NetworkOpen,
			Reads:         reads,
			Writes:        writes,
		}}
	})
}

func hasWrite(writes []model.WriteTarget, owner, name string) bool {
	return slices.ContainsFunc(writes, func(w model.WriteTarget) bool {
		return w.Repo.Same(model.RepoRef{Owner: owner, Name: name})
	})
}

func hasRead(reads []model.RepoRef, owner, name string) bool {
	return slices.ContainsFunc(reads, func(r model.RepoRef) bool {
		return r.Same(model.RepoRef{Owner: owner, Name: name})
	})
}

// A reads: or writes: entry dropped from a standing session's declaration
// while there is no record of what was last synced onto its task -- a
// store from before console_session_sync existed, the first sync after
// the upgrade that brings it -- is dropped from the task too: the
// declaration is authoritative, the way it already was for GrantByGrain
// grants. Kept, it was kept for good, since every later sync saw it as
// already synced, and the session went on pushing to a repository the
// deployment's configuration no longer let it write.
func TestConsoleSessionSyncDropsUndeclaredWritesWithoutSnapshot(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	docs, infra := model.RepoRef{Owner: "acme", Name: "docs"}, model.RepoRef{Owner: "acme", Name: "infra"}
	widgets := model.WriteTarget{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Base: "main"}
	infraWrite := model.WriteTarget{Repo: infra, Base: "main"}
	declareDirector(t, store, []model.RepoRef{docs, infra}, []model.WriteTarget{widgets, infraWrite})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	if !hasWrite(director.Writes, "acme", "infra") || !hasRead(director.Reads, "acme", "infra") {
		t.Fatalf("filed director Reads = %v, Writes = %v, want acme/infra on both", director.Reads, director.Writes)
	}

	declareDirector(t, store, []model.RepoRef{docs}, []model.WriteTarget{widgets})
	if _, err := store.DB().ExecContext(ctx, "DELETE FROM `console_session_sync`"); err != nil {
		t.Fatal(err)
	}
	runSessions(t, store, baseTime.Add(time.Minute))
	runSessions(t, store, baseTime.Add(2*time.Minute))

	synced, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasWrite(synced.Writes, "acme", "infra") || !hasWrite(synced.Writes, "acme", "widgets") {
		t.Errorf("Writes = %v, want only the declared acme/widgets", synced.Writes)
	}
	if hasRead(synced.Reads, "acme", "infra") || !hasRead(synced.Reads, "acme", "docs") {
		t.Errorf("Reads = %v, want acme/infra dropped and acme/docs kept", synced.Reads)
	}
}

// What a session's sync last applied is in the store, not in the
// daemon's memory, so a restart tells an operator's own edit on the task
// from a declaration change exactly as a running daemon does: the write
// the operator added survives it, and one the declaration dropped while
// the daemon was down is dropped.
func TestConsoleSessionSnapshotSurvivesARestart(t *testing.T) {
	orchestrator.LogToTest(t)
	dir := t.TempDir()
	store, db := openStoreInDir(t, dir)
	ctx := context.Background()
	docs := model.RepoRef{Owner: "acme", Name: "docs"}
	infra := model.WriteTarget{Repo: model.RepoRef{Owner: "acme", Name: "infra"}, Base: "main"}
	declareDirector(t, store, []model.RepoRef{docs}, []model.WriteTarget{infra})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		StateRepoRef: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }
	writes := []ui.WriteTarget{{Repo: "acme/infra", Base: "main"}, {Repo: "acme/widgets", Base: "main"}}
	if _, err := client.UpdateTask(ctx, director.ID, ui.UpdateTaskRequest{Writes: &writes}); err != nil {
		t.Fatalf("operator adding a write: %v", err)
	}
	runSessions(t, store, baseTime.Add(2*time.Minute))
	db.Close()

	store, db = openStoreInDir(t, dir)
	defer db.Close()
	declareDirector(t, store, []model.RepoRef{docs}, nil)
	runSessions(t, store, baseTime.Add(3*time.Minute))

	synced, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWrite(synced.Writes, "acme", "widgets") {
		t.Errorf("Writes = %v after a restart, lost the operator's acme/widgets", synced.Writes)
	}
	if hasWrite(synced.Writes, "acme", "infra") {
		t.Errorf("Writes = %v after a restart, kept acme/infra the declaration dropped while the daemon was down", synced.Writes)
	}
}
