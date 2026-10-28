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

package hostinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func stepStatus(r Result, name string) (Step, bool) {
	for _, s := range r.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return Step{}, false
}

// stubPreflight configures sys so preflight finds every hard
// requirement it checks already satisfied -- the shape of a host that
// has them, for a test whose own point is something further down
// initFor's step order.
func stubPreflight(sys *fakeSystem) {
	sys.on("GNU Make 4.3", nil, "make", "--version")
	sys.on("10.8.2", nil, "npm", "--version")
	sys.on("", nil, "test", "-e", "/dev/kvm")
}

func baseOptions(t *testing.T) Options {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/x\n\ngo 1.26.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Options{
		RepoRoot: repoRoot,
		DataDir:  t.TempDir(),
	}
}

// --- Options.validate ------------------------------------------------

func TestValidateRejectsWildcardGitHubPattern(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.GitHubCredentialName = "bot"
	opts.GitHubToken = "ghp_x"
	opts.GitHubRepos = []string{"*"}
	if err := opts.validate(); err == nil {
		t.Fatalf("expected an error for a \"*\" entry in GitHubRepos")
	}
}

func TestValidateRequiresRepoRootAndDataDir(t *testing.T) {
	t.Parallel()
	if err := (Options{}).validate(); err == nil {
		t.Fatalf("expected an error with neither RepoRoot nor DataDir set")
	}
	if err := (Options{RepoRoot: "/x"}).validate(); err == nil {
		t.Fatalf("expected an error with no DataDir")
	}
}

// TestWithDefaultsMakesEveryPathAbsolute: docs/grain-init.md's own
// `-repo-root .` reached the systemd unit as ExecStart=./bin/grain
// (rejected by systemd as a bad unit file setting) and made
// ensureGrainBinary copy bin/grain onto ./bin/grain, i.e. onto itself.
func TestWithDefaultsMakesEveryPathAbsolute(t *testing.T) {
	t.Parallel()
	opts := Options{RepoRoot: ".", DataDir: "data", Platform: PlatformLinuxContainer}.withDefaults()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"RepoRoot":          opts.RepoRoot,
		"DataDir":           opts.DataDir,
		"GrainBinaryPath":   opts.GrainBinaryPath,
		"GranuleBinaryPath": opts.GranuleBinaryPath,
		"ClaudeHome":        opts.ClaudeHome,
	} {
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q, want an absolute path", name, got)
		}
	}
	if want := filepath.Join(wd, "bin", "grain"); opts.GrainBinaryPath != want {
		t.Errorf("GrainBinaryPath = %q, want %q -- the path make build writes, so it is never copied onto itself", opts.GrainBinaryPath, want)
	}
	unit := daemonUnit(opts, "10.241.0.1")
	if !strings.Contains(unit, "ExecStart="+filepath.Join(wd, "bin", "grain")+" daemon -data-dir "+filepath.Join(wd, "data")+" ") {
		t.Errorf("unit's ExecStart is not absolute:\n%s", unit)
	}
	if (Options{}).withDefaults().RepoRoot != "" {
		t.Errorf("an unset RepoRoot must stay unset for validate to refuse, not become the working directory")
	}
}

func TestValidateRequiresCredentialNameWithToken(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.GitHubToken = "ghp_x"
	if err := opts.validate(); err == nil {
		t.Fatalf("expected an error: a token with no credential name to write it under")
	}
}

// --- a bare host, by convention -----------------------------------------
//
// fakeSystem returns errNotFound for everything it was not told about,
// and every ensureX check in this package treats "the command errored"
// as "not installed/not built/not running yet" -- so a fakeSystem with
// only the handful of responses a test configures already behaves like
// a bare host for everything else.

func TestInitRefusesNonLinuxAndRunsNoOtherStep(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true

	opts := baseOptions(t)
	result, err := initFor(context.Background(), sys, opts, "darwin")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("Steps = %+v, want exactly one (the platform refusal, with nothing run after it)", result.Steps)
	}
	s := result.Steps[0]
	if s.Status != StepRefused {
		t.Errorf("status = %s, want %s", s.Status, StepRefused)
	}
	if !strings.Contains(s.Detail, "darwin") {
		t.Errorf("detail = %q, want it to name the platform", s.Detail)
	}
	if len(sys.calls) != 0 {
		t.Errorf("initFor ran a command on an unsupported platform: %v", sys.calls)
	}
	if len(sys.httpCalls) != 0 {
		t.Errorf("initFor made an HTTP call on an unsupported platform: %v", sys.httpCalls)
	}
}

func TestInitRefusesNonLinuxEvenOnDryRun(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true

	opts := baseOptions(t)
	opts.DryRun = true
	result, err := initFor(context.Background(), sys, opts, "windows")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Status != StepRefused {
		t.Fatalf("Steps = %+v, want exactly one StepRefused -- -dry-run must not paper over this", result.Steps)
	}
}

func TestInitDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	stubPreflight(sys)
	// The one response a dry run still needs: ensureSandboxNetwork reads
	// the network's state even on a dry run (it is a read, not a
	// change) -- empty output from "network ls" means it does not exist.
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")

	opts := baseOptions(t)
	opts.DryRun = true
	opts.GitHubCredentialName = "bot"
	opts.GitHubToken = "ghp_x"
	opts.GitHubRepos = []string{"acme/widgets"}

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	mutating := []string{
		"apt-get", "sh", "setcap", "systemctl daemon-reload", "systemctl enable", "ln", "rm", "tar", "go build",
	}
	for _, call := range sys.calls {
		for _, m := range mutating {
			if strings.HasPrefix(call, m) {
				t.Errorf("dry run made a mutating call: %q", call)
			}
		}
	}
	if len(sys.httpCalls) != 0 {
		t.Errorf("dry run made HTTP calls: %v", sys.httpCalls)
	}
	for _, s := range result.Steps {
		if s.Status == StepDone {
			t.Errorf("dry run produced a StepDone: %+v", s)
		}
	}
	if result.SandboxGitProxyHost != "" {
		t.Errorf("dry run should not have resolved a real gateway, got %q", result.SandboxGitProxyHost)
	}
}

func TestInitSkipsAlreadySatisfiedToolchainSteps(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	stubPreflight(sys)
	sys.on("git version 2.40.0", nil, "git", "--version")
	sys.on("go version go1.26.6 linux/amd64", nil, "go", "version")
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")

	opts := baseOptions(t)
	opts.DryRun = true
	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	for _, name := range []string{"git", "go toolchain"} {
		s, ok := stepStatus(result, name)
		if !ok {
			t.Fatalf("no step named %q", name)
		}
		if s.Status != StepSkipped {
			t.Errorf("%s: status = %s, want %s", name, s.Status, StepSkipped)
		}
	}
}

func TestInitSkipsGoToolchainStepWhenInstalledVersionIsNewer(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	stubPreflight(sys)
	sys.on("git version 2.40.0", nil, "git", "--version")
	// go.mod (via baseOptions) asks for go1.26.6; a host that already
	// has a newer toolchain satisfies that, and must not be treated as
	// missing one just because the version strings differ.
	sys.on("go version go1.27.1 linux/amd64", nil, "go", "version")
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")

	opts := baseOptions(t)
	opts.DryRun = true
	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	s, ok := stepStatus(result, "go toolchain")
	if !ok {
		t.Fatalf("no step named %q", "go toolchain")
	}
	if s.Status != StepSkipped {
		t.Errorf("go toolchain: status = %s, want %s (detail: %s)", s.Status, StepSkipped, s.Detail)
	}
}

func TestInitSkipsDependentStepsWhenAPrerequisiteFails(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	stubPreflight(sys)
	// Nothing is configured for "go version", "go build", "docker", etc,
	// so every one of them fails -- the granule CLI build in particular
	// (StepManual, since non-dry-run with no "go" configured as working
	// errors out).
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on("", nil, "docker", "network", "create", "grain-sandboxes")
	// After create, still no gateway reported: ensureSandboxNetwork
	// must refuse rather than guess, even though docker "succeeded".
	// The only response configured for "network ls" above is "does not
	// exist"; the real create path would make the second ls (after
	// create) report the network present, which this test cannot do
	// with a single static response, so the second dockerNetworkGateway
	// call sees the same "does not exist" answer and ensureSandboxNetwork
	// refuses on that, not on a read failure -- either way it must not
	// guess a gateway.

	opts := baseOptions(t)
	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	goStep, ok := stepStatus(result, "go toolchain")
	if !ok || goStep.Status != StepManual {
		t.Fatalf("go toolchain: got %+v, want StepManual (nothing configured for go version or go.dev's index)", goStep)
	}
	granuleStep, ok := stepStatus(result, "build the granule CLI")
	if !ok || granuleStep.Status != StepSkipped {
		t.Fatalf("build the granule CLI: got %+v, want StepSkipped (the go toolchain step above did not succeed)", granuleStep)
	}
	imagesStep, ok := stepStatus(result, "build the granule runtime, guest-base and guest images")
	if !ok || imagesStep.Status != StepSkipped {
		t.Fatalf("images: got %+v, want StepSkipped (granule CLI never built)", imagesStep)
	}
	daemonStep, ok := stepStatus(result, "start the daemon")
	if !ok || daemonStep.Status != StepSkipped {
		t.Fatalf("start the daemon: got %+v, want StepSkipped", daemonStep)
	}
}

