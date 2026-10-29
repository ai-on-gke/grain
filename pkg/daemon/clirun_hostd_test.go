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

//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// startTestHostd serves a real grain hostd on a temporary socket, whose
// allowlist is allowlist (YAML, with %[1]s standing for a shell and %[2]s
// for the temporary directory), and returns the socket.
func startTestHostd(t *testing.T, allowlist string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	// Not t.TempDir: a unix socket's path has to fit in 108 bytes.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(fmt.Sprintf(allowlist, sh, dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv.Socket
}

// offersAgy is an allowlist offering agy and nothing else.
const offersAgy = "clis:\n  agy:\n    path: %[1]s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]\n"

// The matrix for a daemon in the release container: setting × hostd
// absent, answering without the CLI, or offering it. container is
// always the daemon's own container; host is hostd, and a run is
// allowed only for an entry hostd offers.
func TestHostdResolutionMatrix(t *testing.T) {
	hostds := map[string]string{
		"absent":       filepath.Join(t.TempDir(), "no-hostd.sock"),
		"not offering": startTestHostd(t, "clis:\n  claude:\n    path: %[1]s\n    cwd_under: [%[2]s]\n"),
		"offering":     startTestHostd(t, offersAgy),
	}
	type want struct{ location, via, err string }
	for _, tc := range []struct {
		setting string
		want    map[string]want
	}{
		{"", map[string]want{"absent": {"container", ViaDaemon, ""}, "not offering": {"container", ViaDaemon, ""}, "offering": {"container", ViaDaemon, ""}}},
		{"container", map[string]want{"absent": {"container", ViaDaemon, ""}, "not offering": {"container", ViaDaemon, ""}, "offering": {"container", ViaDaemon, ""}}},
		{"host", map[string]want{
			"absent":       {"host", ViaHostd, "grain hostd is not reachable"},
			"not offering": {"host", ViaHostd, `offers no cli entry "agy"`},
			"offering":     {"host", ViaHostd, ""},
		}},
	} {
		for name, socket := range hostds {
			t.Run(fmt.Sprintf("%q/%s", tc.setting, name), func(t *testing.T) {
				cfg := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
				if tc.setting != "" {
					cfg.CLIRun = map[string]model.CLIRunSetting{"antigravity": {Location: tc.setting}}
				}
				r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity)
				w := tc.want[name]
				if r.Location != w.location || r.Via != w.via {
					t.Fatalf("resolved %+v, want %s via %s", r, w.location, w.via)
				}
				if (w.err == "") != (r.Err == "") || !strings.Contains(r.Err, w.err) {
					t.Fatalf("err = %q, want one containing %q", r.Err, w.err)
				}
				// A host run names the binary hostd's allowlist entry
				// runs -- on the host, not the container's own -- and so
				// does its summary line.
				if tc.setting == "host" {
					sh, _ := exec.LookPath("sh")
					sh, _ = filepath.EvalSymlinks(sh)
					wantPath := map[string]string{"offering": sh}[name]
					if r.HostPath != wantPath {
						t.Fatalf("hostPath = %q from the %s hostd, want %q", r.HostPath, name, wantPath)
					}
					if name == "offering" && !strings.Contains(r.Summary(), "agy: host ("+sh+", through grain hostd") {
						t.Fatalf("summary = %q, want it to name %s", r.Summary(), sh)
					}
				}
				// Settings is told whether host would work, whatever the
				// setting is.
				if (r.HostUnavailable == "") != (name == "offering") {
					t.Fatalf("hostUnavailable = %q from the %s hostd", r.HostUnavailable, name)
				}
				_, err := cfg.cliHostdHost(r)
				if w.via == ViaHostd && (err == nil) != (w.err == "") {
					t.Fatalf("cliHostdHost: %v, want an error only when %q", err, w.err)
				}
			})
		}
	}
	// On the bare machine nothing asks hostd.
	r := Config{HostdSocket: hostds["offering"], AgyPath: "/usr/bin/agy"}.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity)
	if r.Via != ViaDaemon || r.HostUnavailable != "" || r.Err != "" {
		t.Fatalf("a daemon on the bare machine resolved agy to %+v", r)
	}
}

