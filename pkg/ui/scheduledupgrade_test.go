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

package ui_test

// Scheduling an upgrade for later rather than starting one now
// (grain/task-156): POST /api/upgrade with afterTasksFinish writes the
// branch down and starts nothing, GET reports what is pending and what
// it is waiting for, and DELETE /api/upgrade/scheduled -- or resuming
// the pipeline -- calls it off.
//
// What actually applies it is orchestrator's own reconciler, which these
// do not reach: the API's whole job here is the row and the reading of
// it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// scheduledUpgradeResponse is GET /api/upgrade's "scheduled" object --
// the same fields UpgradePanel.jsx reads, spelled the same way.
type scheduledUpgradeResponse struct {
	Branch      string    `json:"branch"`
	RequestedAt time.Time `json:"requestedAt"`
	ByKind      string    `json:"byKind"`
	ByID        string    `json:"byId"`
	BusyRuns    int       `json:"busyRuns"`
	BusyCIRuns  int       `json:"busyCiRuns"`
	CountError  string    `json:"countError"`
}

type upgradeWithScheduleResponse struct {
	Enabled   bool                      `json:"enabled"`
	Phase     string                    `json:"phase"`
	Branch    string                    `json:"branch"`
	Scheduled *scheduledUpgradeResponse `json:"scheduled"`
	Cancelled bool                      `json:"cancelled"`
}

// serverAndStoreWithUpgrader is testServerWithUpgrader with the store
// handed back too: everything about a scheduled upgrade is a row, so
// these tests have to be able to look at one.
func serverAndStoreWithUpgrader(t *testing.T, up ui.Upgrader) (*ui.Server, *model.Store) {
	t.Helper()
	_, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	_ = store.PutConfig(ctx, cfg)
	uicfg := ui.Config{Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities(), Upgrader: up}
	return ui.NewServer(uicfg, store), store
}

// scheduleUpgrade posts what the pane's own "when this deployment is
// idle" checkbox posts: a branch, the flag, and nobody named -- so the
// schedule is recorded as this deployment's actor.
func scheduleUpgrade(t *testing.T, srv *ui.Server, branch string) (int, upgradeWithScheduleResponse) {
	t.Helper()
	return postUpgrade(t, srv, map[string]any{"branch": branch, "afterTasksFinish": true})
}

// postUpgrade is the same POST with the body spelled out, for the tests
// that care about the fields the pane never sends.
func postUpgrade(t *testing.T, srv *ui.Server, body map[string]any) (int, upgradeWithScheduleResponse) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodPost, "/api/upgrade", string(encoded))
	if rec.Code >= 300 {
		return rec.Code, upgradeWithScheduleResponse{}
	}
	return rec.Code, decode[upgradeWithScheduleResponse](t, rec)
}

func TestSchedulingAnUpgradeStartsNothingAndIsReportedBack(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{}
	srv, store := serverAndStoreWithUpgrader(t, fake)

	code, got := scheduleUpgrade(t, srv, "grain/task-156")
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if fake.started != "" {
		t.Fatalf("Start was called with %q; scheduling starts nothing", fake.started)
	}
	if got.Phase == "running" {
		t.Error("the pane was told an upgrade is running; nothing was started")
	}
	if got.Scheduled == nil {
		t.Fatal("the reply says nothing about the upgrade it just scheduled")
	}
	if got.Scheduled.Branch != "grain/task-156" {
		t.Errorf("scheduled branch = %q", got.Scheduled.Branch)
	}
	if got.Scheduled.ByID != "alice" || got.Scheduled.ByKind != string(model.PrincipalHuman) {
		t.Errorf("scheduled by (%q, %q), want the API's own actor",
			got.Scheduled.ByKind, got.Scheduled.ByID)
	}

	scheduled, err := store.GetScheduledUpgrade(t.Context())
	if err != nil || scheduled == nil {
		t.Fatalf("the schedule did not reach the store: (%+v, %v)", scheduled, err)
	}

	// And the next poll says the same thing, which is what the pane
	// actually renders from.
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	if polled := decode[upgradeWithScheduleResponse](t, rec); polled.Scheduled == nil ||
		polled.Scheduled.Branch != "grain/task-156" {
		t.Fatalf("GET reports %+v", polled.Scheduled)
	}
}

