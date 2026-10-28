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

import { useEffect, useRef, useState } from "react";
import { Alert, Button, CircularProgress, TextField } from "@mui/material";
import api from "../api.js";
import LinkedNote from "../linkedNote.jsx";
import { linkTaskRefs, taskRefFromHref } from "../taskRefs.js";
import {
  awaitsGitHubApproval,
  awaitsHumanSubmit,
  completionPhase,
  inboxTasks,
  mergeQueueStalled,
  pullRequestUrl,
  reasonControl,
  redBaseRepairSentence,
  waitingBaseRepair,
  askedCase,
} from "../state.js";
import { forceMergeOffered, promptForceMerge } from "../taskWrites.js";
import { useNotificationSetting } from "../inboxNotifications.js";
import ItemGlyph from "./ItemGlyph.jsx";
import ClChip from "./ClChip.jsx";
import { STATE_SYNC_TITLE, muiSeverity, stateSyncSentence } from "../alerts.js";
import {
  ackNotification,
  investigateNotification,
  notificationSentence,
  decideRelease,
  pendingReleaseOf,
} from "../notifications.js";
import { formatTime } from "../time.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { ListEmpty, ListHeader } from "./ListPrimitives.jsx";
import Markdown from "./Markdown.jsx";
import QuestionDisclosure from "./QuestionDisclosure.jsx";
import { TaskRow } from "./TaskList.jsx";
import {
  LocalScriptApproval,
  answerLocalScript,
} from "./detail/LocalScriptApprovals.jsx";

// usePendingLocalScripts is every local-script call waiting on a person,
// on a task the reader may act on (GET /api/local-script-approvals).
// Polled on its own rather than read off the task list: a call waits
// inside a running task, which the list has no other reason to say
// anything about, and only for as long as its script's timeout.
function usePendingLocalScripts() {
  const [pending, setPending] = useState([]);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    // Nothing while the tab is hidden, as with every other read this
    // page makes for itself (inbox-hidden-tab.spec.js): a call waiting
    // on somebody is read again the moment they look.
    const load = () => {
      if (document.hidden) return;
      Promise.resolve()
        .then(() => api("/api/local-script-approvals"))
        .then((list) => {
          if (live) setPending(Array.isArray(list) ? list : []);
        })
        .catch(() => {});
    };
    load();
    const timer = setInterval(load, 5000);
    document.addEventListener("visibilitychange", load);
    return () => {
      live = false;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, [tick]);
  return [pending, () => setTick((n) => n + 1)];
}

// LocalScriptsGroup is the inbox's group of waiting local-script calls,
// each with Approve and Deny on the row and its task one click away.
function LocalScriptsGroup({ pending, onAct, onOpenTask, reload }) {
  if (pending.length === 0) return null;
  return (
    <section className="inbox-group" key="local-scripts">
      <div className="inbox-group-header">
        <h3>Local scripts waiting for approval</h3>
        <span className="count">{pending.length}</span>
        <p className="inbox-group-hint">
          A run asks to run a script on the node host. It waits until somebody
          approves or denies the call, or until the script&apos;s timeout.
        </p>
      </div>
      <ul className="task-list">
        {pending.map((a) => (
          <li className="inbox-item" key={a.id}>
            <div className="inbox-line">
              <Button size="small" onClick={() => onOpenTask(a.taskId)}>
                Task {a.taskId}
              </Button>
            </div>
            <LocalScriptApproval
              a={a}
              onAnswer={async (approve) => {
                await onAct(() => answerLocalScript(a, approve));
                reload();
              }}
            />
          </li>
        ))}
      </ul>
    </section>
  );
}

