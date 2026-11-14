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
import type { RouteParams } from "../api/grain-api.service";
import type { LocalScriptApproval, Notification, Task } from "../api/models";
import type { TaskDetail } from "../api/models";
import {
  gateSummary,
  isMerged,
  submitBlock,
  withDetailGates,
} from "../shared/gates";

// The classification here is a port of the React Inbox's (ui/src/state.js
// waitsOnUser and ui/src/components/InboxPage.jsx GROUPS, plus
// ui/src/notifications.js pendingReleaseOf), kept as pure functions so it
// is unit-tested on its own and shared by every view of the queue.

export type Tone =
  "reply" | "proposal" | "blast" | "error" | "healthy" | "neutral";
export type FilterId =
  | "all"
  | "asked"
  | "proposals"
  | "merges"
  | "approvals"
  | "alerts"
  | "paused"
  | "unread";

export interface InboxAction {
  id: string;
  label: string;
  route: RouteKey;
  params: RouteParams;
  body?: unknown;
  variant: "primary" | "tonal" | "text";
  // confirm is asked, in a dialog, before the call is made.
  confirm?: string;
  // destructive marks the dialog's accept button as a warning.
  destructive?: boolean;
  // bulk is the batch bar's label when several selected rows offer it.
  bulk?: boolean;
  repo?: string;
  // blocked is why the action must not be taken now (Submit on a red or
  // unreported CI): the button is off with this as its tooltip, and
  // batches skip it.
  blocked?: string;
}

export interface PendingRelease {
  repo: string;
  version: string;
  soak: string | null;
  shortSoak: string | null;
  breaking: string | null;
  dispatchFailure: {
    runId: string;
    message: string;
    remedy: string | null;
    runUrl: string | null;
    needsApprovalKey: boolean;
  } | null;
}

export type EntryKind =
  | "base-repair"
  | "release"
  | "access"
  | "secret"
  | "reply"
  | "host-script"
  | "review"
  | "submit"
  | "merge-blocked"
  | "github-approval"
  | "merge-stalled"
  | "failed"
  | "stalled"
  | "proposal"
  | "stopped"
  | "notification";

export interface InboxEntry {
  key: string;
  kind: EntryKind;
  groupId: string;
  ref: string;
  title: string;
  repo: string;
  // The task's owner, when the server names one.
  owner: string;
  summary: string;
  pillLabel: string;
  pillTone: Tone;
  at: string | null;
  // rationale is the "why this needs a person" line the inspector shows.
  rationale: string;
  taskId: string | null;
  taskLink: string | null;
  externalLink: string | null;
  task?: Task;
  notification?: Notification;
  release?: PendingRelease;
  localScript?: LocalScriptApproval;
  acked?: boolean;
  // gone marks a row that has left the queue but is still shown, greyed,
  // until the person applies the buffered updates (or, for the selected
  // row, moves on). It offers no actions.
  gone?: boolean;
  actions: InboxAction[];
}

export interface InboxGroup {
  id: string;
  title: string;
  hint: string;
  filter: Exclude<FilterId, "all" | "unread">;
  entries: InboxEntry[];
}

export interface GroupDef {
  id: string;
  title: string;
  hint: string;
  filter: Exclude<FilterId, "all" | "unread">;
}

