package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

func baseNow() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) }

// newPause is `&Pause{}` with this test's claim on the log taken first --
// Begin and Lift both say what they did through log.Printf, and every
// test here drives one or the other. See log_test.go.
func newPause(t *testing.T) *Pause {
	t.Helper()
	logToTest(t)
	return &Pause{}
}

// Scopes the tests below pause and run under: two teams on the same
// framework, each on a connection of its own, and alpha with a second
// connection on another key.
var (
	alphaKey   = PauseScope{Connection: "alpha-claude", Team: "alpha", Framework: "claude"}
	alphaOther = PauseScope{Connection: "alpha-claude-2", Team: "alpha", Framework: "claude"}
	betaKey    = PauseScope{Connection: "beta-claude", Team: "beta", Framework: "claude"}
)

func claudeLimit(reset time.Time) *agent.UsageLimitError {
	return &agent.UsageLimitError{Framework: "claude", ResetAt: reset}
}

func cancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// The whole point of the type: one run's limit stops every other run in
// flight on the same credential, rather than leaving each to spend an
// agent's worth of wall-clock time meeting the same refusal.
func TestBeginCancelsEveryLiveRunOnThatCredential(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	ctxA, cancelA := context.WithCancelCause(context.Background())
	defer cancelA(nil)
	ctxB, cancelB := context.WithCancelCause(context.Background())
	defer cancelB(nil)
	p.register("7-1", alphaKey, now, cancelA)
	p.register("8-1", alphaKey, now, cancelB)

	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))

	for name, ctx := range map[string]context.Context{"7-1": ctxA, "8-1": ctxB} {
		if !cancelled(ctx) {
			t.Errorf("run %s was left running through a usage-limit pause", name)
		} else if !errors.Is(context.Cause(ctx), errUsageLimit) {
			t.Errorf("run %s cause = %v, want errUsageLimit", name, context.Cause(ctx))
		}
	}
}

// And the point of scoping it by connection: one team exhausting its key
// is none of another team's business, even on the same framework, and
// none of the same team's other connection on another key either. The
// framework-wide pause this replaced cancelled both.
func TestBeginLeavesOtherCredentialsRunning(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	limited, cancelLimited := context.WithCancelCause(context.Background())
	defer cancelLimited(nil)
	otherTeam, cancelOtherTeam := context.WithCancelCause(context.Background())
	defer cancelOtherTeam(nil)
	otherKey, cancelOtherKey := context.WithCancelCause(context.Background())
	defer cancelOtherKey(nil)
	p.register("7-1", alphaKey, now, cancelLimited)
	p.register("8-1", betaKey, now, cancelOtherTeam)
	p.register("9-1", alphaOther, now, cancelOtherKey)

	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))

	if !cancelled(limited) {
		t.Error("the run on the connection that met the limit was left running")
	}
	if cancelled(otherTeam) {
		t.Errorf("team beta's claude run was cancelled by team alpha's limit: %v", context.Cause(otherTeam))
	}
	if cancelled(otherKey) {
		t.Errorf("alpha's run on another key was cancelled: %v", context.Cause(otherKey))
	}
	gated := p.Blocked(now)
	if !gated.Covers(alphaKey) || gated.Covers(betaKey) || gated.Covers(alphaOther) {
		t.Errorf("Blocked covers alpha %t, beta %t, alpha's other key %t; want only alpha's paused key",
			gated.Covers(alphaKey), gated.Covers(betaKey), gated.Covers(alphaOther))
	}
	if !p.ConnectionPaused("alpha-claude", "alpha", now) || p.ConnectionPaused("beta-claude", "beta", now) {
		t.Error("ConnectionPaused does not say exactly alpha-claude is paused")
	}
}

// A run on no connection -- the deployment-level credentials -- has
// nothing narrower than its team and framework to be paused by, and its
// pause reaches no further: not another team's runs on that framework,
// nor its own team's runs on connections, which spend other keys.
func TestBeginOnNoConnectionCoversItsTeamAndFrameworkOnly(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	legacy := PauseScope{Team: model.DefaultTeamID, Framework: "claude"}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	other, cancelOther := context.WithCancelCause(context.Background())
	defer cancelOther(nil)
	connected, cancelConnected := context.WithCancelCause(context.Background())
	defer cancelConnected(nil)
	p.register("7-1", PauseScope{Framework: "claude"}, now, cancel) // the default team, said as none
	p.register("8-1", PauseScope{Team: "beta", Framework: "claude"}, now, cancelOther)
	p.register("9-1", PauseScope{Connection: "default-claude", Team: model.DefaultTeamID, Framework: "claude"}, now, cancelConnected)

	p.Begin(now, legacy, claudeLimit(now.Add(time.Hour)))

	if !cancelled(ctx) {
		t.Error("the default team's run on the deployment credential was left running")
	}
	if cancelled(other) || cancelled(connected) {
		t.Error("a pause on the deployment credential cancelled a run spending another key")
	}
	gated := p.Blocked(now)
	if !gated.Covers(PauseScope{Framework: "claude"}) || gated.Covers(PauseScope{Framework: "codex"}) ||
		gated.Covers(PauseScope{Team: "beta", Framework: "claude"}) {
		t.Error("Blocked does not cover exactly the default team's claude runs on no connection")
	}
}

