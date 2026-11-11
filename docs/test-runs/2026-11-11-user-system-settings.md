# User & system settings walkthrough — 2026-11-11 (task 3935)

- **Date:** 2026-11-11, 15:00–16:00 UTC, one run.
- **Task:** task 3935, branch
  `grain/098073e4/task-3935-walk-through-settings-its`, base `main`.
- **Commit tested:** `87a5e763` ("Merge pull request #1220"), binary
  `v0.0.0-20261111140231-87a5e7638d43`. Both fixes below were checked again
  in the browser on a rebuild of that commit with the fixes applied
  (`/var/tmp/grain-fixed`, demo on a fresh store).
- **Scope:** Settings layout, filter, save/lock/role model, Preferences,
  Access, CLI, Audit, General, Config repo, Models, Agents, Memory and
  Tasks. Git, Hosts, Capabilities, Hub, Engine, CI, Sandboxes, Caches,
  Upgrade and Repositories were touched only where the lock model or a
  read-back needed them.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC unless
    stated, viewport 1400×900 unless stated (tablet 820×1180, phone
    390×844, filter checks also at 1280×600).
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Servers, each over a throwaway store (`TMPDIR` set to a fresh
    directory):
    1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file>` — most steps.
    2. The same on `:8421` with `-external-config-repo https://github.com/example/walk-config` — external config repo mode.
    3. `grain daemon -data-dir <tmp>/data -sandbox-dir <tmp>/sb -ui-addr 127.0.0.1:8422 -sandbox-port-base=0 -as alice -poll-interval 200ms -max-workers 1 -max-mergers 1 -agy-path <fake agy script> -gemini-api-key-file <fake key> -github-host 127.0.0.1:9 -github-insecure-http -disable-native-sandboxes` — the controller-local `grain/config`, Settings lock, Config Repo Browser and a held config pull request.
    4. `grain demo ... -agent-paused` on `:8423` — a paused connection.
    5. The fixed build on `:8425` — re-checking the fixes.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots; layout was checked with
    bounding boxes and `scrollWidth` instead.
- **How it was driven:** throwaway Playwright scripts outside the checkout.
  Each signs in through the Access code page and then acts only through
  visible controls (clicks, typing, `Tab`, `Enter`, `Escape`, `/`). Every
  page recorded uncaught page errors, console errors and warnings, failed
  requests, `/api/` responses of 400 or more (with bodies), every native
  dialog (its text, and whether it was accepted), and every non-GET `/api/`
  request with its body. The REST API (bearer access code) was used only to
  read back what a gesture persisted, plus the setup steps named below.
- **Console baseline:** each sign-in logs one `401` for the unauthenticated
  first load, which is expected. Navigating with `page.goto` aborts the
  previous page's in-flight `GET /api/tasks` long reads (`ERR_ABORTED`),
  which is the browser's own cancellation. No uncaught page error and no 5xx
  from the real server was recorded. Every other console error was the
  browser's line for an intended 400/409 listed below.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout. Playwright `page.route` was used only where the demo cannot
produce a state:

| Route                                                                                                            | Why                                                                                                                                                            |
| ---------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PUT /api/settings` → one `503`                                                                                  | To see `Couldn't save Max agent turns` with Retry, then a real save on Retry.                                                                                  |
| `GET /api/settings` + `pendingRestart: ["pollInterval"]`                                                         | The demo hot-applies settings, so it never reports a pending restart; used for the Restart needed chip.                                                        |
| `GET /api/config` + `access: {role: "grain/viewer", scopes: [tasks:read, settings:read]}`                        | The access code always signs in as an operator; used for the role read-only notes.                                                                             |
| `GET /api/config` with `high` dropped from `priority`'s options                                                  | The demo's custom fields come from the config repo; used so a task carrying `priority: high` (set through the real `PATCH /api/tasks/1`) has a stranded value. |
| `GET /api/settings` and `GET /api/config` with `settingsLocked`/`configRepoOnly` forced `false` (daemon, locked) | A stale tab, so a write reaches the real daemon's `409` refusal.                                                                                               |
| `GET /api/config` + `stateRepoSyncFailure` (daemon)                                                              | The failing-sync alert and Inbox row.                                                                                                                          |
| `GET /api/deployment/audit-logs?limit=100` → `500`; `GET /api/deployment/secret-audit` → `[]`                    | Audit's independent error and empty states.                                                                                                                    |

