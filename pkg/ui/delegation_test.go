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
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

type delegationHarness struct {
	srv        *ui.Server
	client     *ui.Client
	ecKey      *ecdsa.PrivateKey
	issuer     string
	audience   string
	accessCode string
	logs       interface{ String() string }
}

func newDelegationHarness(t *testing.T) *delegationHarness {
	t.Helper()
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	logs := ui.LogBufferForTest(t)

	ecKey, _, _, jwksSrv := newHubJWKSServer(t)
	const (
		issuer     = "https://grain.example.com"
		audience   = "https://my-grain.grain.example.com"
		accessCode = "test-access-code-secret-123456"
	)

	srv, client := testServer(t)
	client.Config.Actor = ui.DefaultActor("owner@example.com")
	client.Config.HubIssuer = issuer
	client.Config.HubAudience = audience
	client.Config.HubKeysURL = jwksSrv.URL
	ui.SetAccessCodeForTest(srv, accessCode)

	return &delegationHarness{
		srv:        srv,
		client:     client,
		ecKey:      ecKey,
		issuer:     issuer,
		audience:   audience,
		accessCode: accessCode,
		logs:       logs,
	}
}

func (h *delegationHarness) mintDelegated(t *testing.T, personEmail, appSA, appName, grantID, access string) string {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	actObj := map[string]any{
		"sub":  appSA,
		"kind": "service",
	}
	if appName != "" {
		actObj["app"] = appName
	}
	if grantID != "" {
		actObj["grant"] = grantID
	}
	if access != "" {
		actObj["access"] = access
	}
	actBytes, err := json.Marshal(actObj)
	if err != nil {
		t.Fatal(err)
	}
	claims := ui.HubClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    h.issuer,
			Subject:   personEmail,
			Audience:  jwt.ClaimStrings{h.audience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
			ID:        "jti-deleg-1",
		},
		Kind:      webauth.PrincipalHuman,
		RequestID: "rid-deleg-1",
		Act:       actBytes,
	}
	return signHubES256(t, h.ecKey, "reg-key-1", claims)
}

func (h *delegationHarness) mintDirect(t *testing.T, sub string, kind model.PrincipalKind) string {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	claims := ui.HubClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    h.issuer,
			Subject:   sub,
			Audience:  jwt.ClaimStrings{h.audience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
			ID:        "jti-direct-1",
		},
		Kind:      webauth.PrincipalKind(kind),
		RequestID: "rid-direct-1",
	}
	return signHubES256(t, h.ecKey, "reg-key-1", claims)
}

func (h *delegationHarness) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	return serveFrom(h.srv, "203.0.113.10:443", method, path, body, headers)
}

func TestDelegatedRequestNeedsPersonAndDelegateEntry(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		bob     = "bob@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
		otherSA = "other-bot@my-proj.iam.gserviceaccount.com"
	)

	if _, err := h.client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "user:" + alice,
		Role:   "grain/operator",
		Reason: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "delegate:" + slackSA,
		Role:   "grain/delegated",
		Reason: "workspace slack bot",
		Label:  "slack-bot",
	}); err != nil {
		t.Fatal(err)
	}
	// otherSA is allowed ONLY as a direct serviceAccount:, NOT as a delegate:.
	if _, err := h.client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "serviceAccount:" + otherSA,
		Role:   "grain/operator",
		Reason: "direct automation",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Both alice (user:) and slackSA (delegate:) are allowed -> 200 OK.
	tokOK := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")
	recOK := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokOK,
	})
	if recOK.Code != http.StatusOK {
		t.Fatalf("both person and delegate allowed: status = %d %s, want 200", recOK.Code, recOK.Body.String())
	}

	// 2. Person not allowed (bob), even with valid delegate: entry and valid access code -> 403.
	tokNoPerson := h.mintDelegated(t, bob, slackSA, "slack-bot", "dg_01", "write")
	recNoPerson := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokNoPerson,
		"Authorization":            "Bearer " + h.accessCode,
	})
	if recNoPerson.Code != http.StatusForbidden {
		t.Fatalf("unallowed person + allowed delegate: status = %d %s, want 403", recNoPerson.Code, recNoPerson.Body.String())
	}

	// 3. Person allowed (alice), but app has only serviceAccount: entry (not delegate:) -> 403 delegate_not_allowed.
	tokNoDelegate := h.mintDelegated(t, alice, otherSA, "other-bot", "dg_02", "write")
	recNoDelegate := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokNoDelegate,
		"Authorization":            "Bearer " + h.accessCode,
	})
	if recNoDelegate.Code != http.StatusForbidden || !strings.Contains(recNoDelegate.Body.String(), `"delegate_not_allowed"`) {
		t.Fatalf("allowed person + serviceAccount-only app: status = %d %s, want 403 delegate_not_allowed", recNoDelegate.Code, recNoDelegate.Body.String())
	}
}

