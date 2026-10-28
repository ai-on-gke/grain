package modelproxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upstream is a stand-in for a vendor's own API that records what it was
// asked and answers with whatever the test wants.
//
// It records only requests the proxy under test forwarded -- those
// carrying its marker, which the proxy's Transport (upstream.transport)
// adds -- and answers anything else 404. Its loopback port is one the OS
// may just have taken back from another package's test server, and a
// client of that one still polling (say, for /api/tasks) would otherwise
// be recorded as something the proxy forwarded.
type upstream struct {
	server  *httptest.Server
	marker  string
	got     *http.Request
	gotBody string
	status  int
	body    string
	// header is set on every answer, beside its Content-Type.
	header map[string]string
}

// upstreamMarkerHeader carries upstream.marker from the proxy's
// Transport to the upstream, which strips it before recording.
const upstreamMarkerHeader = "X-Modelproxy-Test-Upstream"

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{status: http.StatusOK, body: `{"ok":true}`, marker: rand.Text()}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(upstreamMarkerHeader) != u.marker {
			http.NotFound(w, r)
			return
		}
		r.Header.Del(upstreamMarkerHeader)
		body, _ := io.ReadAll(r.Body)
		u.got, u.gotBody = r.Clone(context.Background()), string(body)
		w.Header().Set("Content-Type", "application/json")
		for name, value := range u.header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(u.status)
		_, _ = io.WriteString(w, u.body)
	}))
	t.Cleanup(u.server.Close)
	return u
}

// transport is the Proxy.Transport to forward to u with: the default
// transport, marking each request as one u should record.
func (u *upstream) transport() http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set(upstreamMarkerHeader, u.marker)
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testConnection is the team connection the tests' tokens forward with,
// and what proxyFor's credential resolver answers for.
var testConnection = Connection{ID: "conn-a"}

// proxyFor wires a proxy for vendor in front of up, holding credential,
// and returns it served over HTTP with the endpoint the test mints from.
func proxyFor(t *testing.T, vendor Vendor, up *upstream, credential string) (*httptest.Server, *Endpoint, *[]Entry) {
	t.Helper()
	endpoint := NewEndpoint()
	endpoint.SetCredential(func(context.Context, Connection) (string, error) { return credential, nil })
	var audit []Entry
	proxy := &Proxy{
		Vendor:    vendor,
		Endpoint:  endpoint,
		Upstream:  up.server.URL,
		Transport: up.transport(),
		Audit:     func(e Entry) { audit = append(audit, e) },
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server, endpoint, &audit
}

func call(t *testing.T, server *httptest.Server, method, path string, header http.Header, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// authenticated is a caller of this vendor presenting the proxy's own
// token, in the header that vendor's clients use.
func authenticated(vendor Vendor, token string) http.Header {
	header := http.Header{}
	switch vendor.Name {
	case Gemini.Name:
		header.Set("x-goog-api-key", token)
	default:
		header.Set("Authorization", "Bearer "+token)
	}
	return header
}

// credentialShown is what reached the upstream as a credential -- the
// header, or for Gemini the query parameter, that vendor takes a key in.
func credentialShown(vendor Vendor, r *http.Request) string {
	switch vendor.Name {
	case Claude.Name:
		if key := r.Header.Get("x-api-key"); key != "" {
			return key
		}
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	case Gemini.Name:
		return r.URL.Query().Get("key")
	default:
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
}

// path is a request path this vendor's own API answers, so a test reads
// as the call a run would really make.
func path(vendor Vendor) string {
	switch vendor.Name {
	case Claude.Name:
		return "/v1/messages"
	case Gemini.Name:
		return "/v1beta/models/gemini-3-pro:generateContent"
	default:
		return "/v1/chat/completions"
	}
}

// queryParam is a query parameter this vendor's clients really send, so
// a test can check the caller's own parameters survive the trip.
func queryParam(vendor Vendor) (string, string) {
	switch vendor.Name {
	case Claude.Name:
		return "beta", "true"
	case Gemini.Name:
		return "alt", "sse"
	default:
		return "stream", "true"
	}
}

// Every claim below is made about all three vendors: there is one
// handler, built three times, and a property that held only for Claude
// would be one this package's whole shape is wrong about.
func eachVendor(t *testing.T, f func(t *testing.T, vendor Vendor)) {
	t.Helper()
	for _, vendor := range Vendors() {
		t.Run(vendor.Name, func(t *testing.T) { f(t, vendor) })
	}
}

func TestAMintedTokenReachesTheVendorWithItsConnectionsCredential(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, audit := proxyFor(t, vendor, up, "the-deployments-own-credential")
		_, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}

		header := authenticated(vendor, token)
		header.Set("Content-Type", "application/json")
		name, value := queryParam(vendor)
		resp := call(t, server, http.MethodPost, path(vendor)+"?"+name+"="+value, header, `{"model":"a-model"}`)

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		if body, _ := io.ReadAll(resp.Body); string(body) != `{"ok":true}` {
			t.Fatalf("body = %q, want the vendor's own answer streamed back", body)
		}
		if up.got == nil {
			t.Fatal("nothing reached the upstream")
		}
		// The request itself is the vendor's, untouched: method, path,
		// body, and whatever the caller put in the query beyond a key.
		if up.got.Method != http.MethodPost || up.got.URL.Path != path(vendor) {
			t.Errorf("upstream saw %s %s", up.got.Method, up.got.URL.Path)
		}
		if got := up.got.URL.Query().Get(name); got != value {
			t.Errorf("upstream query %s = %q, want the caller's own parameters kept", name, got)
		}
		if up.gotBody != `{"model":"a-model"}` {
			t.Errorf("upstream body = %q", up.gotBody)
		}
		// The credential, though, is this deployment's -- and the token
		// the sandbox holds never leaves the controller.
		if got := credentialShown(vendor, up.got); got != "the-deployments-own-credential" {
			t.Errorf("upstream saw the credential %q, want this deployment's", got)
		}
		if strings.Contains(up.got.Header.Get("Authorization")+up.got.URL.RawQuery+
			up.got.Header.Get("x-api-key")+up.got.Header.Get("x-goog-api-key"), token) {
			t.Error("the sandbox's own proxy token was forwarded to the vendor")
		}
		if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeForwarded || (*audit)[0].Owner != "run-7" {
			t.Errorf("audit = %+v, want one forwarded entry owned by run-7", *audit)
		}
		if (*audit)[0].Vendor != vendor.Name {
			t.Errorf("audit entry names vendor %q, want %q", (*audit)[0].Vendor, vendor.Name)
		}
		// An audit entry is a log line, and this vendor's credential
		// lives in a query string, so no entry ever carries one.
		if strings.ContainsAny((*audit)[0].Path, "?&") {
			t.Errorf("audit path = %q, want the query string left out of it", (*audit)[0].Path)
		}
	})
}

