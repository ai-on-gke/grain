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
	"fmt"
	"io"
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
const ControllerMCPServerName = "grain-controller"

// Controller MCP tool names (6 fixed tools instead of ~277 per-route schemas).
const (
	ToolGrainExploreScope  = "grain_explore_scope"
	ToolGrainInvoke        = "grain_invoke"
	ToolGrainFilingContext = "grain_filing_context"
	ToolGrainSubmitTask    = "grain_submit_task"
	ToolGrainListTasks     = "grain_list_tasks"
	ToolGrainGetTask       = "grain_get_task"
)

// ControllerMCPToolNames returns the 6 fixed tool names served on POST /api/mcp.
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

// controllerMCPInstructions is the ~20-line workflow guide returned on MCP
// initialize so external harnesses and built-in sessions know how to discover
// and invoke controller routes without loading ~277 schemas into context.
const controllerMCPInstructions = `Grain Controller MCP workflow guide:
1. Everyday task operations have dedicated compact tools — prefer these first:
   - grain_list_tasks(repo?, state?, q?, limit?) to browse tasks (compact summary rows by default).
   - grain_get_task(id, view?) to inspect one task (~1 KB "summary" by default, "conversation" to page comments, "full" for raw JSON).
   - grain_filing_context(repo?) to read target repositories, templates, boards, custom fields, and grantable capabilities before filing work.
   - grain_submit_task(...) to file a task. approve defaults to false (filed as "proposed" awaiting human approval); only set approve=true when explicitly authorized.
2. All other Grain controller operations (~270+ routes across CI, merge queue, repos, releases, schedules, templates, settings, secrets, capabilities, deployment) use progressive discovery:
   - Call grain_explore_scope() with no arguments to list the scope groups you are authorized to use.
   - Call grain_explore_scope(scope="<group>") (e.g. "ci", "merge", "repos", "tasks") to get domain guidance and exact JSON input schemas for tools in that scope.
   - Call grain_invoke(tool="api_...", arguments={...}, reason?="...") to execute an authorized route.
3. Safety & authorization rules:
   - Destructive (DELETE) and dangerous-scope routes require a non-empty reason explaining why the change is being made.
   - Task-admin mutations accept arguments.expect ({state?, merge_queue_state?, has_pull_request?}) to guard against stale reads.
   - Calls run under your authenticated identity (and X-Grain-Acting-Task grants when present); if a scope is refused, ask a human to grant api:<scope> rather than working around it.`

var controllerAlwaysLoadMeta = map[string]any{
	"antigravity/alwaysLoad": true,
}

type controllerMCPRequestKey struct{}

func withControllerMCPRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, controllerMCPRequestKey{}, r)
}

func controllerMCPRequestFromContext(ctx context.Context) *http.Request {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(controllerMCPRequestKey{}).(*http.Request)
	return r
}

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
	actingTaskID  string
	externalToken *model.ExternalAgentToken
	fullAccess    bool
	grantSet      APIGrantSet
	heldGrants    []string
	channel       string
	repoLimits    []string
}

func (s *Server) resolveMCPCallerAccess(ctx context.Context, outerReq *http.Request) (mcpCallerAccess, error) {
	if outerReq == nil {
		return mcpCallerAccess{}, fmt.Errorf("missing outer HTTP request context")
	}
	taskID := strings.TrimSpace(outerReq.Header.Get(ActingTaskHeader))
	channel := strings.TrimSpace(outerReq.Header.Get(ChannelHeader))
	if taskID != "" {
		if s.tasks == nil || s.tasks.Store == nil {
			return mcpCallerAccess{actingTaskID: taskID, channel: channel}, errNoAgentAPI
		}
		acting, err := s.tasks.Store.GetTask(ctx, taskID)
		if err != nil || acting == nil {
			return mcpCallerAccess{actingTaskID: taskID, channel: channel}, fmt.Errorf("acting task %s could not be read: %v", taskID, err)
		}
		held := grantNames(acting.Grants)
		var set APIGrantSet
		if channel == SandboxCLIChannel {
			set = model.SandboxAPIGrantsOf(held)
		} else {
			set = APIGrantsOf(held)
		}
		return mcpCallerAccess{
			actingTaskID: taskID,
			grantSet:     set,
			heldGrants:   held,
			channel:      channel,
		}, nil
	}
	if tok := externalTokenFromContext(ctx); tok != nil {
		_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
		if err != nil {
			return mcpCallerAccess{externalToken: tok}, err
		}
		return mcpCallerAccess{
			externalToken: tok,
			grantSet:      set,
			repoLimits:    slices.Clone(tok.Repos),
		}, nil
	}
	return mcpCallerAccess{fullAccess: true}, nil
}

