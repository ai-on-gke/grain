package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

// wrappedRootfs is a wrapped image's filesystem as granule-container finds
// it: programs at paths that only exist inside it.
func wrappedRootfs(t *testing.T, programs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range programs {
		path := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The path lookup happens in the wrapped filesystem but the answer is the
// path *after* the chroot: the guest's own PATH has nothing to do with
// it, and a path resolved against the guest would exec the wrong binary
// or none at all.
func TestLookPathAnswersWithTheContainersOwnPath(t *testing.T) {
	rootfs := wrappedRootfs(t, "usr/bin/serve", "bin/busybox")
	for _, tc := range []struct{ name, pathEnv, want string }{
		{"serve", "/usr/local/bin:/usr/bin:/bin", "/usr/bin/serve"},
		{"busybox", "/usr/local/bin:/usr/bin:/bin", "/bin/busybox"},
		{"/bin/busybox", "", "/bin/busybox"},
	} {
		got, err := lookPath(rootfs, tc.name, tc.pathEnv)
		if err != nil {
			t.Errorf("lookPath(%q): %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("lookPath(%q) is %q, want %q", tc.name, got, tc.want)
		}
		if strings.HasPrefix(got, rootfs) {
			t.Errorf("lookPath(%q) answered with a path outside the chroot: %q", tc.name, got)
		}
	}
}

// The most ordinary image there is: Alpine's /bin/sh is a symlink to the
// absolute path /bin/busybox, which means /bin/busybox *inside the
// container*. Resolving it against the guest instead finds nothing and
// reports the image's entrypoint as missing, which is what a real boot of
// a wrapped alpine did before this was handled.
func TestLookPathFollowsSymlinksInsideTheContainer(t *testing.T) {
	rootfs := wrappedRootfs(t, "bin/busybox", "opt/app/real")
	symlink(t, filepath.Join(rootfs, "bin", "sh"), "/bin/busybox")
	symlink(t, filepath.Join(rootfs, "usr", "bin", "app"), "../../opt/app/real")
	// A link out of the wrapped filesystem cannot escape it, because the
	// chroot it is checked for will not let it either.
	symlink(t, filepath.Join(rootfs, "bin", "escape"), "/../../../bin/busybox")

	for _, tc := range []struct{ name, want string }{
		{"sh", "/bin/sh"},
		{"app", "/usr/bin/app"},
		{"escape", "/bin/escape"},
	} {
		got, err := lookPath(rootfs, tc.name, "/usr/bin:/bin")
		if err != nil {
			t.Errorf("lookPath(%q): %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("lookPath(%q) is %q, want %q", tc.name, got, tc.want)
		}
	}

	// A symlink pointing at nothing is still a missing command.
	symlink(t, filepath.Join(rootfs, "bin", "dangling"), "/bin/absent")
	if _, err := lookPath(rootfs, "dangling", "/bin"); err == nil {
		t.Error("a dangling symlink was accepted as a command")
	}
}

func symlink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestLookPathSaysWhatIsMissing(t *testing.T) {
	rootfs := wrappedRootfs(t, "usr/bin/serve")
	if err := os.WriteFile(filepath.Join(rootfs, "usr", "bin", "data"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lookPath(rootfs, "absent", "/usr/bin"); err == nil || !strings.Contains(err.Error(), "not on its own PATH") {
		t.Errorf("a command that is not in the image gave %v", err)
	}
	// A file that is there but not executable is the more confusing of
	// the two failures, since the path exists.
	if _, err := lookPath(rootfs, "/usr/bin/data", ""); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("a non-executable command gave %v", err)
	}
	if _, err := lookPath(rootfs, "data", "/usr/bin"); err == nil {
		t.Error("a non-executable file was found on the PATH")
	}
}

// A container runtime gives a process a HOME and a PATH whether or not
// its image set them; a shell in a wrapped image with no HOME writes its
// history to the root of the filesystem.
func TestEnvironFillsInWhatAContainerRuntimeWould(t *testing.T) {
	spec := containerspec.Spec{Env: []string{"LANG=C"}}
	env := environ(spec, containerspec.Credential{UID: 1000, Home: "/srv/app", Set: true})
	if !contains(env, "HOME=/srv/app") {
		t.Errorf("the user's home did not reach the environment: %v", env)
	}
	if !contains(env, "PATH="+containerspec.DefaultPath) {
		t.Errorf("no PATH in %v", env)
	}

	// What the image says, the image keeps.
	spec = containerspec.Spec{Env: []string{"HOME=/elsewhere", "PATH=/opt/bin"}}
	env = environ(spec, containerspec.Credential{Home: "/srv/app", Set: true})
	if !contains(env, "HOME=/elsewhere") || contains(env, "HOME=/srv/app") {
		t.Errorf("the image's own HOME was overridden: %v", env)
	}
	if contains(env, "PATH="+containerspec.DefaultPath) {
		t.Errorf("the image's own PATH was overridden: %v", env)
	}

	// A user with no home of its own still gets one, as it would under a
	// container runtime.
	env = environ(containerspec.Spec{}, containerspec.Credential{UID: 65532, Set: true})
	if !contains(env, "HOME=/") {
		t.Errorf("a user with no home entry got %v", env)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Restarting the runner must not stack a second /proc on the first.
func TestMountedReadsMountinfo(t *testing.T) {
	const mountinfo = "23 28 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw\n" +
		"99 28 0:42 / /var/lib/granule/container/rootfs/proc rw,relatime shared:9 - proc proc rw\n"
	if !mountedIn(mountinfo, "/var/lib/granule/container/rootfs/proc") {
		t.Error("an existing mount was not noticed")
	}
	if mountedIn(mountinfo, "/var/lib/granule/container/rootfs/sys") {
		t.Error("a mount that is not there was reported")
	}
}

// TestLookPathNativeRootfs verifies lookPath resolves commands when Rootfs is "/".
func TestLookPathNativeRootfs(t *testing.T) {
	got, err := lookPath("/", "sh", "/bin:/usr/bin")
	if err != nil {
		t.Fatalf("lookPath(/, sh): %v", err)
	}
	if got != "/bin/sh" && got != "/usr/bin/sh" {
		t.Errorf("lookPath(/, sh) = %q, want /bin/sh or /usr/bin/sh", got)
	}
}

// TestRunNativeRootfs verifies running a native workload with Rootfs: "/".
func TestRunNativeRootfs(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"exit 0"},
		HaltOnExit: false,
	}
	code, asked, err := run(spec, workloadio.NewBroker(0))
	if err != nil {
		t.Fatalf("run(spec): %v", err)
	}
	if asked {
		t.Errorf("asked is true, want false")
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunNativeRootfsNonZeroExit(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"exit 42"},
		HaltOnExit: false,
	}
	code, asked, err := run(spec, workloadio.NewBroker(0))
	if err != nil {
		t.Fatalf("run(spec): %v", err)
	}
	if asked {
		t.Errorf("asked is true, want false")
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestRunMissingRootfs(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/nonexistent-rootfs-path-12345",
		Entrypoint: []string{"/bin/sh"},
	}
	_, _, err := run(spec, workloadio.NewBroker(0))
	if err == nil {
		t.Fatal("expected error on missing rootfs, got nil")
	}
}

// The container's stdout and stderr reach the broker each as its own
// stream, in the order written, for the host that attaches after the
// container has already finished -- as well as reaching the console.
func TestRunSendsStreamsToBroker(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"echo out1; echo err1 >&2; echo out2"},
	}
	broker := workloadio.NewBroker(0)
	if _, _, err := run(spec, broker); err != nil {
		t.Fatalf("run: %v", err)
	}

	host, guest := net.Pipe()
	defer host.Close()
	go broker.Serve(guest)
	go broker.Close(5 * time.Second)

	br := bufio.NewReader(host)
	if _, err := workloadio.ReadOpening(br); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			break
		}
		switch typ {
		case execwire.TypeStdout:
			stdout.Write(payload)
		case execwire.TypeStderr:
			stderr.Write(payload)
		}
	}
	if stdout.String() != "out1\nout2\n" || stderr.String() != "err1\n" {
		t.Errorf("stdout = %q, stderr = %q; want %q and %q", stdout.String(), stderr.String(), "out1\nout2\n", "err1\n")
	}
}

func TestDrain(t *testing.T) {
	ch1 := make(chan struct{})
	ch2 := make(chan struct{})
	close(ch1)
	close(ch2)
	drain(100*time.Millisecond, ch1, ch2)
}

func TestCopyInto(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootfs := filepath.Join(dir, "rootfs")
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyInto(rootfs, src); err != nil {
		t.Fatalf("copyInto failed: %v", err)
	}

	dest := filepath.Join(rootfs, src)
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "hello" {
		t.Errorf("dest file content = %q, err = %v", string(data), err)
	}
}

func TestExecutableIn(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "myexe")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := executableIn(dir, "myexe"); err != nil {
		t.Errorf("executableIn(myexe) = %v, want nil", err)
	}

	// directory is not executable
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := executableIn(dir, "subdir"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("executableIn(subdir) = %v, want fs.ErrPermission", err)
	}
}

// The spec granule-container runs is the baked one with the VM's override
// merged in, and it leaves that spec where granule-agent's container exec
// looks for it -- owner-only, since an override's environment may carry
// secrets.
func TestEffectiveSpecMergesTheOverrideAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "container.json")
	baked := containerspec.Spec{
		Rootfs:     containerspec.RootfsDir,
		Entrypoint: []string{"/entry"},
		Cmd:        []string{"serve"},
		Env:        []string{"PATH=/bin", "MODE=prod"},
		User:       "app",
	}
	data, err := baked.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0o644); err != nil {
		t.Fatal(err)
	}

	sysBlock, dev := filepath.Join(dir, "sys"), filepath.Join(dir, "dev")
	disk, err := containerspec.EncodeOverrideDisk(containerspec.Override{
		Entrypoint: []string{"/bin/sh"},
		Env:        []string{"MODE=dev", "SECRET=s3cret"},
		WorkingDir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(sysBlock, "vdb"), dev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sysBlock, "vdb", "serial"), []byte(containerspec.OverrideSerial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "vdb"), disk, 0o644); err != nil {
		t.Fatal(err)
	}

	effective := filepath.Join(dir, "run", "granule", "container.json")
	spec, err := effectiveSpec(config, effective, sysBlock, dev)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(spec.Argv(), " "); got != "/bin/sh" {
		t.Errorf("argv = %q, want the override's entrypoint with the image's command dropped", got)
	}
	if v, _ := spec.LookupEnv("MODE"); v != "dev" || spec.User != "app" || spec.WorkingDir != "/work" {
		t.Errorf("spec = %+v", spec)
	}

	written, err := containerspec.Load(effective)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(written.Argv(), " ") != "/bin/sh" || written.WorkingDir != "/work" {
		t.Errorf("the effective spec on disk is %+v", written)
	}
	if fi, err := os.Stat(effective); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("effective spec mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}

	// No override: the baked spec, still recorded.
	if err := os.Remove(filepath.Join(dev, "vdb")); err != nil {
		t.Fatal(err)
	}
	spec, err = effectiveSpec(config, effective, sysBlock, dev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(spec.Argv(), " ") != "/entry serve" {
		t.Errorf("argv = %q with no override", spec.Argv())
	}
	if written, err := containerspec.Load(effective); err != nil || strings.Join(written.Argv(), " ") != "/entry serve" {
		t.Errorf("the effective spec on disk is %+v (%v), want the baked one", written, err)
	}
}

// attachHost attaches a host to broker and returns its connection and
// everything the guest sends it, as one string per stream, once the
// broker closes.
func attachHost(t *testing.T, broker *workloadio.Broker) (net.Conn, func() (stdout, stderr string)) {
	t.Helper()
	host, guest := net.Pipe()
	t.Cleanup(func() { host.Close() })
	go broker.Serve(guest)
	br := bufio.NewReader(host)
	if _, err := workloadio.ReadOpening(br); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	read := make(chan struct{})
	go func() {
		defer close(read)
		for {
			typ, payload, err := execwire.ReadFrame(br)
			if err != nil {
				return
			}
			switch typ {
			case execwire.TypeStdout:
				stdout.Write(payload)
			case execwire.TypeStderr:
				stderr.Write(payload)
			}
		}
	}()
	return host, func() (string, string) {
		<-read
		return stdout.String(), stderr.String()
	}
}

// A container started with its stdin open reads what the host sends,
// and the host closing its end is the container's end-of-file: cat
// finishes.
func TestRunGivesTheContainerTheHostsStdin(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"cat; echo done >&2"},
		OpenStdin:  true,
	}
	broker := workloadio.NewBroker(0)
	broker.OfferInput(spec.OpenStdin, spec.Tty)
	host, output := attachHost(t, broker)
	go func() {
		execwire.WriteFrame(host, execwire.TypeStdin, []byte("hello "))
		execwire.WriteFrame(host, execwire.TypeStdin, []byte("world\n"))
		execwire.WriteFrame(host, execwire.TypeStdinClose, nil)
	}()

	result := make(chan error, 1)
	go func() {
		code, _, err := run(spec, broker)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit status %d", code)
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the container never saw its stdin end")
	}
	broker.Close(5 * time.Second)
	stdout, stderr := output()
	if stdout != "hello world\n" || stderr != "done\n" {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

// Without its stdin open the container reads /dev/null, as before.
func TestRunGivesTheContainerDevNullByDefault(t *testing.T) {
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"cat; echo eof"},
	}
	broker := workloadio.NewBroker(0)
	done := make(chan error, 1)
	go func() { _, _, err := run(spec, broker); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cat waited on a stdin that should have been /dev/null")
	}
}

