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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/ai-on-gke/grain/pkg/model"
)

// consoleSessionPrincipal is what grain acts as when it is keeping a
// standing session alive -- filing its task, putting it back to waiting
// after an exchange, and posting the one line it says for itself. Its
// own actor beside "schedule" and "upgrade", so a human reading the
// director's task can tell which of grain's mechanisms wrote each thing
// on it.
//
// Defined in pkg/model (model.ConsoleSessionPrincipal) because the
// approval it writes is read there as well as written here --
// model.SelfApproved counts a task this principal queued as a level of
// self-approved work. Aliased rather than spelled out twice, so the
// principal a session acts as and the principal that bound recognizes
// cannot drift apart.
var consoleSessionPrincipal = model.ConsoleSessionPrincipal

// consoleSessionOpening is the first thing in a session's conversation,
// and it is also what the session is parked on until somebody says
// something (parkConsoleSession). A comment rather than nothing because
// parking is "waiting for a reply to *this*"
// (Observation.PendingQuestionCommentID), which needs a comment to point
// at, and because a chat window that opens empty says nothing about what
// it is for.
const consoleSessionOpening = model.ConsoleSessionOpening

// consoleSessionResting is what grain says when a session's run ended
// without asking anything, which is the ordinary way an exchange ends:
// the agent answered, and there is nothing further until somebody
// replies. It is the comment the session is then parked on.
//
// Short and unmistakably grain's, rather than an imitation of the agent
// saying goodbye: it is punctuation between exchanges, and the chat
// draws it as such.
const consoleSessionResting = model.ConsoleSessionResting

// isConsoleSessionResting reports whether c is grain's resting line: the
// words *and* the principal that parks a session. Matching the words
// alone would also catch a person, or an agent, saying them.
func isConsoleSessionResting(c model.Comment) bool {
	return c.Author.Actor == consoleSessionPrincipal && strings.TrimSpace(c.Body) == consoleSessionResting
}

// reconcileConsoleSessions keeps this deployment's standing interactive
// sessions open -- the director, and anything else
// model.Config.ConsoleSessions names (grain/task-415).
//
// # What "standing" costs, and what it buys
//
// One task per session, found by its tag and never finished. Everything
// else about it is an ordinary task: it is dispatched by dispatch.Cycle
// with an ordinary sandbox, it holds capabilities the same way, its runs
// are recorded the same way, and what it proposes is a proposal like any
// other. The whole of the exemption is here, and it is three rules:
//
//   - It is filed approved, without anybody approving it. A session a
//     person has to approve before they can talk to it is a session that
//     is not there when they need it, and the thing being approved has
//     already been decided -- by whoever configured the session.
//   - It is never done. A run that ends without asking a question would
//     ordinarily complete the task, and a completed task sits in the
//     inbox waiting to be read (model.StateAwaitingAck) -- a person's
//     comment sends it back to the queue (ui.Client.AddComment), but it
//     is not a conversation resting between exchanges. So a completed
//     session is un-completed here and parked back on its own
//     conversation, which is what makes the chat the same chat tomorrow.
//   - It is never retired for failing. Three failed runs in a row take
//     an ordinary task out of dispatch (model.MaxConsecutiveFailures);
//     for a session that would mean the chat silently stopping
//     answering, so the streak is cleared and the person on the other
//     end can simply say it again.
//
// What it deliberately does *not* do is hold a sandbox open between
// exchanges. A session is live in the sense that matters -- the
// conversation persists, and a message reaches an agent with the whole
// history of it -- but the sandbox is built for each exchange and
// destroyed with it, exactly as for every other task. Holding one open
// would mean a VM and a worker slot spent on a director nobody is
// talking to, for a saving that is real but small, and grain's sandboxes
// are per-run everywhere else.
//
// # Level-triggered, like every other reconciler
//
// Nothing here remembers anything. Each cycle reads the configured
// sessions, looks up the task each one names, and makes the difference:
// a missing task is filed, a completed one is put back to waiting, a
// drifted capability set is written back. A deployment restored from a
// state repository dump finds its sessions and their tasks exactly as it
// left them, and one whose settings changed a second ago is following
// the new settings a cycle later.
//
// A session that fails does not stop the others, for the reason
// reconcileSchedule keeps firing schedules past one that failed.
func reconcileConsoleSessions(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	stored, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading the console session settings: %w", err)
	}
	cfg := model.DefaultConfig()
	if stored != nil {
		cfg = *stored
	}
	sessions := model.ConsoleSessionsOr(cfg.ConsoleSessions)
	configured := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		configured[s.ID] = true
	}
	var errs []error
	openBySession, err := deps.Store.OpenConsoleSessionTaskIDs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing open console session tasks: %w", err)
	}
	for sessionID, taskID := range openBySession {
		if configured[sessionID] {
			continue
		}
		if err := deps.Store.ObserveField(ctx, taskID, now, func(o *model.Observation) {
			o.ClosedAt = &now
			o.PendingQuestionCommentID = nil
			o.PendingSecret = ""
		}); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: closing removed %s console session task %s: %w", sessionID, taskID, err))
			continue
		}
		_ = deps.Store.UpdateTask(ctx, taskID, func(t *model.Task) error {
			if !slices.Contains(t.Tags, model.ConsoleSessionResetTag) {
				t.Tags = append(t.Tags, model.ConsoleSessionResetTag)
			}
			return nil
		})
	}
	if len(sessions) == 0 {
		return errors.Join(errs...)
	}
	// Read once for the whole pass rather than per session: the answer
	// this needs is "is this session mid-exchange", and a live run is the
	// only way it can be. There are as many of these rows as there are
	// runs in flight on the whole deployment.
	live, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading the live runs: %w", err)
	}
	running := make(map[string]bool, len(live))
	for _, run := range live {
		running[run.TaskID] = true
	}
	for _, session := range sessions {
		if err := reconcileConsoleSession(ctx, deps, session, running, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: keeping the %s console session open: %w", session.ID, err))
		}
	}
	return errors.Join(errs...)
}

