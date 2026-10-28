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

package security

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A single-tenant daemon (-tenancy=single, Track 1.1) keeps no accounts.
// What has to hold:
//
//   - it serves no caller a multi-tenant daemon would not, with no more
//     than that daemon would give them. A caller is who holds which
//     credential where: a person's own session gets a request in only
//     where it came from would without it, and then as the deployment's
//     own actor -- what the same caller sending no session already is,
//     on either daemon. Only a caller holding the daemon's access code
//     -- as a bearer token or the access session it signs in to --
//     reaches the API or the UI, from loopback as from anywhere else
//     (grain/task-4343); nothing is trusted for where it comes from;
//   - a person a proxy names -- an identity header, an IAP assertion --
//     is refused outright. The people behind a proxy on loopback cannot
//     send a request without the proxy's header, so running them as the
//     daemon's own actor would make each of them its operator, where a
//     multi-tenant daemon runs each as their own RBAC-limited user;
//   - every handler sees the caller the gate let in: none of them
//     authorizes against, acts as or attributes to the person a session
//     on a loopback request names (requestUser, actingUser);
//   - none of the routes that make an account, sign in to one or set its
//     password does anything, from anywhere.

// singleTenantFixture is a single-tenant daemon's Server: the Server
// has to refuse whoever an identity header names, not promote them.
func singleTenantFixture(t *testing.T) *ui.Server {
	t.Helper()
	srv, _ := loopbackFixture(t, ui.Config{
		Actor:         ui.DefaultActor("operator"),
		LocalAPIToken: testAccessCode,
	})
	return srv
}

// testAccessCode is the single-tenant fixtures' access code.
const testAccessCode = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"

// accessHeader carries code as the grain CLI sends it.
func accessHeader(code string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+code)
	return h
}

// accessSession signs in to srv's "Enter access code" page from from
// with code and returns the access session cookie it hands back.
func accessSession(t *testing.T, srv http.Handler, from, code string) string {
	t.Helper()
	rec := serveForm(srv, from, "/api/auth/access-code", url.Values{"code": {code}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in with the access code from %s: %d, want 303: %s", from, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("signing in with the access code set no %s cookie: %v", ui.AccessCookieName, rec.Header())
	return ""
}

func serveForm(srv http.Handler, from, target string, form url.Values, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.RemoteAddr = from
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, values := range header {
		r.Header[name] = values
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func withAccessCookie(h http.Header, value string) http.Header {
	if h == nil {
		h = http.Header{}
	}
	h.Add("Cookie", (&http.Cookie{Name: ui.AccessCookieName, Value: value}).String())
	return h
}

func TestASingleTenantDaemonIgnoresSessionsFromOutside(t *testing.T) {
	srv := singleTenantFixture(t)
	header := sessionHeader(t, rootUser)
	for _, target := range []string{"/api/tasks", "/api/settings", "/api/deployment"} {
		if rec := serveFrom(srv, publicAddr, http.MethodGet, target, "", header); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s from %s with the system admin's session: %d, want 401: %s", target, publicAddr, rec.Code, rec.Body.String())
		}
	}
}

// Nothing is trusted for where it comes from: loopback without the
// access code is a stranger, and the code is owner access from anywhere
// (grain/task-4343).
func TestASingleTenantDaemonServesOnlyItsAccessCode(t *testing.T) {
	srv := singleTenantFixture(t)
	for _, from := range []string{loopbackAddr, publicAddr} {
		for _, target := range []string{"/api/tasks", "/api/settings", "/api/config", "/api/auth/user"} {
			if rec := serveFrom(srv, from, http.MethodGet, target, "", nil); rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s from %s without the access code: %d, want 401: %s", target, from, rec.Code, rec.Body.String())
			}
			if rec := serveFrom(srv, from, http.MethodGet, target, "", accessHeader("ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4566")); rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s from %s with a wrong access code: %d, want 401: %s", target, from, rec.Code, rec.Body.String())
			}
		}
		// The UI itself is the login page, not the app.
		for _, target := range []string{"/", "/tasks/1", "/index.html", "/assets/index.js"} {
			rec := serveFrom(srv, from, http.MethodGet, target, "", nil)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
				t.Errorf("GET %s from %s without the access code: %d %q, want 401 and the access code page", target, from, rec.Code, rec.Body.String())
			}
		}

		// The code as a bearer token, as the CLI sends it -- in lower
		// case and without its dashes too -- and the session it signs
		// in to, are the owner.
		for name, header := range map[string]http.Header{
			"bearer":          accessHeader(testAccessCode),
			"bearer, retyped": accessHeader(strings.ToLower(strings.ReplaceAll(testAccessCode, "-", ""))),
			"session":         withAccessCookie(nil, accessSession(t, srv, from, testAccessCode)),
		} {
			if rec := serveFrom(srv, from, http.MethodGet, "/api/tasks", "", header); rec.Code != http.StatusOK {
				t.Errorf("GET /api/tasks from %s with the access code (%s): %d, want 200: %s", from, name, rec.Code, rec.Body.String())
			}
			rec := serveFrom(srv, from, http.MethodGet, "/api/auth/user", "", header)
			var resp ui.AuthUserResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding /api/auth/user: %v: %s", err, rec.Body.String())
			}
			if resp.Authenticated || resp.User != "" || resp.AuthMethod != ui.AuthMethodTrusted || resp.Tenancy != "single" {
				t.Errorf("the access code (%s) from %s: /api/auth/user = %+v, want the trusted, unnamed, single-tenant caller", name, from, resp)
			}
		}
	}

	// A session on a request carrying the access code names nobody: the
	// request is the daemon's own, not the person it claims.
	header := withAccessCookie(sessionHeader(t, rootUser), accessSession(t, srv, loopbackAddr, testAccessCode))
	rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/auth/user", "", header)
	var resp ui.AuthUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding /api/auth/user: %v: %s", err, rec.Body.String())
	}
	if resp.Authenticated || resp.User != "" || resp.AuthMethod != ui.AuthMethodTrusted {
		t.Errorf("a session beside the access code: /api/auth/user = %+v, want the trusted, unnamed caller", resp)
	}
}

