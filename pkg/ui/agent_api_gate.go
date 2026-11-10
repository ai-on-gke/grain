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
	"log"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ActingTaskHeader names the task whose run is making a request through
// the agent API: `grain mcpserver` sets it on every call a generated api
// tool makes (agent_api_tools.go). A request carrying it is held to that
// task's api grants by agentAPIGate below and attributed to it.
//
// The header can only narrow what a request may do, never widen it: a
// request without it is whatever the access gate already let in, so a
// caller able to send this header at all could have left it off. Which
// is why it is believed as it stands rather than signed.
const ActingTaskHeader = "X-Grain-Acting-Task"

// ActingReasonHeader carries the reason an agent gave for a call. A call
// under a dangerous scope, and every DELETE, is refused without one.
const ActingReasonHeader = "X-Grain-Acting-Reason"

// ChannelHeader names the channel an agent API request arrived on:
// "sandbox-cli" when forwarded by the sandbox proxy (127.0.0.1:9728).
const ChannelHeader = "X-Grain-Channel"

// SandboxCLIChannel is the ChannelHeader value the sandbox proxy sets on
// requests forwarded from inside a sandbox.
const SandboxCLIChannel = "sandbox-cli"

// ActingRunHeader optionally carries the live run ID resolved by the sandbox proxy.
const ActingRunHeader = "X-Grain-Acting-Run"

// IsSandboxDeniedScope reports whether scope is refused on the sandbox CLI channel.
func IsSandboxDeniedScope(scope APIScope) bool {
	return model.IsSandboxDeniedAPIScope(scope)
}

// maxAgentAPIBody bounds how much of a request body the gate reads to
// find the repository and the capabilities it names; the handler reads
// the same bytes again after.
const maxAgentAPIBody = 8 << 20

// selfMutableTaskRoutes are the task routes a run may call on its own
// task: everything else under /api/tasks/{id} could end it (stop, close,
// retry, defer), orphan it (withdraw its approval, change its
// dependencies), or rewrite what it was granted, and a run is not to do
// any of that to itself (task 420's self-protection rails).
var selfMutableTaskRoutes = map[string]bool{
	"POST /api/tasks/{id}/comments": true,
	"POST /api/tasks/{id}/messages": true,
}

// ownTaskOnlyRoutes are the task routes a run may call on its own task
// and on no other. POST /api/tasks/{id}/messages is the chat tool's write
// half: it speaks as the run of task {id} to the person watching it and
// parks {id} on their reply (Client.PostAgentMessage). Called on another
// task it put words in that task's mouth and parked it on the operator
// (task 768's advice to tasks 796, 657 and 736, which went to their
// operators as questions and never reached their agents).
var ownTaskOnlyRoutes = map[string]string{
	"POST /api/tasks/{id}/messages": "it says something to the human watching your own task and parks it on their reply",
}

// ownMessagesReadRoute is the read half of a run's conversation: what the
// person watching it said, and (agents=true) the notes other tasks'
// agents left for it. It is the run's own to read; any other task's
// needs api:task-admin, as message_task to an unrelated task does.
const ownMessagesReadRoute = "GET /api/tasks/{id}/messages"

// agentMessageRail refuses the two ways round a run can reach the wrong
// conversation: an own-task-only route on another task, which it points
// at POST /api/tasks/{id}/agent-messages instead, and that route on the
// run's own task, which is no other agent.
func agentMessageRail(acting, pattern, target string) error {
	if what, ok := ownTaskOnlyRoutes[pattern]; ok && target != acting {
		return fmt.Errorf("task %s cannot call %s on task %s: %s, so it takes only your own task's id (%s). "+
			"To send a message to task %s's agent, call POST /api/tasks/{id}/agent-messages "+
			"(the api_message_task_agent tool) with id %s; to leave a note for the people on it, "+
			"POST /api/tasks/{id}/comments", acting, pattern, target, what, acting, target, target)
	}
	if pattern == agentMessagesRoute && target == acting {
		return fmt.Errorf("task %s cannot send a message to its own agent with %s: to ask the human "+
			"watching your task something, use the chat tool (POST /api/tasks/{id}/messages with id %s)",
			acting, pattern, acting)
	}
	return nil
}

// agentMessagesRoute is one run's message to another task's agent
// (Client.PostMessageToAgent). The message is itself signed as the
// sender, so recordAgentCall writes no second note about it.
const agentMessagesRoute = "POST /api/tasks/{id}/agent-messages"

// taskNotesRoute is message_task (Client.PostTaskNote): a note for the
// acting task's parent or child.
const taskNotesRoute = "POST /api/tasks/{id}/notes"

// agentCall is one authorized call, for recordAgentCall.
type agentCall struct {
	taskID      string
	runID       string
	channel     string
	external    *model.ExternalAgentToken
	grants      APIGrantSet
	pattern     string
	scope       APIScope
	repo        string
	reason      string
	filedVia    string
	body        []byte
	targetTask  string
	targetTasks []string
	// session is whether the acting task is a console session, whose
	// calls are recorded with source session rather than sandbox.
	session bool
	// onBehalfOf is the person the call is made for, from grain's own
	// records (agentOnBehalfOf), and nil for nobody.
	onBehalfOf *model.Principal
}

// source is how the gate established the agent principal it runs this
// call as (operator-attribution spec §1.1.2).
func (c agentCall) source() model.AuthSource {
	switch {
	case c.external != nil:
		return model.SourceExternalToken
	case c.session:
		return model.SourceSession
	}
	return model.SourceSandbox
}

// owner is the person an external agent credential's call is made on
// behalf of (model.ExternalAgentToken.Owner), "" for none.
func (c agentCall) owner() string {
	if c.external == nil {
		return ""
	}
	return strings.TrimSpace(c.external.Owner)
}

func (c agentCall) principalID() string {
	if c.external != nil {
		return "external:" + c.external.Name
	}
	return c.taskID
}

func (c agentCall) journalActor() string {
	who := c.principalID()
	if owner := c.owner(); owner != "" {
		who += " on behalf of " + owner
	}
	return who
}

type agentCallContextKey struct{}

// withAgentCall records the agentCall that agentAPIGate authorized on ctx,
// so downstream checks (checkRepoPerson) know the request passed its
// route's api:<scope> gate even though its principal is PrincipalAgent.
func withAgentCall(ctx context.Context, call agentCall) context.Context {
	return context.WithValue(ctx, agentCallContextKey{}, call)
}

func agentCallFromContext(ctx context.Context) (agentCall, bool) {
	if ctx == nil {
		return agentCall{}, false
	}
	call, ok := ctx.Value(agentCallContextKey{}).(agentCall)
	if !ok || call.scope == "" {
		return agentCall{}, false
	}
	return call, true
}

// handlerName is the bare method name of a handler bound from a Server
// method ("handleRunSchedule"), or "" for anything else.
func handlerName(h http.HandlerFunc) string {
	fn := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
	if fn == nil {
		return ""
	}
	name := fn.Name()
	name = strings.TrimSuffix(name, "-fm")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if !strings.HasPrefix(name, "handle") {
		return ""
	}
	return name
}

// agentAPIGate wraps one route's handler: a request with no
// ActingTaskHeader passes straight through, and one with it is
// authorized against the acting task's api grants, run as that task's
// agent principal, and recorded.
func (s *Server) agentAPIGate(pattern string, next http.HandlerFunc) http.HandlerFunc {
	scope := routeScopes[pattern]
	return func(w http.ResponseWriter, r *http.Request) {
		if pattern == "POST /api/mcp" {
			next(w, r)
			return
		}
		if tok := externalTokenFromContext(r.Context()); tok != nil && ownCredentialRoute(pattern, r, tok) {
			next(w, r)
			return
		}
		if pattern != "POST /mcp" {
			if tok := externalTokenFromContext(r.Context()); tok != nil {
				call, err := s.authorizeExternalAgentCall(r, pattern, scope, tok)
				if err != nil {
					if call.filedVia != "" {
						log.Printf("grain api: refused {agent, %s} %s client=%q: %v", call.journalActor(), pattern, call.filedVia, err)
					} else {
						log.Printf("grain api: refused {agent, %s} %s: %v", call.journalActor(), pattern, err)
					}
					writeError(w, agentRefusalStatus(err), err)
					return
				}
				agent := model.Principal{Kind: model.PrincipalAgent, ID: call.principalID(), Source: call.source()}
				callCtx := withAgentCall(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), call)
				r = r.WithContext(model.WithEventActor(callCtx, RequestActor(callCtx)))
				s.serveAgentCall(w, r, next, call)
				return
			}
		}
		taskID := strings.TrimSpace(r.Header.Get(ActingTaskHeader))
		if taskID == "" {
			next(w, r)
			return
		}
		call, err := s.authorizeAgentCall(r, pattern, scope, taskID)
		if err != nil {
			log.Printf("grain api: refused {agent, %s} %s: %v", taskID, pattern, err)
			writeError(w, agentRefusalStatus(err), err)
			return
		}
		agent := model.Principal{Kind: model.PrincipalAgent, ID: taskID, Source: call.source()}
		ctx := withOnBehalfOf(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), call.onBehalfOf)
		callCtx := withAgentCall(ctx, call)
		r = r.WithContext(model.WithEventActor(callCtx, RequestActor(callCtx)))
		s.serveAgentCall(w, r, next, call)
	}
}

// ownCredentialRoute is a call an external agent credential may always
// make about itself, whatever its scopes: ask what it is (GET
// /api/auth/whoami) and revoke itself (`grain logout`), both
// external-cli.spec.md#XC-8 and #XC-9.
func ownCredentialRoute(pattern string, r *http.Request, tok *model.ExternalAgentToken) bool {
	switch pattern {
	case "GET /api/auth/whoami":
		return true
	case "POST /api/external-agents/tokens/{id}/revoke":
		return strings.TrimSpace(r.PathValue("id")) == tok.ID
	}
	return false
}

// agentRefusalStatus is the status a refused agent call answers with: 400
// for a request that is malformed (a repo that is not one), 403 for
// every refusal of what the agent may do.
func agentRefusalStatus(err error) int {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return http.StatusBadRequest
	}
	return http.StatusForbidden
}

var errNoAgentAPI = errors.New("this server has no task store, so it cannot check an agent's api grants")

// authorizeAgentCall decides whether taskID's run may make this call.
func (s *Server) authorizeAgentCall(r *http.Request, pattern string, scope APIScope, taskID string) (agentCall, error) {
	call := agentCall{
		taskID:   taskID,
		runID:    strings.TrimSpace(r.Header.Get(ActingRunHeader)),
		channel:  strings.TrimSpace(r.Header.Get(ChannelHeader)),
		pattern:  pattern,
		scope:    scope,
		reason:   strings.TrimSpace(r.Header.Get(ActingReasonHeader)),
		filedVia: model.SanitizeGrainClient(r.Header.Get(model.GrainClientHeader)),
	}
	if scope == "" || scope == scopeInternal {
		return call, fmt.Errorf("%s is not part of the agent API (docs/agent-api.md)", pattern)
	}
	if call.channel == SandboxCLIChannel && IsSandboxDeniedScope(scope) {
		return call, fmt.Errorf("%s requires scope %s, which is not permitted on the %s channel", pattern, scope, SandboxCLIChannel)
	}
	if s.tasks == nil || s.tasks.Store == nil {
		return call, errNoAgentAPI
	}
	ctx := r.Context()
	acting, err := s.tasks.Store.GetTask(ctx, taskID)
	if err != nil || acting == nil {
		return call, fmt.Errorf("acting task %s could not be read: %v", taskID, err)
	}
	call.session = acting.IsConsoleSession()
	call.onBehalfOf = agentOnBehalfOf(ctx, s.tasks.Store, *acting)
	held := grantNames(acting.Grants)
	var set APIGrantSet
	if call.channel == SandboxCLIChannel {
		set = model.SandboxAPIGrantsOf(held)
	} else {
		set = APIGrantsOf(held)
	}
	// The director never reaches a Tier 3 scope, however it came to
	// hold a grant for one (model.WithinDirectorFloor).
	set = model.WithinDirectorFloor(*acting, set)
	call.grants = set
	capAllowed := agentCapabilityAllowsRoute(held, set, pattern, scope)
	ciCap := grainCIReadsRoute(held, pattern)
	adminPatch := tasksAdminAllowsTaskPatch(set, pattern)
	detach := capabilitiesDetachAllowsRoute(set, pattern)
	if fsMergeQueueByCI(set, pattern, r) {
		capAllowed = true
	}
	if !set.Has(scope) && !capAllowed && !ciCap && !adminPatch && !detach {
		return call, fmt.Errorf("task %s holds no api grant for scope %s, which %s needs: "+
			"ask a person to grant api:%s or a preset that includes it (docs/agent-api.md)", taskID, scope, pattern, scope)
	}
	body, err := peekBody(r)
	if err != nil {
		return call, err
	}
	call.body = body
	method, _, _ := strings.Cut(pattern, " ")
	if target := r.PathValue("id"); target != "" && strings.HasPrefix(pathOf(pattern), "/api/tasks/{id}") {
		call.targetTask = target
	}
	call.repo, err = s.agentCallRepo(ctx, r, pattern, call.targetTask, body, acting, set, scope, ciCap)
	if err != nil {
		return call, err
	}
	if _, fs := fsRoutes[pattern]; (fs || pattern == "GET /api/events") && call.repo == "" && set.Has(scope) {
		// A namespace read or event stream naming no repository is narrowed
		// to the ones the grant reaches by fsScope / eventsReadable, not
		// refused for naming none.
		capAllowed = true
	}
	if isRepoNarrowedTasksRead(pattern, r) && call.repo == "" && set.Has(scope) {
		capAllowed = true
	}
	if ciCap && !capAllowed && !set.Allows(scope, call.repo) && call.repo != "" && agentMayRead(acting, call.repo) {
		// grain-ci reads CI of the repositories the task's runs may
		// read, as it does over /fs/ci/ and /cli/ci/.
		capAllowed = true
	}
	if adminPatch && !set.Allows(scope, call.repo) && set.Allows("tasks:admin", call.repo) {
		if err := tasksAdminTaskPatchBodyAllowed(body); err != nil {
			return call, err
		}
		capAllowed = true
	}
	if detach && !set.Allows(scope, call.repo) && set.Allows("capabilities:detach", call.repo) {
		if err := s.capabilitiesDetachAllowed(ctx, "task "+taskID, pattern, call.repo, call.targetTask, call.reason, body); err != nil {
			return call, err
		}
		capAllowed = true
	}
	if !set.Allows(scope, call.repo) && !capAllowed {
		missingScope := scope
		if !set.Has(scope) && adminPatch {
			missingScope = "tasks:admin"
		}
		if !set.Has(scope) && detach {
			missingScope = "capabilities:detach"
		}
		if call.repo == "" {
			return call, fmt.Errorf("task %s holds %s only for some repositories, and %s does not name one it reaches",
				taskID, missingScope, pattern)
		}
		return call, fmt.Errorf("task %s holds %s, but not on %s", taskID, missingScope, call.repo)
	}
	if call.channel == SandboxCLIChannel {
		if err := sandboxChannelGuard(pattern, body); err != nil {
			return call, err
		}
	}
	if pattern == "PATCH /api/tasks/{id}" && patchTouchesTaskGrants(body) {
		if !set.Allows("capabilities:admin", call.repo) {
			return call, fmt.Errorf("task %s holds no api grant for scope capabilities:admin, which %s needs when editing task capabilities",
				taskID, pattern)
		}
		if call.reason == "" {
			return call, fmt.Errorf("%s with capabilities is destructive (scope capabilities:admin): give a reason", pattern)
		}
	}
	if (IsDangerousAPIScope(scope) || method == http.MethodDelete) && call.reason == "" {
		return call, fmt.Errorf("%s is destructive (scope %s): give a reason", pattern, scope)
	}
	if err := s.agentRails(ctx, acting, pattern, method, scope, call.targetTask, r.PathValue("id")); err != nil {
		return call, err
	}
	if err := s.agentStandingDeclaredPush(ctx, pattern, r.PathValue("id")); err != nil {
		return call, err
	}
	if err := agentGrantEscalationFrom(held, pattern, body, s.storedFieldGrants(ctx, pattern, call.repo), s.tasks.normalizeCapabilityID); err != nil {
		return call, err
	}
	if err := s.checkExternalTokenRotationEscalation(ctx, held, pattern, r.PathValue("id")); err != nil {
		return call, err
	}
	if err := s.agentFilingEscalation(ctx, acting, set, pattern, scope, call.repo, body); err != nil {
		return call, err
	}
	if pattern == "PATCH /api/tasks/{id}" {
		if err := agentTaskPatchBodyAllowed(body); err != nil {
			return call, err
		}
	}
	return call, nil
}

// agentCapabilityAllowsRoute reports whether releases:read lets a release
// manager read (never write) the release notes. Writing them stays with
// release-notes:write (and its preset, api:release-notes): releases:write
// is a per-repository release scope, and the weekly notes are the
// deployment's own.
func agentCapabilityAllowsRoute(held []string, set APIGrantSet, pattern string, scope APIScope) bool {
	path := pathOf(pattern)
	if path == "/api/release-notes" || strings.HasPrefix(path, "/api/release-notes/") {
		if scope == "release-notes:read" && set.Allows("releases:read", "") {
			return true
		}
	}
	return false
}

func tasksAdminAllowsTaskPatch(set APIGrantSet, pattern string) bool {
	return pattern == "PATCH /api/tasks/{id}" && set.Has("tasks:admin")
}

