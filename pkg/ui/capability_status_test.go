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
// bootstrap-playbooks, which need none), gated on GCP config
// (gcp-key/gemini-key), and gated on secrets once one is (github-sandbox).

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
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
// gemini-key as "Not ready -- Needs: GCP project" on one pane while the
// other offered it as an ordinary tickable row. Ticking it filed a
// perfectly valid grant whose only symptom was the task failing to
// dispatch later on. These two cover the join between them.
func TestConfigPickerReportsACapabilityThisDeploymentCannotHonour(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cfg := decode[struct {
		Capabilities []ui.Capability `json:"capabilities"`
	}](t, rec)

	gemini := pickerRow(t, cfg.Capabilities, "gemini-key")
	if gemini.Ready == nil {
		t.Fatal("gemini-key: Ready is nil -- the picker was told nothing about readiness")
	}
	if *gemini.Ready {
		t.Error("gemini-key: Ready = true with no GCP project set on this deployment")
	}
	if !reflect.DeepEqual(gemini.Needs, []string{"GCP project"}) {
		t.Errorf("gemini-key: Needs = %v, want [GCP project]", gemini.Needs)
	}

	// A capability that needs no deployment configuration at all is
	// reported ready, not merely "not not-ready" -- the picker has to be
	// able to tell the two apart to warn about only one of them.
	selfDebug := pickerRow(t, cfg.Capabilities, "self-debug")
	if selfDebug.Ready == nil || !*selfDebug.Ready {
		t.Errorf("self-debug: Ready = %v, want true", selfDebug.Ready)
	}
	if len(selfDebug.Needs) != 0 {
		t.Errorf("self-debug: Needs = %v, want none", selfDebug.Needs)
	}
}

func TestConfigPickerReportsGeminiKeyReadyOnceItsProjectAndSecretAreSet(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	if err := client.Config.Secrets.Set(gcpkey.DefaultMinterCredential, "value", []byte("shh")); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	cfg.GCPProject = "acme-project"
	if err := client.Store.PutConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[struct {
		Capabilities []ui.Capability `json:"capabilities"`
	}](t, rec)

	gemini := pickerRow(t, got.Capabilities, "gemini-key")
	if gemini.Ready == nil || !*gemini.Ready {
		t.Fatalf("gemini-key: Ready = %v, want true (row: %+v)", gemini.Ready, gemini)
	}
	if len(gemini.Needs) != 0 {
		t.Errorf("gemini-key: Needs = %v, want none once the project and its minter secret are set", gemini.Needs)
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
	want := []string{"bootstrap-playbooks", "claude-proxy", "console", "db-read", "db-write", "deployment-health", "durable", "gcp-key", "gemini-key", "gemini-proxy", "git-push-any-ref", "github-sandbox", "grain-log", "image-signing", "memory", "openai-proxy", "package-publish", "pipeline-control", "priority-dispatch", "read-issues", "registry-push", "release-notes", "self-debug", "self-repair", "self-upgrade", "test-quarantine"}
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
	for _, id := range []string{"self-debug", "self-repair", "bootstrap-playbooks"} {
		status := capabilityStatus(t, got.Capabilities, id)
		if !status.Ready {
			t.Fatalf("%s: Ready = false, want true (status: %+v)", id, status)
		}
		if len(status.MissingConfig) != 0 || len(status.MissingSecrets) != 0 {
			t.Fatalf("%s: expected nothing missing, got %+v", id, status)
		}
	}
}

func TestCapabilitiesGcpKeyAndGeminiKeyAreNotReadyWithoutAGcpProject(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	gcpKey := capabilityStatus(t, got.Capabilities, "gcp-key")
	if gcpKey.Ready {
		t.Fatalf("gcp-key: Ready = true with no GCP project configured, want false")
	}
	want := []string{"GCP project", "GCP service account email"}
	if !reflect.DeepEqual(gcpKey.MissingConfig, want) {
		t.Fatalf("gcp-key: MissingConfig = %v, want %v", gcpKey.MissingConfig, want)
	}

	geminiKey := capabilityStatus(t, got.Capabilities, "gemini-key")
	if geminiKey.Ready {
		t.Fatalf("gemini-key: Ready = true with no GCP project configured, want false")
	}
	if !reflect.DeepEqual(geminiKey.MissingConfig, []string{"GCP project"}) {
		t.Fatalf("gemini-key: MissingConfig = %v, want [GCP project]", geminiKey.MissingConfig)
	}
}

func TestCapabilitiesGeminiKeyIsReadyOnceAGcpProjectAndItsMinterSecretAreSet(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Secrets = secrets.New(t.TempDir())
	if err := c.Config.Secrets.Set(gcpkey.DefaultMinterCredential, "value", []byte("shh")); err != nil {
		t.Fatal(err)
	}

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	project := "acme-project"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GCPProject: &project}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	geminiKey := capabilityStatus(t, got.Capabilities, "gemini-key")
	if !geminiKey.Ready {
		t.Fatalf("gemini-key: Ready = false, want true (status: %+v)", geminiKey)
	}

	// gcp-key still needs the service account email, and its own minter
	// secret was never set under a different name -- unaffected by
	// gemini-key becoming ready.
	gcpKey := capabilityStatus(t, got.Capabilities, "gcp-key")
	if gcpKey.Ready {
		t.Fatalf("gcp-key: Ready = true, want false (status: %+v)", gcpKey)
	}
}

