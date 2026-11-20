# User & system settings walkthrough — 2026-11-15 (task 5890)

## Environment

- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-5890-walk-through-settings-its`, base `main`.
- **Commit tested**: `5a9bce62c` (`main` at the start of the run; build stamp
  `v3.9.0-dev.104+5a9bce6`). Every fix below was re-verified on `/var/tmp/grain-fixed`
  rebuilt over the updated `ui-ng/` bundle.
- **Scope**: Settings layout, filter, save/role/config-repo lock model,
  Preferences, Access, CLI, Audit, General, Config repo, Models, Agents,
  Memory and Tasks. Infrastructure sections (`Git`, `Hosts`, `Capabilities`,
  `Hub`, `Engine`, `CI`, `Sandboxes`, `Caches`), operations sections
  (`System`, `Upgrade`, `Metrics`), and `Repositories` were touched only where
  legacy redirects, config-repo locks, or cross-surface read-backs required
  them.
- **Build**: `npm ci && npm run build` in `ui/`, `ui-ng/` and `ui-hub/`, then
  `go build -tags grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Version=v3.9.0-dev.104+5a9bce6" ./cmd/grain`.
- **Browser**: Playwright (`ui-ng/package-lock.json`) headless Chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, default `timezoneId: UTC`.
  Throwaway scripts in `/var/tmp/walk-5890/` drove visible controls (rail
  links, section items, switches, pill menus, text inputs, buttons, confirm
  dialogs, browser Back/Forward, reload, `/`, `Escape`) and recorded uncaught
  `pageerror` events, failed requests, and `/api/` mutations. Checked at
  `2048×1080`, `1440×900`, `820×1180` (tablet) and `390×844` (phone) in light
  and dark modes.
- **Gemini / screenshots**: this sandbox has no `/home/debian/.gemini-api-key`,
  so `LOOK=1` was unavailable and PNG contact sheets could not be visually
  inspected inside the sandbox. Layout and contrast were checked via DOM
  bounding boxes, `scrollWidth === clientWidth`, computed styles, PNG byte
  comparison between light and dark captures, and `axe-core` WCAG 2.1 AA scans
  alongside the `ui/e2e/screenshot.mjs` before/after `contact-sheet.png`
  bundles attached to the task.
- **Servers**: real `grain demo` processes over isolated throwaway stores:

| Demo | Port | Build | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 8420 / 8460 | base / fixed | `-github-sim -fake-ci -git-proxy -secrets-store -host-executor hostd -model-catalog <loopback>` | primary walkthrough, before/after verification, screenshots |
| B | 8421 / 8461 | base / fixed | `-github-sim -fake-ci -secrets-store -external-config-repo https://github.com/acme/grain-config` | external config-repo read-only & refusal flows |
| C | 8422 / 8462 | base / fixed | `-github-sim -fake-ci -secrets-store -config-change` | internal `grain/config` repo, Settings lock, Config Repo Browser & held PRs |
| D | 8423 / 8463 | base / fixed | `-github-sim -agent-paused` | paused model connection banner, timezone pause time & `Resume` |
| E | 8424 / 8464 | base / fixed | `-github-sim -disable-self-upgrade -unconfigured` | `-disable-self-upgrade` omission & empty model-connections state |
| F | 8425 | base | `-github-sim -ui-iap-audience test-aud -ui-iap-keys-url <loopback JWKS>` | verified-identity `Access Denied` card naming exact principal to add |

## Stubs and external-boundary helpers

Every request hit the real `grain demo` server and real compiled Angular /
React bundles except at external boundaries or states the demo cannot produce
directly:

| Boundary / route | Why |
| --- | --- |
| `-github-sim` & `-fake-ci` | In-process bare git repo and simulated CI instead of outbound GitHub traffic. |
| Loopback `-model-catalog` HTTP server (`GET /v1beta/models`, `GET /v1/models`) | Served deterministic Gemini (`gemini-2.5-pro`, `gemini-2.5-flash`) and Anthropic (`claude-sonnet-4-5`, `claude-haiku-4-5`) catalogs without calling external provider APIs. |
| Loopback ES256 JWKS server (`docs/test-runs/2026-11-02-verified-identity/iapstub.go` pattern) | Signed `X-Goog-IAP-JWT-Assertion` headers for `stranger@example.com` so the real IAP verifier produced the `403 Access Denied` screen. |
| `POST /api/settings` → one `503`, then pass-through | Exercised `Couldn't save <field>` and `Retry`. |
| `GET /api/settings` + `pendingRestart: ["pollInterval"]` | Exercised the `Restart needed` header chip (`grain demo` hot-applies settings). |
| `GET /api/config` + `access: { role: "grain/viewer", scopes: ["tasks:read", "settings:read"] }` | Exercised read-only `RoleChip` explanations (access-code login grants operator scopes). |
| `GET /api/notifications` + `state-repo-sync` notification | Exercised the failing config-repo sync Inbox item and link back to `/next/settings/config-repo`. |
| `GET /api/deployment/credential-audit` → `500`, `GET /api/deployment/secret-audit` → `[]` | Exercised Audit's independent per-log error banner and empty state while config changes still rendered. |
| `GET /api/custom-fields` dropping `high` from `priority` options (classic check) | Exercised the stranded legacy custom-field count link (`/?f=custom.priority:legacy:`). |

## Which UI each step ran against

All 20 Settings sections exist under `/next/settings/<section>` (`ui-ng/`), so
every primary step ran against `/next/` first. Sub-features not yet ported to
`/next/` were verified on classic `/` (`ui/`) and noted below:

| Area / step | UI walked | Notes on `/next/` vs classic `/` |
| --- | --- | --- |
| Entry from rail/phone bar, desktop default `Models`, phone grouped list & `← All settings`, reload & Back/Forward | `/next/` | `/next/` rail includes `Settings` directly (`nav-settings`) and keeps the 280 px drawer down to 600 px; tablet searchable `Section` `<select>` at 820 px is classic-only (verified on `/settings`). |
| `#<card>` deep links (`#usage`, `#external-agents`, `#commit-identity`, `#credentials`, `#other-secrets`, `#card-qa-rollout`) & legacy `/admin/*`, `/system/*`, `/memory` redirects | `/next/` | Fixed on `/next/` (Bugs 1 & 2). |
| `/` filter focus, section keyword matches, `"No setting matches"`, `Escape` restore | `/next/` & classic `/` | `/next/` filters the 20 drawer sections (fixed `/` shortcut, `Escape`, and keywords in Bug 1); field-level search index + `.setting-highlight` outline + subpage collapse is classic `/` (`/settings`). |
| Save bar, dirty dot, `Discard unsaved changes?` guard on section/rail/Back/reload, `All changes saved` omission on stateless sections, `Restart needed` chip, server duration read-back (`90s` → `1m30s`), range validation, role read-only banner, Settings lock & external config repo lock + held config PR | `/next/` | Fixed dirty guard, stateless header omission, `pendingRestart` chip, and false `settingsLocked` lock on no-config-repo deployments on `/next/` (Bugs 2 & 3). |
| Preferences: appearance (`System`/`Light`/`Dark`), timezone (`Asia/Tokyo`), Customize rail visibility/order & cross-tab `storage` sync, zero server writes | `/next/` & classic `/` | Added timezone selector, `Customize…` button, and cross-tab sync on `/next/` (Bug 4); pinning individual boards, sessions, Settings sections & System tabs and task-timeline order remain classic `/settings/preferences`. |
| Access: `This browser` (`Your role`, `/api/whoami` summary, `Log out` + deep-link return), IAP `Access Denied` principal hint, principal validation (`user:`, `group:`, `serviceAccount:` `*.gserviceaccount.com`), reason enforcement, role updates, self/last-entry removal confirmation | `/next/` & classic `/` | Added `This browser` card, fixed invalid `"grain/developer"` default role, added removal confirmation dialog & deep-link preserve on `/next/` (Bug 5); separate `People & groups` vs `Service accounts` tables remain classic `/settings/access`. |
| CLI (`Install`, `Sign in`, `Accept external agents` warning + link, 4 `Agent skills` rows, `Copy` buttons) & Audit (`Configuration changes`, `Credential access`, `Secret resolutions`, phone stacked layout, independent error/empty states) | `/next/` | Fixed `/next/settings/cli` workstation commands/skills and `/next/settings/audit` independent error reporting + phone stacked table (Bug 6). |
| General: `Environment name`, `Show closed tasks by default`, `Settings lock` disabled without config repo, `About` version matching rail build stamp, `-disable-self-upgrade` | `/next/` & classic `/` | Fixed `Settings lock` gating, `Show closed tasks by default`, and `About` card on `/next/` (Bug 3); auto-upgrade channel/schedule lives on `/next/settings/upgrade` and classic `/settings/general`. |
| Config repo: status panel, `Export now`, Config Repo Browser (`/next/settings/config-repo/<file...>` & `/next/config?path=<file>`), failing-sync Inbox row | `/next/` & classic `/` | Fixed multi-segment `/next/settings/config-repo/<file...>` route on `/next/` (Bug 1); inline reset dot and mirror/secrets-backup controls verified on classic `/settings/config-repo`. |
| Models: New connection, Test Connection, Enable/Disable, Remove confirm, stored vs ambient credentials, `hostd` placement, default & fallback `<g-model-picker>`, reasoning effort, Token usage & budgets (`#usage`, mode, floor, retention, budgets), paused banner `Resume`, New task model propagation | `/next/` & classic `/` | Fixed Go zero-time `"0001-01-01"` timestamp, added paused `Resume` button, empty-connections message, `#usage` anchor, and config-lock disabling on `/next/`; stored `Custom command` disabled card, rich host-login dropdown with `;` fallbacks, and live burn/sustains columns verified on classic `/settings/models`. |
| Agents: `Standing instructions` (`promptExtension`), `Task branches` (`branchTemplate` & `deploymentId` validation), `External agents` (`#external-agents` toggle & `gma_` token mint/revoke), `Commit identity` (`#commit-identity` → `Settings → Git`), max turns & default capabilities | `/next/` | Added `Standing instructions`, `#external-agents` toggle, and `#commit-identity` link on `/next/settings/agents`. |
| Memory: deployment Markdown edit, dirty guard, `PUT /api/memory` save & reload, repository memory notes, `/next/memory` redirect, not pinned in rail by default | `/next/` | Wired deployment memory draft into `SettingsStateService` dirty tracking and rendered repository memory notes on `/next/settings/memory` (Bug 2). |
| Tasks: auto-merge default, approval/review templates, QA rollout (`#card-qa-rollout`) & propagation into New task; structured `GrantsPicker` & custom-field legacy value filter | `/next/` & classic `/` | `/next/settings/tasks` covers auto-merge, templates, and QA rollout; `GrantsPicker` (`api:ci-read`) and custom-field legacy task-list link (`/?f=custom.priority:legacy:`) verified on classic `/settings/tasks`. |

## Steps and outcomes

### 1. Layout, paths, legacy links and filter — PASS after Bugs 1 & 2

- Desktop (`1440×900`): `/next/settings` redirects to `/next/settings/models`
  and highlights `Models` in the 280 px Settings drawer (`scrollWidth == 1440`).
  Phone (`390×844`): `/next/settings` renders the five groups (`Models &
  sandboxes`, `Secrets & access`, `This browser`, `System / Operations`,
  `Managed in config repo`) full-width (`scrollWidth == 390`); tapping `Audit`
  opens `/next/settings/audit` alone under `← All settings`, which returns to
  `/next/settings`.
- Legacy routes verified on `/next/`: `/next/admin` → `/next/settings/general`,
  `/next/admin/workflow` → `/next/settings/tasks`, `/next/admin/executors`,
  `/next/system/executors` and `/next/settings/hosts` → `/next/settings/hostds`,
  `/next/admin/integrations`, `/next/system/integrations`,
  `/next/settings/credentials` and `/next/settings/legacy-capabilities` →
  `Capabilities` scrolled to `#credentials` (`scrollTop: 1063`),
  `/next/settings/secrets` → `Capabilities` scrolled to `#other-secrets`
  (`scrollTop: 1601`), `/next/admin/upgrade` and `/next/system/upgrade` →
  `/next/settings/upgrade`, and `/next/memory` → `/next/settings/memory`.