func TestAnEditionsHostdDefaultAppliesOnlyBehindHostd(t *testing.T) {
	socket := startTestHostd(t, offersAgy)
	prev := hostdDefaults
	hostdDefaults = map[string]string{}
	t.Cleanup(func() { hostdDefaults = prev })

	cfg := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
	if r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity); r.Location != model.CLIRunContainer {
		t.Fatalf("core build's agy default behind hostd = %+v, want the daemon's own container", r)
	}
	// What the Google build registers.
	RegisterHostdDefault(model.AgentFrameworkAntigravity, model.CLIRunHost)
	got := map[string]CLIRunResolution{}
	for _, r := range cfg.ResolveCLIRuns(context.Background()) {
		got[r.Framework] = r
	}
	if r := got["antigravity"]; r.Location != model.CLIRunHost || r.Via != ViaHostd || r.Source != "default" || r.Err != "" {
		t.Fatalf("agy = %+v, want host through hostd by default", r)
	}
	for _, fw := range []string{"claude", "codex", "custom"} {
		if got[fw].Location != model.CLIRunContainer {
			t.Fatalf("%s = %+v, want container", fw, got[fw])
		}
	}
	// Elsewhere the default is unchanged: no hostd, or the bare machine.
	none := withExecutor(Config{}, inContainer, func(string) bool { return false })
	if r := none.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity); r.Location != model.CLIRunContainer || r.Warning != "" {
		t.Fatalf("agy in a container with no hostd = %+v, want the container with no warning", r)
	}
	bare := withExecutor(Config{AgyPath: "/usr/bin/agy"}, whererun.Detection{Host: true, Signal: "test"}, nil)
	if r := bare.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity); r.Via != ViaDaemon || r.Setting != model.CLIRunHost {
		t.Fatalf("agy on the bare machine = %+v, want its own child", r)
	}
}

func TestBuildFrameworksBehindHostd(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := startTestHostd(t, "clis:\n  claude:\n    path: %[1]s\n    cwd_under: [%[2]s]\n  agy:\n    path: %[1]s\n    cwd_under: [%[2]s]\n")
	cfg := withExecutor(Config{DataDir: t.TempDir(), HostdSocket: socket, ClaudePath: "/bin/sh", ClaudeOAuthTokenFile: token, AgentAuthMode: model.AntigravityAuthAmbient},
		inContainer, nil)

	cfg.CLIRun = map[string]model.CLIRunSetting{"claude": {Location: "host"}}
	fw, err := BuildClaudeFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := fw.(agent.HostProvider).Host().(agent.HostdHost); !ok || h.Entry != "claude" || h.Client.Socket != socket || h.Path == "" {
		t.Fatalf("host = %#v, want agent.HostdHost for entry claude on %s with host path", fw.(agent.HostProvider).Host(), socket)
	}

	cfg.CLIRun = map[string]model.CLIRunSetting{"antigravity": {Location: "host"}}
	agyFW, err := BuildAntigravityFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := agyFW.(agent.HostProvider).Host().(agent.HostdHost); !ok || h.Entry != "agy" || h.Client.Socket != socket || h.Path == "" {
		t.Fatalf("host = %#v, want agent.HostdHost for entry agy on %s with host path", agyFW.(agent.HostProvider).Host(), socket)
	}

	// Not offered: the run fails, saying why.
	cfg.CLIRun = map[string]model.CLIRunSetting{"codex": {Location: "host"}}
	if _, err := buildCodexFramework(context.Background(), cfg, nil); err == nil ||
		!strings.Contains(err.Error(), `offers no cli entry "codex"`) {
		t.Fatalf("codex on a hostd that does not offer it: err = %v", err)
	}
}

