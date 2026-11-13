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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ssoVerifier stands in for a wrapper's identity-aware proxy, which
// loginServer's front end plays: a bearer "sso:<email>" arrives at the
// daemon as X-Test-Sso, signed in as that person.
type ssoVerifier struct{ _ func() }

func (ssoVerifier) Name() string      { return "test-sso" }
func (ssoVerifier) Headers() []string { return []string{"X-Test-Sso"} }
func (ssoVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	email := r.Header.Get("X-Test-Sso")
	if email == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	return ui.VerifiedIdentity{Email: email, Kind: model.PrincipalHuman}, true, nil
}

// loginServer is a deployment with external agent tokens on, which
// admits jane@example.com through ssoVerifier, and a "test-sso"
// credential source that signs in as her. jane holds no admin grant of
// any kind: she is only on the allowlist.
func loginServer(t *testing.T) (*httptest.Server, *model.Store) {
	t.Helper()
	srv, store := loginDaemon(t, true, "")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if email, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer sso:"); ok {
			r.Header.Del("Authorization")
			r.Header.Del("X-Grain-Tunnel-Token")
			r.Header.Set("X-Test-Sso", email)
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, store
}

// loginDaemon is loginServer's daemon, without the front end: with
// externalAgentApi as given, and an IAP audience when iapAudience is set.
func loginDaemon(t *testing.T, externalAgentAPI bool, iapAudience string) (*ui.Server, *model.Store) {
	t.Helper()
	useConfigHome(t)
	t.Setenv(ui.LocalAPITokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	for _, env := range ui.TokenEnvVars {
		t.Setenv(env, "")
	}
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval, ExternalAgentAPI: externalAgentAPI}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ui.RegisterIdentityVerifier(ssoVerifier{}))
	RegisterCredentialSource("test-sso", CredentialSourceFunc(func(context.Context, CredentialRequest) (string, error) {
		return "sso:jane@example.com", nil
	}))
	t.Cleanup(func() { RegisterCredentialSource("test-sso", nil) })
	c := ui.NewClient(ui.Config{SourceDB: db, Actor: ui.DefaultActor("operator"),
		AllowedPrincipals: []string{"user:jane@example.com"}, IAPAudience: iapAudience}, store)
	return accessCodeServer(c), store
}