// A container on a terminal has one there -- for all three streams,
// whose output arrives as stdout -- sized as the host asks, and the
// host's end-of-file reaches a reader in canonical mode even after an
// unfinished line.
func TestRunOnATerminal(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx here")
	}
	spec := containerspec.Spec{
		Rootfs:     "/",
		Entrypoint: []string{"/bin/sh", "-c"},
		// Waits for the size to arrive rather than racing it.
		Cmd:       []string{`while [ "$(stty size)" != "40 120" ]; do sleep 0.02; done; [ -t 0 ] && [ -t 2 ] && echo tty-ok; stty -echo; echo ready; cat; echo; echo cat-done >&2`},
		OpenStdin: true,
		Tty:       true,
	}
	broker := workloadio.NewBroker(0)
	broker.OfferInput(spec.OpenStdin, spec.Tty)
	host, output := attachHost(t, broker)
	execwire.WriteFrame(host, execwire.TypeWinsize, execwire.EncodeWinsize(120, 40))

	result := make(chan error, 1)
	go func() {
		code, _, err := run(spec, broker)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit status %d", code)
		}
		result <- err
	}()
	// Typed only once echo is off, so that what comes back is cat's.
	time.Sleep(500 * time.Millisecond)
	execwire.WriteFrame(host, execwire.TypeStdin, []byte("partial"))
	execwire.WriteFrame(host, execwire.TypeStdinClose, nil)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the container on a terminal never saw its stdin end")
	}
	broker.Close(5 * time.Second)
	stdout, stderr := output()
	for _, want := range []string{"tty-ok", "ready", "partial", "cat-done"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want everything on the terminal's one stream", stderr)
	}
}

