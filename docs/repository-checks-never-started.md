# Saying that a *watched* repository's checks have never started

**Status: decided and built** (grain/task-298). The decision is none of
the three options this task was filed with. All three were answers to
"where does grain keep the record", and the answer here is that there is
no record: the verdict is read from GitHub at the moment grain has a
reason to want it, said where the reader already is, and forgotten. What
follows is why, because the three rejected options are real designs and
the next person to propose one should start from why this one did not.

This is the sibling of
[state-repo-check-startup-failures.md](state-repo-check-startup-failures.md),
which decided the same question for the one repository grain owns the
workflow file of. Read that one first: everything here is about the
repositories grain merely watches.

## The condition, and what already covered half of it

GitHub concludes a workflow run before any job of it begins when the run
cannot start: `startup_failure` when it never created the jobs, and plain
`failure` when it created them and then refused to start them, which is
what an account out of Actions minutes produces
(`github.JobLog.NoJobStarted`). Every cause is a fact about the
repository rather than about the change -- Actions disabled or restricted
for it, no runner it may use, an account at its spending limit, or a
workflow file GitHub cannot parse at that commit.

grain already recognises the shape, per pull request.
`github.OnlyStartupFailures` classifies the read, and
`orchestrator.escalateStartupFailure` blocks that merge-queue entry and
hands it to a person -- correctly, since the pull request's one automatic
repair would otherwise be spent proving that no commit clears it. That
much is right and unchanged.

grain/task-228 then covered the **state repository**: `pkg/staterepo/
workflowhealth.go` asks GitHub hourly how the workflow file grain
installed there has been running, and reports the verdict on the State
pane, in `grain state status` and in the journal, with the three remedies
named. That works because it is grain's own repository, grain's own
workflow file, and grain's own settings pane.

Every **other** repository grain watches was left uncovered. An operator
whose target repository has Actions switched off gets one "this pull
request is failing" comment per task, and nowhere the sentence that would
tell them what to do.

## What was decided: ask, say it, forget it

`github.RESTClient.RecentRunStarts` reads what a repository as a whole has
been running lately -- one page of its recent Actions runs, plus a jobs
read for each run whose plain `failure` conclusion is ambiguous -- and
reports how many finished, how many are known to have got a job as far as
running a step, and how many are known not to have.
`RunStarts.ChecksNeverStarted` is the verdict: finished runs exist, at
least one demonstrably never started a job, and not one is known to have.

`escalateStartupFailure` asks for it at the one moment grain has a reason
to (`pkg/orchestrator/checkstartups.go`), and puts the answer in the
comment it was already writing. Both directions are worth saying, which
is what makes the read worth making:

- **Nothing here starts a job.** "Of the last 20 finished Actions runs in
  `owner/repo`, not one got a job as far as running a step. Every pull
  request open against this repository is failing this way and none of
  them can be repaired from a branch." The reader stops reading branches.
- **Other runs here do start jobs.** Then Actions is on, a runner exists
  and the account has minutes -- two of the four causes the comment has
  just listed are ruled out, and what is left is the workflow file on the
  base branch or a runner label.

The verdict also goes in the journal, once per escalation, because the
person who filed the task and the person running the deployment are not
the same person and do not read the same surface.

## Why nothing is remembered

The three options this task was filed with were a durable marker per
watched repository, an in-process map, and nothing. They differ only in
where a tally of grain's own escalations lives, and each inherits a
problem from the tally itself: a marker is grain keeping opinions about
repositories nobody told it it owns, with no pane and no settings file of
its own to point the reader at; a map is re-reported on every restart and
cannot be asked a question; nothing leaves the operator counting comments.

The premise is what was wrong. grain does not have to remember whether a
repository's checks start, because GitHub knows, the question costs one
API read, and the read happens on a path that by construction only runs
when a pull request has already failed this way. Asking has properties a
record cannot have:

- **It cannot go stale.** A record says what grain saw; the read says
  what is true now. Nobody has to dismiss anything, and the sentence
  stops being written the moment somebody turns Actions back on -- which
  is the same reason task-228's marker is a cache of GitHub's answer
  rather than a tally of grain's escalations.
- **It cannot be wrong about a repository grain has not watched for
  long.** The claim is GitHub's own history of the repository, not "every
  failure grain happens to have seen", so it does not get more confident
  the longer a deployment runs.
- **It says the true thing in both directions.** A tally of escalations
  can only ever accumulate; it has no way to tell an operator that their
  repository's Actions are fine and the problem is one workflow file.
- **It needs no home.** No marker beside the data directory, no table, no
  pane, no wiring from the orchestrator to a surface that knows nothing
  about merge queues -- which is what made the previous attempt at this
  feel like bookkeeping in place of an answer.

**The count of pull requests the condition has cost is not kept.** It is
not what a reader needs: the verdict is true of *every* pull request
against the repository, whether grain has one open on it or not, and the
comment says exactly that. Where somebody does want the number, the queue
already holds it -- those tasks are blocked, in one repository, each
carrying this comment -- and deriving it from the store beats maintaining
a second copy that can disagree with it.

