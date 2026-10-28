package daemon

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// inContainer is how hostmode describes grain's own image.
var inContainer = whererun.Detection{Host: false, Signal: "GRAIN_IN_CONTAINER=1"}

// noHostdLookup fails the test if hostd's socket is looked for at all.
func noHostdLookup(t *testing.T) func(string) bool {
	return func(p string) bool {
		t.Errorf("looked for grain hostd at %s, where there is no host to find it on", p)
		return false
	}
}

// withExecutor is cfg with the executor DetectHostExecutor finds for it.
func withExecutor(cfg Config, where whererun.Detection, socketDirExists func(string) bool) Config {
	e := DetectHostExecutor(cfg, where, socketDirExists)
	cfg.HostExec = &e
	return cfg
}

// captureDaemonLog collects the daemon log for one test and forgets which
// stored host settings have already been warned about, on both sides.
func captureDaemonLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	forget := func() { storedHostWarned.Range(func(k, _ any) bool { storedHostWarned.Delete(k); return true }) }
	forget()
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		forget()
	})
	return &buf
}

func TestKubernetesHasNoHostExecutorAndNeverLooksForHostd(t *testing.T) {
	t.Setenv(PodNameEnv, "grain-0")
	for _, cfg := range []Config{{K8sSandboxes: true}, {K8sGranuleSandboxes: true}} {
		// Even a process that looks like the bare machine: Kubernetes is
		// decided from the config before anything else is read.
		for _, where := range []whererun.Detection{inContainer, {Host: true, Signal: "GRAIN_INSTALL_MODE=host"}} {
			e := DetectHostExecutor(cfg, where, noHostdLookup(t))
			if e.Kind != HostExecutorNone || e.HostAvailable() {
				t.Errorf("DetectHostExecutor(%+v, %+v) = %+v, want no host executor", cfg, where, e)
			}
			if !strings.Contains(e.Reason, "no host executor (Kubernetes)") {
				t.Errorf("reason %q does not say Kubernetes has no host executor", e.Reason)
			}
		}
	}
	// And with no detection at all, Config alone says the same.
	if e := (Config{K8sSandboxes: true}).hostExecutor(); e.Kind != HostExecutorNone {
		t.Errorf("an undetected Kubernetes config's executor is %+v, want none", e)
	}
	if e := (Config{K8sGranuleSandboxes: true}).hostExecutor(); e.Kind != HostExecutorNone {
		t.Errorf("an in-pod K8sGranuleSandboxes config's executor is %+v, want none", e)
	}
}

