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

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

// upgrader is the automation principal every upgrade check is attributed
// to -- its own actor beside "schedule" (a schedule's firing) and
// "merge-queue" (an automatic repair), so a human reading the task's
// origin can tell which of grain's own mechanisms filed it. The same
// principal an upgrade holds the queue as (upgradehold.go), which is
// exported because that hold is released by a different process than
// took it.
var upgrader = UpgradePrincipal

// upgradeCheckWindow bounds how long after an upgrade finished a check
// of it is still worth filing.
//
// The ordinary path is minutes: an upgrade's own restart brings this
// process up, and the first cycle it runs finds a status file written
// just before that restart. The window is for the other paths, where the
// status file is old news -- a daemon restarted for its own reasons a
// month after the last upgrade, a store moved aside so the tag that
// records "this upgrade was already checked" is gone, a deployment that
// has just turned the setting on. None of those is an upgrade anybody is
// waiting to hear about, and a check filed for one would be a run spent
// re-examining a deployment that has been up for weeks.
//
// A day rather than an hour: an upgrade whose restart failed can leave a
// deployment down for a good while before somebody brings it back, and
// that is exactly the case where the check is worth having.
const upgradeCheckWindow = 24 * time.Hour

// upgradeCheckTag is the idempotency marker an upgrade check carries --
// the same shape as a schedule's own model.ScheduleFiringTag, keyed by the upgrade
// rather than by the schedule.
//
// The key is the branch and the instant the upgrade started, which
// together name one upgrade: a second upgrade of the same branch is a
// different one and gets its own check. An upgrade with no start time
// recorded (a status file from a build that predates them) keys on the
// branch alone, which at worst checks such a deployment once.
//
// It is read with Store.HasTaskWithTag rather than HasOpenTaskWithTag:
// an upgrade is checked once and not once per week -- see that method's
// own doc comment on the difference.
func upgradeCheckTag(status upgrade.Status) string {
	if status.StartedAt == nil {
		return "upgrade-check:" + status.Branch
	}
	return fmt.Sprintf("upgrade-check:%s:%d", status.Branch, status.StartedAt.UTC().Unix())
}

// reconcileUpgradeCheck files a task to check the upgrade this
// deployment has just installed, once per upgrade, and -- if the
// deployment asks for it -- holds the rest of the queue until that check
// is done.
//
// The check exists because pkg/upgrade cannot answer the question it
// leaves behind. It builds, installs, health-checks the new binary by
// running it once, and restarts the service; what it cannot do is look
// at the deployment that came up afterwards and say whether it is
// actually working -- whether runs dispatch, whether reconcilers are
// still firing, whether the state repository still syncs, whether the
// runs since the restart look like the runs before it. That is an
// agent's job, and this is the task that asks an agent to do it.
//
// It is level-triggered like every other reconciler, and reads the
// upgrade's own status file rather than being told: the process that
// performed the upgrade is not this one -- the restart replaced it -- so
// there is no in-memory event to carry across, and a status file is
// exactly what pkg/upgrade already persists across that restart. What
// stops it filing the same check every tick afterwards is the tag, which
// is durable in the same store the task is.
//
// Nothing here files anything when the deployment has no upgrader wired
// (Config.UpgradeStatus nil -- a test, `grain demo`, a deployment that
// has not opted into in-place upgrades), when its mode says not to
// (model.UpgradeCheckOff), when the last upgrade did not succeed (a
// failed upgrade left the old binary running: there is no new deployment
// to check, and the UI already says the upgrade failed), or when that
// upgrade is older than upgradeCheckWindow.
func reconcileUpgradeCheck(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Config.UpgradeStatus == nil {
		return nil
	}
	mode, pauses, err := upgradeCheckSettings(ctx, deps)
	if err != nil {
		return err
	}
	if mode == model.UpgradeCheckOff {
		return nil
	}
	status, err := deps.Config.UpgradeStatus()
	if err != nil {
		return fmt.Errorf("orchestrator: reading the last upgrade's status: %w", err)
	}
	if status.Phase != upgrade.PhaseOK || status.Unchanged || status.FinishedAt == nil {
		return nil
	}
	if now.Sub(status.FinishedAt.UTC()) > upgradeCheckWindow {
		return nil
	}
	// This process is the one the upgrade is about to restart away, not
	// the one it brings up: the check is the next process's to file,
	// with that build's hash, the restart's gap and what its adoption
	// pass did -- none of which exists yet (task 1937).
	if status.RestartPending(deps.Config.ProcessStartedAt) {
		return nil
	}
	tag := upgradeCheckTag(status)
	checked, err := deps.Store.HasTaskWithTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if checked {
		return nil
	}

	taskID, err := fileUpgradeCheck(ctx, deps, status, tag, mode, now)
	if err != nil {
		return fmt.Errorf("orchestrator: filing a check of the upgrade to %s: %w", status.Branch, err)
	}
	log.Printf("orchestrator: filed task %s to check the upgrade to %s (%s)", taskID, status.Branch, mode)

	if !pauses {
		return nil
	}
	// Taken here, in the same cycle as the filing, and deliberately not
	// retried by a later one: a cycle that arrives afterwards cannot tell
	// a pause that failed to be written from one an operator has just
	// lifted by hand, and re-closing a gate somebody has just opened is
	// the worse of the two mistakes. So a pause that cannot be written is
	// logged and given up on -- the check still runs, at the head of the
	// queue, with the rest of the deployment moving beside it.
	pause := model.PipelinePause{
		PausedAt:    now.UTC(),
		Reason:      fmt.Sprintf("checking the upgrade to %s (task %s)", status.Branch, taskID),
		By:          upgrader,
		UntilTaskID: taskID,
	}
	if err := deps.Store.SetPipelinePause(ctx, pause); err != nil {
		log.Printf("orchestrator: task %s was filed to check the upgrade to %s, but the pipeline could not "+
			"be paused for it (%v) -- the check runs with the rest of the queue still moving", taskID, status.Branch, err)
		return nil
	}
	log.Printf("orchestrator: pipeline paused until task %s has checked the upgrade to %s", taskID, status.Branch)
	return nil
}

