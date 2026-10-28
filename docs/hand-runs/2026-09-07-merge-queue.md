# Hand run: the merge queue's three verdicts

`docs/test-plans/2026-09-06.md` section 8. What the plan asked for: on
`bwsalmon/grain` itself, stage two pull requests where the second
conflicts with the first and watch what the second is told (`#876`); push
a branch whose own build is broken and confirm it is repaired once and
then escalated rather than repaired round and round (`#898`, `#909`);
cancel a check run by hand mid-queue and confirm the "never started"
branch fires (`#886`, `#858`, `#920`); and confirm
`docs/merge-queue-staleness.md` still describes what all that does.

The plan's own "not proved" is the frame for this: every one of those
decisions is read off CI's own reporting, and in a test CI's own
reporting is `githubsim`'s. "A real GitHub check run that is queued, or
cancelled, or reported by an App that stopped answering, is a shape no
test has produced."

## What was run, and what was not

**Not run against the live queue.** The sandbox this was done in carries
no GitHub credential -- its only git credential is for the local mirror
this repository is cloned from -- so no pull request was opened on
`bwsalmon/grain`, no check run was cancelled by hand, and no cycle of the
real deployment's merge queue was observed. The plan's staging
instructions were not followed, and could not be.

**What was run instead**, in two halves, because the sandbox turned out
to have one thing the release-trains hand run did not:

1. Each of the three scenarios driven through the real
   `orchestrator.SyncPullRequests` against `githubsim`, from a scratch
   driver in `pkg/orchestrator`, with every comment printed verbatim
   rather than asserted on. That is grain's whole decision, on a real
   bare git repository -- the sim answers `POST /merges` by shelling out
   to real `git`, so the 201 and the 409 below are git's verdicts and not
   a fixture's -- with only the HTTP framing simulated.
2. **The check-run shapes read off real GitHub.** `api.github.com`
   answers this sandbox unauthenticated, and `bwsalmon/grain` is public,
   so the Actions and Checks endpoints grain reads are readable from
   here for real. That is what closes part of the plan's gap, and it is
   what turned the third scenario's verdict around.

Every finding below says which of the two it came from. One divergence
was found, it is in the scenario the plan was most interested in, and it
is now pinned by a test rather than only written down here.

## 1. Two pull requests, the second conflicting with the first

**As the plan expects, on both halves of it.** Nothing filed.

*Established against `githubsim`, with a real conflict.* Two auto-merge
tasks, both green, both branched off `main`, each writing a different
line into the same file so a real `git merge` between them really fails.
The queue merged the first, and on the next cycle -- once GitHub had
recomputed the second's mergeability against the `main` the first had
landed on -- the second was sent back to its own branch for repair with
this, which is the whole of what the dispatched run is told:

> acme/widgets#9001 the merge queue tried to merge `main` into
> `grain/task-t2` and it conflicted, so this needs a real resolution
> rather than a merge, so the merge queue has sent this task back to an
> agent to repair it. No approval needed, and no separate task: the work
> happens on `grain/task-t2` -- the branch this task's pull request is
> already open from -- so the resolution and the change share one pull
> request and one round of checks. [...]

The clause worth reading is the first one. It is `repairReason`'s
`refreshConflicted` arm, and it is a report of something the queue
watched happen -- `POST /merges` answered 409 by a real `git merge` --
rather than `healthReason`'s inference from `detail.Mergeable`. The
observation carries both `MergeQueueRefreshedAt` and `MergeQueueRepairAt`
stamped at that cycle, so the refresh is spent and the repair is the one
this pull request gets.

*The `#876` half, staged separately*, because the sequence above never
produces it: hold the head conflicted while a green pull request sits
behind it in the same repo. The head got its repair on that same cycle
and kept its place; the green one behind it **merged**, on the same
cycle, and its task closed. That is the whole of what `#876` claims, and
it holds.

The conflicted head's own comment in that arrangement is
`healthReason`'s, not the merge's -- "it has conflicts with `main`" --
because the head was conflicted from the start and its own refresh
attempt was against a `main` that had not moved. Both wordings are
correct for what produced them; a reader comparing two escalations should
not read the difference as inconsistency.

