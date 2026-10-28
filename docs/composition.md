# How a firing's task is composed

grain files most of its own tasks out of a combination of declarations
rather than out of one form: a schedule fires a template, a suite runs
several of them against a repo and branch, a schedule can run a whole
suite on a cadence, a qualification plan runs them against a candidate's
branch. Every one of those has to answer the same question -- what does
the task that comes out actually carry -- and until grain/task-405 each
answered it in its own function, which is how the same template came to
auto-merge when a schedule fired it and not when a suite did, with
neither place saying so.

This document is the rule, the table, and where to see it applied. The
code is `pkg/model/compose.go`, which is the only place that resolves
this now: `Store.FireSchedule`, `fireSuitePass` and
`CreateQualificationRun` all compose through it, and so do the two
endpoints a UI previews with, so a preview cannot promise something a
firing would not do.

## The rule

A composed task has three kinds of field, and each kind has exactly one
layer that decides it.

**Content** -- title, body, reads, writes, capabilities, custom fields,
network access, what a `nothing_needed` ending does. The template's, whenever the firing has a template; the
schedule's own inline fields when it has none. Never merged: a
template-backed schedule carries no per-field override, deliberately, so
"where did this body come from" has one answer and not two. A schedule
that needs a value of its own says it on the template, or fires no
template.

**Placement** -- the repo the task is filed against, and the branch it
bases off. The template's binding (`Template.Target`) when it has one,
because a binding is the template saying its content only makes sense
against that repo; otherwise whatever fired it -- the schedule's own
repo, the suite run's own repo. A bound template with no branch of its
own means that repo's *default* branch, never the branch the firing was
going to use, which belongs to another repo.

A qualification run is the exception, and says so out loud: its tasks
exist to test one candidate's own branch, so the candidate's placement
wins over everything, and a template bound to another repo is refused
entry to the plan rather than silently retargeted.

**Disposition** -- whether the task lands approved, and whether its pull
request auto-merges. The batch decides when there is a batch: a suite
run's own `RequireApproval`/`AutoMerge` are the policy for every task in
every pass of it, since "run these templates together, this way" is the
whole of what a suite is. With no batch policy, the template's own
`AutoMerge` stands, and a schedule's firing is always approved --
creating the schedule was the standing approval (`docs/data-model.md`'s
dissolved `SCHEDULED` special case).

## The table

Each cell is the layer that decides that field at that firing point.

| Field | Schedule, no template | Schedule → template | Suite run (by hand or by schedule) | Qualification run |
| --- | --- | --- | --- | --- |
| Title, body | schedule | template | template | template |
| Reads, writes, capabilities | schedule | template | template | template |
| Custom fields | schedule | template | template | template |
| Network access | schedule | template | template | template |
| Model | schedule | **schedule** if it chose one, else template | template | template |
| Repo, branch | schedule | template if bound, else schedule | template if bound, else the run | **candidate, always** |
| Auto-merge | schedule | template | **suite** | template |
| Approval | approved | approved | suite (`RequireApproval`) | plan (`RequireApproval`) |

Two cells are worth reading twice, because they are the ones that
surprised people:

- **Auto-merge in a suite run is the suite's**, not each template's. A
  template that auto-merges on its own does not auto-merge inside a
  suite that says not to. This is the batch rule above, and it is the
  reason the preview names the layer rather than only the value.
- **Network access is the one field whose empty value names another
  layer.** Whoever was asked, a `NetworkAccess` left unset is not that
  layer's answer: it is a deferral, and the deployment's own
  `Config.NetworkAccess` decides when the run is dispatched. So the
  preview says *deployment* over a blank network in every column of the
  table above, and names the template or the schedule only when one of
  them actually fixed a value (`model.networkSourceOf`). Every other
  field's blank is that layer's own answer -- "reads: none" is the
  template saying none, and no branch is the schedule choosing the
  repo's default.

- **A schedule's model beats its template's.** The model is not
  content: which model runs a recurring job is a choice about that
  cadence, and a declared schedule carries no content of its own to make
  it with. So `Schedule.Model`, when set, wins over `Template.Model`. A
  firing's sync of the template's content onto the schedule row leaves
  it alone. Like network access, an empty model is a deferral: the
  team's default model answers it at dispatch, and the preview says
  *team* (`model.modelSourceOf`). A suite-run schedule cannot choose one.

Network access was itself an exception here until grain/task-427:
`Schedule` had no field for it, so an inline schedule's firings could
only ever take the deployment's default, and a recurring job that had no
business reaching the internet had to be moved into a template to say
so. It has one now (`Schedule.NetworkAccess`, `schedule.network_access`,
the same select on the schedule form the template form already had), on
the reasoning that an inline schedule is a task on a cadence: it already
carries the whole of what a task may touch -- the repos it reads, the
repos it may have work landed in, the capabilities it holds -- and
leaving the network out put the cost on the restrictive answer while the
permissive one stayed free.

## What a firing carries besides

Not everything on a task is composed. The filing itself supplies the id,
the origin (the automation actor -- `schedule`, `suite`,
`qualification` -- and why the task exists), a schedule's idempotency
tag, a qualification instance's dependency links, and the order key that
puts it in the queue. `model.Composition.Task` is the join: the
composition's own fields, plus those.

## Fields that are set somewhere and read nowhere

Rationalizing the combination did not make every field meaningful
everywhere, and the honest list is short:

- A **suite-backed schedule** keeps `Title` as a display cache of the
  suite's name and reads nothing from its own body, auto-merge, reads,
  writes, capabilities or custom fields: a suite run takes all of those
  from the suite and its templates.