// upgradeCheckSettings reads the two knobs this reconciler answers to
// out of the store, on every cycle, rather than off deps.Config: they are
// model.Config's own (grain_config), and RunCycle refreshes only the
// handful of settings this package's *dispatch* path consumes. A
// deployment with no configuration row yet gets model.DefaultConfig's
// answer, which is the same one every other reader of an unconfigured
// deployment gets.
func upgradeCheckSettings(ctx context.Context, deps Deps) (mode string, pauses bool, err error) {
	stored, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return "", false, fmt.Errorf("orchestrator: reading the upgrade-check settings: %w", err)
	}
	cfg := model.DefaultConfig()
	if stored != nil {
		cfg = *stored
	}
	return model.NormalizeUpgradeCheckMode(cfg.UpgradeCheckMode), cfg.UpgradeCheckPausesPipeline, nil
}

// fileUpgradeCheck writes the check task and returns its id.
//
// The shape of the task is the whole of what this feature is, so each
// piece of it is a decision:
//
//   - No Target. A check reports what it found -- through
//     comment_on_issue, which is what its own body asks for -- and has no
//     change to push. Giving it a write target would invite one.
//   - Reads: the state repository, when this deployment has one. That is
//     grain's own database as text (pkg/staterepo): the tasks, the runs
//     and the settings this deployment is running on, which is what
//     "did the upgrade break anything?" is actually answered out of.
//   - The self-debug grant: grain's own source checkout on this host,
//     read-only at grain-src/ -- the code that is actually running here,
//     rather than whatever a branch says -- the task readers, and the
//     api:self-debug preset (api:tasks:read and api:deployment:read),
//     whose deployment:read routes include the redacted log sources the
//     daemon serves at /api/logs.
//   - The grain-log grant, which is one tool onto this host's own
//     systemd journal, bounded and redacted on the host
//     (pkg/capability/grainlog). The upgrade check holds it beside
//     self-debug because read_grain_log reads the journal directly,
//     whatever log sources the daemon has been configured to serve.
//   - Approval per the deployment's own mode, and the head of the
//     backlog either way: a check that runs after the day's queue has
//     drained is a check of a deployment that has already spent the day
//     running on whatever the upgrade did.
func fileUpgradeCheck(ctx context.Context, deps Deps, status upgrade.Status, tag, mode string,
	now time.Time) (string, error) {
	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating an id: %w", err)
	}
	orderKey, err := deps.Store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return "", err
	}
	task := model.Task{
		ID:     id,
		Intent: model.IntentAnalyze,
		Title:  "Check the upgrade to " + status.Branch,
		Body: upgradeCheckBody(status, deps.Config.settingsRepo(), restartGapFor(deps.Config, status),
			upgradeAdoption(ctx, deps, status, now)),
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: upgrader},
			Reason:      model.ReasonUpgradeCheck,
		},
		Binding: model.BindingDirective,
		Grants: []model.Grant{
			{Capability: selfdebug.CapabilityName, Via: model.GrantByGrain},
			{Capability: grainlog.CapabilityName, Via: model.GrantByGrain},
		},
		Tags:      []string{tag},
		CreatedAt: &now,
		OrderKey:  orderKey,
	}
	if repo := deps.Config.settingsRepo(); repo.Owner != "" {
		task.Reads = append(task.Reads, repo)
	}
	if mode == model.UpgradeCheckAuto {
		task.Approval = &model.Attribution{Actor: upgrader}
		task.ApprovedAt = &now
	}
	if err := deps.Store.PutTask(ctx, task); err != nil {
		return "", err
	}
	return id, nil
}

