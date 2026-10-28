package ci

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The ordering half of the gate (grain/task-316). What it replaced was a
// free-for-all: every release woke every waiter and whichever goroutine
// reached the mutex first won, so a job that had waited forty minutes had
// exactly the chance of one that arrived a millisecond ago -- and rather
// worse than that, because admission is two tests and the small jobs kept
// slipping through releases the 16 GiB ones could not use.

// acquiring is one job queueing at a gate from its own goroutine, as a
// test watches it: what it is waiting for, and the permit when it gets
// one.
type acquiring struct {
	got     chan func()
	reasons chan string
}

// queue starts a job at gate and returns the handle to watch it with. The
// channels are buffered because a test that stops reading them must not
// wedge the waiter it is asserting about.
//
// The job queues under a context the test's cleanup cancels, so a waiter
// a test deliberately leaves at the gate ends with the test rather than
// outliving the whole package -- and giving up that way is not an error
// it reports, because the test asked for it.
func queue(t *testing.T, gate *SharedGate, req GateRequest) *acquiring {
	t.Helper()
	a := &acquiring{got: make(chan func(), 1), reasons: make(chan string, 16)}
	wait := req.Wait
	req.Wait = func(w Wait) {
		if wait != nil {
			wait(w)
		}
		if w.Reason != "" {
			signal(a.reasons, w.Reason)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		release, err := gate.Acquire(ctx, req)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Acquire: %v", err)
			}
			return
		}
		a.got <- release
	}()
	return a
}

// counted wraps one job's request so that every attempt it makes at the
// gate is announced on attempt.
//
// SharedGate asks a waiter's Priority once per attempt (Acquire's loop,
// through rankOf), which makes "this job has asked again" a signal the
// gate itself raises. A test whose subject is repeated attempts -- that a
// job held for a long time is logged once rather than once per poll, that
// no number of further attempts turns a shortage into a reservation --
// waits for the attempts themselves rather than sleeping for long enough
// that several have probably happened by now (grain/task-431).
func counted(req GateRequest, attempt chan<- int) GateRequest {
	priority := req.Priority
	var made atomic.Int64
	req.Priority = func() Priority {
		signal(attempt, int(made.Add(1)))
		return rankOf(priority)
	}
	return req
}

// window is a gate setting that never changes, as SharedGate.AgeBoost and
// ReserveAfter now take one: a deployment reads them off its live
// configuration, and a test mostly wants one number.
func window(d time.Duration) func() time.Duration {
	return func() time.Duration { return d }
}

// waitingFor blocks until this job says what is holding it, and returns
// the first reason it gave.
func (a *acquiring) waitingFor(t *testing.T) string {
	t.Helper()
	return awaitSignal(t, a.reasons, "a job that should have been held at the gate never said it was waiting")
}

// admitted blocks until this job has a permit and returns the release, so
// that a test can hold it while it asserts about whoever is still waiting.
func (a *acquiring) admitted(t *testing.T, what string) func() {
	t.Helper()
	select {
	case release := <-a.got:
		return release
	case <-time.After(signalWaitLimit):
		t.Fatalf("%s was never admitted (nothing after %s)", what, signalWaitLimit)
		return func() {}
	}
}

// heldBy blocks until this job reports a reason matching want, and fails
// the test if it is admitted first or never says it at all. It returns
// the reason, so a caller can go on to assert about the words in it.
//
// This is how a test proves a job was *not* let in. A gate that refuses a
// job tells the job so, once per reason (GateRequest.Wait), so the
// refusal is a signal the gate itself raises and it arrives however
// loaded the machine is -- where "nothing happened in the next 20ms" only
// ever said the refusal had not been reversed by the time the test
// looked, on the strength of a number nobody could defend
// (grain/task-431). Where nothing in the test can free what the job
// needs, a reported refusal is the whole of the proof: every later
// attempt reads the same gate and gets the same answer.
func (a *acquiring) heldBy(t *testing.T, want string) string {
	t.Helper()
	limit := time.After(signalWaitLimit)
	var said []string
	for {
		select {
		case why := <-a.reasons:
			if strings.Contains(why, want) {
				return why
			}
			said = append(said, why)
		case release := <-a.got:
			a.got <- release
			t.Fatalf("this job was admitted rather than held %q; it had said %q", want, said)
			return ""
		case <-limit:
			t.Fatalf("no reason matching %q after %s; what it did say was %q", want, signalWaitLimit, said)
			return ""
		}
	}
}

