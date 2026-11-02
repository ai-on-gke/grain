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
import { Box, Chip, Link, Stack, Typography } from "@mui/material";
import api from "../api.js";
import { REFRESH_MS } from "./CIPage.jsx";
import { buildPath } from "../paths.js";
import { pullRequestUrl } from "../state.js";

// The merge queue's overlap re-checks, shown as the thing they are
// (GET /api/merge-batches, grain/task-301).
//
// When a finished change overlaps what has landed on its base branch since
// the commit its own run tested, the merge queue merges it onto a throwaway
// ref of grain's own and runs grain CI against that tree before merging
// (orchestrator's startMergeRecheck).
//
// So this draws the re-check in flight: the ref and the commit that is
// actually being judged, the run as a link, how long it has been going
// against the bound past which the queue gives up on it, and the change
// being re-checked. It is mounted twice -- on a repo's own page, where
// "what is my queue doing" is asked, and on the change's task page -- and
// it is the same card both times.
//
// Read-only, deliberately. Nothing here cancels a re-check: it is the
// reconciler's own state machine, and it ends on its own within the age
// bound whatever happens.

// batchAge is how long a batch has been holding its members, against how
// long it may. "22m" alone says nothing -- a full suite here is 20-45
// minutes -- and "22m of 90m" says how much rope is left before the queue
// abandons it.
//
// maxAgeSeconds comes from the API (ui.MergeBatches.MaxAgeSeconds,
// model.MaxMergeBatchAge) rather than being written in here, so changing
// the bound changes what this says.
export function batchAge(batch, maxAgeSeconds, now = Date.now()) {
  if (!batch?.startedAt) return "";
  const started = new Date(batch.startedAt).getTime();
  const until = batch.finishedAt ? new Date(batch.finishedAt).getTime() : now;
  const minutes = Math.max(0, Math.round((until - started) / 60000));
  const limit = maxAgeSeconds ? Math.round(maxAgeSeconds / 60) : 0;
  if (!batch.running) return `ran for ${minutes}m`;
  return limit ? `${minutes}m of ${limit}m` : `${minutes}m`;
}

// batchStanding is the one word beside the branch name, and the sentence
// behind it.
//
// A re-check that is still going is the interesting case and the common
// one. The rest are the moments after a verdict, which a card that polls
// every few seconds does catch: only "failure" means the merged tree is
// broken (model.MergeBatch.Failed) -- everything else that is not a pass
// is a run that said nothing about the tree, which discards the re-check
// without blaming the change.
export function batchStanding(batch) {
  if (batch.running) {
    return {
      label: "Testing",
      color: "info",
      title:
        "One grain CI run is judging the base branch with this change merged into it. It merges if the run passes; nothing has landed yet.",
    };
  }
  if (batch.conclusion === "success") {
    return {
      label: "Passed",
      color: "success",
      title: "The re-check was green, so the change merges.",
    };
  }
  if (batch.conclusion === "failure") {
    return {
      label: "Failed",
      color: "error",
      title:
        "The change broke grain CI when merged on top of the base branch. It goes back to its own agent for repair and the rest of the queue carries on.",
    };
  }
  return {
    label: "Abandoned",
    color: "warning",
    title:
      "The run said nothing about the tree -- it errored, was cancelled, was lost, or took longer than the queue waits. No failure can be read out of that, so the re-check was discarded.",
  };
}

// shortSHA is the commit as everything else in this UI writes one.
function shortSHA(sha) {
  return sha ? sha.slice(0, 12) : "";
}

