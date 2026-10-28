package gitproxy

// Google's GitHub-on-Prem (depot.code.forge.test, stage.code.forge.test):
// an ambient login from forgecli, and every connection to it through the
// host record's proxy (uplink) -- and nothing else's through it.

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

const depotHost = "depot.code.forge.test"

// fakeGGH puts a fake `forgecli` beside fakeHostTools' gh and git. It answers
// `GH_HOST=<host> forgecli auth token` with forgecli-token-<host> for the two GHOP
// hosts and refuses any other, the way an account logged in to depot
// only does. With exportProxy set it instead sets HTTPS_PROXY and runs
// gh from PATH, as a wrapper applying uplink does.
func fakeGGH(t *testing.T, exportProxy string) string {
	t.Helper()
	state := fakeHostTools(t)
	bin := os.Getenv("PATH")
	body := `#!/bin/sh
echo "GH_HOST=$GH_HOST $*" >> "` + state + `/forgecli-calls"
[ "$1 $2" = "auth token" ] || { echo "unexpected forgecli $*" >&2; exit 2; }
`
	if exportProxy != "" {
		body += `HTTPS_PROXY=` + exportProxy + `
export HTTPS_PROXY
exec gh "$@"
`
	}
	body += `case "$GH_HOST" in
  depot.code.forge.test|stage.code.forge.test) echo "forgecli-token-$GH_HOST" ;;
  *) echo "You are not logged into $GH_HOST. Run: GH_HOST=$GH_HOST forgecli auth login" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "forgecli"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAmbientAutoPicksGGHForGHOPAndNeverForGitHub(t *testing.T) {
	for host, want := range map[string]string{
		"depot.code.forge.test": testForgeSource,
		"stage.code.forge.test": testForgeSource,
		"github.com":            AmbientSourceGH,
		"ghe.example.com":       AmbientSourceGH,
		"code.forge.test.evil":  AmbientSourceGH,
	} {
		if got := AmbientAutoSource(host); got != want {
			t.Errorf("AmbientAutoSource(%s) = %s, want %s", host, got, want)
		}
	}
	state := fakeGGH(t, "")
	writeState(t, state, "gh-token", "gho_github")

	for _, host := range []string{"depot.code.forge.test", "stage.code.forge.test"} {
		token, from, err := AmbientToken(host, AmbientSourceAuto)
		if err != nil || token != "forgecli-token-"+host || from != testForgeSource {
			t.Fatalf("AmbientToken(%s, auto) = %q, %q, %v; want forgecli's token", host, token, from, err)
		}
	}
	if calls(t, state, "gh-calls") != 0 {
		t.Fatalf("auto asked plain gh for a GHOP host")
	}
	if token, from, err := AmbientToken("github.com", AmbientSourceAuto); err != nil || from != AmbientSourceGH || token != "gho_github" {
		t.Fatalf("AmbientToken(github.com, auto) = %q, %q, %v; want gh's", token, from, err)
	}
	// Not even by name: forgecli is never asked for a github.com login.
	before := calls(t, state, "forgecli-calls")
	if _, _, err := AmbientToken("github.com", testForgeSource); err == nil || !strings.Contains(err.Error(), "never for github.com") {
		t.Fatalf("AmbientToken(github.com, forgecli) = %v; want a refusal", err)
	}
	if calls(t, state, "forgecli-calls") != before {
		t.Fatalf("forgecli was run for github.com")
	}
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", testForgeSource); err == nil {
		t.Fatalf("SetAmbient(github.com, forgecli) succeeded")
	}
	if err := c.SetAmbientOn("depot", CredentialHost{Host: depotHost, Kind: model.GitHostGit}, testForgeSource); err == nil {
		t.Fatalf("forgecli on a plain git host succeeded")
	}
	if err := c.SetAmbient("depot", depotHost, "auto"); err != nil {
		t.Fatal(err)
	}
	if cred, ok := c.Get("depot"); !ok || cred.Token == nil || *cred.Token != "forgecli-token-"+depotHost || cred.Ambient.From != testForgeSource {
		t.Fatalf("Get(depot) = %+v, %v", cred, ok)
	}
}

func TestAmbientGGHMissingOrLoggedOutSaysWhatToRun(t *testing.T) {
	fakeGGH(t, "")
	_, _, err := AmbientToken("stage.code.forge.test", testForgeSource)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Logged in to depot and stage only: a third corp host is not.
	_, _, err = AmbientToken("other.code.forge.test", AmbientSourceAuto)
	if err == nil || !strings.Contains(err.Error(), "run `GH_HOST=other.code.forge.test forgecli auth login` as ") {
		t.Fatalf("logged out = %v; want the forgecli login command", err)
	}
	t.Setenv("PATH", t.TempDir())
	_, _, err = AmbientToken(depotHost, AmbientSourceAuto)
	if err == nil || !strings.Contains(err.Error(), "forgecli is not installed") ||
		!strings.Contains(err.Error(), "GH_HOST=depot.code.forge.test forgecli auth login") {
		t.Fatalf("no forgecli = %v", err)
	}
	if got := AmbientLoginFix(depotHost, AmbientSourceAuto); got != "GH_HOST=depot.code.forge.test forgecli auth login" {
		t.Fatalf("AmbientLoginFix = %q", got)
	}
}

func TestHostProxyIsValidated(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"127.0.0.1:3128":      "http://127.0.0.1:3128",
		"http://uplink:3128/": "http://uplink:3128",
		"https://uplink:3129": "https://uplink:3129",
	} {
		if got, err := NormalizeHostProxy(in); err != nil || got != want {
			t.Errorf("NormalizeHostProxy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"socks5://x:1", "http://u:p@x:1", "http://x:1/path", "http://"} {
		if _, err := NormalizeHostProxy(bad); err == nil {
			t.Errorf("NormalizeHostProxy(%q) succeeded", bad)
		}
	}
}

// recordingProxy is a fake uplink: it records every CONNECT and every
// absolute-URI request it is sent, refuses the CONNECTs (there is no
// depot to tunnel to), and answers the plain requests itself.
type recordingProxy struct {
	addr string
	mu   sync.Mutex
	seen []string
	auth []string
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &recordingProxy{addr: "http://" + ln.Addr().String()}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		if r.Method == http.MethodConnect {
			p.seen = append(p.seen, "CONNECT "+r.Host)
		} else {
			p.seen = append(p.seen, r.Method+" "+r.URL.String())
			p.auth = append(p.auth, r.Header.Get("Authorization"))
		}
		p.mu.Unlock()
		if r.Method == http.MethodConnect {
			http.Error(w, "no tunnel in this test", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"login":"octo-depot"}`)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return p
}

