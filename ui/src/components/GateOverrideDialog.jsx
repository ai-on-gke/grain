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
  Box,
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
import {
  deriveMergeGates,
  formatGateStatus,
  overrideReviewButtonLabel,
  unsatisfiedReviewGates,
} from "./MergeGatesList.jsx";

// pullRequestNumber extracts the numeric PR number from "owner/repo#42",
// or "" when t has no numbered pull request yet.
export function pullRequestNumber(pullRequest) {
  const m = /#(\d+)$/.exec(String(pullRequest || "").trim());
  return m ? m[1] : "";
}

// normalizeTypedPR strips an optional leading "#" and whitespace so both "42"
// and "#42" match pull request #42.
function normalizeTypedPR(value) {
  return String(value || "")
    .trim()
    .replace(/^#/, "");
}

// GateOverrideDialog is the explicit confirmation modal required whenever an
// operator holding merge:force chooses to merge past an unsatisfied review
// gate (security-review or review). It names every skipped gate and its
// current state, requires typing the pull request number and a non-empty
// audit reason, and posts overrideGates explicitly to /force-merge.
export default function GateOverrideDialog({ open, onClose, t, onSubmit }) {
  const [reason, setReason] = useState("");
  const [typedPR, setTypedPR] = useState("");
  const [submitting, setSubmitting] = useState(false);

  if (!open || !t) return null;

  const allGates = deriveMergeGates(t);
  const reviewSkipped = unsatisfiedReviewGates(t);
  const skippedGates = allGates.filter(
    (g) =>
      g.state !== "passed" &&
      g.state !== "overridden" &&
      (g.name === "security-review" ||
        g.name === "review" ||
        g.name === "ci" ||
        g.name === "approval"),
  );
  const overrideGateNames = reviewSkipped.map((g) => g.name);
  const expectedPR = pullRequestNumber(t.pullRequest);
  const prMatches = !expectedPR || normalizeTypedPR(typedPR) === expectedPR;
  const trimmedReason = reason.trim();
  const canConfirm = Boolean(trimmedReason) && prMatches && !submitting;
  const actionLabel = overrideReviewButtonLabel(reviewSkipped);
  const head = t.ci?.commit || undefined;

  const handleClose = () => {
    if (submitting) return;
    setReason("");
    setTypedPR("");
    onClose?.();
  };

  const handleConfirm = async (e) => {
    e?.preventDefault();
    if (!canConfirm) return;
    setSubmitting(true);
    try {
      await onSubmit?.(() =>
        api(`/api/tasks/${t.id}/force-merge`, {
          method: "POST",
          body: JSON.stringify({
            reason: trimmedReason,
            head,
            overrideGates: overrideGateNames,
          }),
        }),
      );
      setReason("");
      setTypedPR("");
      onClose?.();
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={handleClose}
      maxWidth="sm"
      fullWidth
      data-testid="gate-override-dialog"
    >
      <form onSubmit={handleConfirm}>
        <DialogTitle sx={{ pb: 1 }}>{actionLabel}</DialogTitle>
        <DialogContent>
          <Stack spacing={1.5}>
            <Alert severity="warning" sx={{ fontSize: "0.82rem" }}>
              This bypasses required merge gates on{" "}
              <strong>{t.pullRequest || `task ${t.id}`}</strong> and merges
              immediately. Your name, the skipped gates, and your reason are
              recorded on the task, on the pull request, in the Inbox, and in
              the audit trail. Any review still running continues against the
              merged commit on the base branch and files follow-up fix tasks for
              any findings.
            </Alert>

            <Box>
              <Typography
                variant="subtitle2"
                sx={{ fontSize: "0.82rem", mb: 0.5 }}
              >
                Gates bypassed by this override:
              </Typography>
              <Box
                component="ul"
                data-testid="override-skipped-gates"
                sx={{ m: 0, pl: 2.5, fontSize: "0.82rem" }}
              >
                {skippedGates.map((g) => (
                  <li key={g.name} data-testid={`override-gate-${g.name}`}>
                    <strong>{formatGateStatus(g)}</strong>
                    {g.detail ? ` — ${g.detail}` : ""}
                  </li>
                ))}
              </Box>
            </Box>

            {head && (
              <Typography variant="caption" color="text.secondary">
                Applies to commit <code>{head.slice(0, 12)}</code> only; if new
                commits are pushed before the merge lands, grain withdraws the
                override.
              </Typography>
            )}

            {expectedPR && (
              <TextField
                size="small"
                fullWidth
                required
                label={`Confirm pull request number (#${expectedPR})`}
                placeholder={`#${expectedPR}`}
                value={typedPR}
                onChange={(e) => setTypedPR(e.target.value)}
                slotProps={{
                  htmlInput: {
                    "aria-label": `Confirm pull request number (#${expectedPR})`,
                    "data-testid": "override-pr-input",
                  },
                }}
                helperText={`Type ${expectedPR} or #${expectedPR} to confirm which pull request you are overriding.`}
              />
            )}

            <TextField
              size="small"
              fullWidth
              required
              multiline
              minRows={2}
              label="Reason for overriding merge gates (required)"
              placeholder="Explain why this change must land without waiting for the review gates…"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              slotProps={{
                htmlInput: {
                  "aria-label": "Reason for overriding merge gates",
                  "data-testid": "override-reason-input",
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
            color="warning"
            disabled={!canConfirm}
            data-testid="confirm-gate-override"
          >
            {actionLabel}
          </Button>
        </DialogActions>
      </form>
    </Dialog>
  );
}
