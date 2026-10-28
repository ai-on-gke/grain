package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestLoadDirReadsEveryWorkflowAndNothingElse(t *testing.T) {
	tree := fstest.MapFS{
		".grain/workflows/b.yml":     &fstest.MapFile{Data: []byte("on: push\njobs:\n  b:\n    steps:\n      - run: true\n")},
		".grain/workflows/a.yaml":    &fstest.MapFile{Data: []byte("on: push\njobs:\n  a:\n    steps:\n      - run: true\n")},
		".grain/workflows/README.md": &fstest.MapFile{Data: []byte("not a workflow")},
		".grain/workflows/.keep":     &fstest.MapFile{Data: []byte("")},
		".github/workflows/c.yml":    &fstest.MapFile{Data: []byte("this is GitHub's, and is not read")},
	}
	workflows, _, err := LoadDir(tree)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	var paths []string
	for _, w := range workflows {
		paths = append(paths, w.Path)
	}
	if strings.Join(paths, ",") != ".grain/workflows/a.yaml,.grain/workflows/b.yml" {
		t.Fatalf("loaded %v", paths)
	}
}

func TestLoadDirIsQuietAboutARepositoryThatDeclaresNothing(t *testing.T) {
	workflows, _, err := LoadDir(fstest.MapFS{"README.md": &fstest.MapFile{Data: []byte("hi")}})
	if err != nil || len(workflows) != 0 {
		t.Fatalf("got %d workflows, %v", len(workflows), err)
	}

	// Including a tree whose `.grain/workflows` exists and holds no
	// workflow file, where there is no `.github/workflows` to fall back
	// to: no workflow file anywhere is the empty answer PlanRun is
	// deliberately silent about, and this tree has none.
	workflows, _, err = LoadDir(fstest.MapFS{
		".grain/workflows/README.md": &fstest.MapFile{Data: []byte("how to add one")},
	})
	if err != nil || len(workflows) != 0 {
		t.Fatalf("got %d workflows, %v", len(workflows), err)
	}
}

// A `.grain/workflows` that holds no workflow file overrides nothing: it
// names nothing to run in GitHub's files' place, and reading it as "this
// repository declares no workflows" hid a whole `.github/workflows` suite
// behind the quiet empty plan PlanRun keeps for a repository with no CI
// at all (LoadDir, grain/task-254's third case).
func TestLoadDirFallsBackWhenTheGrainDirectoryDeclaresNothing(t *testing.T) {
	github := "on: push\njobs:\n  c:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n"
	for _, tc := range []struct {
		name string
		tree fstest.MapFS
	}{
		{"only a readme", fstest.MapFS{
			".grain/workflows/README.md": &fstest.MapFile{Data: []byte("how to add one")},
			".github/workflows/ci.yml":   &fstest.MapFile{Data: []byte(github)},
		}},
		{"a renamed workflow", fstest.MapFS{
			".grain/workflows/tests.yml.disabled": &fstest.MapFile{Data: []byte("on: push\njobs: {}\n")},
			".github/workflows/ci.yml":            &fstest.MapFile{Data: []byte(github)},
		}},
		{"a dotfile", fstest.MapFS{
			".grain/workflows/.keep":   &fstest.MapFile{Data: []byte("")},
			".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(github)},
		}},
		{"only a subdirectory", fstest.MapFS{
			".grain/workflows/shared/tests.yml": &fstest.MapFile{Data: []byte("on: push\njobs: {}\n")},
			".github/workflows/ci.yml":          &fstest.MapFile{Data: []byte(github)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflows, _, err := LoadDir(tc.tree)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			if len(workflows) != 1 || workflows[0].Path != ".github/workflows/ci.yml" {
				t.Fatalf("a directory that declares nothing shadowed GitHub's files: %+v", workflows)
			}
		})
	}
}

// The same thing said as a run rather than as a load, through the source
// a real checkout is read by: the shadowed repository used to plan no
// jobs at all, and a plan with no jobs and nothing to report is the quiet
// success that merges an untested tree.
func TestAGrainDirectoryThatDeclaresNothingDoesNotEmptyTheRun(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		".grain/workflows/README.md": "how to add one",
		".github/workflows/ci.yml": "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n" +
			"    steps:\n      - run: go test ./...\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	workflows, _, err := FSSource{Root: root}.Workflows(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("reading the checkout: %v", err)
	}
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush}, workflows)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 1 || plan.Unmatched != "" {
		t.Fatalf("the repository's own suite did not plan: %+v / %q", plan.Jobs, plan.Unmatched)
	}
}

