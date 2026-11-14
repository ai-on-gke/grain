# Integration Events

**Owner:** @bwsalmon

**The core rule:** Grain records every task state transition, new comment or question, and per-task CI verdict in a bounded, monotonically ordered `integration_event` log and serves it over `GET /api/events` (SSE and long-poll JSON), filtered to the repositories the caller's `tasks:read` permission reaches and carrying only identifiers, summaries, external references, and actor attribution — never secrets, raw comment bodies, or transcripts.

## 1. Survey of Existing Surfaces

Before adding an integration surface, we surveyed every notification, streaming, webhook, and trigger mechanism in grain:

| Surface | Location | What it does today | Why it does not fit integrations |
| --- | --- | --- | --- |
| Store notifications & inbox | `pkg/model/notification.go`, `pkg/model/notification_wake.go`, `GET /api/notifications`, `GET /api/inbox` | Stores operator escalations deduplicated by `subject` (`RaiseNotification`) and wakes subscribed durable tasks (`WakeNotificationSubscribers`). `GET /api/inbox` snapshots current open questions, secret requests, held merges, warnings, and proposals. | Pull-only snapshots with no cursor or stream; notifications record escalations rather than normal task lifecycle transitions, comments, or CI verdicts. |
| Browser UI & restart watch | `ui/src`, `pkg/ui/tasks.go`, `pkg/ui/restartwatch.go`, `pkg/ui/compress.go` | The UI uses no SSE or WebSockets; it polls `GET /api/tasks` every 3 s with `ETag` / `If-None-Match` (`Store.ChangeToken`) and holds a 25 s long-poll on `GET /api/restart-watch`. `compressResponse` already bypasses gzip for `text/event-stream`, and `configRefusalWriter` and `statusRecorder` already forward `Flush()` and `Unwrap()`. | `GET /api/tasks` returns full current state rather than discrete transitions (missing intermediate states between polls), and `GET /api/restart-watch` watches only daemon restarts. |
| Explicit-task wait | `pkg/ui/external_cli.go` (`GET /api/tasks/wait`) | Long-polls `Store.Changed()` for up to 60 s until named tasks reach `closed`, `merged`, `awaiting-reply`, `ci-done`, or `any`. | Requires the caller to name explicit task IDs (`?ids=...`, max 100), returns synthesized current state rather than an ordered event log, and cannot watch a repository or deployment for newly filed or transitioning tasks. |
| Pipeline change feed | `pkg/ui/pipeline_changes.go` (`GET /api/pipeline/changes`) | Pull-only `(at, seq)` cursor feed synthesized retroactively from current timestamps on `task`, `task_run`, `task_event`, `base_branch_ci`, and `merge_batch_history`. | Overwritten columns (`approved_at`, `deferred_at`, `pending_question_comment_id`, `ci_*`) lose intermediate transitions (`proposed -> queued`, `awaiting_reply -> queued`, `deferred -> queued`); omits new comments/questions, `awaiting_reply` question summaries, per-task CI verdicts, external references, and non-close transition actors; has no SSE or long-poll transport. |
| GitHub webhooks & triggers | `pkg/ghwebhook/server.go`, `pkg/orchestrator/githubevents.go`, `.grain/triggers` | `ghwebhook` receives inbound `POST /webhooks/github` deliveries into a SQLite spool drained over an internal 25 s long-poll (`GET /v1/events`). Schedules and `.grain/triggers` react to cron expressions, git refs, pushes, and PR changes by filing tasks or CI runs inside grain. | Strictly inbound. Grain has no outbound webhook dispatcher, retry queue, signing secret store, or event delivery log. |

## 2. Recommendation: Authenticated Event Stream (`GET /api/events`)

Between **(a) an authenticated event stream (`GET /api/events`)** with SSE and a long-poll JSON fallback and **(b) deployment-configured outbound webhooks**, we recommend and implement **(a) `GET /api/events`**:

1. **Same per-caller authorization model as the rest of the API.** Callers authenticate with their existing credentials (IAP / verified identity checked against `-ui-allowed-principals` and `allowed_principal` RBAC roles, scoped external-agent bearer tokens, Acting-Task grants, or the deployment access code). Every event is filtered per caller by `tasks:read` repository access. Outbound webhooks configured per deployment would bypass per-caller RBAC unless paired with a parallel per-subscription authorization and secret-management model.
2. **Zero deployment configuration and NAT-safe topology.** Planning boards, chat integrations, and CLI bridges connect outbound to grain over HTTPS with no inbound firewall holes, public webhook endpoints, HMAC secret rotation, or dead-letter management in Settings.
3. **Independent consumer cursors and lossless replay.** Each integration tracks its own monotonic sequence cursor (`?since=<cursor>` or SSE `Last-Event-ID`) and replays missed transitions after a reconnect without head-of-line blocking across consumers.
4. **Works everywhere HTTP works.** Long-lived clients use SSE (`Accept: text/event-stream`), while environments where proxies buffer streaming responses or run short-lived workers use the JSON poll / long-poll fallback (`GET /api/events?since=<cursor>&wait=<seconds>`).

## 3. Event Model and Payload Contract

### 3.1 Durable Bounded Event Log (`integration_event`)

Events are appended inside the same SQLite / Postgres write transaction (`Store.write`) that mutates task state, adds a comment, or records a CI verdict, immediately before `Store.signalChanged()` wakes waiting stream and long-poll readers.

- **Ordering:** `id` is a monotonically increasing integer primary key (`AUTOINCREMENT` on SQLite, `BIGSERIAL` on Postgres). Readers order strictly by `id ASC` and pass the last seen `id` back as `since` or `Last-Event-ID`.
- **Retention:** The log retains the newest `MaxRetainedIntegrationEvents` (10,000) rows, pruning older rows incrementally on write so local storage remains bounded while giving disconnected integrations a generous replay window.
- **State repository:** `integration_event` is runtime history (`notSettings`), never applied live from `grain/config` pull requests, and preserved across full backup/restore dumps so sequence cursors do not reset backward.

### 3.2 Event Types

- `task.state`: Emitted when a task is created (`previousState: ""`) or whenever its derived `task_state` view changes (`proposed`, `deferred`, `queued`, `running`, `awaiting_reply`, `awaiting_submit`, `completed`, `failed`, `closed`). When `state` is `awaiting_reply`, `questionSummary` carries the pending question's one-line summary (or derived summary, or pending secret name prompt); a task parked on nothing but resting on a run's `comment_on_issue` note carries that note's own `summary` instead (`Store.NoteSummary`), and none when the note has no summary — never a line derived from the note's body.
- `task.question`: Emitted when a comment carrying a question/request summary (`ask_question`, `request_secret`, `request_access`, or parked agent question) is recorded on a task. A `comment_on_issue` note's optional `summary` does not make it a question.
- `task.comment`: Emitted when an ordinary conversation comment (human reply, external agent note, or agent closing/progress note — including a `comment_on_issue` note given a `summary`, which `task.comment` does not carry) is added to a task. Internal handover checkpoints and turn-stopped markers are excluded.
- `task.ci`: Emitted when a task's grain CI run (`Observation.CI`) or a GitHub check/workflow job (`GitHubCIJob`) on an open task's branch records a conclusion (`success`, `failure`, `cancelled`, `timed_out`, etc.).
- `notification.raised`: Emitted when a notification is raised for the first time, or raised again at a higher severity (which re-surfaces an acknowledged one). A repeat raise that only bumps its count emits nothing. Carries `notificationId`, `subject`, `severity`, `summary` (its title), and the notification's `taskId` and `repo` when it has them (`repo` falls back to the task's).
- `notification.acked` / `notification.cleared`: Emitted when a notification is acknowledged or cleared, by a person or by its `clears_when` condition, with the same fields.
- `local_script.pending`: Emitted when a host-script call (`approve: true`) starts waiting for a person. Carries the call's `taskId`, the task's `repo`, `approvalId`, `script`, and `summary` (the script and how many arguments it was given).
- `local_script.answered`: Emitted when that call is answered, with `decision` set to `approved`, `denied`, or `expired` (its run stopped waiting first).

Readers are filtered by `repo` for every type alike: a repo-scoped reader hears only its repositories' notifications and calls, and a notification with neither a task nor a repository reaches only unrestricted readers. Notification bodies and a call's arguments and stdin are never carried.

