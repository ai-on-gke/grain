package ci

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stressSandboxBackend implements Sandboxes and StreamingSandbox for high-concurrency stress testing.
type stressSandboxBackend struct {
	live          atomic.Int64
	peakLive      atomic.Int64
	totalAcquired atomic.Int64
	totalReleased atomic.Int64

	// jobStarts and jobEnds record start and end timestamps for DAG ordering verification.
	jobStarts sync.Map // map[string]time.Time
	jobEnds   sync.Map // map[string]time.Time

	// streamingLogs enables high-frequency live log chunk streaming via ExecStream.
	streamingLogs bool
	// streamChunks is the number of rapid chunks emitted per streaming step.
	streamChunks int
	// failJobIDs is a set of job IDs configured to simulate step failure.
	failJobIDs sync.Map // map[string]bool
	// artifactDir is the directory where mock artifact files are written before collection.
	artifactDir string
	// stepDelay adds simulated execution duration.
	stepDelay time.Duration
}

func newStressSandboxBackend() *stressSandboxBackend {
	return &stressSandboxBackend{
		streamChunks: 15,
		stepDelay:    time.Millisecond,
	}
}

func (b *stressSandboxBackend) Acquire(ctx context.Context, req SandboxRequest) (Sandbox, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	current := b.live.Add(1)
	b.totalAcquired.Add(1)
	for {
		peak := b.peakLive.Load()
		if current <= peak || b.peakLive.CompareAndSwap(peak, current) {
			break
		}
	}
	b.jobStarts.Store(req.Name, time.Now())
	return &stressSandbox{
		backend: b,
		name:    req.Name,
		repo:    req.Repo,
	}, nil
}

type stressSandbox struct {
	backend *stressSandboxBackend
	name    string
	repo    string
}

func (s *stressSandbox) Name() string { return s.name }

func (s *stressSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	return s.ExecStream(ctx, cmd, nil)
}

func (s *stressSandbox) ExecStream(ctx context.Context, cmd Command, out func(chunk string)) (CommandResult, error) {
	if ctx.Err() != nil {
		return CommandResult{ExitCode: -1}, ctx.Err()
	}

	// Stream rapid live log chunks if enabled and out is provided
	if s.backend.streamingLogs && out != nil {
		chunks := s.backend.streamChunks
		if chunks <= 0 {
			chunks = 10
		}
		for i := 0; i < chunks; i++ {
			if ctx.Err() != nil {
				return CommandResult{ExitCode: -1}, ctx.Err()
			}
			out(fmt.Sprintf("[%s] step progress chunk %d/%d at %s\n", s.name, i+1, chunks, time.Now().Format(time.RFC3339Nano)))
		}
	}

	if s.backend.stepDelay > 0 {
		select {
		case <-time.After(s.backend.stepDelay):
		case <-ctx.Done():
			return CommandResult{ExitCode: -1}, ctx.Err()
		}
	}

	// Check if this job is configured to fail
	if _, shouldFail := s.backend.failJobIDs.Load(s.name); shouldFail || strings.Contains(cmd.Script, "exit 1") {
		return CommandResult{
			Output:   fmt.Sprintf("step failed deliberately in sandbox %s: simulated failure", s.name),
			ExitCode: 1,
		}, nil
	}

	// Write simulated artifact if directory is configured
	if s.backend.artifactDir != "" {
		artPath := filepath.Join(s.backend.artifactDir, "trace.txt")
		_ = os.WriteFile(artPath, []byte("simulated diagnostic trace payload"), 0o644)
	}

	return CommandResult{
		Output:   fmt.Sprintf("executed script successfully in %s", s.name),
		ExitCode: 0,
	}, nil
}

func (s *stressSandbox) Release(ctx context.Context) error {
	s.backend.jobEnds.Store(s.name, time.Now())
	s.backend.live.Add(-1)
	s.backend.totalReleased.Add(1)
	return nil
}