// TestInitLinuxContainerReachesRunningDaemonWithoutGranuleOrImages is
// the end-to-end claim this task exists to make cheaply: on
// PlatformLinuxContainer, "build the granule CLI" and "build the
// granule runtime, guest-base and guest images" are both skipped for a
// platform-specific reason (never "the Go toolchain step above did not
// succeed" -- the Go toolchain here succeeds), no command naming
// granule or either guest image is ever run, and none of that is what
// -- in a real run -- would make this platform slower than it has to
// be: a daemon still starts, with -container-sandboxes, on the gateway
// ensureSandboxNetwork resolved.
func TestInitLinuxContainerReachesRunningDaemonWithoutGranuleOrImages(t *testing.T) {
	// Not t.Parallel(): redirects the shared systemdUnitPath package var.
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t)
	opts.Platform = PlatformLinuxContainer
	opts = opts.withDefaults()

	sys := newFakeSystem()
	sys.root = true
	sys.on("GNU Make 4.3", nil, "make", "--version")
	sys.on("10.8.2", nil, "npm", "--version")
	sys.on("git version 2.40.0", nil, "git", "--version")
	sys.on("installed", nil, "dpkg", "-s", "docker-ce")
	sys.on("grain-sandboxes", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on(`[{"Subnet":"10.241.0.0/24","Gateway":"10.241.0.1"}]`, nil,
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")
	sys.on("", fmt.Errorf("no such user"), "id", "-u", DefaultDaemonUser)
	sys.on("", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such user"), "id", "-nG", DefaultDaemonUser)
	sys.on("", nil, "usermod", "-aG", "docker", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such file"), "stat", "-c", "%U:%G", opts.DataDir)
	sys.on("", nil, "chown", "-R", DefaultDaemonUser+":"+DefaultDaemonUser, opts.DataDir)
	sys.on("go version go1.26.6 linux/amd64", nil, "go", "version")
	sys.on("", nil, "make", "build")
	sys.on("cap_net_bind_service=ep", nil, "getcap", opts.GrainBinaryPath)
	sys.on("claude "+DefaultClaudeCLIVersion, nil, "claude", "--version")
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")
	sys.onHTTPRequest(http.MethodGet, "http://"+DefaultUIAddr+"/api/config", http.StatusOK, nil, nil)

	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}

	granuleStep, ok := stepStatus(result, "build the granule CLI")
	if !ok || granuleStep.Status != StepSkipped || !strings.Contains(granuleStep.Detail, "linux-container") {
		t.Fatalf("build the granule CLI: got %+v, want StepSkipped naming -platform=linux-container", granuleStep)
	}
	imagesStep, ok := stepStatus(result, "build the granule runtime, guest-base and guest images")
	if !ok || imagesStep.Status != StepSkipped || !strings.Contains(imagesStep.Detail, "linux-container") {
		t.Fatalf("images: got %+v, want StepSkipped naming -platform=linux-container", imagesStep)
	}
	daemonStep, ok := stepStatus(result, "start the daemon")
	if !ok || daemonStep.Status != StepDone {
		t.Fatalf("start the daemon: got %+v, want StepDone -- the images this platform never built must not block it", daemonStep)
	}

	for _, call := range sys.calls {
		if strings.Contains(call, "granule") || strings.Contains(call, "Granulefile") {
			t.Errorf("Init ran %q for -platform=linux-container, which never calls granule", call)
		}
	}

	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	if !strings.Contains(string(unit), "-container-sandboxes") {
		t.Errorf("unit does not start the daemon with -container-sandboxes:\n%s", unit)
	}
}

// TestInitAdoptsRunningDaemonEvenWhenDaemonAccountDoesNotExist is the
// regression this task exists for: a daemon already running, by hand,
// as some account other than opts.DaemonUser ("eric", not the "grain"
// a fresh install would create) must still be adopted, and the three
// provisioning steps after it must reach that daemon's own -ui-addr,
// not this run's flag defaults. Before the fix,
// daemonUserReady -- and so canStart -- was false because "grain" does
// not exist on this host, which meant ensureDaemon (and so
// adoptRunningDaemon) was never even called: adopt mode was gated on
// exactly the precondition it exists to route around.
func TestInitAdoptsRunningDaemonEvenWhenDaemonAccountDoesNotExist(t *testing.T) {
	// Not t.Parallel(): redirects the shared systemdUnitPath package var.
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t)
	opts = opts.withDefaults()

	withFakeProc(t, []string{
		"/home/eric/grain", "daemon",
		"-data-dir", opts.DataDir,
		"-ui-addr", "127.0.0.1:8430",
	}, "1000", "1000", nil, nil)

	sys := newFakeSystem()
	sys.root = true
	stubPreflight(sys)
	// Deliberately no response configured for "id -u grain", "useradd",
	// "usermod" or "chown -R ... <data-dir>": adopting an already-running
	// daemon must never even attempt to create the account
	// opts.DaemonUser names, since that is not who is actually running
	// it.
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "grain-daemon.service")
	sys.onHTTPRequest(http.MethodGet, "http://127.0.0.1:8430/api/config", http.StatusOK, nil, nil)

	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}

	for _, name := range []string{
		"create the daemon account",
		"add the daemon account to the docker group",
		"give the daemon account ownership of -data-dir",
	} {
		s, ok := stepStatus(result, name)
		if !ok || s.Status != StepSkipped {
			t.Errorf("%s: got %+v, want StepSkipped -- nothing to do for a daemon already running as someone else", name, s)
		}
	}

	daemonStep, ok := stepStatus(result, "start the daemon")
	if !ok || daemonStep.Status != StepDone {
		t.Fatalf("start the daemon: got %+v, want StepDone (adopted)", daemonStep)
	}

	for _, call := range sys.calls {
		if strings.HasPrefix(call, "useradd") || strings.HasPrefix(call, "usermod") || strings.HasPrefix(call, "chown -R") {
			t.Errorf("adopting an already-running daemon tried to create or chown a fresh account: %q", call)
		}
	}

	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit adoptRunningDaemon should have written: %v", err)
	}
	if !strings.Contains(string(unit), "User=eric\n") {
		t.Errorf("unit does not carry the adopted daemon's own user:\n%s", unit)
	}

	reachStep, ok := stepStatus(result, "confirm the daemon's API is reachable")
	if !ok || reachStep.Status != StepDone {
		t.Fatalf("confirm the daemon's API is reachable: got %+v, want StepDone -- it must poll the adopted daemon's own -ui-addr (127.0.0.1:8430), not this run's default", reachStep)
	}

	var sawConfig bool
	for _, c := range sys.httpRequestCalls {
		if c.method == http.MethodGet && c.url == "http://127.0.0.1:8430/api/config" {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Fatalf("the provisioning steps never reached the adopted daemon's own -ui-addr")
	}
}

// --- ensureSandboxNetwork -------------------------------------------

func TestEnsureSandboxNetworkRefusesWhenGatewayCannotBeRead(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("", fmt.Errorf("docker: permission denied talking to the socket"),
		"docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")

	var r Result
	opts := baseOptions(t).withDefaults()
	gw := ensureSandboxNetwork(context.Background(), sys, opts, &r)
	if gw != "" {
		t.Fatalf("gateway = %q, want \"\" (a real error, not \"does not exist\")", gw)
	}
	s, ok := stepStatus(r, "sandbox docker network")
	if !ok || s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
}

