package ci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The collection half of these tests runs the real collection script in
// a real shell against a real directory, rather than against a fake
// sandbox answering from a table.
//
// That is deliberate, and it is where the risk in this feature actually
// lives: everything above the script is ordinary Go, and the script
// itself is the part that has to survive `set -e` from the transport,
// paths that do not exist, a tar that fails, and a payload arriving
// through a channel that also carries stderr. A test that handed the
// engine a canned answer would exercise none of that.
type bashSandbox struct {
	name string
	dir  string
	// commands records every script it was asked to run, so a test can
	// assert that a job which declares nothing is asked nothing.
	commands []string
}

func (b *bashSandbox) Name() string { return b.name }

func (b *bashSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	b.commands = append(b.commands, cmd.Script)
	dir := b.dir
	if cmd.Dir != "" {
		dir = filepath.Join(dir, cmd.Dir)
	}
	// `set -e` in front, which is what both of grain's real transports do
	// to a script they are given (ci.Command.Script says so): a
	// collection script that cannot survive it would pass here and fail
	// in a deployment.
	c := exec.CommandContext(ctx, "bash", "-c", "set -e\n"+cmd.Script)
	c.Dir = dir
	for k, v := range cmd.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	out, err := c.CombinedOutput()
	result := CommandResult{Output: string(out)}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case asExitError(err, &exit):
		result.ExitCode = exit.ExitCode()
	default:
		return result, err
	}
	return result, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func (b *bashSandbox) Release(context.Context) error { return nil }

type bashSandboxes struct {
	dir  string
	last *bashSandbox
}

func (b *bashSandboxes) Acquire(_ context.Context, req SandboxRequest) (Sandbox, error) {
	b.last = &bashSandbox{name: req.Name, dir: b.dir}
	return b.last, nil
}

func requireShellTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "tar", "base64"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("this test collects artifacts with the real script, and %s is not on PATH", tool)
		}
	}
}

// artifactPlan is one job declaring one artifact, as the engine takes
// it. The steps are the caller's, so a test can decide whether the job
// passes or fails.
func artifactPlan(t *testing.T, spec ArtifactSpec, steps []Step) Plan {
	t.Helper()
	job := Job{ID: "ui-e2e", Name: "ui e2e", Artifacts: []ArtifactSpec{spec}, Steps: steps}
	return Plan{
		Request: Request{Repo: "acme/widgets", Ref: "main", Commit: "c0ffee", Event: EventPush},
		Jobs: []PlannedJob{{
			ID: "tests.yml:ui-e2e", Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml",
			Job: job, Key: Fingerprint(Request{Repo: "acme/widgets"}, Workflow{Path: "tests.yml"}, job),
		}},
	}
}

func openDirArtifacts(t *testing.T, opts DirArtifactsOptions) *DirArtifacts {
	t.Helper()
	return openDirArtifactsIn(t, t.TempDir(), opts)
}

// openDirArtifactsIn is the same, for a test that needs to look in the
// directory itself rather than only through the store.
func openDirArtifactsIn(t *testing.T, dir string, opts DirArtifactsOptions) *DirArtifacts {
	t.Helper()
	store, err := OpenDirArtifacts(dir, opts)
	if err != nil {
		t.Fatalf("OpenDirArtifacts: %v", err)
	}
	return store
}

