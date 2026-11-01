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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import { formatSeconds } from "../metricsFormat.js";

// GitHubBudgetPanel is System → Health's reading of GitHub's hourly REST
// budget (docs/github-api-rate-limits.md §4): what is left, how fast it
// is falling, when it runs out at that rate, and which part of grain is
// spending it. One read of GET /api/metrics?section=github, which costs
// the store nothing -- the counts live in the daemon's memory.
//
// Nothing is shown until a GitHub response has reported a budget: a
// deployment that has made no calls has nothing to say here.
export default function GitHubBudgetPanel({ sx }) {
  const [github, setGitHub] = useState(null);
  // Re-read while Health is open, one read at a time, as the sandbox
  // section below it does: read once, the calls left, the projection
  // and the idle-poll backoff sat at the moment the page was opened
  // while everything around them moved. A failed read keeps the last
  // figures rather than blanking the panel.
  useEffect(() => {
    let live = true;
    let timer = null;
    const tick = async () => {
      try {
        const r = await api("/api/metrics?section=github");
        if (live) setGitHub(r?.github || null);
      } catch {
        // Keep what was last read.
      }
      if (live) timer = setTimeout(tick, BUDGET_REFRESH_MS);
    };
    tick();
    return () => {
      live = false;
      if (timer) clearTimeout(timer);
    };
  }, []);
  return <GitHubBudgetView github={github} sx={sx} />;
}

// BUDGET_REFRESH_MS is how often an open Health page re-reads the
// budget: often enough to follow a reconcile cycle's spend and a
// webhook's backoff reset, and it is a cheap in-memory read.
export const BUDGET_REFRESH_MS = 10000;

const shownCallers = 6;

// secondsUntil is how far ahead an RFC 3339 time is, or null.
function secondsUntil(at, now) {
  if (!at || at.startsWith("0001-")) return null;
  const s = (Date.parse(at) - now) / 1000;
  return Number.isFinite(s) ? Math.max(0, s) : null;
}

export function GitHubBudgetView({ github, now = Date.now(), sx }) {
  const budget = github?.budget;
  if (!github?.enabled || !budget?.limit) return null;
  const resets = secondsUntil(budget.resetsAt, now);
  const runsOut = secondsUntil(budget.exhaustedAt, now);
  const severity = budget.low ? "error" : runsOut != null ? "warning" : "info";
  const parts = [
    `${budget.remaining.toLocaleString()} of ${budget.limit.toLocaleString()} calls left this hour`,
  ];
  if (resets != null) parts.push(`resets in ${formatSeconds(resets)}`);
  if (budget.spendPerHour > 0)
    parts.push(
      `spending ${Math.round(budget.spendPerHour).toLocaleString()}/h`,
    );
  const callers = (github.callers || []).slice(0, shownCallers);
  const polls = github.polls?.tracked ? github.polls : budget.polls;
  return (
    <Box sx={sx} data-testid="health-github-budget">
      <Alert severity={severity} sx={{ mb: 1 }}>
        <Typography variant="body2" component="div">
          GitHub API: {parts.join(" · ")}.
        </Typography>
        {runsOut != null && (
          <Typography
            variant="body2"
            component="div"
            data-testid="health-github-runs-out"
          >
            At that rate it runs out in {formatSeconds(runsOut)}, before it
            resets.
          </Typography>
        )}
        {polls?.tracked > 0 && (
          <Typography
            variant="body2"
            component="div"
            data-testid="health-github-polls"
          >
            Idle-poll backoff: {(polls.backedOff || 0).toLocaleString()} of{" "}
            {polls.tracked.toLocaleString()} tracked{" "}
            {polls.tracked === 1 ? "PR" : "PRs"} backed off · poll interval
            median {formatSeconds(polls.medianSeconds || 0)}, max{" "}
            {formatSeconds(polls.maxSeconds || 0)}
            {polls.baseSeconds && polls.capSeconds
              ? ` (${formatSeconds(polls.baseSeconds)} base, ${formatSeconds(polls.capSeconds)} cap)`
              : ""}
            .
          </Typography>
        )}
        {budget.low && (
          <Typography variant="body2" component="div">
            Under {Math.round((budget.lowFraction || 0.15) * 100)}% left:
            polling of idle pull requests, stale branches and path triggers is
            cut back. The merge queue and base-branch CI are not.
          </Typography>
        )}
      </Alert>
      {callers.length > 0 && (
        <Box sx={{ overflowX: "auto" }}>
          <Table size="small" data-testid="health-github-callers">
            <TableHead>
              <TableRow>
                <TableCell>Caller</TableCell>
                <TableCell align="right">Spent</TableCell>
                <TableCell align="right">Free (304)</TableCell>
                <TableCell sx={{ display: { xs: "none", sm: "table-cell" } }}>
                  Mostly
                </TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {callers.map((c) => (
                <TableRow key={c.caller}>
                  <TableCell>{c.caller}</TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {c.spent.toLocaleString()}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {c.revalidated.toLocaleString()}
                  </TableCell>
                  <TableCell sx={{ display: { xs: "none", sm: "table-cell" } }}>
                    {(c.kinds || [])
                      .slice(0, 2)
                      .map((k) => `${k.kind} (${k.spent.toLocaleString()})`)
                      .join(", ")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Box>
      )}
    </Box>
  );
}
