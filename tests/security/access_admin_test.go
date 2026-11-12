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

package security

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestVerifiedAdminAccessGuardrailsAndReasonSanitization pins the security
// invariants walked in docs/test-plans/2026-11-10.md §2:
//  1. Access change reasons strip ANSI CSI/OSC escape sequences and control/bidi
//     characters from config_audit, allowed_principal.reason and daemon logs,
//     and reject reasons consisting solely of escapes/whitespace with 400.
//  2. Narrowing or removing the sole full admin returns 403 last_admin directly
//     (before confirm_self), while self_escalation still blocks DELETE?confirmSelf=1
//     followed by POST wider and blocks group-to-user persistence.
//  3. An external gma_ agent credential receives 403 even on an idempotent
//     POST of an already-listed entry (acting-task credentials take the same
//     checkAccessWriteCaller path; this test does not mint one).
//  4. Unchecked group entries fail closed as potentially matching the caller.
func TestVerifiedAdminAccessGuardrailsAndReasonSanitization(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	var logBuf bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	mc := model.DefaultConfig()
	mc.TargetRepos = []string{"acme/widgets"}
	mc.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}

	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	groupFail := false
	uiCfg := ui.Config{
		Actor:         ui.DefaultActor("owner@example.com"),
		LocalAPIToken: code,
		GroupChecker: groupCheckerFunc(func(_ context.Context, groupEmail, memberEmail string) (bool, error) {
			if groupFail {
				return false, errors.New("cloud identity unavailable")
			}
			return groupEmail == "ops@example.com" && (memberEmail == "bob@example.com" || memberEmail == "temp@example.com"), nil
		}),
	}
	sharedIdentityProxy().configure(&uiCfg)

	client := ui.NewClient(uiCfg, store)
	for _, entry := range []string{"user:alice@example.com", "user:bob@example.com", "group:ops@example.com"} {
		if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: entry, Role: model.DefaultAccessRole, Reason: "seed"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := ui.NewServerWithClient(client)

	serveIAP := func(email, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, rdr)
		req.RemoteAddr = "203.0.113.10:443"
		for k, vs := range sessionHeader(t, email) {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	esc := func(entry string) string { return "/api/access/principals/" + url.PathEscape(entry) }

	// 1. ANSI CSI/OSC escapes and CR/LF in reasons are stripped; pure-ANSI reasons fail 400.
	if rec := serveIAP("alice@example.com", http.MethodPatch, esc("user:bob@example.com"),
		`{"role":"grain/viewer","reason":"oncall\r\n\u001b[31mFORGED\u001b[0m\u001b]52;c;dGVzdA==\u0007\u202edone"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH with ANSI/CRLF reason = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := serveIAP("alice@example.com", http.MethodPatch, esc("user:bob@example.com"),
		`{"role":"grain/operator","reason":"\u001b[31m\u001b[0m\r\n"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with pure-ANSI reason = %d %s, want 400", rec.Code, rec.Body)
	}
	audits, err := store.ConfigAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range audits {
		if strings.ContainsAny(a.Summary, "\r\n\x1b\x07\u202e") {
			t.Fatalf("audit summary retained control/escape bytes: %q", a.Summary)
		}
	}
	if strings.ContainsAny(logBuf.String(), "\x1b\x07\u202e") {
		t.Fatalf("daemon log retained terminal escape bytes: %q", logBuf.String())
	}

	// 2. Group-admitted caller cannot entrench a direct user: entry; DELETE?confirmSelf=1 + POST wider stays blocked.
	if rec := serveIAP("temp@example.com", http.MethodPost, "/api/access/principals",
		`{"entry":"user:temp@example.com","role":"grain/operator","reason":"persist"}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "self_escalation") {
		t.Fatalf("group-admitted POST own user entry = %d %s, want 403 self_escalation", rec.Code, rec.Body)
	}
	if rec := serveIAP("bob@example.com", http.MethodDelete, esc("user:bob@example.com")+"?confirmSelf=1", ""); rec.Code != http.StatusOK {
		t.Fatalf("bob DELETE own user entry = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := serveIAP("bob@example.com", http.MethodPost, "/api/access/principals",
		`{"entry":"user:bob@example.com","role":"grain/operator","reason":"re-add"}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "self_escalation") {
		t.Fatalf("bob POST own user entry after DELETE = %d %s, want 403 self_escalation", rec.Code, rec.Body)
	}

	// 3. Agent credentials receive 403 even on an idempotent POST of an existing entry.
	issued, err := client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name:   "probe",
		Scopes: []string{"access:read", "access:write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gmaReq := httptest.NewRequest(http.MethodPost, "/api/access/principals", strings.NewReader(`{"entry":"user:alice@example.com"}`))
	gmaReq.RemoteAddr = "203.0.113.10:443"
	gmaReq.Header.Set("Authorization", "Bearer "+issued.Token)
	gmaReq.Header.Set("Content-Type", "application/json")
	gmaReq.Header.Set(ui.ActingReasonHeader, "probe")
	gmaRec := httptest.NewRecorder()
	srv.ServeHTTP(gmaRec, gmaReq)
	if gmaRec.Code != http.StatusForbidden {
		t.Fatalf("gma_ POST listed entry = %d %s, want 403", gmaRec.Code, gmaRec.Body)
	}

	// 4. Remove group:ops so alice is the sole full admin: narrowing/removing alice returns last_admin immediately (even without confirmSelf).
	if rec := serveIAP("alice@example.com", http.MethodDelete, esc("group:ops@example.com")+"?reason=cleanup", ""); rec.Code != http.StatusOK {
		t.Fatalf("alice DELETE group:ops = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := serveIAP("alice@example.com", http.MethodPatch, esc("user:alice@example.com"),
		`{"role":"grain/viewer","reason":"step down"}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "last_admin") {
		t.Fatalf("sole admin PATCH without confirmSelf = %d %s, want 403 last_admin", rec.Code, rec.Body)
	}

	// 5. Failing group checker fails closed on group mutations.
	groupFail = true
	if rec := serveIAP("alice@example.com", http.MethodPost, "/api/access/principals",
		`{"entry":"group:ops@example.com","role":"grain/viewer"}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "self_escalation") {
		t.Fatalf("POST group with broken group checker = %d %s, want 403 self_escalation", rec.Code, rec.Body)
	}
}
