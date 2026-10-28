import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  ButtonBase,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  FormControlLabel,
  Link,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import api, { isUnreachable, unreachableReason } from "../api.js";
import BranchField from "./BranchField.jsx";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import { ListHeader } from "./ListPrimitives.jsx";
import NarrowingBar from "./NarrowingBar.jsx";
import { formatBytes } from "./PackageCachePanel.jsx";
import RepoField from "./RepoField.jsx";
import {
  CONCLUSION_LABELS,
  RUN_ATTRIBUTES,
  conclusionOf,
  hasStarted,
  instantOf,
  jobConclusionOf,
  jobsNamed,
  narrowRuns,
  queuedByRun,
  queuedOf,
  runsQuery,
  spanOf,
  useCINarrowing,
} from "../ciFilters.js";
import {
  faultDetail,
  faultFixes,
  faultSource,
  faultSourceLabel,
  faultSummary,
  faultsForRun,
  fixWhere,
} from "../ciFaults.js";
import { knownRepos, pullRequestUrl } from "../state.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// instantOf, hasStarted and spanOf live with the filter vocabulary now
// (ciFilters.js), since narrowing and ordering runs by when they started
// and how long they took needs them -- and are re-exported from here
// because this is where every reader of them still looks.
export { hasStarted, instantOf, spanOf };

// CIPage is grain's own CI (pkg/ci, GET /api/ci/runs) with somebody
// looking at it (grain/task-247). The engine and its API have existed
// since grain/task-185 and had no reader at all: watching a run meant
// curling the daemon.
//
// Two views, because there are two questions. The list answers "what has
// run, and is any of it red" -- one row per run, newest first, which is
// the order the daemon holds them in. Opening one answers "why", which
// is a job, a step and what that step printed, and is the whole reason
// anybody comes here.
//
// Above the list is a third question, and the one this pane is opened on
// a quiet day to ask: what is running *now*, and on what
// (activeSandboxes below, grain/task-354). A CI job is a whole sandbox
// of this deployment's capacity for as long as it lasts -- the same
// machines an agent run is dispatched onto -- so "CI is holding three of
// them and has been for forty minutes" is an answer about the
// deployment, not just about a run. It is drawn from the runs the list
// already fetches rather than from a route of its own: a job in flight
// is reported to the daemon's store with its sandbox on it the moment
// it gets one (ci.Engine.Run's observer).
//
// Both of the API's own verbs are here (grain/task-297), not just the
// one: DELETE is the "Cancel run" on an open run, and POST is the "Start
// a run" above the list -- a repo and a branch, which is the whole of
// what ui.CIRunRequest takes. Until that button existed the only way to
// make a run happen was `grain ci run` from a terminal, which is a
// strange thing for a pane whose every other control is a click, and it
// is what an operator reaches for after fixing a workflow file two tabs
// away in Settings.
//
// The narrowing is done here rather than by the daemon, over a bounded
// page of the deployment's runs (ciFilters.js's runsQuery and
// repoNarrowedTo, which say why each way round): the poll asks for a
// limit and for the window the reader named, and the conditions are
// answered in the browser as they are typed.
//
// It polls rather than loading once. Run detail and its own polling live
// in CIRunOverlay: ci.Engine reports each job as it finishes, and the
// overlay stops asking once the run has a conclusion.
//
// Exported because a repo's own page lists that repo's runs too
// (RepoRuns.jsx, grain/task-351) and there is no reason for the two
// listings of the same thing to tick at different rates.
export const REFRESH_MS = 5000;

// CACHE_REFRESH_MS is the job cache's own, slower clock -- see the
// effect that uses it.
const CACHE_REFRESH_MS = 30000;

// COLOURS is the colour each word of the conclusion vocabulary is drawn
// in. The words themselves, and what each one means, are the filter
// vocabulary's (ciFilters.js's CONCLUSION_LABELS) -- one list, so the
// chip on a row and the option in the filter menu cannot drift apart.
//
// "Errored" is a warning rather than an error chip: a job grain never got
// as far as judging the tree -- no sandbox to run it in, or a step that
// could not fetch the environment its job runs in
// (ci.Step.Infrastructure, so that a cold cache costs time and not a
// verdict) -- says nothing about the tree, so the thing to go and look at
// is this deployment, not the commit.
const COLOURS = {
  success: "success",
  failure: "error",
  errored: "warning",
  cancelled: "warning",
  skipped: "default",
  running: "info",
  pending: "default",
};

export function conclusionChip(conclusion) {
  return {
    label: CONCLUSION_LABELS[conclusion] || conclusion,
    color: COLOURS[conclusion] || "default",
  };
}

// An empty conclusion means unfinished, not necessarily running. Jobs and
// steps acquire StartedAt only when execution begins. Runs are recorded when
// they enter the queue, so their own StartedAt cannot make that distinction;
// a run has begun once any of its jobs has begun or finished -- which is
// why a run is asked a different question from a job or a step.
export function progressChip(thing, run = false) {
  return conclusionChip(run ? conclusionOf(thing) : jobConclusionOf(thing));
}

// RANK is ci.SortJobRuns' own ordering, restated for the browser:
// failures first, because that is what the reader came for, then the
// jobs grain could not run, then everything else in plan order. Kept in step with pkg/ci by having the
// same two-line rule and nothing else -- there is no ordering to share
// over the wire, since Run.Jobs is served in plan order on purpose (it
// is the order the run happened in).
const RANK = { failure: 0, errored: 1, cancelled: 2, success: 3 };

export function sortJobs(jobs) {
  return [...(jobs || [])].sort(
    (a, b) => (RANK[a.conclusion] ?? 4) - (RANK[b.conclusion] ?? 4),
  );
}

// formatDuration renders a span in the words somebody reading a CI run
// uses for one: seconds while it is seconds, minutes and seconds up to
// an hour, hours and minutes past that. Coarser the longer it gets --
// nobody reading a two-hour run needs the seconds on the end of it.
export function formatDuration(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms) || ms < 0) return "—";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

// formatSpent is a job's or a run's time said as the two things it is
// made of: what it spent waiting for a machine, and what it spent on
// one. "18m 0s queued · 4m 0s" is a different sentence from "22m 0s",
// and only the first sends a reader to this deployment's capacity rather
// than to the job (ci.JobRun.Queued, grain/task-326).
//
// The wait goes first because it happened first, and a thing that never
// got a machine is all wait -- which is exactly the job whose record
// used to say nothing at all.
function formatSpent(span, queued) {
  const parts = [];
  if (queued) parts.push(`${formatDuration(queued)} queued`);
  if (span !== null && span !== undefined) parts.push(formatDuration(span));
  return parts.join(" · ") || "—";
}

// jobSummary is the listing's one-line answer to "what happened in
// there": how many jobs, and how they ended when that is not simply all
// of them succeeding.
export function jobSummary(run) {
  const jobs = run.jobs || [];
  if (jobs.length === 0) return "no jobs";
  const counts = {};
  for (const job of jobs) {
    const state = jobConclusionOf(job);
    counts[state] = (counts[state] || 0) + 1;
  }
  const cached = jobs.filter((job) => job.cached).length;
  const cacheSummary = cached
    ? ` · ${cached} cache ${cached === 1 ? "hit" : "hits"}`
    : "";
  const jobCount = `${jobs.length} ${jobs.length === 1 ? "job" : "jobs"}`;
  if (counts.success === jobs.length)
    return `${jobCount}, all green${cacheSummary}`;
  // Worst first, so what is wrong is the first thing in the cell.
  const parts = [];
  for (const state of [
    "failure",
    "errored",
    "cancelled",
    "skipped",
    "success",
    "running",
    "pending",
  ]) {
    if (counts[state]) parts.push(`${counts[state]} ${state}`);
  }
  return `${jobCount}: ${parts.join(", ")}${cacheSummary}`;
}

