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

// alerts.ts is every standing warning about the deployment as rows of one
// list: what the top bar's bell counts and its panel lists
// (alerts-bell.component.ts), and what Fleet → Health lists beside the
// rest of the deployment's health (fleet/health-panel.component.ts).
//
// It is the classic UI's alerts.js and notifications.js notificationAlerts
// ported as pure functions, with the same ids, titles, sentences and
// actions, so the two bells say the same thing about the same deployment
// while both UIs run side by side. Each alert is derived from what
// GET /api/config, the task list and GET /api/notifications?open=1 say
// now, so an alert clears the first poll after its cause has.

import type {
  AgentPauseEntry,
  GrainConfig,
  Notification,
  PipelinePauseStatus,
  Task,
} from "../api/models";
import type { RouteKey } from "../api/generated-routes";
import {
  approveReleaseLabel,
  awaitsGitHubApproval,
  mergeQueueStalled,
  notificationSentence,
  pendingReleaseOf,
  waitingBaseRepair,
  type PendingRelease,
} from "../inbox/inbox-classifier";

// Three severities, worst first (alerts.js SEVERITY_RANK):
//   - critical: grain is not doing its job and will not recover by
//     itself (the reconcile loop stopped, settings not being exported).
//   - warning: grain is deliberately holding work, or is configured in a
//     way somebody should know about (a pause, host sandboxes).
//   - info: something is unfinished rather than wrong (setup steps left).
export type AlertSeverity = "critical" | "warning" | "info";

const SEVERITY_RANK: Record<AlertSeverity, number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

export type AlertScope =
  | { kind: "deployment" }
  | { kind: "connection"; connection: string; connectionName: string };

// AlertAction is one button on an alert's row. `link` actions navigate
// (routerLink and its query); the rest are carried out by the panel
// (alerts-panel.component.ts). `route` is the API route the action calls,
// which the panel checks the reader's access against before offering it.
export type AlertAction =
  | {
      kind: "link";
      label: string;
      link: string;
      query?: Record<string, string>;
      testId: string;
    }
  | {
      kind: "lift-agent-pause";
      label: string;
      entry: AgentPauseEntry;
      route: RouteKey;
    }
  | {
      kind: "resume-pipeline";
      label: string;
      pause: PipelinePauseStatus;
      route: RouteKey;
    }
  | { kind: "submit-task"; label: string; taskId: string; route: RouteKey; repo: string }
  | {
      kind: "force-merge-task";
      label: string;
      taskId: string;
      branch: string;
      head?: string;
      route: RouteKey;
      repo: string;
    }
  | {
      kind: "ack-notification";
      label: string;
      notification: Notification;
      route: RouteKey;
    }
  | {
      kind: "investigate-notification";
      label: string;
      notification: Notification;
      route: RouteKey;
    }
  | {
      kind: "approve-release" | "reject-release";
      label: string;
      release: PendingRelease;
      route: RouteKey;
      repo: string;
    };

export interface GrainAlert {
  id: string;
  severity: AlertSeverity;
  title: string;
  sentence: string;
  scope: AlertScope;
  since?: string;
  until?: string;
  impact?: string;
  actions: AlertAction[];
}

// ---- Time, on the deployment's clock (ui/src/time.js).

function fmt(
  value: string | Date,
  zone: string,
  options: Intl.DateTimeFormatOptions,
): string {
  const at = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  const opts = { ...options };
  if (zone) opts.timeZone = zone;
  try {
    return at.toLocaleString(undefined, opts);
  } catch {
    // An unknown zone throws rather than falling back: the browser's own.
    delete opts.timeZone;
    return at.toLocaleString(undefined, opts);
  }
}

const DAY: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "numeric",
  day: "numeric",
};

export function formatDateTime(value: string, zone = ""): string {
  return fmt(value, zone, {});
}

