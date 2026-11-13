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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// accessCodeAudit is the access history's access-code entries, newest
// first.
func accessCodeAudit(t *testing.T, c *ui.Client) []model.ConfigAuditEntry {
	t.Helper()
	all, err := c.Store.ConfigAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.ConfigAuditEntry
	for _, e := range all {
		if e.Surface == "access-code" {
			out = append(out, e)
		}
	}
	return out
}

func signInWithCode(t *testing.T, srv *ui.Server, name string) *http.Cookie {
	t.Helper()
	form := url.Values{"code": {testAccessCode}, "name": {name}}.Encode()
	rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", form,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in as %q: %d %s", name, rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("signing in as %q set no session cookie", name)
	return nil
}

// Every request the code authenticates records who presented it: the
// name a UI sign-in gave, or the one the CLI sends beside a bearer code.
func TestAnAccessCodeRequestRecordsANamedActor(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cookie := signInWithCode(t, srv, "Alice@Example.com")
	rec := serveFrom(srv, networkAddr, http.MethodDelete, "/api/pipeline-pause", "",
		map[string]string{"Cookie": cookie.String()})
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE /api/pipeline-pause with alice's session: %d %s", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodDelete, "/api/pipeline-pause", "",
		map[string]string{"Authorization": "Bearer " + testAccessCode, ui.AccessNameHeader: "ops@build-host"})
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE /api/pipeline-pause with a bearer code: %d %s", rec.Code, rec.Body)
	}

	entries := accessCodeAudit(t, client)
	want := map[string]bool{"access-code:alice@example.com": false, "access-code:ops@build-host": false}
	for _, e := range entries {
		if _, ok := want[e.By.ID]; ok && e.By.Source == model.SourceAccessCode && e.By.Kind == model.PrincipalHuman &&
			strings.Contains(e.Summary, "DELETE /api/pipeline-pause") && strings.Contains(e.Summary, "203.0.113.7") {
			want[e.By.ID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("no access-code audit entry by %s naming the route and source; have %+v", id, entries)
		}
	}
	if got := ui.PrincipalLabel(model.Principal{Kind: model.PrincipalHuman, ID: "access-code:alice@example.com", Source: model.SourceAccessCode}, nil); got != "alice@example.com (access code)" {
		t.Errorf("label = %q, want alice@example.com (access code)", got)
	}
}

// A UI sign-in names somebody, and somebody the allowlist admits when it
// names anybody.
func TestAccessCodeSignInNeedsANameTheAllowlistAdmits(t *testing.T) {
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	for _, c := range []struct {
		name string
		want int
	}{
		{"", http.StatusForbidden},
		{"not an email", http.StatusBadRequest},
		{"mallory@example.com", http.StatusForbidden},
		{"alice@example.com", http.StatusOK},
	} {
		body := `{"code":"` + testAccessCode + `","name":"` + c.name + `"}`
		rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, map[string]string{"Content-Type": "application/json"})
		if rec.Code != c.want {
			t.Errorf("signing in as %q: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
}

// In recovery mode, once a person with a full role can sign in with a
// verified identity, the code manages access and settings and nothing
// else, and each use raises an Inbox notification.
func TestRecoveryModeHoldsTheCodeToAccessAndSettings(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ui.SetAccessCodeModeForTest(srv, ui.AccessCodeRecovery)
	code := map[string]string{"Authorization": "Bearer " + testAccessCode, ui.AccessNameHeader: "alice@example.com"}

	// No verifier yet: alice could not sign in any other way, so the
	// code is still the bootstrap and works everywhere.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", code); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks in recovery mode with no verifier: %d %s, want 200", rec.Code, rec.Body)
	}

	ui.SetIAPAudience(tunnelAudience)
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", code); rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/tasks in recovery mode: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodDelete, "/api/pipeline-pause", "", code); rec.Code != http.StatusForbidden {
		t.Errorf("DELETE /api/pipeline-pause in recovery mode: %d %s, want 403", rec.Code, rec.Body)
	}
	for _, path := range []string{"/api/access/principals", "/api/settings"} {
		if rec := serveFrom(srv, networkAddr, http.MethodGet, path, "", code); rec.Code != http.StatusOK {
			t.Errorf("GET %s in recovery mode: %d %s, want 200", path, rec.Code, rec.Body)
		}
	}
	notes, err := client.ListNotifications(context.Background(), model.NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	raised := false
	for _, n := range notes {
		if strings.HasPrefix(n.Subject, "access-code:recovery:") && strings.Contains(n.Body, "alice@example.com") {
			raised = true
		}
	}
	if !raised {
		t.Errorf("no recovery-use notification raised; have %+v", notes)
	}
}

// Off refuses the code everywhere: as a bearer token, a session, and at
// sign-in.
func TestTurningTheCodeOffRefusesIt(t *testing.T) {
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	cookie := signInWithCode(t, srv, "alice@example.com")
	ui.SetAccessCodeModeForTest(srv, ui.AccessCodeOff)

	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + testAccessCode}); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/tasks with the code switched off: %d %s, want 401", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Cookie": cookie.String()}); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/tasks with a session and the code switched off: %d %s, want 401", rec.Code, rec.Body)
	}
	body := `{"code":"` + testAccessCode + `","name":"alice@example.com"}`
	if rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "switched off") {
		t.Errorf("signing in with the code switched off: %d %s, want 403 saying it is off", rec.Code, rec.Body)
	}
}

