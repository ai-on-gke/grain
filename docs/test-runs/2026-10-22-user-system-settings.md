# 2026-10-22: User settings, System settings configuration sections, and Memory walkthrough

Task 180 (`grain/049f1e09/task-180`): end-to-end browser walkthrough of user settings (`Settings → Preferences` and `Settings → Access`), every deployment configuration section in `Settings` (`/settings/<section>`), `System → Upgrade` (`/system/upgrade`), and `Settings → Memory` (`/settings/memory`) against a live `grain demo` server and built frontend.

## Environment & Commit Tested

- **Base commit**: `747f82455` (`candidate`: `docs(release): add release notes for v2.1.0`)
- **Branch**: `grain/049f1e09/task-180`
- **Server & Frontend**: `grain demo` over throwaway SQLite stores via `ui/e2e/fixtures.js`, serving the production bundle built with `make frontend` (Vite `v6.4.3`).
- **Browser & Toolchain**: Playwright `1.64.1` driving Chromium `151.0.7922.34` on Debian Bookworm (`Linux 6.1.0-43-amd64`), Go `1.26.9`, Node `v20.19.0`, Vitest `4.1.11`.
- **Viewports exercised**: Desktop (`1280×860` and `1280×720`), Tablet (`768×1024` and `720×844`), and Phone (`390×844`).
- **Gemini API key check**: `/home/debian/.gemini-api-key` is not present in this sandbox; Gemini model selection (`gemini-2.5-pro`), reasoning effort, and provider connection discovery were exercised through the real server and UI with the external Google Generative Language test-connection upstream stubbed where network calls leave the sandbox.

---

## Architecture Mapping (`design/design/settings-panel.design.md`)

Following `design/design/settings-panel.design.md` (SET-1..SET-3), the legacy System overlay (`/admin/<tab>`) and standalone `/settings` / `/memory` surfaces referenced in `.grain/templates/ui-walkthrough-settings.yml` are unified into:

1. **Settings (`/settings/<section>`)** — opened from the top-bar gear icon (`data-testid="top-bar-settings"`), with 16 sections across 4 groups:
   - **Deployment**: `General` (`/settings/general`), `Access` (`/settings/access`), `Config repo` (`/settings/config-repo`)
   - **Agents**: `Models` (`/settings/models`), `Agents` (`/settings/agents`), `Memory` (`/settings/memory`), `Tasks` (`/settings/tasks`), `Repositories` (`/settings/repositories`)
   - **Integrations**: `Git` (`/settings/git`), `Credentials` (`/settings/credentials`), `Executors` (`/settings/executors`)
   - **Infrastructure**: `Engine` (`/settings/engine`), `CI` (`/settings/ci`), `Sandboxes` (`/settings/sandboxes`), `Caches` (`/settings/caches`)
   - **This browser**: `Preferences` (`/settings/preferences`)
2. **System (`/system/<tab>`)** — opened from the top-bar System icon (`data-testid="top-bar-system"`) or the sidebar footer version stamp (`data-testid="sidebar-version"` → `/system/upgrade`), holding operational tabs: `Health` (`/system`), `Logs` (`/system/logs`), `Sandboxes` (`/system/sandboxes`), `Caches` (`/system/caches`), `Metrics` (`/system/metrics`), `Pipeline` (`/system/pipeline`), `Power` (`/system/power`), and `Upgrade` (`/system/upgrade`).
3. **Legacy URL Redirects (`ui/src/settingsSections.js`)**:
   - `/admin`, `/admin/general`, `/system/general`, `/settings` (desktop) → `/settings/general`
   - `/admin/<settings-tab>` and `/system/<settings-tab>` → corresponding `/settings/<section>` (`/admin/models` → `/settings/models`, `/admin/git` → `/settings/git`, `/admin/workflow` → `/settings/tasks`, `/admin/engine` → `/settings/engine`, `/admin/sandbox` → `/settings/sandboxes`, `/admin/integrations` → `/settings/credentials`, `/admin/state` → `/settings/config-repo`, `/admin/access` → `/settings/access`)
   - `/admin/upgrade` → `/system/upgrade`; `/admin/<ops-tab>` → `/system/<ops-tab>`
   - `/memory` → `/settings/memory`

---

## Walkthrough Coverage & Results