func TestAgyModelListerUsesHostdWhenAgyRunsOnHostAndContainerOtherwise(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	dir, err := os.MkdirTemp("", "hostd-agy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Behind hostd, the daemon container's TMPDIR is inside paths_under so
	// writeCatalogHome's staged settings file is visible on the host.
	t.Setenv("TMPDIR", dir)

	hostAgy := filepath.Join(dir, "host-agy")
	hostCalls := filepath.Join(dir, "host-calls")
	hostScript := "#!/bin/sh\n" +
		"echo \"$@\" >> " + hostCalls + "\n" +
		"case \"$1\" in\n" +
		"--help) echo '  --effort  Reasoning effort for the current CLI session (low|medium|high)' ;;\n" +
		"models) printf 'Fetching available models...\\ngemini-3.1-pro-high\\tGemini 3.1 Pro (High)\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(hostAgy, []byte(hostScript), 0o700); err != nil {
		t.Fatal(err)
	}

	// The container's own agy has no ambient corp login: when asked under
	// ambient auth it refuses with "Please sign in", and when given an API
	// key (or run in container mode with its own session) it answers from
	// the container binary.
	containerAgy := filepath.Join(dir, "container-agy")
	containerCalls := filepath.Join(dir, "container-calls")
	containerScript := "#!/bin/sh\n" +
		"echo \"$@\" >> " + containerCalls + "\n" +
		"case \"$1\" in\n" +
		"--help) echo '  --effort  Reasoning effort for the current CLI session (low|medium|high)' ;;\n" +
		"models)\n" +
		"  if [ -z \"$GEMINI_API_KEY\" ]; then\n" +
		"    printf 'Fetching available models...\\nError: Please sign in to view available models. Launch the CLI without arguments to sign in.\\n'\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  printf 'Fetching available models...\\ngemini-3.8-flash-low\\tGemini 3.8 Flash (Low)\\n'\n" +
		"  ;;\n" +
		"esac\n"
	if err := os.WriteFile(containerAgy, []byte(containerScript), 0o700); err != nil {
		t.Fatal(err)
	}

	allowPath := filepath.Join(dir, "hostd.yaml")
	allowYAML := fmt.Sprintf("clis:\n  agy:\n    path: %s\n    paths_under: [%s]\n    cwd_under: [%s]\n    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]\n",
		hostAgy, dir, dir)
	if err := os.WriteFile(allowPath, []byte(allowYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allowPath,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}

	keyFile := filepath.Join(dir, "gemini-key")
	if err := os.WriteFile(keyFile, []byte("test-api-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. agy run=host (ambient auth): probes through hostd's agy entry and
	// lists the host binary's models without touching the container's
	// unsigned-in agy.
	hostCfg := withExecutor(Config{
		HostdSocket:   srv.Socket,
		AgyPath:       containerAgy,
		AgentAuthMode: model.AntigravityAuthAmbient,
		CLIRun:        map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunHost}},
	}, inContainer, nil)
	lister := &AgyModelLister{Live: &LiveConfig{Applied: hostCfg}}
	gotHost, err := lister.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels with agy run=host: %v", err)
	}
	if len(gotHost.Models) != 1 || gotHost.Models[0].ID != "gemini-3.1-pro-high" {
		t.Fatalf("Models with agy run=host = %+v, want [gemini-3.1-pro-high] from hostd's agy", gotHost.Models)
	}
	if _, err := os.Stat(containerCalls); !os.IsNotExist(err) {
		t.Fatalf("container agy was invoked when agy run=host")
	}

	// 2. agy run=container: probes the container's agy directly (and does
	// not serve the cached hostd catalog).
	containerCfg := withExecutor(Config{
		HostdSocket:      srv.Socket,
		AgyPath:          containerAgy,
		GeminiAPIKeyFile: keyFile,
		CLIRun:           map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunContainer}},
	}, inContainer, nil)
	lister.Live = &LiveConfig{Applied: containerCfg}
	gotContainer, err := lister.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels with agy run=container: %v", err)
	}
	if len(gotContainer.Models) != 1 || gotContainer.Models[0].ID != "gemini-3.8-flash-low" {
		t.Fatalf("Models with agy run=container = %+v, want [gemini-3.8-flash-low] from container's agy", gotContainer.Models)
	}
}

