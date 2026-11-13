# User & system settings walkthrough — 2026-11-13 (task 4730)

- **Date:** 2026-11-13, 02:40–03:45 UTC, one run.
- **Task:** task 4730, branch
  `grain/098073e4/task-4730-walk-through-settings-its`, base `main`.
- **Commit tested:** `4ae6438fd` ("Merge pull request #1435"), binary
  `v0.0.0-20261113022244-4ae6438fdcd4`. Both fixes below were checked again
  in the browser on a rebuild with the fixes applied (`/var/tmp/grain-fixed`,
  a fresh demo store on `:8425`).
- **Scope:** Settings layout, filter, save/lock/role model, Preferences,
  Access, CLI, Audit, General, Config repo, Models, Agents, Memory and
  Tasks in the classic UI (`/settings/...`), plus the `/next/` Access page
  for fix F2. Git, Hosts, Capabilities, Hub, Engine, CI, Sandboxes, Caches,
  Upgrade and Repositories were touched only where the lock model or a
  read-back needed them.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC unless
    stated, viewport 1400×900 unless stated (tablet 820×1180, phone
    390×844, card scrolling at 1280×600).
  - Built with `make frontend` (classic `ui/` and `ui-ng/`), then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Servers, each over a throwaway store (`TMPDIR` set to a fresh
    directory):
    1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file>` — most steps.
    2. `grain demo -addr 127.0.0.1:8421 -external-config-repo https://github.com/example/walk-config` — external config repo mode.
    3. `grain demo -addr 127.0.0.1:8422 -config-change` — controller-local `grain/config`, Settings lock, Config Repo Browser.
    4. `grain demo -addr 127.0.0.1:8423 -agent-paused` — a paused connection.
    5. The fixed build on `:8425` — re-checking the fixes.
  - There was no `/home/debian/.gemini-api-key`, so no model was called;
    layout was judged from bounding boxes, `scrollWidth` and screenshots
    kept outside the checkout.
- **How it was driven:** throwaway Playwright scripts outside the checkout
  (`/var/tmp/walk/*.mjs`). Each signs in through the Access code page and
  then acts only through visible controls (clicks, typing, `Tab`, `Enter`,
  `Escape`, `/`). Every page recorded uncaught page errors, console errors
  and warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), every native dialog (its text and whether it was accepted), and
  every non-GET `/api/` request with its body. The REST API (bearer access
  code) was used only to read back what a gesture persisted, and for the
  setup and clean-up steps named below.
- **Console baseline:** each sign-in logs one `401` for the
  unauthenticated first load (expected). `page.goto` aborts the previous
  page's `GET /api/tasks` long read (`ERR_ABORTED`), the browser's own
  cancellation. No uncaught page error and no 5xx from the real server was
  recorded; every other console error was the browser's line for an
  intended 400/403 or a stubbed 500/503 listed below.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout. Playwright `page.route` was used only where the demo cannot
produce a state:

| Route                                                                                         | Why                                                                                                                                                           |
| --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PUT /api/settings` → one `503` (after 700 ms)                                                | `Saving…`, then `Couldn't save Max agent turns` with Retry; Retry reached the real server.                                                                    |
| `GET /api/settings` + `pendingRestart: ["pollInterval"]`                                      | The demo hot-applies settings, so it never reports a pending restart; used for the Restart needed chip.                                                       |
| `GET /api/config` + `access: {role: "grain/viewer", scopes: [tasks:read, settings:read]}`     | The access code always signs in as an operator; used for the role read-only notes.                                                                            |
| `GET /api/config` with `high` dropped from `priority`'s options                               | The demo's custom fields come from its config; used so tasks carrying `priority: high` (one set through the real `PATCH /api/tasks/1`) have a stranded value. |
| `GET /api/config` + `stateRepoSyncFailure` (`-config-change` demo)                            | The failing-sync Inbox row.                                                                                                                                   |
| `GET /api/deployment/audit-logs?limit=100` → `500`; `GET /api/deployment/secret-audit` → `[]` | Audit's independent error and empty states.                                                                                                                   |

No GitHub, identity, upgrade or model-provider request left the sandbox:
Test Connection answered from the demo itself, and the external config repo
is a made-up name the demo never fetches. Setup through the API: `PATCH
/api/tasks/1 {"customFields":{"priority":"high"}}` (restored to `medium`),
and re-adding `user:alice@example.com` after the F2 reproduction.

## Steps and outcomes

### 1. Layout, paths and old links — PASS

