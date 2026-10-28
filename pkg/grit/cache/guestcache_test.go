package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// fakeWarmService is the two things a GuestWarmer asks of the CI service
// that owns it -- start a run, read it back -- with the conclusion under
// the test's control, so that "one build, everybody waits" can be checked
// without a sandbox anywhere.
type fakeWarmService struct {
	mu       sync.Mutex
	started  []workflows.Request
	runs     map[string]workflows.Run
	startErr error
	// release, when set, is what a run is waiting for before it
	// concludes: every Get before it closes reports a run still going.
	release chan struct{}
	// conclusion is what a released run ends as.
	conclusion workflows.Conclusion
	// starts is sent to as each run is started, so a test waits for the
	// warmer to have got somewhere rather than sleeping.
	starts chan string
	nextID int
}

func newFakeWarmService() *fakeWarmService {
	return &fakeWarmService{
		runs: map[string]workflows.Run{}, release: make(chan struct{}),
		conclusion: workflows.ConclusionSuccess, starts: make(chan string, 8),
	}
}

func (f *fakeWarmService) Start(_ context.Context, req workflows.Request) (workflows.Run, error) {
	f.mu.Lock()
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return workflows.Run{}, err
	}
	f.nextID++
	id := "warm-" + string(rune('a'+f.nextID-1))
	f.started = append(f.started, req)
	f.runs[id] = workflows.Run{ID: id, Repo: req.Repo, Ref: req.Ref, Commit: req.Commit, Event: req.Event}
	f.mu.Unlock()
	signal(f.starts, id)
	return workflows.Run{ID: id}, nil
}

func (f *fakeWarmService) Get(id string) (workflows.Run, bool) {
	f.mu.Lock()
	run, ok := f.runs[id]
	f.mu.Unlock()
	if !ok {
		return workflows.Run{}, false
	}
	select {
	case <-f.release:
		run.Conclusion = f.conclusion
	default:
	}
	return run, true
}

func (f *fakeWarmService) requests() []workflows.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workflows.Request(nil), f.started...)
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

// TestSeveralJobsMissingOneGuestKeyStartOneWarmRun is the failure this
// whole mechanism is written around, and the reason the flight book is
// keyed on the guest key and on nothing else: several jobs -- here on two
// different branches, which is the case a per-branch lock would get wrong
// -- want the same guest at the same moment, and exactly one run is
// started for all of them.
func TestSeveralJobsMissingOneGuestKeyStartOneWarmRun(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	warmer := warmerFor(service, ".grain/workflows/image.yml:guest-cache")

	const key = workflows.Key("guest:abc")
	waiting := make(chan error, 6)
	for i := 0; i < 6; i++ {
		ref := "grain/task-1"
		if i%2 == 1 {
			ref = "grain/task-2" // same guest inputs, a different branch
		}
		go func(ref string) {
			waiting <- warmer.Warm(context.Background(), GuestCacheRequest{
				RunID: "run-of-a-job", Repo: "bwsalmon/grain", Ref: ref,
				Commit: "c0ffee", Key: key,
			}, nil)
		}(ref)
	}

	awaitSignal(t, service.starts, "the warmer never started a run")
	// Everybody is now waiting on that one run. Let it conclude.
	close(service.release)
	for i := 0; i < 6; i++ {
		select {
		case err := <-waiting:
			if err != nil {
				t.Fatalf("a job waiting for a warmed guest got %v", err)
			}
		case <-time.After(signalWaitLimit):
			t.Fatalf("a job was still waiting for the guest %s after the warm run finished", key)
		}
	}

	if got := service.requests(); len(got) != 1 {
		t.Fatalf("six jobs missing one guest key started %d runs, and the whole point is that they start one: %+v",
			len(got), got)
	}
	req := service.requests()[0]
	if req.Event != workflows.EventManual {
		t.Errorf("the warm run was started under %s; it has to be %s, which is the only event a "+
			"package-publish credential is placed for besides a release", req.Event, workflows.EventManual)
	}
	if len(req.Jobs) != 1 || req.Jobs[0] != ".grain/workflows/image.yml:guest-cache" {
		t.Errorf("the warm run ran %v rather than the one job the operator named", req.Jobs)
	}
	if !req.WithholdReleaseSecrets {
		t.Error("the warm run was started without withholding release secrets: it runs a branch nobody " +
			"has necessarily reviewed, and warming a cache never needs a signing key (issue #2389)")
	}
	if !strings.HasPrefix(req.RequestedBy, "guest-cache-warm:") {
		t.Errorf("the warm run says it was requested by %q, which is not something a reader of the run "+
			"list can attribute to the guest cache", req.RequestedBy)
	}
}

