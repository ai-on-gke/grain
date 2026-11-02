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
	"pr/review-comment": "add_review_comment",
	"revert-info":       "revert_task",
	"status":            "update_status",
	"handover":          "write_handover",
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
	cw := &countingResponseWriter{ResponseWriter: w}
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
	case route == "ci/test-history":
		p.serveTestHistory(w, q, scope)
	case route == "pr/status":
		p.servePRStatus(w, r, scope)
	case route == "tasks":
		p.serveTasks(w, r, scope)
	case route == "pr/base-status":
		base := scope.TaskBase
		if base == "" {
			base = "main"
		}
		p.serveBranchStatus(w, scope, "", base, "the base branch")
	case route == "issues" || strings.HasPrefix(route, "issues/"):
		if !scope.MayReadIssues {
			cliError(w, namespace.RefuseCapability("grain issue", "read-issues"))
			return
		}
		p.serveIssues(w, r, scope, strings.TrimPrefix(strings.TrimPrefix(route, "issues"), "/"))
	case route == "db/query":
		if !scope.MayReadDB {
			cliError(w, namespace.RefuseCapability("grain db query", "db-read"))
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
		return "", nil, namespace.RefuseCapability("grain ci", "grain-ci")
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
		return "", "", namespace.RefuseCapability("grain ci", "grain-ci")
	}
	if own := scope.TaskIDStr; own != "" && strings.EqualFold(own, task) {
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
		ref = scope.TaskBranch
	}
	f := ci.RunFilter{Repo: repo, Ref: ref, Job: q.Get("job"), Descending: true}
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
	if exact != "" {
		// Ref matches as a substring, so task-19's branch would find
		// task-190's runs too: read a wider page and keep the branch's own.
		page := p.findCIRuns(r.Context(), store, f, 200)
		var runs []ci.Run
		for _, run := range page.Runs {
			if strings.EqualFold(run.Ref, exact) && len(runs) < limit {
				runs = append(runs, run)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%d run(s) of task %s on %s in %s, newest first:\n", len(runs), strings.TrimPrefix(strings.TrimSpace(q.Get("task")), "#"), exact, repo)
		for _, run := range runs {
			fmt.Fprintln(w, formatCIRunLine(run))
		}
		return
	}
	page := p.findCIRuns(r.Context(), store, f, limit)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%d of %d matching run(s) in %s, newest first:\n", len(page.Runs), page.Matched, repo)
	for _, run := range page.Runs {
		fmt.Fprintln(w, formatCIRunLine(run))
	}
	if page.note != "" {
		fmt.Fprintf(w, "(GitHub Actions: %s)\n", page.note)
	}
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
	external, note := p.Namespace.ExternalCI.FindExternalRuns(ctx, f, limit)
	if len(external) == 0 {
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
	page.Matched += len(external)
	page.Stored += len(external)
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
		cliError(w, namespace.RefuseCapability("grain ci", "grain-ci"))
		return
	}
	if p.Namespace == nil || p.Namespace.CIStore == nil {
		cliError(w, fmt.Errorf("%w: CI store is unavailable", namespace.ErrNotFound))
		return
	}
	run, ok := p.Namespace.CIStore.Get(id)
	if !ok && p.Namespace.ExternalCI != nil && externalRunPermitted(scope, id) {
		run, ok, _ = p.Namespace.ExternalCI.ExternalRun(context.Background(), id)
	}
	if !ok || run.Repo == "" || !scope.PermitsCI(run.Repo) {
		// A run outside the scope is answered as one nobody remembers.
		cliError(w, fmt.Errorf("%w: no CI run %s in a repository this sandbox may read", namespace.ErrNotFound, id))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": run.ID, "repo": run.Repo, "ref": run.Ref})
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
		cliError(w, namespace.RefuseCapability("grain ci", "grain-ci"))
		return
	}
	if p.Namespace == nil || p.Namespace.CIStore == nil {
		cliError(w, fmt.Errorf("%w: CI store is unavailable", namespace.ErrNotFound))
		return
	}
	run, ok := p.Namespace.CIStore.Get(id)
	if !ok && p.Namespace.ExternalCI != nil && externalRunPermitted(scope, id) {
		var err error
		if run, ok, err = p.Namespace.ExternalCI.ExternalRun(r.Context(), id); err != nil {
			ok = false
		}
	}
	if !ok || run.Repo == "" || !scope.PermitsCI(run.Repo) {
		cliError(w, fmt.Errorf("%w: no CI run %s in a repository this sandbox may read", namespace.ErrNotFound, id))
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

func (p *GitProxy) serveTestHistory(w http.ResponseWriter, q url.Values, scope namespace.Scope) {
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
	page := store.Find(ci.RunFilter{Repo: repo, Descending: true}, cliLimit(q, "limit", 20, 100))
	report := ci.QueryTestHistory(page.Runs, test)
	report.Repo = repo
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
		p.serveBranchStatus(w, scope, repo, scope.TaskBranch, "this task's branch")
	}
}

// serveBranchStatus reports the latest grain CI run on branch in repo
// ("" is the task's own) and each of its jobs' verdicts.
func (p *GitProxy) serveBranchStatus(w http.ResponseWriter, scope namespace.Scope, repo, branch, what string) {
	repo, store, err := p.ciReadRepo(scope, repo)
	if err != nil {
		cliError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if branch == "" {
		fmt.Fprintf(w, "No branch is recorded for %s.\n", what)
		return
	}
	page := store.Find(ci.RunFilter{Repo: repo, Ref: branch, Descending: true}, 20)
	for _, run := range page.Runs {
		// Ref matches as a substring; a status is about the branch itself.
		if !strings.EqualFold(run.Ref, branch) {
			continue
		}
		fmt.Fprintf(w, "%s (%s) in %s -- latest grain CI run:\n%s\n", branch, what, repo, formatCIRunLine(run))
		for _, job := range ci.SortJobRuns(run.Jobs) {
			verdict := string(job.Conclusion)
			if verdict == "" {
				verdict = "running"
			}
			fmt.Fprintf(w, "  %s: %s\n", job.Name, verdict)
		}
		return
	}
	fmt.Fprintf(w, "%s (%s) in %s has no grain CI run yet.\n", branch, what, repo)
}
