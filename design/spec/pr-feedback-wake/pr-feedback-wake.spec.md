# PR feedback wake (current runtime)

This specifies the current orchestrator, independently of the proposed built-in
gates protocol. Implementation: `githubevents.go`, `reviewthreads.go`,
`model/pr_feedback.go`, `task_ready`, and `RunDispatch`.

## Ingestion and identity

| Path | Supported actionable feedback |
| --- | --- |
| Verified `issue_comment` webhook | `created`, nonempty body, on a mapped PR |
| Verified `pull_request_review_comment` webhook | `created`, nonempty body; both roots and replies |
| Verified `pull_request_review` webhook | `submitted` with nonempty body, or `changes_requested` without a body |
| Existing `SyncReviewThreads` polling | Previously unrecorded unresolved root comments on finished changes or tasks resting on their own `resume_later`, including outdated roots; existing draft/relayed-review exclusions apply |

Edits, deletions, review dismissals, empty non-change-request reviews and ordinary
issue comments are not new PR feedback wakes. Ordinary issue replies retain their
existing behavior. Polling remains a root-thread fallback, not conversation or
review-submission backfill. Replies require webhook delivery.

Webhook feedback requires the existing trusted-collaborator permission check.
The configured App's own login, existing delegated-author signature footers,
work-tree/lineage markers, draft reviews
and relayed review roots are filtered using existing provenance. Polling retains
its existing reviewer/bot policy and resolved/draft/relayed-thread filtering;
this feature does not broaden collaborator permissions.

Primary ownership follows on-target `fixes` and `external-pr` links, including
queued, running and waiting owners. Review tasks and fix tasks stacked on another
branch are excluded from primary ownership. With no primary owner, external PRs
retain the existing reply convention: the newest live task with that GitHub
origin receives feedback, including an external-review task waiting on input. Closed tasks and closed/merged PR links are excluded. Multiple
owners produce an explicit reconciliation error rather than choosing a task.

Source identity is `issue_comment:<id>`, `review_comment:<id>` or `review:<id>`,
per task. Delivery IDs do not determine whether feedback is new. Existing
`github-event` and `review-thread` links remain deduplication evidence. Migration
152 creates an empty receipt table; it does not replay historical comments.
Webhook and polling paths share receipts, so observing the same root through
both paths cannot request two runs.

## Delivery and eligibility

The source receipt, conversation entry and provenance links commit together.
Original author and source URL accompany quoted, explicitly untrusted text.
External feedback is attributed to the webhook automation, not to a human posting
instructions in Grain. Thus it cannot automatically answer a question, settle
review questions, supply a secret, grant access, or constitute a CI verdict.

A receipt is pending while its comment ID exceeds the existing
`Observation.DeliveredCommentID` (zero before any delivery). Multiple pending
receipts confer one readiness exception, not one schedule per comment.

An approved, open task may dispatch despite:

- its own requested CI or message suspension, identified by the run that actually
  registered the deferral; or
- an ordinary pending question, with no separate deferral.

A CI/message suspension with a fallback time is still an OR wait; feedback may
dispatch before that fallback. Time-only and task-only parks, operator deferrals
and deliberate future schedules are not overridden. Dependencies, task and
pipeline pauses, operator stop, failure limits, admission checks, capacity,
permission/secret requests and the review-questions decision hold still apply.
Unapproved and ambiguous states are not implicitly approved or normalized.

The old question, CI target/head evidence, deferral and requested suspension remain
in place. A feedback dispatch does not mark that suspension resumed. The wait
continues to govern after the feedback is delivered; a new deferral registered by
the agent supersedes the old one through the existing suspension ownership
timestamps. An ordinary later dispatch resumes suspensions normally. Derived
task state continues to describe the retained wait; `task_ready` expresses the
exception rather than manufacturing an answer or a verdict.

Queued tasks receive context through their existing dispatch. Running tasks receive
the existing MCP addenda delivery and no second live run or extra attempt. The
unique live-run constraint and a transactional readiness recheck at ordinary
queue claiming protect competing dispatchers and changes during admission.

Unread feedback is included in full in a dedicated dispatch prompt section,
outside the bounded conversation-history window, and in suspended-session
prompts. A successful run advances the delivery cursor only through its prompt
snapshot; normal addenda acknowledgments can advance it through feedback actually
shown mid-run. Setup/execution failures do not acknowledge the prompt. Feedback
arriving after the snapshot remains pending, even if a new question was written
later. Feedback received during a live run (or its queued result-processing window) also retains that
run's identity: if
the run completes without seeing it, the pending receipt permits one subsequent
feedback dispatch on the still-open task. No receipt is spent merely by claiming
a run.

Existing explicit-mention/changes-requested repair behavior for completed changes
is retained. An unmentioned conversation comment on an already completed change
does not independently request a repair. Closed/canceled tasks are never revived.

## Limits and future integration

Webhook text retains the existing normalized-envelope size limit (32 KiB per
body). Without webhook delivery, only the existing root-thread poll fallback is
available, in its existing task states; conversation comments, review summaries
and inline replies are not recovered by a new poller. Unknown ownership is
reported and leaves the webhook unacknowledged for reconciliation.

The future gates message/event wake policy should consume these source identities
and delivery receipts and express this same narrow readiness exception. Feedback
is a reason to reconsider work, never evidence that an unrelated gate passed.
This change adds no gate protocol, deployment or configuration changes.

## Regression validation

`go test ./pkg/orchestrator -run '^TestPRFeedback' -count=1` exercises all three
event types; CI wait to one dispatch with preserved head evidence; user-input
wait without a fabricated answer; queued/running coalescing; replay, edit,
self-generated and unauthorized events; unseen feedback during final parking and
successful completion; polling/webhook identity sharing; history truncation;
closed, paused, deferred, future, dependency and permission holds; and changes
between admission and queue claiming.

The full affected suites are `go test ./pkg/orchestrator ./pkg/dispatch
./pkg/model/... ./pkg/staterepo`.
