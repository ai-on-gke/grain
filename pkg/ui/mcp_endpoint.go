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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ControllerMCPServerName is serverInfo.name advertised by POST /api/mcp and
// the capability server name built-in sessions attach it under.
const ControllerMCPServerName = mcp.ControllerToolNamespace

// Controller MCP tool names: six fixed tools, whatever the caller's
// grants, in place of one tool per route of the agent API (a few hundred
// schemas for a caller holding many scopes; mcp_token_benchmark_test.go).
const (
	ToolGrainExploreScope  = "grain_explore_scope"
	ToolGrainInvoke        = "grain_invoke"
	ToolGrainFilingContext = "grain_filing_context"
	ToolGrainSubmitTask    = "grain_submit_task"
	ToolGrainListTasks     = "grain_list_tasks"
	ToolGrainGetTask       = "grain_get_task"
)

// ControllerMCPToolNames returns the six fixed tool names served on POST /api/mcp.
func ControllerMCPToolNames() []string {
	return []string{
		ToolGrainExploreScope,
		ToolGrainInvoke,
		ToolGrainFilingContext,
		ToolGrainSubmitTask,
		ToolGrainListTasks,
		ToolGrainGetTask,
	}
}

// controllerMCPInstructions is the workflow guide returned on MCP
// initialize so external harnesses and built-in sessions know how to discover
// and invoke controller routes without loading every route's schema into context.
const controllerMCPInstructions = `Grain Controller MCP workflow guide:
1. Everyday task operations have dedicated compact tools -- prefer these first:
   - grain_list_tasks(repo?, state?, q?, limit?, summary?) to browse tasks (compact summary rows by default).
   - grain_get_task(id, view?) to inspect one task (~1 KB "summary" by default, "conversation" to page comments, "full" for raw JSON).
   - grain_filing_context(repo?) to read target repositories, templates, boards, custom fields, and grantable capabilities before filing work.
   - grain_submit_task(...) to file a task. approve defaults to false (filed as "proposed" awaiting human approval); only set approve=true when explicitly authorized.
2. All other Grain controller operations (a few hundred routes across CI, merge queue, repos, releases, schedules, templates, settings, secrets, capabilities, deployment) use progressive discovery:
   - Call grain_explore_scope() with no arguments to list the scope groups you are authorized to use.
   - Call grain_explore_scope(scope="<group>", tool="<word>") (e.g. scope="tasks", tool="comment") to get the exact JSON input schemas of just the routes you need; tool matches words of tool names, paths and descriptions, and a word that matches nothing answers with the nearest tool names.
   - A whole group (scope alone), or every route (scope="all" or view="index"), comes back in one answer: the tool drops from full schemas to compact lines to an index of tool names as needed to fit ~10 KB, or name view="full"|"compact"|"index" yourself. Per-field aliases of api_set_repo_settings are folded out unless includeAliases=true.
   - Call grain_invoke(tool="api_...", arguments={...}, reason?="...") to execute an authorized route; a misspelled tool name is answered with the nearest real ones.
3. Safety & authorization rules:
   - Destructive (DELETE) and dangerous-scope routes require a non-empty reason explaining why the change is being made.
   - Task mutations accept arguments.expect ({state?, title_prefix?, kind?}): the call reads the task first and refuses, changing nothing, if the record disagrees.
   - Calls run under your authenticated identity (and X-Grain-Acting-Task grants when present); if a scope is refused, ask a human to grant api:<scope> rather than working around it.`

var controllerAlwaysLoadMeta = map[string]any{
	"antigravity/alwaysLoad": true,
}

type controllerMCPRequestKey struct{}

func withControllerMCPRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, controllerMCPRequestKey{}, r)
}

// controllerMCPRequestFromContext is the POST /api/mcp request a tool
// call arrived on (handleControllerMCP), or nil for a handler called
// outside the endpoint.
func controllerMCPRequestFromContext(ctx context.Context) *http.Request {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(controllerMCPRequestKey{}).(*http.Request)
	return r
}

// toolFailure is the error result every controller tool answers with:
// "<tool> failed: <why>", flagged isError so the harness shows it as one.
func toolFailure(tool string, err error) mcp.Result {
	return mcp.Result{Text: tool + " failed: " + err.Error(), IsError: true}
}

func toolFailuref(tool, format string, args ...any) mcp.Result {
	return toolFailure(tool, fmt.Errorf(format, args...))
}

var errNoOuterRequest = errors.New("missing outer HTTP request context")

// handleControllerMCP serves the single stateless HTTP MCP endpoint at POST /api/mcp.
func (s *Server) handleControllerMCP(w http.ResponseWriter, r *http.Request) {
	s.controllerMCPOnce.Do(func() {
		reg := mcp.NewRegistry()
		reg.SetServerName(ControllerMCPServerName)
		reg.SetInstructions(controllerMCPInstructions)
		for _, tool := range s.controllerMCPTools() {
			reg.Register(tool)
		}
		s.controllerMCPHandler = mcp.StatelessHTTPHandler(reg)
	})
	r = r.WithContext(withControllerMCPRequest(r.Context(), r))
	s.controllerMCPHandler.ServeHTTP(w, r)
}

func (s *Server) controllerMCPTools() []mcp.Tool {
	return []mcp.Tool{
		s.exploreScopeTool(),
		s.invokeRouteTool(),
		s.filingContextTool(),
		s.submitTaskTool(),
		s.listTasksTool(),
		s.getTaskTool(),
	}
}

type mcpCallerAccess struct {
	role          *accessRole
	actingTaskID  string
	externalToken *model.ExternalAgentToken
	fullAccess    bool
	grantSet      APIGrantSet
	heldGrants    []string
	channel       string
	repoLimits    []string
}

// resolveMCPCallerAccess is who is calling POST /api/mcp and what routes
// they reach: the acting task's grants when X-Grain-Acting-Task names
// one, an external agent token's scopes when the request carries one, a
// verified session's role when accessRoleFromContext carries a limited or
// delegated role (intersected with any token or acting task grants), and
// otherwise every non-internal route for an unlimited caller.
func (s *Server) resolveMCPCallerAccess(ctx context.Context) (mcpCallerAccess, error) {
	outerReq := controllerMCPRequestFromContext(ctx)
	if outerReq == nil {
		return mcpCallerAccess{}, errNoOuterRequest
	}
	role := accessRoleFromContext(ctx)
	// An external agent token is the credential, as it is for agentAPIGate:
	// X-Grain-Acting-Task on a token's request is ignored rather than let
	// widen the token's scopes to a task's grants.
	if tok := externalTokenFromContext(ctx); tok != nil {
		_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
		if err != nil {
			return mcpCallerAccess{role: role, externalToken: tok}, err
		}
		if role != nil && !role.Full {
			set = set.Intersect(role.set).Without(role.deny)
		}
		return mcpCallerAccess{
			role:          role,
			externalToken: tok,
			grantSet:      set,
			repoLimits:    slices.Clone(tok.Repos),
		}, nil
	}
	taskID := strings.TrimSpace(outerReq.Header.Get(ActingTaskHeader))
	channel := strings.TrimSpace(outerReq.Header.Get(ChannelHeader))
	if taskID != "" {
		if s.tasks == nil || s.tasks.Store == nil {
			return mcpCallerAccess{role: role, actingTaskID: taskID, channel: channel}, errNoAgentAPI
		}
		acting, err := s.tasks.Store.GetTask(ctx, taskID)
		if err != nil || acting == nil {
			return mcpCallerAccess{role: role, actingTaskID: taskID, channel: channel}, fmt.Errorf("acting task %s could not be read: %v", taskID, err)
		}
		held := grantNames(acting.Grants)
		var set APIGrantSet
		if channel == SandboxCLIChannel {
			set = model.SandboxAPIGrantsOf(held)
		} else {
			set = APIGrantsOf(held)
		}
		set = model.WithinDirectorFloor(*acting, set)
		if role != nil && !role.Full {
			set = set.Intersect(role.set).Without(role.deny)
		}
		return mcpCallerAccess{
			role:         role,
			actingTaskID: taskID,
			grantSet:     set,
			heldGrants:   held,
			channel:      channel,
		}, nil
	}
	if role != nil && (!role.Full || role.Delegated) {
		return mcpCallerAccess{
			role:       role,
			grantSet:   role.set,
			heldGrants: slices.Clone(role.grants),
		}, nil
	}
	return mcpCallerAccess{role: role, fullAccess: true}, nil
}