## 2. A branch whose own build is broken

**As the plan expects.** Nothing filed. The escalation's claim is inside
what it checked, which is what the plan asked to be checked.

*Established against `githubsim`.* A head with a red `go` check and a
real Go test failure in the job's log. Three cycles:

**Cycle 1 -- repaired, once.** `refreshStaleHead` merged first and got
GitHub's 204: nothing was behind, so the failure is genuine, and the
queue went straight to the repair rather than spending a cycle on a
merge that would change nothing. The comment names the failing check and
carries the job's own log under a "What CI printed" heading, with the
job's URL:

> acme/widgets#9000 its checks are failing (`go`), so the merge queue has
> sent this task back to an agent to repair it. [...] Everything failing
> on it is in scope, including breakage this branch's own commits did not
> cause: a check that was already red on the base still stops this pull
> request merging, so fix it here rather than reporting it as somebody
> else's.

That last sentence is `#909`'s, and reading it beside cycle 1's 204 is
the point: the queue only says "including breakage this branch's own
commits did not cause" after it has established, with a write it made
itself, that the branch is not merely behind.

**Cycle 2 -- nothing.** The repair ran to completion and the branch is
still red, and the queue did not act on the cycle the repair finished on.
That is `advanceMergeQueueHead`'s one-cycle wait, and it is load-bearing:
dispatch runs before sync inside a tick, so this cycle's `detail`
describes the branch as it was before the repair's own push.

**Cycle 3 -- escalated, not repaired again.** One more refresh (204
again), then:

> The automatic repair of acme/widgets#9000 didn't take -- checks are
> still failing -- so this needs a person. Push a fix by hand (or resolve
> it directly on GitHub) and acme/widgets#9000 will merge as soon as it
> reads clean. The merge queue has moved on to the next task in
> acme/widgets. **The queue tried merging the base branch in first: it
> was already up to date, so this is not a branch that has simply fallen
> behind.**

**On "check the escalation comment claims only what it actually
checked".** It does, and the mechanism is worth naming because it is
easy to lose. The bolded sentence is added by a `switch` in
`escalateToUser` with arms for exactly two outcomes -- `refreshUpToDate`
and `refreshConflicted`, the two where the queue made the call and got an
answer from it. `refreshAlreadyTried`, `refreshUnreachable` (a fork's
head branch, a 404) and `refreshUpdated` add nothing: on those the
comment stops at "didn't take", which is all that was established. The
same discipline holds in `escalateStartupFailure`, where the sentence
ruling out a workflow edit is gated on `workflowsRead` and reads "the
merge queue could not read which files this pull request touches, so it
did not rule that out" when the read failed. Neither escalation says
anything grain did not do.

No further cycle said anything a third time, and no separate fix task was
ever filed anywhere in this scenario.

## 3. Cancelling a check run

**Not as the plan expects, and this is the finding.** The plan's
expectation -- "the 'never started' branch fires and the pull request is
escalated rather than repaired" -- is wrong twice over, and what actually
happens is that the pull request **merges**.

> **Since decided, and the merge is gone.** Everything below is what the
> code did on 2026-09-07 and is left standing as the reading that
> produced the question. The question was taken up on 2026-09-08 and
> answered with the second of the two narrower options at the foot of
> this section: a cancelled check now reads `PrPending` rather than
> `PrClean`, so the head holds its place and, if the verdict is still
> missing at `defaultCheckStallDeadline`, is handed to a person instead
> of merged. It is still not read as a failure and still files nothing,
> so the "cancelled, will retry" and "waiting on a human's approval"
> objections that the exclusion existed for are untouched -- and one
> thing the option as filed did not say: a genuinely failing check now
> beats a cancelled one, so a fail-fast matrix (one job red, its
> siblings cancelled with it) still gets its repair on the cycle it is
> read rather than sitting out the deadline. The whole argument, and
> what it costs, is in `healthFrom`'s own doc comment;
> `TestSyncPullRequestsHoldsAHeadWhoseCheckWasCancelled` is the test
> below under its new name and its new verdict.

