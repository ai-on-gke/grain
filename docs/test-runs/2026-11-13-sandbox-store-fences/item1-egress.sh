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

# §12 item 1: apply MANIFESTS (default: deploy/kubernetes/grain.yaml
# alone) and probe from an open-network sandbox pod, a network:none
# sandbox pod and an unlabelled control pod.
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
. "$HERE/lib-k8s.sh"
MANIFESTS=${MANIFESTS:-$ROOT/deploy/kubernetes/grain.yaml}

say "1a. cluster and CNI"
kubectl get nodes -o wide | awk '{print $1, $2, $6}'
kubectl -n kube-system get ds calico-node -o jsonpath='calico-node ready {.status.numberReady}/{.status.desiredNumberScheduled}{"\n"}'

say "1b. apply: $(for m in $MANIFESTS; do printf '%s ' "${m#$ROOT/}"; done)"
kubectl delete networkpolicy -n $NS --all --ignore-not-found >/dev/null 2>&1 || true
for m in $MANIFESTS; do kubectl apply -f "$m" 2>&1 | grep -i "networkpolicy\|namespace/\|error"; done
kubectl -n $NS get networkpolicy

say "1c. targets"
setup_targets

say "1d. probes"
prober control default ""
prober sbx-open $NS "app.kubernetes.io/managed-by=grain,app.kubernetes.io/component=sandbox"
prober sbx-none $NS "app.kubernetes.io/managed-by=grain,app.kubernetes.io/component=sandbox,grain.dev/network=none"
sleep 3 # let Calico program the new pods' policy
echo "control pod (namespace default, no labels):"; probe control default
echo "open-network sandbox pod:"; probe sbx-open $NS
echo "network:none sandbox pod:"; probe sbx-none $NS
