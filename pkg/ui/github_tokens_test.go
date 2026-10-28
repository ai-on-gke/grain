package ui_test

// grain/task-117: a deployment's extra named GitHub tokens are
// capabilities like any other -- offered by the picker, attachable to a
// task, reportable on Settings' Capabilities tab -- and the point of
// giving each one an id rather than building a second, token-shaped
// pane is that none of the machinery in between has to know they exist.
// These tests are that claim, from the offered row through to the
// override the git proxy reads back off the task.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// withTokenCapabilities is testClient with the picker listing a real
// deployment would have on top of the fixed rows: one per named token
// pkg/cli/daemon.go found under secrets/github.
func withTokenCapabilities(t *testing.T, names ...string) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	c, store, ctx := testClient(t)
	c.Config.Capabilities = append(ui.OfferedCapabilities(), ui.GitHubTokenCapabilities(names)...)
	return c, store, ctx
}

func TestGitHubTokenCapabilitiesIsOneRowPerNamedToken(t *testing.T) {
	t.Parallel()
	got := ui.GitHubTokenCapabilities([]string{"release-bot", "", "docs-bot"})
	want := []ui.Capability{
		{ID: "github-credential:release-bot", Name: "GitHub token: release-bot",
			Description: got[0].Description},
		{ID: "github-credential:docs-bot", Name: "GitHub token: docs-bot",
			Description: got[1].Description},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GitHubTokenCapabilities = %+v, want %+v (an empty name is skipped)", got, want)
	}
	for _, capability := range got {
		if capability.Description == "" {
			t.Errorf("%s: no description for the picker to show", capability.ID)
		}
	}
}

func TestGitHubTokenCapabilitiesAreNoneWithoutExtraTokens(t *testing.T) {
	t.Parallel()
	if got := ui.GitHubTokenCapabilities(nil); len(got) != 0 {
		t.Errorf("GitHubTokenCapabilities(nil) = %+v, want none -- a deployment with only a default token offers no rows", got)
	}
}

// The whole path a human takes: tick the token on a task, and the git
// proxy resolving that task's sandbox is told to use it in place of the
// owner/repo ladder.
func TestAttachingATokenCapabilityOverridesTheSandboxCredential(t *testing.T) {
	t.Parallel()
	c, store, ctx := withTokenCapabilities(t, "release-bot")
	task := create(t, c, ctx)

	if err := c.SetCapability(ctx, task.ID, "github-credential:release-bot", true); err != nil {
		t.Fatalf("attaching the token capability: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "sandbox-0", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "bwsalmon", "grain")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "release-bot" {
		t.Fatalf("GitCredentialOverride = %q, %v, want %q true", name, ok, "release-bot")
	}

	// Detaching puts the sandbox back on its team's own credential --
	// and, for a team with none, on nothing at all: there is no
	// deployment ladder for a team's work to fall back to (grain/task-1592).
	if err := c.SetCapability(ctx, task.ID, "github-credential:release-bot", false); err != nil {
		t.Fatalf("detaching the token capability: %v", err)
	}
	if _, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "bwsalmon", "grain"); !errors.Is(err, model.ErrNoTeamGitCredential) || ok {
		t.Fatalf("GitCredentialOverride ok=%v err=%v, want false, ErrNoTeamGitCredential after detaching", ok, err)
	}
}

// A token this deployment does not have is rejected the same way any
// other unknown capability id is -- the picker listing is still the one
// gate every grant passes through.
func TestAttachingAnUnconfiguredTokenIsRejected(t *testing.T) {
	t.Parallel()
	c, _, ctx := withTokenCapabilities(t, "release-bot")
	task := create(t, c, ctx)
	if err := c.SetCapability(ctx, task.ID, "github-credential:not-configured", true); err == nil {
		t.Fatal("expected attaching a token this deployment has no credential for to be rejected")
	}
}

