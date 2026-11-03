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

package cli

// grain/task-172: the daemon's half of "test this credential" -- the
// adapter Settings' Capabilities tab reaches a real provider through.
// What matters here is which questions it answers itself and which it
// passes on: it decides whether this deployment has a provider for the
// capability at all, and everything past that is the provider's own
// sentence carried back unchanged.

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// noCredentials is a resolver with nothing in it -- a deployment where
// the secret was never pasted. It fails before any provider reaches a
// network, which is what keeps these tests offline.
type noCredentials struct{}

func (noCredentials) Resolve(ctx context.Context, name string) (string, error) {
	return "", errors.New("no such credential: " + name)
}

func testLiveConfig(cfg daemon.Config) *daemon.LiveConfig {
	return daemon.NewLiveConfig(nil, nil, cfg, nil)
}

// gemini-key was retired onto the gcp server's gcp#gemini-key (schema
// migration 103), so no deployment registers a provider for it any more,
// with or without a GCP project: a check says so rather than naming a
// setting that would not register one either.
func TestCapabilityCheckOfRetiredGeminiKeyFindsNoProvider(t *testing.T) {
	t.Parallel()
	for _, cfg := range []daemon.Config{{}, {GcpProject: "example-project"}} {
		adapter := daemon.CapabilityCheckAdapter{Live: testLiveConfig(cfg), Creds: noCredentials{}}
		_, err := adapter.CheckCapability(context.Background(), "gemini-key")
		if err == nil || !strings.Contains(err.Error(), `registers no provider for "gemini-key"`) {
			t.Errorf("project %q: err = %v, want no provider for gemini-key", cfg.GcpProject, err)
		}
		if err != nil && strings.Contains(err.Error(), "blank") {
			t.Errorf("project %q: err %q names a blank setting, which no longer registers it", cfg.GcpProject, err)
		}
	}
}

// A registered provider answers -- and its answer is carried back as it
// stands, including the names of the credentials it tried to
// authenticate as, which is what the pane reporting a refusal tells an
// operator to replace.
func TestCapabilityCheckCarriesTheProvidersOwnAnswer(t *testing.T) {
	t.Parallel()
	adapter := daemon.CapabilityCheckAdapter{Live: testLiveConfig(daemon.Config{}), Creds: noCredentials{}}

	result, err := adapter.CheckCapability(context.Background(), "github-sandbox")
	if err == nil {
		t.Fatal("expected a check with no App secrets stored to fail")
	}
	want := []string{githubsandbox.DefaultAppIDCredential, githubsandbox.DefaultPrivateKeyCredential}
	if !slices.Equal(result.Credentials, want) {
		t.Errorf("Credentials = %v, want %v named even on the failure path", result.Credentials, want)
	}
}

// Capabilities that hold no standing credential are refused here as well
// as in pkg/ui: nothing to authenticate as means nothing a check could
// report on.
func TestCapabilityCheckRefusesACapabilityWithNoCredential(t *testing.T) {
	t.Parallel()
	adapter := daemon.CapabilityCheckAdapter{Live: testLiveConfig(daemon.Config{}), Creds: noCredentials{}}
	if _, err := adapter.CheckCapability(context.Background(), "self-debug"); err == nil {
		t.Fatal("expected self-debug, which holds no credential, to be refused")
	}
}

// --- named GitHub tokens (grain/task-189) ------------------------------

// writeLadder lays out a data directory's credential ladder: a default
// token every repo falls back to, plus whatever else is named, and
// returns the data directory.
func writeLadder(t *testing.T, files map[string]string) string {
	t.Helper()
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"*":"bot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir
}

// A named token's material is not in the secrets store at all -- it is
// an operator's file under secrets/github -- so its provider is built
// holding the credential ladder instead, and the resolver every other
// capability is checked through goes unused. This is that wiring, and it
// stays offline by using a credential the ladder serves as anonymous: an
// empty token file, which the provider has an answer for before it ever
// reaches GitHub.
func TestCapabilityCheckReachesANamedGitHubTokensLadder(t *testing.T) {
	t.Parallel()
	dataDir := writeLadder(t, map[string]string{"bot.token": "tok\n", "release-bot.token": "\n"})
	ladder, err := daemon.GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	live := daemon.NewLiveConfig(nil, nil, daemon.Config{}, ladder)
	if got := live.GitHubTokens(); len(got) != 2 || got[0] != "bot" || got[1] != "release-bot" {
		t.Fatalf("named tokens = %v, want every stored token", got)
	}
	adapter := daemon.CapabilityCheckAdapter{Live: live, Creds: noCredentials{}}

	result, err := adapter.CheckCapability(context.Background(), model.GitCredentialCapability("release-bot"))
	if err == nil {
		t.Fatal("expected a credential file with no token in it to be reported")
	}
	if !strings.Contains(err.Error(), "secrets/github/release-bot.token") {
		t.Errorf("error %q does not name the file that resolves to nothing", err)
	}
	if len(result.Credentials) != 1 || result.Credentials[0] != "secrets/github/release-bot.token" {
		t.Errorf("Credentials = %v, want the file named even on the failure path", result.Credentials)
	}
}

