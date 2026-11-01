# Test run — Bootstrap, Secrets/Access, Browser and Operations settings across UI, `grain/config` and `settings_locked` (2026-11-01)

Checks every setting in the first four Settings groups (Models & sandboxes, Secrets & access, This browser, System / Operations) against [`design/design/settings.design.md`](../../design/design/settings.design.md), [`settings.spec.md`](../../design/spec/settings/settings.spec.md) and [`settings-mutability.spec.md`](../../design/spec/settings/settings-mutability.spec.md), on three axes:

- **(a)** a UI edit with `settings_locked: false`;
- **(b)** a commit to `grain/config`;
- **(c)** the lock and external mode.

Task 2155.

## Environment

- **Tree:** `main` at `1d324a89`, plus this branch. Fixes are below.
- **Build:** `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`. The UI is embedded.
- **Daemon:** a real `grain daemon`, not `grain demo`.
  - Set up with `grain init workstation -non-interactive -repo acme/widgets -sandbox-backend host -model-backend claude -ui-addr 127.0.0.1:18731 -start-service=false` under a temp `HOME`.
  - Started from the `daemon.sh` that init wrote, with `-sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http`. Later runs added `-upgrade-src-dir <checkout> -upgrade-install-path <copy of the binary>`, so the upgrade form is live.
  - Its state repository is the controller-local `grain/config` at `<data-dir>/state-repo`. `GET /api/state-repo` reports `mode: internal`.
- **Callers:**
  - REST: the local API token as a bearer token.
  - Browser: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) with the same bearer header.
- **Scripts:**
  - [`rest-matrix.py`](2026-11-01-settings-bootstrap-operations-verification/rest-matrix.py), run as `python3 rest-matrix.py <grain binary>`.
  - [`ui-matrix.mjs`](2026-11-01-settings-bootstrap-operations-verification/ui-matrix.mjs). Copy it into `ui/` and run it with `node` so it resolves `playwright`.
  - Their full output is in [`rest-matrix.out`](2026-11-01-settings-bootstrap-operations-verification/rest-matrix.out) and [`ui-matrix.out`](2026-11-01-settings-bootstrap-operations-verification/ui-matrix.out).
- **Stubbed:** only the upgrade POST. The browser's `POST /api/upgrade` was intercepted with `page.route`, so no upgrade ran.

## Results

**Final run, on the fixed build: REST 117/117 passed, UI 45/45 passed.**

The first pass ran before the fixes. Its only real failures were D-1, D-2 and D-10 below, plus three script mistakes, which were corrected and are not daemon bugs:

- durations are exported as `*_ms` integers;
- an invalid `networkAccess` value was sent;
- SQLite exports `package_cache_enabled` as `1`.

### (a) UI and REST edits with `settings_locked: false`

