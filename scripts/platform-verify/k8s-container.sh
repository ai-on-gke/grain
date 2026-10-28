#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# scripts/platform-verify/k8s-container.sh — take `grain init -platform=k8s-container` to exit 0
# on a real GKE cluster. This is grain task 35.
#
# Why this is an operator run and not a task
# ------------------------------------------
# It needs a cluster, and the credentials to create one are not in a task's
# sandbox: the controller host's own service account answers 403 to
# `gcloud container clusters list`. Task 35 is deferred with that said on it.
#
# What this platform has and has not had
# --------------------------------------
# grain task 27 ran its whole preflight against real GKE and every check did
# real work -- a real ghcr pull, a real ReadWriteOnce PVC bound and mounted --
# and came out clean. What it never had is an exit 0: "the deployment is
# actually up and provisioned". And task 27 predates the sandbox-budget
# refusal (#2891), adopt mode and its fix (tasks 24/29/32), the non-root
# daemon account (task 28) and the resync (#2888). docs/grain-init-platforms.md
# now names it as the one row in that state.
#
# How it differs from scripts/platform-verify/k8s-granule.sh
# -----------------------------------------
#   - No nested virtualization, and no reason to pay for a machine that has
#     it: sandboxes here are plain pods sharing the node's kernel. e2 is
#     fine, which the granule row cannot use at all.
#   - `enforce=baseline`, not `privileged` -- and this manifest ships its own
#     Namespace carrying that label (grain.yaml's line 51), unlike
#     grain-granule.yaml which ships none. So nothing is created here by
#     hand; the apply does it, and the preflight skips the check while the
#     namespace does not exist yet.
#   - Only one CHANGEME actually needs rendering. The grain image and
#     -k8s-sandbox-image already carry real values in the file, and the
#     cosign.pub ConfigMap is deliberately left a placeholder: the file's own
#     comment says that means "every upgrade is refused at its signature
#     step -- the daemon itself starts and runs whatever image it was applied
#     with", which is exactly what this run wants.
#   - `-k8s-sandbox-image` is required by Options.validate for this platform,
#     where the granule row's image flag is not.
#
# Usage:
#   ./scripts/platform-verify/k8s-container.sh              # create, prove, destroy
#   ./scripts/platform-verify/k8s-container.sh --keep       # leave the cluster up
#
# Teardown runs on exit, failure and Ctrl-C. It sweeps orphaned `pvc-*` disks
# after deleting the cluster, because deleting the cluster does not reclaim
# them and neither does deleting the namespace first: PersistentVolume
# deletion is asynchronous and the CSI controller that would do it goes away
# with the cluster. Measured here -- namespace deleted, cluster deleted, and
# the 20 GiB volume still READY until the sweep removed it. Every GKE run
# before this one left one behind, unattached and absent from `clusters
# list`, billing quietly.

set -uo pipefail

CLUSTER="${CLUSTER:-grain-k8sc}"
ZONE="${ZONE:-us-east1-b}"
PROJECT="${PROJECT:-grain-host}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-standard-4}"
NAMESPACE="${NAMESPACE:-grain}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-ghcr.io/bwsalmon/grain/k8s-sandbox:main}"
TARGET_REPO="${TARGET_REPO:-bwsalmon/grain}"
# The repository this verifies is the one this script lives in, so the
# root is two directories up rather than a sibling checkout. These ran
# from ~/Developer/grain-gcp before they moved here, where "../grain"
# was right; REPO= still overrides it for a checkout elsewhere.
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
REF="${REF:-origin/main}"
DATA_DIR="${DATA_DIR:-$HOME/.local/state/grain-k8sc}"
UI_ADDR="${UI_ADDR:-127.0.0.1:8450}"
GRAIN="${GRAIN:-}"

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
    info "deleting cluster $CLUSTER"
    "${G[@]}" container clusters delete "$CLUSTER" --zone "$ZONE" --quiet 2>&1 | tail -2
    orphans="$("${G[@]}" compute disks list --filter="zone:($ZONE) AND -users:*" \
      --format='value(name)' 2>/dev/null | grep '^pvc-' || true)"
    if [ -n "$orphans" ]; then
      info "deleting orphaned PVC disks:"; printf '      %s\n' "$orphans"
      # shellcheck disable=SC2086
      "${G[@]}" compute disks delete $orphans --zone "$ZONE" --quiet 2>&1 | tail -2
    fi
    info "remaining clusters:"; "${G[@]}" container clusters list 2>&1 | tail -2
  elif [ "$created" = "1" ]; then
    say "kept"
    info "delete with: gcloud container clusters delete $CLUSTER --zone $ZONE --project $PROJECT --quiet"
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

say "preflight"
for c in gcloud kubectl gke-gcloud-auth-plugin; do
  command -v "$c" >/dev/null || { bad "$c is not installed"; exit 1; }
done
ok "gcloud, kubectl and the GKE auth plugin are present"

