package model

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// pkg/workflows restates LifecycleImageEnv because it may not import this
// package; a job's key and its environment both read that copy, so the
// two must not drift.
func TestWorkflowsSpellsTheLifecycleImageVariableTheSameWay(t *testing.T) {
	if workflows.LifecycleImageEnv != LifecycleImageEnv {
		t.Fatalf("workflows.LifecycleImageEnv = %q, model.LifecycleImageEnv = %q", workflows.LifecycleImageEnv, LifecycleImageEnv)
	}
}
