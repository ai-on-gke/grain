package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
)

// restartedPause is a daemon coming back up on st: a fresh Pause, with
// nothing in memory, restored from the store at now.
func restartedPause(t *testing.T, st PauseStore, now time.Time) (*Pause, int) {
	t.Helper()
	p := newPause(t)
	n, err := p.Restore(context.Background(), st, now)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return p, n
}

func storedPauses(t *testing.T, st *model.Store) []model.StoredUsageLimitPause {
	t.Helper()
	rows, err := st.ListUsageLimitPauses(context.Background())
	if err != nil {
		t.Fatalf("ListUsageLimitPauses: %v", err)
	}
	return rows
}

// The incident in grain/task-1259: a connection met its limit, the daemon
// restarted inside the window, and the pool dispatched onto it again.
func TestAPauseSurvivesARestartUntilItExpires(t *testing.T) {
	st := openTestStoreForOrchestrator(t)
	now := baseNow()
	reset := now.Add(6 * time.Hour)

	before, _ := restartedPause(t, st, now)
	scope := alphaKey
	scope.Model = "gemini-3-pro"
	before.Begin(now, scope, &agent.UsageLimitError{Framework: "antigravity", ResetAt: reset, Model: "gemini-3-pro"})
	if rows := storedPauses(t, st); len(rows) != 1 || !rows[0].Pause.Until.Equal(reset) {
		t.Fatalf("stored pauses after Begin = %+v, want one until %s", rows, reset)
	}

	later := now.Add(5 * time.Minute)
	after, n := restartedPause(t, st, later)
	if n != 1 {
		t.Fatalf("Restore restored %d pauses, want 1", n)
	}
	if !after.ModelPaused(alphaKey.Connection, "gemini-3-pro", alphaKey.Team, later) {
		t.Fatal("the restarted daemon would dispatch gemini-3-pro onto the connection whose quota is spent")
	}
	if after.ModelPaused(alphaKey.Connection, "claude-opus-5-5", alphaKey.Team, later) {
		t.Fatal("a model-scoped pause came back covering the connection's other models")
	}
	if after.ModelPaused(alphaOther.Connection, "gemini-3-pro", alphaOther.Team, later) {
		t.Fatal("a restored pause covers another connection")
	}
	got := after.Pauses(later)
	if len(got) != 1 || got[0].Model != "gemini-3-pro" || got[0].Team != "alpha" || !got[0].Until.Equal(reset) {
		t.Fatalf("restored Pauses = %+v", got)
	}

	// Past the reset the connection dispatches again, and the reconcile
	// loop's read clears the row.
	expired := reset.Add(time.Second)
	if after.ModelPaused(alphaKey.Connection, "gemini-3-pro", alphaKey.Team, expired) {
		t.Fatal("still paused after the window reset")
	}
	if after.Blocked(expired).Any() {
		t.Fatal("Blocked reports an expired window")
	}
	if rows := storedPauses(t, st); len(rows) != 0 {
		t.Fatalf("stored pauses after expiry = %+v, want none", rows)
	}
}

