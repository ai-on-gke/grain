# Deploying Grain on GKE Behind IAP (`scripts/deploy-gke.sh`)

This guide documents how to deploy **Grain on GKE** (`docs/design/grain-on-gke.md`) using `scripts/deploy-gke.sh`, the Kustomize overlay under `deploy/kubernetes/gke/` (and `deploy/gke/manifests/`), and the Terraform module under `deploy/gke/terraform/`.

In this topology, `grain daemon`, its in-process services (package cache, git proxy, model proxy, Actions cache), the granule dial-out mTLS acceptor (`Service/grain-access:4244`), and `github-webhook` run on a GKE cluster behind an Identity-Aware Proxy (IAP) global external Application Load Balancer (`Gateway/grain-gateway`), while **`grain hostd` stays on the existing workstation** and dials out to GKE over a single IAP + token-authenticated WebSocket (`wss://<dns-name>/api/hostd/connect`).

---

## Files & Layout

| Path | Purpose |
|---|---|
| `scripts/deploy-gke.sh` | Re-runnable, idempotent entry point for cluster setup, dependency provisioning, manifest deployment, IAP setup, `hostd` connection, `--migrate`, `--cutover`, `--rollback`, and `--check`. |
| `deploy/gke/deploy-gke.env.example` | Example configuration file passed via `scripts/deploy-gke.sh --config <file>`. |
| `deploy/kubernetes/gke/` | Kustomize overlay over `grain-granule.yaml` and `github-webhook.yaml` (`namespace.yaml`, `sandbox-sa.yaml`, `services.yaml`, `gateway.yaml`, `networkpolicy.yaml`, `secrets-csi.yaml`, `deployment-patch.yaml`, `postgres-incluster.yaml`). |
| `deploy/gke/manifests/` | Alias Kustomize overlay pointing to `deploy/kubernetes/gke/`. |
| `deploy/gke/terraform/` | Terraform module for GKE cluster & nested-virt node pool, VPC/PSA, Cloud SQL for PostgreSQL 16, Workload Identity, Secret Manager containers, and Gateway IP/TLS/DNS/IAP. |

---

## Flags & Configuration File

Every option can be supplied either as a CLI flag or in a `--config <path>` environment file (see `deploy/gke/deploy-gke.env.example`); CLI flags take precedence over config file entries.

| Flag | Config Variable | Default | Description |
|---|---|---|---|
| `--config <path>` | `GRAIN_GKE_CONFIG` | — | Path to `key=value` configuration file. |
| `--project <id>` | `GRAIN_GKE_PROJECT` | *(required)* | GCP project ID hosting the GKE cluster, Cloud SQL, and Secret Manager. |
| `--region <region>` | `GRAIN_GKE_REGION` | `us-central1` | GCP region for the cluster and Cloud SQL instance. |
| `--zone <zone>` | `GRAIN_GKE_ZONE` | `<region>-a` | GCP zone for node pools. |
| `--cluster <name>` | `GRAIN_GKE_CLUSTER` | `grain-gke` | GKE cluster name to create or adopt. |
| `--adopt-cluster` | `GRAIN_GKE_ADOPT_CLUSTER` | `false` | Adopt an existing cluster named `--cluster` instead of creating one. |
| `--namespace <ns>` | `GRAIN_GKE_NAMESPACE` | `grain` | Kubernetes namespace (`pod-security.kubernetes.io/enforce=privileged`). |
| `--dns-name <fqdn>` | `GRAIN_GKE_DNS_NAME` | *(required)* | Fully qualified domain name for the IAP-protected Gateway. |
| `--dns-zone <zone>` | `GRAIN_GKE_DNS_ZONE` | `""` | Cloud DNS managed zone name to populate with the Gateway A record. |
| `--iap-principal <m>` / `--iap-principals <csv>` | `GRAIN_GKE_IAP_PRINCIPALS` | `""` | IAM principal(s) granted `roles/iap.httpsResourceAccessor` (e.g. `user:alice@example.com`). |
| `--iap-client-id <id>` | `GRAIN_GKE_IAP_CLIENT_ID` | `""` | Custom IAP OAuth 2.0 client ID (defaults to Google-managed IAP client). |
| `--iap-client-secret-file <path>` | `GRAIN_GKE_IAP_CLIENT_SECRET_FILE` | `""` | File holding custom IAP OAuth client secret (never passed on CLI or logged). |
| `--image <ref>` | `GRAIN_GKE_IMAGE` | `ghcr.io/bwsalmon/grain/grain:main` | Grain container image (`ghcr.io/bwsalmon/grain/...`), resolved and pinned by `@sha256:` digest. |
| `--sandbox-image <ref>` | `GRAIN_GKE_SANDBOX_IMAGE` | `ghcr.io/bwsalmon/grain/granule:latest` | Packed granule guest image (`ghcr.io/bwsalmon/grain/...`), resolved and pinned by `@sha256:` digest. |
| `--postgres <mode>` | `GRAIN_GKE_POSTGRES` | `cloudsql` | `cloudsql` (Cloud SQL for PostgreSQL 16 with private IP + IAM auth) or `in-cluster` (`StatefulSet/grain-postgres`). |
| `--cloudsql-instance <name>` | `GRAIN_GKE_CLOUDSQL_INSTANCE` | `grain-postgres` | Cloud SQL instance name when `--postgres=cloudsql`. |
| `--cloudsql-tier <tier>` | `GRAIN_GKE_CLOUDSQL_TIER` | `db-custom-2-8192` | Cloud SQL machine tier. |
| `--hostd-name <name>` | `GRAIN_GKE_HOSTD_NAME` | `$(hostname -s)` | Registration name of this workstation's `grain hostd`. |
| `--hostd-owner <user>` | `GRAIN_GKE_HOSTD_OWNER` | `$USER` | Owner principal for this workstation's `grain hostd`. |
| `--data-dir <dir>` | `GRAIN_GKE_DATA_DIR` | `/var/lib/grain/data` | Local workstation `grain` data directory for `--migrate`. |
| `--staging-bucket <name>` | `GRAIN_GKE_STAGING_BUCKET` | `<project>-grain-staging` | GCS bucket for PVC migration staging and scheduled backups. |
| `--access-mode <mode>` | `GRAIN_GKE_ACCESS_MODE` | `dialout` | Granule sandbox access channel mode (`dialout` or `kubectl`). |
| `--use-terraform` | `GRAIN_GKE_USE_TERRAFORM` | `false` | Provision GCP infrastructure through `deploy/gke/terraform` before applying manifests. |
| `--dry-run` | — | `false` | Print every command and action without modifying any GCP, Kubernetes, or host state. |

