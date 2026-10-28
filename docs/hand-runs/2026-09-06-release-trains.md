# Hand run: release trains against a protected real repository

`docs/test-plans/2026-09-05.md` section 6, the first of its four filed
follow-ups. What the plan asked for: a pass on a scratch repository with
branch protection on `main`, cutting `rel.latest`, letting a second cut
move `rel.previous`, taking one backport through to a merged pull
request, cutting again onto a branch name that already exists, and a
backport that conflicts.

## What was run, and what was not

**Not run against real GitHub.** The sandbox this was done in reaches
`github.com` but carries no GitHub credential -- the only git credential
it has is for the local mirror this repository is cloned from. No scratch
repository was created, branch protection was never configured, and none
of the four steps was performed against a real repo. The parts that
genuinely require one are listed under "Still owed" at the end, and they
are the parts the plan was most interested in.

**What was run instead.** Each step was driven against `githubsim` -- a
real bare git repository behind a simulated API, so every branch below is
a real ref that a real `git` created and moved -- and, where the answer
lives on GitHub's side rather than grain's, read off the code. Every
finding below says which of the two it is. That covers all of grain's own
decision-making, which is where both of the questions the plan singled
out turned out to live; it covers none of GitHub's.

Two behaviours found this way are now pinned by tests in
`pkg/orchestrator`, and one of them was a bug, fixed here.

## 1. Two cuts, and where the two names end up

**Established against `githubsim`.** As the README says. One cut creates
`rel.1.0.1` at the source branch's tip and publishes `rel.latest` onto
it, with no `rel.previous` at all -- there is nothing for it to name yet,
and a `rel.previous` pointing at the release under test would be a lie
about what has shipped. A second cut, with the source branch moved on in
between, creates `rel.1.0.2`, moves `rel.previous` onto release 1 and
`rel.latest` onto release 2. `rel.1.0.1` does not move again. Both moving
names are registered as `branch` rows on the repo, once, however many
times the train republishes them.

`TestAReleaseTrainCutsItsFirstReleaseAndPointsLatestAtIt`,
`TestASecondCutMovesPreviousOntoTheReleaseBeforeIt` and
`TestTheMovingBranchesWalkAlongTheTrain` already covered this before this
run, and re-reading them against the branches actually on the bare repo
is all this step amounts to.

**What protection on `main` would add: nothing.** A cut only *reads* the
source branch. Nothing in `publishIteration` writes to `main`, so a rule
on `main` is not in the path of any of it. The configuration that would
be in the path is a rule on the release branches themselves -- a `rel.*`
pattern, which is a very ordinary thing to want -- and that is one of the
things still owed.

## 2. A backport through to a merged pull request

**Established against `githubsim`, except for the merge itself.** A pull
request grain is watching that merges into `rel.latest` files two tasks:
one against the repo's default branch (carrying an empty `Base`, which is
how every task says "whatever the default branch is") and one against
`rel.previous`, both pre-approved and both with auto-merge on, each
carrying its own `backport:<pr>:<branch>` tag so neither is ever filed
twice. Reads, writes and grants come across from the task whose change it
is. This was already covered by `TestAMergeIntoLatestFilesBackportsToDefaultAndPrevious`.

From there a backport is an ordinary task, and its pull request is an
ordinary member of the merge queue; nothing about the merge is
release-train code. Which is why the interesting version of this step is
the one the plan asked for and this run could not do: a backport merging
onto a **protected** default branch. See "Still owed".

## 3. Cutting onto a branch name that already exists

**A bug.** Found against `githubsim`, fixed in this change, and pinned by
`TestATrainRefusesToPublishAReleaseBranchItDidNotCut`.

`ensureIterationBranch` adopted an existing iteration branch exactly as
it stood. For the case it was written for that is right and necessary: a
cycle that cut the branch and then failed to publish the moving branches
has to finish the job next cycle, and re-cutting the branch from a source
that has moved on since would ship a different release under a number
already published.

But "the branch is already there" was two situations wearing one face,
and nothing told them apart. The second is a branch of that name this
train never made. Adopting one recorded a clean, successful release whose
`source` and `sha` say it came off the source branch when it did not, and
published `rel.latest` onto a commit the train never cut -- with no
error, no warning, and the iteration's status reading `cut`.

It is reachable without anybody doing anything strange, by two supported
operations in a row. Deleting a train and filing it again with the same
prefix restarts its numbering at 1, because `CutReleaseIteration` counts
within one schedule id, while `DeleteReleaseSchedule` deliberately leaves
the releases it already cut on GitHub. Observed, on a train that had cut
two releases:

```
after two cuts:      rel.1.0.1=db5aa16  rel.1.0.2=7bd9dad
                     rel.latest=7bd9dad  rel.previous=db5aa16
delete, re-create, one cycle:
  iteration 1  branch=rel.1.0.1  status=cut  source="main"  sha=db5aa16  lastError=""
                     rel.latest=db5aa16  rel.previous=db5aa16
```

`rel.latest` -- the name everything downstream is pointed at, which is
the entire point of the feature -- had been dragged back two releases
onto a commit two behind `main`, `rel.latest` and `rel.previous` now
named one release between them, and grain reported a successful cut.

Worse than "refuses and says why", so a bug rather than a note.

**What it does now.** The iteration's own SHA is what separates the two
faces, so it is written by `Store.MarkReleaseIterationBranch` as soon as
the branch exists and before either moving branch is touched, rather than
only by `MarkReleaseIterationCut` at the very end. A branch that exists
while the iteration holds no SHA is a collision: refused, left pending,
with the reason on the iteration --