// InboxPage is the one view that is about the reader rather than about
// the work (grain/task-20): every task grain has stopped on and cannot
// go any further with until a person does something, with that person's
// own possible answers on the row itself.
//
// # Why it is not a filter of the task list
//
// It nearly is one -- state.js's waitsOnUser is a predicate over the
// same tasks the list already holds -- and that is exactly what it was
// worth not building. A filtered list shows you the tasks and then makes
// you open each one to find out what it wants: Approve lives in one
// task's own pane, Submit in another's, and a question needs the
// timeline at the bottom of a third. Five clicks a task, on the tasks
// that by definition nobody has got round to.
//
// So the row carries the answer instead. Each group below names what the
// tasks in it are waiting for and offers exactly the responses that
// apply to that wait -- Approve on a proposal, Submit on a finished
// change, Retry on a failure -- and the reply box for a parked question
// opens in place, without leaving the page. The full pane is still one
// click away on the row itself, for everything an inbox deliberately
// does not do (editing the target, reading the transcript, closing with
// its pull request).
//
// # Answering a whole group at once
//
// A row at a time is still a click a task, and the group that most
// needs this page -- proposals -- is the one that arrives a dozen at a
// time and gets the same answer to all of them. So a group of two or
// more carries that answer for the lot in its own header ("Approve all
// (12)", "Submit all (3)"): the batch bar's trick (BatchActionsBar)
// without the selecting, since the heading has already said what these
// tasks have in common and there is nothing left to pick. Each asks
// before it fires, and only the answers that start work or move it
// along are offered this way -- see bulkActions.
//
// # The order the groups come in
//
// Urgency, not the life cycle STATE_ORDER describes. A parked question
// is a run stopped mid-sentence, and answering it is the only thing that
// starts it again, so it is first. A finished change waiting on Submit
// is work already paid for that is not landing, and an answer nobody has
// read is work already paid for that nobody has had the use of -- the
// same waste one step further along, which is why the two sit together.
// A merge the queue gave up on and a run that has exhausted its attempts
// are both stuck but have already had their say. A stalled merge has had
// no say at all -- nothing has escalated and nobody has commented -- but
// it is also the one wait here that may yet come unstuck on its own, so
// it goes beside the other merge that is not landing rather than at the
// top. A task whose own state has stood too long closes that run of
// stuck things: it is the same finding as a stalled merge one lane over
// (state.js's waitsOnUser, eighth), and it comes last of them because it
// is the coarsest -- where the other three name a pull request, a check
// or a failure, this names only a state that has not moved. Proposals
// come last: they are the one group where the honest answer is often
// "not now", and a long backlog of them should not be the first thing
// between a reader and the run that is waiting on their answer.
const GROUPS = [
  {
    id: "red_base_repair",
    title: "Base-branch repair waiting",
    hint: "A base branch is red and its repair pull request is waiting. Submit it to the merge queue or force-merge it to unblock the branch.",
    match: (t) => waitingBaseRepair(t),
    note: (t) => redBaseRepairSentence(t),
    actions: [
      {
        label: "Submit",
        variant: "contained",
        post: "/submit",
        when: (t) => !t.autoMerge,
        title:
          "Put this base-branch repair pull request on grain's merge queue: it is merged once its checks pass.",
      },
      {
        label: "Force merge",
        variant: "outlined",
        color: "warning",
        forceMerge: true,
        when: (t, config) => forceMergeOffered(config, t),
        title:
          "Force-merge this base-branch repair pull request now, overriding the merge queue.",
      },
    ],
  },
  {
    id: "awaiting_reply",
    title: "Asked: reply",
    hint: "A run stopped to ask something, you stopped its turn, or it answered on the task itself and opened no pull request. Reply to run it again, or acknowledge to close it.",
    match: (t) => t.state === "awaiting_reply",
    // Which of "Asked"'s cases this is (state.js's askedCase): a
    // question, a stopped turn, or a conversation waiting for its next
    // message.
    note: (t) => askedCase(t)?.title || null,
    // The one group whose response can be prose rather than a verb, so it
    // opens Responder below in addition to offering Acknowledge.
    reply: true,
    actions: [
      {
        label: "Acknowledge",
        variant: "outlined",
        post: "/close",
        // Closing is what acknowledging *is* here: when there is no pull
        // request to merge and no further run to wait for, the task has
        // no other ending. The button says the meaning and the title
        // says the mechanism, rather than offering a "Close" that reads
        // as giving up on work that in fact succeeded.
        title:
          "You have read it: close the task. Reopen puts it back if you need it again.",
        bulk: {
          label: "Acknowledge all",
          confirm: (n) =>
            `Acknowledge all ${n} answered tasks? Each is closed, and reopening puts one back.`,
        },
      },
    ],
  },
  // A change whose review fixed what it could and left questions only its
  // owner can settle (orchestrator's reviewquestions.go). It is
  // awaiting_submit as far as model.State is concerned, but Submit is the
  // lesser answer here: a reply is the decision, and it sends another run
  // to carry it out. Submit stays, as "ship as is".
  {
    id: "review_questions",
    title: "Review needs your decision",
    hint: "Each change's review fixed what it could and left questions only you can settle. Reply with your decision and another run carries it out, or ship it as it stands.",
    match: (t) => awaitsHumanSubmit(t) && !!t.reviewQuestions,
    reply: true,
    actions: [
      {
        label: "Ship as is",
        variant: "outlined",
        post: "/submit",
        title:
          "Submit the change as it stands, leaving the review's questions open: it goes on grain's merge queue and lands once its checks pass.",
      },
    ],
  },
  {
    id: "awaiting_submit",
    title: "Asked: submit for merge",
    hint: "Each pull request is on no queue -- some may still have a run waiting on their checks. Nothing lands them until you submit them.",
    // awaitsHumanSubmit, not the state alone: a run that opened a pull
    // request it will not merge by itself is often still running, blocked
    // in wait_for_checks, and Submit is owed just the same.
    match: (t) => awaitsHumanSubmit(t) && !t.reviewQuestions,
    // "Asked you to submit owner/repo#12", with the hold that keeps it
    // off the queue where there is one (state.js's askedCase).
    note: (t) => askedCase(t)?.title || null,
    actions: [
      {
        label: "Submit",
        variant: "contained",
        post: "/submit",
        title:
          "Put this pull request on grain's merge queue: it is merged once its checks pass.",
        bulk: {
          label: "Submit all",
          confirm: (n) =>
            `Submit all ${n} finished changes for merge? Each pull request goes on grain's merge queue and lands once its checks pass.`,
        },
      },
    ],
  },
  {
    id: "merge_blocked",
    title: "Unblock a merge",
    hint: "The merge queue gave up on landing these automatically -- its own comment on each says why.",
    // completionPhase is the whole of this test: it is non-null for a
    // completed task the queue has stopped driving and null for the
    // ordinary one sitting on it (state.js). Such a task's badge still
    // reads "Queued for merge", which is why of everything in this
    // inbox it is the case nobody finds by scrolling the list.
    match: (t) => t.state === "completed" && completionPhase(t) !== null,
    // A change the queue gave up on (merge_failed, whose sub-case control
    // is Retry merge) is claimed here before the stalled group below can
    // offer that control, so this group offers it instead: without it the
    // inbox had no way to hand the change back to the queue. Other ways
    // of stopping (a merge grain withdrew, one closed on GitHub) name no
    // control and get none.
    controls: (t) => {
      const control = reasonControl(t);
      return control?.label === "Retry merge"
        ? [{ variant: "contained", ...control }]
        : [];
    },
    actions: [
      {
        label: "Open pull request",
        href: (t) =>
          t.pullRequest
            ? pullRequestUrl(t.pullRequest, t.branch, t.base)
            : null,
        title: "Open the pull request.",
      },
    ],
  },
  // A clean change GitHub refused to merge until somebody approves it
  // (orchestrator's githubrefusal.go, state.js's awaitsGitHubApproval).
  // It is not blocked -- the queue lands it by itself the moment the
  // approval arrives -- and not stalled, so neither group above or below
  // claims it, and nothing in grain will ever give the approval: the one
  // answer is a person's, on GitHub. The queue's note names the rule and
  // the pull request, so it is the row's substance here as it is on a
  // stalled merge's.
  //
  // It comes before merge_stalled so that an approval wait the stall
  // detector has also flagged stays here: the approval is what it is
  // waiting for, and "go and look" says less than "go and approve".
  // A change red only where its base branch is red stays out: it
  // depends on the base branch's repair task, a link it shows as
  // "blocked on #N", and nobody is asked to do anything about it.
  {
    id: "github_approval",
    title: "Approve on GitHub",
    hint: "GitHub will not merge these until somebody approves them, and nothing in grain will. Once one is approved the merge queue lands it by itself.",
    match: (t) => awaitsGitHubApproval(t),
    note: (t) => t.mergeQueueNote || null,
    actions: [
      {
        label: "Open pull request",
        href: (t) =>
          t.pullRequest
            ? pullRequestUrl(t.pullRequest, t.branch, t.base)
            : null,
        title: "Open the pull request on GitHub to review and approve it.",
      },
    ],
  },
  // A merge-queue wait grain's own stall detector has given up believing
  // in (state.js's mergeQueueStalled, off the stamp orchestrator's
  // liveness.go writes). It is deliberately a group of its own rather
  // than an arm of merge_blocked above: the queue still thinks it is
  // driving this, so there is no comment on the thread saying what went
  // wrong and no Retry merge to press, and both of those are what the
  // "Unblock a merge" wording and its action promise.
  //
  // It is also the one group on this page whose answer is not a verb.
  // What a reader does about a stall is go and look at whatever the
  // queue's note names -- the run on the commit, the task ahead of this
  // one in the queue, the base branch's own repair -- so the row offers
  // the two places that lead to and says the note out loud, and nothing
  // here fires a request.
  {
    id: "merge_stalled",
    title: "Look at a stalled merge",
    hint: "grain's merge queue has stood in one state on these for longer than whatever drives that state should take, so it reads them as stuck rather than waiting. Nothing has escalated and there is nothing to press: go and look at what each note names.",
    match: (t) => mergeQueueStalled(t),
    // The queue's own account of the wait (Task.MergeQueueNote), which
    // elsewhere is a tooltip on the activity line. On this page it is the
    // substance of the row -- it is what says which check, which task
    // ahead of this one, which branch -- and a hint nobody hovers is a
    // hint nobody reads.
    note: (t) => t.mergeQueueNote || null,
    actions: [
      {
        label: "Open pull request",
        href: (t) =>
          t.pullRequest
            ? pullRequestUrl(t.pullRequest, t.branch, t.base)
            : null,
        title:
          "Open the pull request this wait is about: its checks are where a stalled wait for CI shows itself.",
      },
      {
        label: "Open task",
        // The task's own pane rather than a request: the queue's whole
        // history of this change is on the timeline there, which is what
        // tells a stall that has just begun from one that has stood all
        // morning.
        open: true,
        title:
          "Open the task's own pane: the timeline, the run, and the queue's account of this wait.",
      },
    ],
  },
  {
    id: "failed",
    title: "Retry or close",
    hint: "Dispatch has given up on these after too many failures in a row. Retry is the only way back into the queue.",
    match: (t) => t.state === "failed",
    actions: [
      {
        label: "Retry",
        variant: "contained",
        post: "/retry",
        title: "Clear the failure streak and put this task back in the queue.",
        bulk: {
          label: "Retry all",
          confirm: (n) =>
            `Retry all ${n} failed tasks? Each clears its failure streak and goes back into the queue to be dispatched again.`,
        },
      },
      {
        label: "Close",
        color: "error",
        post: "/close",
        confirm: (t) => `Close ${t.id}? It can be reopened later.`,
        title: "Give up on this task. Reopen puts it back.",
      },
    ],
  },
  // The same finding as merge_stalled above, about the task's own state
  // rather than about the merge queue's (ui.Task.Stall, orchestrator's
  // taskLane -- the other half of the same table). A queued task nothing
  // is dispatching, a run whose process is gone, a change that has sat
  // on the queue longer than the whole of that queue's own deadlines.
  //
  // t.stall is the whole of the test, and it is only ever set for the
  // three states grain itself drives (model.StateTimes.Since), so the
  // only tasks this can take from a group above are completed ones --
  // which merge_blocked and merge_stalled both claim first, and rightly:
  // where this says only "'completed' has stood too long", they say
  // which state of the merge cycle it is standing in and offer the pull
  // request it is about. What is left here is the case the merge lane
  // cannot see at all, and that is the one worth a row: a task grain is
  // not working on and has not noticed.
  //
  // The row's answer is the sub-case's own control (ui.Task.Reason,
  // orchestrator's taskReasons): this group is the task lane's
  // escalation to a person, and a person handed a stuck task is handed
  // the button that ends that kind of wait -- Run now on a queued task
  // nothing dispatches, Cancel on a run no process is finishing, Undefer
  // on a deferral whose wake time has passed, Retry merge on a change the
  // queue gave up on. A sub-case with no control (a task waiting on
  // another, a change on the merge queue) gets none: what holds it is
  // the thing waited on, and its own row is where that is dealt with.
  {
    id: "stalled",
    title: "Look at a stuck task",
    hint: "Grain has stood in one state on these for longer than that state should take, so it reads them as stuck rather than waiting. Each note says which part of grain should have moved this on; where the button that ends that kind of wait exists, it is on the row.",
    match: (t) => !!t.stall,
    controls: (t) => {
      const control = reasonControl(t);
      return control ? [{ variant: "contained", ...control }] : [];
    },
    // The sentence itself on the row, for merge_stalled's reason: on
    // this page it is the substance of the row rather than an
    // annotation, and a tooltip nobody hovers is a tooltip nobody reads.
    // The chip beside the badge carries it everywhere else
    // (TaskList.jsx).
    note: (t) => t.stall || null,
    actions: [
      {
        label: "Open task",
        // Reading rather than doing, exactly as merge_stalled's own
        // "Open task" is: the timeline is what tells a wait that has
        // just gone past its patience from one that has stood all week,
        // and what to do about either is not a button this page could
        // offer.
        open: true,
        title:
          "Open the task's own pane: its timeline, its attempts, and what grain last did about it.",
      },
    ],
  },
  {
    id: "proposed",
    title: "Approve a proposal",
    hint: "An agent proposed these. Nothing dispatches them until you approve one.",
    match: (t) => t.state === "proposed",
    actions: [
      {
        label: "Approve",
        variant: "contained",
        post: "/approve",
        title: "Let this task join the queue and be dispatched.",
        bulk: {
          label: "Approve all",
          confirm: (n) =>
            `Approve all ${n} proposals? Every one of them joins the queue and is dispatched.`,
        },
      },
      // The third answer to a proposal, and the one this page most
      // needs: "not now" is what somebody actually means about half the
      // proposals they read, and without it the choice on the row is
      // approve something you are not ready for or close something you
      // do want one day. A deferred task reads as "deferred"
      // (model.StateOf) rather than as a proposal, so answering this way
      // takes the row off this page too -- which is the whole point of
      // saying it.
      {
        label: "Defer",
        post: "/defer",
        title:
          "Put it aside. It stops waiting to be approved and drops out of the lists until you ask for it.",
        bulk: {
          label: "Defer all",
          confirm: (n) =>
            `Defer all ${n} proposals? They stop waiting to be approved and drop out of the lists until you ask for them.`,
        },
      },
      {
        label: "Decline",
        color: "error",
        post: "/close",
        confirm: (t) => `Close ${t.id} without running it? It can be reopened.`,
        title: "Close it unrun. Reopen puts it back among the proposals.",
      },
    ],
  },
];