func tasksAdminTaskPatchBodyAllowed(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return errors.New("api:tasks:admin without api:tasks:write requires a JSON object editing priority on PATCH /api/tasks/{id}")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || len(fields) == 0 {
		return errors.New("api:tasks:admin without api:tasks:write requires a single JSON object editing priority on PATCH /api/tasks/{id}")
	}
	for k, raw := range fields {
		if gateKey(k) != "priority" {
			return fmt.Errorf("api:tasks:admin without api:tasks:write may only edit priority on PATCH /api/tasks/{id} (got %q)", k)
		}
		var on bool
		if json.Unmarshal(raw, &on) == nil {
			return errors.New("api:tasks:admin without api:tasks:write may only edit numeric priority on PATCH /api/tasks/{id}")
		}
		if _, err := decodeTaskPriority(raw); err != nil || strings.TrimSpace(string(raw)) == "null" {
			return errors.New("api:tasks:admin without api:tasks:write requires a numeric priority on PATCH /api/tasks/{id}")
		}
	}
	return nil
}

// capabilitiesDetachAllowed holds a call admitted through
// capabilities:detach alone (capabilitiesDetachAllowsRoute) to what that
// scope is for (director-capabilities.spec.md §3.1.2): a non-empty reason,
// a body that only removes (capabilitiesDetachBodyAllowed), and never a
// standing session's task -- not another session's, and not the caller's
// own. who names the caller in the refusal.
func (s *Server) capabilitiesDetachAllowed(ctx context.Context, who, pattern, repo, target, reason string, body []byte) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%s under scope capabilities:detach removes grants: give a reason (%s)", pattern, ActingReasonHeader)
	}
	if pattern == "POST /api/tasks/{id}/capabilities" {
		if target == "" {
			return fmt.Errorf("%s under scope capabilities:detach names no task", pattern)
		}
		t, err := s.tasks.Store.GetTask(ctx, target)
		if err != nil || t == nil {
			return fmt.Errorf("task %s could not be read: %v", target, err)
		}
		if t.IsConsoleSession() {
			session, _ := model.ConsoleSessionIDOf(*t)
			return fmt.Errorf("%s cannot detach grants from task %s: it is the standing session %q, and "+
				"capabilities:detach never changes a standing session's grants (its own included)", who, target, session)
		}
	}
	return capabilitiesDetachBodyAllowed(pattern, body, s.storedFieldGrants(ctx, pattern, repo), s.tasks.normalizeCapabilityID)
}

// capabilitiesDetachBodyAllowed reports whether body only removes grants
// on pattern: "attach": false with no "replace" on a task, or a repository
// list every entry of which is already stored on that field (stored, from
// storedFieldGrants). Anything else -- an attach, a replacement, a list
// naming something the row does not hold, or a body that does not name
// the list at all -- needs capabilities:admin.
func capabilitiesDetachBodyAllowed(pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) error {
	trimmed := bytes.TrimSpace(body)
	switch pattern {
	case "POST /api/tasks/{id}/capabilities":
		var req setCapabilityRequest
		if len(trimmed) == 0 || json.Unmarshal(trimmed, &req) != nil {
			return fmt.Errorf("capabilities:detach requires a JSON object with \"attach\": false on %s", pattern)
		}
		if req.Attach == nil || *req.Attach {
			return fmt.Errorf("capabilities:detach may only remove a grant on %s (\"attach\": false); attaching one needs capabilities:admin", pattern)
		}
		if req.Replace != "" {
			return fmt.Errorf("capabilities:detach cannot replace a grant on %s; replacing one needs capabilities:admin", pattern)
		}
		if strings.TrimSpace(req.ID) == "" && strings.TrimSpace(req.Grant) == "" && strings.TrimSpace(req.Capability) == "" {
			return fmt.Errorf("capabilities:detach requires the grant to remove (id, grant, or capability) on %s", pattern)
		}
		return nil
	case "PUT /api/repos/{owner}/{name}/capabilities", "PUT /api/repos/{owner}/{name}/ci-capabilities":
		field, key := "defaultCapabilities", "defaultcapabilities"
		if pattern == "PUT /api/repos/{owner}/{name}/ci-capabilities" {
			field, key = "ciCapabilities", "cicapabilities"
		}
		var fields map[string]json.RawMessage
		if len(trimmed) == 0 || json.Unmarshal(trimmed, &fields) != nil {
			return fmt.Errorf("capabilities:detach requires a JSON object naming %s on %s", field, pattern)
		}
		var list []string
		found := false
		for k, raw := range fields {
			if gateKey(k) != key {
				return fmt.Errorf("capabilities:detach may only narrow %s on %s (got %q)", field, pattern, k)
			}
			if json.Unmarshal(raw, &list) != nil || list == nil {
				return fmt.Errorf("capabilities:detach requires %s to be a list on %s", field, pattern)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("capabilities:detach requires a JSON object naming %s on %s", field, pattern)
		}
		existing := stored[key]
		for _, name := range list {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if !grantAlreadyStored(existing, name, normalize) {
				return fmt.Errorf("capabilities:detach may only narrow %s on %s, and %q is not already on it; "+
					"adding one needs capabilities:admin", field, pattern, name)
			}
		}
		return nil
	}
	return fmt.Errorf("%s is not a route capabilities:detach reaches", pattern)
}

// agentTaskPatchBodyAllowed refuses empty or no-op PATCH /api/tasks/{id}
// bodies from agent callers. Malformed JSON or wrong field types are left to
// readJSON so they keep their usual syntax/type error messages.
func agentTaskPatchBodyAllowed(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return validationErrorf("PATCH /api/tasks/{id} requires a JSON object naming at least one field to edit")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return nil
	}
	if len(fields) == 0 {
		return validationErrorf("PATCH /api/tasks/{id} requires at least one field to edit")
	}
	var req UpdateTaskRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return nil
	}
	if reflect.ValueOf(req).IsZero() {
		return validationErrorf("PATCH /api/tasks/{id} requires at least one recognized non-null field to edit")
	}
	return nil
}

// grainCIReadRoutes are the agent API's CI reads the grain-ci capability
// reaches on its own, for the repositories its task's runs may read
// (Task.Repo, Reads and Writes): the same records /fs/ci/ and /cli/ci/
// serve a run holding it.
var grainCIReadRoutes = map[string]bool{
	"GET /api/ci/runs":                             true,
	"GET /api/ci/runs/{id}":                        true,
	"GET /api/ci/runs/{id}/log":                    true,
	"GET /api/ci/runs/{id}/artifacts":              true,
	"GET /api/ci/runs/{id}/artifacts/{job}/{name}": true,
	"GET /api/ci/workflows":                        true,
}

// grainCIReadsRoute reports whether held includes grain#ci (model.HasGrainCI) and pattern is
// one of the CI reads it opens. Which repository is the caller's to
// check once it is known (authorizeAgentCall).
func grainCIReadsRoute(held []string, pattern string) bool {
	return grainCIReadRoutes[pattern] && model.HasGrainCI(held)
}

// agentRepoDefaultRoutes are the read-only collection routes that filter
// by ?repo=, and that a caller reaching only one repository may call
// without naming it.
var agentRepoDefaultRoutes = map[string]bool{
	"GET /api/tasks":                   true,
	"GET /api/ci/runs":                 true,
	"GET /api/ci/workflows":            true,
	"GET /api/merge-batches":           true,
	"GET /api/base-branch-gates":       true,
	"GET /api/pipeline/changes":        true,
	"GET /api/memory":                  true,
	"GET /api/memory/documents/{name}": true,
	"GET /api/memory/documents":        true,
	"GET /api/templates":               true,
	"GET /api/suite-runs":              true,
	"GET /api/ci/faults":               true,
}

// sandboxChannelGuard enforces the additional restrictions required when an
// agent API route is called over X-Grain-Channel: sandbox-cli.
func sandboxChannelGuard(pattern string, body []byte) error {
	switch pattern {
	case "POST /api/tasks/{id}/messages",
		"POST /api/tasks/{id}/park-until",
		"POST /api/tasks/{id}/resume-later":
		return fmt.Errorf("%s is a model-only control action and cannot be called from the sandbox CLI", pattern)
	case "POST /api/tasks/{id}/wait-on":
		var req struct {
			Park bool `json:"park"`
		}
		if len(body) > 0 && json.Unmarshal(body, &req) == nil && req.Park {
			return errors.New("parking a run (park: true) is a model-only control action; grain task wait supports blocking waits only")
		}
	case "PATCH /api/tasks/{id}", "POST /api/tasks/{id}/capabilities":
		if len(grantsInBody(pattern, body)) > 0 || (pattern == "PATCH /api/tasks/{id}" && patchTouchesTaskGrants(body)) {
			return errors.New("attaching capabilities to an existing task is not permitted from the sandbox CLI")
		}
	case "POST /api/tasks":
		var fields map[string]json.RawMessage
		if len(body) > 0 && json.Unmarshal(body, &fields) == nil {
			for k, raw := range fields {
				if strings.EqualFold(k, "supersedes") {
					var sup string
					if json.Unmarshal(raw, &sup) == nil && strings.TrimSpace(sup) != "" {
						return errors.New("supersedes is not permitted on the sandbox credential; use the propose_task control tool")
					}
				}
			}
		}
	}
	return nil
}

// agentRails are the self-protection rails: a run cannot end or orphan
// its own run, cannot act on a standing session it does not own, and
// honours an upgrade's hold on dispatch.
func (s *Server) agentRails(ctx context.Context, acting *model.Task, pattern, method string, scope APIScope,
	target, sessionID string) error {
	if pattern == ownMessagesReadRoute && target != "" && model.NormalizeTaskID(target) != acting.ID && !isTaskAdmin(acting) {
		return fmt.Errorf("task %s cannot read task %s's messages: %s is a run's own conversation and the notes "+
			"other tasks' agents left for it (message_task), so it takes only your own task's id (%s). "+
			"Reading another task's needs api:task-admin", acting.ID, target, pattern, acting.ID)
	}
	if method == http.MethodGet {
		return nil
	}
	ownSession, _ := model.ConsoleSessionIDOf(*acting)
	if target != "" {
		if err := agentMessageRail(acting.ID, pattern, target); err != nil {
			return err
		}
		if target == acting.ID && !selfMutableTaskRoutes[pattern] {
			return fmt.Errorf("task %s cannot call %s on itself: a run does not end, orphan or regrant its own task", acting.ID, pattern)
		}
		if t, err := s.tasks.Store.GetTask(ctx, target); err == nil && t != nil {
			// A note between parent and child (message_task) is the one
			// call on a session the acting task does not own that the rail
			// lets through: it never parks the session or acts on its run,
			// and the CLI and MCP hops already carry it, so a director's
			// child reports back whichever tool it uses.
			if session, ok := model.ConsoleSessionIDOf(*t); ok && session != ownSession &&
				!(pattern == taskNotesRoute && related(acting, t)) {
				return fmt.Errorf("task %s is the standing session %q, which task %s does not own", target, session, acting.ID)
			}
		}
		if pattern == "POST /api/tasks/{id}/run-now" || pattern == "POST /api/tasks/{id}/pause" || pattern == "POST /api/tasks/{id}/resume" {
			me := model.Principal{Kind: model.PrincipalAgent, ID: acting.ID}
			if p, err := s.tasks.Store.GetPause(ctx, target); err == nil && p != nil && !p.PausedBy.Same(me) {
				return fmt.Errorf("task %s is paused by %s %s; only they may lift or override that pause",
					target, p.PausedBy.Kind, principalName(p.PausedBy))
			}
		}
	}
	if (pattern == approveTaskRoute || pattern == runNowTaskRoute) && target != "" {
		if err := s.agentApprovalRail(ctx, acting, target, pattern == runNowTaskRoute); err != nil {
			return err
		}
	}
	path := pathOf(pattern)
	if strings.HasPrefix(path, "/api/console/{id}") {
		return fmt.Errorf("task %s cannot type into or close a live console through the agent API", acting.ID)
	}
	if strings.HasPrefix(path, "/api/console-sessions") {
		// Creating, deleting, restarting or cancelling a session ends
		// somebody's run; editing one is only a run's own to do.
		if pattern != "PUT /api/console-sessions/{id}" || ownSession == "" || ownSession != sessionID {
			return fmt.Errorf("task %s may only edit its own standing session, and never create, restart, "+
				"cancel or delete one", acting.ID)
		}
	}
	upgradeHold := pattern == "POST /api/pipeline-pause" ||
		pattern == "DELETE /api/pipeline-pause" || pattern == "DELETE /api/pause" ||
		strings.HasPrefix(string(scope), "deployment:") && scope != "deployment:read"
	if upgradeHold {
		pause, err := s.tasks.Store.GetPipelinePause(ctx)
		if err != nil {
			return fmt.Errorf("reading the pipeline pause: %w", err)
		}
		if pause != nil {
			me := model.Principal{Kind: model.PrincipalAgent, ID: acting.ID}
			switch {
			case strings.HasPrefix(path, "/api/pipeline-pause") || strings.HasPrefix(path, "/api/pause"):
				if !pause.By.Same(me) {
					return fmt.Errorf("dispatch was paused by %s %s (%s); only they may lift it", pause.By.Kind, pause.By.ID, pause.Reason)
				}
				if method == http.MethodDelete && (!pause.AdmitsRunNow() || s.tasks.upgradeUnderway(ctx)) {
					return fmt.Errorf("an upgrade holds the queue (%s); %s waits for it", pause.Reason, pattern)
				}
			case pause.UntilTaskID != "" && pause.UntilTaskID != acting.ID:
				return fmt.Errorf("an upgrade hold is standing for task %s (%s); %s waits for it", pause.UntilTaskID, pause.Reason, pattern)
			}
		}
	}
	return nil
}

// approveTaskRoute is the agent API's approval: the person's half of
// every proposal gate, which agentApprovalRail keeps an agent from
// spending on its own work or on work held for a person.
const approveTaskRoute = "POST /api/tasks/{id}/approve"

// runNowTaskRoute dispatches a task ahead of the queue, and approves it
// first when it has no approval yet (Client.RunNow); agentApprovalRail
// applies to that implicit approval too.
const runNowTaskRoute = "POST /api/tasks/{id}/run-now"

// maxProposalLineageDepth bounds agentApprovalRail's walk up a task's
// LinkProposedBy chain. Proposal trees are shallow in practice; the bound
// is only there so a malformed chain cannot make one request walk the
// store forever (the walk also stops at a task it has already seen).
const maxProposalLineageDepth = 256

// agentApprovalRail refuses an agent approving target
// (POST /api/tasks/{id}/approve, or POST /api/tasks/{id}/run-now on an
// unapproved task) when the approval would be the agent deciding something
// a person must
// (design/spec/director/director-capabilities.spec.md §3.3 item 3, G8):
//
//   - target is in acting's own proposal lineage: acting proposed or filed
//     it, or proposed a task that proposed it, however far down the
//     chain. Approving it would be a run queueing its own work on a
//     permission nobody gave that work.
//   - target is held for a person (proposalPersonHolds): it asked for
//     hold_for_approval, targets grain/config or a person-held change
//     target, carries a declared push set or ungranted auto-merge, holds a
//     requires_person or person-only grant, or asks for capabilities,
//     writes, reads or settings its own proposer could not give it.
//
// It holds whatever acting is granted, api:tasks:admin and dispatch:write
// included, which is what makes it a rail rather than a scope: tasks:admin
// being a person's grant says a person trusts the run to approve other
// work, not that the run may stand in for the person every hold is waiting
// for. When onlyIfUnapproved is true (POST /api/tasks/{id}/run-now), an
// already-approved target passes straight through so dispatch:write can
// still jump queued work ahead of the backlog.
func (s *Server) agentApprovalRail(ctx context.Context, acting *model.Task, target string, onlyIfUnapproved bool) error {
	id := model.NormalizeTaskID(target)
	task, err := s.tasks.Store.GetTask(ctx, id)
	if err != nil {
		return fmt.Errorf("reading task %s: %w", id, err)
	}
	if task == nil || (onlyIfUnapproved && task.Approval != nil) {
		// Not there (the handler's own 404 says so), or already approved on a
		// route that only approves unapproved tasks.
		return nil
	}
	me := model.NormalizeTaskID(acting.ID)
	proposer, chain, err := s.tasks.proposalLineage(ctx, *task, me)
	if err != nil {
		return err
	}
	if len(chain) > 0 {
		how := "it proposed it"
		if len(chain) > 1 {
			how = "it is in the chain that proposed it (" + strings.Join(chain, " → ") + ")"
		}
		return fmt.Errorf("task %s cannot approve task %s: %s, and an agent never approves its own proposals, "+
			"directly or through the tasks they filed, even under api:tasks:admin or dispatch:write -- a person approves them "+
			"(docs/agent-api.md)", me, task.ID, how)
	}
	if holds, err := s.tasks.proposalPersonHolds(ctx, *task, proposer, acting); err != nil {
		return err
	} else if len(holds) > 0 {
		return fmt.Errorf("task %s cannot approve task %s: it is held for a person (%s), and an agent never "+
			"approves a task held for a person, even under api:tasks:admin or dispatch:write (docs/agent-api.md)",
			me, task.ID, strings.Join(holds, "; "))
	}
	return nil
}