// MergeBatchCard is one batch. `maxAgeSeconds` is the envelope's bound,
// `highlight` the task whose page this is (so a member can see itself in
// the list rather than counting rows), and onOpenRun/onOpenTask the ways
// in that the mounting page has -- each falls back to a plain link, since
// every destination here has a URL of its own.
function MergeBatchCard({
  batch,
  maxAgeSeconds,
  highlight,
  onOpenRun,
  onOpenTask,
  now,
}) {
  const standing = batchStanding(batch);
  return (
    <Box
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        p: 1.5,
        mb: 1,
      }}
    >
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        useFlexGap
        flexWrap="wrap"
      >
        <Typography variant="subtitle2">
          {batch.members.length}{" "}
          {batch.members.length === 1 ? "change" : "changes"} on {batch.branch}
        </Typography>
        <Chip
          size="small"
          color={standing.color}
          label={standing.label}
          title={standing.title}
        />
        <Typography variant="caption" color="text.secondary">
          {batchAge(batch, maxAgeSeconds, now)}
        </Typography>
        <Box sx={{ flex: 1 }} />
        {/* The run is the whole bet: one suite, several changes riding on
            it. Without a way in, following a batch means guessing which
            of a repository's runs is this one from its ref. */}
        {batch.runId &&
          (onOpenRun ? (
            <Link
              component="button"
              type="button"
              variant="caption"
              underline="hover"
              onClick={() => onOpenRun(batch.runId)}
            >
              Show the run
            </Link>
          ) : (
            <Link
              variant="caption"
              underline="hover"
              href={buildPath({ view: "ci", ciRunId: batch.runId })}
            >
              Show the run
            </Link>
          ))}
      </Stack>
      {/* The ref and the commit, because they are what makes the verdict
          a verdict about anything: the tree being judged is neither the
          base branch nor the change's head, and nobody's pull request
          points at it. */}
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mt: 0.5 }}
        title={`Built on ${batch.ref} from ${batch.branch} at ${shortSHA(batch.baseCommit)}. Nothing merges from this ref -- it is where the re-check is run, and what lands is the change's own pull request.`}
      >
        {batch.ref} at {shortSHA(batch.commit)}
      </Typography>
      {batch.detail && !batch.running && (
        <Typography variant="caption" color="text.secondary" component="div">
          {batch.detail}
        </Typography>
      )}
      <Box component="ol" sx={{ pl: 3, my: 0.5 }}>
        {batch.members.map((m) => (
          <Box
            component="li"
            key={m.task}
            sx={{
              fontWeight: highlight && m.task === highlight ? 600 : 400,
            }}
          >
            <Stack
              direction="row"
              spacing={1}
              alignItems="baseline"
              useFlexGap
              flexWrap="wrap"
            >
              {/* A real link either way, so a middle click or "copy
                  link" reaches the task; a plain click opens it in place
                  when the page hosting this panel can (TaskLink). Without
                  that, the repo page's copy reloaded the whole app. */}
              <Link
                variant="body2"
                underline="hover"
                href={buildPath({ view: "tasks", taskId: m.task })}
                onClick={(e) => {
                  if (!onOpenTask) return;
                  if (
                    e.button !== 0 ||
                    e.metaKey ||
                    e.ctrlKey ||
                    e.shiftKey ||
                    e.altKey
                  )
                    return;
                  e.preventDefault();
                  onOpenTask(m.task);
                }}
              >
                {m.title || m.task}
              </Link>
              {m.number > 0 && (
                <Link
                  variant="caption"
                  color="text.secondary"
                  underline="hover"
                  target="_blank"
                  rel="noreferrer"
                  href={pullRequestUrl(`${batch.repo}#${m.number}`, m.headRef)}
                  title={`Merged onto the re-check ref pinned to ${shortSHA(m.head)}: a push landing on ${m.headRef} while this runs discards the re-check rather than merging a commit it never tested.`}
                >
                  #{m.number}
                </Link>
              )}
            </Stack>
          </Box>
        ))}
      </Box>
    </Box>
  );
}

// MergeBatchPanel reads the batches and draws them.
//
// Two mountings, told apart by `taskId`. With one, this is a member's own
// task page and the panel is the single batch that task is in, or nothing
// at all -- which is the answer almost always, and a heading over nothing
// on every task page would be noise. Without one, this is a repository's
// page: the section is always there, because "no batch in flight" is a
// real answer to the question that page is being asked, and a section
// that appears only sometimes is one nobody knows to look for.
//
// Polled on the CI pane's own clock, because a batch is a run in flight:
// it forms, reports and disappears inside a sitting, and a card that
// needed a reload would mostly be showing a batch that had already ended.
export default function MergeBatchPanel({
  repo,
  taskId,
  onOpenRun,
  onOpenTask,
  showError,
}) {
  // data is GET /api/merge-batches as it stands, null while the first
  // read is in flight -- the same distinction the panels around this one
  // draw, since "no batches" and "not asked yet" are different answers.
  const [data, setData] = useState(null);
  const [now, setNow] = useState(() => Date.now());

  const refresh = useCallback(async () => {
    try {
      const path = repo
        ? `/api/merge-batches?repo=${encodeURIComponent(repo)}`
        : "/api/merge-batches";
      setData(await api(path));
      setNow(Date.now());
    } catch (err) {
      if (showError) showError(err);
    }
  }, [repo, showError]);

  useEffect(() => {
    setData(null);
    refresh();
    const interval = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(interval);
  }, [refresh]);

  const all = data?.batches || [];
  const batches = taskId
    ? all.filter((b) => b.members.some((m) => m.task === taskId))
    : all;

  if (taskId) {
    if (!batches.length) return null;
    return (
      <Box sx={{ mb: 2 }}>
        <Typography variant="overline" color="text.secondary">
          Merge re-check
        </Typography>
        {batches.map((b) => (
          <MergeBatchCard
            key={`${b.repo}\u0000${b.branch}`}
            batch={b}
            maxAgeSeconds={data?.maxAgeSeconds}
            highlight={taskId}
            onOpenRun={onOpenRun}
            onOpenTask={onOpenTask}
            now={now}
          />
        ))}
      </Box>
    );
  }

  return (
    <Box>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Merge re-checks
      </Typography>
      {data === null ? (
        <Typography variant="caption" color="text.secondary">
          Loading merge re-checks…
        </Typography>
      ) : batches.length === 0 ? (
        <Typography variant="caption" color="text.secondary">
          No re-check is running. When a finished change overlaps what has
          landed on its base branch since the commit its own run tested, grain
          merges it onto a throwaway ref and re-checks it before it lands. What
          appears here is that re-check while it is running.
        </Typography>
      ) : (
        batches.map((b) => (
          <MergeBatchCard
            key={`${b.repo}\u0000${b.branch}`}
            batch={b}
            maxAgeSeconds={data?.maxAgeSeconds}
            onOpenRun={onOpenRun}
            onOpenTask={onOpenTask}
            now={now}
          />
        ))
      )}
    </Box>
  );
}
