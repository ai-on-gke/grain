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

package deploy

// deploy/kubernetes/grain-granule.yaml, held to the same handful of
// cross-referenced agreements kubernetes_manifest_test.go holds
// grain.yaml to -- reusing that file's own helpers (k8sObject,
// k8sDeployment, grainContainer, daemonArgs, flagValue,
// manifestDocsForFile) through the one parameter that differs, the
// filename, rather than a second copy of each. What is asserted beyond
// that parity is what is genuinely different about this manifest:
// docs/k8s-granule-design.md §3's three points (no Namespace object at
// all, the daemon's own pod unprivileged exactly as grain.yaml's is,
// different Deployment flags) and the exact opposite Pod Security
// posture from grain.yaml's own.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ports"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// grainGranuleDeployment is grainDeployment's own counterpart, reading
// grain-granule.yaml instead of grain.yaml.
func grainGranuleDeployment(t *testing.T) k8sDeployment {
	t.Helper()
	objects, nodes := manifestDocsForFile(t, "grain-granule.yaml")
	return grainDeploymentFromDocs(t, objects, nodes)
}

// TestTheKubernetesGranuleManifestShipsNoNamespace is
// docs/k8s-granule-design.md §3's first point, as a test: relaxing Pod
// Security admission on an operator's behalf is too large a thing for
// a bring-up command -- or a manifest that bring-up command applies --
// to do quietly, so this file must never carry a live Namespace object
// at all, privileged or otherwise.
func TestTheKubernetesGranuleManifestShipsNoNamespace(t *testing.T) {
	objects, _ := manifestDocsForFile(t, "grain-granule.yaml")
	want := []string{"ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding",
		"ClusterRole", "ClusterRoleBinding", "PriorityClass", "PriorityClass", "PriorityClass", "PriorityClass", "PersistentVolumeClaim",
		// grain-access before the Deployment, so the grain pod starts with
		// GRAIN_ACCESS_SERVICE_HOST (the acceptor's ClusterIP).
		"Service", "Deployment", "Service"}
	var got []string
	for _, obj := range objects {
		got = append(got, obj.Kind)
		if obj.Kind == "Namespace" {
			t.Fatal("grain-granule.yaml ships a live Namespace object -- docs/k8s-granule-design.md §1 argues " +
				"grain init must never relax Pod Security admission on an operator's behalf, and a manifest " +
				"this file's own apply step runs is exactly that relaxation if it creates the Namespace itself")
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("grain-granule.yaml ships %v; want %v (no Namespace -- see this file's own header)", got, want)
	}
}

// TestTheKubernetesGranuleManifestPutsEveryObjectInTheGrainNamespace is
// TestTheKubernetesManifestPutsEveryObjectInTheNamespaceItCreates' own
// counterpart -- except this file creates no Namespace to read the
// name back from, so "grain" (DefaultK8sNamespace, this manifest's own
// -k8s-namespace default) is asserted directly.
func TestTheKubernetesGranuleManifestPutsEveryObjectInTheGrainNamespace(t *testing.T) {
	objects, _ := manifestDocsForFile(t, "grain-granule.yaml")
	for _, obj := range objects {
		if clusterScoped[obj.Kind] {
			continue
		}
		if obj.Metadata.Namespace != "grain" {
			t.Errorf("%s/%s is in namespace %q, want %q", obj.Kind, obj.Metadata.Name, obj.Metadata.Namespace, "grain")
		}
	}
}

// TestTheKubernetesGranuleDeploymentIsNeverPrivileged is
// docs/k8s-granule-design.md §3's second point: only *sandbox* pods
// this platform's daemon creates are privileged; the daemon's own pod
// is identical in shape to grain.yaml's and must stay that way.
func TestTheKubernetesGranuleDeploymentIsNeverPrivileged(t *testing.T) {
	dep := grainGranuleDeployment(t)
	sc := dep.Spec.Template.Spec.SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("the daemon's pod does not set runAsNonRoot: true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser == 0 {
		t.Error("the daemon's pod runs as root (or names no uid at all)")
	}
	if sc.FSGroup == nil || sc.RunAsUser == nil || *sc.FSGroup != *sc.RunAsUser {
		t.Error("fsGroup and runAsUser are not the same number -- see grain.yaml's own test for why that pair has to move together")
	}
	c := grainContainer(t, dep)
	if c.SecurityContext.Capabilities.Add != nil {
		for _, cap := range c.SecurityContext.Capabilities.Add {
			if cap != "NET_BIND_SERVICE" {
				t.Errorf("the daemon container adds capability %q beyond NET_BIND_SERVICE -- this pod is never privileged", cap)
			}
		}
	}
}

// TestTheKubernetesGranuleManifestSelectsTheGranuleBackend is this
// file's whole point: it must pass -k8s-granule-sandboxes, never
// -k8s-sandboxes (cmd/grain's own checkConfig refuses both together as
// two sandbox backends at once), and -k8s-granule-image rather than
// -k8s-sandbox-image -- and none of the other flags that same
// checkConfig refuses alongside this backend, since every one of those
// is a Deployment that cannot start.
func TestTheKubernetesGranuleManifestSelectsTheGranuleBackend(t *testing.T) {
	flags := daemonArgs(t, grainContainer(t, grainGranuleDeployment(t)))
	if _, ok := flags["k8s-granule-sandboxes"]; !ok {
		t.Error("grain-granule.yaml does not pass -k8s-granule-sandboxes")
	}
	if _, ok := flags["k8s-sandboxes"]; ok {
		t.Error("grain-granule.yaml passes -k8s-sandboxes alongside -k8s-granule-sandboxes -- cmd/grain's own " +
			"checkConfig refuses two sandbox backends selected at once")
	}
	if _, ok := flags["k8s-sandbox-image"]; ok {
		t.Error("grain-granule.yaml passes -k8s-sandbox-image, the plain-pod backend's own flag -- this " +
			"platform names its image with -k8s-granule-image instead")
	}
	// The same class as -k8s-sandboxes above: a flag this backend's own
	// checkConfig refuses, which therefore stops the Deployment from
	// starting at all rather than degrading it. This one shipped for
	// real, and cost a GKE bring-up to find: the container exited
	// immediately, the pod sat in CrashLoopBackOff, and `grain init
	// -platform=k8s-granule` waited out its whole five-minute rollout
	// for a daemon that had already refused its own arguments. The
	// manifest's Role comment said this platform's self-upgrade "is not
	// built yet (cmd/grain's own checkConfig refuses -upgrade-image with
	// -k8s-granule-sandboxes)" while the args passed it anyway, so the
	// file contradicted itself and the args won.
	if _, ok := flags["upgrade-image"]; ok {
		t.Error("grain-granule.yaml passes -upgrade-image, which cmd/grain's checkConfig refuses outright " +
			"with -k8s-granule-sandboxes -- the daemon exits on startup and the Deployment never becomes " +
			"available. Keep it commented out until this backend's self-upgrade exists")
	}
	image := flagValue(t, flags, "k8s-granule-image")
	if image != "CHANGEME" {
		t.Errorf("-k8s-granule-image is %q, want the CHANGEME placeholder docs/k8s-granule-design.md §4 "+
			"explains: nothing publishes a packed guest image yet", image)
	}
}

// TestTheKubernetesGranuleManifestStillShipsItsCHANGEMEValues is
// TestTheKubernetesManifestStillShipsItsCHANGEMEValues' own counterpart.
func TestTheKubernetesGranuleManifestStillShipsItsCHANGEMEValues(t *testing.T) {
	flags := daemonArgs(t, grainContainer(t, grainGranuleDeployment(t)))
	if got := flagValue(t, flags, "default-target-repo"); got != "CHANGEME/CHANGEME" {
		t.Errorf("-default-target-repo is %q, want the CHANGEME placeholder", got)
	}
}

// TestTheKubernetesGranuleManifestPassesNoFlagTheDaemonDoesNotDefine is
// TestTheKubernetesManifestPassesNoFlagTheDaemonDoesNotDefine's own
// counterpart, over grain-granule.yaml's args block instead of
// grain.yaml's -- which is also what proves this manifest's two new
// flags (-k8s-granule-sandboxes, -k8s-granule-image) are ones
// pkg/cli/daemon.go actually defines, not merely ones this manifest
// hopes it does.
func TestTheKubernetesGranuleManifestPassesNoFlagTheDaemonDoesNotDefine(t *testing.T) {
	defined := daemonFlags(t)
	manifest := read(t, "deploy", "kubernetes", "grain-granule.yaml")
	args := between(t, manifest, "          args:\n", "          env:")
	found := 0
	for _, line := range strings.Split(args, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		arg, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		arg = strings.TrimSpace(arg)
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, _, _ = strings.Cut(name, "=")
		found++
		if _, ok := defined[name]; !ok {
			t.Errorf("deploy/kubernetes/grain-granule.yaml passes -%s and pkg/cli/daemon.go defines no such flag", name)
		}
	}
	if found < 5 {
		t.Fatalf("only %d flags read out of the manifest's args list -- this test is reading the wrong block", found)
	}
}

// TestTheKubernetesGranuleRolePinsDeploymentWritesToTheDeploymentAnUpgradeRepoints
// is TestTheKubernetesRolePinsDeploymentWritesToTheDeploymentAnUpgradeRepoints's
// own counterpart. This platform's self-upgrade is refused at startup
// today (cmd/grain's checkConfig), but the Role already grants the
// same verbs grain.yaml's does -- see this manifest's own header,
// point 2 -- and they have to stay pinned to the same Deployment if
// that refusal is ever lifted.
func TestTheKubernetesGranuleRolePinsDeploymentWritesToTheDeploymentAnUpgradeRepoints(t *testing.T) {
	objects, nodes := manifestDocsForFile(t, "grain-granule.yaml")
	var role struct {
		Rules []struct {
			Resources     []string `yaml:"resources"`
			ResourceNames []string `yaml:"resourceNames"`
			Verbs         []string `yaml:"verbs"`
		} `yaml:"rules"`
	}
	for i, obj := range objects {
		if obj.Kind == "Role" {
			if err := nodes[i].Decode(&role); err != nil {
				t.Fatalf("decoding the Role: %v", err)
			}
		}
	}
	deployment := flagValue(t, daemonArgs(t, grainContainer(t, grainGranuleDeployment(t))), "k8s-deployment")

	var pinned bool
	for _, rule := range role.Rules {
		found := false
		for _, res := range rule.Resources {
			if res == "deployments" {
				found = true
			}
		}
		if !found {
			continue
		}
		canWrite := false
		for _, v := range rule.Verbs {
			if v == "patch" || v == "update" {
				canWrite = true
			}
		}
		if !canWrite {
			continue
		}
		if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != deployment {
			t.Errorf("a Role rule grants %v on deployments %v; want it pinned to resourceNames [%s]",
				rule.Verbs, rule.ResourceNames, deployment)
			continue
		}
		pinned = true
	}
	if !pinned {
		t.Errorf("no Role rule grants patch/update on deployment %q", deployment)
	}
}

// TestTheKubernetesGranuleRoleGrantsOnlyCreateAndDeleteOnSecrets checks
// that Role/grain in grain-granule.yaml grants only create and delete on
// Secrets (for per-pod grain-access-<pod> credential Secrets) and never
// get, list, watch, update or patch -- pkg/k8s/secret.go never reads or
// lists Secrets in the namespace.
func TestTheKubernetesGranuleRoleGrantsOnlyCreateAndDeleteOnSecrets(t *testing.T) {
	objects, nodes := manifestDocsForFile(t, "grain-granule.yaml")
	var role struct {
		Rules []struct {
			Resources []string `yaml:"resources"`
			Verbs     []string `yaml:"verbs"`
		} `yaml:"rules"`
	}
	for i, obj := range objects {
		if obj.Kind == "Role" && obj.Metadata.Name == "grain" {
			if err := nodes[i].Decode(&role); err != nil {
				t.Fatalf("decoding Role/grain: %v", err)
			}
		}
	}
	var sawSecretsRule bool
	wantVerbs := []string{"create", "delete"}
	for _, rule := range role.Rules {
		if slices.Contains(rule.Resources, "secrets") {
			sawSecretsRule = true
			if !slices.Equal(rule.Verbs, wantVerbs) {
				t.Errorf("Role/grain secrets verbs = %v, want %v (pkg/k8s/secret.go never reads or lists Secrets)",
					rule.Verbs, wantVerbs)
			}
		}
	}
	if !sawSecretsRule {
		t.Error("Role/grain in grain-granule.yaml has no secrets rule for sandbox-access credentials")
	}
}

// TestTheKubernetesGranuleSandboxAccessServiceAndFlagsAgree checks that
// Service/grain-access is defined ahead of Deployment/grain (so the pod
// receives GRAIN_ACCESS_SERVICE_HOST at start), stays ClusterIP-only on
// ports.SandboxAccess (4244), selects only the daemon pod, and agrees
// with Deployment/grain's -sandbox-access-addr and containerPort.
func TestTheKubernetesGranuleSandboxAccessServiceAndFlagsAgree(t *testing.T) {
	raw := stripComments(read(t, "deploy", "kubernetes", "grain-granule.yaml"))
	for _, forbidden := range []string{
		"7443",
		"GRAIN_GRANULE_ACCESS_MODE",
		"GRAIN_SANDBOX_ACCESS_ADDR",
		"GRAIN_SANDBOX_CA_KEY_FILE",
	} {
		absent(t, raw, forbidden)
	}

	objects, nodes := manifestDocsForFile(t, "grain-granule.yaml")
	accessIdx, depIdx := -1, -1
	var svc *k8sService
	var depDoc struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Labels map[string]string `yaml:"labels"`
				} `yaml:"metadata"`
				Spec struct {
					Containers []struct {
						Name  string `yaml:"name"`
						Ports []struct {
							Name          string `yaml:"name"`
							ContainerPort int    `yaml:"containerPort"`
						} `yaml:"ports"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	for i, obj := range objects {
		switch {
		case obj.Kind == "Service" && obj.Metadata.Name == "grain-access":
			accessIdx = i
			svc = &k8sService{}
			if err := nodes[i].Decode(svc); err != nil {
				t.Fatalf("decoding Service/grain-access: %v", err)
			}
		case obj.Kind == "Deployment" && obj.Metadata.Name == "grain":
			depIdx = i
			if err := nodes[i].Decode(&depDoc); err != nil {
				t.Fatalf("decoding Deployment/grain: %v", err)
			}
		}
	}
	if svc == nil {
		t.Fatal("grain-granule.yaml defines no Service/grain-access")
	}
	if accessIdx > depIdx {
		t.Errorf("Service/grain-access (doc %d) is defined after Deployment/grain (doc %d); "+
			"must precede it so GRAIN_ACCESS_SERVICE_HOST is populated when the pod starts", accessIdx, depIdx)
	}
	if svc.Spec.Type != "" && svc.Spec.Type != "ClusterIP" {
		t.Errorf("Service/grain-access type = %q, want ClusterIP", svc.Spec.Type)
	}
	if svc.Spec.ClusterIP == "None" {
		t.Error("Service/grain-access must not be headless")
	}

	podLabels := depDoc.Spec.Template.Metadata.Labels
	for k, v := range svc.Spec.Selector {
		if podLabels[k] != v {
			t.Errorf("Service/grain-access selects %s=%s, which Deployment/grain's pod template lacks (%v)",
				k, v, podLabels)
		}
	}
	if svc.Spec.Selector[k8s.SandboxLabel] != runhost.KubeDaemonComponent {
		t.Errorf("Service/grain-access does not select %s=%s, so nothing stops it matching a non-daemon pod",
			k8s.SandboxLabel, runhost.KubeDaemonComponent)
	}

	wantAddr := ":" + strconv.Itoa(ports.SandboxAccess)
	flags := daemonArgs(t, grainContainer(t, grainGranuleDeployment(t)))
	if got := flagValue(t, flags, "sandbox-access-addr"); got != wantAddr {
		t.Errorf("Deployment/grain -sandbox-access-addr = %q, want %q", got, wantAddr)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != ports.SandboxAccess || fmt.Sprint(svc.Spec.Ports[0].TargetPort) != "sandbox-access" {
		t.Errorf("Service/grain-access ports = %+v, want sole port %d -> sandbox-access", svc.Spec.Ports, ports.SandboxAccess)
	}
	var sawContainerPort bool
	for _, c := range depDoc.Spec.Template.Spec.Containers {
		if c.Name != "grain" {
			continue
		}
		for _, p := range c.Ports {
			if p.Name == "sandbox-access" && p.ContainerPort == ports.SandboxAccess {
				sawContainerPort = true
			}
		}
	}
	if !sawContainerPort {
		t.Errorf("Deployment/grain container exposes no sandbox-access containerPort %d", ports.SandboxAccess)
	}
}

// TestTheSandboxAccessNetworkPoliciesIsolateTheAcceptorAndExcludeNetworkNone
// checks deploy/kubernetes/sandbox-access-networkpolicy.yaml against the
// labels in pkg/k8s, orchestrator.NetworkLabel, and Deployment/grain:
// sandbox pods get no ingress, open-network internet egress excludes
// grain.dev/network=none pods and blocks cloud metadata + RFC1918 CIDRs,
// and port 4244 on the daemon pod is admitted only from sandbox pods.
func TestTheSandboxAccessNetworkPoliciesIsolateTheAcceptorAndExcludeNetworkNone(t *testing.T) {
	objects, nodes := manifestDocsForFile(t, "sandbox-access-networkpolicy.yaml")
	type netPolPeer struct {
		PodSelector *struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		} `yaml:"podSelector"`
		IPBlock *struct {
			CIDR   string   `yaml:"cidr"`
			Except []string `yaml:"except"`
		} `yaml:"ipBlock"`
	}
	type netPolPort struct {
		Protocol string `yaml:"protocol"`
		Port     int    `yaml:"port"`
		EndPort  int    `yaml:"endPort"`
	}
	type netPol struct {
		k8sObject `yaml:",inline"`
		Spec      struct {
			PodSelector struct {
				MatchLabels      map[string]string `yaml:"matchLabels"`
				MatchExpressions []struct {
					Key      string   `yaml:"key"`
					Operator string   `yaml:"operator"`
					Values   []string `yaml:"values"`
				} `yaml:"matchExpressions"`
			} `yaml:"podSelector"`
			PolicyTypes []string `yaml:"policyTypes"`
			Ingress     []struct {
				From  []netPolPeer `yaml:"from"`
				Ports []netPolPort `yaml:"ports"`
			} `yaml:"ingress"`
			Egress []struct {
				To    []netPolPeer `yaml:"to"`
				Ports []netPolPort `yaml:"ports"`
			} `yaml:"egress"`
		} `yaml:"spec"`
	}

	byName := map[string]netPol{}
	for i, obj := range objects {
		if obj.Kind != "NetworkPolicy" {
			t.Fatalf("sandbox-access-networkpolicy.yaml doc %d has kind %q, want NetworkPolicy", i, obj.Kind)
		}
		if obj.Metadata.Namespace != "grain" {
			t.Errorf("NetworkPolicy/%s namespace = %q, want \"grain\"", obj.Metadata.Name, obj.Metadata.Namespace)
		}
		var np netPol
		if err := nodes[i].Decode(&np); err != nil {
			t.Fatalf("decoding NetworkPolicy/%s: %v", obj.Metadata.Name, err)
		}
		byName[obj.Metadata.Name] = np
	}

	egressPol, ok := byName["grain-sandbox-egress"]
	if !ok {
		t.Fatal("missing NetworkPolicy/grain-sandbox-egress")
	}
	if egressPol.Spec.PodSelector.MatchLabels[k8s.ManagedByLabel] != "grain" ||
		egressPol.Spec.PodSelector.MatchLabels[k8s.SandboxLabel] != "sandbox" {
		t.Errorf("grain-sandbox-egress podSelector = %v, want sandbox labels", egressPol.Spec.PodSelector.MatchLabels)
	}
	if len(egressPol.Spec.Ingress) != 0 || !slices.Contains(egressPol.Spec.PolicyTypes, "Ingress") {
		t.Errorf("grain-sandbox-egress must enforce empty Ingress, got policyTypes=%v ingress=%+v",
			egressPol.Spec.PolicyTypes, egressPol.Spec.Ingress)
	}

	internetPol, ok := byName["grain-sandbox-egress-internet"]
	if !ok {
		t.Fatal("missing NetworkPolicy/grain-sandbox-egress-internet")
	}
	var excludesNone bool
	for _, expr := range internetPol.Spec.PodSelector.MatchExpressions {
		if expr.Key == orchestrator.NetworkLabel && expr.Operator == "NotIn" && slices.Equal(expr.Values, []string{"none"}) {
			excludesNone = true
		}
	}
	if !excludesNone {
		t.Errorf("grain-sandbox-egress-internet must exclude %s=none via NotIn so network:none pods stay isolated, got %+v",
			orchestrator.NetworkLabel, internetPol.Spec.PodSelector.MatchExpressions)
	}
	var excepts []string
	for _, rule := range internetPol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				excepts = append(excepts, peer.IPBlock.Except...)
			}
		}
	}
	for _, cidr := range []string{"169.254.169.254/32", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if !slices.Contains(excepts, cidr) {
			t.Errorf("grain-sandbox-egress-internet ipBlock.except = %v, missing %s", excepts, cidr)
		}
	}

	ingressPol, ok := byName["grain-access-ingress"]
	if !ok {
		t.Fatal("missing NetworkPolicy/grain-access-ingress")
	}
	if ingressPol.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "grain" ||
		ingressPol.Spec.PodSelector.MatchLabels[k8s.SandboxLabel] != runhost.KubeDaemonComponent {
		t.Errorf("grain-access-ingress podSelector = %v, want daemon pod labels", ingressPol.Spec.PodSelector.MatchLabels)
	}
	var sawAcceptorRule bool
	for _, rule := range ingressPol.Spec.Ingress {
		touchesAcceptor := false
		for _, p := range rule.Ports {
			if p.Port == ports.SandboxAccess || (p.EndPort >= ports.SandboxAccess && p.Port <= ports.SandboxAccess) {
				touchesAcceptor = true
			}
		}
		if !touchesAcceptor {
			continue
		}
		sawAcceptorRule = true
		for _, from := range rule.From {
			if from.IPBlock != nil || from.PodSelector == nil ||
				from.PodSelector.MatchLabels[k8s.ManagedByLabel] != "grain" ||
				from.PodSelector.MatchLabels[k8s.SandboxLabel] != "sandbox" {
				t.Errorf("grain-access-ingress admits port %d from non-sandbox peer %+v", ports.SandboxAccess, from)
			}
		}
	}
	if !sawAcceptorRule {
		t.Errorf("grain-access-ingress has no ingress rule for port %d", ports.SandboxAccess)
	}

	webhookPol, ok := byName["grain-webhook-isolation"]
	if !ok {
		t.Fatal("missing NetworkPolicy/grain-webhook-isolation")
	}
	if !slices.Contains(webhookPol.Spec.PolicyTypes, "Egress") || len(webhookPol.Spec.Egress) != 0 {
		t.Errorf("grain-webhook-isolation must deny all egress, got policyTypes=%v egress=%+v",
			webhookPol.Spec.PolicyTypes, webhookPol.Spec.Egress)
	}
}