// TestAFailingJobsFilesOutliveItsSandbox is grain/task-182's whole
// complaint, as a test: a job fails, the file that says why is inside a
// machine that is about to be destroyed, and afterwards somebody can
// still read it.
func TestAFailingJobsFilesOutliveItsSandbox(t *testing.T) {
	requireShellTools(t)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "ui", "test-results"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := openDirArtifacts(t, DirArtifactsOptions{})
	engine := &Engine{Sandboxes: &bashSandboxes{dir: workspace}, Artifacts: store}

	run := engine.Run(context.Background(), "run1", artifactPlan(t,
		ArtifactSpec{Name: "playwright-test-results", Paths: []string{"ui/test-results"}},
		[]Step{{Name: "Test e2e", Run: "printf 'a TypeError unmounted the tree' > ui/test-results/trace.txt\nexit 1"}},
	), nil)

	if run.Conclusion != ConclusionFailure {
		t.Fatalf("conclusion = %q, want failure", run.Conclusion)
	}
	artifacts := run.Jobs[0].Artifacts
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want one", artifacts)
	}
	got := artifacts[0]
	if !got.Stored || got.Bytes == 0 || got.Note != "" {
		t.Fatalf("artifact = %+v, want a stored one with no note", got)
	}
	if got.Run != "run1" || got.Job != "ui-e2e" || got.Name != "playwright-test-results" {
		t.Fatalf("artifact = %+v, want it named after the run and the job", got)
	}

	// And it is really the file, not just a record of one.
	body, _, err := store.Open(context.Background(), got.Ref())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer body.Close()
	files := readTarGz(t, body)
	if files["ui/test-results/trace.txt"] != "a TypeError unmounted the tree" {
		t.Fatalf("the archive holds %v, want the file the job wrote", files)
	}

	// The store agrees with the run, which is what the API serves from.
	listed, err := store.List(context.Background(), "run1")
	if err != nil || len(listed) != 1 || listed[0].Name != "playwright-test-results" {
		t.Fatalf("List = %+v, %v", listed, err)
	}
}

// TestASuccessfulJobKeepsNothing is the policy, and the reason the
// declaration can sit in a workflow file permanently: a green job costs
// this feature nothing at all, not even the command that would ask.
func TestASuccessfulJobKeepsNothing(t *testing.T) {
	requireShellTools(t)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "ui", "test-results"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := openDirArtifacts(t, DirArtifactsOptions{})
	sandboxes := &bashSandboxes{dir: workspace}
	engine := &Engine{Sandboxes: sandboxes, Artifacts: store}

	run := engine.Run(context.Background(), "run1", artifactPlan(t,
		ArtifactSpec{Name: "traces", Paths: []string{"ui/test-results"}},
		[]Step{{Name: "Test e2e", Run: "touch ui/test-results/trace.txt"}},
	), nil)

	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q, want success", run.Conclusion)
	}
	if got := run.Jobs[0].Artifacts; len(got) != 0 {
		t.Fatalf("artifacts = %+v, want none from a job that passed", got)
	}
	if len(sandboxes.last.commands) != 1 {
		t.Fatalf("the sandbox ran %d commands, want only the step itself", len(sandboxes.last.commands))
	}
	if entries, err := os.ReadDir(store.Dir()); err != nil || len(entries) != 0 {
		t.Fatalf("the store holds %v (%v), want nothing", entries, err)
	}
}

// TestNothingMatchedIsRecordedRatherThanSilent: the ordinary shape of a
// test suite that only writes its directory when something fails. The
// reader has to be told, because "no artifact" and "the feature is
// broken" look identical otherwise.
func TestNothingMatchedIsRecordedRatherThanSilent(t *testing.T) {
	requireShellTools(t)
	store := openDirArtifacts(t, DirArtifactsOptions{})
	engine := &Engine{Sandboxes: &bashSandboxes{dir: t.TempDir()}, Artifacts: store}

	run := engine.Run(context.Background(), "run1", artifactPlan(t,
		ArtifactSpec{Name: "traces", Paths: []string{"ui/test-results", "ui/playwright-report"}},
		[]Step{{Name: "Test e2e", Run: "exit 3"}},
	), nil)

	artifacts := run.Jobs[0].Artifacts
	if len(artifacts) != 1 || artifacts[0].Stored {
		t.Fatalf("artifacts = %+v, want one that was not stored", artifacts)
	}
	if !strings.Contains(artifacts[0].Note, "ui/test-results") ||
		!strings.Contains(artifacts[0].Note, "nothing matched") {
		t.Fatalf("note = %q, want it to name the paths that did not match", artifacts[0].Note)
	}
}

