package upgrade

// The upgrade this package could not previously survive: one whose new
// build knows a different database schema than the deployment it is
// arriving at (grain/task-59).
//
// None of these tests needs a real image or a real database. The image
// is a shell script that answers `schema-version` and either accepts or
// refuses a `state check -migrate`, which is exactly the surface
// pkg/upgrade uses; what the answers mean to a deployment is
// cmd/grain's own schema-bump tests, against a real store and a real
// state repository.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedPrepare stands in for cmd/grain's writeSchemaUpgrade: it makes
// a directory and remembers what it was told about the schema change,
// so a test can assert both that the rows were written out and which
// two versions the upgrade thought it was moving between.
type recordedPrepare struct {
	mu        sync.Mutex
	dir       string
	branch    string
	from, to  int
	calls     int
	discarded int
	// fail, set before the upgrade starts, is what Prepare returns
	// instead of a directory -- an export this deployment could not
	// make.
	fail error
}

func (r *recordedPrepare) config(t *testing.T, version int) *SchemaConfig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "schema-upgrade")
	return &SchemaConfig{
		Version:     version,
		VersionArgs: []string{"schema-version"},
		CheckArgs:   []string{"state", "check", "-migrate"},
		Prepare: func(_ context.Context, branch string, from, to int) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.calls++
			r.dir, r.branch, r.from, r.to = dir, branch, from, to
			if r.fail != nil {
				return "", r.fail
			}
			return dir, os.MkdirAll(dir, 0o755)
		},
		Discard: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.discarded++
		},
	}
}

func (r *recordedPrepare) read() recordedPrepare {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recordedPrepare{dir: r.dir, branch: r.branch, from: r.from, to: r.to,
		calls: r.calls, discarded: r.discarded}
}

// fakeImage is a stand-in for `docker run --rm`: a shell script that
// answers as the image being upgraded to would. It prints `schema` when
// asked which schema it knows, and exits with checkExit when asked to
// rehearse a migration -- logging every invocation, with its whole
// argument list, to a file the test reads back.
func fakeImage(dir, schema string, checkExit int) []string {
	log := filepath.Join(dir, "image-runs")
	script := `printf '%s\n' "$*" >> ` + log + `
case "$*" in
*schema-version*) echo ` + schema + `;; 
*"state check -migrate"*) exit ` + strconv.Itoa(checkExit) + `;;
esac`
	return []string{"sh", "-c", script, "sh"}
}

