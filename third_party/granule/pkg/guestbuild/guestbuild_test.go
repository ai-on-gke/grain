package guestbuild

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/netshim"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-build")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "../guestboot/testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

// testOptions returns Options pointed at fakedocker, plus a function
// reading back the calls it recorded as one []string per invocation.
func testOptions(t *testing.T) (Options, func() [][]string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)

	opts := Options{
		From:            "ghcr.io/bwsalmon/granule:debian12",
		Setup:           "#!/bin/sh\necho 'hello'\n",
		Tag:             "my-guest:dev",
		ShutdownTimeout: 90 * time.Second,
		Stdout:          io.Discard,
		Stderr:          io.Discard,
	}
	return opts, func() [][]string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("reading fakedocker log: %v", err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				call := strings.Split(line, "\x1f")
				for i := range call {
					call[i] = strings.ReplaceAll(call[i], "\x1e", "\n")
				}
				calls = append(calls, call)
			}
		}
		return calls
	}
}

// find returns the first recorded call whose args contain every one of
// want, in order but not necessarily adjacent.
func find(calls [][]string, want ...string) []string {
	if i := indexOf(calls, want...); i >= 0 {
		return calls[i]
	}
	return nil
}

// indexOf is find's position rather than its call, for assertions about
// the order two calls were made in.
func indexOf(calls [][]string, want ...string) int {
	for i, call := range calls {
		joined := strings.Join(call, " ")
		rest := joined
		ok := true
		for _, w := range want {
			i := strings.Index(rest, w)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(w):]
		}
		if ok {
			return i
		}
	}
	return -1
}

