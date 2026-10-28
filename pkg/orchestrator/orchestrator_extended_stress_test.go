package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// stressServingSandbox simulates a serving standing sandbox instance.
type stressServingSandbox struct {
	name      string
	spec      ServeSpec
	released  atomic.Bool
	releaseCh chan struct{}
	onRelease func()
}

func newStressServingSandbox(name string, spec ServeSpec, onRelease func()) *stressServingSandbox {
	return &stressServingSandbox{
		name:      name,
		spec:      spec,
		releaseCh: make(chan struct{}),
		onRelease: onRelease,
	}
}

func (s *stressServingSandbox) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return nil, errors.New("stressServingSandbox: DialStream not implemented")
}

func (s *stressServingSandbox) Output(ctx context.Context) string {
	return "fake output from " + s.name
}

func (s *stressServingSandbox) Release(ctx context.Context) error {
	if s.released.CompareAndSwap(false, true) {
		close(s.releaseCh)
		if s.onRelease != nil {
			s.onRelease()
		}
	}
	return nil
}

// TestStandingSandboxSupervisorExtendedStress exercises the StandingSandboxSupervisor
// under high-concurrency conditions: rapid concurrent Reconcile calls with changing
// definitions, dynamic config updates, simulated failures via MarkFailed, concurrent
// status reads via GetActive, and verifies zero leaks and thread-safety under the race detector.
func TestStandingSandboxSupervisorExtendedStress(t *testing.T) {
	t.Parallel()

	duration := 4 * time.Second
	if val := os.Getenv("GRAIN_STRESS_DURATION"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			duration = d
		}
	}

	var (
		launchCount  atomic.Int64
		releaseCount atomic.Int64
		activePoolMu sync.Mutex
		activePool   = make(map[string]*stressServingSandbox)
	)

	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		launchCount.Add(1)
		inst := newStressServingSandbox(spec.Name, spec, func() {
			releaseCount.Add(1)
		})
		activePoolMu.Lock()
		activePool[spec.Name] = inst
		activePoolMu.Unlock()
		return inst, nil
	})

	const numSandboxes = 20
	baseNames := make([]string, numSandboxes)
	for i := 0; i < numSandboxes; i++ {
		baseNames[i] = fmt.Sprintf("sb-stress-%02d", i)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup

	// Goroutine 1: Rapid Reconciler with dynamic configuration changes
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewPCG(111, 222))
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()

		epoch := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				epoch++
				var desired []model.StandingSandbox
				for i, name := range baseNames {
					enabled := (i+epoch)%5 != 0
					imageVer := (epoch / 10) % 3
					desired = append(desired, model.StandingSandbox{
						Name:          name,
						Image:         fmt.Sprintf("ghcr.io/acme/agent:v%d", imageVer),
						Command:       "/bin/serve",
						CPUs:          2 + (i % 2),
						MemoryMB:      4096,
						Enabled:       enabled,
						Env:           map[string]string{"EPOCH": fmt.Sprintf("%d", epoch), "ID": name},
						NetworkAccess: model.NetworkOpen,
					})
				}

				if err := sup.Reconcile(ctx, Deps{}, desired, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("Reconcile error: %v", err)
				}
				time.Sleep(time.Duration(rng.IntN(2)) * time.Millisecond)
			}
		}
	}()

	// Goroutine 2: Concurrent Failure Injection
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewPCG(333, 444))
		ticker := time.NewTicker(3 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				target := baseNames[rng.IntN(len(baseNames))]
				sup.MarkFailed(target, "simulated transient crash")
			}
		}
	}()

	// Goroutine 3: Concurrent Active Status Queries
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewPCG(555, 666))
		ticker := time.NewTicker(1 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				target := baseNames[rng.IntN(len(baseNames))]
				active, ok := sup.GetActive(target)
				if ok && active != nil {
					_ = active.Failed
					_ = active.Restarts
					_ = active.Def.Name
				}
			}
		}
	}()

	wg.Wait()

	// Final teardown reconcile: empty desired list terminates all
	if err := sup.Reconcile(context.Background(), Deps{}, nil, time.Now().UTC()); err != nil {
		t.Fatalf("Teardown reconcile failed: %v", err)
	}

	for _, name := range baseNames {
		if active, ok := sup.GetActive(name); ok && active != nil {
			t.Errorf("Sandbox %s still active after teardown reconcile", name)
		}
	}

	t.Logf("Standing sandbox supervisor stress test completed:\n"+
		"  - Total Launches: %d\n"+
		"  - Total Releases: %d",
		launchCount.Load(), releaseCount.Load())
}

type fakeStressSandbox struct {
	name      string
	rebuildFn func() error
}

func (s *fakeStressSandbox) Name() string                              { return s.name }
func (s *fakeStressSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *fakeStressSandbox) Release(context.Context) error             { return nil }
func (s *fakeStressSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *fakeStressSandbox) Rebuild(ctx context.Context) error {
	if s.rebuildFn != nil {
		return s.rebuildFn()
	}
	return nil
}

