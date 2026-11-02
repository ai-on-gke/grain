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

// bwsalmon/agents#427: a targetRepos entry with no covering credential in
// the ladder the git proxy resolves pushes against is a deployment
// misconfiguration that otherwise only surfaces later, confusingly, as a
// 500 "no credential configured" from the proxy itself (core_test.go's
// own TestHandleNoConfiguredCredentialIs500NotForwarded). These tests
// cover the other half: Settings flagging the same gap directly, at the
// moment targetRepos is widened, when this UI is colocated with the
// proxy's own credential ladder (Config.Credentials, nil otherwise).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// loadCredentialSet writes a credentials.json covering exactly the
// owner/repo patterns given and returns the CredentialSet LoadCredentialSet
// reads back -- the same shape pkg/cli/daemon.go's own startUIServer
// loads at startup, minus the token files, which Select doesn't need to
// answer whether a pattern covers a repo.
func loadCredentialSet(t *testing.T, patterns map[string]string) *gitproxy.CredentialSet {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(patterns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func testClientWithCredentials(t *testing.T, credentials *gitproxy.CredentialSet) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	c, store, ctx := testClient(t)
	c.Config.Credentials = credentials
	return c, store, ctx
}

func TestConfigReportsCriticalSetupReadiness(t *testing.T) {
	t.Parallel()
	credentials := loadCredentialSet(t, nil)
	c, store, ctx := testClientWithCredentials(t, credentials)
	srv := ui.NewServerWithClient(c)

	got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["modelsConfigured"] != false {
		t.Fatalf("modelsConfigured = %v on a fresh deployment, want false", got["modelsConfigured"])
	}
	if got["gitCredentialsConfigured"] != false {
		t.Fatalf("gitCredentialsConfigured = %v on a fresh deployment, want false", got["gitCredentialsConfigured"])
	}

	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: []model.AgentBackend{{
			ID: "primary", Framework: model.AgentFrameworkClaude,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetToken("default", "github-token"); err != nil {
		t.Fatal(err)
	}

	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["modelsConfigured"] != true {
		t.Fatalf("modelsConfigured = %v with a backend, want true", got["modelsConfigured"])
	}
	if got["gitCredentialsConfigured"] != true {
		t.Fatalf("gitCredentialsConfigured = %v with a token, want true", got["gitCredentialsConfigured"])
	}
}

// TestUpdateSettingsRoundTripsShowClosedByDefault is bwsalmon/agents#537's
// global default: unset it reads back false (hide closed tasks by
// default), and setting it sticks through a GetSettings read the same
// way NewestFirst already does (client_test.go's own
// TestNewestFirstSettingMovesNewTasksToTheFrontOfTheQueue).
func TestUpdateSettingsRoundTripsShowClosedByDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.ShowClosedByDefault {
		t.Fatalf("ShowClosedByDefault = true with nothing set, want false")
	}

	show := true
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{ShowClosedByDefault: &show}); err != nil {
		t.Fatal(err)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !read.ShowClosedByDefault {
		t.Fatalf("ShowClosedByDefault = false after UpdateSettings, want true")
	}
}

// TestUpdateSettingsRoundTripsEnvironmentName is grain/task-69's
// deployment label: unset it reads back empty (an unnamed deployment,
// which the UI shows nothing for), setting it sticks through a
// GetSettings read, and clearing it back to "" is a real value rather
// than a no-op -- an operator who names a deployment by mistake has to
// be able to unname it.
func TestUpdateSettingsRoundTripsEnvironmentName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.EnvironmentName != "" {
		t.Fatalf("EnvironmentName = %q with nothing set, want empty", read.EnvironmentName)
	}

	// Surrounding whitespace is trimmed on the way in, so a stray space
	// is not the difference between named and unnamed.
	name := "  staging  "
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{EnvironmentName: &name}); err != nil {
		t.Fatal(err)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.EnvironmentName != "staging" {
		t.Fatalf("EnvironmentName = %q after UpdateSettings, want %q", read.EnvironmentName, "staging")
	}

	cleared := ""
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{EnvironmentName: &cleared}); err != nil {
		t.Fatal(err)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.EnvironmentName != "" {
		t.Fatalf("EnvironmentName = %q after clearing, want empty", read.EnvironmentName)
	}
}

// TestUpdateSettingsRejectsUnrenderableEnvironmentName pins the two
// bounds on what is otherwise free text: it is rendered as a badge in
// the sidebar and appended to the tab title, so a pasted paragraph or an
// embedded newline would take that chrome over rather than label it.
func TestUpdateSettingsRejectsUnrenderableEnvironmentName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"too long", strings.Repeat("s", 33)},
		{"newline", "staging\nproduction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.value
			if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{EnvironmentName: &value}); err == nil {
				t.Fatalf("UpdateSettings(%q) = nil error, want a validation error", tc.value)
			}
			read, err := c.GetSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if read.EnvironmentName != "" {
				t.Fatalf("EnvironmentName = %q after a refused save, want empty", read.EnvironmentName)
			}
		})
	}

	// The bound is in runes, not bytes: 32 characters of a multi-byte
	// script is a name, and rejecting it would be a limit that depends on
	// what alphabet an operator writes in.
	multibyte := strings.Repeat("é", 32)
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{EnvironmentName: &multibyte}); err != nil {
		t.Fatalf("UpdateSettings with a 32-rune multi-byte name: %v", err)
	}
}

// TestUpdateSettingsRoundTripsAgentGitIdentity is grain/task-14's own
// setting: the name and address every agent's commits are authored
// under. Unset, both read back empty with grain's own reported beside
// them, so a pane can show what is actually in effect; setting either
func strPtr(s string) *string { return &s }

// TestUpdateSettingsRoundTripsTaskDefaults is bwsalmon/agents#612's own
// pair of global defaults: unset, both read back true
// (model.DefaultConfig -- NewTaskOverlay.jsx's own "Queue immediately"
// and "Auto-merge once checks pass" checkboxes start checked), and
// turning either off sticks through a GetSettings read the same way
// TestUpdateSettingsRoundTripsShowClosedByDefault already covers for
// ShowClosedByDefault.
//
// The first save is what makes the "unset" half worth pinning: it goes
// through the same PutConfig every other save does, binding both columns,
// so a first save that never mentions these two has to write the default
// rather than the Go zero value beside it.
func TestUpdateSettingsRoundTripsTaskDefaults(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !read.ApprovedByDefault {
		t.Fatalf("ApprovedByDefault = false with nothing set, want true")
	}
	if !read.AutoMergeByDefault {
		t.Fatalf("AutoMergeByDefault = false with nothing set, want true")
	}

	approved, autoMerge := false, false
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		ApprovedByDefault:  &approved,
		AutoMergeByDefault: &autoMerge,
	}); err != nil {
		t.Fatal(err)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.ApprovedByDefault {
		t.Fatalf("ApprovedByDefault = true after UpdateSettings, want false")
	}
	if read.AutoMergeByDefault {
		t.Fatalf("AutoMergeByDefault = true after UpdateSettings, want false")
	}
}

// grain/task-14: the default capability set round-trips like every
// other setting, and the Capabilities tab's own per-capability status
// says which ones it names -- a capability every task is filed holding
// is the one whose readiness an operator most needs to see.
func TestUpdateSettingsRoundTripsDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.DefaultCapabilities) != 0 {
		t.Fatalf("DefaultCapabilities = %v with nothing set, want none", read.DefaultCapabilities)
	}

	// Repeated deliberately: a picker can produce a duplicate, and this
	// is a set.
	defaults := []string{"gcp-key", "gcp-key"}
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &defaults})
	if err != nil {
		t.Fatalf("setting default capabilities: %v", err)
	}
	if !reflect.DeepEqual(saved.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("DefaultCapabilities = %v, want [gcp-key] with the repeat dropped", saved.DefaultCapabilities)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("DefaultCapabilities = %v after a re-read, want [gcp-key]", read.DefaultCapabilities)
	}
	for _, status := range read.Capabilities {
		if want := status.ID == "gcp-key"; status.Default != want {
			t.Errorf("capability %s: Default = %t, want %t", status.ID, status.Default, want)
		}
	}

	// Empty is a value, not "leave it alone": it is how the set is turned
	// back off.
	none := []string{}
	cleared, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &none})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.DefaultCapabilities) != 0 {
		t.Fatalf("DefaultCapabilities = %v after clearing, want none", cleared.DefaultCapabilities)
	}
}

