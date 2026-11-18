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

package orchestrator_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestRunCycleRefusesExternalGrantsOnAnUnfencedKubernetesBackend walks
// docs/test-plans/2026-11-13.md §12 item 1's last check through a real
// dispatch cycle rather than OpenNetworkUnfenced alone: on the kubernetes
// backend without -k8s-network-policy, a task holding an external
// capability grant is parked before any pod is created, and what it is
// told names the command that turns the flag on for a deployment that
// already has stored settings.
func TestRunCycleRefusesExternalGrantsOnAnUnfencedKubernetesBackend(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t-k8s-unfenced", repo)
	task.Grants = []model.Grant{{
		Capability: `https://grain.dev/capabilities/config-map#config-map={"config_maps":["operator-map"]}`,
		Via:        model.GrantByLabel,
	}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// Kubectl names a binary that does not exist: the cycle's sandbox
	// reconciliation shells out to kubectl, and against a developer's
	// current context it would reap that cluster's sandbox pods.
	k8s := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Image: "example/sandbox", Namespace: "grain",
		Kubectl: filepath.Join(t.TempDir(), "no-kubectl"), Kubeconfig: filepath.Join(t.TempDir(), "no-kubeconfig"),
	})
	dialed := false
	deps := orchestrator.Deps{
		Store: store, Client: client,
		Sandboxes:         k8s,
		Framework:         orchestrator.StaticFramework(&grantCapturingFramework{}),
		CapabilityServers: countingCapabilityServers{inner: staticCapabilityServers{}, onLookup: func(string) { dialed = true }},
		CapabilityPasses:  orchestrator.NewCapabilityPasses(),
		MaxWorkers:        1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatal(err)
	}
	if dialed {
		t.Error("an unfenced kubernetes backend dialed a capability server for an external grant")
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said strings.Builder
	for _, c := range comments {
		said.WriteString(c.Body + "\n")
	}
	for _, want := range []string{
		"-k8s-network-policy",
		"`grain settings -k8s-network-policy=true`",
		"grain-sandbox-egress",
		"so it runs no attempt holding a capability grant",
	} {
		if !strings.Contains(said.String(), want) {
			t.Errorf("the refused task was not told %q:\n%s", want, said.String())
		}
	}
}