- Deep-link `#<card>` scrolling inside `.settings-stage-scroll` aligns section
  headings near the top of the stage (`rectTop` 76–182 px) across
  `/next/settings/models#usage`, `/next/settings/agents#external-agents`,
  `/next/settings/tasks#card-qa-rollout`, and
  `/next/settings/capabilities#credentials`, surviving reload and browser
  Back/Forward.
- Stateless sections (`Upgrade`, `Audit`, `CLI`, `Preferences`, `Config repo`)
  omit the `All changes saved` header badge; stateful sections show
  `All changes saved` when clean, `Unsaved changes` when dirty, and `Saving…`
  while committing.
- Pressing `/` focuses `settings-filter-input`; typing `house style` filters to
  `Agents` (typing `zzzqqq` renders `No setting matches "zzzqqq".`), and
  pressing `Escape` clears the filter and restores all 20 sections. Tablet
  searchable `Section` `<select>` (`820×1180`) and field-level highlight were
  additionally confirmed on classic `/settings` (`/next/` keeps its 2-pane
  drawer cleanly at `820×1180` with `scrollWidth == 820`).

### 2. Save, role and config-repo lock model — PASS after Bugs 2 & 3

- Editing `Standing instructions` on `/next/settings/agents` or `Deployment
  memory` on `/next/settings/memory` marks `Agents` / `Memory` with a dirty dot
  in the drawer and opens the bottom `Unsaved changes` save bar (`Discard` +
  `Save changes`). Clicking another section (`General`), a stage deep link
  (`Settings → Git`), or a rail destination (`Inbox`) opens `Discard unsaved
  changes?`; choosing `Cancel` keeps the section and draft intact, while
  choosing `Discard` drops the draft and navigates cleanly. Clean sections never
  prompt.
- Duration normalization: saving `90s` for `CI capacity reserved after` reads
  back `1m30s` from the server and survives reload. Out-of-range numbers
  (`Max agent turns` `-5`) are rejected before saving and clear once restored.
- A simulated `503` on `POST /api/settings` surfaces the error banner with
  `Retry`, and `pendingRestart: ["pollInterval"]` renders the `Restart needed`
  chip linking to `/next/fleet?tab=controls` (Power controls).
- Viewer role (`grain/viewer` lacking `settings:write`, `memory:write`,
  `access:write`): `/next/settings/general`, `/next/settings/agents` and
  `/next/settings/memory` show the read-only role notice naming `grain/viewer`
  and disable form controls; `/next/settings/access` hides mutating role menus
  and `Remove` buttons.
- External config repo (`Demo B`, `https://github.com/acme/grain-config`):
  `General`, `Agents`, `Tasks`, `Memory`, `Engine`, `Caches`, `Sandboxes`,
  `Capabilities` (servers, policies, scope sets) and `Models` connection config
  + usage settings render disabled under the `Managed in external config repo`
  banner and drop out of the keyboard `Tab` order, while `Preferences`,
  `Access`, `CLI`, `Audit`, stored secret inputs, and model credential rotation
  stay usable.
- Internal `grain/config` (`Demo C`): `General → Lock settings in UI` is
  disabled when no config repo exists (`Demo A`) and enabled on `Demo C`. When
  locked, managed sections link `View in config repo → tables/grain_config.json`
  (`Memory` links `memory/deployment.md`), `Engine → Max worker agents` stays
  editable as a bootstrap setting, and a refused write opens the held config-repo
  PR action (clearing the refusal banner once a subsequent save succeeds).

### 3. Preferences (`/next/settings/preferences`) — PASS after Bug 4

- Appearance switches cleanly between `System`, `Light` and `Dark` in
  `localStorage` (`grain-theme`), changing `--bg-canvas` / `--text-primary`
  immediately across open tabs via `window` `storage` events.
- Choosing `Asia/Tokyo` in `Display timezone` updates `grain.timeZone` in
  `localStorage` (zero `/api/` writes) and shifts `Access` added-at timestamps,
  `Audit` entries (`Nov 15, 2026, 02:52:04 PM GMT+9`), and `Models` pause times
  from browser UTC to JST; `Browser default` restores the browser's zone.
