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

# §12 item 1: grain-sandbox-egress on a real cluster. Runs against the
# current kubectl context: a kind cluster created with kind.yaml (default
# CNI disabled) and Calico, with deploy/kubernetes/grain.yaml applied.
#
# Probe pods carry the labels grain's Kubernetes backends put on every
# sandbox pod (pkg/k8s ManagedByLabel/SandboxLabel, plus
# grain.dev/network=none for a network:none task), in namespace grain.
# A control pod with no labels, in namespace default, probes the same
# targets so a refused probe is the policy's doing, not a dead target.
#
# Targets:
#   internet   https://example.com
#   dns        http://grain-standin.grain.svc.cluster.local:8080 -- a
#              headless Service name for the stand-in grain pod, which
#              both sandbox pods may reach, so only resolution decides it
#   metadata   http://169.254.169.254/ -- a stub: busybox httpd on a
#              hostNetwork pod on the worker, after 169.254.169.254/32 is
#              added to the node's lo (kind has no metadata server)
#   apiserver  https://kubernetes.default.svc (the Service VIP)
#   apinode    https://<control-plane node IP>:6443
#   pod        http://<peer pod IP>:8080 (busybox httpd, namespace default)
#   nspod      http://<pod IP>:8080 (busybox httpd in namespace grain, unlabelled)
#   node       https://<worker node IP>:10250 (kubelet)
#   grainpod   http://<stand-in grain pod IP>:8080 (label app.kubernetes.io/name=grain)
#
# A probe is "blocked" when curl times out (exit 28) and "reached" for any
# other result, an HTTP error or a TLS failure included: the packet got there.
set -u
NS=grain
CURL=${CURL:-curlimages/curl:8.11.1}
BB=${BB:-busybox:1.37}
say() { printf '\n== %s\n' "$*"; }

pod() { # name ns labels image args...
  local name=$1 ns=$2 labels=$3 image=$4; shift 4
  kubectl -n "$ns" delete pod "$name" --ignore-not-found --grace-period=1 --wait=true >/dev/null
  kubectl -n "$ns" run "$name" --restart=Never --image="$image" ${labels:+--labels="$labels"} --command -- "$@" >/dev/null
}
ready() { kubectl -n "$2" wait --for=condition=Ready "pod/$1" --timeout=180s >/dev/null; }
ip_of() { kubectl -n "$2" get pod "$1" -o jsonpath='{.status.podIP}'; }

setup_targets() {
  WORKER=$(kubectl get node -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')
  WORKER_IP=$(kubectl get node "$WORKER" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
  CP_IP=$(kubectl get node -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
  docker exec "$WORKER" sh -c 'ip addr show dev lo | grep -q 169.254.169.254 || ip addr add 169.254.169.254/32 dev lo'
  kubectl -n default delete pod metadata-stub --ignore-not-found --grace-period=1 --wait=true >/dev/null
  kubectl -n default apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: metadata-stub}
spec:
  hostNetwork: true
  nodeName: $WORKER
  containers:
  - name: httpd
    image: $BB
    command: [sh, -c, "mkdir -p /w/computeMetadata/v1/instance/service-accounts && echo default/ > /w/computeMetadata/v1/instance/service-accounts/index.html && exec httpd -f -p 169.254.169.254:80 -h /w"]
YAML
  pod peer default "" "$BB" sh -c 'echo peer > /i.html; exec httpd -f -p 8080 -h /'
  pod nspeer "$NS" "" "$BB" sh -c 'echo nspeer > /i.html; exec httpd -f -p 8080 -h /'
  pod grain-standin "$NS" "app.kubernetes.io/name=grain,walk=grain-standin" "$BB" sh -c 'echo grain > /i.html; exec httpd -f -p 8080 -h /'
  kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Service
metadata: {name: grain-standin}
spec:
  clusterIP: None
  selector: {walk: grain-standin}
  ports: [{port: 8080}]
YAML
  for p in "metadata-stub default" "peer default" "nspeer $NS" "grain-standin $NS"; do ready $p; done
  PEER_IP=$(ip_of peer default); NSPEER_IP=$(ip_of nspeer "$NS"); GRAIN_IP=$(ip_of grain-standin "$NS")
  echo "worker=$WORKER ($WORKER_IP) control-plane=$CP_IP peer=$PEER_IP nspeer=$NSPEER_IP grain-standin=$GRAIN_IP"
}

# probe POD NS -- runs every target from a long-lived curl pod.
probe() {
  local p=$1 ns=$2 name url rc verdict line=""
  for t in "internet https://example.com" "dns http://grain-standin.$NS.svc.cluster.local:8080/i.html" \
           "metadata http://169.254.169.254/computeMetadata/v1/instance/service-accounts/" \
           "apiserver https://kubernetes.default.svc" "apinode https://$CP_IP:6443" \
           "pod http://$PEER_IP:8080/i.html" "nspod http://$NSPEER_IP:8080/i.html" \
           "node https://$WORKER_IP:10250" "grainpod http://$GRAIN_IP:8080/i.html"; do
    name=${t%% *}; url=${t#* }
    kubectl -n "$ns" exec "$p" -- curl -sk -o /dev/null -m 4 "$url" >/dev/null 2>&1; rc=$?
    # dns: 6 is "could not resolve", 28 a resolver that never answered.
    case $rc in 28|6) verdict=blocked ;; *) verdict=reached ;; esac
    printf '  %-10s %-8s (curl exit %d)\n' "$name" "$verdict" "$rc"
  done
}

prober() { # name ns labels
  pod "$1" "$2" "$3" "$CURL" sleep 3600
  ready "$1" "$2"
}
