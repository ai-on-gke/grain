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

import type { Task } from "../api/models";

export type MetricsWindow = "24h" | "7d" | "30d";

export interface MetricsScope {
  repos?: string[];
  authors?: string[];
}

export interface MetricsBucket {
  since: string;
  until: string;
  filed: number;
  completed: number;
  runsFinished: number;
}

export interface MetricsThroughput {
  tasksFiled: number;
  tasksCompleted: number;
  tasksClosed: number;
  runsStarted: number;
  runsFinished: number;
  filedPerDay: number;
  completedPerDay: number;
  runsFinishedPerDay: number;
  buckets?: MetricsBucket[];
}

export interface MetricsDistribution {
  n: number;
  minSeconds: number;
  p50Seconds: number;
  p90Seconds: number;
  p99Seconds: number;
  maxSeconds: number;
  meanSeconds: number;
}

export interface MetricsStage extends MetricsDistribution {
  stage: string;
  label: string;
  description: string;
}

export interface MetricsRuns {
  outcomes?: Record<string, number>;
  endings?: Record<string, number>;
  execTunnelFailures: number;
  execTunnelToolCalls?: number;
  attemptsPerCompletion: number;
  meanConcurrent: number;
  maxConcurrent: number;
  ordinaryMaxConcurrent?: number;
  utilization: number;
  live: number;
}

export interface MetricsBacklog {
  byState?: Record<string, number>;
  queued: number;
  stoppedByOperator?: number;
  oldestQueuedSeconds: number;
  oldestQueuedTaskId?: string;
}

export interface MetricsToolUse {
  name: string;
  surface?: string;
  runs: number;
  calls: number;
  errored: number;
  errorRate: number;
  timedOut: number;
  timeoutRate: number;
}

export interface MetricsTools {
  runs: number;
  calls: number;
  errored: number;
  erroredShare: number;
  cut?: number;
  cutShare?: number;
  controlCalls?: number;
  cliCalls?: number;
  byTool?: MetricsToolUse[];
}

export interface MetricsChecks {
  waits: number;
  runs: number;
  verdicts?: Record<string, number>;
  blocked?: MetricsDistribution;
  greenRuns: number;
}

export interface MetricsCIQueueCause {
  cause: string;
  jobs: number;
  queuedSeconds: number;
  share: number;
  longestSeconds: number;
  reason?: string;
}

export interface MetricsCIQueueing {
  jobs: number;
  waited: number;
  neverRan?: number;
  cached: number;
  queuedSeconds: number;
  busySeconds: number;
  queuedShare: number;
  cacheHitShare: number;
  byCause?: MetricsCIQueueCause[];
}

export interface MetricsMergeBatches {
  batches: number;
  changes: number;
  fresh: number;
  firstTry: number;
  firstTryRate: number;
  passed: number;
  passRate: number;
  split: number;
  splitRate?: number;
  culprits?: number;
  abandoned?: number;
  meanSize?: number;
  saved: number;
}

export interface MetricsMergeBucket {
  since: string;
  until: string;
  landed: number;
  perHour: number;
}

export interface MetricsMerges {
  landed: number;
  landedByForge?: Record<string, number>;
  landedPerHour: number;
  landedPerDay: number;
  filedToLanded?: MetricsDistribution;
  completedToLanded?: MetricsDistribution;
  openedToLanded?: MetricsDistribution;
  batches?: MetricsMergeBatches;
  baseHolds?: { passes: number; open: number };
  buckets?: MetricsMergeBucket[];
}

export interface MetricsAgentReportCheck {
  wakes: number;
  claims: number;
  uncited: number;
  unread: number;
  mismatched: number;
  uncitedRate: number;
  unreadRate: number;
  mismatchRate: number;
}

export interface MetricsAgentTemplate {
  templateId: string;
  wakes: number;
  calls: number;
  bytes: number;
  callsP50: number;
  callsP95: number;
  report?: MetricsAgentReportCheck;
}

export interface MetricsAgents {
  templates?: MetricsAgentTemplate[];
  reportChecks?: MetricsAgentReportCheck;
}

export interface MetricsSectionFailure {
  section: string;
  error: string;
  ref?: string;
}

export interface MetricsReport {
  since: string;
  until: string;
  windowSeconds: number;
  scope?: MetricsScope;
  throughput?: MetricsThroughput;
  latency?: MetricsStage[];
  runs?: MetricsRuns;
  backlog?: MetricsBacklog;
  tools?: MetricsTools;
  checks?: MetricsChecks;
  ciQueueing?: MetricsCIQueueing;
  merges?: MetricsMerges;
  agents?: MetricsAgents;
  failed?: MetricsSectionFailure[];
}

