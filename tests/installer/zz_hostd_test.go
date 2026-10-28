package installer

// The Google-track install shape (docs/design/cli-run-location.md, "Host
// CLIs through grain hostd"): the daemon in the release container, and
// agy -- and a `run: host` capability server -- on the bare machine
// through grain hostd, as the installing user. Host mode is gone (task
// 5041), so this drives every shape a machine install has left: the
// package's own install, made with GRAIN_HOSTD=0; a host-mode install as
// an older installer left it, which GRAIN_INSTALL_MODE=host no longer
// recreates and a re-run migrates to the default -- container mode plus
// hostd; and a re-run with GRAIN_HOSTD=0 that switches hostd off again.
//
// It re-installs the package's one deployment several times over, so it
// lives in the file Go runs last ("zz_") and leaves the deployment for
// TestMain's uninstall rather than for any other test.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

const (
	hostdUnit       = "/etc/systemd/system/grain-hostd.service"
	hostdAllowlist  = "/etc/grain/hostd.yaml"
	hostdSocket     = "/run/grain-hostd/hostd.sock"
	daemonUnit      = "/etc/systemd/system/grain-daemon.service"
	hostdVersions   = "/usr/local/lib/grain/hostd"
	hostdExtractor  = "/usr/local/lib/grain/extract-host-binaries.sh"
	hostdCapID      = "https://github.com/bwsalmon/grain/capabilities/hostd-e2e"
	hostScriptCapID = "https://github.com/bwsalmon/grain/capabilities/host-script"
)

// setupAgain re-runs this deployment's setup.sh with extra NAME=value
// settings on top of the ones the package installed it with.
func (d *deployment) setupAgain(t *testing.T, extra ...string) result {
	t.Helper()
	gateway := strings.TrimSpace(sudo("docker", "network", "inspect", "bridge",
		"-f", "{{(index .IPAM.Config 0).Gateway}}").stdout)
	argv := setupArgv(d, d.image, filepath.Join(d.root, "minter-key.json"), d.secretsKey, gateway)
	script := argv[len(argv)-2:]
	argv = append(append(append([]string{}, argv[:len(argv)-2]...), extra...), script...)
	return sudo(argv...)
}

// hostdFixture is what the test dispatches against: where its pieces are,
// and the report each run writes.
type hostdFixture struct {
	hostOnly  string // a file on the host and nowhere in the container
	agy       string // the fake agy, outside every directory the container mounts
	probe     string // tests/installer/hostscriptprobe, beside it
	claude    string // the fake claude, under the data directory (mounted)
	reports   string // where both write what they saw, owned by the service account
	capReport string
	uid       string
	// client and accessToken reach whichever daemon is installed now;
	// waitForAPI renews them after each reinstall.
	client      *ui.HTTPClient
	accessToken string
}

func TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode(t *testing.T) {
	requireInstaller(t)
	d := deployed(t)
	if d.setup.exitCode != 0 {
		t.Fatalf("successful setup prerequisite failed: %s", d.setup)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	f := newHostdFixture(t, d)
	f.waitForAPI(t, ctx, d)

	// 0. The package's own install is a fresh one with GRAIN_HOSTD=0: no
	// hostd, nothing of it in the daemon's unit, and "host" not offered.
	if !strings.Contains(d.setup.stdout, "hostd:             not installed (GRAIN_HOSTD=0)") {
		t.Errorf("a GRAIN_HOSTD=0 install's readiness report does not say hostd is not installed:\n%s", tail(d.setup.stdout, 60))
	}
	f.assertNoHostd(t, ctx, d, "a fresh GRAIN_HOSTD=0 install")

	// Something the store has before any of this, which every later
	// shape must still have: the data directory carried across.
	keep, err := f.client.CreateTask(ctx, ui.CreateTaskRequest{Title: "kept across install modes", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("filing the task the migrations must keep: %v", err)
	}

	// 1. A host-mode install, which is what every Cloudtop had before. No
	// installer writes one any more, so it is put together the way an
	// older one left it: the unit, the drop-in a host-mode Upgrade wrote,
	// the nft sudoers rule, the extractor's marker and the source copy.
	f.becomeAHostModeInstall(t, d)
	refused := d.setupAgain(t, "GRAIN_INSTALL_MODE=host")
	if refused.exitCode == 0 {
		t.Fatalf("GRAIN_INSTALL_MODE=host was accepted: %s", refused)
	}
	if out := refused.stdout + refused.stderr; !strings.Contains(out, "GRAIN_INSTALL_MODE=host is no longer supported") ||
		!strings.Contains(out, "container mode plus grain hostd") {
		t.Errorf("the refusal does not point to container mode plus grain hostd: %s", refused)
	}
	if unit := sudoRead(daemonUnit); !strings.Contains(unit, "Environment=GRAIN_INSTALL_MODE=host") {
		t.Fatalf("a refused GRAIN_INSTALL_MODE=host changed the installed unit:\n%s", unit)
	}

	// 2. The migration: a re-run with no mode and hostd left at its
	// default, the way a Cloudtop's installer now runs setup.sh.
	migrated := d.setupAgain(t, f.googleTrack()...)
	if migrated.exitCode != 0 {
		t.Fatalf("migrating to container mode plus hostd: %s", migrated)
	}
	for _, want := range []string{"moving this host-mode install to container mode plus grain hostd",
		"hostd:             installed", "clis: claude-detached, codex-detached, agy"} {
		if !strings.Contains(migrated.stdout, want) {
			t.Errorf("the migration does not say %q:\n%s", want, tail(migrated.stdout, 60))
		}
	}
	for _, left := range []string{"/etc/systemd/system/grain-daemon.service.d/grain.conf", "/etc/sudoers.d/zzz-grain-nft",
		"/usr/local/lib/grain/.extracted-image-id", "/usr/local/share/grain/src"} {
		if sudoTest("-e", left) {
			t.Errorf("the migration left host mode's %s behind", left)
		}
	}
	f.assertContainerModeWithHostd(t, d, "after the migration")
	f.waitForAPI(t, ctx, d)
	f.mustStillHave(t, ctx, keep.ID, "the migration")
	f.assertHostdOffersAgy(t, ctx, d)

	// An ambient git connection's gh, a registered source's tool or git credential runs through
	// hostd as well (pkg/gitproxy/hostgit.go): the allowlist offers
	// grain's own host-git server with a fixed command line, and the
	// containerised daemon -- which refuses an ambient connection with no
	// host to borrow a login from -- accepts one because hostd offers it.
	if allow := sudoRead(hostdAllowlist); !strings.Contains(allow, "  host-git:\n    command: /usr/local/bin/grain\n    args: [capability, host-git]\n") {
		t.Errorf("the allowlist offers no host-git entry:\n%s", allow)
	}
	if !strings.Contains(migrated.stdout, "capabilities: host-git") {
		t.Errorf("the readiness report does not list host-git:\n%s", tail(migrated.stdout, 60))
	}
	if added := sudo("/usr/local/bin/grain", "git-ambient", "-host", "github.com", "-kind", "github", "-source", "gh"); added.exitCode != 0 {
		t.Errorf("the containerised daemon refused an ambient connection beside a hostd offering host-git: %s", added)
	}
	if added := sudo("/usr/local/bin/grain", "git-ambient", "-host", "127.0.0.1:8088", "-kind", "git", "-base-url", "http://127.0.0.1:8088", "-source", "git"); added.exitCode != 0 {
		t.Errorf("the containerised daemon refused a plain-git ambient connection beside a hostd offering host-git: %s", added)
	}

	approved := false
	backends := []model.AgentBackend{
		{ID: "hostd-agy", Name: "agy through hostd", Framework: model.AgentFrameworkAntigravity,
			AuthMode: model.AntigravityAuthAmbient, Models: []string{"fake-agy-model"}},
		// Ambient: the fake needs no token, and with none grain would
		// refuse the run before starting it.
		{ID: "container-claude", Name: "claude in the container", Framework: model.AgentFrameworkClaude,
			AuthMode: model.AntigravityAuthAmbient, Models: []string{"fake-claude-model"}},
	}
	if _, err := f.client.UpdateSettings(ctx, ui.UpdateSettingsRequest{ApprovedByDefault: &approved, AgentBackends: &backends}); err != nil {
		t.Fatalf("configuring the fake agy and claude backends: %v", err)
	}

	// 3. claude stays in the daemon's own container: a subprocess there.
	t.Run("claude runs as a subprocess in the daemon's container", func(t *testing.T) {
		report := filepath.Join(f.reports, "claude.report")
		attempt := f.dispatch(t, ctx, model.AgentFrameworkClaude, "fake claude", "")
		got := sudoRead(report)
		if !strings.Contains(got, "in_container=true") || !strings.Contains(got, "host_only_seen=false") {
			t.Fatalf("the fake claude did not run in the daemon's container: %q (attempt %s: %s)", got, attempt.Outcome, attempt.Detail)
		}
		if !strings.Contains(got, "uid="+f.uid+" ") {
			t.Fatalf("the fake claude did not run as %s's uid %s: %q", d.user, f.uid, got)
		}
	})

	// 4. agy goes through hostd: on the host, as the installing user,
	// with its MCP tool calls going through the host's own grain.
	t.Run("agy runs on the host through hostd, with its MCP tools and transcript", func(t *testing.T) {
		report := filepath.Join(f.reports, "agy.report")
		attempt := f.dispatch(t, ctx, model.AgentFrameworkAntigravity, "fake agy",
			"report="+report+" host_only="+f.hostOnly)
		var r struct {
			UID          int      `json:"uid"`
			Home         string   `json:"home"`
			EnvNames     []string `json:"envNames"`
			HostOnlySeen bool     `json:"hostOnlySeen"`
			InContainer  bool     `json:"inContainer"`
			MCPCommand   string   `json:"mcpCommand"`
			MCPArgs      []string `json:"mcpArgs"`
			MCPStderr    string   `json:"mcpStderr"`
			ToolResults  []string `json:"toolResults"`
			ToolErrors   []string `json:"toolErrors"`
		}
		raw := sudoRead(report)
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("the fake agy left no report (attempt %s: %s): %q", attempt.Outcome, attempt.Detail, raw)
		}
		if r.InContainer || !r.HostOnlySeen {
			t.Errorf("agy did not run on the host: in_container=%t, saw the host-only file=%t", r.InContainer, r.HostOnlySeen)
		}
		if fmt.Sprint(r.UID) != f.uid {
			t.Errorf("agy ran as uid %d, want %s's %s", r.UID, d.user, f.uid)
		}
		if !strings.HasPrefix(r.Home, d.data+"/") {
			t.Errorf("agy's HOME %q is not under the data directory %s, which both sides mount", r.Home, d.data)
		}
		if r.MCPCommand != "/usr/local/bin/grain" {
			t.Errorf("agy's MCP server is %q, want the host's /usr/local/bin/grain", r.MCPCommand)
		}
		if len(r.ToolErrors) != 0 || len(r.ToolResults) != 2 {
			t.Errorf("agy's MCP tool calls through the host's grain (%s %q): results %q, errors %q, server stderr:\n%s",
				r.MCPCommand, r.MCPArgs, r.ToolResults, r.ToolErrors, r.MCPStderr)
		}
		// Its environment is the allowlist's, not the daemon's: of grain's
		// own variables only the token file set_env names, which the
		// mcpserver it forks needs to reach the daemon's API.
		for _, name := range r.EnvNames {
			if strings.HasPrefix(name, "GRAIN_") && name != "GRAIN_LOCAL_API_TOKEN_FILE" {
				t.Errorf("hostd passed agy the daemon's %s; it passes only what the allowlist names", name)
			}
		}
		if !contains(r.EnvNames, "NO_PROXY") {
			t.Errorf("agy was not given the proxy setup.sh recorded (pass_env); it got %v", r.EnvNames)
		}
		transcript, err := f.client.AttemptTranscript(ctx, attempt.taskID, attempt.Number)
		if err != nil {
			t.Fatalf("reading the attempt's transcript: %v", err)
		}
		for _, want := range []string{"run_command", "write_file", "fake agy finished on the host"} {
			if !strings.Contains(transcript, want) {
				t.Errorf("the transcript does not carry %q:\n%s", want, tail(transcript, 40))
			}
		}
	})

	// 5. A stdio capability server with run: host, through hostd.
	t.Run("a run: host capability server starts on the host through hostd", func(t *testing.T) {
		f.editAllowlist(t, "capabilities:\n", fmt.Sprintf("capabilities:\n  fakecap:\n    command: %s\n    args: [capability-server, %s]\n", f.agy, f.capReport))
		if _, err := f.client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{
			ID: hostdCapID, Config: protocol.ServerConfig{Command: "fakecap", Run: protocol.RunHost},
		}); err != nil {
			t.Fatalf("adding the run: host capability server: %v", err)
		}
		waitFor(t, "the run: host capability server to attach", 90*time.Second, func() error {
			list, err := f.client.ListCapabilityServers(ctx)
			if err != nil {
				return err
			}
			for _, s := range list {
				if s.ID != hostdCapID {
					continue
				}
				if s.Status == nil || s.Status.State != "attached" {
					return fmt.Errorf("status %+v", s.Status)
				}
				if !contains(s.Status.Methods, "host-probe") {
					return fmt.Errorf("attached with methods %v", s.Status.Methods)
				}
				return nil
			}
			return errors.New("not listed")
		})
		got := sudoRead(f.capReport)
		if !strings.Contains(got, "uid="+f.uid+" ") || !strings.Contains(got, "in_container=false") {
			t.Fatalf("the capability server did not start on the host as %s: %q", d.user, got)
		}
	})

	// 5b. The host-script server as an operator installs it: an allowlist
	// entry for the host's own grain, and a run: host mcpServers entry.
	// The daemon attaches it; then the probe, as the installing user,
	// mints a grant through the same hostd entry and calls it the way
	// `grain host-script whoami` does. Its script reports `id -u`, which
	// is the installing user's, and the grant's port belongs to the pid hostd reported
	// starting -- what lets grain's relay forward a run: host server's
	// ports. Called on the host, not in a sandbox: this deployment's
	// container sandboxes run the published k8s-sandbox:main
	// (scripts/installer-test-image.sh), and until that image is built
	// from a Dockerfile.k8s-sandbox with the debian user and python3 it
	// cannot take a capability placement. tests.yml's
	// host-script-container job covers the in-sandbox call against this
	// commit's image meanwhile.
	t.Run("a run: host host-script server runs its scripts as the installing user", func(t *testing.T) {
		data, scratch := filepath.Join(f.reports, "host-script"), filepath.Join(f.reports, "host-script-probe")
		for _, dir := range []string{data, scratch} {
			if r := sudo("install", "-d", "-m0700", "-o", d.user, dir); r.exitCode != 0 {
				t.Fatalf("making %s: %s", dir, r)
			}
		}
		f.editAllowlist(t, "capabilities:\n", fmt.Sprintf("capabilities:\n  host-script:\n    command: /usr/local/bin/grain\n    args: [capability, host-script, -listen, 127.0.0.1:0, -data-dir, %s]\n", data))
		// Saved first with no config and then configured, as Settings
		// does it: grain checks a config against the schema the attached
		// server reports, and the Configure form is offered only then.
		cfg := protocol.ServerConfig{Command: "host-script", Run: protocol.RunHost}
		attached := func(method string) {
			waitFor(t, "the run: host host-script server to attach", 90*time.Second, func() error {
				list, err := f.client.ListCapabilityServers(ctx)
				if err != nil {
					return err
				}
				for _, s := range list {
					if s.ID != hostScriptCapID {
						continue
					}
					if s.Status == nil || s.Status.State != "attached" {
						return fmt.Errorf("status %+v", s.Status)
					}
					if method != "" && !contains(s.Status.Methods, method) {
						return fmt.Errorf("attached with methods %v", s.Status.Methods)
					}
					return nil
				}
				return errors.New("not listed")
			})
		}
		if _, err := f.client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: hostScriptCapID, Config: cfg}); err != nil {
			t.Fatalf("adding the run: host host-script server: %v", err)
		}
		attached("")
		cfg.Config = map[string]any{"scripts": []any{map[string]any{"name": "whoami", "description": "reports who it runs as", "script": "id -u", "timeout": 10}}}
		if _, err := f.client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: hostScriptCapID, Config: cfg}); err != nil {
			t.Fatalf("configuring the run: host host-script server: %v", err)
		}
		attached("host-script")
		// The server hostd starts is the image's grain, which carries a
		// file capability (Dockerfile's setcap cap_net_bind_service): it
		// restores PR_SET_DUMPABLE on startup (protocol.MakeDumpable),
		// `grain capability` drops the capability it was exec'd with
		// (protocol.DropCapabilities: a process holding one its same-uid
		// inspector lacks keeps /proc/<pid>/fd/<n> closed to it, dumpable
		// or not), and hostd answers OpSocketOwner for processes in its
		// own trees, so the unprivileged probe and the daemon's relay
		// verify the grant port's owner without extra capabilities.
		out := run("sudo", "-n", "-u", d.user, f.probe, hostdSocket, "host-script", scratch)
		var r struct {
			ServicePID      int    `json:"servicePid"`
			ListenerOwner   int    `json:"listenerOwner"`
			PortForwardable bool   `json:"portForwardable"`
			Stdout          string `json:"stdout"`
			Stderr          string `json:"stderr"`
			Exit            int    `json:"exit"`
			ExitAfter       int    `json:"exitAfterRelease"`
		}
		if out.exitCode != 0 || json.Unmarshal([]byte(out.stdout), &r) != nil {
			t.Fatalf("the probe: %s", out)
		}
		if want := f.uid + "\n$(id -u) literal\n"; r.Exit != 0 || r.Stdout != want {
			t.Errorf("the script answered exit %d, stdout %q, stderr %q; want %q: run as %s, its argument literal", r.Exit, r.Stdout, r.Stderr, want, d.user)
		}
		if r.ServicePID <= 0 || !r.PortForwardable {
			t.Errorf("hostd reported pid %d and the grant's port is owned by %d: the relay would refuse to forward it", r.ServicePID, r.ListenerOwner)
		}
		if r.ExitAfter != 125 {
			t.Errorf("after release the command exited %d, want 125", r.ExitAfter)
		}
	})

	// 6. Cancelling a host run kills its whole process group on the host.
	t.Run("cancelling a host-run agy kills its process group", func(t *testing.T) {
		report := filepath.Join(f.reports, "agy-cancel.report")
		task := f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy cancelled", "mode=hang report="+report)
		pids := f.waitForPids(t, report)
		if err := f.client.Close(ctx, task, ui.CloseOptions{}); err != nil {
			t.Fatalf("cancelling the task: %v", err)
		}
		waitFor(t, "the cancelled agy's process group to be gone", 60*time.Second, func() error { return gone(pids) })
	})

	// 6b. A daemon upgrade leaves a host-run agy running in hostd, and the
	// daemon that comes up adopts it: the same process carries on, the
	// transcript has every line once, and the run ends in its first
	// attempt rather than being dispatched again. Before 7, whose
	// max_duration would cut the run short.
	t.Run("a daemon upgrade leaves a host-run agy running and the new daemon adopts it", func(t *testing.T) {
		const ticks = 90 // 45 s: longer than the daemon's restart
		report := filepath.Join(f.reports, "agy-upgrade.report")
		task := f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy across a daemon upgrade",
			fmt.Sprintf("mode=steady ticks=%d report=%s", ticks, report))
		var pid string
		waitFor(t, "the steady agy to start on the host", 3*time.Minute, func() error {
			if pid = strings.TrimSpace(sudoRead(report + ".pid")); pid == "" {
				return errors.New("no pid yet")
			}
			return nil
		})
		time.Sleep(3 * time.Second) // some ticks read by the old daemon
		if r := sudo("systemctl", "restart", "grain-daemon.service"); r.exitCode != 0 {
			t.Fatalf("restarting the daemon: %s", r)
		}
		if err := gone([]string{pid}); err == nil {
			t.Fatalf("the host-run agy (pid %s) did not outlive the daemon's restart", pid)
		}
		f.waitForAPI(t, ctx, d)
		var detail ui.TaskDetail
		waitFor(t, "the adopted run to finish", 3*time.Minute, func() error {
			var err error
			if detail, err = f.client.GetTask(ctx, task); err != nil {
				return err
			}
			if len(detail.Attempts) == 0 || detail.Attempts[0].FinishedAt == nil {
				return errors.New("the attempt is still live")
			}
			return nil
		})
		defer f.client.Close(ctx, task, ui.CloseOptions{})
		attempt := detail.Attempts[0]
		if len(detail.Attempts) != 1 || attempt.Outcome == model.InterruptedOutcome {
			t.Fatalf("the run was not carried through the upgrade: %d attempt(s), the first %s: %s",
				len(detail.Attempts), attempt.Outcome, attempt.Detail)
		}
		if got := strings.TrimSpace(sudoRead(report + ".pid")); got != pid {
			t.Fatalf("the fake agy was started again (pid %s, then %s): a re-dispatch, not an adoption", pid, got)
		}
		transcript, err := f.client.AttemptTranscript(ctx, task, attempt.Number)
		if err != nil {
			t.Fatalf("reading the adopted attempt's transcript: %v", err)
		}
		for i := range ticks {
			if n := strings.Count(transcript, fmt.Sprintf("steady tick %03d", i)); n != 1 {
				t.Errorf("the transcript has tick %03d %d times, want once:\n%s", i, n, tail(transcript, 40))
				break
			}
		}
		if !strings.Contains(transcript, "fake agy finished on the host") {
			t.Errorf("the transcript does not reach the run's end:\n%s", tail(transcript, 40))
		}
	})

	// 6b'. A detached claude -- the image's claude through hostd's
	// container-equivalent entry (docs/non-disruptive-upgrade.md, "Which
	// substrate carries which CLI") -- outlives a daemon upgrade the same
	// way: the same process, one attempt, every line of its stream once.
	// It runs on the host with the container's HOME. The extracted claude
	// is replaced by a fake for it, which the next start's extractor
	// leaves alone (same image, same version directory).
	t.Run("a daemon upgrade leaves a detached claude running and the new daemon adopts it", func(t *testing.T) {
		const ticks = 90 // 45 s: longer than the daemon's restart
		if allow := sudoRead(hostdAllowlist); !strings.Contains(allow, "  claude-detached:\n    path: "+hostdVersions+"/current/claude\n") ||
			!strings.Contains(allow, "      HOME: "+d.data+"/home\n") {
			t.Fatalf("the allowlist offers no container-equivalent claude:\n%s", allow)
		}
		report := filepath.Join(f.reports, "claude-detached.report")
		script := fmt.Sprintf(`#!/bin/sh
# A fake detached claude: says where it ran and its pid, then streams
# %[1]d ticks of claude's stream-json, half a second apart.
case "$1" in --version) echo "9.9.9 (Claude Code)"; exit 0 ;; esac
cat > /dev/null
c=false; [ -e /.dockerenv ] && c=true
echo "uid=$(id -u) in_container=$c home=$HOME" > %[2]q
echo $$ > %[2]q.pid
echo '{"type":"system","subtype":"init","session_id":"fake-detached"}'
i=0
while [ $i -lt %[1]d ]; do
  printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"steady tick %%03d"}]},"session_id":"fake-detached"}\n' $i
  i=$((i+1))
  sleep 0.5
done
echo '{"type":"result","subtype":"success","is_error":false,"result":"fake claude finished detached","session_id":"fake-detached"}'
`, ticks, report)
		tmp := filepath.Join(d.root, "fake-detached-claude")
		if err := os.WriteFile(tmp, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if r := sudo("install", "-m0755", tmp, hostdVersions+"/current/claude"); r.exitCode != 0 {
			t.Fatalf("installing the fake detached claude: %s", r)
		}
		f.setCLIRun(t, ctx, d, `{"claude":{"location":"detached"}}`)
		defer f.setCLIRun(t, ctx, d, `{}`)
		if got, body := f.cliRun(t, ctx, d); func() bool {
			for _, r := range got.Resolved {
				if r.Framework == model.AgentFrameworkClaude {
					return r.Location != model.CLIRunDetached || r.Via != "hostd"
				}
			}
			return true
		}() {
			t.Fatalf("claude set to detached does not resolve to hostd's detached entry: %s", body)
		}

		task := f.start(t, ctx, model.AgentFrameworkClaude, "fake claude detached across a daemon upgrade", "")
		var pid string
		waitFor(t, "the detached claude to start on the host", 3*time.Minute, func() error {
			if pid = strings.TrimSpace(sudoRead(report + ".pid")); pid == "" {
				return errors.New("no pid yet")
			}
			return nil
		})
		if got := sudoRead(report); !strings.Contains(got, "in_container=false") || !strings.Contains(got, "home="+d.data+"/home") ||
			!strings.Contains(got, "uid="+f.uid+" ") {
			t.Errorf("the detached claude did not run on the host as %s with the container's HOME: %q", d.user, got)
		}
		time.Sleep(3 * time.Second) // some ticks read by the old daemon
		if r := sudo("systemctl", "restart", "grain-daemon.service"); r.exitCode != 0 {
			t.Fatalf("restarting the daemon: %s", r)
		}
		if err := gone([]string{pid}); err == nil {
			t.Fatalf("the detached claude (pid %s) did not outlive the daemon's restart", pid)
		}
		f.waitForAPI(t, ctx, d)
		var detail ui.TaskDetail
		waitFor(t, "the adopted claude run to finish", 3*time.Minute, func() error {
			var err error
			if detail, err = f.client.GetTask(ctx, task); err != nil {
				return err
			}
			if len(detail.Attempts) == 0 || detail.Attempts[0].FinishedAt == nil {
				return errors.New("the attempt is still live")
			}
			return nil
		})
		defer f.client.Close(ctx, task, ui.CloseOptions{})
		attempt := detail.Attempts[0]
		if len(detail.Attempts) != 1 || attempt.Outcome == model.InterruptedOutcome {
			t.Fatalf("the detached claude was not carried through the upgrade: %d attempt(s), the first %s: %s",
				len(detail.Attempts), attempt.Outcome, attempt.Detail)
		}
		if got := strings.TrimSpace(sudoRead(report + ".pid")); got != pid {
			t.Fatalf("the fake claude was started again (pid %s, then %s): a re-dispatch, not an adoption", pid, got)
		}
		transcript, err := f.client.AttemptTranscript(ctx, task, attempt.Number)
		if err != nil {
			t.Fatalf("reading the adopted attempt's transcript: %v", err)
		}
		for i := range ticks {
			if n := strings.Count(transcript, fmt.Sprintf("steady tick %03d", i)); n != 1 {
				t.Errorf("the transcript has tick %03d %d times, want once:\n%s", i, n, tail(transcript, 40))
				break
			}
		}
	})

	// 6c. A hostd upgrade: the extractor grain-daemon.service runs before
	// every start takes a new image's grain into a version of its own and
	// moves the current link, and grain-hostd-upgrade.path cuts hostd over
	// onto it. A hostd on the same protocol keeps taking starts: the run it
	// holds carries on to its end, a host run filed meanwhile starts at
	// once rather than waiting, and hostd exits for the new version the
	// first moment nothing runs.
	next := f.hostdImage(t, d, "next")
	original := f.hostdVersion(t)
	t.Run("a hostd upgrade cuts over: the running child carries on, a new host run starts, and hostd moves to the new build once idle", func(t *testing.T) {
		const ticks = 120 // 60 s: long enough to file a run during the cutover
		report := filepath.Join(f.reports, "agy-hostd-upgrade.report")
		task := f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy across a hostd upgrade",
			fmt.Sprintf("mode=steady ticks=%d report=%s", ticks, report))
		defer f.client.Close(ctx, task, ui.CloseOptions{})
		var pid string
		waitFor(t, "the steady agy to start on the host", 3*time.Minute, func() error {
			if pid = strings.TrimSpace(sudoRead(report + ".pid")); pid == "" {
				return errors.New("no pid yet")
			}
			return nil
		})
		f.extractHostd(t, next)
		upgraded := f.hostdVersion(t)
		if upgraded == original {
			t.Fatalf("the current link still names %s after extracting %s", original, next)
		}
		waitFor(t, "grain-hostd-upgrade.path to cut hostd over", time.Minute, func() error {
			if st := f.hostdStatus(t, d); !strings.Contains(st, "cutting over to a new build") {
				return fmt.Errorf("hostd says %q", st)
			}
			return nil
		})
		if got, body := f.cliRun(t, ctx, d); got.HostExecutor.Hostd.Updating {
			t.Errorf("Settings mid-cutover says hostd is updating, which holds host runs back: %s", body)
		}

		// Filed now, it starts: a cutover refuses nothing.
		startedReport := filepath.Join(f.reports, "agy-during-hostd-cutover.report")
		during := f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy filed during a hostd cutover", "report="+startedReport)
		defer f.client.Close(ctx, during, ui.CloseOptions{})
		if err := gone([]string{pid}); err == nil {
			t.Fatalf("the host-run agy (pid %s) did not outlive the start of hostd's cutover", pid)
		}
		waitFor(t, "the run filed during the cutover to start", 3*time.Minute, func() error {
			if !sudoTest("-e", startedReport) {
				return errors.New("it has not run yet")
			}
			return nil
		})

		detail := f.waitForFinished(t, ctx, task)
		if a := detail.Attempts[0]; len(detail.Attempts) != 1 || a.Outcome == model.InterruptedOutcome {
			t.Fatalf("the run was not carried through hostd's cutover: %d attempt(s), the first %s: %s",
				len(detail.Attempts), a.Outcome, a.Detail)
		}
		if got := strings.TrimSpace(sudoRead(report + ".pid")); got != pid {
			t.Fatalf("the fake agy was started again (pid %s, then %s)", pid, got)
		}
		f.waitForHostdOn(t, d, upgraded)
	})

	// 6d. A rollback to the image before: the extractor points current
	// back at its version, which it kept, and hostd drains back onto it.
	t.Run("a rollback points hostd back at the version before", func(t *testing.T) {
		upgraded := f.hostdVersion(t)
		f.extractHostd(t, d.image)
		if got := f.hostdVersion(t); got != original {
			t.Fatalf("after a rollback current names %q, want %s", got, original)
		}
		if !sudoTest("-x", hostdVersions+"/"+upgraded+"/grain") {
			t.Errorf("the rollback pruned the version it replaced (%s), which a rollback back needs", upgraded)
		}
		f.waitForHostdOn(t, d, original)
	})

	// 6e. A child that never ends holds a cutover only until the
	// allowlist's drain_cap, and the drain that follows it for drain_cap
	// again: then hostd cancels it, says so, and restarts on the new
	// version.
	t.Run("a stuck host run is cancelled at the drain cap", func(t *testing.T) {
		f.editAllowlist(t, "\nclis:\n", "\ndrain_cap: 20s\nclis:\n")
		report := filepath.Join(f.reports, "agy-drain-cap.report")
		task := f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy that never ends, across a hostd upgrade", "mode=hang report="+report)
		defer f.client.Close(ctx, task, ui.CloseOptions{})
		pids := f.waitForPids(t, report)
		f.extractHostd(t, next)
		upgraded := f.hostdVersion(t)
		time.Sleep(5 * time.Second)
		if err := gone(pids); err == nil {
			t.Fatalf("the stuck agy was killed at the start of the drain, not at its cap")
		}
		waitFor(t, "hostd to cancel the stuck agy at the drain cap", 90*time.Second, func() error { return gone(pids) })
		if log := sudo("journalctl", "-u", "grain-hostd.service", "-n", "300", "--no-pager").stdout; !strings.Contains(log, "drain cap 20s reached") {
			t.Errorf("hostd's journal does not say it cancelled at the drain cap:\n%s", tail(log, 30))
		}
		f.waitForHostdOn(t, d, upgraded)
	})

	// 7. hostd enforces the entry's max_duration itself, whatever the
	// daemon asks for -- an operator's edit to the allowlist.
	t.Run("hostd kills a host run at its entry's deadline", func(t *testing.T) {
		f.editAllowlist(t, "    path: "+f.agy+"\n", "    path: "+f.agy+"\n    max_duration: 20s\n")
		report := filepath.Join(f.reports, "agy-deadline.report")
		f.start(t, ctx, model.AgentFrameworkAntigravity, "fake agy past its deadline", "mode=hang report="+report)
		pids := f.waitForPids(t, report)
		waitFor(t, "hostd to kill the agy past its deadline", 90*time.Second, func() error { return gone(pids) })
	})

	// 8. A re-run keeps the operator's allowlist, edits and all.
	if r := d.setupAgain(t, f.googleTrack()...); r.exitCode != 0 {
		t.Fatalf("re-running the container-mode install: %s", r)
	}
	allow := sudoRead(hostdAllowlist)
	for _, edit := range []string{"max_duration: 20s", "fakecap:"} {
		if !strings.Contains(allow, edit) {
			t.Errorf("a re-run lost the operator's edit %q:\n%s", edit, allow)
		}
	}
	f.assertContainerModeWithHostd(t, d, "after a re-run")

	// 8b. A rollback with an older release's own setup.sh puts that
	// release's grain, which has no hostd, at /usr/local/bin/grain and
	// leaves grain-hostd.service behind. Its next start is skipped by the
	// unit's ExecCondition rather than crash-looping on `unknown command`.
	f.withPreHostdGrain(t, d, func() {
		sudo("systemctl", "restart", "grain-hostd.service")
		waitFor(t, "grain-hostd.service to skip a grain without hostd", 30*time.Second, func() error {
			show := run("systemctl", "show", "-p", "ActiveState", "-p", "ConditionResult", "-p", "NRestarts", "grain-hostd.service").stdout
			for _, want := range []string{"ActiveState=inactive", "ConditionResult=no", "NRestarts=0"} {
				if !strings.Contains(show, want) {
					return fmt.Errorf("want %s:\n%s", want, show)
				}
			}
			return nil
		})
	})
	sudo("systemctl", "restart", "grain-hostd.service")
	waitFor(t, "grain-hostd.service to start again on a grain with hostd", 30*time.Second, func() error {
		if run("systemctl", "is-active", "--quiet", "grain-hostd.service").exitCode != 0 {
			return fmt.Errorf("not active")
		}
		return nil
	})

	// 9. A re-run with GRAIN_HOSTD=0 switches hostd off: its unit and the
	// socket mount go, the allowlist stays, and "host" is no longer offered.
	off := d.setupAgain(t, append(f.googleTrack(), "GRAIN_HOSTD=0")...)
	if off.exitCode != 0 {
		t.Fatalf("re-running with GRAIN_HOSTD=0: %s", off)
	}
	if !strings.Contains(off.stdout, "hostd:             not installed (GRAIN_HOSTD=0)") {
		t.Errorf("the readiness report does not say hostd was left out:\n%s", tail(off.stdout, 60))
	}
	if !sudoTest("-f", hostdAllowlist) {
		t.Errorf("GRAIN_HOSTD=0 removed %s, which may hold the operator's edits", hostdAllowlist)
	}
	// The Google track's GRAIN_AGY_RUN=host has nothing to run through
	// any more, and the daemon refuses a -agy-run=host it has no host
	// executor for: it is dropped, said so, and never written (task 5127).
	if !strings.Contains(off.stdout, "ignoring GRAIN_AGY_RUN=host") {
		t.Errorf("the re-run does not say it dropped GRAIN_AGY_RUN=host:\n%s", tail(off.stdout, 60))
	}
	if unit := sudoRead(daemonUnit); strings.Contains(unit, "-agy-run host") {
		t.Errorf("a GRAIN_HOSTD=0 unit still passes -agy-run host, which the daemon refuses at start:\n%s", unit)
	}
	if !strings.Contains(off.stdout, "daemon:            active\n") || strings.Contains(off.stdout, "\nunknown\n") {
		t.Errorf("the readiness report does not say the daemon is active, on one line:\n%s", tail(off.stdout, 60))
	}
	f.waitForAPI(t, ctx, d)
	f.assertNoHostd(t, ctx, d, "a re-run with GRAIN_HOSTD=0")
	f.mustStillHave(t, ctx, keep.ID, "switching hostd off")

	// 10. A daemon that refuses its settings at start exits a moment
	// after docker has started it, over and over. setup.sh must fail on
	// that rather than report a deploy: here the refusal is an unknown
	// store driver, which the daemon reads from its environment.
	const defaults = "/etc/default/grain-daemon"
	hadDefaults := sudoTest("-e", defaults)
	saved := ""
	if hadDefaults {
		saved = sudoRead(defaults)
	}
	restore := func() {
		if hadDefaults {
			sudo("sh", "-c", "printf '%s' \"$1\" > "+defaults, "sh", saved)
		} else {
			sudo("rm", "-f", defaults)
		}
	}
	defer restore()
	if r := sudo("sh", "-c", "echo 'GRAIN_EXTRA_DOCKER_RUN_ARGS=-e GRAIN_DB_DRIVER=no-such-driver' > "+defaults); r.exitCode != 0 {
		t.Fatalf("writing %s: %s", defaults, r)
	}
	looping := d.setupAgain(t, append(f.googleTrack(), "GRAIN_HOSTD=0")...)
	loopingOut := looping.stdout + looping.stderr
	if looping.exitCode == 0 {
		t.Errorf("setup.sh reported success while the daemon was exiting at start:\n%s", tail(loopingOut, 4000))
	}
	// Which of setup.sh's two checks catches it depends on where in the
	// restart cycle it looks; either way it has to say why.
	if !(strings.Contains(loopingOut, "grain-daemon.service failed to start") ||
		strings.Contains(loopingOut, "grain-daemon.service started and then exited")) ||
		!strings.Contains(loopingOut, "no-such-driver") {
		t.Errorf("the failure does not say the daemon exited, with its own reason:\n%s", tail(loopingOut, 4000))
	}
	restore()
	if r := d.setupAgain(t, append(f.googleTrack(), "GRAIN_HOSTD=0")...); r.exitCode != 0 {
		t.Fatalf("re-running once the daemon's settings are fixed: %s", r)
	}
	f.waitForAPI(t, ctx, d)
	f.mustStillHave(t, ctx, keep.ID, "a failed start and its repair")
}