- Clicking `Customize…` opens `Customize navigation`; hiding `Sessions` or
  reordering destinations updates both the current tab's rail and a second open
  tab immediately with zero server writes. Board/session/section/System-tab
  pins and task-timeline order were additionally verified on classic
  `/settings/preferences`.

### 4. Access (`/next/settings/access`) — PASS after Bug 5

- `This browser` displays `Your role` (`grain/operator`) and
  `Signed in with the access code. Changes made here are recorded as
  alice@example.com (access code), not as a person.`, matching `GET /api/whoami`
  and the account popover.
- Clicking `Log out` from `/next/settings/agents` signs out while keeping
  `/next/settings/agents` in the URL; signing back in with the access code
  returns directly to `/next/settings/agents`.
- On IAP-enabled `Demo F`, an unaccepted JWT for `stranger@example.com` shows
  `Access Denied` naming `user:stranger@example.com` as the exact principal to
  add.
- `Allow principal` validates `user:<email>`, `group:<email>`, and
  `serviceAccount:<name>@*.gserviceaccount.com`, defaults new entries to
  `grain/viewer` (not the nonexistent `grain/developer`), requires an audit
  reason for `serviceAccount:` entries and dangerous-scope roles
  (`grain/operator`, `grain/admin`), persists role changes via `PATCH`, and
  opens a confirmation dialog before every removal — explicitly warning when
  removing your own identity (`Remove your own access entry?`) or the last
  remaining entry (`Remove the last allowed principal?`).

### 5. CLI (`/next/settings/cli`) and Audit (`/next/settings/audit`) — PASS after Bug 6

- Both remain fully interactive under an external config repo (`Demo B`).
- `/next/settings/cli` renders `Install`
  (`curl -fsSL <origin>/cli/install.sh | sh` plus running release
  `v3.9.0-dev.104+5a9bce6`), `Sign in`
  (`grain login <origin> --name <slug> --issue-token --scope-set grain/external-dev --expires 30d`),
  and all four `Agent skills` commands (`grain skill install --jetski`,
  `--claude`, `--codex`, `--gemini`), with working `Copy` buttons verified
  against clipboard contents. With `Accept external agents` off, `Sign in`
  displays an inline warning linking to `/next/settings/agents#external-agents`;
  enabling `Accept external agents` clears the warning.
- `/next/settings/audit` renders `Configuration changes`, `Credential access`
  and `Secret resolutions` without exposing secret values, stacks rows cleanly
  as block cards at `390×844` phone width (`scrollWidth == 390`, zero axe
  issues), and displays per-log read errors (`Credential audit could not be
  read (vault offline)`) without hiding unaffected logs.

### 6. General (`/next/settings/general`) — PASS after Bug 3

- `Environment name` and `Show closed tasks by default` commit via
  `POST /api/settings` and survive reload.
- `Lock settings in UI` is disabled on deployments without a config repo and
  enabled when `grain/config` is active.
- `About` displays `v3.9.0-dev.104+5a9bce6`, matching the rail footer stamp.
- With `-disable-self-upgrade` (`Demo E`), self-upgrade controls are omitted on
  `/next/settings/upgrade` and classic `/settings/general`.

### 7. Config repo (`/next/settings/config-repo`) — PASS after Bug 1

- On `Demo C`, `/next/settings/config-repo` shows `grain/config`, branch `main`,
  HEAD commit, working-tree file list, and `Export now`.
- Navigating to `/next/settings/config-repo/tables/grain_config.json` or
  `/next/config?path=tables/grain_config.json` opens the file preview directly.
- With a `state-repo-sync` notification active, Inbox surfaces the failing-sync
  row whose action navigates back to `/next/settings/config-repo`.

### 8. Models (`/next/settings/models`) — PASS

- On `-unconfigured` (`Demo E`), `Connections` displays `No model connections
  configured yet. Add a connection below to advertise models for agent runs.`
  instead of a blank card.
