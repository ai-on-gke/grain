# User & System Settings Walkthrough — 2026-11-09

## Environment & Commit Tested

- **Base commit**: `8ea343b0` (`main` / `grain/098073e4/task-3718-walk-through-settings-its`)
- **Runtime**: Linux (`go1.26.10 linux/amd64`, Node.js `v20.19.0`, Vite `v6.4.3`)
- **Browser**: Headless Playwright Chromium (`/var/cache/playwright`) driving the built production bundle (`pkg/ui/static/`) embedded into `/var/tmp/grain-e2e` (`go build -tags grain_e2e`)
- **Servers exercised**:
  1. `grain demo -addr 127.0.0.1:0 -open=false -github-sim -fake-ci -secrets-store -access-code-file <tmp>` (plus separate throwaway `grain demo -agent-paused` instance for connection-wide pause/resume)
  2. `grain daemon -data-dir <tmp>/data -sandbox-dir <tmp>/sandboxes -ui-addr 127.0.0.1:0 -sandbox-port-base=0 -as alice -poll-interval 200ms -max-workers 1 -max-mergers 1 -agy-path <fake-agy> -gemini-api-key-file <tmp>/gemini-key -github-host 127.0.0.1:9 -github-insecure-http` (exercising real controller-local `grain/config` export, Config Repo Browser, `Settings lock`, and held `grain/config` pull requests)
- **Visual inspection note**: `/home/debian/.gemini-api-key` is not present in this sandbox (`LOOK=1` unavailable), so PNG pixels could not be inspected through Gemini vision; DOM layout bounding boxes, viewport containment, scroll/highlight state, and zero horizontal overflow were verified programmatically across desktop (`1440×900`), tablet (`820×1180` / `800×1000`), and phone (`390×844`) in light and dark themes, and `before`/`after` contact sheets were captured via `ui/e2e/screenshot.mjs`.

## Boundary Stubs & Intercepted Routes

Only external boundaries unavailable in throwaway `grain demo` / offline `grain daemon` sandboxes or transient failure simulations were intercepted via Playwright `page.route`; all UI controls, state transitions, validation, and server persistence ran against the real Go HTTP handlers and embedded SQLite stores:

| Intercepted Route | Purpose & Scope |
| --- | --- |
| `PUT /api/settings` (600 ms delayed pass-through, then single `503` failure) | Observed transient `Saving…` header line in real time, then triggered `Couldn't save Max agent turns` with `Retry` recovery against the real server. |
| `GET /api/settings` (`pendingRestart: ["pollInterval"]`) | Exercised the `Restart needed` warning chip in the section header and verified clicking it navigates to `/system/power` (` grain demo` hot-applies settings without setting `pendingRestart`). |
| `GET /api/config` & `GET /api/whoami` (IAP viewer simulation) + `GET /_gcp_iap/clear_login_cookie` | Simulated an Identity-Aware Proxy signed-in viewer (`viewer@example.com`, `signInMethod: "iap"`, `logoutUrl: "/_gcp_iap/clear_login_cookie"`) because the sandbox has no external IAP proxy; verified header email badge, `This browser` identity readout, `access-read-only` notice, and IAP logout redirect. |
| `POST /api/agent-models` | Returned live model probe payload (`ok: true`, `models: ["claude-sonnet-4-6", "claude-opus-4-7"]`) during `Settings → Models → Test Connection` because the sandbox has no outbound Anthropic/Gemini internet access. |
| `GET /api/config` (`agentPause` single-model pause) & `DELETE /api/agent-pause` | Simulated a rate-limited single-model pause (`claude-sonnet-4-6`) alongside the real `-agent-paused` flag used for connection-wide pause/resume. |
| `GET /api/state-repo` (`resetNotices`) & `POST /api/state-repo/reset-notice` (pass-through) | Injected a post-merge schema-default `resetNotices` entry on `grain daemon` and confirmed `Dismiss` calls the real `POST /api/state-repo/reset-notice` endpoint and clears the alert. |
| `GET /api/config` (`stateRepoSyncFailure`) | Simulated a remote sync failure on `grain daemon` to verify the header alert badge and `/inbox` `grain/config` failure row navigating back to `/settings/config-repo`. |
| `GET /api/config` & `GET /api/settings` (`externalConfigRepo: "https://github.com/acme/grain-config"`, `configRepoOnly: true`) | Simulated external Infrastructure-as-Code mode without outbound GitHub network access to verify `external-config-repo-banner` on managed sections while `/settings/access` stays unlocked. |
| `GET /api/config` & `GET /api/settings` (`configRepoOnly: false` stale-tab simulation against locked `grain daemon`) | Simulated a browser tab opened before `settingsLocked` was enabled so submitting `Environment name` hit the real `grain daemon` `409 config_managed_externally` handler, opening `ConfigRefusalDialog` and filing a real held `awaiting_submit` PR task via `POST /api/state-repo/pull-requests`. |

