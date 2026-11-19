# User & system settings walkthrough — 2026-11-18 (task 7019)

- **Date:** 2026-11-18, 04:15–06:00 UTC, one run.
- **Task:** task 7019, branch
  `grain/53549e42/task-7019-walk-through-settings-its`, base `main`.
- **Commit tested:** `a6fdad20c` ("Merge pull request #1820"), then the
  fixes below on top of it. Each fix was rebuilt and checked again in the
  browser.
- **UI walked:** the new UI at `/next/settings/...` for every step. Settings
  is in `ui-ng/src/app/app.routes.ts` and `ui-ng/src/app/shell/nav.ts`, so
  no step fell back to a classic page. The classic UI was read only, to
  compare behaviour (`ui/src/components/settings/GeneralSection.jsx`,
  `AccessSection.jsx`). It was not walked and not changed.
- **Environment:**
  - Debian sandbox, `linux/amd64`, Go 1.26.10, Node.js v20.19.0.
  - Playwright from `ui-ng/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
    Viewports: 1400×900 (desktop), 820×1180 (tablet), 390×844 (phone).
  - Built with `make frontend`, then `go build -o /var/tmp/grain ./cmd/grain`.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. The file reader here shows a
    PNG as bytes, so layout was checked with bounding boxes and
    `scrollWidth`.
- **Servers.** Each ran over its own throwaway store (`TMPDIR` set to a
  fresh directory). Each was signed in through the real "Enter access
  code" page (email `alice@example.com` and the code) and opened at
  `/next/`:
  1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file> -as alice@example.com`: the base build.
  2. The same on `:8421` with
     `-external-config-repo https://github.com/example/walk-config` and
     without `-git-proxy`: external config repo mode.
  3. The fixed build on `:8430` (base flags) and `:8431` (external).
- **How it was driven:** throwaway Playwright scripts outside the checkout,
  acting only through visible controls: clicks, typing, `/`, `Escape`,
  `Tab`, reloads, Back/Forward and the rail. Each page recorded:
  - page errors, console errors and warnings;
  - failed requests, and `/api/` responses of 400 or more (with bodies);
  - every native dialog (dismissed unless the step accepted it);
  - every non-GET `/api/` request made from Preferences.

  `GET /api/whoami` and `GET /api/config` were read with the access code
  only to confirm what the UI showed.

## Stubs

None. The server, store, frontend, browser and gestures were all real, and
no route was intercepted. The demo's simulator covered the GitHub side.
The identity, upgrade, model-provider and config-repo pull-request
boundaries were not reached (see "Not covered"), so there was nothing to
stub.

## Steps and outcomes

### 1. Layout and paths: PASS

- The rail's single **Settings** link opens `/next/settings`, and bare
  `/settings` shows **Models** on desktop.
- Clicking General, then Agents, pushes history. Back returns to General,
  Forward to Agents, and a reload keeps `/settings/agents`.
- Phone (390 px): bare `/settings` shows only the grouped list. The groups
  are Models & Compute, Repositories & Code, Work & Agents, Access &
  Security, and Instance & Preferences. A section opens alone (drawer
  hidden) under **← All settings**, which returns to the list.
- Tablet (820 px): the side nav stays a 280 px list with no horizontal
  overflow. There is no searchable Section select (see Observations).
- Upgrade, Audit and CLI show no Save bar or state line.
- Old links: `e2e/next-settings-walkthrough.spec.js` covers them and
  passes. `/admin`, `/admin/workflow`, `/admin/executors`,
  `/system/executors`, `/admin/integrations`, `/system/integrations`,
  `/settings/credentials`, `/settings/secrets`, `/admin/upgrade` and
  `/settings/config-repo/<file>` each land on the page the task names.

### 2. Filter: PASS

- `/` focuses **Filter settings**.
- Each query lists these sections:

  | Query                   | Sections                 |
  | ----------------------- | ------------------------ |
  | `budget`                | Models, Agents           |
  | `max agent turns`       | Agents                   |
  | `environment name`      | General                  |
  | `lock`                  | General                  |
  | `time zone`, `timezone` | General                  |
  | `standing instructions` | Memory                   |
  | `auto-merge`            | Tasks                    |
  | `zzzqqq`                | `No settings sections match "zzzqqq".` |

- Escape clears the box and restores all five groups.
- The filter lists sections, not fields, as the last walkthrough recorded.

### 3. Save, role and lock model: PASS after fix 1

- An edit to Environment name brings up the Save bar (**1 unsaved change**,
  Discard, Plan / Preview changes, Save changes).
- The draft survives moving to another section, and leaving Settings by the
  rail and coming back.
- A full navigation away raised the `beforeunload` prompt. Dismissing it
  kept the page.