- Adding an Anthropic connection (`claude-walk`), entering an API key, running
  `Discover models` against the loopback catalog (`claude-sonnet-4-5`,
  `claude-haiku-4-5`), testing connection (`OK`), toggling Enable/Disable, and
  confirming `Remove` all persisted cleanly; discovered models appeared
  immediately in `New task`'s model picker alongside `Deployment default
  (gemini-2.5-pro)`.
- Suppresses Go zero timestamps (`0001-01-01T00:00:00Z`) instead of printing
  `Jan 1, 00:00 UTC`.
- `Token usage & budgets` (`#usage`) saves `Concurrency mode` (`Automatic`),
  `Fewest workers` (`2`), `Retention days`, and connection budgets.
- On `-agent-paused` (`Demo D`), the quota banner formats the pause timestamp in
  the selected timezone and provides `Resume` (`DELETE /api/pause`), clearing
  the banner immediately and across reload.

### 9. Agents, Memory and Tasks — PASS after Bug 2

- `/next/settings/agents`: `Standing instructions` saves via save bar; invalid
  branch templates (`bad..{task}`, `static-name`) and invalid deployment IDs
  (`bad id!`) surface server validation errors; `External agents`
  (`#external-agents`) toggles `acceptExternalAgents` and mints/revokes `gma_`
  tokens; `Commit identity` (`#commit-identity`) links to `/next/settings/git`.
- `/next/settings/memory`: editing deployment Markdown triggers the dirty dot,
  save bar, and route-leave confirmation; `Save changes` persists via
  `PUT /api/memory` and survives reload; repository memory notes render below.
- `/next/settings/tasks`: toggling `Auto-merge new tasks by default`, selecting
  approval/review templates, and configuring `#card-qa-rollout` persist cleanly
  and propagate into the `New task` sheet.

## Bugs found and fixed

### Bug 1 — Legacy `/admin/*` & `/system/*` routes, multi-segment `/settings/config-repo/<file>`, `#<card>` deep links, and `/` / `Escape` filter keys failed on `/next/`

- **Reproduction**:
  1. Navigate to `/next/admin`, `/next/admin/workflow`, `/next/admin/executors`,
     `/next/admin/integrations`, `/next/admin/upgrade`, or
     `/next/settings/config-repo/tables/grain_config.json` → wildcards fell
     through to `/next/inbox` instead of the target Settings section or file.
  2. Navigate to `/next/settings/tasks#card-qa-rollout` or
     `/next/settings/capabilities#credentials` → `.settings-stage-scroll`
     stayed at `scrollTop === 0`.
  3. On `/next/settings/models`, press `/` → filter input did not focus; type a
     query and press `Escape` → filter did not clear.
- **Root cause**: `app.routes.ts` lacked legacy `/admin/*` and `/system/*`
  redirect routes and used a single-segment `:file` parameter for
  `settings/config-repo/:file`; `SettingsPageComponent` neither scrolled
  `ActivatedRoute.fragment` inside `.settings-stage-scroll` nor registered `/`
  in `KeyboardService` or handled `Escape` on the filter input.
- **Fix**: Added legacy redirects and `settingsConfigRepoFileMatcher` in
  `ui-ng/src/app/app.routes.ts`, alias fragment anchors and section keywords in
  `ui-ng/src/app/settings/settings-sections.ts`, and fragment scrolling + `/` /
  `Escape` handling in `ui-ng/src/app/settings/settings-page.component.ts`.
- **Regression coverage**: `ui-ng/e2e/next-user-system-settings.spec.ts`
  (`resolves legacy URLs, scrolls #card fragments, focuses filter on '/', and guards unsaved edits`).

### Bug 2 — Dirty Settings drafts (`Agents`, `General`, `Memory`, etc.) had no route-leave guard, dirty nav dot, stateless header suppression, or `Restart needed` chip

- **Reproduction**: Edit `Environment name` or `Deployment memory` without
  clicking `Save`, then click another Settings section or `Inbox` in the rail →
  navigation proceeded immediately and discarded the unsaved text without
  confirmation. `Upgrade`, `Audit` and `CLI` also rendered `All changes saved`.
- **Root cause**: Settings routes in `app.routes.ts` had no `canDeactivate`
  guard, `SettingsStateService` did not track deployment memory drafts or
  `pendingRestart`, and `SettingsPageComponent` did not render a header status
  line, dirty dot, or `Restart needed` link.
