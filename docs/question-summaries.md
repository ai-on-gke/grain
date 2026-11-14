# Question summaries

Every question an agent asks a person carries a **one-line summary** as
well as the full question. Wherever there is room for only one line -- the
inbox, the task list, a notification -- grain shows the summary and keeps
the full question one click away.

## For agents: what to put in `summary`

These tools ask a person something and park the task (or, for `chat`,
wait in the same run), and each takes a `summary`:

| Tool | What the summary says |
| --- | --- |
| `ask_question` | the decision or input the question needs |
| `request_secret` | what has to be set and why ("Set stripe-api-key so the deploy script can authenticate") |
| `request_access` | what access is needed and why ("Grant write to acme/lib so the fix lands in both repos") |
| `chat` (live sessions) | what this message needs from the person |
| `api_post_task_message` / `POST /api/tasks/{id}/messages` | the same, as the `summary` field of the body |

`comment_on_issue` also takes an optional `summary` for a closing note or
report that needs no action from a person (when a run *does* need a
person to act, it ends with `ask_question` instead). A closing note whose
text asks a person to set, grant or run something and omits `summary` is
turned back once with an error asking the run to switch to `ask_question`
or supply a summary; the same note sent again is recorded as written. A
closing note without `summary` never has one derived from its opening.

A summary is **one line of at most 100 characters**. It names the
decision, not the fact that there is a question:

- Good: "Keep the v1 API alongside v2, or remove it?", "Pick the base
  branch: main or release-1.4", "Approve dropping the legacy importer?"
- Bad: "I have a question", "Question about the API", "Need input".

A summary with a newline in it, or longer than 100 characters, is refused
by the tool (or answered with a 400 by the API) so the agent can fix it.
The parameter is required in every question tool's schema (and optional on
`comment_on_issue`), but optional on the
wire: a run dispatched before it existed, or an older API client, still
works, and for a question grain derives a summary from the question's first sentence (or
first line), cut to fit. Every fallback is logged
(`question summary: ... gave none; derived ...`) and counted
(`model.QuestionSummaryFallbacks`), so missing summaries are visible.

## Where it is stored and returned

The summary is stored on the comment (`task_comment.summary`,
`model.Comment.Summary`, schema 57). Like every other column it travels
with the state repository's export and import of `task_comment`.

The API returns it in three places:

- `summary` on each comment in `GET /api/tasks/{id}` (and on the message
  `POST /api/tasks/{id}/messages` answers with) -- set on the questions
  and closing notes (`comment_on_issue`) where an agent gave one, absent
  on everything else and on questions stored before summaries existed.
- `questionSummary` on the task itself, in both `GET /api/tasks` and
  `GET /api/tasks/{id}`, while the task is `awaiting_reply` on a question:
  the summary of the question it is parked on, derived from the body for
  a question stored without one. Absent for a stopped turn.
- `noteSummary` on the task itself, in both `GET /api/tasks` and
  `GET /api/tasks/{id}`: the `summary` the agent gave the newest
  non-handover comment (`Store.NoteSummary`), never derived from the
  comment's body.

## In the UI

- **Inbox.** A parked question is shown by its summary alone, as a button
  with a chevron and "Show question" (and a task resting on a summarized
  closing note shows `noteSummary` with its body behind the expander; one
  with no summary still says "Answered in a comment"). Activating it (click, Enter or
  Space) reveals the full markdown and its attachments in place;
  the button carries `aria-expanded` and `aria-controls`, so its state is
  available to keyboard and screen-reader users. Questions start
  collapsed.
- **Task list.** A row in `awaiting_reply` on a question puts
  `questionSummary` on its status line (and one resting on a summarized
  closing note puts `noteSummary` there), cut with an ellipsis when the row runs out of
  width; hovering shows the whole line. The inbox's own rows leave it off
  their status line, since the summary is already shown under them.
- **The task's own pane.** The pending question, and the question in the
  conversation, lead with the summary as a bold first line, with the full
  text below.
- **Notifications.** A browser notification for a question uses the
  summary as its title, with the task number and title as its body.
