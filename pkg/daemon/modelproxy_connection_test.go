package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	modelproxycap "github.com/bwsalmon/grain/pkg/capability/modelproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// claudeVendorUpstream is a stand-in for api.anthropic.com that records
// the key each request reached it with, and answers whatever answer
// says for that key.
type claudeVendorUpstream struct {
	mu     sync.Mutex
	keys   []string
	answer func(key string, w http.ResponseWriter)
}

func (u *claudeVendorUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.keys...)
}

// proxyOverTeams serves vendor's proxy the way startModelProxy wires it,
// over cfg's connections and secretStore's keys, in front of a recording
// upstream, and with pause as the usage-limit pause it shares.
func proxyOverTeams(t *testing.T, cfg Config, secretStore *secrets.Store, pause *orchestrator.Pause) (*modelproxy.Endpoint, *httptest.Server, *claudeVendorUpstream) {
	t.Helper()
	up := &claudeVendorUpstream{}
	vendorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		up.mu.Lock()
		up.keys = append(up.keys, key)
		answer := up.answer
		up.mu.Unlock()
		if answer != nil {
			answer(key, w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(vendorServer.Close)
	endpoint := modelproxy.NewEndpoint()
	WireModelProxy(modelproxy.Claude, endpoint, cfg, nil, secretStore, pause)
	proxy := httptest.NewServer(&modelproxy.Proxy{Vendor: modelproxy.Claude, Endpoint: endpoint, Upstream: vendorServer.URL})
	t.Cleanup(proxy.Close)
	return endpoint, proxy, up
}

// twoTeams is a deployment with two teams, each with a Claude connection
// of its own holding its own API key -- and a deployment-wide Claude
// secret no proxied request may ever be sent with.
func twoTeams(t *testing.T) (Config, *secrets.Store) {
	t.Helper()
	secretStore := secrets.New(t.TempDir())
	cfg := Config{AgentBackends: []model.AgentBackend{
		{ID: "alpha-claude", Framework: model.AgentFrameworkClaude, Team: "alpha"},
		{ID: "beta-claude", Framework: model.AgentFrameworkClaude, Team: "beta"},
	}}
	for id, key := range map[string]string{"alpha-claude": "sk-ant-api-alpha", "beta-claude": "sk-ant-api-beta"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := secretStore.Set(secrets.ClaudeOAuthTokenSecret, secrets.AgentCredentialKey, []byte("sk-ant-api-deployment-wide")); err != nil {
		t.Fatal(err)
	}
	return cfg, secretStore
}

// mintFor mints a token the way the claude-proxy capability does, for a
// run of team whose own agent spends own.
func mintFor(t *testing.T, endpoint *modelproxy.Endpoint, team, own string) string {
	t.Helper()
	connection, err := endpoint.ConnectionFor(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := endpoint.Mint("run-"+team, connection)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func callClaude(t *testing.T, proxy *httptest.Server, token string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", token)
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// Model connections are the deployment's whichever team saved them
// (docs/single-tenant-reset.md, stage 1): a run's proxied request carries
// its own connection's key, and a run that names none the deployment's
// first -- whichever team it is for, and never the deployment-wide
// secret.
func TestModelProxySpendsTheRunsConnectionWhicheverTeam(t *testing.T) {
	t.Parallel()
	cfg, secretStore := twoTeams(t)
	endpoint, proxy, up := proxyOverTeams(t, cfg, secretStore, &orchestrator.Pause{})

	for _, run := range []struct{ team, own string }{{"alpha", "beta-claude"}, {"beta", ""}} {
		if resp, body := callClaude(t, proxy, mintFor(t, endpoint, run.team, run.own)); resp.StatusCode != http.StatusOK {
			t.Fatalf("team %s: %d %s", run.team, resp.StatusCode, body)
		}
	}
	if got := up.seen(); len(got) != 2 || got[0] != "sk-ant-api-beta" || got[1] != "sk-ant-api-alpha" {
		t.Fatalf("the vendor saw keys %q, want beta-claude's for the run on it, then the first connection's", got)
	}
}

// A deployment with no connection for the vendor is refused, and nothing
// reaches the vendor -- the deployment-wide secret is not a fallback.
func TestModelProxyRefusesATeamWithNoConnectionForTheVendor(t *testing.T) {
	t.Parallel()
	cfg, secretStore := twoTeams(t)
	cfg.AgentBackends = []model.AgentBackend{{ID: "gamma-codex", Framework: model.AgentFrameworkCodex, Team: "gamma"}}
	endpoint, proxy, up := proxyOverTeams(t, cfg, secretStore, &orchestrator.Pause{})

	resp, body := callClaude(t, proxy, mintFor(t, endpoint, "gamma", "gamma-codex"))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "this deployment has no Claude connection") {
		t.Fatalf("got %d %s, want a 403 saying this deployment has no Claude connection", resp.StatusCode, body)
	}
	if got := up.seen(); len(got) != 0 {
		t.Fatalf("the vendor saw %q from a team with no connection", got)
	}
}

// A connection borrowing another's key (CredentialBackendID) resolves to
// the source, whether the run's own agent named the borrower or the
// proxy chose it for the team -- and the request carries the source's
// key.
func TestModelProxyResolvesABorrowedCredentialToItsSource(t *testing.T) {
	t.Parallel()
	cfg, secretStore := twoTeams(t)
	cfg.AgentBackends = []model.AgentBackend{
		{ID: "alpha-source", Framework: model.AgentFrameworkClaude, Team: "alpha", Disabled: true},
		{ID: "alpha-borrower", Framework: model.AgentFrameworkClaude, Team: "alpha", CredentialBackendID: "alpha-source"},
	}
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("alpha-source"), secrets.AgentCredentialKey, []byte("sk-ant-api-source")); err != nil {
		t.Fatal(err)
	}
	endpoint, proxy, up := proxyOverTeams(t, cfg, secretStore, &orchestrator.Pause{})

	for _, own := range []string{"alpha-source", ""} {
		connection, err := endpoint.ConnectionFor(t.Context(), own)
		if err != nil || connection.ID != "alpha-source" {
			t.Fatalf("own %q: ConnectionFor = %+v, %v, want the source alpha-source", own, connection, err)
		}
	}
	if resp, body := callClaude(t, proxy, mintFor(t, endpoint, "alpha", "")); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := up.seen(); len(got) != 1 || got[0] != "sk-ant-api-source" {
		t.Fatalf("the vendor saw %q, want the source's key", got)
	}
}

// The run's own connection wins over the team's first one: a run spends
// the same key through the proxy as its own agent does.
func TestModelProxyPrefersTheRunsOwnConnection(t *testing.T) {
	t.Parallel()
	backends := []model.AgentBackend{
		{ID: "first", Framework: model.AgentFrameworkClaude, Team: "alpha"},
		{ID: "second", Framework: model.AgentFrameworkClaude, Team: "alpha"},
		{ID: "theirs", Framework: model.AgentFrameworkClaude, Team: "beta"},
	}
	if got := modelProxyConnection(backends, modelproxy.Claude, "second", nil, time.Now()); got.ID != "second" {
		t.Errorf("a run on second was given %q", got.ID)
	}
	// A connection another team saved is the deployment's too.
	if got := modelProxyConnection(backends, modelproxy.Claude, "theirs", nil, time.Now()); got.ID != "theirs" {
		t.Errorf("a run on a connection another team saved was given %q, want that connection", got.ID)
	}
	if got := modelProxyConnection(backends, modelproxy.OpenAI, "", nil, time.Now()); got.ID != "" {
		t.Errorf("a team with no OpenAI connection was given %q", got.ID)
	}
}

// A connection that authenticates through Vertex or a subscription login
// has no key the proxy can present, and is refused saying so rather than
// forwarded with another.
func TestModelProxyRefusesAConnectionWithNoKeyToPresent(t *testing.T) {
	t.Parallel()
	backends := []model.AgentBackend{
		{ID: "vertex", Framework: model.AgentFrameworkClaude, Team: "alpha", AuthMode: model.ClaudeAuthVertex},
		{ID: "chatgpt", Framework: model.AgentFrameworkCodex, Team: "alpha", AuthMode: model.CodexAuthSubscription},
	}
	secretStore := secrets.New(t.TempDir())
	for _, tc := range []struct {
		vendor     modelproxy.Vendor
		connection string
		want       string
	}{
		{modelproxy.Claude, "vertex", "Vertex AI"},
		{modelproxy.OpenAI, "chatgpt", "subscription login"},
		{modelproxy.Claude, "gone", "no longer one of this deployment's connections"},
	} {
		_, err := modelProxyCredential(context.Background(), backends, tc.vendor, modelproxy.Connection{ID: tc.connection}, secretStore)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want a refusal mentioning %q", tc.connection, err, tc.want)
		}
	}
}

// A usage limit the vendor answers through the proxy pauses that
// connection -- in the same Pause agent runs are held by -- and only it:
// the other team's connection keeps forwarding, and the paused one
// refuses until the pause lifts.
func TestAUsageLimitThroughTheProxyPausesOnlyThatConnection(t *testing.T) {
	t.Parallel()
	cfg, secretStore := twoTeams(t)
	pause := &orchestrator.Pause{}
	endpoint, proxy, up := proxyOverTeams(t, cfg, secretStore, pause)
	reset := time.Now().Add(2 * time.Hour).Unix()
	up.answer = func(key string, w http.ResponseWriter) {
		if key != "sk-ant-api-alpha" {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`)
	}
	alpha, beta := mintFor(t, endpoint, "alpha", ""), mintFor(t, endpoint, "beta", "beta-claude")

	if resp, _ := callClaude(t, proxy, alpha); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("alpha's limited request got %d, want the vendor's 429", resp.StatusCode)
	}
	now := time.Now()
	if !pause.ConnectionPaused("alpha-claude", "alpha", now) {
		t.Fatal("the usage limit did not pause alpha-claude")
	}
	if pause.ConnectionPaused("beta-claude", "beta", now) {
		t.Fatal("alpha's usage limit paused beta's connection")
	}
	pauses := pause.Pauses(now)
	if len(pauses) != 1 || pauses[0].Connection != "alpha-claude" || pauses[0].Team != "alpha" ||
		pauses[0].Framework != model.AgentFrameworkClaude || !pauses[0].Until.Equal(time.Unix(reset, 0).UTC()) {
		t.Fatalf("pauses = %+v, want one on alpha-claude until the vendor's reset", pauses)
	}

	seen := len(up.seen())
	if resp, body := callClaude(t, proxy, alpha); resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "paused") {
		t.Fatalf("alpha's next request got %d %s, want a 429 saying its connection is paused", resp.StatusCode, body)
	}
	if len(up.seen()) != seen {
		t.Fatal("a request on a paused connection reached the vendor")
	}
	if resp, body := callClaude(t, proxy, beta); resp.StatusCode != http.StatusOK {
		t.Fatalf("beta's request got %d %s, want it forwarded", resp.StatusCode, body)
	}

	pause.Lift()
	up.answer = nil
	if resp, body := callClaude(t, proxy, alpha); resp.StatusCode != http.StatusOK {
		t.Fatalf("alpha's request after the pause lifted got %d %s", resp.StatusCode, body)
	}
}

// vertexServiceAccount is a service account key for project whose token
// endpoint is tokens -- a stand-in for oauth2.googleapis.com answering
// access.
func vertexServiceAccount(t *testing.T, project string, tokens *httptest.Server) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sa, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     project,
		"private_key_id": "k1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "proxy@" + project + ".iam.gserviceaccount.com",
		"token_uri":      tokens.URL,
	})
	return string(sa)
}

// A team whose Claude connection authenticates through Vertex AI is
// served by translating its requests: they reach Vertex under the
// project of the connection's own service account, with an access token
// minted from it -- once, and reused -- and never with any Anthropic key.
// A Vertex connection with no key of its own is refused, not forwarded
// on this host's ambient login or the deployment-wide secret.
func TestModelProxyForwardsAVertexConnectionToVertexAI(t *testing.T) {
	for _, name := range []string{"CLAUDE_CODE_USE_VERTEX", "CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"} {
		t.Setenv(name, "")
	}
	var mints int
	var mintsMu sync.Mutex
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mintsMu.Lock()
		mints++
		mintsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"ya29.alpha","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokens.Close)
	var paths, auths []string
	var seenMu sync.Mutex
	vertex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		paths, auths = append(paths, r.URL.Path), append(auths, r.Header.Get("Authorization")+r.Header.Get("x-api-key"))
		seenMu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(vertex.Close)

	cfg, secretStore := twoTeams(t)
	cfg.AgentBackends = []model.AgentBackend{
		{ID: "alpha-vertex", Framework: model.AgentFrameworkClaude, Team: "alpha", AuthMode: model.ClaudeAuthVertex},
		{ID: "beta-vertex", Framework: model.AgentFrameworkClaude, Team: "beta", AuthMode: model.ClaudeAuthVertex},
	}
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("alpha-vertex"), secrets.AgentCredentialKey,
		[]byte(vertexServiceAccount(t, "alpha-proj", tokens))); err != nil {
		t.Fatal(err)
	}
	endpoint := modelproxy.NewEndpoint()
	WireModelProxy(modelproxy.Claude, endpoint, cfg, nil, secretStore, &orchestrator.Pause{})
	proxy := httptest.NewServer(&modelproxy.Proxy{Vendor: modelproxy.Claude, Endpoint: endpoint, Upstream: vertex.URL, VertexUpstream: vertex.URL})
	t.Cleanup(proxy.Close)

	alpha := mintFor(t, endpoint, "alpha", "")
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
		req.Header.Set("x-api-key", alpha)
		resp, err := proxy.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("alpha: %d %s", resp.StatusCode, body)
		}
	}
	want := "/v1/projects/alpha-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:rawPredict"
	if len(paths) != 2 || paths[0] != want || auths[0] != "Bearer ya29.alpha" || auths[1] != "Bearer ya29.alpha" {
		t.Fatalf("Vertex saw %q with %q, want %s with alpha's access token", paths, auths, want)
	}
	if mints != 1 {
		t.Errorf("minted %d access tokens for two requests, want one reused", mints)
	}

	seen := len(paths)
	resp, body := callClaude(t, proxy, mintFor(t, endpoint, "beta", "beta-vertex"))
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "no service account key") {
		t.Fatalf("beta, with no key: %d %s, want a 502 saying it holds no service account key", resp.StatusCode, body)
	}
	if len(paths) != seen {
		t.Fatal("a Vertex connection with no key of its own reached the upstream")
	}

	// And a run is given its token in the first place: the claude-proxy
	// capability, resolved over the same endpoint, honours alpha's Vertex
	// connection rather than refusing it for holding no Anthropic key --
	// for team beta's run too, since the deployment's first Claude
	// connection serves every team (docs/single-tenant-reset.md, stage 1).
	endpoint.SetURL(proxy.URL)
	capability := modelproxycap.New(modelproxy.Claude, endpoint)
	for _, tc := range []struct {
		team, refusal string
	}{{"alpha", ""}, {"beta", ""}} {
		resolution, err := capability.Resolve(t.Context(), model.CapabilityContext{Task: model.Task{}, Run: model.Run{ID: tc.team + "-1"}})
		if err != nil {
			t.Fatalf("%s: Resolve: %v", tc.team, err)
		}
		if resolution.Refused != (tc.refusal != "") || !strings.Contains(resolution.Reason, tc.refusal) {
			t.Errorf("%s: claude-proxy resolution = %+v, want refused=%v mentioning %q", tc.team, resolution, tc.refusal != "", tc.refusal)
		}
	}
}
