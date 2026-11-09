# Host Migration Playbook

Goal: safely migrate a Grain deployment from a source host to a new target host with zero state loss, preserving the SQLite/PostgreSQL database, the controller state repository (`state-repo`), credential ladders, and encrypted secret stores.

Read this whole playbook before running anything. Every shell command below is something you (the agent) run through the `run_host_command` tool, one at a time -- each one is posted into this chat and waits for human approval before running.

---

## 1. Prerequisites & Information Needed

Confirm the following details with the operator before proceeding:
- **Source host**: `$GRAIN_DATA_DIR` (defaults to `/var/lib/grain/data`), SSH access, and current status (`systemctl status grain`).
- **Target host**: Host IP/hostname, SSH access, disk space (`df -h /var/lib`), and root/sudo permissions.
- **Migration passphrase**: (Optional, recommended) Symmetric passphrase to encrypt the migration archive bundle during transit.

---

## 2. Source Host: Exporting Grain State & Secrets

On the source host, we use `grain export` to snapshot the database (performing a safe SQLite WAL checkpoint and VACUUM snapshot), bundle the controller state repository, and collect all credentials under `$GRAIN_DATA_DIR/secrets/` into a verified tarball bundle.

### Step 2.1: Stop Grain Daemon on Source Host (Recommended for Cutover)

To ensure no tasks are dispatched or modified during the final cutover:

```bash
sudo systemctl stop grain
```

*(Note: `grain export` can safely snapshot a running SQLite database using WAL checkpoints, but stopping the daemon guarantees an exact point-in-time cutover.)*

### Step 2.2: Run `grain export`

Run `grain export` to package the data directory:

```bash
# Export with symmetric passphrase encryption. The passphrase is read from
# stdin (or from a file with -passphrase-file FILE), never from the command
# line, where other users could read it through ps:
printf '%s' "${MIGRATION_PASSPHRASE}" | sudo grain export -data-dir /var/lib/grain/data -out /tmp/grain-migration.enc -passphrase-file -

# Or export unencrypted:
sudo grain export -data-dir /var/lib/grain/data -out /tmp/grain-migration.tar.gz
```

Verify that the export completed and generated the manifest:
```bash
ls -la /tmp/grain-migration.*
```

---

## 3. Transferring the Migration Bundle

Transfer the export archive bundle from the source host to the target host using `scp` or `rsync`:

```bash
# From source host to target host:
scp /tmp/grain-migration.enc debian@${TARGET_HOST_IP}:/tmp/grain-migration.enc

# Or using rsync with progress:
rsync -avzP /tmp/grain-migration.enc debian@${TARGET_HOST_IP}:/tmp/grain-migration.enc
```

---

## 4. Target Host: Preparing the Environment

On the target host, prepare the data directory and ensure proper ownership and permissions:

```bash
# Ensure grain user and group exist (if installed via scripts/setup.sh):
sudo id grain || sudo useradd -r -s /usr/sbin/nologin -d /var/lib/grain/data grain

# Create target data directory with 0700 permissions
sudo mkdir -p /var/lib/grain/data
sudo chown -R grain:grain /var/lib/grain/data
sudo chmod 0700 /var/lib/grain/data
```

Ensure the Grain daemon is stopped on the target host before importing:
```bash
sudo systemctl stop grain || true
```

---

## 5. Target Host: Importing the Migration Bundle

On the target host, run `grain import` to unpack the bundle, verify SHA-256 integrity checksums against `manifest.json`, restore database and secrets with strict `0600`/`0700` permissions, clone the state repository, and run any needed database migrations.

```bash
# If bundle was encrypted with passphrase (read from stdin, as on export):
printf '%s' "${MIGRATION_PASSPHRASE}" | sudo grain import -data-dir /var/lib/grain/data -in /tmp/grain-migration.enc -passphrase-file - -force

# If bundle was unencrypted:
sudo grain import -data-dir /var/lib/grain/data -in /tmp/grain-migration.tar.gz -force
```

Fix file ownership so the `grain` daemon user owns all restored files:
```bash
sudo chown -R grain:grain /var/lib/grain/data
sudo chmod 0700 /var/lib/grain/data
```

---

## 6. Target Host: Verification & Starting the Daemon

### Step 6.1: Verify State & Key Status

Run `grain state status` to verify the state repository and secrets key:

```bash
sudo -u grain grain state -data-dir /var/lib/grain/data status
```

Confirm that:
1. The state repository branch and status are clean and verified.
2. The secrets key is reported (`/var/lib/grain/data/secrets/secrets.key`).
3. GitHub credential ladder and GCP credentials under `/var/lib/grain/data/secrets/` are intact.

### Step 6.2: Start the Grain Service

Enable and start the Grain daemon service:

```bash
sudo systemctl enable --now grain
sudo systemctl status grain --no-pager
```

### Step 6.3: Health Check UI and API

Verify that the local UI / API responds:

```bash
curl -f http://127.0.0.1:8420/api/settings || curl -f http://127.0.0.1:8420/
```

### Step 6.4: Cleanup Temporary Migration Files

Once migration is verified, remove temporary migration bundles from both hosts:

```bash
# On target host:
rm -f /tmp/grain-migration.*

# On source host:
rm -f /tmp/grain-migration.*
```
