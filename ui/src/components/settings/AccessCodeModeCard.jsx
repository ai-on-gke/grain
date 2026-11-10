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

import { Alert, Box, Chip, Stack, Typography } from "@mui/material";
import CopyButton from "../CopyButton.jsx";
import SettingsCard from "./SettingsCard.jsx";

const MODE_LABEL = {
  on: "On",
  recovery: "Recovery",
  off: "Off",
};

// modeChipColor is the chip colour for a mode: on is the one to move
// away from once people sign in with a verified identity.
function modeChipColor(view) {
  if (view.mode === "off") return "default";
  if (view.mode === "recovery") {
    return view.recoveryInForce ? "success" : "warning";
  }
  return "warning";
}

// modeSummary says what the mode means for whoever holds the code.
function modeSummary(view) {
  switch (view.mode) {
    case "recovery":
      return view.recoveryInForce
        ? "The access code only manages access and settings. Each use raises an Inbox notification and is listed in the access history below."
        : "Recovery is set but not yet in force: nobody with a full role on the allowlist can sign in through a verifier, so the code still works everywhere as the way in.";
    case "off":
      return "The access code is refused everywhere: the UI, the CLI and every session. Only a verified identity signs in.";
    default:
      return "The access code is an everyday credential: whoever holds it can do anything on this deployment.";
  }
}

// AccessCodeModeCard is Settings → Access's access-code card: the mode
// GET /api/access/access-code reports, a recommendation to switch to
// recovery once the viewer has signed in with a verified identity, and
// the command on the daemon's host that changes it. The mode is
// read-only here -- only that command writes it, so a code holder on the
// network cannot switch the code back on (docs/access-code.md).
export default function AccessCodeModeCard({ view }) {
  if (!view?.mode) return null;
  const recommend = view.mode === "on" && view.viewerVerified;
  const command = view.mode === "on" ? view.command : view.onCommand;
  return (
    <SettingsCard title="Access code" anchor="access-code-mode">
      <Box data-testid="access-code-mode-card">
        <Stack
          direction="row"
          alignItems="center"
          spacing={1}
          sx={{ mt: 1, flexWrap: "wrap", rowGap: 0.5 }}
        >
          <Typography variant="body2" color="text.secondary">
            Mode:
          </Typography>
          <Chip
            size="small"
            label={MODE_LABEL[view.mode] || view.mode}
            color={modeChipColor(view)}
            data-testid="access-code-mode"
            sx={{ fontWeight: 600 }}
          />
          {view.mode === "recovery" ? (
            <Typography
              variant="body2"
              color="text.secondary"
              data-testid="access-code-recovery-state"
            >
              {view.recoveryInForce ? "in force" : "not yet in force"}
            </Typography>
          ) : null}
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          {modeSummary(view)}
        </Typography>
        {recommend ? (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="access-code-recommend-recovery"
          >
            You are signed in with a verified identity, so the access code no
            longer needs to be an everyday credential. Switch it to{" "}
            <strong>recovery</strong>: it keeps working as a way back in to
            manage access and settings, and each use raises an Inbox
            notification.
          </Alert>
        ) : view.mode === "on" ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Once a person with a full role signs in with a verified identity,
            switching the code to <strong>recovery</strong> is recommended.
          </Typography>
        ) : null}
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5 }}>
          {view.mode === "on"
            ? "Only the daemon's own host changes the mode. Run this there:"
            : "Only the daemon's own host changes the mode. To make the code an everyday credential again, run this there:"}
        </Typography>
        <Stack
          direction="row"
          alignItems="flex-start"
          spacing={0.5}
          sx={{ mt: 0.5 }}
        >
          <Box
            component="pre"
            data-testid="access-code-mode-command"
            sx={{
              flex: 1,
              minWidth: 0,
              m: 0,
              p: 1,
              borderRadius: 1,
              bgcolor: "action.hover",
              fontFamily: "monospace",
              fontSize: "0.85em",
              whiteSpace: "pre-wrap",
              overflowWrap: "anywhere",
            }}
          >
            {command}
          </Box>
          <CopyButton text={command} label="Copy command" />
        </Stack>
      </Box>
    </SettingsCard>
  );
}
