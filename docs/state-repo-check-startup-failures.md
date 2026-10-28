# What should a deployment do when its state repository cannot run the check grain installs?

**Status: decided and built** (grain/task-228). The decision is the first
of the three options below -- grain watches the check it installed and
says, where an operator looks, when every run of it dies before a job
starts. It does not remove the check, and it does not take over running
it. The other two are written down here because both are real designs,
both were rejected for reasons that are about permissions and about who
gets to make a call rather than about effort, and the next person to
propose one should start from why this one did not.

## The condition

grain installs `.github/workflows/grain-state-check.yml` in its own state
repository (`pkg/staterepo/format.go`, `installWorkflow`). It runs `grain
state check` on pull requests: the same import the deployment does at
start, against a throwaway database, so a change to what grain runs that
would not load is red before it is merged rather than after.

GitHub can conclude a workflow run before any job of it starts.
`startup_failure` is its own word for that, and the causes belong to the
repository rather than to the change: Actions disabled or restricted for
it, a runner it may not use, a workflow file GitHub cannot parse at that
commit, or -- for a private repository -- an account with no Actions
minutes left or one that has reached its spending limit. The run's page
draws its jobs with no steps under them, because no step of any of them
was reached, and there is no log anywhere to read.

`bwsalmon/grain-home` is in that state and has been since the file was
installed. Its check has never once passed on any tree, so every settings
pull request the deployment will ever make is gated on a red X that no
commit clears. Two agent runs have now been spent on it: grain/task-134
established what was happening, and grain/task-201 proved the file grain
writes is not the fault by running the same rendering in a repository
whose Actions work, and made the file's own header say what a no-steps
run means. Neither could make the *deployment* know.

That is the gap. From every surface grain has -- the State pane, `grain
state status`, the journal -- a repository whose check passes on every
change and one whose check has never started look exactly alike:
installed, committed, syncing, nothing in error. The one surface that
does say something, the merge queue's `escalateStartupFailure`
(`pkg/orchestrator/sync.go`), is about one task's pull request and speaks
to whoever reads that task. Nobody was telling the operator that the
deployment's own gate had never worked.

## What was decided: grain watches and says so

`pkg/staterepo/workflowhealth.go` asks GitHub, once an hour on the sync
tick, how the workflow it installed has been running --
`Config.WorkflowRuns`, wired in `cmd/grain/statechecks.go` to a read of
`/repos/{owner}/{repo}/actions/workflows/{file}/runs`, because
`pkg/staterepo` has no GitHub credential and should not grow one
(`Config.AnnounceResets` is the same seam for the same reason). The
verdict -- every finished run read died at startup, and there is at least
one -- goes into a marker in the git directory beside
`grain-workflow-refused`, and comes back out on the State pane and in
`grain state status`, with the run's own page, because GitHub's actual
reason is written there and in no API answer grain gets.

The verdict is deliberately conservative in both directions. A repository
whose check has not run yet says nothing: the workflow is triggered by
pull requests, so "no runs" is the ordinary state of a state repository
the week it is adopted. One run that got a job started -- passed, failed,
however it ended -- clears it: what is being reported is a repository
that cannot run Actions at all, and that is no longer true the moment one
job runs. A read that fails leaves the previous verdict alone, since this
is a condition an operator fixes by paying a bill or changing a
repository's settings, and a warning that came and went with the network
is one nobody can act on.

Nothing else changes. grain goes on installing the check, goes on
syncing, and does not touch the gate.

## What was rejected here and has since been built: grain runs the check itself

The deployment holds everything the check needs -- the binary, the
schema, and a GitHub credential -- so it could run `grain state check`
against a state-repo pull request itself and post the result through the
Checks API. That removes the dependency on hosted-runner minutes in a
private repository entirely, and it is the only version of this that
makes the gate work again without the operator paying GitHub or standing
up a self-hosted runner.

It was not built here, for three reasons, and none of them is that it is
hard:

- It needs `checks: write` on the App, which this deployment may not
  have. A design whose first step is "grant the automation a permission
  it has never held" is a decision for whoever owns the account, not one
  to arrive as a fait accompli inside a change that was meant to add a
  warning. (grain/task-288 went and asked what that permission actually
  is, and it is a larger ask than this sentence assumed: only a GitHub
  App may create a check run at all, and this deployment's documented
  credential is a fine-grained PAT. See
  `docs/state-repo-check-posted-by-grain.md`, which is now where this
  option is designed and where the question waits.)