// formatMoment is the clock time alone when value falls on today in zone,
// and the date and time otherwise: an alert can stand for days, and
// "Since 6:10:07 AM" on one raised last week reads as this morning.
export function formatMoment(
  value: string,
  zone = "",
  now: Date = new Date(),
): string {
  const on = fmt(value, zone, DAY);
  if (!on) return "";
  return on === fmt(now, zone, DAY)
    ? fmt(value, zone, { hour: "numeric", minute: "2-digit", second: "2-digit" })
    : formatDateTime(value, zone);
}

// formatResetMoment is formatMoment for a time somebody plans around --
// a pause lifting -- with the zone named and no seconds.
export function formatResetMoment(
  value: string,
  zone = "",
  now: Date = new Date(),
): string {
  const on = fmt(value, zone, DAY);
  if (!on) return "";
  const clock: Intl.DateTimeFormatOptions = {
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  };
  return fmt(
    value,
    zone,
    on === fmt(now, zone, DAY) ? clock : { ...DAY, ...clock },
  );
}

// formatRemaining is a countdown in hours and minutes, coarse on purpose:
// it is re-read every poll, and seconds would flicker.
export function formatRemaining(seconds: number | undefined): string {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return "less than a minute";
  const hours = Math.floor(s / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  if (hours === 0) return `about ${minutes}m`;
  return `about ${hours}h ${minutes}m`;
}

// ---- Usage-limit pauses (config.agentPause).

// pauseConnectionLabel reads a host-scoped pause key
// "<connection>@hostd/<host>" as "<connection> on host <host>".
function pauseConnectionLabel(key: string): string {
  const at = key.indexOf("@hostd/");
  if (at < 0) return key;
  return `${key.slice(0, at)} on host ${key.slice(at + "@hostd/".length)}`;
}

// liftQuery is the DELETE /api/pause query that lifts entry and nothing
// else (alerts.js liftPath).
export function liftQuery(entry: AgentPauseEntry): string {
  const q = new URLSearchParams();
  if (entry.connection) {
    q.set("connection", entry.connection);
    if (entry.model) q.set("model", entry.model);
  } else if (entry.team) {
    q.set("team", entry.team);
    if (entry.framework) q.set("framework", entry.framework);
  }
  return q.toString();
}

export function pauseSubject(entry: AgentPauseEntry): string {
  if (entry.connection) {
    const name = entry.connectionName || pauseConnectionLabel(entry.connection);
    if (entry.model) return `${entry.model} on ${name}`;
    return `connection ${name}`;
  }
  if (entry.framework) return `this deployment's ${entry.framework} runs`;
  return "";
}

// pauseMessage is one pause's sentence: what has stopped, until when (a
// clock time and a countdown), and what the provider itself said.
export function pauseMessage(
  entry: AgentPauseEntry,
  zone = "",
  now: Date = new Date(),
): string {
  const until = entry.until ? formatResetMoment(entry.until, zone, now) : "";
  const when = until
    ? `${until} (${formatRemaining(entry.secondsRemaining)})`
    : "the provider's window resets";
  const reason = entry.reason ? ` — ${entry.reason}` : "";
  const subject = pauseSubject(entry);
  if (entry.connection && entry.model) {
    return `${subject} paused until ${when} — usage limit; the connection's other models keep dispatching.${reason}`;
  }
  if (entry.connection && entry.escalated) {
    return `Agent usage limit reached: nothing is being dispatched onto ${subject}, on any model, until ${when} (${entry.escalated}).${reason}`;
  }
  const what = subject
    ? `nothing is being dispatched onto ${subject}`
    : "nothing is being dispatched";
  return `Agent usage limit reached: ${what} until ${when}.${reason}`;
}

// ---- The deployment's own pause (config.pipelinePause).

// scheduledUpgradeHold is the pause a scheduled upgrade keeps while it
// waits for the deployment to go idle, recognised by its principal.
export function scheduledUpgradeHold(pause: PipelinePauseStatus): boolean {
  return pause.byKind === "automation" && pause.byId === "upgrade-schedule";
}

// resumeCancelsUpgrade is what Resume asks before it lifts that hold.
export function resumeCancelsUpgrade(pause: PipelinePauseStatus): string {
  return (
    `This deployment is paused because it is waiting to upgrade${
      pause.reason ? ` — ${pause.reason}` : ""
    }.\n\n` +
    "Resuming cancels that upgrade: tasks start again and the upgrade " +
    "will not be applied. Schedule it again from Settings → Upgrade."
  );
}

export function pipelinePauseMessage(
  pause: PipelinePauseStatus,
  zone = "",
  now: Date = new Date(),
): string {
  const since = pause.since ? ` since ${formatMoment(pause.since, zone, now)}` : "";
  const reason = pause.reason ? ` — ${pause.reason}` : "";
  const waiting = pause.untilTask
    ? ` It lifts itself when task ${pause.untilTask} finishes.`
    : scheduledUpgradeHold(pause)
      ? " The upgrade starts once they have, and Resume now cancels it."
      : "";
  // Manual mode: what somebody starts with Run now still runs under every
  // pause but an upgrade's hold.
  const manual = pause.runNowPasses
    ? " Run now still starts the task it is pressed on."
    : " Run now waits for the upgrade too: its restart would discard the run.";
  return (
    `No new tasks are being picked up${since}: this deployment is paused. ` +
    `Runs already in flight are left to finish.${manual}${waiting}${reason}`
  );
}

export function pipelinePauseTitle(pause: PipelinePauseStatus): string {
  return pause.runNowPasses
    ? "Paused — only tasks you Run now will start"
    : "Deployment paused";
}

// ---- Settings not reaching the config repository
// (config.stateRepoSyncFailure).

interface StateSyncFailure {
  repo?: string;
  since?: string;
  error?: string;
  external?: boolean;
}

export const STATE_SYNC_TITLE = "Settings are not being exported";
export const STATE_PULL_TITLE = "Configuration is not being pulled";

export function stateSyncSentence(f: StateSyncFailure, zone = ""): string {
  const repo = f.repo || "the state repository";
  const since = f.since
    ? formatDateTime(f.since, zone) || "an unknown time"
    : "an unknown time";
  if (f.external) {
    return `Configuration has not been pulled from ${repo} since ${since}: ${f.error}`;
  }
  return `Settings have not been exported to ${repo} since ${since}: ${f.error}`;
}

function stateSyncImpact(f: StateSyncFailure): string {
  return f.external
    ? `Changes merged in ${f.repo || "the config repository"} since then are not applied here until the sync is fixed.`
    : "Changes made in the UI since then exist only in this deployment's database until the sync is fixed.";
}

// fullStop ends a sentence that does not already end in one (git's own
// errors do).
function fullStop(s: string): string {
  return s.replace(/[^.!?]$/, "$&.");
}

// ---- The deployment's setup steps left (alerts.js
// deploymentSetupStepsLeft).

interface SetupStep {
  id: "git" | "connections" | "repos";
  label: string;
  link: string;
}

// hasGitCredential is gitConnections.js's rule: the server's own answer
// when it gives one, else a non-anonymous credential for the default
// host.
function hasGitCredential(d: Record<string, unknown>): boolean {
  if (typeof d["hasGitCredential"] === "boolean") {
    return d["hasGitCredential"] as boolean;
  }
  const legacy = String(d["gitCredential"] ?? "").trim();
  if (legacy && legacy !== "anonymous") return true;
  const conns = Array.isArray(d["gitConnections"])
    ? (d["gitConnections"] as { credential?: string; host?: string }[])
    : [];
  return conns.some((c) => {
    const cred = (c?.credential || "").trim();
    if (!cred || cred === "anonymous") return false;
    const host = (c.host || "").trim().toLowerCase();
    return host === "" || host === "github.com";
  });
}

export function deploymentSetupStepsLeft(
  deployment: Record<string, unknown> | undefined,
  config: GrainConfig,
): SetupStep[] {
  if (!deployment) return [];
  const repos = new Set<string>(
    Array.isArray(deployment["targetRepos"])
      ? (deployment["targetRepos"] as string[])
      : [],
  );
  if (Array.isArray(deployment["repos"])) {
    for (const r of deployment["repos"] as { repo?: string }[]) {
      if (r?.repo) repos.add(r.repo);
    }
  }
  const steps: (SetupStep & { done: boolean })[] = [
    {
      id: "git",
      label: "Connect a git host",
      done: hasGitCredential(deployment),
      link: "/settings/git",
    },
    {
      id: "connections",
      label: "Add a model connection",
      done: (config.agentBackends || []).some(
        (b) => !(b as { disabled?: boolean }).disabled,
      ),
      link: "/settings/models",
    },
    {
      id: "repos",
      label: "Add a repository",
      done: repos.size > 0,
      link: "/repos",
    },
  ];
  return steps.filter((s) => !s.done);
}

export function setupAlertText(left: number): string {
  return `${left} setup ${left === 1 ? "step" : "steps"} left`;
}

const HOST_SANDBOXES_TITLE = "No isolation: runs execute on this host";

// ---- A red base branch whose repair pull request waits
// (state.js baseRepairWaitReason / redBaseRepairSentence).

const MERGE_QUEUE_PHRASES: Record<string, string> = {
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

type LooseTask = Task & Record<string, unknown>;

function awaitingReplyPhrase(t: LooseTask): string {
  if (t["unansweredCommentFrom"]) {
    return `unanswered question from ${t["unansweredCommentFrom"]}`;
  }
  return t["questionSummary"] || t["pendingSecret"]
    ? "waiting for a reply to its question"
    : "answered in a comment";
}

export function baseRepairWaitReason(task: Task): string {
  const t = task as LooseTask;
  if (!t.autoMerge) {
    const approvals = (t["approvals"] as
      | { rule?: string; approvers?: string[]; approvedBy?: string }[]
      | undefined) ?? [];
    const pending = approvals.filter((a) => !a.approvedBy);
    if (pending.length > 0) {
      const rules = pending
        .map((a) => `${a.rule} (${(a.approvers || []).join(" or ") || "human"})`)
        .join(", ");
      return `waiting for human approval on ${rules}`;
    }
    const holds = (t.humanReviewHold ?? []) as { reason?: string }[];
    if (holds.length) {
      const line = [...new Set(holds.map((r) => r.reason))].join("; ");
      return `held for a person's review (${line}) — waiting for Submit or Release hold`;
    }
    if (t["pathTriggerHold"]) {
      return `held by repository gating triggers (human: ${t["pathTriggerHold"]}) — waiting for Submit`;
    }
    if (t["securityHold"]) {
      const review = t["securityReviewTask"]
        ? ` for security review #${t["securityReviewTask"]}`
        : "";
      return `on security hold (${t["securityHold"]})${review} — waiting for Submit`;
    }
    if (t.reviewQuestions) {
      return `waiting on review questions from #${t.reviewQuestions}`;
    }
    if (t.state === "awaiting_reply") return awaitingReplyPhrase(t);
    if (t.state === "failed") return "failed and waiting for retry";
    if (t.state === "proposed") return "waiting for approval";
    return "awaiting submit";
  }
  const note = t["mergeQueueNote"] as string | undefined;
  if (t.mergeQueueBlockedAt) {
    return note
      ? `blocked on the merge queue (${note})`
      : "blocked on the merge queue";
  }
  if (awaitsGitHubApproval(t)) {
    return note
      ? `waiting for GitHub approval (${note})`
      : "waiting for GitHub approval";
  }
  if (mergeQueueStalled(t)) {
    return note
      ? `stalled in the merge queue (${note})`
      : "stalled in the merge queue";
  }
  if (t.stall) return `stalled (${t.stall})`;
  if (t.state === "awaiting_reply") return awaitingReplyPhrase(t);
  if (t.state === "failed") return "failed and waiting for retry";
  if (note) return `waiting in the merge queue (${note})`;
  const phrase = t.mergeQueueState && MERGE_QUEUE_PHRASES[t.mergeQueueState];
  if (phrase) return `waiting in the merge queue (${phrase.toLowerCase()})`;
  if (t.state === "queued") {
    return t["repairing"] ? "queued for repair" : "queued";
  }
  return "waiting in the merge queue";
}

export function redBaseRepairSentence(t: Task): string {
  return `${String(t.redBaseBranch)} is red — repair #${t.id} is ${baseRepairWaitReason(t)}`;
}

function redBaseRepairAlerts(tasks: readonly Task[]): GrainAlert[] {
  return tasks.filter(waitingBaseRepair).map((t) => {
    const branch = String(t.redBaseBranch);
    const repo = t.repo || "";
    const actions: AlertAction[] = [
      {
        kind: "link",
        label: `Open #${t.id}`,
        link: `/task/${t.id}`,
        testId: "alert-open-task",
      },
    ];
    if (!t.autoMerge) {
      actions.push({
        kind: "submit-task",
        label: "Submit",
        taskId: t.id,
        route: "POST /api/tasks/{id}/submit",
        repo,
      });
    }
    actions.push({
      kind: "force-merge-task",
      label: "Force merge",
      taskId: t.id,
      branch,
      head: (t.ci as { commit?: string } | undefined)?.commit || undefined,
      route: "POST /api/tasks/{id}/force-merge",
      repo,
    });
    return {
      id: `red-base-repair/${t.id}`,
      severity: "critical",
      title: `${branch} is red — repair #${t.id}`,
      sentence: redBaseRepairSentence(t),
      scope: { kind: "deployment" },
      impact: `Other work targeting ${branch} waits until the repair merges.`,
      actions,
    };
  });
}

// ---- Open notifications (notifications.js notificationAlerts).

export function notificationAlerts(
  notifications: readonly Notification[],
): GrainAlert[] {
  return notifications
    .filter(
      (n) => !(n as Notification & { clearedAt?: string }).clearedAt && !n.ackedAt,
    )
    .map((n) => {
      const release = pendingReleaseOf(n);
      const repo = n.repo || release?.repo || "";
      const actions: AlertAction[] = [];
      if (n.taskId) {
        actions.push({
          kind: "link",
          label: `Open #${n.taskId}`,
          link: `/task/${n.taskId}`,
          testId: "alert-open-task",
        });
      }
      if (release) {
        actions.push(
          {
            kind: "approve-release",
            label: approveReleaseLabel(release),
            release,
            route:
              "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
            repo,
          },
          {
            kind: "reject-release",
            label: "Reject",
            release,
            route:
              "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
            repo,
          },
        );
      }
      actions.push(
        {
          kind: "ack-notification",
          label: "Acknowledge",
          notification: n,
          route: "POST /api/notifications/{id}/ack",
        },
        {
          kind: "investigate-notification",
          label: n.investigationTaskId ? "Open investigation" : "Investigate",
          notification: n,
          route: "POST /api/notifications/{id}/investigate",
        },
      );
      const sev = n.severity;
      return {
        id: `notification-${n.id}`,
        severity:
          sev === "critical" || sev === "warning" || sev === "info" ? sev : "info",
        title: n.title || n.subject,
        sentence: notificationSentence(n),
        scope: { kind: "deployment" },
        since: n.raisedAt,
        actions,
      };
    });
}

// ---- Everything config says (alerts.js collectAlerts).

const POWER: AlertAction = {
  kind: "link",
  label: "Power",
  link: "/fleet",
  query: { tab: "controls" },
  testId: "alert-open-power",
};

export function collectAlerts(
  config: GrainConfig | null,
  tasks: readonly Task[] = [],
  zone = "",
  now: Date = new Date(),
): GrainAlert[] {
  if (!config) return [];
  const out = redBaseRepairAlerts(tasks);

  if (config.reconcilerDown) {
    const conditions = config.dispatchBlocked || [];
    const why = conditions
      .map((c) => `${(c.name || "").replaceAll("-", " ")}: ${c.error}`)
      .join("; ");
    const remedy = conditions.find((c) => c.remedy)?.remedy;
    out.push({
      id: "reconciler-down",
      severity: "critical",
      title: "Reconcile loop stopped",
      sentence:
        "This deployment's reconcile loop has stopped -- no tasks are being dispatched or checked." +
        (why ? ` ${why}.` : "") +
        ` ${remedy ? remedy.charAt(0).toUpperCase() + remedy.slice(1) : "Restart the daemon to recover"}.`,
      scope: { kind: "deployment" },
      since: conditions[0]?.since || undefined,
      impact: "No task is dispatched or checked until the daemon restarts.",
      actions: [POWER],
    });
  }

  const storeSwitch = config["storeSwitch"] as
    | { message?: string; since?: string }
    | undefined;
  if (storeSwitch) {
    out.push({
      id: "store-switched",
      severity: "critical",
      title: "Store switched while grain was running",
      sentence: fullStop(storeSwitch.message || ""),
      scope: { kind: "deployment" },
      since: storeSwitch.since || undefined,
      impact: "No task is dispatched until the daemon restarts.",
      actions: [],
    });
  }

  const sync = config.stateRepoSyncFailure as StateSyncFailure | undefined;
  if (sync) {
    out.push({
      id: "state-sync-failing",
      severity: "critical",
      title: sync.external ? STATE_PULL_TITLE : STATE_SYNC_TITLE,
      sentence: fullStop(stateSyncSentence(sync, zone)),
      scope: { kind: "deployment" },
      since: sync.since || undefined,
      impact: stateSyncImpact(sync),
      actions: [
        {
          kind: "link",
          label: "Config repo",
          link: "/settings/config-repo",
          testId: "alert-open-config-repo",
        },
      ],
    });
  }

  if (config["hostSandboxes"]) {
    out.push({
      id: "host-sandboxes",
      severity: "warning",
      title: HOST_SANDBOXES_TITLE,
      sentence:
        "Every dispatched run executes in a plain directory on the machine this daemon runs on, as its user, with that machine's filesystem, network and credentials -- nothing isolates an agent from it, and what a run changes outside its directory is not thrown away with it.",
      scope: { kind: "deployment" },
      impact:
        "Do not file work here you would not run on this machine yourself. Restart the daemon with -container-sandboxes or -granule-sandboxes (or re-run scripts/setup.sh without GRAIN_HOST_SANDBOXES) to isolate each run.",
      actions: [],
    });
  }

  // One alert per pause the server reports, each with its own scope and
  // Resume button; an older daemon's single pause is one row.
  const agentPause = config.agentPause;
  if (agentPause?.paused) {
    const entries: AgentPauseEntry[] = agentPause.pauses?.length
      ? agentPause.pauses
      : [agentPause as AgentPauseEntry];
    entries.forEach((entry, i) => {
      const key = entry.connection
        ? entry.connection + (entry.model ? `/${entry.model}` : "")
        : entry.framework || String(i);
      const scope: AlertScope = entry.connection
        ? {
            kind: "connection",
            connection: entry.connection,
            connectionName:
              entry.connectionName || pauseConnectionLabel(entry.connection),
          }
        : { kind: "deployment" };
      const actions: AlertAction[] = [];
      if (entry.canLift) {
        actions.push({
          kind: "lift-agent-pause",
          label: "Resume now",
          entry,
          route: "DELETE /api/pause",
        });
      }
      if (entry.connection) {
        actions.push({
          kind: "link",
          label: "Open connection",
          link: "/settings/models",
          testId: "alert-open-connection",
        });
      }
      out.push({
        id: `agent-pause/${entry.team || ""}/${key}`,
        severity: "warning",
        title: entry.model
          ? `Usage limit reached (${entry.model})`
          : entry.framework
            ? `Usage limit reached (${entry.framework})`
            : "Usage limit reached",
        sentence: pauseMessage(entry, zone, now),
        scope,
        until: entry.until || undefined,
        impact: "Runs in flight on it were cancelled; queued work waits.",
        actions,
      });
    });
  }

  const pipeline = config.pipelinePause;
  if (pipeline?.paused) {
    out.push({
      id: "pipeline-pause",
      severity: "warning",
      title: pipelinePauseTitle(pipeline),
      sentence: pipelinePauseMessage(pipeline, zone, now),
      scope: { kind: "deployment" },
      since: pipeline.since || undefined,
      actions: [
        {
          kind: "resume-pipeline",
          label: "Resume now",
          pause: pipeline,
          route: "DELETE /api/pipeline-pause",
        },
      ],
    });
  }

  const deployment = config.deployment;
  if (deployment && !deployment["archived"]) {
    const missing = deploymentSetupStepsLeft(deployment, config);
    if (missing.length > 0) {
      const blocking = missing.some(
        (s) => s.id === "git" || s.id === "connections",
      );
      out.push({
        id: "setup",
        severity: "info",
        title: setupAlertText(missing.length),
        sentence: `Still to do: ${missing.map((s) => s.label.toLowerCase()).join(", ")}.`,
        scope: { kind: "deployment" },
        impact: blocking
          ? "Tasks wait until the deployment has a git host and a model connection."
          : undefined,
        actions: [
          {
            kind: "link",
            label: "Finish setup",
            link: missing[0].link,
            testId: "alert-open-setup",
          },
        ],
      });
    }
  }

  return sortAlerts(out);
}

// sortAlerts is severity first, then the most recent first.
export function sortAlerts(alerts: readonly GrainAlert[]): GrainAlert[] {
  const at = (a: GrainAlert) => (a.since ? Date.parse(a.since) || 0 : 0);
  return [...alerts].sort(
    (a, b) =>
      SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || at(b) - at(a),
  );
}

export function worstSeverity(
  alerts: readonly GrainAlert[],
): AlertSeverity | null {
  let worst: AlertSeverity | null = null;
  for (const a of alerts) {
    if (worst === null || SEVERITY_RANK[a.severity] < SEVERITY_RANK[worst]) {
      worst = a.severity;
    }
  }
  return worst;
}

// scopeLabel is where an alert applies: "Deployment", or "Connection
// <name>".
export function scopeLabel(scope: AlertScope): string {
  if (scope.kind !== "connection") return "Deployment";
  return `Connection ${scope.connectionName || pauseConnectionLabel(scope.connection)}`;
}

// GROUP_AT is how many alerts it takes before the panel groups them by
// scope.
const GROUP_AT = 5;

// groupAlerts is alerts under a heading per scope, "Deployment" first,
// once there are GROUP_AT of them across more than one scope; null lists
// them flat.
export function groupAlerts(
  alerts: readonly GrainAlert[],
): [string, GrainAlert[]][] | null {
  if (alerts.length < GROUP_AT) return null;
  const groups = new Map<string, GrainAlert[]>();
  for (const a of alerts) {
    const heading = scopeLabel(a.scope);
    if (!groups.has(heading)) groups.set(heading, []);
    groups.get(heading)!.push(a);
  }
  if (groups.size < 2) return null;
  const entries = [...groups.entries()];
  return [
    ...entries.filter(([h]) => h === "Deployment"),
    ...entries.filter(([h]) => h !== "Deployment"),
  ];
}
