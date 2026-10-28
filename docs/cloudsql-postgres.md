# PostgreSQL & Cloud SQL Auth Proxy Guide

Grain supports PostgreSQL as an alternative to the embedded SQLite task store. This enables managed databases such as Google Cloud SQL for high availability, automated backups, Point-in-Time Recovery (PITR), and centralized management across multi-host or ephemeral instances.

---

## 1. Overview & Architecture

### Backend Selection
By default, Grain uses SQLite storing database files under `<data-dir>/store`. To switch to PostgreSQL:
- Flag: `-db-driver=postgres` (or env var `GRAIN_DB_DRIVER=postgres`)
- DSN: `-db-dsn="<connection-string>"` (or env var `GRAIN_DB_DSN="<connection-string>"`)

### Cloud SQL Auth Proxy Connectivity
Grain supports connecting to Google Cloud SQL via the [Cloud SQL Auth Proxy](https://cloud.google.com/sql/docs/postgres/connect-auth-proxy) running on the same host or sidecar container:

1. **TCP Localhost (`127.0.0.1:5432`)**:
   - Proxy command:
     ```bash
     cloud-sql-proxy PROJECT_ID:REGION:INSTANCE --port 5432
     ```
   - Connection URL:
     ```
     postgres://grain_user:secret_password@127.0.0.1:5432/graindb?sslmode=disable
     ```
   - *Note*: Because `cloud-sql-proxy` encrypts traffic to Cloud SQL over mTLS, `sslmode=disable` is standard between Grain and the local proxy daemon.

2. **Unix Domain Socket (`/cloudsql/INSTANCE`)**:
   - Proxy command:
     ```bash
     cloud-sql-proxy --unix-socket /cloudsql PROJECT_ID:REGION:INSTANCE
     ```
   - Connection URL:
     ```
     postgres://grain_user:secret_password@/graindb?host=/cloudsql/PROJECT_ID:REGION:INSTANCE
     ```
   - *Container Mode Mount*: In container mode, `scripts/setup.sh` automatically binds `/cloudsql:/cloudsql` when `/cloudsql` exists on the host.

---

## 2. Startup Resilience & Readiness Retries

During system boot or sidecar initialization, the Cloud SQL Auth Proxy may take a few seconds to complete IAM authentication and start accepting connections.

Grain's `openStore` implementation automatically performs startup connection readiness retries with exponential backoff (retrying up to 15 attempts, 30 seconds total) with health checks (`db.Ping()`) before giving up, ensuring seamless cold-start orchestration without startup race crashes.

---

## 3. Host Provisioning via `scripts/setup.sh`

`scripts/setup.sh` supports database backend configuration via CLI flags or environment variables when generating systemd units (`grain-daemon.service`):

### Using CLI Flags
```bash
sudo ./scripts/setup.sh \
  --db-driver postgres \
  --db-dsn "postgres://grain_user:secret@127.0.0.1:5432/graindb?sslmode=disable"
```

### Using Environment Variables
```bash
export GRAIN_DB_DRIVER=postgres
export GRAIN_DB_DSN="postgres://grain_user:secret@/graindb?host=/cloudsql/PROJECT_ID:REGION:INSTANCE"
sudo -E ./scripts/setup.sh
```

---

## 4. Cloud SQL Instance & IAM Setup

### Step 1: Create Cloud SQL PostgreSQL Instance
```bash
gcloud sql instances create grain-db \
  --database-version=POSTGRES_15 \
  --cpu=2 \
  --memory=7680MB \
  --region=us-central1 \
  --project=MY_PROJECT_ID
```

### Step 2: Create Database & User
```bash
gcloud sql databases create graindb --instance=grain-db --project=MY_PROJECT_ID

gcloud sql users create grain_user \
  --instance=grain-db \
  --password="CHANGE_ME_SECURE_PASSWORD" \
  --project=MY_PROJECT_ID
```

### Step 3: Grant IAM Permissions for Cloud SQL Proxy
Ensure the host VM service account has the `Cloud SQL Client` role:
```bash
gcloud projects add-iam-policy-binding MY_PROJECT_ID \
  --member="serviceAccount:HOST_SA_EMAIL@MY_PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/cloudsql.client"
```

### Step 4: Run Cloud SQL Proxy Systemd Service
Create `/etc/systemd/system/cloud-sql-proxy.service`:
```ini
[Unit]
Description=Google Cloud SQL Auth Proxy
After=network.target

[Service]
Type=simple
User=cloudsqlproxy
ExecStart=/usr/local/bin/cloud-sql-proxy \
  --port 5432 \
  MY_PROJECT_ID:us-central1:grain-db
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```
Start the service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cloud-sql-proxy
```

---

## 5. Schema & Migrations

Grain manages PostgreSQL schemas identically to SQLite:
- `store.Init(ctx)` creates required tables (`tasks`, `runs`, `templates`, `schedules`, `config`, etc.) and indexes automatically upon first connect.
- Safe transactional sweeps (`sweepOrphanedSettingsRows`) clean up orphaned settings rows on daemon start.
