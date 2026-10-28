# Reading the nightly suite

`.grain/workflows/nightly.yml` is the long suite: everything worth
running against this repository that is too slow, too repetitive or too
noisy to put in front of a pull request. **That file is the list of what
it runs** -- this document deliberately does not repeat it, because a
job list written down twice is a job list that is wrong by the second
month, and this document is where it was wrong before. None of it blocks
a merge, and all of it is expected to be red sometimes for reasons that
are not "last night's commits broke something".

That is the problem this document exists for. A gate is read by whoever
it blocked; a nightly is read by nobody unless somebody is asked. So the
run is given to an agent once it has finished, and the agent's job is the
judgement a green/red tick cannot carry: which of these failures is a
bug, which is the suite finding its own flake, and which is somebody
else's advisory landing on our tree.

`.github/workflows/nightly.yml` is a different file with a sixth job
(`guest-cache-drift`) and is not what any of this is about. It cannot
run on this deployment at all -- GitHub Actions are blocked at the
account level here, and grain does not read GitHub's checks for this
repository -- so where the two are told apart below, the `.grain` one is
the one that runs.

## Scheduling it

**Grain does this for itself (grain/task-344), and the instructions this
section used to carry have been deleted rather than left standing.**
`.grain/workflows/nightly.yml` declares `cron: "11 2 * * *"`, and a
workflow cron in grain's own CI directory is now read as a task schedule
of this deployment's (`docs/declarations.md`): it starts that workflow's
run at 02:11 and files a task to read it, holding `grain-ci`, with the
prompt shipped in grain's code (`model.workflowReviewTemplate`). Which
means the two failure modes
this section was built to work around are gone rather than worked around
-- the review cannot be pointed at the wrong workflow, because it is the
workflow file's own cadence that files it, and it cannot be sent to read a
run that has not happened, because the run is started by the same firing.

**The review is filed when the run has finished, not when it starts**
(grain/task-351; `docs/schedules.md` has the mechanism). That matters more
to this suite than to anything else grain schedules: its jobs ask for four
CPUs and sixteen gigabytes each and queue behind one another for the few
sandbox lanes CI admission allows, the longest are bounded at an hour or
more and the lot may total twelve hours, and an agent run is two hours
plus at most 45 minutes of wait credit. A reviewer dispatched at 02:11 therefore reported
on part of the night and said the rest was unread as its *ordinary*
outcome. So the firing now starts the run and nothing else, and the review
is filed once that run ends, with the conclusion and the failing jobs
already in the task's first comment. Two things follow for whoever is
reading a report rather than writing one:

- **A review that arrives at five in the morning is the feature working.**
  The task's own filing time is the run's end, not the cron; the cron is
  still 02:11, and `list_ci_runs` on the run named in the comment is what
  says when the suite actually ran.
- **"The nightly did not complete" is still a report.** A run that could
  not be started, one that was cancelled or lost with the daemon, and one
  still going twelve hours after the cron came due are each concluded as
  errored and reviewed anyway, saying so. That is the one case where the
  report is partial by construction, and the prompt still asks for the
  partial report rather than a verdict nobody saw.
- **A run grain stopped waiting for is not the same report as a night
  with no run** (grain/task-386). The first two of those three are a
  sentence and nothing else: there is nothing to read. The third is the
  opposite and is read hardest of all -- the run exists, most of its jobs
  have ended and have logs, and the one that hung is still writing one,
  which `get_ci_log` will follow while it is being written. Which job
  never ended and what it last printed is the whole report, because a
  suite that hangs is invisible from everywhere else. The firing's row
  says which of the two it is (`model.ScheduledWorkflowRun.Unfinished`)
  and the task's first comment says so in as many words; `errored` on
  such a run means grain never got a verdict out of it, not that the run
  reached one about the tree.

So `.grain/templates/nightly.yml` and `.grain/schedules/nightly.yml` have
been deleted: they were a second copy of this job, firing at noon at a
`.github/workflows/nightly.yml` that cannot run on this account at all,
and expecting six jobs where the workflow has five. Whatever still fires
from this deployment's own settings (`sched-6` in `bwsalmon/grain-home`,
below) is a third copy, and is an operator's to retire -- it is a store
row, so it cannot be removed from this repository.

