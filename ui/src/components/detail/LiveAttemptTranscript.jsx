import { Box, Typography } from "@mui/material";
import { useAttemptTranscript } from "../../useAttemptTranscript.js";

// LiveAttemptTranscript is the still-running attempt's transcript shown
// inline in the pane, with the same fetching, polling and pin-to-bottom
// scrolling as AttemptTranscriptOverlay (useAttemptTranscript).
export default function LiveAttemptTranscript({ taskId, attempt, showError }) {
  const { transcript, view, onScroll } = useAttemptTranscript(
    taskId,
    attempt,
    showError,
  );

  return (
    <Box sx={{ mb: 2 }}>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Live Attempt #{attempt.number} Transcript
      </Typography>
      <pre className="logs-view" ref={view} onScroll={onScroll}>
        {transcript === null
          ? "Loading…"
          : transcript || "Still running -- nothing written yet."}
      </pre>
    </Box>
  );
}
