# Connections & Infrastructure Settings Walkthrough — 2026-11-19

- **Date:** 2026-11-19, 03:14–04:30 UTC.
- **Scope:** Settings → Git, Hosts, Capabilities (including capability server detail pages, capability policy, API scope sets, Deployment credentials, stored secrets, and legacy redirects), Hub (`registry`), Engine, CI, Caches, and Sandboxes.
- **Task:** task 7309, branch `grain/53549e42/task-7309-walk-through-settings-git`, base `main`.
- **Commit tested:** `17339dd8295ff22c8c1ad602e057ac1e587b7726` ("Merge pull request #1885"). The four fixes below were rebuilt and re-verified on top of it.
- **Outcome:**
  - **4 bugs fixed in `ui-ng/`** (F1–F4): Sandboxes leaving an unsaved invalid auto-overcommit bound in draft after switching back to Fixed mode; Deployment credentials' `gcp` links still pointing to `#capability-servers` instead of the `gcp` capability server page; Hosts offering `Rotate` on key-bound and shared-token rows and omitting host auth provenance; API scope sets lacking an `Edit` action and description input for declared sets.
  - **3 parity gaps proposed as follow-up tasks** (G1–G3): `/next/settings/capabilities/<id>` now exists (closing G2 from the 2026-11-16/17 walks), but lacks `ConfigMapList`/`ConfigMapConfigEditor` for `config-map`/`secret-map` (G1) and `HostScriptList` with per-script `Test` for `host-script` (G2); `/next/settings/cli` also does not yet render `hostExecutor.hostd` version/protocol, adopted runs, or missing-allowlist suggestions from `GET /api/settings/cli-run` (G3). Those three flows were exercised on the classic UI where they live today.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go `1.26.10`, Node `v20.19.0`, npm `10.8.2`. |
