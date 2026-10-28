# Batching the merge queue

> Companion to `merge-queue-behind.md`, which is the other half of the
> same trade: that one is why a branch no longer has to contain its base
> before it merges, and what compensates for it after the fact. This one
> is how several changes now get tested together before any of them land.

## What it replaces

Until grain/task-294 a repository's merge queue drove one entry at a
time. It merged the head, started a full suite against the branch that
merge landed on (`model.BaseBranchCI`; one per merge then, one at a time
since grain/task-334), and only then
looked at the next entry. Three queued changes therefore cost three
suites of 16-50 minutes and three cycles of latency, and the failure
those post-merge suites exist to catch -- two changes each green on their
own branch and broken together -- was only ever found *after* both had
landed, at which point the whole repository's queue stops and an agent is
spent repairing `main` with two changes in it to bisect between.

A change that is ready alone used to merge on its own green with no
second look. It is now asked the overlap rule, which in `enforce` mode
re-checks it as a batch of one when it overlaps what has landed since it
forked (docs/merge-queue-behind.md, "The overlap re-check"). That makes
batching an optimisation among changes that need a re-check anyway, not
the only way a re-check happens.

## The shape

The same one GitHub's own merge queue uses:

1. Take the first few ready entries from the front of a repository's
   queue (`collectBatchCandidates`).
2. Force-reset a throwaway ref grain owns --
   `grain-merge-queue/<deployment>/<branch>`, `model.MergeBatchRef`, with
   the same deployment id task branches carry so two grains on one
   repository never reset each other's batch -- to the base branch's current tip, and merge
   each member's head branch into it (`startMergeBatch`).
3. Run one grain CI suite against that ref.
4. On a pass, merge every member's own pull request, each pinned to the
   exact commit the batch contained (`mergeBatchMembers`).
5. On a failure, re-run the jobs it failed on once, uncached, against the
   same batch commit (`recheckMergeBatch`). A failure that does not
   reproduce is a flake: it is recorded on every member and in the test
   history, and the batch merges. Only a failure that reproduces is
   bisected (below). This is what keeps a flaky job from sending a
   bisection down to a member that broke nothing.

The ref is the part worth saying twice. Nothing is ever merged *from* it
and nobody has a pull request open against it; it exists so that the
speculative merge happens somewhere grain owns rather than on the
contributor's branch. grain's one pre-existing "test them together"
mechanism does the opposite (`bringHeadUpToDate`, the strict
`RequireBranchUpToDate` rule) -- it writes the base into somebody's own
branch, which throws away the full-suite verdict that branch was holding
and turns a conflict into a dispatched repair task.

## Failure policy

A batch of N is a bet that all N are good. The saving when it pays off is
at most N-2 suites and on a busy branch is nothing at all (the arithmetic
is under "What a batch costs, counted" below: the post-merge run happens
either way, a batch pays it once, and one of those runs already judges
several merges); the cost when it does not is an extra suite plus the
latency of the changes that were fine. So the failure case is most of the
design:

- **N > 1: halve from the front.** The first `ceil(N/2)` members become
  the next batch and the rest go back to the queue. If that half fails
  the culprit is in it and it halves again; if it passes, those members
  merge and the remainder is re-batched.

  Halving from the front rather than testing both halves is deliberate:
  the members are in the queue's order, and the second half will land on
  top of the first, so testing it alone would be testing a tree that
  never exists.

  There is no subdivision limit to pick, because halving reaches a single
  member on its own. A batch of 3 is isolated in at most two further
  runs -- four suites in the worst case against the three the queue would
  have spent anyway, and one suite in the case that actually happens most
  of the time.

- **N == 1: that member is the culprit.** It is green on its own head and
  red on top of the base it wants to merge into, which is the same
  evidence a red pull request head carries, so it gets the same treatment
  the queue has always given that: an automatic repair on its own
  branch, naming the ref and the run -- a second only if the base has
  moved or the failures differ since its first, never a third -- and a
  person after that (`sendBatchCulpritBack`). Everything else in the queue carries on
  merging without it.

- **Anything that is not a pass and not a failure** -- an errored or
  cancelled run, a run the daemon has lost, a ref that could not be
  built, a batch older than `maxMergeBatchAge` -- is not a statement
  about the tree, so no culprit can be read out of it. The batch is
  abandoned, its members are released, and the repository merges one at a
  time for `mergeBatchCooldown` before another batch is formed. That is
  the fallback: it degrades to the queue grain had before, not to a
  blocked queue.

## How deep to speculate

`model.Config.MergeBatchSize`, stored beside the other queue settings and
defaulting to `model.DefaultMergeBatchSize` = **3**.

Three was picked as the smallest number that saves a suite at all, and
the arithmetic below no longer supports that: the saving is `(N-1)/c - 1`
suites, where `c` is how many merges one post-merge run already judges,
and `c` measured 2.7 here -- which makes a batch of three cost a quarter
of a suite rather than save one. Nothing under four saves a suite at that
`c`, and batches of four have never formed here, because arrivals rather
than the limit are what bound them. So the number is not chosen on
capacity at all: three is a small bet whose worst case stays cheap under
halving, and what it buys is the thing batching is actually for, the
combination tested before it lands. That -- rather than the arithmetic it
used to be justified by -- is why it has not moved. It is a setting
rather than a constant because the right number depends on how often a
deployment's changes actually break each other, which is a thing to
measure rather than guess: start at three, watch how many batches pass
first time, and raise it if they nearly all do. **1 turns batching off**
and leaves the one-at-a-time queue exactly as it was, which is the right
answer for a repository whose changes conflict constantly. The ceiling is
`model.MaxMergeBatchSize`, because a batch holds every member out of the
queue until it reports.

What that watching is done with is below, and what it has said so far is
in "Is three the right number?".

### Per team (grain/task-1404)

