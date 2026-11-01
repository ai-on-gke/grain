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

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// daemonPullRequests implements mcp.PullRequestOpener by asking a running
// "grain daemon" to open the pull request for one task's branch (pkg/ui's
// POST /api/tasks/{id}/pull-request) -- the same REST API the grain CLI
// speaks, reached the same way, since this process is on the controller
// alongside the daemon that forked it.
//
// The hop exists because of what this call is, not because this process
// cannot reach GitHub at all: pullRequestReader below does build a REST
// client, for pull_request_status. That one only ever reads, and only
// within a scope fixed at process start. Opening a pull request is a
// write, and which task it is opened for, on which branch, against which
// base, has always been grain's decision rather than an agent's -- so it
// is made where those decisions are already made. This asks the daemon
// by task id, and the daemon reads the repo and the branch out of that
// task's own record; nothing in a tool call reaches GitHub as data.
type DaemonPullRequests struct {
	Client *ui.HTTPClient
	TaskID string
}

func (d DaemonPullRequests) OpenPullRequest(ctx context.Context) (mcp.PullRequestReport, error) {
	return d.OpenChange(ctx, "", "")
}

// OpenChange is open_pull_request with a change description, which a
// changelist task's CL is built from (mcp.ChangeOpener).
func (d DaemonPullRequests) OpenChange(ctx context.Context, summary, body string) (mcp.PullRequestReport, error) {
	var status ui.PullRequestStatus
	var err error
	if summary == "" && body == "" {
		status, err = d.Client.OpenPullRequest(ctx, d.TaskID)
	} else {
		status, err = d.Client.OpenChange(ctx, d.TaskID, ui.OpenChangeRequest{Summary: summary, Body: body})
	}
	if err != nil {
		return mcp.PullRequestReport{}, err
	}
	report := mcp.PullRequestReport{
		Repo:            status.Repo,
		Number:          status.Number,
		URL:             status.URL,
		ChecksAvailable: status.ChecksAvailable,
		ChecksError:     status.ChecksError,
		Note:            status.Note,
	}
	for _, c := range status.Checks {
		report.Checks = append(report.Checks, mcp.CheckReport{
			Name: c.Name, Status: c.Status, Conclusion: c.Conclusion,
		})
	}
	return report, nil
}

// DaemonChangeChecks implements mcp.ChangeChecksReader by asking the
// daemon (POST /api/tasks/{id}/change-checks), which reads the task's
// primary change and answers through its registered change.Checks.
type DaemonChangeChecks struct {
	Client *ui.HTTPClient
	TaskID string
}

func (d DaemonChangeChecks) ChangeChecksStatus(ctx context.Context) (change.ChecksReport, error) {
	resp, err := d.Client.ChangeChecks(ctx, d.TaskID, ui.ChangeChecksRequest{Op: ui.ChangeChecksStatus})
	return resp.Report, err
}

func (d DaemonChangeChecks) WaitChangeChecks(ctx context.Context, req change.ChecksWait) (change.ChecksReport, error) {
	resp, err := d.Client.ChangeChecks(ctx, d.TaskID, ui.ChangeChecksRequest{
		Op: ui.ChangeChecksWait, Jobs: req.Jobs,
		TimeoutMS: req.Timeout.Milliseconds(), CreditedMS: req.Credited.Milliseconds(),
	})
	return resp.Report, err
}

// DaemonChangeReview implements mcp.ChangeReviewer by asking the daemon
// (POST /api/tasks/{id}/change-review), which answers through the
// registered change.Replier.
type DaemonChangeReview struct {
	Client *ui.HTTPClient
	TaskID string
}

func (d DaemonChangeReview) ReplyToChangeThread(ctx context.Context, thread, body string) (change.ReplyResult, error) {
	resp, err := d.Client.ChangeReview(ctx, d.TaskID, ui.ChangeReviewRequest{Op: ui.ChangeReviewReply, Thread: thread, Body: body})
	return resp.Result, err
}

func (d DaemonChangeReview) ResolveChangeThread(ctx context.Context, thread string) (change.ReplyResult, error) {
	resp, err := d.Client.ChangeReview(ctx, d.TaskID, ui.ChangeReviewRequest{Op: ui.ChangeReviewResolve, Thread: thread})
	return resp.Result, err
}

func (d DaemonChangeChecks) ChangeCIRefusal(ctx context.Context, tool string) (string, error) {
	resp, err := d.Client.ChangeChecks(ctx, d.TaskID, ui.ChangeChecksRequest{Op: ui.ChangeChecksRefusal, Tool: tool})
	return resp.Refusal, err
}

// daemonSandbox implements mcp.SandboxRecreator by asking a running
// "grain daemon" to destroy this run's sandbox and build an empty one in
// its place (pkg/ui's POST /api/tasks/{id}/sandbox/recreate) -- the same
// REST hop daemonPullRequests above makes, over the same client, for the
// same reason.
//
// The reason is sharper here than it is there, though. This process
// cannot do the job at all, not merely "should not": every tool it
// serves reaches into the sandbox and does nothing else, while creating
// and destroying sandboxes needs the VM shape this run asked for, the
// proxy token to mint for it, the capability material to place in it and
// the repo to clone into it -- none of which exists on this side of the
// hop. Which sandbox gets destroyed is fixed at process start by -task,
// so no tool call can name another run's.

type DaemonTasks struct {
	Client *ui.HTTPClient
	// taskID is the task this run belongs to, sent with a broadcast so
	// that the daemon checks that task's own team and grants rather than
	// taking this process's word for them.
	taskID string
}

func (d DaemonTasks) ListTasks(ctx context.Context) ([]mcp.TaskSummary, error) {
	tasks, err := d.Client.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.TaskSummary, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskSummary(t))
	}
	return out, nil
}

func (d DaemonTasks) Task(ctx context.Context, id string) (mcp.TaskRecord, error) {
	detail, err := d.Client.GetTask(ctx, id)
	if err != nil {
		return mcp.TaskRecord{}, err
	}
	record := mcp.TaskRecord{TaskSummary: taskSummary(detail.Task), Description: detail.Description, Author: detail.Author, AuthorKind: detail.AuthorKind, Base: detail.Base, Capabilities: detail.Capabilities, PullRequest: detail.PullRequest, FailedAttempts: detail.FailedAttempts, LastFailureAt: detail.LastFailureAt, LastFailureReason: detail.LastFailureReason, Activity: mcp.TaskActivity{Note: detail.Activity, At: detail.ActivityAt, BySetup: detail.ActivityBySetup}}
	for _, a := range detail.Attempts {
		record.Attempts = append(record.Attempts, mcp.TaskAttempt{Number: a.Number, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, Outcome: a.Outcome, Detail: a.Detail, PromptBytes: a.PromptBytes, PromptTokens: a.PromptTokens, Model: a.Model, AgentFramework: a.AgentFramework})
	}
	for _, c := range detail.Comments {
		record.Comments = append(record.Comments, mcp.TaskComment{ID: c.ID, Author: c.Author, AuthorKind: c.AuthorKind, Body: c.Body, CreatedAt: c.CreatedAt, Handover: c.Handover})
	}
	return record, nil
}

func (d DaemonTasks) TaskPrompt(ctx context.Context, id string) (mcp.TaskPromptRecord, error) {
	p, err := d.Client.TaskPrompt(ctx, id)
	return mcp.TaskPromptRecord{Prompt: p.Prompt, Attempt: p.Attempt,
		Bytes: p.Bytes, Tokens: p.Tokens, ContextTokens: p.ContextTokens}, err
}

func (d DaemonTasks) AttemptTranscript(ctx context.Context, id string, attempt int) (string, error) {
	return d.Client.AttemptTranscript(ctx, id, attempt)
}

// daemonMetrics implements mcp.MetricsReader by asking a running "grain
// daemon" for the measurement report (GET /api/metrics, the same request
// `grain metrics` makes over the same client) and rendering it here with
// the CLI's own renderer.
//
// Rendered on this side of the hop rather than in pkg/mcp because the
// report shape is pkg/ui's, and pkg/ui imports pkg/mcp by way of
// pkg/capability/selfdebug -- so this process, which already imports
// both, is the only place the two can meet. It is also where the
// question gets parsed: metricsRequest is the same function cmdMetrics
// builds its request with, so a run that asks for a window nobody can
// parse gets the sentence an operator would have got.
type DaemonMetrics struct{ Client *ui.HTTPClient }

func (d DaemonMetrics) Metrics(ctx context.Context, q mcp.MetricsQuery) (string, error) {
	req, err := MetricsRequest(q.Window, q.Since, q.Until, q.Repos, q.Authors)
	if err != nil {
		return "", err
	}
	// Asked of the daemon too, not only rendered here: the daemon then
	// reads just what that section is built from, so a section is not
	// lost to an input it never used.
	req.Section = q.Section
	report, err := d.Client.Metrics(ctx, req)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	RenderMetrics(&out, report, q.Section)
	return out.String(), nil
}

// taskSummary projects the task shape the daemon serves onto the one the
// tools render. The merge queue's five fields come across whole -- they
// are already on the list shape, since the UI's own row draws them, so a
// self-debug run reading "which of these is stuck" pays nothing for them
// that the page does not already pay. The fifth is the daemon's own
// answer to that question (ui.Task.MergeQueueStalledAt). The stall note
// beside them is the same bargain for the other lane (ui.Task.Stall,
// orchestrator's taskLane): computed for the row either way, and the one
// thing in the record that says a task's own state has stood longer than
// it should. BlockedBy comes across on those same terms -- the list
// shape carries it because the UI's own row draws a chip from it, and it
// is the one thing that tells a queued task waiting its turn from a
// queued task nothing is ever going to dispatch.
//
// Kinds, DependsOn and ChangedAt are the compact views' (DT-13): the kind
// tags are the daemon's own rule (ui.TaskKindTags), so the summary a
// judgement agent reads and the expect its writes are checked against
// agree by construction.
func taskSummary(t ui.Task) mcp.TaskSummary {
	return mcp.TaskSummary{ID: t.ID, Title: t.Title, State: string(t.State), Repo: t.Repo, Priority: t.Priority, Interactive: t.Interactive, CreatedAt: t.CreatedAt, BlockedBy: t.BlockedBy, Stall: t.Stall, MergeQueue: mcp.TaskMergeQueue{State: t.MergeQueueState, Note: t.MergeQueueNote, At: t.MergeQueueStateAt, StalledAt: t.MergeQueueStalledAt, BlockedAt: t.MergeQueueBlockedAt},
		Kinds: ui.TaskKindTags(t), DependsOn: t.DependsOn, ChangedAt: taskChangedAt(t)}
}

// taskChangedAt is the latest change the record stamps (mcp.TaskSummary.ChangedAt).
func taskChangedAt(t ui.Task) *time.Time {
	var latest *time.Time
	for _, at := range []*time.Time{t.CreatedAt, t.StartedAt, t.EndedAt, t.ClosedAt, t.PrMergedAt, t.PrClosedAt,
		t.MergeQueueStateAt, t.MergeQueueStalledAt, t.MergeQueueBlockedAt} {
		if at != nil && (latest == nil || at.After(*latest)) {
			latest = at
		}
	}
	return latest
}

// DaemonPipeline implements mcp.PipelineReader over GET
// /api/pipeline/changes.
type DaemonPipeline struct{ Client *ui.HTTPClient }

func (d DaemonPipeline) PipelineChanges(ctx context.Context, after string, limit int) (mcp.PipelineChangesPage, error) {
	page, err := d.Client.PipelineChanges(ctx, after, limit)
	if err != nil {
		return mcp.PipelineChangesPage{}, err
	}
	out := mcp.PipelineChangesPage{Next: page.Next, More: page.More, Since: page.Since}
	for _, c := range page.Changes {
		out.Changes = append(out.Changes, mcp.PipelineChange{At: c.At, ID: c.ID, Kind: c.Kind, From: c.From, To: c.To, Detail: c.Detail})
	}
	return out, nil
}

type daemonDB struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonDB) QueryDB(ctx context.Context, query string, params []any) (dbaccess.QueryResult, error) {
	if d.client == nil {
		return dbaccess.QueryResult{}, errors.New("daemon DB client is not available")
	}
	res, err := d.client.TaskDBQuery(ctx, d.taskID, query, params)
	if err != nil {
		return dbaccess.QueryResult{}, err
	}
	return dbaccess.QueryResult{
		Columns:   res.Columns,
		Rows:      res.Rows,
		Count:     res.Count,
		Truncated: res.Truncated,
	}, nil
}

func (d daemonDB) ExecDB(ctx context.Context, statement string, params []any) (dbaccess.ExecResult, error) {
	if d.client == nil {
		return dbaccess.ExecResult{}, errors.New("daemon DB client is not available")
	}
	res, err := d.client.TaskDBExec(ctx, d.taskID, statement, params)
	if err != nil {
		return dbaccess.ExecResult{}, err
	}
	return dbaccess.ExecResult{
		RowsAffected: res.RowsAffected,
		LastInsertID: res.LastInsertID,
	}, nil
}

type daemonMergeQueue struct {
	client *ui.HTTPClient
	// defaultBranch answers a repository's default branch, which is the
	// base of every task whose Base is empty -- the ordinary task. Nil
	// when this process has no GitHub credential to ask with.
	defaultBranch func(owner, repo string) (string, error)
}

// queuedPRBranch is the branch a queued task's pull request merges into:
// the branch of the batch it is testing in if it is in one (the pull
// request's own base, as the batch builder read it from GitHub), else
// the task's declared Base, else its repository's default branch. An
// empty Base means "the default branch", not "no branch", so reporting
// or filtering on it as written prints an empty branch and drops every
// default-branch pull request from a branch filter.
//
// The answer is "" only when none of those can be known; defaults caches
// the default-branch reads (failures included) for one report.
func (d daemonMergeQueue) queuedPRBranch(t ui.Task, inBatch map[string]string, defaults map[string]string) string {
	if branch := inBatch[t.ID]; branch != "" {
		return branch
	}
	if t.Base != "" {
		return t.Base
	}
	if d.defaultBranch == nil || t.Repo == "" {
		return ""
	}
	if branch, ok := defaults[t.Repo]; ok {
		return branch
	}
	branch := ""
	if ref, err := model.ParseRepo(t.Repo); err == nil {
		if def, err := d.defaultBranch(ref.StoredOwner(), ref.Name); err == nil {
			branch = def
		}
	}
	defaults[t.Repo] = branch
	return branch
}

