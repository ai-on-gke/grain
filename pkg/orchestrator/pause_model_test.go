package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Two models on alpha's one connection, and the same connection with no
// model grain could name.
var (
	alphaPro   = PauseScope{Connection: "alpha-gemini", Model: "gemini-3-pro", Team: "alpha", Framework: "antigravity"}
	alphaFlash = PauseScope{Connection: "alpha-gemini", Model: "gemini-3.8-flash-high", Team: "alpha", Framework: "antigravity"}
	alphaAuto  = PauseScope{Connection: "alpha-gemini", Model: "auto", Team: "alpha", Framework: "antigravity"}
	alphaAny   = PauseScope{Connection: "alpha-gemini", Team: "alpha", Framework: "antigravity"}
)

func geminiLimit(reset time.Time) *agent.UsageLimitError {
	return &agent.UsageLimitError{Framework: "antigravity", ResetAt: reset}
}

// The point of the change: one model's limit leaves the connection's
// other models dispatchable, and running.
func TestModelLimitLeavesTheConnectionsOtherModelsRunning(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	proCtx, cancelPro := context.WithCancelCause(context.Background())
	defer cancelPro(nil)
	flashCtx, cancelFlash := context.WithCancelCause(context.Background())
	defer cancelFlash(nil)
	p.register("1-1", alphaPro, now, cancelPro)
	p.register("2-1", alphaFlash, now, cancelFlash)

	held := p.BeginWindow(now, alphaPro, geminiLimit(now.Add(time.Hour)))

	if !held.ModelScoped() || held.Model != "gemini-3-pro" || held.Escalated != "" {
		t.Fatalf("held = %+v, want a pause on gemini-3-pro alone", held)
	}
	if !cancelled(proCtx) {
		t.Error("the run on the limited model was left running")
	}
	if cancelled(flashCtx) {
		t.Error("a run on another model of the same connection was cancelled")
	}
	if !p.ModelPaused("alpha-gemini", "gemini-3-pro", "alpha", now) {
		t.Error("ModelPaused(gemini-3-pro) = false, want the limited model paused")
	}
	if p.ModelPaused("alpha-gemini", "gemini-3.8-flash-high", "alpha", now) {
		t.Error("ModelPaused(flash) = true, want the connection's other model dispatchable")
	}
	if p.ConnectionPaused("alpha-gemini", "alpha", now) {
		t.Error("ConnectionPaused = true for a pause on one model")
	}
	// A model name is one quota however it is spelled.
	if !p.ModelPaused("alpha-gemini", " Gemini-3-Pro ", "alpha", now) {
		t.Error("ModelPaused is case- or space-sensitive")
	}
	// A run whose model grain cannot name is held only by a pause on the
	// whole connection.
	if p.ModelPaused("alpha-gemini", "", "alpha", now) {
		t.Error("a run of unknown model is held by one model's pause")
	}
	// And a new run on the limited model, starting into the pause, is
	// stopped at once while one on the other model is not.
	lateCtx, cancelLate := context.WithCancelCause(context.Background())
	defer cancelLate(nil)
	p.register("3-1", alphaPro, now, cancelLate)
	if !cancelled(lateCtx) {
		t.Error("a run starting on the paused model was not cancelled")
	}
	otherCtx, cancelOther := context.WithCancelCause(context.Background())
	defer cancelOther(nil)
	p.register("4-1", alphaFlash, now, cancelOther)
	if cancelled(otherCtx) {
		t.Error("a run starting on another model was cancelled")
	}
}

// A refusal the provider says is the account's, not the model's, pauses
// the whole connection and cancels everything on it.
func TestAccountWideLimitEscalatesToTheConnection(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	flashCtx, cancelFlash := context.WithCancelCause(context.Background())
	defer cancelFlash(nil)
	p.register("2-1", alphaFlash, now, cancelFlash)

	limit := geminiLimit(now.Add(time.Hour))
	limit.Scope = agent.LimitScopeAccount
	held := p.BeginWindow(now, alphaPro, limit)

	if held.Model != "" || held.Connection != "alpha-gemini" || !strings.Contains(held.Escalated, "account-wide") {
		t.Fatalf("held = %+v, want the whole connection, escalated as account-wide", held)
	}
	if !cancelled(flashCtx) {
		t.Error("a run on another model survived an account-wide refusal")
	}
	if !p.ConnectionPaused("alpha-gemini", "alpha", now) || !p.ModelPaused("alpha-gemini", "gemini-3.8-flash-high", "alpha", now) {
		t.Error("the connection's other model is still dispatchable after an account-wide refusal")
	}
	if p.ModelPaused("alpha-claude", "claude-opus-4", "alpha", now) {
		t.Error("an escalated pause reached another connection")
	}
}

