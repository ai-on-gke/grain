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

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// deployment is one real setup.sh run, and everything it left behind.
//
// Built once for the package and torn down by TestMain: the run is the
// expensive part and every test here asks a different question of the same
// result, the way an operator would look over one finished deploy rather
// than doing five.
type deployment struct {
	setup               result
	missingSandboxSetup result
	unitAfterMissing    bool
	root                string
	data                string
	sandbox             string
	src                 string
	user                string
	port                int
	image               string
	sandboxImage        string
	testAgent           string
	upstream            string
	githubHost          string
	githubServer        *httptest.Server
	githubSim           *lockedGitHubSim
	// The key this deploy was handed through GRAIN_SECRETS_KEY, so the
	// tests below can verify that the host kept it rather than minting one.
	secretsKey secrets.Key
}

var deployOnce struct {
	sync.Once
	d   *deployment
	err error
}

// deployed is the fixture: the finished deploy, stood up on first use.
func deployed(t *testing.T) *deployment {
	t.Helper()
	deployOnce.Do(func() { deployOnce.d, deployOnce.err = install() })
	if deployOnce.err != nil {
		t.Fatalf("running the installer: %v", deployOnce.err)
	}
	return deployOnce.d
}

func install() (*deployment, error) {
	root, err := os.MkdirTemp("", "grain-installer-e2e-")
	if err != nil {
		return nil, err
	}
	// setup.sh's own units read these paths as $GRAIN_USER; MkdirTemp
	// makes the directory this account's alone.
	if err := os.Chmod(root, 0o755); err != nil {
		return nil, err
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}

	if _, _, ok := splitImage(image); !ok {
		return nil, fmt.Errorf("GRAIN_TEST_IMAGE=%q names no tag to deploy", image)
	}
	d := &deployment{
		root:    root,
		data:    filepath.Join(root, "data"),
		sandbox: filepath.Join(root, "sandbox"),
		src:     filepath.Join(root, "src"),
		user:    "grain-e2e",
		port:    port,
		image:   image,
	}

	checkout, err := repoRootFromCaller()
	if err != nil {
		return nil, err
	}

	// The script under test, put on this host the way a deploy puts it
	// there -- and nothing else, because nothing else is read from
	// beside it any more. setup.sh keeps no checkout of its own.
	script, err := os.ReadFile(filepath.Join(checkout, "scripts", "setup.sh"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(d.src, "scripts"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(d.src, "scripts", "setup.sh"), script, 0o755); err != nil {
		return nil, err
	}

	// Build the deterministic custom agent on the host, then keep it under
	// the data directory. setup.sh bind-mounts that directory at the same
	// absolute path in the daemon container, so the installed daemon runs
	// this exact test helper while all of its tools still cross the real MCP
	// subprocess and per-run sandbox boundary.
	if err := os.MkdirAll(d.data, 0o755); err != nil {
		return nil, err
	}
	d.testAgent = filepath.Join(d.data, "container-e2e-agent")
	buildAgent := exec.Command("go", "build", "-o", d.testAgent, "./tests/installer/testagent")
	buildAgent.Dir = checkout
	buildAgent.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildAgent.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building the installer e2e agent: %w: %s", err, out)
	}

	// A real smart-HTTP repository and GitHub API stand-in stay on the host.
	// Grain's daemon reaches it over host networking; a task container can
	// reach only Grain's credentialed git proxy on the Docker bridge.
	d.upstream = filepath.Join(root, "upstream")
	d.githubSim, d.githubServer, err = startInstallerGitHub(d.upstream)
	if err != nil {
		return nil, err
	}
	d.githubHost = strings.TrimPrefix(d.githubServer.URL, "http://")

	gateway := strings.TrimSpace(sudo("docker", "network", "inspect", "bridge",
		"-f", "{{(index .IPAM.Config 0).Gateway}}").stdout)
	if gateway == "" {
		return nil, errors.New("docker's bridge network has no gateway")
	}

	// A stand-in for the GCP minter credential push-secrets.sh pushes. Its
	// contents never matter, but its presence exercises the staging path.
	minter := filepath.Join(root, "minter-key.json")
	if err := os.WriteFile(minter,
		[]byte(`{"type": "service_account", "project_id": "fake"}`), 0o644); err != nil {
		return nil, err
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		return nil, err
	}
	d.secretsKey = key

	// First run the real setup script with an otherwise valid Grain image
	// whose embedded sandbox reference names an unreachable private image.
	// This must stop at preflight, before a daemon unit exists or a task can
	// be dispatched. Then install the real pair from this commit.
	missingImage, cleanupMissing, err := imageWithMissingSandbox(checkout, root, image)
	if err != nil {
		return nil, err
	}
	d.missingSandboxSetup = sudo(setupArgv(d, missingImage, minter, key, gateway)...)
	d.unitAfterMissing = sudo("systemctl", "cat", "grain-daemon.service").exitCode == 0
	cleanupMissing()

	d.setup = sudo(setupArgv(d, image, minter, key, gateway)...)
	if d.setup.exitCode == 0 {
		query := sudo("docker", "run", "--rm", image, "container-sandbox-image")
		if query.exitCode != 0 {
			return nil, fmt.Errorf("reading the installed image's sandbox stamp: %s", query)
		}
		d.sandboxImage = strings.TrimSpace(query.stdout)
	}
	return d, nil
}

func setupArgv(d *deployment, imageRef, minter string, key secrets.Key, gateway string) []string {
	repo, tag, ok := splitImage(imageRef)
	if !ok {
		return []string{"false"}
	}
	env := [][2]string{
		{"GRAIN_DATA_DIR", d.data},
		{"GRAIN_SANDBOX_DIR", d.sandbox},
		{"GRAIN_USER", d.user},
		{"GRAIN_IMAGE", repo},
		{"GRAIN_IMAGE_TAG", tag},
		{"GRAIN_UI_ADDR", fmt.Sprintf("127.0.0.1:%d", d.port)},
		// This is the installer path a host without /dev/kvm selects. It is
		// explicit here because GitHub's Docker runners may expose KVM; the
		// lifecycle under test requires no KVM and must remain Docker-only.
		{"GRAIN_GRANULE_ENABLE", "0"},
		{"GRAIN_CONTAINER_SANDBOXES", "1"},
		{"GRAIN_GRANULE_GIT_PROXY_HOST", gateway},
		{"GRAIN_ENABLE_UI_UPGRADE", "1"},
		// A fresh install without grain hostd: the opt-out's shape, and the
		// one whose /usr/local/bin/grain is the `docker run` wrapper every
		// `sudo grain` below goes through. zz_hostd_test.go re-installs it
		// with hostd, at the default, and switches it off again.
		{"GRAIN_HOSTD", "0"},
		// Not the default block: inside a grain sandbox its guest agent
		// already holds 127.0.0.1:9728 and its neighbours, forwarding the
		// outer deployment's git proxy and caches, and a daemon that
		// cannot bind its git proxy serves its API but dispatches nothing
		// -- every task this suite files sat queued until it timed out.
		{"GRAIN_SANDBOX_PORT_BASE", "10240"},
		{"GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE", minter},
		{"GRAIN_GCP_PROJECT", ""},
		{"GRAIN_GITHUB_HOST", d.githubHost},
		{"GRAIN_GITHUB_INSECURE_HTTP", "1"},
		{"GRAIN_GITHUB_TOKEN", "ghp_fake_token_for_the_installer_e2e"},
		{"GRAIN_SECRETS_KEY", key.String()},
		{"GRAIN_TARGET_REPO", "acme/widgets"},
		{"PATH", pathOrDefault()},
	}
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "GOPROXY", "NPM_CONFIG_REGISTRY", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	noProxy := "127.0.0.1,localhost," + strings.Split(d.githubHost, ":")[0]
	if v := os.Getenv("NO_PROXY"); v != "" {
		noProxy = v + "," + noProxy
	} else if v := os.Getenv("no_proxy"); v != "" {
		noProxy = v + "," + noProxy
	}
	env = append(env, [2]string{"NO_PROXY", noProxy}, [2]string{"no_proxy", noProxy})
	argv := []string{"env"}
	for _, pair := range env {
		argv = append(argv, pair[0]+"="+pair[1])
	}
	return append(argv, "bash", filepath.Join(d.src, "scripts", "setup.sh"))
}

func imageWithMissingSandbox(checkout, root, baseImage string) (string, func(), error) {
	const missing = "127.0.0.1:1/private/grain-sandbox:missing"
	binary := filepath.Join(root, "grain-with-missing-sandbox")
	build := exec.Command("go", "build", "-o", binary,
		"-ldflags", "-X github.com/ai-on-gke/grain/pkg/cli.defaultK8sSandboxImage="+missing, "./cmd/grain")
	build.Dir = checkout
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return "", func() {}, fmt.Errorf("building missing-sandbox Grain image: %w: %s", err, out)
	}

	name := fmt.Sprintf("grain-installer-image-%d", os.Getpid())
	tag := fmt.Sprintf("grain-installer-missing:%d", os.Getpid())
	sudo("docker", "rm", "-f", name)
	created := sudo("docker", "create", "--name", name, baseImage)
	if created.exitCode != 0 {
		return "", func() {}, fmt.Errorf("creating derivative Grain image: %s", created)
	}
	cleanup := func() {
		sudo("docker", "rm", "-f", name)
		sudo("docker", "image", "rm", "-f", tag)
		_ = os.Remove(binary)
	}
	if copied := sudo("docker", "cp", binary, name+":/usr/local/bin/grain"); copied.exitCode != 0 {
		cleanup()
		return "", func() {}, fmt.Errorf("copying the stamped Grain binary: %s", copied)
	}
	if committed := sudo("docker", "commit", name, tag); committed.exitCode != 0 {
		cleanup()
		return "", func() {}, fmt.Errorf("committing the stamped Grain image: %s", committed)
	}
	sudo("docker", "rm", "-f", name)
	return tag, cleanup, nil
}

