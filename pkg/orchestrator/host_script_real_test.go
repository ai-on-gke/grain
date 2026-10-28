package orchestrator_test

// The host-script capability (pkg/capability/intree/hostscript.go) lets a
// sandbox run an operator's script on grain's side as `grain host-script
// <name>`. Its own tests cover the server, a real hostd starting it, and
// the minted placements used in a plain container with the files
// bind-mounted where they belong. None of that goes through grain's materialization, which is what
// a real run gets: the pass minted from a task's grant, its port opened on
// the capability port relay and forwarded into the guest by grain's own
// forwarder, its placements written by placeInSandbox, and grain host-script
// called through the sandbox's own run_command. The tests here do exactly
// that, against a real granule VM and a real container sandbox.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/capability/portforward"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/granule"
	"github.com/bwsalmon/grain/pkg/hostscriptcli"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

const (
	// hostScriptServeEnv makes this test binary serve the host-script
	// server over stdio, with its data directory in hostScriptDataEnv:
	// the stand-in for `grain capability host-script`, started as a stdio
	// child the way the daemon starts an attached server, so its
	// data-plane listener belongs to a service process of its own.
	hostScriptServeEnv = "GRAIN_ORCHESTRATOR_TEST_HOST_SCRIPT"
	hostScriptDataEnv  = "GRAIN_ORCHESTRATOR_TEST_HOST_SCRIPT_DATA"

	hostScriptID = "https://github.com/bwsalmon/grain/capabilities/host-script"
)