- **Fix**: Wired `canDeactivate: [repoLeaveGuard]` onto Settings routes, added
  `SettingsPageComponent.canLeave()` + `beforeunload` + stage section-link
  interception, tracked `memoryDraft` and `pendingRestart` in
  `SettingsStateService`, suppressed `All changes saved` on stateless sections,
  and cleared `refusedChanges` after a subsequent successful save.
- **Regression coverage**: `ui-ng/src/app/settings/settings-page.component.spec.ts`
  and `ui-ng/e2e/next-user-system-settings.spec.ts`.

### Bug 3 — `SettingsStateService.configRepoOnly` locked UI controls whenever `settingsLocked` was true even without a config repo, and `Engine` / `Capabilities` / `Models` missed config-lock rules

- **Reproduction**: On a standalone deployment with no config repo, if
  `settingsLocked` was true in stored settings while server `configRepoOnly` was
  false, `/next/settings/*` locked every form control as read-only AND disabled
  `Lock settings in UI` without a config repo, trapping the user. Conversely,
  under `-external-config-repo`, `/next/settings/engine` showed no config-repo
  lock banner and `Capabilities` / `Models` config inputs stayed editable.
- **Root cause**: `configRepoOnly` in `SettingsStateService` checked
  `Boolean(s["settingsLocked"])` instead of server `configRepoOnly` /
  `externalConfigRepo`; `CONFIG_REPO_SECTION_PATHS` omitted `engine`,
  `registry` and `sandboxes`; `General` did not gate `switch-settings-locked` on
  `hasInternalConfigRepo()`.
- **Fix**: Keyed `SettingsStateService.configRepoOnly` on
  `configRepoOnly || Boolean(externalConfigRepo)`, gated `switch-settings-locked`
  on `hasInternalConfigRepo()`, added `About` and `Show closed tasks by default`
  on `General`, kept bootstrap `maxWorkers` editable under internal lock, and
  disabled config-managed fields on `Capabilities` and `Models` while keeping
  secret/credential inputs editable.
- **Regression coverage**: `ui-ng/src/app/settings/settings-page.component.spec.ts`
  and `ui-ng/e2e/next-user-system-settings.spec.ts`.

### Bug 4 — `/next/settings/preferences` lacked timezone selection, `Customize…` button, and cross-tab `localStorage` synchronization

- **Reproduction**: Open `/next/settings/preferences` in two tabs: there was no
  timezone control or `Customize…` button, and changing theme or pinned rail
  items in tab 1 left tab 2 stale until a full reload.
- **Root cause**: `SettingsPreferencesSectionComponent` only rendered theme and
  density radios, and `ThemeService` / `NavConfigService` did not listen to
  `window` `storage` events or store `grain.timeZone`.
- **Fix**: Added `Display timezone` (`grain.timeZone`), `Customize…`
  (`CustomizeNavDialogComponent`), and `window` `storage` listeners (with
  `ngOnDestroy` cleanup) in `ThemeService` and `NavConfigService`, formatting
  `Access`, `Audit`, and `Models` timestamps in the selected timezone.
- **Regression coverage**: `ui-ng/e2e/next-user-system-settings.spec.ts`
  (`syncs Preferences rail customization across tabs and confirms Access principal removal`).

### Bug 5 — `/next/settings/access` defaulted new principals to nonexistent role `"grain/developer"`, deleted entries without confirmation, omitted `This browser`, and dropped deep links on sign-out

- **Reproduction**: Open `/next/settings/access`, type `user:bob@example.com`
  without touching the Role dropdown, and click `Allow principal` → server
  rejected the default role `"grain/developer"`. Clicking `Remove` on your own
  principal or the last entry immediately fired `DELETE` with no confirmation
  dialog. Signing out from `/next/settings/agents` redirected to `/next/`
  instead of returning to `/next/settings/agents` after signing back in.
- **Root cause**: `newPrincipalRole` initialized to `"grain/developer"` instead
  of `"grain/viewer"`, `removePrincipal` only checked a brittle raw-email match
  that missed `"user:<email>"`, `This browser` was absent, and
  `AccountPopoverComponent.signOut()` hardcoded `window.location.assign("/next/")`.