type lockedGitHubSim struct {
	mu  sync.Mutex
	sim *githubsim.Sim
}

func (s *lockedGitHubSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.Request(method, path, headers, body)
}

func (s *lockedGitHubSim) pullRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sim.PullRequests)
}

func startInstallerGitHub(root string) (*lockedGitHubSim, *httptest.Server, error) {
	bare := filepath.Join(root, "acme", "widgets.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		return nil, nil, err
	}
	if out, err := runAt(root, "git", "init", "--bare", "--initial-branch=main", bare); err != nil {
		return nil, nil, fmt.Errorf("initializing test upstream: %w: %s", err, out)
	}
	if out, err := runAt(root, "git", "--git-dir", bare, "config", "http.receivepack", "true"); err != nil {
		return nil, nil, fmt.Errorf("enabling pushes: %w: %s", err, out)
	}
	seed := filepath.Join(root, "seed")
	for _, command := range [][]string{
		{"git", "clone", bare, seed},
		{"git", "-C", seed, "config", "user.name", "installer seed"},
		{"git", "-C", seed, "config", "user.email", "installer@example.com"},
	} {
		if out, err := runAt(root, command[0], command[1:]...); err != nil {
			return nil, nil, fmt.Errorf("seeding test upstream: %w: %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("installer e2e\n"), 0o644); err != nil {
		return nil, nil, err
	}
	for _, command := range [][]string{
		{"git", "-C", seed, "add", "README.md"},
		{"git", "-C", seed, "commit", "-m", "Initial commit"},
		{"git", "-C", seed, "push", "origin", "main"},
	} {
		if out, err := runAt(root, command[0], command[1:]...); err != nil {
			return nil, nil, fmt.Errorf("seeding test upstream: %w: %s", err, out)
		}
	}

	sim := &lockedGitHubSim{sim: githubsim.New("acme", "widgets", bare, "main")}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for key := range r.Header {
			headers[key] = r.Header.Get(key)
		}
		response, err := sim.Request(r.Method, r.URL.RequestURI(), headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for key, value := range response.Headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	})
	mux.Handle("/", gitHTTPBackend(root))
	server := httptest.NewServer(mux)
	return sim, server, nil
}

func gitHTTPBackend(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		command := exec.Command("git", "http-backend")
		command.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+root,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		command.Stdin = bytes.NewReader(body)
		out, err := command.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}
		separator := []byte("\r\n\r\n")
		headEnd := bytes.Index(out, separator)
		if headEnd < 0 {
			separator = []byte("\n\n")
			headEnd = bytes.Index(out, separator)
		}
		if headEnd < 0 {
			http.Error(w, "git http-backend returned no headers", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for _, line := range strings.Split(string(out[:headEnd]), "\n") {
			key, value, _ := strings.Cut(strings.TrimRight(line, "\r"), ":")
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
					status = code
				}
			} else if key != "" {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(out[headEnd+len(separator):])
	}
}

func runAt(dir, name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Dir = dir
	return command.CombinedOutput()
}

// The suspend policy setup.sh imposes on the host it deploys to
// (disable_host_suspend), named here so the assertion below and the
// teardown that undoes it cannot drift apart.
var sleepTargets = []string{
	"sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target",
}

const logindDropIn = "/etc/systemd/logind.conf.d/10-grain-no-suspend.conf"

// teardown puts the host back: every unit this deploy installed, the
// container it started, the wrappers it wrote, and the account it created.
func (d *deployment) teardown() {
	if d.githubServer != nil {
		d.githubServer.Close()
	}
	// Including the one thing this deploy changed about the host itself
	// rather than adding to it. A runner left unable to suspend is a
	// small thing; a runner left unable to suspend by a suite that never
	// says so is how a machine ends up in a state nobody can account for.
	sudo(append([]string{"systemctl", "unmask"}, sleepTargets...)...)
	sudo("rm", "-f", logindDropIn)
	sudo("systemctl", "reload", "systemd-logind.service")

	for _, unit := range []string{
		"grain-daemon.service",
		"grain-reboot.path", "grain-restart.path", "grain-shutdown.path",
		"grain-reboot.service", "grain-restart.service", "grain-shutdown.service",
		// Nothing installs these two any more (grain/task-193), but a
		// runner that ran this suite before then may still be carrying
		// them, and leaving an armed root trigger behind is the one
		// thing this teardown exists to prevent.
		"grain-shell.path", "grain-shell.service",
	} {
		sudo("systemctl", "disable", "--now", unit)
		sudo("rm", "-f", "/etc/systemd/system/"+unit)
	}
	sudo("systemctl", "daemon-reload")
	sudo("docker", "rm", "--force", "grain-daemon")
	// Empty now, and setup.sh's own: `rmdir` rather than `rm -rf`, so a
	// host that keeps something else under /etc/grain keeps it.
	sudo("rmdir", "/etc/grain")
	sudo("rm", "-rf", "/usr/local/lib/grain", "/usr/local/bin/grain",
		"/usr/local/bin/granule", "/etc/profile.d/grain.sh")
	sudo("userdel", d.user)
	sudo("rm", "-rf", d.root)
}

// uninstall takes the deploy back off this host the way an operator is
// told to -- `setup.sh --purge`, given the GRAIN_USER, data and sandbox
// directories and image the install was -- and says what it left behind.
// --keep-images because the image under test is this suite's input, and
// the hostmode and trixie packages that run after this one deploy it too.
//
// Run from TestMain, after every test has looked at the finished deploy,
// so the uninstall cannot pull a deployment out from under one of them.
func (d *deployment) uninstall() []string {
	repo, _, _ := splitImage(d.image)
	r := sudo("env",
		"GRAIN_DATA_DIR="+d.data,
		"GRAIN_SANDBOX_DIR="+d.sandbox,
		"GRAIN_USER="+d.user,
		"GRAIN_IMAGE="+repo,
		"PATH="+pathOrDefault(),
		"bash", filepath.Join(d.src, "scripts", "setup.sh"), "--purge", "--keep-images")
	if r.exitCode != 0 {
		return []string{r.String()}
	}

	var left []string
	for _, unit := range []string{
		"grain-daemon.service",
		"grain-reboot.path", "grain-restart.path", "grain-shutdown.path",
		"grain-reboot.service", "grain-restart.service", "grain-shutdown.service",
	} {
		if sudoTest("-e", "/etc/systemd/system/"+unit) {
			left = append(left, "unit file /etc/systemd/system/"+unit)
		}
		if state := strings.TrimSpace(sudo("systemctl", "is-active", unit).stdout); state == "active" {
			left = append(left, unit+" still active")
		}
	}
	if sudo("docker", "container", "inspect", "grain-daemon").exitCode == 0 {
		left = append(left, "the grain-daemon container")
	}
	for _, path := range []string{
		"/usr/local/bin/grain", "/usr/local/bin/granule", "/usr/local/lib/grain",
		"/etc/grain/image.env", "/etc/grain/cli.env", "/etc/profile.d/grain.sh",
		logindDropIn, d.data, d.sandbox,
	} {
		if sudoTest("-e", path) {
			left = append(left, path)
		}
	}
	for _, target := range sleepTargets {
		if state := strings.TrimSpace(sudo("systemctl", "is-enabled", target).stdout); state == "masked" {
			left = append(left, target+" still masked")
		}
	}
	if sudo("id", "-u", d.user).exitCode == 0 {
		left = append(left, "the "+d.user+" account")
	}
	if sudo("docker", "image", "inspect", d.image).exitCode != 0 {
		left = append(left, "--keep-images still removed "+d.image)
	}
	if !strings.Contains(r.stdout, "grain is uninstalled from this host.") {
		left = append(left, "no closing report in its output:\n"+r.stdout)
	}
	return left
}

func TestMain(m *testing.M) {
	code := m.Run()
	if deployOnce.d != nil {
		if left := deployOnce.d.uninstall(); len(left) > 0 {
			fmt.Fprintln(os.Stderr, "FAIL: setup.sh --purge left this behind:")
			for _, what := range left {
				fmt.Fprintln(os.Stderr, "  "+what)
			}
			code = 1
		} else {
			fmt.Println("setup.sh --purge left nothing of the deployment behind")
		}
		// Still run: it is how this host gets back to clean when the
		// uninstall above did not, and is a no-op when it did.
		deployOnce.d.teardown()
	}
	os.Exit(code)
}

// The whole point: it has to *finish*.
//
// Every failure this file exists for looked like a partial deploy -- the
// script exiting mid-way under `set -e`, leaving a host with some of a
// deployment on it and no service. The exit code is the assertion; the
// output is what makes a failure readable without a re-run.
func TestSetupRunsToCompletion(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	if d.setup.exitCode != 0 {
		t.Fatalf("setup.sh exited %d\n--- last 4000 chars of stdout ---\n%s\n"+
			"--- last 2000 chars of stderr ---\n%s",
			d.setup.exitCode, tail(d.setup.stdout, 4000), tail(d.setup.stderr, 2000))
	}
}

func TestSetupRejectsAMissingPrivateSandboxImageBeforeStartup(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	if d.missingSandboxSetup.exitCode == 0 {
		t.Fatalf("setup.sh accepted a Grain image whose sandbox image was missing:\n%s", d.missingSandboxSetup)
	}
	combined := d.missingSandboxSetup.stdout + d.missingSandboxSetup.stderr
	for _, want := range []string{
		"Pulling local Docker sandbox image 127.0.0.1:1/private/grain-sandbox:missing",
		"could not pull local Docker sandbox image",
		"private package needs GRAIN_IMAGE_PULL_USER/GRAIN_IMAGE_PULL_TOKEN",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("missing-image setup output does not contain %q:\n%s", want, combined)
		}
	}
	if d.unitAfterMissing {
		t.Error("grain-daemon.service existed after the sandbox image preflight failed")
	}
	if strings.Contains(combined, "Writing grain-daemon.service") {
		t.Error("setup reached service installation after its sandbox image preflight failed")
	}
}

