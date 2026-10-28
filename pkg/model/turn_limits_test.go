package model

import (
	"testing"
	"time"
)

func TestParseModelTurnLimitHoldsItsBounds(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"":     0,
		" ":    0,
		"30s":  30 * time.Second,
		"20m":  20 * time.Minute,
		"60m":  time.Hour,
		"1h":   time.Hour,
		"100s": 100 * time.Second,
	} {
		got, err := ParseModelTurnLimit(value)
		if err != nil || got != want {
			t.Errorf("ParseModelTurnLimit(%q) = %s, %v; want %s", value, got, err, want)
		}
	}
	for _, value := range []string{"29s", "61m", "2h", "0s", "-5m", "fifteen minutes", "15"} {
		if got, err := ParseModelTurnLimit(value); err == nil {
			t.Errorf("ParseModelTurnLimit(%q) = %s, want it refused", value, got)
		}
	}
}

func TestAgentBackendTurnLimitsDefaultWhatIsUnsetOrInvalid(t *testing.T) {
	b := AgentBackend{ModelTurnTimeout: "25m", ModelRetryCutoff: "2m"}
	if timeout, cutoff := b.TurnLimits(); timeout != 25*time.Minute || cutoff != 2*time.Minute {
		t.Errorf("TurnLimits = %s, %s; want 25m, 2m", timeout, cutoff)
	}
	b = AgentBackend{ModelTurnTimeout: "3h", ModelRetryCutoff: "soon"}
	if timeout, cutoff := b.TurnLimits(); timeout != 0 || cutoff != 0 {
		t.Errorf("TurnLimits of out-of-range values = %s, %s; want the defaults (zero)", timeout, cutoff)
	}
}
