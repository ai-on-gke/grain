# Stale system schedules: 2026-11-18 investigation

The daily report schedules are intentionally waiting for their previous
reports to close. Pipeline steward has a separate initialization defect:
its safety-wake gate can drop every occurrence while a pipeline
notification is open, before a standing task exists to subscribe to it.
Neither finding requires attributing the behavior to the November 18
03:11 UTC restart.

## Deployment evidence

Inspected the installed source at
`/home/debian/grain-src`, revision
`46fdc1104ba8c6febd3abf9820dfb0055240421c`, alongside the working
checkout. The relevant overlap predicate and steward preflight are
present in that installed build. Read daemon task records using
`grain task read` and `grain api GET /api/tasks/{id}`, the exported
`config://settings/schedules.yml`, health, notifications, and scheduler
journal entries. Tasks and attempts come from the daemon database, not
the configuration export.

At the initial read on November 18 around 03:34 UTC:

| Schedule | Cadence | Last run | Next occurrence | Evidence |
| --- | --- | --- | --- | --- |
| system:infra-review | Daily 03:00 UTC | Nov 16 03:00:06.163359780Z | Nov 17 03:00Z | Task 34, awaiting_reply/answered |
| system:memory-agent | Daily 03:00 UTC | Nov 16 03:00:06.163359780Z | Nov 17 03:00Z | Task 35, awaiting_reply/answered |
| system:pipeline-steward | Every 360 minutes | null | Nov 18 05:36:20.165977182Z | No steward task in the complete 82-task list |

All three schedules were enabled with `overlap: skip`. Task 34's only
attempt succeeded at Nov 16 03:04:39.602568565Z; task 35's only attempt
succeeded at 03:01:50.566812353Z. Both ended with
`comment_on_issue`, produced reports, and transitioned to awaiting_reply.

The narrow journal window Nov 17 02:59–03:02 UTC contains explicit holds
at 03:00:08: tasks 34 and 35 were still open, so each Nov 17 firing was
held until its task closes. Both task conversations contain the
corresponding schedule note. This predates the restart by a day.

A later daemon-record read showed task 35 closed by the operator
(access code) at Nov 18 03:35:28.120236211Z; that does not change the
original hold diagnosis. This investigation did not close or mutate any
deployment task.

For steward, a narrow Nov 17 23:35–23:38 UTC journal window records a
quiet drop at 23:36:21: the occurrence due at 23:36:20 was dropped and
the cadence advanced. Broader logs also show six-hourly quiet drops
through Nov 16–17; the broader log query reported truncation, so it is
not evidence of exhaustive history. The next occurrence in the future
and null last-run timestamp describe dropped occurrences, not an
overlap hold or a scheduler that stopped polling.

The daemon also has an uncleared
`pipeline.not-publishing:ai-on-gke/grain@main` notification, ID 6,
raised Nov 17 12:08:13.521384786Z, linked to task 73. The 17:36 and
23:36 safety firings nevertheless logged quiet drops. The base-branch
gates API returned an empty list; the quiet log's generic statement
that every base branch is green is not proof that publishing is healthy.

## Report tasks: intentional lifecycle and operator options

`model.OverlapSkip` defines an unfinished task firing as an **open
task**, not an active attempt. `Store.fireTaskSchedule` asks
`OpenTaskWithTag("schedule:" + scheduleID)`; that query filters only
`task_state.state != closed`. It deliberately includes answered reports,
completed tasks, and tasks awaiting human attention. Run success does
not close a task.

For a schedule-filed report, `ProcessResult` records completion after
`comment_on_issue`; `ClosesOnAnswer` is for report-only subtasks filed
by another agent task and does not apply to these schedule-filed reports.
There is no basis here to replace the open-task predicate with a
successful-run predicate: that would also allow new work while a real
question or unread report is pending.

Concrete options, without changing cadence or grants:

- Keep `overlap: skip` and have an operator read and close each completed
  report through its task page. The held occurrence then fires once,
  folding missed occurrences, and normal cadence resumes. This is an
  operator workflow, not an exploratory repair performed here.
- If daily reports should continue independently of whether earlier ones
  have been read, choose `overlap: allow` for these two schedules in a
  **separate grain/config pull request** against
  `settings/schedules.yml`. Keep their daily 03:00 cadence and grants.
  This permits multiple open reports and also overlapping running tasks;
  that tradeoff belongs to the operator.
- A genuinely empty run can use `nothing_needed` with the existing
  default close policy, if its instructions permit that. A report
  somebody should read still belongs on `comment_on_issue`.
  Setting `nothing-needed: ack` is **not** an unblock option: it leaves
  the task open for acknowledgment and therefore still holds
  `overlap: skip`.

No deployment configuration or task lifecycle changes were made.

## Steward: confirmed initialization defect and fix

`reconcileSystemSchedules` seeds the schedule only. The standing task,
goal, and `pipeline.*` subscriptions are installed by the first actual
firing. Quiet firings intentionally call `DropQuietFiring`: they advance
`NextRunAt`, leave `LastRunAt` unset, and create no task.

The installed `pipelineStewardQuiet` checks base-branch CI and task/merge
stall observations, but not open pipeline notifications. Meanwhile,
`WakeNotificationSubscribers` can wake only existing subscribers. An
open publishing notification can therefore have no subscriber and
remain invisible to every safety firing. This explains the observed
absence of initialization despite an open publishing failure. A steward
that never runs on a genuinely quiet deployment is intentional; one
that never initializes despite this notification is a defect.

The fix makes the quiet predicate consider open `pipeline.*`
notifications using the same helper as the pipeline-health goal.
It preserves the notification-wake exclusion for
`SubjectStewardRegression`, so reports about the steward's own output
do not cause wake loops. Existing firing code still enforces enabled
state, standing-task reuse, overlap, cadence, and grants. On the next
due safety firing, an eligible open notification can bootstrap the
standing task and its subscriptions. No unconditional startup run or
cadence reset is introduced.

## Validation

`TestPipelineStewardSafetyWakeInitializesNotificationSubscriber`
failed before the fix with `task = <nil>, want run true` for an open
publishing notification on a deployment with no steward task or
base-CI/stall rows. The test also covers cleared notifications, unrelated
notifications, the self-regression exclusion, the first firing timestamp,
cadence advancement, and installed subscriptions.

After the fix, the regression and focused scheduler/report-lifecycle
checks passed. The complete orchestrator package also passed:
`go test ./pkg/orchestrator -count=1` (213.808 seconds).

The investigation uses tasks 34/35 as evidence, not prerequisites.
There is no dependency on task 78 or another pending code change.
