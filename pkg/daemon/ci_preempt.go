package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// ciPreempter is orchestrator.CIPreempter over this deployment's grain
// CI (LiveCI): what a base-branch repair the budget's reserve cannot hold
// may stop first, because a CI run on a branch that lands on a red base
// is producing a verdict nobody can use until that base is fixed
// (grain/task-488).
//
// Only a run holding a sandbox is worth stopping -- one whose jobs are
// all still queueing has nothing to give back -- and of those the
// newest-started, which has the least work to lose. It is cancelled as
// grain's own (ci.CancelBySystem), the cancellation that leaves the
// commit to be run again once the base is green, and the task that asked
// for it is told why in its own conversation.
type ciPreempter struct {
	store *model.Store
	now   func() time.Time
}

var _ orchestrator.CIPreempter = ciPreempter{}

func (p ciPreempter) PreemptBlockedCI(ctx context.Context, repo, base, except, reason string) (string, string) {
	svc := LiveCI.Load()
	if svc == nil || p.store == nil {
		return "", ""
	}
	victim, task, ok := p.victim(ctx, svc.List(), repo, base, except)
	if !ok {
		return "", ""
	}
	if !svc.Cancel(victim.ID, reason, ci.CancelBySystem) {
		return "", ""
	}
	what := fmt.Sprintf("run %s on %s", victim.ID, strings.TrimPrefix(victim.Ref, "refs/heads/"))
	if task != nil {
		what += " (task " + task.ID + ")"
		notePreemptedCI(ctx, p.store, task.ID, victim, reason, p.now())
	}
	return victim.ID, what
}

func (p ciPreempter) CIRunFinished(runID string) bool {
	svc := LiveCI.Load()
	if svc == nil {
		return true
	}
	run, ok := svc.Get(runID)
	return !ok || run.Conclusion.Done()
}

// victim is the run PreemptBlockedCI stops: live, holding at least one
// sandbox, on a branch of repo other than base and except, asked for by a
// task whose work lands on base -- and never by a console session, a chat
// or another repair.
func (p ciPreempter) victim(ctx context.Context, runs []ci.Run, repo, base, except string) (ci.Run, *model.Task, bool) {
	var best ci.Run
	var bestTask *model.Task
	var bestAt time.Time
	found := false
	for _, run := range runs {
		branch := strings.TrimPrefix(run.Ref, "refs/heads/")
		if run.Conclusion.Done() || run.Repo != repo || branch == base || branch == except || !holdsSandbox(run) {
			continue
		}
		if run.RequestedBy == "" {
			continue
		}
		task, err := p.store.GetTask(ctx, run.RequestedBy)
		if err != nil || task == nil || task.HasPriorityDispatch() || task.Interactive || model.FixesBaseBranch(*task) {
			continue
		}
		if r, b := model.TaskBaseBranch(*task); r.String() != repo || b != base {
			continue
		}
		var at time.Time
		if run.StartedAt != nil {
			at = *run.StartedAt
		}
		if !found || at.After(bestAt) {
			best, bestTask, bestAt, found = run, task, at, true
		}
	}
	return best, bestTask, found
}

// holdsSandbox reports whether any of run's jobs has a sandbox and has
// not finished.
func holdsSandbox(run ci.Run) bool {
	for _, j := range run.Jobs {
		if j.Sandbox != "" && !j.Conclusion.Done() {
			return true
		}
	}
	return false
}

// notePreemptedCI says on the task that asked for run why its CI stopped.
func notePreemptedCI(ctx context.Context, store *model.Store, taskID string, run ci.Run, reason string, now time.Time) {
	body := fmt.Sprintf("grain CI run %s was %s: its base branch is red, this deployment's sandbox budget was full, "+
		"and a verdict on top of a red base cannot be used until the base is fixed. It was cancelled by grain "+
		"rather than failed, so nothing here counts against this task, and its checks run again once the base "+
		"is green.", run.ID, reason)
	if _, err := store.AddComment(context.WithoutCancel(ctx), model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		log.Printf("grain daemon: noting on task %s that CI run %s was pre-empted: %v", taskID, run.ID, err)
	}
}
