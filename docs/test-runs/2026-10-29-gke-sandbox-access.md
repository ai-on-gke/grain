# GKE deployment: sandbox-access acceptor across restarts, the GKE overlay, and `deploy-gke.sh`

- **Plan:** `docs/test-plans/2026-10-29.md` §7 (PRs #108, #124, #118, #99, #123)
- **Date:** 2026-10-29, 14:15–15:20 UTC
- **Task:** [Task #1801](/tasks/1801), `grain/049f1e09/task-1801` on `ai-on-gke/grain`, base `main` at `d71aa7e4` (origin: [Task #1753](/tasks/1753))
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), `linux/amd64`. No GKE cluster, no nested virtualization, no GCP credentials. No live GCP call was made.
  - Go `go1.26.10`. `grain` built from the branch: `go build -o /home/debian/bin/grain ./cmd/grain`.
  - `sqlite3` 3.40.1 (`sudo apt-get install -y sqlite3`), `shellcheck` 0.9.0.
  - Downloaded into `/var/tmp/tools`: `kubectl` v1.31.2 (`dl.k8s.io`), `kustomize` v5.5.0, `kubeconform` v0.6.7, `terraform` v1.9.8 (`releases.hashicorp.com`).
  - A throwaway Go helper, `acchelper`, built from the module and kept outside the tree (`/var/tmp/w7/acchelper/main.go`). It has two modes:
    - `mint` loads the daemon's own `<data-dir>/sandbox-access-ca.pem` through `sandboxaccess.LoadOrCreateCA`, mints one sandbox's credentials with `Acceptor.Mint`, writes the Secret's four files, and prints `TokenHash` and `NotAfter`.
    - `dial` runs granule's real wrapper side (`granulesandbox.NewServer(...).DialOut`) against an acceptor, using those files.
  - A fake `kubectl` (`/var/tmp/w7/kubectl`) logs every call and answers `get pods` with an empty list.

## Outcome

| Step | Result |
|---|---|
| 1. Real daemon with the acceptor, `SIGTERM`, restart, wrappers redial; `sandbox_access` read in SQLite | **Pass after fix (F2).** The acceptor ran on `-sandbox-access-addr`. The CA was created `0600` and kept across three lifetimes (same SHA-256 fingerprint). An unregistered wrapper was refused. At restart, an orphan row (no run holds it) was re-registered and then cleared by the startup reap. The expired credential was refused at TLS. A wrong token hash was refused. **The row of a sandbox whose run had already finished survived every restart.** Fixed and re-verified on the real daemon. Re-admission of a *held* sandbox could not be driven through the daemon without a cluster (see below). It is covered by `TestK8sGranuleRestartReadmitsFromStoredHashAndRefusesReleased`, which runs two lifetimes over one store with a real `sandboxaccess.Acceptor` on TCP. That test was re-run here and passes. |
| 2. Render, schema-validate and read the manifests; `terraform validate` | **Pass, with one fix (F1) and one follow-up (FU2).** Both kustomize renderers produce identical output. kubeconform `-strict` (K8s 1.31 plus the CRD catalog) passes all 36 resources of the GKE overlay and all 41 resources of the base manifests. Every NetworkPolicy rule matches the design: the acceptor port is reachable only from sandbox pods, and `network: none` pods are selected by no internet rule. Terraform `validate` and `fmt -check` are clean. **However, `--adopt-cluster` never checked that the cluster enforces NetworkPolicy (F1).** Separately, the opt-in `create_iap_brand` uses an API Google shut down on 2026-03-19 (FU2). |
| 3. `deploy-gke.sh --dry-run` in all six modes, compared with `docs/deploy-gke.md` | **Pass with findings.** All six modes exit 0. A fake `gcloud`/`gsutil`/`terraform`/`helm` on `PATH` recorded **zero** live calls. The stages match the doc. **`--check` step 3 runs `grain sandbox probe`, which does not exist (FU1).** There are smaller doc drifts (below). |

## Steps and observations

### 1. Restart path with a real daemon

The daemon was started with this command line (`/var/tmp/w7/run.sh`):

```
grain daemon -data-dir /var/tmp/w7/data -ui-addr 127.0.0.1:18190 -sandbox-port-base=0 \
  -as alice -agy-path /bin/true -gemini-api-key-file /var/tmp/w7/gem \
  -github-host 127.0.0.1:9 -github-insecure-http \
  -k8s-granule-sandboxes -k8s-kubectl /var/tmp/w7/kubectl -k8s-namespace grain \
  -k8s-workspace /home/agent -k8s-git-proxy-host 10.9.9.9 \
  -sandbox-access-addr 127.0.0.1:14244 -sandbox-access-dial 127.0.0.1:14244
```

The first two attempts were refused at startup and named the missing flags: `-k8s-workspace is required…` and then `-k8s-git-proxy-host is required…`. That is the intended behaviour.

**Lifetime 1.**
- The log showed `sandbox access acceptor on 127.0.0.1:14244; wrappers dial 127.0.0.1:14244, guest traffic to [] dropped in-pod`. The list is empty because `GRAIN_POD_IP` and `GRAIN_ACCESS_SERVICE_HOST` are unset outside a pod.
- `data/sandbox-access-ca.pem` was `-rw-------`, with subject `CN = grain sandbox access CA e4084e93`, which is the deployment id.
- Four sandboxes' credentials were minted from that CA file:
  - `901-1`, valid for 1 h
  - `902-1`, valid for 2 s
  - `903-1`, valid for 1 h
  - `904-1`, valid for 1 h
- Dialing `901-1` was refused: `sandbox: authentication failed: sandbox "901-1" has no credentials`.
- `SIGTERM` ended the daemon cleanly: `shutdown interrupted 0 run(s)`.

**Between lifetimes**, `sandbox_access` was seeded with `sqlite3`:
- `901-1`: its real hash and expiry.
- `902-1`: its real hash and expiry, already in the past.
- `903-1`: its real hash, plus a `task_run` row `r903` for sandbox `903-1` with `finished_at` set.
- `904-1`: a hash of 64 zeros.

**Lifetime 2.** Each wrapper redialed as soon as the port opened. `sandbox_access` was then read.

| Sandbox | Wrapper | Row after startup |
|---|---|---|
| `901-1` (valid, no run holds it) | refused, `has no credentials`: the startup `ReapOrphans` revoked it before the dial | cleared |
| `902-1` (expired) | refused at TLS: `x509: certificate has expired…` (the wrapper keeps retrying; each attempt is logged and refused) | cleared |
| `903-1` (run finished) | refused, `has no credentials` (never registered) | **kept** |
| `904-1` (wrong hash) | refused, `has no credentials` | cleared |

The CA fingerprint was unchanged: `50:E7:F8:48:…:27:1C`. The `903-1` row was still present after the second `SIGTERM`. Nothing would ever clear it (F2).

**Lifetime 3**, after the F2 fix, on the same data dir: `select count(*) from sandbox_access` returned `0` after startup, and the `903-1` wrapper was refused.

**Not exercised through the daemon: re-admission of a sandbox a live run still holds.**
- At startup the daemon readopts a run's sandbox only through run-host adoption (`adoptAll`). Without a cluster that has no run host to match.
- A seeded unfinished `task_run` is therefore finished as interrupted, and its row is then rightly cleared.
- That path is covered in Go instead. `go test -race -run TestK8sGranule ./pkg/orchestrator` passes, including `TestK8sGranuleRestartReadmitsFromStoredHashAndRefusesReleased`. In that test:
  - Lifetime 1 is `sandboxaccess.Listen` on `127.0.0.1:0` with a `K8sGranuleSandboxes` over a SQLite store. A wrapper connects and runs `echo`.
  - Lifetime 2 is a new acceptor and backend over the same store and CA. The same wrapper is admitted before `Readopt`, again after `Readopt`, and refused after `Release`, which also deletes the row.

### 2. Manifests, NetworkPolicies and Terraform

```
kustomize build deploy/kubernetes/gke            # 36 objects; identical to `kubectl kustomize`
kubeconform -strict -summary -kubernetes-version 1.31.0 -schema-location default \
  -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' <rendered>
#   gke overlay: Valid 36, Invalid 0, Errors 0, Skipped 0 (Gateway, HTTPRoute, GCPBackendPolicy,
#   BackendConfig and SecretProviderClass validated against their CRD schemas)
#   deploy/kubernetes/*.yaml: github-webhook 5, grain-granule 15, grain 17, headroom 2,
#   networkpolicy 1, runhost-rwx 1 -- all valid
terraform init -backend=false && terraform validate && terraform fmt -check -recursive
#   valid; two deprecation warnings (google_iap_brand, google_iap_client)
```

The base `deploy/kubernetes/` has no `kustomization.yaml`; its files are applied one by one. `sandbox-access-networkpolicy.yaml` no longer exists: #123 folded it into `deploy/kubernetes/gke/networkpolicy.yaml`, and `TestDeployGKEManifestsAndBaseSync` asserts it stays gone.

The rendered policies, read rule by rule against `design/design/grain-on-gke.design.md` (Security, point 3):

- **`grain-access-ingress`** selects the daemon pod. The rendered Deployment carries `app.kubernetes.io/name=grain` and `app.kubernetes.io/component=daemon`.
  - TCP 4244 is admitted only `from` pods with `app.kubernetes.io/managed-by=grain` and `app.kubernetes.io/component=sandbox`. These are the labels `pkg/k8s.Manifest` stamps on every sandbox pod (`ManagedByValue`, `SandboxValue`).
  - The guest-facing ports (53, 80, 443, 9728, 9744–9746) are admitted from the same pods only.
  - 8080 is admitted only from `130.211.0.0/22`, `35.191.0.0/16` and `10.129.0.0/23`. That last range matches Terraform's proxy-only subnet.
  - Nothing else reaches the daemon pod. **Matches.**
- **`grain-sandbox-egress`** has the pod selector managed-by/component `sandbox` **and** `grain.dev/network NotIn [none]`. Its egress rules are:
  1. 4244 to the daemon pod (the wrapper rule, kept separate).
  2. The guest proxy ports to the daemon pod.
  3. kube-dns.
  4. `0.0.0.0/0` except the metadata server, RFC 1918 and link-local ranges.

  Ingress is empty. **Matches.**
- **`grain-sandbox-network-none`** selects `grain.dev/network: none`, which `k8s_granule_sandboxes.go:696` sets on such pods.
  - Ingress is empty.
  - Egress goes only to the daemon pod, on DNS, 80, 443, 4244, 9728 and 9744–9746, plus kube-dns.
  - Because `grain-sandbox-egress` excludes these pods with `NotIn`, **no policy carrying an internet rule selects a `network: none` pod.** **Matches.**
- **`grain-webhook-isolation`**: egress is `[]`, and ingress on 8080 comes only from the daemon pod and the load balancer ranges.
- NetworkPolicy cannot keep the guest of an inline-mode pod off 4244, because the wrapper and the guest share the pod IP. That is left to granule's netshim drop and to the acceptor's mTLS check. The policy file says so.

Two pieces of design-doc drift were corrected here:
- The design said 4244 is admitted from pods labelled `app=grain-sandbox`, and 8080 from "the Gateway's proxy-only subnet and `grain-webhook`".
- The manifests use the `pkg/k8s` labels, and admit nothing in-cluster to 8080 (grain polls the webhook).

### 3. `deploy-gke.sh --dry-run`, every mode

Common arguments: `--project grain-proj --dns-name grain.example.com --iap-principal user:alice@example.com --staging-bucket grain-stage --data-dir /var/tmp/w7/data --dry-run`. `PATH` had a logging, failing `gcloud`, `gsutil`, `terraform` and `helm` in front of the real `kubectl` and `kustomize`.

| Mode | Exit | Output lines | What it printed |
|---|---|---|---|
| create | 0 | 67 | Stage 1:<br>- `services enable`<br>- node GSA<br>- `clusters create … --enable-dataplane-v2 --workload-pool … --gateway-api=standard --enable-secret-manager`<br>- the nested-virt `grain-sandbox-pool` (`--enable-nested-virtualization --workload-metadata=GKE_METADATA`)<br>- namespace with `enforce=privileged`<br><br>Stage 2: Workload Identity, bucket, 8 Secret Manager containers each with its own `secretAccessor`, `secretVersionAdder` on `grain-sandbox-ca-key`, Cloud SQL PG16 with an IAM user.<br><br>Stage 3: `kubectl apply` of the rendered overlay pinned by digest.<br><br>Stage 4: IP, cert, `GRAIN_IAP_AUDIENCE`, and IAP bindings on the two backend services only.<br><br>Stage 5: hostd token `0600`, the drop-in, `restart grain-hostd.service` only. |
| `--adopt-cluster` | 0 | 67 | The same as create, except `clusters update --workload-pool … --gateway-api=standard --enable-secret-manager` replaces `clusters create`. **Before F1 nothing checked the cluster's NetworkPolicy enforcement.** After F1 the dry run prints `Would verify that adopted cluster 'grain-gke' enforces NetworkPolicy … and refuse otherwise`. |
| `--migrate` | 0 | 12 | `POST /api/database/upgrade` with the Cloud SQL DSN; `secrets versions add grain-secrets-key`; a 30-day `transcripts/`/`artifacts/` tar to GCS; scale to 0; `Job/grain-data-seed`. |
| `--cutover` | 0 | 12 | `dispatch/pause`, `backups/run`, stop and disable `grain-daemon.service`, scale to 1, rollout status. |
| `--rollback` | 0 | 10 | Scale to 0 and wait for pod deletion, remove the hostd drop-ins, restart hostd, `enable --now grain-daemon.service`. |
| `--check` | 0 | 11 | IAP must answer 302/401/403; `/api/executors`; `grain sandbox probe … --access=dialout`; an audit-log query for exec/attach/portforward. |

Compared with `docs/deploy-gke.md`:

- **`--check` step 3 cannot pass on a real deployment.** It runs `kubectl exec deploy/grain -- /usr/local/bin/grain sandbox probe --id=… --access=dialout`. The `grain` binary has no `sandbox` command: `grain sandbox probe` prints `grain: unknown command "sandbox"` and exits 1. Step 4's audit-log query is keyed on that probe id, so it proves nothing until step 3 creates a sandbox. **Follow-up FU1.**
- **`--check` in dry-run ends with `All 4 verification checks passed`.** In a dry run nothing has been checked.
- **`--migrate` and the doc disagree on how the secret is passed.** The doc says secrets go up with `gcloud secrets versions add --data-file=-` (stdin). The script passes the key's path (`--data-file=<data-dir>/secrets/secrets.key`). Neither logs the value. Only the doc wording is off.
- **`--cutover` does not drain.** The doc says it "drains in-flight work". The script pauses dispatch and backs up, but has no wait for in-flight runs. Its step 4/4, "Ensuring capability placement…", runs only `systemctl is-active grain-hostd.service`.
- **`--rollback` matches the doc.**
- **Temporary files.** The dry run's manifest temp file under `/tmp` was cleaned up. The real `--migrate` uses `mktemp` (0600) for the archive. The `/tmp/grain-pvc-seed.tar.zst` path in the dry-run output is only a label.

`--adopt-cluster` after F1, with `GRAIN_GKE_ASSUME_NETWORK_POLICY` standing in for the describe:
- `ADVANCED_DATAPATH`: proceeds, logging `enforces NetworkPolicy (ADVANCED_DATAPATH)`.
- `CALICO`: proceeds.
- `NONE`: exits 1 with `refusing to adopt cluster 'grain-gke': it does not enforce NetworkPolicy…`, before any `clusters update` or `kubectl apply`.

## Findings, fixes and follow-ups

### Fixed here

- **F1 (security): `--adopt-cluster` deployed onto clusters that ignore NetworkPolicy.** `scripts/deploy-gke.sh` adopted any existing cluster, whether through `--adopt-cluster` or because `clusters describe` found one. It ran `clusters update` for Workload Identity, Gateway and Secret Manager, but never checked enforcement. On a GKE cluster with neither Dataplane V2 nor `--enable-network-policy`, all four policies are accepted and ignored:
  - 4244 is open to every pod in the cluster.
  - `network: none` sandboxes reach the internet.
  - Sandboxes reach node and pod CIDRs.
  - The daemon still runs with `-k8s-network-policy`.

  The fix:
  - `require_network_policy_enforcement` reads `networkConfig.datapathProvider` and `networkPolicy.enabled`, and refuses before changing anything. `GRAIN_GKE_ASSUME_NETWORK_POLICY` stands in for tests, in the same way as the existing `GRAIN_GKE_ASSUME_*` variables.
  - The Terraform module's adopted-cluster data source gets a matching `postcondition`.
  - `docs/deploy-gke.md` states the requirement.
  - Tests: the subtest `adopt cluster requires network policy enforcement` in `TestDeployGKEScriptDryRunAllModes` covers the dry-run notice, both accepted modes, and the refusal (with no `clusters update` or `kubectl apply` printed). `TestDeployGKETerraformModule` asserts the postcondition.
- **F2: a `sandbox_access` row of a sandbox whose run had finished was never cleared.**
  - `SandboxAccessCredentials` excludes such rows, so they are never re-admitted. That part is correct.
  - But `sweepOrphanAccess` builds its candidates from the same list, so nothing ever deletes them.
  - A daemon that dies between finishing a run and releasing its sandbox leaves the token hash behind for good. Lifetime 2 above showed this.

  The fix:
  - `model.Store.DeleteFinishedSandboxAccess` deletes rows that a finished `task_run` or `ci_job` claims and no unfinished one does.
  - The k8s-granule orphan sweep (startup `ReapOrphans` and every reconcile tick) calls it.
  - Test: `TestK8sGranuleSweepClearsFinishedRunsStoredHash` covers finished, running, unclaimed, and finished-plus-running rows, and the sweep. It fails without the sweep call. It was re-verified on the real daemon (lifetime 3).
- **Design-doc drift:** `design/design/grain-on-gke.design.md` now names the real sandbox labels and the real 8080 sources, and notes the enforcement requirement.

### Follow-ups proposed

- **FU1:** `deploy-gke.sh --check`'s dial-out step calls a `grain sandbox probe` command that does not exist. Either add the command, which acquires a k8s-granule sandbox, runs one command over the dial-out channel and releases it, or rework steps 3 and 4. The fix should also stop the dry run from claiming `All 4 verification checks passed`.
- **FU2:** `deploy/gke/terraform`'s opt-in `create_iap_brand` path uses `google_iap_brand` and `google_iap_client`. Google shut down the IAP OAuth Admin API they depend on on 2026-03-19 (the provider's deprecation warning says so). The option should be removed, or documented as broken, in favour of the Google-managed OAuth client (the default) or an existing `iap_client_id`. The default (`false`) is unaffected.

### Doc drift left as observations

These are in `docs/deploy-gke.md` against the script, and are not security-relevant:
- `--migrate` describes `--data-file=-` where the script passes the key's path.
- `--cutover` says "drains in-flight work", but the script only pauses dispatch and backs up.
- `--cutover` step 4/4's log line overstates what it does.

### Notes for later runs

- `pgrep -f`/`pkill -f` with a pattern from the daemon's command line also matches the `run_command` shell that runs it, and killed it (exit 255). Use `pgrep -x grain`.
- `sqlite3` is still absent from the sandbox image. `kubectl`, `kustomize`, `kubeconform` and `terraform` all download fine from their release hosts.
