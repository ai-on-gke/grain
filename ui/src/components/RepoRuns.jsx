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
  Alert,
  Box,
  Button,
  CircularProgress,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { REFRESH_MS, RunList, StartRunForm } from "./CIPage.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";

// RepoRuns is one repo's own grain CI runs, on that repo's page
// (grain/task-351).
//
// It is there because the page above it is where this deployment's
// workflows for that repo are written (RepoPage's "CI workflows",
// grain/task-245) and it is the one place in the UI where somebody is
// thinking about one repo's CI. Until this, the page said nothing about
// what those workflows had done: seeing that meant closing the page,
// opening System, picking CI and narrowing a hundred deployment-wide
// runs back down to the repo you had just been looking at. Editing a
// workflow and watching what it did are the same sitting.
//
// It is a listing and not a second CI pane. The API's own `?repo=`
// filter does the narrowing (grain/task-297), and the rows are the pane's
// own RunList with its repo column dropped. A row opens the shared CI run
// overlay over this page, so its jobs, steps, output and cancellation stay
// in the repo context where the reader asked for them.
const RECENT = 5;

export default function RepoRuns({ repo, onOpenRun, showError }) {
  // data is GET /api/ci/runs?repo= as it stands, null while the first
  // read is in flight -- the same distinction the rest of this page
  // draws, since an empty table would read as "nothing has ever run
  // here" while the answer is still on its way.
  const [data, setData] = useState(null);
  const [starting, setStarting] = useState(false);
  const zone = useTimeZone();

  const refresh = useCallback(async () => {
    try {
      setData(await api(`/api/ci/runs?repo=${encodeURIComponent(repo)}`));
    } catch (err) {
      showError(err);
    }
  }, [repo, showError]);

  // Polled, on the CI pane's own clock, because a run started from the
  // button below fills in job by job while somebody is looking at it --
  // and because the reason to be on this page at all is often a workflow
  // that was just saved. The answer is a repo's worth of run summaries
  // with no step output in them (ui.ciListSummary), so this is a small
  // thing to ask for every five seconds.
  useEffect(() => {
    setData(null);
    refresh();
    const interval = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(interval);
  }, [refresh]);

  // A run that has just been asked for is opened rather than left to
  // appear in the table: the response to POST is the run itself, and
  // somebody who has just pressed Start wants to watch it. That means
  // the shared run overlay -- closing it puts them back here.
  const onStarted = (run) => {
    setStarting(false);
    refresh();
    if (run?.id) onOpenRun(run.id, repo);
  };

  const runs = data?.runs || [];
  return (
    <Box>
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 1,
          flexWrap: "wrap",
          mb: 0.5,
        }}
      >
        {/* The gate arms the CI pane and the rail's CI entry carry
            (ItemGlyph.jsx): the rows under this heading are the same
            runs that pane lists, narrowed to this repo. */}
        <Typography
          variant="subtitle2"
          sx={{ display: "flex", alignItems: "center", gap: 0.8 }}
        >
          <ItemGlyph kind="ci" size={20} />
          CI runs
        </Typography>
        {data?.enabled && (
          <Button size="small" onClick={() => setStarting((v) => !v)}>
            {starting ? "Close" : "Start a run"}
          </Button>
        )}
      </Box>
      {/* Said here rather than left to be inferred from an empty list:
          a deployment started with -ci=false will store every workflow
          written on the repository's CI tab and run none of them, which
          is worth knowing before writing another. This list sits on the
          Overview, not under that form, so the note names the tab
          rather than pointing "above" at workflows that are not there. */}
      {data !== null && !data.enabled && (
        <Alert severity="info">
          This deployment does not run grain CI: its daemon was started with
          -ci=false, so the workflows on this repository's CI tab are stored
          but never run.
        </Alert>
      )}
      {/* The repo is not a question here, so the form is a branch and a
          button (CIPage.StartRunForm with its picker fixed). */}
      {data?.enabled && starting && (
        <StartRunForm
          repo={repo}
          onStarted={onStarted}
          onCancel={() => setStarting(false)}
        />
      )}
      {data === null && (
        <Box sx={{ display: "flex", justifyContent: "center", py: 2 }}>
          <CircularProgress size={24} aria-label="Loading CI runs" />
        </Box>
      )}
      {data?.enabled &&
        (runs.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No CI runs for {repo} yet. Start one above, against a branch. A repo
            grain CI has not been enabled for is refused rather than run: that
            takes the grain-ci capability, on this repo or on this
            deployment&apos;s defaults.
          </Typography>
        ) : (
          <>
            <RunList
              runs={runs.slice(0, RECENT)}
              zone={zone}
              onOpen={(id) => onOpenRun(id, repo)}
              showRepo={false}
              faults={data.faults}
            />
            {/* The deployment-wide listing remains useful for history
                beyond the recent rows kept on this repo page. */}
            <Box sx={{ mt: 0.5 }}>
              <Button size="small" onClick={() => onOpenRun(null, repo)}>
                {runs.length > RECENT
                  ? `All ${runs.length} runs for this repo`
                  : "All runs for this repo"}
              </Button>
            </Box>
          </>
        ))}
    </Box>
  );
}