// Two different models of one key spent at once is read as the key
// being spent.
func TestASecondModelsLimitEscalatesToTheConnection(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, alphaPro, geminiLimit(now.Add(time.Hour)))
	// The same model again is not a second model.
	again := p.BeginWindow(now.Add(time.Minute), alphaPro, geminiLimit(now.Add(2*time.Hour)))
	if again.Model != "gemini-3-pro" || p.ConnectionPaused("alpha-gemini", "alpha", now) {
		t.Fatalf("a repeat of the same model's limit = %+v, connection paused %t; want the model's own window extended",
			again, p.ConnectionPaused("alpha-gemini", "alpha", now))
	}

	autoCtx, cancelAuto := context.WithCancelCause(context.Background())
	defer cancelAuto(nil)
	p.register("5-1", alphaAuto, now, cancelAuto)

	held := p.BeginWindow(now.Add(2*time.Minute), alphaFlash, geminiLimit(now.Add(30*time.Minute)))
	if held.Model != "" || !strings.Contains(held.Escalated, "second model") {
		t.Fatalf("held = %+v, want the whole connection, escalated by a second model", held)
	}
	if !cancelled(autoCtx) {
		t.Error("a run on a third model survived the escalation")
	}
	// The first model's window stays beside the escalated one, which
	// took the second model's place: lifting the connection's leaves
	// gemini-3-pro to run out on its own.
	pauses := p.Pauses(now.Add(3 * time.Minute))
	if len(pauses) != 2 || pauses[0].Model != "" || pauses[1].Model != "gemini-3-pro" {
		t.Fatalf("Pauses = %+v, want the escalated pause and gemini-3-pro's", pauses)
	}
	p.LiftWhere(func(v model.UsageLimitPause) bool { return v.Connection == "alpha-gemini" && v.Model == "" })
	at := now.Add(3 * time.Minute)
	if p.ConnectionPaused("alpha-gemini", "alpha", at) {
		t.Error("the escalated pause survived being lifted")
	}
	if !p.ModelPaused("alpha-gemini", "gemini-3-pro", "alpha", at) {
		t.Error("lifting the escalated pause lifted the first model's own")
	}
	if p.ModelPaused("alpha-gemini", "auto", "alpha", at) {
		t.Error("a model that met no limit is still paused once the escalation lifted")
	}
}

// A model's pause that has expired is no evidence of anything: a limit on
// another model afterwards is that model's alone.
func TestAnExpiredModelPauseDoesNotEscalate(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaPro, geminiLimit(now.Add(10*time.Minute)))
	later := now.Add(20 * time.Minute)
	held := p.BeginWindow(later, alphaFlash, geminiLimit(later.Add(time.Hour)))
	if held.Model != "gemini-3.8-flash-high" || held.Escalated != "" {
		t.Fatalf("held = %+v, want flash's own pause", held)
	}
}

// Lifting one model's pause by hand leaves another's -- and another
// connection's -- in force.
func TestLiftWhereLiftsOneModel(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaPro, geminiLimit(now.Add(time.Hour)))
	other := PauseScope{Connection: "alpha-gemini-2", Model: "gemini-3.8-flash-high", Team: "alpha"}
	p.Begin(now, other, geminiLimit(now.Add(time.Hour)))

	lifted := p.LiftWhere(func(v model.UsageLimitPause) bool {
		return v.Connection == "alpha-gemini" && v.Model == "gemini-3-pro"
	})
	if len(lifted) != 1 || lifted[0].Model != "gemini-3-pro" {
		t.Fatalf("lifted = %+v, want gemini-3-pro's pause alone", lifted)
	}
	if p.ModelPaused("alpha-gemini", "gemini-3-pro", "alpha", now) {
		t.Error("the lifted model is still paused")
	}
	if !p.ModelPaused("alpha-gemini-2", "gemini-3.8-flash-high", "alpha", now) {
		t.Error("lifting one model lifted another connection's")
	}
}

// "auto" runs are keyed on the label grain passed, unless the provider's
// error named the concrete model -- in which case that model's window
// holds the label too, so the next "auto" run is not sent into it.
func TestAutoRunsArePausedOnTheModelTheProviderNamed(t *testing.T) {
	t.Parallel()
	now := baseNow()

	bare := newPause(t)
	held := bare.BeginWindow(now, alphaAuto, geminiLimit(now.Add(time.Hour)))
	if held.Model != "auto" {
		t.Errorf("an auto run's limit naming no model = %+v, want it keyed on auto", held)
	}

	named := &Pause{}
	limit := geminiLimit(now.Add(time.Hour))
	limit.Model = "Gemini-3-Pro"
	held = named.BeginWindow(now, alphaAuto, limit)
	if held.Model != "gemini-3-pro" || len(held.Labels) != 1 || held.Labels[0] != "auto" {
		t.Fatalf("held = %+v, want gemini-3-pro holding the auto label", held)
	}
	for _, m := range []string{"gemini-3-pro", "auto"} {
		if !named.ModelPaused("alpha-gemini", m, "alpha", now) {
			t.Errorf("ModelPaused(%s) = false, want it held", m)
		}
	}
	if named.ModelPaused("alpha-gemini", "gemini-3.8-flash-high", "alpha", now) {
		t.Error("another model is held by auto's pause")
	}
	// gemini-3-pro met again by a run that chose it is the same quota,
	// not a second model.
	again := named.BeginWindow(now, alphaPro, geminiLimit(now.Add(time.Hour)))
	if again.Model != "gemini-3-pro" || again.Escalated != "" {
		t.Errorf("the same concrete model again = %+v, want no escalation", again)
	}
}

