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

// grain/task-172: Settings could say a capability was **Ready** -- a
// project, an agent account and a `gcp-key-minter` secret all set -- and
// be describing a credential the far end had stopped accepting months
// ago. These cover the action that closes that gap: one cheap, harmless
// call made as the standing credential, on demand, reported as a
// point-in-time answer that is deliberately not folded back into Ready.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeChecker stands in for pkg/cli/daemon.go's own adapter over the
// live providers: it answers for one capability id and records what it
// was asked, with no cloud and no credential anywhere near it.
type fakeChecker struct {
	result ui.CapabilityCheckResult
	err    error
	asked  []string
}

func (f *fakeChecker) CheckCapability(ctx context.Context, id string) (ui.CapabilityCheckResult, error) {
	f.asked = append(f.asked, id)
	return f.result, f.err
}

func testServerWithChecker(t *testing.T, checker ui.CapabilityChecker) *ui.Server {
	t.Helper()
	_, store, _ := testClient(t)
	cfg := ui.Config{
		Actor:            ui.DefaultActor("alice"),
		Capabilities:     ui.OfferedCapabilities(),
		Secrets:          secrets.New(t.TempDir()),
		CapabilityChecks: checker,
	}
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	return ui.NewServer(cfg, store)
}

func TestCheckCapabilityReportsWhatTheCredentialAnswered(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{result: ui.CapabilityCheckResult{
		Credentials: []string{"gcp-key-minter"},
		Detail:      "GCP accepted the key held in `gcp-key-minter` and listed 3 user-managed key(s) on agent@example.",
	}}
	srv := testServerWithChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/capabilities/gcp-key/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	check := decode[ui.CapabilityCheck](t, rec)
	if !check.OK {
		t.Errorf("OK = false for a credential the far end accepted: %+v", check)
	}
	if check.ID != "gcp-key" {
		t.Errorf("ID = %q, want gcp-key", check.ID)
	}
	if check.Detail != checker.result.Detail {
		t.Errorf("Detail = %q, want the provider's own sentence", check.Detail)
	}
	if len(check.Credentials) != 1 || check.Credentials[0] != "gcp-key-minter" {
		t.Errorf("Credentials = %v, want the credential that was checked", check.Credentials)
	}
	// A point-in-time answer says when it was true: read an hour later,
	// nothing about this should look current.
	if check.CheckedAt.IsZero() {
		t.Error("CheckedAt is zero -- an answer with no moment attached")
	}
	if len(checker.asked) != 1 || checker.asked[0] != "gcp-key" {
		t.Errorf("checker was asked %v, want exactly [gcp-key]", checker.asked)
	}
}

// The state this whole action exists to make visible: configured,
// checked, refused. It is an answer about this deployment, not a failure
// of the API, so it comes back 200 with ok=false and the provider's own
// sentence -- the one naming the dead secret -- rather than as an error
// a pane would render as grain being broken.
func TestCheckCapabilityReportsARefusedCredentialAsAnAnswer(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{
		result: ui.CapabilityCheckResult{Credentials: []string{"gcp-key-minter"}},
		err: errors.New("GCP will not issue a token for the minter credential held in the " +
			"`gcp-key-minter` secret: invalid_grant"),
	}
	srv := testServerWithChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/capabilities/gcp-key/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a refused credential is an answer, not a server error: %s", rec.Code, rec.Body)
	}
	check := decode[ui.CapabilityCheck](t, rec)
	if check.OK {
		t.Error("OK = true for a credential the far end refused")
	}
	if check.Detail != checker.err.Error() {
		t.Errorf("Detail = %q, want the provider's own explanation verbatim", check.Detail)
	}
	// The remedy is "replace what is in this secret", so the failure
	// path has to name which one.
	if len(check.Credentials) != 1 || check.Credentials[0] != "gcp-key-minter" {
		t.Errorf("Credentials = %v, want the checked credential named on the failure path too", check.Credentials)
	}
}

// Ready keeps meaning "configured". A refused check does not silently
// turn the badge red, because nothing about the deployment's
// configuration changed and a badge that moved on its own would be a
// worse answer than a second, dated one beside it.
func TestACheckDoesNotChangeTheReadyBadge(t *testing.T) {
	t.Parallel()
	srv := testServerWithChecker(t, &fakeChecker{err: errors.New("refused")})

	rec := do(t, srv, http.MethodPost, "/api/capabilities/self-debug/check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a capability with no standing credential: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings status = %d, want 200", rec.Code)
	}
	settings := decode[ui.Settings](t, rec)
	if !capabilityStatus(t, settings.Capabilities, "self-debug").Ready {
		t.Error("self-debug stopped being Ready because a check was run")
	}
}

