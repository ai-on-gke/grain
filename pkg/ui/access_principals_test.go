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
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAllowedPrincipalsFlagMigrationHappensExactlyOnce(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	logs := ui.LogBufferForTest(t)

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	// 1. First start with -ui-allowed-principals and an empty DB list:
	// user: and group: entries are imported once; serviceAccount: and domain:
	// entries are dropped, logged individually, and surfaced in GET /api/access/principals.
	cfg1 := ui.Config{
		Actor:             ui.DefaultActor("operator@example.com"),
		LocalAPIToken:     testAccessCode,
		AllowedPrincipals: []string{"user:Alice@Example.com, group:Eng@Example.com", "serviceAccount:ci@proj.iam.gserviceaccount.com", "domain:example.com"},
	}
	client1 := ui.NewClient(cfg1, store)
	if err := client1.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatalf("first MigrateAllowedPrincipals: %v", err)
	}

	resp1, err := client1.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListAllowedPrincipals: %v", err)
	}
	if len(resp1.Entries) != 2 || resp1.Entries[0].Entry != "group:eng@example.com" || resp1.Entries[1].Entry != "user:alice@example.com" {
		t.Fatalf("imported entries = %+v, want [group:eng@example.com user:alice@example.com]", resp1.Entries)
	}
	if len(resp1.DroppedFlagEntries) != 2 ||
		resp1.DroppedFlagEntries[0] != "serviceAccount:ci@proj.iam.gserviceaccount.com" ||
		resp1.DroppedFlagEntries[1] != "domain:example.com" {
		t.Fatalf("DroppedFlagEntries = %v, want [serviceAccount:ci@proj.iam.gserviceaccount.com domain:example.com]", resp1.DroppedFlagEntries)
	}
	for _, wantLog := range []string{
		"imported 2 allowed user/group entries from -ui-allowed-principals",
		`dropped "serviceAccount:ci@proj.iam.gserviceaccount.com"`,
		`dropped "domain:example.com"`,
	} {
		if !strings.Contains(logs.String(), wantLog) {
			t.Errorf("first start logs missing %q:\n%s", wantLog, logs.String())
		}
	}

	// 2. Remove every entry from the DB allowlist, then simulate a second daemon
	// start with -ui-allowed-principals / GRAIN_ALLOWED_PRINCIPALS still set:
	// the flag is NOT re-imported, a deprecation warning is logged, and the DB
	// list remains empty.
	if _, err := client1.DenyPrincipal(ctx, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := client1.DenyPrincipal(ctx, "group:eng@example.com"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "user:bob@example.com,user:alice@example.com")
	cfg2 := ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: testAccessCode,
	}
	client2 := ui.NewClient(cfg2, store)
	if err := client2.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatalf("second MigrateAllowedPrincipals: %v", err)
	}
	resp2, err := client2.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp2.Entries) != 0 {
		t.Fatalf("second start re-imported entries into emptied DB: %+v", resp2.Entries)
	}
	if !strings.Contains(logs.String(), "$GRAIN_ALLOWED_PRINCIPALS is deprecated and ignored") {
		t.Errorf("second start logs missing deprecation warning:\n%s", logs.String())
	}

	// 3. Also verify that if a fresh DB already has an entry before the flag is
	// ever seen, setting the flag later does NOT import it.
	storePreseeded, _ := sqlitetest.Open(t)
	if _, _, err := storePreseeded.AddAllowedPrincipal(ctx, "user:seeded@example.com", "installer", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	client3 := ui.NewClient(ui.Config{
		AllowedPrincipals: []string{"user:from-flag@example.com"},
	}, storePreseeded)
	if err := client3.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatal(err)
	}
	resp3, err := client3.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp3.Entries) != 1 || resp3.Entries[0].Entry != "user:seeded@example.com" {
		t.Fatalf("pre-seeded DB imported flag entries: %+v", resp3.Entries)
	}
}