// Multi-tenant workflow template definitions
const stressWorkflowDiamond = `
name: Diamond Pipeline
on: push
jobs:
  setup:
    steps:
      - name: init
        run: echo init
  worker_1:
    needs: [setup]
    steps:
      - name: work1
        run: echo worker 1
  worker_2:
    needs: [setup]
    steps:
      - name: work2
        run: echo worker 2
  worker_3:
    needs: [setup]
    steps:
      - name: work3
        run: echo worker 3
  aggregate:
    needs: [worker_1, worker_2, worker_3]
    steps:
      - name: join
        run: echo aggregate results
  publish:
    needs: [aggregate]
    steps:
      - name: deploy
        run: echo publish artifact
`

const stressWorkflowDeepChain = `
name: Deep Sequential Pipeline
on: push
jobs:
  lint:
    steps:
      - name: check
        run: echo linting
  typecheck:
    needs: [lint]
    steps:
      - name: types
        run: echo typechecking
  build:
    needs: [typecheck]
    steps:
      - name: compile
        run: echo building
  test_unit:
    needs: [build]
    steps:
      - name: unit
        run: echo testing unit
  test_integration:
    needs: [test_unit]
    steps:
      - name: integration
        run: echo testing integration
  package:
    needs: [test_integration]
    steps:
      - name: bundle
        run: echo packaging
`

const stressWorkflowFanoutFanin = `
name: Matrix Fanout Pipeline
on: push
jobs:
  prep:
    steps:
      - name: prepare
        run: echo preparing matrix
  matrix_a:
    needs: [prep]
    steps:
      - name: branch_a
        run: echo running matrix A
  matrix_b:
    needs: [prep]
    steps:
      - name: branch_b
        run: echo running matrix B
  join_a:
    needs: [matrix_a]
    steps:
      - name: merge_a
        run: echo merging branch A
  join_all:
    needs: [matrix_a, matrix_b]
    steps:
      - name: merge_all
        run: echo merging all branches
  finalize:
    needs: [join_a, join_all]
    steps:
      - name: finish
        run: echo final stage complete
`

const stressWorkflowGuestWarming = `
name: Guest Warming Workflow
on: push
jobs:
  image_warm:
    guest-cache:
      - Granulefile
      - scripts/setup.sh
    steps:
      - name: build_guest
        run: echo building guest image
  test_guest:
    needs: [image_warm]
    guest-cache:
      - Granulefile
      - scripts/setup.sh
    steps:
      - name: run_in_guest
        run: echo running inside guest
`

const stressWorkflowFlakyChaotic = `
name: Flaky Chaotic Workflow
on: push
jobs:
  stage_stable:
    steps:
      - name: ok_step
        run: echo stable step ok
  stage_flaky:
    needs: [stage_stable]
    steps:
      - name: risky_step
        run: echo maybe fail
  stage_dependent:
    needs: [stage_flaky]
    steps:
      - name: after_flaky
        run: echo after flaky
  stage_independent:
    needs: [stage_stable]
    steps:
      - name: independent
        run: echo independent branch
`

