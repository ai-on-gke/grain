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

package gitproxy

// cli_actions.go serves the /cli/ routes that act rather than read: the
// in-sandbox grain CLI's grain ci wait, run-job and retrigger, grain pr
// open, reply, resolve and review-comment, and the metadata grain git
// revert reverts by (docs/design/sandbox-first-agent-surface.md, Stage 2).
//
// Every one of them acts on the task the sandbox's token belongs to and
// on nothing else. No request names a repository, a branch or a pull
// request: the run's repository and branch are its scope's (the live
// run's model.NamespaceScope), the CI run a resumed wait names is checked
// against them before it is read, and a comment or thread a PR action
// names is checked against the one pull request open for that branch
// before anything is posted. That is the same rule pkg/mcp's
// pullRequestToolsWithNumber holds the MCP tools these replace to.
//
// The two blocking routes, /cli/ci/wait and /cli/ci/run-job, are served
// inside this daemon, which is what lets them do what the MCP tools'
// forked mcpserver has to ask for over HTTP: they put the run's activity
// row in a declared wait while they block (so a durable run's idle
// watchdog leaves it alone), credit the time they block back to the run's
// deadline as they spend it (CLIWaitCreditor, orchestrator.RunWaitCredits)
// and record the wait in task_run_check_wait. They block for one poll
// slice at most -- DefaultCLIWaitSlice, inside run_command's own ceiling
// -- and answer status "running" with the run to resume when CI outlasts
// it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// DefaultCLIWaitSlice is the longest one /cli/ci/wait or /cli/ci/run-job
// request blocks: nine minutes, so the answer is back before the 600s
// ceiling run_command puts on the command that asked.
const DefaultCLIWaitSlice = 540 * time.Second

// cliWaitDeadlineMargin is how long before the request's own deadline a
// poll slice gives up, so the "still running" answer is written while
// somebody is still reading.
const cliWaitDeadlineMargin = 15 * time.Second

// cliWaitPoll and cliWaitHeartbeat are how often a blocked wait re-reads
// the CI run, and how often it re-stamps the run's activity and reports
// its blocked time for credit. Variables so tests can run in
// milliseconds.
var (
	cliWaitPoll      = 5 * time.Second
	cliWaitHeartbeat = 15 * time.Second
)

// CIStartRequest is what a /cli/ci/ action asks grain CI to run on the
// task's own branch: ui.TaskCIRunOptions in this package's terms.
type CIStartRequest struct {
	Jobs       []string
	Workflow   string
	Commit     string
	Recheck    bool
	FailedOnly bool
}

// CLIActionResult is a PR action's answer: the sentence the MCP tool it
// replaces would have said, and whether that sentence is a refusal.
type CLIActionResult struct {
	Text    string
	Refused bool
}

// RevertInfo is what grain git revert needs to find a task's work in the
// sandbox's own history: the branch it was pushed on (the merge commit
// names it) and the pull request it was opened as, if any.
type RevertInfo struct {
	Task        string `json:"task"`
	Title       string `json:"title,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Branch      string `json:"branch,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
}

// CLIActions is the daemon behind the acting /cli/ routes. Every method
// takes the task the authenticated sandbox belongs to and resolves its
// repository, branch and pull request from that task's own record.
// Implemented by pkg/ui's SandboxCLIActions.
type CLIActions interface {
	// StartTaskCI starts grain CI on taskID's own branch, or attaches to
	// the identical run already in flight.
	StartTaskCI(ctx context.Context, taskID string, req CIStartRequest) (ci.Run, error)
	// CIRun reads one run by id. Its false is "not remembered" -- unless
	// the actions are a ci.Readiness saying CI is not up yet, when it is
	// that instead (served as 503, not 404).
	CIRun(ctx context.Context, id string) (ci.Run, bool)
	// OpenPullRequest opens (or finds) the pull request for taskID's
	// branch and reports it.
	OpenPullRequest(ctx context.Context, taskID string) (CLIActionResult, error)
	// ReplyToPullRequestComment posts body on taskID's own open pull
	// request: in commentID's thread, or top-level when commentID is 0.
	ReplyToPullRequestComment(ctx context.Context, taskID string, commentID int, body string) (CLIActionResult, error)
	// ResolveReviewThread resolves a thread on taskID's own open pull
	// request, named by threadID or by a commentID in it.
	ResolveReviewThread(ctx context.Context, taskID, threadID string, commentID int) (CLIActionResult, error)
	// RevertInfo describes target for a revert asked for by taskID's run.
	RevertInfo(ctx context.Context, taskID, target string, anyTask bool) (RevertInfo, error)
}

// CLIWaitCredit is what one report of CLI-blocked time bought.
type CLIWaitCredit struct {
	Blocked  time.Duration
	Credited time.Duration
	Cap      time.Duration
	Live     bool
}

// CLIWaitCreditor credits a run for time the sandbox CLI spent blocked on
// its behalf. blocked is the run's cumulative CLI total, never a delta.
// Implemented by pkg/daemon over orchestrator.RunWaitCredits.
type CLIWaitCreditor interface {
	CreditCLIWait(ctx context.Context, taskID string, blocked time.Duration) (CLIWaitCredit, error)
}

// proxyCheckWaitRecorder records one CI wait the proxy served
// (task_run_check_wait). Implemented by *model.Store.
type proxyCheckWaitRecorder interface {
	RecordProxyCheckWait(ctx context.Context, runID, verdict string, waited time.Duration) error
}

// proxyToolCallRecorder keeps a call's arguments for the finish path to
// read with the run's own tool calls (add_review_comment's findings).
// Implemented by *model.Store.
type proxyToolCallRecorder interface {
	RecordProxyToolCallArgs(ctx context.Context, runID, tool string, args map[string]any) error
}

