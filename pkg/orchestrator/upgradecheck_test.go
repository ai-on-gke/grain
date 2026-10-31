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

package orchestrator_test

// The upgrade check and the pause it can take (grain/task-38), reached
// only through the exported RunCycle -- this package's tests never call
// an unexported reconciler directly. A cycle here needs nothing beyond a
// store: with no ready task and no pull request link, dispatch and sync
// both no-op without a github.Client, Sandboxes or Framework of any kind
// (schedule_test.go's own note on why that is safe).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// upgradeDone is the status file a successful upgrade leaves behind: the
// deployment this very process came up as.
func upgradeDone(branch string, at time.Time) upgrade.Status {
	started := at.Add(-10 * time.Minute)
	return upgrade.Status{
		Branch:     branch,
		Phase:      upgrade.PhaseOK,
		Detail:     "built and installed; restarting",
		StartedAt:  &started,
		FinishedAt: &at,
	}
}

// runWithUpgrade runs one cycle against a store, with status as what
// this deployment's upgrader reports.
func runWithUpgrade(t *testing.T, store *model.Store, status upgrade.Status, now time.Time) error {
	t.Helper()
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			StateRepo:     func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}
	return orchestrator.RunCycle(t.Context(), deps, now)
}

// upgradeChecks is every task filed by the upgrade check, whatever its
// tag says about which upgrade it is about.
func upgradeChecks(t *testing.T, store *model.Store) []model.Task {
	t.Helper()
	all, err := store.ListTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if task.Origin.Reason == model.ReasonUpgradeCheck {
			out = append(out, task)
		}
	}
	return out
}

func settings(t *testing.T, store *model.Store, mutate func(*model.Config)) {
	t.Helper()
	cfg := model.DefaultConfig()
	mutate(&cfg)
	if err := store.PutConfig(t.Context(), cfg); err != nil {
		t.Fatalf("storing settings: %v", err)
	}
}

func TestUpgradeCheckFilesOneApprovedTaskAtTheHeadOfTheBacklog(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	// Something already queued, so "at the head" is a claim with
	// something to be ahead of.
	existing := filedTask(t, ctx, store, "1", model.RepoRef{Owner: "acme", Name: "widgets"})

	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d upgrade checks, want exactly 1", len(checks))
	}
	check := checks[0]

	if check.Approval == nil {
		t.Error("the check is not approved: the default mode is auto, which files it queued")
	}
	if check.Target != nil {
		t.Errorf("Target = %v, want none: a check reports what it found and pushes nothing", check.Target)
	}
	if check.OrderKey >= existing.OrderKey {
		t.Errorf("OrderKey = %v, want less than the queued task's %v -- the check runs next, not last",
			check.OrderKey, existing.OrderKey)
	}
	if !strings.Contains(check.Body, "main") || !strings.Contains(check.Title, "main") {
		t.Errorf("neither the title (%q) nor the body names the branch that was upgraded", check.Title)
	}
	if !strings.Contains(check.Body, "acme/grain-home") {
		t.Error("the body does not name the state repository the task is given read access to")
	}

	var grants []string
	for _, g := range check.Grants {
		grants = append(grants, g.Capability)
	}
	// Both, and separately: self-debug is the source this host runs,
	// grain-log is the journal it wrote. This task is the one that needs
	// both, which is exactly why the second is not folded into the first
	// -- every other self-debug task keeps only the source.
	slices.Sort(grants)
	if !slices.Equal(grants, []string{"grain-log", "self-debug"}) {
		t.Errorf("Grants = %v, want self-debug and grain-log", grants)
	}
	if len(check.Reads) != 1 || check.Reads[0].Name != "grain-home" {
		t.Errorf("Reads = %v, want the state repository", check.Reads)
	}

	// And it is filed once. A second cycle over the same upgrade finds
	// the tag it left and does nothing -- which is what stops a
	// deployment filing a check every poll interval for the rest of the
	// day.
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if got := upgradeChecks(t, store); len(got) != 1 {
		t.Fatalf("filed %d upgrade checks over two cycles, want 1", len(got))
	}

	// Not even once the check is closed: an upgrade is checked once, not
	// once per week (Store.HasTaskWithTag's own doc comment).
	if err := store.ObserveField(ctx, check.ID, baseTime, func(o *model.Observation) {
		o.ClosedAt = &baseTime
	}); err != nil {
		t.Fatalf("closing the check: %v", err)
	}
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("third cycle: %v", err)
	}
	if got := upgradeChecks(t, store); len(got) != 1 {
		t.Fatalf("filed %d upgrade checks after the first was closed, want 1", len(got))
	}
}

