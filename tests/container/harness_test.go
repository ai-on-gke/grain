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

// Package container holds the end-to-end tests against the real
// deployment container.
//
// tests/deploy checks that the files describing the container deployment
// agree with each other. This one actually runs it: it starts the
// published image the way scripts/setup.sh's own unit starts it -- host
// networking, an unprivileged --user, the data and sandbox directories
// bind-mounted at the paths they have on the host -- and then drives the
// daemon inside through its own REST API, its CLI, and the two
// host-facing mechanisms (the reboot/restart control files and the image
// upgrade) that only exist because it is in a container at all.
//
// These are the claims a container makes that no unit test can check,
// because each one is about the boundary rather than about any code on
// either side of it: that the store survives the container it was written
// from, that files come out owned by the host account rather than by
// root, that a non-root process reaches port 80 through a file
// capability, that a request written into a mounted directory reaches a
// systemd unit outside, and that an upgrade pulls a real tag out of a
// real registry and repoints a real deployment at it -- including the
// upgrade whose tag knows a different database schema, which hands this
// deployment's rows to a container of the new image to import before
// anything cuts over (schema_upgrade_test.go).
//
// Gated, and skipped rather than failed when the gate is closed -- the
// same shape the live suites elsewhere in this repository already use for
// /dev/kvm and a GEMINI_API_KEY:
//
//	GRAIN_TEST_IMAGE  the image to test, e.g. `grain-e2e:test`. Unset
//	                  skips every test here, so `go test ./...` on a
//	                  developer's laptop stays a unit run.
//	docker            must answer `docker info`.
//	go, git           only for the schema-change upgrade tests, which
//	                  build a second image out of this checkout and read
//	                  the state repository a migration wrote
//	                  (schema_upgrade_test.go). Nothing else here builds
//	                  or clones anything.
//
// Two workflows build an image and run this against it, for two
// different reasons. .github/workflows/build-artifacts.yml does it
// before publishing anything, so a commit whose container does not come
// up cannot become a tag a deployment might pull; .github/workflows/
// tests.yml does it because that workflow's runs are the ones that
// cannot be evicted before they start, so this suite's answer for a
// merge commit exists even when the publishing run is thrown away
// (grain/task-291, and that file's container-e2e job).
package container

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// image is the deployment image under test. Empty means every test here
// skips.
var image = os.Getenv("GRAIN_TEST_IMAGE")

var dockerOnce struct {
	sync.Once
	works bool
}

// requireImage is the gate, asked at the top of every test rather than
// once for the package: a skip has to name which test skipped for the
// reason to be readable in a CI log.
func requireImage(t *testing.T) {
	t.Helper()
	if image == "" {
		t.Skip("GRAIN_TEST_IMAGE is unset; this suite needs a built image to drive")
	}
	dockerOnce.Do(func() {
		if _, err := exec.LookPath("docker"); err != nil {
			return
		}
		dockerOnce.works = exec.Command("docker", "info").Run() == nil
	})
	if !dockerOnce.works {
		t.Skip("docker does not answer `docker info`")
	}
}

// --- plumbing ----------------------------------------------------------

// docker runs a docker command and returns its stdout, failing the test
// with both streams when it exits non-zero.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := tryDocker(args...)
	if err != nil {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// tryDocker is docker for the calls whose failure is not itself a test
// failure -- inspecting a container that may have exited, or removing one
// in a cleanup that must not mask the real error.
func tryDocker(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w\n%s%s", err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

// freePort is a port nothing is listening on, for a daemon on the host
// network.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := unusedPort()
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	return port
}

// unusedPort is freePort for the registry, which is stood up once for the
// package and so has no *testing.T of its own to fail.
func unusedPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// call is one REST call against a running daemon: its status and its
// undecoded body. A transport error is returned as an error, the way the
// callers that poll for a daemon that is not up yet need it. A non-empty
// token is sent as a bearer token, the way the grain CLI sends the local
// API token.
func call(base, method, path, token string, body any, timeout time.Duration) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, raw, nil
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
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, last)
}

// --- the daemon under test ---------------------------------------------