// cliWaitLedger is each run's cumulative CLI-blocked time, which is what
// CLIWaitCreditor is told.
type cliWaitLedger struct {
	mu      sync.Mutex
	blocked map[string]time.Duration
}

func (l *cliWaitLedger) add(runID string, d time.Duration) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocked == nil {
		l.blocked = map[string]time.Duration{}
	}
	if d > 0 {
		l.blocked[runID] += d
	}
	return l.blocked[runID]
}

// MaxCLIProposalsPerAttempt caps how many unapproved proposals one sandbox
// attempt may file through POST /cli/proposals.
const MaxCLIProposalsPerAttempt = 10

// CLINotifyRequest is POST /cli/notify's body.
type CLINotifyRequest struct {
	Subject    string                   `json:"subject"`
	Title      string                   `json:"title"`
	Body       string                   `json:"body,omitempty"`
	Severity   string                   `json:"severity,omitempty"`
	Task       string                   `json:"task,omitempty"`
	ClearsWhen *model.NotificationClear `json:"clears_when,omitempty"`
}

// CLINotifyResult is POST /cli/notify's answer.
type CLINotifyResult struct {
	ID       int64  `json:"id"`
	Subject  string `json:"subject"`
	Severity string `json:"severity"`
	Count    int    `json:"count"`
	New      bool   `json:"new"`
}

