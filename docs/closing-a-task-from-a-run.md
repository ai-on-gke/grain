# Closing a task from a run

**Status: decided — grain is not building a general `close_task`.**
Decided 21 Sep 2026 by grain/task-528, the follow-up grain/task-473 filed
when it landed `supersedes` on `propose_task`. This is the reasoning, so
that reopening the question costs a reader one file rather than a
re-derivation, and so that whoever reopens it knows what would have to be
different.

## The question

`propose_task`'s `supersedes` argument closes exactly one kind of task:
one being replaced, in the act of filing its replacement. That was built
for a real pattern — a chat session refining a design cannot edit a task,
so five revisions are five tasks and four of them need closing — and it
covers nothing else.

Two tasks from the same sitting were not covered, and each cost an
operator a manual close:

- **462**, a proposal an operator made redundant by ticking a setting in
  the UI. There is no replacement to point at; the work simply stopped
  being work.
- **459**, a duplicate filed by a *different* task's run. The task it
  duplicates already exists, so there is no filing act to hang the close
  on, and the direction is wrong besides: `supersedes` closes the older
  task when the newer is filed, and a duplicate wants the *newer* one
  closed.

So: should there be a `close_task(id, reason)` — its own capability, the
same never-dispatched bound, the same visible record?

## What already closes a task, and what they have in common

Four writers of `Observation.ClosedAt` in the tree as of this decision:

| Closer | What it closes on |
| --- | --- |
| `ui.Client.setClosed` (`pkg/ui/client.go`) | a human clicked Close |
| `orchestrator.SyncPullRequests` (`pkg/orchestrator/sync.go`) | the task's pull request merged or was closed on GitHub |
| `orchestrator.closeFinishedReview` (`pkg/orchestrator/review.go`) | a scheduled review task finished filing its own findings |
| `model.Store.Supersede` (`pkg/model/supersede.go`) | a replacement for this task was filed (and the tasks waiting on it now wait on the replacement) |
| `orchestrator.finishNothingNeeded` (`pkg/orchestrator/finish.go`) | the task's own run ended on `nothing_needed` (added later by grain/task-1549; see below) |

Every one of them closes on **a fact grain itself observed**: a click, a
merge, a run's own completion, a filing. None of them closes on an
agent's *judgement about a task it did not write*.

A general `close_task` would be the first, and that — not the blast
radius — is the thing that makes it a different object. `SupersedeRefusal`
bounds the damage well: nothing with an attempt, a live run or an open
pull request can go, so no sandbox, branch or pull request is ever thrown
away, and `Reopen` exists. What it cannot bound is the judgement. It
cannot tell a duplicate from a queued task an operator filed deliberately
and a passing run misread, and the caller best placed to make that call is
the one least likely to be making it: a run in some other task, working
from a title and a body it skimmed. `supersedes`' caller has at least
read both tasks, because it is writing one of them.

## What the two cases actually want

Neither of them wants a close tool, on inspection.

**462 — redundant after an operator changed a setting.** The person who
made it redundant was in the UI, in that session, with the Close button on
the same page. For a run to close it instead, the run would first have to
notice that a setting changed and infer which queued proposals that
retired — a much larger and much less reliable inference than the close
itself. Automating the click is not the hard part and not the valuable
part.

**459 — a duplicate filed by another run.** A duplicate is a proposal that
should not have been filed. The cheap intervention is at `propose_task`
time — tell the filing run what its proposal looks like a duplicate of,
before it is filed — which closes nothing, needs no capability, and puts
the judgement in front of the one run that has both descriptions in
context. If that were built and duplicates still got through, a close tool
would be tidying up after a gap that has a better fix upstream.

## The evidence that was supposed to decide this, and why it is absent

grain/task-473 was explicit that usage should decide it: how often a run
reaches for `supersedes`, finds it does not fit, and asks a human to close
something instead. That count does not exist yet and could not be read
here.

