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
	targetTask  string
	targetTasks []string
}

func (c agentCall) principalID() string {
	if c.external != nil {
		return "external:" + c.external.Name
	}
	return c.taskID
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
		if pattern != "POST /mcp" {
			if tok := externalTokenFromContext(r.Context()); tok != nil {
				call, err := s.authorizeExternalAgentCall(r, pattern, scope, tok)
				if err != nil {
					log.Printf("grain api: refused {agent, %s} %s: %v", call.principalID(), pattern, err)
					writeError(w, agentRefusalStatus(err), err)
					return
				}
				agent := model.Principal{Kind: model.PrincipalAgent, ID: call.principalID()}
				r = r.WithContext(withAgentCall(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), call))
				rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
				next(rec, r)
				s.recordAgentCall(r.Context(), call, rec.status, rec.bytes)
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
		agent := model.Principal{Kind: model.PrincipalAgent, ID: taskID}
		r = r.WithContext(withAgentCall(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), call))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		s.recordAgentCall(r.Context(), call, rec.status, rec.bytes)
	}
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
		taskID:  taskID,
		runID:   strings.TrimSpace(r.Header.Get(ActingRunHeader)),
		channel: strings.TrimSpace(r.Header.Get(ChannelHeader)),
		pattern: pattern,
		scope:   scope,
		reason:  strings.TrimSpace(r.Header.Get(ActingReasonHeader)),
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
	held := grantNames(acting.Grants)
	var set APIGrantSet
	if call.channel == SandboxCLIChannel {
		set = model.SandboxAPIGrantsOf(held)
	} else {
		set = APIGrantsOf(held)
	}
	call.grants = set
	capAllowed := agentCapabilityAllowsRoute(held, set, pattern, scope)
	ciCap := grainCIReadsRoute(held, pattern)
	if !set.Has(scope) && !capAllowed && !ciCap {
		return call, fmt.Errorf("task %s holds no api grant for scope %s, which %s needs: "+
			"ask a person to grant api:%s or a preset that includes it (docs/agent-api.md)", taskID, scope, pattern, scope)
	}
	body, err := peekBody(r)
	if err != nil {
		return call, err
	}
	method, _, _ := strings.Cut(pattern, " ")
	if target := r.PathValue("id"); target != "" && strings.HasPrefix(pathOf(pattern), "/api/tasks/{id}") {
		call.targetTask = target
	}
	call.repo, err = s.agentCallRepo(ctx, r, pattern, call.targetTask, body, acting, set, scope, ciCap)
	if err != nil {
		return call, err
	}
	if ciCap && !capAllowed && !set.Allows(scope, call.repo) && call.repo != "" && agentMayRead(acting, call.repo) {
		// grain-ci reads CI of the repositories the task's runs may
		// read, as it does over /fs/ci/ and /cli/ci/.
		capAllowed = true
	}
	if !set.Allows(scope, call.repo) && !capAllowed {
		if call.repo == "" {
			return call, fmt.Errorf("task %s holds %s only for some repositories, and %s does not name one it reaches",
				taskID, scope, pattern)
		}
		return call, fmt.Errorf("task %s holds %s, but not on %s", taskID, scope, call.repo)
	}
	if call.channel == SandboxCLIChannel {
		if err := sandboxChannelGuard(pattern, body); err != nil {
			return call, err
		}
	}
	if (IsDangerousAPIScope(scope) || method == http.MethodDelete) && call.reason == "" {
		return call, fmt.Errorf("%s is destructive (scope %s): give a reason", pattern, scope)
	}
	if err := s.agentRails(ctx, acting, pattern, method, scope, call.targetTask, r.PathValue("id")); err != nil {
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
	return call, nil
}

// agentCapabilityAllowsRoute reports whether a non-API capability grant ("memory",
// "release-notes") authorizes pattern, or releases:read lets a release
// manager read (never write) the release notes. Writing them stays with
// the release-notes capability and memory:write: releases:write is a
// per-repository release scope, and the weekly notes are the
// deployment's own.
func agentCapabilityAllowsRoute(held []string, set APIGrantSet, pattern string, scope APIScope) bool {
	path := pathOf(pattern)
	if path == "/api/memory" || strings.HasPrefix(path, "/api/memory/") {
		if (scope == "memory:read" || scope == "memory:write") && slices.Contains(held, "memory") {
			return true
		}
	}
	if path == "/api/release-notes" || strings.HasPrefix(path, "/api/release-notes/") {
		if slices.Contains(held, "release-notes") {
			return true
		}
		if scope == "memory:read" && set.Allows("releases:read", "") {
			return true
		}
	}
	return false
}

