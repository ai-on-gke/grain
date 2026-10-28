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

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
)

// decodeManifest renders a pod and unmarshals it back, so a test can
// assert on what the API server would actually be sent rather than on a
// substring of JSON.
func decodeManifest(t *testing.T, c Client, name string, shape PodShape, opts PodOptions) map[string]any {
	t.Helper()
	raw, err := c.Manifest(name, shape, opts)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshalling the manifest: %v", err)
	}
	return out
}

func TestManifestSizesAndLabelsAPod(t *testing.T) {
	pod := decodeManifest(t, Client{Namespace: "grain"}, "grain-sandbox-7-1",
		PodShape{CPUs: 4, MemoryMB: 8192, DiskGB: 30},
		PodOptions{Image: "example/sandbox:latest", Labels: map[string]string{"grain.dev/network": "none"}})

	meta := pod["metadata"].(map[string]any)
	if got := meta["namespace"]; got != "grain" {
		t.Errorf("namespace = %v, want grain", got)
	}
	labels := meta["labels"].(map[string]any)
	// The caller's own label survives, and the two this package sets
	// itself are there regardless -- reaping matches on both.
	for key, want := range map[string]string{
		"grain.dev/network": "none",
		ManagedByLabel:      ManagedByValue,
		SandboxLabel:        SandboxValue,
	} {
		if got := labels[key]; got != want {
			t.Errorf("label %s = %v, want %q", key, got, want)
		}
	}

	spec := pod["spec"].(map[string]any)
	if got := spec["restartPolicy"]; got != "Never" {
		t.Errorf("restartPolicy = %v, want Never: a restarted sandbox is one whose run silently lost its filesystem", got)
	}
	if got := spec["automountServiceAccountToken"]; got != false {
		t.Errorf("automountServiceAccountToken = %v, want false: nothing in a sandbox should reach the API server", got)
	}

	container := spec["containers"].([]any)[0].(map[string]any)
	resources := container["resources"].(map[string]any)
	// Requests and limits are the same numbers when no overcommit is set.
	for _, section := range []string{"requests", "limits"} {
		got := resources[section].(map[string]any)
		want := map[string]any{"cpu": "4", "memory": "8192Mi", "ephemeral-storage": "30Gi"}
		for key, wantValue := range want {
			if got[key] != wantValue {
				t.Errorf("%s.%s = %v, want %v", section, key, got[key], wantValue)
			}
		}
	}
}

func TestManifestLabelsAPodWithItsInstance(t *testing.T) {
	labels := func(c Client) map[string]any {
		pod := decodeManifest(t, c, "grain-sandbox-7-1", PodShape{CPUs: 1, MemoryMB: 1024, DiskGB: 10},
			PodOptions{Image: "example/sandbox:latest", Labels: map[string]string{InstanceLabel: "spoofed"}})
		return pod["metadata"].(map[string]any)["labels"].(map[string]any)
	}
	// The client's own instance wins over a caller's label of the same
	// name: listing and reaping depend on it.
	if got := labels(Client{Instance: "a1b2c3d4"})[InstanceLabel]; got != "a1b2c3d4" {
		t.Errorf("label %s = %v, want a1b2c3d4", InstanceLabel, got)
	}
	if got, ok := labels(Client{})[InstanceLabel]; ok && got != "spoofed" {
		t.Errorf("label %s = %v with no instance configured, want only the caller's own", InstanceLabel, got)
	}
}

func TestListSandboxesSelectsItsInstanceAndUnlabelledPods(t *testing.T) {
	// A script of its own rather than fakeKubectl's: the two calls share
	// a verb, and are told apart by their selectors.
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\036' \"$*\" >> " + log + "\n" +
		"case \"$*\" in\n" +
		"  *'!" + InstanceLabel + "'*) printf 'grain-sandbox-old\\n' ;;\n" +
		"  *'" + InstanceLabel + "=a1b2c3d4'*) printf 'grain-sandbox-mine\\n' ;;\n" +
		"  *) printf 'grain-sandbox-mine\\ngrain-sandbox-theirs\\ngrain-sandbox-old\\n' ;;\n" +
		"esac\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}

	names, err := Client{Kubectl: kubectl, Instance: "a1b2c3d4"}.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if strings.Join(names, ",") != "grain-sandbox-mine,grain-sandbox-old" {
		t.Errorf("names = %v, want this instance's pod and the unlabelled one, never another instance's", names)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading the calls: %v", err)
	}
	calls := strings.Split(strings.TrimSuffix(string(data), "\x1e"), "\x1e")
	base := ManagedByLabel + "=" + ManagedByValue + "," + SandboxLabel + "=" + SandboxValue
	for i, want := range []string{base + "," + InstanceLabel + "=a1b2c3d4", base + ",!" + InstanceLabel} {
		if i >= len(calls) || !strings.Contains(calls[i], "-l "+want+" ") {
			t.Errorf("call %d = %q, want selector %q", i, calls, want)
		}
	}

	names, err = Client{Kubectl: kubectl}.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(names) != 3 {
		t.Errorf("names = %v with no instance configured, want every grain sandbox pod", names)
	}
}