// CLITaskWaitRequest is POST /cli/tasks/wait's body.
type CLITaskWaitRequest struct {
	IDs            []string `json:"ids"`
	Until          string   `json:"until,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Park           bool     `json:"park,omitempty"`
	ResumeAt       string   `json:"resume_at,omitempty"`
}

// CLITaskWaitResponse is POST /cli/tasks/wait's answer.
type CLITaskWaitResponse struct {
	Status      string             `json:"status"`
	Until       string             `json:"until"`
	Targets     []model.WaitTarget `json:"targets"`
	Summary     string             `json:"summary"`
	WaitedMS    int64              `json:"waited_ms"`
	CreditMS    int64              `json:"credit_used_ms"`
	CreditCapMS int64              `json:"credit_cap_ms,omitempty"`
	CreditLive  bool               `json:"credit_live"`
}

// CLIProposeRequest is POST /cli/proposals' body.
type CLIProposeRequest struct {
	ID              string              `json:"id,omitempty"`
	Title           string              `json:"title"`
	Body            string              `json:"body"`
	Repo            string              `json:"repo,omitempty"`
	Branch          string              `json:"branch,omitempty"`
	NoRepo          bool                `json:"no_repo,omitempty"`
	DependsOn       []string            `json:"depends_on,omitempty"`
	Capabilities    []string            `json:"capabilities,omitempty"`
	CustomFields    map[string][]string `json:"custom_fields,omitempty"`
	NotBefore       *time.Time          `json:"not_before,omitempty"`
	FixesBaseBranch bool                `json:"fixes_base_branch,omitempty"`
	NoPort          bool                `json:"no_port,omitempty"`
	KeepRunning     bool                `json:"keep_running,omitempty"`
	Origin          string              `json:"origin,omitempty"`
	Reads           []string            `json:"reads,omitempty"`
	HoldForApproval *bool               `json:"hold_for_approval,omitempty"`
	AutoMerge       *bool               `json:"auto_merge,omitempty"`
	Supersedes      string              `json:"supersedes,omitempty"`
}

// CLIProposeResult is POST /cli/proposals' answer.
type CLIProposeResult struct {
	ID              string `json:"id"`
	URL             string `json:"url"`
	HoldForApproval bool   `json:"hold_for_approval"`
	AutoMerge       bool   `json:"auto_merge"`
	Queued          bool   `json:"queued"`
	Held            bool   `json:"held"`
	ApprovalNote    string `json:"approval_note,omitempty"`
}

// CLIQuarantineRequest is POST /cli/quarantine's body.
type CLIQuarantineRequest struct {
	TestName     string `json:"test_name"`
	Reason       string `json:"reason"`
	TTL          string `json:"ttl,omitempty"`
	FileFollowUp *bool  `json:"file_follow_up,omitempty"`
}

// CLIQuarantineResult is POST /cli/quarantine's answer.
type CLIQuarantineResult struct {
	TaskID         string `json:"task_id"`
	FollowUpTaskID string `json:"follow_up_task_id,omitempty"`
}

// CLIAPIRoute is one route returned by GET /cli/api-routes.
type CLIAPIRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	NeedsReason bool   `json:"needs_reason,omitempty"`
}

// CLITaskActions extends CLIActions with the Stage 3 collaboration, wait,
// proposal, and API-discovery actions. Implemented by pkg/ui's
// SandboxCLIActions and pkg/daemon's sandboxCLIGate.
type CLITaskActions interface {
	NotifyOperator(ctx context.Context, taskID string, req CLINotifyRequest) (CLINotifyResult, error)
	MessageTask(ctx context.Context, callerTaskID, targetTaskID, body string) (int64, error)
	CheckWaitTargets(ctx context.Context, callerTaskID string, ids []string) ([]model.WaitTarget, error)
	ProposeTask(ctx context.Context, callerTaskID, runID string, req CLIProposeRequest) (CLIProposeResult, error)
	ListAPIRoutes(ctx context.Context, callerTaskID string, grants []string, scopeFilter string) ([]CLIAPIRoute, error)
}

// CLIGitHubCheckActions is an optional extension of CLIActions for tasks whose
// repository gates on GitHub check runs (ci_mode: "github") rather than
// Grain CI. When UsesGitHubChecks reports true for a task, /cli/ci/wait polls
// GitHub check runs via WaitForGitHubChecks instead of starting a Grain CI run
// via StartTaskCI.
type CLIGitHubCheckActions interface {
	UsesGitHubChecks(ctx context.Context, taskID string) (bool, error)
	WaitForGitHubChecks(ctx context.Context, taskID string, timeout, poll time.Duration, onPoll func(waited time.Duration)) (mcp.GitHubCheckWaitResult, error)
}

// proxyHandoverWriter writes a standing handover comment for a task run.
// Implemented by *model.Store.
type proxyHandoverWriter interface {
	WriteTaskHandover(ctx context.Context, taskID, runID, body string) error
}

type cliProposalLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (l *cliProposalLimiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = make(map[string]int)
	}
	if l.counts[key] >= MaxCLIProposalsPerAttempt {
		return false
	}
	l.counts[key]++
	return true
}

// cliActionRoutes are the routes this file serves, by method.
var cliActionRoutes = map[string]string{
	"ci/wait":           http.MethodPost,
	"ci/run-job":        http.MethodPost,
	"ci/retrigger":      http.MethodPost,
	"pr/open":           http.MethodPost,
	"pr/reply":          http.MethodPost,
	"pr/resolve":        http.MethodPost,
	"pr/review-comment": http.MethodPost,
	"status":            http.MethodPost,
	"handover":          http.MethodPost,
	"notify":            http.MethodPost,
	"tasks/wait":        http.MethodPost,
	"proposals":         http.MethodPost,
	"quarantine":        http.MethodPost,
	"api-routes":        http.MethodGet,
}

func isRevertInfoRoute(route string) bool {
	rest, ok := strings.CutPrefix(route, "tasks/")
	if !ok {
		return false
	}
	id, tail, _ := strings.Cut(rest, "/")
	return id != "" && tail == "revert-info"
}

func isTaskNotesRoute(route string) bool {
	rest, ok := strings.CutPrefix(route, "tasks/")
	if !ok {
		return false
	}
	id, tail, _ := strings.Cut(rest, "/")
	return id != "" && tail == "notes"
}

// serveCLIAction serves route when it is one of the acting routes, and
// reports whether it was.
func (p *GitProxy) serveCLIAction(w http.ResponseWriter, r *http.Request, scope namespace.Scope, route string) bool {
	method, ok := cliActionRoutes[route]
	if !ok && isRevertInfoRoute(route) {
		method, ok = http.MethodGet, true
	}
	if !ok && isTaskNotesRoute(route) {
		method, ok = http.MethodPost, true
	}
	if !ok {
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, fmt.Sprintf("/cli/%s takes %s", route, method), http.StatusMethodNotAllowed)
		return true
	}
	if scope.TaskIDStr == "" {
		http.Error(w, "forbidden: this sandbox has no task to act for", http.StatusForbidden)
		return true
	}
	if route == "status" {
		p.serveStatus(w, r, scope)
		return true
	}
	if route == "handover" {
		p.serveHandover(w, r, scope)
		return true
	}
	if p.CLIActions == nil {
		http.Error(w, "this deployment's sandbox proxy is not wired to act on tasks: use the MCP tools", http.StatusNotImplemented)
		return true
	}
	switch {
	case route == "ci/wait" || route == "ci/run-job":
		p.serveCIWait(w, r, scope, route == "ci/run-job")
	case route == "ci/retrigger":
		p.serveCIRetrigger(w, r, scope)
	case route == "pr/open":
		res, err := p.CLIActions.OpenPullRequest(r.Context(), scope.TaskIDStr)
		writeActionResult(w, res, err)
	case route == "pr/reply":
		var req struct {
			Body      string `json:"body"`
			CommentID int    `json:"comment_id"`
		}
		if !readCLIJSON(w, r, &req) {
			return true
		}
		if strings.TrimSpace(req.Body) == "" {
			http.Error(w, "a comment body is required", http.StatusBadRequest)
			return true
		}
		if req.CommentID < 0 {
			http.Error(w, "comment_id must be a positive integer", http.StatusBadRequest)
			return true
		}
		res, err := p.CLIActions.ReplyToPullRequestComment(r.Context(), scope.TaskIDStr, req.CommentID, req.Body)
		writeActionResult(w, res, err)
	case route == "pr/resolve":
		var req struct {
			ThreadID  string `json:"thread_id"`
			CommentID int    `json:"comment_id"`
		}
		if !readCLIJSON(w, r, &req) {
			return true
		}
		if strings.TrimSpace(req.ThreadID) == "" && req.CommentID <= 0 {
			http.Error(w, "name the thread: --thread-id, or --comment-id of a comment in it", http.StatusBadRequest)
			return true
		}
		res, err := p.CLIActions.ResolveReviewThread(r.Context(), scope.TaskIDStr, strings.TrimSpace(req.ThreadID), req.CommentID)
		writeActionResult(w, res, err)
	case route == "pr/review-comment":
		p.serveReviewComment(w, r, scope)
	case route == "notify":
		p.serveNotify(w, r, scope)
	case isTaskNotesRoute(route):
		id := strings.TrimSuffix(strings.TrimPrefix(route, "tasks/"), "/notes")
		p.serveTaskNotes(w, r, scope, id)
	case route == "tasks/wait":
		p.serveTasksWait(w, r, scope)
	case route == "proposals":
		p.serveProposals(w, r, scope)
	case route == "quarantine":
		p.serveQuarantine(w, r, scope)
	case route == "api-routes":
		p.serveAPIRoutes(w, r, scope)
	default:
		id := strings.TrimSuffix(strings.TrimPrefix(route, "tasks/"), "/revert-info")
		info, err := p.CLIActions.RevertInfo(r.Context(), scope.TaskIDStr, id, scope.MayReadAllTasks)
		if err != nil {
			cliError(w, err)
			return true
		}
		writeCLIJSON(w, http.StatusOK, info)
	}
	return true
}

func readCLIJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "reading the request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		http.Error(w, "the request body is not JSON this route reads: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeCLIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeActionResult answers a PR action: its sentence as plain text, a
// refusal as 422 so the CLI exits non-zero, and an error as the daemon's
// own words.
func writeActionResult(w http.ResponseWriter, res CLIActionResult, err error) {
	if err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if res.Refused {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	text := res.Text
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = io.WriteString(w, text)
}

// requireOwnCI refuses a CI action to a run without grain-ci, the grant
// that decides whether its branch goes through grain CI at all.
func requireOwnCI(scope namespace.Scope) error {
	if !scope.MayReadCI && !scope.MayReadAllCI {
		return namespace.RefuseCapability("grain ci", "grain-ci")
	}
	if scope.TaskRepo == "" || scope.TaskBranch == "" {
		return fmt.Errorf("%w: this task has no repository and branch of its own for grain CI to run on", namespace.ErrBadRequest)
	}
	return nil
}

// cliWaitRequest is POST /cli/ci/wait's and /cli/ci/run-job's body.
type cliWaitRequest struct {
	Jobs           []string `json:"jobs,omitempty"`
	Job            string   `json:"job,omitempty"`
	ResumeRunID    string   `json:"resume_run_id,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// CLIJobVerdict is one job's state in a wait's answer.
type CLIJobVerdict struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// CLIWaitResponse is what /cli/ci/wait and /cli/ci/run-job answer.
type CLIWaitResponse struct {
	// Status is "passed", "failed" or "running" (the slice ran out).
	Status      string          `json:"status"`
	RunID       string          `json:"run_id"`
	ResumeRunID string          `json:"resume_run_id,omitempty"`
	Commit      string          `json:"commit,omitempty"`
	Conclusion  string          `json:"conclusion,omitempty"`
	Error       string          `json:"error,omitempty"`
	Report      string          `json:"report,omitempty"`
	Selective   bool            `json:"selective,omitempty"`
	Jobs        []CLIJobVerdict `json:"jobs"`
	WaitedMS    int64           `json:"waited_ms"`
	CreditMS    int64           `json:"credit_used_ms"`
	CreditCapMS int64           `json:"credit_cap_ms,omitempty"`
	CreditLive  bool            `json:"credit_live"`
}

func (p *GitProxy) serveCIWait(w http.ResponseWriter, r *http.Request, scope namespace.Scope, single bool) {
	if single {
		if err := requireOwnCI(scope); err != nil {
			cliError(w, err)
			return
		}
	}
	var req cliWaitRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	jobs := cleanJobs(req.Jobs)
	if single {
		job := strings.TrimSpace(req.Job)
		if job == "" {
			http.Error(w, "a job name is required: grain ci run-job <job>", http.StatusBadRequest)
			return
		}
		jobs = []string{job}
	}
	ctx := r.Context()

	slice := DefaultCLIWaitSlice
	if req.TimeoutSeconds > 0 {
		slice = min(slice, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	start := time.Now()
	end := start.Add(slice)
	if deadline, ok := ctx.Deadline(); ok {
		if d := deadline.Add(-cliWaitDeadlineMargin); d.Before(end) {
			end = d
		}
	}

	if !single && len(jobs) == 0 && strings.TrimSpace(req.ResumeRunID) == "" {
		if ghActions, ok := p.CLIActions.(CLIGitHubCheckActions); ok {
			usesGitHub, err := ghActions.UsesGitHubChecks(ctx, scope.TaskIDStr)
			if err != nil {
				cliError(w, err)
				return
			}
			if usesGitHub {
				if scope.TaskRepo == "" || scope.TaskBranch == "" {
					cliError(w, fmt.Errorf("%w: this task has no repository and branch of its own for CI to run on", namespace.ErrBadRequest))
					return
				}
				p.serveGitHubCIWait(w, ctx, scope, start, end, ghActions)
				return
			}
		}
	}
	if err := requireOwnCI(scope); err != nil {
		cliError(w, err)
		return
	}

	// A daemon that has just restarted serves this route before its CI
	// service is up (ci.ErrNotUp), for as long as adopting its runs
	// takes -- over a minute on a busy one. That is the wait's own
	// business to sit out, inside its slice, rather than a reason to
	// stop waiting: the run it was asked about is still there.
	var run ci.Run
	for {
		got, err := p.startCIWait(ctx, scope, req.ResumeRunID, jobs)
		if err == nil {
			run = got
			break
		}
		if !ci.IsNotUp(err) || !p.napBefore(ctx, end) {
			cliError(w, err)
			return
		}
	}

	tool := "wait_for_checks"
	if single {
		tool = "run_ci_job"
	}
	phrase := mcp.ToolActivityNote(tool)
	p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)

	lastReport := start
	var credit CLIWaitCredit
	report := func(now time.Time) {
		credit = p.creditCLIWait(ctx, scope, now.Sub(lastReport))
		lastReport = now
	}

	poll := time.NewTicker(cliWaitPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(cliWaitHeartbeat)
	defer heartbeat.Stop()
	for !run.Conclusion.Done() && !p.shuttingDown() {
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
		case <-p.ShutdownStarted:
		case <-poll.C:
			if got, ok := p.CLIActions.CIRun(ctx, run.ID); ok {
				run = got
			}
		case now := <-heartbeat.C:
			p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)
			report(now)
		case <-timer.C:
		}
		timer.Stop()
		if ctx.Err() != nil || p.shuttingDown() {
			break
		}
	}
	now := time.Now()
	report(now)
	waited := now.Sub(start)

	resp := CLIWaitResponse{
		RunID:       run.ID,
		Commit:      run.Commit,
		Conclusion:  string(run.Conclusion),
		Error:       run.Error,
		Selective:   len(jobs) > 0,
		WaitedMS:    waited.Milliseconds(),
		CreditMS:    credit.Credited.Milliseconds(),
		CreditCapMS: credit.Cap.Milliseconds(),
		CreditLive:  credit.Live,
	}
	for _, job := range ci.SortJobRuns(run.Jobs) {
		verdict := string(job.Conclusion)
		if verdict == "" {
			verdict = "running"
		}
		resp.Jobs = append(resp.Jobs, CLIJobVerdict{Name: job.Name, Conclusion: verdict})
	}
	verdict := mcp.WaitVerdictTimedOut
	switch {
	case !run.Conclusion.Done():
		resp.Status = "running"
		resp.ResumeRunID = run.ID
	case run.Conclusion == ci.ConclusionSuccess:
		resp.Status = "passed"
		verdict = mcp.WaitVerdictPassed
	default:
		resp.Status = "failed"
		verdict = mcp.WaitVerdictFailed
	}
	p.recordCheckWait(ctx, scope.RunID, verdict, waited)
	writeCLIJSON(w, http.StatusOK, resp)
}

func (p *GitProxy) serveGitHubCIWait(w http.ResponseWriter, ctx context.Context, scope namespace.Scope,
	start, end time.Time, ghActions CLIGitHubCheckActions) {

	phrase := mcp.ToolActivityNote("wait_for_checks")
	p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)

	lastReport := start
	var credit CLIWaitCredit
	report := func(now time.Time) {
		credit = p.creditCLIWait(ctx, scope, now.Sub(lastReport))
		lastReport = now
	}

	timeout := max(time.Until(end), 0)
	res, err := ghActions.WaitForGitHubChecks(ctx, scope.TaskIDStr, timeout, cliWaitPoll, func(_ time.Duration) {
		now := time.Now()
		p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)
		report(now)
	})
	if err != nil {
		cliError(w, err)
		return
	}
	now := time.Now()
	report(now)
	waited := now.Sub(start)

	resp := CLIWaitResponse{
		Status:      res.Status,
		RunID:       "github",
		Commit:      res.Commit,
		Conclusion:  res.Conclusion,
		Report:      res.Report,
		WaitedMS:    waited.Milliseconds(),
		CreditMS:    credit.Credited.Milliseconds(),
		CreditCapMS: credit.Cap.Milliseconds(),
		CreditLive:  credit.Live,
	}
	for _, c := range res.Checks {
		verdict := ""
		if c.Conclusion != nil {
			verdict = strings.ToLower(strings.TrimSpace(*c.Conclusion))
		}
		if !strings.EqualFold(c.Status, "completed") || verdict == "" {
			verdict = "running"
		}
		resp.Jobs = append(resp.Jobs, CLIJobVerdict{Name: c.Name, Conclusion: verdict})
	}
	if res.Verdict != "" {
		p.recordCheckWait(ctx, scope.RunID, res.Verdict, waited)
	}
	writeCLIJSON(w, http.StatusOK, resp)
}

