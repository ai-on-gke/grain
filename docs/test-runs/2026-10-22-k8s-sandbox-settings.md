# Test Run: Kubernetes Sandbox Settings in UI, CLI, and `grain_config` (`docs/test-plans/2026-10-22.md` §2)

Task 213 (`grain/049f1e09/task-213`): end-to-end walkthrough, defect audit, and automated test coverage for **§2 of `docs/test-plans/2026-10-22.md`: Kubernetes Sandbox Backends Default Availability and `grain_config` Persistence** (commit `4c7da2279`).

## Environment & Commit Tested

- **Base commit**: `f0c399a3b` (`candidate`), verifying commit `4c7da2279` (`feat(sandboxes): default Kubernetes backends to available and store Kubernetes settings in grain_config`)
- **Branch**: `grain/049f1e09/task-213`
- **Server & CLI**: Real `grain daemon` and `grain` CLI (`./bin/grain`) running against scratch SQLite data directories with `-sandbox-port-base=0` and no `-k8s-*` startup flags, serving the production frontend bundle built with `make frontend` (Vite `v6.4.3`).
- **Browser & Toolchain**: Playwright driving Chromium on Debian Bookworm (`Linux 6.1.0-43-amd64`), Go `1.26.2`, Node `v20.19.0`, Vitest `4.1.11`.
- **Viewports exercised**: Desktop (`1280×800`) and Phone (`390×844`), plus light/dark screenshot verification via `make ui-screenshots`.

---

## Walkthrough Execution & Results

### 1. Default Availability Without `-k8s-*` Startup Flags

| Check | Steps & Observations | Outcome |
|---|---|---|
| Startup without `-k8s-*` flags | Started a real `grain daemon` on a fresh scratch data directory (`-disable-native-sandboxes -sandbox-port-base=0`) with zero `-k8s-*` command-line flags. | Pass |
| `GET /api/settings` backend list | Confirmed `GET /api/settings` returned `sandboxBackend: "none"`, `sandboxBackends: ["kubernetes", "kubernetes-granule", "none"]` (matching `Config.availableSandboxBackends()` in `pkg/daemon/sandbox_backends.go`), and `pendingRestart: null` (empty). | Pass |
| Desktop (`1280×800`) & Phone (`390×844`) UI | Opened `/settings/sandboxes` in Playwright at both `1280×800` and `390×844`. Verified `[data-setting-id="sandboxBackend"] select` is visible, offers `Kubernetes (pod per run)` (`kubernetes`) and `Kubernetes Granule (VM pod per run)` (`kubernetes-granule`), and renders the `#kubernetes` (`Kubernetes`) settings card. | Pass |

### 2. Editing `kubernetes` and `kubernetes-granule` Fields in the UI & `pendingRestart` Tracking

| Check | Steps & Observations | Outcome |
|---|---|---|
| Selecting `kubernetes` & verifying default placeholders | Selected `kubernetes` in `[data-setting-id="sandboxBackend"] select` on `/settings/sandboxes`. Verified `GET /api/settings` and the DOM `placeholder` attributes match the constants in `pkg/model/config.go` (`model.DefaultK8s*`): `k8sNamespaceDefault: "grain"`, `k8sWorkspaceDefault: "/home/agent"` (for `kubernetes`), `k8sDeploymentDefault: "grain"`, `k8sContainerDefault: "grain"`, `k8sSandboxImageDefault: "ghcr.io/bwsalmon/grain/k8s-sandbox:latest"`, and `k8sGranuleImageDefault: "ghcr.io/bwsalmon/grain/guest:latest"`. *(Note: the test plan prose listed illustrative placeholder strings `"default"`, `"/workspace"`, and `"grain-daemon"`, whereas commit `4c7da2279` defines `"grain"` and `"/home/agent"` / `"/home/debian"` in `pkg/model/config.go` lines 848–855.)* | Pass |
| Populating all 12 `kubernetes` fields in `/settings/sandboxes` | Populated `k8sNamespace` (`"grain-sandboxes"`), `k8sKubectl` (path to stub `kubectl`), `k8sSandboxImage` (`"ghcr.io/bwsalmon/grain/k8s-sandbox:v2"`), `k8sWorkspace` (`"/workspace/agent"`), `k8sGitProxyHost` (`"10.244.0.10"`), `k8sSandboxService` (`"grain-sandbox"`), `k8sServiceAccount` (`"grain-sandbox-runner"`), `k8sNodeSelector` (`"pool=pods, zone=us-central1-b"`), `k8sDeployment` (`"grain-daemon-custom"`), `k8sContainer` (`"grain-custom"`), and checked `k8sPrivileged` (`true`) and `k8sNetworkPolicy` (`true`). Each text input saved on blur via `PUT /api/settings` and each checkbox saved immediately on change. | Pass |
| `GET /api/settings` persistence & `pendingRestart` banner | Confirmed `GET /api/settings` persisted all 12 Kubernetes values plus `sandboxBackend: "kubernetes"`, and `pendingRestart` contained all 13 changed keys (`sandboxBackend`, `k8sNamespace`, `k8sKubectl`, `k8sSandboxImage`, `k8sWorkspace`, `k8sGitProxyHost`, `k8sSandboxService`, `k8sServiceAccount`, `k8sNodeSelector`, `k8sPrivileged`, `k8sNetworkPolicy`, `k8sDeployment`, `k8sContainer`). Verified `/settings/sandboxes` displayed `[data-testid="settings-restart-chip"]` (`"Restart needed"`) and per-field `"restart to apply"` chips, and `/system` displayed `[data-testid="health-pending-restart"]` (`"Saved, not applied until restart: Sandbox backend, Kubernetes namespace, Kubectl binary, Kubernetes sandbox pod image, Kubernetes workspace directory, Kubernetes git & model proxy host, Kubernetes sandbox Service, Kubernetes sandbox ServiceAccount, Kubernetes node selector, Run Kubernetes sandbox containers privileged, Cluster CNI enforces NetworkPolicy for network: none runs, Kubernetes Deployment, Kubernetes container."`). | Pass |
| Switching to `kubernetes-granule` | Switched `[data-setting-id="sandboxBackend"] select` to `kubernetes-granule`. Verified `GET /api/settings` updated `k8sWorkspaceDefault` from `"/home/agent"` to `"/home/debian"` (`model.DefaultK8sGranuleWorkspace`) and updated the `k8sWorkspace` input `placeholder` attribute to `"/home/debian"`. Populated `k8sGranuleImage` (`"ghcr.io/bwsalmon/grain/guest:v2"`), blurred, and verified `GET /api/settings` persisted `k8sGranuleImage: "ghcr.io/bwsalmon/grain/guest:v2"` and added `"k8sGranuleImage"` to `pendingRestart` (all 14 restart-only keys tracked). | Pass |

