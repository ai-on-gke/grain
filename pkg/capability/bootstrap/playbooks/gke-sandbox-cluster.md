# Bootstrap playbook: Off-Cluster GKE Sandbox Cluster (`gke-sandbox-cluster`)

This is the runbook for walking an operator through setting up a **GKE
Standard sandbox cluster** (`grain init -platform=gke-sandboxes`) for a
grain controller running outside the cluster, and switching
`[Settings → Sandboxes](/settings/sandboxes)` to `kubernetes-granule`.

In this Stage 1 architecture, the grain controller (daemon, state store,
git proxy, and package cache) continues running on its existing host,
while each task's sandbox boots as a hardware-isolated Granule VM inside
a privileged pod on a nested-virtualization GKE node pool. The host
daemon talks to the cluster through `/var/lib/grain/kubeconfig` using a
least-privilege `grain` ServiceAccount (`pods`, `pods/exec`, `pods/log`)
and bridges its local git proxy and package cache into each pod via
`granule forward` over `pods/exec`.

Every host command below is one you run through `run_host_command` —
each call pauses for the human's live approval in the chat before
anything executes on the host, so say in one line what you are about to
run and why before calling it.

## Before you run anything — what to ask the human for

Ask for these four things in **one** message before making your first
`run_host_command` call, rather than discovering them one question at a
time mid-flow:

1. **The GCP sandbox project ID** (e.g. `grain-sandboxes` or
   `my-org-grain-sandboxes`). Using a dedicated sandbox project keeps
   sandbox node pools, quotas, and IAM cleanly separated from production
   services.
2. **The GCP zone or region** (default `us-central1-a`). Confirm a zone
   where their chosen machine family (`c4-standard-*` or `n2-standard-*`)
   is available.
3. **The GKE Standard cluster name** (default `grain-sandboxes`) and
   **node pool name** (default `kvm-pool`). Note that GKE Autopilot is
   **not** supported because nested virtualization and `/dev/kvm` device
   plugins require GKE Standard with `UBUNTU_CONTAINERD` nodes.
4. **The machine type and pool sizing**:
   - **High-density C4 option**: `c4-standard-96-lssd` (requires
     `--disk-type=hyperdisk-balanced`; supports ~20–40 concurrent 2–4
     vCPU sandbox VMs per node with local SSD scratch).
   - **Smaller N2 option**: `n2-standard-8`, `n2-standard-16`, or
     `n2-standard-32` (uses `--disk-type=pd-balanced`; good for starter
     pools or lower concurrency).
   - **Pool size / autoscaling**: fixed node count (`--num-nodes=1` to
     start) or autoscaled (`--min-nodes=1 --max-nodes=5`).

Once you have those answers, run the steps below in order.

---

## Step 1 — Check credentials, APIs, quotas, and org policies (~15–30s)

Before creating the cluster, verify that `gcloud`, `kubectl`, and
`gke-gcloud-auth-plugin` are installed on the controller host, that the
active `gcloud` identity has access to `<PROJECT>`, that nested
virtualization is not blocked by an Organization Policy, and that the
project has enough CPU / disk quota in `<LOCATION>`.

```sh
gcloud --version && kubectl version --client && gke-gcloud-auth-plugin --version
gcloud auth list
```

Enable the Compute Engine and Kubernetes Engine APIs if they are not
already enabled (~10–20s):

```sh
gcloud services enable compute.googleapis.com container.googleapis.com \
  --project=<PROJECT>
```

Check the Organization Policy for nested virtualization (~5s):

```sh
gcloud resource-manager org-policies describe \
  constraints/compute.disableNestedVirtualization \
  --project=<PROJECT> --effective
```

- **If `booleanPolicy: { enforced: true }` is returned**: stop and tell
  the operator that `constraints/compute.disableNestedVirtualization` is
  enforced on `<PROJECT>`. An organization policy administrator must
  exempt `<PROJECT>` before GKE can create nodes with
  `--enable-nested-virtualization`.
- **If empty / not enforced**: nested virtualization is permitted.

Check regional CPU and disk quota in `<REGION>` (the region prefix of
`<LOCATION>`, e.g. `us-central1` for `us-central1-a`, ~5s):

```sh
gcloud compute regions describe <REGION> --project=<PROJECT> \
  --format="table(quotas.metric,quotas.limit,quotas.usage)" | \
  grep -E "CPUS|C4_CPUS|N2_CPUS|SSD_TOTAL_GB|HYPERDISK"
```

