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

// taskRowKey is what a GET /api/tasks row says about a task that a detail
// or summary read would also see change: its state, its run's activity,
// the question it waits on, its pull request and merge-queue state, its
// gates and holds. The list row carries no single change stamp (pkg/ui's
// Task has no updatedAt), so callers that skip re-reading an unchanged
// task compare this key instead. A change it cannot see -- a comment that
// moves nothing else -- still arrives through the task's own SSE event
// or the detail poll's MAX_DETAIL_SKIP_MS ceiling.
export function taskRowKey(t: Task | null | undefined): string {
  if (!t) return "";
  const gates = (t.mergeGates ?? [])
    .map((g) => `${g.id}:${g.state}:${g.detail ?? ""}:${g.approvedBy ?? ""}`)
    .join(",");
  const r = t as Task & {
    endedAt?: string;
    closedAt?: string;
    runNowAt?: string;
    mergeQueueStateAt?: string;
    unansweredCommentFrom?: string;
    readCount?: number;
  };
  return [
    t.state,
    t.activityAt ?? "",
    t.activity ?? "",
    t.questionCommentId ?? "",
    r.unansweredCommentFrom ?? "",
    r.readCount ?? "",
    t.awaitingAccess ? "1" : "0",
    t.turnStopped ? "1" : "0",
    t.prMergedAt ?? "",
    t.prClosedAt ?? "",
    r.endedAt ?? "",
    r.closedAt ?? "",
    r.runNowAt ?? "",
    t.mergeQueueState ?? "",
    r.mergeQueueStateAt ?? "",
    t.humanApprovedBy ?? "",
    (t.humanReviewHold ?? []).length,
    t.approved ? "1" : "0",
    t.deferredUntil ?? "",
    t.priority ?? "",
    t.title ?? "",
    t.reason?.reason ?? "",
    t.paused ? "1" : "0",
    t.blocked ? "1" : "0",
    gates,
  ].join("|");
}
