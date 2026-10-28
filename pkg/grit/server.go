package grit

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

const (
	maxJSONBodyBytes = 16 << 20  // 16 MiB (for StartRun with secrets)
	maxTreeBodyBytes = 256 << 20 // 256 MiB compressed tree upload
)

// ServerConfig configures grit's `/v1` and `/pkg/v1` HTTP server.
type ServerConfig struct {
	Local *Local
	// AllowUnauthenticated disables token verification (for local embedded testing only).
	AllowUnauthenticated bool
}

// Server serves grit's `/v1` and `/pkg/v1` HTTP APIs over a `*Local` instance.
type Server struct {
	local   *Local
	noAuth  bool
	limiter *clientRateLimiter
	pkgV1   http.Handler
}

// NewServer constructs an HTTP Server for grit's `/v1` and `/pkg/v1` routes.
func NewServer(cfg ServerConfig) *Server {
	return &Server{
		local:   cfg.Local,
		noAuth:  cfg.AllowUnauthenticated,
		limiter: newClientRateLimiter(),
		pkgV1:   pkgcache.NewV1Handler(cfg.Local.pkgAdmin),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/pkg/v1/"):
		if !client.HasScope(gritapi.ScopeCacheAdmin, "*") {
			writeAPIError(w, &gritapi.APIError{
				StatusCode: http.StatusForbidden,
				Code:       "forbidden",
				Message:    "cache-admin scope required",
			})
			return
		}
		s.pkgV1.ServeHTTP(w, r)
		return

	case path == "/v1/info":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeMethodNotAllowed(w, "GET")
			return
		}
		info, err := s.local.Info(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
		return

	case path == "/v1/trees:negotiate":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, "POST")
			return
		}
		if !hasAnyScope(client, gritapi.ScopeSubmit) {
			writeForbidden(w, "submit scope required")
			return
		}
		var req gritapi.NegotiateTreesRequest
		if !readJSON(w, r, &req) {
			return
		}
		missing, err := s.local.NegotiateTrees(r.Context(), req.Trees)
		if err != nil {
			writeError(w, err)
			return
		}
		if missing == nil {
			missing = []string{}
		}
		writeJSON(w, http.StatusOK, gritapi.NegotiateTreesResponse{Missing: missing})
		return

	case strings.HasPrefix(path, "/v1/trees/"):
		if r.Method != http.MethodPut {
			writeMethodNotAllowed(w, "PUT")
			return
		}
		if !hasAnyScope(client, gritapi.ScopeSubmit) {
			writeForbidden(w, "submit scope required")
			return
		}
		shaHex := strings.TrimPrefix(path, "/v1/trees/")
		s.handleUploadTree(w, r, client, shaHex)
		return

	case path == "/v1/runs":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, "POST")
			return
		}
		s.handleStartRun(w, r, client)
		return

	case strings.HasPrefix(path, "/v1/runs/"):
		s.handleRunSubroute(w, r, client, strings.TrimPrefix(path, "/v1/runs/"))
		return

	case path == "/v1/events":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, "GET")
			return
		}
		if !hasAnyScope(client, gritapi.ScopeRead) {
			writeForbidden(w, "read scope required")
			return
		}
		s.handleEvents(w, r, client)
		return

	default:
		writeAPIError(w, &gritapi.APIError{
			StatusCode: http.StatusNotFound,
			Code:       "not_found",
			Message:    "route not found",
		})
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (ClientRecord, bool) {
	id, secret, ok := ExtractClientToken(r)
	if !ok {
		if s.noAuth {
			return ClientRecord{
				ID: "anonymous",
				Scopes: []ClientScope{
					{Scope: gritapi.ScopeSubmit, Repo: "*"},
					{Scope: gritapi.ScopeRead, Repo: "*"},
					{Scope: gritapi.ScopeCancel, Repo: "*"},
					{Scope: gritapi.ScopeCacheAdmin, Repo: "*"},
					{Scope: gritapi.ScopeRelease, Repo: "*"},
				},
			}, true
		}
		writeAPIError(w, &gritapi.APIError{
			StatusCode: http.StatusUnauthorized,
			Code:       "unauthorized",
			Message:    "missing or malformed Grit-Client token",
		})
		return ClientRecord{}, false
	}

	rec, found, err := s.local.Store().GetClient(id)
	if err != nil {
		writeError(w, err)
		return ClientRecord{}, false
	}
	if !found || rec.Revoked || !VerifySecret(secret, rec.SecretSHA256) {
		writeAPIError(w, &gritapi.APIError{
			StatusCode: http.StatusUnauthorized,
			Code:       "unauthorized",
			Message:    "invalid or revoked client token",
		})
		return ClientRecord{}, false
	}
	return rec, true
}

func hasAnyScope(c ClientRecord, want gritapi.Scope) bool {
	if c.Revoked {
		return false
	}
	for _, sc := range c.Scopes {
		if sc.Scope == want {
			return true
		}
	}
	return false
}