// TestAScheduledUpgradeSaysWhenItsRunsCannotBeCounted: when the store
// cannot count the busy runs, the reconciler holds the queue and never
// starts the upgrade -- and GET /api/upgrade used to report that as
// "busyRuns": 0, so the pane and `grain upgrade status` read "waiting on
// 0 active run(s)" over a schedule going nowhere
// (docs/test-runs/2026-11-18-upgrade-scheduled-cancel.md F8). The
// response now says the counts could not be read.
func TestAScheduledUpgradeSaysWhenItsRunsCannotBeCounted(t *testing.T) {
	t.Parallel()
	srv, store := serverAndStoreWithUpgrader(t, &fakeUpgrader{})
	if code, _ := scheduleUpgrade(t, srv, "main"); code != http.StatusAccepted {
		t.Fatalf("schedule = %d, want 202", code)
	}
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if got := decode[upgradeWithScheduleResponse](t, rec); got.Scheduled == nil || got.Scheduled.CountError != "" {
		t.Fatalf("before breaking the count, scheduled = %+v, want no count error", got.Scheduled)
	}
	if _, err := store.DB().ExecContext(t.Context(), "DROP TABLE `task_run`"); err != nil {
		t.Fatalf("breaking the busy run count: %v", err)
	}
	rec = do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeWithScheduleResponse](t, rec)
	if got.Scheduled == nil {
		t.Fatal("the schedule vanished from the status when its runs could not be counted")
	}
	if !strings.Contains(got.Scheduled.CountError, "runs in flight could not be counted") ||
		!strings.Contains(got.Scheduled.CountError, "queue stays held") {
		t.Errorf("countError = %q, want it to say the runs could not be counted and the queue is held",
			got.Scheduled.CountError)
	}
}

// A branch the upgrader would refuse has to be refused now, while the
// person who typed it is still looking at the form -- not hours later,
// in a log line nobody is reading, once the deployment finally goes
// idle.
func TestSchedulingRefusesABranchAnUpgradeWouldRefuse(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{}
	srv, store := serverAndStoreWithUpgrader(t, fake)

	code, _ := scheduleUpgrade(t, srv, "--upload-pack=evil")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if scheduled, err := store.GetScheduledUpgrade(t.Context()); err != nil || scheduled != nil {
		t.Fatalf("a branch the upgrader would refuse was written down anyway: (%+v, %v)", scheduled, err)
	}
}

// bwsalmon/grain#2385: a well-formed branch this deployment was not
// configured to upgrade to is refused at the form too, by the real
// upgrader's own allowlist, whether the upgrade is started now or
// scheduled for later.
func TestUpgradeRefusesABranchOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	up := upgrade.New(upgrade.Config{
		Image:      &upgrade.ImageConfig{Repository: "docker.io/grainai/grain", RefFile: filepath.Join(dir, "image.env")},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})
	srv, store := serverAndStoreWithUpgrader(t, up)

	for _, after := range []bool{true, false} {
		code, _ := postUpgrade(t, srv, map[string]any{"branch": "totally-unreviewed-agent-branch", "afterTasksFinish": after})
		if code != http.StatusBadRequest {
			t.Fatalf("afterTasksFinish=%v: status = %d, want 400", after, code)
		}
	}
	if scheduled, err := store.GetScheduledUpgrade(t.Context()); err != nil || scheduled != nil {
		t.Fatalf("a branch outside the allowlist was written down anyway: (%+v, %v)", scheduled, err)
	}
	if status, err := up.Status(); err != nil || status.Phase != "" {
		t.Fatalf("a branch outside the allowlist was started: (%+v, %v)", status, err)
	}
}

// grain/task-353: an upgrade a task scheduled is recorded as that task,
// not as the operator who was not there. The self-upgrade capability's
// upgrade_deployment is the caller that names one (cmd/grain's
// daemonUpgrade), and before this the pane said "operator" about a
// restart no operator had asked for.
func TestSchedulingRecordsTheTaskThatAskedForIt(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{}
	srv, store := serverAndStoreWithUpgrader(t, fake)

	code, got := postUpgrade(t, srv, map[string]any{
		"branch":           "main",
		"afterTasksFinish": true,
		"requestedBy":      "353",
		"requestedByKind":  string(model.PrincipalAgent),
	})
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if got.Scheduled == nil {
		t.Fatal("the reply says nothing about the upgrade it just scheduled")
	}
	if got.Scheduled.ByID != "353" || got.Scheduled.ByKind != string(model.PrincipalAgent) {
		t.Errorf("scheduled by (%q, %q), want the task that asked",
			got.Scheduled.ByKind, got.Scheduled.ByID)
	}

	scheduled, err := store.GetScheduledUpgrade(t.Context())
	if err != nil || scheduled == nil {
		t.Fatalf("the schedule did not reach the store: (%+v, %v)", scheduled, err)
	}
	if want := (model.Principal{Kind: model.PrincipalAgent, ID: "353", Source: model.SourceSandbox}); scheduled.By != want {
		t.Errorf("stored by = %+v, want %+v", scheduled.By, want)
	}
}

