package cli

// Choosing (and crediting) the agent framework a dispatch runs under:
// the deployment's own default read live from the store, a task's
// override of it, and the credential each one needs -- read from the
// secrets database the UI writes before the file a deployment was seeded
// from.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/agent/custom"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func testSecrets(t *testing.T) *secrets.Store {
	t.Helper()
	return secrets.New(t.TempDir())
}

func testStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store
}

func TestAgentCredentialPrefersTheSecretsDatabaseOverTheFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testSecrets(t)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Only the file: what a deployment seeded by scripts/setup.sh has
	// before anyone opens the UI.
	got, err := daemon.AgentCredential(ctx, store, secrets.ClaudeOAuthTokenSecret, file)
	if err != nil || got != "from-the-file" {
		t.Fatalf("agentCredential = %q, %v; want the file's contents, trimmed", got, err)
	}

	// Then a key pasted into Settings: it wins, without the file having
	// to be rewritten or the daemon restarted.
	if err := store.Set(secrets.ClaudeOAuthTokenSecret, secrets.AgentCredentialKey, []byte("from-the-ui")); err != nil {
		t.Fatal(err)
	}
	got, err = daemon.AgentCredential(ctx, store, secrets.ClaudeOAuthTokenSecret, file)
	if err != nil || got != "from-the-ui" {
		t.Fatalf("agentCredential = %q, %v; want the stored secret to win", got, err)
	}
}

func TestAgentCredentialIsEmptyWithNeitherSourceRatherThanAnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// A file path that names nothing is the same "not configured this
	// way" an unset flag is: scripts/setup.sh passes
	// -gemini-api-key-file unconditionally, pointing at a file it only
	// writes once there is a key to write.
	got, err := daemon.AgentCredential(ctx, testSecrets(t), secrets.GeminiAPIKeySecret, filepath.Join(t.TempDir(), "never-written"))
	if err != nil || got != "" {
		t.Fatalf("agentCredential = %q, %v; want empty and no error", got, err)
	}
	got, err = daemon.AgentCredential(ctx, nil, secrets.GeminiAPIKeySecret, "")
	if err != nil || got != "" {
		t.Fatalf("agentCredential with no store and no file = %q, %v; want empty and no error", got, err)
	}
}

func TestAgentFrameworksSaysWhereToSetAMissingCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// -claude-path names a file that need not exist (claude.New does not
	// stat it) purely so the claude leg gets past exec.LookPath and
	// reaches the credential check this is about. Without it, the answer
	// depends on whether the machine running the test happens to have a
	// claude binary on $PATH: present, and the credential error asserted
	// below comes back; absent -- every CI runner -- and the framework
	// fails one step earlier, on the missing binary, which is a correct
	// error about a different missing thing.
	// -agy-path and -codex-path are stubbed for the same reason, every
	// framework needing a binary now that agent/antigravity runs the
	// Antigravity CLI where the in-process Gemini runtime needed nothing
	// on the host.
	cfg := daemon.Config{
		AgyPath:    filepath.Join(t.TempDir(), "agy"),
		ClaudePath: filepath.Join(t.TempDir(), "claude"),
		CodexPath:  filepath.Join(t.TempDir(), "codex"),
	}
	build := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), testSecrets(t))

	// Every framework there is, from the one list of them
	// (model.AgentFrameworks), so a fourth added later is covered here
	// the moment it is added rather than the moment somebody remembers
	// this test.
	for _, framework := range model.AgentFrameworks() {
		_, err := build(ctx, orchestrator.AgentChoice{Framework: framework})
		if err == nil {
			t.Fatalf("building the %s framework with no credential succeeded", framework)
		}
		// The message is the whole point: this reaches an operator as a
		// setup-failed run in the UI, and it has to say what to do about
		// it there rather than name a flag they cannot reach.
		if !strings.Contains(err.Error(), "Settings -> Models") {
			t.Fatalf("%s framework error = %v; want it to point at the UI", framework, err)
		}
	}
}

func TestAntigravityAmbientAuthenticationDoesNotRequireAKey(t *testing.T) {
	t.Parallel()
	framework, err := daemon.BuildAntigravityFramework(context.Background(), daemon.Config{
		AgyPath:       filepath.Join(t.TempDir(), "agy"),
		AgentAuthMode: model.AntigravityAuthAmbient,
	}, testSecrets(t))
	if err != nil {
		t.Fatalf("ambient Antigravity authentication: %v", err)
	}
	if framework == nil {
		t.Fatal("ambient Antigravity authentication returned no framework")
	}
}

