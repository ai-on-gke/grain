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

// PlanSettings (settings.go) is UpdateSettings with the write left off,
// and the only thing it is worth anything for is being exactly that: a
// `grain sync -dry-run` run against a config repo's pull request reports
// what this returns as what merging the file will do, and a plan that is
// subtly not the save is worse than the "settings are skipped under
// -dry-run" line it replaced (grain/task-241).
//
// So these tests are all one claim from two sides: whatever a save would
// store, a plan reports and does not store -- including the values the
// save normalizes on the way in and the ones it refuses outright, which
// are precisely what a caller comparing a request against GetSettings
// itself would get wrong.

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The whole of it: the plan is what the save then does, field for
// field, and nothing was stored in between.
func TestPlanSettingsIsTheSaveWithoutTheWrite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	before, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	workers, autoMerge := 9, false
	req := ui.UpdateSettingsRequest{MaxMergesPerRepo: &workers, AutoMergeByDefault: &autoMerge}
	planned, err := c.PlanSettings(ctx, req)
	if err != nil {
		t.Fatalf("PlanSettings: %v", err)
	}
	if planned.MaxMergesPerRepo != 9 || planned.AutoMergeByDefault {
		t.Fatalf("planned = %d workers / auto-merge %v, want 9 / false",
			planned.MaxMergesPerRepo, planned.AutoMergeByDefault)
	}

	unchanged, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatalf("a plan changed the stored settings:\n before %+v\n  after %+v", before, unchanged)
	}

	// And the save it described really is the save: same request, same
	// answer, now stored.
	applied, err := c.UpdateSettings(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !reflect.DeepEqual(planned, applied) {
		t.Fatalf("the plan and the save disagree:\n   plan %+v\n  saved %+v", planned, applied)
	}
}

// The normalization cases, which are the reason this is the daemon's
// answer rather than something a CLI works out for itself: each of these
// is a request whose values differ from what is stored as *text* and not
// at all as settings, and a plan has to report the setting.
func TestPlanSettingsReportsNormalizedValuesRatherThanWhatWasSent(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	// "0.5m" is the 30s firstSettings already stored; the name is one
	// with a stray space around it.
	pollInterval, name := "0.5m", "  staging  "
	planned, err := c.PlanSettings(ctx, ui.UpdateSettingsRequest{
		PollInterval: &pollInterval, EnvironmentName: &name,
	})
	if err != nil {
		t.Fatalf("PlanSettings: %v", err)
	}
	if planned.PollInterval != "30s" {
		t.Errorf("planned poll interval = %q, want the stored %q that \"0.5m\" is", planned.PollInterval, "30s")
	}
	if planned.EnvironmentName != "staging" {
		t.Errorf("planned environment name = %q, want it trimmed to %q", planned.EnvironmentName, "staging")
	}
}

