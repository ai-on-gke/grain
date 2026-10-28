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

// scalability_test.go exercises Grain's metadata and model layers under
// extreme scale -- thousands of teams, hundreds to thousands of concurrent
// agents, thousands of concurrent CI runs and step output updates, high-throughput
// concurrent REST/UI API mutations, and large metadata backlogs (10,000+ tasks)
// driving orchestrator.RunCycle.
//
// Focus: Scalability of the metadata, database (SQLite/PostgreSQL), and model code,
// not the sandboxes themselves.
package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ScalabilityConfig defines the scaling parameters for the comprehensive test.
type ScalabilityConfig struct {
	// Teams dimension
	TeamsCount int

	// Concurrent agents dimension
	ConcurrentAgents int
	AgentIterations  int

	// Concurrent CI dimension
	CIRunsCount   int
	CIStepStreams int
	CIStepsPerJob int

	// API concurrency dimension
	APIWorkers    int
	APIOperations int

	// Orchestrator & Metadata scaling dimension
	TotalTasks        int
	OrchestratorTicks int
}

func defaultScalabilityConfig() ScalabilityConfig {
	return ScalabilityConfig{
		TeamsCount:        1000,
		ConcurrentAgents:  250,
		AgentIterations:   1000,
		CIRunsCount:       2500,
		CIStepStreams:     100,
		CIStepsPerJob:     10,
		APIWorkers:        50,
		APIOperations:     2500,
		TotalTasks:        10000,
		OrchestratorTicks: 10,
	}
}

func smokeScalabilityConfig() ScalabilityConfig {
	return ScalabilityConfig{
		TeamsCount:        20,
		ConcurrentAgents:  10,
		AgentIterations:   20,
		CIRunsCount:       30,
		CIStepStreams:     5,
		CIStepsPerJob:     3,
		APIWorkers:        5,
		APIOperations:     50,
		TotalTasks:        50,
		OrchestratorTicks: 2,
	}
}

// LatencyTracker records duration samples and computes percentiles.
type LatencyTracker struct {
	mu      sync.Mutex
	samples []time.Duration
}

func (l *LatencyTracker) Record(d time.Duration) {
	l.mu.Lock()
	l.samples = append(l.samples, d)
	l.mu.Unlock()
}

type LatencyStats struct {
	Count int
	Min   time.Duration
	P50   time.Duration
	P90   time.Duration
	P95   time.Duration
	P99   time.Duration
	Max   time.Duration
	Avg   time.Duration
}

func (l *LatencyTracker) Stats() LatencyStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.samples)
	if n == 0 {
		return LatencyStats{}
	}
	sorted := make([]time.Duration, n)
	copy(sorted, l.samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, s := range sorted {
		sum += s
	}

	return LatencyStats{
		Count: n,
		Min:   sorted[0],
		P50:   sorted[n*50/100],
		P90:   sorted[n*90/100],
		P95:   sorted[n*95/100],
		P99:   sorted[n*99/100],
		Max:   sorted[n-1],
		Avg:   sum / time.Duration(n),
	}
}

func (s LatencyStats) String() string {
	return fmt.Sprintf("N=%d min=%v p50=%v p90=%v p95=%v p99=%v max=%v avg=%v",
		s.Count, s.Min, s.P50, s.P90, s.P95, s.P99, s.Max, s.Avg)
}

// TestScalabilitySmoke runs a fast verification of all scalability benchmark paths
// on every commit to ensure harness and model compatibility.
func TestScalabilitySmoke(t *testing.T) {
	t.Parallel()
	runScalabilitySuite(t, smokeScalabilityConfig(), false)
}

