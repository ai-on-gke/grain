# GitHub's rate limits, and what grain spends against them

grain is a poller. Nothing about that is going to change -- the design is
level-triggered on purpose, and a reconciler that re-asks GitHub what it
thinks is what makes a cycle recoverable from any state -- but until
2026-09-08 nothing in this repository knew what a poll costs, and nothing
knew what to do when GitHub said stop.

This is the arithmetic, the audit that produced it, and the two things
that were added.

## The two limits

**Primary.** A budget per credential per hour: 5,000 requests for a
personal access token or a GitHub App installation, 60 for an
unauthenticated caller. Every request spends one, and a spent budget
refuses everything with `403` and `x-ratelimit-remaining: 0` until the
instant in `x-ratelimit-reset`.

**Secondary.** Not a budget but a shape: no more than about 900 points a
minute (a GET is one point, a write five), no more than 100 concurrent
requests, and a separate ceiling on content-creating writes. A refusal is
`403` or `429`, usually with `Retry-After`, and GitHub's own
documentation says a client that keeps calling through one may have the
limit extended or be banned. That last sentence is the reason half of
this document exists: the worst thing a throttled client can do is carry
on, and carrying on was exactly what grain did.

The two need telling apart from 403's other meanings -- a missing
permission, SAML enforcement, an organization IP allow list -- which is
why `github.IsRateLimited` reads GitHub's own wording rather than the
status, exactly as `github.IsPermissionDenied` does for its own case. The
two predicates partition 403 between them and a test pins that they never
both answer for one refusal: a rate limit read as a missing permission
would latch `orchestrator.ChecksUnavailable` and switch auto-merge off
for the life of the process, and a missing permission read as a rate
limit would be waited out forever.

## What a deployment spends

Per reconcile cycle -- `model.Config.PollInterval`, thirty seconds by
default -- for each pull request grain is still watching (`P` below):

| call | where | per cycle |
| --- | --- | --- |
| `GetPullRequest` | `queueEntry.observe` | 1 |
| `ListCheckRuns` (or `ListWorkflowRuns`) | `checkRunsFor` | 1, plus a page |
| `GetBranchHead` | `reconcileTaskCI`, only while a task has a recorded required Grain CI run | 1 |

`GetPullRequest` is also where a merged pull request's size comes from.
GitHub returns `additions`/`deletions`/`changed_files` on this response
and on no list endpoint, and `orchestrator.recordPullRequestEvents`
records them from the read it was already making
(`docs/release-notes.md`) -- so the week's lines-changed figure costs
nothing in this table.

So the steady state is 2 requests per watched pull request per cycle,
with a third while Grain is collecting an explicitly requested full CI
run: **240 an hour each**, temporarily 360. A dozen open pull requests is
most of a personal access token's whole hourly budget spent on re-reading
JSON that has not changed.

Beside that, per live run (up to `max_workers`, six on this deployment):

- for a task without the `grain-ci` capability, `wait_for_checks` reads
  the branch head once and then the check runs
  every fifteen seconds until CI settles. grain's own CI takes about nine
  minutes, so that is roughly 36 requests a wait, and the tool's ceiling
  of an hour is 240.
- for a task with `grain-ci`, `wait_for_checks` starts and polls Grain CI
  through the daemon and makes no GitHub Checks API requests.
- a wait that ends red then reads the failing jobs' logs, up to five
  times over fifty seconds (`mcp.jobLogRetryDelays`), each attempt
  walking runs -> jobs -> logs and bounded at `maxFailedJobLogs` (4).

And on the state-repository sync loop, its own thirty-second ticker:
`OpenPullRequests` once a tick while `postCheck` is on (120 an hour), and
`WorkflowFileRuns` at most once an hour.

None of those is a bug on its own. Together, on a deployment with a
handful of open pull requests and its workers busy, they are the whole
budget -- and the ones that dominate are all polls of resources that
almost never change between two reads.

## What was wrong, and what changed

### 1. Nothing knew it was being throttled

`pkg/github` had no notion of a rate limit. A `403` came back as an
ordinary `*Error`, the reconciler that made the call logged it and
returned, and thirty seconds later the next cycle made every one of the
same calls again. An exhausted budget was therefore invisible -- it
looked like an assortment of unrelated GitHub failures -- and a secondary
limit was actively made worse, since ignoring `Retry-After` is what turns
a pause into a longer one.

