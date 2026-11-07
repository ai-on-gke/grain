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

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

type mockGitHubSandboxAPI struct {
	mu    sync.Mutex
	repos map[string]time.Time
	now   time.Time
}

func startMockGitHubSandboxAPI(t *testing.T) (*mockGitHubSandboxAPI, string, releaseTestCredentials) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	api := &mockGitHubSandboxAPI{
		repos: map[string]time.Time{},
		now:   time.Now().UTC().Truncate(time.Second),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/app/installations"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":      77,
				"account": map[string]any{"login": "sandbox-org"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			var req struct {
				Repositories []string `json:"repositories"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			scope := "org"
			if len(req.Repositories) > 0 {
				scope = strings.Join(req.Repositories, ",")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs-tok-" + scope,
				"expires_at": api.now.Add(time.Hour).Format(time.RFC3339),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/sandbox-org/repos":
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			api.mu.Lock()
			_, exists := api.repos[req.Name]
			if !exists {
				api.repos[req.Name] = api.now
			}
			api.mu.Unlock()
			if exists {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"name already exists"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":      req.Name,
				"full_name": "sandbox-org/" + req.Name,
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/sandbox-org/"):
			name := strings.TrimPrefix(r.URL.Path, "/repos/sandbox-org/")
			api.mu.Lock()
			_, exists := api.repos[name]
			delete(api.repos, name)
			api.mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/sandbox-org/repos":
			api.mu.Lock()
			type item struct {
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"created_at"`
			}
			var list []item
			for name, created := range api.repos {
				list = append(list, item{Name: name, CreatedAt: created})
			}
			api.mu.Unlock()
			_ = json.NewEncoder(w).Encode(list)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	creds := releaseTestCredentials{
		githubsandbox.DefaultAppIDCredential:      "app-77",
		githubsandbox.DefaultPrivateKeyCredential: string(pemBytes),
	}
	return api, srv.Listener.Addr().String(), creds
}

func (a *mockGitHubSandboxAPI) repoCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.repos)
}

func (a *mockGitHubSandboxAPI) hasRepo(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.repos[name]
	return ok
}

