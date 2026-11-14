# User & system settings walkthrough — 2026-11-14 (task 5634)

- **Date:** 2026-11-14, 11:46–12:45 UTC, one run.
- **Task:** task 5634, branch
  `grain/53549e42/task-5634-walk-through-settings-its`, base `main`.
- **Commit tested:** `3266698ea` ("Merge pull request #1455"), then the
  fixes below on top of it (each rebuilt and checked again in the browser).
- **UI walked:** the new UI at `/next/settings/...` for every step. Settings
  is in `ui-ng/src/app/app.routes.ts` and `ui-ng/src/app/shell/nav.ts`, so
  no step had to fall back to a classic page. The classic UI at `/settings`
  was not walked and not changed.
- **Environment:**
  - Debian sandbox, `linux/amd64`, Go from `/usr/local/bin/go`, Node.js
    v20.19.0.
  - Playwright 1.62.1 from `ui-ng/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
    Viewports: 1400×900 (desktop), 820×1180 (tablet), 390×844 (phone).
  - Built with `make frontend`, then `go build -o /var/tmp/grain ./cmd/grain`.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe the screenshots. Layout was checked with
    bounding boxes and `scrollWidth` instead.
- **Servers.** Each ran over a throwaway store (`TMPDIR` set to a fresh
  directory) and was signed in through the real "Enter access code" page as
  `alice@example.com`:
  1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file>`: the base build.
  2. The same on `:8421` with `-external-config-repo https://github.com/example/walk-config`: external config repo mode.
  3. The fixed builds on `:8430` and `:8440`.
- **How it was driven:** throwaway Playwright scripts outside the checkout,
  acting only through visible controls (clicks, typing, `/`, `Escape`,
  `Enter`, reloads, Back/Forward). Each page recorded page errors, console
  errors and warnings, failed requests, `/api/` responses of 400 or more
  (with bodies), and every native dialog. The REST API (bearer access code)
  was used only to read back state and to restore the demo's allowlist
  after Bug 5 removed `user:alice@example.com`.

## Stubs

The server, store, frontend, browser and gestures were all real. Only one
route was intercepted:

| Route                                                       | Why                                                                                 |
| ----------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `POST /api/settings` → one `503 {"error":"stubbed outage"}` | To see the save-failure banner and a retry. The next press went to the real server. |

The GitHub, identity, upgrade, model-provider and config-repo boundaries
were not stubbed. The demo's simulator covered what the walk reached, and
the identity/IAP and config-repo pull-request flows were not reached (see
"Not covered").

## Steps and outcomes

### 1. Layout and paths — PASS after fixes 1–2

- The gear in the header rail opens `/next/settings`. Bare `/settings`
  opens **Models** on desktop. At 390 px it opens the grouped section list
  (Models & Compute, Repositories & Code, Work & Agents, Access & Security,
  Instance & Preferences). A section then opens alone under a back arrow
  (`settings-mobile-back`), with no horizontal overflow (`scrollWidth` 390).
- At 820 px the side nav stays a list. There is no searchable Section
  select, so there is no discard prompt to cancel (see Observations).
- Clicking a section pushes `/settings/<section>`. Reload keeps it, and
  Back/Forward move between sections.
- `/settings/hosts` and `/settings/hostds` open Hosts, `/system/upgrade`
  opens Upgrade, and `/config?path=<file>` opens the Config repo file
  viewer.
- **Bug 1 (fixed):** on `/next/`, `/admin`, `/admin/workflow`,
  `/admin/executors`, `/admin/integrations` and `/admin/upgrade` opened the
  **Inbox**. `/system/executors` and `/system/integrations` opened
  **Fleet**, and `/settings/config-repo/<file>` opened the Inbox. All now
  land on their Settings page; the full list is under Bug 1 below.
- **Bug 2 (fixed):** `#card` fragments did nothing on `/next/`. Now
  `/settings/models#usage`, `/settings/access#external-agents` and
  `/settings/capabilities#credentials` scroll the card to its heading once
  the section loads, retrying for up to 4 s while it renders.