A team can set its own size, `model.Team.MergeBatchSize` (Team settings →
Capacity → *Merge batch size*, or `mergeBatchSize` on
`PUT /api/teams/{id}`), stored as `team.merge_batch_size`. **0 inherits**
the deployment's number; 1 keeps that team's changes out of batches; the
ceiling is the same `model.MaxMergeBatchSize`.

Batches are still formed per repository and base branch, from whichever
teams' changes are queued there, so the per-team number is a rule about
membership: **a batch is held to the smallest size among its members'
teams** (`mergeBatchSizes` in `pkg/orchestrator/mergebatch.go`). A team
that asked for small batches -- or none -- is never speculated on in a
bigger one because the change ahead of it belongs to a team that likes big
ones; the cost lands on the bolder team, which gets smaller batches when
its changes queue behind a cautious team's. A ready change whose team's
size the batch has already reached ends collection for that branch
rather than being skipped, so it merges on the ordinary path first and
nothing behind it is batched ahead of it. A lone candidate is held by the
gather window only where its limit would let a second member join.

A team with a size of 2 or more batches even on a deployment whose own
size is 1, and turning a team's batching off under a running batch that
holds one of its changes abandons that batch, exactly as turning it off
for the deployment does. The batch history's "held down by the limit"
reading (`Queued`, `AtLimit`) is still measured against the deployment's
number.

### Per path (grain/task-1660)

A repository's `.grain/merge-rules.yml` can cap batching for the paths a
change touches (`batching: alone`, or a maximum size; see
`docs/declarations.md`). The cap works like a team's size: it is folded
into the same minimum, so `alone` (a cap of one) ends collection where the
change stands and the change merges on the ordinary path. In a repository
with rules, a change that has not yet been classified for its current head
is not batched. The same file can also turn off a batch's flake recheck:
if any member says `flake_recheck: false`, a red batch is bisected straight
away.

## What a batch costs, counted

Every batch that ends writes one row to `merge_batch_history`
(`model.MergeBatchRecord`, grain/task-300): its size, how much was queued
behind it, how many halvings deep it was, what it concluded, how many
changes actually landed on that verdict, and the run it was about. The
`merge_batch` row itself is deleted the moment a batch ends, so before
this the only trace a batch left was a line in the daemon log, and the
question this whole section is about had no data behind it at all.

It is bounded (`model.MergeBatchHistoryKept`), holds no member list --
which changes were in a batch is on the members' own tasks -- and is
summarized by `model.SummarizeMergeBatches` into the counters the
Settings pane shows under the batch size (`ui.Settings.MergeBatching`,
`MergeBatchingNote`).

Those counters are over the whole kept history at once, which answers
"are our batches paying off?" and cannot answer "are they paying off
*now*". The same rows counted **over a window** are the metrics report's
`merges` section (`metrics.Merges`, grain/task-556): the size
distribution, the first-try pass rate and the suites saved, sliced the
same way as every other series there and printed beside what a change
actually waited to land -- which is the other half of this decision, since
a bigger batch that fails makes every change in it wait through a bisect.
`grain metrics` prints it with the rest of the report, and a run holding
`self-debug` asks for that section alone with `read_grain_metrics`'s
`section: "merges"`; `docs/tuning-stats.md` says what else is in there.

**The arithmetic, and the run it is easy to forget.** A batch run is one
suite against the throwaway ref. The subtlety is the *post-merge* run:
grain tests a base branch whenever its tip moves (`startPostMergeCI`,
grain/task-320), and a batch that lands three changes moves that tip once
where the one-at-a-time queue moves it three times. That run therefore
belongs on both sides of the comparison rather than cancelling out.

But a tip move is not a suite. grain runs **one post-merge suite per
branch at a time** (grain/task-334): merges that land while a run is
going are not queued behind it, they are swallowed by the next one, which
judges a tree containing all of them. So the honest unit is *merges per
post-merge run* -- call it `c`, one on a branch that is idle between
merges, larger the faster merges arrive relative to the suite. Counting
in suites rather than in tip moves:

| | batch of N that passes | one at a time |
| --- | --- | --- |
| batch runs | 1 | 0 |
| post-merge runs | 1/c | N/c |

So the saving is **`(N-1)/c - 1` suites**, and the smallest batch that
saves one at all is the smallest `N` above `c+1`. At `c = 1` -- a branch
quiet enough that every merge gets its own run -- that is the N-2 this
section used to claim outright, and a batch of two saving nothing. It is
the best case, not the usual one.

**Measured, `c` is about 2.7 here** (grain/task-501, below), which puts
the saving at a batch of three at `2/2.7 - 1` = **-0.25 suites**: on this
deployment a batch of three costs a quarter of a suite rather than saving
one, and the capacity argument does not break even until four. The sign
is what matters rather than the quarter -- both are inside the noise of
one re-run of a flaky job -- and the conclusion is that **batching here
is not a capacity mechanism**. What it buys is what it was written for:
the combination tested before it lands rather than after, and N changes
landing on one queue cycle rather than on N of them. That is worth having
for its own sake; it is just not suites.

Two caveats on the model itself. `c` is a property of a deployment's load
rather than a constant, so a quiet repository really does see something
near N-2 and a busy one sees nothing; and batching, by moving the tip
less often, slightly *lowers* `c` for the runs that remain, which makes
`(N-1)/c - 1` an optimistic reading of its own sign rather than a
pessimistic one.

And it is measured against whatever the deployment actually does: a
repository grain does not test after a merge spends nothing per merge, so
a batch there costs a suite and saves none. That is recorded per batch
(`MergeBatchRecord.PostMerge`) rather than assumed, because it is a
setting an operator changes -- taking the `grain-ci` capability off a
repository turns the post-merge run off, and takes the saving with it.
`RepoConfig.ReleaseWorkflowsDisabled` does not: since grain/task-458 it
changes which event that run is asked for and nothing else.

### How much the post-merge run already coalesces (grain/task-501)

