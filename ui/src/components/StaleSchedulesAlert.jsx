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
import { Alert, AlertTitle, Box, Link, Typography } from "@mui/material";
import api from "../api.js";
import { processPath } from "../paths.js";
import { formatDateTime } from "../time.js";
import { useTimeZone } from "../TimeZoneContext.jsx";

// staleScheduleRepo is a StaleSchedule's repo as written everywhere else:
// owner/name, or host/owner/name off the default host. The API sends a
// model.RepoRef, which has no text form of its own on the wire.
export function staleScheduleRepo(repo) {
  if (!repo) return "";
  if (typeof repo === "string") return repo;
  const owner = repo.Owner ?? repo.owner ?? "";
  const name = repo.Name ?? repo.name ?? "";
  if (!owner && !name) return "";
  const host = repo.Host ?? repo.host ?? "";
  return host ? `${host}/${owner}/${name}` : `${owner}/${name}`;
}

// formatCadence is a Go time.Duration, in nanoseconds, said the way a
// schedule's cadence is thought of: every 1d, every 6h, every 15m.
export function formatCadence(ns) {
  const minutes = Math.round((Number(ns) || 0) / 6e10);
  if (minutes <= 0) return "—";
  if (minutes % 1440 === 0) return `${minutes / 1440}d`;
  if (minutes % 60 === 0) return `${minutes / 60}h`;
  if (minutes > 60) return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  return `${minutes}m`;
}

export const STALE_SCHEDULES_REFRESH_MS = 30000;

// useStaleSchedules is GET /api/sandboxes?section=staleSchedules, re-read
// while the page is open: a store read, without the sandbox sweep the
// rest of that report costs. A failed read keeps what was last read, and
// a reader without deployment:read simply has none to show.
export function useStaleSchedules() {
  const [schedules, setSchedules] = useState([]);
  useEffect(() => {
    let live = true;
    let timer = null;
    const tick = async () => {
      try {
        const r = await api("/api/sandboxes?section=staleSchedules");
        if (live) setSchedules(r?.staleSchedules || []);
      } catch {
        // Keep what was last read.
      }
      if (live) timer = setTimeout(tick, STALE_SCHEDULES_REFRESH_MS);
    };
    tick();
    return () => {
      live = false;
      if (timer) clearTimeout(timer);
    };
  }, []);
  return schedules;
}

// StaleSchedulesAlert is every enabled schedule -- a workflow's cron
// included -- that has gone more than twice its cadence without a firing
// that ran anything (model.Store.StaleSchedules, grain/task-4344). A
// cron that comes due and is dropped every time looks exactly like one
// that is fine from everywhere else: the live model suites went two
// weeks dark that way with nothing saying so.
//
// An error rather than a warning, as `grain health` prints it under
// FAILING: whatever the schedule was there to run has not been run.
// `repo`, given, narrows it to the schedules on that repository, which
// is how the CI pane shows it beside that repository's runs.
export default function StaleSchedulesAlert({ schedules, repo = "", sx }) {
  const zone = useTimeZone();
  const want = repo.trim().toLowerCase();
  const shown = (Array.isArray(schedules) ? schedules : []).filter(
    (s) => !want || staleScheduleRepo(s.repo).toLowerCase() === want,
  );
  if (shown.length === 0) return null;
  return (
    <Alert
      severity="error"
      data-testid="stale-schedules"
      sx={{ mb: "1rem", overflowWrap: "anywhere", ...sx }}
    >
      <AlertTitle sx={{ mb: 0.5 }}>
        {shown.length === 1
          ? "A schedule has stopped running"
          : `${shown.length} schedules have stopped running`}
      </AlertTitle>
      <Typography variant="body2" sx={{ mb: 0.75 }}>
        No firing that ran anything in more than twice its cadence.
      </Typography>
      <Box
        component="ul"
        sx={{ m: 0, pl: 0, listStyle: "none", display: "grid", gap: 0.75 }}
      >
        {shown.map((s) => {
          const where = staleScheduleRepo(s.repo);
          return (
            <Box
              component="li"
              key={s.id}
              data-testid="stale-schedule"
              data-schedule={s.id}
            >
              <Typography variant="body2" component="div">
                <Link
                  href={processPath("schedule", s.id)}
                  underline="hover"
                  sx={{ fontWeight: 600 }}
                  data-testid="stale-schedule-name"
                >
                  {s.title || s.id}
                </Link>
                {s.workflow && (
                  <>
                    {" · workflow "}
                    <code>{s.workflow}</code>
                  </>
                )}
                {where && !want && ` · ${where}`}
                {` · every ${formatCadence(s.cadence)}`}
                {" · last ran "}
                {s.lastRunAt ? formatDateTime(s.lastRunAt, zone) : "never"}
              </Typography>
              {s.reason && (
                <Typography
                  variant="body2"
                  component="div"
                  sx={{ opacity: 0.85 }}
                  data-testid="stale-schedule-reason"
                >
                  {s.reason}
                </Typography>
              )}
            </Box>
          );
        })}
      </Box>
    </Alert>
  );
}