- A **template-backed schedule** keeps all of those columns as a display
  cache too -- resynced from the template each time it fires, so a list
  can render what a schedule fires without a second lookup -- but they
  are not what gets filed. Editing them would change what the row *says*
  and never what it *does*.

Neither is a field the API lets you set and then quietly discards.
`POST /api/schedules` and `PATCH /api/schedules/{id}` **refuse** a
content field sent alongside a `templateId` or a `suiteId`, naming the
fields and the template or suite that decides them
(`ui.refuseIgnoredScheduleContent`) -- the rule
`ui.refuseSystemScheduleContentEdit` already states for grain's own
schedules, one layer out. What is refused is a field that would
*change* something, not a field that was sent: a form that submits a
whole schedule and resends what it read goes on saving, and a schedule's
own cadence, overlap policy, repo, branch and pause switch stay editable
whatever it fires. Detaching (`"templateId": ""`) is the one update that
may name a template and carry content at once, since afterwards the
content is the schedule's own.

The rule is about content a caller *meant*, not about every field a call
declines to read. `PutQualificationPlanRequest.TemplateName` is still
accepted and ignored: it is the response shape's own display field,
echoed back by a form that round-trips what it read, and the name always
comes fresh from the store. `CreateSuiteRunRequest` carries no content
at all -- `suiteId`, `repo`, `base`, all three read -- so there is
nothing there to refuse.
- A **qualification plan** has an approval switch and no auto-merge one,
  which is why its tasks take the template's auto-merge. Adding one
  would be a plan-level policy like a suite's; nobody has asked.

## Seeing it

Four endpoints answer with the composition itself, resolved by the code
above without filing anything:

- `GET /api/schedules/{id}/composition` -- every task this schedule's
  next firing files. For a suite-backed schedule, that is one pass of
  the suite, plus the stopping rule (`mode`, `count`/`maxPasses`) that
  says how many passes there may be.
- `GET /api/suites/{id}/composition?repo=&base=` -- every task one pass
  of this suite files against that repo and branch. `repo` may be
  omitted, which is what the "Run…" form shows before anybody has
  chosen one: everything that is not placement is already decided.
- `POST /api/schedules/composition` -- the same answer for a schedule
  that has not been saved, given the `CreateScheduleRequest` body that
  would have created one. It writes nothing; the POST is because the
  thing being composed is a body somebody is still typing, with no id to
  be named by.
- `POST /api/suites/composition?repo=&base=` -- the same, for a
  `CreateSuiteRequest`. `repo` and `base` stay in the query where the
  saved suite's endpoint has them: they are the run's, not the suite's,
  and the body here is the suite.

The two drafts compose *as far as the draft goes*. A form halfway
through being filled in fails most of what creating the thing would
insist on -- no cadence, no title, no repo, no branch, no count -- and
still describes a composition worth showing, so those requirements stay
in `CreateSchedule`/`CreateSuite` and out of the resolution the drafts
share with them. What a draft still refuses is what it cannot resolve at
all: a template or suite that is not there, a repo that does not parse,
a custom field this deployment does not declare. Nothing is snapshotted
either -- a draft naming a template resolves it out of the store on
every call, so a preview of an unsaved schedule shows today's template
rather than a copy of one.

Each task comes back as its fields, each with the layer that decided it
(`ui.ComposedField.Source`, one of `template`, `schedule`, `suite`,
`plan`, `candidate`, `repo`, `deployment`, and per-value `Sources` when
a list field like `capabilities` combines a schedule's or template's own
grants with the target repo's default `grain-ci` grant). `ui/src/components/CompositionPanel.jsx`
renders that under the schedule overlay, under the suite overlay, and in
the run form. The two overlays hand it the form's own body and it
recomposes, debounced, as the form is edited -- so the panel follows the
edits rather than the saved row, and a schedule or suite that does not
exist yet has one too. The run form composes a saved suite by id and
recomposes as the repo and branch settle; one of grain's own schedules
keeps asking about its saved row, its content being a copy of a template
that lives in grain's code rather than one the form could describe.

## Filling a template in (grain/task-58)

A template can leave named holes in its title and body -- `{{service}}`,
`{{environment}}` -- declared as input fields (`model.TemplateField`,
`template_field`): a text box, or a picker where the field lists
options, each with an optional default and an optional "required".

`composeTemplate` substitutes them, which is what puts the substitution
behind every firing point at once rather than at each of them. The
firings nobody is watching pass no values at all, so each field's
default fills its hole and a raw `{{name}}` never reaches an agent's
prompt. The one firing point with somebody to ask is a person or an
agent filing a single task from the template, `ComposeTemplateTask`, and
its layer is `filing` -- the repo, the branch and the approval are that
one filing's, decided by whoever was looking at it, with no standing
declaration behind them.

Two surfaces reach it:

- `POST /api/tasks` with `templateId` and `fields` -- the template fills
  every blank the request left (title, body, reads, writes,
  capabilities, declared-field values, auto-merge, network access) and
  anything the request states for itself stands. A bound template's repo
  and branch win, as they do everywhere else.
- `GET /api/templates/{id}/composition?repo=&base=&field.<name>=` -- the
  question form of exactly that filing, one query parameter per value so
  a value with a comma or a newline in it needs nothing but ordinary
  escaping.

`ui/src/components/UseTemplateOverlay.jsx` is the form over both: the
template's inputs, where the task runs, and the preview under them,
re-asked whenever a value is settled rather than on every keystroke.
Nothing in the browser substitutes anything -- a second implementation
of the substitution is a second answer to disagree with the one that
gets filed.