// TestAWarmAlreadyDoneIsNotStartedAgain: the key stays warm. A branch
// that arrives after the build has been published pulls it, and nothing
// spends another sandbox proving that.
func TestAWarmAlreadyDoneIsNotStartedAgain(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	close(service.release) // every run concludes as soon as it is read back
	warmer := warmerFor(service, "guest-cache")

	for i := 0; i < 3; i++ {
		if err := warmer.Warm(context.Background(), GuestCacheRequest{
			Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
		}, nil); err != nil {
			t.Fatalf("warm %d: %v", i, err)
		}
	}
	if got := len(service.requests()); got != 1 {
		t.Fatalf("a guest that is already warm was warmed %d times", got)
	}
}

// TestADifferentGuestKeyIsADifferentWarm: the flight book is not a
// global lock. Two branches that changed *different* guest inputs need
// two different images and get two runs.
func TestADifferentGuestKeyIsADifferentWarm(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	close(service.release)
	warmer := warmerFor(service, "guest-cache")

	for _, key := range []workflows.Key{"guest:abc", "guest:def"} {
		if err := warmer.Warm(context.Background(), GuestCacheRequest{
			Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: key,
		}, nil); err != nil {
			t.Fatalf("warming %s: %v", key, err)
		}
	}
	if got := len(service.requests()); got != 2 {
		t.Fatalf("two different guests were warmed by %d runs", got)
	}
}

// TestAJobThatWaitsPastItsOwnTimeoutStopsWaiting. The wait is bounded by
// the caller's context -- the engine gives it the job's own timeout --
// and what a job gets back is an error rather than a warm guest, which
// is what makes it go and build its own.
func TestAJobThatWaitsPastItsOwnTimeoutStopsWaiting(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService() // never released: the warm run never ends
	warmer := warmerFor(service, "guest-cache")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := warmer.Warm(ctx, GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
	}, nil)
	if err == nil {
		t.Fatal("a job whose timeout expired while the guest was still being built was told the guest was warm")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the job that stopped waiting was told %v, which does not say its own clock ran out", err)
	}

	// And the warm itself is still going for whoever is next: the first
	// job walking away must not take the build with it.
	if got := len(service.requests()); got != 1 {
		t.Fatalf("the warm run was not started, or was started %d times", got)
	}
	warmer.mu.Lock()
	flight := warmer.warms["guest:abc"]
	warmer.mu.Unlock()
	select {
	case <-flight.done:
		t.Error("the warm was abandoned when the job waiting on it gave up; it serves every later job too")
	default:
	}
}

// TestAFailedWarmIsNotStartedAgainForTheSameGuest. A guest that cannot be
// built in this deployment is the failure that must not become a loop:
// every branch would otherwise spend a fresh 25-minute run finding it out
// again. What the jobs get instead is the error, which they build their
// own guest on.
func TestAFailedWarmIsNotStartedAgainForTheSameGuest(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	service.conclusion = workflows.ConclusionFailure
	close(service.release)
	now := time.Now()
	warmer := warmerFor(service, "guest-cache")
	warmer.Now = func() time.Time { return now }

	first := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
	}, nil)
	if first == nil {
		t.Fatal("a warm whose run failed reported success, so the job after it would wait for a guest nobody published")
	}
	second := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-2", Key: "guest:abc",
	}, nil)
	if second == nil {
		t.Fatal("the second job was told the guest was warm")
	}
	if got := len(service.requests()); got != 1 {
		t.Fatalf("a failed warm was retried immediately (%d runs), which is how one broken guest eats a "+
			"deployment's CI", got)
	}

	// Past the cooldown, it is worth another try: a fix pushed in the
	// meantime is exactly what this window is for.
	now = now.Add(DefaultGuestWarmCooldown + time.Minute)
	service.conclusion = workflows.ConclusionSuccess
	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-3", Key: "guest:abc",
	}, nil); err != nil {
		t.Fatalf("after the cooldown the guest was warmed again and the job was told %v", err)
	}
	if got := len(service.requests()); got != 2 {
		t.Fatalf("after the cooldown the warm was not tried again (%d runs)", got)
	}
}

// TestAWarmRunDoesNotWarmItself. The job that fills the cache resolves a
// guest of its own, so a repository declaring `guest-cache:` on it would
// have grain dispatching a run to warm the run that is warming.
func TestAWarmRunDoesNotWarmItself(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	close(service.release)
	warmer := warmerFor(service, "guest-cache")

	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
	}, nil); err != nil {
		t.Fatalf("warming: %v", err)
	}
	started := service.requests()
	if len(started) != 1 {
		t.Fatalf("expected one warm run, got %d", len(started))
	}
	warmer.mu.Lock()
	var runID string
	for id := range warmer.started {
		runID = id
	}
	warmer.mu.Unlock()

	// A job of that run, asking for a different guest: still nothing.
	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		RunID: runID, Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:zzz",
	}, nil); err != nil {
		t.Fatalf("a job of the warm run was told %v", err)
	}
	if got := len(service.requests()); got != 1 {
		t.Fatalf("the warm run warmed itself: %d runs", got)
	}
}

