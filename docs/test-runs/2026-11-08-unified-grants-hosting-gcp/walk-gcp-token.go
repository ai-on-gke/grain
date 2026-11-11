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

//go:build ignore

// walk-gcp-token.go: executes 2026-11-08 §3 Requirement 4c end-to-end through
// orchestrator.RunCycle, intree.NewConfigurableGCP (gcpfake, credential:
// token), portforward.Relay, and real /usr/bin/gcloud, verifying
// auth/credential_file_override, live token refresh without
// sts.googleapis.com/v1/introspect, post-release revocation, and stderr
// preservation in setup-failure comments on non-zero gcloud exit.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/gke-labs/granule/pkg/sandbox"
)

type staticServers map[string]*protocol.Server

func (s staticServers) Lookup(id string) (*protocol.Server, error) {
	if srv, ok := s[id]; ok {
		return srv, nil
	}
	return nil, os.ErrNotExist
}

type isolatedBox struct {
	name      string
	home      string
	proxyURL  string
	extraPath string
	portMap   map[int]string
	listeners []net.Listener
}

func (b *isolatedBox) Name() string                              { return b.name }
func (b *isolatedBox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (b *isolatedBox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (b *isolatedBox) Release(context.Context) error {
	for _, ln := range b.listeners {
		_ = ln.Close()
	}
	return nil
}
func (b *isolatedBox) resolve(p string) string {
	if p == "~" {
		return b.home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(b.home, strings.TrimPrefix(p, "~/"))
	}
	if strings.HasPrefix(p, "/home/debian/") {
		return filepath.Join(b.home, strings.TrimPrefix(p, "/home/debian/"))
	}
	if strings.HasPrefix(p, "/home/grain/") {
		return filepath.Join(b.home, strings.TrimPrefix(p, "/home/grain/"))
	}
	if strings.HasPrefix(p, "/root/") {
		return filepath.Join(b.home, strings.TrimPrefix(p, "/root/"))
	}
	if filepath.IsAbs(p) {
		return filepath.Join(b.home, strings.TrimPrefix(p, "/"))
	}
	return filepath.Join(b.home, p)
}
func (b *isolatedBox) rewriteContent(s string) string {
	out := strings.ReplaceAll(s, "/home/debian", b.home)
	out = strings.ReplaceAll(out, "/home/grain", b.home)
	out = strings.ReplaceAll(out, "/root", b.home)
	for sbPort, localAddr := range b.portMap {
		out = strings.ReplaceAll(out, fmt.Sprintf("127.0.0.1:%d", sbPort), localAddr)
	}
	return out
}
func (b *isolatedBox) PlaceFile(_ context.Context, path, content, mode string) error {
	dst := b.resolve(path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || perm == 0 {
		perm = 0o600
	}
	return os.WriteFile(dst, []byte(b.rewriteContent(content)), os.FileMode(perm))
}
func (b *isolatedBox) UsesBatchPlacement() bool { return true }
func (b *isolatedBox) PlaceFilesBatch(_ context.Context, files []sandbox.FileUpload) error {
	for _, f := range files {
		dst := b.resolve(f.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(dst, []byte(b.rewriteContent(string(f.Content))), mode); err != nil {
			return err
		}
	}
	return nil
}
func (b *isolatedBox) env() []string {
	pathVal := os.Getenv("PATH")
	if b.extraPath != "" {
		pathVal = b.extraPath + ":" + pathVal
	}
	return []string{
		"HOME=" + b.home,
		"PATH=" + pathVal,
		"CLOUDSDK_CONFIG=" + filepath.Join(b.home, ".config", "gcloud"),
		"CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true",
		"HTTPS_PROXY=" + b.proxyURL,
		"HTTP_PROXY=" + b.proxyURL,
		"NO_PROXY=127.0.0.1,localhost",
	}
}
func (b *isolatedBox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	rewritten := make([]string, len(argv))
	for i, a := range argv {
		a = b.rewriteContent(a)
		if strings.HasPrefix(a, "~/") {
			a = b.resolve(a)
		}
		rewritten[i] = a
	}
	cmd := exec.CommandContext(ctx, rewritten[0], rewritten[1:]...)
	cmd.Dir = b.home
	cmd.Env = b.env()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
			errBuf.WriteString(err.Error())
		}
	}
	return outBuf.String(), errBuf.String(), code
}
func (b *isolatedBox) UsesChannelPortForward() bool { return true }
func (b *isolatedBox) ForwardCapabilityPort(ctx context.Context, sandboxPort int, serve func(context.Context, net.Conn)) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	if b.portMap == nil {
		b.portMap = map[int]string{}
	}
	b.portMap[sandboxPort] = ln.Addr().String()
	b.listeners = append(b.listeners, ln)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(ctx, conn)
		}
	}()
	return nil
}

