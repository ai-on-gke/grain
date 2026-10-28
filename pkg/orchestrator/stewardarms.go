package orchestrator

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
)

// The pipeline steward's mechanical duties, moved into reconcilers
// (docs/design/durable-tasks.md#DT-6, "Mechanical duties move into
// reconcilers"). What the steward did by hand each wake is done here every
// cycle, so that what is left for the steward is judgement:
//
//   - retriggerKnownFlake: a task's grain CI failing on a known flaky job
//     alone is re-run once per fingerprint (commit and job).
//   - operatorCIHold: CI a person cancelled under a time-bound hold is
//     left alone while the hold lasts, and re-run once it has expired
//     (reconcileTaskCI's done arm, ciReplacementFor).
//   - reconcileTaskStallNotifications: a task whose state has outlived
//     its patience (TaskStallNote) raises pipeline.task-stalled:<task>,
//     the task-side twin of pipeline.merge-stalled.
//   - reconcilePortStaleNotifications: a port making no progress raises
//     pipeline.port-stale:<task>.
//
// reconcileJudgementModel sits beside them: it tells somebody when the
// steward's judgement model resolves to nothing.

// Notification families the arms above raise.
const (
	// FamilyTaskStalled is a task whose state has outlived its patience,
	// "<task>".
	FamilyTaskStalled = "pipeline.task-stalled"
	// FamilyPortStale is a port that has made no progress past
	// portStalePatience, "<task>".
	FamilyPortStale = "pipeline.port-stale"
)

// portStalePatience is how long a port may sit in one state before it is
// stale: a day, since a port is a small mechanical change that is
// normally merged within hours of the change it ports.
const portStalePatience = 24 * time.Hour

// stewardArmsNotifier is who raises and clears the families above.
var stewardArmsNotifier = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "pipeline-reconciler"})

// --- known flakes ------------------------------------------------------

// flakeRetriggerMarker is the line a known-flake retrigger leaves on its
// task, and what makes it once per fingerprint: a later cycle that finds
// the marker for this commit and job does not retrigger it again.
func flakeRetriggerMarker(commit, job string) string {
	return fmt.Sprintf("<!-- grain:flake-retrigger %s %s -->", commit, job)
}

// knownFlakyJob reports whether job (ciJobName's spelling) has flaked
// before in a run of repo this daemon still holds: failed and then passed
// an uncached recheck of the same tree (ci.JobRun.Flake, recordFlake).
// Only repo's own runs count: a job of the same name flaking in another
// repository says nothing about this one, and must not buy it a re-run.
func knownFlakyJob(deps Deps, repo, job string) bool {
	for _, run := range deps.CI.List() {
		if run.Repo != repo {
			continue
		}
		for _, j := range run.Jobs {
			if j.Flake && ciJobName(j) == job {
				return true
			}
		}
	}
	return false
}

