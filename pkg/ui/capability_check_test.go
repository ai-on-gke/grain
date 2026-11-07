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
	if !capabilityStatus(t, settings.Capabilities, "durable").Ready {
		t.Error("durable stopped being Ready because a check was run")
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
	rec := do(t, srv, http.MethodPost, "/api/capabilities/self-debug/check", "")
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
	for _, id := range []string{"image-signing"} {
		if !capabilityStatus(t, settings.Capabilities, id).Checkable {
			t.Errorf("%s: Checkable = false, but it holds a standing credential that can go stale", id)
		}
	}
	for _, id := range []string{"durable"} {
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
	if capabilityStatus(t, settings.Capabilities, "image-signing").Checkable {
		t.Error("image-signing: Checkable = true on a UI with nothing to run a check through")
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
