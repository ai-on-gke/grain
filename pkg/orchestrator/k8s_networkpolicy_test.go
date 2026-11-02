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
	"bytes"
	"errors"
	"io"
	"net/netip"
	"os"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The NetworkPolicy fields these tests read, and nothing else.
type testNetpol struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		PodSelector testLabelSelector `yaml:"podSelector"`
		PolicyTypes []string          `yaml:"policyTypes"`
		Egress      []struct {
			To []struct {
				PodSelector *testLabelSelector `yaml:"podSelector"`
				IPBlock     *struct {
					CIDR   string   `yaml:"cidr"`
					Except []string `yaml:"except"`
				} `yaml:"ipBlock"`
			} `yaml:"to"`
			Ports []struct {
				Protocol string `yaml:"protocol"`
				Port     int    `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"egress"`
	} `yaml:"spec"`
}

type testLabelSelector struct {
	MatchLabels      map[string]string `yaml:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `yaml:"key"`
		Operator string   `yaml:"operator"`
		Values   []string `yaml:"values"`
	} `yaml:"matchExpressions"`
}

// matches is a label selector's verdict on labels, for the operators
// networkpolicy.yaml uses; any other fails the test that meets it.
func (s testLabelSelector) matches(t *testing.T, labels map[string]string) bool {
	t.Helper()
	for k, v := range s.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, e := range s.MatchExpressions {
		v, has := labels[e.Key]
		switch e.Operator {
		case "In":
			if !has || !slices.Contains(e.Values, v) {
				return false
			}
		case "NotIn":
			if has && slices.Contains(e.Values, v) {
				return false
			}
		default:
			t.Fatalf("selector operator %q is not one this test evaluates", e.Operator)
		}
	}
	return true
}

func loadSandboxNetworkPolicies(t *testing.T) []testNetpol {
	t.Helper()
	b, err := os.ReadFile("../../deploy/kubernetes/networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []testNetpol
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var p testNetpol
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// sandboxPodLabels is what a sandbox pod for shape carries: the labels
// k8s.CreatePod always adds, and whatever podOptions asks for.
func sandboxPodLabels(shape Shape) map[string]string {
	labels := map[string]string{
		k8s.ManagedByLabel: k8s.ManagedByValue,
		k8s.SandboxLabel:   k8s.SandboxValue,
		k8s.InstanceLabel:  "deployment-1",
	}
	for k, v := range NewK8sSandboxes(K8sConfig{Image: "example/sandbox"}).podOptions(shape).Labels {
		labels[k] = v
	}
	return labels
}

func selectingPolicies(t *testing.T, policies []testNetpol, labels map[string]string) []string {
	t.Helper()
	var names []string
	for _, p := range policies {
		if p.Spec.PodSelector.matches(t, labels) {
			names = append(names, p.Metadata.Name)
		}
	}
	return names
}

// TestSandboxNetworkPoliciesSelectEachSandboxExactlyOnce: policies are
// additive, so a network:none pod that the open policy also selected
// would be allowed the internet through it, and an open pod neither
// selected keeps the namespace's ordinary networking -- the metadata
// server and every peer included.
func TestSandboxNetworkPoliciesSelectEachSandboxExactlyOnce(t *testing.T) {
	t.Parallel()
	policies := loadSandboxNetworkPolicies(t)
	for _, tc := range []struct {
		access model.NetworkAccess
		want   string
	}{
		{model.NetworkNone, "grain-sandbox-network-none"},
		{model.NetworkOpen, "grain-sandbox-egress"},
		{"", "grain-sandbox-egress"},
	} {
		got := selectingPolicies(t, policies, sandboxPodLabels(Shape{NetworkAccess: tc.access}))
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("a %q sandbox pod is selected by %v, want only %s", tc.access, got, tc.want)
		}
	}
	// grain's own pod is no sandbox, and fencing it would cut the proxies
	// every sandbox reaches through it.
	if got := selectingPolicies(t, policies, map[string]string{"app.kubernetes.io/name": "grain"}); len(got) != 0 {
		t.Errorf("the grain pod is selected by %v", got)
	}
}

// openSandboxAllows evaluates grain-sandbox-egress's ipBlock rules for a
// connection to addr:port over proto, the way a CNI would.
func openSandboxAllows(t *testing.T, p testNetpol, addr string, proto string, port int) bool {
	t.Helper()
	ip := netip.MustParseAddr(addr)
	for _, rule := range p.Spec.Egress {
		portOK := len(rule.Ports) == 0
		for _, rp := range rule.Ports {
			if rp.Protocol == proto && rp.Port == port {
				portOK = true
			}
		}
		if !portOK {
			continue
		}
		for _, peer := range rule.To {
			if peer.IPBlock == nil {
				continue
			}
			if !netip.MustParsePrefix(peer.IPBlock.CIDR).Contains(ip) {
				continue
			}
			excepted := false
			for _, e := range peer.IPBlock.Except {
				if netip.MustParsePrefix(e).Contains(ip) {
					excepted = true
				}
			}
			if !excepted {
				return true
			}
		}
	}
	return false
}

func TestOpenSandboxEgressPolicyFencesOffTheCluster(t *testing.T) {
	t.Parallel()
	var p testNetpol
	for _, q := range loadSandboxNetworkPolicies(t) {
		if q.Metadata.Name == "grain-sandbox-egress" {
			p = q
		}
	}
	if p.Metadata.Name == "" {
		t.Fatal("networkpolicy.yaml has no grain-sandbox-egress policy")
	}
	if !slices.Equal(p.Spec.PolicyTypes, []string{"Egress"}) {
		t.Errorf("policyTypes = %v, want [Egress] alone", p.Spec.PolicyTypes)
	}
	for _, tc := range []struct {
		why, addr, proto string
		port             int
		want             bool
	}{
		{"the metadata server", "169.254.169.254", "TCP", 80, false},
		{"GKE's metadata proxy", "169.254.169.252", "TCP", 988, false},
		{"GKE's IPv6 metadata server", "fd20:ce::254", "TCP", 80, false},
		{"a 10/8 pod, node or API server", "10.0.0.1", "TCP", 443, false},
		{"a 172.16/12 Service", "172.20.0.10", "TCP", 443, false},
		{"a 192.168/16 node", "192.168.1.5", "TCP", 10250, false},
		{"a shared-space pod range", "100.64.3.4", "TCP", 8080, false},
		{"loopback", "127.0.0.1", "TCP", 80, false},
		{"IPv6 link-local", "fe80::1", "TCP", 80, false},
		{"NodeLocal DNSCache on anything but DNS", "169.254.20.10", "TCP", 80, false},
		{"NodeLocal DNSCache", "169.254.20.10", "UDP", 53, true},
		{"the internet", "8.8.8.8", "TCP", 443, true},
		{"the internet over IPv6", "2001:4860:4860::8888", "TCP", 443, true},
	} {
		if got := openSandboxAllows(t, p, tc.addr, tc.proto, tc.port); got != tc.want {
			t.Errorf("%s (%s %s/%d): allowed = %v, want %v", tc.why, tc.addr, tc.proto, tc.port, got, tc.want)
		}
	}
	// The pod CIDR is fenced off, so grain's own pod -- the git and model
	// proxies -- and cluster DNS need peer rules of their own, or every
	// open sandbox fails its first clone.
	var grainPod, kubeDNS bool
	for _, rule := range p.Spec.Egress {
		for _, peer := range rule.To {
			if peer.PodSelector == nil {
				continue
			}
			if peer.PodSelector.MatchLabels["app.kubernetes.io/name"] == "grain" && len(rule.Ports) == 0 {
				grainPod = true
			}
			if peer.PodSelector.MatchLabels["k8s-app"] == "kube-dns" {
				kubeDNS = true
			}
		}
	}
	if !grainPod {
		t.Error("grain-sandbox-egress does not allow the grain pod on every port")
	}
	if !kubeDNS {
		t.Error("grain-sandbox-egress does not allow kube-dns")
	}
}