// Defaulting a capability no task could be granted by hand would be a
// setting that failed silently at every filing, so it is refused while
// whoever chose it is still looking at it -- unlike a stored id grain
// has since retired, which CreateTask skips (TestCreateTaskSkipsRetired
// DefaultCapability).
func TestUpdateSettingsRejectsUnknownDefaultCapability(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	defaults := []string{"not-a-real-capability"}
	_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &defaults})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.DefaultCapabilities) != 0 {
		t.Fatalf("DefaultCapabilities = %v after a refused save, want none stored", read.DefaultCapabilities)
	}
}

// grain/task-43: the other half of the rejection above -- a save that
// *drops* a stored id this build no longer offers goes through, even
// though one that *adds* an unknown id does not.
//
// That asymmetry is the whole recovery path. The set is reported as
// stored, retired ids included, so that an operator can see one and
// clear it (Settings.DefaultCapabilities); the picker offers a row for
// it purely to be unticked (capabilityRows, ui/src/state.js). Both are
// worth nothing unless the save that arrives with the id gone is
// accepted, and it is, because what is validated is the set being
// written rather than the set already there. A check that also ran over
// what was stored would pin the whole Capabilities tab: every save of
// it, of any field, would be refused for an id nothing could remove.
func TestUpdateSettingsAcceptsDroppingARetiredDefaultCapability(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	// Written straight to the store, which is the only way this state
	// arises: UpdateSettings itself would never have accepted the id, so
	// it can only be one that had a row when it was chosen and lost it to
	// an upgrade since (OfferedCapabilities' own "scratch-repo", now
	// github-sandbox).
	putDefaultCapabilities(t, ctx, store, "gcp-key", "scratch-repo")

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.DefaultCapabilities, []string{"gcp-key", "scratch-repo"}) {
		t.Fatalf("DefaultCapabilities = %v, want it reported as stored: an operator can only clear one they can see",
			read.DefaultCapabilities)
	}

	kept := []string{"gcp-key"}
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &kept})
	if err != nil {
		t.Fatalf("dropping a retired default capability: %v", err)
	}
	if !reflect.DeepEqual(saved.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("DefaultCapabilities = %v, want [gcp-key] with the retired id gone", saved.DefaultCapabilities)
	}
	read, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("DefaultCapabilities = %v after a re-read, want [gcp-key]: the retired id is gone for good",
			read.DefaultCapabilities)
	}
}

// grain/task-24: with a second layer, the Capabilities tab has to say
// which one defaults a capability. Default stays deployment-wide --
// "every task filed here" -- and DefaultRepos names the repos that add
// it on their own, so the tab never describes a deployment-wide default
// that only some tasks actually get.
func TestSettingsReportsWhichLayerDefaultsACapability(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	deployment := []string{"gemini-key"}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &deployment}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/gadgets", []string{"gcp-key", "gemini-key"}); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ui.CapabilityStatus{}
	for _, status := range read.Capabilities {
		byID[status.ID] = status
	}
	if got := byID["gemini-key"]; !got.Default {
		t.Errorf("gemini-key Default = false, want true: the deployment defaults it")
	}
	// Reported even though the deployment already defaults it: dropping
	// the deployment-wide entry leaves acme/gadgets' own standing.
	if got := byID["gemini-key"].DefaultRepos; !reflect.DeepEqual(got, []string{"acme/gadgets"}) {
		t.Errorf("gemini-key DefaultRepos = %v, want [acme/gadgets]", got)
	}
	if got := byID["gcp-key"]; got.Default {
		t.Errorf("gcp-key Default = true, want false: no task outside those two repos is filed with it")
	}
	// Sorted by repo, the order Store.ListRepoConfigs returns.
	if got := byID["gcp-key"].DefaultRepos; !reflect.DeepEqual(got, []string{"acme/gadgets", "acme/widgets"}) {
		t.Errorf("gcp-key DefaultRepos = %v, want [acme/gadgets acme/widgets]", got)
	}
	if got := byID["self-debug"].DefaultRepos; len(got) != 0 {
		t.Errorf("self-debug DefaultRepos = %v, want none", got)
	}
}

// A repo can be given defaults before a deployment has ever saved a
// settings row -- nothing about SetRepoDefaultCapabilities requires one
// -- and the Capabilities tab has to say so on that deployment too,
// rather than hiding it until someone presses Save on an unrelated pane.
func TestSettingsReportsRepoDefaultsBeforeAnythingIsSaved(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"}); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Configured {
		t.Fatalf("Configured = true, want false: no settings row was ever saved")
	}
	for _, status := range read.Capabilities {
		if status.ID != "gcp-key" {
			continue
		}
		if !reflect.DeepEqual(status.DefaultRepos, []string{"acme/widgets"}) {
			t.Fatalf("gcp-key DefaultRepos = %v, want [acme/widgets]", status.DefaultRepos)
		}
	}
}

// TestGetSettingsReportsTaskDefaultsBeforeAnythingIsSaved is the same
// pair read on a deployment with no grain_config row at all -- the state
// an operator opening Settings on a fresh install sees. Configured is
// false there, but the two checkboxes still have to describe what filing
// a task would actually do, which is model.DefaultConfig's "on" rather
// than the zero value of a Settings nobody has written yet.
func TestGetSettingsReportsTaskDefaultsBeforeAnythingIsSaved(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Configured {
		t.Fatalf("Configured = true with nothing saved, want false")
	}
	if !read.ApprovedByDefault {
		t.Fatalf("ApprovedByDefault = false with nothing saved, want true")
	}
	if !read.AutoMergeByDefault {
		t.Fatalf("AutoMergeByDefault = false with nothing saved, want true")
	}
}

// TestGetSettingsReportsGrainsOwnSandboxDefaults covers the other half of
// what a Settings pane shows for an unset sandbox shape: the three
// *Default fields, which are the size a sandbox is really built at when
// the stored value is 0 (granule.DefaultCPUs/DefaultMemoryMB/
// DefaultDiskGB, passed on every create). They are reported on both
// paths, since a deployment with no grain_config row is exactly the one
// whose shape is entirely these numbers.
//
// Disk is the one worth pinning: it reported no default at all until
// grain started naming every dimension itself, so a build that quietly
// dropped it would put the pane back to explaining why the box has no
// placeholder.
func TestGetSettingsReportsRunningSandboxBackendBeforeFirstSave(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = []model.SandboxBackend{model.SandboxBackendContainer}
	c.Config.RunningConfig = func() model.Config {
		return model.Config{SandboxBackend: model.SandboxBackendContainer}
	}

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Configured {
		t.Fatal("fresh settings reported configured")
	}
	if settings.SandboxBackend != "container" ||
		!reflect.DeepEqual(settings.SandboxBackends, []string{"container"}) {
		t.Fatalf("fresh sandbox settings = backend %q, choices %v; want container, [container]",
			settings.SandboxBackend, settings.SandboxBackends)
	}
	if len(settings.SandboxBackendOptions) != len(model.SandboxBackends()) {
		t.Fatalf("SandboxBackendOptions length = %d, want %d", len(settings.SandboxBackendOptions), len(model.SandboxBackends()))
	}
	for _, opt := range settings.SandboxBackendOptions {
		if opt.Backend == model.SandboxBackendContainer {
			if !opt.Available || opt.Reason != "" {
				t.Fatalf("container option = %+v, want available with empty reason", opt)
			}
		} else if opt.Available || opt.Reason == "" {
			t.Fatalf("unavailable option %q = %+v, want Available=false with non-empty Reason", opt.Backend, opt)
		}
	}
}

