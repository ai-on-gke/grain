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
import { isMerged } from "../shared/gates";
import { GROUP_ORDER, type InboxEntry } from "./inbox-classifier";

// What the Inbox page says about a selected row that has left the queue,
// and when a reply must not be sent. Kept out of the store, which the
// shell loads up front, so it ships with the page.

// whyGone says, as far as the list knows, why a row left the queue:
// what this page did to it, the group it moved to, or its new state.
export function whyGone(
  e: InboxEntry,
  tasks: readonly Task[],
  live: readonly InboxEntry[],
  answeredWith: string | undefined,
): string {
  if (answeredWith) return `you answered it (${answeredWith})`;
  if (!e.taskId) return "it was acknowledged or cleared";
  const t = tasks.find((x) => x.id === e.taskId);
  if (!t) return "it is no longer listed";
  const now = live.find((x) => x.taskId === e.taskId && x.key !== e.key);
  if (now) {
    const g = GROUP_ORDER.find((x) => x.id === now.groupId);
    return `it moved to ${g ? `\u201c${g.title}\u201d` : "another group"}`;
  }
  switch (t.state) {
    case "running":
      return "it is running now";
    case "queued":
      return "it is approved and waiting to run";
    case "completed":
      return "it was submitted and is merging";
    case "closed":
      return isMerged(t) ? "it merged" : "it was closed";
    case "deferred":
      return "it was deferred";
    default:
      return `it is ${t.state.replace(/_/g, " ")} now`;
  }
}

// replyRefusal is why a reply to task `id` must not be sent now, or "":
// the task is gone, no longer awaiting a reply, a stopped turn rather
// than a question, or parked on a different question than the one the
// person saw.
export function replyRefusal(
  t: Task | undefined,
  id: string,
  questionCommentId?: number,
): string {
  if (!t) return `Not sent: #${id} is no longer waiting on a reply.`;
  if (t.state !== "awaiting_reply" || t.awaitingAccess) {
    return `Not sent: #${id} is no longer waiting on a reply (it is ${t.state.replace(/_/g, " ")}).`;
  }
  if (t.turnStopped || t.reason?.reason === "awaiting_reply/stopped") {
    return `Not sent: #${id} was stopped rather than asking; resume it instead.`;
  }
  if (
    questionCommentId !== undefined &&
    t.questionCommentId !== undefined &&
    t.questionCommentId !== questionCommentId
  ) {
    return `Not sent: #${id} asked a new question since you started typing. Read it first.`;
  }
  return "";
}