// No network is trusted on a single-tenant daemon: loopback, IPv6
// loopback and a public address all present the access code.
func TestASingleTenantDaemonTrustsNoNetwork(t *testing.T) {
	srv, _ := loopbackFixture(t, ui.Config{
		Actor:         ui.DefaultActor("operator"),
		LocalAPIToken: testAccessCode,
	})
	for _, from := range []string{loopbackAddr, publicAddr, "[::1]:4242"} {
		if rec := serveFrom(srv, from, http.MethodGet, "/api/tasks", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/tasks from %s without the access code: %d, want 401", from, rec.Code)
		}
	}
	// Forwarding headers beside the code, as the corp web proxy adds:
	// the code is what counts.
	header := accessHeader(testAccessCode)
	header.Set("X-Forwarded-For", "198.51.100.7")
	header.Set("X-Forwarded-Proto", "https")
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", header); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks through a proxy with the access code: %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestASingleTenantDaemonsAccessCookieIsTightlyScoped(t *testing.T) {
	srv := singleTenantFixture(t)
	rec := serveForm(srv, publicAddr, "/api/auth/access-code", url.Values{"code": {testAccessCode}}, http.Header{"X-Forwarded-Proto": {"https"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in: %d %s", rec.Code, rec.Body.String())
	}
	var c *http.Cookie
	for _, got := range rec.Result().Cookies() {
		if got.Name == ui.AccessCookieName {
			c = got
		}
	}
	if c == nil {
		t.Fatalf("no %s cookie: %v", ui.AccessCookieName, rec.Header())
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" {
		t.Errorf("access cookie %+v, want HttpOnly, Secure over https, SameSite=Strict, host-only", c)
	}
	canon := strings.ToUpper(strings.ReplaceAll(testAccessCode, "-", ""))
	if strings.Contains(strings.ToUpper(strings.ReplaceAll(c.Value, "-", "")), canon) || strings.Contains(c.Value, testAccessCode) {
		t.Errorf("the access cookie carries the code itself: %q", c.Value)
	}
	if c.MaxAge != int(ui.DefaultAccessSessionLifetime.Seconds()) {
		t.Errorf("access cookie MaxAge %d, want the default 90 days", c.MaxAge)
	}
}

// Wrong codes are limited per source, and the limit is reached before
// the code is looked at: the right code from a remote source over its
// limit is refused exactly as a wrong one is, so the limit is no oracle.
func TestASingleTenantDaemonRateLimitsWrongAccessCodes(t *testing.T) {
	srv := singleTenantFixture(t)
	limited := false
	for i := 0; i < 50 && !limited; i++ {
		rec := serveForm(srv, publicAddr, "/api/auth/access-code", url.Values{"code": {wrongAccessCode}}, nil)
		switch rec.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			limited = true
		default:
			t.Fatalf("a wrong access code: %d %s", rec.Code, rec.Body.String())
		}
	}
	if !limited {
		t.Fatal("50 wrong access codes from one source were never rate-limited")
	}
	if rec := serveForm(srv, publicAddr, "/api/auth/access-code", url.Values{"code": {testAccessCode}}, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the right code from a source over its limit: %d, want the same 429 a wrong one gets", rec.Code)
	}
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the right bearer code from a source over its limit: %d, want 429", rec.Code)
	}
	// Another source is not held to the first one's limit.
	if rec := serveFrom(srv, otherAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
		t.Errorf("the right bearer code from another source: %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Bearer guesses count the same way.
	srv = singleTenantFixture(t)
	limited = false
	for i := 0; i < 50 && !limited; i++ {
		limited = serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode)).Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("50 wrong bearer access codes from one source were never rate-limited")
	}
}

// wrongAccessCode is shaped like testAccessCode, so it counts as a guess.
const wrongAccessCode = "WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG"

// otherAddr is a remote source that is not publicAddr.
const otherAddr = "198.51.100.200:4242" // TEST-NET-2

// Nobody's wrong codes keep the owner out (grain/task-4430): with the
// global limit spent by guesses from many other sources, the right code
// as a bearer and at the login page still gets in from a source that
// has guessed nothing, and from loopback -- the CLI, every run's `grain
// mcpserver` -- whatever this host's processes have sent. A source
// that guesses while the global limit is spent is held to its own.
func TestASingleTenantDaemonsOwnerIsNotLockedOutByGuesses(t *testing.T) {
	srv := singleTenantFixture(t)
	for i := 0; i < 30; i++ {
		from := fmt.Sprintf("192.0.2.%d:4242", i+1) // TEST-NET-1
		for j := 0; j < 10; j++ {
			serveFrom(srv, from, http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode))
			serveForm(srv, from, "/api/auth/access-code", url.Values{"code": {wrongAccessCode}}, nil)
		}
	}
	if rec := serveFrom(srv, "192.0.2.1:4242", http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the guessing sources were not limited (%d): the global limit was never spent", rec.Code)
	}

	for _, from := range []string{publicAddr, loopbackAddr, "[::1]:4242"} {
		if rec := serveFrom(srv, from, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
			t.Errorf("the right bearer code from %s with the global limit spent: %d, want 200: %s", from, rec.Code, rec.Body.String())
		}
		rec := serveForm(srv, from, "/api/auth/access-code", url.Values{"code": {testAccessCode}}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("signing in with the right code from %s with the global limit spent: %d, want 303: %s", from, rec.Code, rec.Body.String())
		}
	}

	// A source that guesses now is held at once, right code or wrong.
	if rec := serveFrom(srv, otherAddr, http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a wrong code from a fresh source with the global limit spent: %d, want 429", rec.Code)
	}
	if rec := serveFrom(srv, otherAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the right code from a source that just guessed with the global limit spent: %d, want the same 429", rec.Code)
	}
}

