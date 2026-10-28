# Test run: merge_on_green, base-repair human-gate bypass, Submit head-pinning (2026-10-25 plan §3)

- **Plan:** `docs/test-plans/2026-10-25.md` §3
- **Run by:** task 929, 2026-10-26 UTC
- **Branch under test:** `candidate` at `6d8f57485` (task branch `grain/049f1e09/task-929`)
- **Main-only items inspected on:** `origin/main` at `76b8d773f`, in a side worktree. `main` was not merged into this branch.
- **Environment:** grain sandbox VM (Debian bookworm), Go and Node from the image, Playwright Chromium from `/var/cache/playwright`. Browser steps ran over `grain demo` through `ui/e2e/fixtures.js`, using the `grain_e2e` build that `ui/e2e/global-setup.js` makes. Screenshots came from `make ui-screenshots` (`LOOK=1` was not available: the sandbox has no Gemini key).

## Summary

| # | Step | Outcome |
|---|------|---------|
| 1 | Repository switches in a real browser (toggle, PUT, reload) | **Pass**, with a test bug fixed (below) |
| 2 | Per-branch overrides of either switch | **Not applicable**: the feature has none (plan inaccuracy) |
| 3 | `/merge-queue` page | **Not applicable**: no such route; the merge queue is the repository tab |
| 4a | `FixesBaseBranch` repair, red base, bypass on, ordinary files | **Pass**: merges on green, nothing to bypass, no review filed (new test) |
| 4b | Same repair touching a `pkg/trustboundary` path | **Behaves as designed, but the plan's expectation is wrong**: the bypass does lift the trust-boundary hold; a security review is still filed |
| 4c | Human `Submit` head-pinning (main only) | **Finding**: a push landing between Submit and the next sync rides the Submit unreviewed |
| 5 | Security-hold deduplication | Already on `candidate` as `c76a030f7` (backport of `10a2bb336`); the plan mislabels it as `eb8827a47` |

## 1. Repository switches in the browser

There are two places to set them:

- `/repos/acme/widgets/settings/merge-queue`, the repository page's Merge queue tab. It has three ticks: *Require a branch to contain its base*, *Merge when green and clean* (`PUT /api/repos/acme/widgets/merge-on-green`, body `{"mergeOnGreen": bool}`), and *Base-branch repair PRs may merge without a human when their CI is green* (`PUT …/base-repair-bypasses-human-gates`, body `{"baseRepairBypassesHumanGates": bool}`).
- `/settings/repositories` carries only the bypass tick, one per repository, under "Base-branch repair human-gate bypass". It has no merge-on-green tick.

New spec `ui/e2e/merge-on-green-base-repair-walkthrough.spec.js`:

- From the repository tab it turns both on and checks each PUT's request body and answer. It checks that turning the bypass on leaves `mergeOnGreen` true, so neither write clobbers the other. After a reload both are still ticked. The Overview shows its "Merge when green" and "Base repair bypass" rows, and Settings > Repositories shows the bypass ticked. Turning merge-on-green back off leaves the bypass on, and that survives a reload too.
- From Settings > Repositories it turns the bypass on, reloads, and reads `GET …/base-repair-bypasses-human-gates`. The repository tab then shows the bypass on and merge-on-green off. Turning it off survives a reload.
- Both tests reset the repository row in a `finally` block.

Result: 15/15 passed with `--repeat-each=3`, together with `merge-on-green.spec.js`.

### Bug fixed: `merge-on-green.spec.js` race

Before this change, `turning merge-on-green on saves it and shows it on the overview` failed on every run in this sandbox with `locator.check: Clicking the checkbox did not change its state`.

Reproduction: `npx playwright test e2e/merge-on-green.spec.js --project=chromium --workers=1` on `6d8f57485`.

Cause: the tick is controlled by the server's answer (`setUpToDate(await api(PUT))`). React therefore puts the box back to unticked on the click itself, and only ticks it once the PUT returns. Playwright's `check()` reads the state straight after its click. A request trace showed the PUT returning about 10 ms after the click, so the test passes or fails on timing alone.

Fix: the spec now uses `click()` and then `expect(...).toBeChecked()`. The page's behaviour (the tick showing the stored value) is intended and is left as it is.

## 2–3. Plan inaccuracies on candidate

- **No per-branch overrides.** The plan says `MergeOnGreen *bool` and `BaseRepairBypassesHumanGates *bool` live on repository and branch configs in `pkg/config/repos.go`, read through `MergeOnGreenForBranch` and `BaseRepairBypassesHumanGatesForBranch`. None of those exists on `candidate` or on `main`. Both are plain `bool` columns on `repo_config` (`model.RepoConfig`, schema versions 36 and 37), set only through the two PUT routes above. They are not settable from `repos.yaml`. The bypass applies to whichever branch the repair's pull request targets (`resolveTaskBaseBranch`).
- **No `/merge-queue` page.** It falls back to the board at `/`. Per-repository merge-queue settings are on `/repos/<owner>/<name>/settings/merge-queue`, which also lists that repository's merge batches.
- **Files the plan cites.** `pkg/orchestrator/base_repair_bypass_test.go` is really `baserepairbypass_test.go`. The bypass lives in `baserepairbypass.go`, `securityhold.go` and `pathtriggerhold.go`, not in `merge_readiness.go` or `auto_submit.go`.