// serveHostScriptIfAsked is TestMain's first act: in the child a test
// starts as a capability server, it serves and exits.
func serveHostScriptIfAsked() {
	if os.Getenv(hostScriptServeEnv) != "1" {
		return
	}
	s, err := intree.NewHostScript(intree.HostScriptConfig{Listen: "127.0.0.1:0", DataDir: os.Getenv(hostScriptDataEnv)})
	if err == nil {
		err = s.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// hostScriptName is the one script the server is configured with. It
// prints a line, waits, prints another -- so a caller can see the first
// before the script has finished -- then its arguments one per line and
// a line to stderr, and exits 7.
const hostScriptName = "streamargs"

const hostScriptBody = `echo first; sleep 3; echo second; printf 'arg:%s\n' "$@"; echo to-stderr >&2; exit 7`

// attachHostScript attaches the host-script server as a stdio child of
// this test binary, configured with hostScriptName.
func attachHostScript(t *testing.T) *protocol.Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := protocol.ServerConfig{
		Command: exe,
		Env:     map[string]string{hostScriptServeEnv: "1", hostScriptDataEnv: t.TempDir()},
		Config: map[string]any{"scripts": []any{map[string]any{
			"name":        hostScriptName,
			"description": "streams two lines, then its arguments",
			"usage":       hostScriptName + " ARGS...",
			"script":      hostScriptBody,
			"timeout":     60,
		}}},
	}
	srv, err := protocol.Attach(context.Background(), hostScriptID, cfg,
		func(string) (string, bool) { return "", false }, protocol.DialOptions{Stderr: os.Stderr})
	if err != nil {
		t.Fatalf("attaching the host-script server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// serveRelay serves a capability port relay on every interface, as the
// daemon serves it on the sandbox fabric, and returns it with the URL a
// guest reaches it at through gateway.
func serveRelay(t *testing.T, gateway string) (*portforward.Relay, string) {
	t.Helper()
	relay := portforward.NewRelay()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: relay}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return relay, fmt.Sprintf("http://%s:%d", gateway, ln.Addr().(*net.TCPAddr).Port)
}

var runCommandResult = regexp.MustCompile(`(?s)^exit=(-?\d+)\nstdout:\n(.*)\nstderr:\n(.*)$`)

// runIn runs command through the sandbox's own run_command tool, the one
// an agent calls, and returns the exit status, stdout and stderr it
// reports.
func runIn(t *testing.T, runCommand mcp.Tool, command string) (code int, stdout, stderr string) {
	t.Helper()
	res := runCommand.Handler(context.Background(), map[string]any{"command": command})
	m := runCommandResult.FindStringSubmatch(res.Text)
	if m == nil {
		t.Fatalf("run_command %q answered %q, not exit=/stdout:/stderr:", command, res.Text)
	}
	code, _ = strconv.Atoi(m[1])
	return code, m[2], m[3]
}

// exerciseHostScript grants the host-script server's script to a run in
// sb, materializes the pass into it as a dispatch does, and checks what
// the agent sees: the grant's descriptor where it belongs, the server's
// port forwarded, grain on run_command's PATH (the image's own, or the
// stand-in the grant's setup installs), `grain host-script <name>`'s
// output streamed with its argv carried exactly and its exit status
// propagated, --grain-help and --list, and, after the grant is released,
// exit 125.
func exerciseHostScript(t *testing.T, sb orchestrator.Sandbox, gateway string, reserved map[int]bool) {
	t.Helper()
	ctx := context.Background()
	srv := attachHostScript(t)
	lookup := func(id string) (*protocol.Server, error) {
		if id != hostScriptID {
			return nil, fmt.Errorf("no capability server %s", id)
		}
		return srv, nil
	}

	// The task's grant, as a task records it, resolved and minted the way
	// admission mints it (mintCapabilityPass).
	grant := hostScriptID + "#host-script=" + `{"script":"` + hostScriptName + `"}`
	grants, err := model.CapabilityGrants([]model.Grant{{Capability: grant}}, lookup)
	if err != nil {
		t.Fatalf("resolving the grant %s: %v", grant, err)
	}
	deadline := time.Now().Add(time.Hour)
	pass, err := protocol.MintPass(ctx, grants, func(string) (protocol.Minter, error) { return srv, nil }, protocol.PassOptions{
		Deployment: "grain", Task: "5145", Attempt: 1, Deadline: deadline, ReservedSandboxPorts: reserved,
	})
	if err != nil {
		t.Fatalf("minting the pass: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_, _ = pass.Release(context.Background())
		}
	})
	if len(pass.Grants) != 1 || len(pass.Ports()) != 1 {
		t.Fatalf("the pass holds %d grants and %d ports, want one of each", len(pass.Grants), len(pass.Ports()))
	}
	attempt := pass.Grants[0].AttemptID
	port := pass.Ports()[0].SandboxPort

	relay, relayURL := serveRelay(t, gateway)
	closeForwards, err := orchestrator.MaterializePass(ctx, relay, relayURL, sb, pass, deadline, func(id string) (portforward.Service, error) {
		s, err := lookup(id)
		if err != nil {
			return portforward.Service{}, err
		}
		return portforward.ServiceFor(s.ServiceProcess())
	})
	if err != nil {
		t.Fatalf("materializing the host-script grant into the sandbox: %v", err)
	}
	t.Cleanup(closeForwards)

	tools, err := sb.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var runCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			runCommand = &tools[i]
		}
	}
	if runCommand == nil {
		t.Fatalf("the sandbox has no run_command tool")
	}

	call := "grain host-script " + hostScriptName
	descriptor := hostscriptcli.Dir + "/" + hostScriptName + ".json"
	t.Run("placements", func(t *testing.T) {
		code, out, errs := runIn(t, *runCommand, "stat -c '%n %a %U' "+descriptor+"; test ! -e "+intree.HostScriptBinDir+"/"+hostScriptName)
		want := descriptor + " 600 debian\n"
		if code != 0 || out != want {
			t.Errorf("stat: exit %d, %q %q; want %q and no per-script command", code, out, errs, want)
		}
		if code, out, _ := runIn(t, *runCommand, "grep -c "+attempt+" "+descriptor); code != 0 || out != "1\n" {
			t.Errorf("the descriptor does not name the grant %s", attempt)
		}
	})
	t.Run("port forwarded", func(t *testing.T) {
		// The server's data plane answers inside the sandbox, on the port
		// the command calls: a call with no token is refused by the
		// server itself, which only the forward can have carried it to.
		code, out, errs := runIn(t, *runCommand, fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' -X POST http://127.0.0.1:%d/runs/%s/exec", port, attempt))
		if code != 0 || out != "401" {
			t.Errorf("an untokened call to 127.0.0.1:%d in the sandbox: exit %d, %q %q; want the server's 401", port, code, out, errs)
		}
	})
	t.Run("on PATH", func(t *testing.T) {
		if code, out, errs := runIn(t, *runCommand, "command -v grain"); code != 0 || out == "" {
			t.Errorf("command -v grain: exit %d, %q %q; want grain on PATH", code, out, errs)
		}
		if code, out, errs := runIn(t, *runCommand, "grain host-script --list"); code != 0 || !strings.HasPrefix(out, hostScriptName+"\t") {
			t.Errorf("grain host-script --list: exit %d, %q %q; want %s and its description", code, out, errs, hostScriptName)
		}
	})
	t.Run("call", func(t *testing.T) {
		// Each line is stamped as it arrives. The script sleeps between
		// its first two, so a streamed answer shows the gap and a
		// buffered one does not. PIPESTATUS keeps the command's status.
		code, out, errs := runIn(t, *runCommand, call+` 'two words' '$(id)' '' 2>/tmp/hs-err | while IFS= read -r l; do echo "$(date +%s.%N) $l"; done; s=${PIPESTATUS[0]}; cat /tmp/hs-err >&2; exit $s`)
		if code != 7 {
			t.Errorf("exit %d (stderr %q), want the script's own 7", code, errs)
		}
		if strings.TrimSpace(errs) != "to-stderr" {
			t.Errorf("stderr %q, want the script's to-stderr", errs)
		}
		var at []float64
		var lines []string
		for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			stamp, text, _ := strings.Cut(l, " ")
			f, _ := strconv.ParseFloat(stamp, 64)
			at, lines = append(at, f), append(lines, text)
		}
		want := []string{"first", "second", "arg:two words", "arg:$(id)", "arg:"}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Fatalf("stdout lines %q, want %q", lines, want)
		}
		if gap := at[1] - at[0]; gap < 2 {
			t.Errorf("first and second arrived %.2fs apart, want the script's 3s sleep between them: its output is not streamed", gap)
		}
	})
	t.Run("grain-help", func(t *testing.T) {
		code, out, errs := runIn(t, *runCommand, call+" --grain-help")
		if code != 0 || !strings.Contains(out, hostScriptName+" ARGS...") {
			t.Errorf("--grain-help: exit %d, %q %q; want the usage", code, out, errs)
		}
	})
	if shell, ok := sb.(orchestrator.SandboxShell); ok {
		// A grain CI step on this backend runs through RunShell rather
		// than run_command, and calls grain host-script too.
		t.Run("CI step", func(t *testing.T) {
			res, err := shell.RunShell(ctx, orchestrator.ShellRequest{Script: call + " ci-arg", Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != 7 || !strings.Contains(res.Output, "arg:ci-arg") {
				t.Errorf("a CI step calling grain host-script %s: exit %d, %q; want 7 and its argument", hostScriptName, res.ExitCode, res.Output)
			}
		})
	}
	t.Run("after release", func(t *testing.T) {
		// What the end of a run does: release every grant, then close the
		// forwards.
		if _, err := pass.Release(ctx); err != nil {
			t.Fatal(err)
		}
		released = true
		closeForwards()
		code, _, errs := runIn(t, *runCommand, call+" x")
		if code != 125 || !strings.Contains(errs, hostScriptName+":") {
			t.Errorf("after release: exit %d, stderr %q; want 125 and the command saying why", code, errs)
		}
	})
}

// TestHostScriptGrantInARealGranuleVM materializes a host-script grant
// into a real granule VM. It runs where the rest of this package's
// real-VM suites do (granuleDockerRealTestPrereqs).
func TestHostScriptGrantInARealGranuleVM(t *testing.T) {
	granuleDockerRealTestPrereqs(t)
	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)
	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		CreateArgs: []string{
			"-kontur-image", image,
			"-guest-port", "22",
			"-guest-user", "debian",
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
	})
	name, err := k.VMNameFor("hs1-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})
	sb, err := k.Acquire(context.Background(), "hs1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire against a real granule VM: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })

	// The relay is reached at the gateway the guest's namespace routes
	// through, as the model proxies are (model_proxy_real_test.go).
	gateway := dockerInspect(t, "granule-vm-"+name, `{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}`)
	reserved := map[int]bool{}
	for _, p := range k.ReservedGuestPorts() {
		reserved[p] = true
	}
	exerciseHostScript(t, sb, gateway, reserved)
}

// hostScriptContainerImageEnv names the image
// TestHostScriptGrantInARealContainerSandbox runs, which must already be
// in the local docker daemon.
const hostScriptContainerImageEnv = "GRAIN_HOST_SCRIPT_CONTAINER_IMAGE"

// TestHostScriptGrantInARealContainerSandbox is the same grant
// materialized into a container sandbox, configured as the daemon
// configures its container backend (pkg/daemon/run.go). Opt-in on the
// image named in GRAIN_HOST_SCRIPT_CONTAINER_IMAGE.
//
// The image has to hold the sandbox contract capability materialization
// assumes (docs/capabilities-spec.md §5.1, §5.3): a sandbox user debian
// who owns /home/debian, and python3 for grain's guest forwarder. The
// stock container sandbox image, the k8s-sandbox image the
// -container-sandbox-image flag defaults to, holds it, and tests.yml's
// host-script-container job runs this against that image built from this
// commit. Before it did -- it ran as agent in /home/agent with no python3
// -- this failed at the first placement with "mkdir: cannot create
// directory '/home/debian': Permission denied", as did every capability
// grant with a port or a placement in such a sandbox.
func TestHostScriptGrantInARealContainerSandbox(t *testing.T) {
	image := os.Getenv(hostScriptContainerImageEnv)
	if image == "" {
		t.Skipf("%s not set; set it to a sandbox image in the local docker daemon to run this", hostScriptContainerImageEnv)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Fatalf("%s=%s is not in the local docker daemon: %v", hostScriptContainerImageEnv, image, err)
	}
	c := orchestrator.NewContainerSandboxes(orchestrator.ContainerConfig{Image: image, Workspace: "/home/agent"})
	sb, err := c.Acquire(context.Background(), "hs-container-"+strconv.Itoa(os.Getpid()), orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire against a real container sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	container := sb.(interface{ ContainerName() string }).ContainerName()
	gateway := dockerInspect(t, container, `{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}`)
	exerciseHostScript(t, sb, gateway, nil)
}