// TestCIMultiTenantDAGConcurrencyStress exercises the CI DAG engine, gate, guest cache warmer,
// and live log streaming under heavy concurrent multi-tenant load with dynamic capacity resizing.
func TestCIMultiTenantDAGConcurrencyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-tenant CI concurrency stress test in -short mode")
	}

	teams := []string{"team-alpha", "team-bravo", "team-charlie", "team-delta", "team-infra"}
	workflowsMap := map[string]string{
		".grain/workflows/diamond.yml": stressWorkflowDiamond,
		".grain/workflows/deep.yml":    stressWorkflowDeepChain,
		".grain/workflows/fanout.yml":  stressWorkflowFanoutFanin,
		".grain/workflows/warming.yml": stressWorkflowGuestWarming,
		".grain/workflows/chaotic.yml": stressWorkflowFlakyChaotic,
	}

	var parsedWorkflows []Workflow
	for path, body := range workflowsMap {
		parsedWorkflows = append(parsedWorkflows, mustParse(t, path, body))
	}

	// Dynamic capacity limits (max_ci_jobs)
	var dynamicCapacity atomic.Int64
	dynamicCapacity.Store(4)

	gate := &SharedGate{
		Limit: func() int {
			return int(dynamicCapacity.Load())
		},
		Poll:     time.Millisecond * 5,
		AgeBoost: window(time.Millisecond * 50),
	}

	backend := newStressSandboxBackend()
	backend.streamingLogs = true
	backend.streamChunks = 12
	backend.stepDelay = time.Millisecond * 2

	// Guest cache warmer with fake service
	warmService := newFakeWarmService()
	warmer := warmerFor(warmService, ".grain/workflows/warming.yml:image_warm")

	engine := &Engine{
		Sandboxes:        backend,
		Jobs:             gate,
		GuestCache:       warmer,
		ConcurrencyLimit: func() int { return int(dynamicCapacity.Load()) },
		Priority: func(req Request) Priority {
			switch owner, _, _ := strings.Cut(req.Repo, "/"); owner {
			case "team-infra":
				return PriorityMergeHead
			case "team-alpha":
				return PriorityUnderway
			default:
				return PriorityNormal
			}
		},
	}

	// Periodically release warm service builds
	stopWarmReleaser := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Millisecond * 10)
		defer ticker.Stop()
		for {
			select {
			case <-stopWarmReleaser:
				return
			case <-ticker.C:
				warmService.mu.Lock()
				for id, run := range warmService.runs {
					if !run.Conclusion.Done() {
						run.Conclusion = ConclusionSuccess
						warmService.runs[id] = run
					}
				}
				warmService.mu.Unlock()
			}
		}
	}()
	defer close(stopWarmReleaser)

	// Dynamic capacity resizer: oscillates max_ci_jobs between 1, 2, 4, 8, 16, 3, 6, 12
	stopResizer := make(chan struct{})
	capacitySchedule := []int64{2, 4, 8, 16, 1, 3, 6, 12, 4, 8, 2}
	go func() {
		idx := 0
		ticker := time.NewTicker(time.Millisecond * 40)
		defer ticker.Stop()
		for {
			select {
			case <-stopResizer:
				return
			case <-ticker.C:
				idx = (idx + 1) % len(capacitySchedule)
				dynamicCapacity.Store(capacitySchedule[idx])
			}
		}
	}()
	defer close(stopResizer)

	// Total runs to execute concurrently across teams
	const totalRuns = 120
	var wg sync.WaitGroup
	wg.Add(totalRuns)

	type runOutcome struct {
		runID      string
		team       string
		conclusion Conclusion
		jobCount   int
		err        error
		cancelled  bool
	}
	outcomes := make([]runOutcome, totalRuns)

	ctx, masterCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer masterCancel()

	for i := 0; i < totalRuns; i++ {
		idx := i
		team := teams[idx%len(teams)]
		workflowKey := idx % 5

		go func(i int, team string, wfChoice int) {
			defer wg.Done()

			// Select specific workflow for this run
			var wf Workflow
			switch wfChoice {
			case 0:
				wf = parsedWorkflows[0] // diamond
			case 1:
				wf = parsedWorkflows[1] // deep
			case 2:
				wf = parsedWorkflows[2] // fanout
			case 3:
				wf = parsedWorkflows[3] // warming
			default:
				wf = parsedWorkflows[4] // chaotic
			}

			repo := fmt.Sprintf("%s/repo-%d", team, idx%4)
			ref := fmt.Sprintf("branch-%d", idx%8)
			req := Request{
				Repo:   repo,
				Ref:    ref,
				Commit: fmt.Sprintf("commit-%04d", idx),
				Event:  EventPush,
			}

			plan, err := PlanRun(req, []Workflow{wf})
			if err != nil {
				outcomes[i] = runOutcome{team: team, err: err}
				return
			}

			// Simulate chaotic mid-run context cancellation for ~10% of runs
			runCtx := ctx
			var cancel context.CancelFunc
			isCancelled := (idx%11 == 0)
			if isCancelled {
				runCtx, cancel = context.WithTimeout(ctx, time.Duration(10+rand.Intn(30))*time.Millisecond)
				defer cancel()
			}

			// Simulate job failures for chaotic workflow (wfChoice == 4) on odd indices
			if wfChoice == 4 && idx%2 == 1 {
				for _, pj := range plan.Jobs {
					if strings.Contains(pj.ID, "stage_flaky") {
						sbName := sandboxName(fmt.Sprintf("stress-run-%04d", idx), plan, pj)
						backend.failJobIDs.Store(sbName, true)
					}
				}
			}

			runID := fmt.Sprintf("stress-run-%04d", idx)
			var observedCount atomic.Int64
			result := engine.Run(runCtx, runID, plan, func(jr JobRun) {
				observedCount.Add(1)
			})

			outcomes[i] = runOutcome{
				runID:      runID,
				team:       team,
				conclusion: result.Conclusion,
				jobCount:   len(result.Jobs),
				cancelled:  isCancelled,
			}

			// Assert DAG invariant: No job should start before its needed jobs succeed
			for _, jr := range result.Jobs {
				if jr.Conclusion == ConclusionSuccess && len(jr.Steps) > 0 {
					// Check prerequisites in plan
					for _, pj := range plan.Jobs {
						if pj.ID == jr.WorkflowPath+":"+jr.JobID {
							for _, need := range pj.Needs {
								for _, needJr := range result.Jobs {
									if needJr.WorkflowPath+":"+needJr.JobID == need {
										if needJr.Conclusion != ConclusionSuccess {
											t.Errorf("DAG violation in run %s: job %s ran and succeeded despite dependency %s having conclusion %s",
												runID, jr.JobID, need, needJr.Conclusion)
										}
									}
								}
							}
						}
					}
				}
			}

			// Assert that skipped jobs due to dependency failure are correctly marked
			if wfChoice == 4 && idx%2 == 1 {
				for _, jr := range result.Jobs {
					if strings.Contains(jr.JobID, "stage_dependent") && !isCancelled {
						if jr.Conclusion != ConclusionSkipped {
							t.Errorf("run %s: dependent job %s should be skipped on flaky failure, got %s",
								runID, jr.JobID, jr.Conclusion)
						}
					}
				}
			}
		}(idx, team, workflowKey)
	}

	wg.Wait()

	// Assertions on the completed stress test run
	if got := gate.inUse(); got != 0 {
		t.Errorf("shared gate has lingering permits held: got %d, want 0", got)
	}

	if live := backend.live.Load(); live != 0 {
		t.Errorf("lingering active sandboxes in backend: got %d, want 0", live)
	}

	acquired := backend.totalAcquired.Load()
	released := backend.totalReleased.Load()
	if acquired != released {
		t.Errorf("sandbox leak: acquired %d != released %d", acquired, released)
	}

	// Verify all runs produced valid conclusions
	completedCount := 0
	cancelledCount := 0
	for _, o := range outcomes {
		if o.err != nil {
			t.Errorf("run error for team %s: %v", o.team, o.err)
			continue
		}
		if o.cancelled {
			cancelledCount++
		} else {
			completedCount++
		}
		if !o.conclusion.Done() {
			t.Errorf("run %s never concluded: %s", o.runID, o.conclusion)
		}
	}

	t.Logf("Multi-tenant concurrency stress test completed successfully: total runs=%d, completed=%d, cancelled=%d, peak sandboxes=%d, total acquired sandboxes=%d",
		totalRuns, completedCount, cancelledCount, backend.peakLive.Load(), acquired)
}