func TestUpdateSettingsSandboxBackendUsesAvailableMethodsAndNeedsRestart(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = []model.SandboxBackend{
		model.SandboxBackendContainer,
		model.SandboxBackendGranule,
	}
	c.Config.RunningConfig = func() model.Config {
		return model.Config{SandboxBackend: model.SandboxBackendGranule}
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	container := string(model.SandboxBackendContainer)
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxBackend: &container})
	if err != nil {
		t.Fatal(err)
	}
	if saved.SandboxBackend != container {
		t.Fatalf("SandboxBackend = %q, want %q", saved.SandboxBackend, container)
	}
	if !reflect.DeepEqual(saved.SandboxBackends, []string{"container", "granule"}) {
		t.Fatalf("SandboxBackends = %v, want [container granule]", saved.SandboxBackends)
	}
	if !slices.Contains(saved.RestartRequired, "sandboxBackend") ||
		!slices.Contains(saved.PendingRestart, "sandboxBackend") {
		t.Fatalf("backend restart annotations = required %v, pending %v", saved.RestartRequired, saved.PendingRestart)
	}

	stored, err := c.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SandboxBackend != model.SandboxBackendContainer {
		t.Fatalf("stored SandboxBackend = %q, want container", stored.SandboxBackend)
	}
}

func TestUpdateSettingsRejectsUnavailableSandboxBackend(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = []model.SandboxBackend{model.SandboxBackendContainer}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"granule", "host", "unknown"} {
		t.Run(value, func(t *testing.T) {
			_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxBackend: &value})
			if err == nil {
				t.Fatalf("UpdateSettings accepted sandboxBackend %q", value)
			}
		})
	}
}

func TestGetSettingsReportsGrainsOwnSandboxDefaults(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	fresh, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	saved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		s    ui.Settings
	}{{"fresh store", fresh}, {"saved settings", saved}} {
		if tc.s.SandboxCPUsDefault != granule.DefaultCPUs {
			t.Errorf("%s: SandboxCPUsDefault = %d, want %d", tc.name, tc.s.SandboxCPUsDefault, granule.DefaultCPUs)
		}
		if tc.s.SandboxMemoryMBDefault != granule.DefaultMemoryMB {
			t.Errorf("%s: SandboxMemoryMBDefault = %d, want %d", tc.name, tc.s.SandboxMemoryMBDefault, granule.DefaultMemoryMB)
		}
		if tc.s.SandboxDiskGBDefault != granule.DefaultDiskGB {
			t.Errorf("%s: SandboxDiskGBDefault = %d, want %d", tc.name, tc.s.SandboxDiskGBDefault, granule.DefaultDiskGB)
		}
	}
}

// TestGetSettingsReportsWhenTheSandboxShapeIsIgnored is the annotation
// the three numbers above are shown with on a deployment whose sandbox
// backend has no shape at all (Config.SandboxShapeIgnored,
// grain/task-9): a host-directory sandbox is sized by nothing, and a
// pane showing "2 vCPUs / 2048 MiB" unqualified describes a cap that
// does not exist.
//
// Reported on both paths, like the defaults above: a fresh deployment is
// exactly where someone is deciding what to type into those boxes. And
// reported *alongside* the stored values rather than instead of them --
// they are still stored, still editable, and still what this deployment
// would build VMs at if it were pointed at granule.
func TestGetSettingsReportsWhenTheSandboxShapeIsIgnored(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// The default client speaks for no backend, and says nothing.
	quiet, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.SandboxShapeIgnored {
		t.Errorf("SandboxShapeIgnored = true with no backend to speak for, want false")
	}

	c.Config.SandboxShapeIgnored = true
	fresh, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.SandboxShapeIgnored {
		t.Errorf("SandboxShapeIgnored = false on a fresh store, want true")
	}

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	cpus := 4
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxCPUs: &cpus})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.SandboxShapeIgnored {
		t.Errorf("SandboxShapeIgnored = false in an update response, want true")
	}
	if saved.SandboxCPUs != cpus {
		t.Errorf("SandboxCPUs = %d, want the stored %d reported alongside the annotation", saved.SandboxCPUs, cpus)
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !read.SandboxShapeIgnored || read.SandboxCPUs != cpus {
		t.Errorf("GetSettings = %d cpus, ignored %v, want %d and true", read.SandboxCPUs, read.SandboxShapeIgnored, cpus)
	}
}

// The restart-only settings and the pending-restart report they exist
// for: every other setting on this pane reaches the running daemon
// within a poll interval (pkg/cli/daemon.go's liveConfig), so the two
// that do not have to say so -- both up front, as an annotation on the
// field, and afterwards, once one has been changed and is sitting saved
// but not applied.

// contains reports whether keys names key -- Settings.RestartRequired
// and PendingRestart are small, unordered lists, so this reads better at
// the call sites below than a sorted reflect.DeepEqual would.
func contains(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// runningConfig points a test client's Config.RunningConfig at a fixed
// model.Config -- standing in for the daemon liveConfig would otherwise
// be answering for.
func runningConfig(c *ui.Client, running model.Config) {
	c.Config.RunningConfig = func() model.Config { return running }
}

func TestSettingsReportsWhichSettingsNeedARestart(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Before anything has ever been saved: the annotation belongs on the
	// field from the first look at it, not only once a value exists.
	fresh, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(fresh.RestartRequired, "githubHost") || !contains(fresh.RestartRequired, "githubInsecureHttp") {
		t.Fatalf("restartRequired = %v on an unconfigured deployment, want both GitHub host settings", fresh.RestartRequired)
	}
	if len(fresh.PendingRestart) != 0 {
		t.Fatalf("pendingRestart = %v with nothing stored, want none", fresh.PendingRestart)
	}

	// And the settings a running daemon does pick up on its own are
	// deliberately absent: this list is what the UI annotates, so
	// anything named here is a promise that it cannot be applied live.
	for _, live := range []string{"pollInterval", "geminiModel", "claudeModel",
		"maxAgentTurns", "agentFramework", "gcpProject", "sandboxCpus", "sandboxMemoryMb", "sandboxDiskGb"} {
		if contains(fresh.RestartRequired, live) {
			t.Errorf("restartRequired names %q, which the daemon applies without a restart", live)
		}
	}

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	saved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(saved.RestartRequired, "githubHost") {
		t.Fatalf("restartRequired = %v once configured, want githubHost", saved.RestartRequired)
	}
}

func TestSettingsReportsARestartOnlyChangeAsPending(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	// What the daemon is actually running with: what firstSettings just
	// stored, which is the state right after a restart -- including the
	// priority classes a stored row defaults to.
	runningConfig(c, model.Config{
		GitHubHost:                  "github.com",
		K8sPriorityClassInteractive: model.DefaultK8sPriorityClassInteractive,
		K8sPriorityClassAgent:       model.DefaultK8sPriorityClassAgent,
		K8sPriorityClassCI:          model.DefaultK8sPriorityClassCI,
	})

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.PendingRestart) != 0 {
		t.Fatalf("pendingRestart = %v with stored and running agreeing, want none", settings.PendingRestart)
	}

	// Now change one, the way an operator does. The update's own
	// response has to carry it: this is the moment the UI puts the
	// "saved, but not applied" warning up.
	host := "github.internal"
	updated, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GitHubHost: &host})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(updated.PendingRestart, "githubHost") {
		t.Fatalf("pendingRestart = %v right after changing githubHost, want it named", updated.PendingRestart)
	}
	if contains(updated.PendingRestart, "githubInsecureHttp") {
		t.Fatalf("pendingRestart = %v, want only the setting that actually changed", updated.PendingRestart)
	}

	// And changing it back clears it again -- a pending restart is a
	// comparison, not a flag something has to remember to unset.
	back := "github.com"
	restored, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GitHubHost: &back})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.PendingRestart) != 0 {
		t.Fatalf("pendingRestart = %v after putting githubHost back, want none", restored.PendingRestart)
	}
}

// A UI with no daemon to speak for (`grain demo`, or any client built
// without Config.RunningConfig) reports nothing pending rather than
// comparing against a zero model.Config, which would call every setting
// changed.
func TestSettingsReportsNoPendingRestartWithoutARunningConfig(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.PendingRestart) != 0 {
		t.Fatalf("pendingRestart = %v with no RunningConfig configured, want none", settings.PendingRestart)
	}
	if len(settings.RestartRequired) == 0 {
		t.Fatal("restartRequired is empty; the annotation does not depend on having a running config to compare with")
	}
}

