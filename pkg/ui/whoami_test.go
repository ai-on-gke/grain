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

package ui_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestWhoamiSaysWhoTheRequestActsAs is operator-attribution spec §5: the
// access code is the operator, marked as such; a verified person is
// themselves; a request the gate refuses gets the gate's answer, not this
// body.
func TestWhoamiSaysWhoTheRequestActsAs(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name: "fake", headers: []string{"X-Fake-Identity"}, nonComparable: func() {},
		verifyFn: parseSimpleSignedHeader,
	}))
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:bob@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    ui.WhoamiResponse
	}{
		{"the access code", map[string]string{"Authorization": "Bearer " + testAccessCode}, ui.WhoamiResponse{
			Actor:  ui.WirePrincipal{Kind: "human", Source: "access-code", Label: "operator (access code)"},
			Source: "access-code", Allowed: true,
		}},
		{"a verified person", map[string]string{"X-Fake-Identity": "ok:Bob@example.com"}, ui.WhoamiResponse{
			Actor:  ui.WirePrincipal{Kind: "human", ID: "bob@example.com", Source: "fake", Label: "bob@example.com"},
			Source: "fake", Verified: true, Allowed: true,
		}},
		{"the access code with unsigned identity headers", map[string]string{
			"Authorization":                   "Bearer " + testAccessCode,
			"X-Goog-Authenticated-User-Email": "accounts.google.com:mallory@example.com",
			"X-Grain-User":                    "mallory",
		}, ui.WhoamiResponse{
			Actor:  ui.WirePrincipal{Kind: "human", Source: "access-code", Label: "operator (access code)"},
			Source: "access-code", Allowed: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/whoami", "", tc.headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/whoami: %d: %s", rec.Code, rec.Body)
			}
			var got ui.WhoamiResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tc.want.Actor.ID == "" {
				// The operator is whatever this deployment's own actor is
				// called; only how it reads matters here.
				got.Actor.ID = ""
			}
			if got.Actor != tc.want.Actor || got.OnBehalfOf != nil || got.Source != tc.want.Source ||
				got.Verified != tc.want.Verified || got.Allowed != tc.want.Allowed {
				t.Errorf("whoami = %+v, want %+v", got, tc.want)
			}
		})
	}

	if rec := serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/whoami", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credential: GET /api/whoami = %d, want the gate's 401", rec.Code)
	}
	if rec := serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/whoami", "",
		map[string]string{"X-Fake-Identity": "ok:stranger@example.com"}); rec.Code != http.StatusForbidden {
		t.Errorf("a stranger: GET /api/whoami = %d, want the gate's 403", rec.Code)
	}
}

// TestTaskAndCommentCarryPrincipalLabels is spec §4.1: a task's author and
// each comment's author come with their source and the label the UI
// renders, for a verified person and for the operator alike.
func TestTaskAndCommentCarryPrincipalLabels(t *testing.T) {
	_, client := testServer(t)
	alice := ui.WithPrincipal(context.Background(),
		model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		wantSource string
		wantLabel  string
	}{
		{"verified", alice, "iap", "alice@example.com"},
		{"operator", context.Background(), "in-process", "operator (local)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := client.CreateTask(tc.ctx, ui.CreateTaskRequest{Title: "attributed " + tc.name, Description: "b"})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.AddComment(tc.ctx, task.ID, "a word", nil); err != nil {
				t.Fatal(err)
			}
			got, err := client.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AuthorSource != tc.wantSource || got.AuthorLabel != tc.wantLabel {
				t.Errorf("task author = %q/%q, want %q/%q", got.AuthorSource, got.AuthorLabel, tc.wantSource, tc.wantLabel)
			}
			if len(got.Comments) == 0 {
				t.Fatal("no comments")
			}
			c := got.Comments[len(got.Comments)-1]
			if c.AuthorSource != tc.wantSource || c.AuthorLabel != tc.wantLabel || c.Who() != tc.wantLabel {
				t.Errorf("comment author = %q/%q (who %q), want %q/%q", c.AuthorSource, c.AuthorLabel, c.Who(), tc.wantSource, tc.wantLabel)
			}
		})
	}
}