// reads reports whether the caller holds scope outright across the
// deployment: what each deployment-wide part of grain_filing_context is
// gated on, as the route serving the same data is (route_scopes.go).
func (a mcpCallerAccess) reads(scope APIScope) bool {
	if a.role != nil && !a.role.Full && !a.role.allows(scope, "") {
		return false
	}
	return a.fullAccess || a.grantSet.Has(scope)
}

func (a mcpCallerAccess) allowsRoute(rt APIRoute) bool {
	if !roleAllowsMCPRoute(a.role, rt) {
		return false
	}
	if a.fullAccess {
		return rt.Scope != "" && rt.Scope != scopeInternal
	}
	if a.role != nil && a.role.Full && a.externalToken == nil && a.actingTaskID == "" {
		return true
	}
	if a.externalToken != nil {
		if _, blocked := isNoTaskContextRoute(rt.Pattern); blocked {
			return false
		}
		return a.grantSet.Has(rt.Scope) || tasksAdminAllowsTaskPatch(a.grantSet, rt.Pattern) ||
			capabilitiesDetachAllowsRoute(a.grantSet, rt.Pattern)
	}
	if a.channel == SandboxCLIChannel {
		if IsSandboxDeniedScope(rt.Scope) || sandboxChannelGuard(rt.Pattern, nil) != nil {
			return false
		}
	}
	return a.grantSet.Has(rt.Scope) ||
		tasksAdminAllowsTaskPatch(a.grantSet, rt.Pattern) ||
		capabilitiesDetachAllowsRoute(a.grantSet, rt.Pattern) ||
		agentCapabilityAllowsRoute(a.heldGrants, a.grantSet, rt.Pattern, rt.Scope) ||
		grainCIReadsRoute(a.heldGrants, rt.Pattern)
}

func (a mcpCallerAccess) authorizedRoutes() []APIRoute {
	all := AgentAPIRoutes()
	out := make([]APIRoute, 0, len(all))
	for _, rt := range all {
		if a.allowsRoute(rt) {
			out = append(out, a.reachedAs(rt))
		}
	}
	return out
}

// reachedAs is rt labelled with the scope this caller actually reaches it
// through. A route reached only through a narrower scope's carve-out --
// capabilities:detach on the capabilities:admin routes
// (capabilitiesDetachAllowsRoute), tasks:admin's priority patch
// (tasksAdminAllowsTaskPatch) -- is listed under that scope, so
// grain_explore_scope does not tell a credential holding only the
// non-dangerous capabilities:detach that it holds the dangerous
// capabilities:admin (docs/test-runs/2026-11-07-director-agent-api.md,
// finding 3). Only the label changes: the gate still authorizes the call
// against routeScopes and the carve-out's own body rules.
func (a mcpCallerAccess) reachedAs(rt APIRoute) APIRoute {
	if a.fullAccess || a.grantSet.Has(rt.Scope) ||
		(a.role != nil && a.role.Full && a.externalToken == nil && a.actingTaskID == "") {
		return rt
	}
	switch {
	case capabilitiesDetachAllowsRoute(a.grantSet, rt.Pattern):
		rt.Scope = "capabilities:detach"
	case tasksAdminAllowsTaskPatch(a.grantSet, rt.Pattern):
		rt.Scope = "tasks:admin"
	}
	return rt
}

func scopeDomainGroup(scope APIScope) string {
	s := string(scope)
	if domain, _, ok := strings.Cut(s, ":"); ok && domain != "" {
		return domain
	}
	return s
}

var domainGuidance = map[string]string{
	"tasks":        "Task lifecycle & mutations. Prefer grain_list_tasks, grain_get_task, grain_filing_context, and grain_submit_task for common reads/filing; use grain_invoke for comments, state transitions, dependencies, approvals, and task-admin edits. Pass arguments.expect on admin mutations to avoid clobbering concurrent changes.",
	"ci":           "Grain CI workflows, runs, logs, faults, and artifacts. Filter list calls by repo/branch when possible before fetching full job logs.",
	"merge":        "Pull request creation, merge-queue retries, and force-merges. Force-merge requires merge:force and an explicit reason.",
	"repos":        "Target repository onboarding, branch configuration, CI mode, merge settings, and declarations. api_get_repo_settings / api_set_repo_settings read and write every per-repository setting together; the per-field routes (ci-mode, setup-command, ...) are aliases of them, listed only with includeAliases=true.",
	"releases":     "Release branches, release candidates, qualification plans, and promotions.",
	"schedules":    "Recurring task/suite schedules, composition previews, and immediate schedule triggers.",
	"templates":    "Reusable task templates and template composition previews.",
	"suites":       "Multi-template suites and suite runs.",
	"boards":       "Kanban/triage boards and filter views.",
	"settings":     "Deployment-wide configuration, model rotation, CLI-run settings, and state-repo sync. Preview changes with api_plan_settings when unsure.",
	"deployment":   "Host diagnostics, logs, active sandboxes, metrics, upgrades, and daemon restart/reboot controls.",
	"capabilities": "Capability server registration, health tests, API scope sets, capability policies, and per-task capability grants.",
	"secrets":      "Encrypted secrets, model keys, GCP keys, and GitHub tokens. Secret values are write-only; destructive/dangerous calls require a reason.",
	"connections":  "Git connections and model backend definitions.",
	"sessions":     "Interactive console sessions and standing Director/chat sessions.",
	"caches":       "CI, package, and GitHub Actions cache inspection and flushing.",
	"dispatch":     "Queue/pipeline pause controls and immediate task dispatch (run-now).",
	"memory":       "Deployment and per-repository standing agent memory documents.",
}

// dispatchSubrequest executes an inner REST request through s.mux.ServeHTTP,
// preserving the outer request's RemoteAddr, Host, TLS, Context() (principal,
// auth method, external token), auth headers, and X-Grain-Acting-Task so
// empty-RemoteAddr owner trust is never synthesized for a remote caller.
// It returns the status and body of a 2xx answer, and an error naming
// the status and the route's own message for anything else.
func (s *Server) dispatchSubrequest(ctx context.Context, method, pathWithQuery string, bodyBytes []byte, reason string) (int, []byte, error) {
	outerReq := controllerMCPRequestFromContext(ctx)
	if outerReq == nil {
		return 0, nil, errNoOuterRequest
	}
	rehearsal := &configWriteRehearsal{}
	subCtx := model.WithWriteRehearsal(ctx, rehearsal)

	var bodyReader io.Reader = http.NoBody
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	innerReq, err := http.NewRequestWithContext(subCtx, method, "http://localhost"+pathWithQuery, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	innerReq.RemoteAddr = outerReq.RemoteAddr
	if outerReq.Host != "" {
		innerReq.Host = outerReq.Host
	}
	innerReq.TLS = outerReq.TLS
	innerReq.RequestURI = pathWithQuery

	// Preserve auth, identity-proxy, and acting-task headers from outerReq while
	// stripping outer MCP framing and conditional-cache headers.
	innerReq.Header = make(http.Header, len(outerReq.Header)+2)
	for k, vals := range outerReq.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Content-Type", "Accept",
			"If-None-Match", "If-Modified-Since",
			"Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id":
			continue
		default:
			innerReq.Header[k] = slices.Clone(vals)
		}
	}
	innerReq.Header.Set("Accept", "application/json")
	if bodyBytes != nil {
		innerReq.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(reason) != "" {
		innerReq.Header.Set(ActingReasonHeader, strings.TrimSpace(reason))
	} else {
		innerReq.Header.Del(ActingReasonHeader)
	}

	rec := httptest.NewRecorder()
	rw := &configRefusalWriter{
		ResponseWriter: rec,
		rehearsal:      rehearsal,
		// As serveHTTP's onSuccess: a configuration write is recorded
		// and exported as made by whoever the handler guarded it as --
		// the acting task's agent principal, which is on the handler's
		// context alone -- not as the person whose request carried it.
		onSuccess: func() {
			if s.tasks != nil {
				by := rehearsal.writer(subCtx)
				s.tasks.recordConfigWrite(by, innerReq)
				s.tasks.syncStateRepoAfterWrite(by)
			}
		},
	}
	s.mux.ServeHTTP(rw, innerReq)
	rw.finish()
	if rec.Code < 200 || rec.Code > 299 {
		return rec.Code, nil, routeError(rec.Code, rec.Body.Bytes())
	}
	return rec.Code, rec.Body.Bytes(), nil
}

