package ui_test

// What clicking Upgrade actually does, end to end across the HTTP
// boundary: the exact request UpgradePanel.jsx sends, into the real
// pkg/ui.Server, into a real *upgrade.Upgrader -- not the fakeUpgrader
// server_test.go uses to check route wiring.
//
// The gap this closes is the join. server_test.go proves the routes call
// *an* Upgrader and map its errors; pkg/upgrade proves the pipeline works
// when Start is called directly. Neither one proves that posting
// {"branch":"..."} to /api/upgrade ends with a deployment actually
// upgraded, and that is the whole of what the button claims to do.
//
// The image path (pkg/upgrade/image.go) is what these drive, because it
// is what a deployment scripts/setup.sh installed actually runs: pull the
// tag CI published for a branch, prove it runs, and rewrite the one
// GRAIN_IMAGE= line the systemd unit reads. `docker` itself is stubbed by
// a shell script -- tests/container/upgrade_test.go runs this same path
// against a real registry and a real docker, and needs an image built
// first to do it, so the part worth having *here* is everything between
// the HTTP request and those commands being run.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// upgradeResponse is GET/POST /api/upgrade's body -- the same fields
// UpgradePanel.jsx reads, spelled the same way, so a rename that broke
// the panel would break this too.
type upgradeResponse struct {
	Enabled    bool            `json:"enabled"`
	Mode       string          `json:"mode"`
	Running    *upgradeRunning `json:"running"`
	Channel    string          `json:"channel"`
	Branch     string          `json:"branch"`
	Phase      string          `json:"phase"`
	Detail     string          `json:"detail"`
	Unchanged  bool            `json:"unchanged"`
	Steps      []upgradeStep   `json:"steps"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`

	ImageRepo        string               `json:"imageRepo"`
	DefaultImageRepo string               `json:"defaultImageRepo"`
	DefaultImageRef  string               `json:"defaultImageRef"`
	ImageRepos       []string             `json:"imageRepos"`
	DiscardedRC      *upgrade.DiscardedRC `json:"discardedRc"`
}

// upgradeRunning is that body's "running": where the deployment already
// is, which the pane shows beside where the button would take it.
type upgradeRunning struct {
	Image       string     `json:"image"`
	Commit      string     `json:"commit"`
	CommittedAt *time.Time `json:"committedAt"`
	Modified    bool       `json:"modified"`
}