// The claim made about every setting at once rather than the three
// above. plannedConfig is the whole of both paths today, so this is
// structural rather than a hope -- but it is the structure that could
// change (a normalization added to UpdateSettings after the plan step,
// or a field applied twice), and a plan silently differing from the
// save on one setting is the failure a config repo's check would carry
// into a merge.
//
// Derived from the type, like `grain sync`'s own diff table
// (TestSettingsDiffRowsCoverEveryUpdatableSetting): a field with no
// value below is one this test says nothing about, and it fails until
// somebody gives it one.
func TestPlanSettingsMatchesTheSaveForEverySettableField(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxBackends = model.SandboxBackends()
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	req := everySettingChanged()
	v := reflect.ValueOf(req)
	for i := range v.NumField() {
		if v.Field(i).IsNil() {
			name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
			t.Fatalf("everySettingChanged leaves %q unset: a setting with no value here is one this test "+
				"never asks the plan about -- give it a valid value", name)
		}
	}

	planned, err := c.PlanSettings(ctx, req)
	if err != nil {
		t.Fatalf("PlanSettings: %v", err)
	}
	applied, err := c.UpdateSettings(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !reflect.DeepEqual(planned, applied) {
		t.Fatalf("the plan and the save disagree:\n   plan %+v\n  saved %+v", planned, applied)
	}
}

// everySettingChanged is one request touching every field of
// ui.UpdateSettingsRequest, each with a value a save accepts -- the
// vocabularies read from the packages that own them, so a word one of
// them stops accepting fails here rather than silently narrowing what
// is covered.
func everySettingChanged() ui.UpdateSettingsRequest {
	return ui.UpdateSettingsRequest{
		PollInterval:            ptr("1m"),
		MaxWorkers:              ptr(4),
		MaxMergesPerRepo:        ptr(2),
		MergeBatchSize:          ptr(4),
		MergeGatherWindow:       ptr("3m"),
		MaxAutoSubmitDepth:      ptr(3),
		MaxAutoSubmitFanOut:     ptr(4),
		MaxAgentTurns:           ptr(42),
		GitHubHost:              ptr("github.example"),
		GitHubInsecureHTTP:      ptr(true),
		GCPProject:              ptr("acme-project"),
		GCPServiceAccountEmail:  ptr("agent@acme-project.iam.gserviceaccount.com"),
		GCPServiceAccountCount:  ptr(3),
		IAPBackendService:       ptr("ui-backend"),
		IAPResource:             ptr("projects/123/iap_web/compute/services/grain-ui"),
		NewestFirst:             ptr(true),
		SandboxBackend:          ptr(string(model.SandboxBackendContainer)),
		SandboxCPUs:             ptr(4),
		SandboxMemoryMB:         ptr(4096),
		SandboxDiskGB:           ptr(50),
		SandboxCPUOvercommit:    ptr(3.0),
		SandboxMemoryOvercommit: ptr(1.5),
		SandboxDiskOvercommit:   ptr(4.0),
		SandboxOvercommit:       &model.OvercommitSettings{Mode: model.OvercommitAuto, CPUMax: 4},
		ExternalAgentAPI:        ptr(true),
		SessionSandboxIdleTTL:   ptr("45m"),
		SandboxMemoryReserveMB:  ptr(4096),
		SandboxDiskFloorShare:   ptr(0.2),
		SandboxDiskFloorMinMB:   ptr(8192),
		SandboxImage:            ptr("registry.example/team/sandbox:v1"),
		SandboxImageMirror:      ptr("us-docker.pkg.dev/acme-project/grain/guest"),
		K8sNamespace:            ptr("grain-sandboxes"),
		K8sKubectl:              ptr("/usr/local/bin/kubectl"),
		K8sSandboxImage:         ptr("ghcr.io/bwsalmon/grain-k8s-sandbox:v1"),
		K8sGranuleImage:         ptr("ghcr.io/bwsalmon/grain-sandbox:v1"),
		K8sWorkspace:            ptr("/home/agent"),
		K8sGitProxyHost:         ptr("10.0.0.5"),
		K8sSandboxService:       ptr("grain-sandbox-proxy"),
		K8sServiceAccount:       ptr("grain-sandbox-sa"),
		K8sNodeSelector:         ptr([]string{"pool=sandboxes"}),
		K8sPrivileged:           ptr(true),
		K8sNetworkPolicy:        ptr(true),
		K8sDeployment:           ptr("grain-controller"),
		K8sContainer:            ptr("grain-daemon"),

		K8sPriorityClassInteractive: ptr("team-interactive"),
		K8sPriorityClassAgent:       ptr("team-agent"),
		K8sPriorityClassCI:          ptr(""),
		PackageCacheEnabled:         ptr(false),
		PackageCacheSizeGB:          ptr(40),
		ActionsCacheEnabled:         ptr(false),
		ActionsCacheSizeGB:          ptr(60),
		CIRunHistory:                ptr(10000),
		CIStepOutputMB:              ptr(128),
		CIStepOutputDays:            ptr(30),
		CIRunOutputKB:               ptr(512),
		CICacheMB:                   ptr(512),
		CICacheEntries:              ptr(8192),
		CICacheDays:                 ptr(60),
		CIArtifactMB:                ptr(4096),
		CIArtifactRunMB:             ptr(256),
		CIArtifactFileMB:            ptr(64),
		CIArtifactDays:              ptr(21),
		CIJobsPerRun:                ptr(6),
		CIAgeBoost:                  ptr("90s"),
		CIReserveAfter:              ptr("30s"),
		NetworkAccess:               ptr(string(model.NetworkAccesses()[0])),
		ShowClosedByDefault:         ptr(true),
		EnvironmentName:             ptr("staging"),
		TimeZone:                    ptr("Europe/London"),
		ApprovedByDefault:           ptr(false),
		AutoMergeByDefault:          ptr(false),
		UpgradeCheckMode:            ptr(model.UpgradeCheckModes()[0]),
		UpgradeCheckPausesPipeline:  ptr(true),
		AutoUpgradeChannel:          ptr("main"),
		AutoUpgradeTime:             ptr("04:30"),
		AgentBackends:               ptr([]model.AgentBackend{{ID: "backend-1", Name: "Primary", Framework: model.AgentFrameworks()[0], Models: []string{"gemini-3.1-pro"}}}),
		DefaultModel:                ptr("gemini-3.1-pro"),
		PromptExtension:             ptr("be brief"),
		RepairTemplateID:            ptr("custom-repair-template"),
		TaskBranchTemplate:          ptr("alice/grain/task-{id}"),
		DeploymentID:                ptr("prod"),
		DefaultCapabilities:         ptr([]string{ui.OfferedCapabilities()[0].ID}),
		SettingsLocked:              ptr(true),
	}
}

// A value a save would refuse is refused by the plan, with the same
// error: a dry run is where a config repo finds out its file is bad,
// and one that reported a change the merge would then reject would be
// worse than useless.
func TestPlanSettingsRefusesWhatASaveWouldRefuse(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	interval, capability := "half an hour", []string{"no-such-capability"}
	for _, tc := range []struct {
		name string
		req  ui.UpdateSettingsRequest
		want string
	}{
		{"an unparseable poll interval", ui.UpdateSettingsRequest{PollInterval: &interval}, "pollInterval"},
		{"an unknown capability", ui.UpdateSettingsRequest{DefaultCapabilities: &capability}, "defaultCapabilities"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.PlanSettings(ctx, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PlanSettings() = %v, want an error naming %s", err, tc.want)
			}
		})
	}
}

