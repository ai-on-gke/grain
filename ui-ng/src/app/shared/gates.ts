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

import type { MergeGate, Task, TaskDetail } from "../api/models";

// Merge gates (pkg/model merge_gates.go) read for a person deciding
// whether to submit. The approval gate is the one Submit itself
// satisfies, so it never counts against Submit and is left out of the
// n/n count.

export const APPROVAL_GATE = "approval";

export type GateRollup = "passed" | "running" | "failing" | "unknown";

export interface GateSummary {
  state: GateRollup;
  // True while the list placeholder ("waiting for checks") has not been
  // confirmed by GET /api/tasks/{id}.
  checking?: boolean;
  // Labels of the gates that are failing or have findings.
  failing: string[];
  // Labels of the gates still needed (other than approval).
  needed: string[];
  // Labels of the gates still running.
  running: string[];
  passed: number;
  total: number;
}

function satisfied(g: MergeGate): boolean {
  return g.state === "passed" || g.state === "overridden";
}

// GET /api/tasks builds mergeGates without obs.CI (pkg/ui/tasks.go taskFrom),
// so failed CI still arrives there as state "running" / "waiting for checks".
// Only GET /api/tasks/{id} passes the real CI observation.
export function isListCiPlaceholder(g: MergeGate): boolean {
  return (
    g.id === "ci" &&
    g.state === "running" &&
    (g.detail ?? "").trim().toLowerCase() === "waiting for checks"
  );
}

// withDetailGates merges real gates from GET /api/tasks/{id} onto a list
// task. hasDetail is true once a detail carrying mergeGates has arrived.
export function withDetailGates(
  t: Task,
  detail?: TaskDetail | null,
): { task: Task; hasDetail: boolean } {
  if (detail?.mergeGates) {
    return { task: { ...t, mergeGates: detail.mergeGates }, hasDetail: true };
  }
  return { task: t, hasDetail: false };
}

// gateSummary rolls a task's merge gates up into one state: failing if
// any gate is failing or has findings, running if any is running or
// still needed, passed when every gate is satisfied, unknown when the
// server has reported no gates for it yet (or listOnly sees the server's
// unverified "waiting for checks" CI placeholder).
export function gateSummary(t: Task, listOnly = false): GateSummary {
  const all = t.mergeGates ?? [];
  const gates = all.filter((g) => g.id !== APPROVAL_GATE);
  const unverifiedCi = listOnly && gates.some(isListCiPlaceholder);
  const failing = gates
    .filter((g) => g.state === "failing" || g.state === "findings")
    .map((g) => g.label || g.id);
  const needed = gates
    .filter((g) => g.state === "needed")
    .map((g) => g.label || g.id);
  const running = gates
    .filter(
      (g) => g.state === "running" && !(unverifiedCi && isListCiPlaceholder(g)),
    )
    .map((g) => g.label || g.id);
  const passed = gates.filter(satisfied).length;
  let state: GateRollup;
  let checking = false;
  if (all.length === 0) state = "unknown";
  else if (failing.length) state = "failing";
  else if (unverifiedCi && !needed.length && !running.length) {
    state = "unknown";
    checking = true;
  } else if (running.length || needed.length) state = "running";
  else state = "passed";
  return {
    state,
    checking,
    failing,
    needed,
    running,
    passed,
    total: gates.length,
  };
}

// submitBlock is why Submit must not be offered on this task now, or ""
// when it may be. Submit is off while a required check is failing or
// has findings, while a gate other than approval is still needed, and
// while CI is unknown or still being checked against the task detail. A
// verified running gate does not block: Submit records the approval and
// the change merges once its gates pass.
export function submitBlock(t: Task, listOnly = false): string {
  const s = gateSummary(t, listOnly);
  if (s.failing.length) {
    return `Submit is off: ${s.failing.join(", ")} ${s.failing.length === 1 ? "is" : "are"} failing. Fix it or override the gate on the task page.`;
  }
  if (s.needed.length) {
    return `Submit is off: ${s.needed.join(", ")} ${s.needed.length === 1 ? "is" : "are"} still needed.`;
  }
  if (s.state === "unknown") {
    return s.checking
      ? "Checking CI…"
      : "Submit is off: CI has not reported on this change yet.";
  }
  return "";
}

// pkg/ui's Task JSON carries prMergedAt only (not merged or closedReason),
// so non-PR closes (e.g. direct push or CL merges) stay under "Not
// merged" until the server exposes a dedicated merged field.
export function isMerged(t: Task): boolean {
  return !!(t.merged || t.prMergedAt || t.closedReason === "merged");
}
