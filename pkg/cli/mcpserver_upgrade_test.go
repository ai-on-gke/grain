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

// The api:self-upgrade preset end to end: the controller MCP endpoint
// (POST /api/mcp -> grain_explore_scope / grain_invoke for
// api_get_upgrade_status, api_start_upgrade, api_cancel_scheduled_upgrade) over
// the daemon's own REST API, against a real store and a real reconcile
// cycle.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

type fakeDeploymentUpgrader struct {
	started []string
	status  upgrade.Status
}

func (f *fakeDeploymentUpgrader) Start(branch string) error {
	f.started = append(f.started, branch)
	f.status = upgrade.Status{Branch: branch, Phase: upgrade.PhaseRunning}
	return nil
}
func (f *fakeDeploymentUpgrader) Status() (upgrade.Status, error) { return f.status, nil }
func (f *fakeDeploymentUpgrader) ValidateBranch(branch string) (string, error) {
	return upgrade.ValidBranchName(branch)
}
func (f *fakeDeploymentUpgrader) Mode() upgrade.Mode { return upgrade.ModeImage }
func (f *fakeDeploymentUpgrader) Running() (upgrade.Running, error) {
	return upgrade.Running{Ref: "docker.io/grainai/grain:main"}, nil
}

func upgradeToolDeployment(t *testing.T, up ui.Upgrader, grants ...string) (string, *model.Store) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	var taskGrants []model.Grant
	for _, g := range grants {
		taskGrants = append(taskGrants, model.Grant{Capability: g, Via: model.GrantByLabel})
	}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "335", Intent: model.IntentImplement, Title: "manage upgrades", Body: "please",
		Grants:   taskGrants,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing task 335: %v", err)
	}

	cfg := ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		Upgrader:     up,
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
	t.Cleanup(srv.Close)

	return srv.URL, store
}

