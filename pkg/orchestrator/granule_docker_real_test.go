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

package orchestrator_test

// TestGranuleSandboxesAgainstARealDockerBackedVM is the one place
// in this repo that drives orchestrator.GranuleSandboxes against the real
// `granule` and `docker` binaries and a real cloud-hypervisor VM under
// real KVM, instead of the hand-written shell-script doubles every other
// granule test in this repo (granule_sandboxes_test.go,
// pkg/granule/granule_test.go, pkg/cli/daemon_granule_wiring_test.go,
// pkg/cli/daemon_true_e2e_test.go) uses. Those doubles are what let the
// rest of the suite run fast and everywhere, but they can only be as
// correct as whoever wrote them already knew to make them: they are
// exactly why pkg/granule's own Create/Delete execing a binary literally
// named "granule" (rather than "granule", the actual operator-facing
// binary bwsalmon/granule ships under a different name -- see that
// package's doc comment) went unnoticed until this test was written
// against the real thing and failed with "exec: \"granule\": executable
// file not found in $PATH" the moment the fake stopped being named to
// match the bug.
//
// It skips outright unless docker and /dev/kvm are both usable -- the
// same "skip rather than run" shape tests/test_vm_integration.py's
// own gate uses for v1's equivalent live suite (that file's own comment:
// "none of which a hosted runner has, so they skip rather than run").
// The guest image build below used to add passwordless sudo plus
// debootstrap/mke2fs to that list; since scripts/granule converged on
// granule's own guest build it needs nothing beyond the docker already
// required here (scripts/granule/README.md, "Converged on granule's own
// guest build").
// Neither of these is present on the hosted GitHub Actions runner
// .github/workflows/tests.yml's go-test job runs on, so this never runs
// there, but it runs for real on any host -- this repo's own dev sandboxes
// included -- that has all of them, which is the whole point: wherever
// granule's prerequisites genuinely exist, this suite exercises it for
// real rather than only ever exercising a fake that agrees with whatever
// the last person to touch it assumed.
//
// bwsalmon/agents#466 stopped short of proving that a dispatched task's
// tool calls actually execute inside the guest over SSH: the only kernel
// available at the time (the firecracker-ci PVH kernel bwsalmon/granule's
// own benchmarks/kernel/build.sh fetches) panics with "Cannot open root
// device vda" against any cloud-hypervisor guest, direct-kernel-booted or
// not -- Firecracker uses virtio-mmio, cloud-hypervisor uses virtio-pci,
// and that kernel only has a driver for the former -- and scripts/granule's
// own guest image had never actually been built. bwsalmon/agents#478
// resolved both (scripts/granule/README.md, "Why no custom kernel": Debian's
// own stock kernel already has everything a cloud-hypervisor direct-
// kernel-boot guest needs, nothing built from source), so this test now
// builds that real guest image and asserts a real dispatched tool call
// actually runs inside it over SSH, not just that Acquire resolves an
// address.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// granuleDockerRealTestPrereqs are the binaries and devices this test
// needs on the host it runs on, beyond what every other test in this
// repo already assumes (go, a POSIX shell). Each check's own failure
// message is the skip reason, so a run against a host missing exactly one
// of these says which.
func granuleDockerRealTestPrereqs(t *testing.T) {
	t.Helper()
	// Opt-in rather than "run wherever the host happens to allow it".
	// GitHub's Linux runners do expose /dev/kvm, so before this gate the
	// checks below passed in tests.yml's go-test job too and this ~5.6
	// minute suite ran twice per commit -- once there, once in the
	// real-vm job built for it. The go-test copy had no timeout of its
	// own, so it ran against `go test`'s 600s per-package default with
	// about 1.8x headroom, and lost that race often enough to redden PRs
	// that had not touched any of this (bwsalmon/grain#519).
	//
	// Coverage is unchanged: real-vm sets this and asserts both tests
	// reported "--- PASS", so dropping the variable there fails that job
	// loudly rather than quietly skipping the suite it exists to run.
	if os.Getenv("GRAIN_REAL_VM_TESTS") == "" {
		t.Skip("GRAIN_REAL_VM_TESTS not set; this suite runs in .grain/workflows/nightly.yml's real KVM sandboxes job (set it to 1 to run locally)")
	}
	if runtime.GOOS != "linux" {
		t.Skip("real granule/docker VMs are only wired up for Linux hosts")
	}
	for _, bin := range []string{"docker", "ssh-keygen", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH: %v", bin, err)
		}
	}
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	// Stat, not Open: the VM container this test creates runs as root
	// and reaches /dev/kvm through --device /dev/kvm (in
	// internal/dockervm/docker.go's own "docker run"), not as this test
	// process's own, ordinarily much less privileged, host user.
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("/dev/kvm not present: %v", err)
	}
	// buildGranuleGuestImage below needs no privilege check of its own:
	// `granule granulefile build` is a `docker build`, against the
	// same daemon already proven reachable above. It deliberately gets no
	// skip of its own for a docker too old for BuildKit's --output --
	// that is a broken host rather than a missing prerequisite, and a
	// build failure naming it says far more than a silent skip would.
}

// buildGranule builds bwsalmon/granule's own operator-facing CLI from the
// vendored copy this repo carries (third_party/granule/VENDORED.md) into a
// fresh temp directory, and returns that directory so the caller can put
// it on PATH -- the exact binary name pkg/granule.Create/Delete now exec
// (this test's entire reason to exist: proving that against the real
// thing).
func buildGranule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "granule")
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root absolute path: %v", err)
	}
	cmd := exec.Command("go", "build", "-mod=vendor", "-o", bin, "./cmd/granule")
	cmd.Dir = filepath.Join(repoRoot, "third_party", "granule")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building granule from granule: %v\n%s", err, out)
	}
	return dir
}

// buildGranuleGuestImage builds the repository's Granulefile for real and
// returns the image reference it produced: a Debian rootfs with
// git/build tooling/docker/gcloud/terraform installed by the Granulefile's
// RUN steps, packed by its `VM PACK` onto the granule image
// scripts/granule/build-granule-image.sh builds.
//
// It returns one image reference rather than a directory of three files
// because that is what a VM needs now. This used to hand back a
// kernel/initramfs/disk triple for granule's -kernel/-initramfs/-disk
// flags to point at, built by a `docker build` that debootstrapped a
// rootfs from scratch; the guest travels inside the image itself since
// bwsalmon/granule#36, so there is nothing to unpack and nothing for the
// caller to mount.
//
// No SSH key goes into it, and no sshd either. This helper used to
// generate a throwaway keypair and hand the build the public half,
// because that was the only entry in the guest's authorized_keys; then
// granule generated one per boot in the VM's own container
// (bwsalmon/granule#35). Since bwsalmon/granule#46 `granule exec` reaches
// the guest over virtio-vsock and there is no key, no authorized_keys and
// no sshd anywhere in the picture, so the callers below configure no
// ExecKeyPath.
//
// Cached by tag: the build installs several hundred MB of packages,
// which is the most expensive single step in this
// already-expensive, opt-in test, and nothing in it changes between runs.
// A rebuild is one `docker rmi` away.
//
// CI leans on that tag deliberately rather than incidentally. The real-vm
// job in .github/workflows/tests.yml pulls the guest build-artifacts.yml
// published for this commit's guest inputs and tags it grain-guest:e2e-test
// before this suite runs, so the branch below is what decides whether a
// run builds a guest at all: a commit that changes one of those inputs
// misses, arrives here with no such image, and builds it from scratch.
// Nothing about that is visible from this file, which is why it is
// written down in it.
//
// The granule image it packs onto is built by that script out of
// third_party/granule, so this exercises the vendored tree end to end --
// granule, the packing and the guest all from the same source.
func buildGranuleGuestImage(t *testing.T) (image string) {
	t.Helper()
	image = "grain-guest:e2e-test"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		return image
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root absolute path: %v", err)
	}
	// What the guest is packed onto: granulefile's own default for it is
	// `granule:latest`, a Docker Hub reference nothing in this repository
	// produces, so the vendored tree is built here first and named below.
	// See scripts/granule/build-granule-image.sh.
	const granuleImage = "granule:vendored"
	granule := exec.Command("bash", "./scripts/granule/build-granule-image.sh")
	granule.Dir = repoRoot
	granule.Env = append(os.Environ(), "IMAGE="+granuleImage)
	if out, err := granule.CombinedOutput(); err != nil {
		t.Fatalf("building %s from third_party/granule: %v\n%s", granuleImage, err, out)
	}

	// The granule binary rather than "go run": this has to execute from
	// repoRoot, because that is the build context the Granulefile's own
	// paths are relative to -- and "go run -mod=vendor <path>" from there
	// fails outright, since grain's module root has no vendor directory and
	// the one that does belongs to the nested module the path points into
	// ("inconsistent vendoring in <repo>", listing every one of grain's own
	// requirements). buildGranule already compiles it the way that works,
	// from inside third_party/granule where the vendor directory is, so
	// this asks it for one and runs that.
	cmd := exec.Command(filepath.Join(buildGranule(t), "granule"),
		"granulefile", "build", "-kontur-image", granuleImage, "-f", "Granulefile", "-t", image)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running granule granulefile build: %v\n%s", err, out)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Fatalf("granulefile build reported success but %s does not exist: %v\n%s", image, err, out)
	}
	return image
}

// TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently is the
// concurrency counterpart to TestGranuleSandboxesAgainstARealDockerBackedVM
// above: that test proves Acquire works against one real cloud-hypervisor
// VM under KVM, this one proves two slots' worth of real VMs actually come
// up side by side under real docker/netshim/cloud-hypervisor, driven the
// same way reconcileDispatch's own goroutine-per-dispatch loop
// (cycle.go) drives distinct slots -- concurrently, from independent
// goroutines, racing granule.Create for two different VM names against
// each other for real, not against sandboxes_concurrency_test.go's fake
// granule double.
//
// It exists because bwsalmon/agents#528 asked for granule to be exercised
// under real nested virtualization specifically for corner cases and
// concurrency, and because GranuleSandboxes.ensure used to hold a single
// GranuleSandboxes-wide lock across the whole granule.Create subprocess
// call -- serializing every slot's VM creation behind whichever slot
// happened to start first, silently undoing the concurrency
// reconcileDispatch's own doc comment promises. That bug was caught and
// fixed against a fake (sandboxes_concurrency_test.go's
// TestGranuleSandboxesCreatesDistinctVMsConcurrentlyNotSerially, which can
// assert wall-clock overlap precisely because its fake granule sleeps a
// known amount); this test is the same claim proven against the real
// thing, where "does it still work" -- two independent netshim network
// namespaces, two independent cloud-hypervisor guests -- matters more
// than exact timing.
func TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)

	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		CreateArgs: []string{
			// No -disk/-kernel/-initramfs, and no -images-hostpath: the
			// guest travels inside -granule-image, so granule boots
			// what that image carries.
			"-kontur-image", image,
			"-guest-port", "22",
			// The guest agent runs as root and drops to the account it
			// is asked for, so this is the account grain's commands run
			// as rather than an authorization decision. Same flag
			// scripts/setup.sh passes in a real deployment.
			"-guest-user", "debian",
			// Each VM gets a writable root: a thin qcow2 created inside
			// its own container, backed by the image's disk, which is
			// only ever read (bwsalmon/granule#37). granule's default is
			// read-only, and a read-only root is not something a guest
			// grain dispatches work into can be: every checkout, build
			// and package install writes to it (confirmed by hand: such
			// a VM reports "/dev/vda / ext4 ro,relatime" in
			// /proc/mounts). The same flag scripts/setup.sh's own
			// GRAIN_KONTUR_ENABLE=1 branch passes in a real deployment.
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
		// No IP/Port: this runs in flat mode (the default), where
		// createArgs drops both because the container runtime assigns the
		// address. They were set here to keep this test's VMs off the
		// other real test's addresses, which flat mode makes moot -- and
		// leaving them set implied this test exercises addressing, which
		// it does not.
	})

	// Two run-shaped sandbox names, distinct from the other real test's
	// below. The prefix is a constant now (orchestrator.VMNamePrefix), so
	// keeping these tests' VMs apart on a shared docker daemon is the
	// sandbox name's job rather than the prefix's.
	slots := []string{"c1-1", "c1-2"}
	names := make([]string, len(slots))
	for i, slot := range slots {
		name, err := k.VMNameFor(slot)
		if err != nil {
			t.Fatal(err)
		}
		names[i] = name
	}
	t.Cleanup(func() {
		for _, name := range names {
			if err := granule.Delete(context.Background(), stateDir, name); err != nil {
				t.Logf("cleaning up real granule VM %q: %v", name, err)
			}
		}
	})

	type outcome struct {
		slot  string
		tools []mcp.Tool
		err   error
	}
	results := make(chan outcome, len(slots))
	var wg sync.WaitGroup
	for _, slot := range slots {
		wg.Add(1)
		go func(slot string) {
			defer wg.Done()
			sb, err := k.Acquire(context.Background(), slot, orchestrator.Shape{})
			if err != nil {
				results <- outcome{slot: slot, err: err}
				return
			}
			t.Cleanup(func() { _ = sb.Release(context.Background()) })
			tools, err := sb.Tools(context.Background())
			results <- outcome{slot: slot, tools: tools, err: err}
		}(slot)
	}
	wg.Wait()
	close(results)

	// Every outcome is reported before failing, rather than t.Fatalf on
	// whichever error came off the channel first. Which VMs failed is the
	// whole diagnosis here: one failing where the other came up is a
	// concurrency problem in this package, while both failing is the
	// guest image's own flat-mode control link never coming up, which is
	// nothing to do with how the VMs were created.
	toolsBySlot := map[string][]mcp.Tool{}
	var failures []string
	for r := range results {
		if r.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", r.slot, r.err))
			continue
		}
		toolsBySlot[r.slot] = r.tools
	}
	if len(failures) > 0 {
		t.Fatalf("%d of %d VMs failed against real granule/docker/cloud-hypervisor:\n  %s",
			len(failures), len(slots), strings.Join(failures, "\n  "))
	}

	// Each guest actually runs and answers a distinct command, for two
	// guests at once this time -- which is now the whole of what this
	// test can say about the transport, and enough.
	//
	// There used to be an assertion here about the guests' addresses,
	// inverted once already: first that the two *differed*, while
	// GranuleConfig derived an -ip per slot, then that they were the
	// *same*, while flat mode gave each VM its own namespace and `granule
	// exec` dialed a fixed control-link address in it (KONTUR_EXEC_ADDR).
	// Since bwsalmon/granule#46 there is no address in the picture at all,
	// and nothing per-VM left for this test to read: the transport is a
	// vsock socket inside each VM's own container, and that container is
	// built FROM scratch, so there is no `test`, `stat` or shell in it to
	// look at the socket with. Two guests answering independently with
	// nothing per-VM configured anywhere is the evidence that wanted
	// asserting, and it is the loop below.
	for _, slot := range slots {
		var runCommand *mcp.Tool
		for i, tool := range toolsBySlot[slot] {
			if tool.Name == "run_command" {
				runCommand = &toolsBySlot[slot][i]
			}
		}
		if runCommand == nil {
			t.Fatalf("slot %s: no run_command tool returned", slot)
		}
		marker := fmt.Sprintf("grain-granule-concurrent-marker-%s", slot)
		var result mcp.Result
		deadline := time.Now().Add(2 * time.Minute)
		for {
			result = runCommand.Handler(context.Background(), map[string]any{"command": "echo " + marker})
			if !result.IsError {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("slot %s: run_command in the guest never succeeded within %s: %s", slot, 2*time.Minute, result.Text)
			}
			time.Sleep(2 * time.Second)
		}
		if !strings.Contains(result.Text, marker) {
			t.Errorf("slot %s: run_command output = %q, want it to contain %q", slot, result.Text, marker)
		}
	}
}

