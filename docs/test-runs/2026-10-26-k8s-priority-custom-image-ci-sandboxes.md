# Walkthrough: K8s PriorityClasses, Cluster Free Capacity, Custom Sandbox Image, `grain init -platform=gke-sandboxes` & Collapsible CI Sandbox Tables (`docs/test-plans/2026-10-26.md` §7)

- **Date:** 2026-10-26, 14:21–16:10 UTC
- **Task:** `grain/049f1e09/task-1205`, base `main` (origin: task 1140)
- **Commit tested:** `add8ca764` (`main` at the start of the run, "Merge pull request #4916"). Both bugs found during the walkthrough were reproduced against the unmodified binary built from `add8ca764`, fixed in `9fe336e40`, and re-verified on `grain/049f1e09/task-1205`.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-54-amd64`, 7 vCPUs, 15 GiB RAM, `linux/amd64`
  - Go `go1.26.10`, Node.js `v20.19.0`, npm `10.8.2`
  - Playwright `1.62.1`, headless Chromium `chromium-1234` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, desktop `1440x900` and phone `390x844`
  - Server builds: `cd ui && npm ci && npm run build`, then `go build ./cmd/grain` (baseline binary at `/var/tmp/grain-unmodified`, fixed binary at `/var/tmp/grain-fixed`).
  - Demo servers driven during the walkthrough:
    - `grain demo -addr 127.0.0.1:<port> -open=false -github-sim -fake-ci -access-code-file <file>` for `/settings/sandboxes` UI and `grain settings` CLI checks
    - `grain demo -addr 127.0.0.1:<port> -open=false -observability -staging -access-code-file <file>` for `/ci` and `/system/sandboxes` live CI + agent + persistent sandbox checks and cluster free capacity readings

## Real Services vs Stubs

- **Real services exercised:**
  - Embedded SQLite store (`grain_config` table) backing `/api/config`, `PUT /api/settings`, `GET /api/settings`, and the `grain settings` CLI subcommand
  - Built React + MUI frontend (`ui/dist` embedded into `pkg/ui/static`) rendered in headless Chromium via Playwright at `1440x900` and `390x844`
  - `pkg/ci.Service`, `pkg/ci.Engine`, on-disk `ci.DirCache`, `orchestrator.HostSandboxes`, and `daemon.SandboxHealthAdapter` in `grain demo -observability -staging`
  - `grain init -platform=gke-sandboxes -dry-run` command-plan generator (`pkg/hostinit`) across `c4-standard-16` (`hyperdisk-balanced`) and `n2-standard-16` (`pd-balanced` + cluster autoscaler)
  - `k8s.FitChecker` and `orchestrator.K8sSandboxes` unit/integration suites against `k8s.io/client-go/kubernetes/fake`
- **Stubs / demo boundaries:**
  - `demoObservabilityBackend.ClusterFree` supplies a deterministic 3-node GKE reading (`6.5 vCPU / 40.0 GiB` free of `286.5 vCPU / 1080.0 GiB` allocatable, roomiest node `gke-pool-1-node-b: 3.5 vCPU / 20.0 GiB`, `1` pending pod) so `/system/sandboxes` renders `[data-testid="sandbox-cluster-free"]` without a live GKE control plane.
  - `POST /api/e2e/sandboxes/other` and `POST /api/e2e/ci/finish` in `grain demo -observability` pace the host-directory CI and agent sandboxes so Playwright can inspect them deterministically while running.

---

## Summary of Bugs Found & Fixed

### Bug 1 — `sandboximage.ValidateReference` accepted `http://` / `https://` URLs and shell/URI metacharacters (`pkg/sandboximage/sandboximage.go`)

