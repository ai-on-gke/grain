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

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// DefaultMaxNamespaceConcurrentRequests is the default per-sandbox concurrency
// limit on /fs/ requests to protect the daemon from runaway shell loops.
const DefaultMaxNamespaceConcurrentRequests = 8

// NamespaceScopeLookup resolves a sandbox name to the namespace scope of its
// currently live task run. Implemented by *model.Store.
type NamespaceScopeLookup interface {
	SandboxNamespaceScope(ctx context.Context, sandbox string) (model.NamespaceScope, bool, error)
}

type namespaceLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

func (l *namespaceLimiter) acquire(sandbox string, limit int) bool {
	if limit <= 0 {
		limit = DefaultMaxNamespaceConcurrentRequests
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active = make(map[string]int)
	}
	if l.active[sandbox] >= limit {
		return false
	}
	l.active[sandbox]++
	return true
}

func (l *namespaceLimiter) release(sandbox string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		return
	}
	if l.active[sandbox] <= 1 {
		delete(l.active, sandbox)
		return
	}
	l.active[sandbox]--
}

func extractNamespaceAuthToken(header string) (string, bool) {
	if tok, ok := ExtractBasicAuthToken(header); ok {
		return tok, true
	}
	if rest, ok := strings.CutPrefix(strings.TrimSpace(header), "Bearer "); ok {
		tok := strings.TrimSpace(rest)
		if tok != "" {
			return tok, true
		}
	}
	return "", false
}

// authenticateSandbox resolves r's proxy token to its sandbox, answering
// 401 itself when it cannot.
func (p *GitProxy) authenticateSandbox(w http.ResponseWriter, r *http.Request) (string, bool) {
	grant, ok := p.authenticateSandboxGrant(w, r)
	if !ok {
		return "", false
	}
	return grant.Sandbox, true
}

// authenticateSandboxGrant resolves r's proxy token to its full Grant, answering
// 401 itself when it cannot.
func (p *GitProxy) authenticateSandboxGrant(w http.ResponseWriter, r *http.Request) (Grant, bool) {
	token, hasToken := extractNamespaceAuthToken(r.Header.Get("Authorization"))
	var grant Grant
	var ok bool
	if hasToken && p.Tokens != nil {
		grant, ok = p.Tokens.AuthenticateGrant(token)
	}
	if !hasToken || !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="grain-git-proxy"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return Grant{}, false
	}
	return grant, true
}

// resolveSandboxScope resolves grant's sandbox to the live run's NamespaceScope
// and verifies per-attempt token binding (TaskID and Attempt), answering 403 on
// any mismatch.
func (p *GitProxy) resolveSandboxScope(w http.ResponseWriter, r *http.Request, grant Grant) (model.NamespaceScope, bool) {
	if p.NamespaceScopes == nil {
		http.Error(w, "forbidden: no namespace scope configured", http.StatusForbidden)
		return model.NamespaceScope{}, false
	}
	modelScope, live, err := p.NamespaceScopes.SandboxNamespaceScope(r.Context(), grant.Sandbox)
	if err != nil {
		http.Error(w, "failed to resolve sandbox namespace scope", http.StatusInternalServerError)
		return model.NamespaceScope{}, false
	}
	if !live {
		http.Error(w, "forbidden: no live task run on this sandbox", http.StatusForbidden)
		return model.NamespaceScope{}, false
	}
	if grant.TaskID != "" && modelScope.TaskID != "" && grant.TaskID != modelScope.TaskID {
		http.Error(w, fmt.Sprintf("forbidden: sandbox token belongs to task %s, not live task %s", grant.TaskID, modelScope.TaskID), http.StatusForbidden)
		return model.NamespaceScope{}, false
	}
	if grant.Attempt > 0 && modelScope.Attempt > 0 && grant.Attempt != modelScope.Attempt {
		http.Error(w, fmt.Sprintf("forbidden: sandbox token belongs to attempt %d, not current attempt %d", grant.Attempt, modelScope.Attempt), http.StatusForbidden)
		return model.NamespaceScope{}, false
	}
	return modelScope, true
}

func isNamespacePath(path string) bool {
	return path == "/fs" || strings.HasPrefix(path, "/fs/")
}

func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

func isCapPath(path string) bool {
	return path == "/cap" || strings.HasPrefix(path, "/cap/")
}

