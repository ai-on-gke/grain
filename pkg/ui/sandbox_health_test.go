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

package ui_test

// bwsalmon/agents#536: the sandbox health pane's own API surface -- the
// same enabled-flag conventions logs_test.go's own tests already
// exercise for the System pane's other panel.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

type fakeSandboxHealth struct {
	snapshots []ui.SandboxSnapshot
}

func (f fakeSandboxHealth) Health(ctx context.Context) []ui.SandboxSnapshot {
	return f.snapshots
}

func (f fakeSandboxHealth) SandboxCount() int { return len(f.snapshots) }

func TestConfigReportsTheLiveSandboxCount(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{
		{Sandbox: "task-1"},
		{Sandbox: "ci-job-1"},
	}}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["sandboxCount"] != float64(2) {
		t.Fatalf("sandboxCount = %v, want 2", got["sandboxCount"])
	}
}

func TestGetSandboxHealthReportsUnavailableWithNeitherConfigured(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
}

func TestGetSandboxHealthReportsEverySandboxAndTheHost(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{
		{Backend: "granule", Name: "grain-0", CPUs: 4, MemoryMB: 4096, DiskGB: 20, NetworkAccess: "none", Ready: true, LoadAverage: "0.1 0.2 0.3", MemoryUsedMB: 100, MemoryTotalMB: 200, SwapUsedMB: 25, SwapTotalMB: 50},
		{Backend: "granule", Name: "grain-1", Error: "unreachable"},
	}}
	client.Config.HostStats = func() (ui.HostPressure, error) {
		return ui.HostPressure{
			LoadAverage1: 1.5, CPUs: 4, MemoryUsedMB: 512, MemoryTotalMB: 1024, SwapUsedMB: 128, SwapTotalMB: 512,
			// One figure per filesystem the daemon's state sits on, not
			// one "disk" figure -- the store's small volume and the much
			// larger one sandboxes were given (grain/task-148).
			Disks: []ui.DiskUsage{
				{Holds: []string{"store"}, Path: "/var/lib/grain/data", UsedMB: 4096, TotalMB: 20480},
				{Holds: []string{"sandboxes", "docker"}, Path: "/var/lib/grain/scratch", UsedMB: 61440, TotalMB: 102400},
			},
		}, nil
	}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Enabled   bool                 `json:"enabled"`
		Sandboxes []ui.SandboxSnapshot `json:"sandboxes"`
		Host      *ui.HostPressure     `json:"host"`
	}](t, rec)

	if !got.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if len(got.Sandboxes) != 2 {
		t.Fatalf("sandboxes = %v, want 2 entries", got.Sandboxes)
	}
	if got.Sandboxes[0].CPUs != 4 || got.Sandboxes[0].MemoryMB != 4096 || got.Sandboxes[0].DiskGB != 20 || got.Sandboxes[0].NetworkAccess != "none" || got.Sandboxes[0].SwapUsedMB != 25 {
		t.Errorf("sandboxes[0] shape = %+v, want 4 vCPU/4096 MB/20 GB/none/25 MB swap", got.Sandboxes[0])
	}
	if got.Sandboxes[1].Error != "unreachable" {
		t.Errorf("sandboxes[1].Error = %q, want %q", got.Sandboxes[1].Error, "unreachable")
	}
	if got.Host == nil || got.Host.MemoryTotalMB != 1024 || got.Host.SwapUsedMB != 128 {
		t.Fatalf("host = %v, want MemoryTotalMB 1024 and SwapUsedMB 128", got.Host)
	}
	// The denominator the load averages beside it are read against: a
	// load of 1.5 is a quiet 4-core host and a starved single-core one,
	// and the pane cannot tell those apart without this.
	if got.Host.CPUs != 4 {
		t.Errorf("host.CPUs = %d, want 4", got.Host.CPUs)
	}
	if len(got.Host.Disks) != 2 {
		t.Fatalf("host.Disks = %v, want one entry per filesystem", got.Host.Disks)
	}
	// The sandbox volume, which is the one a runaway build fills and the
	// one this pane showed no figure for at all before grain/task-148.
	sandboxVolume := got.Host.Disks[1]
	if sandboxVolume.TotalMB != 102400 || sandboxVolume.UsedMB != 61440 {
		t.Errorf("host.Disks[1] usage = %d/%d MB, want 61440/102400", sandboxVolume.UsedMB, sandboxVolume.TotalMB)
	}
	// Two names on one row: the daemon found the sandbox root and
	// docker's data root to be the same filesystem and said so once.
	if strings.Join(sandboxVolume.Holds, ",") != "sandboxes,docker" {
		t.Errorf("host.Disks[1].Holds = %v, want everything that shares that filesystem", sandboxVolume.Holds)
	}
}

