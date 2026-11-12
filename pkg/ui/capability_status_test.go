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

// bwsalmon/agents#611: Settings' new "Capabilities" tab needs to answer
// "is this capability actually usable right now, and if not, what's
// missing" for every capability grain ships a provider for -- not just
// the ones OfferedCapabilities offers a human to attach to a task. These
// tests cover GetSettings' new Capabilities field end to end: ready
// without any secrets store colocated (self-debug/self-repair/
// bootstrap-playbooks, which need none), and gated on secrets once one is
// (github-sandbox).

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func capabilityStatus(t *testing.T, statuses []ui.CapabilityStatus, id string) ui.CapabilityStatus {
	t.Helper()
	for _, s := range statuses {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no capability status for %q among %+v", id, statuses)
	return ui.CapabilityStatus{}
}

// The per-task picker (GET /api/config) and Settings' Capabilities tab
// used to be entirely independent listings, so a deployment could report
// a capability as "Not ready -- Needs: ..." on one pane while the
// other offered it as an ordinary tickable row. Ticking it filed a
// perfectly valid grant whose only symptom was the task failing to
// dispatch later on. This covers the join between them.
func TestConfigPickerReportsACapabilityThisDeploymentCannotHonour(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	// github-sandbox is retired from OfferedCapabilities() onto
	// github#sandbox-repo (schema migration 124), while capabilityStatuses
	// keeps its readiness row; attach a legacy row here to exercise the
	// join on capabilitiesWithReadiness as well as ciCapabilitiesWithReadiness.
	client.Config.Capabilities = append(ui.OfferedCapabilities(), ui.Capability{
		ID:   "github-sandbox",
		Name: "GitHub sandbox",
	})

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cfg := decode[struct {
		Capabilities   []ui.Capability `json:"capabilities"`
		CICapabilities []ui.Capability `json:"ciCapabilities"`
	}](t, rec)

	sandbox := pickerRow(t, cfg.Capabilities, "github-sandbox")
	if sandbox.Ready == nil {
		t.Fatal("github-sandbox: Ready is nil -- the picker was told nothing about readiness")
	}
	if *sandbox.Ready {
		t.Error("github-sandbox: Ready = true with no App secrets stored on this deployment")
	}
	if want := []string{githubsandbox.DefaultAppIDCredential, githubsandbox.DefaultPrivateKeyCredential}; !reflect.DeepEqual(sandbox.Needs, want) {
		t.Errorf("github-sandbox: Needs = %v, want %v", sandbox.Needs, want)
	}

	ciSandbox := pickerRow(t, cfg.CICapabilities, model.GitHubSandboxRepoGrant)
	if ciSandbox.Ready == nil || *ciSandbox.Ready {
		t.Errorf("%s: Ready = %v, want false with no App secrets stored", model.GitHubSandboxRepoGrant, ciSandbox.Ready)
	}
	if want := []string{githubsandbox.DefaultAppIDCredential, githubsandbox.DefaultPrivateKeyCredential}; !reflect.DeepEqual(ciSandbox.Needs, want) {
		t.Errorf("%s: Needs = %v, want %v", model.GitHubSandboxRepoGrant, ciSandbox.Needs, want)
	}

	// Once its secrets are configured, the same picker row reports ready
	// (explicitly true, not merely nil) with nothing missing -- the picker
	// has to tell configured and unconfigured apart to warn on only one.
	if err := client.Config.Secrets.Set("github-app", "app-id", []byte("123")); err != nil {
		t.Fatal(err)
	}
	if err := client.Config.Secrets.Set("github-app", "private-key", []byte("-----BEGIN KEY-----")); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cfg = decode[struct {
		Capabilities   []ui.Capability `json:"capabilities"`
		CICapabilities []ui.Capability `json:"ciCapabilities"`
	}](t, rec)
	readyCISandbox := pickerRow(t, cfg.CICapabilities, model.GitHubSandboxRepoGrant)
	if readyCISandbox.Ready == nil || !*readyCISandbox.Ready || len(readyCISandbox.Needs) != 0 {
		t.Errorf("%s after secrets set: Ready = %v, Needs = %v, want ready with none", model.GitHubSandboxRepoGrant, readyCISandbox.Ready, readyCISandbox.Needs)
	}
	readySandbox := pickerRow(t, cfg.Capabilities, "github-sandbox")
	if readySandbox.Ready == nil || !*readySandbox.Ready {
		t.Errorf("github-sandbox after secrets set: Ready = %v, want true", readySandbox.Ready)
	}
	if len(readySandbox.Needs) != 0 {
		t.Errorf("github-sandbox after secrets set: Needs = %v, want none", readySandbox.Needs)
	}
}

func pickerRow(t *testing.T, rows []ui.Capability, id string) ui.Capability {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no picker row for %q among %+v", id, rows)
	return ui.Capability{}
}

func TestCapabilitiesListsEveryKnownCapabilityOnAFreshStore(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got.Capabilities {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	// Each named as the grant its bare provider name was retired onto,
	// never the retired name itself.
	want := []string{
		model.GitHubSandboxRepoGrant,
		model.CanonicalLegacyGrant("image-signing"),
		model.CanonicalLegacyGrant("package-publish"),
		model.CanonicalLegacyGrant("registry-push"),
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("capability ids = %v, want %v", ids, want)
	}
}

func TestCapabilitiesSelfDebugAndSelfRepairAreAlwaysReady(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"registry-push", "package-publish"} {
		status := capabilityStatus(t, got.Capabilities, model.CanonicalLegacyGrant(id))
		if !status.Ready {
			t.Fatalf("%s: Ready = false, want true (status: %+v)", id, status)
		}
		if len(status.MissingConfig) != 0 || len(status.MissingSecrets) != 0 {
			t.Fatalf("%s: expected nothing missing, got %+v", id, status)
		}
	}
}

// A UI not colocated with a daemon has no second reading to compare
// against, and says nothing rather than inventing a gap -- the same
// nil-means-unavailable contract Config.Secrets already follows.
func TestCapabilitiesReportNothingPendingWithNoRunningConfig(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	stored := model.DefaultConfig()
	stored.GCPProject = "acme-project"
	if err := client.Store.PutConfig(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	got, err := client.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range got.Capabilities {
		if len(status.PendingConfig) != 0 {
			t.Errorf("%s: PendingConfig = %v with no RunningConfig to compare against",
				status.ID, status.PendingConfig)
		}
	}
}

func TestCapabilitiesGithubSandboxFlagsMissingSecretsWhenColocatedWithAStore(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Secrets = secrets.New(t.TempDir())

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := capabilityStatus(t, got.Capabilities, model.GitHubSandboxRepoGrant)
	if status.Ready {
		t.Fatalf("github-sandbox: Ready = true with no secrets set, want false")
	}
	want := []string{githubsandbox.DefaultAppIDCredential, githubsandbox.DefaultPrivateKeyCredential}
	sort.Strings(want)
	got2 := append([]string(nil), status.MissingSecrets...)
	sort.Strings(got2)
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("github-sandbox: MissingSecrets = %v, want %v", status.MissingSecrets, want)
	}

	if err := c.Config.Secrets.Set("github-app", "app-id", []byte("123")); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Secrets.Set("github-app", "private-key", []byte("-----BEGIN KEY-----")); err != nil {
		t.Fatal(err)
	}
	got, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status = capabilityStatus(t, got.Capabilities, model.GitHubSandboxRepoGrant)
	if !status.Ready {
		t.Fatalf("github-sandbox: Ready = false once both secrets are set (status: %+v)", status)
	}
}

func TestCapabilitiesReportNothingMissingWithNoSecretsStoreColocated(t *testing.T) {
	t.Parallel()
	// The default testClient -- Config.Secrets left nil, the same "not
	// colocated" case TestSettingsSkipsTheCredentialCheckWithNoCredentialsConfigured
	// covers for targetRepos.
	c, _, ctx := testClient(t)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := capabilityStatus(t, got.Capabilities, model.GitHubSandboxRepoGrant)
	if len(status.MissingSecrets) != 0 {
		t.Fatalf("github-sandbox: MissingSecrets = %v, want none reported with no store to check", status.MissingSecrets)
	}
	// Nothing to offer either: with no store colocated there is nowhere
	// to write a value, so the pane gets no field to write it in rather
	// than one whose every use would 404 (grain/task-110).
	if len(status.Secrets) != 0 {
		t.Fatalf("github-sandbox: Secrets = %+v, want none offered with no store to write to", status.Secrets)
	}
}

// grain/task-110: each capability's own secrets are set from the pane
// that reports them missing, so every Requires entry is reported --
// whether or not it is set -- with the secret and key a write would
// address it by.
func TestCapabilitiesReportWhereEachRequiredSecretIsWritten(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Secrets = secrets.New(t.TempDir())
	if err := c.Config.Secrets.Set("github-app", "app-id", []byte("123")); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// The "<secret>/<key>" form is split exactly as written: one set,
	// one not, both offered.
	status := capabilityStatus(t, got.Capabilities, model.GitHubSandboxRepoGrant)
	want := []ui.CapabilitySecret{
		{Name: githubsandbox.DefaultAppIDCredential, Secret: "github-app", Key: "app-id", Set: true},
		{Name: githubsandbox.DefaultPrivateKeyCredential, Secret: "github-app", Key: "private-key", Set: false},
	}
	if !reflect.DeepEqual(status.Secrets, want) {
		t.Fatalf("github-sandbox: Secrets = %+v, want %+v", status.Secrets, want)
	}

}

// Grantable is the half of "is this capability usable" that Ready says
// nothing about: whether the picker listing this Client was built with
// (Config.Capabilities) offers the capability at all, since grantsFor
// and SetCapability both reject an id it has no row for. A deployment
// can have a capability fully configured -- Ready, nothing missing --
// and still have no way to attach it to a task, which is what a
// provider registered by pkg/daemon's CapabilityProviders but
// absent from OfferedCapabilities looks like from here.
//
// The picker listing is set explicitly rather than taken from
// OfferedCapabilities so this covers the reporting, not whatever that
// list happens to hold today.
func TestCapabilityStatusReportsWhetherATaskCanBeGrantedIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Capabilities = []ui.Capability{
		{ID: "github-sandbox", Name: "GitHub sandbox"},
	}

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{
		"github-sandbox": true,
		"registry-push":  false,
	} {
		if status := capabilityStatus(t, got.Capabilities, model.CanonicalLegacyGrant(id)); status.Grantable != want {
			t.Errorf("%s: Grantable = %t, want %t (status: %+v)", id, status.Grantable, want, status)
		}
	}
}

// A capability nothing can grant is still reported ready when this
// deployment has everything it needs -- the two are independent, and
// collapsing them would hide whichever gap the other one covers for.
func TestCapabilityStatusKeepsGrantableAndReadyIndependent(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Capabilities = nil

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := capabilityStatus(t, got.Capabilities, model.GitHubSandboxRepoGrant)
	if !status.Ready {
		t.Fatalf("github-sandbox: Ready = false, want true -- no secret store is configured (status: %+v)", status)
	}
	if status.Grantable {
		t.Fatalf("github-sandbox: Grantable = true with an empty picker listing (status: %+v)", status)
	}
}