// GROUP_ORDER is the order groups are listed in: what blocks the most,
// soonest, first -- the React Inbox's order with releases, access and
// host-script approvals placed among the "Asked" groups they belong to.
export const GROUP_ORDER: GroupDef[] = [
  {
    id: "base-repair",
    title: "Base-branch repair waiting",
    hint: "A base branch is red and its repair pull request is waiting for your approval.",
    filter: "merges",
  },
  {
    id: "release",
    title: "Asked: approve a release",
    hint: "The release train staged a release that waits for a person to promote or reject it.",
    filter: "approvals",
  },
  {
    id: "access",
    title: "Asked: grant access",
    hint: "A run asked for more access than it holds. Grant what it needs or deny it with a reason.",
    filter: "asked",
  },
  {
    id: "secret",
    title: "Asked: set a credential",
    hint: "A run asked for a credential this deployment does not have. Set it on the task.",
    filter: "asked",
  },
  {
    id: "reply",
    title: "Asked: reply",
    hint: "A run stopped to ask something, or answered on the task itself. Reply to run it again, or acknowledge to close it.",
    filter: "asked",
  },
  {
    id: "host-script",
    title: "Asked: approve a host script",
    hint: "A run's host script waits for a person to approve the exact call it is about to make.",
    filter: "approvals",
  },
  {
    id: "review",
    title: "Review needs your decision",
    hint: "A review fixed what it could and left questions only you can settle.",
    filter: "merges",
  },
  {
    id: "submit",
    title: "Asked: submit for merge",
    hint: "Submit records your approval; the pull request merges once its required gates pass.",
    filter: "merges",
  },
  {
    id: "merge-blocked",
    title: "Unblock a merge",
    hint: "The merge queue gave up on landing these automatically; its own comment on each says why.",
    filter: "merges",
  },
  {
    id: "github-approval",
    title: "Approve on GitHub",
    hint: "GitHub will not merge these until somebody approves them there.",
    filter: "merges",
  },
  {
    id: "merge-stalled",
    title: "Look at a stalled merge",
    hint: "These have stood in one merge-queue state far longer than it should take.",
    filter: "merges",
  },
  {
    id: "failed",
    title: "Retry or close",
    hint: "These failed. Retry puts one back in the queue; Close gives up on it.",
    filter: "alerts",
  },
  {
    id: "stalled",
    title: "Look at a stuck task",
    hint: "Grain reads these as stuck rather than busy.",
    filter: "alerts",
  },
  {
    id: "proposal",
    title: "Approve a proposal",
    hint: "Tasks somebody or something suggested. Nothing runs until one is approved.",
    filter: "proposals",
  },
  {
    id: "notification",
    title: "Alerts",
    hint: "Open alerts raised for this deployment. Acknowledge one you have seen, or have an agent investigate it.",
    filter: "alerts",
  },
  {
    id: "paused",
    title: "Paused",
    hint: "Turns a person stopped. Nothing was asked: resume one to let it carry on, or close it.",
    filter: "paused",
  },
];

// Groups that are not decisions waiting on anyone; the headline and the
// rail badge leave them out.
export const QUIET_GROUPS: ReadonlySet<string> = new Set(["paused"]);

// --- task predicates (ui/src/state.js) -------------------------------

export function isTaskMerged(t: Task): boolean {
  return isMerged(t);
}

// isStoppedTurn is a task a person stopped mid-run: Stop writes a
// pending comment, so the server reads it as awaiting_reply, but nothing
// was asked (reason awaiting_reply/stopped, or turnStopped).
export function isStoppedTurn(t: Task): boolean {
  return (
    t.state === "awaiting_reply" &&
    (t.reason?.reason === "awaiting_reply/stopped" || !!t.turnStopped)
  );
}

export function waitingBaseRepair(t: Task): boolean {
  if (t.state === "closed" || t.state === "running") return false;
  if (t.mergeQueueState === "merging" || isTaskMerged(t)) return false;
  return !!(t.redBaseBranch && t.pullRequest);
}

export function awaitsHumanSubmit(t: Task): boolean {
  if (
    t.humanApprovedBy &&
    !t.reviewQuestions &&
    !(t.reviewFindings && t.reviewFindings.length > 0) &&
    !t.humanReviewHold?.some((r) => r.category === "security-findings")
  ) {
    return false;
  }
  // The classic UI (ui/src/state.js) also counts a running task with an
  // open pull request and no auto-merge, so a run parked in
  // wait_for_checks is visible. Here a running task is never offered
  // Submit: the agent may still push to the branch, and on 2026-11-11 a
  // Submit pressed mid-run approved a change that kept changing. Such a
  // task is on Agents and in the Running tab until its run ends.
  return t.state === "awaiting_submit";
}

export function awaitsGitHubApproval(t: Task): boolean {
  return (
    t.state !== "closed" && t.mergeQueueState === "waiting_github_approval"
  );
}

export function mergeQueueStalled(t: Task): boolean {
  return t.state !== "closed" && !!t.mergeQueueState && !!t.mergeQueueStalledAt;
}

