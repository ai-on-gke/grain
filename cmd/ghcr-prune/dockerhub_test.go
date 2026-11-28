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
	"errors"
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

// fakeOrgHub is a Docker Hub that, like the real one, refuses to log an
// organization in through /v2/users/login/, exchanges only organization
// access tokens through /v2/auth/token, and accepts their bearers only on
// the namespace-scoped repository routes.
type fakeOrgHub struct {
	mu        sync.Mutex
	oat       string
	issued    int
	expireAt  int // the bearer issued this many times is refused once
	userLogin int
	exchanges []map[string]string
	deleted   []string
	refused   bool
}

func (f *fakeOrgHub) bearer() string {
	return "oat-bearer-" + string(rune('0'+f.issued))
}

func (f *fakeOrgHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/users/login/":
		f.userLogin++
		http.Error(w, `{"detail":"Cannot log into an organization account"}`, http.StatusUnauthorized)

	case r.Method == http.MethodPost && r.URL.Path == "/v2/auth/token":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.exchanges = append(f.exchanges, body)
		if body["identifier"] != "grainai" || body["secret"] != f.oat {
			http.Error(w, `{"detail":"incorrect username or password"}`, http.StatusUnauthorized)
			return
		}
		f.issued++
		_, _ = w.Write([]byte(`{"access_token":"` + f.bearer() + `"}`))

	case strings.HasPrefix(r.URL.Path, "/v2/repositories/"):
		http.Error(w, `{"detail":"token issued from organization access token is not allowed"}`, http.StatusForbidden)

	case r.Header.Get("Authorization") != "Bearer "+f.bearer():
		http.Error(w, `{"detail":"unknown bearer"}`, http.StatusUnauthorized)

	case f.expireAt == f.issued && !f.refused:
		f.refused = true
		http.Error(w, `{"detail":"token expired"}`, http.StatusUnauthorized)

	case r.Method == http.MethodGet && r.URL.Path == "/v2/namespaces/grainai/repositories/grain/tags":
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

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/namespaces/grainai/repositories/grain/tags/"):
		f.deleted = append(f.deleted, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.NotFound(w, r)
	}
}

func pruneAgainstOrgHub(t *testing.T, hub *fakeOrgHub, secret string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	api := &DockerHubAPI{
		Client:    srv.Client(),
		BaseURL:   srv.URL,
		Namespace: "grainai",
		Username:  "grainai",
		Password:  secret,
		BranchLister: func() (map[string]bool, error) {
			return map[string]bool{"main": true}, nil
		},
	}
	var out bytes.Buffer
	p := Policy{KeepFor: 48 * 3600 * 1e9, KeepCommits: 3, KeepInputs: 1, KeepBases: 1, MaxDeletes: 100}
	err := prune(api, []string{"grain"}, p, true, now, &out)
	return out.String(), err
}

func TestDockerHubAPIExchangesOrgAccessTokenAndUsesNamespaceRoutes(t *testing.T) {
	hub := &fakeOrgHub{oat: "dckr_oat_orgsecret"}
	out, err := pruneAgainstOrgHub(t, hub, hub.oat)
	if err != nil {
		t.Fatalf("prune with an organization access token failed: %v\n%s", err, out)
	}
	if hub.userLogin != 0 {
		t.Errorf("a dckr_oat_ token was sent to /v2/users/login/ %d times; it should go straight to /v2/auth/token", hub.userLogin)
	}
	if len(hub.exchanges) != 1 {
		t.Fatalf("token exchanges = %v, want exactly one", hub.exchanges)
	}
	want := []string{"/v2/namespaces/grainai/repositories/grain/tags/sha-0000000"}
	if strings.Join(hub.deleted, ",") != strings.Join(want, ",") {
		t.Errorf("deleted = %v, want %v", hub.deleted, want)
	}
}

func TestDockerHubAPIFallsBackToTokenExchangeWhenLoginRefusesOrgAccount(t *testing.T) {
	// An organization access token without the dckr_oat_ prefix is still
	// accepted once /v2/users/login/ says the name is an organization.
	hub := &fakeOrgHub{oat: "legacy-org-token"}
	out, err := pruneAgainstOrgHub(t, hub, hub.oat)
	if err != nil {
		t.Fatalf("prune after the organization-account refusal failed: %v\n%s", err, out)
	}
	if hub.userLogin != 1 || len(hub.exchanges) != 1 {
		t.Errorf("user logins = %d, token exchanges = %d; want 1 and 1", hub.userLogin, len(hub.exchanges))
	}
	if len(hub.deleted) != 1 {
		t.Errorf("deleted = %v, want the one dead tag", hub.deleted)
	}
}

func TestDockerHubAPILogsInAgainWhenTheBearerExpires(t *testing.T) {
	hub := &fakeOrgHub{oat: "dckr_oat_orgsecret", expireAt: 1}
	out, err := pruneAgainstOrgHub(t, hub, hub.oat)
	if err != nil {
		t.Fatalf("prune across an expired bearer failed: %v\n%s", err, out)
	}
	if len(hub.exchanges) != 2 {
		t.Errorf("token exchanges = %d, want 2 (the second after the expiry)", len(hub.exchanges))
	}
	if len(hub.deleted) != 1 {
		t.Errorf("deleted = %v, want the one dead tag", hub.deleted)
	}
}

func TestDockerHubAPINamesTheFixWhenAPersonalTokenLogsInAsTheOrg(t *testing.T) {
	// The failure release.yml hit: vars.DOCKERHUB_USERNAME unset, so the
	// organization name is paired with a user's personal access token.
	hub := &fakeOrgHub{oat: "dckr_oat_orgsecret"}
	out, err := pruneAgainstOrgHub(t, hub, "dckr_pat_usersecret")
	if err == nil {
		t.Fatalf("prune succeeded with a PAT logging in as an organization:\n%s", out)
	}
	var orgErr *ErrDockerHubOrgLogin
	if !errors.As(err, &orgErr) {
		t.Fatalf("error = %v (%T), want *ErrDockerHubOrgLogin", err, err)
	}
	for _, want := range []string{
		"Cannot log into an organization account",
		"vars.DOCKERHUB_USERNAME",
		"owns secrets.DOCKER_PAT",
		"Read, Write, Delete",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if len(hub.deleted) != 0 {
		t.Errorf("deleted = %v after a failed login", hub.deleted)
	}
}
