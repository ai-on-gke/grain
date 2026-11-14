# 2026-11-13 live events, `ghsa_` callbacks and comment deep links — test run

Walks [docs/test-plans/2026-11-13.md §3](../test-plans/2026-11-13.md) (items 1–5) and
[docs/test-plans/2026-11-13-afternoon.md §2](../test-plans/2026-11-13-afternoon.md)
against real `grain daemon -sandbox-port-base=0` processes on **SQLite and
PostgreSQL 15**, auditing PR #1467 (`e76c5eb28`, `f77175c11`, `9b4024fcd`).
[Task #5355](/tasks/5355) captured the curl/daemon halves (`items1_2.sh`,
`item3.sh`, `item5.sh`); [Task #5484](/tasks/5484) added the real `ghsa_`
callback walk, the `/next/inbox` browser walk, 3,000-comment deep links, three
fixes and this report.

Everything is under [`2026-11-13-live-events/`](2026-11-13-live-events/): the
scripts, and each run's capture in `out/<run>/`.

## Results

| § | Item | SQLite | PostgreSQL | Capture |
|---|------|--------|------------|---------|
| 3.1–3.2 | `/api/events` SSE: notification and host-script events, viewer scoping, `Last-Event-ID` resume, 40-raise burst | pass | pass | `out/items1_2-*` |
| 3.3 | schema 139 → 146 upgrade, with and without `integration_event` | pass | pass | `out/item3-*` |
| 3.4 / afternoon 2.3 | 3,000-comment `#comment=` deep links | **fixed** (F1), pass | pass | `out/browser-*` |
| 3.5 | comment on a closed task delivered once on reopen (A, B, C) | pass | pass | `out/item5-*` |
| afternoon 2.1 | real `ghsa_` callback attribution, sweep expiry, no duplicate answer | pass | pass | `out/ghsa-*` |
| afternoon 2.2 | `/next/inbox` live in ≤ 2 s, no fallback poll, repo-scoped viewer | **fixed** (F2, F3), pass | pass | `out/browser-*` |

## Fixes found by this walk

- **F1 — `/next/` comment deep links never paged, scrolled or highlighted (P0).**
  PR #1388 updated only the classic UI. `/next/task/<id>#comment=<10th of 3,000>`
  read only the newest 100 comments, polled them every 3 s, and never revealed
  the comment. Paging older comments in while scrolled up also raised a false
  "New messages ↓" pill.
  - Fix (`ui-ng/src/app/task/task.store.ts`, `task-detail.component.ts`,
    `shared/conversation/`): a cold load reads 500 comments, then pages back
    500 at a time to the comment. It commits `detail()` once, and polls in
    flight join that read.
  - A link followed on an open page pages the same way. The tab switches to
    Conversation. The comment is scrolled to the middle instantly, focused,
    and highlighted with `primary-container`.
  - A bad or missing comment shows a snackbar. "New messages" now counts only
    comments added at the end.
- **F2 — `GET /api/notifications` refused repo-scoped readers (P0).**
  `GET /api/events` already streamed `notification.*` events to a caller holding
  `tasks:read` on `acme/widgets` only. But the list `/next/inbox` re-reads on
  each event was missing from `isRepoNarrowedTasksRead`, so every read failed
  with 403. The before walk recorded 56 such 403s, and the viewer never saw its
  repository's notification. The handler did not filter by repository either.
  - Fix (`pkg/ui/refs.go`, `pkg/ui/notifications.go`): the route admits such
    callers without `?repo=`. It narrows the list by the notification's repo,
    or its task's repo, the way `/api/events` does. Deployment-wide
    notifications stay operator-only.
  - Regression test: `TestListNotificationsNarrowsToWhatARepoScopedReaderMayRead`.
- **F3 — `/next/inbox` cold load checked access before `/api/config` (P1).**
  `InboxStore.load()` asked `access.can("GET /api/local-script-approvals")`
  before the config arrived, and an unknown config counts as unrestricted. Now
  it waits for the config on a cold load. Unit test in `inbox.store.spec.ts`.
- **Review follow-up — the highlight faded in.** The 600 ms transition ran on
  the way in, so the comment was at about 2 % tint when revealed. Now the
  highlight shows at once and fades out, with no fade under reduced motion.

## afternoon §2 item 1 — real `ghsa_` callback tokens (`ghsa_callback.sh`)

[`ghsa_callback.sh`](2026-11-13-live-events/ghsa_callback.sh) runs the daemon
with **fenced container sandboxes**: `-container-sandboxes` on the `grain-walk`
network, the `grain-sbx:walk` image, and `-container-sandbox-fence require`
with `sudo -n nft`.
- `-host-sandboxes` refuses external capability grants by design
  (`orchestrator/capability_pass.go`). That is why `items1_2.sh` had to write
  the grant with SQL and post the approvals with curl.
- Here the built-in host-script server (`grain capability host-script`, a child
  of the daemon) is configured with four `approve: true` scripts. A task holding
  four `…/host-script#host-script {script}` grants is dispatched.
- Each call is a real `grain host-script <name>` inside the task's container.
  The server's `localexec.HTTPApprover` posts and expires approvals with the
  `ghsa_` token the daemon minted for that task.
- Stubbed: only GitHub (`ghsim.go`) and the agent CLI. The agent CLI only holds
  the run open.

| approval | how it ends | `local_script.pending` actor | `local_script.answered` actor | call's exit |
|---|---|---|---|---|
| `lsa-1` deploy | operator approves | agent `2` (internal) | human `alice@example.com` (access-code), `approved` | `deployed:--env staging`, 0 |
| `lsa-2` rollback | operator denies | agent `2` | human `alice@example.com`, `denied` | 125 "the call was denied" |
| `lsa-3` migrate | caller gives up (`timeout 1.5`), server's `ghsa_` `/expire` | agent `2` | **agent `2`**, `expired` | 124 |
| `lsa-4` slow (2 s) | server SIGSTOPped across the deadline; a task read sweeps it; SIGCONT lets its `ghsa_` `/expire` arrive late | agent `2` | **automation `grain`**, `expired` | 125 "not approved within 2s" |

Identical on both stores:
- `GET /api/events` and the `integration_event` rows (`actor_kind`,
  `actor_id`, `actor_source`) agree.
- Each approval has exactly one `local_script.answered`. The late `/expire`
  after the sweep added none.
- Event ids `1..18` have no gaps.
- The operator's SSE stream and the `tasks:read@acme/widgets` viewer's stream
  saw the same 8 events.

This confirms `9b4024fcd` (the `ghsa_` early-return path attributes the
task's agent), `f77175c11` (a caller's `/expire` is the caller's) and
`e76c5eb28` (one `grain` answer per sweep expiry) on live `ghsa_` tokens.
`items1_2.sh` was last captured at `33a2a3c2f`, before `9b4024fcd`. Its curl
posts used the local token, so its pending and caller-expire rows show
`alice@example.com`.

## afternoon §2 item 2 — `/next/inbox` live updates (`browser_walk.sh`)

[`browser_walk.sh`](2026-11-13-live-events/browser_walk.sh) starts a daemon
behind `tests/iap/iapsim.go`. The viewer is a person: `widgets@example.com`,
signed in through iapsim with a declared `widgets-reader` role (`tasks:read`
on `acme/widgets` only). [`browser_walk.mjs`](2026-11-13-live-events/browser_walk.mjs)
opens `/next/inbox` as the operator and as that viewer, at 1440×900 and
390×844, in light and dark. Then it:
- raises a notification on the widgets task, re-raises it at critical, acks it
  and clears it;
- raises an `acme/secret` notification and a deployment-wide one;
- posts four host-script approvals and settles them by approve, deny, the
  caller's `/expire`, and a sweep past a 2 s deadline.

The inbox's fallback timer is 60 s (`POLL_MS`), so a change within 2 s came
from `/api/events`.

| | before (`ab445d443`) | after, SQLite | after, PostgreSQL |
|---|---|---|---|
| slowest operator row (16 steps × 4 viewports) | 0.35 s | 0.36 s | 0.37 s |
| `/api` reads per change | 3–5 (one refresh) | 3–5 | 3–5 |
| viewer sees its acme/widgets notification | **never** (403) | ≤ 5 ms after the operator | ≤ 5 ms |
| viewer 403s | **56** (`GET /api/notifications`) | 0 | 0 |
| out-of-scope rows on the viewer's page | 0 | 0 | 0 |

## §3 item 4 / afternoon §2 item 3 — 3,000-comment deep links

`browser_walk.mjs` seeds 3,000 comments and opens the 10th. A "read" is a
`GET /api/tasks/<id>` with `comments_limit`/`comments_before` made before the
comment was revealed. "Renders" lists every message count the thread showed.

| route | before | after (SQLite and PostgreSQL) |
|---|---|---|
| `/next/task/<id>#comment=<10th>` | 100 comments, 3 s polls, never revealed | **6 reads** (500 + 5 × 500), renders `3000` once, in view, focused, highlighted, 0 px scroll after |
| `/next/tasks/<id>#comment=<10th>` | redirect kept the fragment; never revealed | lands on `/next/task/<id>#comment=<10th>`; same as above |
| in-page `location.hash = #comment=<20th>` | nothing | 6 `comments_before` reads from the 100 shown, one change `100 → 3000`, revealed |
| classic `/tasks/<id>#comment=<10th>` | 7 reads (50 + 6 × 500), renders `50 → 3000`, revealed | unchanged |

The classic UI pages correctly, in 6 pages of 500. Its first read takes 50
comments, so it makes 7 reads in all. `/next/` makes 6 because its first read
takes 500.

`ui-ng/e2e/next-live-events-and-comment-links.spec.ts` keeps both in CI:
- live inbox rows within 2 s, on the desktop, phone and dark projects;
- a 1,100-comment `/next/tasks/<id>#comment=` redirect landing in ≤ 3 reads
  with one render;
- an in-page hash change, and a snackbar for a missing comment.

`make test-e2e-ng-suite` passed 66/66, including `next-full-system.spec.js`.

## UI review (design book §11)

`ui/e2e/screenshot.mjs` captured `/next/inbox` and `/next/task/20#comment=8`
at 1440 and 390 px, in light and dark, before and after. The contact sheets
are attached to [Task #5484](/tasks/5484). A Playwright probe measured each
viewport:

- **Design:** tokens only, with no hex in component CSS.
  - The highlight is `primary-container` with a `primary` border and
    `on-primary-container` text.
  - Contrast is 12.6:1 in light and 7.0:1 in dark, for body and meta text.
  - There is no horizontal overflow at 390 px.
  - The design book's "The conversation" section documents the behaviour.
- **Functional:**
  - The tab switches to Conversation from Runs.
  - The comment is in view and focused, with a 2 px `primary` focus ring.
  - The highlight clears after 4 s.
  - Overlapping polls join the deep-link read, so the thread renders once.
  - The next 100-comment poll keeps all 3,000 comments (unit test).
- **Adversarial:**
  - Fragments for another task's path, malformed ids and missing ids are
    ignored or get a snackbar.
  - Repeated route and `hashchange` reports of one navigation are de-duplicated.
  - A paging gap cannot drop shown comments, because the head page is kept with
    the older pages.
  - P1 found and fixed: the highlight faded in (see Fixes).

## Reproduce

```sh
make frontend && go build -o /home/debian/bin/grain ./cmd/grain
# PostgreSQL 15 on 127.0.0.1:5432 with role debian/walk; sqlite3, jq, docker, nftables
docker network create --subnet 10.241.0.0/16 --gateway 10.241.0.1 \
  -o com.docker.network.bridge.enable_icc=false grain-walk
# grain-sbx:walk: 2026-11-04-capabilities/Dockerfile.walk-sandbox with
# `go build -o grain ./cmd/grain-sandbox` beside it
docs/test-runs/2026-11-13-live-events/ghsa_callback.sh sqlite 4291
docs/test-runs/2026-11-13-live-events/ghsa_callback.sh postgres 4292
docs/test-runs/2026-11-13-live-events/browser_walk.sh sqlite 4293
docs/test-runs/2026-11-13-live-events/browser_walk.sh postgres 4283
```
