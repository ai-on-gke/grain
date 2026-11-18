# Walkthrough — `comment_on_issue` notes as `task.comment`; host-mode install notice (2026-11-15 §7)

Plan: [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §7 — PR #1515 ([Task #5467](/tasks/5467)) and PR #1510 ([Task #5394](/tasks/5394)). Run as [Task #5919](/tasks/5919), filed by [Task #5885](/tasks/5885).

## Environment

- Commit walked: `c6743dc5d` (`origin/main` when the run began), plus the fixes below.
- `grain daemon -sandbox-port-base=0 -host-sandboxes`, built from `HEAD` (`make frontend && go build ./cmd/grain`). It used the fake agent CLI and the GitHub sim from [`2026-11-13-grain-health-exit-codes/`](2026-11-13-grain-health-exit-codes/) (`env.sh`, `fake-agy.py`, `ghsim.go`), reused unchanged.
- Stores: SQLite 3.40.1, and PostgreSQL 15.20 (Debian 15.20-0+deb12u1, local cluster).
- `grain-plc/cmd/plc-chat-gateway` was built from `HEAD` (`cd grain-plc && go build ./cmd/plc-chat-gateway`) and run with `-watch-sse` against the live daemon. The only stub was the chat platform's outbound API: a 20-line Python HTTP server that appends every POST to `chat.jsonl`.
- Browser: Playwright Chromium 1234 (`/var/cache/playwright`). Screenshots were taken with `ui/e2e/screenshot.mjs` against the live daemons (`URL=`/`CODE=`), at 1440 px desktop and 390 px phone, light and dark.
- Helpers live in [`2026-11-15-note-events-and-host-notice/`](2026-11-15-note-events-and-host-notice/):
  - `run_walkthrough.sh` covers items 1 and 2.
  - `host_mode.sh` covers item 4.
  - `actions/*.json` holds the fake agent's tool calls for each case.
  - `dom-dump.mjs` dumps rendered page text, because nobody in the sandbox can view a PNG: `LOOK=1` has no Gemini key here.
  - `captures/` holds the `GET /api/events` payloads and what the chat platform received, per store.

## Item 1 — every relay path on a live daemon

`STORE=sqlite|postgres ./run_walkthrough.sh`. Each case files a task and writes the fake agent's action under the task's real id. It then approves the task, waits for the task to settle, and saves `GET /api/events?limit=500`. It also saves the long-poll (`?wait=1s`) and SSE (`Accept: text/event-stream`) forms.

| Case | Run's calls | Task ends | Events for the task (both stores) |
|---|---|---|---|
| (a) branchless report | `comment_on_issue` + `summary` | `awaiting_reply` | `task.comment` (commentId, **no** `questionSummary`), then `task.state awaiting_reply` with `questionSummary: "Report: widgets README needs no change"` |
| (b) note beside a pull request | `run_command` (commit + `git push`), `comment_on_issue` + `summary` | `awaiting_submit` (acme/widgets#9000) | `task.comment` (no `questionSummary`), `task.state awaiting_submit` |
| (c) report-only sub-task | parent: `propose_task` + `nothing_needed`; child: `comment_on_issue` + `summary` | parent and child both `closed` | child: `task.comment` (no `questionSummary`), `task.state closed`, `task.comment` (close note). No `task.question` |
| `ask_question` | `ask_question` + `summary` | `awaiting_reply` | `task.question` + `questionSummary`, `task.state awaiting_reply` + the same summary |
| `request_secret` | `request_secret` + `summary` | `awaiting_reply` | `task.question` + `questionSummary` |
| `request_access` | `request_access` (network: open) + `summary` | `awaiting_reply` | `task.question` + `questionSummary` |
| chat message | interactive task: `chat` + `summary`; after a person's reply, `comment_on_issue` + `summary` | `awaiting_reply` | while waiting: `task.question` + `questionSummary`, `task.state awaiting_reply` + summary. After the reply and the note: `task.comment` (no `questionSummary`), `task.state awaiting_reply` with the note's summary |

**Outcome: passes on both stores.** All four kinds of notes (a, b, c and the chat case's closing note) are `task.comment` with no `questionSummary`. The four question kinds stay `task.question`. (a)'s `awaiting_reply` `task.state` carries the note's summary. The SSE and long-poll forms carry the same events: `captures/*/events-all.json`, and `events-chat-waiting.json` for the chat case while it waits.

Notes:
- The `chat` tool is only given to `-interactive` runs (`pkg/daemon/mcpserver.go`). A first attempt from a background run went through `grain api POST /api/tasks/{id}/messages` and was refused for lacking `api:tasks:write`, as designed. The case was then run as an interactive task.
- `GET /api/tasks/{id}` for (b) has no `noteSummary`, because the task rests on `awaiting_submit`, not on the note. This is as `Task.NoteSummary` documents.

## Item 2 — PLC chat gateway against the live events

**Bug found (F1): the gateway delivered nothing anybody could see.** `plc-chat-gateway` passed every forwarded event to `plcseams.HMACChatAdapter.PostMessage`. That method only appends to an in-memory `Outbox`, which nothing outside a test in the same process can read, and the command had no flag to set the adapter's `Sink`. A running gateway therefore "posted" its messages nowhere.
- Reproduction: run `plc-chat-gateway -watch-sse -grain-url … -default-space spaces/x` against any daemon with events. Nothing leaves the process.
- **Fix:** `-outbound-url` (`$PLC_CHAT_OUTBOUND_URL`) POSTs each message as JSON to the chat platform, signed in `X-PLC-Chat-Signature` when `-webhook-secret` is set.
- **Regression test:** `TestOutboundURLDeliversForwardedEvents` (`grain-plc/cmd/plc-chat-gateway/main_test.go`). It runs the command with `-watch-sse -outbound-url` against a stub stream. It checks the signature, that a `task.comment` note is not announced as a question, and that a `task.question` is announced exactly once.

**Bug found (F2): every question message repeated its summary** ("…parked on a question: X — X"). grain sends only `questionSummary`, and `grainclient.IntegrationEvent.UnmarshalJSON` reads it into both `Summary` and `Question`.
- **Fix:** the gateway names the body only when it differs from the summary. The same test asserts the exact text.

Live result after the fixes (`captures/sqlite/chat.jsonl`; `captures/postgres/chat.jsonl` was captured before F2's fix and shows the doubled text):

```
Task #6 is parked on a question: Keep the v1 widget API alongside v2?
Task #7 is parked on a question: Set widgets-api-key so the sync job can authenticate
Task #8 is parked on a question: Open network access to fetch the widgets schema
Task #9 is parked on a question: Land the widgets fix on main or a release branch?
```

**Outcome:** exactly one "parked on a question" message per real question (ask, secret, access, chat) on both stores, and **none** for the notes of tasks 2, 3, 5 or 9. Notes arrive as `Task #N event task.comment`. `cd grain-plc && go vet ./... && go test -race ./...` passes.

**F5 (raised by the gating review, fixed):** a slow or failing chat platform stalled or broke the stream. `-outbound-url` used `http.DefaultClient`, which has no timeout, so a POST the platform never answered held every later event behind it. A refused POST ended `WatchSSE` after its cursor had already moved past the event, which lost that event and reconnected. Each POST is now bounded (10 s). A delivery failure is written to stderr (`task N <type> not delivered: …`) and costs only that message; the stream carries on. Regression test: `TestOutboundURLOutlivesASlowOrFailingChatPlatform`.

## Item 3 — `/next/` Inbox and task page

**Bug found (F3): `/next/` ignored `Task.noteSummary`.** Classic `ui/` uses `noteSummary` for an `awaiting_reply` task resting on a summarized note (`questionActivity`, `inboxNotifications.js`); `ui-ng/` never read it. On the live daemon (before the fix):
- In `/next/inbox`, tasks #2 (a) and #9 (chat case's note) were filed under "Asked: reply" with a **"Needs reply"** pill in the reply tone and shape. Their line read "Answered on the task itself." instead of the note's summary, and the inspector said "The run is parked until somebody answers it."
- `/next/tasks/2` showed no summary at all, while a question (#6) gets an "Asked: …" card.

**Fix (`ui-ng/`):**
- `Task.noteSummary` is added to `api/models.ts`.
- The inbox classifier gives a note entry a neutral **"Note"** pill drawn with the neutral shape (`InboxEntry.iconKind`), the note's own line as its summary, and a rationale saying nothing was asked. It stays in the reply group, so Acknowledge (bulk) and replying still work.
- The task page shows a hairline **"Note: …"** card where a question shows its filled "Asked: …" card.
- Workstream nodes use the note's line as their activity text.
- `ui-design-book.design.md` gains the rule "A note is not a question".

After the fix, on the live daemon (`captures/sqlite/next-pages-after.txt`): #2 reads "Note · Report: widgets README needs no change", #9 reads "Note · Report: fix goes to main", and `/next/tasks/2` shows "Note: Report: widgets README needs no change". #6, #7 and #8 are unchanged ("Needs reply" / "Access request" with their questions).

**Regression tests:**
- `inbox-classifier.spec.ts` › "reads a task resting on a summarized closing note as that note, not a question".
- `ui-ng/e2e/next-note-summary.spec.js`: over `grain demo`, it rewrites one filed task into the shape the daemon sends for a note. It asserts the Inbox row, the inspector's dock and the task page's card.
- `vitest` for inbox/task/workstreams passes (167). `next-note-summary`, `next-full-system` (desktop/phone × light/dark plus contrast) and `next-inbox` pass: 11 of 11.

Screenshots: `/next/inbox`, `/next/tasks/2`, `/next/tasks/9` and `/next/tasks/6` at 1440/390 px in light and dark, before and after, on the live SQLite daemon. The contact sheets are attached to the task's closing note. The images could not be viewed in the sandbox (no image reader, no Gemini key for `LOOK=1`), so layout was checked through rendered text and the e2e bounding-box assertion, not by eye. Report links stay on classic routes (`/tasks/<id>`).

The design book's three independent reviews (Design, Functional, Adversarial) were **not** run as separate reviewers in this sandbox; the every-PR gating review is the first independent look.

## Item 4 — host-mode install notice

`./host_mode.sh` starts a real daemon on `127.0.0.1:4290` that `hostModeInstall` reads as an unmigrated host-mode install. It uses three things:
- `GRAIN_IN_CONTAINER=0` (bare machine);
- `-upgrade-image docker.io/grainai/grain -upgrade-image-ref-file …`;
- systemd's `INVOCATION_ID` with `GRAIN_INSTALL_MODE=host`, set through a wrapper because `env.sh` starts the daemon with `env -i`.

| Surface | Text (captures/hostmode/) | Names only `sudo ./scripts/setup.sh`? |
|---|---|---|
| Daemon log | `level=warning condition=host_mode_install … -- run sudo ./scripts/setup.sh.` | yes |
| `GET /api/config` `installNotice` | `"command": "sudo ./scripts/setup.sh"`, signals as above, `affectedClis` claude/codex | yes |
| Upgrade refusal (`POST /api/upgrade {"branch":"latest"}`) | "… This release expects container mode, so it is not upgraded in place: run sudo ./scripts/setup.sh instead, which also upgrades it" | yes |
| Classic banner, `/system` Health, `/settings/upgrade` | "Run sudo ./scripts/setup.sh." | yes |
| `/next/system` (redirects to `/next/fleet`) and `/next/settings/upgrade` | **no notice at all**; Upgrade offers "Start upgrade" | n/a — see F4 |

No edition-specific installer path appears anywhere.

**Gap found (F4, not fixed here):** `ui-ng/` reads `installNotice` nowhere. A host-mode install's operator who uses `/next/` sees no banner, nothing on Fleet, and an enabled "Start upgrade" button that the server then refuses. Fixing it means designing a new notice surface in `ui-ng/` (shell banner, Fleet health, Upgrade pane), with its own design-book reviews. It is proposed as a follow-up task rather than squeezed into this walkthrough. Screenshots of the four pages (both UIs) were taken at 1440/390 px in light and dark.

## Findings summary

| # | Finding | Status |
|---|---|---|
| F1 | `plc-chat-gateway` had no way to deliver outbound messages (in-memory Outbox only) | **Fixed**: `-outbound-url`; `TestOutboundURLDeliversForwardedEvents` |
| F2 | Gateway question messages repeated the summary ("X — X") | **Fixed**, same test |
| F3 | `/next/` Inbox and task page ignored `noteSummary`: summarized notes read "Needs reply" / "Answered on the task itself." | **Fixed** in `ui-ng/`; unit + e2e spec |
| F4 | `/next/` shows no host-mode install notice (Fleet, Upgrade, shell) | Follow-up proposed |
| F5 | A slow or failing chat platform stalled the gateway's stream or ended it past the failed event | **Fixed**: 10 s POST bound, failures logged; `TestOutboundURLOutlivesASlowOrFailingChatPlatform` |
| — | PR #1515 event types, PR #1510 notice text | **Confirmed** on live SQLite and PostgreSQL daemons |
