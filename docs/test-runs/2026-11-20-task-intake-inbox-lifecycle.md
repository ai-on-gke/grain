# Task intake, Inbox and lifecycle walkthrough — 2026-11-20 (task 7544)

- **Date:** 2026-11-20, 12:25–13:20 UTC, one run.
- **Task:** task 7544, branch
  `grain/53549e42/task-7544-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `2c44ffa0b` ("Merge pull request #1941"). Each fix
  below was checked again on a rebuild of that commit with the fix
  applied.
- **Focus:** what the 2026-11-18 report listed as not covered: host-script
  (local-script) approvals on the task page and in the Inbox, the inline
  config-repo change card's Approve & merge / Reject against a real
  `grain/config`, the goal editor's `on_limit` choice and Clear goal, and
  the stuck-task group's controls. It also re-walked proposal approval,
  a parked question, Alerts' Clear all confirmation, the New task sheet's
  Task priority and fork head labels, close/reopen at phone width and
  Tasks search persistence.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`; Node.js
    v20.19.0; Playwright from `ui-ng/package-lock.json` with headless
    Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`); timezone
    UTC; viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend`, then `npm ci` in `ui-ng/` (`make
frontend` still installs `ui-ng` with `--omit=dev`, which drops
    `@playwright/test`), and `go build ./cmd/grain`. The fixed build
    re-ran `cd ui-ng && npm run build` and `go build`.
  - Server: `grain demo -addr 127.0.0.1:8450 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -qualification -mechanical-port -config-change
-access-code-file <file>`, with `TMPDIR` set to a fresh scratch
    directory, so the store was a throwaway embedded SQLite file and
    `-config-change`'s `grain/config` a throwaway git repository.
  - There was no `/home/debian/.gemini-api-key`, so no model was called
    and `LOOK=1` was not available.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through `POST /api/auth/access-code` as
  `alice@example.com` (as `ui-shared/e2e/accesscode.js` `signIn` does)
  and then acts only through visible controls. Each page logged uncaught
  page errors, console errors and warnings, failed requests, `/api/`
  responses of 400 or more (with bodies), native dialogs, and every
  non-GET `/api/` request with its body. The REST API (bearer access
  code) read back what each gesture persisted.
- **Console baseline:** no uncaught page error on any `/next/` page and
  no failed request. The only console errors were the `400`s from step
  3.3, where the server refused a goal on purpose.
- **UI:** every step ran on `/next/`. No step needed the classic UI; the
  classic Inbox's host-script group was read in source only (step 2.2).

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Two inputs stand in for a model run, because no model was
available:

1. **Host-script calls:** the run's forked `grain mcpserver` asks for an
   approval with `POST /api/tasks/{id}/local-script-approvals`; the
   scripts made that call with the demo's access code as a bearer token
   (no person on it), as `ui-ng/e2e/next-live-events-and-comment-links.spec.ts`
   does. The task was filed with the
   `https://grain.dev/capabilities/host-script#host-script={"script":"ops"}`
   grant the demo's host-script server offers.
2. **Config-repo changes:** `-config-change` seeds #39 and #40 parked on
   proposal branches of the demo's real, throwaway `grain/config` (as
   `propose_config` leaves them).

## Steps and outcomes

### 1. Inbox (`/next/inbox`)

