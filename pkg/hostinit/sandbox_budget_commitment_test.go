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
