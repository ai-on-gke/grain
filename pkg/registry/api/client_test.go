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

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
)

func TestValidationHelpers(t *testing.T) {
	t.Parallel()

	for _, okName := range []string{"abc", "alice-dev", "grain-k8s-01"} {
		if err := api.ValidateEntryName(okName); err != nil {
			t.Errorf("ValidateEntryName(%q) unexpected error: %v", okName, err)
		}
	}
	for _, badName := range []string{"", "ab", "1abc", "Alice", "bad-", "-bad", "api", "admin", "directory", "route", "healthz"} {
		if err := api.ValidateEntryName(badName); err == nil {
			t.Errorf("ValidateEntryName(%q) succeeded, want error", badName)
		}
	}

	scope, err := api.NormalizeScope(api.Scope{
		Repo:       "  Acme/Autoscaler ",
		PathPrefix: "/pkg/autoscaler/",
		Role:       "",
	})
	if err != nil {
		t.Fatalf("NormalizeScope: %v", err)
	}
	if scope.Repo != "acme/autoscaler" || scope.PathPrefix != "pkg/autoscaler" || scope.Role != api.ScopeRolePrimary {
		t.Errorf("NormalizeScope = %+v, want repo=acme/autoscaler prefix=pkg/autoscaler role=primary", scope)
	}

	for _, badScope := range []api.Scope{
		{Repo: "noslash"},
		{Repo: "a/b/c"},
		{Repo: "acme/repo", PathPrefix: "../escape"},
		{Repo: "acme/repo", PathPrefix: "pkg/../other"},
		{Repo: "acme/repo", PathPrefix: `pkg\win`},
		{Repo: "acme/repo", Role: "admin"},
	} {
		if _, err := api.NormalizeScope(badScope); err == nil {
			t.Errorf("NormalizeScope(%+v) succeeded, want error", badScope)
		}
	}

	if _, err := api.ValidateRegistryURL("https://grain.example.com/", false); err != nil {
		t.Errorf("https registry URL rejected: %v", err)
	}
	if _, err := api.ValidateRegistryURL("http://127.0.0.1:8080", false); err != nil {
		t.Errorf("loopback http registry URL rejected: %v", err)
	}
	for _, badURL := range []string{
		"http://grain.example.com",
		"https://user:pass@grain.example.com",
		"http://169.254.169.254/latest",
		"https://metadata.internal",
		"https://grain.example.com?foo=1",
	} {
		if _, err := api.ValidateRegistryURL(badURL, false); err == nil {
			t.Errorf("ValidateRegistryURL(%q) succeeded, want error", badURL)
		}
	}
}

func TestClientCreateHeartbeatGetAndDelete(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotGrainAuth string
	var gotRegistryToken string
	var gotHeartbeat api.HeartbeatRequest
	var deletedConfirm string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/entries", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotGrainAuth = r.Header.Get("X-Grain-Auth")
		gotRegistryToken = r.Header.Get("X-Grain-Registry-Token")
		var req api.CreateEntryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(api.CreateEntryResponse{
			Entry: api.Entry{
				Name:     req.Name,
				Kind:     req.Kind,
				HostKind: req.HostKind,
				State:    api.StateRunning,
				Status:   api.StatusOffline,
			},
			Token: "grc_testtoken123",
		})
	})
	mux.HandleFunc("PUT /api/entries/team-k8s/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotGrainAuth = r.Header.Get("X-Grain-Auth")
		gotRegistryToken = r.Header.Get("X-Grain-Registry-Token")
		if err := json.NewDecoder(r.Body).Decode(&gotHeartbeat); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
			Status:               api.StatusOnline,
			NextHeartbeatSeconds: 20,
			ScopeConflicts: []api.ScopeConflict{
				{Repo: "acme/autoscaler", PathPrefix: "", ConflictWith: "incumbent-grain"},
			},
		})
	})
	mux.HandleFunc("GET /api/entries/team-k8s", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.Entry{
			Name:   "team-k8s",
			Kind:   api.KindConnected,
			Status: api.StatusOnline,
		})
	})
	mux.HandleFunc("DELETE /api/entries/team-k8s", func(w http.ResponseWriter, r *http.Request) {
		var req api.DeleteEntryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		deletedConfirm = req.Confirm
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /api/entries/rate-limited/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "rate limit exceeded"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	oidcClient, err := api.NewClient(srv.URL, api.WithTokenSource(api.StaticTokenSource("oidc-jwt-1")))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	created, err := oidcClient.CreateEntry(ctx, api.CreateEntryRequest{
		Name:      "team-k8s",
		Kind:      api.KindConnected,
		HostKind:  api.HostKindKubernetes,
		MintToken: true,
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if gotAuth != "Bearer oidc-jwt-1" || gotGrainAuth != "" || gotRegistryToken != "" {
		t.Errorf("OIDC headers = Auth:%q X-Grain-Auth:%q X-Grain-Registry-Token:%q", gotAuth, gotGrainAuth, gotRegistryToken)
	}
	if created.Name != "team-k8s" || created.Token != "grc_testtoken123" {
		t.Errorf("CreateEntry response = %+v", created)
	}

	hbClient, err := api.NewClient(srv.URL, api.WithTokenSource(api.StaticTokenSource(created.Token)))
	if err != nil {
		t.Fatalf("NewClient heartbeat: %v", err)
	}
	hbResp, err := hbClient.Heartbeat(ctx, "team-k8s", api.HeartbeatRequest{
		GrainVersion:       "v2.4.0",
		GrainCommit:        "abc1234",
		Health:             api.HealthOK,
		ActiveTasks:        3,
		AwaitingReplyTasks: 1,
		HostKind:           api.HostKindKubernetes,
		Scopes: []api.Scope{
			{Repo: "acme/autoscaler", PathPrefix: "", Role: api.ScopeRolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if gotAuth != "Bearer grc_testtoken123" || gotGrainAuth != "token" || gotRegistryToken != "grc_testtoken123" {
		t.Errorf("Heartbeat headers = Auth:%q X-Grain-Auth:%q X-Grain-Registry-Token:%q", gotAuth, gotGrainAuth, gotRegistryToken)
	}
	if gotHeartbeat.GrainVersion != "v2.4.0" || gotHeartbeat.ActiveTasks != 3 || gotHeartbeat.AwaitingReplyTasks != 1 {
		t.Errorf("Heartbeat body = %+v", gotHeartbeat)
	}
	if hbResp.Interval() != 20*time.Second || len(hbResp.ScopeConflicts) != 1 {
		t.Errorf("HeartbeatResponse = %+v", hbResp)
	}

	entry, err := oidcClient.GetEntry(ctx, "team-k8s")
	if err != nil || entry.Status != api.StatusOnline {
		t.Fatalf("GetEntry = %+v, %v", entry, err)
	}

	if err := oidcClient.DeleteEntry(ctx, "team-k8s"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if deletedConfirm != "team-k8s" {
		t.Errorf("DeleteEntry confirm = %q, want team-k8s", deletedConfirm)
	}

	_, err = hbClient.Heartbeat(ctx, "rate-limited", api.HeartbeatRequest{Health: api.HealthOK})
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RetryAfter != 42*time.Second || !api.IsRetryable(err) {
		t.Errorf("429 error = %#v (retryable=%v), want APIError{StatusCode:429, RetryAfter:42s}", err, api.IsRetryable(err))
	}
}
