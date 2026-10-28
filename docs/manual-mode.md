# Manual mode: pause the pipeline, then Run now

Pausing the pipeline (`grain pause`, the banner's **Pause**, `POST
/api/pipeline-pause`) stops this deployment picking up new work. While the
pause is in force, **Run now** still works: a task somebody explicitly starts
is dispatched, within capacity, and nothing else is. That is manual mode. An
operator, or an agent holding `dispatch-control`, pauses the deployment and
then starts exactly the tasks they choose, one by one.

## What a pause holds

Under an operator's pause (or an agent's), `dispatch.Cycle` starts:

- each task with an outstanding Run now request (`Observation.RunNowAt`,
  set only by `POST /api/tasks/{id}/run-now`, which is the UI button,
  `grain run-now` and `run_task_now`), oldest first, as capacity and the
  worker limit allow. A request that finds no room stays outstanding for
  the next cycle. A request is spent by the run it starts, unless that
  run ends without doing the work -- setup-failed, paused on a usage
  limit, out of provider capacity, interrupted, or OOM-killed
  (`model.RunNowSurvives`). Then it is put back with its original
  timestamp, the task's conversation says "Run now kept", and the task
  goes ahead of the queue again once the same retry backoff a ready task
  would wait has passed. A setup failure that reaches the failure cap
  (`MaxConsecutiveFailures`) spends it, and so does a run that parks its
  task on an environment failure, which waits for a human's reply;
- the one task a pause names in `UntilTaskID` (the upgrade check), as
  before.

It holds everything else: the ordinary backlog, merge-queue repairs,
scheduled and suite-filed tasks, ports. CI runs for a task that is
running proceed as usual.

A Run now is still subject to everything that is not a pause:

| Hold | Run now passes it? |
| --- | --- |
| Operator or agent pipeline pause | **Yes** |
| The upgrade check's own task-scoped pause | **Yes**, see below |
| A running upgrade's hold (`UpgradePrincipal`) | No |
| A scheduled upgrade's hold (`ScheduledUpgradePrincipal`) | No |
| Provider usage-limit pause (`dispatch.PausedFor`) | No |
| Grant, credential and policy holds (`dispatch.Hold`) | No |
| Capacity, worker limit | No |

An upgrade's hold shuts the queue in front of a restart, and the restart
discards every run in flight. A run started under that hold is wasted
whoever asked for it, so the request waits until the upgrade is over. The
upgrade check's pause comes after the restart. The upgrade is already done
and no restart is coming. That pause only keeps the deployment quiet while
the check looks at it. Someone asking for one particular run has decided
that run is worth more than the quiet, and that is their call to make.
They could already make it by resuming the check's pause outright.
(`model.PipelinePause.AdmitsRunNow`.)

The pause row does not always show an upgrade's hold. Neither upgrade
path takes the row over from a pause that is already in force, and a
pause taken while the hold is in force replaces it. So a Run now also waits
whenever an upgrade is running or scheduled, whatever pause the row holds
(the orchestrator's `upgradeUnderway`). Otherwise anyone could pause
on top of the hold and Run now straight past it.

The banner says which case applies: "Paused — only tasks you Run now will
start" for a pause Run now passes, and that Run now waits for an
upgrade's hold.

## dispatch-control: manual mode for an agent

`dispatch-control` is a capability granted per task or per standing
session (the director's capability pane), like `self-upgrade`. It goes
through the same capability admission as any other grant. A run holding
it is served:

- `pause_pipeline(reason)`: pause the whole deployment;
- `resume_pipeline()`: lift the pause. It is refused while an upgrade
  holds the queue (any pause Run now does not pass, above): lifting that
  hold would pass it with every task at once, and lifting a scheduled
  upgrade's hold calls the upgrade off, which is `self-upgrade`'s
  `cancel_deployment_upgrade` rather than this grant's. A person can still
  do either from the banner;
- `run_task_now(task_id)`: Run now. It approves a proposed task and picks
  a deferred one back up first, and follows the rules above, so it never
  passes an upgrade's hold. It refuses to approve a task that asks for a
  capability, a write target or a read the agent's own task does not hold
  (`model.EscalatedCapabilities`, `UngrantedWrites`, `UngrantedReads`), or
  that is filed against a repository the agent's own task cannot read:
  those proposals wait for a person, and an agent that could approve them
  could propose itself any grant and then start it. Nor does it approve an
  auto-merge task unless the agent's own task is an auto-merge job landing
  where that task would (`model.LandsWhereGranted`), so an agent whose own
  work is reviewed cannot land unreviewed work by approving it. A person approves such
  a task first, and the agent may then run it. It also refuses a task
  parked on a question or a `request_secret` (`awaiting_reply`): starting
  it is answering "go on without one", which is for the person it asked.
  The button un-parks such a task; `run_task_now` does not;
- `defer_task(task_id, until?)`: set a task aside.

`pause_pipeline` and `resume_pipeline` share their names with
`pipeline-control`'s. A run holding both grants is served
dispatch-control's versions, which accept pipeline-control's `repo`
argument too.

Pauses and Run nows are made as `{agent, <task id>}`. The pause row records
that agent as who paused, and a Run now's approval and the note it leaves in
the task's conversation are attributed to it. A resume and a deferral record
no actor, for an agent as for a person. That matches how `self-upgrade`
records a scheduled upgrade. The grant is checked again on every call, so
taking it off the task stops the next call.

### The risk

An agent holding `dispatch-control` decides what this deployment spends
its runs on. It can hold the queue for as long as it likes and start
whatever it chooses ahead of everybody else's work. Only grant it to
a task or session you would trust with the pause button and Run now
yourself. A person can always see who paused (the banner names the
agent's task) and lift the pause from the banner. Upgrades, usage
limits, capacity and per-task grant holds still bind it.
