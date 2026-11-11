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

// ---------- Tabs (spec D-6, D-28) ----------

export type TabId =
  | "backlog"
  | "proposed"
  | "running"
  | "asked"
  | "merging"
  | "deferred"
  | "closed"
  | "all";

export interface TabDef {
  id: TabId;
  label: string;
  // The task states the tab lists (model.StateOf); closed comes from the
  // paged closed list, not the live one.
  states: readonly string[];
  tip: string;
  // After the divider: work set aside rather than in flight.
  setAside?: boolean;
}

export const TABS: readonly TabDef[] = [
  {
    id: "backlog",
    label: "Backlog",
    states: ["queued"],
    tip: "Approved work waiting for a worker, in the order Grain will run it.",
  },
  {
    id: "proposed",
    label: "Needs approval",
    states: ["proposed"],
    tip: "Proposals: nothing runs until somebody approves them.",
  },
  {
    id: "running",
    label: "Running",
    states: ["running"],
    tip: "An agent is working on these now.",
  },
  {
    id: "asked",
    label: "Asked",
    states: ["awaiting_reply", "awaiting_access", "awaiting_secret", "failed"],
    tip: "Stopped on a person: a question, an access or secret request, or a run that failed.",
  },
  {
    id: "merging",
    label: "Merging",
    states: ["awaiting_submit", "completed"],
    tip: "The run is done; the change is waiting to merge or to be submitted.",
  },
  {
    id: "deferred",
    label: "Deferred",
    states: ["deferred"],
    tip: "Put aside. Bring back returns a task to where it came from.",
    setAside: true,
  },
  {
    id: "closed",
    label: "Closed",
    states: ["closed"],
    tip: "Merged, finished or closed.",
    setAside: true,
  },
  {
    id: "all",
    label: "All",
    states: [],
    tip: "Every open task.",
    setAside: true,
  },
];

export function isTabId(v: unknown): v is TabId {
  return TABS.some((t) => t.id === v);
}

// Standing session tasks are conversations, not work in a list.
export function isWork(t: Task): boolean {
  return !t.consoleSession && !t.interactive;
}

export function inTab(t: Task, tab: TabId): boolean {
  if (tab === "all") return t.state !== "closed";
  return TABS.find((x) => x.id === tab)!.states.includes(t.state);
}

const STATE_LABELS: Record<string, string> = {
  proposed: "Proposed",
  queued: "Waiting",
  running: "Running",
  awaiting_reply: "Asked",
  awaiting_access: "Needs access",
  awaiting_secret: "Needs a secret",
  failed: "Failed",
  awaiting_submit: "Ready to submit",
  completed: "Merging",
  deferred: "Deferred",
  closed: "Closed",
};

export function stateLabel(state: string): string {
  return STATE_LABELS[state] ?? state.replace(/_/g, " ");
}

// ---------- Priority (named levels) ----------

export interface Level {
  name: string;
  value: number; // what Set priority writes
  arrow: "up" | "flat" | "down";
}

// Grain's number reads backwards for anyone used to "P0 is highest", so
// it shows as a named level with its number and an arrow, never colour
// alone: Urgent 9, High 7-8, Normal 5-6 (5 is the default), Low 3-4,
// Lowest 1-2.
export const LEVELS: readonly Level[] = [
  { name: "Urgent", value: 9, arrow: "up" },
  { name: "High", value: 8, arrow: "up" },
  { name: "Normal", value: 5, arrow: "flat" },
  { name: "Low", value: 3, arrow: "down" },
  { name: "Lowest", value: 1, arrow: "down" },
];

export function priorityOf(t: Task): number {
  return typeof t.priority === "number" && t.priority > 0 ? t.priority : 5;
}

export function levelOf(p: number): Level {
  if (p >= 9) return LEVELS[0];
  if (p >= 7) return LEVELS[1];
  if (p >= 5) return LEVELS[2];
  if (p >= 3) return LEVELS[3];
  return LEVELS[4];
}

// ---------- Run order (D-28) ----------

// Admission bypass or durable lets a task skip worker limits: it runs
// before every priority band (model.Admission).
export function jumpsQueue(t: Task): boolean {
  const a = (t as Task & { admission?: string }).admission;
  return a === "bypass" || a === "durable";
}

