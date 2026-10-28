package orchestrator_test

// bwsalmon/agents#536: the sandbox health pane's own backend. These cover
// HostSandboxes.Health directly and reuse granule_sandboxes_test.go's own
// writeFakeGranule/writeFakeDockerGuest helpers -- the same real
// *mcp.DockerExecRunner path Acquire and ConfigureGitCredentials already
// exercise against a fake docker, rather than a mock of GranuleSandboxes
// itself -- for GranuleSandboxes.Health.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestHostSandboxesHealthReportsEveryLiveSandboxAsReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := orchestrator.NewHostSandboxes(t.TempDir())

	if got := h.Health(ctx); len(got) != 0 {
		t.Fatalf("Health with nothing running = %v, want empty", got)
	}
	if got := h.SandboxCount(); got != 0 {
		t.Fatalf("SandboxCount with nothing running = %d, want 0", got)
	}

	before := time.Now()
	first, err := h.Acquire(ctx, "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Acquire(ctx, "t2-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	got := h.Health(ctx)
	if len(got) != 2 {
		t.Fatalf("Health = %v, want 2 entries", got)
	}
	if count := h.SandboxCount(); count != 2 {
		t.Fatalf("SandboxCount = %d, want 2", count)
	}
	for _, entry := range got {
		if entry.Backend != "host" {
			t.Errorf("%s: Backend = %q, want %q", entry.Sandbox, entry.Backend, "host")
		}
		if !entry.Ready || entry.Error != "" {
			t.Errorf("%s: Ready/Error = %v/%q, want true/\"\"", entry.Sandbox, entry.Ready, entry.Error)
		}
		if entry.Name == "" {
			t.Errorf("%s: Name is empty, want the sandbox's own directory", entry.Sandbox)
		}
		if entry.StartedAt.Before(before) || entry.StartedAt.After(after) {
			t.Errorf("%s: StartedAt = %v, want when Acquire ran (between %v and %v)", entry.Sandbox, entry.StartedAt, before, after)
		}
	}

	// Releasing one drops it from the pane: what this reports is what is
	// running, not a fixed set of slots that are sometimes idle.
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	got = h.Health(ctx)
	if len(got) != 1 || got[0].Sandbox != "t2-1" {
		t.Fatalf("Health after releasing one = %v, want only t2-1", got)
	}
	if count := h.SandboxCount(); count != 1 {
		t.Fatalf("SandboxCount after releasing one = %d, want 1", count)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.Health(ctx); len(got) != 0 {
		t.Fatalf("Health after releasing both = %v, want empty", got)
	}
	if count := h.SandboxCount(); count != 0 {
		t.Fatalf("SandboxCount after releasing both = %d, want 0", count)
	}
}

func TestGranuleSandboxesHealthReportsLoadMemoryAndDiskOverSSH(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30082)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:    stateDir,
		SSHUser:     "debian",
		ExecKeyPath: "/images/key",
		Workspace:   "/workspace",
	})

	if got := k.Health(context.Background()); len(got) != 0 {
		t.Fatalf("Health before any VM exists = %v, want empty", got)
	}

	before := time.Now()
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{CPUs: 6, MemoryMB: 8192, DiskGB: 40, NetworkAccess: model.NetworkNone}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	got := k.Health(context.Background())
	if len(got) != 1 {
		t.Fatalf("Health = %v, want 1 entry", got)
	}
	h := got[0]
	if h.StartedAt.Before(before) || h.StartedAt.After(after) {
		t.Errorf("StartedAt = %v, want when Acquire ran (between %v and %v)", h.StartedAt, before, after)
	}
	if h.Sandbox != "t1-1" || h.Kind != "agent" || h.Backend != "granule" || h.Name != "g-t1-1" {
		t.Errorf("Sandbox/Kind/Backend/Name = %q/%q/%q/%q, want t1-1/agent/granule/g-t1-1", h.Sandbox, h.Kind, h.Backend, h.Name)
	}
	if h.CPUs != 6 || h.MemoryMB != 8192 || h.DiskGB != 40 || h.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("shape = %+v, want 6 vCPU/8192 MB/40 GB/none", h)
	}
	if h.Error != "" {
		t.Errorf("Error = %q, want none", h.Error)
	}
	if !h.Ready {
		t.Error("Ready = false, want true")
	}
	// What these figures were parsed out of is a real Linux guest's own
	// output, not a value shaped to suit the parser: the fake docker
	// script (writeFakeDockerGuest) answers guestStatsCommand with
	// /proc/loadavg, /proc/meminfo and `df -Pk /` as a Linux kernel and a
	// real `df` actually print them, recorded verbatim from a Linux VM
	// (fakeGuestStatsOutput, which says where from and why it is recorded
	// rather than read live on whatever host runs the suite -- a mac has
	// no /proc to read). So parseProcStats and parseDiskStats are being
	// fed exactly the format a production guest hands them, on every
	// platform; what they are not being fed is a live reading, so assert
	// that a figure was parsed at all and never on its value.
	if h.LoadAverage == "" {
		t.Error("LoadAverage is empty, want a real /proc/loadavg reading")
	}
	if h.MemoryTotalMB == 0 {
		t.Error("MemoryTotalMB = 0, want a real /proc/meminfo reading")
	}
	// Same again for the disk reading (grain/task-41): a real `df -Pk /`'s
	// output, carried in the same one command as the two /proc files
	// above.
	if h.DiskTotalMB == 0 {
		t.Error("DiskTotalMB = 0, want a real df reading")
	}
	if h.DiskUsedMB > h.DiskTotalMB {
		t.Errorf("DiskUsedMB = %d, want no more than DiskTotalMB (%d)", h.DiskUsedMB, h.DiskTotalMB)
	}
}

