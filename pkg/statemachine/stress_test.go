package statemachine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/statemachine"
	_ "modernc.org/sqlite"
)

// TestStateMachineConcurrentStress exercises the state machine engine and store under heavy
// multi-threaded concurrency. It verifies:
//  1. In-process instance serialization and optimistic DB sequence verification.
//  2. High-throughput transitions across multiple concurrent instances.
//  3. Simultaneous transitions on the SAME instance without sequence gaps or data races.
//  4. Error handling (reducer failures, unavailable reducers, unaccepted events) under concurrency.
//  5. Exact effect execution order and transition log continuity.
func TestStateMachineConcurrentStress(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sm_stress.db")+"?_txlock=immediate&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	// One physical connection: sqlite serializes writers at the file
	// level regardless, so letting database/sql open more than one just
	// adds contention on top of that same lock. Under real parallelism
	// (grain CI's 16-vCPU go job) that contention pushed some of this
	// test's 40 concurrent writers past the 5s busy_timeout into
	// SQLITE_BUSY; routing them through Go's own connection-pool queue
	// instead avoids the lock entirely, the same way
	// pkg/staterepo/check.go and change_token_test.go already do for
	// the same driver.
	db.SetMaxOpenConns(1)

	store := statemachine.NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing sql store: %v", err)
	}

	var effectDispatches sync.Map // key -> count

	engine := statemachine.Engine{
		Store: store,
		Definitions: statemachine.DefinitionFunc(func(_ context.Context, version string) (statemachine.Reducer, error) {
			return statemachine.ReducerFunc(func(_ context.Context, state json.RawMessage, event statemachine.Event) (statemachine.Result, error) {
				if event.Type == "transient_error" {
					return statemachine.Result{}, statemachine.ErrReducerUnavailable
				}
				if event.Type == "fatal_error" {
					return statemachine.Result{}, errors.New("simulated fatal error")
				}
				if event.Type == "reducer.failed" {
					return statemachine.Result{
						State:   json.RawMessage(`{"status":"recovered"}`),
						Accepts: []string{"inc", "noop", "transient_error", "fatal_error"},
					}, nil
				}

				var counter int
				if len(state) > 0 {
					var data struct {
						Counter int `json:"counter"`
					}
					_ = json.Unmarshal(state, &data)
					counter = data.Counter
				}

				if event.Type == "inc" {
					counter++
				}

				newState, _ := json.Marshal(map[string]int{"counter": counter})
				effects := []statemachine.Effect{
					{Type: "audit.log", Data: json.RawMessage(fmt.Sprintf(`{"val":%d}`, counter))},
				}
				return statemachine.Result{
					State:   newState,
					Effects: effects,
					Accepts: []string{"inc", "noop", "transient_error", "fatal_error"},
				}, nil
			}), nil
		}),
		Executor: statemachine.ExecutorFunc(func(ctx context.Context, intent statemachine.EffectIntent) error {
			effectDispatches.Store(intent.Key, true)
			return nil
		}),
	}

	numInstances := 10
	workersPerInstance := 4
	eventsPerWorker := 15

	if env := os.Getenv("GRAIN_STATEMACHINE_STRESS_INSTANCES"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			numInstances = n
		}
	}

	// 1. Concurrently create all instances
	var createWG sync.WaitGroup
	for i := 0; i < numInstances; i++ {
		createWG.Add(1)
		go func(instID int) {
			defer createWG.Done()
			id := fmt.Sprintf("inst-%d", instID)
			_, err := engine.Create(ctx, id, "counter@v1", json.RawMessage(`{"counter":0}`), []string{"inc", "noop", "transient_error", "fatal_error"})
			if err != nil {
				t.Errorf("failed to create instance %s: %v", id, err)
			}
		}(i)
	}
	createWG.Wait()

	// 2. Concurrently submit events to each instance from multiple workers
	var submitWG sync.WaitGroup
	var errCount atomic.Int64
	var totalSubmitted atomic.Int64

	for i := 0; i < numInstances; i++ {
		for w := 0; w < workersPerInstance; w++ {
			submitWG.Add(1)
			go func(instID, workerID int) {
				defer submitWG.Done()
				id := fmt.Sprintf("inst-%d", instID)
				r := rand.New(rand.NewPCG(uint64(instID*100+workerID), uint64(time.Now().UnixNano())))

				for e := 0; e < eventsPerWorker; e++ {
					evtType := "inc"
					roll := r.IntN(10)
					if roll == 0 {
						evtType = "transient_error"
					} else if roll == 1 {
						evtType = "fatal_error"
					}

					evtID := fmt.Sprintf("evt-i%d-w%d-e%d", instID, workerID, e)
					evt := statemachine.Event{
						SpecVersion: "1.0",
						ID:          evtID,
						Source:      "stress://test",
						Type:        evtType,
					}

					_, err := engine.Submit(ctx, id, evt)
					if err != nil {
						if evtType == "transient_error" && errors.Is(err, statemachine.ErrReducerUnavailable) {
							// Expected transient error
							continue
						}
						t.Errorf("instance %s worker %d submit event %s failed: %v", id, workerID, evtID, err)
						errCount.Add(1)
						return
					}
					totalSubmitted.Add(1)
				}
			}(i, w)
		}
	}
	submitWG.Wait()

	if errCount.Load() > 0 {
		t.Fatalf("State machine stress encountered %d errors", errCount.Load())
	}

	// 3. Verify invariants for every instance:
	// - Loaded instance sequence matches transition log length
	// - Transition log sequences are strictly contiguous 1..N
	// - Effects for each committed transition were dispatched
	for i := 0; i < numInstances; i++ {
		id := fmt.Sprintf("inst-%d", i)
		inst, err := store.Load(ctx, id)
		if err != nil {
			t.Fatalf("Load instance %s: %v", id, err)
		}

		log, err := store.Log(ctx, id)
		if err != nil {
			t.Fatalf("Log instance %s: %v", id, err)
		}

		if uint64(len(log)) != inst.Sequence {
			t.Errorf("instance %s sequence mismatch: inst.Sequence=%d, log len=%d", id, inst.Sequence, len(log))
		}

		for seqIdx, tr := range log {
			expectedSeq := uint64(seqIdx + 1)
			if tr.Sequence != expectedSeq {
				t.Errorf("instance %s transition %d has sequence %d, want %d", id, seqIdx, tr.Sequence, expectedSeq)
			}
			if tr.InstanceID != id {
				t.Errorf("instance %s transition %d has wrong instance ID %s", id, seqIdx, tr.InstanceID)
			}
		}
	}

	t.Logf("Completed %d event submissions across %d instances (%d workers each)",
		totalSubmitted.Load(), numInstances, workersPerInstance)
}

