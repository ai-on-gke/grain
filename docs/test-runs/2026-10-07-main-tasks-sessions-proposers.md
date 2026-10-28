# 2026-10-07: `main` tasks, sessions and teams (test plan 2026-10-06 §9)

Task 2387 (filed by task 2231, the nightly test plan). Plan: `docs/test-plans/2026-10-06.md` §9,
read from `origin/candidate` because the plan has not reached `main` yet.

## Environment and commit

- Base: `main` at `7819e615d` (Merge pull request #3160 from bwsalmon/grain/task-2468).
- `git branch -r --contains` for every commit in scope (`92bfc4e15`, `0f4e204d7`, `4cdf3750c`,
  `103c1e3a1`, `8fe0b904d`, `0a85a5cd5`) lists `origin/main` and `origin/grain-merge-queue/main`
  but not `origin/candidate`. No release cut has brought them to `candidate` yet (`candidate`'s
  tip was `dafd69778`), so this ran on `main` as the plan asks.
- Debian sandbox, Go 1.26.6, Node from the image, Playwright Chromium from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, and PostgreSQL 15 installed with apt for the
  run.
- UI built with `cd ui && npm ci && npm run build` into `pkg/ui/static`, and `grain` built from
  that tree.

## Commands

```
go build -o /tmp/grain-main ./cmd/grain
/tmp/grain-main demo -addr 127.0.0.1:0 -open=false -github-sim    # manual API pass with curl
/tmp/grain-main -server $URL list -filter user:has:ad             # and user:has:AD, user:any:ADA
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  npx playwright test e2e/main-tasks-sessions-proposers-walkthrough.spec.js --project=chromium
go test ./pkg/ui -run TestTaskCreating -count=1
go test ./pkg/orchestrator -run 'TestTwoSessionsOnTwoTeams|TestDirectorTurnDispatches' -count=1
GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model/postgres -run TestBranchFromColumnMigration -count=1
# migration on a candidate database: see step 1c
```

## Stubs

- **GitHub** is `grain demo -github-sim` (pkg/github/githubsim over a real bare repo holding `main`
  and `release/1.0`) in the browser walkthrough. The Go tests use the same sim (`newSim`).
- **Live model sessions** are `blockingFramework` in
  `pkg/orchestrator/concurrent_dispatch_test.go`, which is an agent that holds its run open until
  released. Everything else is real: the store, `RunCycle`, dispatch, session filing, and replies
  through `ui.Client`.
- Nothing of grain's own was mocked. The demo runs no orchestrator, so the branch being cut and
  the held task being dispatched could not be followed there. They were followed in Go against
  the sim instead (`TestDispatchHoldsATaskUntilItsRequestedBranchIsCut` and
  `TestSyncBranchesCreatesAPendingBranchFromTheBranchItNames`, both already on `main` and passing).

## Outcomes

### 1. New branch (`0f4e204d7`)

a. **Form to request.** In the browser, with "Create a new branch" ticked, "New branch name"
   appears and "Base branch" is relabelled "Create from". `feature/x` from `release/1.0` files the
   task (201, `base: feature/x`), and `GET /api/repos/acme/widgets/branches` shows
   `{name: feature/x, from: release/1.0, status: pending}`. The demo has no reconciler, so the
   branch stays pending there and no "waiting" note is written: the note comes from the
   dispatcher's hold.

   The cut, the hold and the dispatch were checked in Go against githubsim:
   `TestSyncBranchesCreatesAPendingBranchFromTheBranchItNames` asserts that the new ref on the bare
   repo is `release/1.2`'s own tip. `TestDispatchHoldsATaskUntilItsRequestedBranchIsCut` asserts
   the `waiting: branch "hotfix" is not created` wait, including the reconciler's error. It then
   asserts no dispatch in the cycle that cuts the branch, and dispatch on the next cycle. Both
   pass.

b. **Edge cases** (browser and API, with the answers as recorded):
   - `a..b` → 400 `base "a..b" is not a usable branch name: use letters, digits, '.', '_', '-'
     and '/' only, …`. A "Create from" of `a..b` gets the same sentence for `newBranchFrom`.
   - A missing "Create from" (`no-such-branch`) → the form warns `No branch "no-such-branch" on
     acme/widgets: …`, and the server refuses with 400 `base "no-such-branch" does not exist on
     acme/widgets: push it first, or have the task depend on the one that creates it`.
   - An existing name (`release/1.0`, "Create from" `main`) → 201. The branch is adopted as it
     stands, and its "Create from" is not used. This is by design: the form's hint says "one that
     already exists is used as it stands".
   - **Bug F1 (fixed):** a name that is already *pending*, requested again from another base →
     201 before the fix. See Findings.

c. **Migration.** I built `candidate` (`dafd69778`), ran its demo, recorded a branch request, and
   copied its `grain.db*`. Opening the copy with `main`'s `model.Store.Init` (a throwaway
   `cmd/` program, not committed) gave:
   - `from_branch` column before: 0. After: 1.
   - The old row reads back as `From: ""`, which means the default branch.
   - A new `RequestBranchFrom(feature/x, release/1.0)` round-trips.
   - A second `Init` is a no-op.

   On PostgreSQL 15, the new `TestBranchFromColumnMigrationAndRoundTrip` drops the column, inserts
   a row the old way, runs `Init` twice and checks both rows. It passes.

