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

package container

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var digits = regexp.MustCompile(`^\d+$`)

// `docker run <image> schema-version` is not an arbitrary smoke test.
//
// It is the exact command pkg/upgrade's image health check runs against a
// freshly pulled image before pointing a deployment at it
// (healthCheckImage), and the one setup.sh's
// reformat_store_if_schema_changed asks through its own wrapper. If the
// entrypoint, the binary or its libc coupling were wrong, this is where
// every one of those would fail.
func TestTheImageRunsTheCLIAndReportsASchemaVersion(t *testing.T) {
	requireImage(t)
	out := strings.TrimSpace(docker(t, "run", "--rm", image, "schema-version"))
	if !digits.MatchString(out) {
		t.Fatalf("schema-version printed %q", out)
	}
}

// Dockerfile's header lists these and says why each is there.
//
// A missing one does not fail a build or a startup -- it fails the first
// dispatch, the first granule VM, or the first look at the Logs or Top
// pane, which is exactly the class of failure putting them in an image
// was meant to end.
//
// Every agent CLI is in that list on purpose: which framework a run uses
// is a live per-task choice, so an image carrying some of them is an
// image that fails every run choosing another.
func TestTheImageCarriesEveryBinaryTheDaemonShellsOutTo(t *testing.T) {
	requireImage(t)
	script := `for b in git bash granule kubectl docker claude agy codex journalctl top ssh curl initdb postgres pg_ctl psql; do ` +
		`command -v "$b" >/dev/null || { echo "MISSING $b"; exit 1; }; ` +
		`done; echo all-present`

	out := docker(t, "run", "--rm", "--entrypoint", "sh", image, "-c", script)
	if !strings.Contains(out, "all-present") {
		t.Fatalf("a binary the daemon shells out to is missing: %s", out)
	}
}

// The image carries the agy it was built with, and goes on carrying it.
//
// agy updates its own binary in the background during an ordinary run --
// one dispatch-shaped exec of the pinned release replaced a writable copy
// seven seconds later (docs/hand-runs/2026-09-17-agy-self-update.md) --
// which would make the Dockerfile's verified `AGY_CLI_SHA256_AMD64`, the
// `agy --version` check at the end of that install, and every measured
// fact in pkg/agent/antigravity claims about a release this image stops
// running seconds into its first dispatch.
//
// pkg/agent/antigravity sets this for every agy grain itself starts
// (selfupdate.go); the ENV asserted here is what covers the execs that do
// not come through that package, including an operator's shell in this
// container. The value has to be exactly "true" -- a bare 1 was measured
// not to disable anything.
func TestTheImageDisablesAgysSelfUpdater(t *testing.T) {
	requireImage(t)
	out := strings.TrimSpace(docker(t, "run", "--rm", "--entrypoint", "sh", image, "-c",
		`printf '%s' "${AGY_CLI_DISABLE_AUTO_UPDATE-unset}"`))
	if out != "true" {
		t.Fatalf("AGY_CLI_DISABLE_AUTO_UPDATE in the image is %q, want \"true\": agy will replace the "+
			"pinned binary this image verified at build time", out)
	}
}

// A host on PATH alone is insufficient: Codex looks beside its resolved
// executable. Exercise that location as the unprivileged deployment user
// so a misplaced host cannot pass the image's publication checks.
func TestTheImageCanLaunchTheCodexCodeModeHost(t *testing.T) {
	requireImage(t)
	script := `set -eu
codex="$(readlink -f "$(command -v codex)")"
host="$(dirname "$codex")/codex-code-mode-host"
"$host" --help >/dev/null
echo code-mode-host-ready`
	out := docker(t, "run", "--rm", "--user", "1234:1234", "--entrypoint", "sh", image, "-c", script)
	if !strings.Contains(out, "code-mode-host-ready") {
		t.Fatalf("Codex cannot launch its sibling Code Mode host: %s", out)
	}
}