func TestManifestScalesRequestsByOvercommitAndKeepsFullLimits(t *testing.T) {
	pod := decodeManifest(t, Client{Namespace: "grain"}, "grain-granule-774-1",
		PodShape{CPUs: 8, MemoryMB: 16384, DiskGB: 30, CPUOvercommit: 10, MemoryOvercommit: 10},
		PodOptions{
			Image: "example/granule:latest",
			Setup: &Setup{
				Script: "printf x >\"$1/f0\"",
				Files:  []SetupFile{{SubPath: "f0", MountPath: "/etc/pip.conf"}},
			},
		})

	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	initContainer := spec["initContainers"].([]any)[0].(map[string]any)

	for _, c := range []map[string]any{container, initContainer} {
		name := c["name"].(string)
		resources := c["resources"].(map[string]any)
		requests := resources["requests"].(map[string]any)
		limits := resources["limits"].(map[string]any)

		wantRequests := map[string]any{"cpu": "800m", "memory": "1638Mi", "ephemeral-storage": "30Gi"}
		for key, wantValue := range wantRequests {
			if requests[key] != wantValue {
				t.Errorf("%s requests.%s = %v, want %v", name, key, requests[key], wantValue)
			}
		}
		wantLimits := map[string]any{"cpu": "8", "memory": "16384Mi", "ephemeral-storage": "30Gi"}
		for key, wantValue := range wantLimits {
			if limits[key] != wantValue {
				t.Errorf("%s limits.%s = %v, want %v", name, key, limits[key], wantValue)
			}
		}
	}

	// Overcommit <= 1 leaves requests equal to limits.
	for _, ratio := range []float64{0, 1, 0.5, -2} {
		unscaled := decodeManifest(t, Client{}, "p",
			PodShape{CPUs: 8, MemoryMB: 16384, DiskGB: 30, CPUOvercommit: ratio, MemoryOvercommit: ratio},
			PodOptions{Image: "example/sandbox"})
		res := unscaled["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		req := res["requests"].(map[string]any)
		lim := res["limits"].(map[string]any)
		if req["cpu"] != "8" || req["memory"] != "16384Mi" || lim["cpu"] != "8" || lim["memory"] != "16384Mi" {
			t.Errorf("ratio %v: requests=%v limits=%v, want 8 and 16384Mi for both", ratio, req, lim)
		}
	}

	// Extreme overcommit clamps at 1m and 1Mi rather than 0.
	clamped := decodeManifest(t, Client{}, "p",
		PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 10, CPUOvercommit: 5000, MemoryOvercommit: 5000},
		PodOptions{Image: "example/sandbox"})
	clampedRes := clamped["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	clampedReq := clampedRes["requests"].(map[string]any)
	if clampedReq["cpu"] != "1m" || clampedReq["memory"] != "1Mi" {
		t.Errorf("clamped requests = %v, want cpu=1m and memory=1Mi", clampedReq)
	}
}

// A sandbox's disk is a thin overlay a run mostly never writes, so the
// ephemeral-storage request is what DiskOvercommit scales -- in MiB, so a
// 10x ratio on 40 GiB is 4096Mi rather than a rounded-off 4Gi -- while the
// limit stays the full disk the guest is promised.
func TestManifestScalesTheEphemeralStorageRequestByDiskOvercommit(t *testing.T) {
	resources := func(shape PodShape) (requests, limits map[string]any) {
		t.Helper()
		pod := decodeManifest(t, Client{}, "p", shape, PodOptions{Image: "example/sandbox"})
		res := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		return res["requests"].(map[string]any), res["limits"].(map[string]any)
	}
	for _, tc := range []struct {
		diskGB  int
		ratio   float64
		request string
	}{
		{40, 10, "4096Mi"},
		{40, 2, "20480Mi"},
		{30, 3, "10240Mi"},
		{1, 5000, "1Mi"},
		{40, 1, "40Gi"},
		{40, 0, "40Gi"},
		{40, 0.5, "40Gi"},
		{40, -2, "40Gi"},
	} {
		req, lim := resources(PodShape{CPUs: 2, MemoryMB: 4096, DiskGB: tc.diskGB, DiskOvercommit: tc.ratio})
		if req["ephemeral-storage"] != tc.request {
			t.Errorf("%d GiB at %vx: requests.ephemeral-storage = %v, want %s", tc.diskGB, tc.ratio, req["ephemeral-storage"], tc.request)
		}
		if want := fmt.Sprintf("%dGi", tc.diskGB); lim["ephemeral-storage"] != want {
			t.Errorf("%d GiB at %vx: limits.ephemeral-storage = %v, want the full %s", tc.diskGB, tc.ratio, lim["ephemeral-storage"], want)
		}
		// Disk overcommit is its own ratio: cpu and memory are untouched.
		if req["cpu"] != "2" || req["memory"] != "4096Mi" {
			t.Errorf("%d GiB at %vx: cpu/memory requests = %v/%v, want 2/4096Mi", tc.diskGB, tc.ratio, req["cpu"], req["memory"])
		}
	}
}

// TestManifestHostDevicesMountsAtTheSamePath is -platform=k8s-granule's
// entire reason for existing: a privileged pod needs /dev/kvm reachable
// at the same path a real cloud-hypervisor VM expects it, with no other
// device-plugin machinery to install first (gke-pod-example.yaml's own
// shape).
func TestManifestHostDevicesMountsAtTheSamePath(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/granule", Privileged: true, HostDevices: []string{"/dev/kvm"}})
	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)

	security := container["securityContext"].(map[string]any)
	if security["privileged"] != true {
		t.Errorf("securityContext.privileged = %v, want true", security["privileged"])
	}

	mounts := container["volumeMounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("volumeMounts = %v, want one entry", mounts)
	}
	mount := mounts[0].(map[string]any)
	if mount["mountPath"] != "/dev/kvm" {
		t.Errorf("mountPath = %v, want /dev/kvm", mount["mountPath"])
	}

	volumes := spec["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("volumes = %v, want one entry", volumes)
	}
	volume := volumes[0].(map[string]any)
	if volume["name"] != mount["name"] {
		t.Errorf("volume name %v does not match volumeMount name %v", volume["name"], mount["name"])
	}
	hostPath := volume["hostPath"].(map[string]any)
	if hostPath["path"] != "/dev/kvm" {
		t.Errorf("hostPath.path = %v, want /dev/kvm", hostPath["path"])
	}
	if hostPath["type"] != "CharDevice" {
		t.Errorf("hostPath.type = %v, want CharDevice: untyped, a node without the device gets an empty directory created at the path", hostPath["type"])
	}
}