// The fallback is about a directory with nothing in it to run, not about
// one grain could not read: a broken file under `.grain/workflows` is
// still this repository's answer, and quietly running GitHub's suite
// instead would report a verdict on a commit nobody's workflow tested.
func TestLoadDirDoesNotFallBackPastABrokenGrainWorkflow(t *testing.T) {
	tree := fstest.MapFS{
		".grain/workflows/broken.yml": &fstest.MapFile{Data: []byte("on: [")},
		".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(
			"on: push\njobs:\n  c:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n")},
	}
	if _, _, err := LoadDir(tree); err == nil || !strings.Contains(err.Error(), "broken.yml") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadDirNamesTheFileThatDoesNotParse(t *testing.T) {
	tree := fstest.MapFS{
		".grain/workflows/broken.yml": &fstest.MapFile{Data: []byte("on: push\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n")},
	}
	if _, _, err := LoadDir(tree); err == nil || !strings.Contains(err.Error(), "broken.yml") {
		t.Fatalf("got %v", err)
	}
}

// staticSource is a Source with its answer written down.
type staticSource struct {
	workflows []Workflow
	commit    string
	err       error
}

func (s staticSource) Workflows(context.Context, string, string) ([]Workflow, string, error) {
	return s.workflows, s.commit, s.err
}

// testService is a Service that says when a run has finished.
//
// Service.Start hands the run to a goroutine and returns, so the only
// thing a test can otherwise do is read the store over and over until the
// answer changes -- which is a stopwatch, and a stopwatch was what made
// these tests fail on a loaded CI host rather than on a broken tree
// (grain/task-431). The service already announces a finished run on its
// own Log, after the finished record is stored, so that is the signal
// waited on here: it cannot arrive before the record is readable, and it
// arrives however slowly the machine got there.
type testService struct {
	*Service
	finished chan string
}

func newTestService(t *testing.T, sandboxes Sandboxes, files map[string]string) *testService {
	t.Helper()
	var workflows []Workflow
	for path, body := range files {
		workflows = append(workflows, mustParse(t, path, body))
	}
	svc := &testService{
		Service: &Service{
			Source: staticSource{workflows: workflows, commit: "resolved-commit"},
			Engine: &Engine{Sandboxes: sandboxes},
		},
		// Room for every run a test starts, since a test that waits for
		// the second of two must still be able to find the first.
		finished: make(chan string, 16),
	}
	svc.Log = func(format string, args ...any) {
		if line := fmt.Sprintf(format, args...); strings.Contains(line, "ci: run ") {
			signal(svc.finished, runIDIn(line))
		}
	}
	return svc
}

// watchedStore is a RunStore that says when the service has written a
// run's progress to it -- the other half of the same idea as testService
// above, for a test watching a run that has not finished. Service
// publishes what the engine observes by updating the record, so "there is
// something new to read" is a signal the service raises rather than
// something a reader has to go looking for on a timer.
type watchedStore struct {
	RunStore
	wrote chan<- struct{}
}

func (s *watchedStore) Update(id string, fn func(*Run)) {
	s.RunStore.Update(id, fn)
	signal(s.wrote, struct{}{})
}