// The counterpart to the test above, for the other thing each framework
// needs: a binary to exec. Dockerfile carries both agent CLIs now
// (bwsalmon/agents#645), so on a real deployment this error means an
// image built without one rather than a host missing a package -- and it
// is still what a dispatch hits, once per run, the moment someone
// selects that framework in Settings.
func TestAgentFrameworksSaysHowToInstallAMissingClaudeCLI(t *testing.T) {
	// An empty $PATH, so this asserts the same thing whether or not the
	// machine running it happens to have a claude binary -- the reverse
	// of the -claude-path trick the credential test above uses.
	t.Setenv("PATH", t.TempDir())
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkClaude, "sk-ant-oat01-fake")

	_, err := daemon.AgentFrameworks(withDefaultTeamBackends(daemon.Config{}), testStore(t), secretStore)(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude})
	if err == nil {
		t.Fatal("building the claude framework with no claude binary succeeded")
	}
	// "executable file not found in $PATH" alone reads as grain being
	// broken; an operator needs to be told this is a missing install,
	// and where the binary is supposed to come from.
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v; want it to name the CLI as not installed", err)
	}
	if !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("error = %v; want it to name where the CLI comes from", err)
	}
}

func TestAgentFrameworksRejectsAnUnknownName(t *testing.T) {
	t.Parallel()
	_, err := daemon.AgentFrameworks(withDefaultTeamBackends(daemon.Config{}), testStore(t), testSecrets(t))(context.Background(), orchestrator.AgentChoice{Framework: "gpt"})
	if err == nil || !strings.Contains(err.Error(), "unknown agent framework") {
		t.Fatalf("err = %v, want an unknown-framework error", err)
	}
}

func TestAgentFrameworksBuildsClaudeFromAUISetToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkClaude, "sk-ant-oat01-fake")
	// -claude-path, so this needs no claude binary on $PATH to prove the
	// credential half.
	cfg := daemon.Config{ClaudePath: filepath.Join(t.TempDir(), "claude")}

	framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude})
	if err != nil {
		t.Fatalf("building the claude framework: %v", err)
	}
	if _, ok := unwrapBackend(framework).(*claude.Framework); !ok {
		t.Fatalf("framework = %T, want *claude.Framework", framework)
	}
}

func TestClaudeVertexAuthenticationDoesNotRequireAToken(t *testing.T) {
	t.Parallel()
	framework, err := daemon.BuildClaudeFramework(context.Background(), daemon.Config{
		ClaudePath:     filepath.Join(t.TempDir(), "claude"),
		ClaudeAuthMode: model.ClaudeAuthVertex,
	}, testSecrets(t))
	if err != nil {
		t.Fatalf("vertex Claude authentication: %v", err)
	}
	if framework == nil {
		t.Fatal("vertex Claude authentication returned no framework")
	}
}

func TestClaudeVertexAuthenticationWithCredential(t *testing.T) {
	t.Parallel()
	secretStore := testSecrets(t)
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("claude-vertex-backend"), secrets.AgentCredentialKey, []byte(`{"type":"service_account","project_id":"v-proj"}`)); err != nil {
		t.Fatal(err)
	}
	framework, err := daemon.BuildClaudeFramework(context.Background(), daemon.Config{
		AgentBackendID: "claude-vertex-backend",
		ClaudePath:     filepath.Join(t.TempDir(), "claude"),
		ClaudeAuthMode: model.ClaudeAuthVertex,
	}, secretStore)
	if err != nil {
		t.Fatalf("vertex Claude authentication with credential: %v", err)
	}
	if framework == nil {
		t.Fatal("vertex Claude authentication returned no framework")
	}
}

// The codex half of the two tests above: it builds from a key set in
// the UI, and says where to install its CLI when the image has none.
func TestAgentFrameworksBuildsCodexFromAUISetKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkCodex, "sk-openai-fake")
	// -codex-path, so this needs no codex binary on $PATH to prove the
	// credential half.
	cfg := daemon.Config{CodexPath: filepath.Join(t.TempDir(), "codex")}

	framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatalf("building the codex framework: %v", err)
	}
	if _, ok := unwrapBackend(framework).(*codex.Framework); !ok {
		t.Fatalf("framework = %T, want *codex.Framework", framework)
	}
}

