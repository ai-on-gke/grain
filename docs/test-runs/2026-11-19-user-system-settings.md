# Settings walkthrough (2026-11-19)

- **Date:** 2026-11-19, 03:03–04:05 UTC, one run.
- **Task:** task 7308, branch `grain/53549e42/task-7308-walk-through-settings-its`, base `main`.
- **Commit tested:** `17339dd82` ("Merge pull request #1885"), then the fixes below on top of it (rebuilt and re-verified in Chromium).
- **UI walked:** the new Angular UI at `/next/settings/...` for every step (`ui-ng/src/app/app.routes.ts` and `ui-ng/src/app/shell/nav.ts` both route Settings to `/next/settings`). The classic UI at `/settings/tasks` was spot-checked only to confirm which classic-only features (Task fields / Default grants) are not yet ported to `/next/`. No classic `ui/` file was modified.
- **Environment:**
  - Debian sandbox, `linux/amd64`, Go `go1.26.10`, Node.js `v20.19.0`, npm `10.8.2`.
  - Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC. Viewports: `1440×900` (desktop), `820×1180` (tablet), `390×844` (phone), light and dark themes.
  - Built `pkg/ui/static`, `pkg/ui/static-next`, `pkg/hub/server/static`, and `/var/tmp/grain` (`go build ./cmd/grain`).
  - No `/home/debian/.gemini-api-key` was present in this sandbox, so no external model was called and `LOOK=1` was unavailable; before/after screenshots were generated with `ui/e2e/screenshot.mjs` and layout/clipping/alignment was verified programmatically with bounding boxes and `scrollWidth` across all 48 `(light/dark × desktop/tablet/phone × 8 sections)` renders.
- **Real `grain demo` servers exercised** (each over a fresh throwaway SQLite store via `TMPDIR` and signed in through the real `"Enter access code"` UI form as `alice@example.com`, except the IAP server which authenticated via signed `X-Goog-IAP-JWT-Assertion` headers):
  1. `grain demo -config-change -github-sim -fake-ci -git-proxy -secrets-store -host-executor=hostd` (real controller-local `grain/config` repo, secrets store, and host executor).
  2. The same plus `-model-catalog 127.0.0.1:<port>` (real model-discovery loop against a local `GET /v1/models` stub).
  3. `grain demo -external-config-repo https://github.com/example/walk-config -github-sim -fake-ci -secrets-store` (external config repo mode).
  4. `grain demo -disable-self-upgrade -secrets-store` (no config repo, self-upgrade disabled).
  5. `grain demo -agent-paused -secrets-store` (provider usage-limit pause banner and timestamp formatting).
  6. `grain demo -unconfigured -secrets-store` (zero configured model connections empty state).
  7. `grain demo -ui-iap-audience <aud> -ui-iap-keys-url http://127.0.0.1:<port>/jwks -ui-require-iap -secrets-store` (real ES256 IAP verifier testing both `grain/viewer` read-only access and unaccepted `stranger@example.com` Access Denied flow).

## Stubs

The Grain server, SQLite store, frontend bundles, browser, and user gestures were all real. Only unavailable external boundaries and targeted fault injections were stubbed:

| Boundary / Route | Stub behavior | Why |
| --- | --- | --- |
| Local HTTP server (`-model-catalog`) `GET /v1/models` | Returned two Anthropic models (`claude-sonnet-4-20250514`, `claude-opus-4-20250514`) | Demo has no external Anthropic API access; verified real discovery and propagation into model pickers. |
| Local HTTP JWKS server (`-ui-iap-keys-url`) + ephemeral ES256 JWTs | Served a real JWK public key and signed `X-Goog-IAP-JWT-Assertion` tokens for `viewer@example.com` (`grain/viewer`) and `stranger@example.com` (unlisted) | Demo has no external IAP proxy; verified real IAP verification, viewer RBAC, and Access Denied output. |
| Browser route intercept: `POST /api/settings` → one `503 {"error":"stubbed outage"}` | Returned one transient 503, then continued to the real server | Verified save-failure banner and retry recovery. |
| Browser route intercept: `POST /api/settings` → one `409 config_managed_externally` | Returned a rehearsed `tables/grain_config.json` patch once, then continued to the real server | Verified refusal banner's in-app Config PR link and clearing of `refusedChanges` after a later save. |
| Browser route intercept: `GET /api/deployment/audit-logs*` → `500 {"error":"audit store offline"}` | Returned 500 for one audit source while config-audit and access-history stayed live | Verified independent error surfacing alongside live rows from other audit endpoints. |
| Browser route intercept: `POST /api/agent-keys/*/check*` → `200` | Returned synthetic check response | Demo has no wired provider daemon (`404` otherwise); verified both the real 404 error banner and 200 status path. |

