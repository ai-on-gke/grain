# Merging: clean and green means merge

**Owner:** @bwsalmon

This design defines when grain merges a pull request into its base branch, what may hold a pull request back, and who decides what happens when a base branch goes red after a merge. It applies to every pull request grain drives to a merge, whatever the repository's `ci_mode`. Who reviews a change is defined by [`contributions.design.md`](contributions.design.md); this document only says that those reviews are gates.

**The core rule:** a pull request merges into branch B as soon as it is

- **clean**: the forge says it merges cleanly into B now;
- **green**: its required checks passed on its own head commit, from grain CI or GitHub Actions according to the repository's `ci_mode`, or an operator has recorded a force merge for that exact head; and
- **through its gates** (below).

**What has landed on B since the pull request was tested does not matter.** There is no overlap re-check, no re-test on top of B's tip, and no merge-queue ref. The trade-off is deliberate: two changes that are each green can be red together, for example when one changes a function's signature and the other adds a caller of the old one. That is caught after the merge by B's post-merge CI and handled by the pipeline steward ([A red base](#a-red-base-the-pipeline-steward-decides)). It is not prevented before the merge.

## No ordering, no queue, no batching

- **No ordering between pull requests.** Each pull request is judged on its own, every cycle. Its state (pending, mergeability unknown, conflicted, failing, in review, awaiting a Submit) holds that pull request and no other. No pull request is "behind" another, and the UI shows each pull request's own blocker rather than a queue position.
- **The only limit is rate.** `max_merges_per_repo` (default 5) caps how many pull requests merge into one branch of one repository per cycle. It throttles and never orders: a pull request the cap does not reach in this cycle is judged again next cycle on its own state, and nothing it is waiting for is another pull request's state. Task priority ([`task-priorities.design.md`](task-priorities.design.md)) decides only which eligible pull requests are visited first within a cycle.
- **Each merge in a cycle sees the branch as it now stands.** Before each merge, "clean" is re-read from the forge against B as earlier merges in the same cycle left it, so the cap never lets a stale "clean" through.

## Gates

A gate holds only the pull request it belongs to.

- **The second-agent review.** Every change has one, and the change does not merge until it has landed or been cleared for the current head.
- **The security review**, for a change that touches a trust-boundary path. A push of new trust-boundary edits after the review needs another.
- **A person's Submit**, where one is required: pull requests against the config repository, changes to `.grain/**`, and tasks without auto-merge.