func TestMountShares(t *testing.T) {
	rootfs := t.TempDir()
	// An image whose /data is a symlink out to the guest's /var: the
	// share goes on the container's /var instead, never the guest's.
	if err := os.Symlink("/var", filepath.Join(rootfs, "data")); err != nil {
		t.Fatal(err)
	}
	type call struct {
		source, target, fstype string
		flags                  uintptr
	}
	var calls []call
	mount := func(source, target, fstype string, flags uintptr, data string) error {
		calls = append(calls, call{source, target, fstype, flags})
		return nil
	}
	shares, err := mountShares(rootfs, []containerspec.Mount{
		{Tag: "cache", Destination: "/data/cache"},
		{Tag: "data", Destination: "/data", ReadOnly: true},
	}, mount)
	if err != nil {
		t.Fatalf("mountShares error = %v", err)
	}
	want := []call{
		{"data", filepath.Join(rootfs, "var"), "virtiofs", syscall.MS_RDONLY},
		{"cache", filepath.Join(rootfs, "var/cache"), "virtiofs", 0},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("mounts = %+v, want %+v", calls, want)
	}
	if !shares[filepath.Join(rootfs, "var")] || !shares[filepath.Join(rootfs, "var/cache")] || len(shares) != 2 {
		t.Errorf("shares = %v", shares)
	}

	if _, err := mountShares(rootfs, []containerspec.Mount{{Tag: "x", Destination: "/.."}}, mount); err == nil {
		t.Errorf("mountShares mounted on the container's root")
	}
	failing := func(string, string, string, uintptr, string) error { return syscall.ENODEV }
	if _, err := mountShares(rootfs, []containerspec.Mount{{Tag: "x", Destination: "/x"}}, failing); !errors.Is(err, syscall.ENODEV) {
		t.Errorf("mountShares error = %v, want the mount's ENODEV", err)
	}
}

