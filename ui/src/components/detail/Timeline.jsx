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

import { useRef, useState } from "react";
import { Button, Link, TextField } from "@mui/material";
import LinkIcon from "@mui/icons-material/Link";
import api from "../../api.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { useTimelineOrder } from "../../TimelineOrderContext.jsx";
import { formatDateTime } from "../../time.js";
import { formatTokens } from "../settings/UsageCard.jsx";
import fileToAttachment from "../../attachments.js";
import {
  STATE_LABELS,
  outcomeLabel,
  pullRequestUrl,
  isAgentRunning,
  isTaskRepairing,
} from "../../state.js";
import AttachmentLinks from "../AttachmentLinks.jsx";
import AttachmentPicker, {
  getPastedImages,
  getDroppedFiles,
} from "../AttachmentPicker.jsx";
import AttemptTranscriptOverlay from "../AttemptTranscriptOverlay.jsx";
import CopyButton from "../CopyButton.jsx";
import ExpandableMarkdown from "../ExpandableMarkdown.jsx";
import StateDot from "../StateDot.jsx";
import { linkTaskRefs, taskRefFromHref } from "../../taskRefs.js";
import { commentAuthor, transitionActor } from "../../principal.js";
import { commentUrl } from "../../commentLinks.js";

// OUTCOME_BADGES covers model.Run's own outcome vocabulary, the same
// one state.js's OUTCOME_LABELS gives the words for -- see that map's
// own doc comment for what each outcome is and why the odd ones out
// ("paused", "capacity") take the badge they do. The labels live there
// rather than here because a task's outcomes are read in two places now:
// this timeline, and the chat an interactive task's conversation is
// rendered as (chat.js). The badges have only ever been read here.
const OUTCOME_BADGES = {
  "": "running",
  succeeded: "completed",
  failed: "failed",
  cancelled: "closed",
  paused: "closed",
  capacity: "closed",
  "setup-failed": "failed",
  "finish-failed": "failed",
  no_action: "failed",
  nothing_needed: "completed",
  orphaned: "failed",
  interrupted: "closed",
};

// PR_EVENT_LABELS covers ui.PullRequestEvent's own Kind vocabulary
// (bwsalmon/agents#493) -- "closed" specifically means closed *without*
// merging, since a merged PR is always "merged" (pullRequestEventsFrom's
// own doc comment), which the label spells out rather than leaving a
// reader to infer from "closed" alone what a task's own bare "Closed"
// state transition already uses that word for.
const PR_EVENT_LABELS = {
  opened: "PR opened",
  merged: "PR merged",
  closed: "PR closed without merging",
};

// setupBreadcrumb is how far grain got in setting an attempt up before
// it stopped -- ui.Attempt.SetupNote, the last phrase grain stamped on
// the run while it was still building its sandbox, cloning its repo or
// minting its credentials, left standing because the run ended before
// its agent ever started.
//
// It is null for almost every attempt, and that is the ordinary case:
// grain clears its last phrase as it hands a run to its agent, so a note
// survives only on a run that never got that far -- a "setup-failed"
// one, or a "failed"/"cancelled" one whose checkout or capabilities
// broke first. Nothing an agent wrote through update_status is ever
// carried here (attemptFrom's own doc comment on why: an agent's last
// words beside a failed attempt read as that attempt's diagnosis and are
// not one).
//
// Only for a finished attempt. A live one is already narrated as
// something happening now, at the top of this same page (runActivity),
// and the wording below is deliberately past tense -- this is a
// breadcrumb, not a status.
//
// The verb follows the outcome: a run somebody cancelled, one grain
// paused for want of agent budget, or one the provider had no capacity
// for, did not give up on anything.
// usageLine is a run's or a task's token usage in one line: billable
// tokens (input, output and cache writes), then the breakdown.
function usageLine(u) {
  const billable =
    u.billable ??
    (u.inputTokens || 0) + (u.outputTokens || 0) + (u.cacheWriteTokens || 0);
  let line = `${formatTokens(billable)} tokens (${formatTokens(u.inputTokens)} in, ${formatTokens(u.outputTokens)} out`;
  if (u.cacheReadTokens) line += `, ${formatTokens(u.cacheReadTokens)} cached`;
  line += ")";
  if (u.costUsd) line += ` · $${u.costUsd.toFixed(2)}`;
  return line;
}

function usageTitle(u) {
  return [
    `${u.inputTokens || 0} input tokens`,
    `${u.outputTokens || 0} output tokens`,
    `${u.cacheReadTokens || 0} read from cache`,
    `${u.cacheWriteTokens || 0} written to cache`,
    u.costUsd ? `$${u.costUsd} reported by the provider` : "",
  ]
    .filter(Boolean)
    .join(", ");
}

