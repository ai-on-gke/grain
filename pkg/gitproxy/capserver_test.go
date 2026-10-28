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

package gitproxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestCapServesACIJobBySandboxName: a CI job's sandbox has no task run,
// so its servers are found under its own name -- with the same model-only
// refusal -- and a sandbox with neither a run nor a registration is
// refused.
func TestCapServesACIJobBySandboxName(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"ci-job-1": "tok-ci", "ci-job-2": "tok-other"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	caps := gitproxy.NewCapRegistry()
	unregister := caps.Register(gitproxy.CapSandboxKey("ci-job-1"), []gitproxy.CapServer{{
		Name:  "widgets",
		Tools: []gitproxy.CapTool{{Name: "ping"}, {Name: "rotate", ModelOnly: true}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached++
			w.WriteHeader(http.StatusOK)
		}),
	}})
	h := gitproxy.NewHandler(&gitproxy.GitProxy{
		Tokens:          tokens,
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{}},
		CapServers:      caps,
	})
	call := func(token, tool string) int {
		req := httptest.NewRequest(http.MethodPost, "/cap/widgets/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`"}}`))
		req.Header.Set("Authorization", basicAuthHeader(token))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call("tok-ci", "ping"); code != http.StatusOK || reached != 1 {
		t.Errorf("CI job calling its sandbox tool: %d, reached %d times", code, reached)
	}
	if code := call("tok-ci", "rotate"); code != http.StatusForbidden || reached != 1 {
		t.Errorf("CI job calling a model-only tool: %d, reached %d times; want 403 and untouched", code, reached)
	}
	if code := call("tok-other", "ping"); code != http.StatusForbidden || reached != 1 {
		t.Errorf("another sandbox with no run calling the job's tool: %d; want 403", code)
	}
	unregister()
	if code := call("tok-ci", "ping"); code != http.StatusForbidden || reached != 1 {
		t.Errorf("the job's sandbox after its pass closed: %d; want 403", code)
	}
}

func TestCapRegistryUnregisterLeavesAReplacementAlone(t *testing.T) {
	caps := gitproxy.NewCapRegistry()
	first := caps.Register("k", []gitproxy.CapServer{{Name: "a"}})
	caps.Register("k", []gitproxy.CapServer{{Name: "b"}})
	first()
	if got, ok := caps.Servers("k"); !ok || len(got) != 1 || got[0].Name != "b" {
		t.Errorf("after the replaced registration was removed: %+v, %v", got, ok)
	}
}

// TestCapRefusesBodiesParsersDisagreeOn: encoding/json matches keys
// without regard to case and keeps the last duplicate, so a body whose
// tools/call reads as a sandbox tool to the proxy's check could name a
// model-only one to the server's own parser. Every such body is refused
// before the server sees it.
func TestCapRefusesBodiesParsersDisagreeOn(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"ci-job-1": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	caps := gitproxy.NewCapRegistry()
	caps.Register(gitproxy.CapSandboxKey("ci-job-1"), []gitproxy.CapServer{{
		Name:  "widgets",
		Tools: []gitproxy.CapTool{{Name: "ping"}, {Name: "secret", ModelOnly: true}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached++
			w.WriteHeader(http.StatusOK)
		}),
	}})
	h := gitproxy.NewHandler(&gitproxy.GitProxy{
		Tokens:          tokens,
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{}},
		CapServers:      caps,
	})
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret"},"Params":{"name":"ping"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret"},"paramſ":{"name":"ping"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"ping","params":{"name":"secret"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret","NAME":"ping"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret","name":"ping"}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret","Name":"ping"}}]`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/cap/widgets/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code < 400 || reached != 0 {
			t.Errorf("POST %s: %d, server reached %d times; want refused and untouched", body, rec.Code, reached)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/cap/widgets/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{"a":{"b":1},"c":[{"name":"x"},{"name":"y"}]}}}`))
	req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || reached != 1 {
		t.Errorf("an unambiguous call with nested arguments: %d, reached %d times; want it forwarded", rec.Code, reached)
	}
}