| #   | Step                                          | Outcome                                                                                                                                                                                                                                                                                                                                                                            |
| --- | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1.1 | Open `/next/`, then the rail's Inbox.         | Rail reads "Inbox (28 waiting)"; the headline "28 decisions waiting on you". Groups: Asked: approve a release (1), Asked: reply (4), Review needs your decision (1), Asked: submit for merge (10), Unblock a merge (1), Approve on GitHub (1), Look at a stuck task (5), Approve a proposal (3), Alerts (3). The **All** chip reads 29 (the acknowledged alert, as 2026-11-18 O1). |
| 1.2 | Proposals → row #1 → **Approve**.             | `POST /api/tasks/1/approve`; #1 `queued`, approved; the row leaves at once.                                                                                                                                                                                                                                                                                                        |
| 1.3 | Asked: reply → #4 → type a reply → **Reply**. | **Reply** (`reply-focus`) only focuses the composer; nothing is sent, and #4 stays `awaiting_reply`. The composer's own **Send to #4** sends. Not a defect, but see O2.                                                                                                                                                                                                            |
| 1.4 | Alerts → **Clear all 3**.                     | `POST /api/notifications/clear {"dryRun":true}` first, then a dialog: "Clear all 3 alerts? … 1 of them is expected back on the next check while its condition holds (pipeline.merge-stalled)", with an audit reason box, **Cancel** and **Clear 3**. **Cancel** clears nothing.                                                                                                    |
| 1.5 | Stuck → #27 (deferred past its wake).         | The row's text names the sub-case ("its wake time has passed and it is still deferred -- "Undefer" picks it back up"). The inspector leads with **Undefer**. Pressed: `POST /api/tasks/27/undefer`, `deferred` → `queued`, the row leaves.                                                                                                                                         |
| 1.6 | Stuck → #25 (queued, no run in 72h).          | Inspector leads with **Run now** (its reason's control). Pressed: `POST /api/tasks/25/run-now`; the row leaves.                                                                                                                                                                                                                                                                    |
| 1.7 | Stuck → #26 (live run with no process).       | Its reason's control is `Cancel`; the inspector offers it as **Cancel task**, beside Stop, Pause and Pause immediately (not pressed).                                                                                                                                                                                                                                              |
| 1.8 | Stuck → #5 (on the merge queue too long).     | Its reason (`completed/on_merge_queue`) names no control; the inspector offers Run now and Pause.                                                                                                                                                                                                                                                                                  |
| 1.9 | Reload.                                       | Only #5, #22 and #26 remain in the stuck group.                                                                                                                                                                                                                                                                                                                                    |

### 2. Host-script approvals (`/next/inbox`, `/next/task/<id>`)

| #   | Step                                                                                                                              | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| --- | --------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2.1 | File #42 with the host-script grant; the run asks for call 1: `deploy` with args `["main", "$(rm -rf /)"]` and 29 bytes of stdin. | The rail goes to 28 and a new group "Asked: approve a host script (1)" appears, with the row "#42 · Run deploy · Host script". **Bug 1:** the row's line reads `deploy main $(rm -rf /)`: the arguments are joined with bare spaces.                                                                                                                                                                                                                                       |
| 2.2 | `/next/task/42`.                                                                                                                  | The card "Local script approval: local_deploy" reads `deploy "main" "$(rm -rf /)"` (quoted), then "stdin (29 B · sha256 ab12cd34ef56)" and the preview. **Allow** → `POST …/lsa-1/answer {"approve":true}`; the card leaves and the conversation records "Approved local script call 1 (local_deploy): deploy "main" "$(rm -rf /)" (and 29 bytes on stdin)" by alice. Classic's Inbox renders the same `LocalScriptApproval` component as its task pane, so it quotes too. |
| 2.3 | Call 2 (`deploy prod`, no stdin) → Inbox row → inspector → **Deny**.                                                              | The inspector reads "A run wants to run this on a host: deploy prod" with **Approve** / **Deny**. **Deny** → `{"approve":false}`; the row leaves, nothing is pending, and the rail drops to 27 after a reload.                                                                                                                                                                                                                                                             |

### 3. Config-repo change card and goals (`/next/task/<id>`)