- Save cleared the bar, and a reload read `walk-env-7019` back. The test
  value was then restored.
- Max agent turns `-3` shows "Max agent turns must be a whole number of at
  least 0." and disables Save. `40` clears the error, and Discard puts the
  value back.
- External config repo (`:8421`):
  - General, Agents, Tasks and Memory show **Managed in config repo**, the
    banner, and **View in Config repo** →
    `/next/settings/config-repo?path=tables%2Fgrain_config.json`. Their
    switches are disabled.
  - Access, CLI, Audit, Models and Preferences have no pill. Access, Audit,
    Models and Preferences stay usable.
  - CLI's three run-location selects are disabled, because they are a
    deployment setting.
- **Bug 1 (fixed):** the **Lock settings in UI** switch was enabled in both
  servers.
  - On `:8420`, which has no config repo (`GET /api/config` has no
    `configRepo`), turning it on and saving succeeded. General, Agents,
    Tasks and Memory then said they were "managed in the config repository
    (tables/grain_config.json)", which does not exist. Its link leads to a
    file browser that answers 501.
  - On `:8421` the save was refused with 409 and the diff to commit to the
    external repository instead. The switch stayed drawn on, and the page
    then blocked the reload with an unsaved draft.
  - Classic General disables the switch in both cases. `/next/` does now
    too, with a hint saying why ("Unavailable without a config
    repository…" or "Set in the external config repository…"). It stays
    clickable without a config repo only while an old lock is on, so that
    lock can still be turned off.
- Role read-only: `next-qa-settings.spec.js` covers it and passes.

### 4. Preferences: PASS

- **Dark** writes `grain.themeMode=dark` to `localStorage` and survives a
  reload (`html.dark`).
- No non-GET `/api/` request was made from Preferences.
- The time zone select is on General, and Audit prints its times in that
  zone.

### 5. Access: PASS after fix 2

- The card shows "Your role: operator", and the rail's user tile shows
  `alice@example.com`.
- `GET /api/whoami` answers `alice@example.com (access code)`.
- Removing your own `user:alice@example.com` asks "Remove your own access
  entry?" (Cancel / Confirm self-removal). Removing any other entry asks
  "Remove <entry>?". A reload after cleanup shows the three seeded rows.
- **Bug 2 (fixed):** the entry box's placeholder was `alice@example.com`,
  a value the server refuses. Every refusal went to the banner at the top
  of the page, and in four of six refusals that banner was scrolled out of
  the viewport when Grant access was pressed. The refused entries were:
  - `walk-bob@example.com` and `bogus-no-prefix`: "must start with user:,
    group:, serviceAccount: or delegate:";
  - `serviceAccount:bot@example.com`: not `*.gserviceaccount.com`;
  - a service account with no reason;
  - `grain/operator` with no reason.

  `/next/` now checks the entry the way classic `validatePrincipalEntry`
  does (`ui-ng/src/app/settings/principal-entry.ts`), lowercases it, and
  requires a reason for a service account. Its own errors and the server's
  are shown under the Grant access row (`principal-entry-error`,
  `aria-invalid`), in view of the button. The placeholder is now
  `user:alice@example.com`.

### 6. CLI and Audit: PASS

- The CLI card shows these lines, and stays usable under the external
  config repo:

  ```
  curl -fsSL <origin>/cli/install.sh | sh
  grain login <origin> --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d
  grain skill install --jetski   # or --claude, --codex, --gemini
  ```

- Audit lists 37 rows, with timestamps in the display zone, actor, action
  and detail. No secret values are shown: secret rows name the path only.
- At 390 px, Audit's card is 356 px wide inside a 390 px page, with no
  page overflow.

### 7. General: PASS after fix 1

- The card has Environment name, Deployment ID, Display timezone, Settings
  lock and Broadcast. Updates and About are not on `/next/` General.

### 8. Config repo: PASS (demo limits)

- The panel, Sync now, the remote and KMS controls, and the file viewer
  render.
- `GET /api/state-repo/files` answers differently on each server:
  - On `:8420` it answers `501 this deployment's UI does not manage a state
    repository`.
  - On `:8421` it answers `500 internal`. The server log says: "grain demo:
    -external-config-repo names a repository and nothing more; there is no
    working tree here to sync, browse or reconfigure". That is the demo
    flag's own stand-in, not a deployment fault, so it was left alone.
- The sync-failure alert and held config-repo pull requests need a daemon
  with a real config repo and were not reached.

### 9. Models: PASS (read and confirm)

- The page has three cards: `card-model-defaults`, `card-model-backends`
  and `card-token-budget`.
- **Remove** on a connection asks "Remove connection gemini-pro?" with the
  fallback consequence.
- Under the external config repo, 7 of 29 controls are disabled and
  credentials stay editable. `next-settings-models.spec.js` passes.

### 10. Agents, Tasks, Memory: PASS after fix 3

- Agents: Max agent turns, Default capabilities and Standing console
  sessions.
- Tasks: Queue new tasks immediately and Auto-merge (both on), Show closed
  tasks (off), follow-up limits, branch template, and quality control.
- Memory: a correction to the deployment memory saved, and a reload read it
  back. The original text was then restored and read back again.
- **Bug 3 (fixed):** an unsaved edit to the deployment memory was dropped
  silently when leaving by the rail, with no prompt. Returning showed the
  saved text. Another section, Back or a reload did the same, because the
  memory document is not part of the settings draft.
  - Settings now tracks it (`SettingsStateService.memoryDirty`) and asks
    "Leave Memory without saving your edit?" before the router leaves:
    `canDeactivate` on the three settings routes (`pageLeaveGuard`).
  - A reload or closing the tab now raises `beforeunload`.
  - Cancel keeps the reader and the edit on Memory. A clean Memory never
    asks.

### Console and network

- Each sign-in logs one expected `401` for the unauthenticated first load.
- Config repo logs the demo's `501` (or `500` under
  `-external-config-repo`), described in step 8.
