# Walkthrough — Grain Registry Phase 2: delegating apps, consent grants, `scope_owners`, stop/start & recovery (2026-11-08 §1)

**Plan:** [`docs/test-plans/2026-11-08.md`](../test-plans/2026-11-08.md) §1 (feature PRs #809, #852, #854, #864, #865, #866, #869, #872, #876, #878, #879, #883, #884, #914).
**Task:** [Task #3636](/tasks/3636), filed by [Task #3495](/tasks/3495).
**Base:** `main` at `468b48f7` (PR #970).
**Harness & transcripts:** [`2026-11-08-grain-registry-phase2/`](2026-11-08-grain-registry-phase2/).

## Outcome

Everything in §1 was run live: two real `grain-registry` OS processes sharing one PostgreSQL 15 database, the IAP/JWKS stub from the 2026-11-07 walk, a real tunnelled `grain daemon -registry-connect -ui-require-verifier=registry`, the compiled `grain` CLI, and Playwright Chromium.

The walk found **five bugs, fixed here, each with a regression test**, plus **one design gap in rolling drains, filed as a follow-up**:

| # | Finding | Severity | Status |
|---|---|---|---|
| F1 | `grain registry resize -disk-gb 200 <connected-entry>` was accepted. It stored `boot_disk_gb=200` on the connected entry and moved it to `lifecycle_state=replacing`, and nothing ever moved it back. From then on the registry answered every request for the entry with the "currently replacing" page (503) instead of routing to its live tunnel. The plan expected "connected entry refusal". | High: one CLI call takes a connected grain offline | **Fixed**: `POST /api/entries/{name}/replace` refuses non-instance entries with `invalid_entry_kind`, as stop/start already do. Test: `TestReplaceRefusesConnectedEntries` (PostgreSQL) |
| F2 | **Rolling drain with a long-lived stream open returns 503.** If an SSE stream (the grain UI keeps `/api/events?stream=1` open in every tab) or a WebSocket is open on a replica's tunnel when that replica gets SIGTERM, `DrainAndClose` stops new streams at once. It does not let go of the tunnel until the stream ends or `drain_timeout` passes, and the grain is not told to redial elsewhere until then. New requests for the entry, through either replica, wait in `reconnect_grace` and then get **503 `entry_offline` (`disconnect_reason: graceful shutdown`)**. With the defaults (`drain_timeout: 30s`, `reconnect_grace: 2s`), that is up to ~28 s of 503s per entry on every rolling restart. With no long-lived stream, the same drain hands off with zero 503s. | High: breaks the zero-503 handoff PR #869 promised, in the common case | **Not fixed here** (design change in tunnel draining). Reproduction below; follow-up task proposed |
| F3 | GET `/api/entries/{name}` answered 403 "caller does not have permission to view this entry" for **somebody else's deleted tombstone**. `grain registry register` probes that endpoint before creating. So after an admin purge with `release_reservation: true`, `GET /api/names/<name>` said `available`, but nobody else could claim the name through the CLI. | Medium | **Fixed**: non-managers get 404 for tombstones (owners and admins still see them, to restore or purge). This also stops revealing that the deleted entry exists. Test: `TestOthersDeletedEntryReadsAsNotFound` |
| F4 | The shared `ConfirmDialog` behind every ui-registry confirmation (Stop, Start, Delete, purge, revoke) was `aria-modal` but ignored the keyboard. Escape did not close it, focus stayed behind it, and after it closed focus was left on `<body>`. | Medium (accessibility) | **Fixed** in `ui-shared/primitives.jsx`: focus goes to Cancel on open, Tab is trapped, Escape cancels, focus returns to the opener. Test: `App.test.jsx` Stop/Start test, plus the Playwright walk |
| F5 | `grain registry transfer` had no way to withdraw a pending offer. The registry has accepted `{"cancel": true}` since the two-step transfer landed, so the plan's "transfer → cancel → re-offer" could only be done with raw curl. | Low | **Fixed**: `grain registry transfer -cancel <name>` (CLI spec regenerated, `docs/registry.md` row). Test: `TestRegistryCLITransferCancel` |
| F6 | `grain access list` (text output) dropped a `delegate:` entry's `--label`. Only `-json` showed it. | Low | **Fixed**: the line ends with `label=<name>`. Test: `TestAccessAllowDelegateCLI` |

Smaller observations, not changed:
- An admin force-transfer to the entry's **current** owner (`POST /api/admin/entries/conn-alice/transfer {"to": <owner>}`) is accepted as a no-op ownership change, but it still revokes every `grc_…` credential and disconnects the tunnel. This walk hit it once and had to re-mint the daemon's credential.
- `grain registry show` does not print `boot_disk_gb`. The value is only visible in `-json` and in PostgreSQL.
- `grain registry register` against a local daemon without `-host-kind` resets `host_kind` to `other`. The 2026-11-07 walk saw the same with `-display-name`.
- The daemon ignores a changed `-registry-token-file` once `grain_config.registry` holds a token. A credential rotated with `rotate-credential -token-file` only takes effect after `grain registry register -rotate-token` on the daemon.
- Routing treats bare `acme/x` and `github.com/acme/x` as different repositories, so `route github.com/acme/billing` does not find a bare `acme/billing` claim. That looks deliberate; only `scope_owners` matching folds the host.

## Environment

- Debian 12 sandbox, Go 1.26.10, Node 20.19.0, PostgreSQL 15.19 (`apt-get install postgresql postgresql-client socat`), Playwright Chromium 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- Built from `HEAD`: `cd ui && npm ci && npm run build`, `cd ui-registry && npm ci && npm run build`, `go build -o /home/debian/bin/grain ./cmd/grain`, `go build -o /home/debian/bin/grain-registry ./cmd/grain-registry`. After each fix the binaries were rebuilt and the affected walk re-run.
- Topology ([`env-up.sh`](2026-11-08-grain-registry-phase2/env-up.sh), 42xx ports):

| Port | Process |
|---|---|
| 4240 | PostgreSQL (`initdb` cluster under `/tmp/gr2/pg`, database `registry`) |
| 4250 | IAP JWKS stub (`docs/test-runs/2026-11-07-grain-registry/iapstub.go`) |
| 4251 → 4252 (public 4253, peer 4254) | IAP front proxy → replica A |
| 4261 → 4262 (public 4263, peer 4264) | IAP front proxy → replica B |
| 4259 | [`frontlb.go`](2026-11-08-grain-registry-phase2/frontlb.go): health-checked TCP balancer (prefers A, skips any replica whose `/healthz` is not 200). The daemon dials the registry through it, so a drain can move its tunnel |
| 4270 | `grain daemon` (entry `conn-alice`, owner `alice@example.com`), started by [`start-daemon.sh`](2026-11-08-grain-registry-phase2/start-daemon.sh) |
| 4280 | `grain-registry demo` for the UI walk |

- Registry config: `auth.provider: iap`, `allowed_domains: [example.com]`, admin `root@example.com`, `service_principals` = two `serviceAccount:` SAs (`ci-bot@walk-proj…`, `other-bot@walk-proj…`), `delegation_policy: consent`, static groups `acme-owners@example.com: [carol]` and `eng@example.com: [alice, bob]`, `tunnel.reconnect_grace: 5s`, `drain_timeout: 5s`, `compute.provider: none`. `SCOPE_OWNERS=1` adds `acme/*` → `group:acme-owners@example.com` and `gitlab.example.com/acme/*` → `user:alt@example.com`.
- Callers are simulated by `X-Test-Email` / unsigned JWTs that the IAP stub re-signs as real ES256 IAP assertions (`lib.sh`'s `fakejwt`). `compute.provider: none` provisions nothing, so the walk seeded one `standard` shape and stood in for the reconciler by setting `lifecycle_state` in PostgreSQL (shown in the transcripts as `sql>` lines).
- Each replica allows a person 10 control-plane mutations a minute. The walks therefore spread grant creation over replica B, keep revocation and narrowing on replica A as the plan asks, and the lifecycle parts restart the replicas between parts.

## Item 1 — `delegation_policy: consent`, the CLI, cross-replica eviction and `delegate:`

Transcript: [`delegation-walk.txt`](2026-11-08-grain-registry-phase2/transcripts/delegation-walk.txt). Migration locking: [`lock-probe.txt`](2026-11-08-grain-registry-phase2/transcripts/lock-probe.txt).

- **Migrations / `pg_advisory_lock`.** Both replicas started together against an empty database. Schema reached 7 (`5 fleet_upgrades_and_stop_start`, `6 transfer_expiry_and_admin_recovery`, `7 custom_boot_disk_gb`), 7 rows with 7 distinct versions, and neither log has an error. [`lock-probe.sh`](2026-11-08-grain-registry-phase2/lock-probe.sh) then held `pg_advisory_lock(0x67726e726567)` from psql for 4 s while two fresh replicas started on a new database. For 3 s `pg_locks` showed **2 ungranted advisory waiters** and both `/healthz` were down. Both came up 3.66 s after start, as soon as the lock was released, with exactly 7 migrations applied.
- **Delegating apps.** `grain registry delegating-apps create` as alice → `403 admin privileges required`. A human principal → refused ("is a person, not a service account"). A bare `ci-bot@walk-proj.iam.gserviceaccount.com` was **normalized to `serviceAccount:ci-bot@…`** (CLI output, `list` on B, and the `registry.delegating_apps` row). `-max-access read -max-grant-ttl 1h` was stored as `max_access=read ttl=3600`.
- **Grants.** Requesting `write` from the read-only app → `403 exceeds_max_access`. A `-ttl 10h` grant to `reader-app` was **capped at 1h** (expiry 1h out). `grants list` on B showed alice's grants; bob saw none. `-include-revoked` listed revoked grants with their `revoked` time.
- **Delegated traffic.** `X-Grain-On-Behalf-Of: alice@example.com` from `ci-bot` onto `conn-alice.grain-user.test` returned 200 through both A and B. The daemon's `/api/whoami` reported `actor=service ci-bot…`, `onBehalfOf=alice`, `via=ci-bot`, `verified=true`. A POST under a read grant → `403 read_only_grant`. bob (no grant) → `403 no_grant`.
- **Eviction across replicas, `<1s`.** Both replicas' caches were warmed, then each change was made on **A** while **B** was polled every 25 ms. B refused:
  - (a) after narrowing a write grant to read: **14 ms**, `read_only_grant`;
  - (a′) after narrowing entries away from `conn-alice`: **13 ms**, "grant does not cover entry";
  - (b) after a revoke by `dlg_…` ID: **12 ms**, `no_grant`;
  - (b′) after a revoke by app name: **12 ms**;
  - (c) after `delegating-apps disable ci-bot`: **12 ms**, `app_disabled`.

  After `enable`, the still-live grant worked again. `pg_stat_activity` shows one `LISTEN registry_delegations` session per replica.
- **Refusals.** bob revoking alice's grant by ID or by app name → `404 you have no live delegation grant` (indistinguishable from a missing grant), and B kept serving alice. Grant creation for a disabled app → `403 app_disabled`. Delegating-app calls to `POST /api/entries/conn-alice/stop` and `/start` → `403 service principals may only call read-only catalog endpoints`. With `X-Grain-On-Behalf-Of` on the registry host → `400 delegation_not_supported`.
- **Downstream `delegate:` allowlist.** `grain access allow --label CI_bot …` → refused by the `[a-z][a-z0-9-]{0,31}` rule. `--label ci-bot --role grain/delegated --reason …` → allowed. `reader-app`, which alice did grant but the daemon does not list, got **403 `delegate_not_allowed` from the daemon**. `grain access deny delegate:ci-bot…` flipped ci-bot from 200 to 403 `delegate_not_allowed`, and re-allowing restored 200. (F6: the text `access list` did not show the label.)

## Item 2 — `catalog.scope_owners`

Transcripts: [`scope-owners-seed.txt`](2026-11-08-grain-registry-phase2/transcripts/scope-owners-seed.txt), [`scope-owners-enforce.txt`](2026-11-08-grain-registry-phase2/transcripts/scope-owners-enforce.txt).

- **Seed, with no policy.** bob registered `bob-ws` with `acme/billing:primary` and `github.com/acme/payments:primary`, and `route` showed bob-ws as primary for both.
- **Policy enabled.** Both replicas restarted with `SCOPE_OWNERS=1`. The startup sweep logged "demoted unauthorized primary scopes on 1 entries".
  - (b) `show bob-ws` and `GET /api/directory?repo=acme/billing` list both scopes as `role=secondary effective_role=secondary`. `route` no longer returns bob-ws as primary for `acme/billing`, `github.com/acme/billing`, `acme/payments` or `github.com/acme/payments`. A connected-entry heartbeat re-declaring both as `primary` on the public listener came back with `active_scopes` `role=secondary`, and `route` stayed unchanged.
  - (a) Primary claims by bob → **403 `scope_owner_forbidden`**:
    - `scopes -set acme/repo:primary`, `github.com/acme/repo:primary`, and case-folded `GitHub.com/ACME/repo:primary` (normalized and still refused);
    - `create -scope acme/repo:primary` and `create -scope github.com/acme/repo:primary`;
    - `register … -scope github.com/acme/repo:primary`.

    `acme/repo:secondary` is still allowed. carol (in `acme-owners`) can register `acme/repo:primary` and set `github.com/acme/other:primary`.
  - (c) alt registered `gitlab.example.com/acme/repo:primary` (routed as primary). carol, an `acme/*` owner, was **refused** `gitlab.example.com/acme/repo2`, because the host rule overrides the bare rule. alt was refused bare `acme/repo3`.

## Item 3 — transfer, purge, `-disk-gb`, stop/start, instance init and drain

Transcripts:
- [`lifecycle-disk-stop-start.txt`](2026-11-08-grain-registry-phase2/transcripts/lifecycle-disk-stop-start.txt) (after the F1 fix)
- [`lifecycle-transfer.txt`](2026-11-08-grain-registry-phase2/transcripts/lifecycle-transfer.txt) (after F5)
- [`lifecycle-walk-run1.txt`](2026-11-08-grain-registry-phase2/transcripts/lifecycle-walk-run1.txt) (first pass: F1 and F3 reproductions, and the delete/restore/purge results)
- [`instinit.txt`](2026-11-08-grain-registry-phase2/transcripts/instinit.txt)
- [`drain-walk.txt`](2026-11-08-grain-registry-phase2/transcripts/drain-walk.txt) and [`drain-walk-no-sse.txt`](2026-11-08-grain-registry-phase2/transcripts/drain-walk-no-sse.txt)

- **`-disk-gb`.**
  - The CLI refuses 19 and 4001 on `create` and `resize`. The server refuses them on `POST /api/entries` (`boot_disk_gb must be between 20 and 4000 GB`) and on `/replace`.
  - 20 and 4000 are created (`boot_disk_gb` 20 / 4000). A third create hit carol's instance quota (`2 + 1 exceeds limit 2`).
  - `resize -disk-gb 200` → `replacing` with `boot_disk_gb=200`.
  - `resize -disk-gb 200 conn-alice` was **accepted before the fix (F1)** and refused with `invalid_entry_kind` after it.
- **Stop/start.** bob → 403. `conn-alice` stop/start → `400 invalid_entry_kind`. A running VM → `stopping`, and a repeat stop is idempotent. With the reconciler's part played (`stopped`), `list` shows `stopped`, and `start` (from A, then again from B) → `creating`.
- **Two-step transfer of `conn-alice` with a live tunnel.**
  1. Offer, then `transfer -cancel` (F5). carol's accept → 403.
  2. Re-offer: `transfer_expires_at` is 7 days out. Expired in PostgreSQL, carol's accept → `409 … offer to carol@example.com expired at …`.
  3. Fresh offer → accept. The old `grc_…` heartbeat → `401 invalid or revoked credential`, the tunnel is closed with `last_disconnect_reason=ownership transferred` (carol gets `503 entry_offline`), and alice → 403.
  4. Admin force-transfer: bob → 403, root → 200, audited as `admin_entry_force_transfer`.
- **Delete / restore / purge.**
  - carol: `delete` → `restore` (running) → `delete`. Purge as carol → 403. Admin purge `{}` → `restore` refused ("disk/database has already been purged"), and `GET /api/names/carol-ws` is `reserved` until +30 days.
  - alt-ws: admin purge `{"release_reservation": true}` → `restore` refused, and the name is `available`. Before the F3 fix bob's `register alt-ws` failed with "permission to view this entry". A direct `POST /api/entries` by bob succeeded, which showed the name really was free.
  - With `db_purge_after` set in the past, `restore carolvm2` → refused.
- **`grain-instance-init.sh` against live data.** [`instinit.go`](2026-11-08-grain-registry-phase2/instinit.go) loaded the walk's `registry-a.yaml`, read instance `alice-vmb` from the live store, and called `reconcile.InstanceGrainConfig` with a GCP-provider-shaped bundle. It then ran `scripts/grain-instance-init.sh` against local metadata and Secret Manager stubs.
  - The metadata carries 26 keys: the `GRAIN_*` set plus snake_case `instance`, `deployment_owner`, `issuer`, `audience`, `jwks_url`, `management_url`, `sql_instance`, `db_name`, `sm_prefix`, `object_store` and `instance_domain`. Empty `owner_group` / `config_repo` / `db_user` / `db_dsn` are omitted.
  - The script exited 0, generated and stored the 64-byte `local-api-token` secret, and wrote `daemon.env`: deployment owner, verifier `registry` with audience `https://alice-vmb.grain-user.test`, an IAM Cloud SQL DSN over the proxy socket, the SM prefix and the object store. It also wrote `cloud-sql-proxy.env`.
- **Rolling drain** ([`drain-walk.sh`](2026-11-08-grain-registry-phase2/drain-walk.sh)). The tunnel is placed on A, a long-lived SSE stream (`/api/events?stream=1`) and a 2.3 MB rate-limited asset download are opened through A, and `/api/whoami` is polled every 50 ms through B and through the front LB. Then A gets SIGTERM.
  - **With the SSE stream:** A drained for 5.0 s (`drain_timeout`). The tunnel was released only at the end, the daemon redialed onto B 1.3 s later, and **both clients got exactly one `503 entry_offline` (`disconnect_reason: graceful shutdown`)** at the moment the session closed (91×200 + 1×503 each). The SSE stream was cut when A exited. Reproduced on two consecutive runs. This is **F2**.
  - **With no long-lived stream (`NO_SSE=1`):** A exited 58 ms after SIGTERM, the daemon redialed within a second, and **189×200, zero 503s** went through each of B and the LB. The in-flight download completed.

### F2 analysis

`Router.BeginDrain` → `ActiveTunnel.DrainAndClose(drain_timeout)` marks the tunnel superseded, so it opens no new streams. It then waits for `ActiveStreams()` to reach 0 before `CloseWithReason("graceful shutdown")`. A request arriving in that window (locally, or peer-forwarded from B) cannot open a stream, so it goes to `handleOfflineOrWait` and waits up to `reconnect_grace` for a reconnect. That reconnect cannot happen while the grain's only session is still held open by the draining replica. An SSE or WebSocket stream never reaches 0 on its own, so whenever `drain_timeout` ≥ `reconnect_grace` (the defaults are 30 s vs 2 s), requests in the window time out with 503.

Possible fixes, for the follow-up:
- tell the grain to dial a second session as soon as the drain starts (a mux GOAWAY), keeping the old session only for its in-flight streams; or
- leave long-lived streams (SSE `Accept: text/event-stream`, upgrades) out of the drain wait, since clients reconnect those anyway.

`TestARequestCaughtByADrainFollowsTheTunnel` covers only the idle-tunnel case.

## Item 4 — Playwright Chromium walk of `ui-registry`

[`ui-walk.mjs`](2026-11-08-grain-registry-phase2/ui-walk.mjs) runs against `grain-registry demo`. Transcript: [`ui-walk.txt`](2026-11-08-grain-registry-phase2/transcripts/ui-walk.txt), **54 checks, 0 problems** after the F4 fix.

- **Stop modal in My entries, at 390×844 and 1440×900.** The modal names the entry and fits the viewport (480 px wide on desktop). **Escape closes it** (before F4 it stayed open and the walk timed out) and **focus returns to the Stop button**. Confirming shows the `stopped` badge.
- **Entry detail for `docs-preview` (stopped).** The `stopped-banner` reads "…visit its URL to wake it" (auto-wake on). Start is offered, its confirmation also closes on Escape, and confirming moves the entry out of `stopped`.
- **`/grants`.** "Apps acting for you" renders.
- **`/admin#delegating-apps`.** The delegating apps panel renders, and `/admin` shows the recovery panels: quotas, scope conflicts, SQL pool drain, orphan VMs, tunnel disconnect and reconciler lease.
- **Overflow sweep.** `/`, `/grants`, `/admin`, `/admin#delegating-apps`, `/entries/docs-preview` and `/entries/platform-vm` were checked at 1440×900 and 390×844 in light and dark (24 renders). Nothing overflows horizontally and there are no page errors.
- **Before/after screenshots.** `node ui-registry/e2e/screenshot.mjs --widths desktop,phone` was run for `/`, `/grants`, `/admin`, `/admin#delegating-apps` and `/entries/docs-preview`, on both the base build and the fixed build. The page renders are identical. F4 changes behaviour, not layout; the only visible difference is the focus ring on Cancel while a dialog is open.

## Tests run

- `GRAIN_REQUIRE_POSTGRES=1 PATH=/usr/lib/postgresql/15/bin:$PATH go test ./pkg/registry/... ./cmd/grain-registry ./pkg/cli`: all pass. The new tests fail without their fixes; checked by reverting each fix.
- `GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` (regenerated `design/spec/cli/host.spec.md` for `-cancel`).
- `cd ui-registry && npx vitest run`: all pass. `make fmt-frontend`: clean.

## Reproducing

```bash
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client
(cd ui && npm ci && npm run build); (cd ui-registry && npm ci && npm run build)
go build -o /home/debian/bin/grain ./cmd/grain && go build -o /home/debian/bin/grain-registry ./cmd/grain-registry
D=docs/test-runs/2026-11-08-grain-registry-phase2
$D/env-up.sh && $D/start-daemon.sh            # pg + IAP stub + replicas A/B + tunnelled daemon
$D/lock-probe.sh                              # pg_advisory_lock migration serialization
$D/delegation-walk.sh                         # item 1
phase=seed $D/scope-owners-walk.sh; SCOPE_OWNERS=1 RESTART=1 $D/env-up.sh; phase=enforce $D/scope-owners-walk.sh
go build -o /tmp/gr2/frontlb $D/frontlb.go && /tmp/gr2/frontlb -addr 127.0.0.1:4259 \
  -backend 127.0.0.1:4251=http://127.0.0.1:4253/healthz -backend 127.0.0.1:4261=http://127.0.0.1:4263/healthz &
GRAIN_SERVER=http://127.0.0.1:4270 GRAIN_DATA_DIR=/tmp/gr2/d1 grain registry register -registry-url http://127.0.0.1:4259 \
  -registry-token "$(. $D/lib.sh; fakejwt alice@example.com)" -entry conn-alice -listed
$D/drain-walk.sh; NO_SSE=1 $D/drain-walk.sh   # F2 with and without a long-lived stream
part=disk $D/lifecycle-walk.sh; part=transfer $D/lifecycle-walk.sh; part=purge $D/lifecycle-walk.sh
go run $D/instinit.go -config /tmp/gr2/registry-a.yaml -entry alice-vm
grain-registry demo -addr 127.0.0.1:4280 &
(cd ui-registry && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node ../$D/ui-walk.mjs /tmp/gr2/shots http://127.0.0.1:4280)
```