func TestAgentFrameworksBuildsCodexFromTheConnectionsOwnLogin(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	authFile := codex.BackendAuthFile(daemon.ManagedCodexAuthFile(dataDir), defaultTeamBackendID(model.AgentFrameworkCodex))
	if err := codex.WriteAuthFile(authFile, []byte(`{"tokens":{"refresh_token":"fake"}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{
		DataDir:   dataDir,
		CodexPath: codexVersionStub(t),
	}

	framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), testSecrets(t))(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatalf("building the codex framework: %v", err)
	}
	terminal, ok := framework.(agent.TerminalFramework)
	if !ok {
		t.Fatalf("framework = %T, want agent.TerminalFramework", framework)
	}
	cmd, err := terminal.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	defer cmd.Cleanup()
	var home string
	for _, env := range cmd.Env {
		if value, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
			home = value
		}
		if strings.HasPrefix(env, "OPENAI_API_KEY=") {
			t.Errorf("env = %v, want the connection's login rather than an API key", cmd.Env)
		}
	}
	target, err := os.Readlink(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("reading private home's auth.json link: %v", err)
	}
	want, _ := filepath.Abs(authFile)
	if target != want {
		t.Errorf("auth.json target = %q, want %q", target, want)
	}
}

// The deployment's shared ChatGPT login -- the UI-managed one or the
// file -codex-auth-file names -- is grain's own, not a connection's: a
// connection with no login of its own does not run on it
// (grain/task-1656).
func TestAgentFrameworksIgnoresTheDeploymentsCodexLogin(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := codex.WriteAuthFile(daemon.ManagedCodexAuthFile(dataDir), []byte(`{"tokens":{"refresh_token":"ui"}}`)); err != nil {
		t.Fatal(err)
	}
	flagFile := filepath.Join(t.TempDir(), "auth.json")
	if err := codex.WriteAuthFile(flagFile, []byte(`{"tokens":{"refresh_token":"flag"}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{
		DataDir:       dataDir,
		CodexPath:     codexVersionStub(t),
		CodexAuthFile: flagFile,
	}

	_, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), testSecrets(t))(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err == nil || !strings.Contains(err.Error(), "no Codex credential of its own") {
		t.Fatalf("building codex on the deployment's login: err = %v, want the connection's missing credential", err)
	}
}

// Nor does a connection with no key run on the deployment's key for its
// framework, from the UI or from a startup flag's file.
func TestAgentFrameworksIgnoreTheDeploymentsKey(t *testing.T) {
	t.Parallel()
	secretStore := testSecrets(t)
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("from-a-flag"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{secrets.ClaudeOAuthTokenSecret, secrets.OpenAIAPIKeySecret, secrets.GeminiAPIKeySecret} {
		if err := secretStore.Set(name, secrets.AgentCredentialKey, []byte("deployment-key")); err != nil {
			t.Fatal(err)
		}
	}
	cfg := daemon.Config{
		AgyPath:              filepath.Join(t.TempDir(), "agy"),
		ClaudePath:           filepath.Join(t.TempDir(), "claude"),
		CodexPath:            filepath.Join(t.TempDir(), "codex"),
		GeminiAPIKeyFile:     keyFile,
		ClaudeOAuthTokenFile: keyFile,
		OpenAIAPIKeyFile:     keyFile,
	}
	build := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)
	for _, framework := range []string{model.AgentFrameworkAntigravity, model.AgentFrameworkClaude, model.AgentFrameworkCodex} {
		_, err := build(context.Background(), orchestrator.AgentChoice{Framework: framework})
		if err == nil || !strings.Contains(err.Error(), "of its own") {
			t.Errorf("building %s with only a deployment key: err = %v, want the connection's missing key", framework, err)
			continue
		}
		// A team admin sets a connection's key in Team settings; Settings
		// -> Models is a system admin's page
		// (docs/test-runs/2026-10-04-connections-and-models.md, F4).
		if !strings.Contains(err.Error(), "Settings -> Models (/settings/models)") {
			t.Errorf("building %s with only a deployment key: err = %v, want it to name Settings -> Models", framework, err)
		}
	}
}

func TestAgentFrameworksPrefersAnAPIKeyOverTheSharedLogin(t *testing.T) {
	t.Parallel()
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkCodex, "sk-openai-fake")
	// The auth path deliberately does not exist: choosing it despite the
	// API key would make framework construction fail.
	cfg := daemon.Config{
		CodexPath:     codexVersionStub(t),
		CodexAuthFile: filepath.Join(t.TempDir(), "missing-auth.json"),
	}

	framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatalf("building the codex framework: %v", err)
	}
	terminal := framework.(agent.TerminalFramework)
	cmd, err := terminal.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	defer cmd.Cleanup()
	if !slices.Contains(cmd.Env, "OPENAI_API_KEY=sk-openai-fake") {
		t.Errorf("env = %v, want the configured API key", cmd.Env)
	}
}