func TestInstallerSelectsAndPrePullsTheVersionedContainerSandbox(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)
	if d.setup.exitCode != 0 {
		t.Fatalf("successful setup prerequisite failed: %s", d.setup)
	}

	unit := sudo("systemctl", "cat", "grain-daemon.service").stdout
	if !strings.Contains(unit, "-container-sandboxes") {
		t.Fatalf("installed unit did not select the local Docker backend:\n%s", unit)
	}
	if strings.Contains(unit, "-granule-sandboxes") {
		t.Fatalf("installed unit selected another sandbox backend as well:\n%s", unit)
	}
	if d.sandboxImage == "" {
		t.Fatal("installed Grain image has an empty local Docker sandbox stamp")
	}
	if inspected := sudo("docker", "image", "inspect", d.sandboxImage); inspected.exitCode != 0 {
		t.Fatalf("versioned sandbox image was not present before dispatch: %s", inspected)
	}
	// Settled before the first dispatch, which is the claim -- by a pull,
	// or by finding it already on this host. CI builds the pair here and
	// hands the tag to `make image`, so a run of this suite takes the
	// second branch and an operator's host takes the first; what would be
	// wrong either way is setup naming some other image, or naming none.
	out := d.setup.stdout + d.setup.stderr
	if !strings.Contains(out, "Pulling local Docker sandbox image "+d.sandboxImage) &&
		!strings.Contains(out, "Local Docker sandbox image "+d.sandboxImage+" is already present") {
		t.Fatalf("setup did not settle the image this Grain build selected (%s) before dispatch:\n%s",
			d.sandboxImage, out)
	}
}

