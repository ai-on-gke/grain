--- review
This change touches paths the repository's gating triggers watch:

- `payments` (repository file `.grain/triggers/payments.yml`, sensitive, human: `on-findings`) filed task 20 (grain's security review) for `payments/pay.go`

Auto-merge is withdrawn until every one of those tasks has finished without findings and without commits of its own; otherwise it waits for a human's Submit.
--- merge-queue
Merge rules: .grain/merge-rules.yml at f21207fb9b67 matched no rule (the defaults apply) for this change's head 216b59841da1. It needs:

- `ci.mode: github` -- from repository setting

--- review
Review task 20 left findings on this change, so its path-trigger review gate did not pass. Address the findings so a re-review passes, or, if the change should merge as it is, accept them as they are with **Accept findings** on the merge-gates panel (`grain accept-findings`, a reason required), which clears this review's findings on this gate and nothing else. Pressing Submit records your approval only and will not merge over open findings; Force merge is for overriding every gate, not for this.
--- review
**Review needs your decision.** The review of this change (task 20) fixed what it could and left 1 open question on acme/widgets#9008 that it could not settle without you:

- `payments/pay.go:3` -- Q9: should Charge log the amount it refuses?

Reply here (or on task 20) with your decision, and grain sends another run on this task to carry it out -- it fixes what you decided and a clean re-review clears the gate. Pressing Submit records your approval only and will not merge over open findings; to merge without fixing them, accept them with **Accept findings** (`grain accept-findings`, a reason required) rather than Force merge. Posting them as a review on acme/widgets#9008 failed, so they are here and on task 20 only.