// TestManifestReadinessExecWaitsOnTheCommandNotTheContainer is what makes
// `kubectl wait --for=condition=Ready` wait for a VM to actually boot
// rather than for the pod's single container to merely start.
func TestManifestReadinessExecWaitsOnTheCommandNotTheContainer(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/granule", ReadinessExec: []string{"granule", "ready", "-timeout", "0"}})
	container := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	probe, ok := container["readinessProbe"].(map[string]any)
	if !ok {
		t.Fatalf("no readinessProbe rendered")
	}
	cmd := probe["exec"].(map[string]any)["command"].([]any)
	want := []string{"granule", "ready", "-timeout", "0"}
	if len(cmd) != len(want) {
		t.Fatalf("command = %v, want %v", cmd, want)
	}
	for i, w := range want {
		if cmd[i] != w {
			t.Errorf("command[%d] = %v, want %v", i, cmd[i], w)
		}
	}
}

// TestManifestWithNoHostDevicesOrReadinessExecIsUnchanged guards against
// a regression where every sandbox pod on -platform=k8s-container, which
// sets neither, grows an empty "volumes"/"readinessProbe" key that was
// never there before.
func TestManifestWithNoHostDevicesOrReadinessExecIsUnchanged(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1}, PodOptions{Image: "example/sandbox"})
	spec := pod["spec"].(map[string]any)
	if _, ok := spec["volumes"]; ok {
		t.Errorf("spec has a volumes key with no HostDevices given: %v", spec["volumes"])
	}
	container := spec["containers"].([]any)[0].(map[string]any)
	if _, ok := container["readinessProbe"]; ok {
		t.Errorf("container has a readinessProbe with no ReadinessExec given: %v", container["readinessProbe"])
	}
	if _, ok := container["volumeMounts"]; ok {
		t.Errorf("container has a volumeMounts key with no HostDevices given: %v", container["volumeMounts"])
	}
	if _, ok := spec["initContainers"]; ok {
		t.Errorf("spec has initContainers with no Setup given: %v", spec["initContainers"])
	}
}

// A Setup is an init container from the same image, as the image's own
// user, whose files reach the sandbox container read-only -- the only
// way a file under /etc gets into a pod `kubectl exec` cannot be root in.
func TestManifestSetupRunsAnUnprivilegedInitContainerAndMountsItsFilesReadOnly(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 2, MemoryMB: 512, DiskGB: 4}, PodOptions{
		Image:      "example/sandbox",
		Privileged: true,
		Security:   PodSecurity{Container: SecurityContext{"allowPrivilegeEscalation": false}},
		Setup: &Setup{
			Script: "printf x >\"$1/f0\"",
			Args:   []string{"/etc/ssl/certs/ca-certificates.crt"},
			Files:  []SetupFile{{SubPath: "f0", MountPath: "/etc/pip.conf"}},
		},
	})
	spec := pod["spec"].(map[string]any)
	inits, _ := spec["initContainers"].([]any)
	if len(inits) != 1 {
		t.Fatalf("initContainers = %v, want one", spec["initContainers"])
	}
	init := inits[0].(map[string]any)
	if init["name"] != SetupContainer || init["image"] != "example/sandbox" {
		t.Errorf("init container = %v", init)
	}
	command, _ := json.Marshal(init["command"])
	if want := `["sh","-c","printf x \u003e\"$1/f0\"","sh","/grain-setup","/etc/ssl/certs/ca-certificates.crt"]`; string(command) != want {
		t.Errorf("init command = %s, want %s", command, want)
	}
	security, _ := init["securityContext"].(map[string]any)
	if security["privileged"] != nil || security["runAsUser"] != nil {
		t.Errorf("the init container asked for more than the sandbox's own constraints: %v", security)
	}
	if security["allowPrivilegeEscalation"] != false {
		t.Errorf("the init container dropped the sandbox's own securityContext: %v", security)
	}
	mounts, _ := json.Marshal(init["volumeMounts"])
	if want := `[{"mountPath":"/grain-setup","name":"grain-setup"}]`; string(mounts) != want {
		t.Errorf("init volumeMounts = %s, want %s", mounts, want)
	}

	container := spec["containers"].([]any)[0].(map[string]any)
	mounts, _ = json.Marshal(container["volumeMounts"])
	if want := `[{"mountPath":"/etc/pip.conf","name":"grain-setup","readOnly":true,"subPath":"f0"}]`; string(mounts) != want {
		t.Errorf("sandbox volumeMounts = %s, want %s", mounts, want)
	}
	volumes, _ := json.Marshal(spec["volumes"])
	if want := `[{"emptyDir":{},"name":"grain-setup"}]`; string(volumes) != want {
		t.Errorf("volumes = %s, want %s", volumes, want)
	}
}

