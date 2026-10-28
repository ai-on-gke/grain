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

package ci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTheGuestKeyIsTheContentOfTheDeclaredInputs pins what the single
// flight is keyed on: two branches whose guest inputs are identical want
// one build, a branch that changed one wants its own, and a change to
// anything else in the tree changes neither.
func TestTheGuestKeyIsTheContentOfTheDeclaredInputs(t *testing.T) {
	t.Parallel()
	job := Job{ID: "kvm", Cache: true, GuestCache: []string{"go.mod", "Granulefile*"},
		Steps: []Step{{Run: "true"}}}
	w := Workflow{Path: ".grain/workflows/tests.yml", Name: "tests",
		On: Triggers{Push: &BranchTrigger{}}, Jobs: []Job{job}}
	req := Request{Repo: "bwsalmon/grain", Ref: "grain/task-1", Commit: "aaa", Event: EventPush}

	tree := &Tree{Files: []TreeFile{
		{Path: "go.mod", Blob: "100644:1"},
		{Path: "Granulefile", Blob: "100644:2"},
		{Path: "pkg/ci/engine.go", Blob: "100644:3"},
	}}
	base := guestKeyOf(t, req, w, tree)
	if base == "" {
		t.Fatal("a job declaring guest inputs that exist in the tree got no guest key")
	}

	// Another branch, another commit, the same guest inputs: one guest.
	other := guestKeyOf(t, Request{Repo: "bwsalmon/grain", Ref: "grain/task-2", Commit: "bbb", Event: EventPush}, w, tree)
	if other != base {
		t.Errorf("two branches with identical guest inputs derived two keys (%s, %s), so each would "+
			"build the same image", base, other)
	}

	// A file this job does not build its guest from: still one guest.
	touched := &Tree{Files: []TreeFile{
		{Path: "go.mod", Blob: "100644:1"},
		{Path: "Granulefile", Blob: "100644:2"},
		{Path: "pkg/ci/engine.go", Blob: "100644:changed"},
	}}
	if got := guestKeyOf(t, req, w, touched); got != base {
		t.Errorf("a commit that touched nothing the guest is built from changed its key (%s -> %s)", base, got)
	}

	// A guest input: a different guest, and a warm of its own.
	changed := &Tree{Files: []TreeFile{
		{Path: "go.mod", Blob: "100644:changed"},
		{Path: "Granulefile", Blob: "100644:2"},
		{Path: "pkg/ci/engine.go", Blob: "100644:3"},
	}}
	if got := guestKeyOf(t, req, w, changed); got == base {
		t.Error("a commit that changed a guest input kept the same guest key, so it would wait for an " +
			"image built from the tree it replaced")
	}

	// And another repository's identical tree is another registry.
	elsewhere := guestKeyOf(t, Request{Repo: "somebody/else", Ref: "main", Commit: "aaa", Event: EventPush}, w, tree)
	if elsewhere == base {
		t.Error("two repositories share one guest key, and the cache they read is per repository")
	}
}

// TestAJobThatDeclaresNoGuestInputsIsNotWarmed covers the three ways a
// plan says "nothing to warm here", all of which leave the job exactly as
// it was before this existed.
func TestAJobThatDeclaresNoGuestInputsIsNotWarmed(t *testing.T) {
	t.Parallel()
	req := Request{Repo: "bwsalmon/grain", Ref: "main", Commit: "aaa", Event: EventPush}
	tree := &Tree{Files: []TreeFile{{Path: "go.mod", Blob: "100644:1"}}}

	undeclared := Workflow{Path: "w.yml", On: Triggers{Push: &BranchTrigger{}},
		Jobs: []Job{{ID: "go", Cache: true, Steps: []Step{{Run: "true"}}}}}
	if key := guestKeyOf(t, req, undeclared, tree); key != "" {
		t.Errorf("a job that declares no guest inputs got the guest key %s", key)
	}

	typo := Workflow{Path: "w.yml", On: Triggers{Push: &BranchTrigger{}},
		Jobs: []Job{{ID: "kvm", Cache: true, GuestCache: []string{"scripst/kontur/**"},
			Steps: []Step{{Run: "true"}}}}}
	if key := guestKeyOf(t, req, typo, tree); key != "" {
		t.Errorf("a declaration matching no file in the tree got the guest key %s, which would be the "+
			"same key for every commit for ever", key)
	}

	declared := Workflow{Path: "w.yml", On: Triggers{Push: &BranchTrigger{}},
		Jobs: []Job{{ID: "kvm", Cache: true, GuestCache: []string{"go.mod"},
			Steps: []Step{{Run: "true"}}}}}
	if key := guestKeyOf(t, req, declared, nil); key != "" {
		t.Errorf("a run whose source could not read the tree got the guest key %s", key)
	}
}