---

## Walkthrough Steps & Outcomes

### 1. Layout, Entry Points, Responsive Modes, and Legacy Redirects

- **Header entry**: Verified Settings has no link in the left navigation rail (`railSettingsLinks = 0`) and opens exclusively from the gear button (`data-testid="top-bar-settings"`) at the right of the header rail on desktop and phone.
- **Bare `/settings`**:
  - On desktop (`1440×900`), bare `/settings` normalizes via `replaceState` to `/settings/models` (`h1 = Models`) alongside the 5-group left nav: `Models & sandboxes`, `Secrets & access`, `This browser`, `System / Operations`, and `Managed in config repo`.
  - On phone (`390×844`), bare `/settings` renders the 5-group section list alone (`settings-section` absent); tapping a section opens `/settings/<section>` under the `All settings` back arrow (`data-testid="settings-back"`), which returns to `/settings`.
- **Tablet width (`800×1000` / `820×1180`)**: Left section nav collapses and renders the searchable `Section` combobox (`data-testid="settings-section-select"`), switching sections cleanly and keeping the current section when a dirty-card leave confirmation is declined.
- **URL persistence & `#<card>` scrolling**: Navigated across sections, reloaded, stepped through Browser Back/Forward, and deep-linked to `/settings/agents#commit-identity` at `1280×600`, confirming `#commit-identity` scrolled into view (`y = 437.7 px`) and received `.setting-highlight`.
- **Legacy redirects**: Verified every legacy URL lands on its target section without leaving extra history entries:
  - `/admin` → `/settings/general` (`General`)
  - `/admin/workflow` → `/settings/tasks` (`Tasks`)
  - `/admin/executors`, `/system/executors`, `/settings/hosts`, `/settings/hostds` → `/settings/hostds` (`Hosts`)
  - `/admin/upgrade`, `/system/upgrade` → `/settings/upgrade` (`Upgrade`)
  - `/memory` → `/settings/memory` (`Memory`)
  - `/settings/credentials`, `/settings/legacy-capabilities` → `/settings/capabilities#credentials` (`Capabilities`)

### 2. Filter Settings

- Pressing `/` outside form inputs focused `Filter settings` (`aria-label="Filter settings"`).
- Typing a non-matching query (`zzzznonexistent`) rendered `No setting matches 'zzzznonexistent'.`; pressing `Escape` cleared the query and restored all 5 section groups.
- Typing a helper keyword (`house style`) listed `Standing instructions for every run, Agents` grouped under `Agents`; selecting it navigated to `/settings/agents#promptExtension`, scrolled to and focused `textarea[name="promptExtension"]`, and outlined the control with `.setting-highlight`.
- Exercising Browser Back after cross-section filter navigation, filtering for conditionally hidden fields (`Automatic upgrade time of day` while `Automatic upgrades` is `Off`), and filtering/clicking nav items from `/settings/capabilities/<server>` and `/settings/config-repo/<file>` uncovered **Bugs 1, 2, and 3** (fixed and verified below).

### 3. Save and Lock Model

- **Immediate vs. explicit card saves**:
  - Checkboxes (`Show closed tasks by default`), selects (`Check the upgrade`, `Automatic upgrades`), and text/number inputs (`Environment name` on `Enter`, `Poll interval` on blur) committed immediately via `PUT /api/settings`, updating `document.title` (`staging-walk — grain`) and surviving reloads.
  - Multi-line and composite cards (`Standing instructions`, `Memory`, `Task fields`) waited for their own `Save` / `Discard` buttons.
- **Header status, dirty dot, and leave guards**:
  - Confirmed `All changes saved` at rest, `Saving…` while `PUT /api/settings` is in flight, `Unsaved changes in Standing instructions` alongside `data-testid="settings-dirty-dot"` in the nav when dirty, `window.confirm("You have unsaved changes here. Leave and discard them?")` on section/Settings leave and `beforeunload` (`defaultPrevented === true`) when dirty, and zero prompts when leaving clean sections.
