package ci

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// The stamp a test deployment claims to be running. A real one reads
// this out of its own binary and a test binary carries no stamp at all
// (pkg/version), which is why Service.Build exists.
var testBuild = version.Info{
	Revision: "acc97d35b0f1e2d3c4b5a69788776655443322110",
	Time:     time.Date(2026, 9, 18, 11, 22, 33, 0, time.UTC),
}

func TestBuildOfIsAbsentRatherThanEmptyForAnUnstampedBinary(t *testing.T) {
	if got := BuildOf(version.Info{}); got != nil {
		t.Fatalf("an unstamped binary should record no build at all, got %+v", got)
	}
	// Half a stamp is still worth recording: a revision with no readable
	// commit time is what pkg/version hands back for one, and the
	// revision alone answers the question this field exists for.
	got := BuildOf(version.Info{Revision: "abc123", Modified: true})
	if got == nil || got.Revision != "abc123" || !got.Modified || got.CommittedAt != nil {
		t.Fatalf("build = %+v", got)
	}

	got = BuildOf(testBuild)
	if got == nil || got.Revision != testBuild.Revision || got.Modified {
		t.Fatalf("build = %+v", got)
	}
	if got.CommittedAt == nil || !got.CommittedAt.Equal(testBuild.Time) {
		t.Fatalf("committed at = %v, want %v", got.CommittedAt, testBuild.Time)
	}
}

// Describe is what a list of twenty runs says about each of them, so it
// is short -- but it has to carry the two things the reader is comparing
// against: enough of the revision to recognise, and the instant that
// decides whether this build predates the commit they have in mind.
func TestBuildDescribesItselfInOneClause(t *testing.T) {
	got := BuildOf(testBuild).Describe()
	if !strings.HasPrefix(got, "acc97d3") || strings.Contains(got, testBuild.Revision) {
		t.Errorf("description should shorten the revision: %q", got)
	}
	if !strings.Contains(got, "2026-09-18T11:22:33Z") {
		t.Errorf("description should say when that commit was made: %q", got)
	}
	if strings.Contains(got, "modified") {
		t.Errorf("a clean build should not claim a modified tree: %q", got)
	}
	dirty := Build{Revision: "abc123", Modified: true}.Describe()
	if !strings.Contains(dirty, "modified tree") {
		t.Errorf("a dirty build should say so: %q", dirty)
	}
}

// The point of the whole thing: a run says which build produced it, and
// still says so once it has finished -- the finished record replaces the
// started one wholesale (Service.Start's goroutine), which is exactly
// where a fact the engine does not know about gets lost.
func TestServiceStampsTheBuildThatProducedARun(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	svc.Build = func() version.Info { return testBuild }

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if started.Build == nil || started.Build.Revision != testBuild.Revision {
		t.Fatalf("started run = %+v, want the build that planned it", started.Build)
	}

	finished := waitForRun(t, svc, started.ID)
	if finished.Build == nil || finished.Build.Revision != testBuild.Revision {
		t.Fatalf("finished run = %+v, want the build that ran it", finished.Build)
	}
	if finished.Build.CommittedAt == nil || !finished.Build.CommittedAt.Equal(testBuild.Time) {
		t.Errorf("finished run's build lost its commit time: %+v", finished.Build)
	}
}

// A run that never got as far as a job is the one a reader is most
// likely to be asking this question about: grain/task-388's whole
// subject was a matchless run, whose behaviour changed between two
// builds and which nothing on the record could attribute.
func TestServiceStampsARunThatRanNothing(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/manual.yml": "on: workflow_dispatch\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	svc.Build = func() version.Info { return testBuild }

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "grain/task-388", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finished := waitForRun(t, svc, started.ID)
	if finished.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q", finished.Conclusion)
	}
	if finished.Build == nil || finished.Build.Revision != testBuild.Revision {
		t.Fatalf("a run with no jobs still says what made it: %+v", finished.Build)
	}

	// And so is a run whose workflows could not be read at all, which
	// never reaches the engine either (recordWorkflowFailure).
	broken := &Service{
		Source: SourceFunc(func(context.Context, string, string) ([]Workflow, string, error) {
			return LoadDir(fstest.MapFS{
				".grain/workflows/broken.yml": &fstest.MapFile{Data: []byte("on: [push\n")},
			})
		}),
		Engine: &Engine{Sandboxes: &definitionSandboxes{}},
		Build:  func() version.Info { return testBuild },
	}
	run, err := broken.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if run.Conclusion != ConclusionFailure {
		t.Fatalf("conclusion = %q", run.Conclusion)
	}
	if run.Build == nil || run.Build.Revision != testBuild.Revision {
		t.Fatalf("a workflow failure still says which build reached it: %+v", run.Build)
	}
}

// A Service nobody configured stamps the build it is actually running,
// which in a test binary is no build at all: absence is normal and is
// not an error (BuildOf).
func TestServiceReadsItsOwnBuildWhenNobodyWiresOne(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	want, got := BuildOf(version.Get()), svc.build()
	switch {
	case want == nil && got != nil:
		t.Fatalf("an unwired service invented a build: %+v", got)
	case want != nil && (got == nil || got.Revision != want.Revision):
		t.Fatalf("build = %+v, want this binary's own %+v", got, want)
	}
	run, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if run.Build != nil && run.Build.Revision == "" {
		t.Fatalf("an unstamped build should be absent, not present and blank: %+v", run.Build)
	}
}

// The stamp is worth nothing if it does not survive the process that
// wrote it: the run somebody is attributing next week is a row, read
// back by a daemon that is by then a different build entirely.
func TestAStoredRunRemembersTheBuildThatProducedIt(t *testing.T) {
	dir := t.TempDir()
	run := storedTestRun("3f2a1c", time.Now().UTC().Truncate(time.Millisecond))
	run.Conclusion = ConclusionSuccess
	run.Build = BuildOf(testBuild)
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(run)

	got, ok := storedTestStore(t, dir, StoredRunStoreOptions{}).Get("3f2a1c")
	if !ok {
		t.Fatal("the run was gone")
	}
	if got.Build == nil || got.Build.Revision != testBuild.Revision || got.Build.Modified {
		t.Fatalf("build came back as %+v", got.Build)
	}
	if got.Build.CommittedAt == nil || !got.Build.CommittedAt.Equal(testBuild.Time) {
		t.Fatalf("the commit time came back as %v", got.Build.CommittedAt)
	}

	// And a run no build stamped -- every run recorded before these
	// columns existed -- round-trips as absent rather than as a build
	// with nothing in it, which is a reader's cue that the record cannot
	// answer the question rather than that the answer is blank.
	unstamped := storedTestRun("7c6b5a", time.Now().UTC().Truncate(time.Millisecond))
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	runs.Put(unstamped)
	if got, ok := runs.Get("7c6b5a"); !ok || got.Build != nil {
		t.Fatalf("an unstamped run came back as %+v (%v)", got.Build, ok)
	}
}
