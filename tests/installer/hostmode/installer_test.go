// Package hostmode is the migration off host mode, end to end (task
// 5041). Host mode -- scripts/setup.sh with GRAIN_INSTALL_MODE=host, the
// daemon as a plain systemd service on the machine -- is gone, and every
// machine install is container mode plus grain hostd. What is left to
// test is the way an existing host-mode deployment leaves it:
//
//  1. a host-mode install, made by the last setup.sh that had the mode
//     (lastHostModeSetup, read out of this checkout's own history);
//  2. its Upgrade button, onto the build under test: the old unit's own
//     ExecStartPre takes the new binary out of the new image, and the
//     deployment has to come back up rather than end up broken;
//  3. this setup.sh refusing GRAIN_INSTALL_MODE=host, changing nothing;
//  4. a bare re-run of this setup.sh, which moves it to container mode
//     plus hostd, keeping its data directory and grain-daemon.service, and
//     removing what only host mode had left on the machine.
//
// The "host" is a privileged Debian container booted with systemd as
// PID 1, the same stand-in tests/installer/trixie uses and for the same
// reasons: a clean userspace, a writable cgroup tree, and the runner's
// own Docker socket mounted in so the image CI just built is reachable
// without a nested registry. A separate package from both of its
// neighbours, because CI runs ./tests/installer/... with -p 1 and each
// of the three owns real system state while it runs.
package hostmode

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const optIn = "GRAIN_INSTALLER_HOSTMODE_E2E"

// The account and the port this deployment is installed with. Both are
// fixed rather than found: the UI is bound inside the host container's
// own network namespace, where nothing else is listening, and the
// account is created in a userspace this test threw away a moment ago.
const (
	user   = "grain-hostmode-e2e"
	uiPort = 8420
	// adminUser is GRAIN_ADMIN_USER: the person installing, whom the
	// install makes a system admin with a password (grain/task-3688).
	adminUser = "e2e-operator"
)

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

func execIn(host string, argv ...string) result {
	return docker(append([]string{"exec", host}, argv...)...)
}

// shIn is execIn for the cases that want a shell's own reading of a
// path: a redirect, a glob, a pipeline.
func shIn(host, script string) result {
	return execIn(host, "sh", "-c", script)
}

