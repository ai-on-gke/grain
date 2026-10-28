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

import { describeGrant } from "./capabilityArgs.js";

// model.State's own vocabulary, in model.StateOf's precedence order.
export const STATE_ORDER = [
  "proposed",
  "queued",
  "running",
  "awaiting_reply",
  "failed",
  "awaiting_submit",
  "completed",
  // The two states a task is in when it is not in play: one put aside
  // for later and one over with. They sit together at the end because
  // that is how they are read -- everything above this line is the work
  // in flight, and both of these are hidden from a list until somebody
  // asks for them (SET_ASIDE_STATES below).
  "deferred",
  "closed",
];

export const STATE_LABELS = {
  proposed: "Proposed",
  // Every label here is one word (the operator's rule, grain/task-1238):
  // "Waiting" for a task that is waiting its turn, and "Asked" for both
  // of the states that are waiting on the reader. The state names
  // themselves -- the store's, the API's, ?state='s -- are unchanged.
  queued: "Waiting",
  running: "Running",
  // "Asked", like awaiting_submit below: to a person both mean the task
  // has stopped and it is their move. Which move is askedCase's job --
  // the chip's tooltip and the row's action say it.
  awaiting_reply: "Asked",
  // A task stops here, not "queued", once it has failed
  // model.MaxConsecutiveFailures runs in a row -- see StateFailed's own
  // doc comment. "Retry" (DetailOverlay's own Actions) is the only way
  // out.
  failed: "Failed",
  // The run is over and its pull request is on no queue: nothing will
  // land it until a human clicks Submit (model.StateAwaitingSubmit).
  // Worded as a wait on the reader, like awaiting_reply above, because
  // that is what it is -- these two are the only states a task never
  // leaves on its own.
  //
  // This used to be a chip beside a badge that read "Queued for merge",
  // which said the opposite of the truth about the one task on the list
  // that actually needed somebody. As a state it is countable: it gets a
  // sidebar entry and a filter of its own.
  //
  // A word in a column of states is still not enough on its own, because
  // every other word in that column is something grain is doing
  // (grain/task-296). So it is not only a label: it is one of
  // INBOX_STATES below, which is what puts it on the page about the
  // reader (InboxPage.jsx) with the Submit button on its row, in the
  // count beside that page's rail entry, and -- in the rail's own state
  // list -- in the accent and the hover that say this number is waiting
  // on you rather than on a machine (Sidebar.jsx).
  awaiting_submit: "Asked",
  // A run ending is not the work landing. model.StateOf holds a task
  // here from the moment its run finishes until its pull request is
  // merged or closed -- that is the one thing that sets Observation.
  // ClosedAt for a completed task (orchestrator.recordPullRequestEvents)
  // and so the only way out of this state -- which makes "waiting on the
  // merge queue", not "done", what it actually describes. "Completed"
  // read as done-and-landed, the one thing it never means.
  //
  // Now very nearly the only reading of it: a task nobody has submitted
  // and a task with nothing to submit both have states of their own
  // above. One case still sits under this label that is not literally on
  // the queue -- one the queue has tried and given up on
  // (completionPhase below is the chip that says so beside the badge).
  //
  // A review that pushed nothing was the other, and it was the worst of
  // them: excluded from 'awaiting_ack' by model.AwaitsAck because its
  // findings are on the change under review, it had no pull request, so
  // no queue could move it and nothing would ever close it -- it read
  // "Queued for merge" forever. It is not here any more.
  // orchestrator.SyncReviews closes such a review the cycle after its run
  // ends, which is the honest answer for a task that waits on nobody.
  completed: "Merging",
  // Put aside on purpose (model.StateDeferred): still work grain is
  // meant to do one day, but not work anybody is waiting on today, so it
  // is out of the way until somebody picks it back up. "Deferred" rather
  // than "Later" or "Someday" because it is the word the store, the API
  // and the CLI all use for it.
  deferred: "Deferred",
  closed: "Closed",
  merged: "Merged",
  // Not a state: the one display state ASKED_STATES are shown as, and
  // the rail's and the board editor's single entry for both of them.
  asked: "Asked",
};

// ASKED_STATES are the two states shown as one: "Asked". Both are a task
// that has stopped until the reader does something, and a person reads
// them as one thing; which thing is askedCase's answer.
export const ASKED_STATES = ["awaiting_reply", "awaiting_submit"];

// displayState is the state a person is shown for a model state: "asked"
// for either of ASKED_STATES, and the state itself otherwise.
export function displayState(state) {
  return ASKED_STATES.includes(state) ? "asked" : state;
}

// statesOf is the model states one display state stands for -- both
// ASKED_STATES for "asked", and the state itself for anything else, so a
// link or a stored layout naming awaiting_reply alone still selects
// exactly what it always did.
export function statesOf(state) {
  return state === "asked" ? [...ASKED_STATES] : [state];
}

// DISPLAY_STATE_ORDER is STATE_ORDER as a picker offers it: the two
// asked states folded into one "asked" entry where the first of them
// stood.
export const DISPLAY_STATE_ORDER = STATE_ORDER.flatMap((s) =>
  s === ASKED_STATES[0] ? ["asked"] : ASKED_STATES.includes(s) ? [] : [s],
);

// stateLabels is the labels of a list of states with repeats dropped, so
// a column collecting both asked states is titled "Asked" once.
export function stateLabels(states) {
  return [...new Set((states || []).map((s) => STATE_LABELS[s] || s))];
}

// displayCounts folds a per-state count (an object keyed by state, or a
// list of {state, count}) into one entry per display state, in
// DISPLAY_STATE_ORDER: a legend counting both asked states shows one
// "Asked" chip with their sum. A state this build does not know follows,
// as written.
export function displayCounts(counts) {
  const entries = Array.isArray(counts)
    ? counts.map((c) => [c.state, c.count])
    : Object.entries(counts || {});
  const sums = new Map();
  for (const [state, count] of entries) {
    if (!count) continue;
    const s = displayState(state);
    sums.set(s, (sums.get(s) || 0) + count);
  }
  const known = DISPLAY_STATE_ORDER.filter((s) => sums.has(s));
  const rest = [...sums.keys()].filter((s) => !DISPLAY_STATE_ORDER.includes(s));
  return [...known, ...rest].map((state) => ({
    state,
    count: sums.get(state),
  }));
}

// askedCase says which of the several things "Asked" covers one task is
// in, or null for a task in neither of ASKED_STATES: kind is "question"
// (an agent's ask_question), "message" (an interactive task or console
// session waiting for its next message), "stopped" (a turn the reader
// stopped -- ui.Task.TurnStopped) or "submit" (awaiting_submit); title
// is the chip's tooltip; action is what ends it, "reply" or "submit".
export function askedCase(t) {
  if (!t || !ASKED_STATES.includes(t.state)) return null;
  if (t.state === "awaiting_submit") {
    const what = t.pullRequest || "its change";
    let title = `Asked you to submit ${what}`;
    if (t.pathTriggerHold) {
      title += ` — held by repository gating triggers (human: ${t.pathTriggerHold})`;
    } else if (t.securityHold) {
      const review = t.securityReviewTask
        ? ` for security review #${t.securityReviewTask}`
        : "";
      title += ` — on security hold (${t.securityHold})${review}`;
    }
    return { kind: "submit", title, action: "submit" };
  }
  if (t.turnStopped)
    return { kind: "stopped", title: "Stopped by you", action: "reply" };
  if (t.interactive || t.consoleSession)
    return {
      kind: "message",
      title: "Waiting for your message",
      action: "reply",
    };
  return { kind: "question", title: "Asked a question", action: "reply" };
}

// SET_ASIDE_STATES is the states a task list leaves out unless it is
// asked for them: a list is about what is in play, and neither a closed
// task nor one somebody deliberately put aside is.
//
// A list leaves them out as an ordinary filter condition -- "State is
// none of Deferred, Closed", the one every task view starts narrowed by
// (taskFilters.js's defaultFilters) -- rather than by a rule of its own.
// It used to be the latter: a checkbox each in the toolbar ("Show closed
// tasks", "Show deferred tasks"), a branch of their own in the list's
// matching, and a per-list override that no link could carry. That made
// what a list was about two questions that could not be asked together
// -- the conditions said one thing, the checkboxes said another, and
// only one of them was on screen as a chip or in the URL. As a condition
// it is editable beside every other condition, visible in the toolbar
// without opening anything, and travels in the link.
export const SET_ASIDE_STATES = ["deferred", "closed"];

// OUTCOME_LABELS covers model.Run's own outcome
// vocabulary (orchestrator.outcomeOf, orchestrator.run's "cancelled",
// orchestrator.runOne's "setup-failed", orchestrator.recoverRun's
// "orphaned"), plus the empty string a run still in flight (no finishedAt
// yet) comes back as -- the one case that isn't itself an outcome. An
// outcome this doesn't recognise falls back to the raw string,
// capitalised, rather than disappearing, so a value added on the backend
// later still shows up before this map catches up with it.
//
// It lives here rather than beside DetailOverlay's own OUTCOME_BADGES,
// which it used to sit with, because two views say what became of an
// attempt now: the task's timeline, and the chat an interactive task's
// conversation is rendered as (chat.js, grain/task-414). The badges are
// still only read by the timeline.
//
// The non-obvious ones are all runs that failed somewhere other than the
// agent's own work, which is exactly why each needs a word of its own:
// "setup-failed" is one whose sandbox could not be built (a VM that never
// booted, a token that could not be minted -- dispatch retries the task
// after its backoff), "orphaned" is one whose process died mid-run and
// which the next startup finished on its behalf (RecoverOrphanedRuns),
// "finish-failed" is one whose agent worked but whose result could not be
// turned into a pull request or a comment (orchestrator.noteFinishFailure
// -- the branch is pushed and nothing points at it), and "no_action" is
// one that ran fine and produced nothing to act on at all
// (orchestrator.ProcessResult). Every one of them takes the "failed"
// badge rather than the fallback's "queued", which would read as "hasn't
// run yet" for a run that did.
//
// "paused" (model.PausedOutcome) is the exception among those: it is a
// run grain stopped because the deployment's agent had no budget left in
// its provider's current window (orchestrator.Pause), so it takes the
// "closed" badge "cancelled" takes rather than a red one. Nothing about
// it failed, and the backend does not count it against the task's own
// failure streak either.
//
// "capacity" (model.CapacityOutcome) is the other one of those: the
// provider was overloaded and refused the run, which is weather rather
// than a fault. Same "closed" badge for the same reason -- the backend
// does not count it against the task's failure streak, and the task is
// simply retried -- and a label that says whose doing it was, since
// telling this apart from a real failure at a glance is the whole point
// of it having a word of its own.
//
// "nothing_needed" (model.NothingNeededOutcome) is a run that looked and
// said, through the nothing_needed tool, that the task needed no change:
// a success, labelled apart from "Succeeded" so a deliberate no-op is
// told from a run that produced something.
export const OUTCOME_LABELS = {
  "": "Running",
  succeeded: "Succeeded",
  failed: "Failed",
  cancelled: "Cancelled",
  paused: "Paused (agent usage limit)",
  capacity: "Provider overloaded",
  "setup-failed": "Setup failed",
  "finish-failed": "Finish failed",
  no_action: "No action",
  nothing_needed: "Nothing needed",
  orphaned: "Orphaned",
  interrupted: "Interrupted (daemon restarted)",
};

// outcome is undefined, not "", for a still-running attempt: the API's
// own Attempt.Outcome carries `omitempty`, so the wire form drops the
// key entirely rather than sending an empty string.
export function outcomeLabel(outcome) {
  outcome = outcome || "";
  return (
    OUTCOME_LABELS[outcome] ||
    outcome.charAt(0).toUpperCase() + outcome.slice(1)
  );
}

// isTaskMerged returns true if the task's pull request has merged, or if the
// task closed because its pull request merged.
export function isTaskMerged(t) {
  if (!t || typeof t !== "object") return false;
  if (t.merged) return true;
  if (t.prMergedAt) return true;
  if (t.closedReason === "merged") return true;
  return (t.pullRequestEvents || []).some((e) => e.kind === "merged");
}
// taskMergedAt is when the task's pull request merged, as the API gave
// it: the PR's own merge time when grain observed one, else the merged
// event in its PR history, else -- for a task that closed because it
// merged but carries neither -- the moment it closed. null for a task
// that has not merged. The Merged panel shows it on every row and sorts
// by it (taskAttributes.js's "merged").
export function taskMergedAt(t) {
  if (!isTaskMerged(t)) return null;
  if (t.prMergedAt) return t.prMergedAt;
  const ev = (t.pullRequestEvents || []).find((e) => e.kind === "merged");
  if (ev?.at) return ev.at;
  return t.closedAt || t.endedAt || null;
}
// isTaskPaused is whether an operator has paused this open task
// (ui.Task.Paused, model.TaskPause). Its state is still its own -- a pause
// changes nothing a state is derived from -- but every label reads
// "Paused", since that is what is true of it until somebody resumes it.
export function isTaskPaused(t) {
  return Boolean(t?.paused) && t.state !== "closed";
}