// A sandbox with no default route is a healthy sandbox, not a broken
// one: model.NetworkNone is a shape whose whole content is that route
// being taken away (enforceGuestNetwork), and the guest goes on answering
// and goes on having a load average worth reading.
//
// Health used to reach its guest through waitForGuestReady, which asks
// for a default route because Acquire needs one -- so every sandbox a
// task asked for no network on sat in this pane permanently red, saying
// "answers but has no default route yet" after spending the entire health
// budget establishing it, on every five-second poll (grain/task-134).
func TestGranuleSandboxesHealthReportsAGuestWithNoDefaultRouteAsReady(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30084)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      time.Second,
		ReadyPollInterval: 5 * time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{NetworkAccess: model.NetworkNone}); err != nil {
		t.Fatal(err)
	}

	// The guest as enforceGuestNetwork leaves it: every command still
	// answers, and /proc/net/route no longer has a default route in it.
	writeFakeDockerGuestNetwork(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter2"), 0, home, 1000)

	got := k.Health(context.Background())
	if len(got) != 1 {
		t.Fatalf("Health = %v, want 1 entry", got)
	}
	if got[0].Error != "" {
		t.Errorf("Error = %q, want none -- a sandbox with no network is not a sandbox that is down", got[0].Error)
	}
	if !got[0].Ready {
		t.Error("Ready = false, want true for a guest that answers without a default route")
	}
	if got[0].LoadAverage == "" || got[0].MemoryTotalMB == 0 {
		t.Errorf("LoadAverage/MemoryTotalMB = %q/%d, want real readings from a guest that is answering",
			got[0].LoadAverage, got[0].MemoryTotalMB)
	}
}

// A sandbox that was acquired fine and has since stopped answering is
// exactly what this pane exists to surface (bwsalmon/agents#536): it is
// reported with an Error rather than dropped, or waited on for the whole
// boot-length ReadyTimeout.
//
// The previous version of this covered a VM that never became reachable
// at all, which was a state a slot's sandbox could sit in indefinitely --
// ensure() marked it created the moment `granule vm create` returned,
// whether or not the guest ever came up. A failed Acquire now deletes the
// VM and registers nothing, so that state no longer exists; a VM that
// dies under a live run does.
func TestGranuleSandboxesHealthReportsErrorWhenAVMStopsAnswering(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30083)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      20 * time.Millisecond,
		ReadyPollInterval: 5 * time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	// Shadow the fake docker with one that never answers, so the guest
	// this run is holding stops responding without the handle knowing.
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter2"), 1000, "")

	// A caller's own deadline, rather than this test sitting out the
	// whole healthTimeout: that budget is deliberately generous now, so
	// that a guest which is merely slow under a loaded host is not
	// reported as broken (see healthTimeout), and Health retries until it
	// runs out. What is under test here is which answer a guest that
	// never comes back gets, not how long grain is willing to wait for
	// one, and the shorter of the two deadlines is the one that applies.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	got := k.Health(ctx)
	if len(got) != 1 {
		t.Fatalf("Health = %v, want 1 entry -- an unreachable sandbox is still one this run holds", got)
	}
	if got[0].Ready {
		t.Error("Ready = true, want false for a guest that stopped answering")
	}
	if got[0].Error == "" {
		t.Error("Error is empty, want a reason the sandbox is not reachable")
	}
}