func newHostdFixture(t *testing.T, d *deployment) *hostdFixture {
	t.Helper()
	hostOnlyDir := filepath.Join(d.root, "host-only")
	if err := os.MkdirAll(hostOnlyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &hostdFixture{
		hostOnly:  filepath.Join(hostOnlyDir, "marker"),
		agy:       filepath.Join(hostOnlyDir, "agy"),
		probe:     filepath.Join(hostOnlyDir, "hostscriptprobe"),
		claude:    filepath.Join(d.data, "fake-claude", "claude"),
		reports:   filepath.Join(d.sandbox, "hostd-e2e"),
		capReport: filepath.Join(d.sandbox, "hostd-e2e", "capability.report"),
		uid:       strings.TrimSpace(run("id", "-u", d.user).stdout),
	}
	if err := os.WriteFile(f.hostOnly, []byte("on the host only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkout, err := repoRootFromCaller()
	if err != nil {
		t.Fatal(err)
	}
	for bin, pkg := range map[string]string{f.agy: "./tests/installer/fakeagy", f.probe: "./tests/installer/hostscriptprobe"} {
		build := exec.Command("go", "build", "-o", bin, pkg)
		build.Dir = checkout
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v: %s", pkg, err, out)
		}
	}
	// Owned by the service account, so the fakes -- which run as it --
	// can write their reports, and the CLI wrappers' directory checks
	// see nothing of the test's own.
	for _, dir := range []string{f.reports, filepath.Dir(f.claude)} {
		if r := sudo("install", "-d", "-m0755", "-o", d.user, dir); r.exitCode != 0 {
			t.Fatalf("making %s: %s", dir, r)
		}
	}
	claudeReport := filepath.Join(f.reports, "claude.report")
	script := fmt.Sprintf(`#!/bin/sh
# A fake claude for tests/installer: says where it ran, then answers in
# claude's stream-json.
case "$1" in --version) echo "9.9.9 (Claude Code)"; exit 0 ;; esac
cat > /dev/null
c=false; [ -e /.dockerenv ] && c=true
h=false; [ -e %q ] && h=true
echo "uid=$(id -u) in_container=$c host_only_seen=$h" > %q
echo '{"type":"system","subtype":"init","session_id":"fake-claude"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"fake claude finished in the container","session_id":"fake-claude"}'
`, f.hostOnly, claudeReport)
	tmp := filepath.Join(d.root, "fake-claude")
	if err := os.WriteFile(tmp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := sudo("install", "-m0755", "-o", d.user, tmp, f.claude); r.exitCode != 0 {
		t.Fatalf("installing the fake claude: %s", r)
	}
	return f
}

// googleTrack is what a Cloudtop's installer now hands setup.sh: the
// Google track in container mode, agy on the host, and hostd at its
// default -- which overrides the package install's GRAIN_HOSTD=0; a later
// GRAIN_HOSTD=0 overrides it in turn, since env takes the last.
func (f *hostdFixture) googleTrack() []string {
	return []string{
		"GRAIN_HOSTD=",
		"GRAIN_INSTALL_MODE=container",
		"GRAIN_INSTALL_TRACK=google",
		"GRAIN_AGY_PATH=" + f.agy,
		"GRAIN_AGY_RUN=host",
		"GRAIN_CLAUDE_RUN=container",
		"GRAIN_CLAUDE_PATH=" + f.claude,
	}
}

func (f *hostdFixture) assertContainerModeWithHostd(t *testing.T, d *deployment, when string) {
	t.Helper()
	unit := sudoRead(daemonUnit)
	if strings.Contains(unit, "GRAIN_INSTALL_MODE=host") || !strings.Contains(unit, "docker run --name grain-daemon") {
		t.Fatalf("%s: grain-daemon.service is not a container-mode unit:\n%s", when, unit)
	}
	for _, want := range []string{"-hostd-socket " + hostdSocket, "--volume /run/grain-hostd:/run/grain-hostd",
		"--volume " + d.data + ":" + d.data, "Wants=network-online.target grain-hostd.service"} {
		if !strings.Contains(unit, want) {
			t.Errorf("%s: grain-daemon.service does not carry %q:\n%s", when, want, unit)
		}
	}
	if strings.Contains(unit, "--volume /etc/grain") {
		t.Errorf("%s: grain-daemon.service mounts /etc/grain, where hostd's allowlist is:\n%s", when, unit)
	}
	if strings.Contains(unit, "-agy-path") {
		t.Errorf("%s: the daemon is handed -agy-path, but agy runs on the host through hostd:\n%s", when, unit)
	}
	if user := strings.TrimSpace(run("systemctl", "show", "-p", "User", "--value", "grain-hostd.service").stdout); user != d.user {
		t.Errorf("%s: grain-hostd.service runs as %q, want the installing user %q", when, user, d.user)
	}
	if run("systemctl", "is-active", "--quiet", "grain-hostd.service").exitCode != 0 {
		t.Errorf("%s: grain-hostd.service is not running", when)
	}
	if got := strings.TrimSpace(sudo("stat", "-c", "%U:%G %a", hostdAllowlist).stdout); got != "root:root 644" {
		t.Errorf("%s: %s is %q, want root:root 644 -- the only kind hostd loads", when, hostdAllowlist, got)
	}
	if got := strings.TrimSpace(sudo("stat", "-c", "%U %a", hostdSocket).stdout); got != d.user+" 600" {
		t.Errorf("%s: %s is %q, want %s's own, 0600", when, hostdSocket, got, d.user)
	}
	// The host's grain is the image's own binary, not the CLI wrapper:
	// a host-run agy forks its MCP server from it.
	if head := sudo("head", "-c", "4", "/usr/local/bin/grain").stdout; head != "\x7fELF" {
		t.Errorf("%s: /usr/local/bin/grain is not a binary (it starts %q)", when, head)
	}
	// The allowlist cannot be written from the daemon's container: it is
	// not there to write, and its directory is root's.
	if r := sudo("docker", "exec", "grain-daemon", "test", "-e", hostdAllowlist); r.exitCode == 0 {
		t.Errorf("%s: %s is visible inside the daemon's container", when, hostdAllowlist)
	}
	if r := sudo("docker", "exec", "grain-daemon", "sh", "-c", "mkdir -p /etc/grain && echo 'clis: {}' > "+hostdAllowlist); r.exitCode == 0 {
		// Written in the container's own filesystem, if at all: the host's
		// file must be untouched.
		if strings.Contains(sudoRead(hostdAllowlist), "clis: {}") {
			t.Errorf("%s: the daemon's container rewrote the host's %s", when, hostdAllowlist)
		}
	}
	if check := sudo("runuser", "-u", d.user, "--", "/usr/local/bin/grain", "hostd", "-check", "-allowlist", hostdAllowlist); check.exitCode != 0 ||
		!strings.Contains(check.stdout, "cli\tagy\t"+f.agy) {
		t.Errorf("%s: hostd -check does not offer agy at %s: %s", when, f.agy, check)
	}
}

func (f *hostdFixture) waitForAPI(t *testing.T, ctx context.Context, d *deployment) {
	t.Helper()
	waitFor(t, "the reinstalled daemon to serve its API", 120*time.Second, func() error {
		token := strings.TrimSpace(sudoRead(filepath.Join(d.data, "local-api-token")))
		if token == "" {
			return errors.New("no access code yet")
		}
		f.accessToken = token
		f.client = ui.NewHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", d.port))
		f.client.Token = token
		_, err := f.client.ListTasks(ctx)
		return err
	})
}

func (f *hostdFixture) mustStillHave(t *testing.T, ctx context.Context, id, after string) {
	t.Helper()
	if f.client == nil {
		return
	}
	if _, err := f.client.Task(ctx, id); err != nil {
		t.Fatalf("after %s the deployment no longer has task %s: %v", after, id, err)
	}
}

// becomeAHostModeInstall replaces the package's container-mode unit with
// the host-mode one an older setup.sh wrote, and the files host mode
// alone left on the machine. It is not started: what is under test is
// the re-run that migrates it.
func (f *hostdFixture) becomeAHostModeInstall(t *testing.T, d *deployment) {
	t.Helper()
	sudo("systemctl", "stop", "grain-daemon.service")
	sudo("docker", "rm", "--force", "grain-daemon")
	unit := fmt.Sprintf(`[Unit]
Description=grain daemon (host mode, as an older setup.sh wrote it)

[Service]
User=%s
Environment=HOME=%s/home
Environment=GRAIN_INSTALL_MODE=host
ExecStart=/usr/local/bin/grain daemon -data-dir %s -ui-addr 127.0.0.1:%d

[Install]
WantedBy=multi-user.target
`, d.user, d.data, d.data, d.port)
	for path, content := range map[string]string{
		daemonUnit: unit,
		"/etc/systemd/system/grain-daemon.service.d/grain.conf": "[Service]\nEnvironment=GRAIN_INSTALL_MODE=host\n",
		"/etc/sudoers.d/zzz-grain-nft":                          "# host mode's nft rule, as far as this test needs it\n",
		"/usr/local/lib/grain/.extracted-image-id":              "sha256:host-mode\n",
		"/usr/local/share/grain/src/go.mod":                     "module github.com/bwsalmon/grain\n",
	} {
		tmp := filepath.Join(d.root, "host-mode-file")
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mode := "0644"
		if strings.HasPrefix(path, "/etc/sudoers.d/") {
			mode = "0440"
		}
		if r := sudo("install", "-D", "-m"+mode, tmp, path); r.exitCode != 0 {
			t.Fatalf("writing host mode's %s: %s", path, r)
		}
	}
	sudo("systemctl", "daemon-reload")
}

// cliRun is GET /api/settings/cli-run, decoded as far as these tests read it.
type cliRun struct {
	Resolved []struct {
		Framework       string `json:"framework"`
		Location        string `json:"location"`
		Via             string `json:"via"`
		HostUnavailable string `json:"hostUnavailable"`
	} `json:"resolved"`
	HostExecutor struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
		Hostd  struct {
			Updating bool `json:"updating"`
		} `json:"hostd"`
	} `json:"hostExecutor"`
}

func (f *hostdFixture) cliRun(t *testing.T, ctx context.Context, d *deployment) (cliRun, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/settings/cli-run", d.port), nil)
	req.Header.Set("Authorization", "Bearer "+f.accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/settings/cli-run: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got cliRun
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding /api/settings/cli-run: %v: %s", err, body)
	}
	return got, string(body)
}

// setCLIRun stores Settings -> Models' run locations (PUT
// /api/settings/cli-run), settings being its JSON map.
func (f *hostdFixture) setCLIRun(t *testing.T, ctx context.Context, d *deployment, settings string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/api/settings/cli-run", d.port),
		strings.NewReader(`{"settings":`+settings+`}`))
	req.Header.Set("Authorization", "Bearer "+f.accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings/cli-run: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT /api/settings/cli-run %s: %s: %s", settings, resp.Status, body)
	}
}

// assertNoHostd checks a deployment installed without grain hostd: no
// hostd unit, nothing of it in the daemon's unit, and no host executor,
// so "host" is not offered and every CLI resolves to the container.
func (f *hostdFixture) assertNoHostd(t *testing.T, ctx context.Context, d *deployment, when string) {
	t.Helper()
	if sudoTest("-e", hostdUnit) || run("systemctl", "is-active", "--quiet", "grain-hostd.service").exitCode == 0 {
		t.Errorf("%s: grain-hostd.service is installed or running", when)
	}
	unit := sudoRead(daemonUnit)
	for _, absent := range []string{"-hostd-socket", "/run/grain-hostd"} {
		if strings.Contains(unit, absent) {
			t.Errorf("%s: grain-daemon.service still carries %q:\n%s", when, absent, unit)
		}
	}
	got, body := f.cliRun(t, ctx, d)
	if got.HostExecutor.Kind != "none" || !strings.Contains(got.HostExecutor.Reason, "no grain hostd") {
		t.Errorf("%s: the host executor is %q (%q), want none, saying there is no grain hostd: %s",
			when, got.HostExecutor.Kind, got.HostExecutor.Reason, body)
	}
	for _, r := range got.Resolved {
		if r.Location != "container" {
			t.Errorf("%s: %s resolves to %s, want container", when, r.Framework, r.Location)
		}
	}
}

// hostdVersion is the version the installer's current link names.
func (f *hostdFixture) hostdVersion(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(sudo("readlink", hostdVersions+"/current").stdout)
}

// hostdImage makes an image whose grain is this deployment's, under an
// image ID of its own -- what a new release looks like to the extractor.
func (f *hostdFixture) hostdImage(t *testing.T, d *deployment, tag string) string {
	t.Helper()
	ref := "grain-hostd-e2e:" + tag
	cid := strings.TrimSpace(sudo("docker", "create", d.image).stdout)
	if cid == "" {
		t.Fatalf("opening %s", d.image)
	}
	defer sudo("docker", "rm", "-f", cid)
	if r := sudo("docker", "commit", "--change", "LABEL grain.e2e.hostd="+tag, cid, ref); r.exitCode != 0 {
		t.Fatalf("making %s: %s", ref, r)
	}
	t.Cleanup(func() { sudo("docker", "rmi", "-f", ref) })
	return ref
}

// extractHostd runs the extractor grain-daemon.service runs before every
// start, on image, as an upgrade's restart or a rollback runs it.
func (f *hostdFixture) extractHostd(t *testing.T, image string) {
	t.Helper()
	if r := sudo(hostdExtractor, image); r.exitCode != 0 {
		t.Fatalf("extracting hostd from %s: %s", image, r)
	}
}

// hostdStatus is `grain hostd -status`, asked as the service account:
// hostd answers its own account alone.
func (f *hostdFixture) hostdStatus(t *testing.T, d *deployment) string {
	t.Helper()
	r := sudo("runuser", "-u", d.user, "--", hostdVersions+"/current/grain", "hostd", "-status", "-socket", hostdSocket)
	if r.exitCode != 0 {
		return "not answering: " + r.String()
	}
	return r.stdout
}

// waitForHostdOn waits for grain-hostd.service to be serving, not
// draining, from version: its main process's binary is that version's.
func (f *hostdFixture) waitForHostdOn(t *testing.T, d *deployment, version string) {
	t.Helper()
	want := hostdVersions + "/" + version + "/grain"
	waitFor(t, "hostd to serve from version "+version, 2*time.Minute, func() error {
		pid := strings.TrimSpace(run("systemctl", "show", "-p", "MainPID", "--value", "grain-hostd.service").stdout)
		if pid == "" || pid == "0" {
			return errors.New("grain-hostd.service has no main process")
		}
		if exe := strings.TrimSpace(sudo("readlink", "/proc/"+pid+"/exe").stdout); exe != want {
			return fmt.Errorf("hostd (pid %s) runs %s, want %s", pid, exe, want)
		}
		if st := f.hostdStatus(t, d); !strings.HasPrefix(st, "answering on ") || strings.Contains(st, "draining") {
			return fmt.Errorf("hostd says %q", st)
		}
		return nil
	})
}

// waitForFinished waits for task's first attempt to end.
func (f *hostdFixture) waitForFinished(t *testing.T, ctx context.Context, task string) ui.TaskDetail {
	t.Helper()
	var detail ui.TaskDetail
	waitFor(t, "the run to finish", 3*time.Minute, func() error {
		var err error
		if detail, err = f.client.GetTask(ctx, task); err != nil {
			return err
		}
		if len(detail.Attempts) == 0 || detail.Attempts[0].FinishedAt == nil {
			return errors.New("the attempt is still live")
		}
		return nil
	})
	return detail
}

// withPreHostdGrain runs fn with /usr/local/bin/grain replaced the way an
// older release's setup.sh replaces it: by a grain that answers `hostd`
// the way v2.1.1's does, with its usage, `unknown command "hostd"` and
// exit 1. The real binary is put back afterwards.
func (f *hostdFixture) withPreHostdGrain(t *testing.T, d *deployment, fn func()) {
	t.Helper()
	const bin, saved = "/usr/local/bin/grain", "/usr/local/bin/grain.hostd-test"
	if r := sudo("cp", "-p", bin, saved); r.exitCode != 0 {
		t.Fatalf("keeping the real %s: %s", bin, r)
	}
	defer func() {
		if r := sudo("mv", "-f", saved, bin); r.exitCode != 0 {
			t.Fatalf("putting the real %s back: %s", bin, r)
		}
	}()
	old := "#!/bin/sh\necho 'usage: grain [global flags] <command> [args]' >&2\necho \"grain: unknown command \\\"$1\\\"\" >&2\nexit 1\n"
	tmp := filepath.Join(d.root, "pre-hostd-grain")
	if err := os.WriteFile(tmp, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := sudo("install", "-m0755", tmp, bin); r.exitCode != 0 {
		t.Fatalf("installing a grain without hostd: %s", r)
	}
	fn()
}

func (f *hostdFixture) assertHostdOffersAgy(t *testing.T, ctx context.Context, d *deployment) {
	t.Helper()
	got, body := f.cliRun(t, ctx, d)
	if got.HostExecutor.Kind != "hostd" {
		t.Errorf("the daemon's host executor is %q, want hostd: %s", got.HostExecutor.Kind, body)
	}
	for _, r := range got.Resolved {
		switch r.Framework {
		case model.AgentFrameworkAntigravity:
			if r.Location != "host" || r.Via != "hostd" {
				t.Errorf("agy resolves to %s via %s, want host via hostd", r.Location, r.Via)
			}
		case model.AgentFrameworkClaude:
			if r.Location != "container" {
				t.Errorf("claude resolves to %s, want container", r.Location)
			}
		}
	}
}

// editAllowlist rewrites the host's allowlist as an operator would: as
// root, in place, keeping its owner and mode.
func (f *hostdFixture) editAllowlist(t *testing.T, old, new string) {
	t.Helper()
	allow := sudoRead(hostdAllowlist)
	if !strings.Contains(allow, old) {
		t.Fatalf("%s has no %q to edit:\n%s", hostdAllowlist, old, allow)
	}
	cmd := exec.Command("sudo", "-n", "tee", hostdAllowlist)
	cmd.Stdin = strings.NewReader(strings.Replace(allow, old, new, 1))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("editing %s: %v: %s", hostdAllowlist, err, out)
	}
}

type finishedAttempt struct {
	ui.Attempt
	taskID string
}

// start files and approves one task for framework, with fields in its
// description for the fake to read, and returns its id.
func (f *hostdFixture) start(t *testing.T, ctx context.Context, framework, title, fields string) string {
	t.Helper()
	task, err := f.client.CreateTask(ctx, ui.CreateTaskRequest{Title: title, Repo: "acme/widgets", AgentFramework: framework})
	if err != nil {
		t.Fatalf("filing %q: %v", title, err)
	}
	if fields != "" {
		if _, err := f.client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Description: &fields}); err != nil {
			t.Fatalf("describing %q: %v", title, err)
		}
	}
	if err := f.client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving %q: %v", title, err)
	}
	return task.ID
}

