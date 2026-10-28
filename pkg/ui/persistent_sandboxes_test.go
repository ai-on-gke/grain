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

package ui_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakePersistentSandboxes is ui.PersistentSandboxes over one upstream
// listener standing in for the service inside a sandbox.
type fakePersistentSandboxes struct {
	upstream string
	strip    bool
	actions  []string
}

func (f *fakePersistentSandboxes) List(context.Context) ([]model.PersistentSandboxStatus, error) {
	return []model.PersistentSandboxStatus{{Name: "web", Repo: "acme/web", State: "running", UISubpath: "/staging/web"}}, nil
}
func (f *fakePersistentSandboxes) Logs(name string) (string, bool) { return "hello log", name == "web" }
func (f *fakePersistentSandboxes) Control(_ context.Context, name, action string) error {
	f.actions = append(f.actions, name+":"+action)
	return nil
}
func (f *fakePersistentSandboxes) Dial(ctx context.Context, name string) (net.Conn, error) {
	if name != "web" {
		return nil, fmt.Errorf("no %s", name)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", f.upstream)
}
func (f *fakePersistentSandboxes) StripPrefix(name string) (bool, bool) {
	return f.strip, name == "web"
}

// stagingUpstream answers plain requests with the path it saw and the
// cookies it was sent, streams /events as server-sent events, and echoes
// a WebSocket-style upgraded connection on /ws.
func stagingUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ws"):
			if r.Header.Get("Upgrade") != "websocket" {
				http.Error(w, "want an upgrade", http.StatusBadRequest)
				return
			}
			conn, rw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
				"Set-Cookie: grain_access=evil; Path=/\r\nSet-Cookie: app=1; Path=/\r\n\r\n")
			rw.Flush()
			line, _ := rw.ReadString('\n')
			fmt.Fprint(rw, "echo:"+line)
			rw.Flush()
		case strings.HasSuffix(r.URL.Path, "/cookies"):
			// A staging app trying to plant cookies on grain's origin,
			// clear grain's site data and loosen the sandbox policy.
			w.Header().Add("Set-Cookie", "grain_access=evil; Path=/")
			w.Header().Add("Set-Cookie", "grain_csrf=evil")
			w.Header().Add("Set-Cookie", "app=1; Path=/; Domain=example.com; HttpOnly")
			w.Header().Add("Set-Cookie", "deep=2; Path=/app/")
			w.Header().Add("Set-Cookie", "bare=3")
			w.Header().Set("Clear-Site-Data", `"*"`)
			w.Header().Set("Content-Security-Policy", "default-src *")
			fmt.Fprint(w, "ok")
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: first\n\n")
			http.NewResponseController(w).Flush()
			<-r.Context().Done()
		default:
			var names []string
			for _, c := range r.Cookies() {
				names = append(names, c.Name)
			}
			fmt.Fprintf(w, "path=%s prefix=%s auth=%q cookies=%s", r.URL.Path, r.Header.Get("X-Forwarded-Prefix"),
				r.Header.Get("Authorization"), strings.Join(names, ","))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stagingServer(t *testing.T, strip bool) (*httptest.Server, *fakePersistentSandboxes) {
	t.Helper()
	_, store, ctx := testClient(t)
	mc := model.DefaultConfig()
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	upstream := stagingUpstream(t)
	fake := &fakePersistentSandboxes{upstream: strings.TrimPrefix(upstream.URL, "http://"), strip: strip}
	srv := httptest.NewServer(ui.NewServer(ui.Config{Actor: ui.DefaultActor("alice"), PersistentSandboxes: fake, LocalAPIToken: testAccessCode}, store))
	t.Cleanup(srv.Close)
	return srv, fake
}

