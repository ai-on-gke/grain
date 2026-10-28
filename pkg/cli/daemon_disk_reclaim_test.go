package cli

// The other half of grain/task-196's trade, as grain/task-213 settled it:
// what a deployment does about a volume it has just found under its
// floor. The volume is stubbed for daemon_disk_floor_test.go's reason --
// filling a 443 GB one is not a unit test -- and each step's own
// reclaiming is covered where it lives (ci.DirArtifacts.Clear,
// orchestrator.GranuleSandboxes.SweepOrphans).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// reclaimHarness is a volume whose free space the steps change, which is
// the one thing a reclaim is: a sequence of actions judged by what the
// filesystem says afterwards.
type reclaimHarness struct {
	mu      sync.Mutex
	totalMB int
	usedMB  int
	lines   []string
	ran     []string
}

func newReclaimHarness(totalMB, freeMB int) *reclaimHarness {
	return &reclaimHarness{totalMB: totalMB, usedMB: totalMB - freeMB}
}

func (h *reclaimHarness) space() model.DiskSpace {
	h.mu.Lock()
	defer h.mu.Unlock()
	return model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: h.totalMB, FreeMB: h.totalMB - h.usedMB}
}

func (h *reclaimHarness) log(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (h *reclaimHarness) journal() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.lines, "\n")
}

func (h *reclaimHarness) steps() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ran...)
}

// step is one reclaim step that frees freesMB and says so.
func (h *reclaimHarness) step(name string, freesMB int, err error) daemon.ReclaimStep {
	return daemon.ReclaimStep{What: name, Run: func(context.Context) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.ran = append(h.ran, name)
		if freesMB == 0 {
			return "", err
		}
		h.usedMB -= freesMB
		return name, err
	}}
}

func (h *reclaimHarness) reclaim(steps ...daemon.ReclaimStep) *daemon.DiskReclaim {
	return &daemon.DiskReclaim{
		Steps: steps, Read: h.space, FloorOf: model.DefaultDiskFloor,
		Log: h.log, Now: time.Now,
	}
}

// A sweep stops at the first reading over the target, so the steps that
// cost something are not paid for by a deployment the cheap one already
// rescued.
func TestDiskReclaimStopsAtTheTargetRatherThanRunningEveryStep(t *testing.T) {
	// 443 GB, 40 GiB free: under the 44.3 GiB floor, and well under the
	// 66.4 GiB target.
	h := newReclaimHarness(443*1024, 40*1024)
	r := h.reclaim(
		h.step("orphaned VMs", 30*1024, nil),
		h.step("the job cache", 200, nil),
		h.step("artifacts", 2048, nil),
	)

	r.Sweep(context.Background())

	if got := h.steps(); len(got) != 1 || got[0] != "orphaned VMs" {
		t.Errorf("steps run = %v, want only the first: the volume was over its target after it", got)
	}
	journal := h.journal()
	for _, want := range []string{"reclaiming disk", "orphaned VMs", "admitted again"} {
		if !strings.Contains(journal, want) {
			t.Errorf("the journal does not say %q:\n%s", want, journal)
		}
	}
}

// And goes on through the steps while the volume is still short, which
// is the case a deployment is actually in when its overlays are the
// problem.
func TestDiskReclaimGoesOnUntilItRunsOutOfStepsAndSaysItIsStillShort(t *testing.T) {
	h := newReclaimHarness(443*1024, 10*1024)
	r := h.reclaim(
		h.step("orphaned VMs", 1024, nil),
		h.step("the job cache", 256, nil),
		h.step("artifacts", 2048, nil),
	)

	r.Sweep(context.Background())

	if got := h.steps(); len(got) != 3 {
		t.Errorf("steps run = %v, want all three on a volume none of them rescued", got)
	}
	journal := h.journal()
	if !strings.Contains(journal, "still short") {
		t.Errorf("a sweep that did not reach its target claims otherwise:\n%s", journal)
	}
	// The figures are the volume's own readings, before and after, rather
	// than a sum of what the steps removed: the runs go on writing while
	// this sweeps.
	if !strings.Contains(journal, "13.2 GiB free") || !strings.Contains(journal, "up from 10.0") {
		t.Errorf("the journal does not say what the volume did:\n%s", journal)
	}
}

