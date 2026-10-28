package modelexec

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// TestCapabilityMCPCarriedOverTheExecutorStream is §5.2.4's last
// paragraph end to end: a CLI on the executor is handed a URL on the
// executor's own loopback, and what it sends there reaches the capability
// server through grain's proxy under §5.2.2's rules, applied on grain's
// side.
func TestCapabilityMCPCarriedOverTheExecutorStream(t *testing.T) {
	t.Parallel()
	const token = "secret-test-token"
	const attemptID = "e1010c47-175-2-0"

	type seen struct {
		method, path, auth, extra, body string
	}
	var mu sync.Mutex
	var got []seen
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Hop"), string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "s-1")
		w.Header().Set("X-Upstream-Only", "1")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer upstream.Close()
	proxy, err := protocol.NewProxy(protocol.DataPlane{Origin: upstream.URL, Headers: map[string]string{"Authorization": "Bearer server-credential"}}, attemptID, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}

	srv, err := NewServer(Config{Token: token, Wait: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()
	d := &Daemon{Addr: ln.Addr().String(), Token: token, Name: "worker-capability-mcp", Binaries: map[string]string{"agy": "/bin/sh"}}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host: %v", err)
	}
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:1/grain-side/mcp", Tools: []string{"read_log"}, Handler: proxy}}
	carried, release, err := agent.CarryCapabilityServers(host, servers)
	if err != nil {
		t.Fatalf("a remote executor that carries capability servers was refused: %v", err)
	}
	if len(carried) != 1 || carried[0].Name != "self-debug" || carried[0].Tools[0] != "read_log" {
		t.Fatalf("carried = %+v, want the same server under the same name", carried)
	}
	url := carried[0].URL
	if !strings.HasPrefix(url, host.conn.capabilityMCPURL+"/") || url == servers[0].URL {
		t.Fatalf("carried URL = %q, want one on the executor's listener %s", url, host.conn.capabilityMCPURL)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, target, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer from-the-cli")
		req.Header.Set("Connection", "X-Hop")
		req.Header.Set("X-Hop", "hop-by-hop")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, target, err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	resp := do(http.MethodPost, url, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"result"`) {
		t.Fatalf("POST over the executor = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Mcp-Session-Id") != "s-1" || resp.Header.Get("X-Upstream-Only") != "" {
		t.Errorf("response headers = %v, want only the proxied ones", resp.Header)
	}
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(got))
	}
	want := seen{http.MethodPost, protocol.EndpointPath(attemptID), "Bearer server-credential", "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`}
	if got[0] != want {
		t.Errorf("upstream saw %+v, want %+v", got[0], want)
	}
	mu.Unlock()

	if resp := do(http.MethodPut, url, ""); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT over the executor = %d, want 405", resp.StatusCode)
	}
	if resp := do(http.MethodPost, host.conn.capabilityMCPURL+"/guessed/mcp", "{}"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a path grain never issued = %d, want 404", resp.StatusCode)
	}

	release()
	if resp := do(http.MethodPost, url, "{}"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a released server's path = %d, want 404", resp.StatusCode)
	}
	mu.Lock()
	if len(got) != 1 {
		t.Errorf("upstream saw %d requests, want only the one before release", len(got))
	}
	mu.Unlock()
}

func TestCapabilityMCPRefusedByAnExecutorThatCannotCarryIt(t *testing.T) {
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:1/x/mcp", Tools: []string{"t"}, Handler: http.NotFoundHandler()}}
	old := &RemoteHost{conn: &hostConn{name: "old-executor"}}
	if _, _, err := agent.CarryCapabilityServers(old, servers); err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("an executor that announced no capability listener: err = %v, want a refusal saying to upgrade it", err)
	}
	lying := &RemoteHost{conn: &hostConn{name: "lying-executor", capabilityMCPURL: "http://example.com:80"}}
	if _, _, err := agent.CarryCapabilityServers(lying, servers); err == nil {
		t.Error("an executor announcing a non-loopback listener was handed capability servers")
	}
	ok := &RemoteHost{conn: &hostConn{name: "executor", capabilityMCPURL: "http://127.0.0.1:4000"}}
	unproxied := []agent.CapabilityMCPServer{servers[0], {Name: "other", URL: "http://127.0.0.1:1/y/mcp"}}
	if _, _, err := agent.CarryCapabilityServers(ok, unproxied); err == nil {
		t.Error("a server with no proxy to carry was handed on")
	}
	ok.conn.capabilityRoutes.mu.Lock()
	n := len(ok.conn.capabilityRoutes.routes)
	ok.conn.capabilityRoutes.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused carry left %d routes behind", n)
	}
}

func TestLoopbackHTTPOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://127.0.0.1:4000":     true,
		"http://[::1]:4000":         true,
		"http://127.0.0.1":          false,
		"https://127.0.0.1:4000":    false,
		"http://10.0.0.1:4000":      false,
		"http://localhost:4000":     false,
		"http://127.0.0.1:4000/x":   false,
		"http://u:p@127.0.0.1:4000": false,
		"http://127.0.0.1:4000?q=1": false,
		"":                          false,
	} {
		if got := loopbackHTTPOrigin(origin); got != want {
			t.Errorf("loopbackHTTPOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}