// TestAnOversizedArtifactIsRefusedInsideTheSandbox: the bound that
// matters most, because enforcing it on this side would mean copying the
// very thing the bound exists to refuse.
func TestAnOversizedArtifactIsRefusedInsideTheSandbox(t *testing.T) {
	requireShellTools(t)
	workspace := t.TempDir()
	store := openDirArtifacts(t, DirArtifactsOptions{MaxArtifactBytes: 1 << 10})
	engine := &Engine{Sandboxes: &bashSandboxes{dir: workspace}, Artifacts: store}

	run := engine.Run(context.Background(), "run1", artifactPlan(t,
		ArtifactSpec{Name: "traces", Paths: []string{"big"}},
		// Random bytes: gzip must not be able to shrink this back under
		// the bound, which is what the test is about.
		[]Step{{Name: "Test e2e", Run: "head -c 400000 /dev/urandom > big\nexit 1"}},
	), nil)

	artifacts := run.Jobs[0].Artifacts
	if len(artifacts) != 1 || artifacts[0].Stored {
		t.Fatalf("artifacts = %+v, want one that was not stored", artifacts)
	}
	if !strings.Contains(artifacts[0].Note, "not copied out of the sandbox") {
		t.Fatalf("note = %q, want it to say the bytes never left the sandbox", artifacts[0].Note)
	}
	listed, _ := store.List(context.Background(), "run1")
	if len(listed) != 0 {
		t.Fatalf("List = %+v, want nothing kept", listed)
	}
}

