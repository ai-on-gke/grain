package orchestrator_test

// The standing console sessions (grain/task-415), reached only through
// the exported RunCycle -- this package's tests never call an unexported
// reconciler directly. A cycle here needs nothing beyond a store, for
// upgradecheck_test.go's own reason: with no ready task and no pull
// request link, dispatch and sync both no-op.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/capability/durable"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
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
	if !slices.Equal(grants, []string{"bootstrap-playbooks", "deployment-health", "durable", "grain-log", "self-debug"}) {
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
