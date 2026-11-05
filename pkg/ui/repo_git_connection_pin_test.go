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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The 2026-11-01 walkthrough added a GitHub App connection scoped to
// acme/widgets, then picked "acme on github.com (PAT)" for acme/widgets
// in Settings -> Git's Repositories card: the App connection was dropped
// and its stored credential left behind, with no confirmation. Choosing a
// connection for a repository may only drop a repository-scoped entry
// whose credential another connection still holds.
func TestSetTeamRepoGitConnectionKeepsAConnectionWithItsOwnCredential(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	conns := []model.TeamGitConnection{
		{Credential: "walk-app", Scope: "acme/widgets"},
		{Credential: "walk-pat", Scope: "acme"},
		{Credential: "default"},
	}
	if _, err := c.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{GitConnections: &conns}); err != nil {
		t.Fatal(err)
	}
	for _, pick := range []string{"walk-pat", ""} {
		_, err := c.SetTeamRepoGitConnection(ctx, model.DefaultTeamID, "acme", "widgets", ui.SetTeamRepoGitConnectionRequest{Credential: pick}, "walker")
		if err == nil || !strings.Contains(err.Error(), "walk-app") {
			t.Fatalf("SetTeamRepoGitConnection(acme/widgets -> %q) = %v, want a refusal naming walk-app", pick, err)
		}
	}
	team, err := c.GetTeam(ctx, model.DefaultTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if !holdsConnection(team.GitConnections, "walk-app", "acme/widgets") {
		t.Fatalf("the App connection was dropped: %+v", team.GitConnections)
	}

	// A repository pinned to a credential another connection holds can
	// still be re-pointed, and set back to Default.
	if _, err := c.SetTeamRepoGitConnection(ctx, model.DefaultTeamID, "acme", "lib", ui.SetTeamRepoGitConnectionRequest{Credential: "walk-pat"}, "walker"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetTeamRepoGitConnection(ctx, model.DefaultTeamID, "acme", "lib", ui.SetTeamRepoGitConnectionRequest{Credential: "default"}, "walker"); err != nil {
		t.Fatalf("re-pointing a pinned repository: %v", err)
	}
	if _, err := c.SetTeamRepoGitConnection(ctx, model.DefaultTeamID, "acme", "lib", ui.SetTeamRepoGitConnectionRequest{}, "walker"); err != nil {
		t.Fatalf("setting a pinned repository back to Default: %v", err)
	}
	team, err = c.GetTeam(ctx, model.DefaultTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if holdsConnection(team.GitConnections, "default", "acme/lib") || !holdsConnection(team.GitConnections, "walk-pat", "acme") {
		t.Fatalf("connections after re-pointing acme/lib: %+v", team.GitConnections)
	}
}

func holdsConnection(conns []model.TeamGitConnection, credential, scope string) bool {
	for _, conn := range conns {
		if conn.Credential == credential && conn.Scope == scope {
			return true
		}
	}
	return false
}
