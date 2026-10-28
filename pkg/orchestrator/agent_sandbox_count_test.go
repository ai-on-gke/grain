package orchestrator

import "testing"

// An agent run's sandbox is named for the run -- digits and a hyphen --
// and a CI job's is "c" plus the run it belongs to (ci.sandboxName).
// That one letter is the whole of what tells the two apart in a
// backend's live map, so it is worth a test of its own: the count that
// ends up in a queued CI job's activity line is only as good as this.
func TestAgentSandboxCountSeparatesAgentSandboxesFromCIJobs(t *testing.T) {
	names := []string{"405", "432-2", "c9f2c1a-0", "c9f2c1a-1", "cac1125-0"}

	host := NewHostSandboxes(t.TempDir())
	for _, name := range names {
		host.live[name] = &hostSandbox{}
	}
	if got, want := host.AgentSandboxCount(), 2; got != want {
		t.Errorf("host backend counted %d agent sandboxes of %d, want %d", got, len(names), want)
	}
	if got, want := host.SandboxCount(), len(names); got != want {
		t.Errorf("the whole count is unchanged: %d, want %d", got, want)
	}

	k8s := &K8sSandboxes{live: map[string]*k8sSandbox{}}
	for _, name := range names {
		k8s.live[name] = &k8sSandbox{}
	}
	if got, want := k8s.AgentSandboxCount(), 2; got != want {
		t.Errorf("k8s backend counted %d agent sandboxes, want %d", got, want)
	}

	granule := &GranuleSandboxes{live: map[string]*granuleSandbox{}}
	for _, name := range names {
		granule.live[name] = &granuleSandbox{}
	}
	if got, want := granule.AgentSandboxCount(), 2; got != want {
		t.Errorf("granule backend counted %d agent sandboxes, want %d", got, want)
	}
}