// TestGranuleSandboxesAgainstARealDockerBackedVM is the
// docker-exec transport's (GranuleConfig.DockerExec) counterpart to
// TestGranuleSandboxesAgainstARealDockerBackedVM above: the same
// real granule, real docker, real cloud-hypervisor guest under real
// KVM, reached through `docker exec <vm container> granule exec` instead
// of SSH to netshim's externally forwarded port.
//
// The fake-docker tests in granule_sandboxes_test.go can prove that
// nothing looks a VM's address up under this transport, because a fake
// can be made to fail that lookup. What they cannot prove is the half
// this test exists for, all of which depends on code neither this repo
// nor its fakes own:
//
//   - That `granule exec` reaches *this* guest image at all, with nothing
//     configured anywhere. Since bwsalmon/granule#46 that rests on a chain
//     nothing here owns and no fake can stand in for: `granule run`
//     attaches a virtio-vsock device and holds the host end as a unix
//     socket in the VM's container, the guest's own granule-agent listens
//     on the matching port, and the two agree on a wire format neither
//     side negotiates. Every link has to hold, and a break in any of them
//     looks identical from here: a guest that never becomes reachable.
//     This is the only test that can tell them apart from each other, or
//     from a guest that simply booted slowly.
//   - That a guest command's exit status survives both hops (the guest's
//     agent to `granule exec`, then `granule exec`'s own os.Exit to
//     `docker exec`), including the exit 1 that DockerExecRunner has to
//     tell apart from its own failure to reach the guest at all.
//   - That stdin survives both hops, which write_file depends on (it
//     pipes content into `dd` rather than embedding it in a command
//     line).
//
// It skips under exactly the same conditions the tests above do, and
// shares their build helpers, so it costs a host without docker/KVM
// nothing.
func TestGranuleSandboxesAgainstARealDockerBackedVM(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)

	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		CreateArgs: []string{
			// No -disk/-kernel/-initramfs, and no -images-hostpath: the
			// guest travels inside -granule-image.
			"-kontur-image", image,
			// Still 22, and still not optional -- granule's own default
			// is 80. This transport never goes through the DNAT rule that
			// forwards to it, but granule validates and netshim
			// installs the rule either way, so leaving it wrong would
			// only mean building a VM whose forwarded port goes nowhere.
			"-guest-port", "22",
			// The guest agent runs as root and drops to the account it
			// is asked for, so this is the account grain's commands run
			// as rather than an authorization decision. Same flag
			// scripts/setup.sh passes in a real deployment.
			"-guest-user", "debian",
			// No -ip/-port: this VM is built in flat mode (GranuleConfig's
			// own default since 7a58bec), where the container runtime
			// assigns the address the guest takes over and granule
			// rejects -ip outright -- "ip must not be set in \"flat\" net
			// mode". These were left behind when the default changed, and
			// failed every run of this test since. There is also nothing
			// left for them to collide with: each VM gets its own network
			// namespace under the docker backend, so the "distinct from
			// the concurrent test above" they used to carry was guarding
			// a collision that shape makes impossible.
			//
			// granule's disk mode defaults to read-only, which is fine
			// for a guest that only ever reads its disk -- but this test
			// also proves write_file/edit_file work over this transport
			// (below), and those need a root filesystem that is actually
			// writable. Confirmed by hand against a real VM without this:
			// `cat /proc/mounts` inside the guest reports "/dev/vda /
			// ext4 ro,relatime", and write_file fails with "Read-only
			// file system". overlay gives it one for free -- a thin qcow2
			// in the VM's own container, backed by the image's disk
			// (bwsalmon/granule#37) -- and is what scripts/setup.sh passes.
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
	})

	name, err := k.VMNameFor("e1-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})

	sb, err := k.Acquire(context.Background(), "e1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire over docker exec against a real granule/docker/cloud-hypervisor VM: %v", err)
	}
	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for i := range tools {
		byName[tools[i].Name] = &tools[i]
	}
	for _, want := range []string{"run_command", "read_file", "write_file", "edit_file"} {
		if byName[want] == nil {
			t.Fatalf("Tools() returned no %s tool (got %d tools)", want, len(tools))
		}
	}

	// This needs no retry loop around the first tool call. A readiness
	// wait that only watched a TCP port start answering -- what reaching
	// the guest over a forwarded port used to do -- would clear before
	// the guest had finished booting; waitForGuestReady's probe is a
	// whole command *running in the guest*, and one that only succeeds
	// once the guest has a default route, so Acquire returning here
	// already means both. Asserting that directly, rather
	// than retrying and hiding it, is what would catch that guarantee
	// regressing.
	result := byName["run_command"].Handler(context.Background(), map[string]any{
		"command": "echo grain-granule-dockerexec-marker; id -un; uname -s",
	})
	if result.IsError {
		t.Fatalf("run_command over docker exec failed on the first attempt, though Acquire had already run a command in this guest to decide it was ready: %s", result.Text)
	}
	for _, want := range []string{"grain-granule-dockerexec-marker", "debian", "Linux"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("run_command output = %q, want it to contain %q", result.Text, want)
		}
	}

	assertGuestHasEgress(t, byName["run_command"], name)
	assertGuestResolvesNames(t, byName["run_command"], name)
	assertSandboxDiskSizeApplies(t, k, stateDir, byName["run_command"])
	assertNestedVirtIsReported(t, k, "e1-1")

	// A guest command's own exit status has to survive the guest's agent
	// -> `granule exec` -> `docker exec` chain intact. 42 proves the
	// status is carried rather than collapsed to a success/failure bit;
	// 1 is the one that matters most, since it is exactly what
	// DockerExecRunner's own failure-to-reach-the-guest case also exits
	// with (see execFailedBeforeGuest) -- a -1 here would mean this
	// transport reports "the command never ran" for every command that
	// merely failed.
	for _, code := range []int{42, 1} {
		result := byName["run_command"].Handler(context.Background(), map[string]any{
			"command": fmt.Sprintf("exit %d", code),
		})
		if !result.IsError {
			t.Errorf("run_command `exit %d` reported success, want an error result", code)
		}
		if want := fmt.Sprintf("exit=%d", code); !strings.Contains(result.Text, want) {
			t.Errorf("run_command `exit %d` result = %q, want it to report %q", code, result.Text, want)
		}
	}

	// write_file pipes its content over stdin (sshWriteRemote's `dd`), so
	// a write/read round trip is what proves stdin survives both hops --
	// the one direction `docker exec` needs -i for, and the one a
	// command-line-only test would never exercise. The tab and the
	// trailing newline are there to catch a round trip that "works" but
	// mangles whitespace somewhere in the two shells between here and
	// that `dd`.
	//
	// The path is absolute rather than relative to Workspace: these two
	// tools pass file_path through to `cat`/`dd` verbatim (unlike
	// run_command, which cds into Workspace first), so a relative path
	// would resolve against whatever directory the guest's own login
	// leaves the session in.
	const content = "docker exec stdin round trip\nsecond line\twith a tab\n"
	const remotePath = "/home/debian/dockerexec-stdin.txt"
	if result := byName["write_file"].Handler(context.Background(), map[string]any{
		"file_path": remotePath, "content": content,
	}); result.IsError {
		t.Fatalf("write_file over docker exec: %s", result.Text)
	}

	// Byte-for-byte, through run_command: what `cat` prints lands in
	// run_command's own output unaltered, so this is the assertion that
	// actually holds stdin fidelity. read_file below cannot serve that
	// purpose -- it renders what it read as numbered lines
	// (numberedRange), which no longer contains the original text.
	catResult := byName["run_command"].Handler(context.Background(), map[string]any{
		"command": "cat -- " + remotePath,
	})
	if catResult.IsError {
		t.Fatalf("reading back what write_file piped over stdin: %s", catResult.Text)
	}
	if !strings.Contains(catResult.Text, content) {
		t.Errorf("guest file contents = %q, want it to carry back exactly what write_file piped in over stdin (%q)", catResult.Text, content)
	}

	// And through read_file itself, so the tool a dispatched task
	// actually calls is exercised over this transport too -- asserted per
	// line, since its output is numbered rather than raw.
	readBack := byName["read_file"].Handler(context.Background(), map[string]any{
		"file_path": remotePath,
	})
	if readBack.IsError {
		t.Fatalf("read_file over docker exec: %s", readBack.Text)
	}
	for _, want := range []string{"docker exec stdin round trip", "second line\twith a tab"} {
		if !strings.Contains(readBack.Text, want) {
			t.Errorf("read_file returned %q, want it to carry the line %q", readBack.Text, want)
		}
	}

	assertRebuildKeepsTheRunReachable(t, sb, byName["run_command"], name, remotePath)

	// The VM container should still be running: the guest booted all the
	// way to a listening agent and cloud-hypervisor supervises it rather
	// than exiting once it has -- reached without anything outside this
	// container ever connecting to it.
	statusOut, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", "granule-vm-"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect on the real VM container: %v\n%s", err, statusOut)
	}
	if status := string(bytes.TrimSpace(statusOut)); status != "running" {
		t.Errorf("real VM container status = %q, want %q", status, "running")
	}
}

