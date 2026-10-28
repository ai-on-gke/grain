#!/usr/bin/env bash
# scripts/platform-verify/k8s-granule.sh — prove `grain init -platform=k8s-granule` boots a
# guest on a real GKE cluster.
#
# This is the one platform claim docs/grain-init-platforms.md has not earned.
# Its preflight was proven against a real GKE cluster by grain task 27, which
# found three rough edges (all fixed in #2863) and then stopped at the third:
# the run had been given `granule:vendored`, the *runtime* image, which carries
# no guest disk, so the pod exited 1 and nothing ever booted. The preflight now
# names that mistake specifically. What has still never happened is a guest
# actually booting in a pod on a real cluster.
#
# Why this runs from the Mac and not from the sandbox host
# --------------------------------------------------------
# `grain init`'s two Kubernetes platforms build nothing locally: they apply a
# manifest, wait for a rollout and then run the same ProvisionOverAPI every
# platform runs, reaching the Service through a `kubectl port-forward` they
# manage themselves. So the only things the machine running this needs are
# kubectl, a grain binary and credentials for the cluster.
#
# The sandbox host has none of the third: its service account was created with
# default scopes, so `gcloud container clusters list` there answers 403 and
# `get-credentials` cannot work. Changing an instance's scopes means stopping
# it. The Mac already has gcloud auth, kubectl and gke-gcloud-auth-plugin, so
# it is both the easier and the more representative place to run this from --
# it is what an operator with a laptop would do.
#
# Why the guest image comes from ghcr and is not built here
# ---------------------------------------------------------
# `-k8s-granule-image` wants a *packed* guest: self-contained, its own guest
# disk and kernel baked in, and -- because a pack's generated final stage is
# `FROM` the granule image it was packed with -- its own granule and
# cloud-hypervisor too. That self-containment is why an older packed guest is
# fine here: the granule inside it and the guest contract it declares are one
# build, so they agree with each other whatever this repository's vendored
# granule has moved on to. (ghcr.io/bwsalmon/grain/guest:main declares
# `dev.granule.guest.contract=1` where the current vendored granule implements
# 3; that is not a mismatch, because nothing outside the image reads it.)
#
# It is public -- an anonymous ghcr token fetches its manifest -- so the
# cluster pulls it with no imagePullSecret, and we are spared pushing 8 GiB to
# Artifact Registry to learn the same thing.
#
# Usage:
#   ./scripts/platform-verify/k8s-granule.sh                 # create, prove, destroy
#   ./scripts/platform-verify/k8s-granule.sh --keep          # leave the cluster up
#   GUEST_IMAGE=... ./scripts/platform-verify/k8s-granule.sh
#
# The cluster is deleted on exit unless --keep, including on failure and on
# Ctrl-C: a forgotten 8-vCPU node pool is the expensive mistake here, and this
# script has one job that does not need the cluster to outlive it.

set -uo pipefail

CLUSTER="${CLUSTER:-grain-k8sg}"
ZONE="${ZONE:-us-east1-b}"
PROJECT="${PROJECT:-grain-host}"
# Nested virt needs N1/N2/N2D/C2, and it must be asked for explicitly on the
# node pool. UBUNTU_CONTAINERD because that is what granule's own gke.md uses
# and what task 27's run used; COS does not carry the kvm module the same way.
MACHINE_TYPE="${MACHINE_TYPE:-n2-standard-8}"
IMAGE_TYPE="${IMAGE_TYPE:-UBUNTU_CONTAINERD}"
NAMESPACE="${NAMESPACE:-grain}"
GUEST_IMAGE="${GUEST_IMAGE:-ghcr.io/bwsalmon/grain/guest:main}"
GRAIN="${GRAIN:-}"
UI_ADDR="${UI_ADDR:-127.0.0.1:8440}"
# The repository this verifies is the one this script lives in, so the
# root is two directories up rather than a sibling checkout. These ran
# from ~/Developer/grain-gcp before they moved here, where "../grain"
# was right; REPO= still overrides it for a checkout elsewhere.
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# For the Kubernetes platforms -data-dir is not the daemon's: there is no
# shared filesystem with a daemon that runs as a pod. It names a local
# directory for the admin-password file and the preflight's scratch
# manifests, so it belongs on this machine and is disposable.
DATA_DIR="${DATA_DIR:-$HOME/.local/state/grain-k8sg}"
TARGET_REPO="${TARGET_REPO:-bwsalmon/grain}"