// startCIWait is the run a wait follows: resumeID's, or the one
// StartTaskCI starts (or joins) on the task's branch. A resumeID CIRun
// cannot find while CI is not up yet is ci.ErrNotUp, not a run that has
// been forgotten.
func (p *GitProxy) startCIWait(ctx context.Context, scope namespace.Scope, resumeID string, jobs []string) (ci.Run, error) {
	id := strings.TrimSpace(resumeID)
	if id == "" {
		return p.CLIActions.StartTaskCI(ctx, scope.TaskIDStr, CIStartRequest{Jobs: jobs})
	}
	got, ok := p.CLIActions.CIRun(ctx, id)
	if !ok {
		if !ci.IsUp(p.CLIActions) {
			return ci.Run{}, ci.ErrNotUp
		}
		return ci.Run{}, fmt.Errorf("%w: no CI run %q -- a run is remembered for a while and then forgotten", namespace.ErrNotFound, id)
	}
	if !strings.EqualFold(got.Repo, scope.TaskRepo) || got.Ref != scope.TaskBranch {
		return ci.Run{}, fmt.Errorf("%w: CI run %s is not on this task's branch (%s in %s)", namespace.ErrForbidden, id, scope.TaskBranch, scope.TaskRepo)
	}
	return got, nil
}