**The state repository's own record does not grow one either.** The same
argument: `WorkflowStartupFailures` reports a condition, and a
pull-request count on it would be a number that has to be kept correct
across restarts to say something the State pane's own task list already
shows.

## What the conclusion now governs (grain/task-291)

The section above is still the answer to "should grain keep a record of
this", and the answer is still no. What it got wrong was narrower, and
cost a real afternoon: the conclusion was reached **per pull request**,
at the end of a diagnosis each entry had to make for itself, and thrown
away in between.

So on 2026-09-18 the `gke-labs/kontur` queue had four entries in it, all
failing the same eight checks, and the one at the front carried this
comment -- word for word the paragraph above, correct in every part,
including the sentence saying the finding covers every pull request in
the repository. The three behind it read "Needs repairing". An operator
asked the queue to try the front one again; it merged the base in, hit a
conflict, dispatched an agent, and two full CI suites later the same
eight checks were still red. That sequence was then queued up for the
other three, one at a time.

Two things changed, and neither is a record:

- **The read is shared for the length of one reconciler cycle**
  (`orchestrator.repoFinder`). Asked once per repository, read by every
  entry in it, gone by the next cycle, which asks again. That is enough
  for both things a record was wanted for -- no entry rediscovers it,
  and every entry's row can say it -- with none of what a persisted
  finding would owe. Everything the section above says about staleness,
  dismissal and homes still holds, because nothing outlives the cycle
  that read it.
- **The conclusion reaches the decision, not just the prose.**
  `forge.SplitReds` divides a head's failing checks into the red a commit
  on its branch could clear and the red nothing pushed there reaches. The
  queue spends its moves -- the base merge, the repair, the CI suite that
  answers it -- on the first half only. A head with nothing of its own to
  fix gets neither, and is handed to a person in the seconds it takes to
  read the checks.

The second half of that sentence is the one to keep hold of. A head that
does have red of its own still earns exactly the repair it always did:
`grain/task-219` was seven checks an Actions budget had stopped and one
`grain CI` failure its own base merge had caused, and the `grain CI`
failure is work an agent should do. What changed for it is that the
repair comment now names which half is the run's to fix, and that no base
merge is written first to reach a green that pull request cannot have
until somebody pays a bill.

## The judgement calls

**The escalation comment changed; the escalation did not.** The comment
is where the reader is, so it is where the repository-wide sentence goes.
Nothing about the *decision* moved: the head is given up on either way,
the repair is still not spent, and a read that fails (a credential
without Actions scope, a repository whose CI is not Actions, a GitHub
that is briefly unreachable) leaves the comment exactly as it was before
this existed. That is the invariant worth protecting -- a merge queue
that stopped advancing, or a person who never got woken, because one
extra API read did not answer would be a worse deployment than one that
says less.

**One read at escalation time, not a poller.** grain could ask this of
every target repository on a timer and put a badge on the repo pane. It
would mean polling repositories that are almost all fine, for a reader
who is not looking, to pre-compute an answer that is only ever needed at
the moment of an escalation -- and the escalation is exactly when grain
finds out that something might be wrong. If somebody does want the
repositories pane to carry it, that is a poller and a surface, and it can
be built on `RecentRunStarts` without changing anything decided here.

**Positive evidence in both directions.** `ChecksNeverStarted` needs a
finished run, a demonstrated never-started run, and no started run at
all. A repository whose recent runs were all cancelled, or whose Actions
this credential may not read, produces the zero value and no sentence:
grain says nothing rather than sending somebody to change a setting on
the strength of an absence.

**The jobs read earns its place.** A run concluded `startup_failure` is
unambiguous and costs nothing extra. A run concluded plain `failure` is
the case this deployment actually hit -- GitHub created the jobs and
refused to start them, because the account's payments had failed -- and
nothing but the jobs' empty `steps` array separates it from an ordinary
red build. Without that second read the verdict would miss the one cause
that costs money to fix. It is bounded (`maxRunStartsJobReads`), because
a repository with twenty ordinary red builds must not become twenty-one
requests per escalation.

**A branch's own workflow edit is still not this.**
`advanceMergeQueueHead` separates the one startup failure whose cause is
in the tree -- a workflow file this pull request made unparseable -- and
repairs it rather than escalating. The repository read only happens on
the other path, so a change's own mistake never gets reported as the
repository's.

## What was deliberately not built

- **A pane, a badge or a banner.** See "one read at escalation time"
  above. The repositories pane exists (`pkg/ui/repos.go`,
  `RepoPage.jsx`), so this is a real option rather than a missing
  surface; what it needs is a poller with a reader, and neither exists
  yet.
- **Throttling the escalations.** A deployment in this state still blocks
  each pull request individually and comments on each one. Each of those
  pull requests genuinely is not mergeable, and the cost this line of
  work set out to remove -- an agent run per escalation -- was already
  removed by the escalation path itself.
- **Any action on the repository.** grain does not disable its own gates,
  does not stop opening pull requests against a repository in this state,
  and does not touch anybody's workflow files. Every remedy belongs to
  whoever owns the GitHub account, which is the same call
  `installWorkflow` makes for the state repository and for the same
  reason.