export interface TokenBudget {
  connection: string;
  model?: string;
  tokens: number;
  window: string;
}

export interface UsageSettings {
  mode?: "advisory" | "auto" | "off" | string;
  minWorkers?: number;
  retentionDays?: number;
  budgets?: TokenBudget[];
}

export interface UsageScope {
  connection: string;
  connectionName?: string;
  model?: string;
  runs: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costUsd?: number;
  used: number;
  windowStart?: string;
  window: string;
  limit?: number;
  limitSource?: "configured" | "learned" | string;
  remaining?: number;
  resetAt?: string;
  pausedUntil?: string;
  burnPerHour: number;
  runsOutAt?: string;
  sustainableWorkers?: number;
}

export interface UsageTotals {
  runs: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costUsd?: number;
  billable: number;
  burnPerHour: number;
  since?: string;
}

export interface UsageResponse {
  generatedAt?: string;
  mode: string;
  maxWorkers: number;
  minWorkers: number;
  scopes: UsageScope[];
  rollup: UsageTotals;
  sustainableWorkers?: number;
  effectiveWorkers: number;
  advisories?: string[];
  warnings?: string[];
  settings: UsageSettings;
}

export interface RepoWork {
  repo: string;
  pullRequestsMerged: number;
  pullRequestsMeasured: number;
  linesAdded: number;
  linesDeleted: number;
  tasksCompleted: number;
  tasksFiled: number;
  runs: number;
}

export interface DirectionWork {
  taskId: string;
  title?: string;
  repo?: string;
  origin?: string;
  author?: string;
  authorKind?: string;
  pullRequestsMerged: number;
  linesAdded: number;
  linesDeleted: number;
  tasksCompleted: number;
  tasksFiled: number;
  tasksSpawned: number;
  runs: number;
  runsSucceeded: number;
  agentHours: number;
}

export interface ClusterStats {
  period: string;
  since: string;
  until: string;
  pullRequestsMerged: number;
  pullRequestsMeasured: number;
  linesAdded: number;
  linesDeleted: number;
  tasksCompleted: number;
  tasksFiled: number;
  runs: number;
  runsSucceeded: number;
  agentHours: number;
  repos?: RepoWork[];
  directions?: DirectionWork[];
}

export interface ReleaseNote {
  period: string;
  title?: string;
  text?: string;
  length: number;
  updatedAt?: string;
  updatedBy?: string;
  updatedByKind?: string;
}

export interface ThroughputPoint {
  index: number;
  x: number;
  width: number;
  completedY: number;
  completedH: number;
  filedY: number;
  filedH: number;
  mergeX: number;
  mergeY: number;
  completed: number;
  filed: number;
  landed: number;
  label: string;
}

export interface ThroughputChartGeometry {
  bars: ThroughputPoint[];
  mergeLine: string;
  mergeArea: string;
  maxVal: number;
}

export interface SpaceActorSummary {
  id: string;
  label: string;
  kind: string;
  repos: string[];
  workstreams: number;
  activeTasks: number;
}

export function formatDurationSec(sec: number | undefined | null): string {
  if (sec === undefined || sec === null || Number.isNaN(sec) || sec <= 0) {
    return "—";
  }
  if (sec < 60) return `${Math.round(sec)}s`;
  if (sec < 3600) {
    const m = sec / 60;
    return m >= 10 ? `${Math.round(m)}m` : `${m.toFixed(1)}m`;
  }
  if (sec < 86400) {
    const h = sec / 3600;
    return h >= 10 ? `${Math.round(h)}h` : `${h.toFixed(1)}h`;
  }
  return `${(sec / 86400).toFixed(1)}d`;
}