func (d daemonMergeQueue) QueryMergeQueue(ctx context.Context, repo, branch string) (mcp.MergeQueueReport, error) {
	if repo != "" && d.defaultBranch != nil {
		if ref, err := model.ParseRepo(repo); err == nil {
			if _, err := d.defaultBranch(ref.StoredOwner(), ref.Name); err != nil {
				var report mcp.MergeQueueReport
				report.Repo = repo
				report.Branch = branch
				report.Paused = true
				report.PauseInfo = fmt.Sprintf("Merge queue is unavailable for %s: repository is on %s, a plain git host", repo, ref.GitHost())
				return report, nil
			}
		}
	}
	batches, err := d.client.MergeBatches(ctx, repo)
	if err != nil {
		return mcp.MergeQueueReport{}, fmt.Errorf("reading merge batches: %w", err)
	}
	pauseStatus, err := d.client.PipelinePause(ctx)
	if err != nil {
		return mcp.MergeQueueReport{}, fmt.Errorf("reading pipeline pause: %w", err)
	}
	tasks, err := d.client.ListTasks(ctx)
	if err != nil {
		return mcp.MergeQueueReport{}, fmt.Errorf("reading tasks: %w", err)
	}

	var report mcp.MergeQueueReport
	report.Repo = repo
	report.Branch = branch
	report.Paused = pauseStatus.Paused
	if pauseStatus.Paused {
		report.PauseInfo = pauseStatus.Reason
	}

	inBatch := make(map[string]string)
	for _, b := range batches.Batches {
		for _, m := range b.Members {
			inBatch[m.TaskID] = b.Branch
		}
		if branch != "" && b.Branch != branch {
			continue
		}
		var members []mcp.MergeBatchMemberReport
		for _, m := range b.Members {
			members = append(members, mcp.MergeBatchMemberReport{
				TaskID: m.TaskID,
				Number: m.Number,
				Title:  m.Title,
			})
		}
		report.Batches = append(report.Batches, mcp.MergeBatchReport{
			Repo:       b.Repo,
			Branch:     b.Branch,
			Ref:        b.Ref,
			BaseCommit: b.BaseCommit,
			Commit:     b.Commit,
			RunID:      b.RunID,
			Conclusion: b.Conclusion,
			Detail:     b.Detail,
			QueuedAt:   b.StartedAt,
			Members:    members,
		})
	}

	defaults := make(map[string]string)
	for _, t := range tasks {
		if t.MergeQueueState == "" {
			continue
		}
		if repo != "" && t.Repo != repo {
			continue
		}
		prBranch := d.queuedPRBranch(t, inBatch, defaults)
		// A branch nobody could resolve is kept rather than filtered
		// out: it may well be the one asked about, and a report that
		// silently leaves a pull request out is the fault this fixes.
		if branch != "" && prBranch != "" && prBranch != branch {
			continue
		}
		policy := ""
		if t.MergePolicy != nil {
			policy = t.MergePolicy.Describe()
		}
		report.QueuedPRs = append(report.QueuedPRs, mcp.QueuedPRReport{
			MergePolicy: policy,
			TaskID:      t.ID,
			Title:       t.Title,
			Repo:        t.Repo,
			Branch:      prBranch,
			State:       t.MergeQueueState,
			Note:        t.MergeQueueNote,
			At:          t.MergeQueueStateAt,
			Stalled:     t.MergeQueueStalledAt != nil,
			Blocked:     t.MergeQueueBlockedAt != nil,
		})
	}
	return report, nil
}

type daemonDeploymentHealth struct{ client *ui.HTTPClient }

func (d daemonDeploymentHealth) DeploymentHealth(ctx context.Context) (any, error) {
	health, err := d.client.SandboxHealth(ctx)
	if err != nil {
		return nil, err
	}
	// Kernel OOM evidence is absent from the daemon journal when the killed
	// process is the one that would have reported it. Keep only a small,
	// recent summary from the controller kernel log.
	lines, oomErr := systemlog.Dmesg{}.Read(ctx, systemlog.Query{Lines: 500})
	var oom []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "out of memory") || strings.Contains(lower, "oom-kill") || strings.Contains(lower, "killed process") {
			oom = append(oom, line)
			if len(oom) == 20 {
				break
			}
		}
	}
	return struct {
		Health       ui.SandboxHealthResponse `json:"health"`
		SandboxCount int                      `json:"sandboxCount"`
		RecentOOM    []string                 `json:"recentOOM,omitempty"`
		OOMError     string                   `json:"oomError,omitempty"`
	}{Health: health, SandboxCount: len(health.Sandboxes), RecentOOM: oom, OOMError: errorString(oomErr)}, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type daemonSandbox struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonSandbox) RecreateSandbox(ctx context.Context) (mcp.SandboxRecreationReport, error) {
	recreation, err := d.client.RecreateSandbox(ctx, d.taskID)
	if err != nil {
		return mcp.SandboxRecreationReport{}, err
	}
	return mcp.SandboxRecreationReport{
		Sandbox:     recreation.Sandbox,
		CheckoutDir: recreation.CheckoutDir,
		Restored:    recreation.Restored,
		Warnings:    recreation.Warnings,
	}, nil
}

// daemonStatus implements mcp.StatusReporter by asking a running "grain
// daemon" to record what this run says it is doing (pkg/ui's POST
// /api/tasks/{id}/activity) -- the third of the REST hops
// daemonPullRequests and daemonSandbox above make, over the same client.
//
// The hop is not about authority here, unlike those two: recording a
// phrase authorizes nothing and destroys nothing. It is about where the
// row is. A run's status is shown on its task, the task is a row in the
// daemon's store, and this process holds a transport into a sandbox and
// nothing else. Which task it lands on is fixed by -task at process
// start, so a run can only ever narrate itself.
// daemonConfigChanges is propose_config_change's mcp.ConfigChangeProposer:
// the daemon diffs, validates and records the branch the tool pushed, for
// this run's own task.
type daemonConfigChanges struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonConfigChanges) ProposeConfigChange(ctx context.Context, p mcp.ConfigChangeProposal) (*model.PendingConfigChange, error) {
	return d.client.ProposeConfigChange(ctx, d.taskID, ui.ConfigChangeProposal{
		Summary: p.Summary, Description: p.Description, Branch: p.Branch, HeadSHA: p.HeadSHA,
	})
}

// writesConfigRepo reports whether t may push grain/config.
func writesConfigRepo(t ui.Task) bool {
	for _, w := range t.Writes {
		if ref, err := model.ParseRepo(w.Repo); err == nil && model.IsConfigRepo(ref) {
			return true
		}
	}
	return false
}

type daemonStatus struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonStatus) ReportStatus(ctx context.Context, note string) (mcp.StatusReport, error) {
	activity, err := d.client.SetTaskActivity(ctx, d.taskID, note)
	if err != nil {
		return mcp.StatusReport{}, err
	}
	return mcp.StatusReport{Live: activity.Live}, nil
}

// daemonSessionRenamer implements mcp.SessionRenamer by asking a running
// "grain daemon" to retitle the standing session this run's task is the
// conversation of (pkg/ui's PUT /api/tasks/{id}/session-title) -- the
// same REST hop daemonStatus makes, for daemonStatus's reason: the
// settings row being written is the daemon's, and this process holds a
// transport into a sandbox and nothing else. Which session it lands on
// is fixed by -task at process start, so a run can only ever rename its
// own chat, and the daemon refuses the write for any task that is not a
// session.
type daemonSessionRenamer struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonSessionRenamer) RenameSession(ctx context.Context, title string) (mcp.SessionRenameReport, error) {
	renamed, err := d.client.RenameConsoleSessionOfTask(ctx, d.taskID, title)
	if err != nil {
		return mcp.SessionRenameReport{}, err
	}
	return mcp.SessionRenameReport{Title: renamed.Title}, nil
}

// daemonStagedAttachments holds a file this run attached by path in the
// daemon's store (pkg/ui's POST /api/tasks/{id}/staged-attachments) until
// its finish relays it -- another of the REST hops daemonStatus makes, for
// daemonStatus's reason: the row is the daemon's, and this process holds
// a transport into a sandbox and nothing else.
type daemonStagedAttachments struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonStagedAttachments) Stage(ctx context.Context, path string, a mcp.ResponseAttachment) error {
	staged, err := d.client.StageAttachment(ctx, d.taskID, path, ui.AttachmentUpload{
		Filename: a.Filename, ContentType: a.ContentType, Content: base64.StdEncoding.EncodeToString(a.Content),
	})
	if err != nil {
		return err
	}
	if !staged.Live {
		return errors.New("grain has already finished this run, so nothing is left to relay it")
	}
	return nil
}

// daemonWaitCredit implements mcp.WaitCreditReporter by asking a running
// "grain daemon" to credit this run's wall-clock deadline for the time it
// has spent blocked on CI (pkg/ui's POST /api/tasks/{id}/wait-credit) --
// one more of the REST hops the types above make, over the same client.
//
// The hop is about where the clock is. A run is cancelled by a timer in
// the daemon that dispatched it (orchestrator.RunWaitCredits); this
// process holds a transport into a sandbox and a copy of the deadline it
// was told at start. Moving that copy alone would be this process
// promising an agent time the thing that kills it does not agree exists,
// so the granting is asked for and the answer is adopted rather than
// assumed.
//
// Which run is credited is fixed by -task at process start, exactly as
// for daemonStatus: a run can only ever credit itself, and only for time
// it actually spent.
type daemonWaitCredit struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonWaitCredit) CreditWait(ctx context.Context, blocked time.Duration) (mcp.WaitCredit, error) {
	credit, err := d.client.CreditWait(ctx, d.taskID, blocked)
	if err != nil {
		return mcp.WaitCredit{}, err
	}
	return mcp.WaitCredit{
		Credited: time.Duration(credit.CreditedMS) * time.Millisecond,
		Live:     credit.Live,
	}, nil
}

// daemonTaskWaiter implements mcp.TaskWaiter over the daemon's
// POST /api/tasks/{id}/wait-on (ui.Client.WaitOn) and .../park-until
// (ui.Client.ParkUntil), for the task fixed at
// process start.
type daemonTaskWaiter struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonTaskWaiter) WaitOn(ctx context.Context, ids []string, park bool) ([]model.WaitTarget, error) {
	return d.client.WaitOn(ctx, d.taskID, ids, park)
}

func (d daemonTaskWaiter) ParkUntil(ctx context.Context, until time.Time) (time.Time, error) {
	return d.client.ParkUntil(ctx, d.taskID, until)
}

func (d daemonTaskWaiter) ResumeLater(ctx context.Context, when model.ResumeOn) (model.ResumeOn, error) {
	return d.client.ResumeLater(ctx, d.taskID, when)
}

// daemonNotifier implements mcp.OperatorNotifier over the daemon's
// POST /api/tasks/{id}/notify (ui.Client.AgentNotify) and
// POST /api/tasks/{id}/notes (ui.Client.PostTaskNote), for the task
// fixed at process start.
type daemonNotifier struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonNotifier) Notify(ctx context.Context, n mcp.OperatorNotification) (mcp.RaisedNotification, error) {
	req := ui.RaiseNotificationRequest{Subject: n.Subject, Title: n.Title, Body: n.Body, Severity: n.Severity, Task: n.Task}
	if n.ClearsWhen != nil {
		raw, err := json.Marshal(n.ClearsWhen)
		if err != nil {
			return mcp.RaisedNotification{}, err
		}
		var clears model.NotificationClear
		if err := json.Unmarshal(raw, &clears); err != nil {
			return mcp.RaisedNotification{}, fmt.Errorf("clears_when: %w", err)
		}
		req.ClearsWhen = &clears
	}
	resp, err := d.client.Notify(ctx, d.taskID, req)
	if err != nil {
		return mcp.RaisedNotification{}, err
	}
	n2 := resp.Notification
	return mcp.RaisedNotification{ID: n2.ID, Subject: n2.Subject, Severity: string(n2.Severity),
		Count: n2.RaiseCount, New: resp.New}, nil
}

func (d daemonNotifier) MessageTask(ctx context.Context, task, body string) (int64, error) {
	msg, err := d.client.PostTaskNote(ctx, d.taskID, task, body)
	return msg.ID, err
}

// grainCIReader is the verdict grain's own CI has recorded on this run's
// task, for the three tools that answer "is this pull request in good
// shape?" (mcp's grain_ci.go) -- or nil, which is silence and is the
// answer for almost every run.
//
// Two things have to be true for a run to be told anything about grain
// CI. There has to be a daemon to ask, because the verdict is a row on
// the task and this process holds no store handle -- the same missing
// handle daemonStatus and daemonChat work around. And the task has to
// hold the grain-ci capability, because that capability is what decides
// this branch is put through grain CI at all and therefore what decides
// the verdict counts: orchestrator.grainCIVerdictOf reads a task that
// does not hold it as silent however red its record, and a report that
// said otherwise would be a report disagreeing with the gate it is
// describing.
//
// nil is returned as a nil interface rather than a nil *daemonGrainCI,
// since what pkg/mcp switches on is the reader being absent.
func grainCIReader(client *ui.HTTPClient, taskID string, grants GrantNames) mcp.GrainCIReader {
	if client == nil || !grants.Has(grainci.CapabilityName) {
		return nil
	}
	return DaemonGrainCI{Client: client, TaskID: taskID}
}

// ciRepoScope is which repositories this run's list_ci_runs and
// get_ci_log may read (grain/task-172, pkg/mcp's ci_repo_scope.go).
//
// -pr-repo is the answer for an ordinary task: the repository whose
// branch this run is pushing to is the only one grain CI ever runs that
// branch in, and pull_request_status is already scoped to it. A task
// with no repository at all gets an empty scope, which reads as "no CI
// to see" rather than as "no scope configured, so all of it".
//
// self-debug is the wide grant, for the reason ci_repo_scope.go gives:
// it is already the grant that lets a task read grain's own record
// across every task in the deployment, and "is this job flaky
// everywhere" is that same question asked of CI. It is resolved here
// rather than in pkg/mcp because the grant names live in
// pkg/capability, which imports pkg/mcp.
//
// Only self-debug in its deployment-wide form widens it: deploymentWide
// is the daemon's ui.TaskDetail.DeploymentWide for this task. A team
// whose self-debug is scoped to its own work (grain/task-2555) reads its
// own task's repository here like any other run, since another team's
// CI is another team's work.
func CiRepoScope(prRepo string, grants GrantNames, deploymentWide []string) mcp.CIRepoScope {
	own := strings.TrimSpace(prRepo)
	wide := grants.Has(selfdebug.CapabilityName) &&
		slices.Contains(deploymentWide, selfdebug.CapabilityName)
	apiGrants := model.APIGrantsOf(grants)
	if wide || apiGrants.Allows("ci:read", "") {
		return mcp.CIRepoScope{Repo: own, AllRepos: true}
	}
	var extra []string
	for _, r := range apiGrants.Repos("ci:read") {
		if own == "" || !strings.EqualFold(r, own) {
			extra = append(extra, r)
		}
	}
	return mcp.CIRepoScope{
		Repo:  own,
		Reads: extra,
	}
}