| Server | This repository's real `bin/grain demo` built from `./cmd/grain` over a throwaway embedded SQLite store, exercised with combinations of `-secrets-store`, `-host-executor=hostd`, `-host-executor=hostd-remote`, `-unconfigured`, `-external-config-repo=https://github.com/acme/grain-config.git`, and `-github-api=127.0.0.1:<port>`. |
| Frontend | Real built assets embedded into `bin/grain`: classic `ui/` (`pkg/ui/static`), Angular `ui-ng/` (`pkg/ui/static-next`), and `ui-hub/` (`pkg/hub/server/static`). |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900`, `390×844` (phone), `1024×860`, and `2048×1200`, in light and dark colour schemes, plus axe-core accessibility audits (`serious`/`critical` impact floor = 0). |
| Model | None. `/home/debian/.gemini-api-key` was not present in the sandbox, so `LOOK=1` was unavailable. |

## Stubs

Every real Grain server route, store write, frontend build, and browser gesture ran live unless the demo environment lacked an external system boundary:

1. **Loopback GitHub API stub (`-github-api=127.0.0.1:<port>`)** — a local HTTP server answering `/user`, `/user/repos`, and `/app/installations/...` with 200 OK or 401 Bad credentials so Settings → Git's real `Test` button verified both success (`Accepted as @walk-bot by github.com`, `✓ acme/widgets: read, push`) and 401 failure banners.
2. **Hosts connected / cleared-pauses / skipped-prune / rekey responses (`GET /api/hostds`, `GET /api/hostds/pending`, `DELETE /api/hostds/{name}`, `POST /api/hostds/delete-revoked`, `POST /api/hostds/pending/{id}/reject`)** — in the exploratory script only, to exercise connected remote host metadata (`from <addr> · as <principal> · <build>`), rekey pending requests (`rekey: ws-keybound`), shared-token rows, cleared usage-limit pauses, and skipped hosts during `Delete revoked`, which the offline demo rows do not produce on their own.
3. **Capabilities retired grants, away-host placement, and Figma test (`GET /api/capability-policy` `catalogue.inProcess`, `GET /api/capability-servers` `needs-hostd:<host>`, `POST /api/capability-servers/figma-test`)** — in the exploratory script only, because the demo configures no retired `in_process` entries, its local demo host executor cannot route a placed capability server to an offline remote host, and there is no live Figma API token.
4. **Hub non-connected states (`GET /api/registry`)** — in the exploratory script only, patching the real demo snapshot with `enabled: false` (`Not configured`), `state: "connecting"`, `state: "degraded"` + `scopeConflicts`, and `state: "error"` + `lastError` so every status chip and alert was exercised (`grain demo` reports `connected` by default).
5. **Sandboxes `"none"` backend availability (`GET /api/settings`)** — in the exploratory script only, flipping `sandboxBackends` for `"none"` to `available: true` and `sandboxBackend: "none"` to observe the `"None (control plane only)"` explanation text, which is otherwise disabled because the demo was not started with `-disable-native-sandboxes`.
6. **Host agent adopted process (`GET /api/settings/cli-run`)** — in the exploratory script only, adding a `processes` entry with `detached: true, adopted: true` onto the real `-host-executor=hostd` payload so classic `CLIRunLocations` rendered its adopted-run line.
7. **Secrets role gating (`GET /api/whoami`, `GET /api/secrets`)** — exercised both via Playwright route interception in existing `next-capabilities-parity.spec.js` and exploratory probing.

All exploratory scripts lived under `/tmp/walk/` and screenshots under `/tmp/screenshots/{before,after}/` (never committed).

## Steps and outcomes

Every section in scope exists under `/next/` (`ui-ng/src/app/settings/settings-sections.ts` and `ui-ng/src/app/app.routes.ts`), including `/next/settings/capabilities/<encoded-id>`. Only the three sub-features noted as G1–G3 below required dropping to classic `/` to exercise.

### 1. Git — `/next/settings/git` (and `/next/repos`, New task)

| Step | UI | Outcome |
| --- | --- | --- |
| Initial table, default `github.com` row, axe audit | `/next/` | ✅ Renders `default · github.com · GitHub · All repos on host · pat` with `Test` and `Remove`; 0 console/page errors, 0 axe violations. |
| Per-row `Test` against loopback `-github-api` stub (200 & 401) | `/next/` | ✅ 200 renders `Accepted as @walk-bot by github.com` + `✓ acme/widgets: read, push`; 401 renders `GitHub rejected the token (bad credentials)…` with `Dismiss`. |
| Plain git host (`git.example.test`) + token; GitHub App disabled check | `/next/` | ✅ Switching Host kind to `Plain git host` disables `GitHub App (app)` with reason `"Needs a GitHub host: a plain git host has no App API"`. Save persists across reload. |
| Remove asks first (Cancel keeps row, Confirm deletes + cleans secret) | `/next/` | ✅ In-page confirm (no native `window.confirm`); Cancel preserves row, Confirm deletes row and removes auto-generated credential from `/api/github-tokens` across reload. |
| Plain git host + SSH key (`known_hosts` required, invalid refusal, banner clears on save) | `/next/` | ✅ Save stays disabled with blank `known_hosts`; invalid `known_hosts` returns 400 refusal banner (`known_hosts pins no host key…`); fixing with valid `ssh-ed25519` line saves cleanly, clears the refusal banner, and persists across reload. |
| Nameless connections distinguishable in `Reuse authentication from` | `/next/` | ✅ Two inline plain-host connections on the same host with different scopes render distinct labels (`<host> · git host · <scope> (pat) · stored as grain-git-…`) under `Link stored credential`. |
| GitHub host (`ghe.example.test`) as token and as GitHub App with scope & proxy | `/next/` | ✅ Both rows save with `org-a/repo-1` / `org-a/repo-2` and `via proxy http://proxy.example.test:3128`, persist across reload, and read back from `GET /api/deployment/git-connections`. |
| GitHub host pointer link (`Settings → Capabilities`) | `/next/` | ✅ Navigates to `/next/settings/capabilities#credentials` with card top `y=136` clear of the 56px header. |
| No-git-connection notice (`grain demo -unconfigured`) on Repos & New task | `/next/` | ✅ Both `/next/repos` and the New task sheet name GitHub tokens, GitHub Apps, tokens, SSH keys, and ambient logins, and link directly to `/next/settings/git`. |

### 2. Hosts — `/next/settings/hosts` and `/next/settings/hostds`

