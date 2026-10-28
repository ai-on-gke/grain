package orchestrator_test

// TestGranuleSandboxAddressesAgainstRealVMs is the live counterpart to the
// shell-script doubles in granule_sandboxes_test.go: it drives
// GranuleSandboxes with a real sandboxaddr pool against the real granule
// and docker binaries and real cloud-hypervisor VMs under real KVM, and
// asks the guests themselves what address they ended up with.
//
// That question is the whole point. Every other test of this can only
// check which arguments grain handed granule; none of them can tell
// whether an address given to "docker run --ip" actually arrives inside a
// guest. It does so only because granule's flat mode has the guest take
// over the address of the container it boots inside -- a chain of three
// assumptions (grain names it to docker, docker assigns it to the
// container, the guest adopts it from the container) that nothing short of
// asking the guest can confirm.
//
// It skips unless docker and /dev/kvm are both usable, the same gate
// granuleDockerRealTestPrereqs applies to the other live suites here.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sandboxaddr"
)

// addressPoolCIDR is the pool these VMs are addressed out of.
//
// Deliberately not 172.17.0.0/16 and not anything a default docker
// install uses: this test creates a real user-defined docker network for
// it on whatever host runs the suite, and a subnet that collided with an
// existing one would fail the create rather than the assertion.
const addressPoolCIDR = "10.188.0.0/16"

// addressTestNetwork is the docker network name. Distinct per run so two
// invocations on one host -- or a run that died before its cleanup -- do
// not collide over a network whose subnet is already taken.
func addressTestNetwork(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("grain-addr-test-%d", time.Now().UnixNano())
}