- **Server normalization, error & Retry, local range validation, and Restart needed**:
  - Entering `"1m"` in `Poll interval` saved and read back the server's normalized `"1m0s"`.
  - A simulated `503` on `Max agent turns` rolled back the field, displayed `Couldn't save Max agent turns`, and recovered to `All changes saved` (`25`) on clicking `Retry`.
  - Entering `0` in `Max unreviewed follow-up depth` (`min = 1`) refused locally before sending any `PUT` (`putsSent = 0`), naming the field by label (`Max unreviewed follow-up depth must be at least 1`) in the error banner and `Couldn't save Max unreviewed follow-up depth` in the header; restoring the saved value cleared both the header status and the error banner.
  - With `pendingRestart: ["pollInterval"]`, `Restart needed` (`data-testid="settings-restart-chip"`) appeared in the header and navigated to `/system/power`.
- **Settings lock & Config Repo modes**:
  - Verified `Lock settings in UI (require config repo PRs)` is disabled on `grain demo` (no `configRepo`) and enabled on `grain daemon` (`grain/config`).
  - Toggling `Settings lock` on `grain daemon` immediately rendered `Managed in config repo` sections (`General`, `Agents`, `Tasks`, `Memory`, `Capabilities`, etc.) plus `Caches` and `Engine`'s config-managed fields (`Poll interval`, `Max merges per repo branch`, while keeping bootstrap `Max worker agents` editable) disabled inside `ReadOnlyFieldset` with `"View in config repo → <path>"` links into the Config Repo Browser (`memory/deployment.md` opened `/settings/config-repo/memory/deployment.md`).
  - Audited every `fieldset[disabled]` across `general`, `agents`, `tasks`, `memory`, `engine`, `caches`, and `capabilities`: zero focusable selects or deletable chips remained in the keyboard tab order (`focusLeaks = 0`, `tabindex="-1"`, `aria-disabled="true"`).
  - Submitting a setting against a locked `grain daemon` returned `409 config_managed_externally`, opened `ConfigRefusalDialog` (`Couldn't save Environment name` with no `Retry`), and clicking `Create Config PR` filed a held `grain/config` task (`Change tables/grain_config.json`, `state: "awaiting_submit"`). Subsequent succeeding save cleared the refusal status back to `All changes saved`.

### 4. Preferences (`/settings/preferences`)

- **Appearance**: Toggled `Dark`, `Light`, and `Auto`; verified computed background (`rgb(15, 16, 19)` dark vs. `rgb(247, 246, 243)` light) and persistence across reload.
- **Time zone**: Verified default `Browser default (America/Los_Angeles)`, switched to `Asia/Tokyo`, reloaded, and confirmed timestamps across the app (including `Settings → Access` principal `Added` timestamps) formatted in `JST` / `Asia/Tokyo`.
- **Task timeline order**: Flipped `Newest first` / `Oldest first` and confirmed persistence across reload.
- **Customize pinned items**: Opened `Customize sidebar`, pinned a Settings section (`Memory`), a System tab (`Power`), a board (`Board`), and a session (`Director`), reordered items, saved, reloaded, verified the left rail followed the custom order, then unpinned/reset to default. Confirmed zero deployment-wide API mutations (`PUT /api/settings`, `PUT /api/deployment`, etc.) were sent during any Preferences interaction.

### 5. Access (`/settings/access`)

- Confirmed `/settings/access` stays usable (no `external-config-repo-banner`, no `data-settings-readonly`, `Log out` enabled) when settings are locked or backed by an external config repo.
- Verified `This browser` card matches the signed-in method: access-code description with `Log out` returning to the access-code gate (`POST /api/auth/logout`), and under simulated IAP identity (`viewer@example.com`), matching the header email badge, rendering `access-read-only` for non-admin roles, and routing `Log out` to `/_gcp_iap/clear_login_cookie`.
- Exercised `Allowed users and groups` validation (prefix requirement, service-account key hint, mandatory reason for dangerous roles like `grain/operator`), adding and removing a principal cleanly.

### 6. General (`/settings/general`)

- Verified `Deployment` card (`Environment name`, `Show closed tasks by default`), `Settings lock` (disabled without config repo, functional on `grain daemon`), `Updates` (`Check the upgrade` mode options, `Upgrade check pauses the pipeline`, `Automatic upgrades`, conditional `Automatic upgrade channel` and `Automatic upgrade time of day` persisting `04:15`), and `About` version matching the build stamp in the rail footer.

