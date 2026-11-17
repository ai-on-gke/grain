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

// cli.go serves /cli/: the reads of the in-sandbox grain CLI (pkg/cli's
// sandboxcli.go) that are queries over grain's records rather than files
// in the /fs/ namespace -- recent CI runs, a test's history, the CI
// verdict on the task's own branch and on its base. They authenticate and
// are scoped exactly as /fs/ is (the sandbox's proxy token, the live run's
// model.NamespaceScope), are narrated and counted the same way
// (activity.go), and refuse a run that lacks a grant with the sentence the
// MCP tool it replaces would (namespace.RefuseCapability).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

func isCLIPath(path string) bool {
	return path == "/cli" || strings.HasPrefix(path, "/cli/")
}

// cliRouteTools names each /cli/ read after the MCP tool it replaces.
var cliRouteTools = map[string]string{
	"ci/runs":         "list_ci_runs",
	"ci/run":          "list_ci_runs",
	"ci/show":         "get_ci_log",
	"ci/checks":       "list_ci_runs",
	"ci/test-history": "query_test_history",
	"pr/status":       "pull_request_status",
	"pr/base-status":  "base_branch_status",
	"issues":          "list_issues",
	"issue":           "read_issue",
	"db/query":        "query_grain_db",
	"tasks":           "list_grain_tasks",
	// The acting routes (cli_actions.go).
	"ci/wait":           "wait_for_checks",
	"ci/run-job":        "run_ci_job",
	"ci/retrigger":      "retrigger_ci",
	"pr/open":           "open_pull_request",
	"pr/reply":          "reply_to_pull_request_comment",
	"pr/resolve":        "resolve_review_thread",
	"pr/threads":        "list_review_threads",
	"pr/review-comment": "add_review_comment",
	"revert-info":       "revert_task",
	"status":            "update_status",
	"handover":          "write_handover",
	"report/check":      "check_report",
	"notify":            "notify_operator",
	"tasks/notes":       "message_task",
	"tasks/wait":        "wait_for_task",
	"proposals":         "propose_task",
	"quarantine":        "api_quarantine_test",
}

// CLIRouteActivity names a /cli/<route> request after the MCP tool it
// stands in for.
func CLIRouteActivity(path string) RouteActivity {
	route := strings.Trim(strings.TrimPrefix(path, "/cli"), "/")
	var targets []string
	if strings.HasPrefix(route, "issues/") {
		route = "issue"
	}
	if isRevertInfoRoute(route) {
		id := strings.TrimSuffix(strings.TrimPrefix(route, "tasks/"), "/revert-info")
		if id != "" {
			targets = []string{id}
		}
		route = "revert-info"
	}
	if isTaskNotesRoute(route) {
		id := strings.TrimSuffix(strings.TrimPrefix(route, "tasks/"), "/notes")
		if id != "" {
			targets = []string{id}
		}
		route = "tasks/notes"
	}
	if route == "context" || route == "api-routes" {
		// The CLI's own bookkeeping, not something the run asked for:
		// neither narrated nor counted.
		return RouteActivity{}
	}
	tool, ok := cliRouteTools[route]
	if !ok {
		// A route the proxy does not serve answers 404 and is not
		// counted: the path is the sandbox's own choice, and naming a
		// task_run_tool row after it would let a run write as many
		// rows as it cared to invent.
		return RouteActivity{}
	}
	phrase := mcp.ToolActivityNote(tool)
	if route == "status" {
		// /cli/status writes the caller's custom status note directly.
		phrase = ""
	}
	return RouteActivity{Tool: tool, Phrase: phrase, Targets: targets}
}

// ServeCLIHTTP handles a request under /cli/<route>.
func (p *GitProxy) ServeCLIHTTP(w http.ResponseWriter, r *http.Request) {
	grant, ok := p.authenticateSandboxGrant(w, r)
	if !ok {
		return
	}
	if !p.nsLimiter.acquire(grant.Sandbox, p.MaxNamespaceConcurrency) {
		http.Error(w, "too many concurrent requests from this sandbox", http.StatusTooManyRequests)
		return
	}
	defer p.nsLimiter.release(grant.Sandbox)
	modelScope, ok := p.resolveSandboxScope(w, r, grant)
	if !ok {
		return
	}
	scope := toNamespaceScope(modelScope)
	act := CLIRouteActivity(r.URL.Path)
	p.noteActivity(r.Context(), modelScope.TaskID, act.Phrase)
	cw := newCountingResponseWriter(w, act)
	p.serveCLIRoute(cw, r, scope)
	p.recordRouteCall(r.Context(), modelScope.RunID, act, cw)
}

