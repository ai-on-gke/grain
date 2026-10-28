package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// errPreempted is what context.Cause(runCtx) reads once a run has been
// stopped to make room for a base-branch repair (grain/task-488). It is
// finished the way a daemon restart finishes a run -- model.InterruptedOutcome,
// no strike against the task's streak, the conversation kept for the
// next dispatch -- since nothing about the task caused it either.
var errPreempted = errors.New("orchestrator: run pre-empted to make room for a base-branch repair")

// PreemptedDetail is the task_run.detail a pre-empted run records.
const PreemptedDetail = "pre-empted to make room for a base-branch repair on a full sandbox budget; " +
	"re-queued without a strike"

// CIPreempter is what the deployment's grain CI offers a repair that
// needs room: stopping a run whose verdict is moot while the base is red.
//
// PreemptBlockedCI cancels at most one live run, on a branch of repo
// whose work lands on base -- never base itself, nor except's own branch
// (the repair's, whose CI is the confirmation the repair is waiting on)
// -- and returns its id and a sentence naming it, or "" when there is no
// such run holding a sandbox. CIRunFinished reports whether that run
// has finished winding down, which is when its sandboxes are back.
type CIPreempter interface {
	PreemptBlockedCI(ctx context.Context, repo, base, except, reason string) (runID, what string)
	CIRunFinished(runID string) bool
}

// preemptSettle is the longest a pre-emption is waited on before another
// is allowed for the same base: a victim that has not given its sandbox
// back by then is not going to, and a repair should not wait on it.
const preemptSettle = 5 * time.Minute

// Preemptions is this process's record of the pre-emptions it has made,
// one per base at most: the victim each base's repair is waiting on to
// give its sandbox back. Shared across cycles for that reason (Config).
//
// The zero value is not usable; NewPreemptions builds one. A nil
// *Preemptions pre-empts nothing.
type Preemptions struct {
	mu      sync.Mutex
	pending map[string]preemption
}

type preemption struct {
	// ciRun or runID is the victim, whichever kind it was.
	ciRun string
	runID string
	what  string
	at    time.Time
}

// NewPreemptions returns an empty record.
func NewPreemptions() *Preemptions {
	return &Preemptions{pending: map[string]preemption{}}
}

// preempter is the dispatch.Preempter a cycle installs: CI first, since a
// queued or running job on a branch blocked by the red base produces a
// verdict nobody can use until the base is fixed, then the newest-started
// agent run that is itself waiting on that base. Never a console session
// or a chat (Interactive), and never another repair.
func preempter(deps Deps, now time.Time) dispatch.Preempter {
	p := deps.Config.Preemptions
	return func(ctx context.Context, repair model.Task, base string) (string, bool) {
		if p == nil {
			return "", false
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if prev, ok := p.pending[base]; ok {
			if !p.settled(ctx, deps, prev, now) {
				return prev.what, false
			}
			delete(p.pending, base)
		}
		repo, branch := model.TaskBaseBranch(repair)
		reason := fmt.Sprintf("pre-empted to make room for task %s, the repair of %s's %s", repair.ID, repo, branch)
		if deps.Config.CIPreempter != nil {
			if id, what := deps.Config.CIPreempter.PreemptBlockedCI(ctx, repo.String(), branch, repair.BranchName(), reason); id != "" {
				what = "pre-empted grain CI " + what
				p.pending[base] = preemption{ciRun: id, what: what, at: now}
				log.Printf("orchestrator: %s, for base-branch repair %s", what, repair.ID)
				return what, true
			}
		}
		victim, ok := agentVictim(ctx, deps, base)
		if !ok {
			return "", false
		}
		if !deps.Config.TaskCancellers.Cancel(victim.TaskID, errPreempted) {
			return "", false
		}
		what := fmt.Sprintf("pre-empted run %s of task %s", victim.ID, victim.TaskID)
		p.pending[base] = preemption{runID: victim.ID, what: what, at: now}
		log.Printf("orchestrator: %s, for base-branch repair %s", what, repair.ID)
		notePreempted(ctx, deps.Store, victim.TaskID, victim.ID, repair, now)
		return what, true
	}
}

// settled reports whether prev's victim has given its sandbox back, or
// has had preemptSettle to and is no longer worth waiting on.
func (p *Preemptions) settled(ctx context.Context, deps Deps, prev preemption, now time.Time) bool {
	if now.Sub(prev.at) >= preemptSettle {
		return true
	}
	if prev.ciRun != "" {
		return deps.Config.CIPreempter == nil || deps.Config.CIPreempter.CIRunFinished(prev.ciRun)
	}
	runs, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		return false
	}
	return !slices.ContainsFunc(runs, func(r model.Run) bool { return r.ID == prev.runID })
}

// agentVictim is the run a repair of base may stop: the newest-started
// live run whose task lands on the same base branch, preferring one grain
// itself linked to wait on the base's repair (model.BaseRepairWaitTag),
// and passing over console sessions, chats and every other repair.
func agentVictim(ctx context.Context, deps Deps, base string) (model.Run, bool) {
	if deps.Store == nil || deps.Config.TaskCancellers == nil {
		return model.Run{}, false
	}
	runs, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		log.Printf("orchestrator: reading live runs for a pre-emption: %v", err)
		return model.Run{}, false
	}
	var best model.Run
	var bestLinked, found bool
	for _, r := range runs {
		task, err := deps.Store.GetTask(ctx, r.TaskID)
		if err != nil || task == nil || !preemptible(*task, base) {
			continue
		}
		linked := slices.ContainsFunc(task.Tags, func(t string) bool {
			return strings.HasPrefix(t, model.BaseRepairWaitTag(""))
		})
		better := !found || (linked && !bestLinked) || (linked == bestLinked && r.StartedAt.After(best.StartedAt))
		if better {
			best, bestLinked, found = r, linked, true
		}
	}
	return best, found
}

// preemptible reports whether task's run may be stopped for a repair of
// base: its work lands on that same branch, so it is waiting on the
// repair whatever it is doing, and it is neither a session a person is
// talking to nor a repair itself.
func preemptible(task model.Task, base string) bool {
	if task.HasPriorityDispatch() || task.Interactive || model.FixesBaseBranch(task) {
		return false
	}
	repo, branch := model.TaskBaseBranch(task)
	if branch == "" {
		return false
	}
	return model.BaseRepairBranchTag(repo, branch) == base
}

// notePreempted tells a task, in its own conversation, why its run ended
// half way -- the same promise noteInterrupted makes for a daemon
// restart, with the repair that needed the room named.
func notePreempted(ctx context.Context, store *model.Store, taskID, runID string, repair model.Task, now time.Time) {
	repo, branch := model.TaskBaseBranch(repair)
	body := fmt.Sprintf(
		"Run %s was pre-empted: %s's %s is red, this deployment's sandbox budget was full, and its sandbox "+
			"was needed for [task %s](/tasks/%s), the repair this task is waiting on anyway. Nothing about this "+
			"task caused it, so it does not count against the task's failure streak, and the task has been "+
			"queued again. The next run picks the conversation back up, in a fresh sandbox: anything the "+
			"pre-empted run wrote and did not push is gone.", runID, repo, branch, repair.ID, repair.ID)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		log.Printf("orchestrator: run %s: noting on task %s that it was pre-empted: %v", runID, taskID, err)
	}
}