func TestAgentFrameworksRotatesModelsOfTheSameKindWithTheirOwnKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "codex-fast", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "codex-deep", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"sk-fast", "sk-deep"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(backends[i].ID), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)
	for i, want := range []struct {
		model, key string
	}{
		{"gpt-5.6-luna", "sk-fast"},
		{"gpt-5.6-sol", "sk-deep"},
	} {
		framework, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
		if err != nil {
			t.Fatalf("building backend %d: %v", i, err)
		}
		terminal := framework.(agent.TerminalFramework)
		cmd, err := terminal.TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
		if err != nil {
			t.Fatalf("backend %d TerminalCommand: %v", i, err)
		}
		defer cmd.Cleanup()
		if len(cmd.Args) < 2 || cmd.Args[0] != "--model" || cmd.Args[1] != want.model {
			t.Errorf("backend %d args = %v, want --model %s", i, cmd.Args, want.model)
		}
		if !slices.Contains(cmd.Env, "OPENAI_API_KEY="+want.key) {
			t.Errorf("backend %d env has no key for %q", i, want.model)
		}
	}
}

func TestAgentFrameworksReusesCredentialsAndSkipsDisabledBackends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "source", Framework: model.AgentFrameworkCodex, Models: []string{"source-model"}, AuthMode: model.CodexAuthAPIKey, Disabled: true},
		{ID: "reused", Framework: model.AgentFrameworkCodex, Models: []string{"reused-model"}, CredentialBackendID: "source"},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("source"), secrets.AgentCredentialKey, []byte("sk-shared")); err != nil {
		t.Fatal(err)
	}

	framework, err := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatal(err)
	}
	pooled := framework.(*daemon.BackendFramework)
	if pooled.Id != "reused" {
		t.Fatalf("selected backend = %q, want reused", pooled.Id)
	}
	cmd, err := pooled.Framework.(agent.TerminalFramework).TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Cleanup()
	if !slices.Contains(cmd.Env, "OPENAI_API_KEY=sk-shared") {
		t.Errorf("env = %v, want reused source key", cmd.Env)
	}
}

func TestAgentFrameworksRotatesAcrossDifferentKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}},
		{ID: "codex", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"sk-ant-oat01-claude", "sk-codex"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(backends[i].ID), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{
		ClaudePath: filepath.Join(t.TempDir(), "claude"),
		CodexPath:  codexVersionStub(t),
	}, store, secretStore)
	first, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatal(err)
	}
	firstPool, ok := first.(*daemon.BackendFramework)
	if !ok {
		t.Fatalf("first framework = %T, want pooled backend", first)
	}
	secondPool, ok := second.(*daemon.BackendFramework)
	if !ok {
		t.Fatalf("second framework = %T, want pooled backend", second)
	}
	if _, ok := firstPool.Framework.(*claude.Framework); !ok {
		t.Errorf("first pooled framework = %T, want Claude", firstPool.Framework)
	}
	if _, ok := secondPool.Framework.(*codex.Framework); !ok {
		t.Errorf("second pooled framework = %T, want Codex", secondPool.Framework)
	}
}

// A task that named a model gets that model, whatever the rotation was
// about to reach and whether or not the model is still in the rotation at
// all (grain/task-110). The disabled backend here is the whole point: an
// account kept out of the pool so nothing lands on it by accident is
// exactly the one an operator points a particular task at.
func TestAgentFrameworksBuildsTheModelATaskNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "codex-day", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "codex-night", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}, Disabled: true},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"sk-day", "sk-night"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(backends[i].ID), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)
	// Twice: a pinned dispatch must not advance the rotation's own
	// cursor either, so the answer cannot depend on how many pinned runs
	// came before it.
	for i := 0; i < 2; i++ {
		framework, err := build(ctx, orchestrator.AgentChoice{BackendID: "codex-night"})
		if err != nil {
			t.Fatalf("building the pinned model: %v", err)
		}
		pooled := framework.(*daemon.BackendFramework)
		if pooled.Id != "codex-night" {
			t.Fatalf("pinned dispatch %d used %q, want codex-night", i, pooled.Id)
		}
		cmd, err := pooled.Framework.(agent.TerminalFramework).TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if len(cmd.Args) < 2 || cmd.Args[0] != "--model" || cmd.Args[1] != "gpt-5.6-sol" {
			t.Errorf("pinned dispatch args = %v, want --model gpt-5.6-sol", cmd.Args)
		}
		if !slices.Contains(cmd.Env, "OPENAI_API_KEY=sk-night") {
			t.Errorf("pinned dispatch env = %v, want the pinned model's own key", cmd.Env)
		}
		cmd.Cleanup()
	}

	// And the rotation is untouched by all that: the next task that
	// named nothing still gets the one enabled model.
	framework, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("building the rotation's own choice: %v", err)
	}
	if got := framework.(*daemon.BackendFramework).Id; got != "codex-day" {
		t.Fatalf("unpinned dispatch used %q, want codex-day", got)
	}
}

