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
import { APPROVAL_GATE, gateSummary } from "./gates";

export interface CiChip {
  text: string;
  tone: "healthy" | "error" | "setup" | "neutral";
  tip: string;
}

// ciChip is the compact CI state for a row or a decision area, or null
// when the task has no gates and is not waiting for a submit (nothing to
// say yet). Kept out of gates.ts so lazy pages alone carry its strings.
export function ciChip(t: Task, listOnly = false): CiChip | null {
  const s = gateSummary(t, listOnly);
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
      if (s.checking) {
        return {
          text: "Checking CI…",
          tone: "neutral",
          tip: "Reading the CI result from the task detail.",
        };
      }
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