```
release branch "rel.1.0.1" is already on acme/widgets at db5aa16 and was
not cut by this train: rename the train's prefix, or delete that branch,
rather than publishing it as release 1
```

-- because the way out is a decision somebody makes, not something
another cycle reaches by retrying. Two neighbours of the same rule went
in with it: an iteration whose branch has since *moved* off the recorded
commit is refused the same way, and one whose branch has been *deleted*
is restored to where it was cut rather than re-cut from a moved source.

The retry case that motivated the adoption in the first place still
works, and is now covered rather than assumed
(`TestARetriedCutFinishesTheReleaseItAlreadyCut`): a cut interrupted
between creating `rel.1.0.1` and publishing `rel.latest` finishes next
cycle, at the commit it was cut at, with `main` moved on in between.

That interruption is also the closest this run could get to protection on
a release branch, and it is the shape a `rel.*` rule would produce: the
iteration branch is created, and the force-move of `rel.latest` onto it
is refused. Grain's half of that is now pinned. GitHub's half -- that a
protected ref refuses a force-move with a 422 that arrives as an
`*github.Error` rather than as something stranger -- is not.

## 4. A backport that conflicts

**A note, not a bug.** Nothing merges, the operator is told why, and
nothing is left half-done. Pinned by
`TestABackportThatConflictsIsNotMergedAndSaysWhy`.

A backport is a task, so a conflict is not something grain resolves --
it is something the run doing the backport is asked to resolve, and
`backportBody` already tells it to ("resolving whatever the two branches
have drifted apart on rather than reverting either side of it"). The
question worth asking is therefore about the pull request that run
pushes, when it does not merge cleanly into the release it is going to,
and specifically about this one being a task grain filed for itself: it
lands pre-approved and with auto-merge on, and "approved automatically"
must not turn into "merged automatically".

It does not. Driven through: the backport's pull request reads
`PrConflicted`, is not merged, and the merge queue asks for exactly one
repair on the same branch, saying so on the task:

> acme/widgets#9001 it has conflicts with `rel.previous`, so the merge
> queue has sent this task back to an agent to repair it. No approval
> needed, and no separate task: the work happens on `grain/task-2` --
> the branch this task's pull request is already open from -- so the
> resolution and the change share one pull request and one round of
> checks.

Nothing is half-done. The pull request stays open, the task stays open,
no second pull request is opened, the original merge into `rel.latest`
stays recorded, and none of the four release branches moves. If the
repair does not land within `defaultRepairDeadline` (six hours) the queue
stops waiting and asks a person; that clock is the merge queue's own and
is covered by its own tests.

One interaction found while looking at this is *not* covered anywhere and
is not obviously anybody's bug, so it is written up as a question rather
than fixed: a backport aimed at `rel.previous` that is still open when
the train cuts again. `rel.previous` is force-moved onto the next
release, and a pull request's base is a name -- so a fix reviewed against
release 1 is now, without anything being said, a pull request against
release 2, with auto-merge on. Whether backports should be filed against
the moving name or pinned to the numbered branch that name meant at
filing time is a design call. Filed as a follow-up.

## Still owed

Everything below needs a real repository and a credential, and none of it
was done. Grain's side of each is now either covered or reasoned about
above; what is missing in every case is what GitHub actually answers.

1. **A `rel.*` protection rule.** Both moving branches are force-moved
   (`setBranch`, `force: true`). A protected ref refuses that. The
   expected shape is a 422 reaching `publishIteration` as an
   `*github.Error`, recorded on the iteration's `LastError` and retried
   next cycle forever, with the train not cutting again meanwhile
   (`HasPendingReleaseIteration` suppresses the next firing and leaves
   `NextRunAt` where it is, so the train stalls rather than losing
   releases). Confirm the status and that the error is legible; confirm
   the partial-failure ordering holds in practice, that `previous` moving
   before `latest` leaves both names on the release that was under test
   rather than on a state that lies about what shipped.

2. **A backport merging onto a protected `main`.** This is the one to do
   first. A backport lands pre-approved *in grain*, which is not a GitHub
   review. Where `main` requires one, the pull request reads
   `Mergeable: true` with `MergeableState: "blocked"` -- and
   `MergeableState` is deliberately not in the merge gate
   (`github.PullRequestDetail.MergeableState`'s own doc comment says why,
   and names this exact case), so `healthFrom` answers `PrClean` and the
   queue attempts the merge. GitHub refuses it, 405. Reading the code,
   that error returns from `syncEntry`, is collected per entry by
   `SyncPullRequests`, and is retried every cycle with no bound: neither
   `defaultCheckStallDeadline` (which wants `PrPending`) nor
   `advanceMergeQueueHead` (`PrConflicted`/`PrFailing`) reaches a pull
   request that is clean and refused. If that is what happens, a
   deployment backporting onto a protected default branch has a queue
   head that retries forever and never asks anybody -- which would be a
   bug, and one worth knowing about before somebody configures it. It was
   not confirmed, because confirming it needs the protected repository
   this run did not have.

3. **Opening a pull request onto a protected ref**, which the plan lists
   and which is expected to be unremarkable -- protection governs merges
   and pushes, not opens -- but has never been watched.

None of the three is mechanisable from here without giving `githubsim` a
notion of branch protection, and a simulator's answer to "what does a
protected ref do" is only ever the answer whoever wrote it assumed. That
is the gap section 6 named, and it is still open.