- No Settings link in the rail; the header gear (`top-bar-settings`) opens
  `/settings/models`. On a phone the gear (`phone-settings`) opens bare
  `/settings` with the grouped list and no section; Audit opens alone under
  the back arrow, which returns to `/settings`; `scrollWidth` 390 at 390 px.
- Old links, each landing exactly: `/settings` → `/settings/models`;
  `/admin` → General; `/admin/workflow` → Tasks; `/admin/executors`,
  `/system/executors`, `/settings/hosts` → `/settings/hostds`;
  `/settings/credentials`, `/settings/legacy-capabilities`,
  `/admin/integrations`, `/system/integrations` →
  `/settings/capabilities#credentials`; `/settings/secrets` →
  `#other-secrets`; `/admin/upgrade`, `/system/upgrade` →
  `/settings/upgrade`; `/memory` → `/settings/memory`;
  `/config?path=README.md` → `/settings/config-repo/README.md`.
- Card deep links at 1280×600 land the card in view, tall ones aligned to
  their heading: `#external-agents` top 269, `#usage` 306 (553 tall),
  `#task-fields` 129, `#updates` 104.
- `All changes saved` shows on Models, General, Agents, Tasks, Memory,
  Preferences, Access and Config repo, and is absent on CLI, Audit and
  Upgrade.
- Back/Forward and reload keep the section (`/settings/general` ⇄
  `/settings/agents`).
- Tablet (820×1180): the nav is the `Section` select; with dirty Standing
  instructions, choosing Tasks asks `You have unsaved changes here. Leave
and discard them?`; dismissing keeps `/settings/agents` and the select on
  `Agents`.

### 2. Filter — PASS

- `/` focuses `Filter settings`. 24 probes each listed their card by
  section (for example `house style` → Standing instructions, Agents;
  `fewest workers` → Token usage and budgets, Models; `settings lock` →
  General; `log out` → Sign out, Access; `pinned` → Sidebar, Preferences;
  `skill` → Agent skills, CLI; `audit` → the three Audit logs).
- Choosing `house style` pushes `/settings/agents#promptExtension`, focuses
  the `promptExtension` textarea; Back returns to `/settings/models`.
- `environment name` chosen inside `/settings/config-repo/README.md` leaves
  the file view for `/settings/general#environmentName` and focuses the box.
- `zzzqqq` → `No setting matches 'zzzqqq'.`; Escape clears the box and
  restores the nav.
- Note: `mirror` lists only Upgrade's `Minimum interval between release
upgrades`, not the Config repo panel's mirror control — the panel's
  controls are not in the filter index (minor, not filed).

### 3. Save, role and lock model — PASS

- `Environment name` saves on Enter only (`PUT {"environmentName":"walk-4730"}`;
  nothing while typing), retitles the tab `walk-4730 — grain`, and reads
  back after reload. `Show closed tasks by default` commits on change and
  survives reload (restored).
- `Max agent turns` = `-5`: no request; `Max agent turns must be at least 0;
-5 was not saved.` and `Couldn't save Max agent turns`. Putting `0` back
  clears both (`All changes saved`).
- Stubbed `503`: `Saving…` in flight, `Couldn't save Max agent turns`, Retry
  saved 77 on the real server (read back via API; restored to 0).
- Engine `Poll interval` `1m` reads back `1m0s`, also after reload
  (restored to `10s`).
- Server refusals: `Deployment ID` `not hex!` → 400 (`deploymentId may
contain only lowercase letters…`), state `Couldn't save Deployment ID`,
  box back to empty with the sample `64d38714` still its placeholder;
  branch template `grain/{{bad` → 400 (`must contain {id}`), `Couldn't save
Branch name template`, the card's suggestion text still visible.
- Dirty Standing instructions: `Unsaved changes in Standing instructions`,
  one nav dirty dot; the section nav and the Commit identity `Settings →
Git` link each ask first; a reload raises `beforeunload`; Discard restores
  the text; a clean leave never asks. Save round-trips through reload.
- `Restart needed` chip (stubbed) opens `/system/power`.
- Viewer role (stubbed): General, Agents, Tasks, Memory say `Read-only for
you. Needs settings:write / memory:write, which your role grain/viewer
does not grant…`, Models names `connections:write`; zero enabled inputs.
- External config repo (`:8421`): General, Agents, Tasks, Memory, Models,
  Engine, Caches, Hub and Capabilities show `Configuration is read-only and
managed in https://github.com/example/walk-config (main)…` with an edit
  link into `tables/grain_config.json` (Memory: `memory/`); zero enabled
  inputs except Models' credential path (`Test Connection` and `Edit`
  enabled; `New connection`, `Disable`, `Remove`, `Save` disabled) and Hub's
  non-config fields; no `aria-disabled` control left in the tab order.
  Access, CLI, Audit and Preferences have no banner and stay usable.