// reconcileConsoleSession is one session's whole reconciliation: find
// its task, file one if there is none, and otherwise make what is there
// match what is configured.
func reconcileConsoleSession(ctx context.Context, deps Deps, session model.ConsoleSession,
	running map[string]bool, now time.Time) error {

	tag := model.ConsoleSessionTag(session.ID)
	id, err := deps.Store.TaskIDWithTag(ctx, tag)
	if err != nil {
		return err
	}
	// The closed conversation this filing replaces, if it replaces one.
	var previous string
	if id != "" {
		task, err := deps.Store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		obs, err := deps.Store.GetObservation(ctx, id)
		if err != nil {
			return err
		}
		// A closed session is a conversation somebody ended. grain does
		// not reopen it -- reopening a thing a person deliberately shut
		// is the worse of the two mistakes, and the way to stop having a
		// session at all is to remove it from the settings -- but the
		// session is still configured, so the next one starts as a fresh
		// task with a fresh conversation, and the closed one keeps its
		// record.
		if task != nil && (obs == nil || obs.ClosedAt == nil) {
			return updateConsoleSession(ctx, deps, session, *task, obs, running[task.ID], now)
		}
		// The conversation that ended. The replacement is told where it
		// came from and is handed that thread's standing summary --
		// fileConsoleSession's own doc comment on both -- and a session
		// filed for the first time, which reaches here with no task at
		// all, is handed nothing. A task closed by deleting or resetting
		// the session carries ConsoleSessionResetTag so a newly created
		// session with the same id starts fresh.
		if task != nil && !slices.Contains(task.Tags, model.ConsoleSessionResetTag) {
			previous = task.ID
		}
	}
	filed, err := fileConsoleSession(ctx, deps, session, previous, now)
	if err != nil {
		return err
	}
	log.Printf("orchestrator: filed task %s for the %s console session", filed, session.ID)
	return nil
}