### 3. `grain/config` Export and Schema Check

| Check | Steps & Observations | Outcome |
|---|---|---|
| `grain state -data-dir <dir> sync` export | Ran `grain state -data-dir <data-dir> sync` and inspected `<data-dir>/state-repo/tables/grain_config.json`. Confirmed all 13 `k8s_*` columns (`k8s_namespace`, `k8s_kubectl`, `k8s_sandbox_image`, `k8s_granule_image`, `k8s_workspace`, `k8s_git_proxy_host`, `k8s_sandbox_service`, `k8s_service_account`, `k8s_node_selector`, `k8s_privileged`, `k8s_network_policy`, `k8s_deployment`, `k8s_container`) and `sandbox_backend: "kubernetes-granule"` were exported with exact values. | Pass |
| `grain state check <state-repo>` | Ran `grain state check <data-dir>/state-repo` and verified it exited `0` with `schema: 28, which is what this build of grain knows` and `ok: grain can load this`. | Pass |

### 4. CLI `grain settings -k8s-*` and `grain/config` Round-Trip

| Check | Steps & Observations | Outcome |
|---|---|---|
| Updating via `grain settings -k8s-*` | Unchecked `k8sPrivileged` and `k8sNetworkPolicy` in `/settings/sandboxes`, then ran `grain -server <url> -token <token> settings -k8s-namespace=grain-cli-ns -k8s-node-selector=pool=sandboxes,zone=us-central1-a -k8s-sandbox-service-account=cli-sandbox-sa -k8s-network-policy=true -k8s-sandbox-privileged=true`. Verified the CLI output printed the updated Kubernetes settings (`k8s namespace: grain-cli-ns`, `k8s node selector: pool=sandboxes, zone=us-central1-a`, `k8s service account: cli-sandbox-sa`, `k8s network policy: true`, `k8s privileged: true`) and the `saved, but not running yet -- restart the daemon to apply` notice. | Pass |
| `grain/config` sync & check | Ran `grain state -data-dir <data-dir> sync` and `grain state check <data-dir>/state-repo`; verified `tables/grain_config.json` recorded `"k8s_namespace": "grain-cli-ns"`, `"k8s_node_selector": "pool=sandboxes,zone=us-central1-a"`, `"k8s_service_account": "cli-sandbox-sa"`, `"k8s_network_policy": 1`, and `"k8s_privileged": 1`. | Pass |
| Reloading `/settings/sandboxes` at Desktop (`1280×800`) & Phone (`390×844`) | Reloaded `/settings/sandboxes` in Playwright at both `1280×800` and `390×844` viewports and verified all 13 Kubernetes inputs/checkboxes and `sandboxBackend` (`kubernetes-granule`) rendered the updated values. | Pass |

### 5. Daemon Restart Adoption