// The one group on this page that is not about a task (grain/task-470).
//
// A base branch nothing is judging has no task to hang on: it is a
// repository, found by reading the row the merge queue already keeps
// (pkg/ui/basebranchgate.go), and every other surface it reached is one
// somebody only opens once they already suspect something -- the System
// pane and one repository's own settings. The inbox is the one place
// this deployment says "somebody has to deal with this", and the fault
// it describes went 33 hours unnoticed with roughly a hundred merges
// landing onto an untested `main`.
//
// It is first, above the parked questions, which is the only placement
// here that is not about the life cycle either. Everything below this is
// StateSyncGroup is the inbox row for a deployment admin when this
// deployment's settings have stopped reaching its state repository
// (config.stateRepoSyncFailure): the alerts panel says it to everyone
// (alerts.js, in the same words: STATE_SYNC_TITLE, stateSyncSentence), and this
// puts it on the list of things waiting on the people who can fix it.
function StateSyncGroup({ failure, onOpenConfigRepo }) {
  const zone = useTimeZone();
  if (!failure) return null;
  return (
    <section className="inbox-group" key="state-sync">
      <div className="inbox-group-header">
        <h3>{STATE_SYNC_TITLE}</h3>
        <span className="count">1</span>
        <p className="inbox-group-hint">
          Until this is fixed, settings changed here are not backed up, and a
          restart into a newer commit of the state repository has only the last
          export to merge against.
        </p>
      </div>
      <ul className="task-list">
        <li className="inbox-item">
          <div className="inbox-line">
            <div className="inbox-gate">
              <span className="inbox-gate-repo">{failure.repo}</span>
            </div>
            <div className="inbox-actions">
              {onOpenConfigRepo && (
                <Button
                  size="small"
                  variant="outlined"
                  onClick={onOpenConfigRepo}
                >
                  Config repo
                </Button>
              )}
            </div>
          </div>
          <p className="inbox-note">{stateSyncSentence(failure, zone)}</p>
        </li>
      </ul>
    </section>
  );
}