// activeSandboxes is every CI sandbox this deployment is spending right
// now: one entry per job that has a machine and has not finished with
// it, with the run it belongs to, longest-running first.
//
// A job qualifies on two facts and no guesswork. Its run has no
// conclusion -- a finished run has released everything it held, and a
// run the daemon was killed in the middle of is concluded as cancelled
// when the next daemon reads it back (ci.StoredRunStore.
// RecoverInterrupted), so neither leaves a row here forever. And the job
// itself has a sandbox name and no conclusion of its own: a job that is
// queueing for one of this deployment's CI slots has neither, and a job
// answered from the cache never gets a machine at all (ci.JobRun.
// Sandbox is cleared on a cache hit, precisely so it cannot be mistaken
// for one).
//
// Longest-running first because that is the row worth acting on: a job
// that has been on a machine for an hour is either a workflow that
// deserves a timeout or a sandbox nothing is going to give back.
export function activeSandboxes(runs) {
  const active = [];
  for (const run of runs || []) {
    if (run.conclusion) continue;
    for (const job of run.jobs || []) {
      if (job.conclusion || !job.sandbox) continue;
      active.push({ run, job });
    }
  }
  return active.sort(
    (a, b) =>
      (instantOf(a.job.startedAt) ?? 0) - (instantOf(b.job.startedAt) ?? 0),
  );
}

// ACTIVE_SANDBOXES_STORAGE_KEY is where this browser remembers whether
// the active-sandboxes table was left open. Collapsed is the default and
// is stored as no key at all.
export const ACTIVE_SANDBOXES_STORAGE_KEY = "grain.ci.activeSandboxes.expanded";