// TestManifestNoCommandLeavesTheImagesOwnEntrypointAlone is what lets a
// granule pod's ENTRYPOINT (cloud-hypervisor's own boot, not a shell
// sitting still) run unmodified -- the opposite of every sandbox pod's
// DefaultCommand.
func TestManifestNoCommandLeavesTheImagesOwnEntrypointAlone(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/granule", NoCommand: true})
	container := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if _, ok := container["command"]; ok {
		t.Errorf("command = %v, want no command key at all with NoCommand set", container["command"])
	}
}

func TestManifestDefaultsTheCommandToSomethingThatSitsStill(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/sandbox"})
	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	command := container["command"].([]any)
	if len(command) != len(DefaultCommand) {
		t.Fatalf("command = %v, want DefaultCommand %v", command, DefaultCommand)
	}
	// The trap is the point: without it the shell ignores SIGTERM and
	// every Release waits out the grace period.
	if !strings.Contains(command[len(command)-1].(string), "trap") {
		t.Errorf("the default command does not trap TERM: %v", command)
	}
	if got := decodeManifest(t, Client{}, "p", PodShape{}, PodOptions{Image: "i"})["metadata"].(map[string]any)["namespace"]; got != DefaultNamespace {
		t.Errorf("namespace with none configured = %v, want %q", got, DefaultNamespace)
	}
}

func TestManifestOrdersEnvByName(t *testing.T) {
	// One Env map has to produce one manifest, or an unchanged config
	// looks like a change on every render.
	first := decodeManifest(t, Client{}, "p", PodShape{}, PodOptions{
		Image: "i", Env: map[string]string{"B": "2", "A": "1", "C": "3"},
	})
	env := first["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
	var names []string
	for _, entry := range env {
		names = append(names, entry.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "A,B,C" {
		t.Errorf("env names = %v, want A,B,C", names)
	}
}

func TestManifestRefusesAPodWithNoImage(t *testing.T) {
	// There is no default worth guessing: the image is the whole of what
	// a dispatched agent finds when it lands.
	if _, err := (Client{}).Manifest("p", PodShape{}, PodOptions{}); err == nil {
		t.Fatal("Manifest with no image succeeded")
	}
	if _, err := (Client{}).Manifest("", PodShape{}, PodOptions{Image: "i"}); err == nil {
		t.Fatal("Manifest with no name succeeded")
	}
}

func TestManifestLeavesASandboxPodUnconstrainedAndCarriesASecurityContextWhenGivenOne(t *testing.T) {
	// A sandbox names no uid: which one its image runs as is the image's
	// own declaration, and a number here would override images this
	// package has never seen.
	sandbox := decodeManifest(t, Client{}, "p", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/sandbox"})
	spec := sandbox["spec"].(map[string]any)
	if _, ok := spec["securityContext"]; ok {
		t.Errorf("a sandbox pod was given a pod-level securityContext: %v", spec["securityContext"])
	}
	if _, ok := spec["containers"].([]any)[0].(map[string]any)["securityContext"]; ok {
		t.Error("a sandbox container was given a securityContext")
	}

	// The self-upgrade's check pod, on the other hand, is constrained
	// exactly as the Deployment it is a candidate for.
	checked := decodeManifest(t, Client{}, "grain-upgrade-check", PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1},
		PodOptions{Image: "example/grain:main", Security: PodSecurity{
			Pod:       SecurityContext{"runAsUser": 1000, "fsGroup": 1000, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			Container: SecurityContext{"allowPrivilegeEscalation": false},
		}})
	spec = checked["spec"].(map[string]any)
	podSecurity, ok := spec["securityContext"].(map[string]any)
	if !ok {
		t.Fatalf("the check pod has no pod-level securityContext: %v", spec)
	}
	if podSecurity["runAsUser"] != float64(1000) || podSecurity["fsGroup"] != float64(1000) {
		t.Errorf("pod securityContext = %v, want the uid and group it was given", podSecurity)
	}
	// Carried verbatim, nested values and all: a typed subset of the
	// fields in use today would drop whatever a Deployment grows next.
	if profile, ok := podSecurity["seccompProfile"].(map[string]any); !ok || profile["type"] != "RuntimeDefault" {
		t.Errorf("seccompProfile = %v, want it copied through", podSecurity["seccompProfile"])
	}
	containerSecurity := spec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
	if containerSecurity["allowPrivilegeEscalation"] != false {
		t.Errorf("container securityContext = %v, want the one it was given", containerSecurity)
	}
}

func TestManifestKeepsPrivilegedAlongsideAContainerSecurityContext(t *testing.T) {
	pod := decodeManifest(t, Client{}, "p", PodShape{},
		PodOptions{Image: "i", Privileged: true, Security: PodSecurity{
			Container: SecurityContext{"readOnlyRootFilesystem": true},
		}})
	security := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
	if security["privileged"] != true || security["readOnlyRootFilesystem"] != true {
		t.Errorf("container securityContext = %v, want both what the caller asked for and privileged", security)
	}
}

// deploymentJSON is what `kubectl get deployment -o json` answers for a
// Deployment shaped like deploy/kubernetes/grain.yaml's.
const deploymentJSON = `{"spec":{"template":{"spec":{
  "securityContext":{"runAsNonRoot":true,"runAsUser":1000,"runAsGroup":1000,"fsGroup":1000},
  "containers":[
    {"name":"other","securityContext":{"privileged":true}},
    {"name":"grain","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}
  ]}}}}`

func TestDeploymentSecurityReadsBothContextsForTheNamedContainer(t *testing.T) {
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: deploymentJSON}})
	c := Client{Kubectl: kubectl}
	security, err := c.DeploymentSecurity(context.Background(), "grain", "grain")
	if err != nil {
		t.Fatalf("DeploymentSecurity: %v", err)
	}
	if security.Pod["runAsUser"] != float64(1000) || security.Pod["fsGroup"] != float64(1000) {
		t.Errorf("pod securityContext = %v, want the uid the Deployment runs as", security.Pod)
	}
	// The named container's, not the first one's: a Deployment with a
	// sidecar must not hand the check pod the sidecar's privileges.
	if _, privileged := security.Container["privileged"]; privileged {
		t.Errorf("container securityContext = %v, want the grain container's", security.Container)
	}
	if security.Container["allowPrivilegeEscalation"] != false {
		t.Errorf("container securityContext = %v, want the grain container's own", security.Container)
	}
	if security.Empty() {
		t.Error("PodSecurity.Empty on a context that constrains a pod")
	}
	if !strings.Contains(calls()[0], "get deployment grain -o json") {
		t.Errorf("call = %q, want it to read the deployment", calls()[0])
	}

	if _, err := c.DeploymentSecurity(context.Background(), "grain", "nope"); err == nil {
		t.Error("DeploymentSecurity succeeded for a container the Deployment does not have")
	}
	if _, err := (Client{Kubectl: "/nonexistent"}).DeploymentSecurity(context.Background(), "", "grain"); err == nil {
		t.Error("DeploymentSecurity succeeded with no deployment named")
	}
}

func TestDeploymentSecurityReportsAnUnconstrainedDeploymentAsEmpty(t *testing.T) {
	// No securityContext anywhere is an answer, not a failure: a pod of
	// that Deployment runs as whatever its image says, and so should a
	// check pod standing in for one.
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: `{"spec":{"template":{"spec":{"containers":[{"name":"grain"}]}}}}`}})
	security, err := Client{Kubectl: kubectl}.DeploymentSecurity(context.Background(), "grain", "grain")
	if err != nil {
		t.Fatalf("DeploymentSecurity: %v", err)
	}
	if !security.Empty() {
		t.Errorf("security = %v, want nothing at all", security)
	}
}