// mergeBlocked is a pull request the merge queue has stopped driving, or
// one a person's review holds. awaiting_submit counts as well as
// completed: a change that is not auto-merge reads awaiting_submit even
// after the queue spent its repairs on it and gave up, and once somebody
// has approved it no Submit item lists it either -- task 4525 sat out of
// the Inbox for seventeen hours that way.
export function mergeBlocked(t: Task): boolean {
  return (
    (t.state === "completed" || t.state === "awaiting_submit") &&
    !!t.pullRequest &&
    (!!t.mergeQueueBlockedAt || !!t.humanReviewHold?.length)
  );
}

// approvedButGated is an approved awaiting_submit change that a merge
// gate other than approval still blocks: approval has been given, so
// awaitsHumanSubmit no longer lists it, but it is not going to merge
// until somebody deals with the red or stuck gate.
export function approvedButGated(t: Task, listOnly = false): boolean {
  if (t.state !== "awaiting_submit" || !t.humanApprovedBy) return false;
  const s = gateSummary(t, listOnly);
  return s.failing.length > 0 || s.needed.length > 0;
}

const INBOX_STATES = [
  "awaiting_reply",
  "awaiting_submit",
  "failed",
  "proposed",
];

export function waitsOnUser(t: Task): boolean {
  if (t.consoleSession) return false;
  return (
    waitingBaseRepair(t) ||
    awaitsHumanSubmit(t) ||
    INBOX_STATES.includes(t.state) ||
    awaitsGitHubApproval(t) ||
    mergeQueueStalled(t) ||
    !!t.stall ||
    mergeBlocked(t)
  );
}

function pullRequestUrl(t: Task): string | null {
  const pr = t.pullRequest;
  if (!pr) return null;
  if (typeof pr === "string") return /^https?:\/\//.test(pr) ? pr : null;
  return typeof pr.url === "string" && /^https?:\/\//.test(pr.url)
    ? pr.url
    : null;
}

// --- notifications (ui/src/notifications.js) --------------------------

const PENDING_RELEASE_FAMILY = "release.pending";

function safeUrl(url: string | null): string | null {
  if (!url) return null;
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : null;
  } catch {
    return null;
  }
}

export function pendingReleaseOf(n: Notification): PendingRelease | null {
  const family = n.family || (n.subject || "").split(":")[0];
  if (family !== PENDING_RELEASE_FAMILY) return null;
  if (!(n.actions || []).includes("approve-release")) return null;
  const version = (n.subject || "").slice(PENDING_RELEASE_FAMILY.length + 1);
  if (!/^v\d+\.\d+\.\d+$/.test(version)) return null;
  const lines = (n.body || "").split("\n").map((l) => l.trim());
  const soak =
    lines
      .find((l) => l.startsWith("Soak: "))
      ?.match(/soaked \d+h\d{2}m of \d+h(?: \(FORCE=true\))?/)?.[0] ?? null;
  const shortSoak = lines.find((l) => l.startsWith("SHORT SOAK:")) ?? null;
  const breaking =
    lines.find(
      (l) =>
        l.startsWith("BREAKING CHANGES:") ||
        (/carries breaking changes\b/.test(l) &&
          /is not the major release RELEASE_MAJOR asks for/.test(l)),
    ) ??
    (n.breaking ? String(n.breaking) : null) ??
    (lines.some((l) => /^#{1,3}\s+Breaking changes\b/i.test(l))
      ? "Release notes list breaking changes; approving needs allow_breaking."
      : null);

  let dispatchFailure: PendingRelease["dispatchFailure"] = null;
  let i = 0;
  while (i < lines.length && lines[i] === "") i++;
  const header = lines[i]?.startsWith("DISPATCH FAILED (") ? lines[i] : null;
  if (header && !String(n.raisedBy || "").startsWith("agent:")) {
    let remedy: string | null = null;
    let runUrl: string | null = null;
    for (i++; i < lines.length; i++) {
      const l = lines[i];
      if (l.startsWith("Remedy: ") && remedy === null) remedy = l.slice(8);
      else if (l.startsWith("Run URL: ") && runUrl === null)
        runUrl = l.slice(9).trim();
      else break;
    }
    const m = header.match(/^DISPATCH FAILED \(([^)]+)\):\s*(.+)$/);
    if (m) {
      dispatchFailure = {
        runId: m[1],
        message: m[2],
        remedy,
        runUrl: safeUrl(runUrl),
        needsApprovalKey:
          /GRAIN_RELEASE_APPROVAL_KEY|\/api\/release-approval-key/.test(
            [header, remedy || ""].join("\n"),
          ),
      };
    }
  }
  return {
    repo: n.repo || "",
    version,
    soak,
    shortSoak,
    breaking,
    dispatchFailure,
  };
}

