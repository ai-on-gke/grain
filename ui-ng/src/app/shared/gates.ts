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

import type { MergeGate, Task } from "../api/models";

// Merge gates (pkg/model merge_gates.go) read for a person deciding
// whether to submit. The approval gate is the one Submit itself
// satisfies, so it never counts against Submit and is left out of the
// n/n count.

export const APPROVAL_GATE = "approval";

export type GateRollup = "passed" | "running" | "failing" | "unknown";

export interface GateSummary {
  state: GateRollup;
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

// gateSummary rolls a task's merge gates up into one state: failing if
// any gate is failing or has findings, running if any is running or
// still needed, passed when every gate is satisfied, unknown when the
// server has reported no gates for it yet.
export function gateSummary(t: Task): GateSummary {
  const all = t.mergeGates ?? [];
  const gates = all.filter((g) => g.id !== APPROVAL_GATE);
  const failing = gates
    .filter((g) => g.state === "failing" || g.state === "findings")
    .map((g) => g.label || g.id);
  const needed = gates
    .filter((g) => g.state === "needed")
    .map((g) => g.label || g.id);
  const running = gates
    .filter((g) => g.state === "running")
    .map((g) => g.label || g.id);
  const passed = gates.filter(satisfied).length;
  let state: GateRollup;
  if (all.length === 0) state = "unknown";
  else if (failing.length) state = "failing";
  else if (running.length || needed.length) state = "running";
  else state = "passed";
  return { state, failing, needed, running, passed, total: gates.length };
}

// submitBlock is why Submit must not be offered on this task now, or ""
// when it may be. Submit is off while a required check is failing or
// has findings, while a gate other than approval is still needed, and
// while the server has reported no gates at all (CI unknown). A gate
// that is still running does not block: Submit records the approval and
// the change merges once its gates pass.
export function submitBlock(t: Task): string {
  const s = gateSummary(t);
  if (s.failing.length) {
    return `Submit is off: ${s.failing.join(", ")} ${s.failing.length === 1 ? "is" : "are"} failing. Fix it or override the gate on the task page.`;
  }
  if (s.needed.length) {
    return `Submit is off: ${s.needed.join(", ")} ${s.needed.length === 1 ? "is" : "are"} still needed.`;
  }
  if (s.state === "unknown") {
    return "Submit is off: CI has not reported on this change yet.";
  }
  return "";
}

export interface CiChip {
  text: string;
  tone: "healthy" | "error" | "setup" | "neutral";
  tip: string;
}

// ciChip is the compact CI state for a row or a decision area, or null
// when the task has no gates and is not waiting for a submit (nothing to
// say yet).
export function ciChip(t: Task): CiChip | null {
  const s = gateSummary(t);
  const lines = (t.mergeGates ?? [])
    .filter((g) => g.id !== APPROVAL_GATE)
    .map(
      (g) =>
        `${g.label || g.id}: ${g.state}${g.detail ? ` (${g.detail})` : ""}`,
    )
    .join("; ");
  switch (s.state) {
    case "failing":
      return {
        text: `✕ ${s.failing[0]}`,
        tone: "error",
        tip: `Failing: ${s.failing.join(", ")}. ${lines}`,
      };
    case "running":
      return {
        text: `… ${s.running[0] ?? s.needed[0]}`,
        tone: "setup",
        tip: `Still running or needed. ${lines}`,
      };
    case "passed":
      return s.total
        ? {
            text: `✓ ${s.passed}/${s.total}`,
            tone: "healthy",
            tip: `Every required gate passed. ${lines}`,
          }
        : {
            text: "✓ no checks",
            tone: "healthy",
            tip: "This change has no required checks besides approval.",
          };
    default:
      if (t.state !== "awaiting_submit" && !t.pullRequest) return null;
      return {
        text: "CI unknown",
        tone: "neutral",
        tip: "The server has not reported any gates for this change yet.",
      };
  }
}

// repoShort is a repository's name without its owner, for a row; the
// full name goes in the tooltip and in every confirmation.
export function repoShort(repo: string | undefined | null): string {
  if (!repo) return "";
  const i = repo.lastIndexOf("/");
  return i >= 0 ? repo.slice(i + 1) : repo;
}

export function isMerged(t: Task): boolean {
  return !!(t.merged || t.prMergedAt || t.closedReason === "merged");
}