// externalApprovalRail is agentApprovalRail for an external agent
// credential (principal "external:<name>"): the same G8 rail, asked of a
// caller that has no task of its own. An owned credential is its owner's
// delegate, not its owner, so it never stands in for the person a hold
// is waiting for (docs/agent-api.md, "Routes without calling-task
// context"). It refuses approving target -- over POST
// /api/tasks/{id}/approve, or POST /api/tasks/{id}/run-now on an
// unapproved task -- when:
//
//   - target is in the credential's own lineage: the credential filed it,
//     or filed a task whose LinkProposedBy chain proposed it
//     (Client.externalApprovalRefusal);
//   - target is held for a person (proposalPersonHolds, with no acting
//     task): hold_for_approval, grain/config, a person-held change
//     target, a declared push set, auto-merge, a requires_person or
//     person-only grant, keep_running or admission above standard, or an
//     escalation past its in-grain proposer;
//   - on run-now, the credential holds dispatch:write but not tasks:admin
//     on the task's repository -- or, for a task with no repository, not
//     tasks:admin on every repository: run-now's implicit approval is an
//     approval, so it asks for exactly the scope the approve route's own
//     scope check does (set.Allows("tasks:admin", repo)), and the two
//     routes agree on every unapproved task. tasks:admin held only for
//     some repositories never approves a task that names none;
//   - on run-now, the call gives no reason (X-Grain-Acting-Reason):
//     approve is destructive under tasks:admin and refused without one,
//     so the implicit approval asks for it too.
//
// An already-approved target passes run-now straight through, as it does
// for a task's run, so dispatch:write still jumps queued work ahead.
func (s *Server) externalApprovalRail(ctx context.Context, principal string, set APIGrantSet, repo, target, reason string, onlyIfUnapproved bool) error {
	id := model.NormalizeTaskID(target)
	task, err := s.tasks.Store.GetTask(ctx, id)
	if err != nil {
		return fmt.Errorf("reading task %s: %w", id, err)
	}
	if task == nil || (onlyIfUnapproved && task.Approval != nil) {
		return nil
	}
	if onlyIfUnapproved && !set.Allows("tasks:admin", repo) {
		return fmt.Errorf("%s cannot run task %s now: it is not approved, and run-now would approve it first, which "+
			"an external agent credential may only do under tasks:admin -- approve it under tasks:admin, or a person "+
			"approves it (docs/agent-api.md)", principal, task.ID)
	}
	if onlyIfUnapproved && reason == "" {
		return fmt.Errorf("%s cannot run task %s now without a reason: it is not approved, and run-now would approve it "+
			"first, which is destructive (scope tasks:admin) as POST /api/tasks/{id}/approve is: give a reason "+
			"(X-Grain-Acting-Reason, grain_invoke's reason, grain run-now -reason)", principal, task.ID)
	}
	return s.tasks.externalApprovalRefusal(ctx, principal, *task)
}

// externalApprovalRefusal is the half of externalApprovalRail that reads
// only the task: its lineage and its person holds. Client.Approve and
// Client.RunNow ask it again of an external caller, so the rail holds
// whichever route reaches them (REST, POST /mcp, POST /api/mcp's
// grain_invoke), and the two routes give the same answer.
func (c *Client) externalApprovalRefusal(ctx context.Context, principal string, task model.Task) error {
	filed, chain, proposer, err := c.externalProposalLineage(ctx, task, principal)
	if err != nil {
		return err
	}
	if filed {
		how := "it filed it"
		if len(chain) > 0 {
			how = "it filed task " + chain[0] + ", which is in the chain that proposed it (" + strings.Join(chain, " → ") + ")"
		}
		return forbiddenErrorf("%s cannot approve task %s: %s, and an external agent credential never approves its own "+
			"filings, directly or through the tasks they proposed, even under api:tasks:admin or dispatch:write -- a "+
			"person approves them (docs/agent-api.md)", principal, task.ID, how)
	}
	holds, err := c.proposalPersonHolds(ctx, task, proposer, nil)
	if err != nil {
		return err
	}
	if len(holds) > 0 {
		return forbiddenErrorf("%s cannot approve task %s: it is held for a person (%s), and an external agent "+
			"credential, even one a person owns, never approves a task held for a person, even under api:tasks:admin "+
			"or dispatch:write (docs/agent-api.md)", principal, task.ID, strings.Join(holds, "; "))
	}
	return nil
}

// externalProposalLineage walks task's proposal chain upwards, as
// proposalLineage does, looking for a task the external credential
// principal filed. filed is true when principal filed task itself or any
// task up its chain; chain is then the in-grain chain from principal's
// filing down to task's proposer, nearest last (empty when principal
// filed task itself). proposer is task's own in-grain proposer, nil when
// it has none.
func (c *Client) externalProposalLineage(ctx context.Context, task model.Task, principal string) (bool, []string, *model.Task, error) {
	var proposer *model.Task
	var chain []string
	seen := map[string]bool{task.ID: true}
	cur := task
	for range maxProposalLineageDepth + 1 {
		if filedByExternal(cur, principal) {
			slices.Reverse(chain)
			return true, chain, proposer, nil
		}
		up := proposedByOf(cur)
		if up == "" || seen[up] {
			return false, nil, proposer, nil
		}
		seen[up] = true
		next, err := c.Store.GetTask(ctx, up)
		if err != nil {
			return false, nil, nil, fmt.Errorf("reading task %s, which proposed %s: %w", up, cur.ID, err)
		}
		if next == nil {
			return false, nil, proposer, nil
		}
		if proposer == nil {
			proposer = next
		}
		chain = append(chain, up)
		cur = *next
	}
	return false, nil, nil, fmt.Errorf("task %s's LinkProposedBy chain exceeds %d hops, so its proposal lineage cannot be verified",
		task.ID, maxProposalLineageDepth)
}

// filedByExternal says whether the external agent credential principal
// ("external:<name>") filed t: t's origin names it as the actor, whoever
// it acted on behalf of.
func filedByExternal(t model.Task, principal string) bool {
	a := t.Origin.Attribution.Actor
	return principal != "" && a.Kind == model.PrincipalAgent && strings.TrimSpace(a.ID) == principal
}

// proposalLineage walks task's LinkProposedBy chain upwards. It returns
// task's own proposer (nil when it has none, or it is no longer stored)
// and, when actor is somewhere on that chain, the chain from actor down
// to task's proposer, nearest last -- empty when actor proposed none of
// it.
func (c *Client) proposalLineage(ctx context.Context, task model.Task, actor string) (*model.Task, []string, error) {
	var proposer *model.Task
	var chain []string
	seen := map[string]bool{task.ID: true}
	cur := task
	for range maxProposalLineageDepth {
		up := proposedByOf(cur)
		if up == "" || seen[up] {
			return proposer, nil, nil
		}
		seen[up] = true
		chain = append(chain, up)
		if up == actor {
			slices.Reverse(chain)
			return proposer, chain, nil
		}
		next, err := c.Store.GetTask(ctx, up)
		if err != nil {
			return nil, nil, fmt.Errorf("reading task %s, which proposed %s: %w", up, cur.ID, err)
		}
		if next == nil {
			return proposer, nil, nil
		}
		if proposer == nil {
			proposer = next
		}
		cur = *next
	}
	if up := proposedByOf(cur); up != "" && !seen[up] {
		return nil, nil, fmt.Errorf("task %s's LinkProposedBy chain exceeds %d hops, so its proposal lineage cannot be verified",
			task.ID, maxProposalLineageDepth)
	}
	return proposer, nil, nil
}

// proposedByOf is the in-grain task that proposed or filed t: its
// LinkProposedBy target, or the acting task recorded on t.Origin when an
// agent filed t directly over POST /api/tasks rather than propose_task.
// "" for a task filed by a person, automation or an external credential.
func proposedByOf(t model.Task) string {
	for _, l := range t.Links {
		if l.Kind == model.LinkProposedBy {
			return model.NormalizeTaskID(l.Target)
		}
	}
	if t.Origin.Attribution.OnBehalfOf != nil {
		if id := inGrainAgentTaskID(*t.Origin.Attribution.OnBehalfOf); id != "" {
			return id
		}
	}
	return inGrainAgentTaskID(t.Origin.Attribution.Actor)
}

func inGrainAgentTaskID(p model.Principal) string {
	id := strings.TrimSpace(p.ID)
	if p.Kind != model.PrincipalAgent || p.Source == model.SourceExternalToken ||
		id == "" || strings.HasPrefix(id, "external:") {
		return ""
	}
	return model.NormalizeTaskID(id)
}

// proposalPersonHolds is every reason task waits for a person rather than
// for any automated approver, each phrased to sit in a list after "held
// for a person": none means it waits for nobody in particular. The
// reasons are the ones filing a proposal or approving via RunNow holds it
// for (ui.CreateTask, orchestrator.relayProposedTasks, Client.agentMayApprove),
// asked again of the task as it stands now:
//
//   - hold_for_approval (model.HoldRequested: HoldRequestedTag, or
//     HeldForApprovalTag on a proposal filed before it was recorded);
//   - targeting or writing this deployment's configuration repository
//     (model.ConfigRepoProposalHold);
//   - targeting a change target that requires a person's approval
//     (model.ChangeTargetNeedsPersonApproval);
//   - a grant settings/capabilities.yml marks requires_person, for the
//     capability or the method it names (catalogueRequiresPerson);
//   - a person-only grant (model.IsPersonOnlyGrant): a dangerous api
//     scope, a capability method only a person may grant, durable or
//     priority-dispatch;
//   - carrying a repository-declared push set (declaredPushOf) or
//     auto-merge that acting could not have inherited itself;
//   - keep_running or admission above standard on any task, and against
//     proposer when there is one:
//     capabilities, write targets or reads it does not hold
//     (model.EscalatedCapabilities, UngrantedWrites, UngrantedReads), and
//     keep_running or a priority above its own
//     (model.PersonOnlyProposalSettings).
func (c *Client) proposalPersonHolds(ctx context.Context, task model.Task, proposer, acting *model.Task) ([]string, error) {
	var holds []string
	if model.HoldRequested(task) {
		holds = append(holds, "it was filed with hold_for_approval")
	}
	if why := model.ConfigRepoProposalHold(task); why != "" {
		holds = append(holds, why)
	}
	if needs, why := model.ChangeTargetNeedsPersonApproval(task.ChangeTarget); needs {
		holds = append(holds, why)
	}
	needsPerson, err := c.catalogueRequiresPerson(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("reading the capability catalogue: %w", err)
	}
	if len(needsPerson) > 0 {
		holds = append(holds, fmt.Sprintf("it holds %s, which settings/capabilities.yml marks requires_person",
			strings.Join(needsPerson, ", ")))
	}
	var personOnly []string
	for _, g := range task.Grants {
		if model.IsPersonOnlyGrant(g.Capability) && !slices.Contains(personOnly, g.Capability) {
			personOnly = append(personOnly, g.Capability)
		}
	}
	if len(personOnly) > 0 {
		holds = append(holds, fmt.Sprintf("it holds %s, which only a person may grant", strings.Join(personOnly, ", ")))
	}
	if task.TemplateID != "" {
		decl, err := c.Store.GetRepoDeclaration(ctx, task.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("reading declaration %s: %w", task.TemplateID, err)
		}
		if push := declaredPushOf(decl); push != nil {
			holds = append(holds, fmt.Sprintf("it carries a declared push set from %s", push.Path))
		}
	}
	if task.AutoMerge && (acting == nil || !(acting.AutoMerge && model.LandsWhereGranted(task, *acting))) {
		holds = append(holds, "it has auto-merge enabled, which would land its change without review")
	}
	if proposer == nil {
		// Admission above standard is asked here too, not only of a
		// proposal: it replaced the priority-dispatch and durable grants,
		// which the person-only check above held whoever filed the task,
		// so a held task an external credential or a person filed with
		// admission bypass or durable still waits for a person.
		if why := model.PersonOnlyProposalSettings(model.Task{KeepRunning: task.KeepRunning, Admission: task.Admission}, model.Task{}); why != "" {
			holds = append(holds, why)
		}
		return holds, nil
	}
	if escalated := model.EscalatedCapabilities(task, *proposer); len(escalated) > 0 {
		holds = append(holds, fmt.Sprintf("it asks for %s, which task %s that proposed it does not hold",
			strings.Join(escalated, ", "), proposer.ID))
	}
	if writes := model.UngrantedWrites(task, *proposer); len(writes) > 0 {
		names := make([]string, 0, len(writes))
		for _, w := range writes {
			names = append(names, w.Repo.String())
		}
		holds = append(holds, fmt.Sprintf("it may land work in %s, which task %s that proposed it was never given",
			strings.Join(names, ", "), proposer.ID))
	}
	if reads := model.UngrantedReads(task, *proposer); len(reads) > 0 {
		names := make([]string, 0, len(reads))
		for _, r := range reads {
			names = append(names, r.String())
		}
		holds = append(holds, fmt.Sprintf("it reads %s, which task %s that proposed it cannot read",
			strings.Join(names, ", "), proposer.ID))
	}
	if why := model.PersonOnlyProposalSettings(task, *proposer); why != "" {
		holds = append(holds, why)
	}
	return holds, nil
}

// agentGrantEscalation refuses a body that writes any capability the
// acting task does not itself hold, or any dangerous api grant at all:
// an agent can never use the API to raise its own or another task's
// grants, and dangerous scopes are a person's to give. Which names a
// body grants is grantsInBody's to say; normalize, when not nil, puts a
// capability reference in the form the store records it in (the
// Client's normalizeCapabilityID) before it is compared.
func agentGrantEscalation(held []string, pattern string, body []byte, normalize func(string) (string, error)) error {
	return agentGrantEscalationFrom(held, pattern, body, nil, normalize)
}

// agentGrantEscalationFrom is agentGrantEscalation subtracting the grants
// already stored on each whole-list replacement field (storedFieldGrants),
// so an agent can narrow an existing capability list without holding every
// other capability already on that row.
func agentGrantEscalationFrom(held []string, pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) error {
	for _, name := range addedGrantsInBody(pattern, body, stored, normalize) {
		if err := agentGrantRefusal(held, name, normalize); err != nil {
			return err
		}
	}
	return nil
}

// storedFieldGrants returns the capability lists already stored on the row
// pattern replaces, keyed by lowercase JSON field name ("cicapabilities",
// "defaultcapabilities"). On whole-list replacement routes an agent is only
// escalating when it adds a capability that is not already on that field;
// keeping or narrowing the stored list grants nothing new.
func (s *Server) storedFieldGrants(ctx context.Context, pattern, repo string) map[string][]string {
	if s.tasks == nil || s.tasks.Store == nil {
		return nil
	}
	switch pattern {
	case "PUT /api/repos/{owner}/{name}/ci-capabilities":
		rc := s.storedRepoConfig(ctx, repo)
		if rc == nil {
			return nil
		}
		return map[string][]string{"cicapabilities": rc.CICapabilities}
	case "PUT /api/repos/{owner}/{name}/capabilities":
		rc := s.storedRepoConfig(ctx, repo)
		if rc == nil {
			return nil
		}
		return map[string][]string{"defaultcapabilities": rc.DefaultCapabilities}
	case "PUT /api/repos/{owner}/{name}/settings",
		"PUT /api/deployment/repos/{owner}/{name}/config":
		rc := s.storedRepoConfig(ctx, repo)
		if rc == nil {
			return nil
		}
		return map[string][]string{
			"cicapabilities":      rc.CICapabilities,
			"defaultcapabilities": rc.DefaultCapabilities,
		}
	case "PUT /api/settings", "POST /api/settings", "POST /api/settings/plan":
		cfg, err := s.tasks.Store.GetConfig(ctx)
		if err != nil || cfg == nil {
			return nil
		}
		return map[string][]string{"defaultcapabilities": cfg.DefaultCapabilities}
	default:
		return nil
	}
}

func (s *Server) storedRepoConfig(ctx context.Context, repo string) *model.RepoConfig {
	if repo == "" {
		return nil
	}
	parsed, err := s.tasks.parseRepo(repo)
	if err != nil {
		return nil
	}
	rc, err := s.tasks.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return nil
	}
	return rc
}

// agentGrantRefusal applies agentGrantEscalation's two rules to one
// grant name, compared as grants rather than as strings: an api grant
// by the scopes and repository it reads as (so api:tasks:read@acme/x is
// held by a task holding api:tasks:read, whatever the repository's
// case), a capability reference by whether a held one covers it
// (model.UncoveredCapabilityRefs), and anything else by its name.
func agentGrantRefusal(held []string, name string, normalize func(string) (string, error)) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if _, ok := model.LegacyHostingAlias(name); ok {
		return nil
	}
	if expanded := model.CanonicalLegacyGrants(name); len(expanded) > 1 {
		for _, part := range expanded {
			if err := agentGrantRefusal(held, part, normalize); err != nil {
				return err
			}
		}
		return nil
	}
	if canonical := model.CanonicalLegacyGrant(name); canonical != name && model.IsCapabilityRef(canonical) {
		name = canonical
	}
	// APIGrantsOf reads an alias (self-upgrade) as the preset it now is,
	// which ParseAPIGrant alone does not.
	if slices.ContainsFunc(APIGrantsOf([]string{name}).Scopes(), IsDangerousAPIScope) || model.IsDangerousAPIGrant(name) {
		return fmt.Errorf("%s is a dangerous api grant: only a person may grant it", name)
	}
	if model.IsDangerousGrant(name) {
		return fmt.Errorf("%s is a dangerous grant: only a person may grant it", name)
	}
	// priority-dispatch lets a task jump the queue, so it is a person's
	// to give even to an agent whose own task holds it.
	if name == model.PriorityDispatchCapability {
		return fmt.Errorf("%s lets a task jump the queue: only a person may grant it", name)
	}
	// durable implies priority-dispatch, and holds a sandbox for as long
	// as the work lasts.
	if name == model.DurableCapability {
		return fmt.Errorf("%s lets a task jump the queue and run with no deadline: only a person may grant it", name)
	}
	notHeld := fmt.Errorf("an agent cannot grant %s, which its own task does not hold", name)
	if g, ok, err := ParseAPIGrant(name); ok {
		if err != nil {
			return notHeld
		}
		// A scope set limited to some repositories grants each scope on
		// those alone, so that is what the agent must hold: read through
		// ReposOf, not g.Repo, or a repo-limited set could never be passed
		// on by the very task holding it.
		set := APIGrantsOf(held)
		for _, scope := range g.Scopes {
			for _, repo := range g.ReposOf(scope) {
				if !set.Allows(scope, repo) {
					return notHeld
				}
			}
		}
		return nil
	}
	if model.IsCapabilityRef(name) {
		if normalize != nil {
			if recorded, err := normalize(name); err == nil {
				name = recorded
			}
		}
		parent := make([]model.Grant, len(held))
		for i, h := range held {
			parent[i] = model.Grant{Capability: h}
		}
		if len(model.UncoveredCapabilityRefs([]model.Grant{{Capability: name}}, parent)) > 0 {
			return notHeld
		}
		return nil
	}
	if !slices.Contains(held, name) {
		return notHeld
	}
	return nil
}