function readActiveSandboxesExpanded() {
  try {
    return localStorage.getItem(ACTIVE_SANDBOXES_STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

function writeActiveSandboxesExpanded(expanded) {
  try {
    if (expanded) localStorage.setItem(ACTIVE_SANDBOXES_STORAGE_KEY, "true");
    else localStorage.removeItem(ACTIVE_SANDBOXES_STORAGE_KEY);
  } catch {
    // A browser that will not store it just forgets the choice.
  }
}

// activeSandboxesSummary is the collapsed section's one line: how many,
// and the longest-running of them with the job and the branch it is
// for -- the row worth acting on, which activeSandboxes already put
// first. The branch is said by its last segment ("task-955"), since a
// grain branch's prefix is the same on every row.
export function activeSandboxesSummary(active, now = Date.now()) {
  const count = `${active.length} CI ${
    active.length === 1 ? "sandbox" : "sandboxes"
  } running`;
  const longest = active[0];
  if (!longest) return count;
  const span = spanOf(longest.job, now);
  if (span === null || span === undefined) return count;
  const ref = (longest.run.ref || "").split("/").pop();
  const what = [jobName(longest.job), ref].filter(Boolean).join(", ");
  return `${count} · longest ${formatDuration(span)}${what ? ` (${what})` : ""}`;
}

// ActiveSandboxes is that list with a table around it, above the runs.
//
// It says so when there are none, rather than disappearing: "nothing is
// running" is the answer somebody came for as often as the other one,
// and a section that is sometimes absent makes a reader wonder whether
// they are looking at the right pane.
//
// Collapsed by default to its one-line summary (grain/task-989): with
// dozens of CI sandboxes running at once the table pushed the runs --
// what most people open this page for -- far down it. The reader's
// choice is remembered per browser.
function ActiveSandboxes({ active, onOpen }) {
  const now = Date.now();
  const [expanded, setExpanded] = useState(readActiveSandboxesExpanded);
  const toggle = () => {
    setExpanded((v) => {
      writeActiveSandboxesExpanded(!v);
      return !v;
    });
  };
  const heading = `Active sandboxes${
    active.length > 0 ? ` (${active.length})` : ""
  }`;
  if (active.length === 0) {
    return (
      <Box sx={{ mb: 2 }}>
        <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
          {heading}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          No CI job is on a machine right now.
        </Typography>
      </Box>
    );
  }
  return (
    <Box sx={{ mb: 2 }}>
      {/* The summary sits beside the heading rather than in it, so the
          heading is still named "Active sandboxes (N)" and nothing else;
          it describes the button instead, and a click on it toggles too. */}
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          flexWrap: "wrap",
          columnGap: 1,
          mb: 0.5,
        }}
      >
        <Typography variant="subtitle2">
          <ButtonBase
            aria-expanded={expanded}
            aria-controls="ci-active-sandboxes"
            aria-describedby="ci-active-sandboxes-summary"
            onClick={toggle}
            sx={{
              gap: 1,
              font: "inherit",
              borderRadius: 1,
              "&.Mui-focusVisible": {
                outline: 2,
                outlineColor: "primary.main",
              },
            }}
          >
            <ExpandMoreIcon
              fontSize="small"
              aria-hidden
              sx={{
                transform: expanded ? "rotate(0deg)" : "rotate(-90deg)",
                transition: "transform 150ms",
              }}
            />
            {heading}
          </ButtonBase>
        </Typography>
        <Typography
          id="ci-active-sandboxes-summary"
          variant="caption"
          color="text.secondary"
          onClick={toggle}
          sx={{ cursor: "pointer" }}
        >
          {activeSandboxesSummary(active, now)}
        </Typography>
      </Box>
      {/* Always in the document, so aria-controls names something
          whether the table is open or not. */}
      <Box id="ci-active-sandboxes" hidden={!expanded}>
        {expanded && (
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Sandbox</TableCell>
                <TableCell>Repo</TableCell>
                <TableCell>Branch</TableCell>
                <TableCell>Job</TableCell>
                <TableCell>Running for</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {active.map(({ run, job }) => (
                <TableRow
                  key={`${run.id}:${job.workflowPath}:${job.jobId}`}
                  hover
                >
                  {/* The name the backend gave the machine, which is what
                    an operator greps the daemon log for and what they
                    will find it under in a list of VMs (ci.sandboxName).
                    Monospaced for that reason. */}
                  <TableCell
                    sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                  >
                    {job.sandbox}
                  </TableCell>
                  <TableCell>
                    <Link
                      component="button"
                      type="button"
                      underline="hover"
                      onClick={() => onOpen(run.id)}
                    >
                      {run.repo}
                    </Link>
                  </TableCell>
                  <TableCell
                    sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                  >
                    {run.ref}
                  </TableCell>
                  <TableCell>
                    {jobName(job)}
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="span"
                      sx={{ display: "block" }}
                    >
                      {job.workflow}
                    </Typography>
                  </TableCell>
                  {/* "Running for", so the wait that got it here is not in
                    this cell: a job in this table is on a machine, and
                    what it waited for is on its own row in the run
                    (JobPanel). */}
                  <TableCell>{formatDuration(spanOf(job, now))}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Box>
    </Box>
  );
}

// shortCommit is the SHA as anybody says it out loud. The full one is
// still on the element's title, since it is what a `git show` takes.
function shortCommit(commit) {
  return commit ? commit.slice(0, 12) : "";
}

function jobName(job) {
  return job.name || job.jobId;
}

// requesterLabel is who asked for a run, as its heading says it. A run a
// task's own checks started records the task's bare id as its requester
// (orchestrator.ci.go, ui.ci.go's task runs), which read as "asked by
// 3214" -- a number with nothing to say what it counts. Every other
// requester is a principal's id or a "kind:detail" name grain gives
// itself ("merge-batch:…", "release:…"), and those read as they are.
export function requesterLabel(requestedBy) {
  return /^\d+$/.test(requestedBy) ? `task ${requestedBy}` : requestedBy;
}

// buildLabel is which build of grain produced a run, as the line under
// its heading says it: the short revision, because that line is already
// three facts long, with the whole of it and the commit's own date on
// the hover, which is what somebody comparing this build against a fix
// actually needs (ci.Build).
//
// Empty for a run no build stamped -- every run recorded before grain
// wrote this down -- so that the line says nothing rather than "unknown".
function buildLabel(build) {
  return build && build.revision ? shortCommit(build.revision) : "";
}

function buildTitle(build, zone) {
  if (!build || !build.revision) return "";
  const parts = [`grain build ${build.revision}`];
  if (build.committedAt) {
    parts.push(`committed ${formatDateTime(build.committedAt, zone)}`);
  }
  if (build.modified) parts.push("built from a modified tree");
  return parts.join(" · ");
}

// StepRow is one step of one job: its own conclusion, its exit status,
// how long it took, and -- behind a disclosure -- what it printed.
//
// Collapsed by default and expanded for the step that did not pass, which
// is the one thing anybody opened this run to read. A step that printed
// nothing has no disclosure at all rather than an empty one.
//
// "Did not pass" rather than "failed", because a step has two ways of not
// passing and the second one is the more thankless to go looking for: a
// step that fetches the environment its job runs in -- an image out of a
// content-addressed cache, a pinned tool -- is errored rather than failed
// when it cannot (ci.Step.Infrastructure), and the registry's own refusal
// in its output is the whole of what the person who has to act on it has
// to work from.
//
// The output is rendered in the same monospaced, scrolling <pre> the
// Logs pane uses (.logs-view), because it is the same kind of thing:
// lines somebody scans and copies a fragment out of.
// pkg/ci has already truncated it in the middle past 256KB with a line
// saying so, so what arrives here is bounded and nothing further is done
// to it -- a pane that quietly dropped the tail of a failing test's
// output would be hiding what it was opened for.
const OUTPUT_PINNED_SLACK_PX = 24;

function StepRow({ step }) {
  const failed = step.conclusion === "failure" || step.conclusion === "errored";
  const live = !step.conclusion;
  const [open, setOpen] = useState(failed || live);
  const output = useRef(null);
  const pinned = useRef(true);
  const { label, color } = progressChip(step);

  useEffect(() => {
    if (live) setOpen(true);
  }, [live]);

  useEffect(() => {
    const el = output.current;
    if (!open || !el || !pinned.current) return;
    el.scrollTop = el.scrollHeight;
  }, [open, step.output]);

  const onOutputScroll = useCallback(() => {
    const el = output.current;
    if (!el) return;
    pinned.current =
      el.scrollHeight - el.scrollTop - el.clientHeight <=
      OUTPUT_PINNED_SLACK_PX;
  }, []);
  const span = spanOf(step);
  return (
    <Box sx={{ borderTop: 1, borderColor: "divider", py: 0.75 }}>
      <Box
        sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}
      >
        <Chip size="small" label={label} color={color} />
        <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
          {step.name}
        </Typography>
        {/* -1 is "it never produced one" -- the transport failed, or the
            step was never run -- and 0 is the success the chip beside it
            already says. Neither is worth a column of its own. */}
        {step.exitCode > 0 && (
          <Typography variant="caption" color="text.secondary">
            exit {step.exitCode}
          </Typography>
        )}
        {span !== null && (
          <Typography variant="caption" color="text.secondary">
            {formatDuration(span)}
          </Typography>
        )}
        {(step.output || live) && (
          <Link
            component="button"
            type="button"
            variant="caption"
            underline="hover"
            onClick={() => setOpen((v) => !v)}
          >
            {open ? "Hide output" : "Show output"}
          </Link>
        )}
      </Box>
      {/* A failure of the call rather than of the script (ci.StepRun.
          Error): the step never got to exit at all, so it is said here
          rather than left to be inferred from a missing status. */}
      {step.error && (
        <Alert severity="error" sx={{ mt: 0.5 }}>
          {step.error}
        </Alert>
      )}
      {open && (step.output || live) && (
        <pre
          className="logs-view"
          style={{ marginTop: "0.5rem" }}
          ref={output}
          onScroll={onOutputScroll}
        >
          {step.output || "(waiting for output...)"}
        </pre>
      )}
    </Box>
  );
}

// Where an artifact's bytes live: GET
// /api/ci/runs/{run}/artifacts/{job}/{name} is the only place they are
// served (handleGetCIArtifact), as a gzipped tar with a
// Content-Disposition naming the run and the job it came out of. A plain
// link rather than a fetch: it is a download, the browser is better at
// those than this page is, and there is nothing to render.
const artifactURL = (a) =>
  `/api/ci/runs/${encodeURIComponent(a.run)}/artifacts/${encodeURIComponent(
    a.job,
  )}/${encodeURIComponent(a.name)}`;

// JobPanel is one job: the unit a sandbox is built for, and the row a
// reader scans down. Expanded by default when it failed and collapsed
// otherwise, for the same reason its steps are.
function JobPanel({ job, zone }) {
  const failed = job.conclusion === "failure";
  const running = !job.conclusion && (hasStarted(job) || Boolean(job.sandbox));
  const onMachine = !job.conclusion && Boolean(job.sandbox || hasStarted(job));
  const [expanded, setExpanded] = useState(failed || running);
  const { label, color } = progressChip(job);
  const span = spanOf(job);
  const queued = queuedOf(job);

  useEffect(() => {
    if (running) setExpanded(true);
  }, [running]);

  return (
    <Accordion
      disableGutters
      expanded={expanded}
      onChange={(_, next) => setExpanded(next)}
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1,
            flexWrap: "wrap",
          }}
        >
          <Chip size="small" label={label} color={color} />
          <Typography variant="body2">{jobName(job)}</Typography>
          <Typography variant="caption" color="text.secondary">
            {job.workflow}
            {/* The wait and the work, in that order: a job that queued
                for eighteen minutes and ran for four is not a job that
                took twenty-two, and a job that only ever queued -- one
                cancelled before it got a machine -- has nothing else to
                show for itself at all. */}
            {span !== null || queued !== null
              ? ` · ${formatSpent(span, queued)}`
              : ""}
          </Typography>
          {/* A cache hit means this job did not use a sandbox or rerun its
              steps. Keep that fact out of the muted metadata: it is the
              answer to why a green job completed almost immediately. */}
          {job.cached && (
            <Chip
              size="small"
              label="Cache hit"
              color="info"
              variant="outlined"
            />
          )}
          {/* A job grain is running again in a fresh sandbox after its
              first attempt failed. Without this a job that has been
              going twice as long as usual reads as hung (ci.JobRun.
              Retried, grain/task-1345). */}
          {job.retried && (
            <Chip
              size="small"
              label={`retry ${job.retryCount || 1}/1`}
              color="warning"
              variant="outlined"
            />
          )}
          {job.activity && (
            <Typography variant="caption" color="warning.main">
              {job.activity}
            </Typography>
          )}
        </Box>
      </AccordionSummary>
      <AccordionDetails>
        {/* Not a step's non-zero exit: a sandbox that could not be built,
            a transport that stopped answering, a job timeout. It is why
            a job can be red with no red step under it. */}
        {job.error && (
          <Alert severity="error" sx={{ mb: 1 }}>
            {job.error}
          </Alert>
        )}
        {/* Why there was a retry: the first attempt's own failure, which
            the steps below no longer show -- they are the retry's. */}
        {job.retried && job.retryReason && (
          <Alert severity="warning" sx={{ mb: 1 }}>
            first attempt failed: {job.retryReason}
          </Alert>
        )}
        {/* What the wait above was for, in the words of whatever was
            holding the job: the count, the budget, a hole of its own
            shape, a capability grant, its own siblings. The duration
            says a job was starved rather than slow; this says which
            scarce thing starved it (ci.JobRun.QueuedReason). */}
        {job.queuedReason && (
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ m: 0 }}
          >
            queued {formatDuration(queued)} — {job.queuedReason}
          </Typography>
        )}
        {job.sandbox && (
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ m: 0 }}
          >
            sandbox {job.sandbox}
          </Typography>
        )}
        {job.startedAt && (
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ m: 0 }}
          >
            started {formatDateTime(job.startedAt, zone)}
            {job.finishedAt
              ? ` · completed ${formatDateTime(job.finishedAt, zone)}`
              : ""}
          </Typography>
        )}
        {/* Above the steps, not below them. An artifact exists because
            the steps' output was not enough to explain the failure, and
            a reader who has scrolled to the bottom of a red test suite's
            log has already stopped looking for one. */}
        {(job.artifacts || []).map((artifact) => (
          <Typography
            key={artifact.name}
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ m: 0 }}
          >
            {artifact.stored ? (
              <>
                artifact{" "}
                <Link href={artifactURL(artifact)} download>
                  {artifact.name}
                </Link>{" "}
                ({formatBytes(artifact.bytes)}, .tar.gz)
              </>
            ) : (
              /* A declaration that kept nothing is still worth a line:
                 "280MB, over the 32MB an artifact may be" is what
                 somebody who came looking for the trace needs to be
                 told, and silence reads as a broken feature. */
              <>
                artifact {artifact.name}: {artifact.note}
              </>
            )}
          </Typography>
        ))}
        {(job.steps || []).length === 0 ? (
          onMachine ? (
            <Box sx={{ display: "flex", alignItems: "center", gap: 1, py: 1 }}>
              <CircularProgress size={16} />
              <Typography variant="body2" color="text.secondary">
                Running...
              </Typography>
            </Box>
          ) : (
            <Typography variant="body2" color="text.secondary">
              No steps ran.
            </Typography>
          )
        ) : (
          (job.steps || []).map((step, i) => (
            <StepRow key={`${step.name}-${i}`} step={step} />
          ))
        )}
      </AccordionDetails>
    </Accordion>
  );
}

// RETRYABLE is the conclusions a Retry is offered on: a run that ended
// without a pass. A success has nothing to retry, and a run still going
// has Cancel instead.
const RETRYABLE = new Set(["errored", "failure", "cancelled"]);