// runIDIn picks the run id out of Service's own "ci: run <id>: ..." line.
func runIDIn(line string) string {
	rest := strings.TrimPrefix(line, "ci: run ")
	id, _, _ := strings.Cut(rest, ":")
	return id
}

// waitForRun blocks until this service has finished the run with this id
// and returns the finished record. Runs it finished in the meantime are
// put back, so two tests' worth of waiting in either order still works.
func waitForRun(t *testing.T, svc *testService, id string) Run {
	t.Helper()
	// A run that had already finished before this call is not waited for
	// at all: the record is the answer, whoever drained the signal.
	if run, ok := svc.Get(id); ok && run.Conclusion.Done() {
		return run
	}
	var others []string
	defer func() {
		for _, other := range others {
			signal(svc.finished, other)
		}
	}()
	for {
		said := awaitSignal(t, svc.finished, fmt.Sprintf("run %s never finished", id))
		if said != id {
			others = append(others, said)
			continue
		}
		// The store is the answer, not the line: Service logs a run's
		// ending after it has stored the finished record, and it says
		// other things about a live run too.
		if run, ok := svc.Get(id); ok && run.Conclusion.Done() {
			return run
		}
	}
}

func TestWorkflowOptionsReturnsOnlyWorkflowsMatchingTheRequestedEvent(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/manual.yml": "name: Manual\non: workflow_dispatch\njobs:\n  one:\n    steps:\n      - run: true\n",
		".grain/workflows/push.yml":   "name: Push\non: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	options, err := svc.WorkflowOptions(context.Background(), Request{
		Repo: "o/r", Ref: "main", Event: EventManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Path != ".grain/workflows/manual.yml" || options[0].Name != "Manual" {
		t.Fatalf("options = %+v", options)
	}
}

func TestServiceStartsARunAndAnswersForItAfterwards(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush, RequestedBy: "operator"})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if started.ID == "" || started.Conclusion.Done() {
		t.Fatalf("a started run has an id and no conclusion yet: %+v", started)
	}
	if len(started.Jobs) != 1 || started.Jobs[0].JobID != "one" {
		t.Errorf("the caller is handed the jobs it is about to run: %+v", started.Jobs)
	}
	if started.Commit != "resolved-commit" {
		t.Errorf("a request with no commit takes the source's: got %q", started.Commit)
	}

	finished := waitForRun(t, svc, started.ID)
	if finished.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q (%s)", finished.Conclusion, finished.Error)
	}
	if finished.RequestedBy != "operator" || finished.ID != started.ID {
		t.Errorf("the finished run is the started one: %+v", finished)
	}
	if len(svc.List()) != 1 {
		t.Errorf("the run should still be listed: %+v", svc.List())
	}
}

// The whole of grain/task-254, end to end: a repository whose one
// workflow answers `push` on main alone, asked for a push of a task
// branch, produced a run that finished in seconds with no jobs and said
// "success" -- which wait_for_checks and the merge queue read as a suite
// that ran and passed. It says errored now, and the error names the
// branch, the event and what the file actually answers to, which is
// everything somebody has to compare to see what went wrong.
func TestServiceDoesNotReportAVacuousRunAsAPass(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".github/workflows/ci.yml": "on:\n  push:\n    branches: [main]\njobs:\n" +
			"  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n",
	})

	started, err := svc.Start(context.Background(), Request{
		Repo: "o/r", Ref: "grain/task-246", Event: EventPush, RequestedBy: "task-246",
	})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finished := waitForRun(t, svc, started.ID)
	if finished.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q, want errored rather than a green tick over nothing", finished.Conclusion)
	}
	for _, want := range []string{"grain/task-246", ".github/workflows/ci.yml", "push on main"} {
		if !strings.Contains(finished.Error, want) {
			t.Errorf("run error does not mention %q:\n%s", want, finished.Error)
		}
	}

	// Asking for one job by name is answered the same way. It used to be
	// refused on the request -- `ci: selected job "test" does not exist
	// in the matching workflows` -- which named the selection rather than
	// the reason there were no matching workflows to select from.
	selected, err := svc.Start(context.Background(), Request{
		Repo: "o/r", Ref: "grain/task-246", Event: EventPush, Jobs: []string{"test"},
	})
	if err != nil {
		t.Fatalf("selecting a job on a branch that matched nothing: %v", err)
	}
	if finished = waitForRun(t, svc, selected.ID); finished.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q / %q", finished.Conclusion, finished.Error)
	}
}