| Setting(s) | Surface | Result |
| --- | --- | --- |
| `defaultModel`, `maxWorkers`, `sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, `networkAccess`, `sandboxCpuOvercommit`, `sandboxMemoryOvercommit`, `sandboxDiskFloorShare`, `sandboxImage`, `sessionSandboxIdleTtl` | `PUT /api/settings`; UI (Sandboxes vCPUs, idle TTL) | 200. Each write committed to `grain/config` at once (`SyncAfterWrite`), and the value is in `tables/grain_config.json`. Durations are stored as `*_ms`. |
| `githubHost`, `gcpProject`, `gcpServiceAccountEmail` | REST; UI (GitHub host) | 200, exported. |
| `packageCacheEnabled`, `packageCacheSizeGb`, `actionsCacheEnabled`, `actionsCacheSizeGb` | REST; UI (Caches checkboxes and sizes) | 200, exported. |
| `pollInterval`, `maxMergesPerRepo`, `mergeBatchSize`, `mergeGatherWindow`, `maxWorkers` | REST; UI (Engine) | 200, exported (`poll_interval_ms: 27000`, `merge_gather_window_ms: 120000`, …). |
| Persistence | `POST /api/state-repo/sync` after the UI edits | The UI's values survive the import. Nothing was clobbered. |
| Secret store | `PUT /api/secrets/verif-secret/token` | 200. The value is absent from `GET /api/secrets`, `GET /api/settings` and every file of `grain/config`. |
| Executors | `POST /api/executors/tokens`, `POST …/{id}/revoke` | 201 / 200. Neither the token nor the `executor_token` table is in `grain/config`. |
| Preferences | UI appearance → dark | `localStorage` changed. No `PUT /api/settings` or `/api/config` was sent, and nothing reached `grain/config`. |
| Access | access-code / bearer session | Not stored in `grain/config` (nothing under `tables/` names it). |

### (b) A commit to `grain/config`

The run edited `tables/grain_config.json` in the state repo's checkout:

- `poll_interval_ms: 40000`
- `merge_batch_size: 5`
- `package_cache_size_gb: 20`
- `sandbox_cpus: 5`
- `session_sandbox_idle_ttl_ms: 3000000`
- `gcp_project`

`grain state check <repo>` printed `ok: grain can load this`. After the commit and `POST /api/state-repo/sync`, `GET /api/settings` returned every value. A second commit (`poll_interval_ms: 44000`, `actions_cache_size_gb: 13`) showed up in the Engine and Caches fields after a reload.

### (c) `settings_locked: true`, external mode and `directConfigWrites`

| What | Locked (internal) API | Locked UI | External API | External UI |
| --- | --- | --- | --- | --- |
| Models: `defaultModel` | 200 | editable | 409 | read-only, with banner |
| Sandboxes sizes, network, overcommit, disk floor, image | 200 | editable | 409 | read-only |
| `sessionSandboxIdleTtl` | 409 `config_managed_externally` | **read-only (fixed, D-1)** | 409 | read-only |
| Engine `maxWorkers` | 200 | editable; save works while locked | 409 | read-only |
| Engine `pollInterval`, `maxMergesPerRepo`, `mergeBatchSize`, `mergeGatherWindow` | 409 | **read-only, with notice (fixed, D-1)** | 409 | read-only |
| Caches (all four) | 409 | **read-only, with notice (fixed, D-1)** | 409 | read-only |
| Legacy capabilities `githubHost`, `gcp*` | 409 | **read-only, with notice (fixed, D-2)** | 409 | read-only |
| Secrets `PUT /api/secrets/…` | 200 | editable | 200 | editable (Legacy capabilities opts out) |
| Executor join tokens | 201 | editable | — | read-only (D-9, open) |
| Preferences | n/a (browser only) | editable | n/a | editable |
| Upgrade | `POST /api/upgrade` is unguarded (empty branch → its own 400) | editable. The click posted `{"branch":"main","afterTasksFinish":false}` (intercepted) | — | editable |
| Config repo / sync | `POST /api/state-repo/sync` 200 | editable | — | editable |
| The lock itself | `{"settingsLocked":false}` 200 | General's toggle enabled; clicking it unlocks | 409 (external refuses every write, the lock included) | — |

- **External mode:** set with `POST /api/state-repo/remote {"remote":"file://…/ext.git"}`. `GET /api/config` reported `configRepoOnly: true`, and `{"none":true}` switched back to internal.
- **`directConfigWrites: true`** in `state-repo.json`, with the lock on:
  - Before the fix: the API accepted `pollInterval` (200), but Engine and General stayed read-only.
  - After the fix: both are editable, and the API still returns 200.
  - Back to `{"branch":"main"}`: the API returns 409 and the UI is read-only again.

## Bugs found and fixed

1. **D-1: Engine, Caches and `sessionSandboxIdleTtl` were editable while locked, but the API refused them.**
   - Repro: lock settings, open `/settings/engine`, change Poll interval and blur. `PUT /api/settings {"pollInterval":…}` returns 409, and the config-repo dialog opens.
   - Fix:
     - `caches` joins `CONFIG_REPO_SECTION_PATHS`: the section is read-only, with the config-managed notice.
     - Engine disables the four non-bootstrap fields one by one, with a notice, and leaves Max worker agents editable.
     - Sandboxes disables only the idle TTL.
   - All three key on the page's `configRepoOnly` (`s.configLocked`).
2. **D-2: Legacy capabilities' GitHub host and GCP fields were editable while locked.**
   - Same repro on `/settings/credentials`.
   - The deployment-wide fieldset is now disabled while locked too, with a notice. Secret entry stays writable.
3. **D-10: the Settings page ignored `directConfigWrites`.**
   - This branch first fixed it by adding `directConfigWrites` to `GET /api/settings`.
   - `main` then landed its own fix independently (`docs/test-runs/2026-11-01-settings-deployment-work-verification.md`): `GET`/`PUT /api/settings` carry `configRepoOnly`, and `SettingsPage` keys its lock on that.
   - When merging `main`, this branch took that version and dropped its own field. The live behaviour recorded above is the same under both.

**Regression tests:**

- `TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut` (`pkg/ui/config_locked_fields_test.go`). It pins, field by field, which settings the API accepts and refuses under the lock, and so which ones the UI must grey out.
- `TestConfigSaysWhenSettingsAreConfigRepoOnly` now also checks that `GET /api/settings` reports `configRepoOnly: false` under the opt-out.
- Five vitest cases in `SettingsPage.test.jsx` under "with settings locked".

The spec marks D-1, D-2 and D-10 fixed.

## Screens

Engine, Caches, Sandboxes and Legacy capabilities were shot with the lock on, at 1280×900 and 390×844, light and dark (16 shots).

Bounding boxes checked:

- Every config-managed notice lies inside the settings content column: 760 px wide on desktop, 366 px (340 px in Legacy capabilities' nested card) on a phone.
- No page scrolls horizontally.
- Sandboxes shows no notice, only the disabled TTL field.

## Left open

- **D-9.** In external mode, Models (model-connection credentials) and Executors (join tokens) are read-only in the UI, but their routes still accept writes. Verified: `POST /api/executors/tokens` succeeds while the Executors section is drawn read-only. It is unchanged here; the spec already lists it.
- **D-12.** On a deployment with no config repository, `settings_locked` still refuses. Not exercised: this daemon always had `grain/config`.
- **`mergeBatchSize` export commit.** On the first unlocked pass, the `PUT {"mergeBatchSize":3}` added no commit of its own to `grain/config`, though the value was in `tables/grain_config.json` straight afterwards. It is most likely folded into the neighbouring commit. It was not seen again, and the value was correct every time.
