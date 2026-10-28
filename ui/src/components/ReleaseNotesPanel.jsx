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

import { useCallback, useEffect, useState } from "react";
import {
  Box,
  Chip,
  Divider,
  MenuItem,
  Select,
  Stack,
  Typography,
} from "@mui/material";
import api from "../api.js";
import Markdown from "./Markdown.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { releaseNoteAuthor, weekLabel } from "../releaseNotes.js";
import ReleaseVersions from "./ReleaseVersions.jsx";

// Release notes, of two kinds that share a heading and nothing else:
//
//   - Weekly: what this deployment shipped, a week at a time
//     (model.ReleaseNote), written by grain's release-notes job.
//   - Versions: what each grain release brought, as committed to
//     docs/releases by the release cut and embedded in this binary
//     (ReleaseVersions.jsx, pkg/releasenotes) -- so they show with no
//     access to the grain repository.
//
// An operator looking for "the v1.8.0 release notes" found only the
// first kind here (grain/task-3092), which is why the description says
// which is which.
export default function ReleaseNotesPanel({ showError }) {
  return (
    <Box data-testid="release-notes">
      <Typography variant="h6" component="h3" sx={{ mt: 0 }}>
        Release notes
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1, mb: 2 }}>
        <strong>Weekly</strong> is what this deployment shipped each week,
        across every repository it works in. <strong>Versions</strong> is what
        each release of grain itself brought, from the notes this build of grain
        carries.
      </Typography>
      <WeeklyReleaseNotes showError={showError} />
      <Divider sx={{ my: 2 }} />
      <ReleaseVersions showError={showError} />
    </Box>
  );
}