// Settings lists what hostd holds -- a detached run's CLI, which the next
// daemon adopts across an upgrade -- beside hostd's build.
func TestSettingsListsHostdsDetachedRunsAndItsBuild(t *testing.T) {
	socket := startTestHostd(t, offersAgy)
	dir := filepath.Dir(socket)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	client := hostd.Client{Socket: socket, OnStarted: func(hostd.Started) { close(started) }}
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		client.Run(ctx, hostd.Request{Kind: hostd.KindCLI, Name: "agy", Args: []string{"-c", "sleep 30"}, Dir: dir,
			Detach: true, Run: "9-1", Task: "9", Deadline: time.Now().Add(time.Minute)}, nil, nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("hostd never started the detached run")
	}

	cfg := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
	st := cfg.HostExecutorStatus(context.Background())
	if st.Hostd == nil || !st.Hostd.Reachable || st.Hostd.Protocol != hostd.ProtocolVersion {
		t.Fatalf("hostd status = %+v", st.Hostd)
	}
	if len(st.Hostd.Processes) != 1 {
		t.Fatalf("processes = %+v, want the one detached run", st.Hostd.Processes)
	}
	p := st.Hostd.Processes[0]
	if p.Run != "9-1" || p.Task != "9" || p.Kind != "cli" || p.Name != "agy" || p.PID == 0 || !p.Detached || p.Exited {
		t.Fatalf("process = %+v", p)
	}

	// Released, as a daemon with no row for it would.
	if err := (hostd.Client{Socket: socket}).Cancel(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-ran
}

// Settings suggests the entry an edition's default puts on the host when
// the operator's allowlist has none of it -- and only suggests it.
func TestHostdStatusSuggestsAMissingDefaultEntry(t *testing.T) {
	socket := startTestHostd(t, "capabilities:\n  search:\n    command: %[1]s\n# %[2]s\n")
	prev := hostdDefaults
	hostdDefaults = map[string]string{}
	t.Cleanup(func() { hostdDefaults = prev })
	RegisterHostdDefault(model.AgentFrameworkAntigravity, model.CLIRunHost)

	cfg := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
	st := cfg.HostExecutorStatus(context.Background())
	if st.Hostd == nil || !st.Hostd.Reachable {
		t.Fatalf("hostd status = %+v, want reachable", st.Hostd)
	}
	if fmt.Sprint(st.Hostd.Suggest) != "[cli agy]" {
		t.Errorf("suggestions = %v, want [cli agy]", st.Hostd.Suggest)
	}

	offered := withExecutor(Config{HostdSocket: startTestHostd(t, offersAgy)}, inContainer, nil)
	if s := offered.HostExecutorStatus(context.Background()).Hostd.Suggest; len(s) != 0 {
		t.Errorf("an allowlist that offers agy is still told to add %v", s)
	}
	moved := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
	moved.CLIRun = map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunContainer}}
	if s := moved.HostExecutorStatus(context.Background()).Hostd.Suggest; len(s) != 0 {
		t.Errorf("a deployment that moved agy into the container is still told to add %v", s)
	}
}

// offersClaudeDetached is an allowlist with the container-equivalent
// claude and codex entries setup.sh writes, and the operator's own claude beside it.
const offersClaudeDetached = "clis:\n  claude:\n    path: %[1]s\n    cwd_under: [%[2]s]\n  claude-detached:\n    path: %[1]s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n  codex-detached:\n    path: %[1]s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n"

