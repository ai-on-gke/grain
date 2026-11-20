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

import type { RouteKey } from "../api/generated-routes";
import type { MergeGate, Task, TaskDetail } from "../api/models";
import { APPROVAL_GATE, gateSummary, submitBlock } from "./gates";

// One place that says what a person can do with a task right now
// (design/design/ui-design-book.design.md, "The action area"). Every
// surface that offers task actions -- the task page, the Inbox
// inspector, the Agents and Sessions panes -- renders availableActions'
// answer through g-task-action-area, so the same task offers the same
// set everywhere, and nothing else on those surfaces offers them.
//
// Only what can work now is listed. The one exception is a Submit that
// waits on CI that has not reported: it is shown off, with its reason,
// because it turns on without anyone acting.

export type AnyTask = Task & Partial<TaskDetail>;

export interface ActionViewer {
  can(route: RouteKey, repo?: string): boolean;
  // An unlimited role (grain/operator), which accepting review findings and
  // force-merging need.
  fullAccess: boolean;
}

export type TaskActionId =
  | "submit"
  | "send-back"
  | "accept-findings"
  | "force-merge"
  | "retry-merge"
  | "approve"
  | "reply"
  | "retry"
  | "resume-pause"
  | "undefer"
  | "run-now"
  | "pause"
  | "pause-immediate"
  | "stop"
  | "defer"
  | "defer-until"
  | "withdraw"
  | "follow-up"
  | "compact-open"
  | "rollover"
  | "recreate-sandbox"
  | "reopen"
  | "close";

export interface TaskAction {
  id: TaskActionId;
  label: string;
  route: RouteKey;
  variant: "primary" | "tonal" | "text";
  // Why the action is shown off. Only ever set on a Submit waiting on CI.
  disabled?: string;
  tip?: string;
  // The merge gate this action answers: it is drawn on that gate's row in
  // the gates list as well as in the action row.
  gate?: string;
  danger?: boolean;
  // Less common actions sit after a divider, in quieter buttons.
  secondary?: boolean;
}

export function isStoppedTurn(t: AnyTask): boolean {
  return (
    t.state === "awaiting_reply" &&
    (!!t.turnStopped || t.reason?.reason === "awaiting_reply/stopped")
  );
}

function parkedOn(t: AnyTask): "secret" | "access" | "config" | "" {
  if (t.state !== "awaiting_reply") return "";
  if (t.pendingSecret) return "secret";
  if (t.pendingAccess || t.awaitingAccess) return "access";
  if (t.pendingConfigChange) return "config";
  return "";
}

// control is the button a stuck task's sub-case names (the liveness
// reason's control, or the merge queue's): "Run now", "Cancel",
// "Undefer" or "Retry merge".
function control(t: AnyTask): string {
  return t.reason?.control || t.mergeQueueReason?.control || "";
}

// blockingGates are the gates a person has to settle before Submit means
// anything: failing, with findings, or still needed (approval aside --
// Submit is the approval).
export function blockingGates(t: AnyTask): MergeGate[] {
  return (t.mergeGates ?? []).filter(
    (g) =>
      g.id !== APPROVAL_GATE &&
      (g.state === "failing" || g.state === "findings" || g.state === "needed"),
  );
}

function findingsCount(t: AnyTask, gate: string): number {
  return ((t.reviewFindings ?? []) as { gate?: string }[]).filter(
    (f) => f?.gate === gate,
  ).length;
}

function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

// findingsPhrase is "left 1 finding" / "left findings" for a gate.
function findingsPhrase(t: AnyTask, g: MergeGate): string {
  const n = findingsCount(t, g.id);
  return n ? `left ${plural(n, "finding")}` : "left findings";
}

function label(g: MergeGate): string {
  return g.label || g.id;
}

export interface AutoMergeChange {
  at?: string;
  reason: string;
  by?: string;
}

