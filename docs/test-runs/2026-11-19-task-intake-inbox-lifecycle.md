# Task intake, inbox, and lifecycle UI walkthrough (2026-11-19)

- **Date:** 2026-11-19, 03:03–04:05 UTC, one run.
- **Task:** task 7304, branch
  `grain/53549e42/task-7304-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `17339dd82` ("Merge pull request #1885", including newly
  landed `#1538` New task Owner/Keep running/Task priority/No repo/fork heads,
  `#1581` Task Edit Owner/External ref/Reviewers/Admission/Hosting/Keep running/
  Fixes red base, `#1854` Retry merge primary header action, `#1660` `#comment=`
  deep links, `16d3527c2` Batch Defer until…, `ee4744df8` reversible Close
  optional reason, and `2ecb73388` capability-policy verdicts in grants
  pickers). Each fix below was re-verified on a rebuild of `17339dd82` with
  the fix applied.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`; Node.js
    v20.19.0; Playwright from `ui-ng/package-lock.json` with headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`); timezone UTC; viewport
    1440×900 desktop and 390×844 phone in light and dark themes.
  - Built with `make frontend grain` (`npm ci` run in `ui-ng/` and `ui/`
    afterwards because `make frontend` uses `npm ci --omit=dev` which drops
    `@playwright/test`). Fixed builds re-ran `cd ui-ng && npm run build` and
    `CGO_ENABLED=0 go build -o bin/grain ./cmd/grain`.
  - Servers:
    - `:8460` (base build): `bin/grain demo -addr 127.0.0.1:8460 -open=false
      -github-sim -fake-ci -scheduled-workflows -fork-pull-request -git-proxy
      -base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
      -sensitive-hold -review-questions -human-review -secrets-store
      -notifications -qualification -mechanical-port -config-change
      -access-code-file /tmp/wt/access-code`.
    - `:8461` (fixed build): `bin/grain demo -addr 127.0.0.1:8461 -open=false
      -access-code-file /tmp/wt/access-code-fixed` for layout/bounding-box
      measurements, plus `ui/e2e/screenshot.mjs` spinning up its own throwaway
      `grain demo` instances for `before` and `after` contact sheets.
    - Each server used a fresh throwaway `TMPDIR` embedded SQLite store.
  - No `/home/debian/.gemini-api-key` was present, so no model was called and
    `LOOK=1` was unavailable.
- **How it was driven:** throwaway Playwright scripts kept under `/tmp/wt/`
  (outside the repository). Each signed in via `POST /api/auth/access-code`
  (`ui-shared/e2e/accesscode.js` `signIn`) and interacted strictly through
  visible UI controls while logging uncaught `pageerror`s, `console.error`s,
  failed requests, `/api/` responses ≥ 400, native dialogs, and non-GET `/api/`
  payloads. Every mutation was verified via reload and REST API read-back.
- **Console baseline:** zero uncaught `pageerror`s and zero `console.error`s
  across all `/next/` pages. The only failed requests observed were
  `net::ERR_ABORTED` on in-flight `GET /api/config` or `GET /api/tasks` during
  immediate route navigation.

## Stubs

The server, SQLite store, real throwaway `grain/config` repository, built
frontends, Chromium browser and user gestures were real throughout. Four
external/model boundaries were stubbed or seeded:

1. **Base-branch red/green verdict:** `POST /api/e2e/base-branch/{red,green}`
   (`-base-branch-ci`) toggled `main` red and back to green through grain's
   real base-branch reconciler (which also automatically closed our
   `fixesBaseBranch: true` tasks `#42` and `#43` with `"goal met (base-green)"`).
2. **Pending access request:** `POST /api/e2e/tasks/42/pending-access`
   (`pkg/demo/demo_access_request.go`) parked `#42` on a real
   `model.PendingAccessRequest` with `network: open` and dangerous
   `https://grain.dev/capabilities/api#tasks:admin`.
3. **Read-only role (`grain/viewer`):** Playwright `page.route` rewrote the
   `access` block of `GET /api/config` to
   `{role: "grain/viewer", roles: ["grain/viewer"], full: false, scopes: ["tasks:read"]}`
   because an access-code session always authenticates as the deployment owner
   on the server.