// pausedDescription is "Paused by alice at <time> (was running)": who,
// when, and what the task was doing, for a paused task's hover text and
// its task page.
export function pausedDescription(t) {
  const p = t?.paused;
  if (!p) return "";
  const at = p.at ? new Date(p.at).toLocaleString() : "";
  const from = p.fromState
    ? ` (was ${STATE_LABELS[p.fromState] || p.fromState})`
    : "";
  return `Paused by ${p.by || "someone"}${at ? ` at ${at}` : ""}${from}`;
}

// stateLabel is STATE_LABELS for one task, plus the single thing a task's
// state cannot say on its own: that the run it is in, or waiting for, is
// the merge queue repairing its own pull request branch rather than
// writing the change (ui.Task.Repairing, model.Observation.
// MergeQueueRepairAt).
//
// The state really is "running" or "queued" -- the task went back to
// working, on the same branch, and that is what the repair *is* now that
// it no longer happens on a branch of its own. But a row that reads
// "Running" days after its pull request opened looks like a task starting
// over, so the label says which kind of work it is, and StateDot colours
// the mark to match.
export function stateLabel(t) {
  if (!t || typeof t !== "object") return STATE_LABELS.queued;
  if (isTaskMerged(t)) return "Merged";
  if (isTaskPaused(t)) return "Paused";
  if (isBlockedWaiting(t)) return "Blocked";
  const label = STATE_LABELS[t.state] || t.state;
  if (!t.repairing && t.interactive && t.state === "running")
    return "Interactive";
  if (!t.repairing) return label;
  if (t.state === "running") return "Repairing";
  if (t.state === "queued") return "Waiting for repair";
  return label;
}

// stateTitle is the hover text for a task's state icon: stateLabel, plus
// the pre-run wait reason when a queued task is waiting on a grant,
// connection, or branch (ui.Task.Activity with ActivityBySetup).
//
// A blocked task's title is blockedDescription instead -- "Blocked on:
// #12 (running)", or "Waiting on: ..." for a task its own run parked --
// so the blocked oval says what it waits on the way the old "Blocked"
// chip beside the badge used to. byId (a Map, array or id-keyed object of
// tasks) lets it name each blocker's state; without it the ids alone are
// named.
//
// A task that still carries an open link but wears its own state's badge
// -- a finished change whose pull request waits in the merge queue on its
// base branch's repair, say -- keeps that link in its title after its
// label ("Completed — Blocked on: #12 (queued)"), since there is no other
// mark left on the row to say it. A running task's link is not what holds
// it up, so its title stays its label.
export function stateTitle(t, byId) {
  if (isTaskPaused(t)) return pausedDescription(t);
  if (isBlockedWaiting(t)) return blockedDescription(t, byId);
  const label = stateLabel(t);
  const asked = askedCase(t);
  if (asked) return asked.title;
  if (t?.state === "queued" && t.activityBySetup && t.activity) {
    // A setup wait is worded "waiting: ...", which after the "Waiting"
    // label would say it twice.
    const why = t.activity.replace(/^waiting:\s*/i, "");
    return why !== t.activity && label === STATE_LABELS.queued
      ? `${label}: ${why}`
      : `${label} — ${t.activity}`;
  }
  if (
    t?.blocked &&
    t.blockedBy?.length > 0 &&
    t.state !== "running" &&
    !isTaskMerged(t)
  ) {
    return `${label} — ${blockedDescription(t, byId)}`;
  }
  return label;
}

// blockedDescription describes the open blocking dependencies for a task,
// including each blocker's current state (e.g. "Blocked on: #105 (cancelled)").
export function blockedDescription(t, byId) {
  if (!t.blockedBy || t.blockedBy.length === 0) {
    return t.parked ? "Waiting" : "Blocked";
  }
  const parts = t.blockedBy.map((id) => {
    const dep =
      byId instanceof Map
        ? byId.get(id)
        : Array.isArray(byId)
          ? byId.find((x) => x.id === id)
          : byId?.[id];
    if (!dep) return `#${id}`;
    let state = dep.state;
    if (isTaskMerged(dep)) {
      state = "merged";
    } else if (state === "closed" && !dep.endedAt) {
      state = "cancelled";
    } else if (STATE_LABELS[state]) {
      state = STATE_LABELS[state].toLowerCase();
    }
    return `#${id} (${state})`;
  });
  // A task its own run parked (wait_for_task) is waiting by choice.
  return `${t.parked ? "Waiting on" : "Blocked on"}: ${parts.join(", ")}`;
}

// completionPhase names what a task whose run is over is waiting on when
// that is not what its state badge already says. There are two such
// cases: a change on a security hold (below), and a pull request the
// merge queue has given up on, which is still
// "Queued for merge" as far as model.State is concerned (StateOf never
// reads a PR's own health) and is on no queue in any useful sense. It is
// a second axis rendered beside the state badge rather than folded into
// it, the same treatment Blocked already gets.
//
// It is the badge-axis half of one state rather than a state of its own:
// the same fact renders on the activity line as merge_failed, whose note
// says which phase of the merge cycle it died in (orchestrator's
// queuestatus.go). The chip says a person owns this now and where the
// button is; the line beside it says what they have been handed.
//
// The other half of the distinction bwsalmon/agents#494 asked for -- a
// task waiting on a human's Submit click -- was a chip here until it
// became a state (STATE_LABELS.awaiting_submit). A chip is a correction
// to the badge beside it, and once the badge says the thing itself there
// is nothing left to correct.
//
// Returns null for a task whose run is not over, for one with no pull
// request (nothing to land, so nothing to be blocked on), and for the
// ordinary case of a pull request sitting on the queue as advertised.
export function completionPhase(t) {
  if (t.state !== "completed" && t.state !== "awaiting_submit") return null;
  if (!t.pullRequest) return null;
  if (t.mergeQueueBlockedAt) {
    return {
      label: "Merge blocked",
      color: "error",
      title:
        "The merge queue gave up on landing this automatically -- its own comment says why. Sort it out by hand and ask it to try again (Retry merge), or close it.",
    };
  }
  // A security hold (ui.Task.SecurityHold, orchestrator's
  // securityhold.go) is the other correction to "Awaiting submit": the
  // change is not waiting on a person so much as on the security review
  // grain filed for it, and merges itself if that review comes back
  // clean. Without the chip it looked like every other change waiting for
  // a Submit click (docs/test-runs/2026-10-01-security-hold-escalated-
  // proposals.md, finding 3).
  if (t.securityHold) {
    const review = t.securityReviewTask
      ? `security review (task ${t.securityReviewTask})`
      : "security review";
    return {
      label: "Security hold",
      color: "warning",
      title: `This change touches grain's trust boundary (${t.securityHold}), so it is not merging automatically until its ${review} finishes without findings. Submit merges it without waiting.`,
    };
  }
  return null;
}

// runActivity is what a task's live run is doing right now
// (ui.Task.Activity), or null when there is nothing to show.
//
// The state check is the whole of the filtering. The API only ever
// carries a synopsis for a task with a live run, but a poll's answer is
// up to POLL_INTERVAL_MS old and a task that has just finished would
// otherwise keep showing what it was doing a moment before it stopped --
// which reads, next to a "Completed" badge, as a run still going.
//
// age is the same phrase's other half: "waiting for CI" said ten seconds
// ago and the same words left standing for an hour mean opposite things,
// and only the age tells them apart. It is null for a synopsis with no
// timestamp -- a row written by an older build -- in which case the note
// is shown alone rather than with a made-up age, and null again while the
// phrase is still fresh: see activityAge.
//
// since is that same age with no threshold under it ("now", "3m", "22m"),
// for the tooltip. A row has a width to spend and spends it only where
// the age says something; a hover has none, and "since when?" is the
// question somebody hovering is asking -- so the answer is always there
// even for a line too fresh to be worth stamping on the row.
//
// bySetup says the phrase is grain's own rather than the agent's: what
// the dispatch path is doing to get this run started, over the minutes
// before there is an agent to say anything (ui.Task.ActivityBySetup,
// orchestrator.setupNotes). Everything else that appears here is
// something an agent wrote, so the two are marked apart rather than left
// to be told apart by their wording.
//
// byQueue is the third voice, and the reason this function answers at all
// for a task whose run is over: the merge queue, landing the change the
// run pushed. Its phrase comes from a fixed vocabulary
// (MERGE_QUEUE_PHRASES) rather than from anybody's sentence, and detail
// is the queue's own account of it -- which check it is waiting on, which
// task is ahead of this one, what GitHub said when it refused the merge.
//
// The queue's voice comes second, behind the agent's, and only that
// order is safe. A merge-queue state is written each cycle for a task
// the queue is watching and is only ever taken back by another cycle
// (orchestrator's clearStaleQueueState), so it can be minutes old, or --
// for a task that left the watched set between two cycles -- describe a
// round of the queue that is over. A live run's synopsis cannot be
// either: it was written by the run this row is about, which is
// happening now. So where both are there, the one that is happening wins
// -- "Merging" over a branch an agent is pushing to is grain describing
// a decision it is explicitly not making (queuestatus.go's statusOf says
// the same thing, in the states it writes).
//
// Only against a *live* run, though: a task that is queued shows the
// queue's phrase over grain's own setup line, because "Queued for
// repair" is the fact about that row and the setup phrase is the plumbing
// underneath it.
export function runActivity(t, now = Date.now()) {
  const liveRun = t.state === "running" && !!t.activity;
  if (
    !liveRun &&
    t.state !== "closed" &&
    MERGE_QUEUE_PHRASES[t.mergeQueueState]
  ) {
    return {
      note: MERGE_QUEUE_PHRASES[t.mergeQueueState],
      // The queue's own sentence about this wait (Task.MergeQueueNote):
      // which check, which task ahead of this one, what GitHub said when
      // it refused. Shown beside the phrase where there is room for it
      // and in the tooltip where there is not.
      detail: t.mergeQueueNote || null,
      // Which of the state's ways of waiting this is (ui.Task.
      // MergeQueueReason), said beside the phrase and explained on hover.
      ...(t.mergeQueueReason ? { reason: t.mergeQueueReason } : {}),
      age: activityAge(t.mergeQueueStateAt, now, GRAIN_ACTIVITY_AGE_AFTER_MS),
      since: t.mergeQueueStateAt ? relativeAge(t.mergeQueueStateAt, now) : null,
      bySetup: true,
      byQueue: true,
    };
  }
  // A finite grant wait is grain's own pre-run activity. It deliberately
  // leaves the task queued, because no attempt or sandbox exists yet.
  if (
    !t.activity ||
    (t.state !== "running" && !(t.state === "queued" && t.activityBySetup))
  )
    return null;
  const bySetup = !!t.activityBySetup;
  return {
    note: t.activity,
    age: activityAge(
      t.activityAt,
      now,
      bySetup ? GRAIN_ACTIVITY_AGE_AFTER_MS : RUN_ACTIVITY_AGE_AFTER_MS,
    ),
    since: t.activityAt ? relativeAge(t.activityAt, now) : null,
    bySetup,
  };
}

// questionActivity is the status line of a task parked on a question:
// the question's one-line summary (ui.Task.QuestionSummary), so a row in
// the task list says what it was asked as well as that it was asked. Null
// for any other task, and for a stopped turn, which asked nothing.
export function questionActivity(t) {
  if (t.state !== "awaiting_reply" || t.turnStopped || !t.questionSummary)
    return null;
  return { note: t.questionSummary, byQuestion: true };
}

// How old one of these phrases has to be before the row says how old it
// is, in milliseconds. Two numbers, one for each voice, and every place
// that renders an activity line shares them -- a row, a board card and
// the detail pane disagreeing about whether a line has gone stale would
// be worse than either threshold, since the pane is the page somebody
// opens *because* the row said something.
//
// RUN_ACTIVITY_AGE_AFTER_MS is for an agent's own line. A live run now
// narrates every tool call it makes (mcp.Registry.AnnounceActivity), so
// the line is rewritten several times a minute by anything that is
// working; a line that has stood five minutes means the run is inside one
// long call -- a test suite, a wait on CI, a chat nobody has answered --
// or that nothing is driving it at all. Both are worth dating, and
// neither is visible from the words themselves.
//
// GRAIN_ACTIVITY_AGE_AFTER_MS is for grain's own voice: a setup phrase
// (RunActivity.BySetup) and the merge queue's. It is the longer of the
// two on purpose. "building a sandbox" is a VM boot, "running the repo's
// setup command" is bounded only by the orchestrator's own ten-minute
// timeout, and "Waiting for CI" is a suite that takes half an hour here
// -- those lines standing for minutes is them working, not them stuck, so
// dating them early would cry wolf on every dispatch. Past ten minutes a
// setup phrase has outlived the longest step it can describe, and the age
// is the fact somebody wants.
export const RUN_ACTIVITY_AGE_AFTER_MS = 5 * 60 * 1000;
export const GRAIN_ACTIVITY_AGE_AFTER_MS = 10 * 60 * 1000;

