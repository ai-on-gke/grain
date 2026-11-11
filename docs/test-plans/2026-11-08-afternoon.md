# Nightly New Features Test Plan — 2026-11-08 (Afternoon Batch)

**Audited range:**
- `origin/main`: `1ab19467..33a44581`. This covers every pull request merged into `ai-on-gke/grain` after [`2026-11-08.md`](2026-11-08.md)'s range ended (`1ab19467`, 2026-11-08 11:08 UTC). That is **5 first-parent merges** (PRs #929, #945, #946, #948, #949; 15 non-merge commits, 69 files, +2,839/−2,289), landed `2026-11-08 11:34 UTC .. 12:23 UTC`. Audit timestamp: 2026-11-08 12:45 UTC.
- `latest..origin/main`: `latest` still points to `v3.1.0` (`8a740f99`), so all 2,463 commits in `latest..origin/main` — including this range — remain unreleased on `latest`. `release/v3.2.0` is still preparing `v3.2.0`; nothing in this range was backported to it.
- Schemas: unchanged. Core `SchemaVersion` stays **127**, the registry PostgreSQL schema stays **7**. PR #929 adds a store-upgrade regression test (`pkg/model/sqlite/legacy_grant_migration_test.go`) but no migration.
- **File naming:** named `2026-11-08-afternoon.md` because `2026-11-08.md` (PR #948, committed 11:30 UTC) already covers the earlier batch on the same date, following `2026-10-23-afternoon.md` and `2026-11-01-late.md`.

**Previous plan (`docs/test-plans/2026-11-08.md`) status.** That plan merged at 11:34 UTC (PR #948) and the walkthrough tasks for its seven sections were filed less than two hours before this audit; none of their `docs/test-runs/2026-11-08-*` reports have landed yet. Two of its sections are affected by this batch:
- **§3 (Unified Grants Slice 2, `hosting`, `github#sandbox-repo` retirement)**: item 4 walks `/settings/capabilities`, which now also draws the **Deployment credentials** card (§1 below). The `-legacy-capability` demo flag its shared step 2 mentions still exists, but as a plain catalogue row with no provider behind it (§2 below).
- **§7 (UI narrowing & role-gating polish)**: any step that opens **Settings → Legacy capabilities** now lands on **Settings → Capabilities**.

**Feature summary for this batch:**

| § | Area | PRs |
|---|---|---|
| 1 | The **Settings → Legacy capabilities** tab is retired into a **Deployment credentials** card on **Settings → Capabilities**: old-address redirects, retired-capability secrets kept visible, role gating moved from `settings:write` to `capabilities:admin`, picker hint copy | #929 (`f4e9db32`, `6eb2679f`, `2510d211`, `632a8ffd`) |
| 2 | The **`capability.RegisterProvider` seam for in-process providers is removed**: an empty `ui.OfferedCapabilities`, `-legacy-capability` reduced to a catalogue row, the pre-conversion store upgrade path, bare-name aliases, the in-process servers and editions that relied on the seam | #929 (`5adcdab3`, `4e868623`, `c3b43710`, `5c67dd55`) |

---

## Branch situation & how follow-up tasks should handle it

Releases are cut from `main` (`.grain/settings.yml`), with minor release candidates converging on `release/v3.2.0`. PR #929 is a `refactor`/`feat` change, so it is **not** an `[rc-blocker]` backport candidate. Any `fix:` a walkthrough lands for it on `main` only needs a backport if the same defect exists on `release/v3.2.0` (which still has the Legacy capabilities tab). In that case, propose a backport task with `writes: [{repo: "ai-on-gke/grain", base: "release/v3.2.0"}]` and a `Backported-from: <sha>` footer rather than pushing to the release branch directly.

Every follow-up walkthrough task filed from this plan targets `main` (`repo: "ai-on-gke/grain"`, `branch: "main"`) and depends on [Task #3643](/tasks/3643), so `docs/test-plans/2026-11-08-afternoon.md` is on `main` when each walkthrough begins. Each proposal asks for unattended `auto_merge: true` once CI is green. The operator who approves the proposal decides whether to grant auto-merge on `main`.

The gate is **GitHub Actions**: `Tests` in `.github/workflows/tests.yml`, `cla/google` and the workflow/hygiene scans. Inside a sandbox, inspect and wait on PR checks with `grain pr open` and `grain ci wait` (with `timeout: 600000`, no `--jobs` flag). Do not rely on the MCP `pull_request_status`, `base_branch_status` or `wait_for_checks` tools on `hostd`/`runhost` sandboxes, and never chain `grain pr status` ahead of `grain ci wait`. If a check is red on `origin/main` before your branch touches it, merge `origin/main` and report the breakage rather than weakening the check.

Shared environment and hygiene rules (the same as [`2026-11-08.md`](2026-11-08.md#branch-situation--how-follow-up-tasks-should-handle-it), repeated so each task is self-contained):

1. **Build from `HEAD`:** `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`. Run `make fmt-frontend` before committing any edit under `ui/` or `ui-shared/`.
2. **Ports & local servers:**
   - Start in-sandbox daemons with `-sandbox-port-base=0` (`9728` is held by the sandbox git proxy). Committed scripts and tests use `42xx` convention ports or `127.0.0.1:0` listeners so `go test ./pkg/ports/...` passes.
   - `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:<port>` runs no orchestrator dispatch. Add `-secrets-store` to set and remove secrets for real, `-legacy-capability` for a Legacy capabilities picker row, `-external-config-repo <url>` for config-locked mode, and `-as <principal>` to attribute fake tasks.
   - Flows that need real dispatch, capability passes, store migrations or upgrades must run a real `grain daemon` (`-sandbox-port-base=0`) over SQLite **and** PostgreSQL.
3. **System packages:** `postgresql`, `postgresql-client`, `sqlite3` and `jq` are an `apt-get` away (`sudo apt-get update && sudo apt-get install -y <pkg>`). Check with `command -v` before chaining commands.
4. **Playwright & screenshots:**
   - Run specs with `cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test <spec>`, and take screenshots with `make ui-screenshots PAGES=... OUT=...` (1440 px desktop and 390 px phone, light and dark).
   - Restore any mutation of the worker-scoped `demoServer` (`ui/e2e/fixtures.ts`) in `finally`.
   - Any fix that touches `ui/` must render before/after screenshots and attach `contact-sheet.png` by `path` to `comment_on_issue`.
5. **Hygiene tests:**
   - Every committed `.go`/`.py`/`.sh`/`.js`/`.mjs` file under `docs/test-runs/` carries the Apache-2.0 header (`go test ./tests/hygiene/...`).
   - No bare `"v2"` literals or `v2/<ident>` path segments in committed code (`tests/deploy/v2_test.go`).
   - No Google-internal tokens outside `docs/test-runs/`, `docs/test-plans/` and `docs/releases/` (`TestCoreSaysNoGoogleTokens`).
   - CLI help and UI copy say "host"/"hosts", never `hostd`.
   - Any change to a `grain` command or flag regenerates `GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` and commits `design/spec/cli/*.spec.md`.
6. **Git author & trust boundary:**
   - Commit with the sandbox's pre-configured author (never set `user.email` or `--author`).
   - Edits under `pkg/capability/*`, `pkg/staterepo/*` or `pkg/access/*` trigger the automated security review in `.grain/triggers/*`. Expect it and say so in the report.
7. **Sibling overlap:** §1 and §2 both touch `ui/src/state.js` (`NO_CAPABILITIES_HINT`, `configuredCapabilities`) and `pkg/ui/labels.go`. They also overlap with `2026-11-08.md` §3 on `ui/src/components/settings/CapabilitiesSection.jsx` and `GrantsPicker.jsx`. Fetch and merge `origin/main` before pushing, and rebase small fixes rather than reformatting shared files.

---

## §1. Settings → Capabilities "Deployment credentials" card — retiring the Legacy capabilities tab

### What changed
- **The tab is retired into a card** (`f4e9db32`, PR #929):
  - `CapabilitiesPanel.jsx` (601 lines) and its tests are deleted.
  - `CredentialsSection.jsx` is no longer a Settings section (`credentials` is gone from `SETTINGS_GROUPS`' "Secrets & access" group). It is now a **Deployment credentials** card at the foot of `CapabilitiesSection.jsx`, at the same `#credentials` anchor. The card holds the GitHub host (`githubHost`), the GCP project and service-account pool (`gcpProject`, `gcpServiceAccountEmail`) that the gcp server's migrated `gcp-key`/`gemini-key` methods were seeded from, and the `SecretsPanel` secrets list.
- **Old addresses redirect** (`ui/src/settingsSections.js`):
  - `SETTINGS_SECTION_ALIASES` now maps `credentials`, `legacy-capabilities` and `secrets` → `capabilities`.
  - `OLD_SETTINGS_TABS.integrations` → `capabilities`, so `/admin/integrations` and `/system/integrations` land there (`ui/src/paths.test.js`).
  - The `#hash` is kept, for example `/settings/credentials#githubHost`.
- **Settings search** (`ui/src/settingsIndex.js`): the five credential fields and the card entry (`card:capabilities:credentials`, label "Deployment credentials") now point at `section: "capabilities"`. The old section's `keywords: ["credentials", "secrets", "legacy capabilities"]` went with the section.
- **Role gating changed:**
  - `SECTION_WRITE_SCOPE` lost `credentials: "settings:write"`. The card now inherits Capabilities' `capabilities:admin` read-only state (`CapabilitiesSection` → `s.readOnly`).
  - The routes it writes are still `PUT /api/settings` (`settings:write`) and `PUT`/`DELETE /api/secrets/...` (`secrets:write`) (`pkg/ui/route_scopes.go`).
  - `SecretsPanel` takes no `readOnly` prop at all (`SecretsPanel({ showError, claimed, onSecretsChanged })`).
- **Retired-capability secrets stay visible** (`632a8ffd`):
  - `SettingsPage` no longer claims `settings.capabilities[].secrets` in `claimedSecrets`. `GET /api/settings` still reports retired statuses (`github-sandbox`, `registry-push`, `image-signing`) through `capabilityProviderCatalog`, so their stored `github-app`, `registry-push`, `cosign-key` and `cosign-password` secrets stay listed under **Other secrets**, with **Delete secret**.
  - The `SecretsPanel` helper copy now links to `#capability-servers` instead of linking `/settings/capabilities` to itself.
- **Copy changes** (`ui/src/state.js`):
  - `NO_CAPABILITIES_HINT` is now `"None offered -- grant access under Grants"`.
  - `capabilityUnavailableHint` and `RETIRED_CAPABILITY_HINT` comments point at **Settings > Capabilities**.
  - Hints in `CapabilityServersPanel.jsx`, `GitSection.jsx` and `SecretField.jsx` name Capabilities.
- **Docs/specs:**
  - `design/spec/settings/settings.spec.md` §3.4 is now "Capabilities → Deployment credentials (`/settings/capabilities#credentials`)".
  - `docs/capability-servers-ui.md` has a "Deployment credentials (formerly Legacy capabilities)" section.
  - `docs/team-gcp-keys.md` is marked historical.

### What CI proves
- Vitest:
  - `SettingsPage.test.jsx` (+86 lines: the card renders on Capabilities; retired secrets aren't claimed).
  - `SecretsPanel.test.jsx`, `CapabilitiesSection.test.jsx`, `CredentialsSection.test.jsx`, `settingsIndex.test.jsx`, `settingsPlan.test.js`.
  - `paths.test.js`, which covers alias resolution for `/admin/integrations`, `/system/integrations`, `/settings/credentials`, `/settings/legacy-capabilities` and `/settings/secrets`.
- Playwright:
  - `deployment-credentials-gcp-help.spec.js` (new, 175 lines): GCP help wraps; `github-app`/`registry-push` rows and Delete buttons are aligned at desktop and phone.
  - `settings-panel-walkthrough.spec.js` and `sessions-worktree-pinned-nav-walkthrough.spec.js`: old addresses land on Capabilities and light its nav pin.
  - `capability-server-settings.spec.js` (exact "Capability servers" list locator), `deployment-operations-pending-restart-field.spec.js` and `config-repo-iac-modes.spec.js`.

### What is NOT proved
1. **Every old entry point, end to end in a browser, with real data:**
   - Bookmarks with hashes other than `#githubHost`: `/settings/credentials#gcp-settings`, `/settings/secrets#other-secrets`, `/settings/legacy-capabilities#credentials`, `/admin/integrations#githubHost`. Check that each scrolls to the right field on desktop *and* on the 390 px phone layout, where the Settings nav is a drawer.
   - Browser back/forward after a redirect: no redirect loop, and no duplicate history entry that traps **Back**.
   - Settings search for the words people used to find the old tab: `legacy`, `legacy capabilities`, `credentials`, `secrets`, `GitHub host`, `GCP project`. The section keywords were removed, so `legacy` and `credentials` may now match nothing.
   - In-app links that named the old tab: the `capabilityUnavailableHint` "Settings > Capabilities" text, the `GitSection` and `CapabilityServersPanel` hints, `SecretField`'s link, and the setup checklist / alerts bell when the GitHub host is unset. Each should navigate to the card, not to the top of a long Capabilities page.
2. **Role and lock gating of the moved card against a real server:**
   - With the RBAC `reader`, `writer` and `admin` roles (signed in through a live daemon's allowed principals, or the demo's identity override if available), record what each sees:
     - Is `githubHost`/`gcpProject`/`gcpServiceAccountEmail` editable?
     - Are **Delete secret** and set-secret controls shown? `SecretsPanel` has no `readOnly` prop, so a reader may be offered buttons that `DELETE /api/secrets/{secret}` refuses with 403.
     - Is the read-only reason text present and accurate now that the card follows `capabilities:admin` instead of `settings:write`?
   - Confirm the server still enforces `settings:write` on `PUT /api/settings` and `secrets:write` on `/api/secrets` for each role (curl with each identity). A UI that offers a control the server refuses is a bug to fix: pass `readOnly` through to `SecretsPanel`, or hide the controls.
   - Config-locked mode (`grain demo -external-config-repo <url>`): `githubHost` is disabled with the config-repo message, while secrets (never in the config repo) stay settable. Check that `allowInExternalMode` on the card doesn't also unlock the GCP fields.
3. **Secrets lifecycle on the card with a real secrets store:**
   - With `grain demo -secrets-store` (or a real daemon), seed `github-app`, `registry-push`, `cosign-key`, `cosign-password`, a secret claimed by a registered capability server, and an unclaimed one.
   - Check that claimed secrets are listed under their server in the Capability servers card and *not* duplicated under Other secrets.
   - Check that retired-capability secrets appear once, under Other secrets.
   - Set a key, delete a key, delete a whole secret: the list refreshes without a reload and **Pending restart** chips appear only for `githubHost`.
   - Look for console errors and leaked secret values in the DOM or network responses (values must never come back from `GET /api/secrets`).
4. **Picker hint copy everywhere it renders:**
   - Check that `"None offered -- grant access under Grants"` appears, wraps cleanly and is accurate (it names a Grants section that is actually on the same surface) in each of: the New task overlay, Template overlay, Schedule overlay, the task pane's `CapabilityToggles`, Settings → Tasks **Default capabilities**, and the Repository page's default and CI capability pickers.
   - Do this both without and with `-legacy-capability`. With it, the `example-legacy` row should appear instead of the hint.
5. **Visual regression of the now-longer Capabilities page:** render `/settings/capabilities` at 1440×900 and 390×844 in light and dark with many capability servers (10+) and many secrets (20+). Check card spacing, the sticky section nav, `scrollWidth <= clientWidth`, and the `#credentials` anchor offset beneath the header.

### Walkthrough & test procedures
1. Build `ui` and `grain` (shared step 1). Start `grain demo -github-sim -fake-ci -secrets-store -legacy-capability -addr 127.0.0.1:<port>`, a second demo with `-external-config-repo`, and, for item 2, a real `grain daemon -sandbox-port-base=0` with three allowed principals at `reader`, `writer` and `admin`. Put scripts and Playwright harnesses in `docs/test-runs/2026-11-08-deployment-credentials-card/`.
2. Drive items 1–5 in real Chromium with Playwright at desktop (1440×900) and phone (390×844), light and dark, asserting final URL + hash, `document.activeElement`/scroll position, button presence per role, network status codes, and `scrollWidth <= clientWidth`.
3. Turn each confirmed defect into a deterministic Playwright or Vitest regression, fix scoped bugs (role-gated `SecretsPanel` controls, missing search keywords, anchor offsets), take before/after `make ui-screenshots` of `/settings/capabilities`, and write `docs/test-runs/2026-11-08-deployment-credentials-card.md`.

---

## §2. In-process provider seam removal — upgrades, aliases, empty pickers & downstream editions

### What changed
- **The seam is gone** (`5adcdab3`, PR #929):
  - Removed `capability.Registration`, `RegisterProvider`, `Registered` and `Lookup` (`pkg/capability/registry.go`, −152 lines), `admincaps.Providers` (`admincaps` now only holds `TeamScoped`), the made-up provider in `pkg/capability/capabilitytest`, and the `capability.Lookup` fallback in `capabilityDisplayName` (`pkg/ui/capability_status.go`).
  - `capabilityProviderCatalog` no longer appends `admincaps.Providers()`.
  - `pkg/daemon/capabilities.go` stops wiring registered providers.
  - The per-registration `RunGrant` flag (forwarding a grant to `agent.RunConfig.Grants` / `grain mcpserver -grant`) went with `Registration`. `pkg/orchestrator/run_grants_test.go` drops its case.
- **What remains:**
  - `model.CapabilityProvider` and the capability registry still serve the in-process servers (`release`, `model-proxy`, `gcp`, `grain`) and the `release-gate-read` CI alias.
  - A build contributes server methods with `capability.Register` (`methods.go`), and a method can still be `DefaultOn` (`4e868623`).
- **An empty offered list:** `ui.OfferedCapabilities()` now returns nothing (its 100-line rationale comment is gone). `Config.Capabilities` is just the api presets plus whatever a caller appends.
- **The demo flag:** `grain demo -legacy-capability` (`pkg/demo/demo.go`, `pkg/cli/demo.go`) no longer registers a provider. It prepends a plain `ui.Capability{ID: "example-legacy"}` catalogue row "with nothing behind it", so the task forms' still-present but empty **Legacy capabilities** checklists have a row to exercise.
- **Upgrade path test** (`c3b43710`): `pkg/model/sqlite/legacy_grant_migration_test.go` (+92 lines) starts a store at schema 66 holding every retired bare name across tasks, templates, schedules, deployment defaults, the standing session, and repo default/CI capabilities. It upgrades the store to 127 in one step and checks with `sameGrantMeaning` that each grant has the same meaning (api scopes, danger, channels) with nothing retired left over.
- **Docs:** `docs/capabilities.md`, `design/design/capabilities.design.md` and `design/design/unified-grants.design.md` (new "Status (task 2876)" note) describe the seam as removed. The bare names stay **one-release aliases** (`model.CanonicalLegacyGrant`), because no promoted release yet contains every conversion. The task forms' empty Legacy capabilities checklist "is still to be removed".

### What CI proves
- `pkg/capability` (`registry_test.go`, `methods_test.go`): method registration, `DefaultOn` seeding by method name, alias retirement.
- `pkg/ui/registered_capability_test.go`: a made-up *method* (not provider) is listed and resolved. `pkg/orchestrator/consolesession_defaults_test.go`: standing-session defaults without providers.
- `pkg/model/sqlite/legacy_grant_migration_test.go`: the schema 66 → 127 upgrade on **SQLite only**, through the model layer.
- `tests/e2e/loadtest_capabilities_test.go` still passes without the provider registration it used to make.

### What is NOT proved
1. **A real upgrade of a real old deployment, on both stores:**
   - Build an old binary from a pre-conversion commit (`git worktree add /tmp/old <sha>`, where `<sha>` is the last commit at `SchemaVersion` ≤ 66, found with `git log -S'SchemaVersion = 66' -- pkg/model/schema.go`, or `v2.x`/`v3.0.0` if their schema predates the conversions).
   - Run that `grain daemon` over **SQLite** and over **PostgreSQL**, and create tasks/templates/schedules/default capabilities/repo CI capabilities that carry bare names (`gcp-key`, `gcp-key:<name>`, `gemini-key`, `claude-proxy`, `registry-push`, `image-signing`, `package-publish`, `github-sandbox`, `read-issues`, `bootstrap-playbooks`, `github-credential:<name>`, `local-scripts:<name>`, `grain-log`, `self-debug`, `console`, `priority-dispatch`, `durable`) through its API or CLI. Stop it.
   - Start HEAD's `grain daemon` on the same store. Expect a one-step upgrade to 127.
   - Then run `grain state check` and `grain state prepare-upgrade`, a `pkg/staterepo` dump/import round-trip, `GET /api/tasks/{id}`, `GET /api/settings`, `GET /api/repos/{owner}/{name}`, and the UI's grant chips. Every row should be canonical, with nothing reported as an unknown capability, and the daemon log should show no `unknown capability` warnings.
   - The PostgreSQL leg is the important gap: the committed test is SQLite-only.
2. **Bare-name inputs still accepted for one release, end to end:**
   - On HEAD's daemon, file tasks with `capabilities: ["github-sandbox"]`, `["gcp-key"]`, `["read-issues"]` and `["console"]` (via `POST /api/tasks`, the New task form and any `grain` CLI flag that takes capability names — check `grain help`). Each should be stored canonically, with a deprecation warning where the code promises one.
   - `PUT /api/settings` `defaultCapabilities` and `PUT /api/repos/{owner}/{name}/capabilities|ci-capabilities` with bare names: same check. `registry-push` must keep its CI-only refusal on the task path.
   - `capabilities: ["example-legacy"]` on a daemon *without* the demo row must be refused as an unknown capability (400), not silently stored.
   - Mixed spellings in one request are refused (`model.SplitRequestGrants`).
3. **In-process servers and run grants still work without `Registration.RunGrant`:**
   - On a real daemon with dispatch, run one task holding `https://grain.dev/capabilities/model-proxy#model-proxy` `{vendor}` and one holding a `https://grain.dev/capabilities/grain#…` method. Check that admission mints the pass, the sandbox gets its placements/MCP endpoints, and `grain mcpserver -grant <grant>` accepts the forwarded grants without the "unknown grant" warning `RunGrant` used to suppress.
   - Run a grain CI job (fake CI) that relies on the `release-gate-read` alias and a `release#registry-push` CI grant. Check that the job is admitted on a post-merge event and refused on a PR event.
4. **The `-legacy-capability` demo row and the empty pickers:**
   - With `grain demo -legacy-capability`, tick `example-legacy` on a new task, a template, a schedule and Settings → Tasks defaults. Save, reload, and untick.
   - With nothing behind the row, it must not break dispatch-preview/composition endpoints (`pkg/ui/composition.go`), `POST /api/capability-grants/check`, or the escalation/dangerous-grant badges, and it must not 500 any API. Record what an operator sees.
   - Without the flag, check that no form renders an empty "Legacy capabilities" heading with no hint.
5. **Editions that contributed providers:**
   - The commit asserts the downstream Google layer contributes only server methods (`capability.Register`). Verify it if this task can read that repository (`depot.code.corp.goog/experimental/grain`): search it for `RegisterProvider`, `capability.Registration`, `capability.Registered`, `capability.Lookup`, `admincaps.Providers`, and any code assuming `ui.OfferedCapabilities()` is non-empty. If feasible, build it against core `HEAD` with a local `replace` directive.
   - If the repository can't be read (proxy 403), record that as **not verified** in the report and file nothing against it from core.
   - Only describe the downstream repository in `docs/test-runs/` (exempt from `TestCoreSaysNoGoogleTokens`). Never name it in code, comments or fixtures.

### Walkthrough & test procedures
1. Build HEAD's `grain` and an old pre-conversion `grain` in a scratch worktree under `/tmp` (never run release scripts in `/home/debian/work`). Install `postgresql`. Put scripts, seed SQL and captured API output in `docs/test-runs/2026-11-08-provider-seam-removal/`.
2. Run item 1 on SQLite and PostgreSQL, items 2–3 on a real `grain daemon -sandbox-port-base=0` with fake CI and local HTTP stubs for GitHub/GCP/model vendors (stub only those external boundaries), item 4 on `grain demo` with Playwright, and item 5 by repository inspection.
3. Turn each confirmed defect into a deterministic Go regression (extend `legacy_grant_migration_test.go` to run on PostgreSQL if the CI `postgres` job can host it, or add a `tests/e2e` case). Fix scoped bugs, noting the `.grain/triggers/*` security review for `pkg/capability/*`/`pkg/staterepo/*` edits. Write `docs/test-runs/2026-11-08-provider-seam-removal.md`.

---

## Changes not requiring a separate task

- **Recurring UI-surface walkthrough fixes (regression-tested in place):**
  - PR #945 (task 3463, `docs/test-runs/2026-11-08-connections-infrastructure-settings-task-3463.md`) clears blanked host capacities and dismisses resolved host errors (`cbc14c04`: `HostdsSection.jsx`, `HostdsSection.test.jsx`, `ui/e2e/hostds-capacity-and-banner.spec.js`).
  - PR #946 (task 3464, `docs/test-runs/2026-11-08-deployment-operations-task-3464.md`) renders `UnbackedStateDot` inline at 8×8 and formats every remaining `SandboxHealthPage` timestamp in the deployment zone (`9998e793`: `SandboxHealthPage.jsx`, `HealthTab.jsx`, `UnbackedStateDot.jsx`, `ui/e2e/deployment-operations-health-sandboxes.spec.js`).
  - Both were walked in real Chromium at desktop and phone, in light and dark, by the tasks that fixed them. The recurring Connections & infrastructure and System health schedules walk those surfaces again.
- **Test plan:** PR #948 (`docs/test-plans/2026-11-08.md`, this plan's predecessor).
- **Security review:** PR #949 (`docs/security/audits/2026-11-08-second-review.md`, `d7dde0c2..1ab19467`) reports three findings: `pkg/model/canonical_grant.go` is missing from the trust-boundary list (Medium); a hosted instance's Postgres password sits in GCE metadata with TLS disabled (Medium); and run-now on an external credential approves an unapproved no-repository task (Low). The review files one fix task per finding itself, so this plan files none.