// The body asks about the daemon's log again, and every question in it
// is one the grants in fileUpgradeCheck can actually answer.
//
// The log question was asked once before there was any way to answer it,
// and that is the failure this whole arrangement exists for: the first
// check this deployment ran answered it out of the state repository
// dump, which is evidence about the log rather than the log itself, and
// reported clean. The question was taken out then and comes back now
// naming read_grain_log, which reads the journal on the host. So the
// assertion is not "the body mentions a log" -- it is that a question
// about the log arrives with the tool that answers it, in the same body.
func TestUpgradeCheckBodyAsksAboutTheLogAndNamesTheToolThatReadsIt(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d upgrade checks, want exactly 1", len(checks))
	}
	body := checks[0].Body

	asked, told, found := strings.Cut(body, "That is the whole of what this run can reach")
	if !found {
		t.Fatalf("the body never bounds what the run can reach:\n%s", body)
	}
	if !strings.Contains(strings.ToLower(asked), "log") {
		t.Errorf("the body no longer asks about the daemon's log at all:\n%s", asked)
	}
	if !strings.Contains(asked, "read_grain_log") {
		t.Errorf("the body asks about the log without naming the tool that reads it:\n%s", asked)
	}
	// The standing instruction survives the log question coming back: it
	// was never only about the log, and a run that cannot reach some
	// other part of this should still say so rather than substitute.
	if !strings.Contains(told, "could not") {
		t.Errorf("the body does not tell the run to report what it could not answer:\n%s", told)
	}
	// And it is told what an answer from that tool can look like, since
	// a truncated window read as a clean one is the same false clean by
	// another route (grainlog's own truncation markers).
	if !strings.Contains(body, "truncation") {
		t.Errorf("the body does not warn the run about truncated log reads:\n%s", body)
	}
}

// The same failure as the log question, one table over: the check is
// asked whether the queue resumed and whether runs have started since
// the restart, and it used to be told those answers were in
// tables/task.json and tables/task_run.json of the state repository.
// Nothing has exported either since staterepo.ExportConfig -- the
// repository holds settings and memory -- so the body has to send the
// run to the tools that do answer it, and must not name those files as
// anything but absent.
func TestUpgradeCheckBodySendsTheQueueQuestionToTheTaskTools(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d upgrade checks, want exactly 1", len(checks))
	}
	body := checks[0].Body

	if !strings.Contains(body, "grain task list") || !strings.Contains(body, "grain task read") {
		t.Errorf("the body asks what the deployment has dispatched without naming the tools "+
			"that read it:\n%s", body)
	}
	for _, sentence := range strings.Split(strings.ReplaceAll(body, "\n", " "), ". ") {
		for _, file := range []string{"tables/task.json", "tables/task_run.json"} {
			if !strings.Contains(sentence, file) {
				continue
			}
			if !strings.Contains(sentence, "no ") && !strings.Contains(sentence, "not ") {
				t.Errorf("the body still sends the check to %s, which no export writes:\n\n%s",
					file, strings.TrimSpace(sentence))
			}
		}
	}
}

func TestUpgradeCheckHonoursItsMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode     string
		filed    bool
		approved bool
	}{
		{mode: model.UpgradeCheckOff},
		{mode: model.UpgradeCheckApprove, filed: true},
		{mode: model.UpgradeCheckAuto, filed: true, approved: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			store, _ := openStore(t)
			settings(t, store, func(c *model.Config) { c.UpgradeCheckMode = tc.mode })

			if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
				t.Fatalf("cycle: %v", err)
			}
			checks := upgradeChecks(t, store)
			if got := len(checks) > 0; got != tc.filed {
				t.Fatalf("filed = %v (%d tasks), want %v", got, len(checks), tc.filed)
			}
			if !tc.filed {
				return
			}
			if got := checks[0].Approval != nil; got != tc.approved {
				t.Errorf("approved = %v, want %v", got, tc.approved)
			}
		})
	}
}

// A failed upgrade left the old binary running: there is no new
// deployment to check, and the UI already says the upgrade failed. Nor
// is an upgrade nobody is waiting to hear about any more -- one that
// finished a fortnight ago, which is what a daemon restarted for its own
// reasons finds in the status file.
func TestUpgradeCheckIgnoresFailedAndStaleUpgrades(t *testing.T) {
	t.Parallel()
	failed := upgradeDone("main", baseTime)
	failed.Phase = upgrade.PhaseFailed

	stale := upgradeDone("main", baseTime.Add(-14*24*time.Hour))

	running := upgrade.Status{Branch: "main", Phase: upgrade.PhaseRunning}

	for name, status := range map[string]upgrade.Status{
		"failed": failed, "stale": stale, "still running": running, "never upgraded": {},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := openStore(t)
			if err := runWithUpgrade(t, store, status, baseTime); err != nil {
				t.Fatalf("cycle: %v", err)
			}
			if got := upgradeChecks(t, store); len(got) != 0 {
				t.Fatalf("filed %d upgrade checks for a %s upgrade, want none", len(got), name)
			}
		})
	}
}