### 2. Sessions (`4cdf3750c`, `103c1e3a1`)

- **Two live sessions on two teams.** The new `TestTwoSessionsOnTwoTeamsAreLiveAtOnceBesideAFullWorkerSlot`
  sets up a Director on team `infra` and a Reviewer on team `security`, with `MaxWorkers: 1`
  taken by an ordinary run. A reply to each starts both runs in the same cycle. Both tasks are
  `running`, `LiveRunCounts` is `{Workers: 1, Directors: 2}`, and a second ordinary task waits
  ("deployment at capacity"). It passes.
- **Demo.** Creating team `open` filed a Director session for it (`director-2`). A second session
  created as bob (`POST /api/console-sessions`, "Open chat") was listed on team `open` beside the
  default team's Director. The demo has no orchestrator, so neither can go live there.
- **Sessions page.** The Director row shows "Starting", because its demo task is `queued`. The
  rail's "Awaiting reply" count equals the API's count of `awaiting_reply` tasks that are not
  sessions. Both are asserted in the browser.
- **`GET /api/tasks?state=running`** (the plan's own query) ignores `state` and returns every
  task. The API has no such parameter. The equivalent is `grain list -state running`. I'm noting
  this as a plan inaccuracy, not a bug.
- **Finding F2 (follow-up proposed):** `kind:any:interactive` shows no session in the demo. The
  demo's seeded Director task is filed with `interactive: false`, and a real session task never
  is (`orchestrator.fileConsoleSession` sets `Interactive: true`). Through the real orchestrator,
  `director.spec.js` already shows a session under `kind:any:interactive` in the browser and in
  `grain list`.

### 3. User filter (`92bfc4e15`)

- I signed in as ada and opened Filter by… → User. The menu offers `zed`, a team member who has
  filed nothing.
- `/?f=user:has:AD` (contains, upper case) shows ada's and Adam's tasks but not carol's.
- The rows shown are the same ids as `grain list -json -filter user:has:ad`.
- From the CLI by hand: `user:has:deb` and `user:has:DEB` give the same rows (case folding).

### 4. Anyone may propose (`8fe0b904d`)

- Before the switch, nina (a deployment user on no list) gets 403 from `GET /api/teams/open`.
- carol (a member, not an admin) gets 403 from `PUT {anyoneMayPropose: true}`: `user "carol"
  does not have permission to manipulate team "open"`.
- bob (the team admin) flips the "Anyone may propose" switch on Team settings → Members (PUT
  200).
- nina then reads the team and files into it. Her task is `proposed` even with
  `approved: true`, and she can edit its title (200).
- nina gets 403 on all of these, each with the proposer sentence ("…approving, running or
  otherwise changing a task needs the member or admin role on it"):
  - approving her own task;
  - editing, commenting on or closing carol's task;
  - flipping the setting.
- bob switches it off. nina's next reads of the team (403), of her own task (404) and of
  `/api/tasks` (empty) take effect at once.

### 5. Capability refusal (`0a85a5cd5`)

- bob adds CI capability `gcp-key` on `open`/`acme/widgets` → 400 `ciCapabilities: capability
  "gcp-key" is restricted to authorized teams [default]; team "open" is not authorized. A
  deployment operator can authorize team "open" for it under Settings > Capabilities ("gcp-key",
  "Also authorize teams"); or team "open" can configure a GCP key of its own under its Team
  Settings and grant gcp-key:<name> instead`.
- With `open` authorized (`PUT /api/settings capabilityTeams`), the save stores `gcp-key`. The
  operator then narrows the allowlist again, and an unrelated save
  (`defaultCapabilities: [gemini-key]`) still returns 200 and keeps `gcp-key`.
- Adding another restricted id (`self-debug`) in the same save is refused with its own sentence.

## Findings

- **F1 (bug, fixed here).** "Create a new branch" onto a name that is already pending, from a
  different "Create from", was accepted silently. Branch requests are per name, so the branch was
  cut from the *first* request's base, and the second task ran on code its form never asked for.

  Reproduction, on any `main` demo with `-github-sim`:
  1. `POST /api/tasks {"repo":"acme/widgets","base":"feature/x","newBranch":true,"newBranchFrom":"release/1.0",…}` → 201.
  2. The same with `"newBranchFrom":"main"` → 201.
  3. `GET /api/repos/acme/widgets/branches` shows one request, from `release/1.0`.

  Fix: `ui.Client.CreateTask` refuses with 400 `branch "feature/x" is already waiting to be
  created on acme/widgets from "release/1.0": create it from that too, or name another branch`.
  A request from the same base still files, and a branch already created or adopted is still used
  as it stands. Regression tests: `TestTaskCreatingABranchAlreadyPendingFromAnotherBaseIsRefused`
  (fails without the fix) and the browser walkthrough.
- **F2 (demo realism, follow-up proposed).** The demo's seeded Director task is not
  `interactive`, so `kind:any:interactive` cannot show a session in the demo. Real session tasks
  are interactive.
- **Note.** A name that already exists on GitHub ignores "Create from" and is adopted. This is
  intended and the form says so, so no change was made.
- **Note.** The plan's `GET /api/tasks?state=running` is not an API. `state` is ignored without a
  word.

No overlap: `docs/test-runs/2026-10-06-daily-exploratory-ui.md` and the 10-05 reports do not
cover any of these.

## Fixes and tests added

- `pkg/ui/client.go` and `pkg/ui/branches.go`: the F1 refusal.
- `pkg/ui/new_branch_task_test.go`: `TestTaskCreatingABranchAlreadyPendingFromAnotherBaseIsRefused`.
- `pkg/orchestrator/concurrent_dispatch_test.go`: `TestTwoSessionsOnTwoTeamsAreLiveAtOnceBesideAFullWorkerSlot`.
- `pkg/model/postgres/branch_from_migration_test.go`: `TestBranchFromColumnMigrationAndRoundTrip`
  (skips without PostgreSQL, like the rest of that package).
- `ui/e2e/main-tasks-sessions-proposers-walkthrough.spec.js`: the browser walkthrough of steps
  1, 3, 4 and 5, and the browser half of step 2. Six tests, all passing locally on Chromium.

## CI verdict

Recorded in the pull request: grain CI's full suite `wait_for_checks` on the final commit of
`grain/task-2387`.