// Loopback is every process on this host -- the CLI and each run's
// `grain mcpserver` among them -- so a local account sending wrong codes
// to it is told 429 for them, but never stalls a caller with the right
// one. A proxy on loopback is its callers, and limited like them.
func TestASingleTenantDaemonNeverLimitsTheRightCodeFromLoopback(t *testing.T) {
	srv := singleTenantFixture(t)
	for i := 0; i < 50; i++ {
		serveFrom(srv, loopbackAddr, http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode))
	}
	if rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/tasks", "", accessHeader(wrongAccessCode)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a wrong code from loopback over its limit: %d, want 429", rec.Code)
	}
	if rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
		t.Errorf("the right bearer code from loopback over its limit: %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := serveForm(srv, loopbackAddr, "/api/auth/access-code", url.Values{"code": {testAccessCode}}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("signing in with the right code from loopback over its limit: %d, want 303: %s", rec.Code, rec.Body.String())
	}
	proxied := accessHeader(testAccessCode)
	proxied.Set("X-Forwarded-For", "198.51.100.7")
	if rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/tasks", "", proxied); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the right code relayed by a proxy on loopback over its limit: %d, want 429", rec.Code)
	}
}

// A bearer token that cannot be the access code -- a JWT, an executor
// token -- is some other credential sent to the wrong place, not a guess:
// it is refused, and never counted towards anybody's limit.
func TestASingleTenantDaemonDoesNotCountOtherCredentialsAsGuesses(t *testing.T) {
	srv := singleTenantFixture(t)
	for _, tok := range []string{
		"gxt_0123456789abcdef0123456789abcdef",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl",
		"not-an-access-code",
	} {
		for i := 0; i < 50; i++ {
			if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(tok)); rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET /api/tasks with bearer %q: %d, want 401: %s", tok, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
		t.Errorf("the right code after other credentials: %d, want 200 -- they were counted as guesses: %s", rec.Code, rec.Body.String())
	}
}

// fakeExecutorServer stands in for modelexec.Server: it takes each
// connection the daemon hands it, and says so on it.
type fakeExecutorServer struct{ conns chan struct{} }

func (f *fakeExecutorServer) HandleConn(ctx context.Context, conn net.Conn) {
	f.conns <- struct{}{}
	_, _ = conn.Write([]byte("executor-hello"))
}

func (f *fakeExecutorServer) RevokeToken(string) {}

// A single-tenant daemon accepts remote executors as a multi-tenant one
// does: an executor connects from anywhere without the access code --
// it authenticates with its executor token in its own protocol -- and
// the executor (or IAP) token pkg/modelexec sends as a bearer is not a
// guess at the code.
func TestASingleTenantDaemonAcceptsRemoteExecutors(t *testing.T) {
	exec := &fakeExecutorServer{conns: make(chan struct{}, 64)}
	srv, _ := loopbackFixture(t, ui.Config{
		Actor:          ui.DefaultActor("operator"),
		LocalAPIToken:  testAccessCode,
		ExecutorServer: exec,
	})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = publicAddr
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)

	for i := 0; i < 2*12; i++ {
		path := []string{"/_grain/executor", "/api/executors/connect", "/api/executors/stream"}[i%3]
		conn, err := net.Dial("tcp", remote.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: grain.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"+
			"Authorization: Bearer gxt_0123456789abcdef0123456789abcdef\r\nX-Grain-Executor-Token: gxt_0123456789abcdef0123456789abcdef\r\n\r\n", path)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatalf("connecting an executor to %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("connecting an executor to %s from %s without the access code: %d, want 101", path, publicAddr, resp.StatusCode)
		}
		select {
		case <-exec.conns:
		case <-time.After(5 * time.Second):
			t.Fatalf("connecting an executor to %s: the executor server was never handed the connection", path)
		}
	}
	// None of those bearers was counted against publicAddr.
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
		t.Errorf("the right code from a source that connected executors: %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// And the bootstrap: the install script, from either of its paths.
	for _, target := range []string{"/install-executor.sh", "/api/executors/install.sh"} {
		if rec := serveFrom(srv, publicAddr, http.MethodGet, target, "", accessHeader("gxt_0123456789abcdef0123456789abcdef")); rec.Code != http.StatusOK ||
			!strings.HasPrefix(rec.Body.String(), "#!") {
			t.Errorf("GET %s from %s without the access code: %d, want 200 and the script", target, publicAddr, rec.Code)
		}
	}
}

// Rotating the code (grain access-code rotate) ends every session and
// bearer use of the old one, without a restart.
func TestRotatingTheAccessCodeEndsEverySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), ui.LocalAPITokenFileName)
	old, err := ui.EnsureLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := loopbackFixture(t, ui.Config{
		Actor:             ui.DefaultActor("operator"),
		LocalAPIToken:     old,
		LocalAPITokenFile: path,
	})
	session := accessSession(t, srv, publicAddr, old)
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", withAccessCookie(nil, session)); rec.Code != http.StatusOK {
		t.Fatalf("the session before rotation: %d %s", rec.Code, rec.Body.String())
	}
	rotated, err := ui.RotateLocalAPIToken(path)
	if err != nil || rotated == old {
		t.Fatalf("rotating: %q, %v", rotated, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(old))
		if rec.Code == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old code as a bearer token 5s after rotation: %d, want 401", rec.Code)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rec := serveFrom(srv, publicAddr, http.MethodGet, "/", "", withAccessCookie(nil, session))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
		t.Errorf("the old session after rotation: %d, want the access code page", rec.Code)
	}
	if rec := serveFrom(srv, publicAddr, http.MethodGet, "/api/tasks", "", accessHeader(rotated)); rec.Code != http.StatusOK {
		t.Errorf("the new code after rotation: %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A browser without the access code is told why a single-tenant daemon
// gives it nothing -- it used to get an empty app with nothing to sign
// in to (task 4359). Since the access code (grain/task-4343) that is the
// gate's own 401, naming the code, from loopback and anywhere else
// alike, ahead of /api/config; a caller with the code reads /api/config
// and is told no such thing.
func TestASingleTenantDaemonTellsAnOutsiderWhy(t *testing.T) {
	srv := singleTenantFixture(t)
	for _, from := range []string{publicAddr, loopbackAddr} {
		rec := serveFrom(srv, from, http.MethodGet, "/api/config", "", nil)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "single-tenant") || !strings.Contains(rec.Body.String(), "access code") {
			t.Errorf("GET /api/config from %s without the access code: %d %s, want 401 saying the daemon is single-tenant and needs its access code",
				from, rec.Code, rec.Body.String())
		}
		// The browser itself is shown the "Enter access code" page.
		if rec := serveFrom(srv, from, http.MethodGet, "/", "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
			t.Errorf("GET / from %s without the access code: %d, want 401 and the access code page", from, rec.Code)
		}

		rec = serveFrom(srv, from, http.MethodGet, "/api/config", "", accessHeader(testAccessCode))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config from %s with the access code: %d, want 200: %s", from, rec.Code, rec.Body.String())
		}
		var inside map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &inside); err != nil {
			t.Fatal(err)
		}
		deployment, _ := inside["deployment"].(map[string]any)
		if deployment["id"] != "default" || inside["singleTenantRefusal"] != nil || inside["tenancy"] != nil || inside["teams"] != nil {
			t.Errorf("from %s with the access code: deployment %v, singleTenantRefusal %v, tenancy %v, teams %v; want the deployment, no refusal, and no tenancy or teams",
				from, inside["deployment"], inside["singleTenantRefusal"], inside["tenancy"], inside["teams"])
		}
	}
}