// fakeKubectl writes a script standing in for kubectl that records the
// arguments it was called with and answers from a canned script, so
// every test below runs against a real exec.Command with no cluster
// anywhere.
//
// stdout/exit are keyed by the *verb* (the argument after the "-n
// <namespace>" this package always prepends), which is enough to tell
// the calls in one test apart.
func fakeKubectl(t *testing.T, responses map[string]struct {
	stdout string
	code   int
}) (path string, calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	// Delimited rather than one call per line: an argument can itself
	// contain a newline (the go-templates above do), which would
	// otherwise read back as two calls.
	script.WriteString("printf '%s\\036' \"$*\" >> " + log + "\n")
	script.WriteString("if [ \"$1\" = \"--kubeconfig\" ]; then shift 2; fi\n")
	script.WriteString("verb=$3\n")
	for verb, response := range responses {
		script.WriteString("if [ \"$verb\" = \"" + verb + "\" ]; then\n")
		if response.stdout != "" {
			script.WriteString("  printf '%s' '" + response.stdout + "'\n")
		}
		if response.code != 0 {
			script.WriteString("  echo 'Error from server (NotFound): pods \"p\" not found' >&2\n")
		}
		script.WriteString("  exit " + itoa(response.code) + "\n")
		script.WriteString("fi\n")
	}
	script.WriteString("exit 0\n")
	path = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path, func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var out []string
		for _, call := range strings.Split(string(data), "\x1e") {
			if call = strings.TrimSpace(call); call != "" {
				out = append(out, call)
			}
		}
		return out
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

func TestStatusReadsPhaseReadinessAndReason(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Pending|False||ImagePullBackOff: no such image"}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}

	st, err := c.Status(context.Background(), "p")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Phase != PhasePending || st.Ready {
		t.Errorf("Status = %+v, want a not-ready Pending", st)
	}
	// The reason is the whole point: "this sandbox never came up"
	// becomes "ImagePullBackOff on the image you named".
	if !strings.Contains(st.Reason, "ImagePullBackOff") {
		t.Errorf("Reason = %q, want the kubelet's own reason", st.Reason)
	}
}

func TestStatusReadsTheImageIDThePodRan(t *testing.T) {
	// What a caller pins to: the digest the kubelet actually ran, rather
	// than the tag the spec asked for. A reason holding a "|" of its own
	// must not be read into it, nor it into the reason.
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Succeeded|False|docker-pullable://example/grain@sha256:abc |Completed | done"}})

	st, err := Client{Kubectl: kubectl}.Status(context.Background(), "p")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ImageID != "docker-pullable://example/grain@sha256:abc" {
		t.Errorf("ImageID = %q, want the container's own imageID", st.ImageID)
	}
	if st.Phase != PhaseSucceeded || st.Reason != "Completed | done" {
		t.Errorf("Status = %+v, want Succeeded with the whole reason", st)
	}
}

func TestStatusReportsAMissingPodAsAnAnswer(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {code: 1}})
	st, err := Client{Kubectl: kubectl}.Status(context.Background(), "p")
	// "Gone" is an answer every caller here wants back as one, not as a
	// failure to get one.
	if err != nil {
		t.Fatalf("Status against a missing pod: %v", err)
	}
	if st.Phase != PhaseMissing {
		t.Errorf("Phase = %q, want %q", st.Phase, PhaseMissing)
	}
	if !st.Phase.Terminal() {
		t.Error("PhaseMissing is not terminal, so a wait would sit out its whole deadline against a pod that is gone")
	}
}

