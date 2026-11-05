# Walkthrough — Orchestration, schedules and repositories (§9 of `docs/test-plans/2026-11-04.md`)

- **Date:** 2026-11-04, 21:28–22:57 UTC
- **Task:** `grain/049f1e09/task-3075-walkthrough-git-ref-schedules` ([Task #3075](/tasks/3075)), base `main`
- **Commit tested:** `40e2820f` (`origin/main` at the start of the run, `v0.0.0-20261104182831-40e2820f5325`), then rebuilt and re-verified with the five scoped fixes below.
- **Environment:**
  - Debian sandbox (`linux/amd64`), Go `go1.26.10`, Node.js `v20.19.0`, `git` (`git-http-backend` for local Smart HTTP bare repositories).
  - Embedded UI built via `cd ui && npm run build`, embedded into `/home/debian/bin/grain` (`go build -o /home/debian/bin/grain ./cmd/grain`).
  - Headless Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` (desktop) and `390×844` (phone) in `light` and `dark` themes (`ui/e2e/screenshot.mjs`).
  - Driver & artifacts: `docs/test-runs/2026-11-04-orchestration/walkthrough.go` (`//go:build ignore`), JSON cycle transcripts (`item1-git-ref.json` through `item5-playwright-audit.json`), and screenshots (`system_metrics-{desktop,phone}-{light,dark}.png`, `processes-{desktop,phone}-{light,dark}.png`, `contact-sheet-before.png`, `contact-sheet.png`).

## Boundaries & Stubs

Every store (`pkg/model/sqlite`), git repository (`git init --bare` + `git-http-backend`), git credential proxy (`pkg/gitproxy`), daemon subprocess (`grain daemon`), reconciler (`orchestrator.RunCycle`), state export/import (`pkg/staterepo`), UI server (`ui.NewServerWithClient`), and browser (`@playwright/test`) was real. Only the two unavailable external boundaries were stubbed:

1. **GitHub REST API (no live GitHub credential in sandbox):** replaced with `pkg/github/githubsim.Sim` backed by real bare git repositories (`realGitMergeableTransport` computing `pr.Mergeable` via `git merge-tree --write-tree` against real git commits, plus fault-injection hooks in Item 3b for HTTP 405 stale-cache refusal and HTTP 502 server failure).
2. **External LLM vendor API in Items 2 & 4 (`orchestrator.StaticFramework`):** deterministic `agent.Provider` callbacks performing real `git clone`/`git fetch`/`git push` through `gitproxy.NewHandler` and invoking real `mcp.PullRequestStatus` / `ui.Client.ResumeLater` / `ui.Client.WaitForTask` handlers rather than calling an external model endpoint.

---

## Walkthrough Execution & Reconcile Cycles

### 1. `git_ref` trigger on another repo's `refs/tags/latest` (`item1-git-ref.json`)

**Setup & command:**
```bash
go run ./docs/test-runs/2026-11-04-orchestration/walkthrough.go -mode=item1
```
- Local bare git repositories `acme/edition.git`, `upstream/core.git` (seeded with annotated tag `refs/tags/latest` → `0ea5e6ce1b825bfbb707604ae15e71325d11214c`), `acme/secrets-backup.git`, and `offlist/external.git` served over `net/http/cgi` `git-http-backend` requiring HTTP Basic token `local-walkthrough-token`.
- Configured `secrets-repo.json` pointing at `acme/secrets-backup.git` before starting a real `/home/debian/bin/grain daemon` subprocess (`-poll-interval 1s -disable-native-sandboxes -ci=false -github-host <local> -github-insecure-http`).
- Configured default GitHub token (`PUT /api/github-tokens/default`) and target-repo allowlist `["acme/edition", "upstream/core", "acme/secrets-backup"]` (`POST /api/repos`), then created schedule `sched-3` (`POST /api/schedules`, `overlap: "skip"`, `recurrence: {"kind": "git_ref", "gitRef": {"repo": "upstream/core", "ref": "refs/tags/latest"}}`).

**Cycle-by-cycle decisions & outputs:**
1. **First look (poll 1, `22:35:52Z`):**
   - Daemon log: `orchestrator: schedule sched-3 starts watching upstream/core refs/tags/latest at 0ea5e6ce1b82`
   - SQLite `schedule_ref_cursor`: `0ea5e6ce1b825bfbb707604ae15e71325d11214c`.
   - Tasks filed: **`0`** (`1_first_look` passed — first observation records cursor without firing).
2. **Tag move 1 (`0ea5e6ce...` → `b666ae5d...`, real 2-minute `gitRefPollInterval` at `22:37:53Z`):**
   - Pushed annotated tag `refs/tags/latest` (`1.1.0`) to `upstream/core.git` at `b666ae5d4596621519ea561936d5fc20a7226279`.
   - Daemon log: `orchestrator: schedule sched-3 fired: refs/tags/latest of upstream/core moved from 0ea5e6ce1b825bfbb707604ae15e71325d11214c to b666ae5d4596621519ea561936d5fc20a7226279`
   - Tasks filed: **`1`** (task `#2`, tagged `schedule:sched-3`); cursor updated to `b666ae5d4596621519ea561936d5fc20a7226279`. Subsequent 1s reconcile ticks did not re-fire.
3. **Tag move 2 while first task is still open (`b666ae5d...` → `e104f760...` at `22:39:54Z`), then close task `#2` (`22:40:03Z` → poll at `22:41:54Z`):**
   - While task `#2` remained open (`overlap: "skip"`), poll logged:
     `orchestrator: schedule sched-3: refs/tags/latest of upstream/core moved from b666ae5d4596621519ea561936d5fc20a7226279 to e104f760e68f3dcf1aea0839613379a8b8de10f3, but its previous firing is still open; it fires once that closes`
   - Verified `whileOpenTasks = 1` and cursor **held at `b666ae5d...`** so the move was not lost.
   - Closed task `#2` via `POST /api/tasks/2/close`. On the next poll (`22:41:54Z`), daemon logged:
     `orchestrator: schedule sched-3 fired: refs/tags/latest of upstream/core moved from b666ae5d4596621519ea561936d5fc20a7226279 to e104f760e68f3dcf1aea0839613379a8b8de10f3`
   - Verified `afterCloseTasks = 2` and cursor advanced to `e104f760e68f3dcf1aea0839613379a8b8de10f3`.
4. **Daemon restarts (`22:41:55Z`–`22:42:01Z`):**
   - **Restart 4a (tag unchanged at `e104f760...`):** stopped `grain daemon` (`SIGTERM`) and started a fresh `grain daemon` on the same `-data-dir`. `git ls-remote` ran immediately on startup (in-memory `gitRefPolled` cleared), saw `cursor.Commit == commit`, and filed **`0` additional tasks** (`afterUnchangedRestart = 2` — neither re-fires).
   - **Restart 4b (tag moved to `d4030cb9...` while daemon stopped):** stopped `grain daemon`, moved `refs/tags/latest` (`1.3.0`) to `d4030cb9704ba0c41acb9fd256f4fa65f22dd05f`, and started `grain daemon`. Startup poll immediately fired task `#3`:
     `orchestrator: schedule sched-3 fired: refs/tags/latest of upstream/core moved from e104f760e68f3dcf1aea0839613379a8b8de10f3 to d4030cb9704ba0c41acb9fd256f4fa65f22dd05f`
     (`afterOfflineMoveRestart = 3`, `finalCursor = d4030cb9...` — nor misses).
5. **Forbidden / invalid ref / off-allowlist refusals:**
   - **Repo-declared `.grain/schedules/sync.yml` watching `grain/config` (`repodecl.Load`):** refused with `.grain/schedules/sync.yml: ` + "`git_ref`" + ` watches grain/config, which is grain's own configuration; no repository's file may reach it -- a schedule that watches it is declared in grain's own settings`.
   - **Short ref via `POST /api/schedules` (`ref: "latest"`):** refused HTTP `400`: `recurrence: git_ref trigger needs a full ref: such as refs/tags/latest or refs/heads/main, got "latest"`.
   - **Secrets-backup forbidden repo (`acme/secrets-backup`):** daemon refused to read with clear reason: `orchestrator: schedule sched-1: reading acme/secrets-backup refs/heads/main: acme/secrets-backup: grain's own state repository holds (or once held) its encrypted secrets file, so no sandbox may fetch or push it: everything a sandbox can clone is everything it can read`.
   - **Off-allowlist repo (`offlist/external`):** daemon refused to poll: `orchestrator: schedule sched-2 watches offlist/external refs/heads/main, which is not on this deployment's target repo list; it is not looked at until it is`.
   - *(Follow-up proposed via `propose_task`: `POST /api/schedules` itself accepted `sched-1` and `sched-2` with `201 Created` rather than rejecting forbidden `gitRef.repo` on write or surfacing suppression on [Processes](/processes).)*

---

### 2. Fork-to-upstream (`upstream_repo`) over `githubsim` + `gitproxy` (`item2-fork-upstream.json`)

**Setup & command:**
```bash
go run ./docs/test-runs/2026-11-04-orchestration/walkthrough.go -mode=item2
```
- Two real bare repositories (`gke-labs/kube-agents.git` upstream and `sackheads/kube-agents.git` fork) behind a real `gitproxy.NewHandler` + `githubsim.Sim` per repo.
- Configured `PUT /api/repos/sackheads/kube-agents/settings` with `upstreamRepo: "gke-labs/kube-agents"` and `ciMode: "github"`.

**Cycle-by-cycle decisions & outputs:**
1. **Cycle 1 (dispatch, proxy enforcement, cross-repo PR open):**
   - Sandbox cloned `sackheads/kube-agents` and fetched `gke-labs/kube-agents` through `gitproxy` (allowed: `upstream_repo` is automatically added to the sandbox's read scope).
   - Attempted direct `git push` to `gke-labs/kube-agents.git` through `gitproxy`: **refused HTTP 403** with `remote: gke-labs/kube-agents is read-only for this sandbox: it may fetch it but not push to it`.
   - Pushed `grain/task-1` (`a7c1ca3`) to `sackheads/kube-agents.git`. Reconciler opened upstream PR **`gke-labs/kube-agents#9000`** with `head = "sackheads:grain/task-1"` (`HeadRepo = "sackheads/kube-agents"`, `Head = "grain/task-1"`), `base = "main"`, and `sackheads/kube-agents` held **0** pull requests.
2. **Agent `pull_request_status` against upstream CI (`mcp.PullRequestStatus`):**
   - Seeded failing check `Tests / unit` (`failure`) and upstream Actions workflow run `#5010` on `gke-labs/kube-agents` at `a7c1ca3`.
   - `mcp.PullRequestStatus` queried `gke-labs/kube-agents` (not the fork), reporting `Pull request #9000 (https://github.example/gke-labs/kube-agents/pull/9000) is open and merges cleanly into main`, `FAILING Tests / unit (failure)`, and the upstream Actions job log tail (`TestController (controller_test.go:19): expected ready`). *(Uncovered & fixed Bugs A and B in `githubsim` — see below.)*
3. **Cycle 2 (green upstream CI → merge queue lands on upstream):**
   - Updated `Tests / unit` on `gke-labs/kube-agents` to `completed/success` and ran `orchestrator.RunCycle`.
   - Reconciler merged `gke-labs/kube-agents#9000` into `gke-labs/kube-agents` `main` (`6f39a355...` → `2b104ef5...`) and closed task `#1` with `prMergedAt` recorded.
4. **Bonus (`RequireBranchUpToDate: true` behind-upstream fork PR refresh):**
   - Advanced `gke-labs/kube-agents` `main` while task `#2`'s fork branch was based on an older commit. `SyncPullRequests` merged upstream `main` into `sackheads/kube-agents:grain/task-2` and merged `gke-labs/kube-agents#9001` cleanly (`task2State = closed`).

---

### 3. Five merges per lane on real git content (`item3-five-per-lane.json`)

**Setup & command:**
```bash
go run ./docs/test-runs/2026-11-04-orchestration/walkthrough.go -mode=item3
```
- Real bare git repositories with `realGitMergeableTransport` running `git merge-tree --write-tree` against current `refs/heads/main` after each merge.

**Cycle-by-cycle decisions & outputs:**
1. **Scenario 3a — Six clean PRs on one repo (`defaultMaxMergesPerRepo = 5`):**
   - **Cycle 1:** merged **5 of 6** clean PRs (`#301`–`#305` → `closed`; `#306` stayed `completed`, capped by `DefaultMaxMergesPerRepo = 5`).
   - **Cycle 2:** merged remaining PR `#306` (`6/6` merged).
2. **Scenario 3b — Conflict and server failure after the 3rd merge on real git content:**
   - **Re-read `Mergeable == false` after 3rd merge (`reReadMergeableFalseAfterThird`):** PR `#403` modified `shared.txt`, making PR `#404` conflict on `shared.txt` while PR `#405` touched `clean_5.txt`. In Cycle 1, `#401`, `#402`, `#403` merged; GitHub's re-read marked `#404` `Mergeable = false`, sending `#404` to conflict repair (`state = proposed`) while clean `#405` merged (`mergeWalk` does not let one conflicted PR FIFO-block clean siblings).
   - **Stale-cache HTTP 405 conflict on `PUT .../merge` (`putMerge405ConflictAfterThird`):** GitHub's cached PR read still reported `Mergeable = true` for `#404`, and real `git merge --no-ff` inside `githubsim` failed with `405` (`CONFLICT (content): Merge conflict in shared.txt`). Because a `405` leaves `main` untouched, `#404` stayed `completed` for next cycle's re-read while `#405` merged.
   - **HTTP 502 server failure after 3rd merge (`server502AfterThirdClosesLane`):** `PUT .../merge` on `#454` returned `502 Bad Gateway`. Because an ambiguous 5xx may have moved `main`, `mergeWalk.closeEntry` **closed the `acme/error-lane:main` lane** for the rest of Cycle 1: `#451`–`#453` merged, `#454` did not merge, and clean `#455` was held unmerged until the next cycle.
3. **Scenario 3c — Store holding `maxMergesPerRepo = 1` keeps `1`:**
   - Set `cfg.MaxMergesPerRepo = 1`, exported via `staterepo.Export`, imported into a fresh SQLite DB via `staterepo.Import`, verified `MaxMergesPerRepo == 1`, and ran Cycle 1 with 3 clean PRs → **only 1 of 3 merged** in Cycle 1.

---

### 4. `resume_later {ci: my-branch}`, `until: "merged"` wait, and auto-merge hand-back (`item4-waits-and-handback.json`)

**Setup & command:**
```bash
go run ./docs/test-runs/2026-11-04-orchestration/walkthrough.go -mode=item4
```

**Cycle-by-cycle decisions & outputs:**
1. **Scenario 4a — `resume_later {when: {ci: "my-branch"}}` wakes on an Actions verdict in `githubsim`:**
   - Task pushed `grain/task-1` on a `CIMode: "github"` repo with an `in_progress` Actions workflow run and called `ResumeLater(when: {ci: "my-branch"}, reason: "waiting for GitHub Actions suite to finish")`.
   - **Cycle 1:** task transitioned to `deferred` (`attempts = 1`).
   - **Cycle 2 (while Actions `in_progress`):** stayed `deferred` (`attempts = 1`, not woken prematurely).
   - **Cycle 3 (Actions workflow run completed with verdict):** reconciler logged `orchestrator: task 1 has the CI verdict it waited for and is picked back up`, dispatched Attempt 2 (`attempts = 2`) with the suspension reason in Attempt 2's prompt (`attempt2PromptHasReason = true`), and finished cleanly (`awaiting_submit`).
2. **Scenario 4b — `wait_for_task {until: "merged", park: true}` on a closed task with an open PR:**
   - Task `#601` pushed `grain/task-601`, opened PR `#9000`, and was **closed unmerged** while PR `#9000` remained open on `githubsim`.
   - Dependent task called `WaitForTask(task_id: "601", until: "merged", park: true)`. Tool reply noted: `[reached] Task 601 "feat: prerequisite change": closed, outcome cancelled (closed, but its pull request is still open: grain records a merge if a person merges it by hand).`
   - **Reconcile while PR `#9000` still open:** `dispatch: candidate 1 skipped: parked until task 601 merges: it closed with its pull request still open` (`attempts = 1`, held).
   - **After PR `#9000` merged on `githubsim`:** `reconcileClosedUnmergedOpenPRs` observed the merge on `#601`, unblocking the `until: "merged"` wait and dispatching Attempt 2 (`attempts = 2`), with `#601`'s merge outcome in the prompt.
3. **Scenario 4c — Auto-merge hand-back when a security hold clears (including across `staterepo` export/import):**
   - Task filed with `AutoMerge: true` modified trust-boundary file `pkg/gitproxy/proxy.go`.
   - **Cycle 1:** classifier logged `orchestrator: acme/sec-app#9000 touches the trust boundary (credential-proxy); auto-merge withdrawn pending security review`, set `AutoMerge = false`, added `security-hold` + `security-review-task` links, and persisted `auto_merge_before_hold` in SQLite.
   - Exported DB via `staterepo.Export` and imported into a brand-new SQLite DB via `staterepo.Import` (`AutoMerge` remained `false`, `security-hold` intact).
   - **Cycles on imported store:** automated security review task `#3` completed with `"no findings"`, lifting `security-hold` with a valid `trust-boundary-cleared` link → **`AutoMerge` handed back (`true`)**, PR `#9000` merged automatically, and task closed!
   - **Negative guard (`#699`):** dropping `security-hold` *without* a matching `trust-boundary-cleared` link (e.g. retargeting repo) kept `AutoMerge = false` (`withheldWhenClearanceAlsoDropped = true`).

---

### 5. Direction rollups & [System → Metrics](/system/metrics) (`item5-direction-rollups.json`, `item5-playwright-audit.json`)

**Seeded multi-repo, multi-hop task tree (reference clock `2026-11-04T15:00:00Z`, ISO week `2026-W45`):**
- **Direction `#100`** (`Migrate storage layer to connection pooling`, `alice@example.com`, `acme/widgets`, **closed** `2026-10-28` in `2026-W44` / `2026-10`, PR `+180 −40`, `0.5h`):
  - Child `#101` (`Add connection pool idle reaper metrics`, `proposed-by: 100`, merged Sunday `2026-11-01` — still `2026-W44` but in `2026-11`, `+95 −12`, `0.75h`).
  - Child `#102` (`Document pool tuning flags in operator guide`, `acme/docs`, `proposed-by: 100`, merged `2026-11-03` yesterday in `2026-W45` / `2026-11`, `+64 −8`, 2 runs `0.5h`).
  - Grandchild `#103` (`Fix pool timeout default in example config`, `acme/widgets`, `proposed-by: 102`, merged `2026-11-04` today in `2026-W45` / `2026-11`, `+22 −5`, `0.5h`).
- **Direction `#200`** (`Sync upstream v1.3.0 release tag`, schedule-triggered today `2026-11-04`, PR `+48 −19`, `0.3h`) → Child `#201` (`acme/docs`, completed today with no PR, `0.3h`).
- **Direction `#300`** (`Audit weekly flake budget across suites`, repo-less investigation completed today with no PR, `0.2h`).

**Verified `GET /api/release-notes/stats` across every window:**

| Query `period` | Resolved `period` | PRs (`+add` / `-del`) | Tasks completed / filed | Runs (succeeded) | Directions (`taskId`: PRs, `+add/-del`, completed, spawned) |
| --- | --- | --- | --- | --- | --- |
| `""` (empty) | `2026-W44` | `2` (`+275` / `-52`) | `2` / `2` | `2` (`2`) | `#100`: 2 PRs (`+275/-52`), 2 completed, 1 spawned |
| `last-week` / `2026-W44` | `2026-W44` | `2` (`+275` / `-52`) | `2` / `2` | `2` (`2`) | `#100`: 2 PRs (`+275/-52`), 2 completed, 1 spawned |
| `2026-W43` | `2026-W43` | `0` (`+0` / `-0`) | `0` / `0` | `0` (`0`) | *(none)* |
| `today` / `2026-11-04` | `2026-11-04` | `2` (`+70` / `-24`) | `4` / `4` | `4` (`4`) | `#200` (1 PR `+48/-19`, 2 done, 1 spawned), `#100` (1 PR `+22/-5`, 1 done via grandchild `#103`), `#300` (0 PRs, 1 done) |
| `yesterday` / `2026-11-03` | `2026-11-03` | `1` (`+64` / `-8`) | `1` / `1` | `2` (`1`) | `#100`: 1 PR (`+64/-8`), 1 completed (via child `#102`) |
| `this-week` / `2026-W45` | `2026-W45` | `3` (`+134` / `-32`) | `5` / `5` | `6` (`5`) | `#100` (2 PRs `+86/-13`, 2 done), `#200` (1 PR `+48/-19`, 2 done, 1 spawned), `#300` (0 PRs, 1 done) |
| `this-month` / `2026-11` | `2026-11` | `4` (`+229` / `-44`) | `6` / `6` | `7` (`6`) | `#100` (3 PRs `+181/-25`, 3 done, 1 spawned — includes Nov 1 `#101`!), `#200` (1 PR `+48/-19`, 2 done, 1 spawned), `#300` (0 PRs, 1 done) |
| `last-month` / `2026-10` | `2026-10` | `1` (`+180` / `-40`) | `1` / `1` | `1` (`1`) | `#100`: 1 PR (`+180/-40`), 1 completed |
| `bogus-window` | HTTP `400` | — | — | — | `"bogus-window" is not a rollup period grain counts...` |

In every window, `sum(directions[*].pullRequestsMerged) == pullRequestsMerged`, `sum(directions[*].linesAdded) == linesAdded`, `sum(directions[*].linesDeleted) == linesDeleted`, `sum(directions[*].tasksCompleted) == tasksCompleted`, and `sum(directions[*].runs) == runs` (even with repo-less `#300`, where `sum(repos[*].tasksCompleted)` is `1` smaller because `#300` has no repository). Multi-hop grandchild `#103` rolled all the way up to root `#100` even in windows (`today`, `this-week`, `this-month`) where `#100` itself had zero direct activity and was already closed.

**Playwright UI & screenshot audit (`item5-playwright-audit.json`, `contact-sheet.png`):**
- Captured [System → Metrics](/system/metrics) and [Processes](/processes) at `1440×900` (desktop) and `390×844` (phone) in `light` and `dark` themes (`docs/test-runs/2026-11-04-orchestration/contact-sheet.png`, attached).
- Across all 4 viewport/theme combinations: `0` console errors, `0` uncaught page errors, `0` HTTP `>= 400` responses, and `scrollWidth === innerWidth` (`1440px` on desktop, `390px` on phone — zero horizontal overflow).
- Clicking `#100` inside `[data-testid="direction-rollup"]` navigated in-app (`window.__spaMarker` survived, zero page reload) to `/tasks/100` and rendered closed root task `#100` cleanly.
- On [Processes](/processes), `Sync upstream latest release` rendered `on ref upstream/core refs/tags/latest` and `Next run: on ref move` (no raw `git_ref` token or fake `2027` date).

---

## Bugs Found & Fixed

1. **Bug A — `githubsim` panicked on `GET /repos/{owner}/{repo}/git/matching-refs/...` during `reconcileReleasePending` (`pkg/github/githubsim/githubsim.go`):**
   - *Reproduction:* Configure any repo with `CIMode: "github"` (such as an `upstream_repo` fork target) and run `orchestrator.RunCycle` (or `grain daemon -github-sim`). `reconcileReleasePending` calls `ListMatchingTags` (`GET /repos/.../git/matching-refs/tags/v?per_page=100`), panicking with `githubsim: unhandled request`.
   - *Fix & regression test:* Implemented `gitMatchingRefsRe` (via `git for-each-ref` on `s.BareRepo`) and `gitTagObjectRe` (via `git cat-file tag` on `s.BareRepo`) in `pkg/github/githubsim/githubsim.go`. Covered by `TestSimMatchingTagsAndAnnotatedTagObject` in `pkg/github/githubsim/githubsim_test.go`.
2. **Bug B — `githubsim` `GET /repos/{owner}/{repo}/actions/runs?head_sha=<sha>` ignored `Sim.WorkflowRuns` (`pkg/github/githubsim/githubsim.go`):**
   - *Reproduction:* Seed a workflow run via `sim.AddWorkflowRun` and call `FailedJobLogs` (used by `pull_request_status`) or `ListWorkflowRuns(owner, repo, headSHA)`. Because `?head_sha=` only consulted `s.ActionsRuns`, `pull_request_status` stalled retrying for 50s and reported no workflow runs.
   - *Fix & regression test:* Included matching `s.WorkflowRuns` entries when `head_sha` is specified in `pkg/github/githubsim/githubsim.go`. Covered by `TestSimListWorkflowRunsByHeadSHAIncludesAddWorkflowRun` in `pkg/github/githubsim/githubsim_test.go`.
3. **Bug C — `ClusterStats` and `releaseNotePeriod` ignored `Config.TimeZone` (`pkg/ui/releasenotes.go`):**
   - *Reproduction:* Set `Config.TimeZone = "America/Los_Angeles"` and query `GET /api/release-notes/stats?period=today` at `2026-11-04T02:30:00Z` (18:30 PST on `2026-11-03`). Because `c.scheduleLocation(ctx)` returned hardcoded `time.UTC`, `ClusterStats` resolved `2026-11-04` instead of `2026-11-03`.
   - *Fix & regression test:* Added `c.deploymentLocation(ctx)` reading `Config.TimeZone` (defaulting to `UTC`) in `pkg/ui/releasenotes.go` for `ClusterStats` and `releaseNotePeriod`. Covered by `TestClusterStatsAndReleaseNotePeriodReadDeploymentTimeZone` in `pkg/ui/releasenotes_test.go`.
4. **Bug D — Clicking `#<taskId>` in `ReleaseNotesPanel` caused a full page reload instead of SPA navigation (`ui/src/components/SystemPage.jsx`, `ui/src/components/ReleaseNotesPanel.jsx`):**
   - *Reproduction:* Open [System → Metrics](/system/metrics) and click `#100` under **By direction**. Because `SystemPage.jsx` did not pass `onOpenTask` to `ReleaseNotesPanel` and `<a href="/tasks/100">` had no unmodified-click handler, the browser reloaded the entire app instead of opening `#100` via `onOpenTask`.
   - *Fix & regression test:* Passed `onOpenTask={onOpenTask}` from `SystemPage.jsx` through `ReleaseNotesPanel.jsx` and handled unmodified primary clicks via `onOpenTask(dir.taskId)`. Covered by `ReleaseNotesPanel.test.jsx` (`opens a direction's root task in-app on unmodified click`) and Playwright audit (`spaMarkerSurvived: true`).
5. **Bug E — `git_ref` and `github_workflow_run` schedules showed raw kind strings and fake `+1 year` `Next run` dates on [Processes](/processes) (`ui/src/processes.js`):**
   - *Reproduction:* Create a `git_ref` schedule (`POST /api/schedules`) and open [Processes](/processes). `describeRecurrence` rendered raw `"git_ref"` and `buildProcessRows` displayed the SQLite placeholder `nextRunAt` (`Nov 5, 2027`) as the next wall-clock run.
   - *Fix & regression test:* Formatted `git_ref` (`on ref <repo> <ref>`) and `github_workflow_run` (`on workflow <workflow>`) in `describeRecurrence`, and set `nextRunAt: null` with `nextLabel: "on ref move"` / `"on workflow run"` (and excluded event triggers from `nextThrough`) in `buildProcessRows`. Covered by `ui/src/processes.test.js` and Playwright audit on `/processes`.

## Follow-up Proposed

- **Proposed via `propose_task` (depends on `#3075`):** *Refuse forbidden `git_ref` watched repos in schedule API and surface off-target suppression on `/processes`* — `POST /api/schedules` currently returns `201 Created` when `gitRef.repo` names the deployment's encrypted secrets backup repo or an off-`TargetRepos` repo, relying solely on the daemon's runtime refusal log rather than rejecting on API write or showing a `suppressed` badge in `/processes`.