// TestCommentWhoNamesARelayedAgent: a verified on-behalf-of is already in
// the label; an agent's, which the label leaves out, is said beside it.
func TestCommentWhoNamesARelayedAgent(t *testing.T) {
	for _, tc := range []struct {
		c    ui.Comment
		want string
	}{
		{ui.Comment{Author: "director", AuthorLabel: "session 7 for alice@example.com", OnBehalfOf: "alice@example.com",
			OnBehalfOfLabel: "alice@example.com"}, "session 7 for alice@example.com"},
		{ui.Comment{Author: "grain", AuthorLabel: "grain", OnBehalfOf: "42", OnBehalfOfLabel: "task 42"}, "grain on behalf of task 42"},
		{ui.Comment{Author: "bob", OnBehalfOf: "42"}, "bob on behalf of 42"},
	} {
		if got := tc.c.Who(); got != tc.want {
			t.Errorf("%+v.Who() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// TestConfigAuditReadsBackLabelled is spec §4.4: a config-audit row reads
// back with the label of whoever made the change.
func TestConfigAuditReadsBackLabelled(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}
	if _, err := client.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{At: time.Now(), By: alice, Surface: "settings", Summary: "PUT /api/settings"}); err != nil {
		t.Fatal(err)
	}
	rec := serveFrom(srv, "", http.MethodGet, "/api/deployment/config-audit", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config-audit: %d: %s", rec.Code, rec.Body)
	}
	var got []ui.ConfigAuditView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ActorLabel != "alice@example.com" || got[0].ActorSource != "iap" || got[0].Surface != "settings" {
		t.Errorf("config audit = %+v, want alice's settings change", got)
	}
}

// TestAuthWhoamiUnauthenticatedAuthenticatedAndExpiredToken is
// external-cli.spec.md#XC-8: GET /api/auth/whoami tells a caller with no
// credential how it could sign in and nothing about anybody, tells an
// external agent credential its own grants, and answers an expired one
// with 401 token_expired and when.
func TestAuthWhoamiUnauthenticatedAuthenticatedAndExpiredToken(t *testing.T) {
	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }
	expires := now.Add(48 * time.Hour)
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name: "laptop", ScopeSet: "grain/external-dev", Owner: "jane@example.com", ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}

	ask := func(bearer string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
		req.RemoteAddr = "198.51.100.7:4242" // a caller elsewhere, not this machine
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET /api/auth/whoami answered %d with no JSON: %s", rec.Code, rec.Body.String())
		}
		return rec.Code, body
	}

	code, anon := ask("")
	if code != http.StatusOK || anon["authenticated"] != false || anon["externalAgentApi"] != true {
		t.Fatalf("unauthenticated: %d %v", code, anon)
	}
	accepted, _ := anon["acceptedAuth"].([]any)
	if !slices.Contains(accepted, any("access-code")) || !slices.Contains(accepted, any("external-token")) {
		t.Errorf("acceptedAuth = %v, want access-code and external-token", accepted)
	}
	for _, leaked := range []string{"principal", "tokenId", "owner", "resolvedScopes"} {
		if _, ok := anon[leaked]; ok {
			t.Errorf("unauthenticated answer carries %q: %v", leaked, anon)
		}
	}

	code, op := ask(testAccessCode)
	if code != http.StatusOK || op["authenticated"] != true || op["principal"] != "operator" {
		t.Errorf("access code: %d %v, want the operator", code, op)
	}

	code, agent := ask(issued.Token)
	if code != http.StatusOK || agent["authenticated"] != true || agent["principal"] != "agent:external:laptop" ||
		agent["tokenId"] != issued.ID || agent["owner"] != "jane@example.com" || agent["scopeSet"] != "grain/external-dev" ||
		agent["expiresAt"] == nil {
		t.Errorf("gma_ token: %d %v", code, agent)
	}
	if scopes, _ := agent["resolvedScopes"].([]any); !slices.Contains(scopes, any("tasks:read")) {
		t.Errorf("resolvedScopes = %v, want tasks:read among them", scopes)
	}

	now = expires.Add(time.Hour)
	code, expired := ask(issued.Token)
	if code != http.StatusUnauthorized || expired["code"] != ui.TokenExpiredCode {
		t.Fatalf("expired token: %d %v, want 401 %s", code, expired, ui.TokenExpiredCode)
	}
	if at, _ := time.Parse(time.RFC3339, expired["expiredAt"].(string)); !at.Equal(expires) {
		t.Errorf("expiredAt = %v, want %v", expired["expiredAt"], expires)
	}

	// Over HTTPClient the expiry is a TokenExpiredError.
	ts := httptest.NewServer(srv)
	defer ts.Close()
	hc := ui.NewHTTPClient(ts.URL)
	hc.Token = issued.Token
	hc.RetryPolicy = &ui.RetryPolicy{}
	_, err = hc.AuthWhoami(context.Background())
	var te *ui.TokenExpiredError
	if !errors.As(err, &te) || !te.ExpiredAt.Equal(expires) {
		t.Fatalf("HTTPClient.AuthWhoami = %v, want a TokenExpiredError at %v", err, expires)
	}
}