// The escalation review 4330 found: a front proxy on loopback that names its users in an identity header, and
// adds no X-Forwarded-For, made every one of them the daemon's operator.
func TestASingleTenantDaemonRefusesWhoeverAProxyNames(t *testing.T) {
	srv := singleTenantFixture(t)
	for _, h := range ui.PlaintextIdentityHeaders {
		header := http.Header{}
		header.Set(h, memberUser)
		for _, target := range []string{"/api/tasks", "/api/settings", "/api/deployment", "/api/auth/user", "/api/config"} {
			rec := serveFrom(srv, loopbackAddr, http.MethodGet, target, "", header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s from loopback naming %s in %s: %d, want 401: %s", target, memberUser, h, rec.Code, rec.Body.String())
			}
		}
		// The system admin's own name: still nobody.
		root := http.Header{}
		root.Set(h, rootUser)
		if rec := serveFrom(srv, loopbackAddr, http.MethodPut, "/api/settings", "{}", root); rec.Code != http.StatusUnauthorized {
			t.Errorf("PUT /api/settings from loopback naming %s in %s: %d, want 401: %s", rootUser, h, rec.Code, rec.Body.String())
		}
	}

	// What the same proxy's host sends with the access code is the
	// operator's own request, whatever header a proxy adds beside it.
	if rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/tasks", "", accessHeader(testAccessCode)); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks from loopback with the access code: %d, want 200: %s", rec.Code, rec.Body.String())
	}
	for _, h := range ui.PlaintextIdentityHeaders {
		header := withAccessCookie(nil, accessSession(t, srv, loopbackAddr, testAccessCode))
		header.Set(h, rootUser)
		rec := serveFrom(srv, loopbackAddr, http.MethodGet, "/api/auth/user", "", header)
		var resp ui.AuthUserResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding /api/auth/user: %v: %s", err, rec.Body.String())
		}
		if resp.User != "" {
			t.Errorf("the access code with %s naming %s: /api/auth/user = %+v, want nobody named", h, rootUser, resp)
		}
	}
}

// What a handler learns of its caller (requestUser, actingUser, the
// change-password fallback) is what the gate let in. On a single-tenant
// daemon that is the deployment's own actor for every loopback request,
// whoever its session names, so every route answers a loopback request
// carrying a member's or a stranger's session exactly as it answers one
// carrying none. A handler that read the session past the gate would
// refuse the member a team-admin route the unnamed request is allowed,
// or file what it makes under their name.
func TestASingleTenantDaemonsHandlersSeeTheCallerItsGateLetIn(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	const stranger = "mallory@example.com"
	shared := newRouteFixtureFor(t, true)
	for _, pattern := range shared.server.Routes() {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			fixture := func() *routeFixture {
				if strings.HasPrefix(pattern, http.MethodGet+" ") {
					return shared
				}
				return newRouteFixtureFor(t, true)
			}
			want := fixture().do(t, pattern, "")
			for _, user := range []string{memberUser, stranger, rootUser} {
				if got := fixture().do(t, pattern, user); got.code != want.code {
					t.Errorf("%s from loopback with %s's session: %d %s; with no session: %d %s -- want the same answer",
						pattern, user, got.code, got.excerpt(), want.code, want.excerpt())
				}
			}
			// Nor does anything outside get in, session or not -- but
			// remote execution's own routes, which authenticate in their
			// own protocol (TestASingleTenantDaemonAcceptsRemoteExecutors).
			if _, path, _ := strings.Cut(pattern, " "); strings.HasPrefix(path, "/api/") && path != "/api/config" && !strings.HasPrefix(path, "/api/auth/") &&
				singleTenantOpenRoutes[pattern] == "" {
				outside := *shared
				outside.from = publicAddr
				outside.accessCookie = ""
				for _, user := range []string{"", memberUser, rootUser} {
					if got := outside.do(t, pattern, user); got.code != http.StatusUnauthorized && got.code != http.StatusNotFound {
						t.Errorf("%s from %s as %q: %d %s, want 401", pattern, publicAddr, user, got.code, got.excerpt())
					}
				}
			}
		})
	}
}

// The attribution half of the sweep above: what a loopback request
// carrying a session makes is the daemon's, not the person's.
func TestASingleTenantDaemonAttributesToItsOwnActor(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	f := newRouteFixtureFor(t, true)
	rec := f.send(t, http.MethodPost, "/api/tasks/"+f.taskID+"/comments", `{"body":"hello","author":"`+rootUser+`"}`, memberUser)
	if rec.code/100 != 2 {
		t.Fatalf("commenting from loopback with %s's session: %d %s", memberUser, rec.code, rec.excerpt())
	}
	comments, err := f.store.Comments(context.Background(), f.taskID)
	if err != nil || len(comments) == 0 {
		t.Fatalf("reading the comment back: %v, %d comments", err, len(comments))
	}
	if by := comments[len(comments)-1].Author.Actor; by.ID != "operator" {
		t.Errorf("a comment from loopback with %s's session was attributed to %+v, want the daemon's own actor", memberUser, by)
	}

}