// upgradeStep is one entry of that body's "steps" -- the plan the pane
// draws its progress bar and its running commentary from.
type upgradeStep struct {
	Name       string     `json:"name"`
	State      string     `json:"state"`
	Detail     string     `json:"detail"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// stepStates is what the pane would render for each step, in order, for
// an assertion that reads as the sequence it is about.
func stepStates(resp upgradeResponse) map[string]string {
	states := map[string]string{}
	for _, step := range resp.Steps {
		states[step.Name] = step.State
	}
	return states
}

// serverWithRealUpgrader wires a genuine *upgrade.Upgrader into a real
// Server, configured the way pkg/cli/daemon.go's own -upgrade-image
// branch configures one: the same "schema-version" health check, a
// restart command, and a status file under the data directory. Only
// PullCmd/RunCmd differ, standing in for a docker this test has no reason
// to need.
// testImageDigest is the digest serverWithRealUpgrader's stand-in for
// `docker image inspect` gives every pulled image.
const testImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func serverWithRealUpgrader(t *testing.T, image *upgrade.ImageConfig, dir string) *ui.Server {
	t.Helper()
	_, store, ctx := testClient(t)
	cfgStored := model.DefaultConfig()
	_ = store.PutConfig(ctx, cfgStored)
	// `docker image inspect` too: every pulled tag resolves to
	// testImageDigest, which is what the upgrade then records.
	if image != nil && image.DigestCmd == nil {
		image.DigestCmd = []string{"sh", "-c", `printf '%s@%s\n' "${1%:*}" ` + testImageDigest, "sh"}
	}
	up := upgrade.New(upgrade.Config{
		Image:           image,
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"touch", filepath.Join(dir, "restarted")},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
		// The branches these tests click Upgrade on, which are about
		// what an allowed upgrade does rather than which are allowed.
		AllowedBranches: []string{"main", "grain/*", "branch-ci-has-not-published-yet"},
	})
	// An upgrade outlives every assertion made about it here: the status
	// file says "ok" before the restart command has even been started,
	// and the writes that follow -- the restart step's result, the final
	// flush -- land after pollUpgrade has already returned. A test that
	// returned in that window would leave those writes racing the
	// RemoveAll t.TempDir registered for the directory they are being
	// made in, and RemoveAll losing that race fails the test from the
	// cleanup path with "TempDir RemoveAll cleanup: unlinkat ...:
	// directory not empty" -- a red build with nothing wrong behind it.
	// (Seen in pkg/upgrade's own tests, which run the same Upgrader the
	// same way; this is the same guard, made through the one thing
	// pkg/upgrade exports about it.)
	//
	// Cleanups run last-registered-first, so this runs before that
	// removal: dir was a t.TempDir the caller had to create before it
	// could pass it here.
	t.Cleanup(func() { waitForUpgradeToFinish(t, up) })
	cfg := ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Upgrader:     up,
	}
	return ui.NewServer(cfg, store)
}

// waitForUpgradeToFinish blocks until up's run goroutine has let go of
// the deployment -- which is later than any phase the status file
// reports, and is the only point at which nothing is going to write
// under dir again (upgrade.Upgrader.Done).
func waitForUpgradeToFinish(t *testing.T, up *upgrade.Upgrader) {
	t.Helper()
	select {
	case <-up.Done():
	case <-time.After(upgradeWaitTimeout):
		// Not Fatalf: this runs from a cleanup, which is no place to
		// stop the world.
		t.Errorf("timed out waiting for the upgrade to finish")
	}
}

// upgradeWaitTimeout bounds every wait in this file. Deliberately far
// longer than any of these upgrades needs -- none of them measures how
// fast one is, so a tight bound can only redden the build on a busy CI
// runner (pkg/upgrade's own waitTimeout, for the same reason).
const upgradeWaitTimeout = 60 * time.Second

// clickUpgrade posts what the Upgrade button posts -- api("/api/upgrade",
// {method: "POST", body: JSON.stringify({branch})}) in UpgradePanel.jsx --
// and returns the status the panel would render from the reply.
func clickUpgrade(t *testing.T, srv *ui.Server, branch string) (int, upgradeResponse) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"branch": branch})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodPost, "/api/upgrade", string(body))
	if rec.Code >= 300 {
		return rec.Code, upgradeResponse{}
	}
	return rec.Code, decode[upgradeResponse](t, rec)
}

// pollUpgrade re-fetches GET /api/upgrade the way the panel's own
// STATUS_POLL_MS interval does, until the upgrade stops reporting
// "running".
func pollUpgrade(t *testing.T, srv *ui.Server) upgradeResponse {
	t.Helper()
	deadline := time.Now().Add(upgradeWaitTimeout)
	for time.Now().Before(deadline) {
		rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/upgrade = %d: %s", rec.Code, rec.Body)
		}
		got := decode[upgradeResponse](t, rec)
		if got.Phase != string(upgrade.PhaseRunning) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the upgrade never stopped reporting \"running\"")
	return upgradeResponse{}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// TestClickingUpgradeRunsARealUpgrade is the button's whole claim, from
// the request it sends to the file that decides what this deployment runs
// next.
func TestClickingUpgradeRunsARealUpgrade(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	ran := filepath.Join(dir, "ran")
	refFile := filepath.Join(dir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The pull blocks until this file exists, so that the upgrade is
	// still going when the assertions about a *running* upgrade are made.
	// With a pull that returns straight away the whole pipeline takes
	// about ten milliseconds, which is quick enough for a loaded machine
	// to run it to completion between Start returning and the POST
	// handler reading the status back -- and a reply saying "ok" there is
	// a correct answer about a finished upgrade, not the bug an assertion
	// on "running" would be reporting (grain/task-179).
	releaseFile := filepath.Join(dir, "release-pull")
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    refFile,
		// Stand-ins for `docker pull` and `docker run --rm`, each
		// recording the argv it was handed so the assertions below can
		// read back what was actually pulled and run.
		PullCmd: []string{"sh", "-c", `printf '%s' "$*" > ` + pulled +
			`; while [ ! -e ` + releaseFile + ` ]; do sleep 0.05; done`, "sh"},
		RunCmd: []string{"sh", "-c", `printf '%s' "$*" > ` + ran, "sh"},
	}, dir)
	// Registered after serverWithRealUpgrader's own cleanup so that it
	// runs before it (cleanups are last-in-first-out): a test that fails
	// before it gets to the release below would otherwise leave the pull
	// spinning and waitForUpgradeToFinish waiting out its whole timeout.
	releasePull := sync.OnceFunc(func() {
		if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(releasePull)

	// The pane the button lives on renders at all only because GET says
	// this deployment has an upgrader.
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if before := decode[upgradeResponse](t, rec); !before.Enabled || before.Phase != "" {
		t.Fatalf("before any upgrade: %+v, want enabled with no phase yet", before)
	}

	code, accepted := clickUpgrade(t, srv, "grain/task-31")
	if code != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade = %d, want 202", code)
	}
	// The reply is what the panel swaps in immediately, and what disables
	// the button until the poll below says otherwise.
	if accepted.Phase != string(upgrade.PhaseRunning) || accepted.Branch != "grain/task-31" {
		t.Fatalf("the reply the panel renders = %+v, want a running upgrade of grain/task-31", accepted)
	}
	// And the poll the panel keeps up meanwhile says the same, so the
	// button stays disabled rather than flickering back on between the
	// reply and the end of the upgrade.
	rec = do(t, srv, http.MethodGet, "/api/upgrade", "")
	if during := decode[upgradeResponse](t, rec); during.Phase != string(upgrade.PhaseRunning) {
		t.Fatalf("polled mid-upgrade: %+v, want it still running", during)
	}

	releasePull()
	final := pollUpgrade(t, srv)
	if final.Phase != string(upgrade.PhaseOK) {
		t.Fatalf("the upgrade ended %q: %s", final.Phase, final.Detail)
	}
	if final.FinishedAt == nil || final.StartedAt == nil {
		t.Errorf("a finished upgrade carries StartedAt=%v FinishedAt=%v", final.StartedAt, final.FinishedAt)
	}

	// '/' is not legal in a tag, so the branch the operator typed and the
	// image CI published for it are spelled differently -- the button has
	// to have made that translation rather than pulling ":grain/task-31".
	const wantRef = "ghcr.io/bwsalmon/grain/grain:grain..task-31"
	if got := readTestFile(t, pulled); got != wantRef {
		t.Errorf("pulled %q, want %q", got, wantRef)
	}
	// And from the pull on, the image it fetched rather than the tag:
	// the tag may have moved by the time the restart reads the ref file.
	const pinned = wantRef + "@" + testImageDigest
	if got, want := readTestFile(t, ran), pinned+" schema-version"; got != want {
		t.Errorf("health-checked with %q, want %q", got, want)
	}
	if got, want := readTestFile(t, refFile), "GRAIN_IMAGE="+pinned+"\n"; got != want {
		t.Errorf("the ref file the unit reads = %q, want %q", got, want)
	}
	if !strings.Contains(final.Detail, wantRef) {
		t.Errorf("the detail the panel shows = %q, want it to name the image now installed", final.Detail)
	}
	// The plan the pane draws its progress from, all the way through the
	// HTTP boundary: the steps that ran, the one this deployment has no
	// sandbox containers for, and the restart still in flight below.
	states := stepStates(final)
	for name, want := range map[string]string{
		"pull image":    "done",
		"health check":  "done",
		"sandbox image": "skipped",
		"record image":  "done",
	} {
		if got := states[name]; got != want {
			t.Errorf("step %q = %q, want %q (whole plan: %+v)", name, got, want, final.Steps)
		}
	}
	if pull := final.Steps[0]; pull.StartedAt == nil || pull.FinishedAt == nil {
		t.Errorf("the pull step carries no timings for the pane to count: %+v", pull)
	}
	// The restart is fired after the "ok" status is written (pkg/upgrade's
	// run, deliberately), so it can still be in flight here.
	deadline := time.Now().Add(upgradeWaitTimeout)
	for {
		if _, err := os.Stat(filepath.Join(dir, "restarted")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upgrade never asked for the restart that brings the new image up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAFailedUpgradeIsReportedThroughTheAPI: the panel's only account of
// a failure is the phase and detail it reads back from this endpoint, so
// a pull that fails has to arrive there -- with the failing command's own
// output, since "upgrade failed" alone tells an operator nothing about
// whether to retry, fix a typo, or wait for CI to finish publishing.
func TestAFailedUpgradeIsReportedThroughTheAPI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    refFile,
		PullCmd:    []string{"sh", "-c", "echo 'manifest unknown' >&2; exit 1", "sh"},
	}, dir)

	if code, _ := clickUpgrade(t, srv, "branch-ci-has-not-published-yet"); code != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade = %d, want 202", code)
	}

	final := pollUpgrade(t, srv)
	if final.Phase != string(upgrade.PhaseFailed) {
		t.Fatalf("phase = %q, want %q", final.Phase, upgrade.PhaseFailed)
	}
	if !strings.Contains(final.Detail, "manifest unknown") {
		t.Errorf("detail = %q, want the failing pull's own output in it", final.Detail)
	}
	// And the pane can point at the step that failed rather than only
	// saying the upgrade did.
	if got := stepStates(final)["pull image"]; got != "failed" {
		t.Errorf("the pull step = %q, want %q (whole plan: %+v)", got, "failed", final.Steps)
	}
	if got := readTestFile(t, refFile); got != previous {
		t.Errorf("a failed upgrade rewrote the ref file to %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "restarted")); err == nil {
		t.Error("a failed upgrade still asked for a restart")
	}

	// And the deployment is not wedged: the next click is allowed
	// through, rather than being told an upgrade is still in progress.
	if code, _ := clickUpgrade(t, srv, "main"); code != http.StatusAccepted {
		t.Fatalf("a second POST after a failed upgrade = %d, want 202", code)
	}
	pollUpgrade(t, srv)
}

// TestUpgradeRejectsABranchNameThatIsNotOne: the panel sends whatever was
// typed into the box, so validateBranch's rejection has to come back as a
// 400 the panel can show -- and, more to the point, no upgrade may start.
func TestUpgradeRejectsABranchNameThatIsNotOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pullMarker := filepath.Join(dir, "pulled")
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    filepath.Join(dir, "image.env"),
		PullCmd:    []string{"touch", pullMarker},
	}, dir)

	for _, branch := range []string{"--upgrade-image", "main; rm -rf /", "a..b", "   "} {
		code, _ := clickUpgrade(t, srv, branch)
		if code != http.StatusBadRequest {
			t.Errorf("POST /api/upgrade with branch %q = %d, want 400", branch, code)
		}
	}

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if got := decode[upgradeResponse](t, rec); got.Phase != "" {
		t.Errorf("a rejected branch left a status behind: %+v", got)
	}
	if _, err := os.Stat(pullMarker); err == nil {
		t.Error("a rejected branch reached the pull anyway")
	}
}

// TestAnUpgradeThatNamesNoBranchFollowsTheChannel: the pane, a
// scheduled upgrade and an agent's upgrade all post here, and one that
// names no branch goes to the channel the deployment already runs
// (grain/task-3689) -- latest for one installed on a release, not
// main's head. A deployment pinned to one build follows nothing, so it
// still has to be told.
func TestAnUpgradeThatNamesNoBranchFollowsTheChannel(t *testing.T) {
	t.Parallel()
	up := &fakeUpgrader{running: upgrade.Running{Ref: "ghcr.io/bwsalmon/grain/grain:latest@sha256:0123abcd"}}
	srv := testServerWithUpgrader(t, up)

	if got := decode[upgradeResponse](t, do(t, srv, http.MethodGet, "/api/upgrade", "")); got.Channel != "latest" {
		t.Errorf("GET /api/upgrade channel = %q, want latest", got.Channel)
	}
	if rec := do(t, srv, http.MethodPost, "/api/upgrade", `{}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade with no branch = %d, want 202: %s", rec.Code, rec.Body)
	}
	if up.started != "latest" {
		t.Errorf("an upgrade naming no branch started onto %q, want the deployment's channel, latest", up.started)
	}

	pinned := &fakeUpgrader{running: upgrade.Running{Ref: "ghcr.io/bwsalmon/grain/grain:sha-1234567"}}
	srv = testServerWithUpgrader(t, pinned)
	if rec := do(t, srv, http.MethodPost, "/api/upgrade", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/upgrade with no branch on a pinned deployment = %d, want 400: %s", rec.Code, rec.Body)
	}
	if pinned.started != "" {
		t.Errorf("a pinned deployment started an upgrade onto %q with no branch named", pinned.started)
	}
}

