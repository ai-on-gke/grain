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

package gitproxy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A credential on a plain git host, or recorded on another GitHub, is
// never listed against the deployment's own API: in the 10-15 walkthrough
// (docs/test-runs/2026-10-15-ambient-git-credentials.md) the plain host's
// password went to the GitHub API as "Authorization: token ...".
func TestReachNeverSendsAnotherHostsTokenToTheAPI(t *testing.T) {
	t.Parallel()
	reach, creds, sim, _ := reachFixture(t)
	reach.Host = "github.com"
	if err := creds.SetToken("plain", "plain-password"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("plain", gitproxy.CredentialHost{Host: "127.0.0.1:8080", Kind: model.GitHostGit,
		BaseURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetToken("ghe", "ghe-token"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("ghe", gitproxy.CredentialHost{Host: "ghe.example", Kind: model.GitHostGitHub}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plain", "ghe"} {
		if got, err := reach.Check(name, "team", "notes", true); got != gitproxy.ReachUnknown || err != nil {
			t.Errorf("Check(%s) = %v, %v; want unknown with nothing asked", name, got, err)
		}
	}
	for _, call := range sim.Calls {
		t.Errorf("the API was asked %s %s with another host's credential", call.Method, call.Path)
	}
	// The API's own host is still listed.
	if got, _ := reach.Check("bouncer", "gke-labs", "bouncer", true); got != gitproxy.Reachable {
		t.Errorf("Check(bouncer) = %v; want reachable", got)
	}
}

// teamsOnHosts is a store whose team has a connection on one host only.
type teamsOnHosts map[string]string

func (t teamsOnHosts) TeamGitCredential(ctx context.Context, team, owner, repo string) (string, error) {
	return t.TeamGitCredentialOn(ctx, team, "", owner, repo)
}

func (t teamsOnHosts) TeamGitCredentialOn(ctx context.Context, team, host, owner, repo string) (string, error) {
	if name, ok := t[host]; ok {
		return name, nil
	}
	return "", model.ErrNoTeamGitCredential
}

// A task on a plain git host is checked against the team's connection on
// that host: the walkthrough's default team had one only there, and every
// task on it was refused for "no GitHub credential".
func TestReachRefusalAsksForTheConnectionOnTheRepositorysHost(t *testing.T) {
	t.Parallel()
	reach, creds, _, _ := reachFixture(t)
	if err := creds.SetToken("plain", "plain-password"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("plain", gitproxy.CredentialHost{Host: "git.example:8080", Kind: model.GitHostGit}); err != nil {
		t.Fatal(err)
	}
	teams := teamsOnHosts{"git.example:8080": "plain"}
	target := model.RepoRef{Host: "git.example:8080", Owner: "team", Name: "notes"}
	if refusal, err := reach.Refusal(t.Context(), teams, "default", &target, nil, nil); err != nil || refusal != "" {
		t.Errorf("Refusal on the team's own host = %q, %v; want none", refusal, err)
	}
	other := model.RepoRef{Host: "git.other", Owner: "team", Name: "notes"}
	refusal, err := reach.Refusal(t.Context(), teams, "default", &other, nil, nil)
	if err != nil || !strings.Contains(refusal, `the deployment has no git connection on git.other`) {
		t.Errorf("Refusal on a host the team has no connection on = %q, %v", refusal, err)
	}
	if strings.Contains(refusal, "GitHub") || !strings.Contains(refusal, "add one in Settings → Git") {
		t.Errorf("Refusal on a host with no connection mentions GitHub or does not say to add one: %q", refusal)
	}
}

func TestReachRefusalWordingDependsOnTheHostsKind(t *testing.T) {
	t.Parallel()
	reach, creds, _, _ := reachFixture(t)
	if err := creds.SetToken("plain", "plain-password"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("plain", gitproxy.CredentialHost{Host: "127.0.0.1:8088", Kind: model.GitHostGit,
		BaseURL: "http://127.0.0.1:8088"}); err != nil {
		t.Fatal(err)
	}
	teams := teamsOnHosts{"": "bouncer"}

	// 1. Before any connection on 127.0.0.1:8088, filing on that host says
	// to add one in Settings -> Git, and never says "on GitHub".
	plainRepo := model.RepoRef{Host: "127.0.0.1:8088", Owner: "walk", Name: "demo"}
	refusal, err := reach.Refusal(t.Context(), teams, "default", &plainRepo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refusal, "the deployment has no git connection on 127.0.0.1:8088") ||
		!strings.Contains(refusal, "add one in Settings → Git") ||
		strings.Contains(refusal, "GitHub") {
		t.Errorf("Refusal before a connection on 127.0.0.1:8088 = %q", refusal)
	}

	// 2. On GitHub, an unreachable repository names the GitHub credential
	// and says "give the credential access on GitHub".
	ghRepo := model.RepoRef{Owner: "acme", Name: "private"}
	ghRefusal, err := reach.Refusal(t.Context(), teams, "default", &ghRepo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`the deployment's GitHub credential "bouncer"`, "give the credential access on GitHub"} {
		if !strings.Contains(ghRefusal, want) {
			t.Errorf("GitHub refusal = %q, want %q", ghRefusal, want)
		}
	}
}
