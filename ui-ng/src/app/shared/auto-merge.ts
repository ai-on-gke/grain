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

// AutoMergeStatus is what a pull request grain is landing on its own is
// waiting for, in words: "Auto-merging" and the concrete thing it waits
// on, read off the same entry state and note `grain merge-queue` prints
// (Task.mergeQueueState, Task.mergeQueueNote; pkg/orchestrator's
// queuestatus.go writes both).
//
// It deliberately never says "merge queue" or a position in one. Merge
// batching and ordering were removed (docs/merge-queue-batching.md,
// docs/merge-queue-behind.md "No order between pull requests"): each pull
// request merges by itself once it is green and merges cleanly, so a
// "queue" label read as the old ordering being back. The API's reason
// codes (completed/on_merge_queue, waiting_queue, ...) are unchanged; only
// the words a person reads are.
export interface AutoMergeStatus {
  // "Auto-merging" while grain is driving the merge, "Auto-merge stopped"
  // once it has handed the change to a person (merge_failed that is not a
  // GitHub refusal it keeps retrying).
  label: string;
  // What it is waiting for, lower case so it reads after a "·": "waiting
  // for CI", "branch conflicted, repair 1 of 2". Empty when the state
  // carries nothing more specific than the label.
  detail: string;
  // True when nobody is driving it any more and a person owns it.
  stopped: boolean;
}

export const AUTO_MERGING = "Auto-merging";
export const AUTO_MERGE_STOPPED = "Auto-merge stopped";

// The repair attempt the note names: "automatic repair 2 of 2".
const REPAIR_ATTEMPT = /automatic repair (\d+) of (\d+)/;
// GitHub's refusal, quoted: "GitHub has refused this merge 3 times: <why>".
const REFUSED = /^GitHub has refused this merge [^:]*:\s*(.*)$/s;

// The states that carry nothing past a fixed phrase. Kept to the
// orchestrator's own list (queuestatus.go); a state this build does not
// know reads as the bare label rather than as raw text.
const FIXED: Record<string, string> = {
  waiting_ci: "waiting for CI",
  // Retired states (batching was removed): a stored row still means CI.
  batching: "waiting for CI",
  gathering: "waiting for CI",
  waiting_review: "waiting for its review",
  merging: "merging",
  updating_branch: "bringing the branch up to date",
  checks_unfixable: "its checks can't pass from a branch",
  waiting_github_approval: "waiting for an approval GitHub requires",
  github_merge_queue: "in GitHub's own merge queue",
  agent_working: "an agent is working on the branch",
  repair_queued: "repair hasn't started (waiting for a free run slot)",
};

// Every state the orchestrator writes, so a test can hold this file to
// queuestatus.go's list.
export const AUTO_MERGE_STATES = [
  ...Object.keys(FIXED),
  "repairing",
  "repair_needed",
  "waiting_queue",
  "unreadable",
  "merge_failed",
];

// repairWhat is what the repair is for, from the note: a conflict with
// the base, or checks that failed.
function repairWhat(note: string): string {
  if (/conflict/i.test(note)) return "branch conflicted";
  if (/checks? (are |is )?failing|failing check/i.test(note))
    return "checks failing";
  return "";
}

function repairDetail(note: string, fallback: string): string {
  const what = repairWhat(note);
  const m = REPAIR_ATTEMPT.exec(note);
  const attempt = m ? `repair ${m[1]} of ${m[2]}` : "";
  if (what && attempt) return `${what}, ${attempt}`;
  return what || attempt || fallback;
}

// autoMergeStatus is null for a task grain is not landing: no merge-queue
// state, and not a completed task (completed/on_merge_queue). A closed
// task, or one whose pull request has merged or closed, is never
// auto-merging, whatever state its last cycle left behind.
export function autoMergeStatus(t: Task): AutoMergeStatus | null {
  if (t.state === "closed" || t.prMergedAt || t.prClosedAt || t.merged)
    return null;
  const state = t.mergeQueueState ?? "";
  const note = (t.mergeQueueNote ?? "").trim();
  if (!state) {
    if (t.state !== "completed") return null;
    return { label: AUTO_MERGING, detail: "", stopped: false };
  }
  const going = (detail: string): AutoMergeStatus => ({
    label: AUTO_MERGING,
    detail,
    stopped: false,
  });
  switch (state) {
    case "repair_queued":
      // Two repair_queued notes are not a run waiting for a slot: a
      // repair a person stopped (sync.go's stoppedRepairNote), and
      // unresolved review threads sent back to the task.
      if (/^stopped by an operator/.test(note))
        return going("repair stopped by an operator");
      if (/sent back to the task/.test(note))
        return going("unresolved review threads sent back to the task");
      return going(FIXED[state]);
    case "repairing":
      return going(repairDetail(note, "repairing the branch"));
    case "repair_needed":
      return going(repairDetail(note, "needs a repair"));
    case "waiting_queue":
    case "unreadable":
      if (/merges cleanly|mergeab/i.test(note))
        return going("waiting for GitHub's mergeability check");
      if (state === "unreadable") return going("can't read the pull request");
      return going("merging next cycle");
    case "merge_failed": {
      const refused = REFUSED.exec(note);
      if (refused) return going(`merge refused: ${refused[1].trim()}`);
      return {
        label: AUTO_MERGE_STOPPED,
        detail: "handed to a person",
        stopped: true,
      };
    }
  }
  if (t.mergeQueueBlockedAt)
    return {
      label: AUTO_MERGE_STOPPED,
      detail: "handed to a person",
      stopped: true,
    };
  return going(FIXED[state] ?? "");
}

// autoMergeText is the status on one line: "Auto-merging · waiting for CI".
export function autoMergeText(s: AutoMergeStatus): string {
  return s.detail ? `${s.label} · ${s.detail}` : s.label;
}
