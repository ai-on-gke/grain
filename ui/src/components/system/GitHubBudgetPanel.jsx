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
  useEffect(() => {
    let live = true;
    api("/api/metrics?section=github")
      .then((r) => live && setGitHub(r?.github || null))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);
  return <GitHubBudgetView github={github} sx={sx} />;
}

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
