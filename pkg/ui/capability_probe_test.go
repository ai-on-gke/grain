package ui_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/capability/servers"
	"github.com/bwsalmon/grain/pkg/ui"
)

// attachBuiltinOnItsDefaultPort attaches the built-in name, run as this
// test binary (serveCapabilityIfForked), with args and env and no
// -listen, so its data plane binds the server's default port. When that
// port is already held -- by another test, or in a sandbox by grain's own
// git proxy on 9728 -- the attach fails on it; either way the port is
// held while the caller probes, which is the case under test.
func attachBuiltinOnItsDefaultPort(t *testing.T, name string, port int, args []string, env map[string]string, config map[string]any) (*servers.Registry, protocol.ServerConfig) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := intree.CanonicalID(name)
	cfg := protocol.ServerConfig{Command: exe, Args: append([]string{"capability", name}, args...), Env: env, Builtin: name, Config: config}
	held := false
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err != nil {
		held = true
		t.Logf("port %d is already held: %v", port, err)
	} else {
		ln.Close()
	}
	reg := servers.New(servers.Options{})
	t.Cleanup(reg.Close)
	reg.Sync(context.Background(), map[string]protocol.ServerConfig{id: cfg})
	for _, st := range reg.Statuses() {
		if st.ID != id {
			continue
		}
		if st.State != servers.Attached && !held {
			t.Fatalf("attaching %s: %s: %s", name, st.State, st.Error)
		}
	}
	return reg, cfg
}

// testWithRegistry is TestCapabilityServer on a daemon whose prober is
// reg.
func testWithRegistry(t *testing.T, reg *servers.Registry, req ui.TestCapabilityServerRequest) servers.ProbeResult {
	t.Helper()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.CapabilityServers = reg
	res, err := client.TestCapabilityServer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestTestingAnAttachedHostScriptProbesBesideIt is #1000: Test of the
// host-script built-in while it is attached started a second server on
// the attached one's data-plane port, which exited on the bind before it
// answered initialize ("closed its output: EOF"). A probe binds an
// ephemeral port instead.
func TestTestingAnAttachedHostScriptProbesBesideIt(t *testing.T) {
	scripts := func(name string) map[string]any {
		return map[string]any{"scripts": []any{map[string]any{"name": name, "description": name, "timeout": float64(60), "script": "echo hi\n"}}}
	}
	data := t.TempDir()
	reg, cfg := attachBuiltinOnItsDefaultPort(t, "host-script", intree.DefaultHostScriptPort, []string{"-data-dir", data}, nil, scripts("attached"))

	next := cfg
	next.Config = scripts("x")
	res := testWithRegistry(t, reg, ui.TestCapabilityServerRequest{ID: intree.CanonicalID("host-script"), Config: &next})
	if !res.OK || len(res.Methods) != 1 || res.Methods[0].Name != "host-script" {
		t.Fatalf("Test of an attached host-script = %+v", res)
	}
}

// TestTestingAnAttachedGitProbesBesideIt is the same for the git
// built-in, whose proxy listens on DefaultGitPort.
func TestTestingAnAttachedGitProbesBesideIt(t *testing.T) {
	reg, cfg := attachBuiltinOnItsDefaultPort(t, "git", intree.DefaultGitPort, nil, map[string]string{intree.GitTokenEnv: "test-token"},
		map[string]any{"repos": []any{"o/attached"}})

	next := cfg
	next.Config = map[string]any{"repos": []any{"o/next"}}
	res := testWithRegistry(t, reg, ui.TestCapabilityServerRequest{ID: intree.CanonicalID("git"), Config: &next})
	if !res.OK || len(res.Methods) == 0 {
		t.Fatalf("Test of an attached git = %+v", res)
	}
}

// TestAProbeThatExitsEarlySaysWhy: a stdio server that exits before it
// answers initialize has its stderr in the probe's error, not just
// "closed its output".
func TestAProbeThatExitsEarlySaysWhy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reg := servers.New(servers.Options{})
	t.Cleanup(reg.Close)
	cfg := protocol.ServerConfig{Command: exe, Args: []string{"capability", "no-such-server"}}
	res := testWithRegistry(t, reg, ui.TestCapabilityServerRequest{ID: "https://capabilities.example.com/broken", Config: &cfg})
	if res.OK || !strings.Contains(res.Error, "closed its output") || !strings.Contains(res.Error, `no in-tree capability "no-such-server"`) {
		t.Fatalf("Test of a server that exits at once = %+v", res)
	}
}