func TestGitHubSandboxServerMigrationFidelity(t *testing.T) {
	api, host, validCreds := startMockGitHubSandboxAPI(t)
	cfg := githubsandbox.Config{Host: host, InsecureHTTP: true}
	githubsandbox.Configure(cfg)
	t.Cleanup(func() { githubsandbox.Configure(githubsandbox.Config{}) })

	legacyReg := model.NewCapabilityRegistry(githubsandbox.NewProvider(cfg))
	if got := model.CanonicalLegacyGrant("github-sandbox"); got != model.GitHubSandboxRepoGrant {
		t.Fatalf("CanonicalLegacyGrant(github-sandbox) = %q, want %q", got, model.GitHubSandboxRepoGrant)
	}

	attemptID, err := protocol.AttemptID("dep1", "42", 1, 0)
	if err != nil {
		t.Fatalf("AttemptID: %v", err)
	}

	// 1. Task path fidelity (configured + unconfigured, canonical grant + bare alias).
	for _, unconfigured := range []bool{false, true} {
		creds := validCreds
		if unconfigured {
			creds = releaseTestCredentials{}
		}
		ctx := context.Background()
		legacyCC := model.CapabilityContext{
			Task:        model.Task{ID: "42", Grants: []model.Grant{{Capability: "github-sandbox", Via: model.GrantByLabel}}},
			Run:         model.Run{ID: attemptID},
			Now:         api.now,
			Deadline:    api.now.Add(30 * time.Minute),
			Credentials: creds,
		}
		resolved, err := model.ResolveGrants(ctx, legacyReg, legacyCC)
		if err != nil {
			t.Fatalf("legacy task ResolveGrants(unconfigured=%v): %v", unconfigured, err)
		}
		oldRefused := resolved[0].Resolution.Refused
		var oldFiles []string
		var oldPrompt string
		if !oldRefused {
			mats, err := model.MaterializeGrants(ctx, legacyReg, legacyCC, resolved)
			if err != nil {
				t.Fatalf("legacy task MaterializeGrants: %v", err)
			}
			for _, pl := range mats[0].Materialization.Placements {
				oldFiles = append(oldFiles, fmt.Sprintf("%s 0%s %q", pl.Path, pl.EffectiveMode(), pl.Content))
			}
			sort.Strings(oldFiles)
			p := githubsandbox.NewProvider(cfg)
			oldPrompt, err = p.PromptSection(ctx, legacyCC, mats[0].Materialization.Placements)
			if err != nil {
				t.Fatalf("legacy PromptSection: %v", err)
			}
			if err := p.Revoke(ctx, legacyCC, *mats[0].Materialization.Lease); err != nil {
				t.Fatalf("legacy Revoke: %v", err)
			}
		}

		for _, spelling := range []string{
			model.GitHubSandboxRepoGrant,
			model.CanonicalLegacyGrant("github-sandbox"),
			model.GitHubSandboxRepoRef,
			// Not the bare server id: the github server also answers
			// core's #read-issues, so a grant naming no method is refused.
		} {
			lookup := grainserver.Lookup(nil)
			recorded, err := model.NormalizeCapabilityGrants([]model.Grant{{Capability: spelling, Via: model.GrantByLabel}}, lookup)
			if err != nil {
				t.Fatalf("NormalizeCapabilityGrants(%s): %v", spelling, err)
			}
			grants, err := model.CapabilityGrants(recorded, lookup)
			if err != nil {
				t.Fatalf("CapabilityGrants(%s): %v", spelling, err)
			}
			mintCtx := model.WithMintRunID(ctx, "run-42")
			mintCtx = model.WithMintCredentials(mintCtx, creds)
			pass, err := protocol.MintPass(mintCtx, grants, func(id string) (protocol.Minter, error) {
				return lookup(id)
			}, protocol.PassOptions{
				Deployment: "dep1",
				Task:       "42",
				Attempt:    1,
				Deadline:   api.now.Add(30 * time.Minute),
				Now:        func() time.Time { return api.now },
			})
			newRefused := err != nil
			if newRefused != oldRefused {
				t.Fatalf("task unconfigured=%v spelling=%s: refused=%v (%v), want %v", unconfigured, spelling, newRefused, err, oldRefused)
			}
			if newRefused {
				continue
			}
			var newFiles []string
			for _, pl := range pass.Placements() {
				newFiles = append(newFiles, fmt.Sprintf("%s %s %q", pl.Path, pl.Mode, pl.Content))
			}
			sort.Strings(newFiles)
			if !slices.Equal(oldFiles, newFiles) {
				t.Errorf("task spelling=%s placements = %v, want %v", spelling, newFiles, oldFiles)
			}
			if gotPrompt := pass.Grants[0].Response.PromptSection; gotPrompt != oldPrompt {
				t.Errorf("task spelling=%s prompt = %q, want %q", spelling, gotPrompt, oldPrompt)
			}
			if api.repoCount() != 1 {
				t.Errorf("expected 1 active sandbox repo before Release, got %d", api.repoCount())
			}
			if _, err := pass.Release(context.Background()); err != nil {
				t.Fatalf("pass.Release(%s): %v", spelling, err)
			}
			if api.repoCount() != 0 {
				t.Errorf("ephemeral repo survived pass.Release (count=%d)", api.repoCount())
			}
		}
	}

	// 2. CI path fidelity across every CI event (unlike release#*, #sandbox-repo
	// is NOT event-gated: every CI event receives it when granted).
	ciAttemptID, err := protocol.AttemptID("dep1", "0123abcd", 2, 0)
	if err != nil {
		t.Fatalf("CI AttemptID: %v", err)
	}
	for _, e := range releaseTestEvents {
		for _, unconfigured := range []bool{false, true} {
			creds := validCreds
			if unconfigured {
				creds = releaseTestCredentials{}
			}
			ctx := context.Background()
			cc, resolved, err := ResolveCICapabilities(ctx, legacyReg, creds, model.RepoRef{Owner: "acme", Name: "widgets"}, ciAttemptID, e.event, e.trusted, []string{"github-sandbox"})
			if err != nil {
				t.Fatalf("legacy CI %s: %v", e.event, err)
			}
			oldRefused := len(resolved) == 1 && resolved[0].Resolution.Refused
			var oldFiles []string
			if !oldRefused {
				mats, err := model.MaterializeGrants(ctx, legacyReg, cc, resolved)
				if err != nil {
					t.Fatalf("legacy CI materialize %s: %v", e.event, err)
				}
				for _, pl := range mats[0].Materialization.Placements {
					oldFiles = append(oldFiles, fmt.Sprintf("%s 0%s %q", pl.Path, pl.EffectiveMode(), pl.Content))
				}
				sort.Strings(oldFiles)
				if err := githubsandbox.NewProvider(cfg).Revoke(ctx, cc, *mats[0].Materialization.Lease); err != nil {
					t.Fatalf("legacy CI revoke: %v", err)
				}
			}

			for _, spelling := range []string{model.GitHubSandboxRepoGrant, "github-sandbox"} {
				newFiles, newRefused := releaseRewrittenOutcome(t, creds, &model.Config{DeploymentID: "dep1"}, e.event, e.trusted, spelling)
				if newRefused != oldRefused {
					t.Fatalf("CI %s trusted=%v unconfigured=%v spelling=%s: refused=%v, want %v",
						e.event, e.trusted, unconfigured, spelling, newRefused, oldRefused)
				}
				if !slices.Equal(oldFiles, newFiles) {
					t.Errorf("CI %s spelling=%s placements = %v, want %v", e.event, spelling, newFiles, oldFiles)
				}
				if api.repoCount() != 0 {
					t.Errorf("CI %s spelling=%s leaked repo after Acquire's done() released the pass", e.event, spelling)
				}
			}
		}
	}
}

