package claude

import (
	"context"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// A run on a -k8s-sandboxes deployment has no sandbox root, VM or
// container -- its sandbox is a pod -- and used to fail every dispatch
// with "RunConfig.SandboxRoot, .GranuleVM, or .Container is required"
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F1). The forked
// mcpserver is pointed at the pod instead.
func TestMCPServerArgsReachAKubernetesSandboxPod(t *testing.T) {
	f := New("/usr/local/bin/claude", "/usr/local/bin/grain")
	args, err := f.mcpServerArgs(context.Background(), agent.RunConfig{
		K8sPod: agent.KubePod{Namespace: "grain", Pod: "grain-sandbox-7-1", Workspace: "/home/agent"},
	})
	if err != nil {
		t.Fatalf("mcpServerArgs for a sandbox pod: %v", err)
	}
	want := []string{"mcpserver", "-k8s-pod", "grain-sandbox-7-1", "-k8s-namespace", "grain", "-workspace", "/home/agent"}
	if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
		t.Fatalf("mcpServerArgs = %q, want it to start %q", args, want)
	}
}