// A capability the deployment does not know keeps the generic
// no-provider answer, with no claim about this build or an unrelated
// setting.
func TestCapabilityCheckKeepsGenericAnswerForUnknownCapability(t *testing.T) {
	t.Parallel()
	adapter := daemon.CapabilityCheckAdapter{Live: testLiveConfig(daemon.Config{}), Creds: noCredentials{}}

	id := "unavailable-capability"
	_, err := adapter.CheckCapability(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "registers no provider") || !strings.Contains(err.Error(), id) {
		t.Fatalf("err = %v, want the generic no-provider answer for an unavailable capability", err)
	}
	for _, unwanted := range []string{"GCP project", "service-account", "Settings -> Capabilities", "in this build"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("generic error %q unexpectedly contains %q", err, unwanted)
		}
	}
}

// githubCredentialSource is the one place the credential ladder and the
// capability package meet: a name in, the material a push through it
// would carry out, and which of the two file forms it came from -- which
// is what decides whether a refusal tells an operator to paste a token
// or to replace a file.
func TestGitHubCredentialSourceResolvesBothFileForms(t *testing.T) {
	t.Parallel()
	dataDir := writeLadder(t, map[string]string{
		"bot.token":         "tok\n",
		"release-bot.token": "ghp_live\n",
		// Deliberately not a usable App credential: what is asserted
		// here is that it is recognised as one, and the ladder's own
		// fail-soft-to-anonymous path is exactly the state the check
		// reports as "every push goes out unauthenticated".
		"app-bot.app.json": `{"app_id":"1","installation_id":"2","private_key":"not a key"}`,
	})
	ladder, err := daemon.GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	source := daemon.GithubCredentialSource{Set: ladder}

	cred, ok := source.GitHubCredential("release-bot")
	if !ok || cred.Token != "ghp_live" || cred.App {
		t.Errorf("release-bot = %+v, %v, want the token file's own value", cred, ok)
	}
	cred, ok = source.GitHubCredential("app-bot")
	if !ok || !cred.App || cred.Token != "" {
		t.Errorf("app-bot = %+v, %v, want an App credential with nothing minted from it", cred, ok)
	}
	if _, ok := source.GitHubCredential("never-configured"); ok {
		t.Error("a credential with no file behind it was resolved anyway")
	}
	// A process with no ladder at all answers the same way rather than
	// panicking: it offers no named tokens to check in the first place.
	if _, ok := (daemon.GithubCredentialSource{}).GitHubCredential("release-bot"); ok {
		t.Error("a nil ladder resolved a credential")
	}
}

// --- the CLI side ------------------------------------------------------

// `grain settings -check-capability <id>` is the same question asked
// from the host, which is where whoever is reading a failed task's error
// usually already is. A refused credential prints a verdict and the
// provider's sentence -- it is not a failure of the command, so it is
// not an error return either.
func TestCmdSettingsChecksACapabilityCredential(t *testing.T) {
	ctx := context.Background()
	srv := checkerTestServer(t, &stubChecker{result: ui.CapabilityCheckResult{
		Credentials: []string{"gcp-key-minter"},
	}, err: errors.New("GCP will not issue a token for the minter credential held in the `gcp-key-minter` secret")})
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-check-capability", "gcp-key"}); err != nil {
			t.Errorf("cmdSettings -check-capability: %v", err)
		}
	})
	for _, want := range []string{"gcp-key: REFUSED", "checked as:  gcp-key-minter", "will not issue a token"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// The listing itself keeps saying "ready", which means configured -- so
// where anything can be tested it also says where to go and test it.
func TestCmdSettingsPointsAtTheCredentialCheck(t *testing.T) {
	ctx := context.Background()
	srv := checkerTestServer(t, &stubChecker{})
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	if !strings.Contains(out, "grain settings -check-capability <id>") {
		t.Errorf("output %q does not name the command that tests a stored credential", out)
	}
}

// stubChecker is ui.CapabilityChecker with a fixed answer -- the daemon
// adapter's place, with no provider and no cloud behind it.
type stubChecker struct {
	result ui.CapabilityCheckResult
	err    error
}

func (s *stubChecker) CheckCapability(ctx context.Context, id string) (ui.CapabilityCheckResult, error) {
	return s.result, s.err
}

func checkerTestServer(t *testing.T, checker ui.CapabilityChecker) *httptest.Server {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	cfg := ui.Config{
		Actor:            ui.DefaultActor("operator"),
		Capabilities:     ui.OfferedCapabilities(),
		CapabilityChecks: checker,
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
	t.Cleanup(srv.Close)
	if _, err := accessCodeClient(srv.URL).UpdateSettings(context.Background(),
		*settingsRequest("30s", 2, "github.example")); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}
	return srv
}