// ServeNamespaceHTTP handles a GET or HEAD request under /fs/<scheme>/<path>,
// authenticating with the sandbox's proxy token and enforcing the live task's
// namespace scope before delegating to p.Namespace.ServeHTTP.
func (p *GitProxy) ServeNamespaceHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Namespace == nil {
		http.Error(w, "namespace not configured", http.StatusNotFound)
		return
	}
	grant, ok := p.authenticateSandboxGrant(w, r)
	if !ok {
		return
	}

	if !p.nsLimiter.acquire(grant.Sandbox, p.MaxNamespaceConcurrency) {
		http.Error(w, "too many concurrent /fs/ requests from this sandbox", http.StatusTooManyRequests)
		return
	}
	defer p.nsLimiter.release(grant.Sandbox)

	modelScope, ok := p.resolveSandboxScope(w, r, grant)
	if !ok {
		return
	}

	scope := toNamespaceScope(modelScope)
	act := NamespaceRouteActivity(r.URL.Path, r.URL.Query())
	p.noteActivity(r.Context(), modelScope.TaskID, act.Phrase)
	cw := &countingResponseWriter{ResponseWriter: w}
	p.Namespace.ServeHTTP(cw, r, scope)
	p.recordRouteCall(r.Context(), modelScope.RunID, act, cw)
}

// ServeAPIHTTP handles a request under /api/..., authenticating with the
// sandbox's per-attempt proxy token and forwarding to p.API (grain's UI/API
// server) with X-Grain-Acting-Task, X-Grain-Acting-Run, and
// X-Grain-Channel: sandbox-cli so agentAPIGate enforces non-dangerous scope
// boundaries and records the call.
func (p *GitProxy) ServeAPIHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil {
		http.Error(w, "api not configured", http.StatusNotFound)
		return
	}
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
	if p.API == nil {
		http.Error(w, "this deployment's sandbox proxy is not wired to the agent API", http.StatusNotImplemented)
		return
	}
	p.forwardToAPI(w, r, modelScope, r.URL.Path)
}

func (p *GitProxy) forwardToAPI(w http.ResponseWriter, r *http.Request, modelScope model.NamespaceScope, targetPath string) {
	// p.API answers as the deployment's own actor, and X-Grain-Acting-Task
	// is the only thing that holds a forwarded request to a task's grants:
	// agentAPIGate passes a request without one straight through. A live
	// run with no task to act for is refused rather than forwarded bare.
	if strings.TrimSpace(modelScope.TaskID) == "" {
		http.Error(w, "forbidden: this sandbox has no task to act for", http.StatusForbidden)
		return
	}
	in := r.Clone(r.Context())
	in.URL.Path = targetPath
	in.URL.RawPath = ""
	in.RequestURI = in.URL.RequestURI()
	in.RemoteAddr = ""
	in.Host = "localhost"
	reason := strings.TrimSpace(r.Header.Get("X-Grain-Acting-Reason"))
	if reason == "" {
		reason = strings.TrimSpace(r.Header.Get("X-Grain-Reason"))
	}
	for k := range in.Header {
		if strings.EqualFold(k, "Authorization") || strings.HasPrefix(strings.ToLower(k), "x-grain-") {
			in.Header.Del(k)
		}
	}
	in.Header.Set("X-Grain-Acting-Task", modelScope.TaskID)
	if modelScope.RunID != "" {
		in.Header.Set("X-Grain-Acting-Run", modelScope.RunID)
	}
	in.Header.Set("X-Grain-Channel", "sandbox-cli")
	if reason != "" {
		in.Header.Set("X-Grain-Acting-Reason", reason)
	}
	phrase := fmt.Sprintf("calling agent API %s %s", r.Method, targetPath)
	p.noteActivity(r.Context(), modelScope.TaskID, phrase)
	p.API.ServeHTTP(w, in)
}