// fileConsoleSession files the task a session's conversation lives on.
//
// The shape of it is the feature, so each piece is a decision, the same
// accounting fileUpgradeCheck makes for the other task grain files on
// its own behalf:
//
//   - Interactive, which is what draws the task as a chat rather than a
//     timeline (model.Task.Interactive, ui/src/components/TaskChat.jsx)
//     and dispatches it ahead of the ordinary backlog. A person waiting
//     in front of a chat is the one case where jumping the queue is
//     obviously right.
//   - IntentAnalyze and no Target. A session answers in words and files
//     tasks; it has no branch of its own and no change to push, and
//     giving it a repository to write to would invite a pull request
//     nobody asked for. The work it decides on is a proposal, dispatched
//     as its own task with its own sandbox and its own review.
//   - Reads: the state repository, when this deployment has one -- the
//     same evidence base the memory agent and the upgrade check are
//     given, and what "what is this deployment actually doing" is
//     answered out of.
//   - The capabilities the session configures, granted GrantByGrain: it
//     is grain applying them to a task of its own, on the strength of a
//     settings row, rather than a human labelling a task.
//   - Approved, with no approver. See reconcileConsoleSessions' own
//     doc comment on why a session may not wait for approval.
//   - Parked on its opening line, so it starts as a chat waiting for the
//     person rather than as a run about to happen: filing a session must
//     not spend a sandbox on an agent nobody has said anything to yet.
//
// # What a replacement carries, and what it does not
//
// previous is the session's own closed task when this filing is a fresh
// conversation for a session that had one, and "" when the session is
// being filed for the first time. A replacement carries exactly two
// things across, and both are decided in docs/implementation-notes.md's
// "A chat that never ends is not split for you":
//
//   - model.LinkContinues back to the closed task -- provenance, so the
//     new chat says where it came from and the old one is findable from
//     it. Never a blocking kind: see LinkContinues' own doc comment.
//   - The most recent handover that thread's runs wrote
//     (carryConsoleSessionHandover), and nothing else of it. Carrying
//     more would be carrying the very thing the conversation bound
//     exists to stop carrying (orchestrator's maxConversationBytes), and
//     nothing is lost by not carrying it: the old thread is a closed
//     task one click away.
//
// Everything else a session is -- its title, body, capabilities, network
// access, reads and writes -- is rebuilt here out of the
// model.ConsoleSession row rather than inherited, which is why a
// replacement needs nothing from the task it replaces.
func fileConsoleSession(ctx context.Context, deps Deps, session model.ConsoleSession,
	previous string, now time.Time) (string, error) {
	return model.FileConsoleSessionTask(ctx, deps.Store, session, model.FileConsoleSessionOptions{
		StateRepo:            deps.Config.settingsRepo(),
		NetworkIsolates:      sandboxesIsolateNetwork(deps.Sandboxes),
		PreviousTaskID:       previous,
		CarryHandoverSummary: previous != "",
		Now:                  now,
	})
}

const consoleSessionHandoverNote = model.ConsoleSessionHandoverNote

func carryConsoleSessionHandover(ctx context.Context, deps Deps, previous, taskID string, now time.Time) error {
	return model.CarryConsoleSessionHandover(ctx, deps.Store, previous, taskID, now)
}

// updateConsoleSession is the standing half: a session's task that
// already exists, made to match what the settings now say and put back
// into a state somebody can talk to.
func updateConsoleSession(ctx context.Context, deps Deps, session model.ConsoleSession,
	task model.Task, obs *model.Observation, running bool, now time.Time) error {

	if err := syncConsoleSessionTask(ctx, deps, session, task, now); err != nil {
		return err
	}
	// A live run is the one state to leave entirely alone: the agent is
	// mid-exchange, and whatever it does next -- answering, asking, or
	// resting -- is what the next cycle reads.
	if running {
		return nil
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		return restConsoleSession(ctx, deps, task.ID, now)
	}
	return nil
}

// restConsoleSession is what to do with a session's task between runs:
// park it, unless there is a message waiting on it that no run has been
// dispatched for yet, in which case leave it queued so this same cycle's
// dispatch reconciler picks it up.
//
// Both cases arrive here as the same row -- an approved task that is
// neither parked nor running -- and telling them apart is the whole of
// this function, because getting it wrong breaks the session in one
// direction or the other:
//
//   - A reply that has not been dispatched yet. ui.Client.AddComment
//     un-parks by clearing Observation.PendingQuestionCommentID, and
//     this reconciler runs *before* reconcileDispatch in the same cycle
//     (Reconcilers()), so parking unconditionally here would re-park the
//     task within seconds of the person hitting send and dispatch would
//     never see it ready. That is the whole session, silently: grain
//     answers "_Waiting for you._" to everything and no agent is ever
//     asked (grain/task-399).
//   - A run that ended without completing the task -- a failure, a
//     cancellation, a deployment restarted mid-run. Nothing has been
//     said since that run, so leaving it queued would dispatch an agent
//     into a conversation it has already answered, over and over, since
//     a session's failure streak is cleared rather than retiring it.
//     Park it: the person can simply say it again.
//
// Except for a run whose sandbox could not be prepared because the
// cluster's exec transport failed (consoleSessionSetupRetry): nothing
// about that turn is the person's to repeat, so the message stays
// pending and is answered by grain's own retry.
func restConsoleSession(ctx context.Context, deps Deps, taskID string, now time.Time) error {
	retrying, retryNote, err := consoleSessionSetupRetry(ctx, deps, taskID)
	if err != nil {
		return err
	}
	if retrying {
		if retryNote != "" {
			if err := noteConsoleSession(ctx, deps, taskID, retryNote, now); err != nil {
				return err
			}
		}
		return nil
	}
	susps, err := deps.Store.Suspensions(ctx, taskID)
	if err != nil {
		return err
	}
	for _, s := range susps {
		if s.ResumedAt != nil {
			continue
		}
		if s.Kind != model.SuspendRequested {
			return nil
		}
		ro, err := model.ParseResumeOn(s.ResumeOn)
		if err != nil || !ro.Message || ro.CI != "" || ro.Goal || s.ResumeAt != nil {
			return nil
		}
		// An open requested suspension on {message: true} whose deferral has
		// already been cleared (WakeOnMessage, WakeRestingTask, or Undefer)
		// is a session that has just been woken and is waiting for dispatch.
		t, err := deps.Store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if t != nil && t.DeferredAt == nil {
			return nil
		}
	}
	waiting, unanswered, err := consoleSessionRest(ctx, deps, taskID)
	if err != nil {
		return err
	}
	if waiting {
		if _, err := deps.Store.WakeOnMessage(ctx, taskID, now); err != nil {
			return err
		}
		return nil
	}
	note := consoleSessionResting
	if unanswered != nil {
		note = consoleSessionFailureNote(*unanswered)
	}
	return parkConsoleSession(ctx, deps, taskID, note, now)
}

