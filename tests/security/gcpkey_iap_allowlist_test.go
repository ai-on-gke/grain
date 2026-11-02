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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken verifies
// Finding 1 of the 2026-11-02 security audit:
//  1. gcp-key agent service accounts (single or pooled) are not on the default
//     IAP allowlist and get 403 on both /api/tasks and /api/external-agents/tokens.
//  2. A PrincipalService that IS admitted without an explicit -ui-allowed-principals
//     entry (such as grain-daemon@<project> under the default allowlist, or an
//     in-process PrincipalService context) cannot mint or rotate a dangerous
//     external-agent token.
//  3. A PrincipalService explicitly listed in AllowedPrincipals (and outside the
//     gcp-key pool) can mint and rotate one, as can the human owner.
func TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	mc := model.DefaultConfig()
	mc.TargetRepos = []string{"acme/widgets"}
	mc.ExternalAgentAPI = true
	mc.GCPProject = "my-proj"
	mc.GCPServiceAccountEmail = "grain-agent@my-proj.iam.gserviceaccount.com"
	mc.GCPServiceAccountCount = 2
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}

	uiCfg := ui.Config{
		Actor:                      ui.DefaultActor("owner@example.com"),
		LocalAPIToken:              "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567",
		ProvisionedServiceAccounts: []string{"grain-daemon@my-proj.iam.gserviceaccount.com"},
	}
	sharedIdentityProxy().configure(&uiCfg)

	client := ui.NewClient(uiCfg, store)
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
		req.RemoteAddr = "203.0.113.77:443"
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

	const dangerousBody = `{"name":"esc-cred","scopes":["tasks:admin","settings:write","secrets:write","capabilities:admin","merge:force","deployment:upgrade","deployment:reboot"]}`

	// 1. gcp-key pool accounts are refused at the gate on both read and mint routes.
	for _, poolSA := range []string{
		"grain-agent@my-proj.iam.gserviceaccount.com",
		"grain-agent-0@my-proj.iam.gserviceaccount.com",
		"grain-agent-1@my-proj.iam.gserviceaccount.com",
	} {
		if rec := serveIAP(poolSA, http.MethodGet, "/api/tasks", ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET /api/tasks as gcp-key SA %q: got %d, want 403: %s", poolSA, rec.Code, rec.Body)
		}
		if rec := serveIAP(poolSA, http.MethodPost, "/api/external-agents/tokens", dangerousBody); rec.Code != http.StatusForbidden {
			t.Errorf("POST /api/external-agents/tokens as gcp-key SA %q: got %d, want 403: %s", poolSA, rec.Code, rec.Body)
		}
	}

	// 2. Owner mints a dangerous external-agent token (used below to test rotation too).
	recOwner := serveIAP("owner@example.com", http.MethodPost, "/api/external-agents/tokens", dangerousBody)
	if recOwner.Code != http.StatusCreated {
		t.Fatalf("owner POST /api/external-agents/tokens: got %d, want 201: %s", recOwner.Code, recOwner.Body)
	}
	var ownerIssued ui.IssuedExternalAgentToken
	if err := json.Unmarshal(recOwner.Body.Bytes(), &ownerIssued); err != nil {
		t.Fatal(err)
	}

	// 3. Default-admitted PrincipalService (grain-daemon@my-proj) passes the gate on
	// GET /api/tasks, and can mint a non-dangerous token, but CANNOT mint or rotate a
	// dangerous external-agent token because it is not in an explicit allowlist.
	if rec := serveIAP("grain-daemon@my-proj.iam.gserviceaccount.com", http.MethodGet, "/api/tasks", ""); rec.Code != http.StatusOK {
		t.Fatalf("grain-daemon GET /api/tasks: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveIAP("grain-daemon@my-proj.iam.gserviceaccount.com", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"daemon-esc","scopes":["settings:write"]}`); rec.Code != http.StatusForbidden {
		t.Fatalf("default-admitted SA POST dangerous token: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveIAP("grain-daemon@my-proj.iam.gserviceaccount.com", http.MethodPost, "/api/external-agents/tokens/"+ownerIssued.ID+"/rotate", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("default-admitted SA rotate dangerous token: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 4. An in-process PrincipalService not named in an explicit allowlist is also
	// refused when minting or rotating a dangerous external-agent token, even when an
	// explicit allowlist exists for other principals.
	client.Config.AllowedPrincipals = []string{
		"user:owner@example.com",
		"serviceAccount:explicit-ci@my-proj.iam.gserviceaccount.com",
	}
	unlistedSvcCtx := ui.WithPrincipal(context.Background(), model.Principal{
		Kind: model.PrincipalService,
		ID:   "unlisted-svc@my-proj.iam.gserviceaccount.com",
	})
	inProcReq := httptest.NewRequestWithContext(unlistedSvcCtx, http.MethodPost, "/api/external-agents/tokens",
		strings.NewReader(`{"name":"unlisted-esc","scopes":["tasks:admin"]}`))
	inProcReq.RemoteAddr = ""
	inProcReq.Header.Set("Content-Type", "application/json")
	inProcRec := httptest.NewRecorder()
	srv.ServeHTTP(inProcRec, inProcReq)
	if inProcRec.Code != http.StatusForbidden {
		t.Fatalf("unlisted in-process PrincipalService POST dangerous token: got %d, want 403: %s", inProcRec.Code, inProcRec.Body)
	}

	// 5. The explicitly listed serviceAccount CAN mint and rotate a dangerous token.
	recExplicit := serveIAP("explicit-ci@my-proj.iam.gserviceaccount.com", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"explicit-esc","scopes":["tasks:admin"]}`)
	if recExplicit.Code != http.StatusCreated {
		t.Fatalf("explicitly allowed SA POST dangerous token: got %d, want 201: %s", recExplicit.Code, recExplicit.Body)
	}
	if rec := serveIAP("explicit-ci@my-proj.iam.gserviceaccount.com", http.MethodPost, "/api/external-agents/tokens/"+ownerIssued.ID+"/rotate", ""); rec.Code != http.StatusOK {
		t.Fatalf("explicitly allowed SA rotate dangerous token: got %d, want 200: %s", rec.Code, rec.Body)
	}
}
