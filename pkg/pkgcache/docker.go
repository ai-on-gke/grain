package pkgcache

// The Docker Hub and OCI container registry half: serving both transparent
// requests for registry-1.docker.io, ghcr.io, gcr.io (intercepted via DNS)
// and legacy /v2/ mirror paths.
//
// Manifests asked for by tag are forwarded every time and never stored.
// Blobs, and manifests asked for by digest, are stored by digest (key
// "blob:<digest>"), so identical layers across repositories and registries
// are cached once. Because that key names no registry, a body is only
// stored once its bytes have been checked against the digest it is filed
// under (see digestHash and Server.serve): otherwise one upstream or mirror
// answering with the wrong bytes would poison that digest for every
// registry.

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// dockerAPIPrefix is the registry API's root.
const dockerAPIPrefix = "/v2/"

// DefaultDockerRegistry is the default registry proxied for Docker Hub.
const DefaultDockerRegistry = "https://registry-1.docker.io"

// DefaultGHCRRegistry is GitHub Container Registry's API endpoint.
const DefaultGHCRRegistry = "https://ghcr.io"

// DefaultGCRRegistry is Google Container Registry's API endpoint.
const DefaultGCRRegistry = "https://gcr.io"

const (
	dockerTokenTTL = 60 * time.Second
)

// parseDockerTarget identifies the upstream registry URL, client, and subpath
// from a request path and query under /v2/.
func (s *Server) parseDockerTarget(path string, query url.Values) (registryURL string, client *http.Client, subpath string, isPing bool) {
	rest := strings.TrimPrefix(path, strings.TrimSuffix(dockerAPIPrefix, "/"))
	trimmed := strings.Trim(rest, "/")

	switch {
	case trimmed == "ghcr" || strings.HasPrefix(trimmed, "ghcr/"):
		registryURL = s.ghcrRegistry()
		client = s.ghcrClient
		subpath = strings.TrimPrefix(trimmed, "ghcr")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "ghcr.io" || strings.HasPrefix(trimmed, "ghcr.io/"):
		registryURL = s.ghcrRegistry()
		client = s.ghcrClient
		subpath = strings.TrimPrefix(trimmed, "ghcr.io")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "gcr" || strings.HasPrefix(trimmed, "gcr/"):
		registryURL = s.gcrRegistry()
		client = s.gcrClient
		subpath = strings.TrimPrefix(trimmed, "gcr")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "gcr.io" || strings.HasPrefix(trimmed, "gcr.io/"):
		registryURL = s.gcrRegistry()
		client = s.gcrClient
		subpath = strings.TrimPrefix(trimmed, "gcr.io")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "quay" || strings.HasPrefix(trimmed, "quay/"):
		registryURL, client = s.ociClient("quay.io")
		subpath = strings.TrimPrefix(trimmed, "quay")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "quay.io" || strings.HasPrefix(trimmed, "quay.io/"):
		registryURL, client = s.ociClient("quay.io")
		subpath = strings.TrimPrefix(trimmed, "quay.io")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "registry.k8s.io" || strings.HasPrefix(trimmed, "registry.k8s.io/"):
		registryURL, client = s.ociClient("registry.k8s.io")
		subpath = strings.TrimPrefix(trimmed, "registry.k8s.io")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "k8s.gcr.io" || strings.HasPrefix(trimmed, "k8s.gcr.io/"):
		registryURL, client = s.ociClient("k8s.gcr.io")
		subpath = strings.TrimPrefix(trimmed, "k8s.gcr.io")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "docker" || strings.HasPrefix(trimmed, "docker/"):
		registryURL = s.dockerRegistry()
		client = s.dockerClient
		subpath = strings.TrimPrefix(trimmed, "docker")
		subpath = strings.TrimPrefix(subpath, "/")
	case trimmed == "docker.io" || strings.HasPrefix(trimmed, "docker.io/"):
		registryURL = s.dockerRegistry()
		client = s.dockerClient
		subpath = strings.TrimPrefix(trimmed, "docker.io")
		subpath = strings.TrimPrefix(subpath, "/")
	default:
		switch query.Get("ns") {
		case "ghcr.io":
			registryURL = s.ghcrRegistry()
			client = s.ghcrClient
			subpath = trimmed
		case "gcr.io":
			registryURL = s.gcrRegistry()
			client = s.gcrClient
			subpath = trimmed
		default:
			registryURL = s.dockerRegistry()
			client = s.dockerClient
			subpath = trimmed
		}
	}

	if subpath == "" {
		return registryURL, client, "", true
	}
	return registryURL, client, subpath, false
}

