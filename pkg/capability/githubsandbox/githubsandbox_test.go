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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// --- fakes -----------------------------------------------------------

// fakeRepo is one repo fakeAppClient tracks, standing in for a real
// GitHub org -- the same "no sandbox and no cloud" bar
// model/capability_test.go's own test capabilities hold to.
type fakeRepo struct {
	createdAt time.Time
}

type fakeAppClient struct {
	installation Installation
	findErr      error

	repos map[string]*fakeRepo // name -> repo
	now   time.Time

	createErr   error
	mintErr     error
	deleteErr   map[string]error // repo name -> error, for one bad delete
	listErr     error
	mintedRepos [][]string          // every MintToken call's repos argument, in order
	mintedPerms []map[string]string // every MintToken call's permissions argument, in order
}

func newFakeAppClient(account string) *fakeAppClient {
	return &fakeAppClient{
		installation: Installation{ID: 1, Account: account},
		repos:        map[string]*fakeRepo{},
		now:          time.Now(),
	}
}

func (f *fakeAppClient) FindInstallation(ctx context.Context) (Installation, error) {
	if f.findErr != nil {
		return Installation{}, f.findErr
	}
	return f.installation, nil
}

func (f *fakeAppClient) MintToken(ctx context.Context, installationID int64, repos []string, permissions map[string]string) (Token, error) {
	f.mintedRepos = append(f.mintedRepos, repos)
	f.mintedPerms = append(f.mintedPerms, permissions)
	if f.mintErr != nil {
		return Token{}, f.mintErr
	}
	scope := "installation"
	if len(repos) > 0 {
		scope = strings.Join(repos, ",")
	}
	return Token{Token: "token-for-" + scope, ExpiresAt: f.now.Add(time.Hour)}, nil
}

func (f *fakeAppClient) CreateRepo(ctx context.Context, token, org, name string) (Repo, error) {
	if f.createErr != nil {
		return Repo{}, f.createErr
	}
	if _, exists := f.repos[name]; exists {
		return Repo{}, fmt.Errorf("repo %s already exists", name)
	}
	f.repos[name] = &fakeRepo{createdAt: f.now}
	return Repo{Name: name, FullName: org + "/" + name}, nil
}

func (f *fakeAppClient) DeleteRepo(ctx context.Context, token, owner, name string) error {
	if err, ok := f.deleteErr[name]; ok {
		return err
	}
	delete(f.repos, name)
	return nil
}

func (f *fakeAppClient) ListRepos(ctx context.Context, token, org string) ([]RepoSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []RepoSummary
	for name, r := range f.repos {
		out = append(out, RepoSummary{Name: name, CreatedAt: r.createdAt})
	}
	return out, nil
}

// fakeCredentials resolves exactly the names it is seeded with,
// recording what was asked for -- CredentialResolver's own contract is
// "what a provider can reach is exactly what it names", so a test
// asserting on resolved is asserting that contract held.
type fakeCredentials struct {
	material map[string]string
	err      error
	resolved []string
}

func (c *fakeCredentials) Resolve(ctx context.Context, name string) (string, error) {
	c.resolved = append(c.resolved, name)
	if c.err != nil {
		return "", c.err
	}
	v, ok := c.material[name]
	if !ok {
		return "", errors.New("no such credential: " + name)
	}
	return v, nil
}

func testCredentials() *fakeCredentials {
	return &fakeCredentials{material: map[string]string{
		DefaultAppIDCredential:      "app-123",
		DefaultPrivateKeyCredential: "fake-key-material",
	}}
}

func testProvider(client *fakeAppClient) *Provider {
	p := &Provider{}
	p.newClient = func(ctx context.Context, appID, privateKeyPEM, host string, insecureHTTP bool) (appClient, error) {
		return client, nil
	}
	return p
}

func testContext(creds model.CredentialResolver, now time.Time) model.CapabilityContext {
	return model.CapabilityContext{
		Task:        model.Task{ID: "t1"},
		Run:         model.Run{ID: "t1-1", TaskID: "t1"},
		Now:         now,
		Credentials: creds,
	}
}