func (a mcpCallerAccess) allowsRoute(rt APIRoute) bool {
	if a.fullAccess {
		return rt.Scope != "" && rt.Scope != scopeInternal
	}
	if a.externalToken != nil {
		if _, blocked := isNoTaskContextRoute(rt.Pattern); blocked {
			return false
		}
		return a.grantSet.Has(rt.Scope) || tasksAdminAllowsTaskPatch(a.grantSet, rt.Pattern)
	}
	if a.channel == SandboxCLIChannel {
		if IsSandboxDeniedScope(rt.Scope) || sandboxChannelGuard(rt.Pattern, nil) != nil {
			return false
		}
	}
	return a.grantSet.Has(rt.Scope) ||
		tasksAdminAllowsTaskPatch(a.grantSet, rt.Pattern) ||
		agentCapabilityAllowsRoute(a.heldGrants, a.grantSet, rt.Pattern, rt.Scope) ||
		grainCIReadsRoute(a.heldGrants, rt.Pattern)
}

func (a mcpCallerAccess) authorizedRoutes() []APIRoute {
	all := AgentAPIRoutes()
	out := make([]APIRoute, 0, len(all))
	for _, rt := range all {
		if a.allowsRoute(rt) {
			out = append(out, rt)
		}
	}
	return out
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
	"repos":        "Target repository onboarding, branch configuration, CI mode, merge settings, and declarations.",
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
func (s *Server) dispatchSubrequest(ctx context.Context, outerReq *http.Request, method, pathWithQuery string, bodyBytes []byte, reason string) (int, []byte, error) {
	if outerReq == nil {
		return 0, nil, fmt.Errorf("missing outer HTTP request")
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
		onSuccess: func() {
			if s.tasks != nil {
				s.tasks.syncStateRepoAfterWrite(subCtx)
			}
		},
	}
	s.mux.ServeHTTP(rw, innerReq)
	rw.finish()
	return rec.Code, rec.Body.Bytes(), nil
}

func formatSubrequestError(code int, body []byte) error {
	var errBody struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &errBody) == nil && errBody.Error != "" && errBody.Code == "" {
		return fmt.Errorf("HTTP %d: %s", code, errBody.Error)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		trimmed = http.StatusText(code)
	}
	return fmt.Errorf("HTTP %d: %s", code, trimmed)
}

func (s *Server) exploreScopeTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainExploreScope,
		Description: "List authorized Grain controller API scope groups, or return domain guidance and JSON input schemas for one scope group or scope.",
		Meta:        controllerAlwaysLoadMeta,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{
					"type":        "string",
					"description": "Optional scope group (e.g. 'tasks', 'ci', 'merge', 'repos', 'settings') or exact scope (e.g. 'ci:read'). Omit to list all authorized scope groups.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			outerReq := controllerMCPRequestFromContext(ctx)
			access, err := s.resolveMCPCallerAccess(ctx, outerReq)
			if err != nil {
				return mcp.Result{Text: ToolGrainExploreScope + " failed: " + err.Error(), IsError: true}
			}
			authorized := access.authorizedRoutes()
			rawScope, _ := args["scope"].(string)
			query := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rawScope)), APIGrantPrefix)

			if query == "" {
				return renderScopeOverview(access, authorized)
			}
			return renderScopeDetail(access, authorized, query)
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
		"hint":                  "Call grain_explore_scope(scope='<group>') to view route tool names and JSON input schemas, then grain_invoke(tool, arguments, reason?) to execute.",
		"groups":                out,
	}
	encoded, _ := json.MarshalIndent(payload, "", "  ")
	return mcp.Result{Text: string(encoded)}
}

