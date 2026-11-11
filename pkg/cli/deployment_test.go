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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func setupTestServerAndClient(t *testing.T) (*ui.HTTPClient, *modelStoreDB) {
	t.Helper()
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})
	creds, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		t.Fatalf("LoadCredentialSet: %v", err)
	}
	uiClient := ui.NewClient(ui.Config{SourceDB: db, Actor: ui.DefaultActor("alice"), Credentials: creds}, store)
	server := accessCodeServer(uiClient)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	httpClient := accessCodeClient(ts.URL)

	return httpClient, &modelStoreDB{store: store, db: db, creds: creds}
}

type modelStoreDB struct {
	store any
	db    any
	creds *gitproxy.CredentialSet
}

func TestCLIGCPKey(t *testing.T) {
	client, _ := setupTestServerAndClient(t)
	ctx := context.Background()

	out := &printer{json: false}
	jsonOut := &printer{json: true}

	// 1. List initially empty
	if err := cmdGCPKey(ctx, client, out, []string{"list"}); err != nil {
		t.Fatalf("gcp-key list empty: %v", err)
	}

	// 2. Set key
	if err := cmdGCPKey(ctx, client, out, []string{
		"set", "analytics",
		"-project", "analytics-proj",
		"-account", "sa@analytics-proj.iam.gserviceaccount.com",
		"-count", "2",
		"-max-age", "2h",
		"-minter", "minter-analytics",
	}); err != nil {
		t.Fatalf("gcp-key set: %v", err)
	}

	// 3. Get key
	if err := cmdGCPKey(ctx, client, out, []string{"get", "analytics"}); err != nil {
		t.Fatalf("gcp-key get: %v", err)
	}

	// 4. Get key with JSON
	if err := cmdGCPKey(ctx, client, jsonOut, []string{"get", "analytics"}); err != nil {
		t.Fatalf("gcp-key get json: %v", err)
	}

	// 5. List with JSON
	if err := cmdGCPKey(ctx, client, jsonOut, []string{"list"}); err != nil {
		t.Fatalf("gcp-key list json: %v", err)
	}
	keys, err := client.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Name != "analytics" {
		t.Fatalf("the deployment's keys after set = %+v, want analytics", keys)
	}

	// 6. Delete key
	if err := cmdGCPKey(ctx, client, out, []string{"delete", "analytics"}); err != nil {
		t.Fatalf("gcp-key delete: %v", err)
	}

	// 7. Verify deleted
	keys, err = client.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestCLIDefaultModelIfNone(t *testing.T) {
	client, _ := setupTestServerAndClient(t)
	ctx := context.Background()
	out := &printer{json: false}

	if err := cmdDefaultModel(ctx, client, out, []string{"--if-none"}); err == nil {
		t.Error("default-model with no model was accepted")
	}
	if err := cmdDefaultModel(ctx, client, out, []string{"--if-none", "claude-opus-5-high"}); err != nil {
		t.Fatalf("default-model: %v", err)
	}
	team, err := client.GetDeployment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if team.DefaultModel != "claude-opus-5-high" {
		t.Fatalf("default model = %q, want claude-opus-5-high", team.DefaultModel)
	}
	// --if-none leaves a model already chosen alone; without it, it is replaced.
	if err := cmdDefaultModel(ctx, client, out, []string{"--if-none", "gemini-3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	if team, _ = client.GetDeployment(ctx); team.DefaultModel != "claude-opus-5-high" {
		t.Fatalf("--if-none replaced the default model with %q", team.DefaultModel)
	}
	if err := cmdDefaultModel(ctx, client, out, []string{"gemini-3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	if team, _ = client.GetDeployment(ctx); team.DefaultModel != "gemini-3.1-pro" {
		t.Fatalf("default model = %q, want gemini-3.1-pro", team.DefaultModel)
	}
}

// --if-unserved keeps a default model one of the team's enabled
// connections serves, and replaces one nothing serves -- what a fresh
// install's default team inherits from the deployment's legacy model.
func TestCLIDefaultModelIfUnserved(t *testing.T) {
	client, _ := setupTestServerAndClient(t)
	ctx := context.Background()
	out := &printer{json: false}

	if err := cmdDefaultModel(ctx, client, out, []string{"gemini-3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdDefaultModel(ctx, client, out, []string{"--if-unserved", "claude-opus-5-high"}); err != nil {
		t.Fatalf("default-model --if-unserved: %v", err)
	}
	team, err := client.GetDeployment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if team.DefaultModel != "claude-opus-5-high" {
		t.Fatalf("--if-unserved kept a default no connection serves: %q", team.DefaultModel)
	}

	backends := []model.AgentBackend{{
		ID: "workstation-antigravity", Name: "Antigravity (ambient login)", Framework: "antigravity",
		AuthMode: "ambient", Models: []string{"claude-opus-5-high"}, Team: "default",
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends}); err != nil {
		t.Fatalf("adding a connection: %v", err)
	}
	if err := cmdDefaultModel(ctx, client, out, []string{"--if-unserved", "gemini-3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	if team, _ = client.GetDeployment(ctx); team.DefaultModel != "claude-opus-5-high" {
		t.Fatalf("--if-unserved replaced a served default model with %q", team.DefaultModel)
	}
}

func TestCLIDefaultModelMultiple(t *testing.T) {
	client, _ := setupTestServerAndClient(t)
	ctx := context.Background()
	out := &printer{json: false}

	if err := cmdDefaultModel(ctx, client, out, []string{"claude-opus-5-high", "gemini-3.1-pro", "claude-opus-5-high"}); err != nil {
		t.Fatalf("default-model multiple: %v", err)
	}
	team, err := client.GetDeployment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if team.DefaultModel != "claude-opus-5-high,gemini-3.1-pro" {
		t.Fatalf("default model = %q, want claude-opus-5-high,gemini-3.1-pro", team.DefaultModel)
	}
	if len(team.DefaultModels) != 2 || team.DefaultModels[0] != "claude-opus-5-high" || team.DefaultModels[1] != "gemini-3.1-pro" {
		t.Fatalf("defaultModels = %+v, want [claude-opus-5-high gemini-3.1-pro]", team.DefaultModels)
	}
}

func TestServesModel(t *testing.T) {
	backends := []model.AgentBackend{
		{ID: "a", Framework: "antigravity", Models: []string{"claude-opus-5-high"}, Team: "default"},
		{ID: "b", Framework: "antigravity", Models: []string{"gemini-3.1-pro"}, Team: "default", Disabled: true},
		// Every stored connection is the deployment's, whichever team's
		// editor once listed it.
		{ID: "c", Framework: "antigravity", Models: []string{"gemini-3-flash"}, Team: "infra"},
	}
	for name, want := range map[string]bool{
		"claude-opus-5-high":                 true,
		"gemini-3.1-pro":                     false, // only on a disabled connection
		"gemini-3.1-pro, claude-opus-5-high": true,  // fallback model is served
		"gemini-3-flash":                     true,
		"":                                   false,
	} {
		if got := servesModel(backends, name); got != want {
			t.Errorf("servesModel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestGitConnectionOnMatchesByHost(t *testing.T) {
	team := ui.Team{GitConnections: []model.TeamGitConnection{
		{Credential: "onprem", Host: "git.forge.example"},
	}}
	if got, ok := gitConnectionOn(team, "git.forge.example"); !ok || got != "onprem" {
		t.Errorf("onprem: %q, %v", got, ok)
	}
	if _, ok := gitConnectionOn(team, "github.com"); ok {
		t.Error("a git.forge.example connection counted as one on github.com")
	}
	team = ui.Team{HasGitCredential: true, GitCredential: "legacy"}
	if got, ok := gitConnectionOn(team, "github.com"); !ok || got != "legacy" {
		t.Errorf("legacy github.com credential: %q, %v", got, ok)
	}
	if _, ok := gitConnectionOn(team, "git.forge.example"); ok {
		t.Error("a github.com credential counted as one on git.forge.example")
	}
}

func TestCLIBroadcast(t *testing.T) {
	client, dbWrap := setupTestServerAndClient(t)
	store := dbWrap.store.(*model.Store)
	ctx := context.Background()
	out := &printer{json: false}

	if err := cmdBroadcast(ctx, client, out, nil); err == nil {
		t.Fatal("broadcast with no message should fail")
	}
	if err := cmdBroadcast(ctx, client, out, []string{"hello", "-all"}); err == nil {
		t.Fatal("broadcast with misplaced -all flag should fail")
	}

	// No running tasks yet.
	if err := cmdBroadcast(ctx, client, out, []string{"Anyone running?"}); err != nil {
		t.Fatalf("broadcast with no running tasks: %v", err)
	}

	running, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Running task", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        dispatch.RunID(running.ID, 1),
		TaskID:    running.ID,
		Attempt:   1,
		StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	queued, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Queued task", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask queued: %v", err)
	}

	// Default: running task agents only.
	if err := cmdBroadcast(ctx, client, out, []string{"Hold", "off", "on", "pushing"}); err != nil {
		t.Fatalf("cmdBroadcast running only: %v", err)
	}
	rComments, err := store.Comments(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rComments) != 1 || rComments[0].Body != "Hold off on pushing" {
		t.Fatalf("running task comments = %+v, want 'Hold off on pushing'", rComments)
	}
	qComments, err := store.Comments(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qComments) != 0 {
		t.Fatalf("queued task comments = %+v, want none when -all is not set", qComments)
	}

	// With -all: reaches queued tasks as well.
	if err := cmdBroadcast(ctx, client, &printer{json: true}, []string{"-all", "All hands notice"}); err != nil {
		t.Fatalf("cmdBroadcast -all: %v", err)
	}
	qComments, err = store.Comments(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qComments) != 1 || qComments[0].Body != "All hands notice" {
		t.Fatalf("queued task comments after -all = %+v, want 'All hands notice'", qComments)
	}
}

func TestCLIGitLoginModesAndReplacement(t *testing.T) {
	client, dbWrap := setupTestServerAndClient(t)
	creds := dbWrap.creds
	ctx := context.Background()
	out := &printer{json: false}

	// Conflicting mode flags should be refused.
	if err := cmdGitLogin(ctx, client, out, []string{"-web", "-from-gh"}); err == nil {
		t.Fatal("expected error when both -web and -from-gh are specified")
	}

	// 1. -with-token delegates token from stdin and records explicit -author.
	origStdin := gitLoginStdin
	t.Cleanup(func() { gitLoginStdin = origStdin })
	gitLoginStdin = strings.NewReader("ghp_stdin_secret_1\n")

	if err := cmdGitLogin(ctx, client, out, []string{"-with-token", "-author", "Ada Lovelace"}); err != nil {
		t.Fatalf("cmdGitLogin -with-token: %v", err)
	}

	conns, err := client.ListDeploymentGitConnections(ctx)
	if err != nil {
		t.Fatalf("ListDeploymentGitConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 git connection, got %d (%+v)", len(conns), conns)
	}
	firstCred := conns[0].Credential
	if got := creds.AuthorOf(firstCred); got != "Ada Lovelace" {
		t.Fatalf("AuthorOf(%q) = %q, want Ada Lovelace", firstCred, got)
	}

	// 2. -from-gh delegates token from local `gh auth token` and replaces the existing connection on github.com.
	origGH := gitLoginGHToken
	t.Cleanup(func() { gitLoginGHToken = origGH })
	gitLoginGHToken = func(ctx context.Context, host string) (string, error) {
		return "gho_from_gh_cli_2", nil
	}

	if err := cmdGitLogin(ctx, client, out, []string{"-from-gh", "-author", "Grace Hopper"}); err != nil {
		t.Fatalf("cmdGitLogin -from-gh: %v", err)
	}

	conns, err = client.ListDeploymentGitConnections(ctx)
	if err != nil {
		t.Fatalf("ListDeploymentGitConnections after replace: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 git connection after replacement, got %d (%+v)", len(conns), conns)
	}
	secondCred := conns[0].Credential
	if secondCred == firstCred {
		t.Fatalf("expected replaced connection to allocate a fresh credential name, still %q", secondCred)
	}
	if _, ok := creds.Get(firstCred); ok {
		t.Fatalf("expected old credential %q to be removed after replacement", firstCred)
	}
	if got := creds.AuthorOf(secondCred); got != "Grace Hopper" {
		t.Fatalf("AuthorOf(%q) = %q, want Grace Hopper", secondCred, got)
	}

	// 3. -web runs GitHub OAuth Device Flow against the host's OAuth endpoints.
	oauthSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-1",
				"user_code":        "WXYZ-9999",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       600,
				"interval":         1,
			})
		case "/login/oauth/access_token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "gho_web_device_token_3",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthSrv.Close()

	origHTTP := gitLoginHTTPClient
	origBrowser := gitLoginOpenBrowser
	t.Cleanup(func() {
		gitLoginHTTPClient = origHTTP
		gitLoginOpenBrowser = origBrowser
	})
	gitLoginHTTPClient = oauthSrv.Client()
	var openedURL string
	gitLoginOpenBrowser = func(u string) error {
		openedURL = u
		return nil
	}

	host := strings.TrimPrefix(oauthSrv.URL, "https://")
	if err := cmdGitLogin(ctx, client, out, []string{
		"-web",
		"-host", host,
		"-base-url", oauthSrv.URL,
		"-author", "Alan Turing",
	}); err != nil {
		t.Fatalf("cmdGitLogin -web: %v", err)
	}
	if openedURL != "https://github.com/login/device" {
		t.Fatalf("opened browser URL = %q, want https://github.com/login/device", openedURL)
	}
	conns, err = client.ListDeploymentGitConnections(ctx)
	if err != nil {
		t.Fatalf("ListDeploymentGitConnections after -web: %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("expected 2 connections (github.com + %s), got %d", host, len(conns))
	}
}

// git-login's closing line names the signature the GitHub client will
// actually sign with (the recorded Author), never the commit identity: a
// token whose owner the host reports no human name for (a [bot]) signs
// nothing, and must not be described as signing as this host's git
// user.name (docs/test-runs/2026-11-08-cli-install-skill-mcp.md).
func TestGitLoginSummaryNeverSignsAsTheCommitIdentity(t *testing.T) {
	conns := []ui.TeamGitConnectionView{
		{Host: "github.com", Scope: "acme/widgets", Status: ui.GitCredentialStatus{CommitName: "Daemon Operator"}},
		{Host: "github.com", Scope: "acme/owned", Status: ui.GitCredentialStatus{Author: "Dana Owner", CommitName: "dana"}},
	}
	author, commit := gitLoginSignature(conns, "github.com", "acme/widgets")
	if author != "" || commit != "Daemon Operator" {
		t.Fatalf("gitLoginSignature(acme/widgets) = %q, %q; want no author, commit identity Daemon Operator", author, commit)
	}
	got := gitLoginSummary("github.com", author, commit)
	if strings.Contains(got, "Signed by") || !strings.Contains(got, "not signed") || !strings.Contains(got, "commits authored as Daemon Operator") {
		t.Fatalf("summary for an unsigned connection = %q", got)
	}
	author, _ = gitLoginSignature(conns, "github.com", "acme/owned")
	if got := gitLoginSummary("github.com", author, ""); !strings.Contains(got, `"Signed by Dana Owner (via Grain)"`) {
		t.Fatalf("summary for an owned connection = %q", got)
	}
}