func TestInstalledContainerBackendRunsAndCleansUpACompleteTask(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)
	if d.setup.exitCode != 0 {
		t.Fatalf("successful setup prerequisite failed: %s", d.setup)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := ui.NewHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", d.port))
	client.Token = d.accessCode(t)
	approved, autoMerge := false, false
	backends := []model.AgentBackend{{
		ID: "installer-container-e2e", Name: "installer container e2e",
		Framework: model.AgentFrameworkCustom, Command: d.testAgent,
		// A custom command can't be asked what it serves, and one with an
		// empty list has no models -- its tasks wait for one to be set.
		Models: []string{"installer-test-model"},
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		ApprovedByDefault:  &approved,
		AutoMergeByDefault: &autoMerge,
		AgentBackends:      &backends,
	}); err != nil {
		t.Fatalf("configuring the deterministic custom backend: %v", err)
	}
	// Deliberately no credential for this backend. A custom command may
	// authenticate some other way or not at all, and this one -- this
	// suite's own binary -- has nothing to authenticate to, so the
	// dispatch below is also what proves a backend with no model proxy
	// token still runs.

	hostMarker := filepath.Join("/tmp", fmt.Sprintf("grain-host-marker-%d", os.Getpid()))
	signal := filepath.Join(d.data, "container-e2e.ready")
	gate := filepath.Join(d.data, "container-e2e.continue")
	_ = os.Remove(hostMarker)
	sudo("rm", "-f", signal, gate)
	if err := os.WriteFile(hostMarker, []byte("this file belongs to the host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hostMarker)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:          "installer local Docker lifecycle",
		Repo:           "acme/widgets",
		AgentFramework: model.AgentFrameworkCustom,
	})
	if err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	// A freshly installed deployment names its task branches after
	// itself without anyone configuring a template (grain/task-3091):
	// grain/<8 hex digits of its instance id>/task-<id>-<slug of its title>
	// (model.DefaultTaskBranchTemplate, #4031).
	branch := task.Branch
	suffix := task.ID + "-" + model.BranchSlug(task.Title)
	if want := regexp.MustCompile(`^grain/[0-9a-f]{8}/task-` + regexp.QuoteMeta(suffix) + `$`); !want.MatchString(branch) {
		t.Fatalf("a fresh deployment named task %s's branch %q, want grain/<deployment>/task-%s", task.ID, branch, suffix)
	}
	description := strings.Join([]string{
		"branch=" + branch,
		"repo=acme/widgets",
		"host_marker=" + hostMarker,
		"signal=" + signal,
		"gate=" + gate,
	}, " ")
	if _, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Description: &description}); err != nil {
		t.Fatalf("adding the deterministic task instructions: %v", err)
	}

	before := dockerSandboxIDs(t)
	defer func() {
		// What the daemon made of the task: without it, an agent that
		// never signals is a timeout with nothing to say for itself.
		if t.Failed() {
			t.Logf("grain-daemon.service's journal:\n%s",
				tail(sudo("journalctl", "-u", "grain-daemon.service", "-n", "200", "--no-pager").stdout, 16000))
		}
	}()
	if err := client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving the task: %v", err)
	}
	waitFor(t, "the custom agent to finish checkout and its MCP write", 90*time.Second, func() error {
		if !sudoTest("-f", signal) {
			return errors.New("the agent has not signalled readiness")
		}
		return nil
	})

	during := dockerSandboxIDs(t)
	var runContainer string
	for id := range during {
		if !before[id] {
			if runContainer != "" {
				t.Fatalf("more than one per-run sandbox appeared: %v", during)
			}
			runContainer = id
		}
	}
	if runContainer == "" {
		t.Fatalf("no labeled per-run Docker container appeared; before=%v during=%v", before, during)
	}
	status, err := deploymentConfig(ctx, d.port, d.accessCode(t))
	if err != nil {
		t.Fatal(err)
	}
	if status.SandboxCount != 1 {
		t.Fatalf("deployment reported sandboxCount=%d during the run, want 1", status.SandboxCount)
	}
	if got, err := os.ReadFile(hostMarker); err != nil || string(got) != "this file belongs to the host\n" {
		t.Fatalf("host marker changed while sandbox commands ran: %q, %v", got, err)
	}

	if continued := sudo("touch", gate); continued.exitCode != 0 {
		t.Fatalf("releasing the deterministic agent: %s", continued)
	}
	var attempt ui.Attempt
	waitFor(t, "the task attempt to finish", 90*time.Second, func() error {
		detail, err := client.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if len(detail.Attempts) == 0 || detail.Attempts[len(detail.Attempts)-1].FinishedAt == nil {
			return errors.New("the task attempt is still live")
		}
		attempt = detail.Attempts[len(detail.Attempts)-1]
		return nil
	})
	if attempt.Outcome != "succeeded" {
		t.Fatalf("task attempt ended %q (%s), want succeeded", attempt.Outcome, attempt.Detail)
	}

	bare := filepath.Join(d.upstream, "acme", "widgets.git")
	if out, err := runAt(d.root, "git", "--git-dir", bare, "show-ref", "--verify", "refs/heads/"+branch); err != nil {
		t.Fatalf("task branch was not pushed: %v: %s", err, out)
	}
	mcpFile, err := runAt(d.root, "git", "--git-dir", bare, "show", branch+":MCP_PROBE.md")
	if err != nil || string(mcpFile) != "written through the run's MCP filesystem tool\n" {
		t.Fatalf("MCP-backed file at the pushed tip = %q, %v", mcpFile, err)
	}
	sandboxHost, err := runAt(d.root, "git", "--git-dir", bare, "show", branch+":.sandbox-hostname")
	if err != nil {
		t.Fatalf("reading the sandbox hostname from the pushed tip: %v: %s", err, sandboxHost)
	}
	hostName, _ := os.Hostname()
	if got := strings.TrimSpace(string(sandboxHost)); got == "" || got == hostName {
		t.Fatalf("task command recorded hostname %q, host is %q; isolation was not demonstrated", got, hostName)
	}

	waitFor(t, "the per-run container and live count to be cleaned up", 60*time.Second, func() error {
		now := dockerSandboxIDs(t)
		for id := range now {
			if !before[id] {
				return fmt.Errorf("sandbox container %s still exists", id)
			}
		}
		status, err := deploymentConfig(ctx, d.port, d.accessCode(t))
		if err != nil {
			return err
		}
		if status.SandboxCount != 0 {
			return fmt.Errorf("deployment still reports %d live sandboxes", status.SandboxCount)
		}
		return nil
	})
	waitFor(t, "Grain to open the pushed branch's pull request", 30*time.Second, func() error {
		current, err := client.Task(ctx, task.ID)
		if err != nil {
			return err
		}
		if current.PullRequest == "" || d.githubSim.pullRequestCount() == 0 {
			return errors.New("the pushed task has no pull request yet")
		}
		return nil
	})
	if err := client.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("finishing the task after its pushed run: %v", err)
	}
	current, err := client.Task(ctx, task.ID)
	if err != nil || current.State != model.StateClosed {
		t.Fatalf("finished task state = %q, %v; want closed", current.State, err)
	}

	defaultApproved, defaultAutoMerge := true, true
	noBackends := []model.AgentBackend{}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		ApprovedByDefault:  &defaultApproved,
		AutoMergeByDefault: &defaultAutoMerge,
		AgentBackends:      &noBackends,
	}); err != nil {
		t.Fatalf("restoring deployment defaults after the lifecycle task: %v", err)
	}
}

