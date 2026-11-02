# Task liveness: waiting on a named resource, executing, or needs input

## The rule

At every moment, every task is in exactly one of these:

1. **Waiting for a named resource** that something will free. Named means
   the row can say what it is: a sandbox slot or worker slot, a model
   connection's usage-limit window, another task, a grain CI run that is
   actually queued or running, GitHub's checks or reviews on a pull
   request, the merge gather window's deadline. Something will free it
   means a component of grain is responsible for the event that ends the
   wait (a *driver*), or the event comes from outside (GitHub, a person on
   GitHub) and grain has a clock on it.
2. **Executing**: an agent run, a repair run or a console session is live.
3. **Needs input from a person**: the task is in the inbox, and the row
   offers the control that ends the wait (Reply, Approve, Submit, Retry,
   Retry merge, Undefer, Reopen).

A task that is "waiting" on something nobody will produce, and that is
not in anybody's inbox, breaks the rule. grain has to make that
impossible by construction. Describing it accurately on a row is not
enough.

The UI must always be able to show which of the three a task is in, and
why. The task badge and the merge-queue row show the state. The sub-case
(below) is the why. A state past its patience has been escalated, so the
row moves into one of the three; it does not just get a stall sentence.

## Where it lives

- `pkg/orchestrator/liveness.go` classifies every **state** of both lanes
  (the merge queue's `mergeState*` and a task's own `model.State`) as
  driven, awaiting a person, or awaiting an external answer. Each state has
  a driver, a control or a patience.
- `pkg/orchestrator/livenessreasons.go` classifies every **sub-case**
  (reason) a state can be held for. Each one is a constant
  `"<state>/<reason>"` with:
  - the named resource it waits on;
  - a driver (a function in the package, checked to exist), or a control
    for one a person owns;
  - an **escalation** for when its state's patience runs out:
    - `agent`: hand the task back to its agent;
    - `person`: put it in a person's inbox;
    - `resource`: the thing it waits on carries the escalation.
- `liveness_internal_test.go` and `livenessreasons_internal_test.go` read
  the states and the reasons off the files that declare them. A state or a
  reason that is declared and not classified fails CI. So does a state with
  no reason, a driver that does not exist, or a sub-case with no
  escalation.

## Escalation instead of annotation

Before this change, an entry past its state's patience got a sentence
("read it as stuck rather than as waiting") and a `MergeQueueStalledAt`
stamp. Nothing moved it. Now the merge queue's per-entry cycle
(`syncEntry`) ends with an arm that escalates any entry whose
`MergeQueueStalledAt` is set, according to its sub-case
(`stallEscalationDue`, `escalateStalledEntry`):

- **agent**: grain posts a comment naming the state, the sub-case, the
  resource, the driver that did not act, and since when. The task is
  requeued (`RetryRequestedAt`, `CompletedAt` cleared), the same requeue a
  person-cancelled CI run gets. It is not a repair: `MergeQueueRepairs` and
  `MergeQueueRepairAt` are untouched. It shares the stall's one automatic
  answer per head commit with `retryStalledCI`
  (`MergeQueueStallRetryHead`), so a second stall at the same head goes to
  a person.