No GitHub, identity, upgrade or model-provider request left the sandbox:
Test Connection answered from the demo itself, and the external config repo
is a made-up name the demo never fetches.

## Steps and outcomes

### 1. Layout, paths and old links — PASS

- No Settings link in the rail; the header gear (`top-bar-settings`) opens
  `/settings/models` on desktop. On a phone the gear is `phone-settings` in
  the bar; bare `/settings` lists the five groups (Models & sandboxes,
  Secrets & access, This browser, System / Operations, Managed in config
  repo) with no section open; Audit opens alone under the back arrow, which
  returns to `/settings`. No horizontal overflow at 390 px.
- Old links: `/admin` → General, `/admin/workflow` → Tasks,
  `/admin/executors`, `/system/executors`, `/settings/hosts` →
  `/settings/hostds`, `/settings/credentials`,
  `/settings/legacy-capabilities`, `/admin/integrations`,
  `/system/integrations` → `/settings/capabilities#credentials`,
  `/settings/secrets` → `#other-secrets`, `/admin/upgrade`,
  `/system/upgrade` → `/settings/upgrade`, `/memory` → `/settings/memory`.
- Upgrade, Audit and CLI have no `All changes saved` line; every other
  section does.
- `/settings/agents#commit-identity` deep link lands the card in view;
  Back/Forward step between sections.
- Tablet (820 px): the nav becomes the Section select; with Standing
  instructions dirty, choosing Tasks asks "You have unsaved changes here.
  Leave and discard them?", and declining leaves both the URL and the
  select on Agents.

### 2. Filter — PASS after fix 1

- `/` focuses Filter settings; "house style" lists Standing instructions
  under Agents; choosing it opens `/settings/agents#promptExtension`,
  focuses the textarea and outlines it; Back returns to `/settings/models`.
  "zzzqqq" says `No setting matches 'zzzqqq'.`; Escape clears and restores
  the groups.
- 24 keyword probes. Before the fix "budget", "fewest workers", "run
  location", "fallback", "commit identity", "settings lock", "about" and
  "role" found nothing, or nothing in the section that holds them (see
  Bug 1). After the fix each lands on its card, highlighted in view at
  1280×600 and 390×844.
- Picks of Time zone, Default grants, Accept external agents and Log out
  each open the card, outline it and focus its control.

### 3. Save, role and lock model — PASS

- General: Environment name on Enter (`PUT {"environmentName":"walk-staging"}`,
  title `walk-staging — grain`), Show closed tasks by default on change;
  both survive a reload.
- Max agent turns `-5`: no PUT; `Max agent turns must be at least 0; -5 was
not saved.` and `Couldn't save Max agent turns`. Putting back `0`
  clears both.
- One 503: `Couldn't save Max agent turns` + Retry; Retry saves 77 (server
  reads 77). Put back to 0.
- CI capacity reserved after `90s` reads back `1m30s`, also after reload;
  `banana` is refused with the server's `time: invalid duration` by
  label, and clearing the box clears the error.
- Standing instructions: `Unsaved changes in Standing instructions`, dirty
  dot in the nav; leaving by the nav and by Commit identity's Settings →
  Git link both ask first; Discard then leave does not ask.
- Restart needed chip (stubbed) links to `/system/power`.
- Viewer role (stubbed): Agents and Memory read
  `Read-only for you. Needs settings:write` / `memory:write`, `which your
role grain/viewer does not grant…`, with no enabled control; Access shows
  no Role selectors.
- External config repo (`:8421`): General, Agents, Tasks, Memory, Engine,
  Caches and Capabilities' Deployment credentials have every control
  disabled (`:disabled`, so through the fieldset) under the
  `Configuration is read-only and managed in https://github.com/example/walk-config (main)`
  banner. Models keeps Test Connection and Edit (credentials) enabled.
  Access, CLI, Audit and Preferences stay fully usable. Locked controls
  are not in the Tab order.
- Settings lock (daemon): `Lock settings in UI (require config repo PRs)` is
  disabled with "Unavailable without a config repository" on the demo and
  enabled on the daemon. Locked, the managed sections show `View in config
repo → tables/grain_config.json` (Tasks adds `tables/custom_field.json`,
  Memory `memory/deployment.md`); Engine keeps only Max worker agents
  (a bootstrap setting, by design); Tasks shows Default grants read-only.
  A stale-tab Poll interval write got the real `409`, the "Configuration is
  managed in grain/config" dialog with the diff, and Create Config PR filed
  `Opened config pull request #2 as task 2`.

