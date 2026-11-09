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
  Chip,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import AccessGate from "../AccessGate.jsx";
import DiffView from "../DiffView.jsx";

// PendingConfigChange is what a session's propose_config_change call
// turns into for the person sitting with it
// (design/design/config-repo-settings.design.md, section 4.3): the change it
// pushed to a branch of grain/config, the files it touches, what the
// pre-review validation said, the whole diff, and the two answers.
//
// Approve & Apply merges exactly the commit shown here (headSha) into
// grain/config's main and applies it before the session hears back;
// the daemon refuses with a 409 if the branch moved since this was
// rendered. Reject deletes the branch and hands the session the reason.
// Either posts the decision into the conversation, which is what wakes
// the session. Both endpoints answer a person only -- an agent cannot
// approve its own change -- and only one whose role holds settings:write
// (pkg/ui/access_roles.go), so a role without it is shown both buttons
// disabled, with the missing scope named (AccessGate), rather than
// offered a decision the server refuses
// (docs/test-runs/2026-11-09-demo-config-change.md, finding 2).
//
// t.pendingConfigChange is nil for almost every task, and this renders
// nothing for those.
const INVALID_PREFIX = "the config change does not pass validation: ";

// refusalValidation builds a failed ConfigChangeValidation from approve's
// 422 error so the card drops its proposal-time "Schema valid" verdict at
// once -- App's act() does not re-fetch the task when the mutation throws,
// while pkg/ui/task_config_change.go persists the same refusal on the task
// for subsequent polls and reloads.
function refusalValidation(err) {
  const raw = String(err?.message || "").trim();
  const error = raw.startsWith(INVALID_PREFIX)
    ? raw.slice(INVALID_PREFIX.length)
    : raw || "unknown error";
  const report = err?.validation || {};
  const check = report.check || {};
  return {
    ok: false,
    stale: true,
    error,
    warnings: report.warnings || check.warnings || check.Warnings || [],
    liveness: report.liveness || [],
  };
}

export default function PendingConfigChange({ t, act }) {
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [refused, setRefused] = useState(null);
  const pending = t.pendingConfigChange;
  if (!pending) return null;

  const stored = pending.validation || {};
  const validation =
    stored.ok === false
      ? stored
      : refused?.headSha === pending.headSha
        ? refused.validation
        : stored;
  const files = pending.files || [];
  const warnings = [
    ...(validation.warnings || []),
    ...(validation.liveness || []),
  ];
  const liveness = validation.liveness || [];
  const short = (pending.headSha || "").slice(0, 12);

  const decide = async (verb, body) => {
    setBusy(true);
    try {
      await act(async () => {
        try {
          return await api(`/api/tasks/${t.id}/config-change/${verb}`, {
            method: "POST",
            body: JSON.stringify({ headSha: pending.headSha, ...body }),
          });
        } catch (err) {
          if (verb === "approve" && err?.status === 422) {
            setRefused({
              headSha: pending.headSha,
              validation: refusalValidation(err),
            });
          }
          throw err;
        }
      }, t.id);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Alert
      severity="warning"
      icon={false}
      sx={{ mb: 2, "& .MuiAlert-message": { width: "100%", minWidth: 0 } }}
      role="region"
      aria-label="Config change waiting for your approval"
      className="pending-config-change"
    >
      <Typography variant="subtitle2" component="div">
        Config change waiting for your approval
      </Typography>
      <Typography variant="body2" sx={{ mt: 0.5, fontWeight: 600 }}>
        {pending.summary}
      </Typography>
      {pending.description && (
        <Typography variant="body2" sx={{ mt: 0.5, whiteSpace: "pre-wrap" }}>
          {pending.description}
        </Typography>
      )}
      <Typography
        variant="caption"
        component="div"
        sx={{ mt: 0.5, overflowWrap: "anywhere" }}
      >
        <code>{pending.branch}</code> at <code>{short}</code>
      </Typography>

      <Stack
        direction="row"
        spacing={0.5}
        useFlexGap
        flexWrap="wrap"
        sx={{ mt: 1 }}
        aria-label="Files touched"
      >
        {files.map((file) => (
          <Chip
            key={file}
            size="small"
            label={file}
            component="a"
            clickable
            href={`/config?path=${encodeURIComponent(file)}`}
          />
        ))}
      </Stack>

      {validation.ok ? (
        <Alert
          severity="success"
          sx={{ mt: 1 }}
          data-testid="config-change-validation"
        >
          Schema valid · Capability catalogue consistent · Dispatch liveness OK
          · Dry-run apply succeeded
          {warnings.length > 0 && (
            <Box component="ul" sx={{ m: 0, pl: 2 }}>
              {warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </Box>
          )}
        </Alert>
      ) : (
        <Alert
          severity="error"
          sx={{ mt: 1 }}
          data-testid="config-change-validation"
        >
          {validation.stale ? (
            <>
              Stored validation no longer holds:{" "}
              {validation.error || "unknown error"}. Reject this change to ask
              the session to rebase and propose it again.
            </>
          ) : (
            <>
              Validation failed: {validation.error || "unknown error"}. This
              change cannot be approved as it stands; Reject it to ask the
              session to revise it.
            </>
          )}
          {liveness.length > 0 && (
            <Box component="ul" sx={{ m: 0, mt: 0.5, pl: 2 }}>
              {liveness.map((l) => (
                <li key={l}>{l}</li>
              ))}
            </Box>
          )}
        </Alert>
      )}

      <Box sx={{ mt: 1, minWidth: 0 }}>
        <DiffView
          diff={pending.diff || ""}
          label="Proposed config change"
          maxHeight={480}
        />
      </Box>

      {rejecting ? (
        <Stack spacing={1} sx={{ mt: 1 }}>
          <TextField
            size="small"
            fullWidth
            multiline
            minRows={2}
            label="Why are you rejecting it?"
            placeholder={
              validation.stale
                ? "Rebase onto origin/main and propose the change again."
                : undefined
            }
            value={reason}
            onChange={(evt) => setReason(evt.target.value)}
          />
          <Stack direction="row" spacing={1}>
            <Button
              type="button"
              variant="contained"
              color="error"
              disabled={busy}
              onClick={() => decide("reject", { reason })}
            >
              Reject change
            </Button>
            <Button
              type="button"
              disabled={busy}
              onClick={() => setRejecting(false)}
            >
              Cancel
            </Button>
          </Stack>
        </Stack>
      ) : (
        <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
          <AccessGate scope="settings:write">
            <Button
              type="button"
              variant="contained"
              color="success"
              disabled={busy || !validation.ok}
              onClick={() => decide("approve", {})}
            >
              Approve &amp; Apply
            </Button>
          </AccessGate>
          <AccessGate scope="settings:write">
            <Button
              type="button"
              variant="outlined"
              color="error"
              disabled={busy}
              onClick={() => setRejecting(true)}
            >
              Reject
            </Button>
          </AccessGate>
        </Stack>
      )}
    </Alert>
  );
}