// A step that fails is reported and stepped over. A reclaim that gave up
// on the first error would leave the space the later steps could have
// freed, on a deployment that is already holding work back.
func TestDiskReclaimStepsOverAFailingStep(t *testing.T) {
	h := newReclaimHarness(443*1024, 10*1024)
	r := h.reclaim(
		h.step("orphaned VMs", 0, errors.New("granule: vm delete g-t1-1: exit status 1")),
		h.step("the job cache", 60*1024, nil),
	)

	r.Sweep(context.Background())

	if got := h.steps(); len(got) != 2 {
		t.Fatalf("steps run = %v, want the sweep to have carried on past the failure", got)
	}
	journal := h.journal()
	if !strings.Contains(journal, "exit status 1") {
		t.Errorf("the failure was swallowed:\n%s", journal)
	}
	if !strings.Contains(journal, "the job cache") || !strings.Contains(journal, "admitted again") {
		t.Errorf("the sweep did not go on to succeed:\n%s", journal)
	}
}

// Nothing to reclaim is its own answer, and the one an operator most
// needs: the volume is full of work that is still going, and this
// deployment will go on holding new work until it finishes.
func TestDiskReclaimSaysSoWhenThereIsNothingToReclaim(t *testing.T) {
	h := newReclaimHarness(443*1024, 10*1024)
	r := h.reclaim(h.step("orphaned VMs", 0, nil), h.step("the job cache", 0, nil))

	r.Sweep(context.Background())

	journal := h.journal()
	if !strings.Contains(journal, "found nothing to reclaim") {
		t.Errorf("a sweep that reclaimed nothing did not say so:\n%s", journal)
	}
	if !strings.Contains(journal, "Settings -> Sandbox") {
		t.Errorf("the one line an operator can act on names no lever:\n%s", journal)
	}
}

// A volume over its target is not swept at all: the steps delete things,
// and a deployment that is admitting work has no reason to pay for that.
func TestDiskReclaimDoesNothingOnAVolumeWithRoom(t *testing.T) {
	h := newReclaimHarness(443*1024, 300*1024)
	r := h.reclaim(h.step("orphaned VMs", 1024, nil))

	r.Sweep(context.Background())

	if got := h.steps(); len(got) != 0 {
		t.Errorf("steps run = %v on a volume with 300 GiB free", got)
	}
	if h.journal() != "" {
		t.Errorf("a deployment nothing is wrong with wrote to the journal:\n%s", h.journal())
	}
}

// The window between the floor and the target: a deployment that is
// admitting work again is still worth another step of reclaim, because
// stopping on the floor is stopping at the reading the next write undoes.
func TestDiskReclaimSweepsPastTheReadingThatAdmitsAgain(t *testing.T) {
	floor := model.DefaultDiskFloor().FloorMB(443 * 1024)
	h := newReclaimHarness(443*1024, floor-512)
	r := h.reclaim(
		h.step("orphaned VMs", 1024, nil), // just over the floor, short of the target
		h.step("the job cache", 30*1024, nil),
	)

	r.Sweep(context.Background())

	if got := h.steps(); len(got) != 2 {
		t.Errorf("steps run = %v, want the sweep to carry on past the floor to the target", got)
	}
}

