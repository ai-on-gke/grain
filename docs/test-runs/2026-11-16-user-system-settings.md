# User & system settings walkthrough — 2026-11-16 (task 6335)

- **Date:** 2026-11-16, 10:05–11:10 UTC, one run.
- **Task:** task 6335, branch
  `grain/53549e42/task-6335-walk-through-settings-its`, base `main`.
- **Commit tested:** `feb34cb22` ("Merge pull request #1673"), then the
  fixes below on top of it. Each fix was rebuilt and checked again in the
  browser.
- **UI walked:** the new UI at `/next/settings/...` for every step.
  Settings is in `ui-ng/src/app/app.routes.ts` and
  `ui-ng/src/app/shell/nav.ts`, so no step fell back to a classic page. The
  classic UI was read only to compare behaviour (`GeneralSection.jsx`,
  `CLISection.jsx`). It was not changed, because none of the bugs below
  exist there.
- **Environment:**
  - Debian sandbox, `linux/amd64`, Go from `/usr/local/bin/go`, Node.js
    v20.19.0.
  - Playwright from `ui-ng/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
  - Viewports: 1400×900 (desktop), 820×1180 (tablet) and 390×844 (phone).
  - Browser time zones: UTC, and `America/Los_Angeles` for the time zone
    checks.
  - Built with `make frontend`, then `go build -o /var/tmp/grain ./cmd/grain`.
    The fixed builds were `/var/tmp/grain2` and `/var/tmp/grain3`.
  - There was no `/home/debian/.gemini-api-key`. No model was called, and
    `LOOK=1` could not describe the screenshots. The sandbox's file reader
    shows PNGs only as bytes, so layout was checked with bounding boxes
    and `scrollWidth` instead.
- **Servers.** Each ran over a throwaway store (`TMPDIR` set to a fresh
  directory). Each was signed in through the real "Enter access code" page
  as `alice@example.com`:
  1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file>`:
     the base build.
  2. The same on `:8421`, without `-git-proxy` and with
     `-external-config-repo https://github.com/example/walk-config`: the
     external config repo mode.
  3. The fixed builds on `:8430` and `:8440`.
- **How it was driven:** throwaway Playwright scripts outside the
  checkout, acting only through visible controls: clicks, typing, `/`,
  `Escape`, `Tab`, reloads and Back/Forward. Each page recorded:
  - page errors, and console errors and warnings;
  - failed requests;
  - `/api/` responses of 400 or more, with their bodies;
  - every native dialog;
  - every non-GET `/api/` request (to show what Preferences and the time
    zone write).

  `curl` with the access code as a bearer token was used only to read
  state back. It also made one `settingsLocked` round trip (on, then off
  again) to see what the server accepts.

## Stubs

The server, store, frontend, browser and gestures were all real. The
walkthrough scripts stubbed nothing. The new regression spec stubs two
reads, each a real server answer with one field changed:

| Route                                        | Where                                      | Why                                                                                                                   |
| -------------------------------------------- | ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `GET /api/config` + `configRepo`             | `next-settings-walkthrough.spec.js` (lock) | The demo has no controller-local config repo. This shows the Settings lock switch turns on once a deployment has one. |
| `GET /api/settings` + `settingsLocked: true` | same                                       | A lock that is already on, with no config repo, must still be possible to turn off.                                   |

The GitHub, identity, upgrade, model-provider and config-repo boundaries
were not stubbed. The demo's simulator covered what the walk reached. The
identity sign-in, provider connection tests and config-repo pull requests
were not reached (see "Not covered").

## Steps and outcomes

### 1. Layout and paths — PASS

- The rail's Settings item (`rail-settings`, the `,` key) opens
  `/next/settings`, which shows **Models**.
- At 390 px, bare `/settings` shows the grouped list (Models & Compute,
  Repositories & Code, Work & Agents, Access & Security, Instance &
  Preferences). A section then opens alone under "← All settings", with
  `scrollWidth` 390.
- At 820 px the side nav stays a list. There is no Section select (see
  Observations).
- Every old link the coverage names lands where it should. This is
  covered by `next-settings-walkthrough.spec.js` › "old Admin, System and
  Config Repo Browser links…", which passes. `/memory` opens **Context**,
  which is how `/next/` is arranged.
- `#external-agents` scrolls its card into view (existing spec, passes).
- Upgrade, Audit and CLI show no Save bar.

### 2. Filter — PASS after fix 1

- `/` on Settings focuses **Filter settings**. `budget` lists Models and
  Agents.
- Choosing a result pushes history, and Back returns to the section you
  were on.
- `zzzqqq` says `No settings sections match "zzzqqq".` Escape empties the
  box and brings back all five groups.
- **Bug 1 (fixed):** only 27 of the 166 field labels, switch names and
  card titles the sections show found their section. For the other 139,
  such as "Branch name template", "Deployment ID", "CI runs kept",
  "Commit author email" and "Token name", the filter said "No settings
  sections match". The full list was found by a script that types each
  visible `.s-label`, `.s-card-title` and `.s-toggle-name` into the
  filter.

