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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
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

// The GCP providers are part of this build but deliberately absent from
// the active registry until their deployment settings are filled in.
// Name those gates rather than sending an operator looking for another
// binary, and keep gcp-key's two independent gates distinct.
func TestCapabilityCheckNamesMissingProviderConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		id       string
		cfg      daemon.Config
		want     []string
		dontWant []string
	}{
		{
			name:     "gemini-key needs only a project",
			id:       "gemini-key",
			want:     []string{"gemini-key", "GCP project ID", "blank", "Settings -> Capabilities"},
			dontWant: []string{"service-account", "in this build"},
		},
		{
			name:     "gcp-key with only an email needs a project",
			id:       "gcp-key",
			cfg:      daemon.Config{GcpServiceAccountEmail: "agent@example-project.iam.gserviceaccount.com"},
			want:     []string{"gcp-key", "GCP project ID", "blank", "Settings -> Capabilities"},
			dontWant: []string{"agent service-account email setting is blank", "in this build"},
		},
		{
			name:     "gcp-key with only a project needs an email",
			id:       "gcp-key",
			cfg:      daemon.Config{GcpProject: "example-project"},
			want:     []string{"gcp-key", "agent service-account email", "blank", "Settings -> Capabilities"},
			dontWant: []string{"GCP project ID", "in this build"},
		},
		{
			name:     "gcp-key names both empty settings",
			id:       "gcp-key",
			want:     []string{"gcp-key", "GCP project ID", "agent service-account email", "blank", "Settings -> Capabilities"},
			dontWant: []string{"in this build"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adapter := daemon.CapabilityCheckAdapter{Live: testLiveConfig(tc.cfg), Creds: noCredentials{}}
			_, err := adapter.CheckCapability(context.Background(), tc.id)
			if err == nil {
				t.Fatal("expected a check against an inactive provider to say so")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			for _, unwanted := range tc.dontWant {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("error %q unexpectedly contains %q", err, unwanted)
				}
			}
		})
	}
}

// Once it is configured, the provider answers -- and its answer is
// carried back as it stands, including the name of the credential it
// tried to authenticate as, which is what the pane reporting a refusal
// tells an operator to replace.
func TestCapabilityCheckCarriesTheProvidersOwnAnswer(t *testing.T) {
	t.Parallel()
	adapter := daemon.CapabilityCheckAdapter{
		Live: testLiveConfig(daemon.Config{
			GcpProject:             "example-project",
			GcpServiceAccountEmail: "agent@example-project.iam.gserviceaccount.com",
		}),
		Creds: noCredentials{},
	}

	result, err := adapter.CheckCapability(context.Background(), "gcp-key")
	if err == nil {
		t.Fatal("expected a check with no minter secret stored to fail")
	}
	if !strings.Contains(err.Error(), gcpkey.DefaultMinterCredential) {
		t.Errorf("error %q does not name the credential that could not be resolved", err)
	}
	if len(result.Credentials) != 1 || result.Credentials[0] != gcpkey.DefaultMinterCredential {
		t.Errorf("Credentials = %v, want the credential named even on the failure path", result.Credentials)
	}
}

// The registry is rebuilt per call out of the live configuration, so a
// project set in Settings a moment ago is the project the next check
// runs against -- a registry captured at startup would go on checking
// one an operator has already replaced.
func TestCapabilityCheckFollowsALaterSettingsChange(t *testing.T) {
	t.Parallel()
	live := testLiveConfig(daemon.Config{})
	adapter := daemon.CapabilityCheckAdapter{Live: live, Creds: noCredentials{}}

	if _, err := adapter.CheckCapability(context.Background(), "gemini-key"); err == nil ||
		!strings.Contains(err.Error(), "GCP project ID") {
		t.Fatalf("with no project set, err = %v, want the missing-project answer", err)
	}

	live.Mu.Lock()
	live.Applied.GcpProject = "example-project"
	live.Mu.Unlock()

	_, err := adapter.CheckCapability(context.Background(), "gemini-key")
	if err == nil {
		t.Fatal("expected the check to reach gemini-key's provider once a project is set")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Errorf("error %q still says the capability is unconfigured after the project was set", err)
	}
	if !strings.Contains(err.Error(), gcpkey.DefaultMinterCredential) {
		t.Errorf("error %q is not the provider's own answer about its minter credential", err)
	}
}

// The deployment this whole distinction exists for: a project saved in
// Settings that the running process has not adopted. The pane reports
// the stored row -- every field filled in, badge green -- while the
// registry a check is made through is built out of what this process is
// running with, so the check finds no provider. Answering "it is not
// configured" there sends an operator back to a pane where it plainly
// is; the answer has to name the gap between the two instead, and the
// restart that closes it.
func TestCapabilityCheckSaysWhenStoredSettingsAreNotApplied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	// What an operator just saved, against a process still running the
	// configuration it started with -- exactly what a daemon whose
	// reconcile loop never came up is left holding, since that loop is
	// the only thing that ever applies a change (liveConfig.refresh).
	stored := daemon.Config{}.ToModelConfig()
	stored.GCPProject = "example-project"
	putConfig(t, store, stored)
	live := daemon.NewLiveConfig(store, nil, daemon.Config{}, nil)
	adapter := daemon.CapabilityCheckAdapter{Live: live, Creds: noCredentials{}}

	_, err := adapter.CheckCapability(ctx, "gemini-key")
	if err == nil {
		t.Fatal("expected a check with no provider registered to say so")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Errorf("error %q says the capability is unconfigured, but the project is stored -- "+
			"which is the pane the operator would be sent back to", err)
	}
	if strings.Contains(err.Error(), "blank") || strings.Contains(err.Error(), "Settings -> Capabilities") {
		t.Errorf("error %q diagnoses blank configuration instead of preserving the unapplied-settings guidance", err)
	}
	for _, want := range []string{"gemini-key", "has not", "restarting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	// Once the process adopts the row, the check reaches the provider and
	// the answer is the provider's own again.
	live.Refresh(ctx, &orchestrator.Deps{})
	if _, err := adapter.CheckCapability(ctx, "gemini-key"); err == nil ||
		!strings.Contains(err.Error(), gcpkey.DefaultMinterCredential) {
		t.Errorf("after the setting was applied, err = %v, want the provider's own answer", err)
	}
}

// A deployment with nothing stored gets the precise missing-setting
// answer just like one without a store wired up.
func TestCapabilityCheckNamesBlankSettingWithNothingStored(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	putConfig(t, store, daemon.Config{}.ToModelConfig())
	adapter := daemon.CapabilityCheckAdapter{
		Live:  daemon.NewLiveConfig(store, nil, daemon.Config{}, nil),
		Creds: noCredentials{},
	}

	_, err := adapter.CheckCapability(context.Background(), "gemini-key")
	if err == nil || !strings.Contains(err.Error(), "GCP project ID") ||
		!strings.Contains(err.Error(), "Settings -> Capabilities") {
		t.Fatalf("err = %v, want the missing-project answer on a deployment with nothing stored", err)
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
