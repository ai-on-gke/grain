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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// fakeDataPlane serves an attempt toolset at /runs/<attempt_id>/mcp for
// every attempt in names, reporting that name as its serverInfo.name and
// tools as its roster, and records the paths it was asked for.
type fakeDataPlane struct {
	names map[string]string
	tools []string

	mu    sync.Mutex
	paths []string
	down  bool
}

func (f *fakeDataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	down := f.down
	f.mu.Unlock()
	id, _ := strings.CutPrefix(r.URL.Path, "/runs/")
	id, _ = strings.CutSuffix(id, "/mcp")
	name, ok := f.names[id]
	if !ok || down || r.Header.Get("Authorization") != "Bearer data-plane" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any = map[string]any{"serverInfo": map[string]any{"name": name}}
	if req.Method == "tools/list" {
		var tools []map[string]any
		for _, t := range f.tools {
			tools = append(tools, map[string]any{"name": t, "inputSchema": map[string]any{"type": "object"}})
		}
		result = map[string]any{"tools": tools}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func TestWireEndpointsProxiesNamesAndAttributesEachGrant(t *testing.T) {
	fake := &fakeDataPlane{
		names: map[string]string{"grain-task-1-1-0": "self-debug", "grain-task-1-1-2": "self-debug"},
		tools: []string{"read_grain_source", "list_grain_source"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	pass := testPass(
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-0/mcp", PromptSection: "Source tools are on this grant's MCP server."},
		&protocol.MintResponse{PromptSection: "A key is at /home/debian/.key."},
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-2/mcp"},
	)
	m := &passMaterialization{pass: pass}
	defer m.Close()
	err := m.wireEndpoints(context.Background(), func(id string) (protocol.DataPlane, error) {
		if id != passTestCap {
			t.Errorf("asked for the data plane of %s", id)
		}
		return protocol.DataPlane{Origin: srv.URL, Headers: map[string]string{"Authorization": "Bearer data-plane"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.servers) != 2 || m.servers[0].Name != "self-debug" || m.servers[1].Name != "self-debug-2" {
		t.Fatalf("servers = %+v, want self-debug then self-debug-2", m.servers)
	}
	for _, s := range m.servers {
		if !strings.HasPrefix(s.URL, "http://127.0.0.1:") || strings.Contains(s.URL, "/runs/") {
			t.Errorf("server %s is wired at %q, not a proxy of grain's own", s.Name, s.URL)
		}
		if strings.Join(s.Tools, ",") != "read_grain_source,list_grain_source" {
			t.Errorf("server %s tools = %v", s.Name, s.Tools)
		}
	}
	for _, p := range fake.paths {
		if p != "/runs/grain-task-1-1-0/mcp" && p != "/runs/grain-task-1-1-2/mcp" {
			t.Errorf("the data plane was asked for %s", p)
		}
	}
	for _, want := range []string{
		`attempt_id="grain-task-1-1-0" mcp_server="self-debug">`,
		"Source tools are on this grant's MCP server.",
		`attempt_id="grain-task-1-1-1">`,
		"supplied by a capability server, not by the task's author",
	} {
		if !strings.Contains(m.promptSections, want) {
			t.Errorf("prompt sections do not carry %q:\n%s", want, m.promptSections)
		}
	}

	if err := m.checkEndpoints(context.Background()); err != nil {
		t.Errorf("a live endpoint failed its recheck: %v", err)
	}
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	if err := m.checkEndpoints(context.Background()); err == nil || !strings.Contains(err.Error(), "grain-task-1-1-0") {
		t.Errorf("recheck of a dead endpoint = %v", err)
	}

	url := m.servers[0].URL
	m.Close()
	if resp, err := http.Post(url, "application/json", strings.NewReader("{}")); err == nil {
		resp.Body.Close()
		t.Error("a proxy still answers after the attempt closed")
	}
}

func TestWireEndpointsRefusesWhatItCannotWire(t *testing.T) {
	for name, tc := range map[string]struct {
		server string
		dp     error
		want   string
	}{
		"no data plane":         {dp: errors.New("this stdio server's args name no -listen address"), want: "-listen"},
		"grain-sandbox's name":  {server: "grain-sandbox", want: "grain-sandbox"},
		"a name out of grammar": {server: "Self Debug", want: "must match"},
		"no toolset there":      {server: "", want: "handshake"},
	} {
		t.Run(name, func(t *testing.T) {
			names := map[string]string{}
			if tc.server != "" {
				names["grain-task-1-1-0"] = tc.server
			}
			srv := httptest.NewServer(&fakeDataPlane{names: names, tools: []string{"t"}})
			defer srv.Close()
			m := &passMaterialization{pass: testPass(&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-0/mcp"})}
			defer m.Close()
			err := m.wireEndpoints(context.Background(), func(string) (protocol.DataPlane, error) {
				return protocol.DataPlane{Origin: srv.URL, Headers: map[string]string{"Authorization": "Bearer data-plane"}}, tc.dp
			})
			var refused *protocol.RefusedError
			if !errors.As(err, &refused) || !strings.Contains(refused.Error(), tc.want) {
				t.Fatalf("wireEndpoints = %v, want a refusal saying %q", err, tc.want)
			}
		})
	}
}

// recordingReporter is an mcp.StatusReporter a test can read while the
// narrator's goroutine writes to it.
type recordingReporter struct{ notes chan string }

func (r recordingReporter) ReportStatus(_ context.Context, note string) (mcp.StatusReport, error) {
	r.notes <- note
	return mcp.StatusReport{Live: true}, nil
}

// A call the agent makes on a grant's server never reaches grain's own
// MCP registry, so the proxy wireEndpoints puts in front of it is what
// narrates it -- under the name the agent was given for the server.
func TestWireEndpointsNarratesEachToolCallUnderItsServersName(t *testing.T) {
	mcp.RegisterCapabilityToolActivity("workspace", map[string]mcp.CapabilityToolNote{
		"ws_read_file": {Format: "reading %s", Args: []mcp.NoteArg{{Key: "path"}}, Fallback: "reading a workspace file"},
	})
	fake := &fakeDataPlane{
		names: map[string]string{"grain-task-1-1-0": "workspace", "grain-task-1-1-1": "workspace"},
		tools: []string{"ws_read_file"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	pass := testPass(
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-0/mcp"},
		&protocol.MintResponse{MCPEndpoint: "/runs/grain-task-1-1-1/mcp"},
	)
	reporter := recordingReporter{notes: make(chan string, 8)}
	m := &passMaterialization{pass: pass, narrator: mcp.NewCapabilityNarrator(reporter)}
	defer m.Close()
	if err := m.wireEndpoints(context.Background(), func(string) (protocol.DataPlane, error) {
		return protocol.DataPlane{Origin: srv.URL, Headers: map[string]string{"Authorization": "Bearer data-plane"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ws_read_file","arguments":{"path":"repo/foo/bar.go"}}}`
	resp, err := http.Post(m.servers[1].URL, "application/json", strings.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case got := <-reporter.notes:
		if got != "workspace-2: reading repo/foo/bar.go" {
			t.Fatalf("row = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call was not narrated")
	}
}