### 4. Preferences — PASS

- Time zone Asia/Tokyo is stored only in `localStorage`
  (`grain.timeZone`; no `/api/` write). Access Added times moved from the
  browser zone (America/New_York) to Tokyo; Audit entries print in Tokyo.
  With no zone chosen, times follow the browser's.
- Customize sidebar: pinned Settings → Memory, System → Power, a board and
  the Director session; the rail of this tab and of a second open tab both
  showed them at once. Moving Power above Memory and unpinning the board
  followed in both tabs; the pins open `/settings/memory` and
  `/system/power`. Nothing was saved to the server.

### 5. Access — PASS

- This browser: "Signed in with the access code. Changes made here are
  recorded as alice@example.com (access code), not as a person." matching
  `GET /api/whoami` and the rail's user tile; the phone drawer shows the
  same.
- Add allowed principal: `bob@example.com` (no prefix), `user:not-an-email`,
  `group:`, `serviceAccount:bot@example.com`, a `*.gserviceaccount.com`
  account with no reason and `user:dave@example.com` as grain/operator with
  no reason are each refused with their own sentence; `user:carol@example.com`
  with a reason is added. Changing carol to grain/viewer asks for a reason
  and saves (`PATCH`), surviving a reload. Removing alice (operator) asks
  for a reason; removing the contributor group goes straight through.
- Log out returns to `Enter access code`; signing in from
  `/settings/agents#external-agents` lands back on `/settings/agents`.

### 6. CLI and Audit — PASS

- Install `curl -fsSL http://127.0.0.1:8420/cli/install.sh | sh`; Sign in
  `grain login http://127.0.0.1:8420 --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d`;
  the Copy buttons put exactly those on the clipboard. With external agents
  off, the warning's Settings → Agents link opens
  `/settings/agents#external-agents` in place, highlighted; turning it on
  removes the warning. Skills: Jetski, Claude Code, Codex, Gemini CLI.