// autoMergeTurnedOff is grain's (or anybody's) latest turning off of
// auto-merge, while it is still off: the auto_merge_off step of the
// task's transitions (model.TransitionAutoMergeOff) with no later
// auto_merge_on. null while auto-merge is on or was never turned off.
export function autoMergeTurnedOff(t: AnyTask): AutoMergeChange | null {
  if (t.autoMerge) return null;
  const steps = (
    (
      t as {
        transitions?: {
          state: string;
          at?: string;
          reason?: string;
          detail?: string;
          actorLabel?: string;
          byLabel?: string;
        }[];
      }
    ).transitions ?? []
  ).filter((s) => s.state === "auto_merge_off" || s.state === "auto_merge_on");
  const last = steps[steps.length - 1];
  if (!last || last.state !== "auto_merge_off") return null;
  return {
    at: last.at,
    reason: (last.reason || last.detail || "").trim(),
    by: last.actorLabel || last.byLabel,
  };
}

// autoMergeSentence is what the action area and the conversation say
// about auto-merge going off: why, and what to do about it.
export function autoMergeSentence(t: AnyTask): string {
  const off = autoMergeTurnedOff(t);
  if (!off) return "";
  const because = off.reason
    ? off.reason.replace(/[.\s]+$/, "")
    : "a merge gate needs a person";
  const pending = blockingGates(t).map(label);
  const running = (t.mergeGates ?? [])
    .filter((g) => g.id !== APPROVAL_GATE && g.state === "running")
    .map(label);
  const after = pending.length
    ? `${pending.join(" and ")} ${pending.length === 1 ? "is" : "are"} settled`
    : running.length
      ? `${running.join(" and ")} ${running.length === 1 ? "passes" : "pass"}, or now to merge once ${running.length === 1 ? "it does" : "they do"}`
      : "checking the change";
  return `Auto-merge was turned off because ${because}. Submit again after ${after}.`;
}

// canAcceptFindings reports whether viewer may call POST accept-findings on t:
// POST /api/tasks/{id}/accept-findings carries tasks:write scope, and
// Client.AcceptReviewFindings refuses any role that is not unlimited
// (pkg/ui/accept_findings.go). When viewer is omitted, full access is assumed.
function canAcceptFindings(t: AnyTask, viewer?: ActionViewer): boolean {
  if (!viewer) return true;
  return (
    viewer.fullAccess &&
    viewer.can("POST /api/tasks/{id}/accept-findings", t.repo || undefined)
  );
}

// whatsNeeded is the one sentence at the top of the action area: who
// has to act, and what the action is.
export function whatsNeeded(t: AnyTask, viewer?: ActionViewer): string {
  const state = t.state;
  if (state === "closed") {
    return t.prMergedAt || t.merged
      ? "Nothing is needed: this task is closed and its change merged."
      : "Nothing is needed: this task is closed. Reopen it to work on it again.";
  }
  if (t.paused) {
    return "Paused by a person. Resume it to put it back to work.";
  }
  switch (state) {
    case "proposed":
      return "A person must approve this proposal before it runs. Approve it, defer it or close it.";
    case "deferred":
      return t.deferredUntil
        ? "Deferred: it picks itself back up on the day set. Bring it back to queue it sooner."
        : "Deferred. Bring it back to queue it again.";
    case "queued":
      return "Queued: grain dispatches it on a coming cycle. Nothing is needed from you.";
    case "running":
      return "An agent is working on it now. Nothing is needed from you.";
    case "failed":
      return "Its attempts kept failing, so grain stopped retrying. Retry it, or close it.";
  }
  if (state === "awaiting_reply") {
    switch (parkedOn(t)) {
      case "secret":
        return `The run needs the secret ${t.pendingSecret?.name ?? ""}. Set it below to resume.`.replace(
          "  ",
          " ",
        );
      case "access":
        return "The run asked for more access. Grant or deny it below.";
      case "config":
        return "The run proposed a configuration change. Approve or reject it below.";
    }
    if (isStoppedTurn(t)) {
      return "A person stopped this run. Resume it, or close it.";
    }
    if (t.questionSummary) {
      return `The agent asked: ${t.questionSummary.replace(/\s+$/, "")} Answer it to continue.`;
    }
    return "The agent is waiting on you. Reply to continue, or close the task.";
  }
  if (state === "awaiting_submit") return submitSentence(t, viewer);
  // completed (and anything else grain adds later).
  if (t.mergeQueueBlockedAt || control(t) === "Retry merge") {
    return "The merge queue gave up on this pull request. Sort it out, then Retry merge.";
  }
  if (t.pullRequest && !t.prMergedAt) {
    const s = gateSummary(t);
    if (s.failing.length) {
      return `${s.failing.join(", ")} ${s.failing.length === 1 ? "is" : "are"} failing; grain's merge queue is repairing it.`;
    }
    return t.autoMerge
      ? "Nothing is needed from you: it will merge when CI passes."
      : "Nothing is needed from you: the merge queue is landing it.";
  }
  return "Nothing is needed from you.";
}