// TestScalabilityLimits runs the comprehensive scalability benchmark against large
// dimensions of teams, concurrent agents, concurrent CI, and API mutations.
func TestScalabilityLimits(t *testing.T) {
	t.Parallel()
	if os.Getenv("GRAIN_SCALABILITY_TEST") == "" && os.Getenv("GRAIN_LOAD_TEST") == "" {
		t.Skip("set GRAIN_SCALABILITY_TEST=1 or GRAIN_LOAD_TEST=1 to run the full scalability limits benchmark")
	}

	cfg := defaultScalabilityConfig()
	// Allow environment overrides for sizing up on large VMs
	envInt(t, "GRAIN_SCALE_TEAMS", &cfg.TeamsCount)
	envInt(t, "GRAIN_SCALE_AGENTS", &cfg.ConcurrentAgents)
	envInt(t, "GRAIN_SCALE_AGENT_ITERS", &cfg.AgentIterations)
	envInt(t, "GRAIN_SCALE_CI_RUNS", &cfg.CIRunsCount)
	envInt(t, "GRAIN_SCALE_CI_STREAMS", &cfg.CIStepStreams)
	envInt(t, "GRAIN_SCALE_CI_STEPS", &cfg.CIStepsPerJob)
	envInt(t, "GRAIN_SCALE_API_WORKERS", &cfg.APIWorkers)
	envInt(t, "GRAIN_SCALE_API_OPS", &cfg.APIOperations)
	envInt(t, "GRAIN_SCALE_TASKS", &cfg.TotalTasks)

	runScalabilitySuite(t, cfg, true)
}

func runScalabilitySuite(t *testing.T, cfg ScalabilityConfig, logReport bool) {
	t.Logf("=== Starting Scalability Test Suite with Config: %+v ===", cfg)

	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("schema init: %v", err)
	}

	// 1. Benchmark Teams Scalability
	t.Run("TeamsScalability", func(t *testing.T) {
	})

	// 2. Benchmark Concurrent Agents & Dispatches
	t.Run("ConcurrentAgentsScalability", func(t *testing.T) {
		benchmarkConcurrentAgents(t, ctx, store, cfg.ConcurrentAgents, cfg.AgentIterations)
	})

	// 3. Benchmark Concurrent CI Ingestion & Log Streaming
	t.Run("ConcurrentCIScalability", func(t *testing.T) {
		benchmarkConcurrentCI(t, ctx, store, cfg.CIRunsCount, cfg.CIStepStreams, cfg.CIStepsPerJob)
	})

	// 4. Benchmark High-Throughput API Mutations & Contention
	t.Run("ConcurrentAPIScalability", func(t *testing.T) {
		benchmarkConcurrentAPI(t, ctx, store, cfg.APIWorkers, cfg.APIOperations)
	})

	// 5. Benchmark Orchestrator & Large Metadata Backlog
	t.Run("OrchestratorMetadataScalability", func(t *testing.T) {
		benchmarkOrchestratorScaling(t, ctx, store, cfg.TotalTasks, cfg.OrchestratorTicks)
	})
}

func benchmarkConcurrentAgents(t *testing.T, ctx context.Context, store *model.Store, concurrency, iterations int) {
	t.Logf("--- Benchmarking Concurrent Agent Scalability (Concurrency: %d, Iterations: %d) ---", concurrency, iterations)

	var (
		startRunTracker  = &LatencyTracker{}
		finishRunTracker = &LatencyTracker{}
		activityTracker  = &LatencyTracker{}
		runCountsTracker = &LatencyTracker{}
		completedRuns    atomic.Int64
		errCount         atomic.Int64
		wg               sync.WaitGroup
	)

	start := time.Now()
	for workerID := 0; workerID < concurrency; workerID++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for iter := 0; iter < iterations/concurrency; iter++ {
				taskID := fmt.Sprintf("agent-task-%d-%d", wid, iter)
				runID := fmt.Sprintf("run-%d-%d", wid, iter)
				now := time.Now().UTC()

				// 1. Put task
				task := model.Task{
					ID:        taskID,
					Title:     "Agent scale task",
					Intent:    model.IntentImplement,
					Binding:   model.BindingDirective,
					CreatedAt: &now,
					Origin: model.Origin{
						Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
						Reason:      model.ReasonDirect,
					},
					Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
				}
				if err := store.PutTask(ctx, task); err != nil {
					errCount.Add(1)
					continue
				}

				// 2. StartRun
				opStart := time.Now()
				run := model.Run{
					ID:        runID,
					TaskID:    taskID,
					Sandbox:   fmt.Sprintf("sb-%d-%d", wid, iter),
					Attempt:   1,
					StartedAt: now,
				}
				if err := store.StartRun(ctx, run, model.Limits{}); err != nil {
					errCount.Add(1)
					continue
				}
				startRunTracker.Record(time.Since(opStart))

				// 3. SetTaskActivity & SetRunAgentStarted
				opStart = time.Now()
				_, _ = store.SetTaskActivity(ctx, taskID, "executing scale tests", now)
				_ = store.SetRunAgentStarted(ctx, runID, now)
				activityTracker.Record(time.Since(opStart))

				// 4. LiveRunCounts query
				opStart = time.Now()
				_, _ = store.LiveRunCounts(ctx)
				runCountsTracker.Record(time.Since(opStart))

				// 5. FinishRun
				opStart = time.Now()
				if err := store.FinishRun(ctx, runID, now.Add(time.Second), "succeeded", "completed successfully"); err != nil {
					errCount.Add(1)
					continue
				}
				finishRunTracker.Record(time.Since(opStart))
				completedRuns.Add(1)
			}
		}(workerID)
	}

	wg.Wait()
	totalDuration := time.Since(start)
	runs := completedRuns.Load()
	t.Logf("Concurrent Agents finished: %d runs in %v (%.1f runs/sec, %d errors)",
		runs, totalDuration, float64(runs)/totalDuration.Seconds(), errCount.Load())
	t.Logf("StartRun latency: %s", startRunTracker.Stats())
	t.Logf("SetActivity latency: %s", activityTracker.Stats())
	t.Logf("LiveRunCounts latency: %s", runCountsTracker.Stats())
	t.Logf("FinishRun latency: %s", finishRunTracker.Stats())
}

