// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"net"
	"strings"
)

// The Kubernetes backends' answer to the fence ContainerSandboxes and
// GranuleSandboxes install on their own host (openFence): what keeps an
// open-network sandbox pod off the node's metadata server, the
// cluster's Services, pods and nodes, and any capability server whose
// control plane is inside the cluster.
//
// On Kubernetes that fence is deploy/kubernetes/networkpolicy.yaml's
// grain-sandbox-egress (bundled into grain.yaml and grain-granule.yaml
// too), and it is only a fence on a cluster whose CNI enforces it --
// which grain cannot check from inside a pod, so it believes the
// operator's -k8s-network-policy (NetworkPolicyEnforced). Without it,
// both backends report themselves unfenced (unfencedOpenNetwork) and
// mintCapabilityPass refuses external capability grants on them, as it
// does on an unfenced ContainerSandboxes.
//
// A granule pod adds one layer that does not rest on the CNI at all:
// its splice drops guest frames addressed to the cloud metadata servers
// before they leave the pod (k8sGranuleMetadataGuardEnv). That narrows
// the metadata path, not the cluster one, so it does not make the
// backend fenced by itself.

// k8sUnfencedReason is why a Kubernetes backend without
// NetworkPolicyEnforced cannot promise its open-network pods are fenced.
// mintCapabilityPass appends ", so it runs no attempt holding a
// capability grant".
const k8sUnfencedReason = "this deployment has not said its cluster enforces NetworkPolicy (-k8s-network-policy), " +
	"so nothing grain can rely on keeps its open-network sandbox pods off the node's metadata server and the " +
	"cluster's own Services, pods and nodes, capability servers' control planes included " +
	"(apply deploy/kubernetes/networkpolicy.yaml's grain-sandbox-egress on a CNI that enforces it, then set the flag -- " +
	"or, once this deployment has stored settings, `grain settings -k8s-network-policy=true` and a restart)"

var (
	_ unfencedOpenNetwork = (*K8sSandboxes)(nil)
	_ unfencedOpenNetwork = (*K8sGranuleSandboxes)(nil)
)

// OpenNetworkUnfenced implements unfencedOpenNetwork: a sandbox pod is
// fenced only by grain-sandbox-egress, and that only on a cluster the
// operator has said enforces it.
func (k *K8sSandboxes) OpenNetworkUnfenced(context.Context) string {
	if k.cfg.NetworkPolicyEnforced {
		return ""
	}
	return k8sUnfencedReason
}

// OpenNetworkUnfenced implements unfencedOpenNetwork, for the same
// reason as K8sSandboxes': the in-pod metadata drop
// (k8sGranuleMetadataGuardEnv) covers the metadata servers' own
// addresses, not the cluster's pods, nodes and Services.
func (k *K8sGranuleSandboxes) OpenNetworkUnfenced(context.Context) string {
	if k.cfg.NetworkPolicyEnforced {
		return ""
	}
	return k8sUnfencedReason
}

// NetshimControllerIPsEnv is the variable granule's netshim reads the
// addresses it drops guest traffic to from (netshim.Config.ControllerIPs):
// a tc filter on the guest tap's ingress, ahead of every allow rule,
// dropping IPv4, ARP, IPv6 and neighbour solicitations addressed to each
// one. granule-shim merges the sandbox access controller's own addresses
// into whatever this already holds.
const NetshimControllerIPsEnv = "NETSHIM_CONTROLLER_IPS"

// K8sGranuleMetadataGuardIPs are the cloud metadata and workload-identity
// endpoints an open-network granule guest's frames are dropped toward
// inside its own pod, whatever the cluster's CNI does: the GCE/GKE,
// AWS and Azure instance metadata servers, GKE's metadata server proxy,
// and EKS's Pod Identity agent, over IPv4 and IPv6.
//
// They are addresses rather than openFence's 169.254.0.0/16, fc00::/7
// and fe80::/10 because of where an inline guest's traffic goes. netshim
// splices the guest's tap onto the pod's eth0 with tc ingress redirects,
// which hand each frame straight to the other device's transmit path:
// it never reaches the pod's IP stack, so no nftables chain in the pod
// -- inet, bridge or netdev -- ever sees it, and the only place a drop
// can sit is the splice's own tc filters, which match exact addresses
// today. The ranges stay grain-sandbox-egress's to drop; these are the
// endpoints that hand out credentials, and NodeLocal DNSCache's
// 169.254.20.10, which an open guest resolves through, is deliberately
// not among them.
var K8sGranuleMetadataGuardIPs = []string{
	"169.254.169.254", // GCE/GKE, AWS IMDS and Azure IMDS
	"169.254.169.252", // GKE metadata server (Workload Identity) proxy
	"169.254.170.23",  // EKS Pod Identity agent
	"fd20:ce::254",    // GCE/GKE metadata server over IPv6
	"fd00:ec2::254",   // AWS IMDS over IPv6
	"fd00:ec2::23",    // EKS Pod Identity agent over IPv6
}

// k8sGranuleMetadataGuardEnv adds K8sGranuleMetadataGuardIPs to env's
// NetshimControllerIPsEnv, keeping whatever an operator's -k8s-granule
// env already listed there and listing nothing twice.
func k8sGranuleMetadataGuardEnv(env map[string]string) {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		key := raw
		if ip := net.ParseIP(raw); ip != nil {
			key = ip.String()
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, raw)
	}
	for _, f := range strings.Split(env[NetshimControllerIPsEnv], ",") {
		add(f)
	}
	for _, ip := range K8sGranuleMetadataGuardIPs {
		add(ip)
	}
	env[NetshimControllerIPsEnv] = strings.Join(out, ",")
}