// A granule deployment runs a second image, and is told about it by this
// one.
//
// `grain sandbox-image` prints the sandbox reference stamped into this
// build at build time (pkg/cli/sandboximage.go). That is what
// scripts/setup.sh pulls when GRAIN_GRANULE_OCI_IMAGE names nothing, and
// what an upgrade asks a newly pulled grain for so it can pull the
// matching sandbox alongside it -- so an image that cannot answer is a
// deployment that has to be configured by hand, and one that answers with
// the wrong thing is a deployment running two commits' worth of itself.
//
// The name it must print is `guest`, and that is the whole of the
// rename: what this used to call granule-sandbox was a generic granule
// container that a deployment then built a guest disk to run inside it.
// A guest is derived by booting a granule image and committing the
// result now, so what comes out is itself runnable -- one artifact that
// is both the container and the guest, and so one reference to stamp.
func TestTheImageNamesTheSandboxContainerItExpects(t *testing.T) {
	requireImage(t)
	out := strings.TrimSpace(docker(t, "run", "--rm", image, "sandbox-image"))

	if !strings.Contains(out, "/guest:") {
		t.Fatalf("sandbox-image printed %q", out)
	}
	// A bare `make image` leaves the source default (the tag CI keeps
	// pointed at main); CI stamps the immutable tag of the guest built
	// from the same commit. Either is a real reference -- what must never
	// happen is an empty or malformed one.
	if strings.HasSuffix(out, ":") || strings.Contains(out, " ") {
		t.Fatalf("sandbox-image printed a malformed reference: %q", out)
	}
}

// And the image knows what it is itself.
//
// `grain image` prints the reference stamped into this build
// (pkg/cli/grainimage.go), which is what a deployment writes into the
// CI step it installs in its own state repository. `grain state check`
// refuses a dump stamped with a schema it does not know, so a deployment
// that named the wrong build there would fail every pull request against
// its own settings for a reason that had nothing to do with the change
// in it -- which is precisely what naming a tag that follows main used
// to do to every deployment that was not tracking main.
func TestTheImageNamesItself(t *testing.T) {
	requireImage(t)
	out := strings.TrimSpace(docker(t, "run", "--rm", image, "image"))

	// There are two daemon images, grain and google-grain, and each names
	// itself. When the image under test is one of them by name -- as it is
	// in the release workflow -- it must print that name; a local tag
	// (`grain-e2e:test`) says nothing about which, so either will do.
	names := []string{"/grain:", "/google-grain:"}
	if own := daemonImageName(image); own != "" {
		names = []string{"/" + own + ":"}
	}
	named := false
	for _, name := range names {
		named = named || strings.Contains(out, name)
	}
	if !named {
		t.Fatalf("image printed %q, want a reference containing one of %q", out, names)
	}
	// As above: a bare `make image` leaves the source default and CI
	// stamps the immutable sha- tag it is publishing under. Either is a
	// real reference; an empty or malformed one is a deployment writing
	// nonsense into its own state repository.
	if strings.HasSuffix(out, ":") || strings.Contains(out, " ") {
		t.Fatalf("image printed a malformed reference: %q", out)
	}
}

// daemonImageName is "grain" or "google-grain" when ref is a reference to
// that daemon image (ghcr.io/bwsalmon/grain/google-grain:sha-abc1234), and
// "" when it is anything else, such as a locally built `grain-e2e:test`.
func daemonImageName(ref string) string {
	repo := ref
	if at := strings.Index(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
		repo = repo[:colon]
	}
	switch name := repo[strings.LastIndex(repo, "/")+1:]; name {
	case "grain", "google-grain":
		if strings.Contains(repo, "/") {
			return name
		}
	}
	return ""
}

// The release label and the binary say the same thing.
//
// org.opencontainers.image.version is what `release-images.sh promote`
// reads off the registry to decide a published vX.Y.Z is already stamped
// and needs no rebuild (grain/task-3064), and `grain version` is what the
// UI's sidebar footer shows. A label naming a release the binary does not
// report would leave `latest` showing an rc forever, with every promote
// tick satisfied. A merge's build carries no label, and its binary
// reports whatever Go read off the commit; this asks only that a label,
// where there is one, is the binary's own release.
func TestTheImagesReleaseLabelIsTheReleaseItReports(t *testing.T) {
	requireImage(t)
	label := strings.TrimSpace(docker(t, "image", "inspect", "--format",
		`{{ index .Config.Labels "org.opencontainers.image.version" }}`, image))
	if label == "" || label == "<no value>" {
		return
	}
	var reported struct {
		Release string `json:"release"`
	}
	out := docker(t, "run", "--rm", image, "version", "--json")
	if err := json.Unmarshal([]byte(out), &reported); err != nil {
		t.Fatalf("version --json printed %q: %v", out, err)
	}
	if reported.Release != label {
		t.Fatalf("%s is labelled release %q but its binary reports %q", image, label, reported.Release)
	}
}