// Daemon is a `grain daemon` running in a container, the way the unit
// runs it.
//
// The argument list mirrors scripts/setup.sh's own docker_run_args and
// write_systemd_units rather than being the smallest thing that would
// work: host networking, --user with this process's own uid:gid (the host
// account, exactly as the unit passes $GRAIN_USER's), and the
// data/sandbox directories mounted at the paths they have out here.
// Testing a container started some other way would be testing something
// no deployment runs.
type Daemon struct {
	Name    string
	Root    string
	Data    string
	Sandbox string
	Port    int
	Base    string

	uiAddr  string
	image   string
	network []string
	runArgs []string
	flags   []string
}

// options are the handful of ways a test needs a daemon to differ from
// the one the unit starts.
type options struct {
	// bindPort, when set, is the port the container binds *inside*,
	// published on Daemon.Port out here -- the privileged-port case.
	bindPort int
	// image, when set, is the image to run instead of GRAIN_TEST_IMAGE.
	// Only the schema-upgrade test sets it, and only because the thing it
	// is testing is a deployment coming back up on a *different* build
	// from the one it went down on -- which is what an upgrade is.
	image   string
	network []string
	runArgs []string
	flags   []string
}

func newDaemon(t *testing.T, root string, opts options) *Daemon {
	t.Helper()

	d := &Daemon{
		Name:    "grain-e2e-" + randomSuffix(t, 10),
		Root:    root,
		Data:    filepath.Join(root, "data"),
		Sandbox: filepath.Join(root, "sandbox"),
		Port:    freePort(t),
		image:   opts.image,
		runArgs: opts.runArgs,
		flags:   opts.flags,
	}
	if d.image == "" {
		d.image = image
	}
	d.Base = fmt.Sprintf("http://127.0.0.1:%d", d.Port)

	// The layout setup_data_dir lays out on a real host: a HOME that
	// exists (the unit exports one, and $GRAIN_USER has no home of its
	// own) and the secrets directory the daemon's own flags name.
	for _, dir := range []string{
		filepath.Join(d.Data, "home"),
		filepath.Join(d.Data, "secrets"),
		d.Sandbox,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("laying out the data directory: %v", err)
		}
	}

	d.network = opts.network
	if d.network == nil {
		d.network = []string{"--network", "host"}
	}
	if opts.bindPort != 0 {
		d.uiAddr = fmt.Sprintf("0.0.0.0:%d", opts.bindPort)
		d.network = append(d.network,
			"--publish", fmt.Sprintf("127.0.0.1:%d:%d", d.Port, opts.bindPort))
	} else {
		d.uiAddr = fmt.Sprintf("127.0.0.1:%d", d.Port)
	}
	return d
}

// start brings the container up and waits for it to serve. The cleanup is
// registered before the container is started, so a daemon that never
// serves is still removed.
func start(t *testing.T, root string, opts options) *Daemon {
	t.Helper()
	d := newDaemon(t, root, opts)
	d.run(t)
	d.awaitReady(t)
	return d
}

// run starts the container without waiting for it to serve.
func (d *Daemon) run(t *testing.T) {
	t.Helper()
	t.Cleanup(d.stop)

	args := []string{"run", "--detach", "--name", d.Name}
	args = append(args, d.network...)
	args = append(args,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--env", "HOME="+filepath.Join(d.Data, "home"),
		"--volume", d.Data+":"+d.Data,
		"--volume", d.Sandbox+":"+d.Sandbox,
	)
	args = append(args, d.runArgs...)
	args = append(args, d.image, "daemon",
		"-data-dir", d.Data,
		"-sandbox-dir", d.Sandbox,
		// No sandbox backend: nothing here dispatches (see -poll-interval
		// below), and a daemon must name one now that the host-directory
		// fallback is gone.
		"-disable-native-sandboxes",
		"-gemini-api-key-file", filepath.Join(d.Data, "secrets", "gemini-api-key"),
		"-ui-addr", d.uiAddr,
		// An hour, so the reconciler never wakes during a test. These
		// tests are about the container boundary, not about dispatch: a
		// task reaching a real agent run would need a credential and a
		// repo neither of which exists here.
		"-poll-interval", "1h",
		// A task has to name a repo or fall back to the deployment's
		// default; a deployment with neither rejects every create.
		// Nothing here ever reaches this repo -- no task is approved, so
		// none dispatches -- it just has to be a well-formed owner/name
		// the way a real deployment's own is.
		"-default-target-repo", "grain-e2e/tasks",
		// A closed port on this machine, so asking GitHub what the
		// default team's placeholder token reaches (createTask) is
		// refused at once and read as "unknown" -- which files the task
		// -- rather than reaching out to the real api.github.com.
		"-github-host", closedGitHost,
		"-github-insecure-http",
	)
	args = append(args, d.flags...)

	docker(t, args...)
}