// holdsNothing asserts this job has no permit, without waiting: a permit
// it did get is put back where admitted will find it, since this asks a
// question about the job and must not consume its answer.
//
// It is a complete proof only where the test has already accounted for
// the capacity this job would need -- somebody else holds it and nothing
// has given it back -- because then a job admitted at any point would
// have taken a permit the gate did not have. Each caller says which
// permit that is. A gate that still had room would need heldBy above
// instead, which waits for the gate's own refusal rather than for a
// quiet moment.
func (a *acquiring) holdsNothing(t *testing.T, what string) {
	t.Helper()
	select {
	case release := <-a.got:
		a.got <- release
		t.Error(what)
	default:
	}
}

// The first thing an order has to do: the branch about to merge goes
// first, however long the ordinary work behind it has been in line. Age
// is the tiebreak, not the rule (that is the next test along).
func TestSharedGateAdmitsHigherPriorityWorkAheadOfWhatArrivedFirst(t *testing.T) {
	gate := &SharedGate{
		Limit: func() int { return 1 },
		Poll:  time.Millisecond,
		// The boost off, so this test is about priority alone -- with it
		// on, a long enough wait would admit the first job on its age and
		// prove nothing about rank.
		AgeBoost: window(-1),
	}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ordinary := queue(t, gate, GateRequest{})
	ordinary.waitingFor(t)
	// Queued second, and worth more: the change at the head of a merge
	// queue is the one whose verdict lets everything behind it move.
	head := queue(t, gate, GateRequest{Priority: func() Priority { return PriorityMergeHead }})
	head.waitingFor(t)

	// One permit and two waiters, so which of them has it is the whole
	// question: the ordinary job must still be waiting while the head
	// holds it.
	held()
	release := head.admitted(t, "the merge-queue head's job, queued second")
	// The head holding the gate's only permit is the whole answer: the
	// ordinary job could not have been admitted at any point without the
	// gate having handed out two, so this is a complete check rather than
	// a glance during a quiet moment. The pre-ordering behaviour this
	// guards against fails above instead, at the head's own admission --
	// whoever reached the mutex first won, and half the time that was the
	// job that had arrived first.
	ordinary.holdsNothing(t, "the one permit went to the job that arrived first rather than to the higher-ranked one")
	if got := gate.inUse(); got != 1 {
		t.Errorf("one permit is out, held by the merge-queue head: got %d", got)
	}
	release()
	ordinary.admitted(t, "the ordinary job, once the higher-ranked one had had its turn")()
}