| Check | Steps & Observations | Outcome |
|---|---|---|
| Restarting `grain daemon` on the same data directory | Cleared `k8sSandboxService` via `grain settings -k8s-sandbox-service=` (since the test daemon runs with `-sandbox-port-base=0` ephemeral ports rather than a fixed port block), stopped `grain daemon`, and started a new `grain daemon` on the same data directory with an executable stub `kubectl` on `PATH`. | Pass |
| Effective settings adoption & `pendingRestart` cleared | Confirmed the restarted daemon logged `Kubernetes granule sandboxing -- one privileged pod per run in namespace "grain-cli-ns" from image "ghcr.io/bwsalmon/grain/guest:v2"`, `GET /api/settings` returned all stored Kubernetes settings with `pendingRestart` empty (`null` / `[]`) and `sandboxBackends` containing `["kubernetes", "kubernetes-granule", "none"]`, and neither `/settings/sandboxes` (`[data-testid="settings-restart-chip"]`, `"restart to apply"`) nor `/system` (`[data-testid="health-pending-restart"]`) showed any pending-restart banner. | Pass |

---

## Bugs Found & Fixed

1. **Switching to `kubernetes` / `kubernetes-granule` and restarting dropped `-disable-native-sandboxes` (`"none"`) and `-host-sandboxes` (`"host"`) from `availableSandboxBackends()` (`pkg/daemon/config.go`, `pkg/daemon/sandbox_backends.go`)**:
   - **Cause**: When `grain daemon` started with `-disable-native-sandboxes` or `-host-sandboxes` and `grain_config` stored `sandbox_backend = "kubernetes"` (or `"kubernetes-granule"`), `LoadConfig` called `flagCfg.WithModelConfig(runnable)` → `c.withSandboxBackend(mc.SandboxBackend)`, which set `c.DisableNativeSandboxes = false` and `c.HostSandboxes = false` before `startUIServer` computed `cfg.availableSandboxBackends()`. Because `sandboxBackendAvailable` checked `c.DisableNativeSandboxes` and `c.HostSandboxes`, `"none"` and `"host"` disappeared from `sandboxBackends` after restart, preventing the operator from switching back via `/settings/sandboxes` or `grain settings`.
   - **Fix**: Preserved `startupDisableNativeSandboxes` and `startupHostSandboxes` in `withSandboxBackend` and checked them in `sandboxBackendAvailable`. Added regression test `TestAvailableSandboxBackendsRetainsStartupNoneAndHostAfterSwitchingToKubernetes` in `pkg/daemon/sandbox_backend_setting_test.go`.

2. **Cannot select the first option (`kubernetes`) in `/settings/sandboxes` when `sandboxBackend` is initially unset (`ui/src/components/settings/SandboxesSection.jsx`)**:
   - **Cause**: When `settings.sandboxBackend` was `""` and `settings.sandboxBackends` was `["kubernetes", "kubernetes-granule"]`, the native `<select>` rendered by `SandboxesSection.jsx` had no `<option value="">`, causing the browser DOM to default to index `0` (`"kubernetes"`). Selecting `"kubernetes"` therefore did not fire `onChange`.
   - **Fix**: Rendered `<option value="" disabled />` when `settings.sandboxBackend` is not in `settings.sandboxBackends`. Added unit test in `ui/src/components/settings/SandboxesSection.test.jsx`.

3. **`grain settings` human-readable output omitted the 13 Kubernetes settings (`pkg/cli/settingsprint.go`)**:
   - **Cause**: Commit `4c7da2279` added the 13 `-k8s-*` flags to `cmdSettings` (`pkg/cli/settingscmd.go`) and `settingsDiffRows` (`pkg/cli/sync.go`), but did not print the Kubernetes settings in `printer.settings` (`pkg/cli/settingsprint.go`). Running `grain settings -k8s-namespace=...` updated the store and printed `saved, but not running yet -- restart the daemon to apply: k8sNamespace` without showing the stored Kubernetes values in the settings summary above it.
   - **Fix**: Added formatting for the 13 Kubernetes settings in `pkg/cli/settingsprint.go` when a Kubernetes backend is selected or any `k8s*` field is non-empty. Added unit test `TestCmdSettingsSetsAndPrintsKubernetesSandboxSettings` in `pkg/cli/settings_sandbox_shape_test.go`.

---

## Automated Tests Added

- `ui/e2e/k8s-sandbox-settings-walkthrough.spec.js`: Full Playwright E2E test executing all 5 steps of `docs/test-plans/2026-10-22.md` §2 against a real `grain daemon` and `grain` CLI at desktop (`1280×800`) and phone (`390×844`) viewports, including `grain state sync`, `grain state check`, `grain settings -k8s-*`, and daemon restart adoption.
- `pkg/daemon/sandbox_backend_setting_test.go`: `TestAvailableSandboxBackendsRetainsStartupNoneAndHostAfterSwitchingToKubernetes`.
- `pkg/cli/settings_sandbox_shape_test.go`: `TestCmdSettingsSetsAndPrintsKubernetesSandboxSettings`.
- `ui/src/components/settings/SandboxesSection.test.jsx`: Unit test verifying selecting `"kubernetes"` fires `saveChoice` when `sandboxBackend` is initially `""`.