`pkg/github/ratelimit.go` is the fix. A refusal that is throttling closes
a gate on the transport that made it, and the gate is asked *before the
next request is built*: while it is shut every call is refused locally
with a `*RateLimitError` naming when it opens, at no cost to anybody's
budget and with no packet leaving the host. The instant comes from
`Retry-After` where GitHub sent one, from `x-ratelimit-reset` where the
primary budget is spent, and from a one-minute default otherwise; it is
capped at an hour so a confused proxy cannot switch a deployment's GitHub
access off for a day. One journal line when it shuts, one when GitHub
answers again.

It deliberately does not *wait*. A reconcile cycle is a timer, not a
queue, and blocking inside a GitHub call would hold up every other
repository's merge queue and the tick's own dispatch to buy one read the
next tick would make anyway. The cycle is the retry. The gate only stops
grain spending calls it has already been told will fail.

The gate belongs to the transport and is keyed by credential underneath
it, so a deployment whose ladder holds one token for its own repositories
and another for somebody else's does not have the first one's exhaustion
stop the second one working.

### 2. Every poll paid full price for an unchanged answer

GitHub stamps an `ETag` on every response and does not count a `304 Not
Modified` against the primary rate limit at all. grain sent none of them
back.

`pkg/github/conditional.go` is the fix: a GET revalidates against the
ETag of the last answer to the same request, and a 304 is answered out of
a bounded LRU. Nothing is served that GitHub has not just been asked
about -- there is no freshness heuristic here, only GitHub asserting the
entity is byte-identical to the one held -- so no answer changes. The
cache key is method, path, `Accept` and a hash of the credential, because
the same path is a 200 for a token that may see the repository and a 404
for one that may not.

The headers are cached with the body, which matters more than it looks:
a 304 carries no `Link`, and every paginated read in this package walks
that header. A cached answer that dropped it would silently truncate
listings to their first page.

This is what takes the table above from "most of the budget" to nearly
nothing in the steady state: a pull request nobody has touched, a check
list that has not moved, and a branch head that has not been pushed to
all answer 304.

It is on for every transport `NewRealTransport` builds and off in the
zero value, which is the mocked-GitHub test seam (`RealTransport`'s own
doc comment): a seam whose job is to observe the requests a client makes
should observe all of them.

### 3. A 5xx was a failed operation for every caller with no next cycle

Added 2026-09-18, and the answer to a question that had been asked twice
("what retrying does `pkg/github` already do?"). Until this, the answer
was **none at all**: `RealTransport.Request` built one request, called
`client.Do` once and handed back whatever came of it, so a `502` from
whatever sits in front of the API reached the caller on the first try.
The gate above is the opposite of a retry -- it declines to spend a call
GitHub has said will fail -- and nothing else in the package asked
anything twice.

For the merge queue that was, and still is, the right shape: it
reconciles every ten seconds, so the cycle *is* the retry, and task 211
deliberately made the queue patient rather than stacking a second layer
under it. But the queue is not the only caller. `EnsurePullRequest`, the
finish path, the MCP tools an agent calls and `cmd/ghcr-prune` have no
cycle behind them, and for those a `502` is the operation failing. This
deployment's daemon log carries one every few hours.

`pkg/github/retry.go` is the fix, and it is deliberately the smallest one
that absorbs what the log actually shows:

- **GET and HEAD only, decided from the method inside the transport.**
  Every other method is a write until somebody says otherwise in
  `idempotentMethod`, so `MergePullRequest`, `CreateIssue` and every
  `POST`/`PATCH` are excluded by construction rather than by care -- a
  `502` on a write may mean the write happened. `PUT` and `DELETE` are
  excluded too: idempotent in HTTP's sense, but GitHub answers the second
  `PUT /merge` a `409` and the second branch `DELETE` a `422`, which
  would report a succeeded operation as a failed one.
- **One extra attempt, after a flat 250 ms.** An outage costs twice the
  calls, not ten times, and a daemon inside a retry ladder looks hung
  from outside. Anything not cleared by the second attempt is what the
  caller's own next cycle is for.
