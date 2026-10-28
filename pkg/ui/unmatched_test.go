package ui_test

// The reading end of the instrument over grain's refusal markers: what
// failed runs said when grain could not name why, over a window, served
// as JSON so an operator can read a week of them without reaching the
// machine the database is on.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// record puts one unmatched failure in the store, ago before the
// client's own now.
func recordFailure(t *testing.T, store *model.Store, ctx context.Context,
	runID string, ago time.Duration, text string, suspect bool) {

	t.Helper()
	if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
		RunID: runID, TaskID: "t-" + runID, Framework: "claude",
		At: baseTime.Add(-ago), Text: text, Suspect: suspect,
	}); err != nil {
		t.Fatalf("recording %s: %v", runID, err)
	}
}

func TestUnmatchedFailuresReportsAWindowAndCountsTheSuspects(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	recordFailure(t, store, ctx, "r1", time.Hour, "the model is at capacity, retry shortly", true)
	recordFailure(t, store, ctx, "r2", 2*time.Hour, "exceeded max turns (100)", false)
	// Outside the window asked for below: this is the whole reason the
	// report takes one.
	recordFailure(t, store, ctx, "r3", 30*24*time.Hour, "something ancient", true)

	rep, err := c.UnmatchedFailures(ctx, 7*24*time.Hour, 0)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	if rep.Total != 2 {
		t.Errorf("Total = %d, want the two inside the window: %+v", rep.Total, rep.Failures)
	}
	// The count that makes the list worth scanning at all.
	if rep.Suspected != 1 {
		t.Errorf("Suspected = %d, want 1", rep.Suspected)
	}
	// Newest first: a rewording announces itself at the recent end.
	if len(rep.Failures) != 2 || rep.Failures[0].RunID != "r1" {
		t.Errorf("failures = %+v, want r1 first", rep.Failures)
	}
	if rep.Truncated {
		t.Error("Truncated = true for a report that held everything in the window")
	}
}

// A report that quietly stopped at its own limit would answer "is this
// getting worse?" with a flat line at the limit, so it says when it did.
func TestUnmatchedFailuresSaysWhenItIsTruncated(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	for i, id := range []string{"r1", "r2", "r3"} {
		recordFailure(t, store, ctx, id, time.Duration(i)*time.Minute, "503 from the edge", true)
	}

	rep, err := c.UnmatchedFailures(ctx, 7*24*time.Hour, 2)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	if !rep.Truncated {
		t.Error("Truncated = false, want a report cut short to say so")
	}
	if len(rep.Failures) != 2 {
		t.Errorf("returned %d failures, want the 2 asked for", len(rep.Failures))
	}
}

func TestUnmatchedFailuresEndpointServesTheReportAndRejectsNonsense(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)
	recordFailure(t, store, ctx, "r1", time.Hour, `api error: 5xx {"type":"busy"}`, true)

	rec := do(t, srv, http.MethodGet, "/api/unmatched-failures?window=3d", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.UnmatchedFailureReport](t, rec)
	if got, want := rep.WindowSeconds, float64(3*24*60*60); got != want {
		t.Errorf("WindowSeconds = %v, want %v", got, want)
	}
	if len(rep.Failures) != 1 || rep.Failures[0].Text != `api error: 5xx {"type":"busy"}` {
		t.Errorf("failures = %+v, want the provider's own words, verbatim", rep.Failures)
	}
	if rep.Suspected != 1 {
		t.Errorf("Suspected = %d, want the flag to survive the wire", rep.Suspected)
	}

	// A deployment with nothing to report answers with an empty list
	// rather than a null one: the caller is a program as often as a
	// person.
	empty, _ := testServer(t)
	if rep := decode[ui.UnmatchedFailureReport](t, do(t, empty, http.MethodGet, "/api/unmatched-failures", "")); rep.Failures == nil {
		t.Error("failures = null, want an empty list")
	}

	for _, query := range []string{"?window=banana", "?window=0h", "?limit=0", "?limit=nine", "?limit=99999"} {
		if rec := do(t, srv, http.MethodGet, "/api/unmatched-failures"+query, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/unmatched-failures%s = %d, want 400", query, rec.Code)
		}
	}
}

// The cluster's exec-tunnel failures are counted beside the list, not in
// it: setups the tunnel failed (retried or not), and runs whose tool
// calls it failed mid-run, inside the window only.
func TestUnmatchedFailuresCountsTheExecTunnelAsInfrastructure(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	const tunnel = "this run's sandbox could not be prepared: gitcredentials: " +
		"error: unable to upgrade connection: error dialing backend: dial timeout, backstop"
	for i, detail := range []string{tunnel, "cloning acme/widgets: fatal: repository not found"} {
		id := string(rune('a' + i))
		if err := store.PutTask(ctx, model.Task{ID: id, Title: id, Intent: model.IntentImplement}); err != nil {
			t.Fatal(err)
		}
		at := baseTime.Add(-time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id+"-1", at, model.SetupFailedOutcome, detail); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []model.ExecTransportFailure{
		{RunID: "m-1", TaskID: "m", At: baseTime.Add(-2 * time.Hour), Calls: 3},
		{RunID: "n-1", TaskID: "n", At: baseTime.Add(-3 * time.Hour), Calls: 1},
		{RunID: "old-1", TaskID: "old", At: baseTime.Add(-30 * 24 * time.Hour), Calls: 9},
	} {
		if err := store.RecordExecTransportFailure(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := c.UnmatchedFailures(ctx, 7*24*time.Hour, 0)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	want := ui.InfrastructureFailures{ExecTunnelSetups: 1, ExecTunnelToolRuns: 2, ExecTunnelToolCalls: 4}
	if rep.Infrastructure != want {
		t.Errorf("Infrastructure = %+v, want %+v", rep.Infrastructure, want)
	}
	if rep.Total != 0 {
		t.Errorf("Total = %d, want the tunnel's failures kept out of the unclassified list", rep.Total)
	}
}