// assertNestedVirtIsReported checks the nested-virtualization half of
// GranuleSandboxes.Health against a real guest: that the probe it adds to
// its one stats command actually runs there and comes back as one of the
// four states, and that the state is not the one the guest image is
// responsible for.
//
// It deliberately does not require "ready". Whether a cloud-hypervisor
// guest gets a CPU with vmx/svm at all depends on the machine underneath
// -- a hosted CI runner is itself a VM, and what its own hypervisor
// passes on is not this repository's to decide -- so "unsupported" here
// is a fact about the runner rather than a regression.
//
// "denied" is different, and is the one thing asserted: it means
// /dev/kvm is present and the account every tool call runs as cannot
// open it, which is exactly the state a guest image built without the
// Granulefile's kvm group grant is in, whatever the runner underneath
// can do. This job builds its guest from that Granulefile, so it is the
// one answer this test can attribute.
func assertNestedVirtIsReported(t *testing.T, k *orchestrator.GranuleSandboxes, sandbox string) {
	t.Helper()

	var health orchestrator.SandboxHealth
	for _, h := range k.Health(context.Background()) {
		if h.Sandbox == sandbox {
			health = h
		}
	}
	if health.Sandbox == "" {
		t.Fatalf("Health reported no entry for the live sandbox %q", sandbox)
	}
	t.Logf("nested virtualization in this guest: %q", health.NestedVirt)

	switch health.NestedVirt {
	case orchestrator.NestedVirtReady, orchestrator.NestedVirtNoDevice, orchestrator.NestedVirtUnsupported:
	case orchestrator.NestedVirtDenied:
		t.Errorf("NestedVirt = %q: /dev/kvm is there and the sandbox account cannot open it, "+
			"which means the Granulefile's kvm group grant did not reach this image",
			health.NestedVirt)
	default:
		t.Errorf("NestedVirt = %q, want one of the four states the probe prints -- "+
			"an empty value means it never ran in the guest at all", health.NestedVirt)
	}
}

// assertRebuildKeepsTheRunReachable drives granuleSandbox.Rebuild -- the
// SandboxRebuilder half of the recreate_sandbox tool (pkg/mcp), the one
// move a run has left when its sandbox has stopped being usable -- against
// a real VM, between two run_command calls made through the *same* tool
// handle.
//
// That is the claim Rebuild's own doc comment makes and that nothing
// else in this repo can check. Nothing replaces the transport a run is
// already holding, because a sandbox is addressed by name: `granule exec`
// reaches a guest by exec'ing into the VM's own container, whose name
// follows from the VM name alone (granule.PodName), so the runner this
// sandbox holds -- and the separate one the run's forked `grain
// mcpserver` holds in another process, which the daemon could not reach
// to replace even if it wanted to -- address whatever now answers to
// that name. Every other test of this feature stands on that being true:
// if it were not, a run that rebuilt its sandbox would come back to a VM
// it could not run a single command in, which is strictly worse than the
// wedged one it asked to be rid of. tests/e2e/mcpserver_recreate_sandbox_
// test.go makes the same argument for the host backend, where the name
// is a directory path.
//
// So this asserts three things a hand-written granule double cannot:
// that the container really was replaced (a different container id under
// the same name), that what is behind it is a genuinely new guest (the
// file written to the old one's root filesystem is gone), and that the
// handle taken before the rebuild still lands in it.
func assertRebuildKeepsTheRunReachable(t *testing.T, sb orchestrator.Sandbox, runCommand *mcp.Tool, vmName, writtenPath string) {
	t.Helper()

	rebuilder, ok := sb.(orchestrator.SandboxRebuilder)
	if !ok {
		t.Fatalf("a real granule sandbox (%T) does not implement orchestrator.SandboxRebuilder, "+
			"so no run on this backend could ever rebuild its own sandbox", sb)
	}
	before := dockerInspect(t, "granule-vm-"+vmName, "{{.Id}}")

	start := time.Now()
	if err := rebuilder.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild against a real granule/docker/cloud-hypervisor VM: %v", err)
	}
	t.Logf("rebuilt the real VM %q in %s", vmName, time.Since(start).Round(time.Second))

	if after := dockerInspect(t, "granule-vm-"+vmName, "{{.Id}}"); after == before {
		t.Errorf("the VM container id is unchanged (%s) -- Rebuild deletes this VM and creates "+
			"another under the same name, rather than restarting one", before)
	}

	// The same handle, taken before the rebuild and never replaced. It
	// needs no retry loop for the same reason the first tool call in this
	// test does not: Rebuild does not return until runnerFor has run a
	// command in the new guest, exactly as Acquire does.
	result := runCommand.Handler(context.Background(), map[string]any{
		"command": "echo grain-granule-rebuild-marker; id -un",
	})
	if result.IsError {
		t.Fatalf("run_command through the handle this run already held failed once its VM had been "+
			"rebuilt: %s", result.Text)
	}
	for _, want := range []string{"grain-granule-rebuild-marker", "debian"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("run_command output after the rebuild = %q, want it to contain %q", result.Text, want)
		}
	}

	// And it really is a new guest rather than the old one restarted:
	// what the guest wrote to its own (overlay-backed) root filesystem
	// before the rebuild is gone. That is the operation itself, not a
	// detail of it -- a sandbox worth throwing away is thrown away whole,
	// which is what lets a run trust that nothing it had broken came
	// back with it.
	if gone := runCommand.Handler(context.Background(), map[string]any{
		"command": "test ! -e " + writtenPath,
	}); gone.IsError {
		t.Errorf("%s survived the rebuild (%s), want a guest carrying nothing of the old one's filesystem",
			writtenPath, gone.Text)
	}
}

// dockerInspect reads one Go-template field off a container, failing the
// test if docker cannot see the container at all.
func dockerInspect(t *testing.T, container, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", format, container).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect -f %s %s: %v\n%s", format, container, err, out)
	}
	return string(bytes.TrimSpace(out))
}

