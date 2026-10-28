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

// The self-upgrade grant end to end: the three tools a granted run gets,
// over the daemon's own REST API, against a real store and a real
// reconcile cycle. What is under test here is the whole road -- the grant
// deciding the roster, the tool, the HTTP hop, the route, the row it
// writes, and what orchestrator then does about that row -- since every
// piece of it already has unit tests of its own and none of them can say
// that an agent scheduling an upgrade actually gets one.

import (
	"context"
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

// fakeDeploymentUpgrader is a ui.Upgrader that records rather than
// upgrades: a real one restarts the process running the test.
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

func init() {
	daemon.ConfirmPollInterval = 10 * time.Millisecond
}

// upgradeToolDeployment is a daemon a run can actually reach: a store, a
// UI server over it, and the roster a task holding self-upgrade gets from
// `grain mcpserver`. up nil is a deployment started with neither
// -upgrade-image nor -upgrade-src-dir, which is what the tools have to
// refuse in words.
func upgradeToolDeployment(t *testing.T, up ui.Upgrader) (map[string]mcp.Tool, *model.Store) {
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
		Interactive: true,
		Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: human},
		Binding:     model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing task 335: %v", err)
	}

	// Auto-reply "approve" to confirmation questions for task 335 in background
	stopAutoApprove := make(chan struct{})
	t.Cleanup(func() { close(stopAutoApprove) })
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopAutoApprove:
				return
			case <-ticker.C:
				comments, err := store.Comments(context.Background(), "335")
				if err == nil && len(comments) > 0 {
					var lastComment model.Comment
					var hasQuestion bool
					for _, c := range comments {
						if c.Author.Actor.Kind != model.PrincipalHuman {
							hasQuestion = true
						}
						lastComment = c
					}
					if hasQuestion && lastComment.Author.Actor.Kind != model.PrincipalHuman {
						_, _ = store.AddComment(context.Background(), model.Comment{
							TaskID:    "335",
							Author:    model.Attribution{Actor: human},
							Body:      "approve",
							CreatedAt: time.Now(),
						})
					}
				}
			}
		}
	}()

	cfg := ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		Upgrader:     up,
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
	t.Cleanup(srv.Close)

	client := accessCodeClient(srv.URL)
	byName := map[string]mcp.Tool{}
	for _, tool := range daemon.GrantedDaemonTools(daemon.GrantNames{"self-upgrade"}, "", "", "", "335", client) {
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

// The access question this capability exists to answer: a task that holds
// it gets the tools whether or not anybody is watching its chat, and a
// task that does not holds nothing of them.
func TestTheUpgradeToolsNeedTheirOwnGrant(t *testing.T) {
	t.Parallel()
	wanted := []string{"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade"}

	granted := toolNames(daemon.GrantedDaemonTools(daemon.GrantNames{"self-upgrade"}, "", "", "", "335", nil))
	for _, name := range wanted {
		if !slices.Contains(granted, name) {
			t.Errorf("a task holding self-upgrade got %v, want %s among them", granted, name)
		}
	}

	// Every other tool grant put together, including the one that can
	// already run anything on this host: none of them is this one.
	without := toolNames(daemon.GrantedDaemonTools(
		daemon.GrantNames{"self-debug", "grain-log", "deployment-health", "bootstrap-playbooks", "self-repair"},
		"/src", secretsDirWithAKey(t), "/state", "335", nil))
	for _, name := range wanted {
		if slices.Contains(without, name) {
			t.Errorf("tools without self-upgrade = %v, unexpectedly contains %s", without, name)
		}
	}

	// And a run that tries anyway is told what it would need, rather than
	// that grain has no such tool (mcp's unknownToolMessage).
	for _, name := range wanted {
		if capability, ok := mcp.CapabilityForTool(name); !ok || capability != "self-upgrade" {
			t.Errorf("an ungranted call of %s names capability %q (known: %t)", name, capability, ok)
		}
	}
}

// Scheduling through the tool writes the row, starts nothing, and is what
// the reconciler then acts on: the queue shuts, and it keeps waiting
// while the agent that asked for it is still running.
func TestSchedulingAnUpgradeThroughTheToolHoldsTheQueueAndWaitsForTheAgentItself(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	roster, store := upgradeToolDeployment(t, fake)
	ctx := context.Background()
	now := time.Now().UTC()

	// The agent's own run, live, exactly as it is while it makes the call.
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(ctx, model.Task{
		ID: "335", Intent: model.IntentImplement, Title: "manage upgrades", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing the upgrade agent's task: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "335-1", TaskID: "335", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting the upgrade agent's run: %v", err)
	}

	got := callUpgradeTool(t, roster, "upgrade_deployment", map[string]any{"branch": "main"})
	if got.IsError {
		t.Fatalf("upgrade_deployment = %+v, want it accepted", got)
	}
	if len(fake.started) != 0 {
		t.Fatalf("scheduling started %v; the default mode starts nothing", fake.started)
	}
	scheduled, err := store.GetScheduledUpgrade(ctx)
	if err != nil || scheduled == nil {
		t.Fatalf("the schedule did not reach the store: (%+v, %v)", scheduled, err)
	}
	if scheduled.Branch != "main" {
		t.Errorf("scheduled branch = %q", scheduled.Branch)
	}
	// grain/task-353: the row names the task that asked for the restart,
	// not this deployment's operator principal, who did nothing. It is
	// what the pane reads to answer "why is this about to restart".
	if want := (model.Principal{Kind: model.PrincipalAgent, ID: "335"}); scheduled.By != want {
		t.Errorf("scheduled by %+v, want %+v -- the task that asked for it", scheduled.By, want)
	}

	// One cycle: the queue is held for it, and it does not fire, because
	// the run that asked for it is one of the runs it waits for. That is
	// the intended behaviour -- the agent decides, finishes, and the
	// cut-over follows -- and the tool's own description says so, because
	// an agent that polled for the upgrade to land would wait forever.
	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{StartUpgrade: fake.Start}}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Second)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("reading the pause: %v", err)
	}
	if pause == nil || pause.By != model.ScheduledUpgradePrincipal {
		t.Fatalf("pause = %+v, want the queue held for the scheduled upgrade", pause)
	}
	if len(fake.started) != 0 {
		t.Fatalf("upgraded %v while the agent that scheduled it was still running", fake.started)
	}

	// The status the agent would read back says what it is waiting for --
	// its own run among them.
	reading := callUpgradeTool(t, roster, "deployment_upgrade_status", nil)
	if reading.IsError {
		t.Fatalf("deployment_upgrade_status = %+v", reading)
	}
	if !strings.Contains(reading.Text, "1 run") {
		t.Errorf("the reading does not say which runs it waits for:\n%s", reading.Text)
	}
	if !strings.Contains(reading.Text, "asked for by task 335") {
		t.Errorf("the reading does not say which task asked for it:\n%s", reading.Text)
	}

	// The run ends, as the agent's run does when it stops working: the
	// next cycle cuts over.
	if err := store.FinishRun(ctx, "335-1", now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the agent's run: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(fake.started) != 1 || fake.started[0] != "main" {
		t.Fatalf("started %v, want the upgrade to main once the agent had finished", fake.started)
	}
}