func TestASingleTenantDaemonHasNoAccountRoutes(t *testing.T) {
	srv := singleTenantFixture(t)
	routes := []struct{ method, target, body string }{
		{http.MethodPost, "/api/auth/login", `{"username":"alice","password":"correct horse"}`},
		{http.MethodPost, "/api/auth/register", `{"username":"alice","password":"correct horse"}`},
		{http.MethodPost, "/api/auth/change-password", `{"password":"correct horse"}`},
		{http.MethodGet, "/api/auth/google/login", ""},
		{http.MethodGet, "/api/auth/google/callback?code=x&state=y", ""},
		{http.MethodGet, "/api/users", ""},
		{http.MethodPost, "/api/users", `{"username":"alice","password":"correct horse"}`},
		{http.MethodDelete, "/api/users/alice", ""},
		{http.MethodPut, "/api/users/alice/password", `{"password":"correct horse"}`},
		{http.MethodPost, "/api/users/alice/password", `{"password":"correct horse"}`},
	}
	for _, rt := range routes {
		// grain has none of them any more (docs/single-tenant-reset.md,
		// stage 3d): the code's holder hears there is no such route...
		if rec := serveFrom(srv, loopbackAddr, rt.method, rt.target, rt.body, accessHeader(testAccessCode)); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s from %s with the code: %d, want 404: %s", rt.method, rt.target, loopbackAddr, rec.Code, rec.Body.String())
		}
		// ...and anybody else is refused at the gate, as on every route.
		for _, from := range []string{loopbackAddr, publicAddr} {
			if rec := serveFrom(srv, from, rt.method, rt.target, rt.body, nil); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound {
				t.Errorf("%s %s from %s: %d, want 401: %s", rt.method, rt.target, from, rec.Code, rec.Body.String())
			}
		}
	}
}

// namedTeam is a team a single-tenant caller names in its request, and
// one the daemon does not keep. A single-tenant daemon's teams are labels,
// not tenants: none of them is somebody the caller has to belong to, so
// no refusal it gives is about a team.
const namedTeam = "single-tenant-named-team"

// singleTenantStateRefusals are routes that refuse the fixture's task
// for what state it is in rather than for who asks, with the words that
// say so: a run's database is only open while the run is live, to the
// daemon's own actor as to anybody.
//
// Signing in with the access code is the other: it judges the code in
// its body, which the sweep's probe body does not carry, whatever
// credential the request already has.
var singleTenantStateRefusals = map[string]string{
	"POST /api/tasks/{id}/db/query":   "only for a live run",
	"POST /api/tasks/{id}/db/execute": "only for a live run",
	"POST /api/auth/access-code":      "access code was not accepted",
}

// singleTenantOpenRoutes are the public routes a single-tenant daemon
// serves without its access code, and why: the "Enter access code"
// page's own, logging out, and remote execution's, which authenticate
// in their own protocol (pkg/ui singleTenantOpenRoute). Every other
// public route is in singleTenantGatedPublicRoutes.
// TestRouteAuthorizationSingleTenant holds every public route to one of
// the two, so a gate change cannot
// quietly close an open one, nor a new public route arrive open.
var singleTenantOpenRoutes = map[string]string{
	"GET /api/auth/access-code":          "the login page asks whether it holds a session",
	"POST /api/auth/access-code":         "signing in with the code",
	"GET /api/auth/access-code/login.js": "the login page's script",
	"POST /api/auth/logout":              "signing out",
	"GET /api/auth/logout":               "signing out",
	"GET /api/executors/install.sh":      "remote execution",
	"GET /install-executor.sh":           "remote execution",
	"GET /api/executors/bin/{binary}":    "remote execution",
	"GET /install/{os}/{arch}":           "remote execution",
	"GET /_grain/executor":               "remote execution",
	"GET /api/executors/connect":         "remote execution",
	"GET /api/executors/stream":          "remote execution",
	"GET /api/hostd/connect":             "a grain hostd dialing in, with the hostd token",
}

// singleTenantGatedPublicRoutes are the public routes a single-tenant
// daemon serves only with its access code: what a multi-tenant daemon
// shows a stranger so they can sign in, where a single-tenant one shows
// the "Enter access code" page instead.
var singleTenantGatedPublicRoutes = map[string]bool{
	"/":                    true,
	"GET /api/config":      true,
	"GET /api/auth/user":   true,
	"GET /api/auth/status": true,
}

// executorBearer is the bearer pkg/modelexec sends on an executor's
// connection: its executor token.
const executorBearer = "gxt_0123456789abcdef0123456789abcdef"

