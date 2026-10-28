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

package githubtoken

// The credential check, offline: a fake ladder for the material and a
// github.FakeTransport for GitHub, so every case here -- a live token, a
// revoked one, a token that has lost a repo -- is exercised with nothing
// on the network and no credential anywhere near it, the same bar the
// rest of pkg/capability's own tests hold to.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A named token is a standing credential like any other, and this is the
// interface Settings' "test this credential" action reaches it through.
func TestProviderSatisfiesCredentialChecker(t *testing.T) {
	var _ model.CredentialChecker = (*Provider)(nil)
}

// fakeLadder is githubtoken.CredentialSource: the credential ladder,
// with the two file forms it resolves reduced to what the check actually
// asks it for.
type fakeLadder map[string]Credential

func (f fakeLadder) GitHubCredential(name string) (Credential, bool) {
	cred, ok := f[name]
	return cred, ok
}

// fixedRepos is a Config.Repos answering repos on every call.
func fixedRepos(repos ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return repos, nil }
}

// checkProvider is a provider wired to ladder and transport, with a host
// named so the sentences it writes can be asserted on.
func checkProvider(cfg Config, transport github.Transport) *Provider {
	cfg.Host = "github.example"
	p := New("release-bot", cfg)
	p.newTransport = func(string, bool) github.Transport { return transport }
	return p
}

func jsonResponse(t *testing.T, status int, body any) github.ApiResponse {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return github.ApiResponse{Status: status, Body: data}
}

// rateLimitOK is GitHub's own /rate_limit shape, cut down to the bucket
// the check reads.
func rateLimitOK(t *testing.T, remaining, limit int) github.ApiResponse {
	t.Helper()
	return jsonResponse(t, 200, map[string]any{
		"resources": map[string]any{
			"core": map[string]any{"remaining": remaining, "limit": limit},
		},
	})
}

func repoOK(t *testing.T, push bool) github.ApiResponse {
	t.Helper()
	return jsonResponse(t, 200, map[string]any{"permissions": map[string]any{"push": push}})
}

// The ordinary answer: GitHub accepted the token, and the detail is the
// evidence for that rather than a bare "ok" -- how much of this hour's
// budget is left, which is what says a credential was read at all.
func TestCheckReportsWhatGitHubAnswered(t *testing.T) {
	transport := github.NewFakeTransport(rateLimitOK(t, 4999, 5000))
	p := checkProvider(Config{Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}}}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	for _, want := range []string{"github.example", "release-bot", "4999", "5000"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", check.Detail, want)
		}
	}
	if len(check.Credentials) != 1 || check.Credentials[0] != "secrets/github/release-bot.token" {
		t.Errorf("Credentials = %v, want the file the token lives in", check.Credentials)
	}
	// One call, and a read: a button somebody is expected to press
	// repeatedly must not mint or write anything (model.CredentialChecker).
	if len(transport.Calls) != 1 {
		t.Fatalf("made %d calls, want one: %+v", len(transport.Calls), transport.Calls)
	}
	call := transport.Calls[0]
	if call.Method != "GET" || call.Path != "/rate_limit" {
		t.Errorf("call = %s %s, want GET /rate_limit", call.Method, call.Path)
	}
	if call.Headers["Authorization"] != "token ghp_live" {
		t.Errorf("Authorization = %q, want the token the ladder resolved", call.Headers["Authorization"])
	}
}

// The failure this whole check exists for: the file is still there, so
// every pane says Ready, and GitHub has stopped accepting what is in it.
// The sentence has to name that file and where a current value is
// pasted, not repeat GitHub's 401.
func TestCheckExplainsARevokedToken(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)})
	p := checkProvider(Config{Credentials: fakeLadder{"release-bot": {Token: "ghp_dead"}}}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a token GitHub refuses to be reported as refused")
	}
	for _, want := range []string{"secrets/github/release-bot.token", "revoked", "Settings -> GitHub tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(check.Credentials) != 1 || check.Credentials[0] != "secrets/github/release-bot.token" {
		t.Errorf("Credentials = %v, want the credential named even on the failure path", check.Credentials)
	}
}

