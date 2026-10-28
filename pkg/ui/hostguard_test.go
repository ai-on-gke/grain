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

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostGuardRefusesRebindingHosts(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	g := HostGuard(ok, "127.0.0.1:8420", []string{"grain.example.com", "*.corp.example"})

	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:8420", http.StatusOK},
		{"[::1]:8420", http.StatusOK},
		{"10.0.0.5", http.StatusOK},
		{"169.254.169.254", http.StatusOK},
		{"localhost:8420", http.StatusOK},
		{"LocalHost.", http.StatusOK},
		{"app.localhost", http.StatusOK},
		{"node.tailnet-abc.ts.net", http.StatusOK},
		{"grain.example.com", http.StatusOK},
		{"Grain.Example.com:443", http.StatusOK},
		{"a.b.corp.example", http.StatusOK},
		{"evil-attacker-controlled.example:9999", http.StatusMisdirectedRequest},
		{"example.com", http.StatusMisdirectedRequest},
		{"grain.example.com.evil.example", http.StatusMisdirectedRequest},
		{"localhost.evil.example", http.StatusMisdirectedRequest},
		{"corp.example", http.StatusMisdirectedRequest},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %q: got %d, want %d", c.host, rec.Code, c.want)
		}
	}
}

func TestHostGuardEnforcement(t *testing.T) {
	cases := []struct {
		addr    string
		allowed []string
		want    bool
	}{
		{"127.0.0.1:8420", nil, true},
		{"10.0.0.5:80", nil, true},
		{"0.0.0.0:8080", nil, false},
		{":8080", nil, false},
		{"[::]:8080", nil, false},
		{"0.0.0.0:8080", []string{"grain.example.com"}, true},
		{"0.0.0.0:8080", []string{" ", ""}, false},
		{"127.0.0.1:8420", []string{"*"}, false},
	}
	for _, c := range cases {
		if got := HostGuardEnforced(c.addr, c.allowed); got != c.want {
			t.Errorf("HostGuardEnforced(%q, %q) = %v, want %v", c.addr, c.allowed, got, c.want)
		}
	}
}

func TestHostGuardAllowsListenNameAndHostname(t *testing.T) {
	g, enforce := newHostGuard("grain-box:8420", nil, []string{"build-7.us-central1.internal", "build-7"})
	if !enforce {
		t.Fatal("a named listen address should be enforced")
	}
	for _, h := range []string{"grain-box:8420", "build-7", "build-7.us-central1.internal:80"} {
		if !g.allows(h) {
			t.Errorf("allows(%q) = false, want true", h)
		}
	}
	if g.allows("build-8") {
		t.Error("allows(\"build-8\") = true, want false")
	}
}

// The installers' default bind is every interface, with -ui-allowed-hosts
// defaulted to the machine's own names (`hostname -f`, `hostname -s`).
// Under that list a Host naming anything else is refused, from loopback
// and from the network alike.
func TestHostGuardRefusesUnexpectedHostUnderInstallerDefaults(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	g := HostGuard(ok, "0.0.0.0:4242", []string{"grainhost.corp.example", "grainhost"})
	for _, c := range []struct {
		remote, host string
		want         int
	}{
		{"127.0.0.1:50000", "grainhost.corp.example:4242", http.StatusOK},
		{"10.1.2.3:50000", "grainhost:4242", http.StatusOK},
		{"10.1.2.3:50000", "10.1.2.4:4242", http.StatusOK},
		{"127.0.0.1:50000", "localhost:4242", http.StatusOK},
		{"127.0.0.1:50000", "rebound.attacker.example:4242", http.StatusMisdirectedRequest},
		{"10.1.2.3:50000", "rebound.attacker.example:4242", http.StatusMisdirectedRequest},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.RemoteAddr, req.Host = c.remote, c.host
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %q from %s: got %d, want %d", c.host, c.remote, rec.Code, c.want)
		}
	}
}

// With every interface bound and no -ui-allowed-hosts, the requests
// loopback trust would run as the operator are still checked: a page
// rebound to 127.0.0.1 is refused, while a network client -- which has to
// sign in anyway -- is served under whatever name it used.
func TestHostGuardChecksLoopbackOnAWildcardListener(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, c := range []struct {
		allowed      []string
		remote, host string
		want         int
	}{
		{nil, "127.0.0.1:50000", "rebound.attacker.example:4242", http.StatusMisdirectedRequest},
		{nil, "[::1]:50000", "rebound.attacker.example:4242", http.StatusMisdirectedRequest},
		{nil, "127.0.0.1:50000", "localhost:4242", http.StatusOK},
		{nil, "127.0.0.1:50000", "127.0.0.1:4242", http.StatusOK},
		{nil, "10.1.2.3:50000", "grain.some-ingress.example", http.StatusOK},
		{[]string{"*"}, "127.0.0.1:50000", "rebound.attacker.example:4242", http.StatusOK},
	} {
		g := HostGuard(ok, "0.0.0.0:4242", c.allowed)
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.RemoteAddr, req.Host = c.remote, c.host
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("allowed %v, Host %q from %s: got %d, want %d", c.allowed, c.host, c.remote, rec.Code, c.want)
		}
	}
}