// TestRouteAuthorizationSingleTenant sweeps routeAuthorization against a
// single-tenant server (newRouteFixtureFor),
// whose actor is the system admin a new installation seeds. TestRouteAuthorization holds the multi-tenant daemon to
// the same table; here every route is reachable by exactly the callers
// holding the daemon's access code, whatever else the request presents:
//
//   - every non-public route refuses, with 401 or 403, a request from
//     publicAddr carrying the session a multi-tenant build signed for
//     the system admin (oldSessionToken);
//   - every non-public route refuses loopback without the access code
//     (grain/task-4343): where a request comes from is no credential;
//   - every route answers loopback carrying the access code, not
//     refusing it, both with nothing else and when the request also
//     carries a plain member's old session: it names nobody, so a
//     team-admin or system-admin route that refused the member would
//     have let it count;
//   - no refusal, from either address, mentions namedTeam, which every
//     request names in its query string and its body: whether a
//     single-tenant daemon serves a request is never a team's to say.
//
// There is no baseline: a single-tenant gap fails outright.
func TestRouteAuthorizationSingleTenant(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	shared := newRouteFixtureFor(t, true)
	for _, pattern := range shared.server.Routes() {
		level, ok := routeAuthorization[pattern]
		if !ok {
			// TestRouteAuthorization fails on it.
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			// fixture is shared for a GET and a fresh one for anything
			// else, as in probeRoute, asked from the given address.
			fixture := func(from string) *routeFixture {
				f := shared
				if !strings.HasPrefix(pattern, http.MethodGet+" ") {
					f = newRouteFixtureFor(t, true)
				}
				return &routeFixture{server: f.server, taskID: f.taskID, from: from}
			}
			// withCode is fixture, its requests carrying the access code's
			// session as every caller a single-tenant daemon serves does
			// (grain/task-4343).
			withCode := func(from string) *routeFixture {
				f := fixture(from)
				f.accessCookie = accessSession(t, f.server, from, testAccessCode)
				return f
			}
			method, _, ok := strings.Cut(pattern, " ")
			if !ok {
				method = http.MethodGet
			}
			body := ""
			if method != http.MethodGet {
				body = `{"team":"` + namedTeam + `"}`
				if b, ok := probeBodies[pattern]; ok {
					body = strings.Replace(b, `"team":"`+alphaTeam+`"`, `"team":"`+namedTeam+`"`, 1)
				}
			}
			target := func(f *routeFixture) string {
				return f.path(pattern) + "?team=" + namedTeam + "&user=" + url.QueryEscape(rootUser)
			}
			mentions := func(from string, rec *statusRecorder) {
				if refused(rec.code) && strings.Contains(rec.body.String(), namedTeam) {
					t.Errorf("%s from %s: refused %d %s, naming the team %q the caller named",
						pattern, from, rec.code, rec.excerpt(), namedTeam)
				}
			}

			_, open := singleTenantOpenRoutes[pattern]
			if level == public && !open && !singleTenantGatedPublicRoutes[pattern] {
				t.Errorf("%s is public, and neither singleTenantOpenRoutes nor singleTenantGatedPublicRoutes "+
					"says what a single-tenant daemon does with it: add it to one", pattern)
			}
			// An open route answers anybody without the code, the gate
			// letting it through to its handler, and counts no bearer on
			// it as a guess at the code: an executor's own, or one shaped
			// like the code, sent past the limit, is never refused 429.
			if open {
				// A fresh server each, so no limit is shared with
				// another route's probes.
				fresh := newRouteFixtureFor(t, true)
				f := &routeFixture{server: fresh.server, taskID: fresh.taskID, from: otherAddr}
				for _, bearer := range []string{"", executorBearer, wrongAccessCode} {
					for i := 0; i < 15; i++ {
						header := http.Header{}
						if bearer != "" {
							header.Set("Authorization", "Bearer "+bearer)
						}
						rec := f.serve(t, method, target(f), body, header)
						got := rec.body.String()
						if rec.code == http.StatusTooManyRequests || strings.Contains(got, "every caller shows its access code") ||
							strings.Contains(got, "Enter access code") || strings.Contains(got, "only its access code reaches its API") {
							t.Errorf("%s (open: %s) from %s without the access code, bearer %q, attempt %d: %d %s, want its own handler's answer",
								pattern, singleTenantOpenRoutes[pattern], otherAddr, bearer, i+1, rec.code, rec.excerpt())
							break
						}
					}
				}
			}

			// A gated public route is refused without the code, as a
			// non-public one is, below.
			if singleTenantGatedPublicRoutes[pattern] {
				for _, from := range []string{publicAddr, loopbackAddr} {
					f := fixture(from)
					if rec := f.send(t, method, target(f), body, ""); !refused(rec.code) {
						t.Errorf("%s (public, gated): answered %d %s from %s without the access code, want 401",
							pattern, rec.code, rec.excerpt(), from)
					}
				}
			}

			if level != public {
				f := fixture(publicAddr)
				header := f.sessionHeader(t, rootUser)
				rec := f.serve(t, method, target(f), body, header)
				if !refused(rec.code) {
					t.Errorf("%s (%s): answered %d %s to %s from %s with the system admin's old session, want 401 or 403",
						pattern, level, rec.code, rec.excerpt(), rootUser, publicAddr)
				}
				mentions(publicAddr, rec)
			}

			// Loopback is no credential: without the code it is refused
			// like anywhere else.
			if level != public {
				f := fixture(loopbackAddr)
				if rec := f.send(t, method, target(f), body, ""); !refused(rec.code) {
					t.Errorf("%s (%s): answered %d %s from %s without the access code, want 401 or 403",
						pattern, level, rec.code, rec.excerpt(), loopbackAddr)
				}
			}

			for _, user := range []string{"", memberUser} {
				f := withCode(loopbackAddr)
				rec := f.send(t, method, target(f), body, user)
				as := "with the access code and no other credential"
				if user != "" {
					as = "with the access code and " + user + "'s session"
				}
				if why, ok := singleTenantStateRefusals[pattern]; ok && strings.Contains(rec.body.String(), why) {
					continue
				}
				if refused(rec.code) {
					t.Errorf("%s (%s): answered %d %s from %s %s, want it served as the daemon's actor",
						pattern, level, rec.code, rec.excerpt(), loopbackAddr, as)
				}
				mentions(loopbackAddr, rec)
			}
		})
	}
}

// carriedOverSingleTenant is a single-tenant daemon's Server built as
// startUIServer builds it, over a store a multi-tenant build left
// behind: its access code is the local API token in its data
// directory, its store is marked single-tenant (daemon run.go), and the
// store still holds a system admin and teams with members.
func carriedOverSingleTenant(t *testing.T) (*routeFixture, string) {
	t.Helper()
	tokenFile := ui.LocalAPITokenPath(t.TempDir())
	code, err := ui.EnsureLocalAPIToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	srv, store := loopbackFixture(t, ui.Config{
		LocalAPIToken:     code,
		LocalAPITokenFile: tokenFile,
	})
	store.SingleTenant = true
	return &routeFixture{server: srv, store: store, taskID: "1"}, tokenFile
}