// Which image those sandboxes are built from, beside the one this build
// of grain was stamped for -- so the pane can say when a deployment is
// running a daemon and guests from different commits, which nothing but
// a startup log line said before (grain/task-306).
func TestGetSandboxHealthReportsTheSandboxImageAndTheStampItShouldMatch(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	client.Config.SandboxImage = &ui.SandboxImage{
		Image:    "ghcr.io/bwsalmon/grain/guest:sha-2f89890",
		Expected: "ghcr.io/bwsalmon/grain/guest:sha-e5544d6",
		Pinned:   true,
	}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	got := decode[struct {
		SandboxImage *ui.SandboxImage `json:"sandboxImage"`
	}](t, rec)

	if got.SandboxImage == nil {
		t.Fatal("sandboxImage = nil, want the image sandboxes are built from")
	}
	if *got.SandboxImage != *client.Config.SandboxImage {
		t.Errorf("sandboxImage = %+v, want %+v", got.SandboxImage, client.Config.SandboxImage)
	}

	// A deployment with no sandbox image to name -- host sandboxing --
	// leaves it out entirely rather than reporting an empty one, which
	// the pane would have to draw as a blank line.
	client.Config.SandboxImage = nil
	body := do(t, srv, http.MethodGet, "/api/sandboxes", "").Body.String()
	if strings.Contains(body, "sandboxImage") {
		t.Errorf("body = %s, want no sandboxImage for a deployment that has none", body)
	}
}

func TestGetSandboxHealthReportsHostErrorWithoutHidingSandboxes(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{{Backend: "host", Name: "/tmp/sandbox-0", Ready: true}}}
	client.Config.HostStats = func() (ui.HostPressure, error) { return ui.HostPressure{}, errors.New("not on linux") }

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	got := decode[struct {
		Enabled   bool                 `json:"enabled"`
		Sandboxes []ui.SandboxSnapshot `json:"sandboxes"`
		Host      *ui.HostPressure     `json:"host"`
		HostError string               `json:"hostError"`
	}](t, rec)

	if !got.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if len(got.Sandboxes) != 1 {
		t.Fatalf("sandboxes = %v, want 1 entry", got.Sandboxes)
	}
	if got.Host != nil {
		t.Errorf("host = %v, want nil", got.Host)
	}
	if got.HostError != "not on linux" {
		t.Errorf("hostError = %q, want %q", got.HostError, "not on linux")
	}
}

// A deployment that is restarting rather than running says so in the one
// pane an operator opens when it is misbehaving (grain/task-233). It is
// reported even by a UI that has nothing else to report, because the
// daemon serving it may be the fifty-second attempt at serving it.
func TestGetSandboxHealthReportsTheDaemonsOwnRestarts(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	started := time.Now().Add(-4 * time.Second)
	client.Config.Daemon = func() ui.DaemonHealth {
		return ui.DaemonHealth{
			StartedAt:     started,
			Starts:        52,
			WindowMinutes: 30,
			UnknownFlags:  []string{"-root-shell-control-dir"},
		}
	}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Enabled bool             `json:"enabled"`
		Daemon  *ui.DaemonHealth `json:"daemon"`
	}](t, rec)
	if !got.Enabled {
		t.Fatal("enabled = false: a deployment with no sandbox pool and no host reading can still be one " +
			"that has restarted 52 times, and that is this pane's most important line")
	}
	if got.Daemon == nil {
		t.Fatal("no daemon section at all")
	}
	if got.Daemon.Starts != 52 || got.Daemon.WindowMinutes != 30 {
		t.Errorf("daemon = %d starts in %d minutes, want 52 in 30", got.Daemon.Starts, got.Daemon.WindowMinutes)
	}
	if len(got.Daemon.UnknownFlags) != 1 {
		t.Errorf("unknownFlags = %v, want the flag the daemon had to ignore to come up", got.Daemon.UnknownFlags)
	}
	if !got.Daemon.StartedAt.Equal(started.UTC()) && got.Daemon.StartedAt.Sub(started).Abs() > time.Second {
		t.Errorf("startedAt = %v, want %v", got.Daemon.StartedAt, started)
	}
}