// activityAge is relativeAge with a threshold under it and the word "ago"
// on top: "22m ago", or null while the phrase is younger than after.
//
// Both halves are about what the row is for. A phrase that was written a
// moment ago is current, which is what a reader assumes of it anyway, so
// stamping every one of them with "now" spends width on every running
// task to say nothing about any of them -- the chip appearing at all is
// the signal, and it appears only where there is something to signal. And
// the age says "ago" rather than standing as a bare "22m" beside a
// sentence, because a number next to "waiting for CI" is as easily read
// as how long the wait has left to run as how long it has already stood.
export function activityAge(iso, now, after) {
  if (!iso) return null;
  const at = new Date(iso).getTime();
  if (!Number.isFinite(at) || now - at < after) return null;
  const age = relativeAge(iso, now);
  return age && age !== "now" ? `${age} ago` : null;
}

// activityTitle is the tooltip that goes with one of those phrases,
// shared by the three places a task's activity is rendered (a row, a
// board card, the detail pane) so that the answer to "who is saying this,
// and since when?" is the same in all of them.
//
// The detail comes first because it is the substance -- "behind task 131
// in bwsalmon/grain" is the answer somebody hovering wants, and the rest
// is provenance.
export function activityTitle(a) {
  const whose = a.byQuestion
    ? `Asked: ${a.note}`
    : a.byQueue
      ? "What grain's merge queue is doing about landing this change"
      : a.bySetup
        ? "What grain is doing to get this run started"
        : "What this run says it is doing";
  // Off a.since rather than a.age: the row shows an age only once it has
  // something to say with it, but a hover is where "since when?" is being
  // asked outright, and answering "it is recent enough that we didn't say"
  // is not an answer.
  const age = a.since
    ? `, as of ${a.since === "now" ? "just now" : `${a.since} ago`}`
    : "";
  const reason = a.reason ? `${reasonTitle(a.reason)}\n\n` : "";
  return `${a.detail ? `${a.detail}\n\n` : ""}${reason}${whose}${age}`;
}

// What each of the merge queue's own states is called on a row
// (Task.MergeQueueState, orchestrator's queuestatus.go). A state this
// build has no phrase for renders as no activity at all rather than as
// its raw name, so a newer daemon can add one without a browser running
// an older build showing somebody "waiting_thing".
//
// pkg/mcp keeps the same table word for word, so that an agent asked
// about a task describes the row in the words its operator can see
// (mergeQueuePhrases). The two are edited together: pkg/mcp's
// merge_queue_phrases_test.go reads this object off disk and fails when
// they differ, or when either has fallen behind the states themselves.
const MERGE_QUEUE_PHRASES = {
  waiting_ci: "Waiting for CI",
  waiting_queue: "Waiting on Queue",
  waiting_review: "Waiting for its review",
  merging: "Merging",
  updating_branch: "Bringing the branch up to date",
  batching: "Testing Batch",
  gathering: "Held to be tested with the next change",
  checks_unfixable: "Waiting: its checks can't pass from a branch",
  waiting_github_approval: "Waiting for an approval GitHub requires",
  github_merge_queue: "In GitHub's merge queue",
  repair_needed: "Needs repairing",
  repair_queued: "Queued for repair",
  repairing: "Resolving Conflicts",
  agent_working: "An agent is on this branch",
  merge_failed: "Merge failed",
  unreadable: "Can't read the pull request",
};

// The states in which grain is actively landing the change, as opposed to
// the two in which it is stuck on something: those get the live mark on
// the row (a running dot beside a completed badge, which is what "grain
// is working on this now" looks like here), and the stuck ones
// deliberately do not. A merge that failed and a pull request nobody can
// read are exactly the states that used to be dressed up as progress, and
// dressing them up again is the one thing this must not do.
//
// The three repair states are live: the change is going round the merge
// cycle's second phase (orchestrator's queuestatus.go), which is somebody
// working on it even in the round where that somebody is a worker this
// deployment has not freed up yet. So is agent_working, which is that
// same phase entered by a person putting an agent back on the task
// (RunNow) rather than by the queue filing a repair.
//
// Membership here is the static half of the question and not the whole of
// it: a state grain drives can still be one nothing is getting anywhere
// with, and mergeQueueActive takes the mark back off a row the daemon has
// flagged as stalled (mergeQueueStalled below).
// The merge-queue states in which grain is actively doing work (merging,
// running CI, updating branch, batching), as opposed to states where it is
// waiting in a queue (waiting_queue, waiting_review, gathering, repair_queued,
// repair_needed) or stuck/failed (merge_failed, unreadable).
//
// Things waiting in a queue do not get an animation, and active merge queue
// work gets the normal CSS spinner rather than the agent GrainMark.
const MERGE_QUEUE_LIVE = new Set([
  "waiting_ci",
  "merging",
  // grain is writing to the branch itself, which is the same kind of
  // "grain is doing this now" merging is -- it happens only where a
  // repository has asked for its branches to contain their base
  // (orchestrator's bringHeadUpToDate).
  "updating_branch",
  // grain is running one suite over this change and the others at the
  // front of its repository's queue, on a throwaway ref of its own
  // (orchestrator's reconcileMergeBatches). It is grain driving, and the
  // run that ends it lands this change and several others at once.
  "batching",
]);

// A merge-queue observation can briefly outlive the pull request it
// describes. Once the task is closed, its terminal state wins over that
// stale observation everywhere the UI renders merge activity.
//
// And a state grain has flagged as stalled loses the mark too, for the
// reason the set above is a subset at all. The daemon knows precisely how
// long each of these states can plausibly last -- every one of them names
// the component that ought to be ending it, with a patience beside it
// (orchestrator's liveness.go) -- and stamps the moment an entry outlived
// that (Task.MergeQueueStalledAt). Until now that judgement reached only
// the sentence in the tooltip and the detail pane, so a stalled row went
// on wearing the running dot and went on reading, to anybody scanning the
// list, as a change being landed. The words said stuck and the mark said
// working; the mark is what gets read first.
//
// The phrase itself stays ("Waiting for CI" is still what the queue is
// nominally doing, and the note beside it is the account of why that has
// stopped meaning anything): what goes is the claim that something is
// happening.
export function mergeQueueActive(t) {
  if (!t || typeof t !== "object") return false;
  return (
    t.state !== "closed" &&
    !mergeQueueStalled(t) &&
    MERGE_QUEUE_LIVE.has(t.mergeQueueState)
  );
}

// isAgentRunning returns whether an agent is actively running on this task
// (i.e. state is running, and past grain's setup phase). Only an active agent
// receives the animated GrainMark brand mark.
export function isAgentRunning(t) {
  if (!t || typeof t !== "object") return false;
  return t.state === "running" && !t.activityBySetup;
}

// isGrainActive returns whether grain itself is doing active work for this task
// (sandbox/repo setup during run, or merge queue active operations). Active grain
// work receives the standard CSS spinner instead of the GrainMark.
export function isGrainActive(t) {
  if (!t || typeof t !== "object") return false;
  if (t.state === "running" && t.activityBySetup) {
    return true;
  }
  return mergeQueueActive(t);
}

// BLOCKABLE_STATES are the states in which an open dependency is what a
// task is actually waiting on: it has not started (queued) or has not
// been approved yet (proposed). A task that is running, finished or
// closed may still carry an open depends_on link (ui.Task.Blocked is
// derived from the links alone, model.IsBlocked), but that link is not
// what holds it up any more, so its badge keeps saying its own state.
const BLOCKABLE_STATES = new Set(["queued", "proposed"]);

// isBlockedWaiting reports whether a task should wear the blocked state
// oval rather than its own state's: it is blocked (an open depends_on or
// child-of link) and still waiting to start. Grain doing active work on it
// (setup, merge queue), a merged change and a merge-queue repair all win,
// since each of those is something happening rather than a wait.
export function isBlockedWaiting(t) {
  if (!t || typeof t !== "object" || !t.blocked) return false;
  if (!BLOCKABLE_STATES.has(t.state)) return false;
  if (t.repairing || isGrainActive(t) || isTaskMerged(t)) return false;
  return true;
}

// taskBadgeState returns the badge state class name to use for a task.
// When grain is actively doing work (merging, running CI, setup), it uses
// "running" to drive the normal CSS spinner. A task waiting on another
// task (isBlockedWaiting) is "blocked". Otherwise it uses the task's state
// or outcome (merged).
export function taskBadgeState(t) {
  if (!t || typeof t !== "object") return "queued";
  if (isGrainActive(t)) {
    return "running";
  }
  if (isTaskMerged(t)) {
    return "merged";
  }
  if (isBlockedWaiting(t)) {
    return "blocked";
  }
  return t.state;
}

// isTaskRepairing returns whether a task is undergoing merge-queue repair
// or active merge operations (where its spinner or mark is blue).
export function isTaskRepairing(t) {
  if (!t || typeof t !== "object") return false;
  return Boolean(t.repairing || mergeQueueActive(t));
}

// taskBadgeModifier returns modifier class names for a task's state badge/spinner:
// blue ("badge-repair") for merge stage and repairs,
// violet ("badge-interactive") for interactive runs.
export function taskBadgeModifier(t) {
  if (!t || typeof t !== "object") return "";
  if (isTaskRepairing(t)) {
    return "badge-repair";
  }
  if (t.interactive) {
    return "badge-interactive";
  }
  return "";
}

// mergeQueueStalled is grain's own verdict on a merge-queue wait: this
// entry has been in its state longer than whatever drives that state
// should plausibly take, so it is stuck rather than waiting
// (Task.MergeQueueStalledAt, orchestrator's liveness.go).
//
// It is read off the stamp the daemon writes rather than computed here on
// purpose. The patiences belong next to the drivers they are about --
// that was the whole finding behind the stall detector, that the state
// and the driving lived in different places with nothing holding them
// together -- and a second copy of the budgets in the browser would
// recreate exactly that divergence in a new place, silently, the first
// time one of them changed.
//
// Closed wins over it, the same way it wins over the live mark: a
// merge-queue observation can outlive the pull request it describes, and
// a task whose own state is terminal has no live queue row to flag.
export function mergeQueueStalled(t) {
  return t.state !== "closed" && !!t.mergeQueueState && !!t.mergeQueueStalledAt;
}

// awaitsGitHubApproval is a merge-queue entry GitHub refused to merge
// until somebody approves the pull request (orchestrator's
// githubrefusal.go). The queue does not mark it blocked -- it lands the
// change by itself once the approval arrives -- but nothing in grain
// will give that approval, so it is a wait on a person all the same.
// A change red only where its base branch is red is deliberately not
// here: it depends on the base branch's repair task, a link its row
// names ("blocked on #N"), and nobody is asked to act on it.
export function awaitsGitHubApproval(t) {
  return (
    t.state !== "closed" && t.mergeQueueState === "waiting_github_approval"
  );
}

// mergeQueueStall is that verdict as the chip beside the state badge, or
// null where there is nothing to say -- completionPhase's shape, and
// rendered in the same place, because it is the same kind of thing: a
// correction to the badge, on the axis of "who is getting this landed".
//
// It is a chip as well as the missing mark because the two say different
// halves. Taking the mark off stops the row claiming progress it is not
// making; only the chip says outright that somebody should look. Its
// title is deliberately about what to do next -- the queue's own note,
// already on the activity line beside it, is what says which check, which
// task ahead of it, which branch.
export function mergeQueueStall(t) {
  if (!mergeQueueStalled(t)) return null;
  return {
    label: "Stalled",
    color: "warning",
    title:
      "This has stood in one merge-queue state for longer than whatever drives that state should take, so grain reads it as stuck rather than waiting. The line beside it says what it is nominally waiting for and where to look.",
  };
}

// reasonLabel is a liveness sub-case (ui.Reason, orchestrator's
// livenessreasons.go) as a row's chip says it: the part after the state,
// which the badge beside it already names, in words -- "usage limit
// paused" for "queued/usage_limit_paused". null for no sub-case.
export function reasonLabel(r) {
  const reason = r?.reason;
  if (!reason) return null;
  const at = reason.indexOf("/");
  return (at < 0 ? reason : reason.slice(at + 1)).replaceAll("_", " ");
}