// The first save's own required fields are required of a plan too --
// planning against a deployment nothing has ever configured is exactly
// where a file missing one of them should be told about it.
func TestPlanSettingsOnAnUnconfiguredDeploymentDemandsTheFirstSaveFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	workers := 2
	_, err := c.PlanSettings(ctx, ui.UpdateSettingsRequest{MaxMergesPerRepo: &workers})
	if err == nil || !strings.Contains(err.Error(), "the first time settings are saved") {
		t.Fatalf("PlanSettings() = %v, want the first-save requirement", err)
	}
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Configured {
		t.Fatal("a refused plan configured the deployment")
	}
}

// Over the wire: its own route rather than a flag on the PUT, so that a
// daemon predating it 404s a dry run instead of ignoring the flag and
// saving (ui.Server.handlePlanSettings).
func TestPlanSettingsRouteAnswersWithoutWriting(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	if _, err := client.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/settings/plan", `{"maxMergesPerRepo":11}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if planned := decode[ui.Settings](t, rec); planned.MaxMergesPerRepo != 11 {
		t.Fatalf("planned max merges per repo = %d, want 11", planned.MaxMergesPerRepo)
	}

	settings, err := client.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxMergesPerRepo == 11 {
		t.Fatal("POST /api/settings/plan wrote the settings it was asked to plan")
	}
}

// A refused plan is a 400 with the reason, exactly as the PUT is: the
// caller reading it is a workflow log, not a browser that can guess.
func TestPlanSettingsRouteRejectsABadValue(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	if _, err := client.UpdateSettings(context.Background(), firstSettings()); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/settings/plan", `{"pollInterval":"not-a-duration"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