// ciReadRepoScope is which repositories list_ci_runs, get_ci_log and
// get_ci_artifact may read when a run holds an api ci:read grant rather
// than grain-ci: only the repositories its ci:read grants name (plus all
// repositories for deployment-wide self-debug or an unscoped api:ci-read),
// never -pr-repo on its own when the grant was scoped elsewhere.
func ciReadRepoScope(prRepo string, grants GrantNames, deploymentWide []string) mcp.CIRepoScope {
	own := strings.TrimSpace(prRepo)
	wide := grants.Has(selfdebug.CapabilityName) &&
		slices.Contains(deploymentWide, selfdebug.CapabilityName)
	apiGrants := model.APIGrantsOf(grants)
	if wide || apiGrants.Allows("ci:read", "") {
		return mcp.CIRepoScope{Repo: own, AllRepos: true}
	}
	repos := apiGrants.Repos("ci:read")
	if own != "" && apiGrants.Allows("ci:read", own) {
		var extra []string
		for _, r := range repos {
			if !strings.EqualFold(r, own) {
				extra = append(extra, r)
			}
		}
		return mcp.CIRepoScope{Repo: own, Reads: extra}
	}
	return mcp.CIRepoScope{Reads: repos}
}

// daemonGrainCI implements mcp.GrainCIReader by reading this run's own
// task back from a running "grain daemon" (pkg/ui's GET /api/tasks/{id})
// -- the fifth REST hop this file makes, over the same client as the
// four above.
//
// The hop is about where the verdict is, exactly as daemonStatus's is
// about where the row is: grain CI writes its answer onto the task
// (model.CIStatus, projected as ui.TaskCIStatus), and this process holds
// a GitHub credential and a transport into a sandbox. Unlike the other
// four this one only reads, which is why it needs nothing but the task
// id to say which run it is answering about.
//
// It is read per answer rather than once at start, because the answer
// moves while the run works: grain CI is started by a reconciler once a
// pull request is open for the branch and concludes minutes later, so a
// copy taken when this process started would say "nothing" for every run
// that has not pushed yet -- which is all of them.
type DaemonGrainCI struct {
	Client *ui.HTTPClient
	TaskID string
}

func (d DaemonGrainCI) StartGrainCI(ctx context.Context, jobs []string) (ci.Run, error) {
	return d.Client.StartTaskCIRun(ctx, d.TaskID, jobs)
}

func (d DaemonGrainCI) RetriggerGrainCI(ctx context.Context, workflow, commit string, jobs []string, all bool) (ci.Run, error) {
	return d.Client.RetriggerTaskCIRun(ctx, d.TaskID, workflow, commit, jobs, all)
}

func (d DaemonGrainCI) GrainCIRun(ctx context.Context, id string) (ci.Run, error) {
	return d.Client.CIRun(ctx, id)
}

func (d DaemonGrainCI) FindGrainCIRuns(ctx context.Context, f ci.RunFilter, limit int) (ci.RunPage, error) {
	resp, err := d.Client.FindCIRuns(ctx, f, limit)
	if err != nil {
		return ci.RunPage{}, err
	}
	return ci.RunPage{Runs: resp.Runs, Matched: resp.Matched, Stored: resp.Stored}, nil
}

// GrainCISharedFaults reads the daemon's own grouping of the failures
// showing on more than one branch at once (pkg/ui's GET /api/ci/faults),
// and maps it onto pkg/mcp's vocabulary -- the sixth REST hop this file
// makes, for the reason all the others exist: the evidence is a store of
// every recent run's output and a GitHub credential, and this process
// holds neither.
func (d DaemonGrainCI) GrainCISharedFaults(ctx context.Context) ([]mcp.CIFault, error) {
	faults, err := d.Client.CISharedFaults(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.CIFault, 0, len(faults))
	for _, fault := range faults {
		reported := mcp.CIFault{SharedFault: fault.SharedFault}
		for _, fix := range fault.Fixes {
			reported.Fixes = append(reported.Fixes, mcp.CIFaultFix{
				PullRequest: fix.PullRequest,
				Task:        fix.Task,
				Title:       fix.Title,
				Files:       fix.Files,
			})
		}
		out = append(out, reported)
	}
	return out, nil
}

// DownloadGrainCIArtifact is get_ci_artifact's hop to the daemon's own
// GET /api/ci/runs/{id}/artifacts/{job}/{name} -- the seventh this file
// makes, over the same client as the others, and the one that streams
// rather than decodes: an artifact is a gzipped tar and the point of
// ui.HTTPClient.DownloadCIArtifact is that it reaches w without being
// held in memory on the way.
func (d DaemonGrainCI) DownloadGrainCIArtifact(ctx context.Context, ref ci.ArtifactRef, w io.Writer) (int64, error) {
	return d.Client.DownloadCIArtifact(ctx, ref, w)
}

func (d DaemonGrainCI) GrainCI(ctx context.Context) (*mcp.GrainCIVerdict, error) {
	detail, err := d.Client.GetTask(ctx, d.TaskID)
	if err != nil {
		return nil, err
	}
	if detail.CI == nil {
		// No run recorded against this branch. Nil rather than an error:
		// this is the ordinary state of a task whose branch has not been
		// through CI yet, and absence is never failure (mcp's
		// grain_ci.go, orchestrator.withGrainCI).
		return nil, nil
	}
	return &mcp.GrainCIVerdict{
		RunID:      detail.CI.RunID,
		Commit:     detail.CI.Commit,
		Conclusion: detail.CI.Conclusion,
		Detail:     detail.CI.Detail,
	}, nil
}

// chatPollInterval is how often daemonChat below re-reads the task's
// conversation while a chat turn is waiting for a person to answer.
//
// A second, which is not a tuning knob but the whole point: this is an
// interactive session, and the gap between somebody pressing send and
// their agent seeing it is this number. selfrepair.Confirm polls at five
// seconds for a confirmation somebody stops to think about; a
// conversation is not that, and a reply that takes a minute to arrive is
// not a conversation. The cost is one indexed read of task_comment per
// second per waiting run, over loopback, which is nothing beside a
// sandbox standing idle for the same second.
const chatPollInterval = 100 * time.Millisecond

// daemonChat implements mcp.Chatter by asking a running "grain daemon" to
// post what this run has to say into its own task's conversation (pkg/ui's
// POST /api/tasks/{id}/messages) and then reading that conversation back
// until a person answers (GET /api/tasks/{id}/messages?after=N) -- the
// same REST hop daemonStatus and daemonWaitCredit above make, over the
// same client.
//
// The hop is about where the conversation is, exactly as daemonStatus's
// is about where the row is: a task's chat is in the daemon's store, and
// this process holds a transport into a sandbox. What is different is
// that this one *waits*. pkg/capability/selfrepair's Confirm described
// this shape first and could never be called, because it needs
// *model.Store in-process and every framework that remains forks this
// process, which has no store handle. Two REST calls and a poll loop are
// what that missing handle is worth.
//
// The polling is done here rather than held open by the daemon on
// purpose. A long poll would be one HTTP request parked for the best part
// of an hour across a connection nothing keeps alive, to save reads that
// cost a millisecond each; this way a dropped request costs one second of
// latency and the next tick tries again -- the same reasoning
// orchestrator's addendaPoller and selfrepair.Confirm each apply to the
// identical read.
//
// Which conversation it lands in is fixed by -task at process start, so a
// run can only ever talk to the person watching its own task.
type daemonChat struct {
	client    *ui.HTTPClient
	taskID    string
	writeFile func(context.Context, map[string]any) mcp.Result
	// poll is chatPollInterval for every real server; a test shortens it.
	poll time.Duration
	// delivered is told about every reply this hands back, so the daemon
	// counts it as read (deliveredCursor). nil in a test that does not
	// ask.
	delivered *deliveredCursor
}

func (d daemonChat) Chat(ctx context.Context, message, summary string, attachments []mcp.ResponseAttachment, wait time.Duration) (mcp.ChatTurn, error) {
	uploads := make([]ui.AttachmentUpload, len(attachments))
	for i, attachment := range attachments {
		uploads[i] = ui.AttachmentUpload{
			Filename: attachment.Filename, ContentType: attachment.ContentType,
			Content: base64.StdEncoding.EncodeToString(attachment.Content),
		}
	}
	posted, err := d.client.PostTaskMessage(ctx, d.taskID, message, summary, uploads)
	if err != nil {
		return mcp.ChatTurn{}, err
	}
	if wait <= 0 {
		return mcp.ChatTurn{}, nil
	}
	deadline := time.Now().Add(wait)
	checkedPrePost := false
	pollOnce := func(firstPoll bool, waitSlice time.Duration) (mcp.ChatTurn, bool) {
		after := posted.ID
		needPrePost := !checkedPrePost
		if needPrePost {
			waitSlice = 0
			if d.delivered != nil {
				if cur := d.delivered.current(); cur < after {
					after = cur
				}
			} else if firstPoll {
				after = 0
			}
		}
		pollCtx, cancelPoll := context.WithDeadline(ctx, deadline)
		var (
			messages []ui.Message
			err      error
		)
		if waitSlice > 0 {
			messages, err = d.client.TaskMessagesWait(pollCtx, d.taskID, after, waitSlice)
		} else {
			messages, err = d.client.TaskMessages(pollCtx, d.taskID, after)
		}
		cancelPoll()
		if err != nil {
			return mcp.ChatTurn{}, false
		}
		if needPrePost {
			checkedPrePost = true
		}
		if len(messages) == 0 {
			return mcp.ChatTurn{}, false
		}
		var beforePost, afterPost []ui.Message
		for _, m := range messages {
			if m.ID > posted.ID {
				afterPost = append(afterPost, m)
			} else {
				beforePost = append(beforePost, m)
			}
		}
		beforePost = d.delivered.notByChat(beforePost)
		chosen := afterPost
		if len(chosen) == 0 {
			chosen = beforePost
		}
		if len(chosen) == 0 {
			return mcp.ChatTurn{}, false
		}
		placeMessageAttachments(ctx, d.client, d.taskID, chosen, d.writeFile)
		d.delivered.deliveredByChat(ctx, chosen)
		return mcp.ChatTurn{Reply: joinMessages(chosen)}, true
	}
	if turn, ok := pollOnce(true, 0); ok {
		return turn, nil
	}
	if time.Now().After(deadline) {
		return mcp.ChatTurn{}, nil
	}
	if d.poll <= 0 {
		for {
			if ctx.Err() != nil {
				return mcp.ChatTurn{}, ctx.Err()
			}
			rem := time.Until(deadline)
			if rem <= 0 {
				return mcp.ChatTurn{}, nil
			}
			waitSlice := min(rem, 5*time.Second)
			pollStart := time.Now()
			if turn, ok := pollOnce(false, waitSlice); ok {
				return turn, nil
			}
			if time.Now().After(deadline) {
				return mcp.ChatTurn{}, nil
			}
			if elapsed := time.Since(pollStart); elapsed < chatPollInterval {
				sleep := min(chatPollInterval-elapsed, time.Until(deadline))
				if sleep > 0 {
					timer := time.NewTimer(sleep)
					select {
					case <-ctx.Done():
						timer.Stop()
						return mcp.ChatTurn{}, ctx.Err()
					case <-timer.C:
					}
				}
			}
		}
	}
	interval := d.poll
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return mcp.ChatTurn{}, ctx.Err()
		case now := <-ticker.C:
			// A read that fails is not a wait that ends: the next tick is
			// moments away and will most likely succeed, where treating
			// one dropped request as "nobody is there" would end a
			// conversation over a blip. That covers a daemon restart too
			// (docs/non-disruptive-upgrade.md): this process is the CLI's,
			// not the daemon's, so it outlives an upgrade with the CLI,
			// and the first poll the new daemon answers reads everything
			// said since the run's own message, a reply posted after the
			// restart included. Each read is bounded by the wait's own
			// deadline, so the client's restart-gap retry (ui.RetryPolicy,
			// two minutes) cannot hold the turn past the time it said it
			// would wait.
			if turn, ok := pollOnce(false, 0); ok {
				return turn, nil
			}
			if now.After(deadline) {
				return mcp.ChatTurn{}, nil
			}
		}
	}
}

// joinMessages is what a person typed while this turn was waiting, as one
// reply: every message since the run's own, in the order they were
// written.
//
// All of them rather than the first, because somebody typing three short
// lines has said one thing in three messages, and answering only the
// first would leave the run acting on a third of what it was told.
func joinMessages(messages []ui.Message) string {
	bodies := make([]string, 0, len(messages))
	for _, m := range messages {
		bodies = append(bodies, formatMessageBody(m))
	}
	return strings.Join(bodies, "\n\n")
}

func placeMessageAttachments(ctx context.Context, client *ui.HTTPClient, taskID string, messages []ui.Message, writeFile func(context.Context, map[string]any) mcp.Result) {
	if writeFile == nil || client == nil {
		return
	}
	for _, m := range messages {
		for _, a := range m.Attachments {
			att, err := client.Attachment(ctx, taskID, a.ID)
			if err != nil {
				continue
			}
			path := fmt.Sprintf("attachments/%d-%s", a.ID, a.Filename)
			_ = writeFile(ctx, map[string]any{"file_path": path, "content": string(att.Content)})
		}
	}
}

func formatMessageBody(m ui.Message) string {
	var attLines []string
	for _, a := range m.Attachments {
		attLines = append(attLines, fmt.Sprintf("- ./attachments/%d-%s", a.ID, a.Filename))
	}
	body := m.Body
	if m.Author != "" && strings.TrimSpace(body) != "" {
		body = fmt.Sprintf("[%s]: %s", m.Author, body)
	}
	if len(attLines) == 0 {
		return body
	}
	attSection := "Files attached to this message are available in the sandbox:\n" + strings.Join(attLines, "\n")
	if strings.TrimSpace(body) == "" {
		if m.Author != "" {
			return fmt.Sprintf("[%s]\n%s", m.Author, attSection)
		}
		return attSection
	}
	return body + "\n\n" + attSection
}

