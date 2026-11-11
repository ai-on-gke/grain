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

// Paging a task's conversation and run list on the task page
// (design/design/durable-tasks.design.md#DT-15).
//
// The task page opens with at most TASK_COMMENTS_LIMIT newest comments and
// TASK_ATTEMPTS_LIMIT newest runs. "Show earlier" asks for the page before
// via comments_before= / attempts_before=, and mergeFreshDetail keeps any
// earlier pages the reader has already opened when a poll refreshes the head.

export const TASK_COMMENTS_LIMIT = 50;
export const TASK_ATTEMPTS_LIMIT = 20;
export const TASK_DEEP_LINK_COMMENTS_LIMIT = 200;

export function taskDetailPath(id) {
  return `/api/tasks/${encodeURIComponent(id)}?comments_limit=${TASK_COMMENTS_LIMIT}&attempts_limit=${TASK_ATTEMPTS_LIMIT}`;
}

// isTaskDetailGetPath matches GET /api/tasks/<id> with or without query params.
export function isTaskDetailGetPath(path, id) {
  const prefix = `/api/tasks/${encodeURIComponent(id)}`;
  return path === prefix || path.startsWith(`${prefix}?`);
}

// taskEarlierPath builds the GET /api/tasks/<id> query for the page before
// what task currently holds, or "" when nothing earlier remains.
export function taskEarlierPath(
  task,
  {
    commentsLimit = TASK_COMMENTS_LIMIT,
    attemptsLimit = TASK_ATTEMPTS_LIMIT,
    commentsOnly = false,
  } = {},
) {
  if (!task || task.id == null) return "";
  const comments = Array.isArray(task.comments) ? task.comments : [];
  const attempts = Array.isArray(task.attempts) ? task.attempts : [];
  const wantComments = (task.olderComments || 0) > 0 && comments.length > 0;
  const wantAttempts =
    !commentsOnly && (task.olderAttempts || 0) > 0 && attempts.length > 0;
  if (!wantComments && !wantAttempts) return "";

  const params = new URLSearchParams();
  if (wantComments) {
    params.set("comments_limit", String(commentsLimit));
    params.set("comments_before", String(comments[0].id));
  } else {
    params.set("comments_limit", "1");
  }
  if (wantAttempts) {
    params.set("attempts_limit", String(attemptsLimit));
    params.set("attempts_before", String(attempts[0].number));
  } else {
    params.set("attempts_limit", "1");
  }
  return `/api/tasks/${encodeURIComponent(task.id)}?${params.toString()}`;
}

function mergeTransitions(olderTransitions, newerTransitions) {
  const a = Array.isArray(olderTransitions) ? olderTransitions : [];
  const b = Array.isArray(newerTransitions) ? newerTransitions : [];
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const seen = new Set(
    b.map((tr) => `${tr.state}|${tr.at || ""}|${tr.inferred ? 1 : 0}`),
  );
  const combined = [...b];
  for (const tr of a) {
    const k = `${tr.state}|${tr.at || ""}|${tr.inferred ? 1 : 0}`;
    if (!seen.has(k)) {
      seen.add(k);
      combined.push(tr);
    }
  }
  combined.sort((x, y) => {
    const tx = x.at ? Date.parse(x.at) : 0;
    const ty = y.at ? Date.parse(y.at) : 0;
    return tx - ty;
  });
  return combined;
}

// prependEarlierPage merges an older page fetched via taskEarlierPath in front
// of current.
export function prependEarlierPage(
  current,
  earlier,
  { loadedComments = false, loadedAttempts = false } = {},
) {
  if (!current || !earlier || String(current.id) !== String(earlier.id)) {
    return current;
  }
  const next = { ...current };
  if (loadedComments) {
    const existing = Array.isArray(current.comments) ? current.comments : [];
    const incoming = Array.isArray(earlier.comments) ? earlier.comments : [];
    const seen = new Set(existing.map((c) => c.id));
    const prefix = incoming.filter((c) => !seen.has(c.id));
    next.comments = [...prefix, ...existing];
    next.olderComments = earlier.olderComments || 0;
  }
  if (loadedAttempts) {
    const existing = Array.isArray(current.attempts) ? current.attempts : [];
    const incoming = Array.isArray(earlier.attempts) ? earlier.attempts : [];
    const seen = new Set(existing.map((a) => a.number));
    const prefix = incoming.filter((a) => !seen.has(a.number));
    next.attempts = [...prefix, ...existing];
    next.olderAttempts = earlier.olderAttempts || 0;
    next.transitions = mergeTransitions(
      earlier.transitions,
      current.transitions,
    );
  }
  return next;
}

// mergeFreshDetail merges a newly polled head page onto prev without dropping
// earlier comments or runs the reader has already paged back through.
export function mergeFreshDetail(prev, fresh) {
  if (!prev || !fresh || String(prev.id) !== String(fresh.id)) {
    return fresh;
  }
  let merged = fresh;

  const prevComments = Array.isArray(prev.comments) ? prev.comments : [];
  const freshComments = Array.isArray(fresh.comments) ? fresh.comments : [];
  if (
    prevComments.length > 0 &&
    freshComments.length > 0 &&
    typeof prevComments[0].id === "number" &&
    typeof freshComments[0].id === "number" &&
    prevComments[0].id < freshComments[0].id
  ) {
    const freshFirstId = freshComments[0].id;
    const prevCovered = (prev.olderComments || 0) + prevComments.length;
    const freshSkipped = fresh.olderComments || 0;
    // Only stitch when prev reaches up to fresh's window with no missing gap.
    if (
      prevCovered >= freshSkipped ||
      prevComments.some((c) => c.id >= freshFirstId)
    ) {
      const earlierPrefix = prevComments.filter((c) => c.id < freshFirstId);
      if (earlierPrefix.length > 0) {
        merged = {
          ...merged,
          comments: [...earlierPrefix, ...freshComments],
          olderComments: prev.olderComments || 0,
        };
      }
    }
  }

  const prevAttempts = Array.isArray(prev.attempts) ? prev.attempts : [];
  const freshAttempts = Array.isArray(fresh.attempts) ? fresh.attempts : [];
  if (
    prevAttempts.length > 0 &&
    freshAttempts.length > 0 &&
    typeof prevAttempts[0].number === "number" &&
    typeof freshAttempts[0].number === "number" &&
    prevAttempts[0].number < freshAttempts[0].number
  ) {
    const freshFirstNum = freshAttempts[0].number;
    const prevLastNum = prevAttempts[prevAttempts.length - 1].number;
    if (typeof prevLastNum === "number" && prevLastNum >= freshFirstNum - 1) {
      const earlierPrefix = prevAttempts.filter(
        (a) => a.number < freshFirstNum,
      );
      if (earlierPrefix.length > 0) {
        merged = {
          ...merged,
          attempts: [...earlierPrefix, ...freshAttempts],
          olderAttempts: prev.olderAttempts || 0,
          transitions: mergeTransitions(prev.transitions, fresh.transitions),
        };
      }
    }
  }

  return merged;
}

export function earlierButtonLabel(olderComments = 0, olderAttempts = 0) {
  const parts = [];
  if (olderComments > 0) {
    parts.push(`${olderComments} comment${olderComments === 1 ? "" : "s"}`);
  }
  if (olderAttempts > 0) {
    parts.push(`${olderAttempts} run${olderAttempts === 1 ? "" : "s"}`);
  }
  if (parts.length === 0) return "";
  return `Show earlier (${parts.join(", ")})`;
}
