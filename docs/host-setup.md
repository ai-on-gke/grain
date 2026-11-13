# Linux Host Operations (`scripts/setup.sh`)

This page covers operational details for a single-machine Linux deployment installed with [`scripts/setup.sh`](../README.md#install): release channels, sandbox modes, directory layout, uninstalling, scheduled backups, and remote hosts.

## Release channels and image selection

By default, a fresh `scripts/setup.sh` deployment runs the `latest` channel (the last promoted `vX.Y.Z` release) pulled from Docker Hub (`docker.io/grainai/grain`), so the host needs no container registry credential.

- **Override the image repository:** set `GRAIN_IMAGE=<registry>/<repo>` (defaults to `docker.io/grainai/grain`).
- **Switch channels:** pass `--channel candidate` (`GRAIN_CHANNEL=candidate`) for the current release candidate, or `--channel main` (`GRAIN_CHANNEL=main`) for the latest build of `main`.
- **Pin one build:** set `GRAIN_IMAGE_TAG=vX.Y.Z` or `GRAIN_IMAGE_TAG=sha-<short-sha>`.
- **Verify signatures:** pass `GRAIN_IMAGE_VERIFY_KEY=/path/to/cosign.pub` ([image-signing.md](image-signing.md)).

Re-running `sudo ./scripts/setup.sh` updates the deployment while keeping its configured channel, UI address, and store. Across schema changes, the new build migrates the store's rows automatically. A store at schema 27 (grain 2.0) or later is migrated **in place**, and no pre-upgrade copy is kept. Only a store the new build cannot migrate in place is moved aside as `store.schemaN-<time>` under the data directory and rebuilt from an export. To be able to go back to an older build, take a backup before you upgrade (see [Downgrading to an earlier build](#downgrading-to-an-earlier-build)).

### Upgrade branch allowlist

The UI's **Upgrade** button, scheduled upgrades, and agent-driven upgrades only install branches matching the daemon's `-upgrade-allowed-branches` (by default `main`, `candidate`, `latest`, and `vX.Y.Z` tags). Override it with `--upgrade-allowed-branches` or `GRAIN_UPGRADE_ALLOWED_BRANCHES`:

```bash
sudo ./scripts/setup.sh --upgrade-allowed-branches 'main,latest,release/*'
```

Patterns use comma-separated `path.Match` syntax (`*` does not cross `/`), or `**` to allow any branch. Because an upgrade runs code built from the target branch, `**` is always an explicit opt-in.

### Cancelling an upgrade, grace window, and rollback

- **Manual grace window:** manual upgrades started from `[Settings → Upgrade](/settings/upgrade)`, `POST /api/upgrade`, or `grain upgrade start` wait for a short countdown (`-upgrade-grace-window` / `GRAIN_UPGRADE_GRACE_WINDOW`, default `10s`, `0` to disable) before pulling, building, or checking the target build. Scheduled upgrades (`-after-tasks-finish`) skip that countdown because they can already be cancelled while draining (`DELETE /api/upgrade/scheduled` or `grain upgrade cancel -scheduled`).
- **Cancelling an in-progress upgrade:** while an upgrade is in its grace window or running any staging step before cut-over (pull/checkout, build, signature verification, health check, sandbox image preparation, schema check/export, or cursor flush), click **Cancel upgrade** in `[Settings → Upgrade](/settings/upgrade)`, run `grain upgrade cancel [-reason <why>]`, or call `DELETE /api/upgrade/current` (`deployment:upgrade`, recorded in `config_audit`). Cancellation cleans up newly pulled or tagged images, check pods, temporary files, and prepared schema exports, re-adopts any runs flushed from run hosts or the host agent, lifts the upgrade queue hold, and finishes in the `cancelled` phase.
- **Point of no return:** once the image reference file (`/etc/grain/image.env`), Kubernetes `Deployment`, or installed binary has been rewritten and the restart has begun, `DELETE /api/upgrade/current` returns `409 Conflict` (`restarting onto <target> — too late to cancel; upgrade back to <previous> afterwards`) and **Cancel upgrade** is disabled with the same message.
- **One-click rollback:** each upgrade persists the digest-pinned image reference (or release/commit on source installs) that it replaced. After an upgrade completes, `[Settings → Upgrade](/settings/upgrade)` displays the replaced build with **Roll back to `<previous>`**, and `grain upgrade rollback` starts the same rollback from the CLI. A source install goes back to the replaced commit by its object id, never to a branch of that name, so the rollback works whatever `-upgrade-allowed-branches` says. A rollback always starts at once; it cannot wait for tasks to finish.

### Downgrading to an earlier build

No grain build opens a store written at a newer schema. It exits with `database schema is newer than this build: found N, this build knows M`. A newer build migrates the store in place on its first start and keeps no copy of it. So putting the older binary or image back on the same data directory does not work across a schema change. Use one of these ways back:

- **The way the deployment upgraded, in reverse.** This is **Roll back to `<previous>`** in [Settings → Upgrade](/settings/upgrade), or `grain upgrade rollback`. Use it when the upgrade itself went through Settings → Upgrade or `grain upgrade` (a `setup.sh` image install, a source install, or Kubernetes). It runs the same schema step as an upgrade. The running build writes the deployment's rows out at its schema, and the older build checks that it can import them (except on Kubernetes). On its first start, the older build moves the newer store aside and rebuilds the store at its own schema from those rows. The newer store goes to `store.schemaN-<time>` on SQLite, or to a Postgres schema named `_schemaN_<time>`. Tasks and runs created since the upgrade are kept. Columns and tables the older schema does not have are dropped, and the daemon logs how many rows it could not carry. The rollback is refused if a dump fixup lies between the two schemas, but there is none after schema 24. The [2026-11-11 walkthrough](test-runs/2026-11-11-secrets-store.md) swapped binaries and did not exercise this path. Its tests are in `pkg/daemon` (`TestSchemaUpgradeStoreRebuildRewindsLoadedHeadToSnapshot`) and `pkg/staterepo` (`TestCheckMigrationSummarizesDroppedRollbackRowsInsteadOfWarningPerFile`).
- **A database from before the upgrade.** Restore it by hand. This works however the deployment was upgraded: a binary swap, `grain init workstation`, a `setup.sh` re-run, or a rollback that was refused. Everything written after the backup is lost.

Downgrading **across the secrets-store move** has two more steps. The move is in v3.5.0: GitHub credentials, repository tokens, the release-approval key and the CA bundles move into the secrets store. An older build reads them only from files, and it cannot parse a secrets key wrapped with Cloud KMS. The full procedure, in order:

1. **Unwrap the secrets key, if it is wrapped.** Settings → State shows how the key is protected. Run `grain state key unwrap` (it needs KMS decrypt), with the daemon up or down. The file then starts `grain-secret-key-v1:`. Skip this step for a plaintext key.
2. **Start the newer build once with `GRAIN_GITHUB_CREDENTIALS=files`.** It writes every store-held credential back to the file an older build reads and drops the store's copy. On a `setup.sh` install, add the line `GRAIN_GITHUB_CREDENTIALS=files` to `/etc/grain/daemon.env`, then run `sudo systemctl restart grain-daemon.service`. The log says `GRAIN_GITHUB_CREDENTIALS=files: wrote … back out of the secrets store` for each git credential, repository token and CA bundle that the store held. `release-approval-key.pem` is back in the data directory, but its restore writes no log line. The older build ignores the variable. Remove the line before you upgrade again, or the newer build writes the credentials back out on every start instead of moving them into the store.
3. **Get the database back to the older schema.** Run this step on the database that step 2 ran against. Then use **one** of these:
   - **`grain upgrade rollback`**, as above. This also does step 4.
   - **Restore a pre-upgrade backup**, with `grain-daemon.service` stopped. A backup qualifies only if the **older** build wrote it, before the newer build first started on this data directory:
     - **A `grain backup` archive** (by hand, or `-backup-interval`), whose `manifest.json` `createdAt` is before the upgrade. On PostgreSQL, `pg_restore --clean --if-exists --no-owner` its `database/grain.dump` into the running server. On SQLite, put its `database/grain.db` back as `$GRAIN_DATA_DIR/store/grain.db`, after moving the newer `store/` directory aside. See [database.md](database.md#backups).
     - **A `pg_dump -Fc` taken before the upgrade.** Restore it the same way.
     - **A cold copy or disk snapshot of the data directory, taken before the upgrade with the daemon (and `grain-postgres.service`) stopped.** Put back only the store: `store/` on SQLite, `postgres/` on the local server. Keep the current `secrets/` directory, because the copy's secrets and credential files are from before the upgrade. Secrets set since then live only in the current directory.
     - **A `grain export` bundle taken before the upgrade, on SQLite only.** On PostgreSQL the bundle has no rows, only `db-config.json`. `grain import -force` restores its secrets and credential files as well as the store, so anything set since the bundle is lost.
     - **A `store.schemaN-<time>` directory or `_schemaN_<time>` Postgres schema**, but only where an upgrade moved one aside (see above). An in-place migration leaves none.

     These do **not** qualify: anything taken after the newer build's first start, which is already at the newer schema. Nor does `$GRAIN_DATA_DIR/schema-upgrade`, which is removed once a migration completes. If no backup qualifies, grain has no supported way to restore the pre-upgrade database. Downgrade with `grain upgrade rollback` instead, or stay on the newer build.

     Do not start the newer build again on the restored store: it migrates the store forward again. A restored store is usually behind the high-water mark ([database.md](database.md#when-a-start-refuses-a-store-behind-the-mark)), because it is missing the tasks and runs created since the backup. Builds since v3.3.0 refuse such a store. For the first start of the older build, set `GRAIN_ACCEPT_STORE_BEHIND=1` in `/etc/grain/daemon.env`, then remove it.
4. **Start the older build.** On a `setup.sh` image install, re-run `setup.sh` with the older build pinned (`GRAIN_IMAGE_TAG=vX.Y.Z`). On a binary install, put the older binary back and restart. Check that the older build clones, pushes and signs. It uses the credential files from step 2, which hold the same credentials it had before the upgrade. The [2026-11-11 walkthrough](test-runs/2026-11-11-secrets-store.md) found the CA bundles and the release-approval key byte-identical.

## Sandboxing and the host agent

- **Sandboxes:** every task run gets an isolated sandbox—a [Granule](https://github.com/gke-labs/granule) VM on hosts with `/dev/kvm`, or a local Docker container when installed with `GRAIN_CONTAINER_SANDBOXES=1`. Container sandboxes are fenced from the host, local networks, and cloud metadata servers via `nftables` (`-container-sandbox-fence`, defaulting to `auto`). When `nft` is unavailable, tasks holding external capability grants are refused until fencing is available.
- **Container daemon + `grain-hostd.service`:** `setup.sh` runs the daemon inside Docker (`grain-daemon.service`) and installs `grain-hostd.service` (`grain host`) alongside it on the host. The host service runs only commands explicitly listed in the root-owned allowlist `/etc/grain/hostd.yaml`. Set `GRAIN_HOSTD=0` to omit the host service and run everything inside the daemon container. See [cli-run-location.design.md](../design/design/cli-run-location.design.md) for trust boundaries and `GRAIN_CLAUDE_RUN`, `GRAIN_CODEX_RUN`, and `GRAIN_AGY_RUN`.

## Directory layout

Everything lives under `/var/lib/grain`, split between persistent state and disposable scratch space:

```text
/var/lib/grain/           root:root 0755
/var/lib/grain/data/      grain:grain 0750 -- GRAIN_DATA_DIR (-data-dir): PostgreSQL (postgres/)
                          or SQLite (store/) task store, secrets, secrets/secrets.key,
                          local-api-token -- back this directory up
/var/lib/grain/scratch/   grain:grain 0755 -- GRAIN_SANDBOX_DIR (-sandbox-dir): sandbox working
                          directories and package, Actions, and CI caches -- disposable
```

Mount `/var/lib/grain/scratch` on a separate disk or partition when possible so a large build or checkout cannot fill the disk holding the task store. Override either path with `GRAIN_DATA_DIR` or `GRAIN_SANDBOX_DIR`.

## Uninstalling

`scripts/setup.sh` removes what it installed (pass the same `GRAIN_USER` if you customized it):

```bash
sudo ./scripts/setup.sh --uninstall   # keeps /var/lib/grain/data and the grain user
sudo ./scripts/setup.sh --purge       # also deletes /var/lib/grain/data and the grain user
```

- `--uninstall` stops and removes `grain-daemon.service`, `grain-postgres.service`, `grain-hostd.service`, the control watchers, metadata-block rules, `/usr/local/bin/grain`, `/usr/local/bin/granule`, `/etc/grain/*`, `GRAIN_SANDBOX_DIR` (`/var/lib/grain/scratch`), `/var/lib/granule/vms`, and Grain Docker images (add `--keep-images` to keep cached images). It preserves `GRAIN_DATA_DIR` (`/var/lib/grain/data`, including `secrets/secrets.key`) so a future install can resume the deployment.
- `--purge` additionally deletes `GRAIN_DATA_DIR` and the `grain` system user. Copy `secrets/secrets.key` somewhere safe first if you ever want to read backed-up secrets again.
- OS packages (`docker.io`, `git`, `iptables`, `nftables`, `tailscale`) are listed in the closing report rather than purged automatically. For source-built `grain init` hosts, see [Uninstalling in grain-init.md](grain-init.md#uninstalling).

## Backups

Back up `/var/lib/grain/data` (`GRAIN_DATA_DIR`), never `/var/lib/grain/scratch`. See [database.md](database.md#backups) for local `pg_dump`, SQLite snapshots, cold copies, and restore steps.

Grain can also upload a credential-free ZIP snapshot (`database/grain.dump` or `database/grain.db`, `manifest.json`, and `config/config-repo.bundle`) to Google Cloud Storage or Google Drive. Run a manual backup on the host with `grain-daemon.service` stopped:

```bash
# Google Cloud Storage
sudo grain backup \
  -data-dir /var/lib/grain/data \
  -gcs-bucket <bucket-name> \
  -google-credentials-file /path/to/credentials.json

# Google Drive
sudo grain backup \
  -data-dir /var/lib/grain/data \
  -google-drive-folder <folder-id> \
  -google-credentials-file /path/to/credentials.json
```

Omit `-google-credentials-file` to use Application Default Credentials. To schedule automatic backups on the running daemon, pass `-backup-interval` (for example `24h`) with `-backup-gcs-bucket` or `-backup-google-drive-folder`. Snapshot archives intentionally omit `secrets/` and `secrets/secrets.key`; back up `secrets/secrets.key` separately.

## Remote hosts

Additional machines can run host-placed agent CLIs or capability servers by connecting `grain host` to the daemon. Register a host in **Settings → Hosts** (or enroll one with an invite code from **Settings → Hosts → Invite hosts**):

```bash
# Connect with a registered per-host token:
grain host -server https://<grain-host> -token-file /etc/grain/hostd.token -name <name>

# Or enroll interactively with an invite code:
grain host -server https://<grain-host> -join <invite-code> -key-file /etc/grain/hostd.key -token-file /etc/grain/hostd.token
```

Each host enforces its own root-owned `/etc/grain/hostd.yaml` allowlist, capacity, and labels. While a host is disconnected, runs pinned to it wait in the queue (`waiting for host <name>`) and in-flight CLI sessions re-attach if the host reconnects within `-hostd-reattach-timeout` (default `15m`). See [host-placement.md](host-placement.md) for placing model connections and capability servers onto hosts.