| #   | Step                                                                                                                                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 3.1 | `/next/task/39` (proposal branch `grain/config/39/poll-interval`).                                                                       | The card "Waiting for configuration change: Raise the default poll interval to 30s" shows the branch and head, with the one-file diff (`tables/grain_config.json`, `poll_interval_ms` 10000 → 30000) already open; **Hide config diff** folds it. **Approve & merge config** → `POST /api/tasks/39/config-change/approve {"headSha":…}`; the card leaves, #39 goes back to `queued`, and `GET /api/fs/config/tables/grain_config.json` now reads `"poll_interval_ms": 30000`. |
| 3.2 | `/next/task/40` → **Reject…** → reason → **Confirm reject**.                                                                             | `POST …/config-change/reject` with the reason; the card leaves (also after a reload), #40 `queued`, and the reason shows in the conversation. Both rows leave the Inbox and the rail drops by two.                                                                                                                                                                                                                                                                            |
| 3.3 | `/next/task/25` → **Edit** → Task goal: kind **Pull request merged**, repository emptied, At limit **Notify & back off** → **Set goal**. | Refused with the server's words in a toast: "goal pr-merged needs a task, or a repo and a number".                                                                                                                                                                                                                                                                                                                                                                            |
| 3.4 | Target task `24` → **Set goal**.                                                                                                         | `PUT /api/tasks/25/goal {"kind":"pr-merged","spec":{"task":"24"},"on_met":"close","on_limit":"notify"}`; the row reads `onLimit: notify`, `maxResumes 6`, `maxStall 24h`. After a reload the editor still shows Pull request merged, 24 and Notify & back off.                                                                                                                                                                                                                |
| 3.5 | **Clear goal**, reload.                                                                                                                  | `DELETE /api/tasks/25/goal`; the goal is gone, the Details tab has no Goal line and no goal card is left behind.                                                                                                                                                                                                                                                                                                                                                              |

### 4. New task sheet (`/next/tasks` → rail **New**)

| #   | Step                                          | Outcome                                                                                                                                                                                          |
| --- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 4.1 | Task priority `0`, `-2`, `2.5`.               | Each is marked `aria-invalid` with "a whole number, 1 or more; default 5, higher numbers run first", and **Create and start** is disabled.                                                       |
| 4.2 | Task priority `1e3`.                          | Accepted; filing sends `"priority":1000` and #43 is `queued` at priority 1000.                                                                                                                   |
| 4.3 | Base `octo:feature/x`, `o/c:x`, `octo cat:x`. | As on 2026-11-18: the first is explained as a fork head; the second is refused locally ("…"o/c" is not a GitHub login") and holds Create disabled; the third is not refused before sending (O3). |

### 5. Close and reopen (`/next/task/2`, 390×844)

| #   | Step                              | Outcome                                                                                                                                                                                                                                                                                                       |
| --- | --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 5.1 | **Close**.                        | Close now asks first (it acted at once on 2026-11-18): a reason box, **Close** / **Cancel**. With a 120-character reason → `POST /api/tasks/2/close {"close_pull_request":false,"reason":…}`; `closed`.                                                                                                       |
| 5.2 | **Reopen**, then the Details tab. | `queued`. History: "Closed · alice@example.com (access code)— Superseded…" and above it "reopened · alice@example.com (access code)". **Bug 2:** the reopen reads as a raw lower-case event, and the reason runs into the principal with no space. The long reason wraps inside its row (no overflow at 390). |

### 6. Tasks search (`/next/tasks`)

`retry` in the search box writes `?q=retry`; a reload keeps it (also
covered by `next-intake-lifecycle.spec.js`).

## Bugs found and fixed

### Bug 1 — the Inbox showed a host-script call with its arguments unquoted

**Reproduction:** with the demo's host-script grant on a task, have the
run ask for `deploy` with args `["a b", "$(rm -rf /)"]`. The Inbox row and
its inspector, where the person presses Approve, read `deploy a b $(rm -rf
/)`: one argument with a space reads as two, so the call approved is not
the one shown. The task page's card and classic quote every argument.
Separately, a call that carries stdin without a preview of it showed no
stdin line at all, on either surface.

**Fix:** a shared `ui-ng/src/app/shared/script-call.ts` (`scriptCallLine`,
`stdinLine`) in classic `LocalScriptApprovals.jsx`'s words. The Inbox row,
its inspector and the task page's card all use it. The stdin line ("On
stdin: N bytes, sha256 <full hash>", plus how much of a cut preview is
shown) is shown whenever there is stdin.

