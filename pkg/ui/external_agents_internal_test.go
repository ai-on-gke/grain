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
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestExternalAgentTokenReachesNoInternalRoute holds the bearer path in
// serveHTTP to agentAPIGate: a gma_ credential authenticates the request
// before the access-code gate, so the only thing standing between it and
// the routes outside the agent API -- executor and hostd bootstrap, the
// staging proxy, /mcp's own nesting -- is the gate's refusal of a route
// with no scope. A credential holding every scope there is must still be
// refused on every one of them, over plain HTTP.
func TestExternalAgentTokenReachesNoInternalRoute(t *testing.T) {
	LogToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	cfg.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{Actor: DefaultActor("alice")}, store)

	var every []string
	for _, info := range model.APIScopes() {
		every = append(every, string(info.Scope))
	}
	person := WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := srv.tasks.CreateExternalAgentToken(person, CreateExternalAgentTokenRequest{
		Name: "everything", Scopes: every, RateLimit: 100000, MaxConcurrent: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	param := regexp.MustCompile(`\{[^}]*\}`)
	checked := 0
	for pattern, scope := range routeScopes {
		if scope != scopeInternal && scope != "" {
			continue
		}
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			method, path = http.MethodGet, pattern
		}
		if pattern == "POST /mcp" {
			continue // the endpoint itself, which the credential is for
		}
		req := httptest.NewRequest(method, param.ReplaceAllString(path, "x"), strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		req.Header.Set(ActingReasonHeader, "probing the internal routes")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		checked++
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not part of the agent API") {
			t.Errorf("%s with a gma_ credential holding every scope: status=%d body=%s, want 403 not part of the agent API",
				pattern, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
	if checked == 0 {
		t.Fatal("routeScopes names no internal route to check")
	}
}
