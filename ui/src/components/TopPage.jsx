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

import { useCallback, useEffect, useRef, useState } from "react";
import { PageHeaderActions, PageHeaderPageActions } from "./PageHeader.jsx";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  Typography,
} from "@mui/material";
import api from "../api.js";

// REFRESH_MS is LogsPage's own cadence, for the same reason: every panel
// of the System overlay should feel like the same kind of live view. It is
// also about as often as `top` itself redraws when an operator runs it in
// a terminal.
const REFRESH_MS = 5000;

// Fetch the endpoint's full allowance. The view scrolls, and a busy host can
// have substantially more than fifty processes worth inspecting.
const LINES_TO_FETCH = 500;

// TopPage is the Top tab of SystemOverlay.jsx: `top` on the machine the
// daemon itself runs on, over GET /api/host/top (grain/task-120).
//
// It sits beside Sandbox health rather than inside it because it answers
// the next question rather than the same one. That panel's host section
// is aggregate -- load average, memory, disk -- and says the machine is
// under pressure without ever being able to say by what. This is the
// per-process view an operator would otherwise open an SSH session for.
//
// Auto-refresh is a checkbox the operator can clear, which the Logs pane
// has no equivalent of and needs none: a log is append-only, so a poll
// only ever adds lines below what was already read, while every poll here
// re-sorts the whole table under the cursor -- which is exactly what one
// does not want while reading a row. Each poll costs a `top` run on the
// daemon's own machine besides.
//
// GET /api/host/top's own "enabled" flag says whether this deployment has
// a reader configured at all; when it doesn't (`grain demo`'s throwaway
// UI, or an image without procps) this shows a note rather than a pane
// that could only ever error, the same convention LogsPage and
// SandboxHealthPage already follow.
export default function TopPage({ showError }) {
  const [data, setData] = useState(null);
  const [auto, setAuto] = useState(true);
  // Why the last read failed, or "" once one succeeds. Without it a tab
  // whose first read failed rendered nothing at all once the snackbar
  // faded, and one whose polls started failing went on showing the last
  // table as if it were current.
  const [loadError, setLoadError] = useState("");
  // Whether the reads are failing right now, so a run of failures is
  // reported once rather than in a new snackbar every REFRESH_MS.
  const failing = useRef(false);

  const refresh = useCallback(async () => {
    try {
      setData(await api(`/api/host/top?lines=${LINES_TO_FETCH}`));
      setLoadError("");
      failing.current = false;
    } catch (err) {
      setLoadError(err?.message || String(err));
      if (!failing.current) showError(err);
      failing.current = true;
    }
  }, [showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // The poll stops while auto-refresh is off, and never starts at all on
  // a deployment with no reader configured -- there is nothing behind the
  // call there but a repeated "not available".
  const enabled = data !== null && data.enabled;
  useEffect(() => {
    if (!auto || !enabled) return undefined;
    const interval = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(interval);
  }, [auto, enabled, refresh]);

  if (data === null && !loadError) return null;

  return (
    <section className="top-panel">
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Top
      </Typography>
      {data === null && (
        <Alert
          severity="error"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              Refresh
            </Button>
          }
        >
          The process snapshot could not be read ({loadError}).
        </Alert>
      )}
      {data !== null && loadError && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The latest read failed ({loadError}), so the table below is from the
          last one that succeeded.
        </Alert>
      )}
      {data !== null && !data.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Not available: this deployment has no process snapshot configured.
        </Alert>
      )}
      {data?.enabled && (
        <>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            Processes on the machine the daemon runs on, busiest first.
          </Typography>
          {/* Auto-refresh and Refresh are the page's actions, in its
              header rail (PageHeader.jsx, grain/task-4915). */}
          <PageHeaderActions>
            <Box className="logs-toolbar">
              <PageHeaderPageActions>
                <FormControlLabel
                  control={
                    <Checkbox
                      size="small"
                      checked={auto}
                      onChange={(e) => setAuto(e.target.checked)}
                    />
                  }
                  label="Auto-refresh"
                />
                <Button size="small" variant="outlined" onClick={refresh}>
                  Refresh
                </Button>
              </PageHeaderPageActions>
            </Box>
          </PageHeaderActions>
          <pre className="logs-view top-view">
            {data.lines && data.lines.length > 0
              ? data.lines.join("\n")
              : "(no output)"}
          </pre>
        </>
      )}
    </section>
  );
}