// Runs whose model grain cannot name, and runs on no connection, keep the
// scoping they had: the whole connection, and the team's framework.
func TestUnknownModelAndNoConnectionKeepTheirScope(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	held := p.BeginWindow(now, alphaAny, geminiLimit(now.Add(time.Hour)))
	if held.Model != "" || held.Escalated != "" {
		t.Fatalf("held = %+v, want the whole connection, not escalated", held)
	}
	for _, m := range []string{"", "gemini-3-pro", "auto"} {
		if !p.ModelPaused("alpha-gemini", m, "alpha", now) {
			t.Errorf("ModelPaused(%q) = false under a pause on the whole connection", m)
		}
	}

	team := newPause(t)
	limit := geminiLimit(now.Add(time.Hour))
	limit.Model = "gemini-3-pro"
	held = team.BeginWindow(now, PauseScope{Model: "gemini-3-pro", Team: "alpha", Framework: "antigravity"}, limit)
	if held.Connection != "" || held.Model != "" || held.Framework != "antigravity" {
		t.Fatalf("held = %+v, want alpha's antigravity runs on no connection", held)
	}
	if !team.Blocked(now).Covers(PauseScope{Team: "alpha", Framework: "antigravity"}) {
		t.Error("a pause on no connection no longer covers its team's framework")
	}
}

// What a run on a connection is registered and paused under: the model
// the framework says it passes the CLI, where it says one.
func TestRunPauseScopeCarriesTheRunsModel(t *testing.T) {
	t.Parallel()
	task := model.Task{AgentFramework: "antigravity"}
	fw := modelFramework{connectionFramework: connectionFramework{connection: "alpha-gemini", team: "alpha"}, model: "Gemini-3-Pro"}
	if got := runPauseScope(fw, task, Config{}); got != alphaPro {
		t.Errorf("runPauseScope = %+v, want %+v", got, alphaPro)
	}
}

// The model a run is recorded as running on (Store.SetRunModel): the
// connection's own, where the framework names one, since that is what a
// rotation or a default list actually settled on, and the task's pin
// where it does not.
func TestRunModelForPrefersTheConnectionsModel(t *testing.T) {
	t.Parallel()
	task := model.Task{Model: "gemini-3.1-pro"}
	fw := modelFramework{connectionFramework: connectionFramework{connection: "alpha-gemini", team: "alpha"}, model: "gemini-3.1-flash"}
	if got := runModelFor(fw, task); got != "gemini-3.1-flash" {
		t.Errorf("runModelFor(connection) = %q, want the connection's model", got)
	}
	if got := runModelFor(fw.connectionFramework, task); got != "gemini-3.1-pro" {
		t.Errorf("runModelFor(no model) = %q, want the task's pin", got)
	}
	if got := runModelFor(modelFramework{}, model.Task{}); got != "" {
		t.Errorf("runModelFor(nothing) = %q, want empty", got)
	}
}

// modelFramework is a connectionFramework that also names the model it
// runs, as daemon.BackendFramework does.
type modelFramework struct {
	connectionFramework
	model string
}

func (f modelFramework) CredentialModel() string { return f.model }

// agy names its reset as a delay ("Your quota will reset after
// 2h28m27s"), which reaches here as RetryAfter. The model's pause must
// last that whole window -- not the 15-minute default, whose lifting
// would rotate the next run straight back onto the exhausted model --
// while the connection's other model stays dispatchable throughout.
func TestAModelPauseLastsTheResetAgyNamed(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	window := 2*time.Hour + 28*time.Minute + 27*time.Second

	p.BeginWindow(now, alphaPro, &agent.UsageLimitError{Framework: "antigravity", RetryAfter: window})

	until, paused := p.ModelPausedUntil("alpha-gemini", "gemini-3-pro", "alpha", now)
	if !paused || !until.Equal(now.Add(window)) {
		t.Fatalf("ModelPausedUntil = %s, %t; want %s, the reset agy named", until, paused, now.Add(window))
	}
	for _, at := range []time.Duration{defaultUsageLimitPause + time.Minute, window - time.Second} {
		if !p.ModelPaused("alpha-gemini", "gemini-3-pro", "alpha", now.Add(at)) {
			t.Errorf("gemini-3-pro dispatchable %s in, want it paused until its quota resets", at)
		}
		if p.ModelPaused("alpha-gemini", "gemini-3.8-flash-high", "alpha", now.Add(at)) {
			t.Errorf("gemini-3.8-flash-high paused %s in, want the other model dispatchable", at)
		}
	}
	if p.ModelPaused("alpha-gemini", "gemini-3-pro", "alpha", now.Add(window+time.Second)) {
		t.Error("gemini-3-pro still paused after the reset agy named")
	}
}
