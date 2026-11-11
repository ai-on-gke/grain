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

---

# User & system settings walkthrough — 2026-11-11 (task 4302)

Settings' layout, filter, save/role/lock model, Preferences, Access, CLI,
Audit, General, Config repo, Models, Agents, Memory and Tasks, walked in a
real browser against a real `grain demo` server (task 4302).

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-4302-walk-through-settings-its`.
- **Commit tested:** `bff615b79` (`main` at the start of the run; build
  stamp `v0.0.0-20261111152614-bff615b792f6 · bff615b · 2026-11-11 15:26Z`,
  which General → About and the rail footer both print). The fixes below
  were re-verified on the rebuilt working tree (`897954671`).
- **Build:** `make frontend` (Vite bundle embedded in `pkg/ui/static`), then
  `go build ./cmd/grain`. Note that `make frontend` leaves `ui/node_modules`
  without Playwright; `npm ci` in `ui/` puts it back.
- **Servers:** `grain demo -addr 127.0.0.1:7811 -open=false -github-sim
-fake-ci -secrets-store -access-code-file <file>` (base binary) and the
  same on `127.0.0.1:7812` (fixed binary), each over its own throwaway
  embedded store; signed in through `POST /api/auth/access-code` as
  `alice@example.com` or through the visible "Enter access code" page.
- **Browser:** Playwright headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by scripts
  outside the repository; `timezoneId: UTC`. Viewports 1440×900 and
  1280×600 (desktop), 820×1180 (tablet), 390×844 (phone, dark and light).
- **Model/tooling:** the sandbox has no `/home/debian/.gemini-api-key`, so
  no Gemini-backed tooling was used and `LOOK=1` was unavailable. Layout
  was checked through DOM geometry (bounding boxes, `scrollWidth`) and the
  `make ui-screenshots` contact sheet (not committed).

## Stubs (intercepted with `page.route`)

Everything else ran against the real server and its store.

| Route                                                                                                                         | Why                                                                                                                              |
| ----------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `PUT /api/settings` → one `503`                                                                                               | The demo never fails a save; used to see `Saving…`, `Couldn't save Max agent turns` and `Retry` recover against the real server. |
| `GET /api/settings` + `pendingRestart: ["pollInterval"]`                                                                      | The demo hot-applies everything, so it never needs a restart; used for the `Restart needed` chip.                                |
| `GET /api/config`, `GET /api/settings` + `externalConfigRepo: "https://github.com/acme/grain-config"`, `configRepoOnly: true` | The demo has no external config repository and the sandbox no GitHub; used for the external-config-repo lock.                    |

No model-provider, identity, upgrade or GitHub stub was needed this run
(connection tests were not exercised against a live provider; see "Not
covered").

## Steps & outcomes

### Layout and paths

- The rail carries no Settings link; the header gear
  (`top-bar-settings`) opens `/settings/models`. On a phone the bar's gear
  (`phone-settings`) is the entry.
- Bare `/settings` on a phone lists the five groups (Models & sandboxes,
  Secrets & access, This browser, System / Operations, Managed in config
  repo) with no section open; Access opens under the back arrow, which
  returns to `/settings`. `scrollWidth` 390 at 390 px.
- Every section rides in the URL through reload and Back/Forward
  (`/settings/general` → `/settings/agents`, reload, Back → General,
  Forward → Agents).
- `Upgrade`, `Audit` and `CLI` show no state line; every other section
  walked shows `All changes saved`.
- `#card` deep links at 1280×600 land the card in view with its heading
  visible: `agents#external-agents` (top 269), `models#usage` (553 px
  tall card, top 306, heading 323), `agents#commit-identity`,
  `tasks#task-fields`.
- Old links: `/admin` → General; `/admin/workflow` → Tasks;
  `/admin/executors`, `/system/executors`, `/settings/hosts`,
  `/settings/hostds` → `/settings/hostds`; `/settings/credentials`,
  `/settings/legacy-capabilities`, `/admin/integrations`,
  `/system/integrations` → `/settings/capabilities#credentials`;
  `/settings/secrets` → `/settings/capabilities#other-secrets`;
  `/admin/upgrade`, `/system/upgrade` → `/settings/upgrade`; `/memory` →
  `/settings/memory`; `/config?path=README.md` →
  `/settings/config-repo/README.md`.
- Tablet (820×1180): the nav becomes the `Section` select; with dirty
  Standing instructions, choosing General and cancelling the prompt keeps
  `/settings/agents` and the select on `Agents`; accepting moves to
  General.

### Filter

- `/` focuses `Filter settings`. `house style` lists `Standing
instructions for every run` under Agents; choosing it pushes
  `/settings/agents#promptExtension`, focuses and outlines the textarea,
  and Back returns to `/settings/models`.
- `time zone` → Preferences; `environment name` from inside
  `/settings/config-repo/README.md` leaves the file view for
  `/settings/general#environmentName` and focuses the box; a miss says
  `No setting matches 'zzzqqq'.`; Escape restores the groups.
- **Bug F1** (below): choosing a select (`Check the upgrade`, or
  `Automatic upgrade time of day` while automatic upgrades are off)
  focused the select's hidden native input.

### Save, role and lock model

- `Environment name` saves on Enter (one `PUT /api/settings`, nothing
  while typing), retitles the tab (`walk-4302 — grain`) and reads back
  after reload; restored to empty afterwards.
- Engine's `Poll interval` typed as `1m` reads back `1m0s` (restored to
  `10s`).