Screenshots of the three pages, at desktop (1440×900) and phone (390×844), light and dark, are in the task's closing note (`ui/screenshots/walkthrough/contact-sheet.png`).

## 4. Base-repair bypass and the trust boundary

### What the shipped design does

The plan says the trust-boundary hold is "still enforced (`SecurityHoldReason` is never bypassed)". That is not the design that shipped:

- `holdForTrustBoundary` (`pkg/orchestrator/securityhold.go`) has a `bypass` branch. When `baseRepairBypassActive` holds (option on, task is the branch's repair, base branch red), it records the clearance and `LinkBaseRepairBypass` and keeps auto-merge on. It still files the security review, and posts `securityBypassComment` instead of a hold.
- This covers human-only categories too, such as `.grain/quarantine.yml`. Both settings pages say so in their risk text ("can land … trust-boundary code without human pre-merge sign-off"). The existing test `TestBaseRepairBypassOptionOnMergesOnGreenCIDespiteSensitiveTriggerAndTrustBoundary` asserts it.
- Once the base branch goes green, `revokeExpiredBaseRepairBypass` drops the bypass and the change is held again before it merges (`TestBaseRepairBypassBaseAlreadyGreenOrTurnsGreenBeforeMerge`).
- What decides "is a repair" is `isBaseBranchRepairTask`. A task counts if it is grain's registered repair (`BaseBranchCI.RepairTaskID`), or if it carries the branch's repair tag and was approved by a human. A `fixes_base_branch: true` proposal that was filed already approved (`model.SelfApproved`) does not count. A title alone, or a tag for another branch or repository, does not count either (`TestBaseRepairBypassIgnoresClaimsAnAgentCanMake`).

So "a `FixesBaseBranch` repair touching `pkg/trustboundary` is still held" is false by design. The plan's invariant should be read as "the security review is still filed and the bypass is recorded".

### New regression coverage (`pkg/orchestrator/baserepairbypass_test.go`)

Every existing case pushed both `.grain/workflows/gcp-smoke.yml` and `pkg/ci/basegate.go`, so the two cases the plan asked about were not covered separately. A new helper, `setupBaseRepairBypassEnvTouching`, takes the file list. Two new tests use it:

- `TestBaseRepairOrdinaryFilesMergeOnGreenWithNothingToBypass`: a repair touching `README.md` and `internal/widgets/widgets.go` merges on green with the option off and with it on. No `LinkBaseRepairBypass` is recorded and no security review is filed.
- `TestBaseRepairHumanOnlyTrustBoundaryPath` covers a repair touching only `.grain/quarantine.yml`:
  - With the option off, it is held with a review filed and does not merge.
  - With the option on and the base red, the bypass is recorded, a review is still filed, and the change merges on green.

`go test -race ./pkg/orchestrator -run TestBaseRepair` passes: 9 tests.

## 4c. Human `Submit` head-pinning (on `main` only)

The Submit pinning (`819d821a2`, PR #4641) has not been ported to `candidate`: there is no `LinkHumanSubmitted` or `pinHumanSubmit` there. It was inspected on `origin/main` instead.

The plan's description is inaccurate. There is no `SubmitApprovedHeadSHA`. `ui.Client.Submit` writes a `LinkHumanSubmitted` marker that carries no commit, because the UI cannot read the PR head. On the next `syncEntry`, `pinHumanSubmit` turns the marker into `LinkTrustBoundaryCleared` for whatever head that sync reads.

Two scratch tests ran against `origin/main`. They are reproduced below and were not committed, since this branch does not have the code.

- **Push after the pin: pass.** Submit, then a sync pins the clearance to the shipped head (CI pending), then the agent pushes a new `pkg/ui/server.go` change. The next sync sees the head moved past its clearance: auto-merge is withdrawn, a second security review is filed, and nothing merges.
- **Push before the pin: finding.** Submit, then the agent pushes a new `pkg/ui/server.go` commit before the next sync, then that sync runs. `pinHumanSubmit` pins the clearance to the *new* head (`trust-boundary/v5@<pushed sha>`). The new commit merges with only the first review, which never saw it. The plan's requirement that "a subsequent agent push invalidates the approval instead of riding the earlier human click into the merge queue" therefore fails in this window. The fix needs Submit to record the head the human was shown (the UI's last-synced PR head, or a head the client sends with the Submit), and `pinHumanSubmit` to refuse to pin when the head it reads differs. That fix belongs on `main` and is filed as a follow-up task.

```go
// Run in pkg/orchestrator on origin/main (package orchestrator_test).
func TestScratchPushBeforePinIsPinnedToNewHead(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	humanSubmit(t, ctx, store, task.ID)
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "agent pushed before the pin\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Observed: automerge=true merged=true cleared=trust-boundary/v5@<pushed head> reviews=1
}
```

## 5. Other `main`-only items

- `eb8827a47` (PR #4571) is the release owed-ports work ("promote and cut past a failed back-merge, owing its fixes to main"), not security-hold deduplication. `62ca29585` (PR #4658) keeps guarded-path owed ports in front of a human. Neither is on `candidate`, and both are release-script changes with no walkthrough surface here.
- Security-review deduplication on unchanged heads is on `candidate` as `c76a030f7`, cherry-picked from `10a2bb336`. It is adapted so an existing review is reused only when no base-repair bypass has begun since the head was classified (`securityhold.go`, the `securityReviewReusable` guard).