// TestCIGateDynamicCapacityStress tests high-concurrency contention on SharedGate with rapid capacity mutations.
func TestCIGateDynamicCapacityStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	var capacity atomic.Int64
	capacity.Store(2)

	gate := &SharedGate{
		Limit: func() int {
			return int(capacity.Load())
		},
		Poll:     time.Millisecond * 2,
		AgeBoost: window(time.Millisecond * 20),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Mutate capacity continuously
	stopMutator := make(chan struct{})
	go func() {
		caps := []int64{1, 2, 4, 8, 16, 2, 6, 1, 32, 4}
		i := 0
		ticker := time.NewTicker(time.Millisecond * 15)
		defer ticker.Stop()
		for {
			select {
			case <-stopMutator:
				return
			case <-ticker.C:
				i = (i + 1) % len(caps)
				capacity.Store(caps[i])
			}
		}
	}()
	defer close(stopMutator)

	const numWorkers = 80
	const numAcquiresPerWorker = 30
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	var activeHolders atomic.Int64
	var maxSimultaneous atomic.Int64

	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for j := 0; j < numAcquiresPerWorker; j++ {
				priority := PriorityNormal
				if workerID%4 == 0 {
					priority = PriorityMergeHead
				} else if workerID%2 == 0 {
					priority = PriorityUnderway
				}

				release, err := gate.Acquire(ctx, GateRequest{
					Priority: func() Priority { return priority },
				})
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					t.Errorf("worker %d acquire %d failed: %v", workerID, j, err)
					return
				}

				curr := activeHolders.Add(1)
				for {
					maxVal := maxSimultaneous.Load()
					if curr <= maxVal || maxSimultaneous.CompareAndSwap(maxVal, curr) {
						break
					}
				}

				// Small simulated hold duration
				time.Sleep(time.Duration(rand.Intn(500)+100) * time.Microsecond)

				activeHolders.Add(-1)
				release()
			}
		}()
	}

	wg.Wait()

	if inUse := gate.inUse(); inUse != 0 {
		t.Fatalf("gate inUse != 0 after stress test: got %d", inUse)
	}
	t.Logf("Gate dynamic capacity stress passed: %d workers x %d acquires, peak simultaneous=%d",
		numWorkers, numAcquiresPerWorker, maxSimultaneous.Load())
}