func TestDelegateNotAllowedRefusalNamesTheApp(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	if _, err := h.client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "user:" + alice,
		Role:   "grain/operator",
		Reason: "owner",
	}); err != nil {
		t.Fatal(err)
	}

	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")

	// 1. API request gets JSON {"error": ..., "code": "delegate_not_allowed", "delegatingApp": "<email>"}.
	recAPI := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tok,
	})
	if recAPI.Code != http.StatusForbidden {
		t.Fatalf("API refusal status = %d, want 403", recAPI.Code)
	}
	var body struct {
		Error         string `json:"error"`
		Code          string `json:"code"`
		DelegatingApp string `json:"delegatingApp"`
	}
	if err := json.Unmarshal(recAPI.Body.Bytes(), &body); err != nil {
		t.Fatalf("JSON decode: %v (%s)", err, recAPI.Body.String())
	}
	if body.Code != "delegate_not_allowed" || body.DelegatingApp != slackSA {
		t.Fatalf("API refusal body = %+v, want code=delegate_not_allowed delegatingApp=%s", body, slackSA)
	}

	// 2. Browser request gets 403 HTML page naming "<label> is not allowed to act for people on this grain".
	recHTML := h.do(http.MethodGet, "/", "", map[string]string{
		ui.RegistryAssertionHeader: tok,
		"Accept":                   "text/html",
	})
	if recHTML.Code != http.StatusForbidden {
		t.Fatalf("HTML refusal status = %d, want 403", recHTML.Code)
	}
	if !strings.Contains(recHTML.Body.String(), "slack-bot is not allowed to act for people on this grain") ||
		!strings.Contains(recHTML.Body.String(), slackSA) {
		t.Fatalf("HTML refusal body missing expected message:\n%s", recHTML.Body.String())
	}
}

func TestDelegatedRoleIsPersonIntersectDelegate(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		bob     = "bob@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)

	// alice is grain/operator; bob is grain/viewer; slackSA is grain/delegated.
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/operator", Reason: "owner"},
		{Entry: "user:" + bob, Role: "grain/viewer", Reason: "viewer"},
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot", Label: "slack-bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Alice (operator) ∩ slack-bot (delegated):
	//    - Can file a task (tasks:create is in both).
	//    - Cannot write secrets (secrets:write is in operator, NOT in grain/delegated).
	tokAlice := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")
	recCreate := h.do(http.MethodPost, "/api/tasks", `{"title":"from slack","body":"hello","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tokAlice,
	})
	if recCreate.Code != http.StatusCreated && recCreate.Code != http.StatusOK {
		t.Fatalf("alice via slack-bot POST /api/tasks = %d %s, want 201/200", recCreate.Code, recCreate.Body.String())
	}

	recSecrets := h.do(http.MethodPut, "/api/secrets/my-secret/my-key", `{"value":"top-secret"}`, map[string]string{
		ui.RegistryAssertionHeader: tokAlice,
	})
	if recSecrets.Code != http.StatusForbidden {
		t.Fatalf("alice via slack-bot PUT /api/secrets/my-secret/my-key = %d %s, want 403", recSecrets.Code, recSecrets.Body.String())
	}

	// 2. Bob (viewer) ∩ slack-bot (delegated):
	//    - Can read tasks (tasks:read is in both).
	//    - Cannot file a task (tasks:create is in grain/delegated, NOT in grain/viewer).
	tokBob := h.mintDelegated(t, bob, slackSA, "slack-bot", "dg_02", "write")
	recBobRead := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokBob,
	})
	if recBobRead.Code != http.StatusOK {
		t.Fatalf("bob via slack-bot GET /api/tasks = %d %s, want 200", recBobRead.Code, recBobRead.Body.String())
	}
	recBobCreate := h.do(http.MethodPost, "/api/tasks", `{"title":"bob task","body":"no","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tokBob,
	})
	if recBobCreate.Code != http.StatusForbidden {
		t.Fatalf("bob via slack-bot POST /api/tasks = %d %s, want 403", recBobCreate.Code, recBobCreate.Body.String())
	}
}