func TestAllowedPrincipalsLiveEnforcementEqualPrivilegesAndIdempotency(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)

	mint := func(email string) string {
		return signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))
	}

	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "team@example.com" && member == "teammate@example.com", nil
	})

	bearer := map[string]string{
		"Authorization": "Bearer " + testAccessCode,
		"Content-Type":  "application/json",
	}

	// 1. Before adding anyone, alice@example.com gets 403 naming Settings -> Access.
	recDenied := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: mint("alice@example.com"),
	})
	if recDenied.Code != http.StatusForbidden || !strings.Contains(recDenied.Body.String(), "Settings") {
		t.Fatalf("unlisted alice got %d %s, want 403 naming Settings -> Access", recDenied.Code, recDenied.Body)
	}

	// 2. Access-code holder adds user:alice@example.com -> 201 Created, and
	// re-adding the same entry is an idempotent no-op (200 OK, created=false).
	recAdd1 := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals", `{"entry":"user:Alice@Example.com","reason":"on-call lead"}`, bearer)
	if recAdd1.Code != http.StatusCreated {
		t.Fatalf("access-code POST /api/access/principals: got %d, want 201: %s", recAdd1.Code, recAdd1.Body)
	}
	var addResp1 ui.AllowPrincipalResponse
	if err := json.Unmarshal(recAdd1.Body.Bytes(), &addResp1); err != nil {
		t.Fatal(err)
	}
	if !addResp1.Created || addResp1.Principal.Entry != "user:alice@example.com" || addResp1.Principal.AddedBy != "the access code" {
		t.Fatalf("first add response = %+v", addResp1)
	}

	recAddDup := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals", `{"entry":"user:alice@example.com"}`, bearer)
	if recAddDup.Code != http.StatusOK {
		t.Fatalf("duplicate POST /api/access/principals: got %d, want 200: %s", recAddDup.Code, recAddDup.Body)
	}
	var addRespDup ui.AllowPrincipalResponse
	if err := json.Unmarshal(recAddDup.Body.Bytes(), &addRespDup); err != nil {
		t.Fatal(err)
	}
	if addRespDup.Created || addRespDup.Principal.Entry != "user:alice@example.com" {
		t.Fatalf("duplicate add response = %+v, want Created=false", addRespDup)
	}

	// 3. Without restarting, alice@example.com is immediately admitted at
	// grain/operator: she can view the list and add entries (group:team@example.com
	// and user:bob@example.com) at roles with no dangerous scope -- grain/operator
	// itself only the access code assigns (docs/design/access-rbac.md §3).
	aliceHeaders := map[string]string{
		ui.IAPAssertionHeader: mint("alice@example.com"),
		"Content-Type":        "application/json",
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/access/principals", "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user GET /api/access/principals: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"group:team@example.com","role":"grain/viewer"}`, aliceHeaders); rec.Code != http.StatusCreated {
		t.Fatalf("listed user POST group: got %d, want 201: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"user:bob@example.com","role":"grain/contributor"}`, aliceHeaders); rec.Code != http.StatusCreated {
		t.Fatalf("listed user POST bob: got %d, want 201: %s", rec.Code, rec.Body)
	}
	// alice holds grain/operator, so alice may give it -- with a reason.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"user:carol@example.com"}`, aliceHeaders); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reason") {
		t.Fatalf("listed user POST carol at grain/operator without a reason: got %d %s, want 400 naming the reason", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"user:carol@example.com","reason":"x"}`, aliceHeaders); rec.Code != http.StatusCreated {
		t.Fatalf("listed user POST carol at grain/operator with a reason: got %d %s, want 201", rec.Code, rec.Body)
	}

	// 4. Group member teammate@example.com is immediately admitted without restart;
	// outsider@example.com still gets 403.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: mint("teammate@example.com"),
	}); rec.Code != http.StatusOK {
		t.Fatalf("group member GET /api/tasks: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: mint("outsider@example.com"),
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider GET /api/tasks: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 5. Listed user alice removes bob@example.com -> takes effect immediately
	// without restart, recorded in config_audit as alice@example.com.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:bob@example.com"), "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user DELETE bob: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: mint("bob@example.com"),
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("removed bob GET /api/tasks: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 6. alice's own entry at grain/operator: alice removes it only with a
	// reason and a confirmation; the access code removes it outright, and
	// removing every entry leaves the access code working.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/"+url.PathEscape("group:team@example.com"), "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user DELETE group: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:alice@example.com")+"?reason=leaving", "", aliceHeaders); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confirm_self") {
		t.Fatalf("listed user self-DELETE at grain/operator unconfirmed: got %d, want 403 confirm_self: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "127.0.0.1:4242", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:alice@example.com"), "", bearer); rec.Code != http.StatusOK {
		t.Fatalf("access code DELETE alice (last entry): got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", aliceHeaders); rec.Code != http.StatusForbidden {
		t.Fatalf("self-removed alice GET /api/tasks: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "127.0.0.1:4242", http.MethodGet, "/api/access/principals", "", bearer); rec.Code != http.StatusOK {
		t.Fatalf("access code after emptying allowlist: got %d, want 200: %s", rec.Code, rec.Body)
	}

	// Verify config_audit recorded alice@example.com's changes.
	audits, err := client.Store.ConfigAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawAliceRemove bool
	for _, a := range audits {
		if a.Surface == "access" && a.By.Kind == model.PrincipalHuman && a.By.ID == "alice@example.com" && strings.Contains(a.Summary, "removed user:bob@example.com") {
			sawAliceRemove = true
		}
	}
	if !sawAliceRemove {
		t.Fatalf("expected config_audit entry for alice removing user:bob@example.com; got %+v", audits)
	}
}

func TestAllowedPrincipalsAgentScopeAndSandboxIsolation(t *testing.T) {
	srv, client := testServer(t)

	// Task 1: no access grants (only tasks:read, tasks:write).
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:read", "api:tasks:write")
	// Task 2: read-only access grant (api:access:read).
	seedAPITask(t, client, "2", "acme/widgets", "api:access:read")
	// Task 3: person-granted dangerous api:access:write + api:access:read.
	seedAPITask(t, client, "3", "acme/widgets", "api:access:read", "api:access:write", "api:tasks:write")

	// 1. Agent without access:read cannot even read; agent with access:read can read but CANNOT write (403).
	if code, _ := asAgent(t, srv, "1", http.MethodGet, "/api/access/principals", "", ""); code != http.StatusForbidden {
		t.Fatalf("task 1 GET /api/access/principals without grant: got %d, want 403", code)
	}
	if code, _ := asAgent(t, srv, "1", http.MethodPost, "/api/access/principals", `{"entry":"user:intruder@example.com"}`, "trying to add"); code != http.StatusForbidden {
		t.Fatalf("task 1 POST /api/access/principals without grant: got %d, want 403", code)
	}
	if code, resp := asAgent(t, srv, "2", http.MethodGet, "/api/access/principals", "", ""); code != http.StatusOK {
		t.Fatalf("task 2 GET /api/access/principals with access:read: got %d %s, want 200", code, resp)
	}
	if code, _ := asAgent(t, srv, "2", http.MethodPost, "/api/access/principals", `{"entry":"user:intruder@example.com"}`, "trying to add"); code != http.StatusForbidden {
		t.Fatalf("task 2 POST /api/access/principals with access:read only: got %d, want 403", code)
	}

	// 2. Agent WITH person-granted api:access:write but NO reason is refused with 403.
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/access/principals", `{"entry":"user:oncall@example.com"}`, ""); code != http.StatusForbidden || !strings.Contains(resp, "reason") {
		t.Fatalf("task 3 POST /api/access/principals with empty reason: got %d %s, want 403 naming reason", code, resp)
	}

	// 3. Sandbox CLI channel (X-Grain-Channel: sandbox-cli) is ALWAYS refused on
	// access:write, even when the task holds person-granted api:access:write and gives a reason!
	if code, resp := asSandboxAgent(t, srv, "3", "run-3", http.MethodPost, "/api/access/principals", `{"entry":"user:sandbox@example.com"}`, "from sandbox"); code != http.StatusForbidden {
		t.Fatalf("sandbox-cli POST /api/access/principals: got %d %s, want 403", code, resp)
	}

	// 4. Even with person-granted api:access:write and a reason, an agent
	// never changes who signs in (docs/design/access-rbac.md §3).
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/access/principals", `{"entry":"user:oncall@example.com","role":"grain/viewer"}`, "onboarding rotation"); code != http.StatusForbidden {
		t.Fatalf("task 3 POST /api/access/principals with reason: got %d %s, want 403", code, resp)
	}
	if _, err := client.AllowPrincipal(context.Background(), ui.AllowPrincipalRequest{Entry: "user:oncall@example.com", Role: "grain/viewer"}); err != nil {
		t.Fatal(err)
	}
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/access/principals", `{"entry":"user:oncall@example.com"}`, "duplicate probe"); code != http.StatusForbidden {
		t.Fatalf("task 3 POST /api/access/principals on existing entry: got %d %s, want 403", code, resp)
	}
	for _, try := range []struct{ method, body string }{
		{http.MethodPatch, `{"role":"grain/contributor"}`},
		{http.MethodDelete, ""},
	} {
		if code, resp := asAgent(t, srv, "3", try.method, "/api/access/principals/"+url.PathEscape("user:oncall@example.com"), try.body, "offboarding rotation"); code != http.StatusForbidden {
			t.Fatalf("task 3 %s /api/access/principals/{entry} with reason: got %d %s, want 403", try.method, code, resp)
		}
	}

	// 5. An agent without api:access:write cannot file an approved task granting api:access:write
	// (dangerous scope escalation is refused by POST /api/tasks for agents).
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks",
		`{"title":"escalate","repo":"acme/widgets","capabilities":["api:access:write"]}`, "escalate"); code != http.StatusForbidden {
		t.Fatalf("agent self-granting api:access:write via POST /api/tasks: got %d %s, want 403", code, resp)
	}
}

func TestMigrateAllowedPrincipalsRetriesAfterTransientFailureAndDropsServiceAccounts(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	mc := model.DefaultConfig()
	mc.GCPProject = "my-proj"
	mc.GCPServiceAccountEmail = "grain-agent@my-proj.iam.gserviceaccount.com"
	mc.GCPServiceAccountCount = 2
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: testAccessCode,
		AllowedPrincipals: []string{
			"user:alice@example.com",
			"user:grain-agent-1@my-proj.iam.gserviceaccount.com",
			"user:ci@my-proj.iam.gserviceaccount.com",
		},
	}, store)

	// 1. A cancelled context on first call fails without permanently latching
	// the migration as completed.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.MigrateAllowedPrincipals(cancelledCtx); err == nil {
		t.Fatal("MigrateAllowedPrincipals with cancelled context succeeded, want error")
	}

	// 2. Subsequent call with a live context runs the migration and drops
	// *.gserviceaccount.com / gcp-key pool entries rather than importing them.
	if err := client.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatalf("retry MigrateAllowedPrincipals: %v", err)
	}
	resp, err := client.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Entry != "user:alice@example.com" {
		t.Fatalf("imported entries = %+v, want [user:alice@example.com]", resp.Entries)
	}
	if len(resp.DroppedFlagEntries) != 2 ||
		resp.DroppedFlagEntries[0] != "user:grain-agent-1@my-proj.iam.gserviceaccount.com" ||
		resp.DroppedFlagEntries[1] != "user:ci@my-proj.iam.gserviceaccount.com" {
		t.Fatalf("DroppedFlagEntries = %v, want pool and SA user: entries dropped", resp.DroppedFlagEntries)
	}
}

func TestRegisteredHostdServiceOwnerDialsCleanlyWithoutOperatorAccessOrRefusalLog(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	logs := ui.LogBufferForTest(t)

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	mint := func(email string) string {
		return signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))
	}

	srv, client := testServer(t)
	ctx := context.Background()

	mc := model.DefaultConfig()
	mc.GCPProject = "my-proj"
	mc.GCPServiceAccountEmail = "grain-agent@my-proj.iam.gserviceaccount.com"
	if err := client.Store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: "group:admins@example.com", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	var groupChecks atomic.Int32
	client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, _, _ string) (bool, error) {
		groupChecks.Add(1)
		return true, nil
	})
	client.Config.HostdRemote = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	const hostdSA = "hostd-owner@my-proj.iam.gserviceaccount.com"
	if _, err := client.CreateHostd(ctx, ui.CreateHostdRequest{Name: "remote-1", Owner: hostdSA}); err != nil {
		t.Fatalf("CreateHostd: %v", err)
	}

	// 1. Registered hostd owner SA still gets 403 on operator API routes and
	// does not trigger Cloud Identity group checks.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: mint(hostdSA),
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("hostd SA GET /api/tasks = %d %s, want 403", rec.Code, rec.Body)
	}
	if got := groupChecks.Load(); got != 0 {
		t.Fatalf("GroupChecker called %d times on /api/tasks for SA, want 0", got)
	}

	// 2. Dialing /api/hostd/connect as the active registered hostd owner SA
	// reaches HostdRemote cleanly with 0 group checks and no spurious refusal log.
	logStart := len(logs.String())
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/hostd/connect", "", map[string]string{
		ui.IAPAssertionHeader: mint(hostdSA),
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("hostd SA GET /api/hostd/connect = %d %s, want 204", rec.Code, rec.Body)
	}
	if got := groupChecks.Load(); got != 0 {
		t.Fatalf("GroupChecker called %d times on /api/hostd/connect, want 0", got)
	}
	if connectLogs := logs.String()[logStart:]; strings.Contains(connectLogs, "refused verified principal") {
		t.Fatalf("active hostd owner emitted spurious refusal log:\n%s", connectLogs)
	}

	// 3. Revoking the hostd immediately cuts off /api/hostd/connect for that SA.
	if _, err := client.RevokeHostd(ctx, "remote-1"); err != nil {
		t.Fatalf("RevokeHostd: %v", err)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/hostd/connect", "", map[string]string{
		ui.IAPAssertionHeader: mint(hostdSA),
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked hostd SA GET /api/hostd/connect = %d %s, want 403", rec.Code, rec.Body)
	}

	// 4. Even if a gcp-key pool SA is placed on a hostd row directly in the store,
	// /api/hostd/connect refuses it with 403.
	if _, _, err := client.Store.CreateHostd(ctx, model.Hostd{
		Name:  "pool-owned",
		Owner: "grain-agent@my-proj.iam.gserviceaccount.com",
	}); err != nil {
		t.Fatal(err)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/hostd/connect", "", map[string]string{
		ui.IAPAssertionHeader: mint("grain-agent@my-proj.iam.gserviceaccount.com"),
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("gcp-key pool SA GET /api/hostd/connect = %d %s, want 403", rec.Code, rec.Body)
	}
}

// A service account is admitted only through an explicit serviceAccount:
// entry added with a reason, is attributed as a service, and an account whose
// key grain hands to tasks is refused at add time and at sign-in even when an
// entry for it is already in the table.
func TestServiceAccountAllowlistEntriesNeedAReasonAndNeverAdmitPoolAccounts(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	as := func(email string) map[string]string {
		return map[string]string{
			ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute)),
		}
	}

	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ctx := context.Background()
	const (
		poolSA = "grain-agent@my-proj.iam.gserviceaccount.com"
		setSA  = "iap-test-1@acme-pool.iam.gserviceaccount.com"
		botSA  = "release-bot@my-proj.iam.gserviceaccount.com"
	)
	mc := model.DefaultConfig()
	mc.GCPProject = "my-proj"
	mc.GCPServiceAccountEmail = poolSA
	mc.GCPServiceAccountCount = 2
	mc.MCPServers = map[string]protocol.ServerConfig{
		model.GCPCapabilityServerID: {
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
			Config: map[string]any{
				"permission_sets": []any{
					map[string]any{"name": "iap-test", "project": "acme-pool", "accounts": []any{setSA}},
				},
			},
		},
	}
	if err := client.Store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}

	// 1. Entries that are refused outright.
	for _, req := range []ui.AllowPrincipalRequest{
		{Entry: "serviceAccount:" + botSA},                     // no reason
		{Entry: "user:" + botSA, Reason: "x"},                  // an SA address as a user
		{Entry: "serviceAccount:bot@example.com", Reason: "x"}, // not an SA address
		{Entry: "domain:example.com", Reason: "x"},             // domain: stays unsupported
		{Entry: "serviceAccount:" + poolSA, Reason: "x"},       // gcp-key pool
		{Entry: "serviceAccount:GRAIN-AGENT-1@my-proj.iam.gserviceaccount.com", Reason: "x"},
		{Entry: "serviceAccount:" + setSA, Reason: "x"}, // gcp permission set
	} {
		if _, err := client.AllowPrincipal(ctx, req); err == nil {
			t.Errorf("AllowPrincipal(%+v) succeeded, want refused", req)
		}
	}

	// 2. Not on the list yet: refused.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", as(botSA)); rec.Code != http.StatusForbidden {
		t.Fatalf("unlisted SA GET /api/tasks = %d, want 403", rec.Code)
	}

	// 3. Added with a reason, it is listed with who, when and why, recorded in
	// config_audit, and admitted at once.
	bearer := map[string]string{"Authorization": "Bearer " + testAccessCode}
	rec := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals",
		`{"entry":"serviceAccount:Release-Bot@my-proj.iam.gserviceaccount.com","reason":"release pipeline"}`, bearer)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST serviceAccount: = %d %s, want 201", rec.Code, rec.Body)
	}
	list, err := client.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 || list.Entries[0].Entry != "serviceAccount:"+botSA || list.Entries[0].Reason != "release pipeline" || list.Entries[0].AddedBy == "" {
		t.Fatalf("entries = %+v, want serviceAccount:%s with its adder and reason", list.Entries, botSA)
	}
	audit, err := client.Store.ConfigAudit(ctx, 1)
	if err != nil || len(audit) != 1 || audit[0].Summary != "allowed serviceAccount:"+botSA+" as grain/operator (release pipeline)" {
		t.Fatalf("config audit = %+v, %v", audit, err)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", as(botSA)); rec.Code != http.StatusOK {
		t.Fatalf("listed SA GET /api/tasks = %d %s, want 200", rec.Code, rec.Body)
	}

	// 4. What the service account changes is attributed to it as a service.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"user:carol@example.com","role":"grain/viewer"}`, as(botSA)); rec.Code != http.StatusCreated {
		t.Fatalf("SA POST user: = %d %s, want 201", rec.Code, rec.Body)
	}
	audit, err = client.Store.ConfigAudit(ctx, 1)
	if err != nil || len(audit) != 1 || audit[0].By.Kind != model.PrincipalService || audit[0].By.ID != botSA {
		t.Fatalf("config audit = %+v, %v; want the change attributed to service %s", audit, err, botSA)
	}

	// 5. Pool accounts are refused at sign-in even with an entry in the
	// table, whatever role the entry gives them.
	for _, sa := range []string{poolSA, setSA} {
		if _, _, err := client.Store.AddAllowedPrincipal(ctx, "serviceAccount:"+sa, "test", "planted", time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{model.DefaultAccessRole, "grain/viewer", "grain/contributor", "grain/ci-read"} {
			if _, _, found, err := client.Store.SetAllowedPrincipalRole(ctx, "serviceAccount:"+sa, role); err != nil || !found {
				t.Fatalf("planting role %s on %s: %v, %v", role, sa, found, err)
			}
			for _, path := range []string{"/api/tasks", "/api/auth/user", "/api/ci/runs"} {
				rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, path, "", as(sa))
				if path == "/api/auth/user" {
					if strings.Contains(rec.Body.String(), `"access"`) {
						t.Fatalf("pool SA %s at role %s was given a role: %s", sa, role, rec.Body)
					}
					continue
				}
				if rec.Code != http.StatusForbidden {
					t.Fatalf("pool SA %s at role %s GET %s = %d, want 403", sa, role, path, rec.Code)
				}
			}
		}
	}
}

