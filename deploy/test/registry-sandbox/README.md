# Grain Registry — GCP Sandbox Test Deployment

Disposable, reproducible test/demo environment for `grain-registry` in a GCP
sandbox project (`deploy/test/registry-sandbox/`).

> **Scope note:** This directory provisions a lightweight single-project
> test/demo deployment for connecting external `grain` daemons (`connected`
> entries over `-registry-connect` outbound WebSocket mux tunnels) and testing
> catalog discovery + subdomain routing. It intentionally keeps all resources
> minimal and self-contained so it does not pre-empt the production
> multi-replica deployment layout.

## Architecture

- **Compute & Database (`instance.tf`, `files/startup.sh`)**: One private-IP
  `e2-small` Debian 12 GCE VM (no external IP, outbound egress via Cloud NAT to
  satisfy `constraints/compute.vmExternalIpAccess`) running:
  - `postgres:16-alpine` bound strictly to `127.0.0.1:5432` with persistent data
    on the VM boot disk (`/var/lib/grain-registry/pgdata`);
  - `grain-registry` built from `Dockerfile.registry` (`make image-registry`),
    configured with `compute.provider: "none"` and `config_repos.provider: "none"`,
    listening on `:8080` (IAP-protected control plane + `<entry>.<instance_domain>`
    tunnel router) and `:8081` (locked-down `PublicHandler`).
- **Signing Key (`kms.tf`)**: One Cloud KMS asymmetric signing key
  (`EC_SIGN_P256_SHA256`, `ASYMMETRIC_SIGN`) consumed by
  `pkg/registry/providers/gcp` to sign `X-Grain-Registry-Assertion` JWTs
  (`iss = "grain-registry"`) and publish `/.well-known/jwks.json`.
- **Routing, DNS & Admission (`lb.tf`)**: One `EXTERNAL_MANAGED` Global External
  Application Load Balancer on a reserved static IPv4 (`<a.b.c.d>`) using zero-DNS
  `sslip.io` hostnames:
  - Control plane: `https://<prefix>-<a-b-c-d>.sslip.io`
  - Routed entries: `https://<entry>.i-<prefix>-<a-b-c-d>.sslip.io`
  - **Backend 1 (`iap-bs` -> `:8080`, `timeout_sec = 86400`)**: Cloud IAP
    enabled (Google-managed OAuth client) with `roles/iap.httpsResourceAccessor`
    granted strictly to `--admin <owner@example.com>` (plus any `--extra-user`
    principals and the read-only verifier service account). Serves the registry
    UI/API (`/api/directory`, `/api/route`, `/api/entries`) and all
    `*.<instance_domain>` routed traffic.
  - **Backend 2 (`pub-bs` -> `:8081`, `timeout_sec = 86400`)**: IAP disabled;
    URL map routes strictly `/.well-known/jwks.json`, `/healthz`, and
    `X-Grain-Auth: token` (`grc_...` credential) calls on
    `/api/entries/<name>/heartbeat` and `/api/entries/<name>/connect`.
- **TLS Choice (`--tls-mode`, default `hybrid`)**:
  - `pkg/registry/api` refuses plain `http://` for non-loopback URLs.
  - In `hybrid` mode (default), the HTTPS proxy attaches:
    1. A **Google-managed SSL certificate** covering `<registry_host>` plus every
       pre-declared `--entry <name>` (`<name>.<instance_domain>`), giving
       browser-trusted TLS and zero custom CA flags on connected daemons once
       `ACTIVE` (~10–15 minutes);
    2. A **self-signed wildcard fallback certificate** covering
       `*.<instance_domain>` so dynamically added `<entry>.<instance_domain>`
       names still terminate TLS immediately (`curl -k` / browser click-through,
       following the "TLS later" decision).
  - Pass `--tls-mode self-signed` for instant provisioning without waiting for
    Google's ACME certificate issuance.

## Prerequisites

- Tools on the operator/deployer machine: `gcloud`, `terraform` (>= 1.5),
  `docker`, `jq`, `curl`, `openssl`.
- Active `gcloud` credentials with sandbox project permissions:
  - Minimum with a service-account deployer (`roles/editor` + IAP/KMS):
    `roles/editor`, `roles/iap.admin`, and `roles/cloudkms.admin` (plus
    `roles/iap.tunnelResourceAccessor` and `roles/compute.osAdminLogin` if
    running `register-entry.sh` over IAP SSH from the deployer machine). When
    `resourcemanager.projects.setIamPolicy` or `iam.serviceAccounts.getAccessToken` is
    absent, `up.sh` automatically reuses the project's default Compute Engine
    service account for the VM and the deployer's service account for IAP
    verification.
  - Or `roles/owner` (plus `roles/iap.tunnelResourceAccessor` for IAP SSH) on
    the target sandbox project.

