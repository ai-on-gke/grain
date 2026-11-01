# Walkthrough Report: Tabbed `/settings/capabilities`, Kubernetes-Style `config-map` & Secret Maps, `gchat`, `workspace` & `gcp` Ambient Admin Default (`docs/test-plans/2026-10-26.md` §5)

- **Date:** 2026-10-26
- **Task:** [Task #1203](/tasks/1203) (Origin: [Task #1140](/tasks/1140))
- **Branch:** `grain/049f1e09/task-1203`
- **Test Plan Reference:** `docs/test-plans/2026-10-26.md` §5 (Tabbed `/settings/capabilities` UI, Kubernetes-Style `config-map` & Secret Maps, `workspace` & `gchat` Capability Servers, and `gcp` Ambient Admin Default)
- **Environment:**
  - OS / Kernel: Debian GNU/Linux sandbox (`linux/amd64`)
  - Binary: `grain` built from a wrapper distribution's `cmd/grain` (with embedded `pkg/ui/static` bundle from `ui/`)
  - Runtime Mode: `grain demo -open=false -addr 127.0.0.1:0 -secrets-store -github-sim -fake-ci`
  - Browser Automation: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at desktop (`1440x900` / `1280x720`) and phone (`390x844`) viewports

---

## Executive Summary

Executed the end-to-end walkthrough and verification suite for `docs/test-plans/2026-10-26.md` §5 across all three scopes:

1. **Scope 1 — Split `[/settings/capabilities](/settings/capabilities)` horizontal server tabs & live capability method propagation (`bf30d21fd`, PR #4741; `78a2c3d4a`, PR #4824; `97774207f`, PR #4803; `896d1893e`, PR #4874; `cd582719f`; `b52d45ed5`):** **PASS** (after fixing 4 bugs discovered during live verification).
2. **Scope 2 — Kubernetes-style `config-map` & `secrets` maps (`886e16bea`, PR #4740; `e1080c4e8`, PR #4758; `e991ba904`, PR #4808):** **PASS**.
3. **Scope 3 — `gchat` (`9a0e45549`, `aa1204a7c`, PR #4864), `workspace` (`1d5c8790b`, `e735175b3`, PR #4841), and `gcp` ambient admin default (`c626f9fb4`, `a518b1b2e`, PR #4739):** **PASS**.

During the live walkthrough against `grain demo -secrets-store -github-sim -fake-ci`, **four bugs** were discovered, reproduced deterministically, fixed in this branch, and covered by unit and Playwright E2E regression tests.

---

## Bugs Discovered & Fixed

### Bug 1: Built-in stdio capability servers (`Command: "grain"`) failed to attach when `grain` was invoked by path without `"grain"` on `$PATH`
- **Symptom:** When starting `/tmp/grain demo` or Playwright's E2E worker binary (`/tmp/grain-e2e-.../grain demo`) on a machine where `"grain"` is not in `$PATH`, attaching any built-in stdio capability server (`config-map`, `gchat`, `gcp`, `git`, `host-script`, `model-proxy`) failed with:
  `starting capability server grain: exec: "grain": executable file not found in $PATH`.
- **Root Cause:** `dialStdio` in `pkg/capability/protocol/client.go` passed `cfg.Command` (`"grain"`) directly to `exec.CommandContext`, which relies on `exec.LookPath("grain")`.
- **Fix:** Added `resolveBuiltinExecutable(bin string, executable func() (string, error)) string` in `pkg/capability/protocol/client.go`. When `bin == "grain"` is not found in `$PATH`, it falls back to `os.Executable()` if the running binary's basename is `grain` or `grain-*` (excluding `*.test` test binaries).
- **Regression Test:** `TestResolveBuiltinExecutableFallsBackToRunningGrainBinary` in `pkg/capability/protocol/protocol_test.go`.

### Bug 2: `host-script` bound TCP data-plane port `127.0.0.1:9733` unconditionally before any scripts were configured
- **Symptom:** When `EnsureBuiltins` attached `host-script` across multiple `grain` processes/workers on the same machine, unconfigured `host-script` instances collided on `127.0.0.1:9733` (`bind: address already in use`), causing `host-script` to enter `failed` state instead of `needs-config`. (Commit `aa1204a7c` had fixed this for `gchat` on port `4251`, but `host-script` still bound eagerly inside `NewHostScript`.)
- **Root Cause:** `NewHostScript` in `pkg/capability/intree/hostscript.go` called `s.listen(cfg.Listen, h)` unconditionally at server construction even when `len(cfg.Scripts) == 0`.
- **Fix:** Deferred `s.listen(cfg.Listen, h)` in `pkg/capability/intree/hostscript.go` until at least one script is configured (`m != nil` in `NewHostScript` or in the `configurable` callback).
- **Regression Test:** `TestHostScriptBindsDataPlaneOnlyOnceAScriptIsConfigured` in `pkg/capability/intree/hostscript_test.go`.

### Bug 3: Built-in `gchat`, `gcp`, and `host-script` tabs lacked fallback `ConfigSchema` and `needs-config` state before first attach
- **Symptom:** On a fresh `grain demo` (where `stored.MCPServers` is not pre-populated with built-ins until the first `PUT /api/capability-servers` write), `ListCapabilityServers` and `capabilityConfigSchema` in `pkg/ui/capability_servers.go` only provided a fallback schema (`intree.ConfigMapConfigSchema()`) for `sc.Builtin == "config-map"`. Consequently, `gchat`, `gcp`, and `host-script` tabs rendered in state `"unknown"` with **no `Configure` button**, and `SetCapabilityServer` refused configuration saves with `"has not told grain its config schema yet: test it first"`.
- **Root Cause:** `ListCapabilityServers` and `capabilityConfigSchema` hardcoded `sc.Builtin == "config-map"` instead of resolving operator schemas for all configurable in-tree built-in servers.
- **Fix:** Added `intree.BuiltinConfigSchema(name string) map[string]any` in `pkg/capability/intree/builtins.go` returning the normalized operator schema (`protocol.OperatorSchema`) for `"config-map"`, `"gchat"`, `"gcp"`, and `"host-script"`, and wired it into `ListCapabilityServers` and `capabilityConfigSchema` in `pkg/ui/capability_servers.go`.
- **Regression Test:** Updated `TestBuiltinCapabilityServersAreListedDisableOnlyAndGrantNothing` in `pkg/ui/capability_servers_test.go` and `lists the built-in capability servers first as tabs, badged and disable-only` in `ui/e2e/capability-server-settings.spec.js`.

### Bug 4: Saving configuration on a built-in server left the UI card stuck on `"needs configuring"` instead of polling until `"attached"`
- **Symptom:** After configuring `config-map` or `gchat` in `[/settings/capabilities](/settings/capabilities)` and clicking `Save configuration`, the backend reconciler attached the server within ~20ms, but the UI card remained stuck on `"needs configuring"` (and did not call `invalidateCapabilityMethods()` once the server finished attaching).
- **Root Cause:** `handleSetCapabilityServer` wakes the reconciler asynchronously (`s.wakeReconciler()`) and immediately responds with `ListCapabilityServers`. At that instant, the reconciler has not yet finished `Registry.Sync`, so:
  1. `Registry.Statuses()` still returned the pre-update `NeedsConfig` status for an entry whose pending config in `r.pending` had changed via `r.Update(...)`.
  2. `ListCapabilityServers` synthesized `State: servers.NeedsConfig` when `statuses[id]` was not yet populated even when `len(sc.Config) > 0`.
  Because `s.status.state` was `"needs-config"` rather than `"pending"`, `CapabilityServersPanel.jsx`'s `pending` check (`!s.status || s.status.state === "pending"`) evaluated to `false` and never scheduled `refresh()`.
- **Fix:**
  1. Updated `Registry.Statuses()` in `pkg/capability/servers/servers.go` to report `State: Pending` when `r.updated` is true and `r.pending[id]` differs from `e.cfg` (and is not disabled).
  2. Updated `ListCapabilityServers` in `pkg/ui/capability_servers.go` to report `State: servers.Pending` when `statuses[id]` is not yet populated and `len(sc.Config) > 0`.
- **Regression Test:** `TestStatusesReportsPendingWhenUpdatedConfigAwaitsSync` in `pkg/capability/servers/servers_test.go` and `configures the built-in config-map server with config maps and secret maps` in `ui/e2e/capability-server-settings.spec.js`.

---

## Detailed Walkthrough Results

### 1. Scope 1: Split `[/settings/capabilities](/settings/capabilities)` Horizontal Server Tabs & Live Capability Propagation

| Check | Viewport | Action / Verification | Result |
| :--- | :--- | :--- | :--- |
| Horizontal server tab strip ordering & badges | `1440x900` | Navigated to `[/settings/capabilities](/settings/capabilities)`. Verified 10 tabs in `role="tablist"`: 7 built-in tabs first (`workspace`, `config-map`, `gchat`, `gcp`, `git`, `host-script`, `model-proxy`, each badged `built-in` with no `Remove` button), followed by 2 custom demo servers (`capabilities.demo.grain.example/cloud`, `capabilities.demo.grain.example/gcp`, badged `custom`) and `+ Add server`. | **PASS** |
| Configurable built-in initial state | `1440x900` | Clicked `config-map`, `gchat`, `gcp`, and `host-script` tabs; verified each shows state `"needs configuring"` and a visible `Configure` button. | **PASS** |
| Desktop ↔ Phone viewport resize preservation (`cd582719f`, `b52d45ed5`) | `1440x900` → `390x844` → `1440x900` | Selected `gchat` tab, opened `Configure`, typed `https://grain.example.com` into `grain_url`, resized to `390x844`, and verified `aria-selected="true"`, open form, and input value are preserved with zero horizontal overflow (`cardBox.x = 25`, `cardBox.width = 340` within `390px`). | **PASS** |
| Split `[/settings/credentials](/settings/credentials)` ("Legacy capabilities") | `1440x900` & `390x844` | Navigated to `[/settings/credentials](/settings/credentials)`; verified `"Legacy capabilities"` heading renders cleanly at desktop and phone widths without the capability server tablist (`credsHasServerTablist: false`). | **PASS** |
| Live capability method propagation (`78a2c3d4a`) | `1440x900` | Configured `config-map` in `[/settings/capabilities](/settings/capabilities)` and saved; card transitioned to `"attached"`. Clicked `+ New task` without reloading the page, opened `"Grant a server method…"`, and verified `config-map` was immediately listed and grantable (`[data-grant^="https://grain.dev/capabilities/config-map#config-map"]`). | **PASS** |
| Unready capability remains visible when unticked (`896d1893e`) | `1440x900` | In `New task`, opened the `Capabilities` multi-select combobox, unticked `gcp-key` (`aria-selected="true"` → `"false"`), confirmed the option stayed visible in the dropdown (`isVisible: true`), and re-ticked it (`aria-selected="true"`). | **PASS** |

---

### 2. Scope 2: Kubernetes-Style `config-map` & `secrets` Maps

| Check | Action / Verification | Result |
| :--- | :--- | :--- |
| Multi-file `config_maps` map creation (`886e16bea`) | In `[/settings/capabilities](/settings/capabilities)` → `config-map` → `Configure`, created map `app-conf` with 3 files: `env.conf` (default path `/home/debian/.config-map/app-conf/env.conf`, mode `0644`), `run.sh` (`Executable` checked → mode `0755`), and `custom.sh` (`Path override` `/home/debian/bin/custom.sh`, `Executable` checked → mode `0755`). Verified alignment of `Key` and `Remove file` controls on desktop (`Δy < 8px`). | **PASS** |
| Multi-key `secrets` map creation | Created secret map `sec-map` with keys `key-a` (`val-a-initial`) and `key-b` (`val-b-initial`) and clicked `Save configuration`. Verified `GET /api/capability-servers` stored `secret_values` as `$secret:capability-server-1f6d92c4/secret_values`, `configuredSecretKeys: ["sec-map/key-a", "sec-map/key-b"]`, and leaked no plaintext secret values. | **PASS** |
| Single-key secret rotation (`e1080c4e8`) | Re-opened `Configure`, confirmed both `secret-state-sec-map/key-a` and `secret-state-sec-map/key-b` chips showed `"set"`, entered a new value only for `key-a` (`val-a-rotated`) while leaving `key-b` blank, saved, and verified `configuredSecretKeys` retained both `["sec-map/key-a", "sec-map/key-b"]`. | **PASS** |
| Secret key removal & purge (`e1080c4e8`) | Re-opened `Configure`, clicked `Remove file` on `key-b`, saved, and verified `key-b` was purged from `configuredSecretKeys` (`["sec-map/key-a"]`). | **PASS** |
| Duplicate target path refusal | `POST /api/capability-servers/test` with two keys targeting `/home/debian/same.txt` was refused with: `config-map "dup-map" key "b.txt": path "/home/debian/same.txt" is already placed by dup-map/a.txt`. | **PASS** |
| Reserved sandbox path refusal | `POST /api/capability-servers/test` targeting `/home/debian/.git-credentials` was refused with: `config-map "res-map" key "creds.txt": path "/home/debian/.git-credentials" is in a reserved sandbox directory`. | **PASS** |
| Legacy single-file format refusal | `POST /api/capability-servers/test` with legacy single-file `config_maps` and `secrets` entries was refused with clear migration instructions (`uses the removed single-file format ... rewrite it as a map`). | **PASS** |

---

### 3. Scope 3: `gchat`, `workspace`, and `gcp` Ambient Admin Default

| Check | Action / Verification | Result |
| :--- | :--- | :--- |
| `gchat` lazy data-plane port binding (`aa1204a7c`) | Verified `127.0.0.1:4251` (`DefaultGChatPort`) was **not** listening before configuring a `gchat` space (`before: false`), and bound `127.0.0.1:4251` immediately once space `releases` was saved (`after: true`). | **PASS** |
| `gchat` UI configuration & `Test` (`97774207f`, `9a0e45549`) | Configured `gchat` in `[/settings/capabilities](/settings/capabilities)` with `dataPlaneToken`, `grain_url: "https://grain.example.com"`, `display_name: "Grain Bot"`, `per_attempt: 2`, webhook space `releases` (`display_name: "Release bot"`), and `webhook_urls`. Clicked `Test` and verified report: `space releases: webhook (the sender name is the webhook's own; cards show "Release bot")`. Verified non-`chat.googleapis.com` webhook URL is refused unless allowlisted in `WebhookHosts`. | **PASS** |
| `gchat#post` webhook payload & guardrails | Verified against local `httptest.NewServer` webhook listener (`go test -v ./pkg/capability/intree -run TestGChat`): card header `Release bot`, task subtitle and link button under `grain_url`, `threadKey` threading, `@all` and `<users/...>` mention rejections when disallowed, Markdown link rejection, `max_len` character bound, `per_attempt` and `per_hour` rate limits, and `test_post` delivery. | **PASS** |
| `workspace` capability server (`1d5c8790b`, `e735175b3`) | Verified `workspace` tab in `[/settings/capabilities](/settings/capabilities)` (`grain-workspace -listen 127.0.0.1:4250`, `built-in`, `host`, `mint_workspace`) and ran `go test -v ./google/capability/workspace/...` verifying `os.Root("monorepo")` confinement, symlink traversal rejection, and `read`/`write`/`edit`/`list`/`grep`/`diff`/`status` tools. | **PASS** |
| `gcp` deprecated/internal field hiding & `ambient` default admin (`c626f9fb4`) | On the `gcp` `Configure` form, verified deprecated fixed-pool fields (`service_accounts`, `project`, `keys_json`) and internal `_grain_*` fields have DOM count `0`. Added permission set `walkthrough-reader` (`demo-project`, `roles/storage.objectViewer`) without specifying an admin, clicked `Test`, and verified `admin default (ambient) is you@demo.grain.example; has every permission it needs` and `set walkthrough-reader (demo-project, roles/storage.objectViewer)`. | **PASS** |
| `gcp` explicit admin `role_ceiling` enforcement (`a518b1b2e`) | Configured explicit admin `capped-admin` with `role_ceiling: ["roles/storage.objectViewer"]` and permission set `too-broad` requesting `roles/owner`; verified refusal: `permission set "too-broad": roles/owner exceed(s) admin "capped-admin"'s role ceiling`. | **PASS** |

---

## Automated Test & Verification Summary

- `go test -v ./pkg/capability/protocol -run TestResolveBuiltinExecutableFallsBackToRunningGrainBinary`: **PASS**
- `go test -v ./pkg/capability/intree -run "TestHostScript|TestGChat|TestConfigMap"`: **PASS**
- `go test -v ./pkg/capability/servers/...`: **PASS**
- `go test -v ./pkg/capability/gcpsets/...`: **PASS**
- `go test -v ./google/capability/workspace/...`: **PASS**
- `go test ./pkg/ui/...`: **PASS**
- `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/capability-server-settings.spec.js e2e/gcp-permission-sets.spec.js e2e/host-script-settings.spec.js`: **6 passed (27.7s)**