// A deployment that cannot upgrade itself has no upgrade to check, and
// must not be made to look like one that has never upgraded.
func TestUpgradeCheckFilesNothingWithNoUpgraderWired(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	if err := orchestrator.RunCycle(t.Context(), orchestrator.Deps{Store: store}, baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got := upgradeChecks(t, store); len(got) != 0 {
		t.Fatalf("filed %d upgrade checks with no upgrader wired, want none", len(got))
	}
}

// The whole of the "block other tasks until the check is done" half:
// the pause is taken with the check, it exempts the check and nothing
// else, and it is let go the cycle after the check completes.
func TestUpgradeCheckPausesThePipelineUntilTheCheckFinishes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(c *model.Config) {
		c.UpgradeCheckMode = model.UpgradeCheckAuto
		c.UpgradeCheckPausesPipeline = true
	})

	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d upgrade checks, want 1", len(checks))
	}
	check := checks[0]

	pause, err := store.GetPipelinePause(ctx)
	if err != nil || pause == nil {
		t.Fatalf("GetPipelinePause = (%+v, %v), want a pause", pause, err)
	}
	if pause.UntilTaskID != check.ID {
		t.Errorf("the pause waits on %q, want the check itself (%s)", pause.UntilTaskID, check.ID)
	}
	if !pause.Exempts(check.ID) || pause.Exempts(check.ID+"0") {
		t.Error("the pause does not exempt the check alone")
	}
	if pause.By.Kind != model.PrincipalAutomation {
		t.Errorf("the pause was taken by %+v, want grain's own automation principal", pause.By)
	}

	// Still paused while the check has not finished.
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("cycle while the check is outstanding: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause == nil {
		t.Fatalf("the pause was lifted while the check was still outstanding: (%+v, %v)", pause, err)
	}

	// The check completes, and the next cycle lets go.
	completed := baseTime.Add(2 * time.Minute)
	if err := store.ObserveField(ctx, check.ID, completed, func(o *model.Observation) {
		o.CompletedAt = &completed
	}); err != nil {
		t.Fatalf("completing the check: %v", err)
	}
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), completed.Add(time.Second)); err != nil {
		t.Fatalf("cycle after the check completed: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause != nil {
		t.Fatalf("GetPipelinePause = (%+v, %v) after the check completed, want no pause", pause, err)
	}
}

// A check that never finishes -- parked on a question nobody answers,
// or out of attempts -- does not hold the deployment forever.
func TestPipelinePauseIsLiftedOnceItsDeadlinePasses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(c *model.Config) {
		c.UpgradeCheckMode = model.UpgradeCheckAuto
		c.UpgradeCheckPausesPipeline = true
	})
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause == nil {
		t.Fatalf("GetPipelinePause = (%+v, %v), want a pause", pause, err)
	}

	// Nothing about the check has changed; only the clock has.
	late := baseTime.Add(7 * time.Hour)
	if err := runWithUpgrade(t, store, upgradeDone("main", baseTime), late); err != nil {
		t.Fatalf("cycle seven hours later: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause != nil {
		t.Fatalf("GetPipelinePause = (%+v, %v) seven hours in, want the pause given up on", pause, err)
	}
}

// An operator's own pause names no task, so nothing in the cycle may
// decide it is over.
func TestAnOperatorsPauseIsNeverLiftedByTheCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	pause := model.PipelinePause{
		PausedAt: baseTime.Add(-48 * time.Hour),
		Reason:   "patching the host",
		By:       model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}
	if err := store.SetPipelinePause(ctx, pause); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if err := orchestrator.RunCycle(t.Context(), orchestrator.Deps{Store: store}, baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	got, err := store.GetPipelinePause(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetPipelinePause = (%+v, %v) two days into an operator's pause, want it still in force", got, err)
	}
	if got.Reason != pause.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, pause.Reason)
	}
}

// An upgrader that cannot say what happened fails this reconciler and
// nothing else: every other reconciler in the cycle still runs, which is
// RunCycle's own contract.
func TestUpgradeCheckReportsAnUnreadableStatus(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) {
				return upgrade.Status{}, errors.New("upgrade-status.json is unreadable")
			},
		},
	}
	err := orchestrator.RunCycle(t.Context(), deps, baseTime)
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("RunCycle = %v, want the status read's own error", err)
	}
	if got := upgradeChecks(t, store); len(got) != 0 {
		t.Fatalf("filed %d upgrade checks off an unreadable status, want none", len(got))
	}
}