// What one poll of the health pane actually costs the host, which is the
// number ui.sandboxHealthTTL's coalescing is measured against: one guest
// command per live sandbox and nothing else -- no separate readiness
// probe, no second round trip for the disk or the nested-virtualization
// reading (guestStatsCommand asks for all of them at once), and no
// `docker inspect` unless a probe has actually failed.
//
// Measured rather than asserted from the source: the fake docker logs one
// line per invocation, so this counts the real process launches a sweep
// makes. Eight sandboxes cost eight, and the same eight cost eight again
// for every caller that asks -- which is why GET /api/sandboxes now
// shares one sweep between its callers rather than running one each
// (grain/task-223).
func TestGranuleSandboxesHealthCostsOneGuestCommandPerSandbox(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30084)
	home := t.TempDir()
	dockerArgv := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuest(t, dockerArgv, filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:    stateDir,
		SSHUser:     "debian",
		ExecKeyPath: "/images/key",
		Workspace:   "/workspace",
	})
	names := []string{"t1-1", "t2-1", "t3-1", "c4-1", "c5-1", "t6-1", "t7-1", "t8-1"}
	for _, name := range names {
		if _, err := k.Acquire(context.Background(), name, orchestrator.Shape{}); err != nil {
			t.Fatal(err)
		}
	}

	before := countLines(t, dockerArgv)
	got := k.Health(context.Background())
	if len(got) != len(names) {
		t.Fatalf("Health = %d entries, want %d", len(got), len(names))
	}
	for _, entry := range got {
		if !entry.Ready {
			t.Fatalf("%s: Ready = false (%s), want a healthy sandbox", entry.Sandbox, entry.Error)
		}
	}
	if calls := countLines(t, dockerArgv) - before; calls != len(names) {
		t.Errorf("one sweep of %d sandboxes ran %d guest commands, want %d -- one per sandbox", len(names), calls, len(names))
	}
}

// What the *second* sweep of a sandbox that failed the first one costs,
// which is the whole of grain/task-363: nothing at all. A guest that has
// spent a full health budget refusing to answer has already had the
// retry loop's question put to it, so re-asking it every few seconds
// buys an answer the host already has, at twenty seconds of docker work
// a time -- on the host whose load is usually why somebody opened this
// pane.
//
// Measured rather than asserted from the source, the same way
// TestGranuleSandboxesHealthCostsOneGuestCommandPerSandbox above measures
// the healthy case: the fake docker logs one line per invocation, so
// this counts real process launches.
func TestGranuleSandboxesHealthDoesNotReprobeAGuestThatJustFailed(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30085)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      20 * time.Millisecond,
		ReadyPollInterval: 5 * time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	// Shadow the fake docker with one whose guest never answers, as
	// TestGranuleSandboxesHealthReportsErrorWhenAVMStopsAnswering does.
	dockerArgv := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuest(t, dockerArgv, filepath.Join(t.TempDir(), "counter2"), 1_000_000, "")

	// A caller's own deadline rather than the whole healthTimeout, for
	// the reason that test gives: what is under test is which answer a
	// guest that is gone gets and what it costs, not how long grain is
	// willing to wait for one.
	//
	// Nothing below asserts how much probing fits inside it. That number
	// is a property of the host running the suite -- these are real
	// process launches, and under a loaded CI host one of them can be the
	// whole hundred milliseconds -- so all this sweep has to establish is
	// that it went to the guest at all, which is what makes the sweep
	// after it going nowhere mean something. That the full budget really
	// does retry is asserted by behaviour rather than by a stopwatch, in
	// TestGranuleSandboxesHealthReprobesASickGuestCheaplyUntilItIsDueAFullOne
	// (grain/task-395).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	first := k.Health(ctx)
	if len(first) != 1 || first[0].Error == "" {
		t.Fatalf("first sweep = %v, want one entry with an error", first)
	}
	if first[0].ProbeMS <= 0 {
		t.Errorf("ProbeMS = %d, want how long the probe that failed actually took", first[0].ProbeMS)
	}
	if calls := countLines(t, dockerArgv); calls == 0 {
		t.Fatalf("first sweep ran %d guest commands, want a probe that really went to the guest -- the sweep under test is the one after it", calls)
	}

	before := countLines(t, dockerArgv)
	second := k.Health(context.Background())
	if calls := countLines(t, dockerArgv) - before; calls != 0 {
		t.Errorf("the sweep after a failed one ran %d docker commands, want 0 -- it already knows this guest is not answering", calls)
	}
	if len(second) != 1 {
		t.Fatalf("second sweep = %v, want one entry -- a guest the breaker is open on is still one this run holds", second)
	}
	if second[0].Error != first[0].Error {
		t.Errorf("second sweep Error = %q, want the same failure the first reported (%q)", second[0].Error, first[0].Error)
	}
	if second[0].Ready {
		t.Error("Ready = true, want false -- the remembered reading is a failure")
	}
	if second[0].ProbeMS != first[0].ProbeMS {
		t.Errorf("second sweep ProbeMS = %d, want the failed probe's own %d: the row reports that probe",
			second[0].ProbeMS, first[0].ProbeMS)
	}
}