// A quota window is the rotation's way of not spending attempts on an
// account that has none left. It does not silently redirect a task that
// named one model onto another: there is nowhere to fall back to that
// would still be what was asked for, and running it says the same thing
// the refusal would -- only with a chance of being wrong about it.
func TestAgentFrameworksBuildsAPinnedModelInsideItsQuotaWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "codex-fast", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "codex-deep", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"sk-fast", "sk-deep"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(backends[i].ID), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)
	first, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatal(err)
	}
	firstPool := first.(*daemon.BackendFramework)
	firstPool.Unavailable(firstPool.Id, time.Now().Add(time.Hour))

	framework, err := build(ctx, orchestrator.AgentChoice{BackendID: firstPool.Id})
	if err != nil {
		t.Fatalf("building a pinned model inside its quota window: %v", err)
	}
	if got := framework.(*daemon.BackendFramework).Id; got != firstPool.Id {
		t.Fatalf("pinned dispatch used %q, want %q", got, firstPool.Id)
	}
}

// Both ways a pin can have been overtaken by an edit to the pool, each
// worth a run that fails setup saying so rather than a quiet fallback
// onto a model nobody chose.
func TestAgentFrameworksRefusesAPinItCannotHonour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "codex-day", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
	}}); err != nil {
		t.Fatal(err)
	}

	build := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, testSecrets(t))
	for _, tc := range []struct {
		name   string
		choice orchestrator.AgentChoice
		want   string
	}{
		{"model since removed", orchestrator.AgentChoice{BackendID: "codex-night"}, "codex-night"},
		{
			"model of another framework than the task names",
			orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude, BackendID: "codex-day"},
			"codex-day",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := build(ctx, tc.choice); err == nil {
				t.Fatal("the dispatch was built anyway")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestAgentFrameworksSkipsABackendUntilItsQuotaReturns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "codex-fast", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "codex-deep", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"sk-fast", "sk-deep"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(backends[i].ID), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)
	first, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatal(err)
	}
	firstPool := first.(*daemon.BackendFramework)
	if firstPool.Id != "codex-fast" {
		t.Fatalf("first backend = %q, want codex-fast", firstPool.Id)
	}

	// This is the state change Run makes when the provider reports a
	// UsageLimitError. While that window is live, repeated dispatches use
	// the other model instead of spending attempts on the exhausted one.
	firstPool.Unavailable(firstPool.Id, time.Now().Add(time.Hour))
	for i := 0; i < 2; i++ {
		framework, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
		if err != nil {
			t.Fatal(err)
		}
		if got := framework.(*daemon.BackendFramework).Id; got != "codex-deep" {
			t.Errorf("backend while fast quota is exhausted = %q, want codex-deep", got)
		}
	}

	// Expired windows put the backend back into the same round robin; no
	// daemon restart or settings edit is needed when quota becomes usable.
	firstPool.Unavailable(firstPool.Id, time.Now().Add(-time.Second))
	framework, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatal(err)
	}
	if got := framework.(*daemon.BackendFramework).Id; got != "codex-fast" {
		t.Errorf("backend after fast quota returns = %q, want codex-fast", got)
	}
}

func TestAgentFrameworksSaysHowToInstallAMissingCodexCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkCodex, "sk-openai-fake")

	_, err := daemon.AgentFrameworks(withDefaultTeamBackends(daemon.Config{}), testStore(t), secretStore)(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err == nil {
		t.Fatal("building the codex framework with no codex binary succeeded")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v; want it to name the CLI as not installed", err)
	}
	if !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("error = %v; want it to name where the CLI comes from", err)
	}
}

// Terminal preparation now checks the reviewed CLI version before it
// starts Grain's real MCP child. These tests never start a model turn.
func codexVersionStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'codex-cli "+codex.ReviewedCLIVersion+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The same missing-binary path for the other framework. agent/
// antigravity runs Google's Antigravity CLI as a subprocess, so a
// deployment that upgraded across the runtime replacement has a host
// that never installed one -- and unlike claude's, scripts/setup.sh does
// not install it (no verified installer to run), only warns. That makes
// this the error such a deployment actually hits, so it has to say what
// to do about it.
func TestAgentFrameworksSaysHowToInstallAMissingAgyCLI(t *testing.T) {
	// An empty $PATH, so this asserts the same thing whether or not the
	// machine running it happens to have an agy binary.
	t.Setenv("PATH", t.TempDir())
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkAntigravity, "AIza-fake")

	_, err := daemon.AgentFrameworks(withDefaultTeamBackends(daemon.Config{}), testStore(t), secretStore)(context.Background(), orchestrator.AgentChoice{Framework: model.AgentFrameworkAntigravity})
	if err == nil {
		t.Fatal("building the antigravity framework with no agy binary succeeded")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v; want it to name the CLI as not installed", err)
	}
	// Dockerfile carries agy now (bwsalmon/agents#645), which it did
	// not when this error was written: the message used to send an
	// operator off to install it by hand on the host, and the honest
	// answer on a real deployment is that the image was built without
	// it.
	if !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("error = %v; want it to name where the CLI comes from", err)
	}
}

