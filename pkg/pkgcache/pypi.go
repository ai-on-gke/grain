package pkgcache

// The PyPI half: serving both transparent requests for pypi.org and
// files.pythonhosted.org (intercepted via DNS) and legacy /pypi/simple/
// and /pypi/files/ paths.
//
// Index pages are never stored, for the reason apt's are not: they are
// the half that changes, a new release is exactly what somebody
// installing is usually after, and they are small.
//
// Integrity is not weakened by any of this. A simple-index link carries
// the artifact's hash in its fragment (#sha256=...), the rewrite
// preserves the fragment untouched, and pip checks the file it receives
// against it. A wheel served from this cache that is not the wheel PyPI
// published fails in pip with a hash mismatch rather than being installed.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	// pypiSimplePrefix is the index this service serves, PEP 503 shaped,
	// and is what pip's index-url names.
	pypiSimplePrefix = "/pypi/simple/"
	// pypiFilesPrefix is where rewritten download links point. The
	// upstream host is the first path segment under it, so that the
	// rewrite is reversible without this service having to remember
	// anything about a link it handed out.
	pypiFilesPrefix = "/pypi/files/"
)

// DefaultPyPIIndex is the index proxied when a deployment names no
// other -- the real one.
const DefaultPyPIIndex = "https://pypi.org/simple"

// DefaultPyPIFileHosts is where an artifact may be fetched from: the
// hosts PyPI's own index links to.
var DefaultPyPIFileHosts = []string{"files.pythonhosted.org", ".pythonhosted.org", "pypi.org", "pypi.python.org"}

// maxIndexBytes bounds an index page read into memory to be rewritten.
const maxIndexBytes = 64 << 20

// servePyPIIndex proxies one simple-index page, with its download links
// rewritten to come back through this service.
func (s *Server) servePyPIIndex(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(pypiSimplePrefix, "/"))
	if strings.Contains(rest, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.pypiIndex(), "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.fetchAndServePyPIIndex(w, r, upstream)
}

// serveTransparentPyPIIndex proxies one simple-index page for transparent requests against pypi.org.
func (s *Server) serveTransparentPyPIIndex(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/simple")
	if strings.Contains(rest, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.pypiIndex(), "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.fetchAndServePyPIIndex(w, r, upstream)
}

func (s *Server) fetchAndServePyPIIndex(w http.ResponseWriter, r *http.Request, upstream string) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, nil)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache cannot fetch %s: %v", upstream, err))
		return
	}
	copyRequestHeaders(req, r)
	resp, err := s.client.Do(req)
	if err != nil {
		s.logf("package cache: fetching the package index %s: %v", upstream, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not reach the Python package index at %s: %v\n", upstream, err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
	if err != nil {
		s.logf("package cache: reading the package index %s: %v", upstream, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not read the Python package index at %s: %v\n", upstream, err)
		return
	}
	if resp.StatusCode == http.StatusOK {
		base, err := url.Parse(upstream)
		if err == nil {
			body = s.rewriteIndex(body, resp.Header.Get("Content-Type"), base)
		}
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set(cacheStatusHeader, "pass")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

// servePyPIFile proxies (and caches) one artifact, addressed by the
// upstream host and path the rewritten link carried.
func (s *Server) servePyPIFile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, pypiFilesPrefix)
	host, filePath, ok := strings.Cut(rest, "/")
	if !ok || host == "" || strings.Contains(rest, "..") {
		s.refuse(w, r, http.StatusBadRequest,
			"grain's package cache serves Python artifacts at /pypi/files/<host>/<path>.")
		return
	}
	if !hostAllowed(host, s.pypiFileHosts()) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch Python artifacts from %s: it serves the hosts the package index it "+
				"proxies links to (%s), so that a sandbox with no network of its own cannot use it to reach the rest "+
				"of the internet.", host, strings.Join(s.pypiFileHosts(), ", ")))
		return
	}
	upstream := s.fileScheme + "://" + host + "/" + filePath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindPyPI, url: upstream, cacheable: pypiCacheable(filePath)})
}

// serveTransparentPyPIFile proxies (and caches) one artifact addressed directly by host (e.g. files.pythonhosted.org).
func (s *Server) serveTransparentPyPIFile(w http.ResponseWriter, r *http.Request, host string) {
	if strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	if !hostAllowed(host, s.pypiFileHosts()) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch Python artifacts from %s: it serves the hosts the package index it "+
				"proxies links to (%s), so that a sandbox with no network of its own cannot use it to reach the rest "+
				"of the internet.", host, strings.Join(s.pypiFileHosts(), ", ")))
		return
	}
	upstream := s.fileScheme + "://" + host + r.URL.RequestURI()
	s.serve(w, r, object{kind: KindPyPI, url: upstream, cacheable: pypiCacheable(r.URL.Path)})
}

// pypiCacheable reports whether a path under a file host names a
// published artifact -- the immutable kind -- rather than anything else
// that host might serve.
func pypiCacheable(filePath string) bool {
	lower := strings.ToLower(filePath)
	for _, suffix := range []string{".whl", ".tar.gz", ".zip", ".tar.bz2", ".tar.xz", ".egg", ".exe", ".msi"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

var hrefPattern = regexp.MustCompile(`(?i)href\s*=\s*("([^"]*)"|'([^']*)')`)

func (s *Server) rewriteIndex(body []byte, contentType string, base *url.URL) []byte {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return s.rewriteIndexJSON(body, base)
	}
	return hrefPattern.ReplaceAllFunc(body, func(match []byte) []byte {
		groups := hrefPattern.FindSubmatch(match)
		quoted := string(groups[2])
		quote := `"`
		if len(groups[3]) > 0 || (len(groups[2]) == 0 && strings.Contains(string(groups[1]), "'")) {
			quoted, quote = string(groups[3]), `'`
		}
		rewritten := s.rewriteLink(base, html.UnescapeString(quoted))
		if rewritten == "" {
			return match
		}
		return []byte("href=" + quote + html.EscapeString(rewritten) + quote)
	})
}

func (s *Server) rewriteIndexJSON(body []byte, base *url.URL) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	files, ok := doc["files"].([]any)
	if !ok {
		return body
	}
	changed := false
	for _, f := range files {
		file, ok := f.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := file["url"].(string)
		if !ok {
			continue
		}
		if rewritten := s.rewriteLink(base, raw); rewritten != "" {
			file["url"] = rewritten
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func (s *Server) rewriteLink(base *url.URL, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	link, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(link)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	if !hostAllowed(abs.Host, s.pypiFileHosts()) {
		return ""
	}
	out := pypiFilesPrefix + abs.Host + "/" + strings.TrimPrefix(abs.Path, "/")
	if abs.RawQuery != "" {
		out += "?" + abs.RawQuery
	}
	if abs.Fragment != "" {
		out += "#" + abs.EscapedFragment()
	}
	return out
}

func (s *Server) pypiIndex() string {
	if s.pypiIndexURL != "" {
		return s.pypiIndexURL
	}
	return DefaultPyPIIndex
}

func (s *Server) pypiFileHosts() []string {
	if len(s.pypiHosts) > 0 {
		return s.pypiHosts
	}
	return DefaultPyPIFileHosts
}