// daemonAddenda implements mcp.AddendaReader by asking a running "grain
// daemon" what a person has said on this run's task since a given
// comment -- the same REST hop daemonStatus, daemonWaitCredit and
// daemonChat above make, and literally the same read as daemonChat's own
// poll (pkg/ui's /api/tasks/{id}/messages, ui.Client.MessagesSince), with
// agents=true (ui.Client.AddendaSince).
//
// The hop is about where the conversation is, exactly as daemonChat's
// is: the words are rows in the daemon's store, this process is a fork
// holding a transport into a sandbox, and a run in a sandbox reaches
// neither. What differs from chat is only who is waiting for whom --
// chat is the run asking a person a question and blocking on the answer,
// and this is a person interrupting a run that asked nothing.
//
// Human-authored comments, and other tasks' runs' messages to this one's
// agent (ui.Client.AddendaSince, POST /api/tasks/{id}/agent-messages),
// which is that read's own rule rather than anything decided here:
// grain's own automation posts on a task for reasons that are not
// somebody telling the run to do something differently, and reading
// those back to a run mid-turn would spend its context on notices about
// itself.
type daemonAddenda struct {
	client    *ui.HTTPClient
	taskID    string
	writeFile func(context.Context, map[string]any) mcp.Result
	// delivered is shared with this run's daemonChat, nil for a server
	// with no chat and no report to make (a test).
	delivered *deliveredCursor
}

func (d daemonAddenda) Addenda(ctx context.Context, after int64) ([]mcp.Addendum, error) {
	messages, err := d.client.TaskAddenda(ctx, d.taskID, after)
	if err != nil {
		return nil, err
	}
	// A message this run was already handed as a chat reply is not news
	// on the answer after it: the run has just read it.
	messages = d.delivered.notByChat(messages)
	placeMessageAttachments(ctx, d.client, d.taskID, messages, d.writeFile)
	addenda := make([]mcp.Addendum, 0, len(messages))
	for _, message := range messages {
		addenda = append(addenda, mcp.Addendum{ID: message.ID, Body: formatMessageBody(message)})
	}
	return addenda, nil
}

// Delivered implements mcp.AddendaAcknowledger: what the last poll found
// has gone into an answer, so the daemon can count it as read.
func (d daemonAddenda) Delivered(ctx context.Context, through int64) {
	d.delivered.advance(ctx, through)
}

// deliveredCursor is how much of its task's conversation this run has
// been shown, and the one place that tells the daemon so
// (POST /api/tasks/{id}/delivered, model.Observation.DeliveredCommentID).
//
// Two things hand a run a person's words -- an addendum on a tool result
// (daemonAddenda) and the reply to a chat call (daemonChat) -- and both
// report through this, because what the daemon is told has to be one
// mark that only moves forward. It is seeded from -addenda-after, which
// the daemon recorded itself when it built the prompt.
//
// A report that fails is not retried on its own: the mark is moved again
// by the next delivery, and until then the daemon simply thinks less has
// been read than has. That is the safe way round -- the cost is a fresh
// dispatch for a message the run did see, where the other way round is a
// person's message nobody ever answers.
type deliveredCursor struct {
	report func(ctx context.Context, through int64) error

	mu      sync.Mutex
	through int64
	// byChat is every message handed over as a chat reply, so that the
	// addenda poll on the same answer does not read them out again.
	byChat map[int64]bool
}

func newDeliveredCursor(client *ui.HTTPClient, taskID string, seed int64) *deliveredCursor {
	return &deliveredCursor{
		report: func(ctx context.Context, through int64) error {
			return client.RecordDelivered(ctx, taskID, through)
		},
		through: seed,
		byChat:  map[int64]bool{},
	}
}

// current returns the highest comment ID this cursor has recorded as delivered.
func (c *deliveredCursor) current() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.through
}

// advance moves the mark to through, if that is forward, and tells the
// daemon.
func (c *deliveredCursor) advance(ctx context.Context, through int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if through <= c.through {
		c.mu.Unlock()
		return
	}
	c.through = through
	c.mu.Unlock()
	if c.report != nil {
		_ = c.report(ctx, through)
	}
}

// deliveredByChat records messages a chat call is about to return as its
// reply, and moves the mark past them.
func (c *deliveredCursor) deliveredByChat(ctx context.Context, messages []ui.Message) {
	if c == nil || len(messages) == 0 {
		return
	}
	last := int64(0)
	c.mu.Lock()
	for _, m := range messages {
		c.byChat[m.ID] = true
		last = max(last, m.ID)
	}
	c.mu.Unlock()
	c.advance(ctx, last)
}

// notByChat is messages less any a chat call has already handed over.
func (c *deliveredCursor) notByChat(messages []ui.Message) []ui.Message {
	if c == nil {
		return messages
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := messages[:0:0]
	for _, m := range messages {
		if !c.byChat[m.ID] {
			kept = append(kept, m)
		}
	}
	return kept
}

// SandboxEnv collects the repeated -sandbox-env flag: NAME=VALUE entries
// a -sandbox-root server's run_command sets on top of its allowlist
// (mcp.NewSandboxTools), checked by mcp.ValidateSandboxEnv once
// they are all in.
type SandboxEnv []string

func (e *SandboxEnv) String() string { return strings.Join(*e, " ") }

func (e *SandboxEnv) Set(value string) error {
	*e = append(*e, value)
	return nil
}

// proposeRepos collects the repeated -propose-repo flag: the
// repositories this deployment works in, which a propose_task call may
// name in its `repo` argument (model.Store.WorkRepos, passed on by
// agent.ProposeRepoArgs). Most recently worked in first, which is the
// order the tool's description repeats them in.
//
// A flag.Value rather than one comma-separated string, for grantNames'
// reason: an owner/name is a name, and a run's arguments are something
// an operator reads in ps.
type ProposeRepos []string

func (r *ProposeRepos) String() string { return strings.Join(*r, " ") }

func (r *ProposeRepos) Set(value string) error {
	if value == "" {
		return fmt.Errorf("a repository cannot be empty")
	}
	*r = append(*r, value)
	return nil
}

// nameable is the list as propose_task should describe it: every entry
// that names a repository at all, spelled owner/name.
//
// An entry that does not parse is dropped with a warning rather than
// being fatal, in the spirit of everything else here: a malformed
// argument means one sentence of a description is shorter, not a run
// that cannot edit a file. It would also be nobody's fault but grain's
// -- these come off the store, not off a human -- so the line goes to
// stderr, where the daemon's subprocess plumbing carries it to an
// operator.
func (r ProposeRepos) Nameable() []string {
	var named []string
	for _, entry := range r {
		repo, err := model.ParseRepo(entry)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"grain mcpserver: -propose-repo %q is not an owner/name repository (%v); "+
					"propose_task will not name it\n", entry, err)
			continue
		}
		named = append(named, repo.String())
	}
	return named
}

// grantNames collects the repeated -grant flag: the capability grants
// this run's task holds, in the order a Framework wrote them
// (agent.GrantArgs). A flag.Value rather than one comma-separated
// string because a grant name is a name, and a list of them reads
// plainly in the arguments of a process an operator is looking at in ps.
type GrantNames []string

func (g *GrantNames) String() string { return strings.Join(*g, " ") }

func (g *GrantNames) Set(value string) error {
	if value == "" {
		return fmt.Errorf("a grant name cannot be empty")
	}
	*g = append(*g, value)
	return nil
}

// has reports whether the run holds the named grant, and is the whole of
// how a grant decides this process's tool roster.
func (g GrantNames) Has(name string) bool { return slices.Contains(g, name) }

// servedGrants are the grants this process does something with. A name
// outside it is a warning on stderr and no more, in the spirit of every
// other degraded mode here: an unrecognized grant means one tool set is
// missing, not that the run should lose the sandbox tools it touches
// every file through. The daemon's own subprocess plumbing carries the
// line to an operator, and the likeliest cause is a deployment
// mid-upgrade, whose daemon knows a grant name the binary it forked does
// not.
//
// grain-ci is in the list and serves no tools of its own: what it changes
// is what three tools already on the roster say (grainCIReader, and
// mcp's grain_ci.go). It is here because the alternative is a warning on
// every run of a repository this deployment puts through grain CI,
// saying that a grant which does reach this process does not.
// So is every capability registered from outside core with RunGrant set
// (capability.RunGrants), for the same reason: such a grant may be
// served by the run's agent framework -- agent/antigravity's pre-tool
// hook, say -- rather than by any tool of this process.
var servedGrants = []string{
	selfdebug.CapabilityName, grainlog.CapabilityName, grainserver.JournalRef, grainserver.JournalGrant, bootstrap.CapabilityName,
	grainci.CapabilityName, deploymenthealth.CapabilityName, grainserver.HealthRef, grainserver.HealthGrant,
	readissues.CapabilityName,
	dbaccess.CapabilityDBRead, dbaccess.CapabilityDBWrite,
	grainserver.SQLRef, grainserver.SQLRef + "={}", grainserver.SQLReadGrant, grainserver.SQLWriteGrant,
}

func warnUnknownGrants(grants GrantNames) {
	served := append(slices.Clone(servedGrants), capability.RunGrants()...)
	for _, name := range grants {
		if _, isAPI, err := ui.ParseAPIGrant(name); isAPI && err == nil {
			continue // served as generated tools (ui.AgentAPITools)
		}
		if grainserver.IsInProcessGrant(name) {
			continue
		}
		if model.APIAliasPreset(name) != "" {
			continue // bare alias for an api preset (ui.AgentAPITools)
		}
		if !slices.Contains(served, name) {
			fmt.Fprintf(os.Stderr,
				"grain mcpserver: -grant %q names no capability this binary serves tools for "+
					"(known: %s); this run gets no tools from it\n",
				name, strings.Join(served, ", "))
		}
	}
}

func GrantedDaemonTools(grants GrantNames, srcDir, secretsDir, stateDir, taskID string, client *ui.HTTPClient) []mcp.Tool {
	var tools []mcp.Tool
	if grainserver.HasJournal(grants) {
		tools = append(tools, grainlog.Tools(grainlog.Config{
			TaskID:   taskID,
			Redactor: grainLogRedactor(secretsDir),
		})...)
	}
	if grainserver.HasHealth(grants) {
		redactor := grainLogRedactor(secretsDir)
		var redact func(string) string
		if redactor != nil {
			redact = func(s string) string { lines, _ := redactor.Redact([]string{s}); return lines[0] }
		}
		var reader deploymenthealth.Reader
		if client != nil {
			reader = daemonDeploymentHealth{client: client}
		}
		tools = append(tools, deploymenthealth.Tools(deploymenthealth.Config{TaskID: taskID, StateDir: stateDir, Reader: reader, Redact: redact})...)
	}
	if grants.Has(bootstrap.CapabilityName) {
		tools = append(tools, bootstrap.PlaybookTools()...)
	}
	if grainserver.HasSQL(grants) {
		var executor dbaccess.DBExecutor
		if client != nil {
			executor = daemonDB{client: client, taskID: taskID}
		}
		tools = append(tools, dbaccess.Tools(dbaccess.Config{
			TaskID:     taskID,
			AllowWrite: grainserver.HasSQLWrite(grants),
			Executor:   executor,
		})...)
	}
	// The agent API: one tool per route of grain's own API whose scope
	// the task's api grants reach (docs/agent-api.md). Each call goes to
	// the daemon naming this task, which holds it to those same grants
	// (ui.ActingTaskHeader) -- so a tool listed here is an offer, and
	// the daemon is what decides. Registered last, so a hand-written
	// tool of the same name could never be shadowed by one, though the
	// api_ prefix keeps them apart anyway.
	if client != nil && taskID != "" {
		tools = append(tools, ui.AgentAPITools(client, taskID, grants)...)
	}
	return tools
}

// grainLogRedactor builds read_grain_log's redaction list out of the
// credential material this host actually holds, or nil if it could not
// -- which that tool answers every call with a refusal.
//
// Two stores, because this deployment keeps its credentials in two
// places on purpose (pkg/gitproxy/credentials.go's own note on why the
// ladder is files rather than rows): pkg/secrets, which is everything
// the UI's Secrets pane writes, and the git proxy's ladder, which is the
// GitHub tokens every sandbox's traffic is authorized with. A value
// missing from this list is not redacted by name -- the shape patterns
// are what stands behind it -- so both are worth reading.
//
// Nothing read here is ever returned to a run, logged, or written
// anywhere: the values exist in this process only to be recognized in a
// journal line and replaced. The count is reported; the values are not.
//
// An App-backed credential is skipped rather than resolved. Reading one
// mints a fresh installation token against GitHub, which is a network
// call this has no business making, and the token it would mint is not
// the one already in the journal anyway -- those are re-minted roughly
// hourly. Their shape (ghs_) is in the pattern list instead.
func grainLogRedactor(secretsDir string) *grainlog.Redactor {
	if secretsDir == "" {
		fmt.Fprintln(os.Stderr, "grain mcpserver: read_grain_log is unavailable this run: "+
			"no -grain-secrets-dir, so this process cannot tell which values to redact")
		return nil
	}
	known, err := KnownSecretValues(secretsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grain mcpserver: read_grain_log is unavailable this run: "+
			"this host's secrets under %s could not be read (%v), so nothing can be redacted "+
			"out of the journal\n", secretsDir, err)
		return nil
	}
	known = append(known, knownGitHubTokens(secretsDir)...)
	return grainlog.NewRedactor(known)
}

// knownSecretValues is every value in this deployment's own secret store
// (pkg/secrets), labelled by the name it is stored under. A store that
// holds nothing yet is not an error -- a fresh install, a deployment
// whose credentials are all in the ladder -- and yields no values.
func KnownSecretValues(secretsDir string) ([]grainlog.KnownValue, error) {
	cfg := secrets.Config{
		File:    filepath.Join(secretsDir, secrets.DefaultFileName),
		KeyFile: filepath.Join(secretsDir, secrets.DefaultKeyFileName),
	}
	// secrets.Open mints a key when it finds neither file, which is the
	// right thing for the daemon's own startup and the wrong thing here:
	// this is a short-lived fork of that daemon, and a process reading a
	// journal has no business creating the deployment's own secret key
	// -- least of all racing the process that is supposed to. A missing
	// key is a refusal instead, which read_grain_log answers as "this
	// run cannot read the journal".
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil, fmt.Errorf("no secrets key at %s: %w", cfg.KeyFile, err)
	}
	store := secrets.Open(cfg)
	// Check first: an unreadable store has to be a failure here, not an
	// empty list. "This host cannot decrypt its own secrets" and "this
	// host has no secrets" look identical downstream, and treating the
	// first as the second is how a journal full of a credential this
	// process could not name gets served as a redacted one.
	if err := store.Check(); err != nil {
		return nil, err
	}
	listed, err := store.List()
	if err != nil {
		return nil, err
	}
	var known []grainlog.KnownValue
	for _, info := range listed {
		for _, key := range info.Keys {
			name := info.Name + "/" + key
			value, err := store.Resolve(context.Background(), name)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", name, err)
			}
			known = append(known, grainlog.KnownValue{Label: "secret " + name, Value: value})
		}
	}
	return known, nil
}

