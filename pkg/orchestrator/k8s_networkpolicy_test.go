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
	"reflect"
	"slices"
	"strings"
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
	return loadNetworkPoliciesFrom(t, "networkpolicy.yaml")
}

// bundledEgressManifests are the deployment manifests that carry their
// own copy of grain-sandbox-egress, so that applying one alone never
// leaves an open-network sandbox pod able to reach the metadata server
// or the cluster.
var bundledEgressManifests = []string{"grain.yaml", "grain-granule.yaml"}

// manifestDocuments is every YAML document in deploy/kubernetes/file,
// generically decoded; empty documents are skipped.
func manifestDocuments(t *testing.T, file string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../deploy/kubernetes/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if doc != nil {
			out = append(out, doc)
		}
	}
	return out
}

// loadNetworkPoliciesFrom is the NetworkPolicy objects in
// deploy/kubernetes/file, in the fields these tests read.
func loadNetworkPoliciesFrom(t *testing.T, file string) []testNetpol {
	t.Helper()
	var out []testNetpol
	for _, doc := range manifestDocuments(t, file) {
		if doc["kind"] != "NetworkPolicy" {
			continue
		}
		raw, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var p testNetpol
		if err := yaml.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// namedNetworkPolicy is file's NetworkPolicy called name, generically
// decoded, or nil.
func namedNetworkPolicy(t *testing.T, file, name string) map[string]any {
	t.Helper()
	for _, doc := range manifestDocuments(t, file) {
		md, _ := doc["metadata"].(map[string]any)
		if doc["kind"] == "NetworkPolicy" && md != nil && md["name"] == name {
			return doc
		}
	}
	return nil
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

// granuleSandboxPodLabels is sandboxPodLabels for a
// K8sGranuleSandboxes pod.
func granuleSandboxPodLabels(shape Shape) map[string]string {
	labels := map[string]string{
		k8s.ManagedByLabel: k8s.ManagedByValue,
		k8s.SandboxLabel:   k8s.SandboxValue,
		k8s.InstanceLabel:  "deployment-1",
	}
	for k, v := range NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"}).podOptions(shape).Labels {
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
	for _, file := range append([]string{"networkpolicy.yaml"}, bundledEgressManifests...) {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			checkOpenSandboxEgressPolicy(t, file)
		})
	}
}

func checkOpenSandboxEgressPolicy(t *testing.T, file string) {
	t.Helper()
	var p testNetpol
	for _, q := range loadNetworkPoliciesFrom(t, file) {
		if q.Metadata.Name == "grain-sandbox-egress" {
			p = q
		}
	}
	if p.Metadata.Name == "" {
		t.Fatalf("%s has no grain-sandbox-egress policy", file)
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
		{"the metadata server over plain HTTPS", "169.254.169.254", "TCP", 443, false},
		{"AWS's IPv6 metadata server", "fd00:ec2::254", "TCP", 80, false},
		{"EKS's Pod Identity agent", "169.254.170.23", "TCP", 80, false},
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

// TestBundledSandboxEgressPolicyMatchesNetworkPolicyYAML: grain.yaml and
// grain-granule.yaml each carry grain-sandbox-egress so that applying
// either alone fences open-network sandbox pods, and each copy is the
// same object as networkpolicy.yaml's -- the one whose comments explain
// it, and the one an operator applying both files gets twice. A rule
// edited in one place and not the others fails here.
func TestBundledSandboxEgressPolicyMatchesNetworkPolicyYAML(t *testing.T) {
	t.Parallel()
	want := namedNetworkPolicy(t, "networkpolicy.yaml", "grain-sandbox-egress")
	if want == nil {
		t.Fatal("networkpolicy.yaml has no grain-sandbox-egress policy")
	}
	for _, file := range bundledEgressManifests {
		got := namedNetworkPolicy(t, file, "grain-sandbox-egress")
		if got == nil {
			t.Errorf("%s ships no grain-sandbox-egress: applying it alone leaves every open-network sandbox pod "+
				"able to reach the node's metadata server and the cluster", file)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			gb, _ := yaml.Marshal(got)
			wb, _ := yaml.Marshal(want)
			t.Errorf("%s's grain-sandbox-egress differs from networkpolicy.yaml's; edit both or neither.\n%s:\n%s\nnetworkpolicy.yaml:\n%s",
				file, file, gb, wb)
		}
	}
}

// TestGrainYAMLShipsTheNetworkNonePolicy: grain.yaml carries
// grain-sandbox-network-none too, the same object as networkpolicy.yaml's.
// -k8s-network-policy both admits network:none tasks on K8sSandboxes and
// lets external capability grants through, so an operator who set it for
// the grants after applying grain.yaml alone got network:none pods that
// no policy selected: on kind with Calico such a pod reached the metadata
// server, the API server, peer pods and node IPs, where an open-network
// pod reached none of them (docs/test-runs/2026-11-13-sandbox-store-fences.md,
// F1). grain-granule.yaml does not need it: a granule network:none guest
// boots with no network device at all (K8sGranuleSandboxes.IsolatesNetwork).
func TestGrainYAMLShipsTheNetworkNonePolicy(t *testing.T) {
	t.Parallel()
	want := namedNetworkPolicy(t, "networkpolicy.yaml", "grain-sandbox-network-none")
	if want == nil {
		t.Fatal("networkpolicy.yaml has no grain-sandbox-network-none policy")
	}
	got := namedNetworkPolicy(t, "grain.yaml", "grain-sandbox-network-none")
	if got == nil {
		t.Fatal("grain.yaml ships no grain-sandbox-network-none: with -k8s-network-policy set, a network:none " +
			"sandbox pod is selected by no policy and reaches the metadata server and the cluster")
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := yaml.Marshal(got)
		wb, _ := yaml.Marshal(want)
		t.Errorf("grain.yaml's grain-sandbox-network-none differs from networkpolicy.yaml's; edit both or neither.\ngrain.yaml:\n%s\nnetworkpolicy.yaml:\n%s", gb, wb)
	}
	policies := loadNetworkPoliciesFrom(t, "grain.yaml")
	if got := selectingPolicies(t, policies, sandboxPodLabels(Shape{NetworkAccess: model.NetworkNone})); !slices.Equal(got, []string{"grain-sandbox-network-none"}) {
		t.Errorf("grain.yaml: a network:none sandbox pod is selected by %v, want only grain-sandbox-network-none", got)
	}
}

// TestBundledEgressPolicySelectsEveryOpenSandboxOfItsBackend: the copy in
// each deployment manifest has to select the pods that manifest's
// backend actually creates, open ones included and network:none ones
// excluded (the policies are additive; see
// TestSandboxNetworkPoliciesSelectEachSandboxExactlyOnce).
func TestBundledEgressPolicySelectsEveryOpenSandboxOfItsBackend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file   string
		labels func(Shape) map[string]string
		// none is what may select a network:none pod: grain.yaml ships
		// grain-sandbox-network-none, grain-granule.yaml ships nothing
		// for it (its guests boot with no network device).
		none []string
	}{
		{"grain.yaml", sandboxPodLabels, []string{"grain-sandbox-network-none"}},
		{"grain-granule.yaml", granuleSandboxPodLabels, nil},
	} {
		policies := loadNetworkPoliciesFrom(t, tc.file)
		for _, access := range []model.NetworkAccess{model.NetworkOpen, ""} {
			if got := selectingPolicies(t, policies, tc.labels(Shape{NetworkAccess: access})); !slices.Equal(got, []string{"grain-sandbox-egress"}) {
				t.Errorf("%s: a %q sandbox pod is selected by %v, want only grain-sandbox-egress", tc.file, access, got)
			}
		}
		if got := selectingPolicies(t, policies, tc.labels(Shape{NetworkAccess: model.NetworkNone})); !slices.Equal(got, tc.none) {
			t.Errorf("%s: a network:none sandbox pod is selected by %v, want %v -- additive egress would hand it the internet", tc.file, got, tc.none)
		}
		if got := selectingPolicies(t, policies, map[string]string{"app.kubernetes.io/name": "grain"}); len(got) != 0 {
			t.Errorf("%s: the grain pod is selected by %v", tc.file, got)
		}
	}
}

// TestK8sBackendsAreUnfencedUntilNetworkPolicyIsEnforced: without
// -k8s-network-policy nothing grain can rely on keeps an open-network pod
// off the metadata server or a capability server's in-cluster control
// plane, so both Kubernetes backends say so (unfencedOpenNetwork), and
// mintCapabilityPass refuses external capability grants on them as it
// does on an unfenced ContainerSandboxes.
func TestK8sBackendsAreUnfencedUntilNetworkPolicyIsEnforced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for _, tc := range []struct {
		name               string
		unfenced, enforced Sandboxes
	}{
		{"kubernetes",
			NewK8sSandboxes(K8sConfig{Image: "example/sandbox"}),
			NewK8sSandboxes(K8sConfig{Image: "example/sandbox", NetworkPolicyEnforced: true})},
		{"kubernetes-granule",
			NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"}),
			NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule", NetworkPolicyEnforced: true})},
	} {
		why := openNetworkUnfenced(ctx, tc.unfenced)
		if why == "" {
			t.Errorf("%s without NetworkPolicyEnforced reports its open-network pods fenced", tc.name)
		}
		for _, want := range []string{"-k8s-network-policy", "metadata server", "capability servers' control planes"} {
			if !strings.Contains(why, want) {
				t.Errorf("%s: OpenNetworkUnfenced = %q, want it to mention %q", tc.name, why, want)
			}
		}
		if why := openNetworkUnfenced(ctx, tc.enforced); why != "" {
			t.Errorf("%s with NetworkPolicyEnforced: OpenNetworkUnfenced = %q, want fenced", tc.name, why)
		}
		if isUnisolated(tc.unfenced) {
			t.Errorf("%s reports itself unisolated, which refuses far more than an unfenced backend should", tc.name)
		}
	}
}

// TestK8sGranuleOpenPodsDropMetadataInsideThePod: an inline granule
// guest owns its pod's network, so the pod's own splice is told to drop
// frames to the metadata endpoints (NETSHIM_CONTROLLER_IPS), whatever
// the cluster's CNI does with grain-sandbox-egress.
func TestK8sGranuleOpenPodsDropMetadataInsideThePod(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	guarded := func(env map[string]string) map[string]bool {
		out := map[string]bool{}
		for _, f := range strings.Split(env[NetshimControllerIPsEnv], ",") {
			if ip, err := netip.ParseAddr(strings.TrimSpace(f)); err == nil {
				out[ip.String()] = true
			}
		}
		return out
	}
	for _, access := range []model.NetworkAccess{model.NetworkOpen, ""} {
		opts := k.podOptions(Shape{NetworkAccess: access})
		if opts.Env["CHV_NET_MODE"] != "inline" {
			t.Fatalf("a %q pod boots CHV_NET_MODE=%q, want inline", access, opts.Env["CHV_NET_MODE"])
		}
		got := guarded(opts.Env)
		for _, want := range []string{"169.254.169.254", "169.254.169.252", "fd20:ce::254", "fd00:ec2::254"} {
			if !got[want] {
				t.Errorf("a %q granule pod's splice does not drop guest traffic to %s (%s=%q)",
					access, want, NetshimControllerIPsEnv, opts.Env[NetshimControllerIPsEnv])
			}
		}
		// The resolver an open guest uses through NodeLocal DNSCache is
		// link-local too, and must stay reachable.
		if got["169.254.20.10"] {
			t.Errorf("a %q granule pod drops NodeLocal DNSCache (169.254.20.10)", access)
		}
	}
	// Every guarded address is inside a range grain-sandbox-egress drops
	// as well: the in-pod layer narrows the policy, never widens it.
	for _, raw := range K8sGranuleMetadataGuardIPs {
		ip := netip.MustParseAddr(raw)
		if !netip.MustParsePrefix("169.254.0.0/16").Contains(ip) && !netip.MustParsePrefix("fc00::/7").Contains(ip) {
			t.Errorf("guarded address %s is outside 169.254.0.0/16 and fc00::/7", raw)
		}
	}
	// A network:none guest has no NIC and no splice to guard.
	if v, ok := k.podOptions(Shape{NetworkAccess: model.NetworkNone}).Env[NetshimControllerIPsEnv]; ok {
		t.Errorf("a network:none granule pod sets %s=%q", NetshimControllerIPsEnv, v)
	}
	// An operator's own NETSHIM_CONTROLLER_IPS is kept, merged rather
	// than replaced, and cannot take the metadata addresses away.
	withEnv := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule",
		Env: map[string]string{NetshimControllerIPsEnv: "10.9.8.7, 169.254.169.254"}})
	env := withEnv.podOptions(Shape{NetworkAccess: model.NetworkOpen}).Env
	got := guarded(env)
	if !got["10.9.8.7"] || !got["169.254.169.254"] || !got["169.254.169.252"] {
		t.Errorf("operator env merged into %s=%q, want 10.9.8.7 kept and every metadata address added",
			NetshimControllerIPsEnv, env[NetshimControllerIPsEnv])
	}
	if n := strings.Count(env[NetshimControllerIPsEnv], "169.254.169.254"); n != 1 {
		t.Errorf("%s=%q lists 169.254.169.254 %d times", NetshimControllerIPsEnv, env[NetshimControllerIPsEnv], n)
	}
}
