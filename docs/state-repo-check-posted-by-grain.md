# Should grain run its own state-repo check and post it through the Checks API?

**Status: decided and built** (grain/task-288). The question in section
7 was put to whoever owns the account this deployment runs against, and
the answer was yes: move the checking into grain rather than GitHub. The
design below is what was built, in `pkg/staterepo/postcheck.go`,
`cmd/grain/statecheckpost.go` and the merge queue's `StateRepoGate`
(`pkg/orchestrator/sync.go`) -- with one addition the answer came with,
recorded in section 8: the merge into the state repository is
special-cased on the way out too, so the deployment takes up the commit
that changes what it runs on rather than waiting for a tick.

What is still not done here, and cannot be: the settings half.
`"postCheck": true` in `state-repo.json` and the App credential it needs
are a change to `bwsalmon/grain-home` and to the host, not to this
repository. Until an operator makes it, this code is off on every
deployment and nothing about how any repository is checked has changed.

This is the follow-up to `docs/state-repo-check-startup-failures.md` --
its "What was rejected: grain runs the check itself" section is the
proposal, and task-228 deliberately stopped short of it. That document
named three things to settle before any code. What follows answers all
three, and the first answer is not the one the question assumed.

Read that document first for the condition itself: GitHub concludes the
check grain installs before any job of it starts, `bwsalmon/grain-home`
has been in that state since the file was installed, and no commit
anybody pushes clears it.

## 1. The permission is not the permission we thought

The previous document said this "needs `checks: write` on the App, which
this deployment may not have". Both halves need correcting.