### 1. User Settings (`Settings → Preferences` at `/settings/preferences` and `Settings → Access` at `/settings/access`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| User tile removal & actor attribution | Confirmed `[data-testid="user-tile"]` is absent from the sidebar rail (`count === 0`). Verified in `user-tile-removal-walkthrough.spec.js` that old browser-local `grain.username` and per-user pins migrate cleanly to deployment-wide pins and tasks/comments use the server's actor. | Pass |
| Appearance (`Auto` / `Light` / `Dark`) | On `/settings/preferences`, selected `Light` (`html[data-theme="light"]`), then `Dark` (`html[data-theme="dark"]`), reloaded `/settings/preferences`, verified `Dark` remained checked and `data-theme="dark"` persisted in `localStorage`, then restored `Auto`. | Pass |
| Time zone (`userTimeZone`) | Selected `Africa/Cairo` in `select[name="userTimeZone"]`, verified helper text updated with UTC offset, reloaded `/settings/preferences`, and confirmed `Africa/Cairo` persisted. | Pass |
| Task timeline order | Toggled `Oldest first` (`value="oldest"`), reloaded `/settings/preferences`, verified `Oldest first` remained selected, then restored `Newest first`. | Pass |
| Customize pinned items | Clicked `Customize pinned items` on `/settings/preferences` (and verified the sidebar's `Customize` button), exercised pinning/unpinning and reordering items, reloaded, confirmed the sidebar rail reflected the updated pins, and closed via `Escape`. | Pass |
| Scope isolation | Confirmed no deployment-wide settings appear on `/settings/preferences` (only `Appearance`, `Time zone`, `Task timeline`, and `Sidebar`). | Pass |
| Version display & Sign out | Verified version display on `/settings/general` (`Version` card) and in the sidebar footer (`data-testid="sidebar-version"` → `/system/upgrade`). Verified `Signed in with the access code.` and `Log out` (`onLogout` → `POST /api/auth/logout`) on `/settings/access`. | Pass |

### 2. System & Settings Navigation, Redirects, Filter Jump, and Responsive Layouts

| Check | Steps & Observations | Outcome |
|---|---|---|
| Top-bar entry points | Clicked `data-testid="top-bar-settings"` (`aria-pressed="true"`, opens `/settings/general`) and `data-testid="top-bar-system"` (`aria-pressed="true"`, opens `/system` on `Health` tab). | Pass |
| Legacy `/admin/*`, `/system/*`, `/settings`, `/memory` redirects | Exercised all 36 legacy paths in `settings-panel-walkthrough.spec.js`: `/admin` and `/system/general` open `/settings/general` (not `Logs`); `/admin/workflow` → `/settings/tasks`; `/admin/integrations` → `/settings/credentials`; `/admin/state` → `/settings/config-repo`; `/admin/upgrade` → `/system/upgrade`; `/memory` → `/settings/memory`. Verified browser `Back` skips the redirected path and returns to the prior page. | Pass |
| Filter jump (`/` shortcut & `Filter settings`) | Typed `overcommit` in `Filter settings`, verified results (`CPU overcommit, Sandboxes`, `Memory overcommit, Sandboxes`), clicked `CPU overcommit, Sandboxes`, and verified navigation to `/settings/sandboxes#sandboxCpuOvercommit` with focus on the `CPU overcommit` input. Verified non-matching query (`zzqqxx`) shows `No setting matches 'zzqqxx'.` and `Escape` clears the filter. | Pass |
| Narrow / Tablet / Phone layouts | At `768×1024`, verified the left Settings nav collapses into the `Section` dropdown (`select`). At `720×844` on `/system/upgrade`, verified the `System tabs` `tablist` (`variant="scrollable"`) scrolls horizontally without overflowing the viewport. At `390×844` (phone), verified `/settings` shows the section list and opening a section renders under `data-testid="settings-back"`. | Pass |

### 3. General & Deployment (`/settings/general`) and Agents & Task Branches (`/settings/agents`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| General settings | Edited `Environment name` (`walkthrough`), tabbed out, verified `settings-status` transitioned to `All changes saved`, toggled `Show closed tasks by default`, reloaded, and confirmed both persisted. | Pass |
| Agents, models, effort & Gemini | Exercised `Max agent turns` (`40`), default model and reasoning effort, and `Standing instructions for every run` (`promptExtension`). Verified editing `Standing instructions` marks `settings-status` as `Unsaved changes in Standing instructions`, shows `settings-dirty-dot` on `Agents` in the nav, prompts on navigation away, reverts on `Discard`, and saves on `Save`. | Pass |
| Task branches & Commit identity | Verified deployment-wide task branch prefix (`taskBranchPrefix`), branch auto-delete, release branch pairing, and commit identity fields on `/settings/agents`. | Pass |
| Unrunnable-setting & restart-required notices | Verified `settingHints` unrunnable chips/hints when a setting depends on an unconfigured capability or flag, and restart-needed status propagation. | Pass |

### 4. Engine & Concurrency (`/settings/engine`) and CI (`/settings/ci`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Worker, merger & poll limits | Edited `Poll interval` (`45s` / `30s`), `Max worker agents` (`maxWorkers`), and `Max merges per repo branch` (`maxMergesPerRepo`); verified persistence across reload. | Pass |
| Merge batch size & gather window | Set `Merge batch size` (`mergeBatchSize`) to `3`, verified `Merge gather window` (`mergeGatherWindow`) and the batching/gathering explanatory notes (`MergeBatchingNote`, `MergeGatheringNote`), and verified blank-means-default placeholders. | Pass |
| Out-of-range / invalid input validation | Entered `not-a-duration` into `Poll interval` on `/settings/engine`, tabbed out, and verified `PUT /api/settings` returned `400` and `data-testid="settings-status"` displayed `Couldn't save Poll interval` with `Retry`; restored `30s` and confirmed `All changes saved`. | Pass |
| CI queue, history, job cache & artifacts (`/settings/ci`) | Edited `CI jobs per run` (`3`), inspected CI queue, CI history, CI job cache, and CI artifacts cards and their blank-means-default placeholders, and verified pointers to `System → Sandboxes` and `System → Caches`. | Pass |

### 5. Models & Connections (`/settings/models`, `/admin/models`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Provider connection lifecycle | Exercised in `deployment-connections-models-walkthrough.spec.js` and `provider-connection-lifecycle-walkthrough.spec.js`: verified empty state, added Anthropic and secondary connections, tested connections (`Test Connection`), edited credentials, toggled Enable/Disable, and removed throwaway connections. | Pass |
| Default model, effort & model pickers | Verified discovered/advertised models (including Gemini and Anthropic models) populate the default model selector on `/settings/models` and the model pickers in `New task`, schedules, and templates. | Pass |
| Warnings & resume | Verified warnings when the default model is only served by a disabled or paused connection or is unadvertised, and verified resuming a paused connection/model from `/settings/models`. | Pass |

### 6. Git Connections (`/settings/git`, `/admin/git`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Git identity & multi-host connections | Exercised in `git-connections-multihost-walkthrough.spec.js`: verified agent git name/email, default `github.com` row, added/tested/edited/removed secondary PAT and GitHub App connections, verified connection check indicators, write-only credential modes (`Keep existing` / `Remove`), and `RepoPicker` multi-host syntax in the `New Task` modal. | Pass |

### 7. Workflow & Tasks (`/settings/tasks`, `/admin/workflow`) and Repositories (`/settings/repositories`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Task defaults & per-task limits | Edited `Max unreviewed follow-up depth` (`4`), verified task default toggles (`Approve new tasks by default`, `Auto-merge pull requests by default`, `Ignore GitHub CI by default`, `Default capabilities`), and per-task limits; reloaded and confirmed persistence. | Pass |
| Custom task fields & stranded values | Verified custom task field declarations on `/settings/tasks` and the stranded-values count link (`1 task still carries a value this field no longer offers`), which narrows the task list to affected tasks when clicked. | Pass |
| Repositories index (`/settings/repositories`) | Verified the `Repositories` table (`Repository`, `Base branch`, `Declarations`, `CI mode`, `Git connection`) for `acme/widgets`, clicked `CI mode` → `/repos/acme/widgets/settings/ci` and `Declarations` → `/repos/acme/widgets/settings/advanced`, and verified `Back` returns to `/settings/repositories`. | Pass |

### 8. Sandboxes, Caches, Executors, and Credentials (`/settings/sandboxes`, `/settings/caches`, `/settings/executors`, `/settings/credentials`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Sandboxes (`/settings/sandboxes`) | Edited `CPU overcommit` (`1.5`), inspected `Memory overcommit`, sandbox resource defaults, network mode, setup script, and proxy settings; reloaded and confirmed persistence. | Pass |
| Caches (`/settings/caches`) | Enabled `packageCacheEnabled`, verified `Proxy cache max size (GiB)` and the caption pointing to `System → Caches` (after fixing Bug 4), and exercised `Actions cache max size (GiB)` (`7`). | Pass |
| Executors (`/settings/executors`) | Issued a join token (`walkthrough-token`) via `Issue Token` → `Generate Token`, reloaded `/settings/executors`, and confirmed the token remained listed. | Pass |
| Credentials & Secrets (`/settings/credentials`) | Edited `GCP project` (`walkthrough-project`) and confirmed persistence across reload. Verified the default `grain demo` state (`Not available: this UI was not started with a local secrets directory`) and exercised the `Other secrets` list (`throwaway-token` / `api-key`) with `/api/secrets` stubbed. | Pass |

### 9. Access (`/settings/access`), Config Repo (`/settings/config-repo`), and Upgrade (`/system/upgrade`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Access (`/settings/access`) | Inspected the `This browser` card (`Signed in with the access code.`) and relay/admin access notice in `grain demo`. | Pass |
| Config repo (`/settings/config-repo`) | Inspected the internal `grain/config` state repository status, working tree path, `Export now`, external Infrastructure-as-Code repository URL/Branch/Token fields, and `Secrets key` backup notice. Verified `externalConfigRepo` read-only banner across Settings sections and immediate reload when switching back to the internal config repository (Bug 3 fix). | Pass |
| Upgrade (`/system/upgrade`) | Opened `/system/upgrade`, inspected version and upgrade configuration notice (`neither -upgrade-image nor -upgrade-src-dir`), with `POST /api/system/upgrade` intercepted so no host upgrade could be triggered. | Pass |

### 10. Memory (`/settings/memory`, `/memory`)

| Check | Steps & Observations | Outcome |
|---|---|---|
| Scopes & documents | Opened `/settings/memory` (and verified `/memory` redirects to `/settings/memory`). Verified deployment-wide memory (`Memory carried by every run`), `acme/widgets` repository memory (`Memory about acme/widgets`), and `Documents` sections. | Pass |
| Reversible edit, save & reload | Edited `Memory about acme/widgets` to `Run make frontend before the e2e suite.`, clicked `Save acme/widgets memory`, verified `edited here` chip appeared, reloaded `/settings/memory`, and confirmed the updated note persisted. | Pass |
| Unsaved-edit dirty indicator & leave guards | Typed an unsaved change into `Memory carried by every run`, verified `data-testid="settings-status"` showed `Unsaved changes in Memory` and `data-testid="settings-dirty-dot"` appeared on `Memory` in the Settings nav (Bug 1 fix). Clicked another section in `settings-nav` (`Engine`) and the top-bar `System` button (`data-testid="top-bar-system"`), confirmed both prompted `window.confirm` before leaving and stayed on `Memory` when dismissed (Bug 1 & Bug 2 fixes). Clicked `Revert this deployment's memory` and confirmed `All changes saved`. | Pass |
| Location isolation | Confirmed Memory does not appear in `/system/*` operational tabs or on a repository's page (`/repos/acme/widgets`). | Pass |

---

## Bugs Found & Fixed

### Bug 1: `MemorySection` did not report unsaved edits to `SettingsPage`'s dirty state, and `SettingsPage` left `dirtySections` dots stuck after confirming navigation away
- **Reproduction**:
  1. Navigate to `/settings/memory` and type into `Memory carried by every run` (or any repository memory or document editor) without clicking `Save`.
  2. Observe `data-testid="settings-status"` in the Settings header still says `"All changes saved"` and no `data-testid="settings-dirty-dot"` appears on `Memory` in the Settings nav.
  3. Click another section in the Settings nav (`Engine`): `SettingsPage.go()` only checked `s.dirtyCards.length > 0` (ignoring `leavingLosesEdits()`), so it switched sections immediately and silently discarded the unsaved memory edit without a confirmation dialog.
  4. Furthermore, in `SettingsPage.jsx`, `dirtySections` accumulated `[current]: s.dirtyCards.length > 0` across sections; when confirming the discard dialog to leave a dirty section (e.g. `Agents`) for another section (`Engine`), `current` changed before `Agents` unmounted, leaving `dirtySections["agents"] === true` stuck in the nav. And in `SettingsPage.pick()`, cancelling the confirm dialog when picking a filter result from another section still cleared the filter and called `window.history.replaceState`.
- **Fix**:
  - Added `onDirtyChange` to `ui/src/components/MemoryPanel.jsx` and wired `MemorySection.jsx` (`ui/src/components/settings/MemorySection.jsx`) to call `s?.markDirty("Memory", dirty)` and clear it on unmount.
  - Passed `s={s}` to `<MemorySection>` in `ui/src/components/SettingsPage.jsx`, updated `dirtySections` to reflect only the mounted dirty section, checked `leavingLosesEdits()` in `SettingsPage.go()`, and early-returned in `SettingsPage.pick()` when `go(entry.section)` is cancelled.
- **Regression coverage**:
  - Unit tests in `ui/src/components/SettingsPage.test.jsx` (`marks an edited Memory note dirty in the header and nav, asks before switching sections, and clears the dot after confirming` and `keeps the filter open when cancelling a jump to another section from a dirty card`).
  - Playwright e2e coverage in `ui/e2e/settings-panel-walkthrough.spec.js`.

### Bug 2: Top-bar `System` and `Settings` navigation in `App.jsx` bypassed `leavingLosesEdits()`
- **Reproduction**:
  1. Navigate to `/settings/agents` or `/settings/memory` and make an unsaved edit.
  2. Click the top-bar `System` button (`data-testid="top-bar-system"`) or top-bar `Settings` gear (`data-testid="top-bar-settings"`, which navigates to `/settings/general`).
  3. `openSystem` and `openSettings` in `ui/src/App.jsx` navigated immediately without checking `leavingLosesEdits()`, silently discarding the unsaved edits.
- **Fix**:
  - Guarded `openSystem` and `openSettings` in `ui/src/App.jsx` with `leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)` whenever switching views or switching to a different Settings section.
- **Regression coverage**:
  - Unit test in `ui/src/App.overlays.test.jsx` (`asks before leaving an unsaved Settings edit via the top-bar System or Settings buttons`).
  - Playwright e2e coverage in `ui/e2e/settings-panel-walkthrough.spec.js`.

### Bug 3: `ConfigRepoSection` dropped `onStatusChange` and `SettingsPage` kept stale `config.externalConfigRepo`
- **Reproduction**:
  1. When `externalConfigRepo` is active and an operator switches back to the internal config repository in `/settings/config-repo`, `ConfigRepoSection` did not pass `onStatusChange` to `StateRepoPanel`, and `SettingsPage` computed `settings?.externalConfigRepo || config?.externalConfigRepo || ""`, falling back to the stale `config.externalConfigRepo` even after `settings.externalConfigRepo` became `""`.
- **Fix**:
  - Forwarded `onStatusChange` in `ui/src/components/settings/ConfigRepoSection.jsx` to `StateRepoPanel`, wired `onStatusChange` in `ui/src/components/SettingsPage.jsx` to call `reloadSettings()` and `onSaved?.()`, and updated `externalConfigRepo` to prefer `settings.externalConfigRepo` once `settings` has loaded.
- **Regression coverage**:
  - Unit test in `ui/src/components/SettingsPage.test.jsx` (`reloads settings and clears the externalConfigRepo read-only state when Config repo disconnects`).

### Bug 4: Stale settings section / tab names in `CachesSection.jsx` and `TasksSection.jsx`
- **Reproduction**:
  1. On `/settings/caches` with proxy cache enabled, the helper caption referred to `"on the Caches page in the sidebar"` (moved to `System → Caches` in SET-1).
  2. On `/settings/tasks` with no capabilities configured, the `Default capabilities` empty menu item referred to `"the rows under Integrations & Auth"` (now `Settings → Credentials`).
- **Fix**:
  - Updated `ui/src/components/settings/CachesSection.jsx` to say `"on System → Caches."` and `ui/src/components/settings/TasksSection.jsx` to say `"the rows under Settings → Credentials"`.
- **Regression coverage**:
  - Unit test in `ui/src/components/settingsReferences.test.jsx` (`on the Caches and Tasks settings sections name tabs that exist`).

---

## Stubbed External / Demo-Unavailable Boundaries

All walkthrough steps ran against the real `grain demo` Go server and built React frontend in Chromium. Only boundaries unavailable in `grain demo` or unsafe to trigger on the host were intercepted via Playwright `page.route`:

| Boundary / Route | Why Stubbed | Real Components Retained |
|---|---|---|
| `POST /api/system/upgrade` | Safety guard required by walkthrough protocol (`Never actually upgrade; intercept only that boundary`); `GET /api/system/upgrade` ran unstubbed against `grain demo`. | Real `SystemPage`, `UpgradePanel`, and `GET /api/system/upgrade` handler. |
| `GET /api/secrets` (in secret-listing check) | `grain demo` runs without `-server-data-dir` colocation (`enabled: false` by default). Verified the unstubbed default state first, then stubbed `GET /api/secrets` (`enabled: true`) to exercise the `SecretsPanel` listing on `/settings/credentials`. | Real `CredentialsSection` and `SecretsPanel` UI. |
| Upstream provider / Git host test endpoints (in connection lifecycle specs) | `provider-connection-lifecycle-walkthrough.spec.js` and `git-connections-multihost-walkthrough.spec.js` use local loopback test servers / route stubs for external provider (`api.anthropic.com`) and enterprise git (`ghe.example.com`) reachability inside the sandbox. | Real `grain` server `/api/connections` and `/api/git-connections` CRUD and test handlers. |