func TestDelegateEntriesNeedAReasonAndAPerson(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	as := func(email string) map[string]string {
		return map[string]string{
			ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute)),
		}
	}

	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ctx := context.Background()

	const (
		poolSA     = "grain-agent@my-proj.iam.gserviceaccount.com"
		setSA      = "iap-test-1@acme-pool.iam.gserviceaccount.com"
		operatorSA = "ops-bot@my-proj.iam.gserviceaccount.com"
		delegateSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	mc := model.DefaultConfig()
	mc.GCPProject = "my-proj"
	mc.GCPServiceAccountEmail = poolSA
	mc.GCPServiceAccountCount = 2
	mc.MCPServers = map[string]protocol.ServerConfig{
		model.GCPCapabilityServerID: {
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
			Config: map[string]any{
				"permission_sets": []any{
					map[string]any{"name": "iap-test", "project": "acme-pool", "accounts": []any{setSA}},
				},
			},
		},
	}
	if err := client.Store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "serviceAccount:" + operatorSA,
		Role:   model.DefaultAccessRole,
		Reason: "ops bot",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Missing reason -> 400; invalid email -> 400; invalid label -> 400; gcp-key pool -> 422.
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"missing reason", `{"entry":"delegate:` + delegateSA + `"}`, http.StatusBadRequest},
		{"human email", `{"entry":"delegate:alice@example.com","reason":"x"}`, http.StatusBadRequest},
		{"bad label", `{"entry":"delegate:` + delegateSA + `","reason":"x","label":"Bad_Label"}`, http.StatusBadRequest},
		{"pool SA", `{"entry":"delegate:` + poolSA + `","reason":"x"}`, http.StatusUnprocessableEntity},
		{"numbered pool SA", `{"entry":"delegate:grain-agent-1@my-proj.iam.gserviceaccount.com","reason":"x"}`, http.StatusUnprocessableEntity},
		{"permission-set SA", `{"entry":"delegate:` + setSA + `","reason":"x"}`, http.StatusUnprocessableEntity},
	} {
		rec := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals", tc.body, map[string]string{
			"Authorization": "Bearer " + testAccessCode,
		})
		if rec.Code != tc.want {
			t.Fatalf("%s: status = %d (%s), want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}

	// 2. A service account caller (even at grain/operator) is refused when adding a delegate: entry (403 requires_person).
	recSA := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals",
		`{"entry":"delegate:`+delegateSA+`","reason":"added by bot"}`, as(operatorSA))
	if recSA.Code != http.StatusForbidden || !strings.Contains(recSA.Body.String(), `"requires_person"`) {
		t.Fatalf("service account adding delegate: = %d %s, want 403 requires_person", recSA.Code, recSA.Body.String())
	}

	// 3. A human / loopback operator adding delegate: with a reason succeeds and defaults to grain/delegated.
	recOK := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals",
		`{"entry":"delegate:Slack-Bot@my-proj.iam.gserviceaccount.com","reason":"workspace slack bot","label":"slack-bot"}`,
		map[string]string{"Authorization": "Bearer " + testAccessCode})
	if recOK.Code != http.StatusCreated {
		t.Fatalf("human adding delegate: = %d %s, want 201", recOK.Code, recOK.Body.String())
	}
	list, err := client.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *model.AllowedPrincipal
	for i := range list.Entries {
		if list.Entries[i].Entry == "delegate:"+delegateSA {
			found = &list.Entries[i]
			break
		}
	}
	if found == nil || found.Role != model.DefaultDelegateAccessRole || found.Label != "slack-bot" || found.Reason != "workspace slack bot" {
		t.Fatalf("delegate entry = %+v, want role=%s label=slack-bot", found, model.DefaultDelegateAccessRole)
	}

	// 4. MigrateAllowedPrincipals ignores delegate: entries.
	flagStore, _ := sqlitetest.Open(t)
	flagClient := ui.NewClient(ui.Config{
		Actor:             ui.DefaultActor("operator@example.com"),
		AllowedPrincipals: []string{"user:alice@example.com", "delegate:flag-bot@my-proj.iam.gserviceaccount.com"},
	}, flagStore)
	if err := flagClient.MigrateAllowedPrincipals(ctx); err != nil {
		t.Fatal(err)
	}
	listAfterFlag, err := flagClient.ListAllowedPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listAfterFlag.Entries {
		if strings.Contains(e.Entry, "flag-bot") {
			t.Fatalf("-ui-allowed-principals imported a delegate: entry: %+v", e)
		}
	}
}