- For `c4-standard-96-lssd`, verify `C4_CPUS >= 96 * <NUM_NODES>`.
- For `n2-standard-8`, verify `N2_CPUS >= 8 * <NUM_NODES>`.

---

## Step 2 — Run `grain init -platform=gke-sandboxes` (or step-by-step commands)

`grain init -platform=gke-sandboxes` is idempotent and resumable, prints
every `gcloud` and `kubectl` command it runs, and supports `-dry-run`.

### 2a. Preview the plan with `-dry-run` (~1s)

Always run `-dry-run` first so the operator can see the exact commands
before anything is created:

```sh
grain init -platform=gke-sandboxes -dry-run \
  -repo-root=/home/debian/work \
  -data-dir=/var/lib/grain \
  -gke-project=<PROJECT> \
  -gke-location=<LOCATION> \
  -gke-cluster=<CLUSTER> \
  -gke-node-pool=<NODE_POOL> \
  -gke-machine-type=<MACHINE_TYPE> \
  -gke-num-nodes=<NUM_NODES>
```

*(Note: `-gke-disk-type` automatically defaults to `hyperdisk-balanced`
when `-gke-machine-type` starts with `c4-` and `pd-balanced` for
`n2-*`.)*

### 2b. Execute the full setup (~6–12 minutes on a new cluster, ~30s on an existing one)

Run the same command without `-dry-run`:

```sh
grain init -platform=gke-sandboxes \
  -repo-root=/home/debian/work \
  -data-dir=/var/lib/grain \
  -gke-project=<PROJECT> \
  -gke-location=<LOCATION> \
  -gke-cluster=<CLUSTER> \
  -gke-node-pool=<NODE_POOL> \
  -gke-machine-type=<MACHINE_TYPE> \
  -gke-num-nodes=<NUM_NODES>
```

Under the hood, `grain init -platform=gke-sandboxes` runs these seven
idempotent steps (with timings):

1. **Create or reuse the GKE Standard cluster (~5–8 min if creating, ~2s if existing)**:
   - For `c4-standard-96-lssd`:
     ```sh
     gcloud container clusters create <CLUSTER> \
       --project=<PROJECT> --zone=<LOCATION> \
       --machine-type=c4-standard-96-lssd \
       --image-type=UBUNTU_CONTAINERD \
       --disk-type=hyperdisk-balanced \
       --disk-size=100 \
       --num-nodes=<NUM_NODES> \
       --enable-nested-virtualization \
       --node-labels=granule.io/kvm=true
     ```
   - For `n2-standard-8`:
     ```sh
     gcloud container clusters create <CLUSTER> \
       --project=<PROJECT> --zone=<LOCATION> \
       --machine-type=n2-standard-8 \
       --image-type=UBUNTU_CONTAINERD \
       --disk-type=pd-balanced \
       --disk-size=100 \
       --num-nodes=<NUM_NODES> \
       --enable-nested-virtualization \
       --node-labels=granule.io/kvm=true
     ```
2. **Ensure the nested-virtualization node pool (~3–5 min if creating a separate pool, ~2s if existing)**:
   ```sh
   gcloud container node-pools create <NODE_POOL> \
     --cluster=<CLUSTER> --project=<PROJECT> --zone=<LOCATION> \
     --machine-type=<MACHINE_TYPE> \
     --image-type=UBUNTU_CONTAINERD \
     --disk-type=<DISK_TYPE> \
     --disk-size=100 \
     --num-nodes=<NUM_NODES> \
     --enable-nested-virtualization \
     --node-labels=granule.io/kvm=true
   ```
3. **Install granule's KVM device plugin (~30–60s)**:
   Applies `third_party/granule/deploy/k8s/kvm-device-plugin.yaml` and
   waits for `daemonset/granule-device-plugin` in `kube-system` to roll
   out so every nested-virt node advertises `granule.io/kvm: 20`:
   ```sh
   kubectl apply -f third_party/granule/deploy/k8s/kvm-device-plugin.yaml
   kubectl -n kube-system rollout status daemonset/granule-device-plugin --timeout=2m
   ```
4. **Create the `grain` namespace and least-privilege ServiceAccount RBAC (~5s)**:
   Creates the `grain` Namespace labelled with
   `pod-security.kubernetes.io/enforce=privileged`, a `grain`
   ServiceAccount + Role + RoleBinding scoped strictly to `pods` (`get`,
   `list`, `watch`, `create`, `delete`), `pods/exec` (`create`), and
   `pods/log` (`get`) in `grain`, and the three cluster-scoped sandbox
   `PriorityClasses` (`grain-interactive`, `grain-agent`, `grain-ci`).
