# Leaving the GitHub API

grain's control loop asks GitHub questions whose answers are already in
git. That is what put this deployment into rate limiting on 2026-09-08
(docs/github-api-rate-limits.md has the arithmetic), and the throttle is
the symptom rather than the thing worth fixing: a merge queue that cannot
decide anything while a third party is refusing to answer is a merge
queue with a dependency it does not need.

This is the plan for removing it. It is deliberately not a plan for
leaving GitHub -- humans keep reviewing on github.com, and nothing about
their day changes.

## The shape

One direction, one canonical remote.

```
sandboxes ──▶ git proxy ──▶ bare repos on the daemon host  (canonical)
                                   │
grain reads refs ──────────────────┘   git for-each-ref, local, free
grain CI ─────────────────────────▶ the verdict
git merge-tree ───────────────────▶ mergeable / conflicted
merge ────────────────────────────▶ a local commit, a ref moved
                                   │
                                   └── push ──▶ GitHub ──▶ humans (a pull
                                                    request per task)
                                                └──▶ Actions ── POST ──▶ grain
```

What makes this tractable is that it is one-directional, and what makes
it one-directional is a fact about how this deployment is already
configured: `approved_by_default` and `auto_merge_by_default` are both
on, so grain already merges on its own judgement. It has never read a
human decision back off a pull request. `ListReviewComments` has no
non-test caller in this repository, and never has -- grain *posts* a
draft review (`orchestrator/reviewfeedback.go`) and reads nothing.

So GitHub is downstream: a place branches are pushed to and pull requests
are opened on, for people and for the checks that cannot run anywhere
else. Nothing grain decides depends on reading it.

The one thing that does flow back is CI verdicts, and it flows by being
*pushed* (an Actions job that reports to grain) rather than polled. That
inversion is the whole of what stops the rate limiting; everything else
here is what stops grain needing the API at all.

## Why not the alternatives

**A read-through mirror of GitHub, keeping GitHub canonical.** The first
proposal, and wrong: it is bespoke caching infrastructure whose entire
purpose is to make a poll cheap, with a lifecycle to own (pruning,
corruption, one writer against many readers) and staleness that becomes a
correctness property the moment merge decisions read from it. Hosting git
is a solved problem. If refs have to live somewhere grain trusts, they
should live somewhere that is simply *theirs*, not in a cache pretending.

**Self-hosted Forgejo or GitLab.** Both remove the rate limit, neither
removes the poll -- grain would still be an HTTP client asking a vendor
for facts, just an unmetered one. Both cost a reimplementation of
`github.Client` (33 methods; GitLab's merge-request model would also need
`healthFrom` and `refreshStaleHead` re-derived from scratch), a repo
migration, and answers for every App-shaped thing here:
`pkg/capability/githubsandbox`, `pkg/capability/githubtoken`,
`gitproxy/apptoken.go`, `cmd/grain/controller.go`'s App-manifest
bootstrap, and `postCheck`'s check-run posting. GitLab additionally does
not fit: `terraform/gcp` defaults to `n2-standard-2` (2 vCPU, 8 GB),
already shared with six sandboxes at 3 CPU and 3 GB each.

The deciding argument is that the human interface is not worth owning.
Both options are, in the end, ways of buying a review UI -- and github.com
already provides one, for free, that everybody involved already knows.

**`git http-backend` behind the proxy, rather than gitolite or a forge.**
gitolite's value is access control, and `pkg/gitproxy/authorize.go`
already does per-sandbox tokens, per-task scope, default-deny and an
audit log. Running gitolite would mean two authorization systems with
one opportunity to disagree. `Forwarder` is a one-method interface, so a
local backend swaps in with `core.go`'s decision logic untouched.

## What replaces each call

Already dead -- no non-test caller, v1 leftovers from when input was a
GitHub issue. Delete before porting anything: `ListIssues`, `GetIssue`,
`CreateIssue`, `AddLabel`, `RemoveLabel`, `CloseIssue`, `ReopenIssue`,
`UpdateIssue`.

| today | becomes |
| --- | --- |
| `GetBranchHead`, `BranchExists` | `git for-each-ref` on the local bare repo |
| `CompareCommits` | `git rev-list` / `git log` |
| `detail.Mergeable`, `MergeableState` | `git merge-tree --write-tree` (git ≥ 2.38) |
| `MergePullRequest` | a local merge commit, then the ref moved |
| `MergeBranch` (staleness refresh) | the same merge, the other way round |
| `CreateBranch`, `UpdateBranch` | `git update-ref` |
| `ListCheckRuns`, `ListWorkflowRuns` | grain CI, plus the reported Actions verdict |
| `FailedJobLogs`, `ActionsState`, `RecentRunStarts` | grain CI's own run records |

What stays on the API, and is fine: `CreatePullRequest`,
`ClosePullRequest`, the title/body updates and `CreateComment`. All are
writes, once per task, not polls. They cost nothing worth counting.