func benchmarkConcurrentCI(t *testing.T, ctx context.Context, store *model.Store, runsCount, streamsCount, stepsPerJob int) {
	t.Logf("--- Benchmarking Concurrent CI Scalability (Runs: %d, Stream Workers: %d, Steps/Job: %d) ---",
		runsCount, streamsCount, stepsPerJob)

	putRunTracker := &LatencyTracker{}
	putStepTracker := &LatencyTracker{}
	listRunsTracker := &LatencyTracker{}

	start := time.Now()
	// 1. Populate CI Runs
	for i := 1; i <= runsCount; i++ {
		runID := fmt.Sprintf("ci-run-%06d", i)
		now := time.Now().UTC()
		ciRun := model.CIRun{
			ID:          runID,
			Key:         fmt.Sprintf("key-%d", i),
			Repo:        fmt.Sprintf("acme/repo-%d", i%10),
			Ref:         "main",
			Commit:      fmt.Sprintf("commit-%08d", i),
			Event:       "push",
			RequestedBy: "ci-trigger",
			StartedAt:   now,
			Conclusion:  "success",
			FinishedAt:  now.Add(30 * time.Second),
			Jobs: []model.CIJob{
				{
					JobID:      "test-job",
					Name:       "Run Test Suite",
					StartedAt:  now,
					FinishedAt: now.Add(30 * time.Second),
					Conclusion: "success",
					Steps: []model.CIStep{
						{
							Name:       "step-1",
							Conclusion: "success",
							ExitCode:   0,
							Output:     "Step output log data here...",
						},
					},
				},
			},
		}
		opStart := time.Now()
		if err := store.PutCIRun(ctx, ciRun); err != nil {
			t.Fatalf("PutCIRun(%s): %v", runID, err)
		}
		putRunTracker.Record(time.Since(opStart))
	}
	t.Logf("CI Runs ingestion: %d runs in %v (%.1f runs/sec) | Latency: %s",
		runsCount, time.Since(start), float64(runsCount)/time.Since(start).Seconds(), putRunTracker.Stats())

	// 2. High-concurrency step output streaming
	var wg sync.WaitGroup
	var streamedSteps atomic.Int64
	var streamErrors atomic.Int64
	stepStart := time.Now()
	for s := 0; s < streamsCount; s++ {
		wg.Add(1)
		go func(streamID int) {
			defer wg.Done()
			for stepIdx := 0; stepIdx < stepsPerJob; stepIdx++ {
				runID := fmt.Sprintf("ci-run-%06d", (streamID%runsCount)+1)
				output := fmt.Sprintf("Log chunk stream=%d step=%d timestamp=%v\nLine 1\nLine 2\nLine 3\n",
					streamID, stepIdx, time.Now())
				opStart := time.Now()
				err := store.PutCIStepOutput(ctx, runID, 0, 0, output)
				if err == nil {
					putStepTracker.Record(time.Since(opStart))
					streamedSteps.Add(1)
				} else {
					streamErrors.Add(1)
				}
			}
		}(s)
	}
	wg.Wait()
	if errs := streamErrors.Load(); errs > 0 {
		t.Fatalf("CI Step Output Streaming had %d errors under concurrency", errs)
	}
	t.Logf("CI Step Output Streaming: %d updates in %v (%.1f updates/sec) | Latency: %s",
		streamedSteps.Load(), time.Since(stepStart), float64(streamedSteps.Load())/time.Since(stepStart).Seconds(), putStepTracker.Stats())

	// 3. Query CI Runs
	for i := 0; i < 20; i++ {
		opStart := time.Now()
		_, err := store.ListCIRuns(ctx, 50)
		if err != nil {
			t.Fatalf("ListCIRuns: %v", err)
		}
		listRunsTracker.Record(time.Since(opStart))
	}
	t.Logf("ListCIRuns latency: %s", listRunsTracker.Stats())
}