- **Symptom:** The test plan (§7.1) and commit `ed682ad96` specify that `sandboximage.ValidateReference` must refuse invalid strings including URLs (`"https://..."`), whitespace (`"a b"`), and flags (`"-flag"`) with HTTP 400. On `add8ca764`, `ValidateReference` checked only for whitespace and a leading `-`, so `PUT /api/settings` with `{"sandboxImage":"https://ghcr.io/acme/sandbox:v1"}`, `{"sandboxImage":"http://registry.example/sandbox:v1"}`, or `{"sandboxImage":"registry.example/a;b:v1"}` returned `200 OK` and persisted the invalid string into `grain_config.sandbox_image`.
- **Deterministic reproduction (before fix on `add8ca764`):**
  1. Start `/var/tmp/grain-unmodified demo -addr 127.0.0.1:4291 -open=false -github-sim -fake-ci -access-code-file /var/tmp/code.txt`.
  2. Run:
     ```bash
     /var/tmp/grain-unmodified -server http://127.0.0.1:4291 -token "$(cat /var/tmp/code.txt)" \
       settings -sandbox-image="https://ghcr.io/acme/sandbox:v1"
     ```
     Output:
     ```text
     sandboxImage: "https://ghcr.io/acme/sandbox:v1"
     ```
     (Exit code `0`; `GET /api/config` returned `"sandboxImage": "https://ghcr.io/acme/sandbox:v1"`.)
  3. Sending `PUT /api/settings` with `{"sandboxImage":"img;rm"}` also returned `HTTP 200`.
- **Root cause:** `pkg/sandboximage/sandboximage.go` only tested `strings.IndexFunc(trimmed, unicode.IsSpace) >= 0` and `strings.HasPrefix(trimmed, "-")`. Because `"https://ghcr.io/acme/sandbox:v1"` has no spaces and does not start with `"-"`, it passed validation.
- **Fix (`9fe336e40`):**
  - Tightened `ValidateReference` in `pkg/sandboximage/sandboximage.go` to reject:
    - Any scheme separator `"://"` with a specific message: `"<ref>" is a URL, not an OCI image reference (drop the scheme, e.g. registry.example/team/sandbox:v1)`
    - Non-alphanumeric leading characters (`!isAlphaNum(trimmed[0])`)
    - Any character outside the legal OCI reference alphabet `[A-Za-z0-9._/:@-]`
    - Empty path components (`"//"`), or trailing `'/'`, `':'`, or `'@'`
  - Added unit tests in `pkg/sandboximage/sandboximage_test.go`, API handler tests in `pkg/ui/settings_test.go`, and Playwright e2e coverage in `ui/e2e/custom-sandbox-image-layout.spec.js`.
- **Verification (after fix):**
  - `PUT /api/settings` with `{"sandboxImage":"https://ghcr.io/acme/sandbox:v1"}` returns `HTTP 400`:
    `sandboxImage must be one OCI image reference, such as registry.example/team/sandbox:v1: "https://ghcr.io/acme/sandbox:v1" is a URL, not an OCI image reference (drop the scheme, e.g. registry.example/team/sandbox:v1)`
  - `./grain -server ... settings -sandbox-image="https://ghcr.io/acme/sandbox:v1"` exits non-zero (`exit 1`) with the same error and leaves `sandboxImage` unchanged.

### Bug 2 — `grain init -platform=gke-sandboxes` omitted the three sandbox `PriorityClass` manifests (`pkg/hostinit/gke_sandboxes.go`)

- **Symptom:** Commit `27ced75bd` added `grain-interactive` (`1000000`, `PreemptLowerPriority`), `grain-agent` (`100000`, `Never`), and `grain-ci` (`1000`, `Never`) to `deploy/kubernetes/sandboxes-priorityclasses.yaml` and wired `K8sSandboxes` to stamp `priorityClassName` on every sandbox pod by default (`DefaultInteractivePriorityClass`, `DefaultAgentPriorityClass`, `DefaultCIPriorityClass`), but `pkg/hostinit/gke_sandboxes.go` (`grain init -platform=gke-sandboxes`) only wrote the `Namespace`, `ServiceAccount`, `Role`, and `RoleBinding` in Step 4 (`rbac.yaml`). A remote GKE sandbox cluster provisioned via `grain init -platform=gke-sandboxes` had none of the three default `PriorityClasses`, causing Kubernetes admission to reject every sandbox pod with `no PriorityClass with name grain-agent was found`.
- **Deterministic reproduction (before fix on `add8ca764`):**
  ```bash
  /var/tmp/grain-unmodified init -platform=gke-sandboxes \
    -gke-project=test-proj -gke-location=us-central1-a -dry-run | grep -c "kind: PriorityClass"
  ```
  Output: `0`.
