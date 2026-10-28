# Schedules (v2)

bwsalmon/agents#376: give v2 the recurring-chore mechanism v1 already
proved out (bwsalmon/agents#163, `docs/roadmap.md` §28 — a dependency
audit, a weekly report, something that lands on the queue on its own
cadence instead of only ever starting from a human filing it), reshaped
the way v2 already reshaped ordinary task creation: a schedule is a store
row a UI can create, edit, pause and delete, not a template file an
operator drops on the host.

This document describes what is actually implemented on this branch
(`pkg/model/schedule.go`, `pkg/orchestrator/schedule.go`,
`pkg/ui/schedules.go`, `ui/src/components/ScheduleOverlay.jsx`,
and the accompanying tests), not a proposal — it exists so a reviewer
has one place to read the shape of the feature and why it looks the way
it does, alongside the code's own doc comments.
I have read the diff in full; I have not been able to run `go build`/`go
test` against it myself (this sandbox's Go toolchain is 1.19.8, and
`go.mod` requires 1.26.2 — a pre-existing environment limit, not
something this change introduced), so the "does it actually pass"
question is left to CI/review rather than asserted here.

## What v1 already settled, kept unchanged

`grain/automation/scheduled_jobs.py` and `core.py`'s `_scheduled_jobs`
worked out the mechanism, and this keeps the two load-bearing decisions:

