# Walkthrough: review findings, Accept findings and review gates (`docs/test-plans/2026-11-16.md` §3)

Executed by [Task #6476](/tasks/6476) (filed by [Task #6330](/tasks/6330)) against a real `grain daemon -sandbox-port-base=0 -host-sandboxes` with a local GitHub simulator (`ghsim.go`), a deterministic fake agent CLI (`fake-agy.py`), real headless Chromium via Playwright (`walk_ui.mjs`), and both **SQLite** and **PostgreSQL** backing stores.

## Base commit and environment

- **Base commit (`HEAD`):** `a771a997344da6009bdaccb5e0e435e8f0895857`
- **Daemon:** real compiled `grain daemon -sandbox-port-base=0 -host-sandboxes`
- **Stores tested:**
  - SQLite `3.40.1` (BEFORE fixes and AFTER fixes)
  - PostgreSQL `15.20` (AFTER fixes, identical 65/65 assertions)
- **Browser / Playwright:** Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`)
- **Viewports & themes verified:** `1440x900` (desktop) and `390x844` (phone), light and dark (`localStorage["grain.themeMode"]`)
- **Harness:** [`docs/test-runs/2026-11-16-review-findings-gates/`](2026-11-16-review-findings-gates/)
  - `ghsim.go` — local GitHub REST + git HTTP simulator supporting `acme/widgets`, pull requests, reviews, comments, merges, check-runs, and paired branches (`main` / `candidate`).
  - `fake-agy.py` — deterministic fake agent CLI speaking `grain-sandbox` MCP (`run_command`, `add_review_comment`, `request_human_review`, `comment_on_issue`).
  - `env.sh` — sets up the config repo (`settings/access.yml`, `settings/capabilities.yml`, `settings/reviews.yml`, `settings/repos.yml`), `acme/widgets` (`.grain/triggers/*.yml`, `.grain/schedules/*.yml`, `.grain/templates/*.yml`, `.grain/workflows/*.yml`), starts `ghsim` and `grain daemon` on ephemeral ports with two access-code users: `alice@example.com` (full access, `grain/operator`) and `bob@example.com` (repo-scoped writer on `acme/widgets`, `tasks:read` + `tasks:write`, `full: false`).
  - `walk_ui.mjs` — Playwright inspector and screenshot capturer for `/next/task/<id>?tab=changes`, `/next/processes/declarations`, and classic `/tasks/<id>`.
  - `run_walkthrough.sh` — end-to-end driver covering all §3 items (`EXPECT_FIXED=0` reproduces all 8 bugs on base `HEAD`; `EXPECT_FIXED=1` asserts all 65 checks pass after fixes).
- **Sandbox packages installed during run:** `postgresql`, `sqlite3`, `socat`.

## Summary of runs

| Run | Binary | Store | Result |
| --- | --- | --- | --- |
| Baseline reproduction (`EXPECT_FIXED=0`) | Base `a771a9973` | SQLite 3.40.1 | `PASS=53, FAIL=0, BUG_REPRO=12` (8 distinct bugs reproduced deterministically) |
| Post-fix verification (`EXPECT_FIXED=1`) | Fixed branch | SQLite 3.40.1 | `PASS=65, FAIL=0, BUG_REPRO=0` (12 AFTER screenshots, 0 page errors, 0 horizontal overflows) |
| Post-fix verification (`EXPECT_FIXED=1`) | Fixed branch | PostgreSQL 15.20 | `PASS=65, FAIL=0, BUG_REPRO=0` |

## Item-by-item walkthrough outcomes

### Item 1: Accept findings on `/next/task/<id>?tab=changes`, reconcile stability, re-review, and review-less gate

- Configured `settings/reviews.yml` (`every-pr`, `mode: findings`), a template-less sensitive path trigger `.grain/triggers/payments.yml` (`paths: ["pkg/payments/**"]`, `sensitive: true`, `human: on_findings`), and a trust-boundary touch (`pkg/ci/payments_hook.go` + `pkg/payments/charge.go`) so one task (`T1`) triggered all three review gates concurrently: `security-review`, `review`, and `targeted-review`.
- Verified `security-review`, `review`, and `targeted-review` all report `state: "findings"` and `acceptable: true` once fixed (Bug 1).
- Verified `/next/task/<T1>?tab=changes` as full-access `alice@example.com`:
  - Offers `Accept findings…` beside each acceptable findings gate (`security-review`, `review`, `targeted-review`).
  - Clicking `Accept findings…` opens `accept-findings-dialog` with confirm disabled until a reason is entered; submitting pins the PR commit SHA (`ci.commit`) rather than the branch name (`DiffPreview.head`) and succeeds HTTP 200 (Bug 6).
  - Accepting `security-review` leaves `review` and `targeted-review` findings intact, and across ≥5 daemon reconcile ticks (`SyncHumanReviewHolds` / `SyncSecurityHolds`) the accepted security findings stay accepted and do not re-appear (Bugs 4 & 5).
  - Accepting `targeted-review` via `POST /api/tasks/{id}/accept-findings` (`gate: "targeted-review"`) succeeds HTTP 200 and clears only `targeted-review`, leaving `review` findings standing (Bug 1).
  - Opening `Force merge…` while acceptable findings remain displays the `force-merge-accept-findings-hint` pointing operators to `Accept findings…`.
- Verified `/next/task/<T1>?tab=changes` as repo-scoped writer `bob@example.com` (`full: false`, holding `tasks:write` on `acme/widgets`):
  - `Accept findings…` is hidden (Bug 2) and direct `POST /api/tasks/{id}/accept-findings` returns HTTP 403.
  - `Override review gate…` and `Release hold` are hidden.
- **Re-review raising new findings (`Item 1B`):** pushed a second commit on `T1`'s branch; a fresh security review left a new finding, restoring the `security-findings` hold and `security-review` `findings` gate.
- **Gate without a review behind it (`Item 1C`):** seeded a `review-findings` record with no `reviewTask` (`gating review closed without finishing`); confirmed `GET /api/tasks/{id}` returns `state: "findings", acceptable: false`, `/next/` hides `Accept findings…`, and the Force merge dialog omits the Accept findings hint.

### Item 2: Release hold and Override review gate (findings-only vs mixed hold, `/next/` and classic)

- **Findings-only hold (`T1`):**
  - `POST /api/tasks/{id}/human-review/release` returns HTTP 409 with `code: "review_findings_hold"` and verbatim message explaining that review findings cannot be released directly.
  - On `/next/task/<T1>`, full-access `alice@example.com` sees `Override review gate…` (`hold-force-merge-open`) and no `Release hold`; repo-scoped writer `bob@example.com` sees neither.
  - On classic `/tasks/<T1>`, `Release hold` is hidden for both users.
- **Mixed hold (`T2` — both `security-findings` and `requested` `product-decision` reasons):**
  - On `/next/task/<T2>`, `alice@example.com` sees both `Release hold` and `Override review gate…` plus the note `"Release hold releases the other reasons only."`; `bob@example.com` sees neither.
  - On classic `/tasks/<T2>`, `bob@example.com` no longer sees an enabled `Release hold` button (Bug 3).
  - Clicking `Release hold` as `alice@example.com` returns HTTP 200 with the remaining `security-findings` reason still present; afterwards `/next/` drops `Release hold` while keeping `Override review gate…`.

### Item 3: Review task titles and `targeted-review` gate

- Verified exact titles:
  - Security review: `Security review: Add payment CI hook`
  - Every-PR code review: `Code review: Add payment CI hook (acme/widgets#9000)`
  - Single-trigger targeted review: `Targeted review (payments): Add payment CI hook`
  - Multi-trigger targeted review (`Item 3B`, touching `pkg/payments/**` and `pkg/auth/**`): alphabetical `Targeted review (auth, payments): Rotate payment auth`
  - Empty-title fallback (`Item 3C`): `Code review: <id> (<pr>)` and `Targeted review (payments): <id>`
- Verified overriding `review` via `POST /api/tasks/{id}/force-merge` (`overrideGates: ["review"]`) also satisfies `targeted-review` (`state: "overridden"`).
- Verified `targeted-review` renders on both `/next/task/<id>?tab=changes` and classic `/tasks/<id>`.

### Item 4: Declared credentials and widened `repo#write` vs plain `repo#write {repo}`

- Committed to `acme/widgets`:
  - `.grain/schedules/release-cred.yml` (`repo#credential {credential: release-bot}`)
  - `.grain/schedules/release-push.yml` (`repo#write {refs: ["refs/heads/*", "refs/tags/*"], force: true}`)
  - `.grain/workflows/release-cred-ci.yml` (`cred-job` with `repo#credential`, `widened-job` with `repo#write {refs, force}`, and `plain-job` with plain `repo#write {repo: acme/widgets}`)
  - `.grain/templates/plain-release-train.yml` (plain `repo#write {repo: acme/widgets}`)
- Verified:
  - Both privileged schedules fire held (`state: "proposed"`) and appear awaiting acceptance in `/api/declarations`.
  - Both privileged CI jobs (`cred-job`, `widened-job`) are refused and recorded awaiting acceptance under their stable workflow job IDs `ci:acme/widgets:cred-job` and `ci:acme/widgets:widened-job` (Bug 7).
  - Plain `repo#write {repo}` (`plain-job` and `plain-release-train.yml`) is NOT held; `plain-job` ran immediately and passed.
  - `/next/processes/declarations?repo=acme%2Fwidgets` renders all 4 awaiting declarations cleanly in Playwright with zero page errors.
  - Repo-scoped writer `bob@example.com` is refused HTTP 403 when attempting to accept a `repo#credential` declaration.
  - After full-access `alice@example.com` accepts `release-cred`, the schedule fires approved (`state != "proposed"`).
  - After `alice@example.com` accepts `ci:acme/widgets:widened-job`, re-running the workflow runs `widened-job` and passes (Bug 7).

### Item 5: Port grant filtering (template-restricted dropped, catalogue-disabled kept and held)

- Configured paired branches (`main` -> `candidate`), disabled `https://grain.dev/capabilities/api#ci-read` in `settings/capabilities.yml`, and created repo template `.grain/templates/release-note.yml` declaring both a template-restricted grant (`read-issues`, restricted to `release-note` in `settings/capabilities.yml`) and the catalogue-disabled `https://grain.dev/capabilities/api#ci-read`.
- Filed source task from `repo:template:acme:widgets:release-note`, pushed a commit that conflicts mechanically on `candidate`, and merged its PR into `main` so the orchestrator filed an agent port onto `candidate`.
- Verified on the filed port task:
  - `task_filed_template` is empty (ad-hoc port), while `sourceTemplateId` preserves `repo:template:acme:widgets:release-note`.
  - Template-restricted `read-issues` was dropped, and its drop explanation (naming `read-issues` and `settings/capabilities.yml`) survived the mechanical-conflict body rewrite (Bug 8).
  - Catalogue-disabled `https://grain.dev/capabilities/api#ci-read` was retained, dispatch recorded `task_grant_wait` (`capability-catalogue|capability api:ci-read is disabled in settings/capabilities.yml`), and the port ran 0 agent attempts rather than executing without its grant.

## Bugs reproduced and fixed

| # | Area | Symptom on base `a771a9973` | Root cause | Fix | Regression test(s) |
| --- | --- | --- | --- | --- | --- |
| 1 | `targeted-review` findings acceptance | `targeted-review` gate reported `acceptable: false`, had no `Accept findings…` button, and `POST /api/tasks/{id}/accept-findings` rejected `gate: "targeted-review"` with HTTP 400; meanwhile `review` gate incorrectly covered targeted-review findings records. | `findingsGateCovers` and `questionsGate` in `pkg/model/review_findings_accept.go` did not recognize `GateTargetedReview` or exclude targeted triggers from `GateReview`; `pkg/ui/accept_findings.go` rejected `GateTargetedReview`. | Added `isTargetedReviewTrigger`, updated `findingsGateCovers` / `questionsGate` / `AcceptReviewFindings` in `pkg/model/review_findings_accept.go`, allowed `GateTargetedReview` in `pkg/ui/accept_findings.go`, and updated CLI flag help in `pkg/cli/taskverbs.go`. | `TestTargetedReviewFindingsAcceptableAndSeparateFromCodeReview` (`pkg/model`), `TestAcceptFindingsTargetedReviewGate` (`pkg/ui`), `ui-ng/e2e/next-accept-findings.spec.js` |
| 2 | `/next/` Changes tab access check | Repo-scoped writer (`full: false`, holding `tasks:write` on the repo) was shown enabled `Accept findings…` buttons that always failed HTTP 403 on submit. | `offersAcceptFindings` in `ui-ng/src/app/task/task-changes-tab.component.ts` checked `can("POST /api/tasks/{id}/accept-findings")` without `this.access.isFullAccess()`, unlike `ui.Client.AcceptReviewFindings` (which requires full access). | Required `this.access.isFullAccess()` in `offersAcceptFindings`. | `task-page.component.spec.ts`, `ui-ng/e2e/next-accept-findings.spec.js` |
| 3 | Classic `/tasks/<id>` Release hold access check | Repo-scoped writer (`full: false`) saw an enabled `Release hold` button on a mixed hold even though `POST /api/tasks/{id}/human-review/release` requires full access (403). | `ui/src/components/detail/Actions.jsx` wrapped `Release hold` in `<AccessGate scope="tasks:write">` without checking `(!config?.access || Boolean(config.access.full))`. | Added `(!config?.access || Boolean(config.access.full))` guard matching `canAcceptFindings`. | `Actions.test.jsx` (`ui`) |
| 4 | Reconcile resurrecting accepted security findings | After accepting `security-review` findings, the next reconcile tick resurrected a `security-findings` hold when another review (e.g., `every-pr` or `payments`) had left findings and written `LinkAutoMergeAwaitsPerson` (`"review <id> left findings"`). | `SyncHumanReviewHolds` in `pkg/orchestrator/humanreviewhold.go` matched any `"review <id> left findings"` mark without checking that `<id>` equalled `LinkSecurityReviewTask`. | Required `m[1] == secReview` (`linkTarget(*task, model.LinkSecurityReviewTask)`) before backfilling `HumanReviewSecurityFindings`. | `TestSyncHumanReviewHoldsIgnoresNonSecurityReviewAwaitMark` (`pkg/orchestrator`) |
| 5 | Cross-review hold hijack & question wipe | When a non-security review finished with findings while `LinkSecurityHold` was active, `dropSecurityHoldOnFindings` misattributed those findings to `GateSecurityReview` and raised `HumanReviewSecurityFindings`; conversely, a security review finishing while `LinkPathTriggerHold` was active polluted `GateReview`; and `settleSecurityHold` wiped other reviews' `LinkReviewQuestions`. | `dropSecurityHoldOnFindings` did not verify the finishing review was `LinkSecurityReviewTask`; `dropPathTriggerHoldOnFindings` did not exclude `LinkSecurityReviewTask`; `settleSecurityHold` cleared `LinkReviewQuestions` unconditionally. | Guarded `dropSecurityHoldOnFindings` and `settleSecurityHold` in `pkg/orchestrator/securityhold.go` and `dropPathTriggerHoldOnFindings` in `pkg/orchestrator/pathtriggerhold.go` by review ownership. | `TestNonSecurityReviewFindingsDoNotHijackSecurityHold` (`pkg/orchestrator`) |
| 6 | `/next/` Changes tab sends branch name as `head` | Submitting `Accept findings…` (or `Force merge…`) from `/next/task/<id>?tab=changes` failed HTTP 400 (`head must be a commit hash`) on every real PR. | `task-changes-tab.component.ts` read `this.preview()?.head` (which `GET /api/tasks/{id}/diff-preview` populates with the branch ref name, e.g. `grain/task-2`) instead of `this.task().ci?.commit`. | Added `commitHead()` preferring `this.task().ci?.commit` and only falling back to `preview.head` when it matches a 7–40 hex SHA, used by both `emitAcceptFindings` and `emitForceMerge`. | `task-page.component.spec.ts`, `ui-ng/e2e/next-accept-findings.spec.js` |
| 7 | CI job declaration acceptance never unblocks subsequent runs | Accepting a held CI job declaration (`repo#credential` or widened `repo#write`) never unblocked the next run because `/api/declarations` recorded ephemeral per-run sandbox names (`ci:acme/widgets:c<run>-0`) instead of the workflow's stable job ID (`jobs.<id>`). | `grit/steps.SandboxRequest` only carried `Name` (ephemeral sandbox name), not `Job` (`pj.Job.ID`), so `ciDeclarationRefusal` in `pkg/daemon/ci_policy.go` keyed `CIJobDeclarationID(repo, req.Name)`. | Added `Job string` to `steps.SandboxRequest`, populated `Job: pj.Job.ID` in `pkg/grit/engine/engine.go`, and used `req.Job` (falling back to `req.Name`) in `pkg/daemon/ci_policy.go`. | `TestACIJobsDeclarationKeyedByStableJobIDAcrossRuns` (`pkg/daemon`), `TestSandboxRequestCarriesStableJobID` (`pkg/ci`) |
| 8 | Mechanical port overwrites dropped-grants note | When a port from a template with a template-restricted grant hit a mechanical cherry-pick conflict, the port's body lost the `Dropped template-restricted grants` / `settings/capabilities.yml` note. | `portMechanically` in `pkg/orchestrator/port.go` overwrote `port.Body` via `portBody(...)` without forwarding `droppedGrants` from `newPortTask`. | Added `newPortTaskWithDropped` and threaded `droppedGrants` through `filePorts` -> `portMechanically` -> `portBody`. | `TestAMechanicalPortKeepsDroppedGrantsNoteInBody` (`pkg/orchestrator`) |

## Visual verification (BEFORE & AFTER screenshots)

Captured 12 BEFORE (`a771a9973`) and 12 AFTER screenshots via Playwright at both `1440x900` and `390x844` in light and dark themes (with zero `pageerror` events and `horizontalOverflow: false` on every shot):

- `/next/task/<T1>?tab=changes` (full-access `alice@example.com`): `targeted-review` pill now shows `Accept findings…` alongside `security-review` and `review` cleanly without overflow or pill overlap at 1440 px and 390 px.
- `/next/task/<T1>?tab=changes` (repo-scoped writer `bob@example.com`): `Accept findings…` buttons removed on all review gates.
- `/tasks/<T2>` classic detail (repo-scoped writer `bob@example.com`): `Release hold` button removed on mixed hold while hold reasons remain clearly visible.

Contact sheets (`before/contact-sheet.png` and `after/contact-sheet.png`) are attached to [Task #6476](/tasks/6476).