func dockerSandboxIDs(t *testing.T) map[string]bool {
	t.Helper()
	listed := sudo("docker", "ps", "-aq", "--filter", "label=grain.sandbox=true")
	if listed.exitCode != 0 {
		t.Fatalf("listing sandbox containers: %s", listed)
	}
	out := map[string]bool{}
	for _, id := range strings.Fields(listed.stdout) {
		out[id] = true
	}
	return out
}

// accessCode is the single-tenant daemon's access code: the local API
// token it writes into its data dir, readable only by $GRAIN_USER and
// root. setup.sh installs single-tenant (docs/single-tenant-reset.md,
// stage 5), and this test process reaches the daemon through the
// container's published port rather than as one of the callers the
// daemon trusts without it.
func (d *deployment) accessCode(t *testing.T) string {
	t.Helper()
	code := strings.TrimSpace(sudoRead(ui.LocalAPITokenPath(d.data)))
	if code == "" {
		t.Fatalf("the deployment wrote no access code to %s", ui.LocalAPITokenPath(d.data))
	}
	return code
}

func deploymentConfig(ctx context.Context, port int, accessCode string) (struct {
	SandboxCount int `json:"sandboxCount"`
}, error) {
	var out struct {
		SandboxCount int `json:"sandboxCount"`
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/api/config", port), nil)
	if err != nil {
		return out, err
	}
	request.Header.Set("Authorization", "Bearer "+accessCode)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, fmt.Errorf("GET /api/config: %s", response.Status)
	}
	return out, json.NewDecoder(response.Body).Decode(&out)
}

