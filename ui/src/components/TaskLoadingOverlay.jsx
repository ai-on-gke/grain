import { Button } from "@mui/material";
import GrainMark from "./GrainMark.jsx";
import Overlay from "./Overlay.jsx";

// TaskLoadingOverlay is the task pane in the moment between a click on a
// task and its detail arriving (grain/task-1756). App.jsx used to mount
// nothing until /api/tasks/:id answered, so on a slow connection a click
// on a task looked like it had done nothing at all. This is the same pane
// DetailOverlay fills, with the same "← Back" out of it, holding the
// animated mark LoadingScreen uses for the app's own first load.
//
// It is also that pane once the load has failed for a task the address
// bar named (error set): grain answered with something other than a 404,
// so the task is likely only out of reach for now, and the pane keeps
// /tasks/N in the address bar with the error and a Retry rather than
// dropping the reader on the list (grain/task-3173).
const MARK_SIZE = 160;

export default function TaskLoadingOverlay({
  taskId,
  onClose,
  animate = true,
  error = null,
  onRetry,
}) {
  if (error !== null) {
    return (
      <Overlay
        onClose={onClose}
        pane
        animate={animate}
        label={`Task ${taskId} could not be loaded`}
      >
        <div className="render-error render-error-page" role="alert">
          <h2>Task {taskId} could not be loaded</h2>
          <p>
            grain did not answer for this task just now. It may only be
            restarting: this pane tries again on its own, or retry now.
          </p>
          <pre className="render-error-message">{error}</pre>
          <div className="render-error-actions">
            <Button variant="contained" onClick={onRetry}>
              Retry
            </Button>
          </div>
        </div>
      </Overlay>
    );
  }
  return (
    <Overlay
      onClose={onClose}
      pane
      animate={animate}
      label={`Loading task ${taskId}`}
    >
      <div className="loading-screen task-loading" aria-busy="true">
        <GrainMark size={MARK_SIZE} animated title={`Loading task ${taskId}`} />
      </div>
    </Overlay>
  );
}
