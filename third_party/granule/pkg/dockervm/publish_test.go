package dockervm

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/vmspec"
)

// publishSpec is testSpec with the grant EXPOSE needs to mean anything.
func publishSpec() vmspec.VMSpec {
	s := testSpec()
	s.PublishDeclaredPorts = true
	return s
}

// containsArgPair reports whether call contains flag immediately
// followed by value, which is how a "-p 8080/tcp" is told apart from a
// "-p" and an "8080/tcp" that merely both appear.
func containsArgPair(call []string, flag, value string) bool {
	for i, a := range call {
		if a == flag && i+1 < len(call) && call[i+1] == value {
			return true
		}
	}
	return false
}

// lastCall is the "docker run" Create ends with, which is the one every
// assertion about the VM container is about.
func lastCall(t *testing.T, calls [][]string) []string {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no docker calls at all")
	}
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i][0] == "run" {
			return calls[i]
		}
	}
	t.Fatalf("no `docker run` among %v", calls)
	return nil
}

// TestCreate_PublishesDeclaredPorts is the granting half of EXPOSE: a
// declaration in the image plus a caller who said yes to it comes out as
// publication on the VM's own container, which is the namespace the
// guest takes over -- so the declared port really is reachable rather
// than merely recorded somewhere.
func TestCreate_PublishesDeclaredPorts(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp 53/udp")
	t.Setenv("FAKEDOCKER_PORTS", "8080/tcp -> 127.0.0.1:32768")
	var stdout strings.Builder

	if err := Create(context.Background(), d, publishSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	// The declaration is read before anything is removed or started:
	// finding out then that it cannot be read is the difference between
	// a create that fails and a VM that runs unreachable.
	if got[0][0] != "image" || got[0][1] != "inspect" {
		t.Errorf("first call = %v, want the image's declaration read first", got[0])
	}
	vmCall := lastCall(t, got)
	// Bound to loopback, with no host port of granule's choosing: docker
	// assigns the port, the way it does for `docker run -P`, and pinning
	// one is -docker-run-opt's job. The address is where this differs
	// from -P -- the image's author chose these ports, so the network
	// does not see them until somebody says it should.
	for _, port := range []string{"127.0.0.1::8080/tcp", "127.0.0.1::53/udp"} {
		if !containsArgPair(vmCall, "-p", port) {
			t.Errorf("VM call = %v, missing `-p %s`", vmCall, port)
		}
	}
	if !strings.Contains(stdout.String(), "8080/tcp") || !strings.Contains(stdout.String(), "EXPOSE") {
		t.Errorf("Create() said %q, want it to name what it published and why", stdout.String())
	}
	// The host side is the half the caller does not know, so it is
	// reported rather than left to be looked up.
	if !strings.Contains(stdout.String(), "127.0.0.1:32768 (this host only)") {
		t.Errorf("Create() said %q, want the host ports docker assigned and who can reach them", stdout.String())
	}
}

// TestCreate_PublishAddr: binding the declared ports anywhere but
// loopback is asked for in so many words, and an IPv6 address is
// spelt the way docker's -p can tell apart from the colons after it.
// An explicit -p in DockerRunOpts is the caller's own docker flag and
// is passed on untouched.
func TestCreate_PublishAddr(t *testing.T) {
	for _, tc := range []struct {
		addr, want, said string
	}{
		{addr: "0.0.0.0", want: "0.0.0.0::8080/tcp", said: "every interface"},
		{addr: "::1", want: "[::1]::8080/tcp", said: "this host only"},
		{addr: "10.0.0.5", want: "10.0.0.5::8080/tcp", said: "only via 10.0.0.5"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			d, calls := testDocker(t)
			t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp")
			spec := publishSpec()
			spec.PublishAddr = tc.addr
			spec.DockerRunOpts = []string{"-p", "9000:9000"}
			var stdout strings.Builder

			if err := Create(context.Background(), d, spec, &stdout); err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			vmCall := lastCall(t, calls())
			if !containsArgPair(vmCall, "-p", tc.want) {
				t.Errorf("VM call = %v, missing `-p %s`", vmCall, tc.want)
			}
			if !containsArgPair(vmCall, "-p", "9000:9000") {
				t.Errorf("VM call = %v, want the caller's own -p passed on as written", vmCall)
			}
			if !strings.Contains(stdout.String(), tc.said) {
				t.Errorf("Create() said %q, want it to say %q", stdout.String(), tc.said)
			}
		})
	}
}

func TestPublishedPort_Describe(t *testing.T) {
	for host, want := range map[string]string{
		"0.0.0.0:32768":   "every interface",
		"[::]:32768":      "every interface",
		"127.0.0.1:32768": "this host only",
		"[::1]:32768":     "this host only",
		"10.0.0.5:32768":  "only via 10.0.0.5",
	} {
		p := PublishedPort{Guest: "8080/tcp", Host: host}
		if got := p.Describe(); got != p.String()+" ("+want+")" {
			t.Errorf("Describe() of %s = %q, want %q", host, got, want)
		}
	}
}

