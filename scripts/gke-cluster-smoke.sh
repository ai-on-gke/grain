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

# gke-cluster-smoke.sh -- prove that a GCP credential can actually run the
# GKE cluster lifecycle: create a throwaway cluster, change it while it is
# up, run a workload on it, delete it again.
#
# The sibling of scripts/gce-vm-smoke.sh, and the same kind of check: "is
# this credential good enough to stand something up?", answered on its own
# before blaming a deployment or an agent. GKE is worth its own script
# because a cluster fails differently from a VM -- the create takes minutes
# rather than seconds, the control plane and the nodes are separately
# capable of being wrong, and reaching it needs a kubectl and an auth plugin
# that are not installed in a grain sandbox by default (see below).
#
# Usage:
#   scripts/gke-cluster-smoke.sh [--zone Z] [--machine-type M] [--disk-size S]
#                                [--master-ipv4-cidr CIDR] [--network N] [--subnetwork S]
#                                [--node-service-account SA|default]
#                                [--enable-private-nodes|--no-enable-private-nodes] [--keep]
#
# Budget about 12 minutes: the create is ~6, the delete ~4, and everything
# in between is seconds. Nothing here hangs silently -- each step announces
# itself -- but the first run reads as a hang if you were expecting the GCE
# script's ninety seconds.
#
# It authenticates as whatever gcloud already has active. From inside a
# grain sandbox holding the gcp-key capability, that means first:
#
#   gcloud auth activate-service-account --key-file=~/.gcp-service-account.json
#   gcloud config set project <project>
#
# Activating a key does not select a project, and gcloud's complaint when
# you skip the second line ("The required property [project] is not
# currently set") says nothing about the key -- so it gets misread as the
# key being bad.
#
# kubectl and the GKE auth plugin are a prerequisite this script checks for
# rather than installs, since installing needs root. On a grain sandbox:
#
#   sudo apt-get update && sudo apt-get install -y kubectl \
#       google-cloud-cli-gke-gcloud-auth-plugin
#
# Both come from the cloud-sdk apt repository the image already has
# configured; the update is not optional, because the guest ships with no
# package lists and `apt-get install kubectl` without it fails with
# "Package 'kubectl' has no installation candidate", which reads like the
# repository is missing rather than unread.

set -euo pipefail

ZONE="${ZONE:-us-central1-a}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-medium}"
DISK_SIZE="${DISK_SIZE:-50}"
NODE_SA="${NODE_SA:-}"
# Empty means "choose a free /28 at create time" -- see the create step for
# why there is no fixed default. Set it (or pass --master-ipv4-cidr) to pin
# one, in which case it is used as given and never retried.
MASTER_IPV4_CIDR="${MASTER_IPV4_CIDR:-}"
NETWORK="${NETWORK:-}"
SUBNET="${SUBNET:-}"
ENABLE_PRIVATE_NODES=1
KEEP=0

while [[ $# -gt 0 ]]; do
  # Accept --flag=value as well as --flag value, as gce-vm-smoke.sh does and
  # as gcloud's own flags do.
  case "$1" in
    --*=*) set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
  esac
  case "$1" in
    --zone)         ZONE="$2"; shift 2 ;;
    --machine-type) MACHINE_TYPE="$2"; shift 2 ;;
    --disk-size)    DISK_SIZE="$2"; shift 2 ;;
    --master-ipv4-cidr) MASTER_IPV4_CIDR="$2"; shift 2 ;;
    --network)      NETWORK="$2"; shift 2 ;;
    --subnetwork|--subnet) SUBNET="$2"; shift 2 ;;
    --enable-private-nodes) ENABLE_PRIVATE_NODES=1; shift ;;
    --no-enable-private-nodes) ENABLE_PRIVATE_NODES=0; shift ;;
    # Which service account the *nodes* run as. Default (empty) means "the
    # account gcloud is authenticated as", which is what makes this work as
    # a non-owner -- see the create step. Pass `default` to let GKE attach
    # the project's default compute service account instead, the normal
    # production shape, which needs roles/iam.serviceAccountUser on that
    # account.
    --node-service-account) NODE_SA="$2"; shift 2 ;;
    # Leave the cluster running -- for poking at a failure by hand. It is
    # then yours to delete; the command is printed at the end.
    --keep)         KEEP=1; shift ;;
    *) echo "usage: $0 [--zone Z] [--machine-type M] [--disk-size S] [--master-ipv4-cidr CIDR] [--network N] [--subnetwork S] [--node-service-account SA|default] [--enable-private-nodes|--no-enable-private-nodes] [--keep]" >&2; exit 2 ;;
  esac
done