// TestStartUpgradeRejectsAMalformedBody covers the other way a POST can
// be wrong -- a body that is not the JSON this endpoint reads at all.
func TestStartUpgradeRejectsAMalformedBody(t *testing.T) {
	t.Parallel()
	srv := testServerWithUpgrader(t, &fakeUpgrader{})
	rec := do(t, srv, http.MethodPost, "/api/upgrade", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestUpgradeStatusReportsWhichPipelineItRuns: the pane's own copy is
// built from this field, and getting it wrong means describing a
// checkout and a build to an operator on a host that has neither
// (grain/task-39). Both real configurations, through the real handler.
func TestUpgradeStatusReportsWhichPipelineItRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	image := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    filepath.Join(dir, "image.env"),
	}, dir)
	rec := do(t, image, http.MethodGet, "/api/upgrade", "")
	if got := decode[upgradeResponse](t, rec); got.Mode != string(upgrade.ModeImage) {
		t.Errorf("mode = %q, want %q", got.Mode, upgrade.ModeImage)
	}

	// A deployment that still builds on the host: nil Image is the whole
	// difference, exactly as pkg/cli/daemon.go's -upgrade-src-dir
	// branch leaves it.
	source := serverWithRealUpgrader(t, nil, t.TempDir())
	rec = do(t, source, http.MethodGet, "/api/upgrade", "")
	if got := decode[upgradeResponse](t, rec); got.Mode != string(upgrade.ModeSource) {
		t.Errorf("mode = %q, want %q", got.Mode, upgrade.ModeSource)
	}

	// And nothing to name at all when no upgrader is configured, so the
	// panel cannot key copy off a mode it has no pipeline for.
	srv, _ := testServer(t)
	rec = do(t, srv, http.MethodGet, "/api/upgrade", "")
	got := decode[upgradeResponse](t, rec)
	if got.Enabled || got.Mode != "" {
		t.Errorf("with no upgrader: enabled = %v, mode = %q, want false and empty", got.Enabled, got.Mode)
	}
}

