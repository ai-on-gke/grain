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

import type {
  AgentPauseEntry,
  GrainConfig,
  Notification,
  PipelinePauseStatus,
  Task,
} from "../api/models";
import {
  type PendingRelease,
  approveReleaseLabel,
  notificationSentence,
  pendingReleaseOf,
  pendingReleaseSentence,
  waitingBaseRepair,
} from "../inbox/inbox-classifier";
import {
  effectiveDisplayTimeZone,
  formatLocalReset,
  formatRemainingDuration,
  formatStamp,
} from "../shared/dispatch-status";
import { submitBlock } from "../shared/gates";
import type { HealthCounterSection, HostExecutorStatus } from "./fleet.models";

export type AlertSeverity = "critical" | "warning" | "info";

export interface AlertScope {
  kind: "deployment" | "connection";
  connection?: string;
  connectionName?: string;
}

export type HealthAlertAction =
  | {
      kind: "link";
      label: string;
      route: string[];
      queryParams?: Record<string, string>;
      fragment?: string;
    }
  | {
      kind: "open-power";
      label: string;
    }
  | {
      kind: "lift-agent-pause";
      label: string;
      entry: AgentPauseEntry;
    }
  | {
      kind: "resume-pipeline";
      label: string;
      pause: PipelinePauseStatus;
    }
  | {
      kind: "submit-task";
      label: string;
      taskId: string;
      repo?: string;
      blocked?: string;
    }
  | {
      kind: "approve-release" | "reject-release";
      label: string;
      notification: Notification;
      release: PendingRelease;
    }
  | {
      kind: "ack-notification";
      label: string;
      notification: Notification;
    }
  | {
      kind: "investigate-notification";
      label: string;
      notification: Notification;
    };

export interface HealthAlert {
  id: string;
  severity: AlertSeverity;
  title: string;
  sentence: string;
  scope: AlertScope;
  since?: string;
  until?: string;
  impact?: string;
  actions: HealthAlertAction[];
}

export interface UnrunnableSetting {
  key: string;
  stored: string;
  running: string;
}

export interface HealthSettingsResponse {
  pendingRestart?: string[];
  unrunnable?: UnrunnableSetting[];
  [key: string]: unknown;
}

export interface CLIRunSettingsResponse {
  hostExecutor?: HostExecutorStatus;
  [key: string]: unknown;
}

export interface GitHubPollStats {
  tracked?: number;
  backedOff?: number;
  baseSeconds?: number;
  capSeconds?: number;
  medianSeconds?: number;
  maxSeconds?: number;
}

export interface GitHubBudget {
  limit?: number;
  remaining?: number;
  resetsAt?: string;
  spendPerHour?: number;
  exhaustedAt?: string;
  low?: boolean;
  lowFraction?: number;
  polls?: GitHubPollStats;
}

export interface GitHubCallerKind {
  kind: string;
  spent: number;
}

export interface GitHubCaller {
  caller: string;
  spent: number;
  revalidated: number;
  kinds?: GitHubCallerKind[];
}

export interface GitHubMetricsSection {
  enabled?: boolean;
  budget?: GitHubBudget;
  polls?: GitHubPollStats;
  callers?: GitHubCaller[];
}

export interface MetricsGitHubResponse {
  github?: GitHubMetricsSection;
}

export interface HealthCountersResponse {
  counters?: HealthCounterSection[];
}

export interface StaleScheduleRepoRef {
  Host?: string;
  Owner?: string;
  Name?: string;
  host?: string;
  owner?: string;
  name?: string;
}

export interface StaleSchedule {
  id: string;
  title?: string;
  repo?: string | StaleScheduleRepoRef;
  cadence?: number;
  lastFiredAt?: string;
}

export interface StaleSchedulesResponse {
  staleSchedules?: StaleSchedule[];
}