- Audit lists config changes with actor label and surface, credential
  accesses and secret resolutions. With the credential log failing and the
  secret log empty, each card says so on its own ("could not be read
  (boom)", "Nothing recorded yet.") while the config log still renders.

### 7. General — PASS

- Updates: auto-upgrade options `Off (manual only)`, `On each release
(recommended)`, `Once a day`; choosing release saves
  `{"autoUpgradeOn":"release","autoUpgradeChannel":"latest"}`. Check the
  upgrade offers Don't / File a proposal / File it and queue it.
- About's version string matches the build stamp at the foot of the rail.

### 8. Config repo — PASS

- Daemon: the panel shows `grain/config main 074e7689`, the working tree and
  Export now; the browser opens `/settings/config-repo/tables/grain_config.json`
  with the JSON preview, and `/config?path=…` redirects there. With the
  sync failing (stubbed), the alerts panel and the Inbox row say
  "Settings have not been exported to grain/config since …"; the row's
  Config repo button opens `/settings/config-repo`.

### 9. Models — PASS after fix 2

- New connection → agent type Claude → Continue creates a disabled draft;
  leaving asks and then `DELETE`s it; no Remove prompt. Custom command is
  not offered.
- Disable shows "Every provider connection is disabled…" and "No enabled
  connection serves model gemini-2.5-pro…", survives reload, Enable puts it
  back. Remove asks "Remove the connection Gemini 2.5 Pro? A credential
  stored for it is deleted…"; declining keeps it.
- A default model no connection advertises reads
  `made-up-model-9 (no connection advertises it)` with its warning.
- Edit: Run on host lists hosts with connection state, owner and reported
  logins, plus "A host selected by label…"; picking a host offers the
  `;`-separated Fallback box. Ambient credentials require a host and save
  with it. Picking "by label" first, then an ambient host, left Run on host
  saying "by label" (Bug 2).
- Usage: Concurrency Advisory/Automatic/Off; Automatic with Fewest workers 2
  saves to `PUT /api/usage/settings` and survives reload.
- `-agent-paused`: "paused until 9:36:32 PM (about 5h 59m) — …" (and 6:36:32
  AM in Tokyo, the same calendar day there); Resume sends
  `DELETE /api/pause?connection=gemini-pro` and the pause is gone after a
  reload.
- New task's Model picker offers `Deployment default (gemini-2.5-pro)`,
  `gemini-2.5-pro`, `gemini-2.5-flash`.

### 10. Agents — PASS

- Branch name templates `bad..{task}`, `static-name` and `grain/{id} bad`
  are refused by the server by label; the Deployment ID's sample
  (`9df3e396`) stays visible as its placeholder; `bad id!` is refused.
- Accept external agents saves on change and changes CLI's Sign in card.

### 11. Memory — PASS

- An appended line shows `Unsaved changes in Memory`; the rail, Back and a
  reload each ask first and keep the edit when declined; Save writes
  `PUT /api/memory`, and reopening reads it back. Restored afterwards.
  Memory is not in the rail unless pinned, and `/repos/acme/widgets` has no
  Memory.

### 12. Tasks — PASS

- Auto-merge new tasks by default off and the `ci-read` preset added under
  Default grants both save on change; the New task form opens with
  Auto-merge unchecked and `api:ci-read` among its grants.
- With `high` dropped from Priority's declaration (stubbed), "4 tasks still
  carry a value this field no longer offers" opens
  `/?f=custom.priority:legacy:` listing them.

## Bugs

### Bug 1 — the filter could not find several cards (fixed)

- **Reproduction:** open `/settings/general`, press `/`, type `budget`
  (or `run location`, `commit identity`, `fewest workers`) → `No setting
matches`. `fallback`, `settings lock`, `about` and `role` also found
  nothing in the sections that hold them.
- **Cause:** `ui/src/settingsIndex.js` had no entries for Models' Token
  usage and budgets (`#usage`) and Where each agent CLI runs (`#cli-run`)
  cards or Agents' Commit identity (`#commit-identity`), and those keywords
  were missing on Default model, Settings lock, About and Allowed users and
  groups.
- **Fix:** card entries and keywords added. The New task grant hint's
  stale "Settings → Capability servers" now says "Settings → Capabilities".
- **Coverage:** `settingsIndex.test.jsx` (each query finds its card; the
  existing test checks the anchors exist), and Playwright "the filter finds
  Models' usage and run-location cards and Agents' commit identity" in
  `user-system-settings-walkthrough.spec.js`, which failed on the old bundle
  and passes on the new one.

### Bug 2 — Run on host stuck on "by label" after an ambient host was saved (fixed)

- **Reproduction:** Settings → Models → Edit Gemini 2.5 Pro → Run on host
  "A host selected by label…" → Credentials Ambient → Host
  `build-runner`. The connection saves with `hostd: build-runner`, but Run
  on host still reads "A host selected by label…" over an empty Host labels
  box until the page is reopened.
- **Cause:** `ConnectionHostdField` kept its own `labelled` flag
  regardless of the host the connection named.
- **Fix:** label mode is dropped once the connection names a listed host.
- **Coverage:** `ConnectionHostdField.test.jsx` "leaves label mode when a
  named host is set from outside" (fails without the fix); checked again in
  the browser on the rebuilt demo (Run on host reads
  `host build-runner · not connected`).

## Observations (no change)

- Locked Memory's `View in config repo → memory/deployment.md` opens "file
  does not exist" when the deployment has no memory yet (the file is only
  exported once there is some). The notice deliberately keeps the first
  path; filed as a follow-up.
- Removing `user:alice@example.com` while signed in with the access code as
  alice gives no own-account warning: the access code is not that identity,
  and the warning is for verified sign-in. Left as is.
- The demo's Task branches card advises switching to
  `grain/{deployment}/task-{id}-{slug}` because its stored template is the
  legacy one. Expected for an existing store.

## Tests run

- `npx vitest run src/settingsIndex.test.jsx` — 40 passed.
- `npx vitest run src/components/settings/ConnectionHostdField.test.jsx src/components/ModelBackendPage.test.jsx` — 25 passed.
- `npx playwright test e2e/user-system-settings-walkthrough.spec.js` — 13
  passed and the new test failed on the old bundle; the new test passed
  after `npm run build`.
- `make ui-screenshots PAGES=/settings/models,/settings/agents,/settings/preferences OUT=screenshots/after`
  (desktop and phone, light and dark); not committed.
