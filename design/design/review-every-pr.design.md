# grain reviews every pull request, and that review is the reviewed merge

**Owner:** @bwsalmon

**Status:** proposed (task 6022). Design only: nothing here is built yet.

**Specified by:** [`review-every-pr.spec.md`](../spec/review-every-pr/review-every-pr.spec.md)

**The core rule:** On any repository with pull-request intake enabled, grain adopts every open pull request it does not already own as a review-only shepherd, submits an `APPROVE` review from a dedicated GitHub App review identity (`GRAIN_REVIEW_LOGINS`) only when every gate for that head clears, and `pkg/reviewedmerge` counts those approvals as reviewed merges alongside `GRAIN_MERGE_LOGINS` and collaborator reviews while holding unreviewed merges for an automatic post-merge grain review or recorded break-glass.

## Problem

Core publishes container images and upgrades deployments only when every first-parent commit since the last checked build reached `main` or `release/v*` through a reviewed merge (`pkg/reviewedmerge`, `cmd/reviewed-merge`, `pkg/upgrade/reviewed.go`). Today a merge passes only if `merged_by` is in `GRAIN_MERGE_LOGINS` or a human collaborator other than the author approved the merged head on the forge. Because grain reviews only pull requests it opened itself (or ones explicitly handed to `grain pr adopt`), outside or hand-merged pull requests receive no grain review by default and stall image publishing when merged without a collaborator approval (as #1488, #1489, #1498 and #1501 did on 2026-11-13/14).

## Proposal

- **Intake as review-only shepherds** ([spec §1](../spec/review-every-pr/review-every-pr.spec.md#1-intake)): grain reconciles open pull requests via webhooks and conditional polling (`RP-1`, `RP-2`) and adopts unowned ready pull requests (`RP-4`) as `TakeoverReviewOnly` shepherds with auto-merge off. Collaborator pull requests run immediately; non-collaborator pull requests default to `proposed` (`RP-3`). Incremental re-reviews start only from the last ancestor head grain approved (or whose findings were all accepted under `RP-10`), falling back to a full `base...head` review or carrying open findings forward as blocking gates so an unapproved head's findings can never be bypassed by pushing an unrelated commit on top (`RP-5`). Daily and per-head caps fail closed (`RP-6`).
- **App-backed `APPROVE` and `reviewedmerge` support** ([spec §2](../spec/review-every-pr/review-every-pr.spec.md#2-making-the-review-count)): when every review and trigger gate for a head clears, grain posts `APPROVE` at that `commit_id` from a dedicated App `review_credential` (never a person's token) and dismisses stale grain approvals when the head moves (`RP-5`, `RP-7`). `pkg/reviewedmerge` adds `Policy.Reviewers`, `Pull.MergedAt`, `Review.ID` and `Review.SubmittedAt` (with REST decoding of `merged_at`, `id` and `submitted_at`), plus optional `Merge.Review` and `Merge.PostMerge` (`Via: "grain-review"`).
- **Findings, Opt-in Fixes, and Non-author Acceptance** ([spec §3](../spec/review-every-pr/review-every-pr.spec.md#3-findings-on-someone-elses-pull-request)): reviews with findings post `COMMENT` inline threads rather than `REQUEST_CHANGES` so collaborators can still overrule grain (`RP-8`), and grain never pushes unless asked via `@<slug> fix` or **Fix it for me** (`RP-9`). On an intake pull request, **Accept findings** clears the gate only when the acceptor holds `merge:write` **and** has a verified linked forge login different from the pull request author's (`RP-10`).
- **Post-merge Review and Break-glass** ([spec §4](../spec/review-every-pr/review-every-pr.spec.md#4-bypass-and-emergencies)): if an unreviewed pull request is merged anyway, publishing stays held while intake notifies the operator and runs an automatic post-merge review (`RP-12`), clearing the hold with `PostMerge: true` (`Review.SubmittedAt.After(Pull.MergedAt)`) once clean. Emergency collaborator-only approvals remain valid (`Via: "review"`) and are audited as `reviewed-merge-exception` events (`RP-13`).

## Non-goals

Changing what `.grain/templates/pr-review.yml` or the security review checks; pushing to external branches uninvited; weakening `pkg/reviewedmerge` so unvouched commits pass; or putting forge-specific logic into core ahead of the edition follow-up (`RP-16`).

## Rollout

Roll out per repository via `grain/config` settings (`RP-14`, `RP-15`): start in `pr_intake: shadow` (comment-only), land the `pkg/reviewedmerge` `Reviewers` and timestamp changes and promote to `pr_intake: approve` under an App `review_credential`, enable branch protection once App approval counting on GitHub is verified, and track `grain_pr_merges_total{via, post_merge}` (`via` matching `Merge.Via`: `grain`, `grain-review`, `review`, plus `unreviewed`) and `grain_review_approvals_unrecorded_total` (`RP-17`).