// The third state beside those two (grain/task-369): a stored value no
// daemon can run on, which the running one has replaced with something
// it can (pkg/cli/daemon.go's runnableStoredConfig). Before this, the
// only record that the substitution had happened at all was the
// daemon's own journal -- a browser was shown "poll interval: 0s" while
// the reconcile loop ticked every 30s.

// unrunnableStored is the row the tests below are about: the values a
// deployment is paced by left where a settings import or a hand-edited
// database can leave them, with everything else filled in so nothing
// else about the row is in question. It has to be written through the
// store rather than saved: UpdateSettings refuses every one of these,
// which is why such a row can only arrive from somewhere with no
// validation in front of it.
func unrunnableStored() model.Config {
	return model.Config{
		PollInterval: 0,
		GitHubHost:   "github.com",
	}
}

// runningInsteadOf is what a daemon reading unrunnableStored() ends up
// running: its own flags, in the three places the row said nothing it
// could use.
func runningInsteadOf() model.Config {
	return model.Config{
		PollInterval: 30 * time.Second,
		GitHubHost:   "github.com",
	}
}

// unrunnableByKey is Settings.Unrunnable as a lookup, the way a pane
// reads it: keyed by the JSON field name the annotation goes on.
func unrunnableByKey(t *testing.T, s ui.Settings) map[string]ui.UnrunnableSetting {
	t.Helper()
	byKey := make(map[string]ui.UnrunnableSetting, len(s.Unrunnable))
	for _, u := range s.Unrunnable {
		byKey[u.Key] = u
	}
	return byKey
}

func TestSettingsReportsAStoredValueNoDaemonCanRunOn(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, unrunnableStored()); err != nil {
		t.Fatal(err)
	}
	runningConfig(c, runningInsteadOf())

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := unrunnableByKey(t, settings)
	for key, want := range map[string]struct{ stored, running string }{
		"pollInterval": {"0s", "30s"},
	} {
		got, ok := byKey[key]
		if !ok {
			t.Errorf("unrunnable = %+v, want %s named", settings.Unrunnable, key)
			continue
		}
		if got.Stored != want.stored || got.Running != want.running {
			t.Errorf("unrunnable %s = stored %q running %q, want stored %q running %q",
				key, got.Stored, got.Running, want.stored, want.running)
		}
	}

	// And the fields themselves report what the deployment is doing, not
	// the row it could not do it with: a pane pre-filled from this holds
	// a poll interval a save would accept, where "0s" is a value the pane
	// would have put in the form for UpdateSettings to refuse.
	if settings.PollInterval != "30s" {
		t.Fatalf("pollInterval = %s, want the substituted 30s", settings.PollInterval)
	}

	// Not a pending restart, and deliberately not: restarting reads the
	// same row and substitutes the same value again.
	if len(settings.PendingRestart) != 0 {
		t.Fatalf("pendingRestart = %v, want an unrunnable value reported as its own state", settings.PendingRestart)
	}
}

// A row a daemon can run on reports nothing here, including when the
// daemon has not picked it up yet: a stored value that merely differs
// from the running one is an ordinary save waiting a poll interval, not
// a value grain refused.
func TestSettingsReportsNothingUnrunnableForAUsableRow(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	running := runningInsteadOf()
	running.PollInterval = 5 * time.Minute
	runningConfig(c, running)

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Unrunnable) != 0 {
		t.Fatalf("unrunnable = %+v for a stored row grain can run on, want none", settings.Unrunnable)
	}
	if settings.PollInterval != "30s" {
		t.Fatalf("pollInterval = %s, want the stored 30s rather than what the daemon is still pacing itself by",
			settings.PollInterval)
	}
}

// Storing a usable value is what ends it -- and the update's own
// response has to say so, since that is what the pane merges over the
// settings it is showing the warning from.
func TestUpdateSettingsClearsAnUnrunnableValue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, unrunnableStored()); err != nil {
		t.Fatal(err)
	}
	runningConfig(c, runningInsteadOf())

	pollInterval := "45s"
	updated, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		PollInterval: &pollInterval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Unrunnable) != 0 {
		t.Fatalf("unrunnable = %+v right after storing usable values, want none", updated.Unrunnable)
	}
	if updated.PollInterval != "45s" {
		t.Fatalf("pollInterval = %s, want the 45s just saved", updated.PollInterval)
	}
}

// A client whose RunningConfig answers with a zero model.Config knows
// nothing about a daemon rather than describing one pacing itself by
// nothing, so there is no substitution to report: naming the same 0s
// twice would be a warning with no fact in it, and "substituting" it
// would put the value a save refuses back in the form.
func TestSettingsReportsNothingUnrunnableAgainstAnEmptyRunningConfig(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, unrunnableStored()); err != nil {
		t.Fatal(err)
	}
	runningConfig(c, model.Config{})

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := unrunnableByKey(t, settings)
	if u, ok := byKey["pollInterval"]; ok {
		t.Errorf("unrunnable names pollInterval as stored %q running %q, want nothing said about a value "+
			"with nothing to replace it", u.Stored, u.Running)
	}
	if settings.PollInterval != "0s" {
		t.Fatalf("pollInterval = %s, want the stored value left alone with nothing runnable to put there",
			settings.PollInterval)
	}
}

// A UI with no daemon to speak for reports nothing, the same answer
// pendingRestart gives without a RunningConfig: there is no substitution
// in force to describe, and nothing to report in place of the stored
// value.
func TestSettingsReportsNothingUnrunnableWithoutARunningConfig(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, unrunnableStored()); err != nil {
		t.Fatal(err)
	}

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Unrunnable) != 0 {
		t.Fatalf("unrunnable = %+v with no RunningConfig configured, want none", settings.Unrunnable)
	}
	if settings.PollInterval != "0s" {
		t.Fatalf("pollInterval = %s, want the stored value reported unchanged when nothing is known to have replaced it",
			settings.PollInterval)
	}
}

// grain/task-38: what happens after grain upgrades itself. A deployment
// that has never chosen checks its upgrades and does not hold its queue
// for them; both halves round-trip; and a mode this build does not know
// is refused at the moment somebody types it rather than read back as
// the default forever afterwards.
func TestUpdateSettingsRoundTripsTheUpgradeCheck(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Before anything has been saved at all: the pane describes what an
	// upgrade here would actually do.
	unsaved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unsaved.UpgradeCheckMode != model.UpgradeCheckAuto || unsaved.UpgradeCheckPausesPipeline {
		t.Fatalf("an unconfigured deployment reports mode %q, pauses %v -- want auto and false",
			unsaved.UpgradeCheckMode, unsaved.UpgradeCheckPausesPipeline)
	}
	if len(unsaved.UpgradeCheckModes) != len(model.UpgradeCheckModes()) {
		t.Errorf("upgradeCheckModes = %v, want the whole vocabulary", unsaved.UpgradeCheckModes)
	}

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	saved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpgradeCheckMode != model.UpgradeCheckAuto {
		t.Fatalf("upgradeCheckMode after a first save that never mentioned it = %q, want auto",
			saved.UpgradeCheckMode)
	}

	mode, pauses := model.UpgradeCheckApprove, true
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		UpgradeCheckMode: &mode, UpgradeCheckPausesPipeline: &pauses,
	})
	if err != nil {
		t.Fatalf("setting the upgrade check: %v", err)
	}
	if got.UpgradeCheckMode != model.UpgradeCheckApprove || !got.UpgradeCheckPausesPipeline {
		t.Fatalf("got mode %q, pauses %v, want approve and true", got.UpgradeCheckMode, got.UpgradeCheckPausesPipeline)
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.UpgradeCheckMode != model.UpgradeCheckApprove || !read.UpgradeCheckPausesPipeline {
		t.Fatalf("read back mode %q, pauses %v, want approve and true",
			read.UpgradeCheckMode, read.UpgradeCheckPausesPipeline)
	}

	typo := "atuo"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{UpgradeCheckMode: &typo}); err == nil {
		t.Error("a mode this build does not know was accepted")
	}
	after, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.UpgradeCheckMode != model.UpgradeCheckApprove {
		t.Errorf("a refused save changed the stored mode to %q", after.UpgradeCheckMode)
	}
}