func TestARequestWithNoTokenIsRefused(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, _, audit := proxyFor(t, vendor, up, "the-deployments-own-credential")
		resp := call(t, server, http.MethodPost, path(vendor), http.Header{}, "{}")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		assertVendorError(t, vendor, resp, DenialAuth)
		if up.got != nil {
			t.Fatal("an unauthenticated request reached the vendor")
		}
		if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeNoToken {
			t.Errorf("audit = %+v, want one no-token entry", *audit)
		}
	})
}

func TestATokenThisProxyNeverMintedIsRefused(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, audit := proxyFor(t, vendor, up, "the-deployments-own-credential")
		id, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		unknown := authenticated(vendor, "deadbeef.0123456789")
		if resp := call(t, server, http.MethodPost, path(vendor), unknown, "{}"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("an unknown token got %d, want 401", resp.StatusCode)
		}

		// And the run's own token stops working the moment its lease is
		// revoked, which is what makes a token left behind in a sandbox
		// worthless rather than merely inconvenient.
		endpoint.Revoke(id)
		if resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a revoked token got %d, want 401", resp.StatusCode)
		}
		if up.got != nil {
			t.Fatal("a refused request reached the vendor")
		}
		for _, entry := range *audit {
			if entry.Outcome != OutcomeUnknownToken {
				t.Errorf("audit entry %+v, want every one to be unknown-token", entry)
			}
		}
	})
}