*Established against real GitHub, then driven through `githubsim`.*

`healthFrom` reads a check's conclusion through `failingChecks`, which
counts `failure`, `timed_out` and `startup_failure` and nothing else.
`cancelled` is deliberately excluded, along with `neutral`, `skipped`,
`action_required` and `stale` -- `healthFrom`'s own doc comment argues
for it and `TestHealthFromTheNonFailureConclusionsStayClean` pins it, on
the grounds that a merge queue's own "cancelled, will retry" check must
not block a pull request and a workflow waiting on a human's approval
must not be handed to an agent. So a head whose only non-success checks
are cancelled is not `PrFailing`; every check is `completed`, none is
failing, `Mergeable` is true, and the answer is `PrClean`.

Driven through `SyncPullRequests` with exactly that check list: the pull
request merged, the task closed, no comment was written, no repair was
asked for and `MergeQueueBlockedAt` stayed nil. The "never started"
branch was never within reach of it -- `advanceMergeQueueHead` is only
called for a head that reads `PrConflicted` or `PrFailing`, and this one
reads clean.

**The shape is real, and it is this repository's.** `bwsalmon/grain` has
3,928 workflow runs; the 100 most recent, read on 2026-09-07, contain six
cancelled ones -- five `tests` and one `build-artifacts`. The check runs
on one of those head commits, `63e716f3`, are:

```
publish grain image   completed  success
workflows             completed  success
shell scripts         completed  success
terraform             completed  success
ui e2e                completed  success
granule on a real VM  completed  success
frontend              completed  success
real KVM sandboxes    completed  cancelled
go                    completed  cancelled
grain image           completed  success
sandbox guest image   completed  success
```

`go` is the whole Go suite. That list is `PrClean`. The JSON it was read
from carries exactly the four keys `RESTClient.ListCheckRuns` decodes --
`name`, `status`, `conclusion`, `html_url`, under `check_runs` -- so the
sim's shape and GitHub's are the same shape here, and the test named
below seeds four of those eleven rather than an invented list.

**What has kept it from firing, and it is not grain.** Cross-referencing
those six cancelled runs against the 100 most recently updated closed
pull requests finds no overlap at all: not one of them was cancelled
against the head commit its pull request finally carried. (Two windows of
a hundred, so this is "none seen", not "none ever".) The reason is
`.github/workflows/tests.yml`'s concurrency group: `cancel-in-progress`
is on for `pull_request` events, so a push cancels the run belonging to
the *superseded* commit. The queue reads checks by head sha
(`checkRunsFor`, deliberately, so that a push landing mid-cycle cannot
have its checks read for the commit before it), so a cancellation
belonging to a commit that has been pushed past is never read at all. The
protection is real but it is incidental -- it comes from which commit the
cancellation lands on, not from anything the queue decides. A
cancellation on the head sha itself, which is precisely what the plan's
step 3 asks somebody to stage by hand, is read, and merges.

**And it is not a "never started" run either**, which is the second half
of why the plan's expectation does not hold. `JobLog.NoJobStarted` keys
on a run GitHub concluded with no job of it running a step -- either
`startup_failure`, or a plain `failure` whose jobs came back with an
empty `steps` array. A real cancelled job is neither. Run
`34145813634`'s cancelled `go` job reports 13 steps, all 13 carrying a
conclusion: it ran, and was interrupted. The two conditions are
orthogonal, and no cancellation reaches the startup-failure path however
it is read.

Whether `PrClean` is the right answer for a cancelled required check is a
judgement, not a bug I should have decided here: the exclusion is
deliberate, it is argued for in a doc comment, and reversing it would
have a cancelled "will retry" check block a queue head, which is the
failure the exclusion exists to avoid. What was missing is that nothing
said out loud what the exclusion *costs*, which is a merge on a suite
whose verdict was never delivered.
`TestSyncPullRequestsMergesAHeadWhoseCheckWasCancelled` now says it, on
the check list above. Filed as a question for a person, with the two
narrower options in it.