func TestEnsureSandboxNetworkCreatesAndReadsGateway(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on("", nil, "docker", "network", "create", "grain-sandboxes")
	sys.on(`[{"Subnet":"172.30.0.0/16","Gateway":"172.30.0.1"}]`, nil,
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")

	// The second "network ls" (after create) needs a different canned
	// response than the first; fakeSystem is a static table, so swap it
	// out through a thin wrapper that counts calls.
	wrapped := &countingNetworkLs{fakeSystem: sys, after: "grain-sandboxes"}

	var r Result
	opts := baseOptions(t).withDefaults()
	gw := ensureSandboxNetwork(context.Background(), wrapped, opts, &r)
	if gw != "172.30.0.1" {
		t.Fatalf("gateway = %q, want 172.30.0.1", gw)
	}
	s, ok := stepStatus(r, "sandbox docker network")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
}

func TestEnsureSandboxNetworkSkipsWhenAlreadyExists(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("grain-sandboxes", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on(`[{"Subnet":"172.30.0.0/16","Gateway":"172.30.0.1"}]`, nil,
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")

	var r Result
	opts := baseOptions(t).withDefaults()
	gw := ensureSandboxNetwork(context.Background(), sys, opts, &r)
	if gw != "172.30.0.1" {
		t.Fatalf("gateway = %q, want 172.30.0.1", gw)
	}
	s, ok := stepStatus(r, "sandbox docker network")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped (the network already existed; nothing changed)", s)
	}
}

// countingNetworkLs answers the first "docker network ls" call for
// grain-sandboxes with "absent" (empty output) and every call after
// that with `after` -- simulating the network coming into existence
// between the two calls ensureSandboxNetwork makes around a create.
type countingNetworkLs struct {
	*fakeSystem
	seen  bool
	after string
}

func (c *countingNetworkLs) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	key := cmdKey(name, args...)
	if key == "docker network ls --filter name=^grain-sandboxes$ --format {{.Name}}" {
		if !c.seen {
			c.seen = true
			return "", nil
		}
		return c.after, nil
	}
	return c.fakeSystem.Run(ctx, dir, env, name, args...)
}

// --- dockerNetworkGateway ------------------------------------------------

func TestDockerNetworkGatewayReadsExistenceFromLsNotInspectErrorText(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	// Deliberately nothing configured for "docker network inspect": if
	// dockerNetworkGateway ever called it after ls reported the network
	// absent, the unconfigured fake would fail with errNotFound --
	// catching that regression regardless of what wording a real
	// inspect error would have used.
	gateway, exists, err := dockerNetworkGateway(context.Background(), sys, "grain-sandboxes")
	if err != nil {
		t.Fatalf("dockerNetworkGateway: %v", err)
	}
	if exists {
		t.Fatalf("exists = true, want false: ls reported no network named grain-sandboxes")
	}
	if gateway != "" {
		t.Fatalf("gateway = %q, want \"\"", gateway)
	}
}

func TestDockerNetworkGatewayTreatsAnyInspectErrorAsReal(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("grain-sandboxes", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	// docker 29.8.2's actual wording for a missing network -- different
	// from the older "No such network" this package used to match
	// against, which is exactly why matching inspect's error text for
	// existence was wrong. It must not matter here: once ls says the
	// network exists, any inspect error at all is real.
	sys.on("", fmt.Errorf("Error response from daemon: network grain-sandboxes not found"),
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")

	_, exists, err := dockerNetworkGateway(context.Background(), sys, "grain-sandboxes")
	if err == nil {
		t.Fatalf("want an error: ls said the network exists but inspect failed")
	}
	if !exists {
		t.Fatalf("exists = false, want true: ls said the network exists")
	}
}

// --- ensureImages ------------------------------------------------------

func TestEnsureImagesStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	// Every "docker image inspect" misses (none configured), and the
	// runtime image build itself fails -- nothing configured for
	// build-granule-image.sh, so it errors.
	opts := baseOptions(t).withDefaults()
	var r Result
	ok := ensureImages(context.Background(), sys, opts, &r)
	if ok {
		t.Fatalf("ensureImages reported success with the first build failing")
	}
	runtimeStep, _ := stepStatus(r, "granule runtime image")
	if runtimeStep.Status != StepManual {
		t.Errorf("granule runtime image: got %s, want %s", runtimeStep.Status, StepManual)
	}
	baseStep, _ := stepStatus(r, "guest-base image")
	if baseStep.Status != StepSkipped {
		t.Errorf("guest-base image: got %s, want %s", baseStep.Status, StepSkipped)
	}
	guestStep, _ := stepStatus(r, "guest image")
	if guestStep.Status != StepSkipped {
		t.Errorf("guest image: got %s, want %s", guestStep.Status, StepSkipped)
	}
}

func TestEnsureImagesSkipsWhatAlreadyExists(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	sys.on("sha256:abc", nil, "docker", "image", "inspect", opts.GranuleRuntimeImage)
	sys.on("sha256:def", nil, "docker", "image", "inspect", opts.GuestBaseImage)
	sys.on("sha256:ghi", nil, "docker", "image", "inspect", opts.GuestImage)

	var r Result
	ok := ensureImages(context.Background(), sys, opts, &r)
	if !ok {
		t.Fatalf("ensureImages reported failure when every image already exists")
	}
	for _, name := range []string{"granule runtime image", "guest-base image", "guest image"} {
		s, _ := stepStatus(r, name)
		if s.Status != StepSkipped {
			t.Errorf("%s: got %s, want %s", name, s.Status, StepSkipped)
		}
	}
}

// --- ensureDaemon -------------------------------------------------------

func withTempUnitPath(t *testing.T) string {
	t.Helper()
	old := systemdUnitPath
	path := filepath.Join(t.TempDir(), "grain-daemon.service")
	systemdUnitPath = path
	t.Cleanup(func() { systemdUnitPath = old })
	// The CLI's connection file is written beside the unit, so a test
	// that redirects one never writes the real other.
	oldEnv := cliEnvPath
	cliEnvPath = filepath.Join(filepath.Dir(path), "etc-grain", "cli.env")
	t.Cleanup(func() { cliEnvPath = oldEnv })
	return path
}

// A re-run that pins an older unit to LegacyUIAddr (or any address but
// the CLI's built-in default) writes cli.env naming it, so `grain list`
// in the operator's shell reaches it: without the file the CLI dialled
// 127.0.0.1:4242 and failed (the 10-15 install walkthrough). Rewriting
// the same content is a no-op.
func TestEnsureCLIEnvNamesTheSettledAddress(t *testing.T) {
	withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	opts.UIAddr = loopbackDialAddr(LegacyUIAddr)

	var r Result
	ensureCLIEnv(sys, opts, &r)
	if s, _ := stepStatus(r, "write the grain CLI's connection file"); s.Status != StepDone {
		t.Fatalf("first run: got %+v, want StepDone", s)
	}
	data, err := os.ReadFile(cliEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GRAIN_SERVER=http://127.0.0.1:8420\n", "GRAIN_DATA_DIR=" + opts.DataDir + "\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s is missing %q:\n%s", cliEnvPath, want, data)
		}
	}

	var again Result
	ensureCLIEnv(sys, opts, &again)
	if s, _ := stepStatus(again, "write the grain CLI's connection file"); s.Status != StepSkipped {
		t.Errorf("re-run: got %+v, want StepSkipped", s)
	}

	// A wildcard bind is dialled over loopback, as every other step does.
	opts.UIAddr = DefaultUIAddr
	if got := cliEnvContent(opts.UIAddr, opts.DataDir); !strings.Contains(got, "GRAIN_SERVER=http://127.0.0.1:4242\n") {
		t.Errorf("cliEnvContent(%q) = %q", opts.UIAddr, got)
	}
}

func TestEnsureCLIEnvDryRunAndNonRootWriteNothing(t *testing.T) {
	withTempUnitPath(t)
	for _, tc := range []struct {
		name   string
		dryRun bool
		want   StepStatus
	}{{"dry-run", true, StepWould}, {"not root", false, StepManual}} {
		sys := newFakeSystem()
		opts := baseOptions(t).withDefaults()
		opts.DryRun = tc.dryRun
		var r Result
		ensureCLIEnv(sys, opts, &r)
		if s, _ := stepStatus(r, "write the grain CLI's connection file"); s.Status != tc.want {
			t.Errorf("%s: got %+v, want %s", tc.name, s, tc.want)
		}
		if _, err := os.Stat(cliEnvPath); !os.IsNotExist(err) {
			t.Errorf("%s: %s was written: %v", tc.name, cliEnvPath, err)
		}
	}
}

// withFakeProc redirects readProcCmdline/readProcStatus (and the
// uid/gid name lookups adopting a non-root pid needs) to canned
// in-memory data instead of a real /proc and real passwd/group
// databases -- this package's own pid "4242" below is exactly as fake
// as it was before adopt mode existed, and must stay deterministic
// rather than depend on whether some real process 4242 happens to exist
// on whatever machine runs this test.
func withFakeProc(t *testing.T, argv []string, uid, gid string, uidErr, gidErr error) {
	t.Helper()
	oldCmdline, oldStatus := readProcCmdline, readProcStatus
	oldUser, oldGroup := lookupUserByUID, lookupGroupByGID
	withSamePIDNamespace(t)
	readProcCmdline = func(pid string) ([]byte, error) {
		return []byte(strings.Join(argv, "\x00") + "\x00"), nil
	}
	readProcStatus = func(pid string) ([]byte, error) {
		return []byte(fmt.Sprintf("Name:\tgrain\nUid:\t%s\t%s\t%s\t%s\nGid:\t%s\t%s\t%s\t%s\n", uid, uid, uid, uid, gid, gid, gid, gid)), nil
	}
	lookupUserByUID = func(u string) (string, error) {
		if uidErr != nil {
			return "", uidErr
		}
		return "eric", nil
	}
	lookupGroupByGID = func(g string) (string, error) {
		if gidErr != nil {
			return "", gidErr
		}
		return "eric", nil
	}
	t.Cleanup(func() {
		readProcCmdline, readProcStatus = oldCmdline, oldStatus
		lookupUserByUID, lookupGroupByGID = oldUser, oldGroup
	})
}

// withUnreadableProc redirects readProcCmdline to simulate a pid this
// process cannot actually read (gone by the time it looks, or hidden by
// hidepid) -- adoptRunningDaemon's own refusal path, deterministic the
// same way withFakeProc is.
func withUnreadableProc(t *testing.T) {
	t.Helper()
	old := readProcCmdline
	readProcCmdline = func(pid string) ([]byte, error) { return nil, fmt.Errorf("no such process") }
	t.Cleanup(func() { readProcCmdline = old })
}

// withSamePIDNamespace puts every pid in this process's own PID
// namespace, so a fake pid never depends on whether a real one of that
// number exists, in some container, on the machine running the test.
func withSamePIDNamespace(t *testing.T) {
	t.Helper()
	old := readPIDNamespace
	readPIDNamespace = func(pid string) (string, error) { return "pid:[1]", nil }
	t.Cleanup(func() { readPIDNamespace = old })
}

// TestDaemonAlreadyRunningIgnoresADaemonInAnotherPIDNamespace is the
// 10-05 walkthrough's H6: a kind node's grain pod is a real
// "/usr/local/bin/grain daemon" process the host's pgrep sees. Taken as
// this host's daemon, it kept a re-run after `systemctl stop` from
// chowning -data-dir or starting the unit.
func TestDaemonAlreadyRunningIgnoresADaemonInAnotherPIDNamespace(t *testing.T) {
	withFakeProc(t, []string{"/usr/local/bin/grain", "daemon", "-data-dir=/data"}, "1000", "1000", nil, nil)
	readPIDNamespace = func(pid string) (string, error) {
		if pid == "self" {
			return "pid:[4026531836]", nil
		}
		return "pid:[4026532522]", nil
	}
	sys := newFakeSystem()
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("70164", nil, "pgrep", "-f", "grain daemon")
	if running, desc, _ := daemonAlreadyRunning(context.Background(), sys); running {
		t.Errorf("daemonAlreadyRunning = true (%s) for a daemon in a container's PID namespace", desc)
	}
	// The same process in this namespace is the daemon.
	readPIDNamespace = func(pid string) (string, error) { return "pid:[4026531836]", nil }
	if running, _, pid := daemonAlreadyRunning(context.Background(), sys); !running || pid != "70164" {
		t.Errorf("daemonAlreadyRunning = %v, pid %q; want true, 70164 in this namespace", running, pid)
	}
}

func TestEnsureDaemonRefusesWhenSystemdUnitActiveButUnreadable(t *testing.T) {
	// Not t.Parallel(): every test in this group redirects the shared
	// systemdUnitPath package var, which would race against a sibling
	// doing the same thing concurrently.
	withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("active", nil, "systemctl", "is-active", "grain-daemon.service")
	// No grain-daemon.service on disk (systemdUnitPath is redirected to
	// an empty temp dir) and MainPID is unavailable either -- adopt mode
	// has nothing to read back, so this refuses rather than guessing.

	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
	if sys.calledAny("systemctl enable --now grain-daemon.service") || sys.calledAny("systemctl enable grain-daemon.service") {
		t.Errorf("ensureDaemon tried to start a daemon it believed was already running")
	}
}

func TestEnsureDaemonSkipsAdoptionWhenUnitAlreadyManagesIt(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	existingUnit := fmt.Sprintf("[Service]\nUser=eric\nGroup=eric\nExecStart=/home/eric/grain -data-dir %s -ui-addr 127.0.0.1:8430\n", opts.DataDir)
	if err := os.WriteFile(unitPath, []byte(existingUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem()
	sys.root = true
	sys.on("active", nil, "systemctl", "is-active", "grain-daemon.service")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if sys.calledAny("systemctl daemon-reload") {
		t.Errorf("ensureDaemon rewrote a unit that already matched")
	}
}

// TestEnsureDaemonAdoptsHandStartedProcess is the grain-sbx-probe
// scenario this task exists for: a daemon started by hand, as a
// non-root account, with no systemd unit at all -- found only by
// pgrep. Adopt mode must write a unit matching exactly what is running
// (not grain init's own flag defaults) and enable it without restarting
// the process already up.
func TestEnsureDaemonAdoptsHandStartedProcess(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	withFakeProc(t, []string{
		"/home/eric/grain", "daemon",
		"-data-dir", opts.DataDir,
		"-ui-addr", "127.0.0.1:8430",
		"-granule-sandboxes",
	}, "1000", "1000", nil, nil)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "grain-daemon.service")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	if sys.calledAny("systemctl enable --now grain-daemon.service") {
		t.Errorf("adopting an already-running process must never restart it with enable --now")
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit adoptRunningDaemon should have written: %v", err)
	}
	unit := string(data)
	for _, want := range []string{"User=eric\n", "Group=eric\n", "ExecStart=/home/eric/grain daemon -data-dir " + opts.DataDir + " -ui-addr 127.0.0.1:8430 -granule-sandboxes"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit does not contain %q:\n%s", want, unit)
		}
	}
}

// TestEnsureDaemonAdoptsTheDaemonNotItsSudoWrapper is the 10-05
// walkthrough's H4: `sudo -u grain grain daemon …` is two processes that
// both match `pgrep -f "grain daemon"`, sudo with the lower pid. Adopting
// the first match wrote ExecStart=sudo -u grain … with User= the invoking
// user. The unit must be the daemon's own argv and owner.
func TestEnsureDaemonAdoptsTheDaemonNotItsSudoWrapper(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	daemonArgv := []string{"/opt/grain/bin/grain", "daemon", "-data-dir", opts.DataDir, "-ui-addr", "127.0.0.1:8450", "-poll-interval", "7s", "-granule-sandboxes"}
	withFakeProc(t, nil, "997", "996", nil, nil)
	readProcCmdline = func(pid string) ([]byte, error) {
		switch pid {
		case "4241":
			return []byte(strings.Join(append([]string{"sudo", "-u", "grain"}, daemonArgv...), "\x00") + "\x00"), nil
		case "4242":
			return []byte(strings.Join(daemonArgv, "\x00") + "\x00"), nil
		}
		return nil, fmt.Errorf("no such process")
	}
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4241\n4242", nil, "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "grain-daemon.service")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone || !strings.Contains(s.Detail, "pid 4242") {
		t.Fatalf("got %+v, want StepDone adopting pid 4242", s)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the adopted unit: %v", err)
	}
	if want := "ExecStart=" + strings.Join(daemonArgv, " ") + "\n"; !strings.Contains(string(data), want) {
		t.Errorf("unit does not contain %q:\n%s", want, data)
	}
}