func TestAccessCodeModeFileRoundTrips(t *testing.T) {
	tok := filepath.Join(t.TempDir(), ui.LocalAPITokenFileName)
	if got := ui.ReadAccessCodeMode(tok); got != ui.AccessCodeOn {
		t.Errorf("mode with no file = %q, want on", got)
	}
	for _, m := range []ui.AccessCodeMode{ui.AccessCodeRecovery, ui.AccessCodeOff, ui.AccessCodeOn} {
		if err := ui.WriteAccessCodeMode(tok, m); err != nil {
			t.Fatal(err)
		}
		if got := ui.ReadAccessCodeMode(tok); got != m {
			t.Errorf("mode = %q, want %q", got, m)
		}
	}
	if err := os.WriteFile(ui.AccessCodeModePath(tok), []byte("sometimes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ui.ReadAccessCodeMode(tok); got != ui.AccessCodeOff {
		t.Errorf("a mode file holding nonsense = %q, want off", got)
	}
}

// Every route outside access and settings refuses a recovery-mode code,
// read off the route table so a new route is held to it the moment it is
// added.
func TestRecoveryModeRefusesEveryOtherRoute(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ui.SetAccessCodeModeForTest(srv, ui.AccessCodeRecovery)
	ui.SetIAPAudience(tunnelAudience)
	code := map[string]string{"Authorization": "Bearer " + testAccessCode, ui.AccessNameHeader: "alice@example.com"}
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	checked := 0
	for _, pattern := range srv.Routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			continue
		}
		path = wildcard.ReplaceAllString(path, "x")
		r := httptest.NewRequest(method, path, nil)
		if ui.AccessCodeRecoveryRouteForTest(r) || strings.HasPrefix(path, "/api/hostd/") || path == "/cli/install.sh" || path == "/hostd/install" {
			continue
		}
		rec := serveFrom(srv, networkAddr, method, path, "", code)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "recovery mode") {
			t.Errorf("%s in recovery mode: %d %s, want 403 naming recovery mode", pattern, rec.Code, rec.Body)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("checked only %d routes", checked)
	}
}