// detached is claude's and codex's container-equivalent entry on hostd when
// hostd offers it, and otherwise a container run -- never a failed one -- with
// Settings told why. It is offered for claude and codex, and only behind
// hostd.
func TestDetachedResolution(t *testing.T) {
	sh, _ := exec.LookPath("sh")
	sh, _ = filepath.EvalSymlinks(sh)
	hostds := map[string]string{
		"absent":       filepath.Join(t.TempDir(), "no-hostd.sock"),
		"not offering": startTestHostd(t, "clis:\n  claude:\n    path: %[1]s\n    cwd_under: [%[2]s]\n"),
		"offering":     startTestHostd(t, offersClaudeDetached),
	}
	for name, socket := range hostds {
		t.Run(name, func(t *testing.T) {
			cfg := withExecutor(Config{HostdSocket: socket}, inContainer, nil)
			cfg.CLIRun = map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunDetached}}
			r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude)
			if r.Err != "" {
				t.Fatalf("a detached setting failed the run: %+v", r)
			}
			if name == "offering" {
				if r.Location != model.CLIRunDetached || r.Via != ViaHostd || r.Entry != "claude-detached" || r.HostPath != sh || r.Warning != "" {
					t.Fatalf("resolved %+v, want detached through hostd's claude-detached at %s", r, sh)
				}
				if !strings.Contains(r.Summary(), `claude: detached (`+sh+`, through grain hostd, entry "claude-detached")`) {
					t.Errorf("summary = %q", r.Summary())
				}
				h, err := cfg.cliHostdHost(r)
				if hh, ok := h.(agent.HostdHost); err != nil || !ok || hh.Entry != "claude-detached" {
					t.Fatalf("cliHostdHost = %#v, %v; want entry claude-detached", h, err)
				}
				return
			}
			want := map[string]string{"absent": "grain hostd is not reachable", "not offering": `offers no cli entry "claude-detached"`}[name]
			if r.Location != model.CLIRunContainer || r.Via != ViaDaemon || !strings.Contains(r.Warning, want) {
				t.Fatalf("resolved %+v, want the container with a warning containing %q", r, want)
			}
		})
	}

	cfg := withExecutor(Config{HostdSocket: hostds["offering"]}, inContainer, nil)
	if got := cfg.cliRunLocations(model.AgentFrameworkClaude); !slices.Contains(got, model.CLIRunDetached) {
		t.Errorf("claude's locations behind hostd = %v, want detached among them", got)
	}
	if got := cfg.cliRunLocations(model.AgentFrameworkCodex); !slices.Contains(got, model.CLIRunDetached) {
		t.Errorf("codex's locations behind hostd = %v, want detached among them", got)
	}
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{"claude": {Location: "detached"}}); err != nil {
		t.Errorf("claude detached behind hostd refused: %v", err)
	}
	if err := cfg.CheckCLIRunSettings(map[string]model.CLIRunSetting{"codex": {Location: "detached"}}); err != nil {
		t.Errorf("codex detached behind hostd refused: %v", err)
	}
	cfg.ClaudeRun = model.CLIRunDetached
	cfg.CodexRun = model.CLIRunDetached
	if err := CheckCLIRunFlags(cfg); err != nil {
		t.Errorf("-claude-run=detached -codex-run=detached behind hostd: %v", err)
	}
	if r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude); r.Location != model.CLIRunDetached || r.Source != "flag" {
		t.Errorf("-claude-run=detached resolved to %+v", r)
	}
	if r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkCodex); r.Location != model.CLIRunDetached || r.Entry != "codex-detached" || r.Source != "flag" {
		t.Errorf("-codex-run=detached resolved to %+v", r)
	}

	// Without hostd there is nothing to detach through: the flag is
	// refused at start, and a stored setting runs in the container.
	none := withExecutor(Config{ClaudeRun: model.CLIRunDetached}, inContainer, func(string) bool { return false })
	if err := CheckCLIRunFlags(none); err == nil || !strings.Contains(err.Error(), "-claude-run=detached") {
		t.Errorf("-claude-run=detached with no hostd: %v", err)
	}
	none.ClaudeRun = ""
	none.CLIRun = map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunDetached}}
	if r := none.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude); r.Location != model.CLIRunContainer || r.Warning == "" {
		t.Errorf("a stored detached with no hostd = %+v, want the container with a warning", r)
	}
	if err := none.CheckCLIRunSettings(none.CLIRun); err == nil {
		t.Error("detached accepted with no hostd")
	}
}

