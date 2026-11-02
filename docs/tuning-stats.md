# The numbers a tuning job needs

This is the stats half of "a nightly run that looks at infrastructure and
suggests parameters to tune" (grain/task-495; the schedule itself is its
companion, grain/task-496 — see `docs/infrastructure-review.md`). It says
what this deployment now measures about its own
throughput and contention, how an agent reads it, and what was
deliberately left unbuilt.

## The evening it was written for

An operator asked whether throughput was healthy. Answering it took a
Director session about fifteen minutes of hand-assembly:

- `go` jobs were running **31–34 minutes** where the same job had taken
  **15–26 minutes** earlier in the evening -- not work, but 8-vCPU jobs
  contending at 3× overcommit.
- The host was at **99.4% CPU, load 47.4 on 28 cores**, every CI slot
  busy, jobs queueing 7–21 minutes for one.
- On that evidence `max_ci_jobs` went from 10 to 7.

Every number was reconstructed by eye from `list_ci_runs` output plus one
instantaneous `read_deployment_health` reading. None of it was queryable
and none of it was kept, so "is this better or worse than last week" --
the only question a tuning agent ever asks -- had no answer available even
in principle.

## How an agent reads it

Through **`read_grain_metrics`**, which is `grain metrics` rendered for a
run holding `self-debug`, over `GET /api/metrics`. One call takes a
`window` (or `since`/`until`), an optional repo/author scope, and an
optional `section`. The sections that matter here:

| section | what it answers |
| --- | --- |
| `ci_queueing` | per job: how long it waited, how long it **ran**, and both across the window slice by slice |
| `github_ci` | the CI beside grain's own, labelled `source: github`: pass, fail and cancel rates per workflow/job, time to green per commit, flaky jobs and base-branch red time ([github-ci.md](github-ci.md)) |
| `host` | load, CPU and memory over the same slices |
| `changes` | when one of this deployment's own parameters moved, and which |
| `merges` | what actually landed: changes merged per hour, filed-to-landed time, and the merge queue's batch sizes and pass rate |
| `capacity`, `cycles` | whether the deployment was full, and what the scheduling tick itself cost |
| `checks` | how long runs were blocked on CI, pushes before green, and how CI-fixing runs worked a red build (the share that ran a narrowed local test before the fixing push, and the share that re-ran CI selectively) |

Every series in the report is bucketed on **the same slices**, so the three
lines a tuning decision needs -- what the jobs took, what the host was
doing, and what was changed -- are points over the same spans and can be
read as one picture. That alignment is the design, not a coincidence: ask
each of them for the same window and the points line up.

The number of points is `metrics.DefaultBuckets` (12), so the window is
what sets the resolution -- half a day reads as hours, a season as weeks.
`?buckets=` on `GET /api/metrics` overrides it for the UI, but neither
`grain metrics` nor `read_grain_metrics` passes one, so an agent choosing
a resolution does it by choosing a window.

The intended shape of a nightly run's reasoning is two calls, not one:
the window since the last change, and the window of equal length before
it. `changes` is what tells it where to put the boundary.

## What is measured

**Per CI job: run duration against queue duration, over time.** The
highest-value series, because the two have opposite fixes -- a job that
*ran* longer says reduce concurrency, a job that *waited* longer says
raise it -- and a single "slow CI" figure cannot tell them apart.
`ci_queueing` reports the run distribution beside the wait distribution
overall, per repository and per job, and gives the busiest few jobs their
own per-slice series of both (`metrics.SeriesGroups` bounds how many).
Derived from the `ci_job` rows that already existed; nothing new is
stored.

**Host load, CPU and memory over time.** `pkg/sysstat`'s `History` folds
one reading a minute into hourly rollups in a bounded ring, and the report
re-buckets them onto the window's slices. **This is in memory and starts
again with the daemon** -- `host.reaches` and `host.ringHours` say how far
back it actually goes, so a short history cannot read as a quiet
fortnight. That bound is stated rather than hidden: see "what was left"
below.

**Admission holds, by cause.** Already built (grain/task-345):
`ci_queueing.byCause` counts waiting under `ci.WaitCause`'s own vocabulary
-- `slot`, `budget`, `hole`, `grant`, `run`, `priority` -- with the gate's
own sentence from the longest wait of each. That is the field that says
which limit is binding, and each cause points at a different setting. This
change extended it rather than adding a second count of the same thing.

**Cache effectiveness over a window.** `ci_queueing.cacheHitShare` is
cache hits over every job asked for during the window, which a cumulative
hit/miss counter cannot be: totals since boot only ever go up, so a cache
that broke an hour ago still reads as mostly hits.

**Merge throughput, and what it cost.** `merges` (grain/task-556), added
after the three series above and on the same slices. Three things in one
section, because they are one decision. Changes merged per hour over the
window and per slice -- `throughput` counts tasks *completed*, which is
an agent stopping, and nothing counted the moment a change actually
landed. Filed-to-landed, completed-to-landed and opened-to-landed as
distributions, off `Observation.PrMergedAt`, which is the wait a person
outside this deployment actually feels and which `latency` stops short
of. And the batch history as a rate over the window: size distribution,
first-try pass rate, splits, culprits, and the suites batching spent
against what a one-at-a-time queue would have spent
(`model.MergeBatchRecord`, 200 finished batches, counted through
`model.SummarizeMergeBatches` so this and the Settings pane beside the
batch size cannot disagree).

The two halves were put in one section because the batch size moved
both. Merge batching, and `merge_batch_size` with it, were removed in
grain/task-1023, so the batch half now only reports the history recorded
before that (and the single-member overlap re-checks that still write
`model.MergeBatchRecord` rows); there is no batch size left to tune.
The batch half is narrowed by `?repo=` and not by `?author=`, for the
reason `ci_queueing` is not: a batch belongs to a repository and a base
branch and records no members at all.