// A stored row or a task still carrying the framework's former name has
// to dispatch onto the framework that name now means, rather than into
// agentFrameworks' unknown-framework error. Nothing rewrites those rows
// (model.Config.AgentFramework's own doc comment), so this is the whole
// upgrade path for a deployment that set the framework before the
// rename.
func TestAgentFrameworksAcceptsTheLegacyGeminiName(t *testing.T) {
	t.Parallel()
	cfg := daemon.Config{AgyPath: filepath.Join(t.TempDir(), "agy")}
	secretStore := testSecrets(t)
	setDefaultTeamBackendKey(t, secretStore, model.AgentFrameworkAntigravity, "AIza-fake")

	framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)(
		context.Background(), orchestrator.AgentChoice{Framework: model.LegacyAgentFrameworkGemini})
	if err != nil {
		t.Fatalf("building the framework named %q: %v", model.LegacyAgentFrameworkGemini, err)
	}
	if _, ok := unwrapBackend(framework).(*antigravity.Framework); !ok {
		t.Fatalf("framework = %T, want *antigravity.Framework", framework)
	}
}

func isolateModelProxies(t *testing.T) {
	t.Helper()
	orig := make(map[string]string)
	for _, vendor := range modelproxy.Vendors() {
		if ep := daemon.ModelProxies[vendor.Name]; ep != nil {
			orig[vendor.Name] = ep.URL()
			ep.SetURL("")
		}
	}
	t.Cleanup(func() {
		for vendor, u := range orig {
			if ep := daemon.ModelProxies[vendor]; ep != nil {
				ep.SetURL(u)
			}
		}
	})
}

func TestCustomBackendModelProxyResolvesRunningProxy(t *testing.T) {
	isolateModelProxies(t)

	endpoint := daemon.ModelProxies[modelproxy.Claude.Name]
	endpoint.SetURL("http://127.0.0.1:45678")

	url, token, release, err := daemon.CustomBackendModelProxy(t.Context(), model.AgentBackend{
		ID: "custom-claude",
	}, "claude-sonnet-5", agent.RunConfig{TaskID: "task-123"})
	if err != nil {
		t.Fatalf("customBackendModelProxy: %v", err)
	}
	if release == nil {
		t.Fatal("no release func for a minted token")
	}
	if url != "http://127.0.0.1:45678" {
		t.Fatalf("url = %q, want http://127.0.0.1:45678", url)
	}
	if owner, ok := endpoint.Tokens.Authenticate(token); !ok || owner != "task-123" {
		t.Fatalf("minted token authenticates as (%q, %v), want task-123", owner, ok)
	}
	release()
	if _, ok := endpoint.Tokens.Authenticate(token); ok {
		t.Fatalf("token still authenticates after release; it must be revoked when the run ends")
	}
}

func TestCustomBackendModelProxyEmptyWhenNoProxy(t *testing.T) {
	isolateModelProxies(t)

	url, token, release, err := daemon.CustomBackendModelProxy(t.Context(), model.AgentBackend{
		ID: "custom-claude",
	}, "claude-sonnet-5", agent.RunConfig{TaskID: "task-123"})
	if err != nil {
		t.Fatalf("customBackendModelProxy: %v", err)
	}
	if url != "" || token != "" || release != nil {
		t.Fatalf("expected empty fields, got url=%q token=%q release=%v", url, token, release != nil)
	}
}

// A backend whose vendor cannot be told from its model or name is not
// handed a token for whichever proxy is running: that is access to a
// vendor it never named.
func TestCustomBackendModelProxyEmptyForAnUnknownVendor(t *testing.T) {
	isolateModelProxies(t)
	for _, vendor := range modelproxy.Vendors() {
		daemon.ModelProxies[vendor.Name].SetURL("http://127.0.0.1:45678")
	}

	url, token, release, err := daemon.CustomBackendModelProxy(t.Context(), model.AgentBackend{
		ID: "custom-unknown",
	}, "unknown-model", agent.RunConfig{TaskID: "task-123"})
	if err != nil {
		t.Fatalf("customBackendModelProxy: %v", err)
	}
	if url != "" || token != "" || release != nil {
		t.Fatalf("expected empty fields, got url=%q token=%q", url, token)
	}
}