func TestOffClusterK8sGranuleSandboxesKeepsHostExecutor(t *testing.T) {
	t.Setenv(PodNameEnv, "")
	t.Setenv(PodIPEnv, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	cfg := Config{K8sGranuleSandboxes: true}
	var asked string
	e := DetectHostExecutor(cfg, inContainer, func(p string) bool { asked = p; return true })
	if e.Kind != HostExecutorHostd || e.Socket != "/run/grain-hostd/hostd.sock" || asked != "/run/grain-hostd" {
		t.Fatalf("off-cluster K8sGranuleSandboxes in container: %+v (asked %q), want hostd", e, asked)
	}
	if err := CheckHostExecutorFlags(Config{K8sGranuleSandboxes: true, HostdSocket: "/run/grain-hostd/hostd.sock"}); err != nil {
		t.Fatalf("off-cluster K8sGranuleSandboxes refused -hostd-socket: %v", err)
	}
}

func TestDetectHostExecutorOffTheBareMachine(t *testing.T) {
	if e := DetectHostExecutor(Config{}, whererun.Detection{Host: true, Signal: "no container marker found"}, noHostdLookup(t)); e.Kind != HostExecutorLocal {
		t.Errorf("on the host: %+v, want local", e)
	}
	if e := DetectHostExecutor(Config{}, inContainer, func(string) bool { return false }); e.Kind != HostExecutorNone ||
		!strings.Contains(e.Reason, "no grain hostd") {
		t.Errorf("in a container with no hostd: %+v, want none, saying there is no hostd", e)
	}
	var asked string
	e := DetectHostExecutor(Config{}, inContainer, func(p string) bool { asked = p; return true })
	if e.Kind != HostExecutorHostd || e.Socket != "/run/grain-hostd/hostd.sock" || asked != "/run/grain-hostd" {
		t.Errorf("in a container with hostd's directory mounted: %+v (looked at %q), want hostd at the default socket", e, asked)
	}
	e = DetectHostExecutor(Config{HostdSocket: "/srv/hostd.sock"}, inContainer, func(string) bool { return false })
	if e.Kind != HostExecutorHostd || e.Socket != "/srv/hostd.sock" {
		t.Errorf("with -hostd-socket: %+v, want hostd at that socket", e)
	}
}

// noHostConfigs are the two deployments with no host executor.
func noHostConfigs(t *testing.T) map[string]Config {
	return map[string]Config{
		"kubernetes":         withExecutor(Config{K8sSandboxes: true}, inContainer, noHostdLookup(t)),
		"container no hostd": withExecutor(Config{}, inContainer, func(string) bool { return false }),
	}
}

func TestNoHostExecutorResolvesEveryDefaultToTheDaemonsOwnContainer(t *testing.T) {
	for name, cfg := range noHostConfigs(t) {
		for _, r := range cfg.ResolveCLIRuns(context.Background()) {
			if r.Location != model.CLIRunContainer || r.Via != ViaDaemon {
				t.Errorf("%s: %s resolves to %s via %s, want container via the daemon", name, r.Framework, r.Location, r.Via)
			}
			if r.Setting != model.CLIRunContainer || r.Source != "default" {
				t.Errorf("%s: %s's default is %s (%s), want container", name, r.Framework, r.Setting, r.Source)
			}
			if r.Err != "" || r.Warning != "" {
				t.Errorf("%s: %s: error %q, warning %q", name, r.Framework, r.Err, r.Warning)
			}
			if slices.Contains(r.Locations, model.CLIRunHost) {
				t.Errorf("%s: %s offers %v, which includes host", name, r.Framework, r.Locations)
			}
		}
		if err := CheckCLIRunFlags(cfg); err != nil {
			t.Errorf("%s: default flags refused: %v", name, err)
		}
	}
}

func TestNoHostExecutorRunsAStoredHostInTheContainerAndSaysSoOnce(t *testing.T) {
	for name, cfg := range noHostConfigs(t) {
		buf := captureDaemonLog(t)
		cfg.CLIRun = map[string]model.CLIRunSetting{
			model.AgentFrameworkClaude:      {Location: model.CLIRunHost},
			model.AgentFrameworkAntigravity: {Location: model.CLIRunHost},
		}
		for range 3 {
			for _, fw := range []string{model.AgentFrameworkClaude, model.AgentFrameworkAntigravity} {
				r := cfg.ResolveCLIRun(context.Background(), fw)
				if r.Location != model.CLIRunContainer || r.Via != ViaDaemon || r.Err != "" {
					t.Errorf("%s: a stored host for %s resolves to %+v, want a runnable container", name, fw, r)
				}
				if r.Setting != model.CLIRunHost || !strings.Contains(r.Warning, "no host executor") {
					t.Errorf("%s: %s's resolution does not show the stored host and why it is not used: %+v", name, fw, r)
				}
			}
		}
		for _, cli := range []string{"claude", "agy"} {
			if n := strings.Count(buf.String(), "agent CLI "+cli+" is set to host"); n != 1 {
				t.Errorf("%s: %s's stored host was logged %d times over three resolutions, want once:\n%s", name, cli, n, buf)
			}
		}
	}
}

func TestNoHostExecutorRefusesHostFromTheFlagsAndTheAPI(t *testing.T) {
	for name, cfg := range noHostConfigs(t) {
		flags := cfg
		flags.ClaudeRun = model.CLIRunHost
		err := CheckCLIRunFlags(flags)
		if err == nil || !strings.Contains(err.Error(), "-claude-run=host") || !strings.Contains(err.Error(), "GRAIN_CLAUDE_RUN") ||
			!strings.Contains(err.Error(), "no host executor") {
			t.Errorf("%s: -claude-run=host: %v, want a refusal naming the flag, its variable and why", name, err)
		}
		flags = cfg
		flags.AgyRun = model.CLIRunContainer
		if err := CheckCLIRunFlags(flags); err != nil {
			t.Errorf("%s: -agy-run=container refused, where the daemon's container is the only place agy can run: %v", name, err)
		}

		for _, fw := range model.CLIRunFrameworks {
			err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{fw: {Location: model.CLIRunHost}})
			if err == nil || !strings.Contains(err.Error(), "host is not available") {
				t.Errorf("%s: saving host for %s: %v, want it refused", name, fw, err)
			}
			if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{fw: {Location: model.CLIRunContainer}}); err != nil {
				t.Errorf("%s: saving container for %s: %v", name, fw, err)
			}
		}
	}
	if err := CheckHostExecutorFlags(Config{K8sSandboxes: true, HostdSocket: "/run/grain-hostd/hostd.sock"}); err == nil ||
		!strings.Contains(err.Error(), "Kubernetes") {
		t.Errorf("-hostd-socket on Kubernetes: %v, want it refused", err)
	}
}