// The daily automatic upgrade: off and at the default time until
// somebody chooses, a channel saved without a time gets the default,
// both round-trip, and a time or channel that could never fire is
// refused when it is typed.
func TestUpdateSettingsRoundTripsTheAutomaticUpgrade(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	unsaved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unsaved.AutoUpgradeChannel != "" || unsaved.AutoUpgradeTime != model.DefaultAutoUpgradeTime {
		t.Fatalf("an unconfigured deployment reports channel %q at %q, want off at %s",
			unsaved.AutoUpgradeChannel, unsaved.AutoUpgradeTime, model.DefaultAutoUpgradeTime)
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	latest := " latest "
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeChannel: &latest})
	if err != nil {
		t.Fatalf("choosing a channel: %v", err)
	}
	if got.AutoUpgradeChannel != "latest" || got.AutoUpgradeTime != model.DefaultAutoUpgradeTime {
		t.Fatalf("got channel %q at %q, want latest at the default time", got.AutoUpgradeChannel, got.AutoUpgradeTime)
	}

	candidate, at := "candidate", "21:45"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		AutoUpgradeChannel: &candidate, AutoUpgradeTime: &at,
	}); err != nil {
		t.Fatalf("setting the automatic upgrade: %v", err)
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.AutoUpgradeChannel != "candidate" || read.AutoUpgradeTime != "21:45" {
		t.Fatalf("read back channel %q at %q, want candidate at 21:45", read.AutoUpgradeChannel, read.AutoUpgradeTime)
	}

	for _, bad := range []string{"24:00", "9:30", "12:60", "noon", "-1:00", "12:5a"} {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeTime: &bad}); err == nil {
			t.Errorf("auto-upgrade time %q was accepted", bad)
		}
	}
	badBranch := "not a branch.."
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeChannel: &badBranch}); err == nil {
		t.Error("an auto-upgrade channel that is not a branch name was accepted")
	}
	after, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoUpgradeChannel != "candidate" || after.AutoUpgradeTime != "21:45" {
		t.Errorf("a refused save changed the automatic upgrade to %q at %q", after.AutoUpgradeChannel, after.AutoUpgradeTime)
	}

	off := ""
	if got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeChannel: &off}); err != nil {
		t.Fatalf("turning automatic upgrades off: %v", err)
	} else if got.AutoUpgradeChannel != "" {
		t.Errorf("turning automatic upgrades off left channel %q", got.AutoUpgradeChannel)
	}
}

// refusingUpgrader is a deployment whose -upgrade-allowed-branches
// admits only main.
type refusingUpgrader struct{ fakeUpgrader }

func (refusingUpgrader) ValidateBranch(branch string) (string, error) {
	if branch != "main" {
		return "", fmt.Errorf("%s is not an allowed branch", branch)
	}
	return branch, nil
}

// A channel the upgrader would refuse to upgrade onto is refused when
// it is saved, not at three in the morning.
func TestUpdateSettingsRefusesAnAutomaticUpgradeChannelTheUpgraderWouldNot(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Upgrader = &refusingUpgrader{}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	candidate, main := "candidate", "main"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeChannel: &candidate}); err == nil {
		t.Error("a channel the upgrader does not allow was accepted")
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{AutoUpgradeChannel: &main}); err != nil {
		t.Errorf("an allowed channel was refused: %v", err)
	}
}

// grain/task-411: what narrowing targetRepos would strand, told to the
// operator narrowing it.
//
// The dispatch side is already safe -- a task whose repo has come off
// the list is parked rather than run (orchestrator.reconcileTargetRepos,
// grain/task-404) -- and safe is not the same as said. These cover the
// telling: a plan reports what a save would strand, the save itself
// reports what it did strand, and a save that does not narrow anything
// says nothing at all.

// strandingFixture is a deployment restricted to two repos with work in
// both: a queued task in each, a schedule firing nightly into the one
// The custom sandbox image (model.Config.SandboxImage): stored trimmed,
// cleared back to the stamped default by an empty string, refused when
// it is not one reference, and reported with the minimal base image and
// contract a custom one is built against.
func TestUpdateSettingsRoundTripsTheCustomSandboxImage(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.InspectImageLabels = func(context.Context, string) (map[string]string, error) {
		return map[string]string{
			sandboximage.ContractLabel:        strconv.Itoa(sandboximage.Contract),
			sandboximage.GranuleContractLabel: strconv.Itoa(sandboximage.GranuleContract),
		}, nil
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxImage != "" || read.SandboxImageWarning != "" || read.SandboxImageCheck != nil {
		t.Fatalf("with nothing set: image %q, warning %q, check %+v; want none", read.SandboxImage, read.SandboxImageWarning, read.SandboxImageCheck)
	}
	if read.SandboxImageDefault == "" || read.SandboxBaseImage == "" {
		t.Fatalf("default %q / base %q, want both named", read.SandboxImageDefault, read.SandboxBaseImage)
	}
	if read.SandboxContractVersion != sandboximage.Contract || read.SandboxMinimumContractVersion != sandboximage.MinimumContract {
		t.Fatalf("contract %d (minimum %d), want %d (%d)", read.SandboxContractVersion, read.SandboxMinimumContractVersion,
			sandboximage.Contract, sandboximage.MinimumContract)
	}
	if read.SandboxContract == nil || len(read.SandboxContract.RequiredCommon) == 0 {
		t.Fatalf("no contract checklist in the settings: %+v", read.SandboxContract)
	}

	image := "  registry.example/roundtrip/sandbox:v1 "
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &image})
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(image); saved.SandboxImage != want {
		t.Fatalf("SandboxImage = %q in the save response, want %q", saved.SandboxImage, want)
	}
	if saved.SandboxImageWarning != "" || saved.SandboxImageCheck == nil || !saved.SandboxImageCheck.Verified {
		t.Fatalf("an image at the current contract: warning %q, check %+v; want verified and quiet", saved.SandboxImageWarning, saved.SandboxImageCheck)
	}

	bad := "registry.example/a:v1 registry.example/b:v1"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &bad}); err == nil {
		t.Fatal("two references saved as one sandbox image")
	}
	// It reaches docker's argv as a positional argument.
	flag := "--privileged"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &flag}); err == nil {
		t.Fatal("a value docker would read as a flag saved as the sandbox image")
	}
	for _, invalid := range []string{"https://ghcr.io/acme/sandbox:v1", "http://bad", "img;rm"} {
		v := invalid
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &v}); err == nil {
			t.Fatalf("invalid sandboxImage %q saved without error", invalid)
		}
	}

	blank := "  "
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &blank}); err != nil {
		t.Fatal(err)
	}
	if read, err = c.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if read.SandboxImage != "" {
		t.Fatalf("SandboxImage = %q after clearing, want empty", read.SandboxImage)
	}
}

// A custom image that declares no sandbox contract -- one not built FROM
// a grain base image -- is warned about, naming the base it should be
// built FROM; one whose labels cannot be read at all is reported
// unverified rather than incompatible.
func TestACustomSandboxImageWithoutAContractIsWarnedAbout(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = []model.SandboxBackend{model.SandboxBackendGranule, model.SandboxBackendKubernetes}
	c.Config.InspectImageLabels = func(_ context.Context, ref string) (map[string]string, error) {
		if strings.Contains(ref, "missing") {
			return nil, errors.New("manifest unknown")
		}
		return map[string]string{}, nil
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	backend := string(model.SandboxBackendKubernetes)
	image := "registry.example/nolabel/sandbox:v1"
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxBackend: &backend, SandboxImage: &image})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.SandboxBaseImage, "k8s-sandbox-base") {
		t.Fatalf("base image on kubernetes = %q, want k8s-sandbox-base", saved.SandboxBaseImage)
	}
	if !strings.Contains(saved.SandboxImageWarning, "needs rebuilding FROM "+saved.SandboxBaseImage) {
		t.Fatalf("warning = %q, want it to name the base image to rebuild FROM", saved.SandboxImageWarning)
	}

	missing := "registry.example/missing/sandbox:v1"
	saved, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImage: &missing})
	if err != nil {
		t.Fatal(err)
	}
	if saved.SandboxImageCheck == nil || saved.SandboxImageCheck.Verified || !strings.Contains(saved.SandboxImageWarning, "unverified") {
		t.Fatalf("an unreadable image: check %+v, warning %q; want it unverified", saved.SandboxImageCheck, saved.SandboxImageWarning)
	}
}

