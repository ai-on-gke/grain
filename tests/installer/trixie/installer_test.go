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

// Package trixie exercises setup.sh on the Debian release whose docker
// packaging differs from the hosted runner's.
//
// This is deliberately a separate package from tests/installer. Both suites
// create a container named grain-daemon, and CI runs ./tests/installer/... with
// -p 1 so their real Docker work cannot overlap.
package trixie

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const optIn = "GRAIN_INSTALLER_TRIXIE_E2E"

type result struct {
	stdout   string
	stderr   string
	exitCode int
}

func (r result) combined() string { return r.stdout + r.stderr }

func run(input io.Reader, argv ...string) result {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = input
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
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

func docker(argv ...string) result {
	return run(nil, append([]string{"docker"}, argv...)...)
}

func requireTrixieInstaller(t *testing.T) string {
	t.Helper()
	if os.Getenv(optIn) != "1" {
		t.Skip(optIn + " is not 1; this test starts a privileged systemd container and uses the host Docker socket")
	}
	image := os.Getenv("GRAIN_TEST_IMAGE")
	if image == "" {
		t.Skip("GRAIN_TEST_IMAGE is unset; this test needs the image setup.sh will deploy")
	}
	if runtime.GOOS != "linux" {
		t.Skip("a privileged Linux container is required")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the host has no docker command")
	}
	if info := docker("info"); info.exitCode != 0 {
		t.Skip("the host Docker daemon is unavailable: " + info.combined())
	}
	ensurePackageCache(t)
	return image
}

func ensurePackageCache(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "172.17.0.1:53", 200*time.Millisecond)
	if err != nil {
		return
	}
	conn.Close()

	if _, err := os.Stat("/usr/local/share/ca-certificates/grain-ca.crt"); os.IsNotExist(err) {
		tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 500 * time.Millisecond}, "tcp", "172.17.0.1:443", &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			certs := tlsConn.ConnectionState().PeerCertificates
			tlsConn.Close()
			if len(certs) > 0 {
				rootCert := certs[len(certs)-1]
				pemBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCert.Raw})
				if len(pemBlock) > 0 {
					cmd := exec.Command("sudo", "-n", "sh", "-c", "cat > /usr/local/share/ca-certificates/grain-ca.crt && update-ca-certificates")
					cmd.Stdin = bytes.NewReader(pemBlock)
					_ = cmd.Run()
				}
			}
		}
	}

	resolv, err := os.ReadFile("/etc/resolv.conf")
	if err == nil && !strings.Contains(string(resolv), "172.17.0.1") {
		_ = exec.Command("sudo", "-n", "sh", "-c", "echo 'nameserver 172.17.0.1' > /etc/resolv.conf").Run()
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root: %v", root, err)
	}
	return root
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func splitImage(ref string) (string, string, bool) {
	at := strings.LastIndex(ref, ":")
	if at <= 0 || strings.Contains(ref[at+1:], "/") {
		return "", "", false
	}
	return ref[:at], ref[at+1:], true
}

func execIn(host string, argv ...string) result {
	return docker(append([]string{"exec", host}, argv...)...)
}

func waitFor(t *testing.T, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		last = check()
		if last == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s: %v", what, last)
}

func copyInto(t *testing.T, source, host, destination string) {
	t.Helper()
	if copied := docker("cp", source, host+":"+destination); copied.exitCode != 0 {
		t.Fatalf("copying %s into Trixie: %s", source, copied.combined())
	}
}