- `Max agent turns` = `-5` is refused by name (`Couldn't save Max agent
turns`) with no request sent; putting the old value back clears it.
- A stubbed `503`: `Saving…` while in flight, then `Couldn't save Max agent
turns` with `Retry`, which saved `77` against the real server (restored).
- Server refusals (`Deployment ID` = `not hex!`, a bad branch template)
  answer 400; the state line names the box and the field goes back to the
  stored value with the sample ID `14b7a176` still the placeholder.
- Dirty Standing instructions: `Unsaved changes in Standing instructions`,
  one nav dirty dot; the section nav, the Commit identity `Settings → Git`
  link, a rail link and a reload each ask first (`You have unsaved changes
here. Leave and discard them?` / `beforeunload`); Discard restores the
  text; a clean leave never asks.
- `Restart needed` chip (stubbed `pendingRestart`) opens `/system/power`.
- External config repo (stubbed): General, Agents, Tasks, Memory and
  Models show the read-only banner with an edit link into the repo
  (`tables/grain_config.json`, `memory/`), every control is `:disabled`,
  and no `aria-disabled` control is left in the tab order. Access, CLI,
  Audit and Preferences have no banner and stay usable.

### Preferences

- Appearance Dark persists through reload (body `rgb(15, 16, 19)`);
  Auto restores it. Time zone `Asia/Tokyo` and timeline order are kept in
  `localStorage` (`grain.timeZone`, `grain.timelineOrder`); **no
  deployment-wide write** was sent from the section.
- With Tokyo chosen, Audit (`11/11/2026, 4:10:27 PM` → `11/12/2026,
1:10:27 AM`) and Access added-at times (`11/9/2026, 3:58:58 PM` →
  `11/10/2026, 12:58:58 AM`) follow the zone; with none chosen they print
  in the browser's (UTC).
- Customize sidebar pinned `Settings → Memory`, `System → Power`, the
  `Board` board and the `Director` session, reordered and saved; the
  pinned `Power` opens `/system/power`. No API write was made.

### Access

- This browser: `Signed in with the access code. Changes made here are
recorded as alice@example.com (access code), not as a person.`, matching
  `GET /api/whoami` (`label: "alice@example.com (access code)"`) and the
  rail's user tile. An access-code session has no `Your role` chip (it can
  make any change).
- Add allowed principal: `alice2@example.com` → `Entry must start with
user:, group: or serviceAccount:`; `user:bob` → `user: entry must be a
valid email address.`; `serviceAccount:bot@example.com` → must be
  `*.gserviceaccount.com`; a valid service account without a reason →
  `Give a reason to admit a service account.`; `user:carol@example.com` at
  the default `grain/operator` → `Give a reason: grain/operator holds
dangerous scopes`. `grain/viewer` adds without one. Removing an entry
  (your own `user:alice@example.com` included) opens the pending-change
  summary, which refuses to apply a dangerous role's removal without a
  reason (`Give a reason: this change is recorded in the access history
with why.`); the access history records the service account added with
  its reason. A per-row role change was not driven to completion this
  run.
- Log out returns to the `Enter access code` page. **Bug F2** (below):
  signing in from `/settings/agents#external-agents` came back without the
  `#external-agents` card.

### CLI and Audit

- CLI cards: Install `curl -fsSL http://127.0.0.1:7811/cli/install.sh | sh`
  (the demo is unversioned); Sign in `grain login http://127.0.0.1:7811
--name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires
30d` with the "external agent API is off" warning and its link to
  `/settings/agents#external-agents` (gone once `Accept external agents`
  is on); Agent skills for Jetski, Claude Code, Codex and Gemini CLI. Each
  Copy button copies exactly its own command.
- Audit: `Configuration changes` names the actor (`alice@example.com
(access code)`), surface and summary (`PUT /api/settings`), no secret
  values; at 390 px it stacks into blocks (no `<table>`, no overflow).

### General, Config repo

- Deployment card, Settings lock disabled with `unavailable without a
config repository`, Updates (`Check the upgrade`: don't / file a
  proposal / file it and queue it (default); `Upgrade automatically`: off /
  on each release (recommended) / once a day; `Hold the queue until the
check is done`), About matching the rail's build stamp.
- Config repo panel renders on the demo; the Config Repo Browser shows
  `README.md` (also through `/config?path=README.md`).

### Models

- New connection then Escape cleans the draft up without a Remove prompt
  (1 Remove button before and after). Remove asks first (`Remove the
connection Gemini 2.5 Pro? A credential stored for it is deleted…`);
  dismissing keeps it. Disable / Enable persist through reload. New task's
  model picker offers `Deployment default (gemini-2.5-pro)`,
  `gemini-2.5-pro`, `gemini-2.5-flash`.