func guestKeyOf(t *testing.T, req Request, w Workflow, tree *Tree) Key {
	t.Helper()
	plan, err := PlanRunIn(req, []Workflow{w}, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 1 {
		t.Fatalf("planned %d jobs, wanted 1", len(plan.Jobs))
	}
	return plan.Jobs[0].GuestKey
}

// TestDeclaringGuestInputsMissesNoCacheEntry. The key a job is cached
// under and the guest it resolves are separate questions on purpose, and
// the json tag on Job.GuestCache is what keeps adding this field to the
// format from missing every entry in every deployment's cache.
func TestDeclaringGuestInputsMissesNoCacheEntry(t *testing.T) {
	t.Parallel()
	req := Request{Repo: "bwsalmon/grain", Ref: "main", Commit: "aaa", Event: EventPush}
	w := Workflow{Path: "w.yml", Name: "w"}
	plain := Job{ID: "kvm", Cache: true, Steps: []Step{{Run: "true"}}}
	declaring := plain
	declaring.GuestCache = []string{"go.mod"}

	if Fingerprint(req, w, plain) == Fingerprint(req, w, declaring) {
		t.Error("declaring guest inputs did not change the job's own key, and it is part of the declaration")
	}
	// The one that matters, and the reason for the `omitempty`: a job
	// that declares none is hashed over bytes with no mention of this
	// field in them, so no deployment re-runs its whole suite because the
	// format grew a key most jobs do not use.
	body, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshalling a job: %v", err)
	}
	if strings.Contains(string(body), "guestCache") {
		t.Errorf("a job that declares no guest inputs hashes as %s, which mentions the new field and so "+
			"misses every cache entry this deployment holds", body)
	}
}

// blockingWarmer is a GuestCacheWarmer a test can hold a job at, and can
// make fail, so that "before the machine" and "failing here fails
// nothing" can both be asserted against a real Engine.
type blockingWarmer struct {
	entered chan GuestCacheRequest
	allow   chan struct{}
	waits   chan Wait
	err     error
}

func (b *blockingWarmer) Warm(ctx context.Context, req GuestCacheRequest, wait func(Wait)) error {
	signal(b.entered, req)
	if wait != nil {
		wait(Wait{Cause: WaitForGuest, Reason: "waiting for the sandbox guest this job resolves to be built and published"})
		signal(b.waits, Wait{Cause: WaitForGuest})
	}
	select {
	case <-b.allow:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.err
}

// TestAJobWaitingForItsGuestHoldsNoSandbox. The whole saving is that the
// waiting is done outside the machine: a job held for half an hour on a
// guest build while holding one of ten CI sandboxes would cost more than
// the build it is waiting for.
func TestAJobWaitingForItsGuestHoldsNoSandbox(t *testing.T) {
	sandboxes := &fakeSandboxes{acquires: make(chan SandboxRequest, 2)}
	warmer := &blockingWarmer{
		entered: make(chan GuestCacheRequest, 2), allow: make(chan struct{}), waits: make(chan Wait, 4),
	}
	plan := planFor(t, Request{Repo: "o/r", Ref: "grain/task-1", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  kvm:\n    steps:\n      - run: boot a vm\n",
	})
	plan.Jobs[0].GuestKey = "guest:abc"

	done := make(chan Run, 1)
	go func() {
		done <- (&Engine{Sandboxes: sandboxes, GuestCache: warmer}).Run(context.Background(), "r1", plan, nil)
	}()

	req := awaitSignal(t, warmer.entered, "the job never asked for its guest to be warmed")
	if req.Key != "guest:abc" || req.Repo != "o/r" || req.Ref != "grain/task-1" || req.Commit != "abc" {
		t.Errorf("the warm was asked for on %+v, which is not the tree the job runs against", req)
	}
	if req.RunID != "r1" {
		t.Errorf("the warm request names run %q; it carries the run so a warm run's own jobs never warm", req.RunID)
	}
	sandboxes.mu.Lock()
	acquired := sandboxes.sandboxes
	sandboxes.mu.Unlock()
	if acquired != 0 {
		t.Fatalf("%d sandbox(es) were taken while the job was still waiting for its guest", acquired)
	}

	close(warmer.allow)
	run := awaitSignal(t, done, "the run did not finish once the guest was warm")
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q: %s", run.Conclusion, run.Error)
	}
	job := jobByID(t, run, "kvm")
	if job.Queued <= 0 || job.QueuedReason == "" {
		t.Errorf("a job held for a guest recorded no wait (%s, %q), so the time goes down as time spent "+
			"on nothing", job.Queued, job.QueuedReason)
	}
	if job.Activity != "" {
		t.Errorf("the job is still reported as %q after it ran", job.Activity)
	}
}

// TestAGuestThatCouldNotBeWarmedStillRunsTheJob: every failure of the
// warming degrades to what the branch did before there was any -- the
// job runs, and its own first step resolves a guest for itself.
func TestAGuestThatCouldNotBeWarmedStillRunsTheJob(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	warmer := &blockingWarmer{allow: make(chan struct{}), err: errors.New("the guest build failed twice")}
	close(warmer.allow)
	plan := planFor(t, Request{Repo: "o/r", Ref: "grain/task-1", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  kvm:\n    steps:\n      - run: boot a vm\n",
	})
	plan.Jobs[0].GuestKey = "guest:abc"

	run := (&Engine{Sandboxes: sandboxes, GuestCache: warmer}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("a job whose guest could not be warmed was concluded %q (%s), and warming is an "+
			"optimisation rather than a prerequisite", run.Conclusion, run.Error)
	}
	if len(sandboxes.acquired) != 1 {
		t.Fatalf("the job did not run: %d sandboxes", len(sandboxes.acquired))
	}
}

