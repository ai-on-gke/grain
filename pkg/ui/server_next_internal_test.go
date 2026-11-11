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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFrontendHandlerServesNextBesideLegacy pins how the "/" catch-all
// splits between the two embedded frontends: /next/... is the Angular
// UI with its own SPA fallback, a bare /next redirects to /next/ keeping
// its query, and everything else -- including paths that merely start
// with "next" -- is the React UI exactly as before.
func TestFrontendHandlerServesNextBesideLegacy(t *testing.T) {
	legacy := fstest.MapFS{
		"index.html":    {Data: []byte("legacy-index")},
		"assets/app.js": {Data: []byte("legacy-js")},
	}
	next := fstest.MapFS{
		"index.html":     {Data: []byte("next-index")},
		"main-ABC123.js": {Data: []byte("next-js")},
	}
	h := frontendHandler(legacy, next)

	cases := []struct {
		path, wantBody, wantLocation string
		wantStatus                   int
	}{
		{path: "/", wantStatus: 200, wantBody: "legacy-index"},
		{path: "/inbox", wantStatus: 200, wantBody: "legacy-index"},
		{path: "/assets/app.js", wantStatus: 200, wantBody: "legacy-js"},
		{path: "/nextish", wantStatus: 200, wantBody: "legacy-index"},
		{path: "/next/", wantStatus: 200, wantBody: "next-index"},
		{path: "/next/inbox", wantStatus: 200, wantBody: "next-index"},
		{path: "/next/tasks/42", wantStatus: 200, wantBody: "next-index"},
		{path: "/next/main-ABC123.js", wantStatus: 200, wantBody: "next-js"},
		{path: "/next", wantStatus: http.StatusTemporaryRedirect, wantLocation: "/next/"},
		{path: "/next?x=1", wantStatus: http.StatusTemporaryRedirect, wantLocation: "/next/?x=1"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
			if rec.Code != c.wantStatus {
				t.Fatalf("GET %s: status %d, want %d", c.path, rec.Code, c.wantStatus)
			}
			if c.wantLocation != "" {
				if got := rec.Header().Get("Location"); got != c.wantLocation {
					t.Fatalf("GET %s: Location %q, want %q", c.path, got, c.wantLocation)
				}
				return
			}
			body, _ := io.ReadAll(rec.Body)
			if !strings.Contains(string(body), c.wantBody) {
				t.Fatalf("GET %s: body %q, want %q", c.path, body, c.wantBody)
			}
		})
	}
}

// TestFrontendHandlerNextWithoutBuild is a fresh checkout: static-next/
// holds only placeholder.html, so /next/inbox has no index.html to fall
// back to and 404s rather than serving the placeholder in its place.
func TestFrontendHandlerNextWithoutBuild(t *testing.T) {
	next := fstest.MapFS{"placeholder.html": {Data: []byte("not built")}}
	h := frontendHandler(fstest.MapFS{"index.html": {Data: []byte("legacy")}}, next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/next/inbox", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /next/inbox with no build: status %d, want 404", rec.Code)
	}
}
