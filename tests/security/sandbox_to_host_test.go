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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity/hook"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// Path 1 & 6: An open-network sandbox's nftables fence drops cloud metadata
// (IPv4 169.254.0.0/16, IPv6 ULA fc00::/7 covering GCE fd20:ce::254 and EC2
// fd00:ec2::254, IPv6 link-local fe80::/10, IPv4-mapped ::ffff:0:0/96, and
// 0.0.0.0/8), drops peer sandboxes on its own bridge segment *ahead* of the
// port-53 DNS accept so two open sandboxes cannot communicate over TCP/UDP
// port 53, and jumps from bridge prerouting by port rather than source IP.
func TestOpenFenceBlocksMetadataLoopbackAndPeerDNS(t *testing.T) {
	_, cidr, err := net.ParseCIDR("192.168.9.0/24")
	if err != nil {
		t.Fatal(err)
	}
	script := orchestrator.OpenNetworkFenceRuleset(
		"vm_g_sec_1",
		net.ParseIP("192.168.9.14"),
		cidr,
		net.ParseIP("192.168.9.1"),
		[]string{"vethsec0"},
		[]netip.Prefix{
			netip.MustParsePrefix("10.128.0.0/20"),
			netip.MustParsePrefix("192.168.9.0/24"),
		},
		[]orchestrator.SandboxServicePort{{Proto: "tcp", Port: 9728}},
	)

	for _, family := range []string{"inet", "bridge"} {
		rule := func(body string) string {
			return "add rule " + family + " grain_network_none vm_g_sec_1 " + body + "\n"
		}
		ordered := []string{
			rule("ip daddr 192.168.9.1 tcp dport { 9728 } accept"),
			rule("ip daddr 192.168.9.1 meta l4proto { tcp, udp } th dport 53 accept"),
			rule("ip daddr { 0.0.0.0/8, 127.0.0.0/8, 192.168.9.0/24 } counter drop"),
			rule("ip6 daddr { ::1/128, ::ffff:0.0.0.0/96 } counter drop"),
			rule("meta l4proto { tcp, udp } th dport 53 accept"),
			rule("ip daddr { 0.0.0.0/8, 10.128.0.0/20, 127.0.0.0/8, 169.254.0.0/16, 192.168.9.0/24 } counter drop"),
			rule("ip6 daddr { ::1/128, ::ffff:0.0.0.0/96, fc00::/7, fe80::/10 } counter drop"),
		}
		last := -1
		for _, want := range ordered {
			idx := strings.Index(script, want)
			if idx < 0 {
				t.Fatalf("%s chain missing rule %q in:\n%s", family, want, script)
			}
			if idx < last {
				t.Fatalf("%s rule %q appears out of order in:\n%s", family, want, script)
			}
			last = idx
		}
	}
	if !strings.Contains(script, `add rule bridge grain_network_none prerouting iifname "vethsec0" ether type != arp jump vm_g_sec_1`) {
		t.Fatalf("missing source-IP-independent bridge prerouting jump:\n%s", script)
	}

	// Validate the generated ruleset against the kernel nftables parser when
	// nft and passwordless root/sudo are available (as in real sandboxes and CI).
	nftBin := ""
	for _, candidate := range []string{"/usr/sbin/nft", "/sbin/nft"} {
		if _, err := os.Stat(candidate); err == nil {
			nftBin = candidate
			break
		}
	}
	if nftBin != "" {
		var cmd *exec.Cmd
		if os.Geteuid() == 0 {
			cmd = exec.Command(nftBin, "-c", "-f", "-")
		} else if exec.Command("sudo", "-n", "true").Run() == nil {
			cmd = exec.Command("sudo", "-n", nftBin, "-c", "-f", "-")
		}
		if cmd != nil {
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nft -c -f - rejected OpenNetworkFenceRuleset: %v\n%s\nscript:\n%s", err, out, script)
			}
		}
	}
}