// knownGitHubTokens is the static half of the git proxy's credential
// ladder -- see grainLogRedactor on why the App-backed half is skipped.
// A ladder that cannot be loaded is a warning and no values rather than
// a refusal: the secret store above is the one whose unreadability means
// this host has lost track of its own credentials, while a missing
// credentials.json is an ordinary deployment that has none.
func knownGitHubTokens(secretsDir string) []grainlog.KnownValue {
	dir := filepath.Join(secretsDir, "github")
	set, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grain mcpserver: read_grain_log will not redact GitHub tokens by "+
			"name this run: %s could not be read (%v); their shapes are still redacted\n", dir, err)
		return nil
	}
	var known []grainlog.KnownValue
	for _, name := range set.Names() {
		if set.IsApp(name) {
			continue
		}
		cred, ok := set.Get(name)
		if !ok || cred.Token == nil {
			continue
		}
		known = append(known, grainlog.KnownValue{Label: "github credential " + name, Value: *cred.Token})
	}
	return known
}

func boolCount(values ...bool) int {
	n := 0
	for _, value := range values {
		if value {
			n++
		}
	}
	return n
}

type daemonProposalFiler struct {
	client          *ui.HTTPClient
	parent, baseURL string
	mu              sync.Mutex
	filed           map[string]string
}

func (f *daemonProposalFiler) FileProposal(ctx context.Context, p mcp.ProposedTask, key string) (mcp.FiledProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, err := f.client.GetTask(ctx, f.parent)
	if err != nil {
		return mcp.FiledProposal{}, err
	}
	repo, base := "", ""
	if !p.NoRepo && p.ChangeTarget == nil {
		repo = parent.Task.Repo
	}
	if p.Repo != "" {
		repo = p.Repo
		for _, w := range parent.Task.Writes {
			if strings.EqualFold(w.Repo, p.Repo) {
				base = w.Base
				break
			}
		}
	}
	if p.Branch != "" {
		base = p.Branch
	}
	deps := make([]string, 0, len(p.DependsOn))
	for _, dep := range p.DependsOn {
		dep = strings.TrimPrefix(strings.TrimSpace(dep), "#")
		if real := f.filed[dep]; real != "" {
			dep = real
		}
		if dep != "" {
			deps = append(deps, dep)
		}
	}
	fields := make(map[string]ui.CustomFieldValue, len(p.CustomFields))
	for name, values := range p.CustomFields {
		fields[name] = ui.CustomFieldValue(values)
	}
	var writes []ui.WriteTarget
	if len(p.Writes) > 0 {
		writes = make([]ui.WriteTarget, 0, len(p.Writes))
		for _, w := range p.Writes {
			writes = append(writes, ui.WriteTarget{
				Repo: w.Repo,
				Base: w.Base,
			})
		}
	}
	// What the run asked for rather than what it is allowed: both this
	// and whether the proposal is filed approved at all are capped
	// against the parent's own permission by ui.Client.CreateTask, which
	// reads that permission off the stored parent task ProposedBy names
	// (model.AutoSubmits). Deciding it here as well would be a second
	// copy of that rule, computed from a REST response, on the side of
	// the wire the run can reach.
	auto := parent.Task.AutoMerge
	if p.AutoMerge != nil {
		auto = auto && *p.AutoMerge
	}
	// A changelist task has no pull request to merge: what the parent's
	// permission would have said about one does not reach it.
	if p.ChangeTarget != nil {
		auto = false
	}
	var caps *[]string
	if len(p.Capabilities) > 0 {
		c := append([]string(nil), p.Capabilities...)
		caps = &c
	}
	var priority *int
	if p.Priority != 0 {
		prio := p.Priority
		priority = &prio
	}
	// A local `id` from an earlier call in this run resolves here too: a
	// run that re-files the same proposal twice in one turn is replacing
	// the task it filed a moment ago, and it knows that one by the name
	// it gave it rather than by the number grain allocated.
	supersedes := strings.TrimPrefix(strings.TrimSpace(p.Supersedes), "#")
	if real := f.filed[supersedes]; real != "" {
		supersedes = real
	}
	created, err := f.client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: p.Title, Description: p.Body, Repo: repo, NoRepo: p.NoRepo, ChangeTarget: p.ChangeTarget, Base: base, Priority: priority, AutoMerge: auto,
		TemplateID: p.TemplateID, Fields: p.TemplateFields, AtFront: p.AtFront,
		DependsOn: deps, CustomFields: fields, Supersedes: supersedes,
		Reads: p.Reads, Writes: writes, ReviewTemplateID: p.ReviewTemplateID,
		ProposedBy: f.parent, ProposalKey: key, Capabilities: caps,
		Model: p.Model, AgentBackendID: p.Model, AgentFramework: p.AgentFramework,
		SandboxCPUs: p.SandboxCPUs, SandboxMemoryMB: p.SandboxMemoryMB,
		SandboxDiskGB: p.SandboxDiskGB, NetworkAccess: p.NetworkAccess,
		NothingNeededAction: p.NothingNeededAction,
		KeepRunning:         p.KeepRunning,
		Goal:                p.Goal,
		NoPort:              p.NoPort,
		FixesBaseBranch:     p.FixesBaseBranch,
		Origin:              p.Origin,
		DeferredUntil:       p.NotBefore,
		HoldForApproval:     p.HoldForApproval,
	})
	if err != nil {
		return mcp.FiledProposal{}, err
	}
	if p.ID != "" {
		if _, exists := f.filed[p.ID]; !exists {
			f.filed[p.ID] = created.ID
		}
	}
	return mcp.FiledProposal{
		ID:  created.ID,
		URL: "/tasks/" + created.ID,
		// Read back off the filed task rather than repeated from what
		// was asked for: whether the supersede happened is the daemon's
		// answer (model.SupersedeRefusal), and the link it wrote is the
		// only honest evidence of it on this side of the wire.
		Superseded:     created.Supersedes,
		SupersedeMoved: created.SupersedeMoved,
		SupersedeKept:  supersedeKept(created.SupersedeKept),
		// Approval rather than state: a proposal filed deferred
		// (not_before) reads "deferred" either way, and only its
		// approval says whether a human still has to look at it.
		Queued: filedApproved(created),
		// The daemon's own account of why it is not queued, which only it
		// can give: it is the side that read the parent's permission.
		Held:         created.HeldBy != "",
		ApprovalNote: created.ApprovalNote,
		Parked:       filedParked(created),
	}, nil
}

// filedApproved is whether a proposal CreateTask just filed carries an
// approval. Task.Approved says so whenever it is set; it is omitted when
// false, so the state stands in for it only as far as it can -- queued
// or running is a task that was approved, and every other state
// (proposed, deferred, and awaiting_reply above all, which filing parks a
// task in whatever its approval: a repo off the target repo list) proves
// nothing either way and is read as unapproved. Reading those as queued
// told a run that had held its proposal that the hold had not worked
// (grain/task-2050).
func filedApproved(created ui.Task) bool {
	return created.Approved ||
		created.State == model.StateQueued || created.State == model.StateRunning
}

// filedParked is why filing parked a proposal awaiting a reply, or "".
// The daemon's own comment when it reports one (Task.ParkNote); a daemon
// that does not still shows the park in the state, and is relayed as a
// park with the thread named as the place to read why.
func filedParked(created ui.Task) string {
	if created.ParkNote != "" {
		return created.ParkNote
	}
	if created.State == model.StateAwaitingReply {
		return "filing parked it awaiting a reply; grain's comment on its thread says what lifts that."
	}
	return ""
}

// mcpGitHubPins is the daemon's own answer to where a tunnelled MCP
// server may send this deployment's GitHub credential: the data directory
// the credential ladder is loaded from, the git host the REST client is
// built against, and whether it may be reached without TLS.
//
// A server forked locally (mcpserver, reading stdin) takes its flags from
// the Framework that forked it, which built them from the daemon's own
// config. One arriving over the executor tunnel (modelexec.Config.ServeMCP)
// takes them from whoever holds an executor token, and its first
// pull_request_status call sends the real credential in an Authorization
// header -- so without these pins, -github-host pointed at a server of the
// caller's choosing plus -github-insecure-http was enough to read the
// credential straight off the wire (bwsalmon/grain#2383).
type mcpGitHubPins struct {
	dataDir            string
	githubHost         string
	githubInsecureHTTP bool
}

// check refuses any value the daemon did not configure itself. It runs on
// the parsed flags rather than the raw args, so every spelling the flag
// package accepts (-x v, --x=v, -x=true) is held to the same rule.
// An unset -data-dir stays allowed: it loads no credential at all.
func (p mcpGitHubPins) check(dataDir, githubHost string, insecureHTTP bool) error {
	wantHost := p.githubHost
	if wantHost == "" {
		wantHost = "github.com"
	}
	if !strings.EqualFold(strings.TrimSpace(githubHost), wantHost) {
		return fmt.Errorf("grain mcpserver: -github-host %q is not this deployment's GitHub host", githubHost)
	}
	if insecureHTTP && !p.githubInsecureHTTP {
		return errors.New("grain mcpserver: -github-insecure-http is not enabled on this deployment")
	}
	if dataDir != "" && (p.dataDir == "" || filepath.Clean(dataDir) != filepath.Clean(p.dataDir)) {
		return fmt.Errorf("grain mcpserver: -data-dir %q is not this deployment's data directory", dataDir)
	}
	return nil
}