### 7. Config Repo (`/settings/config-repo`)

- On `grain daemon`, verified the controller-local `grain/config` panel, `working tree:` path, commit chip, `Export now`, `Use external config repository` controls, and `Secrets key` / encrypted secrets backup controls.
- Opened the read-only Config Repo Browser via `/settings/config-repo/tables/grain_config.json` (verifying `showing <sha>` commit stamp and formatted JSON preview) and `/config?path=README.md` (normalizing to `/settings/config-repo/README.md` with rendered Markdown preview).
- Confirmed post-merge `resetNotices` alert renders replaced column details and `Dismiss` clears it via `POST /api/state-repo/reset-notice`.
- Confirmed `stateRepoSyncFailure` surfaces both the header alert indicator and the `/inbox` `grain/config` row linking back to `/settings/config-repo`.

### 8. Models (`/settings/models`)

- Exercised `New connection` (including confirming an unfinished new connection is cleaned up automatically on Escape/reload without prompting `Remove the connection`), `Test Connection` (via stubbed `POST /api/agent-models`), `Edit`, `Disable` / `Enable`, and `Remove` (which asks for confirmation first and preserves the connection when declined).
- Verified empty-connections warning (`This deployment has no provider connection, so its tasks wait`), all-connections-disabled warning (`Every provider connection is disabled`), default-model disabled/paused warnings, and unadvertised orphan default-model warning.
- Confirmed connections advertising discovered models (`models: []`, `discovered.models: [...]`) populate model pickers across `New task` (`/new`), `New session`, `New schedule`, and `New template`.
- Verified per-model reasoning effort saves via `PUT /api/deployment` and persists across reload, `Where each agent CLI runs` toggles container vs. host with readiness reporting, and both connection-wide pause (`-agent-paused`) and single-model pause (`agentPause`) resume cleanly from `Settings → Models`.

### 9. Agents (`/settings/agents`)

- Verified `Agent runs` (`Max agent turns`), `Standing instructions` (`Save` and `Discard`), `Task branches` (`Branch name template` git-ref validation and `Deployment ID` validation keeping the 8-hex sample `deploymentIdDefault` visible when a bad edit is refused), and `Commit identity` (`Settings → Git` SPA link respecting unsaved-edit confirmation).

### 10. Memory (`/settings/memory`)

- Verified deployment-wide notes, per-repository notes (`acme/widgets`), and memory documents render cleanly; saved a reversible edit to a note and read it back after reload, then restored the original text.
- Verified unsaved edits trigger confirmation across all four leave paths: switching Settings sections in the nav, clicking a main rail item, pressing Browser Back (`popstate`), and reloading (`beforeunload`).
- Confirmed Memory does not appear in the left rail unless pinned via `Preferences → Customize sidebar`, and has no tab on `/repos/acme/widgets`.

### 11. Tasks (`/settings/tasks`)

- Note on retired legacy capabilities: `Settings → Legacy capabilities` and the empty `Default legacy capabilities` checklist were retired in `#929` / `25b1d0df`; `Tasks` now presents `Default grants` via `GrantsPicker` (preserving any stored bare legacy IDs in `defaultCapabilities` on save).
- Verified `Queue new tasks immediately by default`, `Auto-merge new tasks by default`, and `Default grants` (`api:*` and capability-server method grants) save, persist across reload, and pre-populate `/new` (`Queue immediately`, `Auto-merge once checks pass`, and default grant chips).
- Verified `Backlog & follow-up limits` (`Work through the backlog newest-first`, `Max unreviewed follow-up depth`, `Max unreviewed follow-up per task`) and `Task fields`, including clicking the `"still carries a value this field no longer offers"` link on a select custom field with stranded values and landing on the `custom.<name>:legacy` filtered task list.

---

## Bugs Found & Fixed

### Bug 1: Cross-section Settings filter selection overwrote browser history (`replaceState`) instead of pushing (`pushState`)

- **Reproduction**:
  1. Start at `/`, click the Settings gear (`top-bar-settings`) to open `/settings/models` (or on a `390×844` phone, tap Settings to open `/settings`).
  2. Press `/`, type `"house style"`, and click `"Standing instructions for every run, Agents"` → URL becomes `/settings/agents#promptExtension`.
  3. Press Browser **Back**.
  4. **Actual (before fix)**: Browser jumped out of Settings back to `/` instead of `/settings/models` (or `/settings` on phone).