// consoleSessionSetupRetry reports whether a session's latest run ended
// before reaching its agent because the cluster's exec transport failed
// (kubectlgate.IsTransientTransport on its detail), in which case the
// message it was dispatched for is still pending and the session is left
// queued rather than parked on the person.
//
// While that run is one of the transient setup retries (a setup
// suspension, finishSetupFailed) nothing is said at all: the retry is
// seconds away, and the person's message is answered by it. Once the
// retries are spent and the run ended setup-failed, note is the one line
// saying why the answer is late and that grain keeps trying -- dispatch's
// own backoff spaces those tries out, and a fresh run gets its own quick
// retries -- so the person is never told to say it again for a fault
// that is the cluster's.
func consoleSessionSetupRetry(ctx context.Context, deps Deps, taskID string) (retrying bool, note string, err error) {
	runs, err := deps.Store.Runs(ctx, taskID)
	if err != nil {
		return false, "", err
	}
	var latest *model.Run
	for i, run := range runs {
		if latest == nil || run.StartedAt.After(latest.StartedAt) {
			latest = &runs[i]
		}
	}
	if latest == nil || latest.FinishedAt == nil || !kubectlgate.IsTransientTransport(latest.Detail) {
		return false, "", nil
	}
	switch {
	case latest.SuspendKind == model.SuspendSetup:
		return true, "", nil
	case latest.Outcome == model.SetupFailedOutcome:
		return true, consoleSessionTunnelNote, nil
	}
	return false, "", nil
}

// consoleSessionTunnelNote is what a session says once a message's quick
// setup retries have all failed on the cluster's exec transport.
const consoleSessionTunnelNote = "_That has not been answered yet: the cluster's exec tunnel is failing, " +
	"so grain could not prepare a sandbox for it. grain will retry automatically on the next cycle; " +
	"there is no need to say it again._"

// noteConsoleSession posts note on a session's task, once: a note the
// session already ends on is not said again every cycle it stays true.
// Unlike parkConsoleSession it leaves the task queued.
func noteConsoleSession(ctx context.Context, deps Deps, taskID, note string, now time.Time) error {
	comments, err := deps.Store.Comments(ctx, taskID)
	if err != nil {
		return err
	}
	if n := len(comments); n > 0 && comments[n-1].Author.Actor == consoleSessionPrincipal && comments[n-1].Body == note {
		return nil
	}
	_, err = deps.Store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: consoleSessionPrincipal},
		Body:      note,
		CreatedAt: now,
	})
	return err
}

