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

// Task references in grain's own comments, made into links.
//
// grain names other tasks in what it says on a task in plain words --
// "Filed task 1514 to review it", "Review task 1514 left findings on this
// change" (orchestrator's securityhold.go and review.go) -- and before
// this the only way from a held change to its security review was to
// read the number off that sentence and go looking for it
// (docs/test-runs/2026-10-01-security-hold-escalated-proposals.md,
// finding 4).
//
// The link is a fragment, "#task-<id>", rather than the task's own path:
// a path would have to know which team prefix the page is under, and
// Markdown sends every link it renders to a new tab. A fragment is
// neither -- Markdown leaves it in this tab, and the timeline catches a
// click on one (taskRefFromHref) and opens the task in place, the way
// every other task link on the pane does. Anywhere nothing catches it,
// it does nothing at all, which is the right failure.
//
// Only grain's own comments are rewritten (the caller decides which):
// a person or an agent writing "task 3" may mean anything, and their
// words are rendered as they wrote them.

const TASK_REF = /(^|[^\w[/#-])([Tt]ask) (\d+)\b/g;

// Code spans and fences are left alone: a path or a command quoted in
// backticks is to be read, not followed.
const CODE = /(```[\s\S]*?(?:```|$)|`[^`\n]*`)/;

// linkTaskRefs returns body with every "task <id>" outside code turned
// into a markdown link to "#task-<id>".
export function linkTaskRefs(body) {
  if (!body) return body;
  return body
    .split(CODE)
    .map((part, i) =>
      i % 2 === 1
        ? part
        : part.replace(
            TASK_REF,
            (_, before, word, id) => `${before}[${word} ${id}](#task-${id})`,
          ),
    )
    .join("");
}

// taskRefFromHref is the task id a linkTaskRefs link names, or "" for
// any other href.
export function taskRefFromHref(href) {
  const m = /^#task-(\d+)$/.exec(href || "");
  return m ? m[1] : "";
}
