// KeepRunningChip marks a task that asks to keep running
// (model.Task.KeepRunning): one restarted every time its run exits, so it
// never completes on its own. A proposal asking for it is held for a person
// to approve (model.PersonOnlyProposalSettings), and the Inbox row with its
// approve button is where that person decides -- so the row says so, not
// only the hold reason in the body on the task's own page.
import { Chip } from "@mui/material";

export default function KeepRunningChip() {
  return (
    <Chip
      size="small"
      variant="outlined"
      color="warning"
      className="chip-keep-running"
      data-testid="task-keep-running-badge"
      title="keep_running: restarted every time its run exits, so it never completes on its own"
      label="keeps running"
    />
  );
}