| Step | UI | Outcome |
| --- | --- | --- |
| Route alias `/next/settings/hosts` and `/next/settings/hostds` | `/next/` | ✅ Both open Settings → Hosts with 0 console/page errors and 0 axe violations. |
| Invite hosts `--join` snippet, Show/Hide code, switching codes | `/next/` | ✅ Displays `--join <code...>` command; switching `select-invite-code` swaps the snippet; `Show code`/`Hide code` toggles raw code display. |
| New enrollment code (expiry `24h`, max uses `3`, capacity `6`, labels `env=walk,tier=fast`, allowed principals) | `/next/` | ✅ Persists across reload with `0 / 3`, expiry date, `capacity 6`, labels, and `allow` chips. |
| Rotate code and Revoke code | `/next/` | ✅ Both prompt via `<g-confirm-dialog>`; Rotate changes the invite snippet token; Revoke marks the code `revoked` with `No actions` across reload. |
| Pending host requests (verified enrollee, no pairing code, fingerprint, editable name/labels/capacity, Accept & Reject, rekey badge) | `/next/` | ✅ Seeded `devbox-carol.example.com` shows `carol@example.com`, `SHA256:…` fingerprint, no pairing code (`NWW9-DVHV` omitted), prefilled editable name/labels/capacity; Accept persists as `carol-walkbox` across reload; stubbed rekey displays `rekey: ws-keybound` and Reject works. |
| Hosts table (`local`, key-bound, legacy token, shared rows; connected addr/principal/build; Edit/blank capacity/error dismiss; Rename; Revoke; Delete + cleared pauses; Delete revoked + skipped) | `/next/` | ⚠️ **Bug F3 found and fixed:** before the fix, key-bound and shared hosts showed no auth provenance and offered `Rotate` (which minted a legacy static token on an Ed25519-enrolled host). After fix: key-bound rows display short `SHA256:… · via <code>` (full fingerprint in `title`) without `Rotate`; legacy rows show `token ghd_…` with `Rotate`; shared rows show `Shared token (deprecated)` with no row actions. Connected hosts display `from <addr> · as <principal> · <build>`; Edit clears blanked capacity and dismisses resolved label errors; Rename, Revoke, Delete (with `clearedPauses` banner), and `Delete revoked` (with `Skipped <name>: <reason>`) all work. |
| Legacy per-host token registration | `/next/` | ✅ Registering `legacy-walk` displays the one-time `ghd_…` token and install command, and persists the row across reload. |

### 3. Capabilities, Capability Server Detail, Policy, Scope Sets & Deployment Credentials

