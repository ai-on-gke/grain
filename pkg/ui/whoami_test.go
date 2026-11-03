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
	"net/http"
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