export function formatCompactNum(n: number | undefined | null): string {
  const v = Number(n ?? 0);
  if (!Number.isFinite(v)) return "0";
  if (Math.abs(v) >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (Math.abs(v) >= 10_000) return `${Math.round(v / 1_000)}k`;
  if (Math.abs(v) >= 1_000) return `${(v / 1_000).toFixed(1)}k`;
  return String(Math.round(v));
}

export function formatPct(rate: number | undefined | null): string {
  if (rate === undefined || rate === null || !Number.isFinite(rate)) return "—";
  return `${(rate * 100).toFixed(1)}%`;
}

export function buildThroughputGeometry(
  buckets: readonly MetricsBucket[] | undefined,
  mergeBuckets: readonly MetricsMergeBucket[] | undefined,
  width = 520,
  height = 148,
): {
  bars: ThroughputPoint[];
  mergeLine: string;
  mergeArea: string;
  maxVal: number;
} {
  const list = buckets ?? [];
  const merges = mergeBuckets ?? [];
  const count = Math.max(list.length, merges.length);
  if (count === 0) {
    return { bars: [], mergeLine: "", mergeArea: "", maxVal: 0 };
  }
  let maxVal = 1;
  for (let i = 0; i < count; i++) {
    const b = list[i];
    const m = merges[i];
    maxVal = Math.max(maxVal, b?.completed ?? 0, b?.filed ?? 0, m?.landed ?? 0);
  }
  const padTop = 10;
  const padBottom = 18;
  const plotH = height - padTop - padBottom;
  const slotW = width / count;
  const barW = Math.max(4, Math.min(26, slotW * 0.62));

  const bars: ThroughputPoint[] = [];
  const linePts: string[] = [];

  for (let i = 0; i < count; i++) {
    const b = list[i];
    const m = merges[i];
    const completed = b?.completed ?? 0;
    const filed = b?.filed ?? 0;
    const landed = m?.landed ?? 0;
    const cx = slotW * i + slotW / 2;
    const x = cx - barW / 2;
    const completedH = (completed / maxVal) * plotH;
    const filedH = (filed / maxVal) * plotH;
    const mergeY = padTop + plotH - (landed / maxVal) * plotH;
    const sinceLabel = (b?.since || m?.since || "")
      .slice(5, 16)
      .replace("T", " ");
    bars.push({
      index: i,
      x: Number(x.toFixed(1)),
      width: Number(barW.toFixed(1)),
      completedY: Number((padTop + plotH - completedH).toFixed(1)),
      completedH: Number(completedH.toFixed(1)),
      filedY: Number((padTop + plotH - filedH).toFixed(1)),
      filedH: Number(filedH.toFixed(1)),
      mergeX: Number(cx.toFixed(1)),
      mergeY: Number(mergeY.toFixed(1)),
      completed,
      filed,
      landed,
      label: `${sinceLabel || "Slice " + (i + 1)}: ${completed} completed, ${filed} filed, ${landed} merged`,
    });
    linePts.push(`${cx.toFixed(1)},${mergeY.toFixed(1)}`);
  }

  const baselineY = (padTop + plotH).toFixed(1);
  const firstX = bars[0].mergeX.toFixed(1);
  const lastX = bars[bars.length - 1].mergeX.toFixed(1);
  const mergeLine = linePts.join(" ");
  const mergeArea =
    bars.length > 1
      ? `M ${firstX},${baselineY} L ${linePts.join(" L ")} L ${lastX},${baselineY} Z`
      : "";

  return { bars, mergeLine, mergeArea, maxVal };
}

// SPACE guardrail: team contributors are ALWAYS returned strictly in
// alphabetical order (A -> Z) and never ranked by output, lines, or PR count.
export function buildAlphabeticalContributors(
  stats: ClusterStats | null,
  tasks: readonly Task[],
): SpaceActorSummary[] {
  const byKey = new Map<
    string,
    {
      id: string;
      label: string;
      kind: string;
      repos: Set<string>;
      workstreams: number;
      activeTasks: number;
    }
  >();

  const touch = (id: string, label: string, kind: string) => {
    const clean = (id || label || "").trim();
    if (!clean) return null;
    const key = clean.toLowerCase();
    let row = byKey.get(key);
    if (!row) {
      row = {
        id: clean,
        label: (label || clean).trim(),
        kind: kind || "human",
        repos: new Set<string>(),
        workstreams: 0,
        activeTasks: 0,
      };
      byKey.set(key, row);
    }
    return row;
  };

  for (const d of stats?.directions ?? []) {
    if (!d.author) continue;
    const row = touch(d.author, d.author, d.authorKind || "human");
    if (row) {
      row.workstreams += 1;
      if (d.repo) row.repos.add(d.repo);
    }
  }

  for (const t of tasks) {
    const who = t.owner || t.author || t.creator || "";
    const label = t.authorLabel || who;
    const kind = t.authorKind || "human";
    const row = touch(who, label, kind);
    if (row) {
      if (t.state !== "closed" && !t.merged) {
        row.activeTasks += 1;
      }
      if (t.repo) row.repos.add(t.repo);
    }
  }

  return [...byKey.values()]
    .map((r) => ({
      id: r.id,
      label: r.label,
      kind: r.kind,
      repos: [...r.repos].sort((a, b) => a.localeCompare(b)),
      workstreams: r.workstreams,
      activeTasks: r.activeTasks,
    }))
    .sort((a, b) =>
      a.label.localeCompare(b.label, undefined, { sensitivity: "base" }),
    );
}