// And the job that is held says so, in its own words: "behind
// higher-priority work" is a different answer from "this deployment is
// full", and a job held for forty minutes should say which of the two it
// is (grain/task-170, one layer down).
//
// A gate that is full by the count says the count, which is the honest
// answer -- nothing is being kept for anybody, the deployment simply has
// nothing free. The order is what a job hears when there *is* room and
// this deployment is spending it on something else, so the higher-ranked
// job here is parked between attempts (its Priority callback blocks) while
// the count frees underneath it.
func TestSharedGateSaysWhenAJobIsHeldBehindHigherPriorityWork(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 1 }, Poll: time.Millisecond, AgeBoost: window(-1)}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ordinary := queue(t, gate, GateRequest{})
	if why := ordinary.waitingFor(t); !strings.Contains(why, "this deployment keeps for CI") {
		t.Errorf("a job held by the count says so: got %q", why)
	}

	var asked atomic.Int32
	letHeadAsk := make(chan struct{})
	// Wake the parked job on the way out whatever happens, including when
	// an assertion below gives up: a goroutine left blocked on this
	// channel outlives the test.
	var woke sync.Once
	letHeadGo := func() { woke.Do(func() { close(letHeadAsk) }) }
	t.Cleanup(letHeadGo)
	head := queue(t, gate, GateRequest{Priority: func() Priority {
		if asked.Add(1) > 1 {
			<-letHeadAsk
		}
		return PriorityMergeHead
	}})
	head.waitingFor(t)
	// The count frees, and the job it frees for is asleep: what holds the
	// ordinary job from here is the order rather than the limit, and it
	// says the other thing.
	held()

	if why := ordinary.heldBy(t, "behind higher-priority work"); !strings.Contains(why, "queued ahead of it") {
		t.Errorf("and says how much is ahead of it: got %q", why)
	}
	// And the work it was held for goes, followed by the work that waited
	// for it: neither is stuck.
	letHeadGo()
	head.admitted(t, "the higher-ranked job, once it asked again")()
	ordinary.admitted(t, "the ordinary job, once the higher-ranked one had had its turn")()
}

// The big-hole problem, which is the one that was actually biting: a
// 16 GiB job never runs if capacity only ever frees in 4 GiB slivers,
// whatever its place in the order. Past ReserveAfter the gate holds the
// released capacity for it instead of admitting anyone smaller.
func TestSharedGateHoldsCapacityForAJobNothingSmallFitsAround(t *testing.T) {
	gate := budgetGate(15, nil)
	gate.ReserveAfter = window(30 * time.Millisecond)
	// 52 GiB of a 64 GiB budget held: room for another 4 GiB job, and not
	// for another 16 GiB one.
	var big []func()
	for i := 0; i < 3; i++ {
		release, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()})
		if err != nil {
			t.Fatalf("16 GiB job %d of 3: %v", i+1, err)
		}
		big = append(big, release)
	}
	filler, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("the 4 GiB job holding the rest: %v", err)
	}
	defer filler()

	expensive := queue(t, gate, GateRequest{Want: bigJob()})
	// First refused for the budget in general, and then -- once it has
	// been refused for the whole window -- for a hole of its own shape.
	if why := expensive.heldBy(t, "a hole big enough"); !strings.Contains(why, "holding capacity") {
		t.Errorf("the reason says what the deployment is doing about it: got %q", why)
	}

	// A 4 GiB job that fits the 12 GiB still free. Before this change it
	// would take it, and the 16 GiB job would go on waiting; that is the
	// bias, and it is what a reservation is for. The reservation is on and
	// nothing releases while it is, so the gate telling this job it is
	// behind higher-priority work is the refusal itself -- where a quiet
	// 100ms only said it had not been let in yet.
	small := queue(t, gate, GateRequest{Want: smallJob()})
	small.heldBy(t, "behind higher-priority work")

	// And the reservation ends in the job running rather than in a wedge:
	// one 16 GiB release makes the hole, and it takes it.
	big[0]()
	expensive.admitted(t, "the 16 GiB job the capacity was held for")()
	small.admitted(t, "the small job, once the reservation it was behind was over")()
	for _, release := range big[1:] {
		release()
	}
}