// Half a principal, or a kind grain does not know, is refused rather
// than quietly recorded or quietly dropped: a caller that went to the
// trouble of naming a requester has an opinion worth failing over, and
// an unreadable kind would land in a column every reader switches on.
func TestSchedulingRefusesARequesterItCannotRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"an id with no kind", map[string]any{"requestedBy": "353"}},
		{"a kind with no id", map[string]any{"requestedByKind": "agent"}},
		{"a kind grain does not know", map[string]any{
			"requestedBy": "353", "requestedByKind": "robot"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeUpgrader{}
			srv, store := serverAndStoreWithUpgrader(t, fake)

			body := map[string]any{"branch": "main", "afterTasksFinish": true}
			for k, v := range tc.body {
				body[k] = v
			}
			if code, _ := postUpgrade(t, srv, body); code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if scheduled, err := store.GetScheduledUpgrade(t.Context()); err != nil || scheduled != nil {
				t.Fatalf("an upgrade was scheduled anyway: (%+v, %v)", scheduled, err)
			}
		})
	}
}

// The same round trip cmd/grain's daemonUpgrade makes when an agent
// calls upgrade_deployment: over HTTP, through the route and into the
// row. The JSON names are the whole of the agreement between the two
// halves of that hop, and nothing either side of it would notice a
// requester that quietly stopped arriving.
func TestHTTPClientSchedulesAnUpgradeAsTheTaskThatAskedForIt(t *testing.T) {
	t.Parallel()
	srv, store := serverAndStoreWithUpgrader(t, &fakeUpgrader{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	remote := serverAccessClient(srv, ts.URL)
	ctx := t.Context()

	task := model.Principal{Kind: model.PrincipalAgent, ID: "353"}
	status, err := remote.StartUpgrade(ctx, "main", true, task)
	if err != nil {
		t.Fatalf("StartUpgrade over HTTP: %v", err)
	}
	if status.Scheduled == nil || status.Scheduled.ByID != "353" ||
		status.Scheduled.ByKind != string(model.PrincipalAgent) {
		t.Fatalf("scheduled = %+v, want it asked for by task 353", status.Scheduled)
	}
	scheduled, err := store.GetScheduledUpgrade(ctx)
	if err != nil || scheduled == nil || !scheduled.By.Same(task) || scheduled.By.Source != model.SourceSandbox {
		t.Fatalf("stored = (%+v, %v), want the task that asked for it", scheduled, err)
	}

	// A caller with no task to name -- `grain mcpserver` started without
	// -task, and the pane's own button -- runs on the access code, as the
	// deployment's actor or, where the CLI names who it is
	// (AccessNameHeader), as access-code:<name>.
	if _, err := remote.StartUpgrade(ctx, "main", true, model.Principal{}); err != nil {
		t.Fatalf("StartUpgrade naming nobody: %v", err)
	}
	scheduled, err = store.GetScheduledUpgrade(ctx)
	if err != nil || scheduled == nil || scheduled.By.Source != model.SourceAccessCode ||
		!(scheduled.By.Same(ui.DefaultActor("alice")) || strings.HasPrefix(scheduled.By.ID, ui.AccessCodeActorPrefix)) {
		t.Fatalf("stored = (%+v, %v), want this deployment's own actor or the code's named holder", scheduled, err)
	}
}

func TestCancellingAScheduledUpgrade(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{}
	srv, store := serverAndStoreWithUpgrader(t, fake)
	if code, _ := scheduleUpgrade(t, srv, "main"); code != http.StatusAccepted {
		t.Fatalf("scheduling = %d", code)
	}

	rec := do(t, srv, http.MethodDelete, "/api/upgrade/scheduled", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeWithScheduleResponse](t, rec)
	if !got.Cancelled {
		t.Error("cancelled = false, but there was an upgrade scheduled")
	}
	if got.Scheduled != nil {
		t.Errorf("still reporting %+v after cancelling it", got.Scheduled)
	}
	if scheduled, err := store.GetScheduledUpgrade(t.Context()); err != nil || scheduled != nil {
		t.Fatalf("the row outlived the cancellation: (%+v, %v)", scheduled, err)
	}

	// Cancelling nothing is not an error, for the reason resuming an
	// unpaused deployment is not: the caller wanted no upgrade pending.
	rec = do(t, srv, http.MethodDelete, "/api/upgrade/scheduled", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second DELETE = %d: %s", rec.Code, rec.Body)
	}
	if again := decode[upgradeWithScheduleResponse](t, rec); again.Cancelled {
		t.Error("cancelled = true with nothing scheduled")
	}
}

// Resuming a deployment that is paused only because it is waiting to
// upgrade calls the upgrade off as well. Without this the reconcile loop
// re-takes the hold on its next tick and the Resume button appears not
// to work.
func TestResumingCancelsTheUpgradeTheQueueWasBeingHeldFor(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})

	if err := store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "main", RequestedAt: time.Now().UTC(),
		By: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now().UTC(),
		Reason:   "waiting for this deployment to finish its work before upgrading to main",
		By:       model.ScheduledUpgradePrincipal,
	}); err != nil {
		t.Fatalf("holding the queue: %v", err)
	}

	status, resumed, err := client.ResumePipeline(ctx)
	if err != nil || !resumed {
		t.Fatalf("resume = (%v, %v)", resumed, err)
	}
	if status.CancelledUpgrade != "main" {
		t.Errorf("cancelledUpgrade = %q, want the branch it was waiting to upgrade onto", status.CancelledUpgrade)
	}
	if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled != nil {
		t.Fatalf("the upgrade is still scheduled after a resume: (%+v, %v)", scheduled, err)
	}
}

