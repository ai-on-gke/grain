# The task store: PostgreSQL or SQLite

grain keeps its task store in one of two databases:

- **PostgreSQL**. This is the default for a new `scripts/setup.sh` install.
  The installer provisions a local server for it, `grain-postgres.service`.
  You can also point grain at a server you run yourself, such as Cloud SQL.
- **SQLite**, embedded in the daemon. It lives in `$GRAIN_DATA_DIR/store`.
  Installs made before PostgreSQL became the default use it, and they keep
  using it until you move them.

## SQLite vs Postgres: when to keep SQLite

PostgreSQL is the better default for a long-lived deployment. It runs as a
separate process, so the daemon is never the only thing holding the store
open. It also has standard dump-and-restore tools, and you can move it onto
a managed server later.

Keep SQLite when:

- **The host is small or short-lived.** SQLite has no second process to
  run, no image to pull and nothing listening on a port. A demo or a
  throwaway VM doesn't need any of that. `grain demo` and the test
  harnesses use SQLite for this reason.
- **The deployment already runs on SQLite and works.** Nothing breaks on
  SQLite. Move to PostgreSQL when it suits you, not because a newer
  installer came out.

To get SQLite on a new install, pass `--db-driver sqlite` (or set
`GRAIN_DB_DRIVER=sqlite`).

## What `setup.sh` chooses