function submitSentence(t: AnyTask, viewer?: ActionViewer): string {
  const gates = (t.mergeGates ?? []).filter((g) => g.id !== APPROVAL_GATE);
  const parts: string[] = [];
  const findings = gates.filter((g) => g.state === "findings");
  const failing = gates.filter((g) => g.state === "failing");
  const needed = gates.filter((g) => g.state === "needed");
  const running = gates.filter((g) => g.state === "running");
  const mayAccept = canAcceptFindings(t, viewer);
  for (const g of findings) {
    const n = findingsCount(t, g.id);
    const it = n > 1 ? "them" : "it";
    parts.push(
      `${label(g)} ${findingsPhrase(t, g)}. Fix ${it} (send ${it} back to the agent)${mayAccept && g.acceptable ? ` or accept ${it}` : ""} to continue.`,
    );
  }
  if (failing.length) {
    parts.push(
      `${failing.map(label).join(" and ")} ${failing.length === 1 ? "is" : "are"} failing. Send it back to the agent to fix.`,
    );
  }
  if (needed.length) {
    parts.push(
      `${needed.map(label).join(" and ")} ${needed.length === 1 ? "is" : "are"} still needed.`,
    );
  }
  const hold = (t.humanReviewHold ?? []).filter(
    (h) => h.source !== "security-findings",
  );
  if (hold.length) {
    parts.push(
      `A person must clear the human-review hold${hold[0].reason ? ` (${hold[0].reason.replace(/[.\s]+$/, "")})` : ""}.`,
    );
  }
  const blocked = findings.length || failing.length || needed.length;
  if (running.length) {
    const names = running.map(label).join(" and ");
    parts.push(
      blocked
        ? `${names} ${running.length === 1 ? "is" : "are"} still running.`
        : `${names} ${running.length === 1 ? "is" : "are"} still running: Submit now and it merges when ${running.length === 1 ? "it passes" : "they pass"}.`,
    );
  }
  if (!parts.length) {
    const off = submitBlock(t);
    if (off) return `${off} Submit turns on once it reports.`;
    if (t.clSubmit) return "Ready: Submit lands this change now.";
    return "Ready for you: Submit to approve the pull request and merge it.";
  }
  return parts.join(" ");
}

// nextStep is the short line a list row shows for a task waiting on a
// person: the concrete next step, not just the state's name. "" when
// nothing is needed from a person.
export function nextStep(
  t: AnyTask,
  listOnly = false,
  viewer?: ActionViewer,
): string {
  if (t.paused && t.state !== "closed") return "Paused: Resume to continue";
  switch (t.state) {
    case "proposed":
      return "Approve to queue it";
    case "failed":
      return "Retry, or close it";
    case "awaiting_reply": {
      const p = parkedOn(t);
      if (p === "secret") return "Set the requested secret";
      if (p === "access") return "Grant or deny the access request";
      if (p === "config") return "Approve or reject the config change";
      if (isStoppedTurn(t)) return "Stopped: Resume or close";
      return t.questionSummary
        ? "Answer the agent's question"
        : "Reply to continue";
    }
    case "awaiting_submit":
      break;
    default:
      return t.mergeQueueBlockedAt
        ? "Sort out the merge, then Retry merge"
        : "";
  }
  const gates = (t.mergeGates ?? []).filter((g) => g.id !== APPROVAL_GATE);
  const findings = gates.filter((g) => g.state === "findings");
  if (findings.length) {
    const n = findings.reduce(
      (sum, g) => sum + Math.max(1, findingsCount(t, g.id)),
      0,
    );
    const what = findings.every((g) => g.id === "security-review")
      ? "security finding"
      : "review finding";
    const canAccept =
      canAcceptFindings(t, viewer) && findings.some((g) => g.acceptable);
    return `${canAccept ? "Fix or accept" : "Fix"} ${plural(n, what)}`;
  }
  const failing = gates.filter((g) => g.state === "failing");
  if (failing.length) return `Fix ${failing.map(label).join(", ")}`;
  const needed = gates.filter((g) => g.state === "needed");
  if (needed.length) return `Waiting for ${needed.map(label).join(", ")}`;
  const running = gates.filter(
    (g) =>
      g.state === "running" &&
      !(
        listOnly &&
        g.id === "ci" &&
        (g.detail ?? "").toLowerCase() === "waiting for checks"
      ),
  );
  if (running.length) {
    return `Waiting for ${running.map(label).join(", ")}, then Submit`;
  }
  if (submitBlock(t, listOnly)) return "Waiting for CI, then Submit";
  return "Ready to Submit";
}