- **Root cause:** `gkeSandboxesRBACManifest` in `pkg/hostinit/gke_sandboxes.go` was not updated when `deploy/kubernetes/sandboxes-priorityclasses.yaml` was introduced in `27ced75bd`.
- **Fix (`9fe336e40`):**
  - Updated `gkeSandboxesRBACManifest` and Step 4 (`Create namespace, PriorityClasses, ServiceAccount, and RBAC`) in `pkg/hostinit/gke_sandboxes.go` to apply `grain-interactive` (`1000000`, `PreemptLowerPriority`), `grain-agent` (`100000`, `Never`), and `grain-ci` (`1000`, `Never`) alongside the namespace and RBAC resources.
  - Updated `pkg/capability/bootstrap/playbooks/gke-sandbox-cluster.md` and `deploy/kubernetes/README.md` so manual bootstrap documentation stays in sync with `grain init -platform=gke-sandboxes`.
  - Added assertions in `pkg/hostinit/gke_sandboxes_test.go`.

### Minor Test-Plan Flag Note

- `docs/test-plans/2026-10-26.md` §7.3 prose mentions `-gke-zone=us-central1-a`, whereas the actual CLI flag implemented in `cmd/grain/init.go` (`4a7136375`) is `-gke-location=us-central1-a` (accepting either a zone or region). Running `./grain init -platform=gke-sandboxes -gke-project=test-proj -gke-location=us-central1-a -dry-run` works as designed.

---

## Step-by-Step Walkthrough Results

### 1. `/settings/sandboxes` Custom OCI Sandbox Image & Kubernetes `PriorityClasses`

| # | Area | Step / Action | Observed Outcome | Verdict |
|---|------|---------------|------------------|---------|
| 1.1 | `/settings/sandboxes` (`1440x900` & `390x844`) | Open `/settings/sandboxes` and inspect `"Custom sandbox image"` card (`#custom-sandbox-image`) | Card renders inside container (`1440x900`: `card={x:264,y:1702,w:1152,h:655}`, `input={x:288,y:1810,w:1104,h:37}`, `docker={x:288,y:1886,w:1104,h:113}`, `overflow=false`; `390x844`: `card={x:16,y:2192,w:358,h:930}`, `input={x:40,y:2376,w:310,h:37}`, `docker={x:40,y:2469,w:310,h:113}`, `overflow=false`). Displays `FROM` stock image, contract versions (`[data-testid="sandbox-contract-versions"]`: `dev.grain.sandbox.contract = 1`, `dev.granule.guest.contract = 1`), and 5-item `[data-testid="sandbox-contract-checklist"]`. | PASS |
| 1.2 | `/settings/sandboxes` | Save valid custom image `ghcr.io/acme/custom-sandbox:2026.10` and reload page | `PUT /api/settings {"sandboxImage":"ghcr.io/acme/custom-sandbox:2026.10"}` -> `200 OK`; status bar shows `"Saved Custom sandbox image (OCI reference)"`; across `page.reload()`, input value remains `"ghcr.io/acme/custom-sandbox:2026.10"` and `GET /api/config` returns `"sandboxImage": "ghcr.io/acme/custom-sandbox:2026.10"`. | PASS |
| 1.3 | `/settings/sandboxes` | Submit invalid image references: `"a b"`, `"-flag"`, `"https://ghcr.io/acme/sandbox:v1"`, `"registry.example/a;b:v1"` | Each returns `HTTP 400` (`sandboxImage must be one OCI image reference...`); UI shows `"Couldn't save Custom sandbox image (OCI reference)"` and does not overwrite stored `ghcr.io/acme/custom-sandbox:2026.10`. | PASS (after Bug 1 fix) |
| 1.4 | `/settings/sandboxes` | Clear `"Custom sandbox image (OCI reference)"` back to `""` and reload | `PUT /api/settings {"sandboxImage":""}` -> `200 OK`; across reload, input is `""` (placeholder shows stock image) and `GET /api/config` returns `"sandboxImage": ""`. | PASS |
| 1.5 | `/settings/sandboxes` (`1440x900` & `390x844`) | Check Kubernetes `PriorityClass` fields (`Interactive`, `Agent`, `CI`) alignment (`f58e2f354`) | All 3 `PriorityClass` inputs align to the exact pixel column and width of `ServiceAccount`: at `1440x900` all 4 have `x: 288, w: 1104` (`dx=[0,0,0], dw=[0,0,0]`); at `390x844` all 4 have `x: 40, w: 310` (`dx=[0,0,0], dw=[0,0,0]`). | PASS |
| 1.6 | `/settings/sandboxes` | Save custom `PriorityClass` names (`custom-interactive`, `custom-agent`, `custom-ci`), reload, then clear `CI PriorityClass` to `""`, save, and reload (`354732488`) | Custom names persist across reload (`k8sPriorityClassInteractive="custom-interactive"`, `k8sPriorityClassAgent="custom-agent"`, `k8sPriorityClassCI="custom-ci"`). Clearing `CI PriorityClass` to `""` sends `PUT /api/settings {"k8sPriorityClassCI":""}` -> `200 OK`, and across reload the input stays `""` (`"no priority class"`) instead of snapping back to `"grain-ci"`. | PASS |
| 1.7 | `grain settings` CLI | Run `./grain -server ... -token ... settings -sandbox-image=ghcr.io/acme/cli-sandbox:v2 -k8s-priority-class-interactive=cli-interactive -k8s-priority-class-agent=cli-agent -k8s-priority-class-ci=cli-ci`, test invalid URL via CLI, then clear back | Valid CLI call exits `0` and updates SQLite (`sandbox_image="ghcr.io/acme/cli-sandbox:v2"`, `k8s_priority_class_interactive="cli-interactive"`, `k8s_priority_class_agent="cli-agent"`, `k8s_priority_class_ci="cli-ci"`). Invalid `-sandbox-image="https://ghcr.io/acme/sandbox:v1"` exits `1` with URL refusal message. Clearing `-sandbox-image=""` and `-k8s-priority-class-ci=""` exits `0` and persists `""`. | PASS |