**GitHub CI, by source.** `github_ci` (`metrics.GitHubCI`,
`pkg/metrics/githubci.go`) is the CI a repository runs that grain does
not -- GitHub Actions jobs and other providers' check runs, recorded in
`github_ci_job` -- and every number and row in it carries `source:
github`. It is computed from those rows alone: the grain-CI sections
(`ci_queueing`, `checks`, `merges`) read `ci_run` exactly as they did
before it existed, and `TestGitHubCILeavesGrainCIAlone` holds the rest of
the report byte-identical with and without GitHub rows. What it reports:

- **Rates per job** (`workflow / job`, or a check's own name): passed,
  failed (`failure`, `timed_out`, `startup_failure`) and cancelled, each a
  share of the three together. Skipped and neutral are counted apart and
  in no denominator, so a workflow that skips most of its jobs cannot look
  green; anything else GitHub says (`action_required`, `stale`) is `other`.
- **Time to green**, per commit: from its first failed job to the moment
  every job it had reported was passing again (a pass, skip or neutral; a
  cancelled job is neither). A commit that went red before the window and
  green inside it is measured from its own first failure. `stillRed` is
  the commits that went red in the window and had not recovered by its end.
- **Flaky jobs**: a job that failed and then passed on the same commit,
  by a higher run attempt or a later run, counted once per commit.
- **Base-branch red/green time** on the branches grain watches
  (`base_branch_ci`): red from the moment the branch's newest commit has a
  job whose latest result failed, until a newer commit or a re-run leaves
  none failed. A late result for a commit the branch has moved past is
  ignored.

Narrowed by `?repo=` and not by `?author=`, for `ci_queueing`'s reason. It
reads no text: `Store.GitHubCIOutcomes` selects identity and conclusion
only, not the outputs and failure tails stored beside them.

**When a parameter changed.** `changes`, read off the state repository's
git history (`staterepo.SettingsChanges`): one `git log -U0` over the same
settings allowlist `staterepo.Apply` imports into a running daemon, parsed
into which file, which key, and -- where the value is a number or a
boolean -- what it went from and to. A value that is *not* a dial is named
and **not** quoted: settings rows hold prompts, paths, credential names and
capability lists beside their numbers, and this report is served to agents
and to anyone with the UI open.

## Auto overcommit

The `host` section also carries `host.overcommit` on a deployment whose
sandboxes run on the daemon's machine. It holds the overcommit mode, the
CPU and memory ratios the budget currently admits against, the last
PSI/swap-in reading, OOM kills, and every adjustment the auto overcommit
controller made inside the window, each with the reading that caused it.
Read those adjustments against `throughput` and `capacity`. A rise in
the memory ratio followed by OOM kills and a cut is the controller
working as designed: it gives up a few retried sandboxes in exchange for
admitting more work. Memory starts at 1.0 because of grain/task-173. See
[design/auto-overcommit.md](design/auto-overcommit.md) for the
controller, its bounds and its targets.

## Retention, stated

- **CI job rows** are swept with the CI history (`ci.StoredRunRetention`),
  so a long window covers however much of it is still stored. The buckets
  show it -- the oldest go empty -- and `jobs` is the denominator to read
  every share against.
- **GitHub CI rows** are kept 14 days (docs/github-ci.md), so a longer
  window's `github_ci` covers the last fortnight only; `jobs` is the
  denominator to read it against.
- **Host rollups** are hourly, in memory, bounded by
  `sysstat.DefaultHistoryHours` (two weeks), a few kilobytes, and lost on
  restart.
- **Merge batch rows** are bounded at `model.MergeBatchHistoryKept` (200
  finished batches), which on a busy deployment is weeks rather than
  months. `merges.batches.reaches` is the oldest one the report got to,
  said whether the window held any batches or not, so a window reaching
  back past the bound cannot read as a fortnight nobody batched in. The
  landing rate beside it has no such bound: it is read off the tasks
  themselves, which are kept.
- **Settings changes** are git's, which keeps everything; the reader
  bounds one call by `staterepo.DefaultSettingsChanges` commits and by the
  window it was asked for.
- **Nothing here adds a table, a counter on a hot path or a row per
  sample.** The whole report is derived on demand from records that already
  exist, which is `pkg/metrics`' standing rule and why no part of this can
  disagree with the thing it describes.

## What was left, and why

**Merge throughput** was left out of this change and filed separately, as
grain/task-556; it landed as the `merges` section above, which is why it
is now described under "what is measured" rather than here.

**A pane for any of this.** The Metrics pane draws throughput, capacity,
latency, the tool census, checks, CI queueing, the backlog and sandboxes;
`host`, `changes` and `merges` are served over `GET /api/metrics` and
printed by `grain metrics`, and nothing draws them yet. That is deliberate
in the same way the rest of this list is: the reader these three were
built for is the nightly job, which reads the report through
`read_grain_metrics` section by section, and a chart is worth building on
the evidence of the series being used rather than ahead of it.

**The sandbox guest cache's hit rate.** `ciJobCache` hits are now a rate
over a window; the guest cache's are not, because the numbers are not in a
place a report can read yet -- a branch that got a cached guest and one
that built a ~1GB one inline differ by about fifty minutes, and that
difference is worth measuring once there is something to measure it from.

**Persisting the host rollups.** An hourly rollup is small enough to keep
for months, and keeping it is what would make host pressure comparable
across a daemon restart -- which on this deployment happens more often
than the comparison window. It was left out on purpose rather than
overlooked: a new table means a schema version, a sweep, and a dump this
repository's state export has to carry, and it is worth doing on the
evidence of the series being used rather than ahead of it. `host.reaches`
is the field that makes the gap visible in the meantime.
