import { Button } from "@mui/material";

// ClosedTasksMore is the end of a task view that can show closed work: the
// live list carries no closed tasks, and the closed ones are paged in
// newest-closed first (closedTasks.js), so this is how a reader reaches
// the ones closed before the pages already read. Nothing is drawn once
// every closed task is in.
export default function ClosedTasksMore({ closed }) {
  if (!closed?.hasMore) return null;
  return (
    <div className="closed-tasks-more">
      <Button
        size="small"
        variant="outlined"
        disabled={closed.loading}
        onClick={() => closed.loadMore()}
      >
        {closed.loading ? "Loading closed tasks…" : "Load older closed tasks"}
      </Button>
    </div>
  );
}