func TestCheckCapabilityRejectsAnUnknownCapability(t *testing.T) {
	t.Parallel()
	srv := testServerWithChecker(t, &fakeChecker{})
	rec := do(t, srv, http.MethodPost, "/api/capabilities/not-a-capability/check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// Told apart from the unknown id above: this is a real capability that
// simply holds nothing that could go stale behind grain's back.
func TestCheckCapabilityRejectsOneWithNoStandingCredential(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{}
	srv := testServerWithChecker(t, checker)
	rec := do(t, srv, http.MethodPost, "/api/capabilities/bootstrap-playbooks/check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if len(checker.asked) != 0 {
		t.Errorf("the checker was called anyway: %v", checker.asked)
	}
}

// The nil-means-unavailable contract every other optional Config field
// gives its own pane: no round trip is offered where none could be made.
func TestCheckCapabilityIs404WithoutACheckerWired(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	rec := do(t, srv, http.MethodPost, "/api/capabilities/gcp-key/check", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// Checkable is what decides whether a pane offers the button at all, so
// it has to answer both halves: grain ships a check for this capability,
// and this deployment can run one.
func TestCapabilityStatusReportsWhichCapabilitiesCanBeChecked(t *testing.T) {
	t.Parallel()
	srv := testServerWithChecker(t, &fakeChecker{})

	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	settings := decode[ui.Settings](t, rec)
	for _, id := range []string{"gcp-key", "gemini-key", "github-sandbox"} {
		if !capabilityStatus(t, settings.Capabilities, id).Checkable {
			t.Errorf("%s: Checkable = false, but it holds a standing credential that can go stale", id)
		}
	}
	for _, id := range []string{"self-debug", "bootstrap-playbooks"} {
		if capabilityStatus(t, settings.Capabilities, id).Checkable {
			t.Errorf("%s: Checkable = true, but it holds no standing credential", id)
		}
	}
}

func TestCapabilityStatusIsUncheckableWithNoCheckerWired(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	settings := decode[ui.Settings](t, rec)
	if capabilityStatus(t, settings.Capabilities, "gcp-key").Checkable {
		t.Error("gcp-key: Checkable = true on a UI with nothing to run a check through")
	}
}

// serverWithTokenChecker is testServerWithChecker plus the picker rows a
// real deployment's named GitHub tokens add (pkg/cli/daemon.go's own
// startUIServer), which is the only thing that makes one of those ids a
// capability this deployment has: they are an operator's files, and no
// build of grain ships a catalog entry for them.
func serverWithTokenChecker(t *testing.T, checker ui.CapabilityChecker, names ...string) *ui.Server {
	t.Helper()
	_, store, _ := testClient(t)
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	return ui.NewServer(ui.Config{
		Actor:            ui.DefaultActor("alice"),
		Capabilities:     append(ui.OfferedCapabilities(), ui.GitHubTokenCapabilities(names)...),
		Secrets:          secrets.New(t.TempDir()),
		CapabilityChecks: checker,
	}, store)
}

// grain/task-189: a named GitHub token's row reports Ready by
// construction -- the file exists -- and a token revoked, expired or
// rotated at GitHub's end changes nothing about that file. The check is
// the one thing that can tell the difference, so the endpoint has to
// reach the provider for one of these ids rather than refusing it as a
// capability with nothing to test.
func TestCheckCapabilityReachesANamedGitHubToken(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{result: ui.CapabilityCheckResult{
		Credentials: []string{"secrets/github/release-bot.token"},
		Detail:      "github.com accepted the \"release-bot\" token (4999 of 5000 core API requests left this hour).",
	}}
	srv := serverWithTokenChecker(t, checker, "release-bot")

	rec := do(t, srv, http.MethodPost, "/api/capabilities/github-credential:release-bot/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	check := decode[ui.CapabilityCheck](t, rec)
	if !check.OK || check.Detail != checker.result.Detail {
		t.Errorf("check = %+v, want the provider's own answer", check)
	}
	// The remedy for a refused token is replacing a file, so the answer
	// names it the same way every other capability's names its secret.
	if len(check.Credentials) != 1 || check.Credentials[0] != "secrets/github/release-bot.token" {
		t.Errorf("Credentials = %v, want the file the token lives in", check.Credentials)
	}
	if len(checker.asked) != 1 || checker.asked[0] != "github-credential:release-bot" {
		t.Errorf("checker was asked %v, want exactly the token's own id", checker.asked)
	}
}

// The other half of that: which tokens exist is this deployment's own
// files, so a token-shaped id this deployment does not offer is an
// unknown capability -- a 400 about grain, not a check that goes out and
// fails.
func TestCheckCapabilityRejectsAnUnofferedGitHubToken(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{}
	srv := serverWithTokenChecker(t, checker, "release-bot")

	rec := do(t, srv, http.MethodPost, "/api/capabilities/github-credential:no-such-token/check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if len(checker.asked) != 0 {
		t.Errorf("the checker was called for a token this deployment has no file for: %v", checker.asked)
	}
}

// And the row itself: Ready stays true by construction (nothing about
// the file is missing), with the answer it cannot give reported beside
// it as a check somebody can run.
func TestCapabilityStatusReportsANamedGitHubTokenAsCheckable(t *testing.T) {
	t.Parallel()
	srv := serverWithTokenChecker(t, &fakeChecker{}, "release-bot")

	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	settings := decode[ui.Settings](t, rec)
	status := capabilityStatus(t, settings.Capabilities, "github-credential:release-bot")
	if !status.Ready {
		t.Errorf("Ready = false for a token whose file exists: %+v", status)
	}
	if !status.Checkable {
		t.Error("Checkable = false, so the pane offers no way to test a token it reports Ready")
	}
}

// A check is a call somebody else's API answers, which is exactly what a
// browser, a proxy or a link prefetch must not be free to repeat on its
// own -- so only POST reaches it. The GET is not asserted on by status:
// an unrouted path falls through to the SPA handler, which answers
// index.html on a build with a frontend in it and 404 on one without.
// What matters is that no check ran.
func TestCheckCapabilityIsNotAGet(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{}
	srv := testServerWithChecker(t, checker)
	do(t, srv, http.MethodGet, "/api/capabilities/gcp-key/check", "")
	if len(checker.asked) != 0 {
		t.Fatalf("a GET ran a credential check: %v", checker.asked)
	}
}
