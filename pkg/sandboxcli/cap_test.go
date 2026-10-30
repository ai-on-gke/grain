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

package sandboxcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

type capTestScopes map[string]model.NamespaceScope

func (s capTestScopes) SandboxNamespaceScope(_ context.Context, sandbox string) (model.NamespaceScope, bool, error) {
	scope, ok := s[sandbox]
	return scope, ok, nil
}

// fakeCapMCP is a capability server's attempt toolset as the sandbox
// proxy hands requests to it: initialize, then tools/call, answered with
// the arguments it was called with.
type fakeCapMCP struct {
	mu    sync.Mutex
	calls []string
	args  map[string]any
	auth  string
}

func (f *fakeCapMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &msg)
	f.mu.Lock()
	f.auth = r.Header.Get("Authorization")
	f.mu.Unlock()
	var result any
	switch msg.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "widgets"}}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		f.mu.Lock()
		f.calls = append(f.calls, p.Name)
		f.args = p.Arguments
		f.mu.Unlock()
		text, _ := json.Marshal(map[string]any{"pong": p.Arguments["host"]})
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}}
	default:
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
}

func newCapTestCLI(t *testing.T) (*sandboxCLI, *fakeCapMCP, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-1": "tok-1"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeCapMCP{}
	caps := gitproxy.NewCapRegistry()
	caps.Register(gitproxy.CapRunKey("run-1"), []gitproxy.CapServer{{
		Name: "widgets", Grant: "https://example.com/widgets#w",
		Tools: []gitproxy.CapTool{
			{Name: "ping", Description: "Ping a host.\nMore detail.", InputSchema: map[string]any{
				"type": "object", "required": []any{"host"},
				"properties": map[string]any{
					"host":    map[string]any{"type": "string", "description": "Host to ping"},
					"count":   map[string]any{"type": "integer"},
					"verbose": map[string]any{"type": "boolean"},
					"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"mode":    map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
				},
			}},
			{Name: "rotate_keys", ModelOnly: true},
		},
		Handler: server,
	}})
	proxy := &gitproxy.GitProxy{
		Tokens:          tokens,
		NamespaceScopes: capTestScopes{"sb-1": {TaskID: "7", RunID: "run-1", Attempt: 1}},
		CapServers:      caps,
	}
	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	c := &sandboxCLI{base: srv.URL, token: "tok-1", out: &out, errOut: &errOut,
		client: &http.Client{Timeout: 30 * time.Second}, env: func(string) string { return "" }}
	return c, server, &out, &errOut
}

func TestGrainCapListHelpAndCall(t *testing.T) {
	c, server, out, errOut := newCapTestCLI(t)

	if err := c.run([]string{"cap", "list"}); err != nil {
		t.Fatalf("grain cap list: %v (%s)", err, errOut)
	}
	if got := out.String(); !strings.Contains(got, "grain cap widgets ping") || !strings.Contains(got, "Ping a host.") ||
		strings.Contains(got, "More detail") || !strings.Contains(got, "model-only, not callable here: rotate_keys") {
		t.Errorf("grain cap list printed:\n%s", got)
	}

	out.Reset()
	if err := c.run([]string{"cap", "widgets", "ping", "--help"}); err != nil {
		t.Fatalf("grain cap widgets ping --help: %v", err)
	}
	for _, want := range []string{"--host <string>", "(required)", "--count <integer>", "--verbose ", "--tags <string>...", "one of fast|slow", "--json '<object>'"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--help lacks %q:\n%s", want, out)
		}
	}
	if len(server.calls) != 0 {
		t.Errorf("--help called the server: %v", server.calls)
	}

	out.Reset()
	if err := c.run([]string{"cap", "widgets", "ping", "--host", "example.com", "--count=3", "--verbose", "--tags", "a", "--tags", "b"}); err != nil {
		t.Fatalf("grain cap widgets ping: %v (%s)", err, errOut)
	}
	want := map[string]any{"host": "example.com", "count": float64(3), "verbose": true, "tags": []any{"a", "b"}}
	if !reflect.DeepEqual(server.args, want) {
		t.Errorf("server was called with %v, want %v", server.args, want)
	}
	if server.auth != "" {
		t.Errorf("the capability server saw Authorization %q; the proxy token is the proxy's alone", server.auth)
	}
	if !strings.Contains(out.String(), `"pong": "example.com"`) {
		t.Errorf("result printed as:\n%s", out)
	}

	if err := c.run([]string{"cap", "widgets", "ping", "--json", `{"host":"a.example","count":1}`, "--count", "2"}); err != nil {
		t.Fatalf("grain cap widgets ping --json: %v", err)
	}
	if want := map[string]any{"host": "a.example", "count": float64(2)}; !reflect.DeepEqual(server.args, want) {
		t.Errorf("--json with a flag over it called with %v, want %v", server.args, want)
	}

	calls := len(server.calls)
	for _, args := range [][]string{
		{"cap", "widgets", "ping"},                                // missing --host
		{"cap", "widgets", "ping", "--host", "h", "--count", "x"}, // not an integer
		{"cap", "widgets", "ping", "--host", "h", "--nope", "1"},  // unknown flag
		{"cap", "widgets", "rotate_keys"},                         // model-only
		{"cap", "widgets", "absent"},                              // never listed
	} {
		if err := c.run(args); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(server.calls) != calls {
		t.Errorf("a refused invocation reached the server: %v", server.calls[calls:])
	}

	// The proxy refuses a model-only tool even when the CLI is bypassed.
	req, _ := http.NewRequest(http.MethodPost, c.base+"/cap/widgets/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rotate_keys"}}`))
	req.SetBasicAuth("x-access-token", "tok-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(server.calls) != calls {
		t.Errorf("raw tools/call of a model-only tool: %d, reached=%v; want 403 and the server untouched", resp.StatusCode, len(server.calls) != calls)
	}
}

func TestCapArgumentsConvertsBySchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"ratio":   map[string]any{"type": "number"},
		"ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		"opts":    map[string]any{"type": "object"},
		"dry_run": map[string]any{"type": "boolean"},
	}}
	got, err := capArguments(schema, []string{"--ratio", "0.5", "--ids", "[1,2]", "--opts", `{"a":1}`, "--dry-run=false"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"ratio": 0.5, "ids": []any{float64(1), float64(2)}, "opts": map[string]any{"a": float64(1)}, "dry_run": false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	got, err = capArguments(schema, []string{"--ids", "3", "--ids", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{int64(3), int64(4)}; !reflect.DeepEqual(got["ids"], want) {
		t.Errorf("repeated --ids: %#v, want %#v", got["ids"], want)
	}
}
