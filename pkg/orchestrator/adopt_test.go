package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/runhost"
)

// A daemon stopping under a run in a run host detaches from it
// (runhost.Host.Detach set to StoppedByDaemon): the framework sees the
// stop as the daemon's, and RunDispatch leaves the row live, and the live
// transcript where it is, for the daemon that adopts the run.
func TestRunDispatchLeavesARunItDetachedFromLive(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	transcripts := t.TempDir()

	daemonCtx, stopDaemon := context.WithCancel(ctx)
	started := make(chan struct{})
	var stoppedByDaemon bool
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if err := os.WriteFile(cfg.TranscriptPath, []byte("first half\n"), 0o600); err != nil {
			return nil, err
		}
		close(started)
		<-runCtx.Done()
		stoppedByDaemon = orchestrator.StoppedByDaemon(runCtx)
		return nil, fmt.Errorf("claude: %w", runhost.ErrDetached)
	})
	go func() { <-started; stopDaemon() }()
	cfg := orchestrator.Config{TranscriptDir: transcripts, Now: func() time.Time { return baseTime }}
	_, runErr := orchestrator.RunDispatch(daemonCtx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
	if !errors.Is(runErr, agent.ErrDetached) {
		t.Fatalf("RunDispatch err = %v, want agent.ErrDetached", runErr)
	}
	if !stoppedByDaemon {
		t.Fatal("the run's context did not report the daemon stopping, so a run host would be destroyed rather than detached")
	}
	if n, err := store.LiveRunCount(ctx); err != nil || n != 1 {
		t.Fatalf("live runs = %d (%v), want the detached run still live", n, err)
	}
	if _, err := os.Stat(filepath.Join(transcripts, "r1")); err != nil {
		t.Fatalf("the detached run's live transcript was removed: %v", err)
	}
}

// A task closed under a run is not the daemon stopping: the run host is
// destroyed, not detached.
func TestStoppedByDaemonIsOnlyTheDaemonsStop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("the task was closed"))
	if orchestrator.StoppedByDaemon(ctx) {
		t.Fatal("a cancellation that is not the daemon's own was read as one")
	}
	if orchestrator.StoppedByDaemon(context.Background()) {
		t.Fatal("a live context was read as a stopped daemon")
	}
}

// parsingFramework is a Framework whose runs can be adopted: ParseResult
// records the spool's stdout and answers with result, and ParseAttempts
// records what the run's earlier attempts wrote as well.
type parsingFramework struct {
	result  *agent.Result
	parsed  chan string
	earlier chan string
}

func (f parsingFramework) ParseAttempts(earlier, stdout string, runErr error) (*agent.Result, error) {
	f.earlier <- earlier
	return f.ParseResult(stdout, runErr)
}

func (f parsingFramework) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	return nil, errors.New("not dispatched in this test")
}

func (f parsingFramework) ParseResult(stdout string, runErr error) (*agent.Result, error) {
	f.parsed <- stdout
	return f.result, runErr
}