function plain(line: string): string {
  return line
    .replace(/\[([^\]\n]+)\]\([^)\s]+\)/g, "$1")
    .replace(/`([^`\n]+)`/g, "$1");
}

export function notificationSentence(n: Notification): string {
  const body = plain((n.body || "").trim().split("\n")[0]);
  const again = (n.raiseCount ?? 0) > 1 ? ` Raised ${n.raiseCount} times.` : "";
  return `${body || n.subject}${again}`;
}

export function pendingReleaseSentence(n: Notification): string {
  const line =
    (n.body || "")
      .split("\n")
      .map((l) => l.trim())
      .find(
        (l) =>
          l !== "" &&
          !l.startsWith("DISPATCH FAILED (") &&
          !l.startsWith("Remedy: ") &&
          !l.startsWith("Run URL: "),
      ) || "";
  return plain(line) || n.subject;
}

export function approveReleaseLabel(r: {
  shortSoak: unknown;
  breaking: unknown;
}): string {
  if (r.shortSoak && r.breaking) return "Approve short soak & breaking";
  if (r.breaking) return "Approve breaking";
  if (r.shortSoak) return "Approve short soak";
  return "Approve";
}

// releaseConfirm is the question asked before a release decision, the
// React Inbox's own wording (notifications.js decideRelease).
export function releaseConfirm(
  r: PendingRelease,
  approve: boolean,
  shortSoakOk: boolean,
  allowBreaking: boolean,
): string {
  if (!approve) {
    return `Reject ${r.version}? Its release candidate is dropped, and the next release train run cuts the next one.`;
  }
  const soak = r.shortSoak
    ? `It ${r.soak || "has no recorded soak"}, under the minimum.`
    : r.soak
      ? `It ${r.soak}, and the release train did not flag it as short.`
      : "It has no recorded soak.";
  const breaking = "It carries breaking changes outside a major release.";
  if (shortSoakOk && allowBreaking) {
    return `Approve ${r.version} on a SHORT SOAK and with BREAKING changes outside a major release? ${soak} ${breaking} This accepts both (short_soak_ok, allow_breaking), tags ${r.version}, moves latest onto it and publishes its images.`;
  }
  if (allowBreaking) {
    return `Approve ${r.version} with BREAKING changes outside a major release? ${breaking} This accepts the breaking changes (allow_breaking), tags ${r.version}, moves latest onto it and publishes its images.`;
  }
  if (shortSoakOk) {
    return `Approve ${r.version} on a SHORT SOAK? ${soak} This accepts the short soak (short_soak_ok), tags ${r.version}, moves latest onto it and publishes its images.`;
  }
  return `Approve ${r.version}? This tags ${r.version}, moves latest onto it and publishes its images.`;
}

// --- entries ----------------------------------------------------------

function taskAction(
  t: Task,
  id: string,
  label: string,
  route: RouteKey,
  variant: InboxAction["variant"],
  extra: Partial<InboxAction> = {},
): InboxAction {
  return {
    id,
    label,
    route,
    params: { id: t.id },
    variant,
    repo: t.repo,
    ...extra,
  };
}

function taskEntry(t: Task, kind: EntryKind, groupId: string): InboxEntry {
  return {
    key: `task:${t.id}:${kind}`,
    kind,
    groupId,
    ref: `#${t.id}`,
    title: t.title,
    repo: t.repo || "",
    summary: "",
    pillLabel: "",
    pillTone: "neutral",
    at: t.updatedAt || t.createdAt || null,
    rationale: "",
    owner: t.owner || "",
    taskId: t.id,
    taskLink: `/task/${encodeURIComponent(t.id)}`,
    externalLink: pullRequestUrl(t),
    task: t,
    actions: [],
  };
}