// The image carries the source it was built from.
//
// tests/deploy pins that the Dockerfile says so and that cmd/grain looks
// there; this is the half that can only be asked of a real image -- that
// the tree actually arrived, that it is grain's own and not some other
// checkout, and that the three things deliberately left behind stayed
// behind. .git is the one worth naming: it is 6MB of history no reader
// wants (selfdebug is os.ReadFile and os.ReadDir), and a `cp -a` that
// stopped excluding it would be invisible except in the image's size.
func TestTheImageCarriesItsOwnSource(t *testing.T) {
	requireImage(t)

	const root = "/usr/local/share/grain/src"
	script := `set -e
test -f ` + root + `/go.mod
grep -q "^module github.com/bwsalmon/grain$" ` + root + `/go.mod
test -d ` + root + `/cmd/grain
test -d ` + root + `/pkg/capability/selfdebug
for excluded in .git bin ui/node_modules; do
  if [ -e ` + root + `/$excluded ]; then echo "LEAKED $excluded"; exit 1; fi
done
echo source-present`

	out := docker(t, "run", "--rm", "--entrypoint", "sh", image, "-c", script)
	if !strings.Contains(out, "source-present") {
		t.Fatalf("the image does not carry a usable copy of its own source: %s", out)
	}
}

// ...and the account that reads it can actually read it.
//
// Separate from the test above because it is a different question, and
// because the test above is the one that passed for the whole of
// grain/task-160 and grain/task-250 while read_grain_source answered
// "permission denied" for every path an agent asked for. Every check there
// is `test -f`, `test -d` and `grep` as root -- and root ignores the mode
// bits, so none of them can see a tree whose directories an unprivileged
// account cannot enter.
//
// The account matters and is not this image's own: setup.sh runs the
// daemon as `docker run --user <host uid>:<gid>` for the host's service
// account, a numeric pair with no entry in this image's /etc/passwd and no
// relation to the root that owns these files. So what the tree has to be
// is world-readable, which is what --user 65534:65534 asks here.
//
// The sweep is the assertion rather than any one path: the failure mode is
// "the root lists and nothing below it opens", so naming one file would
// only move the blind spot.
func TestAnUnprivilegedAccountCanReadTheImagesSource(t *testing.T) {
	requireImage(t)

	const root = "/usr/local/share/grain/src"
	script := `set -e
ls ` + root + ` >/dev/null
ls ` + root + `/pkg >/dev/null
cat ` + root + `/pkg/capability/selfdebug/selfdebug.go >/dev/null
cat ` + root + `/go.mod >/dev/null
denied=$(find ` + root + ` \( \( -type d \( ! -readable -o ! -executable \) \) -o \( ! -type d ! -readable \) \) -print 2>/dev/null | head -20 || true)
if [ -n "$denied" ]; then echo "DENIED:"; echo "$denied"; exit 1; fi
echo source-readable`

	out := docker(t, "run", "--rm", "--user", "65534:65534", "--entrypoint", "sh", image, "-c", script)
	if !strings.Contains(out, "source-readable") {
		t.Fatalf("an unprivileged account cannot read the source in this image, so every self-debug run "+
			"this image serves answers \"permission denied\":\n%s", out)
	}
}

// sourceExportMaxMB is what that tree may weigh. grain's tracked source
// is ~27MB, so this is roughly five times the room it needs; raise it
// when the repository honestly grows into it, and keep
// scripts/export-source.sh's own ceiling in step.
const sourceExportMaxMB = 128

