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
	if _, _, err := storePreseeded.AddAllowedPrincipal(ctx, "user:seeded@example.com", "installer", time.Now()); err != nil {
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
	recAdd1 := serveFrom(srv, "127.0.0.1:4242", http.MethodPost, "/api/access/principals", `{"entry":"user:Alice@Example.com"}`, bearer)
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

	// 3. Without restarting, alice@example.com is immediately admitted AND has
	// full privileges to view, add (group:team@example.com and user:bob@example.com),
	// and remove entries on the allowlist.
	aliceHeaders := map[string]string{
		ui.IAPAssertionHeader: mint("alice@example.com"),
		"Content-Type":        "application/json",
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/access/principals", "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user GET /api/access/principals: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"group:team@example.com"}`, aliceHeaders); rec.Code != http.StatusCreated {
		t.Fatalf("listed user POST group: got %d, want 201: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/access/principals", `{"entry":"user:bob@example.com"}`, aliceHeaders); rec.Code != http.StatusCreated {
		t.Fatalf("listed user POST bob: got %d, want 201: %s", rec.Code, rec.Body)
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

	// 6. Removing yourself and the last entry is allowed; access code still works.
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/"+url.PathEscape("group:team@example.com"), "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user DELETE group: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:alice@example.com"), "", aliceHeaders); rec.Code != http.StatusOK {
		t.Fatalf("listed user self-DELETE (last entry): got %d, want 200: %s", rec.Code, rec.Body)
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
		if a.Surface == "access" && a.By.Kind == model.PrincipalHuman && a.By.ID == "alice@example.com" && strings.Contains(a.Summary, "removed user:alice@example.com") {
			sawAliceRemove = true
		}
	}
	if !sawAliceRemove {
		t.Fatalf("expected config_audit entry for alice removing user:alice@example.com; got %+v", audits)
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

	// 4. Agent WITH person-granted api:access:write AND a reason succeeds on POST and DELETE,
	// and is attributed in config_audit with its task principal and reason.
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/access/principals", `{"entry":"user:oncall@example.com"}`, "onboarding rotation"); code != http.StatusCreated {
		t.Fatalf("task 3 POST /api/access/principals with reason: got %d %s, want 201", code, resp)
	}
	if code, _ := asAgent(t, srv, "3", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:oncall@example.com"), "", ""); code != http.StatusForbidden {
		t.Fatalf("task 3 DELETE /api/access/principals without reason: got %d, want 403", code)
	}
	if code, resp := asAgent(t, srv, "3", http.MethodDelete, "/api/access/principals/"+url.PathEscape("user:oncall@example.com"), "", "offboarding rotation"); code != http.StatusOK {
		t.Fatalf("task 3 DELETE /api/access/principals with reason: got %d %s, want 200", code, resp)
	}

	audits, err := client.Store.ConfigAudit(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) < 2 || audits[0].By.Kind != model.PrincipalAgent || audits[0].By.ID != "3" || !strings.Contains(audits[0].Summary, "offboarding rotation") {
		t.Fatalf("config_audit latest entry = %+v, want agent 3 with reason", audits)
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
	if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: "group:admins@example.com"}); err != nil {
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