func (s *Server) handleUploadTree(w http.ResponseWriter, r *http.Request, client ClientRecord, shaHex string) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxTreeBodyBytes+1))
	if err != nil {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "read_error", Message: err.Error()})
		return
	}
	if int64(len(raw)) > maxTreeBodyBytes {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusRequestEntityTooLarge, Code: "tree_too_large", Message: "tree upload exceeds size limit"})
		return
	}
	if !s.limiter.allowTreeUpload(client, int64(len(raw)), time.Now()) {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusTooManyRequests, Code: "rate_limited", Message: "tree upload rate limit exceeded"})
		return
	}

	archiveBytes := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_gzip", Message: err.Error()})
			return
		}
		archiveBytes, err = io.ReadAll(zr)
		_ = zr.Close()
		if err != nil {
			writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_gzip", Message: err.Error()})
			return
		}
	}

	if err := s.local.UploadTree(r.Context(), shaHex, archiveBytes); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"sha256": strings.ToLower(shaHex)})
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request, client ClientRecord) {
	var req gritapi.StartRun
	if !readJSON(w, r, &req) {
		return
	}
	if !client.HasScope(gritapi.ScopeSubmit, req.Repo) {
		writeForbidden(w, "submit scope required for repo "+req.Repo)
		return
	}
	if req.TrustedRelease && !client.HasScope(gritapi.ScopeRelease, req.Repo) {
		writeForbidden(w, "release scope required for trustedRelease on "+req.Repo)
		return
	}
	if !s.limiter.allowRun(client, time.Now()) {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusTooManyRequests, Code: "rate_limited", Message: "run submission rate limit exceeded"})
		return
	}
	if client.MaxConcurrentJobs > 0 {
		s.local.mu.Lock()
		active := s.local.runningJobs + s.local.queuedJobs
		s.local.mu.Unlock()
		if active >= client.MaxConcurrentJobs {
			writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusTooManyRequests, Code: "quota_exceeded", Message: "max_concurrent_jobs quota reached"})
			return
		}
	}

	status, err := s.local.StartRun(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRunSubroute(w http.ResponseWriter, r *http.Request, client ClientRecord, rest string) {
	parts := strings.Split(rest, "/")
	runID := parts[0]
	if runID == "" {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusNotFound, Code: "not_found"})
		return
	}

	run, err := s.local.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, err)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, "GET")
			return
		}
		if !client.HasScope(gritapi.ScopeRead, run.Repo) {
			writeForbidden(w, "read scope required for repo "+run.Repo)
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}

	switch parts[1] {
	case "cancel":
		if len(parts) != 2 || r.Method != http.MethodPost {
			writeMethodNotAllowed(w, "POST")
			return
		}
		if !client.HasScope(gritapi.ScopeCancel, run.Repo) {
			writeForbidden(w, "cancel scope required for repo "+run.Repo)
			return
		}
		updated, err := s.local.CancelRun(r.Context(), runID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)

	case "retrigger":
		if len(parts) != 2 || r.Method != http.MethodPost {
			writeMethodNotAllowed(w, "POST")
			return
		}
		if !client.HasScope(gritapi.ScopeSubmit, run.Repo) {
			writeForbidden(w, "submit scope required for repo "+run.Repo)
			return
		}
		if run.TrustedRelease && !client.HasScope(gritapi.ScopeRelease, run.Repo) {
			writeForbidden(w, "release scope required for trustedRelease on "+run.Repo)
			return
		}
		var req gritapi.RetriggerRequest
		if !readJSON(w, r, &req) {
			return
		}
		updated, err := s.local.RetriggerRun(r.Context(), runID, req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)

	case "attestation":
		if len(parts) != 2 || r.Method != http.MethodGet {
			writeMethodNotAllowed(w, "GET")
			return
		}
		if !client.HasScope(gritapi.ScopeRead, run.Repo) {
			writeForbidden(w, "read scope required for repo "+run.Repo)
			return
		}
		env, err := s.local.GetAttestation(r.Context(), runID)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.in-toto+json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(env)

	case "logs":
		if len(parts) != 3 || r.Method != http.MethodGet {
			writeMethodNotAllowed(w, "GET")
			return
		}
		if !client.HasScope(gritapi.ScopeRead, run.Repo) {
			writeForbidden(w, "read scope required for repo "+run.Repo)
			return
		}
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
		data, next, comp, err := s.local.GetLog(r.Context(), runID, parts[2], offset, limit)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Grit-Log-Offset", strconv.FormatInt(next, 10))
		if comp {
			w.Header().Set("X-Grit-Log-Complete", "true")
		} else {
			w.Header().Set("X-Grit-Log-Complete", "false")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case "artifacts":
		if len(parts) != 4 || r.Method != http.MethodGet {
			writeMethodNotAllowed(w, "GET")
			return
		}
		if !client.HasScope(gritapi.ScopeRead, run.Repo) {
			writeForbidden(w, "read scope required for repo "+run.Repo)
			return
		}
		data, err := s.local.GetArtifact(r.Context(), runID, parts[2], parts[3])
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	default:
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusNotFound, Code: "not_found"})
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, client ClientRecord) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var wait time.Duration
	if wStr := r.URL.Query().Get("wait"); wStr != "" {
		wait, _ = time.ParseDuration(wStr)
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
	}
	batch, err := s.local.EventsFiltered(r.Context(), after, wait, limit, func(repo string) bool {
		return client.HasScope(gritapi.ScopeRead, repo)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if batch.Events == nil {
		batch.Events = []gritapi.Event{}
	}
	writeJSON(w, http.StatusOK, batch)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "read_error", Message: err.Error()})
		return false
	}
	if int64(len(raw)) > maxJSONBodyBytes {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusRequestEntityTooLarge, Code: "body_too_large"})
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_json", Message: err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
}

func writeForbidden(w http.ResponseWriter, msg string) {
	writeAPIError(w, &gritapi.APIError{StatusCode: http.StatusForbidden, Code: "forbidden", Message: msg})
}

func writeAPIError(w http.ResponseWriter, apiErr *gritapi.APIError) {
	status := apiErr.StatusCode
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, apiErr)
}

func writeError(w http.ResponseWriter, err error) {
	var apiErr *gritapi.APIError
	if errors.As(err, &apiErr) {
		writeAPIError(w, apiErr)
		return
	}
	writeAPIError(w, &gritapi.APIError{
		StatusCode: http.StatusInternalServerError,
		Code:       "internal_error",
		Message:    err.Error(),
	})
}
