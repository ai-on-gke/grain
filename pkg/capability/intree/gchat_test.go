package intree

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const (
	gchatTestToken    = "0123456789abcdef0123456789abcdef-data-plane"
	gchatHookToken    = "hook-token-SECRET-do-not-log"
	gchatHookKey      = "hook-key-SECRET"
	gchatAmbientToken = "ambient-access-token-SECRET"
)

// fakeChat is a fake Google Chat: incoming webhooks under
// /v1/spaces/<id>/messages?key&token, the Chat API's messages.create
// and spaces.get behind a bearer token, and an OAuth token endpoint.
type fakeChat struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	posts []fakePost
	// tokens are the bearer tokens the Chat API accepts.
	tokens map[string]bool
	// fail makes every post answer this status.
	fail int
}

type fakePost struct {
	Space   string
	Webhook bool
	Bearer  string
	Reply   string
	Msg     map[string]any
}

func newFakeChat(t *testing.T) *fakeChat {
	f := &fakeChat{t: t, tokens: map[string]bool{gchatAmbientToken: true, "configured-access-token": true}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChat) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeChat) webhookURL(space string) string {
	return f.srv.URL + "/v1/spaces/" + space + "/messages?key=" + gchatHookKey + "&token=" + gchatHookToken
}

func (f *fakeChat) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"configured-access-token","token_type":"Bearer","expires_in":3600}`)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/spaces/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	space, tail, _ := strings.Cut(rest, "/")
	q := r.URL.Query()
	webhook := q.Get("key") != ""
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if webhook {
		if q.Get("key") != gchatHookKey || q.Get("token") != gchatHookToken {
			http.Error(w, `{"error":{"message":"bad webhook token"}}`, http.StatusUnauthorized)
			return
		}
	} else if !f.tokens[bearer] {
		http.Error(w, `{"error":{"message":"caller is not a member of the space"}}`, http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && tail == "" {
		fmt.Fprintf(w, `{"name":"spaces/%s"}`, space)
		return
	}
	if r.Method != http.MethodPost || tail != "messages" {
		http.NotFound(w, r)
		return
	}
	var msg map[string]any
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	n := len(f.posts)
	f.posts = append(f.posts, fakePost{Space: space, Webhook: webhook, Bearer: bearer, Reply: q.Get("messageReplyOption"), Msg: msg})
	fail := f.fail
	f.mu.Unlock()
	if fail != 0 {
		http.Error(w, `{"error":{"message":"quota exceeded"}}`, fail)
		return
	}
	thread := "spaces/" + space + "/threads/t0"
	if th, ok := msg["thread"].(map[string]any); ok {
		if k, _ := th["threadKey"].(string); k != "" {
			thread = "spaces/" + space + "/threads/" + strings.NewReplacer("/", "_").Replace(k)
		}
		if nm, _ := th["name"].(string); nm != "" {
			thread = nm
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("spaces/%s/messages/m%d", space, n), "thread": map[string]any{"name": thread}})
}

func (f *fakeChat) all() []fakePost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakePost(nil), f.posts...)
}

// fakeADC is the process's ambient credentials: an authorized_user ADC
// that names nobody, as gcloud's application-default login writes.
func fakeADC(_ context.Context, scopes ...string) (*google.Credentials, error) {
	return &google.Credentials{
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: gchatAmbientToken}),
		JSON:        []byte(`{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`),
	}, nil
}

func fakeTokenInfo(_ context.Context, tok string) (string, error) {
	if tok == gchatAmbientToken {
		return "someone@example.com", nil
	}
	return "", errors.New("unknown token")
}

// serviceAccountKey is a configured credential whose token endpoint is
// the fake's.
func serviceAccountKey(t *testing.T, f *fakeChat) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := json.Marshal(map[string]any{
		"type": "service_account", "client_email": "grain-bot@proj.iam.gserviceaccount.com", "private_key": pemKey,
		"private_key_id": "kid", "token_uri": f.srv.URL + "/token", "project_id": "proj",
	})
	return string(b)
}

type gchatFixture struct {
	t    *testing.T
	f    *fakeChat
	s    *Server
	g    *gchatServer
	logs *bytes.Buffer
	now  time.Time
}

func newGChatFixture(t *testing.T) *gchatFixture {
	t.Helper()
	fx := &gchatFixture{t: t, f: newFakeChat(t), logs: &bytes.Buffer{}, now: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)}
	s, g, err := newGChat(GChatServerConfig{
		Listen: "127.0.0.1:0", ADC: fakeADC, TokenInfo: fakeTokenInfo,
		ChatBase: fx.f.srv.URL, WebhookHosts: []string{fx.f.host()},
		Log: log.New(fx.logs, "", 0), Now: func() time.Time { return fx.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	fx.s, fx.g = s, g
	return fx
}

// config is a deployment with an external webhook space and an internal
// app space on ambient credentials, side by side.
func (fx *gchatFixture) config() map[string]any {
	hooks, _ := json.Marshal(map[string]string{"team-room": fx.f.webhookURL("WEBHOOK1")})
	return map[string]any{
		protocol.DataPlaneTokenConfig: gchatTestToken,
		protocol.DeploymentIDConfig:   "e1",
		"grain_url":                   "https://grain.example.com/",
		"display_name":                "Grain Bot",
		"apps":                        []any{map[string]any{"name": "corp-bot", "credential_source": "ambient"}},
		"spaces": []any{
			map[string]any{"name": "team-room", "transport": "webhook", "display_name": "Release Bot", "avatar_url": "https://example.com/a.png"},
			map[string]any{"name": "internal", "transport": "app", "space": "spaces/APP1", "app": "corp-bot",
				"allowed": map[string]any{"threads": true, "max_len": float64(50), "mentions": "users", "per_hour": float64(3)}},
		},
		"webhook_urls": string(hooks),
	}
}

func (fx *gchatFixture) configure(config map[string]any) *protocol.Method {
	fx.t.Helper()
	m, err := fx.g.configure(context.Background(), config)
	if err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func (fx *gchatFixture) mint(attemptID, space string) *protocol.MintResponse {
	fx.t.Helper()
	resp, err := fx.g.mint(context.Background(), attemptID, fx.now.Add(time.Hour), map[string]any{"space": space})
	if err != nil {
		fx.t.Fatal(err)
	}
	return resp
}

func (fx *gchatFixture) post(attemptID, text, thread string) (*gchatPostResult, error) {
	fx.g.mu.Lock()
	gr := fx.g.grants[attemptID]
	fx.g.mu.Unlock()
	if gr == nil {
		fx.t.Fatalf("no grant %s", attemptID)
	}
	return fx.g.post(context.Background(), gr, gr.space, text, thread, false)
}

// callTool calls post_message over the attempt's data-plane endpoint,
// the way grain's proxy does.
func (fx *gchatFixture) callTool(attemptID, token string, args map[string]any) (int, string) {
	fx.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "post_message", "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, "http://"+fx.s.addr+protocol.EndpointPath(attemptID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestGChatConfigValidation(t *testing.T) {
	fx := newGChatFixture(t)
	key := serviceAccountKey(t, fx.f)
	creds, _ := json.Marshal(map[string]string{"bot": key})
	cases := []struct {
		name   string
		edit   func(c map[string]any)
		wantOK bool
		want   string
	}{
		{"webhook and ambient app side by side", func(map[string]any) {}, true, ""},
		{"configured app", func(c map[string]any) {
			c["apps"] = []any{map[string]any{"name": "bot", "credential_source": "configured"}, map[string]any{"name": "corp-bot", "credential_source": "ambient"}}
			c["app_credentials"] = string(creds)
		}, true, ""},
		{"configured app without a credential", func(c map[string]any) {
			c["apps"] = []any{map[string]any{"name": "corp-bot", "credential_source": "configured"}}
		}, false, "app_credentials has no credential"},
		{"ambient app with a credential", func(c map[string]any) {
			c["app_credentials"] = `{"corp-bot": {"type":"service_account"}}`
		}, false, "credential_source is ambient"},
		{"credential for no app", func(c map[string]any) {
			c["app_credentials"] = string(creds)
		}, false, `names "bot"`},
		{"configured credential that is not a key", func(c map[string]any) {
			c["apps"] = []any{map[string]any{"name": "corp-bot", "credential_source": "configured"}}
			c["app_credentials"] = `{"corp-bot": {"type":"authorized_user","refresh_token":"SECRET-REFRESH"}}`
		}, false, "must be a service-account key"},
		{"unknown credential source", func(c map[string]any) {
			c["apps"] = []any{map[string]any{"name": "corp-bot", "credential_source": "corp"}}
		}, false, "credential_source must be configured or ambient"},
		{"app space naming no app", func(c map[string]any) {
			c["spaces"].([]any)[1].(map[string]any)["app"] = "other"
		}, false, `app "other" is not one of the configured apps`},
		{"app space without a space", func(c map[string]any) {
			delete(c["spaces"].([]any)[1].(map[string]any), "space")
		}, false, "needs space"},
		{"webhook space without a URL", func(c map[string]any) { delete(c, "webhook_urls") }, false, "webhook_urls has no URL"},
		{"webhook URL elsewhere", func(c map[string]any) {
			c["webhook_urls"] = `{"team-room": "https://evil.example.com/v1/spaces/X/messages?key=a&token=` + gchatHookToken + `"}`
		}, false, "must be https://chat.googleapis.com"},
		{"duplicate space", func(c map[string]any) {
			c["spaces"] = append(c["spaces"].([]any), c["spaces"].([]any)[0])
		}, false, "configured twice"},
		{"no grain_url", func(c map[string]any) { delete(c, "grain_url") }, false, "grain_url is required"},
		{"no spaces is no method yet", func(c map[string]any) { c["spaces"] = []any{}; delete(c, "webhook_urls") }, true, ""},
		{"no token", func(c map[string]any) { delete(c, protocol.DataPlaneTokenConfig) }, false, "data-plane token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fx.config()
			tc.edit(c)
			_, err := fx.g.configure(context.Background(), c)
			if tc.wantOK != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("configure: %v, want ok=%v containing %q", err, tc.wantOK, tc.want)
			}
			if err != nil {
				for _, secret := range []string{gchatHookToken, "SECRET-REFRESH", "PRIVATE KEY"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error quotes a secret: %v", err)
					}
				}
			}
		})
	}
}

// The schema keeps every secret a whole top-level write-only string, the
// only shape grain stores secrets in.
func TestGChatSchemaSecretsAreTopLevel(t *testing.T) {
	cs, err := protocol.NewCapabilityServer("gchat")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.HandleConfigure(gchatConfigSchema(), func(context.Context, map[string]any) error { return nil }); err != nil {
		t.Fatalf("schema refused: %v", err)
	}
	got := protocol.SecretFields(gchatConfigSchema())
	want := []string{"app_credentials", protocol.DataPlaneTokenConfig, "webhook_urls"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("secret fields = %v, want %v", got, want)
	}
}

func TestGChatGrantArgumentIsAnEnumOfConfiguredSpaces(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	args := fx.g.args
	space := args["properties"].(map[string]any)["space"].(map[string]any)
	if fmt.Sprint(space["enum"]) != "[team-room internal]" {
		t.Errorf("space enum = %v, want the configured spaces", space["enum"])
	}
	if args["additionalProperties"] != false {
		t.Errorf("args admit fields beyond space: %v", args)
	}
	// Neither an unconfigured space nor a URL validates, and mint
	// refuses one that reaches it anyway.
	for _, bad := range []map[string]any{{"space": "elsewhere"}, {"space": fx.f.webhookURL("X")}, {"space": "team-room", "url": "https://x"}} {
		if _, err := protocol.ValidateArgs(args, bad); err == nil {
			t.Errorf("args %v validated", bad)
		}
	}
	if r := fx.mint("e1-7-1-0", "elsewhere"); r.Status != protocol.StatusRefused {
		t.Errorf("mint of an unconfigured space = %+v, want refused", r)
	}
}

func TestGChatPostsInTheTasksOwnThreadWithAStampAndHeader(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	r := fx.mint("e1-42-1-0", "team-room")
	if r.Status != protocol.StatusOK || r.MCPEndpoint != "/runs/e1-42-1-0/mcp" {
		t.Fatalf("mint = %+v", r)
	}
	first, err := fx.post("e1-42-1-0", "Build is *green*. See <https://ci.example.com|the run>.", "")
	if err != nil {
		t.Fatal(err)
	}
	// A later attempt of the same task posts into the same thread; a
	// different task gets its own.
	fx.mint("e1-42-2-0", "team-room")
	second, _ := fx.post("e1-42-2-0", "again", "")
	fx.mint("e1-43-1-0", "team-room")
	other, _ := fx.post("e1-43-1-0", "other task", "")
	if first.Thread == "" || first.Thread != second.Thread || other.Thread == first.Thread {
		t.Errorf("threads: %q, %q, %q; want one per task", first.Thread, second.Thread, other.Thread)
	}
	if first.Message == "" || first.Space != "team-room" {
		t.Errorf("result = %+v", first)
	}
	p := fx.f.all()[0]
	if !p.Webhook || p.Reply != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
		t.Errorf("post = %+v", p)
	}
	if key := p.Msg["thread"].(map[string]any)["threadKey"]; key != "grain-e1-task-42" {
		t.Errorf("threadKey = %v, want grain-e1-task-42", key)
	}
	text := p.Msg["text"].(string)
	if !strings.Contains(text, "<https://grain.example.com/tasks/42|task 42>") || !strings.HasPrefix(text, "Build is *green*") {
		t.Errorf("text = %q, want it stamped with the task's link", text)
	}
	card := p.Msg["cardsV2"].([]any)[0].(map[string]any)["card"].(map[string]any)
	header := card["header"].(map[string]any)
	if header["title"] != "Release Bot" || header["imageUrl"] != "https://example.com/a.png" || !strings.Contains(header["subtitle"].(string), "task 42") {
		t.Errorf("card header = %v, want the space's display name and avatar", header)
	}
}

func TestGChatDisplayNameFallsBackToTheDeploymentDefault(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	fx.mint("e1-5-1-0", "internal")
	if _, err := fx.post("e1-5-1-0", "hello", ""); err != nil {
		t.Fatal(err)
	}
	p := fx.f.all()[0]
	header := p.Msg["cardsV2"].([]any)[0].(map[string]any)["card"].(map[string]any)["header"].(map[string]any)
	if header["title"] != "Grain Bot" {
		t.Errorf("title = %v, want the default display name", header["title"])
	}
	if _, ok := header["imageUrl"]; ok {
		t.Errorf("header has an avatar nobody configured: %v", header)
	}
	if p.Webhook || p.Bearer != gchatAmbientToken || p.Space != "APP1" {
		t.Errorf("app post = %+v, want the Chat API under the ambient token", p)
	}
}

func TestGChatGuardrails(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	fx.mint("e1-9-1-0", "team-room")
	fx.mint("e1-9-1-1", "internal")
	cases := []struct {
		attempt, text, thread, want string
	}{
		{"e1-9-1-0", strings.Repeat("x", defaultGChatMaxLen+1), "", "over space team-room's limit"},
		{"e1-9-1-1", strings.Repeat("y", 51), "", "limit of 50"},
		{"e1-9-1-0", "hey <users/123>", "", "does not allow @-mentions"},
		{"e1-9-1-1", "hey <users/all>", "", "does not allow @all"},
		{"e1-9-1-0", "ping @all now", "", "does not allow @all"},
		{"e1-9-1-0", "see [docs](https://x.example.com)", "", "not Markdown"},
		{"e1-9-1-0", "**bold**", "", "not **text**"},
		{"e1-9-1-0", "# Title", "", "no # headings"},
		{"e1-9-1-0", "<b>hi</b>", "", "HTML is not supported"},
		{"e1-9-1-0", "   ", "", "empty"},
		{"e1-9-1-0", "new thread", "new", `does not allow thread: "new"`},
		{"e1-9-1-1", "elsewhere", "spaces/APP1/threads/someone-elses", "not one this attempt posted to"},
	}
	for _, tc := range cases {
		if _, err := fx.post(tc.attempt, tc.text, tc.thread); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("post(%q, thread %q) = %v, want refusal %q", tc.text, tc.thread, err, tc.want)
		}
	}
	if n := len(fx.f.all()); n != 0 {
		t.Fatalf("%d refused messages reached Chat", n)
	}
	// What a space allows goes through: mentions of people, and a new
	// thread that can then be replied into.
	r, err := fx.post("e1-9-1-1", "cc <users/123>", "new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fx.f.all()[0].Msg["thread"].(map[string]any)["threadKey"].(string), "grain-e1-task-9-") {
		t.Errorf("new thread key = %v", fx.f.all()[0].Msg["thread"])
	}
	if _, err := fx.post("e1-9-1-1", "reply", r.Thread); err != nil {
		t.Errorf("reply into its own new thread: %v", err)
	}
	if p := fx.f.all()[1]; p.Reply != "REPLY_MESSAGE_OR_FAIL" || p.Msg["thread"].(map[string]any)["name"] != r.Thread {
		t.Errorf("reply = %+v", p)
	}
}

func TestGChatRateLimits(t *testing.T) {
	fx := newGChatFixture(t)
	c := fx.config()
	c["per_attempt"] = float64(2)
	fx.configure(c)
	fx.mint("e1-1-1-0", "team-room")
	for i := 0; i < 2; i++ {
		if _, err := fx.post("e1-1-1-0", "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.post("e1-1-1-0", "one too many", ""); err == nil || !strings.Contains(err.Error(), "its limit") {
		t.Errorf("third post of an attempt: %v, want the per-attempt limit", err)
	}
	// The space allows 3 an hour across attempts.
	for i, a := range []string{"e1-2-1-0", "e1-3-1-0", "e1-4-1-0", "e1-5-1-0"} {
		fx.mint(a, "internal")
		_, err := fx.post(a, "hi", "")
		if (i < 3) != (err == nil) {
			t.Errorf("post %d to internal: %v", i, err)
		}
		if i == 3 && (err == nil || !strings.Contains(err.Error(), "in the last hour")) {
			t.Errorf("fourth post: %v, want the per-space limit", err)
		}
	}
	fx.now = fx.now.Add(61 * time.Minute)
	if _, err := fx.post("e1-5-1-0", "later", ""); err != nil {
		t.Errorf("an hour later: %v", err)
	}
	var usage []GChatUsage
	raw, _ := fx.g.usageTool(context.Background(), nil)
	json.Unmarshal(raw, &usage)
	if len(usage) != 2 || usage[1].Space != "internal" || usage[1].Posted != 4 || usage[1].Refused != 1 || usage[1].LastHour != 1 || usage[0].Posted != 2 || usage[0].Refused != 1 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestGChatEndpointServesPostMessageUntilRelease(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	fx.mint("e1-42-1-0", "team-room")
	if code, _ := fx.callTool("e1-42-1-0", "wrong-token-wrong-token-wrong-token", map[string]any{"text": "x"}); code != http.StatusUnauthorized {
		t.Errorf("a wrong token was answered %d", code)
	}
	code, body := fx.callTool("e1-42-1-0", gchatTestToken, map[string]any{"text": "from the agent"})
	if code != http.StatusOK || !strings.Contains(body, `spaces/WEBHOOK1/messages/m0`) || !strings.Contains(body, "threads/grain-e1-task-42") {
		t.Fatalf("post_message = %d %s", code, body)
	}
	if err := fx.g.release(context.Background(), "e1-42-1-0"); err != nil {
		t.Fatal(err)
	}
	if code, _ := fx.callTool("e1-42-1-0", gchatTestToken, map[string]any{"text": "after"}); code != http.StatusNotFound {
		t.Errorf("after release the endpoint answered %d, want 404", code)
	}
	recs, _ := fx.g.takeAudit("e1-42-1-0")
	if len(recs) != 1 {
		t.Fatalf("audit = %+v", recs)
	}
	rec := recs[0].(GChatAudit)
	if rec.Text != "from the agent" || rec.Message == "" || rec.Task != "42" || rec.Space != "team-room" {
		t.Errorf("audit record = %+v", rec)
	}
}

func TestGChatTestPostIsMarked(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	raw, err := fx.g.testPost(context.Background(), map[string]any{"space": "team-room"})
	if err != nil {
		t.Fatal(err)
	}
	var res GChatTestResult
	json.Unmarshal(raw, &res)
	if res.Error != "" || res.Message == "" {
		t.Fatalf("test post = %+v", res)
	}
	text := fx.f.all()[0].Msg["text"].(string)
	if !strings.HasPrefix(text, "*[grain test]*") {
		t.Errorf("test text = %q, want it marked", text)
	}
	raw, _ = fx.g.testPost(context.Background(), map[string]any{"space": "nowhere"})
	if json.Unmarshal(raw, &res); !strings.Contains(res.Error, "not configured") {
		t.Errorf("test post to an unconfigured space = %+v", res)
	}
}

func TestGChatConfiguredCredentialPostsAsTheServiceAccount(t *testing.T) {
	fx := newGChatFixture(t)
	c := fx.config()
	creds, _ := json.Marshal(map[string]string{"bot": serviceAccountKey(t, fx.f)})
	c["apps"] = []any{map[string]any{"name": "bot", "credential_source": "configured", "project": "my-project"}}
	c["app_credentials"] = string(creds)
	c["spaces"].([]any)[1].(map[string]any)["app"] = "bot"
	fx.configure(c)
	if desc := fx.g.desc; !strings.Contains(desc, "app bot (configured): posts as grain-bot@proj.iam.gserviceaccount.com") ||
		!strings.Contains(desc, "can post to spaces/APP1") {
		t.Errorf("report = %q", desc)
	}
	fx.mint("e1-8-1-0", "internal")
	if _, err := fx.post("e1-8-1-0", "hi", ""); err != nil {
		t.Fatal(err)
	}
	if p := fx.f.all()[0]; p.Bearer != "configured-access-token" {
		t.Errorf("post bearer = %q", p.Bearer)
	}
}

func TestGChatAmbientIdentityResolution(t *testing.T) {
	f := newFakeChat(t)
	cases := []struct {
		name         string
		json         string
		want, kind   string
		metadataUsed bool
	}{
		{"user ADC names nobody, so tokeninfo says", `{"type":"authorized_user"}`, "someone@example.com", "authorized_user", false},
		{"service-account key ADC", `{"type":"service_account","client_email":"sa@p.iam.gserviceaccount.com"}`, "sa@p.iam.gserviceaccount.com", "service_account", false},
		{"workload identity impersonating", `{"type":"external_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/wif@p.iam.gserviceaccount.com:generateAccessToken"}`, "wif@p.iam.gserviceaccount.com", "external_account", false},
		{"metadata server", ``, "container@p.iam.gserviceaccount.com", "metadata", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gchatResolver{
				adc: func(_ context.Context, scopes ...string) (*google.Credentials, error) {
					if len(scopes) != 1 || scopes[0] != GChatBotScope {
						t.Errorf("ADC asked for %v", scopes)
					}
					var raw []byte
					if tc.json != "" {
						raw = []byte(tc.json)
					}
					return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: gchatAmbientToken}), JSON: raw}, nil
				},
				metadataEmail: func(context.Context) (string, error) { return "container@p.iam.gserviceaccount.com", nil },
				tokenInfo:     fakeTokenInfo,
			}
			a, err := r.resolve(context.Background(), GChatApp{Name: "a", CredentialSource: GChatSourceAmbient})
			if err != nil {
				t.Fatal(err)
			}
			if a.identity != tc.want || a.kind != tc.kind {
				t.Errorf("identity = %q (%s), want %q (%s)", a.identity, a.kind, tc.want, tc.kind)
			}
		})
	}
	_ = f
	r := gchatResolver{adc: func(context.Context, ...string) (*google.Credentials, error) {
		return nil, errors.New("google: could not find default credentials")
	}}
	if _, err := r.resolve(context.Background(), GChatApp{Name: "a", CredentialSource: GChatSourceAmbient}); err == nil || !strings.Contains(err.Error(), "no ambient credentials") {
		t.Errorf("no ADC: %v", err)
	}
}

// The Test report names who an ambient app resolved to and whether it
// can post; a space it cannot see is reported, not hidden.
func TestGChatReportsAmbientIdentityAndReach(t *testing.T) {
	fx := newGChatFixture(t)
	fx.configure(fx.config())
	desc := fx.g.desc
	for _, want := range []string{"app corp-bot (ambient): posts as someone@example.com (authorized_user)", "space internal: app corp-bot as someone@example.com can post to spaces/APP1", "space team-room: webhook"} {
		if !strings.Contains(desc, want) {
			t.Errorf("report lacks %q:\n%s", want, desc)
		}
	}
	fx.f.mu.Lock()
	fx.f.tokens = map[string]bool{}
	fx.f.mu.Unlock()
	fx.configure(fx.config())
	desc = fx.g.desc
	if !strings.Contains(desc, "cannot post to spaces/APP1") || !strings.Contains(desc, "not a member of the space") {
		t.Errorf("report for an app outside the space:\n%s", desc)
	}
}

// No secret -- a webhook's key and token, a configured key, an access
// token, the data-plane token -- reaches a log line, an error, the Test
// report, the audit or a post's result.
func TestGChatNeverExposesSecrets(t *testing.T) {
	fx := newGChatFixture(t)
	c := fx.config()
	key := serviceAccountKey(t, fx.f)
	creds, _ := json.Marshal(map[string]string{"bot": key})
	c["apps"] = []any{map[string]any{"name": "corp-bot", "credential_source": "ambient"}, map[string]any{"name": "bot", "credential_source": "configured"}}
	c["app_credentials"] = string(creds)
	fx.configure(c)
	var seen strings.Builder
	seen.WriteString(fx.g.desc)
	fx.mint("e1-3-1-0", "team-room")
	res, err := fx.post("e1-3-1-0", "hi", "")
	fmt.Fprint(&seen, res, err)
	fx.f.mu.Lock()
	fx.f.fail = http.StatusTooManyRequests
	fx.f.mu.Unlock()
	_, err = fx.post("e1-3-1-0", "fails", "")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("failed post: %v", err)
	}
	fmt.Fprint(&seen, err)
	// A webhook that cannot be reached at all: the transport's error
	// would quote the URL.
	dead := &webhookPoster{url: "http://127.0.0.1:1/v1/spaces/X/messages?key=k&token=" + gchatHookToken, client: http.DefaultClient}
	_, err = dead.post(context.Background(), gchatMessage{Text: "x"}, "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
	fmt.Fprint(&seen, err)
	fx.g.release(context.Background(), "e1-3-1-0")
	recs, _ := fx.g.takeAudit("e1-3-1-0")
	b, _ := json.Marshal(recs)
	seen.Write(b)
	seen.WriteString(fx.logs.String())
	raw, _ := fx.g.usageTool(context.Background(), nil)
	seen.Write(raw)
	all := seen.String()
	for _, secret := range []string{gchatHookToken, gchatHookKey, gchatAmbientToken, "configured-access-token", "PRIVATE KEY", gchatTestToken} {
		if strings.Contains(all, secret) {
			t.Errorf("%q appears in what the server says:\n%s", secret, all)
		}
	}
	if !strings.Contains(fx.logs.String(), "space team-room posted") {
		t.Errorf("log = %q, want the post recorded", fx.logs.String())
	}
}

func TestGChatTaskOf(t *testing.T) {
	for _, tc := range []struct{ dep, id, want string }{
		{"e1", "e1-42-1-0", "42"},
		{"my-dep", "my-dep-316-2-1", "316"},
		{"", "e1-7-1-0", "7"},
	} {
		if got := gchatTaskOf(tc.dep, tc.id); got != tc.want {
			t.Errorf("gchatTaskOf(%q, %q) = %q, want %q", tc.dep, tc.id, got, tc.want)
		}
	}
	_ = url.PathEscape
}
