# User and system settings walkthrough — 2026-11-08 (task 3615)

Task 3615: Settings' layout, filter, save and config-repo lock model,
Preferences, Access, General, Config repo, Models, Agents, Memory and
Tasks, driven through visible controls in a real Chromium browser against
real `grain demo` and `grain daemon` servers. This is a rerun of the
walkthrough task 3358 recorded in
[2026-11-08-user-system-settings.md](2026-11-08-user-system-settings.md),
after #929 retired Settings → Legacy capabilities.

## Commit and environment

- Commits tested: `21d92af1` (`main`, "Merge pull request #929 from
  ai-on-gke/grain/049f1e09/task-2876-capabilities-remove-the-legacy") for
  the exploratory pass, and `33a44581` (`main`, "Merge pull request #946",
  which touches only System → Health) for the regression run, before and
  after this branch's fixes.
- Sandbox: Debian Linux, Go 1.26.10, Node 20, Playwright Chromium cached
  in `/var/cache/playwright`. Frontend built with `npm ci && npm run build`
  in `ui/`; the server binary is built by the e2e harness from the
  checkout, embedding that bundle.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed run
  or `--look` screenshot review was possible.
- Servers:
  - `grain demo` over a throwaway embedded store (`ui/e2e/fixtures.js`).
  - A real `grain daemon` (`ui/e2e/realdaemon.js` `startDaemon`,
    `directConfigWrites: false`, `-max-mergers 1`) with its own
    controller-local `grain/config` repository, for Config repo, the Config
    Repo Browser, Settings lock and the held config pull request.
- Viewports: desktop `1280×860` (`1440×900` on the daemon), tablet
  `800×1000`, phone `390×844`; the time-zone check ran in a context with
  `timezoneId: "America/Los_Angeles"`.

## Stubs and intercepted routes

Only boundaries the demo or a fresh daemon lack were intercepted with
`page.route`; the Grain server, built bundle and browser gestures stayed
real.

| Route | Where | Why |
|---|---|---|
| `PUT /api/settings` delayed once | demo, General → Environment name | Makes the transient `Saving…` state observable |
| `PUT /api/settings` answered `503` once | demo, General | Drives `Couldn't save <field>` with Retry |
| `GET /api/settings` `pendingRestart` added | demo, Engine | The demo has no restart-pending setting; drives the Restart needed chip |
| `GET /api/config` `stateRepoSyncFailure` added | demo and daemon, Inbox | No config-repo remote to fail; drives the failing-sync alert and Inbox row |
| `GET /api/config` `authMethod: "iap"`, `actor`, viewer `access`; `GET /api/whoami` | demo, Access | No identity-aware proxy; drives the verified-identity This browser card, header badge and viewer read-only gating |
| `GET /api/agent-models` | demo, Models → Test Connection | The model provider boundary: returns a discovered model list |
| `GET /api/config` connection `discovered.models` | demo, New task / session / schedule / template pickers | Feeds a discovered-only model to every picker |
| `GET /api/settings` / `GET /api/deployment` with no backends and an orphan default model | demo, Models | Drives the no-connection and "No connection advertises this model" warnings |
| `GET /api/config` custom field option removed | demo, Tasks → Task fields | Strands a seeded task's value to drive the `custom.<name>:legacy` link |

## Steps and outcomes

Unless noted, each item was exercised through visible controls and
confirmed by reload or deep link.

### Layout and paths

- The header rail gear (`top-bar-settings`) opens Settings on desktop;
  on a phone the bar's Settings button does. Bare `/settings` opens Models
  on desktop and the five-group section list on a phone. Every section's
  `/settings/<section>` survives reload and Back/Forward; `#<card>`
  scrolls the card into view.
- Tablet `800×1000` shows the Section select; phone shows one section
  under the "All settings" back arrow.
- Redirects: `/admin` → General, `/admin/workflow` → Tasks,
  `/admin/executors`, `/system/executors`, `/settings/hosts` →
  `/settings/hostds`, `/admin/upgrade` and `/system/upgrade` → Upgrade,
  `/memory` → Memory, `/settings/legacy-capabilities` → Capabilities. All
  correct.
- **Bug 1 (fixed)**: at tablet width, with Standing instructions dirty,
  picking General in the Section select and cancelling "Discard unsaved
  changes?" kept the page and URL on Agents but left **"General"** in the
  select. See below.

### Filter

`/` focuses the filter; "house style" (a helper keyword of Standing
instructions) lists it under Agents, and picking it scrolls to, focuses
and outlines the field; a miss reads "No setting matches"; Escape restores
the groups. Correct.

### Save and lock model

- Checkboxes and selects saved on change, text/number boxes on blur and
  Enter; Standing instructions waited for its Save and Discard.
- Header states seen: `Saving…`, `Unsaved changes in Standing
  instructions` with the nav dirty dot, `Couldn't save <field>` with
  Retry (Retry then saved), `All changes saved`.
- Leaving a dirty section asked first; a clean one never did.
- Poll interval `1m` read back as `1m0s`.
- `-1` in Max unreviewed follow-up depth and Max worker agents was refused
  by label with no PUT sent; putting the value back cleared the error.
- The Restart needed chip opened `/system/power`.
- On the daemon, ticking General → Lock settings in UI drew the
  Managed-in-config-repo sections disabled with "View in config repo →"
  links into the Config Repo Browser; locked selects stayed off the
  keyboard (`settings-config-modes-walkthrough.spec.js`). A stale second
  tab's write to Auto-merge by default was refused and offered Create
  Config PR. The external-config-repo mode was covered by
  `config-repo-iac-modes.spec.js`, which passed.
- An exploratory probe once saw no `config-repo-link` while iterating
  sections right after locking. Re-reading the code, Caches, Engine and
  Capabilities all draw the notice under the lock (`CONFIG_REPO_SECTION_PATHS`
  includes `caches`; Engine and Credentials check `configLocked`). The
  probe used a non-waiting `count()`, and the existing unit and e2e lock
  tests pass, so I did not record it as a product bug.

### Preferences

Auto/Light/Dark changed the theme; with no time zone chosen, times printed
in the browser's zone (America/Los_Angeles context), and choosing
Asia/Tokyo or UTC moved the Inbox config-repo note and Access's Added
column with it. Timeline order Newest/Oldest first. Customize sidebar:
pinned Memory (a Settings section), Power (a System tab), a board and a
session, reordered, saved, reloaded (the rail kept them), then reset. No
`PUT /api/settings` was sent from Preferences.

### Access

Signed in with the access code ("Signed in with the access code."). With
the identity stub it read "Signed in through IAP as dana@example.com",
matching the header badge, and a viewer saw read-only banners. Adding
`domain:example.com` was refused ("domain: entries are not supported").
Adding `user:walkthrough@example.com` as grain/viewer survived reload and
was then removed. Log out returned to the access-code prompt. Access stayed
usable under the lock and under an external config repo.

### General

Environment name reached the header and title; Show closed by default
saved. Settings lock was disabled on the demo (no config repo) and enabled
on the daemon. Updates' check mode, auto-upgrade channel and time, and
pause-pipeline saved and read back. About's version matched the rail
footer stamp.

### Config repo (daemon)

The panel showed `grain/config` and its sync status. `/settings/config-repo/tables/grain_config.json`
previewed JSON with its commit stamp, and `/config?path=README.md` redirected to
`/settings/config-repo/README.md` with a Markdown preview. The stubbed sync
failure raised the alert and an Inbox row whose Config repo button
returned to `/settings/config-repo`.

### Models

Test Connection showed the stubbed discovered models. Disable raised
`all-connections-disabled` and "No enabled connection serves model …";
Enable cleared both. Remove asked first (declined, kept). An unfinished
New connection left by a reload deleted itself without a Remove prompt.
Per-model reasoning effort persisted across reload and was put back. A
paused connection resumed from its row (`settings-models-walkthrough.spec.js`).
The no-connection and unadvertised-default warnings appeared with their
stubs. The discovered-only model reached the New task, `/session/new`,
New schedule and New template pickers. Where each agent CLI runs reported
container mode and readiness.

### Agents

Max agent turns saved on blur. Standing instructions Save and Discard
worked. Task branches refused a non-ref template and a template without
`{deployment}`/`{id}` uniqueness while keeping the sample 8-hex deployment
ID visible.

- **Bug 3 (fixed)**: Commit identity's "Settings → Git" was a bare `href`.
  It reloaded the whole app, and with a dirty Standing instructions it
  skipped the in-app "Discard unsaved changes?" prompt. CLI → Sign in's
  "Settings → Agents" had the same problem.

### Memory

Deployment and repository notes and their documents listed. A reversible
correction saved, read back on the next open, and was reverted. Leaving
with an unsaved edit asked first via the nav, the rail, Back and reload
(`beforeunload`). Memory was not in the rail unless pinned and did not
appear on `/repos/<owner>/<name>`.

### Tasks

Unticking approved-by-default and auto-merge-by-default, plus adding the
`api:tasks:read` default grant, survived reload; New task opened with both
boxes unticked and the grant listed. All three were then restored. Backlog
& follow-up limits saved. The stranded "still carries a value this field
no longer offers" link opened the task list filtered on
`custom.priority:legacy`.

- **Bug 2 (fixed)**: on the demo (no legacy capabilities offered), the
  Default legacy capabilities menu read "None configured — the rows under
  Settings → Legacy capabilities are where a capability is set up". That
  points at the tab #929 removed. Every other legacy picker says
  `NO_CAPABILITIES_HINT` ("None offered -- grant access under Grants").

## Bugs and fixes

| # | Reproduction | Fix | Regression coverage |
|---|---|---|---|
| 1 | Tablet `800×1000`, `/settings/agents`, type into Standing instructions, Section select → General, Cancel → the box reads "General" while the page is Agents | `SearchSelect.jsx`: when a controlled single-value caller is handed a different choice, give MUI a fresh copy of the kept option so it resets the box text to that option's label | `SearchSelect.test.jsx` "puts the kept option's label back…"; `SettingsPage.test.jsx` tablet cancel test; e2e "the tablet Section select keeps the current section…" |
| 2 | `/settings/tasks` on the demo → open Default legacy capabilities → the item names the retired Settings → Legacy capabilities | `TasksSection.jsx` renders `NO_CAPABILITIES_HINT` | `TasksSection.test.jsx`; e2e "Tasks' empty Default legacy capabilities menu points at Grants" |
| 3 | `/settings/agents`, dirty Standing instructions, click Commit identity's Settings → Git → full page load, no prompt (same for CLI → Sign in's Settings → Agents) | `AgentsSection` and `CLISection` take `onSection` and intercept plain left clicks (modified clicks still open a new tab); `SettingsPage.go` takes an optional anchor so the CLI link lands on `#external-agents` | `SettingsPage.test.jsx` "opens Git from Agents and Agents from CLI in place…"; e2e "Agents' Git link and CLI's Agents link open in place and guard unsaved edits" |

All three new e2e tests fail against `33a44581` without the fixes and
pass with them.

## Tests run

- `npx vitest run src/components/SearchSelect.test.jsx src/components/SettingsPage.test.jsx src/components/settings/`
  — 25 files, 206 tests passed.
- `npx playwright test e2e/user-system-settings-walkthrough.spec.js e2e/settings-config-modes-walkthrough.spec.js e2e/config-repo-only-settings.spec.js e2e/settings-models-walkthrough.spec.js e2e/access-principals-settings.spec.js e2e/memory.spec.js --project=chromium`
  — 39 passed. `e2e/config-repo-iac-modes.spec.js` passed in the
  exploratory run.
- `node e2e/screenshot.mjs` before and after for `/settings/agents`,
  `/settings/tasks`, `/settings/cli` at desktop, tablet and phone in light
  and dark, plus `/settings/tasks` with the Default legacy capabilities
  menu open (desktop, light and dark). The screenshots were not committed.