- The narrow form merged to `main` at 03:25 on 21 Sep 2026 and this
  decision was taken the same afternoon — about ten hours of deployed
  life. (It *is* deployed: `propose_task` as served to this run carries
  the `supersedes` argument.) Ten hours is not a usage record.
- It is not readable from a task sandbox in any case. The count lives in
  task bodies and comments in the deployment's own database; `grain/config`
  exports settings and memory only and has no task tables, and reading the
  record at all needs a run holding `self-debug`.

The honest position is therefore that this decision is made on the
structure of the thing rather than on a count — and the structural
argument above is the part a count would not have changed. A count would
change the *urgency*, not the objection.

## The decision

**No.** Not "not yet, pending a design": the two cases that asked for it
are each better answered somewhere other than a close tool, and a general
close would be the first thing in grain that closes a task on an agent's
opinion rather than on an observed fact. Two manual closes in one sitting
is the friction argument the narrow form was built on, and it was the
right argument there because the pattern recurs mechanically. These two do
not — they are one-offs of two different kinds.

## Terms, if it is ever overturned

Carried from grain/task-473 and not to be re-litigated:

- **Its own capability**, granted deliberately by an operator, not
  something every run holds. That is a `grain/config` change as well as a
  code one.
- **Never-dispatched only**, at minimum: no attempts, no live run, no open
  pull request. `model.SupersedeRefusal` is that bound already written
  down with a sentence per clause; a close tool reuses it rather than
  restating it.
- **Never outside the caller's own repository scope**, the same scope
  `propose_task` enforces.
- **Recorded visibly**: `Observation.ClosedAt` plus a comment on the closed
  task's own conversation naming who closed it and why, attributed to
  grain on behalf of the asking task (`model.SupersedeComment` is the
  pattern).
- **No silent refusal.** The tool call is told in its own turn and the
  reason lands where a human reads it. A run that believes a close
  happened when it did not stops telling anybody it needs doing.

## What would reopen it

A count, from a run holding `self-debug`, over a month or more of the
narrow form in use: runs that reached for `supersedes` and were refused
for a reason other than the never-dispatched bound, plus runs that asked a
human to close something in a `comment_on_issue` note. If that is a
handful over a month, this decision stands and the manual closes are the
right price. If it is routine, and the cases are duplicates, build the
duplicate warning at `propose_task` time first and count again — a close
tool is still the last resort, not the first.

## Addendum: a run closing its *own* task (`nothing_needed`)

grain/task-1549 added the one close this decision leaves room for: a run's
judgement about **the task it was dispatched for**, not about some other
task. A run that, having looked, finds the task needs no change and no
report anybody has to act on calls `nothing_needed` with a reason, and
ends its run the way `comment_on_issue` does.

`orchestrator.ProcessResult` posts the reason as the task's closing
comment and records the run's outcome as `nothing_needed`
(`model.EndingNothingNeeded` — counted apart from `succeeded` and
`no_action` in the metrics, and treated as a success by the failure
streak). What happens to the task is the task's own setting,
`Task.NothingNeededAction`:

- `close` (the default; an empty value means the same): `completed_at`
  and `closed_at` are both stamped and grain notes the close, so the task
  goes straight to `closed` and never sits in `awaiting_ack`.
- `ack`: the task completes into `awaiting_ack` exactly as a closing
  comment would.

The things that file tasks for somebody carry the same setting
(grain/task-1599), copied onto every task they file the way network access
is: a template (`Template.NothingNeededAction`, `nothing-needed:` in a
`.grain/templates` file), an inline schedule
(`Schedule.NothingNeededAction`; a template-backed schedule takes its
template's) and a suite, through its templates. An empty value keeps
meaning `close` everywhere.

**Pushed commits and questions win.** `nothing_needed` never discards
work. If the same run asked a question (or is waiting on a chat reply),
the task parks on it; if it pushed a branch, the ordinary pull request
flow runs. Either way the reason is still posted as a comment beside it,
and the run keeps the outcome it would have had without the call.