// retriggerKnownFlake re-runs, once, a task's failing grain CI verdict
// whose only failing job is a known flaky one (knownFlakyJob). The
// automatic recheck (startCIRecheck) has already sampled the failure
// twice by the time this asks; a job with a record of flaking that fails
// alone is the "obvious flake" the steward used to retrigger by hand.
// Once per fingerprint -- commit and job -- so a job that really is
// broken on this commit costs one extra run and no more. It reports
// whether it started one.
func retriggerKnownFlake(ctx context.Context, deps Deps, task model.Task, status model.CIStatus,
	now time.Time) (bool, error) {

	if status.Conclusion != string(ci.ConclusionFailure) || task.Target == nil || status.Commit == "" {
		return false, nil
	}
	jobs := failedJobsOf(status.Detail)
	if len(jobs) != 1 {
		return false, nil
	}
	job := jobs[0]
	if flakeRecheckRefusal(task) != "" || !knownFlakyJob(deps, task.Target.String(), job) {
		return false, nil
	}
	marker := flakeRetriggerMarker(status.Commit, job)
	comments, err := deps.Store.Comments(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's comments: %w", task.ID, err)
	}
	if slices.ContainsFunc(comments, func(c model.Comment) bool { return strings.Contains(c.Body, marker) }) {
		return false, nil
	}
	run, ok := deps.CI.Get(status.RunID)
	if !ok {
		return false, nil
	}
	var planID string
	for _, j := range run.Jobs {
		if ciJobName(j) != job || j.JobID == "" {
			continue
		}
		planID = j.JobID
		if j.WorkflowPath != "" {
			planID = j.WorkflowPath + ":" + j.JobID
		}
	}
	if planID == "" {
		return false, nil
	}
	recheck, err := deps.CI.Start(ctx, ci.Request{
		Repo: task.Target.String(), Ref: task.BranchName(), Commit: status.Commit,
		Event: ci.EventPush, RequestedBy: task.ID,
		Jobs: []string{planID}, Recheck: true,
	})
	if err != nil {
		log.Printf("orchestrator: task %s: retriggering known flaky job %s at %s: %v",
			task.ID, job, shortSHA(status.Commit), err)
		return false, nil
	}
	started := false
	if err := observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != status.RunID || !o.CI.Done() {
			return
		}
		o.CI = &model.CIStatus{
			RunID: recheck.ID, Commit: status.Commit, StartedAt: status.StartedAt,
			Attempts: status.Attempts, PreviousConclusion: status.PreviousConclusion,
			PreviousDetail: status.PreviousDetail, RecheckOf: status.RunID, RecheckDetail: status.Detail,
		}
		started = true
	}); err != nil {
		return false, err
	}
	if !started {
		deps.CI.Cancel(recheck.ID, fmt.Sprintf("task %s's verdict moved on before the retrigger was recorded", task.ID),
			ci.CancelBySystem)
		return false, nil
	}
	log.Printf("orchestrator: task %s: %s failed alone at %s and is a known flaky job; re-running it once (run %s)",
		task.ID, job, shortSHA(status.Commit), recheck.ID)
	return true, queueComment(ctx, deps.Store, task.ID, fmt.Sprintf(
		"grain: `%s` failed alone at `%s`, and it is a job that has flaked before (it failed and then "+
			"passed an uncached recheck of the same tree). Re-running it once more, uncached (run %s); "+
			"this commit and job are not retriggered again.\n\n%s",
		job, shortSHA(status.Commit), recheck.ID, marker), now)
}

// --- time-bound holds on cancelled CI ----------------------------------

// operatorCIHold reports whether a cancelled verdict a person asked for
// (CancelledBy operator) is under a time-bound hold: the task is deferred
// until a time that has not come. While it lasts no replacement is
// started -- ciReplacementFor would otherwise replace a person's
// cancellation after ciRetryDelay, over the top of their hold. Once it
// expires (reconcileWake clears the deferral) the cancellation is
// replaced like any other, within ciMaximumAttempts, which is the re-run
// the steward used to start by hand.
func operatorCIHold(task model.Task, status model.CIStatus, now time.Time) bool {
	return status.Conclusion == string(ci.ConclusionCancelled) &&
		status.CancelledBy == string(ci.CancelByOperator) &&
		task.DeferredAt != nil && task.DeferredUntil != nil && now.Before(*task.DeferredUntil)
}

// --- task stalls and stale ports ---------------------------------------

// taskStallScan is what both stall reconcilers read: every open task's
// state and how long it has been in it, less the paused ones (a person
// paused them; they are not stalled) and the pipeline steward's own.
type taskStallScan struct {
	states map[string]model.State
	times  map[string]model.StateTimes
	paused map[string]model.TaskPause
	own    []string
}

func readTaskStallScan(ctx context.Context, store *model.Store) (taskStallScan, error) {
	var scan taskStallScan
	var err error
	if scan.states, err = store.States(ctx); err != nil {
		return scan, fmt.Errorf("reading task states: %w", err)
	}
	if scan.times, err = store.StateTimes(ctx); err != nil {
		return scan, fmt.Errorf("reading task state times: %w", err)
	}
	if scan.paused, err = store.PausedSet(ctx); err != nil {
		return scan, fmt.Errorf("reading paused tasks: %w", err)
	}
	if scan.own, err = store.OpenTasksWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID)); err != nil {
		return scan, fmt.Errorf("reading the pipeline steward's own task: %w", err)
	}
	return scan, nil
}