// retryableRun says whether run is over without having passed.
export function retryableRun(run) {
  return Boolean(run && RETRYABLE.has(run.conclusion));
}

// runTaskId is the task a run was started for, or "" for one somebody
// started by hand: the orchestrator and a task's own wait_for_checks ask
// for a run in the task's id (requesterLabel above), and a person's run
// carries their principal instead.
export function runTaskId(run) {
  const by = run?.requestedBy || "";
  return /^\d+$/.test(by) ? by : "";
}

// failedJobIds is the job ids of run that did not pass, deduplicated, for
// a by-hand "Retry failed jobs" (ci.Request.Jobs takes a bare job id).
function failedJobIds(run) {
  return [
    ...new Set(
      (run?.jobs || [])
        .filter((j) => RETRYABLE.has(j.conclusion))
        .map((j) => j.jobId || j.name)
        .filter(Boolean),
    ),
  ];
}

// retryRequest is the POST /api/ci/runs body that runs run again.
//
// A run started for a task is retried *as* that task's (ui.CIRunRequest's
// taskId): the server starts the complete suite on the task's own branch
// and records it as the task's required verdict, which is what releases a
// merge held on an errored or cancelled run (grain/task-480, which had to
// be force-merged because nothing here could ask). failedOnly is the
// task's uncached recheck of only the jobs that did not pass, exactly as
// retrigger_ci asks for it. Any other run is dispatched again as it was
// -- repo, ref, commit and event -- narrowed to its failed jobs, uncached,
// for failedOnly. Returns null where failedOnly has no failed jobs to name.
export function retryRequest(run, failedOnly = false) {
  const taskId = runTaskId(run);
  if (taskId) {
    return failedOnly
      ? { taskId, recheck: true, recheckFailed: true }
      : { taskId };
  }
  const body = { repo: run.repo, ref: run.ref };
  if (run.commit) body.commit = run.commit;
  if (run.event) body.event = run.event;
  if (failedOnly) {
    const jobs = failedJobIds(run);
    if (jobs.length === 0) return null;
    body.jobs = jobs;
    body.recheck = true;
  }
  return body;
}

// RunDetail is one run: what it was, how it ended, and its jobs
// failures-first with their steps under them.
export function RunDetail({ run, zone, onBack, onCancel, onRetry }) {
  // "Retry failed jobs" only where it is narrower than Retry: a cancelled
  // run stopped every job it had, and a run with no failed jobs to name
  // (one that errored before it planned any) has nothing to narrow to.
  const retryFailed =
    run.conclusion !== "cancelled" && retryRequest(run, true) !== null;
  const { label, color } = progressChip(run, true);
  const span = spanOf(run);
  const queued = queuedByRun(run);
  return (
    <Box>
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          gap: 1,
          mb: 1,
          flexWrap: "wrap",
        }}
      >
        {onBack && (
          <Button size="small" onClick={onBack}>
            ← All runs
          </Button>
        )}
        <Chip size="small" label={label} color={color} />
        <Typography variant="subtitle2">{run.repo}</Typography>
        {/* The branch wears the branches figure (ItemGlyph.jsx): a ref
            beside a repo name reads as either a branch or a tag, and
            grain CI runs against a branch. The commit under it is what
            was actually tested, in the same phrase, because a branch
            moves on. */}
        <Typography variant="body2" color="text.secondary">
          <GlyphLabel kind="branches">
            {run.ref}
            {run.commit ? ` (${shortCommit(run.commit)})` : ""}
          </GlyphLabel>
        </Typography>
        {run.event && (
          <Typography variant="caption" color="text.secondary">
            {run.event}
          </Typography>
        )}
        {span !== null && (
          <Typography variant="caption" color="text.secondary">
            {formatDuration(span)}
          </Typography>
        )}
        {/* The suite's own queueing, which no single job's says: its
            jobs' waits added together, which is job-time and may well be
            longer than the run above it. A run that spent more job-time
            waiting than working is a deployment short of sandboxes
            rather than a slow suite (ci.Run.Queued). */}
        {queued !== null && (
          <Typography variant="caption" color="text.secondary">
            {formatDuration(queued)} of job-time queued
          </Typography>
        )}
        {!run.conclusion && (
          <Button
            size="small"
            color="error"
            variant="outlined"
            onClick={onCancel}
          >
            Cancel run
          </Button>
        )}
        {onRetry && retryableRun(run) && (
          <Button
            size="small"
            variant="outlined"
            title={
              runTaskId(run)
                ? "Run the complete suite again on this task's branch and record it as the task's grain CI verdict."
                : "Run this again on the same repository, branch and commit."
            }
            onClick={() => onRetry(false)}
          >
            Retry
          </Button>
        )}
        {onRetry && retryableRun(run) && retryFailed && (
          <Button
            size="small"
            variant="outlined"
            title="Run only the jobs that did not pass again, bypassing the cache."
            onClick={() => onRetry(true)}
          >
            Retry failed jobs
          </Button>
        )}
      </Box>
      <Typography
        variant="caption"
        color="text.secondary"
        component="p"
        sx={{ mt: 0, mb: 1.5 }}
      >
        started {formatDateTime(run.startedAt, zone)}
        {run.finishedAt
          ? ` · completed ${formatDateTime(run.finishedAt, zone)}`
          : ""}
        {run.requestedBy
          ? ` · asked by ${requesterLabel(run.requestedBy)}`
          : ""}
        {run.commit ? ` · ${run.commit}` : ""}
        {/* Which build of grain produced this run, which is not the same
            question as which commit it tested: a deployment upgrades on
            its own schedule, and a run behaving unlike the source
            somebody is reading is stale code at least as often as it is
            a bug (ci.Build). */}
        {buildLabel(run.build) ? (
          <Box component="span" title={buildTitle(run.build, zone)}>
            {` · grain build ${buildLabel(run.build)}`}
          </Box>
        ) : null}
      </Typography>
      {/* A run the daemon stopped in the middle of, which grain resolves
          at its next start rather than leaving to look live forever
          (ci.StoredRunStore.RecoverInterrupted). A warning and not an
          error: nothing about this repository or this branch went wrong,
          the answer simply never arrived -- and the jobs below are how
          far it got, which is the whole reason such a run is kept at all
          instead of being deleted. */}
      {run.interrupted ? (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {run.error ||
            "This run was interrupted: the grain daemon running it stopped before it finished."}{" "}
          What follows is as far as it got. A branch whose run was interrupted
          is put through CI again.
        </Alert>
      ) : (
        /* A run-level error is why there are no jobs at all: a repository
           whose workflows could not be read, a .grain that does not
           parse, or a branch none of this repository's workflows answer
           to. A run that got as far as running jobs reports its failures
           on the jobs instead.

           pre-line because the last of those is a list -- every workflow
           the commit declares and what each one answers to (ci's
           unmatchedReason) -- and a list run into one paragraph is the
           part nobody reads. */
        run.error && (
          <Alert severity="error" sx={{ mb: 2, whiteSpace: "pre-line" }}>
            {run.error}
          </Alert>
        )
      )}
      {/* The note whoever stopped this run left. Information rather than
          a warning: a cancellation is a decision somebody made, and this
          is the account of it -- which is the difference between this run
          and one the daemon abandoned above. */}
      {run.cancelReason && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Cancelled: {run.cancelReason}
        </Alert>
      )}
      {(run.jobs || []).length === 0
        ? !run.error && (
            <Alert severity="info">
              No jobs: this repository declares no workflows, so grain CI had
              nothing to run.
            </Alert>
          )
        : sortJobs(run.jobs).map((job) => (
            <JobPanel
              key={`${job.workflowPath}:${job.jobId}`}
              job={job}
              zone={zone}
            />
          ))}
    </Box>
  );
}