func TestDelegateEntryRoleRefusesDangerousScopes(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	bearer := map[string]string{"Authorization": "Bearer " + testAccessCode}

	const delegateSA = "slack-bot@my-proj.iam.gserviceaccount.com"

	// Custom scope set with a dangerous scope (secrets:write).
	installScopeSets(t,
		model.APIScopeSet{
			Name:        "custom-dangerous",
			Description: "has secrets:write",
			Scopes:      []string{"tasks:read", "secrets:write"},
			Version:     1,
		},
		model.APIScopeSet{
			Name:        "custom-access-write",
			Description: "has access:write",
			Scopes:      []string{"tasks:read", "access:write"},
			Version:     1,
		},
		model.APIScopeSet{
			Name:        "custom-safe",
			Description: "safe subset",
			Scopes:      []string{"tasks:read", "tasks:write"},
			Version:     1,
		},
	)

	// 1. grain/operator, custom-dangerous, and custom-access-write are refused with 422 on POST.
	for _, badRole := range []string{"grain/operator", "custom-dangerous", "custom-access-write"} {
		rec := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals",
			`{"entry":"delegate:`+delegateSA+`","role":"`+badRole+`","reason":"test"}`, bearer)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("POST delegate: with role %s = %d %s, want 422", badRole, rec.Code, rec.Body.String())
		}
	}

	// 2. Adding at grain/delegated or custom-safe succeeds; PATCH to a dangerous role is also refused with 422.
	rec := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals",
		`{"entry":"delegate:`+delegateSA+`","role":"custom-safe","reason":"test"}`, bearer)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST delegate: with custom-safe = %d %s, want 201", rec.Code, rec.Body.String())
	}
	for _, badRole := range []string{"grain/operator", "custom-dangerous", "custom-access-write"} {
		recPatch := serveFrom(srv, "127.0.0.1:4242", http.MethodPatch, "/api/access/principals/delegate:"+delegateSA,
			`{"role":"`+badRole+`"}`, bearer)
		if recPatch.Code != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH delegate: to role %s = %d %s, want 422", badRole, recPatch.Code, recPatch.Body.String())
		}
	}
}

func TestDelegateEntryNeverAdmitsADirectCaller(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)

	srv, client := testServer(t)
	ctx := context.Background()
	const delegateSA = "slack-bot@my-proj.iam.gserviceaccount.com"

	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
		Entry:  "delegate:" + delegateSA,
		Role:   "grain/delegated",
		Reason: "workspace slack bot",
	}); err != nil {
		t.Fatal(err)
	}

	// Direct IAP call by the service account (no act claim / no serviceAccount: entry) is refused (403).
	tok := signIAPToken(t, priv, tunnelKid, tunnelAudience, delegateSA, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))
	rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: tok,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("direct caller with only delegate: entry GET /api/tasks = %d %s, want 403", rec.Code, rec.Body.String())
	}
}
