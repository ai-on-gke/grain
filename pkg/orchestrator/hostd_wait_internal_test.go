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

package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// hostdAway stands in for the daemon's HostdWaitError.
type hostdAway struct{}

func (hostdAway) Error() string        { return "waiting for hostd ws-a" }
func (hostdAway) Is(target error) bool { return target == agent.ErrHostdWaiting }

// A run whose hostd dropped between admission and its framework build is
// interrupted, not a setup failure: no strike, and admission parks the
// task on that hostd.
func TestSetupFailedEndingOnADisconnectedHostdIsNoStrike(t *testing.T) {
	err := fmt.Errorf("orchestrator: building the agent framework for run r1: %w", hostdAway{})
	if !errors.Is(err, agent.ErrHostdWaiting) {
		t.Fatal("the stand-in is not ErrHostdWaiting")
	}
	outcome, detail := setupFailedEnding(err)
	if outcome != model.InterruptedOutcome {
		t.Errorf("outcome = %q, want %q", outcome, model.InterruptedOutcome)
	}
	if !strings.Contains(detail, "waiting for hostd ws-a") {
		t.Errorf("detail = %q, want the hostd it waits for", detail)
	}
}