// TestAJobDeclaringNoGuestNeverReachesTheWarmer keeps the cost of this
// at zero for every job that is not about a guest, which is all but one
// of them.
func TestAJobDeclaringNoGuestNeverReachesTheWarmer(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	warmer := &blockingWarmer{entered: make(chan GuestCacheRequest, 1), allow: make(chan struct{})}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  go:\n    steps:\n      - run: go test\n",
	})

	run := (&Engine{Sandboxes: sandboxes, GuestCache: warmer}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q: %s", run.Conclusion, run.Error)
	}
	select {
	case req := <-warmer.entered:
		t.Fatalf("a job that declares no guest inputs asked for a warm: %+v", req)
	default:
	}
}

// TestTheGuestCacheKeyIsReadFromAWorkflowFile: the declaration is a
// workflow key like any other, takes one pattern or a list, and refuses
// the filters that would quietly match nothing.
func TestTheGuestCacheKeyIsReadFromAWorkflowFile(t *testing.T) {
	t.Parallel()
	w, err := Parse(".grain/workflows/tests.yml", []byte(
		"on: push\njobs:\n  kvm:\n    guest-cache:\n      - go.mod\n      - scripts/granule/**\n    steps:\n      - run: boot a vm\n"))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got := w.Jobs[0].GuestCache; len(got) != 2 || got[0] != "go.mod" || got[1] != "scripts/granule/**" {
		t.Fatalf("guest-cache: got %v", got)
	}

	one, err := Parse("w.yml", []byte("on: push\njobs:\n  kvm:\n    guest-cache: go.mod\n    steps:\n      - run: x\n"))
	if err != nil {
		t.Fatalf("parsing a single pattern: %v", err)
	}
	if got := one.Jobs[0].GuestCache; len(got) != 1 || got[0] != "go.mod" {
		t.Fatalf("guest-cache: got %v", got)
	}

	if _, err := Parse("w.yml", []byte("on: push\njobs:\n  kvm:\n    guest-cache: /etc/passwd\n    steps:\n      - run: x\n")); err == nil {
		t.Error("an absolute path was accepted as a guest input, and it would match nothing for ever")
	}
}

// fakeWarmService and its helpers are pkg/grit/cache's, restated for
// this package's engine-level and stress tests of the guest cache.

// fakeWarmService is the two things a GuestWarmer asks of the CI service
// that owns it -- start a run, read it back -- with the conclusion under
// the test's control, so that "one build, everybody waits" can be checked
// without a sandbox anywhere.
type fakeWarmService struct {
	mu       sync.Mutex
	started  []Request
	runs     map[string]Run
	startErr error
	// release, when set, is what a run is waiting for before it
	// concludes: every Get before it closes reports a run still going.
	release chan struct{}
	// conclusion is what a released run ends as.
	conclusion Conclusion
	// starts is sent to as each run is started, so a test waits for the
	// warmer to have got somewhere rather than sleeping.
	starts chan string
	nextID int
}

func newFakeWarmService() *fakeWarmService {
	return &fakeWarmService{
		runs: map[string]Run{}, release: make(chan struct{}),
		conclusion: ConclusionSuccess, starts: make(chan string, 8),
	}
}

func (f *fakeWarmService) Start(_ context.Context, req Request) (Run, error) {
	f.mu.Lock()
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return Run{}, err
	}
	f.nextID++
	id := "warm-" + string(rune('a'+f.nextID-1))
	f.started = append(f.started, req)
	f.runs[id] = Run{ID: id, Repo: req.Repo, Ref: req.Ref, Commit: req.Commit, Event: req.Event}
	f.mu.Unlock()
	signal(f.starts, id)
	return Run{ID: id}, nil
}

func (f *fakeWarmService) Get(id string) (Run, bool) {
	f.mu.Lock()
	run, ok := f.runs[id]
	f.mu.Unlock()
	if !ok {
		return Run{}, false
	}
	select {
	case <-f.release:
		run.Conclusion = f.conclusion
	default:
	}
	return run, true
}

func (f *fakeWarmService) requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.started...)
}

// warmerFor is a GuestWarmer wired to one fake service, polling fast
// enough that a test does not wait on the fifteen-second default.
func warmerFor(service *fakeWarmService, jobs ...string) *GuestWarmer {
	return &GuestWarmer{
		Jobs:  func(string) []string { return jobs },
		Start: service.Start,
		Get:   service.Get,
		Poll:  time.Millisecond,
	}
}

func awaitStart(f *fakeWarmService) string {
	for {
		f.mu.Lock()
		n := len(f.started)
		f.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.runs {
		return id
	}
	return ""
}

func (f *fakeWarmService) forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runs, id)
}
