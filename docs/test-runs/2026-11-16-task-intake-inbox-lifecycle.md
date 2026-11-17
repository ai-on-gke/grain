# Task intake, Inbox and lifecycle walkthrough — 2026-11-16 (task 6331)

- **Date:** 2026-11-16, 10:00–11:00 UTC, one run.
- **Task:** task 6331, branch
  `grain/53549e42/task-6331-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `feb34cb22` ("Merge pull request #1673"). Each fix
  below was checked again on a rebuild of that commit with it applied.
- **Focus:** the 2026-11-15 report's gap list, plus `/next/` code that
  landed since then (batch task summaries, refused batch calls, Mermaid).
  This run went after the Inbox's stalled, proposal, reply and bulk
  controls, the cold `/api/tasks` failure on both Inbox and Tasks, the
  Tasks selection bar, Bulk edit's Task priority, the task page's
  lifecycle controls, and the New task sheet's queued, proposed and
  fork-head paths.
- **Environment:**
  - Debian sandbox, `linux/amd64`, 7 CPUs; Go from `/usr/local/bin/go`;
    Node.js v20.19.0; Playwright from `ui-ng/package-lock.json` with
    headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`);
    timezone UTC; viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend` and `go build ./cmd/grain`. The fixed
    builds re-ran `cd ui-ng && npm run build` and `go build`. Note that
    `make frontend` reinstalls `ui-ng/node_modules` without dev
    dependencies, which removes `@playwright/test`; `npm ci` in `ui-ng/`
    puts it back.
  - Servers: `grain demo -addr 127.0.0.1:<port> -open=false -github-sim
    -fake-ci -scheduled-workflows -fork-pull-request -git-proxy
    -base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
    -sensitive-hold -review-questions -human-review -secrets-store
    -notifications -qualification -mechanical-port -config-change
    -access-code-file <file>`, each with `TMPDIR` set to a fresh scratch
    directory, so each store was a throwaway embedded SQLite file.
    `:8440` ran the base build, `:8441` the first fixes, and `:8442`
    (`-notifications` only) every fix, for the screenshots.
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through `POST /api/auth/access-code` with the
  run's code (as `ui-shared/e2e/accesscode.js` `signIn` does) and then
  acts only through visible controls. Each page logged uncaught page
  errors, console errors and warnings, failed requests, `/api/`
  responses of 400 or more (with bodies), native dialogs, and every
  non-GET `/api/` request with its body. The REST API (bearer access
  code) read back what each gesture persisted.
- **Console baseline:** no uncaught page error and no console error on
  any `/next/` page except the deliberate ones below (the stubbed 503s,
  and the 400 from step 2.4 on the base build). The only failed requests
  were `GET /api/config … net::ERR_ABORTED`, where a script navigated
  away while a read was in flight.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. One boundary was stubbed:

1. **Cold `/api/tasks` failure:** Playwright's `context.route` answered
   `GET /api/tasks` (any query) with `503` and the plain-text body
   `boom`, standing in for a proxy or server fault, for steps 1.9 and
   3.6. Nothing else was intercepted.

## Which UI each area ran against

| Area                                            | UI                                |
| ----------------------------------------------- | --------------------------------- |
| Inbox                                           | `/next/inbox`                     |
| New task sheet                                  | `/next/` sheet (rail **New**)     |
| Tasks list, tabs, search, selection bar         | `/next/tasks`                     |
| Task detail and lifecycle                       | `/next/task/<id>`                 |
| System → Pipeline manual mode                   | no `/next/` page (`system/:tab` redirects to classic); not walked |
| `/new?field.<name>=` links                      | no `/next/` route; not walked     |
| Boards                                          | classic only; not walked          |

No step was walked on the classic UI. Classic code was read only to
compare behaviour (stalled-row controls, `wholeNumber`/`taskPriority`,
`forkHead.js`).

## Steps and outcomes

### 1. Inbox (`/next/inbox`, base build `:8440`)

| #    | Step | Outcome |
| ---- | ---- | ------- |
| 1.1  | Open `/next/tasks`, then the rail's **Inbox**. | Lands on `/next/inbox`. Headline "28 decisions waiting on you", rail badge 28 ("Inbox (28 waiting)"). Groups: release 1, reply 4, review decision 1, submit 10, unblock a merge 1, approve on GitHub 1, stuck 5, proposals 3, alerts 3. The **All** and **Unread** tabs read 29 (O1). |
| 1.2  | Select each stuck row. | #27 (wake passed, still deferred) offers **Undefer**; #22 and #25 (nothing dispatching) **Run now**; #26 (no process finishing the run) **Cancel task**; #5 (on the merge queue) offers no control, as classic's stalled group documents (O2). |
| 1.3  | #27 → **Undefer**. | `POST /api/tasks/27/undefer`; row leaves; headline and rail 27; API `queued`. |
| 1.4  | #22 → **Run now**. | `POST …/22/run-now`; row leaves; 26; API `queued`. |
| 1.5  | #26 → **Cancel task**. | A confirm dialog: "Cancel #26? The run in flight is stopped and the task is closed; it can be reopened later." with **Cancel** / **Cancel task**. Confirming closes it (API `closed`). |
| 1.6  | #1 (proposal) → **Approve**. | `POST …/1/approve`; row leaves; 25; API `queued`. |
| 1.7  | #4 (parked question) → type a reply → **Send**. | `POST …/4/comments {"body":…}`; row leaves; 24; API `queued`. |
| 1.8  | Reload. | 24 and 24; none of the rows above came back. |
| 1.9  | **Approve all 2** on the proposals group. | Dialog lists "2 in acme/widgets, 1 owned by debian, 1 owned by operator"; **Approve 2** needs "I am approving work owned by someone else" ticked. Both approved (#12, #38 `queued`). |
| 1.10 | Select the v2.4.0 release row. | **Approve** and **Reject**; the soak is already complete (25h10m of 24h), so there is no short-soak choice. |
| 1.11 | Cold: `/api/tasks` stubbed to 503 (Stub 1), open `/next/inbox`. | Headline "At least 3 decisions waiting on you" and "Could not read tasks; retrying." The release and alert rows still show. It never claims nothing is waiting. |

### 2. Creating tasks (`/next/` New task sheet)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 2.1 | Rail **New**, title, a description with a Mermaid block, tick **Don't port** and **This fixes a red base branch**, **Create and start**. | `POST /api/tasks` with `"noPort":true,"fixesBaseBranch":true`; #41 `queued`, with both flags stored. The draft is cleared once filed. |
| 2.2 | Open #41's page. | The Mermaid block renders as an SVG diagram. |
| 2.3 | Reopen New, turn **Approve and start now** off. | The submit button becomes **Save as proposal**; #43 is filed `proposed`. |
| 2.4 | Untick **Create a new branch**, type `octo-fork:feature-x`, then `bad::label`, then `a b:c` in Base branch. | **Bug 3:** nothing under the box for any of them, and **Create** stays on. Filing `bad::label` gets the server's 400 ("has more than one ':'…") as a banner after a round trip. Fixed: see Bug 3. |

### 3. Tasks (`/next/tasks`)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 3.1 | Type `retry` in **Filter this view**, open **Submit & land**, Back, Forward, reload. | URL `?q=retry` ↔ `?q=retry&tab=merging`; the box and tab follow each move and survive the reload. |
| 3.2 | `?q=state:running`, `repo:ACME/WIDGETS`, `owner:any:me`, `admission:durable`, `origin:imported`. | Matched as free text, so nothing matches. `/next/tasks` still has no attribute filters (gap below). |
| 3.3 | Backlog → tick #31 and #33. | Selection bar: "2 selected · Move to top · Move to bottom · Run selected now · Set priority · Defer · More · Close · Clear". **More** offers Bulk edit… and Capability…. |
| 3.4 | **Set priority** → Urgent 9. | Two `PATCH {"priority":9}`; both 9 in the API. |
| 3.5 | More → **Bulk edit…** → tick Task priority, type `0`, `-2`, `2.5`, `1e3`. | Each is refused with "Use a whole number, 1 or more." and **Apply to 2** disabled. **Bug 2:** `1e3` is refused too, though classic Bulk edit reads it as 1000. Fixed: see Bug 2. |
| 3.6 | Type `12` → **Apply to 2**. | Two `PATCH {"priority":12}`; persisted. The selection is cleared after Apply. |
| 3.7 | Reselect, open Bulk edit, **Cancel**. | Nothing sent; "2 selected" stays. |
| 3.8 | **Defer**, then **Deferred** tab → reselect → **Bring back**. | `POST …/defer {"deferredUntil":""}` ×2 → both `deferred`; `POST …/undefer` ×2 → both `queued`. |
| 3.9 | Cold: `/api/tasks` stubbed to 503 (Stub 1), open `/next/tasks`. | **Bug 1:** every open tab reads **0** (Backlog 0, Needs approval 0, Running 0 …), and the banner reads "The task list could not be read: Unexpected token 'b', "boom" is not valid JSON." Fixed: see Bug 1. |

### 4. Task detail and lifecycle (`/next/task/<id>`, fixed build `:8441`)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 4.1 | #20 → **Edit** → new title → **Save changes**. | `PATCH /api/tasks/20` with the title (and the rest of the form); persisted. |
| 4.2 | **Defer until…** → pick 2026-11-20 → submit. | `POST …/defer {"deferredUntil":"2026-11-20T00:00:00.000Z"}`; API `deferred` until that day. The date control is new since the 2026-11-15 run. |
| 4.3 | **Bring back**. | `POST …/undefer`; `queued`. |
| 4.4 | **Close**, then **Reopen**, then reload. | `POST …/close {"close_pull_request":false}` → `closed`; `POST …/reopen` → `queued`. History: "Closed · alice@example.com (access code)" and "reopened · alice@example.com (access code)". Close still takes no reason and asks nothing (O3). |

## Bugs found and fixed

### Bug 1 — a cold task-list failure showed "0" on every tab and quoted the JSON parser

**Reproduction:** `grain demo`; make `GET /api/tasks` answer 503 with a
non-JSON body (Stub 1); open `/next/tasks`. Every open tab reads 0, which
says nothing is waiting when nothing has been read, and the banner reads
`The task list could not be read: Unexpected token 'b', "boom" is not
valid JSON. This page tries again on its own.`

**Cause:** `TasksStore.counts` counted the empty list whether or not it
had ever loaded. The shared `TaskFeedStore` used its own
`extractError`, which lacks the non-JSON case that `errorText`
(`ui-ng/src/app/sessions/session-helpers.ts`) already handles.

**Fix:** an open tab's count is `null` (the template already hides a
null count) until the list has loaded once. The feed uses `errorText`,
so the banner reads "…could not be read: The server answered 503 Service
Unavailable. This page tries again on its own." The Tasks and Agents
banners drop the reason's own full stop, so it is not doubled.

**Coverage:** `ui-ng/e2e/next-tasks-intake-regressions.spec.js` "a task
list that never loaded shows no tab counts and names the status". Fails
on the base build (it got the parser text and a `.tab-count`).

### Bug 2 — Bulk edit refused `1e3` as a Task priority

**Reproduction:** `/next/tasks` → select two backlog rows → More →
**Bulk edit…** → tick Task priority → type `1e3`. It is refused and
**Apply** stays disabled. Classic Bulk edit (`ui/src/wholeNumber.js`)
reads it as 1000, and the `/next/` function's own comment claimed the
same rule as classic.

**Fix:** `taskPriority` (`ui-ng/src/app/tasks/batch-edit-dialog.component.ts`)
accepts decimal exponent notation and still requires a safe whole number
of at least 1, so `0`, `-1`, `2.5`, `1e-3`, `0x10` and `1e400` are still
refused. On blur the box is rewritten as the number it files (`1e3` →
`1000`), so what it shows is what Apply sends.

**Coverage:** the `taskPriority` unit test in `task-view.spec.ts`, and
"Bulk edit reads 1e3 as priority 1000 and refuses what is not a whole
number" in the new e2e spec (fails on the base build).

### Bug 3 — New task neither explained nor refused a fork head label

**Reproduction:** `/next/` → **New** → untick **Create a new branch** →
type `bad::label` in Base branch. Nothing under the box, **Create** stays
on, and the error only arrives as the server's 400 after filing. A
well-formed `octo-fork:feature-x` is not explained either. Classic New
task (`ui/src/forkHead.js`) explains the first and refuses the second
with Create disabled.

**Fix:** `ui-ng/src/app/create/fork-head.ts` ports classic's parser,
with the server's wording. The sheet reads the Base branch as a fork
head only when the task creates no branch of its own. It shows the help
line or the refusal under the box, marks the box `aria-invalid`, and
holds Create disabled while the label is malformed. `main` and other
plain branch names are left alone.

**Coverage:** `fork-head.spec.ts`, and "New task explains a fork head
label and refuses a malformed one before filing" in the new e2e spec.

## Tests run

- `npx vitest run src/app/tasks src/app/agents src/app/services
  src/app/inbox` in `ui-ng/`: 17 files, 215 tests pass. `npx vitest run
  src/app/create`: all pass, including `fork-head.spec.ts`.
- `npx playwright test e2e/next-tasks-intake-regressions.spec.js`: 3
  pass. The first two were also run with the source fixes stashed and
  both failed.
- `npx playwright test` (the whole `ui-ng` e2e suite, all fixes
  applied): 322 passed and 2 failed. The first failure was
  `next-tasks-batch-actions.spec.js` "Bulk edit sets a free Task
  priority…". It asserted that `1e3` is refused (added 2026-11-15 by
  task 5886 with no reason given), which is the behaviour Bug 2
  corrects. It now asserts `2.5` is refused instead. The second was
  `next-shell-sessions-walkthrough.spec.js` "the rail's Inbox link names
  its count apart from its label", which this change does not touch.
  Rerunning those two specs with `next-full-system.spec.js` (14 tests)
  passed every one.
- `npx tsc -p tsconfig.app.json --noEmit` and Prettier on every touched
  file.

## Rendering

The New task sheet with `bad::label` and with `octo-fork:feature-x`, and
`/next/tasks` in the cold-failure state, were shot at 1400×900 and
390×844 in light and dark. The file reader here cannot render PNGs and
there was no Gemini key, so the layout was checked by bounding boxes:

- Desktop: Base box 691×40 at x=685; the refusal 691×16 directly under
  it (5 px gap).
- Phone: box 342×40 at x=24; the refusal wraps to two lines, 342×32,
  within the box's width.
- No sideways overflow at either width.

None of the shots were committed.

## Observations (not treated as defects)

- **O1:** the Inbox's **All** and **Unread** tabs count rows listed (29),
  including the acknowledged alert, while the headline and rail count
  open decisions (28) since the 2026-11-15 fix. The tabs count what each
  filter shows, so they were left alone.
- **O2:** a change still on the merge queue (#5) is listed as stuck with
  no control. Classic's stalled group gives the same reason: what holds
  it is its merge-queue row. No Retry-merge stalled sub-case is seeded,
  so that control was not pressed.
- **O3:** Close on `/next/` still acts at once and takes no reason (as
  2026-11-15 O2). It is reversible with Reopen.
- **O4:** Edit's Save sends the whole form (title, description, repo,
  base, priority, model …) rather than only the changed fields.

## Gaps on `/next/` (no page or control yet)

Still as listed on 2026-11-14 and 2026-11-15, rechecked where noted:

- **Tasks:** the State/Admission/Human review/External ref/Origin
  attribute filters, `owner:any:me`, and server-side closed
  search/`owner`/`externalRef` (rechecked: query words are matched as
  free text). Retry merge, Submit, Reopen and Capability exist in the
  selection bar, behind More.
- **System → Pipeline:** still classic only (`/next/system/:tab`
  redirects). **Run selected now** and the "waits for Run now" row
  marker are now on `/next/tasks` (`next-tasks-batch-actions.spec.js`
  covers them). This run did not walk them, because the pipeline is
  switched to manual mode on the classic page.
- `/new?field.<name>=` links and the `settings/new-task-form.yml` notice.
- Boards (classic only).

## Not covered

- Host-script approvals, the pending-secret box, `request_access` cards,
  suspended `resume_later` sessions, cross-team approvals, the capability
  audit and the live transcript. The first three have no demo seed this
  run, and the 2026-11-15 run walked the access card.
- Pause / Pause now / Resume, Stop / Retry, dependencies, the goal card,
  attachments' full-screen viewer, Force merge and the config-change
  card. The 2026-11-15 run walked these; there was no time to walk them
  again in this run.
- Inbox section collapse, and role-gated read-only rendering.