- `/memory` opens **Context** on `/next/`, which is a rail page there, not
  Settings → Memory. Settings → Memory is still at `/settings/memory`. This
  is the new UI's own information architecture, so it was not changed.
- Upgrade, Audit and CLI show no Save bar.

### 2. Filter — PASS after fix 3

- **Bug 3 (fixed):** `/` on Settings focused the header's "Filter this
  view" box, which filters nothing on Settings. Now `/` focuses **Filter
  settings** while Settings is open, and the header box again once you
  leave (checked on Inbox).
- `budget` lists Models and Agents. `zzzqqq` says `No settings sections
match "zzzqqq".` Escape clears the box and restores all five groups.
- `max agent turns`, a visible field label, matched nothing. Fixed by
  adding field-label keywords to Agents, General and CLI. The `/next/`
  filter lists **sections**, not fields, so it does not focus a field or
  open a subpage (see Observations).

### 3. Save, role and lock model — PASS after fixes 4 and 7

- `/next/` stages edits in a section draft rather than committing each
  field on change or blur. Editing Environment name brings up the
  **Discard / Plan / Preview changes / Save changes** bar. The draft
  survives moving to another section and back, and a reload after Save
  reads the value back (`walk-staging`).
- Save against the 503 stub showed the banner "stubbed outage" with
  Dismiss and kept the bar. Pressing Save again reached the real server.
- **Bug 4 (fixed):** Max agent turns `-3` was sent to the server, which
  answered `400 maxAgentTurns cannot be negative` (the JSON key, not the
  label). `1000000` was accepted. Now Max agent turns and the two follow-up
  limits refuse an out-of-range value by label before saving: "Max agent
  turns must be a whole number of at least 0.", `aria-invalid`, Save
  disabled. The error clears as soon as the box holds a good value.
- **Bug 7 (fixed):** a reload with an unsaved draft dropped it without
  asking. A reload or closing the tab now raises the browser's
  `beforeunload` prompt, and a clean page never does. Moving to another
  page inside the app keeps the draft (root service), so it does not ask.
- External config repo (`:8421`): General, Agents, Tasks and Memory show
  the read-only pill and the banner "This section is read-only because
  deployment settings are managed in the config repository (<path>)…".
  Each has a "View in config repo" link to
  `/settings/config-repo?path=<file>`, and their config fields are
  disabled. Access, CLI, Audit and Models stay usable, with credentials
  still editable on Models.
- Role read-only: covered by the existing
  `next-qa-settings.spec.js › … read-only state for grain/viewer role`
  (passes).

### 4. Preferences — PASS

- Light / Dark / System write `grain.themeMode` to `localStorage` only and
  survive a reload. No non-GET `/api/` request was made from Preferences.
- The time zone select is on **General** in `/next/` and is stored per
  browser (`grain.timeZone`) without a deployment write. It offers five
  zones (Browser local time, UTC, Los Angeles, New York, London).
- Customize pinned items is not on `/next/` Preferences. It offers
  "Navigation rail destinations" (show, hide, reorder) instead.

### 5. Access — PASS after fixes 5–6

- "Your role: operator". `/api/whoami` answers
  `alice@example.com (access code)`.
- **Bug 5 (fixed, data-destroying):** **Remove** on any allowlist row,
  including your own `user:alice@example.com`, deleted it at once with no
  question. The own-account check compared `user:alice@example.com` with
  the viewer id `access-code:alice@example.com`, so it never matched. Now
  every removal opens a confirm dialog ("Remove <entry>?", Cancel /
  Remove), and the last entry gets its own warning. Your own entry gets
  "Remove your own access entry?" and a `confirmSelf` delete.
- **Bug 6 (fixed):** Grant access started on `grain/developer`, a role
  this deployment does not have, so the first **Grant access** press
  failed with `400 role "grain/developer" is no scope set in effect`.
  After the server's role list loads, it now starts on `grain/viewer`, or
  the first role offered.