- Every other 4xx was a refusal a step asked for.
- There was no uncaught page error and no other 5xx.

## Bugs

All three are fixed in `ui-ng/`. None is broken in the classic UI, which
already does each of these right, so `ui/` is unchanged.

1. **The settings lock was offered where it means nothing.**
   - Without a config repo it locked sections to a repository that does
     not exist. Under an external config repo the server refuses it (409).
   - Covered by `e2e/next-settings-lock-access-memory.spec.js` (no config
     repo) and `e2e/next-settings-lock-external.spec.js`.
2. **Grant access invited and hid refusals.**
   - The placeholder was a value the server refuses, and refusals were
     shown out of view.
   - Covered by `e2e/next-settings-lock-access-memory.spec.js` and
     `src/app/settings/principal-entry.spec.ts`. The unit test in
     `settings-page.component.spec.ts` had sent that bare email; it now
     asserts the bare email is caught and the canonical entry is posted.
3. **An unsaved Memory edit was dropped without asking.**
   - Covered by `e2e/next-settings-lock-access-memory.spec.js` (Cancel
     keeps it, another section asks, a clean Memory does not).

## Observations (no change; `/next/` has not ported these yet)

These were already listed by the 2026-11-14 walkthrough and are unchanged
on this commit:

- The per-field commit-on-change/blur model, the header state line, and the
  dirty dot. `/next/` uses a section draft with a Save bar instead.
- The tablet Section select.
- Field-level filter results that focus and outline a field.
- The Restart needed chip.
- Customize pinned items (boards, sessions, System tabs). `/next/` offers
  rail show, hide and reorder instead.
- Access's This browser card and Log out, the separate People & groups and
  Service accounts tables, and added-at timestamps.
- General → Updates and About.
- Tasks → Task fields and Default grants.
- Agents → Task branches validation and the External agents card.
- Models' Test / Enable / Disable, ambient credentials, Run on host
  placement, and Where each agent CLI runs.
- Memory's per-repository notes.
- CLI's Copy buttons and the `Accept external agents` warning.

## Not covered

- No model key, so no provider connection test or model call.
- Identity/IAP sign-in, `Access Denied` and Log out. The demo signs in with
  the access code, and `/next/` has no Log out.
- A controller-local config repo, held config-repo pull requests and
  sync-failure alerts. They need `grain daemon` with a real `grain/config`.

## Tests run

- `npx playwright test e2e/next-settings-lock-access-memory.spec.js
  e2e/next-settings-lock-external.spec.js --project=desktop`: 4 passed.
- `npx playwright test e2e/next-settings-walkthrough.spec.js
  e2e/next-qa-settings.spec.js e2e/next-full-system.spec.js
  e2e/next-processes-unsaved-and-runs.spec.ts
  e2e/next-settings-models.spec.js`: 29 passed. The Processes spec was
  included because its leave guard was renamed `pageLeaveGuard`.
- `npx vitest run src/app/settings`: 65 passed. `npm run lint` is clean.
- Screenshots: `ui/e2e/screenshot.mjs` against the base (`:8420`) and fixed
  (`:8430`) demos for `/next/settings/{general,access,memory}` at desktop
  and phone, light and dark. A scripted shot of the Access refusal at 1400
  and 390 px was checked with bounding boxes:
  - At 1400 px the error sits at x=409, the input's left edge, between the
    entry row (y 624–664) and Grant access (y 732).
  - At 390 px it wraps within 324 px, between the fields and the button.
  - `scrollWidth` equals the viewport at both widths.
