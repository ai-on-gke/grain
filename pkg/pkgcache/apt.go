package pkgcache

// The apt half: this service acting both as a transparent cache for
// intercepted distribution archive domains and as an ordinary HTTP proxy.
//
// What is stored is what is under a pool: a .deb is named after the
// exact source version and architecture it was built from and is never
// rewritten in place, so a cached copy is either the right bytes or not
// there. The indexes beside them -- InRelease, Packages.gz, the
// signatures over both -- are forwarded every time and never stored,
// because they are precisely the files that change when the archive
// does, and because apt verifies them against its keyring anyway: a
// cache that served yesterday's InRelease would either be ignored (apt
// re-checks Date and Valid-Until) or would hide a security update, and
// neither is worth the 10MB it would save.

import (
	"fmt"
	"net/http"
	"path"
	"strings"
)

// serveAPT answers one absolute-form proxy request from apt.
func (s *Server) serveAPT(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.URL.Scheme, "http") && !strings.EqualFold(r.URL.Scheme, "https") {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf(
			"grain's package cache proxies HTTP and HTTPS archives; it was asked for %s://%s, which it cannot cache.",
			r.URL.Scheme, r.URL.Host))
		return
	}
	if !hostAllowed(r.URL.Host, s.aptHosts) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch from %s: it proxies distribution archives (%s) and nothing else, "+
				"so that a sandbox with no network of its own cannot use it to reach the rest of the internet. "+
				"Fetch this repository directly, or turn this cache off in Settings -> Sandbox.",
			r.URL.Host, strings.Join(s.aptHosts, ", ")))
		return
	}
	upstream := r.URL.String()
	if base := s.aptUpstream[hostKey(r.URL.Host)]; base != "" {
		upstream = strings.TrimSuffix(base, "/") + r.URL.RequestURI()
	}
	s.serve(w, r, object{kind: KindAPT, url: upstream, cacheable: aptCacheable(r.URL.Path)})
}

// serveTransparentAPT answers one host-routed HTTP or HTTPS request from apt or curl for a distribution archive.
func (s *Server) serveTransparentAPT(w http.ResponseWriter, r *http.Request, host string) {
	if !hostAllowed(host, s.aptHosts) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch from %s: it proxies distribution archives (%s) and nothing else, "+
				"so that a sandbox with no network of its own cannot use it to reach the rest of the internet. "+
				"Fetch this repository directly, or turn this cache off in Settings -> Sandbox.",
			host, strings.Join(s.aptHosts, ", ")))
		return
	}
	if strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := ""
	if base := s.aptUpstream[hostKey(host)]; base != "" {
		upstream = strings.TrimSuffix(base, "/") + r.URL.RequestURI()
	} else {
		scheme := "http"
		if s.aptScheme != "" {
			scheme = s.aptScheme
		} else if r.TLS != nil && !strings.Contains(host, "debian.org") && !strings.Contains(host, "ubuntu.com") {
			scheme = "https"
		}
		upstream = scheme + "://" + host + r.URL.RequestURI()
	}
	s.serve(w, r, object{kind: KindAPT, url: upstream, cacheable: aptCacheable(r.URL.Path)})
}

// aptCacheable reports whether an archive path names an immutable
// package file rather than an index over them.
//
// Two rules, both conservative: the file is under a pool/ directory, or
// it is named like a package. Anything this does not recognize is
// forwarded uncached, which costs a refetch and can never serve a stale
// byte -- the right way round for a rule about a file this service
// cannot revalidate.
func aptCacheable(urlPath string) bool {
	if strings.Contains(urlPath, "/pool/") {
		return true
	}
	switch strings.ToLower(path.Ext(urlPath)) {
	case ".deb", ".udeb", ".ddeb", ".apk":
		return true
	}
	return false
}
