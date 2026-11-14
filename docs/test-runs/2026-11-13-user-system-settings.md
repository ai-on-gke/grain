# User & system settings walkthrough — 2026-11-13 (task 5388)

- **Date:** 2026-11-13, 14:09–15:30 UTC, one run.
- **Task:** task 5388, branch
  `grain/098073e4/task-5388-walk-through-settings-its`, base `main`.
- **Commit tested:** `e69d3422` ("Merge pull request #1478"), binary
  `v0.0.0-20261113135704-e69d34223665`. The fixes below were checked again
  in the browser on rebuilds of that commit with them applied
  (`/var/tmp/grain-fixed*`, each a demo on a fresh store).
- **UI walked:** the Angular UI at `/next/settings/…` throughout — every
  section in scope has a `/next/` page (`ui-ng/src/app/app.routes.ts`,
  `settings/:section`). The classic UI at `/settings/…` was not walked
  this time: the 2026-11-08, 11-09 and 11-11 reports walk it in depth, and
  nothing found here is broken on the classic page (its CLI card, the
  biggest finding, is right there). No step in scope lacks a `/next/` page;
  several *features* within those pages do (see "Parity gaps").
- **Scope:** Settings layout, filter, save/lock/role model, Preferences,
  Access, CLI, Audit, General, Config repo, Models, Agents, Memory and
  Tasks. Git, Hosts, Capabilities, Hub, Engine, CI, Sandboxes, Caches,
  Upgrade and Repositories were opened only for the old-link redirects and
  the lock model.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC unless
    stated, viewport 1400×900 unless stated (tablet 820×1180, phone
    390×844 with touch).
  - Built with `cd ui && npm ci && npm run build`, `cd ui-ng && npm ci &&
    npm run build`, then `go build -o /var/tmp/grain ./cmd/grain`.
  - Servers, each over a throwaway store (`TMPDIR` a fresh directory):
    1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -access-code-file <file>` — the base build; most "before" observations.
    2. The same flags on `:8425` from the fixed builds — re-checks and the mutation walk.
    3. The fixed build on `:8426` with `-external-config-repo https://github.com/example/walk-config` — external config repo mode.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots; layout was checked with
    bounding boxes and `scrollWidth` instead (below).
- **How it was driven:** throwaway Playwright scripts outside the checkout.
  Each signs in through the "Enter access code" page (email + code →
  Continue) and then acts only through visible controls (clicks, typing,
  `Tab`, `Enter`, `Escape`, `/`, `c`). Every page recorded uncaught page
  errors, console errors and warnings, failed requests, `/api/` responses
  of 400 or more (with bodies), every native dialog (text, accepted or
  dismissed) and every non-GET `/api/` request with its body.
- **Console baseline:** each sign-in logs one `401` for the
  unauthenticated first load, which is expected. No uncaught page error and
  no Angular `ERROR` was recorded anywhere. Every other console error is
  listed with its step.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Only these were intercepted:

| Route | Why |
| --- | --- |
| `GET /api/config` + `access: {role: "grain/viewer", scopes: [tasks:read, settings:read]}` (Playwright `page.route`) | The access code always signs in as an operator; used for the role read-only checks. |
| `localStorage["grain.timeZone"] = "Asia/Tokyo"` | General's Display timezone list has no Asia/Tokyo; set directly to check a zone chosen in the classic UI is honoured. |

No GitHub, identity, upgrade, model-provider or config-repo request left
the sandbox: the external config repo is a made-up name the demo never
fetches (it answers `500 … names a repository and nothing more`), and no
connection test was run (the `/next/` Models page has no Test Connection).

## Steps and outcomes

### 1. Layout and paths — PASS with gaps

- The rail's last item `rail-settings` opens `/next/settings`, which on
  desktop shows Models (heading `Models`, nav item `aria-current="page"`).
  Groups: Models & Compute, Repositories & Code, Work & Agents, Access &
  Security, Instance & Preferences (20 sections). The `,` shortcut opens
  Settings too.