// --- Spec --------------------------------------------------------------

func TestSpec(t *testing.T) {
	p := NewProvider(Config{})
	spec := p.Spec()
	if spec.Name != "github-sandbox" {
		t.Errorf("Name = %q, want github-sandbox", spec.Name)
	}
	if spec.Provision != model.ProvisionMint {
		t.Errorf("Provision = %q, want mint", spec.Provision)
	}
	if spec.MaxLease != time.Hour {
		t.Errorf("MaxLease = %v, want 1h (GitHub's own installation-token ceiling)", spec.MaxLease)
	}
	want := []string{DefaultAppIDCredential, DefaultPrivateKeyCredential}
	if len(spec.Requires) != 2 || spec.Requires[0] != want[0] || spec.Requires[1] != want[1] {
		t.Errorf("Requires = %v, want %v", spec.Requires, want)
	}
}

func TestSpecHonoursConfiguredCredentialNames(t *testing.T) {
	p := NewProvider(Config{AppIDCredential: "custom/id", PrivateKeyCredential: "custom/key"})
	spec := p.Spec()
	if spec.Requires[0] != "custom/id" || spec.Requires[1] != "custom/key" {
		t.Errorf("Requires = %v, want the configured names", spec.Requires)
	}
}

// This used to assert the opposite, on the grounds that "Resolve has
// nothing deployment-specific to check" -- true of *config* (unlike
// gcpkey/geminikey, github-sandbox needs no ProjectID-shaped setting)
// but not of credentials, which are exactly deployment-specific and
// which this capability cannot work without. An empty context carries no
// resolver, so it now refuses; see Resolve's own doc comment. The
// honoured path is TestResolveHonoursAConfiguredApp below.
func TestResolveRefusesAnEmptyContext(t *testing.T) {
	p := NewProvider(Config{})
	res, err := p.Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Error("got Honoured for a context with no credentials, want Refused")
	}
}

// --- Materialize ---------------------------------------------------------

func TestMaterializeCreatesARepoAndMintsAScopedToken(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	p := testProvider(client)
	creds := testCredentials()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	m, err := p.Materialize(context.Background(), testContext(creds, now))
	if err != nil {
		t.Fatal(err)
	}

	wantRepo := "grain-sandbox-t1-1"
	if _, ok := client.repos[wantRepo]; !ok {
		t.Fatalf("repo %q was not created; have %v", wantRepo, client.repos)
	}

	wantResource := "grain-bot/" + wantRepo
	if m.Lease == nil || m.Lease.Resource != wantResource {
		t.Errorf("Lease.Resource = %+v, want %q", m.Lease, wantResource)
	}
	if m.Lease.Capability != "github-sandbox" {
		t.Errorf("Lease.Capability = %q", m.Lease.Capability)
	}
	if m.Lease.ExpiresAt == nil || !m.Lease.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("Lease.ExpiresAt = %v, want %v (1h ceiling)", m.Lease.ExpiresAt, now.Add(time.Hour))
	}

	if len(m.Placements) != 2 {
		t.Fatalf("got %d placements, want 2", len(m.Placements))
	}
	for _, pl := range m.Placements {
		if pl.Side != model.SideSandbox {
			t.Errorf("placement %s: Side = %q, want sandbox", pl.Path, pl.Side)
		}
	}

	// The token handed to the sandbox must be the one minted scoped to
	// exactly this repo, carrying agentPermissions -- never the
	// installation-wide creation token, and never administration.
	foundScopedMint := false
	for i, repos := range client.mintedRepos {
		if len(repos) == 1 && repos[0] == wantRepo {
			foundScopedMint = true
			perms := client.mintedPerms[i]
			if _, ok := perms["administration"]; ok {
				t.Errorf("agent token permissions include administration: %v", perms)
			}
			if _, ok := perms["organization_administration"]; ok {
				t.Errorf("agent token permissions include organization_administration: %v", perms)
			}
			if _, ok := perms["workflows"]; ok {
				t.Errorf("agent token permissions include workflows: %v", perms)
			}
			if perms["packages"] != "write" {
				t.Errorf("agent token permissions packages = %q, want write", perms["packages"])
			}
		}
	}
	if !foundScopedMint {
		t.Errorf("no token was minted scoped to exactly %q; minted repos: %v", wantRepo, client.mintedRepos)
	}
}