// classifyTask puts one waiting task in the first group that claims it,
// so no task is listed twice (InboxPage.jsx's groupTasks).
export function classifyTask(t: Task, listOnly = false): InboxEntry | null {
  if (!waitsOnUser(t)) return null;
  if (waitingBaseRepair(t)) {
    const e = taskEntry(t, "base-repair", "base-repair");
    e.pillLabel = "Base repair";
    e.pillTone = "error";
    e.summary =
      "A red base branch's repair pull request waits for your approval.";
    e.rationale = "Nothing else lands on this branch until its repair merges.";
    if (!t.humanApprovedBy) {
      const blocked = submitBlock(t, listOnly);
      e.actions.push(
        taskAction(
          t,
          "submit",
          "Submit",
          "POST /api/tasks/{id}/submit",
          "primary",
          blocked ? { blocked } : {},
        ),
      );
    }
    return e;
  }
  if (t.state === "awaiting_reply") {
    if (t.awaitingAccess) {
      const e = taskEntry(t, "access", "access");
      e.pillLabel = "Access request";
      e.pillTone = "blast";
      e.summary = t.questionSummary || "This run asked for more access.";
      e.rationale = "Only a person can widen what a run may reach.";
      return e;
    }
    if (isStoppedTurn(t)) {
      const e = taskEntry(t, "stopped", "paused");
      e.pillLabel = "Stopped";
      e.pillTone = "neutral";
      e.summary = "Stopped by a person.";
      e.rationale =
        "Somebody stopped this run. It waits until a person resumes or closes it; nothing was asked.";
      e.actions.push(
        Object.assign(
          taskAction(
            t,
            "resume",
            "Resume",
            "POST /api/tasks/{id}/retry",
            "primary",
          ),
          { confirm: `Resume #${t.id}? A person stopped this run.` },
        ),
        taskAction(t, "close", "Close", "POST /api/tasks/{id}/close", "tonal", {
          confirm: `Close #${t.id}? The task is closed, and reopening puts it back.`,
          destructive: true,
        }),
      );
      return e;
    }
    const e = taskEntry(t, "reply", "reply");
    e.pillLabel = "Needs reply";
    e.pillTone = "reply";
    e.summary = t.questionSummary || "Answered on the task itself.";
    e.rationale = "The run is parked until somebody answers it.";
    e.actions.push(
      taskAction(
        t,
        "acknowledge",
        "Acknowledge",
        "POST /api/tasks/{id}/close",
        "tonal",
        {
          bulk: true,
          confirm: `Acknowledge #${t.id}? The task is closed, and reopening puts it back.`,
        },
      ),
    );
    return e;
  }
  if (awaitsHumanSubmit(t)) {
    const review = !!t.reviewQuestions;
    const blocked = submitBlock(t, listOnly);
    const e = taskEntry(
      t,
      review ? "review" : "submit",
      review ? "review" : "submit",
    );
    const red = gateSummary(t, listOnly).state === "failing";
    e.pillLabel = review ? "Review questions" : red ? "CI failing" : "Submit";
    e.pillTone = review ? "blast" : red ? "error" : "healthy";
    e.summary = review
      ? "The review left questions only you can settle."
      : blocked || "Approve the pull request; it merges once its gates pass.";
    e.rationale = review
      ? "A fresh review clears the gate once your decision is carried out."
      : "This repository waits for a person's approval before merging.";
    if (!t.humanApprovedBy) {
      e.actions.push(
        taskAction(
          t,
          "submit",
          "Submit",
          "POST /api/tasks/{id}/submit",
          review ? "tonal" : "primary",
          {
            bulk: !review,
            ...(blocked ? { blocked } : {}),
          },
        ),
      );
    }
    return e;
  }
  if (mergeBlocked(t)) {
    const e = taskEntry(t, "merge-blocked", "merge-blocked");
    e.pillLabel = t.mergeQueueBlockedAt ? "Merge blocked" : "Human review";
    e.pillTone = "error";
    e.summary = "The merge queue stopped driving this pull request.";
    e.rationale = "Sort it out by hand, then ask the queue to try again.";
    if (t.mergeQueueBlockedAt) {
      e.actions.push(
        taskAction(
          t,
          "retry-merge",
          "Retry merge",
          "POST /api/tasks/{id}/retry-merge",
          "primary",
        ),
      );
    }
    return e;
  }
  if (approvedButGated(t, listOnly)) {
    const s = gateSummary(t, listOnly);
    const names = s.failing.length ? s.failing : s.needed;
    const e = taskEntry(t, "merge-blocked", "merge-blocked");
    e.pillLabel = s.failing.length ? "Gate failing" : "Gate blocked";
    e.pillTone = "error";
    e.summary = `Approved, but ${names.join(", ")} ${names.length === 1 ? "is" : "are"} ${s.failing.length ? "failing" : "still needed"}.`;
    e.rationale =
      "Approval has been given; this merges once the gate passes. Fix it, retry it, or close a stale review holding it.";
    return e;
  }
  if (awaitsGitHubApproval(t)) {
    const e = taskEntry(t, "github-approval", "github-approval");
    e.pillLabel = "GitHub approval";
    e.pillTone = "blast";
    e.summary = "GitHub needs an approving review before it will merge this.";
    e.rationale = "Nothing in grain can approve on GitHub for you.";
    return e;
  }
  if (mergeQueueStalled(t)) {
    const e = taskEntry(t, "merge-stalled", "merge-stalled");
    e.pillLabel = "Stalled";
    e.pillTone = "blast";
    e.summary = `Stuck in ${t.mergeQueueState?.replace(/_/g, " ")}.`;
    e.rationale = "Whatever drives this merge-queue state has not moved it.";
    return e;
  }
  if (t.state === "failed") {
    const e = taskEntry(t, "failed", "failed");
    e.pillLabel = "Failed";
    e.pillTone = "error";
    e.summary = "The run failed.";
    e.rationale = "Retry clears the failure streak; Close gives up on it.";
    e.actions.push(
      taskAction(t, "retry", "Retry", "POST /api/tasks/{id}/retry", "primary", {
        bulk: true,
      }),
      taskAction(t, "close", "Close", "POST /api/tasks/{id}/close", "tonal", {
        confirm: `Close #${t.id}? Reopen puts it back.`,
        destructive: true,
      }),
    );
    return e;
  }
  if (t.stall) {
    const e = taskEntry(t, "stalled", "stalled");
    e.pillLabel = "Stuck";
    e.pillTone = "blast";
    e.summary = t.stall;
    e.rationale = "Grain reads this as stuck rather than busy.";
    return e;
  }
  if (t.state === "proposed") {
    const e = taskEntry(t, "proposal", "proposal");
    e.pillLabel = "Proposal";
    e.pillTone = "proposal";
    e.summary = t.proposedBy
      ? `Proposed by ${t.proposedBy}.`
      : t.generatedFrom
        ? `Proposed by #${t.generatedFrom}.`
        : "Waiting for approval.";
    e.rationale = "Nothing runs until somebody approves it.";
    e.actions.push(
      taskAction(
        t,
        "approve",
        "Approve",
        "POST /api/tasks/{id}/approve",
        "primary",
        { bulk: true },
      ),
      taskAction(t, "defer", "Defer", "POST /api/tasks/{id}/defer", "tonal", {
        bulk: true,
      }),
      taskAction(
        t,
        "decline",
        "Decline",
        "POST /api/tasks/{id}/close",
        "text",
        {
          confirm: `Decline #${t.id}? The proposal is closed; reopening puts it back.`,
          destructive: true,
        },
      ),
    );
    return e;
  }
  return null;
}

