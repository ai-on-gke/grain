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

package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// ciMCPConfig is the mcp.json a CI job's sandbox was given.
type ciMCPConfig struct {
	MCPServers map[string]struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"mcpServers"`
}

// placed is what sb was handed for path, decoded.
func (s *commandSandbox) placed(t *testing.T, path string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, argv := range s.runs {
		if len(argv) > 4 && argv[2] == placeScript && argv[4] == path {
			data, err := base64.StdEncoding.DecodeString(s.stdins[i])
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("nothing was placed at %s; commands: %v", path, s.describe())
	return ""
}

func ciTestServers(t *testing.T, origin string) CapabilityServers {
	t.Helper()
	srv, err := protocol.NewServer(context.Background(), passTestCap, &mintCaller{})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetDataPlane(protocol.DataPlane{Origin: origin, Headers: map[string]string{"Authorization": "Bearer data-plane"}}, nil)
	return oneServer{srv}
}

// TestMaterializeCIPassReachesEachEndpointFromInsideTheSandbox runs the
// real guest forwarder locally, standing in for the job's sandbox: the URL
// mcp.json lists reaches the grant's /runs/<attempt_id>/mcp through the
// relay and grain's own proxy, and nothing else.
func TestMaterializeCIPassReachesEachEndpointFromInsideTheSandbox(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("socket ownership is read from /proc")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 to run the guest forwarder")
	}
	fake := &fakeDataPlane{
		names: map[string]string{"grain-task-1-1-0": "self-debug", "grain-task-1-1-1": "self-debug"},
		tools: []string{"read_grain_source"},
	}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	relay := portforward.NewRelay()
	relaySrv := httptest.NewServer(relay)
	defer relaySrv.Close()

	base := ciEndpointPortBase
	t.Cleanup(func() { ciEndpointPortBase = base })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ciEndpointPortBase = ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// The sandbox runs each guest forwarder it is asked to, here, from
	// the script and token grain placed for it.
	dir := t.TempDir()
	var mu sync.Mutex
	var started []int
	var sb *commandSandbox
	sb = &commandSandbox{answer: func(argv []string) (string, string, int) {
		if argv[0] != "python3" {
			return "", "", 0
		}
		script := filepath.Join(dir, "forwarder.py")
		token := filepath.Join(dir, argv[2]+".token")
		os.WriteFile(script, []byte(portforward.GuestForwarder), 0o600)
		os.WriteFile(token, []byte(sb.placedLocked(argv[5])), 0o600)
		out, err := exec.Command("python3", script, argv[2], argv[3], argv[4], token).CombinedOutput()
		if err != nil {
			return "", string(out), 1
		}
		port, _ := strconv.Atoi(argv[2])
		mu.Lock()
		started = append(started, port)
		mu.Unlock()
		return "", "", 0
	}}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, port := range started {
			if pid, err := portforward.ListenerOwner(port); err == nil && pid != os.Getpid() {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	pass := testPass(
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-0/mcp"},
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-1/mcp"},
	)
	opts := CIPassOptions{Ports: relay, PortsURL: relaySrv.URL, Servers: ciTestServers(t, upstream.URL)}
	m, err := MaterializeCIPass(context.Background(), opts, sb, pass, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	var cfg ciMCPConfig
	if err := json.Unmarshal([]byte(sb.placed(t, CIMCPConfigPath)), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("mcp.json lists %v, want self-debug and self-debug-2", cfg.MCPServers)
	}
	for _, name := range []string{"self-debug", "self-debug-2"} {
		s := cfg.MCPServers[name]
		if s.Type != "http" || !strings.HasPrefix(s.URL, "http://127.0.0.1:") || strings.Contains(s.URL, "/runs/") {
			t.Fatalf("mcp.json lists %s as %+v, not a sandbox-local proxy URL", name, s)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		roster, err := protocol.Handshake(ctx, s.URL)
		cancel()
		if err != nil {
			t.Fatalf("handshaking %s from inside the sandbox: %v", name, err)
		}
		if roster.ServerName != "self-debug" || strings.Join(roster.Tools, ",") != "read_grain_source" {
			t.Errorf("%s answered %+v", name, roster)
		}
	}
	if len(started) != 2 || started[0] != ciEndpointPortBase || started[1] != ciEndpointPortBase+1 {
		t.Errorf("guest forwarders started on %v, want the two ports from %d", started, ciEndpointPortBase)
	}

	// The job's retry in a fresh sandbox is pointed at the same proxies,
	// under the same mcp.json, without minting.
	again := &commandSandbox{}
	if err := m.Apply(context.Background(), again); err != nil {
		t.Fatal(err)
	}
	if got := again.placed(t, CIMCPConfigPath); got != m.MCPConfig() {
		t.Errorf("the retry's mcp.json = %s, want %s", got, m.MCPConfig())
	}
	var forwards []string
	for _, d := range again.describe() {
		if strings.HasPrefix(d, "forward ") {
			forwards = append(forwards, d)
		}
	}
	if len(forwards) != 2 {
		t.Errorf("the retry's sandbox started forwarders %v, want one per endpoint", forwards)
	}
}

// placedLocked is placed for the forwarder's own answer, which already
// runs inside RunInSandbox, so it reads the last stdin handed over for
// path without t.
func (s *commandSandbox) placedLocked(path string) string {
	for i := len(s.runs) - 1; i >= 0; i-- {
		if argv := s.runs[i]; len(argv) > 4 && argv[2] == placeScript && argv[4] == path {
			data, _ := base64.StdEncoding.DecodeString(s.stdins[i])
			return string(data)
		}
	}
	return ""
}

func TestMaterializeCIPassRefusesEndpointsWithoutARelay(t *testing.T) {
	fake := &fakeDataPlane{names: map[string]string{"grain-task-1-1-0": "self-debug"}, tools: []string{"t"}}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	pass := testPass(&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-0/mcp"})
	_, err := MaterializeCIPass(context.Background(), CIPassOptions{Servers: ciTestServers(t, upstream.URL)},
		&commandSandbox{}, pass, time.Now().Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "no capability port relay") {
		t.Fatalf("an endpoint with no relay = %v", err)
	}

	// A pass with no mcp_endpoint writes no mcp.json at all.
	sb := &commandSandbox{}
	m, err := MaterializeCIPass(context.Background(), CIPassOptions{}, sb,
		testPass(&protocol.MintResponse{Placements: []protocol.Placement{{Path: "/home/debian/.k", Content: "k"}}}), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.MCPConfig() != "" || strings.Contains(strings.Join(sb.describe(), "\n"), CIMCPConfigPath) {
		t.Errorf("a pass with no endpoints wrote %q: %v", m.MCPConfig(), sb.describe())
	}
}
