package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/namespace"
)

// SandboxCLIActions is gitproxy.CLIActions over this Client: what the
// sandbox proxy's acting /cli/ routes (grain ci wait / run-job /
// retrigger, grain pr open / reply / resolve, grain git revert's
// revert-info) do, done the way the REST routes the MCP tools reach
// already do it -- StartTaskCIRunOpt for CI, OpenPullRequest for the pull
// request -- for the task the proxy authenticated, and no other.
type SandboxCLIActions struct {
	Client *Client
}

var _ gitproxy.CLIActions = SandboxCLIActions{}

// PullRequestReviewActions is what grain pr reply and grain pr resolve
// need on top of Config.PullRequests: answering and resolving review
// threads on a task's own open pull request. The daemon's pull request
// opener implements it; a Config.PullRequests without it refuses both.
type PullRequestReviewActions interface {
	ReplyForTask(ctx context.Context, taskID string, commentID int, body string) (text string, refused bool, err error)
	ResolveForTask(ctx context.Context, taskID, threadID string, commentID int) (text string, refused bool, err error)
}

// errNoReviewActions is a deployment whose pull request opener cannot
// write review comments.
var errNoReviewActions = errors.New("this deployment cannot answer or resolve review comments from the sandbox: use the reply_to_pull_request_comment and resolve_review_thread tools")

// cliActionError translates this package's error types into the namespace's, so
// the proxy answers them with the status they mean.
func cliActionError(err error) error {
	var validation *ValidationError
	var forbidden *ForbiddenError
	var notFound *NotFoundError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &validation):
		return fmt.Errorf("%w: %v", namespace.ErrBadRequest, err)
	case errors.As(err, &forbidden):
		return fmt.Errorf("%w: %v", namespace.ErrForbidden, err)
	case errors.As(err, &notFound):
		return fmt.Errorf("%w: %v", namespace.ErrNotFound, err)
	}
	return err
}

func (a SandboxCLIActions) StartTaskCI(ctx context.Context, taskID string, req gitproxy.CIStartRequest) (ci.Run, error) {
	run, err := a.Client.StartTaskCIRunOpt(ctx, taskID, TaskCIRunOptions{
		Jobs:       req.Jobs,
		Workflow:   req.Workflow,
		Commit:     req.Commit,
		Recheck:    req.Recheck,
		FailedOnly: req.FailedOnly,
	})
	return run, cliActionError(err)
}

func (a SandboxCLIActions) CIRun(_ context.Context, id string) (ci.Run, bool) {
	if a.Client.Config.CI == nil {
		return ci.Run{}, false
	}
	return a.Client.Config.CI.Get(id)
}

func (a SandboxCLIActions) OpenPullRequest(ctx context.Context, taskID string) (gitproxy.CLIActionResult, error) {
	status, err := a.Client.OpenPullRequest(ctx, taskID)
	var landed interface{ NothingToPropose() bool }
	if err != nil && errors.As(err, &landed) && landed.NothingToPropose() {
		return gitproxy.CLIActionResult{Text: err.Error(), Refused: true}, nil
	}
	if err != nil {
		return gitproxy.CLIActionResult{}, cliActionError(err)
	}
	return gitproxy.CLIActionResult{Text: renderPullRequestStatus(status)}, nil
}

// renderPullRequestStatus is open_pull_request's answer in plain text.
func renderPullRequestStatus(status PullRequestStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%d in %s: %s\n", status.Number, status.Repo, status.URL)
	if status.Note != "" {
		fmt.Fprintln(&b, status.Note)
	}
	switch {
	case status.ChecksError != "":
		fmt.Fprintf(&b, "Its checks could not be read: %s\n", status.ChecksError)
	case !status.ChecksAvailable:
		fmt.Fprintln(&b, "This deployment's credential cannot read GitHub checks; grain ci wait reads grain CI's verdict.")
	case len(status.Checks) == 0:
		fmt.Fprintln(&b, "No checks have reported against it yet.")
	default:
		for _, c := range status.Checks {
			verdict := c.Conclusion
			if verdict == "" {
				verdict = c.Status
			}
			fmt.Fprintf(&b, "  %s: %s\n", c.Name, verdict)
		}
	}
	return b.String()
}

func (a SandboxCLIActions) reviewActions() (PullRequestReviewActions, bool) {
	r, ok := a.Client.Config.PullRequests.(PullRequestReviewActions)
	return r, ok && r != nil
}

func (a SandboxCLIActions) ReplyToPullRequestComment(ctx context.Context, taskID string, commentID int, body string) (gitproxy.CLIActionResult, error) {
	r, ok := a.reviewActions()
	if !ok {
		return gitproxy.CLIActionResult{}, errNoReviewActions
	}
	text, refused, err := r.ReplyForTask(ctx, taskID, commentID, body)
	return gitproxy.CLIActionResult{Text: text, Refused: refused}, err
}

func (a SandboxCLIActions) ResolveReviewThread(ctx context.Context, taskID, threadID string, commentID int) (gitproxy.CLIActionResult, error) {
	r, ok := a.reviewActions()
	if !ok {
		return gitproxy.CLIActionResult{}, errNoReviewActions
	}
	text, refused, err := r.ResolveForTask(ctx, taskID, threadID, commentID)
	return gitproxy.CLIActionResult{Text: text, Refused: refused}, err
}

// revertTargetPattern is the task forms grain git revert --task takes:
// "123", "task-123", "grain/task-123" and "grain/<deployment>/task-123".
var revertTargetPattern = regexp.MustCompile(`^(?:grain/(?:[a-z0-9][a-z0-9-]*/)?)?(?:task-)?([0-9]+)$`)

// RevertInfo describes target -- a task in the caller's own repository,
// or any task when anyTask (self-debug's reach) -- for grain git revert.
func (a SandboxCLIActions) RevertInfo(ctx context.Context, taskID, target string, anyTask bool) (gitproxy.RevertInfo, error) {
	m := revertTargetPattern.FindStringSubmatch(strings.TrimSpace(target))
	if m == nil {
		return gitproxy.RevertInfo{}, fmt.Errorf("%w: %q is not a task ID (e.g. \"123\" or \"grain/task-123\")", namespace.ErrBadRequest, target)
	}
	caller, err := a.Client.Store.GetTask(ctx, taskID)
	if err != nil {
		return gitproxy.RevertInfo{}, err
	}
	task, err := a.Client.Store.GetTask(ctx, m[1])
	if err != nil {
		return gitproxy.RevertInfo{}, err
	}
	if task == nil || caller == nil {
		return gitproxy.RevertInfo{}, fmt.Errorf("%w: no task %s", namespace.ErrNotFound, m[1])
	}
	if !anyTask && !sameTarget(caller.Target, task.Target) {
		// Answered as missing rather than refused: whether a task exists
		// in another repository is not this run's to learn.
		return gitproxy.RevertInfo{}, fmt.Errorf("%w: no task %s in this task's repository", namespace.ErrNotFound, m[1])
	}
	info := gitproxy.RevertInfo{Task: task.ID, Title: task.Title, Branch: task.Branch}
	if task.Target != nil {
		info.Repo = task.Target.String()
	}
	if pr := model.TaskPullRequest(*task); pr != nil {
		info.PullRequest = pr.String()
	}
	return info, nil
}

func sameTarget(a, b *model.RepoRef) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.String(), b.String())
}