export function classifyNotification(n: Notification): InboxEntry {
  const release = pendingReleaseOf(n);
  const taskId =
    n.taskId || n.investigationTaskId || n.clearsWhen?.task || null;
  const title = n.title || n.subject || "Notification";
  const ref = release
    ? release.version
    : n.title && n.subject && n.subject !== n.title
      ? n.subject
      : taskId
        ? `#${taskId}`
        : n.subject || n.family || "alert";
  const base: InboxEntry = {
    key: `notification:${n.id}`,
    kind: release ? "release" : "notification",
    groupId: release ? "release" : "notification",
    ref,
    title: release ? `${release.version} is ready to promote` : title,
    repo: n.repo || release?.repo || "",
    owner: "",
    summary: release ? pendingReleaseSentence(n) : notificationSentence(n),
    pillLabel: release
      ? "Release approval"
      : n.severity === "critical"
        ? "Critical"
        : n.severity === "warning"
          ? "Warning"
          : "Alert",
    pillTone: release ? "blast" : n.severity === "critical" ? "error" : "blast",
    at: n.lastRaisedAt || n.raisedAt || null,
    rationale: release
      ? "Promoting tags the release, moves latest onto it and publishes its images."
      : "An alert stays open until somebody acknowledges or clears it.",
    taskId,
    taskLink: taskId ? `/task/${encodeURIComponent(taskId)}` : null,
    externalLink: null,
    notification: n,
    release: release ?? undefined,
    acked: !!n.ackedAt,
    actions: [],
  };
  if (!release) {
    if (!n.ackedAt) {
      base.actions.push({
        id: "ack",
        label: "Acknowledge",
        route: "POST /api/notifications/{id}/ack",
        params: { id: n.id },
        variant: "tonal",
        bulk: true,
      });
    }
    base.actions.push(
      {
        id: "investigate",
        label: "Investigate",
        route: "POST /api/notifications/{id}/investigate",
        params: { id: n.id },
        variant: "primary",
      },
      {
        id: "clear",
        label: "Clear",
        route: "POST /api/notifications/{id}/clear",
        params: { id: n.id },
        variant: "text",
        confirm: `Clear "${n.subject}"? It leaves the Inbox until it is raised again.`,
      },
    );
  }
  return base;
}

