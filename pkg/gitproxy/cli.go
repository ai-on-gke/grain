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
	"ci/test-history": "query_test_history",
	"pr/status":       "pull_request_status",
	"pr/base-status":  "base_branch_status",
	"issues":          "list_issues",
	"issue":           "read_issue",
	"db/query":        "query_grain_db",
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
	"quarantine":        "quarantine_test",
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
		p.serveCIRuns(w, q, scope)
	case route == "ci/test-history":
		p.serveTestHistory(w, q, scope)
	case route == "pr/status":
		p.serveBranchStatus(w, scope, scope.TaskBranch, "this task's branch")
	case route == "pr/base-status":
		base := scope.TaskBase
		if base == "" {
			base = "main"
		}
		p.serveBranchStatus(w, scope, base, "the base branch")
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

func (p *GitProxy) serveCIRuns(w http.ResponseWriter, q url.Values, scope namespace.Scope) {
	repo, store, err := p.ciReadRepo(scope, q.Get("repo"))
	if err != nil {
		cliError(w, err)
		return
	}
	ref := q.Get("ref")
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
	page := store.Find(f, cliLimit(q, "limit", 20, 200))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%d of %d matching run(s) in %s, newest first:\n", len(page.Runs), page.Matched, repo)
	for _, run := range page.Runs {
		fmt.Fprintln(w, formatCIRunLine(run))
	}
}

func formatCIRunLine(run ci.Run) string {
	verdict := string(run.Conclusion)
	if verdict == "" {
		verdict = "running"
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
	line := strings.TrimSpace(strings.Join([]string{run.ID, run.Ref, shortSHA(run.Commit), verdict, started}, " "))
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

// serveBranchStatus reports the latest grain CI run on branch and each of
// its jobs' verdicts.
func (p *GitProxy) serveBranchStatus(w http.ResponseWriter, scope namespace.Scope, branch, what string) {
	repo, store, err := p.ciReadRepo(scope, "")
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