4. **Host-script approval in Inbox:** Playwright `page.route` stubbed
   `GET /api/local-script-approvals` and
   `POST /api/tasks/3/local-script-approvals/hs-1/answer` to verify the
   host-script Inbox group, stdin preview, and Approve/Deny actions without a
   running model/host-script invocation.
5. **Cold `/api/tasks` failure & mid-session polling:** Playwright `page.route`
   returned `500` on `GET /api/tasks` to exercise the cold-load failure state.

## Which UI each area ran against

| Area                                                 | UI                                            |
| ---------------------------------------------------- | --------------------------------------------- |
| Inbox (groups, inspector, inline & bulk actions)     | `/next/inbox`                                 |
| Inbox collapsible sections (`Enter`/`Space`, reload) | Classic `/inbox` (no `/next/` collapse yet)   |
| New task sheet (rail `+` and Create menu, templates) | `/next/` sheet (`NewTaskSheetComponent`)      |
| `/new?field.<name>=` prefill links                   | Classic `/new?...` (no `/next/new` route yet) |
| Tasks list, tabs, search, sort, batch bar            | `/next/tasks`                                 |
| Tasks advanced attribute filters & unknown-value UX  | Classic `/tasks` (no `/next/` filter bar yet) |
| Task detail, Edit panel, lifecycle, specialized cards| `/next/task/<id>`                             |
| Committed context (`#committed-context`)             | Classic `/tasks/1` (not yet on `/next/task/`) |
| Pipeline manual mode toggle + `Run selected now`     | `/next/fleet?tab=controls` + `/next/tasks`    |
| Standing session controls                            | `/next/sessions/director`                     |

## Steps and outcomes

### 1. New task intake (`/next/` New task sheet & classic `/new`)

| Step | Action                                                                                                     | Result                                                                                                                                                                                      |
| ---- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1.1  | Open Create menu (`rail-create-menu`) → **New task** (`create-new-task`); also direct `+` (`rail-new-task`) | Create menu lists 8 destinations; sheet opens with focus on title, `scrollWidth - clientWidth = 0` at 1440px and 390px.                                                                     |
| 1.2  | **Task priority**: enter `0`, `-1`, `1.5`, real-typed `1e` (`badInput`), then `1e3`                        | `0`, `-1`, `1.5`, and `1e` all refuse inline with `"a whole number, 1 or more; default 5, higher numbers run first"` and disable submit; `1e3` normalizes to `1000` and enables submit.     |
| 1.3  | Untick **Create a new branch**, test valid fork head `contrib:fix/readme-typo` and malformed labels        | `contrib:fix/readme-typo` accepted and explained; `o/c:x`, `:branch`, `owner:`, `a:b:c`, `contrib:bad..ref` refused locally with submit disabled. `octo cat:x` not refused locally (O2).    |
| 1.4  | Switch **Repository** to **No repository (answer in a comment)**                                           | Hides Base branch, Auto-merge, Don't port, and Fixes a red base branch; switching back restores them.                                                                                       |
| 1.5  | Model picker (`nt-model`)                                                                                  | Lists `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash` (matching `config.models`).                                                                               |
| 1.6  | Grants builder: expand capabilities → **+ Add API scope or method grant** → open API scope set → `Escape`  | Lists canonical API scope sets; first `Escape` closes only the listbox menu while sheet and builder remain open; adding an API grant increments held grants cleanly.                        |
| 1.7  | Refused field inside collapsed **More options**: set `nt-found-in` to `not-a-semver-tag`, collapse, submit | Submission reopens **More options** and surfaces `"Use a release tag such as v3.6.0 or v3.6.0-rc.2."`.                                                                                      |
| 1.8  | File full queued task (`#42`) with priority `1e3`, Owner, Keep running, Don't port, Fixes red base, Reviewers, custom fields, PNG attachment, Mermaid | `POST /api/tasks` 201; persisted `#42` `queued`, `priority: 1000`, `owner: "alice@example.com"`, `keepRunning: true`, `noPort: true`, `fixesBaseBranch: true`, `reviewers: ["bob@example.com"]`, custom fields and 1 attachment intact. |
| 1.9  | File proposed task (`#43`) with **Merge on its own** unticked and **Approve and start now** off            | Button reads **Save as proposal**; `POST /api/tasks` sends `autoMerge: false`; persisted `#43` `proposed`, `autoMerge: false`.                                                              |
| 1.10 | Templates: pick `Set up a GKE sandbox cluster` (4 input fields) and a priority-8 template across draft restore | GKE template exposes its input fields; priority-8 template carries `priority: 8` through draft close/reopen until manually edited to `3`, after which re-picking the template keeps `3`. Cancel closes cleanly. |
| 1.11 | Save deny rule on `https://grain.dev/capabilities/github#*` in `/next/settings/capabilities#capability-policy`, open New task, file with defaults | Picker displays `denied by policy` badge + note linking to `/next/settings/capabilities#capability-policy`; server returns 201 with `droppedDefaults`. **Bug 1:** filing toast ignored `droppedDefaults` completely (fixed below). |
| 1.12 | Deep-link `/next/new?field.priority=high` vs classic `/new?field.priority=high&field.requested-by=link-user` | `/next/new` falls through `**` to `/next/inbox`; classic `/new?...` pre-fills `requested-by=link-user` and `priority=high`.                                                                 |

