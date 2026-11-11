# 2026-11-11 walkthrough: reviews as a separate merge gate, one review per PR head (§4)

Plan: [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §4, which covers PRs #1073, #1129 and #1183.
Task: 4517, filed by the nightly test-plan task 4297. Branch: `grain/098073e4/task-4517-walkthrough-reviews-as-a`.
Helpers: [`2026-11-11-merge-gates/`](2026-11-11-merge-gates/). `run.sh` reruns everything in this document.

## Outcome

| § | Item | Outcome |
|---|------|---------|
| 4.1 | A real PR through every gate | **Held after fixes.** Five bugs were found. F2 and F4 were found here, and F2 was the serious one: a trigger review's findings were reported as the security review's. |
| 4.2 | Overrides | **Held after fixes.** Every refusal held. A valid partial override skipped only its named gate, but only after F5 was fixed. Before the fix, an override that did not name `ci` merged over checks that were running or failing. A stale override from an earlier head also stood at the next head (F1). |
| 4.3 | Reviews never open PRs | **Held, with one difference from the plan's wording.** A findings-only review that pushes anyway has its push *accepted onto its own branch and ignored*: no PR is opened and the run is told so. The push is not refused. grain's security review fixes in place (`pushes-reviewed-branch`, branch = the change's own). |
| 4.4 | Post-merge reviews | **Held.** A trigger review still running at a partial-override merge continued against `main` and filed a follow-up on `main` carrying its finding. |
| 4.5 | UI | **Held.** The task pane, the PR page and Inbox rows were checked at 1440×900 and 390×844, light and dark, in the pending, passed, findings and overridden states. There was no overflow and every gate box was inside the viewport. The override dialog keeps Confirm disabled until a reason *and* the right PR number are given. The list rows' CI gate was wrong and is fixed (F3). |

Fixed in this branch (each fix has a regression test that fails without it):

| # | Finding | Fix | Regression test |
|---|---------|-----|-----------------|
| F1 | When the merge queue withdrew a force-merge stamp because the branch moved, the `LinkGateOverride` record stayed standing. The gates kept showing "overridden" at a head nobody had overridden. A second force request at the new head was checked against those stale gates, so it could name **no** review gate and still be accepted, with an audit comment and event claiming an override. The queue's own recheck kept it from merging. | `GateOverrideRecord.WithdrawnAt`: `dropMovedForcedMerge` marks the record withdrawn, which keeps it as audit. `LatestGateOverride` ignores a withdrawn record. `ForceMergeWithGates` resolves the head first and ignores an override given for another head. | `pkg/ui` `TestAGateOverrideForAnEarlierHeadSkipsNothingAtTheNewHead`; withdrawal asserts in `pkg/orchestrator` `TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride` |
| F2 | A gating trigger review's findings on a change that was also on a security hold were booked to the **security-review** gate. `dropSecurityHoldOnFindings` dropped the security hold, wrote a `security-review` findings record naming the trigger review, raised a `security-findings` human-review hold and told the PR "its security-review gate did not pass". The pane read *Security review: findings* even though the security review had passed or was still running. The stray record outlived the trigger's own clean re-review. | Another review's findings stay on that review's gate (`reviewGateOf`). The security hold and the security review's verdict are left alone. The findings are recorded first, so a clean security review still cannot restore auto-merge over them. | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111` (step 3) |
| F3 | Inbox and task-list rows computed merge gates without grain CI's status. The CI gate read "running (waiting for checks)" while the task page said passed or failing. | `Store.CIStatuses` is one query over open tasks. `taskFrom` takes the CI status. | `pkg/ui` `TestAListRowsCIGateMatchesTheTaskPage` |
| F4 | The Code-review gate folds every trigger review (one per template) into one gate but named only the first one filed. With two running it said "running (task #2)". After #2 finished it still pointed at #2 while #3 held the gate. | `MergeGate.Tasks` lists every review behind the gate, and the React UI already renders `tasks`. The running detail names the reviews still running ("running (tasks #2, #3)", then "running (task #3)"). | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111` (steps 2–3) |
| F5 | A forced merge skipped CI whatever its override named. `--override-gates=review` merged with GitHub checks running **or failing**, because grain's CI gate had not observed the failure yet. This contradicts "a forced merge skips only the gates it names". | `forcedMerge.keepsCI` is set when the override recorded for the forced head names gates but not `ci`. The forced arm then also needs a green head and says why it waits. A plain force merge records `ci` and is unchanged. | `pkg/orchestrator` `TestMergeGatesWalkthrough20261111AnOverrideNotNamingCIWaitsForChecks` |

Filed as follow-ups (not fixed here):

- **F6:** a new head refiles an incremental re-review for **every** review kind on the PR, including a kind whose trigger paths the delta does not touch. In the walkthrough the planner (`docs/**`) re-review was filed for a delta touching only `config/secrets/prod.yml`. This costs a review run per kind per push. Whether to skip it is a policy choice.
- **F7:** the trigger comment says a review must finish "without commits of its own; otherwise it waits for a human's Submit". A findings-only review's commits are ignored and its gate passes. Either the note or the behaviour should change, and the plan's "the review run's push is refused" should say what really happens.

## Environment

- Sandbox VM (Debian), Go from `go.mod`, Node and npm. `ui/` was built with `npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` from this branch's `HEAD`. The binary was rebuilt after the fixes for the final UI and HTTP runs.
- **Orchestrator flows (items 1–4)** ran in-process as deterministic Go tests in `pkg/orchestrator`. They used the real `model.Store` (SQLite), the **GitHub simulator** (`githubsim`) with real git pushes to its bare repo, and `.grain/triggers/secrets.yml` / `.grain/triggers/plans.yml` declared on the simulated repo's `main`. The triggers name two review templates, `sensitive` and `planner`. The change touches `pkg/gitproxy/proxy.go` (a trust-boundary path), `config/secrets/prod.yml` and `docs/plan.md`. The code under test is the real `ProcessResult`, `JudgePullRequestTriggers`, `SyncSecurityHolds`, `SyncPathTriggerHolds`, `SyncPullRequests` and `SyncReviews`. Submit and every override go through the real `ui.Client`: `SubmitPullRequest` and `ForceMergeWithGates`.
- **What was stubbed: the model provider, and only that.** The review runs are a scripted agent. It hands `ProcessResult` an `agent.Result` carrying `add_review_comment` findings (or none) and, for item 3, a commit pushed to the review's own branch. The sandbox has no model credential, so a `grain daemon` could not dispatch a real agent into a sandbox. The daemon's own reconcile loop was therefore driven in-process, as earlier walkthroughs did (`2026-11-01-security-fixes-live.md`).
- **HTTP and UI (items 2 and 5)** used a real server, `grain demo -github-sim -fake-ci -review-questions -merge-queue-states -human-review -addr 127.0.0.1:4291`. Its "Publish both daemon images" task (`acme/widgets#106`) holds a code-review gate with findings. `http-overrides.sh` sends what `grain pr submit` sends. The CLI's own `grain pr submit` is shadowed inside a task sandbox by the in-sandbox `grain pr`. `gates-ui.mjs` (Playwright, Chromium) lays the pending, passed and overridden states over that task's real answer with `page.route`, the same way `screenshot.mjs`'s `MERGE=` does.
- No `grain daemon -sandbox-port-base=0` process was used for these items. Every flow it would have run is the reconcile code above, and without a model credential it would have dispatched nothing.

## Steps and observations

### 4.1: a real PR through every gate (`TestMergeGatesWalkthrough20261111`)

1. Finishing the change filed **exactly one review per kind**: the security review (task 1, `pushes-reviewed-branch`), one `planner` review (task 2) and one `sensitive` review (task 3). Both trigger reviews are `review-comments-only`, and `OpenPullRequestForTask` refuses each.
2. **Re-judging the same head** (`JudgePullRequestTriggers` again) filed nothing.
3. **Submit before the reviews finish** was given with `SubmitPullRequest`, before any review had finished. It was recorded (`approval=passed (given by alice@example.com)`) and `SyncPullRequests` did not merge. Gates: `ci=running; security-review=running (task #1); review=running (tasks #2, #3)`. Before F4 this read `review=running (task #2)`.
4. **A finding blocks the PR after Submit.** The security and planner reviews passed. The `sensitive` review left `plaintext token`. The PR stayed open with `security-review=passed; review=findings (plaintext token)`. **Before F2** it read `security-review=findings (plaintext token)` naming task 1, plus a security-findings human hold.
5. **New head.** A push to `config/secrets/prod.yml` filed incremental re-reviews (tasks 4 and 5, `path-trigger-since:<old head>`) **deferred by the 3-minute debounce** (`DeferredUntil` = judge time + 3m). Their body is "Review only what changed since … touches `config/secrets/prod.yml`". Re-judging the new head filed nothing more. The planner re-review was also filed (F6).
6. Both re-reviews passed clean. **The PR merged by itself** on the next cycle with no second Submit: `ci=passed; security-review=passed; review=passed; approval=passed`. No new security review was needed because the new commit does not touch the trust boundary, so the `trust-boundary-cleared` clearance carried.

### 4.2: overrides (`TestMergeGatesWalkthrough20261111Overrides`, `http-overrides.sh`)

The change had a security review and a trigger review running. In-process via `ui.Client`, and over HTTP against the demo:

| Request | Result |
|---|---|
| `overrideGates: [security-review]` while the review gate is unsatisfied | 400: `unsatisfied review gate(s): review (running) … pass --override-gates=security-review,review` |
| reason `"   "` (both `ForceMergeWithGates` and `POST /api/tasks/{id}/force-merge`) | 400: `forcing a merge needs a reason` |
| `pr submit` override without a reason | refused (same) |
| reason without gates | 400: `--reason requires --override-gates naming each skipped gate explicitly` |
| naming `approval`, or an unknown gate | 400: `unknown gate "approval": name each skipped gate explicitly (ci, security-review, review)` |
| an agent principal, both Submit and override | `only a human may submit a pull request or override merge gates` |
| a PR no task owns | 404 |
| a limited role | refused 403 by the route gate (`agent_api_gate.go`); covered by `tests/security` `TestTaskGrantEscalation*` Path 5, which passed on this branch |

No refused request left an override record. A **valid partial override** (`[review]`, with reason and head, after the security review passed) left `security-review=passed`, not overridden, and `review=overridden (overridden by alice@example.com: …)`. It merged on the next cycle. The audit row (`ForceMergeRecords`) and the override link record who did it and why. Over HTTP the same override returned 200 with `overridden: ["review"]`.

**F5:** with checks `in_progress` or `failure`, the same `[review]` override merged at once before the fix. After it, the merge waits ("the override from alice did not name ci, so it still waits for this change's own checks to pass") and merges once the checks pass. `[review, ci]` merges over the failure.

**F1:** override `[security-review]` at head A, push head B, then override at B with no gates. The request was accepted before the fix because the security gate still read "overridden" from A. Now it is refused, the A record stays as a withdrawn audit entry, and an override at B naming the gate stands.

### 4.3: reviews never open PRs (`TestMergeGatesWalkthrough20261111ReviewsOpenNoPullRequests`)

A `sensitive` (findings-only) review pushed a commit to `grain/task-2` and finished. No PR was opened: the simulator's PR count was unchanged and there was no `fixes` link. The review's thread says "Branch grain/task-2 was pushed, but this review was filed to post findings, not to fix them, so no pull request was opened for it." The gate then read `review=passed` (F7). The security review's links are `pushes-reviewed-branch=t1` and its branch is `grain/task-t1`: it fixes in place.

### 4.4: post-merge reviews

After the partial override merged, `SyncReviews` kept the trigger review open, retargeted at `main` with `post-merge-review-commit=<merged sha>`. Its finding ("token logged in plain text") filed follow-up task 3, "Fix post-merge review findings from task 2 on acme/widgets#9000", based on `main` with the finding in its body. The security-review half of this item is covered by `TestOverrideWithReasonMergesIsAuditedAndContinuesSecurityReviewPostMerge`.

### 4.5: UI (`gates-ui.mjs`)

48 shots: {pending, passed, findings, overridden} × {task pane, PR page `/repos/acme/widgets/compare/main...grain/task-22`, Inbox} × {1440×900, 390×844} × {light, dark}. All 48 had the gate block visible, `scrollWidth − clientWidth = 0`, and the block inside the viewport. Mean luminance confirms the themes applied (light ≈ 0.89–0.95, dark ≈ 0.12–0.20). Rendered text, desktop light:

- pending: `CI … Security review: running task 31 Code review: running task 32, task 33 Approval: given by alice@example.com`. Two trigger reviews are listed through the new `tasks` (F4).
- passed: `CI ✓ Security review: passed … Code review: passed …`
- findings: `CI … Code review: findings task 23`
- overridden: `… Code review: overridden task 32 …` plus the banner `Gate override by alice@example.com (review): release blocker…` on the pane and PR page, and `Overridden by alice@example.com (review): …` on the Inbox row.

Override dialog, at both widths: Confirm is disabled when the dialog opens, still disabled with a reason only, still disabled with the wrong PR number (#107), and enabled with the right PR number (#106).

No `ui/` file changed. The sandbox's file reader shows PNGs as bytes, so the screenshots were checked by the geometry and luminance assertions above, not by eye. The contact sheet is attached to the task's closing note.

## Also observed

- `tests/security` `TestRatchetRules/git-argv-separator` fails on `pkg/staterepo/settingshistory.go:424`, a file this branch does not touch. It came in with `main` and is reported, not fixed here.
- The refusal hint for a missing gate suggests `--override-gates=security-review,review`, listing every unsatisfied review gate. That is correct but reads as if both were required even when one is already named.
