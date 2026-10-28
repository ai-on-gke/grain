# The nightly infrastructure review

grain/task-496: a standing job that reads how this deployment's own
machinery actually performed and says which of its parameters should
move — and never moves one. The companion half is `docs/tuning-stats.md`
(grain/task-495), which is what it reads.

This describes what is implemented (`model.InfraReviewTemplateID` and
`infraReviewTemplate` in `pkg/model/system_template.go`, seeded by
`pkg/orchestrator/systemschedule.go`), not a proposal.

## What it is

- **A schedule every deployment gets.** `system:infra-review` is seeded
  the way `system:memory-agent` and `system:release-notes` already were —
  a well-known id, written once, and the deployment's from then on: how
  often it runs, whether firings overlap, and whether it runs at all.
- **Nightly**, at the same quiet hour the other two use
  (`orchestrator.systemScheduleTimeOfDay`), reading a night that has
  finished and competing with workers that are idle.
- **No target, and no capability that writes anything.** It holds
  `self-debug` and `grain-ci`, reads the state repository, and pushes
  nowhere.
- **Its output is a report and, sometimes, a proposal.** The report is
  the run's closing note. A change it wants made is a `propose_task`
  against the state repository, which somebody approves before it runs
  and merges after it does.

## Suggest, never apply

Every parameter in scope is a `grain_config` column, exported as
`tables/grain_config.json`, so a change to one is already a pull request
somebody reads. Giving the job a way to write settings would move this
deployment's behaviour overnight with nobody having read the reasoning,
and the reasoning is the whole of what a tuner is worth: the same slow CI
supports "raise `max_ci_jobs`" and "lower `max_workers`" depending on
which limit is believed to be binding, and those have opposite effects.
The sentence naming the mechanism is the part that has to be reviewed.

This is also why it has no `Target`. A target would be the state
repository — the one repository a run recommending changes to this
deployment's own settings must not be able to push to — and the gap
between "suggest" and "apply" would then be one `git push` wide.

## What a suggestion has to carry

Three things, and the prompt says a suggestion missing any of them is not
made at all. They are not a house style; they are the three ways a tuning
recommendation is wrong.

1. **The number, with its history.** Not "CI looks contended" but "`go`
   ran in 31–34 minutes tonight against 15–26 minutes last week, while
   queue time also rose." A reading with nothing to compare against says
   nothing.
2. **The mechanism.** Which limit is believed to be binding: contention
   *inside* jobs already running, or admission holding them *outside*.
   Checked against grain's own source through `read_grain_source`, not
   against intuition — a recommendation about `max_ci_jobs` made without
   reading what the admission path does with it is a guess with a number
   attached. Where the source is unreadable, the finding is reported as
   an observation rather than filed as a proposal.
3. **A decision rule for reading the result**, stated in advance: what
   would show the change worked, and what would show it was the wrong
   lever. The worked example in the prompt is the one from the night this
   was filed — if run time drops but suite wall-clock gets worse, the
   bottleneck is demand rather than concurrency, and the lever was
   `max_workers` and not `max_ci_jobs`.

## The cooldown

`model.infraReviewCooldownDays` is seven. A parameter that has moved
inside that window is left alone whatever tonight's numbers look like,
and the report says which parameters are inside it and when each comes
out.

A tuner with no cooldown is not a tuner: it moves something every night,
every measurement then spans a period in which something else also
changed, and no improvement can ever be attributed to anything — which is
the one thing the job exists to be able to do. Seven days because that is
this deployment's own default metrics window, so a parameter that has
been still for the cooldown has a full window after the change to compare
against a full window of equal length before it, which is the two-call
shape `docs/tuning-stats.md` describes and the `changes` section exists
to place the boundary for.

It is stated as a rule rather than left to a judgement the run makes each
night, because an agent looking at a bad number will always conclude that
this particular number is special.

## Reading its own history

Before it reads a single measurement it reads what it recommended last
night and what came of it, through the `self-debug` task tools: the
earlier firings of its own schedule, their closing notes, and any task it
proposed from one. "Was it taken" is answerable two ways and it uses
both — the proposal's own task record, and whether the parameter actually
moved in the `changes` section.