// The gate as the cycle actually applies it (grain/task-38): a paused
// deployment acquires no sandbox and starts no run, and the one task its
// pause exempts is dispatched by the very cycle that is dispatching
// nothing else.
//
// Through RunCycle against a real store and a real sandbox backend, not
// through dispatch.Cycle: dispatch_test.go already proves the option
// itself, and what is only decidable here is that reconcileDispatch
// reads the pause row and passes it on at all.
func TestRunCycleDispatchesNothingWhileThePipelineIsPaused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	filedTask(t, ctx, store, "t2", repo)

	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 2,
	}

	pause := model.PipelinePause{
		PausedAt: baseTime, Reason: "patching the host",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}
	if err := store.SetPipelinePause(ctx, pause); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle while paused: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
		t.Fatalf("a paused deployment acquired %+v, want no sandbox at all", acquired)
	}

	// The same pause, now waiting on t2: everything else stays put.
	pause.UntilTaskID = "t2"
	if err := store.SetPipelinePause(ctx, pause); err != nil {
		t.Fatalf("re-pausing with an exemption: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle while paused with an exemption: %v", err)
	}
	acquired, _ := sandboxes.calls()
	if len(acquired) != 1 || acquired[0].name != "t2-1" {
		t.Fatalf("Acquire calls = %+v, want exactly one, for the exempted task's own run", acquired)
	}
}

// Manual mode as the cycle wires it (grain/task-399): an upgrade's hold
// keeps even a Run now waiting, since its restart would discard the run;
// an operator's pause lets exactly that task through and nothing else.
func TestRunCycleRunNowPassesAnOperatorPauseButNotAnUpgradeHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	filedTask(t, ctx, store, "t2", repo)

	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 2,
	}
	at := baseTime
	if err := store.ObserveField(ctx, "t2", at, func(o *model.Observation) { o.RunNowAt = &at }); err != nil {
		t.Fatalf("requesting a run of t2: %v", err)
	}

	for _, by := range []model.Principal{model.UpgradePrincipal, model.ScheduledUpgradePrincipal} {
		if err := store.SetPipelinePause(ctx, model.PipelinePause{
			PausedAt: baseTime, Reason: "upgrading", By: by,
		}); err != nil {
			t.Fatalf("pausing: %v", err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle under %s's hold: %v", by.ID, err)
		}
		if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
			t.Fatalf("under %s's hold acquired %+v, want nothing: Run now does not pass an upgrade", by.ID, acquired)
		}
	}

	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: baseTime, Reason: "manual mode",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle under an operator's pause: %v", err)
	}
	acquired, _ := sandboxes.calls()
	if len(acquired) != 1 || acquired[0].name != "t2-1" {
		t.Fatalf("Acquire calls = %+v, want exactly one, for the task somebody asked to run now", acquired)
	}
}

// Console sessions pass a pipeline pause the way a Run now does: the
// pause grain takes to check its own upgrade holds the backlog but not a
// person's chat, while an upgrade's own hold, whose restart would end the
// session, still holds it.
func TestRunCycleDispatchesASessionUnderTheUpgradeCheckButNotAnUpgradeHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "check", repo)
	filedTask(t, ctx, store, "t1", repo)

	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}
	replyToSession(t, store, director.ID, baseTime.Add(time.Second))

	for _, by := range []model.Principal{model.UpgradePrincipal, model.ScheduledUpgradePrincipal} {
		if err := store.SetPipelinePause(ctx, model.PipelinePause{
			PausedAt: baseTime, Reason: "upgrading", By: by,
		}); err != nil {
			t.Fatalf("pausing: %v", err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
			t.Fatalf("RunCycle under %s's hold: %v", by.ID, err)
		}
		if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
			t.Fatalf("under %s's hold acquired %+v, want nothing: a session does not pass an upgrade", by.ID, acquired)
		}
	}

	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: baseTime, Reason: "checking the upgrade",
		By: model.UpgradePrincipal, UntilTaskID: "check",
	}); err != nil {
		t.Fatalf("pausing for the check: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle under the upgrade check's pause: %v", err)
	}
	acquired, _ := sandboxes.calls()
	names := map[string]bool{}
	for _, a := range acquired {
		names[a.name] = true
	}
	if len(acquired) != 2 || !names["check-1"] || !names[director.ID+"-1"] {
		t.Fatalf("Acquire calls = %+v, want the check and the session and not t1", acquired)
	}
}

