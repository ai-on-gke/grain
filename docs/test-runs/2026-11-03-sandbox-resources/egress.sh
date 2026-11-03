#!/bin/bash
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

# docs/test-plans/2026-11-03.md §8 item 2: deploy/kubernetes/networkpolicy.yaml
# on a real kind + Calico cluster, the way the `real kubernetes` job builds
# one (scripts/prepare-real-k8s-ci.sh must have run first).
#
# Three probe pods run the same checks:
#   open    -- labelled as K8sSandboxes labels an open-network sandbox pod
#   none    -- the same plus grain.dev/network=none
#   control -- no grain labels at all, so every negative result in `open`
#              is shown to be the policy's doing and not the cluster's
# A fake metadata server is bound to 169.254.169.254 on the kind node so
# that address answers when nothing blocks it.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
CLUSTER="${CLUSTER:-grain-egress}"
NS=grain
IMG=buildpack-deps:bookworm-curl
WEB=nginx:1.27-alpine
k() { kubectl --context "kind-$CLUSTER" "$@"; }

if [ -z "${KEEP:-}" ]; then
  trap 'kind delete cluster --name "$CLUSTER" >/dev/null 2>&1' EXIT
fi

kind create cluster --name "$CLUSTER" --config - <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
EOF
kind load image-archive "${GRAIN_K8S_IMAGE_ARCHIVE:-/var/tmp/k8s-workload-images.tar}" --name "$CLUSTER"
sed 's/cpu: 250m/cpu: 50m/' "${GRAIN_CALICO_MANIFEST:-/var/tmp/calico.yaml}" | k apply -f - >/dev/null
k rollout status daemonset/calico-node -n kube-system --timeout=5m

k apply -f "$ROOT/deploy/kubernetes/grain.yaml" >/dev/null
k scale deployment/grain -n "$NS" --replicas=0
k delete pod -n "$NS" -l app.kubernetes.io/name=grain --force --grace-period=0 --ignore-not-found >/dev/null 2>&1
k apply -f "$ROOT/deploy/kubernetes/networkpolicy.yaml"

# Fake metadata server: the address on the node's loopback, nginx in the
# node's network namespace answering on :80.
node="$CLUSTER-control-plane"
docker exec "$node" ip addr add 169.254.169.254/32 dev lo
k run metadata -n kube-system --image="$WEB" --image-pull-policy=IfNotPresent \
  --overrides='{"spec":{"hostNetwork":true,"tolerations":[{"operator":"Exists"}]}}' >/dev/null
# grain's own pod (what the policy's first rule allows), an unrelated
# pod and a ClusterIP Service in front of it.
k run grain-proxy -n "$NS" --image="$WEB" --image-pull-policy=IfNotPresent --labels=app.kubernetes.io/name=grain >/dev/null
k run victim -n "$NS" --image="$WEB" --image-pull-policy=IfNotPresent --labels=app=victim --expose --port=80 >/dev/null
probe_pod() {
  local name=$1 labels=$2
  k run "$name" -n "$NS" --image="$IMG" --image-pull-policy=IfNotPresent ${labels:+--labels="$labels"} \
    --command -- sleep infinity >/dev/null
}
probe_pod open app.kubernetes.io/managed-by=grain,app.kubernetes.io/component=sandbox
probe_pod none app.kubernetes.io/managed-by=grain,app.kubernetes.io/component=sandbox,grain.dev/network=none
probe_pod control ""
k wait -n kube-system pod/metadata --for=condition=Ready --timeout=3m >/dev/null
k wait -n "$NS" pod --all --for=condition=Ready --timeout=3m >/dev/null

grain_ip="$(k get pod grain-proxy -n "$NS" -o jsonpath='{.status.podIP}')"
victim_ip="$(k get pod victim -n "$NS" -o jsonpath='{.status.podIP}')"
svc_ip="$(k get svc victim -n "$NS" -o jsonpath='{.spec.clusterIP}')"
dns_ip="$(k get svc kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}')"
echo "grain pod $grain_ip, victim pod $victim_ip, victim ClusterIP $svc_ip, kube-dns $dns_ip"

# name|command
checks=(
  "internet https://example.com|curl -fsS -m 8 -o /dev/null https://example.com"
  "metadata 169.254.169.254|curl -fsS -m 5 -o /dev/null http://169.254.169.254/"
  "ClusterIP Service $svc_ip|curl -fsS -m 5 -o /dev/null http://$svc_ip/"
  "another pod $victim_ip|curl -fsS -m 5 -o /dev/null http://$victim_ip/"
  "grain pod $grain_ip|curl -fsS -m 5 -o /dev/null http://$grain_ip/"
  "cluster DNS|timeout 8 getent hosts kubernetes.default.svc.cluster.local"
)
# Expected reach (1) or not (0), per pod, in the order of checks.
declare -A want=(
  [open]="1 0 0 0 1 1"
  [none]="0 0 0 0 1 0"
  [control]="1 1 1 1 1 1"
)
fail=0
for pod in open none control; do
  read -ra exp <<<"${want[$pod]}"
  for i in "${!checks[@]}"; do
    label="${checks[$i]%%|*}" cmd="${checks[$i]#*|}"
    if k exec -n "$NS" "$pod" -- sh -c "$cmd" >/dev/null 2>&1; then got=1; else got=0; fi
    verdict=ok
    [ "$got" = "${exp[$i]}" ] || { verdict=UNEXPECTED; fail=1; }
    printf '%-8s %-32s reach=%s want=%s %s\n' "$pod" "$label" "$got" "${exp[$i]}" "$verdict"
  done
done
exit "$fail"
