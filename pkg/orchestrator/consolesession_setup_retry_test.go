package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// sessionTunnelFailure is run 831-35's own setup failure: the API server
// could not reach the sandbox pod's kubelet to write its git credentials.
const sessionTunnelFailure = `this run's sandbox could not be prepared: orchestrator: configuring git credentials for run 831-35: ` +
	`gitcredentials: writing remote .git-credentials and .gitconfig: ` +
	`error: unable to upgrade connection: error dialing backend: dial timeout, backstop`

// startSessionTurn has a person say something to the director session
// and starts the run dispatched for it, returning that run's id.
func startSessionTurn(t *testing.T, store *model.Store, taskID string, attempt int, at time.Time) string {
	t.Helper()
	id := taskID + "-" + string(rune('0'+attempt))
	if err := store.StartRun(t.Context(), model.Run{ID: id, TaskID: taskID, Attempt: attempt, StartedAt: at}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A turn whose sandbox could not be prepared because the cluster's exec
// tunnel failed is not answered with "say it again": while the setup
// retries are pending the session stays queued and says nothing, and once
// they are spent it says once why the answer is late -- and stays queued
// for grain's own next try rather than parking on the person.
func TestASessionTurnWhoseSetupHitTheExecTunnelStaysPending(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)

	// A quick retry: the run is suspended for a setup retry, as
	// orchestrator.finishSetupFailed records it.
	at := said.Add(time.Second)
	run := startSessionTurn(t, store, director.ID, 1, at)
	resume := at.Add(10 * time.Second)
	if err := store.Suspend(ctx, model.Suspension{RunID: run, Kind: model.SuspendSetup,
		Detail: sessionTunnelFailure + "; grain retries it automatically in 10s", ResumeAt: &resume, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	runSessions(t, store, at.Add(time.Second))
	if state, err := store.State(ctx, director.ID); err != nil || state != model.StateQueued {
		t.Fatalf("during the retries, state = %q (%v), want %q: the message is still pending", state, err, model.StateQueued)
	}
	for _, body := range commentBodies(t, ctx, store, director.ID) {
		if strings.Contains(body, "Say it again") || strings.Contains(body, "did not get an answer") {
			t.Fatalf("the session said %q while a setup retry was pending", body)
		}
	}

	// The retries are spent: the last try ended setup-failed.
	at = at.Add(time.Minute)
	run = startSessionTurn(t, store, director.ID, 2, at)
	if err := store.FinishRun(ctx, run, at.Add(time.Second), model.SetupFailedOutcome,
		sessionTunnelFailure+" (the cluster's exec tunnel is failing; 3 automatic retries did not get past it)"); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		runSessions(t, store, at.Add(time.Duration(i+2)*time.Second))
	}
	if state, err := store.State(ctx, director.ID); err != nil || state != model.StateQueued {
		t.Fatalf("after the retries, state = %q (%v), want %q: grain keeps trying, not the person", state, err, model.StateQueued)
	}
	notes := 0
	for _, body := range commentBodies(t, ctx, store, director.ID) {
		if strings.Contains(body, "Say it again") {
			t.Fatalf("the session told the person to say it again: %q", body)
		}
		if strings.Contains(body, "exec tunnel is failing") && strings.Contains(body, "retry automatically") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("the session noted the failing exec tunnel %d times over three cycles, want once", notes)
	}
}

// Any other setup failure is still the person's to resend, as before.
func TestASessionTurnWhoseSetupFailedOtherwiseStillSaysSo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	said := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, said)
	at := said.Add(time.Second)
	run := startSessionTurn(t, store, director.ID, 1, at)
	if err := store.FinishRun(ctx, run, at.Add(time.Second), model.SetupFailedOutcome,
		"this run's sandbox could not be prepared: no credential named \"gcp-key\""); err != nil {
		t.Fatal(err)
	}
	runSessions(t, store, at.Add(2*time.Second))
	if state, err := store.State(ctx, director.ID); err != nil || state != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want %q", state, err, model.StateAwaitingReply)
	}
	if last := lastComment(t, store, director.ID).Body; !strings.Contains(last, "Say it again") {
		t.Errorf("session parked on %q, want the failure note", last)
	}
}
