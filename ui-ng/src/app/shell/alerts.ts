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

import type { GrainConfig, Task } from "../api/models";
import { formatMoment } from "./clock";

export { formatMoment, preferredZone } from "./clock";

// alerts.ts is every standing warning about the deployment as rows of one
// list, read from GET /api/config (and, for a red base branch, the task
// feed): what the shell's alerts bell counts and its panel lists. It is
// the /next/ port of the classic UI's ui/src/alerts.js collectAlerts, in
// the same words, so the two UIs say the same thing about the same
// deployment. Each row links to the /next/ page that fixes it, and goes
// away on the next poll after its cause does.

export type AlertSeverity = "critical" | "warning" | "info";

export interface AlertLink {
  label: string;
  route: string;
  query?: Record<string, string>;
}

export interface ShellAlert {
  id: string;
  severity: AlertSeverity;
  title: string;
  sentence: string;
  // "Deployment", or "Connection <name>".
  scope: string;
  since?: string;
  impact?: string;
  link?: AlertLink;
}

// Severity worst first, as classic SEVERITY_RANK:
//   critical -- grain is not doing its job and will not recover alone;
//   warning  -- grain is deliberately holding work, or runs unisolated;
//   info     -- something unfinished rather than wrong (setup steps).
const SEVERITY_RANK: Record<AlertSeverity, number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

export function worstSeverity(alerts: ShellAlert[]): AlertSeverity | null {
  let worst: AlertSeverity | null = null;
  for (const a of alerts) {
    if (worst === null || SEVERITY_RANK[a.severity] < SEVERITY_RANK[worst]) {
      worst = a.severity;
    }
  }
  return worst;
}

export function sortAlerts(alerts: ShellAlert[]): ShellAlert[] {
  const at = (a: ShellAlert) => (a.since ? Date.parse(a.since) || 0 : 0);
  return [...alerts].sort(
    (a, b) =>
      SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || at(b) - at(a),
  );
}