// leftoverUser is an account a multi-tenant build made.
const leftoverUser = "leftover@example.com"

// A route reaches a single-tenant daemon over its own HTTP listener,
// from this host or from anywhere.
var singleTenantTransports = []struct {
	name       string
	remoteAddr string
}{
	{name: "loopback", remoteAddr: loopbackAddr},
	{name: "loopback (IPv6)", remoteAddr: "[::1]:4242"},
	{name: "the network", remoteAddr: publicAddr},
}

// serveOver is routeFixture.serve over one of singleTenantTransports.
func serveOver(t *testing.T, srv http.Handler, remoteAddr, method, target, body string, header http.Header) *statusRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = remoteAddr
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, values := range header {
		r.Header[name] = values
	}
	return serveUntilAnswered(t, srv, r)
}

// sweepAsAnonymous holds every route of a carried-over single-tenant
// daemon, over every transport, to one rule: a request carrying any of
// credentials is answered exactly as the same request carrying nothing.
// Off the open routes that answer is a refusal (an account route's, 404);
// on them it is the handler's own, unmoved by the credential, and never
// a 429 that would say it was counted as a guess at the access code.
func sweepAsAnonymous(t *testing.T, credentials map[string]http.Header) {
	shared, _ := carriedOverSingleTenant(t)
	for _, pattern := range shared.server.Routes() {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			method, _, ok := strings.Cut(pattern, " ")
			if !ok {
				method = http.MethodGet
			}
			body := ""
			if method != http.MethodGet {
				body = "{}"
				if b, ok := probeBodies[pattern]; ok {
					body = b
				}
			}
			target := shared.path(pattern) + "?user=" + url.QueryEscape(rootUser)
			_, open := singleTenantOpenRoutes[pattern]
			for _, over := range singleTenantTransports {
				f := shared
				if open {
					// Its own server: an open route may count the
					// requests it is sent, and another route's, or
					// another transport's, must not move the answer.
					f, _ = carriedOverSingleTenant(t)
				}
				anon := serveOver(t, f.server, over.remoteAddr, method, target, body, nil)
				for name, header := range credentials {
					got := serveOver(t, f.server, over.remoteAddr, method, target, body, header)
					if got.code != anon.code {
						t.Errorf("%s over %s with %s: %d %s; with nothing: %d %s -- want the anonymous answer",
							pattern, over.name, name, got.code, got.excerpt(), anon.code, anon.excerpt())
					}
					switch {
					case got.code == http.StatusTooManyRequests:
						t.Errorf("%s over %s with %s: 429 -- counted as a guess at the access code", pattern, over.name, name)
					case open:
					case !refused(got.code):
						t.Errorf("%s over %s with %s: %d %s, want it refused", pattern, over.name, name, got.code, got.excerpt())
					}
				}
			}
		})
	}
}

