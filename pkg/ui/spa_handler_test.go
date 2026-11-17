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

// spaHandler itself, in isolation from a real embedded build -- the
// external ui_test package's own TestStaticFrontendIsServed only ever
// sees pkg/ui/static as checked in (placeholder.html, no index.html),
// so it can't exercise the fallback-to-index.html branch a real `npm
// run build` output takes. An in-memory fstest.MapFS stands in for that
// real build here instead.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestIsContentHashedAsset(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		frontend string
		path     string
		want     bool
	}{
		// Real build filenames from Angular (ui-ng/angular.json + esbuild).
		{name: "next main bundle", frontend: frontendNext, path: "main-UCKK7PSP.js", want: true},
		{name: "next polyfills bundle", frontend: frontendNext, path: "polyfills-5CFQRCPP.js", want: true},
		{name: "next styles bundle", frontend: frontendNext, path: "styles-P323WRHF.css", want: true},
		{name: "next chunk bundle", frontend: frontendNext, path: "chunk-OMVX5472.js", want: true},
		{name: "next chunk with leading digit hash", frontend: frontendNext, path: "chunk-22PSM45W.js", want: true},
		{name: "next lazy css chunk", frontend: frontendNext, path: "chunk-7NFLRDLT.css", want: true},
		{name: "next scripts bundle", frontend: frontendNext, path: "scripts-AB12CD34.js", want: true},
		{name: "next worker bundle", frontend: frontendNext, path: "worker-EF56GH78.js", want: true},
		{name: "next media woff2", frontend: frontendNext, path: "media/inter-latin-ABCD1234.woff2", want: true},
		{name: "next media woff", frontend: frontendNext, path: "media/inter-ABCD1234.woff", want: true},
		{name: "next media ttf", frontend: frontendNext, path: "media/mono-ABCD1234.ttf", want: true},
		{name: "next media svg", frontend: frontendNext, path: "media/logo-ABCD1234.svg", want: true},
		{name: "next media png", frontend: frontendNext, path: "media/hero-ABCD1234.png", want: true},
		{name: "next media jpg", frontend: frontendNext, path: "media/hero-ABCD1234.jpg", want: true},
		{name: "next media webp", frontend: frontendNext, path: "media/hero-ABCD1234.webp", want: true},

		// Real build filenames from legacy React+Vite (ui/vite.config.js).
		{name: "legacy index css", frontend: frontendLegacy, path: "assets/index-CL8eXieL.css", want: true},
		{name: "legacy index js with hyphen in hash", frontend: frontendLegacy, path: "assets/index-co56kW-3.js", want: true},
		{name: "legacy chunk with double hyphen", frontend: frontendLegacy, path: "assets/chunk-VQ5W62QI--YVN4Y3G.js", want: true},
		{name: "legacy chunk with triple hyphen in hash", frontend: frontendLegacy, path: "assets/ebnfDiagram-V4P3TTPT-Pmg---kV.js", want: true},
		{name: "legacy chunk with trailing hyphen in hash", frontend: frontendLegacy, path: "assets/diagram-I5XIIV2D-BGn5Wub-.js", want: true},
		{name: "legacy chunk with underscore in hash", frontend: frontendLegacy, path: "assets/ishikawaDiagram-HPEHV2GE-Cu_UFz0p.js", want: true},
		{name: "legacy dotted chunk name", frontend: frontendLegacy, path: "assets/cytoscape.esm-BLAcgzS2.js", want: true},
		{name: "legacy font woff2", frontend: frontendLegacy, path: "assets/roboto-AbCd1234.woff2", want: true},
		{name: "legacy font woff", frontend: frontendLegacy, path: "assets/roboto-AbCd1234.woff", want: true},
		{name: "legacy font ttf", frontend: frontendLegacy, path: "assets/roboto-AbCd1234.ttf", want: true},
		{name: "legacy image svg", frontend: frontendLegacy, path: "assets/mark-AbCd1234.svg", want: true},
		{name: "legacy image png", frontend: frontendLegacy, path: "assets/mark-AbCd1234.png", want: true},
		{name: "legacy image jpg", frontend: frontendLegacy, path: "assets/mark-AbCd1234.jpg", want: true},
		{name: "legacy image webp", frontend: frontendLegacy, path: "assets/mark-AbCd1234.webp", want: true},

		// Unhashed static assets and cross-frontend mismatches.
		{name: "next unhashed public png", frontend: frontendNext, path: "apple-touch-icon.png", want: false},
		{name: "next unhashed favicon svg", frontend: frontendNext, path: "grain-favicon-dark.svg", want: false},
		{name: "next unhashed grain mark png", frontend: frontendNext, path: "grain-mark-32.png", want: false},
		{name: "next licenses txt", frontend: frontendNext, path: "3rdpartylicenses.txt", want: false},
		{name: "next legacy layout under next", frontend: frontendNext, path: "assets/index-co56kW-3.js", want: false},
		{name: "legacy unhashed app js", frontend: frontendLegacy, path: "assets/app.js", want: false},
		{name: "legacy unhashed root svg", frontend: frontendLegacy, path: "grain-mark-dark.svg", want: false},
		{name: "legacy next layout under legacy", frontend: frontendLegacy, path: "main-UCKK7PSP.js", want: false},
		{name: "unknown frontend", frontend: "other", path: "main-UCKK7PSP.js", want: false},
		{name: "empty frontend", frontend: "", path: "assets/index-co56kW-3.js", want: false},

		// Required near-misses and strict rejections.
		{name: "empty hash next", frontend: frontendNext, path: "chunk-.js", want: false},
		{name: "empty hash legacy", frontend: frontendLegacy, path: "assets/chunk-.js", want: false},
		{name: "source map next", frontend: frontendNext, path: "chunk-abc.js.map", want: false},
		{name: "source map full hash next", frontend: frontendNext, path: "chunk-OMVX5472.js.map", want: false},
		{name: "source map legacy", frontend: frontendLegacy, path: "assets/chunk-abc.js.map", want: false},
		{name: "unhashed main.js next", frontend: frontendNext, path: "main.js", want: false},
		{name: "unhashed main.js legacy", frontend: frontendLegacy, path: "assets/main.js", want: false},
		{name: "index.html next", frontend: frontendNext, path: "index.html", want: false},
		{name: "index.html legacy", frontend: frontendLegacy, path: "index.html", want: false},
		{name: "hashed html rejected", frontend: frontendNext, path: "main-UCKK7PSP.html", want: false},
		{name: "traversal chunk-X.js", frontend: frontendNext, path: "../chunk-X.js", want: false},
		{name: "traversal full hash next", frontend: frontendNext, path: "../chunk-OMVX5472.js", want: false},
		{name: "traversal inside legacy", frontend: frontendLegacy, path: "assets/../index-co56kW-3.js", want: false},
		{name: "leading slash next", frontend: frontendNext, path: "/main-UCKK7PSP.js", want: false},
		{name: "nested dir legacy", frontend: frontendLegacy, path: "assets/sub/index-co56kW-3.js", want: false},
		{name: "uppercase extension next js", frontend: frontendNext, path: "chunk-OMVX5472.JS", want: false},
		{name: "uppercase extension next css", frontend: frontendNext, path: "styles-P323WRHF.CSS", want: false},
		{name: "uppercase extension legacy", frontend: frontendLegacy, path: "assets/index-co56kW-3.JS", want: false},
		{name: "uppercase extension media", frontend: frontendNext, path: "media/inter-ABCD1234.WOFF2", want: false},
		{name: "directory name assets", frontend: frontendLegacy, path: "assets", want: false},
		{name: "directory trailing slash", frontend: frontendLegacy, path: "assets/", want: false},
		{name: "directory shaped hashed next", frontend: frontendNext, path: "chunk-OMVX5472.js/", want: false},
		{name: "directory shaped hashed legacy", frontend: frontendLegacy, path: "assets/index-co56kW-3.js/", want: false},
		{name: "dot path", frontend: frontendNext, path: ".", want: false},
		{name: "empty path", frontend: frontendNext, path: "", want: false},
		{name: "short hash next", frontend: frontendNext, path: "chunk-X.js", want: false},
		{name: "short hash legacy", frontend: frontendLegacy, path: "assets/chunk-abc.js", want: false},
		{name: "lowercase hash next rejected", frontend: frontendNext, path: "styles-darkmode.css", want: false},
		{name: "unknown bundle prefix next", frontend: frontendNext, path: "other-UCKK7PSP.js", want: false},
		{name: "punctuation-only hash legacy", frontend: frontendLegacy, path: "assets/foo---------.js", want: false},
		{name: "underscore-only hash legacy", frontend: frontendLegacy, path: "assets/foo-________.js", want: false},
		{name: "query string rejected", frontend: frontendNext, path: "main-UCKK7PSP.js?v=1", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isContentHashedAsset(tc.frontend, tc.path); got != tc.want {
				t.Fatalf("isContentHashedAsset(%q, %q) = %v, want %v", tc.frontend, tc.path, got, tc.want)
			}
		})
	}
}