func TestAConnectionWithNoCredentialSaysSo(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, audit := proxyFor(t, vendor, up, "")
		_, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		header := authenticated(vendor, token)
		resp := call(t, server, http.MethodPost, path(vendor), header, "{}")
		// 502, not 401: the caller's token was fine and the gap is on this
		// side of the proxy.
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", resp.StatusCode)
		}
		message := assertVendorError(t, vendor, resp, DenialUpstream)
		if !strings.Contains(message, "connection conn-a holds no") {
			t.Errorf("message = %q, want it to name the connection an admin fixes", message)
		}
		if strings.Contains(message, vendor.Secret) {
			t.Errorf("message = %q names the deployment-wide secret, which this proxy never forwards with", message)
		}
		if up.got != nil {
			t.Fatal("a request was forwarded with no credential")
		}
		if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeNoCredential {
			t.Errorf("audit = %+v, want one no-credential entry", *audit)
		}

		// A credential that cannot be resolved at all is the same answer:
		// nothing is forwarded unauthenticated.
		endpoint.SetCredential(func(context.Context, Connection) (string, error) { return "", errors.New("secrets are sealed") })
		if resp := call(t, server, http.MethodPost, path(vendor), header, "{}"); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status with an unresolvable credential = %d, want 502", resp.StatusCode)
		}
	})
}

// Whatever the caller authenticated to this proxy with is stripped
// before the request leaves, in every place that vendor takes a
// credential -- a token that is worth nothing upstream must never be
// forwarded as though it were one, and a key a run set for itself must
// never be spent in place of the one this deployment chose.
func TestACallersOwnCredentialsNeverReachTheVendor(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, _ := proxyFor(t, vendor, up, "the-deployments-own-credential")
		_, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		// The token goes in the slot this vendor's Token reads first, and
		// everything else this vendor takes a credential (or a billing
		// account) in is filled with something the run made up.
		header, query := http.Header{}, ""
		switch vendor.Name {
		case Claude.Name:
			header.Set("x-api-key", token)
			header.Set("Authorization", "Bearer a-key-the-run-set-for-itself")
		case Gemini.Name:
			query = "?key=" + token
			header.Set("x-goog-api-key", "a-key-the-run-set-for-itself")
			header.Set("Authorization", "Bearer a-key-the-run-set-for-itself")
		default:
			header.Set("Authorization", "Bearer "+token)
			header.Set("OpenAI-Project", "somebody-elses-project")
			header.Set("OpenAI-Organization", "somebody-elses-organization")
		}
		call(t, server, http.MethodPost, path(vendor)+query, header, "{}")
		if up.got == nil {
			t.Fatal("nothing reached the upstream")
		}

		forwarded := strings.Join([]string{
			up.got.Header.Get("Authorization"), up.got.Header.Get("x-api-key"),
			up.got.Header.Get("x-goog-api-key"), up.got.Header.Get("OpenAI-Project"),
			up.got.Header.Get("OpenAI-Organization"), up.got.URL.RawQuery,
		}, " ")
		for _, made := range []string{"a-key-the-run-set-for-itself", "somebody-elses", token} {
			if strings.Contains(forwarded, made) {
				t.Errorf("the caller's own %q reached the vendor: %q", made, forwarded)
			}
		}
		if got := credentialShown(vendor, up.got); got != "the-deployments-own-credential" {
			t.Errorf("upstream saw the credential %q, want this deployment's", got)
		}
	})
}

// A Claude Code OAuth token is a different credential to that API than
// an API key: different header, and a beta it has to be paired with.
// This is the same rule pkg/agent/credcheck's own check follows, which
// is why the proxy calls into it rather than restating it.
func TestAnOAuthCredentialIsForwardedAsABearerWithItsBeta(t *testing.T) {
	up := newUpstream(t)
	server, endpoint, _ := proxyFor(t, Claude, up, "sk-ant-oat01-whatever")
	_, token, err := endpoint.Mint("run-7", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("x-api-key", token)
	header.Add("anthropic-beta", "fine-grained-tool-streaming-2025-05-14")
	call(t, server, http.MethodPost, "/v1/messages", header, "{}")

	if got := up.got.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-whatever" {
		t.Errorf("upstream Authorization = %q", got)
	}
	if got := up.got.Header.Get("x-api-key"); got != "" {
		t.Errorf("upstream x-api-key = %q, want the caller's proxy token dropped", got)
	}
	// Both betas: the one the credential needs, and the one the caller
	// asked for -- a proxy that replaced the header would silently turn
	// the caller's request into a different one.
	betas := strings.Join(up.got.Header.Values("anthropic-beta"), ",")
	for _, want := range []string{"oauth-2025-04-20", "fine-grained-tool-streaming-2025-05-14"} {
		if !strings.Contains(betas, want) {
			t.Errorf("upstream anthropic-beta = %q, want it to include %q", betas, want)
		}
	}
}

