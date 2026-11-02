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

package daemon

// sandbox_cli.go wires the sandbox proxy's acting /cli/ routes
// (pkg/gitproxy's cli_actions.go: grain ci wait / run-job / retrigger,
// grain pr open / reply / resolve, grain git revert's revert-info) to the
// parts of this daemon that do the work: the UI/API server's own client,
// for CI and for opening the pull request, this daemon's GitHub client
// for review threads, and orchestrator.RunWaitCredits for the credit a
// wait earns.
//
// The proxy is built after the UI server has started, but by a function
// (StartGitProxy) that is not handed the UI server's client, so the
// client is published here when it is made (liveSandboxCLI) and the proxy
// is given a gate that reads it per request -- the same shape
// PullRequestGate has over LivePullRequests.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// liveSandboxCLI is the UI/API server's client, once startUIServer has
// made it.
var liveSandboxCLI atomic.Pointer[ui.Client]

// errSandboxCLIUnwired is a request that arrived before the UI/API server
// was up, or on a daemon running without one.
var errSandboxCLIUnwired = errors.New("this daemon's UI/API server is not running, so the sandbox CLI cannot act on tasks yet")

// sandboxCLIGate is gitproxy.CLIActions over whatever liveSandboxCLI
// holds when a request arrives.
type sandboxCLIGate struct{}

var (
	_ gitproxy.CLIActions            = sandboxCLIGate{}
	_ gitproxy.CLITaskActions        = sandboxCLIGate{}
	_ gitproxy.CLIGitHubCheckActions = sandboxCLIGate{}
)

func (sandboxCLIGate) actions() (ui.SandboxCLIActions, error) {
	client := liveSandboxCLI.Load()
	if client == nil {
		return ui.SandboxCLIActions{}, errSandboxCLIUnwired
	}
	return ui.SandboxCLIActions{Client: client}, nil
}

func (g sandboxCLIGate) StartTaskCI(ctx context.Context, taskID string, req gitproxy.CIStartRequest) (ci.Run, error) {
	a, err := g.actions()
	if err != nil {
		return ci.Run{}, err
	}
	return a.StartTaskCI(ctx, taskID, req)
}

func (g sandboxCLIGate) CIRun(ctx context.Context, id string) (ci.Run, bool) {
	a, err := g.actions()
	if err != nil {
		return ci.Run{}, false
	}
	return a.CIRun(ctx, id)
}

// Ready is ci.Readiness: false until the UI/API server is wired and its
// CI service is up, so that a CIRun answering false before then is
// served as "not up yet" rather than as a forgotten run.
func (g sandboxCLIGate) Ready() bool {
	a, err := g.actions()
	return err == nil && a.Ready()
}

func (g sandboxCLIGate) OpenPullRequest(ctx context.Context, taskID string) (gitproxy.CLIActionResult, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.CLIActionResult{}, err
	}
	return a.OpenPullRequest(ctx, taskID)
}

func (g sandboxCLIGate) ReplyToPullRequestComment(ctx context.Context, taskID string, commentID int, body string) (gitproxy.CLIActionResult, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.CLIActionResult{}, err
	}
	return a.ReplyToPullRequestComment(ctx, taskID, commentID, body)
}

func (g sandboxCLIGate) ResolveReviewThread(ctx context.Context, taskID, threadID string, commentID int) (gitproxy.CLIActionResult, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.CLIActionResult{}, err
	}
	return a.ResolveReviewThread(ctx, taskID, threadID, commentID)
}

func (g sandboxCLIGate) RevertInfo(ctx context.Context, taskID, target string, anyTask bool) (gitproxy.RevertInfo, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.RevertInfo{}, err
	}
	return a.RevertInfo(ctx, taskID, target, anyTask)
}

func (g sandboxCLIGate) NotifyOperator(ctx context.Context, taskID string, req gitproxy.CLINotifyRequest) (gitproxy.CLINotifyResult, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.CLINotifyResult{}, err
	}
	return a.NotifyOperator(ctx, taskID, req)
}

