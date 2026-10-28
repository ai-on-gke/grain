package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeToolset is an upstream data plane: it answers initialize and
// tools/list on /runs/<attempt>/mcp and records what reached it.
type fakeToolset struct {
	name  string
	tools []string

	mu   sync.Mutex
	seen []*http.Request
}

func (f *fakeToolset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Clone(context.Background()))
	f.mu.Unlock()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
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
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"serverInfo": map[string]any{"name": f.name}}
	case "tools/list":
		var tools []map[string]any
		for _, t := range f.tools {
			tools = append(tools, map[string]any{"name": t, "inputSchema": map[string]any{"type": "object"}})
		}
		result = map[string]any{"tools": tools}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", "s-1")
	w.Header().Set("Set-Cookie", "upstream=1")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (f *fakeToolset) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.seen...)
}

func TestProxyTellsItsObserverOfEachToolCallBeforeForwardingIt(t *testing.T) {
	up := &fakeToolset{name: "workspace", tools: []string{"ws_diff"}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p, err := NewProxy(DataPlane{Origin: srv.URL}, "dep-7-1-0", nil)
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		tool string
		args map[string]any
		sent int
	}
	var calls []call
	p.ObserveToolCalls(func(tool string, args map[string]any) {
		calls = append(calls, call{tool, args, len(up.requests())})
	})
	front := httptest.NewServer(p)
	defer front.Close()

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ws_read_file","arguments":{"path":"a.go"}}}`,
		`[{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ws_diff"}},{"jsonrpc":"2.0","method":"notifications/initialized"}]`,
		`not json`,
	} {
		resp, err := http.Post(front.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if len(calls) != 2 || calls[0].tool != "ws_read_file" || calls[0].args["path"] != "a.go" || calls[1].tool != "ws_diff" {
		t.Fatalf("observed %+v, want ws_read_file then ws_diff", calls)
	}
	if calls[0].sent != 1 {
		t.Errorf("ws_read_file was observed after %d requests reached the upstream, want before its own", calls[0].sent)
	}
	// Every request was forwarded, the one that is not JSON included: the
	// observer reads requests, it never refuses them.
	if got := len(up.requests()); got != 4 {
		t.Errorf("upstream saw %d requests, want 4", got)
	}
}

func TestHandshakeLabelsEachToolFromWhatItSaysOfItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{"serverInfo": map[string]any{"name": "acme"}}
		if req.Method == "tools/list" {
			schema := map[string]any{"type": "object"}
			result = map[string]any{"tools": []map[string]any{
				{"name": "a", "inputSchema": schema, "title": "Title A", "description": "Desc A", "annotations": map[string]any{"title": "Annotated A"}},
				{"name": "b", "inputSchema": schema, "title": "Title B", "description": "Desc B"},
				{"name": "c", "inputSchema": schema, "description": "Desc C"},
				{"name": "d", "inputSchema": schema},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()
	roster, err := Handshake(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "Annotated A", "b": "Title B", "c": "Desc C"}
	if len(roster.Labels) != len(want) {
		t.Fatalf("labels = %v, want %v", roster.Labels, want)
	}
	for k, v := range want {
		if roster.Labels[k] != v {
			t.Errorf("label of %s = %q, want %q", k, roster.Labels[k], v)
		}
	}
}

func TestProxyReplacesThePathAndRewritesHeaders(t *testing.T) {
	up := &fakeToolset{name: "self-debug", tools: []string{"read_grain_source"}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p, err := NewProxy(DataPlane{Origin: srv.URL, Headers: map[string]string{"Authorization": "Bearer server-secret", "X-Extra": "1"}}, "dep-7-1-0", nil)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/runs/other-attempt/mcp?x=1", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Authorization", "Bearer client-supplied")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", "s-0")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	req.Header.Set("Last-Event-ID", "7")
	req.Header.Set("Cookie", "c=1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Mcp-Session-Id"); got != "s-1" {
		t.Errorf("Mcp-Session-Id = %q, want the upstream's", got)
	}
	if got := resp.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie %q was handed back", got)
	}

	got := up.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests reached upstream, want 1", len(got))
	}
	r := got[0]
	if r.URL.Path != "/runs/dep-7-1-0/mcp" || r.URL.RawQuery != "" {
		t.Errorf("upstream saw %s?%s, want exactly /runs/dep-7-1-0/mcp", r.URL.Path, r.URL.RawQuery)
	}
	if a := r.Header.Get("Authorization"); a != "Bearer server-secret" {
		t.Errorf("Authorization = %q, want the server's configured header", a)
	}
	for h, want := range map[string]string{"X-Extra": "1", "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"Mcp-Session-Id": "s-0", "Mcp-Protocol-Version": "2025-06-18", "Last-Event-Id": "7", "Cookie": "", "X-Forwarded-For": ""} {
		if v := r.Header.Get(h); v != want {
			t.Errorf("upstream %s = %q, want %q", h, v, want)
		}
	}
}

func TestProxyCarriesOnlyStreamableHTTPMethods(t *testing.T) {
	up := &fakeToolset{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p, _ := NewProxy(DataPlane{Origin: srv.URL}, "a-1", nil)
	front := httptest.NewServer(p)
	defer front.Close()
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodOptions, "CONNECT"} {
		req, _ := http.NewRequest(m, front.URL+"/x", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d, want 405", m, resp.StatusCode)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(m, front.URL+"/x", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if n := len(up.requests()); n != 2 {
		t.Errorf("%d requests reached upstream, want GET and DELETE only", n)
	}
}

func TestProxyNeverHandsOnARedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/mcp", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	p, _ := NewProxy(DataPlane{Origin: srv.URL}, "a-1", nil)
	front := httptest.NewServer(p)
	defer front.Close()
	resp, err := http.Post(front.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Location") != "" {
		t.Errorf("redirect answered %d Location=%q, want 502 and no Location", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestEndpointHandshake(t *testing.T) {
	up := &fakeToolset{name: "self-debug", tools: []string{"read_grain_source", "list_grain_source"}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p, _ := NewProxy(DataPlane{Origin: srv.URL}, "a-1", nil)
	ep, err := ServeEndpoint(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if !strings.HasPrefix(ep.URL, "http://127.0.0.1:") || !strings.HasSuffix(ep.URL, "/mcp") {
		t.Fatalf("endpoint URL %q", ep.URL)
	}
	roster, err := Handshake(context.Background(), ep.URL)
	if err != nil {
		t.Fatal(err)
	}
	if roster.ServerName != "self-debug" || strings.Join(roster.Tools, ",") != "read_grain_source,list_grain_source" {
		t.Errorf("roster = %+v", roster)
	}

	// Only the endpoint's own secret path is served.
	resp, err := http.Post(strings.TrimSuffix(ep.URL, "/mcp")+"x/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another path answered %d, want 404", resp.StatusCode)
	}
}

func TestHandshakeRefusesControlPlaneAndUnwirableTools(t *testing.T) {
	for _, tools := range [][]string{{"mint_self-debug"}, {"release_x"}, {"has space"}, {"a.b"}, nil} {
		up := &fakeToolset{name: "x", tools: tools}
		srv := httptest.NewServer(up)
		p, _ := NewProxy(DataPlane{Origin: srv.URL}, "a-1", nil)
		front := httptest.NewServer(p)
		if _, err := Handshake(context.Background(), front.URL+"/mcp"); err == nil {
			t.Errorf("tools %q: handshake succeeded", tools)
		}
		front.Close()
		srv.Close()
	}
}

func TestServerConfigDataPlane(t *testing.T) {
	dp, err := ServerConfig{ServerURL: "https://caps.example.com:8443/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}.DataPlane()
	if err != nil || dp.Origin != "https://caps.example.com:8443" || dp.Headers["Authorization"] != "Bearer t" {
		t.Errorf("http: %+v, %v", dp, err)
	}
	dp, err = ServerConfig{Command: "grain", Args: []string{"capability", "self-debug", "-listen", "127.0.0.1:9730"},
		Env: map[string]string{DataPlaneTokenEnv: "tok"}}.DataPlane()
	if err != nil || dp.Origin != "http://127.0.0.1:9730" || dp.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("stdio: %+v, %v", dp, err)
	}
	if _, err := (ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}}).DataPlane(); err == nil {
		t.Error("a stdio server with no -listen has a data plane")
	}
	if _, err := (ServerConfig{Command: "grain", Args: []string{"-listen=10.0.0.1:9730"}}).DataPlane(); err == nil {
		t.Error("a non-loopback -listen was accepted")
	}
}