Every review checks the change against the governing designs and specifications, as [`contributions.design.md`](contributions.design.md#reviews) requires.

## Conflicts, failures and repairs

- **One pull request.** A pull request that is not clean, or not green, gets the normal automatic repair: an agent is sent back to its branch. It gets at most two repair attempts (the second only when the base moved or the failures changed), and then goes to the Inbox as `merge_failed`.
- **Conflict clusters.** A *conflict cluster* is the set of open pull requests into B whose conflicts are in the same files and first appeared after the same landed commit on B. A cluster has **at most one repair in flight**, given to its oldest member. When that repair lands, the others are judged again against the new tip. N conflicting pull requests then cost about N repairs, not N². The steward may step in on a stuck cluster: close a redundant pull request, change priorities, or ask the owners.
- **A check that never reports**, such as a third-party status left pending: the pull request stays unmerged while the check is pending. After a fixed bound, reconcilers raise a stall notification (`pipeline.merge-stalled:<task>`), which wakes the steward to explain it, nudge it or escalate it.

## A red base: the pipeline steward decides

Reconcilers judge each base branch's post-merge CI. When a red is confirmed (a second run, or the same job failing on consecutive tips), they raise `pipeline.base-red:<repo>@<branch>` and wake the **pipeline steward** ([`docs/pipeline-steward.md`](../../docs/pipeline-steward.md)), the durable judgement task subscribed to `pipeline.*`. Reconcilers apply no other automatic policy to a red base. **The steward decides what happens.**

| | Reconcilers (mechanical) | Pipeline steward (judgement) |
| --- | --- | --- |
| Detect | Run and confirm B's post-merge CI; record the red stretch and the commit range it started in | — |
| Report | Raise and clear `pipeline.base-red`; wake the steward once per red stretch | Read the notification and the commit range; name the merge it suspects |
| Act | Nothing beyond the above | Choose from the options below, at most `PipelineStewardMaxActions` per wake |

The steward's options, and what each one needs:

| Option | Capability or API |
| --- | --- |
| Treat it as a flake: re-run B's CI, file a deflake task | `api:task-admin` (re-run grain CI); `propose_task` |
| Hold merges on B | Per-branch merge hold (*gap*) |
| Lift a hold | Per-branch merge hold (*gap*) |
| Revert the suspected merge | `propose_task` for a revert task (without `fixes_base_branch`); `request.revert:<task or sha>` to ask a person to revert a person's change |
| Let the base repair run, or file one | `propose_task` with `fixes_base_branch` (*gap*: the steward is told never to file a repair) |
| Let other green pull requests keep merging | Nothing: this is the default |
| Escalate to the on-call | `notify_operator` addressed to `oncall` ([`attribution.design.md`](attribution.design.md#schedule-owners-and-oncall)) (*gap*: `oncall` does not exist yet) |

**Until the steward acts**, merges into B continue. A red base is a fact about B, not about any one pull request, and the steward is woken in the same cycle the red is confirmed.

*Open question:* should merges into B pause automatically between a confirmed red and the steward's first decision? The recommendation is no: merges continue, because the steward is woken immediately and a hold is one of its options.

## Invariants

Each of these is a one-line, testable statement. The test named for each one pins it today; *untested* means no test pins it yet.

| Invariant | Test |
| --- | --- |
| A pull request merges iff it is clean and green on its own head, or force-merged for that exact head and clean | `TestTheMergePredicateHasOneArmAndOneException`, `TestMergePipelineForceMergeOverridesWithCleanRequirement` |
| A clean, green pull request merges on the next cycle regardless of what has landed on B since it was tested | Partly, `TestMergeQueueMergesABranchThatIsBehindOnTheVerdictItAlreadyHolds`; contradicted for overlapping changes by `TestEnforceRechecksAnOverlappingChangeBeforeItMerges` |
| No pull request's state delays another pull request's merge | Partly, `TestSyncPullRequestsRepairsFailingCIBehindQueueHeadInParallel` and `TestMergeOnGreenMergesAGreenChangePastAHeadStillInCI`; contradicted by `TestWithoutMergeOnGreenAHeadStillInCIHoldsTheLane` |
| The per-cycle cap limits how many merge into B and never makes one pull request wait on another's state | Throttle: `TestSyncPullRequestsAllowsConfiguredMergesPerRepo`; never orders: *untested* |
| "Clean" is re-read before each merge in a cycle | *untested* |
| A pull request without its required review, security review or Submit never merges | `TestSyncPullRequestsHoldsTheMergeUntilTheReviewHasLanded`, `TestSecurityHoldReholdsACommitPushedAfterTheReviewersHead`, `TestAHumanRequiredChangeStillHoldsAfterACleanReview`, `TestWalkthroughSubmitThenPushDoesNotMergeTheUnseenHead` |
| A pending pull request never merges | `TestSyncPullRequestsDoesNotMergeAPullRequestWhoseRunIsStillGoing`, `TestABranchThatIsBehindStillNeedsAVerdictAboutItsOwnHead` |
| A pull request gets at most two automatic repairs, then `merge_failed` | `TestASecondRepairNeedsTheBaseToMoveOrTheFailuresToChange`, `TestMergeQueueEscalatesAfterTheSecondRefreshInsteadOfRepairingAgain` |
| A conflict cluster has at most one repair in flight | *untested* |
| A check pending past its bound raises a stall notification and does not merge | Not merging: `TestSyncPullRequestsDoesNotMergeAPullRequestWhoseRunIsStillGoing`; the bound: `TestSyncPullRequestsTimesStalledChecksBehindTheQueueHeadToo`; the notification: *untested* (see gap 11) |
| A confirmed red base raises `pipeline.base-red` and wakes the steward | `TestAConfirmedRedBaseRaisesBaseRedAndTheGreenVerdictClearsIt`, `TestAConfirmedRedBaseBranchWakesThePipelineSteward` |
| A red base changes nothing about which pull requests merge until the steward acts | *untested* |

## Non-goals / removed

- **Merge batching** (gather window, batch lock, bisection) does not exist.
- **The overlap re-check** (testing a change on top of B's tip when what it touches overlaps what has landed) does not exist.
- **Queue ordering** (a head per branch, and pull requests waiting "behind" it) does not exist.

Any of these can be reconsidered only with measured evidence that the simple rule is not keeping up. Examples are a rate of red-after-merge on a branch that the steward cannot keep up with, or repair churn in conflict clusters that the one-repair-per-cluster limit does not contain.

## Implementation gaps

Where the code differs from this design today. [Task #2346](/tasks/2346) is removing ordering from the merge walk.

1. **The overlap re-check is still enforced.** `RepoConfig.MergeRecheck` defaults to `enforce` (`model.MergeRecheckModeOf`), and `pkg/orchestrator/mergerecheck.go` re-tests overlapping changes on the merge-queue ref (`model.MergeBatchRef`) before merging. `mergepredicate.go`'s header still describes this.
2. **Lanes close for ordering.** In `mergeWalk` (`pkg/orchestrator/sync.go`), an entry that is pending, unknown or unreadable closes its lane for everything behind it (`passedOver`). The exception is a repository with `merge_on_green`. A failed act on one entry also closes its lane (`closeEntry`), or the whole repository when the lane is unknown.
3. **Heads still exist.** Only a lane's head has its stale branch refreshed (`advanceMergeQueueHead`'s `isHead`). `queuestatus.go` shows `waiting_queue` ("waiting its turn") and lets entries behind wear `checks_unfixable` "until their own turn comes". `showQueueAtFrontOfBacklog` presents the queue as an order.
4. **The cap's default is 1, not 5** (`model.DefaultMaxMergesPerRepo` and the `max_merges_per_repo` column default). Under `enforce`, a merge also does not spend the cap (`Config.MaxMergesPerRepo`'s comment).
5. **"Clean" before each merge.** `syncEntry` observes each pull request when it reaches it (`queueEntry.observe`), so the read follows earlier merges in the cycle. No test pins this, and no test covers a forge answer computed before the previous merge.
6. **No conflict clusters.** Nothing identifies a cluster or limits a cluster's repairs. Each conflicted pull request gets its own repair.
7. **A red base suppresses a pull request's base refresh.** While B is red, `advanceMergeQueueHead` does not merge B into a failing pull request (`baseIsBroken`). That is automatic policy beyond detecting and reporting.
8. **No per-branch merge hold.** The steward can pause all dispatch (`api:dispatch-control`, `model.PipelinePause`) or park tasks with `depends_on`. Nothing holds, or lifts a hold on, merges into one branch.
9. **Base repairs.** `confirmBaseBranchCI` files no repair. `fileBaseBranchRepair` has no caller outside tests, and the steward's prompt says it never files a repair. So nobody files a base repair automatically, and the steward has no sanctioned way to file one. `docs/pipeline-steward.md` still says reconcilers file one (`fileBaseBranchRepair`) and park dependents (`linkBrokenBaseDependents`, which no longer exists).
10. **No `oncall`.** `grain_config.oncall` is not implemented, so the steward escalates with `notify_operator` to nobody in particular.
11. **A stalled check ends in `merge_failed`, and nothing is raised.** After `defaultCheckStallDeadline` (2h, `pkg/forge/checks.go`), a lane's head is handed to a person as `merge_failed` (`escalateStalledChecks`) and needs a Retry to come back; entries that are not heads are not escalated. `pipeline.merge-stalled` is no longer raised (`reconcileMergeStalledClears` only clears old ones), so the steward finds the stall (`Observation.MergeQueueStalledAt`) only on its next scheduled wake, which may be up to 6 hours later.
12. **Tests pin the old behaviour.** `TestWithoutMergeOnGreenAHeadStillInCIHoldsTheLane`, `TestAHeadStillInCIStillHoldsItsOwnBranch`, `TestSyncPullRequestsDoesNotMergePastAHeadWhoseChecksAreRunning`, `TestWalkthroughOverlappingChangesMergeOneAtATimeWithNoBatch` and the `TestEnforce*` re-check tests assert ordering or the overlap re-check. They have to be inverted or removed along with that behaviour.
13. **`task-priorities.design.md` says priority orders "the merge queue".** Under this design, priority decides only which eligible pull requests a cycle visits first.