### 2. Task detail, Edit panel, lifecycle, and specialized cards (`/next/task/<id>`)

| Step | Action                                                                                                     | Result                                                                                                                                                                                      |
| ---- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2.1  | Open `#43` → **Edit**: test priority (`0`/`-1`/`1.5`/`1e`/`1e3`), title-only PATCH, then all newly landed fields | Explicit `autoMerge: false` preserved unticked; title-only PATCH omits unchanged `owner` and `autoMerge`; editing Owner, External ref (`WT-901`), Reviewers, Admission (`bypass`), Hosting (`console`), Keep running, Fixes red base and priority `1e3` persists all fields. **Bug 2:** `externalRef` never displayed anywhere on `/next/task/<id>` and `/next/tasks` did not match `externalRef` (fixed below). |
| 2.2  | Console session `#13` → **Edit**                                                                           | Admission picker is disabled with explanatory help text.                                                                                                                                    |
| 2.3  | Approve `#43`, Defer (no date), Undefer, Defer until `2026-11-25`, Undefer, Close with long unbroken reason, Reopen | All state transitions persist; inline close confirm accepts optional reason; Details → History records principal + reason and wraps unbroken text with `0px` overflow at 390px phone width. |
| 2.4  | Pause / Resume queued `#42`; More → **Pause immediately** / Resume on running `#3`                         | `#42` keeps `queued` state when paused; `#3` **Pause immediately** confirms before discarding unpushed work, sets `paused.immediate: true`, and Resume clears pause cleanly.                |
| 2.5  | Stop active turn on running `#8` → **Retry**                                                               | `#8` moves to `awaiting_reply/stopped`; pressing **Retry** confirms `"Resume #8? alice@example.com (access code) stopped it."` and `POST /api/tasks/8/retry` returns `#8` to `queued`.      |
| 2.6  | `#29` (`merge_failed`) header **Retry merge** (PR `#1854`)                                                 | Header shows `"Merge failed"` oval and primary **Retry merge** button; clicking transitions `#29` live without reload.                                                                      |
| 2.7  | `#5` (`waiting_ci`, PR `#101`) Changes tab → **Force merge…** and **Re-run CI**                            | Force merge dialog requires ticking unsatisfied gate (`ci`) and reason; submits `POST /api/tasks/5/force-merge` cleanly; **Re-run CI** submits `POST /api/ci/runs`.                         |
| 2.8  | Post comment on `#42` with 2 PNG attachments + valid & invalid Mermaid diagrams; open `/next/task/42#comment=35` | Valid Mermaid renders SVG with Diagram/Source toggles, invalid block falls back cleanly; thumbnails render (open in new tab, no lightbox yet — O3); `#comment=35` applies `.comment-highlight`. |
| 2.9  | Goal section on `#13` and Edit → Goal (`on_limit`: `hold` → `notify` → Clear) on `#42`                     | `PUT`/`DELETE /api/tasks/42/goal` persist; Details Attributes shows `Goal`; clearing removes the goal cleanly with no stale card after reload.                                              |
| 2.10 | Dependencies on `#42?tab=details`: add `#2`, inspect hover title, remove `#2`                              | Chip renders `#2` with title `"#2 Fix flaky retry test — Waiting (blocking this task)"`, sets `blocked: true`; removing clears `dependsOn` and `blocked`.                                   |
| 2.11 | Workstream tree on `#8?tab=tree`                                                                           | Shows nested tree (`#13` → `#1`, `#3`, `#8` → `#11`); `#8` is not self-linked inside the tree panel.                                                                                        |
| 2.12 | Committed context on `#1`                                                                                  | Not rendered on `/next/task/1` (classic `/tasks/1#committed-context` renders target repo badge, `@9f4c2b81e7`, `Theme Switcher Spec`, `spec.md`, `settings-mock.html` — follow-up proposed). |
| 2.13 | Holds: `#34` Human-review (**Release hold** + `#35` link), `#30` Sensitive/path-trigger (`#31` link + cross-team approvals), `#32` Review questions (`#33` link) | `#34` **Release hold** submits `POST /api/tasks/34/human-review/release` and clears hold; `#30` shows hold, `#31` link and approvals; `#32` shows review comment (open questions are findings on the review gate rather than a "Ship as is" bypass). |
| 2.14 | Merge policy lines (`#28?tab=changes`) & Changelist tasks (`#14`–`#19`)                                    | `#28` shows `deploy` merge rule and `cla/google` check; CL tasks enable Submit only when submittable (`#15`), disabling `#14`/`#16`/`#17`/`#18`/`#19`.                                     |
| 2.15 | Inline config-repo change card against real `grain/config`: `#39` **Approve & merge config**, `#40` **Reject…** | `#39` renders diff (`poll_interval_ms: 10000 -> 30000`) and approves via `POST /api/tasks/39/config-change/approve` (`headSha`); `#40` rejects with reason via `POST /api/tasks/40/config-change/reject` and deletes proposal branch. |
| 2.16 | Pending `request_access` card on `#42` (seeded via Stub 2)                                                 | Shows `dangerous` badge on `api#tasks:admin`; unticking flips **Grant all** → **Grant 1 of 2**; **Deny…** keeps submit disabled until reason entered; second ask allows partial `Grant 1 of 2` (`network: open` only). Note: card does not yet render `pa.holds` or `pa.earlier` (follow-up proposed). |
| 2.17 | Suspended session `#23`, Runs tab `#8` (tokens, transcripts, capability audit), `#6` Changes tab PR link   | `#8` Runs tab shows token usage, Transcript and `Audit (3)` views; `#6` links `acme/widgets#102` and `/next/ci`.                                                                            |