// One sweep at a time, and not again straight away: the two callers are
// an admission check and a reconcile tick, and both go on arriving while
// a sweep is deleting VMs.
func TestDiskReclaimIsSingleFlightedAndPaced(t *testing.T) {
	h := newReclaimHarness(443*1024, 10*1024)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	r := h.reclaim(daemon.ReclaimStep{What: "a slow step", Run: func(context.Context) (string, error) {
		entered <- struct{}{}
		<-release
		h.mu.Lock()
		defer h.mu.Unlock()
		h.ran = append(h.ran, "a slow step")
		return "", nil
	}})

	done := make(chan struct{})
	go func() { defer close(done); r.Sweep(context.Background()) }()
	<-entered

	// A second caller arriving mid-sweep does not start another one.
	r.Sweep(context.Background())
	close(release)
	<-done

	if got := h.steps(); len(got) != 1 {
		t.Errorf("the step ran %d times, want one sweep at a time", len(got))
	}
	// And a third, straight afterwards, waits for the interval: the
	// deployment has to have done some work in between for a second sweep
	// to have anything new to find.
	r.Sweep(context.Background())
	if got := h.steps(); len(got) != 1 {
		t.Errorf("the step ran %d times, want the interval to have held the next sweep off", len(got))
	}
	r.Mu.Lock()
	r.Last = time.Now().Add(-2 * daemon.DiskReclaimInterval)
	r.Mu.Unlock()
	r.Sweep(context.Background())
	if got := h.steps(); len(got) != 2 {
		t.Errorf("the step ran %d times, want a sweep once the interval had passed", len(got))
	}
}

// fakeClock is a clock a test moves by hand, shared between a diskWatch
// and the diskReclaim behind it the way the daemon shares time.Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// The interval is measured from the start of a sweep, so a sweep that
// takes minutes does not push the next one a whole interval past the one
// the journal promised (grain/task-1472).
func TestDiskReclaimPacesFromTheStartOfASweep(t *testing.T) {
	h := newReclaimHarness(443*1024, 10*1024)
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: start}
	r := h.reclaim(daemon.ReclaimStep{What: "a slow step", Run: func(context.Context) (string, error) {
		clock.advance(4 * time.Minute)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.ran = append(h.ran, "a slow step")
		return "", nil
	}})
	r.Now = clock.now

	for i := 0; i < 4; i++ {
		clock.set(start.Add(time.Duration(i) * daemon.DiskReclaimInterval))
		r.Sweep(context.Background())
		if got := h.steps(); len(got) != i+1 {
			t.Fatalf("at %s the step had run %d times, want %d: a deployment that stays under its floor "+
				"is swept every %s", clock.now().Sub(start), len(got), i+1, daemon.DiskReclaimInterval)
		}
	}
	// Not straight after a sweep's start, though, however long it took.
	clock.set(start.Add(3*daemon.DiskReclaimInterval + daemon.DiskReclaimInterval/2))
	r.Sweep(context.Background())
	if got := h.steps(); len(got) != 4 {
		t.Errorf("the step ran %d times, want the interval to have held the next sweep off", len(got))
	}
}