// An operator's own pause is not a scheduled upgrade's hold, and lifting
// it says nothing about any upgrade: the schedule stands, and the
// reconciler shuts the queue again because that is what it was asked to
// do.
func TestResumingAnOrdinaryPauseLeavesAScheduledUpgradeAlone(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})

	if err := store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "main", RequestedAt: time.Now().UTC(),
		By: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	if _, _, err := client.PausePipeline(ctx, "patching the host",
		model.Principal{Kind: model.PrincipalHuman, ID: "alice"}); err != nil {
		t.Fatalf("pausing: %v", err)
	}

	status, _, err := client.ResumePipeline(ctx)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if status.CancelledUpgrade != "" {
		t.Errorf("resuming an operator's pause reported cancelling %q", status.CancelledUpgrade)
	}
	if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled == nil {
		t.Fatalf("the schedule was dropped by a resume that had nothing to do with it: (%+v, %v)",
			scheduled, err)
	}
}

type fakeImageUpgrader struct {
	*fakeUpgrader
	startedImage string
}

func (f *fakeImageUpgrader) ImageTarget() (upgrade.ImageTarget, bool) {
	return upgrade.ImageTarget{Repository: "docker.io/grainai/grain"}, true
}

func (f *fakeImageUpgrader) StartImageWith(branch, image string, _ upgrade.ImageOptions) error {
	if err := f.Start(branch); err != nil {
		return err
	}
	f.startedImage = image
	return nil
}

