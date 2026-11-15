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
	"compress/gzip"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	frontendLegacy = "legacy"
	frontendNext   = "next"

	// private rather than public because every frontend asset is served behind
	// the deployment's sign-in gate and must not be stored by shared caches.
	cacheControlImmutable = "private, max-age=31536000, immutable"
	cacheControlNoCache   = "no-cache"
	cacheControlNoStore   = "no-store"
)

var hashedAssetExtensions = map[string]bool{
	".js":    true,
	".css":   true,
	".woff2": true,
	".woff":  true,
	".ttf":   true,
	".svg":   true,
	".png":   true,
	".jpg":   true,
	".webp":  true,
}

var (
	// ui-ng/angular.json configures @angular/build:application with
	// outputHashing: "all" and flat browser output (browser: ""), emitting
	// 8-character uppercase base32 hashes on root bundles and media/<name>-<HASH>.<ext>.
	nextBundlePattern = regexp.MustCompile(`^(?:main|polyfills|chunk|styles|scripts|worker)-[A-Z0-9]{8}\.(?:js|css)$`)
	nextMediaPattern  = regexp.MustCompile(`^media/[A-Za-z0-9_][A-Za-z0-9._~@()-]*-[A-Z0-9]{8}\.[a-z0-9]+$`)

	// Legacy React+Vite (ui/vite.config.js) emits all hashed entry, chunk,
	// and asset files flat under assets/<name>-<hash>.<ext> with Rollup's
	// 8-character base64url hash (or Vite's 8-character hex hash).
	legacyAssetPattern = regexp.MustCompile(`^assets/[A-Za-z0-9_][A-Za-z0-9._~@()-]*-[A-Za-z0-9_-]{8}\.[a-z0-9]+$`)
)

// isContentHashedAsset reports whether name (a slash-separated path relative
// to the embedded frontend root) matches a content-hashed bundle or asset
// emitted by the given frontend's build pipeline.
func isContentHashedAsset(frontend, name string) bool {
	if !fs.ValidPath(name) || name == "." || name == "index.html" || strings.ContainsAny(name, "\\?#") {
		return false
	}
	ext := path.Ext(name)
	if !hashedAssetExtensions[ext] {
		return false
	}
	switch frontend {
	case frontendNext:
		return nextBundlePattern.MatchString(name) || nextMediaPattern.MatchString(name)
	case frontendLegacy:
		return legacyAssetPattern.MatchString(name) && hasAlnumHash(name, ext)
	default:
		return false
	}
}

// hasAlnumHash ensures an 8-character base64url hash segment contains at least
// one alphanumeric character so repeated punctuation (such as --------) does
// not classify as a content hash.
func hasAlnumHash(name, ext string) bool {
	stem := name[:len(name)-len(ext)]
	if len(stem) < 8 {
		return false
	}
	hash := stem[len(stem)-8:]
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return true
		}
	}
	return false
}

// isAssetPath reports whether upath's final path segment has a static asset
// extension (the content-hashed allowlist or .map) so missing asset requests
// return 404 rather than falling back to index.html.
func isAssetPath(upath string) bool {
	seg := upath
	if i := strings.LastIndexByte(upath, '/'); i >= 0 {
		seg = upath[i+1:]
	}
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	ext := strings.ToLower(path.Ext(seg))
	return hashedAssetExtensions[ext] || ext == ".map"
}

type cachedBundle struct {
	once        sync.Once
	raw         []byte
	contentType string
	strongETag  string
	weakETag    string
	err         error

	gzipOnce sync.Once
	gz       []byte
}

type frontendAssets struct {
	static           fs.FS
	staticETags      map[string]string
	staticNext       fs.FS
	staticNextETags  map[string]string
	handler          http.Handler
	bundles          sync.Map // clean request path ("/next/<name>" or "/assets/<name>") -> *cachedBundle
	gzipComputations atomic.Int64
}

var defaultFrontendAssets = sync.OnceValue(func() *frontendAssets {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		// staticFS is embedded at build time from a directory this package
		// ships -- its absence is a build-time mistake, not a runtime
		// condition a caller can do anything about.
		panic("ui: embedding static/: " + err.Error())
	}
	staticNext, err := fs.Sub(staticNextFS, "static-next")
	if err != nil {
		panic("ui: embedding static-next/: " + err.Error())
	}
	return newFrontendAssets(static, staticNext)
})

