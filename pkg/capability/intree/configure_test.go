package intree

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// attachConfigured is attach with config in the entry, its secrets
// resolved from secrets beside the control-plane token.
// testIdentity is the grain every test attaches as: what the daemon
// fills a server's identity fields with (protocol.WithIdentity).
var testIdentity = protocol.Identity{DeploymentID: "049f1e09", InstanceID: "049f1e09c0ffee0123456789abcdef01"}

func attachConfigured(t *testing.T, s *Server, config map[string]any, secrets map[string]string) (*protocol.Server, error) {
	t.Helper()
	t.Cleanup(func() { s.Close(context.Background()) })
	s.CapabilityServer.Token = controlToken
	ts := httptest.NewServer(s.CapabilityServer)
	t.Cleanup(ts.Close)
	ps, err := protocol.Attach(context.Background(), "https://github.com/bwsalmon/grain/capabilities/"+s.Name,
		protocol.ServerConfig{ServerURL: ts.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:control"}, Config: config},
		func(name string) (string, bool) {
			if name == "control" {
				return controlToken, true
			}
			v, ok := secrets[name]
			return v, ok
		}, protocol.DialOptions{Identity: testIdentity})
	if ps != nil {
		t.Cleanup(func() { ps.Close() })
	}
	return ps, err
}

func TestGitIsConfiguredThroughConfigure(t *testing.T) {
	up := &upstreamLog{}
	ts := httptest.NewServer(up.handler())
	defer ts.Close()
	// Only the listener from flags; everything else from configure.
	s, err := newConfigurableGit(GitConfig{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, map[string]any{
		"upstream":   ts.URL,
		"repos":      []any{"bwsalmon/grain"},
		"pushBranch": "^task/[a-z0-9-]+$",
		"token":      "$secret:git-token",
	}, map[string]string{"git-token": "configured-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got := protocol.SecretFields(ps.ConfigSchema); !reflect.DeepEqual(got, []string{"token"}) {
		t.Errorf("secret fields = %v, want [token]", got)
	}
	if _, err := protocol.ValidateArgs(ps.Methods["git-repo"].ArgsSchema, map[string]any{"repo": "other/one"}); err == nil {
		t.Error("a repository configure did not name was accepted")
	}
	if resp := mint(t, ps, "git-repo", "e1-1-0", map[string]any{"repo": "bwsalmon/grain", "mode": "write", "branch": "grain/topic"}); resp.Status != protocol.StatusRefused {
		t.Errorf("a branch outside the configured pushBranch: %+v, want refused", resp)
	}
	resp := mint(t, ps, "git-repo", "e1-2-0", map[string]any{"repo": "bwsalmon/grain"})
	creds := strings.TrimPrefix(strings.TrimSpace(placement(t, resp, "/home/debian/.git-repo/e1-2-0/credentials").Content), "http://x-access-token:")
	req, _ := http.NewRequest(http.MethodGet, "http://"+s.addr+"/bwsalmon/grain.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("x-access-token", creds[:strings.Index(creds, "@")])
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if got := up.all(); r.StatusCode != http.StatusOK || len(got) != 1 || !strings.Contains(got[0], "auth=configured-secret") {
		t.Errorf("proxied fetch: %d, upstream saw %v; want the configured token", r.StatusCode, got)
	}
	if _, err := ps.Release(context.Background(), "git-repo", "e1-2-0"); err != nil {
		t.Fatal(err)
	}
}

func TestGitFallsBackToItsFlagsAndEnvironment(t *testing.T) {
	// An entry from before configure: flags and GIT_REPO_TOKEN, no config.
	ps, err := attachConfigured(t, newTestGit(t, "http://127.0.0.1:1"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp := mint(t, ps, "git-repo", "e1-1-0", map[string]any{"repo": "other/one"}); resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if _, err := ps.Release(context.Background(), "git-repo", "e1-1-0"); err != nil {
		t.Fatal(err)
	}

	// A config field overrides its flag; the token still falls back.
	ps2, err := attachConfigured(t, newTestGit(t, "http://127.0.0.1:1"), map[string]any{"repos": []any{"corp/*"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ValidateArgs(ps2.Methods["git-repo"].ArgsSchema, map[string]any{"repo": "corp/svc"}); err != nil {
		t.Errorf("the configured repository was refused: %v", err)
	}
	if _, err := protocol.ValidateArgs(ps2.Methods["git-repo"].ArgsSchema, map[string]any{"repo": "other/one"}); err == nil {
		t.Error("a -repo the configuration replaced was still accepted")
	}
}

func TestModelProxyIsConfiguredThroughConfigure(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-goog-api-key")+r.URL.Query().Get("key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	// The vendor from flags, with no MODEL_PROXY_GEMINI_KEY.
	s, err := newConfigurableModelProxy(ModelProxyConfig{Vendors: []ModelProxyVendor{
		{Vendor: modelproxy.Gemini, Listen: "127.0.0.1:0", Upstream: ts.URL},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, map[string]any{"geminiKey": "$secret:gemini"}, map[string]string{"gemini": "configured-key"})
	if err != nil {
		t.Fatal(err)
	}
	if got := protocol.SecretFields(ps.ConfigSchema); !reflect.DeepEqual(got, []string{"geminiKey"}) {
		t.Errorf("secret fields = %v, want [geminiKey]: one per vendor served", got)
	}
	resp := mint(t, ps, "model-proxy", "e1-1-0", map[string]any{"vendor": "gemini"})
	url := strings.TrimSpace(placement(t, resp, "/home/debian/.gemini-proxy-url").Content)
	token := strings.TrimSpace(placement(t, resp, "/home/debian/.gemini-proxy-token").Content)
	req, _ := http.NewRequest(http.MethodPost, url+"/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{}`))
	req.Header.Set("x-goog-api-key", token)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if r.StatusCode != http.StatusOK || len(keys) != 1 || keys[0] != "configured-key" {
		t.Errorf("proxied request: %d, upstream saw keys %v; want the configured key", r.StatusCode, keys)
	}
}

func TestModelProxyFallsBackToItsEnvironment(t *testing.T) {
	// MODEL_PROXY_GEMINI_KEY and no config: an entry from before configure.
	ps, err := attachConfigured(t, newTestModelProxy(t, "http://127.0.0.1:1"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Methods["model-proxy"] == nil {
		t.Fatalf("methods = %v", ps.Methods)
	}
	// A vendor with neither fails the attach naming its variable.
	s, err := newConfigurableModelProxy(ModelProxyConfig{Vendors: []ModelProxyVendor{
		{Vendor: modelproxy.Gemini, Listen: "127.0.0.1:0", Credential: "k"},
		{Vendor: modelproxy.Claude, Listen: "127.0.0.1:0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var ce *protocol.ConfigureError
	if _, err := attachConfigured(t, s, map[string]any{}, nil); !errors.As(err, &ce) || !strings.Contains(err.Error(), ModelProxyKeyEnv("claude")) {
		t.Errorf("claude with no key: attach = %v, want a configure error naming %s", err, ModelProxyKeyEnv("claude"))
	}
	// No -vendor flags at all (as EnsureBuiltins seeds the built-in entry):
	// the server still starts and answers initialize, then fails configure
	// rather than closing stdout before initialize with EOF.
	sEmpty, err := newConfigurableModelProxy(ModelProxyConfig{})
	if err != nil {
		t.Fatalf("newConfigurableModelProxy with no vendors: %v", err)
	}
	ce = nil
	if _, err := attachConfigured(t, sEmpty, nil, nil); !errors.As(err, &ce) || !strings.Contains(err.Error(), "at least one -vendor is required") {
		t.Errorf("model-proxy with no vendors: attach = %v, want a configure error naming -vendor", err)
	}
}