// Gemini is the vendor whose credential is not a header at all: a caller
// presents its token as ?key=, and what leaves for Google carries this
// deployment's key in the same place -- with the caller's gone and the
// rest of the query untouched.
func TestTheGeminiKeyIsSwappedInTheQueryString(t *testing.T) {
	up := newUpstream(t)
	server, endpoint, audit := proxyFor(t, Gemini, up, "the-deployments-own-key")
	_, token, err := endpoint.Mint("run-7", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	resp := call(t, server, http.MethodPost,
		"/v1beta/models/gemini-3-pro:streamGenerateContent?key="+token+"&alt=sse", http.Header{}, "{}")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	query := up.got.URL.Query()
	if query.Get("key") != "the-deployments-own-key" {
		t.Errorf("upstream key = %q, want this deployment's own", query.Get("key"))
	}
	if query.Get("alt") != "sse" {
		t.Errorf("upstream alt = %q, want the rest of the caller's query kept", query.Get("alt"))
	}
	if strings.Contains(up.got.URL.RawQuery, token) {
		t.Errorf("the sandbox's own token was forwarded to Google: %q", up.got.URL.RawQuery)
	}
	if (*audit)[0].Path != "/v1beta/models/gemini-3-pro:streamGenerateContent" {
		t.Errorf("audit path = %q, want the path without the query the keys are in", (*audit)[0].Path)
	}
}

// A handler wired up with no endpoint at all refuses everything rather
// than panicking -- the same fail-closed shape gitproxy.ReadScopes has,
// and so does one wired up with no vendor.
func TestAProxyWithNoEndpointOrNoVendorRefusesEverything(t *testing.T) {
	for _, proxy := range map[string]*Proxy{
		"no endpoint": {Vendor: Claude},
		"no vendor":   {Endpoint: NewEndpoint()},
	} {
		server := httptest.NewServer(proxy)
		t.Cleanup(server.Close)
		header := http.Header{}
		header.Set("Authorization", "Bearer anything.at-all")
		if resp := call(t, server, http.MethodPost, "/v1/messages", header, "{}"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	}
}

// The response is streamed, not buffered: a model reply arrives token by
// token over server-sent events, and a proxy that waited for the end of
// one would turn a streaming API into a batch one.
func TestAStreamedAnswerArrivesBeforeItIsFinished(t *testing.T) {
	release := make(chan struct{})
	up := &upstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: message_stop\n\n")
	}))
	t.Cleanup(up.server.Close)
	server, endpoint, _ := proxyFor(t, Claude, up, "the-deployments-own-credential")
	_, token, err := endpoint.Mint("run-7", testConnection)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "message_start") {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event never arrived while the upstream was still writing: the proxy is buffering")
	}
	close(release)
}

func TestAnUnreachableUpstreamIsAVendorShapedError(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, _ := proxyFor(t, vendor, up, "the-deployments-own-credential")
		up.server.Close() // nothing is listening there any more
		_, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", resp.StatusCode)
		}
		message := assertVendorError(t, vendor, resp, DenialUpstream)
		if !strings.Contains(message, "grain") {
			t.Errorf("message = %q, want it to say which proxy could not reach the vendor", message)
		}
		// Whatever the transport had to say about the failure, this
		// deployment's own credential is not in it -- it travels in the
		// URL for one of these vendors, and this body goes to a sandbox.
		if strings.Contains(message, "the-deployments-own-credential") {
			t.Errorf("the error handed to the sandbox carries this deployment's credential: %q", message)
		}
	})
}

// redact is what keeps that promise, and it is the credential itself
// that is redacted rather than anything guessed at by shape.
func TestRedactTakesACredentialBackOutOfAMessage(t *testing.T) {
	if got := redact("dial https://host/v1?key=sk-secret failed", "sk-secret"); strings.Contains(got, "sk-secret") {
		t.Errorf("redact left the credential in: %q", got)
	}
	// An empty credential redacts nothing: replacing an empty needle
	// would otherwise rewrite the whole message.
	if got := redact("dial failed", ""); got != "dial failed" {
		t.Errorf("redact with no credential = %q", got)
	}
}