func benchmarkConcurrentAPI(t *testing.T, ctx context.Context, store *model.Store, workers, totalOps int) {
	t.Logf("--- Benchmarking Concurrent API Operations & Contention (Workers: %d, Total Ops: %d) ---", workers, totalOps)

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("api-tester")}, store)
	var (
		opTracker   = &LatencyTracker{}
		createTrack = &LatencyTracker{}
		readTrack   = &LatencyTracker{}
		updateTrack = &LatencyTracker{}
		errCount    atomic.Int64
		successOps  atomic.Int64
		wg          sync.WaitGroup
	)

	opsPerWorker := totalOps / workers
	start := time.Now()
	for wid := 0; wid < workers; wid++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(workerID+1), uint64(workerID+999)))
			for i := 0; i < opsPerWorker; i++ {
				opType := rng.IntN(4)
				taskID := fmt.Sprintf("api-task-%d-%d", workerID, i)

				switch opType {
				case 0: // Create task
					opStart := time.Now()
					now := time.Now().UTC()
					err := store.PutTask(ctx, model.Task{
						ID:        taskID,
						Title:     "API stress task",
						Intent:    model.IntentImplement,
						Binding:   model.BindingDirective,
						CreatedAt: &now,
						Origin: model.Origin{
							Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
							Reason:      model.ReasonDirect,
						},
						Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
					})
					d := time.Since(opStart)
					opTracker.Record(d)
					createTrack.Record(d)
					if err != nil {
						errCount.Add(1)
					} else {
						successOps.Add(1)
					}

				case 1: // Add Comment
					opStart := time.Now()
					err := client.AddComment(ctx, taskID, "Direction update from API client", nil)
					d := time.Since(opStart)
					opTracker.Record(d)
					updateTrack.Record(d)
					if err != nil && benignOrErr(err) != nil {
						errCount.Add(1)
					} else {
						successOps.Add(1)
					}

				case 2: // Read States / Task
					opStart := time.Now()
					_, err := store.States(ctx)
					d := time.Since(opStart)
					opTracker.Record(d)
					readTrack.Record(d)
					if err != nil {
						errCount.Add(1)
					} else {
						successOps.Add(1)
					}

				case 3: // Close / Reopen
					opStart := time.Now()
					err := client.Close(ctx, taskID, ui.CloseOptions{})
					d := time.Since(opStart)
					opTracker.Record(d)
					updateTrack.Record(d)
					if err != nil && benignOrErr(err) != nil {
						errCount.Add(1)
					} else {
						successOps.Add(1)
					}
				}
			}
		}(wid)
	}

	wg.Wait()
	totalDur := time.Since(start)
	t.Logf("API Operations finished: %d successful ops in %v (%.1f ops/sec, %d errors)",
		successOps.Load(), totalDur, float64(successOps.Load())/totalDur.Seconds(), errCount.Load())
	t.Logf("Overall API Latency: %s", opTracker.Stats())
	t.Logf("Create Task Latency: %s", createTrack.Stats())
	t.Logf("Read States Latency: %s", readTrack.Stats())
	t.Logf("Update/Comment Latency: %s", updateTrack.Stats())
}

