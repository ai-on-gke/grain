package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestFirstTimeUserCLIDaemonOfflineGuidance(t *testing.T) {
	t.Parallel()
	bin := buildGrainCLI(t)

	// Attempting to run grain list when the daemon is offline should produce
	// an informative connection error with the targeted server URL.
	cmd := exec.Command(bin, "-server", "http://127.0.0.1:59999", "list")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected grain list to fail when daemon is offline, got output:\n%s", out)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "http://127.0.0.1:59999") {
		t.Errorf("error output should mention targeted server URL, got: %s", outStr)
	}
	if !strings.Contains(outStr, "connection refused") && !strings.Contains(outStr, "dial tcp") {
		t.Errorf("error output should indicate dial/connection failure, got: %s", outStr)
	}
}

func TestFirstTimeUserCLIAndStoreWalkthrough(t *testing.T) {
	t.Parallel()
	bin := buildGrainCLI(t)
	storeDir := t.TempDir()

	// 1. Fresh store with zero tasks: grain list should succeed with empty results
	listEmpty := runCLIStore(t, bin, storeDir, "-json", "list")
	var tasks []ui.Task
	if err := json.Unmarshal([]byte(listEmpty), &tasks); err != nil {
		t.Fatalf("unmarshaling empty task list: %v (raw: %s)", err, listEmpty)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks on fresh store, got %d", len(tasks))
	}

	// 2. Create the first task via CLI
	const owner, repoName = "acme", "widgets"
	created := runCLIStore(t, bin, storeDir,
		"-json", "create",
		"-title", "First onboarding task",
		"-body", "Explore Grain features and workflow",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("unmarshaling created task: %v (raw: %s)", err, created)
	}
	if task.Title != "First onboarding task" {
		t.Errorf("task title = %q, want 'First onboarding task'", task.Title)
	}
	if task.Repo != owner+"/"+repoName {
		t.Errorf("task repo = %q, want %q", task.Repo, owner+"/"+repoName)
	}
	if task.State != model.StateQueued {
		t.Errorf("task state = %q, want %q", task.State, model.StateQueued)
	}

	// 3. Get the task by ID
	fetched := runCLIStore(t, bin, storeDir, "-json", "get", task.ID)
	var fetchedTask ui.Task
	if err := json.Unmarshal([]byte(fetched), &fetchedTask); err != nil {
		t.Fatalf("unmarshaling fetched task: %v (raw: %s)", err, fetched)
	}
	if fetchedTask.ID != task.ID {
		t.Errorf("fetched task ID = %s, want %s", fetchedTask.ID, task.ID)
	}

	// 4. List tasks again; should contain exactly 1 task
	listPopulated := runCLIStore(t, bin, storeDir, "-json", "list")
	var listAfter []ui.Task
	if err := json.Unmarshal([]byte(listPopulated), &listAfter); err != nil {
		t.Fatalf("unmarshaling populated task list: %v", err)
	}
	if len(listAfter) != 1 || listAfter[0].ID != task.ID {
		t.Fatalf("expected 1 task in list matching %s, got %+v", task.ID, listAfter)
	}
}

func TestFirstTimeDeploymentConfigReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite db: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	// An empty configuration has no backends and no git credentials configured
	cfg := ui.Config{Actor: ui.DefaultActor("operator")}
	srv := ui.NewServer(withAccessCode(cfg), store)
	server := httptest.NewServer(srv)
	defer server.Close()

	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/config", nil)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	resp, err := http.DefaultClient.Do(presentAccessCode(req))
	if err != nil {
		t.Fatalf("GET /api/config: %v", err)
	}
	defer resp.Body.Close()

	var configResp struct {
		ModelsConfigured         bool `json:"modelsConfigured"`
		GitCredentialsConfigured bool `json:"gitCredentialsConfigured"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err != nil {
		t.Fatalf("decoding /api/config response: %v", err)
	}

	if configResp.ModelsConfigured {
		t.Error("expected ModelsConfigured to be false for fresh unconfigured deployment")
	}
	if configResp.GitCredentialsConfigured {
		t.Error("expected GitCredentialsConfigured to be false for fresh unconfigured deployment")
	}

	// Configure a model backend and verify ModelsConfigured updates
	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: []model.AgentBackend{
			{ID: "default", Framework: "antigravity", Models: []string{"gemini-2.5-pro"}},
		},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	req2, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/config", nil)
	resp2, err := http.DefaultClient.Do(presentAccessCode(req2))
	if err != nil {
		t.Fatalf("GET /api/config after configuring model: %v", err)
	}
	defer resp2.Body.Close()

	var configResp2 struct {
		ModelsConfigured bool `json:"modelsConfigured"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&configResp2); err != nil {
		t.Fatalf("decoding /api/config response: %v", err)
	}
	if !configResp2.ModelsConfigured {
		t.Error("expected ModelsConfigured to be true after adding agent backend")
	}
}