// Loopback and the CLI need no sign-in (part 3 of task 4193's plan): the
// grain CLI on the daemon's host, with nothing but the access code file
// the daemon wrote to its data directory, lists, files and reads tasks
// over a real loopback listener -- no login, no access session, no
// cookie. Without the file the same client is refused, so it is the
// file that lets it in, not where it calls from (grain/task-4343).
func TestASingleTenantDaemonServesItsOwnHostsCLIWithNoSignIn(t *testing.T) {
	f, tokenFile := carriedOverSingleTenant(t)
	listener := httptest.NewServer(f.server)
	t.Cleanup(listener.Close)
	if host, _, _ := net.SplitHostPort(listener.Listener.Addr().String()); !net.ParseIP(host).IsLoopback() {
		t.Fatalf("the listener is on %s, not loopback", host)
	}
	ctx := context.Background()

	t.Setenv(ui.LocalAPITokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	t.Setenv("GRAIN_DATA_DIR", "")
	if _, err := ui.NewHTTPClient(listener.URL).ListTasks(ctx); err == nil {
		t.Fatalf("the CLI without the access code file listed tasks: want it refused")
	}

	t.Setenv(ui.LocalAPITokenFileEnv, tokenFile)
	cli := ui.NewHTTPClient(listener.URL)
	created, err := cli.CreateTask(ctx, ui.CreateTaskRequest{Title: "from the CLI", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("filing a task from the CLI with no sign-in: %v", err)
	}
	if _, err := cli.GetTask(ctx, created.ID); err != nil {
		t.Errorf("reading task %s from the CLI: %v", created.ID, err)
	}
	tasks, err := cli.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks from the CLI: %v", err)
	}
	found := false
	for _, task := range tasks {
		found = found || task.ID == created.ID
	}
	if !found {
		t.Errorf("the CLI's task %s is not in the CLI's own listing", created.ID)
	}
	if err := cli.AddComment(ctx, created.ID, "hello", nil); err != nil {
		t.Errorf("commenting from the CLI: %v", err)
	}

	// The browser on the daemon's host is the same: its first visit
	// is the access code page, and the code is all it needs to get in,
	// with no account to sign in to.
	f2, _ := carriedOverSingleTenant(t)
	if rec := serveFrom(f2.server, loopbackAddr, http.MethodGet, "/", "", nil); !strings.Contains(rec.Body.String(), "Enter access code") {
		t.Errorf("GET / from loopback before the code: %d, want the access code page", rec.Code)
	}
	code, err := ui.EnsureLocalAPIToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	session := withAccessCookie(nil, accessSession(t, f.server, loopbackAddr, code))
	if rec := serveFrom(f.server, loopbackAddr, http.MethodGet, "/api/tasks", "", session); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks from loopback with the access session: %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// oldSessionToken is a session token as a multi-tenant build minted one
// at sign-in: an HS256 JWT naming user, under the session secret that
// build kept. grain signs and verifies no sessions any more
// (docs/single-tenant-reset.md, stage 3d), but a browser that signed in
// before the upgrade still carries one.
func oldSessionToken(t *testing.T, user string) string {
	t.Helper()
	return signSession(t, jwt.SigningMethodHS256, []byte("route-authorization-session-secret-0123456789"), user, time.Now().Add(time.Hour))
}

// oldSession is oldSessionToken in the cookie that build kept it in.
func oldSession(t *testing.T, user string) http.Header {
	t.Helper()
	h := http.Header{}
	h.Add("Cookie", (&http.Cookie{Name: "grain_session", Value: oldSessionToken(t, user)}).String())
	return h
}

// Sessions a multi-tenant build signed in, replayed on the single-tenant
// daemon the same installation became, get nothing on any route over
// any transport: not the system admin's, not a
// member's, not the leftover account's.
func TestASingleTenantDaemonRefusesOldSessionsOnEveryRoute(t *testing.T) {
	credentials := map[string]http.Header{}
	for _, user := range []string{rootUser, memberUser, leftoverUser} {
		credentials[user+"'s session cookie"] = oldSession(t, user)
	}
	sweepAsAnonymous(t, credentials)
}

// Beside the access code, an old session names nobody and scopes
// nothing: the caller is the daemon's own unnamed actor, and what it is
// shown of teams is what the code alone is shown.
func TestASingleTenantDaemonsOldSessionsNameNobodyBesideTheCode(t *testing.T) {
	f, tokenFile := carriedOverSingleTenant(t)
	code, err := ui.EnsureLocalAPIToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	withCode := func(h http.Header) http.Header {
		h.Set("Authorization", "Bearer "+code)
		return h
	}
	want := serveFrom(f.server, loopbackAddr, http.MethodGet, "/api/deployment", "", withCode(http.Header{}))
	for _, user := range []string{rootUser, memberUser, leftoverUser, "mallory@example.com"} {
		rec := serveFrom(f.server, loopbackAddr, http.MethodGet, "/api/auth/user", "", withCode(oldSession(t, user)))
		var resp ui.AuthUserResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding /api/auth/user: %v: %s", err, rec.Body.String())
		}
		if resp.Authenticated || resp.User != "" || resp.AuthMethod != ui.AuthMethodTrusted {
			t.Errorf("the code beside %s's old session cookie: /api/auth/user = %+v, want the trusted, unnamed caller", user, resp)
		}
		if got := serveFrom(f.server, loopbackAddr, http.MethodGet, "/api/deployment", "", withCode(oldSession(t, user))); got.Code != want.Code || got.Body.String() != want.Body.String() {
			t.Errorf("GET /api/deployment with the code and %s's old session cookie: %d %s; with the code alone: %d %s -- want the same",
				user, got.Code, got.Body.String(), want.Code, want.Body.String())
		}
	}
}

// A single-tenant daemon offers nobody, its own access code included, a
// multi-user surface: no person to be, whatever a request beside the
// code carries. The team members a multi-tenant build left behind are
// listed only to the code's holder; every other caller, whatever it
// carries, is refused them (the sweeps above).
func TestASingleTenantDaemonOffersNoMultiUserSurface(t *testing.T) {
	f, tokenFile := carriedOverSingleTenant(t)
	code, err := ui.EnsureLocalAPIToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string]http.Header{
		"the code":                         accessHeader(code),
		"the access session and a session": withAccessCookie(oldSession(t, rootUser), accessSession(t, f.server, loopbackAddr, code)),
	} {
		for _, target := range []string{"/api/auth/user", "/api/auth/status"} {
			rec := serveFrom(f.server, loopbackAddr, http.MethodGet, target, "", header)
			var resp ui.AuthUserResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding %s: %v: %s", target, err, rec.Body.String())
			}
			if resp.Tenancy != "single" || resp.Authenticated || resp.User != "" {
				t.Errorf("GET %s with %s: %+v, want a single-tenant caller nobody signed in, offered no sign-in", target, name, resp)
			}
		}
		rec := serveFrom(f.server, loopbackAddr, http.MethodGet, "/api/config", "", header)
		var config struct {
			Authenticated bool            `json:"authenticated"`
			Tenancy       *string         `json:"tenancy"`
			Teams         json.RawMessage `json:"teams"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
			t.Fatalf("decoding /api/config: %v: %s", err, rec.Body.String())
		}
		// tenancy and teams went from /api/config with the reset's
		// residue (task 4924): the one value either could hold.
		if config.Tenancy != nil || config.Teams != nil || config.Authenticated {
			t.Errorf("GET /api/config with %s: %+v, want a single-tenant daemon offering no sign-in or registration", name, config)
		}
	}
}

// The deployment's own routes (/api/deployment, stage 3e-iv of the
// single-tenant reset) are where its git connections, default model,
// model connections, reachable repos and per-repo overrides are
// configured. Each is the owner's: open to the access code, and refused
// to a caller without it from loopback or anywhere else.
func TestASingleTenantDaemonServesItsDeploymentRoutesOnlyToItsOwner(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	var patterns []string
	for _, pattern := range newRouteFixtureFor(t, true).server.Routes() {
		if _, path, ok := strings.Cut(pattern, " "); ok && (path == "/api/deployment" || strings.HasPrefix(path, "/api/deployment/")) {
			patterns = append(patterns, pattern)
		}
	}
	if len(patterns) == 0 {
		t.Fatal("the server registers no /api/deployment routes")
	}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			method, _, _ := strings.Cut(pattern, " ")
			body := ""
			if method != http.MethodGet {
				body = "{}"
			}
			f := newRouteFixtureFor(t, true)
			target := f.path(pattern)
			for _, from := range []string{loopbackAddr, publicAddr} {
				if rec := serveFrom(f.server, from, method, target, body, nil); !refused(rec.Code) {
					t.Errorf("%s from %s without the access code: %d %s, want 401 or 403", pattern, from, rec.Code, rec.Body.String())
				}
			}
			if rec := serveFrom(f.server, loopbackAddr, method, target, body, accessHeader(testAccessCode)); refused(rec.Code) {
				t.Errorf("%s with the access code: %d %s, want it served", pattern, rec.Code, rec.Body.String())
			}
		})
	}
}