// TestCreate_PublishesNothingUnasked is the other half of the same
// relationship, and the one the isolation argument rests on: the same
// image, declaring the same ports, opens nothing at all unless a
// deployment granted it. A guest is shielded by default and an EXPOSE
// somebody wrote as documentation does not change that.
func TestCreate_PublishesNothingUnasked(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp 53/udp")
	var stdout strings.Builder

	if err := Create(context.Background(), d, testSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for _, call := range calls() {
		if call[0] == "image" {
			t.Errorf("the image was inspected for a grant nobody asked for: %v", call)
		}
	}
	if vmCall := lastCall(t, calls()); containsArg(vmCall, "-p") {
		t.Errorf("VM call = %v, want no port published without -publish-declared", vmCall)
	}
}

// TestCreate_PublishDeclaredWithNothingDeclared covers the case a caller
// most easily misreads: the grant was given, the image declares nothing,
// and so nothing is published. The VM is fine -- it is the caller's
// expectation that is wrong, so it is said out loud rather than left as
// silence that looks like success.
func TestCreate_PublishDeclaredWithNothingDeclared(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "")
	var stdout strings.Builder

	if err := Create(context.Background(), d, publishSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if vmCall := lastCall(t, calls()); containsArg(vmCall, "-p") {
		t.Errorf("VM call = %v, want nothing published for an image that declares nothing", vmCall)
	}
	if !strings.Contains(stdout.String(), "declares no ports") {
		t.Errorf("Create() said %q, want it to say the image declared nothing", stdout.String())
	}
}

// TestCreate_PublishDeclaredIgnoresJunk: the declaration comes out of an
// image somebody else authored, so a "port" that is not one is dropped
// out loud rather than becoming an argument to docker run.
func TestCreate_PublishDeclaredIgnoresJunk(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp --privileged")
	var stdout strings.Builder

	if err := Create(context.Background(), d, publishSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := lastCall(t, calls())
	if !containsArgPair(vmCall, "-p", "127.0.0.1::8080/tcp") {
		t.Errorf("VM call = %v, want the usable declaration published", vmCall)
	}
	if containsArg(vmCall, "--privileged") || containsArg(vmCall, "127.0.0.1::--privileged") {
		t.Errorf("VM call = %v, want the unusable one dropped", vmCall)
	}
	if !strings.Contains(stdout.String(), "ignoring") {
		t.Errorf("Create() said %q, want it to name what it dropped", stdout.String())
	}
}

// TestCreate_PublishDeclaredNeedsAReadableImage: a grant that cannot be
// evaluated fails the create rather than publishing nothing. Degrading
// to silence would hand the caller a VM that looks healthy and answers
// on no port at all -- discovered in production, not at the terminal --
// and it fails before anything is torn down or started.
func TestCreate_PublishDeclaredNeedsAReadableImage(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "ExposedPorts")
	var stdout strings.Builder

	err := Create(context.Background(), d, publishSpec(), &stdout)
	if err == nil {
		t.Fatal("Create() with an unreadable image = nil error, want one")
	}
	if !strings.Contains(err.Error(), "EXPOSE") || !strings.Contains(err.Error(), "-docker-run-opt") {
		t.Errorf("error = %v, want it to say what could not be read and what to do instead", err)
	}
	for _, call := range calls() {
		if call[0] == "run" || call[0] == "rm" {
			t.Errorf("Create() touched containers before the grant was resolved: %v", call)
		}
	}
}

// TestPublished_ReportsWhatDockerHolds is the half of the grant that
// only exists on the container: the host ports are docker's to choose,
// so the VM itself is the only record of them, and this is what lets
// "vm status" answer an hour after the create that printed them.
func TestPublished_ReportsWhatDockerHolds(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_PORTS", "8080/tcp -> 0.0.0.0:32768,8080/tcp -> [::]:32768,53/udp -> 0.0.0.0:32769")

	ports, err := Published(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Published() error = %v", err)
	}

	// Sorted by the guest port as a number, both bindings of one port
	// kept: they are different facts about who can reach the guest.
	want := []PublishedPort{
		{Guest: "53/udp", Host: "0.0.0.0:32769"},
		{Guest: "8080/tcp", Host: "0.0.0.0:32768"},
		{Guest: "8080/tcp", Host: "[::]:32768"},
	}
	if len(ports) != len(want) {
		t.Fatalf("Published() = %v, want %v", ports, want)
	}
	for i, p := range ports {
		if p != want[i] {
			t.Errorf("port %d = %v, want %v", i, p, want[i])
		}
	}
	if got := ports[2].HostPort(); got != "32768" {
		t.Errorf("HostPort() of an IPv6 binding = %q, want the port after the last colon", got)
	}
	// It asks the container granule named, so no caller has to know how
	// that name is built.
	if last := calls()[len(calls())-1]; last[0] != "port" || last[len(last)-1] != "granule-vm-web" {
		t.Errorf("last call = %v, want a \"docker port\" of the VM's own container", last)
	}
}

// A VM whose container was never created, or has been removed, publishes
// nothing -- which is a fact about the VM rather than a failure to find
// out, so it is reported the way a missing container's state is.
func TestPublished_MissingContainerPublishesNothing(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web")

	ports, err := Published(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Published() of a missing container = %v, want no error", err)
	}
	if len(ports) != 0 {
		t.Errorf("Published() = %v, want nothing", ports)
	}
}

// A docker that cannot answer at all is not "this VM publishes nothing":
// the two differ by exactly the ports a caller was about to reach for.
func TestPublished_UnusableDockerIsAnError(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "port")

	if _, err := Published(context.Background(), d, "web"); err == nil {
		t.Fatal("Published() with an unusable docker = nil error, want one")
	}
}