// TestARepositoryThatNamesNoCacheJobIsNotWarmed. Dispatching a job that
// publishes is something an operator has said grain may do for this
// repository (model.RepoConfig.PostMergeDispatchJobs); where nobody has,
// grain starts nothing and the job resolves its own guest exactly as it
// did before any of this existed.
func TestARepositoryThatNamesNoCacheJobIsNotWarmed(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	close(service.release)
	warmer := warmerFor(service) // no jobs named

	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "somebody/else", Ref: "main", Key: "guest:abc",
	}, nil); err != nil {
		t.Fatalf("a repository that names no cache job was told %v rather than simply not being warmed", err)
	}
	if got := len(service.requests()); got != 0 {
		t.Fatalf("grain dispatched %d run(s) into a repository whose operator named no job for it", got)
	}
}

// TestAWaitingJobSaysWhatItIsWaitingFor. A job held for twenty minutes
// on a guest and a job held for twenty minutes on memory are the same
// length of wait and two entirely different problems, so they are
// reported apart (Wait, WaitForGuest).
func TestAWaitingJobSaysWhatItIsWaitingFor(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	warmer := warmerFor(service, "guest-cache")

	waits := make(chan workflows.Wait, 4)
	done := make(chan error, 1)
	go func() {
		done <- warmer.Warm(context.Background(), GuestCacheRequest{
			Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
		}, func(w workflows.Wait) { signal(waits, w) })
	}()
	w := awaitSignal(t, waits, "a job blocked on a guest build never said it was waiting")
	if w.Cause != workflows.WaitForGuest {
		t.Errorf("a job waiting for a guest was bucketed as %q", w.Cause)
	}
	if !strings.Contains(w.Reason, "guest") {
		t.Errorf("the wait reads %q, which does not tell a watcher what is holding the job", w.Reason)
	}
	close(service.release)
	if err := <-done; err != nil {
		t.Fatalf("warming: %v", err)
	}
}

// TestAJobAdmittedStraightAwayReportsNoWait holds the warmer to the
// contract every other waiter in the engine follows: a job that is not
// actually held says nothing, so a deployment's wait figures are about
// jobs that waited.
func TestAJobAdmittedStraightAwayReportsNoWait(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	close(service.release)
	warmer := warmerFor(service, "guest-cache")
	// Warm it once, so the second ask is answered from the flight book.
	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
	}, nil); err != nil {
		t.Fatalf("warming: %v", err)
	}
	said := false
	if err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-2", Key: "guest:abc",
	}, func(workflows.Wait) { said = true }); err != nil {
		t.Fatalf("second warm: %v", err)
	}
	if said {
		t.Error("a job whose guest was already warm was recorded as having queued for it")
	}
}

// TestAWarmRunThisDeploymentLosesDoesNotHoldJobsForEver: a run that has
// gone -- pruned, or lost with the daemon that was running it -- is a
// warm that concluded nothing, and leaving it in flight would leave every
// job that needs that guest waiting for a verdict that cannot arrive.
func TestAWarmRunThisDeploymentLosesDoesNotHoldJobsForEver(t *testing.T) {
	t.Parallel()
	service := newFakeWarmService()
	warmer := warmerFor(service, "guest-cache")
	id := ""
	go func() {
		id = awaitStart(service)
		service.forget(id)
	}()
	err := warmer.Warm(context.Background(), GuestCacheRequest{
		Repo: "bwsalmon/grain", Ref: "grain/task-1", Key: "guest:abc",
	}, nil)
	if err == nil {
		t.Fatal("a warm whose run vanished reported the guest as warm")
	}
	if !strings.Contains(err.Error(), "no longer known") {
		t.Errorf("the job was told %q, which does not say the run went missing", err)
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

// signal tells a test something happened without ever making the engine
// wait for the test to be listening: an unset or full channel drops it.
func signal[T any](ch chan<- T, v T) {
	if ch == nil {
		return
	}
	select {
	case ch <- v:
	default:
	}
}

// signalWaitLimit is a ceiling, not a budget -- the one this package's
// tests wait under, whether what they are waiting for comes from the
// engine, a gate or the service. Everything they wait for is a signal the
// code under test raises as it goes, so it either arrives as fast as the
// machine can carry it or never arrives at all; the limit exists only so
// a genuinely stuck run fails the test rather than hanging the package
// until go test's own timeout. Nothing here asserts the engine is quick,
// because a CI host running the whole suite at once is not quick and
// never was: a second of wall clock was enough on an idle machine and
// failed on eight branches at once while a dozen sandboxes were each
// building a guest image (grain/task-394, grain/task-426). A generous
// ceiling costs a genuinely hung engine nothing but a slower red, and
// costs a merely busy one nothing at all.
const signalWaitLimit = 30 * time.Second

// awaitSignal blocks for one value from ch, failing the test with what
// it was waiting for if the code under test never gets there.
func awaitSignal[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(signalWaitLimit):
		var zero T
		t.Fatalf("%s (nothing after %s)", what, signalWaitLimit)
		return zero
	}
}