function setupBreadcrumb(a) {
  if (!a.setupNote || !a.finishedAt) return null;
  const stopped =
    a.outcome === "cancelled" ||
    a.outcome === "paused" ||
    a.outcome === "capacity";
  return `${stopped ? "stopped while" : "gave up while"}: ${a.setupNote}`;
}

// timelineTime is a timestamp off the wire as a Date, or null when there
// is no usable one: missing, unparseable (an Invalid Date's NaN makes any
// comparator inconsistent, which can scramble the whole sort rather than
// just misplace the one entry), or Go's zero time.Time
// ("0001-01-01T00:00:00Z") -- which is what an unset time field
// serialises as, and which would otherwise sort before everything else
// on the task.
function timelineTime(value) {
  if (!value) return null;
  const at = new Date(value);
  const ms = at.getTime();
  return Number.isFinite(ms) && ms > 0 ? at : null;
}

// RANK is the causal order of events stamped with the same instant. The
// API sends every field at its store's full precision, but GitHub's own
// pull request times come in whole seconds and several of grain's own
// moments are written in one breath (an attempt finishing and its task
// completing), so same-instant ties are ordinary -- and broken by kind of
// event, ties came out in the order the arrays were stitched together
// rather than the order things can actually happen in.
const RANK = {
  proposed: 0,
  queued: 1,
  deferred: 1,
  running: 2,
  comment: 3,
  awaiting_reply: 3.5,
  pullRequest: 4,
  attemptFinish: 5,
  requeue: 5.5,
  completed: 6,
  failed: 6,
  awaiting_submit: 6,
  closed: 7,
  // A reopen recorded at the same instant as its close undid it, so it
  // comes after.
  reopened: 7.5,
};

// transitionRank is where a state transition falls among same-instant
// events. A requeue model.Transitions could only infer stands at the
// finish of the attempt before it, so it goes after that attempt rather
// than ahead of it.
// The timeline's titles for a change to a task's auto-merge, each a
// transition of its own (model.MergeTaskEvents).
const AUTO_MERGE_STEPS = {
  auto_merge_on: "Auto-merge turned on",
  auto_merge_off: "Auto-merge turned off",
  auto_merge_kept_off: "Auto-merge kept off",
  // A human-review hold raised and released (model.LinkHumanReviewHold):
  // who raised it is the review task, who released it the person.
  human_review_requested: "Held for a person's review",
  human_review_released: "Human-review hold released",
};

// ADMISSION_LABELS names each admission value the way the task pane's
// Admission select does (detail/Actions.jsx).
const ADMISSION_LABELS = {
  standard: "Standard",
  bypass: "Bypass queue",
  durable: "Durable",
};

// HOSTING_LABELS names each hosting value the way the task pane's
// Hosting select does (detail/Actions.jsx).
const HOSTING_LABELS = {
  unattended: "Unattended",
  console: "Interactive console",
};

function isSettingOrAuditStep(state) {
  return (
    Boolean(AUTO_MERGE_STEPS[state]) ||
    state === "owner_changed" ||
    state === "admission_changed" ||
    state === "hosting_changed"
  );
}

function renderTaskRefLink(taskId, label, onOpenTask, testId) {
  return (
    <Link
      href={`/tasks/${encodeURIComponent(taskId)}`}
      data-testid={testId}
      onClick={(ev) => {
        if (
          !onOpenTask ||
          ev.button !== 0 ||
          ev.metaKey ||
          ev.ctrlKey ||
          ev.shiftKey ||
          ev.altKey
        ) {
          return;
        }
        ev.preventDefault();
        ev.stopPropagation();
        onOpenTask(taskId);
      }}
    >
      {label}
    </Link>
  );
}

function renderProposedAttribution(tr, onOpenTask) {
  if (tr.proposingTaskId) {
    const noun = tr.actor?.source === "session" ? "session" : "task";
    const runLabel =
      tr.proposingRunAttempt > 0
        ? `run #${tr.proposingRunAttempt}`
        : tr.proposingRunId
          ? `run ${tr.proposingRunId}`
          : "";
    const behalf = tr.onBehalfOfOwner || tr.onBehalfOf?.id || "";
    return (
      <>
        by {noun}{" "}
        {renderTaskRefLink(
          tr.proposingTaskId,
          `#${tr.proposingTaskId}`,
          onOpenTask,
          "proposing-task-link",
        )}
        {runLabel && (
          <>
            {" ("}
            {renderTaskRefLink(
              tr.proposingTaskId,
              runLabel,
              onOpenTask,
              "proposing-run-link",
            )}
            {")"}
          </>
        )}
        {behalf && <> on behalf of {behalf}</>}
      </>
    );
  }
  const actor = transitionActor(tr);
  return actor ? <>by {actor}</> : null;
}

