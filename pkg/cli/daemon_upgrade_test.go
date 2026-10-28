// The Upgrade button, from the flags a deployment is started with to the
// upgrade they actually perform.
//
// pkg/upgrade tests the pipeline and pkg/ui tests the endpoint, but both
// build their own upgrade.Config -- so neither can catch the failure that
// costs a deployment its upgrade button: startUIServer (daemon.go) not
// turning -upgrade-image/-upgrade-src-dir into an Upgrader, or building
// one out of the wrong fields. These drive run() with those flags set,
// the same function daemon() calls, and post to the real /api/upgrade
// over a real socket.
//
// docker and make are faked on PATH, the same style
// daemon_granule_wiring_test.go's own fake granule uses: what is under
// test is the wiring in this package, and the commands it ends up
// invoking, not docker's or make's behavior. tests/container/upgrade_test.go
// runs this same image path against a real registry and a real docker.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// builtBinaryScript is what the fake `make` below leaves at bin/grain: a
// binary that answers `schema-version` with the schema this build knows,
// since that is what both the health check and the schema step run
// against a newly installed one. Named here because the test asserts
// that this is what ends up installed.
var builtBinaryScript = "#!/bin/sh\necho " + strconv.Itoa(model.SchemaVersion)

// upgradeStatusBody is GET/POST /api/upgrade's wire shape -- the fields
// UpgradePanel.jsx reads.
type upgradeStatusBody struct {
	Enabled bool   `json:"enabled"`
	Branch  string `json:"branch"`
	Phase   string `json:"phase"`
	Detail  string `json:"detail"`
}

// upgradeDataDir lays out the minimum a daemon needs to come up: the
// anonymous GitHub credential every other run()-driven test here seeds.
func upgradeDataDir(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	return dataDir
}

