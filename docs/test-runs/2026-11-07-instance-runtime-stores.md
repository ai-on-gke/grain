# 2026-11-07 walkthrough: cloud instance runtime, PostgreSQL default, offline migration, backups, object and secret stores

This walkthrough covers [`docs/test-plans/2026-11-07.md`](../test-plans/2026-11-07.md) §2 (task 3486). Every item ran against real `grain` / `grain daemon` processes and a real PostgreSQL server. Google Cloud Storage, Secret Manager, the GCE metadata server and the GitHub webhook gateway were replaced by local stubs in [`2026-11-07-instance-runtime-stores/`](2026-11-07-instance-runtime-stores/). Raw outputs are in [`out/`](2026-11-07-instance-runtime-stores/out/).

**Outcome:** five bugs were found and fixed in this branch, each with a regression test. Two more need changes outside this section's scope, so follow-up tasks were filed for them. Everything else the plan lists behaved as specified.

| # | Finding | Severity | Status |
|---|---|---|---|
| F1 | **Split-brain writes after the PostgreSQL single-writer lease is lost.** Daemon A kept committing for up to one keep-alive interval (5 s) after daemon B took the lease. In the before-fix run that was 43 writes. | High | **Fixed:** `postgres.FenceWrites` + `pkg/daemon/run.go` |
| F2 | **Deployment secret stored and served in the clear when `-object-store` uploads fail.** The fallback copy of the narrative in `task_run.transcript` was redacted only of the sandbox environment, never of the secret store. | High | **Fixed on `main`** by a separate change: with a `TranscriptSink` the narrative is no longer copied into the DB at all. This branch's own fix was removed when main was merged in. |
| F3 | **`grain secrets` (Secret Manager backend) started a new, empty SQLite catalogue next to a PostgreSQL daemon.** The secret went to Secret Manager but was catalogued where the daemon never looks. | Medium | **Fixed:** `refuseUnrecordedCatalogue` in `pkg/cli/secrets.go` |
| F4 | **`grain connections import` ignored `GRAIN_SECRETS_BACKEND=secretmanager`.** It wrote model keys to `<data-dir>/secrets`, so runs failed with "this connection has no Claude Code OAuth token". | Medium | **Fixed:** `connectionSecretStore` in `pkg/cli/connections.go` |
| F5 | **`grain backup -gcs-bucket` could not be pointed at a GCS emulator.** It had no endpoint seam, so §2 item 1's backup round trip could not run without real GCS. | Low (testability) | **Fixed:** `GRAIN_BACKUP_GCS_ENDPOINT` in `pkg/backup` |
| F6 | **The registry's `grain-config` metadata does not match what `grain-instance-init.sh` reads.** The registry writes `GRAIN_ENTRY_NAME`, `GRAIN_SECRET_PREFIX`, … and no owner. The script reads `instance`, `sm_prefix`, … and requires `deployment_owner`. A registry-provisioned VM stops at `missing required field deployment_owner`. | High (managed instances) | Follow-up task (`pkg/registry`, §1's area) |
| F7 | One secret deleted from Secret Manager outside grain makes every transcript upload fail closed. `KnownValues` fails as a whole, and since F2 the DB fallback is dropped too. The catalogue still lists the missing secret until `grain secrets delete`. | Medium | Follow-up task; since addressed on `main` by task 3577 (`grain secrets list` marks keys gone from Secret Manager, and redaction leaves them out instead of failing) |
| O1 | A run's raw `.jsonl` stream is not retried after a failed run-end upload. The narrative is no longer kept in the DB either (F2 on `main`), and the raw stream never reaches the bucket. | Low | Noted in the F7 follow-up |
| O2 | `POST /api/settings` accepts `autoUpgradeOn=daily` on a `-disable-self-upgrade` deployment. The setting has no effect (no `auto_upgrade_run`, no hold) and the UI hides the fields. | Cosmetic | Noted |
| O3 | Sanitized DSNs show the password as `%2A%2A%2A%2A%2A%2A`, which is URL-escaped `******` (Settings → Upgrade Database Backend card, high-water messages). `sanitized_dsn_test.go` pins this format, and `store-highwater.json` compares it, so it was left alone. | Cosmetic | Noted |