export function classifyLocalScript(a: LocalScriptApproval): InboxEntry {
  const call = [a.script || a.tool || "script", ...(a.args || [])].join(" ");
  return {
    key: `local-script:${a.taskId}:${a.id}`,
    kind: "host-script",
    groupId: "host-script",
    ref: `#${a.taskId}`,
    title: `Run ${a.script || a.tool || "a host script"}`,
    repo: "",
    owner: "",
    summary: call,
    pillLabel: "Host script",
    pillTone: "blast",
    at: null,
    rationale:
      "The script runs on a host, so a person approves the exact call.",
    taskId: a.taskId,
    taskLink: `/task/${encodeURIComponent(a.taskId)}`,
    externalLink: null,
    localScript: a,
    actions: [
      {
        id: "approve-script",
        label: "Approve",
        route: "POST /api/tasks/{id}/local-script-approvals/{approval}/answer",
        params: { id: a.taskId, approval: a.id },
        body: { approve: true },
        variant: "primary",
      },
      {
        id: "deny-script",
        label: "Deny",
        route: "POST /api/tasks/{id}/local-script-approvals/{approval}/answer",
        params: { id: a.taskId, approval: a.id },
        body: { approve: false },
        variant: "tonal",
        destructive: true,
      },
    ],
  };
}

export function buildEntries(
  tasks: Task[],
  notifications: Notification[],
  localScripts: LocalScriptApproval[],
  details?: Record<string, TaskDetail>,
): InboxEntry[] {
  const out: InboxEntry[] = [];
  for (const raw of tasks) {
    const { task: t, hasDetail } = withDetailGates(raw, details?.[raw.id]);
    const e = classifyTask(t, !!details && !hasDetail);
    if (e) out.push(e);
  }
  for (const n of notifications) out.push(classifyNotification(n));
  for (const a of localScripts) out.push(classifyLocalScript(a));
  return out;
}

export function groupEntries(entries: InboxEntry[]): InboxGroup[] {
  return GROUP_ORDER.map((g) => ({
    ...g,
    entries: entries.filter((e) => e.groupId === g.id),
  })).filter((g) => g.entries.length > 0);
}

export function matchesFilter(
  e: InboxEntry,
  filter: FilterId,
  seen: ReadonlySet<string>,
): boolean {
  if (filter === "all") return true;
  if (filter === "unread") return !seen.has(e.key);
  return GROUP_ORDER.find((g) => g.id === e.groupId)?.filter === filter;
}

export function matchesQuery(e: InboxEntry, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [e.ref, e.title, e.repo, e.owner, e.summary]
    .join("\n")
    .toLowerCase()
    .includes(needle);
}