`c` above, measured on `bwsalmon/grain`'s `main` over as much history as
grain CI still holds -- **17 September 2026 03:55 UTC to 21 September
14:36 UTC**, 310 merges, with a second look a day later below that takes
it to 351 over five days. This replaces the two-and-a-half-hour sample
grain/task-441 took, which is where the three-to-one figure came from;
the longer window agrees with it.

The method, so it can be repeated from an ordinary repo task sandbox
(which is all it needs -- no `self-debug`):

- merges are `git log origin/main --first-parent` entries in the window.
  Every one of the 310 is a pull request merge; nothing was pushed to
  `main` directly.
- post-merge runs are `list_ci_runs(ref: "main")`, keeping the runs that
  are base-branch runs and dropping the scheduled ones
  (`nightly`, the agent surface captures, `prune-packages`) and the
  batch runs, which are `grain-merge-queue/main` rather than `main`.
- each run names the commit it judges, so the merges it covered are the
  first-parent commits between the previous run's commit and its own.
  That is the count `c` averages.
- cancelled runs, runs the daemon lost mid-flight, and the *second*
  sample of a failure (the confirmation of grain/task-202, a fresh run
  against a commit already judged) are excluded from the run count.

| window | merges | runs that judged | merges per run |
| --- | --- | --- | --- |
| since the gate moved to the push suite (20 Sep 18:51 UTC on) | 67 | 25 | **2.68** |
| the release-gate era before it (17 Sep 03:55 - 20 Sep 17:18) | 243 | 91 started, 65 with a verdict | **2.67** counting every run started |
| both, together | 310 | 117 started | **2.65** |

The mean is the least interesting part of it. The distribution in the
current regime is 12 runs that judged exactly one merge, one that judged
two, three that judged three, four that judged four, two that judged five
and three that judged six: **48% of runs judge a single merge, and 82% of
merges share their run with another merge**. So the coalescing is not a
smooth 2.7 applied to everything -- it is roughly half the runs behaving
exactly as the old table assumed and the other half swallowing a queue
drain.

What drives it is suite wall-clock against merge interval, and both of
those are load: here the push suite takes 30-60 minutes including its
wait for a CI sandbox, and merges arrive about every 18 minutes. The
release-gate era is the same number for a different reason and is worth
reading as a warning rather than as a confirmation -- 80% of *its* runs
judged one merge each, because for days of that window `release images`
failed in under a minute, and a suite that ends in 26 seconds coalesces
nothing. A gate that is broken looks, to this measurement, like a gate on
an idle branch.

Three things this does not say:

- **It is one repository.** `list_ci_runs` only answers for the
  repository the sandbox is checked out from, so `bwsalmon/test1` and
  `gke-labs/granule` could not be measured from here. `c` is a load
  property, so theirs will differ -- a repository that merges twice a day
  has `c = 1` and gets the full N-2.
- **Five days, not seven.** grain CI remembers a bounded number of runs
  (1,188 when the first window was counted, 1,416 a day later), which
  reaches back to 17 September for `main`. Older merges have no runs left
  to compare against, so a week is not available to be counted and
  waiting for one to accumulate costs more than the extra two days buy.
- **It cannot see the runs that were never started.** A stretch where the
  gate was misconfigured or the daemon kept restarting shows up as one
  run "covering" 99 merges (there is such a span, 19-20 September). Those
  are counted above only in the all-runs-started column, and they are the
  reason the two eras' means agree more closely than the eras do.

**Re-checked ten hours later, on a day that had gone wrong.** 21
September 14:36 to 22 September 00:31 UTC: **41 merges** into `main`
(none after 22:17 -- the queue stopped), judged by **12 distinct tips**
whose run actually ran jobs, which is `c` = 3.4; counting every tip with
any verdict at all, including the runs that died at plan time, 17 tips
and `c` = 2.4. Either way the same region as the 2.7 above, and the two
windows together are 351 merges over five days.

It is a worse sample than the first, though, and worth saying why rather
than averaging it in: `main` spent that evening in an infrastructure
outage rather than in ordinary service. Runs errored building a sandbox
guest over the granule transition, `.grain/workflows/kvm-route-stress.yml`
would not parse for seven consecutive tips so their runs judged nothing,
the daemon restarted more than once, and `bc9e4d4` collected **five**
errored runs of its own. Several runs per tip and almost no verdicts is
what a broken deployment looks like to this measurement, and it is the
same confound as the 26-second `release images` era below: a tip that is
re-asked five times is not a tip that coalesced five merges. The
first window remains the one to quote.

**What would make this checkable without a sandbox archaeologist.** The
number the arithmetic turns on is per post-merge run, and grain now
writes it down: `BaseBranchCI.Covers` is how many merges the run in
flight is the answer to, counted up by `noteBaseBranchMerge` as they land
(`PendingMerges`) and taken up by `startBaseBranchCI` when the next run
starts. It is in the line the daemon logs as it starts one -- "covering 4
merges" -- so a `c` that has moved is visible where the run is. What it
is *not* yet is history: the row is replaced per run, so summing it over
a week still wants a counter of its own beside `merge_batch_history` and
`merge_gather_history`. That is the follow-up, and until it exists
`MergeBatchRecord.Saved` stays an upper bound rather than a count.

## Is three the right number? (grain/task-300)

Measured on bwsalmon/grain, 19 September 2026, eleven hours after
batching first ran here. **Keep three** -- but not because three is
finely tuned. Because the size is not what is limiting anything yet:

- **One batch has formed.** Grain CI's history holds exactly one run
  against `grain-merge-queue/main` in seven days: a batch of three
  (tasks 322, 316, 308) that passed in 24m39s and landed all three pull
  requests on that one suite. No splits, no culprits, nothing abandoned.
- **Around it, seventeen other changes merged one at a time.** They were
  never simultaneously ready: a change becomes mergeable, the queue
  merges it inside a cycle, and the next one is not ready yet. Batching
  needs two ready at the same tick, and on this deployment the queue is
  drained faster than it fills.
