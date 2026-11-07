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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestNormalizeDeploymentOwner(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "", want: ""},
		{raw: "   ", want: ""},
		{raw: "Alice@Example.com", want: "alice@example.com"},
		{raw: "  owner@corp.example.com  ", want: "owner@corp.example.com"},
		{raw: "user:Alice@Example.com", want: "alice@example.com"},
		{raw: "not-an-email", wantErr: true},
		{raw: "@example.com", wantErr: true},
		{raw: "alice@", wantErr: true},
		{raw: "alice@example", wantErr: true},
		{raw: "alice @example.com", wantErr: true},
		{raw: "group:alice@example.com", wantErr: true},
	} {
		got, err := ui.NormalizeDeploymentOwner(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeDeploymentOwner(%q) = %q, nil; want error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeDeploymentOwner(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeDeploymentOwner(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDeploymentOwnerSeedsAndLocksOperatorFloor(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	t.Setenv(ui.DeploymentOwnerEnv, "")

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	cfg := ui.Config{
		Actor:           ui.DefaultActor("bot@example.com"),
		DeploymentOwner: "Owner@Example.com",
		LocalAPIToken:   testAccessCode,
	}
	client := ui.NewClient(cfg, store)

	if err := client.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatalf("MigrateAllowedPrincipals: %v", err)
	}

	resp, err := client.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListAllowedPrincipals: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Entry != "user:owner@example.com" || resp.Entries[0].Role != model.DefaultAccessRole {
		t.Fatalf("seeded entries = %+v, want [user:owner@example.com role=%s]", resp.Entries, model.DefaultAccessRole)
	}

	// Removing the deployment owner via DenyPrincipal is refused.
	if _, err := client.DenyPrincipal(ctx, "user:owner@example.com"); err == nil || !strings.Contains(err.Error(), "deployment owner") {
		t.Fatalf("DenyPrincipal(user:owner@example.com) error = %v, want deployment owner refusal", err)
	}

	// Demoting the deployment owner via SetPrincipalRole is refused.
	if _, err := client.SetPrincipalRole(ctx, "user:owner@example.com", ui.SetPrincipalRoleRequest{Role: "grain/viewer"}); err == nil || !strings.Contains(err.Error(), "deployment owner") {
		t.Fatalf("SetPrincipalRole(user:owner@example.com, grain/viewer) error = %v, want deployment owner refusal", err)
	}

	// Re-adding the deployment owner with a lower role via AllowPrincipal is refused.
	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: "user:owner@example.com", Reason: "demote", Role: "grain/member"}); err == nil || !strings.Contains(err.Error(), "deployment owner") {
		t.Fatalf("AllowPrincipal(user:owner@example.com, grain/member) error = %v, want deployment owner refusal", err)
	}

	// Allowing the deployment owner as grain/operator succeeds idempotently.
	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: "user:owner@example.com", Reason: "keep operator", Role: model.DefaultAccessRole}); err != nil {
		t.Fatalf("AllowPrincipal(user:owner@example.com, grain/operator): %v", err)
	}
}

func TestDeploymentOwnerAdmittedAndRecognizedOverHTTP(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	t.Setenv(ui.DeploymentOwnerEnv, "owner@example.com")

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	headersFor := func(email string) map[string]string {
		return map[string]string{
			ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute)),
			"Content-Type":        "application/json",
		}
	}

	// Even with an empty allowed_principal table in SQLite, the deployment
	// owner is admitted by the immutable effectiveAllowedPrincipals floor as
	// grain/operator and passes requireDeploymentOwner on /api/hostds/enrollment-codes.
	rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/user", "", headersFor("owner@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/user as owner@example.com: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"role":"grain/operator"`) {
		t.Fatalf("GET /api/auth/user response missing role grain/operator: %s", rec.Body.String())
	}

	codesRec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/hostds/enrollment-codes", "", headersFor("owner@example.com"))
	if codesRec.Code != http.StatusOK {
		t.Fatalf("GET /api/hostds/enrollment-codes as owner@example.com: status %d, body %s", codesRec.Code, codesRec.Body.String())
	}

	// And the deployment owner cannot delete their own user:<owner> entry over HTTP.
	delRec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/user:owner@example.com", "", headersFor("owner@example.com"))
	if delRec.Code != http.StatusBadRequest || !strings.Contains(delRec.Body.String(), "deployment owner") {
		t.Fatalf("DELETE /api/access/principals/user:owner@example.com: status %d, want 400; body: %s", delRec.Code, delRec.Body.String())
	}
}