// exampleProxyHosts is a made-up HostFamily: a workstation web proxy that
// reaches the daemon as <encoded-id>.proxy.example.test, admitted by a
// workstation installer of its own.
const exampleProxyHosts = "*.proxy.example.test"

func registerExampleProxy(t *testing.T) {
	t.Helper()
	t.Cleanup(RegisterHostFamily(HostFamily{
		Pattern: exampleProxyHosts,
		Advice: func(rerunHosts string) string {
			return "it is a workstation proxy URL, so re-run GRAIN_UI_ALLOWED_HOSTS='" + rerunHosts + "' workstation-install.sh"
		},
	}))
}

// A registered family's wildcard entry admits the long encoded name, from
// loopback and the network alike, and nothing else under its parent.
func TestHostGuardAdmitsAHostFamilyByItsPattern(t *testing.T) {
	registerExampleProxy(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	const proxied = "b2607f8b048001000010f53f2ac1308461092000000000000000001.proxy.example.test"
	g := HostGuard(ok, "0.0.0.0:4242", []string{"me.example.test", "me", exampleProxyHosts})
	for _, c := range []struct {
		remote, host string
		want         int
	}{
		{"10.1.2.3:50000", proxied, http.StatusOK},
		{"10.1.2.3:50000", proxied + ":443", http.StatusOK},
		{"127.0.0.1:50000", proxied, http.StatusOK},
		{"10.1.2.3:50000", "proxy.example.test", http.StatusMisdirectedRequest},
		{"10.1.2.3:50000", "evil.example.test", http.StatusMisdirectedRequest},
		{"10.1.2.3:50000", "x.proxy.example.test.evil.example", http.StatusMisdirectedRequest},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.RemoteAddr, req.Host = c.remote, c.host
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %q from %s: got %d, want %d", c.host, c.remote, rec.Code, c.want)
		}
	}
}

// Refusing a name from a registered family says what the family's own
// advice says, given the hosts already allowed plus its pattern.
func TestHostGuardRefusalUsesTheHostFamilysAdvice(t *testing.T) {
	registerExampleProxy(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	const proxied = "b2607f8b048001000010f53f2ac1308461092000000000000000001.proxy.example.test"
	g := HostGuard(ok, "0.0.0.0:4242", []string{"me.example.test", "me"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr, req.Host = "10.1.2.3:50000", proxied
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("got %d, want 421", rec.Code)
	}
	if want := "re-run GRAIN_UI_ALLOWED_HOSTS='me.example.test,me,*.proxy.example.test' workstation-install.sh"; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("the refusal does not say %q: %s", want, rec.Body.String())
	}

	// Any other name gets the setup.sh re-run, keeping what is already
	// allowed since the variable replaces the list, and the flag.
	req.Host = "Grain.Elsewhere.example:4242"
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "workstation-install") {
		t.Errorf("a non-family refusal names the family's installer: %s", body)
	}
	for _, want := range []string{
		"sudo GRAIN_UI_ALLOWED_HOSTS='me.example.test,me,grain.elsewhere.example' scripts/setup.sh",
		"-ui-allowed-hosts naming it",
		"this machine's own names are always answered",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q: %s", want, body)
		}
	}

	// A Host that is not a plain name is not pasted into a command.
	gp, _ := newHostGuard("127.0.0.1:4242", nil, nil)
	if got := gp.misdirected("evil'$(id).example").Error(); strings.Contains(got, "GRAIN_UI_ALLOWED_HOSTS='") || !strings.Contains(got, "-ui-allowed-hosts naming it") {
		t.Errorf("misdirected = %s", got)
	}

	// With no list given, the suggestion starts from this machine's names.
	gg, _ := newHostGuard("127.0.0.1:4242", nil, []string{"box.example", "box"})
	if got := gg.misdirected(proxied).Error(); !strings.Contains(got, "GRAIN_UI_ALLOWED_HOSTS='box.example,box,*.proxy.example.test'") {
		t.Errorf("misdirected = %s", got)
	}
}

// With nothing registered, a name that would be in a family is refused
// like any other.
func TestHostGuardRefusalWithoutAFamilyNamesSetup(t *testing.T) {
	g, _ := newHostGuard("127.0.0.1:4242", []string{"me"}, nil)
	if got := g.misdirected("x.proxy.example.test").Error(); !strings.Contains(got, "GRAIN_UI_ALLOWED_HOSTS='me,x.proxy.example.test' scripts/setup.sh") {
		t.Errorf("misdirected = %s", got)
	}
}