// serveDocker answers one request under /v2/ from a dockerd that has
// been pointed here as a registry mirror.
func (s *Server) serveDocker(w http.ResponseWriter, r *http.Request) {
	registryURL, client, subpath, isPing := s.parseDockerTarget(r.URL.Path, r.URL.Query())
	if isPing {
		s.dockerPing(w)
		return
	}
	if strings.Contains(subpath, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}

	upstream := strings.TrimSuffix(registryURL, "/") + dockerAPIPrefix + subpath
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(upstream, "/") {
		upstream += "/"
	}
	q := r.URL.Query()
	q.Del("ns")
	if encoded := q.Encode(); encoded != "" {
		upstream += "?" + encoded
	}

	if isDockerAuthEndpoint(subpath, q) || isDockerWriteMethod(r.Method) {
		s.proxyDocker(w, r, upstream)
		return
	}

	_, section, ref, ok := splitDockerRef(subpath)
	if !ok {
		s.refuse(w, r, http.StatusNotFound,
			"grain's package cache mirrors OCI registries for image pulls: it answers /v2/, /v2/<name>/manifests/<ref>, "+
				"/v2/<name>/blobs/<digest>, /v2/<name>/referrers/<ref> and /v2/<name>/tags/<ref>, and nothing else. "+
				"dockerd falls back to the registry itself for anything a mirror does not serve.")
		return
	}

	var fallbackURL string
	var fallbackClient *http.Client
	if registryURL == s.dockerRegistry() || strings.Contains(registryURL, "registry-1.docker.io") || strings.Contains(registryURL, "docker.io") {
		fallbackURL = s.dockerScheme() + "://mirror.gcr.io" + dockerAPIPrefix + subpath
		if encoded := q.Encode(); encoded != "" {
			fallbackURL += "?" + encoded
		}
		_, fallbackClient = s.ociClient("mirror.gcr.io")
	}

	if (section == "blobs" || section == "manifests") && dockerDigest(ref) {
		obj := object{kind: KindDocker, url: upstream, client: client, cacheable: true, key: "blob:" + ref, digest: ref, fallbackURL: fallbackURL, fallbackClient: fallbackClient}
		s.serve(w, r, obj)
		return
	}

	obj := object{kind: KindDocker, url: upstream, client: client, cacheable: false, fallbackURL: fallbackURL, fallbackClient: fallbackClient}
	s.serve(w, r, obj)
}

// serveDockerDirect answers a direct request against an intercepted OCI registry domain.
func (s *Server) serveDockerDirect(w http.ResponseWriter, r *http.Request, registryURL string, client *http.Client) {
	path := r.URL.Path
	if strings.Contains(path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}

	upstream := strings.TrimSuffix(registryURL, "/") + "/" + strings.TrimPrefix(path, "/")
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	if isDockerAuthEndpoint(path, r.URL.Query()) {
		s.proxyDocker(w, r, upstream)
		return
	}

	if !strings.HasPrefix(path, "/v2") {
		s.refuse(w, r, http.StatusNotFound, "grain's package cache mirrors OCI registries: requests must start with /v2/.")
		return
	}
	trimmed := strings.Trim(strings.TrimPrefix(path, "/v2"), "/")
	if trimmed == "" {
		s.dockerPing(w)
		return
	}

	if isDockerDirectWrite(r.Method, trimmed) {
		s.proxyDocker(w, r, upstream)
		return
	}

	var fallbackURL string
	var fallbackClient *http.Client
	if registryURL == s.dockerRegistry() || strings.Contains(registryURL, "registry-1.docker.io") || strings.Contains(registryURL, "docker.io") {
		fallbackURL = s.dockerScheme() + "://mirror.gcr.io/" + strings.TrimPrefix(path, "/")
		if r.URL.RawQuery != "" {
			fallbackURL += "?" + r.URL.RawQuery
		}
		_, fallbackClient = s.ociClient("mirror.gcr.io")
	}

	_, section, ref, ok := splitDockerRef(trimmed)
	if ok && (section == "blobs" || section == "manifests") && dockerDigest(ref) {
		obj := object{kind: KindDocker, url: upstream, client: client, cacheable: true, key: "blob:" + ref, digest: ref, fallbackURL: fallbackURL, fallbackClient: fallbackClient}
		s.serve(w, r, obj)
		return
	}

	obj := object{kind: KindDocker, url: upstream, client: client, cacheable: false, fallbackURL: fallbackURL, fallbackClient: fallbackClient}
	s.serve(w, r, obj)
}