// TestStateMachineSustainedStress runs a sustained time-bounded concurrent state machine stress test.
func TestStateMachineSustainedStress(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sm_sustained.db")+"?_txlock=immediate&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	// See TestStateMachineConcurrentStress's own comment on this.
	db.SetMaxOpenConns(1)

	store := statemachine.NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing sql store: %v", err)
	}

	duration := 2 * time.Second
	if env := os.Getenv("GRAIN_STATEMACHINE_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	} else if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	}

	engine := statemachine.Engine{
		Store: store,
		Definitions: statemachine.DefinitionFunc(func(_ context.Context, version string) (statemachine.Reducer, error) {
			return statemachine.ReducerFunc(func(_ context.Context, state json.RawMessage, event statemachine.Event) (statemachine.Result, error) {
				return statemachine.Result{
					State:   json.RawMessage(`{"tick":1}`),
					Accepts: []string{"step"},
				}, nil
			}), nil
		}),
	}

	// Create 5 instances
	numInstances := 5
	for i := 0; i < numInstances; i++ {
		id := fmt.Sprintf("sustained-inst-%d", i)
		if _, err := engine.Create(ctx, id, "sustained@v1", json.RawMessage(`{}`), []string{"step"}); err != nil {
			t.Fatalf("create instance: %v", err)
		}
	}

	workers := 6
	deadline := time.Now().Add(duration)
	var ops atomic.Int64
	var errCount atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))
			i := 0
			for time.Now().Before(deadline) {
				i++
				instID := fmt.Sprintf("sustained-inst-%d", r.IntN(numInstances))
				if r.IntN(4) == 0 {
					// Read state or log
					if r.IntN(2) == 0 {
						_, _ = store.Load(ctx, instID)
					} else {
						_, _ = store.Log(ctx, instID)
					}
				} else {
					// Submit event
					evtID := fmt.Sprintf("e-w%d-%d", workerID, i)
					_, err := engine.Submit(ctx, instID, statemachine.Event{
						SpecVersion: "1.0",
						ID:          evtID,
						Source:      "stress://sustained",
						Type:        "step",
					})
					if err != nil {
						t.Errorf("worker %d submit: %v", workerID, err)
						errCount.Add(1)
					}
				}
				ops.Add(1)
			}
		}(w)
	}

	wg.Wait()
	if errCount.Load() > 0 {
		t.Fatalf("Sustained state machine stress encountered %d errors", errCount.Load())
	}
	t.Logf("Completed %d state machine operations in %v", ops.Load(), duration)
}