function transitionRank(tr) {
  if (tr.state === "queued" && tr.inferred) return RANK.requeue;
  return RANK[tr.state] ?? RANK.comment;
}

// placeUndated gives each event in one kind's own list (in the order the
// API sent them, which is that kind's own order -- comments by id, pull
// request events opened before merged or closed) that has no usable time
// the time of its nearest dated neighbour before it, or failing that
// after it, to sort by. The sort's last tiebreak (seq) then keeps it
// beside that neighbour, on the side it was sent on. An event with no
// dated neighbour at all keeps no sort time and goes at the end, saying
// "time unknown" -- never at the epoch.
function placeUndated(list) {
  let prev = null;
  list.forEach((e) => {
    if (e.sortAt) prev = e.sortAt;
    else e.sortAt = prev;
  });
  let next = null;
  for (let i = list.length - 1; i >= 0; i--) {
    const e = list[i];
    if (e.at) next = e.at;
    else if (!e.sortAt) e.sortAt = next;
  }
}

// CLOSE_SYNC_WINDOW is how long after a task closes a pull request merged
// or closed event is still taken for the sync catching up with the close
// rather than for something that happened afterwards.
const CLOSE_SYNC_WINDOW_MS = 60 * 60 * 1000;

// timelineEvents merges everything grain has recorded about a task --
// every state transition (bwsalmon/agents#452's t.transitions, which
// already covers filed/queued/running/awaiting_reply/failed/completed/
// closed), every attempt's own result (bwsalmon/agents#445's
// t.attempts, with the outcome and detail a bare state change doesn't
// carry), every pull request event (bwsalmon/agents#493's
// t.pullRequestEvents -- opened, merged, or closed without merging), and
// every comment -- into one list, oldest first, each carrying the
// timestamp it happened at. There is no unified "history" on the wire:
// this just interleaves TaskDetail's own separate arrays by their own
// timestamps, client-side, with RANK breaking same-instant ties.
//
// Always oldest first, whichever way the reader has asked to see it:
// this is where a pull request's close is kept ahead of the task's own
// "Closed" and a re-linked pull request is clamped to the start of the
// story, and both of those are statements about chronological order.
// Timeline reverses the finished list when the preference says newest
// first (TimelineOrderContext), so that the two ends stay each other's
// mirror rather than two orderings that have to agree.
//
// Each event has two times: at, the one it shows (null for none), and
// sortAt, the one it is ordered by. They differ only where ordering by
// the shown time would tell the story wrong -- an attempt still running,
// an undated event, a pull request synced in just after its task closed.
//
// t.transitions omits a state the record has no timestamp for -- most
// notably a past awaiting_reply period once its question has been
// answered (see model.Transitions' own doc comment on why that one is
// unrecoverable) -- so this can skip a state without that being a bug to
// chase. One it could only place approximately comes flagged inferred,
// and its time is shown as approximate.
function timelineEvents(t, zone, now = Date.now(), onOpenTask = null) {
  const events = [];
  const push = (e) => events.push({ ...e, seq: events.length });
  const transitions = t.transitions || [];
  const attempts = t.attempts || [];
  // Every "running" period already gets its own, more detailed node from
  // t.attempts below (start/finish time, outcome, a transcript to click
  // into) -- so once attempts exist, the plain state transition into
  // "running" is a second node for the same run (bwsalmon/agents#503:
  // "a running node ... followed by a running attempt #1 ... we only
  // need a single node"). Older tasks or edge cases with transitions but
  // no recorded attempts still show the bare transition, since it's the
  // only record of that running period they have.
  const shownTransitions =
    attempts.length > 0
      ? transitions.filter((tr) => tr.state !== "running")
      : transitions;
  // Only a lifecycle step can be the task's current state: an auto-merge
  // change (model.TaskEventAutoMergeOn/Off/KeptOff) or ownership transfer
  // (model.TaskEventOwnerChanged) is a note about one setting, not a state
  // the task moved into.
  const lastTransitionIndex = shownTransitions.findLastIndex(
    (tr) => !isSettingOrAuditStep(tr.state),
  );

  const transitionEvents = shownTransitions.map((tr, i) => {
    const at = timelineTime(tr.at);
    const actorNode =
      tr.state === "proposed" ? (
        renderProposedAttribution(tr, onOpenTask)
      ) : transitionActor(tr) ? (
        <>by {transitionActor(tr)}</>
      ) : null;
    const ownerTransferDetail =
      tr.state === "owner_changed" && (tr.oldOwner || tr.newOwner)
        ? `from ${tr.oldOwner || "?"} to ${tr.newOwner || "?"}`
        : "";
    // A change to the admission setting (model.TaskEventAdmissionChanged,
    // unified-grants.design.md §4.1) says the old and new values.
    const admissionDetail =
      tr.state === "admission_changed" && (tr.oldAdmission || tr.newAdmission)
        ? `from ${ADMISSION_LABELS[tr.oldAdmission] || tr.oldAdmission || "?"} to ${ADMISSION_LABELS[tr.newAdmission] || tr.newAdmission || "?"}`
        : "";
    // A change to the hosting setting (model.TaskEventHostingChanged,
    // unified-grants.design.md §4.1) says the old and new values.
    const hostingDetail =
      tr.state === "hosting_changed" && (tr.oldHosting || tr.newHosting)
        ? `from ${HOSTING_LABELS[tr.oldHosting] || tr.oldHosting || "?"} to ${HOSTING_LABELS[tr.newHosting] || tr.newHosting || "?"}`
        : "";
    const shownDetail =
      ownerTransferDetail ||
      admissionDetail ||
      hostingDetail ||
      tr.detail ||
      "";
    const shownReason =
      tr.state === "owner_changed" ||
      tr.state === "admission_changed" ||
      tr.state === "hosting_changed"
        ? ""
        : tr.reason || "";
    return {
      key: `transition-${tr.state}-${i}`,
      at,
      sortAt: at,
      rank: transitionRank(tr),
      // A reopen is not a state of its own (model.TransitionReopened):
      // the task is back to waiting on whatever its record implies, so it
      // takes the waiting dot.
      badge: isSettingOrAuditStep(tr.state)
        ? "comment"
        : tr.state === "reopened"
          ? "queued"
          : tr.state,
      inferred: Boolean(tr.inferred),
      lifecycle: !isSettingOrAuditStep(tr.state),
      // Only the most recent transition can still be "now" -- a running
      // entry earlier in the list is over and done, so its dot should
      // read as static rather than keep spinning as if the task were
      // still running that far in the past.
      current: i === lastTransitionIndex,
      closedTransition: tr.state === "closed",
      render: () => (
        <>
          <div className="timeline-title">
            {tr.state === "reopened"
              ? "Reopened"
              : tr.state === "owner_changed"
                ? "Owner changed"
                : tr.state === "admission_changed"
                  ? "Admission changed"
                  : tr.state === "hosting_changed"
                    ? "Hosting changed"
                    : AUTO_MERGE_STEPS[tr.state] ||
                      STATE_LABELS[tr.state] ||
                      tr.state}
          </div>
          {/* Who proposed, closed, reopened, or transferred it, why, and
              what the step did (model.TaskEvent). */}
          {(actorNode || shownReason || shownDetail) && (
            <div className="timeline-meta timeline-transition-by">
              {actorNode}
              {shownReason && (
                <>
                  {actorNode ? " · " : ""}
                  {shownReason}
                </>
              )}
              {shownDetail && (
                <>
                  {actorNode || shownReason ? " · " : ""}
                  {shownDetail}
                </>
              )}
            </div>
          )}
        </>
      ),
    };
  });
  placeUndated(transitionEvents);
  transitionEvents.forEach(push);

  attempts.forEach((a) => {
    // The very first attempt *is* the task's one "running" node -- calling
    // it out as "Attempt #1" only makes sense once there's a second one to
    // tell it apart from (bwsalmon/agents#503).
    const title =
      a.number === 1
        ? outcomeLabel(a.outcome)
        : `Attempt #${a.number} · ${outcomeLabel(a.outcome)}`;
    const breadcrumb = setupBreadcrumb(a);
    const startedAt = timelineTime(a.startedAt);
    const finishedAt = timelineTime(a.finishedAt);
    // An attempt's node carries its outcome, so it stands where that
    // outcome was reached -- its finish -- with its start in the meta
    // line below. Placed at its start, "Attempt #3 · Succeeded" read as
    // coming before every comment and question posted while it ran. One
    // still running has no outcome yet and is still happening, so it sorts
    // as now, which is after everything said during it so far; it shows
    // when it started.
    let sortAt = finishedAt;
    if (!sortAt && !a.finishedAt) {
      sortAt = new Date(Math.max(now, startedAt?.getTime() ?? 0));
    }
    sortAt = sortAt || startedAt;
    push({
      key: `attempt-${a.number}`,
      at: finishedAt || startedAt,
      sortAt,
      rank: RANK.attemptFinish,
      lifecycle: true,
      badge: OUTCOME_BADGES[a.outcome || ""] || "queued",
      // attempt, not set on any other kind of event, is what tells
      // Timeline's own render loop which <li> to make interactive: typing
      // on a task attempt is how AttemptTranscriptOverlay opens
      // (bwsalmon/agents#446).
      attempt: a,
      // Same reasoning as transitions above: a "running" badge only
      // belongs to an attempt that hasn't finished yet, never a past one.
      current: !a.finishedAt,
      render: () => (
        <>
          {/* The entry's keyboard way in (Timeline's own render loop):
              a real button, which Tab reaches and Enter or Space press,
              named by the title it shows. Its click is the <li>'s. */}
          <div className="timeline-title">
            <button type="button" className="timeline-attempt-open">
              {title}
            </button>
          </div>
          <div className="timeline-meta">
            started {formatDateTime(a.startedAt, zone)}
            {a.finishedAt && (
              <> · finished {formatDateTime(a.finishedAt, zone)}</>
            )}
            {/* The model this attempt actually ran on (ui.Attempt.Model):
                what the task asks for -- the deployment default, the
                rotation -- can come out differently each attempt. Kept
                whole, separator and all, so a phone's line breaks before
                it rather than inside the finished time. */}
            {a.model && (
              <>
                {" "}
                <span
                  className="timeline-attempt-ran-on"
                  title={
                    a.agentFramework
                      ? `Ran on ${a.model} (${a.agentFramework})`
                      : `Ran on ${a.model}`
                  }
                >
                  · <span className="timeline-attempt-model">{a.model}</span>
                </span>
              </>
            )}
          </div>
          {/* What its capability grants recorded doing, listed in the
              same overlay the entry opens (CapabilityAudit). A line of its
              own: on the meta line it pushed a phone's "finished" time
              onto two lines. */}
          {/* Which grain hostd its agent CLI ran on -- its model
              connection's -- the machine that hostd ran on and its owner
              (ui.Attempt.Hostd, read-only), on a line of its own for the
              same reason. A capability server placed on a hostd of its
              own says which on each of its audit records. */}
          {a.hostd?.hostd && (
            <div
              className="timeline-meta timeline-attempt-hostd"
              title={[
                `The agent CLI ran on host ${a.hostd.hostd}`,
                a.hostd.deleted &&
                  "since deleted from the host registry (Settings → Hosts)",
                a.hostd.instance && `on ${a.hostd.instance}`,
                a.hostd.owner && `owned by ${a.hostd.owner}`,
              ]
                .filter(Boolean)
                .join(", ")}
            >
              ran on host {a.hostd.hostd}
              {a.hostd.deleted && (
                <span className="timeline-attempt-hostd-deleted">
                  {" "}
                  (deleted)
                </span>
              )}
              {a.hostd.instance && <> on {a.hostd.instance}</>}
              {a.hostd.owner && <> · {a.hostd.owner}</>}
            </div>
          )}
          {a.capabilityAuditCount > 0 && (
            <div className="timeline-meta">
              {a.capabilityAuditCount} capability audit record
              {a.capabilityAuditCount === 1 ? "" : "s"}
            </div>
          )}
          {/* What it spent at its provider, as its CLI reported it
              (ui.Attempt.Usage, docs/connections-and-models.md). */}
          {a.usage && (
            <div
              className="timeline-meta timeline-attempt-usage"
              data-testid="attempt-usage"
              title={usageTitle(a.usage)}
            >
              {usageLine(a.usage)}
            </div>
          )}
          {/* Marked as grain's own words, the same way the live phrase
              is on the task row (TaskList.jsx): every other sentence
              shown about an attempt is the agent's, and "gave up while"
              is grain talking about the setup it was doing, not the
              agent talking about its work. */}
          {breadcrumb && (
            <div
              className="timeline-setup-note"
              title="How far grain got in setting this attempt up before it stopped"
            >
              <span className="task-activity-by">grain</span>
              {breadcrumb}
            </div>
          )}
          {a.detail && <div className="timeline-detail">{a.detail}</div>}
        </>
      ),
    });
  });

  // A pull request grain re-linked (say, a task reopened against a PR that
  // had already been open for a while) keeps that PR's real, original
  // open time -- which can predate this lifecycle's own first transition,
  // sorting "PR opened" ahead of "Proposed"/"Queued" and everything else
  // rather than where it actually belongs (bwsalmon/agents#503: "PR
  // opened comes out of order at the beginning of the timeline"). Clamping
  // it to this lifecycle's own start keeps it from floating above events
  // that actually happened first.
  const earliestAt = transitionEvents.find((e) => e.sortAt)?.sortAt || null;

  const prEvents = (t.pullRequestEvents || []).map((e, i) => {
    let at = timelineTime(e.at);
    if (e.kind === "opened" && at && earliestAt && at < earliestAt)
      at = earliestAt;
    return {
      key: `pr-event-${i}`,
      at,
      sortAt: at,
      rank: RANK.pullRequest,
      prKind: e.kind,
      badge: `pr_${e.kind}`,
      render: () => (
        <>
          <div className="timeline-title">
            {PR_EVENT_LABELS[e.kind] || e.kind}
          </div>
          {t.pullRequest && (
            <div className="timeline-meta">
              <Link
                href={pullRequestUrl(t.pullRequest, t.branch, t.base)}
                target="_blank"
                rel="noopener noreferrer"
              >
                {t.pullRequest}
              </Link>
            </div>
          )}
        </>
      ),
    };
  });
  placeUndated(prEvents);

  // Closing can outrun the sync that notices its pull request followed
  // suit -- a "merged"/"closed" pull request event synced in afterward
  // would otherwise land after "Closed" by timestamp but read strangely
  // once "Closed" is already the story's end (bwsalmon/agents#503: "PR
  // closing is coming after the closed state, let's make closed the last
  // thing in the timeline"). So such an event is ordered as of the close
  // it trails, which RANK puts just ahead of it -- but only one that
  // trails a close closely, with no attempt or other state change in
  // between. A task closed and then reopened, re-dispatched or redone has
  // its later history after its "Closed", by its own timestamps, and so
  // does a pull request that merged long after the task closed: pinning
  // "Closed" last regardless made it read as the latest event on tasks
  // whose story had carried on past it.
  const closes = transitionEvents.filter((e) => e.closedTransition && e.sortAt);
  const lifecycle = events.filter((e) => e.lifecycle && !e.closedTransition);
  prEvents.forEach((e) => {
    if (!e.at || (e.prKind !== "merged" && e.prKind !== "closed")) return;
    const at = e.at.getTime();
    const close = closes
      .filter((c) => {
        const closedAt = c.sortAt.getTime();
        return (
          closedAt <= at &&
          at - closedAt <= CLOSE_SYNC_WINDOW_MS &&
          !lifecycle.some((l) => {
            const lt = l.sortAt?.getTime();
            return lt > closedAt && lt <= at;
          })
        );
      })
      .pop();
    if (close) e.sortAt = close.sortAt;
  });
  prEvents.forEach(push);

  const commentEvents = (t.comments || []).map((c, i) => {
    // onBehalfOf is set when grain relayed somebody else's words -- a
    // question from a dispatched run reads as grain speaking for an
    // agent, not as grain's own.
    // Its label (operator-attribution spec §4.1) names a verified
    // on-behalf-of itself (commentAuthor).
    const who = commentAuthor(c);
    const at = timelineTime(c.createdAt);
    return {
      key: `comment-${i}`,
      // The comment's own id, which a /tasks/<id>#comment=<id> link
      // names (commentLinks.js) and the entry is marked with.
      commentId: c.id,
      at,
      sortAt: at,
      rank: RANK.comment,
      badge: "comment",
      render: () => (
        <>
          <div className="timeline-meta timeline-comment-header">
            <span>
              {who} · {c.authorKind}
              {c.filedVia ? ` · from ${c.filedVia}` : ""}
              {c.handover ? " · handover" : ""}
            </span>
            <span className="timeline-comment-actions">
              {c.id != null && (
                <CopyButton
                  text={commentUrl(t.id, c.id)}
                  label="Copy link"
                  icon={LinkIcon}
                  size="small"
                  className="timeline-comment-link"
                />
              )}
              {c.body && (
                <CopyButton
                  text={c.body}
                  label="Copy comment"
                  size="small"
                  className="timeline-comment-copy"
                />
              )}
            </span>
          </div>
          {/* Why an agent is summarising a conversation somebody can
              already read: grain asked it to, because this thread is
              longer than one prompt can carry, and this is the comment
              grain keeps quoting to later runs once the rest is cut. */}
          {c.handover && (
            <div className="timeline-meta">
              this task's standing summary for later runs -- carried into their
              prompts even when the rest of the conversation is too long to
              quote
            </div>
          )}
          {/* grain's own comments name other tasks in plain words
              ("Filed task 1514 to review it"), and those become links
              the timeline opens in place (taskRefs.js). */}
          {/* A question an agent asked leads with its own one-line
              summary (Comment.Summary), the line the inbox and the task
              list show of it, bold above the full text. */}
          {c.summary && (
            <p className="question-summary-line">
              <strong>{c.summary}</strong>
            </p>
          )}
          {c.body && (
            <ExpandableMarkdown className="timeline-comment-body">
              {c.authorKind === "automation" ? linkTaskRefs(c.body) : c.body}
            </ExpandableMarkdown>
          )}
          <AttachmentLinks taskId={t.id} attachments={c.attachments} />
        </>
      ),
    };
  });
  placeUndated(commentEvents);
  commentEvents.forEach(push);

  // Undated events with nothing to place them by go last; everything else
  // by time, then causal rank, then the order it was gathered in.
  const sortKey = (e) => (e.sortAt ? e.sortAt.getTime() : Infinity);
  events.sort((a, b) => {
    const ta = sortKey(a);
    const tb = sortKey(b);
    if (ta !== tb) return ta < tb ? -1 : 1;
    return a.rank - b.rank || a.seq - b.seq;
  });

  return events;
}