// grantBodies are the routes whose bodies name a grant under keys of
// their own, read structurally: every name the handler could record (or
// take away) is returned, however the body spells it.
var grantBodies = map[string]func(body []byte) []string{
	"POST /api/tasks/{id}/capabilities": func(body []byte) []string {
		var req setCapabilityRequest
		if json.Unmarshal(body, &req) != nil {
			return nil // the handler refuses a body it cannot read
		}
		if req.Attach != nil && !*req.Attach {
			if strings.TrimSpace(req.Replace) == "" {
				return nil
			}
			return []string{req.Replace}
		}
		names := []string{req.ID, req.Grant, req.Replace}
		if req.Capability != "" {
			grant, err := model.EncodeCapabilityGrant(protocol.GrantSpec{Capability: strings.TrimSpace(req.Capability), With: req.With})
			if err != nil {
				grant = req.Capability
			}
			names = append(names, grant)
		}
		return names
	},
	// A scope set is granted by name and resolved when used, so writing
	// one hands what it resolves to every task holding it (or a set that
	// includes it): an agent may write only a set whose every scope it
	// holds, none of them dangerous. One that does not resolve against
	// the sets in effect is refused rather than read as granting nothing.
	"PUT /api/api-scope-sets": func(body []byte) []string {
		var req PutAPIScopeSetRequest
		if json.Unmarshal(body, &req) != nil {
			return nil // the handler refuses a body it cannot read
		}
		req.Set.Name = strings.TrimSpace(req.Set.Name)
		names, err := model.APIScopeSetWriteGrantNames(req.Set)
		if err != nil {
			return []string{APIGrantPrefix + req.Set.Name + " (" + err.Error() + ")"}
		}
		return names
	},
	"POST /api/external-agents/tokens": externalTokenGrantsInBody,
}

func externalTokenGrantsInBody(body []byte) []string {
	var req CreateExternalAgentTokenRequest
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	grants, _, _, err := model.ResolveExternalAgentGrants(req.ScopeSet, req.Scopes, req.Repos)
	if err != nil {
		return []string{APIGrantPrefix + strings.TrimSpace(req.ScopeSet) + " (" + err.Error() + ")"}
	}
	return grants
}

// grantsInBody is every grant name a request body for pattern writes.
// A route in grantBodies is read by its own request type; any other
// body by key: a key naming capabilities, or "grants", holding a name,
// a list of them (each a name or a {capability, with} object, as
// model.CapabilityList reads one), or a map of such lists by repository.
func grantsInBody(pattern string, body []byte) []string {
	return addedGrantsInBody(pattern, body, nil, nil)
}