func TestSettingsReportsEachNamedTokenAsAReadyCapability(t *testing.T) {
	t.Parallel()
	c, _, ctx := withTokenCapabilities(t, "release-bot")

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := capabilityStatus(t, got.Capabilities, "github-credential:release-bot")
	if !status.Ready || !status.Grantable {
		t.Fatalf("release-bot: Ready=%t Grantable=%t, want both true (status: %+v)",
			status.Ready, status.Grantable, status)
	}
	if status.Name != "GitHub token: release-bot" {
		t.Errorf("Name = %q, want the same name the picker shows", status.Name)
	}
	if status.Default {
		t.Errorf("Default = true, want false -- nothing has defaulted this token (status: %+v)", status)
	}
}

// Defaulting one deployment-wide goes through the same validation and
// the same reporting as every other capability id.
func TestANamedTokenCanBeADeploymentDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := withTokenCapabilities(t, "release-bot")
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	defaults := []string{"github-credential:release-bot"}
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &defaults})
	if err != nil {
		t.Fatalf("defaulting a named token: %v", err)
	}
	if !slices.Equal(got.DefaultCapabilities, defaults) {
		t.Fatalf("defaultCapabilities = %v, want %v", got.DefaultCapabilities, defaults)
	}
	if status := capabilityStatus(t, got.Capabilities, "github-credential:release-bot"); !status.Default {
		t.Errorf("Default = false on a token every new task is filed with (status: %+v)", status)
	}

	// And a task filed afterwards really does carry it, which is what
	// makes the git proxy use that token for its pushes.
	task := create(t, c, ctx)
	if !slices.Contains(task.Capabilities, "github-credential:release-bot") {
		t.Fatalf("a task filed with the token defaulted holds %v", task.Capabilities)
	}
}

// --- adding and removing a named token from the UI --------------------
//
// grain/task-137: task-117 above made a named token grantable; this half
// makes one definable without shell access to the host. The store behind
// these endpoints is the credential ladder's own directory
// (pkg/gitproxy), not the secrets database -- see that package's doc
// comment for why -- so these assert on the files a git proxy would
// actually read back.