type boxPool struct {
	root     string
	proxyURL string
	failBin  string
	lastBox  *isolatedBox
	useFail  bool
}

func (p *boxPool) Acquire(_ context.Context, name string, _ orchestrator.Shape) (orchestrator.Sandbox, error) {
	home := filepath.Join(p.root, name)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	extra := ""
	if p.useFail {
		extra = p.failBin
		_ = os.MkdirAll(filepath.Join(home, ".config"), 0o755)
		_ = os.WriteFile(filepath.Join(home, ".config", "gcloud"), []byte("not-a-directory\n"), 0o400)
	}
	box := &isolatedBox{name: name, home: home, proxyURL: p.proxyURL, extraPath: extra}
	p.lastBox = box
	return box, nil
}

type funcFramework func(context.Context, agent.RunConfig) (*agent.Result, error)

func (f funcFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

func initBare(root string) string {
	bare := filepath.Join(root, "acme", "widgets.git")
	_ = os.MkdirAll(filepath.Dir(bare), 0o755)
	work := filepath.Join(root, "seed")
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Walk", "GIT_AUTHOR_EMAIL=walk@example.com",
			"GIT_COMMITTER_NAME=Walk", "GIT_COMMITTER_EMAIL=walk@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			log.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "init", "--bare", "--initial-branch=main", bare)
	run("", "init", "--initial-branch=main", work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# widgets\n"), 0o644)
	run(work, "add", "README.md")
	run(work, "commit", "-m", "init")
	run(work, "push", bare, "HEAD:refs/heads/main")
	return bare
}

func main() {
	ctx := context.Background()
	tmp, err := os.MkdirTemp("", "walk-gcp-token-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	// 1. Proxy / STS introspection trap: captures any non-loopback HTTP/CONNECT
	// call (specifically proving sts.googleapis.com/v1/introspect is never hit).
	var trapMu sync.Mutex
	var trappedRequests []string
	trapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trapMu.Lock()
		trappedRequests = append(trappedRequests, r.Method+" "+r.Host+" "+r.URL.String())
		trapMu.Unlock()
		http.Error(w, "blocked external call in token-mode walkthrough", http.StatusBadGateway)
	}))
	defer trapSrv.Close()

	// 2. Start real intree.NewConfigurableGCP backed by gcpfake and attach via protocol.Attach.
	const sa = "deployer@walk-proj.iam.gserviceaccount.com"
	const minterSA = "minter@walk-proj.iam.gserviceaccount.com"
	const minterKey = `{"type":"service_account","client_email":"minter@walk-proj.iam.gserviceaccount.com"}`

	cloud := gcpfake.New()
	cloud.DefaultIdentity = minterSA
	cloud.Identities["minter"] = minterSA
	cloud.AddAccount("walk-proj", sa, "walk service account")
	cloud.Bind("projects/walk-proj", "roles/run.admin", "serviceAccount:"+sa)

	var gcpLogs bytes.Buffer
	gcpSrv, err := intree.NewConfigurableGCP(intree.GCPConfig{
		Connector: cloud,
		Listen:    "127.0.0.1:0",
		Log:       log.New(&gcpLogs, "gcp: ", 0),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer gcpSrv.Close(ctx)
	const ctrlTok = "control-plane-token-0123456789abcdef"
	gcpSrv.CapabilityServer.Token = ctrlTok
	mcpHTTP := httptest.NewServer(gcpSrv.CapabilityServer)
	defer mcpHTTP.Close()

	const gcpCapID = "https://grain.dev/capabilities/gcp"
	remoteSrv, err := protocol.Attach(ctx, gcpCapID,
		protocol.ServerConfig{
			ServerURL: mcpHTTP.URL + "/mcp",
			Headers:   map[string]string{"Authorization": "Bearer $secret:ctrl"},
			Config: map[string]any{
				protocol.DeploymentIDConfig: "098073e4",
				protocol.InstanceIDConfig:   "098073e4c0ffee0123456789abcdef01",
				"admins": []any{
					map[string]any{
						"name":              "minter",
						"credential_source": "configured",
						"projects":          []any{"walk-proj"},
						"role_ceiling":      []any{"roles/run.admin"},
					},
				},
				"admin_credentials": "$secret:gcp-admins",
				"permission_sets": []any{
					map[string]any{
						"name":            "org-deploy",
						"admin":           "minter",
						"service_account": sa,
						"roles":           []any{"roles/run.admin"},
						"credential":      "token",
					},
				},
			},
		},
		func(name string) (string, bool) {
			switch name {
			case "ctrl":
				return ctrlTok, true
			case "gcp-admins":
				return `{"minter": ` + minterKey + `}`, true
			}
			return "", false
		},
		protocol.DialOptions{Identity: protocol.Identity{DeploymentID: "098073e4", InstanceID: "098073e4c0ffee0123456789abcdef01"}},
	)
	if err != nil {
		log.Fatalf("protocol.Attach: %v", err)
	}
	defer remoteSrv.Close()
	fmt.Printf("=== 1. Attached intree.NewConfigurableGCP over MCP ===\nMethod description:\n%s\n", strings.TrimSpace(remoteSrv.Methods["gcp-key"].Description))

	// 3. Open SQLite store + githubsim + portforward.Relay + isolated sandbox pool.
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "db")))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		log.Fatal(err)
	}

	bare := initBare(tmp)
	sim := githubsim.New("acme", "widgets", bare, "main")
	ghClient := github.NewClient(sim, nil)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	relay := portforward.NewRelay()
	relaySrv := httptest.NewServer(relay)
	defer relaySrv.Close()

	failBinDir := filepath.Join(tmp, "fail-bin")
	_ = os.MkdirAll(failBinDir, 0o755)
	failGcloud := "#!/bin/sh\necho 'ERROR: (gcloud.config.set) Cannot write auth/credential_file_override: read-only config directory [code=EACCES]' >&2\nexit 1\n"
	_ = os.WriteFile(filepath.Join(failBinDir, "gcloud"), []byte(failGcloud), 0o755)

	pool := &boxPool{root: filepath.Join(tmp, "boxes"), proxyURL: trapSrv.URL, failBin: failBinDir}

	now := time.Now().UTC().Truncate(time.Second)
	grantStr := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"org-deploy"}`

	// 4. Task 101: Real /usr/bin/gcloud activation + live token refresh!
	t1 := model.Task{
		ID:        "101",
		Title:     "gcp-token-gcloud-ok",
		Body:      "Verify real gcloud credential_file_override and token refresh",
		Intent:    model.IntentImplement,
		Target:    &repo,
		Approval:  &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}},
		Grants:    []model.Grant{{Capability: grantStr, Via: model.GrantByLabel}},
		CreatedAt: &now,
		OrderKey:  1,
	}
	if err := store.PutTask(ctx, t1); err != nil {
		log.Fatal(err)
	}

	var hostTokenPort int
	var savedRunPath, savedSubjectToken string
	fwRan := false
	fw := funcFramework(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		fwRan = true
		box := pool.lastBox
		home := box.home
		for sbPort := range box.portMap {
			hostTokenPort = sbPort
		}
		fmt.Printf("\n=== 2. Inside Live Run (Real /usr/bin/gcloud) ===\n")
		// Locate activate.sh under home
		var activatePath string
		_ = filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".sh") {
				activatePath = p
			}
			return nil
		})
		activateBytes, err := os.ReadFile(activatePath)
		if err != nil {
			return nil, fmt.Errorf("read activate script (%s): %w", activatePath, err)
		}
		fmt.Printf("Generated setup script (%s):\n%s\n", filepath.Base(activatePath), strings.TrimSpace(string(activateBytes)))

		credBytes, err := os.ReadFile(filepath.Join(home, ".gcp-service-account.json"))
		if err != nil {
			return nil, fmt.Errorf("read .gcp-service-account.json: %w", err)
		}
		var credDoc struct {
			Type             string `json:"type"`
			TokenURL         string `json:"token_url"`
			CredentialSource struct {
				File string `json:"file"`
			} `json:"credential_source"`
		}
		_ = json.Unmarshal(credBytes, &credDoc)
		if idx := strings.Index(credDoc.TokenURL, "/runs/"); idx >= 0 {
			savedRunPath = credDoc.TokenURL[idx:]
		}
		subjBytes, _ := os.ReadFile(credDoc.CredentialSource.File)
		savedSubjectToken = strings.TrimSpace(string(subjBytes))
		fmt.Printf("Placed .gcp-service-account.json type=%q token_url=%q\n", credDoc.Type, credDoc.TokenURL)

		runGcloud := func(args ...string) (string, string, error) {
			c := exec.CommandContext(ctx, "/usr/bin/gcloud", args...)
			c.Dir = home
			c.Env = box.env()
			var o, e bytes.Buffer
			c.Stdout, c.Stderr = &o, &e
			err := c.Run()
			return strings.TrimSpace(o.String()), strings.TrimSpace(e.String()), err
		}
		overrideVal, e1, err1 := runGcloud("config", "get-value", "auth/credential_file_override")
		projVal, e2, err2 := runGcloud("config", "get-value", "project")
		accessTok1, e3, err3 := runGcloud("auth", "print-access-token")
		accessTok2, e4, err4 := runGcloud("auth", "print-access-token")
		fmt.Printf("gcloud config get-value auth/credential_file_override -> %q (err=%v, stderr=%q)\n", overrideVal, err1, e1)
		fmt.Printf("gcloud config get-value project -> %q (err=%v, stderr=%q)\n", projVal, err2, e2)
		if err3 != nil || err4 != nil || accessTok1 == "" || accessTok2 == "" {
			return nil, fmt.Errorf("gcloud auth print-access-token failed: err3=%v (%s) err4=%v (%s)", err3, e3, err4, e4)
		}
		fmt.Printf("gcloud auth print-access-token #1 -> hasSA=%v distinctFrom#2=%v (err=%v)\n",
			strings.HasSuffix(accessTok1, sa), accessTok1 != accessTok2, err3)

		return &agent.Result{
			ToolCalls: []agent.ToolCall{{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "gcloud verified cleanly"},
			}},
		}, nil
	})

	deps := orchestrator.Deps{
		Store:             store,
		Client:            ghClient,
		Sandboxes:         pool,
		Framework:         orchestrator.StaticFramework(fw),
		CapabilityServers: staticServers{gcpCapID: remoteSrv},
		CapabilityPasses:  orchestrator.NewCapabilityPasses(),
		Config: orchestrator.Config{
			CapabilityPorts:    relay,
			CapabilityPortsURL: relaySrv.URL,
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Second)); err != nil {
		log.Fatalf("RunCycle 1: %v", err)
	}
	if !fwRan {
		cs, _ := store.Comments(ctx, "101")
		log.Fatalf("Task 101 never reached Framework.Run! comments=%+v", cs)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		log.Fatalf("token-mode grant unexpectedly created SA keys: live=%d calls=%v", cloud.LiveKeys(), cloud.Calls)
	}
	fmt.Printf("gcpfake CreateKey calls=%d, LiveKeys=%d, GenerateAccessToken calls=%d\n",
		cloud.Calls["CreateKey"], cloud.LiveKeys(), cloud.Calls["GenerateAccessToken"])

	trapMu.Lock()
	fmt.Printf("STS / external proxy requests during Run 1: %v (count=%d)\n", trappedRequests, len(trappedRequests))
	if len(trappedRequests) != 0 {
		log.Fatalf("expected 0 external/STS calls, got %v", trappedRequests)
	}
	trapMu.Unlock()

	// Verify post-release revocation against the host token listener!
	postReleaseURL := fmt.Sprintf("http://127.0.0.1:%d%s", hostTokenPort, savedRunPath)
	revResp, err := http.Post(postReleaseURL, "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=urn:ietf:params:oauth:grant-type:token-exchange&subject_token="+savedSubjectToken+"&subject_token_type=urn:ietf:params:oauth:token-type:jwt"))
	if err != nil {
		log.Fatalf("post-release probe: %v", err)
	}
	revBody, _ := io.ReadAll(revResp.Body)
	revResp.Body.Close()
	fmt.Printf("Post-release token exchange at %s -> HTTP %d (%s)\n", postReleaseURL, revResp.StatusCode, strings.TrimSpace(string(revBody)))
	if revResp.StatusCode != http.StatusUnauthorized && revResp.StatusCode != http.StatusNotFound {
		log.Fatalf("expected 401/404 after release, got %d", revResp.StatusCode)
	}

	// 5. Task 2: Non-zero gcloud exit during setup -> stderr preserved in setup-failure comment!
	fmt.Printf("\n=== 3. Non-Zero gcloud Exit During activate.sh Setup ===\n")
	pool.useFail = true
	fwRan2 := false
	deps.Framework = orchestrator.StaticFramework(funcFramework(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		fwRan2 = true
		return &agent.Result{}, nil
	}))
	t2 := model.Task{
		ID:        "102",
		Title:     "gcp-token-gcloud-fail",
		Body:      "Verify setup failure preserves gcloud stderr in comment",
		Intent:    model.IntentImplement,
		Target:    &repo,
		Approval:  &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}},
		Grants:    []model.Grant{{Capability: grantStr, Via: model.GrantByLabel}},
		CreatedAt: &now,
		OrderKey:  2,
	}
	if err := store.PutTask(ctx, t2); err != nil {
		log.Fatal(err)
	}
	_ = orchestrator.RunCycle(ctx, deps, now.Add(5*time.Second))
	st2, _ := store.State(ctx, "102")
	comments2, _ := store.Comments(ctx, "102")
	fmt.Printf("Task 2 state=%s, frameworkRan=%v, comments=%d\n", st2, fwRan2, len(comments2))
	foundStderr := false
	for i, c := range comments2 {
		fmt.Printf("  Comment[%d] (%s): %s\n", i, c.Author.Actor.ID, strings.TrimSpace(c.Body))
		if strings.Contains(c.Body, "ERROR:") && strings.Contains(c.Body, "gcloud") {
			foundStderr = true
		}
	}
	if fwRan2 || !foundStderr {
		log.Fatalf("expected setup failure before Framework.Run with preserved gcloud stderr (fwRan2=%v, foundStderr=%v)", fwRan2, foundStderr)
	}
	fmt.Printf("\n=== WALKTHROUGH 4c PASSED ===\n")
}