// The acceptance test's daemon half, against a run host whose CLI
// finished while no daemon was running: the adopting daemon claims the
// run's sandbox back before the startup reap (which then spares it and
// only it), tails the spool from the persisted cursor onto the live
// transcript the previous daemon left, parses the whole stdout through
// the framework, finishes the row by the ordinary rules -- not as
// interrupted -- and then frees the run host and the sandbox.
func TestResumeAdoptedRunCarriesTheRunToItsEnd(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}
	startRun(t, ctx, store, d, time.Now().UTC().Add(-time.Minute))
	if err := store.SetSpoolCursor(ctx, d.RunID, 6, time.Now().UTC().Add(-90*time.Second)); err != nil {
		t.Fatal(err)
	}

	// The previous daemon's sandbox, and a stray one nothing claims.
	sandboxDir := t.TempDir()
	if _, err := orchestrator.NewHostSandboxes(sandboxDir).Acquire(ctx, d.RunID, orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sandboxDir, "stray"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Its run host, finished: the spool is final.
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "stdout"), []byte("line1\nline2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, _ := json.Marshal(runhost.Exit{Code: 0, FinishedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(spool, "exit.json"), exit, 0o600); err != nil {
		t.Fatal(err)
	}
	driver := &listedHosts{hosts: []runhost.Info{{Name: "rh", Running: false,
		Labels: runhost.Labels{Deployment: "d", Task: task.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: spool}}}}
	// The live transcript: an attempt the run retried, in a run host of
	// its own long gone, then the head of this one up to the cursor.
	transcripts := t.TempDir()
	if err := os.WriteFile(filepath.Join(transcripts, d.RunID), []byte("attempt1\nline1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A lease the previous daemon minted for the run while it set up.
	capability := &fakeCapability{name: "keyed"}
	lease := model.Lease{Capability: "keyed", Resource: "key-1", MintedBy: model.CredentialRef{Name: "test"}, IssuedAt: time.Now().UTC()}
	if err := store.RecordLeases(ctx, d.RunID, []model.Lease{lease}); err != nil {
		t.Fatal(err)
	}

	// Startup: adopt, claim the sandbox, then reap.
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	var adopted []orchestrator.AdoptedRun
	if _, err := orchestrator.RecoverRunHosts(ctx, store, client, &runhost.Host{Driver: driver, Deployment: "d"}, time.Now().UTC(),
		func(a runhost.Adoption, cursor int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.Run)
			if err != nil {
				t.Errorf("readopting the sandbox: %v", err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: cursor, Sandbox: sb})
			return true
		}); err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 || adopted[0].Cursor != 6 || adopted[0].Gap < 90*time.Second {
		t.Fatalf("adopted %+v, want run t-1 at cursor 6 with the gap since its cursor was last written", adopted)
	}
	if err := orchestrator.RecoverOrphanedRunsExcept(ctx, store, client, time.Now().UTC(),
		map[string]bool{d.RunID: true}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.LiveRunCount(ctx); err != nil || n != 1 {
		t.Fatalf("live runs = %d (%v), want the adopted run left live by orphan recovery", n, err)
	}
	reaped, err := sandboxes.ReapOrphans(ctx)
	if err != nil || reaped != 1 {
		t.Fatalf("reaped %d (%v), want only the stray sandbox", reaped, err)
	}
	if _, err := os.Stat(filepath.Join(sandboxDir, d.RunID)); err != nil {
		t.Fatalf("the adopted run's sandbox was reaped: %v", err)
	}

	fw := parsingFramework{parsed: make(chan string, 1), earlier: make(chan string, 1), result: toolResult(agent.ToolCall{
		Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}})}
	deps := orchestrator.Deps{Store: store, Client: client, Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(fw),
		Config:    orchestrator.Config{TranscriptDir: transcripts, Capabilities: model.NewCapabilityRegistry(capability)}}
	host := &runhost.Host{Driver: driver, Deployment: "d", Poll: 5 * time.Millisecond}
	var tail []byte
	host.OnCursor = func(context.Context, string, int64) {
		tail, _ = os.ReadFile(filepath.Join(transcripts, d.RunID))
	}
	if err := orchestrator.ResumeAdoptedRun(ctx, deps, host, adopted[0]); err != nil {
		t.Fatalf("ResumeAdoptedRun: %v", err)
	}
	if got := <-fw.parsed; got != "line1\nline2\n" {
		t.Fatalf("the framework parsed %q, want the whole spool", got)
	}
	if got := <-fw.earlier; got != "attempt1\n" {
		t.Fatalf("the framework was given earlier attempts %q, want the retried attempt's stream", got)
	}
	if string(tail) != "attempt1\nline1\nline2\n" {
		t.Fatalf("live transcript = %q, want the previous daemon's half and the rest, with no hole and no duplicate", tail)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v (%v), want the one run finished", runs, err)
	}
	if runs[0].Outcome == model.InterruptedOutcome || runs[0].Outcome == "failed" {
		t.Fatalf("outcome = %q (%s), want the run's own ending", runs[0].Outcome, runs[0].Detail)
	}
	if fmt.Sprint(driver.destroyed) != "[rh]" {
		t.Fatalf("destroyed %v, want the adopted run host freed once the run was done", driver.destroyed)
	}
	assertLeasesRevoked(t, ctx, store, capability, d.RunID, "key-1")
	if _, err := os.Stat(filepath.Join(sandboxDir, d.RunID)); !os.IsNotExist(err) {
		t.Fatalf("the adopted run's sandbox outlived it: %v", err)
	}
}

