# User and system settings walkthrough — 2026-11-07

Task 3162: Settings' layout, filter, save and config-repo lock model,
Preferences, Access, General, Config repo, Models, Agents, Memory and
Tasks, driven through visible controls in a real Chromium against real
grain servers.

## Commit and environment

- Commit tested: `5dd24214` (main, "Merge pull request #858"), then the
  same tree with this branch's three fixes applied for the re-runs marked
  "fixed build". The About card and the rail both read
  `v1.2.0 · 5dd2421-dirty · 2026-11-07 16:41Z`.
- Sandbox: Debian, Go 1.26.10, Node 20.19.0, Playwright Chromium 1234
  (cached in `/var/cache/playwright`). `npm ci && npm run build` in `ui/`;
  `ui/e2e/global-setup.js` builds the `grain_e2e` binary.
- Servers:
  - `grain demo` over a throwaway embedded store, one per Playwright
    worker (`ui/e2e/fixtures.js`). No config repository.
  - A real `grain daemon` (`ui/e2e/realdaemon.js` `startDaemon`, with
    `directConfigWrites: false`, `-max-mergers 1`). It has its own
    controller-local `grain/config` and a fake `agy` CLI. Used for the
    Config repo panel, the lock and refusals.
- No `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed
  agent or screenshot description was used. Screenshots were compared by
  pixel diff and by DOM geometry and attributes, not by eye.
- Viewports: desktop 1280×860 (and 1280×600 for the `#card` scroll),
  1440×900 on the daemon, tablet 800×1000, phone 390×844. Preferences ran
  in a separate context with `timezoneId: America/Los_Angeles`.
- The walkthrough scripts were throwaway Playwright specs
  (`ui/e2e/zz-walk-*.spec.js`) that logged a PASS or FAIL line per step.
  They are not committed. The regressions below are.

## Stubs and intercepted routes

| Route | Where | Why |
|---|---|---|
| `GET /api/settings` + `pendingRestart: ["environmentName"]` | demo, Restart-needed chip | The demo has no setting pending a restart |
| `PUT /api/settings` → 503 once | demo, Agents → Max agent turns | Forces "Couldn't save … / Retry" |
| `PUT /api/settings` delayed 500 ms once | demo, General → Environment name | Makes "Saving…" observable |
| `GET /api/config` + `stateRepoSyncFailure` | demo, Preferences time zone, Inbox row | The demo has no config repo to fail; it is the one time a page prints deterministically |
| `GET /api/config` with `medium` dropped from `priority`'s options | demo, Tasks → Task fields | Creates tasks that carry a withdrawn value |
| `GET /api/whoami` and `GET /api/config` as a verified IAP `dana@example.com` with role `grain/viewer` | demo, Access | The demo has no identity verifier |
| `GET /api/settings`/`/api/config` + `settingsLocked`/`configRepoOnly`/`externalConfigRepo`; writes answered 409 | demo, committed `settings-config-modes-walkthrough.spec.js` | Lock and external modes without locking the shared demo store |
| `PUT /api/settings` → 409 `config_managed_externally`, using the body the real daemon sent | demo, committed refusal regression | Same reason as above |

The lock, refusal, Create Config PR and config-repo panel steps ran
unstubbed on the real daemon. No GitHub, upgrade or model-provider stub
was needed. Test Connection ran against the demo's own connection.

## Steps and outcomes

### Layout and paths — pass

- The gear (`top-bar-settings`) opens `/settings/models`. Bare
  `/settings` on desktop also opens Models. The nav lists Models &
  sandboxes, Secrets & access, This browser, System / Operations and
  Managed in config repo.
- `/settings/tasks` survives a reload. Back goes to Models, Forward to
  Tasks. `/settings/tasks#task-fields` at 1280×600 scrolls the card to
  y < 350.
- Tablet: the nav becomes the `Section` select, and choosing Agents
  navigates there. Phone: `phone-settings` opens the grouped list.
  Agents opens alone under the `All settings` back arrow with no
  horizontal overflow, and the arrow returns to the list.
- Old links: `/admin` → General, `/admin/workflow` → Tasks,
  `/admin/executors` and `/system/executors` → Hostds ("Hosts"),
  `/admin/upgrade` and `/system/upgrade` → Upgrade, `/memory` → Memory,
  `/config?path=tables/grain_config.json` →
  `/settings/config-repo/tables/grain_config.json`.

### Filter settings — pass

`/` focuses "Filter settings". Each of "turns", "house style", "theme",
"pinned" and "channel" lists its section (Agents, Agents, Preferences,
Preferences, General). Choosing "Max agent turns" goes to
`/settings/agents#maxAgentTurns`, focuses the box and outlines it
(`.setting-highlight`). "zzqqxx" says `No setting matches 'zzqqxx'.`, and
Escape clears the box and restores the groups.

### Save model — pass after one fix