// The kubelet's own record of a crash loop, which under Kubernetes is
// what the daemon reports in place of the journal count it has no
// journal for (cmd/grain's daemon_restarts.go).
func TestContainerRestartsReadsTheKubeletsOwnCount(t *testing.T) {
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "7"}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}

	n, err := c.ContainerRestarts(context.Background(), "grain-7d9f-abcde", "grain")
	if err != nil {
		t.Fatalf("ContainerRestarts: %v", err)
	}
	if n != 7 {
		t.Errorf("restarts = %d, want 7", n)
	}
	call := calls()[0]
	if !strings.Contains(call, "get pod grain-7d9f-abcde") || !strings.Contains(call, "restartCount") {
		t.Errorf("call = %q, want it to read that pod's restartCount", call)
	}
	// The container is named in the template, so a pod with a sidecar
	// reports the daemon's own restarts rather than the first
	// container's.
	if !strings.Contains(call, `eq .name "grain"`) {
		t.Errorf("call = %q, want it to select the named container", call)
	}
}

func TestContainerRestartsSaysSoWhenThereIsNoCountToRead(t *testing.T) {
	// A pod whose kubelet has published no status for this container --
	// a Pending one -- renders as nothing, and "nobody could tell" and
	// "it has never restarted" are the same figure and different facts.
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: ""}})
	if _, err := (Client{Kubectl: kubectl}).ContainerRestarts(context.Background(), "p", "grain"); err == nil {
		t.Error("ContainerRestarts succeeded against a pod with no status for the container")
	}

	// And a pod that is not there at all is an error rather than a zero:
	// unlike Status, whose callers all treat "gone" as an answer, the
	// only caller of this is a process asking about *itself*.
	missing, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {code: 1}})
	if _, err := (Client{Kubectl: missing}).ContainerRestarts(context.Background(), "p", "grain"); err == nil {
		t.Error("ContainerRestarts succeeded against a pod that does not exist")
	}

	if _, err := (Client{Kubectl: "/nonexistent"}).ContainerRestarts(context.Background(), "", "grain"); err == nil {
		t.Error("ContainerRestarts succeeded with no pod named")
	}
}

func TestWaitReadyGivesUpAtOnceOnAPodThatWillNeverBeReady(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Failed|False||Evicted: the node was low on ephemeral-storage"}})
	c := Client{Kubectl: kubectl}

	started := time.Now()
	// A deadline far enough out that sitting on it would be obvious.
	err := c.WaitReady(context.Background(), "p", started.Add(time.Minute), 0, 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady against a Failed pod succeeded")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("WaitReady took %s against a terminal pod: it should not wait out its deadline", elapsed)
	}
	if !strings.Contains(err.Error(), "Evicted") {
		t.Errorf("error = %q, want the kubelet's own reason in it", err)
	}
}

func TestWaitReadyGivesUpAtOnceOnAnImagePullFailure(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Pending|False||ErrImagePull: failed to pull image"}})
	c := Client{Kubectl: kubectl}

	started := time.Now()
	err := c.WaitReady(context.Background(), "p", started.Add(time.Minute), 0, 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady against ErrImagePull succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("WaitReady took %s against ErrImagePull: it should not wait out its deadline", elapsed)
	}
	if !strings.Contains(err.Error(), "ErrImagePull") {
		t.Errorf("error = %q, want the kubelet's own reason in it", err)
	}
}

// A pod no node can fit has no containerStatuses and no status.reason --
// all the scheduler leaves is a PodScheduled=False condition -- so that
// condition is what podStatusFormat has to fall back to. Rendered with
// text/template over the pod as JSON, which is what kubectl's own
// go-template output does.
func TestPodStatusFormatReadsTheSchedulersVerdict(t *testing.T) {
	const pod = `{"status": {"phase": "Pending", "conditions": [
		{"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
		 "message": "0/1 nodes are available: 1 Insufficient memory."}]}}`
	var obj map[string]any
	if err := json.Unmarshal([]byte(pod), &obj); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := template.Must(template.New("status").Parse(podStatusFormat)).Execute(&out, obj); err != nil {
		t.Fatalf("rendering podStatusFormat: %v", err)
	}
	want := "Pending|||Unschedulable: 0/1 nodes are available: 1 Insufficient memory."
	if out.String() != want {
		t.Fatalf("rendered %q, want %q", out.String(), want)
	}
}

func TestWaitReadyGivesUpOnAPodThatStaysUnschedulable(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Pending|||Unschedulable: 0/1 nodes are available: 1 Insufficient memory."}})
	c := Client{Kubectl: kubectl}

	started := time.Now()
	err := c.WaitReady(context.Background(), "p", started.Add(time.Minute), 200*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady against an unschedulable pod succeeded")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("WaitReady took %s against an unschedulable pod: it should give up at its unschedulable bound", elapsed)
	}
	for _, want := range []string{"Insufficient memory", "sandboxMemoryMb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q in it", err, want)
		}
	}
}

func TestWaitReadyWithNoUnschedulableBoundWaitsOutItsDeadline(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "Pending|||Unschedulable: 0/1 nodes are available: 1 Insufficient memory."}})
	c := Client{Kubectl: kubectl}

	started := time.Now()
	err := c.WaitReady(context.Background(), "p", started.Add(300*time.Millisecond), 0, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "when the wait ran out") {
		t.Fatalf("error = %v, want the deadline's own", err)
	}
	if !strings.Contains(err.Error(), "Insufficient memory") {
		t.Errorf("error = %q, want the scheduler's message in it", err)
	}
}