// grain/task-2xx: a project saved that the daemon behind this pane has
// not adopted. Both halves of the pane used to read a different
// configuration for this one question -- the badge the stored row, the
// "Test credential" button beside it the registry the running process
// actually holds -- so a deployment whose reconcile loop was down showed
// a green **Ready** on a capability whose own check answered that no
// provider is registered for it, two lines apart on one card. Ready is
// the half that was overclaiming: a task granted this now would fail to
// resolve exactly as the check said.
func TestCapabilitiesAreNotReadyWhileAStoredSettingIsUnapplied(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	if err := client.Config.Secrets.Set(gcpkey.DefaultMinterCredential, "value", []byte("shh")); err != nil {
		t.Fatal(err)
	}
	// A daemon still running what it started with: no project, whatever
	// the row now says.
	client.Config.RunningConfig = func() model.Config { return model.Config{} }
	stored := model.DefaultConfig()
	stored.GCPProject = "acme-project"
	if err := client.Store.PutConfig(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	got, err := client.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	geminiKey := capabilityStatus(t, got.Capabilities, "gemini-key")
	if geminiKey.Ready {
		t.Errorf("gemini-key: Ready = true on a project this deployment is not running with (status: %+v)", geminiKey)
	}
	if len(geminiKey.MissingConfig) != 0 {
		t.Errorf("gemini-key: MissingConfig = %v, want nothing -- the project is set, and "+
			"telling an operator to set it again is the wrong remedy", geminiKey.MissingConfig)
	}
	if !reflect.DeepEqual(geminiKey.PendingConfig, []string{"GCP project"}) {
		t.Errorf("gemini-key: PendingConfig = %v, want [GCP project]", geminiKey.PendingConfig)
	}

	// The picker two panes away is told the same thing, in the same
	// words, rather than being left with a bare "not ready".
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[struct {
		Capabilities []ui.Capability `json:"capabilities"`
	}](t, rec)
	row := pickerRow(t, cfg.Capabilities, "gemini-key")
	if row.Ready == nil || *row.Ready {
		t.Fatalf("gemini-key: picker Ready = %v, want false", row.Ready)
	}
	if len(row.Needs) != 1 || !strings.Contains(row.Needs[0], "GCP project") ||
		!strings.Contains(row.Needs[0], "not yet in effect") {
		t.Errorf("gemini-key: picker Needs = %v, want the project named as saved but unapplied", row.Needs)
	}
}

// Once the daemon is running the same configuration the row holds --
// the ordinary case, one reconcile tick after any change -- there is
// nothing pending and the badge means what it always did.
func TestCapabilitiesHaveNothingPendingOnceTheRunningConfigMatches(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	if err := client.Config.Secrets.Set(gcpkey.DefaultMinterCredential, "value", []byte("shh")); err != nil {
		t.Fatal(err)
	}
	stored := model.DefaultConfig()
	stored.GCPProject = "acme-project"
	if err := client.Store.PutConfig(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	client.Config.RunningConfig = func() model.Config { return stored }

	got, err := client.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	geminiKey := capabilityStatus(t, got.Capabilities, "gemini-key")
	if !geminiKey.Ready || len(geminiKey.PendingConfig) != 0 {
		t.Errorf("gemini-key: Ready = %v, PendingConfig = %v, want ready with nothing pending",
			geminiKey.Ready, geminiKey.PendingConfig)
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
	status := capabilityStatus(t, got.Capabilities, "github-sandbox")
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
	status = capabilityStatus(t, got.Capabilities, "github-sandbox")
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
	status := capabilityStatus(t, got.Capabilities, "github-sandbox")
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
	status := capabilityStatus(t, got.Capabilities, "github-sandbox")
	want := []ui.CapabilitySecret{
		{Name: githubsandbox.DefaultAppIDCredential, Secret: "github-app", Key: "app-id", Set: true},
		{Name: githubsandbox.DefaultPrivateKeyCredential, Secret: "github-app", Key: "private-key", Set: false},
	}
	if !reflect.DeepEqual(status.Secrets, want) {
		t.Fatalf("github-sandbox: Secrets = %+v, want %+v", status.Secrets, want)
	}

	// The bare "<secret>" form, with nothing stored under it yet: it
	// gets the default key name, which is one Resolve's sole-key form
	// finds.
	minter := capabilityStatus(t, got.Capabilities, "gcp-key").Secrets
	wantMinter := []ui.CapabilitySecret{
		{Name: gcpkey.DefaultMinterCredential, Secret: gcpkey.DefaultMinterCredential, Key: "value", Set: false},
	}
	if !reflect.DeepEqual(minter, wantMinter) {
		t.Fatalf("gcp-key: Secrets = %+v, want %+v", minter, wantMinter)
	}
}

// A bare "<secret>" credential someone seeded under a key of their own
// (scripts/setup.sh writes the minter key as gcp-key-minter/key.json) is
// replaced in place rather than joined by a second key -- two keys is
// exactly the state secrets.Store.Resolve refuses to read the bare name
// out of.
func TestCapabilitiesWriteABareSecretBackToTheKeyItAlreadyHas(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Secrets = secrets.New(t.TempDir())
	if err := c.Config.Secrets.Set(gcpkey.DefaultMinterCredential, "key.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []ui.CapabilitySecret{
		{Name: gcpkey.DefaultMinterCredential, Secret: gcpkey.DefaultMinterCredential, Key: "key.json", Set: true},
	}
	if status := capabilityStatus(t, got.Capabilities, "gcp-key"); !reflect.DeepEqual(status.Secrets, want) {
		t.Fatalf("gcp-key: Secrets = %+v, want %+v", status.Secrets, want)
	}
}

// Grantable is the half of "is this capability usable" that Ready says
// nothing about: whether the picker listing this Client was built with
// (Config.Capabilities) offers the capability at all, since grantsFor
// and SetCapability both reject an id it has no row for. A deployment
// can have a capability fully configured -- Ready, nothing missing --
// and still have no way to attach it to a task, which is exactly what a
// gcp-key registered by pkg/cli/daemon.go's capabilityProviders but
// absent from OfferedCapabilities looks like from here.
//
// The picker listing is set explicitly rather than taken from
// OfferedCapabilities so this covers the reporting, not whatever that
// list happens to hold today.
func TestCapabilityStatusReportsWhetherATaskCanBeGrantedIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Capabilities = []ui.Capability{
		{ID: "gemini-key", Name: "Gemini key"},
		{ID: "self-debug", Name: "Self debug"},
	}

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{
		"gemini-key":     true,
		"self-debug":     true,
		"gcp-key":        false,
		"github-sandbox": false,
		"self-repair":    false,
	} {
		if status := capabilityStatus(t, got.Capabilities, id); status.Grantable != want {
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
	status := capabilityStatus(t, got.Capabilities, "self-debug")
	if !status.Ready {
		t.Fatalf("self-debug: Ready = false, want true -- it needs no configuration (status: %+v)", status)
	}
	if status.Grantable {
		t.Fatalf("self-debug: Grantable = true with an empty picker listing (status: %+v)", status)
	}
}