// TestSandboxRecreationsAndCancellersExtendedStress validates the concurrent lifecycle of
// sandbox recreations and task cancellations under extreme concurrency and race detector scrutiny.
func TestSandboxRecreationsAndCancellersExtendedStress(t *testing.T) {
	t.Parallel()
	store, _ := openStressStore(t)

	duration := 4 * time.Second
	if val := os.Getenv("GRAIN_STRESS_DURATION"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			duration = d
		}
	}

	recreations := NewSandboxRecreations()
	cancellers := NewTaskCancellers()

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	const numTasks = 40
	var (
		recreatesCount     atomic.Int64
		cancellationsCount atomic.Int64
		wg                 sync.WaitGroup
	)

	for i := 0; i < numTasks; i++ {
		taskID := fmt.Sprintf("task-%04d", i)
		wg.Add(1)
		go func(tID string, seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed*7))
			attempt := 0

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				attempt++
				runID := fmt.Sprintf("%s-%d", tID, attempt)
				runCtx, cancelRun := context.WithCancelCause(ctx)

				sb := &fakeStressSandbox{
					name: runID,
					rebuildFn: func() error {
						recreatesCount.Add(1)
						return nil
					},
				}

				// Register canceller and recreation
				unregisterCancel := cancellers.register(tID, cancelRun)
				rec := &sandboxRecreation{
					store:   store,
					task:    model.Task{ID: tID},
					sandbox: sb,
				}
				unregisterRec := recreations.register(tID, rec)

				// Concurrently trigger recreations or cancellations
				if rng.Float64() < 0.3 {
					go func(targetTask string) {
						time.Sleep(1 * time.Millisecond)
						_, _ = recreations.Recreate(ctx, targetTask)
					}(tID)
				}
				if rng.Float64() < 0.3 {
					go func(targetTask string) {
						time.Sleep(1 * time.Millisecond)
						if cancellers.Cancel(targetTask, ErrTurnStopped) {
							cancellationsCount.Add(1)
						}
					}(tID)
				}

				time.Sleep(time.Duration(1+rng.IntN(3)) * time.Millisecond)

				// Unregister
				unregisterRec()
				unregisterCancel()
				cancelRun(nil)
				_ = runCtx
			}
		}(taskID, uint64(i+1)*1000)
	}

	wg.Wait()

	t.Logf("Sandbox recreations & cancellers stress completed:\n"+
		"  - Recreate calls: %d\n"+
		"  - Cancel calls: %d",
		recreatesCount.Load(), cancellationsCount.Load())
}

// TestWaitCreditsExtendedStress validates the concurrent wait-credit accounting mechanism
// under heavy concurrency, verifying deadline calculations, cap clamping, and race safety.
func TestWaitCreditsExtendedStress(t *testing.T) {
	t.Parallel()

	duration := 4 * time.Second
	if val := os.Getenv("GRAIN_STRESS_DURATION"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			duration = d
		}
	}

	credits := NewRunWaitCredits()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	const numTasks = 50
	var (
		creditOpsCount atomic.Int64
		wg             sync.WaitGroup
	)

	for i := 0; i < numTasks; i++ {
		taskID := fmt.Sprintf("task-wc-%04d", i)
		wg.Add(1)
		go func(tID string, seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed*13))

			runCtx, cancelRun := context.WithCancelCause(ctx)
			defer cancelRun(nil)

			agentCtx, clock := newRunClock(runCtx, 10*time.Minute, RunWaitCreditCap)
			_ = agentCtx
			stopCrediting := credits.register(tID, clock)
			defer stopCrediting()

			var lastCredited time.Duration

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Random blocked duration report
				blocked := time.Duration(rng.IntN(60)) * time.Minute
				credit, err := credits.Credit(ctx, tID, blocked)
				if err != nil {
					t.Errorf("Credit error for %s: %v", tID, err)
					return
				}
				creditOpsCount.Add(1)

				// Invariant: Credited must be monotonically non-decreasing
				if credit.Credited < lastCredited {
					t.Errorf("Monotonicity violation for %s: got %v < prev %v", tID, credit.Credited, lastCredited)
				}
				lastCredited = credit.Credited

				// Invariant: Credited must never exceed RunWaitCreditCap
				if credit.Credited > RunWaitCreditCap {
					t.Errorf("Cap violation for %s: %v > %v", tID, credit.Credited, RunWaitCreditCap)
				}

				time.Sleep(time.Duration(1+rng.IntN(2)) * time.Millisecond)
			}
		}(taskID, uint64(i+1)*2000)
	}

	wg.Wait()

	t.Logf("Wait credits extended stress completed:\n"+
		"  - Total Credit operations: %d", creditOpsCount.Load())
}

func openStressStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	logToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}
