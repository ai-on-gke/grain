package orchestrator

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestObserveFastFailureReportsTheThirdIdenticalFailure(t *testing.T) {
	var p Pause
	now := time.Date(2026, 10, 2, 17, 15, 0, 0, time.UTC)
	const text = "claude: You've hit a limit nobody has a marker for"
	if burst := p.observeFastFailure(now, alphaKey, "r1", text); burst != nil {
		t.Fatalf("first failure reported burst %v", burst)
	}
	if burst := p.observeFastFailure(now.Add(time.Minute), alphaKey, "r2", text); burst != nil {
		t.Fatalf("second failure reported burst %v", burst)
	}
	burst := p.observeFastFailure(now.Add(2*time.Minute), alphaKey, "r3", text)
	if len(burst) != 3 || burst[0] != "r1" || burst[2] != "r3" {
		t.Fatalf("third failure reported %v, want [r1 r2 r3]", burst)
	}
	// Reported once: the count starts again.
	if burst := p.observeFastFailure(now.Add(3*time.Minute), alphaKey, "r4", text); burst != nil {
		t.Errorf("failure after a reported burst reported %v", burst)
	}
}

func TestObserveFastFailureNeedsTheSameWordsCredentialAndWindow(t *testing.T) {
	var p Pause
	now := time.Date(2026, 10, 2, 17, 15, 0, 0, time.UTC)
	p.observeFastFailure(now, alphaKey, "r1", "one thing")
	p.observeFastFailure(now, alphaKey, "r2", "another thing")
	p.observeFastFailure(now, betaKey, "r3", "one thing")
	if burst := p.observeFastFailure(now, alphaKey, "r4", "yet another"); burst != nil {
		t.Errorf("different words reported burst %v", burst)
	}
	// Two in the window, the first aged out by the third.
	p.observeFastFailure(now, alphaKey, "r5", "one thing")
	if burst := p.observeFastFailure(now.Add(refusalBurstWindow), alphaKey, "r6", "one thing"); burst != nil {
		t.Errorf("a failure outside the window counted towards burst %v", burst)
	}
	if burst := p.observeFastFailure(now, alphaKey, "r7", ""); burst != nil {
		t.Errorf("empty text reported burst %v", burst)
	}
	var nilPause *Pause
	if burst := nilPause.observeFastFailure(now, alphaKey, "r8", "one thing"); burst != nil {
		t.Errorf("nil Pause reported burst %v", burst)
	}
}

func TestIsFastFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *agent.Result
		ran    time.Duration
		want   bool
	}{
		{"no result, quick", nil, 20 * time.Second, true},
		{"empty result, quick", &agent.Result{}, 0, true},
		{"slow", nil, refusalBurstFastRun, false},
		{"worked first", &agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command"}}}, time.Second, false},
	} {
		if got := isFastFailure(tc.result, tc.ran); got != tc.want {
			t.Errorf("%s: isFastFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}