> **Answered on 2026-09-08 (grain/task-330).** Option 2, the `PrPending`
> one. Neither of the answers this section was arguing between turned
> out to be the right one: "clean" merges on a suite that never
> reported, "failing" dispatches an agent at a job nobody thinks is
> broken, and PENDING is what the rest of `healthFrom` already means by
> "no verdict yet". The third option -- telling a cancellation on the
> head sha apart from a superseded one -- stayed unbuilt for the reason
> given when it was filed: the queue only ever reads the head sha, so
> every cancellation it can see is already on it.
>
> What it costs, stated so a later reader can weigh it: a repository
> whose CI legitimately ends a check `cancelled` and leaves it that way
> now waits `defaultCheckStallDeadline` -- two hours -- per head commit
> before a person is told, and, since only a queue head is acted on,
> anything queued behind that head in the same repository waits with it.
> That is the same wait a check stuck in `queued` already earns, and
> `SetCheckStallDeadline` is the same escape hatch. On this repository
> it costs nothing at all, for the reason the paragraph above gives:
> `tests.yml` cancels the superseded commit's run, and the queue never
> reads those.
>
> `mergeable_state` was checked, as the question asked, and is context
> rather than a signal: a cancelled check leaves it `unstable` where the
> check is not required and `blocked` where it is, and `blocked` also
> covers a pull request waiting on a review, so it cannot separate this
> from a block that has nothing to do with CI. It stays out of the merge
> gate for the reasons in `github.PullRequestDetail.MergeableState`'s
> own doc comment, which were measured on live pull requests; no live
> pull request carrying a cancelled check on its head commit was
> available to read one off (the open pull requests on this repository
> were scanned on 2026-09-08 and none had one), so that pair of readings
> is derived from GitHub's own definitions rather than observed.
>
> Still owed, item 3 below is unchanged in kind but not in expectation:
> the cancellation staged by hand on a head commit should now be
> *watched holding*, and the pull request handed to a person two hours
> later, rather than merging within a cycle.

**The startup-failure path itself was driven separately, and is as the
plan expects.** A head whose runs GitHub concluded `startup_failure`,
with no workflow file touched by the branch, is escalated without ever
spending the repair -- `#886` -- and the comment tells the person the
four causes, names the one thing it ruled out and how, and links the run
page:

> [...] but GitHub concluded every one of those runs `startup_failure`:
> each of them failed before a single job of it ran a step, so the merge
> queue has gone straight to a person instead of spending this pull
> request's one automatic repair on it. [...] Only the first can be fixed
> from this branch, and it is ruled out already: this pull request
> touches no file under `.github/workflows/`, so nothing in this branch's
> tree is what failed and no commit pushed to `grain/task-t1` would clear
> it.

That is `#898`'s boundary in the right place. What could not be confirmed
is that a real one looks like this: GitHub's own run-status filter
reports **no** run of `bwsalmon/grain` has ever concluded
`startup_failure`, `timed_out`, `action_required`, `neutral` or `stale`.
The path has never had a real run of this repository to fire on, and
producing one needs either a credential (to break a workflow file on a
branch) or an operator (to turn Actions off). See "Still owed".

## 4. Does `docs/merge-queue-staleness.md` still describe this?

**Almost, and it was worth checking.** One claim had gone stale, and it
is corrected in this change.

Read against the code, the document holds up well: every function and
field it names exists, the `201`/`204`/`409` table is what
`refreshStaleHead` does, the header's note that `fileFixTask` is now
`requeueForRepair` covers the several places the old name still appears,
and decision 4 carries an explicit amendment block for `#917` describing
the sequence as "refresh, repair, refresh, a person".

But its last section, "What this deliberately does not do", still said:

> It does not refresh anything but the queue head, and not more than once
> per pull request.