// `systemctl is-active grain-daemon.service` -- the missing piece.
//
// This is the observation that started it: a host with the config-sync
// service, the image, and no daemon unit. Asked of systemd rather than of
// the filesystem, because a unit file that exists and will not start is
// the same outcome for an operator.
func TestTheDaemonServiceExistsAndIsRunning(t *testing.T) {
	requireInstaller(t)
	deployed(t)

	state := strings.TrimSpace(sudo("systemctl", "is-active", "grain-daemon.service").stdout)
	if state != "active" {
		journal := sudo("journalctl", "-u", "grain-daemon.service", "-n", "50", "--no-pager").stdout
		t.Fatalf("grain-daemon.service is %q:\n%s", state, journal)
	}
}

// A running unit is not the same as a working deployment.
//
// The container has to have come up with mounts it can actually use -- the
// data directory it writes its store into, a HOME that exists -- and to be
// reachable on the address the unit gave it.
func TestTheDeploymentServesItsAPI(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	base := fmt.Sprintf("http://127.0.0.1:%d", d.port)
	var actor string
	waitFor(t, "the deployment to serve "+base+"/api/config", 90*time.Second, func() error {
		request, err := http.NewRequest(http.MethodGet, base+"/api/config", nil)
		if err != nil {
			return err
		}
		code := strings.TrimSpace(sudoRead(ui.LocalAPITokenPath(d.data)))
		if code == "" {
			return fmt.Errorf("no access code in %s yet", ui.LocalAPITokenPath(d.data))
		}
		request.Header.Set("Authorization", "Bearer "+code)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()

		var config struct {
			Actor string `json:"actor"`
		}
		if err := json.NewDecoder(response.Body).Decode(&config); err != nil {
			return err
		}
		if config.Actor == "" {
			return fmt.Errorf("the deployment reports no actor")
		}
		actor = config.Actor
		return nil
	})
	t.Logf("the deployment files work as %q", actor)
}

// The exact failure, asserted on its result rather than its shape.
//
// setup.sh stages this credential under the data directory and hands the
// path to `grain secrets set`, which runs as $GRAIN_USER inside a
// container. Staged as root at 0600 -- the obvious thing for a script
// running as root to do -- that CLI cannot read it, the command fails, and
// `set -e` ends the deploy before the unit is ever written.
//
// `grain secrets list` naming it is the proof the whole path worked:
// staged readably, read inside the container, and written into a store
// that is itself owned correctly.
func TestTheMinterCredentialReachedTheSecretsDatabase(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	listed := sudo("/usr/local/bin/grain", "secrets", "-data-dir", d.data, "list")
	if !strings.Contains(listed.stdout, "gcp-key-minter") {
		t.Fatalf("the minter credential never landed:\n%v", listed)
	}
	// And the staging copy does not outlive the command that needed it.
	staged := filepath.Join(d.data, "secrets", ".minter-key.staged.json")
	if sudoTest("-e", staged) {
		t.Errorf("%s outlived the command that needed it", staged)
	}
}

// A rebuilt host has to end up holding the key it was given, not one it
// minted a moment earlier.
//
// That is the whole of GRAIN_SECRETS_KEY, and it is an ordering claim
// about setup.sh rather than a claim about writing a file: opening the
// secrets store mints a key when there is none, and this deploy opens it
// twice (seed_gcp_minter_key, and the readiness summary's own
// `grain secrets list`). Seeded after either of those, the key on disk
// would be the host's own -- and an encrypted secrets file restored
// beside it would be unreadable for the rest of the deployment's life.
func TestTheDeploymentKeptTheSecretsKeyItWasSeededWith(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	// Compared, never printed: the private half is the one value here
	// that a failure message must not put in a CI log.
	path := filepath.Join(d.data, "secrets", "secrets.key")
	if strings.TrimSpace(sudoRead(path)) != d.secretsKey.String() {
		t.Fatalf("%s does not hold the key this deploy seeded "+
			"(the host minted its own, or seeded after something else had)", path)
	}

	// And what reads it agrees, through the same containerised CLI an
	// operator would use -- which is what makes the file on disk a key
	// this deployment can actually decrypt with.
	shown := sudo("/usr/local/bin/grain", "state", "-data-dir", d.data, "key", "show")
	if !strings.Contains(shown.stdout, d.secretsKey.Public()) {
		t.Fatalf("`grain state key show` does not report the seeded key:\n%v", shown)
	}
}