- Each section is in the URL: `/next/settings/general` → nav to Tasks →
  Back returns to General, Forward to Tasks, reload keeps Tasks.
- Phone (390 px): bare `/next/settings` shows the section list alone
  (stage hidden); a section opens alone under `← All settings`, which goes
  back to the list. No sideways scroll (`scrollWidth` 390) on the list,
  Audit or CLI. There is no Settings gear in the phone bar (the drawer's
  rail item is the way in).
- Tablet (820 px): the drawer stays a side list; there is no searchable
  Section select (gap).
- Old links on the base build: `/next/settings/hosts`, `/credentials`,
  `/legacy-capabilities`, `/secrets` resolve to Hosts / Capabilities;
  `/next/config?path=…` → Config repo; `/next/memory` → `/next/context`
  (the Context page, by design). **But `/next/admin`, `/next/admin/workflow`,
  `/next/admin/executors`, `/next/system/executors`,
  `/next/admin/integrations`, `/next/system/integrations`,
  `/next/admin/upgrade` and `/next/system/upgrade` all landed on the
  Inbox — bug 2, fixed.**
- Unknown `/next/settings/bogus` shows Models under the bogus URL
  (harmless; noted).
- `#card` anchors (`/next/settings/agents#external-agents`) do not scroll
  anywhere: `/next/` Settings has no per-card anchors (gap).
- Sections with nothing to save (Upgrade, Audit, CLI) show no save bar —
  `/next/` has no "All changes saved" header line at all (its model is
  a save bar that appears only while something is dirty).

### 2. Filter — FAIL → fixed

- Base build: pressing `/` focused the top bar's "Filter this view" box,
  which narrows nothing on Settings; the drawer's own "Filter settings…"
  box was only reachable by clicking — **bug 5, fixed** (the two boxes are
  now one query, so `/` + typing narrows the sections; Escape in the
  drawer box restores all 20).
- The `/next/` filter narrows *sections* by label and keywords, not
  individual fields: `turns` → Agents, `standing` → Agents, Memory, `lock`
  → General, `audit` → Audit, `zzzz` → `No settings sections match
  "zzzz".` On the base build `reasoning` matched nothing although Models
  holds per-model reasoning effort — keyword added. Field-level matches,
  outline-and-focus of the target field and history push are classic-only
  (gap).

### 3. Save, role and lock model — PARTIAL; one data-loss bug fixed

- `/next/` keeps one draft for the whole page: editing Max agent turns
  shows the save bar `1 unsaved change` with Discard, Plan / Preview
  changes and Save changes. Save sent `POST /api/settings
  {"maxAgentTurns":77}`; reload read back `77`.
- **Base build: leaving Settings with that draft (rail → Inbox) dropped
  it without a word, as did a reload; an edited Memory box was dropped by
  just moving to another section — bug 1, fixed.** Now: moving between
  sections keeps the draft and never asks; leaving Settings asks
  `Discard 1 unsaved setting change?` (cancel stays with the draft,
  accept leaves); a reload raises the browser's leave prompt; an edited
  Memory box asks `Discard the unsaved deployment memory edit?` before
  any move, and stops asking once it is put back or saved. A clean page
  never asks.
- Out-of-range numbers: Max agent turns `1234567` and Max unreviewed
  follow-up depth `-5` raised no error and left Save enabled (only the
  QA rollout field validates; see `next-qa-settings.spec.js`). The server
  was not asked; gap.
- No per-section dirty dot in the nav, no "Couldn't save <field>" with
  Retry, no Restart needed chip on `/next/` (gaps; the save bar's error
  banner `settings-error-banner` is the failure path).
- Lock: General → `Lock settings in UI (require config repo PRs)` toggled
  on (`POST /api/settings {"settingsLocked":true}`, saved through the save
  bar). Tasks then showed `Managed in config repo`, the banner
  "This section is read-only because deployment settings are managed in
  the config repository (tables/grain_config.json). Secrets and API keys
  remain editable." with **View in Config repo** →
  `/next/settings/config-repo?path=tables%2Fgrain_config.json`, and every
  Tasks control disabled. Access stayed editable. General's lock switch
  stayed enabled so it could be turned off again (restored to off).
  The switch is offered although the demo has no config repo — the
  classic page disables it then (noted).
