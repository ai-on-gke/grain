import { Link } from "@mui/material";

// TaskLink is a link from one task's pane to another task: a plain click
// opens it in place, the way every other task link does, while the href
// names the task itself, so a middle click, a modified click, "open in
// new tab" and "copy link" all reach it too -- an href of "#" sent each
// of those to the pane already open instead.
export default function TaskLink({ id, onOpenTask, children }) {
  return (
    <Link
      href={`/tasks/${encodeURIComponent(id)}`}
      onClick={(e) => {
        if (!onOpenTask) return;
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)
          return;
        e.preventDefault();
        onOpenTask(id);
      }}
    >
      {children ?? id}
    </Link>
  );
}