// And how a reader of that remembered row is meant to know it is reading
// one. ProbeMS and Error on a row the breaker answered from memory both
// describe a probe that ran some seconds ago and will go on describing
// it until the cooldown is up, so a pane drawing them as measurements
// taken just now shows an error chip for up to healthBreakerCooldown
// after the guest came back. The sweep's own age does not cover it: a
// remembered row costs no guest round trip, so the sweep carrying it is
// the fastest kind there is and arrives marked fresh.
func TestGranuleSandboxesHealthSaysHowOldARememberedRowIs(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30088)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      20 * time.Millisecond,
		ReadyPollInterval: 5 * time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	// A guest that answers first, because the age of a row that was
	// really probed is the other half of this: zero, whatever the clock
	// has been doing.
	healthy := k.Health(context.Background())
	if len(healthy) != 1 || !healthy[0].Ready {
		t.Fatalf("first sweep = %v, want one ready entry", healthy)
	}
	if healthy[0].ProbeAgeMS != 0 {
		t.Errorf("ProbeAgeMS = %d on a row this sweep probed, want 0", healthy[0].ProbeAgeMS)
	}

	base := time.Now()
	var elapsed time.Duration
	k.SetHealthClockForTest(func() time.Time { return base.Add(elapsed) })

	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter2"), 1_000_000, "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	failed := k.Health(ctx)
	if len(failed) != 1 || failed[0].Error == "" {
		t.Fatalf("sweep of a guest that stopped answering = %v, want one entry with an error", failed)
	}
	if failed[0].ProbeAgeMS != 0 {
		t.Errorf("ProbeAgeMS = %d on the failing sweep's own row, want 0 -- that probe ran in this sweep",
			failed[0].ProbeAgeMS)
	}

	// Inside the cooldown, so this sweep runs no probe at all and
	// repeats the one from five seconds ago.
	elapsed += 5 * time.Second
	remembered := k.Health(context.Background())
	if len(remembered) != 1 || remembered[0].Error != failed[0].Error {
		t.Fatalf("sweep inside the cooldown = %v, want the remembered failure %q", remembered, failed[0].Error)
	}
	if got, want := remembered[0].ProbeAgeMS, (5 * time.Second).Milliseconds(); got != want {
		t.Errorf("ProbeAgeMS = %d, want %d -- how long before this sweep the probe this row reports ran", got, want)
	}
}

