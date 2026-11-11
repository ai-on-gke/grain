# Comments, closed tasks and reopening

What a person's comment on a task does depends on the task's state. This
page covers the cases, and what happens to a comment left on a closed
task (task 3803).

## Which comments reach an agent

A run sees the task's conversation in its prompt. Grain records the last
comment a run was shown as `Observation.DeliveredCommentID`. A person's
comment past that mark has not reached an agent yet. Only comments from
people count here: a human's own, or one a service posts on a human's
behalf (`model.CommentByPerson`). What agents and grain post never sends a
task anywhere.

| State | What a person's comment does |
|---|---|
| `running` | Delivered into the live session. |
| `awaiting_reply` (a question, a stopped turn, a secret or access request) | Lifts the park, and the task is queued with the comment. |
| `awaiting_reply/answered` (a run answered in a comment and opened no pull request) | Read as "continue": the task is queued for another run (`ui.Client.reopenAcknowledgeable`). This holds for anyone's comment, not only the owner's. |
| `awaiting_submit`, or on the merge queue | Nothing beyond the comment itself. A task with an open fixes pull request has its own way forward (`model.AwaitsAck`). |
| `closed` | **Nothing.** The task stays closed and no agent sees the comment. |

## Commenting on a closed task

Closing is a person's decision. A comment does not undo it, so a comment
on a closed task reaches no agent unless the task is also reopened.

- **UI.** On a closed task the comment box says "This task is closed; a
  comment won't reach an agent." It offers **Reopen and send**, which
  reopens the task and posts the comment in one action, beside **Comment
  only**. A role that may not reopen the task sees the note without the
  button.
- **API.** `POST /api/tasks/{id}/comments` accepts `"reopen": true`. It is
  checked against the caller's role the same way `POST
  /api/tasks/{id}/reopen` is, and it is refused for an agent's call. If a
  comment is left on a task that stays closed, the response has
  `"commentDelivery": "not_delivered"` and a `notice` saying so. The rest
  of the response is the task, as before.
- **CLI.** `grain comment` prints the same notice on stderr, and
  `grain comment -reopen <id> <body>` sends `reopen: true`.

## Reopening delivers what was left meanwhile

Reopening clears the closure, and the task returns to the state its
observations imply. If that state is `awaiting_reply` and a person's
comment is past the delivered mark (and past any question the task is
parked on), the reopen does what that comment would have done on an open
task: the task is queued, and the run's prompt carries the comment. The
reopen's history entry says so, for example "reopened with an unanswered
comment from atyburcy@example.com; queued".

A reopen with no such comment leaves the task where it was, for example
`awaiting_reply/answered`. A task with an open fixes pull request is never
queued by a reopen.

## A question nobody delivered

If a person's comment on an `awaiting_reply` task has still not reached
an agent, the task row and the inbox say "Unanswered question from X"
(`ui.Task.UnansweredCommentFrom`), not "Answered in a comment" with a Reply
for the owner. The liveness sub-case is
`awaiting_reply/unanswered_comment`, and its control is **Run now**.
Normally a comment like this queues a run at once, so the row only shows
this when something kept the comment from being delivered.