// A shortage every job is under, whatever it asks for, is not a hole of
// anybody's shape: a volume below its free-space floor refuses a job that
// commits nothing at all, and capacity held back for a waiter would not
// make a byte of disk appear. So the reservation clock does not run on one
// (grain/task-196's half of the budget).
func TestSharedGateDoesNotReserveCapacityForAVolumeUnderItsFloor(t *testing.T) {
	disk := &freeSpace{mb: 20 * 1024} // 20 GiB of 443, against a 44 GiB floor
	gate := diskGate(15, disk.get)
	// A window that has always already elapsed, so a gate that started the
	// reservation clock on this waiter at all would call the shortage a
	// hole on the job's very next attempt. That is what makes this
	// countable in attempts rather than in milliseconds: the assertion is
	// "however many times it asks, the answer is the volume", and waiting
	// out some made-up number of 5ms windows was only ever a slower way of
	// asking a few times (grain/task-431).
	gate.ReserveAfter = window(time.Nanosecond)

	attempts := make(chan int, 16)
	waiter := queue(t, gate, counted(GateRequest{Want: bigJob()}, attempts))
	if why := waiter.waitingFor(t); !strings.Contains(why, "disk") {
		t.Fatalf("a job held by the volume says so: got %q", why)
	}
	for asked := 0; asked < 5; {
		select {
		case <-attempts:
			asked++
		case why := <-waiter.reasons:
			if strings.Contains(why, "hole big enough") {
				t.Fatalf("a volume under its floor was reported as a hole of this job's shape: %q", why)
			}
		case release := <-waiter.got:
			release()
			t.Fatal("a job was admitted while the volume was under its floor")
		case <-time.After(signalWaitLimit):
			t.Fatalf("the job held by the volume stopped asking again after %d attempts", asked)
		}
	}

	// And it goes when the volume does, which is also how the waiter's own
	// goroutine ends rather than outliving the test.
	disk.set(400 * 1024)
	waiter.admitted(t, "the job, once the volume had room")()
}

// The floor under the whole arrangement: a priority order starves the
// bottom of it unless age eventually dominates rank. Here a stream of
// merge-queue-head work keeps arriving, so there is never a moment when
// the ordinary job is the best-ranked thing waiting -- and it runs
// anyway, after the four AgeBoosts that make up the gap.
func TestSharedGateDoesNotStarveALowRankedJobBehindHigherPriorityWork(t *testing.T) {
	// The boost is this deployment's own two minutes, and the clock is the
	// test's: what this property is about is the age the gate *reads*, and
	// with that in hand nobody has to wait a boost out. A real 50ms boost
	// made the assertion below -- that an ordinary job has not caught up
	// two boosts in -- a race between the test and the machine under it,
	// because the job's age was real time and 100ms of it is nothing on a
	// host running the whole suite at once (grain/task-431).
	const boost = 2 * time.Minute
	clock := newTestClock()
	gate := &SharedGate{
		Limit:    func() int { return 2 },
		Poll:     time.Millisecond,
		AgeBoost: window(boost),
		Now:      clock.Now,
	}
	// One of the two permits held for the whole test, so the stream below
	// and the ordinary job are always competing for exactly one.
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	// Two goroutines taking turns, so that whenever one gives the permit
	// back the other is already queueing for it: the ordinary job is never
	// alone at the gate, and nothing but its own age can get it in.
	stop := make(chan struct{})
	queueing := make(chan struct{}, 2)
	var stream sync.WaitGroup
	for i := 0; i < 2; i++ {
		stream.Add(1)
		go func() {
			defer stream.Done()
			told := false
			for {
				select {
				case <-stop:
					return
				default:
				}
				release, err := gate.Acquire(context.Background(), GateRequest{
					Priority: func() Priority { return PriorityMergeHead },
					Wait: func(w Wait) {
						if w.Reason != "" && !told {
							told = true
							queueing <- struct{}{}
						}
					},
				})
				if err != nil {
					return
				}
				time.Sleep(5 * time.Millisecond)
				release()
			}
		}()
	}
	defer func() {
		close(stop)
		stream.Wait()
	}()
	// Wait until the stream has the gate to itself -- one holding, one
	// queueing -- so that the ordinary job joins a gate that is already
	// full of work ranked above it rather than walking into a gap.
	awaitSignal(t, queueing, "the higher-priority stream never filled the gate")

	attempts := make(chan int, 8)
	ordinary := queue(t, gate, counted(GateRequest{}, attempts))
	if why := ordinary.waitingFor(t); why == "" {
		t.Error("the ordinary job was not held at all")
	}

	// Two boosts in: two of the four ranks that separate an ordinary job
	// from a merge-queue head, so the order is still holding it -- and the
	// clock stops there, so nothing the machine does next can age it any
	// further. Three attempts at that reading, all refused, is the gate
	// applying the order rather than the test happening to look during a
	// busy moment.
	clock.advance(2 * boost)
	for i := 0; i < 3; i++ {
		awaitSignal(t, attempts, "the ordinary job stopped asking again")
	}
	ordinary.holdsNothing(t, "an ordinary job was admitted ahead of higher-priority work before its age had caught up")

	// And four boosts in, the gap is closed: it is the oldest thing
	// waiting at a rank the stream can no longer beat, and it goes.
	clock.advance(2 * boost)
	ordinary.admitted(t, "an ordinary job while higher-priority work kept arriving -- it starved")()
}

