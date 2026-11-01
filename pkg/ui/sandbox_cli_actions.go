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

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
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

var (
	_ gitproxy.CLIActions            = SandboxCLIActions{}
	_ gitproxy.CLITaskActions        = SandboxCLIActions{}
	_ gitproxy.CLIGitHubCheckActions = SandboxCLIActions{}
)

// PullRequestReviewActions is what grain pr reply and grain pr resolve
// need on top of Config.PullRequests: answering and resolving review
// threads on a task's own open pull request. The daemon's pull request
// opener implements it; a Config.PullRequests without it refuses both.
type PullRequestReviewActions interface {
	ReplyForTask(ctx context.Context, taskID string, commentID int, body string) (text string, refused bool, err error)
	ResolveForTask(ctx context.Context, taskID, threadID string, commentID int) (text string, refused bool, err error)
}

// PullRequestCheckReader is an optional interface implemented by
// Config.PullRequests when it can supply an mcp.PullRequestReader for polling
// GitHub check runs on a task's branch.
type PullRequestCheckReader interface {
	GitHubPullRequestReader() mcp.PullRequestReader
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

// Ready is ci.Readiness for the CI this answers from: false while the
// daemon's CI service is not up yet, so a CIRun answering false then is
// not read as a run that has been forgotten.
func (a SandboxCLIActions) Ready() bool {
	return a.Client.Config.CI != nil && ci.IsUp(a.Client.Config.CI)
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

func (a SandboxCLIActions) NotifyOperator(ctx context.Context, taskID string, req gitproxy.CLINotifyRequest) (gitproxy.CLINotifyResult, error) {
	resp, err := a.Client.AgentNotify(ctx, taskID, RaiseNotificationRequest{
		Subject:    req.Subject,
		Title:      req.Title,
		Body:       req.Body,
		Severity:   req.Severity,
		Task:       req.Task,
		ClearsWhen: req.ClearsWhen,
	})
	if err != nil {
		return gitproxy.CLINotifyResult{}, cliActionError(err)
	}
	n := resp.Notification
	return gitproxy.CLINotifyResult{
		ID:       n.ID,
		Subject:  n.Subject,
		Severity: string(n.Severity),
		Count:    n.RaiseCount,
		New:      resp.New,
	}, nil
}

func (a SandboxCLIActions) MessageTask(ctx context.Context, callerTaskID, targetTaskID, body string) (int64, error) {
	msg, err := a.Client.PostTaskNote(ctx, targetTaskID, callerTaskID, body)
	if err != nil {
		return 0, cliActionError(err)
	}
	return msg.ID, nil
}

func (a SandboxCLIActions) CheckWaitTargets(ctx context.Context, callerTaskID string, ids []string) ([]model.WaitTarget, error) {
	targets, err := a.Client.WaitOn(ctx, callerTaskID, ids, false)
	if err != nil {
		return nil, cliActionError(err)
	}
	return targets, nil
}

func sandboxProposalKey(callerTaskID, runID, localID, title string) string {
	identity := strings.TrimSpace(localID)
	if identity == "" {
		identity = fmt.Sprintf("cli:%s:%s", runID, title)
	}
	sum := sha256.Sum256([]byte(callerTaskID + "\x00" + identity))
	return "proposal:" + callerTaskID + ":" + hex.EncodeToString(sum[:])
}

func (a SandboxCLIActions) ProposeTask(ctx context.Context, callerTaskID, runID string, req gitproxy.CLIProposeRequest) (gitproxy.CLIProposeResult, error) {
	parent, err := a.Client.Store.GetTask(ctx, callerTaskID)
	if err != nil {
		return gitproxy.CLIProposeResult{}, cliActionError(err)
	}
	if parent == nil {
		return gitproxy.CLIProposeResult{}, fmt.Errorf("%w: no task %s", namespace.ErrNotFound, callerTaskID)
	}
	repo, base := "", ""
	if !req.NoRepo && parent.Target != nil {
		repo = parent.Target.String()
	}
	if req.Repo != "" {
		repo = req.Repo
		base = ""
		for _, w := range parent.Writes {
			if strings.EqualFold(w.Repo.String(), req.Repo) {
				base = w.Base
				break
			}
		}
	}
	if req.Branch != "" {
		base = req.Branch
	}
	var deps []string
	for _, dep := range req.DependsOn {
		dep = strings.TrimPrefix(strings.TrimSpace(dep), "#")
		if dep == "" {
			continue
		}
		if existing, err := a.Client.Store.TaskWithTag(ctx, sandboxProposalKey(callerTaskID, runID, dep, "")); err == nil && existing != nil {
			dep = existing.ID
		}
		deps = append(deps, dep)
	}
	var fields map[string]CustomFieldValue
	if len(req.CustomFields) > 0 {
		fields = make(map[string]CustomFieldValue, len(req.CustomFields))
		for k, v := range req.CustomFields {
			fields[k] = CustomFieldValue(v)
		}
	}
	var caps *[]string
	if len(req.Capabilities) > 0 {
		c := append([]string(nil), req.Capabilities...)
		caps = &c
	}
	key := sandboxProposalKey(callerTaskID, runID, req.ID, req.Title)
	created, err := a.Client.CreateTask(ctx, CreateTaskRequest{
		Title:           req.Title,
		Description:     req.Body,
		Repo:            repo,
		NoRepo:          req.NoRepo,
		Base:            base,
		AutoMerge:       false,
		HoldForApproval: true,
		Supersedes:      "",
		DependsOn:       deps,
		CustomFields:    fields,
		Reads:           req.Reads,
		ProposedBy:      callerTaskID,
		ProposalKey:     key,
		Capabilities:    caps,
		FixesBaseBranch: req.FixesBaseBranch,
		NoPort:          req.NoPort,
		KeepRunning:     req.KeepRunning,
		Origin:          req.Origin,
		DeferredUntil:   req.NotBefore,
		Priority:        req.Priority,
	})
	if err != nil {
		return gitproxy.CLIProposeResult{}, cliActionError(err)
	}
	return gitproxy.CLIProposeResult{
		ID:              created.ID,
		URL:             "/tasks/" + created.ID,
		HoldForApproval: true,
		AutoMerge:       false,
		Queued:          false,
		Held:            true,
		ApprovalNote:    created.ApprovalNote,
	}, nil
}

// SandboxAPIRoutes returns the non-dangerous agent API routes reachable by
// grants on the sandbox CLI channel, optionally filtered by scopeFilter.
func SandboxAPIRoutes(grants []string, scopeFilter string) []APIRoute {
	set := model.SandboxAPIGrantsOf(grants)
	var out []APIRoute
	for _, r := range AgentAPIRoutes() {
		if IsSandboxDeniedScope(r.Scope) {
			continue
		}
		if sandboxChannelGuard(r.Pattern, nil) != nil {
			continue
		}
		if scopeFilter != "" && !strings.EqualFold(string(r.Scope), scopeFilter) {
			continue
		}
		if !set.Has(r.Scope) && !agentCapabilityAllowsRoute(grants, set, r.Pattern, r.Scope) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// sandboxGuardReadsBody is the routes sandboxChannelGuard refuses only for
// some request bodies: reachable from the sandbox CLI, but with arguments
// (park: true, supersedes, attaching capabilities) that only the model's
// own tool may pass.
var sandboxGuardReadsBody = map[string]bool{
	"POST /api/tasks/{id}/wait-on":      true,
	"PATCH /api/tasks/{id}":             true,
	"POST /api/tasks/{id}/capabilities": true,
	"POST /api/tasks":                   true,
}

// ModelOnlyAgentAPIToolNames is the api_* tools grants reach that stay on
// the model's control roster (docs/design/sandbox-first-agent-surface.md
// §1B): every granted route the sandbox CLI cannot reach -- dangerous and
// model-only scopes, scope sets that need a person, the model-only control
// routes -- and those whose sandbox guard depends on the body, so the
// model keeps the arguments the sandbox credential is refused.
func ModelOnlyAgentAPIToolNames(grants []string) []string {
	set := APIGrantsOf(grants)
	if set.Empty() {
		return nil
	}
	sandbox := map[string]bool{}
	for _, r := range SandboxAPIRoutes(grants, "") {
		if !sandboxGuardReadsBody[r.Pattern] {
			sandbox[r.Pattern] = true
		}
	}
	var names []string
	for _, r := range AgentAPIRoutes() {
		if set.Has(r.Scope) && !sandbox[r.Pattern] {
			names = append(names, r.Tool)
		}
	}
	return names
}

func (a SandboxCLIActions) ListAPIRoutes(ctx context.Context, callerTaskID string, grants []string, scopeFilter string) ([]gitproxy.CLIAPIRoute, error) {
	if len(grants) == 0 && a.Client != nil && a.Client.Store != nil && callerTaskID != "" {
		if t, err := a.Client.Store.GetTask(ctx, callerTaskID); err == nil && t != nil {
			grants = grantNames(t.Grants)
		}
	}
	routes := SandboxAPIRoutes(grants, scopeFilter)
	out := make([]gitproxy.CLIAPIRoute, 0, len(routes))
	for _, r := range routes {
		out = append(out, gitproxy.CLIAPIRoute{
			Method:      r.Method,
			Path:        r.Path,
			Scope:       string(r.Scope),
			Tool:        r.Tool,
			Description: r.Description,
			NeedsReason: r.NeedsReason,
		})
	}
	return out, nil
}

func (a SandboxCLIActions) resolveCITask(ctx context.Context, taskID string) (*model.Task, error) {
	if a.Client == nil || a.Client.Store == nil {
		return nil, nil
	}
	task, err := a.Client.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, cliActionError(err)
	}
	if task == nil {
		return nil, fmt.Errorf("%w: task %s not found", namespace.ErrNotFound, taskID)
	}
	if ownerID, inPlace := task.CIOwner(); inPlace {
		if owner, oerr := a.Client.Store.GetTask(ctx, ownerID); oerr == nil && owner != nil && owner.Target != nil {
			task = owner
		}
	}
	return task, nil
}

func (a SandboxCLIActions) UsesGitHubChecks(ctx context.Context, taskID string) (bool, error) {
	task, err := a.resolveCITask(ctx, taskID)
	if err != nil || task == nil || task.Target == nil {
		return false, err
	}
	if grainci.RequiredFor(*task.Target) {
		return false, nil
	}
	eff, err := a.Client.Store.EffectiveRepoConfig(ctx, *task.Target)
	if err != nil {
		return false, err
	}
	switch eff.CIMode {
	case model.CIModeGitHub:
		if rules, rerr := a.Client.Store.MergeRulesOf(ctx, *task.Target); rerr == nil && !rules.Empty() {
			if !rules.Effective(nil, false, model.CIModeGitHub).Mode.GitHub() {
				return false, nil
			}
		}
		return true, nil
	case model.CIModeGrain, model.CIModeNone:
		return false, nil
	}
	return false, nil
}

func (a SandboxCLIActions) githubCheckReader() mcp.PullRequestReader {
	if a.Client == nil {
		return nil
	}
	if r, ok := a.Client.Config.PullRequests.(PullRequestCheckReader); ok {
		if reader := r.GitHubPullRequestReader(); reader != nil {
			return reader
		}
	}
	if reader, ok := a.Client.Config.GitHubPullRequests.(mcp.PullRequestReader); ok && reader != nil {
		return reader
	}
	return nil
}

func (a SandboxCLIActions) WaitForGitHubChecks(ctx context.Context, taskID string,
	timeout, poll time.Duration, onPoll func(waited time.Duration)) (mcp.GitHubCheckWaitResult, error) {

	task, err := a.resolveCITask(ctx, taskID)
	if err != nil {
		return mcp.GitHubCheckWaitResult{}, err
	}
	if task == nil || task.Target == nil {
		return mcp.GitHubCheckWaitResult{}, fmt.Errorf("%w: task %s has no target repository", namespace.ErrBadRequest, taskID)
	}
	reader := a.githubCheckReader()
	if reader == nil {
		return mcp.GitHubCheckWaitResult{}, errors.New("this deployment has no GitHub client wired to read check runs")
	}
	scope := mcp.PullRequestScope{
		Owner:  task.Target.Owner,
		Repo:   task.Target.Name,
		Branch: task.BranchName(),
		Base:   task.Base,
	}
	if pr := model.TaskPullRequest(*task); pr != nil && pr.Repo.Same(*task.Target) {
		scope.PullRequestNumber = pr.Number
	}
	return mcp.WaitForGitHubChecks(ctx, reader, scope, timeout, poll, 0, onPoll)
}