// The same pacing as a daemon sees it: a deployment that stays under its
// floor repeats its held line every diskHoldRepeat, each repeat starts a
// sweep, and every one of those sweeps runs -- including when the one
// before it took minutes.
func TestDiskWatchSweepsAgainAtTheAdvertisedInterval(t *testing.T) {
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 433 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	cfg := floorCfg()
	cfg.SandboxDir = ""
	w := watchOn(cfg, vols, nil)
	if w == nil {
		t.Fatal("no watcher for a configuration with a floor")
	}
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: start}
	w.Now = clock.now

	swept := make(chan time.Duration, 8)
	reclaim := &daemon.DiskReclaim{Steps: []daemon.ReclaimStep{{What: "a slow step", Run: func(context.Context) (string, error) {
		swept <- clock.now().Sub(start)
		clock.advance(4 * time.Minute) // deleting VMs takes a while
		return "", nil
	}}}, Now: clock.now, Log: func(string, ...any) {}}
	daemon.ReclaimWith(w, context.Background(), reclaim)

	idle := func() bool {
		reclaim.Mu.Lock()
		defer reclaim.Mu.Unlock()
		return !reclaim.Running
	}
	for i := 0; i < 4; i++ {
		at := time.Duration(i) * daemon.DiskHoldRepeat
		clock.set(start.Add(at))
		w.Space()
		select {
		case got := <-swept:
			if got != at {
				t.Fatalf("a sweep started at %s, want %s", got, at)
			}
		case <-time.After(daemonWaitLimit):
			t.Fatalf("at %s the deployment was still under its floor and no sweep started; "+
				"the journal promised one every %s", at, daemon.DiskReclaimInterval)
		}
		deadline := time.Now().Add(daemonWaitLimit)
		for !idle() {
			if time.Now().After(deadline) {
				t.Fatal("a sweep never finished")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// The wiring: the crossing diskWatch already logs is what sets a sweep
// going, so a deployment that starts holding work back starts reclaiming
// in the same instant rather than at the next restart.
func TestDiskWatchStartsAReclaimOnTheCrossing(t *testing.T) {
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 433 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	cfg := floorCfg()
	cfg.SandboxDir = ""
	w := watchOn(cfg, vols, nil)
	if w == nil {
		t.Fatal("no watcher for a configuration with a floor")
	}

	swept := make(chan struct{}, 1)
	reclaim := &daemon.DiskReclaim{Steps: []daemon.ReclaimStep{{What: "a step", Run: func(context.Context) (string, error) {
		swept <- struct{}{}
		return "", nil
	}}}, Now: time.Now, Log: func(string, ...any) {}}
	daemon.ReclaimWith(w, context.Background(), reclaim)

	if space := w.Space(); space.FreeMB != 10*1024 {
		t.Fatalf("read %+v, want the stubbed volume", space)
	}
	select {
	case <-swept:
	case <-time.After(daemonWaitLimit):
		t.Fatal("a volume under its floor was found and nothing was reclaimed")
	}
}

// And a watcher nobody wired a reclaim onto goes on holding work back
// and reclaiming nothing -- which is where grain/task-196 left this, and
// is what every test of the floor alone still gets.
func TestDiskWatchWithNoReclaimJustHoldsWork(t *testing.T) {
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 433 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	cfg := floorCfg()
	cfg.SandboxDir = ""
	w := watchOn(cfg, vols, nil)
	daemon.ReclaimWith(w, context.Background(), &daemon.DiskReclaim{}) // no steps: not wired
	if w.Reclaim != nil {
		t.Error("a reclaim with no steps was wired anyway, so every crossing starts a goroutine to do nothing")
	}
	w.Space() // must not panic on the crossing
}

// What a deployment's backend can contribute: the VM sweep is the only
// step that depends on how this deployment sandboxes at all.
func TestNewDiskReclaimAddsTheVMSweepOnlyForABackendThatHasOne(t *testing.T) {
	withVMs := daemon.NewDiskReclaim(sweepingSandboxes{})
	if len(withVMs.Steps) != 4 {
		t.Errorf("a granule-style backend got %d steps, want the VM sweep, Docker prune, and the two CI ones", len(withVMs.Steps))
	}
	if !strings.Contains(withVMs.Steps[0].What, "orphaned sandbox VM") {
		t.Errorf("the first step is %q, want the one that costs nothing when it was not needed", withVMs.Steps[0].What)
	}
	if without := daemon.NewDiskReclaim(nil); len(without.Steps) != 3 {
		t.Errorf("a backend with no VMs got %d steps, want Docker prune and the two CI ones", len(without.Steps))
	}
}

// sweepingSandboxes is a backend that can sweep orphaned VMs and does
// nothing else -- newDiskReclaim only ever asks it the one question.
type sweepingSandboxes struct{ orchestrator.Sandboxes }

func (sweepingSandboxes) SweepOrphans(context.Context) (int, error) { return 0, nil }