func TestMaterializeIsDeterministicInRunID(t *testing.T) {
	client1 := newFakeAppClient("grain-bot")
	client2 := newFakeAppClient("grain-bot")
	now := time.Now()

	m1, err := testProvider(client1).Materialize(context.Background(), testContext(testCredentials(), now))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := testProvider(client2).Materialize(context.Background(), testContext(testCredentials(), now))
	if err != nil {
		t.Fatal(err)
	}
	if m1.Lease.Resource != m2.Lease.Resource {
		t.Errorf("two Materialize calls for the same run.ID produced different repos: %q vs %q",
			m1.Lease.Resource, m2.Lease.Resource)
	}
}

func TestMaterializeFailsWhenTheAppHasNoSingleInstallation(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	client.findErr = errors.New("found 2 installations")
	p := testProvider(client)

	if _, err := p.Materialize(context.Background(), testContext(testCredentials(), time.Now())); err == nil {
		t.Fatal("want an error when the App's installation is ambiguous")
	}
}

func TestMaterializeResolvesBothCredentials(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	p := NewProvider(Config{})
	p.newClient = func(ctx context.Context, appID, privateKeyPEM, host string, insecureHTTP bool) (appClient, error) {
		if appID != "app-123" || privateKeyPEM != "fake-key-material" {
			t.Errorf("newClient got appID=%q privateKeyPEM=%q, want the resolved secrets", appID, privateKeyPEM)
		}
		return client, nil
	}
	creds := testCredentials()
	if _, err := p.Materialize(context.Background(), testContext(creds, time.Now())); err != nil {
		t.Fatal(err)
	}
	want := []string{DefaultAppIDCredential, DefaultPrivateKeyCredential}
	if len(creds.resolved) != 2 || creds.resolved[0] != want[0] || creds.resolved[1] != want[1] {
		t.Errorf("resolved = %v, want %v", creds.resolved, want)
	}
}

// --- PromptSection -------------------------------------------------------

