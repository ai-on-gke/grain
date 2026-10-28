package cli

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
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
	uiClient := ui.NewClient(ui.Config{SourceDB: db, Actor: ui.DefaultActor("alice")}, store)
	server := accessCodeServer(uiClient)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	httpClient := accessCodeClient(ts.URL)

	return httpClient, &modelStoreDB{store: store, db: db}
}

type modelStoreDB struct {
	store any
	db    any
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