// gateKey is a JSON object key folded the way encoding/json matches one
// against a struct field: case-insensitively, under Unicode simple
// folding, so U+017F (ſ) is an s and U+212A (the Kelvin sign) a k. The
// gates below compare body keys by gateKey, never by strings.ToLower:
// ToLower leaves ſ as it is, so "autoApproveProposalſ" or
// "priorityDiſpatch" reached the field it names while missing the case
// that refuses it.
func gateKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		if r >= utf8.RuneSelf {
			// The smallest rune in r's folding orbit, as
			// encoding/json's foldRune picks it.
			for {
				next := unicode.SimpleFold(r)
				if next <= r {
					r = next
					break
				}
				r = next
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

type jsonField struct {
	key string
	raw json.RawMessage
}

// orderedJSONFields reads a top-level JSON object's fields in wire order,
// keeping duplicate keys rather than letting a second key overwrite the
// first in a map, and so reporting the first refused key deterministically.
func orderedJSONFields(body []byte) ([]jsonField, bool) {
	if len(body) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []jsonField
	for dec.More() {
		ktok, err := dec.Token()
		key, ok := ktok.(string)
		if err != nil || !ok {
			return nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		out = append(out, jsonField{key: key, raw: raw})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return out, true
}

func fieldTouchesTaskGrants(key string, raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	lower := gateKey(key)
	// "priority" is the numeric dispatch priority unless it is a
	// boolean, which UpdateTaskRequest reads as priorityDispatch:
	// the priority-dispatch grant, under either spelling.
	if lower == "priority" {
		var on bool
		return json.Unmarshal(raw, &on) == nil
	}
	return strings.Contains(lower, "capabilit") || lower == "grants" ||
		lower == "admission" ||
		lower == "keeprunning" || lower == "keep_running" ||
		lower == "prioritydispatch" || lower == "priority_dispatch"
}

func firstTaskGrantKeyInPatch(body []byte) string {
	fields, ok := orderedJSONFields(body)
	if !ok {
		return ""
	}
	for _, f := range fields {
		if fieldTouchesTaskGrants(f.key, f.raw) {
			return f.key
		}
	}
	return ""
}

func patchTouchesTaskGrants(body []byte) bool {
	return firstTaskGrantKeyInPatch(body) != ""
}

type grantField struct {
	key  string
	name string
}

func grantNamesFromField(pattern string, f jsonField, stored map[string][]string, normalize func(string) (string, error)) []string {
	lower := gateKey(f.key)
	if lower == "admission" || lower == "defaultadmission" || lower == "default_admission" {
		// Raising admission above standard is what the retired
		// priority-dispatch (bypass) and durable grants gave, and is gated
		// under their names; on a task PATCH any change at all is.
		trimmed := strings.TrimSpace(string(f.raw))
		if trimmed == "" || trimmed == "null" {
			return nil
		}
		var v string
		if json.Unmarshal(f.raw, &v) != nil {
			return []string{model.PriorityDispatchCapability}
		}
		adm, err := model.ParseOptionalAdmission(v)
		switch {
		case err != nil:
			return []string{model.PriorityDispatchCapability}
		case adm == model.AdmissionDurable:
			return []string{model.DurableCapability}
		case adm.AtLeast(model.AdmissionBypass) || pattern == "PATCH /api/tasks/{id}":
			return []string{model.PriorityDispatchCapability}
		}
		return nil
	}
	if lower == "priority" || lower == "prioritydispatch" || lower == "priority_dispatch" {
		var on bool
		if json.Unmarshal(f.raw, &on) == nil && (on || pattern == "PATCH /api/tasks/{id}") {
			return []string{model.PriorityDispatchCapability}
		}
		return nil
	}
	if lower == "keeprunning" || lower == "keep_running" {
		var on bool
		if json.Unmarshal(f.raw, &on) == nil && (on || pattern == "PATCH /api/tasks/{id}") {
			return []string{model.DurableCapability}
		}
		return nil
	}
	if !strings.Contains(lower, "capabilit") && !strings.EqualFold(f.key, "grants") {
		return nil
	}
	existing := stored[lower]
	var names []string
	for _, name := range grantNamesIn(f.raw) {
		if grantAlreadyStored(existing, name, normalize) {
			continue
		}
		names = append(names, name)
	}
	return names
}

func firstMatchingJSONKey(fields []jsonField, candidates ...string) string {
	for _, f := range fields {
		trimmed := strings.TrimSpace(string(f.raw))
		if trimmed == "" || trimmed == "null" || trimmed == `""` {
			continue
		}
		lower := gateKey(f.key)
		for _, want := range candidates {
			if lower == gateKey(want) {
				return f.key
			}
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "capabilities"
}

func addedGrantFieldsInBody(pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) []grantField {
	if len(body) == 0 {
		return nil
	}
	if read, ok := grantBodies[pattern]; ok {
		names := read(body)
		if len(names) == 0 {
			return nil
		}
		fields, _ := orderedJSONFields(body)
		key := "capabilities"
		switch pattern {
		case "POST /api/tasks/{id}/capabilities":
			key = firstMatchingJSONKey(fields, "id", "grant", "capability", "replace")
		case "PUT /api/api-scope-sets":
			key = firstMatchingJSONKey(fields, "set", "scopes")
		case "POST /api/external-agents/tokens":
			key = firstMatchingJSONKey(fields, "scopeSet", "scopes")
		}
		out := make([]grantField, 0, len(names))
		for _, name := range names {
			if strings.TrimSpace(name) != "" {
				out = append(out, grantField{key: key, name: name})
			}
		}
		return out
	}
	fields, ok := orderedJSONFields(body)
	if !ok {
		return nil
	}
	var out []grantField
	for _, f := range fields {
		for _, name := range grantNamesFromField(pattern, f, stored, normalize) {
			out = append(out, grantField{key: f.key, name: name})
		}
	}
	return out
}

func addedGrantsInBody(pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) []string {
	gfs := addedGrantFieldsInBody(pattern, body, stored, normalize)
	if len(gfs) == 0 {
		return nil
	}
	names := make([]string, len(gfs))
	for i, gf := range gfs {
		names[i] = gf.name
	}
	return names
}

// grantNamesPersonScopeSet reports whether name is an api grant of a scope
// set marked Person (directly or through Includes).
func grantNamesPersonScopeSet(name string) bool {
	g, ok, err := ParseAPIGrant(name)
	if !ok || err != nil {
		return false
	}
	if g.RequiresPerson {
		return true
	}
	if g.Preset == "" {
		return false
	}
	seen := map[string]bool{}
	var walk func(setName string) bool
	walk = func(setName string) bool {
		if seen[setName] {
			return false
		}
		seen[setName] = true
		set, ok := model.APIScopeSetNamed(setName)
		if !ok {
			return false
		}
		if set.Person {
			return true
		}
		for _, inc := range set.Includes {
			if walk(strings.TrimSpace(inc)) {
				return true
			}
		}
		return false
	}
	return walk(g.Preset)
}

func grantAlreadyStored(existing []string, name string, normalize func(string) (string, error)) bool {
	if len(existing) == 0 {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if slices.Contains(existing, name) {
		return true
	}
	// A retired name is held by a row storing what it became (the
	// release server's methods, schema migration 101, and
	// github#sandbox-repo, 124), and the reverse; so is a bare or
	// case-varied spelling of the <ref>={} a row records.
	if _, ok := model.CIGrantRuleFor(name); ok {
		canonical := model.CanonicalCIGrant(name)
		return slices.ContainsFunc(existing, func(e string) bool { return model.CanonicalCIGrant(e) == canonical })
	}
	if model.IsCapabilityRef(name) && normalize != nil {
		if recorded, err := normalize(name); err == nil && slices.Contains(existing, recorded) {
			return true
		}
	}
	if target, ok := model.RepoReadTarget(name); ok {
		return slices.Contains(existing, model.RepoReadCapability(target))
	}
	if target, ok := model.RepoWriteTarget(name); ok {
		return slices.Contains(existing, model.RepoWriteCapability(target))
	}
	return false
}

func grantNamesIn(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var names []string
		for _, item := range list {
			if entries, err := model.CapabilityEntryNames(item); err == nil {
				names = append(names, entries...)
			} else if spec, err := protocol.ParseGrant(item); err == nil {
				names = append(names, spec.Capability)
			}
		}
		return names
	}
	var byRepo map[string]json.RawMessage
	if json.Unmarshal(raw, &byRepo) == nil {
		var names []string
		for _, v := range byRepo {
			names = append(names, grantNamesIn(v)...)
		}
		return names
	}
	return nil
}

// networkAccessWidens reports whether a body asking for access gives work
// more network than limit, the caller's own (an acting task's
// NetworkAccess). Both are read as dispatch reads them
// (orchestrator.deploymentNetworkAccess): "" is the deployment's default,
// so a task with no override on a deployment whose default is none has
// none, and may not ask for "open" -- nor, on a deployment whose default is
// open, for "". A config that cannot be read is taken as none.
func (s *Server) networkAccessWidens(ctx context.Context, limit model.NetworkAccess, access string) bool {
	deployment := model.NetworkNone
	if s.tasks != nil && s.tasks.Store != nil {
		if cfg, err := s.tasks.Store.GetConfig(ctx); err == nil && cfg != nil {
			deployment = model.NormalizeNetworkAccess(cfg.NetworkAccess)
		}
	}
	effective := func(v model.NetworkAccess) model.NetworkAccess {
		if v = model.NetworkAccess(strings.TrimSpace(string(v))); v == "" {
			return deployment
		}
		return v
	}
	return effective(limit) == model.NetworkNone && effective(model.NetworkAccess(access)) != model.NetworkNone
}

// filingEscalationOpts distinguishes the three callers sharing
// filingEscalation: an in-grain task's agent (acting != nil), an external
// agent credential (external != nil), or a verified person with a limited
// access role (role != nil).
type filingEscalationOpts struct {
	acting    *model.Task
	external  *model.ExternalAgentToken
	role      *accessRole
	stored    map[string][]string
	normalize func(string) (string, error)
}

func (s *Server) gateTemplate(ctx context.Context, id string) *model.Template {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if model.IsSystemTemplateID(id) {
		if tmpl := model.FileableSystemTemplate(id); tmpl != nil {
			return tmpl
		}
		return model.SystemTemplate(id, model.RepoRef{})
	}
	if s.tasks == nil || s.tasks.Store == nil {
		return nil
	}
	tmpl, err := s.tasks.Store.GetTemplate(ctx, id)
	if err != nil {
		return nil
	}
	return tmpl
}

func (s *Server) normalizeCapFn() func(string) (string, error) {
	if s.tasks == nil {
		return nil
	}
	return s.tasks.normalizeCapabilityID
}

// agentFilingEscalation refuses the rest of what a task, schedule or
// template body can hand out beyond its capabilities (agentGrantEscalation):
// push or read access to a repository, auto-merge (and Submit, which
// turns it on), approval, a requester, a template's grants, or a move of
// the work to a repository the scope does not reach. (A hostd is no
// longer the filer's to name: a body that sets one is refused by the
// handler, TaskHostdRefusal.) Each is what propose_task already withholds from a run
// (model.EscalatedCapabilities, the auto-merge and approval rules), and the
// API is not a way around it. encoding/json matches keys without regard to
// case, and so does this.
func (s *Server) agentFilingEscalation(ctx context.Context, acting *model.Task, set APIGrantSet,
	pattern string, scope APIScope, repo string, body []byte) error {
	var held []string
	if acting != nil {
		held = grantNames(acting.Grants)
	}
	allowedRepo := func(need APIScope, r string) bool {
		if need == "tasks:write" {
			return agentMayWrite(acting, r)
		}
		return agentMayRead(acting, r)
	}
	return s.filingEscalation(ctx, held, set, allowedRepo, pattern, scope, repo, body, filingEscalationOpts{acting: acting})
}

// filingEscalation is the shared body check across agentFilingEscalation,
// externalAgentFilingEscalation and authorizeRoleCall: every task, template,
// schedule, suite or session key that widens authority beyond held and set.
func (s *Server) filingEscalation(ctx context.Context, held []string, set APIGrantSet,
	allowedRepo func(APIScope, string) bool, pattern string, scope APIScope, repo string,
	body []byte, opts filingEscalationOpts) error {
	if opts.role == nil {
		if pattern == "POST /api/tasks/{id}/submit" {
			return errAgentAutoMerge
		}
		if personOnlyChangeReview[pattern] {
			return errAgentChangeReview
		}
	}
	fields, ok := orderedJSONFields(body)
	if !ok && len(body) > 0 {
		fields = nil
	}
	for _, f := range fields {
		key, raw := f.key, f.raw
		if opts.role != nil {
			if pattern == "PATCH /api/tasks/{id}" && fieldTouchesTaskGrants(key, raw) && !set.Allows("capabilities:admin", repo) {
				return fmt.Errorf("%s cannot set %s: %s with capabilities needs scope capabilities:admin",
					opts.role.label(), key, pattern)
			}
			for _, name := range grantNamesFromField(pattern, f, opts.stored, opts.normalize) {
				if err := roleGrantRefusal(opts.role, key, name, opts.normalize); err != nil {
					return err
				}
			}
		}
		switch gateKey(key) {
		case "writes":
			var targets []WriteTarget
			if json.Unmarshal(raw, &targets) != nil {
				continue // the handler refuses a body it cannot read
			}
			for _, t := range targets {
				if !allowedRepo("tasks:write", t.Repo) {
					switch {
					case opts.role != nil:
						return fmt.Errorf("%s cannot set %s: role does not hold tasks:write on %s", opts.role.label(), key, t.Repo)
					case opts.external != nil:
						return fmt.Errorf("external agent %q cannot grant push access to %s, which its credential does not cover", opts.external.Name, t.Repo)
					default:
						return fmt.Errorf("an agent cannot grant push access to %s, which its own task cannot push to", t.Repo)
					}
				}
			}
		case "reads":
			var repos []string
			if json.Unmarshal(raw, &repos) != nil {
				continue
			}
			for _, r := range repos {
				if !allowedRepo("tasks:read", r) {
					switch {
					case opts.role != nil:
						return fmt.Errorf("%s cannot set %s: role does not hold tasks:read on %s", opts.role.label(), key, r)
					case opts.external != nil:
						return fmt.Errorf("external agent %q cannot grant read access to %s, which its credential does not cover", opts.external.Name, r)
					default:
						return fmt.Errorf("an agent cannot grant read access to %s, which its own task cannot read", r)
					}
				}
			}
		case "designs", "context":
			for _, r := range contextReposInBody(gateKey(key), raw) {
				if !allowedRepo("tasks:read", r) {
					switch {
					case opts.role != nil:
						return fmt.Errorf("%s cannot set %s: role does not hold tasks:read on %s", opts.role.label(), key, r)
					case opts.external != nil:
						return fmt.Errorf("external agent %q cannot reference committed context in %s, which its credential does not cover", opts.external.Name, r)
					default:
						return fmt.Errorf("an agent cannot reference committed context in %s, which its own task cannot read", r)
					}
				}
			}
		case "readsall", "reads_all":
			// A console session's opt-in to reading every repository
			// this deployment works in (model.ConsoleSessionAutoReads):
			// the same grant as naming each of them in reads.
			var on bool
			if json.Unmarshal(raw, &on) != nil || !on {
				continue
			}
			switch {
			case opts.role != nil:
				if !set.Allows("tasks:read", "") {
					return fmt.Errorf("%s cannot set %s: reading every repository requires tasks:read without repository limits", opts.role.label(), key)
				}
			case opts.external != nil:
				if !set.Allows(scope, "") {
					return fmt.Errorf("external agent %q cannot turn on readsAll with repository limits", opts.external.Name)
				}
			default:
				if !s.agentMayReadAllRepos(ctx, opts.acting) {
					return errors.New("an agent cannot turn on readsAll, which its own task does not have")
				}
			}
		case "automerge":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				if opts.role != nil {
					return fmt.Errorf("%s cannot set %s: turning on auto-merge requires an unlimited role (grain/operator)", opts.role.label(), key)
				}
				return errAgentAutoMerge
			}
		case "autoapproveproposals", "auto_approve_proposals", "tasksapproved":
			var on bool
			if json.Unmarshal(raw, &on) != nil || !on {
				continue
			}
			switch {
			case opts.role != nil:
				if !set.Allows("tasks:admin", repo) {
					return fmt.Errorf("%s cannot set %s: approving proposals needs tasks:admin", opts.role.label(), key)
				}
			case opts.external != nil:
				return errors.New("an external agent cannot turn on autoApproveProposals")
			default:
				if !s.agentMayAutoApproveProposals(ctx, opts.acting) {
					return errors.New("an agent cannot turn on autoApproveProposals, which its own task does not have")
				}
			}
		case "networkaccess", "network_access":
			var access string
			if json.Unmarshal(raw, &access) != nil {
				continue
			}
			switch {
			case opts.role != nil:
				if s.networkAccessWidens(ctx, "", access) {
					return fmt.Errorf("%s cannot set %s: widening networkAccess past this deployment's default requires an unlimited role (grain/operator)", opts.role.label(), key)
				}
			case opts.external != nil:
				if s.networkAccessWidens(ctx, "", access) {
					return errors.New("an external agent cannot widen networkAccess past this deployment's default of \"none\"")
				}
			default:
				if s.networkAccessWidens(ctx, opts.acting.NetworkAccess, access) {
					return errors.New("an agent with networkAccess \"none\" cannot widen networkAccess")
				}
			}
		case "keeprunning", "keep_running":
			var on bool
			if json.Unmarshal(raw, &on) != nil || !on {
				continue
			}
			switch {
			case opts.role != nil:
				return fmt.Errorf("%s cannot set %s: keepRunning requires an unlimited role (grain/operator)", opts.role.label(), key)
			case opts.external != nil:
				return errors.New("an external agent cannot turn on keepRunning")
			}
		case "priority":
			var on bool
			if json.Unmarshal(raw, &on) == nil {
				continue
			}
			if opts.acting != nil {
				continue
			}
			if p, err := decodeTaskPriority(raw); err == nil && p > model.DefaultTaskPriority && !set.Allows("tasks:admin", repo) {
				if opts.role != nil {
					return fmt.Errorf("%s cannot set %s above %d without tasks:admin", opts.role.label(), key, model.DefaultTaskPriority)
				}
				return fmt.Errorf("an external agent cannot set priority above %d without tasks:admin", model.DefaultTaskPriority)
			}
		case "fixesbasebranch", "fixes_base_branch":
			if opts.acting != nil {
				continue
			}
			var on bool
			if json.Unmarshal(raw, &on) == nil && on && !set.Allows("tasks:admin", repo) {
				if opts.role != nil {
					return fmt.Errorf("%s cannot set %s without tasks:admin", opts.role.label(), key)
				}
				return errors.New("an external agent cannot mark a task fixesBaseBranch without tasks:admin")
			}
		case "approved", "interactive":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				if opts.acting != nil && gateKey(key) == "approved" {
					return errors.New("an agent files work unapproved and never approves its own proposals, even under api:tasks:admin (docs/agent-api.md)")
				}
				if !set.Allows("tasks:admin", repo) {
					if opts.role != nil {
						return fmt.Errorf("%s cannot set %s: approving work needs tasks:admin", opts.role.label(), key)
					}
					return errors.New("an agent files work unapproved: approving it needs tasks:admin")
				}
			}
		case "owner":
			if opts.role == nil && hasTaskOwnerField(pattern, raw) {
				return errAgentTaskOwner
			}
		case "author", "creator", "proposedby":
			lower := gateKey(key)
			if lower == "creator" && opts.acting != nil {
				continue
			}
			var who string
			if json.Unmarshal(raw, &who) != nil || strings.TrimSpace(who) == "" {
				continue
			}
			if opts.role != nil {
				if lower == "proposedby" {
					return fmt.Errorf("%s cannot set %s: proposedBy is daemon-internal", opts.role.label(), key)
				}
				if !strings.EqualFold(strings.TrimSpace(who), opts.role.Who) {
					return fmt.Errorf("%s cannot set %s to %q: a limited role files work only as themselves", opts.role.label(), key, strings.TrimSpace(who))
				}
				continue
			}
			return fmt.Errorf("an agent cannot name somebody else as the %s of work it files", key)
		case "proposalkey":
			var k string
			if json.Unmarshal(raw, &k) == nil && strings.TrimSpace(k) != "" {
				if opts.role != nil {
					return fmt.Errorf("%s cannot set %s directly", opts.role.label(), key)
				}
				return errors.New("an agent cannot set proposalKey directly")
			}
		case "reviewers":
			if opts.role == nil && hasReviewersField(pattern, raw) {
				return errAgentReviewers
			}
		case "template":
			if pattern != "POST /api/tasks/from-template" {
				continue
			}
			var stem string
			if json.Unmarshal(raw, &stem) != nil || strings.TrimSpace(stem) == "" {
				continue
			}
			parsedRepo, err := model.ParseRepo(strings.TrimSpace(repo))
			if err != nil {
				continue
			}
			fullID, err := FromTemplateID(parsedRepo, stem)
			if err != nil {
				continue
			}
			if tmpl := s.gateTemplate(ctx, fullID); tmpl != nil {
				if err := s.checkFilingTemplate(ctx, *tmpl, key, held, set, allowedRepo, scope, repo, opts); err != nil {
					return err
				}
			} else if err := s.checkDeclaredPushID(ctx, fullID, key, opts); err != nil {
				return err
			}
		case "templateid":
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			tmpl := s.gateTemplate(ctx, id)
			if tmpl == nil {
				if err := s.checkDeclaredPushID(ctx, id, key, opts); err != nil {
					return err
				}
				continue // the handler says it is not there
			}
			if err := s.checkFilingTemplate(ctx, *tmpl, key, held, set, allowedRepo, scope, repo, opts); err != nil {
				return err
			}
		case "templateids":
			var ids []string
			if json.Unmarshal(raw, &ids) != nil {
				continue
			}
			for _, id := range ids {
				if tmpl := s.gateTemplate(ctx, id); tmpl != nil {
					if err := s.checkFilingTemplate(ctx, *tmpl, key, held, set, allowedRepo, scope, repo, opts); err != nil {
						return err
					}
				} else if err := s.checkDeclaredPushID(ctx, id, key, opts); err != nil {
					return err
				}
			}
		case "suiteid":
			if s.tasks == nil || s.tasks.Store == nil {
				continue
			}
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			suite, err := s.tasks.Store.GetSuite(ctx, strings.TrimSpace(id))
			if err != nil {
				return err
			}
			if suite == nil {
				continue
			}
			if opts.role != nil {
				if err := s.checkRoleSuite(ctx, *suite, key, held, set, allowedRepo, scope, repo, opts); err != nil {
					return err
				}
				continue
			}
			for _, it := range suite.Items {
				if tmpl := s.gateTemplate(ctx, it.TemplateID); tmpl != nil {
					if err := s.checkFilingTemplate(ctx, *tmpl, key, held, set, allowedRepo, scope, repo, opts); err != nil {
						return err
					}
				} else if err := s.checkDeclaredPushID(ctx, it.TemplateID, key, opts); err != nil {
					return err
				}
			}
		case "reviewtemplateid":
			// The template a task's review is filed from once its work
			// is done (orchestrator.fileReviewTask): approved,
			// auto-merge, and on the branch this agent's work is on, so
			// it is held to the same grants as templateId -- less
			// auto-merge and target, which a review never takes from its
			// template. The repository's own default review is always
			// allowed: it is what the task gets by naming none.
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			tmpl := s.gateTemplate(ctx, id)
			if tmpl == nil {
				continue // the handler says it is not there
			}
			var target *model.RepoRef
			if r, err := model.ParseRepo(repo); err == nil {
				target = &r
			}
			switch {
			case opts.role != nil:
				if target != nil && tmpl.ID == model.DefaultReviewTemplateID(*target) {
					continue
				}
				if err := roleTemplateWithin(opts.role, key, *tmpl, allowedRepo, opts.normalize); err != nil {
					return err
				}
			case opts.external != nil:
				if target != nil && tmpl.ID == model.DefaultReviewTemplateID(*target) {
					continue
				}
				if err := externalTemplateWithin(opts.external, held, *tmpl, func(r string) bool { return allowedRepo(scope, r) }); err != nil {
					return err
				}
			default:
				if err := model.ReviewTemplateWithinTask(*opts.acting, *tmpl, target); err != nil {
					return err
				}
			}
		case "repo":
			// Moving work to another repository (PATCH /api/tasks/{id}
			// with a new repo) is a call on that repository too.
			var to string
			if json.Unmarshal(raw, &to) != nil || strings.TrimSpace(to) == "" || model.SameRepoText(to, repo) {
				continue
			}
			if !set.Allows(scope, strings.TrimSpace(to)) {
				switch {
				case opts.role != nil:
					return fmt.Errorf("%s cannot set %s: role's %s does not reach %s", opts.role.label(), key, scope, strings.TrimSpace(to))
				case opts.external != nil:
					return fmt.Errorf("the external agent's %s does not reach %s", scope, strings.TrimSpace(to))
				default:
					return fmt.Errorf("the agent's %s does not reach %s", scope, strings.TrimSpace(to))
				}
			}
		}
	}
	if opts.role != nil {
		if pattern == "POST /api/tasks/{id}/submit" {
			return fmt.Errorf("%s cannot submit work (autoMerge): letting a change merge unreviewed requires an unlimited role (grain/operator)", opts.role.label())
		}
		if pattern == "POST /api/suites" && !bodyExplicitlyDisablesAutoMerge(fields) {
			return fmt.Errorf("%s cannot set autoMerge: suites default autoMerge to true, which requires an unlimited role (pass \"autoMerge\": false)", opts.role.label())
		}
	}
	return nil
}

func bodyExplicitlyDisablesAutoMerge(fields []jsonField) bool {
	disabled := false
	for _, f := range fields {
		if gateKey(f.key) != "automerge" {
			continue
		}
		var on *bool
		if json.Unmarshal(f.raw, &on) == nil && on != nil {
			disabled = !*on
		} else {
			disabled = false
		}
	}
	return disabled
}

var errAgentDeclaredPush = errors.New("an agent cannot file, schedule, or modify a template that carries a declared push set")

func (s *Server) checkDeclaredPushID(ctx context.Context, templateID, key string, opts filingEscalationOpts) error {
	id := strings.TrimSpace(templateID)
	push, err := s.templateDeclaredPush(ctx, id)
	if err != nil {
		return err
	}
	if push == nil {
		return nil
	}
	if opts.role != nil {
		return fmt.Errorf("%s cannot set %s: template %s carries a declared push set (%s), which requires an unlimited role (grain/operator)",
			opts.role.label(), key, id, push.Path)
	}
	return fmt.Errorf("template %s carries a declared push set (%s): %w", id, push.Path, errAgentDeclaredPush)
}

func (s *Server) agentStandingDeclaredPush(ctx context.Context, pattern, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || s.tasks == nil || s.tasks.Store == nil {
		return nil
	}
	switch pattern {
	case "PATCH /api/templates/{id}":
		push, err := s.templateDeclaredPush(ctx, id)
		if err != nil {
			return err
		}
		if push != nil {
			return fmt.Errorf("template %s carries a declared push set (%s): %w", id, push.Path, errAgentDeclaredPush)
		}
	case "PATCH /api/schedules/{id}", "POST /api/schedules/{id}/run":
		sched, err := s.tasks.Store.GetSchedule(ctx, id)
		if err != nil {
			return fmt.Errorf("reading schedule %s: %w", id, err)
		}
		if sched == nil {
			return nil
		}
		if sched.TemplateID != nil && strings.TrimSpace(*sched.TemplateID) != "" {
			tid := strings.TrimSpace(*sched.TemplateID)
			push, err := s.templateDeclaredPush(ctx, tid)
			if err != nil {
				return err
			}
			if push != nil {
				return fmt.Errorf("schedule %s fires template %s, which carries a declared push set (%s): %w",
					sched.ID, tid, push.Path, errAgentDeclaredPush)
			}
		}
		if sched.SuiteID != nil && strings.TrimSpace(*sched.SuiteID) != "" {
			suite, err := s.tasks.Store.GetSuite(ctx, strings.TrimSpace(*sched.SuiteID))
			if err != nil {
				return fmt.Errorf("reading suite %s: %w", strings.TrimSpace(*sched.SuiteID), err)
			}
			if suite != nil {
				for _, it := range suite.Items {
					push, err := s.templateDeclaredPush(ctx, it.TemplateID)
					if err != nil {
						return err
					}
					if push != nil {
						return fmt.Errorf("schedule %s runs suite %s containing template %s, which carries a declared push set (%s): %w",
							sched.ID, suite.ID, it.TemplateID, push.Path, errAgentDeclaredPush)
					}
				}
			}
		}
	case "PATCH /api/suites/{id}":
		suite, err := s.tasks.Store.GetSuite(ctx, id)
		if err != nil {
			return fmt.Errorf("reading suite %s: %w", id, err)
		}
		if suite != nil {
			for _, it := range suite.Items {
				push, err := s.templateDeclaredPush(ctx, it.TemplateID)
				if err != nil {
					return err
				}
				if push != nil {
					return fmt.Errorf("suite %s contains template %s, which carries a declared push set (%s): %w",
						suite.ID, it.TemplateID, push.Path, errAgentDeclaredPush)
				}
			}
		}
	}
	return nil
}

func (s *Server) checkFilingTemplate(ctx context.Context, tmpl model.Template, key string, held []string, set APIGrantSet,
	allowedRepo func(APIScope, string) bool, scope APIScope, repo string, opts filingEscalationOpts) error {
	if err := s.checkDeclaredPushID(ctx, tmpl.ID, key, opts); err != nil {
		return err
	}
	switch {
	case opts.role != nil:
		if err := roleTemplateWithin(opts.role, key, tmpl, allowedRepo, opts.normalize); err != nil {
			return err
		}
		if tmpl.AutoMerge {
			return fmt.Errorf("%s cannot set %s: template %s sets autoMerge, which requires an unlimited role (grain/operator)", opts.role.label(), key, tmpl.ID)
		}
		if tmpl.KeepRunning {
			return fmt.Errorf("%s cannot set %s: template %s sets keepRunning, which requires an unlimited role (grain/operator)", opts.role.label(), key, tmpl.ID)
		}
		if tmpl.Interactive && !set.Allows("tasks:admin", repo) {
			return fmt.Errorf("%s cannot set %s: template %s is interactive, which needs tasks:admin", opts.role.label(), key, tmpl.ID)
		}
		if tmpl.Priority != nil && *tmpl.Priority > model.DefaultTaskPriority && !set.Allows("tasks:admin", repo) {
			return fmt.Errorf("%s cannot set %s: template %s sets priority %d (above %d), which needs tasks:admin", opts.role.label(), key, tmpl.ID, *tmpl.Priority, model.DefaultTaskPriority)
		}
		if s.networkAccessWidens(ctx, "", string(tmpl.NetworkAccess)) {
			return fmt.Errorf("%s cannot set %s: template %s widens networkAccess past this deployment's default", opts.role.label(), key, tmpl.ID)
		}
		if tmpl.Target != nil && !set.Allows("tasks:write", tmpl.Target.String()) {
			return fmt.Errorf("%s cannot set %s: template %s targets %s, which role's tasks:write does not reach", opts.role.label(), key, tmpl.ID, tmpl.Target)
		}
	case opts.external != nil:
		if err := externalTemplateWithin(opts.external, held, tmpl, func(r string) bool { return allowedRepo(scope, r) }); err != nil {
			return err
		}
		if tmpl.AutoMerge {
			return fmt.Errorf("template %s is auto-merge: %w", tmpl.ID, errAgentAutoMerge)
		}
		if tmpl.KeepRunning {
			return fmt.Errorf("template %s sets keepRunning, which an external agent cannot turn on", tmpl.ID)
		}
		if tmpl.Interactive && !set.Allows("tasks:admin", repo) {
			return fmt.Errorf("template %s is interactive: approving it needs tasks:admin", tmpl.ID)
		}
		if tmpl.Priority != nil && *tmpl.Priority > model.DefaultTaskPriority && !set.Allows("tasks:admin", repo) {
			return fmt.Errorf("template %s sets priority %d (above %d), which needs tasks:admin", tmpl.ID, *tmpl.Priority, model.DefaultTaskPriority)
		}
		if s.networkAccessWidens(ctx, "", string(tmpl.NetworkAccess)) {
			return fmt.Errorf("template %s widens networkAccess past this deployment's default of \"none\"", tmpl.ID)
		}
		if tmpl.Target != nil && !set.Allows(scope, tmpl.Target.String()) {
			return fmt.Errorf("template %s files against %s, which the external agent's %s does not reach", tmpl.ID, tmpl.Target, scope)
		}
	default:
		if err := model.TemplateWithinTask(*opts.acting, tmpl); err != nil {
			return err
		}
		if tmpl.AutoMerge {
			return fmt.Errorf("template %s is auto-merge: %w", tmpl.ID, errAgentAutoMerge)
		}
		if tmpl.Target != nil && !set.Allows(scope, tmpl.Target.String()) {
			return fmt.Errorf("template %s files against %s, which the agent's %s does not reach", tmpl.ID, tmpl.Target, scope)
		}
	}
	return nil
}

func (s *Server) checkRoleSuite(ctx context.Context, suite model.Suite, key string, held []string, set APIGrantSet,
	allowedRepo func(APIScope, string) bool, scope APIScope, repo string, opts filingEscalationOpts) error {
	if suite.AutoMerge {
		return fmt.Errorf("%s cannot set %s: suite %s sets autoMerge, which requires an unlimited role (grain/operator)", opts.role.label(), key, suite.ID)
	}
	if suite.AutoApproveProposals && !set.Allows("tasks:admin", repo) {
		return fmt.Errorf("%s cannot set %s: suite %s sets autoApproveProposals, which needs tasks:admin", opts.role.label(), key, suite.ID)
	}
	if !suite.RequireApproval && !set.Allows("tasks:admin", repo) {
		return fmt.Errorf("%s cannot set %s: suite %s files approved tasks, which needs tasks:admin", opts.role.label(), key, suite.ID)
	}
	for _, it := range suite.Items {
		if tmpl := s.gateTemplate(ctx, it.TemplateID); tmpl != nil {
			if err := s.checkFilingTemplate(ctx, *tmpl, key, held, set, allowedRepo, scope, repo, opts); err != nil {
				return err
			}
		} else if err := s.checkDeclaredPushID(ctx, it.TemplateID, key, opts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) agentMayAutoApproveProposals(ctx context.Context, acting *model.Task) bool {
	if acting == nil {
		return false
	}
	if acting.AutoApproveProposals {
		return true
	}
	if s.tasks != nil {
		if ok, err := s.tasks.consoleSessionQueuesProposals(ctx, *acting); err == nil && ok {
			return true
		}
	}
	return false
}

// errAgentAutoMerge is the refusal of anything that would let work merge
// with nobody looking: turning auto-merge on (a body's autoMerge, a
// template that has it) and the Submit that does the same. That is the
// decision a person's Submit makes -- the one a held review waits for --
// so no scope hands it to an agent.
var errAgentAutoMerge = errors.New("an agent cannot turn on auto-merge or submit work: " +
	"letting a change merge unreviewed is a person's decision (docs/agent-api.md)")

// personOnlyChangeReview is a CL's Mail and Publish replies: who reads a
// change, and when they read a run's replies, is the person's call
// (changelists.md §5), never an agent's.
var personOnlyChangeReview = map[string]bool{
	"POST /api/tasks/{id}/change-mail":    true,
	"POST /api/tasks/{id}/change-publish": true,
	// A CL's Sync (changelists.md §7) is a person's button; the daemon's
	// own syncs are not made through the API.
	"POST /api/tasks/{id}/change-sync": true,
}

var errAgentChangeReview = errors.New("an agent cannot mail a CL, publish its replies or sync it: " +
	"who reviews a change, and when, is a person's decision")

var errAgentReviewers = errors.New("an agent cannot set reviewers: " +
	"who reviews a change is a person's decision (changelists.md §5)")

func hasTaskOwnerField(pattern string, raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	return pattern == "POST /api/tasks" ||
		pattern == "PATCH /api/tasks/{id}" ||
		pattern == "POST /api/tasks/from-template"
}

func hasReviewersField(pattern string, raw json.RawMessage) bool {
	if strings.TrimSpace(string(raw)) == "null" {
		return false
	}
	if strings.HasPrefix(pattern, "PATCH ") || pattern == "POST /api/tasks/{id}/comments" {
		return true
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return len(model.NormalizeReviewers(list)) > 0
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return strings.TrimSpace(one) != ""
	}
	return len(strings.TrimSpace(string(raw))) > 0
}

// agentMayWrite reports whether the acting task's own runs may push to
// repo: its target, or a repository its writes name.
func agentMayWrite(acting *model.Task, repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return true
	}
	if t := taskRepo(acting); t != "" && model.SameRepoText(t, repo) {
		return true
	}
	return slices.ContainsFunc(acting.Writes, func(w model.WriteTarget) bool { return model.SameRepoText(w.Repo.String(), repo) })
}

// agentMayReadAllRepos reports whether acting can already read every
// repository a session that reads all of them is given
// (model.ConsoleSessionWorkingRepos, less the state repository, which a
// session reads either way). Anything it cannot work out is a no.
func (s *Server) agentMayReadAllRepos(ctx context.Context, acting *model.Task) bool {
	if s.tasks == nil || s.tasks.Store == nil {
		return false
	}
	working, err := model.ConsoleSessionWorkingRepos(ctx, s.tasks.Store)
	if err != nil {
		return false
	}
	stateRepo := s.tasks.stateRepoRef()
	for _, r := range working {
		if stateRepo.Owner != "" && r.Same(stateRepo) {
			continue
		}
		if !agentMayRead(acting, r.String()) {
			return false
		}
	}
	return true
}

// agentMayRead reports whether the acting task's own runs may read repo.
func agentMayRead(acting *model.Task, repo string) bool {
	if agentMayWrite(acting, repo) {
		return true
	}
	return slices.ContainsFunc(acting.Reads, func(r model.RepoRef) bool { return model.SameRepoText(r.String(), repo) })
}

// agentCallRepo is the repository a call acts on, where one can be told:
// the {owner}/{name} of its path, the repository of the task its path
// names, or -- for a route whose path names no other resource -- a "repo"
// (or the task of a "taskId") in its body, or a read's repo query. A
// route whose path names something else ({id} of a CI run, a schedule, a
// template, ...) acts on that thing's repository whatever the body or
// query claim, so for those "" is returned: none is known, and only a
// grant for every repository reaches it.
func (s *Server) agentCallRepo(ctx context.Context, r *http.Request, pattern, target string, body []byte,
	acting *model.Task, set APIGrantSet, scope APIScope, ciCap bool) (string, error) {
	if repo, ok, err := pullRequestCallRepo(r, pattern, body); ok {
		if err == nil {
			repo, err = s.agentCanonicalRepo(repo)
		}
		return repo, err
	}
	if repo, ok, err := s.fsCallRepo(ctx, r, pattern); ok {
		return repo, err
	}
	if owner, name := r.PathValue("owner"), r.PathValue("name"); owner != "" && name != "" {
		// {owner} is host/owner, escaped into one segment, for a
		// repository off the default host (RepoFromStored), and an
		// agent may have written a remote there instead; either way
		// owner/name is the written repository.
		return s.agentCanonicalRepo(owner + "/" + name)
	}
	if target != "" {
		t, err := s.tasks.Store.GetTask(ctx, target)
		if err != nil || t == nil {
			return "", nil
		}
		return taskRepo(t), nil
	}
	if pattern == "GET /api/ci/runs/{id}" || pattern == "DELETE /api/ci/runs/{id}" || strings.HasPrefix(pathOf(pattern), "/api/ci/runs/{id}/") {
		// A CI run is its repository's, as externalAgentCallRepo reads it.
		if s.tasks != nil && s.tasks.Config.CI != nil {
			if run, ok := s.tasks.Config.CI.Get(r.PathValue("id")); ok && run.Repo != "" {
				return run.Repo, nil
			}
		}
		// A GitHub Actions run names its repository in its id.
		if repo, _, ok := ParseActionsRunID(r.PathValue("id")); ok {
			return s.agentCanonicalRepo(repo)
		}
	}
	if pattern == tasksWaitRoute {
		repo, _ := s.tasksWaitCallRepo(ctx, r, func(c string) bool {
			return set.Allows(scope, c) || (ciCap && agentMayRead(acting, c))
		})
		return repo, nil
	}
	if pattern == refSummaryRoute {
		if repo, ok, err := s.queryCallRepo(r, pattern, func(c string) bool {
			return set.Allows(scope, c)
		}); ok {
			return repo, err
		}
		return "", nil
	}
	if pathParam.MatchString(pathOf(pattern)) && !agentRepoDefaultRoutes[pattern] {
		return "", nil
	}
	var fields struct {
		Repo   string `json:"repo"`
		TaskID string `json:"taskId"`
	}
	if len(body) > 0 && json.Unmarshal(body, &fields) == nil {
		if fields.Repo != "" && routeBodyHasField(pattern, "repo") {
			return s.agentCanonicalRepo(fields.Repo)
		}
		if fields.TaskID != "" && routeBodyHasField(pattern, "taskId") {
			if t, err := s.tasks.Store.GetTask(ctx, fields.TaskID); err == nil && t != nil {
				return taskRepo(t), nil
			}
		}
	}
	if repo, ok, err := s.queryCallRepo(r, pattern, func(c string) bool {
		return set.Allows(scope, c) || (ciCap && agentMayRead(acting, c))
	}); ok {
		return repo, err
	}
	if pathParam.MatchString(pathOf(pattern)) {
		return "", nil
	}
	if pattern == "POST /api/tests/quarantine" && acting != nil {
		if own := taskRepo(acting); own != "" {
			return own, nil
		}
	}
	// A read-only collection asked for without ?repo= by a task that
	// reaches it on some repositories only lists the one it reaches --
	// the single repository its grant names, or its own -- rather than
	// being refused for naming none.
	if r.Method == http.MethodGet && agentRepoDefaultRoutes[pattern] && !isRepoNarrowedTasksRead(pattern, r) && !set.Allows(scope, "") {
		repo := agentDefaultRepo(acting, set.Repos(scope), ciCap)
		if repo != "" {
			q := r.URL.Query()
			q.Set("repo", repo)
			r.URL.RawQuery = q.Encode()
			return repo, nil
		}
	}
	return "", nil
}

// agentCanonicalRepo is the repository text a call names, spelled this
// deployment's way (Client.parseRepo): the form the acting task's grants,
// target and reads are written in, whichever of host/owner/name, an SSH
// remote or a URL the call used. Text that is not a repository is a 400,
// rather than a string no grant could match.
func (s *Server) agentCanonicalRepo(text string) (string, error) {
	ref, err := s.tasks.parseRepo(text)
	if err != nil {
		return "", err
	}
	return ref.String(), nil
}

// routeBodyHasField reports whether pattern's handler decodes a JSON body
// containing a top-level field matching want (routeBodies), so an extra
// "repo" or "taskId" added to a body the handler ignores cannot vouch for
// a repository on a deployment-wide route.
func routeBodyHasField(pattern, want string) bool {
	body := routeBodies[pattern]
	if body == nil {
		return false
	}
	return structHasJSONField(reflect.TypeOf(body), gateKey(want), 0)
}

func structHasJSONField(t reflect.Type, want string, depth int) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || depth > 4 || t.PkgPath() == "time" {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			if f.Anonymous {
				if structHasJSONField(f.Type, want, depth+1) {
					return true
				}
				continue
			}
			name = f.Name
		}
		if gateKey(name) == want {
			return true
		}
	}
	return false
}

func routeHasQuery(pattern, param string) bool {
	_, ok := routeQueries[pattern][param]
	return ok
}

// queryCallRepo reads ?repo= on a GET route whose handler actually takes
// repo (routeQueries), checking every repository named when the query
// repeats or comma-separates values so an unallowed second value cannot
// ride behind an allowed first one.
func (s *Server) queryCallRepo(r *http.Request, pattern string, allowed func(string) bool) (string, bool, error) {
	if r.Method != http.MethodGet || !routeHasQuery(pattern, "repo") {
		return "", false, nil
	}
	values := r.URL.Query()["repo"]
	if len(values) == 0 {
		return "", false, nil
	}
	if pattern == "GET /api/metrics" {
		var items []string
		for _, raw := range values {
			for _, part := range strings.Split(raw, ",") {
				if part = strings.TrimSpace(part); part != "" {
					items = append(items, part)
				}
			}
		}
		if len(items) == 0 {
			return "", false, nil
		}
		var first string
		for _, item := range items {
			canonical, err := s.agentCanonicalRepo(item)
			if err != nil {
				return "", true, err
			}
			if first == "" {
				first = canonical
			}
			if !allowed(canonical) {
				return canonical, true, nil
			}
		}
		return first, true, nil
	}
	if len(values) == 1 && strings.TrimSpace(values[0]) == "" {
		return "", false, nil
	}
	var first string
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return "", true, nil
		}
		canonical, err := s.agentCanonicalRepo(trimmed)
		if err != nil {
			return "", true, err
		}
		if first == "" {
			first = canonical
		}
		if !allowed(canonical) {
			return canonical, true, nil
		}
	}
	return first, true, nil
}

// agentDefaultRepo is the repository a collection read by acting is
// narrowed to when it names none: the one repository granted, or the
// task's own where that is among those granted (or grain-ci reaches it).
func agentDefaultRepo(acting *model.Task, granted []string, ciCap bool) string {
	var own string
	if acting != nil {
		own = taskRepo(acting)
	}
	if own != "" && (ciCap || slices.ContainsFunc(granted, func(g string) bool { return model.SameRepoText(g, own) })) {
		return own
	}
	if len(granted) == 1 {
		return granted[0]
	}
	return ""
}

// recordAgentCall writes the attribution: a line in the daemon's journal
// for every call, a row in task_run_api_call for the active run, and, for
// a change to another task, a note in that task's conversation signed as
// grain on the acting run's behalf.
func (s *Server) recordAgentCall(ctx context.Context, call agentCall, status int, resultBytes int64) {
	principal := call.principalID()
	// who is the principal as the journal names it, with the person an
	// owned external agent credential acts for (#XC-14).
	who := call.journalActor()
	clientAttr := ""
	if call.filedVia != "" {
		clientAttr = fmt.Sprintf(" client=%q", call.filedVia)
	}
	if call.channel != "" {
		log.Printf("grain api: {agent, %s} %s scope=%s repo=%q channel=%q reason=%q%s -> %d",
			who, call.pattern, call.scope, call.repo, call.channel, call.reason, clientAttr, status)
	} else {
		log.Printf("grain api: {agent, %s} %s scope=%s repo=%q reason=%q%s -> %d",
			who, call.pattern, call.scope, call.repo, call.reason, clientAttr, status)
	}
	if call.external == nil && s.tasks != nil && s.tasks.Store != nil {
		bgCtx := context.WithoutCancel(ctx)
		runID := call.runID
		if runID == "" {
			runID = s.tasks.Store.ActiveRunID(bgCtx, call.taskID)
		}
		if runID != "" && call.channel == SandboxCLIChannel {
			tool := routeToolName(call.pattern)
			_ = s.tasks.Store.RecordProxyToolCall(bgCtx, runID, tool, resultBytes, status >= 400)
			if status < 400 {
				_ = s.tasks.Store.RecordProxyAPICall(bgCtx, runID, tool, call.targetTask, resultBytes)
			}
		}
	}
	method, _, _ := strings.Cut(call.pattern, " ")
	targets := call.targetTasks
	if len(targets) == 0 && call.targetTask != "" {
		targets = []string{call.targetTask}
	}
	if method == http.MethodGet || status >= 300 || len(targets) == 0 ||
		call.pattern == agentMessagesRoute {
		return
	}
	var body string
	if call.external != nil {
		onBehalf := ""
		if owner := call.owner(); owner != "" {
			onBehalf = fmt.Sprintf(" on behalf of `%s`", owner)
		}
		body = fmt.Sprintf("External agent `%s` (`%s`)%s called `%s` through the agent API (scope `%s`).",
			call.external.Name, principal, onBehalf, call.pattern, call.scope)
	} else {
		body = fmt.Sprintf("Task %s called `%s` through the agent API (scope `%s`).", call.taskID, call.pattern, call.scope)
	}
	if call.reason != "" {
		body += " Reason: " + call.reason
	}
	seen := map[string]bool{}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" || seen[target] || (call.external == nil && target == call.taskID) {
			continue
		}
		seen[target] = true
		_, err := s.tasks.Store.AddComment(context.WithoutCancel(ctx), model.Comment{
			TaskID: target,
			Author: model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
				OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: principal},
			},
			Body: body,
		})
		if err != nil {
			log.Printf("grain api: recording {agent, %s}'s %s on task %s: %v", principal, call.pattern, target, err)
		}
	}
}