func TestPromptSectionNamesBothPlacementPaths(t *testing.T) {
	p := NewProvider(Config{})
	text, err := p.PromptSection(context.Background(), model.CapabilityContext{}, []model.Placement{
		{Side: model.SideSandbox, Path: SandboxTokenPath, Content: "should-never-appear"},
		{Side: model.SideSandbox, Path: SandboxRepoPath, Content: "should-never-appear-either"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, SandboxTokenPath) || !strings.Contains(text, SandboxRepoPath) {
		t.Errorf("prompt text = %q, want it to name both placement paths", text)
	}
	if strings.Contains(text, "should-never-appear") {
		t.Errorf("prompt text leaked placement Content: %q", text)
	}
}

// This prompt is the only thing that tells a run how to reach the repo it
// was just given, so it may only name tools the sandbox actually has. It
// named `gh` for as long as it existed, and no grain sandbox image has
// ever carried one -- so its first line, `gh auth setup-git`, failed on
// every run that followed it, and the clone after it had no credential.
// docs/design.md's "The sandbox image has no `gh`" is why that is a fix
// here rather than a package added there.
func TestPromptSectionDrivesTheRepoWithToolsTheSandboxHas(t *testing.T) {
	p := NewProvider(Config{})
	text, err := p.PromptSection(context.Background(), model.CapabilityContext{}, []model.Placement{
		{Side: model.SideSandbox, Path: SandboxTokenPath},
		{Side: model.SideSandbox, Path: SandboxRepoPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "gh ") || strings.Contains(text, "GH_TOKEN") {
		t.Errorf("prompt tells the run to use a GitHub CLI the image has no copy of:\n%s", text)
	}
	// The two it does have, and the credential each carries.
	for _, want := range []string{
		"git clone \"https://x-access-token:$GITHUB_TOKEN@github.com/",
		"curl -sS -H \"Authorization: Bearer $GITHUB_TOKEN\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt does not show how to %q:\n%s", want, text)
		}
	}
}

func TestPromptSectionErrorsWithoutBothPlacements(t *testing.T) {
	p := NewProvider(Config{})
	if _, err := p.PromptSection(context.Background(), model.CapabilityContext{}, nil); err == nil {
		t.Fatal("want an error with no placements")
	}
}

// --- Revoke --------------------------------------------------------------

func TestRevokeDeletesTheRepo(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	client.repos["grain-sandbox-t1-1"] = &fakeRepo{createdAt: time.Now()}
	p := testProvider(client)

	lease := model.Lease{Capability: "github-sandbox", Resource: "grain-bot/grain-sandbox-t1-1"}
	if err := p.Revoke(context.Background(), testContext(testCredentials(), time.Now()), lease); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.repos["grain-sandbox-t1-1"]; ok {
		t.Errorf("repo was not deleted")
	}
}

func TestRevokeIsIdempotentAgainstAnAlreadyGoneRepo(t *testing.T) {
	client := newFakeAppClient("grain-bot") // no repo seeded
	p := testProvider(client)

	lease := model.Lease{Capability: "github-sandbox", Resource: "grain-bot/grain-sandbox-t1-1"}
	if err := p.Revoke(context.Background(), testContext(testCredentials(), time.Now()), lease); err != nil {
		t.Errorf("Revoke of an already-gone repo returned an error: %v", err)
	}
}

func TestRevokeRejectsAMalformedResource(t *testing.T) {
	p := testProvider(newFakeAppClient("grain-bot"))
	lease := model.Lease{Capability: "github-sandbox", Resource: "not-owner-slash-repo"}
	if err := p.Revoke(context.Background(), testContext(testCredentials(), time.Now()), lease); err == nil {
		t.Fatal("want an error for a lease resource with no owner/repo shape")
	}
}

// --- Reap ------------------------------------------------------------

func TestReapDeletesOnlyGrainSandboxReposBeforeBoundary(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	client.repos["grain-sandbox-old"] = &fakeRepo{createdAt: now.Add(-25 * time.Hour)}  // stale, ours: reap
	client.repos["grain-sandbox-fresh"] = &fakeRepo{createdAt: now.Add(-1 * time.Hour)} // ours, not stale: keep
	client.repos["someone-elses-repo"] = &fakeRepo{createdAt: now.Add(-48 * time.Hour)} // stale, not ours: keep
	p := testProvider(client)

	deleted, err := p.Reap(context.Background(), testCredentials(), now.Add(-10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "grain-bot/grain-sandbox-old" {
		t.Fatalf("deleted = %v, want exactly [grain-bot/grain-sandbox-old]", deleted)
	}
	if _, ok := client.repos["grain-sandbox-old"]; ok {
		t.Errorf("grain-sandbox-old was not actually deleted")
	}
	if _, ok := client.repos["grain-sandbox-fresh"]; !ok {
		t.Errorf("grain-sandbox-fresh should not have been deleted")
	}
	if _, ok := client.repos["someone-elses-repo"]; !ok {
		t.Errorf("someone-elses-repo should not have been deleted")
	}
}

func TestReapReportsOneBadDeleteAndContinues(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	client.repos["grain-sandbox-a"] = &fakeRepo{createdAt: now.Add(-25 * time.Hour)}
	client.repos["grain-sandbox-b"] = &fakeRepo{createdAt: now.Add(-25 * time.Hour)}
	client.deleteErr = map[string]error{"grain-sandbox-a": errors.New("transient")}
	p := testProvider(client)

	deleted, err := p.Reap(context.Background(), testCredentials(), now.Add(-10*time.Hour))
	if err == nil {
		t.Fatal("one failed delete should remain visible")
	}
	if len(deleted) != 1 || deleted[0] != "grain-bot/grain-sandbox-b" {
		t.Fatalf("deleted = %v, want exactly [grain-bot/grain-sandbox-b]", deleted)
	}
}

func TestReapMintsNoOverPrivilegedTokenForDeletion(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	client.repos["grain-sandbox-a"] = &fakeRepo{createdAt: now.Add(-25 * time.Hour)}
	p := testProvider(client)

	if _, err := p.Reap(context.Background(), testCredentials(), now.Add(-10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	foundScopedDeleteMint := false
	for i, repos := range client.mintedRepos {
		if len(repos) == 1 && repos[0] == "grain-sandbox-a" {
			foundScopedDeleteMint = true
			if perm := client.mintedPerms[i]["administration"]; perm != "write" {
				t.Errorf("delete token permissions = %v, want administration:write", client.mintedPerms[i])
			}
		}
	}
	if !foundScopedDeleteMint {
		t.Errorf("no token was minted scoped to exactly grain-sandbox-a; minted repos: %v", client.mintedRepos)
	}
}

// --- repoName ----------------------------------------------------------

func TestRepoNameSanitizesDisallowedCharacters(t *testing.T) {
	got := repoName("task/123 with spaces!")
	if strings.ContainsAny(got, "/ !") {
		t.Errorf("repoName(...) = %q, still contains disallowed characters", got)
	}
	if !strings.HasPrefix(got, RepoPrefix+"-") {
		t.Errorf("repoName(...) = %q, want it prefixed with %q", got, RepoPrefix+"-")
	}
}

func TestRepoNameStaysUnder100Characters(t *testing.T) {
	got := repoName(strings.Repeat("x", 200))
	if len(got) > 100 {
		t.Errorf("repoName(...) is %d characters, want <= 100", len(got))
	}
}

// --- Resolve -----------------------------------------------------------

// The provider registers on every deployment, whether or not one ever
// ran `grain controller bootstrap-github-app`. Without a resolve-time
// check the grant read as honoured and the run died later on a missing
// credential, naming neither the capability nor the fix.
func TestResolveRefusesWithoutAGitHubApp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds model.CredentialResolver
	}{
		{"no credentials at all", &fakeCredentials{material: map[string]string{}}},
		{"app id only", &fakeCredentials{material: map[string]string{DefaultAppIDCredential: "app-123"}}},
		{"private key only", &fakeCredentials{material: map[string]string{DefaultPrivateKeyCredential: "k"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProvider(Config{})
			res, err := p.Resolve(context.Background(), testContext(tc.creds, time.Now()))
			if err != nil {
				t.Fatalf("Resolve returned an error rather than a refusal: %v", err)
			}
			if !res.Refused {
				t.Fatal("Resolve honoured the grant with no GitHub App configured")
			}
			if !strings.Contains(res.Reason, "bootstrap-github-app") {
				t.Errorf("refusal reason does not name the fix: %q", res.Reason)
			}
		})
	}
}

// A nil resolver is a deployment wiring bug, not a task's fault -- still
// a refusal rather than a panic one layer up.
func TestResolveRefusesWithNoResolver(t *testing.T) {
	p := NewProvider(Config{})
	res, err := p.Resolve(context.Background(), testContext(nil, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Error("Resolve honoured the grant with no credential resolver")
	}
}

func TestResolveHonoursAConfiguredApp(t *testing.T) {
	p := NewProvider(Config{})
	res, err := p.Resolve(context.Background(), testContext(testCredentials(), time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused {
		t.Errorf("Resolve refused a fully configured App: %s", res.Reason)
	}
}

// --- Reap on a deployment with no App ----------------------------------

// The daemon sweeps every registered provider hourly, and this one
// registers everywhere. Erroring here logged
// `reaping capability "github-sandbox": ...` every hour forever on any
// deployment that never ran bootstrap-github-app -- a recurring error
// nobody can act on. There is genuinely nothing to reap: no App means no
// sandbox repos were ever created.
func TestReapIsQuietWithNoApp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds model.CredentialResolver
	}{
		{"no credentials at all", &fakeCredentials{material: map[string]string{}}},
		{"app id only", &fakeCredentials{material: map[string]string{DefaultAppIDCredential: "app-123"}}},
		{"nil resolver", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProvider(Config{})
			deleted, err := p.Reap(context.Background(), tc.creds, time.Now())
			if err != nil {
				t.Errorf("Reap returned an error for an unconfigured deployment: %v", err)
			}
			if len(deleted) != 0 {
				t.Errorf("Reap reported %d deletions with no App configured", len(deleted))
			}
		})
	}
}

// A configured App that fails is a real problem with real leaked repos
// behind it, and must not be swallowed by the quiet path above.
func TestReapStillErrorsWhenTheAppIsConfiguredButFailing(t *testing.T) {
	p := &Provider{}
	p.newClient = func(ctx context.Context, appID, privateKeyPEM, host string, insecureHTTP bool) (appClient, error) {
		return nil, errors.New("github is unreachable")
	}
	if _, err := p.Reap(context.Background(), testCredentials(), time.Now()); err == nil {
		t.Fatal("expected a configured-but-failing App to surface its error")
	}
}

// --- CheckCredential ---------------------------------------------------

// The App's private key is a standing credential like gcp-key's minter,
// and goes stale the same invisible way -- so the check has to come back
// with what GitHub actually said, not with a repeat of the **Ready**
// badge that was already true while every dispatch failed.
func TestCheckCredentialReportsTheInstallation(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	p := testProvider(client)
	creds := testCredentials()

	check, err := p.CheckCredential(context.Background(), creds)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if !strings.Contains(check.Detail, "grain-bot") {
		t.Errorf("detail %q does not name the account the App is installed on", check.Detail)
	}
	// Both halves of the App credential are named: a refusal's remedy is
	// pasting a fresh private key beside the app id it belongs to.
	want := []string{DefaultAppIDCredential, DefaultPrivateKeyCredential}
	if len(check.Credentials) != 2 || check.Credentials[0] != want[0] || check.Credentials[1] != want[1] {
		t.Errorf("Credentials = %v, want %v", check.Credentials, want)
	}
	// A read and nothing else: no token minted, no repo created. This is
	// a button an operator presses whenever they doubt the deployment.
	if len(client.mintedRepos) != 0 || len(client.repos) != 0 {
		t.Errorf("the check minted or created something: tokens=%v repos=%v", client.mintedRepos, client.repos)
	}
}

func TestCheckCredentialExplainsAnAppGitHubWontAnswerFor(t *testing.T) {
	client := newFakeAppClient("grain-bot")
	client.findErr = errors.New("401 Bad credentials")
	p := testProvider(client)

	check, err := p.CheckCredential(context.Background(), testCredentials())
	if err == nil {
		t.Fatal("expected a refused App credential to fail the check")
	}
	if !strings.Contains(err.Error(), DefaultPrivateKeyCredential) {
		t.Errorf("error %q does not name the secret holding the key GitHub refused", err)
	}
	if !strings.Contains(err.Error(), "401 Bad credentials") {
		t.Errorf("error %q dropped GitHub's own message", err)
	}
	if len(check.Credentials) != 2 {
		t.Errorf("Credentials = %v, want both named even on failure", check.Credentials)
	}
}

// An App nobody ever configured is a different answer from one GitHub
// refuses, and says the thing that can be done about it.
func TestCheckCredentialSaysWhenNoAppIsConfigured(t *testing.T) {
	p := NewProvider(Config{})
	_, err := p.CheckCredential(context.Background(), &fakeCredentials{material: map[string]string{}})
	if err == nil {
		t.Fatal("expected a check with no App configured to fail")
	}
	if !strings.Contains(err.Error(), "bootstrap-github-app") {
		t.Errorf("error %q does not name the command that configures one", err)
	}
}

func TestProviderSatisfiesCredentialChecker(t *testing.T) {
	var _ model.CredentialChecker = (*Provider)(nil)
}

func TestAgentPermissionsCarriesPackagesWrite(t *testing.T) {
	t.Parallel()
	if got := agentPermissions["packages"]; got != "write" {
		t.Errorf("agentPermissions[packages] = %q, want write", got)
	}
}