// count returns how many recorded calls begin with the given args.
func count(calls [][]string, prefix ...string) int {
	n := 0
	for _, call := range calls {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if call[i] != p {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

func TestBuildRunsTheWholeSequence(t *testing.T) {
	opts, calls := testOptions(t)
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls()

	// The one VM container sets up its own network before booting.
	if find(got, "run", "-d", "--device", "/dev/kvm", "CHV_NET_MODE=inline", "NETSHIM_VM=", opts.From, "run") == nil {
		t.Errorf("no VM container with inline networking; calls were %v", got)
	}
	runs := 0
	for _, call := range got {
		if call[0] == "run" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("guest build ran %d containers, want exactly one: %v", runs, got)
	}
	// No disk mount and no CHV_DISK_IMAGE: the guest being customized is
	// the one baked into the image, which is the whole premise.
	for _, call := range got {
		for _, a := range call {
			if strings.HasPrefix(a, "CHV_DISK_IMAGE=") {
				t.Errorf("CHV_DISK_IMAGE was set; the image's own guest should boot: %v", call)
			}
		}
	}
	// The setup script reaches the guest base64-encoded, so quoting and
	// newlines in it never have to survive two shells.
	encoded := base64.StdEncoding.EncodeToString([]byte(opts.Setup))
	if find(got, "exec", "granule", "exec", "--", encoded) == nil {
		t.Errorf("the setup script was not staged base64-encoded; calls were %v", got)
	}
	if find(got, "exec", "granule", "exec", "--", setupPath) == nil {
		t.Errorf("the staged setup script was never run; calls were %v", got)
	}
	if find(got, "exec", "granule", "exec", "--", "rm -f /etc/ssh/ssh_host_*") == nil {
		t.Errorf("the scrub never ran; calls were %v", got)
	}
	// Stopping with the caller's own timeout, not docker's default: what
	// is being committed is the guest's filesystem, so the wait for a
	// clean power-off is the point.
	if find(got, "stop", "-t", "90") == nil {
		t.Errorf("no `docker stop` with the configured timeout; calls were %v", got)
	}
	if find(got, "commit", "org.opencontainers.image.base.name="+opts.From, opts.Tag) == nil {
		t.Errorf("no `docker commit` recording the base image; calls were %v", got)
	}
}

// The regression this package exists to keep fixed: the setup script
// used to be staged in /tmp, and a guest clears /tmp a second or two
// into its boot -- after granule-agent starts answering, which is when
// the build stages. That deleted the script between writing it and
// running it and failed two builds in three on one host, with "exit
// status 127: /bin/bash: line 1: ...: No such file or directory".
//
// fakedocker models the guest that does it (see its own doc comment):
// anything written under /tmp before the guest reports its boot finished
// is gone. So this is the happy path only for a build that does not put
// the script somewhere the boot will empty.
func TestBuildStagesTheSetupScriptOutOfTheBootsWay(t *testing.T) {
	if strings.HasPrefix(setupPath, "/tmp/") {
		t.Fatalf("setupPath = %s, which a guest's boot empties under the script", setupPath)
	}
	opts, calls := testOptions(t)
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v -- the staged setup script did not survive the guest's boot", err)
	}
	if c := find(calls(), "commit"); c == nil {
		t.Errorf("nothing was committed; calls were %v", calls())
	}
}

// A staged tree goes in before the setup script runs, as a tar on the
// stream `granule cp` reads rather than as an argument -- which is the
// whole reason it exists, since the script itself travels on a command
// line and the agents a rebase hands over are megabytes of static
// binary. Nothing here interprets the tree: where its files belong is
// the setup script's business.
func TestBuildStagesATreeBeforeProvisioning(t *testing.T) {
	opts, calls := testOptions(t)
	saved := filepath.Join(t.TempDir(), "staged.tar")
	t.Setenv("FAKEDOCKER_SAVE_STDIN", saved)
	opts.Stage = []StagedTree{{
		Description: "the parts",
		Tar:         strings.NewReader("a tar, as far as anything here is concerned"),
		Dest:        "/root/.parts",
	}}

	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls()
	copied := indexOf(got, "granule", "cp", "-tar", "-", "/root/.parts")
	if copied < 0 {
		t.Fatalf("the staged tree was never copied in; calls were %v", got)
	}
	staged := indexOf(got, "granule", "exec", "--", "base64 -d")
	if staged < 0 {
		t.Fatalf("the setup script was never staged; calls were %v", got)
	}
	if copied > staged {
		t.Errorf("the tree arrived after the script that uses it (%d > %d)", copied, staged)
	}
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("reading what reached the guest: %v", err)
	}
	if string(data) != "a tar, as far as anything here is concerned" {
		t.Errorf("what reached the guest was not the archive handed over: %q", data)
	}
}

// The other half of the same fix, and the general one: the build waits
// for the guest's boot to finish rather than for the first exec that
// answers, so a setup script is not racing whatever systemd has not
// started yet -- of which the /tmp clean above is one instance.
func TestBuildWaitsOutTheGuestsBootBeforeProvisioning(t *testing.T) {
	opts, calls := testOptions(t)
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls()
	waited := indexOf(got, "granule", "exec", "--", "systemctl", "is-system-running", "--wait")
	if waited < 0 {
		t.Fatalf("the build never waited for the guest's boot to finish; calls were %v", got)
	}
	staged := indexOf(got, "granule", "exec", "--", "base64 -d")
	if staged < 0 {
		t.Fatalf("the setup script was never staged; calls were %v", got)
	}
	if waited > staged {
		t.Errorf("the boot wait came after staging (%d > %d), which is no wait at all", waited, staged)
	}
}

// A guest with no systemd to ask -- granule's own alpine variant runs
// OpenRC -- still gets provisioned. The wait is a settling step, not a
// requirement the guest has to meet.
func TestBuildProvisionsAGuestWithNoServiceManagerToAsk(t *testing.T) {
	opts, calls := testOptions(t)
	t.Setenv("FAKEDOCKER_NO_SYSTEMD", "1")

	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c := find(calls(), "commit"); c == nil {
		t.Errorf("a guest that cannot report its boot state was never committed; calls were %v", calls())
	}
}

// Nor does a guest whose boot never settles -- one job stuck starting
// forever -- turn into a failed build: the wait gives up at
// -ready-timeout, says so, and provisions anyway. The staged script does
// not depend on the wait having worked, which is why this can be a
// warning rather than an error.
func TestBuildProvisionsAGuestWhoseBootNeverFinishes(t *testing.T) {
	opts, calls := testOptions(t)
	var out strings.Builder
	opts.Stdout = &out
	opts.ReadyTimeout = 2 * time.Second
	t.Setenv("FAKEDOCKER_HANG_CONTAINS", "is-system-running")

	start := time.Now()
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if elapsed := time.Since(start); elapsed < opts.ReadyTimeout {
		t.Errorf("Build took %s, less than the %s it should have waited for the boot", elapsed, opts.ReadyTimeout)
	}
	if c := find(calls(), "commit"); c == nil {
		t.Errorf("a guest whose boot never settled was never committed; calls were %v", calls())
	}
	if !strings.Contains(out.String(), "not finished booting") {
		t.Errorf("output = %q, want it to say the guest never reported its boot finished", out.String())
	}
}

func TestBuildDoesNotCommitAKilledGuest(t *testing.T) {
	// 137 is a container docker had to SIGKILL, i.e. a guest that never
	// finished powering off -- so the filesystem in its disk image may
	// be mid-write, and committing it would bake that into every VM
	// cloned from the result.
	opts, calls := testOptions(t)
	t.Setenv("FAKEDOCKER_EXIT", "137")

	err := Build(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a guest that had to be killed, got nil")
	}
	if !strings.Contains(err.Error(), "shutdown-timeout") {
		t.Errorf("error = %v, want it to point at -shutdown-timeout", err)
	}
	if c := find(calls(), "commit"); c != nil {
		t.Errorf("committed anyway: %v", c)
	}
}

func TestBuildRefusesAnUncleanExit(t *testing.T) {
	// Any non-zero exit means the supervisor did not bring the VM down
	// the way it was asked to, and the console will show a guest that
	// looks perfectly healthy -- the number is the only evidence, so it
	// has to reach the caller rather than being swallowed on the way to
	// a commit.
	opts, calls := testOptions(t)
	t.Setenv("FAKEDOCKER_EXIT", "2")

	err := Build(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a container that exited uncleanly, got nil")
	}
	if !strings.Contains(err.Error(), "exited 2") {
		t.Errorf("error = %v, want it to name the exit code", err)
	}
	if c := find(calls(), "commit"); c != nil {
		t.Errorf("committed despite an unclean exit: %v", c)
	}
}

func TestBuildCleansUpAfterAFailedSetupScript(t *testing.T) {
	opts, calls := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", setupPath)

	err := Build(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a failing setup script, got nil")
	}
	got := calls()
	if c := find(got, "commit"); c != nil {
		t.Errorf("committed a guest whose setup script failed: %v", c)
	}
	// Create force-removes stale containers of its own before starting,
	// so two `rm -f` calls are its; the cleanup adds the VM and the
	// namespace holder, which would otherwise leak a container and a tap
	// device per failed build.
	if n := count(got, "rm", "-f"); n != 4 {
		t.Errorf("`rm -f` calls = %d, want 4 (2 from Create, 2 cleaning up); calls were %v", n, got)
	}
}

func TestBuildKeepsTheContainerOnFailureWhenAsked(t *testing.T) {
	opts, calls := testOptions(t)
	opts.KeepOnFailure = true
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", setupPath)

	err := Build(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "docker logs") {
		t.Errorf("error = %v, want it to name the container left behind", err)
	}
	if n := count(calls(), "rm", "-f"); n != 2 {
		t.Errorf("`rm -f` calls = %d, want only Create's own 2: nothing should be removed under -keep-on-failure", n)
	}
}

func TestBuildFailsFastWhenTheContainerExits(t *testing.T) {
	// A container that is gone will not become reachable however long
	// the caller waits, so this must not sit out the ready timeout.
	opts, calls := testOptions(t)
	opts.ReadyTimeout = time.Hour
	t.Setenv("FAKEDOCKER_RUNNING", "false")
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "granule exec")

	done := make(chan error, 1)
	go func() { done <- Build(context.Background(), opts) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "exited before") {
			t.Errorf("error = %v, want it to say the container exited", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Build waited out the ready timeout instead of noticing the container had exited")
	}
	if c := find(calls(), "commit"); c != nil {
		t.Errorf("committed despite never reaching the guest: %v", c)
	}
}

func TestBuildWithSnapshot(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Snapshot = true

	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls()

	// VM container boots with persistent disk mode and no net mode
	if find(got, "run", "-d", "CHV_DISK_MODE=persistent", "CHV_NET_MODE=none") == nil {
		t.Errorf("VM container did not boot with CHV_DISK_MODE=persistent and CHV_NET_MODE=none; calls were %v", got)
	}

	// Setup script and scrub are executed
	if find(got, "exec", "granule", "exec", "--", setupPath) == nil {
		t.Errorf("setup script was never run; calls were %v", got)
	}
	if find(got, "exec", "granule", "exec", "--", "rm -f /etc/ssh/ssh_host_*") == nil {
		t.Errorf("scrub was never run; calls were %v", got)
	}

	// The snapshot is taken with -resume, and the guest then powers off
	// the way every other build's does. It has to be -resume: a
	// snapshot leaves the VM paused, and a paused guest never sees the
	// power button, so a build that took one without resuming spent the
	// whole shutdown timeout and had docker kill the container out from
	// under it.
	snapIdx := indexOf(got, "exec", "granule", "snapshot", "-path", "/var/lib/granule/guest/snapshot", "-resume")
	if snapIdx < 0 {
		t.Fatalf("granule snapshot was never run; calls were %v", got)
	}
	stopIdx := indexOf(got, "stop", "-t", "90")
	if stopIdx < 0 {
		t.Fatalf("docker stop was never run; calls were %v", got)
	}
	if snapIdx > stopIdx {
		t.Errorf("snapshot was taken after stop (%d > %d)", snapIdx, stopIdx)
	}
	if commitIdx := indexOf(got, "commit"); commitIdx < stopIdx {
		t.Errorf("committed (%d) before the guest had powered off (%d)", commitIdx, stopIdx)
	}

	// Commit includes snapshotted label
	if find(got, "commit", "LABEL dev.granule.guest.snapshotted=true", opts.Tag) == nil {
		t.Errorf("no `docker commit` recording dev.granule.guest.snapshotted=true; calls were %v", got)
	}

	// Nobody sized this one, so it gets the small default: a snapshot
	// writes every byte of the guest's memory into the image.
	if find(got, "run", "-d", "CHV_MEMORY_MB=384") == nil {
		t.Errorf("an unsized snapshot build did not boot at the small default; calls were %v", got)
	}
}

// A caller that knows what the machine's memory is meant to be says so,
// and that wins over the small default -- a Granulefile's `VM SET
// memory-mb=` beside a `VM SNAPSHOT` is exactly that, and a restore
// replays the snapshot's own size, so a build that ignored it would
// produce a machine that could never honour the setting.
func TestASizedSnapshotBuildBootsAtTheSizeItWasGiven(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Snapshot = true
	opts.MemoryMB = 2048
	opts.MemoryMaxMB = 4096

	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls()
	if find(got, "run", "-d", "CHV_MEMORY_MB=2048") == nil {
		t.Errorf("the snapshot build did not boot at the size it was given; calls were %v", got)
	}
	if find(got, "run", "-d", "CHV_MEMORY_MAX_MB=4096") == nil {
		t.Errorf("the snapshot build did not take the hotplug ceiling it was given; calls were %v", got)
	}
	if find(got, "run", "-d", "CHV_MEMORY_MB=384") != nil {
		t.Errorf("the small default overrode the caller's own size; calls were %v", got)
	}
}

// The build VM's resolver is the caller's to choose, and it reaches the
// VM container as NETSHIM_DNS -- the same field `vm create -dns` sets,
// since the build VM is an ordinary guest and the setup script running
// inside it is the one part of a booting build that resolves names at
// all.
func TestBuildGivesTheGuestTheNameserversItWasAskedFor(t *testing.T) {
	opts, calls := testOptions(t)
	dns := "10.0.0.53,10.0.0.54"
	opts.DNS = &dns

	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if find(calls(), "run", "-d", "NETSHIM_DNS="+dns, opts.From, "run") == nil {
		t.Errorf("the VM container was not told which resolvers to give the guest; calls were %v", calls())
	}
}

// Unset means granule's own default rather than no resolver at all:
// every build made before -dns existed got netshim.DefaultDNS out of
// vmspec.Defaults, and a caller who says nothing still does. An
// explicitly empty string is the other request -- leave the guest
// image's own /etc/resolv.conf alone -- which is why Options.DNS is a
// pointer.
func TestBuildDefaultsTheGuestsResolverAndCanBeToldToLeaveItAlone(t *testing.T) {
	opts, calls := testOptions(t)
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if find(calls(), "run", "-d", "NETSHIM_DNS="+netshim.DefaultDNS, opts.From, "run") == nil {
		t.Errorf("a build that named no resolver did not get granule's default; calls were %v", calls())
	}

	opts, calls = testOptions(t)
	none := ""
	opts.DNS = &none
	if err := Build(context.Background(), opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	vm := find(calls(), "run", "-d", "NETSHIM_VM=", opts.From, "run")
	if vm == nil {
		t.Fatalf("no VM container was started; calls were %v", calls())
	}
	for _, a := range vm {
		if strings.HasPrefix(a, "NETSHIM_DNS=") && a != "NETSHIM_DNS=" {
			t.Errorf("%s, but the build asked for the guest's own resolver to be left alone", a)
		}
	}
}
