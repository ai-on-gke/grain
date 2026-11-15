# Connections & Infrastructure Settings Walkthrough — 2026-11-15

- **Date:** 2026-11-15, 05:09–06:10 UTC
- **Task:** [Task #5891](/tasks/5891), branch `grain/53549e42/task-5891-walk-through-settings-git`, base `main`
- **Commit tested:** `5a9bce62c` ("Merge pull request #1558"), re-verified after fixes on a fresh build over the same tree (`/var/tmp/grain-fixed`).
- **Environment:**
  - Debian Linux `linux/amd64`; Go `go1.26.10`; Node.js `v20.19.0`.
  - Real built frontends (`ui/` at `/` and `ui-ng/` at `/next/`, `make frontend`) embedded into a real `grain` binary (`/var/tmp/grain-base`, then `/var/tmp/grain-fixed`).
  - Headless Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), UTC timezone, driven through visible UI controls across `1440×900` (desktop), `390×844` (phone), `1024×860` (medium), and `2048×1200` (wide) in light and dark themes.
  - No `/home/debian/.gemini-api-key` was mounted in the sandbox, so `LOOK=1` was unavailable; `contact-sheet.png` (`before` and `after`) was rendered via `ui/e2e/screenshot.mjs`, verified programmatically (PNG IHDR dimensions, light-vs-dark byte divergence, zero horizontal overflow, bounding boxes, and `axe-core` serious/critical floor), and attached to the task closing note.
  - Real `grain demo` instances, each over its own throwaway embedded SQLite store (`TMPDIR` isolated per instance):
    1. **Configured demo:** `grain demo -open=false -github-sim -fake-ci -git-proxy -secrets-store -host-executor hostd -staging` (and `-agent-paused` for pause-clearing checks).
    2. **Unconfigured demo:** `grain demo -open=false -unconfigured -secrets-store` (for empty-connection notices).
    3. **External config-repo demo:** `grain demo -open=false -github-sim -secrets-store -external-config-repo https://github.com/example/walk-config` (for locked-mode behaviour).

---

## Stubs and Intercepted Boundaries

The Grain HTTP server, embedded SQLite store, secrets store, simulated GitHub (`-github-sim`), host-agent allowlist (`-host-executor hostd`), in-tree capability servers (`gcp`, `gchat`, `figma`, `config-map`, `host-script`, `cloud`), built Angular/React frontends, and Chromium gestures were all real. Only external or single-state demo boundaries were stubbed:

| Boundary / Route | How stubbed | Why needed |
| --- | --- | --- |
| `GET /api/registry` (Playwright `page.route`) | Merged live response with `state` in `"disabled"`, `"connecting"`, `"degraded"` (`lastError`), and `"error"` (`lastError` + `scopeConflicts`) | Demo hub always reports `connected`; needed to exercise `Not configured`, `Connecting`, `Degraded`, `Heartbeat error`, error alerts, and scope-conflict alerts. |
| `GET /api/config` (Playwright `page.route`) | Replaced `access` with `{ role: "custom", full: false, scopes: [...] }` | Access-code sign-in always grants full owner scopes; needed to verify independent gating for `settings:write`, `secrets:write`, `capabilities:admin`, and read-only viewers. |
| `GET /api/capability-servers` (Playwright `page.route`) | Appended a `run: "hostd"` server on offline host `build-runner` with `state: "needs-hostd"` | Demo process refuses saving a host-placed server (`this process does not route placed capability servers to their host`); needed to verify the `needs-hostd` warning when a placed server's host is away. |
| HTTP proxy (`127.0.0.1:3999`) | Local Node `http` server recording `CONNECT` / request lines and returning `502` | Verifies git connection `proxy` routing without external network egress. |
| Throwaway SSH & App keys | `ssh-keygen -t ed25519` and `openssl genrsa 2048` in `/var/tmp/` | Non-real throwaway material for SSH + `known_hosts` and GitHub App PEM validation. |

---

## `/next/` Coverage vs Classic-Only Surfaces

Per the Two UIs rule, every step was walked on `/next/` first wherever a `/next/` route exists, and on classic `/` where `/next/` does not yet have a page or sub-form:

| Area | `/next/` route walked | Classic-only surfaces walked on `/` |
| --- | --- | --- |
| **Git** | `/next/settings/git`, `/next/settings/repositories` | Per-row `Test` probe button, locked-mode pointer link to `/settings/capabilities#credentials`, classic `/repos` list empty-connection notice (no `/next/repos` index route; `/next/` lists repos in `/next/settings/repositories`). |
| **Hosts** | `/next/settings/hosts`, `/next/settings/hostds` | Shared `--join` curl snippet & code chips, full `New code` options (expiry, max uses, default capacity, default labels, allowed principals), inline Host `Edit` (owner/labels/capacity blank-to-clear) and `Rename`. |
| **Capabilities** | `/next/settings/capabilities` (and aliases `/next/settings/capability-servers`, `/next/settings/credentials`, `/next/settings/legacy-capabilities`, `/next/settings/secrets`, `/next/admin/integrations`, `/next/system/integrations`, `/next/settings/capabilities/<id>`) | Individual server detail page `/settings/capabilities/<id>` (`ui-ng` has no per-server detail route yet): retired in-process grant notices, `(i)` info popovers, per-server JSON-schema forms (`gcp` keyless vs SA key, `figma` account `Test` & OAuth callback instructions, `config-map` + `secret-map` file placements, `host-script` editor & script `Test`, `run: host` placement picker, server rename, and holder links to `/tasks/<id>`). |
| **Policy & Scope sets** | `/next/settings/capabilities#capability-policy`, `#api-scope-sets`, `/next/` New task `GrantsPickerComponent` | Scope-set description editing and holder-listing confirmation dialog on `/settings/capabilities#api-scope-sets` (when a task holds the scope set). |
| **Deployment credentials** | `/next/settings/capabilities#credentials`, `#other-secrets`, `/next/settings/git` | Legacy GCP project & service-account pool card (`#credentials` on `/settings/capabilities` linking to `gcp` server). |
| **Hub** | `/next/settings/registry`, `/next/settings/hub` | Advertised repository scope chips, `Listed in hub directory` toggle, encrypted `Registration token (grc_…)` secret input, and `#registry-scopes` (`Primary` / `Secondary` / `Exclude repo` rules with path prefix) on `/settings/registry`. |
| **Engine, CI, Caches** | `/next/settings/engine`, `/next/settings/ci`, `/next/caches` → `/next/settings/caches` | Engine sandbox commitment note and blank-to-reset on numeric `Max worker agents` / `Max merges per repo branch` inputs; granular CI step-output/artifact sub-bounds on `/settings/ci`. |
| **Sandboxes** | `/next/settings/sandboxes`, `/next/sandboxes` → `/next/fleet` | Auto-overcommit min/max bound inputs (`Auto CPU minimum`), disk floor share/minimum inputs, full Kubernetes pod priority class inputs (`grain-interactive`, `grain-agent`, `grain-ci`) and node-selector validation, and custom sandbox image contract v2 / mirror hints card on `/settings/sandboxes`. |

---

## Step-by-Step Walkthrough & Outcomes

### 1. Git (`/next/settings/git` and `/settings/git`)
- **Verify connections (`/next/settings/git`):** Clicking `Verify connections` (`POST /api/deployment/git-connections/check`) renders `git-check-banner` without console or page errors.
- **Plain git host — PAT and SSH + Known hosts:**
  - On base `/next/settings/git`, there was **no `Host kind` selector**, **no `Known hosts` field**, and **no `Proxy` field**, so `GitHub App` stayed enabled on plain git hosts and saving an SSH key for `git2.example.test` failed with `400 known_hosts pins no host key for git2.example.test` (**Bug 2**, fixed below).
  - Re-tested on `/var/tmp/grain-fixed` at `/next/settings/git`: switching `Host kind` to `Plain git host` disables `GitHub App (app)` (`aria-disabled="true"`, hint *"A GitHub App needs a GitHub host: a plain git host has no App API"*); saving `gitssh.example.test` with an Ed25519 private key and `Known hosts` succeeds (`kind: "git"`, `status.fingerprint: "SHA256:..."`, `status.sshUser: "git"`).
  - On classic `/settings/git`, saving a plain-git PAT and SSH connection and clicking `Test` reports the expected reach guidance (*"give the connection an owner/repo scope, or add a repository on git.example.test"*).