func newFrontendAssets(static, staticNext fs.FS) *frontendAssets {
	return &frontendAssets{
		static:          static,
		staticETags:     precomputeAssetETags(static),
		staticNext:      staticNext,
		staticNextETags: precomputeAssetETags(staticNext),
		handler:         frontendHandler(static, staticNext),
	}
}

func (s *Server) setFrontendFS(static, staticNext fs.FS) {
	s.frontend.Store(newFrontendAssets(static, staticNext))
}

// serveHashedFrontendBundle serves content-hashed embedded frontend bundles
// directly without identity verification or the access-code gate when r
// satisfies all six exact-match exemption conditions (authentication.design.md).
// Those bundles are immutable build output of this open-source repository with
// no deployment data; index.html, the SPA fallback, unhashed assets, and every
// API route stay gated.
func (s *Server) serveHashedFrontendBundle(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || r == nil || r.URL == nil {
		return false
	}
	// 1. Method is GET or HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// 2. Not a hub tunnel request.
	if isHubTunnelRequest(r) {
		return false
	}
	// 3. Empty query string.
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || r.URL.RawFragment != "" {
		return false
	}
	// 4. Raw escaped path contains no percent-escapes, no "//", no "." or ".."
	// segments, no trailing "/", and equals path.Clean of itself.
	rawPath := r.URL.EscapedPath()
	if rawPath == "" || rawPath != r.URL.Path || r.URL.RawPath != "" {
		return false
	}
	if strings.ContainsAny(r.RequestURI, "%?#\\") {
		return false
	}
	if !strings.HasPrefix(rawPath, "/") || strings.HasSuffix(rawPath, "/") ||
		strings.ContainsAny(rawPath, "%?#\\") || strings.Contains(rawPath, "//") ||
		path.Clean(rawPath) != rawPath {
		return false
	}
	parts := strings.Split(rawPath[1:], "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	// 5. Path is exactly /next/<name> (Angular UI) or /assets/<name> (legacy UI)
	// with a single segment <name>.
	if len(parts) != 2 {
		return false
	}
	fa := s.frontend.Load()
	if fa == nil {
		return false
	}
	prefix, name := parts[0], parts[1]
	var (
		frontend string
		fsys     fs.FS
		etags    map[string]string
		relPath  string
	)
	switch prefix {
	case "next":
		frontend = frontendNext
		fsys = fa.staticNext
		etags = fa.staticNextETags
		relPath = name
	case "assets":
		frontend = frontendLegacy
		fsys = fa.static
		etags = fa.staticETags
		relPath = "assets/" + name
	default:
		return false
	}
	// 6. <name> exists as a regular file in the same embedded filesystem that
	// serves it, and isContentHashedAsset accepts it. Never index.html, never
	// the SPA fallback, never directories, never .map files.
	if name == "index.html" || strings.HasSuffix(strings.ToLower(name), ".map") {
		return false
	}
	if fsys == nil || !isContentHashedAsset(frontend, relPath) {
		return false
	}
	info, err := fs.Stat(fsys, relPath)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	fa.serveExemptBundle(w, r, rawPath, fsys, relPath, etags[relPath])
	return true
}

// serveExemptBundle serves an exempt content-hashed bundle from a lazy
// per-file cache keyed by rawPath, ignoring Range headers (always 200 with the
// full body) and caching the gzipped representation on first gzip request so
// unauthenticated callers cannot trigger repeated per-request compression or
// multipart range work.
func (fa *frontendAssets) serveExemptBundle(w http.ResponseWriter, r *http.Request, rawPath string, fsys fs.FS, relPath, precomputedETag string) {
	v, _ := fa.bundles.LoadOrStore(rawPath, &cachedBundle{})
	bundle := v.(*cachedBundle)
	bundle.once.Do(func() {
		data, readErr := fs.ReadFile(fsys, relPath)
		if readErr != nil {
			bundle.err = readErr
			return
		}
		ct := mime.TypeByExtension(path.Ext(relPath))
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		etag := precomputedETag
		if etag == "" {
			etag = etagOf(data)
		}
		bundle.raw = data
		bundle.contentType = ct
		bundle.strongETag = etag
		if strings.HasPrefix(etag, "W/") {
			bundle.weakETag = etag
		} else {
			bundle.weakETag = "W/" + etag
		}
	})
	if bundle.err != nil {
		w.Header().Set("Cache-Control", cacheControlNoStore)
		http.Error(w, "reading embedded asset", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", bundle.contentType)
	h.Set("Cache-Control", cacheControlImmutable)

	body := bundle.raw
	etag := bundle.strongETag
	if acceptsGzip(r) {
		bundle.gzipOnce.Do(func() {
			var buf bytes.Buffer
			gz := gzipWriters.Get().(*gzip.Writer)
			gz.Reset(&buf)
			_, _ = gz.Write(bundle.raw)
			_ = gz.Close()
			gzipWriters.Put(gz)
			bundle.gz = bytes.Clone(buf.Bytes())
			fa.gzipComputations.Add(1)
		})
		body = bundle.gz
		etag = bundle.weakETag
		h.Set("Content-Encoding", "gzip")
		setVaryAcceptEncoding(h)
	}
	if etag != "" {
		h.Set("ETag", etag)
	}
	if r.Header.Get("Range") == "" && etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func setVaryAcceptEncoding(h http.Header) {
	for _, v := range h.Values("Vary") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

// frontendHandler serves both embedded frontends from the one "/"
// catch-all: the Angular UI in staticNext under /next/ (a bare /next
// redirects to /next/), and the React UI in static everywhere else.
// Both sit behind the same ServeHTTP -- security headers, CSRF, the
// access-code gate and verified-identity admission (with exact-match
// content-hashed /next/<name> and /assets/<name> bundles served ahead
// of verifyCallerNow by serveHashedFrontendBundle) -- so the new UI has
// no authentication path of its own: it is the same origin, the same
// session cookie and the same /api/ as the old one.
func frontendHandler(static, staticNext fs.FS) http.Handler {
	legacy := spaHandler(frontendLegacy, static)
	next := http.StripPrefix("/next", spaHandler(frontendNext, staticNext))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			target := "/next/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/next/") {
			next.ServeHTTP(w, r)
			return
		}
		legacy.ServeHTTP(w, r)
	})
}

// spaHandler serves static assets straight out of fsys with appropriate
// Cache-Control and strong ETag headers. Content-hashed bundles get
// private, max-age=31536000, immutable; index.html (served directly or as the
// SPA fallback) and unhashed static files get no-cache plus a startup-computed
// strong ETag; missing asset-like paths answer 404 with no-store rather than
// falling back to index.html.
//
// A //go:embed'd static/ with no built frontend yet (a fresh checkout
// under `go test`, where static/ carries only placeholder.html) has no
// index.html to fall back to; that case answers 404 with no-store rather than
// serving placeholder.html in its place.
func spaHandler(frontend string, fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	etags := precomputeAssetETags(fsys)
	index, indexErr := fs.ReadFile(fsys, "index.html")
	indexETag := etags["index.html"]
	if indexErr == nil && indexETag == "" {
		indexETag = etagOf(index)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if (upath == "" || upath == "index.html") && indexErr == nil {
			serveIndex(w, r, index, indexETag)
			return
		}
		if upath != "" {
			info, err := fs.Stat(fsys, upath)
			if err != nil {
				if isAssetPath(upath) || indexErr != nil {
					w.Header().Set("Cache-Control", cacheControlNoStore)
					http.NotFound(w, r)
					return
				}
				serveIndex(w, r, index, indexETag)
				return
			}
			if !info.IsDir() {
				cc := cacheControlNoCache
				if isContentHashedAsset(frontend, upath) {
					cc = cacheControlImmutable
				}
				serveEmbeddedFile(w, r, fsys, upath, cc, etags[upath])
				return
			}
			w.Header().Set("Cache-Control", cacheControlNoCache)
			fileServer.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", cacheControlNoStore)
		fileServer.ServeHTTP(w, r)
	})
}

func precomputeAssetETags(fsys fs.FS) map[string]string {
	etags := map[string]string{}
	if fsys == nil {
		return etags
	}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr == nil {
			etags[p] = etagOf(data)
		}
		return nil
	})
	return etags
}

func serveIndex(w http.ResponseWriter, r *http.Request, index []byte, etag string) {
	w.Header().Set("Cache-Control", cacheControlNoCache)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}

func serveEmbeddedFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, upath, cacheControl, etag string) {
	f, err := fsys.Open(upath)
	if err != nil {
		w.Header().Set("Cache-Control", cacheControlNoStore)
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, readErr := io.ReadAll(f)
		if readErr != nil {
			w.Header().Set("Cache-Control", cacheControlNoStore)
			http.Error(w, "reading embedded asset", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(data)
	}
	w.Header().Set("Cache-Control", cacheControl)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	http.ServeContent(w, r, upath, time.Time{}, rs)
}
