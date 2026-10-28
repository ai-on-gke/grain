# GitHub CI alongside grain CI

grain measures its own CI (`.grain/workflows`, the `ci_run` tables) and
correlates its failures across branches (shared faults,
`pkg/ci/sharedfault.go`). Most repositories also have CI grain does not
run: GitHub Actions workflows, and check runs third-party providers post
(Codecov, a CLA bot, a licence scanner). Repositories whose `ci_mode` is
not `grain` rely on nothing else. This page says what grain records of
that second source, how it gets it, and what it does not do with it.

## What is recorded

One row per concluded check run, in the `github_ci_job` table
(`model.GitHubCIJob`). An Actions job is a check run on GitHub, with the
same id, so the row is keyed by repository and check-run id. A
`workflow_job` delivery, a `check_run` delivery and a polled listing of the
same job all merge into that one row, and a redelivered webhook rewrites it
rather than counting it twice.

| field | meaning |
| --- | --- |
| `source` | always `github`: what a reader merging this with grain CI labels it with |
| `repo`, `ref`, `commit` | where it ran: pull request head, base branch or merge-queue ref |
| `app` | the App that posted it: `github-actions`, or the provider's slug |
| `workflow`, `job_name` | the job's identity (`JobKey`: `workflow / job`, or the check's own name) |
| `run_id`, `run_attempt` | the Actions run. A higher attempt replaces the earlier verdict |
| `conclusion` | GitHub's own word: `success`, `failure`, `cancelled`, `timed_out`, `skipped`, `neutral`, `action_required`, `startup_failure` |
| `started_at`, `completed_at` | when it ran |
| `failing_step` | the first step GitHub concluded did not pass (Actions jobs, from `workflow_job`) |
| `run_url`, `job_url` | GitHub's pages for the run and the job |
| `output` | the check run's output (title, summary, text), bounded to 16 KiB |
| `failure_tail` | for a **failed** job only: the bounded tail of its log (`tail_source: log`) or, for a check that is not an Actions job, its output (`tail_source: output`) |
| `via` | `webhook` or `poll` |

grain's own checks are not recorded: `grain/review` and grain CI's
mirrored verdicts are grain CI's results, and those are measured already.
Rows are kept for 14 days.

## How it is collected

The cheapest source available is used first:

1. **Webhooks.** Where the GitHub App bot's gateway is configured
   (`pkg/ghwebhook`), concluded `check_run` and `workflow_job` deliveries
   are recorded by the `github-events` reconciler. These cost no API call
   and cover every ref the App sees. A bot made before this change has to
   add the `workflow_job` event and the `actions: read` permission in its
   App settings. `grain init`'s manifest asks for both now.
2. **Polling, without new polls.** On a repository whose pull requests are
   gated on GitHub's checks, the pull request sync already lists each open
   pull request's check runs on its adaptive/ETag schedule. The concluded
   ones in that answer are recorded with no call of their own. A
   repository gated on grain CI (`ci_mode: grain`) does not read GitHub's
   checks for its pull requests, so its GitHub CI arrives through webhooks
   only.
3. **Failure tails.** The `github-ci-tails` reconciler reads
   `GET /repos/{o}/{r}/actions/jobs/{id}/logs` once for each **failed**
   Actions job that is under six hours old (older failures fall outside the
   shared-fault window). It keeps the last 16 KiB of the log. It reads at
   most 30 logs an hour and 5 a cycle, and none while the hour's GitHub
   budget is low (`github.BudgetLow`). A log GitHub will not serve (410
   past retention, 403 without `actions: read`) is asked for once. These
   calls are charged to `github-ci-tails` in the metrics' per-caller budget
   breakdown.

## What GitHub CI is not used for

- **No verdict reuse.** Nothing here writes `ci_run`. A GitHub result
  never stands in for a grain-CI verdict, a cached job, the merge queue's
  grain gate or a task's CI verdict. Pull request health still reads
  GitHub's checks the way it always has (`pkg/forge`).
- **Grain-CI numbers do not move.** The metrics' grain-CI sections read
  `ci_run` only.
- **No cross-source faults.** A grain-CI failure and a GitHub-CI failure
  are never the same shared fault, even with identical text. The two run
  different job definitions on different machines, so "the same job" means
  nothing across them. Correlation stays within a source.

## Metrics

`read_grain_metrics`, `grain metrics`, `GET /api/metrics` and the Metrics
pane carry a `github_ci` section (`githubCI` in the API), placed after
grain's own CI queueing and labelled `source: github` on every row, so the
two sources sit side by side without either being read as the other. It
is computed from `github_ci_job` alone; the grain-CI sections read
`ci_run` exactly as before, and a test holds them byte-identical with and
without GitHub rows. Per workflow/job it reports pass, fail and cancel
rates (`failure`, `timed_out` and `startup_failure` are failures; skipped
and neutral are counted apart, in no rate), time to green per commit,
jobs that failed and then passed on the same commit (flaky), and how long
each base branch grain watches was red by its GitHub results.
[tuning-stats.md](tuning-stats.md) defines each number.

## Shared-fault correlation

`ci.GitHubSharedFaults` (`pkg/ci/sharedfaultreport.go`) converts recent
failed `GitHubCIJob` rows that carry a `FailureTail` into the inputs
`ci.SharedFaults` (`pkg/ci/sharedfault.go`) reads: `JobID = JobKey()`
(`workflow / job`, or the check's name), `Step = FailingStep` (or the job
key when no step is recorded), and `FailureTail` as the failing step's
output. For each `(repo, ref, JobKey)` only the newest recorded row is
fed in, so a branch whose later re-run passed drops out of the fault.

Every rule of grain's shared-fault correlation applies unchanged: same
repository, job and step, byte-identical normalized digest, at least
`minFaultChars` (40) of normalized output, within `DefaultFaultWindow`
(6h), across at least 2 distinct refs, and silence when the evidence is
thin. `normalizeFailure` strips GitHub Actions log noise before
digesting: the RFC3339 timestamp prefix on every line, `##[group]`,
`##[endgroup]`, `##[error]`, `##[warning]`, `##[command]`, `##[debug]`
and `##[notice]` markers, and GitHub-hosted runner paths
(`/home/runner/work/<repo>/<repo>/`, `D:\a\<repo>\<repo>\`,
`/opt/hostedtoolcache/...`, `D:\a\_tool\...`,
`C:\hostedtoolcache\windows\...`).

Correlation runs **within a source only**: every `ci.SharedFault` carries
`source: "grain"` or `source: "github"`, and a grain-CI failure and a
GitHub-CI failure never join into one fault even with identical output.
Both sources are served in `GET /api/ci/faults`, `GET /api/ci/runs`
(`faults`), and `grain ci faults [-repo r] [-source grain|github|all]`,
shown in the UI's CI pane and task detail overlay labelled by source
(`grain CI` vs `GitHub CI`), and annotated on affected tasks and pull
requests by `github-ci-tails` using the same wording and open-fix
attribution (`pkg/cifault`) as grain-CI faults.

## Staging

This was delivered in three stages:

1. Ingestion (`github_ci_job`, `check_run` and `workflow_job` webhooks,
   poll recording, and bounded failure-tail reads).
2. Success/failure metrics sliced by source (`github_ci` in
   `read_grain_metrics`, `grain metrics`, `GET /api/metrics`, and the
   Metrics pane).
3. Shared-fault correlation over GitHub failures with GitHub Actions log
   normalization (`ci.GitHubSharedFaults`, `source` on `SharedFault`,
   `GET /api/ci/faults`, `grain ci faults`, UI source badges, and task/PR
   annotations).
