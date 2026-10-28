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
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/namespace"
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
	// CIRun reads one run by id.
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

// cliActionRoutes are the routes this file serves, by method.
var cliActionRoutes = map[string]string{
	"ci/wait":           http.MethodPost,
	"ci/run-job":        http.MethodPost,
	"ci/retrigger":      http.MethodPost,
	"pr/open":           http.MethodPost,
	"pr/reply":          http.MethodPost,
	"pr/resolve":        http.MethodPost,
	"pr/review-comment": http.MethodPost,
}

func isRevertInfoRoute(route string) bool {
	rest, ok := strings.CutPrefix(route, "tasks/")
	if !ok {
		return false
	}
	id, tail, _ := strings.Cut(rest, "/")
	return id != "" && tail == "revert-info"
}

// serveCLIAction serves route when it is one of the acting routes, and
// reports whether it was.
func (p *GitProxy) serveCLIAction(w http.ResponseWriter, r *http.Request, scope namespace.Scope, route string) bool {
	method, ok := cliActionRoutes[route]
	if !ok && isRevertInfoRoute(route) {
		method, ok = http.MethodGet, true
	}
	if !ok {
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, fmt.Sprintf("/cli/%s takes %s", route, method), http.StatusMethodNotAllowed)
		return true
	}
	if p.CLIActions == nil {
		http.Error(w, "this deployment's sandbox proxy is not wired to act on tasks: use the MCP tools", http.StatusNotImplemented)
		return true
	}
	if scope.TaskIDStr == "" {
		http.Error(w, "forbidden: this sandbox has no task to act for", http.StatusForbidden)
		return true
	}
	switch route {
	case "ci/wait", "ci/run-job":
		p.serveCIWait(w, r, scope, route == "ci/run-job")
	case "ci/retrigger":
		p.serveCIRetrigger(w, r, scope)
	case "pr/open":
		res, err := p.CLIActions.OpenPullRequest(r.Context(), scope.TaskIDStr)
		writeActionResult(w, res, err)
	case "pr/reply":
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
	case "pr/resolve":
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
	case "pr/review-comment":
		p.serveReviewComment(w, r, scope)
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
	Selective   bool            `json:"selective,omitempty"`
	Jobs        []CLIJobVerdict `json:"jobs"`
	WaitedMS    int64           `json:"waited_ms"`
	CreditMS    int64           `json:"credit_used_ms"`
	CreditCapMS int64           `json:"credit_cap_ms,omitempty"`
	CreditLive  bool            `json:"credit_live"`
}

func (p *GitProxy) serveCIWait(w http.ResponseWriter, r *http.Request, scope namespace.Scope, single bool) {
	if err := requireOwnCI(scope); err != nil {
		cliError(w, err)
		return
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

	var run ci.Run
	if id := strings.TrimSpace(req.ResumeRunID); id != "" {
		got, ok := p.CLIActions.CIRun(ctx, id)
		if !ok {
			cliError(w, fmt.Errorf("%w: no CI run %q -- a run is remembered for a while and then forgotten", namespace.ErrNotFound, id))
			return
		}
		if !strings.EqualFold(got.Repo, scope.TaskRepo) || got.Ref != scope.TaskBranch {
			cliError(w, fmt.Errorf("%w: CI run %s is not on this task's branch (%s in %s)", namespace.ErrForbidden, id, scope.TaskBranch, scope.TaskRepo))
			return
		}
		run = got
	} else {
		started, err := p.CLIActions.StartTaskCI(ctx, scope.TaskIDStr, CIStartRequest{Jobs: jobs})
		if err != nil {
			cliError(w, err)
			return
		}
		run = started
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
	for !run.Conclusion.Done() {
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
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
		if ctx.Err() != nil {
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