- Role: with the viewer stub, Agents, Access and Memory showed the
  `Read-only` pill and "Your role grain/viewer does not hold
  settings:write, which this needs. Ask an admin for a role that includes
  it." (Access added the same line for `access:read`); no input was
  enabled. Models still left 6 inputs enabled (model setting, budget rows)
  under that note, though their buttons were disabled (noted).

### 4. External config repo (`:8426`) — PASS with one fix

- General, Tasks, Agents and Memory: `Managed in config repo`, banner and
  link (`tables/grain_config.json`, Memory → `memory/deployment.md`), all
  their inputs disabled (General keeps its browser-local fields).
- Access (10 inputs enabled), CLI and Audit stayed usable.
- Config repo: `GET /api/state-repo/files` answered `500` (the demo has no
  working tree for a made-up external repo) and, on the base build, the
  file viewer just said "Select a config file…" over an empty list; on the
  plain demo it answered `501 this deployment's UI does not manage a state
  repository` with the same silence. **Fixed (bug 7):** the viewer now
  says "The config repository's files can't be listed: <server's reason>".

### 5. Preferences — PASS (narrower than classic)

- `/next/settings/preferences` holds Color theme (Light / Dark / System),
  Single-key keyboard shortcuts, Default to modern UI and Navigation rail
  destinations; it says "Stored locally in this browser; never modifies
  deployment state" and no `/api/` mutation was sent from it. Dark set
  `data-theme="dark"` and survived reload; put back to System.
- Time zone lives in General (`Display timezone`), not here, and the
  task-timeline order is not on `/next/` (gap).
- **The Display timezone was read by nothing in `/next/`** (it only wrote
  `grain.timeZone`): Audit printed raw `2026-11-13T14:22:17.586668063Z`
  strings — **bug 4, fixed for Audit** (now `Nov 13, 2026, 14:34:31 UTC`,
  or `…23:34:31 GMT+9` with Asia/Tokyo; the raw value stays in
  `<time datetime title>`). A zone chosen in the classic UI that General's
  short list lacks showed as "Browser local time" — fixed, the list now
  names the stored zone. Every other printed time on `/next/` still
  ignores it — follow-up proposed.

### 6. Access — PASS after one fix

- `RBAC allowlist & roles` lists `user:alice@example.com` (operator),
  `group:eng-oncall@example.com` (contributor) and
  `serviceAccount:release-bot@demo-project.iam.gserviceaccount.com`
  (release-manager), each with a role picker and Remove; `Your role:
  operator`.
- **Base build: Grant access with the picker showing `grain/ci-read`
  sent `{"entry":"user:bob@example.com","role":"grain/developer"}` and
  the server refused it with `role "grain/developer" is no scope set in
  effect` — bug 6, fixed.** The picker starts on a role the server serves
  (grain/viewer); the walk added `user:bob@example.com`, saw it after
  reload, and removed it.
- No client-side validation of `user:` / `group:` / `serviceAccount:`
  entries (`bob`, `group:` and a non-`gserviceaccount.com` service account
  all left Grant enabled) and no Reason requirement for service accounts
  (gap). Remove of another principal asks nothing; self-removal asks only
  when the server recognises the entry as the caller, which an access-code
  session (`access-code:alice@example.com`) is not — the walk removed
  `user:alice@example.com` from the throwaway store without a prompt
  (noted in the follow-up).
- There is no "This browser" card, Log out or `/api/whoami` readout on
  `/next/` Access (gap). External agent tokens (`Accept external agent
  tokens`, mint `gma_`) and GCP service account keys live here.

### 7. CLI — FAIL → fixed

