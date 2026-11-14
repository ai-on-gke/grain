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
import {
  LEVELS,
  type Level,
  isBlocked,
  isWork,
  jumpsQueue,
  levelOf,
  nextUp,
  priorityOf,
} from "./task-queue-helpers";

export {
  LEVELS,
  type Level,
  isBlocked,
  isWork,
  jumpsQueue,
  levelOf,
  nextUp,
  priorityOf,
};

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
    states: ["awaiting_reply", "failed"],
    tip: "Stopped on a person: a question, or a run that failed.",
  },
  {
    id: "merging",
    label: "Submit & land",
    states: ["awaiting_submit", "completed"],
    tip: "The run is done. Changes waiting for a person to submit, and submitted changes the merge queue is landing.",
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

export function inTab(t: Task, tab: TabId): boolean {
  if (tab === "all") return t.state !== "closed";
  return TABS.find((x) => x.id === tab)!.states.includes(t.state);
}

const STATE_LABELS: Record<string, string> = {
  proposed: "Proposed",
  queued: "Waiting",
  running: "Running",
  awaiting_reply: "Asked",
  failed: "Failed",
  awaiting_submit: "Ready to submit",
  completed: "Merging",
  deferred: "Deferred",
  closed: "Closed",
};

export function stateLabel(state: string): string {
  return STATE_LABELS[state] ?? state.replace(/_/g, " ");
}

// ---------- Run order (D-28) ----------

export interface Band {
  key: string; // the priority number
  label: string;
  priority: number;
  tasks: Task[];
}

// bands groups the backlog (already in dispatch order: GET /api/tasks
// lists open tasks in backlog order) under one header per priority.
// Order applies within a priority only. A priority that shows up twice
// (it cannot in a fresh list) gets a distinct key so headers never clash.
export function bands(backlog: readonly Task[]): Band[] {
  const out: Band[] = [];
  const seen = new Map<number, number>();
  for (const t of backlog) {
    const p = priorityOf(t);
    let band = out[out.length - 1];
    if (!band || band.priority !== p) {
      const n = (seen.get(p) ?? 0) + 1;
      seen.set(p, n);
      band = {
        key: n === 1 ? String(p) : `${p}-${n}`,
        label: `${levelOf(p).name} ${p}`,
        priority: p,
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
  // The band it joins: the row above's, or the row below's at the top.
  const ref = above ?? below;
  const target = ref ? priorityOf(ref) : priorityOf(t);
  const priority = target !== priorityOf(t) ? target : null;
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

// placement is where a proposal would land if approved now. Approving
// keeps the task's place in the queue order (Store.Approve leaves its
// order key alone), and GET /api/tasks lists every open task in that
// order, so it lands after the queued tasks listed before it.
export function placement(
  work: readonly Task[],
  proposal: Task,
): { position: number; of: number; band: string } {
  let ahead = 0;
  let queued = 0;
  let seen = false;
  for (const t of work) {
    if (t.id === proposal.id) seen = true;
    if (t.state !== "queued") continue;
    queued++;
    if (!seen) ahead++;
  }
  const p = priorityOf(proposal);
  return {
    position: ahead + 1,
    of: queued + 1,
    band: `${levelOf(p).name} ${p}`,
  };
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