if [ -z "$GRAIN" ]; then
  say "building grain from $REF"
  work="$(mktemp -d)"
  ( cd "$REPO" && git archive --format=tar.gz --prefix=grain/ -o "$work/repo.tar.gz" "$REF" ) \
    || { bad "git archive $REF failed"; exit 1; }
  mkdir -p "$work/src" && tar -C "$work/src" -xzf "$work/repo.tar.gz"
  SRC="$work/src/grain"
  go build -C "$SRC" -o "$work/grain" ./cmd/grain || { bad "go build failed"; exit 1; }
  GRAIN="$work/grain"
  ok "built $(cd "$REPO" && git rev-parse --short "$REF") from a clean archive of $REF"
else
  SRC="$REPO"
fi
info "grain: $GRAIN"
info "repo-root: $SRC"

# ------------------------------------------------------------ render manifest
say "rendering grain.yaml"
mkdir -p "$DATA_DIR"
src="$SRC/deploy/kubernetes/grain.yaml"
rendered="$DATA_DIR/grain.rendered.yaml"
sed -e "s|-default-target-repo=CHANGEME/CHANGEME|-default-target-repo=$TARGET_REPO|" \
    "$src" > "$rendered"
# The cosign.pub placeholder stays: grain.yaml says leaving it means every
# upgrade is refused at its signature step while the daemon itself starts
# normally. Anything else uncommented would be a real gap.
left="$(grep -n 'CHANGEME' "$rendered" | grep -vE ':\s*#' | grep -v 'cosign.pub\|CHANGEME: replace this whole value' || true)"
if [ -n "$left" ]; then
  bad "an uncommented CHANGEME survived rendering:"; printf '%s\n' "$left" | head -5; exit 1
fi
ok "rendered to $rendered (cosign.pub left a placeholder, deliberately)"

# ------------------------------------------------- validate before spending
say "validating the init invocation (dry run, no cluster yet)"
dry="$("$GRAIN" init -platform=k8s-container -repo-root "$SRC" -data-dir "$DATA_DIR" \
  -k8s-namespace "$NAMESPACE" -k8s-sandbox-image "$SANDBOX_IMAGE" \
  -k8s-manifest "$rendered" -ui-addr "$UI_ADDR" -dry-run 2>&1)"
if printf '%s' "$dry" | grep -qE "is required|not a platform|flag provided but not defined"; then
  bad "grain init refused these flags before reaching the cluster:"
  printf '%s\n' "$dry" | head -6; exit 1
fi
ok "flags accepted"

# ------------------------------------------------------------------ cluster
say "cluster"
if "${G[@]}" container clusters describe "$CLUSTER" --zone "$ZONE" >/dev/null 2>&1; then
  info "$CLUSTER already exists -- reusing it, and not deleting it on exit"
  KEEP=1
else
  info "creating $CLUSTER: 1 node, $MACHINE_TYPE (no nested virt -- plain pods)"
  "${G[@]}" container clusters create "$CLUSTER" --zone "$ZONE" --num-nodes 1 \
    --machine-type "$MACHINE_TYPE" --no-enable-autoupgrade --quiet 2>&1 | tail -2 \
    || { bad "cluster create failed"; exit 1; }
  created=1
  ok "cluster created"
fi
"${G[@]}" container clusters get-credentials "$CLUSTER" --zone "$ZONE" >/dev/null 2>&1 \
  || { bad "get-credentials failed"; exit 1; }
ok "kubeconfig points at $(kubectl config current-context)"

# --------------------------------------------------------------- grain init
# Two passes, for the reason phase2 needed them: the sandbox-image pull check
# and the manifest's own rollout both race a cold registry on a fresh node.
run_init() {
  "$GRAIN" init -platform=k8s-container \
    -repo-root "$SRC" -data-dir "$DATA_DIR" \
    -k8s-namespace "$NAMESPACE" -k8s-sandbox-image "$SANDBOX_IMAGE" \
    -k8s-manifest "$rendered" -ui-addr "$UI_ADDR" > "$DATA_DIR/init.log" 2>&1
  local rc=$?
  tail -40 "$DATA_DIR/init.log"
  return $rc
}

say "grain init -platform=k8s-container, pass 1"
run_init; rc=$?
say "pass 1 exited $rc"
if [ "$rc" != "0" ]; then
  say "grain init, pass 2 (warm)"
  run_init; rc=$?
  say "pass 2 exited $rc"
fi
case "$rc" in
  0)  ok "0 = the deployment is up and provisioned" ;;
  10) info "10 = ran to completion with manual or refused steps left" ;;
  *)  bad "$rc = a genuine failure" ;;
esac

say "what is actually in the namespace"
kubectl -n "$NAMESPACE" get pods,svc,deploy,pvc 2>&1 | tail -16
say "namespace pod-security label (must be baseline for this platform)"
kubectl get ns "$NAMESPACE" -o jsonpath='{.metadata.labels}' 2>&1; echo
say "the deployment's own log, last lines"
kubectl -n "$NAMESPACE" logs deploy/grain --tail=20 2>&1 | tail -20

exit $rc