// stagingDo sends method url to a stagingServer with its access code, as
// the browser's access session or the CLI's bearer token would.
func stagingDo(t *testing.T, method, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStagingProxyHTTP(t *testing.T) {
	for _, strip := range []bool{true, false} {
		srv, _ := stagingServer(t, strip)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/staging/web/app/page", nil)
		req.Header.Set("Authorization", "Bearer "+testAccessCode)
		req.AddCookie(&http.Cookie{Name: ui.AccessCookieName, Value: "secret"})
		req.AddCookie(&http.Cookie{Name: "app_session", Value: "keep"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if csp := resp.Header.Values("Content-Security-Policy"); len(csp) != 1 ||
			!strings.HasPrefix(csp[0], "sandbox ") || strings.Contains(csp[0], "allow-same-origin") {
			t.Errorf("Content-Security-Policy = %q, want only a sandbox without allow-same-origin", csp)
		}
		wantPath := "/staging/web/app/page"
		if strip {
			wantPath = "/app/page"
		}
		want := fmt.Sprintf(`path=%s prefix=/staging/web auth="" cookies=app_session`, wantPath)
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Errorf("strip=%v: %d %q, want %q", strip, resp.StatusCode, body, want)
		}
	}
}

// setCookies is resp's Set-Cookie headers, one per line.
func setCookies(resp *http.Response) string {
	return strings.Join(resp.Header.Values("Set-Cookie"), "\n")
}

// TestStagingProxyResponseCookies checks that a staging app, served from
// grain's own origin, cannot set grain's cookies, cannot set its own
// outside its subtree, cannot clear grain's site data, and cannot loosen
// the sandbox policy with one of its own.
func TestStagingProxyResponseCookies(t *testing.T) {
	for _, strip := range []bool{true, false} {
		srv, _ := stagingServer(t, strip)
		resp := stagingDo(t, http.MethodGet, srv.URL+"/staging/web/cookies")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("strip=%v: status = %d", strip, resp.StatusCode)
		}
		deep := "/staging/web/app"
		if !strip {
			deep = "/staging/web"
		}
		want := "app=1; Path=/staging/web; HttpOnly\ndeep=2; Path=" + deep + "\nbare=3; Path=/staging/web"
		if got := setCookies(resp); got != want {
			t.Errorf("strip=%v: Set-Cookie =\n%s\nwant\n%s", strip, got, want)
		}
		if v := resp.Header.Get("Clear-Site-Data"); v != "" {
			t.Errorf("strip=%v: Clear-Site-Data = %q, want it dropped", strip, v)
		}
		csp := resp.Header.Values("Content-Security-Policy")
		if len(csp) != 2 || csp[0] != "default-src *" || !strings.HasPrefix(csp[1], "sandbox ") {
			t.Errorf("strip=%v: Content-Security-Policy = %q, want the app's and the sandbox policy both", strip, csp)
		}
	}
}

func TestStagingProxyUnknownSandbox(t *testing.T) {
	srv, _ := stagingServer(t, true)
	resp := stagingDo(t, http.MethodGet, srv.URL+"/staging/nope/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStagingProxyServerSentEvents(t *testing.T) {
	srv, _ := stagingServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/staging/web/events", nil)
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The upstream never ends the stream, so this only returns if the
	// first event was flushed through rather than buffered.
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Errorf("first line = %q, %v", line, err)
	}
}

func TestStagingProxyWebSocketUpgrade(t *testing.T) {
	srv, _ := stagingServer(t, true)
	u, _ := url.Parse(srv.URL)
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET /staging/web/ws HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", u.Host, testAccessCode)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s, want 101", resp.StatusCode, b)
	}
	if got := setCookies(resp); got != "app=1; Path=/staging/web" {
		t.Errorf("101 Set-Cookie = %q, want only app scoped to /staging/web", got)
	}
	fmt.Fprint(conn, "ping\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "echo:ping\n" {
		t.Errorf("echo = %q, %v", line, err)
	}
}

func TestPersistentSandboxesAPI(t *testing.T) {
	srv, fake := stagingServer(t, true)
	resp := stagingDo(t, http.MethodGet, srv.URL+"/api/persistent-sandboxes")
	var list []model.PersistentSandboxStatus
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "web" {
		t.Errorf("list = %+v", list)
	}
	resp = stagingDo(t, http.MethodPost, srv.URL+"/api/persistent-sandboxes/web/rebuild")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(fake.actions) != 1 || fake.actions[0] != "web:rebuild" {
		t.Errorf("rebuild: %d, actions %v", resp.StatusCode, fake.actions)
	}
	resp = stagingDo(t, http.MethodPost, srv.URL+"/api/persistent-sandboxes/web/explode")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown action: %d, want 400", resp.StatusCode)
	}
}

func TestPersistentSandboxLogsAsText(t *testing.T) {
	srv, _ := stagingServer(t, true)
	resp := stagingDo(t, http.MethodGet, srv.URL+"/api/persistent-sandboxes/web/logs?format=text")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello log" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("logs as text: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

// TestConfigReportsListenPortAndProxy is what the rail's staging entries
// read to tell whether a host port is reachable from the browser: the
// port the request arrived on, and whether a reverse proxy relayed it.
func TestConfigReportsListenPortAndProxy(t *testing.T) {
	srv, _ := stagingServer(t, true)
	u, _ := url.Parse(srv.URL)
	read := func(forwarded bool) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/config", nil)
		req.Header.Set("Authorization", "Bearer "+testAccessCode)
		if forwarded {
			req.Header.Set("X-Forwarded-For", "203.0.113.7")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&got)
		return got
	}
	direct := read(false)
	if fmt.Sprint(direct["listenPort"]) != u.Port() || direct["proxied"] != nil {
		t.Errorf("direct: listenPort %v proxied %v, want %s and absent", direct["listenPort"], direct["proxied"], u.Port())
	}
	if relayed := read(true); relayed["proxied"] != true {
		t.Errorf("relayed: proxied = %v, want true", relayed["proxied"])
	}
}
