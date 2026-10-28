package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// reconcileTargetRepos is this boundary's last look: it parks every task
// that is about to be dispatched into a repo Config.TargetRepos no
// longer names, rather than letting the same tick's dispatch clone and
// push there (grain/task-404).
//
// Everything that *writes* a repo grain would dispatch into already asks
// this list -- ui.(*Client).refuseOffAllowlist for every edit, and the
// park at filing for a new task (ui.(*Client).parkOffAllowlist). What
// none of those can speak for is the list moving underneath work that is
// already stored, and it moves: Config.TargetRepos is edited under
// Settings while tasks sit queued (ui.UpdateSettings), so a task filed
// against a repo that was on the list, a task a schedule fired, a suite
// pass and a qualification plan all reach a narrowed list already
// approved and already dispatchable. Nothing between them and the
// sandbox re-asked the question: the git proxy authorizes a sandbox
// against its live task's own Target rather than against a list of its
// own (pkg/gitproxy/authorize.go's own doc comment), which is exactly
// why a Target that reaches dispatch is a repo grain's run really will
// clone and push to.
//
// So the answer is re-read where it is used, which is the discipline
// ui.(*Client).parkOffAllowlist already documents, and re-read from the
// one rule (model.RepoAllowed) with the one vocabulary
// (model.OffAllowlistAtDispatchComment) rather than from a copy of
// either.
//
// Parking rather than refusing, for the reason a filing parks: the repo
// is the whole of what the task is for, there is nothing to undo an edit
// back to, and a park costs one reply to lift once an operator has put
// the repo back. It is also what takes the task out of task_ready, so
// this pass parks each task once and not once per tick -- and, being
// registered immediately before "dispatch", it does so on the same cycle
// that would otherwise have started the run.
//
// A deployment that restricts nothing (empty TargetRepos, that field's
// own "unrestricted" zero value, and the shape most deployments are in)
// reads nothing and does nothing here.
//
// What this deliberately does not do is stop somebody narrowing the list
// under work that is already queued, and nothing does: the save reports
// what it strands rather than refusing it (ui.Settings.TargetReposStranded,
// model.Store.StrandedByTargetRepos, grain/task-411), on the reasoning
// in ui.(*Client).RemoveTargetRepo -- which is where the one refusal
// that did stand in for it used to be. That report and this park are two
// halves of one answer rather than alternatives: the report is what an
// operator is told while they can still not do it, and this is what
// makes doing it safe -- including for the work nobody was shown,
// because a schedule, a suite pass or a plan can file a task into a repo
// minutes after the last person looked at Settings.
func reconcileTargetRepos(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	ready, err := deps.Store.ReadyTargets(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading ready tasks to check their target repos: %w", err)
	}
	if len(ready) == 0 {
		return nil
	}
	allowedRepos, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading target repos to check ready tasks against: %w", err)
	}
	var errs []error
	for _, candidate := range ready {
		// A task with no repo at all has nothing to clone and nothing to
		// push, so the list has nothing to say about it -- the same
		// reading ui.(*Client).AddComment takes of a nil Target before
		// re-asking this same question of a reply.
		if candidate.Target == nil {
			continue
		}
		if model.RepoAllowed(allowedRepos, *candidate.Target) {
			continue
		}
		if err := parkOffTargetRepos(ctx, deps, candidate.TaskID, *candidate.Target, allowedRepos, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// repoAllowedForAnyTeam reports whether repo is on at least one active
// team's target repo list, or whether no active team has a list at all.
//
// Once any team lists repositories, a team with an empty list does not
// widen the answer: only a repository somebody has added is allowed. That
// is on purpose, and it differs from model.RepoAllowed, where an empty
// list allows every repo. Base-branch CI, repair dispatch and release
// handling run once per repository rather than once per team, so letting
// one unrestricted team allow everything would bring back every repo the
// other teams' lists were written to leave out.
func repoAllowedForAnyTeam(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if model.IsConfigRepo(repo) {
		return true, nil
	}
	if store == nil {
		return true, nil
	}
	targets, err := store.TargetRepos(ctx)
	if err != nil {
		return false, err
	}
	return model.RepoAllowed(targets, repo), nil
}

// parkOffTargetRepos parks one task on grain's own comment about the
// list having narrowed under it -- the same Observation.PendingQuestionCommentID
// every other park sets (noteEmptyBranch beside it, ask_question's own
// relay, and ui's two), so it reads as awaiting_reply, leaves task_ready,
// and is lifted by the same reply.
//
// Attributed to grain itself rather than on behalf of any agent: no run
// said this, and no run has been dispatched for this task yet.
//
// A collected error rather than a stopped pass, like every other
// per-item failure in a reconciler: one task whose comment could not be
// written must not leave the rest of the ready list unexamined, and the
// next tick asks about it again.
func parkOffTargetRepos(ctx context.Context, deps Deps, taskID string,
	target model.RepoRef, allowedRepos []string, now time.Time) error {

	log.Printf("orchestrator: %s targets %s, which is no longer on this deployment's "+
		"target repo list; parking it rather than dispatching a run into it", taskID, target)

	commentID, err := deps.Store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      model.OffAllowlistAtDispatchComment(target, allowedRepos),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: parking %s off the target repo list: %w", taskID, err)
	}
	return observeField(ctx, deps.Store, taskID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}