// TestDaemonAlreadyRunningIgnoresAMatchThatIsNotADaemon: a readable
// process whose command line only mentions "grain daemon" (a shell, an
// editor) is not a running daemon to adopt.
func TestDaemonAlreadyRunningIgnoresAMatchThatIsNotADaemon(t *testing.T) {
	withFakeProc(t, []string{"sh", "-c", "tail -f /tmp/grain daemon.log"}, "1000", "1000", nil, nil)
	sys := newFakeSystem()
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("5150", nil, "pgrep", "-f", "grain daemon")
	if running, desc, pid := daemonAlreadyRunning(context.Background(), sys); running {
		t.Errorf("daemonAlreadyRunning = true (%s, pid %s) for a shell that only mentions grain daemon", desc, pid)
	}
}

// TestEnsureDaemonRefusesWhenAdoptedProcessIsUnreadable covers the
// pgrep-found-it-but-/proc-cannot-be-read case: refuse rather than fall
// back to grain init's own flag defaults.
func TestEnsureDaemonRefusesWhenAdoptedProcessIsUnreadable(t *testing.T) {
	withTempUnitPath(t)
	withUnreadableProc(t)
	withSamePIDNamespace(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")

	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
}

// TestEnsureDaemonRefusesOnDataDirConflictWhenAdopting covers this
// task's own item 4: -data-dir named for this run disagrees with what
// the already-running daemon actually shows, so this refuses rather
// than silently overriding either one.
func TestEnsureDaemonRefusesOnDataDirConflictWhenAdopting(t *testing.T) {
	withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	withFakeProc(t, []string{"/home/eric/grain", "daemon", "-data-dir", "/some/other/data-dir", "-granule-sandboxes"}, "1000", "1000", nil, nil)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
	if !strings.Contains(s.Detail, "data-dir") {
		t.Errorf("refusal detail does not explain the -data-dir conflict: %s", s.Detail)
	}
}

// TestEnsureDaemonRefusesOnPlatformConflictWhenAdopting covers the same
// item 4 for -platform: the running daemon's own sandbox-backend flag
// disagrees with this run's -platform (PlatformLinuxKVM by default).
func TestEnsureDaemonRefusesOnPlatformConflictWhenAdopting(t *testing.T) {
	withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	withFakeProc(t, []string{"/home/eric/grain", "daemon", "-data-dir", opts.DataDir, "-container-sandboxes"}, "1000", "1000", nil, nil)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
	if !strings.Contains(s.Detail, "-container-sandboxes") {
		t.Errorf("refusal detail does not explain the -platform conflict: %s", s.Detail)
	}
}

// TestEnsureDaemonAdoptsRootOwnedProcess proves a root-owned adopted
// daemon gets exactly the unit daemonUnit itself would write for one --
// no User=/Group= line at all, the same convention wantsRootDaemon
// already uses for -daemon-user=root.
func TestEnsureDaemonAdoptsRootOwnedProcess(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	withFakeProc(t, []string{"/usr/local/bin/grain", "daemon", "-data-dir", opts.DataDir, "-granule-sandboxes"}, "0", "0", nil, nil)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("4242", nil, "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "grain-daemon.service")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit adoptRunningDaemon should have written: %v", err)
	}
	unit := string(data)
	if strings.Contains(unit, "User=") || strings.Contains(unit, "Group=") {
		t.Errorf("a root-owned adopted daemon must get no User=/Group= line at all:\n%s", unit)
	}
}

func TestEnsureDaemonWritesUnitAndStartsWhenNotRunning(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, "-granule-git-proxy-host 172.30.0.1") {
		t.Errorf("unit does not carry the resolved gateway:\n%s", unit)
	}
	if !strings.Contains(unit, "--network="+DefaultSandboxNetworkName) {
		t.Errorf("unit does not join the VM container to the sandbox network:\n%s", unit)
	}
	if strings.Contains(unit, "-container-sandboxes") {
		t.Errorf("PlatformLinuxKVM's unit must not carry PlatformLinuxContainer's flag:\n%s", unit)
	}
}

// TestEnsureDaemonUnitCarriesUserAndGroupWhenSet proves daemonUnit
// writes whatever User=/Group= opts names, explicit or defaulted.
func TestEnsureDaemonUnitCarriesUserAndGroupWhenSet(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "grain"
	opts.DaemonGroup = "grain"
	opts = opts.withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, "User=grain\n") {
		t.Errorf("unit does not carry User=grain:\n%s", unit)
	}
	if !strings.Contains(unit, "Group=grain\n") {
		t.Errorf("unit does not carry Group=grain:\n%s", unit)
	}
}

// TestEnsureDaemonUnitCarriesDefaultUserAndGroup proves grain init's own
// default now runs the daemon as the unprivileged DefaultDaemonUser
// account instead of systemd's own root default.
func TestEnsureDaemonUnitCarriesDefaultUserAndGroup(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, "User="+DefaultDaemonUser+"\n") {
		t.Errorf("unit does not default to User=%s:\n%s", DefaultDaemonUser, unit)
	}
	if !strings.Contains(unit, "Group="+DefaultDaemonUser+"\n") {
		t.Errorf("unit does not default to Group=%s:\n%s", DefaultDaemonUser, unit)
	}
}