// Both windows are operator settings, re-read per attempt like the limit
// and the budget beside them (model.Limits.CIAgeBoost, pkg/cli/ci.go's
// own closures): a job that has been queueing for half an hour is exactly
// the job an operator shortening the boost is trying to rescue, so a
// waiter already in line has to see the new number without being told
// about it and without re-joining.
func TestSharedGateReadsTheAgeBoostAgainOnEveryAttempt(t *testing.T) {
	// An hour is "off" as far as a test's own clock is concerned, so
	// while this is what the gate reads, rank alone decides.
	boost := make(chan time.Duration, 1)
	boost <- time.Hour
	current := func() time.Duration {
		d := <-boost
		boost <- d
		return d
	}
	clock := newTestClock()
	gate := &SharedGate{Limit: func() int { return 1 }, Poll: time.Millisecond, AgeBoost: current, Now: clock.Now}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ordinary := queue(t, gate, GateRequest{})
	ordinary.waitingFor(t)
	// Half an hour of queueing, which is worth nothing against the hour
	// the gate is reading and six steps against the five minutes it is
	// about to read -- more than the four that separate an ordinary job
	// from a merge-queue head, so the order below turns over rather than
	// tying. On the test's clock, because what the gate does with an age
	// is the subject and how long this test takes is not.
	clock.advance(30 * time.Minute)
	<-boost
	boost <- 5 * time.Minute

	// Queued after the setting changed and ranked four steps above it: the
	// older job wins anyway, on an age it accrued while the boost said
	// something else.
	head := queue(t, gate, GateRequest{Priority: func() Priority { return PriorityMergeHead }})
	head.waitingFor(t)
	held()
	release := ordinary.admitted(t, "the job whose age the shortened boost had caught up")
	// The one permit is accounted for -- the older job has it -- so the
	// head cannot have been admitted at any point: see acquiring.holdsNothing.
	head.holdsNothing(t, "the merge-queue head took the permit: the gate was still ranking on the boost it started with")
	release()
	head.admitted(t, "the merge-queue head, once the older job had had its turn")()
}