## Environment

| | |
|---|---|
| Branch | `grain/098073e4/task-3486-walkthrough-cloud-instance` from `main` at `46ab9012` (schema **125**) |
| Host | Debian 12 sandbox, 7 vCPU, 30 GiB, user `debian` |
| Go / Node | go1.26.10 / node v20.19.0, `ui` built with `npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` |
| PostgreSQL | Debian `postgresql-15` (15.19; the plan asks for 16/17, but bookworm ships 15). Scratch cluster `initdb -D /tmp/pgdata -U grain --auth=scram-sha-256` on `127.0.0.1:4290`. Every `pg_hba` line is `scram-sha-256`, so a connection without a password fails with `fe_sendauth: no password supplied` |
| Stubs | [`gcsstub.go`](2026-11-07-instance-runtime-stores/gcsstub.go): GCS JSON API on `:4293`, with every object mirrored to disk and `/_fail` to inject 503s. [`smstub.go`](2026-11-07-instance-runtime-stores/smstub.go): Secret Manager v1 **REST** on `:4303`. `pkg/secrets` talks REST, not the gRPC the plan names. [`metadatastub.py`](2026-11-07-instance-runtime-stores/metadatastub.py): GCE metadata on `:4305`. A real `github-webhook serve` gateway on `:4297` |
| Harness | [`lib.sh`](2026-11-07-instance-runtime-stores/lib.sh) plus one script per item. Daemons run with `-sandbox-backend host`, `-github-host 127.0.0.1:1`, ports 4291–4309 |

---

## Item 1: offline SQLite → PostgreSQL cutover, stale-store refusal, `grain backup` round trip

Script: [`item1-migrate-backup.sh`](2026-11-07-instance-runtime-stores/item1-migrate-backup.sh). Output: [`out/item1.out`](2026-11-07-instance-runtime-stores/out/item1.out).

| Step | Result |
|---|---|
| Seed SQLite through a live daemon: 3 tasks, a comment, a `github_workflow_run` schedule, secret `walk-secret/token`, host `ws-walk` | ✅ |
| `grain upgrade-postgres` while the SQLite daemon holds `daemon.lock` | ✅ Refused with exit 1 (`a grain daemon is attached to this store … Stop it first with sudo systemctl stop grain-daemon.service`). The target database still had **0 tables** afterwards |
| Stop the daemon, then `grain upgrade-postgres -data-dir … -dsn postgres://grain:<pw>@127.0.0.1:4290/walk_i1` | ✅ 135 tables migrated in about 3.8 s. `db-config.json` = `{"driver":"postgres","dsn":"postgres://grain@127.0.0.1:4290/walk_i1?sslmode=disable","passwordSecret":"database-password"}`. The password appears **0 times** in the file |
| Daemon restarted on the data dir (db-config.json selects PostgreSQL) | ✅ Writer lock taken. Task, comment, secrets (`database-password`, `walk-secret/token`) and hosts all read back |
| Change token | ✅ ETag unchanged with no write. It changed after `POST /api/tasks`, **and** after a `psql UPDATE` from another process, and the API then served the out-of-process title |
| Serialized read-write transactions: 20 concurrent `POST …/comments` | ✅ 20 × 200, 20 rows, no `could not serialize` or deadlock |
| Daemon pointed back at the stale SQLite file (`-db-driver sqlite -db-dsn <data>/store`) | ✅ Refused: `the store this daemon opens, SQLite at …, is behind the writes a previous daemon … made (store-highwater.json …): its last task is #4, but task ids up to #5 were handed out … start once with GRAIN_ACCEPT_STORE_BEHIND=1`. There is no marker inside the SQLite file. The refusal comes from `store-highwater.json`, so a cutover with **no** PostgreSQL writes yet would still start on SQLite. That is by design (`pkg/daemon/highwater.go`) |
| `grain backup -gcs-bucket walk-backups` (offline) into the GCS stub | ✅ After F5: a 76 KB zip with `manifest.json` (`databaseDriver: postgres`), `database/grain.dump` and `config/config-repo.bundle` |
| `pg_restore` into a fresh database | ✅ Exit 0, **135/135 tables, row counts identical table by table**. Task list, comment count and `grain_schema` 125 match |
| Daemon on the restored database | ✅ Serves task 2 with its SQLite-era comment |