// upgradeAdoption is adoptionFor completed from the store
// (UpgradeAdoption).
func upgradeAdoption(ctx context.Context, deps Deps, status upgrade.Status, now time.Time) *AdoptionReport {
	report := UpgradeAdoption(ctx, deps.Store, deps.Config.Adoption, status, now)
	if report != nil && deps.Config.UpgradeInterrupts != nil {
		report.Expected = deps.Config.UpgradeInterrupts(ctx)
	}
	return report
}

// UpgradeAdoption is the adoption report adoption made, when the restart
// that started this process is the one status's upgrade ended with
// (adoptionFor), completed from the store (AdoptionReport.withRows):
// every run that ended interrupted since the upgrade started counts as
// lost, and every run is named with its framework. A store that cannot
// be read leaves the adoption pass's own report, said in the log. It is
// what the post-upgrade check is told and what the upgrade pane shows
// for the last restart, so the two say the same thing; nil when there is
// nothing to report.
func UpgradeAdoption(ctx context.Context, store *model.Store, adoption func() *AdoptionReport,
	status upgrade.Status, now time.Time) *AdoptionReport {
	report := adoptionFor(adoption, status.StartedAt)
	var interrupted []model.RunEnd
	if status.StartedAt != nil {
		var err error
		if interrupted, err = store.InterruptedRunsSince(ctx, *status.StartedAt); err != nil {
			log.Printf("orchestrator: upgrade check: %v", err)
		}
	}
	frameworks, err := store.RunFrameworks(ctx, report.RunIDs())
	if err != nil {
		log.Printf("orchestrator: upgrade check: %v", err)
	}
	tasks := report.TaskIDs()
	for _, e := range interrupted {
		tasks = append(tasks, e.Task)
	}
	return report.withRows(now, interrupted, frameworks, consoleSessions(ctx, store, tasks))
}

// consoleSessions is the console session each of tasks is, by task id,
// for AdoptedRunNote.Session; a task that is not one, or cannot be read,
// is left out.
func consoleSessions(ctx context.Context, store *model.Store, tasks []string) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	for _, id := range tasks {
		if seen[id] || id == "" {
			continue
		}
		seen[id] = true
		task, err := store.GetTask(ctx, id)
		if err != nil || task == nil {
			continue
		}
		if session, ok := model.ConsoleSessionIDOf(*task); ok {
			out[id] = session
		}
	}
	return out
}

// restartGapFor is the restart gap this process measured, when the
// restart that started it is the one status's upgrade ended with: a
// process that started before the upgrade did is not that restart's
// successor, and its gap is about some other one.
func restartGapFor(cfg Config, status upgrade.Status) upgrade.RestartGap {
	if cfg.RestartGap == nil {
		return upgrade.RestartGap{}
	}
	gap := cfg.RestartGap()
	if status.StartedAt != nil && gap.StartedAt.Before(*status.StartedAt) {
		return upgrade.RestartGap{}
	}
	return gap
}