- **GitHub token & GitHub App with scope, proxy, and refusal banner clearing:**
  - On base `/next/settings/git`, submitting a GitHub App failed every time with `400 json: cannot unmarshal number into Go struct field AppCredentialInput.app.appId of type string` because `SettingsCodeComponent` sent `appId` and `installationId` as JSON numbers (**Bug 2**, fixed below).
  - Re-tested on `/var/tmp/grain-fixed` at `/next/settings/git` (and verified on classic `/settings/git`): submitting an invalid PEM shows `no PEM block found in GitHub App private key` while keeping all typed fields; replacing it with a valid RSA PEM saves `appId: "12345"`, `installationId: "67890"`, `scope: "walk-app-fixed"`, `proxy: "http://proxy.example.test:3128"`, **clears the error banner on save**, displays `via http://proxy.example.test:3128` on the row, and survives a full reload.
  - Proxy boundary check (`ghe.walk.invalid` with `proxy: http://127.0.0.1:3999` on `/settings/git`): saving sends `CONNECT ghe.walk.invalid:443` to the proxy, whereas clicking per-row `Test` bypasses `CredentialHost.Proxy` and dials `ghe.walk.invalid` directly (known issue in `pkg/capability/githubtoken.ProbeTransport`, a classified path, already tracked from the 2026-11-13 run).
- **Remove confirmation:**
  - On base `/next/settings/git`, clicking `Remove` immediately fired `DELETE /api/deployment/git-connections/{cred}` with no confirmation (**Bug 2**, fixed below).
  - On `/var/tmp/grain-fixed` (and classic `/settings/git`), `Remove` opens `g-confirm-dialog` (`Cancel` focused); cancelling sends no `DELETE`, and confirming deletes the connection and persists across reload.
- **GitHub host pointer & unconfigured notices:**
  - Locked mode (`-external-config-repo`) on `/settings/git`: clicking the `Settings → Capabilities` credential link navigates to `/settings/capabilities#credentials` with the card at `y=88` clear of the `77px` sticky header. On `/next/settings/git`, `Default GitHub host` and author fields are disabled with the `Managed in config repo` pill while inline connection credentials and named tokens remain usable.
  - Unconfigured server (`-unconfigured`): `/repos` and `New task` both explain that no git connection is configured yet, name GitHub tokens/Apps and plain-git tokens/SSH keys, and link to `/settings/git`.

### 2. Hosts (`/next/settings/hosts`, `/next/settings/hostds`, and `/settings/hostds`)
- **Routing & `local` row (`/next/settings/hosts`):** Resolves cleanly to `Hosts` (`hostds`); the co-located `local` row renders `0` action buttons; `old-laptop` (`revoked`) enables `Delete` while `cloudtop-alice` keeps `Delete` disabled (`title="Revoke a host before deleting it"`).
- **Destructive confirmations (`/next/settings/hostds`):** Registering `walk-host-legacy` with `env=walk,tier=test` mints a one-time token banner and dismisses cleanly. Clicking `Revoke` opens `g-confirm-dialog`; `Cancel` keeps the host active; confirming `Revoke` transitions the pill to `revoked` and enables `Delete`; confirming `Delete` removes the row and reports cleared pauses; `Delete revoked` confirms first and reports skipped hosts.
- **Pending host requests (`/next/settings/hostds` and `/settings/hostds`):** Seeded `devbox-carol` shows verified enrollee `carol@example.com`, `SHA256:` key fingerprint, and **no pairing code**; `Accept` moves `devbox-carol` into the Hosts table as a key-bound host.
- **Invite hosts & inline Edit (classic `/settings/hostds`):** Verified shared `--join` snippet, code chips (`default`, `workstations`), creating `walk-enroll` with max uses `5`, default capacity `4`, default labels `pool=walk`, and allowed principal `user:alice@example.com`, followed by `Rotate code` and `Revoke code` confirmations, plus editing host capacity to `6` and clearing it back to blank (resetting to default).

