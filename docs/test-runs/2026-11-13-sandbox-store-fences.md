# Walkthrough: sandbox and store fences — 2026-11-13

Plan: [`docs/test-plans/2026-11-13.md` §12](../test-plans/2026-11-13.md) (PRs #1139, #899).
Task 5364. Commit walked: `17f00df68` (`main` as of 2026-11-13 03:26 UTC). Helpers and raw output are in
[`2026-11-13-sandbox-store-fences/`](2026-11-13-sandbox-store-fences/).

| Item | Verdict |
|---|---|
| 1. Open-network egress fence on a real cluster | **Pass** for open-network pods. **Fail, then fixed** for `network: none` with `grain.yaml` alone (F1). |
| 1. External grants refused without `-k8s-network-policy`, naming `grain settings -k8s-network-policy=true` | **Pass**, proven through a real dispatch cycle (new regression test). Not proven through an in-cluster daemon. |
| 2. Lease fencing: SIGSTOP the leader, second takes over, first's writes refused, no row written by both | **Partial.** Fencing is correct, but a frozen leader is never replaced (F2, filed as a follow-up). Once the frozen leader's sessions are ended, the takeover, refusal and no-double-write all hold. |

## Environment

- Sandbox VM: Debian 12, 7 vCPU, 30 GB RAM, Docker 29.9.3, open network.
- **Item 1:** `kind` v0.32.0 with a two-node Kubernetes v1.36.1 cluster ([`kind.yaml`](2026-11-13-sandbox-store-fences/kind.yaml): default CNI off, pod CIDR `192.168.0.0/16`, node IPs `10.241.0.2`/`10.241.0.3`). It runs **Calico v3.29.3** (`calico.yaml` manifest), which enforces NetworkPolicy.
  - The kind nodes could not pull from Docker Hub, but the VM could. So the Calico, `curlimages/curl:8.11.1` and `busybox:1.37` images were pulled on the VM, saved with `docker save --platform linux/amd64` and imported with `ctr -n k8s.io images import`. `kind load docker-image` failed on Docker 29 (`content digest … not found`).
- **Item 2:** PostgreSQL 15.19 (Debian `postgresql-15`), a scratch cluster on `127.0.0.1:4290` with scram-sha-256 ([`lib.sh`](2026-11-13-sandbox-store-fences/lib.sh) `pg_up`). Two `grain daemon` processes built from `HEAD` (`go build -o /home/debian/bin/grain ./cmd/grain`) ran with `-sandbox-backend host -sandbox-port-base=0`, separate data dirs, UI ports 4294/4296 and one `-postgres-url`.
- **Stubbed:** only the cloud metadata server. kind has none, so `169.254.169.254/32` was added to the worker node's `lo`. A `hostNetwork` busybox `httpd` on the worker serves `/computeMetadata/v1/instance/service-accounts/` there. Everything else was real: CNI, API server, kubelet, CoreDNS, pods, PostgreSQL and daemons.

## Item 1 — Kubernetes egress fence

### Steps

[`item1-egress.sh`](2026-11-13-sandbox-store-fences/item1-egress.sh) (helpers in [`lib-k8s.sh`](2026-11-13-sandbox-store-fences/lib-k8s.sh)) applies `deploy/kubernetes/grain.yaml` **alone** to the kind cluster. The Deployment itself stays in `ImagePullBackOff` because the nodes cannot reach Docker Hub. The probes do not need it: a stand-in pod carries the grain pod's label `app.kubernetes.io/name=grain`.

The script then starts three long-lived curl pods and probes nine targets from each:

- `control`: namespace `default`, no labels.
- `sbx-open`: namespace `grain`, with the labels `pkg/k8s` puts on every sandbox pod (`app.kubernetes.io/managed-by=grain`, `app.kubernetes.io/component=sandbox`).
- `sbx-none`: the same, plus `grain.dev/network=none`, which `K8sSandboxes` adds for a `network: none` task.

A probe is *blocked* when curl times out (exit 28, or 6 for an unresolvable name) and *reached* otherwise.

### Results with `grain.yaml` as shipped ([`out/item1-before-fix.out`](2026-11-13-sandbox-store-fences/out/item1-before-fix.out)) and with the fix ([`out/item1.out`](2026-11-13-sandbox-store-fences/out/item1.out))

| Target | control | open sandbox | `network: none` (before) | `network: none` (after fix) |
|---|---|---|---|---|
| internet (`https://example.com`) | reached | **reached** | reached ✗ | blocked |
| cluster DNS (name of the grain stand-in) | reached | reached | reached ✗ | blocked |
| metadata stub `169.254.169.254` | reached | **blocked** | reached ✗ | blocked |
| API server Service VIP | reached | **blocked** | reached ✗ | blocked |
| API server on the control-plane node `:6443` | reached | **blocked** | reached ✗ | blocked |
| peer pod (namespace `default`) | reached | **blocked** | reached ✗ | blocked |
| unlabelled pod in namespace `grain` | reached | **blocked** | reached ✗ | blocked |
| worker node kubelet `:10250` | reached | **blocked** | reached ✗ | blocked |
| grain pod (stand-in) | reached | reached | reached | reached |

The open-network sandbox pod behaves exactly as PR #1139 promises. The control pod reaches every target, so each *blocked* is the policy's doing, not a dead target.

### F1 — `grain.yaml` alone leaves `network: none` pods unfenced once `-k8s-network-policy` is set (fixed)

**What:** `grain-sandbox-egress` selects sandbox pods whose `grain.dev/network` is *not* `none`. It has to, because policies are additive. The policy that holds `network: none` pods, `grain-sandbox-network-none`, shipped only in `networkpolicy.yaml`, as an "optional" opt-in. But `-k8s-network-policy` means two things:

- K8sSandboxes accepts `network: none` tasks (`k8s_sandboxes.go`: refused unless `NetworkPolicyEnforced`);
- external capability grants are allowed (`mintCapabilityPass`).

An operator who followed the README to the letter would end up with no policy on those pods: apply `grain.yaml`, check an open pod cannot reach the metadata server, uncomment the flag (wanted for capability grants). From then on, every `network: none` task runs in a pod that **no policy selects**. That pod has more reach than an open-network one: the metadata server, the API server, every pod and node. That is the outcome `networkpolicy.yaml`'s own header calls "worse than not doing it at all".

**Reproduction:** `MANIFESTS=<grain.yaml before the fix> item1-egress.sh`. The `sbx-none` column reaches all nine targets. In-tree: `TestGrainYAMLShipsTheNetworkNonePolicy` fails on the old `grain.yaml` with "grain.yaml ships no grain-sandbox-network-none".

**Fix (commit `fix(k8s): ship grain-sandbox-network-none in grain.yaml`):**

- `grain.yaml` now ships a verbatim copy of `grain-sandbox-network-none`. It only selects pods grain creates once the flag is set, so a deployment without the flag sees no change.
- `pkg/orchestrator/k8s_networkpolicy_test.go`: the new test checks the copy is the same object as `networkpolicy.yaml`'s and is the only policy selecting a `network: none` pod. The old assertion that the manifests must *not* carry it is gone. `tests/deploy/kubernetes_manifest_test.go`'s kind list counts the second `NetworkPolicy`.
- The README now applies `networkpolicy.yaml` as a no-op extra, and its fence check also probes a `network: none` pod. The comments in `networkpolicy.yaml` and `grain.yaml` now say both policies are bundled.

With the fix applied to the same cluster, `sbx-none` reaches only the grain pod (table above).

`grain-granule.yaml` was left alone. A granule `network: none` guest boots with no network device (`K8sGranuleSandboxes.IsolatesNetwork`, `CHV_NET_MODE=none`), and the granule backend labels such pods whether or not the flag is set. Adding the policy there would change what the granule shim pod itself can reach, and that was not walked here.

### External capability grants without `-k8s-network-policy`

This was not run through an in-cluster daemon: the nodes could not pull `docker.io/grainai/grain:main`, and wiring an external capability server would have been a separate project. Instead, the new `TestRunCycleRefusesExternalGrantsOnAnUnfencedKubernetesBackend` (`pkg/orchestrator/k8s_grant_refusal_test.go`) runs a real `RunCycle`:

- the backend is `NewK8sSandboxes` without `NetworkPolicyEnforced`;
- the task holds an external grant (`config-map#config-map={"config_maps":["operator-map"]}`).

The task is parked (`awaiting_reply`) without any capability server being dialed. What it is told contains `-k8s-network-policy`, `` `grain settings -k8s-network-policy=true` ``, `grain-sandbox-egress` and "so it runs no attempt holding a capability grant". `grain settings --help` lists `-k8s-network-policy`.

**Side note (test hygiene):** the first draft of that test built `NewK8sSandboxes` without a `Kubectl` override. The dispatch cycle's sandbox reconciliation ran the VM's real `kubectl` against the current context (the kind cluster) and **reaped the walkthrough's `sbx-open`/`sbx-none` pods** ("reaped 2 orphaned backend sandbox(es)"). The committed test points `Kubectl`/`Kubeconfig` at paths that do not exist. Any future test that constructs a real `K8sSandboxes` and runs a cycle should do the same.

## Item 2 — PostgreSQL lease fencing under SIGSTOP

### Steps

[`item2-lease-sigstop.sh`](2026-11-13-sandbox-store-fences/item2-lease-sigstop.sh) runs these steps:

1. Daemon A takes the lease, files a task, and a loop posts `A write N` comments to it every 0.2 s.
2. `kill -STOP A` for 20 s, which is longer than the 5 s keep-alive plus the 15 s re-acquire window.
3. Start daemon B on the same database and wait up to 45 s for `acquired PostgreSQL single-writer`.
4. `kill -CONT A` and watch for 40 s.
5. If B took over, post 10 `B write N` comments through B.
6. Compare the HTTP results with the `task_comment` rows, by database clock.

With `EVICT=1`, after step 2 the script terminates A's sessions from the database side. That is what an expiring lease, PostgreSQL's `idle_session_timeout` or a TCP keepalive on a dead peer would do.

### Run 1: plain SIGSTOP ([`out/item2-sigstop.out`](2026-11-13-sandbox-store-fences/out/item2-sigstop.out))

- After 20 s frozen, A's 6 sessions are still open and its 2 advisory locks (writer lock and daemon lease) are still granted. The lock session shows `state=idle`.
- B logs `… is held by pid=64992 app="grain-daemon-writer-lock" … state=idle; waiting up to 30s for drain`. 30 s later it exits with `another grain daemon holds the single-writer advisory lock`.
- A resumes and keeps writing: 193 writes acknowledged, 193 rows, 0 rows from anyone else.

**Safety holds:** nothing was written twice. **The takeover never happens.**

### F2 — the lease has no expiry, so a frozen leader is never replaced (filed)

The "lease" is a session-level advisory lock (`WriterLock`, plus `HoldDaemonLease`'s daemon lease) with no time bound. PostgreSQL ends neither session of a frozen process: idle sessions are not timed out, and the server's TCP keepalive is the OS default of 2 hours. A whole VM that stops responding without closing its TCP connection is in the same position until keepalives fire. A replacement daemon fails after its 30 s wait, and the store needs a person to run `pg_terminate_backend`. The plan's "pause the leader past its lease" assumes an expiry that the design does not have.

**Reproduction:** `item2-lease-sigstop.sh` with the default `EVICT=0`. It is deterministic: B always exits after 30 s.

**Not fixed here.** A server-side TTL (`idle_session_timeout` on the lock session) is easy for the writer lock. The daemon lease, though, sits on an idle pooled connection with no keep-alive. It needs the same treatment, or B blocks for `storeLockWait` (90 s) behind the frozen A after winning the writer lock. Either way, choosing a TTL decides when a stalled-but-alive daemon gets fenced. That is a store-semantics change worth its own task. Filed as **"Give the PostgreSQL single-writer lease a server-side expiry so a frozen leader can be replaced"** (depends on task 5364), with this reproduction and a suggested shape.

### Run 2: SIGSTOP plus database-side eviction ([`out/item2-sigstop-evict.out`](2026-11-13-sandbox-store-fences/out/item2-sigstop-evict.out))

- A was frozen at 03:50:51.90. Its 5 sessions were terminated about 20 s later. **B acquired the lease 2 s after starting** (03:51:13.05) and served on 4296.
- A resumed at 03:51:13.09. Its queued writes and its own access-log writes were refused:

  ```
  postgres: write refused: this daemon no longer holds the single-writer lease on the store (its lock session is disconnected)
  ```

  It then logged `single-writer advisory lock lost: … taken by pid=69209 … while session was disconnected` and **exited within a second**.
- Database:
  - 14 `A write` rows, all committed before A was frozen; 14 acknowledged 200;
  - 0 A rows after A was frozen or after B took the lease;
  - 10 `B write` rows;
  - 0 bodies written twice.
- A's writer loop saw 22 timeouts while frozen and exactly one `500` (the refused write). No write was acknowledged without being committed.

That is the behavior item 2 asks for, once something ends the frozen leader's sessions.

Existing unit coverage passes with Postgres actually present (`GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model/postgres -run 'Fence|WriterLock|Lease|Advisory'`):

- `TestFenceWritesRefusesWritesOnceTheLeaseSessionIsGone`
- `TestWriterLockDetectsSplitBrainTakeoverAndNotifiesOnLost`
- `TestWriterLockFencesConcurrentDaemonAndReleasesOnClose`
- three more

No new `pkg/model` test was added in this run. The regression test belongs with the F2 fix, and the follow-up task describes it.

## Commands to rerun

```sh
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client
go build -o /home/debian/bin/grain ./cmd/grain
D=docs/test-runs/2026-11-13-sandbox-store-fences
kind create cluster --name fences --config $D/kind.yaml
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.29.3/manifests/calico.yaml
$D/item1-egress.sh                       # grain.yaml alone
$D/item2-lease-sigstop.sh                # F2: no takeover
EVICT=1 $D/item2-lease-sigstop.sh        # takeover, refusal, no double write
go test ./pkg/orchestrator -run 'NetworkPolic|Bundled|NetworkNone|RefusesExternalGrantsOnAnUnfencedKubernetes'
go test ./tests/deploy ./tests/security
GRAIN_REQUIRE_POSTGRES=1 PATH=$PATH:/usr/lib/postgresql/15/bin go test ./pkg/model/postgres -run 'Fence|WriterLock|Lease'
```
