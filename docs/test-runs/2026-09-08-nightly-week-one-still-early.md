# The first week of the nightly suite, second attempt: still early

`docs/test-runs/2026-09-07-nightly-first-week.md` found that there was no
first week to read, and asked for the reading to be re-filed "not before
2026-09-14". The re-filed task was dispatched on **2026-09-08**, six days
early, and before the nightly's *second* scheduled firing had come due.

So this is not the reading either, and for the same reason as last time.
What is new is why it keeps happening, which is the only thing here worth
carrying forward: **grain has no way to say "do not start this task
before date D"**, so the date in that request was a note to a human
rather than a gate, and re-filing the task a third time with a date in
its prose will land it early a third time.

## The count

| | |
| --- | --- |
| `#848` merged (`d66f04b0`) | 2026-09-06 12:16 UTC |
| Scheduled firings of `cron: "11 2 * * *"` due since | **1** (2026-09-07 02:11 UTC) |
| Next firing due | 2026-09-08 02:11 UTC |
| This attempt started | 2026-09-08 01:25 UTC |

One night is on record: run 1, `34094712239`, already read as far as one
night can be read. The second scheduled firing was still 46 minutes away
when this attempt began. The task asked for the count to be checked first
and for "fewer than about five runs" to be reported rather than worked
around; the count is one, so that is what this says.

Two honest caveats on that number. This sandbox has no GitHub API
credential -- `api.github.com` answers `401`, and the git remote is a
push proxy rather than an API -- so the count above is derived from the
cron expression and the merge time rather than read back from the runs
list. And `nightly.yml` also carries `workflow_dispatch`, so somebody may
have dispatched it by hand in the meantime. Neither caveat changes the
decision: a manual dispatch is not a night, and no arithmetic gets a
daily cron to five firings in the day and a half since the last reading.

## Why it was dispatched early, twice

Task 267 was approved a week early and the re-filing was dispatched six
days early. That looks like two human slips, but it is one missing
mechanism: nothing in grain can express the constraint, so there was
nothing for a human to set even had they wanted to. Three things that
look like they might, and do not:

* **`Defer`** (`pkg/ui/client.go:2360`) does put a task aside, and a
  deferred task drops out of `task_ready` -- but `Task.DeferredAt` is
  "when somebody put this task aside" (`pkg/model/task.go:772`), not a
  wake time, and nothing undeferrs on a date. `Undefer` is a button
  somebody presses.
* **`DependsOn`** links a task to other tasks. There is no task whose
  completion means "2026-09-14 has arrived".
* **Schedules** (`docs/schedules.md`) fire on a cadence --
  every-N-hours, daily, weekly, monthly -- and always refire. There is
  no one-shot and no start date; `CreateScheduleRequest` deliberately
  has no `DependsOn` for that reason.

So the two ways to actually make the next attempt land on material,
neither of which is a task body:

1. **Defer the re-filed task** and undefer it on or after 2026-09-14.
   This works today and needs nobody to build anything. It costs one
   human remembering.
2. **Create the daily review schedule** `docs/nightly-test-review.md`
   already asks for, in this deployment's own state
   (`bwsalmon/grain-home`, not this repository). If the nightly is read
   every morning, "the first week" stops being a task anybody has to
   file: it is just the reading that happens on the first morning a week
   exists, and each morning's reading has the previous ones to compare
   against -- which is the series the whole exercise has been waiting
   for. This is the better answer, and the first reading proposed
   confirming that schedule exists; from here there is no way to see
   whether that happened.

## What is still open from the first reading

* **The `govulncheck` bump has not landed.** `go.mod` on `main` still
  says `go 1.26.2`, `golang.org/x/text v0.38.0` and
  `google.golang.org/grpc v1.79.3` -- the three lines the proposed task
  would change. The 16 called advisories are therefore still there, and
  `govulncheck` should still be red every night. Not re-filed here: the
  task exists, and filing it twice is worse than leaving it.
* **`ui e2e, repeated`** should be green from run 2 onwards if
  `1dc74067` did what it says. Still unconfirmed, because there is still
  no run 2.
* **Whether `govulncheck` is a check or noise** remains a question about
  a rate with a denominator of one.

## One thing the next reading gains from the delay

`nightly.yml` has been edited three times since run 1 -- `1dc74067`
(rewrote what `test-e2e-long` runs), `4d376605` (added the sixth job),
`994869a5` (prose). The first write-up's baseline table is already not
comparable job-for-job against anything that runs from here on, which is
exactly what the amendment about checking for workflow edits between
compared nights was added for. The next reading should treat run 1 as
history rather than as a baseline, and take its baseline from the first
run at the current workflow instead.

No amendment to `docs/nightly-test-review.md` in this change: nothing
here is a fact about reading a nightly, only about when this particular
task was allowed to start. The prompt is fine as it stands.

## Reproducing

    # the runs that exist (needs an API credential this sandbox lacks)
    curl -s 'https://api.github.com/repos/bwsalmon/grain/actions/workflows/nightly.yml/runs'
    # the cron, and every edit to it
    awk '/^on:/,/^jobs:/' .github/workflows/nightly.yml
    git log --format='%h %ad %s' --date=iso-strict -- .github/workflows/nightly.yml
    # the advisories the bump would clear, still unbumped
    grep -E '^go |golang.org/x/text|google.golang.org/grpc' go.mod