// NotificationsGroup is the open operator notifications
// (notifications.js, docs/design/durable-tasks.md DT-5), above the task
// rows: one row each, with what a person can do about it. An
// acknowledged one stays here, marked, until whatever raised it clears
// it; acknowledging only takes it out of the alerts bell's count.
function NotificationsGroup({
  notifications,
  onChanged,
  onOpenTask,
  showError,
}) {
  const zone = useTimeZone();
  const [busy, setBusy] = useState(null);
  if (!notifications || notifications.length === 0) return null;
  const act = async (n, kind) => {
    setBusy(`${n.id}:${kind}`);
    try {
      if (kind === "ack") await ackNotification(n);
      if (kind === "approve" || kind === "reject") {
        if (!(await decideRelease(pendingReleaseOf(n), kind === "approve")))
          return;
      }
      if (kind === "investigate") {
        const id = await investigateNotification(n);
        if (id) onOpenTask?.(id);
      }
      await onChanged?.();
    } catch (err) {
      showError?.(err.message);
    } finally {
      setBusy(null);
    }
  };
  return (
    <section
      className="inbox-group"
      key="notifications"
      data-testid="inbox-notifications"
    >
      <div className="inbox-group-header">
        <h3>Notifications</h3>
        <span className="count">{notifications.length}</span>
        <p className="inbox-group-hint">
          Raised by grain and its agents for a person to know about. Each clears
          itself once its condition goes away.
        </p>
      </div>
      <ul className="task-list">
        {notifications.map((n) => (
          <li
            className="inbox-item"
            key={n.id}
            data-testid="inbox-notification"
            data-severity={n.severity}
          >
            <div className="inbox-line">
              <div className="inbox-gate">
                <Alert
                  severity={muiSeverity(n.severity)}
                  variant="outlined"
                  sx={{ py: 0, px: 1, border: 0, alignItems: "center" }}
                >
                  <strong>{n.title}</strong>
                </Alert>
              </div>
              <div className="inbox-actions">
                {pendingReleaseOf(n) && (
                  <>
                    <Button
                      size="small"
                      variant="contained"
                      disabled={busy !== null}
                      onClick={() => act(n, "approve")}
                    >
                      Approve
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      color="error"
                      disabled={busy !== null}
                      onClick={() => act(n, "reject")}
                    >
                      Reject
                    </Button>
                  </>
                )}
                {!n.ackedAt && (
                  <Button
                    size="small"
                    variant="outlined"
                    disabled={busy !== null}
                    onClick={() => act(n, "ack")}
                  >
                    Acknowledge
                  </Button>
                )}
                <Button
                  size="small"
                  variant="outlined"
                  disabled={busy !== null}
                  onClick={() => act(n, "investigate")}
                >
                  {n.investigationTaskId ? "Investigation" : "Investigate"}
                </Button>
                {n.taskId && onOpenTask && (
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={() => onOpenTask(n.taskId)}
                  >
                    Open task
                  </Button>
                )}
              </div>
            </div>
            <p className="inbox-note">
              {notificationSentence(n)}{" "}
              <span className="inbox-note-meta">
                {n.subject} · since {formatTime(n.raisedAt, zone)}
                {n.ackedAt ? " · acknowledged" : ""}
              </span>
            </p>
          </li>
        ))}
      </ul>
    </section>
  );
}