// The helper granule-container starts the workload through is this
// program, and under test this program is the test binary: it has to be
// let through to enterMain before the tests run, as main lets it.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv(enterEnv); ok {
		enterMain()
	}
	os.Exit(m.Run())
}

// enterCommand hands the helper what the Cmd was going to run, and makes
// the Cmd start the helper instead: as root, in the guest's root, in a
// new mount namespace.
func TestEnterCommandStartsTheHelper(t *testing.T) {
	cmd := &exec.Cmd{
		Path:        "/bin/app",
		Args:        []string{"app", "-x"},
		Env:         []string{"A=1"},
		Dir:         "/srv",
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: 7, Gid: 8}},
	}
	if _, err := enterCommand(cmd, "/rootfs", t.Logf); err != nil {
		t.Fatal(err)
	}
	defer cmd.ExtraFiles[0].Close()
	self, _ := os.Executable()
	if cmd.Path != self || cmd.Dir != "" || cmd.SysProcAttr.Credential != nil || cmd.SysProcAttr.Chroot != "" {
		t.Errorf("the helper runs as %q in %q with %+v, want %q in the guest's root as root", cmd.Path, cmd.Dir, cmd.SysProcAttr, self)
	}
	if cmd.SysProcAttr.Unshareflags&syscall.CLONE_NEWNS == 0 || !cmd.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr = %+v, want a new mount namespace and the process group kept", cmd.SysProcAttr)
	}
	if len(cmd.Env) != 1 || len(cmd.ExtraFiles) != 1 {
		t.Fatalf("Env = %q, ExtraFiles = %d, want the entry alone and the report pipe", cmd.Env, len(cmd.ExtraFiles))
	}
	var e entry
	if err := json.Unmarshal([]byte(strings.TrimPrefix(cmd.Env[0], enterEnv+"=")), &e); err != nil {
		t.Fatal(err)
	}
	want := entry{Rootfs: "/rootfs", Path: "/bin/app", Args: []string{"app", "-x"}, Env: []string{"A=1"}, Dir: "/srv", Credential: &syscall.Credential{Uid: 7, Gid: 8}}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("entry = %+v, want %+v", e, want)
	}
}