A firing suppresses its own next one while the previous is unfinished
(`overlap: skip`), which for this kind of schedule means until the run has
ended *and* its review has been read -- the behaviour you want twice over:
a night must not start while last night's is still running, and a morning
where the agent is still working through last night's failures should not
have a second copy stacked on it. And because the run is started by the
firing rather than found by the agent, the schedule cannot be too early --
the hour the suite runs is the hour its cron says, and there is no margin
to get wrong.

`sched-6` in `bwsalmon/grain-home` is the last copy left: a store row
firing at noon with the old prompt in its body, which nothing in this
repository can delete. Retiring it is an operator's job, and until it is
done this repository's nightly is read twice a day by two prompts that
disagree.

## Jobs that used to gate a pull request

Some of this suite was per-PR once and was moved here to make the gate
cheaper (grain/task-980): the WebKit Playwright project, which
`ui-e2e-long` now runs and the gate does not, and tests/installer's
`trixie` and `hostmode` packages, which are `installer-variants`. Each
job's own comment in `nightly.yml` names what it replaced. Read a
failure there differently from one in the rest of the suite:

- **Lean towards "regression".** Nothing caught these on the branch, so
  a failure that points at a commit merged since the last green night is
  most likely exactly that -- a pull request that would have gone red
  per-PR and went in instead. Find the commit and propose the fix.
- **The installer variants have a second witness.** release.yml's
  `images` job runs the same two packages against the built image on
  every merge into main. Red here and green there is this suite's
  assembled image (last published main's layers under this commit's
  binary) disagreeing with the built one; red in both is the tree.
- **If one of them keeps catching things, say so.** A moved job that
  finds a real regression more than once or twice a month is earning a
  place back in `.grain/workflows/tests.yml`, and that is a call for a
  human: put the count in the report rather than moving it back.

## The judgement

The task body is shipped in grain's code (`model.workflowReviewTemplate`)
and is deliberately short: read the workflow file, find the run grain
names, read the failing jobs, file what needs doing, and be honest about
which logs you did not read. It names no job and quotes none of what
follows.

This is the rest -- the part that is judgement rather than mechanics, and
the reason an agent reads a nightly instead of a tick doing it. It is
worth reading beside a red run, and it is guidance rather than a prompt:
nothing pastes it anywhere.

1. **Compare against the last several nights.** A job that has been red
   for a week is a different report from one that went red last night;
   a job whose duration has doubled is worth a sentence even when it is
   green. Say which it is for each failure.

   First check that you are comparing a check against itself.
   `git log` on `.grain/workflows/nightly.yml` and on the `Makefile`
   over the span you are comparing costs a moment and tells you whether
   the job was the same job on each of those nights: this repository
   merges fast enough that a target can be rewritten between two runs,
   and a duration or a conclusion compared across such an edit is two
   different checks put in one column. If something did change, say so
   and compare only across the nights that ran the same check -- a red
   run whose cause a commit has already fixed is a finding about the
   calendar, not about the tree.