func TestDemotingThePersonStopsDelegatedWrites(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/contributor", Reason: "contributor"},
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	// Mint the delegated token once; reuse the exact same token after demoting/removing Alice.
	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")

	// Initially Alice can create a task.
	if rec := h.do(http.MethodPost, "/api/tasks", `{"title":"t1","body":"b","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tok,
	}); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("initial POST /api/tasks = %d %s, want 201/200", rec.Code, rec.Body.String())
	}

	// Demote Alice to grain/viewer -> same JWT immediately loses tasks:create (403), while GET still works.
	if _, _, found, err := h.client.Store.SetAllowedPrincipalRole(ctx, "user:"+alice, "grain/viewer"); err != nil || !found {
		t.Fatalf("SetAllowedPrincipalRole: %v, %v", found, err)
	}
	if rec := h.do(http.MethodPost, "/api/tasks", `{"title":"t2","body":"b","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tok,
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("after demotion POST /api/tasks = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tok,
	}); rec.Code != http.StatusOK {
		t.Fatalf("after demotion GET /api/tasks = %d %s, want 200", rec.Code, rec.Body.String())
	}

	// Remove Alice from allowlist -> same JWT is refused even on GET (403).
	if _, err := h.client.DenyPrincipal(ctx, "user:"+alice); err != nil {
		t.Fatalf("DenyPrincipal: %v", err)
	}
	if rec := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tok,
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("after removal GET /api/tasks = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestReadOnlyDelegationRefusesWrites(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/operator", Reason: "owner"},
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	tokRead := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_read", "read")

	// GET and HEAD succeed.
	if rec := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokRead,
	}); rec.Code != http.StatusOK {
		t.Fatalf("GET with read delegation = %d %s, want 200", rec.Code, rec.Body.String())
	}

	// POST is refused with 403 and code: "read_only_delegation".
	recPost := h.do(http.MethodPost, "/api/tasks", `{"title":"t","body":"b","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tokRead,
	})
	if recPost.Code != http.StatusForbidden || !strings.Contains(recPost.Body.String(), `"read_only_delegation"`) {
		t.Fatalf("POST with read delegation = %d %s, want 403 read_only_delegation", recPost.Code, recPost.Body.String())
	}
}

func TestPersonOnlyOperationsRefuseDelegation(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)

	// Custom delegate role that includes tasks:write and access:read (non-dangerous scopes).
	installScopeSets(t, model.APIScopeSet{
		Name:        "delegated-plus",
		Description: "delegated plus access:read",
		Scopes:      []string{"tasks:read", "tasks:write", "access:read", "capabilities:read"},
		Version:     1,
	})

	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/operator", Reason: "owner"},
		{Entry: "delegate:" + slackSA, Role: "delegated-plus", Reason: "slack bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")
	hdr := map[string]string{ui.RegistryAssertionHeader: tok}

	// 1. POST/PATCH/DELETE /api/access/principals -> 403 requires_person.
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/access/principals", `{"entry":"user:carol@example.com","role":"grain/viewer"}`},
		{http.MethodPatch, "/api/access/principals/user:" + alice, `{"role":"grain/viewer"}`},
		{http.MethodDelete, "/api/access/principals/user:" + alice, ""},
	} {
		rec := h.do(tc.method, tc.path, tc.body, hdr)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"requires_person"`) {
			t.Fatalf("%s %s = %d %s, want 403 requires_person", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// 2. Minting a gma_ token with a dangerous scope -> 403 requires_person.
	recTok := h.do(http.MethodPost, "/api/external-agents/tokens",
		`{"name":"bot-tok","scopes":["tasks:read","secrets:write"]}`, hdr)
	if recTok.Code != http.StatusForbidden || !strings.Contains(recTok.Body.String(), `"requires_person"`) {
		t.Fatalf("delegated mint dangerous token = %d %s, want 403 requires_person", recTok.Code, recTok.Body.String())
	}

	// 3. Approving a held task that carries a person-only grant (priority-dispatch) -> 403 requires_person.
	railTask(t, h.client, "held-priority-1", "", nil, model.PriorityDispatchCapability)
	recApprove := h.do(http.MethodPost, "/api/tasks/held-priority-1/approve", `{}`, hdr)
	if recApprove.Code != http.StatusForbidden || !strings.Contains(recApprove.Body.String(), `"requires_person"`) {
		t.Fatalf("delegated approve person-only grant = %d %s, want 403 requires_person", recApprove.Code, recApprove.Body.String())
	}

	// 3b. The same for admission above standard, which replaced the
	// priority-dispatch and durable grants (schema migration 122): a task
	// holding no person-only grant but admission bypass or durable is
	// still a person's to approve directly.
	for _, adm := range []model.Admission{model.AdmissionBypass, model.AdmissionDurable} {
		id := "held-admission-" + string(adm)
		railTask(t, h.client, id, "", func(task *model.Task) { task.Admission = adm })
		rec := h.do(http.MethodPost, "/api/tasks/"+id+"/approve", `{}`, hdr)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"requires_person"`) ||
			!strings.Contains(rec.Body.String(), "admission "+string(adm)) {
			t.Fatalf("delegated approve admission %s = %d %s, want 403 requires_person naming the admission", adm, rec.Code, rec.Body.String())
		}
	}

	// 4. Registering a hostd (POST /api/hostds) -> 403 requires_person.
	recHostd := h.do(http.MethodPost, "/api/hostds",
		`{"name":"remote-1","owner":"hostd@my-proj.iam.gserviceaccount.com"}`, hdr)
	if recHostd.Code != http.StatusForbidden || !strings.Contains(recHostd.Body.String(), `"requires_person"`) {
		t.Fatalf("delegated POST /api/hostds = %d %s, want 403 requires_person", recHostd.Code, recHostd.Body.String())
	}

	// 5. Connecting a hostd (/api/hostd/connect) with a delegated token -> 403.
	h.client.Config.HostdRemote = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	recConnect := h.do(http.MethodGet, "/api/hostd/connect", "", hdr)
	if recConnect.Code != http.StatusForbidden {
		t.Fatalf("delegated GET /api/hostd/connect = %d %s, want 403", recConnect.Code, recConnect.Body.String())
	}
}

func TestDelegatedWriteRecordsAppAndPerson(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/operator", Reason: "owner"},
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot", Label: "slack-bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_audit_99", "write")
	hdr := map[string]string{ui.RegistryAssertionHeader: tok}

	// 1. GET /api/auth/user and GET /api/auth/whoami report string actor/onBehalfOf/via;
	//    GET /api/whoami reports WirePrincipal actor/onBehalfOf and via.
	for _, path := range []string{"/api/auth/user", "/api/auth/whoami"} {
		rec := h.do(http.MethodGet, path, "", hdr)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", path, rec.Code, rec.Body.String())
		}
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if info["actor"] != "service:"+slackSA || info["onBehalfOf"] != "user:"+alice || info["via"] != "slack-bot" {
			t.Fatalf("GET %s = %+v, want actor=service:%s onBehalfOf=user:%s via=slack-bot", path, info, slackSA, alice)
		}
	}
	recWhoami := h.do(http.MethodGet, "/api/whoami", "", hdr)
	if recWhoami.Code != http.StatusOK {
		t.Fatalf("GET /api/whoami = %d %s, want 200", recWhoami.Code, recWhoami.Body.String())
	}
	var whoamiResp ui.WhoamiResponse
	if err := json.Unmarshal(recWhoami.Body.Bytes(), &whoamiResp); err != nil {
		t.Fatalf("decode /api/whoami: %v", err)
	}
	if whoamiResp.Actor.Kind != "service" || whoamiResp.Actor.ID != slackSA ||
		whoamiResp.OnBehalfOf == nil || whoamiResp.OnBehalfOf.Kind != "human" || whoamiResp.OnBehalfOf.ID != alice ||
		whoamiResp.Via != "slack-bot" || whoamiResp.Actor.Label != "alice@example.com (via slack-bot)" {
		t.Fatalf("GET /api/whoami = %+v, want service %s on behalf of %s via slack-bot", whoamiResp, slackSA, alice)
	}

	// 2. Filing a task via delegation records Actor=service:<slackSA>, OnBehalfOf=human:<alice>, Creator=<alice>,
	//    and logs registry_rid, registry_jti, and delegation_grant.
	logStart := len(h.logs.String())
	recCreate := h.do(http.MethodPost, "/api/tasks", `{"title":"delegated task","body":"from slack","no_repo":true}`, hdr)
	if recCreate.Code != http.StatusCreated && recCreate.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks = %d %s, want 201/200", recCreate.Code, recCreate.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	storedTask, err := h.client.Store.GetTask(ctx, created.ID)
	if err != nil || storedTask == nil {
		t.Fatalf("GetTask(%q): %v, %v", created.ID, storedTask, err)
	}
	if storedTask.Creator != alice {
		t.Fatalf("storedTask.Creator = %q, want %q", storedTask.Creator, alice)
	}
	wantActor := model.Principal{Kind: model.PrincipalService, ID: slackSA, Source: "registry"}
	wantBehalf := model.Principal{Kind: model.PrincipalHuman, ID: alice, Source: "registry"}
	if storedTask.Origin.Attribution.Actor != wantActor || storedTask.Origin.Attribution.OnBehalfOf == nil || *storedTask.Origin.Attribution.OnBehalfOf != wantBehalf {
		t.Fatalf("storedTask.Origin.Attribution = %+v, want Actor=%+v OnBehalfOf=%+v", storedTask.Origin.Attribution, wantActor, wantBehalf)
	}
	if gotLabel := ui.PrincipalLabel(storedTask.Origin.Attribution.Actor, storedTask.Origin.Attribution.OnBehalfOf); gotLabel != "alice@example.com (via slack-bot)" {
		t.Fatalf("PrincipalLabel = %q, want %q", gotLabel, "alice@example.com (via slack-bot)")
	}

	// Check request log fields (#AU-26.5).
	logOut := h.logs.String()[logStart:]
	for _, wantLog := range []string{`registry_rid="rid-deleg-1"`, `registry_jti="jti-deleg-1"`, `delegation_grant="dg_audit_99"`} {
		if !strings.Contains(logOut, wantLog) {
			t.Fatalf("request log missing %q:\n%s", wantLog, logOut)
		}
	}
}

func TestDelegationLeavesCSRFAndBreakGlassUnchanged(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "user:" + alice, Role: "grain/operator", Reason: "owner"},
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Cross-site browser POST (Origin: https://evil.example.com) is still refused by CSRF protection (403),
	//    even when carrying a valid delegated registry JWT.
	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")
	recCSRF := h.do(http.MethodPost, "/api/tasks", `{"title":"csrf","body":"no","no_repo":true}`, map[string]string{
		ui.RegistryAssertionHeader: tok,
		"Origin":                   "https://evil.example.com",
		"Sec-Fetch-Site":           "cross-site",
	})
	if recCSRF.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST with delegated JWT = %d %s, want 403", recCSRF.Code, recCSRF.Body.String())
	}

	// 2. Unforwarded loopback caller with the access code still has full break-glass operator access.
	recBreakGlass := serveFrom(h.srv, "127.0.0.1:4242", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + h.accessCode,
	})
	if recBreakGlass.Code != http.StatusOK {
		t.Fatalf("loopback break-glass GET /api/tasks = %d %s, want 200", recBreakGlass.Code, recBreakGlass.Body.String())
	}
}

func TestPlainHubServicePrincipalNeedsServiceAccountEntry(t *testing.T) {
	h := newDelegationHarness(t)
	ctx := r503Ctx(t)
	const (
		ciSA    = "ci-bot@my-proj.iam.gserviceaccount.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)

	// slackSA has ONLY a delegate: entry; ciSA has a serviceAccount: entry.
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "delegate:" + slackSA, Role: "grain/delegated", Reason: "slack bot"},
		{Entry: "serviceAccount:" + ciSA, Role: "grain/viewer", Reason: "ci bot"},
	} {
		if _, err := h.client.AllowPrincipal(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Direct registry JWT (kind="service", no act) for slackSA (which only has delegate:) is refused (403).
	tokSlackDirect := h.mintDirect(t, slackSA, model.PrincipalService)
	if rec := h.do(http.MethodGet, "/api/tasks", "", map[string]string{
		ui.RegistryAssertionHeader: tokSlackDirect,
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("direct service JWT with only delegate: entry = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// 2. Direct registry JWT (kind="service", no act) for ciSA (which has serviceAccount:) is admitted (200)
	//    and attributed to service:ciSA with no OnBehalfOf.
	tokCIDirect := h.mintDirect(t, ciSA, model.PrincipalService)
	recCI := h.do(http.MethodGet, "/api/whoami", "", map[string]string{
		ui.RegistryAssertionHeader: tokCIDirect,
	})
	if recCI.Code != http.StatusOK {
		t.Fatalf("direct service JWT with serviceAccount: entry = %d %s, want 200", recCI.Code, recCI.Body.String())
	}
	var whoamiCI ui.WhoamiResponse
	if err := json.Unmarshal(recCI.Body.Bytes(), &whoamiCI); err != nil {
		t.Fatal(err)
	}
	if whoamiCI.Actor.Kind != "service" || whoamiCI.Actor.ID != ciSA || whoamiCI.OnBehalfOf != nil {
		t.Fatalf("direct service whoami = %+v, want actor=service:%s and no onBehalfOf", whoamiCI, ciSA)
	}
}
