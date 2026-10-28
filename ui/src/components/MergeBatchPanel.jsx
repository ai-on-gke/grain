import { useCallback, useEffect, useState } from "react";
import { Box, Chip, Link, Stack, Typography } from "@mui/material";
import api from "../api.js";
import { REFRESH_MS } from "./CIPage.jsx";
import { buildPath } from "../paths.js";
import { pullRequestUrl } from "../state.js";

// The merge queue's speculative batches, shown as the thing they are
// (GET /api/merge-batches, grain/task-301).
//
// Batching (grain/task-294) tests several queued changes together on a
// throwaway ref of grain's own and merges the lot on one pass. Each
// member's row says so -- "Testing with the rest of the batch", with a
// sentence behind it naming the ref, the commit and how many others there
// are -- and that is everything a row can say about a thing that is not
// the row. Three of them do not say they are the *same* batch, which one
// is first, or where the single run that decides all three is; a person
// watching had to infer a batch from three rows that happened to agree.
//
// So this draws the batch. One card per batch in flight: the ref and the
// commit that is actually being judged, the run as a link, how long it
// has been going against the bound past which the queue gives up on it,
// and the members in the queue's own order with the first marked. It is
// mounted twice -- on a repo's own page, where "what is my queue doing"
// is asked, and on a member's task page, where "who else is in this with
// me" is -- and it is the same card both times, because it is the same
// question with a different way in.
//
// Read-only, deliberately. Nothing here cancels a batch or drops a
// member: the batch is the reconciler's own state machine, it ends on its
// own within the age bound whatever happens, and a button in the middle
// of it would be a second thing deciding what the queue is testing. The
// way to stop batching is the setting that turns it off
// (Settings, merge batch size), which is a decision about future batches
// rather than a hand on this one.

// batchAge is how long a batch has been holding its members, against how
// long it may. "22m" alone says nothing -- a full suite here is 20-45
// minutes -- and "22m of 90m" says how much rope is left before the queue
// abandons it and falls back to merging one at a time.
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
// A batch that is still going is the interesting case and the common one.
// The rest are the moments after a verdict, which a card that polls every
// few seconds does catch: only "failure" means the combination is broken
// (model.MergeBatch.Failed) -- everything else that is not a pass is a
// run that said nothing about the tree, which ends the batch without
// blaming anybody and lets the ordinary one-at-a-time queue take over.
export function batchStanding(batch) {
  if (batch.running) {
    return {
      label: "Testing",
      color: "info",
      title:
        "One grain CI run is judging the base branch with all of these changes merged into it. They all merge if it passes; none of them has landed yet.",
    };
  }
  if (batch.conclusion === "success") {
    return {
      label: "Passed",
      color: "success",
      title: "The combination was green, so every member merges.",
    };
  }
  if (batch.conclusion === "failure") {
    return {
      label: "Failed",
      color: "error",
      title:
        "The combination is broken. The queue halves the batch from the front and tests again, until one change is left holding the failure -- that one goes back to its own agent and the rest carry on.",
    };
  }
  return {
    label: "Abandoned",
    color: "warning",
    title:
      "The run said nothing about the tree -- it errored, was cancelled, was lost, or took longer than the queue waits. No culprit can be read out of that, so the members were released and this repository merges one at a time for a few minutes.",
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
          base branch nor any member's head, and nobody's pull request
          points at it. */}
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mt: 0.5 }}
        title={`Built on ${batch.ref} from ${batch.branch} at ${shortSHA(batch.baseCommit)}. Nothing merges from this ref -- it is where the combination is tested, and what lands is each member's own pull request.`}
      >
        {batch.ref} at {shortSHA(batch.commit)}
      </Typography>
      {batch.splits > 0 && (
        <Typography variant="caption" color="text.secondary" component="div">
          Halved {batch.splits} {batch.splits === 1 ? "time" : "times"} out of a
          bigger batch that failed.
        </Typography>
      )}
      {batch.detail && !batch.running && (
        <Typography variant="caption" color="text.secondary" component="div">
          {batch.detail}
        </Typography>
      )}
      <Box component="ol" sx={{ pl: 3, my: 0.5 }}>
        {batch.members.map((m, i) => (
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
                  title={`Merged into the batch pinned to ${shortSHA(m.head)}: a push landing on ${m.headRef} while this runs takes this change out of the batch rather than merging a commit the batch never contained.`}
                >
                  #{m.number}
                </Link>
              )}
              {/* Which half a failure is bisected towards, said on the
                  member it is about. The queue halves from the front, so
                  the first change is in every subdivision until it is
                  either cleared or left holding the failure on its own. */}
              {i === 0 && batch.members.length > 1 && (
                <Typography variant="caption" color="text.secondary">
                  first -- a failure is halved towards this end
                </Typography>
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
          Merge batch
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
        Merge batches
      </Typography>
      {data === null ? (
        <Typography variant="caption" color="text.secondary">
          Loading merge batches…
        </Typography>
      ) : batches.length === 0 ? (
        <Typography variant="caption" color="text.secondary">
          No batch is in flight. When several finished changes are queued
          together, grain merges them onto a throwaway ref and runs one suite
          over the lot -- they all land if it passes, and a failure names a
          culprit without anything having reached the base branch. What appears
          here is that bet while it is being made.
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