// isDockerAuthEndpoint reports whether a request path and query parameters correspond
// to an OCI registry authentication or token issuance endpoint.
func isDockerAuthEndpoint(path string, query url.Values) bool {
	p := strings.Trim(path, "/")
	switch p {
	case "token", "auth", "auth/token", "oauth/token", "oauth2/token", "oauth/v2/token", "v2/token", "v2/auth", "v1/token":
		return true
	}
	if strings.HasSuffix(p, "/token") || strings.HasSuffix(p, "/auth") {
		return true
	}
	if query.Get("service") != "" || query.Get("scope") != "" || query.Get("account") != "" || query.Get("client_id") != "" || query.Get("grant_type") != "" {
		return true
	}
	return false
}

// dockerRepoName extracts the repository name from an OCI path.
func dockerRepoName(path string) string {
	trimmed := strings.Trim(path, "/")
	for _, section := range []string{"manifests", "blobs", "referrers", "tags"} {
		marker := "/" + section + "/"
		if i := strings.Index(trimmed, marker); i > 0 {
			return trimmed[:i]
		}
		if strings.HasSuffix(trimmed, "/"+section) {
			return strings.TrimSuffix(trimmed, "/"+section)
		}
	}
	return ""
}

// isDockerWriteMethod reports whether an HTTP method is a mutating write operation.
func isDockerWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isDockerDirectWrite reports whether an OCI registry request on an intercepted domain
// represents a write/push operation or an upload session endpoint.
func isDockerDirectWrite(method, subpath string) bool {
	if isDockerWriteMethod(method) {
		return true
	}
	if strings.Contains(subpath, "blobs/uploads") || strings.Contains(subpath, "blobs/upload") {
		return true
	}
	return false
}

// copyProxyRequestHeaders copies client request headers to the proxied upstream request,
// excluding hop-by-hop headers.
func copyProxyRequestHeaders(dst *http.Request, src *http.Request) {
	for name, values := range src.Header {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		for _, v := range values {
			dst.Header.Add(name, v)
		}
	}
}

// proxyDocker streams an uncacheable/write OCI registry request directly to the upstream registry.
func (s *Server) proxyDocker(w http.ResponseWriter, r *http.Request, upstreamURL string) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache cannot proxy %s: %v", upstreamURL, err))
		return
	}
	req.ContentLength = r.ContentLength
	copyProxyRequestHeaders(req, r)
	if u, err := url.Parse(upstreamURL); err == nil && u.Host != "" {
		req.Host = u.Host
	}

	if req.Header.Get("Authorization") == "" && s.credentials != nil {
		s.autoAuthDocker(req, r, upstreamURL)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logf("package cache: proxying %s %s: %v", r.Method, upstreamURL, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not reach %s: %v\n", upstreamURL, err)
		return
	}
	defer resp.Body.Close()

	// If GHCR returned 403 Forbidden without a Www-Authenticate header
	// (GHCR returns 403 denied: invalid token instead of 401 Www-Authenticate on write/push requests
	// when unauthenticated, when insufficient/pull-only scope is sent, or when basic auth credentials are sent directly to /v2/),
	// translate this to a 401 challenge with Www-Authenticate so OCI clients (like docker daemon)
	// can perform OAuth2/token authentication against /token and retry with a Bearer token.
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Www-Authenticate") == "" {
		if u, err := url.Parse(upstreamURL); err == nil && (s.isGHCRRegistry(u.Host) || s.isGHCRRegistry(r.Host)) {
			owner, repo, ok := extractDockerRepo(r.URL.Path, r.URL.Query())
			if !ok {
				owner, repo, ok = extractDockerRepo(u.Path, u.Query())
			}
			if ok {
				w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:%s/%s:pull,push"`, owner, repo))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set(cacheStatusHeader, "pass")
				w.WriteHeader(http.StatusUnauthorized)
				if r.Method != http.MethodHead {
					io.WriteString(w, "{\"errors\":[{\"code\":\"UNAUTHORIZED\",\"message\":\"authentication required\"}]}\n")
				}
				return
			}
		}
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set(cacheStatusHeader, "pass")
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		io.Copy(w, resp.Body)
	}
}