// groupTasks assigns every waiting task to the first group that claims
// it, so no task is ever listed twice -- a task can be both awaiting a
// submit and carrying a merge-blocked mark, and it belongs under the one
// thing a reader can do about it first. The same order is what puts a
// queue entry that is both blocked and stalled under "Unblock a merge":
// once the queue has given up and said why, that is the fact about the
// row and the stall is how it got there.
//
// Anything waitsOnUser admits that no group claims would vanish here,
// which is the one way these two can drift apart; InboxPage.test.jsx
// checks that they don't.
function groupTasks(tasks) {
  return GROUPS.map((g) => ({
    group: g,
    tasks: tasks.filter(
      (t) => GROUPS.find((candidate) => candidate.match(t)) === g,
    ),
  })).filter((row) => row.tasks.length > 0);
}

// bulkActions are the answers a whole group can be given at once. Only
// the ones an action declares a `bulk` for: "Open pull request" would be
// a handful of pop-ups a browser blocks anyway, and Close and Decline
// are deliberately not here, because a button that closes eight tasks on
// one click is a different kind of thing from one that closes the task
// you are looking at -- the row's own Close, one confirm at a time, is
// the way to give up on work.
//
// Nothing is offered on a group of one: the row's own button is already
// under the reader's cursor, and a header full of "Approve all (1)"
// teaches everybody to click past the confirm that matters.
function bulkActions(group, rows) {
  return (group.actions || []).filter((a) => a.bulk && rows.length >= 2);
}