2. **Classify every failure**, and say which class you put it in and
   why:

   - **A regression.** It passed on previous nights, it fails now, and
     the failure points at commits that landed since. Find the commit if
     you reasonably can -- the nightly runs against `main`, so `git log`
     between the last green run's SHA and this one's is a short list.
   - **A latent bug this suite was built to find.** `-shuffle=on` and
     `-count=2` are in the Go job precisely to surface order dependence
     and leaked state that the per-commit gate cannot see, so a failure
     of this kind is usually old and is a real finding, not noise. The
     seed is in the log; quote it, because it is what makes the failure
     reproducible.
   - **A flake.** Genuinely non-deterministic, no ordering or state
     explanation. Re-running the one job is how you check, and on the
     same commit it will not tell you anything: grain CI keys a verdict
     on its inputs, so a second ask replays the first answer in seconds.
     An independent sample needs the commit to have moved -- which, for
     a nightly on `main`, usually means tomorrow's.
   - **Environment or harness.** The sustained-load job is not sized for
     a throughput number and its own doc comment says so: read a red run
     there for deadlock, leaked slots or lost tasks rather than for an
     RPS it missed, and remember that every job in this suite competes
     for the same few sandbox lanes. A sandbox that ran out of disk, a
     network fetch that failed, a job that failed with no failing step
     under it at all -- that last one is a machine that never came up --
     all belong here too.
   - **An advisory.** `govulncheck` reports on today's vulnerability
     database against a tree that may not have changed. Say which
     dependency, whether the call path is one grain actually reaches,
     and whether the fix is a version bump we can take.
   - **A stale sandbox guest.** This one belongs to
     `.github/workflows/nightly.yml` alone: `guest-cache-drift` is the
     sixth job the Actions suite has and this one does not, so it will
     not be in the run you are reading. It is kept here because it is
     the same tree it describes. The guest job fails only when the guest
     `build-artifacts.yml` serves from its cache differs from a build
     from scratch on something *this repository pins* -- a toolchain
     version, a package that is installed or not, a module or npm package
     the caches hold or do not. Every one of those is decided by a path
     `scripts/granule/guest-inputs.sh` names, so a red run here means an
     input escaped the cache key, and the fix is to name it in that list
     (which invalidates the key and rebuilds every guest after it), not
     to widen the comparison. The job's own error annotation names the
     first few lines that differ, which is enough to say what escaped;
     the whole diff is in the run's summary and in the
     `guest-fingerprints` artifact beside it, and both need more than a
     repository read (the artifact a token with `actions:read`, the
     summary a browser on the run page). `-` is the cached guest and
     `+` is the fresh one. Treat this as a regression even when nothing
     in last night's commits caused it: a guest that is wrong stays wrong
     until an input changes, so it silently affects every task and every
     `.grain/workflows/tests.yml` job dispatched into a sandbox.

     A **green** run of that job can still carry a drift warning, and
     that is not a failure: the values the cache key freezes on purpose
     have moved -- Debian package versions, the guest kernel, the base
     image's own userland. The warning annotation counts them and names
     the first eight (`-` cached, `+` fresh), which is the part you can
     read with nothing but access to the repository; the second summary
     section and the artifact hold the rest. Report the number *and what
     moved*, and say whether it is worth forcing a rebuild. It is
     worth it when a security update the guest should carry is in the
     list, when the guest is weeks behind because nothing has touched an
     input in that long, or when the drift is large enough that a
     dispatched task's environment no longer resembles CI's. Forcing one
     is a commit that changes an input, or deleting the `inputs-<hash>`
     tag from the `guest` package and re-running `build-artifacts` --
     never a push from the nightly, which holds no credential that could.

3. **Then act, in this order.**

   - If a failure is a regression with an obvious, small, well-understood
     fix, propose a task for it (or fix it yourself if you were given
     that latitude), with the reproduction command in the body.
   - If it is a latent bug or an advisory, propose a task describing it,
     including the reproducing seed or the affected dependency and
     version. Do not fix it in the same breath as reporting the night.
   - If it is a flake, propose a task to make that test deterministic.
     Name the test.
   - If it is environment or harness, propose a task to fix the harness.

   Never make the suite less rigorous to make it green. Dropping
   `-count=2`, lowering `GRAIN_STATEREPO_SOAK_ROUNDS`, cutting
   `test-e2e-long` from three passes to one, adding a `t.Skip`, or
   marking a job `continue-on-error` are all ways of deleting the finding
   rather than the fault, and this whole workflow exists because that is
   what happens to checks nobody is asked to read. If a check is
   genuinely not worth its runtime, say so in your report and let a human
   decide.

4. **Report only what the user needs to address.**

   Nightly testing workflows are most likely to have one of two cases:
   1) Everything is fine, nothing needs to be fixed: file nothing and do not report to the user.
   2) There are clear bugs: file new tasks (`propose_task`) to fix them, and do not report to the user.

   The task should only report to the user if neither of these is true: there is some bug or issue that is not easily fixable with a new task because either it is an architectural change or needs user intervention for another reason. In that case, leave a comment with:
   - a clear description of the issue and why user intervention or an architectural decision is required;
   - minimal reproduction steps, failing output, and relevant context;
   - the specific choices or design questions the user needs to address.