// The hold is not the only shape an upgrade takes under a pause: neither
// upgrade path takes the row over from an operator's pause, and a pause
// taken on top of the hold replaces it. A Run now waits behind an upgrade
// running or scheduled whoever's pause is in the row, or anybody holding
// dispatch-control could pause over the hold and then run_task_now
// straight past it.
func TestRunCycleRunNowWaitsForAnUpgradeUnderAnOperatorPause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	phase := upgrade.PhaseOK
	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return upgrade.Status{Phase: phase}, nil },
		},
	}
	at := baseTime
	if err := store.ObserveField(ctx, "t1", at, func(o *model.Observation) { o.RunNowAt = &at }); err != nil {
		t.Fatalf("requesting a run of t1: %v", err)
	}
	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: baseTime, Reason: "manual mode",
		By: model.Principal{Kind: model.PrincipalAgent, ID: "7"},
	}); err != nil {
		t.Fatalf("pausing: %v", err)
	}

	if err := store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "main", RequestedAt: baseTime, By: model.ScheduledUpgradePrincipal,
	}); err != nil {
		t.Fatalf("scheduling an upgrade: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle with an upgrade scheduled: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
		t.Fatalf("with an upgrade scheduled acquired %+v, want nothing", acquired)
	}
	if _, err := store.ClearScheduledUpgrade(ctx); err != nil {
		t.Fatalf("clearing the schedule: %v", err)
	}

	phase = upgrade.PhaseRunning
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle with an upgrade running: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
		t.Fatalf("with an upgrade running acquired %+v, want nothing", acquired)
	}

	phase = upgrade.PhaseOK
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("RunCycle once the upgrade is over: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 1 {
		t.Fatalf("once the upgrade is over acquired %+v, want the task asked for", acquired)
	}
}

func TestUpgradeCheckChannelSelection(t *testing.T) {
	t.Parallel()
	channels := []string{"latest", "v1.2.0", "rc/v1.2", "candidate", "main"}
	for _, channel := range channels {
		t.Run(channel, func(t *testing.T) {
			store, _ := openStore(t)
			if err := runWithUpgrade(t, store, upgradeDone(channel, baseTime), baseTime); err != nil {
				t.Fatalf("cycle: %v", err)
			}
			checks := upgradeChecks(t, store)
			if len(checks) != 1 {
				t.Fatalf("filed %d upgrade checks for channel %s, want 1", len(checks), channel)
			}
			if !strings.Contains(checks[0].Title, channel) {
				t.Errorf("task title %q does not mention channel %s", checks[0].Title, channel)
			}
			if !strings.Contains(checks[0].Body, channel) {
				t.Errorf("task body does not mention channel %s", channel)
			}
		})
	}
}

// The check reports how long the restart left grain unreachable, when
// this process is the one that restart started.
func TestUpgradeCheckReportsTheRestartGap(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	down := now.Add(-50 * time.Second)
	gap := upgrade.RestartGap{
		Mode:      "container",
		StartedAt: now.Add(-45 * time.Second),
		Services: []upgrade.ServiceGap{
			{Service: upgrade.ServiceAPI, DownAt: &down, UpAt: now.Add(-42 * time.Second)},
			{Service: upgrade.ServiceGitProxy, DownAt: &down, UpAt: now.Add(-40 * time.Second), FromShutdown: true},
		},
	}
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			RestartGap:    func() upgrade.RestartGap { return gap },
			StateRepo:     func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatal(err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d checks, want 1", len(checks))
	}
	body := checks[0].Body
	for _, want := range []string{"unreachable (container", "API 8s", "git proxy at most 10s"} {
		if !strings.Contains(body, want) {
			t.Errorf("check body does not say %q:\n%s", want, body)
		}
	}
}

// The check reports how many runs the restart's adoption pass adopted,
// how many it lost, and why (docs/non-disruptive-upgrade.md, piece 6
// step 6) -- when this process is the one that restart started.
func TestUpgradeCheckReportsAdoptedAndLostRuns(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	plan := runhost.Plan{
		Adopt: []runhost.Adoption{
			{Claim: runhost.Claim{Run: "run-a", Task: "41"}, Host: runhost.Info{Name: "rh-a"}, Gap: 12 * time.Second},
			{Claim: runhost.Claim{Run: "run-b", Task: "42"}, Host: runhost.Info{Name: "rh-b"}, Gap: 12 * time.Second},
		},
		Finish:  []runhost.Ending{{Claim: runhost.Claim{Run: "run-c", Task: "43"}, Reason: "its run host is gone"}},
		Release: []runhost.Release{{Host: runhost.Info{Name: "rh-stray"}, Reason: "no live run claims it"}},
	}
	report := orchestrator.NewAdoptionReport(now.Add(-40*time.Second), plan, map[string]bool{"run-a": true})
	report.CIAdopted = []orchestrator.AdoptedCIJobNote{
		{Run: "12", Repo: "acme/widgets", Ref: "main", Job: "test", Sandbox: "ci-12-test"},
	}
	report.CILost = []orchestrator.AdoptedCIJobNote{
		{Run: "13", Repo: "acme/widgets", Ref: "feat", Job: "lint", Reason: "its sandbox pod is gone"},
	}
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			Adoption:      func() *orchestrator.AdoptionReport { return report },
		},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatal(err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d checks, want 1", len(checks))
	}
	body := checks[0].Body
	for _, want := range []string{
		"1 adopted, 2 lost (lost by framework: unknown 2), and 1 run host(s) no live run claimed were freed; 1 CI jobs adopted, 1 lost.",
		"adopted: run run-a of task 41 (its spool went 12s unread)",
		"lost: run run-b of task 42, finished as interrupted because its run host rh-b was still there, but this daemon could not adopt the run",
		"lost: run run-c of task 43, finished as interrupted because its run host is gone",
		"adopted CI job: test in run 12 (acme/widgets@main, sandbox ci-12-test)",
		"lost CI job: lint in run 13 (acme/widgets@feat): its sandbox pod is gone",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("check body does not say %q:\n%s", want, body)
		}
	}
}

