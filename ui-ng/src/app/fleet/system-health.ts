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

// What Fleet → Health reads and how it words it: the classic System →
// Health tab's helpers (ui/src/alerts.js, ui/src/settingsIndex.js,
// ui/src/components/system/*), ported for /next/. Pure functions, so the
// component stays a template and the wording is unit-tested here.

import type { SettingsSectionId } from "../settings/settings-sections";

export type HealthSeverity = "critical" | "warning" | "info";

// HealthLink is where an alert's button goes: a /next/ path and, for
// Fleet, the ?tab= and #fragment that show it.
export interface HealthLink {
  label: string;
  path: string;
  query?: Record<string, string>;
  fragment?: string;
}

export interface HealthAlert {
  id: string;
  severity: HealthSeverity;
  title: string;
  sentence: string;
  scope: string;
  impact?: string;
  links: HealthLink[];
}

type Cfg = Record<string, unknown>;

const POWER_LINK: HealthLink = {
  label: "Power",
  path: "/fleet",
  query: { tab: "controls" },
  fragment: "fleet-power",
};
const CONFIG_REPO_LINK: HealthLink = {
  label: "Config repo",
  path: "/settings/config-repo",
};
const CONTROLS_LINK: HealthLink = {
  label: "Pauses & Controls",
  path: "/fleet",
  query: { tab: "controls" },
};

function obj(v: unknown): Cfg | null {
  return v && typeof v === "object" ? (v as Cfg) : null;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

// sentence ends s with a full stop unless it already ends a sentence.
function sentence(s: string): string {
  const t = s.trim();
  return t && !/[.!?]$/.test(t) ? `${t}.` : t;
}

// moment is an RFC 3339 time as a date and clock time in timeZone (or the
// reader's locale when unset), or the text as sent when it does not parse.
export function moment(at: string, timeZone?: string): string {
  const t = Date.parse(at);
  if (!at || !Number.isFinite(t)) return at;
  try {
    return new Date(t).toLocaleString(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      ...(timeZone ? { timeZone } : {}),
    });
  } catch {
    return new Date(t).toLocaleString(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    });
  }
}