### 3. Save, role and lock model — PASS after fixes 2–4

- `/next/` stages edits in a section draft with a **Discard / Plan /
  Save changes** bar, as the 2026-11-14 report describes. It does not
  commit each field on change or blur (see Observations).
- Max unreviewed follow-up depth `-1` is refused by its label before
  saving.
- **Bug 2 (fixed):** Branch name template `bad..name with space` was sent
  to the server unchecked. It came back as a banner naming the JSON key:
  `taskBranchTemplate branch template must contain {id}`.
- **Bug 3 (fixed, data loss):** an edit to the deployment memory document
  on Memory was thrown away without a question when you left by the rail.
  The Save bar never showed it.
- **Bug 4 (fixed):** **General → Lock settings in UI** could be switched
  on in a deployment with no config repository, and the server accepts
  that (`POST /api/settings {"settingsLocked":true}` answers
  `settingsLocked: true`). Every config-managed section then turned
  read-only behind a "View in Config repo" link to a repository that does
  not exist. Classic General disables the switch here.
- External config repo (`:8421`):
  - General, Tasks, Agents, Memory and Caches show "Managed in config
    repo" and a "View in Config repo" link to
    `/settings/config-repo?path=tables/grain_config.json`, or
    `memory/deployment.md` for Memory. Every config field in them is
    disabled.
  - Engine's fields are disabled too, but it shows no pill or link. That
    section belongs to the infrastructure walkthrough.
  - Access (10 controls enabled), Models (credentials editable) and Audit
    stay usable. CLI's run-location selects are disabled, because they are
    deployment settings.
  - Before fix 4, General's lock switch stayed enabled there.
- The role read-only state is covered by `next-qa-settings.spec.js`
  (passes).

### 4. Preferences and time zone — PASS

- **Light / Dark / System** write `grain.themeMode` to `localStorage`
  only. Dark survives a reload. Preferences made no non-GET `/api/`
  request.
- The time zone picker is on **General** ("Display timezone": Browser
  local time, UTC, America/Los_Angeles, America/New_York, Europe/London).
  It writes `grain.timeZone` only, and brings up no Save bar.
- With the browser in Los Angeles and no zone chosen, Audit printed
  `02:30:53 AM PST`. After choosing UTC it printed `10:34:58 AM UTC`.
- Customize pinned items is still "Navigation rail destinations" here (see
  Observations).

### 5. Access — PASS

- The account tile reads `alice@example.com (access code)`,
  `access-code:alice@example.com · access-code`, Role `operator`. This
  matches `GET /api/whoami`. Access says "Your role: operator".
- **Sign out** returns to the "Enter access code" prompt. Opening
  `/next/settings/audit` and signing in again lands back on
  `/next/settings/audit`.
- The server refuses `not a principal` and a `serviceAccount:` outside
  `*.gserviceaccount.com`, and the banner shows its exact message. Every
  removal asks first (existing spec, passes).

### 6. CLI and Audit — PASS after fix 5

- CLI shows `curl -fsSL <origin>/cli/install.sh | sh`, then
  `grain login <origin> --name <slug> --issue-token --scope-set grain/external-dev --expires 30d`,
  then `grain skill install --jetski   # or --claude, --codex, --gemini`.
- **Bug 5 (fixed):** the three commands were one block with no Copy
  button.
- Audit lists 14 entries. "Config changes" narrows them to 4. Secret rows
  name the secret path, never a value.
- At 390 px Audit stays a table inside the card (`scrollWidth` 390).
- Both CLI and Audit stay usable under the external config repo.

### 7. General — PASS after fix 4

- Environment name, Deployment ID, Display timezone, Settings lock and
  Broadcast are there.
- Updates and About are still not on `/next/` General. The build stamp at
  the foot of the rail reads
  `grain v0.0.0-20261116095301-feb34cb22de8+dirty`.

### 8. Config repo — PASS (the demo has no controller-local repo)

- The panel, Sync now, the remote/KMS controls and the file viewer
  render.
- On `:8420`, `GET /api/state-repo/files` answers
  `501 this deployment's UI does not manage a state repository`.
- On `:8421` it answers `500`, because the demo's stand-in for an external
  repo (`pkg/demo/demo_external_config.go`, `errDemoConfigRepo`) has no
  working tree. This happens in the demo only; the UI shows its empty
  prompt.

### 9. Models — PASS (read and layout)

- Default model and effort, fallback models, Model connections (with
  Discover models, Credential, Edit and Remove), and the token budget
  card all render.
- Connection Test / Enable / Disable, ambient credentials, host placement
  and paused-model resume are still not on `/next/` (see Observations).

### 10. Agents, Tasks, Memory — PASS after fixes 2–3

- Memory is not in the rail and is not on the repository page.
- After fix 3, a memory edit:
  - survives a move to Agents and back, and shows "1 unsaved change";
  - makes the rail and Back out of Settings ask, where Cancel keeps it;
  - saves from the Save bar, and a reload reads it back. It was then
    restored through `PUT /api/memory`.

### Console and network

- Each sign-in logs one expected `401` for the first, unauthenticated
  load.