// The point of entering rather than chrooting: the container's "/" is a
// mount, the root of a mount namespace of its own, so the workload can
// make user namespaces and run runc; it has a cgroup2 hierarchy; and none
// of it reaches the guest's mount table. Root only, since it mounts.
func TestRunPivotsIntoTheRootfs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to mount and pivot_root")
	}
	for _, tool := range []string{"unshare", "mountpoint"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s: %v", tool, err)
		}
	}
	rootfs := t.TempDir()
	// The guest's own programs, bound in; the merged-/usr symlinks are
	// copied as symlinks. /etc stays the rootfs's own, since prepare
	// writes into it.
	for _, dir := range []string{"usr", "bin", "sbin", "lib", "lib64"} {
		src := "/" + dir
		fi, err := os.Lstat(src)
		if err != nil {
			continue
		}
		dst := filepath.Join(rootfs, dir)
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, dst); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unmountPrivately(dst) })
	}
	t.Cleanup(func() { unmountPrepared(rootfs) })
	report := filepath.Join(rootfs, "report")
	spec := containerspec.Spec{
		Rootfs:     rootfs,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd: []string{`exec >/report 2>&1
mountpoint -q / && echo root-is-a-mount
grep -q ' /sys/fs/cgroup .* - cgroup2 ' /proc/self/mountinfo && echo cgroup2
unshare -U true && echo userns
test -e /report && echo inside`},
	}
	code, _, err := run(spec, workloadio.NewBroker(0))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, _ := os.ReadFile(report)
	if code != 0 {
		t.Fatalf("the workload exited %d: %s", code, data)
	}
	for _, want := range []string{"root-is-a-mount", "cgroup2", "userns", "inside"} {
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("the workload's report is missing %q:\n%s", want, data)
		}
	}
	// The bind of the rootfs onto itself was the workload's namespace's
	// alone.
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	if mountedIn(string(mountinfo), rootfs) {
		t.Errorf("%s is mounted in the guest's own namespace after the container ran", rootfs)
	}
}

// A helper that cannot reach the workload's exec says why, and run
// reports it as a container that never started.
func TestRunReportsWhatStoppedTheHelper(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to mount and pivot_root")
	}
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Executable to LookPath, but nothing the kernel can exec.
	if err := os.WriteFile(filepath.Join(rootfs, "bin", "app"), []byte("not a program"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unmountPrepared(rootfs) })
	_, _, err := run(containerspec.Spec{Rootfs: rootfs, Entrypoint: []string{"/bin/app"}}, workloadio.NewBroker(0))
	if err == nil || !strings.Contains(err.Error(), "exec /bin/app") {
		t.Fatalf("run = %v, want the helper's exec error", err)
	}
}

// unmountPrepared takes down what prepare mounted in rootfs.
func unmountPrepared(rootfs string) {
	for _, dir := range []string{"proc", "sys", "dev"} {
		unmountPrivately(filepath.Join(rootfs, dir))
	}
}

// unmountPrivately unmounts path and everything under it without
// touching the mounts they were bound from. prepare's /dev is a
// recursive bind of the host's, in the host's own namespace, and so a
// peer of it: unmounted as it is, its /dev/pts goes from under the host's
// /dev as well.
func unmountPrivately(path string) {
	_ = syscall.Mount("", path, "", syscall.MS_REC|syscall.MS_PRIVATE, "")
	_ = syscall.Unmount(path, syscall.MNT_DETACH)
}
