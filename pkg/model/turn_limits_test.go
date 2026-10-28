// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