func callControllerMCP(t *testing.T, serverURL, taskID, toolName string, args map[string]any) mcp.Result {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatalf("marshalling tools/call: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/mcp", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("building /api/mcp request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ui.AccessCodeHeader, testAccessCode)
	if taskID != "" {
		req.Header.Set(ui.ActingTaskHeader, taskID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mcp status = %d: %s", resp.StatusCode, raw)
	}
	var rpc struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatalf("decoding /api/mcp response: %v (%s)", err, raw)
	}
	if rpc.Error != nil {
		t.Fatalf("/api/mcp JSON-RPC error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	var texts []string
	for _, c := range rpc.Result.Content {
		texts = append(texts, c.Text)
	}
	return mcp.Result{
		Text:    strings.Join(texts, "\n"),
		IsError: rpc.Result.IsError,
	}
}

func exploredDeploymentTools(t *testing.T, serverURL, taskID string) []string {
	t.Helper()
	res := callControllerMCP(t, serverURL, taskID, ui.ToolGrainExploreScope, map[string]any{
		"scope": "deployment",
	})
	if res.IsError {
		return nil
	}
	var explored struct {
		Routes []struct {
			Tool string `json:"tool"`
		} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(res.Text), &explored); err != nil {
		t.Fatalf("decoding grain_explore_scope(deployment): %v\n%s", err, res.Text)
	}
	names := make([]string, 0, len(explored.Routes))
	for _, rt := range explored.Routes {
		names = append(names, rt.Tool)
	}
	return names
}

func callUpgradeTool(t *testing.T, serverURL string, name string, args map[string]any) mcp.Result {
	t.Helper()
	invokeArgs := map[string]any{}
	reason := ""
	for k, v := range args {
		if k == "reason" {
			reason, _ = v.(string)
			continue
		}
		invokeArgs[k] = v
	}
	params := map[string]any{
		"tool":      name,
		"arguments": invokeArgs,
	}
	if reason != "" {
		params["reason"] = reason
	}
	return callControllerMCP(t, serverURL, "335", ui.ToolGrainInvoke, params)
}

func TestTheUpgradeToolsNeedTheirOwnGrant(t *testing.T) {
	t.Parallel()
	wanted := []string{"api_get_upgrade_status", "api_start_upgrade", "api_cancel_scheduled_upgrade"}
	client := ui.NewHTTPClient("http://127.0.0.1:1")

	for _, grant := range []string{"api:self-upgrade", model.APIRef("self-upgrade")} {
		// grain-sandbox no longer exposes generated api_* tools directly.
		sandboxTools := toolNames(daemon.GrantedDaemonTools(daemon.GrantNames{grant}, "", "", "", "335", client))
		for _, name := range wanted {
			if slices.Contains(sandboxTools, name) {
				t.Errorf("grain-sandbox unexpectedly exposed %s for %s: %v", name, grant, sandboxTools)
			}
		}
		// grain-controller (/api/mcp) exposes them under the deployment scope.
		serverURL, _ := upgradeToolDeployment(t, &fakeDeploymentUpgrader{}, grant)
		granted := exploredDeploymentTools(t, serverURL, "335")
		for _, name := range wanted {
			if !slices.Contains(granted, name) {
				t.Errorf("a task holding %s got %v on /api/mcp, want %s among them", grant, granted, name)
			}
		}
	}

	withoutURL, _ := upgradeToolDeployment(t, &fakeDeploymentUpgrader{},
		model.GrainSourceGrant, model.GrainJournalGrant, model.GrainHealthGrant, model.BootstrapPlaybooksGrant, model.GrainHostExecGrant)
	without := exploredDeploymentTools(t, withoutURL, "335")
	for _, name := range []string{"api_start_upgrade", "api_cancel_scheduled_upgrade"} {
		if slices.Contains(without, name) {
			t.Errorf("tools without self-upgrade = %v, unexpectedly contains %s", without, name)
		}
	}
	refused := callUpgradeTool(t, withoutURL, "api_start_upgrade", map[string]any{
		"reason": "unauthorized attempt",
		"body":   map[string]any{"branch": "main", "afterTasksFinish": true},
	})
	if !refused.IsError {
		t.Errorf("api_start_upgrade without self-upgrade grant succeeded: %s", refused.Text)
	}
}

func TestSchedulingAnUpgradeThroughTheToolHoldsTheQueueAndWaitsForTheAgentItself(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	serverURL, store := upgradeToolDeployment(t, fake, "api:self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.StartRun(ctx, model.Run{
		ID: "335-1", TaskID: "335", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting the upgrade agent's run: %v", err)
	}

	// Dangerous scope requires a reason.
	noReason := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"body": map[string]any{"branch": "main", "afterTasksFinish": true},
	})
	if !noReason.IsError {
		t.Fatalf("api_start_upgrade without reason succeeded: %s", noReason.Text)
	}

	// Spoofing another task's principal is refused.
	spoofed := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"reason": "upgrade to latest main",
		"body": map[string]any{
			"branch": "main", "afterTasksFinish": true,
			"requestedBy": "999", "requestedByKind": "agent",
		},
	})
	if !spoofed.IsError {
		t.Fatalf("api_start_upgrade with spoofed requestedBy succeeded: %s", spoofed.Text)
	}

	got := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"reason": "upgrade to latest main",
		"body":   map[string]any{"branch": "main", "afterTasksFinish": true},
	})
	if got.IsError {
		t.Fatalf("api_start_upgrade = %+v, want it accepted", got)
	}
	if len(fake.started) != 0 {
		t.Fatalf("scheduling started %v; afterTasksFinish starts nothing", fake.started)
	}
	scheduled, err := store.GetScheduledUpgrade(ctx)
	if err != nil || scheduled == nil {
		t.Fatalf("the schedule did not reach the store: (%+v, %v)", scheduled, err)
	}
	if scheduled.Branch != "main" {
		t.Errorf("scheduled branch = %q", scheduled.Branch)
	}
	if want := (model.Principal{Kind: model.PrincipalAgent, ID: "335", Source: model.SourceSandbox}); scheduled.By != want {
		t.Errorf("scheduled by = %+v, want %+v", scheduled.By, want)
	}

	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{StartUpgrade: fake.Start}}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Second)); err != nil {
		t.Fatalf("cycle while the agent is still running: %v", err)
	}
	pause, err := store.GetPipelinePause(ctx)
	if err != nil || pause == nil {
		t.Fatalf("the queue was not held for the scheduled upgrade: (%+v, %v)", pause, err)
	}
	if len(fake.started) != 0 {
		t.Fatalf("upgraded %v while the agent that scheduled it was still running", fake.started)
	}

	if err := store.FinishRun(ctx, "335-1", now.Add(2*time.Second), "succeeded", ""); err != nil {
		t.Fatalf("finishing the agent's run: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(3*time.Second)); err != nil {
		t.Fatalf("cycle once the agent finished: %v", err)
	}
	if !slices.Equal(fake.started, []string{"main"}) {
		t.Fatalf("started = %v, want [main] once the agent had finished", fake.started)
	}
}