func TestGranuleSandboxAddressesAgainstRealVMs(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	// granule's own bundled guest, not grain's agent guest image.
	//
	// What is under test is whether an address grain gives docker reaches
	// the guest, and any booting guest answers that. grain's own guest
	// (buildGranuleGuestImage) additionally carries go, node, kind,
	// playwright and the agent CLIs, costs minutes to build, and -- today
	// -- cannot be built for arm64 at all: several of its downloads pin a
	// checksum for the x86_64 tarball, so parameterising the URLs is not
	// enough. Depending on it here would make this test unrunnable on a
	// whole architecture for reasons that have nothing to do with
	// addressing.
	//
	// The bundled guest is Debian with an sshd and a root account, which is
	// what "granule exec" needs and so what the sandbox transport needs.
	image := buildGranuleBaseImage(t)
	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool, err := sandboxaddr.NewPool(addressPoolCIDR)
	if err != nil {
		t.Fatal(err)
	}
	network := addressTestNetwork(t)
	t.Cleanup(func() {
		// After the VMs: docker refuses to remove a network with
		// endpoints still on it, and a leaked network holds this subnet
		// against the next run.
		_ = runDocker(t, "network", "rm", network)
	})

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: t.TempDir(),
		CreateArgs: []string{
			"-kontur-image", image,
			"-guest-user", "root",
			"-disk-mode=overlay",
		},
		// The bundled guest creates only root (granule's own guest image
		// does not add "debian" the way grain's does).
		SSHUser:           "root",
		Workspace:         "/root",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
		Addresses:         pool,
		DockerNetwork:     network,
		// A deliberately small shape, because grain's own default is 2 vCPU
		// and 8 GiB and this test wants three VMs at once -- 24 GiB, which
		// is more than a modest host has, and asking for it does not fail
		// cleanly: the first guest took 97 seconds to reach userspace under
		// the resulting pressure and the second never answered at all,
		// which reads like a networking fault and is not one.
		//
		// Nothing here is about sizing. What these guests have to do is
		// boot, hold an address and answer one command, and the bundled
		// guest does all three in about a second at this size.
		DefaultCPUs:     1,
		DefaultMemoryMB: 512,
		DefaultDiskGB:   2,
	})

	// Two roots and one child of the first. The child is a sandbox created
	// by another sandbox: a sibling VM on the same host, sharing the
	// parent's family block rather than running inside it.
	parent := acquireAddressed(t, k, "", "a1-1")
	other := acquireAddressed(t, k, "", "a2-1")
	child := acquireAddressed(t, k, "a1-1", "a1-2")

	// --- the addresses are the ones grain chose ------------------------
	for _, sb := range []addressedSandbox{parent, other, child} {
		want, ok := pool.Addr(sb.sandbox)
		if !ok {
			t.Fatalf("%s has no address in the pool", sb.sandbox)
		}
		// From inside the guest, not from docker: this is the assertion
		// the doubles cannot make.
		got := guestAddresses(t, sb.runCommand)
		if !strings.Contains(got, want.String()) {
			t.Errorf("guest %s holds %q, want it to carry its allocated %s",
				sb.sandbox, got, want)
		}
	}

	// --- the family block is what the parent and child share -----------
	family, ok := pool.FamilyPrefix("a1-1")
	if !ok {
		t.Fatal("no family for a1-1")
	}
	parentAddr, _ := pool.Addr("a1-1")
	childAddr, _ := pool.Addr("a1-2")
	otherAddr, _ := pool.Addr("a2-1")
	if !family.Contains(parentAddr) || !family.Contains(childAddr) {
		t.Errorf("family %s does not hold both %s and %s", family, parentAddr, childAddr)
	}
	if family.Contains(otherAddr) {
		t.Errorf("unrelated sandbox a2-1 at %s fell inside a1-1's family %s", otherAddr, family)
	}

	// --- sibling reachability -----------------------------------------
	//
	// model.NetworkNone promises "no route off its own segment", so a
	// sandbox reaching another on the same segment is the documented
	// behaviour rather than a leak -- and it is what makes a sub-sandbox
	// usable by the sandbox that created it. These VMs were acquired with
	// no NetworkNone shape, so nothing is enforcing isolation here at all;
	// what is under test is that the addresses are real and routable.
	assertGuestReaches(t, parent.runCommand, childAddr.String(), "its own child")
	assertGuestReaches(t, child.runCommand, parentAddr.String(), "its parent")
	assertGuestReaches(t, parent.runCommand, otherAddr.String(), "an unrelated sandbox")
	// And the probe can say no: an address inside the pool that nothing
	// was allocated. Without this the three above pass on any probe that
	// always reports failure, which is how a broken one went unnoticed.
	unallocated := pool.Prefix().Addr()
	for i := 0; i < 200; i++ {
		unallocated = unallocated.Next()
	}
	assertGuestDoesNotReach(t, parent.runCommand, unallocated.String(), "an unallocated address")

	// --- releasing the parent frees the whole family -------------------
	if err := parent.handle.Release(context.Background()); err != nil {
		t.Fatalf("releasing the parent: %v", err)
	}
	if addr, ok := pool.Addr("a1-1"); ok {
		t.Errorf("the parent still holds %s after Release", addr)
	}
	if addr, ok := pool.Addr("a1-2"); ok {
		t.Errorf("the child still holds %s after its parent was released -- a sub-sandbox's address "+
			"outliving the family is a block this deployment can never hand out again", addr)
	}
	// The unrelated sandbox is untouched, which is what makes this a
	// family teardown rather than a pool reset.
	if _, ok := pool.Addr("a2-1"); !ok {
		t.Error("an unrelated sandbox lost its address when another family was released")
	}
}

// addressedSandbox is one acquired VM and the tool that runs commands in
// its guest.
type addressedSandbox struct {
	sandbox    string
	handle     orchestrator.Sandbox
	runCommand *mcp.Tool
}