// awaitReady blocks until the daemon answers, or says why it never will.
//
// A container that exited is reported immediately, with its logs, rather
// than polled for the full timeout: a daemon that refuses to start says
// so in one line, and waiting a minute to print it turns every such
// failure into a slow one.
func (d *Daemon) awaitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !d.running() {
			t.Fatalf("%s exited before serving %s:\n%s", d.Name, d.Base, d.logs())
		}
		status, _, err := call(d.Base, http.MethodGet, "/api/config", d.token(), nil, 5*time.Second)
		if err == nil && status == http.StatusOK {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never served %s:\n%s", d.Name, d.Base, d.logs())
}

// token is the local API token the daemon wrote to its data directory,
// or "" before it has written one. A new installation runs single-tenant,
// where every caller -- loopback included -- has to present it; a
// multi-tenant daemon (an older image, in the upgrade tests) accepts it
// from loopback too, so it is sent whichever one is running. The
// container runs as this test's own user, so the file is readable here.
func (d *Daemon) token() string {
	raw, err := os.ReadFile(filepath.Join(d.Data, "local-api-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (d *Daemon) running() bool {
	out, _ := tryDocker("inspect", "-f", "{{.State.Running}}", d.Name)
	return strings.TrimSpace(out) == "true"
}

// logs is everything the container has printed, both streams together.
//
// Both, and not through tryDocker, because `docker logs` reproduces the
// container's own streams -- and the daemon logs through the standard
// library's log package, which writes to stderr. tryDocker keeps stdout
// and folds stderr into an error it only returns on failure, so a
// `docker logs` of a perfectly healthy daemon came back empty: every
// failure message in this suite that ends "\n%s" with this in it was
// printing "<no output>" while the log that explained the failure sat
// on the stream being dropped.
func (d *Daemon) logs() string {
	out, err := exec.Command("docker", "logs", d.Name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<docker logs %s: %v>", d.Name, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return "<no output>"
	}
	return string(out)
}

func (d *Daemon) stop() {
	_, _ = tryDocker("rm", "--force", d.Name)
}

// api is one call against this daemon, failing the test on a transport
// error -- which for a daemon awaitReady has already cleared means the
// container died mid-test.
func (d *Daemon) api(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	status, raw, err := call(d.Base, method, path, d.token(), body, 30*time.Second)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", method, path, err, d.logs())
	}
	return status, raw
}

// decode is the JSON half of a call, kept separate so a test that only
// cares about the status never has to name a type.
func decode(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
}

// --- the shapes these tests read ----------------------------------------

type config struct {
	Actor         string            `json:"actor"`
	Capabilities  []json.RawMessage `json:"capabilities"`
	RebootEnabled bool              `json:"rebootEnabled"`
	// The two power actions beside it (grain/task-40), each reaching the
	// host through a control file of its own.
	ShutdownEnabled bool `json:"shutdownEnabled"`
	RestartEnabled  bool `json:"restartEnabled"`
}

type task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Comments []struct {
		Body string `json:"body"`
	} `json:"comments"`
}

type upgradeStatus struct {
	Enabled bool   `json:"enabled"`
	Phase   string `json:"phase"`
	Detail  string `json:"detail"`
	// Running is what the deployment is on now, as opposed to what the
	// last attempt tried to move it to (grain/task-50) -- the ref file's
	// own line, read back by the daemon that a moment ago was handed
	// that file's path on its command line.
	Running struct {
		Image string `json:"image"`
	} `json:"running"`
	// The plan and where each step of it got to (pkg/upgrade/progress.go).
	// What it is for here is telling one failure from another: "the
	// schema step refused these rows" and "the pull never found an
	// image" are the same Phase and very different deployments.
	Steps []upgradeStep `json:"steps"`
}