// TestEnsureDaemonUnitOmitsUserAndGroupWhenDaemonUserIsRoot proves
// -daemon-user=root opts all the way back out to systemd's own root
// default -- no User=/Group= line at all, the exact unit every run
// before DefaultDaemonUser existed wrote.
func TestEnsureDaemonUnitOmitsUserAndGroupWhenDaemonUserIsRoot(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "root"
	opts = opts.withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	unit := string(data)
	if strings.Contains(unit, "User=") || strings.Contains(unit, "Group=") {
		t.Errorf("-daemon-user=root must opt all the way back out, with no User=/Group= line at all:\n%s", unit)
	}
}

// TestEnsureDaemonContainerPlatformStartsWithContainerFlags is the one
// daemon-invocation difference this task exists to prove:
// PlatformLinuxContainer starts the daemon with -container-sandboxes
// and -container-git-proxy-host, never granule's own flags, and the
// gateway it carries is the same one ensureSandboxNetwork already
// resolved -- read, not guessed, exactly as it is for PlatformLinuxKVM.
func TestEnsureDaemonContainerPlatformStartsWithContainerFlags(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	var r Result
	opts := baseOptions(t)
	opts.Platform = PlatformLinuxContainer
	opts = opts.withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "10.241.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	unit := string(data)
	for _, want := range []string{"-container-sandboxes", "-container-sandbox-network " + DefaultSandboxNetworkName, "-container-git-proxy-host 10.241.0.1"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit does not contain %q:\n%s", want, unit)
		}
	}
	for _, mustNotHave := range []string{"-granule-sandboxes", "-granule-git-proxy-host", "-granule-ssh-user", "-granule-create-arg", "-sandbox-image"} {
		if strings.Contains(unit, mustNotHave) {
			t.Errorf("PlatformLinuxContainer's unit carries granule-only flag %q:\n%s", mustNotHave, unit)
		}
	}
}

// TestEnsureDaemonSkipsItsOwnRunningUnit is the re-run case from
// docs/test-runs/2026-10-04-grain-init-platforms.md: a second `grain
// init` with the same arguments found grain-daemon.service -- the unit
// the first run installed -- active, and refused, so every re-run exited
// initExitStepsRemain and none could ever report the deployment up.
func TestEnsureDaemonSkipsItsOwnRunningUnit(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t)
	opts.Platform = PlatformLinuxContainer
	opts = opts.withDefaults()
	if err := os.WriteFile(unitPath, []byte(daemonUnit(opts, "10.241.0.1")), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem()
	sys.root = true
	sys.on("active", nil, "systemctl", "is-active", "grain-daemon.service")

	var r Result
	if up, _ := ensureDaemon(context.Background(), sys, opts, &r, "10.241.0.1"); !up {
		t.Errorf("ensureDaemon = false for a daemon that is running; the provisioning steps would never reach it")
	}
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if r.HasManual() || r.HasRefused() {
		t.Errorf("a re-run against this command's own running daemon must leave nothing for initExitCode to report: %+v", r.Steps)
	}
	if sys.calledAny("systemctl enable --now grain-daemon.service") || sys.calledAny("systemctl daemon-reload") {
		t.Errorf("ensureDaemon touched a unit that was already running as written: %v", sys.calls)
	}
}

// TestEnsureDaemonAdoptsActiveUnitItWouldNotHaveWritten: a daemon that is
// not exactly this run's own -- here the unit on disk names a different
// sandbox-network gateway -- goes to adopt mode, which leaves a daemon
// already managed by a unit alone rather than rewriting it.
func TestEnsureDaemonAdoptsActiveUnitItWouldNotHaveWritten(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t)
	opts.Platform = PlatformLinuxContainer
	opts = opts.withDefaults()
	if err := os.WriteFile(unitPath, []byte(daemonUnit(opts, "172.30.0.1")), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem()
	sys.root = true
	sys.on("active", nil, "systemctl", "is-active", "grain-daemon.service")

	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "10.241.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepSkipped || !strings.Contains(s.Detail, "nothing to adopt") {
		t.Fatalf("got %+v, want adopt mode's StepSkipped for a daemon its unit already manages", s)
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != daemonUnit(opts, "172.30.0.1") {
		t.Errorf("the unit on disk was rewritten (err %v); adopt mode must leave it as it was", err)
	}
}

// TestEnsureDaemonAsksForRestartAfterRebuildingTheBinary: the unit is
// this run's own, but this run also replaced the binary it executes, so
// the deployment is not up on the build just made until it restarts.
func TestEnsureDaemonAsksForRestartAfterRebuildingTheBinary(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	if err := os.WriteFile(unitPath, []byte(daemonUnit(opts, "172.30.0.1")), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem()
	sys.root = true
	sys.on("active", nil, "systemctl", "is-active", "grain-daemon.service")

	r := Result{Steps: []Step{{Name: grainBinaryStepName, Status: StepDone}}}
	if up, _ := ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1"); !up {
		t.Errorf("ensureDaemon = false for a daemon that is running")
	}
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepManual || !strings.Contains(s.Command, "systemctl restart grain-daemon.service") {
		t.Fatalf("got %+v, want StepManual with the restart command", s)
	}
}

func TestEnsureDaemonIsManualWithoutRoot(t *testing.T) {
	withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = false
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")

	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok || s.Status != StepManual {
		t.Fatalf("got %+v, want StepManual", s)
	}
	if s.Command == "" {
		t.Errorf("a manual step needs the exact command (and unit) to run")
	}
}

// --- ensureDaemonAccount / ensureDaemonDockerGroup / ensureDataDirOwnership --

func TestEnsureDaemonAccountSkipsForRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "root"
	opts = opts.withDefaults()
	ok := ensureDaemonAccount(context.Background(), sys, opts, &r, false, "")
	if !ok {
		t.Fatalf("ensureDaemonAccount() = false, want true for -daemon-user=root")
	}
	s, found := stepStatus(r, "create the daemon account")
	if !found || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if len(sys.calls) != 0 {
		t.Errorf("ensureDaemonAccount made a call for -daemon-user=root: %v", sys.calls)
	}
}

func TestEnsureDaemonAccountSkipsWhenAlreadyExists(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("1234", nil, "id", "-u", DefaultDaemonUser)
	var r Result
	opts := baseOptions(t).withDefaults()
	ok := ensureDaemonAccount(context.Background(), sys, opts, &r, false, "")
	if !ok {
		t.Fatalf("ensureDaemonAccount() = false, want true")
	}
	s, found := stepStatus(r, "create the daemon account")
	if !found || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
}

func TestEnsureDaemonAccountCreatesWhenRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("", fmt.Errorf("no such user"), "id", "-u", DefaultDaemonUser)
	sys.on("", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", DefaultDaemonUser)
	var r Result
	opts := baseOptions(t).withDefaults()
	ok := ensureDaemonAccount(context.Background(), sys, opts, &r, false, "")
	if !ok {
		t.Fatalf("ensureDaemonAccount() = false, want true")
	}
	s, found := stepStatus(r, "create the daemon account")
	if !found || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
}

func TestEnsureDaemonAccountManualWithoutRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("", fmt.Errorf("no such user"), "id", "-u", DefaultDaemonUser)
	var r Result
	opts := baseOptions(t).withDefaults()
	ok := ensureDaemonAccount(context.Background(), sys, opts, &r, false, "")
	if ok {
		t.Fatalf("ensureDaemonAccount() = true, want false without root")
	}
	s, found := stepStatus(r, "create the daemon account")
	if !found || s.Status != StepManual || s.Command == "" {
		t.Fatalf("got %+v, want StepManual with a runnable command", s)
	}
}

func TestEnsureDaemonAccountSeparateGroupWhenDaemonGroupDiffers(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("", fmt.Errorf("no such user"), "id", "-u", "svc")
	sys.on("", nil, "groupadd", "--force", "--system", "custom")
	sys.on("", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--gid", "custom", "svc")
	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "svc"
	opts.DaemonGroup = "custom"
	opts = opts.withDefaults()
	ok := ensureDaemonAccount(context.Background(), sys, opts, &r, false, "")
	if !ok {
		t.Fatalf("ensureDaemonAccount() = false, want true")
	}
	s, found := stepStatus(r, "create the daemon account")
	if !found || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
}

func TestEnsureDaemonDockerGroupSkipsForRootAndMissingPrereqs(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true

	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "root"
	opts = opts.withDefaults()
	ensureDaemonDockerGroup(context.Background(), sys, opts, &r, true, true, false, "")
	s, found := stepStatus(r, "add the daemon account to the docker group")
	if !found || s.Status != StepSkipped {
		t.Fatalf("root: got %+v, want StepSkipped", s)
	}

	var r2 Result
	opts2 := baseOptions(t).withDefaults()
	ensureDaemonDockerGroup(context.Background(), sys, opts2, &r2, false, true, false, "")
	s2, found := stepStatus(r2, "add the daemon account to the docker group")
	if !found || s2.Status != StepSkipped {
		t.Fatalf("no account yet: got %+v, want StepSkipped", s2)
	}

	var r3 Result
	ensureDaemonDockerGroup(context.Background(), sys, opts2, &r3, true, false, false, "")
	s3, found := stepStatus(r3, "add the daemon account to the docker group")
	if !found || s3.Status != StepSkipped {
		t.Fatalf("docker not ready: got %+v, want StepSkipped", s3)
	}
}

func TestEnsureDaemonDockerGroupSkipsWhenAlreadyMember(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("debian docker sudo", nil, "id", "-nG", DefaultDaemonUser)
	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemonDockerGroup(context.Background(), sys, opts, &r, true, true, false, "")
	s, found := stepStatus(r, "add the daemon account to the docker group")
	if !found || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
}

func TestEnsureDaemonDockerGroupAddsWhenRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	sys.on("debian", nil, "id", "-nG", DefaultDaemonUser)
	sys.on("", nil, "usermod", "-aG", "docker", DefaultDaemonUser)
	var r Result
	opts := baseOptions(t).withDefaults()
	ensureDaemonDockerGroup(context.Background(), sys, opts, &r, true, true, false, "")
	s, found := stepStatus(r, "add the daemon account to the docker group")
	if !found || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
}