// The configured zone reaches the frontend, which is the whole point of the
// setting: ui/src/time.js formats every timestamp it prints in whatever
// GET /api/config reports, on every view, from the first paint.
//
// It is asserted against that response rather than against GetSettings
// because the response is what the frontend reads, and because the absence
// of this assertion is what let the setting be inert in five places at
// once: model.Config had no field, GetConfig scanned the column into a
// local it discarded, PutConfig wrote the DefaultTimeZone constant over
// whatever was there, the config response assigned a literal "UTC", and
// UpdateSettingsRequest had no field to set it with. Every one of those
// could be fixed and the setting still not work, so the test that matters
// is the end-to-end one.
func TestTheConfiguredTimeZoneReachesTheFrontend(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	// Nothing stored: "", which the frontend reads as the browser's own
	// zone. Reporting "UTC" here forced every browser into UTC on a
	// deployment nobody had set a clock on (grain/task-322).
	got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["timeZone"] != "" {
		t.Fatalf("timeZone = %v on a fresh deployment, want \"\"", got["timeZone"])
	}

	// The first save has to carry the fields a first save requires.
	if _, err := client.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	zone := "America/Los_Angeles"
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TimeZone: &zone}); err != nil {
		t.Fatal(err)
	}
	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["timeZone"] != zone {
		t.Fatalf("timeZone = %v after setting it, want %q", got["timeZone"], zone)
	}

	// Cleared, it is unset again -- back to each browser's own zone.
	cleared := ""
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TimeZone: &cleared}); err != nil {
		t.Fatal(err)
	}
	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["timeZone"] != "" {
		t.Fatalf("timeZone = %v after clearing, want \"\"", got["timeZone"])
	}

	// UTC chosen on purpose is a choice, and is reported as one.
	utc := model.DefaultTimeZone
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TimeZone: &utc}); err != nil {
		t.Fatal(err)
	}
	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["timeZone"] != model.DefaultTimeZone {
		t.Fatalf("timeZone = %v after choosing UTC, want %q", got["timeZone"], model.DefaultTimeZone)
	}
}

// A name that is not a zone is refused while whoever typed it is still
// looking, rather than stored.
//
// model.LoadLocation cannot fail -- it answers time.UTC for a name it does
// not know -- so a typo would be accepted here and then silently print
// every timestamp in the wrong zone, which is indistinguishable from the
// bug this setting exists to fix.
func TestUpdateSettingsRefusesAZoneThatIsNotOne(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	if _, err := client.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"Pacific Time", "America/Nowhere", "PST8PDT7"} {
		if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TimeZone: &bad}); err == nil {
			t.Errorf("UpdateSettings accepted timeZone %q", bad)
		}
	}
	// And the real ones are still accepted, so the check is not simply
	// refusing everything.
	for _, good := range []string{"UTC", "America/Los_Angeles", "Europe/London", "Asia/Tokyo"} {
		if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TimeZone: &good}); err != nil {
			t.Errorf("UpdateSettings refused timeZone %q: %v", good, err)
		}
	}
}

func TestUpdateSettingsRoundTripsKubernetesSettingsAndValidatesNodeSelector(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = model.SandboxBackends()

	unsaved, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unsaved.K8sNamespaceDefault != model.DefaultK8sNamespace ||
		unsaved.K8sSandboxImageDefault != model.DefaultK8sSandboxImage ||
		unsaved.K8sGranuleImageDefault != model.DefaultSandboxImage ||
		unsaved.K8sWorkspaceDefault != model.DefaultK8sWorkspace ||
		unsaved.K8sDeploymentDefault != model.DefaultK8sDeployment ||
		unsaved.K8sContainerDefault != model.DefaultK8sContainer {
		t.Fatalf("unexpected K8s defaults on unconfigured deployment: %+v", unsaved)
	}

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	badSelector := []string{"pool=sandboxes", "missing-equals"}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{K8sNodeSelector: &badSelector}); err == nil {
		t.Fatal("UpdateSettings accepted a malformed k8sNodeSelector entry")
	}

	// A namespace or PriorityClass name Kubernetes could never accept is
	// refused where it is typed, by the setting's name, rather than on
	// every pod grain creates (2026-10-29 walkthrough: "Bad_Class").
	for _, tc := range []struct {
		key string
		req func(*string) ui.UpdateSettingsRequest
	}{
		{"k8sNamespace", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sNamespace: v} }},
		{"k8sPriorityClassCi", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sPriorityClassCI: v} }},
		{"k8sPriorityClassAgent", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sPriorityClassAgent: v} }},
		{"k8sPriorityClassInteractive", func(v *string) ui.UpdateSettingsRequest {
			return ui.UpdateSettingsRequest{K8sPriorityClassInteractive: v}
		}},
		{"k8sServiceAccount", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sServiceAccount: v} }},
		{"k8sSandboxService", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sSandboxService: v} }},
		{"k8sDeployment", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sDeployment: v} }},
		{"k8sContainer", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sContainer: v} }},
	} {
		bad := "Bad_Name"
		if _, err := c.UpdateSettings(ctx, tc.req(&bad)); err == nil || !strings.HasPrefix(err.Error(), tc.key+" ") {
			t.Errorf("UpdateSettings(%s=%q) = %v, want a refusal naming %s", tc.key, bad, err, tc.key)
		}
		good := " walk-low.v1 "
		if tc.key == "k8sNamespace" || tc.key == "k8sSandboxService" || tc.key == "k8sContainer" {
			good = " walk-ns "
		}
		if _, err := c.UpdateSettings(ctx, tc.req(&good)); err != nil {
			t.Errorf("UpdateSettings(%s=%q): %v", tc.key, good, err)
		}
	}
	// The git proxy host is an address: the 2026-10-30 walkthrough saved
	// "not a host!".
	for host, ok := range map[string]bool{"not a host!": false, "http://10.0.0.1": false, "10.96.0.10": true, "grain.grain.svc.cluster.local": true, "fd00::1": true} {
		h := host
		_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{K8sGitProxyHost: &h})
		if ok != (err == nil) {
			t.Errorf("UpdateSettings(k8sGitProxyHost=%q) = %v, want accepted %v", host, err, ok)
		}
	}
	// The pod images are image references and the workspace an absolute
	// path: the 2026-11-01 walkthrough saved "not a ref!!" and
	// "relative/dir".
	for _, tc := range []struct {
		key       string
		req       func(*string) ui.UpdateSettingsRequest
		bad, good string
	}{
		{"k8sSandboxImage", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sSandboxImage: v} }, "not a ref!!", "registry.walk.test/team/k8s-sandbox:v1"},
		{"k8sGranuleImage", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sGranuleImage: v} }, "not a ref!!", "registry.walk.test/team/guest@sha256:" + strings.Repeat("a", 64)},
		{"k8sWorkspace", func(v *string) ui.UpdateSettingsRequest { return ui.UpdateSettingsRequest{K8sWorkspace: v} }, "relative/dir", "/home/walk"},
	} {
		bad, good, blank := tc.bad, tc.good, ""
		if _, err := c.UpdateSettings(ctx, tc.req(&bad)); err == nil || !strings.HasPrefix(err.Error(), tc.key+" ") {
			t.Errorf("UpdateSettings(%s=%q) = %v, want a refusal naming %s", tc.key, bad, err, tc.key)
		}
		for _, v := range []*string{&good, &blank} {
			if _, err := c.UpdateSettings(ctx, tc.req(v)); err != nil {
				t.Errorf("UpdateSettings(%s=%q): %v", tc.key, *v, err)
			}
		}
	}
	none := ""
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{K8sPriorityClassCI: &none}); err != nil {
		t.Errorf("an empty priority class (none) was refused: %v", err)
	}

	backend := string(model.SandboxBackendKubernetesGranule)
	ns := "grain-k8s"
	kubectl := "/usr/local/bin/kubectl"
	sbxImg := "example/k8s-sandbox:v2"
	granuleImg := "example/k8s-granule:v2"
	ws := "/home/debian"
	proxyHost := "10.96.0.10"
	svc := "grain-sandbox-svc"
	sa := "grain-sandbox-sa"
	selector := []string{" pool = kvm ", "zone=us-central1-a"}
	priv := true
	netpol := true
	deploy := "grain-controller"
	ctr := "grain-daemon"

	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxBackend:    &backend,
		K8sNamespace:      &ns,
		K8sKubectl:        &kubectl,
		K8sSandboxImage:   &sbxImg,
		K8sGranuleImage:   &granuleImg,
		K8sWorkspace:      &ws,
		K8sGitProxyHost:   &proxyHost,
		K8sSandboxService: &svc,
		K8sServiceAccount: &sa,
		K8sNodeSelector:   &selector,
		K8sPrivileged:     &priv,
		K8sNetworkPolicy:  &netpol,
		K8sDeployment:     &deploy,
		K8sContainer:      &ctr,
	})
	if err != nil {
		t.Fatalf("UpdateSettings(K8s): %v", err)
	}
	if saved.SandboxBackend != backend ||
		saved.K8sNamespace != ns ||
		saved.K8sKubectl != kubectl ||
		saved.K8sSandboxImage != sbxImg ||
		saved.K8sGranuleImage != granuleImg ||
		saved.K8sWorkspace != ws ||
		saved.K8sWorkspaceDefault != model.DefaultK8sGranuleWorkspace ||
		saved.K8sGitProxyHost != proxyHost ||
		saved.K8sSandboxService != svc ||
		saved.K8sServiceAccount != sa ||
		!reflect.DeepEqual(saved.K8sNodeSelector, []string{"pool=kvm", "zone=us-central1-a"}) ||
		!saved.K8sPrivileged ||
		!saved.K8sNetworkPolicy ||
		saved.K8sDeployment != deploy ||
		saved.K8sContainer != ctr {
		t.Fatalf("saved K8s settings mismatch: %+v", saved)
	}
	if saved.Sources["k8sNamespace"] != "custom" ||
		saved.Sources["k8sSandboxImage"] != "custom" ||
		saved.Sources["k8sGranuleImage"] != "custom" ||
		saved.Sources["k8sWorkspace"] != "custom" ||
		saved.Sources["k8sDeployment"] != "custom" ||
		saved.Sources["k8sContainer"] != "custom" ||
		saved.Sources["k8sKubectl"] != "custom" {
		t.Fatalf("expected custom sources after saving custom K8s fields, got %+v", saved.Sources)
	}
}

