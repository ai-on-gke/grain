# Nightly Prompt: Kubernetes & Docker End-to-End Orchestration

## Objective
Walk through and validate end-to-end Kubernetes and Docker orchestration workflows, including Pod manifests, Multi-VM Pods (1:1 container-to-microVM mapping), Ephemeral Debug containers (`kubectl debug`), standalone Docker execution modes, and presenter demo script rehearsal (`demo/demo.sh`).

## Context & Key Workflows
Granule operates natively in Kubernetes and Docker with zero custom host runtime shims or CRI plugins:
1. **Standard Kubernetes Pods**: Running microVMs using `/dev/kvm` hostPath mounts (`deploy/k8s/pod-example.yaml`, `deploy/k8s/gke-pod-example.yaml`).
2. **Multi-VM Pods**: Running multiple isolated microVM containers in a single Pod (`demo/assets/pod-multi-vm.yaml`), sharing Pod network namespace while maintaining hardware VM isolation.
3. **Isolated Sidecars**: Sidecar containers with isolated network stacks (`CHV_NET_MODE=none`).
4. **Ephemeral Debug VMs**: Attaching on-demand debug microVMs via `kubectl debug` (`demo/assets/ephemeral-vm.yaml`).
5. **Docker Execution**: Running containers via `docker run --privileged` or with `--device /dev/kvm --device /dev/net/tun --cap-add=NET_ADMIN`.
6. **Presenter Demo Rehearsal**: Executing `demo/demo.sh` across all chapters (`provision`, `publish`, `local`, `gke`, `pod`, `build`, `rebase`).

## Execution Steps

### 1. Validate Kubernetes Manifests & Pod Specs
Parse and validate all Kubernetes manifests in `deploy/k8s/` and `demo/assets/`:
```bash
# Check YAML structure and syntax
python3 -c '
import yaml, glob
for path in glob.glob("deploy/k8s/*.yaml") + glob.glob("demo/assets/*.yaml"):
    with open(path) as f:
        docs = list(yaml.safe_load_all(f))
        print(f"Validated {path} ({len(docs)} documents)")
'
```
- Verify that every container spec includes appropriate `/dev/kvm` volume mounts and privileged security context.
- Verify multi-VM pod definitions in `demo/assets/pod-multi-vm.yaml` correctly declare container separation.

### 2. Demo Script Dry Run Rehearsal
Run the full presenter demo suite in dry-run mode:
```bash
demo/demo.sh --dry-run --no-color
```
- Verify that every chapter runs and produces steps:
  - `provision`
  - `publish`
  - `local`
  - `gke`
  - `pod`
  - `build`
  - `rebase`
- Assert that no chapter is silently skipped or missing output.

### 3. Validate Docker Run Configurations
Verify the command construction and flags in `pkg/dockervm` and `pkg/dockercli`:
```bash
go test -v ./pkg/dockervm/... ./pkg/dockercli/...
```
- Verify port forwarding flag synthesis (`-p <hostPort>:<guestPort>`).
- Verify device mapping parameters (`/dev/kvm`, `/dev/net/tun`).
- Verify container stop timeout and graceful shutdown handling.

### 4. IPv6 End to End (docker and kind)
`test/run-nightly-suite.sh` stage 5 runs `test/e2e-ipv6.sh docker kind` when the host has a usable `/dev/kvm`, docker, kind and kubectl. `.grain/workflows/e2e-ipv6.yml` also runs it every night on its own. Read its `PASS`/`FAIL`/`SKIP` lines. Treat a `FAIL` on a check that passed the night before as a regression in netshim's IPv6 derivation or in the guest scripts (`granule-configure-ip6`, `granule-configure-routes`, `granule-configure-dns`). Don't treat it as an infrastructure flake unless the failing line is a cluster or network that never came up. Run GKE (`.grain/workflows/e2e-ipv6-gke.yml`) only if a human asks for it.

## Evaluation & Reporting Criteria
1. **Manifest Validity**: Confirm all Kubernetes manifests conform to standard Kubernetes v1 Pod schemas.
2. **Demo Rehearsal Status**: Confirm `demo.sh` dry-run executes all 7 chapters without syntax errors.
3. **Orchestration Compatibility**: Highlight any discrepancies between documented Kubernetes/Docker commands and CLI implementations.
4. **Action Items**: Note any missing example manifests or broken script steps.
