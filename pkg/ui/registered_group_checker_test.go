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

	"github.com/ai-on-gke/grain/pkg/ui"
)

type testCorporateGroupChecker struct{}

func (testCorporateGroupChecker) HandlesGroup(groupEmail string) bool {
	return strings.HasSuffix(groupEmail, "@directory.example")
}

func (testCorporateGroupChecker) IsMember(_ context.Context, groupEmail, memberEmail string) (bool, error) {
	return groupEmail == "eng-leads@directory.example" && memberEmail == "alice@example.com", nil
}

func TestRegisterGroupMemberCheckerAdmitsAllowedPrincipalGroup(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)

	unreg := ui.RegisterGroupMemberChecker(testCorporateGroupChecker{})
	t.Cleanup(unreg)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"group:eng-leads@directory.example"}

	aliceTok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "sub-alice", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	rec := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: aliceTok})
	if rec.Code != http.StatusOK {
		t.Fatalf("registered corporate group member: got %d, want 200: %s", rec.Code, rec.Body)
	}

	bobTok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "bob@example.com", "sub-bob", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	recBob := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: bobTok})
	if recBob.Code != http.StatusForbidden {
		t.Fatalf("non-member against scoped corporate group: got %d, want 403: %s", recBob.Code, recBob.Body)
	}
	if !strings.Contains(recBob.Body.String(), "not on the allowed users and groups list") {
		t.Fatalf("unexpected refusal body for non-member: %s", recBob.Body)
	}
}
