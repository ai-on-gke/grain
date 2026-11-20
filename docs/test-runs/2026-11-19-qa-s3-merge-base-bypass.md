# QA S3 Git Merge-Base Resolution, Staging CI Refs, and Human S3 Bypass Walkthrough (2026-11-19)

Plan: `docs/test-plans/2026-11-19.md` §3 (from [Task #7303](/tasks/7303), auditing PR #1882 `c48ac453c`).
Task: [Task #7319](/tasks/7319).

- **Commit walked:** `1a449c7d5` (`origin/main` at 2026-11-19).
- **Helpers:** [`docs/test-runs/2026-11-19-qa-s3-merge-base-bypass/`](2026-11-19-qa-s3-merge-base-bypass/) (`ghsim.go`, `fake-agy.py`, `cred_probe.go`, `env.sh`, `run_walkthrough.sh`, `ui_walkthrough.mjs`).
- **QA engine revision:** `QAEngineRev = 7` (untouched; no non-test files under `pkg/qa/**` were modified).

## Summary of Results

| # | Walkthrough Item | SQLite | PostgreSQL | Outcome |
|---|---|---|---|---|
| 1a | Shallow-clone `git merge-base` S3 resolution (controller mirror + `depth=1` sandbox checkout) | pass | pass | Deepened past shallow horizon (`--deepen=256`, 70 commits diverged on `main`), resolved exact `FORK_SHA` (`!= ROOT_SHA`, `!= MAIN_SHA`), promoted staging ref after turn-end S4, and opened PRs `#9000` / `#9001`. |
| 1b | Missing / deleted target base branch (`OpenPullRequestForTaskWithQA` + turn-end `salvageQAStageIfPresent`) | pass after fix | pass after fix | Returns `checks.ErrBaseResolutionFailed` and persists diagnostic `qa:base-resolution` (`verdict=error`, `baseSha=""`) with `s3Blocked=true`. **F1 fixed:** turn-end `salvageQAStageIfPresent` now also persists `qa:base-resolution` on `ErrBaseResolutionFailed`. |
| 1c | Embedded remote URL userinfo redaction (`http://botuser:secret@…` & username-only PAT style) | pass | pass | Direct `checks.OpenMirrorWorkspace` probe (`cred_probe.go`) + live daemon check confirmed zero credentials in git argv, returned errors, `daemon.log`, or persisted `qa:base-resolution` check runs. |
| 2 | `QAEngineRev = 7` rule severities (`standard-workflow`, `fast-baseline`, `gates-only`) | pass | pass | `golden:strict-config-decoder`, `golden:sibling-export-hazard`, and `golden:ui-budget-and-spec-floor` default to `warn` and permit promotion (`acme/widgets#9003`); `golden:test-integrity-guard` defaults to `blocking` and holds commits on `grain-qa-stage/<id>` with zero PR branch created. |
| 3a | Staging CI ref propagation — Grain CI mode (`ci_mode: grain`) | pass | pass | Sandbox `grain ci wait`, `grain ci list`, `grain pr status`, and API `GET /api/ci/runs` + `POST /api/ci/runs` target `grain-qa-stage/<id>` before promotion, then switch seamlessly to `task.BranchName()` once promoted and deleted. |
| 3b | Staging CI ref propagation — GitHub Actions / `ExternalCI` (`ci_mode: github`) | pass after fix | pass after fix | API `GET /api/ci/runs?provider=github-actions`, `POST /api/ci/runs/gha:.../rerun`, and sandbox `grain ci list`, `grain ci wait`, `grain ci retrigger`, `grain pr status` target `grain-qa-stage/<id>` then switch to `task.BranchName()`. **F2 fixed:** `grain pr status` (`pkg/gitproxy/cli.go`) now queries `ExternalCI` via `findCIRuns` instead of Grain-CI-only `store.Find`. **F3 fixed:** `/next/task/<id>` Changes tab (`ui-ng`) now passes `provider=github-actions` when `repoProvider === "github-actions"` and routes Actions re-runs through `POST /api/ci/runs/{id}/rerun`. |
| 4a | Human S3 bypass happy path (`POST /api/tasks/{id}/qa/bypass-s3` & `<g-bypass-s3-dialog>`) | pass | pass | Tested live via Playwright dialog on SQLite and via signed ES256 Hub JWT (`alice@example.com`, 500-multibyte-rune reason, 12-char pinned `head`) on PostgreSQL: promoted & deleted `grain-qa-stage/<id>`, opened PR, withdrew `autoMerge` with `auto_merge_off` transition + persisted `auto-merge-awaits-person` `task_link` row, preserved S5 `qa:merge-triage` (`triageDecision: "require_human"`) and `approval` merge gate, and recorded `qa:s3-operator-bypass` (`verdict=bypassed`) + audit comment. |
| 4b | Human S3 bypass authorization, validation & concurrency refusal matrix (12 categories / 20 assertions) | pass | pass | Every refusal returned the exact expected HTTP code: external agent token with `merge:write` (`403`), delegated human (`403`), viewer/contributor lacking `merge:write` (`403`), human lacking write on secondary `task.Writes` repo `acme/secondary` (`403`), closed task (`400`), no target repo (`400`), blank/whitespace/501-ASCII/501-multibyte reason (`400`), short/non-hex `head` (`400`), unknown JSON field (`400`), stale pinned `head` (`409` without promoting newer commit), unblocked task (`409`), open PR with no staged commits (`409`). |
| 5 | `/next/task/<id>` Changes tab & `<g-bypass-s3-dialog>` browser + axe walkthrough (1440×900 & 390×844, light & dark) | pass after fix | n/a (UI driven against live SQLite daemon + Playwright `grain demo` suite) | Initial focus on `[data-testid="bypass-s3-reason"]`, disabled submit while blank/whitespace, staging branch + 12-char SHA display, Escape/Cancel dismissal returning focus, zero horizontal overflow, zero axe violations across all 4 viewport/theme combinations. **F4 fixed:** stale-head `409 Conflict` now renders an inline `role="alert"` error banner inside `<g-bypass-s3-dialog>` while preserving the typed reason instead of writing behind the inert modal backdrop. |

---

## Environment & Stubbed Boundaries

- **Binary:** built from `HEAD` (`1a449c7d5`, before and after fixes) via `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (`go1.26.10 linux/amd64`, Node `v20.19.0`).
- **System packages installed in stock sandbox:** `postgresql` (`15.20-0+deb12u1`), `postgresql-client`, `sqlite3` (`3.40.1`); `jq` and ImageMagick (`montage`, `convert`, `compare`) were preinstalled; Chromium `151.0.7922.34` at `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Daemon:** real `grain daemon -sandbox-port-base=0 -host-sandboxes -ui-addr 127.0.0.1:4282 -as alice@example.com -repo acme/widgets -default-target-repo acme/widgets -poll-interval 500ms` with Hub JWKS auth pointed at `ghsim` (`127.0.0.1:4286/.well-known/jwks.json`) and seeded role grants for `alice@example.com` (operator), `viewer@example.com` (viewer), `contrib@example.com` (contributor), and `widgets-merger@example.com` (scoped to `acme/widgets` only).
- **Stores verified end-to-end:**
  - **SQLite (`3.40.1`):** `$WORK_DIR/data/store/grain.db`.
  - **PostgreSQL (`15.20`):** `-db-driver postgres -db-dsn postgres://grain:grain@127.0.0.1:5432/grain_walk_s3?sslmode=disable`, freshly dropped and recreated before the run.
- **Stubbed external boundaries only:**
  - `ghsim.go` (`127.0.0.1:4286`): real `pkg/github/githubsim` + `git http-backend` serving real bare `acme/widgets.git` (seeded with `ROOT_SHA` → `FORK_SHA` → 70 commits ahead on `main` to exceed `--depth=64`, plus `temp-base`, `temp-base-salvage`, `fork-point`, and `.grain/workflows/ci.yml`) and `acme/secondary.git`, plus ES256 Hub JWKS (`/.well-known/jwks.json`, `/_sim/mint-jwt` with 2-minute assertion TTL) and GitHub Actions endpoints (`/_sim/seed-actions`, `/_sim/check-runs`, `/_sim/reruns`, `/_sim/prs`, `/_sim/branches`).
  - `fake-agy.py`: deterministic Antigravity CLI stub speaking real `grain mcpserver` stdio JSON-RPC (`run_command`, `ask_question`, `comment_on_issue`) and returning `[]` cleanly for isolated S4 verification prompts.

---

## Defects Found & Fixed

### F1 — Turn-end `salvageQAStageIfPresent` dropped `qa:base-resolution` diagnostic check run on `ErrBaseResolutionFailed`

- **Location:** `pkg/orchestrator/qa_gate.go` (`salvageQAStageIfPresent`).
- **Reproduction:** Create a treatment-arm task targeting branch `temp-base-salvage`, push a commit to `grain-qa-stage/<id>`, delete `temp-base-salvage` on the remote, and let the agent exit without calling `grain pr open` so turn-end salvage runs S3.
- **Observed before fix:** `OpenPullRequestForTaskWithQA` called `recordBaseResolutionErrorCheckRun` when `checks.OpenMirrorWorkspace` returned `checks.ErrBaseResolutionFailed`, but `salvageQAStageIfPresent` logged and returned immediately without calling `recordBaseResolutionErrorCheckRun`. As a result, `GET /api/tasks/{id}/qa` had zero `qa:base-resolution` diagnostic check runs and `s3Blocked` stayed `false`.
- **Fix:** Call `recordBaseResolutionErrorCheckRun(ctx, store, task, assign, headSHA, mErr)` in `salvageQAStageIfPresent` when `errors.Is(mErr, checks.ErrBaseResolutionFailed)`.
- **Regression test:** `TestSalvageQAStageRecordsBaseResolutionErrorCheckRun` in `pkg/orchestrator/qa_gate_test.go`.

### F2 — Sandbox `grain pr status` ignored `ExternalCI` (`ci_mode: github`) on both staging and promoted branches

- **Location:** `pkg/gitproxy/cli.go` (`serveBranchStatus` and its call site in `ServeHTTP`).
- **Reproduction:** Set `acme/widgets` to `ci_mode: github`, seed a GitHub Actions workflow run on `grain-qa-stage/<id>` (and later on the promoted `task.BranchName()`), and run `grain pr status` inside the task sandbox.
- **Observed before fix:** `activeTaskBranch` called `p.findCIRuns` (which queries both Grain CI and `ExternalCI`) and correctly resolved `grain-qa-stage/<id>`, but `serveBranchStatus` then called `store.Find(...)` (Grain CI store only). Even though `serveBranchStatus` already had GitHub Actions formatting code for `provider == "github-actions"`, it always saw 0 runs and printed `grain-qa-stage/<id> (this task's branch) in acme/widgets has no CI run recorded yet.`
- **Fix:** Pass `r.Context()` into `serveBranchStatus`, query `p.findCIRuns(ctx, store, ...)` instead of `store.Find`, and surface `page.note` when zero runs are returned.
- **Regression test:** `TestPRStatusReportsExternalCIOnStageBranchAndAfterPromotion` in `pkg/gitproxy/cli_actions_test.go`.

### F3 — `/next/task/<id>` Changes & CI tab hid GitHub Actions runs on `ci_mode: github` repos and misrouted Actions re-runs

- **Location:** `ui-ng/src/app/api/models.ts` (`CIRunsResponse`) and `ui-ng/src/app/task/task-changes-tab.component.ts` (`loadInitial`, `rerunCi`).
- **Reproduction:** Open `/next/task/<T6>` Changes tab on a `ci_mode: github` repo with a GitHub Actions run recorded on `grain-qa-stage/<T6>` (or on the promoted task branch).
- **Observed before fix:** `GET /api/ci/runs` omits `ExternalCI` runs for human callers unless `provider=github-actions` is explicitly passed (`pkg/ui/ci_actions.go`), returning `{ runs: [], repoProvider: "github-actions" }`. `TaskChangesTabComponent` ignored `repoProvider`, so `/next/task/<T6>` rendered `"No CI runs recorded for this task yet."` for both staging and promoted branches. Additionally, clicking `Re-run CI` / `Re-run failed jobs` always posted to `POST /api/ci/runs` (Grain CI) instead of `POST /api/ci/runs/{id}/rerun` for `gha:*` runs.
- **Fix:** Add `repoProvider?: string` to `CIRunsResponse`; when `repoProvider === "github-actions"`, re-query `t.branch` and `stageRef` with `&provider=github-actions`; route `rerunCi` through `POST /api/ci/runs/{id}/rerun` (`{ all: !failedOnly }`) when the latest run ID starts with `gha:`.
- **Regression test:** Added unit test in `ui-ng/src/app/task/task-page.component.spec.ts` verifying both `provider=github-actions` staging-ref loading and `POST /api/ci/runs/{id}/rerun` dispatch.

- **Review follow-up (security review task 7510):** picking `ciRuns()[0]` alone broke `Re-run failed jobs` on multi-workflow GitHub Actions repositories, where the newest run at a head can be a passing workflow while another workflow at the same head failed. `rerunCi` now uses `actionsRunsToRerun`, which posts `POST /api/ci/runs/{id}/rerun` for every concluded `gha:*` run at the newest Actions head commit (only failed, errored, cancelled or timed-out ones for `Re-run failed jobs`). Regression: `ui-ng/src/app/task/actions-rerun.spec.ts`.

### F4 — `<g-bypass-s3-dialog>` swallowed stale-head `409 Conflict` errors behind the inert modal backdrop

- **Location:** `ui-ng/src/app/task/bypass-s3-dialog.component.ts` and `ui-ng/src/app/task/task-changes-tab.component.ts` (`emitBypassS3`).
- **Reproduction:** Open `<g-bypass-s3-dialog>` on `/next/task/<id>`, push a newer commit to `grain-qa-stage/<id>` so the pinned `head` goes stale, type a reason, and click `Bypass S3 gate & open PR`.
- **Observed before fix:** The daemon returned HTTP `409 Conflict` (`branch head moved since you reviewed it: expected …, actual head is …`), and `<g-bypass-s3-dialog>` stayed open with the typed reason intact, but `emitBypassS3` routed the call through `store.act`, which wrote the error into `[data-testid="task-action-error"]` inside `<article [attr.inert]>` underneath `.modal-backdrop`. Inside the dialog itself, no error alert was displayed (before/after pixel diff: `347,495` changed pixels on `stale-409-desktop-light.png`).
- **Fix:** Extend `BypassS3Request.run` to `Promise<boolean | string>`, post directly in `emitBypassS3` (returning `errorText(err)` on failure while refreshing `taskQa` in the background so the refreshed head is available on reopen), and render an inline `<p class="banner banner-error" role="alert" data-testid="bypass-s3-error">` inside `<g-bypass-s3-dialog>` using existing Material 3 banner tokens (clearing when the user edits the reason).
- **Regression tests:**
  - Unit test in `ui-ng/src/app/task/bypass-s3-dialog.component.spec.ts` covering inline alert rendering, preserved reason, and clearing on edit.
  - Playwright e2e coverage in `ui-ng/e2e/next-qa-bypass-s3.spec.ts` verifying `409` inline alert, `role="alert"`, preserved textarea value, and zero axe violations before retrying to `200`.

---

## Detailed Walkthrough Evidence (SQLite & PostgreSQL)

### Item 1: Shallow-Clone `git merge-base`, Missing Base Branch, and Credential Redaction

- **Seeded topology:** `acme/widgets.git` has `ROOT_SHA` → `FORK_SHA` (`fork-point` branch) → 70 commits ahead on `main` (exceeding `--depth=64`).
- **1a Controller mirror & `depth=1` sandbox:** Both paths deepened past the shallow horizon and computed `baseSha == FORK_SHA` (`cd4716ed59ef` on SQLite, `de79ddec9f81` on PostgreSQL — neither `ROOT_SHA` nor `MAIN_SHA`), passed S3 on `grain-qa-stage/<id>`, deferred to S4 turn-end promotion, and opened PRs `#9000` and `#9001`.
- **1b Missing base branch (`temp-base` & `temp-base-salvage`):** Deleting the base branch caused `grain pr open` to return HTTP `500` with `qa/checks: failed to resolve base commit: git fetch base origin/temp-base failed: exit status 128`, persisted `qa:base-resolution` (`verdict=error`, `baseSha=""`), and marked `s3Blocked=true`. After F1, turn-end salvage (`salvageQAStageIfPresent`) likewise persisted `qa:base-resolution`.
- **1c Credential redaction (`cred_probe.go` + daemon log audit):** Running `checks.OpenMirrorWorkspace` against `http://botuser:s3cr3t-w4lk-t0k3n@127.0.0.1:4286/acme/widgets.git` and username-only `http://ghp_useronly_s3cr3t@127.0.0.1:4286/acme/widgets.git` under a logging `git` wrapper confirmed:
  - `deepenInvoked: true` (`--deepen=256`), `baseSha == FORK_SHA`, diff contained only the task's `pkg/clean/clean.go` (`diffHasMainOnly: false`),
  - missing branch returned `checks.ErrBaseResolutionFailed` (`isBaseResErr: true`),
  - `leaks: []` (`0` occurrences of `s3cr3t-w4lk-t0k3n`, `botuser`, or `ghp_useronly_s3cr3t` across all 12 git argv invocations, returned errors, `daemon.log`, and persisted `qa:base-resolution` check runs).

### Item 2: `QAEngineRev = 7` Rule Severities

- `GET /api/qa/flows` verified all three built-in flows (`standard-workflow`, `fast-baseline`, `gates-only`) report:
  - `golden:strict-config-decoder`: `warn`
  - `golden:sibling-export-hazard`: `warn`
  - `golden:ui-budget-and-spec-floor`: `warn`
  - `golden:test-integrity-guard`: `blocking`
- **2a Warning-only promotion:** Pushing a staged commit adding a `json.NewDecoder` without `DisallowUnknownFields()` recorded a `severity=warn` finding on `golden:strict-config-decoder` and still permitted staging promotion and PR creation (`acme/widgets#9003`).
- **2b Blocking hold:** Pushing a staged commit with an empty `func TestEmpty(t *testing.T) {}` failed `golden:test-integrity-guard`, set `s3Blocked=true`, `stageBranch="grain-qa-stage/7"`, refused promotion, and left `task.BranchName()` absent on the remote.

### Item 3: Staging CI Ref Propagation (`grain-qa-stage/<id>` → `task.BranchName()`)

- **3a Grain CI mode (`ci_mode: grain`):** While commits were held on `grain-qa-stage/8`, sandbox `grain ci wait`, `grain ci list`, `grain pr status`, and API `GET /api/ci/runs?repo=acme/widgets&ref=grain-qa-stage/8` + `POST /api/ci/runs` (`{"taskId":"8","recheck":true}`) all targeted `grain-qa-stage/8`. After promotion and deletion of `grain-qa-stage/8`, a new commit on `task.BranchName()` caused `grain pr status` and `grain ci list` to switch cleanly to `grain/<hash>/task-8-staging-ci-grain-ci-mode`.
- **3b GitHub Actions / `ExternalCI` (`ci_mode: github`):**
  - Seeded failing workflow run `91001` on `grain-qa-stage/9`. Verified `GET /api/ci/runs?repo=acme/widgets&ref=grain-qa-stage/9&provider=github-actions`, `POST /api/ci/runs/gha:acme:widgets:91001/rerun` (recording `RerunFailedJobs` on `ghsim`), and sandbox `grain ci list`, `grain pr status`, `grain ci wait`, and `grain ci retrigger` all resolved `grain-qa-stage/9` (`gha:acme:widgets:91001`).
  - After S3 bypass promoted and deleted `grain-qa-stage/9` and seeded passing run `91002` on `task.BranchName()`, sandbox `grain pr status`, `grain ci list`, and `grain ci wait` switched seamlessly to `grain/<hash>/task-9-staging-ci-github-actions-mode` (`gha:acme:widgets:91002`, `success`).
  - Verified `/next/task/<T6>` Changes & CI tab lists `ci-run-gha:acme:widgets:...` after F3 (before F3 it showed zero runs).

### Item 4: Human S3 Bypass (`POST /api/tasks/{id}/qa/bypass-s3`) Matrix

- **4a Happy path (SQLite via `<g-bypass-s3-dialog>` and PostgreSQL via signed ES256 Hub JWT `alice@example.com` with 500-multibyte-rune `"é"*500` reason and 12-char pinned `head`):**
  - Promoted `grain-qa-stage/10` → `task.BranchName()` and deleted `grain-qa-stage/10` on remote.
  - Opened PR (`acme/widgets#9006`).
  - Withdrew `autoMerge` (`false`), recorded `auto_merge_off` transition naming the operator and reason, and persisted `auto-merge-awaits-person` in SQL `task_link` (`S3 pre-PR quality gate bypassed by operator; human review required before merge`) on both SQLite and PostgreSQL.
  - Preserved S5 merge triage (`qa:merge-triage` persisted, `qaSummary.triageDecision == "require_human"`, `approval` merge gate `needed`).
  - Recorded `qa:s3-operator-bypass` (`verdict: "bypassed"`, `commitSha`, `bypassedBy`, `expectedHead`, `reason`) and closing audit comment; cleared `s3Blocked`.
- **4b Refusal matrix (identical results on SQLite & PostgreSQL):**
  - `403`: external agent token holding `merge:write`; delegated human Hub JWT (`delegate` claim, refusal explains a person directly is required); `viewer@example.com` and `contrib@example.com` lacking `merge:write`; `widgets-merger@example.com` holding `merge:write` on `acme/widgets` only but lacking write on secondary `task.Writes` repo `acme/secondary` (refusal names `acme/secondary`).
  - `400`: closed task; standalone `noRepo: true` task; empty reason, whitespace-only reason, 501-ASCII-rune reason, 501-multibyte-rune reason (`"é"*501`); short `head` (`"abc"`), non-hex `head` (`"zzzzzzzz"`); unknown JSON field (`extra`).
  - `409`: stale pinned `head` after a newer push to `grain-qa-stage/<id>` (confirmed newer commit was **not** promoted); unblocked task; task already having an open PR with no staged commits on `grain-qa-stage/<id>` (even after recording an S3 fail check run on the PR head).

### Item 5: `/next/task/<id>` Browser & Accessibility Walkthrough

- Driven against the live SQLite daemon in Playwright Chromium `151.0.7922.34` across 4 viewport/theme combinations (`1440×900` light/dark, `390×844` light/dark) via `ui_walkthrough.mjs`:
  - **All 35 UI assertions passed (0 failures)** after F3 & F4.
  - Verified light vs dark theme rendering by mean luminance: desktop light `0.654` vs dark `0.100`; phone light `0.774` vs dark `0.158`.
  - Verified initial focus lands on `[data-testid="bypass-s3-reason"]`, submit stays disabled while empty or whitespace-only, staging branch + 12-char commit SHA render cleanly, Escape and Cancel dismiss the dialog and return focus to `[data-testid="qa-bypass-s3-btn"]`, zero horizontal overflow, dialog fits viewport, and **zero axe violations** across all 4 viewport/theme combinations.
  - Verified stale-head `409` visibly renders `[data-testid="bypass-s3-error"]` (`role="alert"`) inside `<g-bypass-s3-dialog>` while preserving the typed reason.
  - Verified `T2` displays `qa:base-resolution`, `T3` displays `golden:strict-config-decoder warn`, `T6` displays GitHub Actions runs, and `T7` happy bypass closes the dialog, hides the blocked bar, and renders `qa:s3-operator-bypass bypassed`.

---

## Automated Verification Results

| Suite | Command | Result |
|---|---|---|
| Live SQLite walkthrough | `STORE=sqlite KEEP_RUNNING=1 SKIP_HAPPY_BYPASS=1 ./docs/test-runs/2026-11-19-qa-s3-merge-base-bypass/run_walkthrough.sh` | **75 passed, 0 failed** |
| Live SQLite UI walkthrough | `node ./docs/test-runs/2026-11-19-qa-s3-merge-base-bypass/ui_walkthrough.mjs .../ui-state.json .../after all` | **35 passed, 0 failed** |
| Live PostgreSQL walkthrough | `STORE=postgres KEEP_RUNNING=0 SKIP_HAPPY_BYPASS=0 ./docs/test-runs/2026-11-19-qa-s3-merge-base-bypass/run_walkthrough.sh` | **87 passed, 0 failed** |
| Go unit & race tests (with `GRAIN_REQUIRE_POSTGRES=1`) | `go test -race -count=1 ./pkg/gitproxy/...`; `go test -race -count=1 ./pkg/orchestrator -run "TestQA\|TestSalvageQA\|..."`; `go test -count=1 ./pkg/ui -run "TestStaging\|TestBypass\|TestGetTaskQA\|TestAngularRouteTableInSync"`; `go test -count=1 ./tests/security -run TestRouteAuthorizationMatrix`; `go test -count=1 ./pkg/qa/... ./pkg/ports/... ./tests/hygiene/...` | **All PASS** |
| Angular unit tests (`ui-ng`) | `cd ui-ng && npm test` | **76 files / 830 tests PASS** |
| Angular Playwright e2e (`ui-ng`) | `cd ui-ng && npx playwright test e2e/next-qa-bypass-s3.spec.ts e2e/next-full-system.spec.js` | **8 passed (desktop, phone, dark + full-system contrast)** |