// TestUpgradeStatusReportsWhatItIsUpgradingFrom: the pane can only say
// "now running X, upgrading to Y" if the daemon tells it X, and the
// deployment that most needs it is the one with no attempt on record at
// all -- its ref file was written by scripts/setup.sh, so the last
// attempt's Detail string, which is where an operator used to have to
// infer this from, does not exist (grain/task-50).
func TestUpgradeStatusReportsWhatItIsUpgradingFrom(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    refFile,
		PullCmd:    []string{"true"},
		RunCmd:     []string{"true"},
	}, dir)

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	got := decode[upgradeResponse](t, rec)
	if got.Running == nil {
		t.Fatalf("no \"running\" on a deployment whose ref file names one: %+v", got)
	}
	if want := "ghcr.io/bwsalmon/grain/grain:main"; got.Running.Image != want {
		t.Errorf("running.image = %q, want %q", got.Running.Image, want)
	}
	if got.Phase != "" {
		t.Errorf("phase = %q, want none: this is what the pane shows *instead* of an attempt", got.Phase)
	}

	// And it moves with the deployment: after a successful upgrade the
	// pane is describing the image that upgrade cut over to.
	if code, _ := clickUpgrade(t, srv, "grain/task-50"); code != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade = %d, want 202", code)
	}
	final := pollUpgrade(t, srv)
	if final.Phase != string(upgrade.PhaseOK) {
		t.Fatalf("phase = %q, want %q: %s", final.Phase, upgrade.PhaseOK, final.Detail)
	}
	if want := "ghcr.io/bwsalmon/grain/grain:grain..task-50@" + testImageDigest; final.Running == nil || final.Running.Image != want {
		t.Errorf("running = %+v after the upgrade, want %q", final.Running, want)
	}
}