// A scope naming neither a connection nor a framework is the widest a
// pause gets: its whole team, and still no other.
func TestBeginWithNoFrameworkCoversOnlyItsTeam(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, PauseScope{Team: "alpha"}, &agent.UsageLimitError{ResetAt: now.Add(time.Hour)})

	gated := p.Blocked(now)
	if !gated.Covers(alphaKey) || !gated.Covers(PauseScope{Team: "alpha", Framework: "codex"}) {
		t.Error("a team-wide pause does not cover the team's runs")
	}
	if gated.Covers(betaKey) {
		t.Error("a team-wide pause covered another team")
	}
}

// A run that has already finished must not be cancelled by a pause its
// own limit began -- RunDispatch unregisters before it reports one, and
// this is the property that ordering exists for.
func TestUnregisteredRunsAreNotCancelled(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	stop := p.register("7-1", alphaKey, now, cancel)
	stop()
	p.Begin(now, alphaKey, &agent.UsageLimitError{})

	if cancelled(ctx) {
		t.Errorf("an unregistered run was cancelled: %v", context.Cause(ctx))
	}
}

// Dispatch is gated for as long as the provider said, and opens again on
// its own once a later tick asks after the instant has passed.
func TestBlockedHoldsDispatchUntilTheWindowResets(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	if p.Blocked(now).Any() {
		t.Fatal("a fresh Pause blocks dispatch")
	}

	until := p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	if want := now.Add(time.Hour); !until.Equal(want) {
		t.Errorf("Begin = %s, want the provider's own reset %s", until, want)
	}
	if gated := p.Blocked(now.Add(59 * time.Minute)); !gated.Covers(alphaKey) {
		t.Error("Blocked before the reset does not hold the connection")
	}
	if p.ConnectionPaused("alpha-claude", "alpha", now.Add(time.Hour)) {
		t.Error("ConnectionPaused at the reset instant still holds the connection")
	}
	if p.Blocked(now.Add(time.Hour)).Any() {
		t.Error("Blocked at the reset instant still holds dispatch")
	}
	if gotUntil, _ := p.Until(); !gotUntil.IsZero() {
		t.Errorf("Until = %s after the pause expired, want it cleared", gotUntil)
	}
}

// Two runs meeting the same limit within a second of each other is the
// ordinary case -- they were in flight together -- and the second must
// not be able to shorten the first's wait.
func TestBeginKeepsTheLaterReset(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, alphaKey, claudeLimit(now.Add(3*time.Hour)))
	got := p.Begin(now, alphaKey, claudeLimit(now.Add(2*time.Hour)))

	if want := now.Add(3 * time.Hour); !got.Equal(want) {
		t.Errorf("Begin = %s, want the longer wait %s kept", got, want)
	}
	if !p.Blocked(now.Add(150 * time.Minute)).Covers(alphaKey) {
		t.Error("the shorter second limit shortened the pause")
	}
}

func TestBeginExtendsToALaterReset(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	got := p.Begin(now, alphaKey, claudeLimit(now.Add(4*time.Hour)))

	if want := now.Add(4 * time.Hour); !got.Equal(want) {
		t.Errorf("Begin = %s, want the pause extended to %s", got, want)
	}
}

// Two credentials refusing at once are two windows, neither of which
// says anything about the other's reset -- so one lifting must not lift
// the other, and the last-to-lift answer (Until) is the last of them.
func TestTwoCredentialsKeepTheirOwnWindows(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	p.Begin(now, betaKey, claudeLimit(now.Add(4*time.Hour)))

	gated := p.Blocked(now.Add(2 * time.Hour))
	if gated.Covers(alphaKey) {
		t.Error("alpha's window did not lift on its own hour")
	}
	if !gated.Covers(betaKey) {
		t.Error("alpha's window lifting took beta's with it")
	}
	if until, reason := p.Until(); !until.Equal(now.Add(4*time.Hour)) || reason == "" {
		t.Errorf("Until = %s, %q; want the last window to lift and its own reason", until, reason)
	}
	got := p.Pauses(now.Add(2 * time.Hour))
	if len(got) != 1 || got[0].Connection != "beta-claude" || got[0].Team != "beta" {
		t.Errorf("Pauses = %+v, want only beta's connection still paused", got)
	}
}

