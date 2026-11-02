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

// deploy/kubernetes/grain.yaml, held to the handful of agreements it
// makes with itself and with the daemon it starts.
//
// Nothing that runs was reading this file. The only thing in the tree
// that applies it is pkg/orchestrator's TestK8sSandboxesAgainstAReal
// KindCluster, and the "real Kubernetes sandboxes" job it belongs to is
// out of grain CI (grain/task-161) -- so a manifest that no longer
// parses, or that has quietly lost a property something else here
// depends on, reaches an operator's cluster before it reaches a test.
//
// What is asserted is only what is *cross-referenced*: a value in this
// file that has to equal another value in this file, or a flag name the
// daemon really reads. Three of them are correct only together -- an
// edit that moves runAsUser and leaves fsGroup, or that moves HOME and
// leaves the initContainer's mkdir, produces a deployment that comes up
// and cannot write its own store, which is exactly the failure a manifest
// review is worst at catching. What is deliberately *not* asserted is the
// rest of the manifest: probe periods, resource requests, the RBAC verbs.
// A test that restates the whole file is one that has to be edited every
// time the file is, which is the test nobody keeps.
//
// This is the one file in this package read as YAML rather than as text.
// The agreements above are between values, not between spellings, and a
// value in a pod spec is a dozen lines below the key that names it.

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"gopkg.in/yaml.v3"
)

// k8sObject is as much of any document as the assertions below need in
// common: what it is and where it lives.
type k8sObject struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

// k8sContainer covers both the initContainer and the container: the
// spellings that have to agree with each other live in these four keys.
type k8sContainer struct {
	Name    string   `yaml:"name"`
	Image   string   `yaml:"image"`
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
		// The downward API, which is the only way a pod learns anything
		// about itself: its own address and its own name are both
		// assigned after the manifest was written.
		ValueFrom struct {
			FieldRef struct {
				FieldPath string `yaml:"fieldPath"`
			} `yaml:"fieldRef"`
		} `yaml:"valueFrom"`
	} `yaml:"env"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
	} `yaml:"volumeMounts"`
	SecurityContext struct {
		Capabilities struct {
			Add  []string `yaml:"add"`
			Drop []string `yaml:"drop"`
		} `yaml:"capabilities"`
	} `yaml:"securityContext"`
}

type k8sDeployment struct {
	k8sObject `yaml:",inline"`
	Spec      struct {
		Template struct {
			Spec struct {
				SecurityContext struct {
					// Pointers, so that "the key is gone" is a different
					// answer from "the key says 0" -- and uid 0 is the
					// answer this manifest exists to stop giving.
					RunAsNonRoot *bool  `yaml:"runAsNonRoot"`
					RunAsUser    *int64 `yaml:"runAsUser"`
					RunAsGroup   *int64 `yaml:"runAsGroup"`
					FSGroup      *int64 `yaml:"fsGroup"`
				} `yaml:"securityContext"`
				PriorityClassName string         `yaml:"priorityClassName"`
				InitContainers    []k8sContainer `yaml:"initContainers"`
				Containers        []k8sContainer `yaml:"containers"`
				Volumes           []struct {
					Name                  string `yaml:"name"`
					PersistentVolumeClaim struct {
						ClaimName string `yaml:"claimName"`
					} `yaml:"persistentVolumeClaim"`
				} `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// manifestDocs decodes every document in grain.yaml, once, and hands
// back the raw nodes beside the identity of each -- so an assertion
// about one object can decode that object into a shape of its own
// without every test re-reading the file.
//
// Every document has to decode: a manifest with one unparseable
// document is a `kubectl apply` that installs nothing, and that is the
// cheapest failure this file can catch.
func manifestDocs(t *testing.T) ([]k8sObject, []*yaml.Node) {
	t.Helper()
	return manifestDocsForFile(t, "grain.yaml")
}

// manifestDocsForFile is manifestDocs against any file in
// deploy/kubernetes -- grain-granule.yaml's own tests
// (kubernetes_granule_manifest_test.go) share every helper in this file
// through this one parameter rather than through a second copy of each.
func manifestDocsForFile(t *testing.T, filename string) ([]k8sObject, []*yaml.Node) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(read(t, "deploy", "kubernetes", filename)))
	var objects []k8sObject
	var nodes []*yaml.Node
	for i := 0; ; i++ {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("deploy/kubernetes/%s: document %d does not parse: %v", filename, i, err)
		}
		// The file opens with its own header comment, which is a
		// document with no content of its own; `kubectl apply` ignores
		// it and so does this.
		if node.Kind == 0 {
			continue
		}
		var obj k8sObject
		if err := node.Decode(&obj); err != nil {
			t.Fatalf("deploy/kubernetes/%s: document %d is not a Kubernetes object: %v", filename, i, err)
		}
		objects = append(objects, obj)
		nodes = append(nodes, &node)
	}
	if len(objects) == 0 {
		t.Fatalf("deploy/kubernetes/%s decodes to no objects at all", filename)
	}
	return objects, nodes
}