// isGHCRRegistry reports whether host matches GitHub Container Registry.
func (s *Server) isGHCRRegistry(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "ghcr.io" {
		return true
	}
	ghcrHost := hostOnly(s.ghcrRegistry())
	if ghcrHost != "" && host == ghcrHost {
		return true
	}
	defaultHost := hostOnly(DefaultGHCRRegistry)
	if defaultHost != "" && host == defaultHost {
		return true
	}
	return false
}

// extractRepoFromScope extracts owner and repo from OCI scope parameter(s).
func extractRepoFromScope(query url.Values) (owner, repo string, ok bool) {
	if query == nil {
		return "", "", false
	}
	for _, scope := range query["scope"] {
		for _, part := range strings.Fields(scope) {
			if strings.HasPrefix(part, "repository:") {
				rest := strings.TrimPrefix(part, "repository:")
				rest = strings.TrimPrefix(rest, "//")
				// If prefixed with registry host like ghcr.io/owner/repo, strip the host
				if i := strings.Index(rest, "/"); i > 0 {
					first := rest[:i]
					if strings.Contains(first, ".") || strings.Contains(first, ":") {
						rest = rest[i+1:]
					}
				}
				// Strip action suffix :pull,push etc.
				if i := strings.LastIndex(rest, ":"); i >= 0 {
					rest = rest[:i]
				}
				rest = strings.Trim(rest, "/")
				if o, r, found := strings.Cut(rest, "/"); found && o != "" && r != "" {
					return o, r, true
				}
			}
		}
	}
	return "", "", false
}

// extractRepoFromPath extracts owner and repo from an OCI request path.
func extractRepoFromPath(path string) (owner, repo string, ok bool) {
	p := strings.TrimPrefix(path, "/v2")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "ghcr.io/")
	p = strings.TrimPrefix(p, "ghcr/")
	p = strings.Trim(p, "/")

	repoPath := dockerRepoName(p)
	if repoPath == "" {
		for _, marker := range []string{"/blobs/uploads", "/blobs/upload", "/blobs", "/manifests", "/tags", "/referrers"} {
			if idx := strings.Index(p, marker); idx > 0 {
				repoPath = p[:idx]
				break
			}
		}
	}
	if repoPath == "" {
		parts := strings.Split(p, "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" && parts[0] != "token" && parts[0] != "auth" {
			repoPath = parts[0] + "/" + parts[1]
		}
	}
	repoPath = strings.Trim(repoPath, "/")
	if o, r, found := strings.Cut(repoPath, "/"); found && o != "" && r != "" {
		return o, r, true
	}
	return "", "", false
}

// extractDockerRepo extracts owner and repo from query parameters or path.
func extractDockerRepo(path string, query url.Values) (owner, repo string, ok bool) {
	if query != nil {
		if o, r, ok := extractRepoFromScope(query); ok {
			return o, r, true
		}
	}
	return extractRepoFromPath(path)
}

// autoAuthDocker injects GitHub credentials into an unauthenticated OCI request
// when a matching credential is configured in the credential ladder.
func (s *Server) autoAuthDocker(req *http.Request, origReq *http.Request, upstreamURL string) {
	if s.credentials == nil {
		return
	}
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return
	}

	// Check if the target is GHCR
	isGHCR := s.isGHCRRegistry(u.Host) ||
		s.isGHCRRegistry(origReq.Host) ||
		origReq.URL.Query().Get("service") == "ghcr.io" ||
		origReq.URL.Query().Get("ns") == "ghcr.io" ||
		u.Query().Get("service") == "ghcr.io" ||
		strings.HasPrefix(origReq.URL.Path, "/v2/ghcr") ||
		strings.EqualFold(origReq.Host, "ghcr.io")
	if !isGHCR {
		return
	}

	owner, repo, ok := extractDockerRepo(origReq.URL.Path, origReq.URL.Query())
	if !ok {
		owner, repo, ok = extractDockerRepo(u.Path, u.Query())
	}
	if !ok {
		return
	}

	baseRepo := repo
	if i := strings.Index(repo, "/"); i > 0 {
		baseRepo = repo[:i]
	}
	token, ok := s.credentials.PackageToken(owner, baseRepo)
	if !ok {
		token, ok = s.credentials.PackageToken(owner, repo)
	}
	if !ok || token == "" {
		return
	}

	if isDockerAuthEndpoint(origReq.URL.Path, origReq.URL.Query()) || isDockerAuthEndpoint(u.Path, u.Query()) {
		if !s.allowPush {
			// The scope a token is minted for is this server's to
			// decide, not the caller's: whatever it asked for, the
			// daemon's credential is only offered for a pull of the
			// repository it was selected for. A POST names its scope
			// in a body this cannot rewrite, so it gets nothing.
			if req.Method != http.MethodGet && req.Method != http.MethodHead {
				return
			}
			q := req.URL.Query()
			q.Set("scope", "repository:"+owner+"/"+repo+":pull")
			req.URL.RawQuery = q.Encode()
		}
		req.SetBasicAuth(PackageUsername, token)
		return
	}

	// Anything else that reaches here is a write; the daemon's
	// credential does not authenticate one unless the deployment said so.
	if !s.allowPush {
		return
	}
	if s.ghcrAuth != nil {
		scope := "repository:" + owner + "/" + repo + ":pull,push"
		if bearerToken, err := s.ghcrAuth.BearerToken(req.Context(), scope); err == nil && bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		}
	}
}