type exploredRouteSchema struct {
	Tool        string         `json:"tool"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Scope       APIScope       `json:"scope"`
	NeedsReason bool           `json:"needsReason,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
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

func renderScopeDetail(access mcpCallerAccess, authorized []APIRoute, query string) mcp.Result {
	presetScopes := ExpandAPIPreset(query)
	var matched []exploredRouteSchema
	for _, rt := range authorized {
		if !routeMatchesScopeQuery(rt, query, presetScopes) {
			continue
		}
		matched = append(matched, exploredRouteSchema{
			Tool:        rt.Tool,
			Method:      rt.Method,
			Path:        rt.Path,
			Scope:       rt.Scope,
			NeedsReason: rt.NeedsReason,
			Description: rt.Description,
			InputSchema: rt.inputSchema(),
		})
	}

	if len(matched) == 0 {
		// Distinguish "exists in Grain's API but caller lacks grant" from "unknown scope".
		var missingScopes []string
		for _, rt := range AgentAPIRoutes() {
			if routeMatchesScopeQuery(rt, query, presetScopes) {
				s := string(rt.Scope)
				if !slices.Contains(missingScopes, s) {
					missingScopes = append(missingScopes, s)
				}
			}
		}
		sort.Strings(missingScopes)
		if len(missingScopes) > 0 {
			return mcp.Result{
				Text:    fmt.Sprintf("Scope %q is not authorized for this caller (requires grant for %s).", query, strings.Join(missingScopes, ", ")),
				IsError: true,
			}
		}
		return mcp.Result{
			Text:    fmt.Sprintf("Unknown scope %q. Call %s with no arguments to list available scope groups.", query, ToolGrainExploreScope),
			IsError: true,
		}
	}

	domain, _, _ := strings.Cut(query, ":")
	guidance := domainGuidance[domain]
	if guidance == "" && len(matched) > 0 {
		guidance = domainGuidance[scopeDomainGroup(matched[0].Scope)]
	}
	payload := map[string]any{
		"scope":      query,
		"guidance":   guidance,
		"routeCount": len(matched),
		"routes":     matched,
	}
	encoded, _ := json.MarshalIndent(payload, "", "  ")
	return mcp.Result{Text: string(encoded)}
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
			outerReq := controllerMCPRequestFromContext(ctx)
			rawTool, _ := args["tool"].(string)
			toolName := strings.TrimSpace(rawTool)
			if toolName == "" {
				return mcp.Result{Text: ToolGrainInvoke + " failed: tool is required", IsError: true}
			}
			if !strings.HasPrefix(toolName, APIToolPrefix) {
				toolName = APIToolPrefix + toolName
			}

			var route *APIRoute
			for _, candidate := range AgentAPIRoutes() {
				if candidate.Tool == toolName {
					c := candidate
					route = &c
					break
				}
			}
			if route == nil {
				return mcp.Result{
					Text:    fmt.Sprintf("%s failed: unknown route tool %q (use %s to discover valid tool names)", ToolGrainInvoke, rawTool, ToolGrainExploreScope),
					IsError: true,
				}
			}

			access, err := s.resolveMCPCallerAccess(ctx, outerReq)
			if err != nil {
				return mcp.Result{Text: route.Tool + " failed: " + err.Error(), IsError: true}
			}
			if !access.allowsRoute(*route) {
				return mcp.Result{
					Text:    fmt.Sprintf("%s failed: HTTP 403: caller holds no grant for scope %s (%s)", route.Tool, route.Scope, route.Pattern),
					IsError: true,
				}
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
				return mcp.Result{
					Text:    fmt.Sprintf("%s failed: reason is required: %s is destructive (scope %s)", route.Tool, route.Pattern, route.Scope),
					IsError: true,
				}
			}

			path := route.Path
			for _, p := range route.Params {
				v, _ := routeArgs[p].(string)
				v = strings.TrimSpace(v)
				if v == "" {
					if raw, ok := routeArgs[p]; ok && raw != nil {
						v = strings.TrimSpace(fmt.Sprint(raw))
					}
				}
				if v == "" {
					return mcp.Result{Text: fmt.Sprintf("%s failed: %s is required", route.Tool, p), IsError: true}
				}
				escaped := url.PathEscape(v)
				path = strings.Replace(path, "{"+p+"...}", escaped, 1)
				path = strings.Replace(path, "{"+p+"}", escaped, 1)
			}

			if expect, ok := expectation(routeArgs); ok && route.takesExpect() {
				id, _ := routeArgs["id"].(string)
				id = strings.TrimSpace(id)
				code, data, err := s.dispatchSubrequest(ctx, outerReq, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, "")
				if err != nil {
					return mcp.Result{Text: fmt.Sprintf("%s failed: reading task %s to check expect: %v", route.Tool, id, err), IsError: true}
				}
				if code < 200 || code > 299 {
					return mcp.Result{
						Text:    fmt.Sprintf("%s failed: reading task %s to check expect: %v", route.Tool, id, formatSubrequestError(code, data)),
						IsError: true,
					}
				}
				var detail TaskDetail
				if err := json.Unmarshal(data, &detail); err != nil {
					return mcp.Result{Text: fmt.Sprintf("%s failed: reading task %s: %v", route.Tool, id, err), IsError: true}
				}
				if err := expect.Check(detail.Task); err != nil {
					return mcp.Result{Text: route.Tool + " failed: " + err.Error(), IsError: true}
				}
			}

			if q, ok := routeArgs["query"].(map[string]any); ok && len(q) > 0 {
				values := url.Values{}
				for k, v := range q {
					if v == nil {
						continue
					}
					values.Set(k, fmt.Sprint(v))
				}
				if encodedQuery := values.Encode(); encodedQuery != "" {
					path += "?" + encodedQuery
				}
			}

			var encodedBody []byte
			if route.Method != http.MethodGet {
				if body, ok := routeArgs["body"]; ok && body != nil {
					b, err := json.Marshal(body)
					if err != nil {
						return mcp.Result{Text: fmt.Sprintf("%s failed: encoding body: %v", route.Tool, err), IsError: true}
					}
					encodedBody = b
				} else if route.body != nil {
					fallback := map[string]any{}
					for k, v := range routeArgs {
						if k == "reason" || k == "query" || k == "expect" || k == "body" || slices.Contains(route.Params, k) {
							continue
						}
						fallback[k] = v
					}
					if len(fallback) > 0 {
						b, err := json.Marshal(fallback)
						if err != nil {
							return mcp.Result{Text: fmt.Sprintf("%s failed: encoding body: %v", route.Tool, err), IsError: true}
						}
						encodedBody = b
					}
				}
			}

			code, data, err := s.dispatchSubrequest(ctx, outerReq, route.Method, path, encodedBody, reason)
			if err != nil {
				return mcp.Result{Text: route.Tool + " failed: " + err.Error(), IsError: true}
			}
			if code < 200 || code > 299 {
				return mcp.Result{Text: route.Tool + " failed: " + formatSubrequestError(code, data).Error(), IsError: true}
			}
			if len(bytes.TrimSpace(data)) == 0 {
				return mcp.Result{Text: fmt.Sprintf("%s %s: %d %s", route.Method, path, code, http.StatusText(code))}
			}
			return mcp.Result{Text: string(data)}
		},
	}
}

