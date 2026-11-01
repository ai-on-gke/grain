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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func testServerWithCreds(t *testing.T) (*ui.Server, *ui.Client, *gitproxy.CredentialSet) {
	t.Helper()
	dir := t.TempDir()
	credentials, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, _, _ := testClient(t)
	client.Config.Credentials = credentials
	return ui.NewServerWithClient(client), client, credentials
}

// The deployment's repository configuration API reads and writes the
// repository's repo_config row, which holds what the default team's
// team_repo_config overrides did until schema 23.
func TestDeploymentRepoConfigWritesRepoConfig(t *testing.T) {
	srv, client, _ := testServerWithCreds(t)
	ctx := context.Background()

	// 1. The deployment targets "acme/widgets".
	rec := do(t, srv, "PUT", "/api/deployment", `{"targetRepos":["acme/widgets"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/deployment: code %d, body: %s", rec.Code, rec.Body.String())
	}

	// Set global repo config for acme/widgets
	repoRef := model.RepoRef{Owner: "acme", Name: "widgets"}
	err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                repoRef,
		SetupCommand:        "make build",
		PromptExtension:     "global prompt instructions",
		DefaultCapabilities: []string{"grain-ci"},
		CIMode:              model.CIModeGitHub,
	})
	if err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	// 2. GET resolves the row
	rec = doWithHeaders(t, srv, "GET", "/api/deployment/repos/acme/widgets/config", "", map[string]string{"X-Grain-User": "alice"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET initial config: code %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp ui.DeploymentRepoConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Repo != "acme/widgets" {
		t.Fatalf("unexpected repo: %+v", resp)
	}
	if resp.Effective.SetupCommand != "make build" || resp.Effective.PromptExtension != "global prompt instructions" || resp.Effective.CIMode != model.CIModeGitHub {
		t.Fatalf("unexpected effective config: %+v", resp.Effective)
	}

	// 3. PUT changes the fields it names on the row
	setReq := `{
		"setupCommand": "npm run custom-setup",
		"promptExtension": "team-specific prompt",
		"defaultCapabilities": ["gemini-key", "self-debug"],
		"ciCapabilities": ["repo-read:acme/widgets"],
		"ciMode": "grain",
		"repairTemplateId": "template-123",
		"user": "alice"
	}`
	rec = doWithHeaders(t, srv, "PUT", "/api/deployment/repos/acme/widgets/config", setReq, map[string]string{"X-Grain-User": "alice"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config: code %d, body: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Effective.SetupCommand != "npm run custom-setup" || resp.Effective.PromptExtension != "team-specific prompt" || resp.Effective.CIMode != model.CIModeGrain || resp.Effective.RepairTemplateID != "template-123" {
		t.Fatalf("unexpected effective config after PUT: %+v", resp.Effective)
	}

	saved, err := client.Store.GetRepoConfig(ctx, repoRef)
	if err != nil || saved == nil {
		t.Fatalf("GetRepoConfig = %+v, %v", saved, err)
	}
	if saved.SetupCommand != "npm run custom-setup" || saved.CIMode != model.CIModeGrain || saved.RepairTemplateID != "template-123" ||
		!slices.Equal(saved.DefaultCapabilities, []string{"gemini-key", "self-debug"}) {
		t.Fatalf("repo_config after PUT = %+v", saved)
	}

	// 4. A field the request leaves out keeps what the row holds.
	rec = doWithHeaders(t, srv, "PUT", "/api/deployment/repos/acme/widgets/config", `{"cloneUrl":"https://git.example.com/acme/widgets.git"}`, map[string]string{"X-Grain-User": "alice"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT cloneUrl: code %d, body: %s", rec.Code, rec.Body.String())
	}
	saved, err = client.Store.GetRepoConfig(ctx, repoRef)
	if err != nil || saved == nil || saved.CloneURL != "https://git.example.com/acme/widgets.git" || saved.SetupCommand != "npm run custom-setup" {
		t.Fatalf("repo_config after a second PUT = %+v, %v", saved, err)
	}
}

func TestDeploymentRepoConfigHTTPClient(t *testing.T) {
	srv, _, _ := testServerWithCreds(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := serverAccessClient(srv, ts.URL)
	ctx := context.Background()

	targets := []string{"acme/tools"}
	if _, err := client.UpdateDeployment(ctx, ui.UpdateDeploymentRequest{TargetRepos: &targets}); err != nil {
		t.Fatalf("UpdateDeployment: %v", err)
	}

	// 1. Get initial config via HTTPClient
	cfgResp, err := client.GetDeploymentRepoConfig(ctx, "acme/tools")
	if err != nil {
		t.Fatalf("GetDeploymentRepoConfig: %v", err)
	}
	if cfgResp.Repo != "acme/tools" {
		t.Fatalf("unexpected GetDeploymentRepoConfig resp: %+v", cfgResp)
	}

	// 2. Set config via HTTPClient
	setupCmd := "cargo build"
	promptExt := "use rust guidelines"
	mode := "grain"
	setResp, err := client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{
		SetupCommand:    &setupCmd,
		PromptExtension: &promptExt,
		CIMode:          &mode,
	})
	if err != nil {
		t.Fatalf("SetDeploymentRepoConfig: %v", err)
	}
	if setResp.Effective.SetupCommand != "cargo build" {
		t.Fatalf("unexpected Effective setup command: %+v", setResp.Effective)
	}

	// 3. Pick the team's default connection for the repo via HTTPClient:
	// a name the team holds no connection for is refused.
	if _, err := client.SetDeploymentRepoGitConnection(ctx, "acme/tools", "no-such-connection"); err == nil {
		t.Fatal("SetTeamRepoGitConnection accepted a credential the team holds no connection for")
	}
	if _, err := client.SetDeploymentRepoGitConnection(ctx, "acme/tools", ""); err != nil {
		t.Fatalf("SetTeamRepoGitConnection(default): %v", err)
	}
}

// Finding 6 (2026-10-07 walkthrough; task 2471): PUT deployment repo config
// must refuse unknown capability ids, hand-attached grain-ci in default
// capabilities, and non-CI capabilities in ciCapabilities, matching the
// repository-level endpoints. Already-stored ids are let through so an
// unrelated save or a tightened catalogue never breaks the rest of the form.
func TestDeploymentRepoConfigRefusesUnknownAndInvalidCapabilities(t *testing.T) {
	srv, c, _ := testServerWithCreds(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := serverAccessClient(srv, ts.URL)
	ctx := context.Background()
	targets := []string{"acme/tools"}
	if _, err := client.UpdateDeployment(ctx, ui.UpdateDeploymentRequest{TargetRepos: &targets}); err != nil {
		t.Fatalf("UpdateDeployment: %v", err)
	}

	// 1. defaultCapabilities refuses unknown capability
	unknownDefault := []string{"no-such-cap"}
	_, err := client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{DefaultCapabilities: &unknownDefault})
	if err == nil {
		t.Fatal("SetDeploymentRepoConfig accepted unknown default capability")
	}
	if !strings.Contains(err.Error(), "defaultCapabilities: unknown capability no-such-cap") {
		t.Errorf("refusal %q does not match expected unknown capability refusal", err)
	}

	// 2. defaultCapabilities refuses hand-attached grain-ci
	grainCIDefault := []string{"grain-ci"}
	_, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{DefaultCapabilities: &grainCIDefault})
	if err == nil {
		t.Fatal("SetDeploymentRepoConfig accepted grain-ci as default capability")
	}
	if !strings.Contains(err.Error(), "defaultCapabilities:") || !strings.Contains(err.Error(), "Use Grain CI") {
		t.Errorf("refusal %q does not match expected grain-ci refusal", err)
	}

	// 3. defaultCapabilities refuses CI-only capability (e.g. registry-push)
	ciOnlyDefault := []string{"registry-push"}
	_, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{DefaultCapabilities: &ciOnlyDefault})
	if err == nil {
		t.Fatal("SetDeploymentRepoConfig accepted CI-only capability in defaultCapabilities")
	}
	if !strings.Contains(err.Error(), "defaultCapabilities:") || !strings.Contains(err.Error(), "CI jobs") {
		t.Errorf("refusal %q does not match expected CI-only refusal", err)
	}

	// 4. ciCapabilities refuses unknown capability
	unknownCI := []string{"no-such-cap"}
	_, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{CICapabilities: &unknownCI})
	if err == nil {
		t.Fatal("SetDeploymentRepoConfig accepted unknown CI capability")
	}
	if !strings.Contains(err.Error(), `ciCapabilities: capability "no-such-cap" is not supported for CI`) {
		t.Errorf("refusal %q does not match expected unsupported CI capability refusal", err)
	}

	// 5. ciCapabilities refuses capability that does not support CI (e.g. memory)
	memoryCI := []string{"memory"}
	_, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{CICapabilities: &memoryCI})
	if err == nil {
		t.Fatal("SetDeploymentRepoConfig accepted memory as CI capability")
	}
	if !strings.Contains(err.Error(), `ciCapabilities: capability "memory" is not supported for CI`) {
		t.Errorf("refusal %q does not match expected unsupported CI capability refusal", err)
	}

	// 6. ciCapabilities accepts cross-repo read and write capabilities
	crossRepoCI := []string{"repo-read:acme/tools", "repo-write:acme/tools"}
	resp, err := client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{CICapabilities: &crossRepoCI})
	if err != nil {
		t.Fatalf("SetDeploymentRepoConfig(repo-read/write): %v", err)
	}
	if !slices.Equal(resp.Effective.CICapabilities, crossRepoCI) {
		t.Fatalf("CI capabilities = %+v, want %v", resp.Effective, crossRepoCI)
	}

	// 7. Stored legacy or tightened capabilities are let through on subsequent saves
	repo := model.RepoRef{Owner: "acme", Name: "tools"}
	if err := c.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                repo,
		DefaultCapabilities: []string{"legacy-default-cap"},
		CICapabilities:      []string{"legacy-ci-cap"},
	}); err != nil {
		t.Fatal(err)
	}
	keepStoredDefaults := []string{"legacy-default-cap", "gemini-key"}
	resp, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{DefaultCapabilities: &keepStoredDefaults})
	if err != nil {
		t.Fatalf("SetDeploymentRepoConfig keeping legacy default: %v", err)
	}
	if !slices.Equal(resp.Effective.DefaultCapabilities, keepStoredDefaults) {
		t.Fatalf("Default capabilities = %+v, want %v", resp.Effective, keepStoredDefaults)
	}

	keepStoredCI := []string{"legacy-ci-cap", "repo-read:acme/tools"}
	resp, err = client.SetDeploymentRepoConfig(ctx, "acme/tools", ui.SetDeploymentRepoConfigRequest{CICapabilities: &keepStoredCI})
	if err != nil {
		t.Fatalf("SetDeploymentRepoConfig keeping legacy CI: %v", err)
	}
	if !slices.Equal(resp.Effective.CICapabilities, keepStoredCI) {
		t.Fatalf("CI capabilities = %+v, want %v", resp.Effective, keepStoredCI)
	}
}

// A repository outside the target repos is a person's to ask about -- any
// person is the owner -- but never an agent's: an agent in the context
// does not borrow the daemon's own actor to get past the target list.
func TestDeploymentRepoConfigOffTheTargetReposIsAPersonsNotAnAgents(t *testing.T) {
	t.Parallel()
	srv, client, _ := testServerWithCreds(t)
	ctx := context.Background()
	if rec := do(t, srv, "PUT", "/api/deployment", `{"targetRepos":["acme/widgets"]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/deployment: %d %s", rec.Code, rec.Body)
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "carol"})
	if _, err := client.GetDeploymentRepoConfigWithUser(person, "acme/gadgets", ""); err != nil {
		t.Fatalf("a person asking about acme/gadgets: %v", err)
	}
	agent := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"})
	_, err := client.GetDeploymentRepoConfigWithUser(agent, "acme/gadgets", "")
	if err == nil || !strings.Contains(err.Error(), "target repos") {
		t.Fatalf("an agent asking about acme/gadgets: %v, want a refusal naming the target repos", err)
	}
}