---

## Step-by-Step Workflow

### 1. Preview the Plan (`--dry-run`)

Always preview the planned actions first:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --region=us-central1 \
  --cluster=grain-gke \
  --dns-name=grain.example.com \
  --iap-principal=user:operator@example.com \
  --dry-run
```

### 2. Provision Cluster, Dependencies, Manifests, IAP, and Connect `hostd`

Run without `--dry-run` to execute stages 1–5:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --region=us-central1 \
  --cluster=grain-gke \
  --dns-name=grain.example.com \
  --iap-principal=user:operator@example.com
```

This stage:
1. Creates (or adopts with `--adopt-cluster`) the GKE cluster with Dataplane V2, Workload Identity, Gateway API (`CHANNEL_STANDARD`), Secret Manager CSI driver, the `daemon` node pool, and the nested-virtualization `grain-sandbox-pool` node pool (`--enable-nested-virtualization --workload-metadata=GKE_METADATA`), plus `Namespace/grain` (`pod-security.kubernetes.io/enforce=privileged`), `PriorityClass` objects (`grain-daemon`, `grain-interactive`, `grain-agent`, `grain-ci`), and `ResourceQuota/grain-quota`. An adopted cluster (with `--adopt-cluster`, or one that already exists under `--cluster`) must already enforce NetworkPolicy -- Dataplane V2, or network policy enforcement on the legacy datapath -- or the script refuses it before changing anything: on a cluster that does not, the `NetworkPolicy` objects below are accepted and ignored, leaving the acceptor port open to every pod and `network: none` sandboxes on the internet. The Terraform module's `adopt_existing_cluster` checks the same with a postcondition.
2. Provisions Cloud SQL for PostgreSQL 16 (private IP + IAM authentication) or in-cluster Postgres (`--postgres=in-cluster`), creates Secret Manager secret containers, and binds `grain/grain` to `grain-daemon@<project>.iam.gserviceaccount.com` with least-privilege IAM (`roles/cloudsql.client`, `roles/cloudsql.instanceUser`, per-secret `roles/secretmanager.secretAccessor` plus `roles/secretmanager.secretVersionAdder` on `grain-sandbox-ca-key`, and bucket-scoped `roles/storage.objectAdmin`). Sandbox pods run as `ServiceAccount/grain-sandbox` with `automountServiceAccountToken: false` and **no** Workload Identity binding.
3. Resolves and pins the `ghcr.io/bwsalmon/grain/...` daemon and granule guest images by `@sha256:` digest and applies the `deploy/kubernetes/gke/` Kustomize overlay (`Deployment/grain`, `Deployment/grain-webhook`, `Service/grain`, `Service/grain-hostd`, `Service/grain-sandbox`, `Service/grain-access:4244`, `SecretProviderClass/grain-secrets`, and `NetworkPolicy` objects `grain-sandbox-network-none`, `grain-sandbox-egress`, `grain-access-ingress`, `grain-webhook-isolation`). If `grain-daemon.service` is still active on the workstation, `Deployment/grain` is staged at `replicas: 0` until `--cutover`.
4. Reserves `grain-gateway-ip`, creates the managed certificate `grain-gateway-cert`, configures the Gateway (`Gateway/grain-gateway` + `HTTPRoute/grain-routes` + `GCPBackendPolicy` with `timeoutSec: 86400` on `Service/grain-hostd`), populates `GRAIN_IAP_AUDIENCE` on `Deployment/grain`, and grants `roles/iap.httpsResourceAccessor` on the `grain` and `grain-hostd` backend services (never project-wide) to every `--iap-principal`.
5. Leaves `/usr/local/bin/grain hostd`, `/etc/grain/hostd.yaml` and its allowlist intact on the workstation, writes `/etc/grain/hostd.token` (`0600`, owned by `--hostd-owner`), syncs the token to Secret Manager (`grain-hostd-token`) and Kubernetes (`Secret/grain-hostd-token`), writes `/etc/grain/hostd-remote.env` and `/etc/systemd/system/grain-hostd.service.d/remote.conf` pointing at `https://<dns-name>`, and restarts `grain-hostd.service` only.