// The same question of the other window: a deployment that finds the
// reservation too eager lengthens it, or shortens one it finds too slow,
// and the job the budget is already refusing is the one that has to feel
// it.
func TestSharedGateReadsTheReservationWindowAgainOnEveryAttempt(t *testing.T) {
	after := make(chan time.Duration, 1)
	after <- time.Hour
	gate := budgetGate(15, nil)
	gate.ReserveAfter = func() time.Duration {
		d := <-after
		after <- d
		return d
	}
	// 52 GiB of a 64 GiB budget held: room for a 4 GiB job, not for a
	// 16 GiB one.
	var big []func()
	for i := 0; i < 3; i++ {
		release, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()})
		if err != nil {
			t.Fatalf("16 GiB job %d of 3: %v", i+1, err)
		}
		big = append(big, release)
	}
	defer func() {
		for _, release := range big {
			release()
		}
	}()
	filler, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("the 4 GiB job holding the rest: %v", err)
	}
	defer filler()

	expensive := queue(t, gate, GateRequest{Want: bigJob()})
	if why := expensive.waitingFor(t); strings.Contains(why, "a hole big enough") {
		t.Fatalf("capacity was held back an hour before the window this deployment set: %q", why)
	}
	// Nothing is being kept for it yet, so a small job walks past.
	first, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("the 4 GiB job the long window lets through: %v", err)
	}
	first()

	<-after
	after <- time.Millisecond
	// The waiter has been refused for far longer than the new window
	// already, so the reservation is on from its very next attempt --
	// without it re-joining, and without anything having woken it for the
	// change.
	expensive.heldBy(t, "a hole big enough")
	// And with the reservation on, the small job that would have walked
	// past a moment ago is held behind it -- which the gate says to that
	// job rather than the test inferring it from a quiet 100ms.
	small := queue(t, gate, GateRequest{Want: smallJob()})
	small.heldBy(t, "behind higher-priority work")
	big[0]()
	big = big[1:]
	expensive.admitted(t, "the 16 GiB job the capacity was held for")()
	small.admitted(t, "the small job, once the reservation it was behind was over")()
}

// The race safety the ordering had to keep: a waiter subscribes before it
// tests, so a release landing between the test and the select still wakes
// it. The wait callback is called in exactly that window, which is what
// makes this a test of the property rather than of the poll -- the poll
// here is longer than the test would ever wait.
func TestSharedGateWakesAWaiterReleasedBetweenItsTestAndItsSelect(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 1 }, Poll: time.Hour}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	var once sync.Once
	got := make(chan func(), 1)
	go func() {
		release, err := gate.Acquire(context.Background(), GateRequest{Wait: func(w Wait) {
			if w.Reason == "" {
				return
			}
			// Refused, not yet listening: this is the window.
			once.Do(held)
		}})
		if err != nil {
			t.Errorf("Acquire: %v", err)
			return
		}
		got <- release
	}()

	awaitSignal(t, got, "a release between a waiter's test and its select never woke it")()
}

// The three kinds of waiting an ordered gate can do, said in three
// distinguishable ways. A job held for forty minutes is indistinguishable
// from a slow one unless it says which of these it is.
func TestSharedGateNamesEachKindOfWaitingDifferently(t *testing.T) {
	deployment := &SharedGate{Limit: func() int { return 3 }}
	kinds := map[string]Wait{
		"the count": deployment.countWait(3, 3),
		"one run's own width": (&SharedGate{
			Limit: func() int { return 3 }, OneRun: true,
		}).countWait(3, 3),
		"the order": priorityWait(2),
		"a big enough hole": holeWait(bigJob(),
			"memory: 52.0 GiB is committed of this deployment's 64.0 GiB sandbox budget"),
	}
	for name, why := range kinds {
		if why.Reason == "" {
			t.Errorf("%s: no reason at all", name)
		}
		// And a cause to attribute the wait to afterwards, which is the
		// half of a wait that can be added up: the sentence moves as the
		// deployment does, and a job held by one thing for forty minutes
		// must not go down as a dozen unrelated short waits
		// (ci.JobRun.QueuedReason).
		if why.Cause == "" {
			t.Errorf("%s: a reason with no cause to count it under: %q", name, why.Reason)
		}
		for other, sibling := range kinds {
			if other != name && why == sibling {
				t.Errorf("%s and %s report the same thing: %q", name, other, why.Reason)
			}
			if other != name && why.Cause == sibling.Cause {
				t.Errorf("%s and %s are counted as the same cause: %q", name, other, why.Cause)
			}
		}
	}
	if !strings.Contains(kinds["the count"].Reason, "keeps for CI") {
		t.Errorf("the count says it is the count: %q", kinds["the count"].Reason)
	}
	if !strings.Contains(kinds["one run's own width"].Reason, "this run may hold") {
		t.Errorf("a run held by its own width says so rather than blaming the deployment: %q",
			kinds["one run's own width"].Reason)
	}
	if !strings.Contains(kinds["the order"].Reason, "higher-priority") {
		t.Errorf("the order says it is the order: %q", kinds["the order"].Reason)
	}
	if !strings.Contains(kinds["a big enough hole"].Reason, "hole big enough") {
		t.Errorf("a reservation says it is a reservation: %q", kinds["a big enough hole"].Reason)
	}
	if !strings.Contains(kinds["a big enough hole"].Reason, "sandbox budget") {
		t.Errorf("and still says what the budget made of it: %q", kinds["a big enough hole"].Reason)
	}
}