**F5.** Before this branch, `pkg/backup.NewGoogleCloudStorage` always called `storage.NewService` against `storage.googleapis.com` with ADC. `STORAGE_EMULATOR_HOST` is not honoured by `google.golang.org/api/storage/v1`, so the walkthrough could not upload a backup anywhere local. `GRAIN_BACKUP_GCS_ENDPOINT` now does for backups what `GRAIN_OBJECT_STORE_GCS_ENDPOINT` already did for `-object-store`: it sets the endpoint, disables authentication and ignores `-google-credentials-file`. Test: `TestGoogleCloudStorageEndpointEnvTargetsAStub`.

Note: `grain backup` refuses to run while a daemon is attached, the same as `upgrade-postgres` does. Backups are therefore offline-only from the CLI. Scheduled backups (`-backup-interval`) run inside the daemon.

## Item 2: single-writer lease, split-brain write fencing, DB webhook cursor

Script: [`item2-lease-fencing.sh`](2026-11-07-instance-runtime-stores/item2-lease-fencing.sh). Outputs: [`out/item2-before-fix.out`](2026-11-07-instance-runtime-stores/out/item2-before-fix.out) (binary built from `main`) and [`out/item2.out`](2026-11-07-instance-runtime-stores/out/item2.out) (this branch).

| Step | Result |
|---|---|
| Daemon A takes the lease. Three signed `issues` webhooks go to the real gateway | ✅ `github_webhook_cursor.last_acked_seq` = 3, and there is no `github-webhook-cursor.json` in the data dir |
| Daemon B (another data dir, same database) starts while A runs | ✅ B logs `… is held by pid=… app="grain-daemon-writer-lock" …; waiting up to 30s for drain`, exits after 30 s with `another grain daemon holds the single-writer advisory lock`, and never binds its UI port |
| **Lease loss under writes.** A writer loop posts comments to A every 50 ms. B waits. A's `grain-daemon-writer-lock` backend is `pg_terminate_backend`-ed, and B takes the lease within about 0.1 s | ❌ → ✅ See F1 |
| Clean cursor handoff: B on a **fresh** data dir in gateway mode | ✅ B starts from the DB cursor (3), consumes event 4 and moves the cursor to 4. Events 1–3 are not replayed |
| Daemon C, a replacement VM in local-listener mode (`-github-webhook-addr`), with an empty `github-webhooks.jsonl` | ✅ `GitHub webhook cursor 4 in the database is ahead of local store … (latest 0), which was replaced; starting from its beginning`. Its first local event (seq 1) moves the cursor to 1 |

**F1: split-brain writes (fixed).** `WriterLock` notices a dead lock session only on its keep-alive tick (`DefaultWriterLockKeepAlive` = 5 s). The before-fix run used the same harness on a binary built from `main`:

```
A's lock session terminated at 2026-11-08 02:41:25.183604+00 (database clock)
B acquired at 2026/11/08 02:41:25
A writes acknowledged 200: 113 (last #113); A writes in the database: 113
A writes committed after its lease was gone: 43 (71,72,...,113)
2026/11/08 02:41:29 grain daemon: PostgreSQL single-writer advisory lock lost: … fencing database pool and stopping immediately
```

For about 4.5 s both daemons were writers. The fix is [`pkg/model/postgres/write_fence.go`](../../pkg/model/postgres/write_fence.go). Every read-write transaction already takes the store's transaction-scoped writer lock (`takeWriterLock`). On a pool the daemon fences (`postgres.FenceWrites(db, writerLock)`, called from `daemon.Run` right after the store opens), the transaction then also checks `pg_locks`: is the lease's advisory lock still granted to this process's lock session? If not, it fails with `ErrWriteFenced` and rolls back. The check happens while the transaction holds the writer lock, and the new holder's transactions queue on that same lock. So every write the old daemon commits is ordered before the new daemon's first write. After the fix:

```
A's lock session terminated at 2026-11-08 03:27:47.52294+00 (database clock)
A writes acknowledged 200: 72 (last #72); A writes in the database: 72
A writes committed after its lease was gone: 0 (none)
api: 500 internal …: postgres: write refused: this daemon no longer holds the single-writer lease on the store (lock session pid 155026 no longer holds it)
```

The fix also removed an ambiguity. Before it, one write could commit while its HTTP caller got a 500 (75 in the database, 74 acknowledged). Now every A write that reached the database was acknowledged. Cost: one `pg_locks` lookup per read-write transaction, on a path that is already serialized. Read-only transactions are untouched. Tests: `TestFenceWritesRefusesWritesOnceTheLeaseSessionIsGone` sets the keep-alive to an hour so that only the fence can refuse; I confirmed it fails with the check disabled. Also `TestFenceWritesNeedsAPool`. The existing `TestWriterLock*` tests still pass.

## Item 3: `-disable-self-upgrade`, `-management-url`, `-ui-require-verifier`

Scripts: [`item3-self-upgrade.sh`](2026-11-07-instance-runtime-stores/item3-self-upgrade.sh), [`item3-screens.mjs`](2026-11-07-instance-runtime-stores/item3-screens.mjs), [`item3b-verifier.sh`](2026-11-07-instance-runtime-stores/item3b-verifier.sh). Outputs: [`out/item3.out`](2026-11-07-instance-runtime-stores/out/item3.out), [`out/item3-screens.out`](2026-11-07-instance-runtime-stores/out/item3-screens.out), [`out/item3b.out`](2026-11-07-instance-runtime-stores/out/item3b.out).

| Step | Result |
|---|---|
| `-management-url` values `javascript:alert(1)`, `ftp://…`, `https://ops:hunter2@…`, `/instances/acme`, `https://`, `data:text/html,hi` | ✅ Each is refused at startup with `-management-url must be an absolute http:// or https:// URL without user credentials`. The credential is stripped from the message (`hunter2` appears 0 times) |
| `http://registry.example.test/instances/acme` | ✅ Accepted. The flag allows plain http, so the plan's "non-HTTPS" case is accepted by design |
| `-disable-self-upgrade -management-url https://… -upgrade-image ghcr.io/…:latest` | ✅ Logs `in-VM self-upgrade is disabled …; upgrades are managed externally at https://…`. `GET /api/config` and `GET /api/upgrade` return `selfUpgradeDisabled:true`, `managementUrl`, `enabled:false` |
| `POST /api/upgrade`, `PUT /api/upgrade/source`, `DELETE /api/upgrade/scheduled`, `GET /api/upgrade/targets`, `GET /api/upgrade/release-notes` | ✅ All return 404 `in-VM self-upgrade is disabled on this deployment (-disable-self-upgrade); upgrades are managed externally` |
| Daily automatic upgrade due **now** (`autoUpgradeOn=daily`, channel `main`, time = current UTC minute) | ✅ Not acted on: no `auto_upgrade_run` row and no upgrade start or hold after two reconcile cycles (`StartUpgrade` is nil). O2: the settings write itself is accepted |
| Playwright on the live daemon: Settings → General (Updates card) and Settings → Upgrade (notice and read-only Database Backend), 1440×900 and 390×844, light and dark | ✅ **40/40 checks.** Management link `href`, no auto-upgrade fields, no Upgrade button in the upgrade section, Database Backend says PostgreSQL with no password and has no buttons or fields, the link stays inside its notice, no horizontal overflow. Contact sheet: [`screens/contact-sheet.png`](2026-11-07-instance-runtime-stores/screens/contact-sheet.png). There were no UI changes, so there is no before/after pair |
| `-ui-require-verifier bogus` / `registry` without keys / `-ui-require-iap` + `-ui-require-verifier registry` / `iap` without audience | ✅ Each refused with its own actionable message |
| `-ui-require-verifier registry` with issuer, audience and keys URL | ✅ Loopback bearer gets 200. The same bearer or `grain_access` cookie with `X-Forwarded-For` gets **401**. No "Enter access code" page is served to network callers. `GET /api/auth/access-code` returns `requireVerifier:"registry"` |
| `grain access-code` against that daemon | ✅ The code goes to stdout. stderr says `the server requires registry (-ui-require-verifier=registry): this code is not accepted over the network …` |
| A server answering `requireVerifier:"registry\e]0;pwned\a\e[2J"` | ✅ stderr falls back to the generic note, with **0** escape bytes |