// A release build in flight is exactly what an upgrade must not destroy,
// and it is what an agent needs to see to know it is waiting on one
// rather than on other agents.
func TestTheStatusToolReportsTheLiveCIRunsAnUpgradeIsWaitingFor(t *testing.T) {
	t.Parallel()
	roster, store := upgradeToolDeployment(t, &fakeDeploymentUpgrader{})
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, roster, "upgrade_deployment", map[string]any{"branch": "main"}); got.IsError {
		t.Fatalf("upgrade_deployment = %+v", got)
	}
	// A run of grain's own CI with no conclusion yet -- the release job
	// whose 90-minute image build an immediate upgrade would kill.
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci-1", Key: "k", Repo: "bwsalmon/grain", Ref: "main", Commit: "deadbeef",
		Event: "push", StartedAt: now,
	}); err != nil {
		t.Fatalf("recording a live CI run: %v", err)
	}

	got := callUpgradeTool(t, roster, "deployment_upgrade_status", nil)
	if got.IsError {
		t.Fatalf("deployment_upgrade_status = %+v", got)
	}
	if !strings.Contains(got.Text, "1 CI run") {
		t.Errorf("the reading does not report the live CI run:\n%s", got.Text)
	}
}

// Cancelling clears the row, and the queue that was being held opens on
// the next cycle.
func TestCancellingThroughTheToolOpensTheQueueAgain(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	roster, store := upgradeToolDeployment(t, fake)
	ctx := context.Background()
	now := time.Now().UTC()

	if got := callUpgradeTool(t, roster, "upgrade_deployment", map[string]any{"branch": "main"}); got.IsError {
		t.Fatalf("upgrade_deployment = %+v", got)
	}
	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{StartUpgrade: fake.Start}}
	// A live run, so the cycle holds the queue instead of cutting over.
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

	got := callUpgradeTool(t, roster, "cancel_deployment_upgrade", nil)
	if got.IsError {
		t.Fatalf("cancel_deployment_upgrade = %+v", got)
	}
	if !strings.Contains(got.Text, "called off") {
		t.Errorf("the answer does not report the cancellation:\n%s", got.Text)
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

// An immediate upgrade is the mode that restarts now. It really does
// reach the upgrader -- this is the only test that lets it -- and the
// answer warns about what it costs, since on a real deployment the
// process that would have answered is the one being restarted.
func TestAnImmediateUpgradeThroughTheToolStartsTheUpgraderAtOnce(t *testing.T) {
	t.Parallel()
	fake := &fakeDeploymentUpgrader{}
	roster, store := upgradeToolDeployment(t, fake)

	got := callUpgradeTool(t, roster, "upgrade_deployment",
		map[string]any{"branch": "main", "after_tasks_finish": false})
	if got.IsError {
		t.Fatalf("upgrade_deployment = %+v", got)
	}
	if len(fake.started) != 1 || fake.started[0] != "main" {
		t.Fatalf("started %v, want the upgrade to have begun", fake.started)
	}
	if scheduled, err := store.GetScheduledUpgrade(context.Background()); err != nil || scheduled != nil {
		t.Fatalf("an immediate upgrade wrote a schedule as well: (%+v, %v)", scheduled, err)
	}
	if !strings.Contains(got.Text, "discarded") {
		t.Errorf("the answer does not warn what an immediate upgrade costs:\n%s", got.Text)
	}
}

// A deployment with no upgrader configured refuses in words rather than
// erroring, through the real 404 the routes answer with: an agent told
// "error" retries, and there is nothing here to retry.
func TestTheToolsRefuseInWordsWithNoUpgraderConfigured(t *testing.T) {
	t.Parallel()
	roster, _ := upgradeToolDeployment(t, nil)

	for _, name := range []string{"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade"} {
		got := callUpgradeTool(t, roster, name, map[string]any{"branch": "main"})
		if got.IsError {
			t.Errorf("%s = error on a deployment that cannot upgrade: %s", name, got.Text)
		}
		if !strings.Contains(got.Text, "cannot upgrade itself") ||
			!strings.Contains(got.Text, "-upgrade-image") {
			t.Errorf("%s does not explain why it did nothing:\n%s", name, got.Text)
		}
	}
}

func TestUpgradeDeploymentDeniedByHumanInChat(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "400", Intent: model.IntentImplement, Title: "manage upgrades", Body: "please",
		Interactive: true,
		Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: human},
		Binding:     model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing task 400: %v", err)
	}

	fake := &fakeDeploymentUpgrader{}
	cfg := ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		Upgrader:     fake,
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
	t.Cleanup(srv.Close)

	// Reply "deny" in background
	stopAutoReply := make(chan struct{})
	t.Cleanup(func() { close(stopAutoReply) })
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopAutoReply:
				return
			case <-ticker.C:
				comments, err := store.Comments(context.Background(), "400")
				if err == nil && len(comments) > 0 {
					var lastComment model.Comment
					var hasQuestion bool
					for _, c := range comments {
						if c.Author.Actor.Kind != model.PrincipalHuman {
							hasQuestion = true
						}
						lastComment = c
					}
					if hasQuestion && lastComment.Author.Actor.Kind != model.PrincipalHuman {
						_, _ = store.AddComment(context.Background(), model.Comment{
							TaskID:    "400",
							Author:    model.Attribution{Actor: human},
							Body:      "deny: not right now",
							CreatedAt: time.Now(),
						})
					}
				}
			}
		}
	}()

	client := accessCodeClient(srv.URL)
	byName := map[string]mcp.Tool{}
	for _, tool := range daemon.GrantedDaemonTools(daemon.GrantNames{"self-upgrade"}, "", "", "", "400", client) {
		byName[tool.Name] = tool
	}

	got := callUpgradeTool(t, byName, "upgrade_deployment", map[string]any{"branch": "main"})
	if !got.IsError {
		t.Fatalf("upgrade_deployment = %+v, want denial error", got)
	}
	if !strings.Contains(got.Text, "denied") || !strings.Contains(got.Text, "not right now") {
		t.Errorf("error does not explain human denial: %s", got.Text)
	}
	if len(fake.started) != 0 {
		t.Fatalf("upgrader was started on denied request: %v", fake.started)
	}
}

func TestUpgradeDeploymentRefusedWithoutConfirmationChannel(t *testing.T) {
	t.Parallel()
	// No client provided -> tools created without confirm function
	tools := daemon.GrantedDaemonTools(daemon.GrantNames{"self-upgrade"}, "", "", "", "401", nil)
	byName := map[string]mcp.Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	got := callUpgradeTool(t, byName, "upgrade_deployment", map[string]any{"branch": "main"})
	if !got.IsError {
		t.Fatalf("upgrade_deployment with no client = %+v, want error", got)
	}
	if !strings.Contains(got.Text, "requires interactive human confirmation") && !strings.Contains(got.Text, "cannot reach the grain daemon") {
		t.Errorf("error does not explain missing route or confirmation: %s", got.Text)
	}
}