### 3. Inbox (`/next/inbox` & classic `/inbox`)

| Step | Action                                                                                                     | Result                                                                                                                                                                                      |
| ---- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 3.1  | Open `/next/inbox` via rail; inspect groups, counts, keyboard `j`/`k`, `keep_running` proposal (`#48`)     | Headline (`25 decisions waiting on you`), filter chips (`All 26`, `Asked`, `Releases & scripts`, `Merges`, `Proposals`, `Blocked & alerts`, `Paused`, `Unread`), and groups render cleanly. First `j` pins row 0 by design (`inbox-page.component.spec.ts` safety rule); second `j` steps to row 1. `/next/inbox` rows do not yet show a `keep_running` chip (O4). |
| 3.2  | Pending-release alert `#2` (`v2.4.0`), alert `#1` (**Acknowledge**), stalled `#27` (**Undefer**)           | `#2` shows soak summary (`25h10m of 24h: PASS`), **Approve**, **Reject**; `#1` **Acknowledge** sends `POST /api/notifications/1/ack`; `#27` **Undefer** moves `#27` to `queued` and removes it from Inbox. |
| 3.3  | Approve proposal `#12` (imported issue); answer parked question `#4`                                       | `#12` **Approve** → `queued`; `#4` reply sends `POST /api/tasks/4/comments` → `queued`.                                                                                                     |
| 3.4  | Host-script approval (Stub 4)                                                                              | Renders in host-script group with `stdin` preview (`echo hello from stdin preview`) and **Approve** / **Deny**; Approve posts `{"approve":true}`.                                           |
| 3.5  | Group bulk actions: **Approve all**, **Clear all alerts**, batch bar **Defer all**                         | Every bulk action opens `<g-confirm-dialog>` with accurate scope breakdown and action verb (Defer says `"deferring"`, not `"approving"`).                                                   |
| 3.6  | Cold `/api/tasks` `500` failure (Stub 5)                                                                   | Shows `"At least 3 decisions waiting on you"` and error alert; never claims nothing is waiting.                                                                                             |
| 3.7  | Read-only viewer (`grain/viewer`, Stub 3)                                                                  | Zero write buttons enabled in resolution dock or group headers; displays `rbac-denied` explanation.                                                                                         |
| 3.8  | Classic `/inbox` collapsible sections (`Enter`/`Space` + reload)                                           | `Notifications` starts collapsed (`aria-expanded="false"`); `Enter` expands, survives reload, `Space` collapses again.                                                                      |

