package main

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

// Core grain turns nothing on by default: everything this binary links
// is registered by the time a test runs, and none of it says DefaultOn,
// so a fresh deployment of the core build starts with no default
// capabilities at all (google/cmd/grain's own test is the other half).
func TestCoreBuildDefaultsNoCapabilityOn(t *testing.T) {
	if got := capability.DefaultOn(); len(got) != 0 {
		t.Errorf("capability.DefaultOn() = %v in the core build, want none", got)
	}
	if got := capability.NewDeploymentConfig().DefaultCapabilities; len(got) != 0 {
		t.Errorf("a fresh core deployment's DefaultCapabilities = %v, want none", got)
	}
	wantDirector := []string{"self-debug", "grain-log", "deployment-health", "bootstrap-playbooks"}
	if got := model.DefaultConsoleSessions()[0].Capabilities; !slices.Equal(got, wantDirector) {
		t.Errorf("a fresh core deployment's Director capabilities = %v, want %v", got, wantDirector)
	}
	if got := version.Get().Edition; got != "" {
		t.Errorf("version.Get().Edition = %q in the core build, want empty", got)
	}
}