// A run whose framework cannot read a spool is finished as interrupted,
// with the reason, and its run host and sandbox freed.
func TestResumeAdoptedRunWithoutAParserFinishesTheRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}
	startRun(t, ctx, store, d, time.Now().UTC())
	sandboxDir := t.TempDir()
	if _, err := orchestrator.NewHostSandboxes(sandboxDir).Acquire(ctx, d.RunID, orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, d.RunID)
	if err != nil {
		t.Fatal(err)
	}
	capability := &fakeCapability{name: "keyed"}
	if err := store.RecordLeases(ctx, d.RunID, []model.Lease{{Capability: "keyed", Resource: "key-1",
		MintedBy: model.CredentialRef{Name: "test"}, IssuedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	driver := &listedHosts{}
	info := runhost.Info{Name: "rh", Running: true,
		Labels: runhost.Labels{Deployment: "d", Run: d.RunID, Protocol: runhost.Protocol, Spool: t.TempDir()}}
	deps := orchestrator.Deps{Store: store, Client: client, Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(stubFramework{}),
		Config:    orchestrator.Config{Capabilities: model.NewCapabilityRegistry(capability)}}
	a := orchestrator.AdoptedRun{Adoption: runhost.Adoption{Claim: runhost.Claim{Run: d.RunID, Task: task.ID}, Host: info}, Sandbox: sb}
	if err := orchestrator.ResumeAdoptedRun(ctx, deps, &runhost.Host{Driver: driver, Deployment: "d"}, a); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].Outcome != model.InterruptedOutcome {
		t.Fatalf("runs = %+v (%v), want the run finished as interrupted", runs, err)
	}
	if fmt.Sprint(driver.destroyed) != "[rh]" {
		t.Fatalf("destroyed %v", driver.destroyed)
	}
	assertLeasesRevoked(t, ctx, store, capability, d.RunID, "key-1")
	if _, err := os.Stat(filepath.Join(sandboxDir, d.RunID)); !os.IsNotExist(err) {
		t.Fatalf("the sandbox outlived the run: %v", err)
	}
}

// assertLeasesRevoked fails t unless the adopted run's recorded leases
// were revoked through their provider and dropped from the store, as
// RunDispatch's own revokeAll does for a run it started.
func assertLeasesRevoked(t *testing.T, ctx context.Context, store *model.Store, c *fakeCapability, runID string, resources ...string) {
	t.Helper()
	c.mu.Lock()
	var revoked []string
	for _, l := range c.revoked {
		revoked = append(revoked, l.Resource)
	}
	c.mu.Unlock()
	if fmt.Sprint(revoked) != fmt.Sprint(resources) {
		t.Fatalf("revoked %v, want the adopted run's leases %v", revoked, resources)
	}
	if left, err := store.RunLeases(ctx, runID); err != nil || len(left) != 0 {
		t.Fatalf("leases left recorded = %+v (%v), want none", left, err)
	}
}

// A run that detaches from its run host leaves what it minted recorded
// against its row, unrevoked, for the daemon that adopts it.
func TestRunDispatchRecordsTheLeasesOfARunItDetachesFrom(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "keyed"})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	capability := &fakeCapability{name: "keyed"}
	daemonCtx, stopDaemon := context.WithCancel(ctx)
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		stopDaemon()
		<-runCtx.Done()
		return nil, fmt.Errorf("claude: %w", runhost.ErrDetached)
	})
	cfg := orchestrator.Config{Capabilities: model.NewCapabilityRegistry(capability), Now: func() time.Time { return baseTime }}
	if _, err := orchestrator.RunDispatch(daemonCtx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("RunDispatch err = %v, want agent.ErrDetached", err)
	}
	if len(capability.revoked) != 0 {
		t.Fatalf("revoked %+v, want the detached run's leases left held", capability.revoked)
	}
	leases, err := store.RunLeases(ctx, d.RunID)
	if err != nil || len(leases) != 1 || leases[0].Capability != "keyed" || leases[0].Resource != "res-1" {
		t.Fatalf("recorded leases = %+v (%v), want the one the run minted", leases, err)
	}
}
