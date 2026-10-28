package metrics_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Runs whose setup the cluster's exec tunnel failed -- retried as a setup
// suspension, or ended setup-failed after the retries -- are counted as
// the cluster's health, apart from every other setup failure.
func TestExecTunnelFailuresAreCountedAsTheClustersHealth(t *testing.T) {
	const tunnel = "this run's sandbox could not be prepared: gitcredentials: writing remote .git-credentials and .gitconfig: " +
		"error: unable to upgrade connection: error dialing backend: dial timeout, backstop"
	run := func(attempt int, outcome, detail string) model.RunTiming {
		return model.RunTiming{TaskID: "1", Attempt: attempt,
			StartedAt:  *at(time.Duration(attempt) * time.Hour),
			FinishedAt: at(time.Duration(attempt)*time.Hour + time.Minute),
			Outcome:    outcome, Detail: detail}
	}
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{{TaskID: "1", CreatedAt: at(0), ApprovedAt: at(0)}},
		Runs: []model.RunTiming{
			run(1, model.SuspendedOutcome, tunnel+"; grain retries it automatically in 10s"),
			run(2, model.SetupFailedOutcome, tunnel+" (the cluster's exec tunnel is failing; 3 automatic retries did not get past it)"),
			run(3, model.SetupFailedOutcome, "this run's sandbox could not be prepared: cloning acme/widgets: fatal: repository not found"),
			run(4, "failed", tunnel),
			withToolFailures(run(5, "succeeded", ""), 3),
			withToolFailures(run(6, "failed", ""), 1),
		},
	})
	if got, want := rep.Runs.ExecTunnelFailures, 2; got != want {
		t.Errorf("ExecTunnelFailures = %d, want %d", got, want)
	}
	if got, want := rep.Runs.ExecTunnelToolCalls, 4; got != want {
		t.Errorf("ExecTunnelToolCalls = %d, want %d", got, want)
	}
}

func withToolFailures(r model.RunTiming, calls int) model.RunTiming {
	r.ExecTransportFailures = calls
	return r
}