func (g sandboxCLIGate) MessageTask(ctx context.Context, callerTaskID, targetTaskID, body string) (int64, error) {
	a, err := g.actions()
	if err != nil {
		return 0, err
	}
	return a.MessageTask(ctx, callerTaskID, targetTaskID, body)
}

func (g sandboxCLIGate) CheckWaitTargets(ctx context.Context, callerTaskID string, ids []string) ([]model.WaitTarget, error) {
	a, err := g.actions()
	if err != nil {
		return nil, err
	}
	return a.CheckWaitTargets(ctx, callerTaskID, ids)
}

func (g sandboxCLIGate) ProposeTask(ctx context.Context, callerTaskID, runID string, req gitproxy.CLIProposeRequest) (gitproxy.CLIProposeResult, error) {
	a, err := g.actions()
	if err != nil {
		return gitproxy.CLIProposeResult{}, err
	}
	return a.ProposeTask(ctx, callerTaskID, runID, req)
}

func (g sandboxCLIGate) ListAPIRoutes(ctx context.Context, callerTaskID string, grants []string, scopeFilter string) ([]gitproxy.CLIAPIRoute, error) {
	a, err := g.actions()
	if err != nil {
		return nil, err
	}
	return a.ListAPIRoutes(ctx, callerTaskID, grants, scopeFilter)
}

func (g sandboxCLIGate) UsesGitHubChecks(ctx context.Context, taskID string) (bool, error) {
	a, err := g.actions()
	if err != nil {
		return false, err
	}
	return a.UsesGitHubChecks(ctx, taskID)
}

func (g sandboxCLIGate) WaitForGitHubChecks(ctx context.Context, taskID string,
	timeout, poll time.Duration, onPoll func(waited time.Duration)) (mcp.GitHubCheckWaitResult, error) {

	a, err := g.actions()
	if err != nil {
		return mcp.GitHubCheckWaitResult{}, err
	}
	return a.WaitForGitHubChecks(ctx, taskID, timeout, poll, onPoll)
}

// NewWaitCreditor returns a gitproxy.CLIWaitCreditor backed by credits.
func NewWaitCreditor(credits *orchestrator.RunWaitCredits) gitproxy.CLIWaitCreditor {
	return waitCreditAdapter{credits: credits}
}

// CreditCLIWait is gitproxy.CLIWaitCreditor over the same registry
// CreditWaitForTask credits, reported as its own source so that a run's
// CLI waits add to its MCP waits rather than competing with them
// (orchestrator.RunWaitCredits.CreditSource).
func (a waitCreditAdapter) CreditCLIWait(ctx context.Context, taskID string, blocked time.Duration) (gitproxy.CLIWaitCredit, error) {
	credit, err := a.credits.CreditSource(ctx, taskID, orchestrator.WaitSourceCLI, blocked)
	if err != nil {
		return gitproxy.CLIWaitCredit{}, err
	}
	return gitproxy.CLIWaitCredit{
		Blocked:  credit.Blocked,
		Credited: credit.Credited,
		Cap:      orchestrator.RunWaitCreditCap,
		Live:     credit.Live,
	}, nil
}

// reviewTool runs one of pkg/mcp's review-thread tools for taskID's own
// pull request, scoped exactly as that task's mcpserver would scope it:
// the task's repository and branch, and the pull request it was opened
// as. The tool's own checks -- the comment or thread must belong to that
// pull request -- are what keep an ID from elsewhere from being acted on.
func (o *PullRequestOpener) reviewTool(ctx context.Context, taskID, name string, args map[string]any) (string, bool, error) {
	task, err := o.Store.GetTask(ctx, taskID)
	if err != nil {
		return "", false, err
	}
	if task == nil || task.Target == nil {
		return "", false, fmt.Errorf("task %s has no repository of its own", taskID)
	}
	scope := mcp.PullRequestScope{Owner: task.Target.Owner, Repo: task.Target.Name, Branch: task.BranchName()}
	if pr := model.TaskPullRequest(*task); pr != nil && pr.Repo == *task.Target {
		scope.PullRequestNumber = pr.Number
	}
	for _, tool := range mcp.NewPullRequestTools(o.Client, nil, scope) {
		if tool.Name == name {
			res := tool.Handler(ctx, args)
			return res.Text, res.IsError, nil
		}
	}
	return "", false, fmt.Errorf("this daemon has no %s", name)
}

