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

package githubsandbox

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestSandboxRepoMigrationFidelity holds github#sandbox-repo, the grant
// schema migration 124 rewrote github-sandbox to, to the retired
// provider's own dispatch: the same repository created on the same
// account, the same scoped token permissions, the same two files with the
// same content and mode, the same prompt, the same CI-job minting, the
// same refusal on a deployment with no GitHub App, and the repository
// deleted when the pass is released, as Revoke deleted it when the run
// ended.
func TestSandboxRepoMigrationFidelity(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	prev := Current()
	t.Cleanup(func() { SetCurrent(prev) })

	// 1. Task mint fidelity: retired provider vs github#sandbox-repo MethodSpec.
	oldClient := newFakeAppClient("grain-bot")
	old := testProvider(oldClient)
	// The method names the repository for the attempt, so the provider
	// is held to a run with that ID.
	const attempt = "dep-1-1-0"
	cc := testContext(testCredentials(), now)
	cc.Run.ID = attempt
	if res, err := old.Resolve(ctx, cc); err != nil || res.Refused {
		t.Fatalf("provider Resolve = %+v, %v", res, err)
	}
	mat, err := old.Materialize(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	oldPrompt, err := old.PromptSection(ctx, cc, mat.Placements)
	if err != nil {
		t.Fatal(err)
	}
	var oldPlacements []protocol.Placement
	for _, pl := range mat.Placements {
		oldPlacements = append(oldPlacements, protocol.Placement{Path: pl.Path, Mode: "0" + pl.EffectiveMode(), Content: pl.Content})
	}

	newClient := newFakeAppClient("grain-bot")
	SetCurrent(testProvider(newClient))
	spec := MethodSpec()
	if got := model.CanonicalLegacyGrant("github-sandbox"); got != model.GitHubSandboxRepoGrant {
		t.Fatalf("github-sandbox is an alias for %q, want %q", got, model.GitHubSandboxRepoGrant)
	}

	mintCreds := testCredentials()
	mintCtx := model.WithMintCredentials(model.WithMintRunID(ctx, "t1-1"), mintCreds)
	resp, err := spec.Mint(mintCtx, attempt, now.Add(time.Hour), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if !slices.Equal(sortedRepoNames(oldClient), sortedRepoNames(newClient)) {
		t.Errorf("repositories created: provider %v, method %v", sortedRepoNames(oldClient), sortedRepoNames(newClient))
	}
	if !reflect.DeepEqual(oldClient.mintedPerms, newClient.mintedPerms) || !reflect.DeepEqual(oldClient.mintedRepos, newClient.mintedRepos) {
		t.Errorf("tokens minted: provider %v %v, method %v %v",
			oldClient.mintedRepos, oldClient.mintedPerms, newClient.mintedRepos, newClient.mintedPerms)
	}
	if !reflect.DeepEqual(resp.Placements, oldPlacements) {
		t.Errorf("placements: method %+v, provider %+v", resp.Placements, oldPlacements)
	}
	if resp.PromptSection != oldPrompt {
		t.Errorf("prompt: method %q, provider %q", resp.PromptSection, oldPrompt)
	}

	// Renewal idempotence: keeps the repository and token the run was given.
	again, err := spec.Mint(mintCtx, attempt, now.Add(time.Hour), map[string]any{})
	if err != nil || !reflect.DeepEqual(again, resp) {
		t.Errorf("renewal = %+v, %v; want the first mint again", again, err)
	}

	// Releasing deletes the repository, identically to Revoke.
	if err := old.Revoke(ctx, cc, *mat.Lease); err != nil {
		t.Fatal(err)
	}
	// Released with a context that names no run, as the orchestrator's
	// end-of-attempt release is: the audit still names the minting run.
	if err := spec.Release(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if len(mintCreds.runs) == 0 {
		t.Fatal("the method resolved no credentials")
	}
	for i, run := range mintCreds.runs {
		if run != "t1-1" {
			t.Errorf("credential resolve %d (%s) audited under run %q, want t1-1", i, mintCreds.resolved[i], run)
		}
	}
	if len(sortedRepoNames(oldClient)) != 0 || len(sortedRepoNames(newClient)) != 0 {
		t.Errorf("after release: provider left %v, method left %v", sortedRepoNames(oldClient), sortedRepoNames(newClient))
	}

	// 2. CI job mint fidelity: model.WithCIMint vs provider with CI run ID.
	ciRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	oldCIClient := newFakeAppClient("grain-bot")
	oldCI := testProvider(oldCIClient)
	ciCC := model.CapabilityContext{
		Task:        model.Task{ID: "ci-42-build", Target: &ciRepo},
		Run:         model.Run{ID: "ci-attempt-1", StartedAt: now},
		Credentials: testCredentials(),
	}
	if res, err := oldCI.Resolve(ctx, ciCC); err != nil || res.Refused {
		t.Fatalf("CI provider Resolve = %+v, %v", res, err)
	}
	ciMat, err := oldCI.Materialize(ctx, ciCC)
	if err != nil {
		t.Fatal(err)
	}
	var oldCIPlacements []protocol.Placement
	for _, pl := range ciMat.Placements {
		oldCIPlacements = append(oldCIPlacements, protocol.Placement{Path: pl.Path, Mode: "0" + pl.EffectiveMode(), Content: pl.Content})
	}

	newCIClient := newFakeAppClient("grain-bot")
	SetCurrent(testProvider(newCIClient))
	ciMintCtx := model.WithCIMint(ctx, model.CIMint{
		Repo:        ciRepo,
		RunID:       "ci-42-build",
		Event:       "pull_request",
		Credentials: testCredentials(),
	})
	ciResp, err := spec.Mint(ciMintCtx, "ci-attempt-1", now.Add(time.Hour), map[string]any{})
	if err != nil || ciResp.Status != protocol.StatusOK {
		t.Fatalf("CI mint = %+v, %v", ciResp, err)
	}
	if !slices.Equal(sortedRepoNames(oldCIClient), sortedRepoNames(newCIClient)) {
		t.Errorf("CI repositories created: provider %v, method %v", sortedRepoNames(oldCIClient), sortedRepoNames(newCIClient))
	}
	if !reflect.DeepEqual(ciResp.Placements, oldCIPlacements) {
		t.Errorf("CI placements: method %+v, provider %+v", ciResp.Placements, oldCIPlacements)
	}
	if err := spec.Release(ciMintCtx, "ci-attempt-1"); err != nil {
		t.Fatal(err)
	}
	if len(sortedRepoNames(newCIClient)) != 0 {
		t.Errorf("after CI release: method left %v", sortedRepoNames(newCIClient))
	}

	// 3. A deployment with no GitHub App is refused identically by both.
	empty := &fakeCredentials{material: map[string]string{}}
	res, err := old.Resolve(ctx, testContext(empty, now))
	if err != nil || !res.Refused {
		t.Fatalf("provider Resolve without an App = %+v, %v", res, err)
	}
	refused, err := spec.Mint(model.WithMintCredentials(mintCtx, empty), "attempt-2", now.Add(time.Hour), map[string]any{})
	if err != nil || refused.Status != protocol.StatusRefused || refused.Reason != res.Reason {
		t.Errorf("method without an App = %+v, %v; want refused with %q", refused, err, res.Reason)
	}
}

func sortedRepoNames(c *fakeAppClient) []string {
	var out []string
	for name := range c.repos {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