func TestSpaHandlerServesRealFilesDirectly(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>root</html>")},
		"assets/app.js": {Data: []byte("console.log('hi')")},
	}
	h := spaHandler(frontendLegacy, fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "console.log('hi')" {
		t.Fatalf("body = %q, want the real asset content", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache for unhashed asset", got)
	}
	if got := rec.Header().Get("ETag"); !isStrongQuotedETag(got) {
		t.Fatalf("ETag = %q, want a strong quoted ETag", got)
	}
}

func TestSpaHandlerFallsBackToIndexForAnUnknownPath(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>root</html>")},
	}
	h := spaHandler(frontendLegacy, fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/repos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>root</html>" {
		t.Fatalf("body = %q, want index.html's content", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("ETag"); !isStrongQuotedETag(got) {
		t.Fatalf("ETag = %q, want a strong quoted ETag", got)
	}
}

func TestSpaHandlerWithNoIndexFallsThroughTo404(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"placeholder.html": {Data: []byte("placeholder")},
	}
	h := spaHandler(frontendLegacy, fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/repos", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 -- no index.html to fall back to", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on 404", got)
	}
}

func TestFrontendHandlerCacheHeadersETagAndMissingAssets(t *testing.T) {
	t.Parallel()
	legacy := fstest.MapFS{
		"index.html":                     {Data: []byte("<html>legacy</html>")},
		"grain-mark-32.png":              {Data: []byte("png-bytes")},
		"assets/app.js":                  {Data: []byte("unhashed-js")},
		"assets/index-co56kW-3.js":       {Data: []byte("legacy-bundle-js")},
		"assets/index-CL8eXieL.css":      {Data: []byte("legacy-bundle-css")},
		"assets/dir-12345678.js":         {Mode: fs.ModeDir | 0o755},
		"assets/dir-12345678.js/keep.js": {Data: []byte("nested")},
	}
	next := fstest.MapFS{
		"index.html":             {Data: []byte("<html>next</html>")},
		"grain-favicon-dark.svg": {Data: []byte("<svg/>")},
		"main-UCKK7PSP.js":       {Data: []byte("next-main-js")},
		"polyfills-5CFQRCPP.js":  {Data: []byte("next-polyfills-js")},
		"chunk-OMVX5472.js":      {Data: []byte("next-chunk-js")},
		"styles-P323WRHF.css":    {Data: []byte("next-styles-css")},
	}
	h := frontendHandler(legacy, next)

	// Content-hashed assets receive private immutable Cache-Control.
	for _, p := range []string{
		"/assets/index-co56kW-3.js",
		"/assets/index-CL8eXieL.css",
		"/next/main-UCKK7PSP.js",
		"/next/polyfills-5CFQRCPP.js",
		"/next/chunk-OMVX5472.js",
		"/next/styles-P323WRHF.css",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
			t.Fatalf("GET %s: Cache-Control = %q, want private, max-age=31536000, immutable", p, got)
		}
	}

	// index.html (direct and SPA fallback) and unhashed static assets get
	// no-cache + strong ETag, and honour If-None-Match with 304.
	for _, tc := range []struct {
		path     string
		wantBody string
	}{
		{path: "/", wantBody: "<html>legacy</html>"},
		{path: "/index.html", wantBody: "<html>legacy</html>"},
		{path: "/inbox", wantBody: "<html>legacy</html>"},
		{path: "/grain-mark-32.png", wantBody: "png-bytes"},
		{path: "/assets/app.js", wantBody: "unhashed-js"},
		{path: "/next/", wantBody: "<html>next</html>"},
		{path: "/next/index.html", wantBody: "<html>next</html>"},
		{path: "/next/inbox", wantBody: "<html>next</html>"},
		{path: "/next/tasks/42", wantBody: "<html>next</html>"},
		{path: "/next/repos/acme/my.repo", wantBody: "<html>next</html>"},
		{path: "/next/grain-favicon-dark.svg", wantBody: "<svg/>"},
	} {
		t.Run("etag_"+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200", tc.path, rec.Code)
			}
			if rec.Body.String() != tc.wantBody {
				t.Fatalf("GET %s: body = %q, want %q", tc.path, rec.Body.String(), tc.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("GET %s: Cache-Control = %q, want no-cache", tc.path, got)
			}
			etag := rec.Header().Get("ETag")
			if !isStrongQuotedETag(etag) {
				t.Fatalf("GET %s: ETag = %q, want strong quoted ETag", tc.path, etag)
			}

			// Exact If-None-Match and weak W/ If-None-Match (as browsers send after gzip).
			for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
				condReq := httptest.NewRequest(http.MethodGet, tc.path, nil)
				condReq.Header.Set("If-None-Match", inm)
				condRec := httptest.NewRecorder()
				h.ServeHTTP(condRec, condReq)
				if condRec.Code != http.StatusNotModified {
					t.Fatalf("GET %s (If-None-Match=%s): status = %d, want 304", tc.path, inm, condRec.Code)
				}
				if condRec.Body.Len() != 0 {
					t.Fatalf("GET %s (If-None-Match=%s): 304 response carried body %q", tc.path, inm, condRec.Body.String())
				}
				if got := condRec.Header().Get("Cache-Control"); got != "no-cache" {
					t.Fatalf("GET %s (If-None-Match=%s): Cache-Control = %q, want no-cache", tc.path, inm, got)
				}
			}

			// Non-matching If-None-Match returns 200 with full body.
			missReq := httptest.NewRequest(http.MethodGet, tc.path, nil)
			missReq.Header.Set("If-None-Match", `"stale-etag"`)
			missRec := httptest.NewRecorder()
			h.ServeHTTP(missRec, missReq)
			if missRec.Code != http.StatusOK || missRec.Body.String() != tc.wantBody {
				t.Fatalf("GET %s with stale ETag: status=%d body=%q, want 200 %q", tc.path, missRec.Code, missRec.Body.String(), tc.wantBody)
			}
		})
	}

	// Missing asset-like paths return 404 with Cache-Control: no-store.
	for _, missing := range []string{
		"/next/chunk-ZZZZZZZZ.js",
		"/next/styles-MISSING1.css",
		"/next/chunk-abc.js.map",
		"/next/missing.woff2",
		"/next/missing.woff",
		"/next/missing.ttf",
		"/next/missing.svg",
		"/next/missing.png",
		"/next/missing.jpg",
		"/next/missing.webp",
		"/next/MISSING.JS",
		"/assets/missing-12345678.js",
		"/assets/missing.js.map",
		"/missing.css",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, missing, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", missing, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s: Cache-Control = %q, want no-store", missing, got)
		}
	}

	// A directory whose name looks like a hashed asset never gets immutable.
	dirRec := httptest.NewRecorder()
	h.ServeHTTP(dirRec, httptest.NewRequest(http.MethodGet, "/assets/dir-12345678.js", nil))
	if strings.Contains(dirRec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("directory GET /assets/dir-12345678.js got Cache-Control %q, must not be immutable", dirRec.Header().Get("Cache-Control"))
	}
}