// The deploy initializes the controller-local config repository without
// requiring an external repository URL or a trip through the UI.
func TestTheDeployCreatedItsLocalConfigRepository(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	gitDir := filepath.Join(d.data, "state-repo", ".git")
	if !sudoTest("-d", gitDir) {
		t.Fatalf("%s was never initialized:\n--- last 4000 chars of the deploy ---\n%s",
			filepath.Join(d.data, "state-repo"), tail(d.setup.stdout, 4000))
	}
	account, err := user.Lookup(d.user)
	if err != nil {
		t.Fatalf("setup.sh did not create %s: %v", d.user, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		t.Fatalf("%s has uid %q: %v", d.user, account.Uid, err)
	}
	if got := sudoUID(t, gitDir); got != uid {
		t.Errorf("%s is owned by uid %d, not %s (%d)", gitDir, got, d.user, uid)
	}

	status := sudo("/usr/local/bin/grain", "state", "-data-dir", d.data, "status")
	if !strings.Contains(status.stdout, "repository: grain/config (controller local)") {
		t.Errorf("`grain state status` does not report the local config repository:\n%v", status)
	}
	// Read as root, at a repository owned by the service account -- which
	// git refuses by default, and rightly: a root git inside someone
	// else's repository obeys that repository's config. The exception is
	// named for this one path on this one command rather than written
	// into root's global config, where it would outlive the test.
	repo := filepath.Join(d.data, "state-repo")
	remote := sudo("git", "-c", "safe.directory="+repo, "-C", repo, "remote")
	if remote.exitCode != 0 {
		t.Fatalf("listing config repository remotes: %v", remote)
	}
	if strings.TrimSpace(remote.stdout) != "" {
		t.Errorf("the config repository still has a remote: %v", remote)
	}
}

// Root ran the installer; the daemon is not root.
//
// Every path the container writes through has to come out owned by
// $GRAIN_USER, or the next thing to touch it -- the daemon itself, an
// operator's `grain` -- fails on permissions. This is the general form of
// the minter-key bug above.
func TestWhatTheDeploymentWritesIsOwnedByTheServiceAccount(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	account, err := user.Lookup(d.user)
	if err != nil {
		t.Fatalf("setup.sh did not create %s: %v", d.user, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		t.Fatalf("%s has uid %q: %v", d.user, account.Uid, err)
	}

	for _, path := range []string{
		d.data,
		filepath.Join(d.data, "secrets"),
		filepath.Join(d.data, "home"),
		filepath.Join(d.data, "image.env"),
	} {
		if !sudoTest("-e", path) {
			t.Errorf("%s was never created", path)
			continue
		}
		if got := sudoUID(t, path); got != uid {
			t.Errorf("%s is owned by uid %d, not %s (%d)", path, got, d.user, uid)
		}
	}

	// The flip side, and the reason every check above needs sudo: the data
	// directory is not readable by an arbitrary account on the host.
	//
	// Only worth asking as an ordinary account. Root bypasses file
	// permissions by definition, so under `sudo go test` this would be
	// asserting something about root rather than about the deployment --
	// and failing for a reason that says nothing about setup.sh.
	if os.Geteuid() == 0 {
		t.Log("running as root, so the unreadability of the data directory says nothing here")
		return
	}
	if _, err := os.ReadDir(d.data); err == nil {
		t.Errorf("%s is readable by this account -- the store and the secrets "+
			"database are supposed to be the service account's alone", d.data)
	}
}

// The image ref file is the indirection an upgrade rewrites.
//
// The unit reads it as an EnvironmentFile, so what it names *is* what the
// deployment runs -- and setup.sh writing it is what makes a re-run with a
// different GRAIN_IMAGE_TAG a rollback.
func TestTheUnitRunsTheImageThisDeployWasGiven(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	ref := strings.TrimSpace(sudoRead(filepath.Join(d.data, "image.env")))
	if want := "GRAIN_IMAGE=" + d.image; ref != want {
		t.Fatalf("the ref file reads %q, want %q", ref, want)
	}

	unit := sudoRead("/etc/systemd/system/grain-daemon.service")
	if !strings.Contains(unit, "docker") || !strings.Contains(unit, "run --name grain-daemon") {
		t.Errorf("the unit does not run the image:\n%s", unit)
	}
	// Through a root-owned copy, never the file the service account can
	// write (#2405): the pre-step before it copies a request across.
	if want := "EnvironmentFile=/etc/grain/image.env"; !strings.Contains(unit, want) {
		t.Errorf("the unit does not read %s:\n%s", want, unit)
	}
	if strings.Contains(unit, "EnvironmentFile="+filepath.Join(d.data, "image.env")) {
		t.Errorf("the unit reads a file the service account can write:\n%s", unit)
	}
	if got := strings.TrimSpace(sudoRead("/etc/grain/image.env")); got != "GRAIN_IMAGE="+d.image {
		t.Errorf("the unit's ref file reads %q, want %q", got, "GRAIN_IMAGE="+d.image)
	}
}

// The daemon's only way to reach the host it runs on.
//
// The reboot button, the shut-down button and the Upgrade button's restart
// are each a touch of a file under the data directory; these are the units
// that turn that into a command. Enabled *and* active: a .path unit that
// exists but is not running is watching nothing.
//
// Watching is as far as this test takes them: none of the three can be
// fired on a CI runner, since two end this machine (`systemctl
// poweroff`, `systemctl reboot`) and the third takes the deployment
// under test down with it. So what they *would* run is pinned below from
// the loaded unit, tests/container proves a .path unit re-arms after
// consuming a request, and the chain from the Power tab to a host that
// is off stays proved link by link rather than end to end.
func TestTheHostControlUnitsAreInstalledAndWatching(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	// grain-shutdown.path belongs here as much as the others: its failure
	// mode is a machine that stays up and bills after an operator asked
	// for it to be off, which is the one nobody notices from the UI.
	for _, unit := range []string{
		"grain-reboot.path", "grain-shutdown.path", "grain-restart.path",
	} {
		state := strings.TrimSpace(sudo("systemctl", "is-active", unit).stdout)
		if state != "active" {
			t.Errorf("%s is %q, so nothing is watching for requests", unit, state)
		}
	}

	// And what each one would do if it did fire, asked of systemd rather
	// than of setup.sh: `systemctl cat` reports the unit as loaded, so a
	// deployment whose shutdown unit reboots instead -- or whose restart
	// unit takes the host with it -- fails here rather than the first time
	// an operator presses the button.
	for _, want := range []struct{ unit, command string }{
		{"grain-reboot.service", "ExecStart=/usr/bin/systemctl reboot"},
		{"grain-shutdown.service", "ExecStart=/usr/bin/systemctl poweroff"},
		{"grain-restart.service", "ExecStart=/usr/bin/systemctl restart grain-daemon.service"},
	} {
		loaded := sudo("systemctl", "cat", want.unit).stdout
		if !strings.Contains(loaded, want.command) {
			t.Errorf("%s does not run %q:\n%s", want.unit, want.command, loaded)
		}
	}

	control := filepath.Join(d.data, "control")
	if !sudoTest("-d", control) {
		t.Errorf("%s was never created", control)
	}
}

// The root shell is not on this host (grain/task-193).
//
// grain-shell.path used to watch the control directory and run whatever
// the daemon wrote there as root, for a tab in the UI (grain/task-13).
// It is gone end to end, and this is the half of that nothing else can
// see: not "the script no longer writes the unit" (tests/deploy checks
// that) but "systemd is not watching that file on a freshly deployed
// host". A .path unit left armed is a trigger for root with nothing left
// that is meant to pull it.
func TestTheRootShellIsNotInstalled(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	for _, unit := range []string{"grain-shell.path", "grain-shell.service"} {
		if state := strings.TrimSpace(sudo("systemctl", "is-active", unit).stdout); state == "active" {
			t.Errorf("%s is active: something is still watching for root shell requests", unit)
		}
		if sudoTest("-e", "/etc/systemd/system/"+unit) {
			t.Errorf("/etc/systemd/system/%s is still installed", unit)
		}
	}
	if sudoTest("-e", "/usr/local/lib/grain/root-shell.sh") {
		t.Error("the root shell responder script is still installed")
	}

	// The control directory keeps its other three request files and
	// nothing of this one -- setup.sh removes a stale exchange along
	// with the units.
	control := filepath.Join(d.data, "control")
	for _, name := range []string{"shell", "shell.out", "shell.status"} {
		if sudoTest("-e", filepath.Join(control, name)) {
			t.Errorf("%s is still in the control directory", name)
		}
	}
}

// The deployed host will not suspend under the task it was given.
//
// Asked of systemd, not of the script: "the script contains a mask
// command" and "this machine cannot reach sleep.target" are different
// claims, and only the second one is the reason the step exists. A host
// that suspends mid-task takes the daemon's clock, its UI and whatever
// the agent had open down with it, and says nothing about it afterwards.
func TestTheDeployedHostCannotSuspend(t *testing.T) {
	requireInstaller(t)
	deployed(t)

	// is-enabled exits non-zero for a masked unit, which is why this
	// reads the state word rather than the exit code (`run` judges
	// neither for us).
	for _, target := range sleepTargets {
		state := strings.TrimSpace(sudo("systemctl", "is-enabled", target).stdout)
		if state != "masked" {
			t.Errorf("%s is %q, so this host can still put itself to sleep under a running task", target, state)
		}
	}
	if !sudoTest("-f", logindDropIn) {
		t.Errorf("%s was never written, so a closed lid still asks for a suspend on every event", logindDropIn)
	}
}

// `grain list` on the host, against the daemon on the host.
//
// The wrapper bakes in GRAIN_SERVER from the unit's own -ui-addr, so this
// exercises the one thing an operator does first -- and would catch a
// wrapper pointed at cmd/grain's 4242 default instead.
func TestTheCLIWrapperTalksToTheDeploymentItInstalled(t *testing.T) {
	requireInstaller(t)
	deployed(t)

	listed := sudo("/usr/local/bin/grain", "-json", "list")
	if listed.exitCode != 0 {
		t.Fatalf("the wrapper does not reach the deployment:\n%v", listed)
	}

	body := strings.TrimSpace(listed.stdout)
	if body == "" {
		body = "[]"
	}
	// A fresh deployment holds exactly its standing sessions -- the
	// director, which the first cycle files for itself
	// (orchestrator.reconcileConsoleSessions, grain/task-415) -- and
	// nothing else. That is a sharper reading of "this is the deployment
	// the wrapper installed" than the empty list this used to assert:
	// the list is not empty by accident but by this deployment's own
	// first cycle, so a wrapper pointed at some other deployment's data
	// directory would be answering with that one's sessions.
	var tasks []struct {
		ConsoleSession string `json:"consoleSession"`
	}
	if err := json.Unmarshal([]byte(body), &tasks); err != nil {
		t.Fatalf("`grain -json list` printed %q: %v", body, err)
	}
	for _, task := range tasks {
		if task.ConsoleSession == "" {
			t.Fatalf("a fresh deployment already has a task nobody filed as a standing session: %s", body)
		}
	}
}

// The other two CLI subcommands an operator runs on this host, and the
// only ones that read the deployment's files rather than its API.
//
// Both take a -data-dir, and both are printed without one: setup.sh's
// closing report ends with `grain state status`. Typed exactly as
// printed, on a real deployment, that failed with "-data-dir is
// required" -- the wrapper bind-mounts the data directory but named it
// to nothing inside the container (grain/task-303). This runs them the
// way the report says to, through sudo, whose environment carries
// nothing from /etc/profile.d either: what makes them work has to be
// baked into the wrapper.
func TestTheCLIWrapperKnowsWhereThisDeploymentsStateLives(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)

	status := sudo("/usr/local/bin/grain", "state", "status")
	if status.exitCode != 0 {
		t.Errorf("`grain state status` needs a -data-dir the report never prints:\n%v", status)
	} else if !strings.Contains(status.stdout, d.data) {
		t.Errorf("`grain state status` reports on some other data directory than %s:\n%s", d.data, status.stdout)
	}
	if listed := sudo("/usr/local/bin/grain", "secrets", "list"); listed.exitCode != 0 {
		t.Errorf("`grain secrets list` needs a -data-dir:\n%v", listed)
	}

	// And a login shell gets the same answer, for the host-side commands
	// (the bootstrap playbooks, this script's own report) that spell out
	// $GRAIN_DATA_DIR.
	profile := sudoRead("/etc/profile.d/grain.sh")
	if want := `export GRAIN_DATA_DIR="` + d.data + `"`; !strings.Contains(profile, want) {
		t.Errorf("/etc/profile.d/grain.sh does not export %s:\n%s", d.data, profile)
	}
}

// --- small helpers -------------------------------------------------------

// splitImage separates the repository from the tag, which setup.sh takes
// as two variables rather than one reference.
func splitImage(ref string) (repo, tag string, ok bool) {
	at := strings.LastIndex(ref, ":")
	if at <= 0 || strings.Contains(ref[at+1:], "/") {
		return "", "", false
	}
	return ref[:at], ref[at+1:], true
}

// tail is the last n characters of s, which is how a failing deploy's
// output is quoted: the end of it is where the error is.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// pathOrDefault is the PATH the deploy runs with. It is this process's
// own, with the superuser directories appended.
//
// Appended rather than trusted, because the script under test runs as
// root and looks for `useradd`, which lives in /usr/sbin: sudo would
// have supplied that through its own secure_path, and naming a PATH here
// -- which this suite does, so the deploy sees the same toolchain and
// docker the test found -- takes it away again. On a hosted Ubuntu
// runner the two happen to agree and nothing shows; on Debian, where a
// non-root login gets no sbin directories at all, the deploy stops at
// "setup.sh: required command not found: useradd" before it has created
// anything.
func pathOrDefault() string {
	const superuser = "/usr/local/sbin:/usr/sbin:/sbin"
	if path := os.Getenv("PATH"); path != "" {
		return path + ":" + superuser
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}