// since is how long id has been in its state, or nil where the record
// cannot say, or the task is closed, paused or the steward's own.
func (scan taskStallScan) since(id string) (model.State, *time.Time) {
	state, ok := scan.states[id]
	if !ok || state == model.StateClosed || slices.Contains(scan.own, id) {
		return state, nil
	}
	if _, paused := scan.paused[id]; paused {
		return state, nil
	}
	return state, scan.times[id].Since(state)
}

// reconcileTaskStallNotifications raises pipeline.task-stalled:<task> for
// each task whose state has outlived its patience (TaskStallNote: the same
// classification list_grain_tasks and the inbox's stalled group read), and
// clears it once the task moves on. Ports are left to
// reconcilePortStaleNotifications, which has a patience of its own. It is
// the task-side escalation the merge queue's pipeline.merge-stalled is
// for queue entries (task 718's follow-on).
func reconcileTaskStallNotifications(ctx context.Context, deps Deps, scan taskStallScan, ports []string,
	now time.Time) error {

	store := deps.Store
	stalled := map[string]bool{}
	ids := make([]string, 0, len(scan.states))
	for id := range scan.states {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if slices.Contains(ports, id) {
			continue
		}
		state, since := scan.since(id)
		if since == nil {
			continue
		}
		note := TaskStallNote(state, *since, now)
		if note == "" {
			continue
		}
		stalled[id] = true
		if err := ensurePipelineNotification(ctx, store, model.Notification{
			Subject:  model.NotificationSubject(FamilyTaskStalled, id),
			Family:   FamilyTaskStalled,
			Severity: model.NotificationWarning,
			Title:    fmt.Sprintf("Task %s is stalled in %s", id, state),
			Body:     fmt.Sprintf("[Task #%s](/tasks/%s): %s", id, id, note),
			TaskID:   id,
			RaisedBy: stewardArmsNotifier,
			Actions:  []string{"open-task", "investigate"},
			ClearsWhen: &model.NotificationClear{
				Kind: model.ClearsOnTask, Task: id, Until: "closed",
			},
		}, now); err != nil {
			return err
		}
	}
	return clearFamilyExcept(ctx, store, FamilyTaskStalled, stalled)
}

// reconcilePortStaleNotifications raises pipeline.port-stale:<task> for
// each open port (model.LinkPortOf) that has sat in one state past
// portStalePatience, or past its state's own patience, and clears it once
// the port moves on. Ports are filed automatically (filePorts); this is
// the nudge the steward used to give them by hand, made a notification a
// person can act on.
func reconcilePortStaleNotifications(ctx context.Context, deps Deps, scan taskStallScan, ports []string,
	now time.Time) error {

	store := deps.Store
	stale := map[string]bool{}
	for _, id := range ports {
		state, since := scan.since(id)
		if since == nil {
			continue
		}
		if now.Sub(*since) < portStalePatience && TaskStallNote(state, *since, now) == "" {
			continue
		}
		stale[id] = true
		if err := ensurePipelineNotification(ctx, store, model.Notification{
			Subject:  model.NotificationSubject(FamilyPortStale, id),
			Family:   FamilyPortStale,
			Severity: model.NotificationWarning,
			Title:    fmt.Sprintf("Port task %s has made no progress since %s", id, since.UTC().Format(time.RFC3339)),
			Body: fmt.Sprintf("[Task #%s](/tasks/%s) is a port and has been %s since %s.",
				id, id, state, since.UTC().Format(time.RFC3339)),
			TaskID:   id,
			RaisedBy: stewardArmsNotifier,
			Actions:  []string{"open-task", "investigate"},
			ClearsWhen: &model.NotificationClear{
				Kind: model.ClearsOnTask, Task: id, Until: "closed",
			},
		}, now); err != nil {
			return err
		}
	}
	return clearFamilyExcept(ctx, store, FamilyPortStale, stale)
}