- **The opportunity is bounded by arrivals, not by the limit.** Taking
  merges into `main` that land within a minute of each other as a proxy
  for "ready at the same tick", a week of history is 228 changes that
  merged alone, 35 pairs, 5 threes, 2 fours, one six and one fifteen (a
  backlog drain). Two thirds of merges could never have been batched at
  all, and a fifth could only ever have been batched in twos -- where,
  per the table above, no suite is saved.
- So raising the limit would have changed almost nothing in the window
  measured, and there is no evidence either way about lowering it: not
  one batch has failed yet.

The decision rule to apply once the counters have a real sample, say
twenty batches:

- raise it (3 -> 5) if `FirstTry/Fresh` stays at or above ~0.9 **and**
  `AtLimit` is a meaningful share of batches -- that second condition is
  the one this deployment fails today, and a limit nothing reaches costs
  nothing to leave alone;
- lower it (towards 1) if more than about a quarter of batches split, or
  if abandonments are common: a batch that cannot be judged spends a
  suite for nothing and hands the queue back to the one-at-a-time path.

The thing worth changing first is not the number. It is that batches
almost never form, and the fix for that is not a bigger limit but a
willingness to wait. That is the next section -- which also re-measures
this one a day later, and finds the arrival rate, rather than anything
decided here, to be what had made batches rare.

## Waiting for the second change (grain/task-360)

**The gather window**: when exactly one entry is ready for a base branch,
the queue holds it for a bounded time rather than merging it, so that a
second change finishing its own CI can be tested with it.
`model.Config.MergeGatherWindow`, beside the batch size, defaulting to
`model.DefaultMergeGatherWindow` = **5 minutes**, ceiling
`model.MaxMergeGatherWindow`; a **negative duration turns the wait off**,
which is the right answer for a deployment with CI capacity to spare.

### What stops it being a tax on every merge

The wait is not a timer. Two thirds of the changes here merge alone, and
a window every solitary merge paid would be latency bought with nothing.
So a hold is armed only while grain can see something worth waiting for:
**another entry on the same base branch with a grain CI run actually in
flight against its own head** (`batchBranch.soon`, read off
`grainCIVerdictOf`). grain started that run, so this is a fact rather
than a guess, and it is a far better predictor of "a second change is
about to be ready" than any duration. The three ways a hold ends:

| what happens | what the held change does |
| --- | --- |
| nothing queued behind it, or nothing with a run in flight | never held; merges on the tick it is ready |
| the run reports a pass | a batch of both forms on the next tick, and one suite lands the two |
| the run reports anything else, or the entry stops being a candidate | released on the next tick and merges |
| the run is still going when the window expires | released and merges, and the wait bought nothing |

That last row is the case the five minutes bounds, and it is why the
window is far shorter than a suite (20-45 minutes here): waiting out a
whole suite to save one saves nothing.

**One wait per commit.** A hold is keyed by pull request *and* head sha
and is marked spent the moment it is taken, so a change cannot be held
again because some other run started -- which, on a deployment whose CI
is never idle, is how one change would be held indefinitely, a window at
a time. A push makes a new commit, which gets a wait of its own.

The hold itself is process state (`orchestrator.gatherHold`,
`mergegather.go`), not a column, for the reason `forge.RecordMergeRefusal`
keeps its record in memory: losing it across a restart costs one more
window and errs toward waiting rather than toward merging something
early. What *is* written down is the row a person reads --
`mergeStateGathering`, "Held to be tested with the next change" -- which
names the change being waited for and the bound, and which
`reconcileSync` leaves alone while the hold stands (`syncEntry`'s
`gathering`, beside `batched`). What *is* kept afterwards is one row per
hold that has ended, which is a measurement rather than the hold
(`model.MergeGatherRecord`, below).

### What the queue looked like a day later

Worth recording, because it moves the premise above rather than
confirming it. Re-measured on **20 September 2026, 01:30 UTC**, over the
preceding 24 hours: **82 changes merged into `main`**, and grain CI holds
**8 runs against `grain-merge-queue/main`** in that window -- against one
batch in the *seven days* task-300 looked at. Clustering those merges by
the minute they landed in: 49 merged alone, 6 landed in pairs, 2 in
threes, one four, and one burst of eleven inside six minutes (several
batches and a queue drain back to back, since a batch here holds three).

So batching did not stay as rare as the eleven-hour sample suggested --
what changed is the arrival rate, which is the thing the whole section
above says is the binding constraint. It is still the case that **three
merges in five land alone**, and that is the population a gather window
is aimed at: every one of them is a suite that a batch could have shared
if something else had been a few minutes from ready. Whether they were is
exactly what the counters below will now say.

### How to tell whether it worked

By the counters this section is measured with already
(`model.MergeBatchRecord`, Settings -> Engine): a window that works shows
up as **more batches formed** and as `Changes` rising against `Batches`;
one that does not shows up as latency with neither moving. The decision
rule is the same shape as the one for the size above -- if batches still
almost never form, the wait is not finding anything and the honest move
is to turn it off rather than to lengthen it.

What those counters cannot show is the holds themselves -- most holds
never become a batch, so the majority of the window's own behaviour
leaves no batch row -- and that is the first thing the reading below ran
into. Since **grain/task-440** they have a counter of their own beside
them: `model.MergeGatherRecord`, table `merge_gather_history`, one row
per *ended hold*, reported in the same pane
(`ui.Settings.MergeGathering`, under the window itself). Each row says
which of the three ends the hold came to -- `batched`, `released`,
`expired` -- how long the change actually waited, and the window it was
armed under, since the point of reading them is to consider changing
that window. Bounded to `MergeGatherHistoryKept` = 200 rows by its own
insert, like the batch history, so rows taken under an older window age
out. Two holds that are not counted: a pull request closed or
hand-merged out from under a standing hold, which is deliberate -- that
is neither a payoff nor an expiry -- and a hold the daemon was holding
when it restarted, which is unavoidable, since the hold itself went with
the process and nothing is left to write a row about. `Holds` is
therefore holds that *ended*, and a sample spanning a restart
undercounts rather than miscounts.