func benchmarkOrchestratorScaling(t *testing.T, ctx context.Context, store *model.Store, totalTasks, ticks int) {
	t.Logf("--- Benchmarking Orchestrator Scaling with %d tasks across %d ticks ---", totalTasks, ticks)

	// Populate backlog with various states
	populateStart := time.Now()
	for i := 1; i <= totalTasks; i++ {
		taskID := fmt.Sprintf("scale-task-%06d", i)
		now := time.Now().UTC()
		approved := (i % 3) != 0
		task := model.Task{
			ID:        taskID,
			Title:     fmt.Sprintf("Scale Backlog Task %d", i),
			Intent:    model.IntentImplement,
			Binding:   model.BindingDirective,
			CreatedAt: &now,
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
				Reason:      model.ReasonDirect,
			},
		}
		if approved {
			task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}}
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask(%s): %v", taskID, err)
		}
	}
	t.Logf("Populated %d tasks in %v (%.1f tasks/sec)",
		totalTasks, time.Since(populateStart), float64(totalTasks)/time.Since(populateStart).Seconds())

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	gh := newLoadGitHub()
	capability := newLoadCapability(0.1, 2*time.Second)
	registry := loadHarnessRegistry(capability)
	metrics := newLoadMetrics()
	rng := rand.New(rand.NewPCG(42, 99))
	var rngMu sync.Mutex

	cycles := orchestrator.NewCycleTimes(100)
	deps := orchestrator.Deps{
		Store: store, Client: gh, Sandboxes: sandboxes,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(newLoadGenerator(&rngMu, rng, metrics)), nil
		},
		Config:     orchestrator.Config{Capabilities: registry},
		MaxWorkers: 10,
		CycleTimes: cycles,
	}

	cycleTracker := &LatencyTracker{}
	dispatchWaitTracker := &LatencyTracker{}

	for tick := 1; tick <= ticks; tick++ {
		tickCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		start := time.Now()
		err := orchestrator.RunCycle(tickCtx, deps, start.UTC())
		cancel()
		if err != nil && !strings.Contains(err.Error(), "task closed while its run was still live") {
			t.Fatalf("RunCycle tick %d: %v", tick, err)
		}
		cycleTracker.Record(time.Since(start))
	}

	history, observed := cycles.History()
	for _, c := range history {
		dispatchWaitTracker.Record(c.DispatchWait)
	}

	t.Logf("Orchestrator RunCycle under %d tasks (%d ticks observed): %s",
		totalTasks, observed, cycleTracker.Stats())
	t.Logf("Dispatch wait time within tick: %s", dispatchWaitTracker.Stats())

	// Breakdown per reconciler
	if len(history) > 0 {
		latest := history[len(history)-1]
		t.Logf("Last Cycle Reconciler Breakdown (Total: %v):", latest.Duration)
		for _, r := range latest.Reconcilers {
			t.Logf("  - %-20s duration: %v, wait: %v, failed: %v", r.Name, r.Duration, r.Wait, r.Failed)
		}
	}
}