- **Base build:** one card showing
  `curl -fsSL http://127.0.0.1:8420/api/hostd/install.sh | sh` and
  `grain login http://127.0.0.1:8420`. `/api/hostd/install.sh` installs a
  root systemd **host** daemon (`/etc/systemd/system/grain-hostd.service`),
  not the workstation CLI (`/cli/install.sh`, which installs `grain` to
  `~/.local/bin`); the login line had none of `--issue-token --scope-set
  grain/external-dev --expires 30d`; no Agent skills, no Copy — **bug 3,
  fixed**, ported from the classic `CLISection.jsx`:
  - Install: `curl -fsSL <origin>/cli/install.sh | sh`, naming the release
    when the build is versioned.
  - Sign in: `grain login <origin> --name 127-0-0-1 --issue-token
    --scope-set grain/external-dev --expires 30d`, with the warning and an
    in-app link to Settings → Access (where `/next/` keeps Accept external
    agent tokens) while the external agent API is off.
  - Agent skills: `grain skill install --jetski|--claude|--codex|--gemini`.
  - Every command has a Copy button; Copy put the login line on the
    clipboard (read back with `navigator.clipboard.readText`).
  - The agent CLI run-location pickers keep their own card.
- Layout (bounding boxes, desktop 1440 and phone 390, light and dark):
  each command box sits inside its card with Copy to its right,
  top-aligned; the phone wraps the login line to 114 px high without
  sideways scroll.

### 8. Audit — PASS after the timezone fix

- `Audit log viewer` with Refresh, actor/action filter, category and time
  window pickers; rows show category, actor (`access-code:alice@…`,
  `task:1`, `operator`), action and summary — no secret values (resolves
  show the secret *name* only). The phone keeps a horizontally scrolling
  table (row 703 px wide inside a 390 px page that itself does not
  scroll) rather than stacked blocks (gap). Independent error/empty states
  were not re-checked this run.

### 9. General — PARTIAL

- Environment name, Deployment ID, Display timezone, the lock switch and a
  Broadcast message card. No Updates (upgrade check / auto-upgrade) and no
  About card on `/next/` General; the build stamp at the rail's foot reads
  `grain v0.0.0-20261113135704-e69d34223665` and Upgrade shows `e69d342`
  (gap).

### 10. Config repo — PARTIAL (demo has no state repository)

- Sync, config pull request, remote/KMS controls render; the file viewer
  now explains a failed listing (step 4). The controller-local daemon walk
  (sync status, browser preview) was not repeated; see 2026-11-11.

### 11. Models — PASS after one fix

- Default model & per-model reasoning effort, Framework API keys & Codex
  auth (write-only; Save key / Check), Model connections & discovered
  models, Token budget & usage guardrails.
- New connection `walk-conn` (`PUT /api/deployment/agent-backends/walk-conn`)
  appeared and survived reload. **Base build: Remove sent `DELETE` at once
  with no confirmation — bug 8, fixed** (`Remove connection walk-conn?`
  with Cancel / Remove connection; Cancel sends nothing).
- Typing a default model no connection advertises (`no-such-model`)
  raised no warning; there is no Test Connection, Enable/Disable or
  pause/resume on `/next/` (gap).

### 12. Agents — PARTIAL

- Max agent turns (saved and read back, step 3), Default capabilities and
  Standing console sessions. Standing instructions are on Memory
  (`input-prompt-extension`); Task branches, External agents (on Access)
  and the Commit identity link are not on `/next/` Agents (gap).

### 13. Memory — PASS after the leave fix

- Standing instructions + Deployment memory markdown with Save memory and
  the document list (`release-checklist`, `flaky-e2e`). A reversible
  correction (`+ "\nWALK NOTE"`) saved (`PUT /api/memory`), read back after
  reload, then restored. Memory is not in the rail (Context is) and the
  repository notes are not shown — the page reads only the deployment row
  of `GET /api/memory` (gap).

### 14. Tasks — PASS

- `Queue new tasks immediately by default` turned off, saved
  (`POST /api/settings {"approvedByDefault":false}`), read back after
  reload; New task (`c`) then opened as a Proposal with `Save as proposal`
  beside `Approve and start now`. Restored to on. Default grants and Task
  fields are not on `/next/` Tasks (gap).