// A *.app.json credential fails the same way and is fixed somewhere
// else: SetToken refuses to write over one, so telling an operator to
// paste a token into Settings would be telling them to do something
// grain would then refuse.
func TestCheckExplainsARefusedAppCredentialAsAFileToReplace(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 401})
	p := checkProvider(Config{Credentials: fakeLadder{"release-bot": {Token: "ghs_dead", App: true}}}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a refused App credential to be reported as refused")
	}
	if !strings.Contains(err.Error(), "secrets/github/release-bot.app.json") {
		t.Errorf("error %q does not name the App credential file to replace", err)
	}
	if len(check.Credentials) != 1 || check.Credentials[0] != "secrets/github/release-bot.app.json" {
		t.Errorf("Credentials = %v, want the App credential file", check.Credentials)
	}
}

// The more useful failure, and the reason the check does not stop at
// /rate_limit: the token is alive, and what it has lost is the access it
// exists for. Reported per repo, since "it can push to one and cannot
// see the other" is what an operator has to act on.
func TestCheckReportsWhichTargetReposTheTokenReaches(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoOK(t, true),
		repoOK(t, false),
		github.ApiResponse{Status: 404},
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/site", "acme/docs", "acme/secrets"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	for _, want := range []string{
		"can push to acme/site",
		"read but not push to acme/docs",
		"cannot see acme/secrets",
		"no access",
	} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("Detail = %q, want it to say %q", check.Detail, want)
		}
	}
	if len(transport.Calls) != 4 {
		t.Fatalf("made %d calls, want one per repo after the rate limit: %+v", len(transport.Calls), transport.Calls)
	}
	if got := transport.Calls[1].Path; got != "/repos/acme/site" {
		t.Errorf("second call = %q, want the repo lookup", got)
	}
}

// A token that reaches none of them is the exception to "reach is
// evidence, not a verdict": it is live and useless for anything any task
// on this deployment could ask of it, which is the same news to an
// operator as a dead token.
func TestCheckRefusesATokenThatReachesNoTargetRepo(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		github.ApiResponse{Status: 404},
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	_, err := p.CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a token that can reach no target repo to be reported as refused")
	}
	for _, want := range []string{"acme/site", "still accepts", "lost the access"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A token deliberately scoped to one repo out of several is doing its
// job. Failing the check for the repos it was never meant to reach would
// leave a correctly-scoped token permanently red, so it passes and says
// what it cannot see.
func TestCheckPassesATokenThatReachesSomeRepos(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		github.ApiResponse{Status: 404},
		repoOK(t, true),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/other", "acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential on a narrowly scoped token: %v", err)
	}
	if !strings.Contains(check.Detail, "can push to acme/site") ||
		!strings.Contains(check.Detail, "cannot see acme/other") {
		t.Errorf("Detail = %q, want both what it reaches and what it does not", check.Detail)
	}
}

// A deployment that names no target repos (the single-repo default) has
// named nothing to hold the token against, so the check reports liveness
// alone rather than guessing at a repo.
func TestCheckWithNoTargetReposReportsLivenessOnly(t *testing.T) {
	transport := github.NewFakeTransport(rateLimitOK(t, 10, 5000))
	p := checkProvider(Config{Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}}}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if len(transport.Calls) != 1 {
		t.Errorf("made %d calls, want only the rate limit: %+v", len(transport.Calls), transport.Calls)
	}
	if strings.Contains(check.Detail, "cannot see") {
		t.Errorf("Detail = %q, want nothing said about repos nobody named", check.Detail)
	}
}

// Bounded: a check is a button, and every repo past the bound costs
// another round trip for an answer the ones before it already gave. What
// was left out is said rather than silently dropped.
func TestCheckBoundsHowManyReposItLooksUp(t *testing.T) {
	responses := []github.ApiResponse{rateLimitOK(t, 4999, 5000)}
	var repos []string
	for i := 0; i < maxCheckedRepos+3; i++ {
		responses = append(responses, repoOK(t, true))
		repos = append(repos, "acme/repo"+string(rune('a'+i)))
	}
	transport := github.NewFakeTransport(responses...)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos(repos...),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if want := maxCheckedRepos + 1; len(transport.Calls) != want {
		t.Errorf("made %d calls, want %d (the rate limit plus %d repos)", len(transport.Calls), want, maxCheckedRepos)
	}
	if !strings.Contains(check.Detail, "3 further target repo(s) were not checked") {
		t.Errorf("Detail = %q, want it to say what was left unchecked", check.Detail)
	}
}