- Checkbox and select commit on change. Environment name commits on
  Enter, shows "Saving…" and then "All changes saved", and sets the tab
  title. Nothing is sent while typing.
- Engine → Poll interval `1m` reads back as `1m0s`, also after a reload.
- Max agent turns `-5` is refused by name ("Max agent turns must be at
  least 0; -5 was not saved.") with no PUT sent. Putting `0` back clears
  the error. A stubbed 503 shows "Couldn't save Max agent turns" with
  Retry, and Retry saves `15`, which survives a reload.
- Standing instructions waits for its own Save. Editing shows "Unsaved
  changes in Standing instructions" and the nav dirty dot. Leaving through
  the section nav or the rail asks first (two prompts, dismissed, page
  kept). Discard restores, Save persists across a reload, and leaving a
  clean section asks nothing.
- Restart needed chip (stubbed) → `/system/power`.
- **Bug C (fixed):** General → Updates → Time of day committed only on
  blur. Enter did nothing (`timeSavedOnEnter=false`), unlike every other
  Settings text box. Fixed build: `timeSavedOnEnter=true`.

### Lock and config repo, real daemon — two bugs found and fixed

- Config repo panel: `grain/config`, branch `main`, and the buttons
  Export now, Use external config repository, Show files, Back up secrets
  here and Import key, plus the secrets-key path. Show files opens the
  read-only browser inline. `/config?path=tables/grain_config.json`
  deep-links into it. `README.md` previews as Markdown (headings "What is
  here", "Changing something", "Secrets are not here").
- General → Settings lock is enabled with a config repo and disabled on
  the demo ("Unavailable without a config repository").
- Locked (`settingsLocked: true`): General, Git, Repositories, Tasks,
  Agents, Capabilities, CI, Memory and Caches render read-only, each with
  "View in config repo → <path>" (`tables/grain_config.json`,
  `tables/repo_config.json`, `tables/custom_field.json`,
  `tables/api_scope_set.json`, `memory/deployment.md`). The only enabled
  inputs left are General's lock box, Engine's Max worker agents (a
  bootstrap setting, by design) and Legacy capabilities' secret
  key/value fields. Access, with Log out enabled, and Models stay usable.
- **Bug A (fixed):** a refused settings save never offered the config
  pull request. Steps: open `/settings/tasks`, lock settings from a
  second page, then tick "Auto-merge new tasks by default". The daemon
  answers `409 {code: "config_managed_externally", repo: "grain/config",
  path: ["tables/grain_config.json"], change: [...]}`.
  `useSettingsSection` passed only the message string to `showError`, so
  no `config-refusal-dialog` opened (count 0). The whole multi-line patch
  went into the toast, and the header offered a Retry that could only be
  refused again. Fixed build: the dialog opens (count 1), its Create
  Config PR opened "config pull request #2 as task 2. It waits for a
  person to submit it.", the header says "Couldn't save Auto-merge new
  tasks by default" with no Retry, and the box rolls back. After
  unlocking, a later save brings the header back to "All changes saved".
- **Bug B (fixed):** Registry's locked cards ("Catalog &
  authentication", "Scope overrides") were wrapped in a bare `<fieldset
  disabled>` instead of `ReadOnlyFieldset`. On the locked daemon its three
  MUI selects (Credential source, Host kind, Rule) stayed enabled:
  `enabledCombos: 3`, no `aria-disabled`, still in the tab order. Fixed
  build: `aria-disabled="true"`, `tabindex="-1"`; ArrowDown, Enter and
  Space open no listbox, and no PUT is sent.
- Template refusal → dialog → Create Config PR opened a held pull
  request, as before. Unlocking from General persisted across a reload.

### Preferences — pass

Dark persists across a reload, then Light, then Auto. The time zone
defaults to "Browser default (America/Los_Angeles)", and the Inbox's
config-repo failure row prints the Los Angeles time. Choosing Asia/Tokyo
reprints it in Tokyo time. Oldest first lists two new comments on task 1
alpha-then-beta, and Newest first flips them. Customize sidebar: pinned
Settings → Memory, System → Power, the board "Board" and the session
"Director"; moved the session above the board; saved. After a reload the
rail shows all four, with the session above the board by bounding box.
Unpinning Power removes it after a reload, and pinned Memory opens
`/settings/memory`. The pins live in `localStorage` (`grain.pinned`). The
Preferences context sent no API writes (the read-only
`/api/capability-grants/check` dry run on the task page is ignored).

### Access — pass

"Signed in with the access code.", recorded as "operator (access code)".
An entry without a prefix is refused ("Entry must start with user:…"). A
`user:` entry with the default `grain/operator` role needs a reason; with
one it is added, survives a reload, and Remove takes it away. A fresh
browser signed in at `/settings/access` returns there, and Log out goes
back to "Enter access code", for `/settings/general` too. Stubbed IAP
identity: "Signed in through IAP as dana@example.com", the rail badge
reads `dana@example.com`, and as `grain/viewer` General shows the
read-only role notice with Environment name disabled.

### General — pass

Deployment (environment name, closed tasks by default, both persisted),
Settings lock, and Updates: check mode "approve", hold the queue, daily
upgrade, channel "candidate", time 04:30. All persisted across a reload
and were then restored. About matches the rail's build stamp exactly.

### Models — pass

One connection, "Gemini 2.5 Pro" (Antigravity, active, up to 4 at a
time), advertising gemini-2.5-pro and gemini-2.5-flash. Disable shows
"Every provider connection is disabled…", which survives a reload, and
Enable clears it. Remove asks first ("Remove the connection Gemini 2.5
Pro? …"), and dismissing keeps the connection after a reload. Edit shows
Credentials Stored / Ambient. Starting New connection and leaving left
the count at 1, with no Remove prompt. "Where each agent CLI runs"
reports a bare-machine daemon: every CLI on the host, with readiness (for
example claude "host (not found on PATH)"). The New task, `/session/new`,
schedule and template model pickers all offer "Deployment default
(gemini-2.5-pro)", gemini-2.5-pro and gemini-2.5-flash. The paused
connection or model Resume, discovered models and unadvertised-default
warnings were exercised through the existing
`model-source-pause-walkthrough`, `settings-models-walkthrough`,
`settings-panel-walkthrough` and `user-system-settings-walkthrough` specs
against the demo, all green (22 tests together with the fix-ups).

### Agents — pass

As under Save model. Task branches refuses `grain/no-id`, `bad branch
{id}` and `a..b/{id}` ("Couldn't save Branch name template") and accepts
`walk/{deployment}/task-{id}`. An invalid Deployment ID is refused while
the sample ID (e.g. `23faf92e`) stays as the placeholder and in the card.
The Commit identity link opens `/settings/git`.

### Memory — pass

There is no Memory entry in the rail unless it is pinned, and none on
`/repos/acme/widgets`. The section shows this deployment's note, the
acme/widgets note, and the documents `release-checklist` (it opens inline
with summary and text) and `flaky-e2e`. An edit shows "Unsaved changes in
Memory". The rail and Back each ask first, and `beforeunload` is armed
for a reload. The correction saved, read back after leaving and
returning, and was reverted.

### Tasks — pass

Queue and auto-merge defaults turned off, legacy capability "github
sandbox" added, an API grant (task-admin) added to Default grants,
newest-first on, follow-up depth 3. All read back after a reload. The New
task form starts with Queue immediately and Auto-merge unchecked and
lists the legacy capability. Task fields: with `medium` withdrawn, "2
tasks still carry a value this field no longer offers" opens
`?f=custom.priority:legacy` listing those 2 tasks. Settings were
restored afterwards.

### Console and network

The only console errors and failed requests were the ones the steps
provoked: the stubbed 503 and the refused 400s on `PUT /api/settings`
(branch template, Deployment ID), and the daemon's 409s on `PUT
/api/settings` and `POST /api/templates` under the lock.

## Fixes and regression coverage

| Bug | Fix | Coverage |
|---|---|---|
| A: refused settings save dropped `configRefusal` | `ui/src/useSettingsSection.js` hands the error itself to `showError` (which opens `ConfigRefusalDialog`), keeps a short status message and offers no Retry for a refusal | `useSettingsSection.test.jsx` "hands a config-repo refusal to showError whole"; e2e `settings-config-modes-walkthrough.spec.js` "a setting refused for the config repo opens the refusal dialog". Both fail on `5dd24214` |
| B: Registry locked selects reachable | `RegistrySection.jsx` wraps both config-managed cards in `ReadOnlyFieldset` | `RegistrySection.test.jsx` "keeps locked selects out of reach of the keyboard"; e2e locked-select loop gains Registry's Credential source, Host kind and Rule. All fail on `5dd24214` |
| C: Time of day ignored Enter | `UpgradePanel.jsx` blurs on Enter, which commits | `UpgradePanel.test.jsx` "saves a new time of day on Enter" (fails without the fix) |

Runs: `npx vitest run` passed 256 files, 3968 tests. Playwright, on the
fixed build: `settings-config-modes-walkthrough` (9),
`config-refusal-pull-request`, `model-source-pause-walkthrough`,
`settings-models-walkthrough`, `settings-panel-walkthrough` and
`user-system-settings-walkthrough` all pass.

Screenshots: `node e2e/screenshot.mjs --pages
/settings/registry,/settings/tasks`, desktop 1440×900 and phone 390×844,
light and dark, before (base) and after. Pixel diff: Tasks desktop is
identical. The only differing regions are small relative-time and badge
areas (≤ 54×10 px on desktop Registry, the 21×21 header badge on phone),
so the unlocked layout is unchanged. The locked Registry rendering was
checked on the daemon by attributes and keyboard (above).