func requireHostModeInstaller(t *testing.T) string {
	t.Helper()
	if os.Getenv(optIn) != "1" {
		t.Skip(optIn + " is not 1; this test starts a privileged systemd container and uses the host Docker socket")
	}
	image := os.Getenv("GRAIN_TEST_IMAGE")
	if image == "" {
		t.Skip("GRAIN_TEST_IMAGE is unset; this test needs the image setup.sh takes its binary out of")
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

func splitImage(ref string) (string, string, bool) {
	at := strings.LastIndex(ref, ":")
	if at <= 0 || strings.Contains(ref[at+1:], "/") {
		return "", "", false
	}
	return ref[:at], ref[at+1:], true
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

// setupCommand is one run of an installer inside the host container.
// Every variable here either names this fixture or turns off something
// a container-in-a-container cannot do: granule needs /dev/kvm. Sandboxes
// are local Docker containers, the backend a host without /dev/kvm gets;
// no task is dispatched here, so none is ever started.
func setupCommand(host, script, root, repo, tag string, extra ...string) []string {
	argv := []string{
		"exec", host, "env",
		"GRAIN_DATA_DIR=" + filepath.Join(root, "data"),
		"GRAIN_SANDBOX_DIR=" + filepath.Join(root, "sandboxes"),
		"GRAIN_USER=" + user,
		"GRAIN_IMAGE=" + repo,
		"GRAIN_IMAGE_TAG=" + tag,
		fmt.Sprintf("GRAIN_UI_ADDR=127.0.0.1:%d", uiPort),
		"GRAIN_CONTAINER_SANDBOXES=1",
		"GRAIN_GRANULE_ENABLE=0",
		"GRAIN_DISABLE_SUSPEND=0",
		"GRAIN_TAILSCALE_ENABLE=0",
	}
	for _, envVar := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "GOPROXY", "NPM_CONFIG_REGISTRY", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if v := os.Getenv(envVar); v != "" {
			argv = append(argv, envVar+"="+v)
		}
	}
	argv = append(argv, extra...)
	return append(argv, "bash", script)
}

// lastHostModeSetup is the commit whose scripts/setup.sh was the last to
// install host mode: main just before task 5041 removed it.
const lastHostModeSetup = "4cb28b7de5c1a0b31db53d2e5a6554efa83cbb38"

// TestSetupMovesAHostModeInstallToContainerModePlusHostd is the whole of
// the migration, in the order a deployment meets it. One test rather than
// several, because each step is only meaningful against the host the one
// before it left.
func TestSetupMovesAHostModeInstallToContainerModePlusHostd(t *testing.T) {
	grainImage := requireHostModeInstaller(t)
	repo, tag, ok := splitImage(grainImage)
	if !ok {
		t.Fatalf("GRAIN_TEST_IMAGE=%q has no tag", grainImage)
	}
	if existing := docker("container", "inspect", "grain-daemon"); existing.exitCode == 0 {
		t.Fatal("grain-daemon already exists; refusing to replace a container this test did not create")
	}

	// Kept under the checkout rather than os.TempDir, for
	// tests/installer/trixie's own reason: the Docker socket may belong
	// to a different mount namespace than this test process, and the
	// checkout is the path both are guaranteed to share.
	root, err := os.MkdirTemp(repoRoot(t), ".grain-installer-hostmode-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%d", os.Getpid())
	host := "grain-installer-hostmode-host-" + id
	baseImage := "grain-installer-hostmode-systemd:" + id
	t.Cleanup(func() {
		// Stop systemd before removing its child: Restart=on-failure can
		// otherwise recreate grain-daemon between the two cleanup calls.
		docker("rm", "--force", host)
		docker("rm", "--force", "grain-daemon")
		// The install owns the data tree as its own service uid; make
		// this bind-mounted fixture removable by the test process again.
		cleared := docker("run", "--rm", "--volume", root+":/cleanup", "debian:trixie", "chmod", "-R", "a+rwX", "/cleanup")
		if cleared.exitCode != 0 {
			t.Errorf("making the fixture removable: %s", cleared.combined())
		}
		docker("image", "rm", "--force", baseImage)
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing the fixture: %v", err)
		}
	})

	// systemd, and the Docker *client* only: host mode talks to the
	// engine through the socket mounted below, and installing the client
	// here rather than leaving it to setup.sh keeps this test about the
	// mode rather than about Debian's docker packaging, which
	// tests/installer/trixie already covers.
	dockerfile := `FROM debian:trixie
ENV container=docker
COPY apt.conf.d /etc/apt/apt.conf.d/
RUN printf 'Acquire::ForceIPv4 "true";\nAcquire::Retries "3";\nAcquire::http::Timeout "60";\nAcquire::https::Timeout "60";\n' > /etc/apt/apt.conf.d/99-grain-build \
	&& apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	ca-certificates systemd systemd-sysv docker-cli && rm -rf /var/lib/apt/lists/*
COPY docker.service /etc/systemd/system/docker.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
`
	// grain-daemon.service is written with Requires=docker.service, so
	// this userspace needs a unit by that name for the engine it reaches
	// over the mounted socket.
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
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	// The installer a host-mode deployment was made with, out of this
	// checkout's history: no file in the tree carries the mode any more.
	oldScript := filepath.Join(root, "setup-host-mode.sh")
	oldBody, err := exec.Command("git", "-C", repoRoot(t), "show", lastHostModeSetup+":scripts/setup.sh").Output()
	if err != nil {
		// A shallow checkout: fetch that one commit and ask again.
		_ = exec.Command("git", "-C", repoRoot(t), "fetch", "--quiet", "--depth", "1", "origin", lastHostModeSetup).Run()
		oldBody, err = exec.Command("git", "-C", repoRoot(t), "show", lastHostModeSetup+":scripts/setup.sh").Output()
	}
	if err != nil {
		t.Fatalf("reading the last host-mode setup.sh (%s) out of this checkout's history: %v -- the job needs the full history", lastHostModeSetup, err)
	}
	if err := os.WriteFile(oldScript, oldBody, 0o755); err != nil {
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
			t.Fatalf("building the systemd host:\n%s", built.combined())
		}
	}
	started := docker(
		"run", "--detach", "--privileged", "--cgroupns=host",
		"--name", host, "--hostname", "grain-hostmode-e2e",
		"--volume", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		// systemd mounts a fresh /run after the container starts, so the
		// socket arrives outside it and is linked into the live /run.
		"--volume", "/var/run/docker.sock:/host-docker.sock",
		"--volume", root+":"+root,
		baseImage,
	)
	if started.exitCode != 0 {
		t.Fatalf("starting the systemd host:\n%s", started.combined())
	}
	waitFor(t, "systemd in the host container", func() error {
		state := strings.TrimSpace(execIn(host, "systemctl", "is-system-running").stdout)
		if state != "running" && state != "degraded" {
			return fmt.Errorf("systemd is %q", state)
		}
		return nil
	})
	if linked := execIn(host, "ln", "-s", "/host-docker.sock", "/var/run/docker.sock"); linked.exitCode != 0 {
		t.Fatalf("making the host Docker socket visible after systemd mounted /run: %s", linked.combined())
	}
	if before := shIn(host, "command -v grain"); before.exitCode == 0 {
		t.Fatalf("the clean host already had a grain at %s", strings.TrimSpace(before.stdout))
	}

	// --- 1. a host-mode install, as the last installer with the mode made one
	installed := docker(setupCommand(host, oldScript, root, repo, tag, "GRAIN_INSTALL_MODE=host", "GRAIN_ADMIN_USER="+adminUser)...)
	if installed.exitCode != 0 {
		t.Fatalf("the last host-mode setup.sh did not install host mode:\n--- stdout ---\n%s\n--- stderr ---\n%s",
			installed.stdout, installed.stderr)
	}
	unitPath := "/etc/systemd/system/grain-daemon.service"
	unit := execIn(host, "cat", unitPath)
	if !strings.Contains(unit.stdout, "Environment=GRAIN_INSTALL_MODE=host") || strings.Contains(unit.stdout, "docker run") {
		t.Fatalf("the old installer did not write a host-mode unit:\n%s", unit.combined())
	}
	waitForAPI(t, host, "the host-mode daemon")
	// Something in the data directory that every later shape must still
	// have: written as the service account, which owns the directory.
	keep := filepath.Join(root, "data", "kept-across-the-migration")
	if wrote := execIn(host, "runuser", "-u", user, "--", "touch", keep); wrote.exitCode != 0 {
		t.Fatalf("writing %s: %s", keep, wrote.combined())
	}

	// --- 2. its Upgrade button, onto the build under test ---------------
	//
	// That button is a pull, a health check, a rewrite of the image ref
	// file, and a restart command (pkg/upgrade/image.go); driven here from
	// the two ends the daemon drives it from -- the ref file it would have
	// written and the control file it would have touched. The old unit's
	// ExecStartPre takes the binary out of whatever that file names, so
	// the image is the one under test with a marker added: "the new build
	// is installed" is then a fact about content. Tagged into the
	// repository the deployment was set up from: the pre-step installs
	// nothing a ref file names outside it (#2405).
	upgradeImage := repo + ":hostmode-upgrade-" + id
	upgradeDir := filepath.Join(root, "upgrade")
	if err := os.MkdirAll(upgradeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := "/usr/local/share/grain/src/UPGRADED-BY-THIS-TEST"
	if err := os.WriteFile(filepath.Join(upgradeDir, "Dockerfile"),
		[]byte("FROM "+grainImage+"\nRUN printf %s upgraded > "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker("image", "rm", "--force", upgradeImage) })
	if built := docker("build", "--network=host", "--tag", upgradeImage, upgradeDir); built.exitCode != 0 {
		t.Fatalf("building the image to upgrade onto:\n%s", built.combined())
	}
	refFile := filepath.Join(root, "data", "image.env")
	if written := shIn(host, "printf 'GRAIN_IMAGE=%s\\n' "+upgradeImage+" > "+refFile); written.exitCode != 0 {
		t.Fatalf("pointing the ref file at the upgraded image: %s", written.combined())
	}
	control := filepath.Join(root, "data", "control")
	if requested := execIn(host, "runuser", "-u", user, "--", "touch", filepath.Join(control, "restart")); requested.exitCode != 0 {
		t.Fatalf("%s could not write its own restart request: %s", user, requested.combined())
	}
	waitFor(t, "the upgraded build to be installed and running", func() error {
		if present := execIn(host, "test", "-e", marker); present.exitCode != 0 {
			return fmt.Errorf("%s is not installed", marker)
		}
		if active := execIn(host, "systemctl", "is-active", "grain-daemon.service"); strings.TrimSpace(active.stdout) != "active" {
			return fmt.Errorf("the daemon is %s", strings.TrimSpace(active.stdout))
		}
		return nil
	})
	// Not broken: the build under test runs as the old host-mode unit's
	// binary until the installer moves it.
	waitForAPI(t, host, "the host-mode daemon on the upgraded build")

	// --- 3. GRAIN_INSTALL_MODE=host is refused, and changes nothing ------
	refused := docker(setupCommand(host, script, root, repo, tag, "GRAIN_INSTALL_MODE=host")...)
	if refused.exitCode == 0 {
		t.Fatalf("this setup.sh accepted GRAIN_INSTALL_MODE=host:\n%s", refused.combined())
	}
	if !strings.Contains(refused.combined(), "GRAIN_INSTALL_MODE=host is no longer supported") ||
		!strings.Contains(refused.combined(), "container mode plus grain hostd") {
		t.Errorf("the refusal does not point to container mode plus grain hostd:\n%s", refused.combined())
	}
	if after := execIn(host, "cat", unitPath); after.stdout != unit.stdout {
		t.Errorf("a refused GRAIN_INSTALL_MODE=host rewrote the unit:\n%s", after.stdout)
	}

	// --- 4. a bare re-run migrates it ----------------------------------
	migrated := docker(setupCommand(host, script, root, repo, tag)...)
	if migrated.exitCode != 0 {
		t.Fatalf("this setup.sh did not migrate the host-mode install:\n--- stdout ---\n%s\n--- stderr ---\n%s",
			migrated.stdout, migrated.stderr)
	}
	// No agy is on this host's PATH, so hostd allowlists only the image's
	// own detached claude and codex, and no host CLI.
	for _, want := range []string{"moving this host-mode install to container mode plus grain hostd",
		"hostd:             installed", "clis: claude-detached, codex-detached;"} {
		if !strings.Contains(migrated.stdout, want) {
			t.Errorf("the migration does not say %q:\n%s", want, migrated.stdout)
		}
	}
	unit = execIn(host, "cat", unitPath)
	for _, want := range []string{"docker run --name grain-daemon", "-hostd-socket", "--volume " + filepath.Join(root, "data") + ":"} {
		if !strings.Contains(unit.stdout, want) {
			t.Errorf("the migrated grain-daemon.service does not carry %q:\n%s", want, unit.stdout)
		}
	}
	if strings.Contains(unit.stdout, "GRAIN_INSTALL_MODE=host") {
		t.Errorf("the migrated unit is still host mode:\n%s", unit.stdout)
	}
	for _, unitName := range []string{"grain-daemon.service", "grain-hostd.service"} {
		if active := execIn(host, "systemctl", "is-active", unitName); strings.TrimSpace(active.stdout) != "active" {
			journal := execIn(host, "journalctl", "-u", unitName, "-n", "50", "--no-pager")
			t.Errorf("%s is not active after the migration:\n%s\n%s", unitName, active.combined(), journal.combined())
		}
	}
	if kept := execIn(host, "test", "-e", keep); kept.exitCode != 0 {
		t.Errorf("the migration lost %s: the data directory is not the one host mode used", keep)
	}
	for _, left := range []string{"/etc/systemd/system/grain-daemon.service.d/grain.conf", "/etc/sudoers.d/zzz-grain-nft",
		"/usr/local/lib/grain/.extracted-image-id", "/usr/local/share/grain/src"} {
		if present := execIn(host, "test", "-e", left); present.exitCode == 0 {
			t.Errorf("the migration left host mode's %s behind", left)
		}
	}
	// Behind hostd /usr/local/bin/grain is the image's own binary again,
	// taken out by the extractor the container unit re-runs.
	if shebang := shIn(host, "head -c 2 /usr/local/bin/grain"); strings.TrimSpace(shebang.stdout) == "#!" {
		t.Error("/usr/local/bin/grain is a script; beside hostd it is the image's own binary")
	}
	if shebang := shIn(host, "head -c 2 /usr/local/bin/granule"); strings.TrimSpace(shebang.stdout) != "#!" {
		t.Error("/usr/local/bin/granule is not the image wrapper container mode writes")
	}
	// The migrated daemon is a container of the runner's own docker
	// engine, with --network host: it listens on the runner's loopback,
	// not the host container's, so it is asked from here.
	waitFor(t, "the migrated container-mode daemon to serve its API", func() error {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/sandboxes", uiPort))
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	})
	if env := execIn(host, "cat", "/etc/grain/cli.env"); !strings.Contains(env.stdout, fmt.Sprintf("GRAIN_SERVER=http://127.0.0.1:%d", uiPort)) {
		t.Errorf("/etc/grain/cli.env does not say where the migrated daemon listens: %s", env.combined())
	}
	assertNoAdminPassword(t, host, root, "after the migration")
}

// waitForAPI waits for the installed daemon to answer `grain list`
// through the CLI this host has, whichever kind it is.
func waitForAPI(t *testing.T, host, what string) {
	t.Helper()
	waitFor(t, what+" to serve its API", func() error {
		listed := execIn(host, "env", fmt.Sprintf("GRAIN_SERVER=http://127.0.0.1:%d", uiPort),
			"/usr/local/bin/grain", "list")
		if listed.exitCode != 0 {
			return fmt.Errorf("grain list: %s", listed.combined())
		}
		return nil
	})
}

// assertNoAdminPassword checks that a single-tenant install wrote no
// admin password: a sign-in account is a row the daemon refuses to start
// on (model.Store.CheckSingleTenant).
func assertNoAdminPassword(t *testing.T, host, root, when string) {
	t.Helper()
	file := filepath.Join(root, "data", "admin-password")
	if present := execIn(host, "test", "-e", file); present.exitCode == 0 {
		t.Errorf("%s, %s exists, but a single-tenant install has no sign-in account to give a password to", when, file)
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
