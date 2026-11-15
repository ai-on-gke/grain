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

// Wire contracts for pkg/ui/ci.go, ci_actions.go, cifault.go, quarantine.go,
// actionscache.go, and packagecache.go.

export interface CIStepRun {
  name: string;
  status?: string;
  conclusion?: string;
  exitCode?: number;
  output?: string;
  outputTruncated?: boolean;
  error?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface CIFailureSummary {
  kind?: string;
  summary?: string;
  failedTests?: string[];
  diagnostics?: { file?: string; line?: number; message?: string }[];
  [key: string]: unknown;
}

export interface CIRunArtifact {
  run?: string;
  job: string;
  name: string;
  stored?: boolean;
  bytes?: number;
  size?: number;
  contentType?: string;
  path?: string;
  collectedAt?: string;
  note?: string;
}

export interface CIJobAttempt {
  attempt: number;
  sandbox?: string;
  startedAt?: string;
  finishedAt?: string;
  conclusion?: string;
  error?: string;
  failureSummary?: CIFailureSummary;
  steps?: CIStepRun[];
  artifacts?: CIRunArtifact[];
  goTestLog?: string;
}

export interface CIJobRun {
  workflow?: string;
  workflowPath?: string;
  jobId: string;
  name: string;
  cached?: boolean;
  sandbox?: string;
  startedAt?: string;
  finishedAt?: string;
  conclusion?: string;
  status?: string;
  error?: string;
  activity?: string;
  activityAt?: string;
  queued?: number;
  queuedCause?: string;
  queuedReason?: string;
  artifacts?: CIRunArtifact[];
  failureSummary?: CIFailureSummary;
  retried?: boolean;
  retryCount?: number;
  retryReason?: string;
  warning?: string;
  flake?: boolean;
  quarantined?: boolean;
  quarantinedTests?: string[];
  steps?: CIStepRun[];
  attempts?: CIJobAttempt[];
  goTestLog?: string;
}

export interface CIActionsMeta {
  runId: number;
  runAttempt?: number;
  workflow?: string;
  workflowName?: string;
  headRepo?: string;
  status?: string;
  conclusion?: string;
  url?: string;
  failedJobs?: string[];
}

export interface CIFaultRun {
  runId: string;
  ref?: string;
  sha?: string;
  taskId?: string;
  commit?: string;
  at?: string;
}

export interface CIFaultFix {
  pullRequest: string;
  task?: string;
  title?: string;
  files: string[];
}

export interface CIFault {
  source?: string;
  repo?: string;
  jobId?: string;
  jobName?: string;
  step?: string;
  kind: string;
  digest?: string;
  refs?: string[];
  runs?: CIFaultRun[];
  paths?: string[];
  ownerTaskId?: string;
  ownerRef?: string;
  fixes?: CIFaultFix[];
  runCount?: number;
  refCount?: number;
  pathCount?: number;
  trimmed?: boolean;
  summary?: string;
  detail?: string;
}

export interface CIRun {
  id: string;
  source?: string;
  provider?: string;
  repo: string;
  ref?: string;
  branch?: string;
  commit?: string;
  event?: string;
  workflow?: string;
  taskId?: string;
  requestedBy?: string;
  url?: string;
  status?: string;
  conclusion?: string;
  detail?: string;
  error?: string;
  interrupted?: boolean;
  cancelReason?: string;
  cancelledBy?: string;
  recheck?: boolean;
  quarantined?: boolean;
  startedAt?: string;
  finishedAt?: string;
  actions?: CIActionsMeta;
  jobs?: CIJobRun[];
  jobCount?: number;
  jobsTruncated?: boolean;
  fault?: CIFault;
}

export interface CIRunsResponse {
  enabled: boolean;
  runs: CIRun[];
  matched: number;
  stored: number;
  faults?: CIFault[];
  actionsNote?: string;
  repoProvider?: string;
  truncated?: boolean;
  nextCursor?: string;
}

export interface CIFaultsResponse {
  enabled: boolean;
  faults?: CIFault[];
  total: number;
  truncated?: boolean;
  nextCursor?: string;
}

export interface CIRunArtifactsResponse {
  enabled: boolean;
  available: boolean;
  artifacts: CIRunArtifact[];
  total: number;
  truncated?: boolean;
  nextCursor?: string;
}

export interface CIRunLogResponse {
  run: string;
  job: string;
  jobId: number;
  url?: string;
  bytes: number;
  truncated?: boolean;
  log: string;
}

export interface CIWorkflowOption {
  path: string;
  name: string;
  events?: string[];
  jobs?: string[];
}

export interface CIWorkflowsResponse {
  workflows: CIWorkflowOption[];
  total: number;
}

export interface CIRunRequest {
  taskId?: string;
  repo?: string;
  ref?: string;
  branch?: string;
  commit?: string;
  event?: string;
  workflow?: string;
  jobs?: string[];
  recheck?: boolean;
  recheckFailed?: boolean;
}

export interface CICacheStats {
  dir?: string;
  entries: number;
  bytes: number;
  hits: number;
  misses: number;
  maxBytes: number;
  maxEntries: number;
}

export interface CICacheResponse {
  enabled: boolean;
  available: boolean;
  cache?: CICacheStats;
  removed?: number;
}

export interface ActionsCacheFlush {
  repo?: string;
  entries: number;
  bytes: number;
}

export interface PackageCacheFlush {
  kind?: string;
  entries: number;
  bytes: number;
}

export interface QuarantineTestRequest {
  repo?: string;
  branch?: string;
  testName: string;
  reason: string;
  ttl?: string;
  fileFollowUp?: boolean;
}

export interface QuarantineTestResponse {
  taskId?: string;
  followUpTaskId?: string;
  testName: string;
  reason: string;
  ttl: string;
  createdAt: string;
  expiresAt?: string;
  quarantineYaml: string;
}

export type CIBucket =
  "running" | "failed" | "errored" | "passed" | "cancelled";
export type CIFilterId = "all" | CIBucket;
export type CIProviderFilter = "all" | "grain" | "github";

export function cleanRef(ref: string | undefined): string {
  if (!ref) return "";
  return ref.replace(/^refs\/heads\//, "");
}

export function runBranch(run: CIRun): string {
  return cleanRef(run.branch || run.ref || "");
}

export function providerOfRun(run: CIRun): "grain" | "github" {
  const p = (run.provider || run.source || "").toLowerCase();
  if (p.includes("github") || run.id.startsWith("gha:")) return "github";
  return "grain";
}

export function runTaskId(run: CIRun): string | null {
  if (run.taskId) return run.taskId;
  if (run.requestedBy && /^\d+$/.test(run.requestedBy.trim())) {
    return run.requestedBy.trim();
  }
  const ref = cleanRef(run.ref || run.branch);
  const m = /^grain\/(?:[0-9a-f]+\/)?task-(\d+)$/i.exec(ref);
  return m ? m[1] : null;
}

export function runTitle(run: CIRun): string {
  if (run.actions?.workflowName) return run.actions.workflowName;
  if (run.workflow) return run.workflow;
  const firstWf = run.jobs?.find((j) => j.workflow)?.workflow;
  if (firstWf) return firstWf;
  if (run.event) return `${run.event} checks`;
  return "CI checks";
}

export function faultSourceOf(source: string | undefined): "grain" | "github" {
  const s = (source || "grain").trim().toLowerCase();
  if (s === "github" || s.startsWith("github")) return "github";
  return "grain";
}

export function faultsForRun(
  run: CIRun,
  faults: readonly CIFault[],
): CIFault[] {
  const matched: CIFault[] = [];
  if (run.fault) matched.push(run.fault);
  const runProvider = providerOfRun(run);
  for (const f of faults) {
    if (faultSourceOf(f.source) !== runProvider) continue;
    if (f.repo && run.repo && f.repo.toLowerCase() !== run.repo.toLowerCase()) {
      continue;
    }
    if (
      matched.some(
        (m) =>
          (m.digest && m.digest === f.digest) ||
          (!m.digest && !f.digest && m === f),
      )
    ) {
      continue;
    }
    if (f.runs?.some((r) => r.runId === run.id)) {
      matched.push(f);
    }
  }
  return matched;
}

export function isRunActive(run: CIRun): boolean {
  const st = (run.status || run.actions?.status || "").toLowerCase();
  if (st === "running" || st === "in_progress" || st === "queued") return true;
  if (!run.conclusion && !run.finishedAt) return true;
  const c = (run.conclusion || "").toLowerCase();
  return c === "running" || c === "queued" || c === "pending";
}

// Core Grain CI thesis: infrastructure / shared deployment faults ("errored")
// are strictly separated from code/test failures on a branch ("failed").
export function runBucket(run: CIRun, faults: readonly CIFault[]): CIBucket {
  if (isRunActive(run)) return "running";
  const c = (run.conclusion || "").toLowerCase();
  const hasFault = faultsForRun(run, faults).length > 0;
  if (c === "errored" || (c === "failure" && hasFault)) return "errored";
  if (c === "failure" || c === "failed" || c === "timed_out") return "failed";
  if (c === "success" || c === "passed" || c === "skipped" || c === "neutral") {
    return "passed";
  }
  return "cancelled";
}

export interface CIStatusBadge {
  bucket: CIBucket;
  label: string;
  tone: "setup" | "error" | "blast" | "healthy" | "neutral";
  iconKind: string;
}

export function runBadge(
  run: CIRun,
  faults: readonly CIFault[],
): CIStatusBadge {
  const b = runBucket(run, faults);
  switch (b) {
    case "running":
      return {
        bucket: b,
        label: "Running",
        tone: "setup",
        iconKind: "running",
      };
    case "errored":
      return {
        bucket: b,
        label:
          (run.conclusion || "").toLowerCase() === "errored"
            ? "Errored · Infra"
            : "Errored · Shared fault",
        tone: "blast",
        iconKind: "blast",
      };
    case "failed":
      return {
        bucket: b,
        label: "Failed · Code",
        tone: "error",
        iconKind: "failed",
      };
    case "passed":
      return {
        bucket: b,
        label: run.quarantined ? "Passed · Quarantined" : "Passed",
        tone: "healthy",
        iconKind: "passed",
      };
    case "cancelled":
      return {
        bucket: b,
        label: run.interrupted ? "Interrupted" : "Cancelled",
        tone: "neutral",
        iconKind: "neutral",
      };
  }
}

export function stepBadge(
  conclusion: string | undefined,
  startedAt?: string,
  finishedAt?: string,
): { label: string; tone: string; iconKind: string } {
  const c = (conclusion || "").toLowerCase();
  if (!c && startedAt && !finishedAt) {
    return { label: "Running", tone: "setup", iconKind: "running" };
  }
  if (c === "errored") {
    return { label: "Errored", tone: "blast", iconKind: "blast" };
  }
  if (c === "failure" || c === "failed" || c === "timed_out") {
    return { label: "Failed", tone: "error", iconKind: "failed" };
  }
  if (c === "success" || c === "passed") {
    return { label: "Passed", tone: "healthy", iconKind: "passed" };
  }
  if (c === "skipped") {
    return { label: "Skipped", tone: "neutral", iconKind: "neutral" };
  }
  if (c === "cancelled") {
    return { label: "Cancelled", tone: "neutral", iconKind: "neutral" };
  }
  return { label: c || "Queued", tone: "neutral", iconKind: "neutral" };
}

export function formatDuration(
  startedAt?: string,
  finishedAt?: string,
  nowMs = Date.now(),
): string {
  if (!startedAt) return "";
  const s = Date.parse(startedAt);
  if (Number.isNaN(s)) return "";
  const e = finishedAt ? Date.parse(finishedAt) : nowMs;
  if (Number.isNaN(e)) return "";
  const sec = Math.max(0, Math.round((e - s) / 1000));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const rem = sec % 60;
  if (m < 60) return rem ? `${m}m ${rem}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function formatBytes(bytes: number | undefined): string {
  if (!bytes || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

export function faultTitle(f: CIFault): string {
  if (f.summary) return f.summary;
  const parts = [f.jobName || f.jobId, f.step].filter(Boolean);
  if (parts.length) return parts.join(" › ");
  return f.detail || `Shared ${f.kind || "infrastructure"} fault`;
}

export function faultRunCount(f: CIFault): number {
  return f.runCount ?? f.runs?.length ?? 0;
}

export function safeExternalUrl(raw: string | undefined | null): string | null {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) return null;
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol === "http:" || parsed.protocol === "https:") {
      return trimmed;
    }
  } catch {
    return null;
  }
  return null;
}

export function faultRefCount(f: CIFault): number {
  return f.refCount ?? f.refs?.length ?? 0;
}

export function extractFailedTests(run: CIRun | null): string[] {
  if (!run?.jobs) return [];
  const found = new Set<string>();
  for (const job of run.jobs) {
    for (const t of job.failureSummary?.failedTests ?? []) {
      if (t) found.add(t);
    }
    for (const t of job.quarantinedTests ?? []) {
      if (t) found.add(t);
    }
    for (const step of job.steps ?? []) {
      if (!step.output) continue;
      for (const line of step.output.split("\n")) {
        const m = /^\s*---\s*FAIL:\s*(\S+)/.exec(line);
        if (m?.[1]) found.add(m[1]);
      }
    }
  }
  return [...found];
}