func TestHostdConfiguredButDownStillOffersHostAndFailsTheRunClearly(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "hostd.sock") // nothing listens here
	cfg := withExecutor(Config{HostdSocket: socket, AgyRun: model.CLIRunHost}, inContainer, func(string) bool { return false })

	agy := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity)
	if agy.Location != model.CLIRunHost || agy.Via != ViaHostd || agy.Warning != "" {
		t.Errorf("agy set to host with hostd configured resolves to %+v, want host through hostd", agy)
	}
	if !slices.Contains(agy.Locations, model.CLIRunHost) {
		t.Errorf("agy offers %v, want host among them while hostd is configured", agy.Locations)
	}
	claude := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude)
	if claude.Location != model.CLIRunContainer || claude.Via != ViaDaemon {
		t.Errorf("claude's default under hostd resolves to %+v, want the daemon's own container", claude)
	}
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{model.AgentFrameworkClaude: {Location: model.CLIRunHost}}); err != nil {
		t.Errorf("saving host for claude with hostd configured: %v", err)
	}
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{model.AgentFrameworkCustom: {Location: model.CLIRunHost}}); err == nil {
		t.Error("saving host for custom commands under hostd was accepted; they have no allowlist entry to run as")
	}
	if err := CheckCLIRunFlags(cfg); err != nil {
		t.Errorf("-agy-run=host with hostd configured refused at start: %v", err)
	}

	st := cfg.HostExecutorStatus(context.Background())
	if !st.HostAvailable || st.Hostd == nil || st.Hostd.Reachable || !strings.Contains(st.Hostd.Error, "not reachable at "+socket) {
		t.Errorf("Settings' status of a hostd that is down: %+v %+v, want host offered and hostd unreachable", st, st.Hostd)
	}

	// The run is refused before anything is staged, naming hostd's socket.
	if _, err := cfg.cliHostdHost(agy); err == nil || !strings.Contains(err.Error(), "grain hostd is not reachable at "+socket) {
		t.Errorf("a host run with hostd down: %v, want it to fail naming hostd's socket", err)
	}
	// And a host that somehow got past that still fails the same way.
	host := agent.HostdHost{Client: hostd.Client{Socket: socket}, Entry: "agy"}
	_, err := host.Run(context.Background(), agent.Command{Path: "agy", Args: []string{"--version"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "grain hostd is not reachable at "+socket) {
		t.Errorf("a host run with hostd down: %v, want it to fail naming hostd's socket", err)
	}
}

func TestTheBareMachineKeepsHostAndItsOwnRules(t *testing.T) {
	cfg := withExecutor(Config{}, whererun.Detection{Host: true, Signal: "GRAIN_INSTALL_MODE=host"}, noHostdLookup(t))
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{model.AgentFrameworkClaude: {Location: model.CLIRunHost}}); err != nil {
		t.Errorf("saving host on the bare machine: %v", err)
	}
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunContainer}}); err == nil {
		t.Error("saving container for agy on the bare machine was accepted; it has no container launcher there")
	}
	if got := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity); got.Via != ViaDaemon || got.Location != model.CLIRunHost ||
		!slices.Equal(got.Locations, []string{model.CLIRunHost}) {
		t.Errorf("agy on the bare machine: %+v, want host as the daemon's child and nothing else offered", got)
	}
}