// assertSandboxDiskSizeApplies is the far end of the disk-size setting
// (model.Config.SandboxDiskGB -> Shape.DiskGB ->
// GranuleConfig.createArgs' -disk-size-mb -> CHV_DISK_SIZE_MB -> the
// qcow2 overlay granule sizes before cloud-hypervisor opens it),
// asserted from inside a guest. That whole chain is only visible at once
// here, which is what makes it worth the extra VM: a fake granule can
// prove the flag was passed and nothing beyond it.
//
// It builds a second sandbox rather than sizing the one the rest of this
// test uses, because the size to ask for is not a number this test can
// write down. granule refuses a size below the disk image the overlay
// reads through to, and that image is this repo's guest -- which grows
// every time the Granulefile installs one more thing. So the size comes
// from the unsized guest already running: one GiB more than the disk it
// booted with, which is both certainly above granule's floor and small
// enough that growing the filesystem onto it costs a runner nothing.
//
// "Unsized" means "asked for no size of its own" rather than "as large
// as the guest image": every create carries grain's own default disk now
// (granule.DefaultDiskGB), so the first sandbox is that size and this one
// is a GiB past it. Reading the number off that guest rather than
// naming it keeps this test true either way.
//
// runCommand belongs to that first, unsized sandbox.
func assertSandboxDiskSizeApplies(t *testing.T, k *orchestrator.GranuleSandboxes, stateDir string, runCommand *mcp.Tool) {
	t.Helper()

	baseMB := guestRootDeviceMB(t, runCommand)
	wantGB := baseMB/1024 + 1
	wantMB := wantGB * 1024

	name, err := k.VMNameFor("e1-2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up the sized granule VM %q: %v", name, err)
		}
	})
	sb, err := k.Acquire(context.Background(), "e1-2", orchestrator.Shape{DiskGB: wantGB})
	if err != nil {
		t.Fatalf("Acquire of a sandbox asking for a %d GiB disk (the guest image's own %d MiB plus a GiB): %v", wantGB, baseMB, err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })

	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sizedRunCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			sizedRunCommand = &tools[i]
		}
	}
	if sizedRunCommand == nil {
		t.Fatalf("the sized sandbox returned no run_command tool (got %d tools)", len(tools))
	}

	// The block device first: this is the virtual size granule gave the
	// overlay, and it being unchanged from the unsized guest's means the
	// size never reached granule at all.
	if gotMB := guestRootDeviceMB(t, sizedRunCommand); gotMB < wantMB {
		t.Errorf("a sandbox created with -disk-size-mb %d has a %d MiB /dev/vda; the unsized one has %d MiB, so the size never reached the overlay",
			wantMB, gotMB, baseMB)
	}

	// Then the filesystem, which is a separate claim: `df` reports what
	// the guest can actually use, and that stays the image's own until
	// grain-growfs' resize2fs runs (the Granulefile installs it). A
	// device at the right size with a filesystem still at the image's is
	// the failure that looks like the setting having done nothing.
	//
	// Field 1 of `df -Pk` is the 1024-block total; -P is what guarantees
	// one line per filesystem with the fields in that order, which is why
	// this package's own health reading uses it too (health.go).
	result := sizedRunCommand.Handler(context.Background(), map[string]any{"command": "df -Pk / | tail -1"})
	if result.IsError {
		t.Fatalf("reading the sized guest's root filesystem: %s", result.Text)
	}
	fields := strings.Fields(guestStdout(t, result.Text))
	if len(fields) < 2 {
		t.Fatalf("guest df line = %q, want at least a device and a block total", result.Text)
	}
	blocksKB, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("parsing the guest's df total %q out of %q: %v", fields[1], result.Text, err)
	}
	// Not the full request: ext4's metadata means a filesystem grown onto
	// an N MiB device never reports N. Nine tenths is well under that
	// overhead and well over the unresized image's own size, which is the
	// distinction being drawn.
	if grownMB := int(blocksKB / 1024); grownMB < wantMB*9/10 {
		t.Errorf("the sized guest's root filesystem is %d MiB on a %d MiB device, want it grown onto most of the disk -- grain-growfs' resize2fs never ran or failed (see the VM's console)",
			grownMB, wantMB)
	}
}

// guestRootDeviceMB is the size of /dev/vda as the guest sees it, in
// MiB. /sys/block/vda/size is in 512-byte sectors and readable by any
// account, unlike the device itself.
func guestRootDeviceMB(t *testing.T, runCommand *mcp.Tool) int {
	t.Helper()
	result := runCommand.Handler(context.Background(), map[string]any{"command": "cat /sys/block/vda/size"})
	if result.IsError {
		t.Fatalf("reading the guest's root device size: %s", result.Text)
	}
	sectors, err := strconv.ParseInt(guestStdout(t, result.Text), 10, 64)
	if err != nil {
		t.Fatalf("parsing /sys/block/vda/size out of %q: %v", result.Text, err)
	}
	return int(sectors * 512 / (1024 * 1024))
}

// guestStdout is the command's own stdout out of a run_command result,
// which reports the exit status and both streams together
// ("exit=%d\nstdout:\n%s\nstderr:\n%s", mcp.NewSSHSandboxTools). Every
// other assertion in this file matches on a substring of the whole thing
// and so never had to care; one that parses a number does.
func guestStdout(t *testing.T, text string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "stdout:\n")
	if !ok {
		t.Fatalf("run_command result %q has no stdout section", text)
	}
	out, _, ok := strings.Cut(rest, "\nstderr:")
	if !ok {
		t.Fatalf("run_command result %q has no stderr section to bound stdout with", text)
	}
	return strings.TrimSpace(out)
}

// assertGuestResolvesNames covers the other half of "the guest has a
// network": egress to an address is not egress to a name.
//
// Every sandbox guest booted with no working DNS for as long as this
// image has existed. Its /etc/resolv.conf was whatever debootstrap copied
// off the machine that built the granule base -- on GitHub's own runners,
// the Azure host-only wireserver -- which is reachable in that host's
// network namespace and unroutable from a guest on a tap. Nothing here
// caught it, because nothing a run does needs a name: the git proxy and
// the UI are literal IPs, and `granule exec` reaches the guest by address.
// What it cost was every task that ran `apt`, `npm`, `pip`, `gcloud` or
// `curl` against an API, each failing with a timeout that reads as a
// blocked network rather than as a missing resolver.
//
// The resolver arrives on the guest's own ip= parameter now
// (netshim.DefaultDNS, granule's -dns, GRAIN_KONTUR_DNS), and
// granule-configure-dns writes it into /etc/resolv.conf on each boot. Both
// halves are asserted: the file, which is this repo's own contract, and
// then a real lookup, which is the thing that was actually broken.
func assertGuestResolvesNames(t *testing.T, runCommand *mcp.Tool, vmName string) {
	t.Helper()

	result := runCommand.Handler(context.Background(), map[string]any{
		"command": "cat /etc/resolv.conf",
	})
	if result.IsError {
		t.Fatalf("reading the guest's /etc/resolv.conf: %s", result.Text)
	}
	if !strings.Contains(result.Text, "nameserver") {
		t.Errorf("guest /etc/resolv.conf = %q, want it to name a nameserver -- without one the guest resolves nothing, however open its egress is", result.Text)
	}

	// A name this runner itself has to be able to resolve to have
	// checked out this repo at all, looked up with the guest's own
	// resolver and bounded so a broken one fails here rather than
	// hanging until the suite's own timeout.
	lookup := runCommand.Handler(context.Background(), map[string]any{
		"command": "for i in $(seq 1 5); do timeout 10 getent hosts github.com && exit 0; sleep 2; done; exit 1",
	})
	if lookup.IsError || !strings.Contains(lookup.Text, "github.com") {
		t.Errorf("resolving github.com inside the guest failed: %s\nthe guest has a resolver on paper (above) but cannot use it", lookup.Text)
		dumpGuestNetwork(t, vmName)
	}
}

