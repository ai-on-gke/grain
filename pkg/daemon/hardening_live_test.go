package daemon

// The security hardening of 2026-10-11 (docs/test-plans/2026-10-11.md
// §11), held at the daemon's own wiring rather than at the packages
// underneath it: the model proxies startModelProxy publishes through the
// Direct fabric -- the same listener, http.Server and credential ladder a
// sandbox reaches -- with only the vendor's upstream swapped for a local
// stub, and the sandboxnet listener's header timeout measured against a
// client dripping its request one byte at a time.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// upstreamStub stands in for a model vendor's API and records every
// request that reaches it.
type upstreamStub struct {
	mu   sync.Mutex
	seen []string // "METHOD path", one per request
	auth []string // the credential each arrived with
}

func (s *upstreamStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.auth = append(s.auth, r.Header.Get("x-goog-api-key")+r.Header.Get("Authorization")+r.URL.Query().Get("key"))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{}`)
}

func (s *upstreamStub) take() (seen, auth []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, auth = s.seen, s.auth
	s.seen, s.auth = nil, nil
	return seen, auth
}

// serveStubbedModelProxy starts vendor's proxy the way StartModelProxies
// does, on a Direct fabric, with its upstream pointed at a stub and its
// credential held by a team connection of vendor's framework, and mints
// a run token on it for a run of that team.
func serveStubbedModelProxy(t *testing.T, vendor modelproxy.Vendor, cfg Config, credential string) (url, token string, stub *upstreamStub) {
	t.Helper()
	stub = &upstreamStub{}
	upstream := httptest.NewServer(stub)
	t.Cleanup(upstream.Close)
	vendor.Upstream = upstream.URL

	const team, connectionID = "hardening", "hardening-connection"
	secretStore := secrets.New(t.TempDir())
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret(connectionID), secrets.AgentCredentialKey, []byte(credential)); err != nil {
		t.Fatal(err)
	}
	cfg.AgentBackends = append(cfg.AgentBackends, model.AgentBackend{ID: connectionID, Framework: vendor.Framework, Team: team})

	endpoint := modelproxy.NewEndpoint()
	stop, err := startModelProxy(vendor, endpoint, cfg, NewDirect(""), secretStore, nil)
	if err != nil {
		t.Fatalf("starting the %s proxy: %v", vendor.Name, err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	connection, err := endpoint.ConnectionFor(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if connection.ID != connectionID {
		t.Fatalf("a run of team %s resolved to connection %q, want %q", team, connection.ID, connectionID)
	}
	_, token, err = endpoint.Mint("run-3147", connection)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint.URL(), token, stub
}

type proxyCall struct {
	method, path string
	forwarded    bool
}

func checkProxyCalls(t *testing.T, base string, stub *upstreamStub, credential string, authorize func(*http.Request), calls []proxyCall) {
	t.Helper()
	for _, c := range calls {
		req, err := http.NewRequest(c.method, base+c.path, strings.NewReader(`{"contents":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		authorize(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen, auth := stub.take()
		path, _, _ := strings.Cut(c.path, "?")
		switch {
		case c.forwarded && (resp.StatusCode != http.StatusOK || len(seen) != 1 || seen[0] != c.method+" "+path):
			t.Errorf("%s %s: status %d, upstream saw %q; want it forwarded as is\n%s", c.method, c.path, resp.StatusCode, seen, body)
		case c.forwarded && auth[0] != credential && auth[0] != "Bearer "+credential:
			t.Errorf("%s %s reached upstream with credential %q, want the deployment's own", c.method, c.path, auth[0])
		case !c.forwarded && (resp.StatusCode != http.StatusForbidden || len(seen) != 0):
			t.Errorf("%s %s: status %d, upstream saw %q; want 403 and nothing forwarded\n%s", c.method, c.path, resp.StatusCode, seen, body)
		}
	}
}

func TestDaemonGeminiProxyForwardsOnlySynchronousModelCalls(t *testing.T) {
	const credential = "deployment-gemini-key"
	base, token, stub := serveStubbedModelProxy(t, modelproxy.Gemini, Config{}, credential)
	authorize := func(r *http.Request) { r.Header.Set("x-goog-api-key", token) }

	var calls []proxyCall
	for _, version := range []string{"/v1beta", "/v1", "/v1alpha"} {
		calls = append(calls,
			proxyCall{"GET", version + "/models", true},
			proxyCall{"GET", version + "/models?pageSize=5", true},
			proxyCall{"GET", version + "/models/gemini-2.5-flash", true},
			proxyCall{"POST", version + "/models/gemini-2.5-flash:generateContent", true},
			proxyCall{"POST", version + "/models/gemini-2.5-flash:streamGenerateContent?alt=sse", true},
			proxyCall{"POST", version + "/models/gemini-2.5-flash:countTokens", true},
			proxyCall{"POST", version + "/models/text-embedding-004:embedContent", true},
			proxyCall{"POST", version + "/models/text-embedding-004:batchEmbedContents", true},

			// Work that outlives the request, and the listing of it.
			proxyCall{"POST", version + "/models/gemini-2.5-flash:batchGenerateContent", false},
			proxyCall{"POST", version + "/models/text-embedding-004:asyncBatchEmbedContent", false},
			proxyCall{"POST", version + "/models/veo-3.0-generate-preview:predictLongRunning", false},
			proxyCall{"GET", version + "/models/veo-3.0-generate-preview/operations", false},
			proxyCall{"GET", version + "/models/veo-3.0-generate-preview/operations/op-1", false},
			proxyCall{"GET", version + "/models/gemini-2.5-flash:generateContent", false},
			proxyCall{"POST", version + "/models/gemini-2.5-flash", false},
			proxyCall{"POST", version + "/models/:generateContent", false},
			proxyCall{"POST", version + "/models/a:b:generateContent", false},
			proxyCall{"POST", version + "/batches", false},
			proxyCall{"GET", version + "/operations", false},
			proxyCall{"POST", version + "/files", false},
		)
	}
	checkProxyCalls(t, base, stub, credential, authorize, calls)

	// The key may come as ?key= too, and is swapped there as well.
	checkProxyCalls(t, base, stub, credential, func(*http.Request) {}, []proxyCall{
		{"POST", "/v1beta/models/gemini-2.5-flash:generateContent?key=" + token, true},
		{"POST", "/v1beta/models/gemini-2.5-flash:batchGenerateContent?key=" + token, false},
	})
}

func TestDaemonOpenAIProxyRefusesStoredInputItems(t *testing.T) {
	const credential = "deployment-openai-key"
	base, token, stub := serveStubbedModelProxy(t, modelproxy.OpenAI, Config{}, credential)
	authorize := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	checkProxyCalls(t, base, stub, credential, authorize, []proxyCall{
		{"POST", "/v1/responses", true},
		{"GET", "/v1/responses/resp_123", true},
		{"POST", "/v1/responses/resp_123/cancel", true},
		{"POST", "/v1/responses/compact", true},
		{"POST", "/v1/responses/input_tokens", true},

		{"GET", "/v1/responses/resp_123/input_items", false},
		{"GET", "/v1/responses/resp_123/input_items?limit=100", false},
		{"DELETE", "/v1/responses/resp_123", false},
		{"POST", "/v1/responses/resp_123", false},
		{"GET", "/v1/responses/resp_123/cancel", false},
	})
}

// A client that sends its request headers one byte a second is cut off
// once ReadHeaderTimeout runs out, while an ordinary client on the same
// listener is answered throughout.
func TestDaemonSandboxnetCutsOffAClientDrippingHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 10s header timeout")
	}
	fabric := NewDirect("")
	endpoint, stop, err := fabric.Serve("slowloris probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })

	conn, err := net.Dial("tcp", endpoint.Bound)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	started := time.Now()
	closed := make(chan time.Duration, 1)
	go func() {
		// The server answers a timed-out header read with a 408 and
		// closes; either way, a read returning is the cut.
		_, _ = bufio.NewReader(conn).ReadByte()
		_, _ = io.Copy(io.Discard, conn)
		closed <- time.Since(started)
	}()
	request := "GET / HTTP/1.1\r\nHost: probe\r\nX-Slow: " + strings.Repeat("a", 60)
	go func() {
		for i := 0; i < len(request); i++ {
			if _, err := conn.Write([]byte{request[i]}); err != nil {
				return
			}
			time.Sleep(time.Second)
		}
	}()

	// While it drips, the listener still answers everybody else.
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Second)
		resp, err := http.Get(endpoint.URL)
		if err != nil {
			t.Fatalf("an ordinary request while a client drips headers: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("an ordinary request while a client drips headers: %d %q", resp.StatusCode, body)
		}
	}

	select {
	case after := <-closed:
		if after < 9*time.Second || after > 14*time.Second {
			t.Errorf("the dripping connection was closed after %v, want about 10s", after)
		}
		t.Logf("dripping connection closed after %v", after.Round(100*time.Millisecond))
	case <-time.After(30 * time.Second):
		t.Fatal(fmt.Sprintf("the dripping connection was still open after %v", time.Since(started)))
	}
}