5. **Write `/var/lib/grain/kubeconfig` (~5s)**:
   Mints a bearer token for ServiceAccount `grain/grain`, writes a
   self-contained kubeconfig to `/var/lib/grain/kubeconfig` (the default
   path `daemon.EffectiveK8sKubeconfig` reads when `-k8s-kubeconfig` is
   unset), restricted to mode `0600`, and removes the admin user entry
   `gcloud container clusters get-credentials` wrote alongside it so the
   file names only the ServiceAccount. It is owned by whoever ran
   `grain init`: if the daemon runs as a different account (the `grain`
   user, under systemd), `sudo chown grain:grain /var/lib/grain/kubeconfig`
   before restarting it.

---

## Step 3 — Verification (~60–120s)

`grain init -platform=gke-sandboxes` automatically runs both
verification checks at the end of its run, and you can also run them
standalone at any time:

1. **Platform KVM verification (`scripts/platform-verify/k8s-granule.sh`, ~30–60s)**:
   Confirms a nested-virt node advertises `granule.io/kvm`, schedules a
   privileged pod requesting `granule.io/kvm: "1"`, boots a real VM over
   `/dev/kvm`, and verifies `granule ready` and `granule exec -- uname -r`:
   ```sh
   KUBECONFIG=/var/lib/grain/kubeconfig \
     IMAGE=ghcr.io/bwsalmon/grain/guest:main \
     ./scripts/platform-verify/k8s-granule.sh --keep
   ```
2. **Throwaway sandbox git-proxy clone and package-cache fetch (~30–60s)**:
   Boots one throwaway `k8s-granule` sandbox pod using
   `/var/lib/grain/kubeconfig` and verifies inside the guest VM that:
   - `git -c http.proxy=<GIT_PROXY_URL> ls-remote https://github.com/ai-on-gke/grain.git`
     succeeds through the controller's git proxy.
   - `curl -fsSL --proxy <PACKAGE_CACHE_URL> https://proxy.golang.org`
     succeeds through the controller's package cache.

Verify least-privilege RBAC on `/var/lib/grain/kubeconfig` (~3s):

```sh
kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain auth can-i create pods       # yes
kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain auth can-i create pods/exec  # yes
kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain auth can-i get pods/log      # yes
kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain auth can-i get secrets       # no
kubectl --kubeconfig=/var/lib/grain/kubeconfig auth can-i create namespaces          # no
```

---

## Step 4 — Switch `[Settings → Sandboxes](/settings/sandboxes)` to `kubernetes-granule`

Once verification passes and `/var/lib/grain/kubeconfig` is in place:

1. Direct the operator to **[Settings → Sandboxes](/settings/sandboxes)**
   in the grain UI (or update the setting over the local API), and switch
   the sandbox backend to **`kubernetes-granule`**.
2. Confirm the `kubernetes-granule` settings match the cluster setup:
   - **Namespace**: `grain`
   - **Kubeconfig**: `/var/lib/grain/kubeconfig` (picked up automatically
     by `daemon.DefaultKubeconfigPath` when left blank)
   - **Guest image**: `ghcr.io/bwsalmon/grain/guest:main` (or the
     deployment's stamped guest image)
3. Dispatch a lightweight smoke task and confirm in **[System](/system)**
   or with `kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain get pods`
   that the sandbox pod schedules on the GKE cluster, reaches `Ready`,
   and cleans up when the run finishes.

---

## Step 5 — Rollback & Teardown

If a dispatch fails on the new cluster or the operator wants to revert
immediately to local host VMs:

1. **Immediate rollback (<5s, zero downtime)**:
   Switch **[Settings → Sandboxes](/settings/sandboxes)** back from
   `kubernetes-granule` to **`granule`** (local Linux KVM) or
   **`container`**. Subsequent dispatches immediately return to the
   local host backend without restarting the daemon.
2. **Delete any in-flight sandbox pods in the cluster (~10s)**:
   ```sh
   kubectl --kubeconfig=/var/lib/grain/kubeconfig -n grain delete pods -l app=grain-sandbox --grace-period=1
   ```
3. **Optional cluster or node-pool teardown (~3–5 min)**:
   If the cluster was created only for testing and should be removed to
   stop billing:
   ```sh
   gcloud container clusters delete <CLUSTER> \
     --project=<PROJECT> --zone=<LOCATION> --quiet
   ```