- **Status only.** A refused connection, a DNS failure or a timeout is
  not retried, for the same reason `IsServerError` excludes them: a
  timeout's retry doubles a wait somebody is already complaining about.
- **Never through a shut gate.** The retry re-enters the whole of
  `Request`, so a throttle that arrived from a concurrent call between
  the attempts stops the second one.

`serverErrorStatus` is the single status test behind both
`github.IsServerError` and the retry, so what a caller is told is
transient and what the transport asks again cannot drift apart.
`Stats.Retried` counts the extra requests, which is what makes the claim
above checkable: the retries taken against the 5xxs in `Failed` is how
much of GitHub's bad minute is being absorbed and what it costs.

### 4. Nobody could say who spent the hour (2026-10-26)

At 04:34 UTC on 2026-10-26 this deployment ran its credential's 5,000
an hour down to nothing and was cut off for about sixteen minutes, and
every pull request in the merge queue read as `unreadable` until the
reset. Revalidation was working. What was still spent was reads that
missed the cache, endpoints that send no ETag, reads of things that had
really changed (often because grain changed them), and every burst. The
per-endpoint counts could not say which part of grain was doing the
spending, because a branch-head read is a merge-queue check, a base-branch
CI poll and a path trigger all at once.

**Attribution (`pkg/github/budget.go`).** Every request is charged to a
*caller* label. Each reconciler's calls go under its own name, because
`RunCycle` hands every reconciler a client wrapped with
`github.WithCaller(client, r.Name)`. The UI server's calls go under `ui`
and agent tools reached through the forge go under `agent tools`.
Anything unlabelled goes under `unattributed`, so a row there is a call
site still to be named. Within a caller, calls are split by *kind*:
`head read`, `PR read`, `checks`, `review comments`, `comments`,
`compare`, `write` and so on (`kindOf`). The label travels as the
`X-Grain-Caller` header, which `RealTransport` strips before sending and
which is not part of the ETag cache key, so two callers reading the same
resource still share one cached answer. The ledger is process-wide, not
per transport, because the budget belongs to the credential and agent
tools build a transport per call.

The same ledger keeps the `X-RateLimit-*` headers for each credential
(core resource only), sampled every 5 seconds at most over a 10-minute
window. From those samples it gives:

- the spend rate per hour;
- when the remainder reaches zero at that rate, if that comes before the
  reset;
- whether the budget is **low**: under 15% left (`LowBudgetFraction`).

Going low and recovering are each logged once:

```
github: credential 1a2b3c4d has 749 of 5000 calls left this hour (under 15%); polling idle pull requests, stale branches and path triggers is cut back until it recovers
github: credential 1a2b3c4d has 4900 of 5000 calls left this hour; polling is back to normal
```

All of it is in `GET /api/metrics` under `github`. The new fields are:

- `callers`: most spent first, each with its `kinds`;
- `budget`: `remaining`, `limit`, `resetsAt`, `spendPerHour`,
  `exhaustedAt` and `low`;
- `cache`: entries, bytes, the bounds, and `evicted`.

`read_grain_metrics` has a `github` section that prints the same data,
budget and projection first.

**Adaptive polling (`pkg/github/poll.go`).** `github.Polls()` is a
schedule that decides whether a poll is due:

- **Active (CI running, just pushed, in a batch):** due on every cycle,
  exactly as before.
- **Idle:** due when its interval runs out. Each poll that sees no change
  doubles the interval, from 30 seconds (`PollBase`) up to 8 minutes
  (`PollCap`). A poll that sees a change resets it to 30 seconds.
- **After a successful write grain makes through `RESTClient`** (a
  comment, a body edit, a ref update): everything about that pull
  request or branch is due again.
- **After a merge:** everything in that repository is due again, because
  every other pull request's base has just moved.
- **On request:** `Polls().Reset(subject)` makes a subject due for a
  caller with its own reason.

Today the schedule gates four reads:

- `SyncPullRequests`' own read of each open pull request -- the pull
  request, its head and its check runs (`pkg/orchestrator/syncpoll.go`);
- the review-comment read for each finished pull request
  (`SyncReviewThreads`, `PollNormal`);
- path triggers' per-branch head read (`PollBackground`, on top of the
  existing one-minute floor);
- path triggers' default-branch read, which used to happen every cycle
  for every repository with a trigger and is now remembered between
  reads (`PollBackground`).

