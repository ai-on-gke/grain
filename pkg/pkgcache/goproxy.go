package pkgcache

// The Go module half: serving both transparent requests for proxy.golang.org
// and sum.golang.org (intercepted via DNS) and legacy /go/ paths.
//
// What is stored is the per-version objects -- `@v/v1.2.3.info`,
// `.mod` and `.zip`, and the checksum database's tiles. Those are
// immutable. Queries whose answers change (@v/list, @latest) are
// forwarded every time and never stored.

import (
	"fmt"
	"net/http"
	"path"
	"strings"
)

// goModulePrefix is the legacy tree this service serves the module proxy under.
const goModulePrefix = "/go/"

// DefaultGoProxy is the module proxy proxied when a deployment names no
// other -- the one the go command would have used itself.
const DefaultGoProxy = "https://proxy.golang.org"

// DefaultGoSumDBHosts is the checksum databases this service will fetch from.
var DefaultGoSumDBHosts = []string{"sum.golang.org"}

// goSumDBPrefix is where the module proxy protocol puts the checksum database.
const goSumDBPrefix = "sumdb/"

// serveGoModule proxies (and where it can, caches) one legacy module proxy request under /go/.
func (s *Server) serveGoModule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), goModulePrefix)
	if rest == "" {
		s.refuse(w, r, http.StatusNotFound,
			"grain's package cache serves the Go module proxy protocol under /go/. Set GOPROXY to that address rather than fetching from it by hand.")
		return
	}
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	if sumdb, ok := strings.CutPrefix(rest, goSumDBPrefix); ok {
		s.serveGoSumDB(w, r, sumdb)
		return
	}
	upstream := strings.TrimSuffix(s.goProxy(), "/") + "/" + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindGo, url: upstream, cacheable: goCacheable(rest)})
}

// serveTransparentGoProxy proxies direct requests against proxy.golang.org.
func (s *Server) serveTransparentGoProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if rest == "" {
		s.refuse(w, r, http.StatusNotFound, "grain's package cache serves Go modules. Direct requests require a module path.")
		return
	}
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.goProxy(), "/") + "/" + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindGo, url: upstream, cacheable: goCacheable(rest)})
}

// serveTransparentGoSumDB proxies direct requests against sum.golang.org.
func (s *Server) serveTransparentGoSumDB(w http.ResponseWriter, r *http.Request, host string) {
	filePath := strings.TrimPrefix(r.URL.Path, "/")
	if filePath == "supported" {
		w.Header().Set(cacheStatusHeader, "pass")
		w.WriteHeader(http.StatusOK)
		return
	}
	scheme := "https"
	if s.fileScheme != "" {
		scheme = s.fileScheme
	}
	upstream := scheme + "://" + host + "/" + filePath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindGo, url: upstream, cacheable: strings.HasPrefix(filePath, "tile/")})
}

// serveGoSumDB answers the checksum-database half under /go/sumdb/.
func (s *Server) serveGoSumDB(w http.ResponseWriter, r *http.Request, rest string) {
	host, filePath, ok := strings.Cut(rest, "/")
	if !ok || host == "" {
		s.refuse(w, r, http.StatusBadRequest,
			"grain's package cache serves a checksum database at /go/sumdb/<database>/<path>.")
		return
	}
	if !hostAllowed(host, s.goSumDBHosts()) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch checksum-database material from %s: it serves %s, so that a "+
				"sandbox with no network of its own cannot use it to reach the rest of the internet.",
			host, strings.Join(s.goSumDBHosts(), ", ")))
		return
	}
	if filePath == "supported" {
		w.Header().Set(cacheStatusHeader, "pass")
		w.WriteHeader(http.StatusOK)
		return
	}
	scheme := "https"
	if s.fileScheme != "" {
		scheme = s.fileScheme
	}
	upstream := scheme + "://" + host + "/" + filePath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindGo, url: upstream, cacheable: strings.HasPrefix(filePath, "tile/")})
}

// goCacheable reports whether a module proxy path names something that
// cannot change under us.
func goCacheable(modulePath string) bool {
	i := strings.LastIndex(modulePath, "/@v/")
	if i < 0 {
		return false
	}
	name := modulePath[i+len("/@v/"):]
	if strings.Contains(name, "/") {
		return false
	}
	ext := path.Ext(name)
	switch ext {
	case ".info", ".mod", ".zip":
	default:
		return false
	}
	return isSemverish(strings.TrimSuffix(name, ext))
}

func isSemverish(version string) bool {
	if len(version) < 2 || version[0] != 'v' || version[1] < '0' || version[1] > '9' {
		return false
	}
	return strings.Contains(version, ".")
}

func (s *Server) goSumDBHosts() []string {
	if len(s.goSumDBList) > 0 {
		return s.goSumDBList
	}
	return DefaultGoSumDBHosts
}

func (s *Server) goProxy() string {
	if s.goProxyURL != "" {
		return s.goProxyURL
	}
	return DefaultGoProxy
}

func goProxyValue(host string) string {
	return fmt.Sprintf("http://%s%s,direct", host, strings.TrimSuffix(goModulePrefix, "/"))
}