// TestGrainLoginIssueTokenSelfServiceNonAdminAndLogoutRevokes is
// external-cli.spec.md#XC-8 and #XC-9: a signed-in person with no admin
// grant mints their own grain/external-dev token with `grain login
// --issue-token`, owned by them and kept out of config.json; a dangerous
// scope set is refused them; an access code is not stored silently;
// `grain whoami` says who the context is and that an expired token has
// expired; and `grain logout` revokes the token and forgets it.
func TestGrainLoginIssueTokenSelfServiceNonAdminAndLogoutRevokes(t *testing.T) {
	ts, store := loginServer(t)
	ctx := context.Background()

	if err := runCLI([]string{"login", ts.URL, "--name", "team", "--auth", "test-sso", "--issue-token", "--expires", "7d"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadContexts()
	team := cfg.Contexts["team"]
	if cfg.Current != "team" || team == nil || team.Auth != "external-token" || team.Credential != "file:team" ||
		team.TokenID == "" || team.Server != ts.URL {
		t.Fatalf("after login, config = %+v / %+v", cfg, team)
	}
	path, _ := contextConfigPath()
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "gma_") {
		t.Fatalf("config.json holds the raw token: %s", raw)
	}
	tok, err := store.GetExternalAgentToken(ctx, team.TokenID)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Owner != "jane@example.com" || tok.ScopeSet != "grain/external-dev" || tok.CreatedBy != "human:jane@example.com" ||
		tok.ExpiresAt == nil || tok.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Errorf("minted token = %+v, want jane's own grain/external-dev token for 7 days", tok)
	}

	out := captureStdout(t, func() {
		if err := runCLI([]string{"whoami", "--json"}); err != nil {
			t.Error(err)
		}
	})
	var who whoamiView
	if err := json.Unmarshal([]byte(out), &who); err != nil {
		t.Fatalf("whoami --json = %q: %v", out, err)
	}
	if who.Context != "team" || who.Owner != "jane@example.com" || !strings.HasPrefix(who.Principal, "agent:external:cli-") || who.TokenID != team.TokenID {
		t.Errorf("whoami = %+v", who)
	}

	// A dangerous scope set is an operator's to issue, not a person's own.
	err = runCLI([]string{"login", ts.URL, "--name", "ops", "--auth", "test-sso", "--issue-token", "--scope-set", "grain/deployment-ops"})
	if exitOf(err) != exitAuth {
		t.Errorf("self-service grain/deployment-ops = %v, want refused with exit 3", err)
	}
	// So is a repository the deployment does not work in.
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"login", ts.URL, "--name", "elsewhere", "--auth", "test-sso", "--issue-token", "--repos", "other/repo"}); err == nil {
		t.Error("self-service token limited to an off-allowlist repo was minted")
	}
	// An access code is not stored while gma_ tokens are on.
	err = runCLI([]string{"-token", testAccessCode, "login", ts.URL, "--name", "ac"})
	if exitOf(err) != exitUsage || !strings.Contains(err.Error(), "--issue-token") {
		t.Errorf("login with the access code = %v, want a refusal pointing at --issue-token", err)
	}
	// Nor is an access code piped to --token-stdin beside --issue-token:
	// that combination is refused rather than storing the code.
	loginStdin = strings.NewReader(testAccessCode + "\n")
	t.Cleanup(func() { loginStdin = os.Stdin })
	err = runCLI([]string{"login", ts.URL, "--name", "piped", "--auth", "access-code", "--issue-token", "--token-stdin"})
	if exitOf(err) != exitUsage || !strings.Contains(err.Error(), "grain -token CODE login "+ts.URL+" --issue-token") {
		t.Errorf("login --issue-token --token-stdin = %v, want a usage refusal naming the -token form", err)
	}
	cfg, _ = loadContexts()
	for _, n := range []string{"ops", "elsewhere", "ac", "piped"} {
		if _, ok := cfg.Contexts[n]; ok {
			t.Errorf("a refused login left context %q", n)
		}
	}

	// logout revokes the token on the server and forgets it here.
	if err := runCLI([]string{"logout"}); err != nil {
		t.Fatal(err)
	}
	tok, _ = store.GetExternalAgentToken(ctx, tok.ID)
	if tok.RevokedAt == nil {
		t.Error("grain logout did not revoke the login's token")
	}
	cfg, _ = loadContexts()
	if c := cfg.Contexts["team"]; c == nil || c.Credential != "" || c.TokenID != "" {
		t.Errorf("after logout, context team = %+v", c)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "credentials", "team")); !os.IsNotExist(err) {
		t.Errorf("logout left the credential file (%v)", err)
	}
	if err := runCLI([]string{"whoami"}); exitOf(err) != exitAuth {
		t.Errorf("whoami after logout = %v, want exit 3", err)
	}

	// An expired token: whoami says so, and how to renew it.
	raw, hash, prefix, err := model.GenerateExternalAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	past := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := store.CreateExternalAgentToken(ctx, model.ExternalAgentToken{Name: "old", Prefix: prefix, TokenHash: hash,
		ScopeSet: "grain/external-read", CreatedAt: past.Add(-time.Hour), ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	loc, err := newCredentialStore().store("old", raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts["old"] = &cliContext{Server: ts.URL, Auth: "external-token", Credential: loc}
	if err := saveContexts(cfg); err != nil {
		t.Fatal(err)
	}
	err = runCLI([]string{"--context", "old", "whoami"})
	if exitOf(err) != exitAuth || !strings.Contains(fmt.Sprint(err), "token for context old expired on 2026-01-02: run grain login --refresh") {
		t.Errorf("whoami with an expired token = %v", err)
	}
}

// TestGrainLoginRefusesFlagsItWouldIgnore: --refresh beside --token-stdin,
// and --scope-set/--repos/--expires without --issue-token, used to be
// dropped silently (docs/test-runs/2026-11-09-external-agent-rails.md,
// F3). Each is a usage refusal with its own code that leaves the
// contexts file byte-for-byte as it was and rotates nothing.
func TestGrainLoginRefusesFlagsItWouldIgnore(t *testing.T) {
	ts, _ := loginServer(t)
	if err := runCLI([]string{"login", ts.URL, "--name", "team", "--auth", "test-sso", "--issue-token"}); err != nil {
		t.Fatal(err)
	}
	path, _ := contextConfigPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loginStdin = strings.NewReader("gma_notarealtoken\n")
	t.Cleanup(func() { loginStdin = os.Stdin })
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"login", "--refresh", "--token-stdin", "--name", "team"}, "token_stdin_refresh"},
		{[]string{"login", ts.URL, "--name", "fresh", "--auth", "test-sso", "--scope-set", "grain/viewer"}, "needs_issue_token"},
		{[]string{"login", ts.URL, "--name", "fresh", "--auth", "test-sso", "--repos", "acme/widgets"}, "needs_issue_token"},
		{[]string{"login", "--refresh", "--name", "team", "--expires", "7d"}, "needs_issue_token"},
	} {
		err := runCLI(tc.args)
		_, code, _ := classifyError(err)
		if exitOf(err) != exitUsage || code != tc.code {
			t.Errorf("grain %s = %v (%s), want exit %d code %s", strings.Join(tc.args, " "), err, code, exitUsage, tc.code)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a refused login changed %s:\n%s\nwant\n%s", path, after, before)
	}
}