### 2. Collapsible CI Sandboxes Summary on `/ci` and `/system/sandboxes`

| # | Area | Step / Action | Observed Outcome | Verdict |
|---|------|---------------|------------------|---------|
| 2.1 | `/ci` (`1440x900`) | Start active CI run in `grain demo -observability -staging` and open `/ci` | `"Active sandboxes (1)"` toggle button has `aria-expanded="false"`, `#ci-active-sandboxes` is hidden (`table` count `0`), and `#ci-active-sandboxes-summary` displays `"1 CI sandbox running · longest 0s (observe, main)"`. | PASS |
| 2.2 | `/ci` (`c8af82a52`) | Query heading by accessible name `getByRole("heading", { name: /^Active sandboxes \(\d+\)$/ })` | Matches `<h6 class="MuiTypography-root MuiTypography-subtitle2">`; summary text is kept in sibling `#ci-active-sandboxes-summary` (`aria-describedby="ci-active-sandboxes-summary"`) and out of the heading's accessible name. | PASS |
| 2.3 | `/ci` (`390x844`, `d4eb7ab90`) | Resize to `390x844` and inspect wrapped summary alignment | Heading is at `{x:16, y:219, w:157, h:22}` and wrapped `#ci-active-sandboxes-summary` is at `{x:16, y:242, w:265, h:20}` (`dx = 0`, `textAlign = "start"`, `overflow = false`). | PASS |
| 2.4 | `/ci` | Focus toggle button, press `Enter`, and reload `/ci` | Pressing `Enter` sets `aria-expanded="true"`, renders the 5-column table (`Sandbox`, `Repo`, `Branch`, `Job`, `Running for`), and writes `localStorage["grain.ci.activeSandboxes.expanded"] = "true"`. After `page.reload()`, `aria-expanded` remains `"true"` and the table remains visible. | PASS |
| 2.5 | `/system/sandboxes` (`1440x900` & `390x844`) | Add agent sandbox (`POST /api/e2e/sandboxes/other`) alongside active CI sandbox and persistent sandboxes (`docs-preview`, `grain-staging`); open `/system/sandboxes` | `[data-testid="sandbox-cluster-free"]` renders `"Cluster free: 6.5 vCPU / 40.0 GiB of 286.5 vCPU / 1080.0 GiB allocatable on 3 nodes · roomiest node gke-pool-1-node-b: 3.5 vCPU / 20.0 GiB · 1 new pod(s) not yet in this reading"`. Persistent sandboxes table renders `docs-preview` (`running`) and `grain-staging` (`restarting`). In the live sandboxes table, agent row `1-identity-check` (`pool: agent`) renders at row index 0 above `[data-testid="ci-sandboxes-group"]` (`"CI sandboxes (1)"`, `aria-expanded="false"`, summary `"1 CI sandbox · 2 vCPU / 2.0 GiB"`). | PASS |
| 2.6 | `/system/sandboxes` | Expand `"CI sandboxes (1)"` via click/keyboard and reload `/system/sandboxes` | Expanding sets `aria-expanded="true"`, writes `localStorage["grain.system.ciSandboxes.expanded"] = "true"`, and reveals CI sandbox row `cb764d4-0` (`pool: ci`) below the summary header row. Across `page.reload()`, `aria-expanded` remains `"true"` and `cb764d4-0` stays visible. | PASS |

