# Bringing a stale queue head up to date before filing a fix for it

> **Narrowed since, and worth reading with its companion.** The queue no
> longer brings a branch up to date merely because it is behind, and it
> no longer offers the refresh below to a head that *grain's own* CI
> failed -- that verdict is about the branch's exact commit, so no merge
> of the base into it can change what it says. Everything here still
> holds where GitHub's own `on: pull_request` checks are the gate, which
> is the case it was written for. See `merge-queue-behind.md`
> (grain/task-198) for what changed and what now guards the base branch
> instead.

**Status: implemented, as described.** This document was written as a
proposal, before the code, because the decisions below are the awkward
part and were worth settling on paper first; it is kept as the reasoning
behind what shipped rather than rewritten into a description of it. Every
function and field it names now exists -- `github.MergeBranch`,
`github.IsMergeConflict`, `Observation.MergeQueueRefreshedAt`,
`orchestrator.refreshStaleHead` and its outcome type -- and the tests it
asks for at the bottom are written.

One name in it has since moved: what this document calls `fileFixTask`
-- the step after a refresh that did not help -- no longer files a
separate stacked task. The merge queue sends the task itself back to an
agent on the pull request's own branch instead (`requeueForRepair`,
`Observation.MergeQueueRepairAt`, grain/task-271), so that a resolution
and the change it repairs share one pull request and one round of CI.
Everything below about *when* the queue acts, and about reading `201`,
`204` and `409` off its own merge attempt, is unchanged; only what it
does afterwards is spelled differently.

The one thing it asks for that code cannot supply is still outstanding:
**the `204`/`409` split has been confirmed against `githubsim` (which
answers both by running real `git` against a real bare repository) and
against GitHub's documentation, not yet against a live repository.** See
decision 2 for why that check is worth making anyway. The check itself
is now written and waiting on a credential rather than on anybody's
time: `pkg/github/live_merges_test.go` makes the three calls against
whatever repository it is given, and under the `github-sandbox`
capability it needs nothing filled in --
`GRAIN_LIVE_GITHUB_MERGES=1 go test ./pkg/github -run Live`.

## The pattern this is about

