package gitproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/namespace"
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
	token, hasToken := extractNamespaceAuthToken(r.Header.Get("Authorization"))
	var sandbox string
	var ok bool
	if hasToken && p.Tokens != nil {
		sandbox, ok = p.Tokens.Authenticate(token)
	}
	if !hasToken || !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="grain-git-proxy"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return "", false
	}
	return sandbox, true
}

func isNamespacePath(path string) bool {
	return path == "/fs" || strings.HasPrefix(path, "/fs/")
}

// ServeNamespaceHTTP handles a GET or HEAD request under /fs/<scheme>/<path>,
// authenticating with the sandbox's proxy token and enforcing the live task's
// namespace scope before delegating to p.Namespace.ServeHTTP.
func (p *GitProxy) ServeNamespaceHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Namespace == nil {
		http.Error(w, "namespace not configured", http.StatusNotFound)
		return
	}
	sandbox, ok := p.authenticateSandbox(w, r)
	if !ok {
		return
	}

	if !p.nsLimiter.acquire(sandbox, p.MaxNamespaceConcurrency) {
		http.Error(w, "too many concurrent /fs/ requests from this sandbox", http.StatusTooManyRequests)
		return
	}
	defer p.nsLimiter.release(sandbox)

	if p.NamespaceScopes == nil {
		http.Error(w, "forbidden: no namespace scope configured", http.StatusForbidden)
		return
	}
	modelScope, live, err := p.NamespaceScopes.SandboxNamespaceScope(r.Context(), sandbox)
	if err != nil {
		http.Error(w, "failed to resolve sandbox namespace scope", http.StatusInternalServerError)
		return
	}
	if !live {
		http.Error(w, "forbidden: no live task run on this sandbox", http.StatusForbidden)
		return
	}

	scope := toNamespaceScope(modelScope)
	act := NamespaceRouteActivity(r.URL.Path, r.URL.Query())
	p.noteActivity(r.Context(), modelScope.TaskID, act.Phrase)
	cw := &countingResponseWriter{ResponseWriter: w}
	p.Namespace.ServeHTTP(cw, r, scope)
	p.recordToolCall(r.Context(), modelScope.RunID, act.Tool, cw)
}

func toNamespaceScope(modelScope model.NamespaceScope) namespace.Scope {
	taskID, _ := strconv.ParseInt(modelScope.TaskID, 10, 64)
	return namespace.Scope{
		TaskID:            taskID,
		TaskIDStr:         modelScope.TaskID,
		RunID:             modelScope.RunID,
		TaskRepo:          modelScope.TaskRepo,
		TaskBranch:        modelScope.TaskBranch,
		TaskBase:          modelScope.TaskBase,
		ConfigRepo:        modelScope.ConfigRepo,
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
	if isCLIPath(path) {
		p.ServeCLIHTTP(rec, req)
	} else {
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
