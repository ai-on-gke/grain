package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// pullRequestOpener is ui.Config.PullRequests over
// orchestrator.OpenPullRequestForTask: the one place this deployment's
// store and its GitHub client are both in scope for a request that
// arrived over the UI/API. Its caller is a dispatched run's own mcpserver
// (pkg/cli/mcpserver.go's daemonPullRequests), which holds no GitHub
// credential itself.
//
// It converts orchestrator.PullRequestStatus into ui.PullRequestStatus
// field for field, for the same reason sandboxHealthAdapter converts
// orchestrator.SandboxHealth: pkg/ui does not import pkg/orchestrator,
// and this file is where both types are in scope.
type PullRequestOpener struct {
	Store  *model.Store
	Client github.Client
}

func (o *PullRequestOpener) OpenForTask(ctx context.Context, taskID string) (ui.PullRequestStatus, error) {
	task, err := o.Store.GetTask(ctx, taskID)
	if err != nil {
		return ui.PullRequestStatus{}, err
	}
	if task == nil {
		return ui.PullRequestStatus{}, fmt.Errorf("no task %s", taskID)
	}
	status, err := orchestrator.OpenPullRequestForTask(ctx, o.Store, o.Client, *task)
	if err != nil {
		return ui.PullRequestStatus{}, err
	}
	out := ui.PullRequestStatus{
		Number:          status.PullRequest.Number,
		URL:             status.PullRequest.HTMLURL,
		ChecksAvailable: status.ChecksKnown,
		ChecksError:     status.ChecksError,
		Note:            status.Note,
	}
	if task.Target != nil {
		out.Repo = task.Target.String()
	}
	for _, c := range status.Checks {
		check := ui.CheckStatus{Name: c.Name, Status: c.Status}
		if c.Conclusion != nil {
			check.Conclusion = *c.Conclusion
		}
		out.Checks = append(out.Checks, check)
	}
	return out, nil
}

// ReattachForTask is ui.PullRequestReattacher over
// orchestrator.ReattachPullRequest: what reopening a task does with the
// pull request still open from its branch.
func (o *PullRequestOpener) ReattachForTask(ctx context.Context, taskID string) error {
	task, err := o.Store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return err
	}
	_, err = orchestrator.ReattachPullRequest(ctx, o.Store, o.Client, *task, true, time.Now().UTC())
	return err
}

// sandboxRecreateAdapter is ui.Config.SandboxRecreate over
// orchestrator.SandboxRecreations, converting the result field for field
// for the reason pullRequestOpener above converts its own: pkg/ui does
// not import pkg/orchestrator, and this file is where both types are in
// scope.
//
// Unlike pullRequestGate below it needs no gate. The registry it wraps
// is allocated at process start (sandboxRecreations' own doc comment)
// and is simply empty until runs register themselves in it, so a request
// that arrives before -- or after -- the reconcile loop is running gets
// the honest "no live run on this daemon" rather than a nil dereference
// or a made-up failure.
type sandboxRecreateAdapter struct {
	recreations *orchestrator.SandboxRecreations
}

func (a sandboxRecreateAdapter) RecreateForTask(ctx context.Context, taskID string) (ui.SandboxRecreation, error) {
	recreation, err := a.recreations.Recreate(ctx, taskID)
	if err != nil {
		return ui.SandboxRecreation{}, err
	}
	return ui.SandboxRecreation{
		Sandbox:     recreation.Sandbox,
		CheckoutDir: recreation.CheckoutDir,
		Restored:    recreation.Restored,
		Warnings:    recreation.Warnings,
	}, nil
}

// waitCreditAdapter is ui.Config.WaitCredit over
// orchestrator.RunWaitCredits, converting the result field for field for
// sandboxRecreateAdapter's reason: pkg/ui does not import
// pkg/orchestrator, and this file is where both types are in scope.
//
// It needs no gate either, and for the same reason that one does not:
// the registry is allocated at process start and is simply empty until
// runs register themselves in it, so a report that arrives before -- or
// after -- a run is live gets the honest "no live run on this daemon".
type waitCreditAdapter struct {
	credits *orchestrator.RunWaitCredits
}

func (a waitCreditAdapter) CreditWaitForTask(ctx context.Context, taskID string, blocked time.Duration) (ui.WaitCredit, error) {
	credit, err := a.credits.Credit(ctx, taskID, blocked)
	if err != nil {
		return ui.WaitCredit{}, err
	}
	out := ui.WaitCredit{
		BlockedMS:  credit.Blocked.Milliseconds(),
		CreditedMS: credit.Credited.Milliseconds(),
		Live:       credit.Live,
	}
	if credit.Live {
		deadline := credit.Deadline.UTC()
		out.Deadline = &deadline
	}
	return out, nil
}

