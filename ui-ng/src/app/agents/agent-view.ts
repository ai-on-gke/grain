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

import type { Attempt, Task, TaskDetail } from "../api/models";

// A run with no activity update for this long is quiet (spec D-5): it is
// outlined, says so, and sorts first. This is the problem-finding half of
// the screen.
export const QUIET_MS = 10 * 60 * 1000;
// How far back the activity sparkline looks, and in how many buckets.
export const SPARK_WINDOW_MS = 30 * 60 * 1000;
export const SPARK_BUCKETS = 12;

export type AgentStatus = "working" | "setup" | "quiet";

export interface SandboxReading {
  // The run's own id (dispatch.RunID, "<task>-<attempt>"), which every
  // backend reports and which is what joins a row onto its run.
  sandbox?: string;
  name: string;
  kind?: string;
  backend?: string;
  ready?: boolean;
  terminationReason?: string;
  cpus?: number;
  loadAverage?: string;
  // Host-directory sandboxes: CPUs the run's own processes kept busy
  // over the last cpuWindowMS (orchestrator.SandboxHealth.CPUCores).
  cpuCores?: number;
  cpuWindowMS?: number;
  memoryUsedMB?: number;
  memoryTotalMB?: number;
}

export interface Cpu {
  // Share of the vCPUs in use, 0..1+: the sandbox's load average over
  // its own vCPUs, or a host run's busy cores over the host's vCPUs.
  share: number;
  // Load average (probe) or busy cores (process).
  load: number;
  cpus: number;
  memoryUsedMB?: number;
  bound: boolean;
  // "probe": the sandbox's own /proc/loadavg. "process": the run's own
  // processes on a host shared with everything else, so `cpus` is the
  // host's and is shared with every other host run.
  source: "probe" | "process";
  windowMs?: number;
}

export interface Agent {
  task: Task;
  detail: TaskDetail | null;
  attempt: Attempt | null;
  status: AgentStatus;
  quietMs: number;
  cpu: Cpu | null;
  // Why there is no CPU reading, when there is none.
  cpuNote: string;
}

// running is what the screen shows: running tasks, not standing
// console sessions -- the classic runningTasks (TaskList.jsx) and the
// set a running_only broadcast reaches (pkg/ui/teams.go).
export function running(tasks: readonly Task[]): Task[] {
  return tasks.filter((t) => t.state === "running" && !t.consoleSession);
}

export function currentAttempt(d: TaskDetail | null): Attempt | null {
  const list = d?.attempts ?? [];
  if (!list.length) return null;
  return [...list].sort((a, b) => b.number - a.number)[0];
}

// lastActivity is when the run last said what it is doing, falling back
// to when the attempt started, then to the task's last update, so a run
// with nothing to go on can still turn quiet.
export function lastActivity(t: Task, a: Attempt | null, now: number): number {
  for (const v of [
    t.activityAt,
    a?.agentStartedAt,
    a?.startedAt,
    t.updatedAt,
    t.createdAt,
  ]) {
    const at = Date.parse(v ?? "");
    if (Number.isFinite(at)) return at;
  }
  return now;
}

// cpuOf reads a sandbox row: a host run's busy cores over the host's
// vCPUs when the backend measured its processes, otherwise the 1-minute
// load average over the sandbox's own vCPUs. A sandbox at its vCPU
// limit is CPU-bound.
export function cpuOf(
  r: SandboxReading | undefined,
  hostCpus?: number,
): Cpu | null {
  if (!r) return null;
  if (typeof r.cpuCores === "number" && Number.isFinite(r.cpuCores)) {
    const cpus = r.cpus || hostCpus;
    if (!cpus) return null;
    const share = r.cpuCores / cpus;
    return {
      share,
      load: r.cpuCores,
      cpus,
      memoryUsedMB: r.memoryUsedMB,
      bound: share >= 0.95,
      source: "process",
      windowMs: r.cpuWindowMS,
    };
  }
  if (!r.cpus || !r.loadAverage) return null;
  const load = parseFloat(r.loadAverage.split(/[\s,]+/)[0]);
  if (!Number.isFinite(load)) return null;
  const share = load / r.cpus;
  return {
    share,
    load,
    cpus: r.cpus,
    memoryUsedMB: r.memoryUsedMB,
    bound: share >= 0.95,
    source: "probe",
  };
}

// cpuNote says why a run has no CPU reading, so the card can say it
// rather than print a bare dash.
export function cpuNote(r: SandboxReading | undefined): string {
  if (!r) {
    return "Not sampled: this deployment reported no sandbox for this run yet.";
  }
  if (r.backend === "host") {
    return "Not sampled: none of this run's processes were found in its directory, or this host has no /proc to read them from.";
  }
  const where = r.backend ? `The ${r.backend} sandbox` : "This run's sandbox";
  return `Not sampled: ${where} did not report its load on the last probe.`;
}

function live(r: SandboxReading): boolean {
  return (
    r.ready !== false &&
    (!r.terminationReason || r.terminationReason === "running") &&
    !/(^|[^a-z])ci([^a-z]|$)/i.test(`${r.kind ?? ""} ${r.name}`)
  );
}