func TestEnsureDataDirOwnershipSkipsForRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	opts := baseOptions(t)
	opts.DaemonUser = "root"
	opts = opts.withDefaults()
	ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, "")
	if !ok {
		t.Fatalf("ensureDataDirOwnership() = false, want true for -daemon-user=root")
	}
	s, found := stepStatus(r, "give the daemon account ownership of -data-dir")
	if !found || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
}

func TestEnsureDataDirOwnershipChownsWhenRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	owner := DefaultDaemonUser + ":" + DefaultDaemonUser
	sys.on("someone:else", nil, "stat", "-c", "%U:%G", opts.DataDir)
	sys.on("", nil, "chown", "-R", owner, opts.DataDir)

	var r Result
	ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, "")
	if !ok {
		t.Fatalf("ensureDataDirOwnership() = false, want true")
	}
	s, found := stepStatus(r, "give the daemon account ownership of -data-dir")
	if !found || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	// ClaudeHome defaults to <DataDir>/claude -- inside DataDir, so it
	// must not get a chown step of its own; the recursive chown above
	// already covers it.
	if _, found := stepStatus(r, "give the daemon account ownership of -claude-home"); found {
		t.Errorf("a default ClaudeHome under DataDir should not get its own ownership step")
	}
}

// A -data-dir nothing has created yet (a fresh host, with
// -install-claude-cli=false so ensureClaudeCLI never made ClaudeHome
// inside it) is created and chowned, not skipped: the daemon account
// cannot create it under /var/lib, and a skip left a unit that exited on
// every restart (the 10-15 install walkthrough).
func TestEnsureDataDirOwnershipCreatesAMissingDataDir(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t)
	opts.DataDir = filepath.Join(t.TempDir(), "var", "lib", "grain")
	opts = opts.withDefaults()
	owner := DefaultDaemonUser + ":" + DefaultDaemonUser
	sys.on("root:root", nil, "stat", "-c", "%U:%G", opts.DataDir)
	sys.on("", nil, "chown", "-R", owner, opts.DataDir)

	var r Result
	if ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, ""); !ok {
		t.Fatalf("ensureDataDirOwnership() = false, want true; steps %+v", r.Steps)
	}
	fi, err := os.Stat(opts.DataDir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("-data-dir after the step: %v, %v; want a 0700 directory", fi, err)
	}
	if s, found := stepStatus(r, "create -data-dir"); !found || s.Status != StepDone {
		t.Errorf("create step: got %+v, want StepDone", s)
	}
	if s, found := stepStatus(r, "give the daemon account ownership of -data-dir"); !found || s.Status != StepDone {
		t.Errorf("ownership step: got %+v, want StepDone", s)
	}
}

func TestEnsureDataDirOwnershipNamesTheMkdirWhenNotRoot(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.DataDir = filepath.Join(t.TempDir(), "grain")
	opts = opts.withDefaults()

	var r Result
	if ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, ""); ok {
		t.Fatalf("ensureDataDirOwnership() = true without root and with no -data-dir")
	}
	s, found := stepStatus(r, "create -data-dir")
	if !found || s.Status != StepManual || !strings.Contains(s.Command, "install -d -m 0700") {
		t.Fatalf("got %+v, want StepManual naming install -d", s)
	}
	if _, err := os.Stat(opts.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("-data-dir was created without root: %v", err)
	}
}

func TestEnsureDataDirOwnershipAlsoChownsClaudeHomeOutsideDataDir(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t)
	opts.ClaudeHome = t.TempDir()
	opts = opts.withDefaults()
	owner := DefaultDaemonUser + ":" + DefaultDaemonUser
	sys.on("someone:else", nil, "stat", "-c", "%U:%G", opts.DataDir)
	sys.on("", nil, "chown", "-R", owner, opts.DataDir)
	sys.on("someone:else", nil, "stat", "-c", "%U:%G", opts.ClaudeHome)
	sys.on("", nil, "chown", "-R", owner, opts.ClaudeHome)

	var r Result
	ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, "")
	if !ok {
		t.Fatalf("ensureDataDirOwnership() = false, want true")
	}
	for _, name := range []string{
		"give the daemon account ownership of -data-dir",
		"give the daemon account ownership of -claude-home",
	} {
		s, found := stepStatus(r, name)
		if !found || s.Status != StepDone {
			t.Fatalf("%s: got %+v, want StepDone", name, s)
		}
	}
}

func TestEnsureDataDirOwnershipSkipsAlreadyOwnedPath(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	owner := DefaultDaemonUser + ":" + DefaultDaemonUser
	sys.on(owner, nil, "stat", "-c", "%U:%G", opts.DataDir)

	var r Result
	ok := ensureDataDirOwnership(context.Background(), sys, opts, &r, true, false, "")
	if !ok {
		t.Fatalf("ensureDataDirOwnership() = false, want true")
	}
	s, found := stepStatus(r, "give the daemon account ownership of -data-dir")
	if !found || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped (already owned)", s)
	}
	if sys.calledAny("chown -R " + owner + " " + opts.DataDir) {
		t.Errorf("ensureDataDirOwnership chowned an already-owned path")
	}
}

// --- ensureCredentialLadder ---------------------------------------------

func TestEnsureCredentialLadderSkipsWithNoToken(t *testing.T) {
	t.Parallel()
	var r Result
	opts := baseOptions(t).withDefaults()
	ensureCredentialLadder(context.Background(), newFakeSystem(), opts, &r)
	s, ok := stepStatus(r, "record the GitHub git connections")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
}

// seedCapture is a fakeSystem that keeps the seed file a `grain
// connections import` call was handed, which is removed once it returns.
type seedCapture struct {
	*fakeSystem
	seed string
}

func (c *seedCapture) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	if len(args) == 5 && args[0] == "connections" && args[3] == "import" {
		data, _ := os.ReadFile(args[4])
		c.seed = string(data)
	}
	return c.fakeSystem.Run(ctx, dir, env, name, args...)
}

// TestEnsureCredentialLadderSeedsConnectionsWithNoHostDefault: the token
// is written and its repositories are imported as git connections
// through `grain connections import`, never as a host default.
func TestEnsureCredentialLadderSeedsConnectionsWithNoHostDefault(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t).withDefaults()
	opts.GitHubCredentialName = "bot"
	opts.GitHubToken = "ghp_supersecret"
	opts.GitHubRepos = []string{"acme/widgets", "acme/*"}
	secretsDir := filepath.Join(opts.DataDir, "secrets", "github")
	sys := &seedCapture{fakeSystem: newFakeSystem()}
	sys.on("imported", nil, opts.GrainBinaryPath, "connections", "-data-dir", opts.DataDir, "import", filepath.Join(secretsDir, ".connections-seed.yaml"))

	var r Result
	ensureCredentialLadder(context.Background(), sys, opts, &r)
	s, ok := stepStatus(r, "record the GitHub git connections")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	tokenData, err := os.ReadFile(filepath.Join(secretsDir, "bot.token"))
	if err != nil || strings.TrimSpace(string(tokenData)) != "ghp_supersecret" {
		t.Errorf("bot.token = %q, %v; want ghp_supersecret", tokenData, err)
	}
	if !strings.Contains(sys.seed, `scope: "acme/widgets"`) || !strings.Contains(sys.seed, `scope: "acme/*"`) {
		t.Errorf("seed file is missing a repository:\n%s", sys.seed)
	}
	if strings.Contains(sys.seed, `scope: ""`) {
		t.Errorf("seed file carries a host default, which this step must never write:\n%s", sys.seed)
	}
	if _, err := os.Stat(filepath.Join(secretsDir, ".connections-seed.yaml")); !os.IsNotExist(err) {
		t.Errorf("the seed file was left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secretsDir, "credentials.json")); !os.IsNotExist(err) {
		t.Errorf("credentials.json was written: %v", err)
	}
}

// --- ensureGo ------------------------------------------------------------

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// These two pin goos/goarch to "linux"/"amd64" and call ensureGoFor
// directly rather than ensureGo: the release index they build is a
// fixture for a specific platform, and grain init only ever targets a
// Linux host anyway, so the test should say so instead of inheriting
// whatever platform happens to be running `go test`.

func TestEnsureGoRefusesOnDigestMismatch(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	// go.mod (from baseOptions) asks for 1.26.6.
	index := fmt.Sprintf(`[{"version":"go1.26.6","files":[{"filename":"go1.26.6.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"%s","kind":"archive"}]}]`,
		strings.Repeat("a", 64))
	sys.onHTTP(GoReleaseIndexURL, []byte(index), nil)
	sys.onHTTP("https://go.dev/dl/go1.26.6.linux-amd64.tar.gz", []byte("not actually go"), nil)

	var r Result
	ok, env := ensureGoFor(context.Background(), sys, opts, &r, "linux", "amd64")
	if ok {
		t.Fatalf("ensureGoFor reported success over a digest mismatch")
	}
	if env != nil {
		t.Errorf("ensureGoFor returned an env override despite refusing to install")
	}
	s, _ := stepStatus(r, "go toolchain")
	if s.Status != StepRefused {
		t.Fatalf("got %+v, want StepRefused", s)
	}
	if sys.calledAny("tar -C /usr/local -xzf " + filepath.Join(os.TempDir(), "go1.26.6.linux-amd64.tar.gz")) {
		t.Errorf("ensureGoFor extracted a tarball whose digest did not match")
	}
}