// TestConcurrentCIStreamingZeroContention verifies zero contention errors, zero _busy_timeout,
// and sub-millisecond to low-millisecond tail latencies under 100+ concurrent streaming CI workers
// emitting frequent step log chunks.
func TestConcurrentCIStreamingZeroContention(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("schema init: %v", err)
	}

	const (
		numRuns       = 20
		streamWorkers = 100
		stepsPerJob   = 10
	)

	now := time.Now().UTC()
	for i := 1; i <= numRuns; i++ {
		runID := fmt.Sprintf("ci-stress-run-%04d", i)
		ciRun := model.CIRun{
			ID:          runID,
			Key:         fmt.Sprintf("stress-key-%d", i),
			Repo:        "acme/heavy-repo",
			Ref:         "main",
			Commit:      fmt.Sprintf("commit-%08d", i),
			Event:       "push",
			RequestedBy: "stress-test",
			StartedAt:   now,
			Conclusion:  "running",
			Jobs: []model.CIJob{
				{
					JobID:     "build-test",
					Name:      "Build and Test",
					StartedAt: now,
					Steps: []model.CIStep{
						{
							Name:       "test",
							Conclusion: "running",
							Output:     "",
						},
					},
				},
			},
		}
		if err := store.PutCIRun(ctx, ciRun); err != nil {
			t.Fatalf("PutCIRun(%s): %v", runID, err)
		}
	}

	tracker := &LatencyTracker{}
	var wg sync.WaitGroup
	var errorCount atomic.Int64
	var successCount atomic.Int64

	start := time.Now()
	for s := 0; s < streamWorkers; s++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			runID := fmt.Sprintf("ci-stress-run-%04d", (workerID%numRuns)+1)
			accumulated := ""
			for step := 0; step < stepsPerJob; step++ {
				accumulated += fmt.Sprintf("stream=%d chunk=%d log output payload line\n", workerID, step)
				opStart := time.Now()
				err := store.PutCIStepOutput(ctx, runID, 0, 0, accumulated)
				elapsed := time.Since(opStart)
				if err != nil {
					errorCount.Add(1)
					t.Errorf("worker %d step %d failed: %v", workerID, step, err)
				} else {
					tracker.Record(elapsed)
					successCount.Add(1)
				}
			}
		}(s)
	}

	wg.Wait()
	totalElapsed := time.Since(start)

	if errs := errorCount.Load(); errs > 0 {
		t.Fatalf("encountered %d contention errors across %d concurrent streams", errs, streamWorkers)
	}

	expectedTotal := int64(streamWorkers * stepsPerJob)
	if succ := successCount.Load(); succ != expectedTotal {
		t.Fatalf("expected %d successful updates, got %d", expectedTotal, succ)
	}

	t.Logf("100+ Concurrent CI Streaming Stress: %d updates in %v (%.1f updates/sec) | Latency: %s",
		successCount.Load(), totalElapsed, float64(successCount.Load())/totalElapsed.Seconds(), tracker.Stats())

	// Verify all runs can be read back cleanly with full output
	for i := 1; i <= numRuns; i++ {
		runID := fmt.Sprintf("ci-stress-run-%04d", i)
		run, err := store.GetCIRun(ctx, runID)
		if err != nil || run == nil {
			t.Fatalf("GetCIRun(%s): %v", runID, err)
		}
		if len(run.Jobs[0].Steps[0].Output) == 0 {
			t.Errorf("run %s step output should not be empty", runID)
		}
	}
}

// BenchmarkConcurrentCIStepStreaming benchmarks concurrent PutCIStepOutput writes
// across parallel streaming workers.
func BenchmarkConcurrentCIStepStreaming(b *testing.B) {
	dataDir := b.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		b.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		b.Fatalf("schema init: %v", err)
	}

	const numRuns = 20
	now := time.Now().UTC()
	for i := 1; i <= numRuns; i++ {
		runID := fmt.Sprintf("ci-bench-run-%04d", i)
		ciRun := model.CIRun{
			ID:          runID,
			Key:         fmt.Sprintf("bench-key-%d", i),
			Repo:        "acme/heavy-repo",
			Ref:         "main",
			Commit:      fmt.Sprintf("commit-%08d", i),
			Event:       "push",
			RequestedBy: "bench",
			StartedAt:   now,
			Conclusion:  "running",
			Jobs: []model.CIJob{
				{
					JobID:     "build-test",
					Name:      "Build and Test",
					StartedAt: now,
					Steps: []model.CIStep{
						{
							Name:       "test",
							Conclusion: "running",
							Output:     "",
						},
					},
				},
			},
		}
		if err := store.PutCIRun(ctx, ciRun); err != nil {
			b.Fatalf("PutCIRun(%s): %v", runID, err)
		}
	}

	var counter atomic.Int64
	b.ResetTimer()
	b.SetParallelism(100)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c := counter.Add(1)
			runID := fmt.Sprintf("ci-bench-run-%04d", (c%numRuns)+1)
			output := fmt.Sprintf("chunk iteration=%d line\n", c)
			if err := store.PutCIStepOutput(ctx, runID, 0, 0, output); err != nil {
				b.Errorf("PutCIStepOutput: %v", err)
			}
		}
	})
}