## Item 4a: `-object-store` over a local GCS stub

Script: [`item4-object-store.sh`](2026-11-07-instance-runtime-stores/item4-object-store.sh). Outputs: [`out/item4-before-fix.out`](2026-11-07-instance-runtime-stores/out/item4-before-fix.out) and [`out/item4.out`](2026-11-07-instance-runtime-stores/out/item4.out).

Daemon A ran with `-object-store gs://walk-objects/instances/a/` (`GRAIN_OBJECT_STORE_GCS_ENDPOINT` → stub) and `-claude-path` set to a stub `claude`. The stub's tool call returns `DEPLOY_TOKEN=<secret deploy/token>`, it mentions the token again in its final text, and it writes a Claude session file. The task is a real no-repo task, dispatched with `max-workers 1`.

| Step | Result |
|---|---|
| What reached the bucket | ✅ `instances/a/transcripts/2-1.jsonl`, `…/2-1.md` and `instances/a/agent-sessions/claude/walk-session.tar.gz` |
| Redaction before the wire | ✅ The secret is in **0** bucket objects. The stored objects carry `DEPLOY_TOKEN=[redacted: secret deploy/token]` |
| Read back through `GET /api/tasks/{id}/attempts/1/transcript` | ✅ Redacted narrative |
| Replacement VM: local `cache/object-store` wiped, daemon restarted | ✅ The transcript is served from the bucket (200, redacted) |
| CI artifacts, per-daemon scoping: daemons A (`instances/a/`) and B (`instances/b/`, another database) share the bucket. Each has a local `state/ci-artifacts/run-77/test.logs.tar.gz` with **the same run, job and name** and different contents | ✅ Both are flushed at startup to `instances/a/ci-artifacts/run-77/test.logs.tar.gz` ("written by daemon a") and `instances/b/…` ("written by daemon b"), with no collision |
| **Fail closed:** uploads under `instances/a/` return 503 during a run | ❌ → ✅ See F2. Nothing reaches the bucket while uploads fail, and no object appears after recovery (O1) |

**F2: secret in the DB fallback (fixed).** When `FlushRunEnd` cannot store the narrative, `pkg/orchestrator/run.go` keeps it in `task_run.transcript` instead. That copy was redacted only by `mcp.SandboxEnvRedactor`: the run's sandbox environment and token shapes. The bucket copy is redacted by every value in the deployment's secret store. So a bucket outage left a *less* redacted transcript in the database, served by the API:

```
before: task 3 narrative via the API (kept in the database instead): … DEPLOY_TOKEN=wlk-deploy-s3cr3t-7f3a9c2e  the token was wlk-deploy-s3cr3t-7f3a9c2e; done
        secret in any task_run row: 1
after:  task 4 narrative via the API (kept in the database instead): … DEPLOY_TOKEN=[redacted: secret deploy/token]  the token was [redacted: secret deploy/token]; done
        secret in any task_run row: 0
```

A sink that redacts more than the sandbox environment now implements `orchestrator.NarrativeRedactor`, which `TranscriptObjectStore` does through `RedactNarrative`. The fallback goes through that redaction. If the redaction itself fails (secret store unreadable), the narrative is **not** kept, consistent with the object store's fail-closed design. Deployments without `-object-store` are unchanged. Tests: `TestFallbackNarrativeUsesTheSinksRedaction` (orchestrator) and new `RedactNarrative` assertions in `TestTranscriptObjectStoreRedactsJSONLAndNarrativeAndFailsClosed`.