The fiddly one is `refreshStaleHead`. Its design leans specifically on
what GitHub's merges endpoint returns -- 201 merged, 204 nothing was
behind, 409 a real conflict -- and its own doc comment explains why that
three-way answer is what the queue asks for. `git merge-tree` answers the
same question, but the mapping wants deriving rather than transcribing.

## The CI question, which is the real blocker

`.grain/workflows/tests.yml` originally ran two jobs, `go` and `frontend`.
A pull request here gets twelve checks. So a queue that merged on grain CI
alone would have been merging on 2 of 12, past "container and installer"
and "sandbox guest image" -- the two that break the deployment itself.

Two things about that turned out not to be true, and both help:

**A CI sandbox is not network-free.** `ciSandboxes.Acquire`
(cmd/grain/ci.go) explicitly requests `NetworkOpen`. That matches a
GitHub-hosted runner and lets repository checks fetch fixed dependencies,
browser assets and validation tools regardless of the deployment's default
for ordinary agent sandboxes.

**Network-free was never the security property.** `ci_hostile_test.go`
says so in as many words: "Reachability is the deployment's business (a
sandbox that reaches no network at all is a firewall, not this
repository) -- so this is reported and not asserted." What it does assert
is that a job holds no credential, that the daemon's environment does not
reach it, and that it cannot reach the host behind the proxy. Those are
the properties that make an in-repo workflow file safe to let every agent
rewrite, and none of them is about egress.

The old `.grain/workflows/tests.yml` header ("A grain sandbox reaches
nothing but the git proxy, so nothing here may download") therefore
described an assumption the adapter did not enforce. The workflow and the
adapter now state the open-network contract directly.

So the split is not about networking. It is about credentials and about
what the guest image carries:

- **Moved to grain CI** -- ui e2e, real KVM sandboxes, granule on a
  real VM, container and installer, workflows, shell scripts, terraform.
  Nine of twelve. The guest image carries terraform and shellcheck;
  actionlint and Playwright assets may be downloaded by their jobs.
  KVM and docker are already there. ("container and installer" has since
  been taken back out of `.grain/workflows/tests.yml` in grain/task-158
  -- it worked, but it set the wall clock of every check-in -- and is to
  be triaged and re-added; it still runs on GitHub meanwhile. "real KVM
  sandboxes" came out of the same file in grain/task-427, for a reason
  that was about the guest cache rather than about what a sandbox can
  do: with no `guest:inputs-<key>` published for the commit under test,
  the job builds a ~1GB guest inline in a nested VM, which by 2026-09-20
  was failing on every branch and on main. It is back in grain/task-497,
  because what made that fatal is fixed rather than because the cache
  got warmer: the resolve step declares `infrastructure: true`
  (`ci.Step.Infrastructure`), so a guest the sandbox cannot get errors
  the job instead of failing it and runs on its own clock. An errored
  job is still not a green one, so the publisher still matters.)
- **Cannot move** -- the three image jobs in `build-artifacts.yml`. They
  push to ghcr.io, which needs a credential, and a CI job holding a
  credential is exactly the property `.grain/README.md` and
  `ci_hostile_test.go` exist to keep. These stay on GitHub.

Which is why the reporting job below is not a transitional measure. Three
checks live on GitHub permanently and grain has to hear about them.

## Build order

1. **The aggregating job, and the receiver it reports to.** A final job
   in `.github/workflows/tests.yml`, `needs:` every other, POSTing commit
   and conclusion. Do this first and on its own: it removes the polling
   that caused the throttling, changes nothing about hosting, and is
   revertible in one commit. The receiver is its own process rather than
   a route on the daemon -- it is the one public-facing thing this
   deployment will have and should not share a process with every
   credential grain holds -- and it is the first piece of the forge to
   live outside the daemon, which is why docs/three-pieces.md draws that
   boundary before this is built.
2. **Stand up the bare repos.** `git clone --mirror` from GitHub onto the
   daemon host. GitHub keeps every ref, so it is also the backup.
3. **A local `Forwarder`.** Sandboxes clone from local disk. Also a real
   speedup: six workers currently each clone from github.com per run.
4. **Push to GitHub on the finish path.** The task branch when the pull
   request is opened, `main` after each merge -- in
   `finishWithPullRequest`, which already has somewhere for a GitHub
   failure to go.
5. **Local merge decisions.** Refs, mergeability, the merge itself.
6. **Retire what is left.** The dead issue methods first, then the checks
   and Actions reads.

Steps 1 and 6 are worth doing whatever happens to 2--5.

## What is deliberately not done

- **grain-home stays on GitHub**, at least at first. It is low volume,
  and moving it means moving `postCheck`'s check-run posting, which only
  a GitHub App may do.
- **No human interface is built.** No diff view, no review comments in
  grain's UI. That is the decision this whole plan rests on, and
  reversing it is what would make a forge worth running.
- **GitHub is not made optional.** A push to it that fails leaves people
  looking at a stale window while grain merges on regardless, so it has
  to be a visible, retried failure -- not a logged one. Tolerating a
  GitHub outage is a further step and this plan does not take it.