// dumpGuestNetwork prints what the guest's own stack looks like, for a
// failure that says the guest cannot reach or resolve anything.
//
// Worth the lines because the symptom is now several removes from the
// cause: `granule exec` runs over vsock, so a guest whose NIC was never
// configured at all answers commands perfectly well and fails only on
// what needs the network -- the loud "unreachable guest" that used to
// stand for a broken ip= parameter is gone.
//
// The probes are chosen to separate the layers that can each produce the
// same silence, from the bottom up: whether the NIC exists at all
// (/sys/class/net, and the virtio_net module it needs, which on a distro
// kernel is a module and not built in), whether the boot parameters that
// describe it arrived, whether the units that act on them ran, and only
// then what the address and routing table ended up as.
// TestGranuleSandboxGuestNetworkIsolationAndBootLatency verifies §4 of
// docs/test-plans/2026-09-22.md:
//  1. Fresh Granule VM sandbox boot latency completes in < 20s (verifying the ~105s initramfs timeout bypass via RESUME=none).
//  2. Nested Docker operations inside the guest (docker run with ping to 10.240.0.1, docker build).
//  3. Concurrent network isolation: git clone / curl operations routed through host gateway proxy 172.30.255.1
//     while nested docker container traffic is active, with zero ECONNREFUSED or routing loops.
//  4. Runtime repair of conflicting guest docker network configuration (ensureGuestDockerNetwork).
func TestGranuleSandboxGuestNetworkIsolationAndBootLatency(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)

	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:     stateDir,
		GitProxyHost: "172.30.255.1",
		CreateArgs: []string{
			"-kontur-image", image,
			"-guest-port", "22",
			"-guest-user", "debian",
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
	})

	name, err := k.VMNameFor("e2e-net")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})

	// 1. Measure boot latency from sandbox request to agent readiness
	bootStart := time.Now()
	sb, err := k.Acquire(context.Background(), "e2e-net", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	bootDuration := time.Since(bootStart)
	t.Logf("Granule sandbox boot elapsed time: %v", bootDuration)

	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for i := range tools {
		byName[tools[i].Name] = &tools[i]
	}
	runCmd := byName["run_command"]
	if runCmd == nil {
		t.Fatal("no run_command tool returned")
	}

	// Verify initramfs resume timeout was bypassed (no 105s wait between virtio_blk and EXT4 root mount)
	dmesgRes := runCmd.Handler(context.Background(), map[string]any{
		"command": "dmesg | grep -E 'virtio_blk|EXT4-fs|Gave up waiting' | head -10",
	})
	if dmesgRes.IsError {
		t.Fatalf("dmesg failed: %s", dmesgRes.Text)
	}
	t.Logf("kernel mount dmesg:\n%s", guestStdout(t, dmesgRes.Text))
	if strings.Contains(dmesgRes.Text, "Gave up waiting for suspend/resume device") {
		t.Errorf("dmesg contains suspend/resume timeout stall")
	}

	// 2. Verify inner dockerd network configuration
	res := runCmd.Handler(context.Background(), map[string]any{
		"command": "cat /etc/docker/daemon.json",
	})
	if res.IsError || !strings.Contains(res.Text, "10.240.0.1/24") || !strings.Contains(res.Text, "10.241.0.0/16") {
		t.Fatalf("inner dockerd daemon.json unexpected: %s", res.Text)
	}

	// Start docker service and verify docker0 bridge address inside guest
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": "sudo -n systemctl start docker && ip -4 addr show docker0",
	})
	if res.IsError || !strings.Contains(res.Text, "10.240.0.1") {
		t.Fatalf("docker0 ip address unexpected: %s", res.Text)
	}
	t.Logf("docker0 bridge verified: %s", guestStdout(t, res.Text))

	// 3. Execute nested docker operations
	// 3a. docker run --rm alpine ping -c 1 10.240.0.1
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": "docker run --rm alpine ping -c 1 10.240.0.1",
	})
	if res.IsError {
		t.Fatalf("nested docker ping to docker0 gateway 10.240.0.1 failed: %s", res.Text)
	}
	t.Logf("nested docker ping result: %s", guestStdout(t, res.Text))

	// 3b. docker build inside guest
	buildScript := `mkdir -p /tmp/docker-build-test && cd /tmp/docker-build-test && ` +
		`printf 'FROM alpine\nRUN echo "grain-nested-build-verified" > /marker.txt\nCMD ["cat", "/marker.txt"]\n' > Dockerfile && ` +
		`docker build -t nested-test-img . && ` +
		`docker run --rm nested-test-img`
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": buildScript,
	})
	if res.IsError || !strings.Contains(res.Text, "grain-nested-build-verified") {
		t.Fatalf("nested docker build failed: %s", res.Text)
	}
	t.Logf("nested docker build successful")

	// 3c. Verify container MTU matches guest eth0 MTU
	mtuScript := `
		eth0_mtu=$(cat /sys/class/net/eth0/mtu)
		container_mtu=$(docker run --rm debian:bookworm-slim cat /sys/class/net/eth0/mtu 2>/dev/null || docker run --rm alpine cat /sys/class/net/eth0/mtu)
		if [ -z "$container_mtu" ] || [ "$eth0_mtu" != "$container_mtu" ]; then
			echo "MTU mismatch: eth0=$eth0_mtu container=$container_mtu" >&2
			exit 1
		fi
		echo "container MTU matches guest eth0: $container_mtu"
	`
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": mtuScript,
	})
	if res.IsError || !strings.Contains(res.Text, "container MTU matches guest eth0") {
		t.Fatalf("nested container MTU check failed: %s", res.Text)
	}
	t.Logf("nested container MTU verified: %s", guestStdout(t, res.Text))

	// 4. Concurrent network operations and isolation
	// Concurrently run container network pings while downloading/curling through host gateway
	concurrentScript := `
		( for i in $(seq 1 5); do docker run --rm alpine ping -c 1 10.240.0.1 >/dev/null 2>&1; sleep 0.1; done ) &
		bg_pid=$!

		curl -fsSL --max-time 10 https://deb.debian.org >/dev/null || exit 1
		curl -fsSL --max-time 10 https://github.com >/dev/null || exit 1

		wait $bg_pid
		echo "concurrent-network-success"
	`
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": concurrentScript,
	})
	if res.IsError || !strings.Contains(res.Text, "concurrent-network-success") {
		t.Fatalf("concurrent network execution failed: %s", res.Text)
	}
	t.Logf("concurrent network isolation verified cleanly")

	// 5. Runtime repair logic (ensureGuestDockerNetwork)
	// Simulate conflicting docker configuration on 172.30.255.1/24
	breakScript := `
		sudo -n sh -c 'printf "{\n  \"bip\": \"172.30.255.1/24\",\n  \"default-address-pools\": [{\"base\": \"172.31.0.0/16\", \"size\": 24}]\n}\n" > /etc/docker/daemon.json' && \
		sudo -n systemctl restart docker && \
		ip -4 addr show docker0
	`
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": breakScript,
	})
	if res.IsError || !strings.Contains(res.Text, "172.30.255.1") {
		t.Fatalf("failed to stage conflicting docker0 bridge: %s", res.Text)
	}

	// Trigger repair via ensureGuestDockerNetwork
	k.EnsureGuestDockerNetworkForTest(context.Background(), name)

	// Verify repair restored daemon.json and docker0 to 10.240.0.1/24
	repairCheckScript := `
		for i in $(seq 1 30); do
			if docker info >/dev/null 2>&1; then break; fi
			sleep 0.5
		done
		cat /etc/docker/daemon.json && \
		docker run --rm alpine ping -c 1 10.240.0.1 && \
		ip -4 addr show docker0
	`
	res = runCmd.Handler(context.Background(), map[string]any{
		"command": repairCheckScript,
	})
	if res.IsError || !strings.Contains(res.Text, "10.240.0.1/24") || !strings.Contains(res.Text, "10.240.0.1") {
		t.Fatalf("ensureGuestDockerNetwork failed to repair guest docker network: %s", res.Text)
	}
	t.Logf("ensureGuestDockerNetwork runtime repair successfully verified")
}

func dumpGuestNetwork(t *testing.T, vmName string) {
	t.Helper()

	// Root, and through the container directly rather than through the
	// sandbox's own run_command: these read /proc, the module list and
	// the journal, none of which the unprivileged sandbox account can
	// see, and half of them live in /usr/sbin, which is not on that
	// account's PATH. KONTUR_EXEC_USER is what picks the guest account
	// (pkg/mcp's DockerExecRunner passes the same variable).
	for _, cmd := range []string{
		"ls -l /sys/class/net",
		"ip -4 addr show; ip -4 route show",
		"cat /proc/cmdline",
		"cat /proc/1/comm; uname -r; ls /lib/modules",
		"grep -E 'virtio' /proc/modules || echo 'no virtio module loaded'",
		"ls -l /etc/systemd/system/sysinit.target.wants/",
		"systemctl --no-pager --full is-active granule-net-cmdline granule-net-routes granule-configure-dns granule-agent",
		"journalctl -b --no-pager -u granule-net-cmdline -u granule-net-routes -u granule-configure-dns | tail -60",
		"cat /etc/resolv.conf",
	} {
		out, err := exec.Command("docker", "exec",
			"-e", "GRANULE_EXEC_USER=root", "granule-vm-"+vmName,
			"granule", "exec", "--", "sh", "-c", cmd).CombinedOutput()
		t.Logf("guest (root) %q: err=%v\n%s", cmd, err, out)
	}
}