// The ladder serves an empty *.token file, and a *.app.json it cannot
// mint from, as anonymous -- a real state, and one where every push goes
// out unauthenticated. That is an answer, not a network error, so it is
// reported without a call being made at all.
func TestCheckReportsACredentialWithNoTokenBehindIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		cred Credential
		want string
	}{
		{name: "an empty token file", cred: Credential{}, want: "secrets/github/release-bot.token"},
		{name: "an App that will not mint", cred: Credential{App: true}, want: "secrets/github/release-bot.app.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := github.NewFakeTransport()
			p := checkProvider(Config{Credentials: fakeLadder{"release-bot": tc.cred}}, transport)

			_, err := p.CheckCredential(context.Background(), nil)
			if err == nil {
				t.Fatal("expected a credential with no token behind it to be reported")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "unauthenticated") {
				t.Errorf("error %q does not say which file resolves to nothing", err)
			}
			if len(transport.Calls) != 0 {
				t.Errorf("made %d calls with nothing to authenticate as", len(transport.Calls))
			}
		})
	}
}

// And where the ladder kept GitHub's own reason for refusing the token
// exchange (grain/task-158), the check says it: "suspended", "Integration
// not found" and a key that will not decode are three different fixes,
// and telling an operator to go and read the daemon's log is a remedy
// that needs the shell access this check exists to avoid.
func TestCheckQuotesWhyAnAppCredentialWouldNotMint(t *testing.T) {
	transport := github.NewFakeTransport()
	p := checkProvider(Config{Credentials: fakeLadder{"release-bot": {
		App:       true,
		MintError: `GitHub answered 403 to the token exchange: "This installation has been suspended"`,
	}}}, transport)

	_, err := p.CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an App credential with no token behind it to be reported")
	}
	if !strings.Contains(err.Error(), "This installation has been suspended") {
		t.Errorf("error %q drops the one fact only GitHub knows", err)
	}
	if len(transport.Calls) != 0 {
		t.Errorf("made %d calls with nothing to authenticate as", len(transport.Calls))
	}
}

// A token removed from the ladder since this process started still has a
// picker row and a provider until a restart (the ladder is not
// hot-reloaded), so the check has to have an answer for one.
func TestCheckSaysWhenTheCredentialIsGone(t *testing.T) {
	transport := github.NewFakeTransport()
	p := checkProvider(Config{Credentials: fakeLadder{}}, transport)

	_, err := p.CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a check against a removed credential to say so")
	}
	if !strings.Contains(err.Error(), "release-bot") || !strings.Contains(err.Error(), "restart") {
		t.Errorf("error %q does not explain that the credential is gone", err)
	}
	if len(transport.Calls) != 0 {
		t.Errorf("made %d calls with no credential to make them as", len(transport.Calls))
	}
}

// A process holding no ladder at all cannot answer the question, and
// says that -- rather than reporting the token itself refused, which is
// a different thing entirely.
func TestCheckSaysWhenThisProcessHasNoLadder(t *testing.T) {
	_, err := New("release-bot", Config{}).CheckCredential(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a provider with no credential source to refuse to guess")
	}
	if !strings.Contains(err.Error(), "no GitHub credential ladder") {
		t.Errorf("error %q does not say why the check could not be made", err)
	}
}

func rateLimitWithHeaders(t *testing.T, remaining, limit int, headers map[string]string) github.ApiResponse {
	t.Helper()
	resp := rateLimitOK(t, remaining, limit)
	resp.Headers = headers
	return resp
}

func repoWithHeaders(t *testing.T, push bool, headers map[string]string) github.ApiResponse {
	t.Helper()
	resp := repoOK(t, push)
	resp.Headers = headers
	return resp
}

func repoWithPackages(t *testing.T, push bool, packages any) github.ApiResponse {
	t.Helper()
	return jsonResponse(t, 200, map[string]any{
		"permissions": map[string]any{
			"push":     push,
			"packages": packages,
		},
	})
}