// Pauses is what the operator's banner reads, so it must name each
// pause's connection and team, and nothing about a window that has
// passed.
func TestPausesReportsLiveWindowsWithTheirConnectionAndTeam(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	if got := p.Pauses(now); len(got) != 0 {
		t.Errorf("Pauses on a fresh Pause = %v", got)
	}
	p.Begin(now, betaKey, claudeLimit(now.Add(time.Hour)))
	p.Begin(now, PauseScope{Framework: model.LegacyAgentFrameworkGemini}, &agent.UsageLimitError{ResetAt: now.Add(2 * time.Hour)})

	got := p.Pauses(now)
	if len(got) != 2 {
		t.Fatalf("Pauses = %+v, want two", got)
	}
	if got[0].Team != "beta" || got[0].Connection != "beta-claude" || got[0].Framework != "claude" ||
		!got[0].Until.Equal(now.Add(time.Hour)) || got[0].Reason == "" {
		t.Errorf("Pauses[0] = %+v, want beta's connection and its window", got[0])
	}
	if got[1].Team != model.DefaultTeamID || got[1].Connection != "" || got[1].Framework != model.AgentFrameworkAntigravity {
		t.Errorf("Pauses[1] = %+v, want the default team's antigravity runs on no connection", got[1])
	}
	if got := p.Pauses(now.Add(3 * time.Hour)); len(got) != 0 {
		t.Errorf("Pauses = %v once every window has passed", got)
	}
}

// A run dispatched by the cycle that was already in flight when the
// pause began has nowhere else to be caught -- but only if the pause
// covers what it is spending.
func TestRegisterCancelsARunStartingIntoAPause(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	p.register("9-1", alphaKey, now.Add(time.Minute), cancel)

	if !cancelled(ctx) {
		t.Error("a run registered during a pause was left running")
	} else if !errors.Is(context.Cause(ctx), errUsageLimit) {
		t.Errorf("cause = %v, want errUsageLimit", context.Cause(ctx))
	}

	other, cancelOther := context.WithCancelCause(context.Background())
	defer cancelOther(nil)
	p.register("9-2", betaKey, now.Add(time.Minute), cancelOther)
	if cancelled(other) {
		t.Errorf("beta's run registered during alpha's pause was cancelled: %v", context.Cause(other))
	}
}

// connectionFramework is an agent.Framework that says which connection's
// credential it spends, as the daemon's pooled frameworks do.
type connectionFramework struct {
	agent.Framework
	connection, team string
}

func (f connectionFramework) CredentialConnection() (string, string) { return f.connection, f.team }

func TestResumeAtBoundsWhatTheProviderSaid(t *testing.T) {
	t.Parallel()
	now := baseNow()
	for _, tc := range []struct {
		name  string
		limit *agent.UsageLimitError
		want  time.Duration
	}{
		{"no reset at all", &agent.UsageLimitError{}, defaultUsageLimitPause},
		{"nil limit", nil, defaultUsageLimitPause},
		{"a delay of seconds", &agent.UsageLimitError{RetryAfter: 5 * time.Second}, minUsageLimitPause},
		{"a reset in the past", &agent.UsageLimitError{ResetAt: now.Add(-time.Hour)}, defaultUsageLimitPause},
		{"an absurd reset", &agent.UsageLimitError{ResetAt: now.Add(4000 * time.Hour)}, maxUsageLimitPause},
		{"an honest reset", &agent.UsageLimitError{ResetAt: now.Add(2 * time.Hour)}, 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resumeAt(now, tc.limit)
			if want := now.Add(tc.want); !got.Equal(want) {
				t.Errorf("resumeAt = %s, want %s", got, want)
			}
		})
	}
}