- After the fixes: grant `user:walk-bob@example.com`, reload (it is
  still there), Remove → Cancel keeps it, Remove → Remove deletes it, and
  a reload confirms it is gone.

### 6. CLI and Audit — PASS after fixes 8–9

- **Bug 8 (fixed):** the CLI card said
  `curl -fsSL <origin>/api/hostd/install.sh | sh`. That is the **host**
  installer, which wants root, `--name` and a registered host; it does not
  install the workstation CLI. It also left out the token flags. Now:

  ```
  curl -fsSL <origin>/cli/install.sh | sh
  grain login <origin> --name <host-slug> --issue-token --scope-set grain/external-dev --expires 30d
  grain skill install --jetski   # or --claude, --codex, --gemini
  ```

  This matches the classic card (`ui/src/components/settings/CLISection.jsx`).

- **Bug 9 (fixed):** Audit printed raw RFC 3339 nanosecond stamps
  (`2026-11-14T12:02:32.239456035Z`). It now prints them in the display
  time zone ("Nov 14, 2026, 12:19:29 PM UTC", and "07:19:29 AM EST" with
  New York chosen), with the raw value in `<time datetime>`.
- Audit lists config, secret and access entries with actor, action and
  detail. No secret values appear (secret rows name the secret path only).
- Both sections stay usable under the external config repo. The CLI
  run-location selects are disabled there, because they are a deployment
  setting.

### 7. General — PASS (partial port)

- Environment name and Deployment ID, Display timezone, "Lock settings in
  UI (require config repo PRs)", and Broadcast. Updates and About are not
  on `/next/` General.

### 8. Config repo — PASS (demo has no controller-local repo)

- The panel, Sync now, the remote/KMS controls and the file viewer render.
  In the demo, `GET /api/state-repo/files` answers
  `501 this deployment's UI does not manage a state repository`. The file
  viewer then shows its empty prompt, and the 501 is logged in the
  console. The sync-failure alert and held config-repo pull requests need
  a daemon with a controller-local repo and were not reached.

### 9. Models — PASS (read and layout)

- Default model, fallback models, per-model effort, framework API keys
  and Codex `auth.json`, Model connections with discovery, and the token
  budget card all render. The Model connections card has Credential /
  Edit / Remove and an Add form. Existing `next-settings-models.spec.js`
  passes. Connection tests that reach a provider were not run (no key).

### 10. Agents, Tasks, Memory — PASS after fix 10

- Agents: Max agent turns, Default capabilities, Standing console
  sessions. Tasks: approval and auto-merge defaults, Show closed tasks,
  follow-up limits, branch template, quality control. Task fields, the
  Default grants picker, Task branches validation and the External agents
  card are not on `/next/` (External agent tokens are under Access).
- **Bug 10 (fixed):** Memory's **Save memory** button, at the top of the
  card holding both boxes, saved only the memory document. Edits to
  **Standing instructions** stayed an unsaved draft: they were lost on
  reload, and "Memory updated" was shown anyway. Save memory now saves
  both. Read back after reload in the browser, then restored.

### Console and network

Each sign-in logs one expected `401` for the unauthenticated first load.
Config repo logs the demo's `501` (above). Every other 4xx was the intended
refusal named in a step. There was no uncaught page error and no 5xx from
the real server.

## Bugs

All fixed in `ui-ng/`, each covered by
`ui-ng/e2e/next-settings-walkthrough.spec.js`. None of these pages is broken
in the classic UI, which already does each of these right, so `ui/` is
unchanged.

1. **Old Admin/System links landed on Inbox or Fleet.**
   - Fix: `app.routes.ts` now redirects `/admin` → General, and
     `/admin/<tab>` and `/system/<tab>` → the matching page:
     `workflow` → Tasks, `executors` → Hosts, `integrations` →
     `capabilities#credentials`, `upgrade` → Upgrade.
   - `/settings/config-repo/<file>` now opens the file viewer with
     `?path=<file>`.
