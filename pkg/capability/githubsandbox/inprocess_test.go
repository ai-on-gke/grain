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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

func testSandboxCredentials() *fakeCredentials {
	return &fakeCredentials{
		material: map[string]string{
			DefaultAppIDCredential:      "12345",
			DefaultPrivateKeyCredential: "-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----\n",
		},
	}
}

func TestInProcessSandboxRepoTaskAndCIMintReleaseAndReap(t *testing.T) {
	fake := newFakeAppClient("sandbox-bot")
	baseNow := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	fake.now = baseNow
	p := &Provider{
		newClient: func(context.Context, string, string, string, bool) (appClient, error) {
			return fake, nil
		},
	}
	t.Cleanup(ConfigureProvider(p))

	srv, ok, err := capability.MethodServer(ServerID)
	if err != nil || !ok || srv == nil {
		t.Fatalf("MethodServer(%q) = %v, %v, %v", ServerID, srv, ok, err)
	}
	minter := func(id string) (protocol.Minter, error) {
		if id != ServerID {
			t.Fatalf("unexpected server lookup %q", id)
		}
		return srv, nil
	}
	grants, err := model.CapabilityGrants([]model.Grant{{
		Capability: model.GitHubSandboxRepoGrant,
		Via:        model.GrantByLabel,
	}}, func(id string) (*protocol.Server, error) { return srv, nil })
	if err != nil {
		t.Fatalf("CapabilityGrants: %v", err)
	}

	// 1. Task mint: creates grain-sandbox-<attempt>, places 0600 repo + token
	// files and PromptSection, renews idempotently, and deletes repo on Release.
	creds := testSandboxCredentials()
	taskCtx := model.WithMintRunID(context.Background(), "run-42")
	taskCtx = model.WithMintCredentials(taskCtx, creds)
	deadline := baseNow.Add(30 * time.Minute)

	pass, err := protocol.MintPass(taskCtx, grants, minter, protocol.PassOptions{
		Deployment: "grain",
		Task:       "42",
		Attempt:    1,
		Deadline:   deadline,
		Now:        func() time.Time { return baseNow },
	})
	if err != nil {
		t.Fatalf("MintPass(task): %v", err)
	}
	wantRepoName := repoName(pass.Grants[0].AttemptID)
	if !strings.HasPrefix(wantRepoName, "grain-sandbox-") {
		t.Fatalf("unexpected repo name %q", wantRepoName)
	}
	if _, exists := fake.repos[wantRepoName]; !exists {
		t.Fatalf("expected ephemeral repo %q to exist, got %v", wantRepoName, fake.repos)
	}
	pls := pass.Placements()
	if len(pls) != 2 {
		t.Fatalf("Placements() = %+v, want 2 files", pls)
	}
	byPath := map[string]protocol.Placement{}
	for _, pl := range pls {
		byPath[pl.Path] = pl
	}
	if got := byPath[SandboxRepoPath]; got.Mode != "0600" || got.Content != "sandbox-bot/"+wantRepoName {
		t.Errorf("repo placement = %+v, want 0600 sandbox-bot/%s", got, wantRepoName)
	}
	if got := byPath[SandboxTokenPath]; got.Mode != "0600" || got.Content != "token-for-"+wantRepoName {
		t.Errorf("token placement = %+v, want 0600 token-for-%s", got, wantRepoName)
	}
	prompt := pass.Grants[0].Response.PromptSection
	if !strings.Contains(prompt, SandboxRepoPath) || !strings.Contains(prompt, SandboxTokenPath) {
		t.Errorf("PromptSection = %q, want sandbox repo instructions", prompt)
	}

	// Renewing the same attempt must reuse the lease rather than failing on
	// duplicate repo creation.
	if err := pass.Renew(taskCtx, deadline.Add(15*time.Minute)); err != nil {
		t.Fatalf("Renew(task): %v", err)
	}

	// Release with a bare background context still revokes using the mint's
	// held credential resolver.
	if _, err := pass.Release(context.Background()); err != nil {
		t.Fatalf("Release(task): %v", err)
	}
	if _, exists := fake.repos[wantRepoName]; exists {
		t.Errorf("repo %q still exists after pass.Release", wantRepoName)
	}

	// 2. CI mint: creates grain-sandbox-<attempt> via WithCIMint and deletes
	// it on Release.
	ciCtx := model.WithCIMint(context.Background(), model.CIMint{
		Repo:        model.RepoRef{Owner: "acme", Name: "widgets"},
		RunID:       "c999-0",
		Credentials: creds,
	})
	ciPass, err := protocol.MintPass(ciCtx, grants, minter, protocol.PassOptions{
		Deployment: "grain",
		Task:       "ci-999",
		Attempt:    1,
		Deadline:   deadline,
		Now:        func() time.Time { return baseNow },
	})
	if err != nil {
		t.Fatalf("MintPass(CI): %v", err)
	}
	ciRepoName := repoName(ciPass.Grants[0].AttemptID)
	if _, exists := fake.repos[ciRepoName]; !exists {
		t.Fatalf("expected CI ephemeral repo %q, got %v", ciRepoName, fake.repos)
	}
	if _, err := ciPass.Release(context.Background()); err != nil {
		t.Fatalf("Release(CI): %v", err)
	}
	if _, exists := fake.repos[ciRepoName]; exists {
		t.Errorf("CI repo %q still exists after Release", ciRepoName)
	}

	// 3. Missing App secret refuses cleanly with bootstrap-github-app guidance.
	unconfiguredCtx := model.WithMintCredentials(context.Background(), &fakeCredentials{material: map[string]string{}})
	_, err = protocol.MintPass(unconfiguredCtx, grants, minter, protocol.PassOptions{
		Deployment: "grain",
		Task:       "99",
		Attempt:    1,
		Deadline:   deadline,
		Now:        func() time.Time { return baseNow },
	})
	if err == nil || !strings.Contains(err.Error(), "bootstrap-github-app") {
		t.Fatalf("MintPass(unconfigured) = %v, want bootstrap-github-app refusal", err)
	}

	// 4. Registered method's Reap sweeps stray grain-sandbox-* repos older
	// than boundary and leaves newer ones alone.
	fake.repos["grain-sandbox-leaked"] = &fakeRepo{createdAt: baseNow.Add(-2 * time.Hour)}
	fake.repos["grain-sandbox-fresh"] = &fakeRepo{createdAt: baseNow.Add(-5 * time.Minute)}
	var reaper func(context.Context, model.CredentialResolver, time.Time) ([]string, error)
	for _, m := range capability.Methods() {
		if m.Ref() == model.GitHubSandboxRepoRef {
			reaper = m.Reap
		}
	}
	if reaper == nil {
		t.Fatalf("method %s registered no Reap function", model.GitHubSandboxRepoRef)
	}
	deleted, err := reaper(context.Background(), creds, baseNow.Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !slices.Equal(deleted, []string{"sandbox-bot/grain-sandbox-leaked"}) {
		t.Errorf("Reap deleted %v, want [sandbox-bot/grain-sandbox-leaked]", deleted)
	}
	if _, still := fake.repos["grain-sandbox-fresh"]; !still {
		t.Errorf("Reap deleted fresh repo grain-sandbox-fresh")
	}
}
