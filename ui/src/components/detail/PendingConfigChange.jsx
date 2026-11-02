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
// approve its own change.
//
// t.pendingConfigChange is nil for almost every task, and this renders
// nothing for those.
export default function PendingConfigChange({ t, act }) {
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = t.pendingConfigChange;
  if (!pending) return null;

  const validation = pending.validation || {};
  const files = pending.files || [];
  const warnings = [
    ...(validation.warnings || []),
    ...(validation.liveness || []),
  ];
  const short = (pending.headSha || "").slice(0, 12);

  const decide = async (verb, body) => {
    setBusy(true);
    try {
      await act(
        () =>
          api(`/api/tasks/${t.id}/config-change/${verb}`, {
            method: "POST",
            body: JSON.stringify({ headSha: pending.headSha, ...body }),
          }),
        t.id,
      );
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
          Validation failed: {validation.error || "unknown error"}. This change
          cannot be approved as it stands.
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
          <Button
            type="button"
            variant="contained"
            color="success"
            disabled={busy || !validation.ok}
            onClick={() => decide("approve", {})}
          >
            Approve &amp; Apply
          </Button>
          <Button
            type="button"
            variant="outlined"
            color="error"
            disabled={busy}
            onClick={() => setRejecting(true)}
          >
            Reject
          </Button>
        </Stack>
      )}
    </Alert>
  );
}
