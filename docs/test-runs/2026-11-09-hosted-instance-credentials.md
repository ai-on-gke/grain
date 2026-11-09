# §4 walkthrough — hosted-instance DB credentials, SA propagation retry (task 3731)

Plan: [`docs/test-plans/2026-11-09.md` §4](../test-plans/2026-11-09.md). Harness and transcripts are in
[`2026-11-09-hosted-instance-credentials/`](2026-11-09-hosted-instance-credentials/).

## Environment

- Sandbox: Debian 12, Go from `go.mod`, PostgreSQL 15 (`apt-get install postgresql postgresql-client`), base `main` @ `0d6a5329`.
- **TLS pool** (`instance-init-walk.sh`): set up like `sql-startup.sh`, with `ssl=on`, a self-signed P-256 cert, a 0600 key and `scram-sha-256`. Its `pg_hba.conf` is **`hostssl` only**, on `127.0.0.1:5432`. Both scripts hard-code 5432 in the DSN they build. A non-TLS control server runs on `127.0.0.1:4272`.
- **`gcpstub.go`** (`127.0.0.1:4271`) is the only Google boundary. It serves the metadata server (`Metadata-Flavor` enforced), Secret Manager (`access`/`create`/`addVersion`/`setIamPolicy`, with injectable 403/404/429/5xx failures), IAM service accounts, CRM IAM policy and Compute `instances`/`operations`. With `sa-delay`, a new service account is invisible to Compute for N seconds: the insert operation completes with `EXTERNAL_RESOURCE_NOT_FOUND`, which is what GCE does. With `-vm-addr 127.0.0.1:8080` it also answers as a healthy hosted VM. The request log never records bodies or `Authorization`. All project names are made up (`made-up-proj`).
- **`registry-fakegcp.go`** is `cmd/grain-registry` with the real `gce` compute and instance-resources providers re-registered with only their base URLs and HTTP client changed. The reconciler, the propagation retry and `provisionPostgresACLs` are all the real code paths. `admin_dsn_template` points at the TLS pool with `sslmode=require` and `cloud_sql_admin: false`, so the built-in password-user path runs. The registry store is PostgreSQL. The IAP/JWKS stub is task 3636's `iapstub.go`.
- Only one registry replica was used for item 2. Items 3–4 (two-replica CLI walk fixes, ui-registry dialogs and screenshots) were **not run**; see [Not done](#not-done).

## Item 1 — `grain-instance-init.sh` and `hosted-vm-startup.sh` against TLS PostgreSQL

Transcripts: [`instance-init.txt`](2026-11-09-hosted-instance-credentials/transcripts/instance-init.txt) and [`hosted-vm.txt`](2026-11-09-hosted-instance-credentials/transcripts/hosted-vm.txt).

| Check | Result |
|---|---|
| Password comes from Secret Manager, not metadata | ✅ `grain-config` holds only `db_password_secret`. Requests are metadata `grain-config` → metadata token → `secrets/…-db-password/versions/latest:access` |
| Password never printed | ✅ 0 hits in the stdout/stderr of every run (success, 403/404/503, retries), in the stub request log, and in the recorded `docker` command lines (hosted-vm). It appears only in `daemon.env` (mode 0600) |
| DSN requires TLS | ✅ `…@127.0.0.1:5432/grain_walk1?sslmode=require`. psql gets `gi_walk1\|t\|TLSv1.3` and pgx (the daemon's driver) gets `ssl=true` |
| Non-TLS server refused | ✅ psql: `server does not support SSL, but SSL was required`. pgx: `tls error: server refused TLS connection` |
| Secret Manager 403 / 404 | ✅ Fails closed and writes no `daemon.env`. 403: `Secret Manager access <secret> failed (HTTP 403)`. 404: `… has no version` |
| Secret Manager 503 | ❌→✅ **Bug 1** (below). It now retries 6 times (1+2+4+8+16 s), then fails closed. 503-twice-then-OK succeeds |
| Pre-existing metadata (`GRAIN_DB_PASSWORD` + `sslmode=disable` `db_dsn`) | ✅ `grain-instance-init.sh` passes the old DSN through unchanged, so the instance keeps working against a pool that accepts `host` connections. Against a `hostssl`-only pool it is refused (`no pg_hba.conf entry … no encryption`); see finding 3. After replacement, `grain-config` carries no password |
| `hosted-vm-startup.sh` | ✅ TLS DSN, 0600 env, no password on any docker argv. ❌→✅ 403/503 used to exit with a bare `curl: (22)` and no retry; now it retries 5xx and names the secret. Under the new script, old-style metadata fails with `incomplete grain-config` (finding 4). The `grain-daemon failed health check` lines are expected: `docker` is a recorder here |

## Item 2 — provisioning against a late-propagating service account

Transcripts: [`provision-late-before.txt`](2026-11-09-hosted-instance-credentials/transcripts/provision-late-before.txt) (base `main`), [`provision-late-fixed.txt`](2026-11-09-hosted-instance-credentials/transcripts/provision-late-fixed.txt) and [`provision-never.txt`](2026-11-09-hosted-instance-credentials/transcripts/provision-never.txt).

- **`SA_DELAY=20`:** the entry stays `creating` with `status_detail` = `waiting for service account gi-walk-late2@made-up-proj.iam.gserviceaccount.com to propagate before creating the GCE instance (attempt 1 failed, retrying in 5s): … EXTERNAL_RESOURCE_NOT_FOUND …`. It moves to attempt 2 / 10s, then reaches `running` at about t+41s. ✅
- **`SA_DELAY=-1`:** attempts 1–6 back off 5/10/20/30/30/… s. At t+132s the entry is `failed` with `… was still not visible to the compute provider after retrying for 2m0s: … EXTERNAL_RESOURCE_NOT_FOUND`. ✅ The bound holds.
- **Secret written once:** ❌→✅ **Bug 2**. On base `main` the 20 s run wrote **3** `grain-inst-walk-before-db-password` versions, one per attempt, and rotated the role's password each time. With the fix, the late and never-propagating runs each write **1**.
- **Failed provision cleanup:** after `failed`, the `…-db-password` secret (1 version), the service account and the `gi-walk-never@made-up-proj.iam` role all remain. Nothing deletes them until the entry is deleted and purged, which runs `Teardown`'s prefix purge. That matches what `failEntry` does for every other provisioning failure. It is recorded here, not changed.

## Bugs found and fixed here

1. **A transient Secret Manager error at boot leaves a hosted VM without `daemon.env`.** `grain-instance-init.service` is `Type=oneshot` with no `Restart=` (`scripts/bake-gce-release-image.sh`). `grain-daemon.service` only orders itself after it (`EnvironmentFile=-/etc/grain/daemon.env`) and starts either way. So a single 503 left the VM with no tenant DSN and no registry verifier until reboot.
   - Repro: `instance-init-walk.sh` step 1c with `count=1` on base.
   - Fix: `sm_access_latest` retries no-connection/429/5xx (`GRAIN_SM_ATTEMPTS`, default 6, and `GRAIN_SM_RETRY_DELAY`, default 1 s doubling). 403/404 are not retried. `hosted-vm-startup.sh` uses `curl --retry 5` and says which secret failed.
   - Regression: `tests/deploy` `TestGrainInstanceInitRetriesTransientSecretManagerErrors`.
2. **Every service-account retry re-provisioned the instance.** That meant a new password, a new Secret Manager version, `ALTER ROLE` and a repeat of `EnsureRepo`/`config-repo-key` `PutSecret`, 5–10 times over the 2-minute window.
   - Repro: `provision-walk.sh` on base (3 versions).
   - Fix: the in-memory `createRetry` keeps the first attempt's bundle and config-repo URL, so later attempts only retry `CreateInstance`.
   - Regression: `pkg/registry/reconcile` `TestReconcilerProvisionsResourcesOnceAcrossServiceAccountRetries`. It fails on base with 4 Provision calls and expects 1.

## Findings not fixed (follow-ups)

3. **The real pool is not `hostssl`-only.** `sql-startup.sh` turns `ssl=on` but keeps the postgres image's default `pg_hba` (`host all all all scram-sha-256`), so a client can still connect in clear. Only the clients enforce TLS (`sslmode=require`), and `require` does not verify the self-signed certificate. Switching to `hostssl` would cut off pre-existing instances whose metadata DSN says `sslmode=disable` (finding 1d). Those instances need replacing first.
4. **Under the new `hosted-vm-startup.sh`, a pre-existing sandbox VM whose metadata lacks `GRAIN_DB_PASSWORD_SECRET` exits `incomplete grain-config`.** This only matters if that VM's `startup-script` metadata is replaced in place. The registry writes metadata only at create time (no `setMetadata` in `compute.go`), so today old VMs keep their old script. This is consistent with `docs/registry.md` ("until replaced").
5. **`grain registry show <name>` never prints `status_detail`.** The propagation reason and the failure reason are only visible through the API (`/api/entries/<name>`). I did not check `grain registry list` or ui-registry, because items 3–4 were not run.
6. **Harness note:** the tenant `CREATE DATABASE … ENCODING UTF8` fails on a pool initialised as SQL_ASCII. Real pools use the postgres image (UTF8), so this is not a product defect, but the error is what an operator would see on a hand-built pool.

## Not done

Items 3 and 4 of the plan were not run in this task's time budget: the walk fixes against two live replicas (`resize` on a connected entry, `transfer -cancel`, tombstone 404 re-register, `access list` `label=`), the ui-registry `ConfirmDialog` focus/Tab/Escape check in Chromium at 1440 and 390 px, and the light/dark screenshots. No UI file was changed. They are filed as a follow-up task.

## Backport

On `origin/release/v3.2.0`, `pkg/registry/reconcile/reconciler.go` has no `createRetry`, because PR #979's propagation retry is not there, so bug 2 does not exist on the release branch. `scripts/grain-instance-init.sh` has no `db_password_secret` path: **PR #969 itself (`fb304423`, `905b52a1`) is not on `release/v3.2.0`**, so the release still puts the tenant password in GCE metadata. The backport proposal is #969 plus bug 1's retry (`sm_access_latest`), which applies to every Secret Manager read the script makes on that branch too.
