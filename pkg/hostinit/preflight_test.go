package hostinit

import (
	"context"
	"strings"
	"testing"
)

// TestPreflightStopsBeforeAnyMutatingCall is the property this package's
// preflight exists for: a host missing something grain init will never
// install for it (make, in this case) gets told so before Init so much
// as checks whether git is installed, let alone installs docker, builds
// the granule CLI or builds an image. Asserted against sys.calls rather
// than against individual Result.Steps, because the point is that
// nothing after preflight ran at all.
func TestPreflightStopsBeforeAnyMutatingCall(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	// make missing; npm and /dev/kvm present, so the only thing
	// preflight has to object to is make -- everything after it in
	// initFor's own order must still never run.
	sys.on("10.8.2", nil, "npm", "--version")
	sys.on("", nil, "test", "-e", "/dev/kvm")

	opts := baseOptions(t)
	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}

	s, ok := stepStatus(result, "make")
	if !ok || s.Status != StepManual {
		t.Fatalf("make: got %+v, want StepManual", s)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("Steps = %+v, want exactly the three preflight checks and nothing initFor's own steps would have added after them", result.Steps)
	}

	for _, call := range sys.calls {
		if call == "make --version" || call == "npm --version" || call == "test -e /dev/kvm" {
			continue
		}
		t.Errorf("Init ran %q after a failed preflight -- it must do nothing until preflight passes", call)
	}
	if len(sys.httpCalls) != 0 || len(sys.httpRequestCalls) != 0 {
		t.Errorf("Init made an HTTP call after a failed preflight: %v %v", sys.httpCalls, sys.httpRequestCalls)
	}
}

// TestPreflightReportsEveryMissingPieceTogether is the other half of the
// same property: an operator fixing what one run told them about should
// not find a second, previously silent problem waiting on the next run.
func TestPreflightReportsEveryMissingPieceTogether(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	// Nothing configured for make, npm or /dev/kvm: all three missing.

	opts := baseOptions(t)
	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	for _, name := range []string{"make", "node & npm (frontend build)", "/dev/kvm"} {
		s, ok := stepStatus(result, name)
		if !ok || s.Status != StepManual {
			t.Errorf("%s: got %+v, want StepManual", name, s)
		}
	}
	if len(sys.calls) != 3 {
		t.Errorf("Init made calls beyond the three preflight checks: %v", sys.calls)
	}
}

// TestPreflightKVMRefusalPointsAtLinuxContainer: on a host with make
// and npm but no /dev/kvm, the refusal used to offer only modprobe --
// nothing told an operator on a host that cannot have KVM that
// -platform=linux-container exists for exactly that host.
func TestPreflightKVMRefusalPointsAtLinuxContainer(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("GNU Make 4.3", nil, "make", "--version")
	sys.on("10.8.2", nil, "npm", "--version")

	result, err := Init(context.Background(), sys, baseOptions(t))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	s, ok := stepStatus(result, "/dev/kvm")
	if !ok || s.Status != StepManual {
		t.Fatalf("/dev/kvm: got %+v, want StepManual", s)
	}
	if !strings.Contains(s.Detail, "-platform=linux-container") || !strings.Contains(s.Command, "-platform=linux-container") {
		t.Errorf("/dev/kvm refusal does not suggest -platform=linux-container: %+v", s)
	}
}

// TestPreflightPassesWhenEverythingIsPresent checks the other branch:
// with make, npm and /dev/kvm already there, preflight reports them
// StepSkipped and lets initFor's own steps run.
func TestPreflightPassesWhenEverythingIsPresent(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	stubPreflight(sys)

	var r Result
	ok := preflight(context.Background(), sys, &r, Options{Platform: PlatformLinuxKVM}, "linux")
	if !ok {
		t.Fatalf("preflight = false, want true with every check satisfied: %+v", r.Steps)
	}
	for _, s := range r.Steps {
		if s.Status != StepSkipped {
			t.Errorf("%s: status = %s, want %s", s.Name, s.Status, StepSkipped)
		}
	}
	// A host that passes the goos gate gets no Step for it at all: the
	// three preflight checks grain init always had are the only ones
	// reported, so a -platform flag that defaults to PlatformLinuxKVM
	// changes nothing about today's output.
	if len(r.Steps) != 3 {
		t.Fatalf("Steps = %+v, want exactly the three checks grain init always ran, with no extra "+
			"\"host platform\" line now that goos is one entry in this table rather than a separate "+
			"mechanism", r.Steps)
	}
}