// And nothing at all where no one can speak for the daemon -- `grain
// demo`'s throwaway UI, a UI not colocated with one.
func TestGetSandboxHealthOmitsTheDaemonSectionWhenUnconfigured(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if strings.Contains(rec.Body.String(), `"daemon"`) {
		t.Errorf("body names a daemon section with nothing configured to fill it: %s", rec.Body)
	}
}

// grain/task-223: one sweep of Config.Sandboxes.Health runs a command in
// every live guest, and that cost used to be paid per caller -- two
// operators with the pane open, or a tab plus the deployment-health MCP
// tool, each probed every sandbox on their own 5-second cadence, on the
// host whose load is the reason they are looking. The three tests below
// pin the three halves of not doing that: concurrent callers share one
// sweep, a caller arriving just after one is handed its readings, and a
// caller who goes away takes nobody else's sweep with them.

// countingSandboxHealth blocks inside Health until it is released, so a
// test can hold a sweep open and see what the callers arriving during it
// do.
type countingSandboxHealth struct {
	snapshots []ui.SandboxSnapshot
	// started carries one value per Health call that has begun, so a test
	// can wait until a sweep is genuinely in flight rather than sleeping.
	started chan struct{}
	// release gates every call's return; nil means "do not block".
	release   chan struct{}
	calls     atomic.Int64
	cancelled atomic.Bool
}

func (c *countingSandboxHealth) Health(ctx context.Context) []ui.SandboxSnapshot {
	c.calls.Add(1)
	if c.started != nil {
		c.started <- struct{}{}
	}
	if c.release != nil {
		<-c.release
	}
	if ctx.Err() != nil {
		c.cancelled.Store(true)
	}
	return c.snapshots
}

func (c *countingSandboxHealth) SandboxCount() int { return len(c.snapshots) }

func getSandboxes(ctx context.Context, t *testing.T, srv *ui.Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sandboxes", strings.NewReader("")).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func sandboxNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Sandboxes []ui.SandboxSnapshot `json:"sandboxes"`
	}](t, rec)
	names := make([]string, len(got.Sandboxes))
	for i, s := range got.Sandboxes {
		names[i] = s.Sandbox
	}
	return names
}