func (p *recordingProxy) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func TestGHOPGitAndAPITrafficGoesThroughTheHostsProxyOnly(t *testing.T) {
	uplink := newRecordingProxy(t)

	// A backend that must only ever be reached directly.
	var direct []string
	var directMu sync.Mutex
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directMu.Lock()
		direct = append(direct, r.Method+" "+r.URL.Path)
		directMu.Unlock()
		io.WriteString(w, "direct")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go backend.Serve(ln)
	t.Cleanup(func() { backend.Close() })

	c, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"depot", "ghe", "dotcom"} {
		if err := c.SetPAT(name, "tok-"+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SetHost("depot", CredentialHost{Host: depotHost, Kind: model.GitHostGitHub, Proxy: uplink.addr}); err != nil {
		t.Fatal(err)
	}
	if h := c.HostOf("depot"); h.Proxy != uplink.addr || h.Kind != model.GitHostGitHub {
		t.Fatalf("HostOf(depot) = %+v", h)
	}
	backendHost := ln.Addr().String()
	if err := c.SetHost("ghe", CredentialHost{Host: backendHost, Kind: model.GitHostGitHub, BaseURL: "http://" + backendHost}); err != nil {
		t.Fatal(err)
	}
	def := &RealForwarder{Host: "github.com", UseTLS: true}
	p := &GitProxy{Credentials: c, TeamCredentials: ladderConnections{creds: c}, Forwarder: def}

	// git: depot's upstream fetch goes to uplink as a CONNECT to depot.
	fw, err := p.forwarderFor(Credential{Name: "depot"})
	if err != nil {
		t.Fatal(err)
	}
	token := "tok-depot"
	if resp, err := fw.Forward("GET", "/o/r.git/info/refs", "service=git-upload-pack", nil, nil, &token); err == nil {
		resp.Stream.Close()
		t.Fatalf("depot fetch through a proxy that tunnels nothing succeeded")
	}
	if got := uplink.requests(); len(got) != 1 || got[0] != "CONNECT depot.code.forge.test:443" {
		t.Fatalf("uplink saw %q; want one CONNECT to depot", got)
	}

	// A host with no proxy is reached directly, and uplink sees nothing.
	fw, err = p.forwarderFor(Credential{Name: "ghe"})
	if err != nil {
		t.Fatal(err)
	}
	token = "tok-ghe"
	resp, err := fw.Forward("GET", "/o/r.git/info/refs", "service=git-upload-pack", nil, nil, &token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Stream.Close()
	if len(direct) != 1 || len(uplink.requests()) != 1 {
		t.Fatalf("direct = %q, uplink = %q; want the GHE fetch direct", direct, uplink.requests())
	}
	// github.com keeps the proxy's own forwarder, untouched.
	if fw, _ := p.forwarderFor(Credential{Name: "dotcom"}); fw != Forwarder(def) {
		t.Fatalf("github.com forwarder = %#v; want the default one", fw)
	}
	if rt := c.HostOf("dotcom").HTTPTransport(nil); rt != http.DefaultTransport {
		t.Fatalf("github.com transport is not the default one")
	}

	// The git forward over plain http to a loopback "depot" stand-in
	// carries the credential through the proxy too.
	if err := c.SetHost("depot", CredentialHost{Host: "localhost:1", Kind: model.GitHostGitHub,
		BaseURL: "http://localhost:1", Proxy: uplink.addr}); err != nil {
		t.Fatal(err)
	}
	fw, _ = p.forwarderFor(Credential{Name: "depot"})
	token = "tok-depot"
	resp, err = fw.Forward("GET", "/o/r.git/info/refs", "service=git-upload-pack", nil, nil, &token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Stream.Close()
	got := uplink.requests()
	if last := got[len(got)-1]; last != "GET http://localhost:1/o/r.git/info/refs?service=git-upload-pack" {
		t.Fatalf("uplink's last request = %q", last)
	}
	uplink.mu.Lock()
	auth := uplink.auth[len(uplink.auth)-1]
	uplink.mu.Unlock()
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("tok-depot:")); auth != want {
		// The forwarder's own Basic shape, whatever it is, must have
		// reached the proxy carrying the depot token.
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if !strings.Contains(string(raw), "tok-depot") {
			t.Fatalf("git auth through uplink = %q", auth)
		}
	}
}

func TestReachListsReachableReposForNonDefaultAmbientHost(t *testing.T) {
	fakeGGH(t, "")
	c, _ := ambientSet(t)
	fake := github.NewFakeTransport(github.ApiResponse{
		Status: 200,
		Body:   []byte(`[{"full_name":"experimental/bsalmon-test","permissions":{"push":true}},{"full_name":"corp/read-only","permissions":{"push":false}}]`),
	})
	c.AppTransport = fake
	if err := c.SetAmbient("depot-cred", depotHost, "auto"); err != nil {
		t.Fatal(err)
	}

	reach := &Reach{Credentials: c, Host: "github.com"}
	repos, ok, err := reach.Repos("depot-cred")
	if err != nil || !ok {
		t.Fatalf("reach.Repos(depot-cred) = %v, %v, %v; want ok=true", repos, ok, err)
	}
	want := []ReachableRepo{
		{Repo: depotHost + "/experimental/bsalmon-test", Push: true},
		{Repo: depotHost + "/corp/read-only", Push: false},
	}
	if len(repos) != 2 || repos[0] != want[0] || repos[1] != want[1] {
		t.Fatalf("repos = %+v; want %+v", repos, want)
	}

	check, err := reach.Check("depot-cred", "experimental", "bsalmon-test", true)
	if err != nil || check != Reachable {
		t.Fatalf("Check(experimental/bsalmon-test) = %v, %v; want Reachable", check, err)
	}
	checkRead, err := reach.Check("depot-cred", "corp", "read-only", false)
	if err != nil || checkRead != Reachable {
		t.Fatalf("Check(corp/read-only, read) = %v, %v; want Reachable", checkRead, err)
	}
	checkPush, err := reach.Check("depot-cred", "corp", "read-only", true)
	if err != nil || checkPush != Unreachable {
		t.Fatalf("Check(corp/read-only, push) = %v, %v; want Unreachable", checkPush, err)
	}
}