// availableActions is every action a person may take on t right now,
// given what viewer may do: one answer for every surface.
export function availableActions(
  t: AnyTask,
  viewer: ActionViewer,
): TaskAction[] {
  const repo = t.repo || undefined;
  const can = (r: RouteKey) => viewer.can(r, repo);
  const out: TaskAction[] = [];
  const add = (a: TaskAction, route = a.route) => {
    if (can(route)) out.push(a);
  };
  const state = t.state;
  const closed = state === "closed";
  const paused = !!t.paused && !closed;
  const stopped = isStoppedTurn(t);

  if (closed) {
    add({
      id: "reopen",
      label: "Reopen",
      route: "POST /api/tasks/{id}/reopen",
      variant: "primary",
    });
    add({
      id: "follow-up",
      label: "Follow up",
      route: "POST /api/tasks",
      variant: "text",
      tip: "File a task that waits for this one, on the same repository",
      secondary: true,
    });
    return out;
  }

  // --- the main decision ---------------------------------------------
  if (paused) {
    add({
      id: "resume-pause",
      label: "Resume",
      route: "POST /api/tasks/{id}/resume",
      variant: "primary",
    });
  }
  if (state === "proposed" || (state === "deferred" && !t.approved)) {
    add({
      id: "approve",
      label: "Approve",
      route: "POST /api/tasks/{id}/approve",
      variant: "primary",
    });
  }
  if (state === "deferred") {
    add({
      id: "undefer",
      // A stuck deferral's sub-case names its control "Undefer".
      label: t.stall && control(t) === "Undefer" ? "Undefer" : "Bring back",
      route: "POST /api/tasks/{id}/undefer",
      variant: "tonal",
    });
  }
  if (state === "awaiting_reply" && !parkedOn(t) && !stopped) {
    add({
      id: "reply",
      label: t.questionSummary ? "Answer" : "Reply",
      route: "POST /api/tasks/{id}/comments",
      variant: "primary",
    });
  }
  if ((state === "failed" || stopped) && !paused) {
    add({
      id: "retry",
      label: state === "failed" ? "Retry" : "Resume",
      route: "POST /api/tasks/{id}/retry",
      variant: "primary",
    });
  }
  if (state === "awaiting_submit") {
    out.push(...submitActions(t, viewer));
  }
  // The merge queue gave up, or a merge failed (tasks/task-view.ts
  // needsRetryMerge): put it back on the queue for another round.
  if (
    !paused &&
    (t.mergeQueueBlockedAt ||
      control(t) === "Retry merge" ||
      (state === "completed" && t.mergeQueueState === "merge_failed"))
  ) {
    add({
      id: "retry-merge",
      tip: "Put this pull request back on the merge queue for another round",
      label: "Retry merge",
      route: "POST /api/tasks/{id}/retry-merge",
      variant: state === "awaiting_submit" ? "tonal" : "primary",
    });
  }
  if (state === "running") {
    add({
      id: "stop",
      label: "Stop",
      route: "POST /api/tasks/{id}/stop",
      variant: "tonal",
      tip: "Interrupt this turn; the task waits for your reply",
    });
  }

  // --- the rest ---------------------------------------------------------
  // On a task waiting on Submit, Run now sends it back to its agent on
  // its branch; where Send back already does that it is not offered twice.
  // Run now approves an unapproved task as it starts it, so it needs the
  // right to approve there too.
  const needsApproval =
    state === "proposed" || (state === "deferred" && !t.approved);
  if (
    state !== "running" &&
    !(state === "awaiting_submit" && out.some((a) => a.id === "send-back")) &&
    !(state === "awaiting_reply" && parkedOn(t)) &&
    (!needsApproval || can("POST /api/tasks/{id}/approve"))
  ) {
    add({
      id: "run-now",
      label: "Run now",
      route: "POST /api/tasks/{id}/run-now",
      variant: "text",
      tip: t.pullRequest
        ? "Put an agent back on this branch ahead of the queue; nothing merges it while the run is in flight"
        : "Dispatch ahead of the queue on the next cycle",
      secondary: true,
    });
  }
  if (state === "proposed" || state === "queued") {
    add({
      id: "defer",
      label: "Defer",
      route: "POST /api/tasks/{id}/defer",
      variant: "text",
      tip: "Put it aside until you bring it back",
      secondary: true,
    });
    add({
      id: "defer-until",
      label: "Defer until…",
      route: "POST /api/tasks/{id}/defer",
      variant: "text",
      tip: "Put it aside until a day you pick",
      secondary: true,
    });
  }
  if (state === "queued" || (state === "deferred" && t.approved)) {
    add({
      id: "withdraw",
      label: "Withdraw approval",
      route: "POST /api/tasks/{id}/withdraw-approval",
      variant: "text",
      tip: "Send it back to Needs approval; nothing runs until it is approved again",
      secondary: true,
    });
  }
  if (!paused) {
    add({
      id: "pause",
      label: "Pause",
      route: "POST /api/tasks/{id}/pause",
      variant: "text",
      secondary: true,
    });
  }
  if (state === "running" && !paused) {
    add({
      id: "pause-immediate",
      label: "Pause immediately",
      route: "POST /api/tasks/{id}/pause",
      variant: "text",
      secondary: true,
    });
    add({
      id: "recreate-sandbox",
      label: "Recreate sandbox",
      route: "POST /api/tasks/{id}/sandbox/recreate",
      variant: "text",
      secondary: true,
    });
  }
  add({
    id: "compact-open",
    label: "Compact context…",
    route: "POST /api/tasks/{id}/compact",
    variant: "text",
    secondary: true,
  });
  if (
    (t.admission === "durable" || !!t.goal) &&
    !t.consoleSession &&
    state !== "running"
  ) {
    add({
      id: "rollover",
      label: "Rollover session",
      route: "POST /api/tasks/{id}/rollover",
      variant: "text",
      secondary: true,
    });
  }
  add({
    id: "follow-up",
    label: "Follow up",
    route: "POST /api/tasks",
    variant: "text",
    tip: "File a task that waits for this one, on the same repository",
    secondary: true,
  });
  // A stuck run's sub-case can name Cancel as its way out: then it is
  // the main action, and named so it is not mistaken for a dialog's
  // own Cancel.
  const cancel = control(t) === "Cancel";
  add({
    id: "close",
    label: cancel ? "Cancel task" : "Close",
    route: "POST /api/tasks/{id}/close",
    variant: cancel ? "tonal" : "text",
    secondary: !cancel,
  });
  return out;
}