func (p *GitProxy) serveCLIRoute(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	route := strings.Trim(strings.TrimPrefix(r.URL.Path, "/cli"), "/")
	if p.serveCLIAction(w, r, scope, route) {
		return
	}
	q := r.URL.Query()
	switch {
	case route == "context":
		// What the CLI needs to build namespace paths for this run.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"task": scope.TaskIDStr, "repo": scope.TaskRepo, "branch": scope.TaskBranch, "base": scope.TaskBase,
		})
	case route == "ci/runs":
		p.serveCIRuns(w, r, scope)
	case route == "ci/run":
		p.serveCIRunRepo(w, q, scope)
	case route == "ci/show":
		p.serveCIShow(w, r, scope)
	case route == "ci/checks":
		p.serveCIChecks(w, r, scope)
	case route == "ci/test-history":
		p.serveTestHistory(w, r, scope)
	case route == "pr/status":
		p.servePRStatus(w, r, scope)
	case route == "tasks":
		p.serveTasks(w, r, scope)
	case route == "pr/base-status":
		if scope.TaskRepo == "" {
			// Without a repository "main" is every repository's main.
			cliError(w, errNoTaskRepo)
			return
		}
		base := scope.TaskBase
		if base == "" {
			base = "main"
		}
		p.serveBranchStatus(w, scope, "", base, "the base branch")
	case route == "issues" || strings.HasPrefix(route, "issues/"):
		if !scope.MayReadIssues {
			cliError(w, namespace.RefuseCapability("grain issue", model.GitHubReadIssuesRef))
			return
		}
		p.serveIssues(w, r, scope, strings.TrimPrefix(strings.TrimPrefix(route, "issues"), "/"))
	case route == "db/query":
		if !scope.MayReadDB {
			cliError(w, namespace.RefuseCapability("grain db query", "https://grain.dev/capabilities/grain#sql"))
			return
		}
		p.serveDBQuery(w, r, scope)
	default:
		http.Error(w, fmt.Sprintf("unknown /cli/ route %q", route), http.StatusNotFound)
	}
}

// CLIBackend builds, for one run, the MCP tools the /cli/ reads below
// stand in for, so that each answers with the very text its tool would:
// list_issues and read_issue (pkg/mcp's issue_tools.go) for
// /cli/issues, and query_grain_db (pkg/capability/dbaccess) for
// /cli/db/query. The grant is checked here, before the backend is asked;
// the backend decides what the run reads with (its git credential, its
// task id).
type CLIBackend interface {
	IssueTools(ctx context.Context, scope namespace.Scope) []mcp.Tool
	// DBQueryTool is query_grain_db, read-only whatever else the run
	// holds: the proxy serves reads, and db-write's execute_grain_db
	// stays a tool.
	DBQueryTool(ctx context.Context, scope namespace.Scope) mcp.Tool
}

// serveIssues answers /cli/issues (list_issues) and /cli/issues/<n>
// (read_issue).
func (p *GitProxy) serveIssues(w http.ResponseWriter, r *http.Request, scope namespace.Scope, number string) {
	if p.CLI == nil {
		http.Error(w, "grain issue is not served by this proxy: use the list_issues and read_issue tools", http.StatusNotImplemented)
		return
	}
	q := r.URL.Query()
	args := map[string]any{}
	if repo := strings.TrimSpace(q.Get("repo")); repo != "" {
		args["repo"] = repo
	}
	name := "list_issues"
	if number != "" {
		n, err := strconv.Atoi(number)
		if err != nil || n <= 0 || strings.Contains(number, "/") {
			http.Error(w, fmt.Sprintf("%q is not an issue number", number), http.StatusBadRequest)
			return
		}
		name = "read_issue"
		args["issue_number"] = n
		if v := q.Get("comments"); v == "0" || v == "false" {
			args["include_comments"] = false
		}
	} else {
		if label := strings.TrimSpace(q.Get("label")); label != "" {
			args["label"] = label
		}
		if limit := cliLimit(q, "limit", 0, 100); limit > 0 {
			args["limit"] = limit
		}
	}
	for _, tool := range p.CLI.IssueTools(r.Context(), scope) {
		if tool.Name == name {
			writeToolResult(w, tool.Handler(r.Context(), args))
			return
		}
	}
	http.Error(w, "grain issue is not served by this proxy: use the list_issues and read_issue tools", http.StatusNotImplemented)
}

// serveDBQuery answers /cli/db/query?sql=... (query_grain_db).
func (p *GitProxy) serveDBQuery(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	if p.CLI == nil {
		http.Error(w, "grain db query is not served by this proxy: use the query_grain_db tool", http.StatusNotImplemented)
		return
	}
	sql := strings.TrimSpace(r.URL.Query().Get("sql"))
	if sql == "" {
		http.Error(w, "sql is required", http.StatusBadRequest)
		return
	}
	tool := p.CLI.DBQueryTool(r.Context(), scope)
	if tool.Handler == nil {
		http.Error(w, "grain db query is not served by this proxy: use the query_grain_db tool", http.StatusNotImplemented)
		return
	}
	writeToolResult(w, tool.Handler(r.Context(), map[string]any{"query": sql}))
}

