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
		if pattern == "POST /mcp" || pattern == "POST /api/mcp" {
			continue // the MCP endpoints themselves, which the credential is for
		}
		if pattern == "GET /api/auth/whoami" {
			continue // a credential asking what it is (ownCredentialRoute, external-cli.spec.md#XC-8)
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

// externalAgentTokenOwner records an owner with the source of whoever set
// it: a verified minter's, the access code's (or loopback's, or the
// in-process CLI's) when only an unverified issuer named it, and the
// minting credential's own when an external agent passes its owner on.
func TestExternalAgentTokenOwnerSources(t *testing.T) {
	c := &Client{Config: Config{Actor: DefaultActor("operator@example.com")}}
	ctx := context.Background()
	accessCode := withRequestSource(ctx, model.SourceAccessCode)
	loopback := withRequestSource(ctx, model.SourceLoopback)
	alice := WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	unverifiedHuman := WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "mallory@example.com"})
	agentOf := func(tok model.ExternalAgentToken) context.Context {
		return withExternalToken(WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: tok.PrincipalID(), Source: model.SourceExternalToken}), &tok)
	}
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		req       CreateExternalAgentTokenRequest
		owner     string
		src       model.AuthSource
		forbidden bool
	}{
		{"verified minter owns it", alice, CreateExternalAgentTokenRequest{}, "alice@example.com", model.SourceIAP, false},
		{"verified minter names bob", alice, CreateExternalAgentTokenRequest{Owner: "bob@example.com"}, "bob@example.com", model.SourceIAP, false},
		{"verified self-service", alice, CreateExternalAgentTokenRequest{SelfService: true}, "alice@example.com", model.SourceIAP, false},
		{"access code names nobody", accessCode, CreateExternalAgentTokenRequest{}, "", "", false},
		{"access code names bob", accessCode, CreateExternalAgentTokenRequest{Owner: "bob@example.com"}, "bob@example.com", model.SourceAccessCode, false},
		{"access code self-service", accessCode, CreateExternalAgentTokenRequest{SelfService: true}, "", "", false},
		{"access code self-service naming bob", accessCode, CreateExternalAgentTokenRequest{SelfService: true, Owner: "bob@example.com"}, "", "", true},
		{"loopback names nobody", loopback, CreateExternalAgentTokenRequest{}, "", "", false},
		{"loopback names bob", loopback, CreateExternalAgentTokenRequest{Owner: "bob@example.com"}, "bob@example.com", model.SourceLoopback, false},
		{"in-process names nobody", ctx, CreateExternalAgentTokenRequest{}, "", "", false},
		{"in-process names bob", ctx, CreateExternalAgentTokenRequest{Owner: "bob@example.com"}, "bob@example.com", model.SourceInProcess, false},
		{"a principal nobody verified is no owner", unverifiedHuman, CreateExternalAgentTokenRequest{}, "", "", false},
		{"agent passes on a verified owner", agentOf(model.ExternalAgentToken{Name: "a", Owner: "alice@example.com", OwnerSource: model.SourceIAP}),
			CreateExternalAgentTokenRequest{}, "alice@example.com", model.SourceIAP, false},
		{"agent passes on an access-code owner", agentOf(model.ExternalAgentToken{Name: "b", Owner: "bob@example.com", OwnerSource: model.SourceAccessCode}),
			CreateExternalAgentTokenRequest{}, "bob@example.com", model.SourceAccessCode, false},
		{"agent passes on an old row's owner", agentOf(model.ExternalAgentToken{Name: "c", Owner: "carol@example.com"}),
			CreateExternalAgentTokenRequest{}, "carol@example.com", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, src, err := c.externalAgentTokenOwner(tc.ctx, tc.req)
			if tc.forbidden {
				if err == nil {
					t.Fatalf("= %q, %q; want refused", owner, src)
				}
				return
			}
			if err != nil || owner != tc.owner || src != tc.src {
				t.Fatalf("= %q, %q, %v; want %q, %q", owner, src, err, tc.owner, tc.src)
			}
		})
	}
}