// serveMCPServerPinned is serveMCPServer with the GitHub-credential flags
// held to pins when pins is non-nil; a mismatch refuses to serve at all.
func ServeMCPServerPinned(in io.Reader, out io.Writer, args []string, pins *mcpGitHubPins) error {
	fs := flag.NewFlagSet("grain mcpserver", flag.ContinueOnError)
	root := fs.String("sandbox-root", "",
		"directory the local sandbox tools are confined to (mutually exclusive with -granule-vm)")

	container := fs.String("container", "", "name of a local Docker sandbox container to run tools in")
	k8sPod := fs.String("k8s-pod", "", "name of a Kubernetes sandbox pod to run tools in, over `kubectl exec` (with -k8s-namespace and -workspace)")
	k8sNamespace := fs.String("k8s-namespace", "", "namespace of -k8s-pod; empty is kubectl's own default")
	k8sKubeconfig := fs.String("k8s-kubeconfig", "", "kubeconfig path passed to kubectl when reaching -k8s-pod")
	k8sGranule := fs.Bool("k8s-granule", false, "reach -k8s-pod's guest VM through `granule exec --` over `kubectl exec`")
	granuleVM := fs.String("granule-vm", "",
		"name of a granule-managed VM (bwsalmon/granule) to run the sandbox tools against over SSH, "+
			"instead of a local directory (mutually exclusive with -sandbox-root)")
	sshUser := fs.String("ssh-user", "", "username to SSH into -granule-vm as (required with -granule-vm)")
	execKey := fs.String("exec-key", "", "path, *inside -granule-vm's container*, of the private key `granule exec` authenticates to the guest with. Optional: left unset, `granule exec` uses the keypair `granule run` generates for that guest at boot, which is what a stock bwsalmon/granule guest image authorizes (see its internal/guestkey). Set it only for a custom guest image that authorizes a key of its own instead.")
	workspace := fs.String("workspace", "",
		"working directory run_command/read_file/edit_file/write_file operate in on -granule-vm (required with -granule-vm)")

	dataDir := fs.String("data-dir", "",
		"grain's own data directory on this controller, holding secrets/github -- the credential "+
			"pull_request_status reads GitHub with. Required with -pr-repo.")
	prRepo := fs.String("pr-repo", "",
		"owner/name of the repository pull_request_status and wait_for_checks report on. Unset "+
			"leaves both tools registered but answering that this run has no repo, which is what "+
			"a task with no target really does have.")
	prBranch := fs.String("pr-branch", "",
		"branch within -pr-repo pull_request_status and wait_for_checks report on -- this run's "+
			"own branch, and the only one they can ever read (required with -pr-repo)")
	prBase := fs.String("pr-base", "",
		"branch -pr-branch merges into, which base_branch_status reads CI for so a run can tell "+
			"a failure its own change caused from one it inherited (model.Task's own Base). "+
			"Unset -- what an ordinary task has -- that tool reads -pr-repo's default branch "+
			"instead, which is the base such a task really has.")
	prRepair := fs.Bool("pr-repair", false,
		"mark this run as the merge queue's repair of -pr-branch, which changes what "+
			"base_branch_status tells it about breakage it inherited: a repair has to fix a "+
			"failing check whether or not its own commits caused it, where every other run is "+
			"told to leave the base's own breakage alone (orchestrator.mergeRepairRun).")
	prIgnoreGitHubCI := fs.Bool("pr-ignore-github-ci", false,
		"say that -pr-repo has declared GitHub's check runs are not a source of verdicts for "+
			"it -- grain CI (.grain/workflows) is its gate (model.RepoConfig.IgnoreGitHubCI). "+
			"pull_request_status and base_branch_status then say so instead of listing checks "+
			"the merge queue is not reading and does not wait on.")
	prBaseIncident := fs.String("pr-base-incident", "",
		"notice of an active incident on the base branch, if any")
	prNumber := fs.Int("pr-number", 0,
		"daemon-pinned external pull request number within -pr-repo (from model.LinkExternalPR) "+
			"that pull_request_status, reply_to_pull_request_comment and resolve_review_thread "+
			"scope to when -pr-branch has no open pull request of its own")
	prCredential := fs.String("pr-credential", "",
		"name of the credential in -data-dir's secrets/github that this run's branch is pushed "+
			"with (its team's git connection for -pr-repo, or its own github-credential grant), "+
			"which pull_request_status, base_branch_status, the issue tools and query_merge_queue "+
			"then read GitHub as. Unset, they read with whatever the deployment's owner/repo "+
			"ladder (credentials.json) names, which on a deployment whose credentials are all "+
			"team connections is no credential at all.")
	githubHost := fs.String("github-host", "github.com",
		"git host whose REST API pull_request_status reads (github.APIHost maps it to the API host)")
	githubInsecureHTTP := fs.Bool("github-insecure-http", false,
		"reach -github-host over plain HTTP instead of HTTPS -- for a local mock GitHub in a "+
			"live test, never for a real deployment")

	server := fs.String("server", "",
		"base URL of the \"grain daemon\" this server's run was dispatched by, e.g. http://127.0.0.1:4242 "+
			"-- with -task, adds the open_pull_request, recreate_sandbox and update_status tools, each "+
			"of which asks that daemon to act on the run's own task rather than acting from here")
	taskID := fs.String("task", "",
		"id of the task this server's run belongs to (required with -server)")

	var sandboxEnvList SandboxEnv
	fs.Var(&sandboxEnvList, "sandbox-env",
		"NAME=VALUE, repeatable, only with -sandbox-root: an environment variable run_command sets on top "+
			"of its allowlist (mcp.NewSandboxTools). Passed by the Framework that forked this process "+
			"from the daemon's -host-sandbox-env (agent.RunConfig.SandboxEnv); HOME is refused, since it stays "+
			"pinned to the sandbox root. Its values sit on this process's argv, readable by every user on the "+
			"host, so a Framework passes -sandbox-env-file instead; this is for running the server by hand")
	sandboxEnvFile := fs.String("sandbox-env-file", "",
		"path of a 0600 file of NUL-terminated NAME=VALUE entries (mcp.WriteSandboxEnvFile), only with "+
			"-sandbox-root: what -sandbox-env sets, without the values on this process's argv. Passed by the "+
			"Framework that forked this process (agent.RunConfig.SandboxEnvFile), naming the file the daemon "+
			"wrote its -host-sandbox-env to")

	var sandboxHideList SandboxEnv
	fs.Var(&sandboxHideList, "sandbox-hide",
		"absolute path, repeatable, only with -sandbox-root: a file or directory run_command runs every "+
			"command with hidden, under bubblewrap, refusing to run one where it cannot (mcp.NewHostSandboxTools). "+
			"Passed by the Framework that forked this process (agent.RunConfig.SandboxHide): a single-tenant "+
			"daemon's data directory, which holds its access code")

	var proposeRepoList ProposeRepos
	fs.Var(&proposeRepoList, "propose-repo",
		"owner/name of a repository this deployment works in, which a propose_task call may "+
			"name in its `repo` argument; repeat the flag for each, most recently worked in "+
			"first. They are listed in that argument's own description, so a run can see which "+
			"repositories are on offer while it is deciding what to write there. Passed by the "+
			"Framework that forked this process from the list the daemon read out of its store "+
			"(model.Store.WorkRepos, agent.RunConfig.ProposeRepos). Unset, the description names "+
			"the rule without the list, exactly as it read before this flag existed.")

	var grants GrantNames
	fs.Var(&grants, "grant",
		"name of a capability grant this run's task holds, whose tools this server should serve; "+
			"repeat the flag for each. \"self-debug\" serves the four task readers and read_grain_metrics "+
			"over -server, along with the api:self-debug preset tools (api:tasks:read and api:deployment:read); "+
			"\"grain-log\" serves read_grain_log over this host's journal, "+
			"redacted with -grain-secrets-dir; \"bootstrap-playbooks\" serves list_bootstrap_playbooks/"+
			"read_bootstrap_playbook off the playbooks embedded in this binary. Passed "+
			"by a Framework from the grants on the run's own task (agent.RunConfig.Grants, "+
			"agent.GrantArgs).")
	grainSrcDir := fs.String("grain-src-dir", "",
		"directory holding grain's own source, read-only (pkg/cli/daemon.go's sourceDir: the copy "+
			"baked into the deployment image, or -upgrade-src-dir's checkout).")
	grainStateDir := fs.String("grain-state-dir", "", "controller-local synchronized deployment state repository; only used with -grant deployment-health")
	grainSecretsDir := fs.String("grain-secrets-dir", "",
		"directory holding this deployment's own secrets (cmd/grain's secretsDir: -data-dir/secrets), "+
			"read on this host so that read_grain_log can redact those values out of the journal "+
			"before returning any of it. Only used with -grant grain-log, and never served to a run "+
			"-- what it buys is the ability to recognize a credential in a log line and remove it. "+
			"Unset, or unreadable, read_grain_log refuses every call rather than answering with an "+
			"unredacted journal.")

	interactive := fs.Bool("interactive", false,
		"this run's task is a live chat somebody is sitting with (model.Task.Interactive, "+
			"passed on by the Framework that forked this process -- agent.InteractiveArgs). "+
			"With -server/-task it adds the chat tool: the run says something in the task's "+
			"own conversation and blocks until a person answers, inside one tool call. It is "+
			"gated because the tool promises somebody is there: on a background run every "+
			"call would stand still for an hour before finding out nobody was.")

	addendaAfter := fs.Int64("addenda-after", 0,
		"the id of the last comment on this run's task already quoted in the run's prompt "+
			"(orchestrator.commentThreadSection), passed on by the Framework that forked this "+
			"process (agent.AddendaArgs). With -server/-task, anything a person posts on that "+
			"task after it rides back to the run appended to its next tool result, so a "+
			"correction typed while the run is in flight is read within the turn rather than "+
			"at the task's next dispatch. Left at zero on a task whose prompt quotes no "+
			"conversation, which is every first dispatch.")

	runDeadline := fs.String("run-deadline", "",
		"RFC3339 time at which grain cancels the run this server serves -- the deadline on "+
			"the context orchestrator.RunDispatch gives framework.Run, passed on by the "+
			"Framework that forked this process (agent.RunDeadlineArgs). Set, every tool "+
			"result carries how much of it is left once the run is inside "+
			"mcp.RunDeadlineNoticeWindow of it; unset, results read exactly as they "+
			"otherwise would.")
	toolBudget := fs.Bool("tool-budget", false,
		"count this run's tool calls and result bytes against a durable judgement task's per-wake "+
			"budget (mcp.ToolBudgetSoftCalls and the rest, docs/design/durable-tasks.md#DT-13), passed "+
			"on by the Framework that forked this process (agent.GrantArgs, for "+
			"agent.RunConfig.ToolBudget). Unset, nothing is counted.")
	toolBudgetPipelineCursor := fs.Bool("tool-budget-pipeline-cursor", false,
		"with -tool-budget: this run is a pipeline steward wake, whose handover the budget's notice asks "+
			"to carry the pipeline-changes cursor (agent.RunConfig.ToolBudgetPipelineCursor)")
	recordedTools := fs.String("recorded-tools", "",
		"path to append full tool call records (JSON lines) as tools run")
	var localScriptsList LocalScriptsFlags
	fs.Var(&localScriptsList, "local-scripts",
		"NAME=PATH, repeatable: a local-scripts declaration this run is served as local_<script> tools, "+
			"snapshotted when this process starts (agent.RunConfig.LocalScripts, docs/local-scripts.md)")
	localScriptsEnvFile := fs.String("local-scripts-env-file", "",
		"path of a 0600 JSON file of the secrets -local-scripts' scripts are delivered, by secret name "+
			"(localscripts.WriteCredentialsFile); never the values themselves in argv")
	surface := fs.String("mcp-surface", "",
		"which tools to serve: \"control\" (the sandbox tools, the run-ending and escalation tools and the "+
			"model-only grants' tools; everything else is a `grain` CLI command inside the sandbox) or \"full\" "+
			"(every tool, for a client that cannot shell out and for tests). Empty is control when a sandbox "+
			"flag is set, full otherwise (docs/design/sandbox-first-agent-surface.md §6)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *surface == "" {
		*surface = os.Getenv(MCPSurfaceEnv)
	}
	switch *surface {
	case "", MCPSurfaceControl, MCPSurfaceFull:
	default:
		return fmt.Errorf("grain mcpserver: -mcp-surface %q is neither %q nor %q", *surface, MCPSurfaceControl, MCPSurfaceFull)
	}
	if pins != nil {
		if err := pins.check(*dataDir, *githubHost, *githubInsecureHTTP); err != nil {
			return err
		}
	}

	if (len(sandboxEnvList) > 0 || *sandboxEnvFile != "") && *root == "" {
		// The other transports run their commands in a guest whose
		// environment is its own; the operator's workstation variables
		// would name sockets and paths that are not there.
		return errors.New("grain mcpserver: -sandbox-env and -sandbox-env-file are only for -sandbox-root")
	}
	if *sandboxEnvFile != "" {
		fromFile, err := mcp.ReadSandboxEnvFile(*sandboxEnvFile)
		if err != nil {
			return fmt.Errorf("grain mcpserver: -sandbox-env-file: %w", err)
		}
		sandboxEnvList = append(sandboxEnvList, fromFile...)
	}
	if len(sandboxHideList) > 0 && *root == "" {
		return errors.New("grain mcpserver: -sandbox-hide is only for -sandbox-root")
	}
	for _, p := range sandboxHideList {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("grain mcpserver: -sandbox-hide %q is not an absolute path", p)
		}
	}
	if err := mcp.ValidateSandboxEnv(sandboxEnvList); err != nil {
		return fmt.Errorf("grain mcpserver: -sandbox-env: %w", err)
	}

	var tools []mcp.Tool
	switch {
	case boolCount(*granuleVM != "", *root != "", *container != "", *k8sPod != "") != 1:
		return errors.New("grain mcpserver: exactly one of -sandbox-root, -granule-vm, -container, or -k8s-pod is required")
	case *k8sPod != "":
		if *workspace == "" {
			return errors.New("grain mcpserver: -k8s-pod needs -workspace")
		}
		kubectlRunner := &mcp.KubectlExecRunner{
			Kubeconfig: *k8sKubeconfig,
			Namespace:  *k8sNamespace,
			Pod:        *k8sPod,
			Container:  k8s.SandboxContainer,
		}
		if *k8sGranule {
			// Into the guest, not the pod -- see
			// orchestrator.granuleExecPrefix, which this repeats.
			kubectlRunner.Through = []string{"granule", "exec", "--"}
		}
		tools = mcp.NewSSHSandboxTools(kubectlRunner, *workspace)
	case *container != "":
		tools = mcp.NewSSHSandboxTools(&mcp.ContainerExecRunner{Container: *container}, "/home/agent")
	case *granuleVM != "":
		var err error
		tools, err = granuleSandboxTools(*granuleVM, *sshUser, *execKey, *workspace)
		if err != nil {
			return err
		}
	case *root != "":
		info, err := os.Stat(*root)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("grain mcpserver: -sandbox-root %q is not a directory: %v", *root, err)
		}
		tools = mcp.NewHostSandboxTools(*root, sandboxHideList, sandboxEnvList...)
	}

	registry := mcp.NewRegistry()
	registry.AnnounceRecordedTools(*recordedTools)
	registry.AnnounceDeadline(ParsedRunDeadline(*runDeadline), nil)
	if *toolBudget {
		registry.AnnounceToolBudget()
	}
	registry.Register(tools...)
	// The sink is a throwaway because nothing reads an escape-hatch call
	// back out of *this* process: orchestrator.ProcessResult recovers
	// ask_question, request_secret, comment_on_issue and propose_task from
	// agent.Result.ToolCalls once the run finishes, and relays each one
	// for real. The sink is here so a call answers with a real Result
	// rather than being a silent no-op, and the tools' own text describes
	// the relay rather than this sink -- see mcp.NewMockTools.
	//
	// -propose-repo is the one thing about those five tools this dispatch
	// gets to say: which repositories a proposal may be filed against,
	// which only the daemon's store knows and which propose_task's own
	// `repo` argument could otherwise only describe in the abstract.
	escapeSink := &mcp.MockSink{}
	registry.Register(mcp.NewMockTools(escapeSink, proposeRepoList.Nameable())...)
	// open_pull_request, recreate_sandbox and update_status are the three
	// tools here that act while the run is still going, and all three do
	// it by asking the daemon rather than from this process, so all three
	// are registered only when this process was actually told which
	// daemon and which task it serves -- a run driven from a bare `grain
	// mcpserver -sandbox-root` (pkg/mcp's own tests, tests/e2e/) has no
	// daemon to ask and is better off not advertising tools that could
	// only ever refuse.
	//
	// The same pair is what lets every tool call narrate itself onto that
	// row -- not a tool of its own, but a line written from the name of
	// whatever the run is calling, and from run_command's own
	// description where it wrote one.
	//
	// The client is built before the pull-request tools below rather than
	// alongside the tools that use it, because one of those reads goes
	// through it too: grain CI's verdict on this run's own branch is a
	// row on the task, so pull_request_status asks the daemon for it
	// (daemonGrainCI) exactly as update_status asks the daemon to write
	// one.
	var client *ui.HTTPClient
	var taskDetail ui.TaskDetail
	switch {
	case *server != "" && *taskID == "":
		return errors.New("grain mcpserver: -task is required with -server")
	case *server == "" && *taskID != "":
		return errors.New("grain mcpserver: -server is required with -task")
	case *server != "":
		client = ui.NewHTTPClient(*server)
		var err error
		taskDetail, err = client.GetTask(context.Background(), *taskID)
		if err != nil {
			return fmt.Errorf("grain mcpserver: fetching task %s: %w", *taskID, err)
		}
		grants = GrantNames(taskDetail.Task.Capabilities)
	}
	warnUnknownGrants(grants)

	// An attachment comment_on_issue or ask_question is given by path is
	// read out of this run's sandbox when the call is made, and held in
	// the daemon's store until the run's finish relays it: the call's
	// arguments are all the finish gets back, and they carry the path,
	// not the bytes. With no daemon to hold it, attaching by path is
	// refused rather than read for nothing.
	if client != nil {
		escapeSink.SetAttachmentFiles(mcp.AttachmentFiles{
			Read:  mcp.SandboxFileReaderOf(tools),
			Stage: daemonStagedAttachments{client: client, taskID: *taskID}.Stage,
		})
	}

	// Unlike the escape hatches, this one acts within the run rather than
	// after it: it really does read GitHub, from this process, on the
	// controller. See the file's doc comment.
	registry.Register(pullRequestToolsWithNumber(*dataDir, *githubHost, *githubInsecureHTTP,
		*prRepo, *prBranch, *prBase, *prCredential, *prRepair, *prIgnoreGitHubCI, *prBaseIncident,
		*prNumber, grainCIReader(client, *taskID, grants))...)

	var queueReader mcp.MergeQueueReader
	if client != nil {
		queueReader = daemonMergeQueue{client: client,
			defaultBranch: githubDefaultBranchReader(*dataDir, *githubHost, *githubInsecureHTTP, *prCredential)}
	}
	registry.Register(mcp.NewMergeQueueTools(queueReader)...)

	// revert_task and revert_merge are on every run's roster, with no
	// grant, deliberately: all they do is commit a git revert in this
	// run's own -sandbox-root, the directory run_command is already a
	// shell in, and push nothing. Without -sandbox-root (a granule VM or a
	// container, whose workspace is not on this host) they have no
	// reverter and refuse every call -- never running git on the
	// controller over a tree the agent wrote.
	var reverter mcp.TaskReverter
	if *root != "" {
		reverter = mcp.NewGitTaskReverter(*root)
	}
	registry.Register(mcp.NewRevertTools(reverter)...)

	if client != nil {
		status := daemonStatus{client: client, taskID: *taskID}
		registry.Register(mcp.NewOpenPullRequestTools(
			DaemonPullRequests{Client: client, TaskID: *taskID})...)
		registry.Register(mcp.NewRecreateSandboxTools(
			daemonSandbox{client: client, taskID: *taskID})...)
		registry.Register(mcp.NewStatusTools(status)...)
		// rename_session is the one tool here that depends on what the
		// task *is* rather than on what it holds: only a standing chat
		// session (model.Task.IsConsoleSession) has a session title to
		// write, so only a session's run is offered it. The wire shape
		// says which session a task is (ui.Task.ConsoleSession) and
		// whether it is interactive, which is what is checked here; the
		// daemon applies the full predicate on the write
		// (ui.Client.RenameConsoleSessionOfTask refuses any other
		// task), and this gate is so an ordinary run is not shown a
		// tool that could only ever refuse.
		// propose_config_change is a grain/config writer's: the director,
		// as grain_config.console_sessions declares it, or any task
		// somebody granted grain/config as a write target.
		if writesConfigRepo(taskDetail.Task) {
			registry.Register(mcp.NewProposeConfigChangeTools(
				daemonConfigChanges{client: client, taskID: *taskID}, *taskID, tools)...)
		}
		if taskDetail.Task.ConsoleSession != "" && taskDetail.Task.Interactive {
			registry.Register(mcp.NewRenameSessionTools(
				daemonSessionRenamer{client: client, taskID: *taskID})...)
		}
		// wait_for_task asks the daemon about other tasks, and parks
		// this one on them, as -task: a run can only ever make its own
		// task wait. The unpushed-work guard runs git through this
		// run's own run_command, wherever its sandbox is.
		registry.Register(mcp.NewWaitForTaskTools(
			daemonTaskWaiter{client: client, taskID: *taskID}, mcp.SandboxUnpushedWork(tools))...)
		// resume_later is the general form of wait_for_task's parks, and
		// offered to every run the same way.
		registry.Register(mcp.NewResumeLaterTools(
			daemonTaskWaiter{client: client, taskID: *taskID}, mcp.SandboxUnpushedWork(tools))...)
		// notify_operator and message_task are every run's, with no
		// grant: the daemon applies their rules for -task.
		registry.Register(mcp.NewNotifyTools(daemonNotifier{client: client, taskID: *taskID})...)
		if grants.Has(grainci.CapabilityName) {
			taskBranch := strings.TrimSpace(*prBranch)
			if taskBranch == "" {
				taskBranch = strings.TrimSpace(taskDetail.Task.Branch)
			}
			registry.Register(mcp.NewGrainCIWaitTools(
				DaemonGrainCI{Client: client, TaskID: *taskID}, CiRepoScope(*prRepo, grants, taskDetail.DeploymentWide), taskBranch)...)
			registry.Register(mcp.NewRunCIJobTools(
				DaemonGrainCI{Client: client, TaskID: *taskID}, CiRepoScope(*prRepo, grants, taskDetail.DeploymentWide))...)
			// get_ci_artifact writes what it downloads onto this host's
			// disk, the same way revert_task does -- so, like that pair,
			// it needs a local -sandbox-root and has nothing to write
			// through for a granule VM or a container, whose workspace is
			// not on this host.
			if *root != "" {
				registry.Register(mcp.NewGrainCIArtifactTools(
					DaemonGrainCI{Client: client, TaskID: *taskID}, CiRepoScope(*prRepo, grants, taskDetail.DeploymentWide), *root, taskBranch)...)
			}
		} else if model.APIGrantsOf(grants).Has("ci:read") {
			// A run holding an api ci:read grant (for instance, a GitHub
			// workflow-run review task on a ci_mode: github repository)
			// reads synthesized and stored CI runs through list_ci_runs and
			// get_ci_log without replacing GitHub's wait_for_checks or
			// advertising grain CI's run_ci_job / retrigger_ci.
			taskBranch := strings.TrimSpace(*prBranch)
			if taskBranch == "" {
				taskBranch = strings.TrimSpace(taskDetail.Task.Branch)
			}
			readScope := ciReadRepoScope(*prRepo, grants, taskDetail.DeploymentWide)
			registry.Register(mcp.NewGrainCIReadTools(
				DaemonGrainCI{Client: client, TaskID: *taskID}, readScope, taskBranch)...)
			if *root != "" {
				registry.Register(mcp.NewGrainCIArtifactTools(
					DaemonGrainCI{Client: client, TaskID: *taskID}, readScope, *root, taskBranch)...)
			}
		}
		// A task whose change is a CL has no pull request and no grain CI:
		// pull_request_status and wait_for_checks answer from the CL's own
		// presubmit through the registered change.Checks, and grain CI's
		// retrigger_ci, run_ci_job and get_ci_log refuse in its words
		// (changelists.md §3). Registered last, in place of those names.
		if taskDetail.Task.ChangeTargetKind != "" || len(taskDetail.Task.Changelists) > 0 {
			registry.Register(mcp.NewChangeChecksTools(DaemonChangeChecks{Client: client, TaskID: *taskID})...)
			// Its review threads are answered on the CL, as drafts
			// published when the run ends (changelists.md §5).
			registry.Register(mcp.NewChangeReviewTools(DaemonChangeReview{Client: client, TaskID: *taskID})...)
		}
		// Not a tool either: the same row, written the way that costs
		// the run nothing. With a daemon to tell, every call this
		// registry answers first puts a line on the task's row saying
		// what the run is doing -- run_command's own "description" where
		// it wrote one, a phrase from the tool's name otherwise -- so a
		// run reading source or blocked in a wait narrates itself
		// without spending a turn on it (mcp.Registry.AnnounceActivity).
		// Announced on the registry rather than wrapped around a roster
		// so that it covers the tools registered below it as well as the
		// ones above.
		registry.AnnounceActivity(status)
		// Not a tool: the one thing this pair turns on that the agent
		// never calls. Time this run spends blocked in wait_for_checks is
		// credited back to the deadline -run-deadline announced, up to
		// orchestrator.RunWaitCreditCap over the whole run -- and the
		// granting is the daemon's, because the timer is
		// (mcp.Registry.AnnounceWaitCredit). Without -server/-task there
		// is nobody to ask, so no credit is earned and every wait is
		// bounded by the announced deadline exactly as it was before.
		registry.AnnounceWaitCredit(orchestrator.RunWaitCreditCap,
			daemonWaitCredit{client: client, taskID: *taskID})
		// Nor is this one: the other thing this pair turns on that the
		// agent never calls. Anything a person types on the task while
		// the run is going is polled for here and appended to the run's
		// next tool result (mcp.Registry.AnnounceAddenda), starting from
		// the comment -addenda-after names as the last one already in
		// the prompt. Without -server/-task there is no conversation to
		// poll, and every result reads exactly as it did before.
		//
		// Not gated on -interactive, deliberately, where the chat tool
		// is: chat promises somebody is sitting there and blocks on it,
		// while this only asks whether anybody has said anything, and
		// the run it matters most on is the long background one nobody
		// is watching until they notice it has gone the wrong way.
		var writeFile func(context.Context, map[string]any) mcp.Result
		for _, t := range tools {
			if t.Name == "write_file" && t.Handler != nil {
				writeFile = t.Handler
				break
			}
		}

		ciScope := CiRepoScope(*prRepo, grants, taskDetail.DeploymentWide)
		// CI beyond the task's own repository is read with an api grant
		// in a spelling the agent API itself accepts (api:ci-read, or
		// api:ci-read@owner/repo for one repository), as on the proxy's
		// /fs/ (model.Store.SandboxNamespaceScope); a bare "ci-read" is
		// no capability and widens nothing.
		apiGrants := model.APIGrantsOf(grants)
		mayReadCI := grants.Has(grainci.CapabilityName) || grants.Has(selfdebug.CapabilityName) || apiGrants.Has("ci:read")
		mayReadAllCI := ciScope.AllRepos || apiGrants.Allows("ci:read", "")
		allowedCIRepos := make(map[string]bool)
		addAllowedRepo := func(r string) {
			r = strings.TrimSpace(r)
			if r == "" {
				return
			}
			allowedCIRepos[r] = true
			allowedCIRepos[strings.ToLower(r)] = true
		}
		addAllowedRepo(ciScope.Repo)
		addAllowedRepo(taskDetail.Task.Repo)
		for _, r := range taskDetail.Task.Reads {
			addAllowedRepo(r)
		}
		for _, w := range taskDetail.Task.Writes {
			addAllowedRepo(w.Repo)
		}
		taskReadRepos := make(map[string]bool)
		addTaskRepo := func(r string) {
			if r = strings.TrimSpace(r); r != "" {
				taskReadRepos[r] = true
				taskReadRepos[strings.ToLower(r)] = true
			}
		}
		// Only an api:tasks:read@repo grant opens other tasks' records;
		// the task's own target, reads and writes do not.
		for _, r := range apiGrants.Repos("tasks:read") {
			addTaskRepo(r)
		}
		for _, r := range apiGrants.Repos("ci:read") {
			addAllowedRepo(r)
		}
		registry.Register(mcp.NewNamespaceTools(mcp.NamespaceToolsConfig{
			Scope: namespace.Scope{
				TaskIDStr:        strings.TrimSpace(*taskID),
				TaskRepo:         ciScope.Repo,
				MayReadCI:        mayReadCI,
				MayReadAllCI:     mayReadAllCI,
				AllowedRepos:     allowedCIRepos,
				MayReadSrc:       grants.Has(selfdebug.CapabilityName),
				MayReadPlaybooks: grants.Has(bootstrap.CapabilityName) || grants.Has(selfdebug.CapabilityName),
				MayReadAllTasks:  grants.Has(selfdebug.CapabilityName) || apiGrants.Allows("tasks:read", ""),
				TaskReadRepos:    taskReadRepos,
				MayReadFullConfig: grants.Has(selfdebug.CapabilityName) ||
					apiGrants.Allows("settings:read", "") || apiGrants.Allows("repos:read", ""),
				MayReadAllMemory: apiGrants.Allows("memory:read", ""),
				MayReadMetrics:   grants.Has(selfdebug.CapabilityName) || apiGrants.Allows("deployment:read", ""),
				MayReadPipeline:  grants.Has(selfdebug.CapabilityName) || apiGrants.Allows("deployment:read", "") || apiGrants.Allows("dispatch:read", ""),
				MayReadHealth:    grainserver.HasHealth(grants) || apiGrants.Allows("deployment:read", ""),
				MayReadGrainLog:  grainserver.HasJournal(grants),
			},
			CIReader:    DaemonGrainCI{Client: client, TaskID: *taskID},
			ArtifactDir: ciArtifactDir(*dataDir),
			SrcDir:      *grainSrcDir,
			NewRedactor: func() *redact.Redactor { return buildNamespaceRedactor(*dataDir) },
			WriteFile:   writeFile,
		})...)

		delivered := newDeliveredCursor(client, *taskID, *addendaAfter)
		registry.AnnounceAddenda(*addendaAfter,
			daemonAddenda{client: client, taskID: *taskID, writeFile: writeFile, delivered: delivered})
		// chat needs the same daemon and task the three above do -- what
		// it posts and reads is that task's own conversation -- and one
		// thing more: that somebody is actually sitting with the task
		// (-interactive). A background run is given no chat tool rather
		// than one that would block for an hour waiting for a person who
		// was never there.
		proposals := &daemonProposalFiler{client: client, parent: *taskID, baseURL: *server, filed: map[string]string{}}
		var liveFiler mcp.ProposalFiler
		if *interactive {
			registry.Register(mcp.NewChatTools(daemonChat{client: client, taskID: *taskID, writeFile: writeFile,
				delivered: delivered}, mcp.SandboxFileReaderOf(tools))...)
			liveFiler = proposals
		}
		registry.Register(mcp.NewLiveProposalTools(escapeSink, proposeRepoList.Nameable(), *taskID,
			liveFiler)...)
	}

	registry.Register(GrantedDaemonTools(grants, *grainSrcDir, *grainSecretsDir, *grainStateDir, *taskID, client)...)
	// Local scripts: snapshotted now, at the start of the run, and closed
	// -- anything still running killed -- when this process's Serve
	// returns, which is when the run ends.
	localTools, closeLocal := localScriptTools(localScriptsList, *localScriptsEnvFile, grants, client != nil, client, *taskID)
	defer closeLocal()
	registry.Register(localTools...)
	if grants.Has(readissues.CapabilityName) {
		registry.Register(issueTools(*dataDir, *githubHost, *githubInsecureHTTP, *prRepo, *prCredential)...)
	}
	if grants.Has(selfdebug.CapabilityName) {
		var reader mcp.TaskReader
		if client != nil {
			reader = DaemonTasks{Client: client}
		}
		registry.Register(mcp.NewTaskTools(reader)...)
		// The delta feed a judgement agent starts each wake from
		// (read_pipeline_changes), over the same daemon.
		var pipeline mcp.PipelineReader
		if client != nil {
			pipeline = DaemonPipeline{Client: client}
		}
		registry.Register(mcp.NewPipelineTools(pipeline)...)
		// The same grant and the same daemon, for the deployment's own
		// numbers rather than for one task's record: read_grain_metrics
		// answers "what do the measurements say", which until now no
		// tool did at all. Registered with a nil reader where there is
		// no daemon, for NewTaskTools' reason -- the name exists and
		// says why it cannot answer.
		var metricsReader mcp.MetricsReader
		if client != nil {
			metricsReader = DaemonMetrics{Client: client}
		}
		registry.Register(mcp.NewMetricsTools(metricsReader)...)
	}

	control := ResolveMCPSurface(*surface, *granuleVM != "" || *root != "" || *container != "" || *k8sPod != "") == MCPSurfaceControl
	registry.WordToolBudget(mcp.ToolBudgetWording{CLI: control, PipelineCursor: *toolBudgetPipelineCursor})
	if control {
		keep := controlRoster(tools, localTools, grants,
			taskDetail.Task.ChangeTargetKind != "" || len(taskDetail.Task.Changelists) > 0)
		registry.Retain(func(name string) bool { return keep[name] })
	}

	// Serve returns io.EOF once its caller closes the write end of our
	// stdin -- the ordinary way a client signals "done", not a failure.
	// This process's own lifetime is the cancellation boundary: whatever
	// spawned it (claude -p's own --mcp-config fork, in production) is
	// killed as a whole when a run is cancelled, which takes this stdio
	// server down with it -- see Serve's own doc comment on why that
	// makes context.Background() the right ctx here, not a derived one.
	return mcp.Serve(context.Background(), registry, bufio.NewReader(in), bufio.NewWriter(out))
}