// dispatch runs one task to the end of its first attempt.
func (f *hostdFixture) dispatch(t *testing.T, ctx context.Context, framework, title, fields string) finishedAttempt {
	t.Helper()
	id := f.start(t, ctx, framework, title, fields)
	var attempt ui.Attempt
	waitFor(t, "the "+title+" attempt to finish", 3*time.Minute, func() error {
		detail, err := f.client.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if len(detail.Attempts) == 0 || detail.Attempts[0].FinishedAt == nil {
			return errors.New("the attempt is still live")
		}
		attempt = detail.Attempts[0]
		return nil
	})
	_ = f.client.Close(ctx, id, ui.CloseOptions{})
	return finishedAttempt{Attempt: attempt, taskID: id}
}

// waitForPids waits for a hanging fake agy to say which processes are
// its group: itself and the child it started.
func (f *hostdFixture) waitForPids(t *testing.T, report string) []string {
	t.Helper()
	var pids []string
	waitFor(t, "the hanging agy to start on the host", 3*time.Minute, func() error {
		pids = strings.Fields(sudoRead(report + ".pids"))
		if len(pids) != 2 {
			return errors.New("no pids yet")
		}
		return nil
	})
	return pids
}

func gone(pids []string) error {
	for _, pid := range pids {
		if _, err := os.Stat("/proc/" + pid); err == nil {
			return fmt.Errorf("pid %s is still running", pid)
		}
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