// napBefore sleeps one poll before asking again, reporting false --
// with nothing slept -- when there is no room for one before end, or
// the request or the proxy is ending.
func (p *GitProxy) napBefore(ctx context.Context, end time.Time) bool {
	nap := cliWaitPoll
	if time.Until(end) < nap {
		return false
	}
	timer := time.NewTimer(nap)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-p.ShutdownStarted:
		return false
	case <-timer.C:
		return true
	}
}

func (p *GitProxy) shuttingDown() bool {
	select {
	case <-p.ShutdownStarted:
		return true
	default:
		return false
	}
}

func cleanJobs(jobs []string) []string {
	var out []string
	for _, j := range jobs {
		for _, part := range strings.Split(j, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// noteDeclaredWait puts a declared-wait phrase on the run's row whether or
// not it was just written: re-stamping it is what tells a durable run's
// idle watchdog the wait is still going.
func (p *GitProxy) noteDeclaredWait(ctx context.Context, taskID, phrase string) {
	if p.Activity == nil || taskID == "" || phrase == "" {
		return
	}
	now := time.Now()
	p.activity.shouldWrite(taskID, phrase, now)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = p.Activity.SetTaskActivity(ctx, taskID, phrase, now)
}

// creditCLIWait adds d to the run's CLI-blocked total and reports the
// total for credit.
func (p *GitProxy) creditCLIWait(ctx context.Context, scope namespace.Scope, d time.Duration) CLIWaitCredit {
	key := scope.RunID
	if key == "" {
		key = "task:" + scope.TaskIDStr
	}
	total := p.cliWaits.add(key, d)
	if p.WaitCredit == nil {
		return CLIWaitCredit{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	credit, err := p.WaitCredit.CreditCLIWait(ctx, scope.TaskIDStr, total)
	if err != nil {
		return CLIWaitCredit{}
	}
	return credit
}

func (p *GitProxy) recordCheckWait(ctx context.Context, runID, verdict string, waited time.Duration) {
	rec, ok := p.Activity.(proxyCheckWaitRecorder)
	if !ok || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = rec.RecordProxyCheckWait(ctx, runID, verdict, waited)
}

// cliRetriggerRequest is POST /cli/ci/retrigger's body.
type cliRetriggerRequest struct {
	Jobs     []string `json:"jobs,omitempty"`
	All      bool     `json:"all,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Workflow string   `json:"workflow,omitempty"`
}

func (p *GitProxy) serveCIRetrigger(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	if err := requireOwnCI(scope); err != nil {
		cliError(w, err)
		return
	}
	var req cliRetriggerRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	jobs := cleanJobs(req.Jobs)
	if len(jobs) == 1 && jobs[0] == "*" {
		jobs, req.All = nil, true
	}
	commit := strings.TrimSpace(req.Commit)
	if commit != "" && !isHexSHA(commit) {
		http.Error(w, fmt.Sprintf("commit %q is not a hex commit SHA", commit), http.StatusBadRequest)
		return
	}
	run, err := p.CLIActions.StartTaskCI(r.Context(), scope.TaskIDStr, CIStartRequest{
		Jobs:       jobs,
		Workflow:   strings.TrimSpace(req.Workflow),
		Commit:     commit,
		Recheck:    true,
		FailedOnly: len(jobs) == 0 && !req.All,
	})
	if err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Retriggered grain CI on %s: %s\n", scope.TaskBranch, formatCIRunLine(run))
	for _, job := range ci.SortJobRuns(run.Jobs) {
		fmt.Fprintf(w, "  %s\n", job.Name)
	}
	fmt.Fprintf(w, "Wait for it with: grain ci wait --resume %s\n", run.ID)
}

func isHexSHA(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// cliReviewCommentRequest is POST /cli/pr/review-comment's body, the
// arguments add_review_comment takes.
type cliReviewCommentRequest struct {
	Body string `json:"body"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// errNoCallRecorder is a review comment with nowhere to be kept.
var errNoCallRecorder = errors.New("this deployment's sandbox proxy cannot keep review comments: use the add_review_comment tool")

// serveReviewComment keeps one review finding with the run, to be relayed
// when the run finishes exactly as an add_review_comment call is
// (orchestrator's reviewFindings).
func (p *GitProxy) serveReviewComment(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	var req cliReviewCommentRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		http.Error(w, "a comment body is required", http.StatusBadRequest)
		return
	}
	path := strings.TrimSpace(req.Path)
	if (path == "") != (req.Line <= 0) {
		http.Error(w, "give both --path and --line to attach a comment to a line, or neither for a general remark", http.StatusBadRequest)
		return
	}
	rec, ok := p.Activity.(proxyToolCallRecorder)
	if !ok || scope.RunID == "" {
		cliError(w, errNoCallRecorder)
		return
	}
	args := map[string]any{"body": req.Body}
	if path != "" {
		args["path"] = path
		args["line"] = req.Line
	}
	if err := rec.RecordProxyToolCallArgs(r.Context(), scope.RunID, "add_review_comment", args); err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	where := "as a general remark"
	if path != "" {
		where = fmt.Sprintf("on %s:%d", path, req.Line)
	}
	fmt.Fprintf(w, "Recorded a review comment %s. It is relayed with this run's other findings when the run finishes.\n", where)
}

func (p *GitProxy) taskActions() (CLITaskActions, bool) {
	ta, ok := p.CLIActions.(CLITaskActions)
	return ta, ok && ta != nil
}

// serveStatus answers POST /cli/status (update_status).
func (p *GitProxy) serveStatus(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	var req struct {
		Status string `json:"status"`
	}
	if !readCLIJSON(w, r, &req) {
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		http.Error(w, "status is required: grain status <one-line-note>", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(status, "\r\n") || len(status) > 120 {
		http.Error(w, "status must be a single line of at most 120 characters", http.StatusBadRequest)
		return
	}
	if p.Activity == nil {
		http.Error(w, "this deployment's sandbox proxy cannot record status: use the update_status tool", http.StatusNotImplemented)
		return
	}
	now := time.Now()
	p.activity.shouldWrite(scope.TaskIDStr, status, now)
	if _, err := p.Activity.SetTaskActivity(r.Context(), scope.TaskIDStr, status, now); err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Updated status: %s\n", status)
}

// serveHandover answers POST /cli/handover (write_handover).
func (p *GitProxy) serveHandover(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	var req struct {
		Handover string `json:"handover"`
	}
	if !readCLIJSON(w, r, &req) {
		return
	}
	handover := strings.TrimSpace(req.Handover)
	if handover == "" {
		http.Error(w, "handover text is required: grain handover write (<text> | --file <f> | -)", http.StatusBadRequest)
		return
	}
	wrote := false
	if hw, ok := p.Activity.(proxyHandoverWriter); ok {
		if err := hw.WriteTaskHandover(r.Context(), scope.TaskIDStr, scope.RunID, handover); err != nil {
			cliError(w, err)
			return
		}
		wrote = true
	}
	if rec, ok := p.Activity.(proxyToolCallRecorder); ok && scope.RunID != "" {
		if err := rec.RecordProxyToolCallArgs(r.Context(), scope.RunID, "write_handover", map[string]any{"handover": handover}); err != nil && !wrote {
			cliError(w, err)
			return
		}
		wrote = true
	}
	if !wrote {
		http.Error(w, "this deployment's sandbox proxy cannot record handovers: use the write_handover tool", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Recorded standing handover summary (%d bytes).\n", len(handover))
}

// serveNotify answers POST /cli/notify (notify_operator).
func (p *GitProxy) serveNotify(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	ta, ok := p.taskActions()
	if !ok {
		http.Error(w, "this deployment's sandbox proxy cannot raise notifications: use the notify_operator tool", http.StatusNotImplemented)
		return
	}
	var req CLINotifyRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Subject) == "" {
		http.Error(w, "--subject is required: a short key for what this notification is about", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		http.Error(w, "--title is required: one line the operator reads first", http.StatusBadRequest)
		return
	}
	res, err := ta.NotifyOperator(r.Context(), scope.TaskIDStr, req)
	if err != nil {
		cliError(w, err)
		return
	}
	writeCLIJSON(w, http.StatusOK, res)
}

// serveTaskNotes answers POST /cli/tasks/<id>/notes (message_task).
func (p *GitProxy) serveTaskNotes(w http.ResponseWriter, r *http.Request, scope namespace.Scope, targetID string) {
	ta, ok := p.taskActions()
	if !ok {
		http.Error(w, "this deployment's sandbox proxy cannot message other tasks: use the message_task tool", http.StatusNotImplemented)
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !readCLIJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		http.Error(w, "a message body is required", http.StatusBadRequest)
		return
	}
	msgID, err := ta.MessageTask(r.Context(), scope.TaskIDStr, targetID, req.Body)
	if err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Left a note on task #%s (comment %d).\n", model.NormalizeTaskID(targetID), msgID)
}

// serveTasksWait answers POST /cli/tasks/wait (wait_for_task blocking wait).
func (p *GitProxy) serveTasksWait(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	ta, ok := p.taskActions()
	if !ok {
		http.Error(w, "this deployment's sandbox proxy cannot wait on tasks: use the wait_for_task tool", http.StatusNotImplemented)
		return
	}
	var req CLITaskWaitRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	if req.Park || strings.TrimSpace(req.ResumeAt) != "" {
		http.Error(w, "parking a task ends the run and is a model-only control operation: call the wait_for_task or resume_later MCP tool instead", http.StatusForbidden)
		return
	}
	var ids []string
	seen := map[string]bool{}
	for _, raw := range req.IDs {
		id := model.NormalizeTaskID(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		http.Error(w, "name at least one task to wait on: grain task wait <id>...", http.StatusBadRequest)
		return
	}
	until := strings.TrimSpace(req.Until)
	switch until {
	case "", "closed":
		until = "closed"
	case "merged":
	default:
		http.Error(w, fmt.Sprintf("until must be \"closed\" or \"merged\", not %q", until), http.StatusBadRequest)
		return
	}
	untilMerged := until == "merged"
	ctx := r.Context()

	targets, err := ta.CheckWaitTargets(ctx, scope.TaskIDStr, ids)
	if err != nil {
		cliError(w, err)
		return
	}
	if waitTargetsSettled(targets, untilMerged) {
		writeCLIJSON(w, http.StatusOK, CLITaskWaitResponse{
			Status:  "settled",
			Until:   until,
			Targets: targets,
			Summary: describeWaitTargets(targets, untilMerged),
		})
		return
	}

	slice := DefaultCLIWaitSlice
	if req.TimeoutSeconds > 0 {
		slice = min(slice, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	start := time.Now()
	end := start.Add(slice)
	if deadline, ok := ctx.Deadline(); ok {
		if d := deadline.Add(-cliWaitDeadlineMargin); d.Before(end) {
			end = d
		}
	}

	phrase := mcp.ToolActivityNote("wait_for_task")
	p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)

	lastReport := start
	var credit CLIWaitCredit
	report := func(now time.Time) {
		credit = p.creditCLIWait(ctx, scope, now.Sub(lastReport))
		lastReport = now
	}

	poll := time.NewTicker(cliWaitPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(cliWaitHeartbeat)
	defer heartbeat.Stop()
	for !waitTargetsSettled(targets, untilMerged) {
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
		case <-poll.C:
			if got, err := ta.CheckWaitTargets(ctx, scope.TaskIDStr, ids); err == nil {
				targets = got
			}
		case now := <-heartbeat.C:
			p.noteDeclaredWait(ctx, scope.TaskIDStr, phrase)
			report(now)
		case <-timer.C:
		}
		timer.Stop()
		if ctx.Err() != nil {
			break
		}
	}
	now := time.Now()
	report(now)
	waited := now.Sub(start)

	status := "waiting"
	if waitTargetsSettled(targets, untilMerged) {
		status = "settled"
	}
	writeCLIJSON(w, http.StatusOK, CLITaskWaitResponse{
		Status:      status,
		Until:       until,
		Targets:     targets,
		Summary:     describeWaitTargets(targets, untilMerged),
		WaitedMS:    waited.Milliseconds(),
		CreditMS:    credit.Credited.Milliseconds(),
		CreditCapMS: credit.Cap.Milliseconds(),
		CreditLive:  credit.Live,
	})
}

func waitTargetsSettled(targets []model.WaitTarget, untilMerged bool) bool {
	for _, t := range targets {
		if settled, _ := t.Settled(untilMerged); !settled {
			return false
		}
	}
	return true
}

func describeWaitTargets(targets []model.WaitTarget, untilMerged bool) string {
	var b strings.Builder
	for _, t := range targets {
		settled, met := t.Settled(untilMerged)
		switch {
		case settled && met:
			b.WriteString("[reached] ")
		case settled:
			b.WriteString("[will not reach it: closed without merging] ")
		default:
			b.WriteString("[still waiting] ")
		}
		b.WriteString(t.Describe())
	}
	return b.String()
}

// serveProposals answers POST /cli/proposals (grain task propose), enforcing
// hold_for_approval=true, auto_merge=false, no supersedes, and a per-attempt
// cap of 10 proposals on the sandbox credential.
func (p *GitProxy) serveProposals(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	ta, ok := p.taskActions()
	if !ok {
		http.Error(w, "this deployment's sandbox proxy cannot file proposals: use the propose_task tool", http.StatusNotImplemented)
		return
	}
	var req CLIProposeRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Supersedes) != "" {
		http.Error(w, "superseding an existing task is not permitted on the sandbox credential: use the propose_task MCP control tool instead", http.StatusForbidden)
		return
	}
	if req.AutoMerge != nil && *req.AutoMerge {
		http.Error(w, "auto_merge is not permitted on the sandbox credential: grain task propose always files unapproved with auto_merge=false; use the propose_task MCP control tool to inherit auto-merge", http.StatusForbidden)
		return
	}
	if req.HoldForApproval != nil && !*req.HoldForApproval {
		http.Error(w, "hold_for_approval=false is not permitted on the sandbox credential: grain task propose always holds proposals for human approval; use the propose_task MCP control tool to inherit auto-approval", http.StatusForbidden)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	if req.Title == "" {
		http.Error(w, "--title is required", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "--body (or --file) is required", http.StatusBadRequest)
		return
	}
	limitKey := scope.RunID
	if limitKey == "" {
		limitKey = fmt.Sprintf("task:%s#%d", scope.TaskIDStr, scope.Attempt)
	}
	if !p.cliProposals.take(limitKey) {
		http.Error(w, fmt.Sprintf("this attempt has already filed %d proposals via the sandbox CLI, the per-attempt cap", MaxCLIProposalsPerAttempt), http.StatusTooManyRequests)
		return
	}
	hold := true
	auto := false
	req.HoldForApproval = &hold
	req.AutoMerge = &auto
	req.Supersedes = ""

	res, err := ta.ProposeTask(r.Context(), scope.TaskIDStr, scope.RunID, req)
	if err != nil {
		cliError(w, err)
		return
	}
	res.HoldForApproval = true
	res.AutoMerge = false
	writeCLIJSON(w, http.StatusOK, res)
}

// serveQuarantine answers POST /cli/quarantine (and POST /cap/quarantine).
func (p *GitProxy) serveQuarantine(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	if !slices.Contains(scope.Grants, "test-quarantine") && !slices.Contains(scope.Grants, "quarantine-test") {
		cliError(w, namespace.RefuseCapability("grain quarantine", "test-quarantine"))
		return
	}
	var req CLIQuarantineRequest
	if !readCLIJSON(w, r, &req) {
		return
	}
	testName := strings.TrimSpace(req.TestName)
	reason := strings.TrimSpace(req.Reason)
	if testName == "" {
		http.Error(w, "test_name is required: grain quarantine <test> --reason <why>", http.StatusBadRequest)
		return
	}
	if reason == "" {
		http.Error(w, "--reason is required: explain why the test is being quarantined", http.StatusBadRequest)
		return
	}
	fileFollowUp := true
	if req.FileFollowUp != nil {
		fileFollowUp = *req.FileFollowUp
	}
	var followUpID string
	if fileFollowUp {
		if ta, ok := p.taskActions(); ok {
			limitKey := scope.RunID
			if limitKey == "" {
				limitKey = fmt.Sprintf("task:%s#%d", scope.TaskIDStr, scope.Attempt)
			}
			if !p.cliProposals.take(limitKey) {
				http.Error(w, fmt.Sprintf("this attempt has already filed %d proposals via the sandbox CLI, the per-attempt cap", MaxCLIProposalsPerAttempt), http.StatusTooManyRequests)
				return
			}
			hold := true
			auto := false
			filed, err := ta.ProposeTask(r.Context(), scope.TaskIDStr, scope.RunID, CLIProposeRequest{
				ID:              "quarantine-fix-" + strings.ToLower(strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(testName)),
				Title:           fmt.Sprintf("fix(test): unquarantine and fix flaky test %s", testName),
				Body:            fmt.Sprintf("Test `%s` was quarantined by task %s.\n\nReason: %s\n\nPlease investigate the root cause, fix the flake, and remove the entry from .grain/quarantine.yml.", testName, scope.TaskIDStr, reason),
				Repo:            scope.TaskRepo,
				Branch:          scope.TaskBase,
				HoldForApproval: &hold,
				AutoMerge:       &auto,
			})
			if err == nil {
				followUpID = filed.ID
			}
		}
	}
	writeCLIJSON(w, http.StatusOK, CLIQuarantineResult{
		TaskID:         scope.TaskIDStr,
		FollowUpTaskID: followUpID,
	})
}

// serveAPIRoutes answers GET /cli/api-routes.
func (p *GitProxy) serveAPIRoutes(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	ta, ok := p.taskActions()
	if !ok {
		http.Error(w, "this deployment's sandbox proxy cannot list API routes", http.StatusNotImplemented)
		return
	}
	routes, err := ta.ListAPIRoutes(r.Context(), scope.TaskIDStr, scope.Grants, strings.TrimSpace(r.URL.Query().Get("scope")))
	if err != nil {
		cliError(w, err)
		return
	}
	writeCLIJSON(w, http.StatusOK, routes)
}