**Budget-aware throttling.** While the budget is low:

- `PollBackground` (path triggers, stale branches) is not polled at all;
- `PollNormal` is polled at the 8-minute cap whether active or not;
- `PollCritical` (the merge queue's head and base-branch CI) is never
  held back, because that is what the remaining budget is kept for.

**The sync reconciler's classification.** `SyncPullRequests` is the
merge state machine, so it decides "active" itself, on each entry's own
turn in the walk and from what its last read saw:

- **Critical (`PollCritical`):** the head of its lane in the merge queue.
  Read every cycle, whatever the budget.
- **Active (`PollNormal`, read every cycle):** an agent run in flight, a
  member of a merge batch, a queue repair in flight; CI unfinished or not
  yet registered at the last read, grain CI running, mergeability still
  being computed, or a PENDING verdict that is not a withdrawn one; a
  head that moved within the last 10 minutes (the first read a process
  makes counts as a move, so a restart reads everything for 10 minutes);
  a grain CI verdict on the task that changed since the last read; or a
  queue member whose lane could still merge this cycle, since reading it
  is how the walk decides that.
- **Idle (`PollNormal`):** everything else, backed off on a fingerprint
  of the state, head SHA, base, mergeable state, health and each check
  run's status and conclusion.

A queue member skipped because the budget is low closes its lane for the
cycle, as an unreadable one does, so nothing is merged past a change
nobody read. Starting grain CI for a task's branch (`wait_for_checks`,
`run_ci_job`, `retrigger_ci`, through `ui.Client.StartTaskCIRunOpt` in
the daemon) resets the task's pull requests, so an agent asking about its
checks gets a fresh read. What idling costs is lateness, bounded by
`PollCap`: a pull request merged, closed or pushed to by hand while idle
is noticed within 8 minutes rather than within a cycle.

Nothing that is not on the schedule is throttled.

**The cache's bounds.** One cycle's working set is about 30 open pull
requests × (pull request + check runs + review comments + branch head),
plus three repositories' base branches: about 150 entries per
credential. 512 entries held that with nothing to spare once single-use
reads (a compare, a file list, the check runs of a superseded head) were
mixed in, and each of those pushed out an entry a poll was about to
revalidate. The bounds are now 2,048 entries and 32 MiB, and evictions
are counted (`cache.evicted`), so whether they still hold the working
set can be checked in the metrics rather than guessed.

## What was looked at and left alone

- **The 30-second cycle, and the 15-second `wait_for_checks` poll.** Both
  are the right latency for what they do, and with revalidation both are
  now nearly free against the primary limit. Slowing them down would cost
  merge latency to save requests that no longer cost anything. The
  adaptive schedule above slows only what is idle, and leaves the cycle
  itself alone.
- **`reconcileTaskCI`'s `GetBranchHead` while collecting a required run,
  which duplicates a head SHA `queueEntry.observe` reads in the same
  cycle.** Hoisting it would mean
  carrying one reconciler's read into another's, and the reconcilers are
  deliberately independent (`RunCycle`). It revalidates to a 304 now, so
  the duplication costs a round trip and nothing else.
- **The unauthenticated path.** `github.NewClient` accepts a nil token
  source, and `credentialTokenSource.TokenFor` returns nil for a repo the
  ladder does not match -- which is a documented, deliberate shape for a
  public repository, but is also a 60-an-hour budget that a polling
  deployment exhausts in under a minute. Nothing here changes it; the
  gate at least makes it visible as throttling rather than as an
  assortment of 403s.
- **An exponential ladder, and a retry count a deployment can raise.**
  Both are levers somebody reaches for during an outage, which is exactly
  when multiplying grain's call volume is the wrong move. Two attempts is
  a constant in `retry.go` for that reason.
- **Retrying a write that has a natural idempotency key.** Opening a pull
  request for a branch that already has one, or adding a label that is
  already there, could in principle be re-sent and reconciled by reading
  the state back. That is a per-call-site decision about what GitHub
  answers on the second attempt, and it belongs at the call site rather
  than in a transport that cannot see it.
- **Git traffic.** The state repository is fetched and pushed on its own
  thirty-second ticker and every dispatched run clones its target. Both
  go through the git host rather than the REST API and are not measured
  by the limits above.