// The same thing in the shape somebody went and reproduced it in
// (grain/task-388): a repository whose one workflow answers
// `workflow_dispatch` and nothing else, pushed. It is the plainest
// matchless run there is -- the file parses, it loads, and it answers no
// branch push anywhere -- so it is the one to hold the whole chain from
// Start to a finished record against, rather than PlanRun alone.
func TestServiceErrorsARunWhoseOnlyWorkflowIsManual(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/manual.yml": "on: workflow_dispatch\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	started, err := svc.Start(context.Background(), Request{
		Repo: "o/r", Ref: "grain/task-366", Event: EventPush, RequestedBy: "task-366",
	})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finished := waitForRun(t, svc, started.ID)
	if finished.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q, want errored rather than a run of no jobs reported green", finished.Conclusion)
	}
	if len(finished.Jobs) != 0 {
		t.Errorf("a matchless run still has no jobs in it: %+v", finished.Jobs)
	}
	for _, want := range []string{"no workflow of o/r answers push", "grain/task-366",
		".grain/workflows/manual.yml", "workflow_dispatch"} {
		if !strings.Contains(finished.Error, want) {
			t.Errorf("run error does not mention %q:\n%s", want, finished.Error)
		}
	}

	// And the same workflow asked for the way it is meant to be asked
	// for runs, so this is a verdict on the event and not on the file.
	manual, err := svc.Start(context.Background(), Request{
		Repo: "o/r", Ref: "grain/task-366", Event: EventManual, RequestedBy: "operator",
	})
	if err != nil {
		t.Fatalf("dispatching it by hand: %v", err)
	}
	if finished = waitForRun(t, svc, manual.ID); finished.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q / %q", finished.Conclusion, finished.Error)
	}
}

func TestServiceHandsBackTheRunAlreadyDoingTheSameWork(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{})}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	req := Request{Repo: "o/r", Ref: "main", Event: EventPush}

	first, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	second, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("starting again: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the same work asked for twice is one run: %s and %s", first.ID, second.ID)
	}
	close(sandboxes.block)
	waitForRun(t, svc, first.ID)

	if sandboxes.sandboxes > 1 {
		t.Errorf("the second request should have built no sandbox of its own: %d built", sandboxes.sandboxes)
	}
}

func TestServiceRefusesARequestItCannotPlan(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, nil)
	if _, err := svc.Start(context.Background(), Request{Ref: "main"}); err == nil {
		t.Error("a run with no repository is refused on the request that asked")
	}

	broken := &Service{Source: staticSource{err: errors.New("no such branch")}, Engine: &Engine{Sandboxes: &fakeSandboxes{}}}
	_, err := broken.Start(context.Background(), Request{Repo: "o/r", Ref: "gone"})
	if err == nil || !strings.Contains(err.Error(), "no such branch") {
		t.Errorf("a source that cannot answer fails the request: got %v", err)
	}
	if len(broken.List()) != 0 {
		t.Error("a request that never became a run should leave no run behind")
	}
}

