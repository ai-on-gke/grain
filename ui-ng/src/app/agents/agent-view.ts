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
  name: string;
  cpus?: number;
  loadAverage?: string;
  memoryUsedMB?: number;
  memoryTotalMB?: number;
}

export interface Cpu {
  // Share of the sandbox's vCPUs in use (load average over vCPUs), 0..1+.
  share: number;
  load: number;
  cpus: number;
  memoryUsedMB?: number;
  bound: boolean;
}

export interface Agent {
  task: Task;
  detail: TaskDetail | null;
  attempt: Attempt | null;
  status: AgentStatus;
  quietMs: number;
  cpu: Cpu | null;
}

// running is what the screen shows: running tasks, not standing
// sessions (TaskList.jsx runningTasks).
export function running(tasks: readonly Task[]): Task[] {
  return tasks.filter(
    (t) => t.state === "running" && !t.consoleSession && !t.interactive,
  );
}

export function currentAttempt(d: TaskDetail | null): Attempt | null {
  const list = d?.attempts ?? [];
  if (!list.length) return null;
  return [...list].sort((a, b) => b.number - a.number)[0];
}

// lastActivity is when the run last said what it is doing, falling back
// to when the attempt started.
export function lastActivity(t: Task, a: Attempt | null): number {
  const at = Date.parse(t.activityAt ?? "");
  if (Number.isFinite(at)) return at;
  const start = Date.parse(a?.agentStartedAt ?? a?.startedAt ?? "");
  return Number.isFinite(start) ? start : Date.now();
}

// cpuOf reads a sandbox probe: the 1-minute load average over its vCPUs.
// A sandbox at its vCPU limit is CPU-bound.
export function cpuOf(r: SandboxReading | undefined): Cpu | null {
  if (!r?.cpus || !r.loadAverage) return null;
  const load = parseFloat(r.loadAverage.split(/[\s,]+/)[0]);
  if (!Number.isFinite(load)) return null;
  const share = load / r.cpus;
  return {
    share,
    load,
    cpus: r.cpus,
    memoryUsedMB: r.memoryUsedMB,
    bound: share >= 0.95,
  };
}

// sandboxFor finds the probe row for a task's sandbox. Sandboxes are
// named after their task (grain-task-<id> and similar); a backend that
// names them otherwise, or does not probe them, gives no reading.
export function sandboxFor(
  t: Task,
  readings: readonly SandboxReading[],
): SandboxReading | undefined {
  const re = new RegExp(`(^|[^0-9])task-?${t.id}([^0-9]|$)`);
  return readings.find((r) => re.test(r.name));
}

export function toAgent(
  t: Task,
  d: TaskDetail | null,
  readings: readonly SandboxReading[],
  now: number,
): Agent {
  const attempt = currentAttempt(d);
  const quietMs = now - lastActivity(t, attempt);
  const status: AgentStatus =
    quietMs >= QUIET_MS ? "quiet" : t.activityBySetup ? "setup" : "working";
  return {
    task: t,
    detail: d,
    attempt,
    status,
    quietMs,
    cpu: cpuOf(sandboxFor(t, readings)),
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

export type GroupBy = "none" | "repo" | "model" | "where";

export function groupKey(a: Agent, by: GroupBy): string {
  switch (by) {
    case "repo":
      return a.task.repo || "No repository";
    case "model":
      return a.attempt?.model || "Deployment default";
    case "where":
      return whereLabel(a);
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

// whereLabel is where the run executes: a grain-agentd host when the task
// is hosted on one, else grain's own sandboxes.
export function whereLabel(a: Agent): string {
  const h = (a.task as Task & { hosting?: string; hostd?: string }).hostd;
  return h ? `grain-agentd ${h}` : "Grain sandbox";
}

export function isAgentd(a: Agent): boolean {
  return !!(a.task as Task & { hostd?: string }).hostd;
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
  if (a.cpu.share >= 0.5) {
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

export function tokens(a: Attempt | null): number {
  return (a as (Attempt & { promptTokens?: number }) | null)?.promptTokens ?? 0;
}
