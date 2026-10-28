package actionscache

// The HTTP half: the Actions cache API, served to sandboxes.
//
// Every route hangs off a token (see scopes.go), which is resolved to a
// repository before anything reads or writes an entry. A request whose
// token names nothing is a 403 and nothing else happens -- not a 404,
// which would tell a caller whether an entry exists, and not a silent
// 204, which would look to a job like an empty cache and send it
// uploading into a service that will refuse the upload too.
//
// What a client sends is not this service's to trust beyond that: a
// key, a version, a range and a length arrive from inside a sandbox,
// where a workflow file this deployment did not write is running. So
// the id in a URL is checked against the token's repository rather than
// believed, a range is written to a file named after its offset rather
// than seeked to in a shared one, and a committed length that does not
// match what was reserved throws the entry away.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// APIPath is the fixed middle of every route, exactly as GitHub's own
// cache API spells it and therefore as actions/cache and buildkit's
// `gha` backend both build their URLs.
const APIPath = "/_apis/artifactcache"

// Config is what a server needs: the store, the token registry, and
// somewhere to complain to.
type Config struct {
	Cache  *Cache
	Scopes *Scopes
	// Log is the daemon's own log. nil discards.
	Log func(format string, args ...any)
}

// Server is the http.Handler this package serves. Build it with
// NewServer.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// NewServer wires the routes. A server with no cache or no scopes
// refuses everything, which is what a deployment that could not open
// the cache directory should look like from a sandbox.
func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{token}"+APIPath+"/cache", s.find)
	s.mux.HandleFunc("POST /{token}"+APIPath+"/caches", s.reserve)
	s.mux.HandleFunc("PATCH /{token}"+APIPath+"/caches/{id}", s.upload)
	s.mux.HandleFunc("POST /{token}"+APIPath+"/caches/{id}", s.commit)
	s.mux.HandleFunc("GET /{token}"+APIPath+"/artifacts/{id}", s.download)
	// actions/cache asks for this before it gives up on a key. There is
	// nothing to clean -- eviction is this service's own business -- and
	// answering anything but 200 turns a tidy-up into a failed step.
	s.mux.HandleFunc("POST /{token}"+APIPath+"/clean", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.scope(w, r); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) log(format string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log(format, args...)
	}
}

// scope resolves the request's token, answering the repository, ref and
// trust it stands for. It has already written the refusal when it
// answers false.
func (s *Server) scope(w http.ResponseWriter, r *http.Request) (Scope, bool) {
	if s.cfg.Cache == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "this deployment keeps no Actions cache"})
		return Scope{}, false
	}
	scope, ok := s.cfg.Scopes.Scope(r.PathValue("token"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "this cache token is not one of this deployment's live CI jobs"})
		return Scope{}, false
	}
	return scope, true
}

// GET <token>/_apis/artifactcache/cache?keys=&version=
func (s *Server) find(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	keys := strings.Split(r.URL.Query().Get("keys"), ",")
	version := r.URL.Query().Get("version")
	entry, found := s.cfg.Cache.Find(scope, keys, version)
	if !found {
		// 204 is the miss GitHub answers with, and what every client
		// reads as "build it yourself".
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result":          "hit",
		"cacheKey":        entry.Key,
		"archiveLocation": s.archiveLocation(r, entry.ID),
	})
}

// POST <token>/_apis/artifactcache/caches
func (s *Server) reserve(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	var body struct {
		Key     string `json:"key"`
		Version string `json:"version"`
		Size    int64  `json:"cacheSize"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	id, err := s.cfg.Cache.Reserve(scope, body.Key, body.Version, body.Size)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrTooLarge) {
			// The client is expected to carry on uncached from this,
			// which is what both actions/cache and buildkit do with a
			// refused reservation.
			status = http.StatusBadRequest
		}
		s.log("actions cache: %s@%s: reserving %q: %v", scope.Repo, scope.Ref, body.Key, err)
		writeJSON(w, status, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cacheId": id})
}

// PATCH <token>/_apis/artifactcache/caches/<id>
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	id, ok := s.entryID(w, r)
	if !ok {
		return
	}
	start, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if err := s.cfg.Cache.Write(scope, id, start, r.Body); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrNotReserved) || errors.Is(err, ErrTooLarge) {
			status = http.StatusBadRequest
		}
		s.log("actions cache: %s@%s: writing %d bytes into entry %d: %v", scope.Repo, scope.Ref, r.ContentLength, id, err)
		writeJSON(w, status, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// POST <token>/_apis/artifactcache/caches/<id>
func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	id, ok := s.entryID(w, r)
	if !ok {
		return
	}
	size, err := s.cfg.Cache.Commit(scope, id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrNotReserved) || errors.Is(err, ErrTooLarge) {
			status = http.StatusBadRequest
		}
		s.log("actions cache: %s@%s: committing entry %d: %v", scope.Repo, scope.Ref, id, err)
		writeJSON(w, status, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"size": size})
}

// GET <token>/_apis/artifactcache/artifacts/<id>
//
// Served with http.ServeFile rather than copied out by hand, so that a
// client fetching the archive in ranges -- which buildkit does -- gets
// the range handling net/http already has.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	id, ok := s.entryID(w, r)
	if !ok {
		return
	}
	path, found := s.cfg.Cache.Body(scope, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) entryID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": fmt.Sprintf("%q is not a cache entry", r.PathValue("id"))})
		return 0, false
	}
	return id, true
}

// archiveLocation is the absolute URL a client will fetch a hit from.
//
// It is built from the host the request arrived at rather than from a
// configured address, because that host is by definition one the
// sandbox asking can reach -- an advertised address that was right for
// one backend and wrong for another is exactly the bug this avoids.
func (s *Server) archiveLocation(r *http.Request, id uint64) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/%s%s/artifacts/%d", scheme, r.Host, r.PathValue("token"), APIPath, id)
}

// SandboxURL is what a job is handed as ACTIONS_CACHE_URL: the base a
// sandbox reaches this deployment at, the job's own token, and the
// trailing slash every client joins its paths onto.
func SandboxURL(base, token string) string {
	return strings.TrimSuffix(base, "/") + "/" + token + "/"
}

// parseContentRange reads the offset out of an upload's Content-Range.
// Only "bytes <start>-<end>/<total>" is understood, which is the one
// form actions/cache and buildkit send.
func parseContentRange(header string) (int64, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "bytes"))
	raw, _, _ = strings.Cut(raw, "/")
	first, _, ok := strings.Cut(strings.TrimSpace(raw), "-")
	if !ok {
		return 0, fmt.Errorf("actionscache: %q is not a Content-Range this cache understands", header)
	}
	start, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("actionscache: %q is not a Content-Range this cache understands", header)
	}
	if start < 0 {
		return 0, fmt.Errorf("actionscache: %q starts before the beginning", header)
	}
	return start, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