function submitActions(t: AnyTask, viewer: ActionViewer): TaskAction[] {
  const repo = t.repo || undefined;
  const out: TaskAction[] = [];
  const gates = t.mergeGates ?? [];
  const blocking = blockingGates(t);
  const fixable = blocking.filter(
    (g) => g.state === "findings" || g.state === "failing",
  );
  // Send back: the findings (or the red check) go into the thread and
  // the task is run again on its own branch (POST run-now, which clears
  // the completion an awaiting_submit task rests on).
  if (
    fixable.length &&
    viewer.can("POST /api/tasks/{id}/run-now", repo) &&
    viewer.can("POST /api/tasks/{id}/comments", repo)
  ) {
    const findings = fixable.some((g) => g.state === "findings");
    out.push({
      id: "send-back",
      label: findings ? "Send findings back" : "Send back to fix",
      route: "POST /api/tasks/{id}/run-now",
      variant: "primary",
      tip: "Re-run the task on its branch with this in its prompt",
      gate: fixable.length === 1 ? fixable[0].id : undefined,
    });
  }
  // Accept: a review's findings on one gate, taken as they are, with a
  // reason (POST accept-findings). Offered to an unlimited role where the
  // server says the findings are a review's to accept (MergeGate.acceptable);
  // CI, the other gates and approvals still apply afterwards.
  if (
    viewer.fullAccess &&
    viewer.can("POST /api/tasks/{id}/accept-findings", repo)
  ) {
    const acceptable = gates.filter(
      (g) => g.state === "findings" && !!g.acceptable,
    );
    for (const g of acceptable) {
      out.push({
        id: "accept-findings",
        label:
          acceptable.length > 1
            ? `Accept ${label(g)} findings…`
            : "Accept findings…",
        route: "POST /api/tasks/{id}/accept-findings",
        variant: "tonal",
        tip: "Merge over this review's findings as they are, with a reason. CI, the other gates and approvals still apply.",
        gate: g.id,
      });
    }
  }
  if (!t.humanApprovedBy && viewer.can("POST /api/tasks/{id}/submit", repo)) {
    const off = submitBlock(t);
    const s = gateSummary(t);
    if (!blocking.length && off) {
      // Waiting on CI that has not reported: off, saying why.
      out.push({
        id: "submit",
        label: "Submit",
        route: "POST /api/tasks/{id}/submit",
        variant: "primary",
        disabled: off,
      });
    } else if (!blocking.length) {
      const running = s.running.length > 0;
      out.push({
        id: "submit",
        label: running
          ? s.running.length === 1 && s.running[0] === "CI"
            ? "Submit (merges when CI passes)"
            : "Submit (merges when its gates pass)"
          : "Submit",
        route: "POST /api/tasks/{id}/submit",
        variant: "primary",
        tip: "Approve the pull request; review gates still apply",
      });
    }
  }
  if (
    viewer.fullAccess &&
    viewer.can("POST /api/tasks/{id}/force-merge", repo) &&
    (t.pullRequest || gates.length)
  ) {
    out.push({
      id: "force-merge",
      label: "Force merge…",
      route: "POST /api/tasks/{id}/force-merge",
      variant: "text",
      danger: true,
      secondary: true,
    });
  }
  return out;
}