// writeToolResult writes what a tool answered: its text, with 422 rather
// than 200 when the tool answered with an error, so the CLI exits
// non-zero and prints it.
func writeToolResult(w http.ResponseWriter, res mcp.Result) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if res.IsError {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	text := res.Text
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	fmt.Fprint(w, text)
}

func cliError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ci.ErrNotUp):
		// The daemon is restarting: ask again shortly.
		status = http.StatusServiceUnavailable
	case errors.Is(err, namespace.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, namespace.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, namespace.ErrBadRequest):
		status = http.StatusBadRequest
	}
	http.Error(w, err.Error(), status)
}

// ciReadRepo resolves the repository a CI query is about -- the task's own
// unless repo names another -- and refuses one outside the run's scope.
func (p *GitProxy) ciReadRepo(scope namespace.Scope, repo string) (string, ci.RunStore, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		repo = scope.TaskRepo
	}
	if !scope.PermitsCI("") {
		return "", nil, namespace.RefuseCIRead("grain ci", scope.TaskRepo)
	}
	if repo == "" && !scope.MayReadAllCI {
		// An empty filter matches every repository's runs (ci.RunFilter),
		// which only a scope reading all CI may ask for.
		return "", nil, fmt.Errorf("%w: this task has no repository of its own: name one with --repo", namespace.ErrBadRequest)
	}
	if !scope.PermitsCI(repo) {
		return "", nil, fmt.Errorf("%w: %s is outside this sandbox's CI read scope", namespace.ErrForbidden, repo)
	}
	if p.Namespace == nil || p.Namespace.CIStore == nil {
		return "", nil, fmt.Errorf("%w: CI store is unavailable", namespace.ErrNotFound)
	}
	return repo, p.Namespace.CIStore, nil
}