## Bugs found and fixed (all in `ui-ng/`)

| # | Bug | Fix | Regression test |
| --- | --- | --- | --- |
| 1 | Leaving Settings or reloading silently dropped unsaved changes; a Memory edit was dropped by moving to another section | One route for `/settings` and `/settings/:section` (the page and its draft survive section moves), `settingsLeaveGuard`, `beforeunload`, Memory edits tracked | `next-settings-walkthrough.spec.js` tests 3, 4 |
| 2 | Classic `/admin*` and `/system/{executors,integrations,upgrade}` links landed on the Inbox | Redirects to the replacing section | test 1 |
| 3 | CLI showed the host installer and a login without a token | Install / Sign in / Agent skills cards with Copy, as the classic page | test 5 |
| 4 | Audit printed raw RFC 3339 times, ignoring Display timezone | `shared/display-time.ts`; General names a stored zone | test 6 |
| 5 | `/` focused a filter that did nothing on Settings | Drawer filter and top-bar filter are one query | test 2 |
| 6 | Grant access sent a role the picker did not show (and the server refused) | Start on a served role | test 7 |
| 7 | Config file viewer hid a failed listing | Says why | walked only (demo answers 501/500) |
| 8 | Model connection Remove had no confirmation | Confirm dialog | test 8 |

Each "before" was reproduced deterministically on the base build at
`:8420` with the steps above.

## Parity gaps (no `/next/` feature yet)

Recorded for the follow-up task rather than fixed here: per-card `#anchor`
scrolling; field-level filter results; section state line, nav dirty dot,
per-field Retry and Restart needed chip; out-of-range number validation
(beyond QA rollout); tablet Section select; Access "This browser" card,
Log out and principal-entry validation/Reason rules; General Updates and
About; Models Test Connection, Enable/Disable, pause/resume, unknown /
paused default-model warnings, credentials and host placement; Agents Task
branches and Commit identity; Tasks Default grants and Task fields; Memory
repository notes; Preferences task-timeline order and pinned-items
customisation beyond the rail; Display timezone across the rest of
`/next/`; Audit stacked phone layout.

## Tests run

- `ui-ng`: `npx tsc -p tsconfig.app.json --noEmit`, `npm run lint`,
  `npx prettier --check`, `npx vitest run` (all pass).
- `npx playwright test e2e/next-settings-walkthrough.spec.js
  --project=desktop`: 8 passed.
- Full `ui-ng` Playwright suite (`npx playwright test` in `ui-ng/`, what
  `make test-e2e-ng-suite` runs, including `next-full-system.spec.js` and
  `next-qa-settings.spec.js`): 46 passed.
- Screenshots (`ui/e2e/screenshot.mjs --full-page`) of
  `/next/settings/{cli,audit,access,config-repo}` at desktop and phone,
  light and dark, before (`:8420`) and after (`:8425`); not committed.

## Follow-up after review (task 5533), 15:45 UTC

- The leave prompt now uses the shared confirm dialog
  (`g-confirm-dialog`, destructive, accept "Discard"), not
  `window.confirm`: the guard returns a promise the dialog settles. Only
  a reload still uses the browser's own `beforeunload` prompt, which it
  requires. The two leave tests drive the dialog's Cancel / Discard and
  assert no native dialog appears.
- `/admin/integrations` and `/system/integrations` no longer carry a
  `#credentials` fragment: `/next/` Settings has no card anchors yet
  (listed in the follow-up), so the redirect no longer promises one.
- Merged `main`, whose new `/system/:tab` redirects sit after this
  change's exact `/system/{executors,integrations,upgrade}` entries.
- Re-run on the merge: full `ui-ng` Playwright suite 55 passed, 2
  failed: `next-inbox.spec.js` "links the classic Inbox…" needs the
  classic UI built (this fresh sandbox had only built `ui-ng`), and
  `next-walkthrough-director-sessions-diff.spec.js` timed out under the
  full-suite load. Both passed when re-run after `cd ui && npm run build`
  (14/14 with this change's spec).