// assertGuestHasEgress checks the one hop nothing else here covers: that
// the guest can reach anything at all beyond its own segment.
//
// A flat-mode guest takes over the identity the container runtime
// assigned the namespace, and the last piece of that identity is the
// namespace's default route -- which the guest can only learn from the
// "ip=" parameter netshim derives for it (its own DiscoverIdentity and
// GuestConfig, in third_party/granule). A missing gateway fails silently:
// the VM boots, every tool call in this test passes -- and the run inside
// it cannot reach the git proxy, a package registry, or GitHub, because
// the guest's routing table stops at its own subnet.
//
// That silence got deeper, not shallower, when `granule exec` moved to
// vsock. A guest with the wrong address or MAC used to be a guest
// `granule exec` could not reach, so the whole identity takeover was
// covered by readiness alone. Now nothing about the guest's networking
// is on the path to running a command in it, and these two assertions
// are the only thing standing between a guest with no network at all and
// a green run.
//
// That is exactly what happened: netshim looked for the default route by
// testing "Dst == nil", which a route read back off the kernel never has
// (the netlink library synthesizes 0.0.0.0/0 for the missing RTA_DST the
// way iproute2 does), so every guest booted with an empty gateway field
// and no egress. It survived because nothing asserted it.
//
// The gateway is compared against the one docker itself reports for the
// VM's own network namespace, rather than a literal, so this says "the
// guest took over the namespace's default route" rather than "the runner
// happened to use 172.17.0.1".
func assertGuestHasEgress(t *testing.T, runCommand *mcp.Tool, vmName string) {
	t.Helper()

	// Ranged over rather than dot-accessed: a container with no legacy
	// single-network attachment has no .NetworkSettings.Gateway at all,
	// and the template would error out ("map has no entry for key")
	// rather than return empty -- the same trap granule.DockerPodIP's own
	// template already hit against a real daemon (bwsalmon/agents#466).
	// The netns holder is the container docker addressed; the VM
	// container merely joined its namespace.
	gwOut, err := exec.Command("docker", "inspect", "-f",
		`{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}`,
		"granule-vm-"+vmName).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect on the VM container: %v\n%s", err, gwOut)
	}
	// Fatal rather than a skip: this suite creates its VMs on docker's
	// default bridge, which always has one, and a skip here would take
	// the rest of this test with it (t.SkipNow on the parent) while
	// reading like coverage -- the exact trade the real-vm job's own
	// "--- PASS" assertion exists to refuse.
	gateway := string(bytes.TrimSpace(gwOut))
	if gateway == "" {
		t.Fatalf("docker reports no gateway for this VM's network namespace, so the guest has nowhere to route out through at all")
	}

	result := runCommand.Handler(context.Background(), map[string]any{
		"command": "ip -4 route show default",
	})
	if result.IsError {
		t.Fatalf("reading the guest's default route: %s", result.Text)
	}
	if !strings.Contains(result.Text, "default via "+gateway) {
		t.Errorf("guest default route = %q, want it to carry %q -- without it the guest has no route off its own segment, and so no egress at all",
			strings.TrimSpace(result.Text), "default via "+gateway)
		dumpGuestNetwork(t, vmName)
	}
}

// TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM proves that NetworkNone
// isolation is enforced outside the guest VM (issue #2302): even if a guest
// root executes `ip route add default` inside the VM, outbound traffic to the
// external internet is blocked and dropped by host firewall rules (nftables),
// while local subnet access to the host gateway and git proxy continues to work.
// It also opens real connections to the host at a non-gateway address and to
// a neighbouring container on the same bridge, the two holes grain task 1449
// closed, so a regression in either the input-hook or the bridge-family rules
// fails here rather than only in granule_firewall_test.go's ruleset checks.
// It runs in nightly.yml's "real KVM sandboxes" job.
func TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM(t *testing.T) {
	granuleDockerRealTestPrereqs(t)
	// The host's own nftables rules, not TestMain's stand-in: they are
	// what this test is about.
	t.Cleanup(orchestrator.UseRealHostFirewall())

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)

	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Two host services on the wildcard address, so both are listening
	// on the docker gateway: one the deployment serves sandboxes on, and
	// one standing in for sshd or a local HTTP proxy that it does not.
	service := serveOnWildcard(t, "grain-sandbox-service")
	other := serveOnWildcard(t, "not-a-sandbox-service")

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		CreateArgs: []string{
			"-kontur-image", image,
			"-guest-port", "22",
			"-guest-user", "debian",
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
		SandboxServicePorts: func() []orchestrator.SandboxServicePort {
			return []orchestrator.SandboxServicePort{{Proto: "tcp", Port: service}}
		},
	})

	name, err := k.VMNameFor("none-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})

	sb, err := k.Acquire(context.Background(), "none-1", orchestrator.Shape{
		NetworkAccess: model.NetworkNone,
	})
	if err != nil {
		t.Fatalf("Acquire NetworkNone VM: %v", err)
	}
	defer sb.Release(context.Background())

	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var runCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			runCommand = &tools[i]
			break
		}
	}
	if runCommand == nil {
		t.Fatal("no run_command tool returned")
	}

	// 1. Verify default route was removed inside the guest by enforceGuestNetwork:
	routeRes := runCommand.Handler(context.Background(), map[string]any{
		"command": "ip -4 route show default",
	})
	if routeRes.IsError {
		t.Fatalf("reading guest default route: %s", routeRes.Text)
	}
	if guestStdout(t, routeRes.Text) != "" {
		t.Fatalf("expected empty default route, got: %q", routeRes.Text)
	}

	// 2. Obtain the host gateway:
	gwOut, err := exec.Command("docker", "inspect", "-f",
		`{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}`,
		"granule-vm-"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect: %v\n%s", err, gwOut)
	}
	gateway := string(bytes.TrimSpace(gwOut))
	if gateway == "" {
		t.Fatal("no gateway found for container")
	}

	// 3. Inside the guest, simulate rogue root re-adding the default route:
	addRes := runCommand.Handler(context.Background(), map[string]any{
		"command": "sudo -n ip route add default via " + gateway + " dev eth0",
	})
	if addRes.IsError {
		t.Fatalf("re-adding default route inside guest: %s", addRes.Text)
	}

	// Verify the route is installed in guest kernel:
	routeCheck := runCommand.Handler(context.Background(), map[string]any{
		"command": "ip -4 route show default",
	})
	if !strings.Contains(routeCheck.Text, "default via "+gateway) {
		t.Fatalf("expected default route installed inside guest, got: %q", routeCheck.Text)
	}

	// 4. Verify host firewall blocks outbound packets to external destinations (e.g. 8.8.8.8, 1.1.1.1):
	extRes := runCommand.Handler(context.Background(), map[string]any{
		"command": "curl --connect-timeout 2 -s http://8.8.8.8/ || curl --connect-timeout 2 -s http://1.1.1.1/",
	})
	if !extRes.IsError {
		t.Errorf("expected external curl to fail due to host firewall dropping outbound packets, but succeeded: %s", extRes.Text)
	}

	// 5. The host is reachable at the gateway and nowhere else. The
	// sandbox service listener is on the wildcard address, so it answers
	// both: the gateway (where the git and model proxies are advertised)
	// must reach it, and the
	// host's own outbound address -- what `ip route get 8.8.8.8` reports
	// as src on the host, the public interface on a real deployment --
	// must not. That second connection never crosses the inet forward
	// hook, only input, so it is what proves the VM chain is jumped to
	// from input as well.
	hostPort := strconv.Itoa(service)

	if ok, out := guestTCPConnect(t, runCommand, gateway, hostPort); !ok {
		t.Errorf("a host listener at the gateway %s is not reachable from a NetworkNone guest, so neither are the git and model proxies: %s", gateway, out)
	}

	hostSrc := hostOutboundAddress(t)
	if hostSrc == gateway {
		t.Fatalf("the host's outbound address %s is the docker gateway itself, so there is no non-gateway host address to test", hostSrc)
	}
	if ok, out := guestTCPConnect(t, runCommand, hostSrc, hostPort); ok {
		t.Errorf("a NetworkNone guest reached a host listener at the host's non-gateway address %s (the inet input hook is not dropping it): %s", hostSrc, out)
	}

	// 5b. A neighbouring container on the VM's own docker bridge is out of
	// reach too. These frames are bridged, not routed: docker 29 loads no
	// br_netfilter for a network with inter-container traffic on, so the
	// inet forward hook never sees them and only the bridge-family copy of
	// the VM's chain stands in the way.
	neighbourIP := startBridgeNeighbour(t, "granule-vm-"+name)
	// The host can reach it, so a guest that cannot is being dropped, not
	// talking to a container that never started listening.
	waitForNeighbour(t, neighbourIP)
	if ok, out := guestTCPConnect(t, runCommand, neighbourIP, "8080"); ok {
		t.Errorf("a NetworkNone guest reached a neighbouring container at %s on its own docker bridge (the bridge forward hook is not dropping it): %s", neighbourIP, out)
	}

	// 5c. At the gateway only the deployment's own service ports answer:
	// the one it serves sandboxes on does, and the other wildcard-bound
	// service on the same address does not.
	serviceRes := runCommand.Handler(context.Background(), map[string]any{
		"command": fmt.Sprintf("curl --connect-timeout 5 -sS http://%s:%d/", gateway, service),
	})
	if serviceRes.IsError || !strings.Contains(serviceRes.Text, "grain-sandbox-service") {
		t.Errorf("a sandbox service port on the gateway was not reachable from the NetworkNone guest: %s", serviceRes.Text)
	}
	otherRes := runCommand.Handler(context.Background(), map[string]any{
		"command": fmt.Sprintf("curl --connect-timeout 3 -sS http://%s:%d/", gateway, other),
	})
	if !otherRes.IsError || strings.Contains(otherRes.Text, "not-a-sandbox-service") {
		t.Errorf("a host service that is not a sandbox service answered the NetworkNone guest at the gateway: %s", otherRes.Text)
	}

	// 6. The host jumps to the VM's chain from its container's own bridge
	// port, whatever the source address, and a guest sending from an
	// address it was not given still gets nowhere:
	prerouting, err := exec.Command("sudo", "-n", "nft", "list", "chain", "bridge", "grain_network_none", "prerouting").CombinedOutput()
	if err != nil {
		t.Logf("cannot list the host's bridge prerouting chain (%v): %s", err, prerouting)
	} else if !strings.Contains(string(prerouting), "iifname") || !strings.Contains(string(prerouting), "jump vm_") {
		t.Errorf("host bridge prerouting chain has no port jump for the VM:\n%s", prerouting)
	}
	spoofRes := runCommand.Handler(context.Background(), map[string]any{
		"command": "a=$(ip -4 -o addr show dev eth0 | awk '{print $4}' | head -1); " +
			"spoof=${a%.*}.250; sudo -n ip addr add $spoof/${a#*/} dev eth0 && " +
			"curl --interface $spoof --connect-timeout 3 -s http://1.1.1.1/",
	})
	if !spoofRes.IsError {
		t.Errorf("a guest sending from a spoofed source address reached the internet: %s", spoofRes.Text)
	}
}