for tool in gcloud kubectl gke-gcloud-auth-plugin; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "$tool is not installed -- see the header of this script" >&2
    exit 2
  }
done

PROJECT="$(gcloud config get-value project 2>/dev/null)"
if [[ -z "$PROJECT" || "$PROJECT" == "(unset)" ]]; then
  echo "no project set: gcloud config set project <project>" >&2
  exit 2
fi

ACCOUNT="$(gcloud config get-value account 2>/dev/null)"
if [[ -z "$NODE_SA" ]]; then
  NODE_SA="$ACCOUNT"
fi

CLUSTER="gke-smoke-$(date +%s)-$$"

step() { printf '\n=== %s ===\n' "$*"; }

# A leaked cluster is a much larger bill than a leaked VM -- a control plane
# plus every node -- so every path out of this script that is not --keep
# goes through here, set -e's and a Ctrl-C during the several minutes of
# waiting included. The kubeconfig goes too: it is a throwaway file (below)
# and leaving it behind would leave a stale context named after a cluster
# that no longer exists.
# get-credentials writes into $KUBECONFIG, so pointing it at a temporary file
# is what keeps this script from editing the caller's own ~/.kube/config --
# it otherwise adds a context and *switches to it*, silently redirecting any
# kubectl the caller runs afterwards at a cluster this script has deleted.
# A directory rather than a mktemp'd file, because the file must not exist
# yet: gcloud reads it first, calls an empty one corrupt ("Unable to load
# default kubeconfig: Empty file"), and copies it aside as a .backup that
# then outlives the run.
KUBECONFIG_DIR="$(mktemp -d -t gke-smoke.XXXXXX)"
export KUBECONFIG="$KUBECONFIG_DIR/kubeconfig"
cleanup() {
  local rc=$?
  rm -rf "$KUBECONFIG_DIR"
  if [[ $KEEP -eq 1 ]]; then
    echo
    echo "--keep: cluster $CLUSTER left running in $ZONE. Delete it with:"
    echo "  gcloud container clusters delete $CLUSTER --zone=$ZONE --quiet"
    return $rc
  fi
  if gcloud container clusters describe "$CLUSTER" --zone="$ZONE" >/dev/null 2>&1; then
    step "delete $CLUSTER (cleanup)"
    gcloud container clusters delete "$CLUSTER" --zone="$ZONE" --quiet || true
  fi
  return $rc
}
trap cleanup EXIT

step "create cluster $CLUSTER ($MACHINE_TYPE x1, $ZONE$([[ $ENABLE_PRIVATE_NODES -eq 1 ]] && echo ', private nodes')) -- takes ~6 minutes"
# --service-account is not a hardening choice, it is what makes this work as
# a non-owner, and it is the same trap gce-vm-smoke.sh documents for VMs.
# GKE otherwise runs the nodes as the project's default compute service
# account, and attaching *any* service account requires
# roles/iam.serviceAccountUser **on that account** -- a permission on the SA,
# which no project-level container role includes. Without it the create fails
# with
#
#   The user does not have access to service account
#   '<num>-compute@developer.gserviceaccount.com'
#
# which names an account the caller never asked for and reads like the
# credential itself is wrong. Naming the calling account sidesteps the grant,
# because a service account can always act as itself. `node-pools create`
# fails exactly the same way and takes the same flag.
#
# --enable-private-nodes and --master-ipv4-cidr satisfy GCP organization policies
# enforcing private clusters (such as constraints/container.managed.enablePrivateNodes).
# Nodes receive only private RFC 1918 IP addresses while the control plane's
# public endpoint remains enabled by default, allowing kubectl from outside the
# VPC (such as a CI runner) to interact with the API server. Outbound access for
# pulling container images from public registries relies on Cloud NAT or Private
# Google Access.
#
# --no-enable-master-authorized-networks is what makes "allowing kubectl from
# outside the VPC" true: for a private cluster gcloud otherwise turns
# authorized networks on with an empty list, so the public endpoint drops
# every connection and the first kubectl times out (`dial tcp <ip>:443: i/o
# timeout`) right after a create that reported success. The cluster lives for
# minutes and every request still needs the caller's credentials.
#
# The --master-ipv4-cidr range is chosen at run time rather than defaulted,
# because a private cluster's control-plane /28 must not overlap any subnet
# in the network -- including the PSC subnet every *other* private cluster
# gets for its own control plane. A fixed default used to be 172.16.0.0/28,
# which is the first range everybody reaches for: the long-lived bouncer
# cluster in the shared project already owns it, so every create failed with
#
#   Conflicting IP cidr range: Invalid IPCidrRange: 172.16.0.0/28 conflicts
#   with existing subnetwork 'gke-...-pe-subnet'
#
# and a fixed range would collide the same way with an overlapping smoke run
# (a manual dispatch beside the nightly one). So pick_master_cidr picks a
# random /28 out of 172.31.0.0/16 -- an unlikely block -- skipping every
# control-plane range and subnet range (primary and secondary) the project
# already lists, and the create retries with a fresh pick if it still loses
# a race and gets the conflict back. A range given explicitly is used as is.
#
# The nodes are then short the roles a production node pool wants
# (roles/container.defaultNodeServiceAccount -- log writing, metric writing,
# Artifact Registry pulls). That does not stop a node registering or a pod
# from a public registry running, which is all this script asserts; it is not
# the shape to copy into a real deployment.
# A plain `if`: the `[[ ... ]] && args+=(...)` idiom the sibling script uses
# is safe here too (bash's `set -e` does not fire on the non-final command of
# an AND list), but it reads like it is one edit away from not being.
CREATE_ARGS=()
if [[ "$NODE_SA" != "default" ]]; then
  CREATE_ARGS+=(--service-account="$NODE_SA")