// StartRunForm is POST /api/ci/runs with a pair of hands on it: a repo,
// a branch, the workflow at that ref, and the run that comes back opened straight away, since
// somebody who has just asked for a run wants to watch it rather than
// find it again in a table.
//
// Its failures are shown here rather than handed to showError, which is
// the one thing this form is careful about. Every refusal Start makes is
// a fact about what was typed -- a branch that does not resolve, a
// workflow that does not parse, a repo grain CI is not enabled for --
// and the last of those is a sentence naming the two clicks that turn it
// on ("attach the grain-ci capability to this repo (Settings, Repos)…",
// ui.handleStartCIRun). That sentence is only useful beside the field
// that provoked it and while what was typed is still on screen; in the
// app-wide error banner it is advice about a repo the reader can no
// longer see they named.
//
// `repo`, given, is a repo that is not a question: on a repo's own page
// the form is inside that repo's section and there is no repo to pick
// (RepoRuns.jsx, grain/task-351). It is used directly rather than
// seeding the picker's state, so the form still names the right repo
// after a reader walks from one repo's page to another's without this
// component ever unmounting.
export function StartRunForm({
  repo: fixedRepo = "",
  repoOptions,
  onStarted,
  onCancel,
}) {
  const [picked, setPicked] = useState("");
  const repo = fixedRepo || picked;
  const [branch, setBranch] = useState("");
  const [recheck, setRecheck] = useState(false);
  const [workflows, setWorkflows] = useState(null);
  const [workflow, setWorkflow] = useState("");
  const [loadingWorkflows, setLoadingWorkflows] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setWorkflows(null);
    setWorkflow("");
  }, [repo, branch]);

  const loadWorkflows = async () => {
    if (repo.trim() === "" || branch.trim() === "" || loadingWorkflows) return;
    setLoadingWorkflows(true);
    setError("");
    try {
      const found = await api(
        `/api/ci/workflows?repo=${encodeURIComponent(repo.trim())}&ref=${encodeURIComponent(branch.trim())}`,
      );
      const options = found?.workflows || [];
      setWorkflows(options);
      setWorkflow(options.length === 1 ? options[0].path : "");
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setLoadingWorkflows(false);
    }
  };

  const submit = async (e) => {
    e.preventDefault();
    if (repo.trim() === "" || branch.trim() === "" || workflow === "" || busy)
      return;
    setBusy(true);
    setError("");
    try {
      const payload = {
        repo: repo.trim(),
        branch: branch.trim(),
        workflow,
      };
      if (recheck) {
        payload.recheck = true;
      }
      const run = await api("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify(payload),
      });
      onStarted(run);
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box
      component="form"
      onSubmit={submit}
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        p: 1.5,
        mb: 2,
      }}
    >
      {error && (
        <Alert severity="warning" sx={{ mb: 1.5 }}>
          {error}
        </Alert>
      )}
      {fixedRepo ? (
        <Typography variant="body2" sx={{ mb: 1 }}>
          Repo: {fixedRepo}
        </Typography>
      ) : (
        <Box component="label" sx={{ display: "block", mb: 1 }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", mb: 0.5 }}
          >
            Repo
          </Typography>
          <RepoField name="repo" options={repoOptions} onChange={setPicked} />
        </Box>
      )}
      {/* No default of "main": grain CI is mostly run against the branch
          a task pushed, and a prefilled branch box is one nobody reads
          before pressing the button beside it. */}
      <BranchField
        name="branch"
        label="Branch"
        margin="dense"
        repo={repo}
        value={branch}
        onChange={setBranch}
      />
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, mt: 1 }}>
        <Button
          type="button"
          size="small"
          variant="outlined"
          disabled={
            loadingWorkflows || repo.trim() === "" || branch.trim() === ""
          }
          onClick={loadWorkflows}
        >
          {loadingWorkflows ? "Loading workflows…" : "Load workflows"}
        </Button>
        {loadingWorkflows && <CircularProgress size={18} />}
      </Box>
      {workflows?.length > 0 && (
        <TextField
          select
          fullWidth
          size="small"
          margin="dense"
          label="Workflow"
          value={workflow}
          onChange={(e) => setWorkflow(e.target.value)}
        >
          {workflows.map((option) => (
            <MenuItem key={option.path} value={option.path}>
              {option.name} — {option.path}
            </MenuItem>
          ))}
        </TextField>
      )}
      {workflows?.length === 0 && (
        <Alert severity="info" sx={{ mt: 1 }}>
          No workflows on this branch accept a manual run.
        </Alert>
      )}
      <FormControlLabel
        control={
          <Checkbox
            name="recheck"
            checked={recheck}
            onChange={(e) => setRecheck(e.target.checked)}
          />
        }
        label="Bypass cache"
        sx={{ display: "flex", mt: 1 }}
      />
      <Box sx={{ display: "flex", gap: 1, mt: 1 }}>
        <Button
          type="submit"
          size="small"
          variant="contained"
          disabled={
            busy ||
            repo.trim() === "" ||
            branch.trim() === "" ||
            workflow === ""
          }
        >
          Start run
        </Button>
        <Button size="small" onClick={onCancel}>
          Cancel
        </Button>
      </Box>
    </Box>
  );
}