// grainDeployment is the one document the three assertions below are
// really about.
func grainDeployment(t *testing.T) k8sDeployment {
	t.Helper()
	objects, nodes := manifestDocs(t)
	return grainDeploymentFromDocs(t, objects, nodes)
}

// grainDeploymentFromDocs is grainDeployment's own decode step, taking
// manifestDocs' output directly so a caller reading a different file
// (manifestDocsForFile) does not need a second copy of it.
func grainDeploymentFromDocs(t *testing.T, objects []k8sObject, nodes []*yaml.Node) k8sDeployment {
	t.Helper()
	for i, obj := range objects {
		if obj.Kind != "Deployment" {
			continue
		}
		var dep k8sDeployment
		if err := nodes[i].Decode(&dep); err != nil {
			t.Fatalf("decoding the Deployment: %v", err)
		}
		return dep
	}
	t.Fatal("manifest ships no Deployment")
	return k8sDeployment{}
}

// grainContainer is the daemon's own container, found by the image it
// runs rather than by position: an initContainer added in front of the
// existing one must not silently become what these assertions are about.
func grainContainer(t *testing.T, dep k8sDeployment) k8sContainer {
	t.Helper()
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("the Deployment runs %d containers, and this file assumes the daemon is the only one", len(containers))
	}
	return containers[0]
}