// blockingSandboxHealth answers the first sweep at once and holds every
// later one open until it is released: a deployment where one guest has
// stopped answering, which is the state the stale-while-revalidate path
// exists for. The pane has a reading, and the sweep that would replace
// it is stuck inside orchestrator's 20-second probe budget.
type blockingSandboxHealth struct {
	first []ui.SandboxSnapshot
	later []ui.SandboxSnapshot
	// started carries one value per held sweep that has begun, so a test
	// can wait until the stuck probe is genuinely in flight.
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (b *blockingSandboxHealth) Health(ctx context.Context) []ui.SandboxSnapshot {
	if b.calls.Add(1) == 1 {
		return b.first
	}
	if b.started != nil {
		b.started <- struct{}{}
	}
	<-b.release
	return b.later
}

func (b *blockingSandboxHealth) SandboxCount() int { return len(b.first) }

// stoppedClock freezes the client's clock and returns the handle that
// moves it, so a test can age a reading by eight seconds without
// spending eight seconds.
func stoppedClock(client *ui.Client) func(time.Duration) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

// sandboxPoll is what one GET /api/sandboxes answered with: the rows,
// and what the server says about how old they are.
type sandboxPoll struct {
	Sandboxes []ui.SandboxSnapshot `json:"sandboxes"`
	Reading   *ui.SandboxReading   `json:"sandboxReading"`
}

func (p sandboxPoll) names() []string {
	names := make([]string, len(p.Sandboxes))
	for i, s := range p.Sandboxes {
		names[i] = s.Sandbox
	}
	return names
}

func sandboxPollOf(t *testing.T, rec *httptest.ResponseRecorder) sandboxPoll {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	return decode[sandboxPoll](t, rec)
}

// The whole point of keeping the last sweep past its freshness window:
// one guest that has stopped answering spends orchestrator's entire
// 20-second probe budget failing to, and every caller now shares that one
// sweep -- so without this the pane sits blank for 20 seconds on every
// refresh, in exactly the state it exists to report (grain/task-262).
func TestGetSandboxHealthServesTheLastSweepWhileASickGuestHoldsTheNextOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &blockingSandboxHealth{
		first:   []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}},
		later:   []ui.SandboxSnapshot{{Sandbox: "task-1", Error: "unreachable"}, {Sandbox: "task-2", Ready: true}},
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client.Config.Sandboxes = health
	release := sync.OnceFunc(func() { close(health.release) })
	t.Cleanup(release)
	advance := stoppedClock(client)

	first := sandboxPollOf(t, getSandboxes(context.Background(), t, srv))
	if names := first.names(); len(names) != 1 || names[0] != "task-1" {
		t.Fatalf("the first poll was answered %v, want the sandbox its own sweep found", names)
	}
	if first.Reading == nil || first.Reading.Stale {
		t.Errorf("the first poll's reading is %+v, want its own sweep's, not a stale one", first.Reading)
	}

	// Eight seconds later, with the next sweep stuck in the guest that
	// has stopped answering. The caller is answered anyway -- if it
	// waited for that sweep this test would not return at all, since
	// nothing releases it until the assertions below have run.
	advance(8 * time.Second)
	stale := sandboxPollOf(t, getSandboxes(context.Background(), t, srv))
	<-health.started
	if names := stale.names(); len(names) != 1 || names[0] != "task-1" {
		t.Errorf("the poll during the stuck sweep was answered %v, want the last sweep's own rows", names)
	}
	if stale.Reading == nil || !stale.Reading.Stale || stale.Reading.AgeMS != 8000 {
		t.Errorf("the stale poll's reading is %+v, want the eight-second-old one it was served, said to be stale", stale.Reading)
	}

	// And the next caller joins that same stuck sweep rather than
	// putting a second one onto the same guests -- and is answered
	// without waiting on it again, because the wait it would spend has
	// already been spent on this sweep by the poll above.
	advance(2 * time.Second)
	started := time.Now()
	again := sandboxPollOf(t, getSandboxes(context.Background(), t, srv))
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("the second poll during the stuck sweep took %s, want an answer from the reading it already has", elapsed.Round(time.Millisecond))
	}
	if again.Reading == nil || !again.Reading.Stale || again.Reading.AgeMS != 10000 {
		t.Errorf("the second poll during the stuck sweep reads %+v, want the same reading, two seconds older", again.Reading)
	}
	if n := health.calls.Load(); n != 2 {
		t.Errorf("probed the guests %d times, want the first sweep and the one revalidating behind these polls", n)
	}

	// Once the sick guest answers, the sweep it was holding open becomes
	// the reading the pane is served -- not stale, and showing what the
	// probe found.
	release()
	deadline := time.Now().Add(10 * time.Second)
	for {
		poll := sandboxPollOf(t, getSandboxes(context.Background(), t, srv))
		if len(poll.names()) == 2 {
			if poll.Reading == nil || poll.Reading.Stale {
				t.Errorf("the poll after the sweep landed reads %+v, want a reading of its own", poll.Reading)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the finished sweep never became the served reading: still %v", poll.names())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := health.calls.Load(); n != 2 {
		t.Errorf("probed the guests %d times altogether, want 2", n)
	}
}

// A caller with nothing cached waits, as it always has: there is no
// honest answer to give it, and an empty sandbox list would read as a
// deployment with nothing running rather than as one still being probed.
func TestGetSandboxHealthMakesTheFirstCallerWaitWithNothingCached(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &countingSandboxHealth{
		snapshots: []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}},
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	client.Config.Sandboxes = health

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- getSandboxes(context.Background(), t, srv) }()
	<-health.started

	// Comfortably past the wait a caller with something cached would
	// give up after.
	select {
	case <-done:
		t.Fatal("answered while the first sweep was still running -- with nothing cached there is nothing to answer with")
	case <-time.After(1500 * time.Millisecond):
	}

	close(health.release)
	poll := sandboxPollOf(t, <-done)
	if names := poll.names(); len(names) != 1 || names[0] != "task-1" {
		t.Errorf("the first caller was answered %v, want the sweep it waited for", names)
	}
	if poll.Reading == nil || poll.Reading.Stale {
		t.Errorf("the first caller's reading is %+v, want its own sweep's", poll.Reading)
	}
}