// Runs whose CLI was a child of the daemon are finished as interrupted
// by the daemon that stops (or the next one's startup recovery), never
// seen by the adoption pass. The check counts them as lost, with the
// framework each was on, beside what the pass adopted -- the 10-26
// upgrade lost eight claude runs and its check said "1 adopted, 0 lost"
// (task 1244).
func TestUpgradeCheckCountsRunsThatStoppedWithTheDaemon(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Date(2026, 10, 26, 7, 20, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	for _, r := range []struct {
		run, task, framework string
		finished             time.Time
	}{
		{"969-3", "969", "claude", now.Add(-5 * time.Minute)},
		{"876-3", "876", "codex", now.Add(-5 * time.Minute)},
		{"900-2", "900", "", now.Add(-5 * time.Minute)},
		{"1115-1", "1115", "antigravity", time.Time{}},
		{"700-1", "700", "claude", now.Add(-2 * time.Hour)},
	} {
		if err := store.PutTask(ctx, model.Task{ID: r.task, Title: "t" + r.task}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: r.run, TaskID: r.task, Attempt: 1, StartedAt: now.Add(-3 * time.Hour)},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if r.framework != "" {
			if err := store.SetRunModel(ctx, r.run, "m", r.framework); err != nil {
				t.Fatal(err)
			}
		}
		if !r.finished.IsZero() {
			if err := store.FinishRun(ctx, r.run, r.finished, model.InterruptedOutcome, "stopped"); err != nil {
				t.Fatal(err)
			}
		}
	}
	report := orchestrator.NewAdoptionReport(now.Add(-40*time.Second), runhost.Plan{
		Adopt: []runhost.Adoption{{Claim: runhost.Claim{Run: "1115-1", Task: "1115"}, Gap: 9 * time.Second}},
	}, map[string]bool{"1115-1": true})
	body := fileCheckWith(t, store, ctx, status, report, nil, now)
	for _, want := range []string{
		"1 adopted, 3 lost (lost by framework: claude 1, codex 1, setup 1)",
		"adopted: run 1115-1 of task 1115 on antigravity (its spool went 9s unread)",
		"lost: run 969-3 of task 969 on claude, finished as interrupted because " + orchestrator.StoppedWithTheDaemon,
		"lost: run 876-3 of task 876 on codex, finished as interrupted because",
		"lost: run 900-2 of task 900, finished as interrupted because it was still setting up",
		"Any lost run is a regression",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("check body does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "700-1") {
		t.Errorf("check body counts a run interrupted before the upgrade started:\n%s", body)
	}
}

// fileCheckWith runs one cycle that files the upgrade check for status,
// with report as the adoption pass's and interrupts as the install's
// UpgradeInterrupts (nil for none set), and returns its body.
func fileCheckWith(t *testing.T, store *model.Store, ctx context.Context, status upgrade.Status,
	report *orchestrator.AdoptionReport, interrupts map[string]string, now time.Time) string {
	t.Helper()
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			Adoption:      func() *orchestrator.AdoptionReport { return report },
		},
	}
	if interrupts != nil {
		deps.Config.UpgradeInterrupts = func(context.Context) map[string]string { return interrupts }
	}
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d checks, want 1", len(checks))
	}
	return checks[0].Body
}

