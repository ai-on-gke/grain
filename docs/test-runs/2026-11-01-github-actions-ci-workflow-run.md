# Test run: GitHub Actions CI, `github_workflow_run` schedules and Grain CI standing down

Plan: `docs/test-plans/2026-11-01.md` §6 (PRs #265, #266, #267, #268, #270,
#272, #274, #279, #293, #208). Task 2221, filed by task 2084. Run on
2026-11-01, attempt 3 (attempts 1 and 2 were paused on usage limits before
they pushed anything).

## Environment

- Sandbox: Debian, Go toolchain from the image, `ai-on-gke/grain` at
  `4077424a` (main, "Merge pull request #364") plus this branch.
- `grain demo -github-sim -fake-ci -reconcile 2s` built from this tree
  (`go build -o /var/tmp/grain ./cmd/grain`), served on `127.0.0.1:4391`.
- Go e2e walkthroughs in `tests/e2e/github_workflow_run_walkthrough_test.go`
  over a real SQLite store, the CI service `daemon.BuildCIService` builds,
  the orchestrator's own reconcilers, `github.RealTransport` over HTTP to a
  `githubsim` (`githubHostServer`), and a real `grain mcpserver`.
- Real pull request #372 for this branch, read with `pull_request_status`.

## Results

| # | Item ("What is NOT proved") | Result |
|---|---|---|
| 1 | `github_workflow_run` schedule fires once per concluded failing run, deduplicated across webhook and poll; body carries run URL and failure tail | **Pass** (Go e2e) |
| 1 | Release triage files on a failure and closes on a later success | **Pass** (Go e2e) |
| 2 | Fork `pull_request` run with head branch `main` changes nothing; a base-repo `push` on main does | **Pass** (Go e2e); no fork effect got through |
| 3 | `ci_mode` grain → github mid-run: system cancel, sandbox freed, `grain ci wait` on GitHub checks; back resumes Grain CI | **Pass after fix B3** (Go e2e) |
| 4 | `grain ci log` reads a GitHub run | **Pass with a caveat (B2)**: the log is readable, but the review task the schedule files holds no `grain-ci`, so it has no tool to read it |
| 5 | Path filter on real PRs | **Pass** for docs-only and `ui/`; `workflow_dispatch` not triggered (no API access), evaluated locally |
| 6 | Poll's GitHub budget: a 304 is not counted | **Fail, fixed (B1)** |
| — | Driving the loop in `grain demo -github-sim` | **Not possible** (B4): the sim cannot post a workflow run and the demo runs no poll |

## Steps and observations

### Demo (step 1)

1. Started `grain demo -github-sim -fake-ci -reconcile 2s`. Read the access
   code from `/tmp/grain-demo-*/local-api-token`.
2. `PUT /api/repos/acme/widgets/settings {"ciMode":"github"}` was accepted, and
   `GET` then returned `"ciMode":"github"`.
3. `GET /api/e2e/github-sim/repos/acme/widgets/actions/runs?status=completed`
   returned `{"total_count":0,"workflow_runs":[]}`.
   `POST` to the same path made the sim panic: `githubsim: unhandled request
   POST /repos/acme/widgets/actions/runs`. net/http recovered the panic.
4. `pkg/demo/demo_reconcile.go` runs `github-events` with no GitHub client,
   and it never runs `github-ci-tails`. So even a seeded run is never polled,
   and a workflow-run webhook cannot read the run's jobs. The plan's fallback
   applies: the loop is pinned in a Go e2e test instead (below). The demo gap
   is filed as B4.

### Item 1, 2, 4, 6: `TestGitHubWorkflowRunScheduleLoopOverSim`

- `acme/widgets` uses `ci_mode: github`, and `main` is a watched base branch.
  `.grain/schedules/release-failures.yaml` is committed to the repo's own tree
  and read back with `repodecl.Load`:
  ```yaml
  trigger:
    github_workflow_run:
      workflow: release.yml
      conclusions: [failure]
      branches: [main]
  ```
- The sim holds three runs:
  - 1201: `release` on `main`, `push`, failure. Job `publish-image`, step
    `ko publish`, log line `error: pushing image to registry: 500 Internal
    Server Error`.
  - 1301: the same workflow from fork `mallory/widgets` as a `pull_request`,
    head branch `main`, failure.
  - 1302: a `pull_request` run from the base repo itself, head branch `main`.
- Webhooks are parsed by `ghwebhook.ParsePayload` and delivered in this order:
  1301, then 1201, then 1201 again (a redelivery), then 1302. After that, the
  `github-ci-tails` poll reads all three runs.
- Observed:
  - The schedule filed exactly one task, "Review last night's "release"
    workflow run". Its body has `actions/runs/1201`, `publish-image`,
    `ko publish` and the failure line.
  - The `scheduled_workflow_run` row is `RunID 1201 → that task`.
  - The log said `orchestrator: ignoring concluded GitHub workflow run 1301 on
    acme/widgets from head repository mallory/widgets`.
  - No task body mentions 1301, 1302 or the fork's log marker, and main's
    release row never takes either run's id.
  - The log said `filed 2 to triage failed release on acme/widgets main at
    deadbeef1234 (failure)`.
  - A later green run 1202 logged `closing 2 as superseded: … published main
    … (run 1202)`. Main's release row went to `1202`, `Failures 0`. The
    success did not fire the failure-only schedule.
- Item 4: the review task's own `grain mcpserver` ran `get_ci_log {run_id:
  "1201"}`, which read the synthesized `ci.Run` and its failure tail. That
  only works once the task holds `grain-ci`; see B2.
- Item 6: a third, unchanged poll reached the wire as `304 GET
  /repos/acme/widgets/actions/runs?status=completed&per_page=…`. The
  `github-ci-tails` caller on `github.Callers()` (Health's GitHub budget
  panel) counted it as paid: `{Sent:1 Revalidated:0} -> {Sent:2
  Revalidated:0}`. That is B1. After the fix it counts as revalidated and
  costs nothing.

### Item 3: `TestCIModeSwitchMidRunStandsGrainCIDownAndBack`

- A `grain-ci` task on a `ci_mode: grain` repo runs one job, which holds
  until a hold file is removed. The `ci` reconciler starts it, the job takes
  a sandbox, and `SandboxCLIActions.UsesGitHubChecks` returns false.
- Switch the repo to `ci_mode: github` and reconcile:
  - The run is `cancelled`, by the system, with reason `repository switched
    to GitHub CI`. The task's verdict detail is `cancelled: repository
    switched to GitHub CI`.
  - The job's sandbox is released (its workspace is removed).
  - `UsesGitHubChecks` now returns true, so `grain ci wait` polls GitHub
    checks.
  - A further reconcile starts no new Grain CI run.
- Switch back to `ci_mode: grain`:
  - `UsesGitHubChecks` returns false again.
  - Inside `ciRetryDelay` (5m) the cancelled verdict stands. After it, a new
    run replaces it and concludes `success`.
- First runs of this test hung about one time in five: the cancelled run
  never concluded. That is B3.

### Item 5: path filter on PR #372

- **Docs-only head** `dcdd9d6` (this report's first draft): `changes`,
  `check-changes`, `scan-pr` and `cla/google` ran and passed. Every Go, UI,
  installer, granule, lint and terraform job was skipped. **Pass.**
- **`ui/` head** `bb9408d` (a one-line comment in `ui/src/style.css`):
  - `go`, `go e2e`, `ui e2e`, `installer` and `frontend` ran. These are the
    go-program group, which embeds the UI.
  - `go unit`, `go orchestrator`, `go model`, `postgres`, `run host`,
    `host-script container`, `package cache`, `real vm`, `real kubernetes`,
    `granule`, `lint` and `terraform` were skipped. **Pass.**
  - The probe was then reverted (`7104cfec`).
- **`workflow_dispatch` with `jobs: go-unit`**: a sandbox has no GitHub API,
  so this was not dispatched. Evaluated locally with `EVENT_NAME=
  workflow_dispatch INPUT_JOBS=go-unit scripts/github-ci-changes.sh`. Only
  `go_unit=true` and `go-unit=true` were set; every other output was false.
  **Pass (local evaluation only).**
- `release.yml` and `promote*.yml` were not run (#2201).

## Bugs

- **B1 — fixed.** A 304 answering a caller's own `If-None-Match` was counted
  as a paid request.
  - `CompletedWorkflowRuns` (the `github_workflow_run` poll) keeps its own
    ETag. `RealTransport` leaves a caller's validator alone, and so recorded
    the 304 as `revalidated=false`. Health's GitHub budget showed every
    unchanged poll as spent.
  - Fix in `pkg/github/github.go`.
  - Regression tests: `TestA304ToACallersOwnValidatorIsRevalidatedAndFree`
    (fails without the fix: `Sent:3 Revalidated:0`), and the ledger
    assertion in the e2e walkthrough.
- **B2 — filed.** The review task a `github_workflow_run` schedule files on a
  `ci_mode: github` repo has no `grain-ci`.
  - `model.workflowReviewTemplate` grants `grain-ci` (`GrantByGrain`) for
    `list_ci_runs` and `get_ci_log`. But `model.applyGrainCIGrant` strips
    every `grain-ci` grant from a task on a repo that grain CI does not
    judge.
  - The result: the reviewer's own comment says "Run `grain ci log 1201`",
    and it has no tool to do that with. The failure tail in the body is
    still there.
  - Not fixed here. Keeping `GrantByGrain` grants would also make
    `GrantedExplicitly` gate those tasks' branches on Grain CI in repos with
    no CI mode, which changes merge gating. That is a design call.
  - The e2e test pins today's behaviour, so the follow-up flips that
    assertion.
- **B3 — fixed.** A double `Cancel` could leave a stored CI run in flight
  for ever.
  - `StoredRunStore.Update` is a read-modify-write under `s.mu`, but `Put`
    did not take `s.mu`.
  - Switching to GitHub cancels a task's run twice, once as a live branch
    run and once as the task's verdict. The second cancel's note could read
    the run still in flight. The run's goroutine then concluded it with
    `Put`, and the note wrote the stale copy back over it.
  - Seen as the run timing out in `settle`: `Conclusion: ""`, `FinishedAt:
    nil`, `CancelReason` set and its job `cancelled`. It happened in 4 of
    about 20 shuffled runs.
  - Fix: `Put` holds `s.mu` (`pkg/ci/runstore_stored.go`). After the fix, 40
    consecutive runs passed.
  - Deterministic regression test:
    `TestAPutDuringAnUpdateIsNotOverwrittenByIt` (fails without the fix).
- **B4 — filed.** `grain demo -github-sim` cannot drive the §6 loop.
  - The sim has no way to add a workflow run (`POST …/actions/runs` panics
    "unhandled request").
  - The demo's reconcile runs `github-events` without a client, and never
    runs `github-ci-tails`.
  - Covered for now by the Go e2e test.

No fork-run effect got through: none of the triggers, release triage,
schedules or task bodies saw run 1301 or 1302.

## Already failing on main (not this change)

`release images`, `promote release images` (no `GHCR_TOKEN`, #2201) and
`granule` fail on `main` at `e779856`.