// Every method has to tolerate the nil a caller with no loop to pause
// wires (Config.Pause), since RunDispatch calls all of them unguarded.
func TestNilPauseIsUsable(t *testing.T) {
	t.Parallel()
	var p *Pause
	stop := p.register("7-1", alphaKey, baseNow(), func(error) { t.Error("a nil Pause cancelled a run") })
	stop()
	if until := p.Begin(baseNow(), alphaKey, &agent.UsageLimitError{}); !until.IsZero() {
		t.Errorf("Begin on a nil Pause = %s, want the zero time", until)
	}
	if p.Blocked(baseNow()).Any() {
		t.Error("a nil Pause blocked dispatch")
	}
	if until, _ := p.Until(); !until.IsZero() {
		t.Errorf("Until on a nil Pause = %s", until)
	}
	if got := p.Pauses(baseNow()); len(got) != 0 {
		t.Errorf("Pauses on a nil Pause = %v", got)
	}
	if p.ConnectionPaused("alpha-claude", "alpha", baseNow()) {
		t.Error("a nil Pause paused a connection")
	}
	if got := p.LiftWhere(func(model.UsageLimitPause) bool { return true }); len(got) != 0 {
		t.Errorf("LiftWhere on a nil Pause = %v", got)
	}
	if p.Lift() {
		t.Error("Lift on a nil Pause reported it had lifted something")
	}
}

// The system admin's override: someone who has topped a plan up opens
// the gate rather than waiting out a window that no longer applies
// (DELETE /api/pause, grain/task-132). Lift is "lift all", so every
// credential's window goes.
func TestLiftOpensTheGateBeforeTheWindowResets(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	if p.Lift() {
		t.Error("Lift on a fresh Pause reported it had lifted something")
	}

	p.Begin(now, alphaKey, claudeLimit(now.Add(5*time.Hour)))
	p.Begin(now, betaKey, claudeLimit(now.Add(2*time.Hour)))
	if !p.Lift() {
		t.Fatal("Lift did not report lifting a pause that was in force")
	}
	if p.Blocked(now).Any() {
		t.Error("dispatch is still held after a lift")
	}
	if until, reason := p.Until(); !until.IsZero() || reason != "" {
		t.Errorf("Until after a lift = %s, %q; want nothing paused", until, reason)
	}

	// A lift is not a promise the limit is over. If it is not, the next
	// run meets the same refusal and shuts the gate again -- the same
	// self-correcting shape as a window that expired without resetting.
	p.Begin(now, alphaKey, claudeLimit(now.Add(5*time.Hour)))
	if !p.Blocked(now).Covers(alphaKey) {
		t.Error("a limit met again after a lift did not pause dispatch")
	}
}

// A lift ends the gate, not the runs the pause already cancelled: those
// are over and recorded as model.PausedOutcome. What it buys is the next
// tick dispatching, which is what a run registering after one proves --
// register cancels on the spot only while a pause is in force.
func TestLiftLetsTheNextRunStart(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)
	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	p.Lift()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := p.register("7-2", alphaKey, now, cancel)
	defer stop()

	select {
	case <-ctx.Done():
		t.Errorf("a run dispatched after a lift was cancelled: %v", context.Cause(ctx))
	default:
	}
}

// A team admin's lift: one connection's pause goes, and every other
// credential's -- another team's above all -- stays exactly as it was.
func TestLiftWhereLiftsOnlyWhatItMatches(t *testing.T) {
	t.Parallel()
	now := baseNow()
	p := newPause(t)

	p.Begin(now, alphaKey, claudeLimit(now.Add(time.Hour)))
	p.Begin(now, betaKey, claudeLimit(now.Add(time.Hour)))

	lifted := p.LiftWhere(func(pause model.UsageLimitPause) bool { return pause.Connection == "alpha-claude" })
	if len(lifted) != 1 || lifted[0].Connection != "alpha-claude" || lifted[0].Team != "alpha" {
		t.Fatalf("LiftWhere = %+v, want alpha's connection alone", lifted)
	}
	gated := p.Blocked(now)
	if gated.Covers(alphaKey) {
		t.Error("alpha's connection is still held after its lift")
	}
	if !gated.Covers(betaKey) {
		t.Error("lifting alpha's pause lifted beta's")
	}
}

// Which framework a run is charged to: its task's own override where it
// made one, and the deployment's default where it did not -- including
// the legacy spelling of a framework that has since been renamed, which
// must fold onto the name a pause is actually keyed under.
func TestFrameworkForResolvesTheTasksOwnChoiceFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		task, deployment string
		want             string
	}{
		{"the task's own", "codex", "claude", "codex"},
		{"the deployment's", "", "claude", "claude"},
		{"neither", "", "", model.AgentFrameworkAntigravity},
		{"the legacy spelling on a task", model.LegacyAgentFrameworkGemini, "claude", model.AgentFrameworkAntigravity},
		{"the legacy spelling on a deployment", "", model.LegacyAgentFrameworkGemini, model.AgentFrameworkAntigravity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := frameworkFor(model.Task{AgentFramework: tc.task}, Config{AgentFramework: tc.deployment})
			if got != tc.want {
				t.Errorf("frameworkFor = %q, want %q", got, tc.want)
			}
		})
	}
}