fi
if [[ -n "$NETWORK" ]]; then
  CREATE_ARGS+=(--network="$NETWORK")
fi
if [[ -n "$SUBNET" ]]; then
  CREATE_ARGS+=(--subnetwork="$SUBNET")
fi

ip_to_int() {
  local a b c d
  IFS=. read -r a b c d <<<"$1"
  echo $(( (a << 24) | (b << 16) | (c << 8) | d ))
}

# Every IPv4 range the project already uses that a control plane must not
# overlap: other clusters' control planes, and every subnet's primary and
# secondary ranges (the PSC subnets of other private clusters among them).
# Project-wide rather than per network, which only errs towards skipping.
used_cidrs() {
  {
    gcloud container clusters list \
      --format='value(privateClusterConfig.masterIpv4CidrBlock)' 2>/dev/null || true
    gcloud compute networks subnets list \
      --format='value(ipCidrRange,secondaryIpRanges[].ipCidrRange)' 2>/dev/null || true
  } | tr -s ';,\t ' '\n' | grep -E '^[0-9]+(\.[0-9]+){3}/[0-9]+$' || true
}

# A random /28 in 172.31.0.0/16 overlapping nothing in used_cidrs or in
# $TRIED_CIDRS (ranges an earlier attempt of this run already lost on).
pick_master_cidr() {
  local used cidr start end net bits size base i
  used="$(used_cidrs; printf '%s\n' "${TRIED_CIDRS[@]}")"
  for (( i = 0; i < 64; i++ )); do
    base=$(( $(ip_to_int 172.31.0.0) + ((RANDOM * 32768 + RANDOM) % 4096) * 16 ))
    start=$base end=$(( base + 15 ))
    local clash=0
    while read -r cidr; do
      [[ -n "$cidr" ]] || continue
      net="$(ip_to_int "${cidr%/*}")" bits="${cidr#*/}"
      size=$(( 1 << (32 - bits) ))
      net=$(( net - net % size ))
      if (( start < net + size && net <= end )); then clash=1; break; fi
    done <<<"$used"
    if (( !clash )); then
      echo "172.31.$(( (base >> 8) & 255 )).$(( base & 255 ))/28"
      return 0
    fi
  done
  echo "no free /28 found in 172.31.0.0/16 after 64 tries" >&2
  return 1
}

PICK_CIDR=0
if [[ $ENABLE_PRIVATE_NODES -eq 1 && -z "$MASTER_IPV4_CIDR" ]]; then
  PICK_CIDR=1