export function formatRemaining(seconds: number | undefined): string {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return "less than a minute";
  const hours = Math.floor(s / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  if (hours === 0) return `about ${minutes}m`;
  return `about ${hours}h ${minutes}m`;
}

// ---- Config shapes read here (ui.PipelinePauseStatus, ui.AgentPauseStatus,
// ui.StateRepoSyncFailure, ...). Every field optional: an older daemon
// leaves things out.

export interface PipelinePause {
  paused?: boolean;
  since?: string;
  reason?: string;
  untilTask?: string;
  runNowPasses?: boolean;
  byKind?: string;
  byId?: string;
}

export interface AgentPauseEntry {
  paused?: boolean;
  connection?: string;
  connectionName?: string;
  model?: string;
  framework?: string;
  team?: string;
  until?: string;
  secondsRemaining?: number;
  reason?: string;
  escalated?: string;
  canLift?: boolean;
}

interface AgentPause extends AgentPauseEntry {
  pauses?: AgentPauseEntry[];
}

interface StateSyncFailure {
  repo?: string;
  since?: string;
  error?: string;
  external?: boolean;
}

interface DispatchCondition {
  name?: string;
  error?: string;
  remedy?: string;
  since?: string;
}

interface DeploymentRow {
  archived?: boolean;
  hasGitCredential?: boolean;
  gitCredential?: string;
  gitConnections?: { host?: string; credential?: string }[];
  targetRepos?: string[];
  repos?: { repo?: string }[];
}

function pauseConnectionLabel(key: string): string {
  const at = key.indexOf("@hostd/");
  if (at < 0) return key;
  return `${key.slice(0, at)} on host ${key.slice(at + "@hostd/".length)}`;
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

export function pauseMessage(
  entry: AgentPauseEntry,
  zone = "",
  now = new Date(),
): string {
  const until = entry.until ? formatMoment(entry.until, zone, now) : "";
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

export function scheduledUpgradeHold(pause: PipelinePause): boolean {
  return pause.byKind === "automation" && pause.byId === "upgrade-schedule";
}

export function pipelinePauseTitle(pause: PipelinePause): string {
  return pause.runNowPasses
    ? "Paused — only tasks you Run now will start"
    : "Deployment paused";
}

export function pipelinePauseMessage(pause: PipelinePause, zone = ""): string {
  const since = pause.since ? ` since ${formatMoment(pause.since, zone)}` : "";
  const reason = pause.reason ? ` — ${pause.reason}` : "";
  const waiting = pause.untilTask
    ? ` It lifts itself when task ${pause.untilTask} finishes.`
    : scheduledUpgradeHold(pause)
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

export function stateSyncTitle(f: StateSyncFailure): string {
  return f.external
    ? "Configuration is not being pulled"
    : "Settings are not being exported";
}

function stateSyncSentence(f: StateSyncFailure, zone: string): string {
  const repo = f.repo || "the state repository";
  const since = f.since
    ? formatMoment(f.since, zone, new Date(0)) || "an unknown time"
    : "an unknown time";
  return f.external
    ? `Configuration has not been pulled from ${repo} since ${since}: ${f.error ?? ""}`
    : `Settings have not been exported to ${repo} since ${since}: ${f.error ?? ""}`;
}

function stateSyncImpact(f: StateSyncFailure): string {
  return f.external
    ? `Changes merged in ${f.repo || "the config repository"} since then are not applied here until the sync is fixed.`
    : "Changes made in the UI since then exist only in this deployment's database until the sync is fixed.";
}

function hasGitCredential(d: DeploymentRow): boolean {
  if (typeof d.hasGitCredential === "boolean") return d.hasGitCredential;
  const legacy = (d.gitCredential || "").trim();
  if (legacy && legacy !== "anonymous") return true;
  return (d.gitConnections || []).some((c) => {
    const cred = (c?.credential || "").trim();
    if (!cred || cred === "anonymous") return false;
    const host = (c.host || "").trim().toLowerCase();
    return host === "" || host === "github.com";
  });
}

interface SetupStep {
  id: string;
  label: string;
  link: AlertLink;
}

export function deploymentSetupStepsLeft(
  d: DeploymentRow,
  config: GrainConfig,
): SetupStep[] {
  const repos = new Set(d.targetRepos || []);
  for (const r of d.repos || []) if (r?.repo) repos.add(r.repo);
  const steps: (SetupStep & { done: boolean })[] = [
    {
      id: "git",
      label: "Connect a git host",
      done: hasGitCredential(d),
      link: { label: "Settings → Git", route: "/settings/git" },
    },
    {
      id: "connections",
      label: "Add a model connection",
      done: (config.agentBackends || []).some((b) => !b.disabled),
      link: { label: "Settings → Models", route: "/settings/models" },
    },
    {
      id: "repos",
      label: "Add a repository",
      done: repos.size > 0,
      link: {
        label: "Settings → Repositories",
        route: "/settings/repositories",
      },
    },
  ];
  return steps.filter((s) => !s.done);
}

// ---- Red base branches (task feed rows; classic state.js
// waitingBaseRepair / redBaseRepairSentence, shortened: the panel names
// the repair and links to it, and its own page says what it waits on).

function isMerged(t: Task): boolean {
  const r = t as unknown as Record<string, unknown>;
  return !!(
    r["merged"] ||
    r["prMergedAt"] ||
    r["closedReason"] === "merged" ||
    (Array.isArray(r["pullRequestEvents"]) &&
      (r["pullRequestEvents"] as { kind?: string }[]).some(
        (e) => e?.kind === "merged",
      ))
  );
}

export function waitingBaseRepair(t: Task): boolean {
  const r = t as unknown as Record<string, unknown>;
  if (
    t.state === "closed" ||
    t.state === "running" ||
    r["mergeQueueState"] === "merging" ||
    isMerged(t)
  ) {
    return false;
  }
  return Boolean(r["redBaseBranch"] && r["pullRequest"]);
}

function repairWait(t: Task): string {
  const r = t as unknown as Record<string, unknown>;
  if (!r["autoMerge"]) {
    if (t.state === "awaiting_reply") return "waiting on a reply";
    if (t.state === "failed") return "failed and waiting for retry";
    if (t.state === "proposed") return "waiting for approval";
    return "awaiting submit";
  }
  return "waiting to merge";
}

function redBaseRepairAlerts(tasks: Task[]): ShellAlert[] {
  return tasks.filter(waitingBaseRepair).map((t) => {
    const branch = String(
      (t as unknown as Record<string, unknown>)["redBaseBranch"],
    );
    return {
      id: `red-base-repair/${t.id}`,
      severity: "critical" as const,
      title: `${branch} is red — repair #${t.id}`,
      sentence: `${branch} is red — repair #${t.id} is ${repairWait(t)}`,
      scope: "Deployment",
      impact: `Other work targeting ${branch} waits until the repair merges.`,
      link: { label: `Open #${t.id}`, route: `/task/${t.id}` },
    };
  });
}

const CONTROLS: AlertLink["query"] = { tab: "controls" };

// collectAlerts is every standing alert config (and the task feed)
// describes, worst first.
export function collectAlerts(
  config: GrainConfig | null,
  tasks: Task[] = [],
  zone = "",
  now = new Date(),
): ShellAlert[] {
  if (!config) return [];
  const c = config as Record<string, unknown>;
  const out: ShellAlert[] = [...redBaseRepairAlerts(tasks)];

  if (c["reconcilerDown"]) {
    const conditions = (c["dispatchBlocked"] as DispatchCondition[]) || [];
    const why = conditions
      .map((d) => `${(d.name || "").replaceAll("-", " ")}: ${d.error ?? ""}`)
      .join("; ");
    const remedy = conditions.find((d) => d.remedy)?.remedy;
    out.push({
      id: "reconciler-down",
      severity: "critical",
      title: "Reconcile loop stopped",
      sentence:
        "This deployment's reconcile loop has stopped -- no tasks are being dispatched or checked." +
        (why ? ` ${why}.` : "") +
        ` ${remedy ? remedy.charAt(0).toUpperCase() + remedy.slice(1) : "Restart the daemon to recover"}.`,
      scope: "Deployment",
      since: conditions[0]?.since || undefined,
      impact: "No task is dispatched or checked until the daemon restarts.",
      link: { label: "Fleet → Controls", route: "/fleet", query: CONTROLS },
    });
  }

  const storeSwitch = c["storeSwitch"] as
    { message?: string; since?: string } | undefined;
  if (storeSwitch) {
    out.push({
      id: "store-switched",
      severity: "critical",
      title: "Store switched while grain was running",
      sentence: (storeSwitch.message || "").replace(/[^.!?]$/, "$&."),
      scope: "Deployment",
      since: storeSwitch.since || undefined,
      impact: "No task is dispatched until the daemon restarts.",
      link: { label: "Fleet → Controls", route: "/fleet", query: CONTROLS },
    });
  }

  const sync = c["stateRepoSyncFailure"] as StateSyncFailure | undefined;
  if (sync) {
    out.push({
      id: "state-sync-failing",
      severity: "critical",
      title: stateSyncTitle(sync),
      sentence: stateSyncSentence(sync, zone).replace(/[^.!?]$/, "$&."),
      scope: "Deployment",
      since: sync.since || undefined,
      impact: stateSyncImpact(sync),
      link: { label: "Settings → Config repo", route: "/settings/config-repo" },
    });
  }

  if (c["hostSandboxes"]) {
    out.push({
      id: "host-sandboxes",
      severity: "warning",
      title: "No isolation: runs execute on this host",
      sentence:
        "Every dispatched run executes in a plain directory on the machine this daemon runs on, as its user, with that machine's filesystem, network and credentials -- nothing isolates an agent from it, and what a run changes outside its directory is not thrown away with it.",
      scope: "Deployment",
      impact:
        "Do not file work here you would not run on this machine yourself. Restart the daemon with -container-sandboxes or -granule-sandboxes (or re-run scripts/setup.sh without GRAIN_HOST_SANDBOXES) to isolate each run.",
      link: { label: "Settings → Sandboxes", route: "/settings/sandboxes" },
    });
  }

  const agentPause = c["agentPause"] as AgentPause | undefined;
  if (agentPause?.paused) {
    const entries = agentPause.pauses?.length
      ? agentPause.pauses
      : [agentPause];
    entries.forEach((entry, i) => {
      const key = entry.connection
        ? entry.connection + (entry.model ? `/${entry.model}` : "")
        : entry.framework || String(i);
      out.push({
        id: `agent-pause/${entry.team || ""}/${key}`,
        severity: "warning",
        title: entry.model
          ? `Usage limit reached (${entry.model})`
          : entry.framework
            ? `Usage limit reached (${entry.framework})`
            : "Usage limit reached",
        sentence: pauseMessage(entry, zone, now),
        scope: entry.connection
          ? `Connection ${entry.connectionName || pauseConnectionLabel(entry.connection)}`
          : "Deployment",
        impact: "Runs in flight on it were cancelled; queued work waits.",
        link: {
          label: entry.canLift
            ? "Resume in Fleet → Controls"
            : "Fleet → Controls",
          route: "/fleet",
          query: CONTROLS,
        },
      });
    });
  }

  const pipeline = c["pipelinePause"] as PipelinePause | undefined;
  if (pipeline?.paused) {
    out.push({
      id: "pipeline-pause",
      severity: "warning",
      title: pipelinePauseTitle(pipeline),
      sentence: pipelinePauseMessage(pipeline, zone),
      scope: "Deployment",
      since: pipeline.since || undefined,
      link: {
        label: "Resume in Fleet → Controls",
        route: "/fleet",
        query: CONTROLS,
      },
    });
  }

  const deployment = c["deployment"] as DeploymentRow | undefined;
  if (deployment && !deployment.archived) {
    const missing = deploymentSetupStepsLeft(deployment, config);
    if (missing.length > 0) {
      const blocking = missing.some(
        (s) => s.id === "git" || s.id === "connections",
      );
      out.push({
        id: "setup",
        severity: "info",
        title: `${missing.length} setup ${missing.length === 1 ? "step" : "steps"} left`,
        sentence: `Still to do: ${missing.map((s) => s.label.toLowerCase()).join(", ")}.`,
        scope: "Deployment",
        impact: blocking
          ? "Tasks wait until the deployment has a git host and a model connection."
          : undefined,
        link: { ...missing[0].link, label: "Finish setup" },
      });
    }
  }

  return sortAlerts(out);
}