// The token a custom backend is handed stops authenticating once the
// run it was minted for returns, rather than living out its whole lease.
func TestCustomBackendModelProxyTokenIsRevokedWhenTheRunEnds(t *testing.T) {
	isolateModelProxies(t)
	endpoint := daemon.ModelProxies[modelproxy.Claude.Name]
	endpoint.SetURL("http://127.0.0.1:45678")

	state := t.TempDir()
	tokenFile := filepath.Join(state, "token")
	script := "#!/bin/sh\n" +
		"sed -n 's/.*\"model_proxy_token\":\"\\([^\"]*\\)\".*/\\1/p' > " + tokenFile + "\n" +
		`printf '%s' '{"protocol_version":2,"result":{"final_text":"done"}}'` + "\n"
	if err := os.WriteFile(filepath.Join(state, "backend"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var seen string
	framework, err := custom.New(custom.Config{
		Command: filepath.Join(state, "backend"),
		ModelProxy: func(ctx context.Context, run agent.RunConfig) (string, string, func(), error) {
			url, token, release, err := daemon.CustomBackendModelProxy(t.Context(), model.AgentBackend{ID: "c"}, "claude-opus-5", run)
			seen = token
			if _, ok := endpoint.Tokens.Authenticate(token); !ok {
				t.Error("token does not authenticate while the run is live")
			}
			return url, token, release, err
		},
		MCPServer: func(context.Context, agent.RunConfig) (custom.Command, error) {
			return custom.Command{Command: "grain"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framework.Run(context.Background(), agent.RunConfig{TaskID: "task-9", SandboxRoot: state}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if seen == "" || strings.TrimSpace(string(got)) != seen {
		t.Fatalf("backend received token %q, minted %q", got, seen)
	}
	if owner, ok := endpoint.Tokens.Authenticate(seen); ok {
		t.Fatalf("token still authenticates as %q after the run returned", owner)
	}
}

func TestCustomAgentMCPServerUsesAContainerTransport(t *testing.T) {
	t.Parallel()
	args, err := daemon.CustomMCPServerArgs(context.Background(), daemon.Config{}, agent.RunConfig{
		Container: "grain-sandbox-run-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mcpserver", "-container", "grain-sandbox-run-7"}
	if !slices.Equal(args, want) {
		t.Fatalf("custom MCP server args = %q, want %q", args, want)
	}
}

func TestLiveTranscriptsPicksTheFormatPerFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcripts := daemon.LiveTranscriptDir(dir)

	// agent/claude mirrors claude's own --output-format stream-json...
	claudeLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"looking at the parser"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "run-claude"), []byte(claudeLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ...and agent/antigravity mirrors agy's, which is NDJSON too. The
	// two are told apart by the key each tags its events with -- "type"
	// above, "event" here -- since "does it open with a brace" stopped
	// separating them when the in-process Gemini runtime (which tee'd an
	// already-readable narrative) was replaced.
	agyLine := `{"event":"step_update","step_update":{"step_index":0,"state":"DONE",` +
		`"step_type":"agent_response","text_delta":"thinking about the parser"}}`
	if err := os.WriteFile(filepath.Join(dir, "run-agy"), []byte(agyLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ...and agent/codex mirrors codex's own --json, which tags its
	// events with "type" the way claude's does -- so that key alone no
	// longer separates those two either. codex's are dotted names from
	// its own thread/item vocabulary, which is what tells them apart.
	codexLine := `{"type":"item.completed","item":{"id":"item_0","type":"agent_message",` +
		`"text":"parsing what the run produced"}}`
	if err := os.WriteFile(filepath.Join(dir, "run-codex"), []byte(codexLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	text, ok, err := transcripts.Tail("run-claude")
	if err != nil || !ok {
		t.Fatalf("Tail(run-claude) = %q, %v, %v", text, ok, err)
	}
	if !strings.Contains(text, "looking at the parser") {
		t.Fatalf("Tail(run-claude) = %q, want the stream-json decoded", text)
	}
	if strings.Contains(text, `"type"`) {
		t.Fatalf("Tail(run-claude) = %q, want it parsed rather than handed back raw", text)
	}

	text, ok, err = transcripts.Tail("run-agy")
	if err != nil || !ok || text != "thinking about the parser" {
		t.Fatalf("Tail(run-agy) = %q, %v, %v; want agy's stream-json decoded", text, ok, err)
	}
	if strings.Contains(text, `"event"`) {
		t.Fatalf("Tail(run-agy) = %q, want it parsed rather than handed back raw", text)
	}

	text, ok, err = transcripts.Tail("run-codex")
	if err != nil || !ok || text != "parsing what the run produced" {
		t.Fatalf("Tail(run-codex) = %q, %v, %v; want codex's own event stream decoded", text, ok, err)
	}
	if strings.Contains(text, "item.completed") {
		t.Fatalf("Tail(run-codex) = %q, want it parsed rather than handed back raw", text)
	}

	if text, ok, err := transcripts.Tail("run-that-never-started"); err != nil || ok || text != "" {
		t.Fatalf("Tail of a missing transcript = %q, %v, %v; want the not-yet reading", text, ok, err)
	}
}

// A usage-limit pause is keyed by the connection owning a credential
// (orchestrator.Pause), and the pool is where dispatch meets it: the
// rotation passes over a paused connection onto the deployment's others
// (whichever team's editor saved them), a connection borrowing the paused
// key is paused with it, and with nothing left a task waits.
func TestAgentPoolPassesOverAPausedCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	backends := []model.AgentBackend{
		{ID: "conn-a", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "conn-borrow", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}, CredentialBackendID: "conn-a"},
		{ID: "conn-b", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}},
		{ID: "conn-c", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-luna"}, Team: "beta"},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conn-a", "conn-b", "conn-c"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("sk-"+id)); err != nil {
			t.Fatal(err)
		}
	}

	pool := daemon.NewAgentPool(daemon.Config{CodexPath: codexVersionStub(t)}, store, secretStore)
	pool.Pause = &orchestrator.Pause{}
	unpinned := orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex}
	built := func(choice orchestrator.AgentChoice) *daemon.BackendFramework {
		t.Helper()
		framework, err := pool.Build(ctx, choice)
		if err != nil {
			t.Fatalf("building %+v: %v", choice, err)
		}
		return framework.(*daemon.BackendFramework)
	}

	// A borrowing connection spends, and so reports, its source's key.
	if conn, _ := built(orchestrator.AgentChoice{BackendID: "conn-borrow"}).CredentialConnection(); conn != "conn-a" {
		t.Fatalf("conn-borrow's credential = %q, want conn-a's", conn)
	}

	pool.Pause.Begin(time.Now(), orchestrator.PauseScope{Connection: "conn-a", Framework: model.AgentFrameworkCodex},
		&agent.UsageLimitError{Framework: model.AgentFrameworkCodex, ResetAt: time.Now().Add(time.Hour)})

	used := map[string]bool{}
	for i := 0; i < 4; i++ {
		got := built(unpinned).Id
		if got != "conn-b" && got != "conn-c" {
			t.Errorf("dispatch %d while conn-a's key is paused went to %q, want conn-b or conn-c", i, got)
		}
		used[got] = true
	}
	if !used["conn-b"] || !used["conn-c"] {
		t.Errorf("dispatches while conn-a's key is paused used %v, want both conn-b and conn-c", used)
	}
	if reason := pool.Availability(ctx, unpinned); reason != "" {
		t.Errorf("a task waits (%q) with conn-b and conn-c still free", reason)
	}
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{BackendID: "conn-borrow"}); !strings.Contains(reason, "paused") {
		t.Errorf("a task pinned to conn-borrow is admitted (%q) while the key it borrows is paused", reason)
	}

	pool.Pause.Begin(time.Now(), orchestrator.PauseScope{Connection: "conn-b", Framework: model.AgentFrameworkCodex},
		&agent.UsageLimitError{Framework: model.AgentFrameworkCodex, ResetAt: time.Now().Add(time.Hour)})
	if got := built(unpinned).Id; got != "conn-c" {
		t.Errorf("dispatch with conn-a and conn-b paused went to %q, want conn-c", got)
	}
	pool.Pause.Begin(time.Now(), orchestrator.PauseScope{Connection: "conn-c", Framework: model.AgentFrameworkCodex},
		&agent.UsageLimitError{Framework: model.AgentFrameworkCodex, ResetAt: time.Now().Add(time.Hour)})
	if reason := pool.Availability(ctx, unpinned); reason == "" {
		t.Error("a task is admitted with every one of the deployment's keys paused")
	}

	// Lifting makes the connection a candidate again at once: the pool
	// keeps no cool-off of its own for a usage limit it hands the Pause.
	pool.Pause.Lift()
	if reason := pool.Availability(ctx, unpinned); reason != "" {
		t.Errorf("a task still waits (%q) after the pauses were lifted", reason)
	}
}