// upgradeStep is one step of that plan. Named rather than left anonymous
// so that a test can build one: the reading that tells a bound expiring
// apart from a build refusing these rows is itself worth a test, and
// that test has no upgrade to read a plan off (schema_upgrade_test.go's
// TestATimedOutContainerRunIsNotReadAsARefusedUpgrade).
type upgradeStep struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// step is where one named step of an upgrade's plan got to, and whether
// the plan had that step in it at all.
func (s upgradeStatus) step(name string) (state, detail string, ok bool) {
	for _, step := range s.Steps {
		if step.Name == name {
			return step.State, step.Detail, true
		}
	}
	return "", "", false
}

// ranOutOfTime reports the step this upgrade lost to one of pkg/upgrade's
// own bounds -- rather than to anything the test reading the status is
// asking about -- along with the failure in full.
//
// It exists because the two are the same Phase, and on the tests here
// often the same failed step, so only the Detail tells them apart. Every
// question this suite's upgrades put to a freshly built image is a whole
// `docker run` of it, bounded by imageRunTimeout (pkg/upgrade/image.go)
// or, for the migration rehearsal, by schemaCheckTimeout; and this suite
// runs where those bounds are most likely to expire -- inside
// .grain/workflows/release.yml, on a host building the very images it is
// testing. When one does, the upgrade fails before the thing under test
// has run at all, and a test that goes straight on to assert on the
// wording of a refusal reports "the failure does not say ..." for a
// refusal that never happened (grain/task-341).
//
// The two phrasings matched are the whole of what pkg/upgrade/output.go
// produces for a killed command: runError's for a command run for its
// exit status, askError's for one run for its answer. Matching text is
// the price of reading this through the daemon's HTTP API, which is the
// only place these tests can see an upgrade from.
func (s upgradeStatus) ranOutOfTime() (step, detail string, ok bool) {
	if !strings.Contains(s.Detail, "ran out of time") && !strings.Contains(s.Detail, "no answer within") {
		return "", "", false
	}
	for _, st := range s.Steps {
		if st.State == "failed" {
			return st.Name, s.Detail, true
		}
	}
	return "", s.Detail, true
}

// createTask files a task through the API and returns it.
//
// Left unapproved (the default), so it sits in `proposed` and no
// reconciler cycle ever tries to dispatch it.
//
// A task is refused at filing when the deployment has no git connection
// (grain/task-1850), so it is given one with a placeholder token first. It never authenticates anything: -github-host points at a
// closed port, so the reach check cannot list what it reaches and
// refuses nothing.
func createTask(t *testing.T, d *Daemon, title string) task {
	t.Helper()
	giveDefaultTeamACredential(t, d)
	status, raw := d.api(t, http.MethodPost, "/api/tasks", map[string]string{
		"title":       title,
		"description": "filed by the container e2e suite",
	})
	if status != http.StatusCreated {
		t.Fatalf("creating a task: %d %s", status, raw)
	}
	var created task
	decode(t, raw, &created)
	return created
}

// closedGitHost is the -github-host every daemon here starts with: a
// closed port on this machine, and so the deployment's default git host.
const closedGitHost = "127.0.0.1:1"

// giveDefaultTeamACredential gives the deployment a git connection on its
// default git host, because a task is refused at filing when nothing it
// holds reaches the task's repo there (pkg/gitproxy/reach_refusal.go,
// grain/task-1850). The connection names that host outright -- a
// credential's host record cannot be empty -- and it has to be that host:
// a connection on any other, github.com say, is never matched against the
// default host's repos (model.MatchTeamGitConnection), so it would leave
// every filing refused. Its token is a placeholder that
// never authenticates anything: -github-host points at a closed port, so
// the reach check cannot list what it reaches, reads that as "unknown"
// and refuses nothing -- so no test here asks GitHub anything. A
// deployment that already has a connection on the default host is left as
// it is.
func giveDefaultTeamACredential(t *testing.T, d *Daemon) {
	t.Helper()
	status, raw := d.api(t, http.MethodGet, "/api/deployment", nil)
	if status != http.StatusOK {
		t.Fatalf("reading the deployment: %d %s", status, raw)
	}
	var deployment struct {
		GitConnections []struct {
			Host string `json:"host"`
		} `json:"gitConnections"`
	}
	decode(t, raw, &deployment)
	for _, c := range deployment.GitConnections {
		if c.Host == "" || c.Host == closedGitHost {
			return
		}
	}
	status, raw = d.api(t, http.MethodPost, "/api/deployment/git-connections", map[string]string{
		"type": "pat", "host": closedGitHost, "value": "grain-e2e-unused",
	})
	if status != http.StatusOK {
		t.Fatalf("giving the deployment a git connection: %d %s", status, raw)
	}
}

