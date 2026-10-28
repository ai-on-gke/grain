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

import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Snackbar,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";

const MAX_LISTED_RECIPIENTS = 8;

// NotifyRunningDialog lets a user send a human notice to all currently
// running task agents at once. The backend posts the notice to each running
// task's conversation (POST /api/deployment/broadcast with running_only: true),
// where daemonAddenda picks it up via GET /api/tasks/{id}/messages and hands
// it to the agent on its next tool call.
export default function NotifyRunningDialog({
  open,
  onClose,
  runningTasks = [],
  onSent,
  showError,
}) {
  const [message, setMessage] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [feedback, setFeedback] = useState(null);

  useEffect(() => {
    if (open) {
      setMessage("");
      setSubmitting(false);
    }
  }, [open]);

  const handleSubmit = async (e) => {
    if (e) e.preventDefault();
    const trimmed = message.trim();
    if (!trimmed || submitting || runningTasks.length === 0) return;

    setSubmitting(true);
    try {
      const res = await api("/api/deployment/broadcast", {
        method: "POST",
        body: JSON.stringify({ message: trimmed, running_only: true }),
      });
      const count = res?.delivered_count ?? 0;
      setFeedback(
        count === 1
          ? "Notice sent to 1 running task agent."
          : `Notice sent to ${count} running task agents.`,
      );
      onClose?.();
      await onSent?.(res);
    } catch (err) {
      showError?.(err);
    } finally {
      setSubmitting(false);
    }
  };

  const listed = runningTasks.slice(0, MAX_LISTED_RECIPIENTS);
  const remaining = runningTasks.length - listed.length;

  return (
    <>
      <Dialog
        open={open}
        onClose={submitting ? undefined : onClose}
        maxWidth="sm"
        fullWidth
      >
        <form onSubmit={handleSubmit}>
          <DialogTitle>Contact running agents</DialogTitle>
          <DialogContent>
            <DialogContentText sx={{ mb: 1.5 }}>
              Send a notice to every task agent currently running. The notice is
              added to each running task’s conversation and delivered on the
              agent’s next tool call.
            </DialogContentText>
            {runningTasks.length === 0 ? (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ mb: 1 }}
                data-testid="notify-running-summary"
              >
                No task agents are currently running.
              </Typography>
            ) : (
              <Box sx={{ mb: 1 }}>
                <Typography
                  variant="body2"
                  color="text.secondary"
                  sx={{ mb: 0.75 }}
                  data-testid="notify-running-summary"
                >
                  {runningTasks.length} running task agent
                  {runningTasks.length === 1 ? "" : "s"} will receive this
                  notice:
                </Typography>
                <Box
                  sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}
                  data-testid="notify-running-recipients"
                >
                  {listed.map((t) => (
                    <Chip
                      key={t.id}
                      size="small"
                      variant="outlined"
                      label={t.title ? `#${t.id} ${t.title}` : `#${t.id}`}
                      sx={{ maxWidth: 260 }}
                    />
                  ))}
                  {remaining > 0 && (
                    <Chip
                      size="small"
                      variant="outlined"
                      label={`+${remaining} more`}
                    />
                  )}
                </Box>
              </Box>
            )}
            <TextField
              autoFocus
              label="Notice"
              placeholder="e.g. Base branch is frozen for release; hold off on pushing"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              fullWidth
              multiline
              minRows={3}
              margin="normal"
              disabled={submitting}
              inputProps={{ "aria-label": "Notice" }}
            />
          </DialogContent>
          <DialogActions sx={{ px: 3, pb: 2 }}>
            <Button onClick={onClose} disabled={submitting}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="contained"
              disabled={
                submitting || !message.trim() || runningTasks.length === 0
              }
            >
              {submitting ? "Sending…" : "Send notice"}
            </Button>
          </DialogActions>
        </form>
      </Dialog>
      <Snackbar
        open={Boolean(feedback)}
        autoHideDuration={5000}
        onClose={() => setFeedback(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          onClose={() => setFeedback(null)}
          severity="success"
          variant="filled"
          sx={{ width: "100%" }}
        >
          {feedback}
        </Alert>
      </Snackbar>
    </>
  );
}