// Starting an immediate upgrade, explicit-image upgrade, or rollback once a
// scheduled upgrade is pending supersedes the drain schedule as soon as the
// upgrader accepts it -- while a rejected immediate request leaves the
// operator's pending schedule intact.
func TestImmediateUpgradeAndRollbackSupersedeScheduledUpgrade(t *testing.T) {
	t.Parallel()

	t.Run("immediate_branch_upgrade_clears_schedule_and_unblocks_run_now_when_done", func(t *testing.T) {
		t.Parallel()
		fake := &fakeUpgrader{}
		srv, store := serverAndStoreWithUpgrader(t, fake)
		ctx := t.Context()

		if code, _ := scheduleUpgrade(t, srv, "latest"); code != http.StatusAccepted {
			t.Fatalf("scheduleUpgrade = %d, want 202", code)
		}

		// A rejected immediate upgrade (e.g. already running or outside allowlist)
		// must not silently drop the pending drain schedule.
		fake.startErr = upgrade.ErrUpgradeInProgress
		if code, _ := postUpgrade(t, srv, map[string]any{"branch": "latest", "afterTasksFinish": false}); code != http.StatusBadRequest {
			t.Fatalf("rejected immediate upgrade = %d, want 400", code)
		}
		fake.startErr = nil
		if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled == nil || scheduled.Branch != "latest" {
			t.Fatalf("rejected immediate upgrade altered scheduled_upgrade: (%+v, %v)", scheduled, err)
		}

		// An accepted immediate upgrade supersedes and clears scheduled_upgrade.
		code, got := postUpgrade(t, srv, map[string]any{"branch": "latest", "afterTasksFinish": false})
		if code != http.StatusAccepted {
			t.Fatalf("immediate upgrade = %d, want 202", code)
		}
		if fake.started != "latest" {
			t.Fatalf("fake.started = %q, want latest", fake.started)
		}
		if got.Scheduled != nil {
			t.Fatalf("immediate upgrade 202 still reported scheduled = %+v", got.Scheduled)
		}
		if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled != nil {
			t.Fatalf("scheduled_upgrade remained after immediate upgrade: (%+v, %v)", scheduled, err)
		}

		rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/upgrade = %d: %s", rec.Code, rec.Body)
		}
		if polled := decode[upgradeWithScheduleResponse](t, rec); polled.Scheduled != nil {
			t.Fatalf("GET /api/upgrade still reported scheduled = %+v", polled.Scheduled)
		}

		// Once the immediate upgrade finishes (e.g. post-restart or unchanged),
		// an operator pause must admit Run now rather than staying stuck behind
		// upgradeUnderway == true.
		fake.status.Phase = upgrade.PhaseOK
		pauseRec := do(t, srv, http.MethodPost, "/api/pipeline-pause", `{"reason":"manual check"}`)
		if pauseRec.Code != http.StatusOK {
			t.Fatalf("POST /api/pipeline-pause = %d: %s", pauseRec.Code, pauseRec.Body)
		}
		var pauseResp struct {
			Pause ui.PipelinePauseStatus `json:"pause"`
		}
		if err := json.Unmarshal(pauseRec.Body.Bytes(), &pauseResp); err != nil {
			t.Fatalf("decode pause response: %v", err)
		}
		if !pauseResp.Pause.RunNowPasses {
			t.Fatalf("RunNowPasses = false after immediate upgrade completed; upgradeUnderway remained stuck: %+v", pauseResp.Pause)
		}
	})

	t.Run("immediate_image_upgrade_clears_schedule", func(t *testing.T) {
		t.Parallel()
		fake := &fakeImageUpgrader{fakeUpgrader: &fakeUpgrader{}}
		srv, store := serverAndStoreWithUpgrader(t, fake)

		if code, _ := scheduleUpgrade(t, srv, "latest"); code != http.StatusAccepted {
			t.Fatalf("scheduleUpgrade = %d, want 202", code)
		}
		code, got := postUpgrade(t, srv, map[string]any{
			"branch":           "latest",
			"image":            "docker.io/grainai/grain:v3.2.0",
			"afterTasksFinish": false,
		})
		if code != http.StatusAccepted {
			t.Fatalf("immediate image upgrade = %d, want 202", code)
		}
		if fake.startedImage != "docker.io/grainai/grain:v3.2.0" {
			t.Fatalf("startedImage = %q", fake.startedImage)
		}
		if got.Scheduled != nil {
			t.Fatalf("immediate image upgrade still reported scheduled = %+v", got.Scheduled)
		}
		if scheduled, err := store.GetScheduledUpgrade(t.Context()); err != nil || scheduled != nil {
			t.Fatalf("scheduled_upgrade remained after immediate image upgrade: (%+v, %v)", scheduled, err)
		}
	})

	t.Run("rollback_clears_schedule_only_when_accepted", func(t *testing.T) {
		t.Parallel()
		fake := &fakeUpgrader{}
		srv, store := serverAndStoreWithUpgrader(t, fake)
		ctx := t.Context()

		if code, _ := scheduleUpgrade(t, srv, "latest"); code != http.StatusAccepted {
			t.Fatalf("scheduleUpgrade = %d, want 202", code)
		}

		// Invalid rollback (with branch set) is rejected before touching the schedule.
		if code, _ := postUpgrade(t, srv, map[string]any{"rollback": true, "branch": "main"}); code != http.StatusBadRequest {
			t.Fatalf("invalid rollback = %d, want 400", code)
		}
		if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled == nil {
			t.Fatalf("rejected rollback cleared scheduled_upgrade: (%+v, %v)", scheduled, err)
		}

		// Accepted rollback supersedes and clears scheduled_upgrade.
		code, got := postUpgrade(t, srv, map[string]any{"rollback": true})
		if code != http.StatusAccepted {
			t.Fatalf("rollback = %d, want 202", code)
		}
		if fake.rolledBack != 1 {
			t.Fatalf("rolledBack = %d, want 1", fake.rolledBack)
		}
		if got.Scheduled != nil {
			t.Fatalf("rollback 202 still reported scheduled = %+v", got.Scheduled)
		}
		if scheduled, err := store.GetScheduledUpgrade(ctx); err != nil || scheduled != nil {
			t.Fatalf("scheduled_upgrade remained after rollback: (%+v, %v)", scheduled, err)
		}
	})
}