function capitalise(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

function pauseConnectionLabel(key: string): string {
  const at = key.indexOf("@hostd/");
  if (at < 0) return key;
  return `${key.slice(0, at)} on host ${key.slice(at + "@hostd/".length)}`;
}

// pauseSentence is one usage-limit pause in words (classic pauseMessage,
// with its instant in the deployment's or reader's timezone).
function pauseSentence(entry: Cfg, timeZone?: string): string {
  const until = moment(str(entry["until"]), timeZone);
  const when = until || "the provider's window resets";
  const reason = entry["reason"] ? ` — ${str(entry["reason"])}` : "";
  const connection = str(entry["connection"]);
  const name = str(entry["connectionName"]) || pauseConnectionLabel(connection);
  const model = str(entry["model"]);
  const framework = str(entry["framework"]);
  const escalated = str(entry["escalated"]);
  if (connection && model)
    return `${model} on ${name} paused until ${when} — usage limit; the connection's other models keep dispatching.${reason}`;
  const subject = connection
    ? `connection ${name}`
    : framework
      ? `this deployment's ${framework} runs`
      : "";
  if (connection && escalated)
    return `Agent usage limit reached: nothing is being dispatched onto ${subject}, on any model, until ${when} (${escalated}).${reason}`;
  const what = subject
    ? `nothing is being dispatched onto ${subject}`
    : "nothing is being dispatched";
  return `Agent usage limit reached: ${what} until ${when}.${reason}`;
}

const RANK: Record<HealthSeverity, number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

// collectHealthAlerts is the standing warnings /api/config describes, the
// rows the classic alerts bell lists (ui/src/alerts.js collectAlerts),
// worst first. Each carries the link its classic row carried; a Resume
// button there is a link to Pauses & Controls here, where resuming is.
export function collectHealthAlerts(
  config: Cfg | null,
  timeZone?: string,
): HealthAlert[] {
  if (!config) return [];
  const out: HealthAlert[] = [];

  if (config["reconcilerDown"]) {
    const conditions = (
      Array.isArray(config["dispatchBlocked"]) ? config["dispatchBlocked"] : []
    ) as Cfg[];
    const why = conditions
      .map((c) => `${str(c["name"]).replaceAll("-", " ")}: ${str(c["error"])}`)
      .join("; ");
    const remedy = str(conditions.find((c) => c["remedy"])?.["remedy"]);
    out.push({
      id: "reconciler-down",
      severity: "critical",
      title: "Reconcile loop stopped",
      sentence:
        "This deployment's reconcile loop has stopped -- no tasks are being dispatched or checked." +
        (why ? ` ${why}.` : "") +
        ` ${remedy ? capitalise(remedy) : "Restart the daemon to recover"}.`,
      scope: "Deployment",
      impact: "No task is dispatched or checked until the daemon restarts.",
      links: [POWER_LINK],
    });
  }

  const storeSwitch = obj(config["storeSwitch"]);
  if (storeSwitch) {
    out.push({
      id: "store-switched",
      severity: "critical",
      title: "Store switched while grain was running",
      sentence: sentence(str(storeSwitch["message"])),
      scope: "Deployment",
      impact: "No task is dispatched until the daemon restarts.",
      links: [POWER_LINK],
    });
  }

  const sync = obj(config["stateRepoSyncFailure"]);
  if (sync) {
    const external = Boolean(sync["external"]);
    const repo = str(sync["repo"]);
    const since = moment(str(sync["since"]), timeZone) || "an unknown time";
    out.push({
      id: "state-sync-failing",
      severity: "critical",
      title: external
        ? "Configuration is not being pulled"
        : "Settings are not being exported",
      sentence: sentence(
        external
          ? `Configuration has not been pulled from ${repo || "the state repository"} since ${since}: ${str(sync["error"])}`
          : `Settings have not been exported to ${repo || "the state repository"} since ${since}: ${str(sync["error"])}`,
      ),
      scope: "Deployment",
      impact: external
        ? `Changes merged in ${repo || "the config repository"} since then are not applied here until the sync is fixed.`
        : "Changes made in the UI since then exist only in this deployment's database until the sync is fixed.",
      links: [CONFIG_REPO_LINK],
    });
  }

  if (config["hostSandboxes"]) {
    out.push({
      id: "host-sandboxes",
      severity: "warning",
      title: "No isolation: runs execute on this host",
      sentence:
        "Every dispatched run executes in a plain directory on the machine this daemon runs on, as its user, with that machine's filesystem, network and credentials.",
      scope: "Deployment",
      impact:
        "Restart the daemon with -container-sandboxes or -granule-sandboxes to isolate each run.",
      links: [],
    });
  }

  const agentPause = obj(config["agentPause"]);
  if (agentPause?.["paused"]) {
    const list = Array.isArray(agentPause["pauses"])
      ? (agentPause["pauses"] as Cfg[])
      : [];
    const entries = list.length ? list : [agentPause];
    entries.forEach((entry, i) => {
      const connection = str(entry["connection"]);
      const model = str(entry["model"]);
      const framework = str(entry["framework"]);
      const links: HealthLink[] = [CONTROLS_LINK];
      if (connection)
        links.push({ label: "Open connection", path: "/settings/models" });
      out.push({
        id: `agent-pause/${str(entry["team"])}/${connection ? connection + (model ? `/${model}` : "") : framework || i}`,
        severity: "warning",
        title: model
          ? `Usage limit reached (${model})`
          : framework
            ? `Usage limit reached (${framework})`
            : "Usage limit reached",
        sentence: pauseSentence(entry, timeZone),
        scope: connection
          ? `Connection ${str(entry["connectionName"]) || pauseConnectionLabel(connection)}`
          : "Deployment",
        impact: "Runs in flight on it were cancelled; queued work waits.",
        links,
      });
    });
  }

  const pipeline = obj(config["pipelinePause"]);
  if (pipeline?.["paused"]) {
    const since = moment(str(pipeline["since"]), timeZone);
    const reason = str(pipeline["reason"]);
    const untilTask = pipeline["untilTask"];
    out.push({
      id: "pipeline-pause",
      severity: "warning",
      title: pipeline["runNowPasses"]
        ? "Paused — only tasks you Run now will start"
        : "Deployment paused",
      sentence:
        `No new tasks are being picked up${since ? ` since ${since}` : ""}: this deployment is paused. ` +
        "Runs already in flight are left to finish." +
        (untilTask
          ? ` It lifts itself when task ${String(untilTask)} finishes.`
          : "") +
        (reason ? ` — ${reason}` : ""),
      scope: "Deployment",
      links: [CONTROLS_LINK],
    });
  }

  return out
    .map((a, i) => ({ a, i }))
    .sort((x, y) => RANK[x.a.severity] - RANK[y.a.severity] || x.i - y.i)
    .map((x) => x.a);
}

// SETTING_SECTIONS is the Settings section each field lives in, for the
// links Health's "saved, not applied" and "not runnable" notes carry
// (classic ui/src/settingsIndex.js, field entries only).
export const SETTING_SECTIONS: Readonly<Record<string, SettingsSectionId>> = {
  environmentName: "general",
  showClosedByDefault: "general",
  settingsLocked: "general",
  upgradeCheckMode: "general",
  upgradeCheckPausesPipeline: "general",
  autoUpgradeOn: "general",
  autoUpgradeChannel: "general",
  autoUpgradeTime: "general",
  autoUpgradeMinIntervalHours: "general",
  maxAgentTurns: "agents",
  promptExtension: "agents",
  taskBranchTemplate: "agents",
  deploymentId: "agents",
  externalAgentApi: "agents",
  pollInterval: "engine",
  maxWorkers: "engine",
  maxMergesPerRepo: "engine",
  ciJobsPerRun: "ci",
  ciAgeBoost: "ci",
  ciReserveAfter: "ci",
  ciRunHistory: "ci",
  ciStepOutputMb: "ci",
  ciStepOutputDays: "ci",
  ciRunOutputKb: "ci",
  ciCacheMb: "ci",
  ciCacheEntries: "ci",
  ciCacheDays: "ci",
  ciArtifactMb: "ci",
  ciArtifactDays: "ci",
  ciArtifactRunMb: "ci",
  ciArtifactFileMb: "ci",
  sandboxBackend: "sandboxes",
  sandboxCpus: "sandboxes",
  sandboxMemoryMb: "sandboxes",
  sandboxDiskGb: "sandboxes",
  networkAccess: "sandboxes",
  sessionSandboxIdleTtl: "sandboxes",
  sandboxOvercommit: "sandboxes",
  sandboxCpuOvercommit: "sandboxes",
  sandboxMemoryOvercommit: "sandboxes",
  sandboxDiskOvercommit: "sandboxes",
  sandboxOvercommitCpuMin: "sandboxes",
  sandboxOvercommitCpuMax: "sandboxes",
  sandboxOvercommitMemoryMin: "sandboxes",
  sandboxOvercommitMemoryMax: "sandboxes",
  sandboxOvercommitCpuPressureTarget: "sandboxes",
  sandboxOvercommitMemoryPressureTarget: "sandboxes",
  sandboxMemoryReserveMb: "sandboxes",
  sandboxDiskFloorShare: "sandboxes",
  sandboxDiskFloorMinMb: "sandboxes",
  sandboxImage: "sandboxes",
  k8sNamespace: "sandboxes",
  k8sSandboxImage: "sandboxes",
  k8sGranuleImage: "sandboxes",
  k8sWorkspace: "sandboxes",
  k8sGitProxyHost: "sandboxes",
  k8sSandboxService: "sandboxes",
  k8sNodeSelector: "sandboxes",
  k8sServiceAccount: "sandboxes",
  k8sPriorityClassInteractive: "sandboxes",
  k8sPriorityClassAgent: "sandboxes",
  k8sPriorityClassCi: "sandboxes",
  k8sDeployment: "sandboxes",
  k8sContainer: "sandboxes",
  k8sKubectl: "sandboxes",
  k8sPrivileged: "sandboxes",
  k8sNetworkPolicy: "sandboxes",
  packageCacheEnabled: "caches",
  packageCacheSizeGb: "caches",
  actionsCacheEnabled: "caches",
  actionsCacheSizeGb: "caches",
  githubHost: "capabilities",
  githubInsecureHttp: "capabilities",
  gcpProject: "capabilities",
  gcpServiceAccountEmail: "capabilities",
  gcpServiceAccountCount: "capabilities",
  hub: "registry",
  registryUrl: "registry",
  registryEntry: "registry",
  registryAuthMode: "registry",
  registryHostKind: "registry",
  registryOidcAudience: "registry",
  registryDisplayName: "registry",
  registryPurpose: "registry",
  registryListed: "registry",
  defaultModel: "models",
  modelSettings: "models",
  agentBackends: "models",
  approvedByDefault: "tasks",
  autoMergeByDefault: "tasks",
  defaultCapabilities: "tasks",
  newestFirst: "tasks",
  maxAutoSubmitDepth: "tasks",
  maxAutoSubmitFanOut: "tasks",
  qaEnabled: "tasks",
  qaFlowId: "tasks",
  qaRolloutPct: "tasks",
  qaShadowOnControl: "tasks",
  qaEpoch: "tasks",
  qaRegimeId: "tasks",
};

// settingHref is a field's place in Settings, /settings/<section>#<id>,
// as the classic Health tab links it; null for a field Settings does not
// show.
export function settingHref(
  field: string,
): { path: string; fragment: string } | null {
  const section = SETTING_SECTIONS[field];
  return section ? { path: `/settings/${section}`, fragment: field } : null;
}

// formatSeconds is a second count as a duration: "21m 30s", "3h 4m".
export function formatSeconds(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  const s = Math.round(value);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400)
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

// secondsUntil is how far ahead an RFC 3339 time is, or null.
export function secondsUntil(at: unknown, now: number): number | null {
  if (typeof at !== "string" || !at || at.startsWith("0001-")) return null;
  const s = (Date.parse(at) - now) / 1000;
  return Number.isFinite(s) ? Math.max(0, s) : null;
}

export interface GitHubBudgetView {
  severity: HealthSeverity;
  summary: string;
  runsOut: string | null;
  polls: string | null;
  low: string | null;
  callers: {
    caller: string;
    spent: number;
    revalidated: number;
    mostly: string;
  }[];
}

// githubBudgetView is the GitHub API budget panel's text, from GET
// /api/metrics?section=github's github object; null until a GitHub
// response has reported a budget (classic GitHubBudgetView).
export function githubBudgetView(
  github: Cfg | null,
  now = Date.now(),
): GitHubBudgetView | null {
  const budget = obj(github?.["budget"]);
  if (!github?.["enabled"] || !budget?.["limit"]) return null;
  const num = (v: unknown) => (typeof v === "number" ? v : 0);
  const resets = secondsUntil(budget["resetsAt"], now);
  const runsOut = secondsUntil(budget["exhaustedAt"], now);
  const parts = [
    `${num(budget["remaining"]).toLocaleString()} of ${num(budget["limit"]).toLocaleString()} calls left this hour`,
  ];
  if (resets != null) parts.push(`resets in ${formatSeconds(resets)}`);
  if (num(budget["spendPerHour"]) > 0)
    parts.push(
      `spending ${Math.round(num(budget["spendPerHour"])).toLocaleString()}/h`,
    );
  const gp = obj(github["polls"]);
  const polls = gp?.["tracked"] ? gp : obj(budget["polls"]);
  let pollText: string | null = null;
  if (polls && num(polls["tracked"]) > 0) {
    const tracked = num(polls["tracked"]);
    pollText =
      `Idle-poll backoff: ${num(polls["backedOff"]).toLocaleString()} of ${tracked.toLocaleString()} tracked ${tracked === 1 ? "PR" : "PRs"} backed off · ` +
      `poll interval median ${formatSeconds(num(polls["medianSeconds"]))}, max ${formatSeconds(num(polls["maxSeconds"]))}` +
      (polls["baseSeconds"] && polls["capSeconds"]
        ? ` (${formatSeconds(num(polls["baseSeconds"]))} base, ${formatSeconds(num(polls["capSeconds"]))} cap)`
        : "") +
      ".";
  }
  const low = Boolean(budget["low"]);
  const callers = (
    Array.isArray(github["callers"]) ? (github["callers"] as Cfg[]) : []
  )
    .slice(0, 6)
    .map((c) => ({
      caller: str(c["caller"]),
      spent: num(c["spent"]),
      revalidated: num(c["revalidated"]),
      mostly: (Array.isArray(c["kinds"]) ? (c["kinds"] as Cfg[]) : [])
        .slice(0, 2)
        .map((k) => `${str(k["kind"])} (${num(k["spent"]).toLocaleString()})`)
        .join(", "),
    }));
  return {
    severity: low ? "critical" : runsOut != null ? "warning" : "info",
    summary: `GitHub API: ${parts.join(" · ")}.`,
    runsOut:
      runsOut != null
        ? `At that rate it runs out in ${formatSeconds(runsOut)}, before it resets.`
        : null,
    polls: pollText,
    low: low
      ? `Under ${Math.round((num(budget["lowFraction"]) || 0.15) * 100)}% left: polling of idle pull requests, stale branches and path triggers is cut back. The merge queue and base-branch CI are not.`
      : null,
    callers,
  };
}

export interface OvercommitView {
  mode: string;
  oomKills: number;
  factors: string;
  adjustments: string[];
}

// overcommitView is the live sandbox overcommit line, from /api/settings'
// sandboxCommitment.budget (and its overcommit); null when the daemon
// reports none (classic SandboxOvercommitStatus).
export function overcommitView(
  settings: Cfg | null,
  timeZone?: string,
): OvercommitView | null {
  const budget = obj(obj(settings?.["sandboxCommitment"])?.["budget"]);
  const oc = obj(budget?.["overcommit"]) || obj(settings?.["overcommit"]);
  const cpu = oc?.["cpuOvercommit"] ?? budget?.["cpuOvercommit"];
  const mem = oc?.["memoryOvercommit"] ?? budget?.["memoryOvercommit"];
  if (!oc && !budget?.["mode"] && cpu == null && mem == null) return null;
  const fixed = (v: unknown) => Number(v ?? 0).toFixed(2);
  const mode = str(oc?.["mode"]) || str(budget?.["mode"]) || "fixed";
  const parts = [
    `CPU ${cpu != null ? `${fixed(cpu)}×` : "—"}`,
    `memory ${mem != null ? `${fixed(mem)}×` : "—"}`,
  ];
  const last = obj(oc?.["last"]);
  let psi = "";
  if (last) {
    psi = ` · PSI: CPU ${Number(last["cpuSome"] ?? last["cpuPressure"] ?? 0).toFixed(1)}%, memory ${Number(last["memorySome"] ?? last["memoryPressure"] ?? 0).toFixed(1)}%`;
  }
  const adjustments = (
    Array.isArray(oc?.["adjustments"]) ? (oc["adjustments"] as Cfg[]) : []
  ).map(
    (a) =>
      `${a["dimension"] === "cpu" ? "CPU" : str(a["dimension"]) || "ratio"} ${fixed(a["from"])}× → ${fixed(a["to"])}×` +
      (a["reason"] ? ` — ${str(a["reason"])}` : "") +
      (a["at"] ? ` (${moment(str(a["at"]), timeZone)})` : ""),
  );
  return {
    mode,
    oomKills: Number(oc?.["oomKills"] ?? 0),
    factors: `Effective factors: ${parts.join(" · ")}${psi}`,
    adjustments,
  };
}

// POWER_ACTIONS are the host power controls Fleet offers beside Restart
// daemon, each gated on its /api/config flag (classic SystemPage's
// POWER_ACTIONS, less the restart Fleet already has).
export const POWER_ACTIONS = [
  {
    id: "reboot",
    label: "Reboot host",
    enabledKey: "rebootEnabled",
    route: "POST /api/host/reboot",
    hint: "Reboots the machine grain itself is running on.",
    title: "Reboot the host machine?",
    confirm:
      "Every task currently running is interrupted, and this UI will be unreachable until the machine comes back up.",
    requested:
      "Reboot requested. This UI is unreachable until the host machine comes back up.",
    failed: "Reboot failed",
  },
  {
    id: "shutdown",
    label: "Shut down host",
    enabledKey: "shutdownEnabled",
    route: "POST /api/host/shutdown",
    hint: "Powers the machine off and leaves it off. Nothing here can start it again -- that takes whatever runs the machine.",
    title: "Shut down the host machine?",
    confirm:
      "Every task currently running is interrupted, and grain stays down until somebody powers the machine back on by some other means.",
    requested:
      "Shutdown requested. grain stays down until somebody powers the machine back on.",
    failed: "Shutdown failed",
  },
] as const;

export type PowerAction = (typeof POWER_ACTIONS)[number];