// A server URL carrying userinfo (http://user:pass@host) is kept as given
// in config.json (0600), but `grain login` never prints the password back
// (docs/test-runs/2026-11-08-cli-install-skill-mcp.md).
func TestGrainLoginDoesNotEchoURLUserinfo(t *testing.T) {
	ts, _ := loginServer(t)
	withUserinfo := strings.Replace(ts.URL, "http://", "http://u5er:pa55w0rd@", 1)
	out := captureStdout(t, func() {
		if err := runCLI([]string{"login", withUserinfo, "--name", "team", "--auth", "test-sso", "--issue-token", "--expires", "7d"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "pa55w0rd") || strings.Contains(out, "u5er") {
		t.Fatalf("login printed the URL's userinfo: %q", out)
	}
	if !strings.Contains(out, "signed in to "+ts.URL+" as context team") {
		t.Fatalf("login output = %q, want the scrubbed server URL", out)
	}
}

// Signing in through a trusted identity proxy (or IAP) as a person keeps
// a personal token afterwards, with no --issue-token asked for, so the
// CLI needs neither the proxy's sign-in nor the access code day to day;
// --issue-token=false keeps signing in through the proxy instead.
func TestGrainLoginThroughAnIdentityProxyHoldsAPersonalToken(t *testing.T) {
	ts, store := loginServer(t)
	ctx := context.Background()

	out := captureStdout(t, func() {
		if err := runCLI([]string{"login", ts.URL, "--name", "team", "--auth", "test-sso"}); err != nil {
			t.Error(err)
		}
	})
	cfg, _ := loadContexts()
	team := cfg.Contexts["team"]
	if team == nil || team.Auth != "external-token" || team.TokenID == "" {
		t.Fatalf("after a proxy sign-in, context = %+v; want a personal external-token", team)
	}
	if !strings.Contains(out, "holding a personal token for jane@example.com") || !strings.Contains(out, "--issue-token=false") {
		t.Errorf("login said %q; want it to name the personal token and how to decline one", out)
	}
	tok, err := store.GetExternalAgentToken(ctx, team.TokenID)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Owner != "jane@example.com" || tok.ScopeSet != defaultLoginScopeSet {
		t.Errorf("personal token = %+v; want jane's own %s token", tok, defaultLoginScopeSet)
	}

	_ = captureStdout(t, func() {
		if err := runCLI([]string{"login", ts.URL, "--name", "proxied", "--auth", "test-sso", "--issue-token=false"}); err != nil {
			t.Error(err)
		}
	})
	cfg, _ = loadContexts()
	if p := cfg.Contexts["proxied"]; p == nil || p.Auth != "test-sso" || p.TokenID != "" {
		t.Fatalf("login --issue-token=false: context = %+v; want one that signs in through test-sso", p)
	}
}

// iapLoginServer is loginDaemon behind a stand-in for IAP, as
// tests/iap/iapsim.go plays it: a bearer Google ID token "idtoken.<email>"
// in Proxy-Authorization (consumed) or Authorization (taken off, as the
// app is not shown it here) signs that person in; anything else is
// refused by "IAP" itself and never reaches the daemon. It listens on a
// non-loopback address, since a loopback URL never takes the identity
// path, and records the Authorization each request reached the daemon
// with.
func iapLoginServer(t *testing.T, externalAgentAPI bool) (string, *model.Store, *[]string) {
	t.Helper()
	srv, store := loginDaemon(t, externalAgentAPI, "/projects/1/global/backendServices/2")
	var host string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				host = n.IP.String()
				break
			}
		}
	}
	if host == "" {
		t.Skip("no non-loopback IPv4 address to listen on")
	}
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("listening on %s: %v", host, err)
	}
	var reached []string
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email := ""
		for _, h := range []string{"Proxy-Authorization", "Authorization"} {
			if v, ok := strings.CutPrefix(r.Header.Get(h), "Bearer idtoken."); ok {
				email = v
				r.Header.Del(h)
				if h == "Authorization" {
					r.Header.Del("X-Grain-Tunnel-Token")
				}
				break
			}
		}
		if email == "" {
			http.Error(w, "IAP: sign-in required", http.StatusUnauthorized)
			return
		}
		r.Header.Set("X-Test-Sso", email)
		reached = append(reached, r.Header.Get("Authorization"))
		srv.ServeHTTP(w, r)
	}))
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL, store, &reached
}