func TestARestoredPauseKeepsItsLabelsAndEscalation(t *testing.T) {
	st := openTestStoreForOrchestrator(t)
	now := baseNow()
	before, _ := restartedPause(t, st, now)
	auto := alphaKey
	auto.Model = "auto"
	// An "auto" run refused on gemini-3-pro holds gemini-3-pro, labelled auto.
	before.Begin(now, auto, &agent.UsageLimitError{Framework: "antigravity", ResetAt: now.Add(time.Hour), Model: "gemini-3-pro"})
	account := betaKey
	account.Model = "claude-opus-5-5"
	before.Begin(now, account, &agent.UsageLimitError{Framework: "claude", ResetAt: now.Add(2 * time.Hour), Scope: agent.LimitScopeAccount})

	after, n := restartedPause(t, st, now)
	if n != 2 {
		t.Fatalf("Restore restored %d pauses, want 2", n)
	}
	if !after.ModelPaused(alphaKey.Connection, "auto", alphaKey.Team, now) {
		t.Fatal("the restored pause lost the auto label it covers")
	}
	if !after.ConnectionPaused(betaKey.Connection, betaKey.Team, now) {
		t.Fatal("the restored account-wide pause no longer covers its whole connection")
	}
	want, got := before.Pauses(now), after.Pauses(now)
	if len(got) != len(want) {
		t.Fatalf("restored %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Escalated != want[i].Escalated || got[i].Reason != want[i].Reason || len(got[i].Labels) != len(want[i].Labels) {
			t.Fatalf("restored pause %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAnExpiredPauseIsNotRestored(t *testing.T) {
	st := openTestStoreForOrchestrator(t)
	now := baseNow()
	before, _ := restartedPause(t, st, now)
	before.Begin(now, alphaKey, claudeLimit(now.Add(10*time.Minute)))
	before.Begin(now, betaKey, claudeLimit(now.Add(3*time.Hour)))

	// The daemon was down when alpha's window reset, so nothing deleted
	// its row; the next one must neither restore it nor keep it.
	later := now.Add(time.Hour)
	after, n := restartedPause(t, st, later)
	if n != 1 {
		t.Fatalf("Restore restored %d pauses, want 1 (beta's)", n)
	}
	if after.ConnectionPaused(alphaKey.Connection, alphaKey.Team, later) {
		t.Fatal("a window that reset while the daemon was down was restored")
	}
	if !after.ConnectionPaused(betaKey.Connection, betaKey.Team, later) {
		t.Fatal("the window still open was not restored")
	}
	rows := storedPauses(t, st)
	if len(rows) != 1 || rows[0].Pause.Connection != betaKey.Connection {
		t.Fatalf("stored pauses after Restore = %+v, want beta's alone", rows)
	}
}

func TestLiftingAPauseDeletesItsRow(t *testing.T) {
	st := openTestStoreForOrchestrator(t)
	now := baseNow()
	p, _ := restartedPause(t, st, now)
	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	p.Begin(now, betaKey, claudeLimit(now.Add(time.Hour)))

	lifted := p.LiftWhere(func(v model.UsageLimitPause) bool { return v.Connection == alphaKey.Connection })
	if len(lifted) != 1 {
		t.Fatalf("LiftWhere lifted %+v, want alpha's", lifted)
	}
	if rows := storedPauses(t, st); len(rows) != 1 || rows[0].Pause.Connection != betaKey.Connection {
		t.Fatalf("stored pauses after lifting alpha = %+v, want beta's alone", rows)
	}
	if !p.Lift() {
		t.Fatal("Lift found nothing to lift")
	}
	if rows := storedPauses(t, st); len(rows) != 0 {
		t.Fatalf("stored pauses after Lift = %+v, want none", rows)
	}
	if after, n := restartedPause(t, st, now); n != 0 || after.ConnectionPaused(betaKey.Connection, betaKey.Team, now) {
		t.Fatal("a lifted pause came back after a restart")
	}
}

// A pause the gate opened before Restore attached the store -- the model
// proxy can meet a limit while the daemon is still starting -- is kept,
// and written, rather than lost.
func TestRestoreKeepsAndRecordsAPauseOpenedBeforeIt(t *testing.T) {
	st := openTestStoreForOrchestrator(t)
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	if _, err := p.Restore(context.Background(), st, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !p.ConnectionPaused(alphaKey.Connection, alphaKey.Team, now) {
		t.Fatal("Restore dropped a pause already in memory")
	}
	if rows := storedPauses(t, st); len(rows) != 1 {
		t.Fatalf("stored pauses = %+v, want the one opened before Restore", rows)
	}
}

// With no store attached the gate is what it always was.
func TestAPauseWithNoStoreStillGates(t *testing.T) {
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	if !p.ConnectionPaused(alphaKey.Connection, alphaKey.Team, now) {
		t.Fatal("not paused")
	}
	if !p.Lift() {
		t.Fatal("nothing lifted")
	}
}