// PlatformK8sGranule and PlatformK8sContainer no longer belong in this
// file's own tests -- both have real preflights now, covered by
// TestK8sGranule* (pkg/hostinit/k8s_granule_test.go) and TestK8sContainer*
// (pkg/hostinit/k8s_test.go) respectively.

// TestValidateRejectsUnknownPlatform is the other half of item 5: a
// -platform value that is not even one of the four named platforms is a
// bad flag (rejected before preflight ever runs), not something for
// preflight to refuse at runtime.
func TestValidateRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = Platform("nonsense")
	if err := opts.withDefaults().validate(); err == nil {
		t.Fatalf("expected an error for an unrecognized platform")
	}
}

// TestValidateDefaultsPlatformToLinuxKVM confirms Options{} -- what
// every caller before this field existed already passes -- still means
// PlatformLinuxKVM once withDefaults runs.
func TestValidateDefaultsPlatformToLinuxKVM(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	if got := opts.withDefaults().Platform; got != PlatformLinuxKVM {
		t.Fatalf("default platform = %q, want %q", got, PlatformLinuxKVM)
	}
}

// TestPreflightLinuxContainerDoesNotDemandKVM is the one preflight
// difference this platform exists for: /dev/kvm's absence is the
// reason to pick it, so a preflight that demanded it anyway would
// refuse every host it was meant for. make and npm present, nothing
// configured for "test -e /dev/kvm" at all -- if preflight asked for it,
// the unconfigured fake would fail the check (or, if it absent-mindedly
// treated a missing stub as success, the test below would still catch
// it by asserting the command was never run).
func TestPreflightLinuxContainerDoesNotDemandKVM(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("GNU Make 4.3", nil, "make", "--version")
	sys.on("10.8.2", nil, "npm", "--version")

	var r Result
	ok := preflight(context.Background(), sys, &r, Options{Platform: PlatformLinuxContainer}, "linux")
	if !ok {
		t.Fatalf("preflight = false, want true: %+v", r.Steps)
	}
	if _, found := stepStatus(r, "/dev/kvm"); found {
		t.Errorf("PlatformLinuxContainer's preflight reported a /dev/kvm step at all: %+v", r.Steps)
	}
	if sys.calledAny("test -e /dev/kvm") {
		t.Errorf("PlatformLinuxContainer's preflight ran \"test -e /dev/kvm\" -- its absence is the reason to pick this platform")
	}
	for _, s := range r.Steps {
		if s.Status != StepSkipped {
			t.Errorf("%s: status = %s, want %s", s.Name, s.Status, StepSkipped)
		}
	}
	if len(r.Steps) != 2 {
		t.Fatalf("Steps = %+v, want exactly the two checks this platform shares with PlatformLinuxKVM (make, node & npm) and nothing else", r.Steps)
	}
}

// TestPreflightLinuxContainerStillDemandsMakeAndNPM confirms the two
// checks this platform does share with PlatformLinuxKVM still fire:
// `make build` needs both regardless of which sandbox backend the
// daemon ends up running.
func TestPreflightLinuxContainerStillDemandsMakeAndNPM(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	// Nothing configured for make or npm: both missing.

	var r Result
	ok := preflight(context.Background(), sys, &r, Options{Platform: PlatformLinuxContainer}, "linux")
	if ok {
		t.Fatalf("preflight = true, want false: make and npm are both missing")
	}
	for _, name := range []string{"make", "node & npm (frontend build)"} {
		s, found := stepStatus(r, name)
		if !found || s.Status != StepManual {
			t.Errorf("%s: got %+v, want StepManual", name, s)
		}
	}
	if len(sys.calls) != 2 {
		t.Errorf("preflight made calls beyond the two checks this platform has: %v", sys.calls)
	}
}

// TestPreflightLinuxContainerRefusesNonLinux confirms the goos gate
// still applies: PlatformLinuxContainer is as Linux-only as
// PlatformLinuxKVM, just without granule or /dev/kvm as part of why.
func TestPreflightLinuxContainerRefusesNonLinux(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true

	var r Result
	ok := preflight(context.Background(), sys, &r, Options{Platform: PlatformLinuxContainer}, "darwin")
	if ok {
		t.Fatalf("preflight = true on darwin, want false")
	}
	if len(r.Steps) != 1 || r.Steps[0].Status != StepRefused {
		t.Fatalf("Steps = %+v, want exactly one StepRefused (the goos gate) and nothing else checked", r.Steps)
	}
	if len(sys.calls) != 0 {
		t.Errorf("preflight ran a command after the goos gate refused: %v", sys.calls)
	}
}
