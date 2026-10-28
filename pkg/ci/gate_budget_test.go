package ci

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// budgetGateBudget is a 16 vCPU / 32 GiB budget: four sandboxes of
// budgetJobShape and no more.
func budgetGateBudget() model.Budget {
	return model.Budget{
		Host:             model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
}

func budgetJobShape() model.Commitment { return model.Commitment{CPUs: 4, MemoryMB: 8 * 1024} }

// A CI job waits while the deployment's budget is full, and the budget
// counts the agent runs too -- one ledger for both pools.
func TestSharedGateWaitsForTheBudgetAgentRunsAreHolding(t *testing.T) {
	ledger := &model.Ledger{}
	budget := budgetGateBudget()
	gate := &SharedGate{
		// No count bound anywhere.
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Millisecond,
	}
	ctx := context.Background()

	// Four agent sandboxes fill the budget, and nothing about them passes
	// through this gate.
	for i := 0; i < 4; i++ {
		if v, _ := ledger.Admit(budget, model.PoolAgent, string(rune('a'+i)), budgetJobShape()); !v.OK {
			t.Fatalf("agent sandbox %d should fit: %q", i, v.Reason)
		}
	}

	var wait Wait
	var mu sync.Mutex
	admitted := make(chan struct{})
	go func() {
		release, err := gate.Acquire(ctx, GateRequest{Want: budgetJobShape(), Wait: func(w Wait) {
			mu.Lock()
			if w.Cause != "" {
				wait = w
			}
			mu.Unlock()
		}})
		if err == nil {
			release()
		}
		close(admitted)
	}()
	select {
	case <-admitted:
		t.Fatal("a CI job was admitted onto a host whose budget the agent pool has filled")
	case <-time.After(50 * time.Millisecond):
	}

	mu.Lock()
	got := wait
	mu.Unlock()
	if got.Cause != WaitForRoom {
		t.Fatalf("a job held by the machine waits for room, got %q (%s)", got.Cause, got.Reason)
	}
	if !strings.Contains(got.Reason, "committed by this deployment's live sandboxes") {
		t.Errorf("the wait reports the live commitment: %s", got.Reason)
	}

	// An agent run ending is what lets it in, which a standing
	// reservation for the configured pool would never have done.
	ledger.Release(model.PoolAgent, "a")
	select {
	case <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never got in after an agent sandbox was released")
	}
}

// And the race the shared ledger exists to close, driven through the gate
// rather than through the ledger directly: a CI job and an agent run
// asking for the last of the budget at the same moment.
func TestSharedGateAndAgentAdmissionCannotBothSpendTheLastCapacity(t *testing.T) {
	for attempt := 0; attempt < 30; attempt++ {
		ledger := &model.Ledger{}
		budget := budgetGateBudget()
		gate := &SharedGate{
			Budget: func() model.Budget { return budget },
			Ledger: func() *model.Ledger { return ledger },
			Poll:   time.Millisecond,
		}
		// Three of the four sandboxes are live already.
		for i := 0; i < 3; i++ {
			if v, _ := ledger.Admit(budget, model.PoolAgent, string(rune('a'+i)), budgetJobShape()); !v.OK {
				t.Fatalf("agent sandbox %d should fit: %q", i, v.Reason)
			}
		}

		limits := model.Limits{Budget: budget, AgentShape: budgetJobShape(), Ledger: ledger}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		var mu sync.Mutex
		var won []string

		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.Acquire(ctx, GateRequest{Want: budgetJobShape()})
			if err != nil {
				return
			}
			mu.Lock()
			won = append(won, "ci")
			mu.Unlock()
			release()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, release := limits.ChargeSandbox("racing-run"); v.OK {
				mu.Lock()
				won = append(won, "agent")
				mu.Unlock()
				release()
			}
		}()

		// Whichever one wins releases, which lets the other in; what must
		// never happen is both holding at once, and the ledger going over
		// the budget is how that would show.
		time.Sleep(20 * time.Millisecond)
		if live := ledger.Live(); live.CPUs > 16 || live.MemoryMB > 32*1024 {
			cancel()
			wg.Wait()
			t.Fatalf("attempt %d: the two pools between them committed %s of a 16 vCPU / 32.0 GiB budget",
				attempt, live)
		}
		cancel()
		wg.Wait()
		mu.Lock()
		n := len(won)
		mu.Unlock()
		if n == 0 {
			t.Fatalf("attempt %d: neither the job nor the run was given the last sandbox", attempt)
		}
	}
}
