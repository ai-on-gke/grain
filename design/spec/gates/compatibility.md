# Source inventory, compatibility and authority handoff

This adapts Task #51's current-behavior mapping at `f764e02e0` (`docs/mcp-gates-migration-coverage.md` on its branch) to the built-in starting point. Source inspected at `e253df886`, including Tasks #47/#50. That mapping's remote transport recommendations are superseded, not carried into v1. Source paths below describe existing code; the new catalogue, policy fields, central owner and result/action contracts are proposed.

| Current source / behavior | Initial placement | Exact import versus intentional improvement |
| --- | --- | --- |
| [finish.go](../../../pkg/orchestrator/finish.go) `processResultFrom`, `finishNothingNeeded`, `finishAnswered`, branch salvage; [state.go](../../../pkg/model/state.go) `AwaitsAck`, `ClosesOnAnswer` | `delivery`, `acknowledgment`; controller disposition policy | Preserve questions/access/secret/suspension before completion; pushed/open PR precedence over report/no-work; unchanged pre-run branch retirement; report-only child close-on-answer and configured no-work acknowledgment by actual filing provenance. No_action is not success from tool use. New successful closure adds message/operation/other-obligation fences. Report delivery and receipt remain separate. |
| [taskgoals.go](../../../pkg/orchestrator/taskgoals.go) `checkGoal`, `actOnGoal`, `meetGoal`, `restOnGoal`, `accountGoalProgress`, `reconcileMetGoalCloses`; [task_goal.go](../../../pkg/model/task_goal.go) | Explicit goal predicate import plus controller close/rest/standing/no-progress policy | Existing goal task reports rest, person-closes never auto-meets, on-limit hold/notify and per-kind defaults persist. New owner never trusts stored MetAt without revalidation; integration no longer closes past other required outcomes. Autonomous [goal_mode.go](../../../pkg/orchestrator/goal_mode.go) verification is separate run policy, not a gate permit. |
| [forge/health.go](../../../pkg/forge/health.go), [sync.go](../../../pkg/orchestrator/sync.go) `grainCIVerdictOf`, [mergerules.go](../../../pkg/orchestrator/mergerules.go), [ci/basegate.go](../../../pkg/ci/basegate.go), [workflows/basegate.go](../../../pkg/workflows/basegate.go) | `checks` + existing merge preconditions | Import configured GitHub/Grain/both/none, latest applicable jobs, source-specific neutral/skipped/canceled/empty semantics, base-owned required workflow jobs and recheck overlay. New explicit complete-set/no-CI policy replaces accidental missing success; exact-head required-success remains mandatory for Grain. Base red is not a blanket merge prerequisite. |
| [review.go](../../../pkg/orchestrator/review.go) `reviewStateOf`/`reviewFinishedEmptyHanded`, [takeover.go](../../../pkg/orchestrator/takeover.go), [reviewfeedback.go](../../../pkg/orchestrator/reviewfeedback.go) | `delivery product=review` versus `review` guard | Legacy closed or empty-handed ordinary review and missing-reference/no-pending-hold behavior are labeled imports only. New enforced guards require attributed current-head disposition and treat missing review as unknown. Fix PR must land where policy requires it. Successful review delivery may contain findings; it is not merge approval. |
| [securityhold.go](../../../pkg/orchestrator/securityhold.go), [securityhold_inherit.go](../../../pkg/orchestrator/securityhold_inherit.go), [repoguard.go](../../../pkg/orchestrator/repoguard.go), [trustboundary](../../../pkg/trustboundary), [pathtriggerhold.go](../../../pkg/orchestrator/pathtriggerhold.go), [pathtriggerreview.go](../../../pkg/orchestrator/pathtriggerreview.go) | `review` category security/path and `approval` | Preserve base-owned trust rules, conservative unknown/truncated path matches, eligible head carry-forward, human:no/conditional/required, named approvers, incremental dependency/fix outcomes and reviewer self-clear restrictions. New qualified IDs/current policy union prevent one task overwriting another's guard. Carry-forward is a validated rule, never blanket reuse of H1 approval for H2. |
| [accept_findings.go](../../../pkg/ui/accept_findings.go), [reviewquestions.go](../../../pkg/orchestrator/reviewquestions.go), [review_findings_accept.go](../../../pkg/model/review_findings_accept.go), [mergegates.go](../../../pkg/orchestrator/mergegates.go) | Registered `accept_findings` action and review disposition | V1 resolves the prior acceptance gap: exact review ID/version/head/category, eligible person, reason, current policy, invalidated by new finding/review/head/policy or revocation. Clean later review clears only its scope. Submit or forge thread resolution is not acceptance; no completion waiver is inferred. |
| [client.go](../../../pkg/ui/client.go) `submit`, `humanSubmitted`, `submittedHead`, `clearUnansweredCI`; [approvals.go](../../../pkg/ui/approvals.go) | `approval`, existing person-only Submit service | Preserve partial approver eligibility, reviews/findings/questions that remain after Submit, and requesting fresh nonpassing Grain CI. New button carries rendered expected head/context, rejected on mismatch; current click-time head capture is weaker. AutoMerge is queue intent, not blanket approval or execution permit. |
| [mergepredicate.go](../../../pkg/orchestrator/mergepredicate.go), [merge_gates.go](../../../pkg/ui/merge_gates.go), [baserepairbypass.go](../../../pkg/orchestrator/baserepairbypass.go), [mergerecheck.go](../../../pkg/orchestrator/mergerecheck.go), [githubqueue.go](../../../pkg/orchestrator/githubqueue.go) | Same domain merge service with unified guard admission | Import conflict refusal, queue/lane/repair/recheck rules and expected-head force, named overrides (empty legacy list = CI only), eligible base-red repair exception with own CI still required, post-merge review ownership. New current-policy/context expiry, shared locks/intent/receipt and guarded final deferred execution are required. Do not invent a new queue algorithm or drop overlap policy. |
| [sync.go](../../../pkg/orchestrator/sync.go) `recordPullRequestEvents`, [landed.go](../../../pkg/orchestrator/landed.go), [change_submit.go](../../../pkg/ui/change_submit.go), [change/submit.go](../../../pkg/change/submit.go), [clreviews.go](../../../pkg/orchestrator/clreviews.go) | `integration` outcome, PR merge/CL submit action service | Legacy direct PR closed/merged and CL submitted closure remains only for legacy tasks. New closed-unmerged records cancellation/non-success; positive landed/empty equivalence needs authenticated proof. All scoped CLs and renumbered identity must be submitted, never assume one CL stands for all. CL current review/snapshot/health/person-only temporary submit grant and sync/conflict/presubmit handling persist; exact-snapshot conditional proof remains a real adapter blocker. |
| [qa_gate.go](../../../pkg/orchestrator/qa_gate.go), [qa_phases.go](../../../pkg/orchestrator/qa_phases.go), [model/merge_gates.go](../../../pkg/model/merge_gates.go) | `qa_stage` + existing bounded QA repair/triage | Preserve S3/S4/S5 flow/engine/arm/head assignment, opt-in treatment versus control/shadow and human triage floors. Custom flow/promotion guards require their own effect mapping before enforced migration. No shadow result gains treatment authority. |
| [taskgoals.go](../../../pkg/orchestrator/taskgoals.go) baseGreenReading; [base_branch_publishing.go](../../../pkg/model/base_branch_publishing.go); base CI/release collectors | `base_health`, separate `publishing_health` | Legacy green is not in-flight/broken and success or BaseCICleared; publishing option passes absent release row or no finding, with three-failure/configured stale-publication thresholds (currently three hours by default) and superseded no-publish handling. New base mode verifies current tip; positive publication requires actual receipt/coverage. Missing row is unknown for new obligations. Non-reproduction is explicit confirmation, not a waiver. |
| [stage1repair.go](../../../pkg/orchestrator/stage1repair.go), [githubbaserepair.go](../../../pkg/orchestrator/githubbaserepair.go) | `nightly_health` | Import declaration-time selection, newest eligible 500-row workflow/default-like/RC filtering, skipped/unjudged cancellation and accepted job conclusions. Verified default-branch push variant cannot use PR jobs named main. New window/completeness/freshness prevents truncated or old positive proof; still records legacy profile's limitations honestly. |
| [taskgoals.go](../../../pkg/orchestrator/taskgoals.go) `openPipelineNotifications`; [pipelinesteward.go](../../../pkg/orchestrator/pipelinesteward.go) `pipelineStewardQuiet` | `pipeline_health` | Legacy checks repo-filtered notifications, then steward quiet checks notifications **deployment-wide** except its own regression subject; base/stalled tasks/queue scans are also **deployment-wide**, exclude own/paused tasks and permit empty/success base conclusion. New scoped mode requires complete expected base set and scoped health. Do not silently authorize deployment-wide reads from a repo-only attachment. |
| [task.go](../../../pkg/model/task.go), [wait_target.go](../../../pkg/model/wait_target.go), [wait_for_task_tool.go](../../../pkg/mcp/wait_for_task_tool.go), SQL task_blocked/task_ready | `children` only when explicitly required; dispatch waits stay controller policy | Legacy depends_on/child-of waits release on closure, failed/open does not release, unknown link remains blocking; merge wait may settle negatively. Preserve until=closed versus merged and one-shot park-owned link cleanup. New required children default to trustworthy success, failure/cancel doesn't satisfy them, and advisory/spawned links don't add requirements. Parent can do independent work. |
| [cycle.go](../../../pkg/orchestrator/cycle.go), [dispatch](../../../pkg/dispatch), [capability_pass.go](../../../pkg/orchestrator/capability_pass.go), [standingwork.go](../../../pkg/orchestrator/standingwork.go), [wake.go](../../../pkg/orchestrator/wake.go) | Admission/capacity/pause/budget/standing policies, not completion gates | Model/team/base setup, grants/CI capabilities, fit/repair reserve, usage/capacity/interruption/environment/OOM retries, Run now semantics and manual/standing exceptions remain. Run now cannot override hard authorization/guard holds. A gate outage is not an agent failure; event wake first evaluates useful work. |
| [releasequalification.go](../../../pkg/orchestrator/releasequalification.go), [releasestage1.go](../../../pkg/orchestrator/releasestage1.go), [release_approval.go](../../../pkg/ui/release_approval.go), [candidate-gate.sh](../../../scripts/candidate-gate.sh) | Existing qualification/publication policy and receipts; separate adapter admission | Preserve RC SHA/provenance, suite/person/soak/breaking/signing/version checks. Gate observation of publication is not authority to promote/publish. Workflow-to-executor exclusion, exact artifact/version conditions and reconciliation are not implemented by this design; do not migrate these effects under generic dispatch. |