### 4. Tasks list, batch bar, and Pipeline manual mode (`/next/tasks`, `/next/fleet?tab=controls`, classic `/tasks`)

| Step | Action                                                                                                     | Result                                                                                                                                                                                      |
| ---- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 4.1  | Open `/next/tasks` via rail; inspect tabs, sorts, search, Closed tab, row ovals                            | Tabs (`Backlog`, `Needs approval`, `Running`, `Asked`, `Submit & land`, `Deferred`, `Closed`, `All`), case-insensitive repo search (`ACME/WIDGETS`), non-default priority (`Urgent 1000`), `Merging`, and Closed tasks render cleanly. **Bug 3:** blocked task `#11` showed `"Waiting"` on All tab (and `"Queued"` on Backlog) instead of `"Blocked"` (fixed below). |
| 4.2  | Batch bar: **Defer until…** (`2026-11-26`), **Bring back**, **More → Bulk edit…** (Task priority `0` refused, `1e3` accepted, `7` applied), **More → Capability…**, **Close** (Cancel then confirm), **Closed → Reopen** | All batch operations persist and update selection cleanly; Cancel keeps selection and tasks intact.                                                                                         |
| 4.3  | **Pipeline manual mode**: `/next/fleet?tab=controls` → **Pause pipeline** → `/next/tasks` → select `#41` → **Run selected now** → reload → **Resume pipeline** | `POST /api/pipeline-pause` sets `paused: true, runNowPasses: true`; queued eligible rows show `"Paused — waits for Run now"` (`waits-run-now`); **Run selected now** posts `POST /api/tasks/41/run-now` (`runNowAt` set) and marker does not return on reload; **Resume pipeline** restores dispatching. |
| 4.4  | Classic `/tasks` attribute filters & unknown filter value (`?state=bogus_state_xyz`)                       | Renders `imported` chip, `Blocked` oval, and handles unknown state filter without error.                                                                                                    |

## Bugs found and fixed

### Bug 1 — `/next/` New task sheet ignored `created.droppedDefaults` on filing

- **Reproduction:** Save a capability-policy `deny` rule on
  `https://grain.dev/capabilities/github#*` in `/next/settings/capabilities#capability-policy`.
  Open the `/next/` New task sheet, enter a title, leave capabilities at
  defaults, and press **Create and start**. `POST /api/tasks` returns `201`
  with `droppedDefaults: [{ grant: "https://grain.dev/capabilities/github#read-issues", reason: "..." }]`,
  but the snackbar said only `"Filed #45; it starts when a slot is free"`,
  giving no notice that a default grant was stripped by policy (where classic's
  `DroppedDefaultsNotice.jsx` warns the operator for 15 seconds).
- **Fix:**
  - Added `DroppedDefaultGrant` and `droppedDefaults?: DroppedDefaultGrant[]`
    to `Task` in `ui-ng/src/app/api/models.ts`.
  - Updated `NewTaskSheetComponent.announce()`
    (`ui-ng/src/app/create/new-task-sheet.component.ts`) to name dropped
    default grants (and the denial reason for a single dropped grant) in the
    filing snackbar with a 15s duration while preserving `Open` (and `Discard`
    for proposals).
  - Added `overflow-wrap: anywhere` to `.snackbar-text` in
    `ui-ng/src/styles.css` so long canonical grant URLs stay inside the
    snackbar at 390px phone width.