// acquireAddressed acquires sandbox -- under parent when one is named --
// and returns it with its run_command tool resolved.
func acquireAddressed(t *testing.T, k *orchestrator.GranuleSandboxes, parent, sandbox string) addressedSandbox {
	t.Helper()
	ctx := context.Background()

	var sb orchestrator.Sandbox
	var err error
	if parent == "" {
		sb, err = k.Acquire(ctx, sandbox, orchestrator.Shape{})
	} else {
		sb, err = k.AcquireUnder(ctx, parent, sandbox, orchestrator.Shape{})
	}
	if err != nil {
		t.Fatalf("acquiring %s (parent %q): %v", sandbox, parent, err)
	}
	t.Cleanup(func() {
		// Release is idempotent enough for this: the test releases the
		// parent itself to assert the cascade, and a second Release of a
		// VM that is already gone returns nil (granule.Exists is false).
		_ = sb.Release(context.Background())
	})

	tools, err := sb.Tools(ctx)
	if err != nil {
		t.Fatalf("tools for %s: %v", sandbox, err)
	}
	var runCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			runCommand = &tools[i]
			break
		}
	}
	if runCommand == nil {
		t.Fatalf("no run_command tool for %s", sandbox)
	}
	return addressedSandbox{sandbox: sandbox, handle: sb, runCommand: runCommand}
}

// guestAddresses is every IPv4 address the guest holds, as the guest
// reports them.
func guestAddresses(t *testing.T, runCommand *mcp.Tool) string {
	t.Helper()
	return guestStdout(t, runGuest(t, runCommand, "ip -4 -o addr show || hostname -I"))
}

// guestReaches reports whether the guest can reach addr at the IP layer.
//
// Three things make this less obvious than it looks, each of which had this
// answering "unreachable" for every address including ones that were up:
//
//   - Nothing listens inside these guests. "granule exec" arrives over
//     vsock, not ssh (granule's own boot line: --vsock cid=3,socket=...),
//     so there is no port to connect to -- `ss -ltn` in the guest is empty.
//     So the success signal is a *refusal*: a RST is a reply, and a reply
//     means the host was reached. A host that is not there answers with "no
//     route to host" or nothing at all.
//   - /bin/sh in the guest is dash, and /dev/tcp is a bash feature. Run
//     through sh it fails with "cannot open /dev/tcp/...: No such file",
//     which looks exactly like an unreachable host and is not one.
//   - ping is not installed, so ICMP is not the easier route it would
//     usually be.
//
// Port 9 (discard) is used because it is closed everywhere and reserved, so
// nothing can be listening on it by accident on some future guest and turn
// a refusal into an accept.
func guestReaches(t *testing.T, runCommand *mcp.Tool, addr string) bool {
	t.Helper()
	out := guestStdout(t, runGuest(t, runCommand, fmt.Sprintf(
		`out=$( { timeout 5 bash -c "exec 3<>/dev/tcp/%s/9"; } 2>&1 ); `+
			`if [ $? -eq 0 ]; then echo GUEST_REACHABLE; `+
			`elif echo "$out" | grep -qi refused; then echo GUEST_REACHABLE; `+
			`else echo "GUEST_UNREACHABLE $out"; fi`, addr)))
	return strings.Contains(out, "GUEST_REACHABLE")
}

// assertGuestReaches is guestReaches with the failure message.
func assertGuestReaches(t *testing.T, runCommand *mcp.Tool, addr, what string) {
	t.Helper()
	if !guestReaches(t, runCommand, addr) {
		t.Errorf("guest could not reach %s at %s", what, addr)
	}
}

// assertGuestDoesNotReach is the negative control. Without it every
// assertion above passes on a probe that cannot fail -- which is exactly
// what happened when the probe was reporting a dash limitation as an
// unreachable host.
func assertGuestDoesNotReach(t *testing.T, runCommand *mcp.Tool, addr, what string) {
	t.Helper()
	if guestReaches(t, runCommand, addr) {
		t.Errorf("guest reached %s at %s, which nothing is on", what, addr)
	}
}

// runGuest runs one shell command in the guest and returns the tool's
// result text.
func runGuest(t *testing.T, runCommand *mcp.Tool, command string) string {
	t.Helper()
	result := runCommand.Handler(context.Background(), map[string]any{"command": command})
	if result.IsError {
		t.Fatalf("guest command %q failed: %s", command, result.Text)
	}
	return result.Text
}

// runDocker runs one docker command, returning its combined output. Used
// only for cleanup, so a failure is reported and not fatal.
func runDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Logf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
