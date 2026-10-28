package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What a daemon adopting a run from a run host reads back about it
// (orchestrator.ResumeAdoptedRun): LiveRuns carries when the run's agent
// started, which is where its deadline is measured from, and the leases
// it minted while it set up are recorded against it, beside the one
// admission reserved, until each is dropped.
func TestAnAdoptedRunsClockAndLeasesAreReadBack(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: "t", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	reserved := model.Lease{Capability: "pool", Resource: "key-a", MintedBy: model.CredentialRef{Name: "m"}, IssuedAt: at}
	run := model.Run{ID: "t-1", TaskID: "t", Sandbox: "t-1", Attempt: 1, StartedAt: at, Leases: []model.Lease{reserved}}
	if err := store.StartRun(ctx, run, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	live, err := store.LiveRuns(ctx)
	if err != nil || len(live) != 1 || live[0].AgentStartedAt != nil {
		t.Fatalf("live runs = %+v (%v), want one still in setup", live, err)
	}
	agentStarted := at.Add(6 * time.Minute)
	if err := store.SetRunAgentStarted(ctx, "t-1", agentStarted); err != nil {
		t.Fatal(err)
	}
	live, err = store.LiveRuns(ctx)
	if err != nil || len(live) != 1 || live[0].AgentStartedAt == nil || !live[0].AgentStartedAt.Equal(agentStarted) {
		t.Fatalf("live runs = %+v (%v), want its agent's start %v", live, err, agentStarted)
	}

	expires := at.Add(time.Hour)
	minted := model.Lease{Capability: "key", Resource: "key-b", MintedBy: model.CredentialRef{Name: "m"},
		IssuedAt: at.Add(time.Minute), ExpiresAt: &expires}
	// The reservation comes back from materialization too, and is
	// recorded once.
	if err := store.RecordLeases(ctx, "t-1", []model.Lease{reserved, minted}); err != nil {
		t.Fatal(err)
	}
	leases, err := store.RunLeases(ctx, "t-1")
	if err != nil || len(leases) != 2 {
		t.Fatalf("leases = %+v (%v), want the reservation and the minted key", leases, err)
	}
	if leases[0].Resource != "key-a" || leases[1].Resource != "key-b" || leases[1].MintedBy.Name != "m" ||
		leases[1].ExpiresAt == nil || !leases[1].ExpiresAt.Equal(expires) {
		t.Fatalf("leases = %+v, want them as recorded", leases)
	}
	if err := store.DropLease(ctx, "t-1", "key", "key-b"); err != nil {
		t.Fatal(err)
	}
	if leases, err := store.RunLeases(ctx, "t-1"); err != nil || len(leases) != 1 || leases[0].Resource != "key-a" {
		t.Fatalf("leases after a drop = %+v (%v)", leases, err)
	}
}