// clearFamilyExcept clears every open notification of family whose task
// is not in keep: the condition that raised it no longer holds.
func clearFamilyExcept(ctx context.Context, store *model.Store, family string, keep map[string]bool) error {
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Family: family, Limit: 1000})
	if err != nil {
		return err
	}
	for _, n := range open {
		_, key, _ := strings.Cut(n.Subject, ":")
		if keep[key] {
			continue
		}
		if err := clearPipelineNotification(ctx, store, n.Subject, stewardArmsNotifier); err != nil {
			return err
		}
	}
	return nil
}

// reconcileStallNotifications is the two stall reconcilers above, on one
// read of the task states.
func reconcileStallNotifications(ctx context.Context, deps Deps, now time.Time) error {
	scan, err := readTaskStallScan(ctx, deps.Store)
	if err != nil {
		return err
	}
	ports, err := deps.Store.OpenTasksWithLink(ctx, model.LinkPortOf)
	if err != nil {
		return err
	}
	if err := reconcileTaskStallNotifications(ctx, deps, scan, ports, now); err != nil {
		return err
	}
	return reconcilePortStaleNotifications(ctx, deps, scan, ports, now)
}

// --- the judgement model -----------------------------------------------

// reconcileJudgementModel raises durable.no-judgement-model while the
// pipeline steward asks for the judgement model (model.JudgementModelName,
// through its template or its schedule) and nothing resolves it
// (model.ResolveJudgementModel): its runs then go to the rotation, which
// is what DT-11 exists to avoid, so somebody is told. It clears once a
// connection offers a ranked model, or the deployment names one. A
// deployment none of whose connections offers any model is not told:
// nothing runs there at all.
func reconcileJudgementModel(ctx context.Context, deps Deps, now time.Time) error {
	store := deps.Store
	asks := false
	for _, id := range []string{model.PipelineStewardTemplateID, model.StewardAuditTemplateID} {
		sched, err := store.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		tmpl := model.SystemTemplate(id, deps.Config.settingsRepo())
		if sched != nil && sched.Enabled && tmpl != nil &&
			(model.IsJudgementModel(sched.Model) || (strings.TrimSpace(sched.Model) == "" && model.IsJudgementModel(tmpl.Model))) {
			asks = true
			break
		}
	}
	if asks {
		cfg, err := store.GetConfig(ctx)
		if err != nil {
			return err
		}
		// A deployment with no connection offering any model runs
		// nothing at all, and has bigger news than this to hear.
		offers := false
		if cfg != nil {
			for _, b := range cfg.AgentBackends {
				if !b.Disabled && b.HasModels() {
					offers = true
				}
			}
		}
		if !offers {
			asks = false
		} else if _, ok := model.ResolveJudgementModel(*cfg, cfg.AgentBackends); ok {
			asks = false
		}
	}
	if !asks {
		return clearPipelineNotification(ctx, store, model.JudgementModelUnresolvedSubject, stewardArmsNotifier)
	}
	return ensurePipelineNotification(ctx, store, model.Notification{
		Subject: model.JudgementModelUnresolvedSubject,
		Family:  model.JudgementModelUnresolvedSubject,
		// Info: the steward still runs, on the rotation. It is listed
		// with the deployment's notifications rather than pushed into
		// the director's conversation.
		Severity: model.NotificationInfo,
		Title:    "The pipeline steward has no judgement model and runs on the rotation",
		Body: "The pipeline steward asks for the `judgement` model, and nothing resolves it: the " +
			"deployment names no judgement model, and none of its connections offers a model on " +
			"grain's ranking (" + strings.Join(model.JudgementModelRanking, ", ") + "). Its wakes run " +
			"on the default rotation until one does. Set the judgement model, or add a connection " +
			"serving one of those models.",
		RaisedBy: stewardArmsNotifier,
	}, now)
}