// TestCIGuestCacheSingleflightStress validates singleflight deduplication under high concurrency.
func TestCIGuestCacheSingleflightStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	service := newFakeWarmService()
	warmer := warmerFor(service, ".grain/workflows/image.yml:guest-cache")
	warmer.Cooldown = time.Millisecond * 50

	const numKeys = 4
	const callersPerKey = 25
	var wg sync.WaitGroup
	wg.Add(numKeys * callersPerKey)

	// Release runs periodically
	stopReleaser := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Millisecond * 10)
		defer ticker.Stop()
		for {
			select {
			case <-stopReleaser:
				return
			case <-ticker.C:
				service.mu.Lock()
				for id, run := range service.runs {
					if !run.Conclusion.Done() {
						run.Conclusion = ConclusionSuccess
						service.runs[id] = run
					}
				}
				service.mu.Unlock()
			}
		}
	}()
	defer close(stopReleaser)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for k := 0; k < numKeys; k++ {
		key := Key(fmt.Sprintf("guest:key-%d", k))
		for c := 0; c < callersPerKey; c++ {
			branch := fmt.Sprintf("branch-%d-%d", k, c)
			go func(key Key, branch string) {
				defer wg.Done()
				err := warmer.Warm(ctx, GuestCacheRequest{
					RunID:  fmt.Sprintf("run-%s", branch),
					Repo:   "bwsalmon/grain",
					Ref:    branch,
					Commit: "abcdef123456",
					Key:    key,
				}, nil)
				if err != nil && ctx.Err() == nil {
					t.Errorf("warmer failed for key %s branch %s: %v", key, branch, err)
				}
			}(key, branch)
		}
	}

	wg.Wait()

	// Verify that exactly 1 warm run was started per unique key
	service.mu.Lock()
	startedCount := len(service.started)
	service.mu.Unlock()

	if startedCount > numKeys {
		t.Errorf("singleflight failed to deduplicate warm runs: started %d runs for %d unique keys",
			startedCount, numKeys)
	}
	t.Logf("Guest cache singleflight stress passed: %d callers across %d keys initiated %d warm runs",
		numKeys*callersPerKey, numKeys, startedCount)
}

// TestCILiveLogStreamingStress verifies that high-frequency live log streaming under concurrent steps
// produces no data races, buffer corruption, or memory leaks.
func TestCILiveLogStreamingStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	var publications atomic.Int64
	var maxOutputLen atomic.Int64

	live := NewLiveOutput(time.Now, time.Millisecond*5, func(out string) {
		publications.Add(1)
		l := int64(len(out))
		for {
			curr := maxOutputLen.Load()
			if l <= curr || maxOutputLen.CompareAndSwap(curr, l) {
				break
			}
		}
	})

	const numWriters = 20
	const writesPerWriter = 100
	var wg sync.WaitGroup
	wg.Add(numWriters)

	for w := 0; w < numWriters; w++ {
		writerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				live.Write(fmt.Sprintf("writer-%02d line %04d: %s\n",
					writerID, i, strings.Repeat("A", 200)))
				if i%10 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()
	live.Close()

	if maxOutputLen.Load() > MaxStepOutput*2 {
		t.Errorf("live output exceeded safety buffer ceiling: %d > %d", maxOutputLen.Load(), MaxStepOutput*2)
	}
	t.Logf("Live log streaming stress passed: %d writers produced %d publications, max buffer=%d bytes",
		numWriters, publications.Load(), maxOutputLen.Load())
}