## Quick Start

### 1. Bring up the test registry (`up.sh`)

```bash
./deploy/test/registry-sandbox/up.sh \
  --project my-gcp-sandbox \
  --admin alice@example.com \
  --entry alice
```

`up.sh` builds and pushes `Dockerfile.registry` to
`us-docker.pkg.dev/<project>/grain/grain-registry:<tag>`, creates a GCS state
bucket `gs://<project>-<prefix>-tfstate`, applies Terraform, reconciles the VM
over IAP SSH, waits for `/healthz` + `/.well-known/jwks.json` + Google-managed
TLS, and registers the `alice` connected entry.

### 2. Connect a `grain` daemon

Using the `grc_...` registration token printed by `up.sh` (or minted later via
`register-entry.sh`):

**On a `setup.sh`-installed `grain-daemon.service` host:**

```bash
sudo tee -a /etc/default/grain-daemon >/dev/null <<'EOF'
GRAIN_EXTRA_DOCKER_RUN_ARGS="-e GRAIN_REGISTRY_CONNECT=true -e GRAIN_REGISTRY_ISSUER=grain-registry -e GRAIN_REGISTRY_AUDIENCE=https://<entry>.<instance_domain> -e GRAIN_REGISTRY_KEYS_URL=https://<registry_host>/.well-known/jwks.json -e GRAIN_ALLOWED_PRINCIPALS=user:alice@example.com"
EOF
sudo systemctl restart grain-daemon.service

grain registry register \
  -registry-url "https://<registry_host>" \
  -registry-token "grc_<token>" \
  "<entry>"
```

**To connect another daemon later:**

```bash
./deploy/test/registry-sandbox/register-entry.sh \
  --project my-gcp-sandbox \
  --entry colleague \
  --owner alice@example.com \
  --allow-principal user:colleague@example.com
```

(If you also want a Google-managed browser-trusted TLS certificate for the new
entry's subdomain instead of the self-signed wildcard fallback, re-run `up.sh`
with `--entry alice --entry colleague` and `--extra-user colleague@example.com`.)

### 3. Verify end-to-end (`verify.sh`)

```bash
./deploy/test/registry-sandbox/verify.sh \
  --project my-gcp-sandbox \
  --entry alice
```

Checks public JWKS, IAP unauthenticated rejection, `/api/directory` (live
heartbeat status, version, queue counts), `/api/route`, and an end-to-end HTTP
request routed over the outbound WebSocket mux tunnel to the connected daemon's
`registry` identity verifier.

### 4. Tear down cleanly (`down.sh`)

```bash
./deploy/test/registry-sandbox/down.sh \
  --project my-gcp-sandbox
```

Destroys all Compute/LB/IAP/NAT/IAM resources via Terraform, schedules enabled
Cloud KMS signing key versions for destruction, deletes the sandbox image tags
pushed by `up.sh` (preserving any shared Artifact Registry repository), and
removes the GCS Terraform state bucket. Re-running `up.sh` afterwards
automatically re-imports the empty KMS key ring/key shell and creates a fresh
key version.

### Offline / Local Smoke Test

To test `registry.yaml` validation, `register-entry.sh` credential minting,
heartbeats, `/api/directory`, `/api/route`, and outbound-tunnel routing locally
without GCP credentials:

```bash
./deploy/test/registry-sandbox/local-smoke-test.sh
```

## Approximate Monthly Cost (if left running 24x7 in `us-central1`)

| Resource | Configuration | Approx. Monthly Cost (USD) |
|---|---|---:|
| GCE VM + Boot Disk | 1x `e2-small` + 20 GiB `pd-balanced` (hosts registry + Postgres) | ~$14.20 |
| Global External Application LB | 1 forwarding rule + minimal test traffic | ~$18.00 |
| Static Global IPv4 | 1 reserved global address (attached to active LB) | ~$0.00 |
| Cloud NAT | 1 gateway (1 NAT IP + minimal egress) | ~$4.50 |
| Cloud KMS + Artifact Registry + GCS state | 1 software EC P-256 key version + <1 GiB storage | <$0.30 |
| **Total (24x7)** | | **~$37 / month** (~$0.05 / hour; **$0** after `./down.sh`) |
