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
  AUTO_MERGE_STATES,
  autoMergeStatus,
  autoMergeText,
} from "./auto-merge";

// The notes are the orchestrator's own sentences (queuestatus.go,
// recheck.go's repairAttemptNote, githubrefusal.go's refusalNote), so
// each case reads the way `grain merge-queue` prints that entry.
const t = (
  mergeQueueState: string | undefined,
  mergeQueueNote = "",
  extra: Partial<Task> = {},
): Task => ({
  id: "7",
  title: "x",
  state: "completed",
  autoMerge: true,
  mergeQueueState,
  mergeQueueNote,
  ...extra,
});

const text = (task: Task) => {
  const s = autoMergeStatus(task);
  return s ? autoMergeText(s) : null;
};

describe("autoMergeStatus", () => {
  it.each([
    [
      "waiting_queue",
      "waiting for GitHub to work out whether this branch merges cleanly (for 2m)",
      "Auto-merging · waiting for GitHub's mergeability check",
    ],
    [
      "unreadable",
      "waiting for GitHub to work out whether this branch merges cleanly",
      "Auto-merging · waiting for GitHub's mergeability check",
    ],
    [
      "waiting_ci",
      "waiting on checks that have not finished (`build`)",
      "Auto-merging · waiting for CI",
    ],
    ["merging", "", "Auto-merging · merging"],
    [
      "repairing",
      "an agent is repairing this branch now (it has conflicts with `main`; automatic repair 1 of 2)",
      "Auto-merging · branch conflicted, repair 1 of 2",
    ],
    [
      "repairing",
      "an agent is repairing this branch now (it has conflicts with `main`; automatic repair 2 of 2, allowed because the first one pushed)",
      "Auto-merging · branch conflicted, repair 2 of 2",
    ],
    [
      "repairing",
      "an agent is repairing this branch now (its checks are failing (`go`); automatic repair 1 of 2)",
      "Auto-merging · checks failing, repair 1 of 2",
    ],
    [
      "repair_needed",
      "it has conflicts with `main`",
      "Auto-merging · branch conflicted",
    ],
    [
      "repair_queued",
      "waiting for a worker to pick this task up and repair the branch (it has conflicts with `main`; automatic repair 1 of 2)",
      "Auto-merging · repair hasn't started (waiting for a free run slot)",
    ],
    [
      "repair_queued",
      "stopped by an operator -- reply or press Retry to resume",
      "Auto-merging · repair stopped by an operator",
    ],
    [
      "merge_failed",
      "GitHub has refused this merge 3 times: Base branch was modified",
      "Auto-merging · merge refused: Base branch was modified",
    ],
    [
      "merge_failed",
      "GitHub has refused this merge once: At least 1 approving review is required",
      "Auto-merging · merge refused: At least 1 approving review is required",
    ],
    [
      "merge_failed",
      "grain has stopped driving this merge and handed it to a person -- its checks are failing",
      "Auto-merge stopped · handed to a person",
    ],
    [
      "waiting_queue",
      "merges next cycle: 1 merge already this cycle (the main branch)",
      "Auto-merging · merging next cycle",
    ],
    [
      "waiting_review",
      "waiting for review task 12 to finish",
      "Auto-merging · waiting for its review",
    ],
    ["batching", "", "Auto-merging · waiting for CI"],
  ])("%s %j reads %j", (state, note, want) => {
    expect(text(t(state, note))).toBe(want);
  });

  it.each([
    [
      "merge_failed",
      "GitHub has refused this merge 5 times: Base branch was modified",
      "Auto-merge stopped · merge refused: Base branch was modified",
    ],
    [
      "repairing",
      "an agent is repairing this branch now (it has conflicts with `main`; automatic repair 2 of 2)",
      "Auto-merge stopped · handed to a person",
    ],
    ["waiting_ci", "", "Auto-merge stopped · handed to a person"],
  ])("a blocked %s %j reads %j", (state, note, want) => {
    const task = t(state, note, {
      mergeQueueBlockedAt: "2026-11-15T00:00:00Z",
    });
    expect(text(task)).toBe(want);
    expect(autoMergeStatus(task)?.stopped).toBe(true);
  });

  it("says Auto-merging for a completed task with no entry state yet", () => {
    expect(text(t(undefined))).toBe("Auto-merging");
  });

  it("is nothing for a task grain is not landing", () => {
    expect(autoMergeStatus(t(undefined, "", { state: "running" }))).toBeNull();
  });

  it("hides closed tasks and merged or closed pull requests, whatever state was left behind", () => {
    expect(
      autoMergeStatus(t("waiting_ci", "", { state: "closed" })),
    ).toBeNull();
    expect(
      autoMergeStatus(t("merging", "", { prMergedAt: "2026-11-15T00:00:00Z" })),
    ).toBeNull();
    expect(
      autoMergeStatus(
        t("waiting_ci", "", { prClosedAt: "2026-11-15T00:00:00Z" }),
      ),
    ).toBeNull();
  });

  it("never says merge queue or a position for any state", () => {
    for (const s of AUTO_MERGE_STATES) {
      const got = text(t(s, "1 ahead of this one")) ?? "";
      expect(got).toMatch(/^Auto-merg/);
      expect(got.toLowerCase()).not.toContain("on merge queue");
      expect(got.toLowerCase()).not.toContain("queued to merge");
      expect(got).not.toMatch(/#\d|\d+ ahead|position/);
    }
  });
});
