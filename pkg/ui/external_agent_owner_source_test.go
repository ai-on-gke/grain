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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// filedOnBehalfOf files a task with the external agent token and returns
// the attribution its origin was recorded with.
func filedOnBehalfOf(t *testing.T, srv *ui.Server, client *ui.Client, token, title string) model.Attribution {
	t.Helper()
	task := filedTask(t, fileAsExternal(t, srv, token, "/api/tasks",
		`{"title":"`+title+`","repo":"acme/widgets"}`, nil))
	stored, err := client.Store.GetTask(context.Background(), task.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading task %s: %v", task.ID, err)
	}
	return stored.Origin.Attribution
}

// An external agent token's on-behalf-of is the verified person who
// minted it (operator-attribution spec §2.4), and the shared access code
// is never a person (§6.3): on a deployment whose own actor is a person,
// a token the access code mints without naming an owner acts for nobody,
// and an owner the access code names is recorded as named by the access
// code rather than as a verified on-behalf-of (external-cli spec #XC-14).
func TestExternalAgentTokenOwnerDefaultsOnlyToAVerifiedMinter(t *testing.T) {
	client, _, _ := testClient(t)
	client.Config.Actor = ui.DefaultActor("operator@example.com")
	srv := ui.NewServerWithClient(client)
	enableExternalAgentAPI(t, client.Store)
	ctx := context.Background()
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)
	accessCode := serverAccessClient(srv, daemon.URL)

	// An IAP-verified alice mints one: its calls are the agent's, on
	// behalf of alice, verified.
	alice := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	aliceTok, err := client.CreateExternalAgentToken(alice, ui.CreateExternalAgentTokenRequest{Name: "alice-laptop", ScopeSet: "grain/external-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if aliceTok.Owner != "alice@example.com" {
		t.Fatalf("alice's token owner = %q, want alice@example.com", aliceTok.Owner)
	}
	got := filedOnBehalfOf(t, srv, client, aliceTok.Token, "from alice")
	if want := (model.Principal{Kind: model.PrincipalAgent, ID: "external:alice-laptop", Source: model.SourceExternalToken}); got.Actor != want {
		t.Errorf("alice's token acted as %+v, want %+v", got.Actor, want)
	}
	if b := got.OnBehalfOf; b == nil || b.Kind != model.PrincipalHuman || b.ID != "alice@example.com" || b.Source != model.SourceToken || !b.Verified() {
		t.Errorf("alice's token acted on behalf of %+v, want verified {human, alice@example.com, token}", b)
	}

	// The access code mints one naming nobody: the deployment's own
	// actor is a person, but not one anybody verified, so the token acts
	// for nobody.
	unowned, err := accessCode.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{Name: "shared-bot", ScopeSet: "grain/external-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if unowned.Owner != "" {
		t.Fatalf("access-code token owner = %q, want none (not the deployment actor operator@example.com)", unowned.Owner)
	}
	if b := filedOnBehalfOf(t, srv, client, unowned.Token, "from the shared bot").OnBehalfOf; b != nil {
		t.Errorf("access-code token acted on behalf of %+v, want nobody", b)
	}

	// Nor does an in-process mint (the CLI on the daemon's host).
	if inProc, err := client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{Name: "local-bot", ScopeSet: "grain/external-read"}); err != nil || inProc.Owner != "" {
		t.Errorf("in-process token = %+v, %v; want one with no owner", inProc.ExternalAgentTokenView, err)
	}

	// The access code may still name an owner (the operator issuing one
	// for somebody), but its word is recorded as the access code's: the
	// on-behalf-of is bob, not a verified bob.
	forBob, err := accessCode.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{Name: "bob-bot", ScopeSet: "grain/external-dev", Owner: "bob@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if forBob.Owner != "bob@example.com" {
		t.Fatalf("named owner = %q, want bob@example.com", forBob.Owner)
	}
	row, err := client.Store.GetExternalAgentToken(ctx, forBob.ID)
	if err != nil || row == nil || row.OwnerSource != model.SourceAccessCode {
		t.Fatalf("bob's token row = %+v, %v; want owner_source access-code", row, err)
	}
	if b := filedOnBehalfOf(t, srv, client, forBob.Token, "for bob").OnBehalfOf; b == nil || b.ID != "bob@example.com" || b.Source != model.SourceAccessCode || b.Verified() {
		t.Errorf("bob's token acted on behalf of %+v, want an unverified {human, bob@example.com, access-code}", b)
	}

	// `grain login --issue-token` under the access code still mints a
	// workstation token, owned by nobody -- and may not name anybody.
	self, err := accessCode.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{Name: "cli-shared", ScopeSet: "grain/external-dev", SelfService: true})
	if err != nil {
		t.Fatalf("self-service mint under the access code: %v", err)
	}
	if self.Owner != "" {
		t.Errorf("self-service access-code token owner = %q, want none", self.Owner)
	}
	if _, err := accessCode.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "cli-claims-bob", ScopeSet: "grain/external-dev", SelfService: true, Owner: "bob@example.com",
	}); err == nil || !strings.Contains(err.Error(), "acts for nobody") {
		t.Errorf("self-service access-code mint naming bob = %v, want refused", err)
	}
}

// A token row from before owner_source was recorded keeps the verified
// on-behalf-of it always had.
func TestExternalAgentTokenOwnerPrincipalOfAnOldRowIsUnchanged(t *testing.T) {
	old := model.ExternalAgentToken{Name: "old", Owner: "carol@example.com"}
	if b := old.OwnerPrincipal(); b == nil || b.ID != "carol@example.com" || b.Source != model.SourceToken || !b.Verified() {
		t.Errorf("old row's owner = %+v, want verified {human, carol@example.com, token}", b)
	}
	verified := model.ExternalAgentToken{Name: "v", Owner: "dan@example.com", OwnerSource: model.SourceIAP}
	if b := verified.OwnerPrincipal(); b == nil || b.Source != model.SourceToken || !b.Verified() {
		t.Errorf("IAP-minted owner = %+v, want verified {human, dan@example.com, token}", b)
	}
	if b := (model.ExternalAgentToken{Name: "none"}).OwnerPrincipal(); b != nil {
		t.Errorf("unowned token's owner = %+v, want nil", b)
	}
}
