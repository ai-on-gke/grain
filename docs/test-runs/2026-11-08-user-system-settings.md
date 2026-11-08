# User and system settings walkthrough — 2026-11-08

Task 3358: Settings' layout, filter, save and config-repo lock model,
Preferences, Access, General, Config repo, Models, Agents, Memory and
Tasks, driven through visible controls in a real Chromium browser against
real `grain demo` and `grain daemon` servers.

## Commit and environment

- Commit tested: `6d15dc3a` (`main`, "Merge pull request #912 from
  ai-on-gke/grain/098073e4/task-3577-secret-manager-one-secret"), then
  the same tree with this branch's fixes applied. The About card and the
  left rail footer both read `v1.2.0 · 6d15dc3-dirty · 2026-11-08 08:08Z`.
- Sandbox: Debian Linux, Go 1.26.10, Node 20.19.0, Playwright Chromium
  (cached in `/var/cache/playwright`). Built with `npm ci && npm run build`
  in `ui/` and `go build -tags grain_e2e` for the server binary.
- No `/home/debian/.gemini-api-key` was present in this sandbox.
- Servers exercised:
  - `grain demo` over a throwaway embedded store (`ui/e2e/fixtures.js`),
    plus `grain demo -agent-paused` for real orchestrator connection-pause
    state.
  - A real `grain daemon` (`ui/e2e/realdaemon.js` `startDaemon`, with
    `directConfigWrites: false` and `-max-mergers 1`), backed by its own
    controller-local `grain/config` repository, for the Config repo
    section, Config Repo Browser, settings lock, read-only fieldsets, and
    held config-repo pull requests.
- Viewports: desktop `1280×860` (and `1280×600` for `#<card>` scroll
  verification, `1440×900` on `grain daemon`), tablet `800×1000`, and
  phone `390×844`. Preferences time-zone verification ran in a browser
  context configured with `timezoneId: "America/Los_Angeles"`.

## Stubs and intercepted routes

Only external or unseeded states that `grain demo` / a fresh local daemon
lack were intercepted via `page.route`, keeping the real Grain HTTP server,
built React bundle, and Chromium user gestures in the loop:

| Route | Where | Why |
|---|---|---|
| `PUT /api/settings` delayed 600 ms once | `grain demo`, General → Environment name | Makes the transient `Saving…` header state deterministic to observe |
| `PUT /api/settings` → `503` once | `grain demo`, Agents → Max agent turns | Exercises `Couldn't save Max agent turns` and `Retry` recovery |
| `GET /api/settings` + `pendingRestart: ["environmentName"]` | `grain demo`, General | Exercises the `Restart needed` chip and its navigation to `/system/power` |
| `GET /api/config` + `stateRepoSyncFailure` | `grain demo` (Preferences / Inbox) & `grain daemon` (Inbox / Alerts) | Simulates a failing remote config-repo push (`remote rejected push`) to verify time-zone rendering, the header alert, and the Inbox `Config repo` button back to `/settings/config-repo` |
| `GET /api/whoami` and `GET /api/config` with `authMethod: "iap"`, `actor: "user:dana@example.com"`, role `grain/viewer` | `grain demo`, Access | Simulates an IAP-authenticated viewer session (`grain demo` has no IAP proxy in front of it) |
| `GET /api/settings` with `agentBackends: []` | `grain demo`, Models | Verifies the empty-connections state without deleting the shared worker's connection |
| `GET /api/deployment` with `defaultModel` / `defaultModels: ["unadvertised-model-x"]` | `grain demo`, Models | Verifies the orphan/unadvertised default-model warning row |
| `GET /api/config` with `"medium"` removed from `priority` options | `grain demo`, Tasks → Task fields | Strands existing `medium`-priority demo tasks so the `"still carries a value this field no longer offers"` button appears |
| `GET /api/settings` & `GET /api/config` with `externalConfigRepo` / `configRepoOnly` / `settingsLocked`, and `409 config_managed_externally` on `PUT /api/settings` | `grain demo`, `settings-config-modes-walkthrough.spec.js` | Exercises external config-repo banner and refusal dialog on `grain demo` alongside the unstubbed `grain daemon` run |

Test Connection, Access principal tables, pause/resume (`-agent-paused`),
`General → Settings lock`, Config Repo Browser, and `Create Config PR` ran
unstubbed against real `grain demo` and `grain daemon` instances.

## Steps and outcomes

### Layout and paths — pass

- Clicking the gear button (`top-bar-settings`) at the right of the
  desktop page header rail navigated to `/settings/models`. Navigating to
  bare `/settings` on desktop (`1280×860`) also resolved to
  `/settings/models` and displayed all five section groups (`Models &
  sandboxes`, `Secrets & access`, `This browser`, `System / Operations`,
  `Managed in config repo`).
- Navigating to `/settings/tasks` survived a reload; moving to
  `/settings/agents` and pressing browser Back/Forward stepped cleanly
  between `/settings/tasks` and `/settings/agents`. Opening
  `/settings/tasks#task-fields` at `1280×600` scrolled the `Task fields`
  card into view (`y ≈ 129 px`).
- At tablet width (`800×1000`), the left section nav collapsed into the
  `Section` select (`settings-section-select`), and picking `Agents`
  navigated to `/settings/agents`.
- At phone width (`390×844`), tapping `phone-settings` in the bar opened
  bare `/settings` showing the grouped section list; tapping `Agents`
  opened `/settings/agents` alone under the `All settings` back arrow
  with zero horizontal document overflow (`scrollWidth <= innerWidth`),
  and tapping `All settings` returned to `/settings`.
- Legacy URLs redirected to their new destinations:
  - `/admin` → `/settings/general`
  - `/admin/workflow` → `/settings/tasks`
  - `/admin/executors` and `/system/executors` → `/settings/hostds`
  - `/admin/upgrade` and `/system/upgrade` → `/settings/upgrade`
  - `/memory` → `/settings/memory`
  - `/config?path=tables/grain_config.json` → `/settings/config-repo/tables/grain_config.json`

### Filter settings — pass

- Pressing `/` on `/settings/models` focused the `Filter settings` input
  (while typing `/` inside an ordinary text input typed a literal `/`).
- Searching by helper keyword (`"house style"`) matched `Agents → Standing
  instructions for every run`; searching by label (`"turns"`) matched
  `Agents → Max agent turns`. Clicking the result navigated to
  `/settings/agents#maxAgentTurns`, focused the input, and outlined its
  form control (`.setting-highlight`).
- Entering `"zzqqxx"` rendered `No setting matches 'zzqqxx'.`; pressing
  `Escape` cleared the query and restored the grouped section nav.

### Save and lock model — pass

- Checkboxes and selects committed immediately on change; text and number
  fields stayed silent while typing and committed on `Enter` or `blur`.
  With a 600 ms delay on `PUT /api/settings`, `Environment name` showed
  `Saving…` followed by `All changes saved`, updated the document title
  to include `walk-3358`, persisted across reload, and cleared cleanly on
  `blur`.
- Multi-line cards (`Agents → Standing instructions`) showed `Unsaved
  changes in Standing instructions` and the dirty dot (`settings-dirty-dot`)
  in the section nav. Clicking another Settings section or `Inbox` in the
  rail triggered a confirmation dialog (`You have unsaved changes here.
  Leave and discard them?`) and stayed put when dismissed; clicking
  `Discard` restored the stored text, cleared the dirty state, and allowed
  leaving without a prompt. Saving persisted the text across a reload.
- Entering `"1m"` in `Engine → Poll interval` saved and read back the
  server's normalized Go duration `"1m0s"`.
- Entering `"-2"` in `Tasks → Max unreviewed follow-up depth` was refused
  client-side by field label (`Couldn't save Max unreviewed follow-up
  depth` and alert `Max unreviewed follow-up depth must be at least 1; -2
  was not saved.`) with zero `PUT` requests sent, and cleared as soon as a
  valid value (`3`) was saved.
- A `503` on `PUT /api/settings` displayed `Couldn't save Max agent turns`
  with a `Retry` button that re-sent the pending value and recovered to
  `All changes saved`.
- With `pendingRestart: ["environmentName"]`, the `Restart needed` chip
  (`settings-restart-chip`) rendered keyboard-focusable and navigated to
  `/system/power`.
- On the real `grain daemon`, checking `General → Lock settings in UI
  (require config repo PRs)` disabled `General`, `Agents`, `Tasks`,
  `Memory`, `Engine` (keeping `Max worker agents` editable as a bootstrap
  setting), `Caches`, `Legacy capabilities`, and `Registry`, rendered
  `View in config repo → <path>` links into `/settings/config-repo/...`,
  kept locked MUI `Select` controls out of keyboard reach (`tabindex="-1"`,
  `aria-disabled="true"`), and offered `Create Config PR` in
  `config-refusal-dialog` when a config-managed write was refused (opening
  held pull request `#2` on `grain/config`). `repo-setting-refusal-clears`
  confirmed that a refusal banner clears immediately when a subsequent
  save on that field succeeds while leaving unrelated field refusals alone.

### Preferences (`/settings/preferences`) — pass after time-zone fixes

- Switching `Appearance` between `Auto`, `Dark` (`rgb(15, 16, 19)`), and
  `Light` updated the theme immediately and persisted in `localStorage`
  across reloads. `Task timeline order` (`Oldest first` / `Newest first`)
  also persisted across reloads.
- In a browser context in `America/Los_Angeles`, with no time zone chosen,
  `Time zone` defaulted to `Browser default (America/Los_Angeles)` and
  printed timestamps in PDT/PST. Choosing `Asia/Tokyo` updated printed
  timestamps across Inbox AND `Settings → Access` (`Allowed users & groups`
  — after fixing Bug 1 below).
- In `Customize sidebar`, pinned `Settings → Memory`, `System → Power`,
  board `Timeline`, and session `Director`, reordered with `Move up`,
  saved, reloaded, and verified all four shortcuts persisted in the left
  rail; unpinning `Memory` removed it from the rail across reload.
- Recorded every mutating `/api/*` request during the entire Preferences
  flow: zero `PUT`/`POST`/`PATCH`/`DELETE` requests were sent.

### Access (`/settings/access`) — pass after DOM-nesting and time-zone fixes

- Remained fully enabled (`Log out` and principal management) both when
  `General → Settings lock` was enabled on `grain daemon` and under
  `externalConfigRepo` / `configRepoOnly`.
- Under access-code sign-in, `This browser` stated `Signed in with the
  access code.` matching `Acting as operator (access code)` in the rail.
  Under IAP (`dana@example.com` with `grain/viewer`), `This browser`
  named `dana@example.com`, matched the rail's `dana@example.com` badge,
  showed `Your role: grain/viewer — actions outside it are hidden or
  refused.` (with `RoleChip` fixed to render inline as a `<span>` rather
  than a `<div>` inside `<p>`), and hid write controls.
- Clicking `Log out` cleared the session and returned to the `Access code`
  sign-in gate (`401` on `/settings/access`).

### General — pass

- `Deployment`: `Environment name` updated the rail header and tab title;
  `Show closed tasks by default` committed on change and persisted.
- `Settings lock`: `Lock settings in UI (require config repo PRs)` was
  disabled on `grain demo` (no config repository) and enabled/functional
  on `grain daemon`.
- `Updates`: upgrade check mode, hold-pipeline-during-upgrade checkbox,
  and auto-upgrade channel/time persisted across reload and restored
  cleanly.
- `About`: displayed `v1.2.0 · 6d15dc3-dirty · 2026-11-08 08:08Z`,
  matching the build stamp link at the foot of the rail verbatim.

### Config repo — pass

- Exercised on the real `grain daemon`: `/settings/config-repo` rendered
  the controller-local `grain/config` panel, sync status, reset notice,
  remote mirror controls, and encrypted `Secrets key` / `Secrets
  repository URL` backup and private-key import controls.
- Navigating to `/settings/config-repo/tables/grain_config.json` and
  `/config?path=README.md` opened the read-only Config Repo Browser with
  commit metadata and JSON / Markdown previews; unbrowsable paths were
  refused (`403`) and missing allowlisted files returned `404` cleanly.
- With `stateRepoSyncFailure` present, both the header `Alerts` popover
  and the `Inbox` `Settings are not being exported` row surfaced the
  failure, and clicking `Config repo` on the Inbox row navigated to
  `/settings/config-repo`.

### Models — pass after time-zone fix

- Listed `Gemini 2.5 Pro` (`Antigravity`, active, concurrency 4)
  advertising `gemini-2.5-pro` and `gemini-2.5-flash`. Exercised `Test
  Connection`, `Edit`, `Disable` (surfacing `Every provider connection is
  disabled…`, persisted across reload) and `Enable`, and `Remove` (which
  prompted `Remove the connection Gemini 2.5 Pro? …` and kept the
  connection when dismissed).
- Clicking `New connection` and navigating away cleaned up the unfinished
  draft without a `Remove` confirmation and left the connection count
  unchanged. With `agentBackends: []`, the card stated `This deployment
  has no provider connection, so its tasks wait.`
- Verified default model, fallback picker, per-model reasoning effort, and
  warnings when no enabled connection serves the default model or when
  `defaultModels` names an unadvertised model (`No connection advertises
  this model`). On `grain demo -agent-paused`, verified the paused warning
  and clicked `Resume` to clear the pause live.
- Verified `Where each agent CLI runs` reported host readiness on the
  bare-machine daemon (`claude` and `codex` not on `PATH`, `agy` at
  `/opt/agy/bin/agy`).
- Confirmed advertised models (`gemini-2.5-pro`, `gemini-2.5-flash`) fed
  the `Model` pickers in `New task`, `/session/new`, new Schedule, and
  new Template.

### Agents — pass

- Verified `Max agent turns` save/validation, `Standing instructions`
  `Save` and `Discard`, `Task branches` git-ref and `{id}` validation
  (`grain/no-id`, `bad branch {id}`, `a..b/{id}` refused; `walk/{deployment}/task-{id}`
  accepted), invalid `Deployment ID` refusal keeping the 8-hex sample
  deployment ID visible in the placeholder and card copy, and the `Commit
  identity` link navigating to `/settings/git`.

### Memory (`/settings/memory`) — pass

- Displayed deployment-wide memory (`Memory carried by every run`),
  repository memory (`acme/widgets`), and memory documents
  (`release-checklist`, opened inline with summary and body, and
  `flaky-e2e`, whose `Delete` button prompted before deleting).
- Editing deployment memory showed `Unsaved changes in Memory`, armed
  `beforeunload` for reloads, and prompted on rail/section/Back
  navigation; saving persisted the edit across navigation and reload, and
  restoring the original text left the store clean.
- Confirmed `Memory` does not appear in the left rail unless pinned and
  does not appear on `/repos/acme/widgets`.

### Tasks — pass

- Unchecked `Queue new tasks immediately` and `Auto-merge new tasks by
  default`, reloaded `/settings/tasks` to confirm persistence, opened the
  `New task` dialog from `/inbox`, and verified both checkboxes started
  unchecked; restored both defaults afterwards. Verified legacy
  capabilities, default API and capability-server grants, and backlog /
  follow-up depth limits.
- In `Task fields`, with `"medium"` withdrawn from `priority`, clicked the
  `<n> tasks still carry a value this field no longer offers` button and
  confirmed it navigated to the `?f=custom.priority:legacy` filtered task
  list.

### Console and network

- Zero unexpected `console.error` messages or failed HTTP requests occurred
  across any unprovoked flow (after fixing Bug 2's `validateDOMNesting`
  warning in `AccessSection`). Expected provoked statuses were limited to
  the stubbed `503` on `PUT /api/settings`, `400` responses for
  deliberately invalid branch templates / deployment IDs, `409` on
  refused locked writes, and `401` after clicking `Log out`.

## Bugs found and fixed

| # | Bug | Deterministic reproduction | Fix | Regression coverage |
|---|---|---|---|---|
| 1 | Several Settings timestamps ignored the browser's chosen time zone in `Settings → Preferences`: `AccessSection` (`PrincipalTable` `Added` column) hardcoded UTC (`toISOString()... + " UTC"`), `HostdsSection` (`Last check-in`) printed an unlabelled UTC slice, `RegistrySection` (`Last heartbeat`) printed raw ISO UTC, and `ConnectionModelsField` (`Provider's list last refreshed`) and `UpgradePanel` (`Last restart`) called `toLocaleString()` without `TimeZoneContext`. | Choose `Asia/Tokyo` in `/settings/preferences`, then open `/settings/access` (or `Hosts`, `Registry`, `Models` with a discovered list, or `Upgrade` with a restart report): the timestamps stay in UTC or the browser's OS zone instead of `Asia/Tokyo`. | Wired `useTimeZone()` and `formatDateTime(..., zone)` into `AccessSection.jsx`, `HostdsSection.jsx`, `RegistrySection.jsx`, `ConnectionModelsField.jsx`, and `UpgradePanel.jsx`. | Unit tests in `AccessSection.test.jsx`, `HostdsSection.test.jsx`, `RegistrySection.test.jsx`, `ConnectionModelsField.test.jsx`, and `UpgradePanel.test.jsx`; Playwright e2e test in `ui/e2e/user-system-settings-walkthrough.spec.js` (`Settings → Access prints principal Added times in this browser's chosen time zone`). |
| 2 | `AccessSection` rendered `<RoleChip>` (a MUI `<Chip>` defaulting to `<div>`) inside `<Typography>` (`<p>`) for `data-testid="access-own-role"`, causing invalid HTML (`<div>` inside `<p>`) and a React `validateDOMNesting` error when viewing `Settings → Access` with a role. | Open `/settings/access` as a caller with `config.access.role` set (e.g. `grain/viewer`): `access-own-role` renders `<p>Your role: <div class="MuiChip-root">...</div>...</p>`. | Set `component="span"` on `<Chip>` in `RoleChip` (`ui/src/components/settings/AccessSection.jsx`). | Unit test in `ui/src/components/settings/AccessSection.test.jsx` asserting `access-own-role` contains no `<div>` descendant and emits zero `console.error` calls. |

## Verification runs

- `cd ui && npx vitest run`: **257 files passed, 4026 tests passed**.
- `cd ui && npx playwright test e2e/user-system-settings-walkthrough.spec.js e2e/settings-config-modes-walkthrough.spec.js e2e/access-principals-settings.spec.js e2e/settings-models-walkthrough.spec.js e2e/settings-models-all-disabled.spec.js e2e/settings-agents-branch-template.spec.js e2e/memory.spec.js e2e/repo-setting-refusal-clears.spec.js --project=chromium`: **29 passed**.

---

## Second run: Task 3462 (`grain/098073e4/task-3462-walk-through-settings-its`)

An independent browser walkthrough executed initially against `297401e2`
(`main`, "Merge pull request #935", before Task 3358's PR #938 landed) and then
rebased and re-verified on `1ab19467` (`main`, "Merge pull request #941").
Because the two runs ran concurrently before PR #938 merged, this run
independently reproduced Task 3358's `AccessSection` hardcoded-UTC `Added`
timestamp and `RoleChip` `<div>`-in-`<p>` `validateDOMNesting` issues, and also
uncovered and fixed two additional Settings UI bugs below.

### Commit and environment

- **Commits tested**: `297401e2` during initial walkthrough and screenshot
  baseline; `1ab19467` after fast-forwarding onto `origin/main` with this
  branch's fixes rebuilt into `pkg/ui/static`.
- **Servers**:
  - `grain demo -addr 127.0.0.1:0 -open=false -access-code-file <temp>` over a
    throwaway embedded SQLite store.
  - `grain daemon` (`startDaemon` with `directConfigWrites: false`,
    `-max-mergers 1`) over a throwaway SQLite store, controller-local
    `grain/config` Git repository, and a throwaway local bare Git repository
    (`git init --bare`) for external config-repo mode.
- **Browser & viewports**: Playwright (`@playwright/test` 1.62.1) driving real
  Chromium (`/var/cache/playwright`) at desktop (`1280×860`, `1440×900`, and
  `1280×560` for hash-anchor scroll checks), tablet (`800×1000`), and phone
  (`390×844`) widths in light and dark themes, with browser time zones `UTC`
  and `America/Los_Angeles`.
- **Gemini API key**: `/home/debian/.gemini-api-key` was absent in this sandbox.

### Stubs and intercepted routes (Task 3462)

| Route / boundary | How it was intercepted | Why |
|---|---|---|
| `PUT /api/settings` (delay & 503) | Delayed one `PUT /api/settings` by 250 ms via `page.route`, and answered a single `PUT /api/settings` with HTTP `503` (`{"error":"simulated transient failure"}`) before letting `Retry` hit the real server. | Deterministic verification of `Saving…` → `All changes saved` and `Couldn't save <field>` + `Retry`. |
| `GET /api/settings` (`pendingRestart` / `restartRequired`) | Folded `restartRequired: ["maxAgentTurns"]`, `pendingRestart: ["maxAgentTurns"]` into one `GET /api/settings` response on `grain demo`. | Exercised the `Restart needed` chip and its navigation to `/system/power` within `Agents`. |
| `GET /api/config` & `/_gcp_iap/clear_login_cookie` | Folded `{ authenticated: true, authMethod: "iap", actor: "user:dana@example.com", iapLogoutURL: "/_gcp_iap/clear_login_cookie", access: { role: "grain/viewer", roles: ["grain/viewer"], full: false, scopes: [...] } }` into `GET /api/config` in an isolated context, and answered `/_gcp_iap/clear_login_cookie` with `200 Signed out of IAP`. | Verified signed-in identity email badge, `This browser` card, role-scoped read-only rendering, and IAP sign-out redirect without an external IAP proxy. |
| `GET /api/settings` & `GET /api/config` (models & pauses) | Folded `discovered: { models: ["gemini-discovered-preview"] }` onto `gemini-pro`, tested `defaultModel: "vanished-model-99"`, folded `paused` (`canLift: true`) onto `gemini-pro` and `gemini-2.5-pro`, and recorded `DELETE /api/pause` calls. | Verified discovered-model propagation across pickers, unadvertised-model warnings, and `Resume` calls without an external provider API key. |
| `GET /api/settings` (`taskFields`) | Folded `legacyTaskCount: 2` onto `priority` on `GET /api/settings`. | Verified the `"2 tasks still carry a value this field no longer offers"` link into `/?f=custom.priority%3Alegacy`. |
| `GET /api/state-repo` (`resetNotices`) & `GET /api/config` (`stateRepoSyncFailure`) | Folded a synthetic `resetNotices` entry (`tables/grain_config.json`) onto `GET /api/state-repo` (letting `POST /api/state-repo/dismiss-reset` hit the real daemon), and folded `stateRepoSyncFailure` onto `GET /api/config` after testing a real unreachable bare path (`502`). | Immediate verification of reset dismissal, header `Alerts` popover, and `/inbox` `Config repo` navigation without waiting 5 minutes for `syncFailureGrace`. |
| Local bare Git repository (`git init --bare`) | Seeded a throwaway local bare repository from `grain/config` and pointed `Config repo → External repository` at its path. | Exercised real Git fetch/validate/switch in `grain daemon` without external GitHub network access. |

### Steps and outcomes summary (Task 3462)

- **Layout, paths, viewports, and legacy redirects**: Opened Settings from
  `top-bar-settings` on desktop and `phone-settings` on phone (`390×844`);
  verified bare `/settings` opens `/settings/models` on desktop and the grouped
  list (`Models & sandboxes`, `Secrets & access`, `This browser`, `Operations`,
  `Managed in config repo`) on phone; verified `/settings/<section>` and
  `/settings/tasks#task-fields` (`top ≈ 80 px` at `1280×560`) across reload and
  Back/Forward; verified tablet (`800×1000`) `Section` select, phone
  `All settings` back arrow, zero horizontal overflow across all 8 sections, and
  all legacy redirects (`/admin`, `/admin/workflow`, `/admin/executors`,
  `/system/executors`, `/settings/hosts` → `/settings/hostds`,
  `/admin/upgrade`, `/system/upgrade`, `/memory`).
- **Filter settings**: Verified `/` focuses the filter input (and is ignored
  when typing in an input), label (`instruc`) and helper keyword (`overcommit`)
  matches grouped by section, `Enter` scroll + focus + `.setting-highlight`,
  `"No setting matches …"`, and `Escape` restoring section groups.
- **Save and config-repo lock model**: Verified immediate save on
  checkbox/select/radio, blur/`Enter` save on single-line text/number, explicit
  `Save` / `Discard` on `Standing instructions` and `Memory`, `Saving…` →
  `All changes saved`, `1m` → `1m0s` server normalization read-back, `-5`
  client-side refusal by label with zero `PUT`s and banner dismissal on restore,
  `503` `Couldn't save Max agent turns` + `Retry`, dirty dot + leave prompts,
  `Restart needed` chip → `/system/power`, `General → Settings lock` across
  `Managed in config repo` + `Engine` + `Caches` + `Legacy capabilities` (where
  **Bug A** below was found), `"View in config repo → <path>"` links, live `409
  config_managed_externally` → `Create Config PR` opening held task `#2` / PR
  `#2` and clearing refusal status after a subsequent valid save, and external
  config-repo switching (`external-config-repo-banner`, disabled `Settings lock`,
  usable `Access`).
- **Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks**:
  - `Preferences`: `Auto` / `Light` / `Dark` theme persistence, `America/Los_Angeles`
    default vs `Asia/Tokyo` override across `/inbox`, `/tasks/<id>`, and
    `/settings/access`, `Oldest first` / `Newest first` timeline order,
    `Customize sidebar` pinning/reordering/unpinning (`Memory`, `Power`, board,
    session) across reload, and zero server writes.
  - `Access`: usable when locked or in external config-repo mode; access-code
    and stubbed IAP (`dana@example.com`, `grain/viewer`) sessions, principal
    validation + dangerous-role/service-account reason prompts, and `Log out`.
  - `General`: `Deployment` (`environmentName`, `showClosedByDefault`),
    `Settings lock` disabled without a config repo, `Updates` (`upgradeCheckMode`,
    `upgradePausePipeline`, `autoUpgradeOn`, `Channel`, `Time of day` `04:30`),
    and `About` matching the rail `build-stamp`.
  - `Config repo`: internal `grain/config` sync status, `Export now`,
    `Back up secrets here` / `Stop backing them up`, reset notice `Dismiss`,
    Config Repo Browser (`/settings/config-repo/tables/grain_config.json`,
    `/config?path=tables/grain_config.json` redirect, `README.md` Markdown
    preview, missing-file inline error), and failing-sync `Alerts` + `/inbox`
    `Config repo` link.
  - `Models`: `Test Connection`, `Disable` / `Enable` + all-disabled warning,
    `Edit` (`Stored` / `Ambient`), unfinished `New connection` draft cleanup on
    leave without a `Remove` prompt (where **Bug B** below was found on
    `Escape` after `Continue`), `Remove` confirmation + 0-connection empty state,
    discovered models + reasoning effort + fallback models feeding pickers in
    `+ New task`, `/session/new`, `New schedule`, and `New template`,
    unadvertised-model warning, paused connection & model `Resume`, and
    `Where each agent CLI runs`.
  - `Agents`: `Max agent turns`, `Standing instructions`, `Task branches`
    git-ref and uniqueness validation keeping the sample deployment ID visible,
    and `Commit identity` link to `/settings/git`.
  - `Memory`: deployment and `acme/widgets` notes + inline documents, dirty
    guards on rail, `Back`, and `beforeunload`, reversible `Save` + reload
    read-back, absent from rail unless pinned and absent from repo pages.
  - `Tasks`: `Queue new tasks immediately`, `Auto-merge new tasks by default`,
    `Default legacy capabilities`, `Default grants` (`api:task-admin`), backlog
    & follow-up limits verified in `+ New task`, and `Task fields` legacy-value
    link into `/?f=custom.priority%3Alegacy`.

### Additional bugs found and fixed in Task 3462

1. **`CredentialsSection` (`Deployment credentials` on `Settings → Capabilities`, formerly `Settings → Legacy capabilities`) wrapped locked config fields in a bare `<fieldset>` instead of `<ReadOnlyFieldset>`**
   - **Deterministic reproduction**: Enable `General → Settings lock` (or
     configure an external config repository) and open `/settings/credentials`
     (which following PR #929 `21d92af1` lands on the `Deployment credentials`
     card of `/settings/capabilities`). `CredentialsSection` passes
     `allowInExternalMode` to `SettingsCard` (so secret inputs remain writable)
     and wrapped its GitHub and GCP config fields in
     `<Box component="fieldset" disabled>` rather than `<ReadOnlyFieldset>`,
     leaving `input[name="githubInsecureHttp"]` without `[data-settings-readonly]`
     dimming (`opacity: 1` instead of `0.5`) and without `ReadOnlyFieldset`'s
     keyboard-inert select/chip protection.
   - **Fix**: `ui/src/components/settings/CredentialsSection.jsx` now wraps its
     GitHub and GCP config fields in
     `<ReadOnlyFieldset disabled={Boolean(readOnly) || Boolean(s.configLocked)}>`
     while keeping `SecretsPanel` outside it.
   - **Regression coverage**: Unit test in
     `ui/src/components/settings/CredentialsSection.test.jsx` and Playwright e2e
     test in `ui/e2e/settings-config-modes-walkthrough.spec.js`.

2. **Pressing `Escape` inside an `Overlay` (such as `Models → New connection` right after `Continue`) failed when an unmounted button dropped focus to `document.body`**
   - **Deterministic reproduction**: Open `/settings/models` → `New connection`,
     select `Claude`, and click `Continue`. `Continue` unmounts as the overlay
     advances to `Authentication`, causing Chromium to drop
     `document.activeElement` to `document.body`. Pressing `Escape` then did
     nothing because MUI's `Dialog` attaches its `Escape` keydown handler to
     `.MuiDialog-root` rather than `document`.
   - **Fix**: `ui/src/components/Overlay.jsx` attaches a ref to `Dialog`'s paper
     and listens for `Escape` on `document`, forwarding it to
     `handleClose(event, "escapeKeyDown")` when the event target is outside this
     `Overlay`'s `.MuiModal-root`, this `Overlay` is the topmost open
     `.MuiModal-root`, and no `.MuiPopper-root` or `[data-open-popover]` is open.
   - **Regression coverage**: Unit tests in `ui/src/components/Overlay.test.jsx`
     and `ui/src/components/ModelBackendPage.test.jsx`, plus Playwright e2e test
     in `ui/e2e/user-system-settings-walkthrough.spec.js`.

### Verification runs (Task 3462 on `1ab19467` and merged `beefc4da`)

- `make frontend && make fmt-frontend`: **clean**.
- `cd ui && npm test -- --maxWorkers=4` & `cd ui-registry && npm test -- --maxWorkers=4`: **passed**.
- Playwright Chromium e2e across `user-system-settings-walkthrough`, `settings-config-modes-walkthrough`, `deployment-credentials-gcp-help`, `capability-server-settings`, `config-repo-iac-modes`, `settings-models-walkthrough`, `settings-panel-walkthrough`, `access-principals-settings`, `hostds-capacity-and-banner`, `task_intake_inbox_lifecycle_walkthrough`, `multi-search-select-layout`, `deployment-operations-pending-restart-field`, `deployment-settings-and-operations-inputs-walkthrough`, `sessions-worktree-pinned-nav-walkthrough`, `config-repo-only-settings`, `config-refusal-pull-request`, and `ui-registry`: **94 passed**.
- `go build ./... && go vet ./... && go test ./tests/hygiene/... ./pkg/ui/... ./cmd/grain/...`: **passed**.