### 3.3 Wire Shape and Redaction Rules

Each event serializes as compact JSON with identifiers and actionable metadata only:

```json
{
  "id": 42,
  "type": "task.state",
  "at": "2026-11-05T05:00:00Z",
  "taskId": "3132",
  "repo": "ai-on-gke/grain",
  "previousState": "running",
  "state": "awaiting_reply",
  "questionSummary": "Stream SSE and long-poll on GET /api/events, or add webhooks?",
  "commentId": 19,
  "externalRef": "ai-on-gke/grain#88",
  "origin": "ai-on-gke/grain#88",
  "pullRequest": "ai-on-gke/grain#91",
  "filedVia": "board-sync",
  "actor": "grain",
  "actorKind": "automation",
  "actorLabel": "agent 3132 (via grain)",
  "onBehalfOf": "3132",
  "onBehalfOfKind": "agent"
}
```

- **External references:** `externalRef` resolves the task's primary external link when set (`google-chat` space link, non-task `origin_ref`, GitHub `issue`, `external-pr` or `derived-from` link, or declared `external_ref` custom field), alongside `origin` and `pullRequest` (`fixes` PR or primary `changelist`).
- **No secrets or bodies:** Events never carry secret values (`PendingSecret` surfaces only the secret's name when parked), task descriptions, raw comment/question bodies, prompts, run transcripts, or CI log tails.

## 4. HTTP Transport (`GET /api/events`)

### 4.1 Query Parameters and Headers

- `since`: Integer cursor (`id`) from a prior response, or `"now"` to start at the current head sequence without replaying history. On SSE requests, `Last-Event-ID` is used when `since` is omitted.
- `wait`: Optional long-poll hold duration in seconds (`0`..`25`; values above `25` are clamped to `25`, matching `GET /api/restart-watch` and `ghwebhook`). Omitted or `0` answers immediately in JSON mode. When `wait > 0` and `since` is omitted, the hold starts from the current head sequence.
- `limit`: Maximum events returned in one JSON response (`1`..`500`, default `100`).
- `repo`: Optional repository filter (`owner/name`).
- `types`: Optional comma-separated filter over `task.state`, `task.question`, `task.comment`, `task.ci`, `notification.raised`, `notification.acked`, `notification.cleared`, `local_script.pending`, `local_script.answered`.
- `stream`: `1` / `true` (or `Accept: text/event-stream`) selects Server-Sent Events mode.

### 4.2 JSON Poll and Long-Poll Response

`GET /api/events?since=41&wait=20` returns `200 OK` with `Content-Type: application/json`:

```json
{
  "events": [ ... ],
  "cursor": "42",
  "hasMore": false,
  "timedOut": false
}
```

When `wait > 0` expires with no readable matching events, `events` is `[]`, `cursor` is the current head sequence, and `timedOut` is `true`.

### 4.3 Server-Sent Events (SSE) Mode

With `Accept: text/event-stream` (or `?stream=1`), the server responds with `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and `X-Accel-Buffering: no`, flushes headers immediately with `: ready cursor=<seq>\n\n`, replays any retained readable matching events when `since` or `Last-Event-ID` was provided, and streams live events as `Store.Changed()` fires:

```text
id: 42
event: task.state
data: {"id":42,"type":"task.state",...}

```

Periodic `: keepalive\n\n` comments keep idle connections open across proxies.

## 5. Authorization

`GET /api/events` requires scope `tasks:read`:

1. **Sign-in allowlist:** `singleTenantGate` enforces `-ui-allowed-principals` / `allowed_principal` before the handler runs; unauthenticated callers receive `401` and unlisted principals receive `403`.
2. **RBAC roles, external-agent tokens, and acting tasks:** Callers without `tasks:read` are rejected with `403`. Callers naming `?repo=<r>` outside their `tasks:read` repositories are rejected with `403`. Callers omitting `?repo=` are admitted if they hold `tasks:read` on at least one repository, and `handleEvents` filters both replayed and live events to repositories their `tasks:read` grant reaches (excluding repository-less tasks unless the caller holds unrestricted `tasks:read`).