func TestEnsureGoInstallsOnDigestMatch(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.root = true
	opts := baseOptions(t).withDefaults()
	payload := []byte("a totally real go toolchain")
	index := fmt.Sprintf(`[{"version":"go1.26.6","files":[{"filename":"go1.26.6.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"%s","kind":"archive"}]}]`,
		sha256Hex(payload))
	sys.onHTTP(GoReleaseIndexURL, []byte(index), nil)
	tarballURL := "https://go.dev/dl/go1.26.6.linux-amd64.tar.gz"
	sys.onHTTP(tarballURL, payload, nil)
	sys.on("", nil, "rm", "-rf", "/usr/local/go")
	tmpFile := filepath.Join(os.TempDir(), "go1.26.6.linux-amd64.tar.gz")
	sys.on("", nil, "tar", "-C", "/usr/local", "-xzf", tmpFile)
	sys.on("", nil, "sh", "-c", `echo 'export PATH="$PATH:/usr/local/go/bin"' > /etc/profile.d/grain-go.sh`)

	var r Result
	ok, env := ensureGoFor(context.Background(), sys, opts, &r, "linux", "amd64")
	if !ok {
		s, _ := stepStatus(r, "go toolchain")
		t.Fatalf("ensureGoFor failed: %+v", s)
	}
	if len(env) != 1 || !strings.HasPrefix(env[0], "PATH=/usr/local/go/bin:") {
		t.Errorf("ensureGoFor env override = %v, want a PATH prepending /usr/local/go/bin", env)
	}
	s, _ := stepStatus(r, "go toolchain")
	if s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
}

// goArchForTest must match steps.go's own runtime.GOARCH exactly: this
// test builds the release index ensureGo will look itself up in.
func goArchForTest() string { return runtime.GOARCH }

// TestEnsureGoManualStepCarriesARunnableCommand covers the fix for a
// prose manual step: a non-root real run (not -dry-run) still has to
// come back with something an operator can paste into a root shell
// verbatim -- the exact filename and sha256 ensureGoFor itself just read
// off go.dev's release index, not a description of how to look them up.
func TestEnsureGoManualStepCarriesARunnableCommand(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	// sys.root defaults to false: this is the ordinary-user case.
	opts := baseOptions(t).withDefaults()
	payload := []byte("a totally real go toolchain")
	wantSHA := sha256Hex(payload)
	index := fmt.Sprintf(`[{"version":"go1.26.6","files":[{"filename":"go1.26.6.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"%s","kind":"archive"}]}]`,
		wantSHA)
	sys.onHTTP(GoReleaseIndexURL, []byte(index), nil)

	var r Result
	ok, env := ensureGoFor(context.Background(), sys, opts, &r, "linux", "amd64")
	if ok {
		t.Fatalf("ensureGoFor reported success with no root access")
	}
	if env != nil {
		t.Errorf("ensureGoFor returned an env override with nothing installed")
	}
	s, _ := stepStatus(r, "go toolchain")
	if s.Status != StepManual {
		t.Fatalf("got %+v, want StepManual", s)
	}
	tarballURL := "https://go.dev/dl/go1.26.6.linux-amd64.tar.gz"
	for _, want := range []string{tarballURL, wantSHA, "sha256sum -c", "tar -C /usr/local -xzf"} {
		if !strings.Contains(s.Command, want) {
			t.Errorf("Command = %q, want it to contain %q", s.Command, want)
		}
	}
	if strings.Contains(s.Command, "verify its sha256 against") {
		t.Errorf("Command = %q, still reads like a description rather than a runnable command", s.Command)
	}
}

// --- ensureDaemonReachable / waitForDaemonAPI ---------------------------

func TestEnsureDaemonReachableSkipsWhenDaemonWasNotStarted(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	opts := baseOptions(t).withDefaults()
	if up := ensureDaemonReachable(context.Background(), sys, opts, &r, false); up {
		t.Fatalf("ensureDaemonReachable reported up with attempted=false")
	}
	s, ok := stepStatus(r, "confirm the daemon's API is reachable")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if len(sys.httpRequestCalls) != 0 {
		t.Errorf("polled the API despite attempted=false: %v", sys.httpRequestCalls)
	}
}

func TestEnsureDaemonReachablePollsUntilReady(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	url := "http://127.0.0.1:4242/api/config"
	sys.onHTTPRequest("GET", url, 0, nil, fmt.Errorf("connection refused"))

	var r Result
	opts := baseOptions(t).withDefaults()
	opts.DaemonReadyTimeout = 50 * time.Millisecond
	opts.DaemonReadyPollInterval = time.Millisecond

	if up := ensureDaemonReachable(context.Background(), sys, opts, &r, true); up {
		t.Fatalf("ensureDaemonReachable reported up against a connection that always refuses")
	}
	s, ok := stepStatus(r, "confirm the daemon's API is reachable")
	if !ok || s.Status != StepManual {
		t.Fatalf("got %+v, want StepManual", s)
	}
	if s.Command == "" {
		t.Errorf("a manual step needs something an operator can run to diagnose it")
	}

	// Now the daemon answers: a fresh run finds it ready immediately.
	sys.onHTTPRequest("GET", url, 200, []byte(`{}`), nil)
	var r2 Result
	if up := ensureDaemonReachable(context.Background(), sys, opts, &r2, true); !up {
		t.Fatalf("ensureDaemonReachable did not report up once the API answered 200")
	}
	s2, _ := stepStatus(r2, "confirm the daemon's API is reachable")
	if s2.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s2)
	}
}

func TestEnsureDaemonReachableDryRunDoesNotPoll(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	opts := baseOptions(t).withDefaults()
	opts.DryRun = true
	if up := ensureDaemonReachable(context.Background(), sys, opts, &r, true); up {
		t.Fatalf("ensureDaemonReachable reported up on a dry run")
	}
	s, ok := stepStatus(r, "confirm the daemon's API is reachable")
	if !ok || s.Status != StepWould {
		t.Fatalf("got %+v, want StepWould", s)
	}
	if len(sys.httpRequestCalls) != 0 {
		t.Errorf("dry run polled the API: %v", sys.httpRequestCalls)
	}
}

// --- ensureTeamTargetRepos -----------------------------------------------

func TestEnsureTeamTargetReposSkipsWithNoReposGiven(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	opts := baseOptions(t).withDefaults()
	ensureTeamTargetRepos(context.Background(), sys, opts, &r, true)
	s, ok := stepStatus(r, "set the default team's target repos")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
}

func TestEnsureTeamTargetReposSetsWhenEmpty(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	url := "http://127.0.0.1:4242/api/deployment"
	sys.onHTTPRequest("GET", url, 200, []byte(`{"id":"default","maxWorkers":0,"targetRepos":[]}`), nil)
	sys.onHTTPRequest("PUT", url, 200, []byte(`{"id":"default","targetRepos":["acme/widgets"]}`), nil)

	var r Result
	opts := baseOptions(t).withDefaults()
	opts.GitHubRepos = []string{"acme/widgets"}
	ensureTeamTargetRepos(context.Background(), sys, opts, &r, true)

	s, ok := stepStatus(r, "set the default team's target repos")
	if !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	if len(sys.httpRequestCalls) != 2 {
		t.Fatalf("got %d HTTP requests, want 2 (GET then PUT): %v", len(sys.httpRequestCalls), sys.httpRequestCalls)
	}
	var sent struct {
		TargetRepos []string `json:"targetRepos"`
	}
	if err := json.Unmarshal(sys.httpRequestCalls[1].body, &sent); err != nil {
		t.Fatalf("unmarshaling the PUT body: %v", err)
	}
	if len(sent.TargetRepos) != 1 || sent.TargetRepos[0] != "acme/widgets" {
		t.Errorf("PUT body targetRepos = %v, want [acme/widgets]", sent.TargetRepos)
	}
}

func TestEnsureTeamTargetReposSkipsWhenAlreadySet(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	url := "http://127.0.0.1:4242/api/deployment"
	sys.onHTTPRequest("GET", url, 200, []byte(`{"id":"default","maxWorkers":0,"targetRepos":["acme/other"]}`), nil)

	var r Result
	opts := baseOptions(t).withDefaults()
	opts.GitHubRepos = []string{"acme/widgets"}
	ensureTeamTargetRepos(context.Background(), sys, opts, &r, true)

	s, ok := stepStatus(r, "set the default team's target repos")
	if !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped (already set to something else)", s)
	}
	if len(sys.httpRequestCalls) != 1 {
		t.Errorf("made a PUT despite the team already having target repos: %v", sys.httpRequestCalls)
	}
}

// --- ensureTeamGitCredential ----------------------------------------------