- **Coverage:** Unit test in
  `ui-ng/src/app/create/new-task-sheet.component.spec.ts` and Playwright e2e
  in `ui-ng/e2e/next-intake-ref-blocked.spec.ts` (desktop, phone, and dark,
  including viewport containment bounding-box check).

### Bug 2 — `/next/task/<id>` never displayed `externalRef` and `/next/tasks` did not filter or search by it

- **Reproduction:** Open `/next/task/8` (seeded with `externalRef: "CA-204"`)
  or edit **External ref** to `WT-901` on `/next/task/43` (added in PR `#1581`),
  save (`PATCH /api/tasks/43` persists `"externalRef": "WT-901"`), and reload.
  Neither the task header nor Details → Attributes showed `externalRef`, and
  typing `CA-204` into `/next/tasks`'s Filter box matched zero tasks.
- **Fix:**
  - Rendered `t.externalRef` in `TaskDetailComponent`
    (`ui-ng/src/app/task/task-detail.component.ts`, `.css`) in the header top
    line (`data-testid="task-external-ref"`, `ref CA-204`) and in Details →
    Attributes (`data-testid="task-attr-external-ref"`, `CA-204`), each linking
    to `/next/tasks?tab=all&externalRef=<ref>`.
  - Added `externalRef` query-param input, clearable `ref: <value> ×` chip
    (`data-testid="tasks-ref-chip"`), case-insensitive exact `externalRef`
    filtering, and free-text `externalRef` matching in
    `TasksPageComponent` (`ui-ng/src/app/tasks/tasks-page.component.ts`).
- **Coverage:** Unit tests in `task-page.component.spec.ts` and
  `tasks-page.component.spec.ts`, plus Playwright e2e in
  `ui-ng/e2e/next-intake-ref-blocked.spec.ts` (desktop, phone, dark).

### Bug 3 — `/next/tasks` and `/next/task/<id>` showed `"Waiting"` / `"Queued"` instead of `"Blocked"` for blocked tasks

- **Reproduction:** Open `/next/tasks?tab=all`, `/next/tasks`, or
  `/next/task/11` (`#11` is `queued`, `blocked: true`, depending on `#8`). On
  the **All** tab and the task page header, `#11` wore a neutral `"Waiting"`
  oval with no blocked indication at all; on **Backlog** it wore `"Queued"`
  beside a separate neutral `"Waits on a dependency"` chip. Classic's
  `ui/src/state.js` renders `"Blocked"` as its own state oval.
- **Fix:**
  - Added `blockedWaiting(t)` in `ui-ng/src/app/tasks/task-view.ts` and
    updated `taskStateLabel(t)` to return `"Blocked"` when `blockedWaiting(t)`
    is true (matching `mergeStuck`).
  - Updated `TasksPageComponent` (`toneOf`, list, timeline, and backlog rows)
    and `TaskDetailComponent` (`tone`) to style blocked queued tasks in the
    warning (`blast`) token with `<g-status-icon tone="blast" kind="warning" />`.
- **Coverage:** Unit tests in `task-view.spec.ts`,
  `tasks-page.component.spec.ts`, and `task-page.component.spec.ts`, plus
  Playwright e2e in `ui-ng/e2e/next-intake-ref-blocked.spec.ts` (desktop,
  phone, dark).

## Rendering and layout verification

Captured `before` (base build) and `after` (fixed build) screenshots via
`ui/e2e/screenshot.mjs` across `/next/tasks`, `/next/tasks?tab=all`,
`/next/tasks?tab=all&externalRef=CA-204`, `/next/task/11`, and `/next/task/8`
at desktop (1440×900) and phone (390×844) widths in light and dark themes
(kept in `/tmp/wt/screenshots/`, not committed; `contact-sheet.png` attached
to the closing note). Because no Gemini key was available for `LOOK=1`,
rendered geometry and tokens were checked directly in Chromium:

- `documentElement.scrollWidth - clientWidth` was `0` on every page at 1440px
  and 390px in both light and dark themes.
- **Blocked oval** (`row-11` on Backlog & All): `79×24`, `0px` internal
  overflow; task header (`task-state` on `/next/task/11`): `63×24`, `0px`
  overflow; light `rgb(61, 40, 0)` on `rgb(254, 240, 195)`, dark
  `rgb(255, 223, 153)` on `rgb(74, 52, 0)`.
- **External ref links** (`task-external-ref` `69×15`,
  `task-attr-external-ref` `47×15`): primary link token (`rgb(11, 87, 208)`
  light, `rgb(168, 199, 250)` dark), `0px` overflow.
- **Tasks ref filter chip** (`tasks-ref-chip`): `112×40`, `0px` overflow,
  secondary-container token (`rgb(0, 29, 53)` on `rgb(194, 231, 255)` light,
  `rgb(194, 231, 255)` on `rgb(0, 74, 119)` dark).
- **Dropped-defaults snackbar**: stays strictly within viewport width on both
  desktop (1440px) and phone (390px) with wrapped canonical grant URL.

## Tests run

- `ui-ng/`: `npx tsc -p tsconfig.app.json --noEmit`, `npm run lint`,
  `npm run format:check`, and `npm test` (all 76 test files, 831 tests pass).
- `ui-ng/e2e`: `next-intake-ref-blocked.spec.ts` (6/6 pass across desktop,
  phone, dark), plus `next-full-system.spec.js`, `next-merge-failed-state.spec.js`,
  `next-capability-policy-pickers.spec.js`, `next-retry-merge-header.spec.ts`,
  `next-defer-until.spec.js`, `next-task-edit-fields.spec.js`,
  `next-inbox.spec.js`, and `next-tasks.spec.js` (38 passed + 6 passed across
  desktop, phone, dark).
- `go test ./tests/hygiene/...` verified clean.

## Observations & remaining `/next/` gaps (follow-ups proposed)

- **O1 (`pendingAccess.holds` & `earlier` on `/next/task/<id>`):** The server
  returns `holds` (what the task already holds) and `earlier` (prior decisions
  on repeated items) on `AccessRequestView`, but `ui-ng`'s `AccessRequestView`
  and `task-gates-bar.component.ts` do not render them yet. Follow-up proposed.
- **O2 (`fork-head.ts` whitespace + colon):** `"octo cat:x"` is not refused
  locally in `parseForkHead` (leaves submit enabled and fails with a server
  400), whereas `"o/c:x"` is refused locally.
- **O3 (Committed context, image lightbox, Mermaid zoom/fullscreen):**
  `/next/task/<id>` still does not render `#committed-context` (manifest files
  and `@<rev>` links — still classic-only), image thumbnails still open in a
  new tab without a full-screen lightbox viewer, and Mermaid fences have
  Diagram/Source without zoom/fullscreen controls. Follow-up proposed for
  Committed context on `/next/task/<id>`.
- **O4 (`/next/inbox` collapsible groups, `keep_running` row markers, CL inline
  Mail/Publish/Sync actions):** `/next/inbox` groups are not yet collapsible
  (Alerts starts open), Inbox rows do not render a `keep_running` chip, and
  changelist rows `#14`–`#19` expose only `Submit` in the resolution dock
  rather than inline `Mail`, `Publish replies`, and `Sync` buttons.
- **O5 (`/next/tasks` URL sort persistence & structured attribute filters):**
  Clicking `sort-newest` on `/next/tasks` does not update the URL query, and
  structured filters (`State`, `Admission`, `Human review`, `Origin`,
  `owner:any:me`, closed-task server-side search, `imported`/external-PR/
  held-for-approval chips) remain classic-only.
- **Not covered without a live model or dedicated demo hook:** Live streaming
  attempt transcript updates, end-to-end secret decryption in a resumed run,
  `settings/new-task-form.yml` omitted-link-value notice on classic `/new`,
  and cross-team approval withdrawal (no seeded withdrawal button was offered
  on `#30` without an active operator approval).