- **person**: `blockMergeQueue` stops the queue driving the change.
  `merge_failed` is written with a note naming the sub-case and the driver
  that did not move it, and the row offers "Retry merge". The inbox shows
  it (`ui/src/state.js`'s `completionPhase`). The change still merges by
  itself if it reads clean.
- **resource**: not escalated here. The entry names what it is waiting on,
  and that thing escalates itself.

The arm never escalates a live run (the task is executing) or an entry
already given up on. It is the last arm of the switch, so every specific
driver arm (merge, repair, cancel hand-back, the CI stall retry and its
escalation) still gets the first word.

## The choices, per sub-case

### Merge lane

| sub-case | waits on | driver | past patience |
|---|---|---|---|
| `waiting_ci/grain_not_started` | a grain CI run for the head | reconcileTaskCI | agent |
| `waiting_ci/grain_queued_for_capacity` | a sandbox slot for CI jobs | reconcileTaskCI | person |
| `waiting_ci/grain_running` | the CI run in flight | reconcileTaskCI | person |
| `waiting_ci/replacement_due` | the replacement run owed | reconcileTaskCI | agent |
| `waiting_ci/replacement_spent` | nothing: retries spent | escalateGivenUpGrainCI | person |
| `waiting_ci/no_verdict` | a CI verdict | reconcileTaskCI | agent |
| `waiting_ci/github_checks_pending` | GitHub's checks (external) | -- | person |
| `waiting_queue/behind_head` | the queue's head entry | queueOrder | resource |
| `waiting_queue/checks_unknown` | the credential reading checks (external) | -- | resource |
| `waiting_review/review_running` | the review task | reviewStateOf | person |
| `merging/merge_call` | GitHub's merge answer | syncEntry | person |
| `updating_branch/base_merge` | GitHub's base-merge answer | bringHeadUpToDate | person |
| `batching/batch_run` | the batch's CI run | reconcileMergeBatches | person |
| `gathering/gather_window` | the gather deadline | reconcileMergeBatches | person |
| `base_broken/base_repair_task` | the base's repair task | reconcileSync | resource |
| `base_broken/confirming` | base-branch CI confirming red | reconcileBaseBranchCI | person |
| `checks_unfixable/awaiting_turn` | reaching the queue's front | escalateUnfixableChecks | person |
| `waiting_github_approval/approval` | an approving review (external) | -- | person |
| `github_merge_queue/github_queue` | GitHub's merge queue (external) | -- | person |
| `repair_needed/awaiting_turn` | the owed repair | requeueForRepair | person |
| `repair_queued/awaiting_worker` | a worker slot | reconcileDispatch | person |
| `repairing/repair_run` | (executing) | ProcessResult | person, not while live |
| `agent_working/agent_run` | (executing) | ProcessResult | person, not while live |
| `merge_failed/given_up` | a person: "Retry merge" | -- | -- |
| `unreadable/read_failing` | the credential reading the PR (external) | -- | resource |

Why these choices:

- **Agent** where an agent with `retrigger_ci` and `wait_for_checks` can
  plausibly fix the wait itself: a run that never started, a replacement
  that never came, a person's cancellation it has to interpret.
- **Person** where the cause is outside the branch (capacity, GitHub, a
  spent retry budget), or where an agent has already had its turn.
- **Resource** where escalating every waiter would multiply one fault.
  Blocking every entry behind a stuck head would empty the queue for one
  stuck change. Commenting on every pull request because the deployment's
  credential cannot read GitHub would bury one fact under many. The head
  entry, or the deployment's health, carries the escalation instead. Rows
  in these states are still flagged as stalled, and the inbox's stalled
  group shows them.

### Task lane

| sub-case | waits on | driver | past patience |
|---|---|---|---|
| `queued/ready` | a worker slot and a sandbox | reconcileDispatch | person (inbox stalled group, "Run now") |
| `queued/blocked_on_task` | the task it depends on | reconcileDispatch | resource |
| `queued/usage_limit_paused` | the connection's limit window | reconcileDispatch | person ("Run now") |
| `running/live_run` | (executing) | ProcessResult | person ("Cancel") |
| `completed/on_merge_queue` | the merge queue landing it | syncEntry | resource (the merge lane) |
| `completed/merge_failed` | a person: "Retry merge" | -- | -- |
| `proposed/unapproved` | a person: "Approve" | -- | -- |
| `awaiting_reply/question` | a person: "Reply" | -- | -- |
| `awaiting_submit/not_auto_merge` | a person: "Submit" | -- | -- |
| `failed/consecutive_failures` | a person: "Retry" | -- | -- |
| `deferred/no_wake_time` | a person: "Undefer" | -- | -- |
| `deferred/wake_time` | the wake time | reconcileWake | person ("Undefer") |
| `closed/closed` | a person: "Reopen" | -- | -- |

The task lane's escalation to a person is the inbox's stalled group:
`TaskStallNote` sets `ui.Task.Stall`, and `waitsOnUser` admits the task.
The task lane has no per-task cycle to block a task from.

`taskReasonOf` computes a task's sub-case from the task, its observation,
its open dependencies, whether a usage-limit pause covers its dispatch,
`DeferredUntil` and the merge queue's state. An open dependency outranks
a pause. The UI reads it on every task read through `ui.Config.Reasons`
(`orchestrator.TaskReason`, wired from `pkg/daemon` the way
`ui.Config.Stall` is). The pause is read off the quota grant wait dispatch
records for a task a pause covers (`gatedByPause`, or
`dispatch.ConnectionPausedWhy`), because only the dispatch cycle can
resolve which connection a task would run on. `ui.Task.Reason` carries
the sub-case, its condition, its resource and its control. The row shows
the sub-case as a chip. A stalled row, and the inbox's stalled group,
offer the control: "Run now", "Cancel", "Undefer" or "Retry merge".

The merge lane's sub-case is recorded with its state
(`Observation.MergeQueueReason`, schema 40). The main pass records
`mergeReasonOf`. Any other writer, which always writes a state with a
single reason, records that state's own reason (`mergeReasonOfState`).
`ui.Task.MergeQueueReason` carries it, classified. The merge-queue
activity line on the row shows it, and its hover says what drives it.

## Audit

Each state and sub-case was checked against the rule. The ones that broke
it before this change:

- **`waiting_ci` after a person cancelled the run** (tasks 335, 351, 360,
  373 on 2026-10-23). The state was classified "driven by reconcileTaskCI",
  but reconcileTaskCI deliberately starts nothing after a person's cancel,
  and the backstop only watched pending checks at the queue head. Task 418
  handed the cancellation back to the agent; task 319 removed that
  special case altogether, so a person's cancel is now replaced after
  `ciRetryDelay` like any other (`waiting_ci/replacement_due`, then
  `waiting_ci/replacement_spent`). Before, it could sit indefinitely.
- **Every driven merge-lane state past patience.** Before, these were only
  annotated (`noteWithStall`, `MergeQueueStalledAt`). Now they are
  escalated per the table above. This includes entries that are not their
  queue's head, which the CI stall arm (`isHead && stalled`) never reached.
- **`waiting_ci` with grain CI required and no run, or an owed replacement
  that never started.** reconcileTaskCI failing to act left nothing behind
  it but a two-hour pending-checks clock that only runs at the head. Now
  the task goes to its agent, then to a person.

Checked and found to comply:

- **`completed` with a `merge_failed` entry.** The badge stays
  `completed`, but the inbox admits it (`completionPhase`) with "Retry
  merge". It is classified `completed/merge_failed`, person-owned.
- **Deferred with no wake time.** This is a person's deliberate "not now":
  the task is person-owned with "Undefer". It is deliberately kept out of
  the inbox (`waitsOnUser`), because deferring is the answer the inbox
  asks for. A deferral with a wake time is driven by `reconcileWake`,
  measured from the wake time (`model.StateTimes.Since`) with one
  cycle's grace (`deferredWake`); past that it is in the inbox's stalled
  group with "Undefer".
- **Blocked on a task that is itself stuck.** The wait is on a named
  resource, the other task, and the escalation belongs to that task. A
  stuck blocker is in the inbox's stalled group, or merge_failed, so the
  chain ends at a person.
- **Paused on a usage limit when every connection serving the model is
  paused for hours.** The resource is named, and the window has an end
  time, so it is driven. Past the backlog patience it is in the inbox's
  stalled group.

## Not done yet

- The task lane still escalates to a person only through the inbox. Its
  sub-case is computed on the read, and nothing in the orchestrator acts
  on it the way `escalateStalledEntry` acts on the merge lane's.