### Agents, Tasks, Memory

- Agents: Max agent turns, Standing instructions (Save/Discard), Task
  branches validation (above), `Accept external agents` round trip.
- Tasks: flipping `Auto-merge new tasks by default` persists and New
  task's `Auto-merge once checks pass` starts the same way (restored).
- Memory: a correction to the first deployment note shows `Unsaved changes
in Memory`; Back asks first; Save, leave and reopen reads it back;
  restored afterwards. Memory is not in the rail unless pinned and
  `/repos/acme/widgets` does not mention it.

### Console and network

No console errors other than the deliberately stubbed `503` and the
`400`s of the refused Deployment ID / branch template, and the `401`s of
the signed-out login page. `ERR_ABORTED` `/api/tasks` long-polls were
navigation, not failures.

## Findings

### F1 (fixed): a filtered select focused its hidden native input

- **Reproduction:** `/settings/models`, Filter settings → `check the
upgrade` → choose it. `document.activeElement` is `<input
name="upgradeCheckMode" aria-hidden="true" tabindex="-1"
class="MuiSelect-nativeInput">` — nothing visibly focused, and Tab starts
  over. Same for `upgrade time of day` while automatic upgrades are off
  (the card's first control is that select).
- **Cause:** `SettingsPage`'s reveal focused the first
  `input:not([type=hidden])`, which for a MUI Select is its hidden native
  input.
- **Fix:** focus the first control a keyboard reaches (skipping
  `aria-hidden` / `tabindex="-1"` inputs, taking `[role=combobox]`).
  After: the focused element is `DIV role=combobox`.

### F2 (fixed): signing in dropped a deep link's `#card`

- **Reproduction:** signed out, open `/settings/agents#external-agents`,
  sign in with the access code. Lands on `/settings/agents` at the top
  (card at y 1239 of a 600 px window).
- **Cause:** the fragment never reaches the server, and
  `localReturnPath` (rightly) refuses `#` in `next`.
- **Fix:** the login script appends the address bar's fragment to the
  form's `action`; a 303 whose `Location` has no fragment keeps the
  request's, so the browser lands on `/settings/agents#external-agents`
  with the card in view (y 269), a wrong code first included.

**Regression coverage:**
`ui/e2e/user-system-settings-focus-and-deep-link.spec.js` (both).
Passing alongside `user-system-settings-walkthrough.spec.js` and
`access-code-walkthrough.spec.js` (17/17); `go test ./pkg/ui -run Access`
and `SettingsPage.test.jsx` (43/43) pass.

### Follow-up pass after merging `main` (`090b09e90`, attempt 3)

- **`-disable-self-upgrade`** (`grain demo -disable-self-upgrade`): `Settings → General` replaces the `Updates` controls with `updates-externally-managed` (`In-VM self-upgrade is disabled on this deployment; upgrades and instance lifecycle are managed externally.`, zero `upgradeCheckMode` controls), and `Filter settings` omits the upgrade entries (`No setting matches 'check the upgrade'.`).
- **Verified IAP identity & `Access Denied`** (`grain demo -ui-iap-audience test-aud -ui-iap-issuer https://test-iap.local -ui-iap-keys-url <local ES256 key server>`): an unaccepted `X-Goog-IAP-JWT-Assertion` for `mallory@example.com` answers `403` (`Not allowed on this grain`) naming `user:mallory@example.com` as the exact principal to add under `Settings -> Access` or `grain access allow user:mallory@example.com`. Signing in as `alice@example.com` shows `Your role: grain/operator` (`RoleChip`) and `Signed in through IAP as alice@example.com.`, matching `GET /api/whoami` and the rail user tile; removing all rows down to `user:alice@example.com` warns that a dangerous role is involved and requires a reason.
- **Controller-local `grain/config`** (`grain demo -config-change`): `Settings → General` enables `Lock settings in UI (require config repo PRs)`; toggling it locks `Settings → Tasks` and the other managed sections with `View in config repo` links into `/settings/config-repo/tables/grain_config.json`.
- **Models**: `grain demo -unconfigured` shows `This deployment has no provider connection, so its tasks wait. Create one to authenticate agent models.`; `Test Connection` on `Gemini 2.5 Pro` reports `Connection valid`; `grain demo -agent-paused` across UTC midnight prints the dated pause time (`paused until 11/12/2026, 12:13:58 AM (about 5h 59m)`) with `Resume`; `Sessions` (`New session`) offers `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro` and `gemini-2.5-flash`.
- **Merged regression & screenshot check**: `SettingsPage.test.jsx` (43/43), `settingsIndex.test.jsx` (40/40), `ConnectionHostdField.test.jsx` (6/6), `go test ./pkg/ui -run Access|AngularRoute`, and Playwright `user-system-settings-focus-and-deep-link.spec.js`, `user-system-settings-walkthrough.spec.js` and `access-code-walkthrough.spec.js` (18/18) all pass together. Before/after screenshots rendered via `ui/e2e/screenshot.mjs` for `/settings/general#updates` and `/settings/agents#external-agents` at desktop (1440×900) and phone (390×844) in light and dark (not committed).