**Coverage:** `script-call.spec.ts` (unit), the updated
`inbox-classifier.spec.ts` assertion, and
`ui-ng/e2e/next-host-script-call-quoting.spec.js`. The e2e spec checks the
row, the inspector and the card. It fails on the base build, whose row
reads `deploy a b $(rm -rf /)`.

### Bug 2 — History showed "reopened" raw and ran reasons into the principal

**Reproduction:** close any task with a reason, reopen it, and open its
Details tab. The rows read "reopened · …" and "Closed · alice@example.com
(access code)— <reason>".

**Fix:** `historyLabel` in `ui-ng/src/app/tasks/task-view.ts`
sentence-cases events that are not task states (Reopened, Owner changed,
…). The reason keeps its leading space (`&ngsp;`), which Angular's
whitespace stripping had removed.

**Coverage:** a `historyLabel` unit test and
`ui-ng/e2e/next-history-labels.spec.js` (desktop and phone). The spec
fails on the base build.

## Tests run

- `npx vitest run src/app/shared/script-call.spec.ts src/app/inbox
src/app/task src/app/tasks/task-view.spec.ts` in `ui-ng/`: all pass.
  `npx tsc -p tsconfig.app.json --noEmit` is clean, and Prettier is
  clean on every touched file.
- On the fixed build, `npx playwright test` passed:
  - the two new specs;
  - `next-live-events-and-comment-links`, `next-inbox`,
    `next-task-lifecycle-regressions` and `next-intake-lifecycle`;
  - a sweep over every `next-inbox*`, `next-task*`, `next-tasks*` and
    `next-intake*` spec, plus `next-full-system`,
    `next-access-grant-label`, `next-merge-failed-state`,
    `next-suites-and-task-actions` and `next-changelist-diff-preview`.
    109 passed (3.4m).

## Observations (not treated as defects)

- **O1:** the **All** chip still counts the acknowledged alert (29 vs 28),
  as 2026-11-18 O1.
- **O2:** a parked question's inspector offers both **Reply** (focuses the
  composer) and **Answer**. The composer's send button reads **Send to
  #4**. A person who types and presses **Reply** sends nothing.
- **O3:** a base with a ':' that is not shaped like a fork head
  (`octo cat:x`) is still left to the server to refuse (2026-11-18 O4).
- **O4:** the goal editor is the only place a goal's `on_limit` shows;
  Details reads only the kind ("Goal: pr-merged").
- **O5:** at 390 px the task action row is a horizontal strip: **Close**
  sits at x≈749 and is reached by scrolling the strip (or **More**).

## Gaps on `/next/` (no page or control yet)

Rechecked against `ui-ng/src/app` at `2c44ffa0b`. These are unchanged
from 2026-11-18:

- **Inbox:** sections cannot be collapsed (the only `aria-expanded` is
  the split pane's).
- **Tasks:** no State/Admission/Human review/External ref/Origin filters,
  no `owner:any:me`, no server-side closed search, no `imported`,
  external-PR or "held for approval by" chip, and no Blocked tab or rail
  count.
- **Task detail:** no full-screen image viewer, and no Mermaid zoom or
  fullscreen in the conversation.
- **Classic-only pages:** System → Pipeline, `/new?field.<name>=` links
  and the `settings/new-task-form.yml` notice, and Boards.

## Not covered

- **Pending-secret box:** no seed or e2e hook parks a task on
  `request_secret`.
- **Force merge:** no seeded task combines a stuck CI wait with a pull
  request.
- **Cross-team approvals, capability audit contents, the live transcript
  and Mermaid/image viewing:** no time in this run. Their specs are in
  the sweep above.
- **Batch bar, template draft priority and the read-only role:** walked
  on 2026-11-17. Their specs are in the sweep above.