| Step | UI | Outcome |
| --- | --- | --- |
| `/next/settings/capability-servers` alias & server list order | `/next/` | ✅ Lists built-in child & in-process servers (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`, `api`, `github`, `grain`, `model-proxy`, `release`, `repo`) before custom servers (`cloud`). Built-in child servers are disable-only (no `Remove`); in-process servers offer no disable/remove. |
| Disable flips state chip to `disabled` without reload; `needs-configuring`; `needs-hostd` away-host warning | `/next/` | ✅ Clicking `Disable` on `figma` flips `needs-configuring` → `disabled` immediately without reload, persists across reload, and `Enable` restores `needs-configuring`. Away-host server (`needs-hostd:cloudtop-alice`) displays `needs host cloudtop-alice` chip and banner pointing to Settings → Hosts. |
| Retired in-process grant deprecation notices | `/next/` | ✅ Stubbing `catalogue.inProcess` renders deprecation notices for `gcp-key`, `gemini-key`, `grain-ci`, `bootstrap-playbooks`, `github-sandbox`, plus `claude-proxy`/`openai-proxy`/`gemini-proxy` (for `model-proxy`) and `image-signing`/`registry-push`/`package-publish` (for `release`). |
| `(i)` info popovers by click and keyboard (`Enter`/`Space`/`Escape`) | `/next/` | ✅ Opened with click, `Enter`, and `Space`; closed cleanly with `Escape` on `/next/settings/capabilities/<id>`. |
| Schema form, `Host` placement on `run: host`, rename with confirm, test, bind secret, secrets stay off Other secrets & cleaned up on Remove | `/next/` | ✅ Configuring `cloud` stores `capability-server-…` under `Used by capability servers` (`No other secrets set.`); removing `cloud` confirms `"Its own secrets go with it"` and deletes the secret (`GET /api/secrets` returns `[]`). Throwaway server supports binding header secret, switching to `stdio` + `run: host` + host picker, renaming after confirm, and `Test`. |
| `gcp` keyless token vs service-account key mode | `/next/` | ✅ Switching `credential` between `key (service-account key pool)` and `token (recommended, no keys)` toggles the explanatory note and hides key-only `pool_max` in keyless mode. |
| `figma` account `Test` and OAuth `Connect Figma` callback instructions | `/next/` | ✅ Saving an OAuth account reveals `Test`, `Connect Figma`, and the callback URI instruction (`Register http://…/settings/capabilities as a callback URL…`); stubbed `Test` displays `Signed in to Figma as designer <designer@example.com>.`. |
| Holder links (`Granted by`) | `/next/` | ✅ `github` server lists `Deployment defaults` (`/next/settings/tasks`) and holding tasks (`Task #1` → `/tasks/1`, matching `PROMPT.md`'s rule to keep task links on `/tasks/<id>` until `/next/` is default); following the link opens the task page. |
| `config-map` & `secret-map` file placements | Classic `/` (gap **G1** on `/next/`) | ⚠️ **Gap G1 proposed:** `/next/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fconfig-map` lacks `ConfigMapList` and `ConfigMapConfigEditor` (read-only view hides saved maps; edit view exposes raw JSON textareas and a single password input for `secret_values`). Exercised on classic `/settings/capabilities/…`: created `walk-cm` (`note.txt`) and `walk-sm` (`api.token`), saved, reloaded, verified `Configured maps (1 config map, 1 secret map)` with `api.token: set`. |
| `host-script` host scripts & per-script `Test` | Classic `/` (gap **G2** on `/next/`) | ⚠️ **Gap G2 proposed:** `/next/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fhost-script` lacks `HostScriptList` and the per-script `Test` runner. Exercised on classic `/settings/capabilities/…`: `ops` script displayed (`timeout 60s`), passed argument `hello walkthrough`, clicked `Run ops` → `exit 0 · 2 ms · stdout: hello walkthrough`. |
| Host agent version, adopted runs, and allowlist suggestions (`GET /api/settings/cli-run`) | Classic `/` (gap **G3** on `/next/`) | ⚠️ **Gap G3 proposed:** `/next/settings/cli` renders only framework dropdowns and drops `hostExecutor.hostd` diagnostics. Exercised on classic `/settings/models` (`CLIRunLocations`): displayed build `v3.14.0-dev.479+17339dd`, `host agent protocol 4`, offered `capability corp-search, cli agy`, adopted process (`pid 4242, running, … adopted by this daemon`), and allowlist hints. |
| Capability policy (`#capability-policy`) + pickers (New task, Templates, Schedules, Tasks, Repo defaults) + `repo#read` CI-only | `/next/` | ✅ Deny `github#*` and allow `gcp#*` on `other/repo` persist across reload. New task and Task detail show `denied by policy` badges, refusal reasons, and links to `/next/settings/capabilities#capability-policy`; `repo#read` states `"CI only: not a grant for agent tasks…"` without blaming policy or linking to `#capability-policy`. Templates, Schedules, and Repo CI pickers verified via `next-capability-policy-pickers` & `next-processes-forms`. |
| API scope sets (`#api-scope-sets`): create, edit, appears in picker after startup, Remove asks first when held | `/next/` | ⚠️ **Bug F4 found and fixed:** before the fix, declared scope sets had no `Edit` button and no description input. After fix: declared sets display descriptions, offer `Edit` (locking name, prefilling description & scopes, offering `Cancel`, and preserving `includes`/`repos`/`deny`/`requiresPerson`/`deprecated`/`version` on update). Newly created `walk/live` appears immediately in New task's `API scope` picker; Remove on a held set confirms with holder name (`1 grant holds api:walk/triage… task Task holding walk/triage`). |
| Deployment credentials (`#credentials`, `#other-secrets`) & legacy redirects (`/settings/{credentials,legacy-capabilities,secrets}`, `/admin/integrations`, `/system/integrations`) | `/next/` | ⚠️ **Bug F2 found and fixed:** all 5 legacy redirects land on `#credentials` or `#other-secrets` clear of the 56px header (`cardTop=136`). Before the fix, `credentials-gcp-link` pointed to `#capability-servers` instead of the `gcp` server page, and the GCP project/SA hints lacked links. After fix: all three link directly to `/next/settings/capabilities/https:%2F%2Fgrain.dev%2Fcapabilities%2Fgcp`. Real `-secrets-store` `SecretsPanel` passed add, masked input (`type="password"`), replace, remove-with-confirm, retired capability secrets visibility, config-repo lock (`-external-config-repo` dims/disables settings while keeping secrets writable), and role gating (`settings:write` vs `secrets:write` independent of `capabilities:admin`). |

### 4. Hub — `/next/settings/registry`

| Step | UI | Outcome |
| --- | --- | --- |
| `#registry-status`: all 5 status chips, `Heartbeat now`, health summary, alerts, advertised scopes & origin chips | `/next/` | ✅ Verified `Not configured`, `Connecting`, `Connected`, `Degraded` (with scope conflict alert), and `Heartbeat error` (with `lastError` alert). `Heartbeat now` reported `"Heartbeat delivered… the hub reports this entry connected."` Advertised scopes showed `acme/widgets · / (whole repo) · primary · from repo config`. |
| Fields: Hub URL, Entry name, Credential source (`token` vs `oidc` + OIDC audience), Platform, Display name, Purpose, Listed toggle, encrypted Registration token (`grc_…`) | `/next/` | ✅ Switching to `Workload OIDC` enables `OIDC audience`; switching to `Shared bearer token` shows masked `grc_…` input (`type="password"`) that stores write-only (`set`) under `-secrets-store`. All fields persist across reload. |
| `#registry-scopes`: `Primary`, `Secondary`, `Exclude repo` with optional path prefixes | `/next/` | ✅ Added all three rules, saved, reloaded, verified persistence; removed first rule, saved, reloaded, verified removed. |
| Config-repo lock (`-external-config-repo`) | `/next/` | ✅ Dims/disables Hub URL, auth mode, scope overrides, and selects while keeping `Registration token (grc_…)` editable and savable. |

### 5. Engine — `/next/settings/engine`

| Step | UI | Outcome |
| --- | --- | --- |
| Poll interval (`10s` placeholder), Max worker agents (`10`), Max merges per repo branch (`5`) + sandbox commitment note | `/next/` | ✅ Out-of-range inputs refused (`pollInterval: time: invalid duration "notaduration"`, `maxMergesPerRepo must be at least 1`). Valid values (`15s`, `4`, `3`) persist across reload; clearing all three and saving resets cleanly to defaults (`10s`, `10`, `5`). |

### 6. CI (`/next/settings/ci`) and Caches (`/next/settings/caches`)

| Step | UI | Outcome |
| --- | --- | --- |
| CI: all 14 settings across CI queue, history, job cache, and artifacts | `/next/` | ✅ Edited all 14 inputs, saved, reloaded; 0 mismatches on readback (e.g., `2m` → `2m0s`, `6`, `2500`, `512`, `4096`). |
| Caches: proxy cache and Actions cache max sizes + fractional validation | `/next/` | ✅ Fractional `1.5` refused cleanly by field name (`packageCacheSizeGb must be a whole number, not 1.5`). Valid `12` / `8` persist across reload. |

### 7. Sandboxes — `/next/settings/sandboxes`

| Step | UI | Outcome |
| --- | --- | --- |
| Backend & size (`None (control plane only)` description, disabled backends with reasons), Network | `/next/` | ✅ Unavailable backends (`granule`, `host`, `none`) show disabled with exact startup/config reasons; when enabled, `None (control plane only)` renders its full control-plane explanation. Network offers `Open (full network)` and `None (isolated)`. |
| Session sandbox idle TTL | `/next/` | ✅ Invalid `bogus` refused; valid `2h` persists; blank resets to `30m0s`. |
| Sandbox budget: fixed overcommit, auto overcommit bounds (out-of-range refused by name), switching Auto → Fixed after invalid bound | `/next/` | ⚠️ **Bug F1 found and fixed:** typing `cpuMin = 999` in auto mode is refused by name (`sandboxOvercommit: sandbox overcommit cpuMin 999.00 is above cpuMax 6.00`), but switching back to `Fixed multiplier` left `cpuMin: 999` in `draft.sandboxOvercommit`, causing every subsequent Save on Sandboxes to fail with 400 even though the offending input was hidden. Fixed by resetting `sandboxOvercommit` from the saved server settings when leaving `auto` mode. |
| Disk floor (blank minimum resets to default) | `/next/` | ✅ `8192` saves and persists; clearing to blank and blurring resets to `5120`. |
| Kubernetes card (prefilled defaults, validation, pod priority classes) | `/next/` | ✅ Prefilled defaults and priority classes (`grain-interactive`, `grain-agent`, `grain-ci`) shown; invalid node selector `bad-no-equals` shows inline error (`Node selector entry "bad-no-equals" must be key=value.`) and disables Save; valid `pool=sandboxes` persists across reload. |
| Custom sandbox image card (contract, mirror hints, revert to default) | `/next/` | ✅ Displays `sandbox-k8s-base` contract and mirror guidance; saving `mirror.example.test/grainai/sandbox:walk` persists across reload; clearing reverts cleanly to the stamped default placeholder. |

## Findings

### Fixed in this task

1. **F1 — Sandboxes: hidden invalid auto-overcommit draft blocked saving after switching back to Fixed mode.**
   - **Reproduction:** On `/next/settings/sandboxes`, switch `Overcommit governor` to `Pressure-governed auto`, set `Auto CPU minimum (x)` to `999`, click `Save changes` (400 refusal), switch back to `Fixed multiplier`, edit `Disk floor minimum (MiB)` to `8192`, and click `Save changes`.
   - **Root cause:** `setOvercommitMode()` in `ui-ng/src/app/settings/settings-compute.component.ts` cleared client-side `fieldErrors` when leaving `"auto"`, but spread the dirty `this.state.val("sandboxOvercommit")` object rather than the saved `this.state.settings()["sandboxOvercommit"]`, keeping the hidden `cpuMin: 999` in the draft payload.
   - **Fix:** When `mode !== "auto"`, `setOvercommitMode()` now derives the new object from `this.state.settings()["sandboxOvercommit"]`, dropping unsaved auto-bound edits.
   - **Regression coverage:** `ui-ng/e2e/next-connections-walk-fixes.spec.ts`.

2. **F2 — Deployment credentials: `gcp` capability server links pointed to `#capability-servers` instead of the `gcp` server page.**
   - **Reproduction:** Open `/next/settings/capabilities#credentials` and click `gcp capability server's`.
   - **Root cause:** `credentials-gcp-link` in `ui-ng/src/app/settings/settings-work.component.ts` still targeted `[routerLink]="[]" fragment="capability-servers"` from before `/next/settings/capabilities/<id>` existed, and the `GCP project` / `GCP service account email` hints had no link to the `gcp` server.
   - **Fix:** Updated `credentials-gcp-link` and added `credentials-gcp-project-link` and `credentials-gcp-sa-link` using `serverPath("https://grain.dev/capabilities/gcp")` (matching classic `CredentialsSection.jsx`), and updated the old fragment assertion in `ui-ng/e2e/next-capabilities-parity.spec.js`.
   - **Regression coverage:** `ui-ng/e2e/next-connections-walk-fixes.spec.ts` and `ui-ng/e2e/next-capabilities-parity.spec.js`.

3. **F3 — Hosts: key-bound and shared-token rows omitted auth provenance and offered `Rotate`.**
   - **Reproduction:** Open `/next/settings/hosts`, accept a pending Ed25519 host request, and inspect its row in the Hosts table.
   - **Root cause:** `HostdRow` in `ui-ng/src/app/settings/settings-compute.component.ts` omitted `token_prefix` and `enrollment_code`, rendered no auth indicator, and rendered `Rotate` on every non-`local` host (including Ed25519 key-bound hosts and `shared` hosts), whereas classic `HostdsSection.jsx` distinguishes key-bound vs legacy vs shared auth, hides `Rotate` on key-bound hosts, and hides row actions on `shared` hosts.
   - **Fix:** Added exported `hostdAuthSummary()` showing short `SHA256:… · via <code>` (full fingerprint in `title`), `token <prefix>…`, or `Shared token (deprecated)` stacked under the host name; hid `Rotate` when `h.key_fingerprint` is present and hid row actions when `h.shared` is true.
   - **Regression coverage:** `ui-ng/src/app/settings/settings-hosts-git.spec.ts` and `ui-ng/e2e/next-connections-walk-fixes.spec.ts` (desktop, phone, dark + bounding-box & axe checks).

4. **F4 — API scope sets: declared sets could not be edited in place or given descriptions.**
   - **Reproduction:** Open `/next/settings/capabilities#api-scope-sets` and inspect a declared (`source === "config"`) row.
   - **Root cause:** `ui-ng/src/app/settings/settings-work.component.ts` rendered only `Remove` on declared rows and exposed only name + scopes inputs, forcing users to retype the exact set name and silently dropping `description`, `includes`, `repos`, `deny`, `requiresPerson`, `deprecated`, and `version` when updating a set.
   - **Fix:** Added `Edit` beside `Remove` on declared rows, displayed `description` under the set name, added `input-scope-set-description` and `cancel-scope-set-btn`, locked `input-scope-set-name` while editing, updated placeholder to `ops/ci-readonly`, preserved existing declaration fields on update, and cancelled active edit if that set is removed.
   - **Regression coverage:** `ui-ng/e2e/next-connections-walk-fixes.spec.ts` (desktop, phone, dark + bounding-box & axe checks).

### Proposed as follow-up tasks (`repo: "ai-on-gke/grain"`, `branch: "main"`)

- **G1 (`port-config-map-ui-ng`):** Port `ConfigMapList` and `ConfigMapConfigEditor` to `/next/settings/capabilities/<id>` for the `config-map` server.
- **G2 (`port-host-script-list-ui-ng`):** Port `HostScriptList` and per-script `Test` runner (`POST /api/capability-servers/test-script`) to `/next/settings/capabilities/<id>` for the `host-script` server.
- **G3 (`port-cli-run-hostd-status-ui-ng`):** Port host agent version/protocol, offered entries, running/adopted processes, and missing-allowlist suggestions from classic `CLIRunLocations.jsx` to `/next/settings/cli`.

## Tests run

- **Go hygiene:** `go test ./tests/hygiene` — passed.
- **Angular unit, lint & formatting (`ui-ng/`):**
  - `npm run format:check` — passed.
  - `npm run lint` — passed.
  - `npm test` (vitest) — 76/76 test files, 828/828 tests passed.
- **Playwright E2E (`ui-ng/`):**
  - `e2e/next-connections-walk-fixes.spec.ts` across `desktop`, `phone`, and `dark` — 8/8 passed.
  - `e2e/next-full-system.spec.js` (desktop light/dark, phone light/dark, colour contrast) + 13 existing settings specs (`next-capabilities-parity`, `next-capability-server-page`, `next-capability-policy-pickers`, `next-connections-infra-settings`, `next-connections-settings-walk`, `next-git-settings-port`, `next-hosts-git-parity`, `next-hosts-invite`, `next-hosts-confirm`, `next-hosts-update`, `next-hub-sandboxes-parity`, `next-settings-connections`, `next-settings-walkthrough`) — all passed (108/108 after fixing a testid selector in the new spec).
- **Layout & visual verification:**
  - Captured before and after contact sheets (`ui/e2e/screenshot.mjs`, desktop `1440×900` and phone `390×844`, light and dark) for `/next/settings/hosts`, `/next/settings/capabilities#api-scope-sets`, `/next/settings/capabilities#credentials`, and `/next/settings/sandboxes`, attached to the closing note / PR.
  - Audited bounding boxes and document/stage overflow across 4 breakpoints (`390×844`, `1024×860`, `1440×900`, `2048×1200`) × 2 themes (`light`, `dark`) with 0 horizontal overflow on all modified cards and 0 axe violations. (`LOOK=1` could not run without `/home/debian/.gemini-api-key`.)
