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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestSandboxRepoMigrationFidelity holds github#sandbox-repo, the grant
// schema migration 103 rewrote github-sandbox to, to the retired
// provider's own dispatch: the same repository created on the same
// account, the same two files with the same content and mode, the same
// prompt, the same refusal on a deployment with no GitHub App, and the
// repository deleted when the pass is released, as Revoke deleted it when
// the run ended.
func TestSandboxRepoMigrationFidelity(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// The retired provider's path, as model.ResolveGrants and
	// prepareCapabilities drove it.
	oldClient := newFakeAppClient("grain-bot")
	old := testProvider(oldClient)
	cc := testContext(testCredentials(), now)
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

	// The rewritten grant's path: the in-process method, minting for the
	// same run with the same secrets.
	newClient := newFakeAppClient("grain-bot")
	current.Store(testProvider(newClient))
	t.Cleanup(func() { current.Store(nil) })
	m := Method()
	if m.Ref() != model.GitHubSandboxRepoRef || m.Grant() != model.GitHubSandboxRepoGrant {
		t.Fatalf("method %s, grant %s; want %s, %s", m.Ref(), m.Grant(), model.GitHubSandboxRepoRef, model.GitHubSandboxRepoGrant)
	}
	if got := model.CanonicalLegacyGrant("github-sandbox"); got != m.Grant() {
		t.Fatalf("github-sandbox is an alias for %q, want %q", got, m.Grant())
	}
	mintCtx := model.WithMintCredentials(model.WithMintRunID(ctx, cc.Run.ID), testCredentials())
	resp, err := m.Spec.Mint(mintCtx, "attempt-1", now.Add(time.Hour), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if !reflect.DeepEqual(repoNames(oldClient), repoNames(newClient)) {
		t.Errorf("repositories created: provider %v, method %v", repoNames(oldClient), repoNames(newClient))
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

	// A renewal keeps the repository and token the run was given.
	again, err := m.Spec.Mint(mintCtx, "attempt-1", now.Add(time.Hour), map[string]any{})
	if err != nil || !reflect.DeepEqual(again, resp) {
		t.Errorf("renewal = %+v, %v; want the first mint again", again, err)
	}

	// Releasing deletes it, as Revoke did.
	if err := old.Revoke(ctx, cc, *mat.Lease); err != nil {
		t.Fatal(err)
	}
	if err := m.Spec.Release(mintCtx, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if len(repoNames(oldClient)) != 0 || len(repoNames(newClient)) != 0 {
		t.Errorf("after release: provider left %v, method left %v", repoNames(oldClient), repoNames(newClient))
	}

	// A deployment with no GitHub App is refused the same way by both.
	empty := &fakeCredentials{material: map[string]string{}}
	res, err := old.Resolve(ctx, testContext(empty, now))
	if err != nil || !res.Refused {
		t.Fatalf("provider Resolve without an App = %+v, %v", res, err)
	}
	refused, err := m.Spec.Mint(model.WithMintCredentials(mintCtx, empty), "attempt-2", now.Add(time.Hour), map[string]any{})
	if err != nil || refused.Status != protocol.StatusRefused || refused.Reason != res.Reason {
		t.Errorf("method without an App = %+v, %v; want refused with %q", refused, err, res.Reason)
	}
}

func repoNames(c *fakeAppClient) []string {
	var out []string
	for name := range c.repos {
		out = append(out, name)
	}
	return out
}
