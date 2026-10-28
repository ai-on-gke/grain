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