// parsedRunDeadline reads -run-deadline, or the zero time for a server
// that was given none -- which mcp.Registry.AnnounceDeadline takes as
// "say nothing about time", the way every caller with no deadline to
// give (pkg/mcp's own tests, tests/e2e, a `grain mcpserver` run by hand)
// already works.
//
// A value that will not parse is a warning on stderr and no more, for
// the reason pullRequestTools below refuses to be fatal: every other
// tool this process serves is how the agent touches its sandbox at all,
// and exiting over a malformed timestamp would turn a wrong reminder
// into a run that cannot edit a file. The daemon's own subprocess
// plumbing carries that line to an operator.
func ParsedRunDeadline(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"grain mcpserver: -run-deadline %q is not an RFC3339 time (%v); this run's tool "+
				"results will not say how long it has left\n", value, err)
		return time.Time{}
	}
	return at
}

// pullRequestTools builds pull_request_status and wait_for_checks for
// repo/branch, reading GitHub with the credential store under dataDir --
// the very files `grain daemon` loads for its own REST client (its
// credentialTokenSource call site), opened a second time here because
// this is a separate process with no route back into that one. Which
// credential in it is read with is the daemon's to say (-pr-credential,
// runCredentialSource below): the one this run's branch is pushed with,
// so a read is made as the account the push was, and the deployment's
// owner/repo ladder only when the daemon named none.
//
// grainCI is the one thing in those answers that does not come from
// GitHub: what grain's own CI made of the same commit, which has held
// grain's merge queue since grain/task-302 and which no GitHub check list
// shows (mcp's grain_ci.go). It is nil for every run grain CI does not
// judge, and a nil reader leaves all three tools reading as they did
// before -- so it is passed through here rather than resolved here,
// exactly as the GitHub client is.
//
// Nothing here is fatal, and that is deliberate: every other tool this
// process serves is how the agent touches its sandbox at all, so exiting
// because CI happens to be unreadable would turn a missing credential
// into a run that cannot edit a file. mcp.NewPullRequestTools registers
// both tools with a nil reader instead, which answers any call with a
// sentence saying there is nothing to report -- and the reason goes to
// stderr, where the daemon's own subprocess plumbing already carries it
// to an operator.
func repoTransport(credentials *gitproxy.CredentialSet, insecureHTTP bool, defaultHost, repo, credential string) (*github.RealTransport, gitproxy.CredentialHost, error) {
	ref, err := model.ParseRepo(repo)
	if err != nil {
		return nil, gitproxy.CredentialHost{}, err
	}
	host := ref.Host
	if host == "" {
		host = defaultHost
	}
	if host == "" {
		host = credentials.DefaultHost()
	}
	// The host the credential is recorded for decides the API this
	// transport reaches: the one the daemon named (the deployment's git
	// connection for the run), else the repository's own host. The
	// credentials.json ladder is not asked (docs/single-tenant-reset.md,
	// stage 1), the same as runCredentialSource.
	credHost := gitproxy.CredentialHost{}
	credName := credential
	if credential != "" {
		credHost = credentials.HostOf(credential)
	}
	if !credHost.Recorded {
		credHost = credentials.HostOf(host)
	}
	if !credHost.Recorded && !model.SameGitHost(host, credentials.DefaultHost(), defaultHost) {
		credHost = gitproxy.CredentialHost{Host: host, Kind: model.GitHostGitHub}
	}
	apiHost, prefix, ok := credHost.APIBase()
	if !ok {
		return nil, credHost, fmt.Errorf("repository %q is on %s, a plain git host with no forge API", repo, host)
	}
	if ref.Host == "" && defaultHost != "" && !credHost.Recorded {
		// A credential with no host record is on -github-host, which
		// for a local mock is the API itself (githubsim on
		// 127.0.0.1:port) -- but it is a *git* host, and for github.com
		// the API answers on api.github.com: pointed at github.com
		// itself, every REST path 404s and pull_request_status reads a
		// branch that was just pushed as one that never was
		// (github.APIHost's own doc comment, which this once repeated).
		apiHost = github.APIHost(defaultHost)
		prefix = ""
	}
	transport := github.NewRealTransport(apiHost)
	transport.Host = apiHost
	transport.PathPrefix = prefix
	transport.UseTLS = !insecureHTTP && credHost.UseTLS()
	switch {
	case credHost.Recorded && credName != "" && credentials.ForgeViaCommand(credName):
		// A host only its own tool reaches (a forge behind a front
		// end): every REST call is a `<tool> api` run.
		transport.Client = credentials.ForgeHTTPClient(credName, 0)
	case credHost.Proxy != "":
		// A host reached only through a proxy is reached through it for
		// REST too, and only it is.
		transport.Client = credHost.HTTPClient(0)
	}
	return transport, credHost, nil
}

