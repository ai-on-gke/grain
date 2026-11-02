# Settings and page fixes across the three config-repo modes — 2026-11-02

Walkthrough of [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §11: the Settings and page fixes merged from the 11-01 walkthroughs (PRs #401, #400, #364, #329, #352, #344, #383, #378, #349, #322, #339, #368, #354, #353, #345, #341, #348, #346, #343, #340, #325), checked side by side in grain's three config modes in a real browser. Task 2453, filed by task 2399.

**Result:** the modes behave as `design/design/config-repo-settings.design.md` §2.1/§4.1.1 describes, with two read-only leaks, both fixed here:

1. With an external config repo, **Settings → Access drew Log out disabled**, under a banner saying to edit `tables/grain_config.json`.
2. In a read-only card (settings locked, or an external config repo), **a MUI Select stayed reachable from the keyboard**. It opened on ArrowDown, and picking a value sent a `PUT /api/settings` that the API refused with `409`.

All cold link follows landed on their target, the disconnected banner came and went within one poll, and no page scrolled sideways or ran off the edge at 1440 or 390 px.

## Environment

- **Code:** branch `grain/049f1e09/task-2453-walkthrough-settings-and-page` off `main` at `cc939397`, Go 1.26.10. `cd ui && npm ci && npm run build`, then `go build -o ~/bin/grain ./cmd/grain`, so every binary embeds the real UI. The "before" observations are from that build. The "after" ones are from a rebuild with the fix.
- **No config repo:** `grain demo -open=false -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:18701 -access-code-file …`.
- **Internal config repo:** a real `grain daemon` (no run host was needed: nothing here dispatches).
  - Set up with `grain init workstation -non-interactive -repo acme/widgets -sandbox-backend host -model-backend claude -ui-addr 127.0.0.1:18702 -start-service=false` under `HOME=/home/debian/live`.
  - Started from the `daemon.sh` that init wrote, with `-sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http`.
  - `GET /api/state-repo` reports `mode: internal` (`grain/config` at `<data-dir>/state-repo`, schema 85).
  - Walked twice: with `settingsLocked: false` (the default) and with `settingsLocked: true`.
- **External config repo:** `grain demo … -addr 127.0.0.1:18703 -external-config-repo https://github.com/acme/grain-config`. `-external-config-repo` is a `grain demo` flag; a daemon gets the same mode from `POST /api/state-repo/remote`.
- **Callers:** Playwright (Chromium, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), authenticated with each server's bearer token: the demo access code, or the daemon's `local-api-token`.
- **Scripts:** all under [`2026-11-02-settings-config-modes/`](2026-11-02-settings-config-modes/), run from `ui/` so `playwright` resolves:
  - `settings-table.mjs` walks every Settings section and dumps each control's `disabled`, the notices and every link.
  - `links-cold.mjs` follows the links on Health, Agents and Git, each in a fresh browser context.
  - `disconnect.mjs` kills and restarts the daemon under open pages.
  - `layout-audit.mjs` is the DOM layout check described under "Screenshots" below.
  - Their JSON and text output sits beside them.

The plan names the design as `docs/design/config-repo-settings.md`. It lives at `design/design/config-repo-settings.design.md`.

## 1. Three modes side by side

`settings-table.mjs` gives each section's controls disabled out of visible controls, then the notice shown and the config-repo file it links. In that output, "notice" is the config-managed notice and "ext banner" is the external-config-repo banner. All four columns are from the fixed build (`none.json`, `internal-unlocked.json`, `internal-locked.json`, `external.json`). `external-before-fix.json` is the external column before the fix.

| Section | none | internal, unlocked | internal, locked | external |
|---|---|---|---|---|
| models | 1/16 | 1/8 | 1/8 | 14/16 · ext banner; ext:tables/grain_config.json |
| sandboxes | 0/36 | 0/36 | 1/36 | 36/36 · ext banner; ext:tables/grain_config.json |
| executors | 0/2 | 0/2 | 0/2 | 0/2 · ext banner; ext:tables/grain_config.json |
| credentials | 0/20 | 0/24 | 5/24 · notice; tables/grain_config.json | 5/20 · ext banner; ext:tables/grain_config.json |
| access | 0/1 | 0/1 | 0/1 | 0/1 (was 1/1 · ext banner) |
| preferences | 0/7 | 0/7 | 0/7 | 0/7 |
| upgrade | 0/5 | 0/5 | 0/5 | 0/5 |
| caches | 0/4 | 0/4 | 4/4 · notice; tables/grain_config.json | 4/4 · ext banner; ext:tables/grain_config.json |
| engine | 0/5 | 0/5 | 4/5 · notice; tables/grain_config.json | 5/5 · ext banner; ext:tables/grain_config.json |
| config-repo | 0/0 | 3/12 | 3/12 | 2/13 |
| general | 2/7 | 1/7 | 6/7 · notice; tables/grain_config.json | 7/7 · ext banner; ext:tables/grain_config.json |
| git | 0/4 | 0/1 | 0/0 · notice; tables/grain_config.json, tables/repo_config.json | 1/1 · ext banner; ext:tables/grain_config.json |
| repositories | 0/1 | 0/1 | 1/1 · notice; tables/grain_config.json, tables/repo_config.json | 1/1 · ext banner; ext:tables/repo_config.json |
| tasks | 0/7 | 0/7 | 6/6 · notice; tables/custom_field.json, tables/grain_config.json | 6/6 · ext banner; ext:tables/grain_config.json |
| agents | 0/5 | 0/4 | 4/4 · notice; tables/grain_config.json | 5/5 · ext banner; ext:tables/grain_config.json |
| capabilities | 1/6 | 1/4 | 3/3 · notice; tables/api_scope_set.json, tables/grain_config.json | 3/3 · ext banner; ext:tables/grain_config.json |
| ci | 0/14 | 0/14 | 14/14 · notice; tables/grain_config.json, tables/repo_config.json | 14/14 · ext banner; ext:tables/grain_config.json |
| memory | 4/11 | 2/4 | 4/4 · notice; memory/deployment.md | 11/11 · ext banner; ext:memory/ |

Control counts differ between the demo and the daemon because their data differs: the demo seeds connections, a Git connection and capability servers that the fresh daemon has none of.

**Checked against the design:**

- **No config repo, and internal unlocked.**
  - Every section is editable and none shows a notice.
  - The few disabled controls are idle buttons: Save run locations with nothing changed, Save policy, memory Save/Revert. The other one is General → Time of day, which waits on a channel.
  - With no config repository at all, General's Lock settings toggle is disabled with "unavailable without a config repo" (#401).
- **Internal locked.**
  - The bootstrap sections stay editable: Models, Executors, Legacy capabilities' secret rows, Access, Preferences, Upgrade, Config repo. Sandboxes keeps everything but the session idle TTL (settings-mutability D-1), and Engine keeps Max worker agents (§4.1.1 `MaxWorkers`).
  - Every config-managed section is read-only, with the notice and its `/config?path=` links. Tasks links `custom_field.json` and does not repeat `settings/capabilities.yml`, which isn't exported here (#400).
  - Engine, Caches and Legacy capabilities' config fields are greyed (#329).
  - The Lock toggle itself stays enabled, so the lock can be lifted from the UI.
- **External.**
  - Everything is read-only under the external banner, except:
    - the credential/secret paths: Models → Test connection and Edit (credential) (#364), Legacy capabilities' 15 secret rows, and Executors → Issue token (#364);
    - Preferences, Upgrade and Config repo, where Pull now and Switch to internal stay enabled;
    - Access, after the fix.
  - Links go to the external repository's `blob/main/<file>`.
- **The API agrees.** `save-refuse.txt` has a config-managed field (`pollInterval`) and a bootstrap field (`sandboxCpus`) written in each mode, with a fresh value each time:

  | mode | pollInterval | sandboxCpus |
  |---|---|---|
  | none | 200 | 200 |
  | internal, unlocked | 200 | 200 |
  | internal, locked | 409 `config_managed_externally`, `tables/grain_config.json` | 200 |
  | external | 409 `config_managed_externally` | 409 `config_managed_externally` |

  An earlier pass sent the locked write the same value the unlocked step had just saved. That write was a no-op and returned 200, which is expected rather than a refusal missed.

## 2. Links followed cold

`links-cold.mjs` collects every in-app `<a href>` in the main pane of `/system/health`, `/settings/agents` and `/settings/git`, at 1440×900 and 390×844. It opens each one in a new browser context and checks that the link's target is on screen once the page settles. The target is:

- for an `#anchor`, the card or the field's `data-setting-id`;
- for `/config?path=`, the named file in the browser;
- otherwise, the section heading.

To give Health links to follow, a real pending restart was created on the daemon by changing `k8sNamespace` and `k8sPriorityClassCi`.

| deployment | links followed (both widths) | result |
|---|---|---|
| none | Agents → Settings → Git | 2/2 ok |
| internal, unlocked | Health → `/settings/sandboxes#k8sNamespace`, `#k8sPriorityClassCi`; Agents → Git | 6/6 ok |
| internal, locked | the above plus Agents/Git → `/config?path=tables/grain_config.json`, Git → `/config?path=tables/repo_config.json`, Git → Legacy capabilities | 14/14 ok |
| external | Agents → Git, Git → Legacy capabilities (external `blob/` links not followed) | 8/8 ok |

- Health's pending-restart links land on the field itself (#383). At 390 px the field sits at y=389/360 of 844, on a tall Sandboxes card (#378).
- Agents' Commit identity links to Settings → Git (#349).
- Git links only the files that exist (#322).
- The first pass flagged the three `/config?path=` links at 390 px, because it checked for the page heading, which the page correctly scrolls past to the file browser. `grain_config.json` was at y=692 of 844. The check was corrected to look for the file, and the re-run is the one recorded.

## 3. Disconnect and reconnect

`disconnect.mjs` opens a page on the daemon and kills it, waits for `disconnected-banner`, then restarts the daemon after 5 s. It records when the API answers again and when the banner clears.

| signal | page | banner appeared after kill | banner text | cleared after the daemon answered |
|---|---|---|---|---|
| SIGTERM | `/` | 133 ms | "grain is restarting -- an upgrade or a restart is replacing the server…" | 923 ms |
| SIGTERM | `/settings/models` | 133 ms | same | 797 ms |
| SIGTERM | `/system/health` | 138 ms | same | 792 ms |
| SIGKILL | `/` | 3.33 s | "This deployment cannot be reached: the connection to it failed…" | 291 ms |
| SIGKILL | `/settings/models` | 3.34 s | same | 286 ms |

Both cases come and go within one poll. The banner clears on the first answer (#354).

## 4. Screenshots, and how they were looked at

`make ui-screenshots` (`node ui/e2e/screenshot.mjs --url … --code …` against the running fixed build) shot these pages at desktop (1440) and phone (390), in light and dark:

- On the no-config-repo demo, 88 shots: all 18 Settings sections, `/system/health`, `/repos`, `/boards/board-1` (kanban) and `/boards/board-3` (the report board).
- On the external demo, 72 shots: all 18 Settings sections.

**The shots could not be looked at by eye in this sandbox.** The agent's file reader returns a PNG as bytes, and there is no Gemini key for `LOOK=1`. So `layout-audit.mjs` checks the same pages, widths and themes from the DOM instead:

- the page scrolling sideways;
- a visible element running past the right edge outside a container that scrolls on purpose;
- nowrap text clipped without an ellipsis;
- text below 3:1 contrast against the background behind it.

Its results (`audit-none.json`, `audit-external.json`):

- **No page scrolls sideways and nothing runs off the edge**, at either width, in either theme or mode.
- **Clipped text:** 4 hits, all the 1-px visually hidden `h2` that `/repos` and the kanban board carry for screen readers. False positives.
- **Low contrast, light theme only:**
  - `form .hint` / `.settings-content … .hint` (`var(--text-tertiary)`) measures 2.99:1 on Settings → Sandboxes, Legacy capabilities, Caches, General and Tasks.
  - Board metadata measures 2.76–2.99:1: `.count`, `.task-number`, `.chip-field-name`, `.task-activity-*`, `.board-column-empty`.
  - Dark theme had none.
  - This is pre-existing and shared, so it is filed as a follow-up rather than changed here.

Layout rules that matter for the fix are asserted with bounding boxes in the regression spec (below). Mobile-specific checks from the 11-01 fixes:

- The Add repo, repo crumbs, Logs select and Customize drawer fixes (#340, #345) have their own specs, which passed in CI on `main`.
- On this run's 390 px shots of `/repos` and the board, the audit found nothing off-edge.

The other §11 fixes have their own merged specs and were not re-walked by hand:

- the paused default-model warning (#339);
- capability-server secrets (#368);
- the report's links (#353);
- the empty Persistent sandboxes panel (#341);
- the Blocked chip (#348);
- a board's asked state (#346);
- sort keys (#343);
- a director's child (#325).

The report board and Health were among the shots and audits, with nothing found.

## Findings

### F1 — fixed: Access's Log out is disabled under an external config repo

**Reproduction:**

1. Run `grain demo -external-config-repo https://github.com/acme/grain-config` and open `/settings/access`.
2. The "This browser" card sits inside `fieldset[disabled][data-settings-readonly]`, under "Configuration is read-only and managed in … Edit tables/grain_config.json in acme/grain-config".
3. The **Log out** button is `:disabled`.

`external-before-fix.json`, `access`: `{"label":"log out","disabled":true}`.

Sign-in is deliberately outside the config repo (design §1, invariant 7), and nothing on Access is in `grain_config.json`.

**Cause:** `SettingsPage.jsx` treats every section not in `NOT_CONFIG` as read-only under an external repo.

**Fix:** `access` joins `NOT_CONFIG`, beside `preferences`, `upgrade` and `config-repo`.

### F2 — fixed: Selects in a read-only card open from the keyboard and send a refused write

**Reproduction:**

1. On the internal daemon, `PUT /api/settings {"settingsLocked":true}`, then open `/settings/tasks`.
2. Focus "Default legacy capabilities" (it is `tabindex=0` and not `aria-disabled`) and press ArrowDown. The listbox opens.
3. Press ArrowDown, then Enter. The page sends `PUT /api/settings {"defaultCapabilities":["bootstrap-playbooks"]}` and gets `409`.

General → Updates' "Channel" does the same: `{"autoUpgradeChannel":"latest","autoUpgradeTime":"03:00"}` gets `409`. So does every Select inside a read-only card, in locked and external mode.

**Cause:** `ReadOnlyFieldset` relies on `<fieldset disabled>`, which does not reach MUI's `div` Select, and stops only the mouse (`pointer-events: none`).

**Fix:** `ReadOnlyFieldset` marks `.MuiSelect-select` and `.MuiChip-deletable` inside it `aria-disabled="true"` / `tabindex="-1"` as they are drawn, using a `MutationObserver` because sections draw some only after their data loads. It also swallows non-Tab keydowns aimed at them.

Re-checked live after the fix: `tabindex -1`, `aria-disabled true`, ArrowDown opens nothing, no write.

### F3 — follow-up: Executors under an external repo says it is read-only while all of it works

`/settings/executors` in external mode shows the external banner ("Configuration is read-only … edit tables/grain_config.json"). Yet its only card is `allowInExternalMode` (#364), and executor tokens are a runtime auth table that is not exported (design §2.1).

#364's unit test asserts the banner there, so changing it is a decision rather than a fix. It was filed as a follow-up.

### F4 — follow-up: light-theme hint and board metadata text is under 3:1

This is the audit's low-contrast list above. It is pre-existing and shared across pages. Filed as a follow-up.

### Notes

- `grain init workstation -repo acme/widgets` produced a daemon whose Repositories section lists only `grain/config`. That is not something §11 covers, and it was not investigated.
- `disconnect.mjs`'s node process did not exit on its own after `browser.close()`, and was ended by `timeout`. Its results had already printed.

## Fix and regression spec

- `ui/src/components/SettingsPage.jsx`: `NOT_CONFIG` gains `access`.
- `ui/src/components/settings/SettingsCard.jsx`: `ReadOnlyFieldset` marks MUI Selects and deletable chips disabled and out of the tab order, and stops their keys.
- `ui/e2e/settings-config-modes-walkthrough.spec.js`, five tests:
  - **Access under an external repo, at 1440 and 390 px:** no banner and no read-only fieldset, Log out enabled. A bounding-box check puts Log out's box inside its card and the viewport. General still shows the banner.
  - **Locked Tasks → Default legacy capabilities, and locked General → Channel:** `aria-disabled`, `tabindex -1`, and ArrowDown, Enter or Space opens nothing and sends no write.
  - **An unlocked select still opens from the keyboard.**
  - With the fix stashed and the UI rebuilt, 4 of the 5 fail. The fifth is the unlocked control, which passes either way.
- **Ran locally:**
  - the new spec: 5/5;
  - `vitest` on `SettingsPage.test.jsx` and `components/settings/`: 142/142;
  - the neighbouring Playwright specs `config-repo-only-settings`, `settings-panel-walkthrough`, `responsive-navigation-walkthrough` and `config-repo-iac-modes`: 104/104, Chromium and WebKit;
  - `prettier --check` on the changed files.

## Not run

- **Eyeballing the PNGs:** the sandbox can't render them, so the DOM audit stands in for it (see §4). The contact sheets are attached to the task's closing note for a person to look at.
- **A run-host-backed daemon:** §11 needs no dispatch, so none was started.
- **Real agent backends:** none.
