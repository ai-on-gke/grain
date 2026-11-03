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

// The api:self-upgrade preset end to end: the generated agent API tools
// (api_get_upgrade_status, api_start_upgrade, api_cancel_scheduled_upgrade) over
// the daemon's own REST API, against a real store and a real reconcile
// cycle.

import (
	"context"
	"encoding/json"
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
	return upgrade.Running{Ref: "ghcr.io/bwsalmon/grain/grain:main"}, nil
}

func upgradeToolDeployment(t *testing.T, up ui.Upgrader, grant string) (map[string]mcp.Tool, *model.Store) {
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
	if err := store.PutTask(context.Background(), model.Task{
		ID: "335", Intent: model.IntentImplement, Title: "manage upgrades", Body: "please",
		Grants:   []model.Grant{{Capability: grant, Via: model.GrantByLabel}},
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

	client := accessCodeClient(srv.URL)
	byName := map[string]mcp.Tool{}
	for _, tool := range daemon.GrantedDaemonTools(daemon.GrantNames{grant}, "", "", "", "335", client) {
		byName[tool.Name] = tool
	}
	return byName, store
}

func callUpgradeTool(t *testing.T, roster map[string]mcp.Tool, name string, args map[string]any) mcp.Result {
	t.Helper()
	tool, ok := roster[name]
	if !ok {
		t.Fatalf("no %s on the roster of a task holding self-upgrade", name)
	}
	return tool.Handler(context.Background(), args)
}

func TestTheUpgradeToolsNeedTheirOwnGrant(t *testing.T) {
	t.Parallel()
	wanted := []string{"api_get_upgrade_status", "api_start_upgrade", "api_cancel_scheduled_upgrade"}
	client := ui.NewHTTPClient("http://127.0.0.1:1")

	for _, grant := range []string{"api:self-upgrade", "self-upgrade"} {
		granted := toolNames(daemon.GrantedDaemonTools(daemon.GrantNames{grant}, "", "", "", "335", client))
		for _, name := range wanted {
			if !slices.Contains(granted, name) {
				t.Errorf("a task holding %s got %v, want %s among them", grant, granted, name)
			}
		}
	}

	without := toolNames(daemon.GrantedDaemonTools(
		daemon.GrantNames{"self-debug", "grain-log", "deployment-health", "bootstrap-playbooks", "self-repair"},
		"/src", secretsDirWithAKey(t), "/state", "335", client))
	for _, name := range []string{"api_start_upgrade", "api_cancel_scheduled_upgrade"} {
		if slices.Contains(without, name) {
			t.Errorf("tools without self-upgrade = %v, unexpectedly contains %s", without, name)
		}
	}
}

func TestSchedulingAnUpgradeThroughTheToolHoldsTheQueueAndWaitsForTheAgentItself(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	roster, store := upgradeToolDeployment(t, fake, "api:self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.StartRun(ctx, model.Run{
		ID: "335-1", TaskID: "335", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting the upgrade agent's run: %v", err)
	}

	// Dangerous scope requires a reason.
	noReason := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
		"body": map[string]any{"branch": "main", "afterTasksFinish": true},
	})
	if !noReason.IsError {
		t.Fatalf("api_start_upgrade without reason succeeded: %s", noReason.Text)
	}

	// Spoofing another task's principal is refused.
	spoofed := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
		"reason": "upgrade to latest main",
		"body": map[string]any{
			"branch": "main", "afterTasksFinish": true,
			"requestedBy": "999", "requestedByKind": "agent",
		},
	})
	if !spoofed.IsError {
		t.Fatalf("api_start_upgrade with spoofed requestedBy succeeded: %s", spoofed.Text)
	}

	got := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
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
	roster, store := upgradeToolDeployment(t, &fakeDeploymentUpgrader{}, "self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
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

	got := callUpgradeTool(t, roster, "api_get_upgrade_status", nil)
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
	roster, store := upgradeToolDeployment(t, fake, "api:self-upgrade")
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
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

	got := callUpgradeTool(t, roster, "api_cancel_scheduled_upgrade", map[string]any{
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
	roster, store := upgradeToolDeployment(t, fake, "api:self-upgrade")

	got := callUpgradeTool(t, roster, "api_start_upgrade", map[string]any{
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