// TailLogs is what the UI's Logs tab reads grain's own pod through on a
// Kubernetes deployment (pkg/systemlog.Pod, grain/task-488): the same
// `kubectl logs` Logs runs, bounded to the last N lines, since the whole
// of a live daemon's log is not what a pane asking for 500 lines wants.
func TestTailLogsBoundsTheReadAndLogsDoesNot(t *testing.T) {
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"logs": {stdout: "one\ntwo\n"}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}
	ctx := context.Background()

	out, err := c.TailLogs(ctx, "grain-7d9f-abcde", "grain", 500)
	if err != nil {
		t.Fatalf("TailLogs: %v", err)
	}
	if out != "one\ntwo\n" {
		t.Errorf("TailLogs = %q, want what the container printed", out)
	}
	if _, err := c.Logs(ctx, "grain-7d9f-abcde", "grain"); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	recorded := calls()
	if len(recorded) != 2 {
		t.Fatalf("recorded %d calls, want 2: %v", len(recorded), recorded)
	}
	if !strings.Contains(recorded[0], "logs grain-7d9f-abcde") || !strings.Contains(recorded[0], "-c grain") {
		t.Errorf("TailLogs called %q, want the pod and container it was given", recorded[0])
	}
	if !strings.Contains(recorded[0], "--tail 500") {
		t.Errorf("TailLogs called %q, want the line bound it was given", recorded[0])
	}
	// Logs is the whole log by contract -- a caller explaining a failed
	// sandbox wants every line of it (pkg/upgrade/kube.go).
	if strings.Contains(recorded[1], "--tail") {
		t.Errorf("Logs called %q, want no bound at all", recorded[1])
	}
}

func TestEveryCallNamesItsNamespace(t *testing.T) {
	// A kubectl left to fall back on the kubeconfig's current namespace
	// is how a sandbox ends up somewhere nothing will ever reap it.
	kubectl, calls := fakeKubectl(t, nil)
	c := Client{Kubectl: kubectl, Namespace: "elsewhere"}
	ctx := context.Background()
	if err := c.Delete(ctx, "p", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := c.Create(ctx, "p", PodShape{}, PodOptions{Image: "i"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := c.ListSandboxes(ctx); err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if err := c.SetDeploymentImage(ctx, "grain", "grain", "example/grain:main"); err != nil {
		t.Fatalf("SetDeploymentImage: %v", err)
	}
	recorded := calls()
	if len(recorded) != 4 {
		t.Fatalf("recorded %d calls, want 4: %v", len(recorded), recorded)
	}
	for _, call := range recorded {
		if !strings.HasPrefix(call, "-n elsewhere ") {
			t.Errorf("call %q does not name its namespace first", call)
		}
	}
}

func TestClientPassesKubeconfigWhenSet(t *testing.T) {
	kubectl, calls := fakeKubectl(t, nil)
	c := Client{
		Kubectl:    kubectl,
		Kubeconfig: "/var/lib/grain/kubeconfig",
		Namespace:  "grain",
	}
	ctx := context.Background()
	if err := c.Delete(ctx, "p", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := c.ListSandboxes(ctx); err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	recorded := calls()
	if len(recorded) != 2 {
		t.Fatalf("recorded %d calls, want 2: %v", len(recorded), recorded)
	}
	for _, call := range recorded {
		if !strings.HasPrefix(call, "--kubeconfig /var/lib/grain/kubeconfig -n grain ") {
			t.Errorf("call %q does not start with --kubeconfig /var/lib/grain/kubeconfig -n grain", call)
		}
	}
}

func TestDeleteIgnoresAPodThatIsAlreadyGone(t *testing.T) {
	kubectl, calls := fakeKubectl(t, nil)
	if err := (Client{Kubectl: kubectl}).Delete(context.Background(), "p", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	call := calls()[0]
	// Release is called on paths that may already have lost the pod, and
	// "it is not there" is the outcome the caller wanted either way.
	if !strings.Contains(call, "--ignore-not-found") {
		t.Errorf("delete call %q does not pass --ignore-not-found", call)
	}
	if !strings.Contains(call, "--wait=true") {
		t.Errorf("delete call %q did not honour wait: a name is not free until the old object has left", call)
	}
}

func TestListSandboxesMatchesBothGrainLabels(t *testing.T) {
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "grain-sandbox-1-1\ngrain-sandbox-2-1\n"}})
	names, err := Client{Kubectl: kubectl}.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if strings.Join(names, ",") != "grain-sandbox-1-1,grain-sandbox-2-1" {
		t.Errorf("names = %v", names)
	}
	// Both labels, so a namespace shared with anything else -- including
	// a future grain-managed pod that is not a sandbox -- is not swept
	// up by a caller reaping orphans.
	call := calls()[0]
	if !strings.Contains(call, ManagedByLabel+"="+ManagedByValue) || !strings.Contains(call, SandboxLabel+"="+SandboxValue) {
		t.Errorf("list call %q does not select on both grain labels", call)
	}
}

func TestSetDeploymentImageRefusesAnIncompleteTarget(t *testing.T) {
	c := Client{Kubectl: "/nonexistent"}
	for _, args := range [][3]string{{"", "grain", "ref"}, {"grain", "", "ref"}, {"grain", "grain", ""}} {
		if err := c.SetDeploymentImage(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Errorf("SetDeploymentImage(%q, %q, %q) succeeded", args[0], args[1], args[2])
		}
	}
}

// podStatusFormat is executed by kubectl, which is not here; text/template
// over the pod's JSON is what kubectl's go-template printer is too. A
// sandbox pod with a Setup has to fail as fast, and as legibly, on its
// init container as it does on its sandbox container.
func TestPodStatusFormatReportsTheSetupInitContainerFirst(t *testing.T) {
	tmpl := template.Must(template.New("status").Parse(podStatusFormat))
	for _, tc := range []struct{ name, pod, want string }{
		{"pulling", `{"status":{"phase":"Pending","initContainerStatuses":[{"name":"setup",
			"state":{"waiting":{"reason":"ErrImagePull","message":"no such image"}}}],
			"containerStatuses":[{"name":"sandbox","imageID":"","state":{"waiting":{"reason":"PodInitializing"}}}]}}`,
			"Pending|| |ErrImagePull: no such image"},
		{"failed", `{"status":{"phase":"Failed","initContainerStatuses":[{"name":"setup",
			"state":{"terminated":{"exitCode":2,"reason":"Error"}}}]}}`,
			"Failed|||init container setup exited 2: Error "},
		{"done", `{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],
			"initContainerStatuses":[{"name":"setup","state":{"terminated":{"exitCode":0,"reason":"Completed"}}}],
			"containerStatuses":[{"name":"sandbox","imageID":"sha","state":{"running":{}}}]}}`,
			"Running|True|sha |"},
	} {
		var pod map[string]any
		if err := json.Unmarshal([]byte(tc.pod), &pod); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := tmpl.Execute(&out, pod); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.String() != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, out.String(), tc.want)
		}
	}
}