// RunList is the runs themselves, in the order they were handed over --
// which the CI pane decides with its sort (ciFilters.js) and a repo page
// takes as it comes, newest first.
//
// showRepo is false on a listing that is already about one repository
// (RepoRuns.jsx, grain/task-351): a column repeating the page's own
// title in every row is noise. The link that opens a run moves to the
// branch with it, since every row still needs one thing to click and the
// branch is what that listing's reader is scanning down.
//
// filters is the narrowing the rows came through, and the Jobs column is
// what it changes: a list narrowed to one job says what *that* job did
// and how long it took, instead of counting up a dozen jobs the reader
// did not ask about. Reading one job down a column of twenty commits is
// the question that separates a flaky job from a broken change, and it
// is the same thing list_ci_runs does with its own `job` argument.
//
// faults is what grain has worked out about red that is not this
// branch's own (GET /api/ci/runs' own `faults`, ciFaults.js): a failure
// showing on several branches at once is one fault, and the row it is
// drawn under says so instead of leaving every reader to discover the
// same outage separately. Defaulted empty, so a listing that does not
// carry them (an older response, a test) draws exactly what it drew
// before.
export function RunList({
  runs,
  zone,
  onOpen,
  showRepo = true,
  filters = [],
  faults = [],
}) {
  const now = Date.now();
  const columns = showRepo ? 8 : 7;
  return (
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>Result</TableCell>
          {showRepo && <TableCell>Repo</TableCell>}
          <TableCell>Branch</TableCell>
          <TableCell>Event</TableCell>
          <TableCell>Started</TableCell>
          <TableCell>Completed</TableCell>
          <TableCell>Took</TableCell>
          <TableCell>Jobs</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {runs.map((run) => {
          const { label, color } = progressChip(run, true);
          const span = spanOf(run, now);
          const queued = queuedByRun(run);
          const shared = faultsForRun(faults, run);
          return (
            <Fragment key={run.id}>
              <TableRow hover>
                <TableCell>
                  <Chip size="small" label={label} color={color} />
                </TableCell>
                {showRepo && (
                  <TableCell>
                    {/* The run's own link, on the repo rather than on a
                      separate column of ids: an id here is a random hex
                      string nobody reads, and what somebody is looking
                      for in this list is a repository and a branch. */}
                    <Link
                      component="button"
                      type="button"
                      underline="hover"
                      onClick={() => onOpen(run.id)}
                    >
                      {run.repo}
                    </Link>
                  </TableCell>
                )}
                {/* The commit under the branch it was on, and not in a
                  column of its own: they are one answer to "what was
                  tested", and a branch moves on. The full SHA is on the
                  title, since that is what a `git show` takes. */}
                <TableCell sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
                  {showRepo ? (
                    run.ref
                  ) : (
                    <Link
                      component="button"
                      type="button"
                      underline="hover"
                      sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                      onClick={() => onOpen(run.id)}
                    >
                      {run.ref}
                    </Link>
                  )}
                  {run.commit && (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="span"
                      title={run.commit}
                      sx={{ display: "block", fontFamily: "monospace" }}
                    >
                      {shortCommit(run.commit)}
                    </Typography>
                  )}
                </TableCell>
                <TableCell>{run.event}</TableCell>
                <TableCell>{formatDateTime(run.startedAt, zone)}</TableCell>
                <TableCell>
                  {run.finishedAt ? formatDateTime(run.finishedAt, zone) : "—"}
                </TableCell>
                <TableCell>
                  {span === null ? "—" : formatDuration(span)}
                  {/* And what this run's jobs spent waiting for machines
                      before any of that, under the wall clock rather
                      than added to it: the two are different units --
                      one run against the job-time of everything in it --
                      and a suite whose queueing dwarfs its duration is
                      the listing's first sign that this deployment is
                      short of sandboxes (ci.Run.Queued). */}
                  {queued !== null && (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="span"
                      sx={{ display: "block" }}
                    >
                      {formatDuration(queued)} queued
                    </Typography>
                  )}
                </TableCell>
                <TableCell>{namedJobSummary(run, filters, now)}</TableCell>
              </TableRow>
              {shared.map((fault) => (
                <SharedFaultRow
                  key={fault.digest}
                  fault={fault}
                  branch={run.ref}
                  columns={columns}
                />
              ))}
            </Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
}

// SharedFaultRow is the line under a red row that says the red is not
// this branch's own: which job, how many other branches are failing it
// the same way right now, and -- where grain can say so without guessing
// -- the open pull request that touches the file they all name
// (ciFaults.js, pkg/ci/sharedfault.go).
//
// A line of its own under the row rather than a chip in it, because it
// is a sentence about somewhere else: a chip would have to be read as a
// property of this run, which is the exact misreading the whole feature
// exists to correct. Warning colour rather than error, for the reason
// ConclusionErrored takes one -- what it points at is the deployment,
// not this commit.
function SharedFaultRow({ fault, branch, columns }) {
  const summary = faultSummary(fault, branch);
  if (!summary) return null;
  const src = faultSource(fault);
  return (
    <TableRow className="ci-fault-row">
      <TableCell colSpan={columns} sx={{ py: 0.3, pl: 2 }}>
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 0.75,
            flexWrap: "wrap",
          }}
        >
          <Chip
            size="small"
            variant="outlined"
            label={faultSourceLabel(fault)}
            className="ci-fault-source"
            data-testid={`ci-fault-source-${src}`}
            sx={{ height: 18, fontSize: "0.7rem" }}
          />
          <Typography
            variant="caption"
            color="warning.main"
            component="span"
            title={faultDetail(fault, branch)}
          >
            {summary}
          </Typography>
        </Box>
        {faultFixes(fault).map((fix) => (
          <Typography
            key={fix.pullRequest}
            variant="caption"
            color="text.secondary"
            component="span"
            title={fix.title ? `${fix.task}: ${fix.title}` : undefined}
            sx={{ display: "block" }}
          >
            <Link
              href={pullRequestUrl(fix.pullRequest)}
              target="_blank"
              rel="noopener noreferrer"
              underline="hover"
            >
              {fix.pullRequest}
            </Link>{" "}
            {fixWhere(fix)}
          </Typography>
        ))}
      </TableCell>
    </TableRow>
  );
}

// SHARED_FAULTS_STORAGE_KEY is where this browser remembers that the
// shared faults box was folded away, and which faults it held when it
// was: {"seen": [sharedFaultKey, ...]}. Expanded is the default and is
// stored as no key at all.
export const SHARED_FAULTS_STORAGE_KEY = "grain.ci.sharedFaults.collapsed";

// sharedFaultKey names one fault across polls: the same failure on the
// same job in the same repo keeps its digest however many branches it
// spreads to.
export function sharedFaultKey(fault) {
  return `${faultSource(fault)}:${fault.repo}:${fault.digest}`;
}

// newSharedFaults is the faults that were not in the box when it was
// collapsed -- what the collapsed header has to own up to, so folding
// the box away never hides a fresh failure.
export function newSharedFaults(faults, seen) {
  const known = new Set(seen || []);
  return (faults || []).filter((f) => !known.has(sharedFaultKey(f)));
}

function readSharedFaultsCollapsed() {
  try {
    const raw = localStorage.getItem(SHARED_FAULTS_STORAGE_KEY);
    if (raw === null) return null;
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed?.seen) ? parsed.seen : [];
  } catch {
    return null;
  }
}

function writeSharedFaultsCollapsed(seen) {
  try {
    if (seen) {
      localStorage.setItem(SHARED_FAULTS_STORAGE_KEY, JSON.stringify({ seen }));
    } else {
      localStorage.removeItem(SHARED_FAULTS_STORAGE_KEY);
    }
  } catch {
    // A browser that will not store it just forgets the choice.
  }
}

// SharedFaultsSection shows shared CI faults that are not already attached to a
// visible grain-CI run row below -- in particular GitHub CI shared faults, and
// grain CI faults when the run table is empty or narrowed away from their runs.
//
// With several faults open the cards fill the top of the page, so the
// header folds them away (grain/task-1520) and the choice is remembered
// per browser. A collapsed box remembers which faults it was hiding, and
// any fault that turns up after that is counted on the header as new
// rather than opening the box by itself.
function SharedFaultsSection({ faults = [] }) {
  // null while expanded; the keys of the faults it held once collapsed.
  const [collapsedSeen, setCollapsedSeen] = useState(readSharedFaultsCollapsed);
  if (!faults || faults.length === 0) return null;
  const expanded = collapsedSeen === null;
  const fresh = expanded ? [] : newSharedFaults(faults, collapsedSeen);
  const toggle = () => {
    const next = expanded ? faults.map(sharedFaultKey) : null;
    writeSharedFaultsCollapsed(next);
    setCollapsedSeen(next);
  };
  const heading = `${faults.length} shared ${
    faults.length === 1 ? "failure" : "failures"
  }`;
  return (
    <Box
      className="ci-shared-faults"
      data-testid="ci-shared-faults-section"
      sx={{ mb: 2 }}
    >
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          flexWrap: "wrap",
          columnGap: 1,
          mb: expanded ? 0.75 : 0,
        }}
      >
        <Typography variant="subtitle2" color="warning.main">
          <ButtonBase
            aria-expanded={expanded}
            aria-controls="ci-shared-faults-list"
            data-testid="ci-shared-faults-toggle"
            onClick={toggle}
            sx={{
              gap: 1,
              font: "inherit",
              borderRadius: 1,
              "&.Mui-focusVisible": {
                outline: 2,
                outlineColor: "primary.main",
              },
            }}
          >
            <ExpandMoreIcon
              fontSize="small"
              aria-hidden
              sx={{
                transform: expanded ? "rotate(0deg)" : "rotate(-90deg)",
                transition: "transform 150ms",
              }}
            />
            {heading}
          </ButtonBase>
        </Typography>
        {fresh.length > 0 && (
          <Chip
            size="small"
            color="error"
            label={`${fresh.length} new`}
            data-testid="ci-shared-faults-new"
            title={`Not here when this box was collapsed: ${fresh
              .map((f) => `${f.repo} · ${f.jobName || f.jobId}`)
              .join(", ")}`}
            sx={{ height: 20, fontSize: "0.72rem" }}
          />
        )}
      </Box>
      <Collapse in={expanded} id="ci-shared-faults-list">
        <Box sx={{ display: "flex", flexDirection: "column", gap: 1 }}>
          {faults.map((fault) => {
            const src = faultSource(fault);
            const job = fault.jobName || fault.jobId;
            const refs = fault.refs || [];
            const paths = fault.paths || [];
            return (
              <Box
                key={`${src}-${fault.digest}`}
                className="ci-shared-fault-card"
                data-testid={`ci-shared-fault-${src}`}
                sx={{
                  border: 1,
                  borderColor: "warning.main",
                  borderRadius: 1,
                  p: 1.25,
                  bgcolor: "action.hover",
                }}
              >
                <Box
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    gap: 1,
                    flexWrap: "wrap",
                    mb: 0.5,
                  }}
                >
                  <Chip
                    size="small"
                    color="warning"
                    variant="outlined"
                    label={faultSourceLabel(fault)}
                    className="ci-fault-source"
                    data-testid={`ci-fault-source-${src}`}
                    sx={{ height: 20, fontSize: "0.72rem" }}
                  />
                  <Typography
                    variant="body2"
                    sx={{ fontWeight: 600, wordBreak: "break-word" }}
                  >
                    {`${fault.repo} · ${job}${fault.step ? ` (${fault.step})` : ""}`}
                  </Typography>
                  <Typography variant="caption" color="warning.main">
                    {`failing the same way on ${refs.length} ${refs.length === 1 ? "branch" : "branches"} right now`}
                  </Typography>
                </Box>
                {refs.length > 0 && (
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ display: "block", wordBreak: "break-word" }}
                  >
                    {`Branches: ${refs.join(", ")}`}
                  </Typography>
                )}
                {paths.length > 0 && (
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ display: "block", wordBreak: "break-word" }}
                  >
                    {`Common files: ${paths.join(", ")}`}
                  </Typography>
                )}
                {faultFixes(fault).map((fix) => (
                  <Typography
                    key={fix.pullRequest}
                    variant="caption"
                    color="text.secondary"
                    title={fix.title ? `${fix.task}: ${fix.title}` : undefined}
                    sx={{ display: "block", mt: 0.25 }}
                  >
                    Open fix:{" "}
                    <Link
                      href={pullRequestUrl(fix.pullRequest)}
                      target="_blank"
                      rel="noopener noreferrer"
                      underline="hover"
                    >
                      {fix.pullRequest}
                    </Link>{" "}
                    {fixWhere(fix)}
                  </Typography>
                ))}
              </Box>
            );
          })}
        </Box>
      </Collapse>
    </Box>
  );
}