// A run lost on a CLI whose own run location loses its runs on a
// restart -- a cloudtop install's claude, which cloudtop-install.sh keeps
// in the daemon's container -- is what that install was set up to do,
// and the check says so rather than calling it a regression; a lost run
// on any other CLI still is one (task 1506).
func TestUpgradeCheckSaysWhichLostRunsTheInstallExpects(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Date(2026, 10, 27, 22, 45, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	for _, r := range []struct{ run, task, framework string }{
		{"1490-1", "1490", "claude"},
		{"1491-1", "1491", "claude"},
		{"1492-1", "1492", "antigravity"},
	} {
		if err := store.PutTask(ctx, model.Task{ID: r.task, Title: "t" + r.task}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: r.run, TaskID: r.task, Attempt: 1, StartedAt: now.Add(-time.Hour)},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRunModel(ctx, r.run, "m", r.framework); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, r.run, now.Add(-30*time.Second), model.InterruptedOutcome, "stopped"); err != nil {
			t.Fatal(err)
		}
	}
	report := orchestrator.NewAdoptionReport(now.Add(-20*time.Second), runhost.Plan{}, nil)
	claudeNote := "A daemon upgrade interrupts claude runs: claude runs in the daemon's own container"
	body := fileCheckWith(t, store, ctx, status, report, map[string]string{"claude": claudeNote}, now)
	for _, want := range []string{
		"0 adopted, 3 lost (lost by framework: antigravity 1, claude 2)",
		"lost runs are expected here rather than a regression",
		"- claude: " + claudeNote,
		"Any other lost run is a regression",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("check body does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "- antigravity: ") {
		t.Errorf("check body calls the lost antigravity run expected:\n%s", body)
	}
}

// Console and Director sessions are durable tasks whose turns are
// task_run attempts, so the adoption pass carries them by the same rules
// as any run -- and the check names each as the session it is, and counts
// them, since a lost one is a conversation somebody was in the middle of:
// the Director's turn adopted from grain hostd, a console session's lost
// with the daemon, both beside an ordinary run.
func TestUpgradeCheckCountsConsoleAndDirectorSessions(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Date(2026, 10, 27, 7, 20, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	for _, r := range []struct {
		run, task, session string
		finished           bool
	}{
		{"dir-4", "dir", model.DirectorSessionID, false},
		{"ops-2", "ops", "ops", true},
		{"500-1", "500", "", true},
	} {
		task := model.Task{ID: r.task, Title: "t" + r.task}
		if r.session != "" {
			task.Tags = []string{model.ConsoleSessionTag(r.session)}
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: r.run, TaskID: r.task, Attempt: 1, StartedAt: now.Add(-3 * time.Hour)},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRunModel(ctx, r.run, "m", "claude"); err != nil {
			t.Fatal(err)
		}
		if r.finished {
			if err := store.FinishRun(ctx, r.run, now.Add(-5*time.Minute), model.InterruptedOutcome, "stopped"); err != nil {
				t.Fatal(err)
			}
		}
	}
	report := orchestrator.NewAdoptionReport(now.Add(-40*time.Second), runhost.Plan{
		Adopt: []runhost.Adoption{{Claim: runhost.Claim{Run: "dir-4", Task: "dir"}, Gap: 9 * time.Second}},
	}, map[string]bool{"dir-4": true})
	got := orchestrator.UpgradeAdoption(ctx, store, func() *orchestrator.AdoptionReport { return report }, status, now)
	if len(got.Adopted) != 1 || got.Adopted[0].Session != model.DirectorSessionID {
		t.Errorf("adopted = %+v, want the Director's turn, named as the Director session", got.Adopted)
	}
	sessions := map[string]string{}
	for _, l := range got.Lost {
		sessions[l.Run] = l.Session
	}
	if len(got.Lost) != 2 || sessions["ops-2"] != "ops" || sessions["500-1"] != "" {
		t.Errorf("lost = %+v, want console session ops and the ordinary run", got.Lost)
	}

	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			Adoption:      func() *orchestrator.AdoptionReport { return report },
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d checks, want 1", len(checks))
	}
	body := checks[0].Body
	for _, want := range []string{
		"1 adopted, 2 lost",
		"of those, console and Director sessions: 1 adopted, 1 lost",
		"adopted: run dir-4 of task dir (the Director session) on claude",
		"lost: run ops-2 of task ops (console session ops) on claude, finished as interrupted because " + orchestrator.StoppedWithTheDaemon,
		"lost: run 500-1 of task 500 on claude,",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("check body does not say %q:\n%s", want, body)
		}
	}
}