fi
TRIED_CIDRS=()
CREATE_LOG="$KUBECONFIG_DIR/create.log"
for attempt in 1 2 3; do
  ATTEMPT_ARGS=("${CREATE_ARGS[@]}")
  if [[ $ENABLE_PRIVATE_NODES -eq 1 ]]; then
    if [[ $PICK_CIDR -eq 1 ]]; then
      MASTER_IPV4_CIDR="$(pick_master_cidr)"
      TRIED_CIDRS+=("$MASTER_IPV4_CIDR")
    fi
    echo "control-plane range: $MASTER_IPV4_CIDR (attempt $attempt)"
    ATTEMPT_ARGS+=(--enable-private-nodes --enable-ip-alias --master-ipv4-cidr="$MASTER_IPV4_CIDR"
      --no-enable-master-authorized-networks)
  fi
  if gcloud container clusters create "$CLUSTER" \
      --zone="$ZONE" \
      --num-nodes=1 \
      --machine-type="$MACHINE_TYPE" \
      --disk-size="$DISK_SIZE" \
      --disk-type=pd-balanced \
      --no-enable-autoupgrade \
      "${ATTEMPT_ARGS[@]}" \
      --labels=purpose=gke-cluster-smoke 2>&1 | tee "$CREATE_LOG"; then
    break
  fi
  # Only a range we chose ourselves is worth choosing again; anything else
  # (quota, policy, a range the caller pinned) fails here as it always did.
  if [[ $PICK_CIDR -eq 0 || $attempt -eq 3 ]] || ! grep -q 'Conflicting IP cidr range' "$CREATE_LOG"; then
    exit 1
  fi
  # The failed create leaves the cluster behind in ERROR, holding the name.
  if gcloud container clusters describe "$CLUSTER" --zone="$ZONE" >/dev/null 2>&1; then
    step "delete $CLUSTER after the range conflict, then retry"
    gcloud container clusters delete "$CLUSTER" --zone="$ZONE" --quiet
  fi
done

step "get credentials and talk to the control plane"
# This is the leg that needs gke-gcloud-auth-plugin: kubeconfig entries GKE
# writes call it as an exec credential provider, and without it every kubectl
# fails with "no Auth Provider found for name gcp" rather than with anything
# naming the missing binary.
gcloud container clusters get-credentials "$CLUSTER" --zone="$ZONE"
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl get nodes -o wide

step "run a workload"
kubectl create deployment hello --image=nginx:alpine --replicas=1
kubectl rollout status deployment/hello --timeout=180s

step "resize the node pool 1 -> 2"
gcloud container clusters resize "$CLUSTER" --zone="$ZONE" \
  --node-pool=default-pool --num-nodes=2 --quiet
kubectl wait --for=condition=Ready nodes --all --timeout=300s

step "spread the workload over both nodes"
kubectl scale deployment/hello --replicas=4
kubectl rollout status deployment/hello --timeout=180s
# The resize is only proved by something actually landing on the new node.
# A cluster whose second node registers but never schedules -- taints, an
# unreachable registry, a node that joins NotReady -- passes every gcloud
# check above and fails here, which is the whole reason a workload is part
# of this script rather than just `clusters describe`.
nodes_used="$(kubectl get pods -l app=hello -o jsonpath='{.items[*].spec.nodeName}' | tr ' ' '\n' | sort -u | grep -c .)"
if [[ "$nodes_used" -lt 2 ]]; then
  echo "4 replicas landed on $nodes_used node(s); expected both" >&2
  exit 1
fi
echo "pods are running on $nodes_used nodes"

step "update the cluster's labels"
gcloud container clusters update "$CLUSTER" --zone="$ZONE" \
  --update-labels=purpose=gke-cluster-smoke,phase=manipulated --quiet
gcloud container clusters describe "$CLUSTER" --zone="$ZONE" \
  --format='value(resourceLabels)' | grep -q 'phase=manipulated' || {
  echo "label update did not take" >&2
  exit 1
}

step "enable autoscaling on the node pool"
gcloud container node-pools update default-pool --cluster="$CLUSTER" \
  --zone="$ZONE" --enable-autoscaling --min-nodes=1 --max-nodes=3 --quiet
autoscaling="$(gcloud container node-pools describe default-pool \
  --cluster="$CLUSTER" --zone="$ZONE" --format='value(autoscaling.enabled)')"
if [[ "$autoscaling" != "True" ]]; then
  echo "autoscaling reads back as '$autoscaling', not True" >&2
  exit 1
fi

# Deletion is the third verb under test, so run it here where a failure is
# reported as one, rather than leaving it to the trap's best-effort cleanup.
if [[ $KEEP -eq 0 ]]; then
  step "delete $CLUSTER -- takes ~4 minutes"
  gcloud container clusters delete "$CLUSTER" --zone="$ZONE" --quiet
  # A cluster that is still STOPPING answers `describe` perfectly happily,
  # so the delete is only confirmed once the name is gone entirely.
  if gcloud container clusters describe "$CLUSTER" --zone="$ZONE" >/dev/null 2>&1; then
    echo "cluster $CLUSTER outlived its delete -- check it by hand" >&2
    exit 1
  fi
fi

# Say only what actually ran: under --keep the delete is skipped, and
# claiming it passed would make the one flag that leaves a cluster behind the
# one that reports the cleanest.
printf '\nOK: create, workload, resize, update%s as %s in %s.\n' \
  "$([[ $KEEP -eq 1 ]] && echo '; delete skipped (--keep)' || echo ' and delete all succeeded')" \
  "$ACCOUNT" "$PROJECT"