Without this it re-files the same recommendation every night until
somebody switches the schedule off, and being switched off is
indistinguishable from the outside from working.

## Reliability is half of it

Throughput is the reading with the obvious numbers behind it, so the
prompt names the reliability signals one at a time rather than asking for
"reliability": jobs cancelled rather than judged (particularly by a
daemon restart, which is not a verdict and can leave a cache cold for
hours), tasks stuck where nothing will move them, the same job failing
the same way across unrelated branches, and repairs the merge queue had
to file. The cross-branch flake is why the job holds `grain-ci` at all:
that grant registers `list_ci_runs` and `get_ci_log`, and `self-debug`
widens their scope to every repository this deployment remembers
(`mcp.CIWideReadGrant`). The grant decides nothing about whether any
repository's workflows run — that is read off the repo and deployment
config, never off a task — and this task has no branch for CI to run on.

Several of these have no dial behind them at all, and the prompt says to
report them anyway: the answer is a bug report, not a setting.

## Scope

In: the deployment-wide concurrency and sizing dials — `max_ci_jobs`,
`max_workers`, `max_mergers`, `max_merges_per_repo`, `merge_batch_size`,
the sandbox sizes and overcommit factors, the cache ceilings and the CI
retention bounds.

Out, and not on grounds of taste:

- **Anything that changes what counts as a verdict.** Not what makes a
  check pass, not whether a red one blocks a merge, not which checks are
  required, not whether a pull request may merge unreviewed. An operator
  ruled on 20 Sep 2026 (grain/task-471) that a fixed merge predicate must
  not be reachable by strategy, and a nightly tuner reaching into the
  gate to make its own numbers look better would be exactly that.
- **Per-repository policy**, which is somebody's decision about a
  repository rather than a dial.

## Saying nothing

"Throughput and reliability are within their usual range; nothing
recommended" is a complete report and on most nights the correct one.
A nightly job that always finds something is a nightly job nobody reads,
and by the time it has something urgent to say nobody is reading it.

## Why nightly, when most settings should not move most weeks

The cadence is how often it *looks*; the cooldown already fixes how often
it can change anything.

- **The evidence is perishable.** The host series is hourly rollups held
  in memory, bounded at a fortnight and lost on every daemon restart
  (`sysstat.DefaultHistoryHours`; `host.reaches` says how far back it
  really goes), and CI job rows are swept with the CI history. A weekly
  run on a deployment whose daemon restarted on Wednesday has no host
  series at all for the first half of the week it is reporting on — and
  the nights a daemon restarts are disproportionately the nights worth
  reading.
- **The reliability half is perishable differently.** "This job failed
  the same way on six branches tonight" is a fact about tonight; by the
  following Monday those branches have merged or been abandoned and their
  logs are swept.
- **A report that usually says nothing gets read.** A weekly one arrives
  carrying a week of everything and gets skimmed — and a tuner whose
  reports are skimmed is a tuner whose one strong recommendation is
  skimmed with them.

The cost is one sandbox a night. What it buys is visible in what the
evening the companion task was filed cost without it: an operator asking
whether throughput was healthy, about fifteen minutes of a Director
session hand-assembling numbers out of `list_ci_runs` output and one
instantaneous health reading, one parameter changed on that evidence, and
no way afterwards to tell whether it helped. A deployment that disagrees
owns the cadence and the switch from the first seeding onward
(`ui.refuseSystemScheduleContentEdit`).

## Left open

- **Nothing checks that a proposal it files was actually about a
  parameter in scope.** The bound is the prompt and the fact that the
  change lands as a reviewed pull request, not a mechanism.
- **Its history is prose.** The run reads its own earlier closing notes
  to find out what it recommended; there is no structured record of
  "recommendation → taken → outcome" that a later run could query, so a
  report that buries its recommendation is a report the next run may
  miss.
- **Merge throughput is still unmeasured** (`docs/tuning-stats.md`'s own
  "what was left"), so a recommendation about `max_mergers` or
  `merge_batch_size` has thinner evidence behind it than one about CI
  concurrency.