// randomSuffix names a container uniquely enough that a re-run over a
// half-cleaned machine does not collide.
func randomSuffix(t *testing.T, n int) string {
	t.Helper()
	suffix, err := randomHex(n)
	if err != nil {
		t.Fatalf("naming a container: %v", err)
	}
	return suffix
}

// randomHex is randomSuffix for the registry, for the same reason
// unusedPort exists.
func randomHex(n int) (string, error) {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:n], nil
}

// errNothingYet is what a waitFor check returns while the thing it is
// waiting for simply has not happened yet -- distinct from the errors
// that describe something going wrong.
var errNothingYet = errors.New("not yet")

// filesUnder is every regular file in the tree at root, which is empty
// (and not an error) until the daemon has written its first one.
func filesUnder(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The store directory itself does not exist until the daemon
			// creates it, which is one of the things waitFor is waiting for.
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// ownedByUs answers the question --user exists to make true: that what
// came out of the container belongs to the host account, not to root.
func ownedByUs(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%s: no ownership information on this platform", path)
	}
	return int(stat.Uid) == os.Getuid(), nil
}

// requireSystemd is the second gate on the host half of the control
// channel: a .path unit needs a system manager to install it into, which
// a container-based CI step may not have.
func requireSystemd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("no systemctl, so there is no .path unit to install")
	}
	// Installing a unit needs root, and this package promises to skip
	// rather than fail when it cannot do what a test asks. Without this a
	// Linux laptop with docker and systemd but no passwordless sudo -- an
	// ordinary machine, not a broken one -- would sit on a password prompt
	// instead.
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			t.Skip("not root and no sudo, so no unit can be installed")
		}
		if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
			t.Skip("not root and sudo needs a password, so no unit can be installed")
		}
	}
	// The state word rather than the exit code: `is-system-running` exits 1
	// for "degraded" -- an ordinary state for a CI runner, and still a
	// systemd that runs units -- but also for "offline", which is what a
	// machine with no systemd as PID 1 says, and there this test would
	// install a unit nothing would ever act on and then time out.
	var out []byte
	if os.Geteuid() != 0 {
		out, _ = exec.Command("sudo", "systemctl", "is-system-running").Output()
	} else {
		out, _ = exec.Command("systemctl", "is-system-running").Output()
	}
	if state := strings.TrimSpace(string(out)); !systemdStates[state] {
		t.Skipf("systemd is %q, so no .path unit here would ever fire", state)
	}
}

// systemdStates are the states a systemd that actually runs units reports
// itself in.
var systemdStates = map[string]bool{
	"running": true, "degraded": true, "maintenance": true, "starting": true,
}

// writeUnit installs one unit file, staged in a temporary file this
// account can write and copied into place as root.
func writeUnit(t *testing.T, name, body string) {
	t.Helper()
	staged := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(staged, []byte(body), 0o644); err != nil {
		t.Fatalf("staging %s: %v", name, err)
	}
	sudo(t, "cp", staged, "/etc/systemd/system/"+name)
}

// sudo runs one command as root, failing the test with its output.
func sudo(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("sudo %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// touch creates the file if it is missing and updates its mtime if it is
// not -- the exact thing -reboot-cmd asks for, and what PathModified
// watches for.
func touch(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("touching %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("touching %s: %v", path, err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("touching %s: %v", path, err)
	}
}

// countLines is how many lines of text are exactly want, which is how the
// control-channel service records each time it acted.
func countLines(text, want string) int {
	var n int
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			n++
		}
	}
	return n
}

// dockerGroupArgs is what lets the container's non-root uid use the
// mounted socket.
//
// setup.sh's own docker_run_args does the same lookup, for the same
// reason: /var/run/docker.sock is root:docker 0660, so a container running
// as an ordinary uid needs the group rather than the file.
func dockerGroupArgs() []string {
	group, err := user.LookupGroup("docker")
	if err != nil {
		return nil
	}
	return []string{"--group-add", group.Gid}
}