func routeToolName(pattern string) string {
	for _, r := range AgentAPIRoutes() {
		if r.Pattern == pattern {
			return r.Tool
		}
	}
	return pattern
}

// noTaskContextRoutes are routes that implicitly assume a calling task run or
// interactive console session and have no meaning for an external principal.
var noTaskContextRoutes = map[string]string{
	"POST /api/tasks/{id}/messages":           "speaks as the calling task's own run to its operator and parks that task; external agents have no calling task (use POST /api/tasks/{id}/comments or POST /api/tasks/{id}/agent-messages instead)",
	"PUT /api/tasks/{id}/session-title":       "renames the calling run's own standing chat session; external agents have no calling task",
	"GET /api/console":                        "lists live interactive sandbox consoles; external agents have no sandbox console context",
	"GET /api/console/{id}/output":            "reads a live sandbox console stream; not available to external agents",
	"POST /api/console/{id}/input":            "types into a live sandbox console; not available to external agents",
	"POST /api/console/{id}/resize":           "resizes a live sandbox console; not available to external agents",
	"DELETE /api/console/{id}":                "closes a live sandbox console; not available to external agents",
	"GET /api/console-sessions":               "standing console sessions are bound to in-grain session tasks; not available to external agents",
	"POST /api/console-sessions":              "standing console sessions are bound to in-grain session tasks; not available to external agents",
	"PUT /api/console-sessions/{id}":          "standing console sessions are bound to in-grain session tasks; not available to external agents",
	"DELETE /api/console-sessions/{id}":       "standing console sessions are bound to in-grain session tasks; not available to external agents",
	"POST /api/console-sessions/{id}/restart": "standing console sessions are bound to in-grain session tasks; not available to external agents",
	"POST /api/console-sessions/{id}/cancel":  "standing console sessions are bound to in-grain session tasks; not available to external agents",
}