## Implemented observation-only subset

The [runtime observation guide](../../../docs/gate-observations.md) covers thirteen
registered v1 types, including QA stages and release qualification: delivery variants, acknowledgment compatibility profiles,
positive integration, explicit child outcome predicates, checks, review, approval
and base/nightly/publishing/pipeline health. Ordinary authorized
reads and Angular task detail carry optional observation-only projections and
bounded history. Existing goal declarations propose monitor contracts only;
`tasks-closed` retains its actual closed/merged predicate, never relabeled success.
No contract, task, goal limit, controller disposition or actual wait is migrated.
Implementation/review/coordinator comparisons retain exact local source fences,
head/policy receipts and unknown states for missing/private/stale evidence.
Finite non-interactive `implement` tasks whose latest settled run produced no
change link (`AwaitsAck` true, non-review, non-no-work) and ended on a durable
closing note project as report products for `delivery(report/current_v1)` and
`acknowledgment(explicit_receipt)` alongside `analyze` tasks; tasks with change
links, review tasks and `nothing_needed` runs do not.
The [framework review](../../../docs/gate-observation-review.md) and operator guide
record the inherited receipt/body limits and source-specific coverage.
The existing [review/approval adapter](../../../docs/gate-review-observations.md)
remains intact, including its current provider, rendered-policy and role-receipt
gaps; those observations remain unknown without exact positive proof.
There is no contract/decision authority, `?view=gates` implementation or
discovery/mutation/action/waiver API.
The shipped [CI checks adapter](../../../docs/gate-check-observations.md) retains
its scoped collectors, complete collections, acceptance fences and inert merge context.