// TestUpgradeStatusOmitsWhatItCannotDetermine: a deployment whose ref
// file cannot be read still has a working Upgrade button, and a pane
// that shows one line fewer -- not a 500, and not an empty "running"
// object the frontend would have to tell apart from a real one.
func TestUpgradeStatusOmitsWhatItCannotDetermine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    filepath.Join(dir, "never-written.env"),
	}, dir)

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeResponse](t, rec)
	if !got.Enabled {
		t.Errorf("enabled = false: an unreadable ref file must not disable upgrading")
	}
	if got.Running != nil {
		t.Errorf("running = %+v with no ref file to read, want it absent", got.Running)
	}
}

// brokenUpgrader stands in for a deployment whose status file cannot be
// read -- a data directory gone read-only, a truncated write from a host
// that lost power mid-upgrade.
type brokenUpgrader struct{}

func (brokenUpgrader) Start(string) error { return nil }
func (brokenUpgrader) Status() (upgrade.Status, error) {
	return upgrade.Status{}, errors.New("upgrade-status.json: permission denied")
}
func (brokenUpgrader) Mode() upgrade.Mode { return upgrade.ModeImage }
func (brokenUpgrader) ValidateBranch(branch string) (string, error) {
	return upgrade.ValidBranchName(branch)
}
func (brokenUpgrader) Running() (upgrade.Running, error) {
	return upgrade.Running{}, errors.New("image.env: permission denied")
}

