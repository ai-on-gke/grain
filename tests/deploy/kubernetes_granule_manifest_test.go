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
	"slices"
	"strings"
	"testing"
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

// TestTheKubernetesGranuleRoleNeverReadsSecrets: the daemon creates
// and deletes the per-sandbox Secrets it hands a guest (pkg/k8s's
// secret.go) and never reads or lists one, so its Role grants exactly
// create and delete on secrets -- a get, list or watch there would let
// a compromised daemon pod read every Secret in its namespace. The GKE
// overlay's test held the base manifest to this before that overlay
// was removed.
func TestTheKubernetesGranuleRoleNeverReadsSecrets(t *testing.T) {
	objects, nodes := manifestDocsForFile(t, "grain-granule.yaml")
	var sawRole, sawSecretsRule bool
	for i, obj := range objects {
		if obj.Kind != "Role" {
			continue
		}
		sawRole = true
		var role struct {
			Rules []struct {
				Resources []string `yaml:"resources"`
				Verbs     []string `yaml:"verbs"`
			} `yaml:"rules"`
		}
		if err := nodes[i].Decode(&role); err != nil {
			t.Fatalf("decoding Role/%s: %v", obj.Metadata.Name, err)
		}
		for _, rule := range role.Rules {
			if !slices.Contains(rule.Resources, "secrets") && !slices.Contains(rule.Resources, "*") {
				continue
			}
			sawSecretsRule = true
			if want := []string{"create", "delete"}; !slices.Equal(rule.Verbs, want) {
				t.Errorf("Role/%s grants %v on %v; want exactly %v (the daemon never reads or lists Secrets)",
					obj.Metadata.Name, rule.Verbs, rule.Resources, want)
			}
		}
	}
	if !sawRole {
		t.Fatal("grain-granule.yaml ships no Role")
	}
	if !sawSecretsRule {
		t.Error("grain-granule.yaml's Role has no rule on secrets; the daemon needs create and delete there")
	}
}
