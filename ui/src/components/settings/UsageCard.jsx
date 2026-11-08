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

import {
  Alert,
  Box,
  Button,
  MenuItem,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { useCallback, useContext, useEffect, useState } from "react";
import api from "../../api.js";
import { formatMoment } from "../../time.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { SettingsReadOnlyContext } from "./SettingsCard.jsx";

// formatTokens renders a token count compactly: 950, 12.3k, 4.5M.
export function formatTokens(n) {
  const v = Number(n) || 0;
  if (v >= 1e9) return `${(v / 1e9).toFixed(1)}G`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e4) return `${Math.round(v / 1e3)}k`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}k`;
  return String(Math.round(v));
}

const MODES = [
  { value: "advisory", label: "Advisory: estimate only" },
  { value: "auto", label: "Automatic: cap workers at the estimate" },
  { value: "off", label: "Off" },
];

// UsageCard is Settings → Models' token usage and budgets (GET
// /api/usage, docs/connections-and-models.md "Token usage and budgets"):
// per connection and model, what the current window used, the limit
// (configured, or learned from a provider's refusal), what is left, the
// next reset, the burn rate and the workers the remainder sustains; the
// deployment rollup; the advisory estimate and default-model warnings;
// and the concurrency mode (PUT /api/usage/settings). Budgets are set
// with `grain usage -budget` or the API.
export default function UsageCard({ showError }) {
  const readOnly = useContext(SettingsReadOnlyContext);
  const zone = useTimeZone();
  const [report, setReport] = useState(null);
  const [failed, setFailed] = useState("");
  const [mode, setMode] = useState("advisory");
  const [minWorkers, setMinWorkers] = useState("");
  const [saving, setSaving] = useState(false);

  const adopt = useCallback((r) => {
    setReport(r);
    setMode(r?.settings?.mode || "advisory");
    setMinWorkers(r?.settings?.minWorkers ? String(r.settings.minWorkers) : "");
  }, []);

  useEffect(() => {
    let live = true;
    api("/api/usage")
      .then((r) => live && adopt(r))
      .catch((err) => live && setFailed(err?.message || String(err)));
    return () => {
      live = false;
    };
  }, [adopt]);

  const save = async () => {
    setSaving(true);
    try {
      const next = {
        ...(report?.settings || {}),
        mode,
        minWorkers: minWorkers === "" ? 0 : Number(minWorkers),
      };
      adopt(
        await api("/api/usage/settings", {
          method: "PUT",
          body: JSON.stringify(next),
        }),
      );
    } catch (err) {
      showError?.(err);
    } finally {
      setSaving(false);
    }
  };

  if (failed) {
    return (
      <Alert severity="warning" data-testid="usage-unavailable">
        Token usage is unavailable: {failed}
      </Alert>
    );
  }
  if (!report) {
    return (
      <Typography variant="body2" color="text.secondary">
        Reading token usage…
      </Typography>
    );
  }
  const rollup = report.rollup || {};
  const scopes = report.scopes || [];
  const dirty =
    mode !== (report.settings?.mode || "advisory") ||
    (minWorkers === "" ? 0 : Number(minWorkers)) !==
      (report.settings?.minWorkers || 0);

  return (
    <Stack spacing={1.5} data-testid="usage-card">
      <Typography variant="body2" color="text.secondary">
        What runs spent at each provider, as their CLIs reported it, and what
        each connection&apos;s budget has left. A usage-limit pause is still
        what stops dispatch onto a spent model; these figures only estimate.
      </Typography>
      <Box data-testid="usage-rollup">
        <Typography variant="body2">
          <strong>{rollup.runs || 0}</strong>{" "}
          {rollup.runs === 1 ? "run" : "runs"} recorded,{" "}
          <strong>{formatTokens(rollup.billable)}</strong> billable tokens (
          {formatTokens(rollup.inputTokens)} in,{" "}
          {formatTokens(rollup.outputTokens)} out,{" "}
          {formatTokens(rollup.cacheReadTokens)} cache read)
          {rollup.costUsd
            ? `, $${rollup.costUsd.toFixed(2)} reported`
            : ""}; {formatTokens(rollup.burnPerHour)}/h in the last hour.
        </Typography>
        <Typography variant="body2" data-testid="usage-workers">
          Worker cap <strong>{report.effectiveWorkers}</strong> of max_workers{" "}
          {report.maxWorkers}
          {report.sustainableWorkers != null
            ? `; the remaining budget sustains ≈ ${report.sustainableWorkers}`
            : "; nothing bounds the estimate (a connection in use has no known budget, or no runs to estimate from)"}
          .
        </Typography>
      </Box>
      {scopes.length > 0 ? (
        <TableContainer sx={{ overflowX: "auto" }}>
          <Table
            size="small"
            data-testid="usage-table"
            sx={{ "& th, & td": { whiteSpace: "nowrap" } }}
          >
            <TableHead>
              <TableRow>
                <TableCell>Connection</TableCell>
                <TableCell>Model</TableCell>
                <TableCell align="right">Used</TableCell>
                <TableCell align="right">Limit</TableCell>
                <TableCell align="right">Left</TableCell>
                <TableCell>Reset</TableCell>
                <TableCell align="right">Burn/h</TableCell>
                <TableCell align="right">Sustains</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {scopes.map((s) => (
                <TableRow
                  key={`${s.connection}/${s.model || ""}`}
                  data-testid="usage-row"
                >
                  <TableCell>{s.connectionName || s.connection}</TableCell>
                  <TableCell>{s.model || "All models"}</TableCell>
                  <TableCell align="right">{formatTokens(s.used)}</TableCell>
                  <TableCell
                    align="right"
                    title={
                      s.limitSource === "learned"
                        ? "Learned from the usage the window had consumed when its provider refused it"
                        : undefined
                    }
                  >
                    {s.limit
                      ? `${formatTokens(s.limit)}${s.limitSource === "learned" ? "~" : ""}`
                      : "—"}
                  </TableCell>
                  <TableCell align="right">
                    {s.pausedUntil
                      ? "Paused"
                      : s.limit
                        ? formatTokens(s.remaining)
                        : "—"}
                  </TableCell>
                  <TableCell>
                    {s.resetAt ? formatMoment(s.resetAt, zone) : "—"}
                  </TableCell>
                  <TableCell align="right">
                    {formatTokens(s.burnPerHour)}
                  </TableCell>
                  <TableCell align="right">
                    {s.sustainableWorkers != null ? s.sustainableWorkers : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : (
        <Typography variant="body2" color="text.secondary">
          No run has reported token usage yet.
        </Typography>
      )}
      {(report.advisories || []).map((a) => (
        <Alert severity="info" key={a} data-testid="usage-advisory">
          {a}
        </Alert>
      ))}
      {(report.warnings || []).map((w) => (
        <Alert severity="warning" key={w} data-testid="usage-warning">
          {w}
        </Alert>
      ))}
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={1.5}
        alignItems={{ xs: "stretch", sm: "center" }}
      >
        <TextField
          select
          size="small"
          label="Concurrency"
          value={mode}
          onChange={(e) => setMode(e.target.value)}
          disabled={readOnly}
          sx={{ minWidth: 280 }}
          inputProps={{ "data-testid": "usage-mode" }}
        >
          {MODES.map((m) => (
            <MenuItem key={m.value} value={m.value}>
              {m.label}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          size="small"
          type="number"
          label="Fewest workers"
          placeholder="1"
          value={minWorkers}
          onChange={(e) => setMinWorkers(e.target.value)}
          disabled={readOnly || mode !== "auto"}
          sx={{ width: 160 }}
          inputProps={{ min: 0 }}
        />
        <Box>
          <Button
            variant="contained"
            onClick={save}
            disabled={readOnly || saving || !dirty}
          >
            Save
          </Button>
        </Box>
      </Stack>
      <Typography variant="caption" color="text.secondary">
        Automatic mode lowers the worker cap to the estimate, never below the
        fewest workers and never above max_workers. Set budgets with{" "}
        <code>grain usage -budget connection/model=tokens/window</code>.
      </Typography>
    </Stack>
  );
}
