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

package hostinit

import (
	"context"
	"strings"
	"testing"
)

// TestCheckSandboxBudgetSaysWhyThereIsNoCommitment is the 10-05
// walkthrough's H-5 side observation: GET /api/settings carries no
// sandboxCommitment while the deployment has no worker slots, for any
// caller, and the step says so -- not "nothing bounds a run".
func TestCheckSandboxBudgetSaysWhyThereIsNoCommitment(t *testing.T) {
	opts := Options{}.withDefaults()
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/settings", 200, []byte(`{}`), nil)

	var r Result
	checkSandboxBudget(context.Background(), sys, opts, &r, true)
	s, _ := stepStatus(r, "check the sandbox budget can admit a run")
	if s.Status != StepSkipped || !strings.Contains(s.Detail, "no worker slots") || strings.Contains(s.Detail, "nothing bounds") {
		t.Errorf("no commitment: %+v", s)
	}
}
