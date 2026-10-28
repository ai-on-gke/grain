package agent_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// -host-sandbox-env's entries reach the forked mcpserver as the path of
// the file holding them, never as their values (GSA1-12), and only for a
// local-directory sandbox: a VM or container run's mcpserver has no
// -sandbox-root for them to apply to and refuses them.
func TestSandboxEnvArgsOnlyForASandboxRoot(t *testing.T) {
	const file = "/tmp/grain-sandbox-env-123"
	got := agent.SandboxEnvArgs(agent.RunConfig{SandboxRoot: "/sb/run-1", SandboxEnvFile: file})
	want := []string{"-sandbox-env-file", file}
	if !slices.Equal(got, want) {
		t.Errorf("SandboxEnvArgs = %q, want %q", got, want)
	}
	if got := agent.SandboxEnvArgs(agent.RunConfig{GranuleVM: "vm-1", SandboxEnvFile: file}); got != nil {
		t.Errorf("SandboxEnvArgs for a granule VM = %q, want nothing", got)
	}
	if got := agent.SandboxEnvArgs(agent.RunConfig{SandboxRoot: "/sb/run-1"}); got != nil {
		t.Errorf("SandboxEnvArgs with no env = %q, want nothing", got)
	}
}

// A host sandbox's hidden paths reach the forked mcpserver on its argv,
// one -sandbox-hide each, alongside the env file or without one -- and,
// like it, only for a local-directory sandbox (grain/task-4366).
func TestSandboxEnvArgsCarryTheHiddenPaths(t *testing.T) {
	const file = "/tmp/grain-sandbox-env-123"
	hide := []string{"/var/lib/grain/data", "/etc/grain/token"}
	got := agent.SandboxEnvArgs(agent.RunConfig{SandboxRoot: "/sb/run-1", SandboxEnvFile: file, SandboxHide: hide})
	want := []string{"-sandbox-env-file", file, "-sandbox-hide", "/var/lib/grain/data", "-sandbox-hide", "/etc/grain/token"}
	if !slices.Equal(got, want) {
		t.Errorf("SandboxEnvArgs = %q, want %q", got, want)
	}
	got = agent.SandboxEnvArgs(agent.RunConfig{SandboxRoot: "/sb/run-1", SandboxHide: hide[:1]})
	if want := []string{"-sandbox-hide", "/var/lib/grain/data"}; !slices.Equal(got, want) {
		t.Errorf("SandboxEnvArgs with no env = %q, want %q", got, want)
	}
	if got := agent.SandboxEnvArgs(agent.RunConfig{GranuleVM: "vm-1", SandboxHide: hide}); got != nil {
		t.Errorf("SandboxEnvArgs for a granule VM = %q, want nothing", got)
	}
}