// NotificationSwitch is the header's switch for desktop notifications
// (inboxNotifications.js): a button rather than a prompt on load, since
// browsers only ask for permission from a click. A browser without the
// API, or one whose reader has blocked this site, gets a line saying so
// rather than a button that cannot do anything.
function NotificationSwitch() {
  const { status, toggle } = useNotificationSetting();
  if (status === "unsupported") return null;
  if (status === "denied") {
    return (
      <span
        className="hint inbox-notify"
        title="Allow notifications for this site in the browser's settings to turn them on."
      >
        Notifications blocked
      </span>
    );
  }
  return (
    <Button
      size="small"
      className="inbox-notify"
      onClick={toggle}
      title={
        status === "on"
          ? "A desktop notification is shown when something new arrives here while grain is in the background."
          : "Show a desktop notification when something new arrives here while grain is in the background."
      }
    >
      {status === "on" ? "Turn off notifications" : "Turn on notifications"}
    </Button>
  );
}

export default function InboxPage({
  tasks,
  config,
  onOpenTask,
  onAct,
  onActBatch,
  onOpenConfigRepo,
  notifications = [],
  onNotificationsChanged,
  showError,
}) {
  // Which task's reply box is open, or null. One at a time: the box is
  // as tall as the question it is answering, and two of them open at
  // once turns a list of what is waiting into a page of forms.
  const [replyTo, setReplyTo] = useState(null);
  const waiting = inboxTasks(tasks);
  const groups = groupTasks(waiting);
  const [localScripts, reloadLocalScripts] = usePendingLocalScripts();
  const syncFailure = config?.stateRepoSyncFailure || null;

  // act wraps App's own runner so a row's button can say what it did
  // before firing: the confirm is here rather than in onAct because only
  // the button knows whether its own action is the kind worth asking
  // about.
  const run = (t, action) => {
    if (action.confirm && !confirm(action.confirm(t))) return;
    onAct(() => api(`/api/tasks/${t.id}${action.post}`, { method: "POST" }));
  };

  // runAll is the same answer given to every task in one group. It
  // always asks first, however harmless the single-row version is: the
  // whole point of the button is that the reader has not opened these
  // tasks one by one, so the confirm is the only place the count is said
  // out loud before anything happens.
  //
  // onActBatch rather than a loop over onAct: it fires the requests
  // together, refreshes the list once instead of once per task, and
  // reports how many failed rather than stopping at the first -- one
  // proposal that is no longer a proposal by the time the click lands
  // should not hold up the other seven (App.jsx's actBatch).
  const runAll = (rows, action) => {
    if (!confirm(action.bulk.confirm(rows.length))) return;
    onActBatch(
      rows.map((t) => t.id),
      (id) => api(`/api/tasks/${id}${action.post}`, { method: "POST" }),
    );
  };

  return (
    <main className="inbox">
      {/* The tray, at the size a heading holds it (ItemGlyph.jsx): the
          same figure the rail's Inbox entry carries, so the entry and
          the pane it opens are visibly the same thing. */}
      <ListHeader
        title="Inbox"
        count={
          waiting.length +
          localScripts.length +
          notifications.length +
          (syncFailure ? 1 : 0)
        }
        icon={<ItemGlyph kind="inbox" size={20} />}
        action={<NotificationSwitch />}
      />
      {/* Deliberately the same sentence it always was, and about tasks
          only: a line promising that every base branch is tested is the
          exact reassurance this page was extended to stop giving. */}
      {groups.length === 0 &&
        localScripts.length === 0 &&
        notifications.length === 0 &&
        !syncFailure && (
          <ListEmpty>
            Nothing is waiting on you. Every task is either running, waiting or
            done.
          </ListEmpty>
        )}
      <NotificationsGroup
        notifications={notifications}
        onChanged={onNotificationsChanged}
        onOpenTask={onOpenTask}
        showError={showError}
      />
      <StateSyncGroup
        failure={syncFailure}
        onOpenConfigRepo={onOpenConfigRepo}
      />
      <LocalScriptsGroup
        pending={localScripts}
        onAct={onAct}
        onOpenTask={onOpenTask}
        reload={reloadLocalScripts}
      />
      {groups.map(({ group, tasks: rows }) => (
        <section className="inbox-group" key={group.id}>
          <div className="inbox-group-header">
            <h3>{group.title}</h3>
            <span className="count">{rows.length}</span>
            <div className="inbox-group-actions">
              {/* Whole-group answers, when there is more than one task
                  to give them to (bulkActions above). */}
              {bulkActions(group, rows).map((action) => (
                <Button
                  key={action.bulk.label}
                  size="small"
                  variant="outlined"
                  onClick={() => runAll(rows, action)}
                >
                  {action.bulk.label} ({rows.length})
                </Button>
              ))}
            </div>
            <p className="inbox-group-hint">{group.hint}</p>
          </div>
          <ul className="task-list">
            {rows.map((t) => {
              // What this group has to say about this particular task,
              // where it has something: the merge queue's own note on a
              // stalled wait (GROUPS' merge_stalled).
              const note = group.note?.(t) || null;
              return (
                <li className="inbox-item" key={t.id}>
                  <div className="inbox-line">
                    <TaskRow
                      t={t}
                      config={config}
                      onOpenTask={onOpenTask}
                      hideQuestion={!!group.reply}
                    />
                    <div className="inbox-actions">
                      {t.changelists?.length > 0 && (
                        <ClChip cl={t.changelists[0]} primary />
                      )}
                      {group.reply && (
                        <Button
                          size="small"
                          variant={replyTo === t.id ? "outlined" : "contained"}
                          onClick={() =>
                            setReplyTo(replyTo === t.id ? null : t.id)
                          }
                        >
                          {replyTo === t.id ? "Cancel" : "Reply"}
                        </Button>
                      )}
                      {[
                        ...(group.controls?.(t) || []),
                        ...(group.actions || []),
                      ]
                        .filter(
                          (action) => !action.when || action.when(t, config),
                        )
                        .map((action) =>
                          action.href ? (
                            <Button
                              key={action.label}
                              size="small"
                              variant={action.variant || "outlined"}
                              color={action.color}
                              title={action.title}
                              href={action.href(t) || undefined}
                              disabled={!action.href(t)}
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              {action.label}
                            </Button>
                          ) : (
                            <Button
                              key={action.label}
                              size="small"
                              variant={action.variant || "outlined"}
                              color={action.color}
                              title={action.title}
                              // `open` is the third kind of answer a row can
                              // carry, beside a link out and a request: the
                              // task's own pane, for a wait whose next step
                              // is reading rather than doing.
                              onClick={() =>
                                action.open
                                  ? onOpenTask(t.id)
                                  : action.forceMerge
                                    ? promptForceMerge(t, (fn) =>
                                        onAct(fn, t.id),
                                      )
                                    : run(t, action)
                              }
                            >
                              {action.label}
                            </Button>
                          ),
                        )}
                    </div>
                  </div>
                  {/* The note is grain's own sentence, and the page it
                      names -- the GitHub review an approval wait needs --
                      is a link here as it is in the task's pane. */}
                  {note && (
                    <p className="inbox-note">
                      <LinkedNote text={note} />
                    </p>
                  )}
                  {group.reply && (
                    <Responder
                      t={t}
                      open={replyTo === t.id}
                      onAct={onAct}
                      onOpenTask={onOpenTask}
                      onSent={() => setReplyTo(null)}
                      showError={showError}
                    />
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </main>
  );
}

// Responder shows the question a parked task is waiting on directly on
// the row, and opens the reply composer under it when the reader clicks
// Reply -- the only part of a task's own pane this page brings with it,
// because a reader should see what is being asked up front without
// having to open a box or leave the page.
//
// It fetches the task's detail on mount rather than reading only the row
// it hangs off: the list shape carries neither the conversation nor
// pendingSecret (pkg/ui's Task vs TaskDetail), and both matter here.
//
// pendingSecret is why this is a fetch and not a textarea. A run that
// asked for a credential (mcp's request_secret) parks in exactly the
// same state as one that asked a question, and a reply is a comment --
// stored in the conversation and fed to the next run's prompt. Offering
// an undifferentiated box here would invite somebody to paste a
// credential into it. So a task parked on a secret gets no box at all on
// this page: it gets the sentence saying what it wants and a way through
// to the write-only field on its own pane (DetailOverlay's
// PendingSecret), which is the one place a value can be typed without
// ending up in the task.
function Responder({ t, open, onAct, onOpenTask, onSent, showError }) {
  const [detail, setDetail] = useState(null);
  const [failed, setFailed] = useState(false);
  const prefetchedIdRef = useRef(null);
  // Uncontrolled, for the reason Timeline's own reply box is
  // (DetailOverlay.jsx): App re-renders this page every poll, and a
  // draft nobody has sent yet should survive that.
  const bodyRef = useRef(null);
  const sending = useRef(false);
  // Whether the box holds anything worth sending -- tracked beside the
  // uncontrolled value rather than instead of it, so Send can say it has
  // nothing to send rather than being pressed and doing nothing.
  const [blank, setBlank] = useState(true);

  useEffect(() => {
    setDetail(null);
    setFailed(false);
    setBlank(true);
    prefetchedIdRef.current = null;
  }, [t.id]);

  useEffect(() => {
    if (!open) {
      setBlank(true);
    }
  }, [open]);

  useEffect(() => {
    if (detail !== null) return;
    if (!open && prefetchedIdRef.current === t.id) return;
    let live = true;
    let fetched = false;
    const load = () => {
      if (fetched) return;
      if (document.hidden && !open) return;
      fetched = true;
      if (!open) prefetchedIdRef.current = t.id;
      setFailed(false);
      Promise.resolve()
        .then(() => api(`/api/tasks/${t.id}`))
        .then((d) => {
          if (live && d) setDetail(d);
        })
        .catch((err) => {
          if (!live) return;
          if (open) {
            setFailed(true);
            showError(err);
          }
        });
    };
    load();
    document.addEventListener("visibilitychange", load);
    return () => {
      live = false;
      document.removeEventListener("visibilitychange", load);
    };
  }, [t.id, detail, open, showError]);

  const handleQuestionClick = (ev) => {
    if (!onOpenTask) return;
    if (
      ev.button !== 0 ||
      ev.metaKey ||
      ev.ctrlKey ||
      ev.shiftKey ||
      ev.altKey
    ) {
      return;
    }
    const id = taskRefFromHref(ev.target.closest?.("a")?.getAttribute("href"));
    if (id) {
      ev.preventDefault();
      ev.stopPropagation();
      onOpenTask(id);
    }
  };

  // The box closes only once the reply has landed: onAct reports a
  // failure itself and resolves false, and closing anyway would unmount
  // the box and the draft in it with the error (grain/task-1699).
  const send = async () => {
    if (sending.current) return;
    const body = bodyRef.current?.value || "";
    if (!body.trim()) return;
    sending.current = true;
    try {
      const posted = await onAct(() =>
        api(`/api/tasks/${t.id}/comments`, {
          method: "POST",
          body: JSON.stringify({ body }),
        }),
      );
      if (posted) onSent();
    } finally {
      sending.current = false;
    }
  };

  const effectiveDetail =
    detail || (Array.isArray(t.comments) || t.pendingSecret ? t : null);
  const question = effectiveDetail
    ? (effectiveDetail.comments || [])[effectiveDetail.comments.length - 1]
    : null;

  const renderedQuestionBody = question?.body
    ? question.authorKind === "automation"
      ? linkTaskRefs(question.body)
      : question.body
    : "";
  // A question somebody is asked -- an agent's, carrying the one-line
  // summary it gave -- is shown by that line alone, with the rest behind
  // an expander. Anything else this box shows (a review's open
  // questions, a report waiting to be read) is grain's own text with no
  // summary of its own to stand in for it, so it is shown whole.
  const shown = (expanded) =>
    effectiveDetail?.state === "awaiting_reply" ? (
      <QuestionDisclosure
        t={effectiveDetail}
        question={question}
        renderedBody={renderedQuestionBody}
        defaultExpanded={expanded}
      />
    ) : (
      <Markdown className="inbox-question">{renderedQuestionBody}</Markdown>
    );

  if (!open) {
    if (!effectiveDetail || effectiveDetail.pendingSecret || !question?.body) {
      return null;
    }
    return (
      <div className="inbox-responder" onClick={handleQuestionClick}>
        {shown(false)}
      </div>
    );
  }

  if (failed) {
    return (
      <div className="inbox-responder">
        <Button size="small" onClick={() => onOpenTask(t.id)}>
          Open the task to reply
        </Button>
      </div>
    );
  }
  if (effectiveDetail === null) {
    return (
      <div className="inbox-responder">
        <CircularProgress size={16} />
      </div>
    );
  }
  if (effectiveDetail.pendingSecret) {
    return (
      <div className="inbox-responder">
        <Alert severity="info" icon={false}>
          This run asked for a credential ({effectiveDetail.pendingSecret.name}
          ), not for an answer. It is set on the task&apos;s own pane, in a
          write-only box that puts the value straight into grain&apos;s secret
          store instead of into this conversation.
          <div className="inbox-responder-row">
            <Button
              size="small"
              variant="contained"
              onClick={() => onOpenTask(t.id)}
            >
              Open the task
            </Button>
          </div>
        </Alert>
      </div>
    );
  }
  return (
    <div className="inbox-responder" onClick={handleQuestionClick}>
      {question ? (
        // Reply opens the whole question with the box: the answer is
        // written to it, not to its summary.
        shown(true)
      ) : (
        <p className="hint">
          This run parked without leaving a question behind it.
        </p>
      )}
      <div className="inbox-responder-row">
        <TextField
          multiline
          rows={2}
          size="small"
          fullWidth
          autoFocus
          placeholder="Reply..."
          inputRef={bodyRef}
          inputProps={{ "aria-label": `Reply to ${t.id}` }}
          onChange={(e) => setBlank(!e.target.value.trim())}
        />
        <Button variant="contained" onClick={send} disabled={blank}>
          Send
        </Button>
      </div>
    </div>
  );
}