// ServeCapHTTP handles a request under /cap/..., authenticating with the
// sandbox's per-attempt proxy token and auditing the call. Built-in capability
// endpoints (/cap/memory, /cap/release-notes, /cap/quarantine) delegate to the
// corresponding API or CLI routes; external capability servers are the
// sandbox-callable ones CapServers holds for the attempt or CI job
// (capserver.go).
func (p *GitProxy) ServeCapHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil {
		http.Error(w, "capability proxy not configured", http.StatusNotFound)
		return
	}
	grant, ok := p.authenticateSandboxGrant(w, r)
	if !ok {
		return
	}
	if !p.nsLimiter.acquire(grant.Sandbox, p.MaxNamespaceConcurrency) {
		http.Error(w, "too many concurrent requests from this sandbox", http.StatusTooManyRequests)
		return
	}
	defer p.nsLimiter.release(grant.Sandbox)

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/cap"), "/")
	// A CI job's sandbox has no task run behind it: its servers are
	// registered under the sandbox itself, and it reaches nothing else
	// here.
	if servers, ok := p.CapServers.Servers(CapSandboxKey(grant.Sandbox)); ok {
		p.serveExternalCap(w, r, servers, rest, "", "", "CI sandbox "+grant.Sandbox)
		return
	}
	modelScope, ok := p.resolveSandboxScope(w, r, grant)
	if !ok {
		return
	}
	srv, tail, _ := strings.Cut(rest, "/")
	switch srv {
	case "memory":
		if p.API == nil {
			http.Error(w, "this deployment's sandbox proxy is not wired to the agent API", http.StatusNotImplemented)
			return
		}
		apiPath := "/api/memory"
		if tail != "" {
			apiPath += "/" + tail
		}
		p.forwardToAPI(w, r, modelScope, apiPath)
		return
	case "release-notes":
		if p.API == nil {
			http.Error(w, "this deployment's sandbox proxy is not wired to the agent API", http.StatusNotImplemented)
			return
		}
		apiPath := "/api/release-notes"
		if tail != "" {
			apiPath += "/" + tail
		}
		p.forwardToAPI(w, r, modelScope, apiPath)
		return
	case "quarantine", "test-quarantine", "quarantine-test":
		scope := toNamespaceScope(modelScope)
		act := RouteActivity{Tool: "quarantine_test", Phrase: "quarantining a test"}
		p.noteActivity(r.Context(), modelScope.TaskID, act.Phrase)
		cw := &countingResponseWriter{ResponseWriter: w}
		p.serveQuarantine(cw, r, scope)
		p.recordRouteCall(r.Context(), modelScope.RunID, act, cw)
		return
	}
	servers, _ := p.CapServers.Servers(CapRunKey(modelScope.RunID))
	p.serveExternalCap(w, r, servers, rest, modelScope.TaskID, modelScope.RunID, "run "+modelScope.RunID)
}

func toNamespaceScope(modelScope model.NamespaceScope) namespace.Scope {
	taskID, _ := strconv.ParseInt(modelScope.TaskID, 10, 64)
	return namespace.Scope{
		TaskID:            taskID,
		TaskIDStr:         modelScope.TaskID,
		RunID:             modelScope.RunID,
		Attempt:           modelScope.Attempt,
		TaskRepo:          modelScope.TaskRepo,
		TaskBranch:        modelScope.TaskBranch,
		TaskBase:          modelScope.TaskBase,
		ConfigRepo:        modelScope.ConfigRepo,
		Grants:            modelScope.Grants,
		MayReadCI:         modelScope.MayReadCI,
		MayReadAllCI:      modelScope.MayReadAllCI,
		AllowedRepos:      modelScope.AllowedRepos,
		MayReadSrc:        modelScope.MayReadSrc,
		MayReadAllTasks:   modelScope.MayReadAllTasks,
		MayReadFullConfig: modelScope.MayReadFullConfig,
		MayReadAllMemory:  modelScope.MayReadAllMemory,
		MayReadMetrics:    modelScope.MayReadMetrics,
		MayReadPipeline:   modelScope.MayReadPipeline,
		MayReadHealth:     modelScope.MayReadHealth,
		MayReadGrainLog:   modelScope.MayReadGrainLog,
		MayReadPlaybooks:  modelScope.MayReadPlaybooks,
		MayReadIssues:     modelScope.MayReadIssues,
		MayReadDB:         modelScope.MayReadDB,
	}
}

func (p *GitProxy) handleNamespaceRequest(ctx context.Context, method, path, query string, headers map[string]string, body io.Reader) ProxyResponse {
	u := &url.URL{Path: path, RawQuery: query}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return Denial(http.StatusBadRequest, err.Error())
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	switch {
	case isCLIPath(path):
		p.ServeCLIHTTP(rec, req)
	case isAPIPath(path):
		p.ServeAPIHTTP(rec, req)
	case isCapPath(path):
		p.ServeCapHTTP(rec, req)
	case isGitSignPath(path):
		p.ServeGitSignHTTP(rec, req)
	default:
		p.ServeNamespaceHTTP(rec, req)
	}
	respHeaders := make(map[string]string, len(rec.Header()))
	for k := range rec.Header() {
		respHeaders[k] = rec.Header().Get(k)
	}
	return ProxyResponse{
		Status:  rec.Code,
		Headers: respHeaders,
		Body:    rec.Body.Bytes(),
	}
}