// namedJobSummary is the Jobs cell: the run's own tally, or -- where the
// list is narrowed to one job -- that job's result and duration, which
// is what was actually asked about.
function namedJobSummary(run, filters, now = Date.now()) {
  const named = jobsNamed(run, filters);
  if (!named) return jobSummary(run);
  return named
    .map((job) => {
      const { label } = conclusionChip(jobConclusionOf(job));
      const span = spanOf(job, now);
      return span === null
        ? `${jobName(job)}: ${label.toLowerCase()}`
        : `${jobName(job)}: ${label.toLowerCase()} in ${formatDuration(span)}`;
    })
    .join(" · ");
}

// formatBytes is the Sandboxes and Caches panes' own (PackageCachePanel's):
// 1024-based, coarse, and labelled as the binary units it counts in. This
// pane once kept a copy of its own that stopped at "MB", so the same job
// cache read "256.0 MB" here and "256.0 MiB" on System → Sandboxes, and a
// 20 GiB ceiling read "20480.0 MB".
export { formatBytes };

// cacheSummary is the job cache in one line: what it is holding against
// what it may hold, and how much of the work it was asked about it
// answered for.
//
// The hit rate says "since this daemon started" because that is how far
// the counters go -- they live in memory, so a restart leaves a full
// cache and an empty tally, and a reader who took 0% for "this cache is
// not working" would be reading a daemon that came up a minute ago.
export function cacheSummary(cache) {
  const parts = [
    `${cache.entries} ${cache.entries === 1 ? "entry" : "entries"}`,
    `${formatBytes(cache.bytes)} of ${formatBytes(cache.maxBytes)}`,
  ];
  const lookups = (cache.hits || 0) + (cache.misses || 0);
  parts.push(
    lookups > 0
      ? `${Math.round((cache.hits / lookups) * 100)}% of ${lookups} lookups hit since this daemon started`
      : "no lookups since this daemon started",
  );
  return parts.join(" · ");
}

// CacheLine is the job cache under the CI pane's own heading: what it
// holds, and the one thing an operator does to it (grain/task-304).
//
// A footnote rather than a section, because that is what it is worth
// next to the runs: two numbers and a hit rate, for the two questions
// the daemon's journal answered only in passing -- is this saving
// anything, and is it about to fill the data disk.
//
// Clearing asks first, and says in the asking that nothing is lost: a
// cache entry is a promise that identical work was already done, so the
// worst an empty cache costs is the re-runs it was saving. The daemon
// refuses this for a deployment with grain CI turned on nowhere, and
// that refusal names the capability to attach.
function CacheLine({ cache, onClear }) {
  if (!cache || !cache.enabled) return null;
  if (cache.error) {
    return (
      <Alert severity="warning" sx={{ mb: 2 }}>
        The job cache could not be read: {cache.error}
      </Alert>
    );
  }
  // Not "the cache is empty": there is no cache here at all, which is a
  // different thing to go and look into. The daemon logs why it could
  // not open one, once, at startup.
  if (!cache.available) {
    return (
      <Typography variant="caption" color="text.secondary" sx={{ mb: 1 }}>
        No job cache: every job runs, every time.
      </Typography>
    );
  }
  const stats = cache.cache;
  if (!stats) return null;
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: 1,
        flexWrap: "wrap",
        mb: 1,
      }}
    >
      {/* The directory is on the title rather than in the line: it is
          what an operator on the host needs and what nobody reading the
          pane does. */}
      <Typography variant="caption" color="text.secondary" title={stats.dir}>
        Job cache: {cacheSummary(stats)}
      </Typography>
      <Button size="small" onClick={onClear}>
        Clear cache
      </Button>
    </Box>
  );
}

function formatCIReadError(err) {
  if (isUnreachable(err) && !err?.explained) {
    return unreachableReason(err);
  }
  return err?.message || String(err);
}