// gateActions are the actions drawn on one gate's row of the gates list.
export function gateActions(
  actions: TaskAction[],
  gate: MergeGate,
): TaskAction[] {
  return actions.filter((a) => a.gate === gate.id);
}

// sendBackComment is the note "Send findings back" leaves on the thread
// before the run it asks for: the agent's prompt carries the thread, so
// this is how the findings reach it.
export function sendBackComment(t: AnyTask): string {
  const lines: string[] = [];
  const records = (t.reviewFindings ?? []) as {
    gate?: string;
    summary?: string;
    reviewTask?: string;
  }[];
  for (const g of blockingGates(t)) {
    if (g.state !== "findings" && g.state !== "failing") continue;
    const mine = records.filter((r) => r?.gate === g.id && r.summary);
    if (mine.length) {
      for (const r of mine) {
        lines.push(
          `- ${label(g)}: ${r.summary}${r.reviewTask ? ` (review task #${r.reviewTask})` : ""}`,
        );
      }
    } else {
      lines.push(
        `- ${label(g)} ${g.state === "failing" ? "is failing" : "left findings"}${g.detail ? `: ${g.detail}` : ""}${g.reviewTask ? ` (review task #${g.reviewTask})` : ""}`,
      );
    }
  }
  return [
    "Sent back to fix before it can be submitted:",
    "",
    ...lines,
    "",
    "Fix these on this task's branch and push, so the reviews and checks run again.",
  ].join("\n");
}