// startDaemonForUpgrade runs the daemon in this process until the test
// ends (or until the returned stop is called, for a test that wants to
// bring a second one up over the same data directory), and hands back the
// base URL of its UI/API server once that is answering.
func startDaemonForUpgrade(t *testing.T, cfg daemon.Config) (string, func()) {
	t.Helper()
	seedTestAccessCode(t, cfg.DataDir)
	cfg.UiAddr = freeTCPAddr(t)
	cfg.MaxWorkers = 1
	cfg.PollInterval = time.Hour
	cfg.GithubHost = "127.0.0.1:0"
	cfg.GithubInsecureHTTP = true
	cfg.Actor = "tester"
	if cfg.SandboxDir == "" {
		cfg.SandboxDir = t.TempDir()
	}
	if store, db, err := daemon.OpenStore("sqlite", "", cfg.DataDir); err == nil {
		_ = store.Init(context.Background())
		_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})
		_ = db.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- daemon.Run(ctx, cfg) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-runErr:
			case <-time.After(daemonWaitLimit):
				t.Error("run() never returned after ctx was cancelled")
			}
		})
	}
	t.Cleanup(stop)

	base := "http://" + cfg.UiAddr
	client := accessCodeClient(base)
	deadline := time.Now().Add(daemonWaitLimit)
	for {
		if _, err := client.ListTasks(ctx); err == nil {
			return base, stop
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon's UI/API server never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// clickUpgradeButton posts what the Upgrade button posts and waits for
// the upgrade it starts to stop reporting "running", exactly as the
// panel's own poll of GET /api/upgrade does.
func clickUpgradeButton(t *testing.T, base, branch string) upgradeStatusBody {
	t.Helper()

	body, err := json.Marshal(map[string]string{"branch": branch})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := accessPost(base+"/api/upgrade", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/upgrade: %v", err)
	}
	started := readUpgradeStatus(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade = %d (%+v)", resp.StatusCode, started)
	}
	if started.Phase != "running" {
		t.Fatalf("the reply that disables the button says %q, want \"running\"", started.Phase)
	}

	deadline := time.Now().Add(daemonWaitLimit)
	for {
		got := getUpgradeStatus(t, base)
		if got.Phase != "running" {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatal("the upgrade never stopped reporting \"running\"")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func getUpgradeStatus(t *testing.T, base string) upgradeStatusBody {
	t.Helper()
	resp, err := accessGet(base + "/api/upgrade")
	if err != nil {
		t.Fatalf("GET /api/upgrade: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/upgrade = %d", resp.StatusCode)
	}
	return readUpgradeStatus(t, resp)
}

func readUpgradeStatus(t *testing.T, resp *http.Response) upgradeStatusBody {
	t.Helper()
	defer resp.Body.Close()
	var got upgradeStatusBody
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding /api/upgrade: %v", err)
	}
	return got
}

// writeFakeBinary puts an executable script named name at the front of
// PATH for the rest of the test.
func writeFakeBinary(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake " + name + " is POSIX shell only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeImageDigest is the digest the fake docker below says every pulled
// image has.
const fakeImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestRunUpgradesAContainerDeploymentWhenTheButtonIsClicked is the shape
// every deployment scripts/setup.sh installs actually runs: grain from an
// image, upgraded by pulling the tag CI published for a branch and
// repointing the unit's EnvironmentFile at it.
func TestRunUpgradesAContainerDeploymentWhenTheButtonIsClicked(t *testing.T) {
	dockerLog := filepath.Join(t.TempDir(), "docker-argv")
	// A docker that records every invocation and succeeds, and an image
	// that answers the two questions an upgrade asks of one. It names
	// this build's schema, which is the case that matters here: an
	// upgrade onto a build at the same schema as the one running does
	// nothing to this deployment's rows, and the migration that happens
	// when the two differ is cmd/grain's own
	// daemon_schema_migration_test.go. Everything else prints nothing,
	// which is what an image with no `sandbox-image` subcommand looks
	// like -- and this deployment, dispatching into host directories
	// rather than granule VMs, asks for none anyway. `docker image
	// inspect` answers with the digest the upgrade pins the tag to.
	writeFakeBinary(t, "docker", fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
case "$*" in
*schema-version) echo %d ;;
"image inspect "*) printf 'ghcr.io/bwsalmon/grain/grain@%s\n' ;;
esac
exit 0
`, dockerLog, model.SchemaVersion, fakeImageDigest))

	dataDir := upgradeDataDir(t)
	refFile := filepath.Join(dataDir, "image.env")
	const before = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	restartMarker := filepath.Join(t.TempDir(), "restarted")

	deployment := daemon.Config{
		DataDir:             dataDir,
		UpgradeImage:        "ghcr.io/bwsalmon/grain/grain",
		UpgradeImageRefFile: refFile,
		UpgradeRestartCmd:   []string{"touch", restartMarker},
		// What -upgrade-allowed-branches=main,grain/* parses to: the
		// branch below has to be one this deployment trusts.
		UpgradeAllowedBranches: daemon.SplitBranchPatterns("main, grain/*"),
	}
	base, stop := startDaemonForUpgrade(t, deployment)

	// Without this the panel draws "not available" and there is no button
	// at all -- the flags above are exactly what turns it on.
	if status := getUpgradeStatus(t, base); !status.Enabled {
		t.Fatalf("a daemon started with -upgrade-image reports no upgrader: %+v", status)
	}

	final := clickUpgradeButton(t, base, "grain/task-31")
	if final.Phase != "ok" {
		t.Fatalf("the upgrade ended %q: %s", final.Phase, final.Detail)
	}

	const wantRef = "ghcr.io/bwsalmon/grain/grain:grain..task-31"
	// Pinned to what the pull fetched, so the tag moving before the
	// restart cannot change what the restart runs.
	const pinned = wantRef + "@" + fakeImageDigest
	if got, want := readTrimmed(t, refFile), "GRAIN_IMAGE="+pinned; got != want {
		t.Errorf("the ref file the unit reads = %q, want %q", got, want)
	}
	// The daemon has to have asked for the pull and for the health-check
	// run -- the second is what stops a broken image being cut over to,
	// and it is startUIServer's own HealthCheckArgs that names the
	// subcommand.
	argv := readTrimmed(t, dockerLog)
	if !strings.Contains(argv, "pull "+wantRef) {
		t.Errorf("docker was never asked to pull %s; it was asked:\n%s", wantRef, argv)
	}
	if !strings.Contains(argv, "run --rm "+pinned+" schema-version") {
		t.Errorf("the pulled image was never health-checked; docker was asked:\n%s", argv)
	}
	waitForPath(t, restartMarker, "the restart that brings the new image up")

	// A real restart replaces this process, and the operator's next look
	// at the pane is served by the new one -- so the account of what just
	// happened has to outlive the daemon that performed it. That is what
	// startUIServer's StatusFile under the data directory is for, and a
	// second daemon over the same data directory is the closest this can
	// come to the restart itself.
	stop()
	next, _ := startDaemonForUpgrade(t, deployment)
	after := getUpgradeStatus(t, next)
	if after.Phase != "ok" || after.Branch != "grain/task-31" {
		t.Errorf("the daemon that came up after the upgrade reports %+v, want the finished upgrade of grain/task-31", after)
	}
}

// TestRunUpgradesASourceDeploymentWhenTheButtonIsClicked covers the other
// half of that switch: -upgrade-src-dir, where an upgrade is a real git
// fetch into a checkout on disk, a build, and an install over the running
// binary.
func TestRunUpgradesASourceDeploymentWhenTheButtonIsClicked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	checkout := seedUpgradeCheckout(t, "feature")

	// Stands in for `make container-build`, which is what daemon.go wires
	// -upgrade-src-dir to: it leaves bin/grain in the checkout, and the
	// binary it leaves is a working executable that answers
	// `schema-version` -- so the health check startUIServer configures
	// passes, the schema step finds the build arriving at the schema this
	// one is on, and the install is allowed to stand.
	writeFakeBinary(t, "make", `#!/bin/sh
case "$1" in
container-build) mkdir -p bin && printf '#!/bin/sh\necho `+strconv.Itoa(model.SchemaVersion)+`\n' > bin/grain && chmod +x bin/grain ;;
*) echo "unexpected make target: $*" >&2; exit 2 ;;
esac
`)

	installPath := filepath.Join(t.TempDir(), "grain")
	if err := os.WriteFile(installPath, []byte("the binary this deployment is running"), 0o755); err != nil {
		t.Fatal(err)
	}
	restartMarker := filepath.Join(t.TempDir(), "restarted")

	base, _ := startDaemonForUpgrade(t, daemon.Config{
		DataDir:                upgradeDataDir(t),
		UpgradeSrcDir:          checkout,
		UpgradeInstallPath:     installPath,
		UpgradeRestartCmd:      []string{"touch", restartMarker},
		UpgradeAllowedBranches: []string{"feature"},
	})

	if status := getUpgradeStatus(t, base); !status.Enabled {
		t.Fatalf("a daemon started with -upgrade-src-dir reports no upgrader: %+v", status)
	}

	final := clickUpgradeButton(t, base, "feature")
	if final.Phase != "ok" {
		t.Fatalf("the upgrade ended %q: %s", final.Phase, final.Detail)
	}

	// The checkout moved onto the branch that was asked for...
	if got := gitOutput(t, checkout, "log", "-1", "--format=%s"); got != "on feature" {
		t.Errorf("the checkout's HEAD commit is %q, want the one on the branch that was upgraded to", got)
	}
	// ...and what the build left behind is now the installed binary,
	// still executable.
	if got, want := readTrimmed(t, installPath), strings.TrimSpace(builtBinaryScript); got != want {
		t.Errorf("the installed binary is %q, want the newly built one", got)
	}
	info, err := os.Stat(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the installed binary is not executable: mode %v", info.Mode())
	}
	waitForPath(t, restartMarker, "the restart that brings the new binary up")
}

// seedUpgradeCheckout builds a bare origin with a commit on main and one
// on branch, and clones it -- the checkout -upgrade-src-dir would name on
// a real deployment, with somewhere real to fetch from.
func seedUpgradeCheckout(t *testing.T, branch string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	checkout := filepath.Join(root, "checkout")
	seed := filepath.Join(root, "seed")

	initBareRepo(t, origin)
	git(t, root, "init", "--quiet", "-b", "main", seed)
	git(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "initial")
	git(t, seed, "push", "--quiet", origin, "HEAD:refs/heads/main")
	git(t, seed, "checkout", "--quiet", "-b", branch)
	git(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "on "+branch)
	git(t, seed, "push", "--quiet", origin, "HEAD:refs/heads/"+branch)
	git(t, root, "clone", "--quiet", "-b", "main", origin, checkout)
	return checkout
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

// waitForPath polls for a file the upgrade creates after it has already
// reported "ok" -- the restart command is fired last and its result is
// never waited on (pkg/upgrade's run, deliberately: on a real deployment
// it ends the process).
func waitForPath(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(daemonWaitLimit)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upgrade never ran %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