// Explore views: what grain_explore_scope answers a route query with,
// from most to least detail. The caller may name one; left to itself
// the tool picks the most detailed view that fits maxExploreResultBytes,
// so a whole group or the whole API comes back readable in one call
// rather than elided in the middle.
const (
	exploreViewAuto    = ""
	exploreViewFull    = "full"    // every route with its JSON input schema
	exploreViewCompact = "compact" // tool, method, path, scope and a one-line summary per route
	exploreViewIndex   = "index"   // tool names only, grouped by scope
)

// maxExploreResultBytes is how large an explore answer may grow before
// the tool drops to a smaller view of the same routes: the ~10 KB a
// harness shows a model whole, under pkg/mcp's own 16 KB cap. The index
// of every route in the API fits under it; a group's compact listing
// fits under it; a group's full schemas mostly do not.
const maxExploreResultBytes = 10 << 10

// maxExploreSummaryChars bounds the one-line summary the compact view
// shows per route: the first sentence of the route's doc, cut at a word.
const maxExploreSummaryChars = 120

func (s *Server) exploreScopeTool() mcp.Tool {
	return mcp.Tool{
		Name: ToolGrainExploreScope,
		Description: "List authorized Grain controller API scope groups, or the routes of one scope group or scope (or of every group, with scope='all'), " +
			"narrowed to the routes matching tool when given. The view shrinks to fit one answer: full JSON input schemas when they fit (~10 KB), " +
			"else compact lines (tool, method, path, one-line summary), else an index of tool names by scope; name view to choose. " +
			"Per-field aliases of api_set_repo_settings are folded out unless includeAliases is true.",
		Meta: controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{
					"type":        "string",
					"description": "Optional scope group (e.g. 'tasks', 'ci', 'merge', 'repos', 'settings'), exact scope (e.g. 'ci:read'), or 'all' for every authorized route. Omit to list the authorized scope groups.",
				},
				"tool": map[string]any{
					"type":        "string",
					"description": "Optional route tool name (e.g. 'api_add_comment') or one or more words to search for in tool names, paths, scopes and descriptions (e.g. 'comment', 'git connection'): only matching routes are returned, which is far cheaper than a whole group. With scope, searches that group; alone, every authorized route. A word that matches nothing answers with the nearest tool names.",
				},
				"view": map[string]any{
					"type":        "string",
					"enum":        []string{exploreViewFull, exploreViewCompact, exploreViewIndex},
					"description": "How much of each route to return: 'full' (its JSON input schema), 'compact' (tool, method, path, scope, one-line summary; no schema) or 'index' (tool names grouped by scope). Omit to get the most detailed view that fits in one answer; then name one route in tool for its full schema.",
				},
				"includeAliases": map[string]any{
					"type":        "boolean",
					"description": "List the per-field repository configuration routes (GET/PUT /api/repos/{owner}/{name}/ci-mode, .../setup-command, ...) too. They are folded out by default: api_get_repo_settings and api_set_repo_settings read and write the same fields together. A tool query that matches one shows it regardless.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			access, err := s.resolveMCPCallerAccess(ctx)
			if err != nil {
				return toolFailure(ToolGrainExploreScope, err)
			}
			authorized := access.authorizedRoutes()
			rawScope, _ := args["scope"].(string)
			query := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rawScope)), APIGrantPrefix)
			rawTool, _ := args["tool"].(string)
			toolQuery := strings.ToLower(strings.TrimSpace(rawTool))
			rawView, _ := args["view"].(string)
			view := strings.ToLower(strings.TrimSpace(rawView))
			switch view {
			case exploreViewAuto, exploreViewFull, exploreViewCompact, exploreViewIndex:
			default:
				return toolFailuref(ToolGrainExploreScope, "unknown view %q: use %s, %s or %s, or omit it", rawView, exploreViewFull, exploreViewCompact, exploreViewIndex)
			}
			includeAliases, _ := args["includeAliases"].(bool)

			if query == "" && toolQuery == "" && view == exploreViewAuto {
				return renderScopeOverview(access, authorized)
			}
			if query == "all" || query == "*" {
				query = ""
			}
			return renderScopeDetail(access, authorized, exploreQuery{
				scope: query, tool: toolQuery, view: view, includeAliases: includeAliases,
			})
		},
	}
}

type scopeSummaryEntry struct {
	Scope       string `json:"scope"`
	Description string `json:"description,omitempty"`
	Dangerous   bool   `json:"dangerous,omitempty"`
	RouteCount  int    `json:"routeCount"`
}

type scopeGroupOverview struct {
	Group       string              `json:"group"`
	Guidance    string              `json:"guidance,omitempty"`
	TotalRoutes int                 `json:"totalRoutes"`
	Scopes      []scopeSummaryEntry `json:"scopes"`
}

func renderScopeOverview(access mcpCallerAccess, authorized []APIRoute) mcp.Result {
	if len(authorized) == 0 {
		msg := "No Grain controller API scopes are currently authorized for this caller."
		if access.actingTaskID != "" {
			msg += fmt.Sprintf(" Ask a human to grant task %s an api:<scope> or api:<preset> capability (e.g. api:tasks-read, api:task-admin, api:ci-read, api:deployment:read).", access.actingTaskID)
		} else if access.role != nil && !access.role.Full {
			msg += fmt.Sprintf(" %s holds no controller API scopes; ask an access-code admin to assign a role that includes them (docs/access-code.md).", access.role.label())
		}
		return mcp.Result{Text: msg}
	}

	byGroup := map[string]map[APIScope]int{}
	var groups []string
	for _, rt := range authorized {
		g := scopeDomainGroup(rt.Scope)
		if byGroup[g] == nil {
			byGroup[g] = map[APIScope]int{}
			groups = append(groups, g)
		}
		byGroup[g][rt.Scope]++
	}
	sort.Strings(groups)

	out := make([]scopeGroupOverview, 0, len(groups))
	for _, g := range groups {
		counts := byGroup[g]
		scopeNames := make([]string, 0, len(counts))
		for sc := range counts {
			scopeNames = append(scopeNames, string(sc))
		}
		sort.Strings(scopeNames)
		entries := make([]scopeSummaryEntry, 0, len(scopeNames))
		total := 0
		for _, scName := range scopeNames {
			sc := APIScope(scName)
			info, _ := apiScopeInfo(sc)
			n := counts[sc]
			total += n
			entries = append(entries, scopeSummaryEntry{
				Scope:       scName,
				Description: info.Description,
				Dangerous:   IsDangerousAPIScope(sc),
				RouteCount:  n,
			})
		}
		out = append(out, scopeGroupOverview{
			Group:       g,
			Guidance:    domainGuidance[g],
			TotalRoutes: total,
			Scopes:      entries,
		})
	}

	payload := map[string]any{
		"totalAuthorizedRoutes": len(authorized),
		"hint":                  "Call grain_explore_scope(scope='<group>', tool='<word>') to view matching route tool names and JSON input schemas; scope alone lists a whole group and scope='all' (or view='index') every route, each shrunk to fit one answer. Then grain_invoke(tool, arguments, reason?) to execute.",
		"groups":                out,
	}
	encoded, _ := json.Marshal(payload)
	return mcp.Result{Text: string(encoded)}
}

