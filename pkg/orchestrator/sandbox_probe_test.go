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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeProber struct {
	mode     string
	acquired []string
	sb       *fakeProbeSandbox
}

func (f *fakeProber) Acquire(_ context.Context, name string, _ Shape) (Sandbox, error) {
	f.acquired = append(f.acquired, name)
	f.sb = &fakeProbeSandbox{name: name}
	return f.sb, nil
}

func (f *fakeProber) PodNameFor(sandbox string) (string, error) {
	return podNameWithPrefix(GranulePodNamePrefix, sandbox)
}

func (f *fakeProber) AccessMode() string { return f.mode }

type fakeProbeSandbox struct {
	name     string
	ran      [][]string
	released bool
}

func (s *fakeProbeSandbox) Name() string                              { return s.name }
func (s *fakeProbeSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *fakeProbeSandbox) Release(context.Context) error             { s.released = true; return nil }
func (s *fakeProbeSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}

func (s *fakeProbeSandbox) RunInSandbox(_ context.Context, argv []string, _ string) (string, string, int) {
	s.ran = append(s.ran, argv)
	return strings.Join(argv[1:], " ") + "\n", "", 0
}

func TestProbeSandboxRunsTheCommandInANamespacedSandboxAndReleasesIt(t *testing.T) {
	backend := &fakeProber{mode: K8sGranuleAccessDialout}
	got, err := ProbeSandbox(context.Background(), SandboxProbeDeps{Sandboxes: backend}, SandboxProbeRequest{
		ID: "gke-check-1", Access: K8sGranuleAccessDialout, Argv: []string{"echo", "dialout-ok:gke-check-1"},
	})
	if err != nil {
		t.Fatalf("ProbeSandbox: %v", err)
	}
	if want := []string{"probe-gke-check-1"}; len(backend.acquired) != 1 || backend.acquired[0] != want[0] {
		t.Errorf("acquired %q, want %q", backend.acquired, want)
	}
	if got.Pod != "grain-granule-probe-gke-check-1" || got.Access != K8sGranuleAccessDialout {
		t.Errorf("result = %+v, want pod grain-granule-probe-gke-check-1 over dialout", got)
	}
	if got.Stdout != "dialout-ok:gke-check-1\n" || got.ExitCode != 0 {
		t.Errorf("result = %+v, want the command's own output", got)
	}
	if !backend.sb.released {
		t.Error("probe sandbox was not released")
	}
}

func TestProbeSandboxIsAdmittedAgainstTheBudgetAndGivesItBack(t *testing.T) {
	shape := model.Commitment{CPUs: 4, MemoryMB: 8192}
	ledger := &model.Ledger{}
	deps := SandboxProbeDeps{
		Sandboxes: &fakeProber{mode: K8sGranuleAccessDialout},
		Ledger:    ledger,
		Budget:    model.NewBudget(model.Commitment{CPUs: 8, MemoryMB: 16384}, 0, 1, 1),
		Shape:     shape,
	}
	if _, err := ProbeSandbox(context.Background(), deps, SandboxProbeRequest{ID: "a", Argv: []string{"echo", "x"}}); err != nil {
		t.Fatalf("ProbeSandbox with room: %v", err)
	}
	if n := len(ledger.Holders()); n != 0 {
		t.Errorf("ledger still holds %d charge(s) after the probe finished", n)
	}

	// A machine already full refuses the probe before anything is built.
	release := ledger.Charge(model.PoolAgent, "1-1", model.Commitment{CPUs: 8, MemoryMB: 16384})
	defer release()
	full := &fakeProber{mode: K8sGranuleAccessDialout}
	deps.Sandboxes = full
	_, err := ProbeSandbox(context.Background(), deps, SandboxProbeRequest{ID: "b", Argv: []string{"echo", "x"}})
	if err == nil || !strings.Contains(err.Error(), "sandbox budget") {
		t.Fatalf("err = %v, want a budget refusal", err)
	}
	if len(full.acquired) != 0 {
		t.Errorf("acquired %q past a full budget", full.acquired)
	}
}

func TestProbeSandboxRefusesAnAccessModeTheDeploymentDoesNotUse(t *testing.T) {
	backend := &fakeProber{mode: K8sGranuleAccessKubectl}
	_, err := ProbeSandbox(context.Background(), SandboxProbeDeps{Sandboxes: backend}, SandboxProbeRequest{
		ID: "x", Access: K8sGranuleAccessDialout, Argv: []string{"true"},
	})
	if err == nil || !strings.Contains(err.Error(), `over "kubectl"`) {
		t.Fatalf("err = %v, want a refusal naming the configured mode", err)
	}
	if len(backend.acquired) != 0 {
		t.Errorf("acquired %q for a refused probe", backend.acquired)
	}
}

func TestProbeSandboxRefusesMalformedRequestsBeforeAcquiring(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  SandboxProbeRequest
	}{
		{"no id", SandboxProbeRequest{Argv: []string{"true"}}},
		{"no argv", SandboxProbeRequest{ID: "x"}},
		{"unknown access", SandboxProbeRequest{ID: "x", Access: "ssh", Argv: []string{"true"}}},
		{"invalid pod name", SandboxProbeRequest{ID: "Not_A_Label", Argv: []string{"true"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeProber{mode: K8sGranuleAccessDialout}
			if _, err := ProbeSandbox(context.Background(), SandboxProbeDeps{Sandboxes: backend}, tc.req); err == nil {
				t.Fatal("ProbeSandbox succeeded, want a refusal")
			}
			if len(backend.acquired) != 0 {
				t.Errorf("acquired %q for a refused probe", backend.acquired)
			}
		})
	}
}

func TestProbeSandboxNeedsTheK8sGranuleBackend(t *testing.T) {
	_, err := ProbeSandbox(context.Background(), SandboxProbeDeps{Sandboxes: NewHostSandboxes(t.TempDir())}, SandboxProbeRequest{ID: "x", Argv: []string{"true"}})
	if !errors.Is(err, ErrSandboxProbeUnsupported) {
		t.Fatalf("err = %v, want ErrSandboxProbeUnsupported", err)
	}
}

func TestK8sGranuleSandboxesAccessMode(t *testing.T) {
	if got := NewK8sGranuleSandboxes(K8sGranuleConfig{}).AccessMode(); got != K8sGranuleAccessKubectl {
		t.Errorf("no access configured: AccessMode() = %q, want kubectl", got)
	}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Access: &K8sGranuleAccess{Mode: K8sGranuleAccessDialout}})
	if got := k.AccessMode(); got != K8sGranuleAccessDialout {
		t.Errorf("dialout configured: AccessMode() = %q, want dialout", got)
	}
	var _ sandboxProber = k
}