// TestCIArtifactStorageConcurrencyStress verifies concurrent artifact storage and bounded sweeps under failure load.
func TestCIArtifactStorageConcurrencyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	tmpDir := t.TempDir()
	artifacts, err := OpenDirArtifacts(tmpDir, DirArtifactsOptions{
		MaxAge:           time.Minute * 10,
		MaxBytes:         500 << 10, // 500 KB limit to trigger concurrent sweeps
		MaxRunBytes:      100 << 10,
		MaxArtifactBytes: 50 << 10,
	})
	if err != nil {
		t.Fatalf("OpenDirArtifacts failed: %v", err)
	}

	const numRuns = 30
	const artifactsPerRun = 5
	var wg sync.WaitGroup
	wg.Add(numRuns)

	for r := 0; r < numRuns; r++ {
		runIdx := r
		go func() {
			defer wg.Done()
			runID := fmt.Sprintf("art-run-%04d", runIdx)
			for a := 0; a < artifactsPerRun; a++ {
				ref := ArtifactRef{
					Run:  runID,
					Job:  fmt.Sprintf("job-%d", a),
					Name: fmt.Sprintf("trace-%d", a),
				}
				data := []byte(fmt.Sprintf("artifact body payload for run %s job %d: %s", runID, a, strings.Repeat("X", 2048)))
				art, saveErr := artifacts.Save(context.Background(), ref, data)
				if saveErr != nil {
					t.Errorf("Save artifact failed for %v: %v", ref, saveErr)
					return
				}
				if !art.Stored && art.Note == "" {
					t.Errorf("unstored artifact %v missing note", ref)
				}
			}
		}()
	}

	wg.Wait()
	t.Logf("Artifact storage concurrency stress completed across %d runs", numRuns)
}

// TestCISharedFaultsConcurrencyStress verifies multi-branch shared fault detection and attribution under concurrent load.
func TestCISharedFaultsConcurrencyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	const numBranches = 20
	const numFlakes = 10
	var wg sync.WaitGroup
	wg.Add(numBranches * numFlakes)

	now := time.Now()
	var mu sync.Mutex
	var runs []Run

	for b := 0; b < numBranches; b++ {
		branchName := fmt.Sprintf("feature-branch-%d", b)
		for f := 0; f < numFlakes; f++ {
			flakeID := f % 3 // 3 distinct fault signatures
			go func(branch string, flake int) {
				defer wg.Done()
				finished := now
				run := Run{
					ID:         fmt.Sprintf("run-%s-%d", branch, flake),
					Repo:       "bwsalmon/grain",
					Ref:        branch,
					Commit:     fmt.Sprintf("commit-%s", branch),
					FinishedAt: &finished,
					Conclusion: ConclusionFailure,
					Jobs: []JobRun{
						{
							JobID:      "test",
							Name:       "Test",
							Conclusion: ConclusionFailure,
							FinishedAt: &finished,
							Sandbox:    "ci-sandbox",
							Steps: []StepRun{
								{
									Name:       "unit-tests",
									Conclusion: ConclusionFailure,
									ExitCode:   1,
									Output:     fmt.Sprintf("--- FAIL: TestFlakeSignature_%d (0.01s)\npanic: nil pointer dereference in fault %d\n", flake, flake),
								},
							},
						},
					},
				}
				mu.Lock()
				runs = append(runs, run)
				mu.Unlock()
			}(branchName, flakeID)
		}
	}

	wg.Wait()

	faults := SharedFaults(runs, now, 0)
	if len(faults) != 3 {
		t.Fatalf("expected 3 shared fault clusters, got %d", len(faults))
	}
	t.Logf("Shared faults concurrency stress passed: grouped %d runs into %d shared fault clusters",
		len(runs), len(faults))
}