That was true before `#917` and is not now -- it is once per *repair
round*, so twice in a pull request's life. The amendment forty lines
above says so; this line is in a later section, carries no warning, and
is the summary a reader is most likely to quote. Corrected, with the
loop-safety argument above it pointed at the amendment too.
`docs/data-model.md` and `Observation.MergeQueueRefreshedAt`'s own doc
comment were already right, so this was the one rotted claim.

The document's own outstanding item is unchanged and this run could not
close it: "the `204`/`409` split has been confirmed against `githubsim`
[...] not yet against a live repository." Below.

## Still owed

Each of these needs the credential this sandbox does not have, and each
is grain reading GitHub rather than grain deciding anything -- the
deciding is covered above.

**A second run took this list on, on 2026-09-08 (`grain/task-331`), and
what follows under each item is what it changed.** It had no GitHub
credential either, and that is worth saying plainly because it is not a
mistake in how it was dispatched: an agent sandbox is not given one by
design. `pkg/capability/githubtoken`'s own doc comment is the rule --
"the token never enters the sandbox -- `pkg/gitproxy`'s whole shape is
that a run's route to GitHub is grain's, never the agent's" -- so a
sandbox's git credential is the proxy that carries pushes to its own
branch, and there is no API credential behind it. `api.github.com` does
answer unauthenticated, and `bwsalmon/grain` is public, but from a
shared address: the hour's sixty requests were down to their last five
when that run started and were spent again within five calls of the
window resetting, by somebody else on the same address. So the
read-only half of this list is a few requests an hour, and the write
half is nothing at all.

What that run could do was re-confirm one fact, close item 4, and work
out for each of the other three what would actually close it -- which
for item 1 turns out to be something this deployment already knows how
to hand a sandbox, and for item 2 turns out to be two different asks
wearing one number. Nothing below is struck through on grain's say-so
alone.

1. **The `204`/`409` split against live GitHub.** `docs/merge-queue-
   staleness.md`'s own ask, still open. `githubsim` answers both by
   running real `git`, so what is unconfirmed is not the merge but the
   HTTP: that GitHub's already-up-to-date really is a `204` that
   `MergeBranch` returns as `MergeResult{Merged: false}` with a nil
   error, and that its conflict really is a `409` that
   `IsMergeConflict` recognises. A `204` misread as "merged" burns the
   refresh without refreshing; a `409` misread as an ordinary error
   turns a genuine conflict into a cycle that errors forever.

   *Still open, but it is smaller than it looks and nothing about it
   needs `bwsalmon/grain`.* `POST /repos/{owner}/{repo}/merges` wants
   `contents: write` and nothing else, and the question is about that
   endpoint rather than about this repository, so any repo a credential
   can write to answers it in three calls -- in `refreshStaleHead`'s own
   direction, which is base = the pull request's head branch and head =
   the base branch (`MergeBranch(owner, repo, detail.HeadRef,
   detail.BaseRef, ...)`, a merge of `main` *into* the branch): a branch
   strictly behind `main` for the `201`, the very same call again for
   the `204`, and a branch carrying a conflicting edit to a line `main`
   also moved for the `409`. Drive `github.RESTClient.MergeBranch`
   itself rather than `curl`, since what is unconfirmed is the mapping
   -- `204` to `MergeResult{}` with a nil error, `409` to an `*Error`
   `IsMergeConflict` says yes to -- and not the status codes on their
   own.

   The deployment already has the capability that supplies this:
   **`github-sandbox`** (`pkg/capability/githubsandbox`) mints a
   private single-use repo under the bot account and places a token
   scoped to just it at `/home/debian/.github-sandbox-token`, and
   `agentPermissions` carries `contents: write`. A task granted it can
   close this item end to end inside one run, without any access to
   `bwsalmon/grain` and with nothing to clean up afterwards -- the repo
   is reaped. The one-hour lease is not a constraint on three calls, and
   nothing else is missing at the sandbox end: `api.github.com` is
   reachable from one, Go builds there, and `github.NewClient` takes a
   transport and a `TokenSource` and nothing else, so the driver is a
   few lines around the real `MergeBranch`. So what this item is waiting
   on is a grant, which is set where grants are set and not in this
   repository, rather than any machinery that still has to be built.

   One thing to check before promising that, because this run could not:
   `github-sandbox` mints through an App, and its two secrets (an App id
   and a private key, from `grain controller bootstrap-github-app`) are
   an operator's setup that this deployment may never have done. If it
   has not, the ask is that setup rather than the grant -- and a
   fine-grained token with `contents: write` on any throwaway repo
   answers item 1 just as well, since the endpoint under test does not
   care whose repository it is.

   **A third run took this item on, on 2026-09-08 (`grain/task-392`),
   and was dispatched without the grant it says it needs, so the live
   calls were not made and nothing above is struck through.** What it
   left behind is the driver, which was the only part of this item that
   was ever work: `pkg/github/live_merges_test.go` seeds the three
   branches this item describes on whatever repository it is pointed at,
   makes the three `MergeBranch` calls in `refreshStaleHead`'s own
   direction, and asserts the mapping -- and, after each call, reads the
   branch back, so a `201` that did not move the branch and a `204` that
   did fail too. Under `github-sandbox` it reads the repo and the token
   out of that capability's own two placements and needs nothing named:

       GRAIN_LIVE_GITHUB_MERGES=1 go test ./pkg/github -run Live -v

   `GRAIN_LIVE_GITHUB_REPO` and `GRAIN_LIVE_GITHUB_TOKEN` point it at a
   throwaway repository and a fine-grained token instead, for the second
   of the two routes above. The identical assertions run against
   `githubsim` on every ordinary `go test`
   (`TestMergeBranchStatusMappingAgainstTheSim`), which is not more sim
   coverage -- `githubsim_test.go` already has that -- but insurance
   that the live harness still works on the day somebody has a
   credential for it.

   So what is left of this item is a dispatch and one command. The
   precondition named just above -- whether this deployment has done
   `grain controller bootstrap-github-app` -- is still unchecked: that
   run could not see it either, since it is this deployment's own
   configuration and lives in `bwsalmon/grain-home`.