// consoleSessionFailureNote is what grain parks a session on when the
// run that should have answered did not get there.
//
// "_Waiting for you._" is the wrong thing to say about a message nobody
// answered: the chat draws no run rows and no failure banner
// (TaskChat.jsx), so a person who asked something and got that line back
// has no way at all of telling "the agent had nothing to add" from "no
// agent ever ran". The detail is the run's own -- the same sentence
// `grain get` shows for a failed attempt -- because for the failures a
// session actually hits it is the whole of the fix: a sandbox this
// deployment cannot build for it, a credential it does not hold, a
// model that refused the turn.
func consoleSessionFailureNote(run model.Run) string {
	detail := strings.TrimSpace(run.Detail)
	switch {
	case detail != "":
	case run.FinishedAt == nil:
		detail = "its run stopped without finishing, which usually means grain was restarted under it"
	default:
		detail = "its run ended as " + string(model.EndingOf(run.Outcome, run.Detail))
	}
	return "_That did not get an answer: " + detail + "_\n\n_Say it again to try once more._"
}

// consoleSessionRest is what a session's idle task says about itself:
// whether a person has said something no run has been shown yet
// (waiting), and, when they have not, the run that ended without
// answering the last thing they said (unanswered, nil if the last
// exchange ended properly or there has been no run at all).
//
// Waiting is decided by what the runs *saw*, not by when they ended
// (Observation.DeliveredCommentID, undeliveredHumanComment). A message is
// shown to a run three ways -- folded into its prompt, handed to it on a
// tool result as an addendum, or returned as the reply to its chat call
// -- and each moves that mark. A person's comment after it is one no run
// has read, however long before the last run ended it was posted: a run
// writing its final answer, or one whose chat call its client had
// already abandoned, has no tool call left to carry it, and counting it
// as answered because the run happened to still be alive dropped it
// (grain/task-1774). A comment at or before the mark is answered, so a
// reply a chat call returned is never dispatched a second time.
//
// A task whose mark was never recorded -- every run of it predates the
// column -- keeps the older rule: a human comment counts only if it is
// after both grain's last line and the latest run's end.
//
// Unanswered turns on the last thing grain itself said for the session
// -- its opening line, or the line it parks on -- since that is the mark
// between one exchange and the next: a run that ended after it is the
// exchange the message got.
//
// Only human comments count as something to answer. The agent's own
// messages and grain's park lines are the session talking to itself,
// and a session dispatched for those would never stop being
// dispatched.
func consoleSessionRest(ctx context.Context, deps Deps, taskID string) (waiting bool, unanswered *model.Run, err error) {
	comments, err := deps.Store.Comments(ctx, taskID)
	if err != nil {
		return false, nil, err
	}
	obs, err := deps.Store.GetObservation(ctx, taskID)
	if err != nil {
		return false, nil, err
	}
	var said time.Time
	var saidID int64
	for _, c := range comments {
		if c.Author.Actor == consoleSessionPrincipal && !c.CreatedAt.Before(said) {
			said = c.CreatedAt
			saidID = c.ID
		}
	}
	runs, err := deps.Store.Runs(ctx, taskID)
	if err != nil {
		return false, nil, err
	}
	cutoff := said
	runAfterSaid := false
	for i, run := range runs {
		ended := run.StartedAt
		if run.FinishedAt != nil {
			ended = *run.FinishedAt
		}
		if !ended.After(cutoff) {
			continue
		}
		cutoff = ended
		runAfterSaid = true
		// The exchange this session is resting after. A run that
		// succeeded or rested on its goal said whatever it had to say in
		// the conversation itself, so there is nothing for grain to add.
		if model.EndingOf(run.Outcome, run.Detail) == model.EndingSucceeded || run.SuspendKind == model.SuspendRequested {
			unanswered = nil
			continue
		}
		unanswered = &runs[i]
	}
	if pending, recorded := undeliveredHumanComment(comments, obs, runs); recorded {
		if pending != nil {
			return true, nil, nil
		}
		return false, unanswered, nil
	}
	for _, c := range comments {
		if c.Author.Actor.Kind != model.PrincipalHuman || c.ID <= saidID {
			continue
		}
		if runAfterSaid {
			if c.CreatedAt.After(cutoff) {
				return true, nil, nil
			}
		} else if !c.CreatedAt.Before(cutoff) {
			return true, nil, nil
		}
	}
	return false, unanswered, nil
}

