# Merging a branch that is behind its base

**Status: implemented, as described, except where "The merge predicate"
and "The overlap re-check" below say otherwise** -- that section replaced the base-branch health
gate this document's "The compensating control" argues for, and is the
current rule (grain/task-198, with "What counts
as red" below added by grain/task-202, "A merge grain did not perform" by
grain/task-320, "A repository grain has never merged into" by
grain/task-331, "One run in flight per branch" by grain/task-334 and "A
provider that is not a source of verdicts" by grain/task-270). This is the
companion to `merge-queue-staleness.md`, and in one sense its opposite:
that document is about the queue writing a branch's base into it, and
this one is about the queue mostly not doing that any more.

## The merge predicate (grain/task-471)

**Status: implemented as described, except the force merge, which is
specified here and enforced in code with nothing yet able to ask for it.**

This section is the current rule. Where the rest of this document
disagrees with it -- and "The compensating control" below does -- this one
is what runs, and the older reasoning is kept because it was weighed
rather than forgotten.

### Two layers, which must not mix

- **The predicate**: may this merge? Fixed, small, short enough to hold in
  your head. `pkg/orchestrator/mergepredicate.go`, and nowhere else.
- **The strategy**: which jobs to run, when, how batches are formed, what
  is cached, published or skipped. Arbitrarily clever, changed freely,
  tuned for cost.

> Strategy may change *when* a change finds out. It may never change
> *whether* it was eligible.

### The rule

> A change may merge into branch `B` **iff it merges cleanly into `B`**,
> and:
>
> - **no batch is in flight for `B`**, and the change has a green grain CI
>   run on its own head; **or**
> - **a batch for `B` is in flight, that batch is green, and the change is
>   the head of that batch's frontier.**

**"In flight"** spans from the moment a batch's tree is fixed through to
its last member merged (`model.MergeBatch.InFlight`), not merely the
seconds spent merging. If the lock covered only the merge phase, a
non-member could land during the batch's forty-minute suite and the
members would then complete `base+A+X+C` -- a tree nobody tested and
nobody chose. This is also what grain already did from the other side:
`model.MergeBatch` has always said that "a batch whose base has moved
since is abandoned rather than believed".

**The frontier** is the maximal unmerged suffix of the batch's own member
order, and only its head may merge: `base+A+C` is as untested as `base+A`.
It is valid only while `B`'s tip is exactly *batch base + the merged
prefix*, which grain checks against the tip it reads after each step; if
`B` is anywhere else the batch is void.

**The mode is the lock.** "Nothing else may land in between" is not an
extra condition to check -- only one arm is ever active, because a batch is
either in flight for `B` or it is not.

### Why the frontier arm exists at all

The tree a batch tested is `base+A+B+C`. `base+A` and `base+A+B` were
tested by nothing. A batch merged one pull request at a time therefore
passes through unjudged intermediate states, and if it is interrupted --
a restart, a crash, both of which happened on this deployment on
20 Sep 2026 -- `B` is left on a tree no green covers with the remaining
members holding no basis to continue.

The frontier arm is that basis: merging along it **completes a tree that
was tested**. The invariant is "`B` is always on a path to a tested tree,
and every merge advances along it" -- you may move towards the tested
tree, never sideways. It is also what makes the older equivalence honest:
"merging each branch in order is logically equivalent to merging the
branch itself" holds only with the frontier rule, because without it most
of those steps are authorised by nothing.

It does not make a member green. A verdict about `base+A+B+C` is not a
verdict about `A`, and nothing writes one onto a member's own CI status;
what the arm authorises is a *merge*, on the strength of the batch's own
verdict.

### The residual, stated rather than papered over

A batch abandoned mid-merge leaves `B` on `base+A`, a tree nothing tested.
**This was already true and is not removable here.** The frontier rule
does not eliminate the untested intermediate: it guarantees the
destination and bounds how long anyone sits on one. What judges `base+A`
is the post-merge base-branch run, which reports and holds nothing
(grain/task-461).

### Failure while merging a batch: abandon, do not resume

On any failure part-way through merging a green batch -- a member that
will not merge, a member whose head moved, an API error, a daemon that
died:

- **What merged stays merged.** It cannot be unmerged and should not be.
- **The batch is dropped and the lock released.** The members that did not
  land still hold their own greens, so they return to the ordinary queue
  and are re-batched later. "Try again later" happens by itself: no resume
  machinery, no batch left open.
- **The merge branch is destroyed** (`dropBatchRef`,
  `github.Client.DeleteBranch`). A half-merged batch's throwaway ref
  stands for a merge nobody is going to perform. The members' own branches
  are untouched -- nothing is ever merged *from* that ref.
- **The lock is recoverable.** grain merges a batch's frontier inside the
  cycle its verdict arrives, so a part-merged batch found at the *start* of
  a cycle can only be one whose daemon stopped. It is abandoned on sight,
  which needs neither a timer nor a liveness check of its own: finding it
  there is the check. A stuck lock is exactly the deadlock class this rule
  removes.
- **The reason is recorded on the batch**, because "we merged A and then
  stopped" is otherwise indistinguishable from a force merge to whoever
  reads the history later.

### What was removed: the base-branch health gate

`brokenBaseBranch` used to be read by `syncEntry` before merging, and a
`model.BaseBranchCI` row concluding `failure` stopped anything being
merged into that branch. That is strategy wired into the predicate, and a
gate whose failure blocks the only route to fixing it:

> "A broken release means we can't officially push out new changes. If that
> also means we cannot update the code then there is literally no way to
> fix the release."

The design half-conceded it already -- the repair task's own pull request
was exempt and merged while everything else waited -- and that exemption
was the admission: one lane wide, and dependent on grain having filed a
repair task *and* matched its id.

It is also less risky to remove than "The compensating control" below
implies, because that section predates batching: the conflict the gate
exists to catch is now mostly caught an hour earlier, on a throwaway ref,
with nothing having reached the base branch.

**The run itself stays, as information rather than as a gate.** It is how
grain tells "this batch broke `main`" from "`main` was already broken", and
what a repair task is filed from. A change waiting its turn is still told
the branch it will land on is red, naming the branch, the commit and the
repair -- and now without claiming a hold that does not exist.

**What does wait, waits on a task.** Work that could only inherit the red
-- a new task targeting the branch, a merge-queue repair on it, a pull
request whose only failures are the base's own -- gets a `depends-on`
link to the branch's open repair task, and reads "blocked on #N" like any
other dependency (`orchestrator/baserepairlink.go`). That replaced two
bespoke waits: a dispatch hold that recorded a free-text "incident lock"
against everything on the branch for as long as any repair task stayed
open, and a merge-queue state, `waiting_base`, that named no task. The
repair task's lifecycle follows the branch: it is closed as superseded the
moment the branch's own grain CI is green, whoever made it so, which is
what releases its dependents -- a pull request among them is re-armed (the
base merged in and its CI re-run), not handed to an agent. A red that has
not earned an agent, or whose repair was closed while it stayed red, has
no task to point at and holds nothing. Release triage tasks hold nothing
either: a failed release is images that did not ship, not a broken tree,
and a release that publishes closes them.

**A person can say a task is the repair** (grain/task-3451). Without a
way to say it, a task filed to fix the branch, like "De flake tests" or
"fix main's go vet break", was linked as waiting on the repair it was
meant to be. Ticking "This fixes a red `<base>`" (or
`fixes_base_branch` on `propose_task`) gives it the same branch repair
tag grain's own repair carries. So it is never made to wait, and
marking it drops a wait it already had. Once approved, it is the
branch's open repair for single flight: grain files no repair of its
own while it is open, and other work waits on it. If grain's repair is
already running, the person's task runs too, and a comment tells it
about the other one. A green branch closes grain's repair but not a
person's, which may still have work to do. The person's task gets a
comment instead, and the work that was waiting on it is released.

### Where this leaked in, for context

Four incidents on 19-20 Sep 2026, all one fault -- strategy leaking into
the predicate:

- `ReleaseWorkflowsDisabled` (a publishing switch) stopped `main` being
  judged, and ~100 merges landed on an unjudged tree over 33 hours
  (grain/task-458).
- the post-merge base-branch run (a conflict-detection strategy) became a
  merge condition, and therefore a deadlock;
- a cache miss (pure performance) became a red verdict (grain/task-464);
- `BaseBranchCIJobs` empty (a cost decision) became "merge nothing".

### The seam: what "green" consists of

The predicate says "green", and what green *is* resolves to the
repository's own declared suite for the event, read from its workflow
files -- not from operator configuration.

> "The CI is defined in the repo, we don't need the operator to redefine it
> again in the UI."

`BaseBranchCIJobs` is already narrowed to an optional cost narrowing
(grain/task-461). No knob may be added that changes what counts as green;
if green can be redefined from a text box, the predicate only looks fixed.

### Admission to a batch is a separate decision

A batch member must still be green on its own head (`batchable`,
`grainCIVerdictOf`). That is deliberately a *named, separate* decision
from eligibility to merge, so that relaxing it later does not touch the
predicate. Both halves of the trade are in `batchable`'s own comment: the
relaxation would dissolve the stuck-verdict wedge (a cancelled CI verdict
is never retried, so a task can sit in `waiting_ci` on an answer that can
never arrive -- tasks 149, 264, 312 -- and such a branch cannot enter a
batch today), and it is deferred because a batch whose members were never
individually green is much harder to attribute when it fails. **This
change does not fix that wedge and does not claim to.**

### The two operator controls

Both belong on the surface that *states the problem* -- the sentence
`baseBrokenStatus` writes.

1. **Run the base-branch check now.** An operator-dispatched base-branch
   run for a branch, without waiting for a merge to move the tip. **Built
   in grain/task-480**, and described under "Asking for a base-branch run
   by hand" below. The other half of the hole it closes was closed here: a
   base-branch run that grain itself cancelled -- every daemon restart and
   upgrade does this -- is recorded as a verdict about nothing and asked
   again, where before only `errored` was retried and no new run started
   until the tip moved. An *operator's* own cancel is still left alone
   (`ci.CancelByOperator`), which is exactly the case the button is for.
2. **Force merge, with a reason.** The one sanctioned exception to the
   predicate, and it is built (grain/task-479). `POST
   /api/tasks/{id}/force-merge` -- the "Force merge" button on the task
   pane, `ui.Client.ForceMerge`, `grain force-merge <id> <reason>` --
   records the override on the task; the next `SyncPullRequests` cycle
   reads it, `mayMerge` takes the forced arm, and **grain performs the
   merge itself**, so the task moves to merged, the queue row closes, and
   post-merge dispatch and the base-branch run fire as they do for any
   other merge. That is the whole argument for it: the hand merge it
   replaces left every one of those undone.

   A reason is required. It is recorded on the task when it is asked for,
   and on the task and the pull request again when the merge happens,
   alongside what was not green at the time -- the queue's own refusal,
   which names the check, and grain CI's conclusion for that commit. Each
   one appends a row to a bounded history (`model.ForceMergeRecord`), and
   the note says how many have been forced in the last seven days, which
   is what makes "it should be rare" checkable rather than a matter of
   opinion.

   A conflict is still a refusal: `mayMerge` turns down a forced merge
   that does not merge cleanly, because a merge GitHub will not perform is
   not something an override can authorise. Such a request stays
   outstanding and lands when the branch merges cleanly, and the queue's
   ordinary repair is left free to be what resolves it.

   It is an operator's action. There is no MCP tool, no task field, and
   nothing a running agent can do to force its own change in. A task with a
   run in flight is not refused, because the run most often in the way is
   one blocked in `wait_for_checks` on CI that will never finish: forcing
   the merge stops that run (or takes a task a Stop already parked in
   `awaiting_reply`), marks the task completed so the next cycle reads it,
   opens the pull request for its pushed branch if the run never did, and
   cancels the task's in-flight CI. The pin to the reviewed head is what
   keeps that safe -- a commit the run pushed after the operator looked is
   withdrawn rather than merged.

### Asking for a base-branch run by hand (grain/task-480)

**Status: implemented as described.**

The first of those two controls, and the shape of it is the point: it is a
**request recorded on the row**, not a run started by the request.

`POST /api/repos/{owner}/{name}/base-branch-runs` writes
`RunRequestedAt` and `RunRequestedBy` on that branch's `model.BaseBranchCI`
row and returns; `reconcileOneBaseBranchCI` honours it on its next cycle.
Nothing else changes about how a base-branch run is started -- the
one-run-in-flight bound (`baseCIInFlight`), the policy the run is asked
under (`baseBranchCIWanted`) and the line that says which jobs the run is
made of all stay in `startBaseBranchCI`, where they already were. A
handler that started its own run would be a second way past that bound,
and that bound is what stopped several full image builds of this
deployment's own `main` stacking on each other (grain/task-334).

Three consequences worth stating, because each is a thing an operator
sees:

- **A request made while a run is going is kept, not refused.** One run
  judges a branch at a time, so it is taken up on the cycle after that run
  concludes, and the response says which of the two happened. A control
  whose work is done by a reconciler has to say when, or it reads as a
  control that did nothing.
- **A run asked for by hand resets `Attempts`.** That counter bounds
  *grain* re-asking a question nothing answered (`baseCIRetryDue`); a
  person who has looked at the branch and pressed the button has asked a
  fresh one, and a button that stopped working on the commit somebody is
  standing in front of would be the wrong bound in the wrong place.
- **Only a branch grain already has a row for.** The row is the opt-in,
  here as in the reconciler. Enrolling a branch nothing has ever judged is
  a settings decision (`RepoConfig.WatchDefaultBranch`), not something a
  button in a task's pane should take, so an unwatched branch is a 404
  that says how a branch comes to be watched.

It is offered where the problem is stated: under the `base_broken`
sentence on a queued change's own pane, and on the repository's Release
pane beside "Run release by hand", one button per branch grain is
watching.

What it does *not* do is change what a cancel means. A run an operator
cancelled still keeps the word cancelled and is still never restarted by
grain (`collectBaseBranchCI`, `ci.CancelByOperator`). That is the whole
reason this control exists: the cancel is a decision about a commit, and
the way to reverse a decision is for the person who made it to say so.

## The overlap re-check (grain/task-985)

**Status: implemented. Every repository is in `observe` unless configured
otherwise; nothing enforces it until an operator says so.**

The predicate above lets a change green on its own head merge as it
stands, and a merge batch re-checks only when two or more changes are
ready together. In the week investigation task 975 measured, 274 of 331
merges into `bwsalmon/grain` went in on their own green with no second
look. The batch re-check caught one real semantic conflict (a duplicate map
key in `pkg/ui/route_scopes.go`). The one conflict that turned `main`
red (10-24, `TestRouteScopesAreTotal`, about six hours) came through the
single-merge path, which checked nothing. Re-checking every merge would
double the suites to catch something that happens about once a week. The
overlap re-check asks for a second suite only where the tree a change was
tested on and the tree it lands on can differ in a way that matters to it.

### The rule

Let `F` be where the pull request's head last met its branch, `T` the
branch's tip now, `P` the files the change touches (`F...head`) and `L`
the files that landed since (`F..T`).

- `F == T`: nothing has landed since. The change merges.
- Otherwise it is **re-checked on top of `T`** if any of these hold:
  1. **same-file**: `P` and `L` share a path (a rename counts under both
     names);
  2. **same-package**: some Go package directory has a changed `.go` file
     on both sides;
  3. **global-input**: `L` touches something every build reads: `go.mod`,
     `go.sum`, `go.work`, a `package.json` or lockfile anywhere in the
     tree, `.grain/workflows/**`, a `Makefile` or `*.mk`, `Dockerfile.build*`,
     or `scripts/build*`.
- If none of them hold, it merges on its own green.

A comparison GitHub cuts short (its 300-file cap) records the clause
`unknown` and counts as a trigger, because a rule that cannot see both
sides cannot say they do not overlap. A comparison GitHub refuses holds
the change in `enforce` mode and leaves it unrecorded otherwise.

**`F` is the fork point, not the run's recorded base.** grain CI runs a
task's suite on the pull request's head as it stands, not merged onto its
base. The base commit a run records (`ci.Request.BaseCommit`,
`JobRun.BaseCommit`) is the branch's tip when the run was planned, and it
is kept to invalidate cached failures. It is not in the tested tree unless
the branch had already merged it. The base the tested tree does contain is
the merge base of head and branch, which GitHub's compare endpoint
reports. Using the recorded base would leave out everything that landed
between the fork and the run, which is where a stale branch's conflicts
live.

This is strategy, not predicate. It decides *when* a change finds out, and
`mayMerge` is unchanged. A re-check is an ordinary merge batch with one
member on the queue's own ref (`grain-merge-queue/<deployment>/<branch>`). It
holds the branch the way any batch does, lands on the frontier arm if it
passes, and goes through the same handling as any batch if it fails: the
uncached flake recheck, the culprit sent back to its agent, and the
release of a failure the branch itself is carrying ("inherited from
base"). After that release the change merges on its own green, rather
than being re-checked on the same tip again. Batching stays an
optimisation among overlapping changes, and a batch of one is not
abandoned when batching is turned off.

### Modes, and how to switch

`repo_config.merge_recheck`, per repository (`model.RepoConfig.MergeRecheck`):

| mode | what merges | what is recorded |
| --- | --- | --- |
| `off` | as before the rule | nothing |
| `observe` (default; also what an empty value means) | as before the rule | one row per merge |
| `enforce` | a triggered change is re-checked first | one row per merge |

`observe` changes nothing about what merges. Its only cost is three GitHub
reads per merge (the tip and two comparisons).

To switch a repository, set `merge_recheck` to `enforce` (or `off`) by
either of two operator routes:

- `PUT /api/repos/{owner}/{name}/settings` with `{"mergeRecheck":"enforce"}`.
  `GET` on the same repository reports the effective mode as
  `mergeRecheck`, with the default filled in.
- In the deployment's state repository, set `"merge_recheck": "enforce"`
  on the repository's row in `tables/repo_config.json`. For this
  deployment, that is a pull request against grain/config changing the
  `bwsalmon/grain` row.

A repository cannot set its own mode. `merge-recheck` in its
`.grain/settings.yml` is refused as a verdict key, the same as
`merge-on-green`: a repository does not decide that its own changes need
no second look.

### What is recorded, and how to read it

Each merge leaves a `merge_recheck_history` row (`model.MergeRecheckRecord`,
the newest 1000 are kept). The row records the mode, the head, fork and
tip, whether the rule triggered and which clauses fired, whether the
change was in fact re-checked (a batch, or an enforced re-check) and
whether that re-check caught it, and the branch's next post-merge verdict
(green or red) on a tree containing it. Rows are written on every merge
path: a single merge, each batch member merged on the frontier, and a
batch culprit (caught). `read_grain_metrics`' `merges` section adds
`rechecks`, which reports `merges`, `triggered`, `skipped`, `triggerRate`,
`baseCurrent`, `clauses`, `packages` (where same-package fired),
`rechecked`, `triggeredAndCaught`, `skippedAndCaught` and
`skippedBaseWentRedAfter`.

Read `triggerRate` as the suites `enforce` would spend, and
`triggeredAndCaught` as what they buy. `skippedBaseWentRedAfter` is an
upper bound on what skipping costs: a skipped merge whose branch went red
on its next run may be the culprit, a flake, or another merge in the same
run.

To estimate the trigger rate before any history has been recorded, replay
the rule over a clone's own merge commits:

    GRAIN_RECHECK_REPLAY=/path/to/clone GRAIN_RECHECK_REPLAY_REF=origin/main \
    GRAIN_RECHECK_REPLAY_N=300 go test ./pkg/orchestrator -run TestReplayOverlapRule -v

On `bwsalmon/grain`'s last 331 first-parent merges (to 2026-10-26), the
replay gave 177 re-checked (53.5%) and 154 skipped, 85 of those with
nothing landed since their fork. By clause: same-package 138, same-file
95, global-input 95 (`.grain/workflows/tests.yml` alone in 59). Clause 2
fired most in `pkg/cli` (41), `pkg/orchestrator` (19), `pkg/daemon` and
`pkg/ui` (16 each), and `pkg/model` (15). The replay reads git only, so a
squash or rebase merge (no second parent) is not counted, and it cannot
see a re-check that already happened.

## The cost this removes

A grain CI verdict is about **one exact commit**
(`orchestrator.grainCIVerdictOf`, and `withGrainCI` which reads it). That
is deliberate and it does not change here: a verdict about any other
commit is not a pass, and the full suite has to have run against the
branch's own head before it merges.

The consequence is that *any* write to a branch's head throws its verdict
away and starts another suite. The suite on this repository is 15-22
minutes. So a queue that brings every branch up to date as `main` moves
re-tests every open branch on every merge: N open branches and N merges
cost on the order of N² suites.

Measured here on 2026-09-17, on one task, four heads in ninety minutes,
each with its own complete green suite:

| commit | run | green at |
|---|---|---|
| `62b80c2` | `6f2c7575ffa11546` | 20:03 |
| `14efa2ca` | `cbc3bdeb7906c500` | 20:52 |
| `bc4fdc69` | `525c5c5bce439756` | 21:19 |
| `aa87a93a` | `73f63ae92ad8aff2` | 21:35 |

At 21:21 that deployment's CI log read `this job is waiting for one of
the 10 sandbox(es) this deployment keeps for CI -- they are all in use`,
and grain/task-173 had just taken the same host from fifteen concurrent
jobs to roughly four.

## What changed

1. **A branch that is behind its base and merges cleanly is merged as it
   stands.** This is what GitHub does for an ordinary pull request with
   *"Require branches to be up to date before merging"* switched off,
   which is how most repositories are configured.

2. **A head that grain's own CI failed is no longer offered a speculative
   base merge first** (`refreshWorthTrying`). `refreshStaleHead` exists
   because GitHub's `on: pull_request` checks run against
   `refs/pull/N/merge`, so a red verdict really can be about a tree
   nobody would merge and a base merge really can clear it. A grain CI
   verdict has no merge ref in it at all -- it is about the branch's own
   commit -- so staleness cannot be what made it red, and merging the
   base in only spends a second twenty-minute suite reaching the answer
   already in hand. Where GitHub's checks are the gate, the refresh is
   exactly as it was.

3. **A branch that genuinely conflicts still goes back to an agent.**
   That is the one case where the branch must be updated, and it is a
   real resolution rather than a merge the queue could make itself.

4. **`model.RepoConfig.RequireBranchUpToDate`** is the way back, per
   repository, defaulting off. With it on, the queue merges the base into
   a clean-but-behind head itself (`bringHeadUpToDate`) and waits for CI
   on the commit that makes -- GitHub's strict rule, done by grain.

## The compensating control

An up-to-date rule buys protection from one thing: two pull requests that
are each green alone and broken together. Dropping the rule without
replacing that protection would trade a slow queue for a broken `main`
nobody is driving.

grain has always run its own CI on the base branch after a merge
(`startPostMergeCI`, since grain/task-334 `recordPostMergeCI` and the
reconciler). Until this change **nothing collected that run's verdict** -- it was started and forgotten, so a `main` a merge had just
broken was found out about by whichever task failed mysteriously next.
(And until grain/task-320 a merge grain performed itself was the *only*
thing that started such a run at all, which is its own hole: see "A merge
grain did not perform".)

Now:

- the run is recorded per (repository, base branch) as
  `model.BaseBranchCI`, and collected by the `base-branch-ci` reconciler;
- while it reads `failure`, the merge queue merges nothing further into
  that branch, and every queued change's row says so
  (`base_broken`, naming the branch, the commit and the repair);
- and the failure earns one repair task, filed against that branch,
  carrying the merged task's own grants and naming the merge it went red
  after -- while saying plainly that the merge need not be the whole
  cause, since the point of this check is the pair of changes neither of
  which is wrong alone.

> **The second of those three bullets no longer describes what runs.** The
> base-branch run stopped being a gate in grain/task-471 -- see "The merge
> predicate" below, which is the current rule, and why this reasoning was
> weighed and replaced rather than deleted. Everything else in this section
> still holds: the run happens, it is recorded, and it earns a repair.

Three things keep that from being a gate that never opens:

- **the repair is exempt from it** (`BaseBranchCI.RepairTaskID`, which is
  `PipelinePause.UntilTaskID`'s shape), so the one change that can fix the
  branch merges into it however far down the queue it sits;
- **a verdict about a commit the branch has moved past is re-asked**. If
  somebody pushes a fix to `main` by hand, the reconciler starts a fresh
  run against the tip that is actually there rather than holding the
  queue on an answer about a tree that has gone. The queue reopens on a
  verdict, not on the assumption that whoever pushed knew what they were
  doing. Since grain/task-320 this is how the check is triggered at all,
  and not only how a failure is cleared -- see "A merge grain did not
  perform" below.
- **a failure that does not reproduce clears itself**, which is the next
  section.

### When this deployment does not run the check (grain/task-3332)

Everything above happens "for every repository grain CI is enabled for",
and until grain/task-3332 that was the whole condition: switching a
repository's release off, or its standing work as a whole, changed
nothing here. On Oct 12 gke-labs/bouncer had `release=off` and the check
went on running `nightly.yml`'s 13 jobs on every new tip of `main`, every
run erroring on a team refusal and being retried up to four times.

The check is now a work item of its own, `base-ci`
(`model.BaseBranchCIItem`), resolved like every other through
`model.RunsWorkItem`: it follows the repository default ("Standing work on
this deployment" on the repo page) unless its own override says otherwise.
When it resolves to no:

- `baseBranchCIWanted` refuses with its own reason, which a merged task's
  grain-ci grant does not overrule, so a merge records nothing and starts
  nothing;
- the reconciler starts no run on a new tip, re-asks nothing that judged
  nothing, confirms nothing and files no repair -- and stands an existing
  row down (`standDownBaseBranchCI`): a run in flight is cancelled, and the
  conclusion, pending merges, a by-hand request and the repair it names are
  cleared, so the row is the no-verdict shape every reader already treats
  as nothing to wait for;
- so the merge queue's `base_broken` rows, inherited-failure attribution,
  `waiting_base` links to a repair and the base-branch gate reading all see
  no verdict expected, rather than pending or red. Tasks that were waiting
  on a repair of the branch are released.

The row is kept: it is still the list of branches a release switched back
on by its own item follows, and its commit is still what a task's required
run compares its base against. The deployment's own configuration
repository (`grainci.RequiredFor`) is judged whatever the switch says.

What it does not stop is anything a person filed: their tasks' own CI, the
merge queue for them and the repair of their pull requests all run as
before.

## A merge grain did not perform (grain/task-320)

As first written the whole control hung off one event: `startPostMergeCI`,
on the cycle the pull-request reconciler observed **grain's own merge of a
task's pull request**, with that task holding the `grain-ci` grant. Nothing
else in grain ever tested a base branch.

That is a narrower trigger than the property needs, and the gap is not
hypothetical. `gke-labs/kontur`'s `main` took five commits with no run
against any of them -- one pull request merged by hand to unblock the
queue, one merged from a pull request nobody had filed as a grain task,
and three that looked like grain's own -- while the row went on describing
a commit from before all of them and the queue went on merging onto an
untested tree. The days it happened over are the days everybody believed
this document described what was running.

Each of these is enough on its own to produce that silence, and all three
are now closed:

1. **The trigger is the branch's tip, not a merge grain authored.** Every
   cycle, `reconcileBaseBranchCI` reads the tip of each base branch it
   tracks and starts a fresh run whenever the recorded verdict is about
   some other commit -- whatever that verdict said. Before, only a
   *failure* was ever re-asked; a green row at a commit five merges old
   was left alone forever. A hand-merge, a push straight to `main`, a
   pull request from outside grain and a merge that landed after grain had
   stopped watching the task are now all the same event: the tip moved,
   and nothing has judged what is there. Who moved it decides nothing,
   because the queue merges onto the result either way.

2. **A run that judged nothing is asked again.** An `errored` run, or one
   lost with the daemon that was running it, was recorded and then
   forgotten -- `errored` holds nothing, and the next merge was the only
   thing that would ever ask again. kontur's last `main` run died exactly
   that way, in an upgrade shutdown. It is now retried, bounded by
   `BaseBranchCI.Attempts` on `baseCIMaximumAttempts` (the run and
   `maxInfraRetries` = 2 automatic re-runs) and spaced by
   `ciRetryDelay`, so a deployment whose CI cannot get a job onto a
   machine does not answer that with a suite every cycle. Each re-run
   asks only for the jobs the run had not passed, uncached
   (`Recheck`), and a failure whose every failing job failed on a
   capability, credential, quota or registry limit is treated the same
   way -- held for nothing -- until those re-runs are spent
   (docs/design/release-from-trunk.md, RFT-2). A *cancelled*
   run is retried the same way, whoever cancelled it: an operator who
   stops a wedged or duplicate run is stopping that run, and leaving the
   tip unjudged for good because of it was a dead end (task 285).

3. **Whether a merge starts a run is the repository's decision, not the
   merged task's.** The gate was `grainci.Granted(task.Grants)` alone, so
   a task filed before its repository was opted into grain CI -- or one
   somebody unticked the capability on -- merged and left `main` untested.
   It now reads the repository's own settings (`baseBranchCIWanted`), with
   the task's grant still enough on its own for the one branch somebody is
   trying grain CI on.

And a merge that starts no run **says so, and says why**. The
investigation above had to reconstruct an absence from a list of commits;
the log now carries the reason at the moment it happens.

One consequence to be clear about: a tip-driven run is the same trusted
`release` run a merge's own is (`ci.EventRelease`, `TrustedRelease`), which
is what makes its verdict comparable -- the same jobs judge the branch
however the commit got there. So a commit pushed to `main` by hand now
also runs that branch's release jobs, where before nothing ran at all. For
the repositories here that means the artifacts that track `main` keep
tracking it, which is the point of a workflow triggered on a push to
`main`; a repository whose release jobs are not idempotent per commit
wants `ReleaseWorkflowsDisabled`, which keeps this check and moves it to
`push` ("Publishing is not judging", below).

## Publishing is not judging (grain/task-330)

`RepoConfig.ReleaseWorkflowsDisabled` is named for publishing -- it
"suppresses the trusted release event Grain normally emits after a pull
request merges into this repo" -- and the base-branch run *was* that
event. So turning releases off used to turn off the compensating control
this whole document is about: an operator who switched publishing off
because a publish job was broken silently lost the gate that stops the
queue merging onto a red `main`, and a row already holding a red base
branch could then never be refreshed either, because nothing would ever
start another run for it.

The two are now separate, and since grain/task-461 they are separate for
every repository rather than only for one that has turned publishing off.
**The judging run is always a `push`** -- untrusted, no release secrets,
nothing published -- and **`RepoConfig.BaseBranchCIJobs`** narrows it
where an operator has said how. A failure of it holds the queue and earns
a repair; the confirmation is asked under the same event, because a
second opinion planned under another one is a different question.

- **Releases on** -- the overwhelmingly common case. The base branch is
  judged by that push run, and the trusted `release` event a merge emits
  is a *second* run at the same tip, started by
  `orchestrator.reconcileBaseBranchRelease` and recorded on
  `model.BaseBranchRelease`. The queue does not read that row, so a
  publisher that fails is images that did not ship rather than a
  repository that cannot merge. See "Releases on: the judging jobs answer
  `release`" below for the rule this replaced and why it went.
- **Releases off.** Exactly the same judging run. What the switch turns
  off is the second run, which is the whole of what its name claims.
- **Judging jobs named.** The same push run, narrowed to exactly those
  jobs (`ci.Request.Jobs`, which `startBaseCIConfirmation` already
  narrows by), whether releases are on or off. This is a cost control --
  on this repository `tests.yml` declares seven jobs and three of them
  would carry the gate in a fraction of the wall-clock -- and the way a
  repository whose publish job sits in its own push plan keeps that job
  out of the gate. It used to be read only with releases off, because
  narrowing a run that was also the publishing would have stopped the
  repository releasing; now that the publishing is a run of its own there
  is nothing left to condition it on.

### Naming no jobs used to mean naming no gate (grain/task-458)

Until grain/task-458, the third case above was a fourth one: releases off
with nothing named started **no run at all**. The reasoning was that grain
cannot tell a publishing job from a judging one by looking, so "run
everything" is not a safe reading of a repository that has asked not to
publish -- for an Actions-backed workflow triggered `on: push` to `main`
the publish job is in the very plan a push run would run, and it would
fail for want of the secrets it is not given rather than be skipped,
trading a missing check for a permanently red base.

That reasoning is sound for that shape of repository and was wrong as a
*default*, because the unset case is what every other repository falls
into by accident. On 19 Sep 2026 releases were switched off on
`bwsalmon/grain` because the publish job was flaky, `BaseBranchCIJobs` was
empty, and the result was no base-branch run for `main` for **33 hours
across roughly 100 merges** -- with `RequireBranchUpToDate` off, so the
one control that catches two branches green apart and broken together was
gone too. The queue went on merging onto an unjudged `main`, nothing on
any surface said so, and it was found by somebody reading `git log` on the
settings repository. A switch whose name says "releases" had taken the
tests with it for a day and a half, and an operator ruled that it should
do only what it says: *"Disabling releases should do just that. Don't run
releases automatically. It should do nothing else."*

So the unset case now falls the other way: toward a gate that may be wrong
for an unusual repository, rather than toward no gate at all for every
repository. The repository in the unusual shape names its judging jobs --
one field, which already existed, and which is exactly what it is for.
What did *not* change is the secrets boundary: the judging run is a push
run, so no release secrets are placed and nothing is published, which is
the line grain/task-355 refused to move and this task did not either.

Skipping a job that would need release material it is not being given --
better than either failing it or refusing the whole run -- was weighed and
not built. Capabilities are read per repository rather than per job
(`ci.Request.Capabilities` is filled from repo settings), so there is
nothing in a plan to read that answer off without inferring it from a
job's steps.

**Why the judging run is a `push` and not a narrowed `release`.** Which
jobs a plan contains is decided by the event, and the two plans are not
the same set with one job removed. For a native grain workflow the
release event matches `on: release` and nothing else, so a repository
whose publishing answers `release` and whose tests answer `push` has a
release plan with no test job in it to narrow to -- which is true of this
repository, was briefly not between grain/task-355 and grain/task-461,
and is true again. Asking under `push` is what
puts the judging jobs in the plan at all, and for a repository whose
publishing lives in a release-triggered workflow it also leaves that
workflow out by construction. For an Actions-backed workflow triggered
`on: push` to `main`, where the publish job *is* in the push plan,
`BaseBranchCIJobs` is what leaves it out instead (`ci.workflowEvent` has
the event translation both of these turn on).

The option not taken here was letting a workflow declare a job as
publishing rather than judging, so that grain could subtract the publish
jobs from a release plan itself. It is attractive -- the declaration
lives next to the job, so it cannot rot the way a list in settings can --
but it puts the answer to "which of these jobs may grain run without
publishing this repository" in a file every run grain dispatches to that
repository can rewrite, which is the one kind of question
`docs/data-model.md` keeps on grain's side of the line. `BaseBranchCIJobs`
is somebody with access to grain's settings naming the jobs, once, in the
same place they turned publishing off.

## A gate that is off, said out loud (grain/task-453)

"The refusal is logged with its reason on the cycle it happens" was the
promise, and a log line turned out to be the whole of it. On 2026-09-19
at 07:22 UTC releases were switched off on
`bwsalmon/grain` -- almost certainly because `release.yml:images` had
been failing on every merge for days -- with no `BaseBranchCIJobs` named
beside them. The last run that judged `main` had started ten minutes
earlier. About a hundred merges landed over the next 33 hours, the merge
queue went on merging batch after batch onto a branch nothing had
tested, and `RequireBranchUpToDate` is off here, so this gate was the
only control catching two branches that are green apart and broken
together. It was found by a Director session reading `git log` on the
settings repository.

Nothing a person looks at said anything: not the repository pane, not
the inbox, not the health pane, not the merge queue. The condition was
`ReleaseWorkflowsDisabled && len(BaseBranchCIJobs) == 0` on a repository
grain was watching -- one comparison, true for a day and a half.

### That particular way in has been closed, and the blind spot has not

grain/task-458 and grain/task-461 removed the cause: the release switch
decides nothing about judging any more, empty `BaseBranchCIJobs` means
"the whole push plan" rather than "nothing", and the condition above is
no longer reachable. **So this reading does not look for it** -- a
warning drawn from `ReleaseWorkflowsDisabled` would now fire on a
perfectly healthy repository, which is how a surface stops being read.

What those tasks fixed is the one route anybody had found. What they do
not fix is that a branch can stop being judged and nothing says so, and
the 33 hours were a symptom of *that* rather than of the switch. So the
symptom is **reported** (`pkg/ui/basebranchgate.go`): every base branch
grain is watching is read each time the health response is assembled,
and one with something wrong is named:

- **nothing will ever judge it** -- grain CI not enabled for a
  repository whose default branch is watched, which is the only
  settings-shaped way left in (`orchestrator.seedWatchedBaseBranch`
  refuses to enrol, says so once with `sayOnce`, and never again). Drawn
  as an error rather than a warning, because it is a control that is off
  rather than a reading that is high;
- **a run that never concluded.** One run judges a branch at a time
  ("One run in flight per branch", below), so a row naming a run with no
  verdict is a branch nothing will start another run for -- and from
  every other surface it looks exactly like a branch being tested right
  now. This is the second half of the failure mode `BaseBranchCIJobs`'
  doc comment named: the stale row that can never be refreshed;
- **a merge nothing has judged.** grain starts a run on the cycle after
  a merge moves a watched branch, so a merge that has sat unjudged for
  hours is reported whatever the reason for the gap -- a wedged cycle, a
  paused pipeline, a daemon that is not running;
- **a run that judged nothing** (grain/task-508). The run started, the
  run finished, and it decided nothing about the tree: an `errored` row
  that grain has stopped re-asking (`baseCIRetryDue` gives up after
  `baseCIMaximumAttempts`). The commonest way in is a repository none of
  whose `.grain/workflows` answer `push`, whose base-branch run is made
  of no job at all and is over in seconds; the others are a deployment
  that cannot get a job onto a machine and a run that keeps being lost
  with its daemon. The finding carries what grain recorded the run as,
  because that is the difference between "this repository has no suite"
  and "this deployment has nowhere to run one" -- two findings of the
  same shape with completely different repairs.

Each finding says **when a base-branch run last started for that
branch**, which is the sentence that would have ended the incident on
its first hour and which was on the row the merge queue already reads
(`model.BaseBranchCI.StartedAt`) the whole time. That sentence is the
durable part of this: it does not depend on grain having correctly
predicted which switch would one day turn the gate off, which is exactly
what nobody managed. It appears on the deployment health pane above the
host figures, in what `read_deployment_health` hands an agent, and on
the repository's own release pane. A deployment whose gates are all
working renders none of it.

Two things it deliberately does not do. It changes no semantics and
starts nothing: every finding is read off state grain had already
written, and the repair in each case is an operator's. And it reports
nothing about a repository it has no record of -- a merge into a
repository with no grain-ci grant writes no row at all
(`orchestrator.recordPostMergeCI`), so if its default branch is not
watched either, there is nothing in the store to walk and no honest
finding to make. Watching the default branch is what makes that
repository's silence visible.

### An empty suite is not a pass on a base branch (grain/task-508)

One state defeated all of the above, and it is the state grain/task-508
went sweeping this deployment's repositories for: a gate that runs and
**passes** without running a test.

`ci.PlanRun` keeps one empty plan green on purpose. A repository that
declares no workflow at all has nothing red about it, and holding its
pull requests pending on a suite nobody wrote would make adding
`.grain/workflows` to one repository a breaking change for every other
(`pkg/forge/checks.go`, "A repository that declares no workflows"). That
rule is right for a task branch. On `model.BaseBranchCI` it was wrong:
the row is read as *the tree this queue is merging onto has been
tested*, by the merge queue, by the release pane, and by whoever goes
looking after an incident. A repository grain CI is enabled for, whose
default branch grain is watching, and which declares no
`.grain/workflows` therefore got a gate that passed over any tree
whatever, for ever, with a green tick on every surface and no finding
above to draw -- no failure, no repair, no stale row, no gap.

So `collectBaseBranchCI` records a concluded base-branch run that
contained **no job at all** as `errored` rather than as a success. Not
as a failure: a repository with no suite has done nothing wrong and must
not have its own queue held shut. `errored` is grain's word for a
verdict about nothing at all, which is exactly what this is -- it holds
no merge, it is re-asked a few times in case a suite appears, and it is
what the reading above reports on. This is the same distinction
`collectBaseBranchCI` already drew for a run grain itself cancelled.

The other half of what task 508 was sent to find is not detectable here
and is not pretended to be: `BaseBranchCIJobs` is free text, so a
repository that narrows its gate to a lint job has a run full of jobs,
all green, testing nothing. Nothing in grain can tell a test job from
any other by its name. What the reading can do is name the jobs the gate
is made of -- `RepoReleaseSettings.BaseBranchJobs` on the repository's
pane, and `orchestrator.baseCIJobsOf` in the line written as each run
starts -- and leave a person to read them.

### A run grain could not even ask for (grain/task-509)

The section above is about a run that starts and judges nothing. The
last way this gate goes quiet is a run that never starts, and it arrives
through the same setting.

`BaseBranchCIJobs` narrows the push plan by job id, and `selectPlanJobs`
rejects a selector matching nothing rather than guessing at it --
`ci: selected job "go" does not exist in the matching workflows`. That
refusal comes back from `CI.Start`, which is a *request* that failed
rather than a run that concluded, so until this there was no row to
record it on: `startBaseBranchCI` returned the error, the reconciler
logged it, the branch's row stood exactly as it was, and the next cycle
asked the same question and was refused the same way. For ever, with no
attempt counted, nothing on any surface, and the queue merging onto the
branch throughout. An operator pressing "run the base-branch check"
(grain/task-480) met the same wall from the other side: the request is
consumed only by a run being started, so the button did nothing, said
nothing, and could be pressed all day.

Nothing on the settings pane can catch this in advance -- whether a job
name matches is a property of a commit, and the pane has none -- which
is `SetRepoBaseBranchCIJobs`' own long-standing note. So the refusal is
recorded where the answer to it lives: `refuseBaseBranchCI` writes the
branch's row with the attribution a started run would have carried, no
run id, and `errored` as the conclusion, with the refusal itself as the
detail.

`errored` rather than `failure`, for this whole document's recurring
reason: nothing about the tree was asked, so nothing about it is wrong,
and a refusal that held the queue would be a gate blocking the only
route to fixing itself. What the word buys is the rest of the machinery
for free. `baseCIRetryDue` re-asks a bounded few times, so a refusal
that was a hiccup heals and one that is a setting stops costing a
request every cycle; the operator's request is consumed, so the button
answers; and `ui.baseGateJudgedNothing` reports it beside the very job
list that caused it, with the planner's own sentence as the diagnosis.
It is the same finding as the section above because it is the same fact
-- nothing judged this tip -- and which of the ways it was is what
`RunDetail` carries.

## Publishing is also a CI input (grain/task-397)

The section above pulls *judging* out of the release switch. What it
leaves behind is everything else the release run does that is not a
release in the sense the switch's name suggests -- and in this
repository there is one of those, and it is expensive.

`.grain/workflows/release.yml` publishes the grain image, the Kubernetes
sandbox image, and `guest:inputs-<key>`: the content-addressed sandbox
guest cache that every job resolving a guest reads
(`scripts/release-images.sh`, `scripts/granule/guest-inputs.sh`). The
first two are release artifacts. The third is a **build input for grain's
own CI**. With releases off, nothing fills it after a merge, the cache
stops following `main`, and each branch's `real KVM sandboxes` job runs
`konturctl guest build` inline instead: ~1GB pulled into a VM nested in a
CI sandbox, 50-65 minutes against a ten-minute cache hit, on the longest
job in the suite, on every branch in flight at once. On 2026-09-20 that
saturated the sandbox pool for a day and stacked seven pull requests in
`repair_needed` behind one repair.

### What it cost was not minutes (grain/task-464)

The paragraph above prices this in wall clock, and that is how it was
filed three times. It is the wrong unit. A cache miss cost a **verdict**,
and then it cost a **test**.

The inline build took 50-110 minutes inside a job bounded by
`ci.DefaultJobTimeout`, so the job was killed and recorded as
`ci.ConclusionFailure` -- run `b543c7b0a2b399d9`, `real KVM sandboxes`,
failure in 54m17s on the step "Resolve the sandbox guest", on a branch
that was fine. Everything downstream believed it: the merge queue filed
repairs against branches nobody had broken, and because the base-branch
gate reads the same verdicts a cold cache could hold every merge in the
repository. grain/task-427 then deleted `real KVM sandboxes` from
`.grain/workflows/tests.yml` to stop the noise -- a genuine test removed
from the suite because a cache had made it unreliable. That is the whole
cost, said plainly: a cache was allowed to decide what gets tested. (The
test is back, in grain/task-497, with its resolve step declared
`infrastructure: true`. That is the measure of the fix: the job returned
because a miss can no longer cost a verdict, not because the cache got
warmer.)

So the rule is not about this cache, and it is stated where CI
conclusions are decided rather than in the workflow that tripped over it:
**anything a cache miss causes is an infrastructure condition, not a
verdict about the tree.** A step may declare itself
`infrastructure: true` (`ci.Step.Infrastructure`) -- it fetches the
environment the job's other steps need rather than testing the tree --
and two things follow. Its time is not charged to the job's
`timeout-minutes`, so a cold cache no longer expires a bound it was never
being measured against; and if it fails anyway the job concludes
`ci.ConclusionErrored`, which holds the merge queue exactly as an
unfinished check does, is retried rather than blamed, does not make a
base branch `Broken()`, and files a repair against nobody. A miss costs
time again, which is all a cache was ever designed to cost.

The release switch is not the only way that cache could go cold, and the
other way is worth naming because the fix for it is a step order rather
than a setting. The release job gates its publish on `tests/container`,
a job stops at its first failing step, and a suite that goes red on a
merge is not rare -- so if the cache tag were pushed by the `push` phase
it would be built and thrown away on every red merge, and the branches
in flight would pay for it. It is not: `resolve_guest` pushes
`guest:inputs-<key>` (and `guest-base:<key>`) as soon as it has built
one, in the build phase, ahead of the gate, while the names a deployment
resolves keep waiting for the verdict. Those tags are safe by
construction -- each names the hash of its own inputs, and nothing
resolves one but a tree that derives the same key -- so a red suite
costs this repository an image and costs its branches nothing.
`tests/deploy`'s `TestARedReleaseGateDoesNotCostTheGuestCache` is what
keeps the two on their own sides of it.

Nothing said so. The switch is in the repository pane; the cost arrives
in the CI queue an hour later; no reading anywhere joined them.

**So grain reports the coupling** (`pkg/ui/sandbox_guest.go`):

- the repository pane says, next to the switch, that this repository's
  release workflow is also what publishes the sandbox guest this
  deployment runs, and names the one job that fills the cache without
  turning publishing back on --
  `grain ci run -job .grain/workflows/image.yml:guest-cache` on the base
  branch for one tree, or that job named in the post-merge dispatch list
  below to have it follow the branch (`RepoReleaseSettings.PublishesSandboxGuest`,
  and `CacheDispatched` on the reading below, which is what tells
  "releases are off" from "the cache is cold");
- `GET /api/sandboxes` -- the sandbox pane, and the same response
  `read_deployment_health` hands an agent -- carries
  `sandboxGuest`: which repository publishes the guest, whether it still
  is, the base branch grain watches for it and the tip it recorded, and
  whether the guest this deployment runs was built from that tip.

Which repository publishes the guest is read off the image reference
(`<registry>/<owner>/<name>/guest`) rather than configured, so a fork's
deployment is described by the fork's own switch and an image grain
cannot attribute to a repository is reported against no repository at
all.

`behind` there is not the same claim as "the cache is cold". Most merges
change none of the paths `guest-inputs.sh` names, so a guest built a
hundred commits ago is usually still the one `main`'s own key resolves
to; what makes a gap worth acting on is `publishing: false` beside it.

**What that change deliberately did not do** was change what grain
*does*: publishing the guest on merge independently of the release
switch is a use of a credential an operator scoped to releases, and that
was not a decision to take inside a change whose whole subject was
something happening without being said. It is the next section.

## The build input follows the branch (grain/task-409)

A repository names the jobs that must follow its base branch --
**`model.RepoConfig.PostMergeDispatchJobs`**, on the same pane as the
release switch -- and `reconcileBaseBranchDispatch` dispatches them
whenever that branch's tip moves: after a merge, after a hand-push, and
whatever the release switch is doing. Here that list is one job,
`.grain/workflows/image.yml:guest-cache`, so the guest cache follows
`main` with publishing switched off. What stops with releases is then
only what the switch is named for: the images, and the moving tags a
deployment resolves.

**Under `workflow_dispatch`, which is the gate honoured rather than
widened.** A job that fills a registry cache pushes to a registry, so it
needs `package-publish`, whose material is placed for a release or a
dispatch and never for a push (`packagepublish.SupportsCIEvent`). The
post-merge *push* run `baseBranchCIWanted` starts for a releases-off
repository therefore cannot carry it, and widening that gate is not the
answer -- it is what keeps a repository-writing credential off an
untrusted branch push. An operator naming a job in operator-owned
repository configuration *is* the operator asking for that dispatch,
which is exactly what the event already means, and what is asked for is
those named jobs rather than everything in a release plan.

**It cannot hold the queue, by construction.** The dispatch is recorded
in `model.BaseBranchDispatch`, a row of its own that the merge queue does
not read; `model.BaseBranchCI` is untouched. A dispatch that fails is a
cache that stayed cold -- a slow job on some later branch -- and is
logged and left. Nothing is retried for the commit it failed on: the
branch's next merge asks again, and a 25-minute build re-dispatched every
cycle would cost more than the cold cache.

**One at a time, and level-triggered.** The row is the lock, as
`BaseBranchCI`'s is (grain/task-334): a branch with a dispatch in flight
starts no other, merges landing meanwhile move the tip rather than
stacking runs, and intermediate commits are skipped -- the tree a later
branch derives its cache key from is the tip, not a commit grain merged
past. The condition is "the tip is not the commit this was dispatched
for, or the named jobs have changed", so turning the setting on takes
effect on the next cycle rather than on the next merge. One dispatch is
started per cycle across the deployment, for `seedWatchedBaseBranches`'
reason.

**What grain still does not infer.** Whether a release workflow also
publishes what a dispatched job publishes: it cannot tell, which is why
both of these lists are named by an operator rather than derived. A
repository with releases *on* that
names the same job here runs it twice per merge -- a `docker pull` on a
warm cache, the build done twice on a cold one. The list is the
operator's statement about their own workflows, and grain does what it
says.

## The one it does infer (grain/task-457)

The section above is the mechanism working because somebody asked for
it. The operator's own objection to that, on 20 Sep, is the reason this
section exists:

> I should not have to manually re-enable the cache; it should just
> continue to work.

Which is right. Everything above is an argument for why grain cannot
derive this *in general* -- and the case it was all built for is one
where it can. Grain cannot read a release workflow, but it can read the
image its own sandboxes are created from:
`<registry>/<owner>/<name>/guest` names the repository publishing the
guest this deployment runs, which is the attribution
`ui.SandboxGuestPublishing` has made since grain/task-397. For that one
repository, grain knows a piece of its release workflow is a build input
it depends on itself.

So `model.EffectivePostMergeDispatchJobs` adds
`.grain/workflows/image.yml:guest-cache` to whatever the repository
named, on three conditions:

- **it publishes the guest this deployment runs**, read off the image
  rather than configured, so a fork is described by its own settings and
  an unattributable image by nobody's;
- **its releases are off**, because with them on the release run fills
  the same cache and a second dispatch is the same ~1GB guest built
  twice -- the double-dispatch trap the section above warns about,
  avoided by not firing rather than by deduplicating;
- **its CI already holds `package-publish`**, so the credential the job
  needs is one an operator granted. Grain widens nothing: the dispatch
  is `workflow_dispatch` on a commit already merged to the base branch,
  exactly as before, and `packagepublish.SupportsCIEvent` is untouched.

All three are true of a repository the moment *before* its releases are
switched off. That is the whole claim: not that grain may now publish
something new, but that the half of a merge which was never publishing
-- a tag naming the hash of its own inputs, which nothing resolves
unless a commit has those inputs -- should not stop because the half
that was publishing did.

**Turning it off means meaning it.** Revoke `package-publish` and grain
stops, because the job could not push anyway; turn releases back on and
grain stops, because the release run has the work back. There is no
third switch, deliberately: the state it would describe -- a repository
whose CI may publish, whose releases are off, and which publishes the
guest this deployment is running on right now -- is one where a cold
cache buys nothing.

**The pane says so rather than asking.** `GET /api/repos/{repo}/release`
carries `sandboxGuestCacheAutomatic` from the same expression the
reconciler dispatches from, so the warning beside the release switch
became a statement, and cannot claim a cold cache grain is keeping warm.
What is left of the warning is the case grain cannot cover: a repository
publishing this deployment's guest whose CI holds no `package-publish`.
That one names the grant to make, not a field to fill in.

## Releases on: the judging jobs answer `release` (grain/task-355, reversed by 461)

This section records a rule that no longer holds and the reasoning that
got there, because the incident behind it is real and keeps being
rediscovered. Read "The release is not the gate" for what grain does now,
and "Should a failing publish hold the queue?" after it for the question
this section left open and what closed it.

"Publishing is not judging", above, settled which jobs judge a base
branch for a repository that had turned publishing **off**. The other
half of that switch -- a repository grain releases -- had an answer
nobody had stated, and this deployment was getting it wrong about itself.

A base-branch run was asked for under `release`, and which jobs a run
contains is decided by the event and by nothing else. For an Actions
workflow that is wider than it sounds: `ci.workflowEvent` lets a workflow
triggered on a push to `main` answer a release too, because from Actions'
perspective a post-merge release *is* that push, so an Actions repository
whose tests run `on: push` has them in its base-branch run already. A
**native grain workflow gets no such translation**: `release` matches
`on: release` and nothing else.

So on `bwsalmon/grain`, whose two workflows are both native, the whole of
the control this document describes was:

```
release -> .grain/workflows/release.yml:images      (build, test, publish an image)
push    -> .grain/workflows/tests.yml:{frontend, go, installer-e2e, lint,
                                       package-cache, real-vm, terraform, ui-e2e}
```

The gate that exists to catch two branches green apart and broken
together was asking "does the deployable image still build and publish",
and running no test at all. Worse than narrow: `release.yml:images` is
`cache: false` and failed on every merge for days (grain/task-230), so
the gate held every merge in the repository, spent an agent on a repair
task, and still said nothing about whether `main`'s tests passed.

**The rule grain/task-355 landed: the jobs that judge a branch answer the
event the judging run is asked for.** With releases on that event was
`release`, so a repository whose tests must judge its base branch said so
in its workflow's `on:` -- `.grain/workflows/tests.yml` answered
`release` beside `push`, and a base-branch run was its jobs beside
`release.yml:images`.

**That rule was reversed by grain/task-461,** and the reversal is below.
It is kept here rather than deleted because it was argued carefully, it
is still written into a workflow file somewhere in every deployment that
followed it, and the alternatives it weighed are the alternatives
anybody re-opening this will weigh again.

Three were recorded, because each is the obvious one from a different
direction:

- **Two runs per base-branch commit** -- the release run publishing as
  before and a second run under `push` judging. It is the shape that
  needs no repository to declare anything, and it was judged the most
  machinery: `model.BaseBranchCI` holds one `RunID`, one commit, one
  conclusion and one confirmation, so a second *verdict* means a second
  half of that row, a rule for combining two answers into "is this branch
  broken", and a second run competing for the sandbox budget under a
  control whose one-run-at-a-time bound (grain/task-334) exists because
  two were too many. **This is what was built in the end**, and the
  objection dissolved once the second run stopped being a verdict: there
  is nothing to combine, because only one of the two runs is an answer to
  "is this branch broken" at all. See below.
- **Reading `BaseBranchCIJobs` with releases on**, deriving the run's
  event from where the named jobs live. Cheap, and it keeps one run -- but
  a run has one event and the two events' plans here are disjoint, so a
  repository that named its test jobs would stop publishing unless it
  named the publish job too, and naming both is not expressible in one
  plan at all for native workflows.
- **Widening the release plan in `ci.workflowEvent`**, so that a native
  `on: push` workflow answers a release the way an Actions one does. One
  line, no declaration, every repository fixed at once -- and it hands
  every job of the push plan the release secrets and the publishing
  credentials, which `docs/ci-capabilities.md` promises "pull-request and
  ordinary push runs cannot obtain". Giving those to a job because grain
  decided its workflow should answer another event is a boundary grain
  should not move on a repository's behalf. That objection still stands
  and nothing below moves that boundary either.

### The release is not the gate (grain/task-461)

An operator ruled on it, and the ruling is the shortest statement of the
rule in this document:

> Release should not be the gate. That is the CI's job. If we wanted
> release to be the gate then we would do it as part of the ci jobs.

What grain/task-355 fixed was *which jobs* the judging run contained. It
did not touch *which run was the verdict*, and that was the more
expensive half. `model.BaseBranchCI.Broken()` is what the merge queue
reads before it merges anything, and the run written to that row was the
trusted release run -- so "did the image publish" held every merge in the
repository exactly as "do the tests pass" did. On this deployment the
publisher is flaky (a `tests/container` health-check timeout,
grain/task-456) and, in the operator's own summary, releases "have
literally never worked from grain in the recent past", so the gate was
red for reasons that had nothing to do with the code being merged. It is
also why the queue kept filing repair tasks against branches that were
never broken.

So the judging run is now **always** a `push` of the repository's own
push plan (`orchestrator.baseBranchCIWanted`, which no longer has an
event to return), and the trusted release a merge still emits is a second
run at the same tip, started by
`orchestrator.reconcileBaseBranchRelease`. That run is recorded on
`model.BaseBranchRelease`, a table the merge queue has never heard of --
the same structural argument `model.BaseBranchDispatch` already makes for
post-merge cache warms, and the reason the release is not folded into
*that* row is that `PostMergeDispatchJobs` is deliberately not
deduplicated against the release run, so one row per branch cannot hold
both.

What follows from it:

- **`.grain/workflows/tests.yml` stopped answering `release`.** Under
  this rule the tests are the push run; answering both meant the whole
  suite ran twice per merge, the second time inside a trusted run
  carrying release secrets -- a cost grain/task-355 flagged itself.
  `pkg/ci/selfcheck_test.go` still pins the general property against the
  checkout ("a judging run's event has to plan the jobs that judge a
  commit"), restated against `push`, and gained a test that no job is in
  both plans.
- **The secrets boundary did not move.** The judging run is a push run,
  so no release secrets are placed and nothing is published; the release
  run keeps its secrets because it is still the release. This is the line
  grain/task-355 refused to move and grain/task-458 did not move either.
- **Two runs per merged tip, not two verdicts.** They are separate rows,
  so neither reconciler's one-in-flight bound sees the other's run;
  separate events, so neither plan holds the other's jobs;
  `collectBaseBranchCI` reads `BaseBranchCI.RunID`, so it only ever
  collects the judging one. What they share is the sandbox budget, so one
  release is started per cycle across the whole deployment.
- **A failed release is recorded and nothing else happens.** No hold, no
  repair, no retry of the same commit; the branch's next merge asks
  again. Noticing one that keeps failing is a *reading* built on top of
  that row (grain/task-511, `pkg/ui/basebranchrelease.go`): a branch
  whose releases have published nothing for a run of merges or for a day
  is reported on the deployment health response and on the repository's
  own release pane, and nothing about it is a gate. The operator's
  ruling was explicit about the priority: *"The system is broken. Not
  noticing a failed release is a 3rd order problem."* -- which is the
  size it was built to.

One thing this still does not do:

- **Whether other repositories are in the same position is unread.**
  `gke-labs/kontur`'s workflows decide what its base-branch runs have
  been running. Nothing in this repository can see another repository's
  tree, and a repository that added a `release:` trigger to its test
  workflow under the old rule now pays for its suite twice per merge
  until somebody takes it off.

### Should a failing publish hold the queue? (grain/task-424)

**No, and nothing does.** The question was left open by grain/task-355
and filed as a task of its own; by the time it was picked up both halves
of it had been answered from other directions, and the answer is worth
stating here because the reasoning is not obvious from either change
alone.

The question as filed: `.grain/workflows/release.yml:images` going red
stopped the queue merging into `main`, earned a confirmation run and a
repair task, exactly as a failing `go` did. That is what made
grain/task-230 expensive -- the publish was broken for days, the whole
repository's queue was held on it, and nothing was being said about the
tree at all. A broken publish is not a statement about the tree: "a base
branch that may be broken is not one to merge onto" does not apply to a
job whose failure means *this commit did not reach the registry*.

Two things had to be true for a broken publish to cost a repository its
queue, and neither is now:

1. **The publish had to be in the judging run.** Since grain/task-461 it
   is a run of its own at the same tip, recorded on
   `model.BaseBranchRelease` -- a table the merge queue has never heard
   of. The judging run is a `push` of the repository's own push plan and
   contains no publishing job on a native workflow, because the two
   events' plans are disjoint.
2. **The judging run had to hold.** Since grain/task-471 it does not,
   for any job. A red base branch is information: it tells "this batch
   broke `main`" from "`main` was already broken", and it is what a
   repair is filed from.

So a failing publish holds nothing because a failing *anything* holds
nothing, and it would not be in the run that fails even if something did.

#### Why none of the three shapes was needed

What made this a task rather than a line in grain/task-355 is that grain
cannot tell a publishing job from a judging one by looking, and both
places that wanted to (this document's "The option not taken",
`model.RepoConfig.BaseBranchCIJobs`) refused to read that distinction out
of a workflow file, because a workflow file is rewritable by every run
grain dispatches to that repository. Three shapes were weighed against
that:

- **A per-repository list in `RepoConfig` of jobs whose failure earns a
  repair but does not hold the queue.** Settings-side, so it answers the
  objection -- and there is nothing left for it to exempt anything from.
  The half that survives the hold's removal is "should this job's failure
  spend an agent", and there the objection in "The option not taken"
  below still stands unchanged: a standing decision to ignore a job's
  verdict, made once and then forgotten, in a list exactly as stale as
  whoever last edited it. `UnfixableChecks` and `IgnoreGitHubCI` each say
  in their own doc comments why they are not that list.
- **Deriving it from the capabilities a job's run is given** -- a job in
  a run holding `package-publish` is a publisher. This was right about
  the grain of the thing and wrong about the unit. Grain reads
  capabilities per repository rather than per job
  (`ci.Request.Capabilities` is filled from repo settings), so there is
  nothing in a plan to read the answer off a single job without inferring
  it from that job's steps. But *run-wide* is not the defect it looks
  like: the trusted release run is exactly the run that holds publishing
  material, and separating publishing from judging by run is what
  grain/task-461 did. The distinction grain refused to read out of a
  workflow file is drawn by the event instead, and the event is grain's
  to choose rather than the repository's to declare -- which is the whole
  answer to the objection that made this a task.
- **Leaving it, on the grounds that a `main` that cannot ship is a `main`
  worth stopping for.** Rejected, by an operator and on the general form:
  a broken release already means new changes cannot be shipped, so if the
  same failure also holds the queue, the fix for the release cannot land
  either and the deployment is deadlocked with no way out but a human
  overriding a gate by hand. The general rule is worth carrying past this
  instance, because the next one will not be called "release": **any gate
  whose own failure blocks the only route to fixing that gate is the same
  bug.**

#### The half of the premise that did not survive either

grain/task-424 was filed assuming a broken publish "plainly deserves its
repair task" and that only the hold was in doubt. It gets no repair
either, deliberately: a failed release is recorded on
`model.BaseBranchRelease`, logged, and left -- no hold, no repair, no
retry of the same commit, because the branch's next merge asks again and
re-publishing a failing 25-minute image build every cycle would eat a
deployment's whole CI capacity. The operator was explicit about the
priority:

> "Not noticing a failed release is a 3rd order problem. Our releases
> have literally never worked from grain in the recent past so gating the
> whole build on it is ludicrous."

Noticing a release that keeps failing was therefore the open question
this section left behind -- a smaller one than the one it closes, since
the failure mode is a repository that quietly stops shipping rather than
one that cannot merge. **It is built, as a reading** (grain/task-511,
`pkg/ui/basebranchrelease.go`), and what it is *not* is the whole of
what makes it safe:

- It reads `model.BaseBranchRelease` and writes nothing. The repair is
  an operator's, as it is for every finding `pkg/ui/basebranchgate.go`
  makes next door, and no repair task is filed for the reason above.
- It cannot hold the merge queue, because the table it reads is one the
  queue has never heard of. That property is structural rather than
  remembered, and `pkg/orchestrator/basebranchrelease_test.go` pins it.
- It does not fire on one red release. A finding needs three in a row,
  or a branch that has published nothing for a day -- one failure is
  noise on a publisher as flaky as this deployment's (grain/task-456),
  and a surface that cries on every one is a surface that gets ignored.
  A repository whose releases an operator has switched off reports
  nothing at all: it publishes nothing on purpose.

The one thing it needed from the write path is two counters on the row
(`model.BaseBranchRelease.Failures`, `FailingSince`), carried across the
replace that gives each release a fresh row. Without them the only
history there is is the newest attempt, so on a repository that merges
often "the last release failed" flickers and "this repository has
shipped nothing since Tuesday" -- the sentence actually worth telling
somebody -- is unsayable.

Sections above used to name grain/task-453 as the task for this. It
never was: that task landed on 2026-09-21 as the reading for a base
branch nothing is *judging* (`pkg/ui/basebranchgate.go`, "A gate that is
off, said out loud") -- adjacent, argued from the same incident, and it
touches `model.BaseBranchRelease` nowhere.

#### What to check first if this is reopened

Both halves, separately, because they are separate mechanisms and either
could come back on its own. Does the judging run contain a publishing job
(`orchestrator.baseBranchCIWanted`, and `pkg/ci/selfcheck_test.go`'s
"no job is in both plans")? And does anything read
`model.BaseBranchCI.Broken` on the way to a merge
(`orchestrator/mergepredicate.go`)? If both answers are no, a broken
publish costs a repository its images and nothing else.

## A repository grain has never merged into (grain/task-331)

The section above watches the branches there are rows for. A merge grain
performed was still the only thing that ever *created* one, so the whole
control skipped a repository whose pull requests are all merged by hand,
and any repository grain had not merged into yet: no row, nothing
watching, and a `main` that could be red with the queue merging onto it
exactly as before any of this existed.

**`model.RepoConfig.WatchDefaultBranch` writes the row from the
repository instead of from a merge.** Set on a repository,
`reconcileBaseBranchCI` resolves that repository's default branch
(`github.Client.DefaultBranch`) and enrols it
(`seedWatchedBaseBranches`), writing exactly the runless row a first
merge writes (`noteBaseBranchMerge`, the next section). Starting the run
is the reconciler's, as it is for every other branch. After that the
branch is an ordinary tracked row with everything above and below
applying to it: the tip is re-read every cycle, one run is in flight at a
time, a failure holds the queue, and a second run decides whether it
earns a repair.

Three decisions worth stating, because each of them is a cost somebody
pays:

1. **It is off until a repository is ticked, rather than on for every
   repository grain CI is enabled for.** Turning it on starts a full
   suite on that branch, and a branch that turns out to be red at once
   holds that repository's merge queue and files a repair task. Doing
   that for every repository with a config row would land N suites and N
   tasks on the strength of a deployment upgrade rather than of anybody's
   decision -- and this deployment's CI host has been over its sandbox
   budget twice in a week (grain/task-173, 232, 234). The set of
   repositories with a `repo_config` row is also not the set whose `main`
   anybody wants suites spent on: a row exists wherever somebody set a
   prompt extension or a clone URL. So the repositories are named, one
   tick at a time, by whoever is paying for the CI. It is a
   `repo_config` field, so it can be ticked on the repository pane or
   arrive as a pull request against the state repository -- both write
   the same row, and the reconciler reads it either way.
2. **One repository is enrolled per cycle, and none at all while any base
   branch grain watches has a run in flight anywhere**
   (`baseBranchCIBusy`). An operator who ticks six repositories therefore
   pays for them one suite after another rather than six at once. The
   per-branch bound in the next section does not cover this: six
   different branches judged once each are six release runs it is
   perfectly happy with, which is precisely the bill worth spreading.
   What it costs is latency -- six repositories are watched after six
   suites rather than after one cycle -- and nothing merges onto an
   unwatched branch any less safely in the meantime than it did before
   this existed, which is why latency is the right thing to trade. It is
   not an admission controller: `pkg/ci` has one, and it is the thing
   that knows what the host can take. This is a rule about how fast grain
   takes on *new* standing obligations of its own.
3. **The tick is not the thing that keeps a branch watched.** Once a row
   exists the row itself is the opt-in, exactly as it is for a row a
   merge created, so unticking stops grain enrolling further branches and
   leaves one it is already watching alone. What stops a repository's
   base branches being judged at all is taking the `grain-ci` capability
   off it, and only that: `ReleaseWorkflowsDisabled` changes the event
   the branch is judged under and nothing else ("Publishing is not
   judging", above) -- `baseBranchCIWanted`
   is the one place all of that is asked, and it now counts
   `grainci.RequiredFor` as grain CI being on, so the deployment's own
   state repository is judged the way that capability already says it
   must be.

A repository ticked for watching that grain CI is *not* enabled for is a
setting that would quietly do nothing, which is the silence
grain/task-320 was filed about. It says so in the log -- once per daemon
rather than once per poll, since it is a thing an operator fixes by
editing a row.

## One run in flight per branch (grain/task-334)

A base-branch run was a *release* run when this was written: on
`bwsalmon/grain` it built and published this deployment's own images, and
it took **22 minutes to 1h31m**. (Since grain/task-461 the judging run is
a `push` and the publishing is a second run beside it; the bound below
applies to each of them separately, on each one's own row, and the
arithmetic that motivated it is unchanged -- a full suite and an image
build are both long, and merges land faster than either.) Merges land far
faster than that. So while every merge started
one of its own, they stacked -- two release runs in flight at once was an
ordinary sight (`423d0f5` at 9m57s beside `99650c2` at 4m25s, both still
on *Build the images*) -- and several full image builds competing for the
sandbox budget are what made the rest of CI slow and what made the
release's own pre-publish gate flake. The most recent completed release
failure before this change built all three images and then lost one test
of twenty-two to `signal: killed` on a `schema-version` call that should
take a second, beside two genuinely heavy tests that passed at 106s and
41s. That is load.

The rule now is the one an operator would state: **at most one release run
in flight per (repository, base branch); when it finishes, if the tip is
not the commit that was last judged, start one against the tip as it
stands at that moment.**

Almost all of that already existed. `reconcileBaseBranchCI` has been
level-triggered on exactly the right condition since grain/task-320 -- "is
the recorded verdict about the commit the branch is *on*" -- so the change
is that it is now the **only** thing that starts a run:

- the merge path records rather than starts. `recordPostMergeCI` writes
  the branch's row (creating it for a branch grain has not watched before)
  and leaves the merging task in `BaseBranchCI.PendingTaskID`, which the
  next run started there takes up as its `MergedTaskID` -- so a failure
  still names a merge, and a run for a commit nobody's merge produced
  still names nobody;
- the reconciler starts nothing for a branch whose row names a run with no
  conclusion yet (`baseCIInFlight`). The row is the lock; no other state
  is needed;
- when that run concludes, the next cycle compares the tip against it and
  starts one, at the tip. Merges that landed in between are **coalesced,
  not queued**: only the newest image is worth building.

A run in flight is *waited for*, not superseded. Cancelling and restarting
on every merge would be the other way to hold the count at one, and on a
branch that merges faster than it builds it never finishes a release at
all.

### Why coalescing does not gate fewer merges

This is the one place where "publish fewer images" could quietly become
"check fewer merges". It does not, and the reasoning is worth writing
down because the two are the same run: the base-branch verdict *is* the
release run, and skipping a commit's run is skipping a verdict. (For a
repository whose releases are off they are not the same run -- the
verdict is a push run that publishes nothing, "Publishing is not
judging" above -- and everything below is about the coalescing rather
than about the event, so it holds either way.)

Whose verdict? Nobody's. `model.BaseBranchCI` is one row per (repository,
branch) and has only ever held one answer, about the branch rather than
about a commit -- each merge's run replaced the last one's as it started.
grain/task-320 settled the rest: a verdict about a commit the branch has
moved past is an answer about a tree that has gone, which is why such a
row is re-run rather than believed. A coalesced commit is exactly that
case. Its run's verdict would have been discarded on the cycle it arrived,
because by then the branch was already past it.

What does happen is that the tip's run judges a tree **containing** the
skipped merges, so a pair of changes green alone and broken together is
caught by it -- which is the whole of what this control buys. What is lost
is the ability to say which of several coalesced merges broke the branch,
and that was never certain anyway: the repair body has always named the
last merge as "the first place to look, though not necessarily the whole
cause".

### Not a retry loop

A level-triggered trigger has one failure mode worth naming: if the rule
were only "the tip is unjudged, so start one", a run that *fails* would
leave the tip unjudged and the next cycle would start another, burning
22-90 minutes an attempt forever.

It does not, because a failing run's row names the tip it failed at, so
the branch is judged and nothing restarts until a new commit arrives.
`BaseBranchCI.Attempts` and `baseCIRetryDue` already drew the distinction
this needs, and it is preserved exactly:

- **failure** -- a real verdict about that tree. Not re-run. It waits for
  a new commit, and a new commit gets its own run as any movement of the
  branch does.
- **errored, or lost with the daemon** -- no verdict at all. Re-asked,
  bounded by `Attempts` and spaced by `ciRetryDelay`.

That second case is also what makes the restart problem self-healing,
without a drain mechanism of its own. In the seven days to 2026-09-19 the
`release images` job on `bwsalmon/grain` ran 94 times: 8 success, 60
failure, 25 cancelled -- and **20 of the 25 cancellations were grain
restarting for an upgrade and destroying the run that would have built its
own next image**. Because the trigger is the state of the branch rather
than the event of a merge, such a run's row concludes as errored, the tip
is still a commit nothing has judged, and the cycle after the daemon comes
back starts a fresh one. Before this, a killed run was simply lost until
some later merge happened to start another.

## What counts as red (grain/task-202)

The control above is two actions, and they are not the same price.
Saying a branch is red is cheap and undone by one write. Filing a repair
spends an agent run and puts a task in front of a person. A deployment
with a flaky job -- this one has `ui e2e`, grain/task-182 -- has reds
that are neither a broken branch nor anybody's to fix, and as first
written every one of them cost both.

(The first action was a *hold* on the merge queue when this was written,
and the prices below were argued in those terms. It is not one since
grain/task-471 -- "What was removed: the base-branch health gate" above
-- so what is being weighed here is now "does this failure deserve to be
said out loud" against "does it deserve an agent". The argument survives
the substitution, which is why it is restated rather than deleted;
grain/task-424 made the substitution.)

**The obvious mitigation does not work.** A grain CI job's verdict is
content-addressed per commit (`ci.Fingerprint`), so re-running the same
commit replays the cached failure in seconds and agrees with itself
every time. Sampling a job independently takes a run that is told not to
read the cache -- `ci.Request.Recheck`, added for this. It reads nothing
from the cache. It did not write to it either at first, on the grounds
that a second sample is no more authoritative than the first. In practice
that left a flake's cached red as the answer every later reader got, even
after the recheck passed, until the entry aged out or the tree changed
(grain/task-1512). So since grain/task-1571 a recheck writes its result
back, and the newest verdict for a key is the one the gate reads. Each
rerun job replaces only its own key: since grain/task-2124 a recheck reruns
the jobs it names (by default an agent's `retrigger_ci` names the ones that
failed), reuses its dependencies' cached passes, and leaves every other
job's verdict as it was -- so the suite's verdict is every job's latest
result combined (data-model.md, "Flaky failures are rechecked before they
are charged").

So the two actions are now decided separately:

1. **What the rows say changes on the first failure**, immediately. A
   base branch that may be broken is worth saying so about, and being
   wrong costs a sentence that is retracted a few minutes later. (Until
   grain/task-471 this action was the hold, and being wrong cost minutes
   of queue latency instead.)
2. **The repair waits for a second opinion.** The reconciler starts a
   recheck of the jobs that did not pass -- not the suite, and not the
   ones that were green -- on the same commit
   (`orchestrator.startBaseCIConfirmation`). If it fails too the branch
   is confirmed red and the repair is filed as before. If it passes,
   nothing is filed, and the row and the log say the failure did not
   reproduce.

While that is in flight every queued change's row says so, in the words
"grain is re-running the failing jobs to see whether that reproduces",
rather than naming a repair that does not exist yet.

Clearing on one passing re-run is a judgement rather than a proof: a
genuine break the recheck happens not to hit is let through. It is the
right judgement here because the very next merge starts another
post-merge run, so a real break is caught again within one suite -- while
a flake is not.

A confirmation that never produces a sample at all -- CI itself is what
is broken, no sandbox, a job renamed out of the workflow -- is bounded at
two attempts and then **leaves the failure standing**. That is the one
place this design has to choose a direction. A repair on a branch that
turns out to be fine is a task somebody reads and closes; merging onto a
base nobody has tested since it went red is the thing this whole document
exists to prevent.

### The option not taken

A **per-repository list of jobs whose failure is not acted on** was the
other candidate, and is cheaper: no second run at all, and a job
everybody knows is flaky is reported rather than spending an agent. It is
not taken because it is a standing decision to ignore a job's verdict,
made once and then forgotten -- a real regression in a listed job reaches
the base branch unremarked, and the list is exactly as stale as whoever
last edited it. The confirmation costs one partial suite, only when a
branch is red, and it judges *this* failure of a flaky job rather than
all of them. If a repository ever does want the list as well, it belongs
in `RepoConfig` beside `RequireBranchUpToDate` and is a `grain/config`
pull request, not a constant in this package.

(`RepoConfig.UnfixableChecks` is not that list, and the next section says
why: it changes only what grain spends trying to clear a red, and not
what is done about one that stands.)

This is the objection grain/task-424 was sent to re-weigh for one
particular kind of job, the publish. It found the question dissolved
rather than answered -- see "Should a failing publish hold the queue?"
above -- and the objection itself unmoved: the reason no exemption list
was needed is that publishing and judging are separated by *run*, which
is a line the event draws and grain chooses, rather than by a list of job
names somebody has to keep true.

## Red a branch cannot reach (grain/task-291)

`RepoConfig.UnfixableChecks` names the checks in one repository that
**will never pass for a pull request grain's agents author**, whatever is
pushed to the branch. A CLA bot is the case it was written for:
restoring an account's Actions budget clears every check that failed
because no job could start, and leaves `cla/google` red on every
agent-authored pull request in the repository, permanently, because what
it asserts is about who signed an agreement rather than about the tree.

It reads like the list the section above declines, and it is the
opposite of it in the one way that matters: **it does not exempt the
check.** A pull request with one of these red is still not merged --
`forge.HealthFrom` reads it exactly as it always did, the queue waits,
and a person clears it or nothing merges. What the declaration changes is
only what grain *spends* on the way: a repair run dispatched at a check
that cannot pass is a run that could not have succeeded, and the base
merge before it is a write made to reach a green that is not reachable.
So the objection to the ignore list -- a regression in a listed job
reaching the base branch unremarked, and a list as stale as whoever last
edited it -- does not transfer. A stale entry here costs a repair that
would have been spent anyway on a check that had started passing again,
and the queue's own next cycle reads that green and merges.

The observed half of the same question is `repoFinder`: whether anything
in this repository gets a job started at all
(`docs/repository-checks-never-started.md`). The two compose into one
`forge.Unfixable`, an operator's standing declaration beside grain's own
reading of what the repository has been running lately, and either alone
is enough to make a repair pointless.

What it drives is `forge.Reds`, which splits a broken head's failing
checks in two:

- **Inherited** decides whether there is a path to green at all. While
  one of these is red the pull request does not merge however green
  everything else goes, so the base merge and the repair -- both of which
  exist to *reach* green -- cannot land it. Neither is made. The row says
  `checks_unfixable`, the escalation names the checks and grain's
  evidence, and where the finding is the repository's it says so: every
  entry there clears at once when somebody changes the setting.
- **Own** decides whether an agent has anything to do, and it is not
  outranked by the other half. A head with red of both kinds gets its
  repair, with the two halves named in the comment and the ordinary
  "everything failing on it is in scope" sentence replaced by one that is
  true (`repairScopeSentence`). `grain CI` is always own: it judges one
  exact commit on the branch, so it cannot be declared unfixable and
  nothing tries.

### Naming one (grain/task-304)

The list is on the repository's own pane, under **Merge queue** beside the
two switches (`PUT /api/repos/{owner}/{name}/unfixable-checks`); from a
shell, as

```
grain repo unfixable-checks acme/widgets            # what is declared
grain repo unfixable-checks -set cla/google acme/widgets
grain repo unfixable-checks -set "" acme/widgets    # withdraw them all
```

which reads and replaces the same column through the same route; or as
the same `repo_config` row set by a `grain/config` pull request. The CLI
is here rather than left to the pane because this is asked in a terminal,
by somebody who has just read a queue comment saying grain spent a repair
run on `cla/google`. `-set` replaces the whole list rather than adding to
it, and given empty it is still a write -- the only way to withdraw the
last declaration. Nothing is needed for the ordinary behaviour: no
repository declares anything by default, and every check one reports is
treated as a check some commit could turn green.

A name is the check's exact name as the pull request reports it, matched
without case and never by part of a name (`forge.Unfixable.Covers`) -- the
mistake that package made once was reading a check's name for a phrase and
letting a substring take a repository's whole gate away. Two names are
refused rather than stored, because either would be a row that looks like
a setting and does nothing: `grain CI` itself, for the reason above, with
the pane saying that reason rather than accepting it; and a name with a
comma in it, which the column's comma-joined storage would read back as
two names no check is called. Both refusals are the route's
(`ui.SetRepoUnfixableChecks`), so the pane and the CLI report them rather
than each deciding for itself what is allowed -- and the name grain's own
CI goes by comes back on the response (`RepoDefaults.GrainCICheckName`)
rather than being written into either of them.

Nothing checks a name against the checks the repository actually reports,
and nothing honestly could -- the case this exists for is a repository
whose provider is dead and reports no names at all. A name nothing
reports never matches, and costs a line on the pane and no behaviour. What
the list beside the two ticks has to say in words is the thing a list of
check names most invites being misread as: **it is not an exemption.**
Everything named there holds the merge exactly as hard as before.

## A provider that is not a source of verdicts (grain/task-270)

`RepoConfig.IgnoreGitHubCI` -- and `Config.IgnoreGitHubCI` for a whole
deployment in the same state -- says that **GitHub's check runs are not a
source of verdicts for this repository at all**. grain CI
(`.grain/workflows`) is its gate. With it set, `forge.Observe` does not
read GitHub's checks, the merge queue does not count them in a pull
request's health and does not wait on them, neither of the clocks this
deployment runs on CI can start on them, and `pull_request_status`,
`base_branch_status` and the merge queue's own repair comment say so
instead of listing reds an agent then has to explain away.

**This is not the option rejected above, and reading the two as the same
decision taken twice in opposite directions would be a mistake.** The
rejected one is a list of *jobs* whose failure does not hold the queue: a
job that runs, produces verdicts, and is exempted from them anyway. Its
whole problem is that a genuine regression in a listed job reaches `main`
unremarked, and that the list is exactly as stale as whoever last edited
it -- a standing decision to ignore an answer somebody is still giving.

This setting makes no exemption, because there is no answer to exempt. It
is a statement about how the deployment is *configured* -- which CI
provider judges this repository -- and it is either true or false at a
glance, not a judgement that rots. Nothing is hidden by it, because
nothing is reported: the reads are not made. And it cannot silently let a
regression through, because it does not remove a gate, it swaps one (see
below). The staleness failure mode does exist in its own form -- a
repository left on this setting after Actions comes back reads GitHub's
checks not at all rather than reading them green -- but it is a single
switch on the repository pane rather than a list, it is visible in words
wherever the queue writes about the repository, and turning it off
restores the old behaviour exactly.

### What made it worth having

GitHub Actions on `bwsalmon/grain` was blocked at the account level from
17 September 2026: every workflow run, on `main` as much as on task
branches, rejected before its first step with *"The job was not started
because an Actions budget is preventing further use"*, and no commit
pushed anywhere could change it. grain kept reading, waiting on and
reporting those checks for days, which cost four separate things:

- **every agent spent part of its closing note on it**, re-deriving the
  same unchanged fact ("red for the account-level Actions budget,
  identically on `main`, inherited, an operator's job to restore");
- **the merge queue instructed agents to fix it**, in so many words --
  its repair comment's "a check already red on the base still stops this
  pull request merging, so fix it here rather than reporting it as
  somebody else's", which under an account-level block is a false
  instruction that tasks 116, 188, 191 and 202 all had to reason their
  way past;
- **`base_branch_status` stopped discriminating.** Its whole purpose is
  telling an inherited red from one your branch caused, and with the
  provider dead it answered "all red on both sides" every time -- a call
  spent settling nothing;
- **it stalled the queue.** grain/task-149's entry was given up on after
  "no check reported against it at all for more than 2h", the stall clock
  running on a provider that was never going to report.

### The decision it forced: a repository with no grain CI

A repository can turn this on without holding `grain-ci`, and it must not
become a way to merge with no verdict at all. The answer taken is neither
"refuse to set it" nor "accept it and merge ungated":

**It is accepted, and it swaps the gate rather than removing it.** The
declaration makes grain CI *required* -- by the repository's declaration
rather than by the task's grant (`orchestrator.grainCIVerdictOf`,
`forge.Observe`) -- so a commit with no grain CI verdict reads PENDING,
not clean, and reaches the ordinary stall deadline and a person. A
repository that turns this on without grain CI therefore **merges
nothing, not everything**, which fails in the safe direction and fails
visibly.

Refusing to set it was the alternative and was not taken, because the
condition it would refuse on cannot be computed honestly: whether grain
CI judges a given pull request is a property of the *task* (does it hold
`grain-ci`), not of the repository, so "this repository has grain CI
enabled" is a guess dressed as a validation. It would also be the wrong
shape for the situation this exists for -- an operator whose CI provider
has just died needs the switch before the replacement is finished being
wired up, not after.

### The half of the repair comment that was missed (grain/task-454)

The setting reached the repair comment's *scope sentence* -- "GitHub's own
check runs are not read here, so a red one on this branch is not what is
holding the merge and is not yours to chase" -- and not the **evidence
section beneath it**. So for three weeks a bounce on `bwsalmon/grain`
opened with that sentence, gave grain CI's actual verdict one line, and
then spent its longest section, headed *"What CI says"*, quoting two
GitHub Actions runs in full, each ending in *"**Act on that sentence
rather than on the list above.**"* The message contradicted itself, and
the emphatic imperative was on the half it had just said to ignore --
addressed at an exhausted billing account no commit reaches, in the one
run that is the last automatic attempt a pull request gets.

The fix is a route rather than a deletion, on the same
`RepoConfig.IgnoreGitHubCI` the scope sentence already reads: where the
gate is grain CI the queue makes no Actions read for the comment at all
(`orchestrator.advanceMergeQueueHead`) and composes no section from one
(`requeueForRepair`), and everywhere else the section is exactly what it
was -- the explainer for a run concluded before a job of it started
included, which is the right thing to hand a repair in a repository that
does read GitHub's checks.

What the "accept it" branch owed in exchange was saying so out loud, and
it is said in three places rather than one: the repository pane says in
words what turning it on means for a repository without grain CI,
`pull_request_status` and `base_branch_status` tell any run dispatched
into that state that nothing will report a verdict for its commit and
that the queue will wait until it asks a person, and the queue's own row
says what it is waiting for. The state is a dead end, and it is a loud
one.

## Two decisions made on purpose

### May a branch be arbitrarily stale?

**Yes, for now: there is no staleness bound, and none is configurable.**

"Merges cleanly" says nothing about how old the tree underneath is, and a
branch green against a two-month-old `main` is a weak verdict. The
argument for a bound is real. It is not taken because:

- every form of the bound costs the thing this change is for. Expressed
  in commits it needs a compare call per entry per cycle, and whatever it
  decides, the *action* on a branch over the bound is to bring it up to
  date -- which is the N² suite bill again, merely means-tested.
- the bound is a guess at the same risk the base-branch gate now measures
  directly. A stale branch that breaks `main` stops the queue within one
  suite and earns a repair; a stale branch that does not break `main` was
  not a problem.
- branches here are hours old, not months. The failure mode a bound
  addresses is a branch left open across a refactor, and that branch
  almost always conflicts -- which already sends it to an agent.

If it is wanted later it belongs in `RepoConfig` beside
`RequireBranchUpToDate`, expressed in commits or in time, never as a
constant in this package.

### What about a branch mid-update when this lands?

**Nothing is left half-merged, and nothing needed a migration.**

The change removes no state and rewrites no row. A branch the queue had
already merged its base into keeps that merge commit and its
`MergeQueueRefreshedAt` stamp; the suite running against the head that
merge made finishes and is read exactly as before; a repair in flight
finishes and is judged exactly as before. The only branch that behaves
differently on the first cycle after the upgrade is one whose grain CI
has failed and which had not yet been refreshed -- it gets its repair a
cycle sooner and a suite cheaper, which is the intent.

`base_branch_ci` starts empty, so no branch is considered broken until
the first merge after the upgrade starts a run there. A deployment
upgrading across this does not inherit a block it cannot explain.

The confirmation added later is the same kind of change: its columns are
added to an existing `base_branch_ci` by `Store.Init`, all defaulted, and
a failure recorded by the build before it reads back as one with no jobs
written down to re-run -- which is confirmed as it stands and files its
repair exactly as that build would have. Only failures recorded from the
new build onwards are challenged.

## Which repositories should have the strict rule on

None, as far as this change can tell, and `bwsalmon/grain` least of all:
it is the repository the measurement above was taken on, its suite is the
expensive one, and its CI host is the constrained one. The option exists
for a repository whose base branch cannot afford to be red for the twenty
minutes it takes to find out -- something publishing on every merge, say
-- and turning it on there is a tick on that repository's own pane,
under **Merge queue** (`PUT /api/repos/{owner}/{name}/
require-branch-up-to-date`, grain/task-203), or the same
`repo_config` row set by a `grain/config` pull request. Nothing at all is
needed for the cheap behaviour: it is the default.

The tick carries the bill beside it -- "every branch open against this
repository is re-tested each time its base moves" and the four suites in
ninety minutes measured above -- because that is the half a switched-on
checkbox cannot say, and because nothing else on that page would ever
show the cost arriving.

## Which repositories should have their default branch watched (grain/task-354)

`WatchDefaultBranch` is the other tick, and the question it asks is not
the one above: it does not change how anything is judged, it decides
whether a branch is judged *at all*. grain/task-331 built it and
deliberately left it off everywhere, so the hole it was built for stayed
open until somebody named the repositories. Here is the naming, and the
answer it arrived at is not the one the shape of the question suggests.

**All four, and it costs nothing.** The repositories grain CI runs for on
this deployment are `bwsalmon/grain`, `gke-labs/kontur`,
`gke-labs/granule` and `grain/config` -- the whole of the CI record, 847
remembered runs, is those four and nothing else, and `bwsalmon/test1` has
never had a run at all. Every one of the four **already has a
`base_branch_ci` row for its default branch**, because grain has merged
into all of them, and each one's `main` has a recent base-branch run to
prove it (read on 2026-09-20):

| repository | last run against `main` | |
|---|---|---|
| `bwsalmon/grain` | `1551bd0546841852` at `99650c2` | success, 39m, `release images` |
| `gke-labs/kontur` | `9dc5e7651cf56d9b` at `8c4ea80` | success, 41m |
| `gke-labs/granule` | `d7db15a953fd4d25` at `afebe1e` | success, 18m |
| `grain/config` | `8ef5a50bcdebb9a4` at `4c6d327` | errored, the `release`-event mismatch grain/task-396 fixed |

So `seedWatchedBaseBranch` finds a row for each of them and returns
`false` -- "already watched, by this or by a merge". Ticking these four
enrols nothing, starts no suite, holds no queue and files no repair. The
bill the switch's own documentation warns about, and which the reasoning
above spends most of its length spreading over one repository per cycle,
**is zero for this deployment**, and the advice to pick a moment when the
CI host has room does not apply to it. That bill is real for a repository
whose default branch has no row; there is not one here.

Which leaves the tick worth setting for what it does *later* rather than
now, and it is worth being honest that this is insurance and not a fix:

- `base_branch_ci` starts empty ("What about a branch mid-update", above),
  so a restored database or a fresh deployment of this configuration is
  back to watching nothing until grain happens to merge into each
  repository again. With the tick, the next cycle re-enrols them.
- a default branch that is renamed is a different `(repository, branch)`
  key, and the row for the old name does not follow it.
- `gke-labs/kontur` is the repository whose `main` took five commits with
  nothing judging them (grain/task-320, above), and hand-merges are what
  did it. Its row exists today because grain has merged into it since;
  the tick is what makes that not depend on grain having done so.

`bwsalmon/test1` is **not** ticked. It has no grain CI runs in the whole
record, so either grain CI is not enabled for it -- in which case the
tick would be the setting that quietly does nothing, logged once per
daemon by `seedWatchedBaseBranch` and no use to anybody -- or it is
enabled and unused, in which case ticking it buys a full suite on a
repository nothing is being merged into. Neither is worth spending.

One limit on all of the above, stated because the next person to revisit
it should not have to rediscover it: the authoritative candidate set is
`grain-ci` in `model.Config.DefaultCapabilities` and in each repository's
own `DefaultCapabilities`, and those live in `grain/config`, which a
`bwsalmon/grain` sandbox cannot read. The four above are derived from CI
history instead, which is sound for a repository grain CI has ever run
for and blind to exactly one case: a repository with `grain-ci` enabled
that has never run anything. Such a repository is also the one the seed
was built for, so whoever holds the state repository should check the
list against `tables/repo_config.json` rather than trusting this table to
be exhaustive.