- Settings lock (`:8422`, `-config-change`): `Lock settings in UI (require
config repo PRs)` is enabled there (disabled with `Unavailable without a
config repository` on `:8420`); checking it locks Tasks (zero enabled
  inputs, Default grants read-only, `View in config repo` →
  `/config?path=tables%2Fgrain_config.json`, which lands on
  `/settings/config-repo/tables/grain_config.json`), Access keeps 6 enabled
  inputs, and the lock box itself stays enabled so it can be turned off.
  A stale-tab write refused by the locked daemon was not re-driven this
  run (the stubbed lock flags did not unlock the classic form; covered by
  `config-refusal-pull-request.spec.js`).

### 4. Preferences — PASS

- Dark persists through reload (body `rgb(15, 16, 19)`), Auto restores it.
- Time zone `Asia/Tokyo` is kept in `localStorage` (`grain.timeZone`);
  Audit (`2:52:06 AM` → `11:52:06 AM`), Access added-at (`11/11/2026,
2:44:15 AM` → `11:44:15 AM`) and the Models usage reset times shift by +9h;
  with none chosen they print in the browser's UTC.
- Task timeline offers Newest first (default) / Oldest first.
- Customize sidebar opens its overlay (Sections, Pinned items, Add item to
  pinned with Type/Item/Icon, Reset to default, Save).
- **No deployment-wide write** was sent from the section (non-GET log empty).

### 5. Access — PASS after fix F2

- This browser: `Signed in with the access code. Changes made here are
recorded as alice@example.com (access code), not as a person.`, matching
  `GET /api/whoami`. Log out returns to `Enter access code`; signing in from
  `/settings/tasks#task-fields` returns to that deep link.
- Add allowed principal: `alice2@example.com` → `Entry must start with
user:, group: or serviceAccount:…`; `user:bob` → `user: entry must be a
valid email address.`; `serviceAccount:bot2@example.com` → `…must name a
service account address (*.gserviceaccount.com).`; a valid service account
  without a reason → `Give a reason to admit a service account.`;
  `group:walkers@example.com` as `grain/operator` without a reason → `Give a
reason: grain/operator holds dangerous scopes…`; as `grain/viewer` it is
  added (`POST {"entry":"group:walkers@example.com","role":"grain/viewer"}`).
- `People & groups` and `Service accounts` tables, per-row Role selectors
  (20 roles, `ops/triage` included); `grain/viewer` → `grain/contributor`
  saved by `PATCH`; timestamps follow the chosen zone.