// upgradeCheckBody is what the run is actually told: what was upgraded,
// when, what the upgrade itself reported, which build is now running --
// facts no agent in a sandbox can find out for itself, since the process
// that performed the upgrade is gone and the sandbox is not this host --
// followed by what to go and look at.
//
// What it asks for is bounded by what the grants in fileUpgradeCheck can
// actually reach: the source checkout this host runs from, the state
// repository if this deployment has one, and -- through the grain-log
// grant -- this host's own journal.
//
// The log question is here because of how its absence went. It was asked
// before there was any way to answer it, and the first check this
// deployment ran answered it out of the state repository dump, which is
// evidence about the log rather than the log itself, and reported clean.
// A panic a reconciler recovers from, or an error repeating every tick
// without stopping anything, leaves no trace in that dump. So the
// question was removed rather than left unanswerable, and it comes back
// now naming the tool that answers it: read_grain_log
// (pkg/capability/grainlog), which reads the journal on the host, bounded
// and redacted there. A question naming the tool that answers it is a
// different thing from one that only hopes.
//
// The standing instruction below stays either way, and is not about the
// log alone: any part of this a run cannot reach should come back as
// "I could not answer this", never as the nearest thing it could reach.
// A check that reports clean because it could not look is worse than one
// that reports it could not look.
//
// It asks for a comment and not a change on purpose. A check that
// diagnoses is worth having on every upgrade; a check that starts
// repairing a deployment nobody has looked at yet, unattended, is not.
//
// gap, when it has any services, is how long the restart left grain
// unreachable (restartGapFor), reported so the check can say whether the
// gap was ordinary for this mode -- the measurement Stage 1 of
// docs/non-disruptive-upgrade.md asks every upgrade to take.
//
// adoption, when set, is what the restart did to the agent runs in run
// hosts (adoptionFor): how many were adopted, how many lost, and why.
func upgradeCheckBody(status upgrade.Status, stateRepo model.RepoRef, gap upgrade.RestartGap,
	adoption *AdoptionReport) string {
	var b strings.Builder
	b.WriteString("grain has just upgraded itself and restarted. Check that the deployment " +
		"that came back up is actually healthy, and report what you find.\n\n")
	b.WriteString("The upgrade:\n\n")
	fmt.Fprintf(&b, "- Branch: %s\n", status.Branch)
	fmt.Fprintf(&b, "- Result: %s\n", status.Phase)
	if status.Detail != "" {
		fmt.Fprintf(&b, "- What the upgrade reported: %s\n", status.Detail)
	}
	if status.StartedAt != nil {
		fmt.Fprintf(&b, "- Started: %s\n", status.StartedAt.UTC().Format(time.RFC3339))
	}
	if status.FinishedAt != nil {
		fmt.Fprintf(&b, "- Finished: %s\n", status.FinishedAt.UTC().Format(time.RFC3339))
	}
	if info := version.Get(); info.Revision != "" {
		dirty := ""
		if info.Modified {
			dirty = " (built from a dirty tree)"
		}
		fmt.Fprintf(&b, "- The build now running: %s%s\n", info.Revision, dirty)
	}
	if len(gap.Services) > 0 {
		fmt.Fprintf(&b, "\nHow long the restart left grain unreachable (%s, measured by the daemon on both sides of it):\n\n", gap.Mode)
		for _, svc := range gap.Services {
			fmt.Fprintf(&b, "- %s\n", svc.Describe())
		}
	}
	if adoption != nil {
		b.WriteString(adoption.describe())
	}
	b.WriteString("\nWhat to check:\n\n" +
		"- Has the deployment dispatched anything since the restart -- did the queue resume, " +
		"did runs start, did they finish?\n" +
		"- Is anything in the daemon's own log new since this upgrade -- panics, repeated errors, " +
		"a reconciler failing every tick? Read it with read_grain_log, starting at the window " +
		"between the upgrade finishing and now.\n" +
		"- Do the runs since the restart look like the runs before it, or is something " +
		"failing that was not failing before?\n" +
		"- Does the state repository still sync, and does the settings this deployment " +
		"is running on still say what it said before the upgrade?\n\n")
	b.WriteString("You have the self-debug capability: inspect grain's own code in the read-only " +
		"`grain-src/` checkout using standard sandbox tools, which is the code the " +
		"upgrade installed, and `grain task list`, `grain task read`, `grain task prompt` and " +
		"`grain task transcript` read this deployment's own tasks and runs through the " +
		"daemon -- which is how you answer the queue questions above, and the only way to " +
		"those rows from here. read_grain_metrics is the same record measured: ask it for a " +
		"window that ends now and one that ended before the upgrade, and \"do the runs since " +
		"the restart look like the runs before it\" is a comparison rather than an " +
		"impression. If they refuse because this run has no route back to the " +
		"daemon, say that rather than answering the question from anything else. You also have the grain-log capability: read_grain_log reads this " +
		"host's own systemd journal -- \"daemon\" for the daemon's log, \"dmesg\" for the kernel's " +
		"underneath it -- bounded by a time window, a minimum priority and a plain substring, and " +
		"redacted on the host before you see it. It reads the log itself, so it is the answer to " +
		"the log question above and the state repository is not. Mind its truncation markers: an " +
		"answer that says it was cut off is not a clean window, and a window that says nothing " +
		"matched is.")
	if stateRepo.Owner != "" {
		fmt.Fprintf(&b, " You also have read access to %s, this deployment's state repository -- "+
			"its editable configuration and memory exported as text, so tables/grain_config.json "+
			"and the settings files beside it are what this deployment is running on, and a sync "+
			"that has stopped shows as an export that is no longer being written. It holds no "+
			"tasks, runs or comments: there is no tables/task.json or tables/task_run.json, and "+
			"the task tools above are the only route to those rows.",
			stateRepo)
	}
	b.WriteString("\n\nThat is the whole of what this run can reach. If any part of what is asked " +
		"above turns out to be beyond it, say so plainly in your report -- name the question and say " +
		"you could not answer it -- rather than answering it from the nearest thing you can reach. " +
		"Evidence about a thing is not that thing: what the state repository says about a run is not " +
		"an answer to what the log says about the process that ran it. A check that reports clean " +
		"because it could not look is worse than one that reports it could not look.")
	b.WriteString("\n\nReport what you found with comment_on_issue, whether or not anything is wrong: " +
		"a clean check is the answer somebody is waiting for too. Do not repair anything you find " +
		"unless it is obviously trivial and obviously safe -- file a task for it instead, or say what " +
		"you would do and let a person decide.")
	return b.String()
}