// A run host released because the run claiming it was lost -- refused by
// protocol, past its deadline -- is that lost run's, not one "no live run
// claimed". The daemon walkthrough's startup said "2 unclaimed run host(s)
// freed" for one stray and one refused-protocol run host
// (docs/test-runs/2026-10-14-run-hosts-daemon.md).
func TestAdoptionReportFreedCountsOnlyUnclaimedRunHosts(t *testing.T) {
	t.Parallel()
	refused := runhost.Info{Name: "rh-p", Labels: runhost.Labels{Run: "run-p", Protocol: runhost.Protocol + 1}}
	stray := runhost.Info{Name: "rh-stray", Labels: runhost.Labels{Run: "run-gone-long-ago"}}
	plan := runhost.Decide([]runhost.Claim{{Run: "run-p", Task: "44"}}, []runhost.Info{refused, stray},
		func(runhost.Info) runhost.SpoolState { return runhost.SpoolState{} }, time.Now())
	if len(plan.Release) != 2 || len(plan.Finish) != 1 {
		t.Fatalf("plan = %+v, want both run hosts released and run-p finished", plan)
	}
	report := orchestrator.NewAdoptionReport(time.Now(), plan, nil)
	if report.Freed != 1 || len(report.Lost) != 1 {
		t.Fatalf("report freed %d and lost %d, want 1 unclaimed run host freed and run-p lost", report.Freed, len(report.Lost))
	}
}

// An adoption pass older than the upgrade belongs to some other restart.
func TestUpgradeCheckLeavesOutAnOlderAdoption(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	status := upgradeDone("main", now.Add(-time.Minute))
	report := orchestrator.NewAdoptionReport(now.Add(-time.Hour), runhost.Plan{}, nil)
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			UpgradeStatus: func() (upgrade.Status, error) { return status, nil },
			Adoption:      func() *orchestrator.AdoptionReport { return report },
		},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatal(err)
	}
	checks := upgradeChecks(t, store)
	if len(checks) != 1 {
		t.Fatalf("filed %d checks, want 1", len(checks))
	}
	if strings.Contains(checks[0].Body, "adopted") {
		t.Errorf("check body reports an adoption pass from before the upgrade:\n%s", checks[0].Body)
	}
}

func TestUpgradeCheckIgnoresUnchangedUpgrade(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(c *model.Config) {
		c.UpgradeCheckMode = model.UpgradeCheckAuto
		c.UpgradeCheckPausesPipeline = true
	})
	status := upgradeDone("main", baseTime)
	status.Unchanged = true
	status.Detail = "the version on main has not changed"
	if err := runWithUpgrade(t, store, status, baseTime); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if checks := upgradeChecks(t, store); len(checks) != 0 {
		t.Errorf("filed %d upgrade checks for an unchanged upgrade, want 0", len(checks))
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil || pause != nil {
		t.Errorf("GetPipelinePause = (%+v, %v), want no pause for an unchanged upgrade", pause, err)
	}
}

// An upgrade with a restart to come is the next process's to check: a
// cycle on the outgoing daemon -- one that ticks between the upgrade
// staging the new build and the restart replacing this process, while
// in-flight runs are flushed -- files nothing, because the build hash,
// the restart gap and the adoption it would report do not exist yet
// (task 1937). The daemon the restart brings up files it.
func TestUpgradeCheckWaitsForTheRestartToComplete(t *testing.T) {
	t.Parallel()
	finished := baseTime.Add(-time.Minute)
	restartAt := baseTime.Add(-30 * time.Second)
	restarting := func(state upgrade.StepState, at *time.Time) upgrade.Status {
		status := upgradeDone("main", finished)
		status.Steps = []upgrade.Step{
			{Name: "install", State: upgrade.StepDone},
			{Name: "restart", State: state, StartedAt: at},
		}
		return status
	}
	outgoing := baseTime.Add(-24 * time.Hour)
	incoming := baseTime.Add(-10 * time.Second)

	for _, c := range []struct {
		name    string
		status  upgrade.Status
		process time.Time
		want    int
	}{
		{"outgoing daemon, restart not yet started", restarting(upgrade.StepPending, nil), outgoing, 0},
		{"outgoing daemon, restart command running", restarting(upgrade.StepRunning, &restartAt), outgoing, 0},
		{"outgoing kube pod, rollout under way", upgradeDone("main", finished), outgoing, 0},
		{"daemon the restart brought up", restarting(upgrade.StepRunning, &restartAt), incoming, 1},
		{"pod the rollout brought up", upgradeDone("main", finished), incoming, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			store, _ := openStore(t)
			status := c.status
			deps := orchestrator.Deps{
				Store: store,
				Config: orchestrator.Config{
					UpgradeStatus:    func() (upgrade.Status, error) { return status, nil },
					ProcessStartedAt: c.process,
					StateRepo:        func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				},
			}
			if err := orchestrator.RunCycle(t.Context(), deps, baseTime); err != nil {
				t.Fatalf("cycle: %v", err)
			}
			if got := upgradeChecks(t, store); len(got) != c.want {
				t.Fatalf("filed %d upgrade checks, want %d", len(got), c.want)
			}
		})
	}
}