// A PAT with the write:packages OAuth scope reports that it can publish packages to target repos.
func TestCheckReportsPackagePublishWithOAuthScope(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitWithHeaders(t, 4999, 5000, map[string]string{"X-OAuth-Scopes": "repo, write:packages"}),
		repoOK(t, true),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if !strings.Contains(check.Detail, "can publish packages to acme/site") {
		t.Errorf("Detail = %q, want it to report package publishing permission", check.Detail)
	}
}

// A PAT without the write:packages OAuth scope reports diagnostic feedback explaining what is missing.
func TestCheckReportsMissingPackagePublishScopeForPAT(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitWithHeaders(t, 4999, 5000, map[string]string{"X-OAuth-Scopes": "repo, gist"}),
		repoOK(t, true),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	for _, want := range []string{"cannot publish packages", "missing 'write:packages' scope on personal access token"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", check.Detail, want)
		}
	}
}

// A GitHub App installation with packages: write permission reports that it can publish packages.
func TestCheckReportsPackagePublishWithGitHubAppPermissions(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoWithHeaders(t, true, map[string]string{"X-GitHub-Permissions": "packages=write"}),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghs_live", App: true}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if !strings.Contains(check.Detail, "can publish packages to acme/site") {
		t.Errorf("Detail = %q, want it to report package publishing permission", check.Detail)
	}
}

// A GitHub App installation with packages: write in JSON permissions body reports package publishing.
func TestCheckReportsPackagePublishWithJSONPermissionsBody(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoWithPackages(t, true, "write"),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghs_live", App: true}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if !strings.Contains(check.Detail, "can publish packages to acme/site") {
		t.Errorf("Detail = %q, want it to report package publishing permission", check.Detail)
	}
}

// A GitHub App installation missing packages: write reports diagnostic feedback for the App.
func TestCheckReportsMissingPackagePublishPermissionForGitHubApp(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoOK(t, true),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghs_live", App: true}},
		Repos:       fixedRepos("acme/site"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	for _, want := range []string{"cannot publish packages", "missing 'packages: write' permission on the GitHub App installation"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", check.Detail, want)
		}
	}
}

// When some repos permit package publishing and others do not, the check distinguishes them.
func TestCheckReportsMixedPackagePublishAccessAcrossRepos(t *testing.T) {
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoWithHeaders(t, true, map[string]string{"X-OAuth-Scopes": "write:packages"}),
		repoOK(t, true),
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       fixedRepos("acme/site", "acme/other"),
	}, transport)

	check, err := p.CheckCredential(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if !strings.Contains(check.Detail, "can publish packages to acme/site") {
		t.Errorf("Detail = %q, want package publish access for acme/site", check.Detail)
	}
	if !strings.Contains(check.Detail, "cannot publish packages to acme/other") {
		t.Errorf("Detail = %q, want package publish refusal for acme/other", check.Detail)
	}
}

// Repos is asked on every check rather than once when the provider is
// built: teams gain and lose repos while the daemon runs, and the check
// holds the token against the repos they own when it is pressed.
func TestCheckReadsReposPerCheck(t *testing.T) {
	owned := []string{"acme/site"}
	transport := github.NewFakeTransport(
		rateLimitOK(t, 4999, 5000),
		repoOK(t, true),
		rateLimitOK(t, 4998, 5000),
		github.ApiResponse{Status: 404},
	)
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       func(context.Context) ([]string, error) { return owned, nil },
	}, transport)

	if _, err := p.CheckCredential(context.Background(), nil); err != nil {
		t.Fatalf("first CheckCredential: %v", err)
	}
	owned = []string{"acme/moved"}
	_, err := p.CheckCredential(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "acme/moved") {
		t.Fatalf("second CheckCredential = %v, want a refusal naming the repo the teams own now", err)
	}
}

// A teams lookup that fails fails the check with it, rather than
// passing the token on liveness alone as if no team owned a repo.
func TestCheckReportsAReposLookupFailure(t *testing.T) {
	transport := github.NewFakeTransport(rateLimitOK(t, 4999, 5000))
	p := checkProvider(Config{
		Credentials: fakeLadder{"release-bot": {Token: "ghp_live"}},
		Repos:       func(context.Context) ([]string, error) { return nil, errors.New("database is locked") },
	}, transport)

	_, err := p.CheckCredential(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("CheckCredential = %v, want the lookup's own error", err)
	}
}