// $GRAIN_ID_TOKEN is a person's identity for IAP, not an explicit
// token: `grain login` through IAP with it keeps a personal token by
// default, --issue-token=false keeps a context that signs in through IAP
// each time, and nothing calls it an access code
// (docs/test-runs/2026-11-11-caller-identity.md, item 4). The personal
// token then gets past IAP with the ID token beside it in
// Proxy-Authorization.
func TestGrainLoginWithGrainIDTokenThroughIAPIsAnIdentity(t *testing.T) {
	server, store, reached := iapLoginServer(t, true)
	t.Setenv(ui.IDTokenEnv, "idtoken.jane@example.com")

	var out string
	errOut := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runCLI([]string{"login", server, "--name", "team"}); err != nil {
				t.Error(err)
			}
		})
	})
	cfg, _ := loadContexts()
	team := cfg.Contexts["team"]
	if team == nil || team.Auth != "external-token" || team.TokenID == "" || team.Audience != server {
		t.Fatalf("login with $GRAIN_ID_TOKEN: context = %+v (stdout %q, stderr %q); want a personal external-token that keeps IAP's audience", team, out, errOut)
	}
	if !strings.Contains(out, "holding a personal token for jane@example.com") || strings.Contains(errOut, "access code") {
		t.Errorf("login said %q / %q; want the personal token named and no access code", out, errOut)
	}
	if tok, err := store.GetExternalAgentToken(context.Background(), team.TokenID); err != nil || tok.Owner != "jane@example.com" {
		t.Errorf("personal token = %+v (%v); want jane's own", tok, err)
	}

	// Day to day the context's gma_ token is what the daemon sees, sent
	// past IAP with $GRAIN_ID_TOKEN in Proxy-Authorization.
	*reached = nil
	out = captureStdout(t, func() {
		if err := runCLI([]string{"whoami", "--json"}); err != nil {
			t.Error(err)
		}
	})
	var who whoamiView
	if err := json.Unmarshal([]byte(out), &who); err != nil || !strings.HasPrefix(who.Principal, "agent:external:cli-") || who.Owner != "jane@example.com" {
		t.Fatalf("whoami through IAP with the personal token = %q (%v); want jane's external token", out, err)
	}
	if len(*reached) == 0 || !strings.HasPrefix((*reached)[0], "Bearer gma_") {
		t.Errorf("the daemon was reached with Authorization %q; want the context's gma_ token", *reached)
	}

	// --issue-token=false: a context that signs in through IAP each time.
	_ = captureStdout(t, func() {
		if err := runCLI([]string{"login", server, "--name", "proxied", "--issue-token=false"}); err != nil {
			t.Error(err)
		}
	})
	cfg, _ = loadContexts()
	if p := cfg.Contexts["proxied"]; p == nil || p.Auth != "google-id-token" || p.TokenID != "" || p.Credential != "" {
		t.Fatalf("login --issue-token=false with $GRAIN_ID_TOKEN: context = %+v; want a google-id-token one holding nothing", p)
	}

	// A secret in $GRAIN_TOKEN is still an explicit token, and the
	// refusal names where it came from (IAP would refuse it before the
	// daemon saw it, so this one goes to a daemon with no IAP in front).
	ts, _ := loginServer(t)
	t.Setenv("GRAIN_TOKEN", testAccessCode)
	err := runCLI([]string{"login", ts.URL, "--name", "ac"})
	if exitOf(err) != exitUsage || !strings.Contains(fmt.Sprint(err), "not storing the token $GRAIN_TOKEN gave") {
		t.Errorf("login with $GRAIN_TOKEN = %v; want a refusal naming $GRAIN_TOKEN", err)
	}
}

// With externalAgentApi off, $GRAIN_ID_TOKEN through IAP keeps a
// google-id-token context -- not an access-code one, and with no warning
// about an access code's authority.
func TestGrainLoginWithGrainIDTokenAndNoExternalAgentAPI(t *testing.T) {
	server, _, _ := iapLoginServer(t, false)
	t.Setenv(ui.IDTokenEnv, "idtoken.jane@example.com")
	errOut := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runCLI([]string{"login", server, "--name", "team"}); err != nil {
				t.Error(err)
			}
		})
	})
	cfg, _ := loadContexts()
	if c := cfg.Contexts["team"]; c == nil || c.Auth != "google-id-token" || c.Credential != "" {
		t.Fatalf("login with externalAgentApi off: context = %+v; want google-id-token holding nothing", c)
	}
	if strings.Contains(errOut, "access code") || !strings.Contains(errOut, "does not accept gma_ tokens") {
		t.Errorf("login warned %q; want it to say there is no personal token, and nothing about an access code", errOut)
	}
}