func TestGitHubSandboxRepoGrantedAndPolicyRules(t *testing.T) {
	_, host, creds := startMockGitHubSandboxAPI(t)
	githubsandbox.Configure(githubsandbox.Config{Host: host, InsecureHTTP: true})
	t.Cleanup(func() { githubsandbox.Configure(githubsandbox.Config{}) })

	// 1. RepoGranted: without repo_config.ci_capabilities granting #sandbox-repo,
	// a workflow job declaring github-sandbox, .../github#sandbox-repo, or the
	// methodless shorthand .../github is NOT granted it by declaration alone,
	// even if capability_policy explicitly allows it.
	eng := &ci.Engine{}
	for _, declared := range []string{
		"github-sandbox",
		model.GitHubSandboxRepoRef,
		model.GitHubSandboxRepoGrant,
		model.GitHubCapabilityServerID,
	} {
		job := ci.Job{Capabilities: []string{declared}}
		if got := eng.JobCapabilities(job, nil); len(got) != 0 {
			t.Errorf("JobCapabilities(%q, provided=nil) = %v, want none without repo_config.ci_capabilities", declared, got)
		}
		if unmet := workflows.UnmetRequirements(job, workflows.Request{}); len(unmet) == 0 {
			t.Errorf("UnmetRequirements(%q, provided=nil) = %v, want unmet", declared, unmet)
		}
		// Once repo_config.ci_capabilities holds the migrated grant, every
		// spelling in .grain/workflows resolves and matches cleanly.
		provided := []string{model.GitHubSandboxRepoGrant}
		got := eng.JobCapabilities(job, provided)
		if len(got) != 1 || model.CanonicalCIGrant(got[0]) != model.GitHubSandboxRepoGrant {
			t.Errorf("JobCapabilities(%q, provided=%v) = %v, want canonical %s", declared, provided, got, model.GitHubSandboxRepoGrant)
		}
		if unmet := workflows.UnmetRequirements(job, workflows.Request{Capabilities: provided}); len(unmet) != 0 {
			t.Errorf("UnmetRequirements(%q, provided=%v) = %v, want none", declared, provided, unmet)
		}
	}

	// 2. Policy can still narrow #sandbox-repo on CI or task runs, while
	// VerdictForKind reports allowed (not CI-only) for task runs by default.
	var defaultPolicy model.CapabilityPolicy
	if v := defaultPolicy.VerdictForKind(model.GitHubSandboxRepoGrant, model.CapabilityRunTask); v == model.CapabilityPolicyCIOnly {
		t.Errorf("VerdictForKind(github#sandbox-repo, task) = %q, want non-CI-only", v)
	}
	denied := &model.Config{
		DeploymentID: "dep1",
		CapabilityPolicy: model.CapabilityPolicy{
			Rules: []model.CapabilityPolicyRule{{
				Capability: model.GitHubSandboxRepoRef,
				Repos:      []string{"acme/widgets"},
				Deny:       true,
			}},
		},
	}
	g := &CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
		Credentials:  creds,
		Servers:      func() orchestrator.CapabilityServers { return releaseInProcessServers{} },
		Config:       func(context.Context) (*model.Config, error) { return denied, nil },
	}
	_, err := g.Acquire(context.Background(), ci.SandboxRequest{
		Name: "c0123abcd-9", Repo: "acme/widgets", Event: ci.EventPullRequest,
		RunID: "0123abcd", JobAttempt: 1, Capabilities: []string{model.GitHubSandboxRepoGrant},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "denies") {
		t.Errorf("CI Acquire under deny policy = %v, want policy denial", err)
	}
}

func TestReapCapabilitiesBeforeSweepsInProcessGitHubSandbox(t *testing.T) {
	api, host, creds := startMockGitHubSandboxAPI(t)
	githubsandbox.Configure(githubsandbox.Config{Host: host, InsecureHTTP: true})
	t.Cleanup(func() { githubsandbox.Configure(githubsandbox.Config{}) })

	api.mu.Lock()
	api.repos["grain-sandbox-stale"] = api.now.Add(-2 * time.Hour)
	api.repos["grain-sandbox-live"] = api.now.Add(-2 * time.Minute)
	api.mu.Unlock()

	// Even with a nil legacy provider registry, ReapCapabilitiesBefore sweeps
	// registered capability.Methods() declaring Reap.
	ReapCapabilitiesBefore(context.Background(), nil, creds, api.now.Add(-10*time.Minute))

	if api.hasRepo("grain-sandbox-stale") {
		t.Errorf("stale grain-sandbox-stale survived ReapCapabilitiesBefore")
	}
	if !api.hasRepo("grain-sandbox-live") {
		t.Errorf("live grain-sandbox-live was deleted by ReapCapabilitiesBefore")
	}
}
