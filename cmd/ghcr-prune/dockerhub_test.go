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

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDockerHubAPIPrunesDeadManifestTagsAndKeepsSharedCacheDigests(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	var sawLoginUser string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/users/login/":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sawLoginUser = body["username"]
			mu.Unlock()
			if body["password"] != "dckr_pat_secret" {
				http.Error(w, `{"detail":"incorrect credentials"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"hub-jwt-token"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/v2/repositories/grainai/grain/tags/":
			if r.Header.Get("Authorization") != "Bearer hub-jwt-token" {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`{
					"next": "",
					"results": [
						{"name": "sha-0000000", "digest": "sha256:dead0000", "last_updated": "2026-01-01T00:00:00Z"},
						{"name": "dead-branch", "digest": "sha256:dead0000", "last_updated": "2026-01-01T00:00:00Z"}
					]
				}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"next": "` + srv.URL + `/v2/repositories/grainai/grain/tags/?page=2",
				"results": [
					{"name": "main", "digest": "sha256:live1111", "last_updated": "2026-08-31T12:00:00Z"},
					{"name": "sha-1111111", "digest": "sha256:live1111", "last_updated": "2026-08-31T12:00:00Z"},
					{"name": "sha-2222222", "digest": "sha256:live2222", "last_updated": "2026-08-30T12:00:00Z"},
					{"name": "sha-3333333", "digest": "sha256:live3333", "last_updated": "2026-08-29T12:00:00Z"}
				]
			}`))

		case r.Method == http.MethodGet && r.URL.Path == "/v2/repositories/grainai/sandbox/tags/":
			w.Header().Set("Content-Type", "application/json")
			// Old cache manifest shared with live sha-1111111 -> must be KEPT!
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name": "inputs-aaaaaaaaaaaaaaaa", "digest": "sha256:sharedcache", "last_updated": "2026-01-10T00:00:00Z"},
					{"name": "sha-1111111", "digest": "sha256:sharedcache", "last_updated": "2026-08-31T12:00:00Z"}
				]
			}`))

		case r.Method == http.MethodGet && r.URL.Path == "/v2/repositories/grainai/sandbox-base/tags/":
			http.NotFound(w, r)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/repositories/grainai/"):
			if r.Header.Get("Authorization") != "Bearer hub-jwt-token" {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	api := &DockerHubAPI{
		Client:    srv.Client(),
		BaseURL:   srv.URL,
		Namespace: "grainai",
		Username:  "grainai",
		Password:  "dckr_pat_secret",
		BranchLister: func() (map[string]bool, error) {
			return map[string]bool{"main": true}, nil
		},
	}

	var out bytes.Buffer
	p := Policy{KeepFor: 48 * 3600 * 1e9, KeepCommits: 3, KeepInputs: 1, KeepBases: 1, MaxDeletes: 100}
	if err := prune(api, []string{"grain", "sandbox", "sandbox-base"}, p, true, now, &out); err != nil {
		t.Fatalf("prune failed: %v\n%s", err, out.String())
	}

	if sawLoginUser != "grainai" {
		t.Errorf("login user = %q, want grainai", sawLoginUser)
	}
	wantDeleted := []string{
		"/v2/repositories/grainai/grain/tags/dead-branch/",
		"/v2/repositories/grainai/grain/tags/sha-0000000/",
	}
	if strings.Join(deleted, ",") != strings.Join(wantDeleted, ",") {
		t.Errorf("deleted = %v, want %v", deleted, wantDeleted)
	}
}

func TestDockerHubAPIRefusesGracefullyWhenPATLacksDeletePermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/users/login/":
			_, _ = w.Write([]byte(`{"token":"rw-only-jwt"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/repositories/grainai/grain/tags/":
			_, _ = w.Write([]byte(`{
				"next": "",
				"results": [
					{"name": "main", "digest": "sha256:live1", "last_updated": "2026-08-31T12:00:00Z"},
					{"name": "sha-1111111", "digest": "sha256:live1", "last_updated": "2026-08-31T12:00:00Z"},
					{"name": "sha-2222222", "digest": "sha256:live2", "last_updated": "2026-08-30T12:00:00Z"},
					{"name": "sha-3333333", "digest": "sha256:live3", "last_updated": "2026-08-29T12:00:00Z"},
					{"name": "sha-0000000", "digest": "sha256:dead0", "last_updated": "2026-01-01T00:00:00Z"}
				]
			}`))
		case r.Method == http.MethodDelete:
			http.Error(w, `{"detail":"insufficient scope"}`, http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	api := &DockerHubAPI{
		Client:    srv.Client(),
		BaseURL:   srv.URL,
		Namespace: "grainai",
		Username:  "grainai",
		Password:  "rw-pat",
		BranchLister: func() (map[string]bool, error) {
			return map[string]bool{"main": true}, nil
		},
	}

	var out bytes.Buffer
	p := Policy{KeepFor: 48 * 3600 * 1e9, KeepCommits: 3, KeepInputs: 1, KeepBases: 1, MaxDeletes: 100}
	if err := prune(api, []string{"grain"}, p, true, now, &out); err != nil {
		t.Fatalf("expected 403 delete refusal to warn rather than fail, got: %v", err)
	}
	if !strings.Contains(out.String(), "::warning title=ghcr-prune::") || !strings.Contains(out.String(), "Delete permission") {
		t.Errorf("expected warning naming DOCKER_PAT Delete permission, got:\n%s", out.String())
	}
}