**Update (attempt 3, merge of `main`):** `main` fixed the same bug a different way while this branch was open. When `cfg.TranscriptSink` is set, `pkg/orchestrator/run.go` no longer falls back to `store.SetRunTranscript` at all, so no less-redacted copy can reach the database. That change makes `NarrativeRedactor`, `fallbackNarrative`, `RedactNarrative` and their tests dead code, so the merge removed them and took `main`'s version. (The `4f … missing` lines in `out/item4-before-fix.out` are a harness artifact: the stub still held objects from an earlier run, so `FlushLocal`'s `Stat` skipped the upload. `gcsstub.go`'s `/_reset` fixed that before the final run.)

## Item 4b: `-secrets-backend=secretmanager` over a local Secret Manager stub

Script: [`item4b-secret-manager.sh`](2026-11-07-instance-runtime-stores/item4b-secret-manager.sh). Output: [`out/item4b.out`](2026-11-07-instance-runtime-stores/out/item4b.out).

| Step | Result |
|---|---|
| Flag validation | ✅ `-secrets-sm-prefix` without the backend, the backend without a prefix, and prefix `projects/walk-proj` are each refused |
| Daemon `-postgres-url postgres://grain:<pw>@… -secrets-backend secretmanager -secrets-sm-prefix projects/walk-proj/secrets/grain-walk-` | ✅ Starts. As designed, the DSN is **not** recorded in `db-config.json` (`the DSN carries a password and there is no secret store to keep it in`). |
| `grain secrets -data-dir D … set deploy token`, with `-data-dir` only | ❌ → ✅ See F3. It now refuses and creates no `store/` |
| `grain secrets … -db-driver postgres -db-dsn … set/list` (including rotating `multi/a` twice) | ✅ `secret_entries` holds `secret/key → SM id vN`. In Secret Manager the old versions are `DESTROYED` and the latest is `ENABLED`. The secret appears **0** times in a `pg_dump` of the database and 0 times under the data dir |
| `grain connections import` with `GRAIN_SECRETS_BACKEND=secretmanager` | ❌ → ✅ See F4. After the fix the key is upstream as `projects/walk-proj/secrets/grain-walk-agent-backend-claude`, and the stub-agent runs below start |
| Daemon reads: `GET /api/secrets` lists the CLI-made secrets. `PUT /api/secrets/ui-made/key` creates `grain-walk-ui-made-key_…` upstream. A stub-agent run that prints the token, under `-object-store` too | ✅ The run's connection credential was resolved from Secret Manager. The bucket transcript carries `DEPLOY_TOKEN=[redacted: secret deploy/token]`, so redaction read every value back from Secret Manager (6 `:access` calls). The secret is in 0 bucket objects |
| A secret deleted upstream outside grain (`multi/b`) | ⚠️ F7. `grain secrets list` still shows `multi: a, b` (no catalogue sync). The next run's transcript upload **and** its DB fallback are refused (`reading secret store for transcript redaction: reading multi/b: … HTTP 404`), so the run keeps no transcript at all |
| `grain secrets delete multi b` and `delete deploy` | ✅ Upstream 404s are tolerated and the catalogue is repaired (`multi/a ui-made/key` left). The next transcript redaction works again |

**F3: an orphaned secret in a new SQLite catalogue (fixed).** Under the Secret Manager backend, a daemon whose DSN has a password cannot record it in `db-config.json`. So `grain secrets -data-dir D` alone resolved to SQLite, and `daemon.OpenStore` **created** `D/store/grain.db` to catalogue the secret there. The value went to Secret Manager but the daemon never saw it: `GET /api/secrets` listed only `ui-made`. Now, when `-db-driver` was not given, the resolved store is not PostgreSQL, no SQLite store exists yet, and `store-highwater.json` names a non-SQLite store, the CLI refuses:

```
grain secrets: the daemon on /tmp/walk/i4sm writes to PostgreSQL at postgres://grain:%2A%2A%2A%2A%2A%2A@127.0.0.1:4290/walk_i4sm?sslmode=disable (store-highwater.json), but nothing under /tmp/walk/i4sm records how to reach it, so this would start a new, empty SQLite catalogue the daemon never reads; name its database with -db-driver postgres -db-dsn URL (or $GRAIN_DB_DRIVER and $GRAIN_DB_DSN)
exit=1 store created=no
```

Test: `TestSecretsCLIRefusesANewSQLiteCatalogueBesideAPostgresDaemon`. The existing SQLite-catalogue tests are unaffected because they have no high-water mark.

**F4: model keys in the wrong secret store (fixed).** `importConnections` always called `daemon.OpenSecrets(dataDir)`. Under Secret Manager, the daemon then failed every run with `this connection has no Claude Code OAuth token of its own`. It now opens the store `$GRAIN_SECRETS_BACKEND` / `$GRAIN_SECRETS_SM_PREFIX` name, catalogued in the same database. The usage text says so. Test: `TestConnectionsImportUnderSecretManagerStoresTheCredentialThere`.

## Item 5: `grain-instance-init` and the `migrate-local-to-gke.sh` guard

Script: [`item5-instance-init.sh`](2026-11-07-instance-runtime-stores/item5-instance-init.sh). Output: [`out/item5.out`](2026-11-07-instance-runtime-stores/out/item5.out).

| Step | Result |
|---|---|
| No `grain-config` attribute (metadata 404) | ✅ `skipping instance init`, exit 0, 0 files written |
| `deployment_owner` missing / `alice` (not an email) | ✅ Exit 1 with `missing required field deployment_owner` / `must be an email address` |
| Full config: `instance`, `deployment_owner`, `owner_group`, `issuer`, `instance_domain`, `sm_prefix`, `sql_instance`, `object_store` | ✅ Exit 0. Modes: `daemon.env` **600**, `/run/grain-secrets` 750, both `local-api-token` copies **600**, data dir 750, `cloud-sql-proxy.env` 644. `daemon.env` carries `GRAIN_DEPLOYMENT_OWNER`, `GRAIN_ALLOWED_PRINCIPALS=user:alice@…,group:platform@…`, the registry verifier triple + `GRAIN_REQUIRE_VERIFIER=registry`, `GRAIN_DISABLE_SELF_UPGRADE=true`, the derived `GRAIN_MANAGEMENT_URL=https://registry.example.test/entries/calm-otter`, a Cloud SQL IAM DSN (`…@walk-proj.iam@/grain_calm_otter?host=…/cloudsql/…`), the Secret Manager backend, `GRAIN_OBJECT_STORE` and `-deployment-owner=alice@example.com`. The generated `local-api-token` was stored as version 1 in Secret Manager |
| Second boot | ✅ The token is read back from Secret Manager, not regenerated |
| A real `grain daemon` started from that `daemon.env` (DSN redirected to the local cluster, registry verifier left out because there is no JWKS) | ✅ Self-upgrade disabled with the management URL. `/api/access/principals` seeds `user:alice@example.com` and `group:platform@example.com` as `grain/operator`. `DELETE` of the owner gets **400** `is the deployment owner (-deployment-owner) and cannot be removed`. The daemon created its capability secrets in Secret Manager |
| `migrate-local-to-gke.sh --dry-run`, scratch root (`GRAIN_*_DIR`), stub `kubectl` | ✅ **Refused:** `grain-postgres.service` + `db-config.json` → `127.0.0.1:5432`. **Refused:** no unit, only `secrets/postgres-password` with no DSN. **Allowed** past the guard: unit left behind with the store moved to `db.internal.example`. **Allowed:** unit on 5433 with the DSN on `localhost:5432`. **Allowed:** an SQLite host. The allowed cases stop later at the hostd check, as expected |
| **The `grain-config` the registry actually writes** (`pkg/registry/reconcile/reconciler.go` `startupEnv`) | ❌ F6: `grain-instance-init: ERROR: grain-config metadata is missing required field deployment_owner`, and no `daemon.env` is written |