// ReplyForTask is ui.PullRequestReviewActions' reply.
func (o *PullRequestOpener) ReplyForTask(ctx context.Context, taskID string, commentID int, body string) (string, bool, error) {
	args := map[string]any{"body": body}
	if commentID > 0 {
		args["comment_id"] = float64(commentID)
	}
	return o.reviewTool(ctx, taskID, "reply_to_pull_request_comment", args)
}

// ResolveForTask is ui.PullRequestReviewActions' resolve.
func (o *PullRequestOpener) ResolveForTask(ctx context.Context, taskID, threadID string, commentID int) (string, bool, error) {
	args := map[string]any{}
	if threadID != "" {
		args["thread_id"] = threadID
	}
	if commentID > 0 {
		args["comment_id"] = float64(commentID)
	}
	return o.reviewTool(ctx, taskID, "resolve_review_thread", args)
}

// ReplyForTask is the same gate for ui.PullRequestReviewActions.
func (PullRequestGate) ReplyForTask(ctx context.Context, taskID string, commentID int, body string) (string, bool, error) {
	opener := LivePullRequests.Load()
	if opener == nil {
		return "", false, errNoGitHubClientYet
	}
	return opener.ReplyForTask(ctx, taskID, commentID, body)
}

// ResolveForTask is the same gate for ui.PullRequestReviewActions.
func (PullRequestGate) ResolveForTask(ctx context.Context, taskID, threadID string, commentID int) (string, bool, error) {
	opener := LivePullRequests.Load()
	if opener == nil {
		return "", false, errNoGitHubClientYet
	}
	return opener.ResolveForTask(ctx, taskID, threadID, commentID)
}

// GitHubPullRequestReader is ui.PullRequestCheckReader.
func (o *PullRequestOpener) GitHubPullRequestReader() mcp.PullRequestReader {
	if o == nil {
		return nil
	}
	return o.Client
}

// GitHubPullRequestReader is the same gate for ui.PullRequestCheckReader.
func (PullRequestGate) GitHubPullRequestReader() mcp.PullRequestReader {
	opener := LivePullRequests.Load()
	if opener == nil {
		return nil
	}
	return opener.GitHubPullRequestReader()
}

var errNoGitHubClientYet = errors.New(
	"this daemon has no GitHub client yet: " +
		"its reconcile loop has not started (or has failed -- check the daemon log)")

// sandboxExternalCI is the namespace's ci.ExternalRuns -- GitHub Actions
// runs, served under ci:// and by grain ci list/show/log -- over whatever
// liveSandboxCLI holds: the UI/API server's client reads them through the
// daemon's own forge connection (pkg/ui/ci_actions.go). Before that
// client is up there are none to serve.
type sandboxExternalCI struct{}

var _ ci.ExternalRuns = sandboxExternalCI{}

func (sandboxExternalCI) FindExternalRuns(ctx context.Context, f ci.RunFilter, limit int) ([]ci.Run, string) {
	client := liveSandboxCLI.Load()
	if client == nil {
		return nil, ""
	}
	return client.ExternalCIRuns().FindExternalRuns(ctx, f, limit)
}

func (sandboxExternalCI) ExternalRun(ctx context.Context, id string) (ci.Run, bool, error) {
	client := liveSandboxCLI.Load()
	if client == nil {
		return ci.Run{}, false, nil
	}
	return client.ExternalCIRuns().ExternalRun(ctx, id)
}

func (sandboxExternalCI) ExternalJobLog(ctx context.Context, id, job string) (string, error) {
	client := liveSandboxCLI.Load()
	if client == nil {
		return "", errSandboxCLIUnwired
	}
	return client.ExternalCIRuns().ExternalJobLog(ctx, id, job)
}
