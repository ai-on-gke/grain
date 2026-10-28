# Bootstrap: PostgreSQL & Cloud SQL Auth Proxy Setup

Goal: configure and wire Google Cloud SQL PostgreSQL backend and Cloud SQL Auth Proxy sidecar for Grain daemon deployment.

Read this whole playbook before running anything. Every shell command below is something you (the agent) run through the `run_host_command` tool, one at a time -- each one is posted into this chat and waits for a human "approve" before it actually runs.

## 1. Prerequisites & Information Needed

Confirm with whoever you are talking to:
- GCP Project ID (`GCP_PROJECT`)
- Cloud SQL Instance connection name (`PROJECT:REGION:INSTANCE`)
- Database name (`graindb`) and user account credentials
- Connection method: TCP Localhost (`127.0.0.1:5432`) or Unix socket (`/cloudsql/INSTANCE`)

## 2. Setting up Cloud SQL Auth Proxy

### Option A: TCP Localhost Mode (127.0.0.1:5432)
```bash
# Download cloud-sql-proxy binary
curl -o /usr/local/bin/cloud-sql-proxy https://storage.googleapis.com/cloud-sql-connectors/cloud-sql-proxy/v2.8.0/cloud-sql-proxy.linux.amd64
chmod +x /usr/local/bin/cloud-sql-proxy

# Configure systemd service
cat <<EOF > /etc/systemd/system/cloud-sql-proxy.service
[Unit]
Description=Cloud SQL Auth Proxy
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloud-sql-proxy --port 5432 ${INSTANCE_CONNECTION_NAME}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cloud-sql-proxy
```

### Option B: Unix Domain Socket Mode (/cloudsql/INSTANCE)
```bash
mkdir -p /cloudsql
chmod 777 /cloudsql

cat <<EOF > /etc/systemd/system/cloud-sql-proxy.service
[Unit]
Description=Cloud SQL Auth Proxy
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloud-sql-proxy --unix-socket /cloudsql ${INSTANCE_CONNECTION_NAME}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cloud-sql-proxy
```

## 3. Provisioning Grain with PostgreSQL Backend

Run `scripts/setup.sh` with the `--db-driver` and `--db-dsn` parameters:

### For TCP Localhost:
```bash
sudo ./scripts/setup.sh \
  --db-driver postgres \
  --db-dsn "postgres://${DB_USER}:${DB_PASS}@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
```

### For Unix Domain Socket:
```bash
sudo ./scripts/setup.sh \
  --db-driver postgres \
  --db-dsn "postgres://${DB_USER}:${DB_PASS}@/${DB_NAME}?host=/cloudsql/${INSTANCE_CONNECTION_NAME}"
```

## 4. Verifying Connectivity & Readiness

Check `grain-daemon.service` journal output to confirm successful database connection:
```bash
journalctl -u grain-daemon.service -n 50 --no-pager
```

Look for:
`grain: imported this deployment's rows` or successful start with no database error.