func imageRuns(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "image-runs"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// A new build at the schema this deployment is already on changes
// nothing: no export, no rehearsal, and an upgrade that looks exactly
// like every upgrade did before this step existed. That is the ordinary
// case -- most releases do not touch the schema -- and it is worth
// pinning, because a step that wrote a deployment's whole database out
// on every upgrade would be a new cost on every release.
func TestAnUpgradeToTheSameSchemaWritesNothingOut(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	prepare := &recordedPrepare{}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     fakeImage(dir, "16", 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	if got := prepare.read().calls; got != 0 {
		t.Errorf("the upgrade wrote this deployment's rows out %d time(s) for a schema that did not change", got)
	}
	if got := stepByName(t, status, stepSchema).Detail; !strings.Contains(got, "unchanged") {
		t.Errorf("schema step detail = %q, want it to say the schema is unchanged", got)
	}
	if got, want := readFile(t, refFile), "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
}

// A new build at another schema: the deployment's rows are written out
// before anything cuts over, the new build is asked to import them at
// its own schema, and only then is the ref file repointed. The order is
// the point -- an upgrade that cut over first would have nothing left
// running that could read those rows.
func TestAnUpgradeToANewSchemaWritesTheRowsOutAndHasTheNewBuildImportThem(t *testing.T) {
	shortRestartGrace(t)
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")
	prepare := &recordedPrepare{}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     fakeImage(dir, "17", 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("grain/task-59"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	got := prepare.read()
	if got.calls != 1 {
		t.Fatalf("the upgrade wrote this deployment's rows out %d time(s), want once", got.calls)
	}
	if got.from != 16 || got.to != 17 {
		t.Errorf("rows written out for a migration from %d to %d, want 16 to 17", got.from, got.to)
	}
	if got.branch != "grain/task-59" {
		t.Errorf("rows written out for branch %q, want the branch being upgraded onto", got.branch)
	}
	if got.discarded != 0 {
		t.Errorf("the upgrade discarded the rows it had written out, %d time(s), on its way to succeeding",
			got.discarded)
	}

	// The rehearsal ran in the new image, against that directory, with
	// it mounted read-only at the path it has on this host -- the whole
	// reason a container can check a migration for a host it cannot
	// otherwise see.
	var rehearsal string
	for _, run := range imageRuns(t, dir) {
		if strings.Contains(run, "state check -migrate") {
			rehearsal = run
		}
	}
	if rehearsal == "" {
		t.Fatalf("the new image was never asked to import this deployment's rows; it ran: %v", imageRuns(t, dir))
	}
	if want := "--volume " + got.dir + ":" + got.dir + ":ro"; !strings.Contains(rehearsal, want) {
		t.Errorf("rehearsal ran %q, want it to mount %q", rehearsal, want)
	}
	if !strings.HasSuffix(rehearsal, "state check -migrate "+got.dir) {
		t.Errorf("rehearsal ran %q, want it to end by checking %s", rehearsal, got.dir)
	}

	// And the cut-over happened, with the status saying what it is that
	// the operator will find on the other side of the restart.
	if got, want := readFile(t, refFile), "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:grain..task-59@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
	if !strings.Contains(status.Detail, "schema 16 to 17") {
		t.Errorf("status detail = %q, want it to say which schema change this upgrade is", status.Detail)
	}
	waitForFile(t, restartMarker)
}

// The failure this whole step exists for: a build that is perfectly
// healthy on its own and cannot take this deployment's rows. It is
// caught before the cut-over, so the deployment goes on running the
// build that can -- where before this it was caught by systemd, after
// the cut-over, as a restart loop with the UI down and no rollback
// possible.
func TestAnUpgradeIsRefusedWhenTheNewBuildCannotTakeTheRows(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")
	prepare := &recordedPrepare{}

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     fakeImage(dir, "17", 1),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("grain/task-59"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q after a refused upgrade, want it left at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("the deployment was restarted onto a build that could not read its rows")
	}
	if got := prepare.read().discarded; got != 1 {
		t.Errorf("the rows written out for the migration were discarded %d time(s), want once: a note left "+
			"behind a refused upgrade would migrate some later restart onto stale rows", got)
	}
	if !strings.Contains(status.Detail, "could not take this deployment's rows") {
		t.Errorf("status detail = %q, want it to say the new build could not take these rows", status.Detail)
	}
}

// The other way the export can end an upgrade: it fails outright, and
// this deployment's rows were never written down. Nothing cuts over,
// which is the same answer as above -- but the note is *not* discarded,
// and that difference is the point (grain/task-255).
//
// Discard removes the whole schema-upgrade directory, and a Prepare that
// failed before it cleared that directory -- a permissions problem, a
// busy mount -- leaves an *earlier* upgrade's note in it, possibly still
// waiting for the restart that spends it. Nothing here can tell that
// apart from a half-written export of this upgrade's own. So cleaning up
// a failed export is the exporter's job, where it knows how far it got
// (cmd/grain's writeSchemaUpgrade), and this step does not touch what it
// did not write.
func TestAFailedExportDoesNotDiscardWhatIsAlreadyOnTheHost(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")
	prepare := &recordedPrepare{fail: errors.New("clearing /var/lib/grain/data/schema-upgrade: permission denied")}

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     fakeImage(dir, "17", 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("grain/task-59"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q after an upgrade whose export failed, want it left at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("the deployment was restarted for an upgrade whose rows were never written out")
	}
	if got := prepare.read().discarded; got != 0 {
		t.Errorf("an export that failed discarded the schema-upgrade directory %d time(s), want none: what "+
			"is there may be an earlier upgrade's note, still pending a restart", got)
	}
	if !strings.Contains(status.Detail, "permission denied") {
		t.Errorf("status detail = %q, want it to say why the rows could not be written out", status.Detail)
	}
}

// A build that will not say which schema it knows is one this upgrade
// cannot reason about, and cutting over on a shrug is precisely how the
// failure this step exists for happened. So it is refused, with nothing
// written out and nothing changed.
func TestAnUpgradeIsRefusedWhenTheNewBuildWillNotSayWhichSchemaItKnows(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	prepare := &recordedPrepare{}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// Answers the health check (it runs, and prints something)
			// and answers the schema question with something that is not
			// a version.
			RunCmd: fakeImage(dir, "unknown", 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if _, err := os.Stat(refFile); err == nil {
		t.Error("the ref file was written for a build that would not say which schema it knows")
	}
	if got := prepare.read().calls; got != 0 {
		t.Errorf("this deployment's rows were written out %d time(s) for a build that never named a schema", got)
	}
	if !strings.Contains(status.Detail, "not a schema version") {
		t.Errorf("status detail = %q, want it to say what the new build answered", status.Detail)
	}
}

// The source pipeline reaches the same wall one step later, because the
// build it is asking only exists once it is installed -- so a refusal
// there has an install to undo, and undoes it. What must come back up
// after the restart is the binary whose store this still is.
func TestASourceUpgradeRollsBackWhenTheNewBinaryCannotTakeTheRows(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	dir := t.TempDir()
	installPath := filepath.Join(dir, "grain")
	const previous = "#!/bin/sh\necho 16\n"
	if err := os.WriteFile(installPath, []byte(previous), 0o755); err != nil {
		t.Fatal(err)
	}
	prepare := &recordedPrepare{}

	// The binary the build produces: a new schema, and an import it
	// refuses.
	built := "#!/bin/sh\ncase \"$*\" in *check*) exit 1;; *) echo 17;; esac\n"
	u := newUpgrader(t, Config{
		SrcDir: checkout,
		BuildCmd: []string{"sh", "-c",
			"mkdir -p bin && printf '%s' " + shellQuote(built) + " > bin/grain && chmod +x bin/grain"},
		BuiltBinary:     filepath.Join(checkout, "bin", "grain"),
		InstallPath:     installPath,
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseFailed)

	if got := readFile(t, installPath); got != previous {
		t.Errorf("installed binary = %q after a refused upgrade, want the previous one back:\n%q", got, previous)
	}
	if got := prepare.read().discarded; got != 1 {
		t.Errorf("the rows written out were discarded %d time(s), want once", got)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fakeImageThatStopsAnsweringTheSchemaQuestion is the shape of the
// failure this test exists for: an image that answers `schema-version`
// once -- so the health check passes and the upgrade goes on -- and then
// cannot be run again, the way a host whose container runtime has run
// out of disk part-way through an upgrade cannot run it again.
//
// The count is kept in a file rather than a variable because the "image"
// is a shell script, and each invocation of it is a fresh process.
func fakeImageThatStopsAnsweringTheSchemaQuestion(dir, complaint string) []string {
	asks := filepath.Join(dir, "schema-asks")
	script := `printf x >> ` + asks + `
if [ "$(wc -c < ` + asks + `)" -gt 1 ]; then
  echo ` + shellQuote(complaint) + ` >&2
  exit 125
fi
echo 16`
	return []string{"sh", "-c", script, "sh"}
}

// A build that cannot be asked which schema it knows fails the upgrade,
// and what it said while failing is what the operator needs: the same
// question succeeded moments earlier as the health check, so the reason
// it stopped working is about this host and is in that output and
// nowhere else. Before this it was thrown away by the call that ran the
// command, and the pane said only "asking the build being upgraded to
// which schema it knows: ... exit status 125".
func TestAFailedSchemaQuestionReportsWhatTheBuildPrinted(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	prepare := &recordedPrepare{}
	const complaint = "docker: failed to create task: no space left on device"

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     fakeImageThatStopsAnsweringTheSchemaQuestion(dir, complaint),
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if !strings.Contains(status.Detail, complaint) {
		t.Errorf("status detail = %q, want it to carry what the build printed: %q", status.Detail, complaint)
	}
	if got := stepByName(t, status, stepSchema).State; got != StepFailed {
		t.Errorf("schema step state = %q, want %q", got, StepFailed)
	}
	if _, err := os.Stat(refFile); err == nil {
		t.Error("the ref file was written for a build this upgrade could not question")
	}
	if got := prepare.read().calls; got != 0 {
		t.Errorf("this deployment's rows were written out %d time(s) for a build that never answered", got)
	}
}

// The same failure with nothing printed at all: the error still has to
// say so, rather than trailing off after a colon into an empty string
// that reads like output was lost on the way to the pane.
func TestASilentFailedSchemaQuestionSaysTheBuildPrintedNothing(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	prepare := &recordedPrepare{}
	asks := filepath.Join(dir, "schema-asks")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c", `printf x >> ` + asks + `
if [ "$(wc -c < ` + asks + `)" -gt 1 ]; then exit 3; fi
echo 16`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          prepare.config(t, 16),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if !strings.Contains(status.Detail, "printed nothing") {
		t.Errorf("status detail = %q, want it to say the build printed nothing", status.Detail)
	}
}

// A build that never answers at all is the other half of the same
// problem: killing it produces "signal: killed", which is what a
// segfaulting binary looks like too. The bound is what actually stopped
// it, so the bound is what the error says.
func TestAQuestionTheBuildNeverAnswersReportsTheBound(t *testing.T) {
	// A progress of its own rather than a whole upgrade, so this is what
	// claims the log for this test (log_test.go).
	logToTest(t)
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	p := newProgress(statusFile, "main", Status{Branch: "main", Phase: PhaseRunning}, []string{stepSchema})
	defer p.stop()
	p.start(stepSchema)

	_, err := runAsked(context.Background(), p, stepSchema, 50*time.Millisecond,
		[]string{"sh", "-c", "sleep 30"})
	if err == nil {
		t.Fatal("runAsked returned no error for a command that never answered")
	}
	if !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Errorf("error = %q, want it to name the bound that stopped the command", err)
	}
}
