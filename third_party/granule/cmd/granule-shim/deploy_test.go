package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setsPrivileged reports whether a manifest sets privileged: true, as
// opposed to mentioning it in a comment.
func setsPrivileged(manifest string) bool {
	for _, line := range strings.Split(manifest, "\n") {
		if strings.TrimSpace(line) == "privileged: true" {
			return true
		}
	}
	return false
}

// Keep the user-facing deployment paths aligned with the entrypoint.
// No YAML dependency is needed to check these checked-in fixtures' shape.
func TestDirectStartupManifests(t *testing.T) {
	for _, name := range []string{"pod-example.yaml", "gke-pod-exec-example.yaml", "gke-pod-example.yaml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "k8s", name))
			if err != nil {
				t.Fatal(err)
			}
			s := string(raw)
			if strings.Contains(s, "\n  initContainers:") {
				t.Fatal("direct startup must not need an init container")
			}
			if strings.Count(s, "\n      image:") != 1 {
				t.Fatal("expected one VM container")
			}
			mode := "inline"
			if name == "gke-pod-example.yaml" {
				mode = "none"
			}
			if !strings.Contains(s, "name: CHV_NET_MODE\n          value: \""+mode+"\"") {
				t.Fatalf("missing explicit %s mode", mode)
			}
			// KVM (and, for inline networking, the tun device) comes from
			// kvm-device-plugin.yaml, never from privileged: true -- see
			// gke.md's "Privileges".
			if setsPrivileged(s) {
				t.Fatal("direct startup must not run privileged")
			}
			if strings.Contains(s, "path: /dev/kvm") {
				t.Fatal("/dev/kvm must come from the device plugin, not a hostPath")
			}
			if !strings.Contains(s, "devic.es/kvm: \"1\"") {
				t.Fatal("missing devic.es/kvm device request")
			}
			if !strings.Contains(s, "drop: [\"ALL\"]") {
				t.Fatal("missing capability drop")
			}
			if !strings.Contains(s, "readOnlyRootFilesystem: true") {
				t.Fatal("missing read-only root filesystem")
			}
			if mode == "inline" {
				if !strings.Contains(s, "devic.es/tun: \"1\"") {
					t.Fatal("inline networking needs the devic.es/tun device")
				}
				if !strings.Contains(s, "add: [\"NET_ADMIN\"]") {
					t.Fatal("inline networking needs NET_ADMIN")
				}
			} else {
				if strings.Contains(s, "add: [") {
					t.Fatal("a networkless VM needs no capabilities")
				}
				if !strings.Contains(s, "runAsNonRoot: true") {
					t.Fatal("a networkless VM should run as non-root")
				}
			}
		})
	}
}

func TestSplitPrivilegeManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "k8s", "pod-split-example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "initContainers:") {
		t.Fatal("split privilege manifest must have an initContainer")
	}
	if !strings.Contains(s, "args: [\"netshim\"]") {
		t.Fatal("initContainer must run netshim")
	}
	if !strings.Contains(s, "args: [\"run\"]") {
		t.Fatal("VM container must run granule run")
	}
	if !strings.Contains(s, "value: \"external\"") {
		t.Fatal("split privilege VM container must declare CHV_NET_MODE=external")
	}
	if setsPrivileged(s) {
		t.Fatal("split privilege manifest must not run privileged")
	}
	if strings.Count(s, "devic.es/tun: \"1\"") != 2 {
		t.Fatal("netshim and the VM container both need the devic.es/tun device")
	}
	if !strings.Contains(s, "devic.es/kvm: \"1\"") {
		t.Fatal("split privilege VM container must declare the devic.es/kvm device resource")
	}

	// NET_ADMIN belongs to netshim alone. The VM container attaches to
	// the taps netshim built as non-root, with no capabilities, which
	// works only because netshim gives those taps the VM container's
	// UID:GID. See gke.md's "Privileges".
	init, vm, ok := strings.Cut(s, "\n  containers:")
	if !ok {
		t.Fatal("split privilege manifest must have a containers section")
	}
	if strings.Count(init, "add: [\"NET_ADMIN\"]") != 1 {
		t.Fatal("netshim must hold NET_ADMIN")
	}
	if strings.Contains(vm, "add: [") {
		t.Fatal("the split VM container must hold no capabilities")
	}
	if !strings.Contains(vm, "drop: [\"ALL\"]") {
		t.Fatal("the split VM container must drop all capabilities")
	}
	if !strings.Contains(vm, "runAsNonRoot: true") || !strings.Contains(vm, "runAsUser: 65534") || !strings.Contains(vm, "runAsGroup: 65534") {
		t.Fatal("the split VM container must run as 65534:65534")
	}
	if !strings.Contains(init, "name: NETSHIM_TAP_OWNER\n          value: \"65534:65534\"") {
		t.Fatal("netshim must give its taps to the UID:GID the VM container runs as")
	}
}

func TestDevicePluginManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "k8s", "kvm-device-plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"--domain=devic.es", `"name": "kvm"`, `"path": "/dev/kvm"`, `"name": "tun"`, `"path": "/dev/net/tun"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("device plugin manifest is missing %s", want)
		}
	}
	if setsPrivileged(s) {
		t.Fatal("the device plugin does not need to run privileged")
	}
}

func TestMultiVMPodManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "demo", "assets", "pod-multi-vm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "\n  initContainers:") {
		t.Fatal("multi-VM pod should not use init containers")
	}

	for _, container := range []string{"web", "builder", "sidecar"} {
		if !strings.Contains(s, "- name: "+container) {
			t.Fatalf("multi-VM pod missing container %q", container)
		}
	}
	if strings.Count(s, "mountPath: /dev/kvm") != 3 {
		t.Fatalf("expected 3 /dev/kvm mounts (one per container), got %d", strings.Count(s, "mountPath: /dev/kvm"))
	}
	if strings.Count(s, "privileged: true") != 3 {
		t.Fatalf("expected 3 privileged containers, got %d", strings.Count(s, "privileged: true"))
	}
	if !strings.Contains(s, "path: /dev/kvm") {
		t.Fatal("missing /dev/kvm hostPath volume in pod-multi-vm.yaml")
	}
	if !strings.Contains(s, "terminationGracePeriodSeconds: 40") {
		t.Fatal("missing terminationGracePeriodSeconds in pod-multi-vm.yaml")
	}
}

func TestEphemeralVMSpec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "demo", "assets", "ephemeral-vm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "args: [\"run\"]") {
		t.Fatal("ephemeral container spec must specify args: [\"run\"]")
	}
	if !strings.Contains(s, "value: \"none\"") {
		t.Fatal("ephemeral container must run in CHV_NET_MODE=none")
	}
	if !strings.Contains(s, "mountPath: /dev/kvm") {
		t.Fatal("ephemeral container must mount /dev/kvm")
	}
	for _, forbidden := range []string{"resources:", "ports:", "lifecycle:", "readinessProbe:", "livenessProbe:"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("ephemeral container spec must not declare %s (refused by Kubernetes API)", forbidden)
		}
	}
}