func isStrongQuotedETag(etag string) bool {
	return len(etag) >= 3 && strings.HasPrefix(etag, `"`) && strings.HasSuffix(etag, `"`) && !strings.HasPrefix(etag, `W/`)
}

func TestSpaHandlerWithCompressionETag(t *testing.T) {
	t.Parallel()
	bigHTML := "<html><body>" + strings.Repeat("x", 2048) + "</body></html>"
	h := spaHandler(frontendNext, fstest.MapFS{
		"index.html": {Data: []byte(bigHTML)},
	})

	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	withCompression(rec, req, h.ServeHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("compressed ETag = %q, want weak W/\"...\"", etag)
	}

	condReq := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	condReq.Header.Set("Accept-Encoding", "gzip")
	condReq.Header.Set("If-None-Match", etag)
	condRec := httptest.NewRecorder()
	withCompression(condRec, condReq, h.ServeHTTP)

	if condRec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET with %s: status = %d, want 304", etag, condRec.Code)
	}
	if condRec.Body.Len() != 0 {
		t.Fatalf("304 body length = %d, want 0", condRec.Body.Len())
	}
	if got := condRec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("304 Cache-Control = %q, want no-cache", got)
	}
}

func TestEmbeddedStaticAssetsClassificationAndHeaders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		embedFS  fs.FS
		sub      string
		frontend string
		prefix   string
	}{
		{name: "legacy", embedFS: staticFS, sub: "static", frontend: frontendLegacy, prefix: "/"},
		{name: "next", embedFS: staticNextFS, sub: "static-next", frontend: frontendNext, prefix: "/next/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := fs.Sub(tc.embedFS, tc.sub)
			if err != nil {
				t.Fatal(err)
			}
			h := spaHandler(tc.frontend, sub)

			// Even on an unbuilt checkout, placeholder.html exists and must get
			// no-cache + strong ETag (never immutable).
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/placeholder.html", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /placeholder.html: status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("GET /placeholder.html: Cache-Control = %q, want no-cache", got)
			}
			if got := rec.Header().Get("ETag"); !isStrongQuotedETag(got) {
				t.Fatalf("GET /placeholder.html: ETag = %q, want strong quoted ETag", got)
			}

			// When built (`make frontend`), verify every emitted file gets the
			// right classification and Cache-Control header.
			if _, err := fs.Stat(sub, "index.html"); err != nil {
				return
			}
			err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() {
					return walkErr
				}
				r := httptest.NewRecorder()
				h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/"+p, nil))
				if r.Code != http.StatusOK {
					t.Fatalf("GET /%s: status = %d, want 200", p, r.Code)
				}
				cc := r.Header().Get("Cache-Control")
				wantImmutable := isContentHashedAsset(tc.frontend, p)
				if tc.frontend == frontendLegacy && strings.HasPrefix(p, "assets/") && !wantImmutable {
					t.Errorf("legacy built asset %q was not classified as content-hashed", p)
				}
				if tc.frontend == frontendNext && (strings.HasPrefix(p, "main-") || strings.HasPrefix(p, "polyfills-") || strings.HasPrefix(p, "chunk-") || strings.HasPrefix(p, "styles-")) && !wantImmutable {
					t.Errorf("next built bundle %q was not classified as content-hashed", p)
				}
				if wantImmutable {
					if cc != "private, max-age=31536000, immutable" {
						t.Errorf("GET /%s: Cache-Control = %q, want immutable", p, cc)
					}
				} else {
					if cc != "no-cache" {
						t.Errorf("GET /%s: Cache-Control = %q, want no-cache", p, cc)
					}
					if etag := r.Header().Get("ETag"); !isStrongQuotedETag(etag) {
						t.Errorf("GET /%s: ETag = %q, want strong quoted ETag", p, etag)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