**F6: registry ↔ instance-init contract (follow-up).** The reconciler writes `GRAIN_ENTRY_NAME`, `GRAIN_REGISTRY_URL`, `GRAIN_JWKS_URL`, `GRAIN_SQL_INSTANCE`, `GRAIN_SQL_CONNECTION_NAME`, `GRAIN_DB_NAME`, `GRAIN_SECRET_PREFIX` (an id prefix, not `projects/<p>/secrets/<prefix>`), `GRAIN_GCS_BUCKET`, `GRAIN_GCS_PREFIX`, `GRAIN_CONFIG_REPO` and `GRAIN_VERSION`, with **no owner**. `scripts/grain-instance-init.sh` reads `instance`/`name`, `deployment_owner`/`owner_email`, `issuer`, `sql_instance`, `db_name`, `sm_prefix`, `object_store` and `config_repo`. The registry e2e (`pkg/registry/e2e_test.go`) never runs the script. Its fake compute interprets the `GRAIN_*` keys itself, so CI cannot catch this. Fixing it needs the reconciler to send the entry's owner and the project-qualified Secret Manager prefix, and one side to adopt the other's key names. That is `pkg/registry` (§1's area), so it is filed as a follow-up rather than changed here.

---

## Code changes in this branch

| File | Change |
|---|---|
| `pkg/model/postgres/write_fence.go` (new), `adapter.go` | `FenceWrites` / `ErrWriteFenced`: a read-write transaction confirms the single-writer lease under the writer lock (F1) |
| `pkg/daemon/run.go` | Fences the store's pool with the daemon's `WriterLock` (F1) |
| `pkg/cli/secrets.go` | `refuseUnrecordedCatalogue` (F3) |
| `pkg/cli/connections.go` | `connectionSecretStore` plus a usage note (F4) |
| `pkg/backup/google_cloud_storage.go` | `GRAIN_BACKUP_GCS_ENDPOINT` (F5) |
| Tests | `write_fence_test.go`, `secrets_sm_test.go` (+2), `google_cloud_storage_test.go` (+1) |

Local verification: `go test ./pkg/model/postgres/... ./pkg/backup/... ./pkg/objectstore/... ./pkg/orchestrator/... ./pkg/cli/... ./pkg/daemon/... ./tests/deploy/... ./tests/hygiene/...` all `ok`. The PostgreSQL tests ran against the scratch cluster through `GRAIN_POSTGRES_TEST_URL`. `GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` reported no spec drift.

## Reproducing

```sh
sudo apt-get install -y postgresql postgresql-client sqlite3
/usr/lib/postgresql/15/bin/initdb -D /tmp/pgdata -U grain --pwfile=<(echo pgpass-walk) --auth=scram-sha-256
/usr/lib/postgresql/15/bin/pg_ctl -D /tmp/pgdata -o "-p 4290 -k /tmp -c listen_addresses=127.0.0.1" -l /tmp/pg.log start
(cd ui && npm ci && npm run build) && go build -o /home/debian/bin/grain ./cmd/grain && go build -o /home/debian/bin/github-webhook ./cmd/github-webhook
cd docs/test-runs/2026-11-07-instance-runtime-stores
go run gcsstub.go -addr 127.0.0.1:4293 -dir /tmp/walk/gcs &   go run smstub.go -addr 127.0.0.1:4303 &
bash item1-migrate-backup.sh; bash item2-lease-fencing.sh; bash item3-self-upgrade.sh
(cp item3-screens.mjs ../../../ui/node_modules/.walk.mjs && cd ../../../ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node node_modules/.walk.mjs)
bash item3b-verifier.sh; bash item4-object-store.sh; bash item4b-secret-manager.sh; bash item5-instance-init.sh
```

`item3-screens.mjs` imports `playwright`, so it has to sit under `ui/` to resolve it. `item4b` reuses the stub agent `item4` writes. To reproduce F1 before the fix, run `GRAIN=<binary built from main> bash item2-lease-fencing.sh`.