// reasonTitle is the sub-case's hover: its full name, which of the three
// conditions it is in, and what it waits on -- the "why" the chip has no
// room for.
export function reasonTitle(r) {
  if (!r?.reason) return "";
  const lines = [r.reason];
  if (r.class === "driven" && r.driver) {
    lines.push(`Driven by ${r.driver}`);
  } else if (r.class) {
    lines.push(r.class.charAt(0).toUpperCase() + r.class.slice(1));
  }
  if (r.resource) lines.push(`Waiting on ${r.resource}`);
  if (r.control) lines.push(`"${r.control}" ends this wait`);
  return lines.join("\n");
}

// REASON_CONTROLS is each sub-case control a row can press for itself:
// the request it makes, as the detail pane's own button of the same name
// makes it (detail/Actions.jsx). The rest -- Approve, Reply, Submit,
// Retry, Reopen -- are their own inbox groups' answers already.
const REASON_CONTROLS = {
  "Run now": {
    post: "/run-now",
    title: "Put an agent on this task next, ahead of the queue.",
  },
  Cancel: {
    post: "/close",
    color: "error",
    confirm: (t) =>
      `Cancel ${t.id}? The run in flight is stopped and the task is closed; it can be reopened later.`,
    title: "Stop the run in flight and close the task. Reopen puts it back.",
  },
  Undefer: {
    post: "/undefer",
    title: "Put this task back in the queue now rather than at its wake time.",
  },
  "Retry merge": {
    post: "/retry-merge",
    title: "Put this pull request back on the merge queue.",
  },
};

// reasonControl is the button that ends this task's wait, as a row
// offers it ({label, post, ...}), or null where its sub-case names no
// control a row can press. A merge-queue sub-case's own control (Retry
// merge on merge_failed) stands in when the task's sub-case has none.
export function reasonControl(t) {
  for (const r of [t?.reason, t?.mergeQueueReason]) {
    const action = r?.control && REASON_CONTROLS[r.control];
    if (action) return { label: r.control, ...action };
  }
  return null;
}

// mergeQueueBatching is the one merge-queue state that is about more than
// this task: grain is testing it together with the other changes at the
// front of its repository's queue, on a throwaway ref of its own. It is
// singled out because the batch is a thing with a page's worth to say
// about it (MergeBatchPanel, GET /api/merge-batches) and this is what
// decides whether to go and ask -- a task in no batch should not be
// making that call on every open.
export function mergeQueueBatching(t) {
  return t.state !== "closed" && t.mergeQueueState === "batching";
}

