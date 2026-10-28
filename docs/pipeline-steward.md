# The pipeline steward

grain/task-461: a standing job that keeps this deployment's merge
pipeline moving. Since DT-S9 (`docs/design/durable-tasks.md#DT-6`) it is
one durable judgement task, woken by `pipeline.*` notifications.
Reconcilers do the mechanical work -- filing repairs, parking work behind
them, retriggering flakes, re-running CI, raising stalls and stale ports
-- and the steward does what they cannot: names the merge it suspects,
decides whether a revert is warranted, deprioritises or pauses work, and
explains a stalled pipeline in a notification a person can act on.

This describes what is implemented, not a proposal:

- `model.PipelineStewardTemplateID` and `pipelineStewardTemplate` in
  `pkg/model/pipeline_steward_template.go`;
- the standing-task wake in `pkg/model/standing_task.go`;
- the precondition gate and the wake on red in
  `pkg/orchestrator/pipelinesteward.go`;
- the reconciler arms that took over its mechanical duties in
  `pkg/orchestrator/stewardarms.go`;
- the judgement model in `pkg/model/judgement_model.go`.

## What it is

- **A schedule every deployment gets.** `system:pipeline-steward` is
  seeded by `reconcileSystemSchedules` alongside `system:memory-agent`,
  `system:release-notes` and `system:infra-review`. It is a well-known id,
  written once, and the deployment owns it from then on: how often it
  runs, and whether it runs at all.
- **Titled "Pipeline steward".** The task and its schedule carry the
  template's title, so the steward is recognisable in the queue and the
  inbox. Both are refreshed from this build: the schedule row on every
  cycle (`resyncSystemSchedule`), the standing task on every wake, so a
  deployment that filed it under an earlier title is renamed without
  anyone touching it.
- **Every 6 hours** (`everyNMinutes`, 360), with firings that never
  overlap. This is a safety wake: the steward is woken by the
  notifications it subscribes to (below). It was every 30 minutes
  before DT-S9. The interval is the seeded default only: a deployment
  that seeded the schedule earlier keeps its own row (on a deployment
  whose settings live in a state repository, that is a change there).
- **Woken by notifications.** The standing task subscribes to
  `pipeline.*` (`model.PipelineStewardSubscriptions`), so a red base, a
  stalled merge or task, a failing batch or repair, or a stale port wakes
  it the moment it is raised (`Store.WakeNotificationSubscribers`).
- **Durable, with a goal.** It holds the `durable` grant (no deadline or
  wait cap, never held behind `max_workers`) and the goal
  `pipeline-healthy` with `on_met: rest` and `on_limit: notify`. A wake
  that ends rests the task on its goal instead of completing it. The
  goal and the subscriptions are applied to the standing task at every
  firing (`Store.applyStandingDeclarations`), so a task filed before
  DT-S9 gets them without losing its handover.
- **On the judgement model.** The template names `Model: "judgement"`.
  Dispatch resolves it to `Config.JudgementModel` (`grain_config.judgement_model`)
  when the deployment set one, and otherwise to the strongest model its
  enabled connections offer by `model.JudgementModelRanking` (Claude Opus
  5.5 first, its medium-effort variant above anything weaker). If nothing
  resolves, the wake runs on the rotation and grain raises
  `durable.no-judgement-model` (info) so somebody is told. A model set
  on the schedule row beats the template's, as for any schedule.
