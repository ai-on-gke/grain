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

// Pure queue-head and priority helpers shared with the shell's NextUpComponent
// so TasksPage-only tab/board/move logic in task-view.ts stays out of the
// initial bundle.

export interface Level {
  name: string;
  value: number;
  arrow: "up" | "flat" | "down";
}

export const LEVELS: readonly Level[] = [
  { name: "Urgent", value: 9, arrow: "up" },
  { name: "High", value: 8, arrow: "up" },
  { name: "Normal", value: 5, arrow: "flat" },
  { name: "Low", value: 3, arrow: "down" },
  { name: "Lowest", value: 1, arrow: "down" },
];

export function isBlocked(t: Task): boolean {
  if (t.blocked) return true;
  return Object.values(t.dependsOnStates ?? {}).some((s) => s !== "closed");
}

export function isWork(t: Task): boolean {
  return !t.consoleSession && !t.interactive;
}

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

export function jumpsQueue(t: Task): boolean {
  const a = (t as Task & { admission?: string }).admission;
  return a === "bypass" || a === "durable";
}

export function nextUp(backlog: readonly Task[]): Task | null {
  const ready = backlog.filter((t) => !isBlocked(t));
  return ready.find(jumpsQueue) ?? ready[0] ?? null;
}