// What the sweep after the cooldown costs -- one attempt rather than the
// ten a full budget makes -- and what brings the full budget back: a
// guest that is merely slow rather than gone would otherwise be written
// off by the one bad minute that tripped its breaker and never looked at
// properly again (healthBreakerFullInterval).
//
// The two halves are asserted the same way, by the cost of a sweep with
// no deadline of its own on it: the cheap one against a guest that never
// answers, where one attempt and the liveness check after it are the
// whole of it, and the full one against a guest that answers its second
// attempt, which is a reading only a sweep that retries comes back with.
// Neither counts what fits inside a wall-clock window -- how many
// process launches a hundred milliseconds buys is a property of the host
// running the suite, and on a loaded CI host it was one (grain/task-395).
func TestGranuleSandboxesHealthReprobesASickGuestCheaplyUntilItIsDueAFullOne(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30086)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      20 * time.Millisecond,
		ReadyPollInterval: 5 * time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	// A clock the test moves rather than one it waits out: the intervals
	// under test are seconds and minutes long.
	base := time.Now()
	var elapsed time.Duration
	k.SetHealthClockForTest(func() time.Time { return base.Add(elapsed) })

	dockerArgv := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuest(t, dockerArgv, filepath.Join(t.TempDir(), "counter2"), 1_000_000, "")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got := k.Health(ctx); len(got) != 1 || got[0].Error == "" {
		t.Fatalf("first sweep = %v, want one entry with an error", got)
	}

	elapsed += orchestrator.HealthBreakerCooldown
	before := countLines(t, dockerArgv)
	got := k.Health(context.Background())
	// One `granule exec` and the one `docker inspect` that follows it --
	// the liveness check that turns "not answering" into "the container
	// has exited", which is worth its single invocation even here.
	if calls := countLines(t, dockerArgv) - before; calls != 2 {
		t.Errorf("the sweep after the cooldown ran %d docker commands, want 2 -- one attempt, not ten", calls)
	}
	if len(got) != 1 || got[0].Error == "" {
		t.Fatalf("sweep after the cooldown = %v, want one entry still reporting the failure", got)
	}

	// And a guest that has been failing for long enough is asked again
	// properly, in case it was slow rather than gone -- asserted by what
	// the retried budget can find that a single attempt cannot, rather
	// than by how much docker work fits inside a deadline. This guest
	// fails one probe and answers the next, which is the shape of the
	// guest healthBreakerFullInterval exists for: the sweep above, with
	// its one attempt, would have written it off, and this one comes back
	// with a real reading instead. The probe ends when the guest answers,
	// so nothing here is waiting a clock out.
	elapsed += orchestrator.HealthBreakerFullInterval
	writeFakeDockerGuest(t, dockerArgv, filepath.Join(t.TempDir(), "counter3"), 1, "")
	before = countLines(t, dockerArgv)
	recovered := k.Health(context.Background())
	if len(recovered) != 1 || !recovered[0].Ready || recovered[0].Error != "" {
		t.Fatalf("sweep after the full-probe interval = %v, want one ready entry -- a guest that answers its second attempt was slow, not gone", recovered)
	}
	if calls := countLines(t, dockerArgv) - before; calls <= 2 {
		t.Errorf("the sweep after %v of failure ran %d docker commands, want a retried probe -- the failed attempt, the liveness check after it, and the try that answered",
			orchestrator.HealthBreakerFullInterval, calls)
	}
}

// A guest that comes back closes its own breaker: the cheap attempt the
// cooldown lets through is the recovery probe, and one answer out of it
// puts the sandbox back on the full reading every sweep. Nothing serves
// a *successful* reading from memory -- a load average from fifteen
// seconds ago presented as current is the thing this pane must never do.
func TestGranuleSandboxesHealthResumesFullProbesOnceAGuestAnswersAgain(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30087)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyTimeout:      20 * time.Millisecond,
		ReadyPollInterval: 5 * time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	var elapsed time.Duration
	k.SetHealthClockForTest(func() time.Time { return base.Add(elapsed) })

	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter2"), 1_000_000, "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got := k.Health(ctx); len(got) != 1 || got[0].Error == "" {
		t.Fatalf("first sweep = %v, want one entry with an error", got)
	}

	// The guest answers again, and the cooldown is up.
	dockerArgv := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuest(t, dockerArgv, filepath.Join(t.TempDir(), "counter3"), 0, home)
	elapsed += orchestrator.HealthBreakerCooldown

	got := k.Health(context.Background())
	if len(got) != 1 || !got[0].Ready {
		t.Fatalf("sweep after the guest came back = %v, want one ready entry", got)
	}
	if got[0].LoadAverage == "" {
		t.Errorf("LoadAverage = %q, want a real reading -- a recovered guest gets the full reading again", got[0].LoadAverage)
	}

	// Immediately, with no cooldown left to wait out: a successful
	// reading is never remembered, so this sweep probes for real.
	before := countLines(t, dockerArgv)
	if got := k.Health(context.Background()); len(got) != 1 || !got[0].Ready {
		t.Fatalf("next sweep = %v, want one ready entry", got)
	}
	if calls := countLines(t, dockerArgv) - before; calls != 1 {
		t.Errorf("the sweep after a healthy one ran %d guest commands, want 1 -- readings are never served from memory", calls)
	}
}

// countLines counts what a fake docker logged, one line per invocation.
func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}
