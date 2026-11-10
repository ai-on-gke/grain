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

import { Link } from "@mui/material";
import { pullRequestUrl } from "../state.js";
import { useReturnFocus } from "./ListPrimitives.jsx";

// TaskTree draws a tree of work (workTree.js): each task under the task
// whose run filed it, with a link to the task, its status and its pull
// request. Two places draw one -- a task's own page (detail/WorkTree.jsx)
// and a Tree board -- and both hand it the same node shape, so a tree
// reads the same wherever it is met.
//
// A node carrying a `state` (a Tree board's, made from task rows) shows
// its status as the state badge every other view of the backlog shows;
// one without (the daemon's, in the progress comment's own words) shows
// the comment's own checkbox beside the word.
//
// The task being read (currentId) is already the page open -- a task's
// own detail, or the standing session that task backs, which is where
// opening a session's task resolves to -- so its id is drawn as text
// rather than a link that would go nowhere (grain/task-2546).
//
// `returnFocus` opts a Tree board's links into useReturnFocus: opening a
// standing session's row replaces the board with /session/<id> (and
// following a repo link inside an opened task replaces it too), so
// browser Back remounts the tree and would otherwise drop focus to
// <body>. Left null (a task pane's own WorkTree), links never claim focus
// on mount.
export default function TaskTree({
  nodes,
  currentId,
  onOpenTask,
  returnFocus = null,
}) {
  if (!nodes || nodes.length === 0) return null;
  return (
    <ul className="task-tree">
      {nodes.map((n) => (
        <TaskTreeNode
          key={n.id}
          node={n}
          currentId={currentId}
          onOpenTask={onOpenTask}
          returnFocus={returnFocus}
        />
      ))}
    </ul>
  );
}

function TaskTreeNode({ node: n, currentId, onOpenTask, returnFocus }) {
  const current = currentId != null && n.id === currentId;
  const href = `/tasks/${encodeURIComponent(n.id)}`;
  const [linkRef, remember] = useReturnFocus(
    href,
    current ? null : returnFocus,
  );
  return (
    <li data-task-id={n.id}>
      <div
        className={`task-tree-row${current ? " task-tree-current" : ""}${n.done ? " task-tree-done" : ""}`}
        aria-current={current ? "true" : undefined}
        style={onOpenTask && !current ? { cursor: "pointer" } : undefined}
        onClick={
          onOpenTask && !current
            ? (e) => {
                if (e.target.closest("a")) return;
                remember();
                linkRef.current?.focus({ preventScroll: true });
                onOpenTask(n.id);
              }
            : undefined
        }
      >
        {!n.state && (
          <span
            className="task-tree-check"
            aria-label={n.done ? "done" : "not done"}
          >
            {n.done ? "☑" : "☐"}
          </span>
        )}
        {current ? (
          <span className="task-tree-id">{n.id}</span>
        ) : (
          <Link
            ref={linkRef}
            href={`/tasks/${n.id}`}
            className="task-tree-id"
            onClick={(e) => {
              // A modified click is the browser's own (a new tab, a
              // new window), as it is on every other task row
              // (ListPrimitives.jsx's RowLink).
              if (
                !onOpenTask ||
                e.button !== 0 ||
                e.metaKey ||
                e.ctrlKey ||
                e.shiftKey ||
                e.altKey
              )
                return;
              e.preventDefault();
              remember();
              onOpenTask(n.id);
            }}
          >
            {n.id}
          </Link>
        )}
        <span className="task-tree-title">{n.title}</span>
        {n.state ? (
          <span className={`badge badge-${n.state} task-tree-status`}>
            {n.status}
          </span>
        ) : (
          <span className="task-tree-status">{n.status}</span>
        )}
        {n.pullRequest && (
          <Link
            className="task-tree-pr"
            href={pullRequestUrl(n.pullRequest)}
            target="_blank"
            rel="noopener noreferrer"
          >
            {n.pullRequest}
          </Link>
        )}
      </div>
      <TaskTree
        nodes={n.children}
        currentId={currentId}
        onOpenTask={onOpenTask}
        returnFocus={returnFocus}
      />
    </li>
  );
}