// taskCancellerAdapter is ui.Config.TaskCanceller over
// orchestrator.TaskCancellers.
type taskCancellerAdapter struct {
	cancellers *orchestrator.TaskCancellers
}

func (a taskCancellerAdapter) CancelTask(ctx context.Context, taskID string) bool {
	if a.cancellers == nil {
		return false
	}
	return a.cancellers.Cancel(taskID, orchestrator.ErrTurnStopped)
}

// Comment is ui.Config.PullRequestComments over the same client: what
// closing a task says on the pull request that close has orphaned (see
// model.OrphanedPullRequestNote). Here rather than on a type of its own
// because it needs exactly what OpenForTask already holds, and the same
// gate below already answers "has this daemon got a GitHub client yet?"
// for both.
//
// ref arrives as a task_link target ("owner/name#123") because that is
// the only spelling pkg/ui has for a pull request; parsing it back is
// this side's job for the same reason converting the status shape is.
func (o *PullRequestOpener) Comment(ctx context.Context, ref, body string) error {
	pr, err := model.ParsePullRequestRef(ref)
	if err != nil {
		return err
	}
	_, err = o.Client.CreateComment(pr.Repo.Owner, pr.Repo.Name, pr.Number, body)
	return err
}

// Close is ui.Config.PullRequestCloser over the same client, and the one
// place in this daemon where a pull request is closed rather than merged
// -- reached only when whoever closed the task asked for it in the same
// request (ui.CloseOptions.ClosePullRequest). It parses ref back the way
// Comment above does, for the same reason.
//
// It closes the pull request and touches nothing else: the branch and
// every commit on it stay where they are, and reopening the pull request
// on GitHub restores it whole.
func (o *PullRequestOpener) Close(ctx context.Context, ref string) error {
	pr, err := model.ParsePullRequestRef(ref)
	if err != nil {
		return err
	}
	return o.Client.ClosePullRequest(pr.Repo.Owner, pr.Repo.Name, pr.Number)
}

// pullRequestGate is the ui.Config.PullRequests the UI/API server is
// given: whatever livePullRequests holds by the time a request actually
// arrives, or a plain refusal until runDaemon has put one there.
type PullRequestGate struct{}

func (PullRequestGate) OpenForTask(ctx context.Context, taskID string) (ui.PullRequestStatus, error) {
	opener := LivePullRequests.Load()
	if opener == nil {
		return ui.PullRequestStatus{}, errors.New(
			"this daemon has no GitHub client yet, so it cannot open a pull request: " +
				"its reconcile loop has not started (or has failed -- check the daemon log)")
	}
	return opener.OpenForTask(ctx, taskID)
}

// ReattachForTask is the same gate for ui.PullRequestReattacher.
func (PullRequestGate) ReattachForTask(ctx context.Context, taskID string) error {
	opener := LivePullRequests.Load()
	if opener == nil {
		return errors.New("this daemon has no GitHub client yet, so it cannot look for the pull request")
	}
	return opener.ReattachForTask(ctx, taskID)
}

// Comment is the same gate for ui.Config.PullRequestComments. Its refusal
// is not thrown away: it ends up quoted in the note grain leaves on the
// task, which is exactly where somebody wondering why the pull request
// was never told should read it.
func (PullRequestGate) Comment(ctx context.Context, ref, body string) error {
	opener := LivePullRequests.Load()
	if opener == nil {
		return errors.New(
			"this daemon has no GitHub client yet: " +
				"its reconcile loop has not started (or has failed -- check the daemon log)")
	}
	return opener.Comment(ctx, ref, body)
}

// Close is the same gate for ui.Config.PullRequestCloser. Refusing is the
// right answer rather than a shame here: a daemon with no GitHub client
// cannot close a pull request, and the alternative to saying so is a
// close that silently left one open. The refusal ends up quoted in the
// note on the task, where whoever ticked the box will read it.
func (PullRequestGate) Close(ctx context.Context, ref string) error {
	opener := LivePullRequests.Load()
	if opener == nil {
		return errors.New(
			"this daemon has no GitHub client yet: " +
				"its reconcile loop has not started (or has failed -- check the daemon log)")
	}
	return opener.Close(ctx, ref)
}
