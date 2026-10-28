import { Link } from "@mui/material";
import { pullRequestUrl } from "../state.js";

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
export default function TaskTree({ nodes, currentId, onOpenTask }) {
  if (!nodes || nodes.length === 0) return null;
  return (
    <ul className="task-tree">
      {nodes.map((n) => (
        <li key={n.id} data-task-id={n.id}>
          <div
            className={`task-tree-row${n.id === currentId ? " task-tree-current" : ""}${n.done ? " task-tree-done" : ""}`}
            aria-current={n.id === currentId ? "true" : undefined}
            style={onOpenTask ? { cursor: "pointer" } : undefined}
            onClick={
              onOpenTask
                ? (e) => {
                    if (e.target.closest("a")) return;
                    e.currentTarget
                      .querySelector(".task-tree-id")
                      ?.focus({ preventScroll: true });
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
            <Link
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
                onOpenTask(n.id);
              }}
            >
              {n.id}
            </Link>
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
          />
        </li>
      ))}
    </ul>
  );
}