func isNoTaskContextRoute(pattern string) (string, bool) {
	if why, ok := noTaskContextRoutes[pattern]; ok {
		return why, true
	}
	return "", false
}

// authorizeExternalAgentCall decides whether external token tok may make this call.
func (s *Server) authorizeExternalAgentCall(r *http.Request, pattern string, scope APIScope, tok *model.ExternalAgentToken) (agentCall, error) {
	call := agentCall{
		external: tok,
		pattern:  pattern,
		scope:    scope,
		reason:   strings.TrimSpace(r.Header.Get(ActingReasonHeader)),
		filedVia: model.SanitizeGrainClient(r.Header.Get(model.GrainClientHeader)),
	}
	principal := call.principalID()
	if scope == "" || scope == scopeInternal {
		return call, fmt.Errorf("%s is not part of the agent API (docs/agent-api.md)", pattern)
	}
	if why, blocked := isNoTaskContextRoute(pattern); blocked {
		return call, fmt.Errorf("%s cannot call %s: %s", principal, pattern, why)
	}
	if s.tasks == nil || s.tasks.Store == nil {
		return call, errNoAgentAPI
	}
	ctx := r.Context()
	held, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
	if err != nil {
		return call, fmt.Errorf("external agent %q has invalid grants: %w", tok.Name, err)
	}
	if role := accessRoleFromContext(ctx); role != nil && !role.Full {
		// A token presented inside a verified session never reaches past
		// that session's role (resolveMCPCallerAccess narrows it the same
		// way), so the multi-scope checks below -- externalApprovalRail's
		// tasks:admin, a task PATCH's capabilities:admin, tasks:admin and
		// capabilities:detach -- read the narrower of the two.
		set = set.Intersect(role.set).Without(role.deny)
	}
	call.grants = set
	adminPatch := tasksAdminAllowsTaskPatch(set, pattern)
	detach := capabilitiesDetachAllowsRoute(set, pattern)
	// GET /api/inbox and GET /api/events without ?repo= answer only the
	// repositories the token reads (Server.inboxReadable,
	// Server.eventsReadable), so a token held to some of them is let
	// through rather than refused for naming none. GET /api/config and
	// GET /api/templates answer board-level metadata (`grain skill
	// install`, `grain list`, `grain create`) under deployment:read and
	// templates:read even when the credential is narrowed to specific
	// repositories.
	fsAllowed := fsMergeQueueByCI(set, pattern, r) ||
		((pattern == "GET /api/inbox" || pattern == "GET /api/events") && set.Has(scope) && strings.TrimSpace(r.URL.Query().Get("repo")) == "") ||
		(pattern == "GET /api/config" && (set.Has(scope) || set.Has("deployment:read"))) ||
		(pattern == "GET /api/templates" && set.Has(scope))
	if !set.Has(scope) && !adminPatch && !fsAllowed && !detach {
		return call, fmt.Errorf("%s holds no api grant for scope %s, which %s needs", principal, scope, pattern)
	}
	if fsRoutes[pattern] == "src" && !set.Has("deployment:src") {
		return call, errExternalSrc(principal)
	}
	body, err := peekBody(r)
	if err != nil {
		return call, err
	}
	call.body = body
	method, _, _ := strings.Cut(pattern, " ")
	if target := r.PathValue("id"); target != "" && strings.HasPrefix(pathOf(pattern), "/api/tasks/{id}") {
		call.targetTask = target
		call.targetTasks = []string{target}
	}
	var extraTargets []string
	call.repo, extraTargets, err = s.externalAgentCallRepo(ctx, r, pattern, scope, set, call.targetTask, body, tok)
	if err != nil {
		return call, err
	}
	if len(extraTargets) > 0 && len(call.targetTasks) == 0 {
		call.targetTasks = extraTargets
		call.targetTask = extraTargets[0]
	}
	adminPatchAllowed := false
	if _, fs := fsRoutes[pattern]; fs && call.repo == "" && set.Has(scope) {
		// As for a task's call: fsScope narrows it.
		fsAllowed = true
	}
	if isRepoNarrowedTasksRead(pattern, r) && call.repo == "" && set.Has(scope) {
		fsAllowed = true
	}
	if adminPatch && !set.Allows(scope, call.repo) && set.Allows("tasks:admin", call.repo) {
		if err := tasksAdminTaskPatchBodyAllowed(body); err != nil {
			return call, err
		}
		adminPatchAllowed = true
	}
	if detach && !set.Allows(scope, call.repo) && set.Allows("capabilities:detach", call.repo) {
		if err := s.capabilitiesDetachAllowed(ctx, principal, pattern, call.repo, call.targetTask, call.reason, body); err != nil {
			return call, err
		}
		adminPatchAllowed = true
	}
	if !set.Allows(scope, call.repo) && !adminPatchAllowed && !fsAllowed {
		missingScope := scope
		if !set.Has(scope) && adminPatch {
			missingScope = "tasks:admin"
		}
		if !set.Has(scope) && detach {
			missingScope = "capabilities:detach"
		}
		if call.repo == "" {
			return call, fmt.Errorf("%s holds %s only for %s, and %s does not name a repository it reaches",
				principal, missingScope, strings.Join(set.Repos(missingScope), ", "), pattern)
		}
		return call, fmt.Errorf("%s holds %s, but not on %s", principal, missingScope, call.repo)
	}
	if pattern == "PATCH /api/tasks/{id}" && patchTouchesTaskGrants(body) {
		if !set.Allows("capabilities:admin", call.repo) {
			return call, fmt.Errorf("%s holds no api grant for scope capabilities:admin, which %s needs when editing task capabilities",
				principal, pattern)
		}
		if call.reason == "" {
			return call, fmt.Errorf("%s with capabilities is destructive (scope capabilities:admin): give a reason", pattern)
		}
	}
	if (IsDangerousAPIScope(scope) || method == http.MethodDelete) && call.reason == "" {
		return call, fmt.Errorf("%s is destructive (scope %s): give a reason", pattern, scope)
	}
	if err := s.externalAgentRails(ctx, tok, set, call.repo, pattern, method, scope, call.targetTask, call.reason, body); err != nil {
		return call, err
	}
	if err := s.agentStandingDeclaredPush(ctx, pattern, r.PathValue("id")); err != nil {
		return call, err
	}
	if err := agentGrantEscalationFrom(held, pattern, body, s.storedFieldGrants(ctx, pattern, call.repo), s.tasks.normalizeCapabilityID); err != nil {
		return call, err
	}
	if err := s.checkExternalTokenRotationEscalation(ctx, held, pattern, r.PathValue("id")); err != nil {
		return call, err
	}
	if err := s.externalAgentFilingEscalation(ctx, tok, held, set, pattern, scope, call.repo, body); err != nil {
		return call, err
	}
	if pattern == "PATCH /api/tasks/{id}" {
		if err := agentTaskPatchBodyAllowed(body); err != nil {
			return call, err
		}
	}
	return call, nil
}

func (s *Server) checkExternalTokenRotationEscalation(ctx context.Context, held []string, pattern, id string) error {
	if pattern != "POST /api/external-agents/tokens/{id}/rotate" || strings.TrimSpace(id) == "" {
		return nil
	}
	existing, err := s.tasks.Store.GetExternalAgentToken(ctx, strings.TrimSpace(id))
	if err != nil || existing == nil {
		return nil
	}
	own := ""
	if tok := externalTokenFromContext(ctx); tok != nil {
		own = strings.TrimSpace(tok.Owner)
	}
	if existingOwner := strings.TrimSpace(existing.Owner); !strings.EqualFold(existingOwner, own) {
		return fmt.Errorf("an agent cannot rotate a credential owned by %q", existingOwner)
	}
	grants, _, needsPerson, err := model.ResolveExternalAgentGrants(existing.ScopeSet, existing.Scopes, existing.Repos)
	if err != nil {
		return err
	}
	if needsPerson {
		return fmt.Errorf("external agent credential %q includes a dangerous scope or a requires_person scope set: only a person may rotate it", existing.Name)
	}
	for _, g := range grants {
		if err := agentGrantRefusal(held, g, s.tasks.normalizeCapabilityID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) externalAgentCallRepo(ctx context.Context, r *http.Request, pattern string, scope APIScope, set APIGrantSet, target string, body []byte, tok *model.ExternalAgentToken) (string, []string, error) {
	if repo, ok, err := pullRequestCallRepo(r, pattern, body); ok {
		if err == nil {
			repo, err = s.agentCanonicalRepo(repo)
		}
		return repo, nil, err
	}
	if repo, ok, err := s.fsCallRepo(ctx, r, pattern); ok {
		return repo, nil, err
	}
	if owner, name := r.PathValue("owner"), r.PathValue("name"); owner != "" && name != "" {
		repo, err := s.agentCanonicalRepo(owner + "/" + name)
		return repo, nil, err
	}
	if target != "" {
		t, err := s.tasks.Store.GetTask(ctx, target)
		if err != nil || t == nil {
			return "", nil, nil
		}
		return taskRepo(t), []string{target}, nil
	}
	if pattern == "GET /api/ci/runs/{id}" || pattern == "DELETE /api/ci/runs/{id}" || strings.HasPrefix(pathOf(pattern), "/api/ci/runs/{id}/") {
		if s.tasks != nil && s.tasks.Config.CI != nil {
			if run, ok := s.tasks.Config.CI.Get(r.PathValue("id")); ok && run.Repo != "" {
				return run.Repo, nil, nil
			}
		}
		if repo, _, ok := ParseActionsRunID(r.PathValue("id")); ok {
			canonical, err := s.agentCanonicalRepo(repo)
			return canonical, nil, err
		}
	}
	if pattern == refSummaryRoute {
		if repo, ok, err := s.queryCallRepo(r, pattern, func(c string) bool {
			return set.Allows(scope, c)
		}); ok {
			return repo, nil, err
		}
		return "", nil, nil
	}
	if pathParam.MatchString(pathOf(pattern)) {
		return "", nil, nil
	}
	if pattern == tasksWaitRoute {
		repo, ids := s.tasksWaitCallRepo(ctx, r, func(c string) bool { return set.Allows(scope, c) })
		return repo, ids, nil
	}
	if pattern == "POST /api/tasks/reorder" && len(body) > 0 {
		var req ReorderRequest
		if json.Unmarshal(body, &req) == nil && len(req.IDs) > 0 {
			var firstRepo string
			for _, id := range req.IDs {
				t, err := s.tasks.Store.GetTask(ctx, strings.TrimSpace(id))
				if err != nil || t == nil {
					continue
				}
				tr := taskRepo(t)
				if tr == "" || !set.Allows(scope, tr) {
					return tr, req.IDs, nil
				}
				if firstRepo == "" {
					firstRepo = tr
				}
			}
			return firstRepo, req.IDs, nil
		}
	}
	var fields struct {
		Repo       string `json:"repo"`
		TaskID     string `json:"taskId"`
		TemplateID string `json:"templateId"`
	}
	if len(body) > 0 && json.Unmarshal(body, &fields) == nil {
		if id := strings.TrimSpace(fields.TaskID); id != "" && routeBodyHasField(pattern, "taskId") {
			if t, err := s.tasks.Store.GetTask(ctx, id); err == nil && t != nil {
				return taskRepo(t), []string{id}, nil
			}
		}
		if repo := strings.TrimSpace(fields.Repo); repo != "" && routeBodyHasField(pattern, "repo") {
			repo, err := s.agentCanonicalRepo(repo)
			return repo, nil, err
		}
		if pattern == "POST /api/tasks" && strings.TrimSpace(fields.TemplateID) != "" {
			if tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(fields.TemplateID)); err == nil && tmpl != nil && tmpl.Target != nil {
				return tmpl.Target.String(), nil, nil
			}
		}
	}
	if repo, ok, err := s.queryCallRepo(r, pattern, func(c string) bool {
		return set.Allows(scope, c)
	}); ok {
		return repo, nil, err
	}
	// If the credential is restricted to a single repository and calls a
	// read-only collection endpoint that accepts ?repo=, default the filter
	// to that repository so listing works without repeating the repo parameter.
	if r.Method == http.MethodGet && len(tok.Repos) == 1 && !isRepoNarrowedTasksRead(pattern, r) {
		switch pattern {
		case "GET /api/tasks", "GET /api/ci/runs", "GET /api/ci/workflows",
			"GET /api/merge-batches", "GET /api/pipeline/changes", "GET /api/memory/documents/{name}":
			q := r.URL.Query()
			if q.Get("repo") == "" {
				q.Set("repo", tok.Repos[0])
				r.URL.RawQuery = q.Encode()
			}
			return tok.Repos[0], nil, nil
		}
	}
	return "", nil, nil
}

func (s *Server) externalAgentRails(ctx context.Context, tok *model.ExternalAgentToken, set APIGrantSet, repo, pattern, method string, scope APIScope, target, reason string, body []byte) error {
	if method == http.MethodGet {
		return nil
	}
	principal := "external:" + tok.Name
	me := model.Principal{Kind: model.PrincipalAgent, ID: principal}
	if target != "" {
		if t, err := s.tasks.Store.GetTask(ctx, target); err == nil && t != nil {
			if session, ok := model.ConsoleSessionIDOf(*t); ok {
				return fmt.Errorf("task %s is the standing session %q, which %s does not own", target, session, principal)
			}
		}
		if pattern == "POST /api/tasks/{id}/run-now" || pattern == "POST /api/tasks/{id}/pause" || pattern == "POST /api/tasks/{id}/resume" {
			if p, err := s.tasks.Store.GetPause(ctx, target); err == nil && p != nil && !p.PausedBy.Same(me) {
				return fmt.Errorf("task %s is paused by %s %s; only they may lift or override that pause",
					target, p.PausedBy.Kind, principalName(p.PausedBy))
			}
		}
	}
	if (pattern == approveTaskRoute || pattern == runNowTaskRoute) && target != "" {
		if err := s.externalApprovalRail(ctx, principal, set, repo, target, reason, pattern == runNowTaskRoute); err != nil {
			return err
		}
	}
	// External agents have no calling task from which to inherit a repository,
	// so filing a task requires an explicit target (repo, templateId with target, or noRepo).
	if pattern == "POST /api/tasks" {
		var req struct {
			Repo       string `json:"repo"`
			NoRepo     bool   `json:"noRepo"`
			TemplateID string `json:"templateId"`
		}
		if len(body) == 0 || json.Unmarshal(body, &req) != nil {
			return fmt.Errorf("%s has no calling task context: POST /api/tasks requires an explicit repo (or noRepo: true)", principal)
		}
		if strings.TrimSpace(req.Repo) == "" && !req.NoRepo {
			if strings.TrimSpace(req.TemplateID) == "" {
				return fmt.Errorf("%s has no calling task context: POST /api/tasks requires an explicit repo (or noRepo: true)", principal)
			}
			if tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(req.TemplateID)); err == nil && tmpl != nil && tmpl.Target == nil {
				return fmt.Errorf("%s has no calling task context: template %s names no repository, so POST /api/tasks requires an explicit repo (or noRepo: true)", principal, tmpl.ID)
			}
		}
	}
	path := pathOf(pattern)
	upgradeHold := pattern == "POST /api/pipeline-pause" ||
		pattern == "DELETE /api/pipeline-pause" || pattern == "DELETE /api/pause" ||
		strings.HasPrefix(string(scope), "deployment:") && scope != "deployment:read"
	if upgradeHold {
		pause, err := s.tasks.Store.GetPipelinePause(ctx)
		if err != nil {
			return fmt.Errorf("reading the pipeline pause: %w", err)
		}
		if pause != nil {
			switch {
			case strings.HasPrefix(path, "/api/pipeline-pause") || strings.HasPrefix(path, "/api/pause"):
				if !pause.By.Same(me) {
					return fmt.Errorf("dispatch was paused by %s %s (%s); only they may lift it", pause.By.Kind, pause.By.ID, pause.Reason)
				}
				if method == http.MethodDelete && (!pause.AdmitsRunNow() || s.tasks.upgradeUnderway(ctx)) {
					return fmt.Errorf("an upgrade holds the queue (%s); %s waits for it", pause.Reason, pattern)
				}
			case pause.UntilTaskID != "":
				return fmt.Errorf("an upgrade hold is standing for task %s (%s); %s waits for it", pause.UntilTaskID, pause.Reason, pattern)
			}
		}
	}
	return nil
}