The [health port](../../../docs/gate-health-observations.md) uses labeled profiles
only for selected existing health goals, without unrelated completion obligations.
All current legacy writers listed below retain authority in this implementation.
The health port preserves actual legacy predicates and declaration timestamps,
including saved verified-push goal dispositions, newest-500 workflow selection,
concrete publishing thresholds/supersession and broad steward exclusions. Current
positive publication remains unknown without authentic complete publication
receipts. Scope/capture gaps never upgrade weak legacy evidence into current proof.
The imports and handoff requirements remain future specifications.

## Explicit concrete goal imports

| Existing name (unchanged API) | Materialized gate + controller policy |
| --- | --- |
| `pr-merged` | `integration pr_merged`, exact task or repository/number resolved identity; existing on_met close/rest and limits retained |
| `cl-submitted` | `integration cl_submitted` over all scoped targets or explicit CL; preserve submitted renumbering provenance |
| `tasks-closed` | `children predicate=closed` or `merged` according to old until; **not** relabeled success |
| `base-green` | `base_health legacy_recorded_v1`, plus optional `publishing_health legacy_no_finding_v1` if publishing=true |
| `nightly-green` | `nightly_health legacy_500_v1` or authenticated verified-push variant, declaration since retained |
| `pipeline-healthy` | `pipeline_health legacy_quiet_v1` with approved broad scope and old filters/exclusions |
| `person-closes` | Controller standing intent, not an always-unmet fake evaluator; explicit request completion gates and rest/close policy |