func setupCommand(host, script, root, repo, tag string, port int) []string {
	argv := []string{
		"exec", host, "env",
		"GRAIN_DATA_DIR=" + filepath.Join(root, "data"),
		"GRAIN_SANDBOX_DIR=" + filepath.Join(root, "sandboxes"),
		"GRAIN_USER=grain-trixie-e2e",
		"GRAIN_IMAGE=" + repo,
		"GRAIN_IMAGE_TAG=" + tag,
		fmt.Sprintf("GRAIN_UI_ADDR=127.0.0.1:%d", port),
		"GRAIN_CONTAINER_SANDBOXES=1",
		"GRAIN_GRANULE_ENABLE=0",
		"GRAIN_ENABLE_UI_UPGRADE=0",
		"GRAIN_DISABLE_SUSPEND=0",
		"GRAIN_TAILSCALE_ENABLE=0",
	}
	for _, envVar := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "GOPROXY", "NPM_CONFIG_REGISTRY", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if v := os.Getenv(envVar); v != "" {
			argv = append(argv, envVar+"="+v)
		}
	}
	return append(argv, "bash", script)
}

// TestSetupOnTrixieInstallsDockerAndHonorsDaemonMounts is section 5 of
// docs/test-plans/2026-09-13.md at the strongest boundary a hosted runner can
// provide: a clean Debian Trixie userspace booted with systemd as PID 1.
//
// The container is privileged because systemd needs a writable cgroup tree.
// It uses the runner's Docker socket rather than a nested image store: that
// keeps the branch image built by CI available without duplicating its layers.
// The fixture root is bind-mounted at the identical path, so the sibling
// grain-daemon container sees exactly the host paths written into its unit.
// Those two host capabilities are why this remains explicitly gated.
func TestSetupOnTrixieInstallsDockerAndHonorsDaemonMounts(t *testing.T) {
	grainImage := requireTrixieInstaller(t)
	repo, tag, ok := splitImage(grainImage)
	if !ok {
		t.Fatalf("GRAIN_TEST_IMAGE=%q has no tag", grainImage)
	}
	if existing := docker("container", "inspect", "grain-daemon"); existing.exitCode == 0 {
		t.Fatal("grain-daemon already exists; refusing to replace a container this test did not create")
	}

	// Keep the fixture under the checkout rather than os.TempDir. A Docker
	// socket may belong to the sandbox host rather than this test process's
	// mount namespace; the checkout is the path both are guaranteed to share.
	root, err := os.MkdirTemp(repoRoot(t), ".grain-installer-trixie-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%d", os.Getpid())
	host := "grain-installer-trixie-host-" + id
	baseImage := "grain-installer-trixie-systemd:" + id
	t.Cleanup(func() {
		// Stop systemd before removing its child: Restart=on-failure can
		// otherwise recreate grain-daemon between the two cleanup calls.
		docker("rm", "--force", host)
		docker("rm", "--force", "grain-daemon")
		// setup owns the data tree as its service uid. Make only this
		// bind-mounted fixture removable by the unprivileged test process.
		cleared := docker("run", "--rm", "--volume", root+":/cleanup", "debian:trixie", "chmod", "-R", "a+rwX", "/cleanup")
		if cleared.exitCode != 0 {
			t.Errorf("making the Trixie fixture removable: %s", cleared.combined())
		}
		docker("image", "rm", "--force", baseImage)
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing the Trixie fixture: %v", err)
		}
	})

	// Only systemd is baked into the host. In particular, neither docker.io
	// nor docker-cli is present when setup.sh starts.
	dockerfile := `FROM debian:trixie
ENV container=docker
COPY apt.conf.d /etc/apt/apt.conf.d/
RUN printf 'Acquire::ForceIPv4 "true";\nAcquire::Retries "3";\nAcquire::http::Timeout "60";\nAcquire::https::Timeout "60";\n' > /etc/apt/apt.conf.d/99-grain-build \
	&& apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates systemd systemd-sysv && rm -rf /var/lib/apt/lists/*
COPY docker.service /etc/systemd/system/docker.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
`
	dockerService := `[Unit]
Description=Docker engine provided through the mounted host socket

[Service]
Type=simple
ExecStart=/bin/sleep infinity

[Install]
WantedBy=multi-user.target
`
	aptConfDir := filepath.Join(root, "apt.conf.d")
	_ = os.MkdirAll(aptConfDir, 0o755)
	if entries, err := os.ReadDir("/etc/apt/apt.conf.d"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				if content, err := os.ReadFile(filepath.Join("/etc/apt/apt.conf.d", entry.Name())); err == nil {
					_ = os.WriteFile(filepath.Join(aptConfDir, entry.Name()), withoutLoopbackProxies(content), 0o644)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docker.service"), []byte(dockerService), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "setup.sh")
	setupBody, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, setupBody, 0o755); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 6; attempt++ {
		if pull := docker("pull", "debian:trixie"); pull.exitCode == 0 {
			break
		}
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	if built := docker("build", "--network=host", "--tag", baseImage, root); built.exitCode != 0 {
		if built = docker("build", "--network=host", "--tag", baseImage, root); built.exitCode != 0 {
			t.Fatalf("building the clean Trixie systemd host:\n%s", built.combined())
		}
	}
	started := docker(
		"run", "--detach", "--privileged", "--cgroupns=host",
		"--name", host, "--hostname", "grain-trixie-e2e",
		"--volume", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		// systemd mounts a fresh /run after the container starts, so mount
		// the host socket outside it and link it into the live /run below.
		"--volume", "/var/run/docker.sock:/host-docker.sock",
		"--volume", root+":"+root,
		baseImage,
	)
	if started.exitCode != 0 {
		t.Fatalf("starting the Trixie systemd host:\n%s", started.combined())
	}
	waitFor(t, "systemd in the Trixie host", func() error {
		state := strings.TrimSpace(execIn(host, "systemctl", "is-system-running").stdout)
		if state != "running" && state != "degraded" {
			return fmt.Errorf("systemd is %q", state)
		}
		return nil
	})
	if linked := execIn(host, "ln", "-s", "/host-docker.sock", "/var/run/docker.sock"); linked.exitCode != 0 {
		t.Fatalf("making the host Docker socket visible after systemd mounted /run: %s", linked.combined())
	}

	release := execIn(host, "sh", "-c", ". /etc/os-release && printf '%s' \"$VERSION_CODENAME\"")
	if strings.TrimSpace(release.stdout) != "trixie" {
		t.Fatalf("test host is %q, want trixie", strings.TrimSpace(release.stdout))
	}
	if before := execIn(host, "sh", "-c", "command -v docker"); before.exitCode == 0 {
		t.Fatalf("clean Trixie unexpectedly began with docker at %s", strings.TrimSpace(before.stdout))
	}

	installed := docker(setupCommand(host, script, root, repo, tag, freePort(t))...)
	if installed.exitCode != 0 {
		t.Fatalf("setup.sh did not install on clean Trixie:\n--- stdout ---\n%s\n--- stderr ---\n%s",
			installed.stdout, installed.stderr)
	}
	packages := execIn(host, "dpkg-query", "-W", "-f=${binary:Package}\n", "docker.io", "docker-cli")
	if packages.exitCode != 0 || !strings.Contains(packages.stdout, "docker.io") || !strings.Contains(packages.stdout, "docker-cli") {
		t.Fatalf("setup did not install both halves of Trixie's Docker packaging:\n%s", packages.combined())
	}
	if active := execIn(host, "systemctl", "is-active", "grain-daemon.service"); strings.TrimSpace(active.stdout) != "active" {
		t.Fatalf("installed daemon service is not active:\n%s", active.combined())
	}

	reftableReposAreConvertedOnARerun(t, host, script, root, repo, tag)

	fixture := filepath.Join(root, "host-fixture")
	fixtureContents := "read through the operator mount\n"
	if err := os.WriteFile(fixture, []byte(fixtureContents), 0o644); err != nil {
		t.Fatal(err)
	}
	validOverride := "GRAIN_EXTRA_DOCKER_RUN_ARGS=--volume=" + fixture + ":/run/grain-installer-fixture:ro\n"
	validFile := filepath.Join(root, "grain-daemon.valid.env")
	if err := os.WriteFile(validFile, []byte(validOverride), 0o644); err != nil {
		t.Fatal(err)
	}
	copyInto(t, validFile, host, "/etc/default/grain-daemon")

	dataMarker := filepath.Join(root, "data", "state-before-invalid-override")
	marked := execIn(host, "sh", "-c", "printf '%s' preserved > "+dataMarker)
	if marked.exitCode != 0 {
		t.Fatalf("writing the prior-state marker: %s", marked.combined())
	}
	imageRefBefore := execIn(host, "cat", filepath.Join(root, "data", "image.env"))
	if imageRefBefore.exitCode != 0 {
		t.Fatalf("reading the prior image ref: %s", imageRefBefore.combined())
	}

	restarted := execIn(host, "systemctl", "restart", "grain-daemon.service")
	if restarted.exitCode != 0 {
		t.Fatalf("restarting through systemd with the operator mount:\n%s", restarted.combined())
	}
	var gotFixture result
	waitFor(t, "the restarted daemon to read the operator fixture", func() error {
		gotFixture = docker("exec", "grain-daemon", "cat", "/run/grain-installer-fixture")
		if gotFixture.exitCode != 0 {
			return fmt.Errorf("%s", gotFixture.combined())
		}
		if gotFixture.stdout != fixtureContents {
			return fmt.Errorf("fixture is %q", gotFixture.stdout)
		}
		return nil
	})
	gotData := docker("exec", "grain-daemon", "cat", dataMarker)
	if gotData.exitCode != 0 || strings.TrimSpace(gotData.stdout) != "preserved" {
		t.Fatalf("daemon lost its existing data mount after restart: %s", gotData.combined())
	}

	invalidFlag := "--grain-deliberately-invalid-docker-run-flag"
	invalidOverride := "GRAIN_EXTRA_DOCKER_RUN_ARGS=" + invalidFlag + "\n"
	invalidFile := filepath.Join(root, "grain-daemon.invalid.env")
	if err := os.WriteFile(invalidFile, []byte(invalidOverride), 0o644); err != nil {
		t.Fatal(err)
	}
	copyInto(t, invalidFile, host, "/etc/default/grain-daemon")

	failedSetup := docker(setupCommand(host, script, root, repo, tag, freePort(t))...)
	if failedSetup.exitCode == 0 {
		t.Fatalf("setup unexpectedly accepted %s", invalidFlag)
	}
	if !strings.Contains(failedSetup.combined(), "GRAIN_EXTRA_DOCKER_RUN_ARGS") ||
		!strings.Contains(failedSetup.combined(), "grain-daemon.service failed to start") {
		t.Fatalf("setup failure does not identify the bad daemon override:\n%s", failedSetup.combined())
	}
	// Restart=on-failure retries the unit every RestartSec, and each retry
	// is active for the instant docker takes to refuse the flag -- so one
	// look can land on a retry. Left up means active over a whole cycle.
	steadilyActive := true
	for i := 0; i < 15 && steadilyActive; i++ {
		if state := execIn(host, "systemctl", "is-active", "grain-daemon.service"); strings.TrimSpace(state.stdout) != "active" {
			steadilyActive = false
		}
		time.Sleep(500 * time.Millisecond)
	}
	if steadilyActive {
		t.Fatalf("bad override left the service active")
	}
	journal := execIn(host, "journalctl", "-u", "grain-daemon.service", "-n", "30", "--no-pager")
	if !strings.Contains(journal.combined(), invalidFlag) {
		t.Fatalf("service journal does not show Docker rejecting %s:\n%s", invalidFlag, journal.combined())
	}
	if marker := execIn(host, "cat", dataMarker); strings.TrimSpace(marker.stdout) != "preserved" {
		t.Fatalf("failed setup overwrote prior data: %s", marker.combined())
	}
	imageRefAfter := execIn(host, "cat", filepath.Join(root, "data", "image.env"))
	if imageRefAfter.stdout != imageRefBefore.stdout {
		t.Fatalf("failed setup changed the prior image ref from %q to %q", imageRefBefore.stdout, imageRefAfter.stdout)
	}
	overrideAfter := execIn(host, "cat", "/etc/default/grain-daemon")
	if overrideAfter.stdout != invalidOverride {
		t.Fatalf("setup overwrote the operator's override: got %q, want %q", overrideAfter.stdout, invalidOverride)
	}
}

// reftableReposAreConvertedOnARerun is task 5195 on a host whose git makes
// reftable repositories by default, which Trixie's 2.47 does when told to
// and a Cloudtop's git is: a host-mode daemon running that git left its
// state repository and checkouts reftable, and the release image's git
// (bookworm's 2.39) cannot open them. A re-run of setup.sh -- the same one
// that moves a host-mode install to container mode -- has to convert each
// one with the host's git before the daemon starts, say which it
// converted, and leave a daemon that stays up and reads its state
// repository.
func reftableReposAreConvertedOnARerun(t *testing.T, host, script, root, repo, tag string) {
	t.Helper()
	const user = "grain-trixie-e2e"
	if installed := execIn(host, "sh", "-c", "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends git"); installed.exitCode != 0 {
		t.Fatalf("installing git on the Trixie host: %s", installed.combined())
	}
	if set := execIn(host, "git", "config", "--system", "init.defaultRefFormat", "reftable"); set.exitCode != 0 {
		t.Fatalf("making reftable the host git's default: %s", set.combined())
	}
	stateRepo := filepath.Join(root, "data", "state-repo")
	checkout := filepath.Join(root, "sandboxes", "host-mode-run", "checkout")
	if stopped := execIn(host, "systemctl", "stop", "grain-daemon.service"); stopped.exitCode != 0 {
		t.Fatalf("stopping the daemon to stage the host-mode repositories: %s", stopped.combined())
	}
	// The state repository as a host-mode daemon's git would have left it
	// (its reflogs dropped: 2.47 migrates none), and a checkout that git
	// created from scratch under the sandbox directory.
	stage := "set -e; git config --global user.name e2e; git config --global user.email e2e@example.com\n" +
		"rm -rf " + stateRepo + "/.git/logs\n" +
		"git -C " + stateRepo + " refs migrate --ref-format=reftable\n" +
		"mkdir -p " + filepath.Dir(checkout) + "\n" +
		"git init -q " + checkout + "\n" +
		"git -C " + checkout + " commit -q --allow-empty -m host-mode\n"
	if staged := execIn(host, "runuser", "-u", user, "--", "env", "HOME=/tmp", "sh", "-c", stage); staged.exitCode != 0 {
		t.Fatalf("staging reftable repositories as %s: %s", user, staged.combined())
	}
	for _, dir := range []string{stateRepo, checkout} {
		if got := execIn(host, "git", "config", "-f", dir+"/.git/config", "--get", "extensions.refstorage"); strings.TrimSpace(got.stdout) != "reftable" {
			t.Fatalf("the fixture at %s is not reftable: %s", dir, got.combined())
		}
	}
	var stateHead string
	if head := execIn(host, "runuser", "-u", user, "--", "git", "-C", stateRepo, "rev-parse", "--verify", "-q", "HEAD"); head.exitCode == 0 {
		stateHead = strings.TrimSpace(head.stdout)
	}

	rerun := docker(setupCommand(host, script, root, repo, tag, freePort(t))...)
	if rerun.exitCode != 0 {
		t.Fatalf("setup.sh did not re-run over reftable repositories:\n--- stdout ---\n%s\n--- stderr ---\n%s", rerun.stdout, rerun.stderr)
	}
	for _, dir := range []string{stateRepo, checkout} {
		if want := "converted " + dir + "/.git: reftable -> files"; !strings.Contains(rerun.stdout, want) {
			t.Errorf("the re-run does not report %q:\n%s", want, rerun.stdout)
		}
		if got := execIn(host, "git", "config", "-f", dir+"/.git/config", "--get", "extensions.refstorage"); strings.TrimSpace(got.stdout) != "" {
			t.Errorf("%s is still %s after the re-run", dir, strings.TrimSpace(got.stdout))
		}
		if owner := strings.TrimSpace(execIn(host, "stat", "-c", "%U", dir+"/.git/HEAD").stdout); owner != user {
			t.Errorf("%s/.git/HEAD is owned by %q after the conversion, want %s", dir, owner, user)
		}
	}
	if !strings.Contains(rerun.stdout, "Git:     converted to the files ref format") {
		t.Errorf("the summary does not list the converted repositories:\n%s", rerun.stdout)
	}
	if strings.Contains(rerun.stdout+rerun.stderr, "NOT converted") {
		t.Errorf("the re-run left a repository unconverted:\n%s\n%s", rerun.stdout, rerun.stderr)
	}
	if active := execIn(host, "systemctl", "is-active", "grain-daemon.service"); strings.TrimSpace(active.stdout) != "active" {
		t.Fatalf("the daemon is not running after the conversion:\n%s", active.combined())
	}
	// The daemon's own git -- the image's -- reads the state repository,
	// and its history still has the commit the host-mode one was on (the
	// daemon may have committed on top of it since).
	waitFor(t, "the daemon's git to read the converted state repository", func() error {
		if got := docker("exec", "grain-daemon", "git", "-C", stateRepo, "rev-parse", "--verify", "HEAD"); got.exitCode != 0 {
			return fmt.Errorf("%s", got.combined())
		}
		if stateHead == "" {
			return nil
		}
		if got := docker("exec", "grain-daemon", "git", "-C", stateRepo, "merge-base", "--is-ancestor", stateHead, "HEAD"); got.exitCode != 0 {
			return fmt.Errorf("the state repository's HEAD no longer descends from %s: %s", stateHead, got.combined())
		}
		return nil
	})
	if again := docker(setupCommand(host, script, root, repo, tag, freePort(t))...); again.exitCode != 0 || strings.Contains(again.stdout, "Converting reftable") {
		t.Errorf("a second re-run found something to convert again, or failed:\n%s\n%s", again.stdout, again.stderr)
	}
}

// withoutLoopbackProxies drops the apt proxy lines that name a loopback
// address. A sandbox's package cache can be one it reaches on its own
// 127.0.0.1 (a Kubernetes granule sandbox's is forwarded there), and the
// image build below runs with --network=host, where that still means
// this machine -- but the container it boots is on Docker's bridge,
// where 127.0.0.1 is the container itself and nothing is listening, so
// every apt-get setup.sh ran inside it was refused. Without the line
// apt goes to the archive directly, as it would on a real host.
func withoutLoopbackProxies(conf []byte) []byte {
	var kept []string
	for _, line := range strings.SplitAfter(string(conf), "\n") {
		if strings.Contains(line, "Proxy") &&
			(strings.Contains(line, "//127.") || strings.Contains(line, "//localhost") || strings.Contains(line, "//[::1]")) {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, ""))
}

func TestWithoutLoopbackProxiesKeepsEverythingElse(t *testing.T) {
	in := "Acquire::Retries \"3\";\n" +
		"Acquire::http::Proxy::deb.debian.org \"http://127.0.0.1:9729/\";\n" +
		"Acquire::https::Proxy::deb.debian.org \"DIRECT\";\n" +
		"Acquire::http::Proxy::ftp.debian.org \"http://172.17.0.1:9729/\";\n"
	want := "Acquire::Retries \"3\";\n" +
		"Acquire::https::Proxy::deb.debian.org \"DIRECT\";\n" +
		"Acquire::http::Proxy::ftp.debian.org \"http://172.17.0.1:9729/\";\n"
	if got := string(withoutLoopbackProxies([]byte(in))); got != want {
		t.Errorf("withoutLoopbackProxies =\n%s\nwant\n%s", got, want)
	}
}