### 3. `grain init -platform=gke-sandboxes -dry-run` & Kubernetes `FitChecker` / Autoscaling / Orphan Sweep

| # | Area | Step / Action | Observed Outcome | Verdict |
|---|------|---------------|------------------|---------|
| 3.1 | `grain init -platform=gke-sandboxes -dry-run` (`c4-standard-16`) | Run `/var/tmp/grain-fixed init -platform=gke-sandboxes -gke-project=test-proj -gke-location=us-central1-a -gke-machine-type=c4-standard-16 -dry-run` | Prints 7-step command plan for `test-proj/us-central1-a/grain-sandboxes`. Step 2 selects `--disk-type=hyperdisk-balanced` for `c4-standard-16` (`4a7136375`). Step 4 creates `Namespace grain`, `PriorityClasses grain-interactive (1000000, PreemptLowerPriority), grain-agent (100000, Never), grain-ci (1000, Never)`, `ServiceAccount grain-orchestrator`, `Role`, and `RoleBinding`. Step 5 mints a 10-year `grain-orchestrator` ServiceAccount token, writes `/etc/grain/sandbox-kubeconfig` (`chmod 600`), and runs `kubectl config delete-user` to strip the bootstrap user (`35646d3b2`). | PASS (after Bug 2 fix) |
| 3.2 | `grain init -platform=gke-sandboxes -dry-run` (`n2-standard-16` + autoscaling) | Run `/var/tmp/grain-fixed init -platform=gke-sandboxes -gke-project=test-proj -gke-location=us-central1-a -gke-machine-type=n2-standard-16 -gke-max-nodes=5 -dry-run` | Step 2 selects `--disk-type=pd-balanced` (`c4-*` Hyperdisk rule does not fire on `n2-*`) and appends `--enable-autoscaling --min-nodes=1 --max-nodes=5`. | PASS |
| 3.3 | `k8s.FitChecker`, autoscaling budget & orphan sweep (`pkg/k8s`, `pkg/orchestrator`, `tests/deploy`) | Run `go test -v ./pkg/k8s/... ./pkg/orchestrator/... ./tests/deploy/...` | All tests pass: `TestFitCheckerAdmitsAndDeductsWithinBurstWindow` (deducts admitted pods across the 5s burst window and returns `ErrClusterFull` when the third pod exceeds node free capacity), `TestFitCheckerAutoscalingPoolDefersToClusterAutoscaler` (does not return `ErrClusterFull` when `cluster-autoscaler.kubernetes.io/safe-to-evict` / autoscaled pool can scale up), `TestSweepOrphansRechecksActiveBeforeDelete` (`fcfb28123`: re-checks active set before deleting a pod so an acquire that completed mid-sweep is not killed), and `TestSandboxesPriorityClassesManifest` (`tests/deploy`). | PASS |

---

## Automated Regression Tests Added / Verified

- `pkg/sandboximage/sandboximage_test.go`: Added `TestValidateReference` cases for `https://`, `http://`, shell metacharacters (`;`), non-alphanumeric leading characters, double slashes, and trailing delimiters.
- `pkg/ui/settings_test.go`: Added `PUT /api/settings` rejection check for `"https://ghcr.io/acme/sandbox:v1"`.
- `pkg/hostinit/gke_sandboxes_test.go`: Verified `grain-interactive`, `grain-agent`, and `grain-ci` `PriorityClass` manifests are emitted in Step 4 of `grain init -platform=gke-sandboxes`.
- `ui/e2e/custom-sandbox-image-layout.spec.js`: Added `"saves, reloads, refuses invalid references and clears the custom sandbox image"` end-to-end Playwright test (`3 passed`).
- `ui/e2e/k8s-priority-class-settings.spec.js`: Extended Playwright test to verify saving custom `PriorityClass` names (`custom-interactive`, `custom-agent`, `custom-ci`) across reload as well as clearing `CI PriorityClass` to `""` across reload (`1 passed`).
- `ui/e2e/observability.spec.js`: Ran full Playwright observability spec (`1 passed`).