// TestArtifactsAreNotCached: a cache hit is a fact about the job, and
// these files are a fact about one run of it -- kept in a directory with
// a sweep of its own, under the run id that produced them.
func TestArtifactsAreNotCached(t *testing.T) {
	cache, err := OpenDirCache(t.TempDir(), DirCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	result := JobRun{
		Name: "ui e2e", Conclusion: ConclusionFailure,
		Steps:     []StepRun{{Name: "Test e2e", Conclusion: ConclusionFailure, ExitCode: 1}},
		Artifacts: []Artifact{{Run: "run1", Job: "ui-e2e", Name: "traces", Stored: true, Bytes: 10, CollectedAt: &now}},
	}
	if err := cache.Save(context.Background(), Key("job:abc"), result); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := cache.Lookup(context.Background(), Key("job:abc"))
	if err != nil || !ok {
		t.Fatalf("Lookup: %v, %v", ok, err)
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("a cache hit offered %+v, want no artifacts", got.Artifacts)
	}
}

func TestDirArtifactsBoundsOneRun(t *testing.T) {
	store := openDirArtifacts(t, DirArtifactsOptions{MaxArtifactBytes: 1 << 10, MaxRunBytes: 2 << 10})
	body := make([]byte, 900)
	for i, name := range []string{"one", "two", "three"} {
		saved, err := store.Save(context.Background(), ArtifactRef{Run: "run1", Job: "job", Name: name}, body)
		if err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
		// Two fit under 2KB and the third does not.
		if want := i < 2; saved.Stored != want {
			t.Fatalf("%s: stored = %v, want %v (note %q)", name, saved.Stored, want, saved.Note)
		}
		if i == 2 && !strings.Contains(saved.Note, "one run may have") {
			t.Fatalf("note = %q, want it to name the per-run bound", saved.Note)
		}
	}
}

func TestDirArtifactsSweepsByAgeAndBySize(t *testing.T) {
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := OpenDirArtifacts(dir, DirArtifactsOptions{
		MaxAge: time.Hour, MaxBytes: 4 << 10, MaxArtifactBytes: 2 << 10, MaxRunBytes: 2 << 10,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	save := func(run string) {
		t.Helper()
		if _, err := store.Save(context.Background(), ArtifactRef{Run: run, Job: "job", Name: "traces"}, make([]byte, 1500)); err != nil {
			t.Fatalf("Save %s: %v", run, err)
		}
	}
	save("old")
	// Age is read from the files' own mtimes, since that is what a sweep
	// can afford to look at; pushed back by hand here rather than by
	// waiting an hour.
	stale := clock.Add(-2 * time.Hour)
	_ = filepath.Walk(filepath.Join(dir, "old"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			return os.Chtimes(path, stale, stale)
		}
		return err
	})
	save("a")
	save("b")
	save("c")

	clock = clock.Add(10 * time.Minute)
	store.Sweep()

	for _, run := range []string{"old", "a"} {
		listed, _ := store.List(context.Background(), run)
		if len(listed) != 0 {
			t.Fatalf("run %s still holds %+v; the stale one goes on age and the oldest of the rest on size", run, listed)
		}
	}
	for _, run := range []string{"b", "c"} {
		listed, _ := store.List(context.Background(), run)
		if len(listed) != 1 {
			t.Fatalf("run %s holds %+v, want its artifact kept", run, listed)
		}
	}
}

// TestArtifactRefsCannotReachOutOfTheStore: a ref arrives from a URL,
// and this is the check between it and the daemon's filesystem.
func TestArtifactRefsCannotReachOutOfTheStore(t *testing.T) {
	store := openDirArtifacts(t, DirArtifactsOptions{})
	for _, ref := range []ArtifactRef{
		{Run: "../../etc", Job: "job", Name: "passwd"},
		{Run: "run1", Job: "..", Name: "passwd"},
		{Run: "run1", Job: "job", Name: "../../../etc/passwd"},
		{Run: "run1", Job: "job", Name: "trace.tar.gz"},
		{Run: "", Job: "job", Name: "traces"},
	} {
		if _, err := store.Save(context.Background(), ref, []byte("x")); err == nil {
			t.Fatalf("Save(%+v) was accepted", ref)
		}
		if _, _, err := store.Open(context.Background(), ref); err == nil {
			t.Fatalf("Open(%+v) was accepted", ref)
		}
	}
}

func TestParseReadsArtifacts(t *testing.T) {
	w, err := Parse(".grain/workflows/tests.yml", []byte(`
name: tests
on: push
jobs:
  ui-e2e:
    artifacts:
      - name: playwright-test-results
        path: ui/test-results
      - name: reports
        path: |
          ui/playwright-report
          ui/coverage
    steps:
      - run: make test-e2e
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := w.Jobs[0].Artifacts
	if len(got) != 2 {
		t.Fatalf("artifacts = %+v, want two", got)
	}
	if got[0].Name != "playwright-test-results" || len(got[0].Paths) != 1 || got[0].Paths[0] != "ui/test-results" {
		t.Fatalf("artifacts[0] = %+v", got[0])
	}
	if len(got[1].Paths) != 2 || got[1].Paths[1] != "ui/coverage" {
		t.Fatalf("artifacts[1] = %+v, want the multi-line path read as two", got[1])
	}
}

func TestParseRefusesUnusableArtifacts(t *testing.T) {
	for name, artifacts := range map[string]string{
		"no name":      "      - path: ui/test-results\n",
		"no path":      "      - name: traces\n",
		"absolute":     "      - name: traces\n        path: /etc\n",
		"climbing out": "      - name: traces\n        path: ../../etc\n",
		"a glob":       "      - name: traces\n        path: ui/test-results/*.zip\n",
		"a shell":      "      - name: traces\n        path: ui/$(whoami)\n",
		"a bad name":   "      - name: ../traces\n        path: ui/test-results\n",
		"two of one":   "      - name: traces\n        path: a\n      - name: traces\n        path: b\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(".grain/workflows/tests.yml", []byte(
				"name: tests\non: push\njobs:\n  ui-e2e:\n    artifacts:\n"+artifacts+"    steps:\n      - run: make\n"))
			if err == nil {
				t.Fatalf("Parse accepted %s", name)
			}
		})
	}
}

// TestDeclaringAnArtifactChangesTheJobsKey: without this, adding
// `artifacts:` to a job whose failure is already cached would be a
// declaration that never takes effect.
func TestDeclaringAnArtifactChangesTheJobsKey(t *testing.T) {
	req := Request{Repo: "acme/widgets", Ref: "main", Commit: "c0ffee", Event: EventPush}
	w := Workflow{Path: ".grain/workflows/tests.yml", Name: "tests"}
	plain := Job{ID: "ui-e2e", Steps: []Step{{Run: "make test-e2e"}}}
	withArtifacts := plain
	withArtifacts.Artifacts = []ArtifactSpec{{Name: "traces", Paths: []string{"ui/test-results"}}}
	if Fingerprint(req, w, plain) == Fingerprint(req, w, withArtifacts) {
		t.Fatal("declaring an artifact left the job's key unchanged")
	}
}

func readTarGz(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	files := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		files[filepath.Clean(header.Name)] = string(body)
	}
}

// Clearing is what the disk-floor reclaim reaches for last
// (grain/task-213): everything of ours goes, nothing that is not ours is
// touched, and the caller is told how many bytes it got back so a
// journal line can say so.
func TestDirArtifactsClearReportsWhatItFreed(t *testing.T) {
	dir := t.TempDir()
	store := openDirArtifactsIn(t, dir, DirArtifactsOptions{MaxArtifactBytes: 4 << 10, MaxRunBytes: 8 << 10})
	for _, run := range []string{"run1", "run2"} {
		for _, name := range []string{"traces", "logs"} {
			if _, err := store.Save(context.Background(), ArtifactRef{Run: run, Job: "job", Name: name}, make([]byte, 1000)); err != nil {
				t.Fatalf("Save %s/%s: %v", run, name, err)
			}
		}
	}
	// Somebody else's file in the same directory, for the reason the
	// sweep leaves one alone: a directory mistyped in the wiring should
	// not become a directory this deletes.
	notOurs := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notOurs, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := store.Clear(context.Background())
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if removed != 4 {
		t.Errorf("Clear removed %d artifacts, want the 4 that were saved", removed)
	}
	if freed != 4000 {
		t.Errorf("Clear freed %d bytes, want 4000 -- the journal line is only as good as this figure", freed)
	}
	for _, run := range []string{"run1", "run2"} {
		if listed, _ := store.List(context.Background(), run); len(listed) != 0 {
			t.Errorf("run %s still holds %+v after a clear", run, listed)
		}
	}
	if _, err := os.Stat(notOurs); err != nil {
		t.Errorf("a file this store never wrote was removed: %v", err)
	}

	// And an empty store clears to nothing rather than to an error: a
	// reclaim runs this every time it gets far enough down its list.
	if removed, freed, err := store.Clear(context.Background()); err != nil || removed != 0 || freed != 0 {
		t.Errorf("clearing an empty store = %d, %d, %v; want 0, 0, nil", removed, freed, err)
	}
}

// The reading half of that store, which is the half an operator is
// offered: what is kept and what bounds it, so the System pane's Storage
// section can say "1 GiB of 2 GiB" rather than a number with nothing
// behind it (ui.CIArtifactStorage, grain/task-276). Three-state for
// ClearArtifacts' reason -- a deployment that keeps no artifacts at all
// and one whose store is empty are different facts, and a pane showing
// both as 0 B would send an operator hunting for disk that is not there.
func TestServiceArtifactStatsSaysWhetherThereIsAStoreToMeasure(t *testing.T) {
	store := openDirArtifacts(t, DirArtifactsOptions{})
	if _, err := store.Save(context.Background(), ArtifactRef{Run: "run1", Job: "job", Name: "traces"}, make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Engine: &Engine{Artifacts: store}}
	stats, ok, err := svc.ArtifactStats(context.Background())
	if err != nil || !ok {
		t.Fatalf("ArtifactStats = %v, %v; want a reading", ok, err)
	}
	if stats.Artifacts != 1 || stats.Runs != 1 || stats.Bytes != 512 {
		t.Errorf("stats = %+v, want the one artifact that is stored", stats)
	}
	if stats.MaxBytes != DefaultArtifactMaxBytes || stats.MaxAge != DefaultArtifactMaxAge {
		t.Errorf("bounds = %+v, want the ones in force beside the reading", stats)
	}

	none := &Service{Engine: &Engine{}}
	if _, ok, err := none.ArtifactStats(context.Background()); ok || err != nil {
		t.Errorf("a deployment that keeps no artifacts reports ok=%v, err=%v", ok, err)
	}
}

// The service's own three-state answer: how much went, and whether there
// was a store to empty at all.
func TestServiceClearArtifactsSaysWhetherThereWasAStore(t *testing.T) {
	store := openDirArtifacts(t, DirArtifactsOptions{})
	if _, err := store.Save(context.Background(), ArtifactRef{Run: "run1", Job: "job", Name: "traces"}, make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Engine: &Engine{Artifacts: store}}
	removed, freed, ok, err := svc.ClearArtifacts(context.Background())
	if err != nil || !ok || removed != 1 || freed != 512 {
		t.Fatalf("ClearArtifacts = %d, %d, %v, %v; want 1, 512, true, nil", removed, freed, ok, err)
	}

	none := &Service{Engine: &Engine{}}
	if _, _, ok, err := none.ClearArtifacts(context.Background()); ok || err != nil {
		t.Errorf("a deployment that keeps no artifacts reports ok=%v, err=%v", ok, err)
	}
}
