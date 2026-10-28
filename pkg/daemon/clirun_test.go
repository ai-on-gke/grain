package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/whererun"
)

// inAContainer is cfg as a daemon in the release container with no grain
// hostd detects itself.
func inAContainer(cfg Config) Config {
	exec := DetectHostExecutor(cfg, whererun.Detection{Signal: "GRAIN_IN_CONTAINER=1"}, func(string) bool { return false })
	cfg.HostExec = &exec
	return cfg
}

func TestCLIRunPrecedenceIsSettingsThenFlagThenDefault(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     map[string]model.CLIRunSetting
		flag       string
		want, from string
	}{
		{"default", nil, "", model.CLIRunContainer, "default"},
		{"flag", nil, model.CLIRunHost, model.CLIRunHost, "flag"},
		{"settings over flag", map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunContainer}}, model.CLIRunHost, model.CLIRunContainer, "settings"},
		{"an auto flag is the default", nil, "auto", model.CLIRunContainer, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := inAContainer(Config{HostdSocket: "/run/grain-hostd/hostd.sock"})
			c.CLIRun, c.ClaudeRun = tc.stored, tc.flag
			got, from := c.CLIRunSetting(model.AgentFrameworkClaude)
			if got != tc.want || from != tc.from {
				t.Fatalf("CLIRunSetting = %q from %q, want %q from %q", got, from, tc.want, tc.from)
			}
		})
	}
}

// A development daemon on the bare machine -- `grain demo`, a local
// `grain daemon`, the e2e harness -- has no container of its own, so it
// offers only host and runs every CLI as its own child.
func TestABareMachineDaemonOffersOnlyHost(t *testing.T) {
	cfg := Config{AgyPath: "/opt/agy/bin/agy", ClaudePath: "/usr/bin/claude"}
	for _, fw := range model.CLIRunFrameworks {
		r := cfg.ResolveCLIRun(context.Background(), fw)
		if r.Location != model.CLIRunHost || r.Via != ViaDaemon || r.Setting != model.CLIRunHost || r.Source != "default" {
			t.Errorf("%s = %+v, want host, the daemon's own child, by default", fw, r)
		}
		if len(r.Locations) != 1 || r.Locations[0] != model.CLIRunHost {
			t.Errorf("%s offers %v, want only host", fw, r.Locations)
		}
		if r.Warning != "" || r.Err != "" {
			t.Errorf("%s: warning %q, error %q, want neither", fw, r.Warning, r.Err)
		}
	}
	if got := cfg.ResolveCLIRun(context.Background(), "antigravity").Summary(); !strings.HasPrefix(got, "agy: host (/opt/agy/bin/agy)") {
		t.Fatalf("summary = %q", got)
	}
}

// A container setting a release with the per-run docker launcher could
// have stored, or an older unit's flag, runs on the host with a warning
// rather than failing every run; and a new one is refused.
func TestABareMachineDaemonRunsAStoredContainerOnTheHost(t *testing.T) {
	cfg := Config{ClaudePath: "/usr/bin/claude", CodexRun: model.CLIRunContainer,
		CLIRun: map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunContainer}}}
	for _, fw := range []string{model.AgentFrameworkClaude, model.AgentFrameworkCodex} {
		r := cfg.ResolveCLIRun(context.Background(), fw)
		if r.Location != model.CLIRunHost || r.Via != ViaDaemon || r.Err != "" {
			t.Errorf("%s = %+v, want it run on the host", fw, r)
		}
		if !strings.Contains(r.Warning, "set to container") || !strings.Contains(r.Warning, "host or default") {
			t.Errorf("%s: warning %q does not say what is stored and how to clear it", fw, r.Warning)
		}
	}
	err := Config{}.CheckCLIRunSettings(map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunContainer}})
	if err == nil || !strings.Contains(err.Error(), "container is not available") {
		t.Fatalf("PUT container on the bare machine: %v, want it refused", err)
	}
	if err := (Config{}).CheckCLIRunSettings(map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunHost}}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCLIRunFlags(t *testing.T) {
	if err := CheckCLIRunFlags(Config{ClaudeRun: "sideways"}); err == nil {
		t.Fatal("-claude-run=sideways should be refused")
	}
	// A unit an older installer wrote: auto is accepted, and read as unset;
	// container is accepted on the bare machine and runs on the host.
	if err := CheckCLIRunFlags(Config{ClaudeRun: "container", CodexRun: "auto", AgyRun: "host"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckCLIRunFlags(inAContainer(Config{AgyRun: "container"})); err != nil {
		t.Fatalf("-agy-run=container refused, where the daemon's container is the only place agy can run: %v", err)
	}
}

func TestBuildClaudeFrameworkOnTheBareMachineRunsTheHostBinary(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: t.TempDir(), ClaudeRun: "container", ClaudePath: "/usr/local/bin/claude", ClaudeOAuthTokenFile: token}
	fw, err := BuildClaudeFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("a claude set to container on the bare machine should build for the host binary: %v", err)
	}
	if hp, ok := fw.(agent.HostProvider); ok {
		if h, ok := hp.Host().(agent.LocalHost); !ok && hp.Host() != nil {
			t.Fatalf("host = %T (%v), want the daemon's own child", hp.Host(), h)
		}
	}
}
