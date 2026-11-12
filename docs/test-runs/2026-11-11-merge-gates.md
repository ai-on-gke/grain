# 2026-11-11 walkthrough: reviews as a separate merge gate, one review per PR head (§4)

Plan: [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §4, covering PRs #1073, #1129 and #1183.
Task: [Task #4517](/tasks/4517), filed by the nightly test-plan task ([Task #4297](/tasks/4297)). Branch: `grain/098073e4/task-4517-walkthrough-reviews-as-a` (PR #1274).
Helpers: [`2026-11-11-merge-gates/`](2026-11-11-merge-gates/). `run.sh` reruns everything in this document.

## Outcome

| § | Item | Outcome |
|---|------|---------|
| 4.1 | A real PR through every gate | **Held after fixes (SQLite + PostgreSQL live daemon, plus deterministic unit suite).** Three triggers across two templates (`secrets` + `secrets-audit` → `sensitive`; `plans` → `planner`) plus a trust-boundary path (`pkg/gitproxy/proxy.go`) filed 1 security review and 2 trigger reviews (one per kind). `grain pr submit` before the reviews finished recorded human approval without cancelling any review; once reviews and CI passed, the PR merged automatically; a finding kept the PR open after Submit; a new head filed an incremental re-review deferred ~3 minutes (debounce); re-judging the same head filed nothing. Two bugs found and fixed here (F2, F4). |
| 4.2 | Overrides | **Held after fixes.** Refusals verified via `grain pr submit` and HTTP on the live daemon: missing gate (`rc=2` / 400), empty reason (`rc=2` / 400), `--reason` without `--override-gates` (400), `approval` or unknown gate (400), verified IAP limited role `grain/task-admin` (403 on both `/api/pull-requests/submit` and `/api/tasks/{id}/force-merge`), and external agent token `gma_…` (403 on both). A valid partial override (`--override-gates=review`) skipped only `review` (`security-review=passed`, `ci=passed`), merged the PR, and audited who/why. Two bugs found and fixed here (F1, F5). |
| 4.3 | Reviews never open PRs | **Held after fix (F6).** Before F6, `model.Store.GitScope` and `GitPushBranch` still granted a findings-only trigger review (`LinkReviewCommentsOnly`) push access to its target repository and `BuildPrompt` appended `git push origin` instructions; `finish.go` ignored the pushed branch without opening a PR, but the push itself was not refused. After F6, the live daemon's `gitproxy` refuses the review run's `git push` at the receive-pack advertisement with HTTP 403 (`acme/widgets is read-only for this sandbox: it may fetch it but not push to it`), `grain pr open` exits 1, no extra PR appears, and grain's security review (`pushes-reviewed-branch`) commits directly on the change's own branch (`exit=0`). |
| 4.4 | Post-merge reviews | **Held.** On both SQLite and PostgreSQL daemons, a `sensitive` trigger review still running when its PR merged under a partial `--override-gates=review` override continued post-merge (`post-merge-review-commit`) and, on reporting `"post-merge leaked token"`, filed a queued follow-up fix task targeting `main` with the finding in its body. |
| 4.5 | UI | **Held after fix (F3).** Task pane, PR comparison page (`/repos/acme/widgets/compare/main...grain/task-22`) and Inbox rows rendered at 1440×900 and 390×844 in light and dark across `pending`, `passed`, `findings` and `overridden` states (48 screenshots, `scrollWidth − clientWidth = 0`, every gate box inside the viewport). The override dialog keeps Confirm disabled until both a non-whitespace reason and the exact PR number are entered. List rows' CI gate was missing grain CI's verdict and is fixed (F3). |

## Bugs found and fixed in this branch

Every fix has a deterministic regression test that fails without it:

| # | Finding | Fix | Regression test |
|---|---------|-----|-----------------|
| **F1** | When the merge queue withdrew a force-merge stamp because the branch moved, `LinkGateOverride` stayed standing. Gates kept showing `overridden` at the new head, and a second force request at the new head was checked against those stale gates (accepting a request naming no review gate and recording an override comment/event, though `forceOverridesGates` blocked the merge itself). | `GateOverrideRecord.WithdrawnAt`: `dropMovedForcedMerge` marks the standing override withdrawn (preserving the audit record), `LatestGateOverride` ignores a withdrawn one, and `ForceMergeWithGates` ignores any override recorded for a different head. | `pkg/ui` `TestAGateOverrideForAnEarlierHeadSkipsNothingAtTheNewHead`; `pkg/orchestrator` `TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride` |
| **F2** | When a gating path-trigger review left findings on a change that also had a trust-boundary security hold, `dropSecurityHoldOnFindings` booked the findings to `security-review`, dropped the security hold, raised a `security-findings` human hold and commented that `security-review` failed — even when the security review itself passed or was still running, and the stray record outlived a clean trigger re-review. | Non-security reviews record findings on their own gate (`reviewGateOf`) while leaving `LinkSecurityHold` and the security review's verdict intact; `HasReviewFindings` still prevents a clean security review from restoring auto-merge over them. | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111` (step 3) |
| **F3** | `ListTasks` computed each row's merge gates (Inbox chips, task list) without `obs.CI`, so a row's CI gate stayed `running (waiting for checks)` after grain CI passed or failed on the task page. | `Store.CIStatuses` reads open tasks' CI statuses in one query (SQLite and PostgreSQL); `taskFrom` passes `CI` into `ComputeMergeGates`. | `pkg/ui` `TestAListRowsCIGateMatchesTheTaskPage` |
| **F4** | `ComputeMergeGates` folded multiple gating trigger reviews into `Code review` using only the first task ID: with tasks #4 and #5 running it said `running (task #4)` and linked only #4, and after #4 finished it still pointed at #4 while #5 held the gate. | `MergeGate.Tasks` lists every review task behind the gate (rendered by `MergeGatesList.jsx`), and a running gate names the reviews still running (`running (tasks #4, #5)`, then `running (task #5)`). | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111` (steps 2–3) |
| **F5** | A forced merge skipped CI regardless of what `overrideGates` named: `--override-gates=review` merged immediately even when checks were `in_progress` or `failure`, contradicting "a forced merge skips only the gates it names". | `forcedMerge.keepsCI` is true when the standing `GateOverrideRecord` for `headSHA` names gates without `ci`; `mayMerge`'s forced arm then requires `greenHead` and explains why it waits. Plain `ForceMerge` records `ci` and is unchanged. | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111AnOverrideNotNamingCIWaitsForChecks` |
| **F6** | A findings-only trigger review (`LinkReviewCommentsOnly`) still received target write scope in `Store.GitScope` and a push branch in `Store.GitPushBranch` (so `gitproxy` allowed `git push` onto `grain/…/task-N`), and `orchestrator.BuildPrompt` still appended `Push your change to a new branch … git push origin …` right below `Do not commit, push or open a pull request`. | `Store.GitScope` moves `target` into `reads` for `LinkReviewCommentsOnly` tasks (while `SandboxNamespaceScope` preserves `TaskRepo`), `Store.GitPushBranch` returns `("", false, nil)`, so `gitproxy` refuses `git push` at the advertisement with HTTP 403 `ReadOnlyPushReason`; `BuildPrompt` omits push instructions for `reviewCommentsOnly` tasks. | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111ReviewsOpenNoPullRequests`; live daemon check in `live-daemon.sh` |

## Follow-up proposed

- **F7 (proposed via `propose_task`):** pushing a new head re-files an incremental, debounced re-review for **every** review kind matching the PR's whole diff, even when the delta since that kind's last clean review touches none of its trigger paths (e.g. `planner` watching `docs/**` is re-filed for a delta touching only `config/secrets/prod.yml`). Whether an untouched kind should carry its clean verdict forward is a policy choice interacting with `human: required` triggers and base-rule updates.

## Environment

- **Build:** Debian sandbox VM (`linux/amd64`), `ui/` built via `npm ci && npm run build`, `/home/debian/bin/grain` built from `HEAD` (`./cmd/grain`).
- **Real daemon (`live-daemon.sh`):** `grain daemon -sandbox-port-base=0 -host-sandboxes -poll-interval 1s` run end-to-end twice — once on embedded **SQLite** and once on **PostgreSQL 15** (`-db-driver postgres`, local `15/main` cluster) — backed by:
  - `ghsim.go` serving `pkg/github/githubsim` REST endpoints + `git http-backend` for bare repository `acme/widgets.git` (`-github-host 127.0.0.1:4284 -github-insecure-http`);
  - `.grain/triggers/{secrets,secrets-audit,plans}.yml`, `.grain/templates/{sensitive,planner}.yml`, and `.grain/workflows/ci.yml` (real grain CI running in host sandboxes);
  - `iap_keys.go` serving ES256 IAP public keys (`-ui-iap-keys-url`) to mint real `X-Goog-IAP-JWT-Assertion` tokens for limited-role RBAC checks;
  - `fake-agy.sh` + `hook_merge_gates.py` configured as the Antigravity CLI backend, driving each run's real `grain mcpserver` over stdio (`git push` through the daemon's real `gitproxy`, `grain pr review-comment`, `grain pr open`). **Only the model provider is stubbed.**
- **Deterministic Go tests:** `pkg/orchestrator/mergegates_walkthrough_20261111_test.go` and `pkg/ui/gate_override_head_test.go`.
- **Browser (`gates-ui.mjs`):** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` and `390×844` in `light` and `dark` over `grain demo -github-sim -fake-ci -review-questions -merge-queue-states -human-review`.

## Steps and live observations

### 4.1: a real PR through every gate (`live-daemon.sh` PR #9000 & #9002, `TestMergeGatesWalkthrough20261111`)

1. Task `T1` pushed `pkg/gitproxy/proxy.go` (trust boundary), `config/secrets/prod.yml` (`secrets` + `secrets-audit` triggers, both template `sensitive`) and `docs/plan.md` (`plans` trigger, template `planner`), opening `acme/widgets#9000`.
2. The daemon filed **1 security review** (`#3`) and **2 distinct trigger reviews** (`#4` for `plans`, `#5` for `secrets` + `secrets-audit`). Reconciling the same head across subsequent ticks filed nothing (`distinctTriggerReviews` stayed `4,5`).
3. While all three reviews were held mid-run, `grain pr submit acme/widgets#9000 --json` recorded human approval (`approved: true`) without cancelling any review:
   `ci=running; security-review=running (running (task #3)); review=running (running (tasks #4, #5)); approval=passed`.
   `grain merge-queue` showed the merge queue idle (not merging prematurely).
4. Releasing the reviews: `#3` (security) and `#4` (`planner`) passed clean; `#5` (`sensitive`) posted `"plaintext token"` on `config/secrets/prod.yml:1`. After grain CI passed, `acme/widgets#9000` stayed **open** (`merged=false`) with `ci=passed; security-review=passed (passed (task #3)); review=findings (plaintext token); approval=passed`.
5. Pushing a fix to `config/secrets/prod.yml` on `T1`'s branch filed incremental re-reviews (`#6, #7`) in state `deferred` with `deferredUntil` ~3 minutes ahead and `"This is an incremental review."` in their body. Re-polling the new head filed nothing more.
6. On `acme/widgets#9002` (`T3`), `grain pr submit` was recorded while security review `#13` was running; once `#13` and grain CI passed, `acme/widgets#9002` **merged automatically** (`merged=true`) without a second Submit.

### 4.2: overrides (`live-daemon.sh` PR #9000 & #9001, `http-overrides.sh`, `TestMergeGatesWalkthrough20261111Overrides`)

On the live daemon against `acme/widgets#9000`:
- `grain pr submit acme/widgets#9000 --override-gates=security-review --reason="outage"` → exit 2: `2 has unsatisfied review gate(s): review (running). Review gates are never skipped implicitly; pass --override-gates=security-review,review with a reason (requires merge:force) to override them`.
- `grain pr submit acme/widgets#9000 --override-gates=security-review,review --reason="   "` → exit 2: `-override-gates requires -reason explaining why the gate is skipped` (and HTTP 400 `forcing a merge needs a reason` on `/api/tasks/{id}/force-merge`).
- Verified IAP user `bob@example.com` with limited role `grain/task-admin`: HTTP **403** on both `POST /api/pull-requests/submit` (`cannot submit work (autoMerge): letting a change merge unreviewed requires an unlimited role (grain/operator)`) and `POST /api/tasks/2/force-merge` (`holds no scope merge:force`).
- External agent token `gma_…` (`tasks:read`, `tasks:write`): HTTP **403** on both `POST /api/pull-requests/submit` (`an agent cannot turn on auto-merge or submit work`) and `POST /api/tasks/2/force-merge` (`holds no api grant for scope merge:force`).
- `http-overrides.sh` additionally verified HTTP 400 on `--reason` without `overrideGates`, HTTP 400 on `overrideGates: ["review","approval"]`, and HTTP 404 on an unowned PR.

On `acme/widgets#9001` (`T2`), once `ci=passed` and `security-review=passed` while trigger review `#10` was still running, `grain pr submit acme/widgets#9001 --override-gates=review --reason="emergency mitigation"` left `security-review=passed`, marked only `review=overridden`, merged `acme/widgets#9001` (`merged=true`), and recorded `gates: ["review"]`, `by`, `reason`, `skipped` and `headSha` on `gateOverride` and `force_merge`.

### 4.3: reviews never open PRs (`live-daemon.sh` task #5 & #13)

- Inside live host sandbox `#5` (`sensitive` trigger review, `LinkReviewCommentsOnly`), `git push origin HEAD` through the daemon's `gitproxy` exited 128 with HTTP **403**: `remote: acme/widgets is read-only for this sandbox: it may fetch it but not push to it`. `grain pr open` exited 1 (`task 5 is a review filed to post findings on the pull request it reviews, not to fix them`). Total PRs on `ghsim` stayed 1, and `prompt_has_push_instruction` was `false`.
- Inside live host sandbox `#13` (grain's security review on `acme/widgets#9002`), `git symbolic-ref --short HEAD` was `T3`'s own branch (`grain/…/task-12-security-review-fixes-in-place`), and committing + pushing `notes/sec-fix.txt` to `origin HEAD` succeeded (`exit=0`) on the change's own branch with no second PR opened.

### 4.4: post-merge reviews (`live-daemon.sh` task #10 → #11)

After `acme/widgets#9001` merged under `--override-gates=review`, still-running trigger review `#10` was kept open (`post-merge-review-commit`), posted `"post-merge leaked token"`, and completed. The daemon filed follow-up task `#11` (`"Fix post-merge review findings from task 10 on acme/widgets#9001"`, `state: "queued"`, `base: "main"`) carrying `"post-merge leaked token"` in its description, identically on SQLite and PostgreSQL.

### 4.5: UI (`gates-ui.mjs`)

48 screenshots across `{pending, passed, findings, overridden} × {task pane, PR page, Inbox} × {1440×900, 390×844} × {light, dark}`: all 48 rendered visible with `overflow = 0` and bounding boxes inside the viewport. On `pending`, `Code review: running` displays `task 32, task 33` via `MergeGate.Tasks` (F4). On the override dialog (`GateOverrideDialog`), `confirm-gate-override` stayed disabled when opened, with reason only, and with wrong PR number (`107`), enabling only when both reason and exact PR number (`106`) were filled at both desktop and phone widths. No `ui/` source file changed; `contact-sheet.png` is attached to the closing note.

## Inherited `main` CI failures encountered and resolved

Attempt 1 pushed at base `2ab9a53ad`, where four checks were already red across `main` and sibling PRs:
1. `go model`: `tests/security` `TestRatchetRules/git-argv-separator` (`pkg/staterepo/settingshistory.go:424`) and `pkg/gitproxy` `TestDefaultCIReadScopeReadsItsOwnGitHubActionsCI`.
2. `go unit`: `pkg/ci` `TestOnlyBuildersNameTheCIService` (`ci_gocache.go`).
3. `go e2e`: `tests/deploy` `TestTheGoJobsCoverEveryPackageInTheModuleOnce` (`pkg/infrasig`).
4. `ui e2e`: `api-scope-set-edit.spec.js`, `capability-policy-pickers.spec.js` and `session-pending-grants.spec.js` expecting pre-#4366 `api:` grant labels.

All four were fixed on `origin/main` in PR #1273 (`a0953e874`); merging `origin/main` into this branch brought in those fixes cleanly.