- **Fix**: Defaulted `newPrincipalRole` to `"grain/viewer"`, added `This browser`
  (`whoamiSummary`, `RoleChip`, `Log out`), confirmed every removal via
  `g-confirm-dialog` with tailored self/last-entry warnings, and reloaded the
  current `/next/...` URL on access-code sign-out so re-authentication lands on
  the original deep link.
- **Regression coverage**: `ui-ng/src/app/settings/settings-page.component.spec.ts`
  and `ui-ng/e2e/next-user-system-settings.spec.ts`.

### Bug 6 — `/next/settings/cli` showed `hostd` registration instead of workstation `cli/install.sh`, `grain login`, and all 4 agent skills, and `/next/settings/audit` overflowed horizontally at 390 px phone width

- **Reproduction**:
  1. Open `/next/settings/cli` → showed `curl ... /api/hostd/install.sh`, plain
     `grain login <origin>`, only 2 skill flags, no `Copy` buttons, and no
     `Accept external agents` warning.
  2. Open `/next/settings/audit` at `390×844` → 5-column `.s-audit-table`
     overflowed horizontally, tripping axe `scrollable-region-focusable`, and a
     failing credential-audit endpoint hid its error via `catchError(() => of([]))`.
- **Root cause**: `SettingsCliSectionComponent` had been stubbed with hostd
  commands, `SettingsAuditSectionComponent` swallowed individual log errors, and
  `settings-forms.css` lacked a `@media (max-width: 599px)` stacked block layout
  for `.s-audit-table`.
- **Fix**: Updated `SettingsCliSectionComponent` to render `/cli/install.sh`
  (with running release stamp), `grain login ... --name <slug> --issue-token
  --scope-set grain/external-dev --expires 30d`, `Accept external agents` off
  warning + link to `/next/settings/agents#external-agents`, all 4 `grain skill
  install` rows (`--jetski`, `--claude`, `--codex`, `--gemini`), and `Copy`
  buttons; surfaced per-source Audit errors independently and stacked
  `.s-audit-table` rows on `< 600px` viewports.
- **Regression coverage**: `ui-ng/src/app/settings/settings-page.component.spec.ts`
  and `ui-ng/e2e/next-user-system-settings.spec.ts` (`stacks Audit rows cleanly
  at phone width with no horizontal overflow or axe violations @phone @dark`).

## Review & verification summary

- **Bundle budget**: `ui-ng` production build `Initial total: 449.49 kB` on
  base `5a9bce62c` (0 warnings) and `455.03 kB` (`125.09 kB` gzipped, well under
  the `550 kB` error budget) after rebasing onto `968121418` (`nav-progress` +
  `route-preload`).
- **Accessibility & layout audit**: Automated Playwright + `axe-core` WCAG 2.1
  AA sweep across 11 Settings pages × 4 widths (`2048×1080`, `1440×900`,
  `820×1180`, `390×844`) × light & dark themes reported `0` serious/critical axe
  violations, `scrollWidth === clientWidth` everywhere, zero raw Go zero-times
  (`0001-01-01`), zero native `<select>` elements on touched `/next/` views, and
  distinct light vs dark PNG renders on every route.
- **Unit & Playwright suites**:
  - `cd ui-ng && npm run lint && npm test` — 49 files / 459 tests passed.
  - `cd ui-ng && npx playwright test e2e/next-user-system-settings.spec.ts e2e/shell.spec.ts e2e/next-walkthrough-daemon-rbac-repos.spec.js e2e/next-full-system.spec.js e2e/next-qa-settings.spec.js e2e/next-settings-models.spec.js` — all passed.
  - Classic `ui/` Playwright regression check (`e2e/user-system-settings-walkthrough.spec.js`, `e2e/settings-cli.spec.js`, `e2e/settings-audit.spec.js`, `e2e/access.spec.js`) — 22 passed.
  - `go test ./tests/hygiene/... ./pkg/ui -run "TestCoreSaysNoGoogleTokens|TestAngularRouteTableInSync"` — passed.
