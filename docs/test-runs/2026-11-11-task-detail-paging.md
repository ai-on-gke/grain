# Walkthrough: paged task detail, keyset cursors, reopen delivery (2026-11-11 plan §2)

Plan: [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §2, covering PRs #1147, #1162, #1121, #1120 and #1172. Task 4515, filed by task 4297.

## Outcome

| # | Item | Result |
|---|------|--------|
| 1 | Big records: size and latency, exact `olderComments`/`olderAttempts` | **Pass** on SQLite and PostgreSQL. |
| 2 | Cursor walk under concurrent writes; head-page cache invalidation | **Pass** on both stores. No duplicates, no gaps, 30/30 polls fresh. |
| 3 | Task page: Show earlier, poll retention, deep link to comment 1 of 2,500, compaction | **Pass** at 1440×900 and 390×844, light and dark. The deep link is slow (14–21 s); filed as a follow-up. |
| 4 | Pending question, failure streak, sessions, usage on a paged answer | **Pass.** Every non-list field is identical to the unpaged answer, on both stores. |
| 5 | Reopen delivery | **Bug found and fixed** (this PR). A closed task with no delivered mark dropped the comment left while it was closed. After the fix it passes on both stores. The `AwaitsAck` case behaves as documented. |
| 6 | `grain mcp` paging from a real stdio MCP client, >200 tasks | **Pass** on both stores. |

## Environment

- **Code:** branch `grain/098073e4/task-4515-walkthrough-paged-task-detail`, based on `main` at `2ab9a53ad` plus the fix in this PR. Built from `HEAD` with `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`.
- **Daemons:** two real `grain daemon -sandbox-backend host -sandbox-port-base=0 -poll-interval 2s`, started by [`start.sh`](2026-11-11-task-detail-paging/start.sh):
  - SQLite (`:4311`);
  - PostgreSQL 15 (`-postgres-url postgres://…@127.0.0.1:5432/tdp`, `:4312`).
- **Real:** dispatch, host sandboxes, the orchestrator's prompt building and delivered-mark bookkeeping, the HTTP API, the head-page cache, the React task page in Chromium (Playwright from `ui/node_modules`, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), and `grain mcp` over stdio.
- **Stubbed (unreachable boundaries only):**
  - **The model provider.** The agent CLI is the stub from the token-usage walk ([`../2026-11-10-token-usage/fake-agy.sh`](2026-11-10-token-usage/fake-agy.sh), configured by that walk's `setup.sh`). It answers in prose and makes no tool call.
  - **GitHub.** None is reachable. The open fixes pull request for the `AwaitsAck` case is a `task_link` row (`fixes → acme/widgets#42`).
  - **No CI runs exist** in this deployment, so `ci_runs` walked an empty list.
- **Seeding by SQL:** the big records were seeded with SQL ([`seed-sql.sh`](2026-11-11-task-detail-paging/seed-sql.sh)). Producing 600 real runs through the stub would take hours, so the 2,500 comments, the 600 finished runs and their `task_run_usage` rows went straight into the tables. Each task was created through the API first.
  - Odd comments are a person's and even ones an agent's.
  - Runs 594–600 failed, so the streak is 7.
  - Comment #40 (id 41) is an agent's question. The task is parked on it through `pending_question_comment_id`, which makes it `awaiting_reply` on a question 2,460 comments older than the head page.
- **Fleet:** 220 more tasks were filed through the API on each store (224/223 in all).

## Item 1: big records

[`measure.sh`](2026-11-11-task-detail-paging/measure.sh) runs 7 GETs of `/api/tasks/2` per query. Latency is min/median/max. The max is the cold build; the rest are head-page cache hits.

| Store | Query | Bytes | ms | comments / attempts | older C / A |
|---|---|---:|---|---|---|
| SQLite | unpaged | 741,565 | 1.1 / 1.2 / 43.2 | 2500 / 600 | – |
| SQLite | `comments_limit=50&attempts_limit=20` (task page) | 20,567 | 0.7 / 0.8 / 7.6 | 50 / 20 | 2450 / 580 |
| SQLite | `comments_limit=500&attempts_limit=500` | 362,603 | 0.8 / 1.1 / 17.4 | 500 / 500 | 2000 / 100 |
| PostgreSQL | unpaged | 741,555 | 1.3 / 1.7 / 76.9 | 2500 / 600 | – |
| PostgreSQL | task page | 20,557 | 1.0 / 1.1 / 92.2 | 50 / 20 | 2450 / 580 |
| PostgreSQL | 500 / 500 | 362,593 | 1.1 / 1.3 / 58.4 | 500 / 500 | 2000 / 100 |

Earlier pages (`comments_before` / `attempts_before`) skip the cache. Each was timed 5 times (min/median/max):

| Store | Query | Bytes | ms |
|---|---|---:|---|
| SQLite | `comments_limit=200&comments_before=1000&attempts_limit=1` | 34,043 | 6.5 / 7.0 / 21.0 |
| SQLite | `comments_limit=500&comments_before=2400&attempts_limit=500&attempts_before=550` | 362,607 | 16.6 / 17.5 / 20.7 |
| PostgreSQL | the first | 34,033 | 46.0 / 49.1 / 51.6 |
| PostgreSQL | the second | 362,597 | 51.6 / 55.3 / 57.4 |

- The task page's answer is 2.8% of the whole record: 20.6 KB against 742 KB.
- Its cold build is 6× cheaper on SQLite (7.6 ms against 43 ms).
- `olderComments`/`olderAttempts` are exact at every page size.
- PostgreSQL's change token (`txid_current_snapshot()` behind `PRAGMA data_version`) works: repeat requests hit the cache there too.

## Item 2: paging under writes

[`walk.sh`](2026-11-11-task-detail-paging/walk.sh) walks from the head page (`comments_limit=100&attempts_limit=50`) back to the start with `comments_before=` and `attempts_before=`. Meanwhile a writer, every 0.2 s:

- posts a person's comment through the API;
- inserts a finished run through `sqlite3`/`psql`.

| Store | Pages | Writes during walk | Comments walked | Dups | Missing / extra vs the task's own ids | Attempts walked | Dups | Missing 1..600 | Cache: fresh 200 after a post |
|---|---:|---:|---:|---:|---|---:|---:|---:|---|
| SQLite | 26 | 24 | 2,563 | 0 | 0 / 0 | 621 | 0 | 0 | 30/30 |
| PostgreSQL | 26 | 18 | 2,547 | 0 | 0 / 0 | 617 | 0 | 0 | 30/30 |

- The cache probe works in rounds. Each round posts a comment and then GETs the head page with the previous `ETag` in `If-None-Match`.
  - Every round answered 200 with the new comment on top: no stale 304 and no stale body.
  - The direct SQL inserts invalidated the cache too, because `data_version` sees other connections' commits.
- Comment ids are global across tasks. The gap check therefore compares against the task's own ids from an unpaged read, not against a numeric range. The first version of the helper made the range mistake and reported a false "300 missing", which were another task's ids.

## Item 3: task page (Playwright)

[`page-walk.mjs`](2026-11-11-task-detail-paging/page-walk.mjs) runs in Chromium against the SQLite daemon's own UI, authenticated with the local API token as a header. It ran at 1440×900 and 390×844, in light and dark. Every check passed in all four:

- **Head:** 50 comments rendered, plus `Show earlier (2450 comments, 580 runs)`. First render took 1.2–2.5 s.
- **Show earlier:** added 50 comments, every one older than the oldest already shown, with no duplicate ids.
- **Conditional poll:** a comment posted through the API showed within 1.9–2.0 s. The 50 earlier comments stayed loaded (101 in the DOM).
- **Deep link `#comment=2` (comment 1 of 2,500):** the page paged back to the comment, highlighted it (`comment-highlight`, caught by a MutationObserver), focused it and scrolled it on screen.
  - It took **14.4–15.7 s**, or 21 s from a fresh page load, using 13–15 sequential `comments_limit=200` fetches.
  - Each fetch is 7 ms on the server. The time goes to the round trips and to re-rendering a list that grows by 200 each step (the DOM ends at ~2,500 comments).
  - This works but is slow. Filed as a follow-up (below).
- **Compaction from `compactedThrough` alone:** the seeded task has 300 comments, `compactedThrough` = the 240th, and a handover comment at the 241st. After Show earlier:
  - all 40 loaded comments at or before the cursor say "· summarised", with their bodies in a closed "Summarised by compaction -- show" `<details>`;
  - none of the 60 after it is folded.
  - The task makes no separate context read.

No `ui/` file changed, so no before/after `make ui-screenshots` was needed. The walk's own screenshots were kept outside the repository.

## Item 4: paged answer vs unpaged

The task is `awaiting_reply` on question comment 41, which is far older than the head page, with a 7-run failure streak and usage on all 600 runs. On both stores, `GET /api/tasks/2` was compared with `GET /api/tasks/2?comments_limit=50&attempts_limit=20`, removing `comments`, `attempts`, `older*` and `transitions` from both:

- **Identical** in every other field:
  - `state: awaiting_reply`, `reason: awaiting_reply/question`;
  - `questionCommentId: 41` and `questionSummary` (read by id, outside the page);
  - `failedAttempts: 7`, `lastFailureReason: detail-600`, `lastFailureAt`;
  - `uncountedAgentReplyFrom: "task 2-101"` (the session label);
  - `usage` = `{runs: 600, inputTokens: 780300, outputTokens: 186300, cacheReadTokens: 60000, costUsd: 1.2621, billable: 966600}` (whole-task totals, not the page's).
- The paged `comments` and `attempts` equal the unpaged answer's last 50 and last 20 exactly, including each attempt's own `usage`.
- PostgreSQL's answer equals SQLite's apart from `createdAt`.
- `transitions` differ by design. The paged answer is a window (42 entries against 1,202) with the same final entries.

## Item 5: reopen delivery

[`reopen.sh`](2026-11-11-task-detail-paging/reopen.sh) does the following on a real daemon:

1. Files a no-repo task and runs it once.
2. Closes it.
3. Comments as a person ("Does this cover the staging cluster too? (left while closed)").
4. Reopens it and waits for the next run.

### Finding: the comment was dropped (fixed here)

On the unfixed build (SQLite), the reopen put the task back in `awaiting_reply/answered`:

- no run was queued;
- the `reopened` event had no detail.

The stub's run is a realistic case: an agent that answers in prose and makes no tool call. Its outcome is `failed` ("the agent made no tool calls at all"), yet grain relays the answer and the task lands in `awaiting_reply/answered`. The orchestrator advances `Observation.DeliveredCommentID` only on `succeeded` turns, so this task has **no delivered mark** (`delivered_comment_id` is NULL).

`queueUndeliveredOnReopen` asks `model.UndeliveredPersonComment`, which answers nil whenever the mark is nil, so the comment was never delivered. The same applies to tasks whose runs predate the column.

**Fix** (`pkg/ui/client.go`):

- `setClosed` keeps the `ClosedAt` that the reopen is clearing.
- With no delivered mark, `queueUndeliveredOnReopen` falls back to timing, as the mark's other readers do: a person's comment posted at or after that close, and past any pending question, is delivered.
- A recorded mark still decides on its own. Comments from before the close are not delivered again.

**Regression tests** (`pkg/ui/reopen_delivers_test.go`):

- `TestReopenQueuesACommentLeftWhileClosedWithNoDeliveredMark` fails without the fix ("state = awaiting_reply; want queued").
- `TestReopenWithNoDeliveredMarkIgnoresCommentsFromBeforeTheClose` covers the other side.

**After the fix, on both stores:**

| Store | After run 1 | Closed + comment | Just reopened | After the next run | Reopen event detail | Runs queued by the reopen | Run 2's prompt carries the comment |
|---|---|---|---|---|---|---|---|
| SQLite | awaiting_reply/answered | closed, 1 run | queued | awaiting_reply/answered, 2 runs | `reopened with an unanswered comment from debian; queued` | 1 | yes (`instr(prompt, …) > 0`); run 1's does not |
| PostgreSQL | same | same | queued | same, 2 runs | same | 1 | yes |

### `AwaitsAck` case (SQLite)

The same flow was run on a task with an open fixes pull request (`task_link fixes → acme/widgets#42`), which puts it in `awaiting_submit`. After close, a comment and reopen:

- it stays `awaiting_submit/not_auto_merge`, with 1 run and no event detail;
- this is what `queueUndeliveredOnReopen`'s doc and `model.AwaitsAck` say.

## Item 6: `grain mcp` paging

[`mcp-walk.py`](2026-11-11-task-detail-paging/mcp-walk.py) is a dependency-free MCP client. It:

1. starts `grain mcp` (with `GRAIN_SERVER`, `GRAIN_DATA_DIR`);
2. does `initialize` / `notifications/initialized` / `tools/list` (13 tools);
3. walks each paged tool by passing `nextCursor` back.

| Store | Tool | Pages | Rows | `total` | Dups | Largest result | Under 10 KB | Complete |
|---|---|---:|---:|---:|---:|---:|---|---|
| SQLite | `list_grain_tasks` (summary) | 5 | 223 | 223 | 0 | 5,512 B | yes | yes |
| SQLite | `list_grain_tasks` (`fields: full`) | 45 | 223 | 223 | 0 | 2,902 B | yes | yes |
| SQLite | `grain_inbox` | 12 | 224 | 224 | 0 | 2,959 B | yes | yes |
| SQLite | `ci_runs` | 1 | 0 | 0 | 0 | 77 B | yes | yes (no CI runs exist) |
| PostgreSQL | `list_grain_tasks` (summary) | 5 | 222 | 222 | 0 | 5,357 B | yes | yes |
| PostgreSQL | `list_grain_tasks` (full) | 45 | 222 | 222 | 0 | 2,894 B | yes | yes |
| PostgreSQL | `grain_inbox` | 12 | 223 | 223 | 0 | 2,898 B | yes | yes |
| PostgreSQL | `ci_runs` | 1 | 0 | 0 | 0 | 77 B | yes | yes |

`ci_runs` paging over a non-empty list is not proved here, because there was no CI to fill it. `pkg/cli/mcp_external_paging_test.go` covers it.

## Follow-ups filed

- **Deep link to a very old comment is slow:** 14–21 s and up to 15 sequential fetches for comment 1 of 2,500. During one in-page deep link the same `comments_before` page was also requested twice in a row. That is harmless (no duplicate ids), but wasted. The fix belongs in `ui/src/components/CommentDeepLink.jsx` / `taskDetailPages.js`, for example by fetching with the 500 maximum or jumping straight to the target's page, and it needs its own screenshots. It is filed as a separate task so it doesn't overlap §1's task-page work.

## How to re-run

```sh
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client sqlite3 jq
T=docs/test-runs/2026-11-11-task-detail-paging W=/home/debian/walk
$T/start.sh /home/debian/bin/grain $W/sq 4311
$T/start.sh /home/debian/bin/grain $W/pg 4312 -postgres-url "postgres://debian:debian@127.0.0.1:5432/tdp?sslmode=disable"
$T/api.sh $W/sq 4311 POST /api/tasks '{"title":"Big record","body":"walk","noRepo":true}'   # task 2
$T/seed-sql.sh 2 2500 600 40 | sqlite3 $W/sq/store/grain.db                                # then park it on the question:
#   INSERT INTO task_observation (task_id,pending_question_comment_id,streak_count) VALUES ('2',<id of comment #40>,7)
$T/measure.sh $W/sq 4311 2 7                                                                 # item 1
bash $T/walk.sh $W/sq 4311 2 "sqlite3 $W/sq/store/grain.db"                                  # item 2
UI_DIR=ui BASE=http://127.0.0.1:4311 TOKEN=$(cat $W/sq/local-api-token) TASK=2 FIRST=2 \
  COMPACTED=<task> CT=<compactedThrough> OUT=/tmp/shots node $T/page-walk.mjs                # item 3
WALK_DIR=$W docs/test-runs/2026-11-10-token-usage/setup.sh $W/sq 4311 $W/fake-agy             # stub agent CLI
WALK_DIR=$W $T/reopen.sh $W/sq 4311 "Reopen walk"                                            # item 5
GRAIN_SERVER=http://127.0.0.1:4311 GRAIN_DATA_DIR=$W/sq python3 -B $T/mcp-walk.py grain       # item 6
```

Repeat each with the PostgreSQL daemon (`:4312`, `psql -q -h 127.0.0.1 -U debian -d tdp` as the SQL command).