### 3. Capabilities (`/next/settings/capabilities` and `/settings/capabilities/<id>`)
- **Legacy redirects:**
  - Classic `/settings/capability-servers` redirects to `/settings/capabilities`. Four retired in-process notices (`gcp-key`, `model-proxy`, `gemini-key`, `release`) render at the head of `/settings/capabilities` (the remaining three one-release aliases were retired in `2246226c3`).
  - On base `/next/`, `/next/admin/integrations` fell through to `/next/inbox`, `/next/system/integrations` redirected to `/next/fleet`, and `/next/settings/capabilities/gcp` fell through to `/next/inbox` (**Bug 4**, fixed below so all 7 legacy paths open `Capabilities` and replace history cleanly without breaking browser `Back`).
- **Built-in ordering, Disable/Enable, and Remove confirmation (`/next/settings/capabilities` & `/settings/capabilities`):**
  - All 13 servers load with built-ins (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`, `api`, `github`, `grain`, `model-proxy`, `release`, `repo`) ahead of custom `capabilities.demo.grain.example/cloud`. In-process servers omit `Disable` and built-ins omit `Remove`.
  - Clicking `Disable` on `gchat` flips its status pill to `disabled` in place without a reload, and `Enable` restores it.
  - On base `/next/settings/capabilities`, clicking `Remove` on a custom server deleted it on one click without confirmation (**Bug 3**, fixed below to require `g-confirm-dialog`).
- **Schema forms, popovers, host placement & secret cleanup (`/settings/capabilities/<id>`):**
  - Added throwaway `https://capabilities.demo.grain.example/walk-throwaway` backed by the demo's loopback MCP server and `$secret:walk-cap-hdr/token`, clicked `Test` (`The server answered with 2 methods`), opened `About cloud-key` `(i)` info popover, validated pattern refusal on `project` (`Bad Project` refused inline, `walk-proj-01` accepted), stored server-owned `apiToken`, verified `apiToken` does **not** appear under `#other-secrets` (both on classic and on fixed `/next/settings/capabilities`), and confirmed `Remove` deletes the server's secret from `/api/secrets`.
  - Exercised `host-script` (`walk-hello` added, tested, saved to `attached`, and removed), `config-map` (config maps + secret maps), `gcp` (`sandbox-agent` and `org-deploy` permission sets, keyless token vs key mode), `figma` (account `Test` and `Connect Figma` OAuth callback instructions), `run: host` allowlist gating (`corp-search`), and stubbed `needs-hostd` offline-host state.

### 4. Capability Policy & API Scope Sets (`#capability-policy`, `#api-scope-sets`)
- **Capability policy (`/next/settings/capabilities` & pickers):**
  - Added a `deny` rule for `https://capabilities.demo.grain.example/cloud#cloud-viewer`, clicked `Preview policy` (`POST /api/capability-policy/preview`), clicked `Save policy` (`PUT /api/capability-policy`), reloaded `/next/settings/capabilities`, and verified the rule persisted.
  - On base `/next/`, `GrantsPickerComponent` filtered out every method with `policyRefusal`, silently hiding `denied`, `may_be_refused`, and `ci_only` methods instead of showing their verdicts and linking to `#capability-policy` (**Bug 3**, fixed below). On `/var/tmp/grain-fixed`, `GrantsPickerComponent` keeps `cloud-viewer` visible as `aria-disabled="true"` with *"the capability policy denies …"*, keeps `repo#read` `aria-disabled="true"` with *"… is a CI capability, not a task grant"* (never blaming policy), shows `may_be_refused` hints on repo-scoped rules, refreshes scope sets/methods on every open, and links to `/next/settings/capabilities#capability-policy`.
- **API scope sets (`/next/settings/capabilities` & `/settings/capabilities#api-scope-sets`):**
  - Created `walk/next-set` (`tasks:read,ci:read`), confirmed it appears immediately in `/next/`'s `New task` grants picker (`api:walk/next-set`), and added a confirmed `Remove` button (`POST /api/api-scope-sets/delete`) for custom scope sets on `/next/settings/capabilities`.
  - On classic `#api-scope-sets`, attached `zz/walk-held` to [Task #1](/tasks/1) and verified `Remove` prompts with the holder (`1 grant holds https://grain.dev/capabilities/api#zz/walk-held … Task #1 "Add a dark mode toggle" on acme/widgets`).

### 5. Deployment Credentials & Stored Secrets (`#credentials`, `#other-secrets`)
- **Classic redirects & header clearance:** `/settings/credentials`, `/settings/legacy-capabilities`, `/admin/integrations`, and `/system/integrations` all land on `/settings/capabilities#credentials` at `cardTop=89` (`headerBottom=77`); `/settings/secrets` lands on `#other-secrets` at `cardTop=253` (`headerBottom=77`).
- **Secret masking, listing & deletion on `/next/settings/capabilities`:**
  - On base `/next/settings/capabilities`, `[data-testid="input-secret-value"]` was an unmasked `<textarea>` with no `autocomplete="new-password"` and no table or `Delete` action for stored secrets (**Bug 3**, fixed below).
  - Re-tested on `/var/tmp/grain-fixed`: `Secret value` is `<input type="password" autocomplete="new-password">` inside `<form autocomplete="off">`; storing `walk-sec-fixed` renders a masked row (`••••••••`) excluding capability-server-owned secrets while retaining user and orphaned/retired secrets; clicking `Delete` confirms via `g-confirm-dialog` before `DELETE /api/secrets/{secret}`.
- **Role gating (`/next/settings/{capabilities,git}`):** Verified all 4 custom role combinations via `GET /api/config` stub:
  - Read-only: GitHub host disabled, Store secret disabled, Save policy disabled.
  - `settings:write` only: GitHub host **enabled**, Store secret disabled, Save policy disabled.
  - `secrets:write` only: GitHub host disabled, Store secret **enabled**, Save policy disabled.
  - `capabilities:admin` only: GitHub host disabled, Store secret disabled, Save policy **enabled**.

### 6. Hub (`/next/settings/registry`, `/next/settings/hub`, and `/settings/registry`)
- **Live & stubbed status chips:** Verified live `connected` pill and `Heartbeat now` (`POST /api/registry/heartbeat`), plus stubbed `Not configured` (`disabled`), `connecting`, `degraded`, and `error` states.
- **Fields & persistence (`/next/settings/registry`):** Edited `Display name` (`Walkthrough Grain Instance`), Hub URL, Entry name, and Auth mode (`Shared bearer token` vs `Workload OIDC` + `OIDC audience`), saved via `Save changes`, reloaded, and verified persistence in `GET /api/settings` (`hub`).
- **Scope overrides & locked mode (`/settings/registry` & `/next/settings/registry`):** Exercised `primary`, `secondary`, and `exclude repo` rules (`acme/widgets` with path prefix `docs/` persisted as `pathPrefix: "docs", role: "secondary"`, and cleared on remove). In external config-repo mode, Hub fields are disabled on `/next/settings/registry` while the encrypted `Registration token` remains editable on classic `/settings/registry`.

### 7. Engine, CI, and Caches (`/next/settings/engine`, `/next/settings/ci`, `/next/settings/caches`)
- **Engine (`/next/settings/engine`):**
  - On base `/next/settings/engine`, `Poll interval` showed `placeholder="30s"` instead of Grain's `pollIntervalDefault` (`"10s"`), and `Max merges per repo branch` lacked its `"5"` placeholder (**Bug 4**, fixed below).
  - Saved `Poll interval` = `3s`, reloaded (`3s`), blanked and saved (`GET /api/settings` reset to `"10s"`), and verified `"banana"` is refused (`pollInterval: time: invalid duration "banana"`). Verified `Max worker agents` `-3` is refused (`maxWorkers must be at least 1`).
- **CI (`/next/settings/ci`):** Saved `CI jobs per run` = `6`, `CI runs kept` = `-5` (unbounded), `CI job cache (MiB)` = `4096`, `CI artifacts (MiB)` = `2048`, `CI capacity reserved after` = `3m` (normalized to `3m0s`), reloaded, and verified read-back; tested `Flush CI job cache` (`DELETE /api/ci/cache`).
- **Caches (`/next/caches` → `/next/settings/caches`):** Verified legacy `/next/caches` redirect, saved `Proxy cache max size` = `25` GiB and `Actions cache max size` = `15` GiB, reloaded and verified read-back, confirmed `-1` is refused (`packageCacheSizeGb must be 0 (unset) or at least 1`), and exercised flush actions.

### 8. Sandboxes (`/next/settings/sandboxes` and `/settings/sandboxes`)
- **Backend & Network pickers (`/next/settings/sandboxes`):**
  - On base `/next/settings/sandboxes`, `sandboxBackendOptions` hardcoded nonexistent values (`"docker"`, `"podman"`, `"local"`) omitting `"container"`, `"kubernetes-granule"`, `"none"` (`None (control plane only)`), and `sandboxBackendOptions[].reason`; and `networkOptions` hardcoded nonexistent `"egress"` and `"full"` while omitting `"open"`, causing `Save changes` to fail with `400 networkAccess must be "open" or "none", or empty for "open"` (**Bug 1**, fixed below).
  - Re-tested on `/var/tmp/grain-fixed`: `/next/settings/sandboxes` displays `Container — local container per run` (`container`) and `Open (internet egress)` (`open`); opening `Sandbox backend` shows `Granule microVM`, `Host directory`, and `None (control plane only)` disabled (`aria-disabled="true"`) with their server-provided reasons (`needs -disable-native-sandboxes at daemon startup`), skips disabled items on keyboard arrow navigation, and saves `none` ↔ `open` network access cleanly.
- **Validation, Overcommit, Disk floor, Kubernetes & Custom image:**
  - Verified `Session sandbox idle TTL` `5x` refusal (`unknown unit "x"`) and `45m` save; `Kubernetes namespace` `Bad_NS` refusal and `grain-walk` save; `Custom OCI sandbox image` `registry.example.test/walk:1` save and blank reset to `""` (`sandboxImageDefault` active).
  - On classic `/settings/sandboxes`, verified `Auto CPU minimum` `0.1` refusal by name, disk floor blank minimum reset (`placeholder="5120"`, resets `sandboxDiskFloorMinMb` to `5120`), Kubernetes pod priority classes (`grain-interactive`, `grain-agent`, `grain-ci`), and custom image contract v2 / mirror hints.

---

## Bugs Found & Fixed in This Run

1. **Bug 1 (P0, fixed): `/next/settings/sandboxes` offered invalid `sandboxBackend` and `networkAccess` enum values and hid disabled backend reasons**
   - **Cause:** `SettingsComputeComponent` hardcoded `["granule", "docker", "podman", "kubernetes", "local"]` and `["none", "egress", "full"]` instead of reading `GET /api/settings`'s `sandboxBackendOptions` (`container`, `granule`, `kubernetes`, `kubernetes-granule`, `host`, `none`) and `NetworkAccesses()` (`open`, `none`).
   - **Fix:** Extended `PillOption` (`ui-ng/src/app/shared/pill-select.component.ts`, `pill-select.css`) with `disabled?: boolean` and `hint?: string` (skipping disabled items on click and keyboard arrows), wired `SettingsComputeComponent.sandboxBackendOptions` to `state.settings()["sandboxBackendOptions"]` with labels and unavailable reasons, and updated `networkOptions` to `open` / `none`.
2. **Bug 2 (P0, fixed): `/next/settings/git` sent numeric GitHub App IDs (400 unmarshal error), lacked `Host kind` / `Known hosts` / `Proxy` fields, left `error` banners up after valid saves, and deleted connections without confirmation**
   - **Cause:** `SettingsCodeComponent.saveGitConnection` serialized `app.appId` and `app.installationId` as numbers (`AppCredentialInput` expects strings), omitted `kind`, `proxy`, and `ssh.knownHosts`, did not clear `state.error` on success, and called `DELETE` directly on `Remove`.
   - **Fix:** Added `Host kind` (`GitHub` vs `Plain git host`, disabling `GitHub App` on plain git), `Proxy (optional)`, and `Known hosts` (for `ssh`), serialized `appId`/`installationId` as strings, cleared `state.error` on save, and gated `Remove` behind `g-confirm-dialog`.
3. **Bug 3 (P1, fixed): `/next/settings/capabilities` exposed secret input in a plain `<textarea>`, deleted capability servers without confirmation, lacked scope-set/secret delete actions, and hid policy verdicts in `GrantsPickerComponent`**
   - **Fix:** Changed `Secret value` in `SettingsWorkComponent` to `<input type="password" autocomplete="new-password">` inside `<form autocomplete="off">`, added a masked non-server `Stored secrets` table with confirmed `Delete` (`DELETE /api/secrets/{secret}`), added confirmed `Remove` for custom capability servers and custom API scope sets (`POST /api/api-scope-sets/delete`), and updated `GrantsPickerComponent` to show `denied` and `ci_only` methods disabled with their exact `policyRefusal` hints, show `may_be_refused` hints, refresh on open, and link to `/next/settings/capabilities#capability-policy`.
4. **Bug 4 (P1, fixed): Legacy `/next/admin/integrations`, `/next/system/integrations`, and `/next/settings/capabilities/<id>` routes missed `Capabilities`, and `/next/settings/engine` showed `30s` instead of `10s` for `Poll interval`**
   - **Fix:** Added `integrations` and `settings/capabilities/:serverId` redirects in `ui-ng/src/app/app.routes.ts`, added fragment/alias scrolling in `SettingsPageComponent`, and bound `Poll interval` placeholder to `pollIntervalDefault` (`10s`) and `Max merges per repo branch` default/placeholder to `5` in `SettingsInstanceComponent`.

---

## Verification, Screenshots & Three-Angle Review

- **Screenshots rendered:** `ui/e2e/screenshot.mjs` at desktop (`1440×900`) and phone (`390×844`) in light and dark before (`--bin /var/tmp/grain-base`) and after (`--bin /var/tmp/grain-fixed`) across `/next/settings/git`, `/next/settings/capabilities`, `/next/settings/engine`, and `/next/settings/sandboxes`. Both `contact-sheet.png` images are attached to the task closing note.
- **Design review:** Verified zero raw hex colours in CSS (M3 tokens only), valid PNG dimensions and light-vs-dark differentiation across all 16 shots, zero native `<select>` elements across all 8 sections, `.s-card` reading measure `<= 1180px` at `2048×1200`, `24px` vertical card rhythm, zero horizontal overflow across `390×844`, `1024×860`, `1440×900`, and `2048×1200`, and `ui-design-book.design.md` updated for `PillOption` `disabled`/`hint`.
- **Functional review:** Verified end-to-end persistence across reloads on `/var/tmp/grain-fixed`, `pill-select` menu containment within `390px` phone width, keyboard arrow navigation skipping disabled options, `g-confirm-dialog` `Cancel`/`Escape` focus & no-op guarantees, and RBAC scope gating.
- **Adversarial review:** Verified zero global CSS class collisions (`pill-opt-copy`, `pill-opt-hint` scoped to `pill-select`), browser `Back` history clean across `/next/admin/integrations` redirect, server-owned capability secrets excluded from `otherSecrets` while orphaned secrets remain deletable, `TestCoreSaysNoGoogleTokens` (`go test ./tests/hygiene/...`) green, and `TestAngularRouteTableInSync` green.
- **Automated suites run:**
  - `ui-ng` unit tests (`npx vitest run`): 49/49 files passed.
  - `ui-ng` lint & format (`npm run lint`, `npm run format:check`): clean, 0 warnings.
  - `ui-ng` Playwright E2E: `e2e/next-connections-infrastructure-settings.spec.ts` (new, desktop + `@phone` + `@dark`), `e2e/next-full-system.spec.js`, `e2e/next-hosts-confirm.spec.js` — all passed.
  - Classic `ui` Playwright E2E: `capability-server-settings`, `hub-settings-layout`, `hostds-settings-layout`, `host-script-settings`, `k8s-priority-class-settings` — 14/14 passed.