func (a mcpCallerAccess) allowsFilingContext() bool {
	if a.fullAccess {
		return true
	}
	for _, sc := range []APIScope{
		"tasks:write", "tasks:from-template", "tasks:read", "tasks:admin",
		"repos:read", "templates:read", "boards:read", "settings:read", "deployment:read",
	} {
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
		for _, allowed := range a.repoLimits {
			if strings.EqualFold(allowed, repo) {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Server) filingContextTool() mcp.Tool {
	return mcp.Tool{
		Name:        ToolGrainFilingContext,
		Description: "Return target repositories, templates, boards, custom fields, and grantable capabilities needed before calling grain_submit_task.",
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
			outerReq := controllerMCPRequestFromContext(ctx)
			access, err := s.resolveMCPCallerAccess(ctx, outerReq)
			if err != nil {
				return mcp.Result{Text: ToolGrainFilingContext + " failed: " + err.Error(), IsError: true}
			}
			if !access.allowsFilingContext() {
				return mcp.Result{
					Text:    ToolGrainFilingContext + " failed: HTTP 403: caller holds no task, template, repo, or deployment read/write scope",
					IsError: true,
				}
			}
			if s.tasks == nil || s.tasks.Store == nil {
				return mcp.Result{Text: ToolGrainFilingContext + " failed: task store is not configured", IsError: true}
			}

			repoFilter, _ := args["repo"].(string)
			repoFilter = strings.TrimSpace(repoFilter)

			cfg, err := s.tasks.Store.GetConfig(ctx)
			if err != nil {
				return mcp.Result{Text: ToolGrainFilingContext + " failed: " + err.Error(), IsError: true}
			}
			team, err := s.tasks.GetTeam(ctx, model.DefaultTeamID)
			if err != nil {
				return mcp.Result{Text: ToolGrainFilingContext + " failed: " + err.Error(), IsError: true}
			}

			var targetRepos []string
			for _, r := range team.TargetRepos {
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

			customFields, err := s.tasks.Store.ListCustomFields(ctx)
			if err != nil {
				return mcp.Result{Text: ToolGrainFilingContext + " failed: " + err.Error(), IsError: true}
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
			var templates []compactTemplate
			if listedTemplates, err := s.tasks.ListTemplates(ctx); err == nil {
				for _, tmpl := range listedTemplates {
					if !access.repoAllowedForContext(tmpl.Repo) {
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
			var boards []compactBoard
			if listedBoards, err := s.tasks.ListBoards(ctx); err == nil {
				for _, b := range listedBoards {
					boards = append(boards, compactBoard{ID: b.ID, Name: b.Name})
				}
			}

			repoConfigs, _ := s.tasks.Store.ListRepoConfigs(ctx)
			var capabilityIDs []string
			var apiPresets []string
			for _, capRow := range s.tasks.capabilitiesWithReadiness(cfg, repoConfigs) {
				if strings.HasPrefix(capRow.ID, APIGrantPrefix) {
					if capRow.Preset {
						apiPresets = append(apiPresets, capRow.ID)
					}
					continue
				}
				capabilityIDs = append(capabilityIDs, capRow.ID)
			}

			var defaultCaps []string
			var defaultModel string
			var models any
			networkAccess := string(model.NetworkOpen)
			if cfg != nil {
				defaultModel = cfg.EffectiveDefaultModel()
				models = allDistinctModels(*cfg)
				networkAccess = string(model.NormalizeNetworkAccess(cfg.NetworkAccess))
				for _, id := range cfg.DefaultCapabilities {
					if _, ok := s.tasks.capabilityByID(id); ok {
						defaultCaps = append(defaultCaps, id)
					}
				}
			}

			out := map[string]any{
				"targetRepos":         targetRepos,
				"defaultCapabilities": defaultCaps,
				"capabilities":        capabilityIDs,
				"apiPresets":          apiPresets,
				"defaultModel":        defaultModel,
				"models":              models,
				"defaultNetwork":      networkAccess,
				"customFields":        customFieldDeclarations(customFields),
				"templates":           templates,
				"boards":              boards,
				"submitDefaults": map[string]any{
					"approve": false,
					"note":    "grain_submit_task defaults approve=false so tasks land in 'proposed' state awaiting human approval unless approve=true is explicitly passed.",
				},
			}
			encoded, _ := json.MarshalIndent(out, "", "  ")
			return mcp.Result{Text: string(encoded)}
		},
	}
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
			outerReq := controllerMCPRequestFromContext(ctx)
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
				return mcp.Result{Text: ToolGrainSubmitTask + " failed: " + err.Error(), IsError: true}
			}
			code, data, err := s.dispatchSubrequest(ctx, outerReq, http.MethodPost, "/api/tasks", encoded, reason)
			if err != nil {
				return mcp.Result{Text: ToolGrainSubmitTask + " failed: " + err.Error(), IsError: true}
			}
			if code < 200 || code > 299 {
				return mcp.Result{Text: ToolGrainSubmitTask + " failed: " + formatSubrequestError(code, data).Error(), IsError: true}
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
				if out, err := json.MarshalIndent(compact, "", "  "); err == nil {
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
			outerReq := controllerMCPRequestFromContext(ctx)
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
			code, data, err := s.dispatchSubrequest(ctx, outerReq, http.MethodGet, path, nil, "")
			if err != nil {
				return mcp.Result{Text: ToolGrainListTasks + " failed: " + err.Error(), IsError: true}
			}
			if code < 200 || code > 299 {
				return mcp.Result{Text: ToolGrainListTasks + " failed: " + formatSubrequestError(code, data).Error(), IsError: true}
			}
			return mcp.Result{Text: string(data)}
		},
	}
}

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
					"enum":        []string{"summary", "conversation", "full"},
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
			outerReq := controllerMCPRequestFromContext(ctx)
			rawID, _ := args["id"].(string)
			if rawID == "" {
				if v, ok := args["id"]; ok && v != nil {
					rawID = fmt.Sprint(v)
				}
			}
			id := model.NormalizeTaskID(strings.TrimSpace(rawID))
			if id == "" {
				id = strings.TrimSpace(rawID)
			}
			if id == "" {
				return mcp.Result{Text: ToolGrainGetTask + " failed: id is required", IsError: true}
			}

			view, _ := args["view"].(string)
			view = strings.ToLower(strings.TrimSpace(view))
			if view == "" {
				view = "summary"
			}
			if view != "summary" && view != "conversation" && view != "full" {
				return mcp.Result{
					Text:    fmt.Sprintf("%s failed: unknown view %q (want 'summary', 'conversation', or 'full')", ToolGrainGetTask, view),
					IsError: true,
				}
			}

			code, data, err := s.dispatchSubrequest(ctx, outerReq, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, "")
			if err != nil {
				return mcp.Result{Text: ToolGrainGetTask + " failed: " + err.Error(), IsError: true}
			}
			if code < 200 || code > 299 {
				return mcp.Result{Text: ToolGrainGetTask + " failed: " + formatSubrequestError(code, data).Error(), IsError: true}
			}
			if view == "full" {
				return mcp.Result{Text: string(data)}
			}

			var detail TaskDetail
			if err := json.Unmarshal(data, &detail); err != nil {
				return mcp.Result{Text: fmt.Sprintf("%s failed: decoding task %s: %v", ToolGrainGetTask, id, err), IsError: true}
			}
			rec := taskDetailToMCPRecord(detail)
			if view == "conversation" {
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
		Kinds:     TaskKindTags(t),
		DependsOn: t.DependsOn,
		ChangedAt: latest,
	}
	rec := mcp.TaskRecord{
		TaskSummary:       summary,
		Description:       detail.Description,
		Author:            detail.Author,
		AuthorKind:        detail.AuthorKind,
		Base:              detail.Base,
		Capabilities:      detail.Capabilities,
		PullRequest:       detail.PullRequest,
		FailedAttempts:    detail.FailedAttempts,
		LastFailureAt:     detail.LastFailureAt,
		LastFailureReason: detail.LastFailureReason,
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
			ID:         c.ID,
			Author:     c.Author,
			AuthorKind: c.AuthorKind,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			Handover:   c.Handover,
		})
	}
	return rec
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