export interface Band {
  key: string; // "jump" or the priority number
  label: string;
  priority: number | null;
  tasks: Task[];
}

// bands groups the backlog (already in dispatch order: GET /api/tasks
// lists open tasks in backlog order) under headers in that same order.
// Order applies within a priority only, so each run of one priority is
// a band.
export function bands(backlog: readonly Task[]): Band[] {
  const out: Band[] = [];
  for (const t of backlog) {
    const jump = jumpsQueue(t);
    const p = priorityOf(t);
    const key = jump ? "jump" : String(p);
    let band = out[out.length - 1];
    if (!band || band.key !== key) {
      band = {
        key,
        label: jump ? "Jumps the queue" : `${levelOf(p).name} ${p}`,
        priority: jump ? null : p,
        tasks: [],
      };
      out.push(band);
    }
    band.tasks.push(t);
  }
  return out;
}

export type SortId = "run" | "priority" | "oldest" | "newest";

export function sorted(list: readonly Task[], sort: SortId): Task[] {
  const by = [...list];
  const at = (t: Task) => t.createdAt ?? "";
  switch (sort) {
    case "priority":
      return by.sort(
        (a, b) => priorityOf(b) - priorityOf(a) || at(a).localeCompare(at(b)),
      );
    case "oldest":
      return by.sort((a, b) => at(a).localeCompare(at(b)));
    case "newest":
      return by.sort((a, b) => at(b).localeCompare(at(a)));
    default:
      return by;
  }
}

export interface MovePlan {
  id: string;
  afterId: string;
  beforeId: string;
  // Set when the new neighbours are in another band: order applies only
  // within a priority, so the task takes that band's priority.
  priority: number | null;
  from: number; // 0-based position before the move
  to: number; // 0-based position after it
}

// planMove is what moving backlog[from] to position `to` asks the server
// for: its new neighbours (POST /api/tasks/reorder) and, when it lands
// among another priority, that priority (PATCH /api/tasks/{id}).
export function planMove(
  backlog: readonly Task[],
  from: number,
  to: number,
): MovePlan | null {
  if (from < 0 || from >= backlog.length) return null;
  const clamped = Math.max(0, Math.min(backlog.length - 1, to));
  if (clamped === from) return null;
  const t = backlog[from];
  const rest = backlog.filter((_, i) => i !== from);
  const above = rest[clamped - 1];
  const below = rest[clamped];
  let priority: number | null = null;
  if (!jumpsQueue(t)) {
    // The band it joins: the row above's, or the row below's at the top.
    const ref = [above, below].find((x) => x && !jumpsQueue(x));
    const target = ref ? priorityOf(ref) : priorityOf(t);
    if (target !== priorityOf(t)) priority = target;
  }
  return {
    id: t.id,
    afterId: above?.id ?? "",
    beforeId: below?.id ?? "",
    priority,
    from,
    to: clamped,
  };
}

// neighbours is where a task sits now, so a move can be undone exactly.
export function neighbours(
  backlog: readonly Task[],
  id: string,
): { afterId: string; beforeId: string } {
  const i = backlog.findIndex((t) => t.id === id);
  return {
    afterId: i > 0 ? backlog[i - 1].id : "",
    beforeId: i >= 0 && i < backlog.length - 1 ? backlog[i + 1].id : "",
  };
}

// placement is where a proposal would land if approved now: after every
// queued task that jumps the queue or has at least its priority.
export function placement(
  backlog: readonly Task[],
  priority: number,
): { position: number; of: number; band: string } {
  const ahead = backlog.filter(
    (t) => jumpsQueue(t) || priorityOf(t) >= priority,
  ).length;
  return {
    position: ahead + 1,
    of: backlog.length + 1,
    band: `${levelOf(priority).name} ${priority}`,
  };
}

// nextUp is the task a free worker takes next: the first backlog task
// not waiting on a dependency.
export function nextUp(backlog: readonly Task[]): Task | null {
  return backlog.find((t) => !t.blocked) ?? null;
}

// ago is a short relative time ("4m", "3h", "2d").
export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const ms = now - new Date(iso).getTime();
  if (!Number.isFinite(ms)) return "";
  const m = Math.max(0, Math.round(ms / 60000));
  if (m < 60) return m <= 0 ? "now" : `${m}m`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.round(h / 24)}d`;
}