### 3. Migrate State (`--migrate`)

Move the workstation deployment's database, encryption keys, and recent transcripts/artifacts to GKE:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --dns-name=grain.example.com \
  --migrate
```

`--migrate` performs three idempotent steps:
1. Upgrades the workstation's SQLite store into PostgreSQL via `POST /api/database/upgrade`.
2. Uploads `secrets.key` and bootstrap credentials into Secret Manager via `gcloud secrets versions add --data-file=-` (never logging secret material).
3. Archives the last 30 days of `transcripts/` and `artifacts/` from `--data-dir` (excluding `repos/`, `home/`, and `cache/jobs/`), uploads the archive to `gs://<staging-bucket>/migration/`, and runs `Job/grain-data-seed` in the cluster to populate the `grain-data` PVC while `Deployment/grain` is scaled to 0.

### 4. Cut Over (`--cutover`)

Switch active execution from the workstation daemon to the GKE cluster:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --dns-name=grain.example.com \
  --cutover
```

`--cutover`:
1. **Refuses to run** if both the workstation's `grain-daemon.service` and GKE's `Deployment/grain` (`replicas > 0`) are simultaneously active against the same state.
2. Pauses dispatch on the workstation daemon (`POST /api/dispatch/pause`), drains in-flight work, and triggers a final backup (`POST /api/backups/run`).
3. Stops and disables `grain-daemon.service` on the workstation while keeping `grain-hostd.service` running.
4. Scales `Deployment/grain` in GKE to `replicas=1` and waits for rollout readiness.

### 5. Verify (`--check`)

Confirm the GKE deployment end-to-end:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --dns-name=grain.example.com \
  --check
```

`--check` verifies:
1. The UI answers through IAP at `https://<dns-name>/api/auth/access-code`: an unauthenticated request gets 302, 401 or 403, never 200.
2. The workstation's `hostd` is connected: inside `deploy/grain`, `GET http://127.0.0.1:8080/api/sandboxes` reports `hostExecutor.hostd.remote.connected: true`.
3. A throwaway sandbox runs a command over the granule dial-out channel (`Service/grain-access:4244`). Inside `deploy/grain` the check runs `grain sandbox probe --id=gke-check-<ts> --access=dialout -- echo dialout-ok:gke-check-<ts>`. The daemon builds sandbox `probe-gke-check-<ts>` (pod `grain-granule-probe-gke-check-<ts>`) with the deployment's default shape, runs the command over the dial-out channel, prints its output and deletes the pod. The probe is refused if the daemon is configured for `-k8s-granule-access=kubectl`.
4. The GKE API server Cloud Audit Log records **zero** `pods/exec`, `pods/attach`, or `pods/portforward` calls against that pod. An empty answer only counts after the log shows at least one entry for the pod, such as its create or delete. The check waits up to `GRAIN_GKE_AUDIT_WAIT_SECONDS` (default 300) for that entry, so ingestion lag cannot make it pass by default.

Checks 2 and 3 run over the pod's loopback. The daemon does not trust loopback on its own, so both present its local API token (`/data/local-api-token`), and the token never leaves the pod. `grain sandbox probe` needs the `deployment:restart` API scope when an agent calls it. With `--dry-run`, `--check` prints each command it would run and ends with "none was run".

### 6. Roll Back (`--rollback`)

If you need to return the daemon to the workstation, run `--rollback`:

```bash
./scripts/deploy-gke.sh \
  --project=my-gcp-project \
  --dns-name=grain.example.com \
  --rollback
```

`--rollback`:
1. Scales `Deployment/grain` in GKE to `replicas=0` and waits for pod deletion so the PostgreSQL advisory lock fence is released.
2. Removes `/etc/grain/hostd-remote.env` and `/etc/systemd/system/grain-hostd.service.d/remote.conf`, reloads systemd, and restarts `grain-hostd.service` on its local unix socket.
3. Re-enables and starts `grain-daemon.service` on the workstation against the same Cloud SQL database with no data copy.