// Settings -> Models and the upgrade pane say which CLIs a daemon
// upgrade interrupts: claude and codex in the container, which detached
// would carry -- but not when detached.
func TestUpgradeInterruptsNamesTheCLIsAnUpgradeLoses(t *testing.T) {
	cfg := withExecutor(Config{HostdSocket: startTestHostd(t, offersClaudeDetached)}, inContainer, nil)
	cfg.CLIRun = map[string]model.CLIRunSetting{
		"claude": {Location: model.CLIRunDetached},
		"codex":  {Location: model.CLIRunDetached},
	}
	if r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude); r.UpgradeInterrupts != "" {
		t.Errorf("a detached claude says an upgrade interrupts it: %q", r.UpgradeInterrupts)
	}
	if r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkCodex); r.UpgradeInterrupts != "" {
		t.Errorf("a detached codex says an upgrade interrupts it: %q", r.UpgradeInterrupts)
	}
	cfg.CLIRun = map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunContainer}}
	r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkClaude)
	if !strings.Contains(r.UpgradeInterrupts, "A daemon upgrade interrupts claude runs") || !strings.Contains(r.UpgradeInterrupts, "Set it to detached") {
		t.Errorf("claude in the container: %q, want it interrupted, with detached named as the cure", r.UpgradeInterrupts)
	}
	r = cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkCodex)
	if !strings.Contains(r.UpgradeInterrupts, "A daemon upgrade interrupts codex runs") || !strings.Contains(r.UpgradeInterrupts, "Set it to detached") {
		t.Errorf("codex in the container: %q, want it interrupted, with detached named as the cure", r.UpgradeInterrupts)
	}
	// The bare machine has no upgrade pane to say it in.
	bare := withExecutor(Config{}, whererun.Detection{Host: true}, nil)
	if r := bare.ResolveCLIRun(context.Background(), model.AgentFrameworkCodex); r.UpgradeInterrupts != "" {
		t.Errorf("a development daemon: %q", r.UpgradeInterrupts)
	}
}

// A detached claude is a container run moved, not a host run: it is sent
// the deployment's OAuth token, as a container run is, and forks its
// mcpserver itself -- from the grain at this binary's path, which the
// installer puts at the same path on the host -- rather than reaching one
// served from this daemon, which would end with it.
func TestBuildClaudeFrameworkDetachedSendsTheTokenAndForksItsMCPServer(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("deployment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := startTestHostd(t, offersClaudeDetached)
	t.Setenv("TMPDIR", t.TempDir())
	cfg := withExecutor(Config{DataDir: t.TempDir(), HostdSocket: socket, ClaudeOAuthTokenFile: token}, inContainer, nil)
	cfg.CLIRun = map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunDetached}}
	fw, err := BuildClaudeFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := fw.(agent.HostProvider).Host().(agent.HostdHost)
	if !ok || h.Entry != "claude-detached" || h.Client.Socket != socket {
		t.Fatalf("host = %#v, want agent.HostdHost for entry claude-detached on %s", fw.(agent.HostProvider).Host(), socket)
	}
	tc, err := fw.(agent.TerminalFramework).TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tc.Env, "CLAUDE_CODE_OAUTH_TOKEN=deployment-token") {
		t.Errorf("env = %v, want the deployment's token", tc.Env)
	}
	config := ""
	for i, a := range tc.Args {
		if a == "--mcp-config" && i+1 < len(tc.Args) {
			b, err := os.ReadFile(tc.Args[i+1])
			if err != nil {
				t.Fatal(err)
			}
			config = string(b)
		}
	}
	self, _ := os.Executable()
	if !strings.Contains(config, `"command"`) || !strings.Contains(config, self) || strings.Contains(config, `"url"`) {
		t.Errorf("mcp config = %s, want grain's mcpserver forked from %s, not an endpoint in this daemon", config, self)
	}
}