2. **A real `startup_failure` end to end.** Push a branch with a
   deliberately unparseable `.github/workflows/*.yml` (which should be
   *repaired*, since the branch caused it) and one with Actions disabled
   for the repository (which should be *escalated*). This is the pair
   `#898` and `#886` split, and neither side has met a real run.

   *Still open, and the two halves want different things -- they should
   not be asked for as one item again.*

   The **repair** half (`#898`, the cause in the tree) is the harder of
   the two, and the obstacle is specific: the branch under test has to
   carry a file under `.github/workflows/`, and the `github-sandbox`
   token cannot push one. `agentPermissions` omits `workflows`
   deliberately -- its own comment cites `docs/design.md`'s "Scopes to
   withhold" -- and GitHub refuses a push touching that directory from
   an App installation token without it. So this half needs either that
   permission decided on (an account-owner's call, and the reason it is
   withheld is a good one) or a credential outside the capability model,
   *and* it needs grain's queue to be watching whichever repository it
   is staged in, because the decision under test is
   `advanceMergeQueueHead`'s choice between repairing and escalating and
   not GitHub's answer alone. Two unknowns are worth writing down before
   somebody spends the credential: whether GitHub concludes an
   unparseable workflow `startup_failure` or plain `failure`
   (`runConclusions` exists because both happen), and whether a push
   made with an App installation token starts a run at all in the repo
   it is scoped to.

   The **escalation** half (`#886`) is narrower than "has never met a
   real run", and the tree says so. `escalateStartupFailure`'s doc
   comment names `bwsalmon/grain-home#1` as the worked example, and
   `startupFailureReason`'s says grain "had it in hand for three
   escalations of `bwsalmon/grain-home#1` before it started passing it
   on" -- the sentence being GitHub's own "The job was not started
   because recent account payments have failed or your spending limit
   needs to be increased", read off a real check run of a real
   repository whose runs really do die before a job starts
   (`docs/state-repo-check-startup-failures.md`). So a real
   `RunFailureReason` has arrived from live GitHub; what has not been
   watched is one arriving *and being quoted* by the comment, since the
   code that quotes it landed after those escalations. That makes the
   cheapest confirmation a read rather than a staging: ask
   `bwsalmon/grain-home`'s recent runs and check runs, with the
   deployment's own credential, whether the conclusion and the sentence
   are still there and which word GitHub uses for them. It has to be
   that credential: `grain-home` is private, and a sandbox asking
   unauthenticated gets a 404 (confirmed, 2026-09-08).

   Re-confirmed the same day, since it is the fact that made this item
   necessary: GitHub's run-status filter still reports **zero** runs of
   `bwsalmon/grain` ever concluded `startup_failure`, `timed_out`,
   `action_required`, `neutral` or `stale`.