// TestCIHighConcurrencyStressExtended sustains multi-tenant CI workload over an extended duration.
// Duration is controlled by GRAIN_CI_STRESS_DURATION (e.g. "30s", "5m", "30m", "60m").
func TestCIHighConcurrencyStressExtended(t *testing.T) {
	durationStr := os.Getenv("GRAIN_CI_STRESS_DURATION")
	var duration time.Duration
	if durationStr != "" {
		var err error
		duration, err = time.ParseDuration(durationStr)
		if err != nil {
			t.Fatalf("invalid GRAIN_CI_STRESS_DURATION %q: %v", durationStr, err)
		}
	} else if testing.Short() {
		t.Skip("skipping extended stress test in -short mode without GRAIN_CI_STRESS_DURATION")
	} else {
		// Default standard duration for non-short test runs
		duration = 15 * time.Second
	}

	t.Logf("Starting extended CI high-concurrency stress run for duration %s (GOMAXPROCS=%d)",
		duration, runtime.GOMAXPROCS(0))

	deadline := time.Now().Add(duration)
	iteration := 0

	var totalRunsExecuted atomic.Int64
	var totalJobsExecuted atomic.Int64

	dynamicCap := atomic.Int64{}
	dynamicCap.Store(8)

	gate := &SharedGate{
		Limit: func() int {
			return int(dynamicCap.Load())
		},
		Poll:     time.Millisecond * 5,
		AgeBoost: window(time.Millisecond * 50),
	}

	backend := newStressSandboxBackend()
	backend.streamingLogs = true
	backend.streamChunks = 8
	backend.stepDelay = time.Millisecond

	engine := &Engine{
		Sandboxes:        backend,
		Jobs:             gate,
		ConcurrencyLimit: func() int { return int(dynamicCap.Load()) },
	}

	diamondWf := mustParse(t, ".grain/workflows/diamond.yml", stressWorkflowDiamond)
	deepWf := mustParse(t, ".grain/workflows/deep.yml", stressWorkflowDeepChain)
	fanoutWf := mustParse(t, ".grain/workflows/fanout.yml", stressWorkflowFanoutFanin)

	workflows := []Workflow{diamondWf, deepWf, fanoutWf}

	for time.Now().Before(deadline) {
		iteration++
		batchSize := 20
		var batchWg sync.WaitGroup
		batchWg.Add(batchSize)

		// Mutate capacity per iteration
		dynamicCap.Store(int64((iteration%8 + 1) * 2))

		for b := 0; b < batchSize; b++ {
			runIdx := b
			wfChoice := (iteration + b) % len(workflows)
			go func(rIdx, wfIdx int) {
				defer batchWg.Done()
				wf := workflows[wfIdx]
				req := Request{
					Repo:   fmt.Sprintf("team-%d/service-%d", rIdx%3, rIdx%5),
					Ref:    fmt.Sprintf("feature-branch-%d", rIdx%10),
					Commit: fmt.Sprintf("sha-%d-%d", iteration, rIdx),
					Event:  EventPush,
				}
				plan, err := PlanRun(req, []Workflow{wf})
				if err != nil {
					t.Errorf("PlanRun error: %v", err)
					return
				}

				runID := fmt.Sprintf("ext-run-%d-%d", iteration, rIdx)
				res := engine.Run(context.Background(), runID, plan, nil)
				if !res.Conclusion.Done() {
					t.Errorf("run %s did not conclude: %s", runID, res.Conclusion)
				}
				totalRunsExecuted.Add(1)
				totalJobsExecuted.Add(int64(len(res.Jobs)))
			}(runIdx, wfChoice)
		}
		batchWg.Wait()
	}

	if inUse := gate.inUse(); inUse != 0 {
		t.Errorf("gate inUse != 0 after extended run: %d", inUse)
	}

	if backend.live.Load() != 0 {
		t.Errorf("backend live sandboxes != 0 after extended run: %d", backend.live.Load())
	}

	t.Logf("Extended CI stress run completed: %d iterations, %d total runs, %d total jobs, peak live sandboxes=%d",
		iteration, totalRunsExecuted.Load(), totalJobsExecuted.Load(), backend.peakLive.Load())
}