Nearly every automatic fix this deployment has filed so far has been
resolved by the same commit: a plain `git merge origin/main` into the
task's own branch, with nothing to arbitrate. On `main`, all merged:
90fc0ff8 (task-11), 25ab1bab (task-14), cc1bf1f9 (task-23), 9d2602ea
(task-30), feccf565 (task-29), 74226a13 (task-26), 9486744b (task-27,
for bwsalmon/grain#549).

The shape is always the same, and it is not the shape the merge queue
was built for. A branch's checks last ran against a `main` that has
since moved -- for #549, `refs/pull/549/merge` was still a merge into
81cdf680 while `main` had moved seven first-parent merges past it -- and
one of two things happens:

- GitHub cannot compute a fresh merge ref, so every `on: pull_request`
  job dies at checkout. The queue reads that as *checks failing*, not as
  a conflict: the jobs exist, they completed, they concluded `failure`,
  which is exactly what `healthFrom` calls `PrFailing`. (74226a13's own
  commit message reports this for #554.)
- Or the run that did report is simply about a tree nobody would ever
  merge -- a green or red verdict on the head branch merged into a base
  seven merges stale.

Either way `advanceMergeQueueHead` reads the stale answer, `fileFixTask`
files a task, `dispatch.Cycle` dispatches an agent, an agent boots a
sandbox, clones, and contributes one merge commit. That is a full agent
run spent on a write the queue could have made itself, and -- worse than
the cost -- the fix task's body names failures (`healthReason`, from
`failingChecks`) that are not what is actually wrong with the branch, so
the agent starts from a false description of its own job.

## The proposal in one paragraph

Before `fileFixTask` gives up on a head, have the queue try merging the
head's base branch into the head branch itself, server-side, as one
GitHub write -- the same class of write it already makes when
`syncEntry` merges a fix's pull request back into the branch it repairs.
If GitHub says the head branch already contains the base, nothing was
stale and the fix task is filed exactly as it is today. If the merge
lands, CI re-runs against a tree somebody would actually merge, the head
holds its queue position while it does (an empty check list reads
`PrPending`), and the *next* cycle decides on a fresh answer -- filing a
fix then, if it is still red, whose body names a real failure. If the
merge conflicts, that is the case a fix task is genuinely for, and it is
filed immediately, now with the conflict named.

Cost: one API call, no agent run, in the case that has been the majority
of this deployment's fix tasks so far.

## Decision 1 -- where the update belongs

**Answer: inside `advanceMergeQueueHead`, in its `!hasFix` branch,
immediately before `fileFixTask`. Not a precondition on being queue
head.**

`advanceMergeQueueHead` gains the client and the entry's observation,
both already in hand at the call site (`syncEntry` holds the whole
`queueEntry`):

```go
fixTaskID, hasFix := fixTaskLink(task)
if !hasFix {
    outcome, err := refreshStaleHead(ctx, store, client, task, e.obs, ref, detail, now)
    if err != nil {
        return err
    }
    if outcome == refreshUpdated {
        // CI is running again against a tree somebody would merge.
        // Decide next cycle, on that answer rather than this one.
        return nil
    }
    return fileFixTask(ctx, store, task, ref, detail, health, checks, outcome, now)
}
```

`syncEntry`'s switch is untouched: the arm that reaches here is already
`isHead && !isFixTask && !blocked && (health == PrConflicted || health ==
PrFailing)`, which is precisely the set of heads worth refreshing and no
wider. Every guard the new behaviour needs is a guard the queue already
computed.

Why not the alternative -- making up-to-date-ness a precondition on
being queue head at all (`isQueueMember`/`queueHeads`):

- `queueHeads` is a pure function over the already-gathered entries, and
  deliberately so. `SyncPullRequests` gathers every entry *before* acting
  on any of them so that "who is at the front of this repo's queue" never
  depends on a change some other entry's handling has half-made. Putting
  a GitHub write in that path would put I/O inside the one loop in this
  file that is *not* isolated per entry -- a failure in the gather loop
  aborts the whole cycle for every repo, on purpose, and a network write
  is a much likelier failure than the store reads that live there now.
- Being behind is not a disqualification from being head. The head is
  the earliest submitted task; a stale branch is a property of it to be
  repaired, not a reason to let a later task merge first. Demoting a
  behind head would reorder the queue exactly where ordering is the whole
  point -- and would do it *most* often for the task that has been
  waiting longest, since that is the one whose base has moved furthest.
- The head is also the only entry whose staleness matters. Everything
  behind it will be refreshed for free, or made stale again, by the
  head's own merge landing on the base. Refreshing non-heads would be N
  writes per cycle to buy a freshness that the next merge invalidates.

## Decision 2 -- telling "behind and stale" from "genuinely failing"

**Answer: don't classify. Ask GitHub to do the merge and let its answer
be the classification.**

This is the question the issue framed as the hard one, and the useful
move is to notice that the queue does not actually need to answer it.
The two candidate signals both disappoint:

- **`mergeable_state`.** Already parsed onto
  `github.PullRequestDetail.MergeableState` and read by nothing (its own
  doc comment explains why it is kept out of the merge gate; task 21,
  "Confirm what GitHub's `mergeable_state` 'clean' actually accounts
  for", is the standing question). It is a single value where we want
  two independent facts, and its precedence order is against us: as far
  as it is documented, `dirty` and `blocked` outrank `behind`, so a pull
  request that is *both* behind and failing a required check reports
  `blocked` and never says "behind" at all. That is exactly the case
  this design exists for. On a repo with no required-check protection
  the same PR reports `behind` (which outranks `unstable`) -- so the
  signal's usefulness depends on branch protection settings that have
  nothing to do with the question. Not load-bearing here. Task 21 stays
  open on its own merits and this design does not wait for it.
- **The check runs' own head SHA.** `github.CheckRun` carries `Name`,
  `Status` and `Conclusion` and no sha at all, so this would be a new
  field -- but adding it would not help. For an `on: pull_request` run
  the check is reported against the pull request's *head* sha, which is
  the sha the queue already has and which says nothing about which base
  the run was merged onto. The staleness lives in the merge ref, which
  the Checks API does not name.

So: `PrHealth` gains no notion of how old its checks are, and no notion
of what base they ran against. Instead, the queue asks the question that
it can get an authoritative answer to, at the moment it matters, with
the call it wants to make anyway.

**`POST /repos/{owner}/{repo}/merges`** with `base` = the pull request's
head branch and `head` = its base branch answers all three cases in one
round trip:

| GitHub says | Means | Queue does |
| --- | --- | --- |
| `201` + merge commit | It was behind; it is not now | Record the attempt, comment, return -- decide next cycle |
| `204 No Content` | Already up to date; nothing was stale | Nothing. `fileFixTask` as today: this failure is genuine |
| `409 Conflict` | Behind *and* it genuinely conflicts | Record the attempt, `fileFixTask` now, naming the conflict |
| `404` | The head branch is not in this repo (a fork PR) or is gone | Record the attempt, `fileFixTask` as today |

The `204` is the whole answer to "is this failure genuine": if the head
branch already contains its base, then whatever CI reported, it reported
about the tree that would actually merge, and there is nothing here for
a merge to fix. No heuristic, no clock, no new health state.

This does mean the queue makes a write to find out something a read
could have told it. That is deliberate -- the read (`GET
/repos/{o}/{r}/compare/{base}...{head}`, whose `behind_by` is the honest
answer `mergeable_state` will not give) would be a *second* call in the
case that matters, and the write is a no-op in the case where the read
would have said "don't bother". If a later change wants "was N commits
behind" in the comment, `compare` is the call to add; it is not needed
for the decision itself.

**Before this ships, confirm the `204`/`409` split against a live
repository.** It is documented, but so is `mergeable_state`, and this
design makes it load-bearing in exactly the way that doc comment warns
about. A `204` misread as "merged" would burn the one refresh attempt
without refreshing anything; a `409` misread as an ordinary error would
turn a genuine conflict into a cycle that errors forever instead of
filing the fix.

That confirmation is what `pkg/github/live_merges_test.go` exists to
make. It is the three calls this section asks for, in `refreshStaleHead`'s
own direction, against a live repository -- opt-in, since it writes to
one over the network, and pointed by default at the repository the
`github-sandbox` capability mints. Its assertions run on every `go test`
against `githubsim`, so the harness stays runnable; what is still owed
is one run of it with a `contents: write` credential in place.

## Decision 3 -- merge, not rebase; and which write

**Answer: a merge commit, made by GitHub, in the pull request's head
branch.**

Rebase is wrong here for four separate reasons, any one of which would
be enough:

- It rewrites the head branch's commits, which needs a force push.
  Nothing in this codebase force-pushes a task branch --
  `Client.UpdateBranch`'s `force` flag exists for release management
  (bwsalmon/agents#398) and is not a write the queue should start making
  to a branch an agent may still be holding a clone of.
- A fix task's `Base` *is* the head branch (`fileFixTask`), stacked so
  that its own PR merges straight back. Moving that base out from under
  an in-flight stacked branch is how you get a fix pull request full of
  unrelated commits, or a push that cannot land at all.
- It discards review history a human may be part-way through.
- The merge is what the agents were already doing by hand. Every commit
  listed at the top of this document is a merge commit. Reproducing
  exactly the change that has been working, at a fraction of the cost,
  is the conservative version of this proposal and the one to ship.

`POST /merges` over the alternative, `PUT
/repos/{o}/{r}/pulls/{n}/update-branch` (GitHub's own "Update branch"
button): the latter is a `202 Accepted` -- asynchronous, so the response
says only that GitHub will try -- and it signals a conflict as a `422`
with a message to parse rather than a status code to switch on. `POST
/merges` is synchronous and its three outcomes are three status codes,
which is precisely the table above. `update-branch` is the better call
only if we ever want GitHub to pick the base revision itself under
concurrency, which we don't: merging the base branch *by name* already
takes whatever its tip is at the moment GitHub processes the call, which
is the freshest thing available and better than any sha we could have
read a moment earlier.

New on `github.Client`:

```go
// MergeBranch merges head into base server-side. Merged is false, with
// a nil error, for GitHub's own 204: base already contains head and
// nothing was written.
MergeBranch(owner, repo, base, head, commitMessage string) (MergeResult, error)
```

with `MergeResult{Merged bool, SHA string}`, plus
`github.IsMergeConflict(err)` alongside `IsPermissionDenied` --
`errors.As` to `*github.Error` and a `409`, the same shape, though
without that predicate's message-sniffing, since 409 on this endpoint is
unambiguous.

The commit message should say who did it and why, in the merge queue's
own voice:

```
Merge main into grain/task-27 (grain merge queue)

Its checks last ran against a base that has moved since; re-running
them against a tree that could actually merge.
```

`githubsim` needs the endpoint too. It already shells out to real `git`
for `BranchExists`, and `tests/e2e`'s `mergeBranchIntoDefault` is
already the exact recipe (clone the bare repo, merge, push) -- so the
sim can answer this one honestly rather than with bookkeeping, including
the `409`, which is the case a test most wants to be real.

Permissions: this is a write to a branch in the repo the pull request
targets, which is the same `contents: write` the queue's existing
`MergePullRequest` needs. A pull request from a *fork* has its head
branch in another repository, and `POST /merges` cannot reach it --
GitHub answers `404`. `PullRequestDetail` carries no head-repo field to
detect that in advance, and it does not need one: `404` is handled as
"cannot refresh this one", the attempt is recorded, and the fix task is
filed as it is today. grain's own pull requests are never from forks
(`finishWithPullRequest` pushes branches into the target repo itself);
this only arises for a PR grain found rather than opened.

## Decision 4 -- not looping

**Answer: one attempt per pull request, persisted, mirroring the fix
policy exactly.**

> **Since amended to one attempt per *repair round*.** Once the merge
> queue began passing a broken head over rather than holding the queue on
> it, the base started moving under a branch while its repair ran -- by
> the queue's own doing, since that is what the changes behind it are now
> doing -- so a resolution could be correct when pushed and merely behind
> by the time it was judged, and be handed to a person for it. A head
> whose repair has finished and still reads broken therefore gets one
> more refresh before `escalateToUser`. The guard below is unchanged in
> kind: `MergeQueueRefreshedAt` is now read against `MergeQueueRepairAt`
> rather than for being set at all, repairs are still capped at one, and
> so the whole sequence is refresh, repair, refresh, a person. See
> docs/data-model.md's merge queue section, and `orchestrator.
> refreshSpent`. The per-base-tip retries this section leaves out below
> are still left out: this is one extra attempt, not a rule keyed on the
> base.

A new nullable column and field, alongside the one the merge queue
already owns:

```go
// MergeQueueRefreshedAt is set the cycle the merge queue tried to bring
// this task's pull request branch up to date with its base -- whether
// or not that merge landed, and whether or not it helped. It is what
// stops a head whose refresh went red from being refreshed again on the
// next cycle, the same one-attempt-then-a-person policy fileFixTask
// already holds ...
MergeQueueRefreshedAt *time.Time
```

`task_observation` gets `merge_queue_refreshed_at DATETIME NULL` in
`schema.go`, plus an `ensureTaskObservationRefreshedColumn` probe-then-
`ALTER` in `Store.Init` -- `CREATE TABLE IF NOT EXISTS` does not add a
column to a table that already exists, the same limitation
`ensureConfigTargetReposColumn` and friends work around -- and the two
statements in `store.go`'s `observe`/`getObservation` widen by one.

Persisted rather than an in-memory `sightings`-style map, and this is
the one place the existing precedent must *not* be followed.
`sightings`' own doc comment justifies living in memory on the grounds
that losing a sighting only ever costs another window of *waiting*.
Losing a refresh record costs a repeated *write to GitHub*, and a
process that restarts in a loop would refresh in a loop. Different
consequence, different storage.

Why one attempt per pull request rather than one per base tip: it is the
same policy, for the same reason, that `escalateToUser` already applies
to fixes -- "suggesting a fix for a fix risks an unbounded chain", from
`core.py`'s own `_suggest_fix`. A refresh that lands and still leaves the
branch red has told us the failure is real; refreshing again when the
base moves once more would be spending writes to re-ask a question that
has been answered. If a deployment later wants per-base retries, the
extension is a `merge_queue_refreshed_base` column holding the sha that
was merged and a guard of "refresh again only if the base tip differs
from that one" -- deliberately left out of this design, and cheap to add
to it.

The loop-safety argument, end to end:

1. Cycle N: head reads `PrFailing`, no fix filed, `MergeQueueRefreshedAt`
   nil, `POST /merges` returns `201`. Flag set, comment written, return.
2. The push moves the head sha, so CI re-runs and the check list is
   momentarily empty. `healthFrom` reads `PrPending` (the registration
   window), the head keeps its position, and nothing merges or files.
   Both existing clocks key their sightings on the head sha, so
   `emptyChecks` and `pendingChecks` restart on the new commit by
   themselves -- no change needed there, and no chance of the refresh
   inheriting the old commit's stall clock.
3. Cycle N+k: fresh verdict. Green -> the head merges, and the whole
   thing cost one API call. Red -> `MergeQueueRefreshedAt` is set, so
   `refreshStaleHead` returns `refreshAlreadyTried` and `fileFixTask`
   runs, its body now describing a failure measured against the current
   base.
4. There is no cycle in which the queue can refresh twice, and none in
   which it both refreshes and files, except the conflict case -- where
   filing is the correct and final answer anyway. (Within one repair
   round, as amended above: the second round runs this same argument
   again, once, after the repair has finished.)

The refresh is additionally skipped when a fix task already exists
(`hasFix`, which is already the branch it sits in) and when the task is
`blocked` or is itself a fix (already excluded by the switch arm). A fix
task's own pull request is never refreshed: it is not a queue member, it
is stacked on a branch that is itself moving, and its base is a branch
the queue is actively managing.

## What still files a fix task, and what it now says

The conflict case is the one the issue calls out as needing to survive
this change, and it does more than survive it -- it gets better. Today a
genuinely conflicted head produces `healthReason`'s "it has conflicts
with `main`", inferred from `detail.Mergeable == false`. Under this
design the queue has *tried the merge and watched it fail*, so
`refreshStaleHead` returns `refreshConflicted` and `fileFixTask` (which
gains the outcome as a parameter) can open the fix's body with what the
agent's job actually is:

> The merge queue tried to merge `main` into `grain/task-27` and it
> conflicted, so this needs a real resolution rather than a merge.

The other two paths into `fileFixTask` -- `204` already-up-to-date and
`404` cannot-reach-the-branch -- file exactly the body they file today.

## What this deliberately does not do

- **It does not refresh a *clean* head before merging it.** A head that
  is behind but whose checks are green still merges on the strength of a
  CI run against an older base. That is real merge skew, and closing it
  would mean every queued task pays a full extra CI round trip; the
  cheaper half of the protection is already there, in that GitHub itself
  judged the merge conflict-free. Worth revisiting if this deployment
  ever actually lands a semantic conflict; not worth doubling every
  task's wall clock against one it has not yet seen.
- **It does not make `mergeable_state` load-bearing.** See decision 2;
  task 21 is unaffected either way.
- **It does not rebase, and it does not force-push anything.**
- **It does not refresh anything but the queue head**, and not more than
  once per repair round -- so twice in a pull request's life, not once,
  since #917 added the refresh that comes after a repair has run. See
  decision 4's amendment; this line said "once per pull request" until
  the 2026-09-07 hand run read it against the code.
- **It does not touch `PrHealth`**, which stays a judgement about a
  single read of GitHub with no notion of time in it.

## Failure modes, and what each costs

- *The merge lands and the fresh tree is red for a real reason.* One
  extra cycle before the fix task is filed, and a fix task whose body is
  accurate. This is the good case for the design even though it "failed".
- *The merge lands and CI never reports on the new commit.*
  `defaultCheckStallDeadline` catches it exactly as it catches any other
  stuck head, timed from the new sha.
- *A human pushes to the branch between our read and our merge.* GitHub
  merges the base into whatever the branch is at that moment, which is
  still the right thing; there is no sha we could have pinned that would
  be better.
- *A human is working in the branch and did not want a merge commit.*
  They get one, and a comment on the task saying the queue made it. This
  is a real cost and the reason the comment is not optional.
- *`POST /merges` is refused for want of a permission.* Ordinary error,
  the cycle retries. Not latched the way `checksUnavailable` is: unlike
  the Checks read, this is a write on the same permission
  `MergePullRequest` already needs, so a deployment that cannot make it
  has a broken merge queue anyway and should say so loudly.

## Code this touches

| File | Change |
| --- | --- |
| `pkg/github/github.go` | `MergeBranch` on `Client` and `RESTClient`; `MergeResult`; `IsMergeConflict` |
| `pkg/github/githubsim/githubsim.go` | `POST /repos/{o}/{r}/merges`, answered with a real `git merge` against the bare repo, including the 204 and 409 |
| `pkg/model/task.go` | `Observation.MergeQueueRefreshedAt` |
| `pkg/model/schema.go`, `pkg/model/store.go` | the column, its migration, and the two statements |
| `pkg/orchestrator/sync.go` | `refreshStaleHead` and its outcome type; `advanceMergeQueueHead` takes the client and the observation; `fileFixTask` takes the outcome and says so in the body |
| `README.md` | the merge queue's narrative (the paragraph beginning "Filing a fix task when a PR goes red is built now") gains the refresh step |

## Tests worth writing

- `pkg/orchestrator`, unit: a failing head with no fix filed makes
  exactly one `MergeBranch` call and files no fix that cycle; the next
  cycle, still failing, files one; the cycle after that files nothing
  more.
- `pkg/orchestrator`, unit: `MergeResult{Merged: false}` (the 204) files
  the fix immediately and makes no second attempt.
- `pkg/orchestrator`, unit: a 409 files the fix immediately, with the
  conflict named in the body, and sets `MergeQueueRefreshedAt`.
- `pkg/orchestrator`, unit: a head that is `blocked`, is a fix task, is
  not its repo's head, or already has a fix linked, is never refreshed.
- `pkg/model`: the migration adds the column to a database created
  before it existed, and `getObservation` round-trips the field.
- `tests/e2e`: the whole path against `githubsim` and a real bare repo --
  a task branch behind `main` whose checks are red, refreshed by the
  queue, going green on the fresh tree and merging, with no fix task ever
  filed. This is the test that would have covered every commit listed at
  the top of this document.
- `tests/e2e`: the conflict variant -- the branch and `main` touch the
  same line -- where a fix task *is* filed, and its body says so.