So the third branch of the rule is now readable:

- **shorten it** if `Expired` is most of `Holds` *and* the mean wait of
  the holds that did pay off (`BatchedWaited/Batched`, shown in the pane
  as "a wait that paid off took ...") is well under the window: the run
  being waited for is slower than the window, and everything past the
  point where the paying waits land is latency with no batch at the end
  of it. `ExpiredWaited` is what that has cost in total.
- **keep it** if holds mostly end `batched`, which is the wait working,
  or mostly `released`, which is the wait letting go the moment it can
  -- neither is a window that is too long.

One caveat on reading those counters here, from the same investigation
and confirmed again while writing this: **no post-merge `release` run has
started for `main` on this deployment since about 07:14 UTC on 19
September 2026** -- `list_ci_runs` for `main`'s `release images` job is
empty from 07:32 UTC onwards, through the 82 merges counted above. While
that is true the queue
spends nothing per merge, so a batch cannot save a suite however well the
window does -- the saving it is measured against is not being paid
(`MergeBatchRecord.PostMerge` records this per batch). What the window
still buys in that state is the combination tested before it lands.
Whether the post-merge runs stopping was deliberate is worth settling
before tuning any of these numbers.

> **Settled, and no longer true.** The post-merge run came back at 17:06
> UTC on 20 September 2026 and has fired for `main` ever since -- as
> `release images` up to the 21:51 merge, and as the repository's own
> seven test jobs from about 22:00, when grain/task-461 and the commits
> around it moved the base-branch gate off the release event. So the
> saving a batch is measured against is being paid again. See the 441
> reading below, which is what confirmed it.

### Reading it too early (grain/task-439)