// What this deployment shipped, a week at a time (model.ReleaseNote).
//
// It sits at the top of the Metrics pane on purpose. That pane is the
// one place in this UI opened because nothing is wrong -- "how much did
// we get through this week" is MetricsOverlay's own account of why it
// exists -- and the sections below answer it in rates and histograms,
// which say how the machine ran and never what it built. This says what
// it built: the week's own numbers, and the prose grain's weekly job
// wrote around them after reading the record.
//
// Two reads, and they are independent on purpose. The counts come from
// /api/release-notes/stats and are computed from grain's own rows every
// time they are asked for, so a week nobody has written up still has
// something true to show -- a deployment that installed this build on
// Tuesday, or whose Monday run failed, is not left with an empty pane.
// The prose comes from /api/release-notes/{period} and is whatever was
// published for that week, which may be nothing.
//
// The week picker lists the weeks that have notes, newest first, plus
// the week that just ended even when it has none: that is the week a
// reader opening this on a Monday came to see, and leaving it out of the
// list would make its absence look like a UI that had not loaded.
function WeeklyReleaseNotes({ showError }) {
  const [weeks, setWeeks] = useState(null);
  const [period, setPeriod] = useState("");
  const [note, setNote] = useState(null);
  const [stats, setStats] = useState(null);
  const zone = useTimeZone();

  // The listing, once: which weeks have been written up. The period is
  // left alone if the reader has already chosen one, so a refresh does
  // not walk them back to the newest week.
  const refresh = useCallback(async () => {
    try {
      const listed = await api("/api/release-notes");
      setWeeks(Array.isArray(listed) ? listed : []);
    } catch (err) {
      showError(err);
    }
  }, [showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // The selected week, and the numbers for it. An empty period asks the
  // server for the week that just ended rather than this browser
  // working out what week it is -- the deployment's own clock is what
  // decides that, and it is not necessarily this one (model.Config.
  // TimeZone).
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        // Cleared first, so a week being loaded never shows the
        // previous week's prose under the new week's numbers.
        setNote(null);
        const query = period ? `?period=${encodeURIComponent(period)}` : "";
        const loadedStats = await api(`/api/release-notes/stats${query}`);
        if (!live) return;
        setStats(loadedStats);
        // The stats answer names the week they are for, which is how a
        // panel that asked for no week in particular finds out which
        // one it is looking at. The notes are then asked for by name,
        // so the two halves on screen are demonstrably the same week.
        const week = period || loadedStats?.period;
        if (!week) return;
        const loadedNote = await api(
          `/api/release-notes/${encodeURIComponent(week)}`,
        );
        if (live) setNote(loadedNote);
      } catch (err) {
        if (live) showError(err);
      }
    };
    load();
    return () => {
      live = false;
    };
  }, [period, showError]);

  // Nothing at all until both halves have answered -- and "answered"
  // includes a deployment whose API said nothing useful, which is what
  // a caller rendering this pane beside others must not be made to
  // handle. A panel with no numbers has nothing to draw.
  if (!weeks || !stats) return null;

  // The weeks worth offering: everything published, plus the week the
  // stats are for when nothing has been published for it yet.
  const options = weeks.map((w) => w.period);
  if (stats.period && !options.includes(stats.period)) {
    options.unshift(stats.period);
  }
  const listed = weeks.find((w) => w.period === stats.period);
  const lines = linesChangedChip(stats);
  const heroes = [
    ["Pull requests merged", stats.pullRequestsMerged],
    ...(lines ? [lines] : []),
    ["Tasks completed", stats.tasksCompleted],
    ["Tasks filed", stats.tasksFiled],
    ["Agent runs", `${stats.runs} (${stats.runsSucceeded} succeeded)`],
    ["Agent hours", (stats.agentHours || 0).toFixed(1)],
  ];

  return (
    <Box data-testid="release-notes-weekly">
      <Stack
        direction="row"
        spacing={2}
        alignItems="baseline"
        sx={{ flexWrap: "wrap" }}
      >
        <Typography variant="subtitle1" component="h4" sx={{ mt: 0 }}>
          Weekly
        </Typography>
        <Select
          size="small"
          value={stats.period || ""}
          onChange={(e) => setPeriod(e.target.value)}
          inputProps={{ "aria-label": "week" }}
        >
          {options.map((week) => (
            <MenuItem key={week} value={week}>
              {weekLabel(week)}
            </MenuItem>
          ))}
        </Select>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1, mb: 2 }}>
        What this deployment shipped that week, across every repository it works
        in -- not a release of grain; those are under Versions below. The
        numbers are grain&apos;s own count of its rows; the prose beneath them
        is written once a week by a standing job grain ships, and is published
        to the state repository as release-notes/
        {stats.period || "<week>"}.md.
      </Typography>
      <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap", gap: 1 }}>
        {heroes.map(([label, value, title]) => (
          <Chip key={label} label={`${label}: ${value}`} title={title} />
        ))}
      </Stack>
      {stats.repos?.length > 0 && (
        <Stack sx={{ mt: 2 }} spacing={0.5}>
          {stats.repos.map((repo) => (
            <Typography key={repo.repo} variant="body2">
              <strong>{repo.repo}</strong>: {repo.pullRequestsMerged} merged
              {repoLines(repo)}, {repo.tasksCompleted} tasks completed,{" "}
              {repo.runs} runs
            </Typography>
          ))}
        </Stack>
      )}
      <Divider sx={{ my: 2 }} />
      {note?.text ? (
        <>
          {note.title && (
            <Typography variant="subtitle1" component="h4" sx={{ mt: 0 }}>
              {note.title}
            </Typography>
          )}
          <Typography variant="caption" color="text.secondary">
            {releaseNoteAuthor(note)}
            {note.updatedAt
              ? ` on ${formatDateTime(note.updatedAt, zone)}`
              : ""}
          </Typography>
          <Box sx={{ mt: 1 }}>
            <Markdown>{note.text}</Markdown>
          </Box>
        </>
      ) : (
        <Typography variant="body2" color="text.secondary">
          {listed
            ? "These notes have been withdrawn."
            : "Nothing has been written up for this week yet. grain's release-notes job runs on a Monday morning; the numbers above are the week itself, whether or not anybody has written about it."}
        </Typography>
      )}
    </Box>
  );
}

// linesChangedChip is the week's lines-changed hero, or null for a week
// that has none to show.
//
// The one number here grain has not always had. A merged pull request's
// diff size is recorded as grain observes the merge
// (model.Observation.PrAdditions), so a window reaching back before it
// started doing that holds merges with no size on file -- which is "not
// recorded", not "changed nothing". So the chip says how much of the
// week it covers when it covers only part of it, and a week with
// nothing measured shows no chip rather than a confident zero.
function linesChangedChip(stats) {
  const merged = stats.pullRequestsMerged || 0;
  const measured = stats.pullRequestsMeasured || 0;
  if (merged === 0 || measured === 0) return null;
  const figure = `+${stats.linesAdded || 0} -${stats.linesDeleted || 0}`;
  if (measured === merged) return ["Lines changed", figure];
  return [
    "Lines changed",
    `${figure} (${measured} of ${merged} merges)`,
    `Grain has no diff size on file for ${merged - measured} of this week's ` +
      `${merged} merges, so this figure covers the other ${measured}.`,
  ];
}

// repoLines is linesChangedChip's figure for one repository's row, in
// the space a line of prose has for it -- and the same empty answer for
// a repository whose merges grain has no size for.
function repoLines(repo) {
  const measured = repo.pullRequestsMeasured || 0;
  if (measured === 0) return "";
  const figure = ` (+${repo.linesAdded || 0} -${repo.linesDeleted || 0}`;
  return measured < (repo.pullRequestsMerged || 0)
    ? `${figure} over ${measured})`
    : `${figure})`;
}