// TestUpgradeStatusReportsAnUnreadableStatusFile: the panel treats a
// failed GET as an error to show, not as "no upgrader", so this must be a
// 500 rather than a 200 carrying an empty status that would quietly
// re-enable a button whose upgrade is still running.
func TestUpgradeStatusReportsAnUnreadableStatusFile(t *testing.T) {
	t.Parallel()
	srv := testServerWithUpgrader(t, brokenUpgrader{})
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
}

// TestUpgradeTargetsListsTheSelector: GET /api/upgrade/targets is what
// the pane's branch selector is built from -- the channels and released
// versions, each marked with whether this deployment may upgrade to it,
// and the patterns a hand-typed branch is held to.
func TestUpgradeTargetsListsTheSelector(t *testing.T) {
	t.Parallel()
	up := upgrade.New(upgrade.Config{
		Image:           &upgrade.ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		AllowedBranches: []string{"main", "candidate"},
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"main", "v1.6.0", "sha-1234567"}, nil
		},
	})
	srv := testServerWithUpgrader(t, up)
	rec := do(t, srv, http.MethodGet, "/api/upgrade/targets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgrade.Targets](t, rec)
	var names []string
	for _, target := range got.Targets {
		names = append(names, target.Name+"="+map[bool]string{true: "ok", false: "refused"}[target.Allowed])
	}
	if want := "latest=refused candidate=ok main=ok v1.6.0=refused"; strings.Join(names, " ") != want {
		t.Errorf("targets = %s, want %s", strings.Join(names, " "), want)
	}
	if strings.Join(got.AllowedBranches, ",") != "main,candidate" {
		t.Errorf("allowedBranches = %v", got.AllowedBranches)
	}

	// A deployment that never set -upgrade-allowed-branches greys none
	// of the standard targets out (grain/task-2011): the selector the
	// pane builds from this offers latest, candidate, main and the
	// released versions as ones it takes.
	def := testServerWithUpgrader(t, upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"main", "v1.6.0", "v1.7.0", "sha-1234567"}, nil
		},
	}))
	rec = do(t, def, http.MethodGet, "/api/upgrade/targets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("default deployment: status = %d: %s", rec.Code, rec.Body)
	}
	names = nil
	for _, target := range decode[upgrade.Targets](t, rec).Targets {
		names = append(names, target.Name+"="+map[bool]string{true: "ok", false: "refused"}[target.Allowed])
	}
	if want := "latest=ok candidate=ok main=ok v1.7.0=ok v1.6.0=ok"; strings.Join(names, " ") != want {
		t.Errorf("default deployment: targets = %s, want %s", strings.Join(names, " "), want)
	}

	// No upgrader, no targets: the same 404 every other upgrade route
	// gives a deployment that cannot upgrade.
	none := testServerWithUpgrader(t, nil)
	if rec := do(t, none, http.MethodGet, "/api/upgrade/targets", ""); rec.Code != http.StatusNotFound {
		t.Errorf("without an upgrader: status = %d, want 404", rec.Code)
	}
}