// undeliveredHumanComment is the first comment a person posted on a task
// that no run has been given, or nil when every one of them has been.
// recorded is false when there is no mark to judge by -- no run has been
// dispatched since grain started keeping one -- and the caller has to
// decide some other way.
//
// Given means either of two things. The comment is at or before
// Observation.DeliveredCommentID, the last comment a run was actually
// shown: in its prompt, as an addendum, or as a chat reply. Or it was
// already there when the latest run started, which is a run dispatched
// for it: that run either had it in its prompt or failed on the way to
// its agent, and a failure is answered by grain's own note in the
// conversation rather than by dispatching into it again and again. The
// second rule is what keeps a dispatch that failed before it could
// record the mark from turning into a loop.
//
// Human comments only: what a run says, and what grain says about it,
// are never something to dispatch a run to answer.
func undeliveredHumanComment(comments []model.Comment, obs *model.Observation, runs []model.Run) (pending *model.Comment, recorded bool) {
	if obs == nil || obs.DeliveredCommentID == nil {
		return nil, false
	}
	var started time.Time
	for _, run := range runs {
		if run.StartedAt.After(started) {
			started = run.StartedAt
		}
	}
	for i, c := range comments {
		if c.ID <= *obs.DeliveredCommentID || c.Author.Actor.Kind != model.PrincipalHuman {
			continue
		}
		if !c.CreatedAt.After(started) {
			continue
		}
		return &comments[i], true
	}
	return nil, true
}

// syncConsoleSessionTask writes back whatever the settings have changed
// about a session since its task was filed: its title, the capabilities
// it holds, its granted read/write repositories, how much network its
// sandbox gets, and which model it runs on.
//
// This is what makes the capability and repo lists in the UI mean anything
// -- a tick there is a settings save, and this is what carries it onto the
// task, in time for the next exchange. The task's body is rewritten with
// it, since what the session is told about its own filing rights depends
// on ConsoleSession.TasksApproved.
//
// Only these fields. Everything else on the task is the conversation's
// own -- and a run's own record of what it did -- which nothing here has
// any business rewriting.
func syncConsoleSessionTask(ctx context.Context, deps Deps, session model.ConsoleSession,
	task model.Task, now time.Time) error {

	goal, err := deps.Store.TaskGoal(ctx, task.ID)
	if err != nil {
		return err
	}
	if goal == nil || goal.Kind != model.GoalPersonCloses {
		if err := deps.Store.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, model.PrincipalString(consoleSessionPrincipal), now); err != nil {
			return err
		}
	}
	if session.TitleSource == "" && session.ID != model.DirectorSessionID {
		// Saved before sessions recorded who titled them: whether the
		// body asks the agent to name the conversation turns on whether
		// the title is still what its first message would give.
		comments, err := deps.Store.Comments(ctx, task.ID)
		if err != nil {
			return err
		}
		session.TitleSource = session.ResolvedTitleSource(model.ConsoleSessionFirstMessage(comments))
	}
	prev, hasPrev := model.LastSyncedConsoleSession(deps.Store, task.ID)
	configTitle := model.ConsoleSessionTitle(session)
	configNetwork := consoleSessionNetworkAccess(deps, session)
	configModel := strings.TrimSpace(session.Model)
	stateRepo := deps.Config.settingsRepo()

	if !hasPrev {
		prev = model.ConsoleSessionConfigSnapshot{
			Capabilities:    session.Capabilities,
			Reads:           session.Reads,
			Writes:          session.Writes,
			NetworkAccess:   configNetwork,
			Model:           configModel,
			Title:           configTitle,
			TasksApproved:   session.TasksApproved,
			GoogleChatSpace: session.GoogleChatSpace,
		}
	}

	title := task.Title
	if title == "" || configTitle != prev.Title {
		title = configTitle
	}
	effectiveSession := session
	effectiveSession.Title = title
	if slices.Contains(task.Tags, model.ConsoleSessionTasksApprovedTag) {
		effectiveSession.TasksApproved = true
	} else if slices.Contains(task.Tags, model.ConsoleSessionTasksUnapprovedTag) {
		effectiveSession.TasksApproved = false
	} else if session.TasksApproved != prev.TasksApproved {
		effectiveSession.TasksApproved = session.TasksApproved
	} else if task.AutoApproveProposals {
		effectiveSession.TasksApproved = true
	}
	body := model.DirectorSessionBody(stateRepo, effectiveSession)

	grants := mergeConsoleSessionGrants(task.Grants, session, prev, hasPrev)
	reads := mergeConsoleSessionReads(task.Reads, stateRepo, session.Reads, prev.Reads, hasPrev)
	writes := mergeConsoleSessionWrites(task.Writes, session.Writes, prev.Writes, hasPrev)

	network := task.NetworkAccess
	if network == "" || configNetwork != prev.NetworkAccess {
		network = configNetwork
	}
	if network == model.NetworkNone && !sandboxesIsolateNetwork(deps.Sandboxes) {
		network = model.NetworkOpen
	}

	chosenModel := task.Model
	if configModel != prev.Model {
		chosenModel = configModel
	}

	links := task.Links
	if links == nil {
		links = []model.Link{}
	}
	if session.GoogleChatSpace != prev.GoogleChatSpace || (!hasPrev && session.GoogleChatSpace != "") {
		var updatedLinks []model.Link
		for _, l := range task.Links {
			if l.Kind != model.LinkGoogleChat {
				updatedLinks = append(updatedLinks, l)
			}
		}
		if session.GoogleChatSpace != "" {
			updatedLinks = append(updatedLinks, model.Link{Kind: model.LinkGoogleChat, Target: session.GoogleChatSpace})
		}
		if updatedLinks == nil {
			updatedLinks = []model.Link{}
		}
		links = updatedLinks
	}

	model.RecordSyncedConsoleSession(deps.Store, task.ID, model.ConsoleSessionConfigSnapshot{
		Capabilities:    session.Capabilities,
		Reads:           session.Reads,
		Writes:          session.Writes,
		NetworkAccess:   configNetwork,
		Model:           configModel,
		Title:           configTitle,
		TasksApproved:   session.TasksApproved,
		GoogleChatSpace: session.GoogleChatSpace,
	})

	taskWrites := task.Writes
	if taskWrites == nil {
		taskWrites = []model.WriteTarget{}
	}
	taskReads := task.Reads
	if taskReads == nil {
		taskReads = []model.RepoRef{}
	}
	taskLinks := task.Links
	if taskLinks == nil {
		taskLinks = []model.Link{}
	}
	if task.Title == title && task.Body == body && task.NetworkAccess == network &&
		task.Model == chosenModel &&
		slices.Equal(task.Grants, grants) &&
		slices.Equal(taskReads, reads) &&
		slices.Equal(taskWrites, writes) &&
		slices.Equal(taskLinks, links) {
		return nil
	}
	return deps.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Title = title
		t.Body = body
		t.Grants = grants
		t.NetworkAccess = network
		t.Reads = reads
		t.Writes = writes
		t.Links = links
		if t.Model != chosenModel {
			t.Model = chosenModel
			t.ModelSource = ""
		}
		return nil
	})
}