// gitHubTokenServer is a Server fronting a real credential directory:
// patterns become credentials.json, tokens become <name>.token files,
// and offered is the named tokens this "process" started with as
// capabilities (what pkg/cli/daemon.go computes once at startup).
func gitHubTokenServer(t *testing.T, patterns, tokens map[string]string, offered ...string) (*ui.Server, *gitproxy.CredentialSet, string) {
	t.Helper()
	dir := t.TempDir()
	for name, token := range tokens {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	credentials, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, _, ctx := testClient(t)
	storeSettingsRow(t, client)
	// patterns are the deployment's git connections, spelled the way the
	// old ladder spelled them: "*" is the default host's default.
	if len(patterns) > 0 {
		keys := make([]string, 0, len(patterns))
		for k := range patterns {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if err := client.Store.UpdateConfig(ctx, func(team *model.Config) error {
			for _, k := range keys {
				scope := k
				if scope == "*" {
					scope = ""
				}
				team.GitConnections = append(team.GitConnections, model.TeamGitConnection{Credential: patterns[k], Scope: scope})
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	client.Config.Credentials = credentials
	client.Config.Capabilities = append(ui.OfferedCapabilities(), ui.GitHubTokenCapabilities(offered)...)
	return ui.NewServerWithClient(client), credentials, dir
}

// tokenBody decodes a github-tokens response loosely, so these tests
// read the JSON an actual browser gets rather than a Go struct this
// package could rename out from under it. Decoded once per response --
// decode drains the recorder's body, so a second call would see nothing.
func tokenBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return decode[map[string]any](t, rec)
}

// tokenRow is one credential's entry in that body.
func tokenRow(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	rows, _ := body["tokens"].([]any)
	for _, row := range rows {
		row, _ := row.(map[string]any)
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("no row for %q in %v", name, body)
	return nil
}

func TestGitHubTokensUnavailableWithoutACredentialLadder(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)

	rec := do(t, srv, http.MethodGet, "/api/github-tokens", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["enabled"] != false {
		t.Fatalf("enabled = %v, want false -- this UI has no credential directory", got["enabled"])
	}
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/github-tokens/release-bot", `{"value":"ghp-fake"}`},
		{http.MethodDelete, "/api/github-tokens/release-bot", ""},
	} {
		rec := do(t, srv, call.method, call.path, call.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404: %s", call.method, call.path, rec.Code, rec.Body)
		}
	}
}

// The whole point of the change: a token added from the UI lands exactly
// where an operator's own file would have, and says out loud that the
// daemon has to restart before it can be ticked on a task.
func TestAddingATokenWritesTheFileTheProxyReads(t *testing.T) {
	t.Parallel()
	srv, credentials, dir := gitHubTokenServer(t,
		map[string]string{"*": "bot"}, map[string]string{"bot": "bot-token"})

	rec := do(t, srv, http.MethodPut, "/api/github-tokens/release-bot", `{"value":"  ghp-release\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "ghp-release") {
		t.Fatalf("the response leaked the token: %s", rec.Body)
	}

	data, err := os.ReadFile(filepath.Join(dir, "release-bot.token"))
	if err != nil {
		t.Fatalf("reading the file the UI should have written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "ghp-release" {
		t.Fatalf("release-bot.token holds %q, want the trimmed token", data)
	}
	// And the ladder itself resolves it -- the same call the git proxy
	// makes for a task holding this token's capability.
	if cred, ok := credentials.Get("release-bot"); !ok || cred.Token == nil || *cred.Token != "ghp-release" {
		t.Fatalf("Get(release-bot) = %+v, %v", cred, ok)
	}

	body := tokenBody(t, rec)
	row := tokenRow(t, body, "release-bot")
	if row["capability"] != "github-credential:release-bot" {
		t.Errorf("capability = %v, want the id a task holds", row["capability"])
	}
	if row["present"] != true || row["offered"] != false || row["needsRestart"] != true {
		t.Errorf("row = %v, want present, not yet offered, and needing a restart", row)
	}
	if body["restartRequired"] != true {
		t.Errorf("restartRequired = %v, want true", body["restartRequired"])
	}
}

// A token this process started with is already a capability, so nothing
// about it is pending; the default credential is the one the default
// host's default git connection names.
func TestListingReportsTheDefaultAndTheOfferedTokens(t *testing.T) {
	t.Parallel()
	srv, _, _ := gitHubTokenServer(t,
		map[string]string{"*": "bot", "acme/*": "bot"},
		map[string]string{"bot": "bot-token", "release-bot": "release-token"},
		"bot", "release-bot")

	rec := do(t, srv, http.MethodGet, "/api/github-tokens", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := tokenBody(t, rec)
	if body["enabled"] != true || body["restartRequired"] != false {
		t.Fatalf("enabled/restartRequired = %v/%v, want true/false", body["enabled"], body["restartRequired"])
	}

	def := tokenRow(t, body, "bot")
	if def["default"] != true {
		t.Errorf("bot = %v, want the deployment default", def)
	}
	if !reflect.DeepEqual(def["connections"], []any{"", "acme"}) {
		t.Errorf("bot connections = %v, want every git connection naming it", def["connections"])
	}
	extra := tokenRow(t, body, "release-bot")
	if extra["offered"] != true || extra["needsRestart"] != false {
		t.Errorf("release-bot = %v, want offered and settled", extra)
	}
}

func TestRemovingATokenDeletesItAndAsksForARestart(t *testing.T) {
	t.Parallel()
	srv, _, dir := gitHubTokenServer(t,
		map[string]string{"*": "bot"},
		map[string]string{"bot": "bot-token", "release-bot": "release-token"},
		"release-bot")

	rec := do(t, srv, http.MethodDelete, "/api/github-tokens/release-bot", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "release-bot.token")); !os.IsNotExist(err) {
		t.Errorf("release-bot.token still exists (stat err %v)", err)
	}
	// Still listed, because this process is still offering it: the row
	// is what tells an operator the picker will keep showing a token
	// nothing backs until the daemon restarts.
	row := tokenRow(t, tokenBody(t, rec), "release-bot")
	if row["present"] != false || row["offered"] != true || row["needsRestart"] != true {
		t.Errorf("row = %v, want absent, still offered, needing a restart", row)
	}
}

// Deleting a credential a git connection still names would take every
// push it covers down, so it is refused rather than done -- the error
// names the connections to repoint or remove first.
func TestRemovingACredentialAConnectionNamesIsRefused(t *testing.T) {
	t.Parallel()
	srv, _, dir := gitHubTokenServer(t,
		map[string]string{"*": "bot", "acme/*": "acme-bot"},
		map[string]string{"bot": "bot-token", "acme-bot": "acme-token"},
		"acme-bot")

	for _, name := range []string{"bot", "acme-bot"} {
		rec := do(t, srv, http.MethodDelete, "/api/github-tokens/"+name, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("DELETE %s status = %d, want 400: %s", name, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "git connections") {
			t.Errorf("DELETE %s said %q, want it to name the connections in the way", name, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(dir, name+".token")); err != nil {
			t.Errorf("%s.token was removed by a refused delete: %v", name, err)
		}
	}
}

func TestAddingATokenRejectsAnUnusableNameOrAnEmptyValue(t *testing.T) {
	t.Parallel()
	srv, _, dir := gitHubTokenServer(t, map[string]string{"*": "bot"}, map[string]string{"bot": "bot-token"})

	for _, call := range []struct{ path, body string }{
		{"/api/github-tokens/release%2Fbot", `{"value":"ghp-fake"}`},
		{"/api/github-tokens/anonymous", `{"value":"ghp-fake"}`},
		{"/api/github-tokens/release-bot", `{"value":"   "}`},
	} {
		rec := do(t, srv, http.MethodPut, call.path, call.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s status = %d, want 400: %s", call.path, rec.Code, rec.Body)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d files after three refused writes, want the original 1", len(entries))
	}
}

// grain/task-158: a GitHub App credential whose installation token
// cannot be minted is served anonymously by the ladder, so the repos it
// covers fail somewhere else entirely -- a private one as an ordinary
// authentication failure, a public one only on the first push. The file
// is still there and still valid-looking, so "present" was the whole of
// what this pane could say about a credential every push was failing on.
// It now mints when it lists one, and reports GitHub's own refusal.
func TestListingReportsAnAppCredentialThatCannotMintAToken(t *testing.T) {
	t.Parallel()
	srv, credentials, dir := gitHubTokenServer(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredentialFile(t, dir, "ci-app")
	credentials.AppTransport = github.NewFakeTransport(github.ApiResponse{
		Status: 403, Body: []byte(`{"message":"This installation has been suspended"}`),
	})

	rec := do(t, srv, http.MethodGet, "/api/github-tokens", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	row := tokenRow(t, tokenBody(t, rec), "ci-app")
	if row["app"] != true || row["present"] != true {
		t.Fatalf("ci-app = %v, want a present App credential", row)
	}
	mintError, _ := row["mintError"].(string)
	if !strings.Contains(mintError, "This installation has been suspended") {
		t.Errorf("mintError = %q, want GitHub's own reason for refusing the token exchange", mintError)
	}
}

// The other half of that: a credential that mints fine says nothing, so
// the pane's alert means what it says.
func TestListingReportsNoMintErrorForAWorkingAppCredential(t *testing.T) {
	t.Parallel()
	srv, credentials, dir := gitHubTokenServer(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredentialFile(t, dir, "ci-app")
	minted, err := json.Marshal(map[string]string{
		"token": "ghs_minted", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials.AppTransport = github.NewFakeTransport(github.ApiResponse{Status: 201, Body: minted})

	rec := do(t, srv, http.MethodGet, "/api/github-tokens", "")
	row := tokenRow(t, tokenBody(t, rec), "ci-app")
	if _, reported := row["mintError"]; reported {
		t.Errorf("ci-app = %v, want no mintError on a credential that minted fine", row)
	}
}

// writeAppCredentialFile writes the three values an App credential is,
// with a real (throwaway) key so the ladder gets as far as the token
// exchange the tests above script.
func writeAppCredentialFile(t *testing.T, dir, name string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	data, err := json.Marshal(map[string]string{
		"app_id": "12345", "installation_id": "67890", "private_key": string(pemKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".app.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
