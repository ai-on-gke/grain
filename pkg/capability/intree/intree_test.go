package intree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

const controlToken = "control-plane-token-0123456789abcdef"

// attach serves s's control plane over HTTP and attaches to it as Grain
// does, so every mint response is checked by protocol.ParseMintResponse.
func attach(t *testing.T, s *Server) *protocol.Server {
	t.Helper()
	t.Cleanup(func() { s.Close(context.Background()) })
	s.CapabilityServer.Token = controlToken
	ts := httptest.NewServer(s.CapabilityServer)
	t.Cleanup(ts.Close)
	ps, err := protocol.Attach(context.Background(), "https://github.com/bwsalmon/grain/capabilities/"+s.Name,
		protocol.ServerConfig{ServerURL: ts.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:control"}},
		func(name string) (string, bool) { return controlToken, name == "control" }, protocol.DialOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	t.Cleanup(func() { ps.Close() })
	return ps
}

func validated(t *testing.T, s *protocol.Server, method string, args map[string]any) map[string]any {
	t.Helper()
	info := s.Methods[method]
	if info == nil {
		t.Fatalf("method %s not advertised; have %v", method, s.Methods)
	}
	out, err := protocol.ValidateArgs(info.ArgsSchema, args)
	if err != nil {
		t.Fatalf("ValidateArgs(%v): %v", args, err)
	}
	return out
}

func mint(t *testing.T, s *protocol.Server, method, attempt string, args map[string]any) *protocol.MintResponse {
	t.Helper()
	resp, err := s.Mint(context.Background(), method, attempt, time.Now().Add(time.Hour), validated(t, s, method, args))
	if err != nil {
		t.Fatalf("Mint %s: %v", method, err)
	}
	return resp
}

func placement(t *testing.T, r *protocol.MintResponse, path string) protocol.Placement {
	t.Helper()
	for _, p := range r.Placements {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no placement at %s in %+v", path, r.Placements)
	return protocol.Placement{}
}

// The args schemas are strict allowlists: nothing undeclared, and every
// open string anchored.
func TestEveryArgsSchemaIsAStrictAllowlist(t *testing.T) {
	servers := map[string]*Server{
		"gcp":         newTestGCP(t, gcpfake.New(), nil),
		"git":         newTestGit(t, "http://127.0.0.1:1"),
		"model-proxy": newTestModelProxy(t, "http://127.0.0.1:1"),
		"skill":       newTestSkill(t),
	}
	for name, s := range servers {
		ps := attach(t, s)
		for method, info := range ps.Methods {
			if info.ArgsSchema["additionalProperties"] != false {
				t.Errorf("%s#%s: additionalProperties = %v, want false", name, method, info.ArgsSchema["additionalProperties"])
			}
			props, _ := info.ArgsSchema["properties"].(map[string]any)
			for prop, raw := range props {
				ps, _ := raw.(map[string]any)
				if ps["type"] != "string" || ps["enum"] != nil {
					continue
				}
				pat, _ := ps["pattern"].(string)
				if !strings.HasPrefix(pat, "^") || !strings.HasSuffix(pat, "$") {
					t.Errorf("%s#%s: %s has pattern %q, want an anchored one", name, method, prop, pat)
				}
			}
			if _, err := protocol.ValidateArgs(info.ArgsSchema, map[string]any{"command": "sh"}); err == nil {
				t.Errorf("%s#%s accepted an undeclared argument", name, method)
			}
		}
	}
}

// --- gcp ---

// testGCPConfig is two permission sets over the fake: "reader", created
// on demand, and "legacy", a migrated fixed pool of two accounts.
func testGCPConfig(t *testing.T) *gcpsets.Config {
	t.Helper()
	c, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: testIdentity.DeploymentID,
		protocol.InstanceIDConfig:   testIdentity.InstanceID,
		"admins":                    []any{map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/storage.objectViewer"}}},
		"admin_credentials":         `{"minter": {"type":"service_account","client_email":"minter@grain-test-proj.iam.gserviceaccount.com"}}`,
		"permission_sets": []any{
			map[string]any{"name": "reader", "admin": "minter", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}, "max_lease": "4h"},
			map[string]any{"name": "legacy", "admin": "minter", "project": "grain-test-proj", "accounts": []any{"agent-0@grain-test-proj.iam.gserviceaccount.com", "agent-1@grain-test-proj.iam.gserviceaccount.com"}, "max_lease": "4h"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestGCP(t *testing.T, cloud *gcpfake.Cloud, now func() time.Time) *Server {
	t.Helper()
	if now != nil {
		cloud.Now = now
	}
	cloud.AddAccount("grain-test-proj", "agent-0@grain-test-proj.iam.gserviceaccount.com", "")
	cloud.AddAccount("grain-test-proj", "agent-1@grain-test-proj.iam.gserviceaccount.com", "")
	s, err := NewGCP(GCPConfig{Fallback: testGCPConfig(t), Connector: cloud, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func liveKeys(cloud *gcpfake.Cloud) int { return cloud.LiveKeys() }

func TestGCPKeyLeaseIsPlacedActivatedAndRevoked(t *testing.T) {
	cloud := gcpfake.New()
	ps := attach(t, newTestGCP(t, cloud, nil))
	// No arguments: the first permission set, as every grant before sets.
	resp := mint(t, ps, "gcp-key", "e1-1-0", nil)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	key := placement(t, resp, GCPKeyPath)
	if key.Mode != "0600" || !strings.Contains(key.Content, "grain-049f1e09-reader-1@grain-test-proj") {
		t.Errorf("key placement = %s mode %s", key.Path, key.Mode)
	}
	if resp.Setup != "/home/debian/.gcp-key/e1-1-0/activate.sh" {
		t.Errorf("setup = %q", resp.Setup)
	}
	script := placement(t, resp, resp.Setup)
	if !strings.Contains(script.Content, "--key-file='"+GCPKeyPath+"'") || !strings.Contains(script.Content, "config set project 'grain-test-proj'") {
		t.Errorf("setup script = %q", script.Content)
	}
	for _, want := range []string{"permission set reader", "grain-049f1e09-reader-1@grain-test-proj.iam.gserviceaccount.com", "project grain-test-proj"} {
		if !strings.Contains(resp.PromptSection, want) {
			t.Errorf("prompt section %q lacks %q", resp.PromptSection, want)
		}
	}
	// A second set on the same task, not primary: its own path, no setup.
	other := mint(t, ps, "gcp-key", "e1-2-0", map[string]any{"permission_set": "legacy", "primary": false})
	if other.Setup != "" || len(other.Placements) != 2 || other.Placements[0].Path != "/home/debian/.gcp-keys/legacy.json" || other.Placements[1].Path != "/home/debian/.gcp-keys/legacy.about" {
		t.Errorf("secondary = %+v", other)
	}
	if liveKeys(cloud) != 2 {
		t.Fatalf("live keys = %d", liveKeys(cloud))
	}
	for _, id := range []string{"e1-1-0", "e1-2-0"} {
		if _, err := ps.Release(context.Background(), "gcp-key", id); err != nil {
			t.Fatal(err)
		}
	}
	if liveKeys(cloud) != 0 {
		t.Error("release left keys behind")
	}
}

// The prompt and the file beside the key both name the permission set,
// the account and the project, so an agent can check it holds the
// credentials it was meant to get.
func TestGCPKeyPromptAndAboutFileNameTheSetAccountAndProject(t *testing.T) {
	cloud := gcpfake.New()
	ps := attach(t, newTestGCP(t, cloud, nil))
	resp := mint(t, ps, "gcp-key", "e1-1-0", map[string]any{"permission_set": "reader"})
	defer ps.Release(context.Background(), "gcp-key", "e1-1-0")
	account := "grain-049f1e09-reader-1@grain-test-proj.iam.gserviceaccount.com"
	about := placement(t, resp, "/home/debian/.gcp-service-account.about")
	for _, want := range []string{"permission_set: reader\n", "service_account: " + account + "\n", "project: grain-test-proj\n", "key: " + GCPKeyPath + "\n"} {
		if !strings.Contains(about.Content, want) {
			t.Errorf("about file %q lacks %q", about.Content, want)
		}
	}
	if strings.Contains(about.Content, "private_key") || about.Mode != "0644" {
		t.Errorf("about file must hold nothing secret: %+v", about)
	}
	for _, want := range []string{"permission set reader", "service account " + account, "in project grain-test-proj", "/home/debian/.gcp-service-account.about"} {
		if !strings.Contains(resp.PromptSection, want) {
			t.Errorf("prompt section %q lacks %q", resp.PromptSection, want)
		}
	}
}

// A mint says which failure it is: credentials only a person can fix and
// an IAM denial are refusals naming what to do, a token fetch that timed
// out even on its retry is transient, and credentials that failed are
// read afresh on the next mint.
func TestGCPKeyMintFailuresSayWhichFailureItIs(t *testing.T) {
	cloud := gcpfake.New()
	ps := attach(t, newTestGCP(t, cloud, nil))
	args := map[string]any{"permission_set": "reader"}
	call := func(id string) (*protocol.MintResponse, error) {
		return ps.Mint(context.Background(), "gcp-key", id, time.Now().Add(time.Hour), validated(t, ps, "gcp-key", args))
	}

	cloud.ListErrs = []error{&gcpsets.CredentialError{Admin: "minter", Source: gcpsets.SourceAmbient, Kind: gcpsets.CredentialExpired, Err: errors.New(`oauth2: "invalid_grant"`)}}
	resp, err := call("e1-1-0")
	if err != nil || resp.Status != protocol.StatusRefused || !strings.Contains(resp.Reason, "expired or revoked") || !strings.Contains(resp.Reason, "gcloud auth application-default login") {
		t.Fatalf("expired: %+v %v", resp, err)
	}
	connects := cloud.Connects
	if ok := mint(t, ps, "gcp-key", "e1-2-0", args); ok.Status != protocol.StatusOK || cloud.Connects != connects+1 {
		t.Errorf("after expired credentials the next mint should connect afresh: %+v, connects %d -> %d", ok, connects, cloud.Connects)
	}
	ps.Release(context.Background(), "gcp-key", "e1-2-0")

	cloud.ListErrs = []error{&gcpsets.CredentialError{Admin: "minter", Source: gcpsets.SourceAmbient, Kind: gcpsets.CredentialMissing, Err: errors.New("could not find default credentials")}}
	if resp, err := call("e1-3-0"); err != nil || resp.Status != protocol.StatusRefused || !strings.Contains(resp.Reason, "no ambient credentials") {
		t.Errorf("missing: %+v %v", resp, err)
	}

	cloud.ListErrs = []error{&gcpsets.CredentialError{Admin: "minter", Source: gcpsets.SourceAmbient, Kind: gcpsets.CredentialTimeout, Err: context.Canceled}}
	if resp, err := call("e1-4-0"); err == nil || !strings.Contains(err.Error(), "transient") {
		t.Errorf("timeout should be a transient error: %+v %v", resp, err)
	}

	cloud.ListErrs = []error{errors.New("listing service accounts in grain-test-proj: googleapi: Error 403: Permission 'iam.serviceAccounts.list' denied, forbidden")}
	if resp, err := call("e1-5-0"); err != nil || resp.Status != protocol.StatusRefused || !strings.Contains(resp.Reason, "IAM denied admin minter in project grain-test-proj") {
		t.Errorf("denied: %+v %v", resp, err)
	}
	if liveKeys(cloud) != 0 {
		t.Errorf("failed mints left %d keys", liveKeys(cloud))
	}
}

func TestGCPKeyOldAccountArgumentStillWorks(t *testing.T) {
	cloud := gcpfake.New()
	ps := attach(t, newTestGCP(t, cloud, nil))
	resp := mint(t, ps, "gcp-key", "e1-1-0", map[string]any{"account": "default", "activate": true})
	if resp.Status != protocol.StatusOK || !strings.Contains(resp.PromptSection, "permission set reader") {
		t.Fatalf("mint = %+v", resp)
	}
	ps.Release(context.Background(), "gcp-key", "e1-1-0")
}

func TestGCPKeyAtTheLimitIsResourceExhausted(t *testing.T) {
	cloud := gcpfake.New()
	ps := attach(t, newTestGCP(t, cloud, nil))
	for _, e := range []string{"agent-0", "agent-1"} {
		for i := 0; i < gcpsets.KeysPerAccount; i++ {
			cloud.AddKey(e+"@grain-test-proj.iam.gserviceaccount.com", fmt.Sprintf("k%d", i), time.Now())
		}
	}
	resp := mint(t, ps, "gcp-key", "e1-1-0", map[string]any{"permission_set": "legacy"})
	if resp.Status != protocol.StatusResourceExhausted || resp.RetryAfter == "" {
		t.Errorf("mint = %+v, want resource_exhausted with retry_after", resp)
	}
}

func TestGCPKeyRefusesADeadlinePastTheKeyLifetime(t *testing.T) {
	ps := attach(t, newTestGCP(t, gcpfake.New(), nil))
	resp, err := ps.Mint(context.Background(), "gcp-key", "e1-1-0", time.Now().Add(5*time.Hour), validated(t, ps, "gcp-key", nil))
	if err != nil || resp.Status != protocol.StatusRefused {
		t.Errorf("5h deadline = %+v, %v; want refused", resp, err)
	}
	if _, err := protocol.ValidateArgs(ps.Methods["gcp-key"].ArgsSchema, map[string]any{"permission_set": "prod"}); err == nil {
		t.Error("a permission set nobody configured was accepted")
	}
}

// The permission_set argument is an enum of the configured sets, its
// default the first, so a grant form draws a dropdown.
func TestGCPKeyPermissionSetIsAnEnumOfTheConfiguredSets(t *testing.T) {
	ps := attach(t, newTestGCP(t, gcpfake.New(), nil))
	props := ps.Methods["gcp-key"].ArgsSchema["properties"].(map[string]any)
	set := props["permission_set"].(map[string]any)
	if !reflect.DeepEqual(set["enum"], []any{"reader", "legacy"}) || set["default"] != "reader" {
		t.Errorf("permission_set = %v", set)
	}
}

func TestGCPKeyExpiresWithoutRelease(t *testing.T) {
	cloud := gcpfake.New()
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s := newTestGCP(t, cloud, clock)
	ps := attach(t, s)
	if _, err := ps.Mint(context.Background(), "gcp-key", "e1-1-0", now.Add(time.Hour), validated(t, ps, "gcp-key", nil)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	now = now.Add(time.Hour + protocol.DefaultGrace + time.Second)
	mu.Unlock()
	if got := s.Methods[0].Expire(context.Background()); len(got) != 1 {
		t.Fatalf("expired %v, want the one lease", got)
	}
	if liveKeys(cloud) != 0 {
		t.Error("the expired lease's key was not deleted")
	}
}

// Closing the server -- Grain closing its stdin -- revokes what it holds.
func TestServeReleasesEveryLeaseOnShutdown(t *testing.T) {
	cloud := gcpfake.New()
	s := newTestGCP(t, cloud, nil)
	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), in, io.Discard) }()
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_gcp-key","arguments":{"attempt_id":"e1-1-0","deadline":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","args":{}}}}` + "\n"
	if _, err := w.Write([]byte(call)); err != nil {
		t.Fatal(err)
	}
	for i := 0; liveKeys(cloud) == 0 && i < 200; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if liveKeys(cloud) != 1 {
		t.Fatal("mint never happened")
	}
	w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if liveKeys(cloud) != 0 {
		t.Error("a lease outlived its server")
	}
}

// listenerAddr is the address of s's one data-plane listener, read off
// its log.
func listenerAddr(t *testing.T, s *Server) string {
	t.Helper()
	if s.addr == "" {
		t.Fatal("server has no data-plane listener")
	}
	return s.addr
}

// --- git ---

type upstreamLog struct {
	mu   sync.Mutex
	reqs []string
}

func (u *upstreamLog) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, r.Method+" "+r.URL.RequestURI()+" auth="+pass+" body="+string(body))
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		io.WriteString(w, "ok")
	})
}

func (u *upstreamLog) all() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.reqs...)
}

func newTestGit(t *testing.T, upstream string) *Server {
	t.Helper()
	s, err := NewGit(GitConfig{Listen: "127.0.0.1:0", Upstream: upstream, Token: "upstream-secret", Repos: []string{"bwsalmon/*", "other/one"}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGitRepoRootAndAdditionalGrants(t *testing.T) {
	ps := attach(t, newTestGit(t, "http://127.0.0.1:1"))
	root := mint(t, ps, "git-repo", "e1-1-0", map[string]any{"repo": "bwsalmon/grain", "mode": "write", "base": "main", "branch": "grain/task-214"})
	if root.SetupRoot != "/home/debian/.git-repo/e1-1-0/setup.sh" || root.Setup != "" {
		t.Errorf("root grant: setup_root %q setup %q", root.SetupRoot, root.Setup)
	}
	creds := placement(t, root, "/home/debian/.git-repo/e1-1-0/credentials")
	if creds.Mode != "0600" || !strings.HasPrefix(creds.Content, "http://x-access-token:") {
		t.Errorf("credentials = %+v", creds)
	}
	script := placement(t, root, root.SetupRoot)
	for _, want := range []string{"store --file=/home/debian/.git-repo/e1-1-0/credentials", "/bwsalmon/grain.git", "'grain/task-214' 'origin/main'"} {
		if !strings.Contains(script.Content, want) {
			t.Errorf("setup script lacks %q:\n%s", want, script.Content)
		}
	}
	if len(root.Ports) != 1 || root.Ports[0].HostPort < 1024 {
		t.Errorf("ports = %+v", root.Ports)
	}

	extra := mint(t, ps, "git-repo", "e1-1-1", map[string]any{"repo": "bwsalmon/silo", "root": false})
	if extra.Setup != "/home/debian/.git-repo/e1-1-1/setup.sh" || extra.SetupRoot != "" {
		t.Errorf("additional grant: setup_root %q setup %q", extra.SetupRoot, extra.Setup)
	}
	if !strings.Contains(extra.PromptSection, "/home/debian/repos/bwsalmon/silo") {
		t.Errorf("prompt section = %q", extra.PromptSection)
	}

	write := mint(t, ps, "git-repo", "e1-2-0", map[string]any{"repo": "bwsalmon/grain", "mode": "write"})
	if write.Status != protocol.StatusRefused {
		t.Errorf("write without a branch: %+v", write)
	}
	for _, bad := range []map[string]any{
		{"repo": "bwsalmon/grain; rm -rf /"},
		{"repo": "../etc"},
		{"repo": "bwsalmon/grain", "branch": "-x"},
		{"repo": "bwsalmon/grain", "mode": "admin"},
	} {
		if _, err := protocol.ValidateArgs(ps.Methods["git-repo"].ArgsSchema, bad); err == nil {
			t.Errorf("args %v were accepted", bad)
		}
	}
	if r := mint(t, ps, "git-repo", "e1-3-0", map[string]any{"repo": "bwsalmon/grain", "base": "a..b"}); r.Status != protocol.StatusRefused {
		t.Errorf("base a..b: %+v", r)
	}
	// A grant's own arguments are untrusted: they cannot make a write
	// grant of the default branch, or of anything outside -push-branch.
	for i, args := range []map[string]any{
		{"repo": "bwsalmon/grain", "mode": "write", "branch": "main"},
		{"repo": "bwsalmon/grain", "mode": "write", "branch": "release/v2"},
		{"repo": "bwsalmon/grain", "mode": "write", "branch": "grain/x", "base": "grain/x"},
	} {
		if r := mint(t, ps, "git-repo", fmt.Sprintf("e1-4-%d", i), args); r.Status != protocol.StatusRefused {
			t.Errorf("write grant %v: %+v", args, r)
		}
	}
	for _, repo := range []string{"other/two", "someone/grain"} {
		if _, err := protocol.ValidateArgs(ps.Methods["git-repo"].ArgsSchema, map[string]any{"repo": repo}); err == nil {
			t.Errorf("repo %s, outside -repo, was accepted", repo)
		}
	}
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestGitProxyHoldsEachAttemptToItsGrant(t *testing.T) {
	up := &upstreamLog{}
	ts := httptest.NewServer(up.handler())
	defer ts.Close()
	s := newTestGit(t, ts.URL)
	base := "http://" + listenerAddr(t, s)
	ps := attach(t, s)
	read := mint(t, ps, "git-repo", "e1-1-0", map[string]any{"repo": "bwsalmon/grain"})
	write := mint(t, ps, "git-repo", "e1-2-0", map[string]any{"repo": "bwsalmon/grain", "mode": "write", "branch": "grain/topic"})
	token := func(r *protocol.MintResponse) string {
		for _, p := range r.Placements {
			if strings.HasSuffix(p.Path, "/credentials") {
				s := strings.TrimPrefix(strings.TrimSpace(p.Content), "http://x-access-token:")
				return s[:strings.Index(s, "@")]
			}
		}
		t.Fatal("no credentials")
		return ""
	}
	do := func(method, path, tok, body string) int {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if tok != "" {
			req.SetBasicAuth("x-access-token", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("1", 40)
	push := func(ref string) string {
		return pktLine(zero+" "+one+" "+ref+"\x00report-status\n") + "0000PACKDATA"
	}
	cases := []struct {
		name, method, path, tok, body string
		want                          int
	}{
		{"no token", "GET", "/bwsalmon/grain.git/info/refs?service=git-upload-pack", "", "", 401},
		{"fetch", "GET", "/bwsalmon/grain.git/info/refs?service=git-upload-pack", token(read), "", 200},
		{"another repo", "GET", "/bwsalmon/silo.git/info/refs?service=git-upload-pack", token(read), "", 403},
		{"dumb http", "GET", "/bwsalmon/grain.git/info/refs", token(read), "", 403},
		{"push on a read grant", "GET", "/bwsalmon/grain.git/info/refs?service=git-receive-pack", token(read), "", 403},
		{"push another branch", "POST", "/bwsalmon/grain.git/git-receive-pack", token(write), push("refs/heads/main"), 403},
		{"push a tag", "POST", "/bwsalmon/grain.git/git-receive-pack", token(write), push("refs/tags/v1"), 403},
		{"delete the branch", "POST", "/bwsalmon/grain.git/git-receive-pack", token(write), pktLine(one+" "+zero+" refs/heads/grain/topic\n") + "0000", 403},
		{"push the branch", "POST", "/bwsalmon/grain.git/git-receive-pack", token(write), push("refs/heads/grain/topic"), 200},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.tok, c.body); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	reqs := up.all()
	if len(reqs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2: %v", len(reqs), reqs)
	}
	for _, r := range reqs {
		if !strings.Contains(r, "auth=upstream-secret") || strings.Contains(r, token(read)) || strings.Contains(r, token(write)) {
			t.Errorf("upstream request %q is not authenticated as the server alone", r)
		}
	}
	if !strings.HasSuffix(reqs[1], "PACKDATA") || !strings.Contains(reqs[1], "/bwsalmon/grain.git/git-receive-pack") {
		t.Errorf("push forwarded as %q", reqs[1])
	}
	if _, err := ps.Release(context.Background(), "git-repo", "e1-1-0"); err != nil {
		t.Fatal(err)
	}
	if got := do("GET", "/bwsalmon/grain.git/info/refs?service=git-upload-pack", token(read), ""); got != 401 {
		t.Errorf("released token: %d, want 401", got)
	}
}

// --- model-proxy ---

func newTestModelProxy(t *testing.T, upstream string) *Server {
	t.Helper()
	s, err := NewModelProxy(ModelProxyConfig{Vendors: []ModelProxyVendor{
		{Vendor: modelproxy.Gemini, Listen: "127.0.0.1:0", Credential: "vendor-secret", Upstream: upstream},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestModelProxyTokenIsPlacedAndRevoked(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" key="+r.Header.Get("x-goog-api-key")+r.URL.Query().Get("key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	s := newTestModelProxy(t, ts.URL)
	ps := attach(t, s)
	if _, err := protocol.ValidateArgs(ps.Methods["model-proxy"].ArgsSchema, map[string]any{"vendor": "openai"}); err == nil {
		t.Error("an unconfigured vendor was accepted")
	}
	resp := mint(t, ps, "model-proxy", "e1-1-0", map[string]any{"vendor": "gemini"})
	url := strings.TrimSpace(placement(t, resp, "/home/debian/.gemini-proxy-url").Content)
	tok := strings.TrimSpace(placement(t, resp, "/home/debian/.gemini-proxy-token").Content)
	if len(resp.Ports) != 1 || !strings.HasSuffix(url, fmt.Sprint(resp.Ports[0].SandboxPort)) {
		t.Errorf("url %q does not use the forwarded port %+v", url, resp.Ports)
	}
	call := func() int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+s.addr+"/v1beta/models/gemini-2.5-pro:generateContent", strings.NewReader(`{}`))
		req.Header.Set("x-goog-api-key", tok)
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if got := call(); got != 200 {
		t.Fatalf("call with the minted token: %d", got)
	}
	mu.Lock()
	if len(seen) != 1 || !strings.Contains(seen[0], "key=vendor-secret") {
		t.Errorf("upstream saw %v", seen)
	}
	mu.Unlock()
	if _, err := ps.Release(context.Background(), "model-proxy", "e1-1-0"); err != nil {
		t.Fatal(err)
	}
	if got := call(); got == 200 {
		t.Error("a released token still works")
	}
}

// --- skill ---

func newTestSkill(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("review/SKILL.md", "---\nname: review\ndescription: How to review a pull request.\n---\nBody", 0o644)
	write("review/scripts/check.sh", "#!/bin/sh\n", 0o755)
	write("release/SKILL.md", "# Cutting a release\n", 0o644)
	s, err := NewSkill(SkillConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSkillIsStatelessPlacements(t *testing.T) {
	ps := attach(t, newTestSkill(t))
	if ps.Methods["skill"].Releasable {
		t.Error("skill advertises release_skill")
	}
	all := mint(t, ps, "skill", "e1-1-0", nil)
	if len(all.Placements) != 3 {
		t.Errorf("placements = %+v", all.Placements)
	}
	if p := placement(t, all, "/home/debian/.skills/review/scripts/check.sh"); p.Mode != "0755" {
		t.Errorf("script mode %s", p.Mode)
	}
	if p := placement(t, all, "/home/debian/.skills/release/SKILL.md"); p.Mode != "0644" {
		t.Errorf("SKILL.md mode %s", p.Mode)
	}
	if !strings.Contains(all.PromptSection, "How to review a pull request.") || !strings.Contains(all.PromptSection, "Cutting a release") {
		t.Errorf("prompt section = %q", all.PromptSection)
	}
	one := mint(t, ps, "skill", "e1-2-0", map[string]any{"skills": []any{"release"}})
	if len(one.Placements) != 1 {
		t.Errorf("one skill placed %+v", one.Placements)
	}
	if _, err := protocol.ValidateArgs(ps.Methods["skill"].ArgsSchema, map[string]any{"skills": []any{"../x"}}); err == nil {
		t.Error("an unknown skill was accepted")
	}
}

func TestSkillDirectoryWithUnplaceableNamesIsRefused(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ok"), 0o755)
	os.WriteFile(filepath.Join(dir, "ok", "SKILL.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "ok", "bad name.sh"), []byte("x"), 0o644)
	if _, err := NewSkill(SkillConfig{Dir: dir}); err == nil {
		t.Error("a file name with a space was accepted")
	}
}

// Every builder parses its flags and starts without its root credential,
// which configure may still supply, advertising configure and refusing
// its attach with what is missing.
func TestBuildersNeedTheirCredentials(t *testing.T) {
	empty := func(string) string { return "" }
	for name, c := range map[string]struct {
		args    []string
		missing string
	}{
		"gcp":         {[]string{"-project", "p", "-account", "a@p.iam.gserviceaccount.com"}, GCPMinterKeyEnv},
		"git":         {[]string{"-listen", "127.0.0.1:0", "-repo", "a/b"}, GitTokenEnv},
		"model-proxy": {[]string{"-vendor", "claude=127.0.0.1:0"}, ModelProxyKeyEnv("claude")},
	} {
		b, ok := Lookup(name)
		if !ok {
			t.Fatalf("no builder %s", name)
		}
		s, err := b.Build(context.Background(), c.args, empty, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var ce *protocol.ConfigureError
		if _, err := attachConfigured(t, s, nil, nil); !errors.As(err, &ce) || !strings.Contains(err.Error(), c.missing) {
			t.Errorf("%s with no credential attached: %v, want a configure error naming %s", name, err, c.missing)
		}
	}
	env := map[string]string{GCPMinterKeyEnv: testMinterKey}
	s, err := builders["gcp"].Build(context.Background(), []string{"-project", "grain-test-proj", "-account", "staging=s@grain-test-proj.iam.gserviceaccount.com*3"}, func(k string) string { return env[k] }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := attach(t, s)
	args, _ := json.Marshal(ps.Methods["gcp-key"].ArgsSchema)
	if !strings.Contains(string(args), `"enum":["staging"]`) {
		t.Errorf("gcp-key args = %s", args)
	}
}

func TestGitServerNeedsARepoAllowlistAndAnAnchoredPushPattern(t *testing.T) {
	if _, err := NewGit(GitConfig{Listen: "127.0.0.1:0", Token: "t"}); err == nil {
		t.Error("a git server with no -repo started")
	}
	if _, err := NewGit(GitConfig{Listen: "127.0.0.1:0", Token: "t", Repos: []string{"a/b"}, PushBranches: "grain/"}); err == nil {
		t.Error("an unanchored -push-branch was accepted")
	}
	schema, err := gitRepoSchema([]string{"a/b", "c/d"})
	if err != nil || schema["enum"] == nil {
		t.Errorf("exact repos: %v, %v; want an enum", schema, err)
	}
}

// gitPack is a receive-pack body updating ref from old to new, carrying
// the pack git itself would send: the commits reachable from new and not
// from old.
func gitPack(t *testing.T, dir, old, new, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "pack-objects", "--revs", "--stdout", "-q")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(new + "\n^" + old + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatalf("git pack-objects: %v", err)
	}
	return pktLine(old+" "+new+" "+ref+"\x00report-status\n") + "0000" + string(pack)
}

// A write grant may move its branch forward and may not force it.
func TestGitProxyRefusesAForcedPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "a")
	a := run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "b")
	b := run("rev-parse", "HEAD")
	run("checkout", "-q", "--detach", a)
	run("commit", "-q", "--allow-empty", "-m", "c")
	c := run("rev-parse", "HEAD")

	up := &upstreamLog{}
	ts := httptest.NewServer(up.handler())
	defer ts.Close()
	s := newTestGit(t, ts.URL)
	ps := attach(t, s)
	resp := mint(t, ps, "git-repo", "e1-1-0", map[string]any{"repo": "bwsalmon/grain", "mode": "write", "branch": "grain/topic"})
	creds := strings.TrimPrefix(strings.TrimSpace(placement(t, resp, "/home/debian/.git-repo/e1-1-0/credentials").Content), "http://x-access-token:")
	token := creds[:strings.Index(creds, "@")]
	push := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+s.addr+"/bwsalmon/grain.git/git-receive-pack", strings.NewReader(body))
		req.SetBasicAuth("x-access-token", token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if got := push(gitPack(t, dir, b, c, "refs/heads/grain/topic")); got != http.StatusForbidden {
		t.Errorf("forced update b -> c: %d, want 403", got)
	}
	if got := push(gitPack(t, dir, a, b, "refs/heads/grain/topic")); got != http.StatusOK {
		t.Errorf("fast-forward a -> b: %d, want 200", got)
	}
	if n := len(up.all()); n != 1 {
		t.Errorf("upstream saw %d pushes, want only the fast-forward", n)
	}
}