- Removing `user:alice@example.com` (the session's own name) went straight
  through — **Bug F2**. Removing the last remaining entry shows `You are
removing the last allowed entry…` with `Remove anyway`.

### 6. CLI and Audit — PASS after fix F1

- CLI: `curl -fsSL http://127.0.0.1:8420/cli/install.sh | sh`; `grain login
http://127.0.0.1:8420 --name 127-0-0-1 --issue-token --scope-set
grain/external-dev --expires 30d`; `grain skill install --jetski`,
  `--claude`, `--codex`, `--gemini`; six Copy buttons (the first put the
  install line on the clipboard). With `Accept external agents` off it
  warns `The external agent API is off on this deployment…` and links
  `/settings/agents#external-agents` (card in view, top 567); turning it
  on removes the warning.
- Audit: `Configuration changes`, `Credential access`, `Secret
resolutions`; no secret values in any summary. Stubbed 500 on credential
  access → `The credential accesses could not be read (500 Internal Server
Error).` while Configuration changes still lists and Secret resolutions
  says `Nothing recorded yet.` Phone: no tables, `scrollWidth` 390.
- Configuration changes was full of `access code: POST
/api/capability-grants/check by alice@example.com…` rows — **Bug F1**.

### 7. General — PASS

- Deployment (Environment name, Show closed tasks by default), Settings
  lock, Updates (`After an upgrade`: `Don't`, `File a proposal to approve`,
  `File it and queue it (default)`; `Hold the queue until the check is
done`; `Upgrade automatically` `Off (manual only)`), About
  `v0.0.0-20261113022244-4ae6438fdcd4 · 4ae6438 · 2026-11-13 02:22Z`,
  identical to the rail's build stamp.

### 8. Config repo — PASS

- `:8422`: the panel names Internal mode, `grain/config` `main` `611c489b`,
  its working tree, Export now, Config repository mode with the external
  URL field and the administrator warning. `/settings/config-repo/<file>`
  and `/config?path=README.md` open the browser. Demo `:8420` says `This UI
is not running inside a daemon that owns a config repository…`.
- A stubbed `stateRepoSyncFailure` raises the `Config repo` Inbox row.

### 9. Models — PASS

- Gemini 2.5 Pro (Antigravity, Active, Up to 4 at a time, advertises
  `gemini-2.5-pro`, `gemini-2.5-flash`); `Test Connection` → `Connection
valid`. Remove asks `Remove the connection Gemini 2.5 Pro? A credential
stored for it is deleted…` (dismissed; kept).
- New connection offers Antigravity, Claude, Codex (no Custom command);
  abandoning before Continue asks the leave prompt, sends no write and
  leaves no connection behind (no Remove prompt).
- Edit → Credentials `Ambient` shows `Run on host`, CLI path and Advanced
  timeouts.
- Disable → `Every provider connection is disabled, so no run has anything
to run on…` and `No enabled connection serves model gemini-2.5-pro`;
  Enable restores.
- Token usage and budgets lists per-model Used/Limit (`~482k` learned)/
  Left/Reset/Burn/h/Sustains with advisories; Fewest workers is disabled in
  Advisory mode.
- `-agent-paused` (`:8423`): `paused until 9:13:07 AM (about 5h 42m) —
antigravity: usage limit reached…` with Resume; Resume sends `DELETE
/api/pause?connection=gemini-pro` and the pause is gone.
- New task's model picker: `Deployment default (gemini-2.5-pro)`,
  `gemini-2.5-pro`, `gemini-2.5-flash`.

### 10. Agents, Tasks and Memory — PASS

- Agents: see §3 (max turns, Standing instructions, Task branches,
  External agents, Commit identity prompt).
- Tasks: Auto-merge and Queue-immediately defaults off and follow-up depth
  3 survive reload, and New task opens with both boxes unchecked and the
  default grants (`api#self-debug`, `grain › source`, `github ›
read-issues`) listed; restored. Default grants card has About/Remove per
  grant, Add another, Edit as JSON, Add grant…. Task fields: `4 tasks still
carry a value this field no longer offers` opens
  `/?f=custom.priority:legacy:` titled `Priority is a legacy value`, listing
  them.
- Memory: deployment and `acme/widgets` notes plus documents; a correction
  saved (`PUT /api/memory`), read back after leaving and returning, and was
  reverted; the rail, Back and reload each ask while it is dirty. Memory is
  not in the rail and `/repos/acme/widgets` does not mention it.

## Findings

### F1 (fixed): read-only POSTs filled Audit's Configuration changes

- **Reproduction:** sign in with the access code, open `/settings/tasks`
  twice, then `/settings/audit`: Configuration changes gains `access code:
POST /api/capability-grants/check by alice@example.com from 127.0.0.1`
  per visit (New task does the same).
- **Cause:** `recordAccessCodeUse` recorded every non-GET request the code
  authenticated as a change, including routes held to a `:read` scope.
- **Fix:** `admitAccessCodeRequest` treats a request whose route scope is
  `:read` (`APIRoute`) as a read, the same rule `access_roles.go` uses.
  After: `grants-check rows=0`, real writes still listed.

### F2 (fixed): an access-code session removed its own entry without asking

- **Reproduction:** sign in with the access code as `alice@example.com`,
  Settings → Access, Remove `user:alice@example.com` (reason given): it is
  gone at once, and the next access-code sign-in as `alice@example.com`
  answers 403 `alice@example.com is not on this deployment's allowlist`.
- **Cause:** `checkAccessChange` returns early for the access code, so the
  `confirm_self` check never ran; the UI's own self-warning looks only at a
  verified identity.
- **Fix:** removing the `user:` entry for the email the access-code session
  signed in as answers the existing `confirm_self` refusal. Classic now
  shows the server's explanation with `Yes, narrow my own access`; `/next/`
  shows `Remove your own access entry?`; Cancel keeps the entry and
  confirming (`?confirmSelf=1`) removes it. Other entries are unchanged.

**Regression coverage:** `pkg/ui/access_code_mode_test.go`
(`TestAccessCodeReadOnlyPostIsNotAConfigurationChange`,
`TestAccessCodeSessionConfirmsRemovingItsOwnEntry`), each failing without
its fix. `go test ./pkg/ui ./pkg/cli ./tests/security` pass.