export interface FormattedGitHubBudget {
  tone: "error" | "warn" | "info";
  headline: string;
  runsOutLine: string | null;
  pollsLine: string | null;
  lowLine: string | null;
  callers: {
    caller: string;
    spent: string;
    revalidated: string;
    mostly: string;
  }[];
}

const SEVERITY_RANK: Readonly<Record<AlertSeverity, number>> = {
  critical: 0,
  warning: 1,
  info: 2,
};

export const ALERT_GROUP_THRESHOLD = 5;
export const SHOWN_GITHUB_CALLERS = 6;

export function pauseConnectionLabel(key: string | undefined): string {
  if (!key) return "";
  const at = key.indexOf("@hostd/");
  if (at < 0) return key;
  return `${key.slice(0, at)} on host ${key.slice(at + "@hostd/".length)}`;
}

export function alertScopeLabel(scope: AlertScope): string {
  if (scope.kind !== "connection") return "Deployment";
  return `Connection ${scope.connectionName || pauseConnectionLabel(scope.connection)}`;
}

export function sortHealthAlerts(
  alerts: readonly HealthAlert[],
): HealthAlert[] {
  const at = (a: HealthAlert): number =>
    a.since ? Date.parse(a.since) || 0 : 0;
  return [...alerts].sort(
    (a, b) =>
      SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || at(b) - at(a),
  );
}

export function groupHealthAlerts(
  alerts: readonly HealthAlert[],
): { heading: string; rows: HealthAlert[] }[] | null {
  if (alerts.length < ALERT_GROUP_THRESHOLD) return null;
  const groups = new Map<string, HealthAlert[]>();
  for (const a of alerts) {
    const heading = alertScopeLabel(a.scope);
    const list = groups.get(heading);
    if (list) list.push(a);
    else groups.set(heading, [a]);
  }
  if (groups.size < 2) return null;
  const entries = [...groups.entries()].map(([heading, rows]) => ({
    heading,
    rows,
  }));
  const deployment = entries.filter((g) => g.heading === "Deployment");
  return [...deployment, ...entries.filter((g) => g.heading !== "Deployment")];
}