func mergeConsoleSessionGrants(current []model.Grant, session model.ConsoleSession,
	prev model.ConsoleSessionConfigSnapshot, hasPrev bool) []model.Grant {

	removedSession := map[string]bool{}
	if hasPrev {
		for _, c := range prev.Capabilities {
			if c != "" && !slices.Contains(session.Capabilities, c) {
				removedSession[c] = true
			}
		}
	}

	out := make([]model.Grant, 0, len(current)+len(session.Capabilities)+1)
	out = append(out, model.Grant{Capability: model.DurableCapability, Via: model.GrantByGrain})
	seen := map[string]bool{model.DurableCapability: true}

	for _, g := range current {
		if g.Capability == "" || seen[g.Capability] {
			continue
		}
		if g.Via == model.GrantByGrain {
			if hasPrev && removedSession[g.Capability] {
				continue
			}
			if !hasPrev && !slices.Contains(session.Capabilities, g.Capability) {
				continue
			}
		}
		if g.Via == model.GrantByDefault && !slices.Contains(session.Capabilities, g.Capability) {
			continue
		}
		seen[g.Capability] = true
		out = append(out, g)
	}

	for _, name := range session.Capabilities {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if !hasPrev || !slices.Contains(prev.Capabilities, name) {
			seen[name] = true
			out = append(out, model.Grant{Capability: name, Via: model.GrantByGrain})
		}
	}
	return out
}

func mergeConsoleSessionReads(current []model.RepoRef, stateRepo model.RepoRef,
	sessionReads, prevReads []model.RepoRef, hasPrev bool) []model.RepoRef {

	var out []model.RepoRef
	has := func(r model.RepoRef) bool {
		return slices.ContainsFunc(out, func(e model.RepoRef) bool { return e.Same(r) })
	}
	if stateRepo.Owner != "" {
		out = append(out, stateRepo)
	}
	for _, r := range current {
		if r.Owner == "" || r.Name == "" || has(r) {
			continue
		}
		wasInPrev := hasPrev && slices.ContainsFunc(prevReads, func(p model.RepoRef) bool { return p.Same(r) })
		inSession := slices.ContainsFunc(sessionReads, func(s model.RepoRef) bool { return s.Same(r) })
		if wasInPrev && !inSession {
			continue
		}
		out = append(out, r)
	}
	for _, r := range sessionReads {
		if r.Owner == "" || r.Name == "" || has(r) {
			continue
		}
		wasInPrev := hasPrev && slices.ContainsFunc(prevReads, func(p model.RepoRef) bool { return p.Same(r) })
		if !wasInPrev {
			out = append(out, r)
		}
	}
	if out == nil {
		out = []model.RepoRef{}
	}
	return out
}

