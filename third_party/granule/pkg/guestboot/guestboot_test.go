package guestboot

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-guestboot")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "./testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

func testOptions(t *testing.T) (Options, func() [][]string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)

	opts := Options{
		Image:           "ghcr.io/bwsalmon/granule:debian12",
		NamePrefix:      "test-",
		ReadyTimeout:    5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		Stdout:          io.Discard,
		Stderr:          io.Discard,
	}

	recorded := func() [][]string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			t.Fatalf("reading fakedocker calls: %v", err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			calls = append(calls, strings.Split(line, "\t"))
		}
		return calls
	}
	return opts, recorded
}

func TestBootRequiresImage(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Image = ""
	_, err := Boot(context.Background(), opts)
	if err == nil {
		t.Fatal("Boot with empty Image expected error, got nil")
	}
}

func TestBootSuccess(t *testing.T) {
	opts, recorded := testOptions(t)
	var stdout bytes.Buffer
	opts.Stdout = &stdout

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if !strings.HasPrefix(session.Container(), "granule-vm-test-") {
		t.Errorf("Container name = %q, want prefix 'granule-vm-test-'", session.Container())
	}

	// Verify Exec
	var outBuf, errBuf bytes.Buffer
	if err := session.Exec(context.Background(), &outBuf, &errBuf, "echo", "hello"); err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	// Verify Script
	if err := session.Script(context.Background(), "echo script-ran", "/tmp/myscript.sh", &outBuf, &errBuf); err != nil {
		t.Fatalf("Script failed: %v", err)
	}

	// Verify Unpack
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	hdr := &tar.Header{
		Name: "testfile.txt",
		Mode: 0o644,
		Size: int64(len("unpack-data")),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("unpack-data")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Unpack(context.Background(), &archive, "/opt", &errBuf); err != nil {
		t.Fatalf("Unpack failed: %v", err)
	}

	// Verify Place
	if err := session.Place(context.Background(), "/var/lib/granule/test.txt", []byte("placed-content")); err != nil {
		t.Fatalf("Place failed: %v", err)
	}

	// Verify Snapshot
	destDir := t.TempDir()
	if err := session.Snapshot(context.Background(), destDir); err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Verify Running
	running, err := session.Running(context.Background())
	if err != nil {
		t.Fatalf("Running failed: %v", err)
	}
	if !running {
		t.Errorf("Running = false, want true")
	}

	// Verify Console & WithConsole
	console := session.Console(context.Background(), 20)
	_ = console
	wrappedErr := session.WithConsole(context.Background(), errors.New("something went wrong"))
	if wrappedErr == nil || !strings.Contains(wrappedErr.Error(), "something went wrong") {
		t.Errorf("WithConsole unexpected error: %v", wrappedErr)
	}

	// Verify Commit
	if err := session.Commit(context.Background(), "my-tag:latest", "LABEL test=1"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify Stop
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	calls := recorded()
	if len(calls) == 0 {
		t.Error("fakedocker recorded no calls")
	}
}

func TestBootExitError(t *testing.T) {
	opts, _ := testOptions(t)
	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	// Run a script designed to fail in fakedocker
	failScript := "#!/bin/sh\n#fake-exit: 3\n#fake-out: fail-message\n"
	err = session.Script(context.Background(), failScript, "/tmp/fail.sh", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected script error, got nil")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.Code != 3 {
		t.Errorf("ExitError code = %d, want 3", exitErr.Code)
	}
}

func TestBootContainerExitedEarly(t *testing.T) {
	opts, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "exec")
	t.Setenv("FAKEDOCKER_RUNNING", "false")

	_, err := Boot(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error when container exited early, got nil")
	}
	if !strings.Contains(err.Error(), "the guest container exited before its guest became reachable") {
		t.Errorf("error message = %q, want mention of container exiting", err.Error())
	}
}

// vmRunCall is the `docker run` that started the VM container, as one
// string to look for arguments in. fakedocker records a call's argv
// separated by \x1f, so the fields are joined back with spaces first.
func vmRunCall(t *testing.T, calls [][]string) string {
	t.Helper()
	var run string
	for _, call := range calls {
		line := strings.ReplaceAll(strings.Join(call, " "), "\x1f", " ")
		if strings.HasPrefix(line, "run ") && strings.Contains(line, "--device /dev/kvm") {
			run = line
		}
	}
	if run == "" {
		t.Fatalf("no docker run of a VM container recorded, calls were %v", calls)
	}
	return run
}

// A machine's own sizing is what it comes up with. VM SET records it in
// the image's OCI config (docs/granulefile.md §5.5), and a throwaway
// boot that passed vmspec.Defaults' 2 vCPUs and 2 GiB along as an
// explicit CHV_CPUS/CHV_MEMORY_MB would override it the way `docker run
// -e` does -- so a machine built to come up with a gigabyte would be
// asked about while running with two.
func TestBootHonoursTheImagesOwnSizing(t *testing.T) {
	opts, recorded := testOptions(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CPUS=2,CHV_CPUS_MAX=4,CHV_MEMORY_MB=1024,CHV_MEMORY_MAX_MB=2048,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	joined := vmRunCall(t, recorded())
	for _, want := range []string{"CHV_CPUS=2", "CHV_CPUS_MAX=4", "CHV_MEMORY_MB=1024", "CHV_MEMORY_MAX_MB=2048"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the VM was not started with %s, which its image records: %s", want, joined)
		}
	}
}

// The same holds for the kernel command line a machine records, and it
// is the setting in that table whose effect a verify would otherwise
// never see: booted with granule's own line instead, a machine whose
// recorded one cannot boot at all still answers every check put to it.
func TestBootHonoursTheImagesOwnCmdline(t *testing.T) {
	opts, recorded := testOptions(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CMDLINE=quiet,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	// Exactly what the image asked for: the VM container adds granule's
	// own parameters to it (config.CmdlineWith), so adding them here as
	// well would compose the line twice.
	joined := vmRunCall(t, recorded())
	if !strings.Contains(joined, "CHV_CMDLINE=quiet") {
		t.Errorf("the VM was not started with the command line its image records: %s", joined)
	}
}

// And so does the rest of what §5.5 lets a machine record and a VMSpec
// can carry: its disk settings and its shutdown budget. Each one
// granule passes explicitly is one a machine records and is then not
// run with, which is how a check comes to pass on a machine nobody
// boots.
func TestBootHonoursTheImagesOwnDiskSettings(t *testing.T) {
	opts, recorded := testOptions(t)
	// No DiskMode of this caller's own, so the image's is what a boot
	// through here uses.
	opts.DiskMode = ""
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_DISK_MODE=overlay,CHV_DISK_SIZE_MB=2048,CHV_EXTENT_SIZE_MB=128,CHV_DISK_NUM_QUEUES=4,CHV_DISK_IOTHREADS=on,CHV_DISK_IO_ENGINE=io_uring,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	joined := vmRunCall(t, recorded())
	for _, want := range []string{
		"CHV_DISK_MODE=overlay", "CHV_DISK_SIZE_MB=2048", "CHV_EXTENT_SIZE_MB=128",
		"CHV_DISK_NUM_QUEUES=4", "CHV_DISK_IOTHREADS=true", "CHV_DISK_IO_ENGINE=io_uring",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the VM was not started with %s, which its image records: %s", want, joined)
		}
	}
}

// §5.5 spells the vCPU count two ways, and a machine that used the
// other one is still a machine that said how many it wants. The VM
// container reads CHV_CPUS first and CHV_VCPUS only in its absence, so
// this side has to resolve them the same way or it would pass granule's
// own default as the CHV_CPUS that then wins.
func TestBootHonoursTheImagesOwnVCPUsSpelling(t *testing.T) {
	opts, recorded := testOptions(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_VCPUS=4,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if joined := vmRunCall(t, recorded()); !strings.Contains(joined, "CHV_CPUS=4") {
		t.Errorf("the VM was not started with the vCPUs its image records as vcpus: %s", joined)
	}
}

// And an image recording both keeps the one the VM container would have
// used, whichever order they come back in.
func TestBootPrefersCPUsOverVCPUs(t *testing.T) {
	opts, recorded := testOptions(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_VCPUS=4,CHV_CPUS=3,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if joined := vmRunCall(t, recorded()); !strings.Contains(joined, "CHV_CPUS=3") {
		t.Errorf("the VM was not started with the cpus its image records: %s", joined)
	}
}

// Including the disk mode itself, for a caller that named none: a
// machine that writes through to its own disk is not one to judge in an
// overlay by default.
func TestBootHonoursTheImagesOwnDiskMode(t *testing.T) {
	opts, recorded := testOptions(t)
	opts.DiskMode = ""
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_DISK_MODE=persistent,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if joined := vmRunCall(t, recorded()); !strings.Contains(joined, "CHV_DISK_MODE=persistent") {
		t.Errorf("the VM was not started in the disk mode its image records: %s", joined)
	}
}

// A machine recording a combination nothing can boot -- an overlay size
// beside a disk mode that has no overlay to size, which the VM
// container refuses too (config.FromEnv) -- is refused here, by name,
// rather than started and found out afterwards.
func TestBootRefusesAnImagesImpossibleCombination(t *testing.T) {
	opts, _ := testOptions(t)
	opts.DiskMode = ""
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_DISK_MODE=persistent,CHV_DISK_SIZE_MB=2048,PATH=/usr/bin")

	_, err := Boot(context.Background(), opts)
	if err == nil {
		t.Fatal("Boot of a machine whose recorded settings cannot boot expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "disk-size-mb") || !strings.Contains(err.Error(), opts.Image) {
		t.Errorf("the refusal names neither the setting nor the image that records it: %v", err)
	}
}

// The caller still wins on disk mode, which is the one setting here a
// caller chooses on purpose: a verify boots into an overlay so that
// nothing it does can reach the image it is judging, whatever that
// image records. The disagreement is reported rather than passed over.
func TestBootCallersDiskModeBeatsTheImages(t *testing.T) {
	opts, recorded := testOptions(t)
	opts.DiskMode = "overlay"
	var stdout bytes.Buffer
	opts.Stdout = &stdout
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_DISK_MODE=persistent,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	joined := vmRunCall(t, recorded())
	if !strings.Contains(joined, "CHV_DISK_MODE=overlay") {
		t.Errorf("the VM was not started in the disk mode its caller asked for: %s", joined)
	}
	if !strings.Contains(stdout.String(), "records disk-mode=persistent") {
		t.Errorf("the boot did not report that it overruled the image's own disk mode: %q", stdout.String())
	}
}

// A shutdown budget is two numbers that have to agree: the one the VM
// container waits for the guest to power off in, and the one `docker
// stop -t` waits for the container in. A machine that records 45s and
// is stopped after 20 is killed mid-shutdown by the half that was not
// told.
func TestBootHonoursTheImagesOwnShutdownTimeout(t *testing.T) {
	opts, recorded := testOptions(t)
	opts.ShutdownTimeout = 0
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_SHUTDOWN_TIMEOUT=45s,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if joined := vmRunCall(t, recorded()); !strings.Contains(joined, "CHV_SHUTDOWN_TIMEOUT=45s") {
		t.Errorf("the VM was not given the shutdown budget its image records: %s", joined)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	var stop string
	for _, call := range recorded() {
		line := strings.ReplaceAll(strings.Join(call, " "), "\x1f", " ")
		if strings.HasPrefix(line, "stop ") {
			stop = line
		}
	}
	if !strings.Contains(stop, "-t 45") {
		t.Errorf("`docker stop` did not wait the image's own 45s: %q", stop)
	}
}

// And a caller that named a budget of its own keeps it, the way it
// keeps its disk mode.
func TestBootCallersShutdownTimeoutBeatsTheImages(t *testing.T) {
	opts, recorded := testOptions(t)
	opts.ShutdownTimeout = 5 * time.Second
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_SHUTDOWN_TIMEOUT=45s,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	if joined := vmRunCall(t, recorded()); !strings.Contains(joined, "CHV_SHUTDOWN_TIMEOUT=5s") {
		t.Errorf("the VM was not given the shutdown budget its caller asked for: %s", joined)
	}
}

// An image that records a setting granule cannot make sense of is
// booted with granule's own rather than with a guess, and rather than
// not at all: an unparseable shutdown-timeout passed through would fail
// VMSpec.Validate and the machine would never start.
func TestBootIgnoresSettingsItCannotRead(t *testing.T) {
	opts, recorded := testOptions(t)
	opts.DiskMode = ""
	opts.ShutdownTimeout = 0
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_SHUTDOWN_TIMEOUT=soon,CHV_DISK_MODE=sideways,CHV_DISK_SIZE_MB=lots,PATH=/usr/bin")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	joined := vmRunCall(t, recorded())
	for _, want := range []string{"CHV_SHUTDOWN_TIMEOUT=1m0s", "CHV_DISK_MODE=overlay"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the VM was not started with granule's own default %s: %s", want, joined)
		}
	}
	if strings.Contains(joined, "CHV_DISK_SIZE_MB") {
		t.Errorf("the VM was given a disk size out of a value that is not one: %s", joined)
	}
}

// And an image that records no sizing of its own is still booted with
// granule's defaults, which is what every machine built before VM SET
// existed gets.
func TestBootDefaultsWhenTheImageRecordsNoSizing(t *testing.T) {
	opts, recorded := testOptions(t)

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	defer session.Remove(context.Background())

	joined := vmRunCall(t, recorded())
	for _, want := range []string{"CHV_CPUS=2", "CHV_MEMORY_MB=2048"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the VM was not started with granule's own default %s: %s", want, joined)
		}
	}
}

func TestBootNoSystemdOpenRC(t *testing.T) {
	opts, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_NO_SYSTEMD", "1")

	session, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot with no systemd failed: %v", err)
	}
	defer session.Remove(context.Background())
}

func TestExitErrorString(t *testing.T) {
	e1 := &ExitError{Code: 1, Stderr: "error msg"}
	if e1.Error() != "exit status 1: error msg" {
		t.Errorf("e1.Error() = %q", e1.Error())
	}

	e2 := &ExitError{Code: 2}
	if e2.Error() != "exit status 2" {
		t.Errorf("e2.Error() = %q", e2.Error())
	}
}

func TestCLIHelpers(t *testing.T) {
	cli := &CLI{Binary: fakedockerPath}
	out, err := cli.Output(context.Background(), "ps")
	if err != nil {
		t.Fatalf("cli.Output failed: %v", err)
	}
	_ = out

	// Test placeInContainer error handling
	err = cli.placeInContainer(context.Background(), "no-such-container", "/path/file", []byte("data"))
	_ = err
}

// ShellQuote has to hand a real shell back exactly the word it was given,
// whatever is in it: a path written into guestScript's `sh -c` line that
// carried a `;` or a quote would otherwise become a second command.
func TestShellQuoteIsOneWord(t *testing.T) {
	for _, s := range []string{
		"/root/.granule-guest-setup.sh",
		"/tmp/a b",
		"/tmp/x; touch /tmp/pwned",
		"/tmp/it's",
		"$(id)`id`\"\\",
		"-rf",
		"",
	} {
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(s)).Output()
		if err != nil {
			t.Fatalf("sh -c with %q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("ShellQuote(%q) came back from sh as %q", s, out)
		}
	}
}