## Steps and outcomes

### 1. Layout and paths — PASS

- **Entry & bare `/next/settings`:** Clicking the Settings gear at the bottom of the left rail on desktop opens `/next/settings` on **Models** (`1440×900`). On phone (`390×844`), Settings is reachable from the bottom bar's **More** sheet; bare `/next/settings` opens the grouped section list (`Models & Compute`, `Repositories & Code`, `Work & Agents`, `Access & Security`, `Instance & Preferences`) with `settings-drawer` full width (`390px`), and selecting a section opens it alone under `← All settings` (`settings-mobile-back`).
- **URL persistence & history:** Section navigation updates `/next/settings/<section>`; reload keeps the section, and Back/Forward move cleanly between sections.
- **Old links:** All verified on `/next/`:
  - `/next/admin` → `/next/settings/general`
  - `/next/admin/workflow` → `/next/settings/tasks`
  - `/next/admin/executors`, `/next/system/executors`, `/next/settings/hosts`, `/next/settings/hostds` → Hosts
  - `/next/settings/credentials`, `/next/settings/legacy-capabilities`, `/next/admin/integrations`, `/next/system/integrations` → `/next/settings/capabilities#credentials`
  - `/next/settings/secrets` → `/next/settings/capabilities#other-secrets`
  - `/next/admin/upgrade`, `/next/system/upgrade` → `/next/settings/upgrade`
  - `/next/config?path=tables/grain_config.json`, `/next/settings/config-repo/tables/grain_config.json` → `/next/settings/config-repo?path=tables%2Fgrain_config.json`
  - `/next/memory` → `/next/context` (`Context & Standing Memory`, `/next/`'s top-level replacement for classic `/memory`).
- **`#<card>` anchors:** `/next/settings/models#usage`, `/next/settings/access#external-agents`, `/next/settings/capabilities#credentials`, and `/next/settings/capabilities#other-secrets` all scrolled their target card headings into view cleanly (`y: 56`–`136` on a `650px`-tall viewport).
- **Filter (`/`):** Pressing `/` on Settings focuses `settings-filter-input`; typing `"max agent turns"` narrows the drawer to `Agents`; clicking pushes history so Back returns to `General`; typing a miss (`zzznosuchsettingzzz`) displays `No settings sections match "zzznosuchsettingzzz".`; pressing `Escape` clears the filter and restores all 20 sections.
- **Tablet width (`820×1180`):** Renders drawer (`280px`) beside stage (`468px`) with zero horizontal overflow (`scrollWidth = 820`); `/next/` does not use a searchable tablet Section select (tracked in Remaining Parity Gaps).
- **Read-only / no-save sections:** `Upgrade`, `Audit`, and `CLI` show no save bar or header state line when clean.

### 2. Save, role, and lock model — PASS after Fixes 3, 4, 5, 9

- **Save bar, Plan preview, Discard, server readback, retry:** Editing `Environment name` opens the save bar (`1 unsaved change`, `Discard`, `Plan / Preview changes`, `Save changes`). Plan preview shows current vs planned values (`not set` → `Walkthrough Staging`). An intercepted `503` shows `settings-error-banner` and keeps the draft intact; retrying saves cleanly and survives reload. On Engine, typing `"1m"` into `Poll interval` saves and reads back normalized as `"1m0s"`. Out-of-range counts (e.g., `-3` in `Max agent turns`) are refused inline by label before saving, disable Save, and clear once corrected.
- **Settings lock & External config repo (Bug 4 — fixed):**
  - Before fix: `Engine`, `Registry & Hub`, and `Models` were missing from config-repo section locking (`banner=false`, no `View in Config repo` link); under `-external-config-repo`, Models left `Edit`, `Remove`, `Add a model connection`, and `Token budget & usage guardrails` enabled; and `General → Lock settings in UI` stayed enabled without a config repo and enabled+unchecked under `-external-config-repo`. Also, a `409 config_managed_externally` error banner had no in-app link to Config repo, and a later successful save did not clear stale `refusedChanges`.
  - After fix: `Engine` and `Registry & Hub` show the `Managed in config repo` pill and `View in Config repo` link under both internal `settingsLocked` and `-external-config-repo`; `Models` and `Sandboxes` show the banner/link and disable config/budget controls under `-external-config-repo` while keeping `Credential`, `Test connection`, and `Discover models` enabled; `General → Lock settings in UI` is disabled when no config repo exists and checked+disabled under `-external-config-repo`; the `409` banner links directly in-app to `/settings/config-repo` preserving rehearsed patches, and a subsequent successful save clears `refusedChanges`.
- **Viewer role via real IAP (Bug 5 — fixed):**
  - Before fix: signing in as `viewer@example.com` (`grain/viewer`) showed a bogus `"Read-only: Your role grain/viewer does not hold settings:write"` banner on browser-local `Preferences` and always-read-only `Audit`, named `settings:write` instead of `access:write` on `Access`, and left `Models`' `Edit`, `Credential`, and `Add a model connection` inputs enabled.
  - After fix: `Preferences` and `Audit` never show a role read-only banner; `Access` names `access:write`; `Memory` names `memory:write`; and `Models` disables `Edit`, `Remove`, `Credential`, `Add a model connection`, and `Token budget` controls for `grain/viewer`.

### 3. Preferences (`/next/settings/preferences`) — PASS after Fixes 1, 2

- **Appearance & zero server writes:** Switching Light / Dark / System updates `data-theme` and `localStorage["grain.themeMode"]` with zero `POST`/`PUT`/`PATCH`/`DELETE` API calls.
- **Time zone:** On `/next/`, `Display timezone` currently lives under `General` (`localStorage["grain.timeZone"]`, no server write); selecting `America/New_York` updated Audit timestamps from UTC to EST immediately and persisted across reload.
- **Navigation rail customization (Bug 1 — fixed):**
  - Before fix: the `"Navigation rail destinations — Show, hide, or reorder destinations on the left rail."` row rendered with NO button at all, even though `openCustomizeNav()` and `CustomizeNavDialogComponent` were already wired in the component.
  - After fix: clicking **Customize…** (`pref-customize-nav-btn`) opens the Customize navigation dialog; toggling `Telemetry` off and saving immediately hid it in a second open browser tab via `storage` sync, and resetting restored it live.
- **Phone layout clipping (Bug 2 — fixed):**
  - Before fix: at `390×844`, `.s-toggle-row` did not wrap, clipping the `System` theme button (`pref-theme-auto`) off-screen (`left: 361px, right: 431px`).
  - After fix: `.s-toggle-row` wraps cleanly on narrow screens; `pref-theme-auto` sits well inside the viewport (`right: 211px`) with zero horizontal overflow.

### 4. Access (`/next/settings/access`) — PASS after Fix 6

- **Sign-in, whoami, sign-out, IAP Access Denied:** Deep-link sign-in to `/next/settings/general` via the access-code page returned to `/next/settings/general`. The header avatar button (`AL`) shows `alice@example.com (access code)` and `GET /api/whoami` matches; Sign out returned to the `Enter access code` prompt. Under `-ui-require-iap`, unaccepted `stranger@example.com` received the `"Not allowed on this grain"` page naming the exact principal `user:stranger@example.com` and `grain access allow user:stranger@example.com`.
- **Allowlist & roles:** Verified adding `group:eng@example.com`, changing role to `grain/viewer` with reload persistence, server validation rejecting missing prefixes, non-`*.gserviceaccount.com` service accounts, and service accounts without a reason, and confirmation dialogs for removing both another user and your own account (`"your own"` warning). Confirmed Access remains usable under `-external-config-repo`.
- **External agent tokens (`gma_`):** Minted `walk-ci-token` (`tasks:read`), saw one-time token banner (`gma_...`), active pill, and Rotate/Revoke/Delete controls.
- **Misleading placeholder (Bug 6 — fixed):**
  - Before fix: `input-principal-entry` had `placeholder="alice@example.com"`, which failed with `400` if followed verbatim, and `switch-external-agent-api` defaulted its fallback to `true` instead of the server default `false`.
  - After fix: placeholder is `user:alice@example.com` with an inline prefix hint (`user:`, `group:`, `serviceAccount:` `*.gserviceaccount.com`, `delegate:`), and `externalAgentApi` fallback matches `false`.

### 5. CLI (`/next/settings/cli`) and Audit (`/next/settings/audit`) — PASS after Fixes 7, 8

- **Both stay usable under `-external-config-repo`** (with CLI's deployment-wide run-location selects properly disabled there).
- **CLI (Bug 8 — fixed):**
  - Before fix: when `Accept external agent tokens` was off (demo default), CLI showed `grain login ... --issue-token` with no warning or link, and offered no Copy buttons.
  - After fix: CLI renders a warning banner (`cli-external-agent-api-off`) with an in-app link (`cli-external-agent-api-link`) straight to `/next/settings/access#external-agents`, plus `Copy install`, `Copy sign-in`, and `Copy skill install` buttons beside the command snippet (`curl -fsSL <origin>/cli/install.sh | sh`, `grain login ... --issue-token --scope-set grain/external-dev --expires 30d`, `grain skill install`).
- **Audit (Bug 7 — fixed):**
  - Displays config, secret, and access audit entries with actor, action, detail (secret paths only, never secret values), actor/category/time filters, and timestamps in the selected timezone (`<time datetime>`).
  - Before fix: endpoint failures (e.g., 500 from `GET /api/deployment/audit-logs`) were silently swallowed with `.catch(() => undefined)`, and at `390×844` the audit table rendered `740px` wide requiring horizontal scroll.
  - After fix: partial endpoint failures surface in `audit-load-errors` (`role="alert"`) while keeping events from healthy endpoints visible, and the table uses `.s-table-wrap.s-table-stack-wrap` + `.s-table-stack` so every row stacks cleanly inside `390px` (`right: 356px`, `scrollWidth: 390`) on phone.

### 6. General (`/next/settings/general`) — PASS after Fix 4

- Verified `Environment name`, `Deployment ID`, `Display timezone`, `Lock settings in UI (require config repo PRs)`, and `Broadcast message to active tasks`.
- Verified `-disable-self-upgrade` on `/next/settings/upgrade` shows `"In-VM self-upgrade is disabled on this deployment; upgrades and instance lifecycle are managed externally."` and hides in-VM upgrade actions.
- Verified shell footer build stamp (`grain v0.0.0-20261119020050-17339dd8295f`).

### 7. Config repo (`/next/settings/config-repo`) — PASS after Fix 9

- **With `-config-change` (real controller-local repo):** Showed `main @ <sha>`, `Sync now`, remote/KMS controls, and the read-only Config repository file viewer opening `README.md` and deep-linked `/next/settings/config-repo/tables/grain_config.json`.
- **Without config repo & with `-external-config-repo` (Bug 9 — fixed):**
  - Before fix: plain demo (no config repo) still fired `GET /api/state-repo/files` after `GET /api/state-repo` answered `available: false`, logging a `501` console error; `-external-config-repo` demo returned an opaque `500 {"error":"internal server error (ref ...)"}` because `errDemoConfigRepo` was not wrapped with `apierror.Public`, and the UI silently swallowed the file-list error.
  - After fix: `settings-code.component.ts` skips `GET /api/state-repo/files` when `available === false` (zero 501 errors) and displays `config-files-unavailable`; when a file-list error occurs, it renders `config-files-error`; and `pkg/demo/demo_external_config.go` wraps `errDemoConfigRepo` with `apierror.Public(...)` so the UI displays the exact human-readable reason.

### 8. Models (`/next/settings/models`) — PASS after Fixes 4, 5

- Verified default model, fallback models, per-model reasoning effort, adding a Claude connection (`walk-claude`) with live discovery against the local `-model-catalog` stub (`2 models discovered`), confirming discovered models propagate into **Default model**, **New task**, and **New session** model pickers, **Edit**, **Credential** save & confirmed **Clear stored secret**, **Test connection** (both real demo 404 banner and stubbed response), confirmed **Remove**, and **Token budget & usage guardrails** (`Auto throttle`, min workers `2`, reload persistence).
- Verified `-agent-paused` renders the quota-exceeded banner with timezone-formatted reset time, and `-unconfigured` renders `"No connections configured yet. Tasks run only on configured connections and wait until at least one is available."`
- Verified external config repo and viewer RBAC gating (Fixes 4 and 5).

### 9. Agents (`/next/settings/agents`), Memory (`/next/settings/memory`), and Tasks (`/next/settings/tasks`) — PASS after Fix 3

- **Agents:** Verified `Max agent turns` validation & save, `Default capabilities`, and `Standing console sessions`.
- **Memory (Bug 3 — fixed):**
  - Before fix: editing `Deployment memory markdown` (`input-deployment-memory`) without touching `Standing instructions` triggered neither `beforeunload` on reload nor any confirmation when switching sections or leaving Settings — edits were silently discarded.
  - After fix: `SettingsWorkComponent` tracks `memoryOriginal` against `SettingsStateService.memoryDirty`, prompting on section switch, rail navigation (`settingsLeaveGuard` in `app.routes.ts`), Back, and reload (`beforeunload`), and leaving cleanly once `Save memory` succeeds. Verified `/next/context` shows deployment + repository scopes (`acme/widgets`, `grain/config`) and their memory packs, and verified repository pages (`/next/repos/acme/widgets`) do not display a Memory tab.
- **Tasks:** Toggled `Queue new tasks immediately by default` and `Auto-merge new tasks by default` off, saved, reloaded, and verified the New task dialog picked up `nt-auto-merge` unchecked. Verified `Quality control & PR rollout` controls render cleanly.

## Bugs found and fixed in this PR

All 9 bugs were reproduced against the real server in Chromium, fixed cleanly (8 in `ui-ng/`, 1 in `pkg/demo/`), covered by unit tests (`settings-page.component.spec.ts`) and Playwright E2E regressions (`ui-ng/e2e/next-settings-walkthrough.spec.js`), and re-verified live.

1. **Preferences missing Customize rail button:** `settings-instance.component.ts` had `openCustomizeNav()` wired to `NavConfigService.openDialog()` but omitted the button in the `"Navigation rail destinations"` row. Added `pref-customize-nav-btn`.
2. **Preferences Color theme clipped off-screen on a 390px phone:** `.s-toggle-row` in `settings-forms.css` lacked `flex-wrap: wrap`, pushing `pref-theme-auto` to `right: 431px`. Added `flex-wrap: wrap` and `flex: 1 1 200px` on `.s-toggle-copy`.
3. **Memory unsaved edits silently lost on reload or navigation:** `input-deployment-memory` edits were not tracked for dirty leave guards. Wired `SettingsStateService.memoryDirty`, `beforeunload`, section-switch confirmation, and `settingsLeaveGuard` in `app.routes.ts`.
4. **Config-repo lock & external-config-repo gaps across General, Models, Engine, Hub, and 409 refusals:** Added `engine` and `registry` to `CONFIG_REPO_SECTION_PATHS`, added `EXTERNAL_CONFIG_REPO_SECTION_PATHS` (`models`, `sandboxes`), disabled Models' connection/usage mutations under `externalConfigRepo` while keeping credentials/tests enabled, disabled General's Settings lock without a config repo or under `externalConfigRepo`, added an in-app Config PR link to `settings-error-banner` when `refusedChanges` is non-empty, and cleared `refusedChanges` on subsequent successful saves.
5. **Viewer role (`grain/viewer`) RBAC holes:** Added `sectionWriteRoute()` in `settings-sections.ts` so `Preferences` and `Audit` never show a `settings:write` read-only banner and `Access`/`Memory` name their real scopes; gated Models' `Edit`, `Credential`, `Add a model connection`, and `Token budget` controls on the caller's scopes.
6. **Access principal placeholder & `externalAgentApi` fallback:** Updated `input-principal-entry` placeholder to `user:alice@example.com` with prefix guidance and aligned `externalAgentApi` fallback to `false`.
7. **Audit swallowed endpoint errors & 740px phone table:** Surfaced per-source load errors in `audit-load-errors` while preserving rows from healthy endpoints, and applied `.s-table-wrap.s-table-stack-wrap` + `.s-table-stack` with `data-label` attributes for clean phone stacking.
8. **CLI missing external-agents-off warning & Copy buttons:** Added `cli-external-agent-api-off` warning banner with an in-app link to `/settings/access#external-agents` when `externalAgentApi` is off, plus Copy buttons for install, sign-in, and skill commands.
9. **Config repo 501 noise without state repo & opaque 500 in `-external-config-repo` demo:** Skipped `GET /api/state-repo/files` when `GET /api/state-repo` reports `available: false`, surfaced file-list errors cleanly in `config-files-error`, and wrapped `errDemoConfigRepo` with `apierror.Public(...)`.

## Remaining classic-to-`/next/` parity gaps (proposed as follow-up)

These features from the classic Settings specification are not yet ported to `/next/` and are outside a scoped walkthrough fix (filed as a focused follow-up task):

- Per-field commit-on-change/blur save model with header state line (`Saving…`, `Unsaved changes in <card>`, `Couldn't save <field>` with Retry, `All changes saved`), dirty nav dot, Restart needed chip linked to Power, tablet searchable Section select, and field-level filter hits that focus/outline the target field.
- Preferences → time zone and task-timeline order controls (time zone currently sits under General) and pinning boards/sessions/System tabs.
- Access → `This browser` card, split `People & groups` and `Service accounts` tables, added-at timestamps, client-side prefix/reason validation, and last-entry removal warning.
- General → `Updates` and `About` cards.
- Models → per-connection Enable/Disable, host picker dropdown with reported CLI logins and ambient missing-login warnings, `;`-separated fallback hosts, paused-model Resume button, stored `Custom command` disabled banner, and `Where each agent CLI runs` readiness card.
- Agents → `Standing instructions` (currently on Memory), `Task branches` git-ref/uniqueness validation, `External agents` card (currently on Access), and `Commit identity` link to Settings → Git.
- Memory → per-repository notes inside `/next/settings/memory` (currently available in `/next/context`).
- Tasks → `Default grants` (`GrantsPicker`) and `Task fields` (including `custom.<name>:legacy` link).

## Tests and checks run

- `ui-ng/`: `npx prettier --check src e2e`, `npm run lint`, `npm test` (76 files, 830 tests passed), `npm run build`.
- `ui-ng/` Playwright E2E (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`):
  - `e2e/next-settings-walkthrough.spec.js` (16/16 passed, including 7 new regression tests with phone bounding-box assertions).
  - `e2e/next-full-system.spec.js` (5/5 passed: desktop/phone × light/dark + WCAG colour contrast).
  - `e2e/next-settings-models.spec.js`, `e2e/next-settings-connections.spec.js`, `e2e/next-settings-connection-tests.spec.js`, `e2e/next-settings-codex-auth.spec.js`, `e2e/next-settings-model-efforts.spec.ts`, `e2e/next-settings-repos.spec.js`, `e2e/next-qa-settings.spec.js`, `e2e/next-capabilities-parity.spec.js`, `e2e/next-connections-infra-settings.spec.js`, `e2e/next-config-repo-sync-error.spec.ts` (all passed across desktop, phone, and dark projects — 81/81 green after selector fix).
- Go tests: `go test ./tests/hygiene/... ./pkg/demo/...` and `go test ./pkg/ui -run TestAngularRouteTableInSync` (all passed).
- Screenshots: `ui/e2e/screenshot.mjs` before and after for `/next/settings/{preferences,cli,audit,general,access,models}` (desktop + phone, light + dark); before and after `contact-sheet.png` attached to the task closing note.