// A detached codex shares the deployment's auth.json symlink in CODEX_HOME
// and writes mcpserver.json instead of starting an HTTP MCP adapter inside
// the daemon, so the host wrapper (grain codex-detached) can serve it
// alongside codex across a daemon upgrade.
func TestBuildCodexFrameworkDetachedSharesAuthFileAndDefersMCPServer(t *testing.T) {
	dataDir := t.TempDir()
	authFile := ManagedCodexAuthFile(dataDir)
	if err := os.MkdirAll(filepath.Dir(authFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authFile, []byte(`{"tokens":{"access_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\necho 'codex-cli "+codex.ReviewedCLIVersion+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	allowlist := fmt.Sprintf("clis:\n  codex-detached:\n    path: %s\n    paths_under: [%%[2]s, %s]\n    cwd_under: [%%[2]s, %s]\n# %%[1]s\n", fakeCodex, dataDir, os.TempDir())
	socket := startTestHostd(t, allowlist)
	t.Setenv("TMPDIR", t.TempDir())
	cfg := withExecutor(Config{DataDir: dataDir, HostdSocket: socket}, inContainer, nil)
	cfg.CLIRun = map[string]model.CLIRunSetting{"codex": {Location: model.CLIRunDetached}}
	fw, err := buildCodexFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := fw.(agent.HostProvider).Host().(agent.HostdHost)
	if !ok || h.Entry != "codex-detached" || h.Client.Socket != socket {
		t.Fatalf("host = %#v, want agent.HostdHost for entry codex-detached on %s", fw.(agent.HostProvider).Host(), socket)
	}
	tc, err := fw.(agent.TerminalFramework).TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if tc.Cleanup != nil {
			tc.Cleanup()
		}
	})
	var codexHome string
	for _, kv := range tc.Env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			codexHome = v
		}
	}
	if codexHome == "" {
		t.Fatalf("env = %v, want CODEX_HOME", tc.Env)
	}
	target, err := os.Readlink(filepath.Join(codexHome, "auth.json"))
	if err != nil || target != authFile {
		t.Fatalf("auth.json symlink = %q, %v; want %q", target, err, authFile)
	}
	specBytes, err := os.ReadFile(filepath.Join(codexHome, "mcpserver.json"))
	if err != nil {
		t.Fatalf("mcpserver.json missing in detached CODEX_HOME: %v", err)
	}
	if !strings.Contains(string(specBytes), `"args"`) || !strings.Contains(string(specBytes), `"mcpserver"`) {
		t.Errorf("mcpserver.json = %s, want mcpserver args", specBytes)
	}
	configBytes, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml missing: %v", err)
	}
	if strings.Contains(string(configBytes), "[mcp_servers.grain-sandbox]") {
		t.Errorf("config.toml = %s, want [mcp_servers.grain-sandbox] deferred to grain codex-detached", configBytes)
	}
}

// The upgrade check is told the same notes, read with the run locations
// the store holds now rather than the flags alone: a claude a person has
// since set to detached is no longer expected to be lost, and an edition's
// caveat rides along with the offer of detached (task 1506).
func TestUpgradeInterruptNotesReadTheStoredRunLocations(t *testing.T) {
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	cfg := withExecutor(Config{HostdSocket: startTestHostd(t, offersClaudeDetached), ClaudeRun: model.CLIRunContainer}, inContainer, nil)
	defer func(old map[string]string) { detachedCaveats = old }(detachedCaveats)
	detachedCaveats = map[string]string{model.AgentFrameworkClaude: "Check the exec policy first."}

	notes := cfg.upgradeInterruptNotes(ctx, store)
	if !strings.Contains(notes["claude"], "A daemon upgrade interrupts claude runs") ||
		!strings.Contains(notes["claude"], "Set it to detached to carry its runs across an upgrade. Check the exec policy first.") {
		t.Errorf("claude set to container by its flag: %q, want it interrupted, with the caveat after detached", notes["claude"])
	}
	if notes["codex"] == "" {
		t.Errorf("codex is not named: %v", notes)
	}
	if err := store.PutConfig(ctx, model.Config{CLIRun: map[string]model.CLIRunSetting{"claude": {Location: model.CLIRunDetached}}}); err != nil {
		t.Fatal(err)
	}
	if notes := cfg.upgradeInterruptNotes(ctx, store); notes["claude"] != "" {
		t.Errorf("claude stored as detached is still expected to be lost: %q", notes["claude"])
	}
}