func TestServiceCancelsARunInFlight(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), execs: make(chan Command, 1)}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: forever\n",
	})

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	// Cancelled with the job's step actually running, which the backend
	// saying it started the command is what establishes: polling for it a
	// millisecond at a time until a made-up deadline only ever asserted
	// that this machine gets a goroutine into a sandbox quickly.
	awaitSignal(t, sandboxes.execs, "the job's step never started, so there was nothing to cancel")
	if !svc.Cancel(started.ID, "", CancelBySystem) {
		t.Fatal("a live run should be cancellable")
	}
	if got := waitForRun(t, svc, started.ID); got.Conclusion != ConclusionCancelled {
		t.Errorf("conclusion: got %q", got.Conclusion)
	}
	if svc.Cancel("nothing-by-that-name", "", CancelBySystem) {
		t.Error("cancelling an unknown run should report that there was nothing to cancel")
	}
}

// The note whoever stopped a run left has to survive the run's own
// ending, which is the only hard part of carrying one: the record is
// replaced wholesale with the finished article when the engine unwinds,
// and a reason written on the record before that would be thrown away
// with it -- leaving the reader this was written for, an agent asking
// what happened to its branch, with a bare "cancelled".
func TestServiceKeepsWhyARunWasCancelled(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), execs: make(chan Command, 1)}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: forever\n",
	})

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	// Cancelled with the job's step actually running, which the backend
	// saying it started the command is what establishes: polling for it a
	// millisecond at a time until a made-up deadline only ever asserted
	// that this machine gets a goroutine into a sandbox quickly.
	awaitSignal(t, sandboxes.execs, "the job's step never started, so there was nothing to cancel")
	const why = "  the base branch is being reverted; this tree is not worth testing  "
	if !svc.Cancel(started.ID, why, CancelByOperator) {
		t.Fatal("a live run should be cancellable")
	}
	// On the record straight away, so a pane polling the run while its
	// jobs give up is told why rather than waiting for the conclusion.
	if got, ok := svc.Get(started.ID); !ok || got.CancelReason != strings.TrimSpace(why) {
		t.Errorf("run while it is stopping: CancelReason = %q", got.CancelReason)
	}
	got := waitForRun(t, svc, started.ID)
	if got.Conclusion != ConclusionCancelled {
		t.Errorf("conclusion: got %q", got.Conclusion)
	}
	if got.CancelReason != strings.TrimSpace(why) {
		t.Errorf("CancelReason on the finished run = %q, want the note trimmed", got.CancelReason)
	}
	// And who asked, which the note cannot say and which decides whether
	// the merge queue starts another run for the same commit
	// (orchestrator's ciReplacementFor).
	if got.CancelledBy != CancelByOperator {
		t.Errorf("CancelledBy on the finished run = %q, want %q", got.CancelledBy, CancelByOperator)
	}
}

// A cancellation with nothing written on it still says who did it: a
// timeout, a shutdown and a person who typed no reason are all "cancelled
// with no note", and only the source tells them apart.
func TestServiceKeepsWhoCancelledARunWithNoNote(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), execs: make(chan Command, 1)}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: forever\n",
	})

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	// Cancelled with the job's step actually running, which the backend
	// saying it started the command is what establishes: polling for it a
	// millisecond at a time until a made-up deadline only ever asserted
	// that this machine gets a goroutine into a sandbox quickly.
	awaitSignal(t, sandboxes.execs, "the job's step never started, so there was nothing to cancel")
	if !svc.Cancel(started.ID, "", CancelByOperator) {
		t.Fatal("a live run should be cancellable")
	}
	if got, ok := svc.Get(started.ID); !ok || got.CancelledBy != CancelByOperator {
		t.Errorf("run while it is stopping: CancelledBy = %q", got.CancelledBy)
	}
	got := waitForRun(t, svc, started.ID)
	if got.Conclusion != ConclusionCancelled {
		t.Errorf("conclusion: got %q", got.Conclusion)
	}
	if got.CancelReason != "" {
		t.Errorf("CancelReason = %q, want nothing: nobody wrote one", got.CancelReason)
	}
	if got.CancelledBy != CancelByOperator {
		t.Errorf("CancelledBy on the finished run = %q, want %q", got.CancelledBy, CancelByOperator)
	}
}

