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

package security

import (
	"bytes"
	"errors"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Path 1 of the 2026-11-10 sandbox-to-host audit, on Kubernetes: an
// open-network sandbox pod must not reach the node's metadata server
// (and on GKE the node service account token behind it) or the
// cluster's own pods, nodes and Services. Three things hold that here:
//
//   - every deployment manifest an operator might apply alone ships the
//     grain-sandbox-egress NetworkPolicy, dropping link-local (the
//     metadata servers), unique-local IPv6 (GKE's and AWS's IPv6 metadata
//     servers) and IPv6 link-local;
//   - a Kubernetes backend whose operator has not said that policy is
//     enforced (-k8s-network-policy) reports its open-network sandboxes
//     unfenced, which refuses external capability grants on it the way an
//     unfenced Docker host's are refused;
//   - an open-network granule pod's own splice drops guest traffic to the
//     metadata endpoints, independent of the cluster's CNI.

type egressPolicy struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Egress []struct {
			To []struct {
				IPBlock *struct {
					CIDR   string   `yaml:"cidr"`
					Except []string `yaml:"except"`
				} `yaml:"ipBlock"`
			} `yaml:"to"`
		} `yaml:"egress"`
	} `yaml:"spec"`
}

func sandboxEgressPolicy(t *testing.T, file string) *egressPolicy {
	t.Helper()
	b, err := os.ReadFile("../../deploy/kubernetes/" + file)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var p egressPolicy
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if p.Kind == "NetworkPolicy" && p.Metadata.Name == "grain-sandbox-egress" {
			return &p
		}
	}
}

func TestEveryKubernetesManifestFencesOpenSandboxesOffMetadata(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"grain.yaml", "grain-granule.yaml", "networkpolicy.yaml"} {
		p := sandboxEgressPolicy(t, file)
		if p == nil {
			t.Errorf("%s ships no grain-sandbox-egress: applied alone, it leaves open-network sandbox pods "+
				"able to reach the node's metadata server", file)
			continue
		}
		var excepts []string
		for _, rule := range p.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil {
					excepts = append(excepts, peer.IPBlock.Except...)
				}
			}
		}
		for _, want := range []string{"169.254.0.0/16", "fc00::/7", "fe80::/10"} {
			if !slices.Contains(excepts, want) {
				t.Errorf("%s's grain-sandbox-egress does not drop %s (excepts %v)", file, want, excepts)
			}
		}
	}
}

func TestKubernetesBackendsRefuseCapabilityGrantsUntilNetworkPolicyIsEnforced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for _, tc := range []struct {
		name     string
		why      func(enforced bool) string
		backends string
	}{
		{"kubernetes", func(enforced bool) string {
			return orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{Image: "example/sandbox", NetworkPolicyEnforced: enforced}).OpenNetworkUnfenced(ctx)
		}, "K8sSandboxes"},
		{"kubernetes-granule", func(enforced bool) string {
			return orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{Image: "example/granule", NetworkPolicyEnforced: enforced}).OpenNetworkUnfenced(ctx)
		}, "K8sGranuleSandboxes"},
	} {
		if why := tc.why(false); why == "" {
			t.Errorf("%s without -k8s-network-policy reports open-network sandboxes fenced, so external "+
				"capability grants would be admitted with nothing keeping the metadata server or an in-cluster "+
				"control plane out of reach", tc.backends)
		} else if !strings.Contains(why, "-k8s-network-policy") {
			t.Errorf("%s: OpenNetworkUnfenced = %q, which does not name the flag that fences it", tc.backends, why)
		}
		if why := tc.why(true); why != "" {
			t.Errorf("%s with -k8s-network-policy: OpenNetworkUnfenced = %q, want fenced", tc.backends, why)
		}
	}
}

func TestGranulePodsGuardMetadataIndependentOfTheCNI(t *testing.T) {
	t.Parallel()
	guarded := map[string]bool{}
	for _, raw := range orchestrator.K8sGranuleMetadataGuardIPs {
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("guard address %q: %v", raw, err)
		}
		guarded[ip.String()] = true
	}
	for _, want := range []string{"169.254.169.254", "169.254.169.252", "fd20:ce::254", "fd00:ec2::254"} {
		if !guarded[want] {
			t.Errorf("open-network granule pods do not drop guest traffic to metadata endpoint %s in-pod", want)
		}
	}
}