2. **`#card` links did nothing.**
   - Fix: the Settings page scrolls to `#<id>` or
     `[data-testid=card-<frag>]`, with aliases `usage`, `external-agents`,
     `credentials` and `other-secrets`.
   - `/settings/credentials` and `/settings/secrets` add their fragment.
3. **`/` focused a filter that does nothing on Settings.**
   - Fix: Settings registers its own `/` shortcut and gives the shell's
     back on leave.
4. **Out-of-range counts were sent to the server**, and its error named
   the JSON key.
   - Fix: `setCount` refuses the value by label before saving.
5. **Allowlist removal never asked**, and own-account detection never
   matched.
   - Fix: every removal now asks first, and self is matched with or
     without the `user:` / `access-code:` prefixes.
6. **Grant access defaulted to a role the server refuses.**
   - Fix: it starts on `grain/viewer`, or the first role the server offers.
7. **A reload silently dropped an unsaved draft.**
   - Fix: `beforeunload` asks while the draft is dirty.
8. **The CLI card gave the host installer** as the workstation installer.
   - Fix: it now shows `/cli/install.sh`, the token sign-in and the skill
     lines.
9. **Audit showed raw timestamps.**
   - Fix: they print in the display time zone (`formatStamp` in
     `shared/dispatch-status.ts`).
10. **Save memory left Standing instructions unsaved.**
    - Fix: `SettingsStateService.saveFields(["promptExtension"])` runs
      before the memory `PUT`.

## Observations (no change; `/next/` has not ported these yet)

These are features the task's coverage list describes from the classic
Settings that `/next/` does not have. They are larger than a scoped fix and
are proposed as a follow-up:

- The per-field commit-on-change/blur model, the header state line
  (Saving… / Unsaved changes in <card> / Couldn't save <field> with Retry /
  All changes saved), and the dirty dot in the section nav.
- The tablet Section select, and field-level filter results that focus and
  outline the target field.
- The Restart needed chip.
- Preferences → Customize pinned items (boards, sessions, System tabs);
  `/next/` has rail show/hide/reorder instead.
- Access: the This browser card (how you signed in, Log out), separate
  People & groups and Service accounts tables, added-at timestamps, and
  client-side `user:` / `group:` / `serviceAccount:` validation with
  required Reasons.
- General → Updates and About.
- Tasks → Task fields (with the `custom.<name>:legacy` link) and Default
  grants (`GrantsPicker`); Agents → Task branches validation and the
  External agents card.
- Models: connection Test / Enable / Disable, the Credentials stored vs
  ambient choice, Run on host placement with fallback hosts, paused-model
  resume, and the Where each agent CLI runs readiness card.
- Memory: per-repository notes.
- Under the external config repo, General's Settings lock switch stays
  enabled.

## Not covered

- No model key, so no provider connection test or model call.
- Identity/IAP sign-in and `Access Denied` were not reached (the demo uses
  the access code).
- A controller-local config repo, held config-repo PRs and sync-failure
  alerts need `grain daemon` with `grain/config`, which this run did not
  start.

## Tests run

- `npx playwright test e2e/next-settings-walkthrough.spec.js
e2e/next-qa-settings.spec.js e2e/next-settings-models.spec.js
e2e/next-settings-repos.spec.js --project=desktop`: all pass.
- `npx playwright test e2e/next-full-system.spec.js`: 5 passed.
- `npm run lint`, `npm test` (49 files, 447 tests) and
  `npx prettier --check src e2e` in `ui-ng/`: all pass.
- Screenshots: `ui/e2e/screenshot.mjs` against the base and fixed demos
  for `/next/settings/{cli,audit,access,agents}` at desktop and phone,
  light and dark. Bounding boxes found no horizontal overflow at 1440 or
  390 px. The CLI snippet wraps inside its card, and the field error sits
  left-aligned under its input.
