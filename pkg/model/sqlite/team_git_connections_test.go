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

package sqlite_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// One team holding credentials on two GitHub hosts and a plain git host
// resolves each repository to the most specific connection on that
// repository's own host, and never to another host's (docs/git-hosts.md).
func TestTeamGitCredentialOnPicksTheMostSpecificConnectionOnTheRepositorysHost(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := setGitConnections(ctx, store, "", []model.TeamGitConnection{
		{Credential: "dotcom-app"},
		{Credential: "dotcom-acme", Scope: "acme"},
		{Credential: "dotcom-widgets", Scope: "acme/widgets"},
		{Credential: "ghe-a-pat", Host: "https://GHE-A.example.com/"},
		{Credential: "ghe-a-infra", Host: "ghe-a.example.com:443", Scope: "infra/*"},
		{Credential: "plain-git", Host: "git.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ host, owner, repo, want string }{
		{"", "someone", "else", "dotcom-app"},
		{"github.com", "ACME", "gadgets", "dotcom-acme"},
		{"", "acme", "widgets.git", "dotcom-widgets"},
		{"ghe-a.example.com", "acme", "widgets", "ghe-a-pat"},
		{"ghe-a.example.com", "infra", "deploy", "ghe-a-infra"},
		{"git.example.com", "acme", "widgets", "plain-git"},
	} {
		got, err := store.GitConnectionOn(ctx, tc.host, tc.owner, tc.repo)
		if err != nil || got != tc.want {
			t.Errorf("TeamGitCredentialOn(%q, %s/%s) = %q, %v; want %q", tc.host, tc.owner, tc.repo, got, err, tc.want)
		}
	}

	_, err := store.GitConnectionOn(ctx, "ghe-b.example.com", "acme", "widgets")
	if !errors.Is(err, model.ErrNoTeamGitCredential) || !strings.Contains(err.Error(), "ghe-b.example.com") {
		t.Errorf("a host with no connection: err = %v, want ErrNoTeamGitCredential naming ghe-b.example.com", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil || len(got.GitConnections) != 6 {
		t.Fatalf("GetConfig = %+v, %v; want the six connections back", got, err)
	}
}

// setGitConnections records the deployment's git connections and legacy
// credential, which grain_config holds since schema 22.
func setGitConnections(ctx context.Context, store *model.Store, legacy string, conns []model.TeamGitConnection) error {
	return store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitCredential = legacy
		c.GitConnections = conns
		return nil
	})
}

// A team configured before connections existed -- one GitCredential, and
// a per-repo override -- resolves exactly as before on the default host,
// and has nothing on any other.
func TestTeamGitCredentialOnKeepsTheLegacySingleCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := setGitConnections(ctx, store, "team-cred", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: model.RepoRef{Owner: "owner", Name: "special"}, GitCredential: "special-cred",
	}); err != nil {
		t.Fatal(err)
	}
	for repo, want := range map[string]string{"repo": "team-cred", "special": "special-cred"} {
		got, err := store.GitConnectionOn(ctx, "", "owner", repo)
		if err != nil || got != want {
			t.Errorf("TeamGitCredential(owner/%s) = %q, %v; want %q", repo, got, err, want)
		}
	}
	if _, err := store.GitConnectionOn(ctx, "ghe.example.com", "owner", "special"); !errors.Is(err, model.ErrNoTeamGitCredential) {
		t.Errorf("legacy team on another host: err = %v, want ErrNoTeamGitCredential", err)
	}
}

// A deployment forwarding to a GitHub Enterprise host makes that host the
// default: a legacy credential, and a connection naming the host
// explicitly, are both on it.
func TestTeamGitCredentialOnFollowsTheDeploymentsDefaultHost(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	store.GitDefaultHost = "ghe.example.com"
	if err := setGitConnections(ctx, store, "legacy", []model.TeamGitConnection{
		{Credential: "hostless-conn"},
		{Credential: "infra", Host: "ghe.example.com", Scope: "infra"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Explicit-host scoped connection:
	got1, err := store.GitConnectionOn(ctx, "", "infra", "repo")
	if err != nil {
		t.Fatal(err)
	}
	got1On, err := store.GitConnectionOn(ctx, "ghe.example.com", "infra", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if got1 != "infra" || got1On != "infra" {
		t.Errorf("TeamGitCredential = %q, TeamGitCredentialOn = %q, want infra", got1, got1On)
	}

	// 2. Host-less connection (matches default host):
	got2, err := store.GitConnectionOn(ctx, "", "other", "repo")
	if err != nil {
		t.Fatal(err)
	}
	got2On, err := store.GitConnectionOn(ctx, "ghe.example.com", "other", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if got2 != "hostless-conn" || got2On != "hostless-conn" {
		t.Errorf("TeamGitCredential = %q, TeamGitCredentialOn = %q, want hostless-conn", got2, got2On)
	}

	// 3. A deployment with only the legacy GitCredential and no connections:
	if err := setGitConnections(ctx, store, "legacy-cred", nil); err != nil {
		t.Fatal(err)
	}
	got3, err := store.GitConnectionOn(ctx, "", "any", "repo")
	if err != nil {
		t.Fatal(err)
	}
	got3On, err := store.GitConnectionOn(ctx, "ghe.example.com", "any", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if got3 != "legacy-cred" || got3On != "legacy-cred" {
		t.Errorf("TeamGitCredential = %q, TeamGitCredentialOn = %q, want legacy-cred", got3, got3On)
	}

	if _, err := store.GitConnectionOn(ctx, "github.com", "infra", "repo"); !errors.Is(err, model.ErrNoTeamGitCredential) {
		t.Errorf("github.com on a GHE deployment: err = %v, want ErrNoTeamGitCredential", err)
	}
}