GitHub's own REST documentation for the Checks API, in its own words
(fetched 2026-09-07 from
<https://docs.github.com/en/rest/checks/runs>):

> Write permission for the REST API to interact with checks is only
> available to GitHub Apps.

> To create a check run, you must use a GitHub App.

> OAuth apps and personal access tokens (classic) cannot use this
> endpoint.

> If you aren't building a GitHub App, you might be interested in using
> the REST API to interact with commit statuses.

So `checks: write` is not a permission that can be granted to the
credential this deployment is documented to run on. `terraform/gcp/
README.md`'s "What the PAT needs" is a **fine-grained** personal access
token, and that section already records the read half of the same fact:
GitHub offered fine-grained tokens a `Checks` permission, withdrew it,
and has said only Apps may use that API in the meantime -- which is why
`pkg/orchestrator`'s `checkRunsFor` falls back to the Actions API at all.
A classic PAT with `repo` can *read* checks and still cannot create one.

The question to put to the account owner is therefore bigger than a
checkbox, and it is this:

> **May this deployment authenticate to its own state repository as a
> GitHub App installation, rather than as the fine-grained PAT it uses
> today -- an App registered on the account, installed on
> `bwsalmon/grain-home` alone, holding Metadata read, Contents
> read/write, Pull requests read/write, Actions read and Checks
> write?**

Three notes on that question, so it can be answered without reading the
rest of this.

**The credential kind already exists in grain.** `pkg/gitproxy/
apptoken.go` mints installation access tokens from an App's private key,
and a `*.app.json` entry in `secrets/github/` is served to everything
that asks the credential ladder for a token -- including
`repoToken`, which is what the state repository's own pushes and reads
authenticate with (`cmd/grain/staterepo.go`). Moving the state repo onto
an App is configuration, not plumbing.

**It should not be the App `grain controller bootstrap-github-app`
registers.** That App exists for `pkg/capability/githubsandbox`: it is
installed on the *bot* account, it creates and destroys throwaway repos
for agent runs, and `manifestPermissions` in `cmd/grain/controller.go` is
deliberately drawn at exactly the union of what that capability ever
mints, with the comment saying why -- "nothing minted from this App,
ever, can exceed what is granted here". Adding `checks: write` to it
widens a ceiling whose whole argument is that it is narrow, for a
repository on a different account that that App has no other business
touching. A second App, installed on one repository, is the smaller ask.

**There is a cheaper answer that needs no App, and it is worse but not
absurd.** A commit status (`POST /repos/{owner}/{repo}/statuses/{sha}`)
is what GitHub itself points non-Apps at, and a fine-grained PAT *can*
hold "Commit statuses: read and write". It shows on the pull request, it
can be a required status check in branch protection, and it costs no new
identity. What it costs instead: grain reads no commit status anywhere
today (`checkRunsFor` reads check runs, or Actions runs, and nothing
else), so the merge queue would need a third read added before its own
gate could see it; a status carries a description of 140 characters and
no output body, so the *reason* an import failed cannot travel with it;
and it is attributed to the account behind the token rather than to an
App, so "grain says this loads" and "the bot account says this loads"
become the same sentence. It is the fallback if the App is refused, not
the recommendation.

## 2. What the merge queue can actually see (measured)

Before anything is decided, one fact that changes the shape of the whole
problem, and that neither previous document had.

**A workflow run GitHub concludes `startup_failure` leaves no check run
behind at all.** Not a red one -- none. Measured on 2026-09-07 against
three public repositories through the unauthenticated REST API
(`home-assistant/core` run 32985966429, `nodejs/node` run 32985996985,
`microsoft/vscode` run 33373652045): each run reports `"total_count": 0`
jobs, its check suite reports `"total_count": 0` check runs, and the head
commit's own `/check-runs` list -- 38 entries on the `node` commit, 10 on
the `vscode` one -- contains nothing from that suite. The suite exists
and is empty, because no job of the run was ever created.

`pkg/orchestrator/sync.go` already knows this, in one clause of
`healthFrom`'s doc comment: `startup_failure` reaches the queue "only
ever from the Actions fallback". The consequence had not been drawn out.

The red X blocking every settings pull request against `bwsalmon/
grain-home` is visible to grain's merge queue **only because this
deployment's credential cannot read the Checks API**. `checkRunsFor`
tries `ListCheckRuns`, is refused, falls back to `ListWorkflowRuns`, and
that endpoint -- and only that endpoint -- reports the dead run. Give the
same deployment a credential that can read checks, and the failing entry
disappears from the list the queue judges: `healthFrom` sees an empty
check list, `emptyChecksSettled` settles it after the registration
window, and the pull request reads `PrClean` and merges.

Two things follow, and the second is a trap.

- The gate that this whole line of work is trying to lift is a property
  of the Actions fallback rather than of GitHub. That is worth knowing
  before anybody argues about how to replace it.
- **Migrating this deployment's credential to a GitHub App, on its own,
  silently removes the gate.** Not "weakens": removes. Settings pull
  requests would start auto-merging unvalidated, with the red X still
  drawn on them in GitHub's web UI -- where a human would read it as
  blocking -- because an empty check suite still renders as a failed
  workflow run there. Any migration onto an App credential must land
  *with* the posted check, never before it, and never as a side effect of
  some unrelated change (a deployment adopting an App to read checks from
  a non-Actions CI provider, say, which is the reason `apptoken.go`
  exists in the first place).

That trap is the strongest argument for doing this work at all, and it is
also why the work has to be done as one piece.

## 3. Who marks the homework

The second objection in the previous document: grain would become the
author of the check on its own settings changes, "self-marked homework
is a weaker gate", and it "fails silently in exactly the case that
matters -- a deployment too broken to run the check is also too broken to
say it did not". Here is the answer, in three parts.

**The independence that exists today is smaller than it looks.** The
workflow does not run some neutral grain. It runs *this deployment's own
image*: `installWorkflow` writes the image line from the build the
deployment is running and keeps it pointed there
(`workflowImageCommitMessage`, `Settings.CheckImage`), for the reason
that file's own header gives -- `grain state check` refuses a dump
stamped with a schema it does not know, so a check running any other
build answers a question nobody asked. And agents editing the state
repository are already told to run `grain state check .` in their
sandboxes before pushing (`stateRepoSection` in `pkg/orchestrator/
run.go`). A third party running the check is not what the Actions run
adds.

What it adds is exactly three things: the **trigger** (GitHub decides to
run it, on every pull request, whether or not grain is alive or willing),
the **venue** (a machine the deployment does not control), and the
**record** (a log any reviewer can open).

Of those, only the trigger is load-bearing. The venue buys nothing for a
question whose entire content is "can the binary that will import this
tree import this tree" -- the deployment's own host is the *more*
faithful venue for that, not a worse one. The record survives the move: a
check run carries a title, a summary and an output body, so the import's
own report can be as readable on the pull request as a job log is, and
more to the point than a job log, since it would be the check's whole
text rather than the tail of a `docker run`.

**The silent failure is real, and it is sharper than "weaker".** Measured
against `healthFrom` rather than argued: a missing check is not a blocked
pull request, it is a *clean* one. An empty check list, once the
registration window settles, is `PrClean`, and the queue merges it. So
the naive shape -- grain posts a check when it can -- converts a gate
that fails closed into one that fails open, by default rather than by
accident. The previous document was right to name this, and it is the
single thing the design has to be built around.

It is closable, and this is the crux of the whole proposal:

- **Post the check before running it, not after.** grain creates the
  check run `in_progress` on the head commit the first time it sees the
  pull request, and concludes it when the import finishes. A deployment
  that dies mid-check then leaves a check that never completes --
  `PrPending`, which blocks, and which `escalateStalledChecks` takes to a
  person after the stall deadline -- rather than leaving nothing.
- **Teach the queue that this repository requires that check by name.**
  For the case a self-posted check can never cover -- a deployment that
  never saw the pull request at all, because it was down for the whole of
  it -- absence has to mean "not clean" rather than "nothing to report".
  That is a rule about one repository (the deployment's own state repo)
  and one check name, and it is the piece of code that keeps the gate
  fail-closed.
- **For humans and for GitHub, make it a required status check** in the
  repository's branch protection. That is a one-off a person does in
  settings; grain neither can nor should do it (`administration: write`
  is not on the list above, deliberately).

**What stays true, and should be written down rather than argued away.**
A grain that is subtly wrong about whether it can load a tree will be
wrong on GitHub's runner too, because it is the same binary -- that was
already true today and this changes nothing about it. And a grain
compromised enough to lie about its own check is a grain that already
writes this repository's contents on a timer. The gate never protected
against a dishonest grain; it protects an honest one from being handed a
tree it cannot read, and that protection survives the move intact as long
as the two rules above hold.

## 4. What happens when both exist

For grain's own merge queue, they never both exist. With a
checks-capable credential the Actions startup failure is invisible
(section 2); without one, a posted check run is impossible (section 1).
There is no duplicate verdict to arbitrate inside grain -- only in
GitHub's web UI, where the empty startup-failed suite goes on being drawn
red beside the posted check.

So: **post always, on every pull request against the state repository,
and not only when `Repo.WorkflowStartupFailures` says the Actions check
is dead.** Three reasons:

- The marker is a once-an-hour verdict about a repository, and what would
  consume it is a per-pull-request decision. A marker that flipped
  mid-flight would leave a check run created and never concluded, or a
  pull request nobody checked, and neither failure is visible from
  anywhere.
- A check that only appears once another one has broken is a check nobody
  has ever seen working. That is precisely the state `grain-home`'s
  Actions check is in today, and it is how it got there.
- Two green verdicts cost a second or two of one host's time. Two
  *different* verdicts on the same tree is information worth having: the
  published image and the running binary disagreeing about a dump is a
  deployment about to import something its own build reads differently.

Where the marker does belong is in what grain *says*, not in what it
does. A deployment posting its own check on a repository whose Actions
check cannot start should say so where workflow health is already
reported -- the State pane and `grain state status` -- because "the red X
is a workflow that never started, the green tick beside it is this
deployment, and here is what to do about the first" is the sentence that
stops an operator debugging the wrong one.

## 5. `installWorkflow` and `noWorkflow`

`installWorkflow` keeps installing, unchanged. Two gates are cheap where
Actions works, the file's own header is still the only place the remedies
for a startup failure are written down, and a grain that deleted its own
file and then needed a second marker to stop itself rewriting it is the
deployment arguing with itself in its own git history that the previous
document refused.

`noWorkflow` keeps meaning exactly what it means now -- "do not offer the
Actions check" -- and stops meaning "give up the validation". The new
setting sits beside it (`"postCheck": true` in `state-repo.json`,
defaulting to false, because it needs a credential most deployments will
not have). Two booleans, four states, and all four are meaningful:

| `noWorkflow` | `postCheck` | what checks this repository |
|---|---|---|
| false | false | GitHub Actions, as today -- the default |
| false | true | both; the deployment's own check and the published image's |
| true | false | nothing grain installs; the operator's own arrangement |
| true | true | the deployment only -- the end state for `grain-home` |

That last row is where `bwsalmon/grain-home` should end up: App
credential, `postCheck` on, `noWorkflow` on, and the dead workflow file
removed by the operator in the same change. It is the same trade the
previous document declined to make on the operator's behalf -- and it
stays theirs to make -- but it stops being a trade of validation for
nothing, which is what it is today.

## 6. The shape, as built

Every bullet below was a sketch when this document was a proposal, and
each is now a paragraph of `pkg/staterepo/postcheck.go` or of the file
named beside it. Two things the sketch did not say, learned in the
building, are at the end.

- **Trigger**: the state sync tick (`stateSyncInterval`, 30s), which
  already carries the workflow-health read. Prompt enough that a settings
  pull request is checked within a cycle, and already holding the lock
  that keeps an adopt and an import apart.
- **Discovery**: list open pull requests on the state repository's remote
  (`pull_requests: read`, which the credential already needs). Not "pull
  requests grain's own tasks opened" -- a person's pull request against
  the settings has to be checked too, and is the one most likely to be
  hand-written.
- **The credential and the client**: `repoToken` and `github.NewClient`,
  exactly as `cmd/grain/statechecks.go` already does it, and for the same
  reason -- `pkg/staterepo` has no GitHub credential and should not grow
  one. This is a second `Config.` seam beside `WorkflowRuns`, not a new
  dependency.
- **The tree**: fetch `refs/pull/N/head` into the clone grain already
  keeps, and check it out into a temporary worktree. Never the merge ref
  (`refs/pull/N/merge`), which is a commit GitHub composed and no check
  run can be keyed to.
- **The check**: `staterepo.Check` is a library function -- the same one
  `grain state check` wraps -- so the import can run in-process. It
  should not. Run the binary as a subprocess with a timeout: the tree
  comes from an unmerged pull request, this is the first time the host
  ever runs anything derived from one, and the whole cost of bounding it
  is one `exec` of a binary that is already on the box. The exposure is
  small either way (the import is a JSON read and a transaction of
  inserts into a throwaway database), but "small" is not a thing to
  discover from inside the daemon's own process.
- **Idempotency**: key on the head SHA, and remember the check-run id --
  the git directory beside `grain-workflow-refused` and the
  workflow-health marker is where the deployment already keeps facts of
  this kind. A tick that re-posts rather than re-using would leave a
  pull request wearing a new check every thirty seconds.
- **Never on the state branch itself.** The installed workflow runs on
  pull requests only, deliberately, and the reason in its header applies
  unchanged: grain commits to this repository on a timer, and checking
  its own export would spend a run every time a task changed state.

And the two the sketch did not have:

- **One import a tick.** The trigger is the sync tick, and that tick
  holds the lock every export needs, so a repository with several open
  settings pull requests must not turn one tick into several checks in a
  row. One a tick, with a two-minute deadline on it, bounds what a check
  can cost the export at one check -- and three open pull requests are
  still all checked inside two minutes. A pull request the tick could not
  even start on (a fetch that failed) does not spend that one, so a
  wedged pull request cannot starve the one behind it.
- **A check that could not be run is not a verdict.** `ErrCheckNotRun`
  is the difference between "this tree does not load", which is
  concluded and posted red, and "grain could not find out", which is
  concluded as nothing at all: the created check run stays `in_progress`
  and the next tick tries again with the same run. That is the blocking
  answer on purpose. A deployment that cannot run its own check must not
  thereby let settings through, and a condition that persists is one the
  queue's stall deadline takes to a person.

## 7. What was asked, and what was answered

One question, for whoever owns the account this deployment runs against:
**may grain hold a GitHub App installation credential on
`bwsalmon/grain-home`, carrying `Checks: write` along with the
contents/pull-requests/actions permissions its PAT already has?**

**Answered yes**: "let's move the checking into grain rather than
GitHub". So the code is section 6's, and the two things that answer
turned into obligations are worth naming again:

- It lands as one change, because the credential migration on its own
  removes the gate (section 2). The half of it that lives in this
  repository is here; the half that is a credential and a switch is
  `bwsalmon/grain-home`'s and the host's, and until *that* half is made
  the switch is off and nothing has changed for anybody.
- The other two answers are no longer live options. If the App turns out
  to be refused in practice, the fallback is section 1's commit statuses
  -- the same design on `Commit statuses: write`, plus a commit-status
  read in `checkRunsFor`, giving up the output body and the App identity.
  Nothing built here forecloses that: what would change is
  `cmd/grain/statecheckpost.go`'s two calls and the name the queue
  requires.

## 8. Special-casing the merge, which the answer came with

"This probably means grain needs to special case merges for the repo to
synchronize with its own commits." It does, and in two places rather than
one.

**Judging the merge.** The merge queue now knows one repository whose
rules are not GitHub's: `StateRepoGate` carries the state repository, the
name of the check this deployment posts there, and what to do when one of
its pull requests merges. Naming the check is what makes its absence
block -- section 3's second rule, and the piece without which posting the
check would be a regression. It reaches the queue as
`orchestrator.Config.StateRepoCheck`, read per cycle off the settings
file the same way `StateRepo` is, so an adopt or a flipped switch takes
effect on the next cycle rather than at the next restart.

**Taking the merge up.** A merge into the state repository is the one
merge that changes what the process doing the merging runs on. Until it
is pulled and applied, this host is running -- and, worse, exporting --
the settings it had rather than the settings it was just given, and every
export in that window is a push the remote rejects and a divergence for
`recoverDivergedStateRepo` to clear. The window was up to a full
`stateSyncInterval`; it is now the rest of the cycle, because the queue
nudges the sync loop the moment the merge lands
(`stateManager.Nudge`, a second reason `stateSyncLoop` runs a cycle).

Deliberately a nudge rather than a synchronous sync, and deliberately not
a reset of the ticker. The caller is the reconciler's own goroutine
walking a queue of pull requests, and a state sync can take as long as a
posted check does; and a merge landing a second before the tick should
add a cycle, not push the next one half a minute out.