export function liftPauseQuery(entry: AgentPauseEntry): string {
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

function pauseSubject(entry: AgentPauseEntry): string {
  if (entry.connection) {
    const name = entry.connectionName || pauseConnectionLabel(entry.connection);
    if (entry.model) return `${entry.model} on ${name}`;
    return `connection ${name}`;
  }
  if (entry.framework) return `this deployment's ${entry.framework} runs`;
  return "";
}

export function formatPauseMessage(
  entry: AgentPauseEntry,
  tz: string,
  nowMs: number,
): string {
  const local = formatLocalReset(
    entry.until,
    entry.secondsRemaining,
    nowMs,
    tz,
  );
  const when =
    local ||
    (entry.secondsRemaining && entry.secondsRemaining > 0
      ? formatRemainingDuration(entry.secondsRemaining)
      : "the provider's window resets");
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

export function isScheduledUpgradeHold(
  pause: PipelinePauseStatus | null | undefined,
): boolean {
  return pause?.byKind === "automation" && pause?.byId === "upgrade-schedule";
}

export function formatPipelinePauseMessage(
  pause: PipelinePauseStatus,
  tz: string,
): string {
  const since = pause.since ? ` since ${formatStamp(pause.since, tz)}` : "";
  const reason = pause.reason ? ` — ${pause.reason}` : "";
  const waiting = pause.untilTask
    ? ` It lifts itself when task ${pause.untilTask} finishes.`
    : isScheduledUpgradeHold(pause)
      ? " The upgrade starts once they have, and Resume now cancels it."
      : "";
  const manual = pause.runNowPasses
    ? " Run now still starts the task it is pressed on."
    : " Run now waits for the upgrade too: its restart would discard the run.";
  return (
    `No new tasks are being picked up${since}: this deployment is paused. ` +
    `Runs already in flight are left to finish.${manual}${waiting}${reason}`
  );
}

interface StateSyncFailureShape {
  repo?: string;
  since?: string;
  error?: string;
  external?: boolean;
}

function hasGitCredential(dep: Record<string, unknown>): boolean {
  if (typeof dep["hasGitCredential"] === "boolean") {
    return dep["hasGitCredential"];
  }
  const legacy = String(dep["gitCredential"] ?? "").trim();
  if (legacy && legacy !== "anonymous") return true;
  const conns = Array.isArray(dep["gitConnections"])
    ? (dep["gitConnections"] as Record<string, unknown>[])
    : [];
  return conns.some((c) => {
    const cred = String(c?.["credential"] ?? "").trim();
    if (!cred || cred === "anonymous") return false;
    const host = String(c?.["host"] ?? "")
      .trim()
      .toLowerCase();
    return host === "" || host === "github.com";
  });
}

function deploymentSetupStepsLeft(
  dep: Record<string, unknown>,
  cfg: GrainConfig,
): { id: string; label: string; tab?: string; view?: string }[] {
  const repos = new Set<string>(
    Array.isArray(dep["targetRepos"])
      ? (dep["targetRepos"] as unknown[]).map(String).filter(Boolean)
      : [],
  );
  if (Array.isArray(dep["repos"])) {
    for (const r of dep["repos"] as Record<string, unknown>[]) {
      const slug = String(r?.["repo"] ?? "").trim();
      if (slug) repos.add(slug);
    }
  }
  const steps = [
    {
      id: "git",
      label: "Connect a git host",
      done: hasGitCredential(dep),
      tab: "git",
    },
    {
      id: "connections",
      label: "Add a model connection",
      done: (cfg.agentBackends ?? []).some((b) => !b.disabled),
      tab: "models",
    },
    {
      id: "repos",
      label: "Add a repository",
      done: repos.size > 0,
      view: "repos",
    },
  ];
  return steps.filter((s) => !s.done);
}

function baseRepairWaitReason(t: Task): string {
  if (t.state === "awaiting_reply") {
    return t.questionSummary
      ? "waiting for a reply to its question"
      : "waiting for a reply";
  }
  if (t.state === "failed") return "failed";
  if (t.state === "proposed") return "waiting for approval";
  if (t.mergeQueueBlockedAt) return "blocked in the merge queue";
  if (t.mergeQueueState === "waiting_github_approval") {
    return "waiting for GitHub approval";
  }
  if (t.state === "awaiting_submit" && !t.humanApprovedBy) {
    return "waiting for Submit";
  }
  if (t.state === "queued") return "queued";
  return "waiting in the merge queue";
}

export function collectHealthAlerts(
  config: GrainConfig | null | undefined,
  tasks: readonly Task[],
  notifications: readonly Notification[],
  preferredTz?: string | null,
  nowMs = Date.now(),
): HealthAlert[] {
  const tz = effectiveDisplayTimeZone(preferredTz ?? config?.timeZone);
  const out: HealthAlert[] = [];

  for (const t of tasks) {
    if (!waitingBaseRepair(t)) continue;
    const branch = String(t.redBaseBranch ?? "main");
    const actions: HealthAlertAction[] = [
      { kind: "link", label: `Open #${t.id}`, route: ["/task", t.id] },
    ];
    if (!t.autoMerge && !t.humanApprovedBy) {
      const blocked = submitBlock(t, true) || undefined;
      actions.push({
        kind: "submit-task",
        label: "Submit",
        taskId: t.id,
        repo: t.repo,
        blocked,
      });
    }
    out.push({
      id: `red-base-repair/${t.id}`,
      severity: "critical",
      title: `${branch} is red — repair #${t.id}`,
      sentence: `${branch} is red — repair #${t.id} is ${baseRepairWaitReason(t)}.`,
      scope: { kind: "deployment" },
      impact: `Other work targeting ${branch} waits until the repair merges.`,
      actions,
    });
  }

  if (config) {
    if (config.reconcilerDown) {
      const conditions = config.dispatchBlocked ?? [];
      const why = conditions
        .map(
          (c) =>
            `${(c.name || "condition").replaceAll("-", " ")}: ${c.error || ""}`,
        )
        .filter((s) => !s.endsWith(": "))
        .join("; ");
      const remedy = conditions.find((c) => c.remedy)?.remedy;
      const remedySentence = remedy
        ? remedy.charAt(0).toUpperCase() + remedy.slice(1)
        : "Restart the daemon to recover";
      out.push({
        id: "reconciler-down",
        severity: "critical",
        title: "Reconcile loop stopped",
        sentence:
          "This deployment's reconcile loop has stopped — no tasks are being dispatched or checked." +
          (why ? ` ${why}.` : "") +
          ` ${remedySentence.replace(/\.$/, "")}.`,
        scope: { kind: "deployment" },
        since: conditions[0]?.since || undefined,
        impact: "No task is dispatched or checked until the daemon restarts.",
        actions: [{ kind: "open-power", label: "Controls" }],
      });
    }

    const storeSwitch = config["storeSwitch"] as
      { message?: string; since?: string } | undefined;
    if (storeSwitch?.message) {
      out.push({
        id: "store-switched",
        severity: "critical",
        title: "Store switched while grain was running",
        sentence: storeSwitch.message.replace(/[^.!?]$/, "$&."),
        scope: { kind: "deployment" },
        since: storeSwitch.since || undefined,
        impact: "No task is dispatched until the daemon restarts.",
        actions: [{ kind: "open-power", label: "Controls" }],
      });
    }

    const sync = config.stateRepoSyncFailure as
      StateSyncFailureShape | null | undefined;
    if (sync && typeof sync === "object") {
      const repo = sync.repo || "the state repository";
      const sinceText = sync.since
        ? formatStamp(sync.since, tz)
        : "an unknown time";
      const rawSentence = sync.external
        ? `Configuration has not been pulled from ${repo} since ${sinceText}: ${sync.error || "sync failed"}`
        : `Settings have not been exported to ${repo} since ${sinceText}: ${sync.error || "sync failed"}`;
      out.push({
        id: "state-sync-failing",
        severity: "critical",
        title: sync.external
          ? "Configuration is not being pulled"
          : "Settings are not being exported",
        sentence: rawSentence.replace(/[^.!?]$/, "$&."),
        scope: { kind: "deployment" },
        since: sync.since || undefined,
        impact: sync.external
          ? `Changes merged in ${sync.repo || "the config repository"} since then are not applied here until the sync is fixed.`
          : "Changes made in the UI since then exist only in this deployment's database until the sync is fixed.",
        actions: [
          {
            kind: "link",
            label: "Config repo",
            route: ["/settings", "config-repo"],
          },
        ],
      });
    }

    if (config["hostSandboxes"]) {
      out.push({
        id: "host-sandboxes",
        severity: "warning",
        title: "No isolation: runs execute on this host",
        sentence:
          "Every dispatched run executes in a plain directory on the machine this daemon runs on, as its user, with that machine's filesystem, network and credentials — nothing isolates an agent from it, and what a run changes outside its directory is not thrown away with it.",
        scope: { kind: "deployment" },
        impact:
          "Do not file work here you would not run on this machine yourself. Restart the daemon with -container-sandboxes or -granule-sandboxes (or re-run scripts/setup.sh without GRAIN_HOST_SANDBOXES) to isolate each run.",
        actions: [
          {
            kind: "link",
            label: "Sandboxes",
            route: ["/settings", "sandboxes"],
            fragment: "sandboxBackend",
          },
        ],
      });
    }

    const agentPause = config.agentPause;
    if (agentPause?.paused) {
      const entries: AgentPauseEntry[] = agentPause.pauses?.length
        ? agentPause.pauses
        : [agentPause];
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
        const actions: HealthAlertAction[] = [];
        if (entry.canLift) {
          actions.push({
            kind: "lift-agent-pause",
            label: "Resume now",
            entry,
          });
        }
        if (entry.connection) {
          actions.push({
            kind: "link",
            label: "Open connection",
            route: ["/settings", "models"],
            fragment: "card-model-backends",
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
          sentence: formatPauseMessage(entry, tz, nowMs),
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
        title: pipeline.runNowPasses
          ? "Paused — only tasks you Run now will start"
          : "Deployment paused",
        sentence: formatPipelinePauseMessage(pipeline, tz),
        scope: { kind: "deployment" },
        since: pipeline.since || undefined,
        actions: [
          { kind: "resume-pipeline", label: "Resume now", pause: pipeline },
        ],
      });
    }

    const dep = config.deployment;
    if (dep && typeof dep === "object" && !dep["archived"]) {
      const missing = deploymentSetupStepsLeft(dep, config);
      if (missing.length > 0) {
        const blocking = missing.some(
          (s) => s.id === "git" || s.id === "connections",
        );
        const first = missing[0];
        out.push({
          id: "setup",
          severity: "info",
          title: `${missing.length} setup ${missing.length === 1 ? "step" : "steps"} left`,
          sentence: `Still to do: ${missing.map((s) => s.label.toLowerCase()).join(", ")}.`,
          scope: { kind: "deployment" },
          impact: blocking
            ? "Tasks wait until the deployment has a git host and a model connection."
            : undefined,
          actions: [
            {
              kind: "link",
              label: "Finish setup",
              route:
                first.view === "repos"
                  ? ["/repos"]
                  : ["/settings", first.tab || "general"],
            },
          ],
        });
      }
    }
  }

  for (const n of notifications) {
    if (n.clearedAt || n.ackedAt) continue;
    const release = pendingReleaseOf(n);
    const severity: AlertSeverity =
      n.severity === "critical" || n.severity === "info"
        ? n.severity
        : "warning";
    const actions: HealthAlertAction[] = [];
    if (n.taskId) {
      actions.push({
        kind: "link",
        label: `Open #${n.taskId}`,
        route: ["/task", n.taskId],
      });
    }
    if (release) {
      actions.push(
        {
          kind: "approve-release",
          label: approveReleaseLabel(release),
          notification: n,
          release,
        },
        {
          kind: "reject-release",
          label: "Reject",
          notification: n,
          release,
        },
      );
    }
    actions.push({
      kind: "ack-notification",
      label: "Acknowledge",
      notification: n,
    });
    if (n.investigationTaskId) {
      actions.push({
        kind: "link",
        label: "Open investigation",
        route: ["/task", n.investigationTaskId],
      });
    } else {
      actions.push({
        kind: "investigate-notification",
        label: "Investigate",
        notification: n,
      });
    }
    out.push({
      id: `notification-${n.id}`,
      severity,
      title: release
        ? `${release.version} is ready to promote`
        : n.title || n.subject || "Alert",
      sentence: release ? pendingReleaseSentence(n) : notificationSentence(n),
      scope: { kind: "deployment" },
      since: n.lastRaisedAt || n.raisedAt || undefined,
      actions,
    });
  }

  return sortHealthAlerts(out);
}

// --- GitHub API budget & idle-poll backoff (ui/src/components/system/GitHubBudgetPanel.jsx)

export function formatDurationSeconds(
  value: number | undefined | null,
): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return "—";
  }
  const s = Math.max(0, Math.round(value));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) {
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  }
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

function secondsUntil(at: string | undefined, nowMs: number): number | null {
  if (!at || at.startsWith("0001-")) return null;
  const s = (Date.parse(at) - nowMs) / 1000;
  return Number.isFinite(s) ? Math.max(0, s) : null;
}

export function formatGitHubBudget(
  github: GitHubMetricsSection | null | undefined,
  nowMs = Date.now(),
): FormattedGitHubBudget | null {
  const budget = github?.budget;
  if (!github?.enabled || !budget?.limit) return null;

  const resets = secondsUntil(budget.resetsAt, nowMs);
  const runsOut = secondsUntil(budget.exhaustedAt, nowMs);
  const tone: FormattedGitHubBudget["tone"] = budget.low
    ? "error"
    : runsOut != null
      ? "warn"
      : "info";

  const remaining = Number(budget.remaining ?? 0);
  const limit = Number(budget.limit ?? 0);
  const parts = [
    `${remaining.toLocaleString()} of ${limit.toLocaleString()} calls left this hour`,
  ];
  if (resets != null) {
    parts.push(`resets in ${formatDurationSeconds(resets)}`);
  }
  if ((budget.spendPerHour ?? 0) > 0) {
    parts.push(
      `spending ${Math.round(budget.spendPerHour!).toLocaleString()}/h`,
    );
  }

  const runsOutLine =
    runsOut != null
      ? `At that rate it runs out in ${formatDurationSeconds(runsOut)}, before it resets.`
      : null;

  const polls = github.polls?.tracked ? github.polls : budget.polls;
  let pollsLine: string | null = null;
  if (polls && (polls.tracked ?? 0) > 0) {
    const tracked = polls.tracked ?? 0;
    const backedOff = polls.backedOff ?? 0;
    const bounds =
      polls.baseSeconds && polls.capSeconds
        ? ` (${formatDurationSeconds(polls.baseSeconds)} base, ${formatDurationSeconds(polls.capSeconds)} cap)`
        : "";
    pollsLine =
      `Idle-poll backoff: ${backedOff.toLocaleString()} of ${tracked.toLocaleString()} tracked ` +
      `${tracked === 1 ? "PR" : "PRs"} backed off · poll interval median ` +
      `${formatDurationSeconds(polls.medianSeconds ?? 0)}, max ${formatDurationSeconds(polls.maxSeconds ?? 0)}${bounds}.`;
  }

  const lowLine = budget.low
    ? `Under ${Math.round((budget.lowFraction || 0.15) * 100)}% left: polling of idle pull requests, stale branches and path triggers is cut back. The merge queue and base-branch CI are not.`
    : null;

  const callers = (github.callers ?? [])
    .slice(0, SHOWN_GITHUB_CALLERS)
    .map((c) => ({
      caller: c.caller,
      spent: Number(c.spent ?? 0).toLocaleString(),
      revalidated: Number(c.revalidated ?? 0).toLocaleString(),
      mostly:
        (c.kinds ?? [])
          .slice(0, 2)
          .map((k) => `${k.kind} (${Number(k.spent ?? 0).toLocaleString()})`)
          .join(", ") || "—",
    }));

  return {
    tone,
    headline: `GitHub API: ${parts.join(" · ")}.`,
    runsOutLine,
    pollsLine,
    lowLine,
    callers,
  };
}

export function staleScheduleRepo(
  repo: StaleSchedule["repo"] | undefined,
): string {
  if (!repo) return "";
  if (typeof repo === "string") return repo;
  const owner = repo.Owner ?? repo.owner ?? "";
  const name = repo.Name ?? repo.name ?? "";
  if (!owner && !name) return "";
  const host = repo.Host ?? repo.host ?? "";
  return host ? `${host}/${owner}/${name}` : `${owner}/${name}`;
}

export function formatScheduleCadence(ns: number | undefined): string {
  const minutes = Math.round((Number(ns) || 0) / 6e10);
  if (minutes <= 0) return "—";
  if (minutes % 1440 === 0) return `${minutes / 1440}d`;
  if (minutes % 60 === 0) return `${minutes / 60}h`;
  if (minutes > 60) return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  return `${minutes}m`;
}