// daemonArgs reads the container's args the way the daemon's own flag
// package does: a token starting with "-" is a flag, and the value is
// whatever follows the "=" in it. Every argument in this manifest is
// written in the attached form, which is the only form a single YAML
// sequence item can carry, so a separated value is a manifest bug rather
// than a shape this has to understand.
func daemonArgs(t *testing.T, c k8sContainer) map[string]string {
	t.Helper()
	flags := map[string]string{}
	for i, arg := range c.Args {
		if i == 0 {
			if arg != "daemon" {
				t.Errorf("the container's first argument is %q, not the `daemon` subcommand", arg)
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			t.Errorf("argument %q is not a flag, and `grain daemon` takes no positional arguments", arg)
			continue
		}
		name, value, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		flags[name] = value
	}
	return flags
}

// flagValue is one flag the manifest must pass, with its value.
func flagValue(t *testing.T, flags map[string]string, name string) string {
	t.Helper()
	value, ok := flags[name]
	if !ok {
		t.Fatalf("deploy/kubernetes/grain.yaml passes no -%s", name)
	}
	if value == "" {
		t.Fatalf("deploy/kubernetes/grain.yaml passes -%s with no value", name)
	}
	return value
}

// The kinds this file ships, in the order it ships them -- which is
// dependency order, and is what makes `kubectl apply -f grain.yaml` work
// as one command against a cluster that has none of it yet.
//
// The list is short enough to write down and stable enough to be worth
// writing down: it is the header's own claim ("namespace, identity and
// permissions, the priorities sandbox pods run at, storage, the key upgrades are verified against, the
// deployment itself, a Service in front of its UI, and a Service in
// front of what its sandboxes reach"), and a document deleted by a bad merge is otherwise a
// deployment that is missing exactly one of them.
func TestTheKubernetesManifestShipsTheObjectsItsHeaderClaims(t *testing.T) {
	objects, _ := manifestDocs(t)
	want := []string{
		"Namespace",
		"ServiceAccount",
		"Role",
		"RoleBinding",
		"ClusterRole",
		"ClusterRoleBinding",
		"ClusterRole",
		"ClusterRoleBinding",
		"PriorityClass",
		"PriorityClass",
		"PriorityClass",
		"PriorityClass",
		"PersistentVolumeClaim",
		"ConfigMap",
		"Deployment",
		"Service",
		"Service",
	}
	var got []string
	for _, obj := range objects {
		got = append(got, obj.Kind)
		if obj.Metadata.Name == "" {
			t.Errorf("a %s is shipped with no metadata.name", obj.Kind)
		}
		if obj.APIVersion == "" {
			t.Errorf("%s/%s is shipped with no apiVersion", obj.Kind, obj.Metadata.Name)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("grain.yaml ships %v; the header says it ships %v. If that is the intended change, say so in "+
			"the header comment and here -- an operator diffing this file against what is running has nothing "+
			"else to go on", got, want)
	}
}

// clusterScoped is the kinds these manifests ship that live in no
// namespace at all: the Namespace itself, and the read-only node grant
// the sandbox budget needs (grain-node-reader).
var clusterScoped = map[string]bool{"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true, "PriorityClass": true}

// Every object in one namespace, and that namespace the one this file
// creates. A Role in a namespace the RoleBinding is not in binds
// nothing, and applies without complaint.
func TestTheKubernetesManifestPutsEveryObjectInTheNamespaceItCreates(t *testing.T) {
	objects, _ := manifestDocs(t)
	namespace := ""
	for _, obj := range objects {
		if obj.Kind == "Namespace" {
			namespace = obj.Metadata.Name
		}
	}
	if namespace == "" {
		t.Fatal("grain.yaml creates no Namespace")
	}
	for _, obj := range objects {
		if clusterScoped[obj.Kind] {
			if obj.Metadata.Namespace != "" {
				t.Errorf("%s/%s is cluster-scoped and names namespace %q", obj.Kind, obj.Metadata.Name, obj.Metadata.Namespace)
			}
			continue
		}
		if obj.Metadata.Namespace != namespace {
			t.Errorf("%s/%s is in namespace %q, and this file creates %q -- applying it would either fail or "+
				"install half of grain somewhere else", obj.Kind, obj.Metadata.Name, obj.Metadata.Namespace, namespace)
		}
	}
}

// The first of the three agreements that are only correct together
// (grain/task-209): the uid the daemon runs as, and the gid the kubelet
// hands its volume to.
//
// fsGroup is what makes -data-dir writable to a process that owns none
// of it. They were chosen as one number and they have to move as one:
// an edit that raises runAsUser for a cluster whose SCCs assign uids,
// and leaves fsGroup where it was, is a pod that starts, passes its
// probes, and cannot write its own SQLite store.
func TestTheKubernetesDeploymentRunsAsANonRootUidThatOwnsItsVolume(t *testing.T) {
	sc := grainDeployment(t).Spec.Template.Spec.SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("the pod's securityContext does not say runAsNonRoot: true. The image declares no USER of its " +
			"own on purpose (scripts/setup.sh passes --user instead), so a manifest that says nothing about " +
			"uids runs the daemon -- and the agent CLIs beside it -- as root over the image they were started from")
	}
	if sc.RunAsUser == nil {
		t.Fatal("the pod's securityContext sets no runAsUser, so runAsNonRoot has nothing to be true of")
	}
	if *sc.RunAsUser == 0 {
		t.Errorf("runAsUser is %d, which is root", *sc.RunAsUser)
	}
	if sc.RunAsGroup == nil {
		t.Error("the pod's securityContext sets no runAsGroup")
	}
	if sc.FSGroup == nil {
		t.Fatal("the pod's securityContext sets no fsGroup, so nothing gives the uid above a writable /data")
	}
	if *sc.FSGroup != *sc.RunAsUser {
		t.Errorf("fsGroup is %d and runAsUser is %d. These are one number, not two: fsGroup is what makes the "+
			"data volume writable to the uid the daemon runs as, so a pod with them apart comes up, passes its "+
			"probes and cannot write its own store. Move both or neither", *sc.FSGroup, *sc.RunAsUser)
	}
}

// TestTheKubernetesContainerKeepsTheCapabilityItsOwnImageNeeds is the
// other cross-reference a securityContext edit can break silently, and
// the one this suite's own header said nothing caught: the Dockerfile
// setcaps /usr/local/bin/grain with CAP_NET_BIND_SERVICE so the package
// cache can bind 443/80 (the same reason pkg/hostinit's own ensureSetcap
// exists for the host platforms), and a container whose securityContext
// drops every capability without adding that one back does not merely
// lose the capability -- the kernel refuses to exec a binary carrying a
// file capability the bounding set cannot satisfy, so the pod never
// starts at all. This was found by actually bringing this manifest up
// on a real cluster while building -platform=k8s-container
// (pkg/hostinit/k8s.go): every pod failed with "[FATAL tini (12)] exec
// /usr/local/bin/grain failed: Operation not permitted", invisible to
// every other test in this file because none of them starts the
// container the manifest describes.
func TestTheKubernetesContainerKeepsTheCapabilityItsOwnImageNeeds(t *testing.T) {
	if !strings.Contains(read(t, "Dockerfile"), "setcap cap_net_bind_service=+ep /usr/local/bin/grain") {
		t.Fatal("the Dockerfile no longer setcaps /usr/local/bin/grain with cap_net_bind_service -- if that line " +
			"moved or was renamed, this test and the one it guards (NET_BIND_SERVICE below) should move with it")
	}
	caps := grainContainer(t, grainDeployment(t)).SecurityContext.Capabilities
	if !slices.Contains(caps.Drop, "ALL") {
		t.Error("the grain container's securityContext no longer drops ALL capabilities")
	}
	if !slices.Contains(caps.Add, "NET_BIND_SERVICE") {
		t.Fatal("the grain container's securityContext does not add back NET_BIND_SERVICE: the image's own " +
			"/usr/local/bin/grain carries that file capability (see the Dockerfile's setcap), and the kernel " +
			"refuses to exec a binary with a file capability the container's bounding set cannot satisfy -- " +
			"this is not \"the daemon can't bind 443\", it is a pod that never starts")
	}
}

// mkdirPath and chmodPath read the initContainer's one-line shell
// script. It is a fixed `sh -c` string rather than anything general, so
// these do not have to be: what is wanted is the directory it makes.
var (
	mkdirPath = regexp.MustCompile(`mkdir -p (\S+)`)
	chmodPath = regexp.MustCompile(`chmod [0-7]+ (\S+)`)
)

// The second and third agreements: $HOME is the directory the
// initContainer creates, that directory is on the data volume, and the
// data volume is the one -data-dir names.
//
// Kubernetes sets no HOME and there is no passwd entry for the uid
// above to take one from, so without this the daemon inherits HOME=/ --
// and claude runs on the controller rather than in a sandbox and keeps
// its state under the controller's $HOME, so the first dispatch fails
// writing to a filesystem it does not own. Both halves are one
// decision written in two places a screen apart: an edit that moves
// HOME and leaves the mkdir, or that moves the mkdir off the volume,
// fails at the first dispatch rather than at apply time.
func TestTheKubernetesDeploymentsHomeIsTheDirectoryItsInitContainerCreates(t *testing.T) {
	dep := grainDeployment(t)
	pod := dep.Spec.Template.Spec
	c := grainContainer(t, dep)

	if len(pod.InitContainers) != 1 {
		t.Fatalf("the pod runs %d initContainers, and this file assumes the one that makes $HOME", len(pod.InitContainers))
	}
	init := pod.InitContainers[0]
	script := strings.Join(init.Command, " ")
	made := mkdirPath.FindAllStringSubmatch(script, -1)
	if len(made) != 1 {
		t.Fatalf("the %s initContainer's command makes %d directories (%q), and this file assumes the one "+
			"that is $HOME", init.Name, len(made), script)
	}
	home := made[0][1]
	for _, m := range chmodPath.FindAllStringSubmatch(script, -1) {
		if m[1] != home {
			t.Errorf("the initContainer makes %s and chmods %s. A mode applied to some other directory leaves "+
				"$HOME -- which is where claude's credential is written -- at whatever the volume's default is", home, m[1])
		}
	}

	var env string
	for _, e := range c.Env {
		if e.Name == "HOME" {
			env = e.Value
		}
	}
	if env == "" {
		t.Fatalf("the %s container names no HOME. Kubernetes sets none and the uid has no passwd entry, so the "+
			"daemon would inherit HOME=/ and the first claude dispatch would fail writing to it", c.Name)
	}
	if env != home {
		t.Errorf("HOME is %q and the initContainer makes %q. The daemon's home has to be the directory something "+
			"created and made writable, or the first dispatch is the thing that finds out", env, home)
	}

	// On the volume, not in a pod filesystem a rollout throws away --
	// and on the *same* volume the daemon keeps everything else on, so
	// that a mkdir in the initContainer is a mkdir the daemon can see.
	mount := ""
	for _, m := range c.VolumeMounts {
		if under(home, m.MountPath) {
			mount = m.Name
			break
		}
	}
	if mount == "" {
		t.Fatalf("HOME (%s) is under none of the %s container's volumeMounts (%v), so it is a directory in the "+
			"pod filesystem that every rollout discards", home, c.Name, c.VolumeMounts)
	}
	initMounted := false
	for _, m := range init.VolumeMounts {
		if m.Name == mount && under(home, m.MountPath) {
			initMounted = true
		}
	}
	if !initMounted {
		t.Errorf("the initContainer does not mount %q where HOME (%s) is, so its mkdir lands in its own "+
			"throwaway filesystem and the daemon starts with a $HOME that does not exist", mount, home)
	}

	claim := ""
	for _, v := range pod.Volumes {
		if v.Name == mount {
			claim = v.PersistentVolumeClaim.ClaimName
		}
	}
	if claim == "" {
		t.Fatalf("volume %q is not a persistentVolumeClaim, and grain's store is not state a pod may lose", mount)
	}
	objects, _ := manifestDocs(t)
	found := false
	for _, obj := range objects {
		if obj.Kind == "PersistentVolumeClaim" && obj.Metadata.Name == claim {
			found = true
		}
	}
	if !found {
		t.Errorf("the pod claims %q and this file ships no PersistentVolumeClaim by that name", claim)
	}

	// And the whole of it under -data-dir, which is the directory the
	// daemon is told its state lives in.
	dataDir := flagValue(t, daemonArgs(t, c), "data-dir")
	if !under(home, dataDir) {
		t.Errorf("HOME is %s and -data-dir is %s. The point of putting $HOME under the data directory is that "+
			"claude's state and kubectl's discovery cache live on the same disk as everything else this "+
			"deployment keeps (scripts/setup.sh does the same thing for the single-node deployment)", home, dataDir)
	}
}

// The flags that name this manifest's own objects back to the daemon
// running inside it. pkg/upgrade's Kubernetes path repoints exactly
// these: it patches .spec.template's image on the Deployment named by
// -k8s-deployment, in the namespace named by -k8s-namespace, for the
// container named by -k8s-container. A typo in any of the three is an
// Upgrade button that health-checks the new image, reports its way
// through every step, and fails at the last one -- which is the most
// expensive place in this deployment to find a typo.
func TestTheKubernetesDeploymentNamesItselfInTheFlagsAnUpgradeRepoints(t *testing.T) {
	dep := grainDeployment(t)
	c := grainContainer(t, dep)
	flags := daemonArgs(t, c)

	if _, ok := flags["k8s-sandboxes"]; !ok {
		t.Error("the container does not pass -k8s-sandboxes, which is what selects the Kubernetes backend and " +
			"what tells the daemon it has no host of its own")
	}
	if got := flagValue(t, flags, "k8s-namespace"); got != dep.Metadata.Namespace {
		t.Errorf("-k8s-namespace is %q and this Deployment is in %q. Sandbox pods would be created somewhere "+
			"this pod's Role grants it nothing", got, dep.Metadata.Namespace)
	}
	if got := flagValue(t, flags, "k8s-deployment"); got != dep.Metadata.Name {
		t.Errorf("-k8s-deployment is %q and this Deployment is named %q, so an upgrade would patch a Deployment "+
			"that does not exist", got, dep.Metadata.Name)
	}
	if got := flagValue(t, flags, "k8s-container"); got != c.Name {
		t.Errorf("-k8s-container is %q and this container is named %q, so an upgrade would repoint no container",
			got, c.Name)
	}

	// -upgrade-image is the repository an upgrade pulls the new tag
	// from, and it has no tag of its own. It has to be the repository
	// this pod is already running, or the first upgrade quietly moves
	// the deployment onto some other build's image.
	repository, _, _ := strings.Cut(c.Image, ":")
	if got := flagValue(t, flags, "upgrade-image"); got != repository {
		t.Errorf("-upgrade-image is %q and this container runs %q. An upgrade pulls a tag of the former and "+
			"writes it into this container's image", got, c.Image)
	}
	// The initContainer runs the same image only so the pod pulls one
	// rather than two, and the manifest says a deployment pinning an
	// immutable sha- tag above may leave this one on the branch tag --
	// so what has to agree is the repository, not the tag.
	initRepository, _, _ := strings.Cut(dep.Spec.Template.Spec.InitContainers[0].Image, ":")
	if initRepository != repository {
		t.Errorf("the initContainer runs %q and the daemon runs %q. The initContainer runs this image only so "+
			"that this pod pulls one image rather than two, which a second repository undoes", initRepository, repository)
	}
}

// The two things this pod cannot be told any other way, and the two
// variables the daemon reads them out of.
//
// Both are downward-API fieldRefs because both are assigned after this
// file was written: the address at scheduling time (which is what makes
// the git proxy reachable from a sandbox pod) and the name when the
// ReplicaSet creates the pod (which is how the daemon asks the kubelet
// how many times it has restarted this container -- this shape's answer
// to "is this deployment restarting rather than running?", grain/
// task-367). A variable dropped from here is not a failure: it is a
// deployment that quietly stops reporting one of them, which is what
// this asserts against.
func TestTheKubernetesDeploymentTellsTheDaemonItsOwnAddressAndName(t *testing.T) {
	c := grainContainer(t, grainDeployment(t))
	daemonSource := read(t, "pkg", "daemon", "sandbox_backends.go")

	for variable, field := range map[string]string{
		"GRAIN_POD_IP":   "status.podIP",
		"GRAIN_POD_NAME": "metadata.name",
	} {
		var found bool
		for _, e := range c.Env {
			if e.Name != variable {
				continue
			}
			found = true
			if got := e.ValueFrom.FieldRef.FieldPath; got != field {
				t.Errorf("%s comes from %q, want the downward API's %s -- a literal value here is one that is "+
					"wrong on every pod but the one it was copied from", variable, got, field)
			}
		}
		if !found {
			t.Errorf("the %s container is given no %s. Nothing else tells a pod its own %s, and the daemon "+
				"reads it from the environment (pkg/daemon/sandbox_backends.go)", c.Name, variable, field)
		}
		// The spelling is the whole agreement: an environment variable
		// renamed at one end is read by nothing at the other, and
		// nothing fails until a deployment is running.
		if !strings.Contains(daemonSource, `"`+variable+`"`) {
			t.Errorf("pkg/daemon/sandbox_backends.go names no %s, so this manifest sets a variable nothing reads", variable)
		}
	}
}

// The values an operator has to edit are still the ones that make an
// unedited apply fail on something obvious.
//
// This is the assertion against a particular accident rather than
// against a design: standing this up against a real cluster means
// filling these in, and a manifest committed with somebody's own
// repository in it is a file the next operator applies and does not
// read. CHANGEME is what the header promises and what
// deploy/kubernetes/README.md tells them to look for.
func TestTheKubernetesManifestStillShipsItsCHANGEMEValues(t *testing.T) {
	dep := grainDeployment(t)
	flags := daemonArgs(t, grainContainer(t, dep))
	if got := flagValue(t, flags, "default-target-repo"); got != "CHANGEME/CHANGEME" {
		t.Errorf("-default-target-repo is %q. This file ships with CHANGEME there on purpose -- an unedited "+
			"apply should fail on something obvious rather than file this deployment's tasks against whichever "+
			"repository was being tested against when it was committed", got)
	}
}

// The Namespace's Pod Security enforce level, and the flag that is the
// one thing in this file it can contradict.
//
// RBAC lets the daemon create pods and cannot say anything about what is
// in them; the enforce label is what refuses a privileged, host-mounting
// pod at admission, and a namespace without one is admitted under the
// cluster's default, which is commonly nothing at all (#2414). And a
// manifest passing -k8s-sandbox-privileged under an enforce level that
// refuses privileged pods is a deployment whose every dispatch fails at
// pod creation -- so the two have to be changed together or not at all.
func TestTheKubernetesNamespaceEnforcesAPodSecurityLevelItsSandboxesMeet(t *testing.T) {
	objects, nodes := manifestDocs(t)
	var labels map[string]string
	for i, obj := range objects {
		if obj.Kind != "Namespace" {
			continue
		}
		var ns struct {
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
		}
		if err := nodes[i].Decode(&ns); err != nil {
			t.Fatalf("decoding the Namespace: %v", err)
		}
		labels = ns.Metadata.Labels
	}
	enforce := labels["pod-security.kubernetes.io/enforce"]
	switch enforce {
	case "baseline", "restricted":
	case "":
		t.Fatal("the grain Namespace carries no pod-security.kubernetes.io/enforce label, so nothing at " +
			"admission stops the daemon's own ServiceAccount creating a privileged or hostPath pod")
	default:
		t.Errorf("the grain Namespace enforces Pod Security level %q; want baseline or restricted", enforce)
	}

	flags := daemonArgs(t, grainContainer(t, grainDeployment(t)))
	if _, ok := flags["k8s-sandbox-privileged"]; ok {
		t.Errorf("the container passes -k8s-sandbox-privileged and the Namespace enforces %q, which refuses "+
			"every privileged sandbox pod at admission", enforce)
	}
}

// The Role's Deployment writes name the Deployment an upgrade repoints,
// and only that one. A rename of either side alone is an Upgrade button
// refused at its last step, after it has already reported the candidate
// healthy.
func TestTheKubernetesRolePinsDeploymentWritesToTheDeploymentAnUpgradeRepoints(t *testing.T) {
	objects, nodes := manifestDocs(t)
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
	deployment := flagValue(t, daemonArgs(t, grainContainer(t, grainDeployment(t))), "k8s-deployment")

	var pinned bool
	for _, rule := range role.Rules {
		if !slices.Contains(rule.Resources, "deployments") {
			continue
		}
		if !slices.Contains(rule.Verbs, "patch") && !slices.Contains(rule.Verbs, "update") {
			continue
		}
		if !slices.Equal(rule.ResourceNames, []string{deployment}) {
			t.Errorf("a Role rule grants %v on deployments %v; want it pinned to resourceNames [%s], the "+
				"Deployment -k8s-deployment names", rule.Verbs, rule.ResourceNames, deployment)
			continue
		}
		pinned = true
	}
	if !pinned {
		t.Errorf("no Role rule grants patch/update on deployment %q, so an upgrade cannot repoint it", deployment)
	}
}

// k8sService is as much of a Service as the sandbox Service's agreements
// need.
type k8sService struct {
	k8sObject `yaml:",inline"`
	Spec      struct {
		Type      string            `yaml:"type"`
		ClusterIP string            `yaml:"clusterIP"`
		Selector  map[string]string `yaml:"selector"`
		Ports     []struct {
			Name       string `yaml:"name"`
			Port       int    `yaml:"port"`
			TargetPort any    `yaml:"targetPort"`
		} `yaml:"ports"`
	} `yaml:"spec"`
}

// The Service sandboxes reach the git proxy and the model proxies
// through, and the three things it has to agree with: the flag that
// names it, the port block those services bind in, and the daemon pod's
// own labels.
//
// This is what lets a run the next daemon pod adopts after a rollout
// (design/design/non-disruptive-upgrade.design.md, Stage 2b) still push its pull
// request. A sandbox is handed its URLs once, when it is created, so
// they have to name something that outlives the pod that created it --
// the Service's cluster IP, on ports the -sandbox-port-base block fixes.
// Each way this can drift is a deployment that works until the first
// rollout: a port missing from the Service, a block moved without the
// Service following it, a selector that matches no daemon pod (or
// matches a run pod), a headless Service that hands back the pod's own
// address after all.
func TestTheKubernetesSandboxServiceForwardsWhatSandboxesAreHandedOut(t *testing.T) {
	objects, nodes := manifestDocs(t)
	dep := grainDeploymentFromDocs(t, objects, nodes)
	flags := daemonArgs(t, grainContainer(t, dep))
	name := flagValue(t, flags, "k8s-sandbox-service")
	base, err := strconv.Atoi(flagValue(t, flags, "sandbox-port-base"))
	if err != nil {
		t.Fatalf("-sandbox-port-base is not a number: %v", err)
	}
	if base == 0 {
		t.Fatal("-sandbox-port-base=0 puts the git proxy on an ephemeral port no Service can forward")
	}

	var svc *k8sService
	for i, obj := range objects {
		if obj.Kind == "Service" && obj.Metadata.Name == name {
			svc = &k8sService{}
			if err := nodes[i].Decode(svc); err != nil {
				t.Fatalf("decoding Service %s: %v", name, err)
			}
		}
	}
	if svc == nil {
		t.Fatalf("-k8s-sandbox-service=%s names a Service grain.yaml does not ship -- the daemon refuses to "+
			"start when it cannot resolve it", name)
	}
	if svc.Spec.Type != "" && svc.Spec.Type != "ClusterIP" {
		t.Errorf("Service %s is a %s; sandboxes need it reachable inside the cluster and nowhere else", name, svc.Spec.Type)
	}
	if svc.Spec.ClusterIP == "None" {
		t.Errorf("Service %s is headless, which resolves to the daemon pod's own address -- the address a "+
			"sandbox loses at the next rollout", name)
	}

	// The selector has to match the daemon pod, and only the daemon pod:
	// run pods share the namespace, and a run pod answering on the git
	// proxy's port is a push that goes nowhere.
	var template struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Labels map[string]string `yaml:"labels"`
				} `yaml:"metadata"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	for i, obj := range objects {
		if obj.Kind == "Deployment" {
			if err := nodes[i].Decode(&template); err != nil {
				t.Fatalf("decoding the Deployment's template labels: %v", err)
			}
		}
	}
	podLabels := template.Spec.Template.Metadata.Labels
	if len(svc.Spec.Selector) == 0 {
		t.Errorf("Service %s selects nothing in particular", name)
	}
	for k, v := range svc.Spec.Selector {
		if podLabels[k] != v {
			t.Errorf("Service %s selects %s=%s, which the daemon pod's template does not carry (%v) -- it would "+
				"forward to nothing", name, k, v, podLabels)
		}
	}
	if svc.Spec.Selector[k8s.SandboxLabel] != runhost.KubeDaemonComponent {
		t.Errorf("Service %s does not select %s=%s, so nothing stops it matching a run pod or an upgrade's "+
			"check pod named grain as well", name, k8s.SandboxLabel, runhost.KubeDaemonComponent)
	}

	// Every service the daemon hands out at this Service's address, on
	// the port the block gives it, forwarded to the same port on the pod.
	offsets, err := daemon.SandboxPortOffsets()
	if err != nil {
		t.Fatalf("SandboxPortOffsets: %v", err)
	}
	forwarded := map[int]string{}
	for _, p := range svc.Spec.Ports {
		target := fmt.Sprint(p.TargetPort)
		if p.TargetPort == nil {
			target = strconv.Itoa(p.Port)
		}
		forwarded[p.Port] = target
	}
	for _, service := range daemon.K8sServiceFrontedNames() {
		offset, ok := offsets[service]
		if !ok {
			t.Errorf("%s has no offset in the port block", service)
			continue
		}
		port := base + offset
		target, ok := forwarded[port]
		if !ok {
			t.Errorf("Service %s forwards no port %d, where the daemon serves the %s and tells every sandbox "+
				"to reach it", name, port, service)
			continue
		}
		if target != strconv.Itoa(port) {
			t.Errorf("Service %s forwards port %d to %s, but the %s listens on %d", name, port, target, service, port)
		}
	}
}

// The node grant binds the ServiceAccount the daemon actually runs as,
// in the namespace it actually runs in, to a ClusterRole that can list
// nodes. Each half applies cleanly without the other, and either one
// renamed alone is a daemon whose `kubectl get nodes` is Forbidden: its
// sandbox budget is unknown and every queued CI job starts at once.
func TestTheKubernetesManifestsLetTheDaemonListNodes(t *testing.T) {
	for _, file := range []string{"grain.yaml", "grain-granule.yaml"} {
		t.Run(file, func(t *testing.T) {
			objects, nodes := manifestDocsForFile(t, file)
			roles := map[string]bool{}
			type binding struct {
				RoleRef struct {
					Kind string `yaml:"kind"`
					Name string `yaml:"name"`
				} `yaml:"roleRef"`
				Subjects []struct {
					Kind      string `yaml:"kind"`
					Name      string `yaml:"name"`
					Namespace string `yaml:"namespace"`
				} `yaml:"subjects"`
			}
			var bindings []binding
			account, namespace := "", ""
			for i, obj := range objects {
				switch obj.Kind {
				case "ClusterRole":
					var role struct {
						Rules []struct {
							APIGroups []string `yaml:"apiGroups"`
							Resources []string `yaml:"resources"`
							Verbs     []string `yaml:"verbs"`
						} `yaml:"rules"`
					}
					if err := nodes[i].Decode(&role); err != nil {
						t.Fatalf("decoding ClusterRole %s: %v", obj.Metadata.Name, err)
					}
					for _, rule := range role.Rules {
						if slices.Contains(rule.APIGroups, "") && slices.Contains(rule.Resources, "nodes") &&
							slices.Contains(rule.Verbs, "get") && slices.Contains(rule.Verbs, "list") {
							roles[obj.Metadata.Name] = true
						}
					}
				case "ClusterRoleBinding":
					var b binding
					if err := nodes[i].Decode(&b); err != nil {
						t.Fatalf("decoding ClusterRoleBinding %s: %v", obj.Metadata.Name, err)
					}
					bindings = append(bindings, b)
				case "Deployment":
					var dep struct {
						Spec struct {
							Template struct {
								Spec struct {
									ServiceAccountName string `yaml:"serviceAccountName"`
								} `yaml:"spec"`
							} `yaml:"template"`
						} `yaml:"spec"`
					}
					if err := nodes[i].Decode(&dep); err != nil {
						t.Fatalf("decoding the Deployment: %v", err)
					}
					account, namespace = dep.Spec.Template.Spec.ServiceAccountName, obj.Metadata.Namespace
				}
			}
			if account == "" {
				t.Fatalf("%s's Deployment names no serviceAccountName", file)
			}
			for _, b := range bindings {
				if b.RoleRef.Kind != "ClusterRole" || !roles[b.RoleRef.Name] {
					continue
				}
				for _, s := range b.Subjects {
					if s.Kind == "ServiceAccount" && s.Name == account && s.Namespace == namespace {
						return
					}
				}
			}
			t.Errorf("%s binds no ClusterRole granting get/list on nodes to ServiceAccount %s/%s", file, namespace, account)
		})
	}
}

// The PriorityClasses both manifests ship are the ones the daemon names by
// default (orchestrator.DefaultK8sPriorityClasses), in the order that lets
// an interactive session's pod preempt a CI job's and never the other way
// round -- and none of them changes the priority of anything else in the
// cluster.
func TestTheKubernetesManifestsShipTheSandboxPriorityClasses(t *testing.T) {
	type priorityClass struct {
		k8sObject        `yaml:",inline"`
		Value            int64  `yaml:"value"`
		GlobalDefault    *bool  `yaml:"globalDefault"`
		PreemptionPolicy string `yaml:"preemptionPolicy"`
		Description      string `yaml:"description"`
	}
	for _, file := range []string{"grain.yaml", "grain-granule.yaml"} {
		t.Run(file, func(t *testing.T) {
			objects, nodes := manifestDocsForFile(t, file)
			got := map[string]priorityClass{}
			for i, obj := range objects {
				if obj.Kind != "PriorityClass" {
					continue
				}
				var pc priorityClass
				if err := nodes[i].Decode(&pc); err != nil {
					t.Fatalf("decoding PriorityClass %s: %v", obj.Metadata.Name, err)
				}
				if pc.GlobalDefault == nil || *pc.GlobalDefault {
					t.Errorf("PriorityClass %s must say globalDefault: false", pc.Metadata.Name)
				}
				got[pc.Metadata.Name] = pc
			}
			interactive, agent, ci := got[k8s.PriorityClassInteractive], got[k8s.PriorityClassAgent], got[k8s.PriorityClassCI]
			for name, pc := range map[string]priorityClass{
				k8s.PriorityClassInteractive: interactive, k8s.PriorityClassAgent: agent, k8s.PriorityClassCI: ci,
			} {
				if pc.Metadata.Name == "" {
					t.Errorf("%s ships no PriorityClass %s, which sandbox pods name by default", file, name)
				}
			}
			// The daemon creates these from k8s.SandboxPriorityClasses when
			// they are missing, so what it creates and what applying this
			// file creates have to be the same objects.
			for _, spec := range k8s.SandboxPriorityClasses {
				pc := got[spec.Name]
				if pc.Value != int64(spec.Value) || pc.PreemptionPolicy != spec.PreemptionPolicy || pc.Description != spec.Description {
					t.Errorf("%s: PriorityClass %s is value=%d preemptionPolicy=%q description=%q; k8s.SandboxPriorityClasses, "+
						"which the daemon creates it from, says value=%d preemptionPolicy=%q description=%q",
						file, spec.Name, pc.Value, pc.PreemptionPolicy, pc.Description, spec.Value, spec.PreemptionPolicy, spec.Description)
				}
			}
			if !(interactive.Value > agent.Value && agent.Value > ci.Value) {
				t.Errorf("priorities interactive=%d agent=%d ci=%d; want interactive > agent > ci",
					interactive.Value, agent.Value, ci.Value)
			}
			if interactive.PreemptionPolicy != "PreemptLowerPriority" {
				t.Errorf("%s preemptionPolicy = %q, want PreemptLowerPriority", k8s.PriorityClassInteractive, interactive.PreemptionPolicy)
			}
			if ci.PreemptionPolicy != "Never" {
				t.Errorf("%s preemptionPolicy = %q, want Never", k8s.PriorityClassCI, ci.PreemptionPolicy)
			}
			// The daemon's own pod must outrank every sandbox class: a
			// grain-interactive pod preempts anything below it, and a pod
			// with no class runs at 0, below even grain-ci.
			daemon := got["grain-daemon"]
			if daemon.Metadata.Name == "" || daemon.Value <= interactive.Value {
				t.Errorf("%s: PriorityClass grain-daemon value=%d; want it shipped, above grain-interactive's %d",
					file, daemon.Value, interactive.Value)
			}
			if daemon.PreemptionPolicy != "Never" {
				t.Errorf("grain-daemon preemptionPolicy = %q, want Never", daemon.PreemptionPolicy)
			}
			if pc := grainDeploymentFromDocs(t, objects, nodes).Spec.Template.Spec.PriorityClassName; pc != "grain-daemon" {
				t.Errorf("%s: the daemon's Deployment runs at priorityClassName %q, want grain-daemon so no sandbox can preempt it", file, pc)
			}
		})
	}
}