// dockerPing answers /v2/, which is how dockerd decides a mirror is a registry.
func (s *Server) dockerPing(w http.ResponseWriter) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(cacheStatusHeader, "pass")
	io.WriteString(w, "{}")
}

// splitDockerRef pulls a registry path apart into the repository, which
// half of the API is being asked for, and the tag or digest.
func splitDockerRef(path string) (name, section, ref string, ok bool) {
	for _, section := range []string{"manifests", "blobs", "referrers", "tags"} {
		marker := "/" + section + "/"
		i := strings.LastIndex(path, marker)
		if i <= 0 {
			continue
		}
		name = strings.Trim(path[:i], "/")
		ref = path[i+len(marker):]
		if name == "" || ref == "" || strings.Contains(ref, "/") {
			return "", "", "", false
		}
		return name, section, ref, true
	}
	return "", "", "", false
}

// dockerDigest reports whether a blob reference is a content digest.
func dockerDigest(ref string) bool {
	algo, hex, ok := strings.Cut(ref, ":")
	if !ok || len(hex) < 32 {
		return false
	}
	switch strings.ToLower(algo) {
	case "sha256", "sha512":
	default:
		return false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// digestHash returns a fresh hash for the algorithm a content digest names,
// or nil if it names none this package can check.
func digestHash(digest string) hash.Hash {
	algo, _, _ := strings.Cut(digest, ":")
	switch strings.ToLower(algo) {
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	}
	return nil
}

// digestMatches reports whether h, having hashed a body, gives the digest
// that body was asked for by.
func digestMatches(digest string, h hash.Hash) bool {
	_, want, _ := strings.Cut(digest, ":")
	return strings.EqualFold(want, hex.EncodeToString(h.Sum(nil)))
}

func (s *Server) dockerRegistry() string {
	if s.dockerRegistryURL != "" {
		return s.dockerRegistryURL
	}
	return DefaultDockerRegistry
}

func (s *Server) ghcrRegistry() string {
	if s.ghcrRegistryURL != "" {
		return s.ghcrRegistryURL
	}
	return DefaultGHCRRegistry
}

func (s *Server) gcrRegistry() string {
	if s.gcrRegistryURL != "" {
		return s.gcrRegistryURL
	}
	return DefaultGCRRegistry
}

type dockerAuth struct {
	base        http.RoundTripper
	host        string
	registryURL string
	client      *http.Client
	credentials RegistryCredentials

	mu     sync.Mutex
	tokens map[string]dockerToken
}

type dockerToken struct {
	token   string
	expires time.Time
}

func (a *dockerAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Host, a.host) {
		return a.base.RoundTrip(req)
	}
	if req.Header.Get("Authorization") != "" {
		return a.base.RoundTrip(req)
	}
	scope := dockerPullScope(req.URL.Path)
	if token := a.cached(scope); token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := parseBearerChallenge(resp.Header.Get("Www-Authenticate"))
	if challenge["realm"] == "" {
		return resp, nil
	}
	// This transport only serves pulls, and fetchToken may attach the
	// daemon's credential: never let the registry's challenge widen the
	// token past pull.
	if challenge["scope"] != "" {
		challenge["scope"] = dockerPullOnlyScope(challenge["scope"])
	}
	token, err := a.fetchToken(req, challenge)
	if err != nil || token == "" {
		return resp, nil
	}
	a.store(scope, token, challenge)
	resp.Body.Close()
	retry := req.Clone(req.Context())
	retry.Header.Set("Authorization", "Bearer "+token)
	return a.base.RoundTrip(retry)
}

func dockerPullScope(path string) string {
	rest := strings.TrimPrefix(path, strings.TrimSuffix(dockerAPIPrefix, "/"))
	name, _, _, ok := splitDockerRef(rest)
	if ok {
		return "repository:" + name + ":pull"
	}
	if name := dockerRepoName(rest); name != "" {
		return "repository:" + name + ":pull"
	}
	return ""
}

// dockerPullOnlyScope narrows every repository scope in a
// space-separated scope list to its pull action and drops anything that
// is not a repository scope at all.
func dockerPullOnlyScope(scope string) string {
	var out []string
	for _, part := range strings.Fields(scope) {
		rest, ok := strings.CutPrefix(part, "repository:")
		if !ok {
			continue
		}
		i := strings.LastIndex(rest, ":")
		if i <= 0 {
			continue
		}
		out = append(out, "repository:"+rest[:i]+":pull")
	}
	return strings.Join(out, " ")
}

func (a *dockerAuth) cached(scope string) string {
	if scope == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	token, ok := a.tokens[scope]
	if !ok || time.Now().After(token.expires) {
		return ""
	}
	return token.token
}

func (a *dockerAuth) store(scope, token string, challenge map[string]string) {
	if scope == "" || challenge["scope"] != "" && challenge["scope"] != scope {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens == nil {
		a.tokens = map[string]dockerToken{}
	}
	a.tokens[scope] = dockerToken{token: token, expires: time.Now().Add(dockerTokenTTL)}
}

func (a *dockerAuth) BearerToken(ctx context.Context, scope string) (string, error) {
	if token := a.cached(scope); token != "" {
		return token, nil
	}
	baseURL := strings.TrimSuffix(a.registryURL, "/")
	if baseURL == "" {
		baseURL = "https://" + a.host
	}
	tokenURL := fmt.Sprintf("%s/token?service=%s&scope=%s", baseURL, a.host, url.QueryEscape(scope))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	token, err := a.fetchToken(req, map[string]string{
		"realm":   baseURL + "/token",
		"service": a.host,
		"scope":   scope,
	})
	if err != nil {
		return "", err
	}
	a.store(scope, token, map[string]string{"scope": scope})
	return token, nil
}

func (a *dockerAuth) fetchToken(req *http.Request, challenge map[string]string) (string, error) {
	realm, err := url.Parse(challenge["realm"])
	if err != nil || (realm.Scheme != "http" && realm.Scheme != "https") {
		return "", fmt.Errorf("pkgcache: the registry pointed at %q for a token, which is not an address to fetch one from", challenge["realm"])
	}
	query := realm.Query()
	if service := challenge["service"]; service != "" {
		query.Set("service", service)
	}
	scope := challenge["scope"]
	if scope == "" {
		scope = dockerPullScope(req.URL.Path)
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	realm.RawQuery = query.Encode()

	tokenReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if a.credentials != nil && (strings.EqualFold(a.host, "ghcr.io") || strings.Contains(strings.ToLower(a.host), "ghcr")) {
		if owner, repo, ok := extractRepoFromScope(query); ok {
			baseRepo := repo
			if i := strings.Index(repo, "/"); i > 0 {
				baseRepo = repo[:i]
			}
			token, ok := a.credentials.PackageToken(owner, baseRepo)
			if !ok {
				token, ok = a.credentials.PackageToken(owner, repo)
			}
			if ok && token != "" {
				tokenReq.SetBasicAuth(PackageUsername, token)
			}
		}
	}
	resp, err := a.client.Do(tokenReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pkgcache: %s answered %s for an anonymous pull token", realm.Host, resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

func parseBearerChallenge(header string) map[string]string {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return nil
	}
	out := map[string]string{}
	for _, part := range splitChallengeParams(header[len("bearer "):]) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return out
}

func splitChallengeParams(s string) []string {
	var out []string
	var current strings.Builder
	quoted := false
	for _, c := range s {
		switch {
		case c == '"':
			quoted = !quoted
			current.WriteRune(c)
		case c == ',' && !quoted:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, current.String())
	}
	return out
}