// grainCIReadRoutes are the agent API's CI reads the grain-ci capability
// reaches on its own, for the repositories its task's runs may read
// (Task.Repo, Reads and Writes): the same records /fs/ci/ and /cli/ci/
// serve a run holding it.
var grainCIReadRoutes = map[string]bool{
	"GET /api/ci/runs":                             true,
	"GET /api/ci/runs/{id}":                        true,
	"GET /api/ci/runs/{id}/artifacts":              true,
	"GET /api/ci/runs/{id}/artifacts/{job}/{name}": true,
	"GET /api/ci/workflows":                        true,
}

// grainCIReadsRoute reports whether held includes grain-ci and pattern is
// one of the CI reads it opens. Which repository is the caller's to
// check once it is known (authorizeAgentCall).
func grainCIReadsRoute(held []string, pattern string) bool {
	return grainCIReadRoutes[pattern] && slices.Contains(held, "grain-ci")
}

// agentRepoDefaultRoutes are the read-only collection routes that filter
// by ?repo=, and that a caller reaching only one repository may call
// without naming it.
var agentRepoDefaultRoutes = map[string]bool{
	"GET /api/tasks":                   true,
	"GET /api/ci/runs":                 true,
	"GET /api/ci/workflows":            true,
	"GET /api/merge-batches":           true,
	"GET /api/pipeline/changes":        true,
	"GET /api/memory/documents/{name}": true,
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
		if len(grantsInBody(pattern, body)) > 0 {
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
			if session, ok := model.ConsoleSessionIDOf(*t); ok && session != ownSession {
				return fmt.Errorf("task %s is the standing session %q, which task %s does not own", target, session, acting.ID)
			}
		}
		if pattern == "POST /api/tasks/{id}/run-now" || pattern == "POST /api/tasks/{id}/pause" || pattern == "POST /api/tasks/{id}/resume" {
			me := model.Principal{Kind: model.PrincipalAgent, ID: acting.ID}
			if p, err := s.tasks.Store.GetPause(ctx, target); err == nil && p != nil && p.PausedBy != me {
				return fmt.Errorf("task %s is paused by %s %s; only they may lift or override that pause",
					target, p.PausedBy.Kind, principalName(p.PausedBy))
			}
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
				if pause.By != me {
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
	// APIGrantsOf reads an alias (self-upgrade) as the preset it now is,
	// which ParseAPIGrant alone does not.
	if slices.ContainsFunc(APIGrantsOf([]string{name}).Scopes(), IsDangerousAPIScope) || model.IsDangerousAPIGrant(name) {
		return fmt.Errorf("%s is a dangerous api grant: only a person may grant it", name)
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

func addedGrantsInBody(pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) []string {
	if len(body) == 0 {
		return nil
	}
	if read, ok := grantBodies[pattern]; ok {
		return read(body)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	var names []string
	for key, raw := range fields {
		lower := strings.ToLower(key)
		if lower == "priority" {
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				names = append(names, model.PriorityDispatchCapability)
			}
			continue
		}
		if lower == "keeprunning" || lower == "keep_running" {
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				names = append(names, model.DurableCapability)
			}
			continue
		}
		if !strings.Contains(lower, "capabilit") && !strings.EqualFold(key, "grants") {
			continue
		}
		existing := stored[lower]
		for _, name := range grantNamesIn(raw) {
			if grantAlreadyStored(existing, name, normalize) {
				continue
			}
			names = append(names, name)
		}
	}
	return names
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

// agentFilingEscalation refuses the rest of what a task, schedule or
// template body can hand out beyond its capabilities (agentGrantEscalation):
// push or read access to a repository, auto-merge (and Submit, which
// turns it on), approval, a requester,
// a template's grants, or a move of the work to a repository the scope does
// not reach. Each is what propose_task already withholds from a run
// (model.EscalatedCapabilities, the auto-merge and approval rules), and the
// API is not a way around it. encoding/json matches keys without regard to
// case, and so does this.
func (s *Server) agentFilingEscalation(ctx context.Context, acting *model.Task, set APIGrantSet,
	pattern string, scope APIScope, repo string, body []byte) error {
	if pattern == "POST /api/tasks/{id}/submit" {
		return errAgentAutoMerge
	}
	if personOnlyChangeReview[pattern] {
		return errAgentChangeReview
	}
	if len(body) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	for key, raw := range fields {
		switch strings.ToLower(key) {
		case "writes":
			var targets []WriteTarget
			if json.Unmarshal(raw, &targets) != nil {
				continue // the handler refuses a body it cannot read
			}
			for _, t := range targets {
				if !agentMayWrite(acting, t.Repo) {
					return fmt.Errorf("an agent cannot grant push access to %s, which its own task cannot push to", t.Repo)
				}
			}
		case "reads":
			var repos []string
			if json.Unmarshal(raw, &repos) != nil {
				continue
			}
			for _, r := range repos {
				if !agentMayRead(acting, r) {
					return fmt.Errorf("an agent cannot grant read access to %s, which its own task cannot read", r)
				}
			}
		case "automerge":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				return errAgentAutoMerge
			}
		case "autoapproveproposals":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on && !acting.AutoApproveProposals {
				return errors.New("an agent cannot turn on autoApproveProposals, which its own task does not have")
			}
		case "approved", "interactive":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on && !set.Allows("tasks:admin", repo) {
				return errors.New("an agent files work unapproved: approving it needs tasks:admin")
			}
		case "author", "proposedby":
			var who string
			if json.Unmarshal(raw, &who) == nil && strings.TrimSpace(who) != "" {
				return fmt.Errorf("an agent cannot name somebody else as the %s of work it files", key)
			}
		case "proposalkey":
			var k string
			if json.Unmarshal(raw, &k) == nil && strings.TrimSpace(k) != "" {
				return errors.New("an agent cannot set proposalKey directly")
			}
		case "reviewers":
			if hasReviewersField(pattern, raw) {
				return errAgentReviewers
			}
		case "templateid":
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(id))
			if err != nil || tmpl == nil {
				continue // the handler says it is not there
			}
			if err := model.TemplateWithinTask(*acting, *tmpl); err != nil {
				return err
			}
			if tmpl.AutoMerge {
				return fmt.Errorf("template %s is auto-merge: %w", tmpl.ID, errAgentAutoMerge)
			}
			if tmpl.Target != nil && !set.Allows(scope, tmpl.Target.String()) {
				return fmt.Errorf("template %s files against %s, which the agent's %s does not reach", tmpl.ID, tmpl.Target, scope)
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
			tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(id))
			if err != nil || tmpl == nil {
				continue // the handler says it is not there
			}
			var target *model.RepoRef
			if r, err := model.ParseRepo(repo); err == nil {
				target = &r
			}
			if err := model.ReviewTemplateWithinTask(*acting, *tmpl, target); err != nil {
				return err
			}
		case "repo":
			// Moving work to another repository (PATCH /api/tasks/{id}
			// with a new repo) is a call on that repository too.
			var to string
			if json.Unmarshal(raw, &to) != nil || strings.TrimSpace(to) == "" || model.SameRepoText(to, repo) {
				continue
			}
			if !set.Allows(scope, strings.TrimSpace(to)) {
				return fmt.Errorf("the agent's %s does not reach %s", scope, strings.TrimSpace(to))
			}
		}
	}
	return nil
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
	}
	if pathParam.MatchString(pathOf(pattern)) && !agentRepoDefaultRoutes[pattern] {
		return "", nil
	}
	var fields struct {
		Repo   string `json:"repo"`
		TaskID string `json:"taskId"`
	}
	if len(body) > 0 && json.Unmarshal(body, &fields) == nil {
		if fields.Repo != "" {
			return s.agentCanonicalRepo(fields.Repo)
		}
		if fields.TaskID != "" {
			if t, err := s.tasks.Store.GetTask(ctx, fields.TaskID); err == nil && t != nil {
				return taskRepo(t), nil
			}
		}
	}
	if repo := r.URL.Query().Get("repo"); repo != "" && r.Method == http.MethodGet {
		return s.agentCanonicalRepo(repo)
	}
	if pathParam.MatchString(pathOf(pattern)) {
		return "", nil
	}
	// A read-only collection asked for without ?repo= by a task that
	// reaches it on some repositories only lists the one it reaches --
	// the single repository its grant names, or its own -- rather than
	// being refused for naming none.
	if r.Method == http.MethodGet && agentRepoDefaultRoutes[pattern] && !set.Allows(scope, "") {
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

// agentDefaultRepo is the repository a collection read by acting is
// narrowed to when it names none: the one repository granted, or the
// task's own where that is among those granted (or grain-ci reaches it).
func agentDefaultRepo(acting *model.Task, granted []string, ciCap bool) string {
	own := taskRepo(acting)
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
	if call.channel != "" {
		log.Printf("grain api: {agent, %s} %s scope=%s repo=%q channel=%q reason=%q -> %d",
			principal, call.pattern, call.scope, call.repo, call.channel, call.reason, status)
	} else {
		log.Printf("grain api: {agent, %s} %s scope=%s repo=%q reason=%q -> %d",
			principal, call.pattern, call.scope, call.repo, call.reason, status)
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
		body = fmt.Sprintf("External agent `%s` (`%s`) called `%s` through the agent API (scope `%s`).",
			call.external.Name, principal, call.pattern, call.scope)
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
	call.grants = set
	if !set.Has(scope) {
		return call, fmt.Errorf("%s holds no api grant for scope %s, which %s needs", principal, scope, pattern)
	}
	body, err := peekBody(r)
	if err != nil {
		return call, err
	}
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
	if !set.Allows(scope, call.repo) {
		if call.repo == "" {
			return call, fmt.Errorf("%s holds %s only for %s, and %s does not name a repository it reaches",
				principal, scope, strings.Join(set.Repos(scope), ", "), pattern)
		}
		return call, fmt.Errorf("%s holds %s, but not on %s", principal, scope, call.repo)
	}
	if (IsDangerousAPIScope(scope) || method == http.MethodDelete) && call.reason == "" {
		return call, fmt.Errorf("%s is destructive (scope %s): give a reason", pattern, scope)
	}
	if err := s.externalAgentRails(ctx, tok, pattern, method, scope, call.targetTask, body); err != nil {
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
	}
	if pathParam.MatchString(pathOf(pattern)) {
		return "", nil, nil
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
		Repo   string `json:"repo"`
		TaskID string `json:"taskId"`
	}
	if len(body) > 0 && json.Unmarshal(body, &fields) == nil {
		if id := strings.TrimSpace(fields.TaskID); id != "" {
			if t, err := s.tasks.Store.GetTask(ctx, id); err == nil && t != nil {
				return taskRepo(t), []string{id}, nil
			}
		}
		if repo := strings.TrimSpace(fields.Repo); repo != "" {
			repo, err := s.agentCanonicalRepo(repo)
			return repo, nil, err
		}
	}
	if repo := strings.TrimSpace(r.URL.Query().Get("repo")); repo != "" && r.Method == http.MethodGet {
		repo, err := s.agentCanonicalRepo(repo)
		return repo, nil, err
	}
	// If the credential is restricted to a single repository and calls a
	// read-only collection endpoint that accepts ?repo=, default the filter
	// to that repository so listing works without repeating the repo parameter.
	if r.Method == http.MethodGet && len(tok.Repos) == 1 {
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

func (s *Server) externalAgentRails(ctx context.Context, tok *model.ExternalAgentToken, pattern, method string, scope APIScope, target string, body []byte) error {
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
			if p, err := s.tasks.Store.GetPause(ctx, target); err == nil && p != nil && p.PausedBy != me {
				return fmt.Errorf("task %s is paused by %s %s; only they may lift or override that pause",
					target, p.PausedBy.Kind, principalName(p.PausedBy))
			}
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
		if strings.TrimSpace(req.Repo) == "" && !req.NoRepo && strings.TrimSpace(req.TemplateID) == "" {
			return fmt.Errorf("%s has no calling task context: POST /api/tasks requires an explicit repo (or noRepo: true)", principal)
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
				if pause.By != me {
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
	if pattern == "POST /api/tasks/{id}/submit" {
		return errAgentAutoMerge
	}
	if personOnlyChangeReview[pattern] {
		return errAgentChangeReview
	}
	if len(body) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	// A repository the work may push to or read is one the credential's
	// own scope for this call reaches: tok.Repos alone is not enough,
	// since a credential with no repos list may still hold its scopes
	// only on named repositories (tasks:write@owner/name).
	allowedRepo := func(r string) bool {
		r = strings.TrimSpace(r)
		if r == "" {
			return true
		}
		return set.Allows(scope, r)
	}
	for key, raw := range fields {
		switch strings.ToLower(key) {
		case "writes":
			var targets []WriteTarget
			if json.Unmarshal(raw, &targets) != nil {
				continue
			}
			for _, t := range targets {
				if !allowedRepo(t.Repo) {
					return fmt.Errorf("external agent %q cannot grant push access to %s, which its credential does not cover", tok.Name, t.Repo)
				}
			}
		case "reads":
			var repos []string
			if json.Unmarshal(raw, &repos) != nil {
				continue
			}
			for _, r := range repos {
				if !allowedRepo(r) {
					return fmt.Errorf("external agent %q cannot grant read access to %s, which its credential does not cover", tok.Name, r)
				}
			}
		case "automerge":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				return errAgentAutoMerge
			}
		case "autoapproveproposals":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on {
				return errors.New("an external agent cannot turn on autoApproveProposals")
			}
		case "approved":
			var on bool
			if json.Unmarshal(raw, &on) == nil && on && !set.Allows("tasks:admin", repo) {
				return errors.New("an agent files work unapproved: approving it needs tasks:admin")
			}
		case "author", "proposedby":
			var who string
			if json.Unmarshal(raw, &who) == nil && strings.TrimSpace(who) != "" {
				return fmt.Errorf("an agent cannot name somebody else as the %s of work it files", key)
			}
		case "reviewers":
			if hasReviewersField(pattern, raw) {
				return errAgentReviewers
			}
		case "templateid":
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(id))
			if err != nil || tmpl == nil {
				continue
			}
			if err := externalTemplateWithin(tok, held, *tmpl, allowedRepo); err != nil {
				return err
			}
			if tmpl.AutoMerge {
				return fmt.Errorf("template %s is auto-merge: %w", tmpl.ID, errAgentAutoMerge)
			}
			if tmpl.Target != nil && !set.Allows(scope, tmpl.Target.String()) {
				return fmt.Errorf("template %s files against %s, which the external agent's %s does not reach", tmpl.ID, tmpl.Target, scope)
			}
		case "reviewtemplateid":
			// A review is filed approved and auto-merge, so its template
			// is held to the credential's own grants exactly as an
			// in-grain agent's is to its task's (ReviewTemplateWithinTask).
			var id string
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				continue
			}
			tmpl, err := s.tasks.Store.GetTemplate(ctx, strings.TrimSpace(id))
			if err != nil || tmpl == nil {
				continue
			}
			if r, err := model.ParseRepo(repo); err == nil && tmpl.ID == model.DefaultReviewTemplateID(r) {
				continue
			}
			if err := externalTemplateWithin(tok, held, *tmpl, allowedRepo); err != nil {
				return err
			}
		case "repo":
			var to string
			if json.Unmarshal(raw, &to) != nil || strings.TrimSpace(to) == "" || model.SameRepoText(to, repo) {
				continue
			}
			if !set.Allows(scope, strings.TrimSpace(to)) {
				return fmt.Errorf("the external agent's %s does not reach %s", scope, strings.TrimSpace(to))
			}
		}
	}
	return nil
}

// externalTemplateWithin is model.TemplateWithinTask for an external agent
// credential: a template it names may grant only what the credential itself
// holds, never a grant only a person may give, and push or read access only
// to repositories the credential covers.
func externalTemplateWithin(tok *model.ExternalAgentToken, held []string, tmpl model.Template, allowedRepo func(string) bool) error {
	for _, g := range tmpl.Grants {
		if model.IsPersonOnlyGrant(g.Capability) {
			return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
		}
		if !slices.Contains(held, g.Capability) {
			return fmt.Errorf("template %s grants %s, which external agent %q does not hold", tmpl.ID, g.Capability, tok.Name)
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