// Timeline is the task's whole history -- every state transition, every
// attempt's result, every comment -- as one time-ordered feed
// (bwsalmon/agents#453: "show the various actions ... as a timeline with
// times next to each transition"), rendered as a connected list of dots
// (reusing the existing badge-<state> dot styling) with the comment box
// beside the newest end of it.
//
// Which end that is is the reader's own choice (grain/task-203,
// TimelineOrderContext): newest first by default -- what just happened,
// directly under the heading, with the older entries trailing behind --
// or the strict oldest-first reading, for somebody following a task from
// its start. The comment box follows the newest end rather than staying
// put: under a newest-first feed it sits above the list, so replying to
// what a run just said is never a scroll past the whole history to reach
// the box.
export default function Timeline({ t, act, showError, onOpenTask }) {
  const textareaRef = useRef(null);
  const zone = useTimeZone();
  const { order } = useTimelineOrder();
  const newestFirst = order === "newest";
  const events = timelineEvents(t, zone, Date.now(), onOpenTask);
  const ordered = newestFirst ? [...events].reverse() : events;
  // openAttempt is the attempt (bwsalmon/agents#446's own Attempt shape,
  // off t.attempts) whose transcript is open, or null -- local to
  // Timeline, since nothing outside it needs to know.
  const [openAttempt, setOpenAttempt] = useState(null);
  // attachments is File objects picked for the reply not yet sent --
  // AttachmentPicker's own doc comment on why reading them is deferred.
  const [attachments, setAttachments] = useState([]);
  const sending = useRef(false);

  const send = async () => {
    if (sending.current) return;
    const body = textareaRef.current?.value || "";
    if (!body.trim() && attachments.length === 0) return;
    sending.current = true;
    try {
      const uploaded = await Promise.all(
        attachments.map((f) => fileToAttachment(f)),
      );
      // Cleared before the request rather than after it, so the next
      // message can be typed while this one is in flight; one that does
      // not land goes back in the box (as TaskChat's own reply does)
      // unless something new has been typed there meanwhile
      // (grain/task-1699).
      const files = attachments;
      if (textareaRef.current) {
        textareaRef.current.value = "";
      }
      setAttachments([]);
      const posted = await act(
        () =>
          api(`/api/tasks/${t.id}/comments`, {
            method: "POST",
            body: JSON.stringify({
              body,
              attachments: uploaded,
            }),
          }),
        t.id,
      );
      if (!posted) {
        if (textareaRef.current && !textareaRef.current.value.trim()) {
          textareaRef.current.value = body;
        }
        setAttachments((cur) => (cur.length ? cur : files));
      }
    } finally {
      sending.current = false;
    }
  };

  // The reply box, held in a variable because where it goes depends on
  // which way round the feed is read: above a newest-first list, below
  // an oldest-first one, beside the newest end either way.
  //
  // Uncontrolled on purpose: a poll landing mid-reply re-renders this
  // component with fresh props, but never touches the textarea's own DOM
  // value, so an unsent draft survives it.
  // A person's comment on a task waiting to be acknowledged sends it back
  // to the queue for another run (ui.Client.AddComment), so the box says
  // so before it is used rather than after.
  const reopens =
    t.state === "awaiting_reply" ? (
      <p className="hint comment-form-reopens">
        Sending a comment reopens this task for another run.
      </p>
    ) : null;
  const composer = (
    <div
      className="comment-form"
      onPaste={(evt) => {
        const files = getPastedImages(evt);
        if (files.length > 0) {
          evt.preventDefault();
          setAttachments((current) => [...current, ...files]);
        }
      }}
      onDrop={(evt) => {
        const dropped = getDroppedFiles(evt);
        if (dropped.length > 0) {
          evt.preventDefault();
          setAttachments((current) => [...current, ...dropped]);
        }
      }}
      onDragOver={(evt) => {
        evt.preventDefault();
      }}
    >
      <TextField
        multiline
        rows={2}
        placeholder={t.interactive ? "Message..." : "Reply..."}
        inputRef={textareaRef}
        autoFocus={t.interactive}
        fullWidth
        size="small"
      />
      <AttachmentPicker files={attachments} onChange={setAttachments} />
      <Button variant="outlined" onClick={send}>
        Comment
      </Button>
      {reopens}
    </div>
  );

  return (
    <div
      className={newestFirst ? "timeline timeline-newest-first" : "timeline"}
      onClick={(ev) => {
        // A task reference in one of grain's own comments
        // (linkTaskRefs) opens that task in place, like every other task
        // link on this pane. One click handler here rather than one per
        // link, so Markdown stays a component of strings alone.
        const id = taskRefFromHref(
          ev.target.closest?.("a")?.getAttribute("href"),
        );
        if (id && onOpenTask) {
          if (
            ev.button !== 0 ||
            ev.metaKey ||
            ev.ctrlKey ||
            ev.shiftKey ||
            ev.altKey
          ) {
            return;
          }
          ev.preventDefault();
          ev.stopPropagation();
          onOpenTask(id);
        }
      }}
    >
      {/* bwsalmon/agents#539 had this heading read "Chat" for an
          interactive task, since its timeline was the only place that
          conversation appeared. An interactive task has a chat window of
          its own now (TaskChat.jsx, grain/task-414) and this is the
          history behind it, so the heading says what the feed is again
          -- for every task alike. */}
      <h3>Timeline</h3>
      {t.usage && (
        <div
          className="timeline-meta timeline-task-usage"
          data-testid="task-usage"
          title={usageTitle(t.usage)}
        >
          {usageLine(t.usage)} across {t.usage.runs} run
          {t.usage.runs === 1 ? "" : "s"}
        </div>
      )}
      {newestFirst && composer}
      {ordered.length > 0 && (
        <ul className="timeline-list">
          {ordered.map((e) => (
            <li
              className={
                e.attempt
                  ? "timeline-item timeline-item-attempt"
                  : "timeline-item"
              }
              key={e.key}
              {...(e.commentId != null && {
                id: `comment-${e.commentId}`,
                "data-comment-id": e.commentId,
              })}
              // An attempt's whole entry takes a pointer's click, but it
              // stays a listitem -- a button in its place would take the
              // list's count away from a screen reader -- and the button
              // a keyboard opens the transcript by is its title, inside
              // it (grain/task-1582).
              {...(e.attempt && {
                title: "View this attempt's agent transcript",
                onClick: () => setOpenAttempt(e.attempt),
              })}
            >
              <div
                className={
                  // The live grain mark is 20px against every other
                  // entry's 11px dot, so its marker starts the
                  // connecting line further down (style.css).
                  e.badge === "running" && e.current && isAgentRunning(t)
                    ? "timeline-marker timeline-marker-mark"
                    : "timeline-marker"
                }
              >
                {(() => {
                  const repairing =
                    e.repairing || (e.current && isTaskRepairing(t));
                  const interactive =
                    !repairing &&
                    (e.interactive || (e.current && Boolean(t.interactive)));
                  const modifier = repairing
                    ? "badge-repair"
                    : interactive
                      ? "badge-interactive"
                      : "";
                  return (
                    <span
                      className={`badge badge-${e.badge}${
                        e.badge === "running"
                          ? e.current
                            ? isAgentRunning(t)
                              ? " badge-mark"
                              : ""
                            : " badge-static"
                          : ""
                      }${modifier ? ` ${modifier}` : ""}`}
                    >
                      <StateDot
                        state={e.badge}
                        live={e.current}
                        repairing={repairing}
                        interactive={interactive}
                        agentRunning={e.current ? isAgentRunning(t) : false}
                        title={STATE_LABELS[e.badge] || e.badge}
                      />
                    </span>
                  );
                })()}
              </div>
              <div className="timeline-body">
                {/* An inferred moment (model.Transition.Inferred) is
                    the earliest the entry can have happened, not when it
                    did, and reads as approximate; an entry with no usable
                    time says so rather than showing none. */}
                {e.at ? (
                  <div
                    className="timeline-when"
                    {...(e.inferred && {
                      title:
                        "Inferred: the record holds no time for this, so it is shown at the earliest it can have happened",
                    })}
                  >
                    {e.inferred && "~ "}
                    {formatDateTime(e.at, zone)}
                  </div>
                ) : (
                  <div className="timeline-when timeline-when-unknown">
                    time unknown
                  </div>
                )}
                {e.render()}
              </div>
            </li>
          ))}
        </ul>
      )}
      {openAttempt && (
        <AttemptTranscriptOverlay
          taskId={t.id}
          attempt={openAttempt}
          onClose={() => setOpenAttempt(null)}
          showError={showError}
        />
      )}
      {!newestFirst && composer}
    </div>
  );
}