// assertVendorError checks the refusal is in the shape this vendor's own
// client libraries already know how to raise, and returns its message.
// Each vendor is spelled out here rather than derived from the same
// table the proxy writes with, because "it matches what we wrote" is not
// the claim worth testing.
func assertVendorError(t *testing.T, vendor Vendor, resp *http.Response, kind Denial) string {
	t.Helper()
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    any    `json:"code"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the refusal: %v", err)
	}
	if body.Error.Message == "" {
		t.Error("the refusal carries no message, so nobody reading it can tell what to do")
	}
	switch vendor.Name {
	case Claude.Name:
		want := map[Denial]string{DenialAuth: "authentication_error", DenialUpstream: "api_error", DenialForbidden: "permission_error",
			DenialTooManyRequests: "rate_limit_error"}[kind]
		if body.Type != "error" || body.Error.Type != want {
			t.Errorf("refusal = %+v, want Anthropic's own %q error", body, want)
		}
	case OpenAI.Name:
		want, wantCode := "invalid_request_error", "invalid_api_key"
		switch kind {
		case DenialUpstream:
			want, wantCode = "api_error", ""
		case DenialForbidden:
			wantCode = ""
		case DenialTooManyRequests:
			want, wantCode = "requests", "rate_limit_exceeded"
		}
		if body.Error.Type != want {
			t.Errorf("refusal type = %q, want OpenAI's own %q", body.Error.Type, want)
		}
		code, _ := body.Error.Code.(string)
		if code != wantCode {
			t.Errorf("refusal code = %v, want %q", body.Error.Code, wantCode)
		}
	case Gemini.Name:
		want := map[Denial]string{DenialAuth: "UNAUTHENTICATED", DenialUpstream: "UNAVAILABLE", DenialForbidden: "PERMISSION_DENIED",
			DenialTooManyRequests: "RESOURCE_EXHAUSTED"}[kind]
		if body.Error.Status != want {
			t.Errorf("refusal status = %q, want Google's own %q", body.Error.Status, want)
		}
		code, _ := body.Error.Code.(float64)
		if int(code) != resp.StatusCode {
			t.Errorf("refusal code = %v, want the HTTP status %d Google echoes there", body.Error.Code, resp.StatusCode)
		}
	}
	return body.Error.Message
}

// limitedProxyFor is proxyFor with a MaxBodyBytes of the test's own, so
// the limit can be exercised without sending tens of megabytes.
func limitedProxyFor(t *testing.T, vendor Vendor, up *upstream, limit int64) (*httptest.Server, string, *[]Entry) {
	t.Helper()
	endpoint := NewEndpoint()
	endpoint.SetCredential(func(context.Context, Connection) (string, error) { return "credential", nil })
	_, token, err := endpoint.Mint("run-big", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	var audit []Entry
	proxy := &Proxy{
		Vendor:       vendor,
		Endpoint:     endpoint,
		Upstream:     up.server.URL,
		Transport:    up.transport(),
		Audit:        func(e Entry) { audit = append(audit, e) },
		MaxBodyBytes: limit,
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server, token, &audit
}

func TestARequestBodyOverTheLimitIsRefusedWith413(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, token, audit := limitedProxyFor(t, vendor, up, 16)

		// Declared up front: refused before anything is forwarded.
		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), strings.Repeat("x", 17))
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("declared oversize body: status %d, want 413", resp.StatusCode)
		}
		if up.got != nil {
			t.Fatalf("an oversize body reached the upstream")
		}
		var refusal map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&refusal); err != nil {
			t.Fatalf("refusal is not JSON: %v", err)
		}
		if got := (*audit)[len(*audit)-1]; got.Outcome != OutcomeTooLarge || got.Owner != "run-big" {
			t.Fatalf("audit = %+v, want a too-large refusal for run-big", got)
		}

		// Chunked, with no length to refuse on: cut off at the limit. The
		// upstream may still be reading when the 413 comes back, so what
		// it received is handed over on a channel rather than read off
		// the shared upstream.
		received := make(chan int, 1)
		chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := io.Copy(io.Discard, r.Body)
			received <- int(n)
		}))
		t.Cleanup(chunked.Close)
		server, token, audit = limitedProxyFor(t, vendor, &upstream{server: chunked}, 16)
		req, err := http.NewRequest(http.MethodPost, server.URL+path(vendor),
			struct{ io.Reader }{strings.NewReader(strings.Repeat("y", 1<<16))})
		if err != nil {
			t.Fatal(err)
		}
		req.Header = authenticated(vendor, token)
		resp, err = server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked oversize body: status %d, want 413", resp.StatusCode)
		}
		if got := (*audit)[len(*audit)-1]; got.Outcome != OutcomeTooLarge || got.Owner != "run-big" {
			t.Fatalf("chunked oversize audit = %+v, want a too-large refusal for run-big", got)
		}
		select {
		case n := <-received:
			if n > 16 {
				t.Fatalf("upstream received %d bytes, more than the 16-byte limit", n)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("upstream never finished reading the cut-off body")
		}
	})
}

func TestARequestBodyAtTheLimitIsForwarded(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, token, _ := limitedProxyFor(t, vendor, up, 16)
		body := strings.Repeat("z", 16)
		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if up.gotBody != body {
			t.Fatalf("upstream got %q, want %q", up.gotBody, body)
		}
	})
}

func TestAZeroMaxBodyBytesMeansTheDefault(t *testing.T) {
	if got := (&Proxy{}).maxBodyBytes(); got != DefaultMaxBodyBytes {
		t.Fatalf("maxBodyBytes() = %d, want %d", got, DefaultMaxBodyBytes)
	}
}

// A sandbox token is a token for calling a model, not for the whole of
// the vendor's API: files, fine-tuning, batches and organization
// endpoints are refused before this deployment's credential is attached
// to anything (#2403), and so is a path dressed up to look like an
// allowed one.
func TestOnlyModelEndpointsAreForwarded(t *testing.T) {
	refused := []string{
		"/v1/files",
		"/v1/fine_tuning/jobs",
		"/v1/batches",
		"/v1/messages/batches",
		"/v1/organization/admin_api_keys",
		"/v1beta/files",
		"/v1beta/tunedModels",
		"/upload/v1beta/files",
		"/",
		"/v1/models/../files",
		"/v1/models/%2e%2e/files",
		"/v1/models/x%2F..%2Ffiles",
		"/v1/models/",
		"//v1/models/x",
		"/v1/messages/",
		"/v1/messagesx",
	}
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		for _, p := range append(refused, path(vendor)+"?purpose=fine-tune") {
			up := newUpstream(t)
			server, endpoint, audit := proxyFor(t, vendor, up, "the-deployments-own-credential")
			_, token, err := endpoint.Mint("run-7", testConnection)
			if err != nil {
				t.Fatal(err)
			}
			resp := rawCall(t, server, http.MethodPost, p, authenticated(vendor, token))
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s: status %d, want 403: %s", p, resp.StatusCode, body)
				continue
			}
			assertVendorError(t, vendor, resp, DenialForbidden)
			if up.got != nil {
				t.Errorf("%s reached the upstream as %s", p, up.got.URL)
			}
			if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeForbidden || (*audit)[0].Owner != "run-7" {
				t.Errorf("%s: audit = %+v, want one forbidden entry owned by run-7", p, *audit)
			}
		}
	})
}

// A path is checked only once the caller has authenticated: a stranger
// learns nothing about the allowlist, and is told what any stranger is.
func TestAnUnauthenticatedCallerIsRefusedBeforeThePathIsChecked(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, _, _ := proxyFor(t, vendor, up, "cred")
		resp := rawCall(t, server, http.MethodPost, "/v1/files", http.Header{})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", resp.StatusCode)
		}
	})
}

func TestEveryAdvertisedModelEndpointIsForwarded(t *testing.T) {
	post, get := http.MethodPost, http.MethodGet
	allowed := map[string][]Route{
		Claude.Name: {{Method: post, Path: "/v1/messages"}, {Method: post, Path: "/v1/messages/count_tokens?beta=true"},
			{Method: get, Path: "/v1/models"}, {Method: get, Path: "/v1/models/claude-x?limit=1"}},
		OpenAI.Name: {{Method: post, Path: "/v1/responses"}, {Method: get, Path: "/v1/responses/resp_1?stream=true"},
			{Method: post, Path: "/v1/responses/resp_1/cancel"}, {Method: post, Path: "/v1/responses/compact"},
			{Method: post, Path: "/v1/responses/input_tokens"},
			{Method: post, Path: "/v1/chat/completions"}, {Method: post, Path: "/v1/completions"}, {Method: post, Path: "/v1/embeddings"},
			{Method: get, Path: "/v1/models"}, {Method: get, Path: "/v1/models/gpt-x"}, {Method: get, Path: "/v1/models/ft:gpt-x:org:custom:id"}},
		Gemini.Name: {{Method: post, Path: "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse"},
			{Method: post, Path: "/v1beta/models/gemini-x:countTokens"}, {Method: post, Path: "/v1/models/gemini-x:generateContent"},
			{Method: post, Path: "/v1beta/models/text-embedding-004:embedContent"},
			{Method: post, Path: "/v1alpha/models/text-embedding-004:batchEmbedContents"},
			{Method: get, Path: "/v1beta/models?pageSize=5"}, {Method: get, Path: "/v1beta/models/gemini-x"}},
	}
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		for _, route := range allowed[vendor.Name] {
			up := newUpstream(t)
			server, endpoint, _ := proxyFor(t, vendor, up, "cred")
			_, token, err := endpoint.Mint("run-7", testConnection)
			if err != nil {
				t.Fatal(err)
			}
			resp := rawCall(t, server, route.Method, route.Path, authenticated(vendor, token))
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s %s: status %d: %s", route.Method, route.Path, resp.StatusCode, body)
				continue
			}
			if up.got == nil || up.got.Method != route.Method {
				t.Errorf("%s %s: upstream saw %v", route.Method, route.Path, up.got)
			}
		}
	})
}

// A path beneath an allowed one is not allowed for being beneath it.
// Gemini's methods on a model share the model's path, and the ones that
// leave a Batch API job or a long-running operation behind on this
// deployment's account -- outliving the run and its revoked token --
// are no more inference than /v1/batches is. Nor is replaying another
// run's stored OpenAI conversation.
func TestAJobOrStoredDataBeneathAModelEndpointIsNotForwarded(t *testing.T) {
	post, get := http.MethodPost, http.MethodGet
	refused := map[string][]Route{
		OpenAI.Name: {{Method: get, Path: "/v1/responses/resp_1/input_items"}, {Method: post, Path: "/v1/responses/resp_1/delete"},
			{Method: get, Path: "/v1/responses/resp_1/cancel"}, {Method: post, Path: "/v1/responses/resp_1/cancel/x"},
			{Method: post, Path: "/v1/responses/resp_1"}},
		Gemini.Name: {{Method: post, Path: "/v1beta/models/gemini-2.5-pro:batchGenerateContent"},
			{Method: post, Path: "/v1beta/models/text-embedding-004:asyncBatchEmbedContent"},
			{Method: post, Path: "/v1beta/models/veo-3.0-generate-001:predictLongRunning"},
			{Method: post, Path: "/v1beta/models/imagen-4.0-generate-001:predict"},
			{Method: get, Path: "/v1beta/models/veo-3.0-generate-001/operations"},
			{Method: get, Path: "/v1beta/models/veo-3.0-generate-001/operations/op-1"},
			{Method: post, Path: "/v1/models/gemini-x:generateContent/x"},
			{Method: post, Path: "/v1/models/gemini-x"},
			{Method: post, Path: "/v1/models/:generateContent"},
			{Method: post, Path: "/v1/models/a:b:generateContent"},
			{Method: get, Path: "/v1/models/gemini-x:generateContent"},
			{Method: post, Path: "/v1alpha/models/gemini-x:streamGenerateContentx"}},
	}
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		for _, route := range refused[vendor.Name] {
			up := newUpstream(t)
			server, endpoint, audit := proxyFor(t, vendor, up, "cred")
			_, token, err := endpoint.Mint("run-7", testConnection)
			if err != nil {
				t.Fatal(err)
			}
			resp := rawCall(t, server, route.Method, route.Path, authenticated(vendor, token))
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s %s: status %d, want 403: %s", route.Method, route.Path, resp.StatusCode, body)
				continue
			}
			assertVendorError(t, vendor, resp, DenialForbidden)
			if up.got != nil {
				t.Errorf("%s %s reached the upstream", route.Method, route.Path)
			}
			if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeForbidden {
				t.Errorf("%s %s: audit = %+v, want one forbidden entry", route.Method, route.Path, *audit)
			}
		}
	})
}

// An allowed path is allowed for the methods inference needs and no
// others: DELETE /v1/models/<id> would delete one of this deployment's
// fine-tuned models, GET /v1/chat/completions lists every completion it
// has stored, and a PATCH or PUT on a model endpoint is never a call to
// one.
func TestAModelEndpointIsForwardedOnlyForItsOwnMethods(t *testing.T) {
	refused := map[string][]Route{
		Claude.Name: {{Method: http.MethodGet, Path: "/v1/messages"}, {Method: http.MethodDelete, Path: "/v1/models/claude-x"},
			{Method: http.MethodPost, Path: "/v1/models"}, {Method: http.MethodPut, Path: "/v1/messages"}},
		OpenAI.Name: {{Method: http.MethodDelete, Path: "/v1/models/ft:gpt-x:org:custom:id"}, {Method: http.MethodDelete, Path: "/v1/responses/resp_1"},
			{Method: http.MethodGet, Path: "/v1/chat/completions"}, {Method: http.MethodPost, Path: "/v1/models/gpt-x"},
			{Method: http.MethodPatch, Path: "/v1/responses/resp_1"}, {Method: http.MethodGet, Path: "/v1/embeddings"},
			// Not a WebSocket handshake, so not the route codex opens.
			{Method: http.MethodGet, Path: "/v1/responses"}},
		Gemini.Name: {{Method: http.MethodDelete, Path: "/v1beta/models/gemini-x"}, {Method: http.MethodPatch, Path: "/v1beta/models/gemini-x"},
			{Method: http.MethodPost, Path: "/v1beta/models"}, {Method: http.MethodPut, Path: "/v1/models/gemini-x:generateContent"}},
	}
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		for _, route := range refused[vendor.Name] {
			up := newUpstream(t)
			server, endpoint, audit := proxyFor(t, vendor, up, "cred")
			_, token, err := endpoint.Mint("run-7", testConnection)
			if err != nil {
				t.Fatal(err)
			}
			resp := rawCall(t, server, route.Method, route.Path, authenticated(vendor, token))
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s %s: status %d, want 403: %s", route.Method, route.Path, resp.StatusCode, body)
				continue
			}
			if message := assertVendorError(t, vendor, resp, DenialForbidden); !strings.Contains(message, route.Method+" ") {
				t.Errorf("%s %s: refusal %q does not name the method it refused", route.Method, route.Path, message)
			}
			if up.got != nil {
				t.Errorf("%s %s reached the upstream", route.Method, route.Path)
			}
			if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeForbidden || (*audit)[0].Method != route.Method {
				t.Errorf("%s %s: audit = %+v, want one forbidden entry", route.Method, route.Path, *audit)
			}
		}
	})
}

// Unrestricted is the escape hatch: a deployment that chose it forwards
// whatever method and path the caller asked for.
func TestAnUnrestrictedProxyForwardsAnyPath(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		endpoint := NewEndpoint()
		endpoint.SetCredential(func(context.Context, Connection) (string, error) { return "cred", nil })
		server := httptest.NewServer(&Proxy{Vendor: vendor, Endpoint: endpoint, Upstream: up.server.URL, Transport: up.transport(), Unrestricted: true})
		t.Cleanup(server.Close)
		_, token, err := endpoint.Mint("run-7", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		resp := rawCall(t, server, http.MethodPost, "/v1/files?purpose=fine-tune", authenticated(vendor, token))
		if resp.StatusCode != http.StatusOK || up.got == nil || up.got.URL.Path != "/v1/files" {
			t.Fatalf("status %d, upstream saw %v", resp.StatusCode, up.got)
		}
		up.got = nil
		resp = rawCall(t, server, http.MethodDelete, "/v1/models/a-model", authenticated(vendor, token))
		if resp.StatusCode != http.StatusOK || up.got == nil || up.got.Method != http.MethodDelete {
			t.Fatalf("status %d, upstream saw %v", resp.StatusCode, up.got)
		}
	})
}

// rawCall sends path exactly as written -- percent-encoding and dot
// segments included -- rather than letting the client normalise it.
func rawCall(t *testing.T, server *httptest.Server, method, rawPath string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+rawPath, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The test upstream records only what the proxy forwarded. A stray client
// that finds its loopback port -- another package's poller whose server
// the OS gave this port to before -- is answered 404 and never mistaken
// for a request the proxy let through.
func TestTheTestUpstreamIgnoresRequestsThatDidNotComeThroughTheProxy(t *testing.T) {
	up := newUpstream(t)
	resp, err := http.Get(up.server.URL + "/api/tasks")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("stray request: status %d, want 404", resp.StatusCode)
	}
	if up.got != nil {
		t.Fatalf("stray request was recorded as %s", up.got.URL)
	}

	server, endpoint, _ := proxyFor(t, Claude, up, "the-deployments-own-credential")
	_, token, err := endpoint.Mint("run-7", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	if resp := call(t, server, http.MethodPost, "/v1/messages", authenticated(Claude, token), "{}"); resp.StatusCode != http.StatusOK || up.got == nil {
		t.Fatalf("forwarded request: status %d, upstream saw %v", resp.StatusCode, up.got)
	}
	if got := up.got.Header.Get(upstreamMarkerHeader); got != "" {
		t.Errorf("marker header was recorded: %q", got)
	}
}