// relativeAge renders how long ago an ISO timestamp was, as short as it
// can be said: "now" under a minute, then minutes, then hours, then days.
// It is deliberately coarse -- this sits beside a task title, where the
// question is "is this current?" rather than "how current?" -- and never
// renders a future time as a negative, since a clock skewed by a second
// between the daemon and the browser is not worth showing anybody.
export function relativeAge(iso, now = Date.now()) {
  const seconds = Math.max(
    0,
    Math.round((now - new Date(iso).getTime()) / 1000),
  );
  if (!Number.isFinite(seconds)) return null;
  if (seconds < 60) return "now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

// waitsOnUser is the question the inbox asks of one task: is grain done
// with this until a person does something about it? (InboxPage.jsx,
// grain/task-20.)
//
// Five states say yes on their own. "Awaiting reply", "Awaiting submit"
// and "Awaiting ack" are the three a task never leaves by itself
// (STATE_LABELS' own wording for all three) -- a question to answer, a
// change to submit, an answer to read. "Failed" is the fourth in
// practice: dispatch has given up after model.MaxConsecutiveFailures,
// and Retry is the only way out. "Proposed" is the fifth -- an agent's
// own propose_task filed it and nothing dispatches it until somebody
// approves it -- and it is the one whose wait is easy to miss, because a
// proposal that nobody ever looks at simply sits in the backlog looking
// like queued work.
//
// The sixth is not a state at all: a completed task the merge queue has
// given up on (completionPhase above). Its badge still reads "Queued for
// merge" and it is on no queue, so of everything here it is the case a
// human is least likely to find by scrolling the list.
//
// The seventh is not a state either, and it is the opposite kind of
// thing: a queue entry grain has flagged as stalled (mergeQueueStalled
// above). Nothing has been escalated, nobody has commented, and the
// queue still believes it is driving -- which is exactly why it belongs
// here. A wait nothing is getting anywhere with is otherwise invisible
// unless somebody happens to scan the list for it, and the stall
// detector's whole finding was that such a wait can stand for hours
// with everything on screen saying a change is on its way. The reader's
// job on one of these is to go and look at what the queue's note names
// rather than to press anything, and the inbox is where this deployment
// says "somebody has to deal with this" (InboxPage's merge_stalled
// group).
//
// The eighth is the seventh one lane over: a task whose *own* state has
// stood for longer than that state should take (ui.Task.Stall,
// orchestrator's taskLane -- a queued task nothing is dispatching, a run
// no process is finishing, a change the merge queue has not landed at
// all). Same argument, one step earlier in the life cycle and with less
// to go on: nothing about such a task looks wrong from the outside, the
// badge says "Queued" and means it, and the only thing separating it
// from the forty healthy queued tasks around it is how long it has said
// so. Its row offers nothing to press either, for the seventh's reason
// (InboxPage's stalled group).
//
// Deliberately not here: "blocked". A blocked task is waiting on another
// task, not on a person -- it starts on its own the moment what it
// depends on lands -- and putting it here would fill the inbox with rows
// there is nothing to do about.
//
// Nor "deferred", and that one needs no rule of its own: deferring is
// already an answer, and model.StateOf reads a deferred proposal as
// deferred rather than as a proposal, so a "not now" takes the task off
// this list by saying the thing the list was asking for.
// waitingBaseRepair reports whether t is a base-branch repair task with an open
// pull request waiting on anything while its target base branch is red.
export function waitingBaseRepair(t) {
  if (!t || typeof t !== "object") return false;
  if (
    t.state === "closed" ||
    t.state === "running" ||
    t.mergeQueueState === "merging" ||
    isTaskMerged(t)
  ) {
    return false;
  }
  return Boolean(t.redBaseBranch && t.pullRequest);
}

// baseRepairWaitReason describes why a base-branch repair pull request is waiting.
export function baseRepairWaitReason(t) {
  if (!t || typeof t !== "object") return "waiting";
  if (!t.autoMerge) {
    const pendingApprovals = (t.approvals || []).filter((a) => !a.approvedBy);
    if (pendingApprovals.length > 0) {
      const rules = pendingApprovals
        .map(
          (a) => `${a.rule} (${(a.approvers || []).join(" or ") || "human"})`,
        )
        .join(", ");
      return `waiting for human approval on ${rules}`;
    }
    if (t.pathTriggerHold) {
      return `held by repository gating triggers (human: ${t.pathTriggerHold}) — waiting for Submit`;
    }
    if (t.securityHold) {
      const review = t.securityReviewTask
        ? ` for security review #${t.securityReviewTask}`
        : "";
      return `on security hold (${t.securityHold})${review} — waiting for Submit`;
    }
    if (t.reviewQuestions) {
      return `waiting on review questions from #${t.reviewQuestions}`;
    }
    if (t.state === "awaiting_reply") {
      return "waiting for a reply to its question";
    }
    if (t.state === "failed") {
      return "failed and waiting for retry";
    }
    if (t.state === "proposed") {
      return "waiting for approval";
    }
    return "awaiting submit";
  }
  if (t.mergeQueueBlockedAt) {
    return t.mergeQueueNote
      ? `blocked on the merge queue (${t.mergeQueueNote})`
      : "blocked on the merge queue";
  }
  if (awaitsGitHubApproval(t)) {
    return t.mergeQueueNote
      ? `waiting for GitHub approval (${t.mergeQueueNote})`
      : "waiting for GitHub approval";
  }
  if (mergeQueueStalled(t)) {
    return t.mergeQueueNote
      ? `stalled in the merge queue (${t.mergeQueueNote})`
      : "stalled in the merge queue";
  }
  if (t.stall) {
    return `stalled (${t.stall})`;
  }
  if (t.state === "awaiting_reply") {
    return "waiting for a reply to its question";
  }
  if (t.state === "failed") {
    return "failed and waiting for retry";
  }
  if (t.mergeQueueNote) {
    return `waiting in the merge queue (${t.mergeQueueNote})`;
  }
  if (t.mergeQueueState && MERGE_QUEUE_PHRASES[t.mergeQueueState]) {
    return `waiting in the merge queue (${MERGE_QUEUE_PHRASES[t.mergeQueueState].toLowerCase()})`;
  }
  if (t.state === "queued") {
    return t.repairing ? "queued for repair" : "queued";
  }
  return "waiting in the merge queue";
}

// redBaseRepairSentence formats the banner and inbox sentence for a waiting
// base-branch repair pull request: "`main` is red — repair #NNNN is …".
export function redBaseRepairSentence(t) {
  if (!waitingBaseRepair(t)) return null;
  return `\`${t.redBaseBranch}\` is red — repair #${t.id} is ${baseRepairWaitReason(t)}`;
}

// awaitsHumanSubmit is a task whose pull request is owed a person's
// Submit: one in awaiting_submit, and one whose run is still going --
// typically blocked in wait_for_checks -- having opened a pull request it
// will not merge by itself (no auto-merge), which has neither merged nor
// closed. Without the second half such a change was invisible to the
// inbox for as long as its run lasted, though nothing would land it until
// somebody pressed Submit.
export function awaitsHumanSubmit(t) {
  if (!t || typeof t !== "object") return false;
  if (t.state === "awaiting_submit") return true;
  return (
    t.state === "running" &&
    !t.autoMerge &&
    Boolean(t.pullRequest) &&
    !t.prClosedAt &&
    !isTaskMerged(t)
  );
}

export function waitsOnUser(t) {
  // A standing session is never inbox work (grain/task-415). Its task
  // is parked on its own conversation between exchanges -- which is what
  // keeps the chat answerable -- so it reads as "awaiting reply" for as
  // long as nobody is typing into it, and an inbox that admitted it
  // would carry one permanent row per session that nothing ever clears.
  // The chat itself is in the rail, which is where a session is
  // answered.
  if (t.consoleSession) return false;
  if (waitingBaseRepair(t)) return true;
  if (awaitsHumanSubmit(t)) return true;
  if (INBOX_STATES.includes(t.state)) return true;
  if (awaitsGitHubApproval(t)) return true;
  if (mergeQueueStalled(t)) return true;
  if (t.stall) return true;
  return t.state === "completed" && completionPhase(t) !== null;
}

// INBOX_STATES is waitsOnUser's own list of states, in no particular
// order -- the inbox decides what order to show its groups in
// (InboxPage.jsx's GROUPS), which is a question about urgency rather
// than about the life cycle STATE_ORDER describes.
export const INBOX_STATES = [
  "awaiting_reply",
  "awaiting_submit",
  "failed",
  "proposed",
];

// inboxTasks is every task waitsOnUser admits, in the order they were
// given -- the backlog's own order (ui.Client.ListTasks), the same order
// every other list of tasks starts from.
export function inboxTasks(tasks) {
  return (tasks || []).filter(waitsOnUser);
}

// inboxGates is the one thing on this page that is not a task: the base
// branches this deployment's merge queue is merging onto that nothing is
// judging (GET /api/base-branch-gates, pkg/ui/basebranchgate.go,
// grain/task-470).
//
// Every row the server sends is a finding -- a gate that is working
// produces no row at all -- so there is nothing to filter here the way
// waitsOnUser filters tasks. What this does is put them in the order the
// inbox wants rather than the order the health pane wants. The server
// sorts by repository, which keeps a redrawing pane from shuffling its
// own alerts; a reader with a list of things to deal with wants the
// worst first, which is worseBaseBranchGate's rule in the browser:
// a branch nothing judges at all before one that is merely late, and
// among two of a kind the one that has been waiting longer.
//
// All four findings belong here, not only the unjudged one. A run that
// will never conclude, a merge nothing has started a run for, and a run
// that finished having judged nothing all end in the same sentence as
// the settings-shaped case -- the queue is merging onto a commit nothing
// has judged -- and they clear themselves the moment a run judges the
// branch, so none can sit in this list once the thing it is about has
// been dealt with.
export function inboxGates(gates) {
  return [...(gates || [])]
    .filter((g) => g && typeof g.repo === "string")
    .sort(
      (a, b) =>
        Number(!!a.judged) - Number(!!b.judged) ||
        (b.lastRunAgeMS || 0) - (a.lastRunAgeMS || 0) ||
        a.repo.localeCompare(b.repo) ||
        (a.branch || "").localeCompare(b.branch || ""),
    );
}

// inboxGateKey is one gate's identity, for a React key and for anything
// else that has to tell two rows apart. A gate is one (repo, branch)
// pair, and the branch is empty on the one finding that is about a
// branch grain never resolved a name for.
export function inboxGateKey(gate) {
  return `${gate.repo} ${gate.branch || ""}`;
}

// inboxCount is the number the rail's Inbox chip carries: actionable
// tasks waiting on the user.
export function inboxCount(tasks) {
  return inboxTasks(tasks).length;
}

// pullRequestUrl turns "owner/name#123" (t.pullRequest, straight off
// model.PullRequestRef.String()) into the surface that owns it: the
// config repository's native comparison page for grain/config
// (model.IsConfigRepo, the only one there is), or GitHub for an external
// repo. It lived
// in DetailOverlay.jsx until the inbox needed the same link on a row
// (InboxPage.jsx) -- two views spelling out github.com/.../pull/... for
// themselves is one of them getting it wrong the first time the ref's
// own shape changes.
export function pullRequestUrl(ref, branch = "", base = "") {
  const [repo, number] = ref.split("#");
  if (isConfigRepoName(repo)) {
    const head = branch || `grain/task-${number}`;
    return internalPullRequestUrl("grain/config", head, base || "main");
  }
  return `https://github.com/${repo}/pull/${number}`;
}

// internalPullRequestUrl is grain's own pull request for a task branch:
// the comparison of that branch against its base, on a page of its own
// (PullRequestPage.jsx, grain/task-375) under the repository it belongs
// to. It is what a task links to where there is no GitHub pull request to
// link to instead -- a branch nobody has opened one for yet, and every
// change to the controller's own config repository, which never reaches
// GitHub at all.
//
// Leave an undeclared base out: the server resolves it from the
// repository, so this works for repos whose default branch is not `main`.
export function internalPullRequestUrl(repo, branch, base = "") {
  return `/repos/${repo}/compare/${base ? `${base}...${branch}` : branch}`;
}

// repoUrl turns "owner/name" or "host/owner/name" -- the way a repo is
// spelled in grain -- into its page on the git host.
export function repoUrl(repo) {
  if (!repo) return "";
  const parts = repo.split("/");
  if (parts.length === 3) {
    return `https://${parts[0]}/${parts[1]}/${parts[2]}`;
  }
  return `https://github.com/${repo}`;
}

// orphanedPullRequest names the pull request a closed task has left open
// behind it, or null when there is none to name.
//
// grain only ever merges a *completed* task's pull request (model.Store.
// OpenPullRequestLinks), and it closes one only when whoever closed the
// task asked for that in the same breath (ui.CloseOptions.
// ClosePullRequest, the checkbox closablePullRequest below is for) -- so
// a task closed without that ask, while its pull request was still open,
// leaves that pull request on GitHub with nobody watching it. It is a
// deliberate outcome, and the point of surfacing it here is that it is
// otherwise invisible: the task is closed, so nobody looks at it, and the
// pull request has merely gone quiet. grain also says so on the task and
// on the pull request itself (model.OrphanedPullRequestNote); this is the
// same fact where the person who just clicked Close is actually looking.
//
// The events check is what keeps it from crying wolf on the ordinary
// ending: a task whose pull request *merged* is closed too (orchestrator.
// recordPullRequestEvents sets ClosedAt alongside PrMergedAt), and its
// link is the most normal thing in the world. It is also what takes this
// warning away once a pull request grain closed on the way out has been
// synced back as closed.
export function orphanedPullRequest(t) {
  if (t.state !== "closed" || !t.pullRequest) return null;
  return pullRequestFinished(t) ? null : t.pullRequest;
}

// closablePullRequest names the pull request that closing this task right
// now would orphan -- the same pull request orphanedPullRequest names, a
// moment earlier, while there is still a choice about it.
//
// It is what decides whether the Close button is offered the checkbox
// that closes the pull request too. Offered only when there is really one
// open to close: a task with no pull request, or one that already merged
// or closed, has nothing for that choice to act on, and an option with
// nothing behind it invites a tick that means nothing.
export function closablePullRequest(t) {
  if (t.state === "closed" || !t.pullRequest) return null;
  return pullRequestFinished(t) ? null : t.pullRequest;
}

// pullRequestFinished is the shared half of the two above: a pull request
// that merged or closed on GitHub is done with, whichever way it went.
function pullRequestFinished(t) {
  return (t.pullRequestEvents || []).some(
    (e) => e.kind === "merged" || e.kind === "closed",
  );
}

export function capabilityName(config, id) {
  const { label, args } = describeGrant(id, { config });
  return args ? `${label} · ${args}` : label;
}

// RETIRED_CAPABILITY_HINT labels a capability that is selected somewhere
// but no longer offered -- worded as an instruction because turning it
// off is the only thing its row is there for (capabilityRows below).
export const RETIRED_CAPABILITY_HINT =
  "No longer offered -- untick to remove it";

// configuredCapabilities is the half of an offered listing that belongs
// in a picker: what this deployment has actually configured, plus
// anything already ticked whatever state it is in.
//
// A capability grain ships a provider for but this deployment has not
// set up -- no GCP project, no secret pasted -- used to be offered
// everywhere with a warning under it (capabilityUnavailableHint). That
// is a row nobody can do anything useful with in the pane they are
// standing in: ticking it files work that fails to dispatch, and the fix
// is on Settings > Legacy capabilities either way, which is the one pane that
// still lists every capability with its own reason for not being ready.
// So a picker hides it instead of explaining it.
//
// Already-selected ids survive the filter, for the reason capabilityRows
// below adds rows for retired ones: a MUI multiple Select can only
// deselect a value through that value's own MenuItem, so hiding one a
// task, template, schedule or defaults set already holds would be a
// grant nothing could take off again. It also keeps the honest order of
// doing things available -- tick gcp-key on the task, then paste the
// secret -- for anything already ticked, warning included.
//
// `ready === false` is the only thing hidden, never a falsy one:
// readiness is computed per deployment (ui.Capability.Ready) and an
// endpoint or a build that computes none at all leaves the field absent.
// "Unknown" has to mean "offer it", or a listing carrying no readiness
// would render as an empty picker.
export function configuredCapabilities(offered, selected) {
  const held = new Set(selected || []);
  return (offered || []).filter((c) => c.ready !== false || held.has(c.id));
}

// NO_CAPABILITIES_HINT is what a picker says when configuredCapabilities
// has left it with nothing at all: a deployment that has configured none
// of them, which is a perfectly ordinary state for a new one. An empty
// menu reads as a broken pane, and the pane that is not empty -- the one
// listing every capability with what it still needs -- is worth naming.
export const NO_CAPABILITIES_HINT =
  "None configured -- set one up in Settings > Legacy capabilities";

// LEGACY_CAPABILITIES_HINT sits under every in-process capability
// checklist: those ids are being converted to capability server methods
// or api scopes (docs/design/unified-grants.md), so new access belongs
// under Grants rather than here.
export const LEGACY_CAPABILITIES_HINT =
  "Legacy in-process capabilities, each being converted to a server method or API scope. Add new access under Grants.";

// capabilityRows is the listing any capability picker has to be built
// from: the rows this build offers and this deployment has configured
// (configuredCapabilities above), plus a row of its own for each
// selected id that has none.
//
// Every stored capability set -- a task's own grants, the deployment's
// defaults (model.Config.DefaultCapabilities) and a repo's
// (model.RepoConfig.DefaultCapabilities) -- is reported as stored, so an
// id retired since somebody chose it stays in the set an operator is
// looking at (ui.OfferedCapabilities' own "scratch-repo", now
// github-sandbox). That is deliberate: ui.Settings.DefaultCapabilities
// says as much, "an operator can only clear one they can see". But a MUI
// multiple Select only ever deselects a value through that value's own
// MenuItem, so an id with no row is a chip nothing can untick, and it is
// sent straight back on the next save -- which ui.UpdateSettings and
// ui.SetRepoDefaultCapabilities both refuse outright as "unknown
// capability", pinning the whole pane until the id goes away. The extra
// row exists purely to be turned off, which is what makes that save
// possible again.
//
// `retired` is set on those rows so a picker can label one as something
// to switch off rather than something to switch on. It is a function of
// its own because all three pickers need the same rows for the same
// reason, and one that quietly stopped adding them would be a pane
// nobody could save.
export function capabilityRows(offered, selected) {
  const rows = configuredCapabilities(offered, selected);
  return rows.concat(
    (selected || [])
      .filter((id) => !rows.some((c) => c.id === id))
      .map((id) => ({
        id,
        name: id,
        description: RETIRED_CAPABILITY_HINT,
        retired: true,
      })),
  );
}

// capabilityUnavailableHint is what a picker row says about a capability
// this deployment is not currently configured to honour -- the
// `ready`/`needs` pair GET /api/config carries per row
// (ui.Capability.Ready), turned into the sentence a human ticking the
// box needs to read.
//
// It exists because the readiness of a capability and the act of
// attaching one used to live on opposite sides of the app: Settings'
// Capabilities tab knew that gemini-key on a deployment with no GCP
// project could not work, and the picker offered it anyway. The first
// symptom was the task itself failing to dispatch, minutes later, with a
// message about a refused capability and no hint that the fix was two
// panes away in Settings.
//
// Only `ready === false` counts, never a falsy one: a row from a build
// or an endpoint that computes no readiness at all leaves the field
// absent, and "unknown" must read as no hint rather than as a warning
// against every capability on the list.
//
// It warns and does not disable. A picker no longer offers an unready
// capability at all (configuredCapabilities above), so the rows this
// still has anything to say about are the ones already ticked: a
// capability configured when it was attached and since broken, or one
// ticked deliberately ahead of the secret that will make it work.
// Either way the row is there to be unticked as much as to be kept, and
// disabling it would be a grant nothing could take off again.
export function capabilityUnavailableHint(c) {
  if (!c || c.ready !== false) return "";
  const needs = c.needs || [];
  const gap =
    needs.length > 0 ? ` -- needs ${needs.join(", ")}` : " on this deployment";
  return `Not ready${gap}. A task holding this will fail to dispatch until that is set (Settings > Legacy capabilities).`;
}

// unionCapabilities composes the two layers of default capabilities the
// way ui.(*Client).defaultCapabilities does server-side: base (the
// deployment's own set) with extra (one repo's) appended, deduped,
// deployment-first.
//
// Union is the whole composition rule, and the only one -- a repo adds
// to what the deployment defaults and can never subtract from it
// (model.RepoConfig.DefaultCapabilities has why "everything except
// gcp-key here" is deferred rather than spelled some other way). It is a
// function of its own so the new-task form (which resolves a repo out of
// the config it already has) and the repos page (which resolves the same
// union from unsaved ticks, to say what Save is about to do) cannot
// drift into two different answers.
export function unionCapabilities(base, extra) {
  const ids = [...(base || [])];
  for (const id of extra || []) {
    if (!ids.includes(id)) ids.push(id);
  }
  return ids;
}

// defaultCapabilitiesFor is the set of capability ids a task filed
// against repo would start out holding -- the frontend's own reading of
// what ui.(*Client).defaultCapabilities resolves server-side, from the
// two layers GET /api/config reports: config.defaultCapabilities (the
// deployment's, chosen on Settings > Tasks) and
// config.repoDefaultCapabilities[repo] (that repo's own, chosen on the
// repos page).
//
// Union, deployment first, deduped: a repo adds to what the deployment
// defaults and can never subtract from it (model.RepoConfig.
// DefaultCapabilities has why "everything except gcp-key here" is
// deferred rather than spelled some other way here). A falsy repo -- no
// repo picked yet, or a deliberately repo-less task -- has no second
// layer to add, which is the same answer CreateTask gives a task whose
// Target is nil.
//
// This is only ever a form's starting state. What a task is filed with is
// what the request names (ui.CreateTaskRequest.Capabilities); computing
// the same union here is what lets the boxes be ticked, and unticked,
// before the task exists.
export function defaultCapabilitiesFor(config, repo) {
  return unionCapabilities(
    config?.defaultCapabilities,
    repo ? config?.repoDefaultCapabilities?.[repo] : null,
  );
}

// repoTasks is every task a repo's own page is about: the ones filed
// against it, plus the ones filed elsewhere that may land work in it
// anyway (Task.Writes, grain/task-54).
//
// The second half is the whole of this function. A task's `repo` is the
// single repo its own runs push their branch to, and until now that was
// the only thing a repo page's list asked about -- so a task filed
// against acme/gadgets that is also allowed to write to acme/widgets
// was invisible on acme/widgets' page, even though a run of it can put
// a pull request there. That is the wrong answer to "what work is
// happening in this repo": the page is about the repo, not about the
// filing.
//
// Reads are deliberately not here. A read-only repo grants a run no way
// to change anything in it (WriteReposField, and repoRows' own comment
// below), so a task that reads acme/schema is not work happening in
// acme/schema.
//
// Only the page's list widens. The `repo` *attribute* a filter names
// (taskAttributes.js) still means the one repo a task's own runs push
// to, because `repo:any:acme/widgets` is written into links people have
// kept, and "everything that may land work here" is the `writes`
// attribute's question to ask.
export function repoTasks(tasks, repo) {
  return (tasks || []).filter(
    (t) => t.repo === repo || (t.writes || []).some((w) => w?.repo === repo),
  );
}

// repoKey folds a repo name to the one thing two spellings of one
// repository have in common, so the maps below group them together.
// model.RepoRef.Same is the same rule on the server, and its own doc
// comment is why this is needed at all: that is the comparison, and
// everything keyed by a repo -- these maps included -- matched exactly.
export function repoKey(repo) {
  return (repo || "").toLowerCase();
}

// repoRows unions the things that can make this deployment know a repo
// at all -- its config repo, config.targetRepos, every repo a task's write target
// names (Task.Repo, never Reads: a read-only repo grants nothing and is
// not what a task "belongs to"), and every repo carrying configuration
// of its own, which today is a default capability set
// (config.repoDefaultCapabilities), standing instructions
// (config.reposWithPromptExtension), a setup command
// (config.reposWithSetupCommand), or any of them -- into one row per repo, sorted
// alphabetically, for the repo page and its per-state counts. Tasks with
// no target (a proposal nobody has pointed at a repo yet) are omitted
// rather than grouped under a blank name.
//
// A targetRepos entry with no tasks yet still gets a (zero-count) row --
// bwsalmon/agents#473 moved adding/removing a target repo onto this
// page, which only works if a repo shows up here the moment it's added,
// before anything has ever run against it. `configured` tells a row
// apart from one that only exists because a task already targets it: the
// repos pane only offers to remove the former, since there is nothing to
// remove otherwise -- an unrestricted deployment's targetRepos is always
// empty, so every row it has is `configured: false`.
//
// The third source is there for the same reason, one step further out.
// None of ui.(*Client).SetRepoDefaultCapabilities,
// SetRepoPromptExtension or SetRepoSetupCommand requires a repo to be
// allow-listed (their own
// doc comments: a repo can be configured before it is allowed, and keeps
// its configuration after it is removed), so a repo can hold stored
// configuration while matching neither of the other two -- and this page
// is the only place that configuration can be edited, so dropping the
// row would leave it real, really applied to every task filed there, and
// unreachable. `grain repo list` (grain/task-36) reads the same sources
// for the same reason; the page and the CLI are meant to agree on which
// repos this deployment knows about.
//
// Two rules decide which repos get a row, and both exist because a row
// here is a claim that this deployment can work in that repo *now*.
//
// A task raises a row only on an unrestricted deployment. Where
// targetRepos is set, it is the deployment saying which repos may be
// worked in at all, so a repo that has fallen off it -- removed, or
// renamed away underneath the tasks that named it -- is somewhere
// nothing here can clone, push, or open a pull request against any
// more. Its old tasks are still real and still readable on their own
// pages; what they are not is evidence that the repo is still a place
// to file work. Before this they held its row up forever, and because
// such a row is `configured: false` the pane offered no Remove for it
// either, so removing a repo could not actually remove it (grain/
// task-45's own "Remove" is what this makes true).
//
// And two spellings of one repository are one row. Every source here is
// text an operator or a task wrote down, GitHub resolves owner/name
// case-insensitively, and model.RepoRef.Same says so on the server --
// so grouping by the string alone showed `Bwsalmon/grain-home` beside
// `bwsalmon/grain-home` as two repos with the tasks split between them.
// The row keeps the first spelling it is created under, which is an
// allowlist entry's wherever there is one: that list is the deployment's
// own canon (ui.(*Client).canonicalRepo reads it for exactly this), and
// ui.(*Client).AddTargetRepo now resolves an entry against GitHub's own
// spelling before storing it, so the canon is the repository's.
//
// `defaults` is whether the repo carries configuration of its own, which
// on a row that is neither configured nor targeted is the only reason it
// is here at all -- the repos pane says so rather than leaving an empty
// row with nothing to explain it.
export function repoRows(config, tasks) {
  const targetRepos = config?.targetRepos || [];
  const restricted = targetRepos.length > 0;

  // The third source, folded once up front rather than looked up per
  // row. A row can be created from any of the spellings below, and a
  // repo's stored configuration is keyed by whichever one wrote it, so
  // an exact lookup here would report a repo as carrying no defaults
  // purely because two spellings of it disagree -- which is the same
  // bug on the same page, one column over.
  //
  // An absent key and an empty list mean the same thing (nothing
  // added), the way ui.configResponse.RepoDefaultCapabilities says they
  // do, so an empty one is not a repo to put a row up for.
  const configured = new Map();
  const note = (repo) => configured.set(repoKey(repo), repo);
  for (const [repo, ids] of Object.entries(
    config?.repoDefaultCapabilities || {},
  )) {
    if ((ids || []).length > 0) note(repo);
  }
  // The other half of that same source: a repo whose only configuration
  // is standing instructions of its own
  // (ui.configResponse.ReposWithPromptExtension, which lists exactly the
  // repos that have some), and then a repo whose only configuration is
  // the setup command grain runs in every checkout it makes there
  // (ui.configResponse.ReposWithSetupCommand).
  for (const repo of config?.reposWithPromptExtension || []) note(repo);
  for (const repo of config?.reposWithSetupCommand || []) note(repo);
  for (const repo of config?.reposWithCICapabilities || []) note(repo);
  for (const repo of config?.reposWithReleaseSettings || []) note(repo);
  // And a repo whose only configuration is the CI workflows grain
  // declares for it (ui.configResponse.ReposWithWorkflows). The likeliest
  // of the four to be a repo's whole presence here: declaring workflows
  // on grain's side is what you do for a repository grain does not
  // control, which may well have no tasks and no allowlist entry.
  for (const repo of config?.reposWithWorkflows || []) note(repo);

  const byRepo = new Map();
  const row = (repo) => {
    const key = repoKey(repo);
    if (!byRepo.has(key)) {
      byRepo.set(key, {
        repo,
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: configured.has(key),
      });
    }
    return byRepo.get(key);
  };
  if (config?.configRepo) {
    row(config.configRepo).configRepo = true;
  }
  for (const repo of targetRepos) {
    row(repo).configured = true;
  }
  for (const repo of configured.values()) {
    row(repo);
  }
  for (const t of tasks || []) {
    if (!t.repo) continue;
    // A session's task -- a standing session's or any other interactive
    // one -- is counted nowhere a repo's work is: the rail leaves it out
    // of its state counts (Sidebar), and the task list a row's "N tasks
    // filed here" links to hides it (taskFilters.js's isSessionTask), so
    // counting it here made that link promise more tasks than it opened.
    // The session is in the rail's Sessions entry instead.
    if (t.consoleSession || t.interactive) continue;
    // On a restricted deployment a task counts towards a row that one
    // of the two sources above already put up, and never raises one of
    // its own -- see this function's own doc comment. byRepo.get rather
    // than row() is the whole of that rule.
    const entry = restricted ? byRepo.get(repoKey(t.repo)) : row(t.repo);
    if (!entry) continue;
    entry.total += 1;
    entry.counts[t.state] = (entry.counts[t.state] || 0) + 1;
    if (t.blocked) entry.blocked += 1;
  }
  return [...byRepo.values()].sort((a, b) => a.repo.localeCompare(b.repo));
}

// repoActivity is one repoRows row read as a single status, for the dot
// the repos list draws ahead of a repo's name (grain/task-327) -- the
// same "what is going on here, at a glance" a task row's own state badge
// gives, for a thing that has no state of its own.
//
// Three answers, because two would be a lie either way round: a repo
// with an agent working in it right now and a repo with a fortnight of
// queued work nobody has started are both "active", and neither is the
// repo whose every task is closed. So:
//
//   - running: at least one task is running there this moment. The dot
//     is the same accent the running state uses everywhere else.
//   - open: work outstanding, none of it moving -- queued, waiting on a
//     human, failed, or sitting on the merge queue. A hollow ring, the
//     queued badge's own figure.
//   - idle: nothing but closed and deferred tasks, or no tasks at all.
//     A grey dot.
//
// "Closed" and "Deferred" are the states counted as not-open, and they
// are the same two a task list hides by default (SET_ASIDE_STATES): one
// is over with, the other is work nobody is waiting on this month, and
// neither is something to go and look at. "Queued for merge"
// (STATE_LABELS.completed) reads like an ending and is not one -- the
// task is waiting on the merge queue and can still come back needing a
// human -- so a repo whose last task sits there is still open work.
export function repoActivity(row) {
  const running = row.counts?.running || 0;
  if (running > 0) {
    return {
      key: "running",
      state: "running",
      label: "Active",
      title: `Active -- ${running} task${running === 1 ? "" : "s"} running now`,
    };
  }
  // The states that are not open work, named in the order STATE_ORDER
  // puts them so the idle title below reads "deferred or closed".
  const setAside = SET_ASIDE_STATES.filter((s) => row.counts?.[s]);
  const open = row.total - setAside.reduce((n, s) => n + row.counts[s], 0);
  if (open > 0) {
    return {
      key: "open",
      state: "queued",
      label: "Open",
      title: `${open} open task${open === 1 ? "" : "s"}, none running right now`,
    };
  }
  return {
    key: "idle",
    state: "closed",
    label: "Idle",
    title:
      row.total > 0
        ? `Idle -- every task here is ${setAside.join(" or ")}`
        : "Idle -- no tasks here yet",
  };
}

// ACTIVITY_RANK is repoActivity's own three answers in the order the
// repos list's "Active first" sort wants them: what is moving, then what
// is waiting, then what is done.
export const ACTIVITY_RANK = { running: 0, open: 1, idle: 2 };

// knownRepos is the option list behind the repo dropdowns
// (bwsalmon/agents#447): the controller-owned config repo, config.targetRepos
// (what CreateTask actually enforces ordinary task repos against, when the
// deployment restricts them), and whatever repo any existing task already targets -- so an unrestricted
// deployment still gets a useful dropdown once it has filed at least one
// task, rather than staying a bare text field forever. Sorted and
// deduped the same way repoRows already sorts its own rows, which now
// means two spellings of one repository collapse to one option.
//
// This is the wide list, and it stays wide: it is what the read-only and
// extra-write pickers offer, and neither of those is gated on the
// allowlist. targetableRepos below is the narrow one, for the field
// where the allowlist does decide.
//
// These sources, not repoRows' configuration-only ones: a repo that only carries defaults of
// its own is not somewhere a task can be filed today (targeting a repo
// off a non-empty allowlist parks it before it dispatches,
// ui.(*Client).parkOffAllowlist), and offering it here would read as
// this deployment inviting a task it is going to park. It still gets a
// row on the repos page, which is where its defaults are edited.
export function knownRepos(config, tasks) {
  const repos = new Map();
  const add = (repo) => {
    if (!repo) return;
    const key = repoKey(repo);
    if (!repos.has(key)) repos.set(key, repo);
  };

  // The config repository first: every deployment can read and write its
  // controller-owned checkout even when targetRepos narrows external repos.
  // The allowlist follows, retaining its own spelling as the canon for every
  // ordinary repository.
  if (config?.configRepo) add(config.configRepo);
  for (const repo of config?.targetRepos || []) add(repo);
  for (const repo of deploymentRow(config)?.targetRepos || []) add(repo);
  for (const t of tasks || []) {
    if (t.repo) add(t.repo);
  }
  return [...repos.values()].sort((a, b) => a.localeCompare(b));
}

// defaultRepoChoice is the repo a required target field starts on:
// this deployment's own default target (Config.DefaultTarget, the repo a
// task with none named is filed against) when the field offers it, and
// otherwise the first option -- which is what a required native select
// shows anyway. Without it a new schedule or suite run started on
// whichever allowed repo sorts first alphabetically, so adding acme/aaa
// to the list quietly moved every new schedule onto it (grain/task-1533).
export function defaultRepoChoice(config, options) {
  const preferred = config?.defaultTarget;
  if (preferred) {
    const hit = (options || []).find((r) => repoKey(r) === repoKey(preferred));
    if (hit) return hit;
  }
  return (options || [])[0] || "";
}

// targetableRepos is knownRepos narrowed to the repos a task may
// actually be *filed against* -- the option list behind a target repo
// field, as against the read-only and extra-write pickers beside it.
//
// The narrowing is the allowlist plus the intrinsic config repository
// (config.configRepo, grain/config).
// CreateTask checks an ordinary task Target against Team.TargetRepos (or
// Config.TargetRepos) and parks it when it is off the list (ui.(*Client).parkOffAllowlist),
// and checks nothing of the kind for Reads or Writes. So a target field
// offering a repo the team no longer names is offering an option
// whose only outcome is a parked task -- while a reads field offering
// one is offering something that works, which is why knownRepos above
// stays wide and this is a second list rather than a tightening of it.
// model.WorksInRepo draws the same line from the other side. grain/config
// is the exception because it is served by every controller and cannot be
// removed from a deployment.
//
// An unrestricted team/deployment restricts nothing, so there the two lists
// are the same list.
// reachableRepoNames is the repos in reachable -- what GET
// /api/deployment/reachable-repos lists the team's GitHub credential as
// reaching, [{repo, push}] -- that a picker may offer: every one for a
// read, only those it may push to for a target or an extra write.
export function reachableRepoNames(reachable, { push = false } = {}) {
  return (reachable || [])
    .filter((r) => r && r.repo && (!push || r.push))
    .map((r) => r.repo);
}

// repoReachable is whether the team's credential reaches repo, as
// reachable lists it: true or false, or null where there is no list to
// ask (not loaded, or the server could not get one) -- which warns
// about nothing. A config repo is the controller's own and always
// reachable.
export function repoReachable(reachable, repo, { push = false } = {}) {
  if (!repo || !Array.isArray(reachable)) return null;
  const [owner] = repo.split("/");
  if ((owner || "").toLowerCase() === "grain") return true;
  const key = repoKey(repo);
  const hit = reachable.find((r) => r && repoKey(r.repo) === key);
  if (!hit) return false;
  return push ? !!hit.push : true;
}

// pickerRepos is the option list behind the read-only and extra-write
// pickers: every repo this deployment knows of (knownRepos' wide list)
// and what its credential reaches (reachable; push-only for writes).
export function pickerRepos(config, tasks, reachable, { push = false } = {}) {
  const repos = new Map();
  for (const repo of [
    ...knownRepos(config, tasks),
    ...reachableRepoNames(reachable, { push }),
  ]) {
    if (!repo) continue;
    const key = repoKey(repo);
    if (!repos.has(key)) repos.set(key, repo);
  }
  return [...repos.values()].sort((a, b) => a.localeCompare(b));
}

// targetableRepos takes reachable as well: for a deployment with no repos
// attached, the target picker offers what its credential can push to
// (pickerRepos above) rather than every repo anything has targeted
// (grain/task-1850).
export function targetableRepos(config, tasks, reachable) {
  const configRepos = [];
  if (config?.configRepo) configRepos.push(config.configRepo);
  const allowed = config?.targetRepos || [];
  if (allowed.length === 0 && Array.isArray(reachable)) {
    const repos = new Map();
    for (const repo of [
      ...configRepos,
      ...reachableRepoNames(reachable, { push: true }),
    ]) {
      if (!repo) continue;
      const key = repoKey(repo);
      if (!repos.has(key)) repos.set(key, repo);
    }
    return [...repos.values()].sort((a, b) => a.localeCompare(b));
  }
  if (allowed.length === 0) {
    return knownRepos(config, tasks);
  }
  const repos = new Map();
  for (const repo of [...configRepos, ...allowed]) {
    if (!repo) continue;
    const key = repoKey(repo);
    if (!repos.has(key)) repos.set(key, repo);
  }
  return [...repos.values()].sort((a, b) => a.localeCompare(b));
}

// repoTargetable is the same rule as targetableRepos above asked about
// one repo rather than rendered as a list: may a task be filed against
// this one? It is model.RepoAllowed on the server, and it exists here
// because a second place needs the answer -- DetailOverlay's "this
// target is off the list" warning, which is shown about a repo somebody
// already typed rather than used to build a menu.
//
// Two of the three clauses are the ones that make an open-coded
// `targetRepos.includes(repo)` wrong, and both had shipped as that:
// grain/config is targetable on every deployment however narrow the
// list is (model.ConfigRepo -- it is the controller's own checkout and
// cannot be taken off), and an entry is matched by repoKey rather than
// exactly, since `acme/Widgets` and `acme/widgets` are one repository
// to GitHub and RepoRef.Same treats them as one here too.
// isConfigRepoName is model.IsConfigRepo: grain/config, matched without
// regard to case. It is the only configuration repository; a
// grain/config-<team> name is an ordinary repo that nothing serves.
function isConfigRepoName(repo) {
  return String(repo).trim().toLowerCase() === "grain/config";
}

export function repoTargetable(config, repo) {
  if (!repo) return true;
  const key = repoKey(repo);
  if (config?.configRepo && repoKey(config.configRepo) === key) return true;
  // The config repository by name too: model.IsConfigRepo admits
  // grain/config on the server, and the tasks New board, Edit board and
  // a panel's Keep file land on it -- a warning that their repo is off
  // the list would contradict the server, which never parks them.
  if (isConfigRepoName(repo)) return true;

  const allowed = config?.targetRepos || [];
  if (allowed.length === 0) return true;
  return allowed.some((entry) => repoKey(entry) === key);
}

// A task in one of these states is no evidence of where a repo's work
// currently lives, so lastBaseForRepo below does not suggest its base.
// The case that matters is a base branch that has been merged and
// deleted: the run pointed at it fails ("base branch 'x' does not exist
// on owner/repo", orchestrator's own prepareCheckout), and without this
// that failed task is then the newest one carrying that base -- so the
// dead branch is suggested again for the next task, which fails the same
// way, which makes it the newest again. The suggestion outlives the
// branch forever, and every task filed from it is dead on arrival.
export const STALE_BASE_STATES = ["failed", "closed"];

// suggestsBase reports whether a task is evidence of where a repo's work
// currently lives -- the filter behind both lastBaseForRepo below and
// NewTaskOverlay's own "does this repo have any history to prefill
// from?" check, which have to agree on what counts or the overlay
// prefills from a task lastBaseForRepo already looked past.
//
// Two kinds of task are no evidence. One is a task in a
// STALE_BASE_STATES state (above). The other is a task nobody filed by
// hand: a schedule firing, a suite pass, the merge queue stacking a fix,
// an agent's own propose_task. Those pick a base for their own reasons
// -- a suite run stacks every task in a pass against one throwaway
// branch, a stacked fix targets the branch of the pull request it
// repairs -- and none of it says anything about where the human filing
// the next task means to start. Letting them set the suggestion means a
// scheduled job that ran overnight silently redirects tomorrow morning's
// hand-filed task onto a branch nobody chose.
//
// authorKind is model.PrincipalKind on the wire (Task.AuthorKind).
// Missing is treated as human: a task from a store or a caller that
// never recorded one is unknown provenance, not known-automated, and
// dropping it would quietly discard history rather than protect it.
export function suggestsBase(task) {
  if (STALE_BASE_STATES.includes(task.state)) return false;
  return !task.authorKind || task.authorKind === "human";
}

// lastBaseForRepo is the branch NewTaskOverlay prefills "Base branch"
// with once a repo is picked (bwsalmon/agents#641): whatever base the
// most recently created task targeting that repo used, so a repo whose
// work currently lives off a release branch (or any other non-default
// base) doesn't make every new task against it retype that branch name.
// Empty when the most recent task on record for that repo left base
// unset too -- an empty base is itself a value worth repeating (the
// deployment's default branch), not a gap to look past for an older
// task's base.
//
// It is a suggestion, not a check: nothing here can know whether a
// branch still exists on GitHub -- that is a fact only GitHub holds, and
// this package deliberately never asks it. What it can do is not repeat
// a base that has already failed, or one no human chose in the first
// place, which is what suggestsBase above is for.
export function lastBaseForRepo(tasks, repo) {
  if (!repo) return "";
  let latest = null;
  for (const t of tasks || []) {
    if (t.repo !== repo) continue;
    if (!suggestsBase(t)) continue;
    if (!latest || new Date(t.createdAt || 0) > new Date(latest.createdAt || 0))
      latest = t;
  }
  return latest ? latest.base || "" : "";
}

// branchesInUse is every branch this deployment's own tasks are already
// working off in repo, newest first -- what the branch boxes offer as
// suggestions (BranchField.jsx) alongside the branches grain has been
// asked to create or adopt there.
//
// Both halves of a task count: the base its own runs push against
// (Task.Base), and the base named for each repo it may also land work
// in (Task.Writes), since acme/docs is just as much "a branch somebody
// picked for acme/docs" when it was picked on a task filed elsewhere.
//
// An empty base is left out rather than offered as a name: it means the
// repo's own default branch, which is what the box already says when it
// is empty. suggestsBase (above) is not applied here for the reason it
// exists -- it protects a *prefilled* value, one nobody chose to type,
// from outliving the branch it names. A suggestion list is read before
// it is picked from, so a branch a scheduled task used, or one a failed
// task pointed at, is worth offering to somebody who is looking for the
// name they half remember.
export function branchesInUse(tasks, repo) {
  if (!repo) return [];
  const ordered = [...(tasks || [])].sort(
    (a, b) => new Date(b.createdAt || 0) - new Date(a.createdAt || 0),
  );
  const out = [];
  const seen = new Set();
  const add = (base) => {
    if (typeof base !== "string" || base.trim() === "") return;
    if (seen.has(base)) return;
    seen.add(base);
    out.push(base);
  };
  for (const t of ordered) {
    if (t.repo === repo) add(t.base);
    for (const w of t.writes || []) if (w?.repo === repo) add(w.base);
  }
  return out;
}

// frameworkLabel names an agent framework the way the UI talks about it
// -- model.AgentFrameworks() are wire values, not display text, and two
// panes (the per-task picker on New task, the "Agent framework" row on a
// task's detail) would otherwise each spell them out and drift. "gemini"
// is the former wire value for Antigravity and still arrives from a
// store written before the rename, so it falls through to the same
// default label rather than being special-cased -- as does anything else
// unrecognized, since a label is not the place to discover that a
// deployment is running a framework this build has never heard of.
const FRAMEWORK_LABELS = {
  claude: "Claude",
  codex: "Codex",
  custom: "Custom command",
};

export function frameworkLabel(framework) {
  return FRAMEWORK_LABELS[framework] || "Antigravity";
}

// sameTeam compares two team ids the way the daemon does: without case,
// and with none meaning the default team.
function sameTeam(a, b) {
  return (a || "default").toLowerCase() === (b || "default").toLowerCase();
}

// connectionPause is the agent usage-limit pause on connection's
// credential (config.agentPause.pauses, ui.AgentPauseEntry), and null
// when there is none. A pause is keyed by the connection owning a key:
// the one connection borrows from (credentialBackendId), else itself --
// so a borrowing connection is paused with its source
// (model.AgentBackend.CredentialOwnerID).
//
// It exists because the banner was the only place that said a connection
// was paused: Connections still showed it "Active", and a queued task's
// pane said only "queued" (grain/task-1937).
//
// A pause can hold one model on a connection (entry.model) rather than
// all of it: a provider's per-model limit. With modelName, the pause
// holding a run of that model there -- the whole connection's, or that
// model's own -- is returned; without it, only a pause on the whole
// connection is, since one model's pause leaves the connection serving
// its others.
export function connectionPause(pause, connection, modelName) {
  if (!pause?.paused || !connection) return null;
  const key = connection.credentialBackendId || connection.id;
  const mine = (pause.pauses || []).filter(
    (p) => p.connection === key && sameTeam(p.team, connection.team),
  );
  return (
    mine.find((p) => !p.model) ||
    (modelName ? mine.find((p) => pauseCoversModel(p, modelName)) : null) ||
    null
  );
}

// pauseCoversModel is whether pause entry p holds runs of modelName on
// its connection: p is the whole connection's, or names that model --
// as its own or as one of its labels ("auto" for a provider-chosen run
// the provider said was refused on p.model). orchestrator's
// pauseWindow.coversRun.
export function pauseCoversModel(p, modelName) {
  if (!p) return false;
  if (!p.model) return true;
  const name = (modelName || "").trim().toLowerCase();
  return name === p.model || (p.labels || []).includes(name);
}

// modelPauses is the pauses on single models of connection, by model:
// what its model list marks as paused, each with its own Resume.
export function modelPauses(pause, connection) {
  if (!pause?.paused || !connection) return [];
  const key = connection.credentialBackendId || connection.id;
  return (pause.pauses || []).filter(
    (p) => p.model && p.connection === key && sameTeam(p.team, connection.team),
  );
}

// modelPause is the pause holding modelName on connection, or null --
// the model list's own question, row by row.
export function modelPause(pause, connection, modelName) {
  return (
    modelPauses(pause, connection).find((p) =>
      pauseCoversModel(p, modelName),
    ) || null
  );
}

// defaultModels splits a deployment's default model configuration into the
// ordered list of models to try, first to last, dropping blanks and
// case-insensitive duplicates while keeping the first-seen spelling
// (model.ParseDefaultModels). Accepts a model array, a comma-separated
// string, a deployment row, or a /api/config response.
export function defaultModels(source) {
  let raw = [];
  if (Array.isArray(source)) {
    raw = source;
  } else if (typeof source === "string") {
    raw = source.split(",");
  } else if (source && typeof source === "object") {
    const dep = source.deployment || source;
    if (Array.isArray(dep.defaultModels) && dep.defaultModels.length > 0) {
      raw = dep.defaultModels;
    } else if (typeof dep.defaultModel === "string") {
      raw = dep.defaultModel.split(",");
    }
  }
  const out = [];
  const seen = new Set();
  for (const part of raw) {
    for (const piece of String(part || "").split(",")) {
      const m = piece.trim();
      if (!m) continue;
      const key = m.toLowerCase();
      if (!seen.has(key)) {
        seen.add(key);
        out.push(m);
      }
    }
  }
  return out;
}

// connectionsServingModels is the subset of connections that advertise at
// least one of modelNames (or every connection when modelNames is empty).
export function connectionsServingModels(models, connections, modelNames) {
  const names = defaultModels(modelNames);
  if (names.length === 0) return connections || [];
  const allowed = new Set();
  for (const name of names) {
    const row = (models || []).find(
      (m) => m.id.toLowerCase() === name.toLowerCase(),
    );
    for (const conn of row?.connections || []) {
      allowed.add(conn.id);
    }
  }
  return (connections || []).filter((c) => allowed.has(c.id));
}

// taskModelOn is the model a run of t would pass the CLI on connection
// b: the task's own choice, the first unpaused deployment default model
// that b supports, or the connection's own (daemon's runModelOn).
export function taskModelOn(t, b, config) {
  if (t?.model) return t.model;
  const defaults = defaultModels(deploymentRow(config));
  if (defaults.length === 0) return providerDefaultModel(b);
  if (!b) return defaults[0];
  const supported = advertisedModels(b).map((m) => m.toLowerCase());
  const matching =
    supported.length > 0
      ? defaults.filter((m) => supported.includes(m.toLowerCase()))
      : defaults;
  const choices = matching.length > 0 ? matching : [defaults[0]];
  if (config?.agentPause?.paused) {
    const unpaused = choices.find(
      (m) => !connectionPause(config.agentPause, b, m),
    );
    if (unpaused) return unpaused;
  }
  return choices[0];
}

function taskConnectionPause(pause, t, b, config) {
  if (t?.model) return connectionPause(pause, b, t.model);
  const defaults = defaultModels(deploymentRow(config));
  if (defaults.length === 0)
    return connectionPause(pause, b, providerDefaultModel(b));
  const supported = advertisedModels(b).map((m) => m.toLowerCase());
  const matching =
    supported.length > 0
      ? defaults.filter((m) => supported.includes(m.toLowerCase()))
      : defaults;
  const choices = matching.length > 0 ? matching : [defaults[0]];
  const pauses = choices.map((m) => connectionPause(pause, b, m));
  if (pauses.some((p) => !p)) return null;
  return pauses.reduce((first, p) =>
    new Date(p.until) < new Date(first.until) ? p : first,
  );
}

// taskFramework is the framework the usage-limit gate reads a task on no
// connection as running on -- its own, else the deployment's default --
// which is orchestrator.frameworkFor.
export function taskFramework(t, config) {
  return t?.agentFramework || config?.agentFramework || "antigravity";
}

// taskPause is the usage-limit pause holding t, or null: what the
// connection pool and dispatch.PausedFor between them pass it over for.
// A task pinned to a connection waits for that connection's pause; one
// left to the rotation waits only while every enabled connection of its
// team that could run it is paused (it rotates onto any other), and then
// until the first of them lifts; and a team's runs on no connection wait
// for a pause on its team and framework.
export function taskPause(t, config) {
  const pause = config?.agentPause;
  if (!pause?.paused) return null;
  const entries = (pause.pauses || []).filter((p) => sameTeam(p.team, t?.team));
  if (entries.length === 0) return null;
  const connections = (config?.agentBackends || []).filter((b) =>
    sameTeam(b.team, t?.team),
  );
  if (t?.agentBackendId) {
    const pinned = connections.find((b) => b.id === t.agentBackendId);
    return pinned ? taskConnectionPause(pause, t, pinned, config) : null;
  }
  const framework = (name) => (name === "gemini" ? "antigravity" : name);
  const want = t?.agentFramework ? framework(t.agentFramework) : "";
  const enabled = connections.filter(
    (b) => !b.disabled && (!want || framework(b.framework) === want),
  );
  const wantedModels = t?.model
    ? [t.model]
    : defaultModels(deploymentRow(config));
  const serving =
    wantedModels.length > 0
      ? enabled.filter((b) => {
          const adv = advertisedModels(b).map((m) => m.toLowerCase());
          return (
            adv.length === 0 ||
            wantedModels.some((m) => adv.includes(m.toLowerCase()))
          );
        })
      : enabled;
  const candidates = serving.length > 0 ? serving : enabled;
  if (candidates.length > 0) {
    const paused = candidates.map((b) =>
      taskConnectionPause(pause, t, b, config),
    );
    if (paused.some((p) => !p)) return null;
    return paused.reduce((first, p) =>
      new Date(p.until) < new Date(first.until) ? p : first,
    );
  }
  const own = framework(taskFramework(t, config));
  return (
    entries.find(
      (p) => !p.connection && (!p.framework || framework(p.framework) === own),
    ) || null
  );
}

// PROVIDER_DEFAULT_LABEL is how the panes name the model a task runs on
// while nobody has chosen one: the provider's own (providerDefaultModel),
// labelled so nobody mistakes it for a choice. grain names no model of its
// own (docs/connections-and-models.md).
export const PROVIDER_DEFAULT_LABEL = "provider default (unset)";

// NO_MODELS_LABEL is what a connection that advertises no model at all
// says: its provider has never answered, or it is a custom command with
// an empty list. Tasks that would run on it wait until somebody sets one.
export const NO_MODELS_LABEL = "no models — set one";

// DISCOVERING_FRAMEWORKS are the frameworks whose provider grain can ask
// what it serves (model.AgentBackend.DiscoversModels). A custom command
// cannot be asked.
export const DISCOVERING_FRAMEWORKS = [
  "antigravity",
  "gemini",
  "codex",
  "claude",
];

// pinnedModels is the list an operator saved on a connection: its own
// `models`, then a legacy `model` written before connections and models
// were separated (model.AgentBackend.PinnedModels).
export function pinnedModels(backend) {
  if (!backend) return [];
  const out = [];
  const add = (m) => {
    const id = (m || "").trim();
    if (id && !out.includes(id)) out.push(id);
  };
  for (const m of backend.models || []) add(m);
  add(backend.model);
  return out;
}

// advertisesDiscovered reports whether a connection advertises what its
// provider reports rather than a list pinned by hand.
export function advertisesDiscovered(backend) {
  return (
    !!backend &&
    DISCOVERING_FRAMEWORKS.includes(backend.framework) &&
    pinnedModels(backend).length === 0
  );
}

// advertisedModels is the list of models one connection serves, in the
// order dispatch reads them (model.AgentBackend.SupportedModels): its
// pinned list when it has one; else what its provider last reported
// (`discovered`), minus `excludedModels`; else nothing -- grain does not
// guess a model name (docs/connections-and-models.md).
export function advertisedModels(backend) {
  if (!backend) return [];
  const pinned = pinnedModels(backend);
  if (pinned.length) return pinned;
  const excluded = (backend.excludedModels || []).map((m) =>
    m.trim().toLowerCase(),
  );
  const keep = (m) => m && !excluded.includes(m.toLowerCase());
  const discovered = DISCOVERING_FRAMEWORKS.includes(backend.framework)
    ? backend.discovered?.models || []
    : [];
  return [...new Set(discovered.map((m) => (m || "").trim()))].filter(keep);
}

// providerDefaultModel is the model a connection runs a task that chose
// none while its team has no default model either
// (model.AgentBackend.EffectiveModel): its first pinned model, else its
// provider's own default -- "auto" for agy, otherwise the first model the
// provider lists -- or "" for a connection with no models at all.
export function providerDefaultModel(backend) {
  const pinned = pinnedModels(backend);
  if (pinned.length) return pinned[0];
  const models = advertisedModels(backend);
  if (!models.length) return "";
  if (["antigravity", "gemini"].includes(backend.framework)) return "auto";
  return models[0];
}

// teamDefaultLabel is the empty choice of a task's model picker: the
// team's default model(s) when it has any, else the provider default.
//
// Given the picker's models (connectionModels), it also says when a
// default is advertised only by disabled connections, the same caveat
// modelOptions puts on that model's own entry -- otherwise the one
// choice most forms leave selected reads as servable while the entry
// for the very same model below it says it is not. A default no
// connection lists at all (the provider's "auto", say) is left alone:
// connections do not advertise those, so their absence means nothing.
// In a fallback chain where only some links are disabled-only, the
// caveat goes on those links alone.
export function teamDefaultLabel(teamDefaultModel, models = null) {
  const chain = defaultModels(teamDefaultModel);
  if (chain.length === 0)
    return `Deployment default (${PROVIDER_DEFAULT_LABEL})`;
  const disabledOnly = (id) => {
    const served = (models || []).find((m) => m.id === id);
    return Boolean(served && !served.enabled);
  };
  const flagged = chain.filter(disabledOnly);
  if (flagged.length === 0) return `Deployment default (${chain.join(" → ")})`;
  if (flagged.length === chain.length)
    return `Deployment default (${chain.join(" → ")}, disabled connections only)`;
  return `Deployment default (${chain
    .map((m) => (disabledOnly(m) ? `${m} [disabled connections only]` : m))
    .join(" → ")})`;
}

// deploymentRow is the deployment's own settings, config.deployment --
// GET /api/deployment's shape (ui.teamFrom): default model, config
// repository, repos and git connections -- or null before config has
// loaded.
export function deploymentRow(config) {
  return config?.deployment || null;
}

// connectionModels is every model the given connections advertise, each with
// the framework that serves it and the connections -- enabled or not --
// that do, in the order the connections are stored (which is the order
// the rotation reaches them in). A model is offered while only disabled
// connections serve it, because pinning one of those is still how such
// a task runs; `enabled` says whether the rotation can reach it at all.
export function connectionModels(backends) {
  const byID = new Map();
  for (const backend of backends || []) {
    for (const id of advertisedModels(backend)) {
      if (!byID.has(id)) {
        byID.set(id, {
          id,
          framework: backend.framework || "",
          connections: [],
          enabled: false,
        });
      }
      const entry = byID.get(id);
      entry.connections.push(backend);
      if (!backend.disabled) entry.enabled = true;
    }
  }
  return Array.from(byID.values());
}

// modelOptions is what a Model picker (ModelSelect) offers: emptyLabel
// for "" -- the team's default -- then every model the team's connections
// advertise (connectionModels), saying so when only disabled connections serve
// one, and last the value the form already carries when no connection
// advertises it any more, so a form opened on it does not silently drop
// it.
export function modelOptions(models, emptyLabel, current = "") {
  const options = [{ value: "", label: emptyLabel }];
  for (const m of models) {
    options.push({
      value: m.id,
      label: m.enabled ? m.id : `${m.id} (disabled connections only)`,
    });
  }
  if (current && !models.some((m) => m.id === current)) {
    options.push({
      value: current,
      label: `${current} (no connection advertises it)`,
    });
  }
  return options;
}

// connectionLabel names one connection in a picker: whatever the operator
// called it and what framework it is, and says so when it is disabled --
// a disabled connection is out of the rotation and only runs work pinned
// to it by hand.
export function connectionLabel(backend) {
  if (!backend) return "";
  const parts = [
    backend.name || backend.id || frameworkLabel(backend.framework),
    frameworkLabel(backend.framework),
  ];
  if (backend.disabled) parts.push("disabled, pin only");
  return parts.join(" · ");
}

// stackedChip is the label and hover text for the chip a stacked task
// carries -- a task branched off another task's branch and merged back
// into it rather than into a repo's own base. Two things are stacked
// that way, and the chip says which: a review (grain/task-284), run over
// a change before it merges, and the merge queue's own automatic fix for
// a pull request. Both are stacked; only one of them is a repair of a
// red build.
//
// It lives here rather than in either view because both of them show it:
// the task list's rows and the board's cards would otherwise each spell
// out the same two labels and drift apart the first time one of them was
// edited.
export function stackedChip(t) {
  if (t.review) {
    return {
      label: "review",
      title: t.generatedFrom
        ? `a review of ${t.generatedFrom}'s own code, run before it merges`
        : "a review of another task's own code, run before it merges",
    };
  }
  return {
    label: "merge fix",
    title: t.generatedFrom
      ? `the merge queue's own automatic fix for ${t.generatedFrom}`
      : "the merge queue's own automatic fix for another task's pull request",
  };
}