func TestClickingUpgradeDoesNotUpgradeWhenVersionHasNotChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	alreadyRunning := "ghcr.io/bwsalmon/grain/grain:main@" + testImageDigest
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+alreadyRunning+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: "ghcr.io/bwsalmon/grain/grain",
		RefFile:    refFile,
		PullCmd:    []string{"true"},
		RunCmd:     []string{"false"},
	}, dir)

	code, _ := clickUpgrade(t, srv, "main")
	if code != http.StatusAccepted {
		t.Fatalf("POST /api/upgrade = %d, want 202", code)
	}
	done := pollUpgrade(t, srv)
	if done.Phase != string(upgrade.PhaseOK) || !done.Unchanged {
		t.Fatalf("done = %+v, want phase ok with unchanged=true", done)
	}
	if !strings.Contains(done.Detail, "the version on main has not changed") {
		t.Errorf("detail = %q, want it to say the version on main has not changed", done.Detail)
	}
	if _, err := os.Stat(filepath.Join(dir, "restarted")); !os.IsNotExist(err) {
		t.Errorf("restarted marker exists (err=%v), want no restart when version has not changed", err)
	}
}

// The pane can say exactly what the button will pull before it is
// pressed, and an operator can point it at another known repository --
// or a tag in one -- without touching the host's environment.
func TestUpgradeChoosesTheImageItPulls(t *testing.T) {
	const ghcr = installtracktest.Repository
	const ar = installtracktest.OptInRepository
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	pulled := filepath.Join(dir, "pulled")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+ar+":main@"+testImageDigest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWithRealUpgrader(t, &upgrade.ImageConfig{
		Repository: ar,
		Track:      installtracktest.Track,
		RefFile:    refFile,
		PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
		RunCmd:     []string{"sh", "-c", "echo 7"},
	}, dir)

	got := decode[upgradeResponse](t, do(t, srv, http.MethodGet, "/api/upgrade", ""))
	if got.ImageRepo != ar || got.DefaultImageRepo != ghcr || got.DefaultImageRef != ar+":main" {
		t.Errorf("GET /api/upgrade image = %q, default %q, ref %q; want %q, %q, %q",
			got.ImageRepo, got.DefaultImageRepo, got.DefaultImageRef, ar, ghcr, ar+":main")
	}
	if !slices.Contains(got.ImageRepos, ghcr) || !slices.Contains(got.ImageRepos, ar) {
		t.Errorf("imageRepos = %v, want both %s and %s", got.ImageRepos, ghcr, ar)
	}

	rec := do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"main","image":"evil.example/grain:main"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST an untrusted image = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"main","image":"`+ghcr+`","afterTasksFinish":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST a scheduled image upgrade = %d, want 400: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"main","image":"`+ghcr+`"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST image %s = %d: %s", ghcr, rec.Code, rec.Body)
	}
	if final := pollUpgrade(t, srv); final.Phase != string(upgrade.PhaseOK) {
		t.Fatalf("upgrade ended %q: %s", final.Phase, final.Detail)
	}
	if got := readTestFile(t, pulled); got != ghcr+":main\n" {
		t.Errorf("pulled %q, want %s:main", got, ghcr)
	}
	if got, want := readTestFile(t, refFile), "GRAIN_IMAGE="+ghcr+":main@"+testImageDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
	got = decode[upgradeResponse](t, do(t, srv, http.MethodGet, "/api/upgrade", ""))
	if got.ImageRepo != ghcr {
		t.Errorf("after the switch imageRepo = %q, want %q remembered", got.ImageRepo, ghcr)
	}
}