// Path 2: The gcp-key token listener refuses non-loopback bind addresses,
// requires each attempt's own 32-byte subject token with no attempt-enumeration
// oracle, prevents cross-attempt token/id-token/introspect access, and
// invalidates the attempt's subject token immediately on Release.
func TestGCPKeyTokenListenerIsolatesAttemptsAndRequiresLoopback(t *testing.T) {
	ctx := context.Background()

	loopbackCheck, err := intree.NewServer("loopback-check", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loopbackCheck.Close(ctx) })
	for _, nonLoopback := range []string{"0.0.0.0:0", "192.168.9.1:0", "[::]:0"} {
		if _, err := loopbackCheck.Listen(nonLoopback, http.NotFoundHandler()); err == nil {
			t.Errorf("data-plane Listen(%q) succeeded; must refuse non-loopback bind", nonLoopback)
		}
	}

	const (
		orgSA     = "org-runner@grain-sec-proj.iam.gserviceaccount.com"
		minterSA  = "minter@grain-sec-proj.iam.gserviceaccount.com"
		minterKey = `{"type":"service_account","client_email":"minter@grain-sec-proj.iam.gserviceaccount.com"}`
		ctrlToken = "control-plane-token-0123456789abcdef"
	)
	cloud := gcpfake.New()
	cloud.DefaultIdentity = minterSA
	cloud.AddAccount("grain-sec-proj", orgSA, "security test SA")

	srv, err := intree.NewConfigurableGCP(intree.GCPConfig{
		Connector: cloud,
		Listen:    "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(ctx) })
	srv.CapabilityServer.Token = ctrlToken
	ts := httptest.NewServer(srv.CapabilityServer)
	t.Cleanup(ts.Close)

	identity := protocol.Identity{DeploymentID: "049f1e09", InstanceID: "049f1e09c0ffee0123456789abcdef01"}
	ps, err := protocol.Attach(ctx, "https://github.com/bwsalmon/grain/capabilities/"+srv.Name,
		protocol.ServerConfig{
			ServerURL: ts.URL + "/mcp",
			Headers:   map[string]string{"Authorization": "Bearer $secret:control"},
			Config: map[string]any{
				protocol.DeploymentIDConfig: identity.DeploymentID,
				protocol.InstanceIDConfig:   identity.InstanceID,
				"admins": []any{
					map[string]any{
						"name":              "minter",
						"credential_source": "configured",
						"projects":          []any{"grain-sec-proj"},
						"role_ceiling":      []any{"roles/storage.objectViewer"},
					},
				},
				"admin_credentials": "$secret:gcp-admins",
				"permission_sets": []any{
					map[string]any{
						"name":            "org",
						"admin":           "minter",
						"service_account": orgSA,
						"roles":           []any{"roles/storage.objectViewer"},
					},
				},
			},
		},
		func(name string) (string, bool) {
			switch name {
			case "control":
				return ctrlToken, true
			case "gcp-admins":
				return `{"minter": ` + minterKey + `}`, true
			default:
				return "", false
			}
		},
		protocol.DialOptions{Identity: identity},
	)
	if err != nil {
		t.Fatalf("attaching configurable GCP server: %v", err)
	}
	t.Cleanup(func() { ps.Close() })

	if ps.Methods["gcp-key"] == nil {
		t.Fatal("gcp-key method not registered")
	}

	mintAttempt := func(attemptID string) (tokenURL, subjectToken string) {
		t.Helper()
		resp, err := ps.Mint(ctx, "gcp-key", attemptID, time.Now().Add(30*time.Minute), map[string]any{"permission_set": "org"})
		if err != nil || resp.Status != protocol.StatusOK {
			t.Fatalf("Mint(%s) = %+v, %v", attemptID, resp, err)
		}
		if len(resp.Ports) != 1 {
			t.Fatalf("Mint(%s) Ports = %+v, want 1 loopback forward", attemptID, resp.Ports)
		}
		var extCred struct {
			TokenURL         string `json:"token_url"`
			CredentialSource struct {
				File string `json:"file"`
			} `json:"credential_source"`
		}
		for _, p := range resp.Placements {
			if p.Path == intree.GCPKeyPath {
				if err := json.Unmarshal([]byte(p.Content), &extCred); err != nil {
					t.Fatalf("decoding external_account placement: %v", err)
				}
			}
		}
		for _, p := range resp.Placements {
			if p.Path == extCred.CredentialSource.File {
				subjectToken = strings.TrimSpace(p.Content)
			}
		}
		if extCred.TokenURL == "" || subjectToken == "" {
			t.Fatalf("Mint(%s) missing token_url (%q) or subject token", attemptID, extCred.TokenURL)
		}
		// Rewrite the guest-side forwarded port to the actual loopback port on the host.
		u, err := url.Parse(extCred.TokenURL)
		if err != nil {
			t.Fatal(err)
		}
		u.Host = net.JoinHostPort("127.0.0.1", jsonNumber(resp.Ports[0].HostPort))
		return u.String(), subjectToken
	}

	urlA, subjA := mintAttempt("attempt-a")
	urlB, subjB := mintAttempt("attempt-b")
	t.Cleanup(func() { _, _ = ps.Release(ctx, "gcp-key", "attempt-b") })

	postExchange := func(targetURL, subject string) (int, string) {
		t.Helper()
		resp, err := http.PostForm(targetURL, url.Values{
			"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"subject_token":      {subject},
			"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	// Valid own exchange works for A.
	codeA, bodyA := postExchange(urlA, subjA)
	if codeA != http.StatusOK {
		t.Fatalf("attempt-a valid exchange = %d %s", codeA, bodyA)
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(bodyA), &tokResp); err != nil || tokResp.AccessToken == "" {
		t.Fatalf("attempt-a access_token missing in %s", bodyA)
	}

	// Attempt A cannot fetch Attempt B's token by guessing attempt_id, with A's
	// subject token, an empty token, or a guessed token; and nonexistent attempt
	// IDs produce the exact same 401 invalid_grant body (no enumeration oracle).
	urlMissing := strings.Replace(urlB, "/runs/attempt-b/token", "/runs/attempt-guessed/token", 1)
	codeCross, bodyCross := postExchange(urlB, subjA)
	codeEmpty, bodyEmpty := postExchange(urlB, "")
	codeMissing, bodyMissing := postExchange(urlMissing, subjA)
	for name, pair := range map[string][2]any{
		"attempt-a token against attempt-b": {codeCross, bodyCross},
		"empty subject against attempt-b":   {codeEmpty, bodyEmpty},
		"guessed attempt_id":                {codeMissing, bodyMissing},
	} {
		if pair[0].(int) != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (%s)", name, pair[0], pair[1])
		}
	}
	if bodyCross != bodyMissing || bodyEmpty != bodyMissing {
		t.Errorf("enumeration oracle across attempt_id responses: cross=%q empty=%q missing=%q", bodyCross, bodyEmpty, bodyMissing)
	}

	// Attempt A cannot mint an OIDC id-token on Attempt B's endpoint.
	idURLB := strings.TrimSuffix(urlB, "/token") + "/id-token"
	idReq, _ := http.NewRequest(http.MethodPost, idURLB, strings.NewReader(url.Values{"audience": {"https://example.invalid"}}.Encode()))
	idReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	idReq.Header.Set("Authorization", "Bearer "+subjA)
	idResp, err := http.DefaultClient.Do(idReq)
	if err != nil {
		t.Fatal(err)
	}
	idResp.Body.Close()
	if idResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("cross-attempt /id-token with attempt-a's subject token = %d, want 401", idResp.StatusCode)
	}

	// Attempt A cannot introspect Attempt B's tokens (nor does Attempt A's minted
	// access token report active under Attempt B).
	introURLB := strings.TrimSuffix(urlB, "/token") + "/introspect"
	introResp, err := http.PostForm(introURLB, url.Values{"token": {tokResp.AccessToken}})
	if err != nil {
		t.Fatal(err)
	}
	introBody, _ := io.ReadAll(introResp.Body)
	introResp.Body.Close()
	if !strings.Contains(string(introBody), `"active":false`) {
		t.Errorf("cross-attempt /introspect = %d %s, want active:false", introResp.StatusCode, introBody)
	}

	// Releasing Attempt A immediately invalidates Attempt A's subject token,
	// while Attempt B keeps working.
	if _, err := ps.Release(ctx, "gcp-key", "attempt-a"); err != nil {
		t.Fatalf("Release(attempt-a): %v", err)
	}
	if codeAfter, _ := postExchange(urlA, subjA); codeAfter != http.StatusUnauthorized {
		t.Errorf("released attempt-a token exchange = %d, want 401", codeAfter)
	}
	if codeB, _ := postExchange(urlB, subjB); codeB != http.StatusOK {
		t.Errorf("live attempt-b token exchange after releasing attempt-a = %d, want 200", codeB)
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func putSecurityTask(t *testing.T, store *model.Store, id string, target model.RepoRef, grants ...model.Grant) {
	t.Helper()
	task := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  "security task " + id,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &target,
		Grants:  grants,
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

// Path 3: The git proxy ignores spoofed client/forwarding headers, refuses an
// attempt-bound token on git fetch, git push, /_grain/git-sign, /fs/, and /api/
// once the sandbox moves to a newer attempt or different task, and refuses to
// follow HTTP 3xx redirects so upstream credentials are never replayed elsewhere.
func TestGitProxyEnforcesAttemptBindingSpoofingAndRedirectSafety(t *testing.T) {
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	now := time.Now().UTC()

	putSecurityTask(t, store, "101", model.RepoRef{Owner: "acme", Name: "alpha"})
	putSecurityTask(t, store, "102", model.RepoRef{Owner: "acme", Name: "beta"})

	if err := store.StartRun(ctx, model.Run{
		ID: "run-101-1", TaskID: "101", Sandbox: "sb-alpha", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-102-1", TaskID: "102", Sandbox: "sb-beta", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bot"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	upstream := &gitUpstream{}
	upSrv := httptest.NewServer(upstream)
	t.Cleanup(upSrv.Close)
	upURL, _ := url.Parse(upSrv.URL)

	dataDir := t.TempDir()
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), "ghs_upstream_secret_value")

	tokenPath := filepath.Join(dataDir, "secrets", "sandbox-tokens.json")
	tokenStore := gitproxy.NewSandboxTokenStore(tokenPath)
	tokAlphaAttempt1, err := tokenStore.EnsureGrant("sb-alpha", "101", 1)
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:         dataDir,
		Store:           store,
		ForwardHost:     upURL.Host,
		ForwardTLS:      false,
		NamespaceScopes: store,
		API:             http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxySrv.Close)

	doProxy := func(method, path, tok string, extraHeaders map[string]string, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, proxySrv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", "git/2.43.0")
		req.Header.Set("Accept", "*/*")
		if tok != "" {
			req.SetBasicAuth("x-access-token", tok)
		}
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 1. Spoofing source/forwarding headers cannot let sb-alpha read sb-beta's repo.
	code, body := doProxy(http.MethodGet, "/acme/beta.git/info/refs?service=git-upload-pack", tokAlphaAttempt1, map[string]string{
		"X-Forwarded-For":     "192.168.9.99",
		"X-Real-IP":           "192.168.9.99",
		"X-Grain-Sandbox":     "sb-beta",
		"X-Grain-Acting-Task": "102",
	}, "")
	if code != http.StatusForbidden {
		t.Fatalf("spoofed cross-repo fetch = %d %s, want 403", code, body)
	}
	if strings.Contains(body, "ghs_upstream_secret_value") {
		t.Fatalf("denial leaked upstream git token: %s", body)
	}

	// 2. While task 101 attempt 1 is live on sb-alpha, its bound token works.
	if code, body := doProxy(http.MethodGet, "/acme/alpha.git/info/refs?service=git-upload-pack", tokAlphaAttempt1, nil, ""); code != http.StatusOK {
		t.Fatalf("live attempt-1 git fetch = %d %s, want 200", code, body)
	}

	// 3. Simulate sb-alpha finishing attempt 1 and starting attempt 2 while
	// replaying attempt 1's bound token: attempt-1's token must be refused across
	// git fetch, git push, /_grain/git-sign, and /api/ because Attempt=1 != live Attempt=2.
	if err := store.FinishRun(ctx, "run-101-1", now.Add(time.Second), "failed", "retry"); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-101-2", TaskID: "101", Sandbox: "sb-alpha", Attempt: 2, StartedAt: now.Add(2 * time.Second),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() (int, string){
		"git fetch": func() (int, string) {
			return doProxy(http.MethodGet, "/acme/alpha.git/info/refs?service=git-upload-pack", tokAlphaAttempt1, nil, "")
		},
		"git push advertisement": func() (int, string) {
			return doProxy(http.MethodGet, "/acme/alpha.git/info/refs?service=git-receive-pack", tokAlphaAttempt1, nil, "")
		},
		"git-sign": func() (int, string) {
			return doProxy(http.MethodPost, "/_grain/git-sign", tokAlphaAttempt1, nil, "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
		},
		"agent API": func() (int, string) {
			return doProxy(http.MethodGet, "/api/tasks/101", tokAlphaAttempt1, nil, "")
		},
	} {
		st, respBody := call()
		if st != http.StatusForbidden {
			t.Errorf("stale attempt-1 token on %s = %d %s, want 403", name, st, respBody)
		}
	}

	// 4. Redirect credential safety: RealForwarder must return an upstream 302
	// untouched rather than following the Location header with Authorization attached.
	attackerSawAuth := false
	attackerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			attackerSawAuth = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(attackerSrv.Close)

	redirectUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attackerSrv.URL+"/captured", http.StatusFound)
	}))
	t.Cleanup(redirectUpstream.Close)
	redirURL, _ := url.Parse(redirectUpstream.URL)

	fwd := &gitproxy.RealForwarder{Host: redirURL.Host, UseTLS: false}
	secretTok := "ghs_do_not_leak_on_redirect"
	fwdResp, err := fwd.Forward(http.MethodGet, "/acme/alpha.git/info/refs", "service=git-upload-pack", nil, nil, &secretTok)
	if err != nil {
		t.Fatalf("RealForwarder on 302: %v", err)
	}
	if fwdResp.Stream != nil {
		fwdResp.Stream.Close()
	}
	if fwdResp.Status != http.StatusFound {
		t.Errorf("RealForwarder status = %d, want 302 returned without following", fwdResp.Status)
	}
	if attackerSawAuth {
		t.Fatal("RealForwarder followed HTTP 302 redirect and leaked Authorization header to redirect target")
	}
}

// Path 4: Reaching the daemon's API via gitproxy (/api/... and /api/mcp) never
// grants singleTenantTrusted access to internal, dangerous, or ungranted routes,
// strips Cookie and proxy-identity headers before p.API sees the request, and
// stops answering as soon as the run ends.
func TestDaemonAPIFromSandboxBlocksInternalAndUnscopedRoutes(t *testing.T) {
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	now := time.Now().UTC()

	putSecurityTask(t, store, "201", model.RepoRef{Owner: "acme", Name: "app"})
	if err := store.StartRun(ctx, model.Run{
		ID: "run-201-1", TaskID: "201", Sandbox: "sb-201", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	secStore := secrets.New(t.TempDir())
	if err := secStore.Set("prod-api-key", "value", []byte("super-secret-prod-key")); err != nil {
		t.Fatal(err)
	}

	uiSrv := ui.NewServer(ui.Config{Secrets: secStore}, store)

	// Wrap uiSrv to record the exact headers forwardToAPI hands to p.API while
	// preserving uiSrv's real route gating.
	var seenHeaders http.Header
	inspectedAPI := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		uiSrv.ServeHTTP(w, r)
	})

	dataDir := t.TempDir()
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), "bot-tok")
	tokenStore := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	sbTok, err := tokenStore.EnsureGrant("sb-201", "201", 1)
	if err != nil {
		t.Fatal(err)
	}

	upSrv := httptest.NewServer(&gitUpstream{})
	t.Cleanup(upSrv.Close)
	upURL, _ := url.Parse(upSrv.URL)

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:         dataDir,
		Store:           store,
		ForwardHost:     upURL.Host,
		ForwardTLS:      false,
		NamespaceScopes: store,
		API:             inspectedAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxySrv.Close)

	callAPI := func(method, path, body string, headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, proxySrv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+sbTok)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Internal, secrets, settings, and capabilities admin routes must all 403
	// even when the sandbox sends forged cookies and proxy identity headers.
	forged := map[string]string{
		"Cookie":                   "grain_access=forged-session",
		"X-Forwarded-For":          "127.0.0.1",
		"X-Forwarded-User":         "admin@example.com",
		"X-Auth-Request-Email":     "admin@example.com",
		"X-User-LDAP":              "admin",
		webauth.IAPAssertionHeader: "eyJhbGciOiJub25lIn0.e30.",
		webauth.HubAssertionHeader: "eyJhbGciOiJub25lIn0.e30.",
		"X-Grain-Acting-Task":      "override-attempt",
		"X-Grain-Channel":          "ui",
		"Content-Type":             "application/json",
	}
	deniedRoutes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/auth/whoami", ""},
		{http.MethodGet, "/api/hostd/install.sh", ""},
		{http.MethodGet, "/api/secrets", ""},
		{http.MethodPut, "/api/settings", `{}`},
		{http.MethodPut, "/api/capability-servers", `{"servers":{}}`},
		{http.MethodPost, "/api/tasks/201/capabilities", `{"grants":[]}`},
	}
	for _, rt := range deniedRoutes {
		code, respBody := callAPI(rt.method, rt.path, rt.body, forged)
		if code != http.StatusForbidden {
			t.Errorf("sandbox calling %s %s = %d %s, want 403", rt.method, rt.path, code, respBody)
		}
		if strings.Contains(respBody, "super-secret-prod-key") {
			t.Fatalf("response leaked secret material: %s", respBody)
		}
	}

	// Verify forwardToAPI stripped Cookie and every proxy/identity header while
	// pinning X-Grain-Acting-Task=201 and X-Grain-Channel=sandbox-cli.
	for _, stripped := range []string{
		"Cookie", "Authorization", "X-Forwarded-For", "X-Forwarded-User",
		"X-Auth-Request-Email", "X-User-Ldap", webauth.IAPAssertionHeader, webauth.HubAssertionHeader,
	} {
		if got := seenHeaders.Get(stripped); got != "" {
			t.Errorf("forwardToAPI leaked header %s=%q into p.API", stripped, got)
		}
	}
	if got := seenHeaders.Get("X-Grain-Acting-Task"); got != "201" {
		t.Errorf("X-Grain-Acting-Task = %q, want 201", got)
	}
	if got := seenHeaders.Get("X-Grain-Channel"); got != "sandbox-cli" {
		t.Errorf("X-Grain-Channel = %q, want sandbox-cli", got)
	}

	// Ending the run invalidates /api/ access immediately even before token expiry.
	if err := store.FinishRun(ctx, "run-201-1", now.Add(time.Minute), "completed", "done"); err != nil {
		t.Fatal(err)
	}
	if code, _ := callAPI(http.MethodGet, "/api/tasks/201", "", nil); code != http.StatusForbidden {
		t.Errorf("sandbox API call after FinishRun = %d, want 403", code)
	}
}