func TestTheStatusToolReportsTheLiveCIRunsAnUpgradeIsWaitingFor(t *testing.T) {
	t.Parallel()
	serverURL, store := upgradeToolDeployment(t, &fakeDeploymentUpgrader{}, "api:self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"reason": "schedule upgrade",
		"body":   map[string]any{"branch": "main", "afterTasksFinish": true},
	}); got.IsError {
		t.Fatalf("api_start_upgrade = %+v", got)
	}
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci-1", Key: "k", Repo: "ai-on-gke/grain", Ref: "main", Commit: "deadbeef",
		Event: "push", StartedAt: now,
	}); err != nil {
		t.Fatalf("recording a live CI run: %v", err)
	}

	got := callUpgradeTool(t, serverURL, "api_get_upgrade_status", nil)
	if got.IsError {
		t.Fatalf("api_get_upgrade_status = %+v", got)
	}
	var status struct {
		Scheduled *struct {
			BusyCIRuns int `json:"busyCIRuns"`
		} `json:"scheduled"`
	}
	if err := json.Unmarshal([]byte(got.Text), &status); err != nil {
		t.Fatalf("decoding api_get_upgrade_status JSON: %v\n%s", err, got.Text)
	}
	if status.Scheduled == nil || status.Scheduled.BusyCIRuns != 1 {
		t.Errorf("scheduled = %+v, want busyCIRuns == 1:\n%s", status.Scheduled, got.Text)
	}
}

func TestCancellingThroughTheToolOpensTheQueueAgain(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	serverURL, store := upgradeToolDeployment(t, fake, "api:self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"reason": "schedule upgrade",
		"body":   map[string]any{"branch": "main", "afterTasksFinish": true},
	}); got.IsError {
		t.Fatalf("api_start_upgrade = %+v", got)
	}
	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{StartUpgrade: fake.Start}}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(ctx, model.Task{
		ID: "336", Intent: model.IntentImplement, Title: "work", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "336-1", TaskID: "336", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Second)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause == nil {
		t.Fatalf("the queue was not held for the scheduled upgrade: (%+v, %v)", pause, err)
	}

	got := callUpgradeTool(t, serverURL, "api_cancel_scheduled_upgrade", map[string]any{
		"reason": "calling off the upgrade",
	})
	if got.IsError {
		t.Fatalf("api_cancel_scheduled_upgrade = %+v", got)
	}
	if !strings.Contains(got.Text, `"cancelled": true`) && !strings.Contains(got.Text, `"cancelled":true`) {
		t.Errorf("the answer does not report cancelled=true:\n%s", got.Text)
	}
	if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled != nil {
		t.Fatalf("the row outlived the cancellation: (%+v, %v)", scheduled, err)
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(2*time.Second)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause != nil {
		t.Fatalf("the queue is still held after the upgrade was called off: (%+v, %v)", pause, err)
	}
	if len(fake.started) != 0 {
		t.Errorf("upgraded %v after the schedule was cancelled", fake.started)
	}
}

func TestAnImmediateUpgradeThroughTheToolStartsTheUpgraderAtOnce(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	serverURL, store := upgradeToolDeployment(t, fake, "api:self-upgrade")

	got := callUpgradeTool(t, serverURL, "api_start_upgrade", map[string]any{
		"reason": "emergency rollback",
		"body":   map[string]any{"branch": "main", "afterTasksFinish": false},
	})
	if got.IsError {
		t.Fatalf("api_start_upgrade = %+v", got)
	}
	if len(fake.started) != 1 || fake.started[0] != "main" {
		t.Fatalf("started %v, want the upgrade to have begun", fake.started)
	}
	if scheduled, err := store.GetScheduledUpgrade(context.Background()); err != nil || scheduled != nil {
		t.Fatalf("an immediate upgrade wrote a schedule as well: (%+v, %v)", scheduled, err)
	}
}