- It makes grain the author of the check on its own settings changes.
  Today the thing that validates a change to what grain runs is
  independent of grain: it is GitHub, running a published image, from a
  file a reviewer can read. A deployment that marks its own homework is a
  weaker gate than one somebody else marks, and it fails silently in
  exactly the case that matters -- a deployment too broken to run the
  check is also too broken to report that it did not.
- It is strictly more work on top of this, not instead of it. A
  deployment that posts its own check still wants to know that the
  Actions one is dead, because that is what decides whether to post at
  all.

Worth doing, probably, once somebody has decided the first point.
Somebody has: the answer was yes, move the checking into grain, and
grain/task-288 built it --
`docs/state-repo-check-posted-by-grain.md` is the design and the record
of the decision, `pkg/staterepo/postcheck.go` is the code, and it is off
on every deployment until an operator sets `"postCheck": true` and gives
the host a GitHub App credential, because no personal access token may
create a check run. The three objections above stand as written and are
answered there rather than waved away -- in particular the second, whose
real form turned out not to be "self-marked homework is weaker" but "a
missing check reads as a clean pull request", which is why the merge
queue now refuses to call a pull request against the state repository
clean without that check by name.

That design also carries one measurement that bears on this document
too. A run GitHub concludes `startup_failure` leaves *no check run
behind at all*, so the red X this
document is about reaches grain's merge queue only through the Actions
fallback a checks-blind credential falls back to. A deployment that
moved to a checks-capable credential for any other reason would stop
seeing it -- and would start auto-merging settings pull requests
unvalidated, with the X still drawn on them on github.com.

## What was rejected: grain retires the file it wrote

grain could delete the workflow when it can see the check has never
started a job, with a commit message saying why and how to get it back.
Self-healing, and it unblocks the deployment's settings pull requests
immediately.

This one is a no rather than a not-yet. The workflow is grain's only
pre-merge validation of a change to what the deployment runs, and
removing it on the strength of an inference from an API read gets the
trade exactly backwards: the inference can be wrong (a credential that
suddenly cannot read Actions, a repository between runners, a spending
limit raised the minute after grain looked), and the cost of being wrong
is that a settings change which would not load merges unchecked, on the
deployment, in front of whoever is on call. The cost of being right is
only that pull requests stay blocked until a person acts.

It also throws away the explanation. The file's own header is where the
remedies are written down for whoever opens that run's page, so a grain
that deletes the file deletes the sentence that tells the operator what
to do -- and then reinstalls the file on the next sync unless a second
marker is invented to stop it, which is a deployment arguing with itself
in its own git history.

The operator can still have this outcome, deliberately and in one line:
`"noWorkflow": true` in `state-repo.json` stops grain offering the check
at all. That switch already exists, every surface added here names it,
and choosing it is a person deciding to trade the validation for the
unblocked queue. That is the right person to be making that trade.

## What an operator can actually do

None of these is something an agent or the deployment can do, which is
why all four are named wherever this condition is reported:

- raise the account's Actions spending limit, or turn Actions back on for
  the repository;
- point `runs-on` at a runner the repository may use -- a self-hosted one
  costs a private repository no minutes;
- set `"noWorkflow": true` in the deployment's `state-repo.json`, which
  unblocks every pull request against the repository at the cost of the
  validation;
- or, since grain/task-288, set `"postCheck": true` beside it and give
  the host a GitHub App credential with Checks write, which has the
  deployment run the check itself and post it -- the one remedy that
  costs the validation nothing, and the only one that needs a new
  credential (`docs/state-repo-check-posted-by-grain.md`);
- or read GitHub's own reason on the run's page, which the pane and
  `grain state status` now link to, because it is the only place the
  cause is actually written down.

## The same condition on a repository grain does not own

Everything above is about the state repository, whose workflow file grain
installs and whose settings file has a switch in it. A *watched*
repository in the same state has none of that -- no grain-owned workflow,
no pane, no `noWorkflow` -- and it is answered differently, by reading
GitHub at the moment the merge queue escalates rather than by watching on
a timer. See
[repository-checks-never-started.md](repository-checks-never-started.md),
which also says why neither half keeps a count of the pull requests the
condition has cost.