// The order holds jobs at a per-run gate too -- every job joins the
// waiter set before its first attempt, so the second job of a run is
// behind the first from the moment it gets in line, whether or not the
// first has taken the slot yet. What it must not do there is answer with
// the deployment's sentence: a run's jobs are ranked by nothing, so
// "higher-priority work this deployment would rather run first" describes
// a priority nobody set, and sends whoever reads it to max_ci_jobs rather
// than to the run's own width. Both of a per-run gate's refusals name
// that width (grain/task-477).
func TestPerRunGateHeldByItsOwnOrderStillNamesTheRunsWidth(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 2 }, OneRun: true}
	// The run's first job: in line, and not admitted yet.
	gate.join(model.Commitment{})
	second := gate.join(model.Commitment{})

	_, why, ok := gate.take(second, PriorityNormal)

	if ok {
		t.Fatal("the second job went ahead of the first: a per-run gate is still FIFO among its own jobs")
	}
	if why.Cause != WaitForRun {
		t.Errorf("cause = %q, want %q: this job is held by its own run, not by the deployment",
			why.Cause, WaitForRun)
	}
	if !strings.Contains(why.Reason, "this run may hold") {
		t.Errorf("reason = %q, want it to name the run's own limit", why.Reason)
	}
	if strings.Contains(why.Reason, "this deployment") {
		t.Errorf("reason = %q, want it to blame this run rather than the deployment", why.Reason)
	}
}

// Engine.Priority is what a deployment ranks a whole run with, and the
// step for a run that has finished jobs already is added to it: what a job
// asks the gate for is both together, read when the job asks rather than
// when the run started.
func TestEngineRanksARunByItsCallerAndByWhatItHasFinished(t *testing.T) {
	gate := &recordingGate{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  first:\n    steps:\n      - run: a\n" +
			"  second:\n    needs: [first]\n    steps:\n      - run: b\n",
	})
	engine := &Engine{
		Sandboxes: &fakeSandboxes{},
		Jobs:      gate,
		Priority:  func(req Request) Priority { return PriorityMergeHead },
	}
	if run := engine.Run(context.Background(), "r1", plan, nil); run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.ranks) != 2 {
		t.Fatalf("one rank per job that asked for a sandbox: got %v", gate.ranks)
	}
	if gate.ranks[0] != PriorityMergeHead {
		t.Errorf("the first job asks at what the caller ranks the run: got %d, want %d",
			gate.ranks[0], PriorityMergeHead)
	}
	// `needs` makes this deterministic: the first job is finished before
	// the second asks for anything.
	if want := PriorityMergeHead + PriorityUnderway; gate.ranks[1] != want {
		t.Errorf("a run with a job finished outranks one with none: got %d, want %d", gate.ranks[1], want)
	}
}

// recordingGate admits everything and writes down what each job said it
// was worth.
type recordingGate struct {
	mu    sync.Mutex
	ranks []Priority
}

func (g *recordingGate) Acquire(ctx context.Context, req GateRequest) (func(), error) {
	g.mu.Lock()
	g.ranks = append(g.ranks, rankOf(req.Priority))
	g.mu.Unlock()
	return func() {}, nil
}