// Settings -> Access's history lists what a code session changed beside
// the role changes (docs/access-code.md), not only Settings -> Audit:
// GET /api/access/history read the access surface alone
// (docs/test-runs/2026-11-11-caller-identity.md, F2).
func TestAccessHistoryListsAccessCodeWrites(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cookie := signInWithCode(t, srv, "alice@example.com")
	rec := serveFrom(srv, networkAddr, http.MethodDelete, "/api/pipeline-pause", "",
		map[string]string{"Cookie": cookie.String()})
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE /api/pipeline-pause with alice's session: %d %s", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodGet, "/api/access/history", "",
		map[string]string{"Cookie": cookie.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/access/history: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"surface":"access-code"`) ||
		!strings.Contains(body, "DELETE /api/pipeline-pause by alice@example.com") ||
		!strings.Contains(body, `"actor":"access-code:alice@example.com"`) {
		t.Errorf("GET /api/access/history = %s, want alice's code-session write beside the role changes", body)
	}
}

// A POST held to a :read scope only reads, so a code session's grants
// check -- Settings -> Tasks and New task send one on every open -- is not
// recorded as a configuration change beside the session's real writes
// (docs/test-runs/2026-11-13-user-system-settings.md, F1).
func TestAccessCodeReadOnlyPostIsNotAConfigurationChange(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cookie := signInWithCode(t, srv, "alice@example.com")
	headers := map[string]string{"Cookie": cookie.String(), "Content-Type": "application/json"}
	rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/capability-grants/check", `{"grants":[]}`, headers)
	if rec.Code/100 != 2 {
		t.Fatalf("POST /api/capability-grants/check with alice's session: %d %s", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodDelete, "/api/pipeline-pause", "", headers)
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE /api/pipeline-pause with alice's session: %d %s", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodGet, "/api/access/history", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/access/history: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/api/capability-grants/check") {
		t.Errorf("GET /api/access/history = %s, want no row for a read-only grants check", body)
	}
	if !strings.Contains(body, "DELETE /api/pipeline-pause by alice@example.com") {
		t.Errorf("GET /api/access/history = %s, want alice's real write still recorded", body)
	}
}

// An access-code session removing the user: entry for the email it signed
// in as is asked to confirm first (confirm_self, which both UIs answer):
// the code signs in only as a listed name, so the removal turns that email
// away at its next sign-in. Other entries go as before.
func TestAccessCodeSessionConfirmsRemovingItsOwnEntry(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cookie := signInWithCode(t, srv, "alice@example.com")
	headers := map[string]string{"Cookie": cookie.String(), "Content-Type": "application/json"}
	for _, entry := range []string{"user:alice@example.com", "user:bob@example.com", "user:carol@example.com"} {
		rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/access/principals",
			`{"entry":"`+entry+`","role":"grain/viewer"}`, headers)
		if rec.Code/100 != 2 {
			t.Fatalf("POST /api/access/principals %s: %d %s", entry, rec.Code, rec.Body)
		}
	}
	rec := serveFrom(srv, networkAddr, http.MethodDelete, "/api/access/principals/user:bob@example.com", "", headers)
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE user:bob@example.com: %d %s, want it removed without a confirmation", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodDelete, "/api/access/principals/user:alice@example.com", "", headers)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confirm_self") {
		t.Fatalf("DELETE user:alice@example.com from alice's code session: %d %s, want 403 confirm_self", rec.Code, rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodDelete, "/api/access/principals/user:alice@example.com?confirmSelf=1", "", headers)
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE user:alice@example.com?confirmSelf=1: %d %s, want it removed once confirmed", rec.Code, rec.Body)
	}
}

// Removing the session's own entry when it is the last person on the
// allowlist asks nothing: an allowlist naming nobody lets the access code
// sign in as any email, so nothing is narrowed.
func TestAccessCodeSessionRemovesItsOwnLastEntryWithoutConfirming(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cookie := signInWithCode(t, srv, "alice@example.com")
	headers := map[string]string{"Cookie": cookie.String(), "Content-Type": "application/json"}
	for _, entry := range []string{"user:alice@example.com", "serviceAccount:bot@demo-project.iam.gserviceaccount.com"} {
		rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/access/principals",
			`{"entry":"`+entry+`","role":"grain/viewer","reason":"test"}`, headers)
		if rec.Code/100 != 2 {
			t.Fatalf("POST /api/access/principals %s: %d %s", entry, rec.Code, rec.Body)
		}
	}
	rec := serveFrom(srv, networkAddr, http.MethodDelete, "/api/access/principals/user:alice@example.com", "", headers)
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE user:alice@example.com, the last person listed: %d %s, want it removed without a confirmation", rec.Code, rec.Body)
	}
}
