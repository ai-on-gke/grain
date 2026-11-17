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

import { useState } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";

// AcceptFindingsDialog is a person's decision to merge over one review's
// findings on one gate as they are (POST /api/tasks/{id}/accept-findings,
// ui.Client.AcceptReviewFindings): the step between Submit, which never
// clears findings, and Force merge, which overrides every gate. It names
// the gate and the review, and requires a reason.
export default function AcceptFindingsDialog({ gate, onClose, t, onSubmit }) {
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);

  if (!gate || !t) return null;

  const reviewTask = gate.reviewTask || gate.tasks?.[0] || "";
  const head = t.ci?.commit || undefined;
  const trimmedReason = reason.trim();
  const canConfirm = Boolean(trimmedReason) && !submitting;

  const handleClose = () => {
    if (submitting) return;
    setReason("");
    onClose?.();
  };

  const handleConfirm = async (e) => {
    e?.preventDefault();
    if (!canConfirm) return;
    setSubmitting(true);
    try {
      await onSubmit?.(() =>
        api(`/api/tasks/${t.id}/accept-findings`, {
          method: "POST",
          body: JSON.stringify({
            gate: gate.name,
            ...(reviewTask ? { reviewTask: String(reviewTask) } : {}),
            reason: trimmedReason,
            head,
          }),
        }),
      );
      setReason("");
      onClose?.();
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open
      onClose={handleClose}
      maxWidth="sm"
      fullWidth
      data-testid="accept-findings-dialog"
    >
      <form onSubmit={handleConfirm}>
        <DialogTitle sx={{ pb: 1 }}>
          Accept {gate.label || gate.name} findings
        </DialogTitle>
        <DialogContent>
          <Stack spacing={1.5}>
            <Typography sx={{ fontSize: "0.85rem" }}>
              {reviewTask ? `Review task ${reviewTask}` : "The review"} left
              findings on this gate
              {gate.detail ? (
                <>
                  : <em>{gate.detail}</em>
                </>
              ) : (
                "."
              )}
            </Typography>
            <Alert severity="info" sx={{ fontSize: "0.82rem" }}>
              Accepting them lets this change merge without fixing them. It
              clears this review&apos;s findings on this gate only. CI, the
              other gates, any approval a trigger asks for and any security hold
              still apply. A later push is reviewed again, and new findings need
              a new decision. Your name, the review, the head and your reason
              are recorded on the task and the pull request.
            </Alert>
            {head && (
              <Typography variant="caption" color="text.secondary">
                At commit <code>{head.slice(0, 12)}</code>.
              </Typography>
            )}
            <TextField
              size="small"
              fullWidth
              required
              multiline
              minRows={2}
              label="Why it merges as it is (required)"
              placeholder="e.g. Out of scope for this change; follow-up filed to discuss the extensions"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              slotProps={{
                htmlInput: {
                  "aria-label": "Reason for accepting the findings",
                  "data-testid": "accept-findings-reason",
                  maxLength: 500,
                },
              }}
            />
          </Stack>
        </DialogContent>
        <DialogActions sx={{ px: 3, pb: 2 }}>
          <Button onClick={handleClose} disabled={submitting}>
            Cancel
          </Button>
          <Button
            type="submit"
            variant="contained"
            disabled={!canConfirm}
            data-testid="confirm-accept-findings"
          >
            Accept findings
          </Button>
        </DialogActions>
      </form>
    </Dialog>
  );
}