func cliLimit(q url.Values, key string, def, max int) int {
	n, err := strconv.Atoi(q.Get(key))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// errNoTaskRepo answers a command about this task's own branch on a task
// with no repository (no_repo): its scope may still name a branch, but no
// repository has it, so nothing is reported about it.
var errNoTaskRepo = fmt.Errorf("%w: this task has no repository or branch; pass --repo <owner/repo> [--ref <b>] or use `grain ci list --repo ...`", namespace.ErrBadRequest)

// TaskRefLookup is what a NamespaceScopeLookup also implements to name
// another task's repository and branch, for /cli/ci/runs?task= and
// /cli/pr/status?task=. Implemented by *model.Store.
type TaskRefLookup interface {
	TaskCIRef(ctx context.Context, taskID string) (repo, branch string, found bool, err error)
}

// taskCIRef resolves task to the repository and branch its CI runs are
// recorded under, refusing a task whose repository is outside the run's
// CI read scope in the same words as one that does not exist.
func (p *GitProxy) taskCIRef(ctx context.Context, scope namespace.Scope, task string) (repo, branch string, err error) {
	task = strings.TrimSpace(strings.TrimPrefix(task, "#"))
	if !scope.PermitsCI("") {
		return "", "", namespace.RefuseCIRead("grain ci", scope.TaskRepo)
	}
	if own := scope.TaskIDStr; own != "" && strings.EqualFold(own, task) {
		if scope.TaskRepo == "" {
			return "", "", errNoTaskRepo
		}
		return scope.TaskRepo, scope.TaskBranch, nil
	}
	lookup, ok := p.NamespaceScopes.(TaskRefLookup)
	if !ok {
		return "", "", fmt.Errorf("%w: this proxy cannot look up another task's branch: name it with --ref", namespace.ErrNotFound)
	}
	repo, branch, found, err := lookup.TaskCIRef(ctx, task)
	if err != nil {
		return "", "", err
	}
	if !found || repo == "" || !scope.PermitsCI(repo) {
		return "", "", fmt.Errorf("%w: task %s is not a task in a repository this sandbox may read CI for", namespace.ErrNotFound, task)
	}
	return repo, branch, nil
}

func (p *GitProxy) serveCIRuns(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	q := r.URL.Query()
	repoArg, ref, exact := q.Get("repo"), q.Get("ref"), ""
	if task := strings.TrimSpace(q.Get("task")); task != "" {
		taskRepo, branch, err := p.taskCIRef(r.Context(), scope, task)
		if err != nil {
			cliError(w, err)
			return
		}
		if repoArg != "" && !strings.EqualFold(repoArg, taskRepo) {
			cliError(w, fmt.Errorf("%w: task %s works in %s, not %s", namespace.ErrBadRequest, task, taskRepo, repoArg))
			return
		}
		repoArg, ref, exact = taskRepo, branch, branch
	}
	repo, store, err := p.ciReadRepo(scope, repoArg)
	if err != nil {
		cliError(w, err)
		return
	}
	if ref == "@branch" {
		if scope.TaskRepo == "" {
			cliError(w, errNoTaskRepo)
			return
		}
		ref = scope.TaskBranch
	}
	f := ci.RunFilter{
		Repo:       repo,
		Ref:        ref,
		Job:        q.Get("job"),
		Workflow:   strings.TrimSpace(q.Get("workflow")),
		Descending: true,
	}
	for _, c := range strings.Split(q.Get("conclusion"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			f.States = append(f.States, c)
		}
	}
	if since := strings.TrimSpace(q.Get("since")); since != "" {
		at, err := ci.ParseSince(since, time.Now())
		if err != nil {
			cliError(w, fmt.Errorf("%w: %v", namespace.ErrBadRequest, err))
			return
		}
		f.Since = at
	}
	limit := cliLimit(q, "limit", 20, 200)
	switch f.Provider = strings.ToLower(strings.TrimSpace(q.Get("provider"))); f.Provider {
	case "", ci.ProviderGrain, ci.ProviderGitHubActions:
	default:
		cliError(w, fmt.Errorf("%w: %q is not a CI provider: use %s or %s", namespace.ErrBadRequest, q.Get("provider"),
			ci.ProviderGrain, ci.ProviderGitHubActions))
		return
	}
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	switch format {
	case "", "text":
		format = ""
	case formatGitHub, formatGitHubJobs:
		if f.Provider == ci.ProviderGrain {
			cliError(w, fmt.Errorf("%w: --format %s is GitHub's shape, which only GitHub Actions runs have: leave out --provider grain", namespace.ErrBadRequest, format))
			return
		}
		f.Provider = ci.ProviderGitHubActions
		most := ci.MaxListedRuns
		if format == formatGitHubJobs {
			most = maxGitHubJobsRuns
		}
		limit = cliLimit(q, "limit", 20, most)
	default:
		cliError(w, fmt.Errorf("%w: %q is not a format: use text, %s or %s", namespace.ErrBadRequest, q.Get("format"), formatGitHub, formatGitHubJobs))
		return
	}
	if exact != "" {
		// Ref matches as a substring, so task-19's branch would find
		// task-190's runs too: read a wider page and keep the branch's own.
		page := p.findCIRuns(r.Context(), store, f, max(200, limit))
		var runs []ci.Run
		for _, run := range page.Runs {
			if strings.EqualFold(run.Ref, exact) && len(runs) < limit {
				runs = append(runs, run)
			}
		}
		if format != "" {
			p.writeGitHubRuns(r.Context(), w, format, runs, page.note)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%d run(s) of task %s on %s in %s, newest first:\n", len(runs), strings.TrimPrefix(strings.TrimSpace(q.Get("task")), "#"), exact, repo)
		for _, run := range runs {
			fmt.Fprintln(w, formatCIRunLine(run))
		}
		return
	}
	page := p.findCIRuns(r.Context(), store, f, limit)
	if format != "" {
		p.writeGitHubRuns(r.Context(), w, format, page.Runs, page.note)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if repo == "" {
		// No repository filter: the runs are every readable repository's,
		// so each row says whose it is.
		fmt.Fprintf(w, "%d of %d matching run(s) across all readable repos, newest first:\n", len(page.Runs), page.Matched)
		for _, run := range page.Runs {
			fmt.Fprintln(w, run.Repo+" "+formatCIRunLine(run))
		}
		return
	}
	fmt.Fprintf(w, "%d of %d matching run(s) in %s, newest first:\n", len(page.Runs), page.Matched, repo)
	for _, run := range page.Runs {
		fmt.Fprintln(w, formatCIRunLine(run))
	}
	if page.note != "" {
		fmt.Fprintf(w, "(GitHub Actions: %s)\n", page.note)
	}
}

// The --format values `grain ci list` writes GitHub's own REST shapes in,
// for scripts that read GitHub's answers (scripts/candidate-gate.sh):
// formatGitHub is GET .../actions/runs ({workflow_runs}), and
// formatGitHubJobs GET .../actions/runs/{id}/jobs?filter=all ({jobs}),
// every attempt of every job of each run listed, concatenated.
const (
	formatGitHub     = "github"
	formatGitHubJobs = "github-jobs"
	// maxGitHubJobsRuns bounds the runs formatGitHubJobs reads the jobs
	// of, one forge request (or more) each.
	maxGitHubJobsRuns = 50
)

// githubRunsAnswer is formatGitHub's answer: GitHub's, and grain's note on
// what it is short of (runs it left out, a forge it could not read), which
// GitHub's has no field for and a reader of GitHub's shape ignores.
type githubRunsAnswer struct {
	TotalCount   int                    `json:"total_count"`
	WorkflowRuns []ci.GitHubWorkflowRun `json:"workflow_runs"`
	Note         string                 `json:"grain_note,omitempty"`
}

// githubJobsAnswer is formatGitHubJobs' answer.
type githubJobsAnswer struct {
	TotalCount int            `json:"total_count"`
	Jobs       []ci.GitHubJob `json:"jobs"`
	Note       string         `json:"grain_note,omitempty"`
}

// writeGitHubRuns writes runs in format's GitHub shape. A run not in
// GitHub's own words (ci.GitHubWorkflowRunOf) is left out and counted in
// the note, never made up: the reader judges releases on these verdicts.
// A run whose jobs the forge would not give fails the whole answer, since
// a listing missing a run's failed attempt reads as a pass.
func (p *GitProxy) writeGitHubRuns(ctx context.Context, w http.ResponseWriter, format string, runs []ci.Run, note string) {
	var notes []string
	if note != "" {
		notes = append(notes, "GitHub Actions: "+note)
	}
	var shaped []ci.GitHubWorkflowRun
	var ids []string
	skipped := 0
	for _, run := range runs {
		gh, ok := ci.GitHubWorkflowRunOf(run)
		if !ok {
			skipped++
			continue
		}
		shaped = append(shaped, gh)
		ids = append(ids, run.ID)
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d run(s) left out: grain has them only from its record of their jobs, not as the forge's workflow runs", skipped))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if format == formatGitHub {
		if shaped == nil {
			shaped = []ci.GitHubWorkflowRun{}
		}
		_ = json.NewEncoder(w).Encode(githubRunsAnswer{TotalCount: len(shaped), WorkflowRuns: shaped, Note: strings.Join(notes, "; ")})
		return
	}
	attempts, ok := p.externalAttempts()
	if !ok && len(ids) > 0 {
		cliError(w, fmt.Errorf("%w: this proxy cannot read GitHub Actions jobs", namespace.ErrNotFound))
		return
	}
	jobs := []ci.GitHubJob{}
	for _, id := range ids {
		got, err := attempts.ExternalJobAttempts(ctx, id)
		if err != nil {
			cliError(w, fmt.Errorf("%w: the jobs of %s: %v", namespace.ErrNotFound, id, err))
			return
		}
		jobs = append(jobs, got...)
	}
	_ = json.NewEncoder(w).Encode(githubJobsAnswer{TotalCount: len(jobs), Jobs: jobs, Note: strings.Join(notes, "; ")})
}

// externalAttempts is the namespace's ExternalCI as ci.ExternalAttempts.
func (p *GitProxy) externalAttempts() (ci.ExternalAttempts, bool) {
	if p.Namespace == nil || p.Namespace.ExternalCI == nil {
		return nil, false
	}
	attempts, ok := p.Namespace.ExternalCI.(ci.ExternalAttempts)
	return attempts, ok
}

// githubChecksAnswer is `grain ci checks --format github`'s answer:
// GitHub's GET .../commits/{sha}/check-runs, and the App grain's own forge
// connection posts its check runs as, which a reader that trusts only
// grain's (candidate-gate.sh's CHECK_QUALIFICATION_APP) matches each
// check run's app against.
type githubChecksAnswer struct {
	TotalCount int                 `json:"total_count"`
	CheckRuns  []ci.GitHubCheckRun `json:"check_runs"`
	GrainApp   string              `json:"grain_app"`
}

// serveCIChecks answers /cli/ci/checks?sha=<commit>[&repo=][&name=][&format=github]:
// every check run (every attempt of each, GitHub's filter=all) on a commit
// of a repository this sandbox may read CI for, read through grain's own
// forge connection.
func (p *GitProxy) serveCIChecks(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	q := r.URL.Query()
	repo, _, err := p.ciReadRepo(scope, q.Get("repo"))
	if err != nil {
		cliError(w, err)
		return
	}
	if repo == "" {
		cliError(w, fmt.Errorf("%w: name the repository with --repo", namespace.ErrBadRequest))
		return
	}
	sha := strings.TrimSpace(q.Get("sha"))
	if !isCommitSHA(sha) {
		cliError(w, fmt.Errorf("%w: %q is not a commit sha", namespace.ErrBadRequest, sha))
		return
	}
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	if format != "" && format != "text" && format != formatGitHub {
		cliError(w, fmt.Errorf("%w: %q is not a format: use text or %s", namespace.ErrBadRequest, q.Get("format"), formatGitHub))
		return
	}
	attempts, ok := p.externalAttempts()
	if !ok {
		cliError(w, fmt.Errorf("%w: this proxy cannot read check runs from the forge", namespace.ErrNotFound))
		return
	}
	name := strings.TrimSpace(q.Get("name"))
	runs, app, err := attempts.ExternalCheckRuns(r.Context(), repo, sha, name)
	if err != nil {
		cliError(w, fmt.Errorf("%w: %v", namespace.ErrNotFound, err))
		return
	}
	if runs == nil {
		runs = []ci.GitHubCheckRun{}
	}
	if format == formatGitHub {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(githubChecksAnswer{TotalCount: len(runs), CheckRuns: runs, GrainApp: app})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	what := "check run(s)"
	if name != "" {
		what = fmt.Sprintf("attempt(s) of check run %q", name)
	}
	fmt.Fprintf(w, "%d %s on %s in %s:\n", len(runs), what, shortSHA(sha), repo)
	for _, run := range runs {
		verdict := run.Status
		if run.Conclusion != nil {
			verdict = *run.Conclusion
		}
		from := "no app"
		if run.App != nil {
			from = run.App.Slug
			if app != "" && run.App.Slug == app {
				from += " (grain)"
			}
		}
		fmt.Fprintf(w, "%-12s %-40s %s  %s\n", verdict, run.Name, from, run.HTMLURL)
	}
}

// isCommitSHA reports whether s is a full or abbreviated hex commit sha.
func isCommitSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ciRunsPage is a page of runs from grain CI and the namespace's
// ExternalCI together, and why the external half is short, if it is.
type ciRunsPage struct {
	ci.RunPage
	note string
}

// findCIRuns is store's runs matching f merged with the namespace's
// ExternalCI (GitHub Actions) runs of f.Repo, newest first, at most limit.
func (p *GitProxy) findCIRuns(ctx context.Context, store ci.RunStore, f ci.RunFilter, limit int) ciRunsPage {
	var page ci.RunPage
	if f.Provider == "" || f.Provider == ci.ProviderGrain {
		grain := f
		grain.Provider = ""
		page = store.Find(grain, limit)
	}
	if p.Namespace == nil || p.Namespace.ExternalCI == nil || f.Repo == "" || f.Provider == ci.ProviderGrain {
		return ciRunsPage{RunPage: page}
	}
	external, extMatched, note := ci.FindExternalRunsCounted(ctx, p.Namespace.ExternalCI, f, limit)
	if len(external) == 0 {
		page.Matched += extMatched
		page.Stored += extMatched
		return ciRunsPage{RunPage: page, note: note}
	}
	runs := append(append([]ci.Run(nil), page.Runs...), external...)
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i].StartedAt, runs[j].StartedAt
		switch {
		case a == nil:
			return b != nil
		case b == nil:
			return false
		}
		return a.After(*b)
	})
	if len(runs) > limit {
		runs = runs[:limit]
	}
	page.Runs = runs
	page.Matched += extMatched
	page.Stored += extMatched
	return ciRunsPage{RunPage: page, note: note}
}

// serveCIRunRepo answers /cli/ci/run?id=<run> with the run's repository
// and branch as JSON, so `grain ci log <run-id>` reads a run of any
// repository the sandbox may read CI for without being told which.
func (p *GitProxy) serveCIRunRepo(w http.ResponseWriter, q url.Values, scope namespace.Scope) {
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if !scope.PermitsCI("") {
		cliError(w, namespace.RefuseCIRead("grain ci", scope.TaskRepo))
		return
	}
	if p.Namespace == nil || p.Namespace.CIStore == nil {
		cliError(w, fmt.Errorf("%w: CI store is unavailable", namespace.ErrNotFound))
		return
	}
	run, ok := p.Namespace.CIStore.Get(id)
	var extErr error
	if !ok && p.Namespace.ExternalCI != nil && externalRunPermitted(scope, id) {
		run, ok, extErr = p.Namespace.ExternalCI.ExternalRun(context.Background(), id)
	}
	if !ok || run.Repo == "" || !scope.PermitsCI(run.Repo) {
		// A run outside the scope is answered as one nobody remembers.
		cliError(w, noCIRunError(id, extErr))
		return
	}
	runID := run.ID
	if p.Namespace.ExternalCI != nil && run.Source == ci.FaultSourceGitHub {
		// grain's polled record of an Actions run, named by its bare
		// GitHub id: the CLI reads it, and its job logs, by the gha: id
		// the forge serves them under (namespace.resolveCI does the same
		// for a path that names the bare id).
		// A forge that will not serve the run leaves the record's id.
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 {
			ghaID := ci.ActionsRunID(run.Repo, n)
			if _, ok, err := p.Namespace.ExternalCI.ExternalRun(context.Background(), ghaID); err == nil && ok {
				runID = ghaID
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": runID, "repo": run.Repo, "ref": run.Ref})
}

// serveCIShow answers /cli/ci/show?id=<run> with the run's line, then
// each job and its steps with their conclusions -- grain CI's or GitHub
// Actions' alike -- for `grain ci show`.
func (p *GitProxy) serveCIShow(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		cliError(w, fmt.Errorf("%w: a run id is required", namespace.ErrBadRequest))
		return
	}
	if !scope.PermitsCI("") {
		cliError(w, namespace.RefuseCIRead("grain ci", scope.TaskRepo))
		return
	}
	if p.Namespace == nil || p.Namespace.CIStore == nil {
		cliError(w, fmt.Errorf("%w: CI store is unavailable", namespace.ErrNotFound))
		return
	}
	run, ok := p.Namespace.CIStore.Get(id)
	var extErr error
	if !ok && p.Namespace.ExternalCI != nil && externalRunPermitted(scope, id) {
		if run, ok, extErr = p.Namespace.ExternalCI.ExternalRun(r.Context(), id); extErr != nil {
			ok = false
		}
	}
	if !ok || run.Repo == "" || !scope.PermitsCI(run.Repo) {
		cliError(w, noCIRunError(id, extErr))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%s (%s)\n", formatCIRunLine(run), run.Repo)
	if a := run.Actions; a != nil {
		if a.WorkflowName != "" || a.Workflow != "" {
			fmt.Fprintf(w, "workflow: %s %s, attempt %d, event %s\n", a.WorkflowName, a.Workflow, a.RunAttempt, run.Event)
		}
		if a.URL != "" {
			fmt.Fprintf(w, "url: %s\n", a.URL)
		}
	}
	for _, job := range run.Jobs {
		state := ci.JobStateOf(job)
		if job.JobID != "" && job.JobID != job.Name {
			fmt.Fprintf(w, "job %s: %s (id %s)\n", job.Name, state, job.JobID)
		} else {
			fmt.Fprintf(w, "job %s: %s\n", job.Name, state)
		}
		for i, step := range job.Steps {
			verdict := string(step.Conclusion)
			if verdict == "" {
				verdict = "not finished"
			}
			fmt.Fprintf(w, "  %d. %s: %s\n", i+1, step.Name, verdict)
		}
	}
}

// noCIRunError is the answer for a run id the sandbox gets no run for. A
// GitHub Actions run the forge and grain's record could not produce says
// why (extErr, only ever set for a run in the sandbox's scope), so the
// next failure is one somebody can act on rather than a bare not-found.
func noCIRunError(id string, extErr error) error {
	if extErr != nil {
		return fmt.Errorf("%w: no CI run %s in a repository this sandbox may read: reading it as a GitHub Actions run: %v",
			namespace.ErrNotFound, id, extErr)
	}
	return fmt.Errorf("%w: no CI run %s in a repository this sandbox may read", namespace.ErrNotFound, id)
}

// externalRunPermitted reports whether scope may read the repository a
// GitHub Actions run id names. It is asked before the run is read: the
// daemon reads it from the forge with its own credential, so an id for
// a repository outside the scope is refused without that read, not after
// it.
func externalRunPermitted(scope namespace.Scope, id string) bool {
	repo, _, ok := ci.ParseActionsRunID(id)
	return ok && scope.PermitsCI(repo)
}

func formatCIRunLine(run ci.Run) string {
	verdict := string(run.Conclusion)
	if verdict == "" {
		verdict = "running"
		if run.Actions != nil {
			verdict = ci.RunStateOf(run)
		}
	}
	started := ""
	if run.StartedAt != nil {
		started = run.StartedAt.UTC().Format(time.RFC3339)
	}
	var failing []string
	for _, job := range ci.SortJobRuns(run.Jobs) {
		if job.Conclusion.Done() && job.Conclusion != ci.ConclusionSuccess && job.Conclusion != ci.ConclusionSkipped {
			failing = append(failing, job.Name)
		}
	}
	// Which CI ran it: grain, or the provider and the workflow's file.
	provider := ci.ProviderOf(run)
	if a := run.Actions; a != nil {
		if len(failing) == 0 {
			failing = a.FailedJobs
		}
		if wf := path.Base(a.Workflow); a.Workflow != "" {
			provider += "/" + wf
		} else if a.WorkflowName != "" {
			provider += "/" + strings.ReplaceAll(a.WorkflowName, " ", "_")
		}
	}
	line := strings.TrimSpace(strings.Join([]string{run.ID, provider, run.Ref, shortSHA(run.Commit), verdict, started}, " "))
	if len(failing) > 0 {
		line += " failing: " + strings.Join(failing, ", ")
	}
	return line
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (p *GitProxy) serveTestHistory(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	q := r.URL.Query()
	test := strings.TrimSpace(q.Get("test"))
	if test == "" {
		http.Error(w, "test is required", http.StatusBadRequest)
		return
	}
	repo, store, err := p.ciReadRepo(scope, q.Get("repo"))
	if err != nil {
		cliError(w, err)
		return
	}
	f := ci.RunFilter{Repo: repo, Descending: true}
	switch f.Provider = strings.ToLower(strings.TrimSpace(q.Get("provider"))); f.Provider {
	case ci.ProviderGrain, ci.ProviderGitHubActions:
	case "":
		// A repository whose CI is GitHub Actions has its history there:
		// grain CI's runs of it are a retired mode's, however many.
		if p.Namespace.ExternalCI != nil && ci.ExternalCIFor(r.Context(), p.Namespace.ExternalCI, repo) {
			f.Provider = ci.ProviderGitHubActions
		}
	default:
		cliError(w, fmt.Errorf("%w: %q is not a CI provider: use %s or %s", namespace.ErrBadRequest, q.Get("provider"),
			ci.ProviderGrain, ci.ProviderGitHubActions))
		return
	}
	page := p.findCIRuns(r.Context(), store, f, cliLimit(q, "limit", 20, 100))
	runs, recorded, recordedNote := page.Runs, 0, ""
	if f.Provider == ci.ProviderGitHubActions {
		if failures, ok := p.Namespace.ExternalCI.(ci.ExternalTestFailures); ok {
			found, note := failures.FindExternalTestFailures(r.Context(), f, test)
			runs, recorded = ci.WithRecordedFailures(runs, found, test)
			recordedNote = note
		}
	}
	report := ci.QueryTestHistory(runs, test)
	report.Repo = repo
	if page.note != "" {
		report.Notes = append(report.Notes, "GitHub Actions: "+page.note)
	}
	if recorded > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"%d further run(s) are grain's record of failed Actions jobs naming this test, kept for 8 weeks: "+
				"failures are counted over that window, passes only over the newest runs listed", recorded))
	}
	if recordedNote != "" {
		report.Notes = append(report.Notes, "GitHub Actions record: "+recordedNote)
	}
	if f.Provider == ci.ProviderGitHubActions && q.Get("provider") == "" {
		grain := f
		grain.Provider = ""
		if n := store.Find(grain, 1).Matched; n > 0 {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"%s's CI is GitHub Actions: its %d Grain CI run(s), from before, are left out (--provider grain reads them)", repo, n))
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, report.Render())
}

// servePRStatus answers /cli/pr/status: this task's own branch, another
// task's (?task=<id>), or any branch (?ref=<branch>, in ?repo= or this
// task's repository).
func (p *GitProxy) servePRStatus(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	q := r.URL.Query()
	task, ref, repo := strings.TrimSpace(q.Get("task")), strings.TrimSpace(q.Get("ref")), strings.TrimSpace(q.Get("repo"))
	switch {
	case task != "" && ref != "":
		cliError(w, fmt.Errorf("%w: give --task or --ref, not both", namespace.ErrBadRequest))
	case task != "":
		taskRepo, branch, err := p.taskCIRef(r.Context(), scope, task)
		if err != nil {
			cliError(w, err)
			return
		}
		p.serveBranchStatus(w, scope, taskRepo, branch, "task "+strings.TrimPrefix(task, "#")+"'s branch")
	case ref != "":
		p.serveBranchStatus(w, scope, repo, ref, "branch")
	default:
		if scope.TaskRepo == "" {
			cliError(w, errNoTaskRepo)
			return
		}
		p.serveBranchStatus(w, scope, repo, scope.TaskBranch, "this task's branch")
	}
}

// serveBranchStatus reports the latest CI on branch in repo ("" is the
// task's own) and each of its jobs' verdicts: the newest grain CI run, or
// for GitHub Actions every workflow's newest run at the newest commit.
func (p *GitProxy) serveBranchStatus(w http.ResponseWriter, scope namespace.Scope, repo, branch, what string) {
	repo, store, err := p.ciReadRepo(scope, repo)
	if err != nil {
		cliError(w, err)
		return
	}
	if repo == "" {
		// --ref with no --repo on a task without one: a branch name
		// alone would match that name in every repository.
		cliError(w, errNoTaskRepo)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if branch == "" {
		fmt.Fprintf(w, "No branch is recorded for %s.\n", what)
		return
	}
	page := store.Find(ci.RunFilter{Repo: repo, Ref: branch, Descending: true}, 50)
	var runs []ci.Run
	for _, run := range page.Runs {
		// Ref matches as a substring; a status is about the branch itself.
		if strings.EqualFold(run.Ref, branch) {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 {
		fmt.Fprintf(w, "%s (%s) in %s has no CI run recorded yet.\n", branch, what, repo)
		return
	}
	latest := runs[0]
	if ci.ProviderOf(latest) != ci.ProviderGitHubActions {
		fmt.Fprintf(w, "%s (%s) in %s -- latest grain CI run:\n", branch, what, repo)
		writeBranchStatusRun(w, latest)
		return
	}
	// GitHub Actions runs one workflow run per workflow file for a push,
	// so the branch's status is every workflow's newest run at the newest
	// commit, not whichever of them reported last.
	fmt.Fprintf(w, "%s (%s) in %s -- latest GitHub Actions runs", branch, what, repo)
	if latest.Commit != "" {
		fmt.Fprintf(w, " at %s", shortSHA(latest.Commit))
	}
	fmt.Fprint(w, ":\n")
	seen := map[string]bool{}
	for _, run := range runs {
		if ci.ProviderOf(run) != ci.ProviderGitHubActions || run.Commit != latest.Commit {
			continue
		}
		workflow := ""
		if run.Actions != nil {
			workflow = run.Actions.Workflow
		}
		if seen[workflow] {
			continue
		}
		seen[workflow] = true
		writeBranchStatusRun(w, run)
	}
}

// writeBranchStatusRun is one run of a branch status: its line, then each
// job's verdict.
func writeBranchStatusRun(w http.ResponseWriter, run ci.Run) {
	fmt.Fprintf(w, "%s\n", formatCIRunLine(run))
	for _, job := range ci.SortJobRuns(run.Jobs) {
		verdict := string(job.Conclusion)
		if verdict == "" {
			verdict = "running"
		}
		fmt.Fprintf(w, "  %s: %s\n", job.Name, verdict)
	}
}