The reading those counters were queued for was attempted on **20
September 2026, 14:40 UTC**, and what it found is that there is nothing
yet to read. **The window stays at five minutes**, untouched, because
every branch of the rule above wants a sample and there is none:
`MergeGatherWindow` reached `main` at **13:36 UTC that same day**
(#1634), 64 minutes earlier, and not one change has merged into `main`
since it did. Turning the wait off on that would be as unevidenced as
leaving it on for an argument.

What was looked at, so the next reading starts further along:

- **Batches.** `list_ci_runs(ref: "grain-merge-queue")` holds eight runs
  against `grain-merge-queue/main` in the 24 hours to 14:41 UTC on 20
  September, every one of them from before the window existed; the last
  started in the 08:00 UTC hour and nothing has batched since. Set
  against the baselines -- one batch in the seven days task-300 looked
  at, eight in the 24 hours to 01:30 UTC -- the rate is simply unchanged,
  which is what "the window has not run yet" looks like rather than
  evidence about the window.
- **Merges, clustered by the minute.** The last pre-window day (13:36
  UTC on 19 September to 13:36 UTC on 20 September) is **84 merges into
  `main`: 53 alone, 11 pairs, 3 threes**. Three in five still land alone,
  so the population the window is aimed at is as large as it was when the
  window was written. It has not yet had a day in which to aim at it.
- **Post-merge runs: still stopped, 31 hours on.** `main`'s `release
  images` job has no run that *started* after about 07:41 UTC on 19
  September -- through all 84 of those merges. The caveat above therefore
  stands in full, and nothing read now can say the window did or did not
  buy capacity, because the suite a batch would save is not being spent.
  Restoring that run is a prerequisite for half of this question rather
  than a footnote to it.

  One thing worth separating out, found while confirming it: the
  deployment is not *frozen* at that commit. Strings that only reached
  `main` at 22:38 UTC on 19 September are in what the live MCP tools
  print, so "no release run" is about what is published and tested per
  merge, not about what is deployed. Which build was driving the queue at
  any given minute is still not something a task sandbox can read, which
  is a second reason not to take a one-hour reading seriously.
- **Holds are not recorded anywhere.** This is the part worth fixing
  before the re-measure. The rule above says to shorten the window if
  holds routinely run the full five minutes and expire -- but a hold is
  process state (`orchestrator.gatherHold`), `MergeBatchRecord` has no
  field for one, and nothing is logged when one is armed, paid off or
  expires. Its only trace is the `gathering` row, overwritten the moment
  the hold ends. So of the three answers this section contemplates, *keep*
  and *turn it off* can be read off the batch counters and **shorten it
  cannot be read off anything at all**. Either that gains a counter, or
  the reading has to be somebody watching the queue live.

  **Closed by grain/task-440 (21 September 2026).** It gained the
  counter: every hold that ends now writes one `merge_gather_history`
  row saying which end it came to and how long the change really waited,
  counted by `model.SummarizeMergeGathers` and shown under the window in
  Settings -> Engine. "Shorten it" is readable off `Expired` against
  `Holds` with the paying holds' mean wait beside it, as set out in *How
  to tell whether it worked* above. Two things this does not change: the
  history only fills as holds actually happen, so the re-measure still
  wants a day of merges under the window rather than an hour; and, like
  the batch counters, **it is not readable from a task sandbox** -- the
  reading wants the Settings pane or `self-debug`.

**When to come back.** On or after **27 September 2026**, or once
`merge_batch_history` holds twenty-odd batches, whichever comes first --
and not before the post-merge runs are back, if the capacity half is what
is being asked. Reading the counters wants the Settings pane or
`self-debug`: `model.MergeBatchRecord` is not reachable from an ordinary
repo task sandbox, which is worth knowing when queueing the run that
does it.

### Reading it a second time, still early (grain/task-441)

Re-attempted on **21 September 2026, 01:44 UTC**, twelve hours after the
window reached `main`. **The window stays at five minutes**, untouched
again -- but for a different reason than last time. There is a sample
now; it is twelve hours long, it spans a daemon restart, and what it says
about a five-minute wait is nothing that could be acted on.

What is readable now that was not on 20 September:

- **The post-merge run is back, so the capacity premise holds again.**
  This was 439's blocking caveat and it is resolved: `main`'s `release
  images` restarted at the 17:06 UTC merge on 20 September and ran for
  every merge through 21:51, after which the base-branch gate moved to
  the repository's own seven test jobs and those have run since. A batch
  that lands three changes is once again saving a suite somebody was
  otherwise going to spend. It also turned up the coalescing correction
  under "What a batch costs, counted" above, which is the more useful
  finding of the two.

  *(The first of those two sentences is wrong, and the second is why:
  measured over four days rather than one afternoon, a post-merge run on
  this branch judges 2.7 merges, so a batch of three saves no suite at
  all -- see "How much the post-merge run already coalesces",
  grain/task-501.)*
- **Two batches have formed since the window landed**, both against
  `grain-merge-queue/main`: one that passed in 32m1s, and
  `fc4713860c939404` still running as this was written. The second is a
  **batch of two** -- the ref's own history is `main`'s tip, then a merge
  of 475, then a merge of 469 -- which is the window doing precisely what
  it was written to do, and the only direct evidence in existence that it
  does anything at all. Two batches in 12.1 hours is not, however, an
  increase on the eight in the 24 hours before it.
- **Merges clustered by the minute**, 13:36 UTC on 20 September to 01:44
  UTC on 21 September: **39 merges into `main`, 35 alone and 2 pairs**.
  Nine in ten land alone, against three in five on the pre-window day (85
  merges: 54 alone, 11 pairs, 3 threes) and four in five on the day
  before that (68: 55 alone, 3 pairs, one three, one four). Taken at face
  value that is the "batches did not increase" arm of the rule above,
  which says turn the window off.

It was not turned off, and here is why that face value is not worth
acting on:

- **Two batches is not twenty.** The precondition this reading was queued
  under -- on or after 27 September, or twenty-odd rows in
  `merge_batch_history` -- was met by neither. It was dispatched early a
  second time.
- **A daemon restart drops every hold, and the sample contains at least
  two.** `gatherHold` is in-memory by design (above), and grain CI's
  history for this window holds runs that ended "the grain daemon running
  it stopped before it finished". Every hold armed before each of those
  went with it. A window whose entire state is in-process cannot be
  measured across restarts of that process: this sample cannot tell a
  window that found nothing from a window that was thrown away twice.
- **The thing being measured against moved three times inside the
  sample.** grain/task-458 landed at 18:51, grain/task-461 at 20:43 and
  grain/task-164 at 22:32, and between them they changed what a base
  branch is judged by and therefore what a batch saves. Half the window
  ran under one arithmetic and half under another.
- **Minute-clustering is a weak proxy in both directions.** Two entries
  merging on consecutive reconcile ticks land in the same minute without
  ever having been a batch, so the pre-window pairs were never all
  batches and the post-window scarcity of them is not all missed ones.
  It is a hint about arrivals, not a measurement of batching.

Two things 439 left open are unchanged and were re-checked in the tree:

- **Holds are still recorded nowhere.** `MergeBatchRecord` has no field
  for one and `mergegather.go` writes no log line when a hold is armed,
  paid off or expires. The instrumentation proposed alongside 439 has not
  landed. So *shorten it* remains the one answer of the three that cannot
  be read off anything, and the 27 September reading will hit the same
  wall unless that counter exists by then.

  *(It exists as of 21 September: grain/task-440 gave holds a history of
  their own, `merge_gather_history`, described under "How to tell whether
  it worked" above. The 27 September reading should read it -- noting
  that it only counts holds armed after that landed, and, as this section
  says of restarts, only holds that actually ended.)*
- **The counters themselves are still unread.** `SummarizeMergeBatches`'s
  output wants the Settings pane or `self-debug`, and this run had
  neither. `Batches`, `Changes`, `FirstTry` and `AtLimit` are as unknown
  as they were on 20 September; everything above is inferred from grain
  CI's run history and `main`'s git log, which is the whole of what a
  repo task sandbox can reach.

**When to come back:** unchanged -- on or after 27 September 2026, or
once `merge_batch_history` holds twenty-odd rows. Read the row count
first and say it out loud before drawing anything from it, and prefer the
counters to anything inferred from run history, because the table
survives the restarts that the holds do not.

### Dispatched early a third time, and without the capability (grain/task-500)

The run queued to take the 27 September reading started on **21
September 2026, 14:37 UTC** -- six days before the date in its own
instructions, and two hours short of the sample being one day old.
**No reading was taken and the window stays at five minutes**, for two
independent reasons, either of which is enough on its own:

- **It is not 27 September.** The precondition is a date or twenty-odd
  rows in `merge_batch_history`, and the run could not check the second
  (see below) while plainly failing the first. Two batches and twelve
  hours was 441's whole finding; thirteen hours is not a different
  answer.
- **The run held no `self-debug`, so the counters were unreadable
  again.** This is the third run in a row to reach this point and stop
  here, and it is worth being exact about why there is no way round it
  from a branch: both histories are deliberately *off* `SettingsTables`
  (`pkg/model/schema.go`, `pkg/staterepo/tables_internal_test.go`), so
  they are not exported to grain/config and would not be readable there
  even if a repo sandbox could reach that repository, which it cannot;
  there is no `grain` CLI on a sandbox's PATH; and without `self-debug` a
  task sandbox has no state-reading tool at all. **The reading is a
  person on Settings -> Engine, or a task carrying `self-debug`.**
  Queueing it any other way produces this paragraph again.

What was confirmed rather than read, so the fourth attempt need not
redo it:

- **The capacity premise still holds.** `main`'s post-merge suite is
  firing and passing: the seven-job run against the 14:36 UTC tip
  (905c1b9) came back green, as did the runs for the tips before it. A
  batch still saves a suite somebody was going to spend, so 439's
  blocking caveat remains closed.

  *(The premise holds -- the runs are firing -- but the sentence about
  what a batch saves does not, and it is the same slip 441 made: those
  runs are firing once per 2.7 merges, not once per merge, so a batch of
  three saves none of them. See "How much the post-merge run already
  coalesces", grain/task-501.)*
- **The hold counter landed and is in the tree.**
  `pkg/model/merge_gather_history.go` (grain/task-440, commit
  `55eb9d7ba`) with `SummarizeMergeGathers` beside it, so *shorten it* is
  no longer the undecidable arm of the rule and the reading can now
  reach all three answers. The dependency 441 flagged for this task is
  satisfied.

**When to come back:** on or after **27 September 2026**, in a task
carrying **`self-debug`** -- or as a person reading Settings -> Engine,
which needs no task at all. Then: say how many rows `merge_batch_history`
and `merge_gather_history` hold before drawing anything from them, defer
again if the batch history is still under twenty, and check
`list_ci_runs` for runs that ended "the grain daemon running it stopped
before it finished" inside the sample, because every hold in flight went
with each of those.

### A fourth early dispatch, still without the capability (grain/task-549)

The run queued to take the 27 September reading started on **22
September 2026, 01:25 UTC** -- five days before the date in its own
instructions, which by then said that date twice and named the missing
capability in bold. **No reading was taken and the window stays at five
minutes**, for the same two reasons as the run before it, and this
section exists mostly so the fifth attempt is not spent rediscovering
them:

- **It is not 27 September.** The sample is 35.8 hours old.
- **The run held no `self-debug`.** Its whole tool set was the sandbox
  plus grain CI's run history (`list_ci_runs`, `get_ci_log`). So step one
  of its own instructions -- *say the row counts out loud first* -- could
  not be carried out, and every step after it depends on that one:
  `SummarizeMergeBatches` and `SummarizeMergeGathers` are unread for the
  fourth time. Nothing on a branch can change that, for the reasons the
  500 section sets out. **The reading is a person on Settings -> Engine,
  or a task carrying `self-debug`.**

Dispatching it early is not free either. This was attempt 10 of the task:
attempts 4, 5, 6, 8 and 9 never reached a sandbox at all (granule guests
that answered but never acquired a default route) and attempt 7 lost its
model provider. A reading that cannot be taken has now cost ten runs.

What run history says anyway, so the fifth attempt starts further along.
All of it is the weaker evidence the instructions say to prefer the
counters over, and it is recorded as a starting point for reading them
rather than as a substitute:

- **Six batches have formed since the window landed** -- `fc471386`,
  `7cb6f9fe`, `5b0073d2`, `a1f04aae`, `08db3515` and `cb619930`, all
  against `grain-merge-queue/main`. That is 4.0 a day against the
  **eight in the 24 hours before the window** (`3a6749a6` through
  `fb6a2879`, the same eight 439 counted), and about 4.4 a day if the
  dead tail below is excluded. Taken at face value the batch rate has
  halved, which is the *turn it off* arm of the rule -- and face value is
  exactly what this evidence is not worth, because how often a batch
  forms depends on arrival clustering and queue depth as much as on the
  window. It is a reason to take the reading, not a reading.
- **A correction to 441:** it counted two batches formed since the window
  landed, but `fb6a2879` *started* before 13:36 on 20 September and only
  finished after it. One batch had formed by then, not two.
- **A batch can be abandoned by something no member did, and one was.**
  `cb619930` (between 16:00 and 18:46 UTC on 21 September) never planned
  a job: `ci: .grain/workflows/kvm-route-stress.yml: line 203: field
  infrastructure not found in type ci.rawStep`. `main`'s copy of that
  file carries no such field, so the malformed step arrived with a
  member. A run-level error is the *neither a pass nor a failure* arm of
  the failure policy above: the batch is abandoned, its members released,
  and the branch merges one at a time for `mergeBatchCooldown` (ten
  minutes, `pkg/orchestrator/mergebatch.go`). Two things follow for the
  reading. At least one `Abandoned` row in the sample is a bad workflow
  file rather than a flaky run, so `Batches` and `FirstTry` want that row
  discounted. And if the file was well formed on each member's own branch
  and invalid only in the merge of them, this is exactly the breakage
  batching exists to catch, arriving in the one shape the policy
  deliberately does not bisect.
- **Daemon restarts are frequent, and cost the holds less than 441
  feared.** `main` alone holds fourteen runs that ended "the grain daemon
  running it stopped before it finished" since 20 September, across eight
  distinct tips (`5072201`, `fb25fbd`, `f292b94`, `51b63bf`, `5ca5606`,
  `6be2243`, `cc7b243`, `ec9e07c`) -- so at least eight restarts inside
  the sample. Every `gatherHold` in flight went with each of them, as 441
  says. But a hold lives at most five minutes, so eight restarts can
  destroy holds over at most about forty minutes of a 35.8-hour sample:
  **`Holds` undercounts by a couple of per cent, not by half.** That is a
  correction to 441, which read two restarts inside a twelve-hour sample
  as grounds to distrust the whole of it. Over a sample this long the
  restarts are noise -- but check the ratio again when the reading is
  taken, because what matters is restarts against sample length, not the
  count.
- **The tail of the sample is an outage, not a quiet queue.** Nothing has
  merged into `main` since 22:17 UTC on 21 September and no batch has
  started since before 18:46. In the same stretch `main`'s own post-merge
  suites are erroring rather than concluding -- five runs against
  `bc9e4d4` died on `/run/granule/vsock.sock` -- and the `frontend` job
  is failing on `main` and two branches at once on an npm cache miss
  (`zwitch-2.0.4.tgz`, `ENOTCACHED`) rather than on anything in the tree:
  the same commit builds clean in a sandbox that can reach the registry.
  No merges means no holds and no batches, so a reading taken in this
  stretch would be measuring the outage. That is a third reason, beyond
  the date and the capability, to take this reading over a span that
  contains a working queue.
- **Minute-clustered merges**, for continuity with the two readings
  before it and worth no more than they said: 13:36 UTC on 20 September
  to 01:25 UTC on 22 September is **120 merges into `main`: 91 alone, 6
  pairs, 3 threes, 2 fours**. The 81 merges since 441 took its slice are
  56 alone -- seven in ten, against the nine in ten 441 saw and the three
  in five of the pre-window day. So 441's "taken at face value that is
  the turn-it-off arm" does not survive even at face value; the
  clustering is back where it was before the window existed. It is still
  a hint about arrivals rather than a measurement of batching, for the
  reason 441 gives: two entries merging on consecutive reconcile ticks
  land in the same minute without ever having been a batch.

One refinement to the precondition, found while failing to check it.
"Twenty-odd rows in `merge_batch_history`" is not quite the right test
any more: that table has been filling since grain/task-300 and is bounded
at `MergeBatchHistoryKept` = 200 rather than reset, so most of its rows
predate the window and say nothing about it. **Read both counts, and take
`merge_gather_history` as the precondition** -- every row in it is a hold
that happened under a window, each carrying the window it was held under
(`model.MergeGatherRecord`; the comment at the top of
`merge_gather_history.go` is explicit that rows ageing out is what keeps
a changed window from being averaged with the old one). Twenty batch rows
with no gather rows under them would be the same non-reading this section
is.

**When to come back:** unchanged, and now with four runs' worth of
evidence that queueing it any other way produces another one of these. On
or after **27 September 2026**, in a task carrying **`self-debug`** -- or
as a person reading Settings -> Engine, which needs no task at all. Then:
say how many rows each history holds before drawing anything from them,
defer if the holds under the current window are still a handful, read
`list_ci_runs`'s daemon restarts as a fraction of the sample rather than
as a count, and prefer the counters to anything inferred from run
history.

And if the reading does conclude the window should move: `grain settings
-merge-gather-window ...` changes this deployment's own configuration,
which lives in **grain/config**, its database exported as text. So the
change is a pull request against that repository (or an operator on the
Settings pane), not a commit here. Nothing in this repository sets the
window -- `model.DefaultMergeGatherWindow` is only what a deployment that
has never set it gets.

## What a batch does *not* do

**It does not change what satisfies the gate for a member.** Every member
arrives already holding a full-suite pass against its own head
(`grainCIVerdictOf`); that is what makes it a queue entry at all. The
batch run is an *additional* verdict about the combination, recorded on
`model.MergeBatch` and nowhere else. It is never copied onto a member's
own `model.CIStatus`, so a change that leaves a batch -- halved out of a
failing one, dropped for conflicting with the ref, or released when the
batch was abandoned -- carries away neither a red about a tree it was
never alone in nor a green about a commit that was never its head.

**It does not replace the post-merge control.** `main` is still tested
as it moves -- one release run per branch at a time since grain/task-334,
so a batch of three is judged by the run started once it lands rather than
by three of them -- a red one still stops the queue, and the failure is
still confirmed by an independently-run second sample before it costs an
agent (grain/task-202). A batch makes both *more* important, not less: a
bad batch would redden `main` with several changes in it at once. What
batching buys is that most such failures are now caught an hour earlier,
on a tree that never reached `main`, with the bisect already done.

**It does not reorder the queue.** The candidate walk follows
`mergeWalk`'s existing rule about who may be reordered around: an entry
whose next move is a whole agent run or a person (conflicted, red, being
repaired, worked on) is passed over, and an entry that is merely still
deciding stops the collection for its branch.

## Where the pieces are

| piece | where |
| --- | --- |
| the row | `model.MergeBatch`, table `merge_batch`, off `SettingsTables` |
| the controller | `orchestrator.reconcileMergeBatches`, cycle step `merge-batch` |
| the hold on a member | `queueEntry.batched`, `mergeStateBatching` |
| the setting | `model.Config.MergeBatchSize`, `ui.Settings.MergeBatchSize`, `grain settings -merge-batch-size` |
| the wait before one forms | `orchestrator.gatherHold` (`mergegather.go`), `mergeStateGathering` |
| its setting | `model.Config.MergeGatherWindow`, `ui.Settings.MergeGatherWindow`, `grain settings -merge-gather-window` |
| what it cost | `model.MergeBatchRecord`, table `merge_batch_history`, `model.SummarizeMergeBatches` |
| how many merges one post-merge run answered for | `model.BaseBranchCI.Covers` and `.PendingMerges` (grain/task-501), in the line `startBaseBranchCI` logs |
| where that is read | Settings -> Engine, under the batch size (`ui.Settings.MergeBatching`, `ui/src/components/MergeBatchingNote.jsx`) |
| the same rows over a window | `metrics.Merges` (`pkg/metrics/merges.go`), `grain metrics`, `read_grain_metrics` section `merges` |
| what each wait did | `model.MergeGatherRecord`, table `merge_gather_history`, `model.SummarizeMergeGathers` (grain/task-440) |
| where *that* is read | Settings -> Engine, under the window (`ui.Settings.MergeGathering`, `ui/src/components/MergeGatheringNote.jsx`) |
| the tests | `pkg/orchestrator/mergebatch_test.go`, `pkg/orchestrator/mergegather_test.go`, `pkg/model/merge_batch_history_test.go`, `pkg/model/merge_gather_history_test.go` |

## Interaction with per-job file keys (grain/task-293)

The two compose: a batch that touches only `ui/` need not re-run the `go`
job, which makes a batch cheaper still. This landed **first**, and it
assumes nothing about what a cache hit means -- it asks
`ci.Request{Event: EventPullRequest}` for the whole suite against the
batch commit and reads `ci.Run.Conclusion`, exactly as the post-merge
base-branch run does. Whatever 293 does to job selection applies to a
batch's run the same way it applies to any other.