// TestEnsureTeamGitCredentialPointsTheDefaultTeamAtTheToken is the
// walkthrough's "team "default" has no GitHub credential": init wrote
// the ladder and set target repos, exited 0, and every task into
// -github-repo was refused, since a team no longer falls back to the
// deployment ladder (grain/task-1592).
func TestEnsureTeamGitCredentialPointsTheDefaultTeamAtTheToken(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	url := "http://127.0.0.1:4242/api/deployment"
	sys.onHTTPRequest("GET", url, 200, []byte(`{"id":"default","targetRepos":["acme/widgets"]}`), nil)
	sys.onHTTPRequest("POST", url+"/git-connections", 200, []byte(`{"id":"default","gitConnections":[{"credential":"bot","host":"github.com"}]}`), nil)

	var r Result
	opts := baseOptions(t).withDefaults()
	opts.GitHubToken = "ghp_x"
	opts.GitHubCredentialName = "bot"
	ensureTeamGitCredential(context.Background(), sys, opts, &r, true)

	if s, ok := stepStatus(r, "point the deployment at the GitHub credential"); !ok || s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	if len(sys.httpRequestCalls) != 2 || sys.httpRequestCalls[1].method != "POST" ||
		string(sys.httpRequestCalls[1].body) != `{"credential":"bot","host":"github.com"}` {
		t.Fatalf("want GET then POST git-connections {\"credential\":\"bot\",\"host\":\"github.com\"}, got %+v", sys.httpRequestCalls)
	}
}

func TestEnsureTeamGitCredentialLeavesAnExistingOneAlone(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	url := "http://127.0.0.1:4242/api/deployment"
	sys.onHTTPRequest("GET", url, 200, []byte(`{"id":"default","gitCredential":"grain-team-ZGVmYXVsdA"}`), nil)

	var r Result
	opts := baseOptions(t).withDefaults()
	opts.GitHubToken = "ghp_x"
	opts.GitHubCredentialName = "bot"
	ensureTeamGitCredential(context.Background(), sys, opts, &r, true)

	if s, ok := stepStatus(r, "point the deployment at the GitHub credential"); !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if len(sys.httpRequestCalls) != 1 {
		t.Errorf("replaced a credential the team already had: %+v", sys.httpRequestCalls)
	}
}

// A team whose legacy gitCredential was moved into its git connections
// (adding or deleting one blanks it) already has a credential: setup must
// not write a second, legacy one beside them. A team whose only
// connection is on another host still gets the token (grain/task-3079).
func TestEnsureTeamGitCredentialCountsGitConnections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		wantPUT    bool
	}{
		{name: "connections, new daemon", body: `{"id":"default","gitCredential":"","hasGitCredential":true,"gitConnections":[{"credential":"a"},{"credential":"b"}]}`},
		{name: "connections, old daemon", body: `{"id":"default","gitConnections":[{"credential":"a"}]}`},
		{name: "another host only", body: `{"id":"default","hasGitCredential":false,"gitConnections":[{"credential":"gl","host":"gitlab.example"}]}`, wantPUT: true},
		{name: "nothing", body: `{"id":"default","hasGitCredential":false}`, wantPUT: true},
		{name: "anonymous by hand", body: `{"id":"default","gitCredential":"anonymous","hasGitCredential":false}`},
		{name: "anonymous connection by hand", body: `{"id":"default","hasGitCredential":false,"gitConnections":[{"credential":"anonymous"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sys := newFakeSystem()
			url := "http://127.0.0.1:4242/api/deployment"
			sys.onHTTPRequest("GET", url, 200, []byte(tc.body), nil)
			sys.onHTTPRequest("POST", url+"/git-connections", 200, []byte(`{"id":"default","gitConnections":[{"credential":"bot","host":"github.com"}]}`), nil)

			var r Result
			opts := baseOptions(t).withDefaults()
			opts.GitHubToken = "ghp_x"
			opts.GitHubCredentialName = "bot"
			ensureTeamGitCredential(context.Background(), sys, opts, &r, true)

			want := StepSkipped
			if tc.wantPUT {
				want = StepDone
			}
			if s, ok := stepStatus(r, "point the deployment at the GitHub credential"); !ok || s.Status != want {
				t.Fatalf("got %+v, want %v", s, want)
			}
			if put := len(sys.httpRequestCalls) == 2; put != tc.wantPUT {
				t.Errorf("PUT = %v, want %v: %+v", put, tc.wantPUT, sys.httpRequestCalls)
			}
		})
	}
}

func TestEnsureTeamGitCredentialSkipsWithNoToken(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	ensureTeamGitCredential(context.Background(), sys, baseOptions(t).withDefaults(), &r, true)
	if s, ok := stepStatus(r, "point the deployment at the GitHub credential"); !ok || s.Status != StepSkipped {
		t.Fatalf("got %+v, want StepSkipped", s)
	}
	if len(sys.httpRequestCalls) != 0 {
		t.Errorf("made requests with no token to point at: %+v", sys.httpRequestCalls)
	}
}

// --- end-to-end: the claim this whole package exists to make true ------

// TestInitLeavesADeploymentThatCanBeSignedIntoAndDispatch runs the whole
// of Init over a fake host where every prerequisite is already
// satisfied, so the only steps that actually do anything are starting
// the daemon and the three provisioning steps after it -- the ones
// grain/task-6 exists because nothing exercised before: a healthy-
// looking `grain init` whose own operator could not sign in, and whose
// default team could neither be dispatched into nor actually dispatch.
// This is the test that matters: not that each step's own HTTP call
// looks right in isolation, but that after Init returns, a browser could
// sign in and a task could run.
func TestInitLeavesADeploymentThatCanBeSignedIntoAndDispatch(t *testing.T) {
	unitPath := withTempUnitPath(t)
	sys := newFakeSystem()
	sys.root = true
	stubPreflight(sys)

	opts := baseOptions(t)
	opts.GitHubRepos = []string{"acme/widgets"}
	installClaude := false
	opts.InstallClaudeCLI = &installClaude
	full := opts.withDefaults()

	// Every prerequisite reports itself already satisfied.
	sys.on("git version 2.40.0", nil, "git", "--version")
	sys.on("", nil, "dpkg", "-s", "docker-ce")
	sys.on("", fmt.Errorf("no such user"), "id", "-u", DefaultDaemonUser)
	sys.on("", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such user"), "id", "-nG", DefaultDaemonUser)
	sys.on("", nil, "usermod", "-aG", "docker", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such file"), "stat", "-c", "%U:%G", full.DataDir)
	sys.on("", nil, "chown", "-R", DefaultDaemonUser+":"+DefaultDaemonUser, full.DataDir)
	sys.on("go version go1.26.6 linux/"+goArchForTest(), nil, "go", "version")
	sys.on("grain-sandboxes", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on(`[{"Subnet":"172.30.0.0/16","Gateway":"172.30.0.1"}]`, nil,
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")
	sys.on("", nil, "docker", "image", "inspect", full.GranuleRuntimeImage)
	sys.on("", nil, "docker", "image", "inspect", full.GuestBaseImage)
	sys.on("", nil, "docker", "image", "inspect", full.GuestImage)

	// The two builds this fake host has not already done.
	sys.on("", nil, "go", "build", "-o", full.GranuleBinaryPath, "github.com/bwsalmon/granule/cmd/granule")
	sys.on("", nil, "make", "build")
	sys.on("", nil, "setcap", "cap_net_bind_service=+ep", full.GrainBinaryPath)

	// No daemon running yet; this run is the one that starts it.
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")

	// Once it is up, its own loopback API answers every one of the
	// three provisioning calls -- this is the part that was missing
	// before grain/task-6.
	configURL := "http://" + full.UIAddr + "/api/config"
	registerURL := "http://" + full.UIAddr + "/api/auth/register"
	teamURL := "http://" + full.UIAddr + "/api/deployment"
	sys.onHTTPRequest("GET", configURL, 200, []byte(`{}`), nil)
	sys.onHTTPRequest("POST", registerURL, 200, []byte(`{"authenticated":true,"user":"alice"}`), nil)
	sys.onHTTPRequest("GET", teamURL, 200, []byte(`{"id":"default","maxWorkers":0,"targetRepos":[]}`), nil)
	sys.onHTTPRequest("PUT", teamURL, 200, []byte(`{"id":"default"}`), nil)
	sys.onHTTPRequest("GET", "http://"+full.UIAddr+"/api/settings", 200, []byte(roomySettingsJSON), nil)

	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}
	if result.HasManual() {
		t.Fatalf("initFor left a manual step on a host where every prerequisite was already satisfied: %+v", result.Steps)
	}
	if result.HasRefused() {
		t.Fatalf("initFor refused a step on a host where every prerequisite was already satisfied: %+v", result.Steps)
	}

	for _, name := range []string{
		"start the daemon",
		"confirm the daemon's API is reachable",
		"set the default team's target repos",
		"check the sandbox budget can admit a run",
	} {
		s, ok := stepStatus(result, name)
		if !ok || s.Status != StepDone {
			t.Errorf("%s: got %+v, want StepDone", name, s)
		}
	}

	if !sys.calledAny("systemctl enable --now grain-daemon.service") {
		t.Errorf("Init never actually started the daemon")
	}
	if _, err := os.Stat(unitPath); err != nil {
		t.Errorf("Init did not write the systemd unit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(full.DataDir, "admin-password")); err == nil {
		t.Errorf("Init wrote an admin password, which grain no longer has any use for")
	}

	var sawRegister, sawTeamPUT bool
	for _, c := range sys.httpRequestCalls {
		if c.method == "POST" && c.url == registerURL {
			sawRegister = true
		}
		if c.method == "PUT" && c.url == teamURL {
			sawTeamPUT = true
		}
	}
	if sawRegister {
		t.Errorf("Init called %s, which grain no longer has", registerURL)
	}
	if !sawTeamPUT {
		t.Errorf("Init never called PUT %s -- the default team still has no target repos", teamURL)
	}
}
