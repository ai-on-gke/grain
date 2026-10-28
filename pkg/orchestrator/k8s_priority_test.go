package orchestrator

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/model"
)

// Every kind of sandbox pod runs at its own class -- the ordering that
// lets an interactive session preempt CI on a full cluster -- on both
// Kubernetes backends, and an acquire that named no class is an agent's.
func TestPodOptionsGiveEachSandboxClassItsPriorityClass(t *testing.T) {
	t.Parallel()
	classes := DefaultK8sPriorityClasses()
	pools := map[string]func(Shape) k8s.PodOptions{
		"kubernetes": NewK8sSandboxes(K8sConfig{Image: "example/sandbox", PriorityClasses: classes}).podOptions,
		"kubernetes-granule": NewK8sGranuleSandboxes(K8sGranuleConfig{
			Image: "example/granule", PriorityClasses: classes,
		}).podOptions,
	}
	want := map[SandboxClass]string{
		SandboxClassInteractive: "grain-interactive",
		SandboxClassAgent:       "grain-agent",
		SandboxClassCI:          "grain-ci",
		"":                      "grain-agent",
	}
	for backend, podOptions := range pools {
		for class, name := range want {
			if got := podOptions(Shape{Class: class}).PriorityClassName; got != name {
				t.Errorf("%s: class %q got priorityClassName %q, want %q", backend, class, got, name)
			}
		}
	}
}

// An empty name is "no priority class" for that kind of pod alone, and a
// deployment that configured none at all gets none on any pod.
func TestPodOptionsSetNoPriorityClassWhereNoneIsNamed(t *testing.T) {
	t.Parallel()
	partial := DefaultK8sPriorityClasses()
	partial.CI = ""
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", PriorityClasses: partial})
	if got := k.podOptions(Shape{Class: SandboxClassCI}).PriorityClassName; got != "" {
		t.Errorf("CI pod got priorityClassName %q with its class cleared, want none", got)
	}
	if got := k.podOptions(Shape{Class: SandboxClassInteractive}).PriorityClassName; got != "grain-interactive" {
		t.Errorf("interactive pod got %q, want grain-interactive", got)
	}
	bare := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	if got := bare.podOptions(Shape{Class: SandboxClassInteractive}).PriorityClassName; got != "" {
		t.Errorf("unconfigured granule pod got priorityClassName %q, want none", got)
	}
}

func TestSandboxClassForAnInteractiveTaskOutranksAnOrdinaryOne(t *testing.T) {
	t.Parallel()
	if got := sandboxClassFor(model.Task{Interactive: true}); got != SandboxClassInteractive {
		t.Errorf("interactive task: class %q, want %q", got, SandboxClassInteractive)
	}
	if got := sandboxClassFor(model.Task{}); got != SandboxClassAgent {
		t.Errorf("ordinary task: class %q, want %q", got, SandboxClassAgent)
	}
}