- **Root cause**: `SettingsPage.pick()` called `go(entry.section)` without the target anchor and immediately executed `window.history.replaceState(..., "/settings/${entry.section}#...")` synchronously before `App.jsx`'s URL sync `useEffect` ran. When `App.jsx` re-rendered, `window.location.pathname` already matched `/settings/agents`, causing `App.jsx` (`samePage === true`) to call `replaceState` instead of `pushState` and overwrite `/settings/models` in history.
- **Fix**: Updated `pick()` in `ui/src/components/SettingsPage.jsx` to pass `target` (`entry.card ? entry.anchor : entry.id`) directly to `go(entry.section, target)` (which passes `nextAnchor` to `App.jsx`'s `onSection`, letting `App.jsx` push `/settings/<section>#<target>` onto browser history) and only call `window.history.replaceState` when updating `#<target>` within the already-open section (`entry.section === current && !item`). Also updated `UpgradeSection`'s `onOpenUpdates` callback to `go("general", "updates")`.
- **Regression coverage**: Unit test in `ui/src/components/SettingsPage.test.jsx` and Playwright e2e test `"cross-section filter navigation pushes browser history on desktop and phone"` in `ui/e2e/user-system-settings-walkthrough.spec.js`.

### Bug 2: Filtering for a conditionally hidden setting neither scrolled to nor highlighted its parent card

- **Reproduction**:
  1. At `1280×500` with default `Automatic upgrades: Off`, open `/settings/models`.
  2. Press `/`, type `"Automatic upgrade time of day"`, and click `"Automatic upgrade time of day, General"`.
  3. **Actual (before fix)**: Navigated to `/settings/general#autoUpgradeTime`, but because `[data-setting-id="autoUpgradeTime"]` is not rendered while `autoUpgradeOn` is `"off"`, `reveal()` matched nothing—leaving the `#updates` card below the fold (`y = 552 px`) with nothing focused and no `.setting-highlight`.
- **Root cause**: `reveal()` in `ui/src/components/SettingsPage.jsx` only queried `[data-setting-id="${focusTarget}"]` and `document.getElementById(focusTarget)`, ignoring the field's parent card `anchor` in `SETTINGS_INDEX`.
- **Fix**: In `SettingsPage.jsx`'s `reveal()`, when neither `[data-setting-id="${focusTarget}"]` nor `document.getElementById(focusTarget)` is present, fall back to `document.getElementById(cardAnchor)` for the matching field entry in `SETTINGS_INDEX` (`e.id === focusTarget && e.section === current && !e.card`), scrolling, focusing, and highlighting the parent card (`#updates`, `#sandbox-budget`, etc.).
- **Regression coverage**: Unit test in `ui/src/components/SettingsPage.test.jsx` and Playwright e2e test `"filtering for a conditionally hidden setting scrolls and highlights its parent card"` in `ui/e2e/user-system-settings-walkthrough.spec.js`.

### Bug 3: Clicking the active section in Settings nav or picking a same-section filter match failed to exit an open `item` subpage

- **Reproduction**:
  1. Open `/settings/capabilities` and click `https://capabilities.demo.grain.example/cloud` (opening `/settings/capabilities/https%3A%2F%2Fcapabilities.demo.grain.example%2Fcloud`), or on `grain daemon` open `/settings/config-repo/tables/grain_config.json`.
  2. Click `Capabilities` (or `Config repo`) in the left Settings nav, or press `/` and pick `"GitHub host, Capabilities"`.
  3. **Actual (before fix)**: Clicking the active section in the left nav did nothing at all (`URL` stayed on the subpage), and picking a same-section filter match rewrote the URL bar via `replaceState` while leaving `App`'s `settingsItem` open on the subpage.
- **Root cause**: `go(next, nextAnchor)` in `ui/src/components/SettingsPage.jsx` guarded `onSection` with `if (next !== current)`, ignoring `Boolean(item)`.
- **Fix**: Changed the condition in `go()` to `if (next !== current || Boolean(item))` so navigating to the section root or a field/card in the same section while viewing an `item` subpage runs the unsaved-edits check and calls `onSection` (which clears `settingsItem` and pushes the new URL into browser history).
- **Regression coverage**: Unit test in `ui/src/components/SettingsPage.test.jsx` and Playwright e2e test `"clicking the active section nav or a same-section filter match exits an open capability server subpage"` in `ui/e2e/user-system-settings-walkthrough.spec.js`.