// ...and it carries nothing else.
//
// This is the assertion the published image failed for weeks. The export
// was a `cp -a` of the build context with three names deleted from the
// copy, and a release host's checkout still held the tree from before the
// Go module was promoted out of its old subdirectory -- untracked, so no
// checkout since had touched it. None of the three names matched
// anything in it, and every published image carried 3.1GB of that
// directory's Go module and build caches: an image of 5.8GB, half of it
// a cache for a layout this repository does not have, pulled by every
// deployment and copied back out of the image on every daemon start in
// host mode (setup.sh's extractor runs as an ExecStartPre, and that is
// what made grain-daemon.service miss its start timeout).
//
// Nothing else could see it. The size is not wrong in any way a build
// fails on, the tree is still a usable checkout, and the only symptom is
// minutes of pull and copy in somebody else's deployment. So the image
// is weighed here, and the shapes of build output that have no business
// travelling are named at any depth rather than at the root.
func TestTheImageCarriesNoBuildOutputUnderItsSource(t *testing.T) {
	requireImage(t)

	const root = "/usr/local/share/grain/src"
	script := `set -e
echo "megabytes=$(du -sm ` + root + ` | cut -f1)"
find ` + root + ` \( -name .git -o -name node_modules -o -name .container-cache \) -print
echo "largest:"
du -sm ` + root + `/* | sort -rn | head -5`

	out := docker(t, "run", "--rm", "--entrypoint", "sh", image, "-c", script)

	var megabytes int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "megabytes="):
			size, err := strconv.Atoi(strings.TrimPrefix(line, "megabytes="))
			if err != nil {
				t.Fatalf("du printed %q: %v", line, err)
			}
			megabytes = size
		case strings.HasPrefix(line, root):
			t.Errorf("%s is build output, not source, and the image carries it", line)
		}
	}

	if megabytes == 0 {
		t.Fatalf("could not measure the source the image carries: %s", out)
	}
	if megabytes > sourceExportMaxMB {
		t.Errorf("the image carries %dMB of source, over the %dMB ceiling -- every deployment pulls this, "+
			"and host mode copies it out of the image on every daemon start:\n%s",
			megabytes, sourceExportMaxMB, out)
	}
}

// The unit always passes --user with the host's own grain account.
//
// An image that only worked as root would leave every file it wrote into
// the mounted data directory owned by root -- see
// TestTheStoreComesOutOwnedByTheHostAccount below for the consequence this
// is the precondition of.
func TestTheImageRunsAsAnUnprivilegedUID(t *testing.T) {
	requireImage(t)
	out := strings.TrimSpace(docker(t,
		"run", "--rm", "--user", "1234:1234", "--entrypoint", "id", image, "-u"))
	if out != "1234" {
		t.Fatalf("id -u printed %q, want 1234", out)
	}
}

// --- the daemon, in the container --------------------------------------

func TestTheDaemonServesItsAPIFromTheContainer(t *testing.T) {
	requireImage(t)
	d := start(t, t.TempDir(), options{})

	status, raw := d.api(t, http.MethodGet, "/api/config", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/config: %d %s", status, raw)
	}

	// Real content, not just a 200: the actor the daemon files work as,
	// and the capability list the UI builds its panes from.
	var got config
	decode(t, raw, &got)
	if got.Actor == "" {
		t.Error("the daemon reports no actor")
	}
	if got.Capabilities == nil {
		t.Error("the daemon reports no capability list at all")
	}
}

func TestTasksCreatedThroughTheAPIComeBackOutOfTheStore(t *testing.T) {
	requireImage(t)
	d := start(t, t.TempDir(), options{})

	created := createTask(t, d, "a task filed inside a container")
	if created.State != "proposed" {
		t.Fatalf("a freshly filed task is in %q, want proposed", created.State)
	}

	status, raw := d.api(t, http.MethodGet, "/api/tasks", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/tasks: %d %s", status, raw)
	}
	var listed []task
	decode(t, raw, &listed)
	if !slicesContainsID(listed, created.ID) {
		t.Fatalf("%s is not in the list: %s", created.ID, raw)
	}

	status, raw = d.api(t, http.MethodGet, "/api/tasks/"+created.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/tasks/%s: %d %s", created.ID, status, raw)
	}
	var fetched task
	decode(t, raw, &fetched)
	if fetched.Title != "a task filed inside a container" {
		t.Fatalf("fetched title %q", fetched.Title)
	}

	// A write that is not a create: the conversation half of a task, which
	// lands in a different table of the same store.
	status, raw = d.api(t, http.MethodPost, "/api/tasks/"+created.ID+"/comments",
		map[string]string{"body": "a comment from the e2e suite"})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("commenting: %d %s", status, raw)
	}

	_, raw = d.api(t, http.MethodGet, "/api/tasks/"+created.ID, nil)
	decode(t, raw, &fetched)
	var found bool
	for _, comment := range fetched.Comments {
		if strings.Contains(comment.Body, "e2e suite") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the comment did not come back out of the store: %s", raw)
	}
}