func TestK8sImageDefaultsFollowVersionAndTrack(t *testing.T) {
	cases := []struct {
		name         string
		info         version.Info
		track        installtrack.Track
		trackRepo    string
		ossRepo      string
		wantTag      string
		wantBuildVer string
		wantSandbox  string
		wantGranule  string
	}{
		{
			name:         "release build on OSS track",
			info:         version.Info{Release: "v2.2.0", Revision: "8e1a209040ea1234567890abcdef1234567890ab"},
			track:        installtrack.TrackOSS,
			wantTag:      "v2.2.0",
			wantBuildVer: "v2.2.0",
			wantSandbox:  "ghcr.io/bwsalmon/grain/k8s-sandbox:v2.2.0",
			wantGranule:  "ghcr.io/bwsalmon/grain/guest:v2.2.0",
		},
		{
			name:         "main build with +sha metadata pins to sha- tag",
			info:         version.Info{Release: "v2.3.0-dev.114+8e1a209", Revision: "8e1a209040ea1234567890abcdef1234567890ab"},
			track:        installtrack.TrackOSS,
			wantTag:      "sha-8e1a209",
			wantBuildVer: "v2.3.0-dev.114+8e1a209",
			wantSandbox:  "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-8e1a209",
			wantGranule:  "ghcr.io/bwsalmon/grain/guest:sha-8e1a209",
		},
		{
			name:         "candidate build with +sha metadata pins to sha- tag",
			info:         version.Info{Release: "v2.2.0-rc.1.48+6f65669", Revision: "6f65669040ea1234567890abcdef1234567890ab"},
			track:        installtrack.TrackOSS,
			wantTag:      "sha-6f65669",
			wantBuildVer: "v2.2.0-rc.1.48+6f65669",
			wantSandbox:  "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-6f65669",
			wantGranule:  "ghcr.io/bwsalmon/grain/guest:sha-6f65669",
		},
		{
			name:         "a registered track with its own image repository override picks that registry",
			info:         version.Info{Release: "v2.2.0"},
			track:        installtracktest.Track,
			trackRepo:    "mirror.example/acme/grain",
			wantTag:      "v2.2.0",
			wantBuildVer: "v2.2.0",
			wantSandbox:  "mirror.example/acme/k8s-sandbox:v2.2.0",
			wantGranule:  "mirror.example/acme/guest:v2.2.0",
		},
		{
			name:         "dev build (Unversioned) falls back to latest",
			info:         version.Info{Release: "v1.14.0-rc.1.0.20261022-8e1a209040ea", Revision: "8e1a209040ea", Unversioned: true},
			track:        installtrack.TrackOSS,
			wantTag:      "latest",
			wantBuildVer: "dev",
			wantSandbox:  "ghcr.io/bwsalmon/grain/k8s-sandbox:latest",
			wantGranule:  "ghcr.io/bwsalmon/grain/guest:latest",
		},
		{
			name:         "unstamped test binary falls back to latest",
			info:         version.Info{},
			track:        installtrack.TrackOSS,
			wantTag:      "latest",
			wantBuildVer: "dev",
			wantSandbox:  "ghcr.io/bwsalmon/grain/k8s-sandbox:latest",
			wantGranule:  "ghcr.io/bwsalmon/grain/guest:latest",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(installtracktest.RepositoryEnv, tc.trackRepo)
			t.Setenv("GRAIN_OSS_IMAGE_REPO", tc.ossRepo)

			if got := ui.ImageTagForVersion(tc.info); got != tc.wantTag {
				t.Errorf("ImageTagForVersion(%+v) = %q, want %q", tc.info, got, tc.wantTag)
			}
			if got := ui.DaemonBuildVersion(tc.info); got != tc.wantBuildVer {
				t.Errorf("DaemonBuildVersion(%+v) = %q, want %q", tc.info, got, tc.wantBuildVer)
			}
			if got := ui.DefaultK8sSandboxImageFor(tc.track, tc.info); got != tc.wantSandbox {
				t.Errorf("DefaultK8sSandboxImageFor(%q, %+v) = %q, want %q", tc.track, tc.info, got, tc.wantSandbox)
			}
			if got := ui.DefaultSandboxImageFor(tc.track, tc.info); got != tc.wantGranule {
				t.Errorf("DefaultSandboxImageFor(%q, %+v) = %q, want %q", tc.track, tc.info, got, tc.wantGranule)
			}
		})
	}

	t.Run("falls back to OSS registry when the track's registry does not have the tag", func(t *testing.T) {
		t.Setenv(installtracktest.RepositoryEnv, "mirror.example/acme/grain")
		info := version.Info{Release: "v2.2.0"}
		missingOnGAR := func(ref string) bool {
			return !strings.HasPrefix(ref, "mirror.example/")
		}
		if got := ui.ResolveDefaultK8sSandboxImage(installtracktest.Track, info, missingOnGAR); got != "ghcr.io/bwsalmon/grain/k8s-sandbox:v2.2.0" {
			t.Errorf("ResolveDefaultK8sSandboxImage fallback = %q, want ghcr.io/bwsalmon/grain/k8s-sandbox:v2.2.0", got)
		}
		if got := ui.ResolveDefaultSandboxImage(installtracktest.Track, info, missingOnGAR); got != "ghcr.io/bwsalmon/grain/guest:v2.2.0" {
			t.Errorf("ResolveDefaultSandboxImage fallback = %q, want ghcr.io/bwsalmon/grain/guest:v2.2.0", got)
		}
	})
}