KEEP=0
# Arguments are validated rather than sniffed. The first version of this
# tested only `[ "$1" = "--keep" ]`, so any other argument -- a typo, a
# `--help` this never had -- fell through as "not --keep" and the script
# went on to build a grain binary and create a cluster. That is the wrong
# response to an argument nobody meant, from a script whose next step
# spends money, so an unknown one stops it here.
usage() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}
case "${1:-}" in
  "")       ;;
  --keep)   KEEP=1 ;;
  -h|--help) usage 0 ;;
  *)        printf 'unknown argument: %s\n\n' "$1" >&2; usage 2 ;;
esac

say()  { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
ok()   { printf '    \033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { printf '    \033[31mFAIL\033[0m  %s\n' "$1"; }
info() { printf '    %s\n' "$1"; }

G=(gcloud --project "$PROJECT")

created=0
# shellcheck disable=SC2317 # reached through the trap below, not in sequence
cleanup() {
  local rc=$?
  if [ "$created" = "1" ] && [ "$KEEP" = "0" ]; then
    say "teardown"
    # Deleting the namespace first does NOT reclaim the PVC's disk, which is
    # what this script originally claimed: PersistentVolume deletion is
    # asynchronous, and the cluster (with the CSI controller that would do it)
    # goes away first. Measured -- namespace deleted, cluster deleted, and the
    # 20 GiB pd-balanced volume still READY afterwards. It is kept because it
    # is the orderly thing to do and costs nothing; the sweep below is what
    # actually reclaims the disk, so that is not optional.
    info "deleting namespace $NAMESPACE so its PVC releases its disk"
    kubectl delete namespace "$NAMESPACE" --timeout=120s 2>&1 | tail -2
    info "deleting cluster $CLUSTER (this takes a few minutes)"
    "${G[@]}" container clusters delete "$CLUSTER" --zone "$ZONE" --quiet 2>&1 | tail -3
    # Belt and braces: anything the namespace delete did not reclaim.
    orphans="$("${G[@]}" compute disks list --filter="zone:($ZONE) AND -users:*" \
      --format='value(name)' 2>/dev/null | grep '^pvc-' || true)"
    if [ -n "$orphans" ]; then
      info "deleting orphaned PVC disks:"; printf '      %s\n' "$orphans"
      # shellcheck disable=SC2086
      "${G[@]}" compute disks delete $orphans --zone "$ZONE" --quiet 2>&1 | tail -2
    fi
    info "remaining clusters:"
    "${G[@]}" container clusters list 2>&1 | tail -3
  elif [ "$created" = "1" ]; then
    say "kept"
    info "cluster $CLUSTER is still up -- delete it with:"
    info "  gcloud container clusters delete $CLUSTER --zone $ZONE --project $PROJECT --quiet"
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- preflight
say "preflight"
command -v gcloud  >/dev/null || { bad "gcloud is not installed";  exit 1; }
command -v kubectl >/dev/null || { bad "kubectl is not installed"; exit 1; }
command -v gke-gcloud-auth-plugin >/dev/null \
  || { bad "gke-gcloud-auth-plugin is not installed (gcloud components install gke-gcloud-auth-plugin)"; exit 1; }
ok "gcloud, kubectl and the GKE auth plugin are present"

if [ -z "$GRAIN" ]; then
  # Built rather than looked for: the k8s paths changed a lot recently, and a
  # stale grain on PATH would be testing something other than this checkout.
  say "building grain from $REPO"
  GRAIN="$(mktemp -d)/grain"
  ( cd "$REPO" && go build -o "$GRAIN" ./cmd/grain ) || { bad "go build ./cmd/grain failed"; exit 1; }
  ok "built $(cd "$REPO" && git rev-parse --short HEAD)"
fi
info "grain: $GRAIN"

# ------------------------------------------------- validate before spending
# `grain init -dry-run` prints the plan and changes nothing, so it answers
# "are these flags even accepted" for free. Worth doing before the cluster
# exists: the first run of this script lost a seven-minute cluster to a
# missing -repo-root, which the dry run would have caught in a second. It is
# expected to get as far as the kubectl check and refuse there -- that is the
# point at which flag validation is already behind us.
say "validating the init invocation (dry run, no cluster yet)"
mkdir -p "$DATA_DIR"
dry="$("$GRAIN" init -platform=k8s-granule -repo-root "$REPO" -data-dir "$DATA_DIR" \
  -k8s-namespace "$NAMESPACE" -k8s-granule-image "$GUEST_IMAGE" -ui-addr "$UI_ADDR" \
  -dry-run 2>&1)"
if printf '%s' "$dry" | grep -qE "is required|not a platform|flag provided but not defined"; then
  bad "grain init refused these flags before reaching the cluster:"
  printf '%s\n' "$dry" | head -5
  exit 1
fi
ok "flags accepted (the plan stops at the cluster check, as it should)"

# ------------------------------------------------------------------ cluster
say "cluster"
if "${G[@]}" container clusters describe "$CLUSTER" --zone "$ZONE" >/dev/null 2>&1; then
  info "cluster $CLUSTER already exists -- reusing it, and not deleting it on exit"
  KEEP=1
else
  info "creating $CLUSTER: 1 node, $MACHINE_TYPE, $IMAGE_TYPE, nested virt on"
  "${G[@]}" container clusters create "$CLUSTER" \
    --zone "$ZONE" \
    --num-nodes 1 \
    --machine-type "$MACHINE_TYPE" \
    --image-type "$IMAGE_TYPE" \
    --enable-nested-virtualization \
    --no-enable-autoupgrade \
    --quiet || { bad "cluster create failed"; exit 1; }
  created=1
  ok "cluster created"
fi

"${G[@]}" container clusters get-credentials "$CLUSTER" --zone "$ZONE" >/dev/null 2>&1 \
  || { bad "get-credentials failed"; exit 1; }
ok "kubeconfig points at $(kubectl config current-context)"

# ------------------------------------------------- /dev/kvm on the node
# The real test of this platform is a guest booting, which the preflight does
# for itself. This is the cheaper check that comes first, so a missing kvm
# module is not diagnosed as a grain problem: task 27's run confirmed
# "crw-rw---- root:994 10,232" on the node before anything else.
say "/dev/kvm on the node"
node="$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')"
info "node: $node"
# Informational only, and best-effort: grain init's own preflight does the
# real check by booting a VM. `kubectl run -i` races the kubelet's log
# endpoint on a fresh GKE node ("No agent available"), so this creates the pod,
# waits for it, then reads the log with a few retries rather than streaming it.
kubectl -n default delete pod kvmcheck --ignore-not-found >/dev/null 2>&1
kubectl -n default run kvmcheck --restart=Never --image=busybox:1.36 \
  --overrides='{"spec":{"containers":[{"name":"kvmcheck","image":"busybox:1.36","command":["ls","-l","/dev/kvm"],"securityContext":{"privileged":true},"volumeMounts":[{"name":"kvm","mountPath":"/dev/kvm"}]}],"volumes":[{"name":"kvm","hostPath":{"path":"/dev/kvm"}}]}}' \
  >/dev/null 2>&1
kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/kvmcheck --timeout=90s >/dev/null 2>&1
kvm=""
for _ in 1 2 3 4 5 6; do
  kvm="$(kubectl -n default logs kvmcheck 2>/dev/null | tr -d '\r')"
  [ -n "$kvm" ] && break
  sleep 5
done
if [ -n "$kvm" ]; then ok "/dev/kvm in a privileged pod: $kvm"; else info "could not read it back (informational only)"; fi
kubectl -n default delete pod kvmcheck --ignore-not-found >/dev/null 2>&1

# ---------------------------------------------------------------- namespace
# grain init never creates or labels this namespace itself, by design: the
# label it needs is `enforce=privileged`, which is precisely the restriction
# k8s-container's preflight exists to protect, so opening it is the operator's
# act and not a side effect of running a command. See
# docs/k8s-granule-design.md §1 and task 27's finding 1.
say "namespace $NAMESPACE, labelled privileged"
kubectl create namespace "$NAMESPACE" 2>&1 | tail -1
kubectl label namespace "$NAMESPACE" \
  pod-security.kubernetes.io/enforce=privileged --overwrite 2>&1 | tail -1
ok "namespace ready"

# --------------------------------------------------------------- grain init
# Run it twice, cold then warm, because the first pass on a fresh node
# cannot pass and the reason is not what it reports.
#
# The nested-virt check waits DefaultK8sGranulePreflightTimeout (30s) for a
# throwaway pod to reach Ready. k8s_granule.go derives that figure from
# gke.md's validated 2.3s boot and says it leaves "room for a cold image pull
# to finish before the boot itself is even timed". For a packed guest that is
# not true: guest:main is 1.73 GiB compressed, and 30s into the first attempt
# the pod is still Pending/ContainerCreating with a "Pulling image" event and
# no log output -- while the advice printed blames nested virtualization, the
# node pool flag and admission policy, none of which is the problem.
#
# So the first pass is what pulls the image, and the second is the one that
# actually times a boot. That is also exactly what the tool tells the operator
# to do ("Run these, then re-run this command"), which is why this needs no
# code change to demonstrate -- though the timeout wanting to start after the
# container starts, rather than including the pull, is the real fix.
run_init() {
  "$GRAIN" init \
    -platform=k8s-granule \
    -repo-root "$REPO" \
    -data-dir "$DATA_DIR" \
    -k8s-namespace "$NAMESPACE" \
    -k8s-granule-image "$GUEST_IMAGE" \
    -k8s-granule-manifest "$rendered" \
    -ui-addr "$UI_ADDR" \
    2>&1 | tail -45
  return "${PIPESTATUS[0]}"
}

# grain init applies grain-granule.yaml as it finds it: K8sGranule.Image is
# used only for the preflight's own throwaway pod, and nothing substitutes
# the manifest's three CHANGEME placeholders. They are there to be edited by
# an operator before the apply (the file says "CHANGEME (1 of 3)" and so on),
# so that editing is what this does -- into a copy, passed with
# -k8s-granule-manifest, leaving the checked-in file alone.
say "rendering the manifest"
src="$REPO/deploy/kubernetes/grain-granule.yaml"
rendered="$DATA_DIR/grain-granule.rendered.yaml"
sed -e "s|-k8s-granule-image=CHANGEME|-k8s-granule-image=$GUEST_IMAGE|" \
    -e "s|-default-target-repo=CHANGEME/CHANGEME|-default-target-repo=$TARGET_REPO|" \
    "$src" > "$rendered"
if grep -n "CHANGEME" "$rendered" | grep -vE "^\s*[0-9]+:\s*#" | grep -q .; then
  bad "the rendered manifest still has an uncommented CHANGEME:"
  grep -n "CHANGEME" "$rendered" | grep -vE ":\s*#" | head -5
  exit 1
fi
ok "rendered to $rendered"
# The flag this PR fixed: a Deployment passing -upgrade-image with
# -k8s-granule-sandboxes exits on startup, so say plainly which way the
# checkout under test has it rather than discovering it in CrashLoopBackOff.
if grep -qE "^\s+- -upgrade-image" "$rendered"; then
  bad "this manifest still passes -upgrade-image -- the daemon will refuse to start (see grain#2908)"
else
  ok "-upgrade-image is not passed (grain#2908)"
fi

say "grain init, pass 1 (cold: this is the pull)"
info "guest image: $GUEST_IMAGE"
run_init
rc=$?
say "pass 1 exited $rc"

if [ "$rc" != "0" ]; then
  # Confirm the image really is on the node now, so pass 2 is testing a boot
  # and not a second pull.
  say "is the image on the node now?"
  kubectl -n "$NAMESPACE" delete pod grain-init-preflight-granule --ignore-not-found >/dev/null 2>&1
  node="$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')"
  kubectl get node "$node" -o jsonpath='{range .status.images[*]}{.names[0]}{"\n"}{end}' 2>/dev/null \
    | grep -i "grain/guest" | head -3 || info "not listed among the node's images"

  say "grain init, pass 2 (warm: this one times the boot)"
  run_init
  rc=$?
  say "pass 2 exited $rc"
fi

# Exit code alone has been misleading here before, so say what is actually
# running rather than trusting it.
say "what is actually in the namespace"
kubectl -n "$NAMESPACE" get pods,svc,deploy 2>&1 | tail -15
say "the deployment's own log, last lines"
kubectl -n "$NAMESPACE" logs deploy/grain --tail=25 2>&1 | tail -25

exit $rc
