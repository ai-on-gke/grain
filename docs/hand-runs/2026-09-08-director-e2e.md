# Hand run: bring the director up and talk to it

Task 399, an end-to-end pass over what shipped on 2026-09-07 and -08 --
the standing console session (grain/task-415), the chat turn
(grain/task-414), the wake time (grain/task-386) and the target-repo
allowlist parking (grain/task-276) -- driven from the CLI and the REST
API against a real `grain daemon`, not from the test suite.

The headline is the first section: the director, shipped and documented
two days ago, could not answer anything at all. Nothing below was found
by reading the code; every one of them was found by typing at it.

## What was run, and against what

`grain daemon -data-dir <fresh> -sandbox-dir <fresh> -host-sandboxes
-ci=false -poll-interval 10s`, built from this branch's parent, on the
sandbox this run was given. A fresh data directory each time, so
everything below starts from the state a new deployment is in.

No agent CLI is installed there, so no run ever reaches a model: every
dispatch below ends as `setup-failed`. That is enough for all four
findings -- each is about what grain does with a task, not about what an
agent says -- and it is also how the third one was found, since a
deployment with no agent is indistinguishable from one whose director is
simply quiet.

## 1. A reply to the director was swallowed before dispatch saw it

Type into the chat (`grain comment 1 "what can you do?"`, which is the
same `POST /api/tasks/{id}/comments` the send button uses) and one cycle
later the task is back to `awaiting_reply`, with grain's own
"_Waiting for you._" under the message and **no run row at all**:

```
--- comment #2 by debian ---
What can you do?

--- comment #3 by console-session ---
_Waiting for you._
```

Replying un-parks a task by clearing
`Observation.PendingQuestionCommentID` (`ui.Client.AddComment`), and
`reconcileConsoleSessions` runs before `reconcileDispatch` in the same
cycle (`Reconcilers()`). It read "approved, not parked, not running" as
"a run ended without answering" -- which is the other thing that row can
mean -- and parked the task straight back. Dispatch then found nothing
ready. Every message, for ever.

Fixed on this branch by telling the two readings apart: a session is left
queued when a human comment is newer than both the last thing grain said
for it and the end of its last run, and parked otherwise. The control
experiment is worth writing down, because it is what said the daemon was
otherwise healthy: an ordinary task filed and approved in the same
deployment dispatched on the next cycle.

## 2. The director cannot be dispatched at all in host mode

With the reply reaching dispatch, the run failed:

```
orchestrator: acquiring a sandbox for run 1-1: orchestrator: sandbox
"1-1" asks for no network access, and a host-directory sandbox has no
network of its own to withhold
```

The director is filed `NetworkAccess: none` (`model.DefaultConsoleSessions`,
and docs/director.md's table calls it a deliberate default), and
`HostSandboxes.Acquire` refuses such a run rather than quietly handing it
the host's network -- also deliberate, and right. The two defaults
together mean the shipped director cannot run on a deployment installed
without `GRAIN_KONTUR_ENABLE`.

Neither half is wrong on its own, so this branch changes neither. What it
changes is who asks: grain is both the thing asking for the isolation and
the thing refusing it, so `reconcileConsoleSessions` now asks the sandbox
backend (`orchestrator.NetworkIsolator`) and files its own session with
the access this deployment can actually give it. Nothing is lost -- on
that backend a run has the host's network whatever the column says -- and
a kontur deployment still gets a director with none.

`tests/e2e`'s randomized cluster found the second half of that on CI, and
it is the same finding from the other end: with replies dispatching, the
director it had been filing all along was dispatched for the first time,
and the cluster fell over on exactly this refusal. Its scripted agent now
answers a task with no repo in words, which is what a session is.

The pane gained the other missing half: `ConsoleSessionPage.jsx` draws
the network select the API has always accepted (`PUT
/api/console-sessions/{id}` takes `networkAccess`, and the GET already
answered with the option list nothing drew), with a note under it in host
mode saying the setting cannot be honoured there.

## 3. A failed exchange said "_Waiting for you._"

Both failures above look identical from the chat, and identical to a
healthy session with nothing to add: the chat draws comments, and
`TaskChat.jsx` has no attempt list, no failure banner and no outcome. A
person asking the director something on a deployment where no run can
start is told, in grain's own voice, that they are the one being waited
for.

This branch parks a failed exchange on the run's own reason instead:

```
--- comment #9 by console-session ---
_That did not get an answer: this run's sandbox could not be prepared:
orchestrator: building the agent framework for run 1-3: the Antigravity
CLI (agy) is not installed ..._

_Say it again to try once more._
```

## 4. `grain settings -target-repos ""` did nothing

Narrowing the allowlist under a queued task works exactly as
grain/task-276 says: the task parks, with a comment naming the list and
what to do about it, and a reply re-parks it rather than letting it
through. Widening it again from the shell did not work at all. The
command exited 0 and printed the unchanged list back.

`splitRepoList` returned a nil slice for an empty value, and the CLI
speaks to the daemon over REST: a `*[]string` at a nil slice marshals to
`null`, and a `null` unmarshals into the pointer the server reads as "not
sent". `-default-capabilities ""` had it too. Fixed by returning an empty
slice; `parseRepoCapabilities` had been patching the same nil at its own
call site since it was written.

## Also fixed

`grain get` on a task deferred with a wake time showed a bare
"deferred". The detail pane has said "Picks itself back up on ..." since
grain/task-386, and `deferredUntil` is in the API, so the shell was the
one place that could set the date and not read it back.

## What was checked and was fine

* A closed session is not reopened, and the next cycle files a fresh task
  with a fresh conversation (docs/director.md's own rule).
* A session's capability set follows the settings row within a cycle, and
  so does its network access.
* The deferred task woke on its own at the instant `-until` named.
* Schedules: `POST /api/schedules`, the composition endpoint's
  field-by-field account of what a firing will file, and `run` filing one
  immediately.
* `grain list`'s filters, shorthands and sort; `metrics`, `failures`,
  `pause`, `repo list`, `config`.

## Left alone, deliberately

**`grain capability <session-task> <cap> attach` is silently undone.**
Attaching a capability to a standing session's task by hand reports
success and is stripped by `syncConsoleSessionTask` on the next cycle,
because a session's grants belong to its settings row. That is the right
enforcement and the wrong answer to the person: the CLI has no
console-session command at all, so there is no shell route to the thing
that would work. Whether the attach should write through to the settings
row or be refused with a message naming the pane is a judgement call,
filed rather than guessed at.

**A deferral leaves no trace once it ends.** `grain get` on a task that
woke shows only its original `proposed` transition: the transition list
is derived from the observation rather than recorded, so "was deferred
until the 14th" is gone the moment it is not true any more. Working as
built, and worth knowing when reading a task's history as an audit trail.
