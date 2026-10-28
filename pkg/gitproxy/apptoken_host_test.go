package gitproxy

// Every credential is on one git host (hosts.go, docs/git-hosts.md). An
// App credential mints its installation token against its own host's API
// -- https://<host>/api/v3 on GitHub Enterprise -- and a credential
// recorded for another host is refused rather than sent.

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeGHE is a GitHub Enterprise stand-in: the App token exchange under
// /api/v3, and git at the root, recording what reached it.
type fakeGHE struct {
	*httptest.Server
	mu        sync.Mutex
	mintPaths []string
	gitAuth   []string
}

func newFakeGHE(t *testing.T) *fakeGHE {
	f := &fakeGHE{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			f.mintPaths = append(f.mintPaths, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token":"ghe-installation-token","expires_at":"2099-01-01T00:00:00Z"}`))
			return
		}
		f.gitAuth = append(f.gitAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGHE) host() string { return strings.TrimPrefix(f.URL, "http://") }

// buildProxyOn wires a real BuildProxy forwarding to host over http, with
// the given ladder and credential files, sandbox-0 allowed owner/repo.
func buildProxyOn(t *testing.T, host string, patterns map[string]string, tokens map[string]string, apps ...string) *GitProxy {
	t.Helper()
	dataDir := t.TempDir()
	credsDir := writeCredentialSet(t, patterns, tokens)
	for _, app := range apps {
		writeAppCredential(t, credsDir, app)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(credsDir, filepath.Join(dataDir, "secrets", "github")); err != nil {
		t.Fatal(err)
	}
	writeTokensFile(t, filepath.Join(dataDir, "secrets"), map[string]string{"sandbox-0": "tok0"})
	proxy, err := BuildProxy(BuildConfig{DataDir: dataDir, ForwardHost: host, ForwardTLS: false})
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Credentials.AppTransport != nil {
		t.Fatal("BuildProxy injected an AppTransport; this test needs the real per-host one")
	}
	proxy.CredentialOverrides = nil
	proxy.TeamCredentials = ladderConnections{proxy: proxy}
	proxy.Authorizer = stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}}
	return proxy
}

func TestAnAppCredentialOnGitHubEnterpriseMintsAgainstThatHostsAPI(t *testing.T) {
	ghe := newFakeGHE(t)
	proxy := buildProxyOn(t, ghe.host(), map[string]string{"*": "ci-app"}, nil, "ci-app")

	// BuildProxy recorded the existing credential on the host it forwards
	// to: the in-place migration.
	if h := proxy.Credentials.HostOf("ci-app"); !h.Recorded || h.Host != ghe.host() || h.BaseURL != ghe.URL {
		t.Errorf("HostOf(ci-app) = %+v, want it recorded on %s", h, ghe.URL)
	}

	resp := pushWith(t, proxy)
	if resp.Status != 200 {
		t.Fatalf("push status = %d, body %q", resp.Status, resp.Body)
	}
	ghe.mu.Lock()
	defer ghe.mu.Unlock()
	if len(ghe.mintPaths) != 1 || ghe.mintPaths[0] != "/api/v3/app/installations/67890/access_tokens" {
		t.Errorf("mint requests = %q, want one to /api/v3/app/installations/67890/access_tokens", ghe.mintPaths)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ghe-installation-token:"))
	if len(ghe.gitAuth) != 1 || ghe.gitAuth[0] != want {
		t.Errorf("git request Authorization = %q, want the minted token %q", ghe.gitAuth, want)
	}
}

func TestACredentialRecordedForAnotherHostIsRefusedNotSent(t *testing.T) {
	ghe := newFakeGHE(t)
	proxy := buildProxyOn(t, ghe.host(), map[string]string{"*": "other"}, map[string]string{"other": "other-host-token"}, "other-app")
	if err := proxy.Credentials.SetHost("other", CredentialHost{Host: "ghe-b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Credentials.SetHost("other-app", CredentialHost{Host: "github.com"}); err != nil {
		t.Fatal(err)
	}

	resp := pushWith(t, proxy)
	if resp.Status == 200 {
		t.Fatalf("push with another host's credential succeeded")
	}
	mustContain(t, string(resp.Body), `"other"`, "ghe-b.example.com", ghe.host())

	if cred, ok := proxy.Credentials.Get("other-app"); !ok || cred.Token != nil || cred.WrongHost == "" {
		t.Errorf("Get(other-app) = %+v, want no token and a WrongHost refusal", cred)
	}
	ghe.mu.Lock()
	defer ghe.mu.Unlock()
	if len(ghe.gitAuth) != 0 || len(ghe.mintPaths) != 0 {
		t.Errorf("forward host saw git %q and mints %q, want nothing", ghe.gitAuth, ghe.mintPaths)
	}
}

func TestAnAppCredentialOnAPlainGitHostIsRefused(t *testing.T) {
	ghe := newFakeGHE(t)
	proxy := buildProxyOn(t, ghe.host(), map[string]string{"*": "ci-app"}, nil, "ci-app")
	if err := proxy.Credentials.SetHost("ci-app", CredentialHost{Host: ghe.host(), Kind: model.GitHostGit}); err == nil {
		t.Error("SetHost recorded an App credential on a plain git host")
	}
	os.WriteFile(filepath.Join(proxy.Credentials.Dir(), "ci-app.host.json"),
		[]byte(`{"host":"`+ghe.host()+`","kind":"git","base_url":"`+ghe.URL+`"}`), 0o600)
	proxy.Credentials.forget("ci-app")
	cred, _ := proxy.Credentials.GetForRepo("ci-app", "owner", "repo")
	if cred.Token != nil || cred.MintFailure == nil || !strings.Contains(cred.MintFailure.Describe(), "plain git host") {
		t.Errorf("credential = %+v, want no token and a MintFailure naming the plain git host", cred)
	}
	ghe.mu.Lock()
	defer ghe.mu.Unlock()
	if len(ghe.mintPaths) != 0 {
		t.Errorf("mint requests = %q, want none", ghe.mintPaths)
	}
}

func TestCredentialHostAPIBase(t *testing.T) {
	for _, tc := range []struct {
		in           CredentialHost
		host, prefix string
		ok           bool
	}{
		{CredentialHost{Host: "github.com"}, "api.github.com", "", true},
		{CredentialHost{Host: "ghe.example.com"}, "ghe.example.com", "/api/v3", true},
		{CredentialHost{Host: "ghe.example.com", BaseURL: "https://ghe.example.com:8443"}, "ghe.example.com:8443", "/api/v3", true},
		{CredentialHost{Host: "git.example.com", Kind: model.GitHostGit}, "", "", false},
	} {
		h, err := NormalizeCredentialHost(tc.in)
		if err != nil {
			t.Fatalf("NormalizeCredentialHost(%+v): %v", tc.in, err)
		}
		host, prefix, ok := h.APIBase()
		if host != tc.host || prefix != tc.prefix || ok != tc.ok {
			t.Errorf("%+v.APIBase() = %q %q %v, want %q %q %v", tc.in, host, prefix, ok, tc.host, tc.prefix, tc.ok)
		}
	}
	if _, err := NormalizeCredentialHost(CredentialHost{Host: "ghe.example.com", BaseURL: "https://elsewhere.example.com"}); err == nil {
		t.Error("a base URL on another host was accepted")
	}
}