// The whole reason the data directory is a bind mount.
//
// A container is disposable; the store, the secrets database and the
// deployment's own configuration are not. Replacing the container -- which
// is what every deploy and every upgrade does -- must leave all of it
// exactly where it was.
func TestATaskSurvivesTheContainerThatCreatedIt(t *testing.T) {
	requireImage(t)
	root := t.TempDir()

	first := start(t, root, options{})
	created := createTask(t, first, "a task that outlives its container")
	first.stop()

	// A genuinely new container over the same directories, the way a
	// `systemctl restart grain-daemon.service` brings one up.
	second := start(t, root, options{})
	if second.Name == first.Name {
		t.Fatal("the second container is the first one")
	}

	status, raw := second.api(t, http.MethodGet, "/api/tasks/"+created.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/tasks/%s from a new container: %d %s", created.ID, status, raw)
	}
	var fetched task
	decode(t, raw, &fetched)
	if fetched.Title != "a task that outlives its container" {
		t.Fatalf("fetched title %q", fetched.Title)
	}
}

// `docker run --user` is what keeps this true.
//
// Without it the daemon would run as root inside the container and every
// file it wrote into the mounted data directory would come out root-owned
// on the host -- unreadable by the account an operator's own `grain` CLI
// runs as, and a permission failure the moment anything outside the
// container touched it.
func TestTheStoreComesOutOwnedByTheHostAccount(t *testing.T) {
	requireImage(t)
	d := start(t, t.TempDir(), options{})
	createTask(t, d, "a task whose store file we stat")

	store := filepath.Join(d.Data, "store")
	var written []string
	waitFor(t, "a store file under "+store, 60*time.Second, func() error {
		var err error
		written, err = filesUnder(store)
		if err != nil {
			return err
		}
		if len(written) == 0 {
			return errNothingYet
		}
		return nil
	})

	for _, path := range written {
		owned, err := ownedByUs(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !owned {
			t.Errorf("%s is not ours", path)
		}
	}
}

// /usr/local/bin/grain is a wrapper around this exact invocation.
//
// install_cli_wrappers writes a script that runs the deployment image on
// the host network with GRAIN_SERVER pointed at the daemon's own port --
// so an operator's `grain list` and the daemon are the same build, talking
// over the loopback address they share. Like the wrapper, it hands the CLI
// GRAIN_DATA_DIR and mounts the data directory at that path: that is where
// the CLI finds the local API token a single-tenant daemon demands of
// every caller, loopback included.
func TestTheCLIReachesTheDaemonFromASecondContainer(t *testing.T) {
	requireImage(t)
	d := start(t, t.TempDir(), options{})
	createTask(t, d, "a task the CLI should list")

	out := docker(t, "run", "--rm", "--network", "host",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--env", "GRAIN_SERVER="+d.Base,
		"--env", "GRAIN_DATA_DIR="+d.Data,
		"--volume", d.Data+":"+d.Data,
		image, "-json", "list")

	var listed []task
	decode(t, []byte(out), &listed)
	for _, listedTask := range listed {
		if listedTask.Title == "a task the CLI should list" {
			return
		}
	}
	t.Fatalf("the CLI did not list the task: %s", out)
}

func slicesContainsID(tasks []task, id string) bool {
	for _, candidate := range tasks {
		if candidate.ID == id {
			return true
		}
	}
	return false
}