func (s *Server) externalAgentFilingEscalation(ctx context.Context, tok *model.ExternalAgentToken, held []string, set APIGrantSet,
	pattern string, scope APIScope, repo string, body []byte) error {
	// A repository the work may push to or read is one the credential's
	// own scope for this call reaches: tok.Repos alone is not enough,
	// since a credential with no repos list may still hold its scopes
	// only on named repositories (tasks:write@owner/name).
	allowedRepo := func(_ APIScope, r string) bool {
		r = strings.TrimSpace(r)
		if r == "" {
			return true
		}
		return set.Allows(scope, r)
	}
	return s.filingEscalation(ctx, held, set, allowedRepo, pattern, scope, repo, body, filingEscalationOpts{external: tok})
}

// externalTemplateWithin is model.TemplateWithinTask for an external agent
// credential: a template it names may grant only what the credential itself
// holds, never a grant only a person may give, and push or read access only
// to repositories the credential covers.
func externalTemplateWithin(tok *model.ExternalAgentToken, held []string, tmpl model.Template, allowedRepo func(string) bool) error {
	heldKeys := make([]string, 0, len(held))
	for _, h := range held {
		for _, c := range model.CanonicalLegacyGrants(h) {
			heldKeys = append(heldKeys, model.CanonicalGrantKey(c))
		}
	}
	for _, g := range tmpl.Grants {
		if model.IsPersonOnlyGrant(g.Capability) {
			return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
		}
		for _, needed := range model.CanonicalLegacyGrants(g.Capability) {
			if model.IsPersonOnlyGrant(needed) {
				return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
			}
			if !slices.Contains(heldKeys, model.CanonicalGrantKey(needed)) {
				return fmt.Errorf("template %s grants %s, which external agent %q does not hold", tmpl.ID, g.Capability, tok.Name)
			}
		}
	}
	for _, w := range tmpl.Writes {
		if !allowedRepo(w.Repo.String()) {
			return fmt.Errorf("template %s grants push access to %s, which external agent %q's credential does not cover", tmpl.ID, w.Repo, tok.Name)
		}
	}
	for _, r := range tmpl.Reads {
		if !allowedRepo(r.String()) {
			return fmt.Errorf("template %s grants read access to %s, which external agent %q's credential does not cover", tmpl.ID, r, tok.Name)
		}
	}
	if len(tmpl.Reviewers) > 0 {
		return fmt.Errorf("template %s sets reviewers, which only a person may set", tmpl.ID)
	}
	if model.NormalizeAdmission(tmpl.Admission).AtLeast(model.AdmissionBypass) {
		return fmt.Errorf("template %s sets admission %s, which only a person may give", tmpl.ID, tmpl.Admission)
	}
	return nil
}

// taskRepo is the repository a task's runs push to, or "" for a task
// with none.
func taskRepo(t *model.Task) string {
	if t.Target == nil {
		return ""
	}
	return t.Target.String()
}

func grantNames(grants []model.Grant) []string {
	out := make([]string, len(grants))
	for i, g := range grants {
		out[i] = g.Capability
	}
	return out
}

// peekBody reads r's body and puts the same bytes back for the handler.
func peekBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxAgentAPIBody+1))
	r.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading the request body: %w", err)
	}
	if len(data) > maxAgentAPIBody {
		return nil, fmt.Errorf("request body is larger than the agent API reads (%d bytes)", maxAgentAPIBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

// pathOf is a pattern's path, without its method.
func pathOf(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

var pathParam = regexp.MustCompile(`\{([A-Za-z]+)(\.\.\.)?\}`)

// statusRecorder remembers the status and byte count a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type taskMutationRespKind int

const (
	// taskRespNone marks a mutating task route whose response carries only
	// action-specific metadata (never a Task or TaskDetail record).
	taskRespNone taskMutationRespKind = iota + 1
	// taskRespSingle marks a mutating route that echoes a single Task or
	// TaskDetail JSON object. Without tasks:read on that repository, only
	// {id, state} is returned.
	taskRespSingle
	// taskRespPatch marks PATCH /api/tasks/{id}, which returns {id, state}
	// plus any scalar/list fields explicitly replaced by the request body
	// when the caller lacks tasks:read.
	taskRespPatch
	// taskRespList marks POST /api/tasks/reorder, which returns a []Task
	// slice across the queue.
	taskRespList
	// taskRespWaitOn marks POST /api/tasks/{id}/wait-on, whose WaitTarget
	// entries include target tasks' titles and closing notes.
	taskRespWaitOn
	// taskRespCreate marks POST /api/tasks. A plain filing echoes back only
	// what the caller just wrote, so it is returned whole; a filing that
	// names a template or a task to supersede draws on text the caller may
	// not read, and is cut to {id, state} like taskRespSingle.
	taskRespCreate
)

// taskMutationResponseKinds classifies every non-GET, non-internal route under
// /api/tasks (and every non-GET route holding a tasks:* scope). Held total by
// TestTaskMutationResponseClassificationIsTotal so a new task mutation cannot
// silently echo respondWithTask to a caller without tasks:read.
var taskMutationResponseKinds = map[string]taskMutationRespKind{
	"POST /api/tasks":                              taskRespCreate,
	"POST /api/tasks/from-template":                taskRespSingle,
	"POST /api/tasks/reorder":                      taskRespList,
	"PATCH /api/tasks/{id}":                        taskRespPatch,
	"POST /api/tasks/{id}/capabilities":            taskRespSingle,
	"POST /api/tasks/{id}/depends-on":              taskRespSingle,
	"POST /api/tasks/{id}/approve":                 taskRespSingle,
	"POST /api/tasks/{id}/withdraw-approval":       taskRespSingle,
	"POST /api/tasks/{id}/park-until":              taskRespNone,
	"POST /api/pull-requests/review":               taskRespNone,
	"POST /api/tasks/{id}/resume-later":            taskRespNone,
	"POST /api/tasks/{id}/defer":                   taskRespSingle,
	"POST /api/tasks/{id}/undefer":                 taskRespSingle,
	"POST /api/tasks/{id}/submit":                  taskRespSingle,
	"POST /api/tasks/{id}/comments":                taskRespSingle,
	"POST /api/tasks/{id}/reply":                   taskRespSingle,
	"PUT /api/tasks/{id}/secret":                   taskRespSingle,
	"POST /api/tasks/{id}/close":                   taskRespSingle,
	"POST /api/tasks/{id}/stop":                    taskRespSingle,
	"POST /api/tasks/{id}/pause":                   taskRespSingle,
	"POST /api/tasks/{id}/resume":                  taskRespSingle,
	"PUT /api/tasks/{id}/goal":                     taskRespSingle,
	"PUT /api/tasks/{id}/hostd":                    taskRespSingle,
	"DELETE /api/tasks/{id}/goal":                  taskRespSingle,
	"POST /api/tasks/{id}/reopen":                  taskRespSingle,
	"POST /api/tasks/{id}/retry":                   taskRespSingle,
	"POST /api/tasks/{id}/retry-merge":             taskRespSingle,
	"POST /api/tasks/{id}/force-merge":             taskRespSingle,
	"POST /api/tasks/{id}/run-now":                 taskRespSingle,
	"POST /api/tasks/{id}/pull-request":            taskRespNone,
	"POST /api/tasks/{id}/sandbox/recreate":        taskRespNone,
	"POST /api/tasks/{id}/messages":                taskRespNone,
	"POST /api/tasks/{id}/notes":                   taskRespNone,
	"POST /api/tasks/{id}/human-review":            taskRespNone,
	"POST /api/tasks/{id}/human-review/release":    taskRespNone,
	"POST /api/tasks/{id}/agent-messages":          taskRespNone,
	"POST /api/tasks/{id}/change-publish":          taskRespSingle,
	"POST /api/tasks/{id}/change-mail":             taskRespSingle,
	"POST /api/tasks/{id}/change-sync":             taskRespSingle,
	"POST /api/tasks/{id}/wait-on":                 taskRespWaitOn,
	"POST /api/repos/{owner}/{name}/import-issues": taskRespNone,
}

type bufferedResponseRecorder struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (b *bufferedResponseRecorder) Header() http.Header {
	return b.header
}

func (b *bufferedResponseRecorder) WriteHeader(code int) {
	if !b.wroteHeader {
		b.status = code
		b.wroteHeader = true
	}
}

func (b *bufferedResponseRecorder) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

func (s *Server) serveAgentCall(w http.ResponseWriter, r *http.Request, next http.HandlerFunc, call agentCall) {
	kind, ok := taskMutationResponseKinds[call.pattern]
	if !ok || kind == taskRespNone {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		s.recordAgentCall(r.Context(), call, rec.status, rec.bytes)
		return
	}

	buf := &bufferedResponseRecorder{
		header: make(http.Header),
		status: http.StatusOK,
	}
	next(buf, r)

	raw := buf.body.Bytes()
	out := raw
	if buf.status >= 200 && buf.status < 300 && len(raw) > 0 {
		out = s.redactAgentTaskResponse(r.Context(), call, kind, raw)
	}
	for k, vals := range buf.header {
		w.Header()[k] = append([]string(nil), vals...)
	}
	if len(out) != len(raw) {
		w.Header().Del("Content-Length")
	}
	if buf.wroteHeader || len(out) > 0 {
		w.WriteHeader(buf.status)
	}
	var written int
	if len(out) > 0 {
		written, _ = w.Write(out)
	}
	s.recordAgentCall(r.Context(), call, buf.status, int64(written))
}

func (s *Server) redactAgentTaskResponse(ctx context.Context, call agentCall, kind taskMutationRespKind, raw []byte) []byte {
	if kind == taskRespCreate {
		if !createDrawsOnOtherText(call.body) {
			return raw
		}
		kind = taskRespSingle
	}
	switch kind {
	case taskRespSingle, taskRespPatch:
		var full map[string]json.RawMessage
		if err := json.Unmarshal(raw, &full); err != nil || full == nil {
			return raw
		}
		respRepo := jsonStringField(full, "repo")
		canRead := call.grants.Allows("tasks:read", respRepo)
		if canRead && kind == taskRespPatch && !model.SameRepoText(call.repo, respRepo) {
			canRead = call.grants.Allows("tasks:read", call.repo)
		}
		if canRead {
			return raw
		}
		redacted := map[string]json.RawMessage{}
		for _, k := range []string{"id", "state"} {
			if v, ok := full[k]; ok {
				redacted[k] = v
			}
		}
		if kind == taskRespPatch {
			for _, k := range editedPatchResponseKeys(call.body) {
				if v, ok := full[k]; ok {
					redacted[k] = v
				}
			}
		}
		return marshalJSONLine(redacted, raw)

	case taskRespList:
		if call.grants.Allows("tasks:read", "") {
			return raw
		}
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return raw
		}
		requested := map[string]bool{}
		var req ReorderRequest
		if len(call.body) > 0 && json.Unmarshal(call.body, &req) == nil {
			for _, id := range req.IDs {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					requested[trimmed] = true
				}
			}
		}
		filtered := make([]map[string]json.RawMessage, 0, len(items))
		for _, item := range items {
			repo := jsonStringField(item, "repo")
			if repo != "" && call.grants.Allows("tasks:read", repo) {
				filtered = append(filtered, item)
				continue
			}
			id := jsonStringField(item, "id")
			if requested[id] {
				stub := map[string]json.RawMessage{}
				for _, k := range []string{"id", "state"} {
					if v, ok := item[k]; ok {
						stub[k] = v
					}
				}
				filtered = append(filtered, stub)
			}
		}
		return marshalJSONLine(filtered, raw)

	case taskRespWaitOn:
		if call.grants.Allows("tasks:read", "") {
			return raw
		}
		var resp waitOnResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return raw
		}
		for i := range resp.Targets {
			targetRepo := ""
			if s.tasks != nil && s.tasks.Store != nil {
				if t, err := s.tasks.Store.GetTask(ctx, resp.Targets[i].ID); err == nil && t != nil {
					targetRepo = taskRepo(t)
				}
			}
			if !call.grants.Allows("tasks:read", targetRepo) {
				resp.Targets[i].Title = ""
				resp.Targets[i].ClosingNote = ""
			}
		}
		return marshalJSONLine(resp, raw)
	}
	return raw
}

// createDrawsOnOtherText reports whether a POST /api/tasks body names a
// template, a task to supersede, a proposalKey, or a parent/source task from
// which an empty externalRef would be inherited, any of which can populate or
// resolve the response from an existing record rather than only what the caller sent.
func createDrawsOnOtherText(body []byte) bool {
	var req CreateTaskRequest
	if len(body) == 0 || json.Unmarshal(body, &req) != nil {
		return true
	}
	parsedOrigin, originErr := model.ParseWorkOrigin(req.Origin, nil)
	inheritsExternalRef := strings.TrimSpace(req.ExternalRef) == "" &&
		(strings.TrimSpace(req.ProposedBy) != "" ||
			model.NormalizeTaskID(req.Reverts) != "" ||
			originErr != nil ||
			parsedOrigin.Task() != "")
	return strings.TrimSpace(req.TemplateID) != "" ||
		strings.TrimSpace(req.Supersedes) != "" ||
		strings.TrimSpace(req.ProposalKey) != "" ||
		inheritsExternalRef
}

func editedPatchResponseKeys(body []byte) []string {
	var req UpdateTaskRequest
	if len(body) == 0 || json.Unmarshal(body, &req) != nil {
		return nil
	}
	var keys []string
	add := func(set bool, key string) {
		if set {
			keys = append(keys, key)
		}
	}
	add(req.Title != nil, "title")
	add(req.Description != nil, "description")
	add(req.Owner != nil, "owner")
	add(req.Repo != nil, "repo")
	add(req.Base != nil, "base")
	add(req.ExternalRef != nil, "externalRef")
	add(req.Priority != nil, "priority")
	add(req.PriorityDispatch != nil, "priorityDispatch")
	add(req.Admission != nil, "admission")
	add(req.Hosting != nil, "hosting")
	add(req.Issue != nil, "issue")
	add(req.AutoMerge != nil, "autoMerge")
	add(req.AutoApproveProposals != nil, "autoApproveProposals")
	add(req.SandboxCPUs != nil, "sandboxCpus")
	add(req.SandboxMemoryMB != nil, "sandboxMemoryMb")
	add(req.SandboxDiskGB != nil, "sandboxDiskGb")
	add(req.AgentFramework != nil, "agentFramework")
	add(req.Model != nil, "model")
	add(req.AgentBackendID != nil, "agentBackendId")
	add(req.NetworkAccess != nil, "networkAccess")
	add(req.NothingNeededAction != nil, "nothingNeededAction")
	add(req.KeepRunning != nil || req.KeepRunningSnake != nil, "keepRunning")
	add(req.NoPort != nil, "noPort")
	add(req.FixesBaseBranch != nil, "fixesBaseBranch")
	add(req.PromptExtension != nil, "promptExtension")
	add(req.ReviewTemplateID != nil, "reviewTemplateId")
	add(req.Reads != nil, "reads")
	add(req.Writes != nil, "writes")
	add(req.GoogleChatSpace != nil, "googleChatSpace")
	add(req.Designs != nil, "designs")
	add(req.Context != nil, "context")
	return keys
}

func jsonStringField(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func marshalJSONLine(v any, fallback []byte) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return append(b, '\n')
}

// tasksWaitRoute is GET /api/tasks/wait, whose tasks are named in its
// query rather than its path.
const tasksWaitRoute = "GET /api/tasks/wait"

// tasksWaitCallRepo is the repository a GET /api/tasks/wait call is
// judged on, checking every task it names (#XC-22): the first task's
// repository whose grant allowed refuses, so it is the one the gate
// names, and otherwise the first task's own. A task id that names no
// task (yet) belongs to no repository a grant can be judged on, so the
// call is judged on "" -- only a grant for every repository reaches it,
// and the handler answers that caller 404 -- rather than letting a
// token held to some repositories name an id another's task is filed
// under between this check and the handler's read.
func (s *Server) tasksWaitCallRepo(ctx context.Context, r *http.Request, allowed func(string) bool) (string, []string) {
	ids := TasksWaitIDs(r)
	first, firstSet := "", false
	for _, id := range ids {
		t, err := s.tasks.Store.GetTask(ctx, id)
		if err != nil || t == nil {
			return "", ids
		}
		repo := taskRepo(t)
		if !allowed(repo) {
			return repo, ids
		}
		if !firstSet {
			first, firstSet = repo, true
		}
	}
	return first, ids
}