- **One task, not one per firing.** The template is standing (`model.IsStandingTemplateID`). The
  first firing files the task. Every later firing *wakes* that same task:
  it clears the completion, stamps a retry, and adds a note ("Scheduled
  wake." or "Woken early: <why>."). The task keeps its conversation and
  its handover (`write_handover`), which is how it remembers what it
  filed, who it told to back off and what is pending. A wake only
  happens when the task is idle: its last run finished into
  awaiting_reply with nothing pending, or it rests on its goal
  (`Store.RestingTask`). If the task is queued or running,
  the wake is already happening and the firing is held. If it is parked
  on a question, failed or deferred, a person has to look first. If
  somebody closes the task, the next firing files a fresh one with no
  handover.
- **`nothing_needed` is "ack".** A wake that ends on `nothing_needed`
  leaves the task open to be woken again. Closing it would throw the
  handover away.
- **No target.** It pushes nothing and files no repairs; the base-branch
  reconciler owns those. A revert is a `propose_task` without
  `fixes_base_branch`. A hold is a dependency, a reorder or a dispatch
  pause, made through its tools.

## What the reconcilers do instead

| Former steward duty | Now |
|---|---|
| File one repair for a confirmed red | `fileBaseBranchRepair` |
| Park work against a red base, release it on green | `linkBrokenBaseDependents`, `ReleaseBaseRepairWaiters` |
| Retrigger an obvious flake | Batches and base reds already re-run. Task CI failing on a *known* flaky job alone (one that has failed and passed an uncached recheck before) is re-run once more per commit and job (`retriggerKnownFlake`), marked on the task so it is never repeated. |
| Re-run CI a person cancelled under a time-bound hold | `reconcileTaskCI` leaves a person's cancellation alone while the task is deferred until a time that has not come (`operatorCIHold`); once the hold expires the usual replacement re-runs it. |
| Retry or nudge stalled tasks | A task past its state's patience (`TaskStallNote`) raises `pipeline.task-stalled:<task>`, cleared when it moves on. |
| Nudge owed ports | An open port with no progress for 24h, or past its state's patience, raises `pipeline.port-stale:<task>`. |

## When it actually runs

### Quiet periods cost nothing

Before the schedule reconciler fires `system:pipeline-steward`, it asks
`pipelineStewardQuiet`. A quiet queue meets all three of these
conditions:

- every base branch grain watches has a green verdict, has no verdict
  yet, or has a run in flight. A confirmed or unconfirmed failure
  (`BaseBranchCI.Broken`) is not quiet, and neither is an errored or
  cancelled run;
- no task is stalled. That means no task has outlived its state's
  patience (`TaskStallNote`, the `stall` that `list_grain_tasks`
  reports), and the merge queue has not stopped driving or flagged any
  task (`MergeQueueBlockedAt`, `MergeQueueStalledAt`). The pipeline
  steward's own task is left out of this check;
- no merge batch has failed, and none has been running for more than two
  hours.

On a quiet queue the occurrence is dropped before any sandbox is spent.
The cadence moves on, `LastRunAt` stays put, and the schedule's skip
history records it as `quiet`. If the gate cannot read what it checks, it
fires anyway: a run that finds nothing costs less than a red branch
nobody saw.

### A confirmed red wakes it at once

`confirmBaseBranchCI` in `pkg/orchestrator/basebranchci.go` is where a
base-branch red gets confirmed, in every one of the ways it can be: the
confirmation re-run started by `startBaseCIConfirmation`, a failure with
no jobs to re-run, and a confirmation that never answered. Once the
repair task is filed, `confirmBaseBranchCI` calls `wakePipelineSteward`,
which fires the schedule out of band (`Store.FireScheduleWake`) with the
red named in the wake note. The cadence is left where it was. Because the
repair is filed first, the steward wakes to a branch that already has its
repair and does not file a second one. Any later way of confirming a red
(such as task 436's repeated red on a moving tip) wakes the steward as
long as it ends in `confirmBaseBranchCI`.

A schedule that has been switched off is not woken. A task that is
already running is not woken twice; its next wake picks up the red
itself.

## What it holds

| Grant | Why |
|---|---|
| `api:task-admin` | Comment on, reorder, retry and close tasks, add the `depends_on` that parks one behind a repair, retry merges, start and re-run grain CI |
| `api:dispatch-control` | Pause and resume dispatch, run a task now |
| `api:ci-read` | Read CI runs, logs, merge batches and base-branch gates |
| `self-debug` | `list_grain_tasks`, `read_grain_task` and the other task readers |
| `priority-dispatch` | Dispatched the way a console session is, so a congested pipeline can't starve it (below) |
| `durable` | No deadline or wait cap; rests on its goal between wakes |

`query_merge_queue` needs no grant, because every run has it.

Apart from `priority-dispatch` and `durable`, it gets nothing that only a
person may grant: no `merge:force` (it escalates a force-merge instead of
performing one), no `capabilities:admin`, `settings:write` or
`secrets:write`, and no `deployment:*`.
`TestThePipelineStewardHoldsTheDocumentedPresetsAndNothingDangerous`
enforces this list.

### Compact reads and the tool budget

A wake is meant to fit in context with no compaction
(docs/design/durable-tasks.md#DT-13). The tools it reads with answer
compactly by default: `read_pipeline_changes(after, limit)` is one line per
change since a cursor (task transitions, merge-queue moves, stalls,
base-branch verdicts, merge-batch results; `GET /api/pipeline/changes`) and
ends with the next cursor; `read_grain_task` answers in a ~1 KB `summary`
unless asked for `full` or `conversation`; `list_grain_tasks` filters by
`changed_since`, `stalled` and `kind`; `list_ci_runs` is one line per run
with `after` paging; `query_merge_queue` is `compact` with `changed_since`.

Its runs are under a per-wake tool budget (`model.UsesToolBudget`, the
mcpserver's `-tool-budget`): past 40 calls or 150 KB of tool output each
result says so, and past 60 calls or 300 KB each result asks it to write
its handover and end the wake. Ending in `resume_later()` rather than a
plain end arrives with DT-S4. `TestAStewardWakeFitsItsToolBudget` (pkg/mcp)
replays a wake's reads through these tools and holds them to the budget.

### Why it holds priority dispatch

The steward is meant to unstick a congested pipeline, but as ordinary
queued work it was starved by that congestion. On one deployment a
steward filed when `main` went red failed its first attempt at sandbox
setup. It then sat queued for more than 13 hours behind a full worker
pool, directors and merge-queue tasks. While it was queued every firing
was held, so nothing managed the pipeline all day.

`priority-dispatch` (`docs/capabilities.md`) gives it the admission
console sessions get. It starts with every worker busy, ahead of
merge-queue repairs and ordinary work. It passes any pause but an
upgrade's own hold, and it is never parked behind a red branch's
repair. A connection's concurrency limit doesn't hold it, and a setup
failure is retried within two minutes. It still needs room in the
sandbox budget. Only a person may grant `priority-dispatch` to any
other task. The steward's template carries it by construction, and a
standing task filed before the template did is given it at its next
wake.

### Releases are not its job

The steward does not cut, promote or port releases, and nothing in its
prompt asks it to. Releases are made from `main` by the daily release train
(`.grain/workflows/release-train.yml`,
`docs/release-candidate-management.md`). The train cuts and soaks release
candidates and stages a passing one as `vX.Y.0-pending`. A person approves
it. There is no `candidate` back-merge to watch and no owed port to chase:
`candidate` is a pointer the train force-moves, and no fix is ported to it.

It does not hold `releases:write`, and the `release-manager` API preset
(which carries `releases:write` and `schedules:write`) is not given to it.
Granting `api:releases:write` to the standing task would still not let it
make a release: the approve and reject routes
(`POST /api/repos/{owner}/{name}/pending-releases/{version}/approve|reject`)
refuse any caller that is not a person, whatever it holds. What the steward
does see is the train's own scheduled workflow. A red `release-train` run is
reviewed like any other scheduled workflow run, and a stalled train is the
release watchdog's to report (`.grain/templates/release-watchdog.yml`).

## What it is told

The prompt is `pipelineStewardBody`, written around the four rules of
`docs/design/durable-tasks.md#DT-12`:

1. **Work from deltas.** Read the handover's cursor and call
   `read_pipeline_changes(after: <cursor>)`. Read nothing else unless a
   change, or the note that woke it, names it.
2. **Re-read before acting or naming.** Call `read_grain_task(id,
   view: "summary")` immediately before acting on a task or naming it,
   and pass `expect` on every mutating call.
3. **Disagreement means stop.** A record that contradicts the handover
   is not acted on: it raises `agent.<task>:record-disagrees` with
   `notify_operator`, with both versions, and carries on with the rest.
4. **Short reports, every claim cited.** The closing report is at most
   15 lines (`model.PipelineStewardReportLines`), each claim ending with
   the id it was read from (`[task 832]`, `[ci run …]`, `[batch …]`).

Its actions are comment, reorder, retry, `notify_operator`,
`message_task` (to another task's agent; it never parks that task) and
`propose_task` for a revert -- at most five per wake
(`model.PipelineStewardMaxActions`). It never files a repair and never
re-raises a `pipeline.*` notification. Force-merges and reverts of a
person's change are escalated as `request.force-merge:<task>` and
`request.revert:<task or sha>`. It writes the cursor into its handover
before finishing, and never ends on `ask_question`, because a question
parks the task and nothing wakes it until somebody answers.