| The host | Store |
|---|---|
| `--db-driver sqlite` | SQLite. This is refused on a host whose store is already the local PostgreSQL server. Nothing moves a store back to SQLite. |
| `GRAIN_DB_DSN` / `--db-dsn` naming a server (`postgres://…`, or a `host=… dbname=…` DSN) | That server. No local server is provisioned. |
| A host that `setup.sh` provisioned before | The same server, with the same image, port and password. |
| An existing install: a `grain-daemon.service`, `store/grain.db`, or a `db-config.json` | Unchanged. A SQLite store stays SQLite, and the installer prints a one-line hint about moving it. |
| `--db-driver postgres` on a SQLite install | Provisions the local server and migrates the store into it (see [Moving a store](#moving-a-store)). |
| A fresh install (none of the above) | Provisions the local server. |

## The local server

`grain-postgres.service` sits next to `grain-daemon.service` and has the same
unit style: a `docker run` under systemd.

- **Image:** `GRAIN_POSTGRES_IMAGE`. The default is
  `mirror.gcr.io/library/postgres:16-bookworm`, Docker Hub's official image
  served through Google's public mirror, pinned by digest
  (`GRAIN_POSTGRES_DEFAULT_IMAGE` in `setup.sh`), so the tag moving
  upstream never changes what a new install runs. The mirror answers on networks
  that can't reach Docker Hub. If neither is reachable, set
  `GRAIN_POSTGRES_IMAGE` to a copy in a registry you can reach.
- **Data:** `$GRAIN_DATA_DIR/postgres`, mode 0700. The container runs as
  the deployment's own account (`--user`), so the cluster belongs to
  `grain` like everything else in the data directory.
- **Network:** host networking, with `listen_addresses=127.0.0.1` and
  nothing else, on `GRAIN_POSTGRES_PORT` (default 5432). Nothing on
  another interface can reach it.
- **Password:** generated once into
  `$GRAIN_DATA_DIR/secrets/postgres-password` and never replaced. `initdb`
  runs with `--auth=scram-sha-256`, so loopback and local-socket connections
  require this password too (the official image's default `pg_hba.conf`
  trusts `127.0.0.1`, `::1` and the local socket). Re-running `setup.sh`
  rewrites any `trust` lines in an existing cluster's `pg_hba.conf` to
  `scram-sha-256` and reloads the server.
- **DSN:** `/etc/grain/postgres.env`, owned by root, mode 0600.
  `grain-daemon.service` reads it as an `EnvironmentFile` and passes
  `GRAIN_DB_DRIVER` and `GRAIN_DB_DSN` into its container by name. The
  password is never in the unit or on a command line. The host's `grain`
  CLI reads the same file when root runs it. For anyone else, the CLI
  finds the store through `db-config.json`, which the daemon records on
  start, with the password kept in grain's encrypted secrets.
- **Ordering:** `grain-daemon.service` has `Requires=` and `After=` on
  `grain-postgres.service`. The server's `ExecStartPost` waits until
  `pg_isready` answers, so the daemon starts only once the database takes
  connections.

Restarting the daemon, upgrading it from the UI, or re-running `setup.sh`
doesn't restart Postgres. A re-run restarts the server only when its own
unit changed, for example a new `GRAIN_POSTGRES_IMAGE` or
`GRAIN_POSTGRES_PORT`. Restarting `grain-postgres.service` restarts the
daemon with it, because of the `Requires=` dependency.

`grain backup`, `grain health`, `grain state …` and the rest of the CLI work
the same on either store. `setup.sh`'s schema-upgrade step (`grain state
prepare-upgrade`) runs against the local server when that is the store.

## Upgrading the PostgreSQL image

Upgrading the server is a separate, explicit step. A re-run of `setup.sh`
keeps the image that the installed unit records, even when a newer
`setup.sh` has a newer default. To change the image:

```bash
sudo GRAIN_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16.6-bookworm ./scripts/setup.sh
```

A minor version within the same major works this way. Name the new image
by digest (`…:16.6-bookworm@sha256:…`) to pin it the way the default is. The server restarts
onto the new image, and the daemon restarts with it.

`setup.sh` refuses a new **major** version: PostgreSQL can't open a data
directory made by another major. To move majors, dump the data and
restore it into a fresh cluster:

```bash
# 1. Dump, with the old server still running.
sudo systemctl stop grain-daemon.service
sudo docker exec grain-postgres sh -c 'PGPASSWORD=$(cat /run/grain/postgres-password) exec pg_dump -U grain -d grain -Fc' > /root/grain.dump

# 2. Set the old cluster aside.
sudo systemctl stop grain-postgres.service
sudo mv /var/lib/grain/data/postgres /var/lib/grain/data/postgres.pg16

# 3. Start the new major on an empty cluster, with the daemon still
#    stopped. Starting grain-postgres.service by itself never starts the
#    daemon. The server initializes the cluster with the same password.
sudo install -d -m0700 -o grain -g grain /var/lib/grain/data/postgres
old="$(sed -n 's/^Environment=GRAIN_POSTGRES_IMAGE=//p' /etc/systemd/system/grain-postgres.service)"
sudo sed -i "s|$old|mirror.gcr.io/library/postgres:17-bookworm|g" /etc/systemd/system/grain-postgres.service
sudo systemctl daemon-reload && sudo systemctl start grain-postgres.service

# 4. Restore into it.
sudo docker exec -i grain-postgres sh -c 'PGPASSWORD=$(cat /run/grain/postgres-password) exec pg_restore -U grain -d grain --no-owner' < /root/grain.dump

# 5. Record the image the way setup.sh does. The unit already matches, so
#    the server is not restarted. Then start the daemon.
sudo GRAIN_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:17-bookworm ./scripts/setup.sh
```

Keep `postgres.pg16` until the deployment has run on the new server for a
while. Then delete it.

## Backups

Back up `$GRAIN_DATA_DIR` (`/var/lib/grain/data`) as before. With the local
server, the cluster in `$GRAIN_DATA_DIR/postgres` is part of it. A live
server's files copied while it is writing are not a consistent copy, so
use one of these:

- **`grain backup` and `-backup-interval` (Google Drive or GCS).** Snapshots a
  PostgreSQL store online with `pg_dump -Fc` into `database/grain.dump`
  alongside a restorable `config/config-repo.bundle`, and uploads the ZIP to
  Google Drive or GCS (see [host-setup.md](host-setup.md#backups)).
  Run it by hand with `grain-daemon.service` stopped (`sudo grain backup
  -data-dir /var/lib/grain/data …`), or schedule `-backup-interval` on the
  running daemon. The archive excludes `$GRAIN_DATA_DIR/secrets` and database
  passwords, so back up `secrets/secrets.key` and `secrets/postgres-password`
  separately. The `grain` container image carries `postgresql-client-16`; a
  host-only binary needs a `pg_dump` on `PATH` (or under
  `/usr/lib/postgresql/*/bin`) whose major version is at least the server's.
- **`pg_dump` by hand, online.** Writes a standalone custom-format dump to
  local disk. Restore it with `pg_restore`.

  ```bash
  sudo docker exec grain-postgres sh -c 'PGPASSWORD=$(cat /run/grain/postgres-password) exec pg_dump -U grain -d grain -Fc' > grain-$(date -u +%Y%m%dT%H%M%SZ).dump
  ```

  The dump has the store only. Also back up the rest of `$GRAIN_DATA_DIR`,
  above all `secrets/secrets.key` and `secrets/postgres-password`.
- **A cold copy, with the container stopped.**

  ```bash
  sudo systemctl stop grain-daemon.service grain-postgres.service
  sudo tar -C /var/lib/grain -czf grain-data.tar.gz data
  sudo systemctl start grain-postgres.service grain-daemon.service
  ```

  A disk snapshot of the data disk taken while both are stopped works the
  same way.

Take one of these before an upgrade if you may want to go back. A newer
build migrates the store in place and keeps no copy, and an older build
refuses a store at a newer schema. See [Downgrading to an earlier
build](host-setup.md#downgrading-to-an-earlier-build).

To restore a cold copy, stop the daemon, put the data directory back in
place and re-run `setup.sh`. The installer finds the password and the cluster and keeps
using them. To restore a dump (either a standalone `.dump` or
`database/grain.dump` unpacked from a `grain backup` archive), run
`pg_restore` into the running server (`--clean --if-exists --no-owner`) with
`grain-daemon.service` stopped.

## Moving a store

A store is moved only **offline**: stop the daemon, move the store, check
it, start the daemon. There is no online path. The UI's Database pane only
shows which store is in use and these commands. No API route copies,
migrates or switches a running daemon's store.

The reason is what happens when a store is copied under a running daemon.
The daemon keeps writing to the store it opened, and the copy stops at the
moment it was taken. Rewriting `db-config.json` doesn't move a running
daemon either. It only changes which store the *next* start opens. Either
way the next start opens a store that is missing everything written since
the copy, and it hands those task ids out again, though branches, pull
requests and messages already name them.

### What enforces it

- **The daemon's lock.** A running daemon holds an exclusive lock on
  `$GRAIN_DATA_DIR/daemon.lock`. On PostgreSQL it also holds a session
  advisory lock on the store itself, which a tool on another host can see.
  `grain upgrade-postgres` and `grain backup` refuse while either is held,
  and the error gives the command to run first:
  `sudo systemctl stop grain-daemon.service`. They never stop the daemon
  themselves. `setup.sh --db-driver postgres` does stop it, because that
  is the offline procedure. A second daemon on the same data directory or
  store waits 90 seconds for the first to exit, then refuses to start.
- **The high-water mark.** The daemon keeps
  `$GRAIN_DATA_DIR/store-highwater.json` up to date as it writes. The file
  holds the last task id it handed out, the last run it started and when
  the mark last moved. The mark only moves forward. A daemon refuses to
  start on a store behind it. The refusal names the store it opened, how
  far behind it is (tasks and runs), and the other stores on the host
  (the SQLite `store`, every `postgres*` cluster). It also says how to
  recover.
- **The switch alert.** A running daemon checks every ten seconds which
  store its data directory selects (`db-config.json`, `GRAIN_DB_DRIVER`,
  `GRAIN_DB_DSN`). If that stops being the store it opened, the daemon
  logs an error and raises a critical alert. The alert shows in the UI's
  alerts and in `grain health`. The daemon then dispatches nothing more
  until it is restarted.
- **`grain db check`** only reads, and is safe beside a running daemon.
  It prints the store a start would open, what that store holds, the
  mark, and any other store. It exits 1 when the store is behind the
  mark. A `setup.sh` re-run runs it before restarting the daemon on a
  host with more than one store. If the store is behind, the re-run stops
  and leaves the running daemon alone.

### SQLite to the local PostgreSQL server

```bash
sudo ./scripts/setup.sh --db-driver postgres
```

On a host with a SQLite store, this does the following:

1. Stops `grain-daemon.service`.
2. Provisions `grain-postgres.service`.
3. Runs `grain upgrade-postgres -data-dir … -dsn …` into the new server.
   This copies every table and writes `db-config.json`.
4. Checks the new store against the high-water mark (`grain db check`).
5. Starts the daemon on PostgreSQL.

The SQLite store in `$GRAIN_DATA_DIR/store` is left where it is. If the
migration fails, re-running the same command tries it again.
`$GRAIN_DATA_DIR/.postgres-migrated` marks a migration as finished.

### By hand, into any server

```bash
# 1. Stop the daemon. Everything below refuses while it runs.
sudo systemctl stop grain-daemon.service

# 2. Migrate. This writes db-config.json to select the new store.
sudo grain upgrade-postgres -data-dir /var/lib/grain/data -dsn "postgres://grain:…@db.internal:5432/grain"

# 3. Verify: the new store must be at or past the high-water mark.
sudo grain db check -data-dir /var/lib/grain/data

# 4. Start it, or re-run setup.sh with --db-dsn set to that server.
sudo systemctl start grain-daemon.service
```

To move a PostgreSQL store to another server, the steps are the same, with
`pg_dump` and `pg_restore` in step 2 (see [Upgrading the PostgreSQL
image](#upgrading-the-postgresql-image)), and with the daemon pointed at
the new server before step 3.

### The Google edition

The Google edition's installer supplies its own commands for moving the
store, for example moving PostgreSQL into or out of its container. They
follow the same offline procedure, and the same locks and checks refuse
them while a daemon is attached:

1. `sudo systemctl stop grain-daemon.service`
2. Run the edition's move or migration with the daemon stopped.
3. `sudo grain db check -data-dir /var/lib/grain/data`
4. `sudo systemctl start grain-daemon.service`

### When a start refuses a store behind the mark

Don't delete the mark to get past the refusal. Find the newer store
first. The refusal and `grain db check` list the other stores on the
host. Then do one of these:

- Point the daemon back at the newer store (`db-config.json`, or
  `GRAIN_DB_DRIVER`/`GRAIN_DB_DSN`).
- With the daemon stopped, copy the newer store into the one it opens, as
  above.

If the newer store is gone for good, start once with
`GRAIN_ACCEPT_STORE_BEHIND=1`. The daemon logs what was lost and seeds the
task id sequence past the mark, so ids continue from the last one ever
handed out and none is reused.

## An external server

Pass the DSN and no local server is provisioned:

```bash
sudo ./scripts/setup.sh --db-dsn "postgres://grain:secret@db.internal:5432/grain?sslmode=require"
```

[cloudsql-postgres.md](cloudsql-postgres.md) covers Cloud SQL and the Cloud
SQL Auth Proxy.

## Migrating to GKE

`scripts/migrate-local-to-gke.sh` refuses a host whose store is the local
server. The controller in the cluster can't reach this host's 127.0.0.1,
and the server's files aren't a consistent copy while it runs. First move
the store to a server the cluster can reach, as described in
[Moving a store](#moving-a-store). Then run the script again. The refusal
prints the commands to run.
[grain-init-platforms.md](grain-init-platforms.md#migrating-a-local-install-to-gke)
covers the rest of the migration.
