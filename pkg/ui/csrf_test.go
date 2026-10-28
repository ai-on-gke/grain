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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestCrossOriginStateChangeRefused pins #2371: a POST a browser sends
// from another site -- the text/plain, no-preflight shape a <form> or a
// bare fetch() can fire -- is refused before any handler decodes it,
// while the same request from the UI's own origin, and from a client
// that is not a browser at all, still reaches the route.
func TestCrossOriginStateChangeRefused(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("initializing store: %v", err)
	}
	srv := NewServer(Config{Actor: DefaultActor("alice")}, store)
	body := `{"name":"csrf-team"}`

	cases := []struct {
		name    string
		headers map[string]string
		refused bool
	}{
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"same-site but other origin", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"foreign Origin without fetch metadata", map[string]string{"Origin": "https://evil.example"}, true},
		{"same-origin fetch metadata", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, false},
		{"Origin matching Host", map[string]string{"Origin": "http://example.com"}, false},
		{"non-browser client", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://example.com/api/deployment/git-connections", strings.NewReader(body))
			r.Header.Set("Content-Type", "text/plain")
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if tc.refused && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
			}
			if !tc.refused && w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "cross-origin") {
				t.Fatalf("refused a request that is not cross-origin: %s", w.Body.String())
			}
		})
	}

	// A cross-site GET is a read and is left alone.
	r := httptest.NewRequest(http.MethodGet, "http://example.com/api/config", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("cross-site GET refused: %s", w.Body.String())
	}
}