Defaults for max_resumes/max_stall/on_limit are materialized from the exact existing `GoalDefaults` at import, with source version; do not replace them with a single catalogue-wide limit. New success-required children, exact-head reviews, positive publication and current-window evidence are explicit stronger contracts, never silent rewrites of old goals.

## Writers that must lose lifecycle authority per migrated task

- Finish/report/no-work/keep-running/PR completion and review-task cleanup preserve facts and disposition requests only.
- Goal act/meet/reconcileMetGoalCloses, base/nightly callbacks, direct PR event/landed closure and CL RecordSubmitted cease direct close/rest writes for that epoch.
- Security/path/QA/review/Submit settlement supplies dispositions and guarded action requests, never a second completion owner or AutoMerge permit.
- Wake/suspension, UI/API Run now/retry/defer/close/reopen, message ack, goal edits and recovery go through central epoch-aware transition functions. Scheduler capacity remains separate.
- [Go StateOf](../../../pkg/model/state.go), [SQLite schema](../../../pkg/model/schema.go) and [Postgres schema](../../../pkg/model/postgres/schema.go) project the same authority/precedence. Compatibility fields never override the new owner.

Collector reuse does not prove implementation readiness: generation capture, conditional writes, immutable approval/outcome records and complete-set fences must be added where absent. See [acceptance scenarios](acceptance.md) for qualification obligations and the spec for the one-epoch handoff/rollback.


Task #69 adds qa_stage task monitors (only nonrequired, action=null) before PR
creation and release_qualification repository monitors. The [QA/release audit](../../../docs/gate-qa-release-observations.md)
records full source mapping and inaccessible evidence. The review port's repository
authorization, transitive-evidence checks and scoped-history omission are retained.
No legacy authority or effect admission changes. The [combined audit](../../../docs/gate-coverage-audit.md)
accounts for every original Task #51 row, all thirteen types and configuration
modes, concrete source/authorization blockers, unchanged controller policies and
the planned discrepancy inspection workflow. Standing policy does not suppress
independently selected integration/child outcomes; none of those observations
predicts lifecycle or scheduling readiness.
