package codex

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestCatalogReadsPagesAndModelSpecificEfforts(t *testing.T) {
	responses := `{"id":1,"result":{}}
{"method":"notification"}
{"id":2,"result":{"data":[{"id":"a","model":"old-codex","displayName":"Earlier model","supportedReasoningEfforts":[{"reasoningEffort":"high"}],"defaultReasoningEffort":"high"}],"nextCursor":"page2"}}
{"id":3,"result":{"data":[{"id":"gpt-5.6-sol","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"max"}]},{"id":"hidden","hidden":true}],"nextCursor":null}}
`
	var requests bytes.Buffer
	models, err := readCatalog(&requests, strings.NewReader(responses))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "old-codex" || models[0].DefaultEffort != "high" || len(models[1].Efforts) != 2 {
		t.Fatalf("models: %+v", models)
	}
	for _, want := range []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"model/list"`, `"cursor":"page2"`} {
		if !strings.Contains(requests.String(), want) {
			t.Fatalf("missing %s in %s", want, requests.String())
		}
	}
}

// probeHost is a machine that is not this one only in the way that
// matters here: it is not agent.LocalHost, so every path catalogHome
// stages goes through a Host.
type probeHost struct {
	agent.LocalHost
}

// The private CODEX_HOME a probe runs under, staged through the
// agent.Host seam -- the same catalogHome a run's own home goes through,
// which is why asserting it here covers both.
//
// What matters is that auth.json is *linked* rather than copied (codex
// rewrites it in place when its OAuth tokens rotate, and a copy would
// strand the refreshed token), that CODEX_HOME names the staged
// directory, and that the directory is removed again afterwards.
func TestCatalogHomeStagesItsHomeThroughTheHost(t *testing.T) {
	host := &probeHost{}
	authFile := filepath.Join(t.TempDir(), "auth.json")
	// A real login: resolveAuthFile validates it, since a file codex
	// cannot read or write a refreshed token back through is a run that
	// fails minutes later saying nothing about auth.json.
	if err := os.WriteFile(authFile, []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home, cleanup, err := catalogHome(context.Background(), host, authFile)
	if err != nil {
		t.Fatal(err)
	}
	if home == "" {
		t.Fatal("no home was staged")
	}
	if link, err := os.Readlink(filepath.Join(home, authFileName)); err != nil || link != authFile {
		t.Errorf("auth link %q (%v), want %q", link, err, authFile)
	}
	if env := catalogEnv(home, ""); !slices.Contains(env, "CODEX_HOME="+home) {
		t.Errorf("env %v, want CODEX_HOME=%s", env, home)
	}
	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("home left behind: %v", err)
	}

	// A relative auth path is refused: it would name whatever directory
	// the process happened to start in rather than the file meant.
	if _, _, err := catalogHome(context.Background(), host, "auth.json"); err == nil {
		t.Error("accepted a relative auth file")
	}
}

func TestCatalogRefusesBrokenResponses(t *testing.T) {
	for _, response := range []string{"", "not-json\n", `{"id":1,"error":{"message":"failure"}}` + "\n", `{"id":1,"result":{}}` + "\n" + `{"id":2,"result":{"data":[{}]}}` + "\n"} {
		if _, err := readCatalog(&bytes.Buffer{}, strings.NewReader(response)); err == nil {
			t.Fatalf("accepted %q", response)
		}
	}
}

// TestNoEffortWithoutAModel: the effort pairs with a model, so a run left
// to codex's own default model is left its default effort too.
func TestNoEffortWithoutAModel(t *testing.T) {
	f := newTestFramework(&recordingRunner{}, "grain", WithEffort("high"))
	home, _, cleanup, err := f.writeConfig(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	config, _ := os.ReadFile(filepath.Join(home, configFileName))
	if strings.Contains(string(config), "model_reasoning_effort") {
		t.Errorf("config = %s, want no model_reasoning_effort for a run that names no model", config)
	}
}

func TestNoEnvironmentManifestAndEffort(t *testing.T) {
	f := newTestFramework(&recordingRunner{}, "grain", WithModel("gpt-5.1-codex"), WithEffort("high"))
	home, _, cleanup, err := f.writeConfig(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(filepath.Join(home, "environments.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "default = \"none\"\ninclude_local = false\n" {
		t.Fatalf("manifest: %s", data)
	}
	config, _ := os.ReadFile(filepath.Join(home, configFileName))
	for _, expected := range []string{`model_reasoning_effort = "high"`, `code_mode_host = true`, `web_search = "disabled"`, `[orchestrator.skills]`, `required = true`, `tool_timeout_sec = 3720`, `enabled_tools = ["write_file","run_command"]`} {
		if !strings.Contains(string(config), expected) {
			t.Errorf("missing %s", expected)
		}
	}
	for _, forbidden := range []string{"command =", "args =", "code_mode_only ="} {
		if strings.Contains(string(config), forbidden) {
			t.Errorf("forbidden %s", forbidden)
		}
	}
}

func TestUnreviewedRuntimeFailsBeforeMCPStartup(t *testing.T) {
	for _, v := range []string{"codex-cli 0.152.0", "codex-cli 0.154.0", "codex-cli 99.0.0", "garbage"} {
		if validateVersion(v) == nil {
			t.Errorf("accepted %q", v)
		}
	}
	if err := validateVersion("codex-cli " + ReviewedCLIVersion + "\n"); err != nil {
		t.Fatal(err)
	}
	f := newTestFramework(&recordingRunner{}, "grain")
	f.checkRuntime = func(context.Context) error { return errors.New("unreviewed CLI") }
	if _, err := f.Run(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "unreviewed") {
		t.Fatalf("Run: %v", err)
	}
	if _, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "unreviewed") {
		t.Fatalf("Terminal: %v", err)
	}
}
