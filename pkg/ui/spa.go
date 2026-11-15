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
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
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

// frontendHandler serves both embedded frontends from the one "/"
// catch-all: the Angular UI in staticNext under /next/ (a bare /next
// redirects to /next/), and the React UI in static everywhere else.
// Both sit behind the same ServeHTTP -- security headers, CSRF, the
// access-code gate and verified-identity admission -- so the new UI has
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