export default function CIPage({ config, onOpenRun, initialRepo, showError }) {
  const [data, setData] = useState(null);
  // Why the last poll of the run list failed, or "" once one succeeds.
  // Without it a list whose first read failed spun "Loading CI runs" for
  // as long as the pane was open, raising a new snackbar on every poll.
  const [listError, setListError] = useState("");
  // Whether the polls are failing right now, so a run of failures is
  // reported once rather than every REFRESH_MS.
  const listFailing = useRef(false);
  const listInFlight = useRef(0);
  const listSeq = useRef(0);
  const listUnreachableStreak = useRef(0);
  const cacheInFlight = useRef(0);
  const cacheSeq = useRef(0);
  // The question this pane is asking: the search box, the conditions and
  // the order, kept in this browser so that coming back to the pane does
  // not reset it (ciFilters.js).
  //
  // initialRepo is the repo this pane was opened *about*: a repo page's
  // own runs list sending its reader here for the rest of them (grain/
  // task-351). Seeded rather than watched -- it is where the filter
  // starts, not one the reader may not change. Seeding is enough because
  // this page is only mounted while the CI
  // view is showing (App.jsx): arriving from a repo page mounts it
  // afresh, which is when the repo it was opened about is decided.
  const [narrowing, narrow] = useCINarrowing(initialRepo);
  const [starting, setStarting] = useState(false);
  // seenRepos is every repo this pane has watched a run go past for --
  // one of the two sources the "Start a run" form offers repos from. The
  // deployment's allowlist is the other and usually the better one, but
  // it can be empty (an unrestricted deployment names no target repos at
  // all) and a run against a repo that is not on the list is perfectly
  // ordinary. Kept across polls rather than read off the current answer,
  // since a deployment that ran something an hour ago is still a
  // deployment somebody may want to run something on now.
  const [seenRepos, setSeenRepos] = useState([]);
  // cache is GET /api/ci/cache as it stands, plus an `error` of this
  // pane's own for a read that failed. A cache that has stopped
  // answering is worth saying out loud -- from the outside it looks
  // exactly like a cache that is empty -- but it is a footnote, so it
  // does not take the runs table down with it.
  const [cache, setCache] = useState(null);
  const zone = useTimeZone();

  // A page of what this deployment remembers, and the pane's own
  // question asked of it in the browser (narrowRuns below) -- see
  // ciFilters.js's repoNarrowedTo on why none of the narrowing is asked
  // of the daemon, though the route understands all of it.
  //
  // A page rather than the whole of it: the poll carries a limit, and
  // the window the reader has asked to look back over where they have
  // asked for one (runsQuery), so the ordinary table stays cheap while
  // "started within the last 14 days" reaches as far back as the
  // deployment keeps (grain/task-289).
  //
  // The two things the poll's own question is made of are named here
  // rather than closed over whole, so that the interval below restarts
  // when the reader changes the window and not every time this pane
  // rerenders.
  const pollFilters = narrowing.filters;
  const pollZone = config?.timeZone;
  const refreshList = useCallback(
    async ({ background = false } = {}) => {
      if (background && listInFlight.current > 0) return;
      const seq = ++listSeq.current;
      listInFlight.current += 1;
      try {
        const listed = await api(
          "/api/ci/runs" +
            runsQuery({ filters: pollFilters }, { timeZone: pollZone }),
        );
        if (seq !== listSeq.current) return;
        setData(listed);
        setListError("");
        listFailing.current = false;
        listUnreachableStreak.current = 0;
        const repos = (listed?.runs || []).map((r) => r.repo).filter(Boolean);
        // Returning prev unchanged when nothing is new matters: this runs
        // every five seconds, and a fresh array each time would rerender
        // the pane for no reason.
        setSeenRepos((prev) =>
          repos.every((r) => prev.includes(r))
            ? prev
            : [...new Set([...prev, ...repos])],
        );
      } catch (err) {
        if (seq !== listSeq.current) return;
        if (background && isUnreachable(err)) {
          listUnreachableStreak.current += 1;
          if (listUnreachableStreak.current < 2) return;
        } else {
          listUnreachableStreak.current = isUnreachable(err) ? 2 : 0;
        }
        setListError(formatCIReadError(err));
        if (!listFailing.current && showError) showError(err);
        listFailing.current = true;
      } finally {
        listInFlight.current = Math.max(0, listInFlight.current - 1);
      }
    },
    [showError, pollFilters, pollZone],
  );

  const refreshCache = useCallback(async ({ background = false } = {}) => {
    if (background && cacheInFlight.current > 0) return;
    const seq = ++cacheSeq.current;
    cacheInFlight.current += 1;
    try {
      const next = await api("/api/ci/cache");
      if (seq !== cacheSeq.current) return;
      setCache(next);
    } catch (err) {
      if (seq !== cacheSeq.current) return;
      setCache({ enabled: true, error: formatCIReadError(err) });
    } finally {
      cacheInFlight.current = Math.max(0, cacheInFlight.current - 1);
    }
  }, []);

  // The listing remains mounted and current while a run overlay is open,
  // so closing the run returns to a fresh table.
  useEffect(() => {
    refreshList();
    const interval = setInterval(
      () => refreshList({ background: true }),
      REFRESH_MS,
    );
    return () => clearInterval(interval);
  }, [refreshList]);

  useEffect(() => {
    refreshCache();
    const interval = setInterval(
      () => refreshCache({ background: true }),
      CACHE_REFRESH_MS,
    );
    return () => clearInterval(interval);
  }, [refreshCache]);

  // Clearing is safe and is not free, which is exactly what the
  // question says: no warning about losing anything, because nothing is
  // lost, and a plain account of what it costs instead.
  const clearCache = async () => {
    if (
      !confirm(
        "Empty the CI job cache? Nothing is lost -- a cached job simply runs " +
          "again next time -- so this costs the re-runs the cache was saving.",
      )
    )
      return;
    try {
      setCache(await api("/api/ci/cache", { method: "DELETE" }));
    } catch (err) {
      // The daemon's own refusal is worth relaying whole: a deployment
      // with grain CI turned on nowhere is told which capability to
      // attach and where.
      showError(err);
      refreshCache();
    }
  };

  // The repos this pane can offer: the deployment's own allowlist, plus
  // whatever has actually run here. knownRepos takes tasks as its second
  // source and this pane has none -- it is not a task view -- so the
  // runs stand in for them. And whichever repo this pane was opened
  // about, which neither source is bound to name: a repo that is on no
  // allowlist and has never run anything here is one this pane would
  // otherwise be filtering by with no such option to unpick.
  const repoOptions = [
    ...new Set(
      [...knownRepos(config, []), ...seenRepos, initialRepo || ""].filter(
        Boolean,
      ),
    ),
  ].sort((a, b) => a.localeCompare(b));

  // What came back, and what this pane's own question makes of it. The
  // menus and the "nothing matches" line are both about the difference
  // between the two.
  const fetched = data?.runs || [];
  const shown = narrowRuns(fetched, narrowing, config);
  const inlineGrainFaultDigests = new Set(
    shown.flatMap((run) =>
      faultsForRun(data?.faults, run).map(
        (f) => `${faultSource(f)}:${f.digest}`,
      ),
    ),
  );
  const standaloneFaults = (data?.faults || []).filter(
    (f) => !inlineGrainFaultDigests.has(`${faultSource(f)}:${f.digest}`),
  );

  const onStarted = (started) => {
    setStarting(false);
    refreshList();
    if (started?.id) onOpenRun(started.id);
  };

  return (
    <main className="ci-panel standalone-page">
      {/* "CI" rather than "CI runs": the page holds two sections, what is
          running and what has run, and the second says its own name below.
          ListHeader gives this destination the same heading, gutters and
          divider as the other pages opened from the rail. */}
      <ListHeader
        title="CI"
        icon={<ItemGlyph kind="ci" size={20} />}
        action={
          data?.enabled && (
            <Box
              sx={{
                display: "flex",
                alignItems: "center",
                gap: 1,
                flexWrap: "wrap",
                ml: "auto",
              }}
            >
              {/* These controls belong to the listing, not to the run
                overlay that may be open above it. The narrowing that
                used to sit here -- a single "Repo filter" menu -- is the
                toolbar over the runs table below (grain/task-231). */}
              <Button
                size="small"
                variant="outlined"
                onClick={() => setStarting((v) => !v)}
              >
                {starting ? "Close" : "Start a run"}
              </Button>
              <Button
                size="small"
                variant="outlined"
                onClick={() => {
                  refreshList();
                  // The cache polls slowly on its own, so somebody who has
                  // just pressed Refresh is the one reader who wants it
                  // asked after now.
                  refreshCache();
                }}
              >
                Refresh
              </Button>
            </Box>
          )
        }
      />
      {/* Under the heading and above everything else on the listing:
          it is the state of the pane rather than a row in it, and it is
          the answer to "why was that run so quick" that the rows
          themselves only hint at with "· cached". */}
      <CacheLine cache={cache} onClear={clearCache} />
      {data?.enabled && starting && (
        <StartRunForm
          repoOptions={repoOptions}
          onStarted={onStarted}
          onCancel={() => setStarting(false)}
        />
      )}
      {data === null && !listError && (
        <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
          <CircularProgress size={28} aria-label="Loading CI runs" />
        </Box>
      )}
      {data === null && listError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          CI runs could not be listed ({listError}). This pane tries again every
          few seconds.
        </Alert>
      )}
      {data !== null && listError && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The latest read of CI runs failed ({listError}), so the list below is
          from the last one that succeeded. This pane tries again every few
          seconds.
        </Alert>
      )}
      {/* Not an empty list: this deployment's daemon was started with
          -ci=false, so there is no engine here to have run anything, and
          a table with no rows in it would read as "nothing has run yet".
          ui.CIRunsResponse.Enabled is the flag that tells the two
          apart. */}
      {data !== null && !data.enabled && (
        <Alert severity="info" sx={{ mb: standaloneFaults.length > 0 ? 2 : 0 }}>
          Not available: this deployment does not run grain CI. Its daemon was
          started with -ci=false, so there is no engine here to have run
          anything.
        </Alert>
      )}
      {data !== null && <SharedFaultsSection faults={standaloneFaults} />}
      {/* What CI is spending right now, above what it has spent: a
          reader who came to see whether anything is running should not
          have to find the un-concluded rows in a table of a hundred
          finished ones. Only where there is an engine to be running
          anything -- a deployment with CI turned off has already been
          told so above. */}
      {data !== null && data.enabled && (
        <ActiveSandboxes active={activeSandboxes(fetched)} onOpen={onOpenRun} />
      )}
      {data !== null && data.enabled && (
        <>
          <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
            Runs
          </Typography>
          {/* The same toolbar every task view wears, over this
              vocabulary instead of a task's (ciFilters.js): repo,
              branch, conclusion, job and a time window, in the same
              words list_ci_runs takes them in. Its menus are built from
              every run the pane has fetched rather than from the ones
              currently shown, so narrowing to one conclusion does not
              take the other conclusions out of its own menu. */}
          <NarrowingBar
            tasks={fetched}
            config={config}
            attributes={RUN_ATTRIBUTES}
            narrowing={narrowing}
            onNarrow={narrow}
            searchPlaceholder="Search runs…"
            orderLabel="No order"
            orderNote="The order the daemon holds them in: newest first."
          />
        </>
      )}
      {data !== null &&
        data.enabled &&
        (fetched.length === 0 ? (
          // Nothing has ever run here, which is a different emptiness
          // from the one below -- and the only one this line may claim,
          // now that the daemon is asked for everything it remembers and
          // the narrowing happens here.
          <Alert severity="info">
            No CI runs yet. Start a run above, against a repo and a branch. A
            repo grain CI has not been enabled for is refused rather than run:
            that takes the grain-ci capability, on the repo (Settings, Repos) or
            on this deployment&apos;s defaults.
          </Alert>
        ) : shown.length === 0 ? (
          // The other emptiness, and the one worth saying out loud: runs
          // have happened and are right here, and this question is not
          // about any of them. A blank table under a toolbar reads as a
          // pane that has broken.
          <Alert severity="info">
            No CI run matches. {fetched.length}{" "}
            {fetched.length === 1 ? "run is" : "runs are"} here, and none of
            them answers this -- widen a filter, or press Clear above to ask
            about all of them again.
          </Alert>
        ) : (
          <RunList
            runs={shown}
            zone={zone}
            onOpen={onOpenRun}
            filters={narrowing.filters}
            faults={data.faults}
          />
        ))}
    </main>
  );
}