func TestUpgradeStatusAndTargetsReportDiscardedRC(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:v2.3.0-rc.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
		},
		RunningRelease: func() string { return "v2.3.0-rc.1" },
		StatusFile:     filepath.Join(dir, "upgrade-status.json"),
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"latest", "candidate", "rc", "v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded", "v2.3.0-rc.2"}, nil
		},
	})
	srv := testServerWithUpgrader(t, up)

	targetsRec := do(t, srv, http.MethodGet, "/api/upgrade/targets", "")
	if targetsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade/targets = %d: %s", targetsRec.Code, targetsRec.Body)
	}
	targets := decode[upgrade.Targets](t, targetsRec)
	if targets.DiscardedRC == nil || targets.DiscardedRC.Running != "v2.3.0-rc.1" ||
		targets.DiscardedRC.Latest != "v2.1.1" || targets.DiscardedRC.ReplacementRC != "v2.3.0-rc.2" {
		t.Errorf("targets.DiscardedRC = %+v, want running=v2.3.0-rc.1 latest=v2.1.1 replacementRc=v2.3.0-rc.2", targets.DiscardedRC)
	}

	statusRec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade = %d: %s", statusRec.Code, statusRec.Body)
	}
	status := decode[upgradeResponse](t, statusRec)
	if status.Channel != "candidate" {
		t.Errorf("status.Channel = %q, want candidate", status.Channel)
	}
	if status.DiscardedRC == nil || status.DiscardedRC.Running != "v2.3.0-rc.1" ||
		status.DiscardedRC.Latest != "v2.1.1" || status.DiscardedRC.ReplacementRC != "v2.3.0-rc.2" {
		t.Errorf("status.DiscardedRC = %+v, want running=v2.3.0-rc.1 latest=v2.1.1 replacementRc=v2.3.0-rc.2", status.DiscardedRC)
	}
}