func mergeConsoleSessionWrites(current, sessionWrites, prevWrites []model.WriteTarget, hasPrev bool) []model.WriteTarget {
	sameWrite := func(a, b model.WriteTarget) bool {
		return a.Repo.Same(b.Repo) && a.Base == b.Base
	}
	var out []model.WriteTarget
	has := func(w model.WriteTarget) bool {
		return slices.ContainsFunc(out, func(e model.WriteTarget) bool { return sameWrite(e, w) })
	}
	for _, w := range current {
		if w.Repo.Owner == "" || w.Repo.Name == "" || has(w) {
			continue
		}
		wasInPrev := hasPrev && slices.ContainsFunc(prevWrites, func(p model.WriteTarget) bool { return sameWrite(p, w) })
		inSession := slices.ContainsFunc(sessionWrites, func(s model.WriteTarget) bool { return sameWrite(s, w) })
		if wasInPrev && !inSession {
			continue
		}
		out = append(out, w)
	}
	for _, w := range sessionWrites {
		if w.Repo.Owner == "" || w.Repo.Name == "" || has(w) {
			continue
		}
		wasInPrev := hasPrev && slices.ContainsFunc(prevWrites, func(p model.WriteTarget) bool { return sameWrite(p, w) })
		if !wasInPrev {
			out = append(out, w)
		}
	}
	if out == nil {
		out = []model.WriteTarget{}
	}
	return out
}

// consoleSessionNetworkAccess is how much of the internet a session's
// sandbox gets: what the session asks for, unless this deployment cannot
// give it that, in which case it is what the deployment can give.
//
// The one case that matters is the shipped default meeting the shipped
// backend. A session asks for no network at all
// (model.DefaultConsoleSessions), and a host-directory deployment has no
// network of its own to withhold, so HostSandboxes.Acquire refuses the
// run rather than handing over the host's -- correctly, and fatally for
// a session: every message fails in setup, the chat is the one place
// with no run rows to see it in, and the operator's own director is
// dead. Since grain is both the thing asking and the thing refusing, it
// asks for what it can have (grain/task-399).
//
// Nothing is lost by it. On that backend a run has the host's network
// whatever the column says, so this changes what the task *asks* for and
// not what it gets. A deployment that can honour "none" -- granule --
// still does.
func consoleSessionNetworkAccess(deps Deps, session model.ConsoleSession) model.NetworkAccess {
	return model.ConsoleSessionNetworkAccess(session, sandboxesIsolateNetwork(deps.Sandboxes))
}

// consoleSessionGrants is the session's configured capabilities as
// grants: GrantByGrain, because it is grain applying them to its own
// task from a settings row rather than a human labelling one
// (model.ConsoleSessionGrants).
func consoleSessionGrants(session model.ConsoleSession) []model.Grant {
	return model.ConsoleSessionGrants(session)
}

func parkConsoleSession(ctx context.Context, deps Deps, taskID, note string, now time.Time) error {
	return model.ParkConsoleSessionTask(ctx, deps.Store, taskID, note, now)
}

// consoleSessionApprovesProposals reports whether tasks proposed by this
// task are filed approved rather than waiting for a person: this
// deployment's settings, read, and model.ConsoleSessionQueuesProposals
// asked.
//
// The reading is here and the rule is there because the rule has three
// callers and only one of them is in this package: the other gate is
// ui.Client.CreateTask, which files a proposal while its run is still
// going, and the third is the prompt the session itself is given
// (model.DirectorSessionBody). One predicate for all three is what stops
// a session being told its work is queued by a path that files it
// proposed (grain/task-311).
func consoleSessionApprovesProposals(ctx context.Context, store *model.Store, task model.Task) (bool, error) {
	if _, ok := model.ConsoleSessionIDOf(task); !ok {
		return false, nil
	}
	stored, err := store.GetConfig(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading the console session settings: %w", err)
	}
	cfg := model.DefaultConfig()
	if stored != nil {
		cfg = *stored
	}
	return model.ConsoleSessionQueuesProposals(model.ConsoleSessionsOr(cfg.ConsoleSessions), task), nil
}