- Config repo logs the demo's `501` or `500` (step 8).
- Every other 4xx was a refusal a step provoked on purpose.
- There were no uncaught page errors, and no 5xx outside the demo's
  external config repo stand-in.

## Bugs

All are fixed in `ui-ng/` and covered in `ui-ng/e2e/`. The classic UI
gets these right already, so `ui/` is unchanged.

1. **Filter settings could not find most fields by their label.**
   - Fix: `settings-field-labels.ts` lists what every section shows, and
     `filterSettingsGroups` searches it.
   - Test: `next-settings-filter.spec.js` opens every section, types each
     visible label into the filter, and fails on any miss.
2. **The server, not the box, refused a bad branch template, and named
   the JSON key.**
   - Fix: `branch-template.ts` mirrors `model.FormatTaskBranchName` and
     `ValidBranchName`. The box refuses a bad template by its label
     ("Branch name template must contain {id}, so every task gets its own
     branch.") and keeps Save off.
   - It also shows the branch task 42 would get, with this deployment's
     ID in it.
   - Tests: `branch-template.spec.ts` (unit), and the "Branch name
     template is refused…" e2e.
3. **An unsaved memory document edit was dropped silently.**
   - Fix: the draft (`memoryDraft`, `memorySaved`) moved into
     `SettingsStateService`. The Save bar counts it, and Save changes and
     Discard handle it.
   - A new `settingsLeaveGuard` (`app.routes.ts`) asks before leaving
     Settings with any unsaved draft. Moving between sections never asks.
   - Tests: "an unsaved memory edit survives a section change…" and "the
     Save bar saves a memory edit…".
4. **Settings lock could be turned on with no config repository.**
   - Fix: the switch is disabled under an external config repo, and when
     there is no config repo unless the lock is already on, so it can
     still be turned off. Its hint says why.
   - Test: "Lock settings in UI is off-limits without a config
     repository…".
5. **CLI commands had no Copy button.**
   - Fix: each command sits on its own line with a Copy button.
   - Test: "each CLI command has its own Copy button" reads the clipboard
     and checks the layout at 390 px with bounding boxes.

## Observations (no change; `/next/` has not ported these yet)

These are still missing on `/next/`, as on 2026-11-14, and are filed as a
follow-up:

- Commit on change or blur, the header state line (Saving… / Unsaved
  changes in <card> / Couldn't save <field> with Retry / All changes
  saved), the nav's dirty dot, and the Restart needed chip.
- The tablet Section select. Filter results also stop at the section:
  they do not focus or outline the field.
- Preferences → Customize pinned items (boards, sessions, System tabs).
- Access:
  - the This browser card;
  - separate People & groups and Service accounts tables, with added-at
    times;
  - `user:`, `group:` and `serviceAccount:` checks and required Reasons
    in the browser (today the server refuses these, with a clear
    message).
- General → Updates and About.
- Agents: Task branches with its sample, and the External agents card
  (`Accept external agents` is under Access as "Accept external agent
  tokens").
- Tasks: Task fields (with the `custom.<name>:legacy` link) and Default
  grants (`GrantsPicker`).
- Models: connection Test / Enable / Disable, stored vs ambient
  credentials, Run on host with fallback hosts, paused-model resume, and
  readiness in "Where each agent CLI runs".
- Memory: per-repository notes. `GET /api/memory` returns them, and the
  card shows only the deployment's.
- Engine shows no "Managed in config repo" pill under an external config
  repo, although its fields are disabled (infrastructure walkthrough).

## Not covered

- No model key, so no provider connection test or model call.
- Identity/IAP sign-in and `Access Denied` were not reached, because the
  demo uses the access code.
- A controller-local config repo, held config-repo pull requests and
  sync-failure alerts need `grain daemon` with `grain/config`, which this
  run did not start.

## Screenshots

`ui/e2e/screenshot.mjs` was run against the base build (`BIN=/var/tmp/grain`)
and the fixed build (`BIN=/var/tmp/grain3`). It shot
`/next/settings/{general,tasks,memory,cli}` at desktop and phone widths,
in light and dark. The contact sheets are attached to the task's closing
note and are not committed.

Bounding boxes on the fixed build, at 1400 and 390 px:

- Each CLI Copy button sits right of its command line, top-aligned with
  it, inside the card.
- The branch sample sits under its input, left-aligned with it.
- No page scrolls sideways.

## Tests run

- `npx playwright test` in `ui-ng/` (all `/next/` specs, desktop project)
  on the first fix build: 252 passed, 1 skipped, and 1 failed:
  `next-task-interactions.spec.js` › "Tasks split presets…". That spec
  does not touch Settings, and it passed on the rerun.
- `npx playwright test e2e/next-settings-walkthrough.spec.js e2e/next-settings-filter.spec.js e2e/next-task-interactions.spec.js e2e/next-qa-settings.spec.js e2e/next-full-system.spec.js`
  on the final build: 27 passed.
- In `ui-ng/`: `npm run lint`, `npx vitest run` (62 files, 623 tests) and
  `npx prettier --check src e2e` all pass.