// There is a floor under how old a reading may be and still be handed
// over as one. A minute of sweeps that never come back is not a delayed
// sample of this deployment, it is a claim about a different one, and an
// operator cannot tell the two apart from a load average.
func TestGetSandboxHealthWaitsRatherThanServeAReadingTooOldToBeOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &blockingSandboxHealth{
		first:   []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}},
		later:   []ui.SandboxSnapshot{{Sandbox: "task-2", Ready: true}},
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client.Config.Sandboxes = health
	advance := stoppedClock(client)

	if names := sandboxPollOf(t, getSandboxes(context.Background(), t, srv)).names(); len(names) != 1 {
		t.Fatalf("the first poll was answered %v, want the sandbox its own sweep found", names)
	}
	advance(2 * time.Minute)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- getSandboxes(context.Background(), t, srv) }()
	<-health.started
	select {
	case rec := <-done:
		t.Fatalf("served a two-minute-old reading as this deployment's: %v", sandboxPollOf(t, rec).names())
	case <-time.After(1500 * time.Millisecond):
	}

	close(health.release)
	poll := sandboxPollOf(t, <-done)
	if names := poll.names(); len(names) != 1 || names[0] != "task-2" {
		t.Errorf("the caller that waited was answered %v, want the sweep it waited for", names)
	}
	if poll.Reading == nil || poll.Reading.Stale {
		t.Errorf("the reading it waited for is %+v, want a fresh one", poll.Reading)
	}
}

func TestGetSandboxHealthGivesConcurrentCallersOneSweep(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &countingSandboxHealth{
		snapshots: []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}},
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	client.Config.Sandboxes = health

	// The first caller, held inside the sweep, so the others below are
	// unambiguously arriving while it runs -- which is the state two
	// operators and an MCP tool put this endpoint in.
	recs := make([]*httptest.ResponseRecorder, 4)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		recs[0] = getSandboxes(context.Background(), t, srv)
	}()
	<-health.started

	for i := 1; i < len(recs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = getSandboxes(context.Background(), t, srv)
		}()
	}
	close(health.release)
	wg.Wait()

	if n := health.calls.Load(); n != 1 {
		t.Errorf("probed the guests %d times for %d concurrent callers, want 1 shared sweep", n, len(recs))
	}
	for i, rec := range recs {
		if names := sandboxNames(t, rec); len(names) != 1 || names[0] != "task-1" {
			t.Errorf("caller %d was answered %v, want the one sandbox the sweep found", i, names)
		}
	}
}

func TestGetSandboxHealthServesARecentSweepWithoutProbingAgain(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &countingSandboxHealth{snapshots: []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}}}
	client.Config.Sandboxes = health

	var clockMu sync.Mutex
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(d)
	}

	for i := 0; i < 3; i++ {
		if names := sandboxNames(t, getSandboxes(context.Background(), t, srv)); len(names) != 1 {
			t.Fatalf("poll %d was answered %v, want the sandbox the first sweep found", i, names)
		}
	}
	if n := health.calls.Load(); n != 1 {
		t.Errorf("probed the guests %d times for three polls inside the cache window, want 1", n)
	}

	// Past the window, a caller gets a fresh reading: this pane exists to
	// show a sandbox that has just gone wrong, so the saving is bounded
	// at a couple of seconds and is not allowed to become a stale pane.
	advance(2 * time.Second)
	health.snapshots = []ui.SandboxSnapshot{{Sandbox: "task-1", Error: "unreachable"}, {Sandbox: "task-2", Ready: true}}
	if names := sandboxNames(t, getSandboxes(context.Background(), t, srv)); len(names) != 2 {
		t.Errorf("poll after the cache window was answered %v, want the second sweep's own two sandboxes", names)
	}
	if n := health.calls.Load(); n != 2 {
		t.Errorf("probed the guests %d times over both windows, want 2", n)
	}
}

func TestGetSandboxHealthSweepSurvivesTheCallerThatStartedIt(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	health := &countingSandboxHealth{
		snapshots: []ui.SandboxSnapshot{{Sandbox: "task-1", Ready: true}},
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	client.Config.Sandboxes = health

	// A closed browser tab: the request that started the sweep goes away
	// while two other viewers are waiting on it.
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		getSandboxes(ctx, t, srv)
	}()
	<-health.started

	joined := make(chan *httptest.ResponseRecorder, 1)
	joining := make(chan struct{})
	go func() {
		close(joining)
		joined <- getSandboxes(context.Background(), t, srv)
	}()
	<-joining
	cancel()
	<-gone

	close(health.release)
	if names := sandboxNames(t, <-joined); len(names) != 1 || names[0] != "task-1" {
		t.Errorf("the caller still waiting was answered %v, want the sweep's own readings", names)
	}
	if health.cancelled.Load() {
		t.Error("the sweep saw its context cancelled -- one caller leaving must not cancel the probe the others are sharing")
	}
	if n := health.calls.Load(); n != 1 {
		t.Errorf("probed the guests %d times, want the one sweep both callers shared", n)
	}
}