func TestK8sSettingsSourcesWarningsAndPendingRestart(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = model.SandboxBackends()
	c.Config.DaemonVersion = func() version.Info {
		return version.Info{Release: "v2.3.0-dev.114+8e1a209", Revision: "8e1a209040ea1234567890abcdef1234567890ab"}
	}
	c.Config.CheckImageManifest = func(_ context.Context, imageRef string) error {
		if strings.HasSuffix(imageRef, ":missing-tag") {
			return fmt.Errorf("%w: %s not found", ui.ErrImageNotFound, imageRef)
		}
		return nil
	}

	// Running config uses the build-pinned default images; stored config has empty strings (meaning "use default").
	running := model.DefaultConfig()
	running.K8sNamespace = model.DefaultK8sNamespace
	running.K8sKubectl = model.DefaultK8sKubectl
	running.K8sSandboxImage = "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-8e1a209"
	running.K8sGranuleImage = "ghcr.io/bwsalmon/grain/guest:sha-8e1a209"
	running.K8sWorkspace = model.DefaultK8sWorkspace
	running.K8sDeployment = model.DefaultK8sDeployment
	running.K8sContainer = model.DefaultK8sContainer
	c.Config.RunningConfig = func() model.Config { return running }

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	s, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.DaemonBuildVersion != "v2.3.0-dev.114+8e1a209" {
		t.Fatalf("DaemonBuildVersion = %q, want v2.3.0-dev.114+8e1a209", s.DaemonBuildVersion)
	}
	if s.K8sSandboxImageDefault != "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-8e1a209" {
		t.Fatalf("K8sSandboxImageDefault = %q, want sha-8e1a209", s.K8sSandboxImageDefault)
	}
	if s.K8sGranuleImageDefault != "ghcr.io/bwsalmon/grain/guest:sha-8e1a209" {
		t.Fatalf("K8sGranuleImageDefault = %q, want sha-8e1a209", s.K8sGranuleImageDefault)
	}
	if s.K8sKubectlDefault != "kubectl" {
		t.Fatalf("K8sKubectlDefault = %q, want kubectl", s.K8sKubectlDefault)
	}
	for _, key := range []string{
		"k8sNamespace",
		"k8sSandboxImage",
		"k8sGranuleImage",
		"k8sWorkspace",
		"k8sDeployment",
		"k8sContainer",
		"k8sKubectl",
	} {
		if s.Sources[key] != "default" {
			t.Errorf("Sources[%q] = %q, want default", key, s.Sources[key])
		}
		if slices.Contains(s.PendingRestart, key) {
			t.Errorf("PendingRestart unexpectedly contains %q when stored is empty (default): %v", key, s.PendingRestart)
		}
	}

	// Customizing k8sSandboxImage to a missing tag marks source="custom", triggers a warning, and marks pendingRestart.
	missingImg := "ghcr.io/bwsalmon/grain/k8s-sandbox:missing-tag"
	updated, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		K8sSandboxImage: &missingImg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Sources["k8sSandboxImage"] != "custom" {
		t.Errorf("Sources[k8sSandboxImage] = %q, want custom", updated.Sources["k8sSandboxImage"])
	}
	if !strings.Contains(updated.K8sSandboxImageWarning, "missing-tag") {
		t.Errorf("K8sSandboxImageWarning = %q, want warning mentioning missing-tag", updated.K8sSandboxImageWarning)
	}
	if !slices.Contains(updated.PendingRestart, "k8sSandboxImage") {
		t.Errorf("PendingRestart = %v, want k8sSandboxImage included", updated.PendingRestart)
	}

	// Resetting k8sSandboxImage to "" clears the custom value, clears pendingRestart, and restores source="default".
	empty := ""
	reset, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		K8sSandboxImage: &empty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reset.K8sSandboxImage != "" {
		t.Errorf("K8sSandboxImage after reset = %q, want empty", reset.K8sSandboxImage)
	}
	if reset.Sources["k8sSandboxImage"] != "default" {
		t.Errorf("Sources[k8sSandboxImage] after reset = %q, want default", reset.Sources["k8sSandboxImage"])
	}
	if reset.K8sSandboxImageWarning != "" {
		t.Errorf("K8sSandboxImageWarning after reset = %q, want empty", reset.K8sSandboxImageWarning)
	}
	if slices.Contains(reset.PendingRestart, "k8sSandboxImage") {
		t.Errorf("PendingRestart after reset still contains k8sSandboxImage: %v", reset.PendingRestart)
	}
}

func TestCheckImageManifestHTTPServer(t *testing.T) {
	t.Parallel()
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"test-bearer-tok"}`))
		case "/v2/bwsalmon/grain/k8s-sandbox/manifests/v2.2.0":
			if r.Header.Get("Authorization") != "Bearer test-bearer-tok" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry",scope="repository:bwsalmon/grain/k8s-sandbox:pull"`, srvURL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/v2/bwsalmon/grain/k8s-sandbox/manifests/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	host := strings.TrimPrefix(srv.URL, "http://")

	ctx := context.Background()
	if err := ui.CheckImageManifest(ctx, host+"/bwsalmon/grain/k8s-sandbox:v2.2.0"); err != nil {
		t.Fatalf("CheckImageManifest(v2.2.0) unexpected error: %v", err)
	}
	err := ui.CheckImageManifest(ctx, host+"/bwsalmon/grain/k8s-sandbox:missing")
	if !errors.Is(err, ui.ErrImageNotFound) {
		t.Fatalf("CheckImageManifest(missing) = %v, want ErrImageNotFound", err)
	}
}

// The 2026-11-01 walkthrough saved "not an email" as the GCP service
// account email, which only failed when gcp-key next minted a key.
func TestUpdateSettingsRefusesAGCPServiceAccountEmailThatIsNotOne(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		email string
		ok    bool
	}{
		{"not an email", false},
		{"no-at-sign", false},
		{"@project.iam.gserviceaccount.com", false},
		{"minter@", false},
		{"a@b@c", false},
		{" grain-minter@walk-1.iam.gserviceaccount.com ", true},
		{"", true},
	} {
		e := tc.email
		_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GCPServiceAccountEmail: &e})
		if tc.ok != (err == nil) {
			t.Errorf("UpdateSettings(gcpServiceAccountEmail=%q) = %v, want accepted %v", tc.email, err, tc.ok)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "gcpServiceAccountEmail ") {
			t.Errorf("refusal %q does not name gcpServiceAccountEmail", err)
		}
		if tc.ok && err == nil && tc.email != "" {
			saved, err := c.GetSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.GCPServiceAccountEmail != strings.TrimSpace(tc.email) {
				t.Errorf("GCPServiceAccountEmail = %q, want %q trimmed", saved.GCPServiceAccountEmail, tc.email)
			}
		}
	}
}

// TestUpdateSettingsRefusesAGCPProjectThatIsNotAProjectID: the 2026-11-01
// walkthrough saved "Bad Project!" from Legacy capabilities, which every
// gcp-key and gemini-key attempt would then have failed on.
func TestUpdateSettingsRefusesAGCPProjectThatIsNotAProjectID(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	for project, ok := range map[string]bool{
		"Bad Project!":           false,
		"UPPER-case-1":           false,
		"short":                  false,
		"1starts-with-digit":     false,
		"ends-with-hyphen-":      false,
		"my-project-123":         true,
		" walkthrough-project ":  true,
		"example.com:my-project": true,
		"":                       true,
	} {
		p := project
		got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GCPProject: &p})
		if ok != (err == nil) {
			t.Errorf("UpdateSettings(gcpProject=%q) = %v, want accepted %v", project, err, ok)
			continue
		}
		if !ok && !strings.HasPrefix(err.Error(), "gcpProject ") {
			t.Errorf("UpdateSettings(gcpProject=%q) = %v, want a refusal naming gcpProject", project, err)
		}
		if ok && got.GCPProject != strings.TrimSpace(project) {
			t.Errorf("UpdateSettings(gcpProject=%q) stored %q", project, got.GCPProject)
		}
	}
}