// guestTCPConnect opens a TCP connection from inside the guest with
// bash's /dev/tcp, so the check needs no client the guest might lack. It
// fails the test outright when the probe itself could not run: a missing
// binary would otherwise read as "not reachable" and pass every check that
// a connection must fail.
func guestTCPConnect(t *testing.T, runCommand *mcp.Tool, host, port string) (ok bool, out string) {
	t.Helper()
	res := runCommand.Handler(context.Background(), map[string]any{
		"command": "timeout 5 bash -c 'exec 3<>/dev/tcp/" + host + "/" + port + "'",
	})
	if strings.Contains(res.Text, "exit=126") || strings.Contains(res.Text, "exit=127") {
		t.Fatalf("the guest could not run the connection probe to %s:%s: %s", host, port, res.Text)
	}
	return !res.IsError, res.Text
}

// hostOutboundAddress is the source address the host itself would use to
// reach the internet: on a real deployment, its public interface.
func hostOutboundAddress(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ip", "-4", "route", "get", "8.8.8.8").CombinedOutput()
	if err != nil {
		t.Fatalf("ip -4 route get 8.8.8.8 on the host: %v\n%s", err, out)
	}
	fields := strings.Fields(string(out))
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "src" {
			return fields[i+1]
		}
	}
	t.Fatalf("ip -4 route get 8.8.8.8 on the host reported no src address: %s", out)
	return ""
}

// startBridgeNeighbour runs a busybox HTTP server on port 8080 in a
// container on the same docker network as vmContainer, and returns its
// IPv4 address on that network.
func startBridgeNeighbour(t *testing.T, vmContainer string) string {
	t.Helper()
	netOut, err := exec.Command("docker", "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}`, vmContainer).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect %s networks: %v\n%s", vmContainer, err, netOut)
	}
	network := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(netOut)), "\n", 2)[0])
	if network == "" {
		t.Fatalf("%s is on no docker network", vmContainer)
	}
	neighbour := vmContainer + "-neighbour"
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", neighbour, "--network", network,
		"busybox", "sh", "-c", "mkdir -p /www && echo neighbour-ok > /www/index.html && exec httpd -f -p 8080 -h /www").CombinedOutput()
	if err != nil {
		t.Fatalf("starting a neighbour container on %s: %v\n%s", network, err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", neighbour).Run() })
	ipOut, err := exec.Command("docker", "inspect", "-f",
		`{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, neighbour).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect %s: %v\n%s", neighbour, err, ipOut)
	}
	ip := strings.TrimSpace(string(ipOut))
	if ip == "" {
		t.Fatalf("neighbour container %s has no IPv4 address on %s", neighbour, network)
	}
	return ip
}

// waitForNeighbour waits until the host can fetch the neighbour's page.
func waitForNeighbour(t *testing.T, ip string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + net.JoinHostPort(ip, "8080") + "/"
	var lastErr error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		return
	}
	t.Fatalf("the host cannot reach the neighbour container at %s either, so the guest's failure to would prove nothing: %v", url, lastErr)
}

// serveOnWildcard serves body over HTTP on an ephemeral port of every
// host address -- the docker gateway included -- for the rest of the
// test, and returns the port.
func serveOnWildcard(t *testing.T, body string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// buildGranuleBaseImage builds the vendored granule image and returns its
// reference, without packing a guest onto it.
//
// It is the first half of buildGranuleGuestImage, split out for the tests
// that want a VM that boots rather than a VM carrying grain's whole agent
// toolchain. That image is minutes of build and, on a non-x86_64 host,
// currently impossible: some of its downloads pin a checksum for the
// x86_64 asset, so the URL being architecture-aware is not sufficient.
// granule's own bundled guest is Debian with an sshd and a root account,
// which is everything the sandbox transport needs.
func buildGranuleBaseImage(t *testing.T) (image string) {
	t.Helper()
	image = "granule:withguest"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		return image
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root absolute path: %v", err)
	}
	// A whole build, deliberately not scripts/granule/build-granule-image.sh:
	// that script builds `--target runtime`, which is the VMM half alone and
	// "deliberately carries no kernel: it is the VMM half for guests that
	// bring their own" (granule's own pkg/guestpack says so when a pack has
	// no kernel to find). A VM created from it dies immediately with
	// "disk[0] /var/lib/granule/guest/disk.img: no such file or directory",
	// because there is no guest in it to boot.
	//
	// The untargeted build is what granule's README tells its own readers to
	// make, and it carries the bundled guest -- kernel, rootfs and all -- at
	// the paths pkg/config's defaults already name. That is the guest this
	// wants: one that boots, with nothing packed onto it.
	cmd := exec.Command("docker", "build", "-t", image, "third_party/granule")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s from third_party/granule: %v\n%s", image, err, out)
	}
	return image
}