func TestServiceShowsJobsFinishingBeforeTheRunDoes(t *testing.T) {
	release := make(chan struct{})
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "slow") {
			<-release
		}
		return CommandResult{}, nil
	}}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  quick:\n    steps:\n      - run: quick\n  slow:\n    steps:\n      - run: slow\n",
	})
	updates := make(chan struct{}, 32)
	svc.Runs = &watchedStore{RunStore: NewRunStore(DefaultRunHistory), wrote: updates}

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	// Read the record every time the service writes to it, rather than
	// every millisecond until a deadline: publishing a job's progress is
	// an update (Start's observer), so the store saying it has changed is
	// the service's own signal that there is something new to look at.
	for {
		awaitSignal(t, updates, "the quick job never showed up as finished while the run was still going")
		run, _ := svc.Get(started.ID)
		if run.Conclusion.Done() {
			t.Fatal("the run finished before the slow job was released")
		}
		quick := false
		for _, job := range run.Jobs {
			if job.JobID == "quick" && job.Conclusion == ConclusionSuccess {
				quick = true
			}
		}
		if quick {
			break
		}
	}
	close(release)
	waitForRun(t, svc, started.ID)
}

func TestRunStoreKeepsLiveRunsAndForgetsOldOnes(t *testing.T) {
	store := NewRunStore(2)
	store.Put(Run{ID: "live", StartedAt: timePtr(time.Unix(1, 0))})
	store.Put(Run{ID: "old", StartedAt: timePtr(time.Unix(2, 0)), Conclusion: ConclusionSuccess})
	store.Put(Run{ID: "newer", StartedAt: timePtr(time.Unix(3, 0)), Conclusion: ConclusionSuccess})
	store.Put(Run{ID: "newest", StartedAt: timePtr(time.Unix(4, 0)), Conclusion: ConclusionFailure})

	if _, ok := store.Get("old"); ok {
		t.Error("the oldest finished run should have been evicted")
	}
	if _, ok := store.Get("live"); !ok {
		t.Error("a run still going is never evicted")
	}
	got := store.List()
	if len(got) != 3 || got[0].ID != "newest" {
		t.Errorf("List is newest first: %+v", got)
	}
	if _, ok := store.Live(""); !ok {
		t.Error("Live should find the unfinished run")
	}
}

// The two-hour backstop, at a scale a test can wait out.
//
// MaxRunTime is not the sum of the jobs' own timeouts and is not meant to
// be (DefaultMaxRunTime's own doc comment): it is what ends a run whose
// backend has stopped answering in a way no individual job notices --
// a VM that boots and never finishes a command, a transport that accepts
// a script and never returns. Nothing exercised it while the only backend
// was a fake that always answered; a real one made it worth pinning, and
// this pins it with the fake that never answers, which is the same
// failure at a hundred-millisecond scale.
func TestServiceStopsARunThatOutlastsItsCeiling(t *testing.T) {
	// block is never closed: every command hangs until something ends it
	// from outside, which is exactly the failure this bound exists for.
	sandboxes := &fakeSandboxes{block: make(chan struct{})}
	svc := newTestService(t, sandboxes, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    timeout-minutes: 60\n    steps:\n      - run: forever\n",
	})
	svc.MaxRunTime = 100 * time.Millisecond

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finished := waitForRun(t, svc, started.ID)

	if finished.Conclusion != ConclusionCancelled {
		t.Errorf("a run stopped by its own ceiling is cancelled, not %s (%s)", finished.Conclusion, finished.Error)
	}
	// And the machine it was holding is gone: a backstop that ended the
	// run and left the sandbox behind would cost the deployment its
	// capacity every time it fired.
	sandboxes.mu.Lock()
	released := len(sandboxes.released)
	sandboxes.mu.Unlock()
	if released != 1 {
		t.Errorf("the job's sandbox should have been released: %d of %d released", released, sandboxes.sandboxes)
	}
}
