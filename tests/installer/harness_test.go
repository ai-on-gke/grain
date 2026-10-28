// Package installer holds the end-to-end tests that actually run
// scripts/setup.sh.
//
// tests/container drives the *image*; this drives the *installer*. The
// distinction cost a deployment: a fresh VM came up with the config-sync
// service running, the grain image pulled, and no grain-daemon.service at
// all, because seed_gcp_minter_key staged a credential as root and then
// handed it to a `grain` that runs as $GRAIN_USER inside a container.
// Nothing in CI ran setup.sh, so nothing could have caught it -- every
// test either drove the image (which was fine) or read the script's text
// (which looked fine).
//
// So this runs the script, as root, on the machine running the tests, and
// then asks systemd whether the deployment it was supposed to produce is
// actually there and serving. What it deliberately does *not* do is
// sandbox the script: it creates a real system user, writes real units
// into /etc/systemd/system, and starts a real service, because a fake of
// any of those is a fake of the thing that broke.
//
// That makes it destructive to the host it runs on, which is why the gate
// below is as narrow as it is: a throwaway CI runner, never a developer's
// laptop, and never without being asked explicitly.
//
//	GRAIN_TEST_IMAGE      the image to deploy, e.g. `grain-e2e:test`.
//	GRAIN_INSTALLER_E2E=1 explicit opt-in. Without it every test here
//	                      skips even where everything else is available,
//	                      because "the tests took over
//	                      /usr/local/bin/grain" is not a surprise anyone
//	                      should get from `go test ./...`.
//	root                  via passwordless sudo; setup.sh refuses otherwise.
//	systemd + docker      the two things the deployment is made of.
//
// Granule sandboxing stays off and the local Docker backend is selected.
// That is the supported path on a host without KVM, requires no nested
// virtualisation, and lets this suite dispatch a real isolated task rather
// than merely checking that an otherwise-idle service started.
//
// This suite also deployed the host executor (GRAIN_MODEL_EXEC_ENABLE)
// and watched systemd run it, until #1395 removed remote model execution
// -- the protocol, the listener, the installer's half of it and the pane
// that reported it. Those tests went with it; there is no second service
// to watch any more.
package installer

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var image = os.Getenv("GRAIN_TEST_IMAGE")

// requireInstaller is the gate, asked at the top of every test. Every
// clause of it names something this suite would otherwise destroy or fail
// on, and a skip says which.
func requireInstaller(t *testing.T) {
	t.Helper()
	switch {
	case image == "":
		t.Skip("GRAIN_TEST_IMAGE is unset; this suite needs an image to deploy")
	case os.Getenv("GRAIN_INSTALLER_E2E") != "1":
		t.Skip("GRAIN_INSTALLER_E2E is not 1; this suite takes over the host it runs on")
	case !canSudo():
		t.Skip("needs root, or passwordless sudo; setup.sh refuses otherwise")
	case !systemdRunning():
		t.Skip("needs a running systemd to install units into")
	case !dockerWorks():
		t.Skip("docker does not answer `docker info`")
	}
}

func canSudo() bool {
	if os.Geteuid() == 0 {
		return true
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// systemdStates are the states a systemd that actually runs units reports
// itself in. The state word rather than the exit code, because
// `is-system-running` exits 1 for "degraded" -- an ordinary state for a CI
// runner, and one this suite is happy with -- but also for "offline",
// which is what a machine with no systemd as PID 1 says, and there the
// destructive half of these tests would run against nothing and time out
// rather than skip.
var systemdStates = map[string]bool{
	"running": true, "degraded": true, "maintenance": true, "starting": true,
}

// systemdRunning is the gate: a systemd present, and in a state that
// accepts units.
//
// Asked as root, which is how every other systemctl in this suite is
// asked and the only way the answer is worth having. systemctl talks to
// systemd over the system bus for an unprivileged caller and over
// /run/systemd/private, which is root's alone, for a privileged one -- so
// on a machine with systemd as PID 1 but no dbus installed (a minimal
// image, and grain's own CI sandbox is one) the unprivileged question
// fails with "Failed to connect to bus" and this gate would skip the
// whole suite on a host that can run every one of its tests.
func systemdRunning() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return systemdStates[strings.TrimSpace(sudo("systemctl", "is-system-running").stdout)]
}

func dockerWorks() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

// --- running commands ---------------------------------------------------

// result is one finished command: what it printed and how it exited.
type result struct {
	argv     []string
	stdout   string
	stderr   string
	exitCode int
}

func (r result) String() string {
	return fmt.Sprintf("%s exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		strings.Join(r.argv, " "), r.exitCode, r.stdout, r.stderr)
}

// run executes a command and reports how it went, without judging it: the
// callers here care about exit codes as often as they care about output.
func run(argv ...string) result {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		code = -1
		stderr.WriteString(err.Error())
	}
	return result{argv: argv, stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

// sudo is run as root, which this process may already be.
func sudo(argv ...string) result {
	if os.Geteuid() == 0 {
		return run(argv...)
	}
	return run(append([]string{"sudo", "-n"}, argv...)...)
}

// The data directory is 0750 $GRAIN_USER and its secrets 0700, which is
// the point of it -- so the account running these tests cannot read it,
// and every filesystem assertion below has to look as root. Asking through
// sudo rather than loosening the deployment is what keeps the test honest
// about what it is inspecting.
func sudoTest(flag, path string) bool {
	return sudo("test", flag, path).exitCode == 0
}

func sudoUID(t *testing.T, path string) int {
	t.Helper()
	out := strings.TrimSpace(sudo("stat", "-c", "%u", path).stdout)
	uid, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("stat %s printed %q, not a uid", path, out)
	}
	return uid
}

func sudoRead(path string) string { return sudo("cat", path).stdout }

// --- odds and ends -------------------------------------------------------

// repoRootFromCaller is the checkout this test binary was compiled from,
// found by walking up from this file's own path rather than from the
// working directory. It is where the script under test is copied from,
// so a run tests this checkout's setup.sh rather than a deployed host's.
func repoRootFromCaller() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate this test file, so cannot locate the repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("%s does not look like the repository root: %w", root, err)
	}
	return root, nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitFor polls until check returns nil, or fails saying what never
// happened and what the last attempt said.
func waitFor(t *testing.T, what string, timeout time.Duration, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		last = check()
		if last == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, last)
}