// runCredentialSource is this process's github.TokenSource: the
// credential the daemon named (-pr-credential, agent.RunConfig.
// GitCredential) for every repository it is asked about, and none when
// it named none -- the credentials.json ladder is not asked
// (docs/single-tenant-reset.md, stage 1). The daemon's
// own client resolves the same question through the store
// (repoTeamCredential); this process has no store, so the daemon's
// answer travels as a name on the command line -- a name, never a
// token, read back out of the credential store already on this disk. A
// name that is not in that store resolves to nothing, exactly as no name
// does (credentialTokenSource.selectGitCredential).
func runCredentialSource(credentials *gitproxy.CredentialSet, credential string) credentialTokenSource {
	src := credentialTokenSource{credentials: credentials}
	if name := strings.TrimSpace(credential); name != "" {
		src.teamCredential = func(string, string, string) (string, bool) { return name, true }
	}
	return src
}

func issueTools(dataDir, githubHost string, insecureHTTP bool, repo, credential string) []mcp.Tool {
	client, scope, err := issueReader(dataDir, githubHost, insecureHTTP, repo, credential)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grain mcpserver: list_issues and read_issue are unavailable this run: %v\n", err)
		return mcp.NewIssueTools(nil, scope)
	}
	return mcp.NewIssueTools(client, scope)
}

func issueReader(dataDir, githubHost string, insecureHTTP bool, repo, credential string) (mcp.IssueReader, mcp.IssueScope, error) {
	var scope mcp.IssueScope
	if repo != "" {
		ref, err := model.ParseRepo(repo)
		if err == nil {
			scope.DefaultOwner = ref.Owner
			scope.DefaultRepo = ref.Name
		}
	}
	if dataDir == "" {
		return nil, scope, nil
	}
	credentials, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		return nil, scope, fmt.Errorf("loading the GitHub credential ladder: %w", err)
	}
	var transport github.Transport
	if repo != "" {
		t, _, err := repoTransport(credentials, insecureHTTP, githubHost, repo, credential)
		if err != nil {
			return nil, scope, fmt.Errorf("repository %q is on a plain git host with no issues API: %w", repo, err)
		}
		transport = t
	} else {
		rt := github.NewRealTransport(githubHost)
		rt.UseTLS = !insecureHTTP
		transport = rt
	}
	client := github.NewClient(transport, runCredentialSource(credentials, credential))
	client.Caller = "agent tools"
	return client, scope, nil
}

func PullRequestTools(dataDir, githubHost string, insecureHTTP bool, repo, branch, base, credential string,
	repair, ignoreGitHubCI bool, baseIncident string, grainCI mcp.GrainCIReader) []mcp.Tool {

	return pullRequestToolsWithNumber(dataDir, githubHost, insecureHTTP, repo, branch, base, credential,
		repair, ignoreGitHubCI, baseIncident, 0, grainCI)
}

func pullRequestToolsWithNumber(dataDir, githubHost string, insecureHTTP bool, repo, branch, base, credential string,
	repair, ignoreGitHubCI bool, baseIncident string, prNumber int, grainCI mcp.GrainCIReader) []mcp.Tool {

	client, scope, err := pullRequestReaderWithNumber(dataDir, githubHost, insecureHTTP, repo, branch, base, credential,
		repair, ignoreGitHubCI, baseIncident, prNumber)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"grain mcpserver: pull_request_status and wait_for_checks are unavailable this run: %v\n", err)
		return mcp.NewPullRequestTools(nil, nil, mcp.PullRequestScope{})
	}
	return mcp.NewPullRequestTools(client, grainCI, scope)
}

// githubDefaultBranchReader reads repositories' default branches with
// this run's credential (runCredentialSource), for query_merge_queue.
// Nil when there is no data directory or no credential store to read
// with: the queue report then falls back to what the daemon alone knows.
func githubDefaultBranchReader(dataDir, githubHost string, insecureHTTP bool, credential string) func(owner, repo string) (string, error) {
	if dataDir == "" {
		return nil
	}
	credentials, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		return nil
	}
	return func(owner, repo string) (string, error) {
		stored := owner
		if repo != "" {
			stored = owner + "/" + repo
		}
		ref := model.RepoFromStored(owner, repo)
		transport, _, err := repoTransport(credentials, insecureHTTP, githubHost, stored, credential)
		if err != nil {
			return "", err
		}
		client := github.NewClient(transport, runCredentialSource(credentials, credential))
		client.Caller = "agent tools"
		return client.DefaultBranch(ref.Owner, ref.Name)
	}
}

// PullRequestReader resolves the flags into a client and the one branch
// it may be asked about. An unset -pr-repo is not an error but a task
// with no repo attached (orchestrator.BuildPrompt has its own sentence
// for that case), so it returns a nil client and no error at all --
// there is nothing to warn an operator about.
func PullRequestReader(dataDir, githubHost string, insecureHTTP bool, repo, branch, base, credential string,
	repair, ignoreGitHubCI bool, baseIncident string) (mcp.PullRequestReader, mcp.PullRequestScope, error) {
	return pullRequestReaderWithNumber(dataDir, githubHost, insecureHTTP, repo, branch, base, credential,
		repair, ignoreGitHubCI, baseIncident, 0)
}

func pullRequestReaderWithNumber(dataDir, githubHost string, insecureHTTP bool, repo, branch, base, credential string,
	repair, ignoreGitHubCI bool, baseIncident string, prNumber int) (mcp.PullRequestReader, mcp.PullRequestScope, error) {
	if repo == "" {
		return nil, mcp.PullRequestScope{}, nil
	}
	if branch == "" && prNumber <= 0 {
		return nil, mcp.PullRequestScope{}, fmt.Errorf("-pr-branch is required with -pr-repo")
	}
	if dataDir == "" {
		return nil, mcp.PullRequestScope{}, fmt.Errorf("-data-dir is required with -pr-repo")
	}
	ref, err := model.ParseRepo(repo)
	if err != nil {
		return nil, mcp.PullRequestScope{}, fmt.Errorf("-pr-repo %q: %w", repo, err)
	}
	credentials, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		return nil, mcp.PullRequestScope{}, fmt.Errorf("loading the GitHub credential ladder: %w", err)
	}
	transport, credHost, err := repoTransport(credentials, insecureHTTP, githubHost, repo, credential)
	if err != nil {
		return nil, mcp.PullRequestScope{}, fmt.Errorf("repository %q is on %s, a plain git host with no pull request or CI checks API", repo, credHost.Host)
	}
	// The credential here is the one the daemon named for this run --
	// the one its branch is pushed with -- and not a posting identity of
	// its own: these tools only ever read a branch's checks, and this
	// process posts nothing (the agent's own open_pull_request goes back
	// through the daemon). What the name buys is that the read can see
	// the repository at all: a private repository read with no
	// credential is a 404, which pull_request_status would otherwise
	// report as a branch that was never pushed.
	client := github.NewClient(transport, runCredentialSource(credentials, credential))
	client.Caller = "agent tools"
	// An unset -pr-base is carried through as the empty string rather
	// than resolved here: it is the ordinary case, and what it resolves
	// to is a read of the repo that only base_branch_status needs. Doing
	// it there keeps a repo grain cannot read from costing this run
	// pull_request_status and wait_for_checks as well.
	return client, mcp.PullRequestScope{
		Owner: ref.Owner, Repo: ref.Name, Branch: branch, Base: base,
		// Carried through as it was given, and never derived from
		// anything here: whether a run is a repair is grain's own reading
		// of the task it dispatched (orchestrator.mergeRepairRun), the
		// same rule the repo and the two branches above are under.
		Repair: repair,
		// Likewise grain's own fact and not the agent's: whether
		// GitHub's checks are read for this repository at all is a
		// declaration somebody made in grain's settings, carried here
		// rather than sniffed out of the answers GitHub gives.
		GitHubCIIgnored:   ignoreGitHubCI,
		BaseIncident:      baseIncident,
		PullRequestNumber: prNumber,
	}, nil
}

// granuleSandboxTools builds the sandbox tools against granuleVM's
// guest, returning an error (matching the -sandbox-root checks above) if
// any required flag is missing -- there is no useful degraded mode for an
// mcpserver process that can't reach the sandbox it was told to run
// against.
//
// Unlike the SSH transport this replaced, there is nothing to resolve
// first: the tools reach the guest by exec'ing into granuleVM's own
// container (mcp.DockerExecRunner), whose name follows from the VM name
// alone, so no port, pod IP or state file lookup stands between this and
// a usable runner.
func granuleSandboxTools(granuleVM, sshUser, execKey, workspace string) ([]mcp.Tool, error) {
	if sshUser == "" {
		return nil, errors.New("grain mcpserver: -ssh-user is required with -granule-vm")
	}
	if workspace == "" {
		return nil, errors.New("grain mcpserver: -workspace is required with -granule-vm")
	}

	runner := &mcp.DockerExecRunner{
		Container: granule.PodName(granuleVM),
		User:      sshUser,
		KeyPath:   execKey,
	}
	return mcp.NewSSHSandboxTools(runner, workspace), nil
}

// supersedeKept is ui.Task.SupersedeKept as mcp reports it.
func supersedeKept(kept []ui.SupersedeKeptEntry) []mcp.SupersedeKept {
	var out []mcp.SupersedeKept
	for _, k := range kept {
		out = append(out, mcp.SupersedeKept{ID: k.ID, Reason: k.Reason})
	}
	return out
}