type exploredRouteSchema struct {
	Tool        string         `json:"tool"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Scope       APIScope       `json:"scope"`
	NeedsReason bool           `json:"needsReason,omitempty"`
	AliasOf     string         `json:"aliasOf,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// exploredRouteLine is one route of the compact view: what the full
// view says less the schema and the boilerplate, in ~150 bytes.
type exploredRouteLine struct {
	Tool        string   `json:"tool"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Scope       APIScope `json:"scope"`
	NeedsReason bool     `json:"needsReason,omitempty"`
	AliasOf     string   `json:"aliasOf,omitempty"`
	Summary     string   `json:"summary"`
}

// exploreQuery is one grain_explore_scope route query as its handler
// read it: scope empty for every authorized route.
type exploreQuery struct {
	scope          string
	tool           string
	view           string
	includeAliases bool
}

func routeMatchesScopeQuery(rt APIRoute, query string, presetScopes []APIScope) bool {
	sc := strings.ToLower(string(rt.Scope))
	if sc == query || scopeDomainGroup(rt.Scope) == query {
		return true
	}
	for _, ps := range presetScopes {
		if strings.EqualFold(string(ps), sc) {
			return true
		}
	}
	return false
}

// routeOneLiner is a route's summary cut to one line: its first
// sentence, and at most maxExploreSummaryChars of that, cut at a word.
func routeOneLiner(rt APIRoute) string {
	s := strings.TrimSpace(rt.Summary)
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len(s) <= maxExploreSummaryChars {
		return s
	}
	cut := strings.LastIndex(s[:maxExploreSummaryChars-3], " ")
	if cut < maxExploreSummaryChars/2 {
		cut = maxExploreSummaryChars - 3
	}
	return strings.TrimRight(s[:cut], " ,;:") + "..."
}

// aliasFieldOf is the field name an alias route's path ends in, for the
// note that lists what was folded out.
func aliasFieldOf(rt APIRoute) string {
	return strings.TrimPrefix(rt.Path, repoSettingsPathPrefix)
}

// aliasNote says what a listing left out and how to see it.
func aliasNote(hidden []APIRoute) string {
	canonical := map[string]bool{}
	fields := []string{}
	for _, rt := range hidden {
		canonical[rt.AliasOf] = true
		if f := aliasFieldOf(rt); !slices.Contains(fields, f) {
			fields = append(fields, f)
		}
	}
	names := make([]string, 0, len(canonical))
	for c := range canonical {
		names = append(names, c)
	}
	sort.Strings(names)
	sort.Strings(fields)
	return fmt.Sprintf("%d per-field aliases of %s folded out (one GET/PUT pair each for %s): "+
		"the whole-settings routes read and write the same fields together; pass includeAliases=true to list them.",
		len(hidden), strings.Join(names, " and "), strings.Join(fields, ", "))
}

func renderScopeDetail(access mcpCallerAccess, authorized []APIRoute, q exploreQuery) mcp.Result {
	query, toolQuery := q.scope, q.tool
	presetScopes := ExpandAPIPreset(query)
	inScope := func(rt APIRoute) bool {
		return query == "" || routeMatchesScopeQuery(rt, query, presetScopes)
	}
	// An alias is folded out of a listing, never out of a search: a
	// word that names one (ci_mode, say) is asking for it.
	foldAliases := !q.includeAliases && toolQuery == ""
	// A query that is exactly a tool's name finds that tool alone: the
	// words of api_set_repo_settings are in a dozen other routes.
	matches := routeMatchesToolQuery
	if toolQuery != "" && slices.ContainsFunc(authorized, func(rt APIRoute) bool {
		return inScope(rt) && routeNamedByToolQuery(rt, toolQuery)
	}) {
		matches = routeNamedByToolQuery
	}
	var matched, hidden []APIRoute
	for _, rt := range authorized {
		if !inScope(rt) || !matches(rt, toolQuery) {
			continue
		}
		if foldAliases && rt.AliasOf != "" {
			hidden = append(hidden, rt)
			continue
		}
		matched = append(matched, rt)
	}

	if len(matched) == 0 && len(hidden) == 0 {
		return renderNoRouteMatched(access, authorized, query, toolQuery, inScope)
	}

	guidance := domainGuidance[scopeDomainGroup(APIScope(query))]
	if guidance == "" && query != "" && len(matched) > 0 {
		guidance = domainGuidance[scopeDomainGroup(matched[0].Scope)]
	}
	payload := map[string]any{
		"scope":      query,
		"routeCount": len(matched),
	}
	if query == "" {
		payload["scope"] = "all"
	}
	if guidance != "" {
		payload["guidance"] = guidance
	}
	if toolQuery != "" {
		payload["tool"] = toolQuery
	}
	if len(hidden) > 0 {
		payload["aliases"] = aliasNote(hidden)
	}

	// Compact rather than indented: the schemas are read by a model, which
	// pays for every byte of indentation, and the indented form of a
	// scope's nested schemas was larger than the tools/list of the same
	// routes it replaced (mcp_token_benchmark_test.go).
	render := func(view string) []byte {
		p := maps.Clone(payload)
		p["view"] = view
		switch view {
		case exploreViewFull:
			routes := make([]exploredRouteSchema, 0, len(matched))
			for _, rt := range matched {
				routes = append(routes, exploredRouteSchema{
					Tool: rt.Tool, Method: rt.Method, Path: rt.Path, Scope: rt.Scope,
					NeedsReason: rt.NeedsReason, AliasOf: rt.AliasOf,
					Description: rt.Description, InputSchema: rt.inputSchema(),
				})
			}
			p["routes"] = routes
		case exploreViewCompact:
			routes := make([]exploredRouteLine, 0, len(matched))
			for _, rt := range matched {
				routes = append(routes, exploredRouteLine{
					Tool: rt.Tool, Method: rt.Method, Path: rt.Path, Scope: rt.Scope,
					NeedsReason: rt.NeedsReason, AliasOf: rt.AliasOf, Summary: routeOneLiner(rt),
				})
			}
			p["routes"] = routes
			p["hint"] = fmt.Sprintf("Schemas omitted: call %s(tool='<tool>') for one route's inputSchema, or view='full' for all of these.", ToolGrainExploreScope)
		case exploreViewIndex:
			// Scope -> tool names, as a map rather than a list of
			// objects: every byte per route counts when all of them are
			// listed, and json.Marshal writes a map's keys sorted.
			index := map[string][]string{}
			for _, rt := range matched {
				index[string(rt.Scope)] = append(index[string(rt.Scope)], rt.Tool)
			}
			p["index"] = index
			p["hint"] = fmt.Sprintf("Tool names by scope: %s(tool='<name>') gives one route's path and inputSchema.", ToolGrainExploreScope)
			if len(hidden) > 0 {
				p["aliases"] = fmt.Sprintf("%d per-field aliases of api_get/set_repo_settings folded out; includeAliases=true lists them.", len(hidden))
			}
		}
		encoded, _ := json.Marshal(p)
		return encoded
	}

	if q.view != exploreViewAuto {
		return mcp.Result{Text: string(render(q.view))}
	}
	// Left to choose, the most detailed view that fits one answer; the
	// index is the floor, whatever its size.
	for _, view := range []string{exploreViewFull, exploreViewCompact} {
		if encoded := render(view); len(encoded) <= maxExploreResultBytes {
			return mcp.Result{Text: string(encoded)}
		}
	}
	return mcp.Result{Text: string(render(exploreViewIndex))}
}

// renderNoRouteMatched is the answer to a route query nothing the caller
// may call matches: that the routes exist but are not theirs, or what
// comes nearest to the word they used.
func renderNoRouteMatched(access mcpCallerAccess, authorized []APIRoute, query, toolQuery string, inScope func(APIRoute) bool) mcp.Result {
	// Distinguish "exists in Grain's API but caller lacks grant" from "unknown scope".
	var missingScopes []string
	for _, rt := range AgentAPIRoutes() {
		if inScope(rt) && routeMatchesToolQuery(rt, toolQuery) {
			s := string(rt.Scope)
			if !slices.Contains(missingScopes, s) {
				missingScopes = append(missingScopes, s)
			}
		}
	}
	sort.Strings(missingScopes)
	what := fmt.Sprintf("Scope %q", query)
	if toolQuery != "" {
		what = fmt.Sprintf("Tool %q", toolQuery)
		if query != "" {
			what += fmt.Sprintf(" in scope %q", query)
		}
	}
	if len(missingScopes) > 0 {
		return mcp.Result{
			Text:    fmt.Sprintf("%s is not authorized for this caller (requires grant for %s).", what, strings.Join(missingScopes, ", ")),
			IsError: true,
		}
	}
	groups := authorizedGroups(authorized)
	if toolQuery != "" {
		var b strings.Builder
		fmt.Fprintf(&b, "No authorized route matches tool %q.", toolQuery)
		// Searched within a group, the nearest routes anywhere the
		// caller may look are still worth naming: the word may be right
		// and the group wrong.
		if near := nearestTools(toolQuery, authorized, 5); len(near) > 0 {
			fmt.Fprintf(&b, " Did you mean %s?", strings.Join(near, ", "))
		}
		if nearGroups := nearestGroups(toolQuery, groups); len(nearGroups) > 0 {
			fmt.Fprintf(&b, " Scope groups near that word: %s.", strings.Join(nearGroups, ", "))
		}
		fmt.Fprintf(&b, " Call %s(scope='<group>') to list a group's routes, or (view='index') for every tool name.", ToolGrainExploreScope)
		return mcp.Result{Text: b.String(), IsError: true}
	}
	msg := fmt.Sprintf("Unknown scope %q.", query)
	if nearGroups := nearestGroups(query, groups); len(nearGroups) > 0 {
		msg += fmt.Sprintf(" Did you mean %s?", strings.Join(nearGroups, ", "))
	}
	msg += fmt.Sprintf(" Call %s with no arguments to list available scope groups.", ToolGrainExploreScope)
	return mcp.Result{Text: msg, IsError: true}
}

// authorizedGroups is the scope groups of routes, each once, sorted.
func authorizedGroups(routes []APIRoute) []string {
	var groups []string
	for _, rt := range routes {
		if g := scopeDomainGroup(rt.Scope); !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	sort.Strings(groups)
	return groups
}

func (s *Server) invokeRouteTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainInvoke,
		Description: "Execute an authorized Grain controller API route discovered via grain_explore_scope.",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool": map[string]any{
					"type":        "string",
					"description": "Route tool name from grain_explore_scope (e.g. 'api_list_ci_runs').",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Route arguments: path parameters by name, optional 'query' object, optional 'body' object (or flat body fields), and optional 'expect' task-state guard.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Audit reason required for destructive (DELETE) and dangerous-scope routes.",
				},
			},
			"required": []string{"tool"},
		},
		ElisionAdvice: "narrow the route call with query parameters, or read one item by its id",
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			rawTool, _ := args["tool"].(string)
			toolName := strings.TrimSpace(rawTool)
			if toolName == "" {
				return toolFailuref(ToolGrainInvoke, "tool is required")
			}
			if !strings.HasPrefix(toolName, APIToolPrefix) {
				toolName = APIToolPrefix + toolName
			}

			// Resolved before the route is looked up, so an unknown name
			// can be answered with the nearest of the tools this caller
			// can see; its error is reported as the route's, as before.
			access, accessErr := s.resolveMCPCallerAccess(ctx)

			var route *APIRoute
			for _, candidate := range AgentAPIRoutes() {
				if candidate.Tool == toolName {
					c := candidate
					route = &c
					break
				}
			}
			if route == nil {
				// The nearest of the tools this caller can see: a guess at
				// a name (api_get_logs) is usually a word or two off the
				// real one (api_get_log_lines).
				msg := fmt.Sprintf("unknown route tool %q", rawTool)
				if accessErr == nil {
					if near := nearestTools(toolName, access.authorizedRoutes(), 3); len(near) > 0 {
						msg += fmt.Sprintf(": did you mean %s?", strings.Join(near, ", "))
					}
				}
				return toolFailuref(ToolGrainInvoke, "%s (use %s(tool='<word>') to find a route, or (view='index') to list every tool name)", msg, ToolGrainExploreScope)
			}
			if accessErr != nil {
				return toolFailure(route.Tool, accessErr)
			}
			if err := s.authorizeMCPRoleScope(ctx, route.Pattern); err != nil {
				return toolFailure(route.Tool, err)
			}
			if !access.allowsRoute(*route) {
				return toolFailuref(route.Tool, "HTTP 403: caller holds no grant for scope %s (%s)", route.Scope, route.Pattern)
			}

			routeArgs, _ := args["arguments"].(map[string]any)
			if routeArgs == nil {
				routeArgs = map[string]any{}
			}
			reason, _ := args["reason"].(string)
			reason = strings.TrimSpace(reason)
			if reason == "" {
				if rArg, ok := routeArgs["reason"].(string); ok {
					reason = strings.TrimSpace(rArg)
				}
			}
			if reason == "" {
				if bodyMap, ok := routeArgs["body"].(map[string]any); ok {
					if rBody, ok := bodyMap["reason"].(string); ok {
						reason = strings.TrimSpace(rBody)
					}
				}
			}
			if route.NeedsReason && reason == "" {
				return toolFailuref(route.Tool, "reason is required: %s is destructive (scope %s)", route.Pattern, route.Scope)
			}

			rc, err := route.buildCall(routeArgs)
			if err != nil {
				return toolFailure(route.Tool, err)
			}
			if err := checkExpectFields(routeArgs); err != nil {
				return toolFailure(route.Tool, err)
			}
			if expect, ok := expectation(routeArgs); ok && route.takesExpect() {
				if err := s.authorizeMCPRoleCall(ctx, route.Pattern, rc); err != nil {
					return toolFailure(route.Tool, err)
				}
				id := rc.params["id"]
				_, data, err := s.dispatchSubrequest(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, "")
				if err != nil {
					return toolFailuref(route.Tool, "reading task %s to check expect: %w", id, err)
				}
				var detail TaskDetail
				if err := json.Unmarshal(data, &detail); err != nil {
					return toolFailuref(route.Tool, "reading task %s: %w", id, err)
				}
				if err := expect.Check(detail.Task); err != nil {
					return toolFailure(route.Tool, err)
				}
			}

			code, data, err := s.dispatchSubrequest(ctx, route.Method, rc.path, rc.body, reason)
			if err != nil {
				return toolFailure(route.Tool, err)
			}
			if len(bytes.TrimSpace(data)) == 0 {
				return mcp.Result{Text: emptyRouteAnswer(route.Method, rc.path, code)}
			}
			return mcp.Result{Text: string(data)}
		},
	}
}

// filingContextScopes are the scopes any one of which lets a caller read
// grain_filing_context: those that file or read tasks, and those that
// read the repositories, templates, boards and settings the context is
// assembled from. The context names things (repositories, capability
// ids, model ids) and holds no secret, so one such scope is enough.
var filingContextScopes = []APIScope{
	"tasks:write", "tasks:from-template", "tasks:read", "tasks:admin",
	"repos:read", "templates:read", "boards:read", "settings:read", "deployment:read",
}

func (a mcpCallerAccess) allowsFilingContext() bool {
	if a.fullAccess {
		return true
	}
	for _, sc := range filingContextScopes {
		if a.grantSet.Has(sc) {
			return true
		}
	}
	return false
}

func (a mcpCallerAccess) repoAllowedForContext(repo string) bool {
	if repo == "" || a.fullAccess {
		return true
	}
	if len(a.repoLimits) > 0 {
		allowed := false
		for _, r := range a.repoLimits {
			if strings.EqualFold(r, repo) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if a.role != nil && !a.role.Full {
		for _, sc := range filingContextScopes {
			if a.grantSet.Allows(sc, repo) {
				return true
			}
		}
		return false
	}
	return true
}

func (a mcpCallerAccess) readsTemplates() bool {
	return a.fullAccess || a.grantSet.Has("templates:read") || a.grantSet.Has("tasks:from-template")
}

func (a mcpCallerAccess) templateAllowedForContext(repo string) bool {
	if !a.repoAllowedForContext(repo) {
		return false
	}
	if a.role != nil && !a.role.Full {
		return a.role.allows("templates:read", repo) || a.role.allows("tasks:from-template", repo)
	}
	return true
}

func (s *Server) filingContextTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainFilingContext,
		Description: "Return what filing a task needs before calling grain_submit_task: targetRepos (always a list -- the repositories tasks may be filed into, or, when anyRepoAllowed, the ones the deployment already works in), defaultRepo (where a task filed with no repo goes, or \"\"), templates, boards, custom fields, and grantable capabilities. A key is absent only when the caller lacks the scope that reads it; a list it may read is [] when empty, never null.",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional owner/name repository filter for templates and repo details.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			access, err := s.resolveMCPCallerAccess(ctx)
			if err != nil {
				return toolFailure(ToolGrainFilingContext, err)
			}
			if !access.allowsFilingContext() {
				if role := accessRoleFromContext(ctx); role != nil && !role.Full {
					if err := s.authorizeMCPRoleScope(ctx, "GET /api/tasks"); err != nil {
						return toolFailure(ToolGrainFilingContext, err)
					}
				}
				return toolFailuref(ToolGrainFilingContext, "HTTP 403: caller holds no task, template, repo, or deployment read/write scope")
			}
			if s.tasks == nil || s.tasks.Store == nil {
				return toolFailuref(ToolGrainFilingContext, "task store is not configured")
			}

			repoFilter, _ := args["repo"].(string)
			repoFilter = strings.TrimSpace(repoFilter)

			cfg, err := s.tasks.Store.GetConfig(ctx)
			if err != nil {
				return toolFailure(ToolGrainFilingContext, err)
			}
			team, err := s.tasks.GetTeam(ctx, model.DefaultTeamID)
			if err != nil {
				return toolFailure(ToolGrainFilingContext, err)
			}

			// The repositories a task may be filed into. Config.TargetRepos
			// bounds them when set; empty, the deployment accepts any
			// owner/name, and what is listed is where its work already is
			// -- the default target a task filed with no repo goes to, and
			// the repositories it has tasks, templates or schedules in --
			// so a caller with one obvious repository need not go through
			// api_config to learn its name. Always a list, never null: an
			// empty one means "none known", and anyRepoAllowed says whether
			// it is also a bound.
			anyRepoAllowed := len(team.TargetRepos) == 0
			defaultRepo := ""
			if s.tasks.Config.DefaultTarget != nil {
				defaultRepo = s.tasks.Config.DefaultTarget.String()
			}
			var candidates []string
			if !anyRepoAllowed {
				candidates = team.TargetRepos
			} else {
				if defaultRepo != "" {
					candidates = append(candidates, defaultRepo)
				}
				for _, assoc := range team.Repos {
					candidates = append(candidates, assoc.Repo)
				}
				candidates = append(candidates, access.repoLimits...)
				candidates = append(candidates, access.role.knownRepos()...)
			}
			targetRepos := []string{}
			for _, r := range candidates {
				if !access.repoAllowedForContext(r) {
					continue
				}
				if repoFilter != "" && !strings.EqualFold(r, repoFilter) {
					continue
				}
				if !slices.Contains(targetRepos, r) {
					targetRepos = append(targetRepos, r)
				}
			}
			if defaultRepo != "" && !access.repoAllowedForContext(defaultRepo) {
				defaultRepo = ""
			}

			customFields, err := s.tasks.Store.ListCustomFields(ctx)
			if err != nil {
				return toolFailure(ToolGrainFilingContext, err)
			}

			type compactTemplateField struct {
				Name     string   `json:"name"`
				Label    string   `json:"label,omitempty"`
				Required bool     `json:"required,omitempty"`
				Default  string   `json:"default,omitempty"`
				Options  []string `json:"options,omitempty"`
			}
			type compactTemplate struct {
				ID          string                 `json:"id"`
				Name        string                 `json:"name"`
				Description string                 `json:"description,omitempty"`
				Repo        string                 `json:"repo,omitempty"`
				Interactive bool                   `json:"interactive,omitempty"`
				Fields      []compactTemplateField `json:"fields,omitempty"`
			}
			templates := []compactTemplate{}
			readsTemplates := access.readsTemplates()
			if listedTemplates, err := s.tasks.ListTemplates(ctx); err == nil && readsTemplates {
				for _, tmpl := range listedTemplates {
					if !access.templateAllowedForContext(tmpl.Repo) {
						continue
					}
					if repoFilter != "" && tmpl.Repo != "" && !strings.EqualFold(tmpl.Repo, repoFilter) {
						continue
					}
					var fields []compactTemplateField
					for _, f := range tmpl.Fields {
						fields = append(fields, compactTemplateField{
							Name:     f.Name,
							Label:    f.Label,
							Required: f.Required,
							Default:  f.Default,
							Options:  f.Options,
						})
					}
					templates = append(templates, compactTemplate{
						ID:          tmpl.ID,
						Name:        tmpl.Name,
						Description: tmpl.Description,
						Repo:        tmpl.Repo,
						Interactive: tmpl.Interactive,
						Fields:      fields,
					})
				}
			}

			type compactBoard struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			boards := []compactBoard{}
			readsBoards := access.reads("boards:read")
			if listedBoards, err := s.tasks.ListBoards(ctx); err == nil && readsBoards {
				for _, b := range listedBoards {
					boards = append(boards, compactBoard{ID: b.ID, Name: b.Name})
				}
			}

			// Each part is gated as the route serving the same data is
			// (route_scopes.go); a caller without the scope gets the key
			// absent rather than empty, so it can tell "none" from "not
			// yours to read". What is present is never null: a list the
			// caller may read and that has nothing in it is [], so the
			// two readings cannot be confused.
			out := map[string]any{
				"targetRepos":    targetRepos,
				"anyRepoAllowed": anyRepoAllowed,
				"defaultRepo":    defaultRepo,
				"customFields":   nonNilList(customFieldDeclarations(customFields)),
				"submitDefaults": map[string]any{
					"approve": false,
					"note":    "grain_submit_task defaults approve=false so tasks land in 'proposed' state awaiting human approval unless approve=true is explicitly passed.",
				},
			}
			if readsTemplates {
				out["templates"] = templates
			}
			if readsBoards {
				out["boards"] = boards
			}
			if access.reads("capabilities:read") {
				repoConfigs, _ := s.tasks.Store.ListRepoConfigs(ctx)
				capabilityIDs := []string{}
				apiPresets := []string{}
				for _, capRow := range s.tasks.capabilitiesWithReadiness(cfg, repoConfigs) {
					if strings.HasPrefix(capRow.ID, APIGrantPrefix) {
						if capRow.Preset {
							apiPresets = append(apiPresets, capRow.ID)
						}
						continue
					}
					capabilityIDs = append(capabilityIDs, capRow.ID)
				}
				out["capabilities"] = capabilityIDs
				out["apiPresets"] = apiPresets
			}
			if access.reads("settings:read") && cfg != nil {
				defaultCaps := []string{}
				for _, id := range cfg.DefaultCapabilities {
					if _, ok := s.tasks.capabilityByID(id); ok {
						defaultCaps = append(defaultCaps, id)
					}
				}
				out["defaultCapabilities"] = defaultCaps
				out["defaultModel"] = cfg.EffectiveDefaultModel()
				out["models"] = nonNilList(allDistinctModels(*cfg))
				out["defaultNetwork"] = string(model.NormalizeNetworkAccess(cfg.NetworkAccess))
			}
			encoded, _ := json.Marshal(out)
			return mcp.Result{Text: string(encoded)}
		},
	}
}

// nonNilList is list, or an empty one for nil: what a JSON reader sees
// as [] rather than null.
func nonNilList[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func (s *Server) submitTaskTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainSubmitTask,
		Description: "File a new Grain task through POST /api/tasks. approve defaults to false (files as 'proposed' awaiting human approval).",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Short imperative task title (required unless template_id supplies one).",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Detailed task specification with enough context for an unattended sandbox run.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Target repository in owner/name form.",
				},
				"no_repo": map[string]any{
					"type":        "boolean",
					"description": "Set true to explicitly file a standalone task with no repository.",
				},
				"base": map[string]any{
					"type":        "string",
					"description": "Base branch to work from (defaults to the repository's default branch).",
				},
				"approve": map[string]any{
					"type":        "boolean",
					"description": "When true, queue the task immediately for dispatch (requires human caller or tasks:admin). Defaults to false ('proposed').",
				},
				"template_id": map[string]any{
					"type":        "string",
					"description": "Optional template ID from grain_filing_context.",
				},
				"fields": map[string]any{
					"type":        "object",
					"description": "Template input field values keyed by field name.",
				},
				"board_id": map[string]any{
					"type":        "string",
					"description": "Optional board ID context.",
				},
				"priority": map[string]any{
					"type":        "integer",
					"description": "Positive dispatch priority (default 5; higher numbers dispatch first).",
				},
				"interactive": map[string]any{
					"type":        "boolean",
					"description": "File as a live interactive chat task.",
				},
				"auto_merge": map[string]any{
					"type":        "boolean",
					"description": "Enable auto-merge on green CI (person callers only; agents cannot enable auto-merge).",
				},
				"capabilities": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Explicit capability IDs for the task (omit to use deployment defaults).",
				},
				"grants": map[string]any{
					"type":        "array",
					"description": "Unified grants, each a grant string or a {capability, with} object, as POST /api/tasks takes them (repo#target, repo#read and repo#write included). A request that also names the same kind the old way -- capabilities, reads, writes, or repo/base/no_repo beside repo#target -- is refused.",
				},
				"depends_on": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs that must finish before this task dispatches.",
				},
				"reads": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Additional owner/name repositories the task may read.",
				},
				"writes": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "object"},
					"description": "Additional {repo, base} write targets for follow-up proposals.",
				},
				"custom_fields": map[string]any{
					"type":        "object",
					"description": "Declared custom field values keyed by field name.",
				},
				"network_access": map[string]any{
					"type":        "string",
					"description": "Per-task sandbox network override (e.g. 'open', 'none').",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model ID override from grain_filing_context.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional audit reason when filing with dangerous/admin escalations.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			if err := s.authorizeMCPRoleScope(ctx, "POST /api/tasks"); err != nil {
				return toolFailure(ToolGrainSubmitTask, err)
			}
			approve := false
			if v, ok := args["approve"].(bool); ok {
				approve = v
			}

			body := map[string]any{
				"approved": approve,
			}
			if v, ok := args["title"].(string); ok {
				body["title"] = v
			}
			if v, ok := args["description"].(string); ok {
				body["description"] = v
			}
			if v, ok := args["repo"].(string); ok && strings.TrimSpace(v) != "" {
				body["repo"] = strings.TrimSpace(v)
			}
			if v, ok := args["no_repo"].(bool); ok && v {
				body["noRepo"] = true
			}
			if v, ok := args["base"].(string); ok && strings.TrimSpace(v) != "" {
				body["base"] = strings.TrimSpace(v)
			}
			if v, ok := args["template_id"].(string); ok && strings.TrimSpace(v) != "" {
				body["templateId"] = strings.TrimSpace(v)
			}
			if v, ok := args["fields"].(map[string]any); ok && len(v) > 0 {
				fields := make(map[string]string, len(v))
				for k, val := range v {
					if val != nil {
						fields[k] = fmt.Sprint(val)
					}
				}
				body["fields"] = fields
			}
			if v, ok := args["board_id"].(string); ok && strings.TrimSpace(v) != "" {
				body["boardId"] = strings.TrimSpace(v)
			}
			if v, ok := intArg(args, "priority"); ok {
				body["priority"] = v
			}
			if v, ok := args["interactive"].(bool); ok && v {
				body["interactive"] = true
			}
			if v, ok := args["auto_merge"].(bool); ok {
				if v {
					body["autoMerge"] = true
				} else {
					body["declineAutoMerge"] = true
				}
			}
			if rawCaps, exists := args["capabilities"]; exists && rawCaps != nil {
				if caps, ok := stringSliceArg(rawCaps); ok {
					body["capabilities"] = caps
				}
			}
			// grants is forwarded as sent, so POST /api/tasks decodes it --
			// and refuses a mixed request -- exactly as it would from REST
			// or grain_invoke, rather than the task being filed without it.
			if rawGrants, exists := args["grants"]; exists && rawGrants != nil {
				body["grants"] = rawGrants
			}
			if rawDeps, exists := args["depends_on"]; exists && rawDeps != nil {
				if deps, ok := stringSliceArg(rawDeps); ok {
					body["dependsOn"] = deps
				}
			}
			if rawReads, exists := args["reads"]; exists && rawReads != nil {
				if reads, ok := stringSliceArg(rawReads); ok {
					body["reads"] = reads
				}
			}
			if rawWrites, exists := args["writes"]; exists && rawWrites != nil {
				body["writes"] = rawWrites
			}
			if rawCF, ok := args["custom_fields"].(map[string]any); ok && len(rawCF) > 0 {
				body["customFields"] = rawCF
			}
			if v, ok := args["network_access"].(string); ok && strings.TrimSpace(v) != "" {
				body["networkAccess"] = strings.TrimSpace(v)
			}
			if v, ok := args["model"].(string); ok && strings.TrimSpace(v) != "" {
				body["model"] = strings.TrimSpace(v)
			}

			reason, _ := args["reason"].(string)
			encoded, err := json.Marshal(body)
			if err != nil {
				return toolFailure(ToolGrainSubmitTask, err)
			}
			_, data, err := s.dispatchSubrequest(ctx, http.MethodPost, "/api/tasks", encoded, reason)
			if err != nil {
				return toolFailure(ToolGrainSubmitTask, err)
			}
			var created Task
			if json.Unmarshal(data, &created) == nil && created.ID != "" {
				compact := map[string]any{
					"id":          created.ID,
					"url":         "/tasks/" + created.ID,
					"title":       created.Title,
					"state":       created.State,
					"approved":    created.Approved,
					"repo":        created.Repo,
					"base":        created.Base,
					"interactive": created.Interactive,
				}
				if created.HeldBy != "" {
					compact["heldBy"] = created.HeldBy
				}
				if created.ApprovalNote != "" {
					compact["approvalNote"] = created.ApprovalNote
				}
				if out, err := json.Marshal(compact); err == nil {
					return mcp.Result{Text: string(out)}
				}
			}
			return mcp.Result{Text: string(data)}
		},
	}
}

func (s *Server) listTasksTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainListTasks,
		Description: "List Grain tasks via GET /api/tasks with compact summary rows by default.",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional owner/name repository filter.",
				},
				"state": map[string]any{
					"type":        "string",
					"description": "Optional comma-separated states (e.g. 'running,queued,proposed,awaiting_reply').",
				},
				"q": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive title substring filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of tasks to return (default 50).",
				},
				"summary": map[string]any{
					"type":        "boolean",
					"description": "Return compact summary rows without full descriptions (default true).",
				},
			},
		},
		ElisionAdvice: "filter with state, repo, q, or a smaller limit, then inspect one task with grain_get_task",
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			if err := s.authorizeMCPRoleScope(ctx, "GET /api/tasks"); err != nil {
				return toolFailure(ToolGrainListTasks, err)
			}
			values := url.Values{}
			if repo, _ := args["repo"].(string); strings.TrimSpace(repo) != "" {
				values.Set("repo", strings.TrimSpace(repo))
			}
			if state, _ := args["state"].(string); strings.TrimSpace(state) != "" {
				values.Set("state", strings.TrimSpace(state))
			}
			if q, _ := args["q"].(string); strings.TrimSpace(q) != "" {
				values.Set("q", strings.TrimSpace(q))
			}
			limit := 50
			if n, ok := intArg(args, "limit"); ok && n > 0 {
				limit = n
			}
			values.Set("limit", strconv.Itoa(limit))
			summary := true
			if b, ok := args["summary"].(bool); ok {
				summary = b
			}
			if summary {
				values.Set("fields", "summary")
			} else {
				values.Set("fields", "full")
			}

			path := "/api/tasks?" + values.Encode()
			_, data, err := s.dispatchSubrequest(ctx, http.MethodGet, path, nil, "")
			if err != nil {
				return toolFailure(ToolGrainListTasks, err)
			}
			return mcp.Result{Text: string(data)}
		},
	}
}

// The views grain_get_task renders a task in.
const (
	taskViewSummary      = "summary"
	taskViewConversation = "conversation"
	taskViewFull         = "full"
)

var taskViews = []string{taskViewSummary, taskViewConversation, taskViewFull}

func (s *Server) getTaskTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainGetTask,
		Description: "Read one Grain task via GET /api/tasks/{id}. Defaults to a compact ~1 KB 'summary' view; pass view='conversation' to page comments or view='full' for raw TaskDetail JSON.",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Task ID (e.g. '42').",
				},
				"view": map[string]any{
					"type":        "string",
					"enum":        taskViews,
					"description": "Output view: 'summary' (~1 KB default), 'conversation' (paged comments), or 'full' (complete JSON).",
				},
				"after": map[string]any{
					"type":        "integer",
					"description": "For view='conversation': return comments with ID > after (omit or -1 for the first page).",
				},
			},
			"required": []string{"id"},
		},
		ElisionAdvice: "use view='summary' or page comments with view='conversation' and after",
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			if err := s.authorizeMCPRoleScope(ctx, "GET /api/tasks/{id}"); err != nil {
				return toolFailure(ToolGrainGetTask, err)
			}
			rawID, ok := stringArg(args, "id")
			if !ok {
				return toolFailuref(ToolGrainGetTask, "id is required")
			}
			id := model.NormalizeTaskID(rawID)
			if id == "" {
				id = rawID
			}

			view, _ := args["view"].(string)
			view = strings.ToLower(strings.TrimSpace(view))
			if view == "" {
				view = taskViewSummary
			}
			if !slices.Contains(taskViews, view) {
				return toolFailuref(ToolGrainGetTask, "unknown view %q (want one of %s)", view, strings.Join(taskViews, ", "))
			}

			_, data, err := s.dispatchSubrequest(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, "")
			if err != nil {
				return toolFailure(ToolGrainGetTask, err)
			}
			if view == taskViewFull {
				return mcp.Result{Text: string(data)}
			}

			var detail TaskDetail
			if err := json.Unmarshal(data, &detail); err != nil {
				return toolFailuref(ToolGrainGetTask, "decoding task %s: %w", id, err)
			}
			rec := taskDetailToMCPRecord(detail)
			if view == taskViewConversation {
				after := int64(-1)
				if n, ok := intArg(args, "after"); ok {
					after = int64(n)
				}
				return mcp.Result{Text: mcp.RenderTaskConversation(rec, after)}
			}
			now := time.Now()
			if s.tasks != nil {
				now = s.tasks.now()
			}
			return mcp.Result{Text: mcp.RenderTaskSummary(rec, now)}
		},
	}
}

func taskDetailToMCPRecord(detail TaskDetail) mcp.TaskRecord {
	t := detail.Task
	var latest *time.Time
	for _, at := range []*time.Time{
		t.CreatedAt, t.StartedAt, t.EndedAt, t.ClosedAt, t.PrMergedAt, t.PrClosedAt,
		t.MergeQueueStateAt, t.MergeQueueStalledAt, t.MergeQueueBlockedAt,
	} {
		if at != nil && (latest == nil || at.After(*latest)) {
			latest = at
		}
	}
	var depStates map[string]string
	if len(t.DependsOnStates) > 0 {
		depStates = make(map[string]string, len(t.DependsOnStates))
		for id, st := range t.DependsOnStates {
			depStates[id] = string(st)
		}
	}
	summary := mcp.TaskSummary{
		ID:          t.ID,
		Title:       t.Title,
		State:       string(t.State),
		Repo:        t.Repo,
		Priority:    t.Priority,
		Interactive: t.Interactive,
		CreatedAt:   t.CreatedAt,
		BlockedBy:   t.BlockedBy,
		Stall:       t.Stall,
		TurnStopped: t.TurnStopped,
		MergeQueue: mcp.TaskMergeQueue{
			State:     t.MergeQueueState,
			Note:      t.MergeQueueNote,
			At:        t.MergeQueueStateAt,
			StalledAt: t.MergeQueueStalledAt,
			BlockedAt: t.MergeQueueBlockedAt,
		},
		Kinds:           TaskKindTags(t),
		DependsOn:       t.DependsOn,
		DependsOnStates: depStates,
		ChangedAt:       latest,
	}
	if t.Reason != nil {
		summary.Reason = &mcp.TaskReason{Reason: t.Reason.Reason, Resource: t.Reason.Resource}
	}
	rec := mcp.TaskRecord{
		TaskSummary:         summary,
		Description:         detail.Description,
		Author:              detail.Author,
		AuthorKind:          detail.AuthorKind,
		AuthorLabel:         detail.AuthorLabel,
		Provenance:          detail.Provenance,
		Base:                detail.Base,
		Capabilities:        detail.Capabilities,
		PullRequest:         detail.PullRequest,
		PullRequestMergedAt: detail.PrMergedAt,
		PullRequestClosedAt: detail.PrClosedAt,
		FailedAttempts:      detail.FailedAttempts,
		LastFailureAt:       detail.LastFailureAt,
		LastFailureReason:   detail.LastFailureReason,
		Activity: mcp.TaskActivity{
			Note:    detail.Activity,
			At:      detail.ActivityAt,
			BySetup: detail.ActivityBySetup,
		},
	}
	for _, a := range detail.Attempts {
		rec.Attempts = append(rec.Attempts, mcp.TaskAttempt{
			Number:         a.Number,
			StartedAt:      a.StartedAt,
			FinishedAt:     a.FinishedAt,
			Outcome:        a.Outcome,
			Detail:         a.Detail,
			PromptBytes:    a.PromptBytes,
			PromptTokens:   a.PromptTokens,
			Model:          a.Model,
			AgentFramework: a.AgentFramework,
		})
	}
	for _, tr := range detail.Transitions {
		rec.Transitions = append(rec.Transitions, mcp.TaskTransition{
			State:    string(tr.State),
			At:       tr.At,
			Inferred: tr.Inferred,
			By:       tr.By,
			Reason:   tr.Reason,
			Detail:   tr.Detail,
		})
	}
	for _, c := range detail.Comments {
		rec.Comments = append(rec.Comments, mcp.TaskComment{
			ID:          c.ID,
			Author:      c.Author,
			AuthorKind:  c.AuthorKind,
			AuthorLabel: c.Who(),
			FiledVia:    c.FiledVia,
			Body:        c.Body,
			CreatedAt:   c.CreatedAt,
			Handover:    c.Handover,
		})
	}
	return rec
}

// expectFields are the keys arguments.expect takes (expectation): every
// other key is refused rather than ignored, since an expectation a model
// believes it set and the call never checked is worse than none.
var expectFields = []string{"state", "title_prefix", "kind"}

func checkExpectFields(args map[string]any) error {
	raw, ok := args["expect"]
	if !ok || raw == nil {
		return nil
	}
	expect, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("expect must be an object with any of %s", strings.Join(expectFields, ", "))
	}
	var unknown []string
	for k := range expect {
		if !slices.Contains(expectFields, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("expect has no field %s; it takes %s", strings.Join(unknown, ", "), strings.Join(expectFields, ", "))
}

func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

func stringSliceArg(raw any) ([]string, bool) {
	switch val := raw.(type) {
	case []string:
		return slices.Clone(val), true
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}
