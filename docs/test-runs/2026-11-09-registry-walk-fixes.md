# §4 items 3–4 walkthrough — registry walk fixes on two replicas, `ConfirmDialog` keyboard contract, and `creating` propagation `status_detail` (task 3745)

**Plan:** [`docs/test-plans/2026-11-09.md` §4 items 3–4](../test-plans/2026-11-09.md), completing what [Task #3731](/tasks/3731) ([`2026-11-09-hosted-instance-credentials.md`](2026-11-09-hosted-instance-credentials.md)) did not reach.
**Task:** [Task #3745](/tasks/3745), filed by [Task #3731](/tasks/3731) from [Task #3713](/tasks/3713).
**Harnesses & transcripts:** [`2026-11-09-registry-walk-fixes/`](2026-11-09-registry-walk-fixes/), reusing [Task #3636](/tasks/3636)'s [`2026-11-08-grain-registry-phase2/`](2026-11-08-grain-registry-phase2/) and [Task #3731](/tasks/3731)'s [`2026-11-09-hosted-instance-credentials/`](2026-11-09-hosted-instance-credentials/).

## Environment

- Sandbox: Debian 12, Go from `go.mod`, PostgreSQL 15 (`postgresql`, `postgresql-client`), Playwright Chromium (`/var/cache/playwright`), base `main` @ `58c162d6`.
- **Two-replica cluster** (`two-replica-walk.sh` → `2026-11-08-grain-registry-phase2/env-up.sh` + `start-daemon.sh`): shared PostgreSQL (`127.0.0.1:4240`), shared ES256 registry signing key, `iapstub.go` JWKS (`:4250`) and two identity-injecting front proxies (`RA = 127.0.0.1:4251 -> :4252`, `RB = 127.0.0.1:4261 -> :4262`) fronting two real `grain-registry serve` OS processes, plus a real `grain daemon` (`127.0.0.1:4270`) holding a live outbound `ws-mux` tunnel as `conn-alice`.
- **Late-propagating service-account cluster** (`creating-walk.sh`): `hostssl`-only TLS PostgreSQL (`127.0.0.1:5432`) + registry PostgreSQL (`127.0.0.1:4272`), `gcpstub.go` (`127.0.0.1:4271`, `SA_DELAY=60`), `iapstub.go` (`127.0.0.1:4281 -as alice@example.com`), and `registry-fakegcp.go` (`127.0.0.1:4282`, real `gce` compute + resources providers + reconciler + embedded `ui-registry` static assets, with `compute.grant_self_act_as: false`).

## Summary of defects found and fixed

| # | Defect | Severity | Fix & regression |
|---|---|---|---|
| 1 | **`grain registry show` and `grain registry list` dropped `status_detail`** ([Task #3731](/tasks/3731) finding 5, confirmed on `list` too). While `walk-creating` was in `creating` with the service-account propagation reason, neither `list` nor `show` printed `status_detail` (nor `boot_disk_gb` or `replace_step`). | Medium (operators and owners cannot see why an instance is stuck `creating` or `failed` from the CLI) | `grain registry list` appends `detail=<status_detail>` when set; `grain registry show` prints `Step:`, `Detail:`, `Health:` (when distinct), and `Disk: <N> GB`. Updated `design/spec/cli/host.spec.md` (`#CLI-109`). Test: `TestRegistryCLIListAndShowPrintStatusDetail` (`pkg/cli`) |
| 2 | **`ui-registry` hid `creating`/`replacing` `status_detail` on My Entries, showed `offline` in the Reachability pill, and shadowed/mislabeled `status_detail` on Entry Detail.** My Entries (`/`) rendered a grey `offline` pill for `creating`/`replacing`/`failed` entries (despite `StatusPill` in `ui-shared/primitives.jsx` already styling those states) and put `status_detail` only in a `title` tooltip (invisible on touch/390 px and in screenshots). Entry Detail (`/entries/<name>`) had no provisioning/failure banner and rendered `entry.health_detail \|\| entry.status_detail` under `Health detail:`, mislabeling provisioning reasons and letting any prior `health_detail` hide `status_detail`. | Medium (UX / visibility) | `reachabilityStatus` returns `creating`/`replacing`/`failed`; My Entries renders a 3-line-clamped `status_detail` summary beneath `StatusPill`; Entry Detail renders `lifecycle-status-banner` (`Provisioning instance:` / `Replacing instance:` / `Instance provisioning failed:`) with `overflow-wrap: anywhere` and separates `Status detail:` from `Health detail:`. Tests in `ui-registry/src/App.test.jsx` |
| 3 | **`Hard purge` (`/admin`), credential `Revoke` (`/entries/<name>`), and consent-grant `Revoke` (`/grants`) had no `ConfirmDialog` at all.** Commit `252d55cb` claimed `ConfirmDialog` backed Stop, Start, Delete, purge, and revoke, while `ui-walk.mjs` guarded Start with `if (await d.isVisible())`. In `ui-registry/src/App.jsx`, only Stop and Delete used `ConfirmDialog`: `Hard purge` fired `POST /api/admin/entries/<name>/purge` (`{confirm: name, force: true, release_reservation: true}`) on a single unconfirmed click with no toggle for `release_reservation`, and both `Revoke` buttons fired irreversible `DELETE` calls immediately. (`ConfirmDialog` also used a static `id="gs-dialog-title"`.) | High (single-click irreversible purge of DB/secrets/reservation and credential/grant revocation) | Gated `Hard purge` (with a `Release the 30-day name reservation` checkbox), credential `Revoke`, and consent-grant `Revoke` behind `ConfirmDialog`; switched `ConfirmDialog`'s heading ID to `useId()`; cleared `deleteConfirmText` on Entry Detail cancel. Tests in `ui-registry/src/App.test.jsx` and Playwright `ui-registry/e2e/registry.spec.js` |
| 4 | **`grain registry upgrade` (`POST /api/entries/{name}/upgrade`) accepted `connected` entries.** While `stop`, `start`, and `resize`/`replace` refused non-`instance` entries with `400 invalid_entry_kind`, `upgrade` on both replicas returned `202 Accepted` for `conn-alice` and wrote `target_version='v3.2.0'` onto the `connected` row even though the reconciler skips `connected` entries (`demo.go` also lacked kind checks on `upgrade` and `replace`). | Low–Medium (state inconsistency / misleading 202) | `handleUpgradeEntry` in `pkg/registry/server.go` (plus `handleUpgradeEntry` and `handleReplaceEntry` in `pkg/registry/demo.go`) refuses `kind != "instance"` with `400 invalid_entry_kind`. Test: `TestUpgradeRefusesConnectedEntries` (`pkg/registry`, PostgreSQL) |
| 5 | **`GET /api/entries` (`handleGetEntries`) did not redact `status_detail` for a pending transfer recipient.** `GET /api/entries/{name}` redacts `Credentials` and `StatusDetail` when the caller is only `PendingOwner` (not yet manager or admin), whereas `GET /api/entries` returned `StatusDetail` unredacted on pending-transfer rows. | Low–Medium (information disclosure across endpoints) | `handleGetEntries` applies the same `!isManager && !caller.IsAdmin` redaction. Test: `TestListEntriesRedactsStatusDetailForPendingTransferRecipient` (`pkg/registry`, PostgreSQL) |

### Note on `Start` confirmation

`Start` (`Start <name>` in My Entries and `Start instance` on Entry Detail) intentionally has **no** confirmation modal by design (`#878` / `8eaf189d`): starting a stopped VM is non-destructive (and visiting its URL with `auto_wake: true` starts it without a prompt), whereas `Stop` drains tasks and deletes the VM. Both `App.test.jsx` and `registry.spec.js` explicitly assert immediate `Start`. Task 3636's report listed `Start` only because `ui-walk.mjs` wrapped the modal check in `if (await d.isVisible().catch(() => false))`, which silently evaluated to `false`. `dialogs-walk.mjs` verifies `Start` activates directly without opening a dialog while testing all seven destructive confirmation entry points.

---

## Item 1 — Walk fixes against two live `grain-registry` replicas

Transcripts: [`two-replica-before.txt`](2026-11-09-registry-walk-fixes/transcripts/two-replica-before.txt) (base `main`) and [`two-replica-after.txt`](2026-11-09-registry-walk-fixes/transcripts/two-replica-after.txt) (fixed).

| Check | Replica A (`:4251`) | Replica B (`:4261`) | Result |
|---|---|---|---|
| `grain registry resize -disk-gb 200 conn-alice` & `POST /api/entries/conn-alice/replace` | `400 (invalid_entry_kind): only kind="instance" entries can be resized or replaced` (exit 1) | `400 (invalid_entry_kind): only kind="instance" entries can be resized or replaced` (exit 1) | ✅ PostgreSQL row stays `connected\|running\|0\|\|` and tunnel `GET /api/whoami` stays `200` through both A and B |
| `grain registry upgrade -version v3.2.0 conn-alice` | Base: `202` (`target_version=v3.2.0` stored!) → Fixed: `400 (invalid_entry_kind)` (exit 1) | Base: `202` → Fixed: `400 (invalid_entry_kind)` (exit 1) | ❌→✅ **Defect 4** fixed; `target_version` stays empty |
| `grain registry transfer -to bob@example.com xfer-walk` → `-cancel xfer-walk` across replicas | Offer on A → visible on B (`PendingTransfer: bob@example.com`); offer on B → `-cancel` on A clears `pending_owner` and `transfer_expires_at` | `-cancel` on B withdraws A's offer; `show` on A confirms no `PendingTransfer` | ✅ Cross-replica offer and withdrawal work in both directions |
| `bob`'s `grain registry accept-transfer xfer-walk` after `-cancel` | `403 (forbidden): only the named pending transfer recipient may accept this transfer` (exit 1) | `403 (forbidden): only the named pending transfer recipient may accept this transfer` (exit 1) | ✅ Refused clearly on both replicas |
| `grain registry transfer -cancel` combined with `-to` or `-owner-group` | `transfer -cancel withdraws a pending offer and takes no -to, -owner-group or -clear-owner-group` (exit 1) | Same local refusal (exit 1, zero HTTP requests) | ✅ |
| Tombstone `GET /api/entries/<name>` + `grain registry register <name>` after admin purge | `bob` `GET /api/entries/reclaim-a` → `404` before and after purge (`alice` owner → `200`); before reservation release `bob` `register` → `409 name_reserved` (not `403`). After `root` purges `reclaim-b` on B (`release_reservation: true`), `bob` `register` on A succeeds (`exit 0`) | `bob` `GET /api/entries/reclaim-a` → `404`; after `root` purges `reclaim-a` on A (`release_reservation: true`), `GET /api/names/reclaim-a` → `available` on A & B and `bob` `register` on B succeeds (`exit 0`, `Owner: bob@example.com` on A) | ✅ Proved in both A→B and B→A directions |
| `grain access list` on connected daemon (`127.0.0.1:4270`) after `grain access allow --label ci-bot --role grain/delegated --reason 'walk delegated ci-bot' delegate:ci-bot@walk-proj.iam.gserviceaccount.com` | Plain text ends with `\twalk delegated ci-bot\tlabel=ci-bot`; `-json` carries `"label":"ci-bot"` | — (daemon-local CLI) | ✅ |

---

## Item 2 — `ui-registry` `ConfirmDialog` keyboard, focus, and viewport walk in Chromium

Transcript: [`dialogs-walk.txt`](2026-11-09-registry-walk-fixes/transcripts/dialogs-walk.txt) (**169 checks, 0 failures**). Regression spec: `ui-registry/e2e/registry.spec.js` (`focuses Cancel, traps Tab, cancels on Escape, and restores focus across every ConfirmDialog at desktop and phone in light and dark`).

Checked across all **4 combinations** (`1440×900` desktop and `390×844` phone × `light` and `dark`) for all **7 confirmation entry points**:
1. **My Entries (`/`) — Stop** (`Stop platform-vm` → `"Stop platform-vm?"`): opens with focus on `Cancel`, `Tab`/`Shift+Tab` cycles `[Cancel, Stop instance]`, fits viewport (`480px` on `1440px`, `358px` on `390px`), zero horizontal overflow, `Escape` closes with no request and returns focus to `Stop platform-vm`.
2. **My Entries (`/`) — Delete** (`Delete` → `"Delete platform-vm?"`): opens with focus on `Cancel` while `Delete entry` is disabled; `Tab`/`Shift+Tab` cycles `[confirm-delete-input, Cancel]` (and includes `Delete entry` once typed); `Escape` closes and returns focus to `Delete`.
3. **Entry Detail (`/entries/platform-vm`) — Stop instance**: same contract, focus returns to `Stop instance`.
4. **Entry Detail (`/entries/platform-vm`) — Delete entry...**: same contract, focus returns to `Delete entry...` and `deleteConfirmText` resets on cancel.
5. **Entry Detail (`/entries/alice-workstation`) — Revoke credential** (`"Revoke registration credential?"`, added in **Defect 3**): opens with focus on `Cancel`, `Tab`/`Shift+Tab` cycles `[Cancel, Revoke credential]`, `Escape` closes without revoking and returns focus to `Revoke`.
6. **Apps acting for you (`/grants`) — Revoke consent grant** (`"Revoke consent for planner?"`, added in **Defect 3**): opens with focus on `Cancel`, `Tab`/`Shift+Tab` cycles `[Cancel, Revoke grant]`, `Escape` closes without revoking and returns focus to `Revoke`.
7. **Admin (`/admin`) — Hard purge** (`"Hard purge alice-sandbox-old?"`, added in **Defect 3**): opens with focus on `Cancel`, `Tab`/`Shift+Tab` cycles `[release-reservation checkbox, Cancel, Hard purge]`, `Escape` closes without purging and returns focus to `Hard purge`.

---

## Item 3 — `creating` entry with service-account propagation `status_detail` (CLI & screenshots)

Transcripts: [`creating-before.txt`](2026-11-09-registry-walk-fixes/transcripts/creating-before.txt) (base) and [`creating-after.txt`](2026-11-09-registry-walk-fixes/transcripts/creating-after.txt) (fixed).

With `SA_DELAY=60` on `gcpstub.go`, `grain registry create -shape n2-standard-2 -listed walk-creating` places `walk-creating` into `state=creating` with:
```text
waiting for service account gi-walk-creating@made-up-proj.iam.gserviceaccount.com, and the registry's permission to attach it, to propagate before creating the GCE instance (attempt 1 failed, retrying in 5s): gcp compute: inserting instance grain-vm-walk-creating in us-central1-a: GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource 'gi-walk-creating@made-up-proj.iam.gserviceaccount.com' of type 'serviceAccount' was not found.
```

- **CLI before vs after:**
  - Base `grain registry list`: printed `walk-creating instance creating offline listed=true owner=alice@example.com https://walk-creating.grain-user.test` (no reason).
  - Fixed `grain registry list`: appends `detail=waiting for service account gi-walk-creating@made-up-proj.iam.gserviceaccount.com, ...`.
  - Base `grain registry show walk-creating`: printed `Name`, `URL`, `Kind`, `State`, `Owner`, `Shape` only.
  - Fixed `grain registry show walk-creating`: prints `Detail: waiting for service account ...` and `Disk: 50 GB` (plus `Step:` and `Health:` when present).
- **UI & geometry before vs after (`1440×900` and `390×844`, light and dark):**
  - Base `/` (My Entries): showed `offline` pill and hid `status_detail` inside `title`. Fixed `/`: shows amber `creating` pill and a 3-line-clamped summary (`239×49 px` at `1440 px`, `147×49 px` inside the scrollable table at `390 px`, `scrollWidth == clientWidth` on both).
  - Base `/entries/walk-creating`: showed `offline` pill, no top banner, and labeled the propagation wait `Health detail:`. Fixed `/entries/walk-creating`: shows amber `creating` pill, top `Provisioning instance:` info banner (`1320×89 px` at `1440 px`, `366×257 px` at `390 px` with `12 px` side margins, zero horizontal overflow), `creating_vm` active stepper pill, and `Status detail:` distinct from `Health detail:`.
  - Before and after `contact-sheet.png` images are attached to the closing note on [Task #3745](/tasks/3745).

*(Harness note: `compute.grant_self_act_as` now defaults to `true` on `main`, so local `registry-fakegcp` configs without ADC or a GCE metadata server must set `grant_self_act_as: false` or `self_principal`, as `creating-walk.sh` does.)*

## Tests run

- `GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` — pass.
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/registry/... ./cmd/grain-registry ./pkg/cli ./tests/hygiene` — all pass (including `TestCoreSaysNoGoogleTokens`).
- `cd ui-registry && npx vitest run` — 20/20 pass.
- `cd ui-registry && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test` — 5/5 pass.
- `make fmt-frontend` — clean across `ui`, `ui-registry`, and `ui-shared`.

## Backport check

`v3.2.0` has already been promoted from `v3.2.0-rc.3` (`refs/tags/v3.2.0`), and `origin/release/v3.2.0` is no longer an active branch on `origin`, so these fixes target `main` for the next release train cut without a separate `release/v3.2.0` backport proposal.
