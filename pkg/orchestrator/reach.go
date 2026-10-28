package orchestrator

// The filing-time reach check (gitproxy.Reach.Refusal, grain/task-1850)
// for the tasks this package files itself, none of which pass through
// ui.Client.CreateTask: the end-of-run replay of propose_task, a
// schedule's firing and a suite run's passes (grain/task-1857). All
// three are automation with nobody at a form to refuse, so a task its
// team cannot reach is filed and parked on gitproxy.ReachParkComment --
// the same sentence CreateTask parks an automation-filed task on --
// rather than dispatched into a clone that stopOnAccessRefused would
// only park a sandbox later.

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// parkUnreached parks each of ids whose team's GitHub credential cannot
// reach a repository it names. Nothing is asked with no reach (a nil
// Reach answers "unknown", which never parks), and a task already parked
// -- off the target repo list, say -- keeps what it was parked on.
func parkUnreached(ctx context.Context, store *model.Store, reach *gitproxy.Reach, ids []string,
	now time.Time) error {

	if reach == nil {
		return nil
	}
	for _, id := range ids {
		task, err := store.GetTask(ctx, id)
		if err != nil {
			return fmt.Errorf("orchestrator: reading %s to check its team's reach: %w", id, err)
		}
		if task == nil {
			continue
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			return fmt.Errorf("orchestrator: reading %s to check its team's reach: %w", id, err)
		}
		if obs != nil && obs.PendingQuestionCommentID != nil {
			continue
		}
		refusal, err := reach.TaskRefusal(ctx, store, *task)
		if err != nil {
			return fmt.Errorf("orchestrator: checking whether %s's team reaches its repositories: %w", id, err)
		}
		if refusal == "" {
			continue
		}
		commentID, err := store.AddComment(ctx, model.Comment{
			TaskID:    id,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Body:      gitproxy.ReachParkComment(refusal),
			CreatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("orchestrator: parking %s, which its team cannot reach: %w", id, err)
		}
		if err := observeField(ctx, store, id, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = &commentID
			o.PendingSecret = ""
		}); err != nil {
			return err
		}
	}
	return nil
}

// suiteRunTaskIDs is the tasks run filed for pass.
func suiteRunTaskIDs(run model.SuiteRun, pass int) []string {
	var ids []string
	for _, t := range run.PassTasks(pass) {
		ids = append(ids, t.TaskID)
	}
	return ids
}