- **No new trigger, cron, or scheduler.** The whole system is already one
  poll loop (`cmd/grain/daemon.go`'s `reconcile`, ticking
  `orchestrator.RunCycle`); a schedule is one more
  `orchestrator.Reconciler` in that same loop
  (`pkg/orchestrator/cycle.go`'s `Reconcilers()`), not new
  infrastructure.
- **A previous, unfinished firing suppresses the next one, independently
  of whether the interval has elapsed.** v1 checked GitHub for an open
  issue carrying the job's marker label; this checks the store for an
  open task carrying the schedule's tag (`Store.HasOpenTaskWithTag`).
  Same reasoning both times: a chore that runs long must not get a
  duplicate, and a chore that finishes early is still held to its own
  cadence rather than refiring immediately.

## What changed, and why

**A schedule is a store row, not a file.** v1's `ScheduledJobsConfig`
loaded `/data/config/scheduled-jobs/*.md` because v1's task creation
itself went through GitHub, and "absence of the directory is the off
switch" fit that world. v2 already moved task creation onto the store
(`Client.CreateTask` writes straight to `model.Store`, no issue
involved) specifically so a UI or the CLI could create one without a
redeploy; a `Schedule` (`pkg/model/schedule.go`) is the same idea —
`ScheduleOverlay.jsx` creates, pauses/resumes, and deletes one the
same way `SecretsPanel.jsx` already manages secrets.

**No `needs_approval` field.** v1's design draft called `SCHEDULED` "the
one origin that chooses per instance" before `docs/data-model.md`
dissolved the special case: creating a schedule *is* the human's standing
approval, so every firing lands already approved
(`fireTaskSchedule` sets `Approval` unconditionally). This
implementation takes that dissolution all the way — there is no
per-schedule opt-in to land `proposed` instead, unlike v1's own
`needs_approval` header. A schedule whose work genuinely needs review
each time is not what this covers today; see "Left open" below.

**Idempotency is a tag on the fired task, not a label matched by
schedule name.** `firingTag(scheduleID)` produces `"schedule:<id>"`,
attached to `Task.Tags` — `docs/data-model.md`'s own resolution ("neither
a state nor a capability: it is an idempotency tag") kept literally,
queried back by `Store.HasOpenTaskWithTag` (a join of `task_tag` against
the `task_state` view for anything short of `closed`).

**Interval is a Go duration string, not v1's integer hours.**
`Schedule.Interval` round-trips as `"24h0m0s"` etc., matching
`Settings.PollInterval`'s already-established convention
(`pkg/ui/settings.go`) rather than inventing a second syntax — this also
buys sub-hour cadences (`"30m"`) v1's `Interval-Hours` header could not
express.

**`NextRunAt`, not "last fired + interval, computed on read."** Each
schedule carries its own `NextRunAt`/`LastRunAt` columns
(`schedule` table, `SchemaVersion` 7 → 8), advanced by
`fireTaskSchedule` itself in a loop that walks `NextRunAt` forward by
`Interval` until it is back in the future — so a schedule paused (or a
daemon down) through several missed intervals fires once on resume and
resyncs to its normal cadence, rather than firing once per missed
interval or drifting against wall-clock time. A freshly created schedule
gets `NextRunAt = Recurrence.Next(now)` — the first occurrence of its own
cadence, never `now` itself. It used to get `now`, so that it fired on
the very next cycle rather than waiting a full interval for its first
task, borrowing `CreateTask`'s "queued the moment it is written"
immediacy; grain/task-61 took that back out. A schedule is a human
saying *when* something should run, and creating one is not a request to
run it now: a "daily at 09:00" written at four in the afternoon filed a
task at four in the afternoon, and — because an unfinished firing
suppresses the next one — that unasked-for task then had to be closed by
hand before the 09:00 firing anybody did ask for was allowed to happen.
Somebody who wants it now still files the task by hand, which is where
"now" was always meant to come from.

`ui.UpdateSchedule` retimes `NextRunAt` the same way, but only when the
cadence itself actually changes: `NextRunAt` is an instant the *old*
cadence chose and `advanceSchedule` only ever walks forward from it, so
a schedule moved from monthly to daily would otherwise keep its next
firing a month out and only start running daily after that. An edit that
resends the recurrence unchanged (every save from a form that submits
the whole schedule) is not a cadence change and leaves the firing time
alone, as does pausing and resuming — that is what makes a pause a pause
rather than a reset.

**Pausing keeps the row.** `DeleteSchedule` removes a schedule
outright with no soft-delete, since (per its own doc comment) a schedule
carries no history worth keeping once nobody wants it — every task it
already filed stays exactly where it was. Pausing instead
(`Enabled = false`) is the separate, reversible operation, matching the
UI's "Pause"/"Resume" button rather than "Delete" for that case.

## Shape of the pieces

- **`model.Schedule`** (`pkg/model/schedule.go`): `ID`, `Title`,
  `Body`, `Target`, `Base`, `AutoMerge`, `Interval`, `Enabled`,
  `NextRunAt`, `LastRunAt`, `CreatedAt`. No `Reads`, `Grants`,
  `DependsOn`, or `Capabilities` — a firing carries none of those (see
  "Left open").
- **Schema** (`pkg/model/schema.go`): `schedule` (one row per
  schedule) and `schedule_sequence` (its own autoincrement
  allocator, so a schedule id like `sched-3` is never confused with a
  task id — `NewScheduleID`, distinct from `NewTaskID`).
  `SchemaVersion` 7 → 8. No changes to `task`, `task_state`,
  `task_blocked`, or `task_ready`: the tag-based open-firing check reads
  `task_tag`/`task_state`, both of which already existed.
- **Store** (`pkg/model/store.go`): `PutSchedule`,
  `GetSchedule`, `ListSchedules`, `UpdateSchedule`
  (read-modify-write-and-retry, `UpdateTask`'s own shape),
  `DeleteSchedule`, `DueSchedules(ctx, now)` (enabled schedules
  whose `NextRunAt` has passed), `HasOpenTaskWithTag`.
- **Orchestrator** (`pkg/orchestrator/schedule.go`): `reconcileSchedule`,
  registered in `Reconcilers()` *before* `dispatch` — so a task a
  schedule files this tick is dispatchable the same tick, the same
  latency argument that already puts `sync` last. `fireTaskSchedule`
  does the open-tag check, allocates a task id, builds and writes the
  `model.Task` (attributed to a new `scheduler` principal — automation,
  ID `"schedule"`, distinct from `"grain"`'s relayed-agent-output
  identity and from the merge queue's own automatic-fix identity), then
  advances `NextRunAt`/`LastRunAt`.
- **HTTP API** (`pkg/ui/schedules.go`, wired in `server.go`): `GET
  /api/schedules`, `POST /api/schedules`, `PATCH /api/schedules/{id}`,
  `DELETE /api/schedules/{id}` — no `GET /api/schedules/{id}`, since the
  overlay only ever needs the list. `Client.CreateSchedule`/
  `UpdateSchedule`/`DeleteSchedule`/`ListSchedules` are store-backed the
  same way `Client.CreateTask` is; `HTTPClient` (the CLI/remote path)
  gains no matching methods, so a schedule can be managed from the web
  UI but not yet from `grain` on the command line — see "Left open."
- **Frontend** (`ScheduleOverlay.jsx`): a list-plus-form overlay
  following `SecretsPanel.jsx`'s pattern exactly, opened from a new
  sidebar entry alongside Settings. A task the overlay's own
  schedule fired shows a "Scheduled" badge in the ordinary task list
  (`Task.Scheduled`, off `Origin.Reason == ReasonSchedule` — the same
  treatment `Task.Stacked` already gives `ReasonFix`).

## Tests present

`pkg/model/sqlite/schedule_store_test.go` (store round-trips, due-listing
against a fixed `now`, the open-tag gate), `pkg/orchestrator/
schedule_test.go` (a due schedule fires exactly one task and advances
its timing; an open previous firing suppresses the next one; a
not-yet-due schedule fires nothing), `pkg/ui/schedules_test.go` and
`server_schedules_test.go` (the `Client` methods and the five HTTP
routes), and `SchedulesList.test.jsx` plus the `Sidebar.test.jsx`
addition for the new entry point.

## Left open

- **No CLI.** `grain schedule ...` would need `HTTPClient` methods this
  branch does not add; straightforward on top of the existing `Client`
  methods, not bundled here.
- **No per-schedule approval gate.** Every firing lands already
  approved; a schedule whose output should be reviewed before it
  dispatches has no way to ask for that today, unlike v1's
  `needs_approval` header. Worth a follow-up if a real chore needs it.
- **No `Reads`, `Grants`, or `DependsOn` on a schedule.** A firing gets
  none of `Task`'s wider capability/read/dependency surface — fine for a
  simple recurring chore against one write target, not yet expressive
  enough for one that needs a capability or a read-only companion repo.
- **No per-schedule timezone.** `NextRunAt`/`LastRunAt` are UTC
  `time.Time` throughout, same as every other timestamp in this store;
  "every N hours/minutes since it last fired" is the whole of what a
  schedule can express, not "at 09:00 local time."

## Update: schedules got their own pane (bwsalmon/agents#455)

The "Frontend" bullet above and `ScheduleOverlay.jsx` describe how
this first shipped; bwsalmon/agents#455 moved the UI from a modal opened
by a footer button to a full pane (`SchedulesList.jsx`), selected the
same way the repo page already is: a `ListItemButton` alongside "Repos"
in `Sidebar.jsx`, switching `App.jsx`'s `view` state to `"schedules"`
rather than toggling an overlay's visibility. This also closed the gap
the original overlay left open -- editing an existing schedule's title,
description, repo, base branch, auto-merge, or interval, which
`UpdateSchedule`/`PATCH /api/schedules/{id}` already accepted but no UI
exposed beyond the enabled toggle. No backend change was needed; the API
surface was already complete.

## Update: cadences beyond "every N hours", and the rest of Task's own field set (bwsalmon/agents#464)

Two of this document's own "Left open" items from when the feature first
shipped are closed as of here.

**A schedule is no longer only "every N hours/minutes since it last
fired."** `model.Schedule.Interval` (a bare `time.Duration`) is
replaced by `Recurrence` (`pkg/model/schedule.go`), a small sum type: `Kind`
is one of `RecurrenceEveryNHours`, `RecurrenceDaily`, `RecurrenceWeekly`, or
`RecurrenceMonthly`, and the fields that apply depend on which -- the same
"column per case, most left unset" shape `Task.Origin`/`Task.Approval`
already use for a different sum type. `Recurrence.Next(after)` is the
whole of the new logic: the first occurrence strictly after `after`,
wall-clock aligned for the three new kinds (a calendar month is not a
fixed number of hours, so `RecurrenceMonthly` walks actual months rather
than adding an approximate duration, clamping a day-of-month past a
shorter month's own last day rather than overflowing into the next one).
`fireTaskSchedule`'s loop is otherwise unchanged in shape -- it still
walks forward from the schedule's own `NextRunAt` (never from "now"
directly) until back in the future, so a schedule paused, or a daemon
down, through several missed occurrences still fires exactly once on
resume. `EveryNHours` keeps hour granularity rather than the old
`Interval`'s arbitrary duration string, matching v1's own
`Interval-Hours` header and what this issue itself asks for; the
still-open "no per-schedule timezone" bullet below is otherwise
unchanged, since `TimeOfDay`/`Weekday`/`DayOfMonth` are read against UTC,
same as `NextRunAt`/`LastRunAt` always have been.

A database created before this migrates in place
(`ensureScheduleRecurrenceColumns`, `pkg/model/store.go`): its old
`interval_ms` column is read once, backfilled into `every_n_hours`
(rounded down to whole hours, matching `RecurrenceEveryNHours`'s own
granularity) and dropped, the same probe-then-`ALTER TABLE` approach
`ensureConfigMaxConcurrentColumn` already established for `grain_config`.
`SchemaVersion` 9 → 10.

**Reads and Grants join Target/Base/AutoMerge on a schedule.** The
former "Left open" bullet on this narrowed: a schedule's own `Reads`
(read-only repos, `schedule_read`) and `Grants` (capabilities,
`schedule_grant`) are new child tables, `task_read`/`task_grant`'s
own shape ported onto `schedule_id`, and `fireTaskSchedule` copies
both onto the filed `Task` the same way it already copies `Target`/
`Base`/`AutoMerge`. `DependsOn` and `Approved` remain deliberately absent
-- `ui.CreateScheduleRequest`'s own doc comment explains why: a one-shot
dependency link makes no sense against a task a schedule refiles
indefinitely, and a firing lands already approved by design regardless of
what a per-task `Approved` flag would otherwise ask for.

**`SchedulesList.jsx`'s create and edit forms are now one component**
(`ScheduleForm`), rather than two independently maintained copies of the
same field list -- worth doing now that the field list grew to match
`NewTaskOverlay.jsx`'s own (title, description, repo, base, read-only
repos, capabilities, auto-merge), plus a new `RecurrenceFields` picker:
a "Repeat" select for the four `Kind`s, showing only the inputs that
apply to whichever is chosen (an hours field for every-N-hours; a time
field for the other three; a weekday select added for weekly; a
day-of-month field added for monthly).

Still open, unchanged from before: no CLI, and no per-schedule approval
gate.

## Update: templates (bwsalmon/agents#516)

A schedule's own content -- title, description, target repo, base
branch, auto-merge, read-only repos, and capabilities -- used to exist
only inline on the schedule itself, one copy per schedule with no way to
share it. `Template` (`pkg/model/template.go`) is that content pulled
out into its own store row, `template` plus its own
`template_read`/`template_grant` child tables (`SchemaVersion` 11 → 12),
so more than one schedule can point at the same declaration -- and, per
this issue's own framing ("we will add more places to use them in the
future"), so a later caller beyond schedules has somewhere to point too,
without this type or its store surface changing shape for that caller.

**A schedule optionally references a template rather than always
carrying its own copy.** `Schedule.TemplateID` (nullable
`schedule.template_id`, a database created before this migrates in
place via `ensureScheduleTemplateColumn` the same probe-then-`ALTER
TABLE` approach every other migration in `store.go` already uses) is
`nil` for a schedule that still declares its content inline, exactly as
every schedule always has; set, it names the template that content comes
from instead. `ui.CreateScheduleRequest`/`UpdateScheduleRequest` treat
the two as mutually exclusive -- a `templateId` given at creation takes
over Title/Description/Repo/Base/AutoMerge/Reads/Capabilities entirely
rather than letting a caller mix a template with its own per-field
overrides, a combination deliberately unsupported so "what does this
schedule actually file" never depends on resolving a merge between two
sources. `UpdateScheduleRequest.TemplateID` adds one more case an
ordinary nilable pointer already covers reasonably: given as `""` it
detaches a schedule from its template, leaving whatever content was last
synced onto the row in place as an independent, directly-editable copy
rather than blanking it out.

**Resolved fresh at firing time, not copied in once.**
`orchestrator.fireTaskSchedule` re-reads a template-backed schedule's
`Template` from the store on every firing rather than trusting
`Schedule`'s own inline columns, so editing a template changes what
every schedule pointing at it files *next*, with no separate "push the
edit out" step -- the entire reason a template is worth having over
copy-paste. Those inline columns are not dead weight even so: the same
firing writes the template's current content back onto them, so
`ui.scheduleFrom` can still render a schedule's effective title, repo,
and the rest with no join, and a template deleted or otherwise
unresolvable out from under a schedule fails that one firing with a
plain error (`reconcileSchedule`'s own "one schedule failing does not
stop the others" tolerance already covers this) rather than filing a
task with no content.

**Deleting a template in use is refused, not silently orphaning.**
`Store.SchedulesUsingTemplate` is what `ui.Client.DeleteTemplate` checks
before deleting: a template still named by a live schedule cannot be
removed until that schedule is repointed or deleted first, since deleting
it anyway would strand that schedule's next firing far more silently than
the plain 404 a stale reference would otherwise surface only days or
weeks later.

**HTTP API** (`pkg/ui/templates.go`, wired in `server.go`): `GET
/api/templates`, `POST /api/templates`, `PATCH /api/templates/{id}`,
`DELETE /api/templates/{id}` -- `schedules.go`'s own four-route shape,
with no per-template `GET` for the same reason schedules have none.

**Frontend**: templates are a kind of row on the Processes page
(`ProcessesList.jsx`, `/processes`), the one table of schedules,
workflow schedules, templates, suites and triggers that replaced the
separate Schedules and Templates panes (docs/design/settings-panel.md
§5). "+ New" there, or a click on an existing template row, opens
`TemplateOverlay.jsx` -- the same fields a schedule carries, minus any
cadence, since a template has none of its own. `ScheduleOverlay.jsx`,
which a schedule row opens, has a "Template" picker ahead of its other
fields: choosing one hides Title/Description/Reads/Capabilities and the
rest outright (rather than showing fields a template-backed request
would silently ignore), and a template bound to a repo replaces the
Repo/Base fields with a line naming where the schedule will fire.

Tests: `pkg/model/sqlite/template_store_test.go` (round trips, the
schema-12 migration), `pkg/orchestrator/schedule_test.go` (fresh
resolution across an edit between two firings, the missing-template
failure path), `pkg/ui/templates_test.go` and `server_templates_test.go`
(the `Client` methods and the four HTTP routes, plus the delete-while-in-
use guard), `pkg/ui/schedules_test.go` (attach/detach, the unknown-
template rejection), `ProcessesList.test.jsx` for the template rows and
"+ New" opening `TemplateOverlay`, and `App.schedules.test.jsx` for the
schedule overlay's picker.

### Interactive templates

A template can file its tasks as live chats (`Template.Interactive`,
`interactive` on the API and in `settings/templates.yml`). The agent of
such a task gets the `chat` tool and talks with whoever is watching the
task while it works, rather than running unattended -- the right shape
for work that needs a person's answers on the way, such as setting up a
cluster in a project only they can name. Tick **Interactive (agent can
chat with you)** in the template form.

Every way of turning a template into a task carries it: **Use…** on the
template's row, a schedule's firing, a suite pass, a qualification run,
and `propose_task`'s `template_id`. **Use…** opens the new task straight
away, and an interactive task's pane is its chat, so the agent's first
message lands where you are already looking; the dialog has no
**Approved** box for one, since a chat a person files is approved and
queued at once. A schedule's firing parks the task on its first `chat`
until somebody replies, the same as any other interactive task. A run's
proposal from an interactive template is interactive too but is not
approved by being one: it lands the way any proposal does.

### Grant presets: capability arguments and network access

A template's capabilities are copied onto every task it files, with
their `with:` arguments (docs/capabilities.md §2.2.3), so a template can
carry a grant preset -- `gcp-key` with a chosen permission set
(docs/gcp-key-permission-sets.md), for example. The template form's
**Grants** section is the task form's (`GrantsPicker`): choose **Add
grant…** → the `gcp` server's `gcp-key` and pick the **Permission set** from its
dropdown. `network_access` (**Network access** in the form, `network` in
the file) is copied the same way: `open` gives every task the template
files outbound internet whatever the deployment's default.

A template that sets up a GKE cluster for grain, talking it through with
whoever files it, holding a key of the `gke-admin` permission set and
with the internet open, is this entry in `settings/templates.yml` (the
grant is written as the reference, `=`, and its arguments as JSON):

```yaml
templates:
  - id: template-7
    name: GKE cluster for grain
    title: Set up a GKE cluster for grain
    body: |
      Ask which project and region to use, then create the cluster and
      install grain on it.
    base: ""
    auto-merge: false
    network: open
    nothing-needed: ""
    model: ""
    interactive: true
    target: ""
    grants:
      - https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"gke-admin","primary":true}
    created-at: "2026-10-24T12:00:00Z"
```

Tests: `pkg/model/interactive_template_test.go` (the round trip and every
firing point), `pkg/orchestrator/interactive_template_test.go` (a
schedule's firing), `pkg/ui/interactive_template_test.go` (filing over
HTTP, the unapproved proposal, a grant with arguments and `network:
open` reaching the task), `pkg/staterepo/interactive_template_test.go`
(the export and import), and `InteractiveTemplate.test.jsx`.

Left open: no CLI for templates (`HTTPClient` gains no matching methods,
the same gap schedules already have); no UI affordance yet for the "more
places to use them" this issue asks for beyond schedules -- creating an
ordinary task from a template, most plausibly, is left for whenever a
concrete caller actually needs it.

## Update: templates page split into a list and a sub-page overlay (bwsalmon/agents#545)

`TemplatesList.jsx`'s list-plus-form shape (previous section) put every
template's full form inline in the row being edited, plus a permanently
open "New template" form at the foot of the list -- fine when templates
were new and few, but unlike `TaskList.jsx` it had no search, no sort,
and nothing to keep a longer list scannable.

`TemplatesList.jsx` is now a flat list of key details only -- name,
target repo, task title -- with `TaskList.jsx`'s own search-and-sort
toolbar (`SORTS` here has no `manual` entry: a template has no backlog
order, so `name` is the default instead). Everything about one
template -- description, base branch, read-only repos, capabilities,
auto-merge, and now delete too -- moved into a new `TemplateOverlay.jsx`,
opened either by the list's own "+ New template" button (blank) or by
clicking a row (pre-filled), `NewTaskOverlay.jsx`/`DetailOverlay.jsx`'s
own split between a list of key details and everything about one item
living behind a click. `TemplateOverlay` stays owned by `TemplatesList`
itself rather than lifting to `App.jsx` the way task detail/creation
does -- `SchedulesList.jsx`'s own form state is local for the same
reason: nothing outside this pane needs to know a template is mid-edit.

No API or model changes: `Template`'s wire shape already carried
everything the new overlay needed, including `createdAt` for the
Newest/Oldest sorts.

Tests: `TemplatesList.test.jsx` covers the list (search, sort, empty and
no-match states) and, since `TemplateOverlay` has no store state of its
own outside this pane, exercises create/edit/delete/cancel through it
the same way `SchedulesList.test.jsx` already exercises `ScheduleForm`
inline rather than as a separate suite.

## Update: schedules page split into a list and a sub-page overlay (bwsalmon/agents#547)

`SchedulesList.jsx`'s list-plus-form shape -- the very shape the two
updates above already carried over to `TemplatesList.jsx`, then moved
`TemplatesList.jsx` off of -- gets the same treatment here, so the
schedules page ends up looking like the tasks page the way both of those
pages already look like each other. Previously `SchedulesList.jsx` put a
full edit form inline in whichever row was being edited (`ScheduleForm`,
reused for both), a Pause/Resume and Delete button on every row, and a
permanently open "New schedule" form pinned to the foot of the list --
fine when there were only a couple of schedules, but with no search or
sort it did not scale the way `TaskList.jsx`'s own toolbar lets the task
list scale, and editing lived somewhere `TaskList.jsx`'s own rows never
put it.

`SchedulesList.jsx` is now a flat list of key details only -- title,
target repo, cadence, template (if any), and a "Paused" chip when
disabled -- with `TaskList.jsx`'s own search-and-sort toolbar (`SORTS`
here has no `manual` entry, `TemplatesList.jsx`'s own reasoning: a
schedule has no backlog order, so `title` is the default instead).
Everything about one schedule -- description, base branch, read-only
repos, capabilities, the recurrence picker, and now Pause/Resume and
Delete too -- moved into a new `ScheduleOverlay.jsx`, opened either by
the list's own "+ New schedule" button (blank) or by clicking a row
(pre-filled), `TemplateOverlay.jsx`'s own split applied here. Pause/
Resume and Delete sitting inside the overlay rather than on the row
matches where a task's own Close/Reopen already live -- inside
`DetailOverlay.jsx`, not on a `TaskList.jsx` row -- rather than being a
schedule-specific pattern.

No API or model changes: `Schedule`'s wire shape already carried
everything the new overlay and sort options need, including `createdAt`
for the Newest/Oldest sorts, and `PATCH /api/schedules/{id}` already
accepted a bare `{"enabled": ...}` body for Pause/Resume, unchanged from
how the old inline button used it.

Tests: `SchedulesList.test.jsx` covers the list (search, sort, empty and
no-match states, recurrence descriptions) and, since `ScheduleOverlay`
has no store state of its own outside this pane, exercises create/edit/
pause/resume/delete/cancel through it the same way
`TemplatesList.test.jsx` already exercises `TemplateOverlay`.
`App.test.jsx`'s own schedules-pane tests are updated to open the
overlay (a row click, or "+ New schedule") rather than an inline "Edit"
button.

## Update: templates carry no target repo or branch (bwsalmon/agents#516 revisited)

`Template` (`pkg/model/template.go`) originally carried Target and Base
alongside Title/Body/AutoMerge/Reads/Grants -- the earlier "Update:
templates" section above documents `ui.CreateScheduleRequest`/
`UpdateScheduleRequest` treating a `templateId` as taking over Repo/Base
along with everything else. That was wrong for the same reason a
qualification plan and a suite were already built the other way:
`QualificationPlan.Repo` and `SuiteRun.Target`/`Base` decide what those
two mechanisms target, never a template's own fields -- a template is
reusable content, and which repo and branch a firing targets is a
property of *how* it is used, not of the reusable content itself.
Schedules were the one caller that had a template override it anyway,
and it never generalized: `CreateQualificationRun` always targeted
`candidate.Repo`/`candidate.Branch`, whatever `tmpl.Target` said, and
only failed a run outright if the two happened to disagree.

`template` drops `target_owner`/`target_name`/`base`
(`ensureTemplateNoTargetColumns`, the same probe-then-`ALTER TABLE`
approach every other migration in `store.go` uses, in the direction
`ensureConfigMaxConcurrentColumn`'s own `slots` removal already goes:
probe for the old columns' presence, then drop them, since they are
`NOT NULL` and `PutTemplate` stops supplying them). `ui.Template`/
`CreateTemplateRequest`/`UpdateTemplateRequest` drop `Repo`/`Base` to
match.

`ui.CreateScheduleRequest`/`UpdateScheduleRequest` now take Repo and
Base as their own fields unconditionally, `templateId` or not --
`templateId` still takes over Title/Description/AutoMerge/Reads/
Capabilities entirely, the same "no mixing a template with per-field
overrides" rule as before, just five fields instead of seven.
`orchestrator.fireTaskSchedule` no longer reads Target/Base off a
resolved template; a schedule's own Target/Base are what every firing
targets, template-backed or not. `ui.PutQualificationPlan` and
`model.CreateQualificationRun` drop their "template must target this
same repo" check along with it -- there is no longer a template-side
Target to check against.

No change to suites: `Suite`/`SuiteRun`/`fireSuitePass` never read a
template's Target or Base to begin with (`model.SuiteItem`'s own doc
comment already says a suite resolves each item's template content
only), so `SuiteOverlay.jsx`/`SuiteRunOverlay.jsx` needed no behavioural
change -- only the template picker's secondary text (`t.repo`) and
`RepoReleases.jsx`'s "templates already declared for this repo" filter,
both of which assumed a field that no longer exists, come out.


## Update: a schedule can run a suite, not only file a task

A suite (`docs/design.md`, `pkg/model/suite.go` -- bwsalmon/agents#642)
was runnable only by hand: a human picks a suite, a repo and a branch,
and `ui.Client.CreateSuiteRun` starts one run. The recurring-chore case
this whole document is about wanted the other half -- "sweep this branch
for bugs every night", not "sweep it when I remember to click Run" -- so
a schedule now fires either of the two things grain knows how to start.

**One schedule type, not a second parallel mechanism.**
`Schedule.SuiteID` (`pkg/model/schedule.go`) sits beside the
existing `TemplateID`, `schedule.suite_id` beside
`schedule.template_id`, and the two are mutually exclusive: a
schedule files a task (inline content or a template's), or it starts a
suite run. Everything else about a schedule keeps meaning exactly what it
already meant -- the recurrence, the enabled/paused switch, `NextRunAt`/
`LastRunAt` and the walk-forward loop that resyncs a schedule paused
through several missed occurrences, the target repo and base branch, and
the "a previous firing that has not finished suppresses the next one"
rule. That was the point of doing it this way rather than adding a
`ScheduledSuite` row, a second reconciler and a second page: none of that
machinery is about what a firing *is*, so none of it needed a second
copy. `orchestrator.fireSchedule` is the whole of the fork --
`fireTaskSchedule` or `fireSuiteSchedule`, sharing `advanceSchedule`
for the timing half both need.

**Idempotency is an active run, not an open task.** A schedule filing a
task checks `Store.HasOpenTaskWithTag` for its own firing tag; a
schedule running a suite checks `Store.HasActiveRunForSchedule`, since a
firing there is a whole run over as many passes as the suite's mode asks
for, not one task. `suite_run.schedule_id` is what that reads (NULL for
a run a human started, which is also how a database created before this
migrates -- `ensureSuiteRunScheduleColumn`, the same
probe-then-`ALTER TABLE` approach as every other migration in
`store.go`). Both checks
have the same consequence: a suppressed firing leaves `NextRunAt` where
it was, so it is delayed rather than skipped.

**Resolved fresh at firing time.** `fireSuiteSchedule` re-reads the
`Suite` on every firing, exactly as `fireTaskSchedule` re-reads a
`Template`, so editing a suite changes what every schedule pointing at
it runs *next*, with no push step; the schedule's own `Title` is kept in
sync with the suite's name as a display cache the same way. A suite
deleted out from under a schedule fails that one firing with a plain
error rather than starting a run of nothing -- `ui.Client.DeleteSuite`
refuses the delete in the first place (`Store.SchedulesUsingSuite`),
`DeleteTemplate`'s own guard applied to the other thing a schedule can
point at.

**What a schedule fires is fixed when it is created.**
`ui.UpdateScheduleRequest.SuiteID` repoints a suite-backed schedule at a
different suite, but converting a schedule from one kind to the other is
refused in both directions: a suite has no title or body of its own to
become a task's, and a task's content has nowhere to go in a suite, so
the honest answer is "delete it and create the other kind" rather than a
half-populated row. This is the one place a suite-backed schedule reads
differently from a template-backed one, where `templateId: ""` detaches
and leaves the last-synced content behind as an editable copy.

**Base is required for a suite-backed schedule**, matching
`CreateSuiteRunRequest`'s own rule (a suite run stacks its tasks against
one named branch and has no default to fall back to), where an ordinary
schedule may leave it blank and take the repo's default branch. Every
task a firing files is the suite's own -- `SuitePrincipal`,
`ReasonSuite`, the suite's `RequireApproval` and `AutoMerge` -- since a
schedule decides only when a run happens and what it runs against, never
what the run's tasks look like. That is why `RequireApproval` is
available here at all, incidentally closing (for suites only) this
document's long-standing "no per-schedule approval gate" gap: a schedule
running a suite that requires approval leaves each pass's tasks for a
human, where a schedule filing a task still cannot.

**UI.** `ScheduleOverlay.jsx` gains a "Fires" picker (a task, or a task
suite) offered on a new schedule only, and a suite picker in place of the
template picker and every content field when a suite is chosen -- the
same "hide fields the request would silently ignore" rule the template
picker already follows. `SchedulesList.jsx` shows a `Suite: <name>` chip
beside the existing `Template:` one, and `SuitesList.jsx`'s run list
marks a run a schedule started with a "Scheduled" chip.

No `SchemaVersion` bump: both new columns are nullable and added by an
`ensure*` migration, so an existing database upgrades in place rather
than being moved aside at deploy (`SchemaVersion`'s own doc comment on
which changes need the bump).

Tests: `pkg/model/sqlite/schedule_store_test.go` (the `SuiteID` round
trip and `SchedulesUsingSuite`), `pkg/model/sqlite/suite_store_test.go`
(a run recording its schedule, `HasActiveRunForSchedule`, the
schedule_id migration), `pkg/orchestrator/schedule_test.go` (a due
suite-backed schedule starts exactly one run and advances its timing; an
active run suppresses the next firing; a missing suite fails the firing
without advancing it), `pkg/ui/schedules_test.go` and `suites_test.go`
(creation, the suite/template exclusivity, the required base, repointing,
the refused kind change, and the delete-while-in-use guard), and
`SchedulesList.test.jsx`.

Still open, unchanged: no CLI for schedules or suites.

## Update: the task list badges every task nobody filed by hand

`Task.SuiteRun` (`ReasonSuite`) had the backend half of the "Scheduled"
badge above and none of the frontend, so a whole pass of suite-filed
tasks sat in the backlog reading as work somebody typed in. `TaskRow`
now renders a `suite` chip beside `scheduled`, same tinted fill and
same title tooltip.

A stacked task -- the merge queue's own automatic fix, `ReasonFix` --
gets a `merge fix` chip on the same rule, but only when it is *not*
nested under the task it repairs: nesting is already the explanation,
and repeating it on every child row would be noise. `TaskRow` takes a
`nested` prop for that, set by `TaskList.jsx` on the rows it puts in a
`.task-sublist` and left off everywhere a task is listed flat -- the
repo pane, and `groupByStack`'s own fallback for a fix whose parent is
filtered out of the current view or gone. Its tooltip names the parent
when there is one to name.

Tests: `TaskList.test.jsx` (the suite chip, and the merge-fix chip
present in each un-nested case but absent under a parent).

## Update: "scheduled tasks" are just schedules

The feature was named twice over, and the two names had drifted apart.
Most of it already said *schedule*: `/api/schedules`,
`pkg/ui/schedules.go` and its `Schedule`, `SchedulesList.jsx`, the
`sched-` id prefix, this document's own prose throughout. The model, the
store and the schema still said *scheduled task* —
`model.ScheduledTask`, `Store.PutScheduledTask`, the `scheduled_task`
table — and so did the two labels a human actually reads, the sidebar
entry and the list heading. The older name is also actively wrong now: a
schedule has fired a whole suite rather than a single task since the
update above, so "scheduled task" names one of the two things it does.

So: one name, the shorter one. `model.Schedule`, `Store.PutSchedule`/
`GetSchedule`/`ListSchedules`/`UpdateSchedule`/`DeleteSchedule`/
`DueSchedules`/`NewScheduleID`, and `Store.CreateScheduledSuiteRun`.
`orchestrator.fireScheduledTask`/`fireScheduledSuite` become
`fireTaskSchedule`/`fireSuiteSchedule` — the two kinds of schedule, named
after the schedule rather than after what it fires, since `fireSchedule`
above already dispatches between them. The UI's two remaining "Scheduled
tasks" labels (the sidebar entry and the list heading) become
"Schedules", and this file moves from `docs/scheduled-tasks.md` to
`docs/schedules.md`.

The tables move with it: `scheduled_task`, `scheduled_task_sequence`,
`scheduled_task_read` and `scheduled_task_grant` become `schedule`,
`schedule_sequence`, `schedule_read` and `schedule_grant`, and the child
tables' `scheduled_task_id` column becomes `schedule_id`. A database
written before this migrates in place — `Store.renameScheduleTables`,
plain `ALTER TABLE ... RENAME`, no row rewritten and no id reissued
(SQLite carries an AUTOINCREMENT table's `sqlite_sequence` entry across a
rename, so the next `sched-` id follows the last one issued rather than
starting over). It runs *before* `Init` applies the DDL, unlike every
`ensure*` migration beside it, and has to: `Statements()` would otherwise
create an empty `schedule` table beside the populated `scheduled_task`
one, leaving the rename with nowhere to go. Each step is guarded on the
old name being present and the new one absent, so it is idempotent and
safe to interrupt.

No `SchemaVersion` bump, for the reason
`ensureConfigWorkerMergerColumns`' own rename of `max_concurrent` did not
need one: an existing database migrates into the new shape rather than
being one this build cannot be re-created into.

Nothing about behaviour changes, and neither does the wire format: the
REST surface was already `/api/schedules` with `pkg/ui.Schedule`'s own
JSON field names, so no client sees this at all. What stays "scheduled"
is what genuinely describes a *task*: `ui.Task.Scheduled`, the task
list's `scheduled` chip, and `model.ReasonSchedule` all still name a task
filed automatically by a schedule.

Tests: `pkg/model/sqlite/schedule_store_test.go` gains a database built
under the old four table names, checking `Init` carries the row, its
reads and grants, and the sequence position onto the new ones.

## Update: "task templates" and "task suites" are just templates and suites

The same double naming the section above unpicked for schedules was
still there twice over, on the two mechanisms that section leans on.
Most of both already said *template* and *suite*: `/api/templates` and
`/api/suites`, `pkg/ui/templates.go` and `pkg/ui/suites.go` with their
own `Template` and `Suite`, `TemplatesList.jsx`/`SuitesList.jsx`, the
`template-` and `suite-` id prefixes, this document's own prose. The
model, the store and the schema still said *task template* and *task
suite* -- `model.TaskTemplate`, `model.TaskSuite`,
`Store.PutTaskTemplate`, `Store.CreateTaskSuiteRun`, the `task_template`
and `task_suite` tables -- and so did the two sidebar entries and the
list headings a human actually reads.

The longer names were also saying something untrue. A template is not a
template *of a task*: it is reusable content that a schedule fires as a
task, a qualification plan runs against a candidate, and a suite runs as
one of its items -- `model.Template`'s own doc comment has said "more
callers are expected to arrive later" since it was written. A suite is
not a suite *of tasks* either; it is a suite of templates, which is what
a run turns into tasks a pass at a time.

So: one name each, the shorter one. `model.Template` and `model.Suite`,
with `SuiteItem`/`SuiteMode`/`SuiteRun`/`SuiteRunStatus`/
`SuiteTaskStatus` and the `SuiteCount`/`SuiteUntilClean`/`SuiteRun*`
constants following; `Store.NewTemplateID`/`PutTemplate`/`GetTemplate`/
`ListTemplates`/`UpdateTemplate`/`DeleteTemplate`, and
`Store.NewSuiteID`/`PutSuite`/`GetSuite`/`ListSuites`/`UpdateSuite`/
`DeleteSuite`/`SuitesUsingTemplate`/`CreateSuiteRun`/`GetSuiteRun`/
`ListSuiteRuns`/`ActiveSuiteRuns`/`CompleteSuiteRun`;
`orchestrator.SyncTaskSuites` becomes `SyncSuites`. The sidebar's "Task
templates" and "Task suites" entries and the two list headings become
"Templates" and "Suites".

The tables move with it: `task_template`, `task_template_sequence`,
`task_template_read` and `task_template_grant` become `template`,
`template_sequence`, `template_read` and `template_grant` (with the
child tables' `task_template_id` column becoming `template_id`), and
`task_suite`, `task_suite_sequence`, `task_suite_item`, `task_suite_run`,
`task_suite_run_item` and `task_suite_run_task` become `suite`,
`suite_sequence`, `suite_item`, `suite_run`, `suite_run_item` and
`suite_run_task`. A database written before this migrates in place --
`Store.renameTemplateAndSuiteTables`, `renameScheduleTables`' own shape
right down to running before `Init` applies the DDL, being guarded step
by step on the old name being present and the new one absent, and
bumping no `SchemaVersion`.

One thing that rename did not have to deal with: SQLite carries an index
across `ALTER TABLE ... RENAME` under its *own* old name, which no
`CREATE INDEX IF NOT EXISTS` in `Statements()` would ever match. The
five suite indexes are therefore dropped rather than renamed, and the
DDL immediately after creates them again under the names `schema.go`
declares -- including the unique one on `suite_run_task.task_id`, which
is load-bearing rather than cosmetic.

`model.SuitePrincipal`, the automation actor a run attributes the tasks
it files to, was `task-suite` and is now `suite` --
`QualificationPrincipal`'s own `qualification` is the shape it matches.
That one is a value in rows rather than a name in the schema, so
`Store.renameSuitePrincipal` carries existing tasks across with a plain
`UPDATE` of the two columns a run ever writes that actor to (a task's
origin, and the approval a run stamps on the tasks it files itself),
narrowed to rows whose kind is automation and whose id is exactly the
old string.

Nothing about behaviour changes, and neither does the wire format: the
REST surface was already `/api/templates` and `/api/suites` with
`pkg/ui`'s own JSON field names, and both id prefixes were already
`template-` and `suite-`, so no client sees this at all. What stays
"task" is what genuinely describes a task: `model.ReasonSuite`, the task
list's `suite` chip, and `ui.Task.SuiteRun` all still name a task filed
automatically by a suite run.

Tests: `pkg/model/sqlite/template_store_test.go` and
`suite_store_test.go` each gain a database built under the old table
names, checking `Init` carries every row, child row and sequence
position onto the new ones -- and, for suites, that the run's own tasks
and the unique index over them come across too. A third test seeds a
task carrying the old `task-suite` actor and checks `Init` brings it
onto `suite`.

## Update: a template may be bound to a repo and branch, optionally (grain/task-285)

The "templates carry no target repo or branch" section above went one
step too far. It was right that a template *had* to carry a target and
that every caller had to honour it: a qualification plan and a suite
were already built the other way, and schedules were the one caller
where a template overrode a repo the schedule had been pointed at.
Making a target impossible, though, left the many templates that only
make sense against one repo -- "cut this repo's release", "run this
repo's nightly sweep" -- with no way to say so, and their repo had to be
re-named (and could be mis-named) at every point of use.

So the target is back, optional this time. `model.Template` carries
`Target *RepoRef` and `Base string`: nil is unbound, which is what every
existing template is and what a template created without a repo stays,
and a non-nil Target binds the template to that repo, with Base pinning
a branch within it or staying empty for the repo's default.
`Template.FiringTarget(target, base)` is the whole rule in one place --
the binding when there is one, the caller's own when there is not --
and a bound template's empty Base is passed through rather than falling
back to the caller's, since a caller's base names a branch of the repo
the binding has just replaced.

Every caller that turns a template into a task goes through it:

- `orchestrator.fireTaskSchedule` targets the binding over the
  schedule's own Target/Base, and resyncs the schedule's row to it,
  alongside the Title/Body/AutoMerge/Reads/Grants display cache it
  already kept there.
- `ui.CreateSchedule`/`UpdateSchedule` apply the same rule at the point
  a schedule is created or attached to a template, so a schedule reads
  as what it will actually fire rather than only after its first firing.
  `repo` stops being required outright on `CreateScheduleRequest`: a
  schedule fired from a bound template needs none, every other schedule
  still does.
- `fireSuitePass` resolves target and base per item, so a suite can mix
  repo-agnostic items with ones bound to a repo of their own and each
  lands where it belongs.

Qualification is the one caller a binding cannot move, and the check the
earlier section deleted comes back with it. A qualification run exists
to test one candidate's own branch, so its tasks still target
`candidate.Repo`/`candidate.Branch` -- a bound template's branch never
moves one. A template bound to a *different* repo is instead refused
entry to that repo's plan: by `ui.Client.PutQualificationPlan` when a
plan is saved, by `ui.Client.UpdateTemplate` when a template a plan
already schedules is bound away from it (`DeleteTemplate`'s own "do not
strand a caller that points at this" care), and by
`CreateQualificationRun` as the backstop for a template bound after the
fact.

`template` gets `target_owner`/`target_name`/`base` back. They are
stored empty rather than NULL, which is what makes
`ensureTemplateBindingColumns` a plain probe-then-`ALTER TABLE ADD
COLUMN` in the direction every other migration in `store.go` goes: a
database from before templates could carry a target has the columns
already, and its values are exactly the bindings they now mean, so it
keeps them instead of dropping them, while one from the era in between
gets them added empty and every template in it stays unbound.
`ensureTemplateNoTargetColumns`, which dropped them, is gone -- left in
place it would have dropped the new ones on the next start.

Frontend: `TemplateOverlay` gains the two fields, optional and labelled
so, with a cleared repo meaning "unbind"; `TemplatesList` chips the
binding on the rows that have one and searches it; `ScheduleOverlay`
replaces its repo and base fields with a line naming where the schedule
will fire once a bound template is chosen; `SuiteOverlay`'s picker
labels a bound item with its repo; and `RepoReleases`' qualification
picker leaves out templates bound elsewhere, matching what the API will
accept.

## Update: the non-task list pages get an order of their own (grain/task-327)

The two sections above hand the schedules and templates pages
`TaskList.jsx`'s toolbar but not its rows: no drag handle, because
neither list has a backlog to reorder, and so also no "Select all" row
under the toolbar to space the first row away from it. Beside the task
list the result read as unfinished -- rows crammed against the
filter/sort panel, and nothing at the left of a row where a task row
carries its state and its handle. The repos page had the same two gaps
plus no sort menu at all, and the suites page had no toolbar whatsoever.

So all four get a *display* order, `ui/src/listOrder.js`: a list of ids
per page in `localStorage` (`grain.list-order.<page>`), the same
browser-local treatment the board's columns and the theme mode already
get, and deliberately nothing the daemon ever sees -- dragging a repo to
the top does not change what runs next, it changes where the row is for
the person who dragged it. It is each page's default sort ("Custom
order", the slot `TaskList`'s "Backlog order" occupies), falling back to
the alphabetical comparator each list already sorted by for every item
nobody has dragged, so a list nobody has touched looks exactly as it did
-- with handles on it. Picking any other order withdraws the handles,
the rule `TaskList` already applies to the backlog: there is nowhere for
a drop to land in an order the page computes.

`ReorderableList` in `ListPrimitives.jsx` is the gesture itself (the
`<ul>`, the `<li>`s, the drop highlight and the trailing "drop at the
end" zone), with each page rendering its own row through a render prop
so the handle sits inside the row's own flex line. `--list-rows-gap` in
`style.css` is the missing "Select all" row's vertical space handed back
to the four lists as a margin. And the repos list gains a status dot
ahead of each name (`state.js`'s `repoActivity`): running when an agent
is working there this moment, an open ring when work is outstanding but
none of it moving, grey when every task there is closed -- so a repo
with something happening in it is visible without reading its counts.

## Update: a schedule's time of day is read on the deployment's clock (grain/task-368)

"No per-schedule timezone" (the `Recurrence` section above) was the right
call and was taken one step too far: with no zone anywhere, a time of day
was read against UTC, which is not a decision anybody made -- it is what
a container with no `/etc/localtime` happens to be. A schedule saying
"daily at 09:00" fired at two in the morning where this deployment's
operator actually is, and the only way to ask for nine was to write 17:00
and remember why.

There is still no zone per schedule. There is one per *deployment*:
`model.Config.TimeZone`, an IANA name in `grain_config`, defaulting to
`America/Los_Angeles` and backfilled to it for a database that predates
the column (`Store.ensureConfigTimeZoneColumn` -- the one config
migration whose backfill is not a zero value, because the clock those
schedules were already firing against was accidental rather than chosen).
An operator who does want UTC now has a way to say so, which is the half
nobody had.

`Recurrence.Next` takes a `*time.Location` (nil is UTC, its old
behaviour) and `orchestrator.reconcileSchedule` resolves one per cycle
from the config row `RunCycle` already refreshes, so a changed zone
retimes the next occurrence rather than waiting for a restart. Three
details are load-bearing:

- **Daily, weekly and monthly walk the calendar, not the clock.**
  `nextDaily`/`nextWeekly` advance with `AddDate`, which keeps the
  wall-clock time it started from, so "09:00" stays 09:00 across the two
  days a year that are 23 and 25 hours long rather than sliding an hour
  with the offset.
- **`everyNHours` ignores the zone entirely.** A duration between
  instants is the same duration everywhere, daylight saving included --
  which is exactly the difference between "every 24 hours" and "daily at
  09:00", and worth keeping distinct now that the two can disagree.
- **An hour that does not exist that day still fires.** `time.Date`
  resolves 02:30 on a spring-forward morning as 03:30, so such a schedule
  runs once, an hour late, rather than being skipped.

`pkg/model` imports `time/tzdata`: a zone has to resolve inside the
deployment image, and "the base image was slimmed and every clock quietly
fell back to UTC" is the failure this whole change exists to end.

Frontend: `ScheduleOverlay`'s time field is labelled with the zone and
its current abbreviation ("Time (America/Los_Angeles, PDT)") instead of
"Time (UTC)", `SchedulesList` prints next/last run on that same clock,
and the zone itself is chosen on Settings' General tab. The rest of the
UI moved with it -- `ui/src/time.js` and `TimeZoneContext` -- so a
schedule's next run and every other time on screen are read against one
clock, which is the one the daemon fires against.

## Update: overlapping is a per-schedule choice (grain/task-67)

"A previous, unfinished firing suppresses the next one" (the first
section above) stays the default and stays the recommendation. What
changed is that it is no longer the only behaviour a schedule can have:
`model.Schedule.Overlap` is a `model.OverlapPolicy`, either
`OverlapSkip` (the zero value) or `OverlapAllow`, and
`fireTaskSchedule`/`fireSuiteSchedule` make their unfinished-firing check
-- the tag query for a task schedule, the active-run query for a
suite-backed one -- only when the policy asks them to.

Everything the check itself means is unchanged. A suppressed firing
leaves `NextRunAt` where it was, so it is delayed rather than skipped and
fires as soon as the previous one closes; `advanceSchedule` is not
reached at all in that case, exactly as before.

The default carries through every layer, because a schedule quietly
gaining the ability to pile firings up is the failure mode this whole
gate exists to prevent:

- **Store.** `schedule.overlap` is `TEXT NOT NULL DEFAULT 'skip'`, and
  `ensureScheduleOverlapColumn` adds it to a database written before it
  existed with `'skip'` in every row -- which is what every schedule on
  such a database already did. `putSchedule` writes the zero value down
  as `skip` rather than as an empty string, so the column never holds a
  value that needs interpreting.
- **API.** `overlap` is `"skip"` or `"allow"` on
  `POST`/`PATCH /api/schedules`, absent means `skip` on create, `nil`
  means "leave it alone" on update (an empty string is refused rather
  than read as either), and a `Schedule` on the way out always names one
  of the two.
- **Frontend.** `ScheduleOverlay` offers it beside the cadence ("If the
  last run has not finished"), since how often a schedule runs and what
  happens when a run outlasts the gap are one question; `SchedulesList`
  chips "Can overlap" on the rows that can and nothing on the rest.

No `SchemaVersion` bump: this adds a column an older database can be
migrated into (`ensureScheduleOverlapColumn`), which is exactly the case
that bump is *not* for (`pkg/model/schema.go`'s own doc comment).

## Update: a schedule can be run now (grain/task-227)

The first section above closes with "somebody who wants it now still
files the task by hand, which is where 'now' was always meant to come
from". That is still the right answer to *creating* a schedule -- a
"daily at 09:00" written at four in the afternoon must not file a task at
four in the afternoon -- but it was the wrong answer to wanting one
firing today, because filing it by hand means copying the template's
content into the new-task form and remembering which capabilities,
reads, writes and custom fields that schedule carries. All of it is
already written down on the schedule.

So there is now a way to ask for a firing outright: `POST
/api/schedules/{id}/run` (`ui.Client.RunScheduleNow`), behind a "Run now"
button beside Pause and Delete in `ScheduleOverlay`. It fires the
schedule rather than imitating it, and reports what it did --
`ui.ScheduleRun`: the task it filed or the run it started, plus the
schedule as it now stands.

**It is an extra firing, not a rescheduling.** `NextRunAt` is left
exactly where it was, so the occurrence already scheduled still happens
when it was going to; `LastRunAt` moves, because the schedule really did
fire; and the display cache a template-backed or suite-backed schedule
keeps is resynced by this firing exactly as the cadence's own firing
resyncs it. Same reasoning as pausing and resuming keeping a schedule's
firing time: what makes a pause a pause rather than a reset is that it
does not touch the cadence, and neither does this.

**A paused schedule fires.** `Enabled` is what stops the cadence
(`Store.DueSchedules` never returns a disabled schedule), and a human
asking for one run is not the cadence. It stays paused afterwards --
`RunScheduleNow` writes nothing to `Enabled`.

**The overlap policy is not walked round.** A schedule set to
`OverlapSkip` whose previous firing has not finished is refused, with a
409 (`scheduleBusyError`) naming the schedule and saying what to do about
it, rather than filing on top of it. The gate is there so a chore cannot
have two copies of itself running and a button is no reason to bypass it;
what does differ from the cadence is that the refusal is *reported*,
because here somebody pressed something and is owed an answer, where a
suppressed cadence firing is silently delayed.

### Firing moved to `pkg/model`

`pkg/ui` does not import `pkg/orchestrator` (`pkg/ui/sandbox_health.go`'s
own doc comment: a presentation-layer package importing core dispatch
logic runs the wrong way), so the firing code the reconciler used to own
could not be called from the HTTP layer where it stood. The choice was
between a second implementation in `pkg/ui` and moving the one there is,
and a second implementation is exactly the drift this feature cannot
afford: the moment the two diverge, a template stops being re-read, or
custom fields stop being sifted against their declarations, on one path
and not the other.

So `fireTaskSchedule`, `fireSuiteSchedule`, `firingCustomFields` (now
`Store.FiringCustomFields`, since `orchestrator.fileReviewTask` sifts a
review template's values the same way) and `advanceSchedule` (now
`recordFiring`) are `pkg/model`'s, alongside `CreateScheduledSuiteRun`
and `CreateQualificationRun`, which already file tasks from the same
place. Two entry points sit on top of them, and the boolean between them
is the whole difference:

- `Store.FireSchedule(ctx, sched, now, loc)` -- the cadence's firing,
  which advances `NextRunAt` to the next occurrence.
- `Store.FireScheduleNow(ctx, sched, now)` -- the one somebody asked for,
  which does not, and needs no location at all because nothing on that
  path reads the cadence.

Both report a `model.ScheduleFiring`: the task id, the suite run id, or
`Skipped` for a firing the overlap policy suppressed. The reconciler
ignores `Skipped` (that firing is delayed, not dropped, and nobody is
waiting on it); `RunScheduleNow` turns it into the 409 above. The
`scheduler` principal moved with them as `model.SchedulePrincipal`,
joining `SuitePrincipal` and `QualificationPrincipal`, and `firingTag` as
`model.ScheduleFiringTag`.

`orchestrator.reconcileSchedule` is what remains: list the due schedules,
resolve the deployment's clock once per cycle, fire each one, and let one
schedule's failure cost no other schedule its turn.

## Update: "not before this date" is a deferral, not a schedule (grain/task-386)

A schedule is a cadence: it recurs, and it always refires. There is no
one-shot and no start date, deliberately -- `CreateScheduleRequest` has
no `DependsOn` for the same reason. So the standing question a schedule
looks like it should answer and does not is "run this task once, but not
before the 14th", and until now grain could not express it anywhere:
`DependsOn` waits on tasks rather than dates, and a date in a task's own
body is a note to a human that holds nothing. It cost two agent runs to
find that out, on a task filed twice asking not to start before a date
and dispatched early both times
([`docs/test-runs/2026-09-08-nightly-week-one-still-early.md`](test-runs/2026-09-08-nightly-week-one-still-early.md)).

That question now belongs to deferring, one page over: a deferral may
carry a wake time (`model.Task.DeferredUntil`), and
`orchestrator.reconcileWake` picks the task back up on the first cycle
at or after it -- the same shape as `reconcileSchedule`, a stored
instant that has come round and is acted on once, and it sits next to it
in `Reconcilers` for that reason. The two stay distinct in the way that
matters: a schedule fires a *new* task every time it comes due, and a
wake time releases the *one* task somebody put aside. A recurring
reading is still a schedule; "this particular task, not before Monday"
is a defer with a date on it.

### Scheduling a task at the moment it is filed (grain/task-1405)

A wake time could only be set on a task that already existed -- the
task pane's Defer with its "Until" date, or `grain defer -until` -- so a
person could schedule work for later in two steps and an agent could not
do it at all: `propose_task` had nowhere to put a date, which is the
same hole the nightly reading fell through. Now:

- `propose_task` takes `not_before` (a date, meaning the start of that
  day in UTC, or an RFC 3339 time). The proposal is filed deferred with
  that wake time, by the live filer (`ui.CreateTask`) and the end-of-run
  relay (`orchestrator.relayProposedTasks`) alike, and the call's result
  says when it may start. An unreadable value refuses the call; a time
  already past files the task undeferred.
- `POST /api/tasks` takes `deferredUntil` (RFC 3339) for the same thing
  in one request (`ui.CreateTaskRequest.DeferredUntil`).

Approval is untouched by either: a proposal filed approved wakes onto
the queue, and one waiting for a human wakes back into proposed. Both
read `deferred` until then, so a task's `approved` field (GET
/api/tasks) is what says which; the task pane's "Picks itself back up
on ..." line names where it wakes to, and `propose_task`'s result reads
it rather than the state to say whether a human still has to approve
the proposal.

## Update: what a firing composes is written down once (grain/task-405)

What a firing *files* -- the fields the task carries, and which of the
schedule, the template, or the suite run decided each of them -- was
resolved separately by `fireTaskSchedule`, `fireSuitePass` and
`CreateQualificationRun`, with precedences that had drifted apart. It is
now one algorithm (`pkg/model/compose.go`, `model.Composition`), which
all three file through and which two new endpoints preview with:

- `GET /api/schedules/{id}/composition`
- `GET /api/suites/{id}/composition?repo=&base=`

Both resolve exactly what the firing would, without writing anything, so
a UI can show a human what a schedule or a suite will do before it does
it (`ui/src/components/CompositionPanel.jsx`, under the schedule and
suite overlays and in the run form).

`docs/composition.md` is the rule and the table: content from the
template when there is one, placement from a bound template and
otherwise from whatever fired it, disposition from the run that owns the
batch -- plus the short, honest list of fields that can be set somewhere
and are read nowhere.

## Update: an inline schedule can say what network its firings get (grain/task-427)

`docs/composition.md`'s table had one cell that read *deployment* where
every other column read *template*: the network access of a task filed
by a schedule that fires no template. `model.Template.NetworkAccess`
(grain/task-269) fixes the network for everything a template files, but
`model.Schedule` had no field for it, so an inline schedule's firings
could only ever take `Config.NetworkAccess`, and a recurring job that
had no business reaching the internet had to have its content moved into
a template before it could say so.

It has one now: `Schedule.NetworkAccess`, the `schedule.network_access`
column (migrated in place by `ensureScheduleNetworkAccessColumn`, empty
for every existing row, which is what every schedule already did),
`networkAccess` on the create and update requests, and the same
three-way select on the schedule form the template form already had --
offered whatever this deployment's sandbox backend is, since a schedule
outlives the backend and the firing is where "this backend cannot
withhold a network" is asked. Like every other content field, it is the
template's while a schedule fires one, resynced onto the row as a
display cache each time it fires.

The reasoning for adding it rather than writing the gap down as
deliberate: an inline schedule is a task on a cadence, and it already
carries the whole of what a task may touch -- the repos it reads, the
repos it may have work landed in, the capabilities it holds. The network
was the one member of that list it could not name, so the restrictive
answer cost a template and the permissive one cost nothing, which is the
wrong way round for an isolation setting.

One thing moved with it. `model.LayerDeployment` used to be reachable
only from an inline schedule, since that was the only firing with
nowhere to say a network at all; it now answers for *any* composed
network access that is empty, template-backed or not
(`model.networkSourceOf`). An unset network is not a value but a
deferral to `Config.NetworkAccess`, so a preview that said "from the
template" over a blank was naming a layer that had decided nothing.

## Update: a draft composes too (grain/task-426)

Both endpoints above are keyed by an id, so the only thing they can show
is the saved row -- the schedule overlay said "as saved, save to see
edits here", and a schedule or suite being created got no preview at
all, which is the moment somebody most wants to know what they are about
to file. Two more endpoints take the request body instead:

- `POST /api/schedules/composition`, body `CreateScheduleRequest`
- `POST /api/suites/composition?repo=&base=`, body `CreateSuiteRequest`

They write nothing and compose through the same `pkg/model/compose.go`
the firings do, resolving whatever template or suite the draft names out
of the store on every call rather than snapshotting it. The
requirements that belong to *creating* the row -- a cadence, a title, a
repo, a branch for a suite run, no content alongside the template that
decides it -- stay where they were, because a half-filled form fails
every one of them and still has a composition worth showing.

The schedule and suite overlays now preview themselves through those,
recomposing as the form is edited.

## Schedules a repository declares

A schedule does not have to be a row somebody typed here. A repository
can declare its own -- templates, suites and schedules as files in its
`.grain` directory, read from the repository and kept level with it --
which `docs/declarations.md` describes in full (grain/task-60).

What arrives from a repository is an ordinary `schedule` row with an
ordinary `template_id` or `suite_id`, so everything above holds for it
unchanged: the same cadence, the same overlap rule, the same firing, the
same composition preview. The differences are all at the edges. Its id
carries the `repo:` prefix, the repository rather than this deployment is
the author, and grain refuses to edit or delete one here -- the file in
the repository is the thing to change. Off unless an operator turns it on
for that repository, and bounded so that a declaration can say what work
to do there and when, but never what that work may touch.

## Update: a workflow's own cron is a schedule too (grain/task-344)

The fourth place a `schedule` row can come from, and the only one nobody
writes a schedule for: a workflow in a repository's `.grain/workflows`
that says `on: schedule`. That cron is translated into one of these rows
-- `pkg/ci`'s `ParseCron` into a `model.Recurrence`, `pkg/repodecl` into a
declared schedule -- and what it fires is `system:workflow-review:<stem>`,
one of grain's own templates: run that workflow, read the run, report on
it. `docs/declarations.md` has the file-level description, the table of
which crons translate, and why the rest are refused rather than rounded.

Three things about it belong here rather than there.

**There is no cron in `pkg/ci`, deliberately.** A workflow cron is a
recurring claim on this deployment's agents and sandboxes, and everything
above is the scheduler for those: one pane, one pause switch, one firing
history, one overlap rule. A timer inside the CI engine would have been a
second scheduler that none of that reaches.

**A firing is two acts across two ticks.** `reconcileSchedule` fires the
schedule, which for this kind files nothing: it writes the firing down
(`model.ScheduledWorkflowRun`, one row per schedule) and leaves it. The
`scheduled-workflows` reconciler, next in the cycle, starts the CI run for
that row; a later tick -- minutes or hours later, whenever the run
actually ends -- collects the conclusion and *then* files the review task,
with the run, its verdict and the failing jobs already in the task's
opening comment. That reader is why the objection to a cron here was never
"a clock starts a run": it was a run nobody reads.

**The review is filed when the run finishes** (grain/task-351). The first
cut filed it at the due tick, which dispatched an agent to watch a suite
that had just begun -- for this deployment's own nightly, 02:11 against a
suite whose longest job alone is allowed two hours, on an agent run budget
of about the same. The prompt asked it to report what finished and say the
rest was unread: honest, and the expected outcome of every long suite
rather than an edge case. Pulling the two apart is `reconcileBaseBranchCI`'s
shape -- record the run, collect the verdict on a later tick, file the task
then -- and the record it hangs on is off `SettingsTables` for
`base_branch_ci`'s reason.

Two consequences worth stating, because they are where this differs from
every other schedule:

- **A firing is unfinished until its review has been read.** `Overlap` asks
  two questions for these (`Store.workflowFiringInFlight`): is the run
  still going, and is the review of it still open. Skipping while either
  holds is what stops tonight's nightly stacking on a morning somebody is
  still reading.
- **Neither question waits for ever** (grain/task-493). A suppressed firing
  files nothing, records nothing and leaves `NextRunAt` in the past, so
  while one is suppressed the poll re-asks and re-skips every cycle: a
  firing that never finishes does not delay the schedule, it stops it, and
  the thing that would have noticed a suite which stopped running is this
  review. The run half is bounded by `scheduledWorkflowDeadline` (12h,
  well inside a nightly's cadence). The review half is an ordinary agent
  task, which can stay open indefinitely -- parked on an `ask_question`,
  waiting on a human -- so it is bounded by `model.workflowReviewHold`:
  `workflowReviewHoldCadences` (3) occurrences of the schedule's own
  recurrence after the review was filed, and never less than
  `workflowReviewHoldFloor` (24h). A multiple of the cadence rather than a
  fixed duration because what is bounded is measured in missed firings,
  which means the same thing to a nightly and to a weekly. Past it the
  schedule fires again and the abandonment is *reported* rather than
  worked around: `model.AbandonedReviewComment` is written on the
  abandoned review itself (which is left open, and left parked if it was
  parked -- a stale report is not an unimportant one),
  `ScheduleFiring.OpenReview` names it, `reconcileSchedule` logs it, and
  "Run now" refused by a review that is still within the hold says which
  task to close. What is bounded is the waiting and not the policy: these
  schedules are still `OverlapSkip` and nothing can change it, because a
  nightly whose previous review has not finished must not have a second
  one filed on top of it.
- **A run with no verdict is still reviewed.** One that could not be
  started after `scheduledWorkflowStartAttempts` tries, one cancelled, one
  interrupted with the daemon that was running it, one still going
  `scheduledWorkflowDeadline` after it came due: each is concluded as
  errored and files the review saying so. "The nightly did not complete"
  is exactly the report that is invisible from everywhere else -- and
  since nothing fires again until a firing is reviewed, giving up loudly
  is also what keeps the cadence alive.
- **But the last of those is a different review from the others**
  (grain/task-386). A run grain stopped waiting for is a run that *is
  there*: it was left alone rather than cancelled, its finished jobs have
  logs, and the job that hung is still writing one. So the row records
  that the run had not ended (`ScheduledWorkflowRun.Unfinished`, which is
  on the row rather than inferred from `Detail` because the review is
  often filed by a later cycle than the one that gave up), the task's
  first comment says so and sends the agent to read the live log with
  `get_ci_log`, and the prompt names three kinds of night rather than
  two. Telling that reviewer to report a dead night and stop would throw
  away the only evidence there is of where the suite hangs.

**A scheduled run is always a recheck** (grain/task-347). grain CI is
content-addressed, so the same commit asked about twice is one answer
served out of the cache in seconds. That is right for a push and fatal for
a cron: on a day the branch has not moved, a firing would replay
yesterday's verdict and be reviewed as a green night in four seconds -- a
true statement about the commit and a false one about the suite, whose
jobs (a shuffled and repeated Go suite, a soak) exist precisely because
running them *again* is the finding. So `startScheduledWorkflowRun` sets
`ci.Request.Recheck`: nothing is served from the cache. What the run
learns is still written back as the newest verdict for its keys, which
carry the schedule event, so a night's result never stands in for a push's.
It is grain's choice rather than a caller's, because
`Recheck` is not caller input in the first place (it is `json:"-"`, with
`TrustedRelease` and the capability list) and because "run this again on a
cadence" is the whole of what a cron means. It costs no de-duplication: a
recheck hashes into a plan key of its own, so it never joins the run whose
answer it is re-asking, and two rechecks of the same plan still join each
other. The review prompt says the run was not cached, so that a fast night
is read as a fast night rather than as a replay.

**"Run now" fires the same two acts.** A workflow schedule run by hand
records a firing and answers `ScheduleRun.workflowRun` rather than a task
id: grain starts the run on its next cycle and files the review when that
run ends, exactly as the cadence would.

## Update: a schedule file can run a workflow on a branch (grain/task-3159)

A workflow's cron runs it on the default branch and nowhere else, which
left this repository's release candidate promoted with no nightly, soak or
live-provider run of its own. A `.grain/schedules` file can now say
`workflow: <stem>` with a `base:`, and what it stores is the cron's own
row with that base on it: `template_id` is `system:workflow-review:<stem>`,
so `Store.fireSchedule` sends it down `fireWorkflowSchedule` like any cron
-- a `ScheduledWorkflowRun` with `Branch` set to the base, a trusted
schedule run of the workflow against that branch on the next cycle
(`scheduledWorkflowRef` takes the branch as given and only asks GitHub when
there is none), and the review once it ends. The review is filed on the
base and titled with it (`model.WorkflowReviewTitle`), and its opening
comment names the branch and the schedule, so the default branch's review
and the candidate's are two different tasks to a reader as well as to the
store.

The one difference from a cron is provenance. A cron's schedule points at
its workflow file, which is what `scheduledWorkflowPath` narrows the run
to; this one points at the schedule file, so the run is narrowed to
`.grain/workflows/<stem>.yml` instead -- which is why `workflow:` refuses a
workflow kept as `.yaml`. It also refuses `overlap: allow`, which a cron
cannot say either: one `ScheduledWorkflowRun` per schedule means one firing
in flight. `docs/declarations.md` has the file format.

## Update: only a run that did not pass is reviewed (grain/task-3160)

Every workflow firing used to end in a review task, whatever its run
concluded, which made a green night -- most nights -- a sandbox, a queue
slot and an agent's tokens spent to report that everything passed. Now
the run is the check. `collectScheduledWorkflowRun` records the
conclusion and hands the firing to `settleScheduledWorkflowRun`, which
reads the run back and, for one that concluded success with at least one
job passing (`scheduledWorkflowRunPassed`), settles the row with
`ScheduledWorkflowRun.ReviewNotNeeded` and files nothing. Anything else
-- failure, errored, cancelled, lost, never started, waited out, a run CI
no longer has, or a "success" with no job in it -- goes to
`fileWorkflowReview` exactly as before.

- **The overlap rule.** `ScheduledWorkflowRun.Reviewed` is now `Settled`:
  a review task *or* `ReviewNotNeeded` ends the firing, so the run half
  of `workflowFiringInFlight` lets the next night fire, and with no
  review task open the review half has nothing to hold it with. An
  explicit column (`scheduled_workflow_run.review_not_needed`) rather
  than an empty `ReviewTaskID`, because empty already means "not filed
  yet".
- **Restarts.** A row with a conclusion and neither a review nor
  `ReviewNotNeeded` is re-entered at `settleScheduledWorkflowRun`, which
  decides the same way every time: a green night is one write, and a
  review is guarded by its firing tag as before.
- **Per schedule.** `Schedule.WorkflowReview` (`schedule.workflow_review`,
  `model.WorkflowReviewPolicy`): empty or `on-failure` is the above,
  `always` reviews a green night too. Written by `review:` on a
  workflow's cron entry (`ci.ScheduleTrigger.Review`), on a
  `.grain/schedules` file with `workflow:`, or on a settings row
  (`docs/declarations.md`, "Which runs are reviewed").
- **Seeing a green night.** `ui.Schedule.LastWorkflowRun`, filled in by
  `ListSchedules`, is the schedule's last firing: run id, branch,
  workflow, conclusion, and the review task or that none was needed. The
  schedules list prints it on the row and the schedule pane above the
  form, linking the CI run (`/ci/<run>`) and any review;
  `ui.Schedule.WorkflowReview` chips "Reviews every run" when it is
  `always`. `list_ci_runs` says "Started by schedule <id>." on a run
  whose `RequestedBy` is `workflow-schedule:<id>`, and the CI page already
  shows that requester. The daemon logs a green settle as
  `schedule <id>'s grain CI run <run> ... passed; no review filed`.

## Update: a suppressed firing says so on the schedules page (grain/task-544)

The bound above stops an unread review holding a schedule for ever. It
does not tell anybody they are inside one, and until it ran out the
schedule looked exactly like a working schedule: a suppressed firing
files nothing, records nothing and leaves `NextRunAt` where it was, so
the page drew a next-run time that had already gone by with no hint why.
For a nightly that is up to three nights of nothing running with nobody
told.

So the fact is *derived* and reported. `model.Store.SuppressedFiring` is
the read-only half of the overlap check each `fireXSchedule` makes --
the same store questions, in the same order, asked without writing
anything -- and answers with a `model.ScheduleSuppression`: which
unfinished firing is holding the cadence
(`SuppressedByOpenTask`, `SuppressedBySuiteRun`,
`SuppressedByWorkflowRun`, `SuppressedByOpenReview`), the open task to
close where there is one, and -- for the review, the only half bounded
from here -- the instant `workflowReviewHold` expires and grain fires
anyway.

Derived rather than stored, which is why there is no column and no
migration: a suppression is a fact about right now, the task holding it
may have been closed a second ago, and a row written by a firing that
did not happen would go stale exactly when it mattered. It costs a store
read on a schedule that is *already* overdue, enabled and set to skip;
every other row answers without asking the store anything, because each
of the three conditions is a different answer to "why is this not
firing" that the page can already see (paused, `OverlapAllow`, not due
yet).

- **API.** `ui.Schedule.Suppressed` (`{reason, taskId?, until?}`), filled
  in by `ListSchedules` alone: that is the read the schedules page is
  drawn from, where a create or update response is about the write just
  made. The reason crosses the wire in `pkg/model`'s own spelling, the
  way `Recurrence.Kind` does.
- **Frontend.** `SchedulesList` chips "Not firing" on the row and prints
  the reason under it, linking the task to close;
  `ScheduleOverlay` says the same thing in the pane it opens, with the
  date grain stops waiting -- the pane being where both the things it
  points at live, the overlap setting that decides to skip and the "Run
  now" button, which is refused with the same fact while this holds
  (`scheduleBusyError`, a 409).
- **A next run that has gone by is itself drawn as a signal**
  (`scheduleSuppression.js`), suppression or not: it is what a suppressed
  firing always looks like, and also what a daemon that is not running
  looks like. Past `OVERDUE_GRACE_MS` (two minutes, several times the
  default poll interval) rather than the instant it goes by, so a cadence
  coming round does not light every row up between one poll and the next.

The other suppression a firing has is deliberately not reported this way:
`suppressOffTargetRepos` advances the cadence, so it leaves `NextRunAt`
in the *future* and does not look like this at all, and the operator was
already told at the save that narrowed the list (`StrandedWork`).

## Update: skipped firings are recorded (grain/task-3560)

The release cut of 2026-10-13 came due at 02:00 UTC while the previous
cut, task #3201, was still waiting on a Submit. Its pull request merged
at 02:09. `Suppressed` covered the nine minutes in between and nothing
covered anything afterwards. The daemon logged nothing for an overlap
skip, so the morning after, the schedule looked the same whether its
firing had been held for nine minutes or never happened.

- **What `skip` does was already a catch-up, and is unchanged.** An
  overlap skip leaves `NextRunAt` where it was, so the first poll after
  the blocker closes fires the held occurrence, once. An occurrence that
  comes round while one is held does not move `NextRunAt` either. It is
  absorbed into the same single late firing, and `recordFiring`'s loop
  then lands the cadence on its next regular occurrence. That is the
  bounded, collapse-to-one catch-up the task asked for, so no `defer` or
  `catch-up` option was added, and `release-candidate.yml` needs no
  change. The hold could only be bounded more tightly by dropping
  occurrences, which would lose the late cut the operator wants.
- **Store.** A runtime table, `scheduled_firing_skip` (`model.ScheduleSkip`), is
  named `scheduled_*` like `scheduled_workflow_run` because it is not a
  settings companion and is not exported to the state repository. It has
  one row per (schedule, occurrence), so repeated polls of a held
  occurrence collapse into one row, and keeps `ScheduleSkipsKept` (20)
  rows per schedule. A row holds the reason (`ScheduleSuppressionReason`,
  extended with `paused`, `offTargetRepos` and `teamArchived`), the
  blocking task and its state, and the outcome: held (`""`), `fired`
  (with `FiredTaskID`, `ResolvedAt`, and `Folded`, the number of later
  occurrences absorbed) or `dropped` (off-target repo, standing work
  off, or a held occurrence an edit retimed the cadence past --
  `DropStrandedScheduleSkips`, called by `ui.UpdateSchedule` when it moves
  `NextRunAt` and by a firing before it settles what it held).
- **Firing.** `fireSchedule` records the `ScheduleFiring.Skip` every
  skip path now returns, but only for a cadence firing. "Run now" is
  already told why it was refused. A firing that fires settles every
  held row of its schedule. The reconciler records paused
  (`PausedDueSchedules`) and archived-team schedules itself, because
  neither reaches `FireSchedule`.
- **Log.** One line per new record (`logScheduleSkip`), not one per
  poll.
- **API and frontend.** `ui.Schedule.skips`, newest first, is filled in by
  `ListSchedules`. The row shows the newest skip once nothing is
  suppressing the schedule. The pane lists them all
  (`scheduleSuppression.js`'s `describeSkip`).
- **Not done: notification.** This deployment has no inbox entry or
  notification for schedule problems, and no way to mark a schedule as
  important. The inbox is built from tasks and base-branch gates. A
  skipped firing therefore shows on the schedule, in its history and in
  the log, and nowhere else. (Since grain/task-3748 a held firing is also noted on
  its blocker; see the next section.)

## Update: a held firing says so on its blocker (grain/task-3748)

A firing held by an open task now writes one comment on that task,
attributed to the schedule, once the task is waiting on a person
(`awaiting_submit`, `awaiting_reply` or `awaiting_ack`). On 10-13 it would
have told #3201, which was waiting on a Submit, that the release cut was
held on it.

- **Why the blocker, not a new inbox entry.** A task waiting on a person
  is already in that person's inbox. What it was missing was the reason
  it mattered. This was option (b) in task 3748. The alternative was an
  `important: true` field on schedule declarations with an inbox gate of
  its own, and it was not built. Every schedule gets the note, since only a
  held occurrence whose blocker waits on a person qualifies.
- **When.** Every cycle that skips an occurrence passes the skip it
  recorded to `Store.NoteHeldScheduleOnBlocker`. The skip carries the
  blocker's state as read that cycle, so a blocker that only moves into a
  human-wait state later is noted then. Only the open-task and
  open-review reasons qualify (`ScheduleSkip.WaitsOnPerson`). Paused,
  archived-team, off-target and still-running skips have no task to
  write on.
- **Once.** `scheduled_firing_skip.noted_task_id` records the blocker
  already noted. The note is claimed before the comment is written and
  released if the write fails. So a held occurrence gets one note however
  many polls skip it again, including the occurrences folded into it, and
  a second blocker for the same occurrence gets one note of its own.
- **What it does not do.** The comment is a store write, so it does not
  un-park the task or dispatch it (`noteAbandonedReview` works the same
  way). It does not change the hold, either.

## Update: every N minutes (grain/task-424)

A schedule can now fire more often than hourly. `RecurrenceEveryNMinutes`
(`"everyNMinutes"` over the API, `every: {minutes: N}` in a settings
document or a repository's `.grain/schedules/*.yml`) is `everyNHours` at
a finer grain, for the jobs an hour is too coarse for -- the planned
system release manager wakes every 30 minutes.

- **Counted from the last firing.** `Recurrence.Next` adds N minutes to
  the instant it is given, exactly as `everyNHours` adds N hours: no
  wall-clock alignment, and no zone (a duration between instants is the
  same everywhere, daylight saving included). A schedule created at
  14:07 with a 30-minute cadence fires at 14:37, not 14:30.
- **A floor of ten minutes.** `model.MinEveryNMinutes` is enforced by
  `Recurrence.Validate`, which `ui.parseRecurrence` (create and edit),
  repository declarations and the editor's own `min` all go through.
  Every firing is an agent run in a sandbox, and the reconciler only
  looks once a poll; a cadence finer than this buys nothing but cost.
  A row that arrives below the floor anyway (a hand-edited state
  repository) is clamped by `Next` to ten minutes rather than obeyed.
  The ceiling is a year, `everyNHours`' own, in minutes.
- **Overlap at this cadence.** Nothing about the overlap policy changes,
  but a short cadence is where it matters most: with the default `skip`,
  a firing whose previous run is still live is held, not stacked. Every
  occurrence that comes due while it is held collapses into the one skip
  record, and when the previous firing closes the schedule fires once,
  folding the held occurrences in (`ScheduleSkip.Folded`), then resumes
  its cadence. `schedule_overlap_timer_test.go` pins this at 10 and 30
  minutes under the daemon's own timer.
- **Storage.** A new `schedule.every_n_minutes` column (schema version
  33), NULL on every other kind, the same column-per-case shape as the
  other five. The settings document writes it as `every: {minutes: N}`
  and a legacy `tables/schedule.json` carries the bare column; an `every`
  block naming both `hours` and `minutes` is refused on import.
- **System schedules.** `model.SystemScheduleRecurrence` may return an
  `everyNMinutes` cadence; the seeder writes it like any other, with the
  first firing N minutes after the cycle that seeds it.
