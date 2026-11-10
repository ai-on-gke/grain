# Linux Host Operations (`scripts/setup.sh`)

This page covers operational details for a single-machine Linux deployment installed with [`scripts/setup.sh`](../README.md#install): release channels, sandbox modes, directory layout, uninstalling, scheduled backups, and remote hosts.

## Release channels and image selection

By default, a fresh `scripts/setup.sh` deployment runs the `latest` channel (the last promoted `vX.Y.Z` release) loaded from checksum-verified GitHub release assets ([release-assets.md](release-assets.md)), so the host needs no container registry credential.

- **Pull from Docker Hub instead:** set `GRAIN_IMAGE=docker.io/grainai/grain`.
- **Switch channels:** pass `--channel candidate` (`GRAIN_CHANNEL=candidate`) for the current release candidate, or `--channel main` (`GRAIN_CHANNEL=main`) for the latest build of `main`.
- **Pin one build:** set `GRAIN_IMAGE_TAG=vX.Y.Z` or `GRAIN_IMAGE_TAG=sha-<short-sha>`.
- **Verify signatures:** pass `GRAIN_IMAGE_VERIFY_KEY=/path/to/cosign.pub` ([image-signing.md](image-signing.md)).

Re-running `sudo ./scripts/setup.sh` updates the deployment while keeping its configured channel, UI address, and store. Across schema changes, `setup.sh` migrates rows automatically and archives the previous store as `store.schemaN-<time>` under the data directory.

### Upgrade branch allowlist

The UI's **Upgrade** button, scheduled upgrades, and agent-driven upgrades only install branches matching the daemon's `-upgrade-allowed-branches` (by default `main`, `candidate`, `latest`, and `vX.Y.Z` tags). Override it with `--upgrade-allowed-branches` or `GRAIN_UPGRADE_ALLOWED_BRANCHES`:

```bash
sudo ./scripts/setup.sh --upgrade-allowed-branches 'main,latest,release/*'
```

Patterns use comma-separated `path.Match` syntax (`*` does not cross `/`), or `**` to allow any branch. Because an upgrade runs code built from the target branch, `**` is always an explicit opt-in.

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