func TestClusterCapacitySumsSchedulableReadyNodesAndParsesQuantities(t *testing.T) {
	nodesJSON := `{
  "items": [
    {
      "metadata": {"name": "node-1", "labels": {"pool": "kvm", "zone": "a"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "4", "memory": "8192Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-2", "labels": {"pool": "kvm", "zone": "b"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "3500m", "memory": "16Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-3", "labels": {"pool": "kvm", "zone": "c"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "500m", "memory": "2097152Ki"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-4-bytes", "labels": {"pool": "kvm", "zone": "d"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "2", "memory": "1073741824"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-cordoned", "labels": {"pool": "kvm"}},
      "spec": {"unschedulable": true},
      "status": {
        "allocatable": {"cpu": "16", "memory": "64Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-not-ready", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "16", "memory": "64Gi"},
        "conditions": [{"type": "Ready", "status": "False"}]
      }
    },
    {
      "metadata": {"name": "node-no-conditions", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi"},
        "conditions": []
      }
    }
  ]
}`
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: nodesJSON}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}

	cpus, memoryMB, _, err := c.ClusterCapacity(context.Background(), nil)
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	// CPUs: 4 + 3500m + 500m + 2 = 10 cores.
	if cpus != 10 {
		t.Errorf("cpus = %d, want 10", cpus)
	}
	// Memory: 8192Mi + 16384Mi (16Gi) + 2048Mi (2097152Ki) + 1024Mi (1073741824 bytes) = 27648 MiB.
	if memoryMB != 27648 {
		t.Errorf("memoryMB = %d, want 27648", memoryMB)
	}
	recorded := calls()
	if len(recorded) != 1 || !strings.Contains(recorded[0], "get nodes -o json") {
		t.Errorf("calls = %v, want get nodes -o json", recorded)
	}
}

// Ephemeral storage is summed the way memory is, in whatever unit each
// node reports it -- GKE reports bare bytes -- and a node that reports
// none adds none rather than failing the reading.
func TestClusterCapacitySumsAllocatableEphemeralStorage(t *testing.T) {
	nodesJSON := `{
  "items": [
    {
      "metadata": {"name": "a"},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi", "ephemeral-storage": "100Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "b"},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi", "ephemeral-storage": "102005473280"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "c"},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "cordoned"},
      "spec": {"unschedulable": true},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi", "ephemeral-storage": "500Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: nodesJSON}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}
	cpus, memoryMB, diskGB, err := c.ClusterCapacity(context.Background(), nil)
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	// 100 GiB + 102005473280 bytes (95 GiB) = 195 GiB; the cordoned node is not counted.
	if cpus != 24 || memoryMB != 3*32*1024 || diskGB != 195 {
		t.Errorf("ClusterCapacity = (%d, %d, %d), want (24, %d, 195)", cpus, memoryMB, diskGB, 3*32*1024)
	}
}

func TestClusterCapacityFiltersByNodeSelector(t *testing.T) {
	nodesJSON := `{
  "items": [
    {
      "metadata": {"name": "kvm-1", "labels": {"pool": "kvm", "tier": "sandbox"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-2", "labels": {"pool": "kvm", "tier": "sandbox"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "7800m", "memory": "16384Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "general-1", "labels": {"pool": "default", "tier": "sandbox"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "32", "memory": "128Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`
	kubectl, calls := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: nodesJSON}})
	c := Client{Kubectl: kubectl, Namespace: "grain"}

	cpus, memoryMB, _, err := c.ClusterCapacity(context.Background(), map[string]string{
		"pool": "kvm",
		"tier": "sandbox",
	})
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	// 8000m + 7800m = 15800m -> 15 whole CPUs; 32768 + 16384 = 49152 MiB.
	if cpus != 15 || memoryMB != 49152 {
		t.Errorf("ClusterCapacity = (%d, %d), want (15, 49152)", cpus, memoryMB)
	}
	recorded := calls()
	if len(recorded) != 1 || !strings.Contains(recorded[0], "get nodes -l pool=kvm,tier=sandbox -o json") {
		t.Errorf("calls = %v, want get nodes -l pool=kvm,tier=sandbox -o json", recorded)
	}
}