// sandboxFor finds the row for a task's agent sandbox. Rows carry the
// run id ("<task>-<attempt>"): the current attempt's row when the
// attempt is known, else the task's newest live one. Rows without it
// fall back to the name heuristic (grain-task-<id> and similar). CI
// sandboxes and rows that are no longer running are skipped.
export function sandboxFor(
  t: Task,
  readings: readonly SandboxReading[],
  attempt?: Attempt | null,
): SandboxReading | undefined {
  const liveRows = readings.filter(live);
  if (attempt) {
    const exact = liveRows.find(
      (r) => r.sandbox === `${t.id}-${attempt.number}`,
    );
    if (exact) return exact;
  }
  let best: SandboxReading | undefined;
  let bestN = -1;
  for (const r of liveRows) {
    if (!r.sandbox?.startsWith(`${t.id}-`)) continue;
    const n = Number(r.sandbox.slice(t.id.length + 1));
    if (Number.isInteger(n) && n > bestN) {
      best = r;
      bestN = n;
    }
  }
  if (best) return best;
  const re = new RegExp(`(^|[^0-9])task-?${t.id}([^0-9]|$)`);
  return liveRows.find((r) => re.test(r.name));
}

export function toAgent(
  t: Task,
  d: TaskDetail | null,
  readings: readonly SandboxReading[],
  now: number,
  hostCpus?: number,
): Agent {
  const attempt = currentAttempt(d);
  const row = sandboxFor(t, readings, attempt);
  const cpu = cpuOf(row, hostCpus);
  const quietMs = now - lastActivity(t, attempt, now);
  const status: AgentStatus =
    quietMs >= QUIET_MS ? "quiet" : t.activityBySetup ? "setup" : "working";
  return {
    task: t,
    detail: d,
    attempt,
    status,
    quietMs,
    cpu,
    cpuNote: cpu ? "" : cpuNote(row),
  };
}

// sortAgents puts quiet runs first (longest quiet first), then the rest
// by how long they have run.
export function sortAgents(list: readonly Agent[]): Agent[] {
  return [...list].sort((a, b) => {
    const qa = a.status === "quiet" ? 1 : 0;
    const qb = b.status === "quiet" ? 1 : 0;
    if (qa !== qb) return qb - qa;
    if (qa) return b.quietMs - a.quietMs;
    return startOf(a) - startOf(b);
  });
}

function startOf(a: Agent): number {
  const s = Date.parse(a.attempt?.startedAt ?? a.task.createdAt ?? "");
  return Number.isFinite(s) ? s : 0;
}

export type GroupBy = "none" | "repo" | "model" | "host";

export function groupKey(a: Agent, by: GroupBy): string {
  switch (by) {
    case "repo":
      return a.task.repo || "No repository";
    case "model":
      return a.attempt?.model || "Deployment default";
    case "host":
      return hostWork(a) || "No host work";
    default:
      return "";
  }
}

export function groupAgents(
  list: readonly Agent[],
  by: GroupBy,
): { key: string; agents: Agent[] }[] {
  if (by === "none") return [{ key: "", agents: [...list] }];
  const map = new Map<string, Agent[]>();
  for (const a of list) {
    const k = groupKey(a, by);
    map.set(k, [...(map.get(k) ?? []), a]);
  }
  return [...map.entries()]
    .sort((x, y) => x[0].localeCompare(y[0]))
    .map(([key, agents]) => ({ key, agents }));
}

// hostWork names the grain hostd the current attempt's host work went to
// (Attempt.hostd), "" when it resolved none. It says where host work ran,
// not where the agent itself runs.
export function hostWork(a: Agent): string {
  const h = a.attempt?.hostd;
  if (!h?.hostd) return "";
  return `Host work on ${h.hostd}${h.deleted ? " (deleted)" : ""}`;
}

// spark turns activity update times into bucket counts over the window,
// oldest first.
export function spark(times: readonly number[], now: number): number[] {
  const out = new Array<number>(SPARK_BUCKETS).fill(0);
  const size = SPARK_WINDOW_MS / SPARK_BUCKETS;
  for (const t of times) {
    const age = now - t;
    if (age < 0 || age >= SPARK_WINDOW_MS) continue;
    out[SPARK_BUCKETS - 1 - Math.floor(age / size)]++;
  }
  return out;
}

export function minutes(ms: number): string {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

// quietReason reads the CPU for a quiet card: busy is probably building
// or testing; idle is likely stuck.
export function quietReason(a: Agent): { text: string; stuck: boolean } {
  const base = `No update for ${minutes(a.quietMs)}`;
  if (!a.cpu) return { text: base, stuck: false };
  // A host run's share is of the whole host, so "busy" there is half a
  // core of its own rather than half the machine.
  const busy =
    a.cpu.source === "process" ? a.cpu.load >= 0.5 : a.cpu.share >= 0.5;
  if (busy) {
    return {
      text: `${base}; CPU busy, probably building or testing`,
      stuck: false,
    };
  }
  return { text: `${base}; CPU idle, likely stuck, worth a look`, stuck: true };
}

export function cpuLevel(c: Cpu): "low" | "mid" | "high" {
  if (c.share >= 0.9) return "high";
  if (c.share >= 0.6) return "mid";
  return "low";
}

// tokens is the attempt's prompt size estimate (Attempt.promptTokens):
// how big the prompt it was handed came out, not what it has spent.
export function tokens(a: Attempt | null): number {
  return a?.promptTokens ?? 0;
}