3. **A cancellation on a head commit, watched.** The staging the plan
   asked for, once somebody can open a pull request here: let the queue
   reach a pull request as head, cancel its `go` check from the Actions
   page, and watch what the queue does. Expected, since section 3's
   answer of 2026-09-08: it does *not* merge -- the head holds its
   place, nothing is filed, and two hours later the task carries a
   comment naming `go` as cancelled and saying a re-run is what answers
   it. Worth doing for the same reason as before, since what a test
   cannot show a person is how long two hours of a queue held on one
   pull request feels.

   *Still open, and it is the one item a scratch repository cannot
   stand in for*, because what is being watched is this deployment's own
   queue merging its own pull request: it needs a real task, on
   `bwsalmon/grain`, reaching the head of the queue, and a person on the
   Actions page (or a credential with `actions: write` for `POST
   /repos/{owner}/{repo}/actions/runs/{id}/cancel`) at the moment it
   gets there. Two things are worth settling first. The behaviour is
   already pinned by
   `TestSyncPullRequestsMergesAHeadWhoseCheckWasCancelled`, so this
   staging buys the demonstration and not the fact -- and the sibling
   decision task above may change what the right behaviour is, which
   would make a run staged now a record of the old one. And the staging
   itself is not free: it merges a real pull request on a suite whose
   verdict was never delivered, which is exactly the cost section 3 says
   nobody had written down.

4. ~~**A check reported by an App that stopped answering.** The plan
   names it and this run did not get near it. Every check run on this
   repository is `github-actions`'; a third-party App that posts
   `queued` and goes away is the case `defaultCheckStallDeadline`
   exists for, and the two-hour clock has never met a real one.~~
   **Closed unstaged by `grain/task-331`**, on that task's own
   instruction to say so rather than simulate it again.

   What a live version costs is the whole of the argument. It needs an
   App registered on the account, installed on `bwsalmon/grain` with
   `checks: write`, posting a `queued` check run against the live
   queue head's sha and then going silent for over two hours -- during
   which that head does not merge and the queue behind it waits, since
   a `queued` check is exactly what makes a head `PrPending`. Every
   step of that is the account owner's rather than an agent's, the
   two-hour wait is longer than a sandbox's whole run, and the cost
   lands on the real queue.

   Against that, grain's side is already covered: `pendingChecks` /
   `defaultCheckStallDeadline` do not care which App posted the check,
   only that a `queued` one has been seen against this head since a
   recorded time, and the tests hold it. What a real App would prove
   that the tests do not is one narrow thing, and it is GitHub's
   behaviour rather than grain's: that a check run left `queued` by an
   App that stops answering stays `queued` in the Checks API
   indefinitely, rather than being expired or swept by GitHub before
   the two hours are up. If that is ever worth knowing, the cheap
   version is to read it off a repository that already has a
   third-party App on it -- not to install one here.

## What is filed

| Task | Kind | Waits on |
| --- | --- | --- |
| Decide what a cancelled check on a queue head should mean (answered 2026-09-08: `PrPending`, see section 3) | decision | this run |
| The `204`/`409` split and a real startup failure, against live GitHub | hand run | this run |
| The `204`/`409` split, under a `github-sandbox` grant | hand run | `grain/task-331` |