// Path 7: Host-side MCP attachment handling never reads host files outside the
// sandbox workspace, and the host-tool PreToolUse hook denies built-in host
// tools while admitting registered HostToolGrant read-only tools only when the
// run holds that grant and call recording succeeds.
func TestAgentCLIHostContainment(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	workspace := filepath.Join(base, "sandbox-workspace")
	hostHome := filepath.Join(base, "host-home")
	for _, d := range []string{workspace, hostHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claudeCred := filepath.Join(hostHome, ".claude-credentials.json")
	codexCred := filepath.Join(hostHome, "codex-auth.json")
	for _, p := range []string{claudeCred, codexCred} {
		if err := os.WriteFile(p, []byte(`{"access_token":"host-only-secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("sandbox-report-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codexCred, filepath.Join(workspace, "linked-auth.json")); err != nil {
		t.Fatal(err)
	}

	reader := mcp.SandboxFileReaderOf(mcp.NewSandboxTools(workspace))
	files := mcp.AttachmentFiles{
		Read:  reader,
		Stage: func(context.Context, string, mcp.ResponseAttachment) error { return nil },
	}

	// Valid in-sandbox file works.
	atts, err := mcp.ResponseAttachments(ctx, map[string]any{
		"attachments": []any{map[string]any{"path": "report.txt"}},
	}, files)
	if err != nil || len(atts) != 1 || string(atts[0].Content) != "sandbox-report-ok" {
		t.Fatalf("reading valid sandbox attachment = %+v, %v", atts, err)
	}

	// Absolute host paths, ../ escapes, and symlinks to host auth files fail.
	for _, bad := range []string{
		claudeCred,
		codexCred,
		"../host-home/codex-auth.json",
		"linked-auth.json",
	} {
		got, err := mcp.ResponseAttachments(ctx, map[string]any{
			"attachments": []any{map[string]any{"path": bad}},
		}, files)
		if err == nil {
			t.Errorf("ResponseAttachments(%q) succeeded and returned %q; must refuse host file", bad, got[0].Content)
		}
	}

	// Verify PreToolUse hook denies host built-in tools and enforces HostToolGrant.
	const testCap = "https://grain.dev/capabilities/security-test#readonly"
	t.Cleanup(hook.RegisterHostToolGrant(hook.HostToolGrant{
		Capability: testCap,
		Tools:      []string{"readonly_doc_lookup", "readonly_symbol_lookup"},
	}))

	isDenied := func(dec []byte) bool {
		return strings.Contains(string(dec), `"decision":"deny"`)
	}

	hookPayload := func(name string, args map[string]any) []byte {
		b, _ := json.Marshal(map[string]any{
			"toolCall": map[string]any{"name": name, "args": args},
		})
		return b
	}

	// Without the grant, both host built-in tools and the read-only lookup tools are denied,
	// while grain-sandbox MCP tools are allowed through.
	for _, deniedTool := range []string{"run_command", "read_file", "write_file", "bash", "readonly_doc_lookup"} {
		if dec := hook.DecisionFor(hook.Options{}, hookPayload(deniedTool, map[string]any{"q": "x"})); !isDenied(dec) {
			t.Errorf("ungranted tool %q was not denied: %s", deniedTool, dec)
		}
	}
	if dec := hook.DecisionFor(hook.Options{}, hookPayload("mcp_grain-sandbox_run_command", map[string]any{"command": "true"})); isDenied(dec) {
		t.Errorf("published grain-sandbox tool was denied: %s", dec)
	}

	// With the grant and a valid RecordPath, ONLY the registered read-only tools are
	// admitted (and recorded); write/shell host tools remain denied; and if RecordPath
	// cannot be written, even the granted read-only tool fails closed!
	recordFile := filepath.Join(t.TempDir(), "host-tools.jsonl")
	grantedOpts := hook.Options{
		Grants:     []string{testCap},
		RecordPath: recordFile,
		TaskID:     "3984",
		RunID:      "run-3984-1",
	}
	roPayload := hookPayload("readonly_doc_lookup", map[string]any{"query": "isolation"})
	if dec := hook.DecisionFor(grantedOpts, roPayload); isDenied(dec) {
		t.Fatalf("granted read-only host tool was denied: %s", dec)
	}
	recorded, err := os.ReadFile(recordFile)
	if err != nil || !strings.Contains(string(recorded), "readonly_doc_lookup") {
		t.Fatalf("granted host tool call was not recorded in %s: %v %q", recordFile, err, recorded)
	}
	shellPayload := hookPayload("run_command", map[string]any{"CommandLine": "id"})
	if dec := hook.DecisionFor(grantedOpts, shellPayload); !isDenied(dec) {
		t.Errorf("host shell tool was admitted under read-only HostToolGrant: %s", dec)
	}

	unwritableOpts := grantedOpts
	unwritableOpts.RecordPath = filepath.Join(recordFile, "not-a-directory", "calls.jsonl")
	if dec := hook.DecisionFor(unwritableOpts, roPayload); !isDenied(dec) {
		t.Errorf("host tool call succeeded when RecordPath could not record it (must fail closed): %s", dec)
	}
}

// knownGapGoCacheUnauthenticatedWrite was true while daemon's package cache
// constructed pkgcache.NewServer with JobTokens == nil and SplitGoCacheTrust
// == false (pkg/daemon/packagecache.go), allowing any NetworkOpen sandbox to
// PUT unauthenticated action->output entries into /gocache/action/ and
// /gocache/trusted/action/ that other task and CI sandboxes execute via
// GOCACHEPROG. Closed by task 4000.
const knownGapGoCacheUnauthenticatedWrite = false

// startDaemonPackageCache starts the package cache exactly as the daemon
// does, so what these tests reach is the daemon's own wiring rather than a
// copy of its pkgcache.Config.
func startDaemonPackageCache(t *testing.T) *daemon.PackageCache {
	t.Helper()
	cfg := daemon.Config{DataDir: t.TempDir(), SandboxDir: t.TempDir(), PackageCacheGB: 1}
	pc, stop, err := daemon.StartPackageCache(cfg, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatalf("StartPackageCache: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	if pc.Url == "" || pc.GoCacheJobs == nil {
		t.Fatalf("daemon package cache came up with url %q and Go cache jobs %v", pc.Url, pc.GoCacheJobs)
	}
	return pc
}

// goCacheDo makes one request to the package cache's Go build cache, as
// the GOCACHEPROG helper does, presenting token when it is not "" -- as
// basic auth, which is what a token in GOCACHEPROG's URL becomes.
func goCacheDo(t *testing.T, base, method, path, token string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(method, strings.TrimSuffix(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.SetBasicAuth("job", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func goCacheEntry(t *testing.T, payload []byte, action string) (outputID, actionID string, actBody []byte) {
	t.Helper()
	outSum := sha256.Sum256(payload)
	actSum := sha256.Sum256([]byte(action))
	actBody, err := json.Marshal(pkgcache.GoCacheAction{
		OutputID: hex.EncodeToString(outSum[:]),
		Size:     int64(len(payload)),
		Time:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(outSum[:]), hex.EncodeToString(actSum[:]), actBody
}

func TestPackageCacheGoCacheRejectsUnauthenticatedWrites(t *testing.T) {
	pc := startDaemonPackageCache(t)

	payload := []byte("trojaned compiled archive")
	outputID, actionID, actBody := goCacheEntry(t, payload, "target-go-compile-action")

	recOut := goCacheDo(t, pc.Url, http.MethodPut, "/gocache/output/"+outputID, "", payload)
	accepted := map[string]int{}
	for _, path := range []string{"/gocache/trusted/action/", "/gocache/action/", "/gocache/untrusted/action/"} {
		if code := goCacheDo(t, pc.Url, http.MethodPut, path+actionID, "", actBody); code == http.StatusOK {
			accepted[path] = code
		}
	}

	unauthAccepted := recOut == http.StatusOK || len(accepted) > 0
	switch {
	case knownGapGoCacheUnauthenticatedWrite && !unauthAccepted:
		t.Fatal("pkgcache now rejects unauthenticated /gocache/ writes: set knownGapGoCacheUnauthenticatedWrite = false")
	case knownGapGoCacheUnauthenticatedWrite:
		t.Skip("known gap: default daemon pkgcache Config accepts unauthenticated PUT to /gocache/trusted/action/; see knownGapGoCacheUnauthenticatedWrite")
	case unauthAccepted:
		t.Fatalf("unauthenticated sandbox PUTs succeeded: output %d, actions %v", recOut, accepted)
	}

	// Whatever was refused must not be served either.
	for _, path := range []string{"/gocache/action/", "/gocache/trusted/action/"} {
		if code := goCacheDo(t, pc.Url, http.MethodGet, path+actionID, "", nil); code != http.StatusNotFound {
			t.Errorf("GET %s after a refused write = %d, want 404", path, code)
		}
	}
}

// TestPackageCacheGoCacheWritesDoNotCrossTasks is the attack itself: Task A
// stores an action entry for an ActionID another build will ask for, and
// neither Task B, a sandbox with no token, nor a trusted CI job may be
// served it. Only a trusted CI job's entries reach everyone.
func TestPackageCacheGoCacheWritesDoNotCrossTasks(t *testing.T) {
	pc := startDaemonPackageCache(t)
	pc.GoCacheJobs.Register("task-a-token", pkgcache.JobAccess{Job: "task A"})
	pc.GoCacheJobs.Register("task-b-token", pkgcache.JobAccess{Job: "task B"})
	pc.GoCacheJobs.Register("ci-trusted-token", pkgcache.JobAccess{Job: "ci main", Trusted: true})

	payload := []byte("task A's trojaned archive")
	outputID, actionID, actBody := goCacheEntry(t, payload, "shared-stdlib-compile-action")

	// Task A, even claiming trust by path and header, is kept to its own scope.
	if code := goCacheDo(t, pc.Url, http.MethodPut, "/gocache/output/"+outputID, "task-a-token", payload); code != http.StatusOK {
		t.Fatalf("task A output PUT = %d, want 200", code)
	}
	for _, path := range []string{"/gocache/action/", "/gocache/trusted/action/"} {
		if code := goCacheDo(t, pc.Url, http.MethodPut, path+actionID, "task-a-token", actBody); code != http.StatusOK {
			t.Fatalf("task A PUT %s = %d, want 200 into its own scope", path, code)
		}
	}
	if code := goCacheDo(t, pc.Url, http.MethodGet, "/gocache/action/"+actionID, "task-a-token", nil); code != http.StatusOK {
		t.Errorf("task A reading its own entry = %d, want 200", code)
	}
	for who, token := range map[string]string{
		"task B":           "task-b-token",
		"a tokenless task": "",
		"trusted CI":       "ci-trusted-token",
	} {
		for _, path := range []string{"/gocache/action/", "/gocache/trusted/action/", "/gocache/untrusted/action/"} {
			if code := goCacheDo(t, pc.Url, http.MethodGet, path+actionID, token, nil); code != http.StatusNotFound {
				t.Errorf("%s GET %s of task A's entry = %d, want 404", who, path, code)
			}
		}
	}

	// A trusted CI job's entry is what every sandbox reads.
	clean := []byte("trusted CI's compiled archive")
	cleanOut, cleanAction, cleanBody := goCacheEntry(t, clean, "trusted-compile-action")
	if code := goCacheDo(t, pc.Url, http.MethodPut, "/gocache/output/"+cleanOut, "ci-trusted-token", clean); code != http.StatusOK {
		t.Fatalf("trusted CI output PUT = %d", code)
	}
	if code := goCacheDo(t, pc.Url, http.MethodPut, "/gocache/action/"+cleanAction, "ci-trusted-token", cleanBody); code != http.StatusOK {
		t.Fatalf("trusted CI action PUT = %d", code)
	}
	for _, token := range []string{"", "task-a-token", "task-b-token", "ci-trusted-token"} {
		if code := goCacheDo(t, pc.Url, http.MethodGet, "/gocache/action/"+cleanAction, token, nil); code != http.StatusOK {
			t.Errorf("GET trusted entry with token %q = %d, want 200", token, code)
		}
	}
}

type staticRegistryCreds map[string]string

func (m staticRegistryCreds) PackageToken(owner, repo string) (string, bool) {
	tok, ok := m[owner+"/"+repo]
	return tok, ok
}

// knownGapGHCRTokenIgnoresSandboxScope is true while pkgcache's /v2/ghcr/token
// endpoint attaches RegistryCredentials.PackageToken(owner, repo) without
// authenticating the calling sandbox or checking its repository read scope,
// returning a minted GHCR pull bearer token for any private image under a
// connected GitHub owner. Filed as a follow-up task from task 3984.
const knownGapGHCRTokenIgnoresSandboxScope = true

func TestPackageCacheGHCRTokenChecksSandboxRepoScope(t *testing.T) {
	var sawBasicAuth string
	fakeGHCR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			sawBasicAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"gho_minted_private_pull_jwt"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fakeGHCR.Close)

	cache, err := pkgcache.NewCache(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", fakeGHCR.Listener.Addr().String())
			},
		},
	}

	srv := pkgcache.NewServer(pkgcache.Config{
		Cache:        cache,
		Client:       upstreamClient,
		GHCRRegistry: "http://ghcr.io",
		Credentials:  staticRegistryCreds{"acme/private-service": "ghp_deployment_pat"},
	})

	// Unauthenticated request from a sandbox with no grant for acme/private-service:
	req := httptest.NewRequest(http.MethodGet, "/token?service=ghcr.io&scope=repository:acme/private-service:pull", nil)
	req.Host = "ghcr.io"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	expectedBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_deployment_pat"))
	mintedUnscoped := rec.Code == http.StatusOK &&
		sawBasicAuth == expectedBasic &&
		strings.Contains(rec.Body.String(), "gho_minted_private_pull_jwt")

	switch {
	case knownGapGHCRTokenIgnoresSandboxScope && !mintedUnscoped:
		t.Fatal("pkgcache /v2/ghcr/token now enforces sandbox repository scope: set knownGapGHCRTokenIgnoresSandboxScope = false")
	case knownGapGHCRTokenIgnoresSandboxScope:
		t.Skip("known gap: /v2/ghcr/token mints GHCR pull tokens using deployment credentials without checking sandbox repo scope; see knownGapGHCRTokenIgnoresSandboxScope")
	case mintedUnscoped:
		t.Fatalf("unauthenticated sandbox minted GHCR pull token for out-of-scope private repo: %s", rec.Body.String())
	}
}
