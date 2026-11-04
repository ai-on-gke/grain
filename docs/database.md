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
- **You rely on `grain backup`.** It snapshots SQLite with `VACUUM INTO`
  and refuses a PostgreSQL store. On PostgreSQL, use `pg_dump` (see
  [Backups](#backups)).

To get SQLite on a new install, pass `--db-driver sqlite` (or set
`GRAIN_DB_DRIVER=sqlite`).

## What `setup.sh` chooses

| The host | Store |
|---|---|
| `--db-driver sqlite` | SQLite. This is refused on a host whose store is already the local PostgreSQL server. Nothing moves a store back to SQLite. |
| `GRAIN_DB_DSN` / `--db-dsn` naming a server (`postgres://…`, or a `host=… dbname=…` DSN) | That server. No local server is provisioned. |
| A host that `setup.sh` provisioned before | The same server, with the same image, port and password. |
| An existing install: a `grain-daemon.service`, `store/grain.db`, or a `db-config.json` | Unchanged. A SQLite store stays SQLite, and the installer prints a one-line hint about moving it. |
| `--db-driver postgres` on a SQLite install | Provisions the local server and migrates the store into it (see [Moving a SQLite install](#moving-a-sqlite-install-to-postgresql)). |
| A fresh install (none of the above) | Provisions the local server. |

## The local server

`grain-postgres.service` sits next to `grain-daemon.service` and has the same
unit style: a `docker run` under systemd.

- **Image:** `GRAIN_POSTGRES_IMAGE`. The default is
  `mirror.gcr.io/library/postgres:16-bookworm`, Docker Hub's official image
  served through Google's public mirror. The mirror answers on networks
  that can't reach Docker Hub. If neither is reachable, set
  `GRAIN_POSTGRES_IMAGE` to a copy in a registry you can reach.
- **Data:** `$GRAIN_DATA_DIR/postgres`, mode 0700. The container runs as
  the deployment's own account (`--user`), so the cluster belongs to
  `grain` like everything else in the data directory.
- **Network:** host networking, with `listen_addresses=127.0.0.1` and
  nothing else, on `GRAIN_POSTGRES_PORT` (default 5432). Nothing on
  another interface can reach it.
- **Password:** generated once into
  `$GRAIN_DATA_DIR/secrets/postgres-password` and never replaced.
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

`grain health`, `grain state …` and the rest of the CLI work the same on
either store. `setup.sh`'s schema-upgrade step (`grain state
prepare-upgrade`) runs against the local server when that is the store.

## Upgrading the PostgreSQL image

Upgrading the server is a separate, explicit step. A re-run of `setup.sh`
keeps the image that the installed unit records, even when a newer
`setup.sh` has a newer default. To change the image:

```bash
sudo GRAIN_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16.6-bookworm ./scripts/setup.sh
```

A minor version within the same major works this way. The server restarts
onto the new image, and the daemon restarts with it.

`setup.sh` refuses a new **major** version: PostgreSQL can't open a data
directory made by another major. To move majors, dump the data and
restore it into a fresh cluster:

```bash
# 1. Dump, with the old server still running.
sudo systemctl stop grain-daemon.service
sudo docker exec grain-postgres pg_dump -U grain -d grain -Fc > /root/grain.dump

# 2. Set the old cluster aside.
sudo systemctl stop grain-postgres.service
sudo mv /var/lib/grain/data/postgres /var/lib/grain/data/postgres.pg16

# 3. Start the new major on an empty cluster, with the daemon still
#    stopped. Starting grain-postgres.service by itself never starts the
#    daemon. The server initializes the cluster with the same password.
sudo install -d -m0700 -o grain -g grain /var/lib/grain/data/postgres
sudo sed -i 's|postgres:16-bookworm|postgres:17-bookworm|g' /etc/systemd/system/grain-postgres.service
sudo systemctl daemon-reload && sudo systemctl start grain-postgres.service

# 4. Restore into it.
sudo docker exec -i grain-postgres pg_restore -U grain -d grain --no-owner < /root/grain.dump

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

- **`pg_dump`, online.** This is the simplest consistent copy. Restore it
  with `pg_restore`.

  ```bash
  sudo docker exec grain-postgres pg_dump -U grain -d grain -Fc > grain-$(date -u +%Y%m%dT%H%M%SZ).dump
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

To restore a cold copy, put the data directory back in place and re-run
`setup.sh`. The installer finds the password and the cluster and keeps
using them. To restore a dump, run `pg_restore` into the running server
(`--clean --if-exists`) with the daemon stopped.

`grain backup` (the README's [Backups](../README.md#backups)) supports
SQLite only.

## Moving a SQLite install to PostgreSQL

```bash
sudo ./scripts/setup.sh --db-driver postgres
```

On a host with a SQLite store, this does the following:

1. Stops `grain-daemon.service`.
2. Provisions `grain-postgres.service`.
3. Runs `grain upgrade-postgres -data-dir … -dsn …` into the new server.
   This copies every table and writes `db-config.json`.
4. Starts the daemon on PostgreSQL.

The SQLite store in `$GRAIN_DATA_DIR/store` is left where it is. If the
migration fails, re-running the same command tries it again.
`$GRAIN_DATA_DIR/.postgres-migrated` marks a migration as finished.

To migrate into a server of your own instead, run `grain upgrade-postgres
-dsn …` yourself with the daemon stopped, then re-run `setup.sh` with
`--db-dsn` set to that server.

## An external server

Pass the DSN and no local server is provisioned:

```bash
sudo ./scripts/setup.sh --db-dsn "postgres://grain:secret@db.internal:5432/grain?sslmode=require"
```

[cloudsql-postgres.md](cloudsql-postgres.md) covers Cloud SQL and the Cloud
SQL Auth Proxy.
