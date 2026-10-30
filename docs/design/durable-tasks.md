# Durable tasks: runs that resume instead of ending

Status: **design, not implemented**. Filed by task 840 (origin task 831).
The follow-up tasks are listed in [The staged plan](#the-staged-plan) and
carry `Design-Item: docs/design/durable-tasks.md#DT-n` trailers, so the
design planner (`docs/design-docs.md`) finds them already open when this
doc merges and files nothing twice.

## Why

Some work in grain has to keep going until a goal is met or a person stops
it. Today a run is one unit of work, and every interruption ends it:

- **The pipeline steward** (`docs/pipeline-steward.md`) is a standing task
  woken every 30 minutes. On 2026-10-25, steward task 768 was filed at
  04:09 when `main` went red. Its first attempt failed in sandbox setup on
  a transient Kubernetes API timeout. It then sat queued for more than 13
  hours behind `max_workers = 3`, and every 30-minute firing was held
  because the task was "already queued". Nothing managed the pipeline all
  day.
- **Console sessions** (director and chat) are interrupted by daemon
  restarts (director task 828). They need a new run when a capability is
  added. They end on a model failure or usage limit. They are bounded by
  the 8h deadline and the 45m wait-credit cap. Pauses and red-base links
  held them (tasks 832 and 833 added session-only exemptions for these).
- **Goal-shaped work**, such as a base-branch repair, ends when the agent
  stops rather than when the goal is met. The one exception is
  `closeBaseRepairsOnGreen` (`pkg/orchestrator/baserepairlink.go`), which
  closes repairs when the branch goes green. This design generalises it.
- **Reaching a person** has two options: `ask_question`, which parks the
  task, or a closing comment nobody is notified about. There is no correct
  way to message *another* task's agent either. In task 839 the steward
  used `POST /api/tasks/{id}/messages`. That route is the *calling run's
  own* chat reply: `ui.Client.PostAgentMessage` stamps
  `PendingQuestionCommentID` on the task it names. Pointed at three other
  tasks, it parked all three on the operator.

The operator's direction: it is acceptable to spend a sandbox and an open
session for as long as the work needs, a task should not exit until its
work is done, and any task can be granted this, not only the steward.

## What exists to build on

| Piece | Where | What it already does |
|---|---|---|
| Streak-exempt outcomes | `model.PausedOutcome`, `CapacityOutcome`, `InterruptedOutcome`, `OOMOutcome` (`pkg/model/state.go`) | Four ways a run ends without counting against its task. Each is a suspension in all but name. |
| Session resume | `agent.RunConfig.ResumeSession`, `SuspendedPrompt`; `task_observation.suspended_session_*` | The next dispatch joins the previous run's CLI conversation and is told what changed. Falls back to the prompt plus conversation plus handover. |
| Handover | `write_handover`, `conversationHandoverBudget` | A standing summary carried into every later prompt. |
| Parking | `wait_for_task` (`park`, `resume_at`), `model.ParkedOutcome`, `pkg/model/wait_target.go` | An agent-chosen exit that re-queues the task when another task closes or a time passes. |
| Adoption | `RecoverRunHosts`, `ResumeAdoptedRun` (`pkg/orchestrator/adopt.go`), `SandboxReadopter` | A restarted daemon carries on a run whose CLI outlived the old process. |
| Priority dispatch | task 834 (`priority-dispatch` grant, `Task.HasPriorityDispatch`) | Outside `max_workers`, passes the pipeline pause, never linked behind a red base's repair. |
| Run-now survives | task 837; `task_run.run_now_at` and `Store.RestoreRunNow` (task-pause work) | A run-now request spent by a run that did no work is given back. |
| Liveness | `docs/design/task-liveness.md`, `livenessreasons.go` | Every wait has a named resource, a driver, and an escalation. |
| Alerts | `ui/src/alerts.js`, `AlertsButton.jsx` | One list of standing deployment warnings, by severity. |

Durable tasks are mostly a matter of naming these as one mechanism, closing
the gaps between them, and making the rule hold for every way a run stops.

## Decisions at a glance

1. **One grant, `durable`**, person-only. It implies `priority-dispatch`.
   Sessions, the steward and repairs that should not give up all get it
   the same way.
2. **A durable run has no deadline and no wait-credit cap.** An idle
   watchdog replaces the deadline. It counts against a separate
   `max_durable_runs` ceiling, not `max_workers`.
3. **Every short stop is a `Suspension`.** Suspensions are recorded in one
   table, never count toward the failure streak, never spend dispatch
   standing, and re-queue the task to resume from its CLI session, or
   from the conversation plus handover when the session is gone.
4. **`resume_later` is the agent's "not done, re-queue me" exit.**
   `wait_for_task(park)` and `resume_at` become two forms of it.
5. **Goals are declared on the task and checked by the daemon.** A run
   ending does not close a task that has a goal. A progress limit turns a
   loop into an operator notification.
6. **Notifications are first-class**: deduplicated by subject, cleared by
   their condition, and never park the sender. `notify_operator` tells a
   person; `message_task` tells another task's agent. They are separate
   tools and separate routes.
7. **The steward becomes one durable judgement task, woken by pipeline
   notifications** rather than polling every 30 minutes. Its mechanical
   duties move into daemon reconcilers. It is pinned to a strong model.
8. **A console session is a durable task whose goal is "a person closes
   it".** The session-only exemptions collapse into that grant.

## Long-lived runs {#DT-1}

### The grant

`durable` is a capability in the same family as task 834's
`priority-dispatch`. `model.IsPersonOnlyGrant` covers it: an agent cannot
grant it, a `propose_task` naming it is filed unapproved, and a template
carrying it cannot be named by an agent. A template may carry it by
construction, as the steward and console-session templates do.

`Task.IsDurable()` is true for a holder of the grant. Durable implies
priority dispatch, so `HasPriorityDispatch()` is true too. The
`priorityDispatchTaskSQL` twin gains the same clause.

### What a durable run is exempt from

- **No wall-clock deadline.** `capabilityDeadline` and the run's clock
  (`newRunClock`, `newAdoptedRunClock`) take `maxRunRuntime() = 0` to mean
  none. A lease that needs an expiry is minted with the deployment's
  longest lease and refreshed by the existing lease-renewal path, not
  bound to a run deadline. The prompt sentence about the budget
  (`run.go`, around `humanDuration(maxRuntime)`) says "this run has no
  deadline; if it goes idle for 60 minutes it is suspended and resumed".
- **No wait-credit cap.** `RunWaitCreditCap` does not apply. With no
  deadline there is nothing to credit back.
- **Outside `max_workers`.** It is admitted by the priority path from task
  834, and counted under a new `Config.MaxDurableRuns` (default 4). It is
  a safety ceiling, not a queue: if a durable task cannot start because
  the ceiling is full, grain raises the notification
  `durable.ceiling:<team>` (see [DT-5](#operator-notifications-dt-5))
  instead of letting the task wait silently. Its sandbox is charged to the
  sandbox ledger under the exception policy, as priority dispatch is.

### The idle watchdog replaces the deadline

A durable run that has done nothing for `durableIdleTimeout` (60 minutes)
is suspended with kind `idle` and resumed. "Nothing" means no transcript
output, no tool call starting or finishing, and not blocked inside a
declared wait such as `wait_for_checks`, `wait_for_task` or a chat wait
for a person. This catches a wedged CLI without bounding a run that is
still working. A session waiting for a person's next message is not a
live run at all (it rests; see [DT-7](#sessions-on-durable-tasks-dt-7)),
so the watchdog never fires on it.

### Adoption across restarts and upgrades

**Why 828 was not kept alive.** Adoption (`RecoverRunHosts` →
`ResumeAdoptedRun`) only resumes a run whose CLI outlived the daemon
process. That is true in two cases:

- a **claude** run in a run host (`-run-host-image`;
  `Config.RunHost`). Codex stays under `agent.LocalHost`
  (`docs/non-disruptive-upgrade.md`, open question 1);
- a CLI started through **hostd** (`hostdRuns`, `cliHostdHost`), when the
  framework's CLI setting is `host`.

Every other run, including every Antigravity run on this deployment's
default rotation, is a child of the daemon. Shutdown drains it and finishes
it as `interrupted` (`recoverRun`), and the task is re-queued without a
strike. A console session re-queued that way waited behind ordinary
dispatch, and it was also linked behind a red base's repair until task
833. The stage that implements this confirms the reason from 828's run
row (`task_run.detail`) and that upgrade's `AdoptionReport`. The design
does not depend on which of these it was.

**What DT-S3 found about 828.** The DT-S3 run could not read 828's row
or the report. It held no self-debug grant, and the deployment's state
repository (`grain/config`) was outside its read scope (the git proxy
answered 403). What the code shows for this deployment's backend,
kubernetes-granule, settles it anyway:

- **What is adoptable on `kubernetes-granule` depends on where the
  daemon runs.** In a cluster pod (`deploy/kubernetes/grain-granule.yaml`)
  there is no host executor (the daemon logs "host executor: none ...
  every agent CLI runs in the daemon pod's own container"), and
  `-run-host-image` builds a Kubernetes run-pod driver only under
  `-k8s-sandboxes` (`NewRunHost` falls back to docker otherwise). So
  every run in a `grain-granule` pod is a child of the daemon. On a
  release-container install with `grain hostd` and `kubernetes-granule`
  sandboxes (see `docs/non-disruptive-upgrade.md`, task 1244), `agy`
  runs detached through `hostd` and is adopted, while `claude` runs in
  the container (`agent.LocalHost`) and stops with it.
- **Why the `AdoptionReport` did not name 828.** Before task 1244, the
  report listed only runs that a run host or `hostd` was holding
  (`NewAdoptionReport` read only the adoption pass). Any `agent.LocalHost`
  daemon-child run was finished as `interrupted` by the drain, or by
  `RecoverOrphanedRuns` if the drain was cut off, before the adoption
  pass ran, so the report read `0 lost` for it (task 1244 added
  `Store.InterruptedRunsSince` to count those too).
- **The row should read** `outcome = interrupted`, and the task should
  carry the "Run … was interrupted: the grain daemon running it was
  stopped for an upgrade or a restart" note (`noteInterrupted`). A
  self-debug run can confirm this with
  `SELECT outcome, detail FROM task_run WHERE task_id = '828'`.

So the doc's reading holds: any run on `agent.LocalHost` (all runs in a
`grain-granule` pod until it has run pods, and container-run CLIs on a
container install) cannot be case 1 and crosses a restart as case 2,
while `pickAgent` / pool placement steers durable runs onto whichever
framework `hostd` or a run host can adopt when one is configured.

**The decision.** A durable task survives a restart in one of two ways,
and it always gets one of them:

1. **Adopted.** When the deployment has an adoptable host for the run's
   framework, a durable task is *placed* there. Model selection prefers an
   adoptable framework for a durable task, a preference that is weaker
   than a pinned model and stronger than the rotation. The run then
   crosses the restart through the existing `ResumeAdoptedRun` path, with
   no deadline to carry. *As built (DT-S3):* there is no `pickAgent`. The
   preference is in the agent pool (`pkg/daemon/adoptable.go`).
   `AgentChoice.Durable` comes from `Task.IsDurable`. `resolve` moves the
   default models on an adoptable framework (`Config.adoptableFrameworks`:
   claude under a run host, any CLI started through hostd) ahead of the
   rest, and `candidatesLocked` narrows the rotation to adoptable
   connections when any of them can take the run. A pinned model or
   connection is left alone. With nothing adoptable free, the run falls
   back to the ordinary order rather than waiting, and a restart is then
   case 2.
2. **Suspended and resumed at once.** Otherwise the restart is a
   suspension of kind `restart`. The new daemon's first cycle starts the
   task ahead of the queue (priority dispatch), with
   `ResumeSession` set to the suspended CLI session. That requires the
   framework's session store (`pkg/agent/sessionstore`) to be on storage
   that survives the restart. On Kubernetes that is the data claim, not
   the pod's filesystem. *As built (DT-S3):* the path was already
   complete once DT-S1 and DT-S2 landed. The drain, or
   `RecoverOrphanedRuns`, finishes the run as `interrupted`, and
   `FinishRun` records that as a `restart` suspension. `earlySession` has
   already recorded the session token. The task is durable, so it takes
   the priority path with no retry backoff, and `ResumeTokenFor` hands
   the token back. The stores were also already on the claim in both
   shipped manifests. agy's and codex's live under
   `-data-dir/state/agent-sessions`, and claude's in `HOME=/data/home`.
   A run pod's HOME is `-run-host-credential-dir`, which has to be on a
   `-k8s-run-host-volume` for the run pod to see it at all. So nothing
   had to move. DT-S3 pins the arrangement with
   `TestTheKubernetesManifestsKeepAgentSessionsOnTheDataClaim`, and adds
   a startup warning (`sessionStoresOffClaim`) for a deployment that has
   moved any of these stores off its claims.

What each sandbox backend needs for case 1, and what DT-S3 proved on
Kubernetes:

| Backend | `Readopt` | Durable runs |
|---|---|---|
| host (`HostSandboxes`) | yes | Nothing beyond the run host or hostd. |
| container (`ContainerSandboxes`) | yes | Run hosts are refused with container sandboxes (`CheckRunHostBackend`), so only hostd CLIs adopt. Durable tasks there use case 2. |
| granule (`GranuleSandboxes`) | yes, with `ReserveRoot` for its address | Descendant VMs are not readopted (by decision). Durable runs must not create them, or must accept losing them. |
| kubernetes (`K8sSandboxes`) | yes (`readoptPod`) | **Case 1 proven** on kind (DT-S3). With `-run-host-image=self`, `TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest` now runs a durable task. Its run pod carries no deadline label, a `kubectl rollout restart` leaves the run pod and the sandbox pod (same UID) in place, the next daemon pod adopts the run, and the same run ID ends `succeeded` and opens its pull request. The reachability gap from task 3858 is closed (`agent.RunConfig.K8sPod`, `grain mcpserver -k8s-pod`). **Case 2 proven** too: `TestDaemonRollResumesADurableRunItDidNotAdopt`, the same roll with claude as the daemon's child. |
| kubernetes-granule (`K8sGranuleSandboxes`) | yes (`readoptPod`, then `startForward` in the background) | **Case 2 only.** There are no run pods: `-run-host-image` builds its Kubernetes driver only under `-k8s-sandboxes`, and a pod has no host executor for hostd. So every run is the daemon's child, and nothing is adoptable for the placement to prefer. The roll test of case 2 (`TestDaemonRollResumesADurableRunItDidNotAdopt`) is the proof for this backend. It runs on `-k8s-sandboxes` because kind cannot host a packed granule guest. The path it exercises does not depend on the sandbox backend: the drain finishing a daemon child as `interrupted`, the `restart` suspension, the priority start on the next pod's first cycles, `--resume` into the session kept under `HOME=/data/home` on the claim, and the pull request. The roll test itself is still not run on a real kubernetes-granule cluster. Case 1 here needs run pods under `-k8s-granule-sandboxes` (a follow-up). |

**What the kind runs found on the way (DT-S3).** The roll test had
drifted from the daemon since task 3858 and was fixed: the
`-agent-framework` flag is gone, so the test seeds a claude
connection; sandbox pods need grain's PriorityClasses; task branches
follow the deployment's branch template, so the test fixes `Branch`; the
Upgrade call has to present the access code. One real bug was fixed in
the shipped manifests. Both probed `/api/config`, which a single-tenant
daemon answers with 401 to the kubelet, so the daemon pod never went
Ready and its liveness probe restarted it every few minutes. The probes
now use `/api/auth/access-code`
(`TestTheKubernetesProbesAreAnsweredWithoutTheAccessCode`). Not fixed:
on the Upgrade path (`GRAIN_RUNHOST_KIND_REGISTRY`) the health check
failed with "the check pod is Missing" in kind, so both rolls were
proven with `kubectl rollout restart`. The `stageKube` flush is still
unexercised on a cluster.

The setup failure that started 768's long wait (a transient API timeout
while building the pod) becomes a suspension of kind `setup` under
[DT-2](#resume-instead-of-end-dt-2). It retries with the 2-minute
backoff from task 834 and never counts toward
`MaxConsecutiveFailures`. After 5 setup suspensions in a row it raises
`durable.setup-failing:<task>` instead.

## Resume instead of end {#DT-2}

### The suspension record

A suspension is one row, written in the same transaction as the run's
finish:

```sql
CREATE TABLE task_suspension (
  id             INTEGER PRIMARY KEY,
  task_id        TEXT     NOT NULL,
  run_id         TEXT     NOT NULL,          -- the run that stopped
  kind           TEXT     NOT NULL,          -- model.SuspensionKind
  detail         TEXT     NOT NULL,
  resume_on      TEXT     NULL,              -- JSON wait target (model.WaitTarget), or NULL for "next cycle"
  resume_at      DATETIME NULL,              -- earliest restart
  standing       TEXT     NOT NULL,          -- JSON model.DispatchStanding snapshot (DT-3)
  model_hint     TEXT     NULL,              -- model or connection to resume on, for kind=model/usage-limit
  created_at     DATETIME NOT NULL,
  resumed_at     DATETIME NULL,
  resumed_run_id TEXT     NULL
);
CREATE INDEX task_suspension_open ON task_suspension(task_id) WHERE resumed_at IS NULL;
```

`task_run` gains `suspend_kind TEXT NULL`. The run's `outcome` stays what
it is today for the four existing kinds (`paused`, `capacity`,
`interrupted`, `oom`), so history and `FailureStreak` read the same. New
kinds use the new outcome `suspended`. `computeStreakForTask` and
`Store.FailureStreak` skip every row whose `suspend_kind` is set, which
replaces their list of exempt outcome strings with one predicate.

`model.SuspensionKind` is a closed set, declared in one file and read by a
test the way `livenessreasons_internal_test.go` reads its reasons:

| Kind | Raised by | Applies to | Resumes |
|---|---|---|---|
| `deadline` | the run clock | non-durable tasks with `resume_on_deadline` (below) | next cycle, CLI session |
| `idle` | the idle watchdog | durable | next cycle, CLI session |
| `restart` | shutdown drain or `recoverRun`, when not adopted (today `interrupted`) | every task | the first cycle of the new daemon |
| `setup` | sandbox or setup failure | durable; ordinary tasks keep today's strike rule | 2-minute backoff; notification after 5 in a row |
| `oom` | today's OOM path | every task | as today (bigger sandbox on the second) |
| `usage-limit` | `orchestrator.Pause` (today `paused`) | every task | on another connection of the same model class if one is free, otherwise when the window resets |
| `capacity` | `agent.CapacityError` | every task | the next model in the task's rotation, or the same one after backoff |
| `model` | the agent CLI failing for a model-side reason that is neither of the above (a 5xx streak, a refused session, a crash of the CLI) | durable | the next model in the rotation; notification after 3 different models fail |
| `capability` | a grant added or removed while the run is live | every task | after the sandbox is rebuilt with the new grants, CLI session |
| `context` | context exhaustion (`promptsize`, the CLI's "prompt is too long", or a compaction the framework reports) | every task | a fresh CLI session from the conversation plus a handover written at the stop |
| `operator-pause` | the task-pause work (`task_pause`) | every task | when the operator resumes |
| `requested` | the agent's `resume_later` | every task | its `resume_on` / `resume_at` |

A run that ends for a reason not in the table is not a suspension. It is a
success, or a failure judged as today.

### Continuity

What the next run gets, best first:

1. **The CLI session** (`ResumeSession` plus `SuspendedPrompt`) whenever
   the framework recorded one, the model is the same family, and the kind
   is not `context`. This is the existing path. The only change is that
   every suspension kind sets `suspended_session_*`, not only the ones that
   do today.
2. **The conversation plus handover** when the session is gone, the model
   changed, or the kind is `context`. For `context`, the stopping run is
   given one last turn (the `handoverRequest` text) to write its handover
   before it is cut. If it does not, the handover is whatever it wrote
   last.

The prompt always opens with one line naming the suspension: "Your
previous run was suspended (kind, detail) at T and this is a fresh run.
Anything you did not push is gone." `SuspendedPreamble` already says the
second half.

### Capability changes mid-task

Today a grant added to a running task reaches only the next run. Under
this design, `Store.SetTaskGrants` on a task with a live run sends the
run an addendum ("a capability changed; commit, push and write your
handover; this run will be restarted with the new grants in 2 minutes").
After that grace period, which reuses the pause-grace reconciler from the
task-pause work, the run is cancelled with kind `capability` and the task
restarts with a sandbox built for its new grants. Grants that need no
sandbox change are applied in place instead: an `api:*` preset is checked
per request by the agent-API gate, so it takes effect at once with no
restart. `capabilityNeedsSandbox(old, new)` decides which, and it is the
only new judgement here.

### The agent's own exit: `resume_later` {#DT-9}

```
resume_later(reason: string,
             when?: { ci?: "<run id or 'my-branch'>",
                      task?: "<id>" | ["<id>", ...], until?: "closed"|"merged",
                      at?: "<RFC 3339 or duration>",
                      message?: true })
```

It ends the run as a `requested` suspension. The task is re-queued when
any condition in `when` is met. With no `when`, it is re-queued on the
next cycle, which suits a durable task splitting long work into steps.
`message: true` adds "or when anybody posts to this task".

How it relates to what exists:

- `wait_for_task(park: true)` is `resume_later(when: {task})`.
- `wait_for_task(resume_at)` is `resume_later(when: {at})`.

Both tools keep their names and arguments, because agents and templates
use them. Their implementation becomes one call into `Store.Suspend`, and
`model.ParkedOutcome` becomes `suspend_kind = requested`. The new
conditions are `ci` (a grain CI verdict on a run or on the task's branch)
and `message`.

`resume_later` is offered to every task, not only durable ones. On a task
with no goal and no grant it is exactly today's park. Its prompt
description says it is how to stop without completing, and that
`comment_on_issue` or `nothing_needed` is how to finish.

### `resume_on_deadline` for ordinary tasks

A non-durable task that runs out of wall clock fails today. A template or
task may set `resume_on_deadline: true` (a new `task.resume_on_deadline`
flag, defaulting to false) so that its deadline becomes a `deadline`
suspension instead. It is resumed at most `MaxDeadlineResumes` (3) times
before the deadline counts as a failure again. This is not the durable
grant. It is the cheap form of "not done, keep going" for implementation
tasks that are merely large.

## A suspension never loses dispatch standing {#DT-3}

**The invariant.** A task re-queued after a suspension has exactly the
dispatch standing it had when the suspended run started. It is started
ahead of the queue on the next cycle it is eligible for, not put back at
its ordinary backlog position.

Standing is five things, captured as `model.DispatchStanding` at
`startRun`:

| Standing | Where it lives | How a suspension keeps it |
|---|---|---|
| run now | `task_observation.run_now_at` | `startRun` already copies it onto `task_run.run_now_at` (task 837 / task-pause work). `Store.Suspend` restores it in the same transaction unless a newer request exists (`Store.RestoreRunNow`), for **every** suspension kind. |
| priority dispatch / durable | the task's grants | Derived from grants, never spent. A suspension must not touch grants. A `capability` suspension that *removes* the grant is the one case where standing changes, because a person changed it. |
| front of backlog | `order_key` | Never written by a run. `Store.Suspend` must not write it. |
| fixes base branch | `fixes_base_branch` mark | Never written by a run. Same rule. |
| session | console-session tag; under DT-7, the durable grant plus the `person-closes` goal | Derived, never spent. |

`task_suspension.standing` records the snapshot. It is not used to restore
anything except `run_now_at`. It exists so the test below, and anyone
reading a task's history, can compare what the task had with what it got.

**How it is tested.**

- `TestEverySuspensionKindKeepsDispatchStanding`
  (`pkg/orchestrator`, internal) is table-driven over the cross product of
  every `model.SuspensionKind`, read from the file that declares the
  kinds so that a new kind with no row fails, and every standing in the
  table above. For each pair it:
  1. starts the task through `dispatch.Cycle` with a full worker pool and
     a backlog of older tasks;
  2. ends the run with that kind through the real finish path
     (`recordRunEnd`, or `Store.Suspend` for the agent's own exit);
  3. runs one more cycle and asserts that the task was started first,
     that `run_now_at`, `order_key` and the grants are byte-for-byte what
     they were, and that `FailureStreak` did not move.
- `TestSuspensionKindsAreClassified` fails CI when a run outcome or a
  `recoverRun` detail can be produced that is neither in
  `SuspensionKind` nor a counted failure or success. Every new way to
  stop has to declare which it is.
- The four outcomes task 837 fixes (setup-failed, paused, capacity,
  interrupted, oom) are rows of the same table, so 837's own tests become
  a subset of this one rather than a second copy of the rule.

## Goal-based completion {#DT-4}

### Declaring a goal

```sql
CREATE TABLE task_goal (
  task_id          TEXT PRIMARY KEY,
  kind             TEXT NOT NULL,      -- base-green | pr-merged | tasks-closed | person-closes | pipeline-healthy
  spec             TEXT NOT NULL,      -- JSON, per kind (below)
  on_met           TEXT NOT NULL,      -- close | rest
  declared_by      TEXT NOT NULL,      -- principal
  declared_at      DATETIME NOT NULL,
  met_at           DATETIME NULL,
  progress_at      DATETIME NOT NULL,  -- last time progress was observed
  resumes_since_progress INTEGER NOT NULL DEFAULT 0,
  max_resumes      INTEGER NOT NULL,   -- per-goal limit, defaulted by kind
  max_stall        INTEGER NOT NULL,   -- seconds, per-goal limit
  on_limit         TEXT NOT NULL,      -- hold | notify
  limit_notified_at DATETIME NULL
);
```

| Kind | Spec | Met when |
|---|---|---|
| `base-green` | `{repo, branch, publishing: bool}` | `BaseBranchCI` is green at the tip and, with `publishing`, the branch's release row has published since (`baseBranchPublishingOf` finds nothing). This generalises `closeBaseRepairsOnGreen`, which becomes the goal check for repairs filed with `fixes_base_branch`. |
| `pr-merged` | `{task}` or `{repo, number}` | The pull request merged. |
| `tasks-closed` | `{tasks: [...], until: closed\|merged}` | Every named task reached it. The same `WaitTarget` evaluation `wait_for_task` uses. |
| `person-closes` | `{}` | Never met by the daemon. Only a person closing the task ends it. |
| `pipeline-healthy` | `{repos?: [...]}` | `pipelineStewardQuiet` is true and no notification in the `pipeline.*` family is open. |

A goal is set by the template (`Template.Goal`), by `propose_task`
(`goal`), by a person on the task page, or by grain itself: every
`fixes_base_branch` repair gets `base-green` with `on_met: close`.

### What an ending means for a goal task

- **The agent ends its run** (`comment_on_issue`, a plain finish, or
  `nothing_needed`) while the goal is unmet: the task does not complete.
  It is suspended with kind `requested` and `resume_on` set to the goal.
  It rests until the goal check, a message, or a progress-limit
  notification moves it. The closing comment is posted as an ordinary
  comment.
- **`nothing_needed`** on a goal task means "nothing to do *now*". The
  only things that close a goal task are the goal being met with
  `on_met: close`, or a person.
- **Goal met**: `reconcileTaskGoals`, a new arm of the cycle, closes the
  task with outcome `goal-met` and a note naming the evidence ("main
  green at abc123, release r42 published"), or puts it to rest for
  `on_met: rest`. A live run is told by addendum and given the pause
  grace to finish before it is cancelled.

Liveness: a resting goal task is "waiting for a named resource", namely
the goal's condition. Its driver is `reconcileTaskGoals` and its patience
is the progress limit. It gets a row in `livenessreasons.go`
(`awaiting-goal/<kind>`) like every other wait.

### Progress and its limits

A run counts as progress when, between its start and end, any of these
happened:

- it pushed a commit;
- it filed, closed, merged, reordered or retried a task;
- the goal's own measure moved. For `base-green` that is the failing job
  set changing or the tip moving. For `tasks-closed` it is the count of
  closed tasks. For `pipeline-healthy` it is the number of open
  `pipeline.*` notifications;
- a person posted to the task.

Each resume without progress increments `resumes_since_progress`. When it
reaches `max_resumes`, or `now - progress_at` exceeds `max_stall`, grain
raises `task.no-progress:<task>` (warning) with the last three runs'
summaries. Then:

- `on_limit: hold`, the default, stops resuming. The task rests on the
  notification. Acting on it (Continue, Close, Change model, Reply)
  resets the counter.
- `on_limit: notify` raises the notification and keeps going with
  exponential backoff (doubling from the suspension's own delay, capped at
  6h). The steward uses this, because a pipeline that stays red is
  exactly when it should keep watching.

Defaults by kind: `base-green` 6 resumes or 12h; `pr-merged` and
`tasks-closed` 6 resumes or 24h; `pipeline-healthy` 4 resumes or 6h with
`notify`; `person-closes` has no limit, because a session that is not
being talked to does not resume.

## Operator notifications {#DT-5}

### The primitive

```sql
CREATE TABLE notification (
  id            INTEGER PRIMARY KEY,
  subject       TEXT NOT NULL,          -- dedup key, e.g. "base-branch.red:bwsalmon/grain@main"
  family        TEXT NOT NULL,          -- prefix before ':' ("base-branch.red"), for subscriptions
  severity      TEXT NOT NULL,          -- critical | warning | info (alerts.js SEVERITY_RANK)
  title         TEXT NOT NULL,
  body          TEXT NOT NULL,          -- markdown, root-relative links
  task_id       TEXT NULL,              -- the task it is about, if any
  repo          TEXT NULL,
  raised_by     TEXT NOT NULL,          -- principal: reconciler name, agent on behalf of task N, person
  raised_at     DATETIME NOT NULL,      -- first raise of this open instance
  last_raised_at DATETIME NOT NULL,
  raise_count   INTEGER NOT NULL DEFAULT 1,
  clears_when   TEXT NULL,              -- JSON condition (below), NULL = cleared by its raiser or a person
  cleared_at    DATETIME NULL,
  cleared_by    TEXT NULL,              -- "condition", a reconciler, or a person
  acked_at      DATETIME NULL,
  acked_by      TEXT NULL,
  actions       TEXT NULL,              -- JSON list of offered actions (investigate, open task, retry merge, ...)
  investigation_task_id TEXT NULL,
  delivered     TEXT NULL               -- JSON {director: comment id, external: [...]}
);
CREATE UNIQUE INDEX notification_open_subject ON notification(subject) WHERE cleared_at IS NULL;
```

The rules:

- **Raise is an upsert on the open subject.** Raising an open subject
  bumps `last_raised_at` and `raise_count`, and may raise its severity
  but never lower it. Nobody sees it twice.
- **It clears itself.** An internal raiser clears it when its condition
  goes away (`Store.ClearNotification(subject, by)`). A notification with
  `clears_when` is checked each cycle by `reconcileNotifications`. The
  conditions are a `WaitTarget` (a task closed or merged, a CI verdict),
  `base-green {repo, branch}`, or `ttl`. A cleared notification stays
  readable in history, and a later raise of the same subject is a new
  instance.
- **Acknowledge is not clear.** Acknowledging hides it from the Alerts
  count. It stays in the inbox list until its condition clears, and a
  raise after the ack re-surfaces it only if the severity rose.
- **It never parks the sender.** Raising writes no
  `PendingQuestionCommentID` and changes no task state. The only way a
  notification moves a task is the progress-limit `hold` above, where the
  task rests on its *own* notification.

### Who can raise one

- **Agents**, through the tool `notify_operator(subject, title, body,
  severity?, task?, clears_when?)`. Every run has it with no grant. The
  subject an agent gives is namespaced as `agent.<task>:<subject>` unless
  it holds `api:task-admin`, so a run cannot clear or overwrite a
  reconciler's notification. Agents are limited to 10 raises per run and
  20 open notifications per task. Past that the call is refused with a
  sentence saying so. An agent may not raise `critical` without
  `api:task-admin`.
- **Reconcilers and health checks**, through `Store.RaiseNotification`.
- **The API**: `POST /api/notifications` (raise),
  `GET /api/notifications?open=1&after=<id>&family=<f>`,
  `POST /api/notifications/{id}/ack`, `/clear`, and `/investigate`.
  Raising and clearing need `notifications:write`, which
  `api:task-admin` includes. Acknowledging needs a person.

### Where they show

- **Alerts** (`AlertsButton.jsx`): open, unacknowledged notifications join
  `alerts.js`'s list as one more source, with the same severities. The
  standing alerts that are really conditions (usage-limit pauses, the
  reconcile loop stopping) can migrate onto notifications over time. That
  is not required by this design.
- **Inbox**: a "Notifications" section above the task rows, one row per
  open notification, with its actions (Acknowledge, Investigate, Open
  task, plus kind-specific ones such as Retry merge).
- **Director chat**: when the deployment has a director session, every
  new `warning` or `critical` notification is posted into its
  conversation as a grain-authored comment (actor
  `model.ConsoleSessionPrincipal`, not a person, so it does not count as
  a message the director must answer). A `critical` one also wakes a
  resting director. `delivered.director` records the comment so a raise
  is delivered once per instance.
- **External channels**: an interface only, for now. `NotificationSink`
  has a `Deliver(ctx, Notification) error` method, configured in
  `grain_config.notification_sinks` (webhook URL, minimum severity). It
  delivers once per instance, with retries. The first sink is a separate
  task, not part of this design's stages.

### Investigate on demand

Investigate (button or API) files one task per notification instance from
the template `system:investigate-notification`. Its body is the
notification, its links and its raiser. It is filed approved, as the
operator's own action, holding the notification's grants profile
(`self-debug` plus `api:ci-read`). It sets `investigation_task_id`, so a
second click opens the same task. "Open in chat" instead posts the
notification into the director session and focuses it. A notification
family may name a durable task that *subscribes* to it (below); that is
how the steward is woken.

### Subscriptions

A durable task may subscribe to notification families
(`task_notification_subscription(task_id, family)`, set by its template).
A raise in a subscribed family re-queues a resting subscriber, as a
message would, and is delivered as an addendum to a live one. This is the
wake source that replaces the steward's 30-minute schedule.

### Existing escalations, mapped

| Today | Becomes | Raised and cleared by |
|---|---|---|
| A red base branch, confirmed (`confirmBaseBranchCI` → `fileBaseBranchRepair`, `wakePipelineSteward`) | `pipeline.base-red:<repo>@<branch>`, warning, `actions: [open repair]` | `confirmBaseBranchCI` raises it. The green verdict that runs `closeBaseRepairsOnGreen` clears it. |
| A red base with nothing publishing (`baseBranchPublishing` in deployment health) | `pipeline.not-publishing:<repo>@<branch>`, warning, critical after 24h | The deployment-health reader's finding is computed by a reconciler, which raises it and clears it when a release publishes. The health page keeps reading the same finding. |
| A merge-queue entry past its patience escalated to `person` (`escalateStalledEntry`) | `pipeline.merge-stalled:<task>`, warning, `actions: [retry merge, open task]` | `escalateStalledEntry` raises it. It clears when the entry leaves the stalled state. The inbox row stays, so nothing is lost while both exist. |
| A failed or long-running merge batch | `pipeline.batch:<id>` | The merge-queue batch driver. `<id>` is `<repo>@<branch>`, because a branch has one batch at a time. It is raised when a batch is abandoned without an answer, except when it was abandoned because batching was switched off, the base moved, or the red was inherited, and when a run passes half of `MaxMergeBatchAge`. The next batch on the branch to reach a verdict clears it, with a 24h ttl as a backstop. |
| A force-merge request (the steward commented for a human) | `request.force-merge:<task>`, warning, `actions: [open task]` (the force-merge control is on the task page and needs `merge:force`) | The steward or any `api:task-admin` holder via `notify_operator`. It clears when the task merges or closes. |
| A revert of a person's change | `request.revert:<task or sha>` | The same. It clears when the revert merges or a person dismisses it. |
| Repeated repair failure | `pipeline.repair-failing:<task>` | The goal progress limit on the repair. Until goals land (DT-S5) the base-branch reconciler applies `base-green`'s defaults to the repair itself: 6 runs or 12h open with the branch still confirmed red. It clears when the repair closes or the branch goes green. |
| A durable task not progressing, not starting, or failing setup | `task.no-progress:<task>`, `durable.ceiling:<team>`, `durable.setup-failing:<task>` | DT-1 and DT-4. |

`ask_question` keeps its meaning: *this* task cannot continue without an
answer, and it parks. `notify_operator` means a person should know or
act, and the task carries on. The prompt says which to use for which.

### Agent-to-agent messages: `message_task` {#DT-10}

"Tell the operator" and "tell another task's agent" are separate tools:

- `message_task(task, body)` → `POST /api/tasks/{id}/notes`. It is a
  comment on the target task authored as the sender's agent principal
  (`Actor: {Kind: agent, ID: <sender task>}`). It never sets
  `PendingQuestionCommentID` and never changes the target's state. It is
  delivered as an addendum to the target's live run, re-queues a resting
  durable target as a message would, and is otherwise shown in the
  target's next prompt. It needs `api:task-admin`, or a sender that is the
  target's parent or child, so that an arbitrary run cannot steer
  arbitrary tasks.
- `POST /api/tasks/{id}/messages` stays what it is: the calling run's
  own reply in its own chat. If task 839 has not already done so, it is
  restricted to the caller's own task (the sandbox token's task id) and
  refuses any other id with a 403 that names `message_task`. Its
  description in `agent_api_tools.go`, "Post a message to a task's running
  agent", is the misreading that caused task 839 and is rewritten.

## Reworking the steward {#DT-6}

### Mechanical duties move into reconcilers

| Steward duty (`pipelineStewardBody`) | Reconciler | State today |
|---|---|---|
| File exactly one repair for a confirmed red | `fileBaseBranchRepair` | Done; the steward's step 2 is already redundant. |
| Park work that keeps failing against a red base; lift holds on green | `linkBrokenBaseDependents`, `ReleaseBaseRepairWaiters` | Done for links. Lifting the steward's *own* manual holds (reorders, pauses) is the gap: under this design the steward does not make those. |
| Retrigger obvious flakes | `noteBatchFlake` (batches); `startBaseCIConfirmation` (base) | Batches and base reds re-run already. Task CI flakes, a known flaky job failing alone, get a reconciler arm that retriggers once per fingerprint (`retriggerKnownFlake`). |
| Retry stalled tasks and held batches | liveness escalation `agent` arm (`stallEscalationDue`, `escalateStalledEntry`) | Covers queue entries. Task-side stalls get the same escalation table (task 718's follow-on). |
| Re-run CI a person cancelled with an expired time-bound hold | a new arm of `reconcileTaskCI` | Missing. |
| Nudge owed ports | `pkg/ports` owed-ports reconciler | Ports are filed automatically; a stale port (no progress past patience) raises `pipeline.port-stale:<task>`. |
| Escalate force-merges, reverts and repeated repair failures | notifications (DT-5) | New. |

After this, nothing the steward does is mechanical. What remains is
judgement:

- naming a suspect merge and deciding whether a revert is warranted;
- deciding to deprioritise or pause work beyond the automatic links;
- reading a stalled pipeline and explaining *why* in a notification a
  person can act on.

### Decision: one durable task, woken by notifications

The steward stays **one standing task**, now **durable** with goal
`pipeline-healthy`, `on_met: rest`, `on_limit: notify`. It subscribes to
the `pipeline.*` families. It is woken by a notification, not polled.
The 30-minute schedule becomes a 6-hourly safety wake, which keeps the
schedule row, the precondition gate and `FireScheduleWake` exactly as
they are and changes only the interval.

Why not "launch an agent from each notification"? The steward's value is
continuity: what it filed, which holds it placed, what a person told it.
One task with a handover keeps that, and a fresh task per notification
re-learns it each time. The Investigate action from DT-5 remains for a
person who wants a one-off look at a specific notification.

With durable dispatch it is never held behind `max_workers` again. With
`setup` suspensions it retries in minutes. The 768 failure mode is closed
twice: by admission and by the setup rule.

The steward's actions shrink to: comment, reorder, retry, `notify_operator`,
`message_task`, and `propose_task` (for a revert, never with
`fixes_base_branch`, since the reconciler owns repairs).
`PipelineStewardMaxActions` stays at 5 per wake.

## Sessions on durable tasks {#DT-7}

A console session (director or chat) is a task with the `durable` grant
and goal `person-closes`. `reconcileConsoleSessions` files it that way,
and between exchanges it rests (suspension kind `requested`,
`resume_on: {message: true}`). That replaces each session-specific rule:

| Session-only rule today | Becomes |
|---|---|
| Never done: a completed session is un-completed and parked (`reconcileConsoleSessions`) | The `person-closes` goal: an ending never completes it, and it rests on messages. |
| Never retired for failing | Suspensions do not strike. A genuine failure streak on a durable task raises `task.no-progress` instead of retiring it. |
| Started whenever ready, outside `max_workers` | `durable` implies `priority-dispatch` (task 834). |
| Passes the pipeline pause (`SessionsPassPause` → `PriorityPassesPause`, task 834) | The same predicate. |
| Never linked behind a red base's repair (task 833, `waitOnBaseRepair` refusing `IsConsoleSession`) | `waitOnBaseRepair` refuses `HasPriorityDispatch`, which task 834 already moves it to. |
| 8h deadline, 45m wait cap | None (DT-1). |
| Interrupted by restart (828) | Adopted, or a `restart` suspension resumed on the first cycle (DT-1). |
| A capability added needs a new run | A `capability` suspension applied automatically, or in place for API presets (DT-2). |
| A model failure or usage limit ends it | `model`, `capacity` and `usage-limit` suspensions resume on another model or connection (DT-2). |

`Task.IsConsoleSession` remains as a *display* predicate (the chat UI, the
session tag). No dispatch, liveness or merge-queue code asks it once the
DT-S10 stage lands. `TestNoDispatchCodeAsksIsConsoleSession`, a grep test
in the style of the liveness tests, keeps it that way.

## Steward reliability {#DT-8}

### What went wrong on 768's first wake

Wake 1 (2026-10-25 17:23–17:43) made 154 tool calls (55 `read_grain_task`,
39 `list_ci_runs`, 12 `list_grain_tasks`), about 1.1 MB of tool output, on
the default Antigravity rotation. Its report described the operator's
proposals 832, 833 and 834 as "main→candidate port tasks", misdescribed
836, and reordered them on that basis. Its access was not the cause: it
holds more grants than a director session. The causes were a weak model
for the job, a sweep-everything procedure that overflowed context, and
reports written from memory.

### Model choice {#DT-11}

- The steward, and any template that is a durable judgement agent, is
  **pinned** through the template's `Model` (and so the schedule's,
  `Schedule.Model`), never the rotation.
- The model is **Claude Opus 5.5 at high effort** where the deployment has
  a connection for it. On this deployment's Antigravity backend that is an
  `opus-5.5` entry. If only `opus-5.5-medium` exists, use that rather
  than the `gemini-pro-coordinator` half of the rotation.
- **The template sets it by default, symbolically.** A concrete model id
  in grain's own template would not resolve on every deployment. So the
  system template names `Model: "judgement"`, a new symbolic model class
  resolved through `Config.JudgementModel`. When that is unset, it
  resolves to the strongest model the deployment's connections offer, by
  a static ranking in `pkg/model` (Opus 5.5 first). A deployment that
  resolves nothing raises `durable.no-judgement-model` and runs the
  steward on the rotation, so it still runs, but somebody is told.
- On this deployment the pin is a change to grain/config (the
  `system:pipeline-steward` schedule's `model`, or `JudgementModel` once
  it exists). It is not made from this repository.

### Prompt and procedure {#DT-12}

`pipelineStewardBody` is rewritten around four rules. Each is enforced by
a tool where it can be, and stated in the prompt where it cannot:

1. **Work from deltas.** Start from the handover's cursor and read
   `read_pipeline_changes(after: <cursor>)` (below). Read nothing else
   unless a change names it. The cursor is written into the handover at
   the end of the wake.
2. **Re-read before acting or naming.** Immediately before acting on a
   task, or naming it in a report, call `read_grain_task(id,
   view: "summary")`. Never describe a task from memory or from the
   handover. Mutating tools take an `expect` argument (`{state, title_prefix,
   kind}`) and refuse when the record disagrees, so acting on a stale
   belief fails loudly.
3. **Short reports, every claim cited.** The report is at most 15 lines.
   Each claim ends with the id it was read from, as `[task 832]`,
   `[ci run …]` or `[batch …]`. A claim with nothing to cite is not made.
4. **Disagreement means stop.** If a record contradicts the handover or
   what the agent believed, it does not act on that object. It raises
   `agent.<task>:record-disagrees` with both versions and continues with
   the rest.

### Bounded context {#DT-13}

The target is that a wake fits in context with no compaction: at most 40
tool calls and 150 KB of tool output, against 154 calls and 1.1 MB on wake 1.

- **`read_pipeline_changes(after?, limit?)`**, a new agent tool, backed by
  `GET /api/pipeline/changes`. It returns one compact line per event since
  the cursor: task state transitions, base-branch verdicts, merge-batch
  results, stalls (`TaskStallNote`, `MergeQueueStalledAt`,
  `MergeQueueBlockedAt`), and notifications raised or cleared. Each line
  is id, kind, from→to, and at. It ends with the next cursor. It reads the
  existing event sources, and the cursor is an `(at, seq)` pair.
- **`read_grain_task(view: "summary")`**, the new default: state, title,
  kind tags (port, repair, session, proposal, durable, imported), base, depends_on,
  stall, last three events and the handover's first lines, in about 1 KB.
  `view: "full"` is today's output. `view: "conversation", after: <comment
  id>` pages the conversation.
- **`list_grain_tasks(changed_since, stalled, kind)`**: filters that make
  the sweep unnecessary. Output stays one line per task.
- **`list_ci_runs`**: `summary` by default (one line per run: id, ref,
  verdict, failing jobs), plus `after` paging. The notes' advice to
  always filter becomes the default shape.
- **`query_merge_queue(changed_since)`**, compact by default.
- **A per-run tool budget for durable judgement tasks**: when a wake
  passes 40 calls or 150 KB, each tool result carries a line saying so.
  At 60 calls or 300 KB, the next result asks it to write its handover
  and `resume_later()`. The wake then continues as the next resumable
  step (DT-2) with a fresh context, not as one compacted run.

### Verification {#DT-14}

Two rates, measured per wake, kept over time, with a notification when
either regresses.

**Tool misuse** comes from `task_run_tool`, which already records per
run and tool the calls, errors, elisions and bytes. A new
`read_grain_metrics` section, `agents`, groups it by template:

- calls and result bytes per wake (p50, p95);
- elided results per wake;
- re-reads, meaning the same id read more than twice in a wake. This needs
  the call log below.

**Confabulation** needs to know what was read. A new
`task_run_api_call(run_id, seq, tool, target_ids, result_bytes, at)`
records each agent-API call's target ids, but not the results. A
deterministic checker, `stewardReportCheck`, runs when a steward wake
posts its report and computes:

- *uncited claims*: report lines with no `[… id]` citation;
- *unread citations*: cited ids with no read of that id in the run's call
  log before the report;
- *attribute mismatches*: for each cited task, whether the words in its
  line contradict the record's kind tags or state. For example, "port"
  for a task with no port tag, or "merged" for an open one. This uses a
  small fixed vocabulary of states and kinds, not free-text judgement.

The results are stored as `task_run_report_check(run_id, claims, uncited,
unread, mismatched)` and shown in the `agents` metrics section. A wake
with any mismatch raises `agent.steward:report-mismatch` with the lines.
A 7-day rolling rate above 5% of claims raises
`pipeline.steward-regression`.

A replay fixture built from 768's wake-1 report and the records of
832/833/834/836 is a test: the checker must flag the "port task" lines.
A weekly sampled judgement audit (one wake in ten reviewed against the
record by a separate pinned-model task) catches what the vocabulary
cannot. It is a schedule under the same `judgement` model, and its
findings are rows in the same table.

## Store and schema changes (summary)

| Change | Stage |
|---|---|
| `task_suspension` table; `task_run.suspend_kind`; `model.SuspensionKind`; `model.DispatchStanding` | DT-S1 |
| `durable` grant; `Config.MaxDurableRuns`; `Config.DurableIdleTimeout` | DT-S2 |
| Session store on persistent storage on Kubernetes | DT-S3 |
| `task.resume_on_deadline`; `resume_later` tool; `model.ResumeOn` (the `task_suspension.resume_on` wait target) with `ci`, `message`; `POST /api/tasks/{id}/resume-later` | DT-S4 |
| `task_goal` table; `Template.Goal`; `propose_task.goal` | DT-S5 |
| `notification`, `task_notification_subscription` tables; `NotificationSink` interface; `notifications:write` permission; `POST /api/tasks/{id}/notes` | DT-S6 |
| `task_run_api_call`, `task_run_report_check`; `GET /api/pipeline/changes` | DT-S8, DT-S11 |
| `Config.JudgementModel`; symbolic model `judgement` | DT-S9 |

Each table is new, so older stores gain empty tables. The one column added
to an existing table, `task_run.suspend_kind`, is nullable and backfilled
from `outcome` for the four existing kinds by an `ensureColumn` rung and
an `UPDATE`. Each table is also exported to the state repository the way
its neighbours are, except `task_run_api_call`, which is high volume,
local, and pruned after 30 days.

## Agent-facing tools and exit modes (summary)

| Tool | Who | Effect |
|---|---|---|
| `comment_on_issue` / plain finish | all | Completes an ordinary task. On a goal task, a `requested` suspension until the goal or a message. |
| `nothing_needed` | all | Closes an ordinary task. On a goal task, rests it ("nothing now"). |
| `ask_question` | all | Parks on a person's answer. Unchanged. |
| `resume_later(reason, when?)` | all | Suspends and re-queues on its condition. `wait_for_task(park)` and `resume_at` are forms of it. |
| `notify_operator(...)` | all (critical needs task-admin) | Raises or bumps a notification and does not park. |
| `message_task(task, body)` | task-admin, or parent/child | A note to another task's agent. Never parks it. |
| `read_pipeline_changes(after)` | self-debug | The delta feed. |
| `write_handover` | all | Unchanged. It is also requested at a `context` suspension. |

## UI surfaces

- **Task page**: a "Suspended" badge with kind and resume condition while
  a task rests or waits to resume. A history strip lists suspensions
  beside runs. The goal is shown with its live check ("main: red, 2
  failing jobs"), progress counters, and limits, with Edit goal, Continue
  and Close.
- **Inbox**: the Notifications section (DT-5). Rows for goal tasks held at
  their limit.
- **Alerts button**: open notifications join the list.
- **Director chat**: notifications delivered as grain-authored lines with
  their actions.
- **Settings → Dispatch**: `max_durable_runs`, the idle timeout, the
  judgement model, and the notification sinks.
- **System → Metrics**: the `agents` section and the steward report checks.

Every UI stage follows the deployment's screenshot rule: before and after
contact sheets at desktop and phone widths, light and dark.

## Migration

- **The steward**: the template gains `durable`, the goal, the
  subscriptions, `Model: "judgement"` and the new prompt. A wake already
  reconciles template grants onto the standing task (it adds what the
  template has and the task lacks), and the same path applies the goal
  and subscriptions to the existing task, so the handover is kept. The
  schedule's interval moves from 30 minutes to 6 hours in the same stage.
  On this deployment that is a grain/config change, because the
  deployment owns the schedule row after seeding.
- **Sessions**: `reconcileConsoleSessions` adds the grant and goal to each
  existing session task on its next pass. The session-specific branches
  are deleted only after that pass has run on a release, so a rollback
  never meets a session with neither. `IsConsoleSession` stays for
  display.
- **Repairs**: open `fixes_base_branch` tasks get `base-green` goals by a
  one-time pass. `closeBaseRepairsOnGreen` becomes a thin call to the goal
  check.
- **Parked tasks**: existing `parked` runs are read as `requested`
  suspensions with their wait targets. No rows are rewritten.

## The staged plan

Each stage is one follow-up task, filed by task 840. Arrows are
`depends_on`.

| Stage | Covers | Depends on |
|---|---|---|
| DT-S1 Suspension model and the standing invariant | DT-2 (record, kinds, continuity), DT-3 | 840, 837 |
| DT-S2 The `durable` grant: no deadline or wait cap, `max_durable_runs`, idle watchdog, setup suspensions | DT-1 | S1, 834 |
| DT-S3 Durable runs survive restarts: adoptable placement, `restart` resume on first cycle, session store on the data claim, kubernetes-granule roll test | DT-1 adoption | S2 |
| DT-S4 New suspension kinds and `resume_later`: model failover, capability change, context exhaustion, `resume_on_deadline`, `wait_for_task` unification | DT-2, DT-9 | S1 |
| DT-S5 Goals and progress limits | DT-4 | S4 |
| DT-S6 Notifications and `message_task`: store, API, `notify_operator`, Alerts/Inbox UI, director delivery, Investigate, subscriptions, `/messages` restriction | DT-5, DT-10 | 840 |
| DT-S7 Existing escalations onto notifications | DT-5 mapping | S6 |
| DT-S8 Compact pipeline tools: `read_pipeline_changes`, summary views, filters, per-run budget | DT-13 | 840 |
| DT-S9 Steward rework: reconciler arms, durable plus goal plus subscriptions, judgement model, new prompt | DT-6, DT-11, DT-12 | S2, S5, S7, S8 |
| DT-S10 Sessions on durable tasks; delete session-only exemptions | DT-7 | S3, S4, S5 |
| DT-S11 Steward verification: call log, report checker, `agents` metrics, replay fixture, sampled audit | DT-14 | S9 |

## Open questions

1. **Is `max_durable_runs = 4` right for a small host?** The operator
   said spending a sandbox is acceptable. The ceiling exists so that a
   misconfigured grant cannot take the whole host. It raises a
   notification rather than queueing silently.
2. **Should `resume_on_deadline` default to true for implementation
   templates?** This design leaves it off. Turn it on after DT-S4 has data.
3. **Can agents raise `critical`?** Here, only holders of
   `api:task-admin`. Revisit once there is a week of notifications to
   look at.
