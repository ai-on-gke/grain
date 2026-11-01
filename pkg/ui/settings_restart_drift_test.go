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

package ui

// restartOnlySettings (settings.go) is the same kind of hand-maintained
// list about UpdateSettingsRequest that `grain sync`'s diff table was
// (pkg/cli/sync.go's settingsDiffRows), and it fails the same silent
// way: a setting a running daemon cannot actually adopt, but that nothing
// names here, is reported to the Settings pane as applied the moment it
// is saved, and the deployment then runs on a value no one is told is not
// in effect.
//
// The list itself cannot be derived from a type -- whether a setting can
// be applied live is a fact about pkg/cli/daemon.go's liveConfig, not
// about this struct, and it is deliberately an allowlist of two rather
// than a reflect-based comparison (restartOnlySetting.Differs' own doc
// comment on why). What can be made structural is that no setting escapes
// the question: every field of UpdateSettingsRequest is either named here
// as restart-only or named below as applied live, by whatever picks it
// up, so a new setting is in neither list until somebody has decided
// which of the two it is.
//
// This lives in package ui rather than ui_test because
// restartOnlySettings is unexported, and it is that list -- not the
// RestartRequired copy of it a response carries -- that is the thing
// worth comparing against.

import (
	"reflect"
	"strings"
	"testing"
)

// settingsAppliedLive is every UpdateSettingsRequest field a *running*
// daemon adopts on its own, mapped to the piece that applies it --
// pkg/cli/daemon.go's liveConfig doc comment, restated as something a
// test can hold against the type instead of prose that goes quietly
// stale. Together with restartOnlySettings it has to account for every
// settable field, which is the whole point of writing it down.
var settingsAppliedLive = map[string]string{
	"pollInterval":           "the reconcile loop's own ticker (liveConfig.refresh)",
	"maxWorkers":             "the reconcile loop's own per-tick refresh (liveConfig.refresh)",
	"maxMergesPerRepo":       "orchestrator.RunCycle's own per-cycle re-read",
	"mergeBatchSize":         "orchestrator.reconcileMergeBatches reads the config row every cycle",
	"mergeGatherWindow":      "orchestrator.reconcileMergeBatches reads the config row every cycle (mergeBatchSettings)",
	"maxAutoSubmitDepth":     "read from the store as each proposal is filed (autoSubmitLimits)",
	"maxAutoSubmitFanOut":    "read from the store as each proposal is filed (autoSubmitLimits)",
	"maxAgentTurns":          "orchestrator.RunCycle's own per-cycle re-read",
	"timeZone":               "GET /api/config carries it, and the frontend formats every timestamp from that response (ui/src/time.js)",
	"agentBackends":          "dispatchConfig and backendPoolManager refresh before each dispatch",
	"defaultModel":           "dispatchConfig's own per-dispatch re-read",
	"gcpProject":             "the capability registry the next cycle resolves grants against (liveConfig.refresh)",
	"gcpServiceAccountEmail": "the capability registry the next cycle resolves grants against (liveConfig.refresh)",
	"gcpServiceAccountCount": "the capability registry the next cycle resolves grants against (liveConfig.refresh)",
	"iapBackendService":      "the IAP IAM policy reconciler (pkg/cli/daemon.go's liveConfig.refresh and TriggerIAPSync)",
	"iapResource":            "the IAP IAM policy reconciler (pkg/cli/daemon.go's liveConfig.refresh and TriggerIAPSync)",
	"sandboxCpus":            "the default shape the next sandbox is built at (liveConfig.refresh, via defaultShaper)",
	"sandboxMemoryMb":        "the default shape the next sandbox is built at (liveConfig.refresh, via defaultShaper)",
	"sandboxDiskGb":          "the default shape the next sandbox is built at (liveConfig.refresh, via defaultShaper)",
	"sandboxCpuOvercommit": "the budget the next sandbox is admitted against (liveConfig.refresh, via config.budget -- " +
		"the CI gate and dispatch both re-read it per job)",
	"sandboxMemoryOvercommit": "the budget the next sandbox is admitted against (liveConfig.refresh, via config.budget)",
	"sandboxDiskOvercommit":   "the budget the next sandbox is admitted against, and on Kubernetes the next pod's ephemeral-storage request (liveConfig.refresh, via config.budget and SetOvercommit)",
	"sandboxOvercommit":       "the auto overcommit controller's mode and bounds (runOvercommit reconfigures it each step, and config.budget reads its ratios)",
	"sessionSandboxIdleTtl":   "how long a session's sandbox and in-process agent wait stay warm between turns (RunCycle refreshes deps.Config.SessionSandboxIdleTTL from grain_config each cycle)",
	"sandboxMemoryReserveMb":  "the budget the next sandbox is admitted against (liveConfig.refresh, via config.budget)",
	"sandboxDiskFloorShare": "how much of each volume the next admission is held over (cmd/grain's diskWatch, which asks " +
		"liveConfig.diskFloor per reading rather than holding the one it was built with)",
	"sandboxDiskFloorMinMb": "how much of each volume the next admission is held over (cmd/grain's diskWatch, via liveConfig.diskFloor)",
	"networkAccess":         "the network the next sandbox is built with (liveConfig.refresh, via defaultShaper -- it rides on the same orchestrator.Shape)",
	"sandboxImage": "the image the next sandbox is created from (liveConfig.refresh, via pkg/sandboximage's Source " +
		"on granule and orchestrator.SandboxImageSetter on every other backend)",
	"sandboxImageMirror": "the mirror repository the stock guest sandbox image is pushed to and resolved from " +
		"(liveConfig.refresh, via applySandboxImage)",
	"packageCacheEnabled": "whether the next sandbox is pointed at this deployment's package cache " +
		"(orchestrator.Config.PackageCache, which asks cmd/grain's liveConfig per dispatch)",
	"packageCacheSizeGb": "the size bound the package cache is held to (pkgcache.Cache.SetMaxBytes via liveConfig.refresh)",
	"actionsCacheEnabled": "whether the next CI job is handed this deployment's Actions cache " +
		"(cmd/grain's actionsCache.grant, which asks liveConfig as each job's sandbox is built)",
	"actionsCacheSizeGb": "the size bound the Actions cache is held to (actionscache.Cache.SetMaxBytes via liveConfig.refresh)",
	"ciRunHistory":       "how many runs the next sweep keeps (ci.StoredRunStore reads liveConfig.ciRetention as each run concludes)",
	"ciStepOutputMb": "the ceiling the next sweep holds stored step output within " +
		"(ci.StoredRunStore reads liveConfig.ciRetention as each run concludes)",
	"ciStepOutputDays": "the age the next sweep drops stored step output past (ci.StoredRunStore reads liveConfig.ciRetention)",
	"ciRunOutputKb":    "how much output the next run stores (ci.StoredRunStore reads liveConfig.ciRetention as it writes a run down)",
	"ciCacheMb":        "the ceiling the next sweep of the job cache holds it within (ci.DirCache reads liveConfig.ciStorageBounds on every sweep)",
	"ciCacheEntries":   "how many entries the next sweep of the job cache leaves (ci.DirCache reads liveConfig.ciStorageBounds on every sweep)",
	"ciCacheDays":      "how old a cache entry may be before the next sweep drops it (ci.DirCache reads liveConfig.ciStorageBounds)",
	"ciArtifactMb":     "the ceiling the next sweep of the artifact store holds it within (ci.DirArtifacts reads liveConfig.ciStorageBounds on every sweep)",
	"ciArtifactRunMb":  "what one run's artifacts may come to (ci.DirArtifacts reads liveConfig.ciStorageBounds as each artifact is saved)",
	"ciArtifactFileMb": "the largest artifact the next job may keep -- the number its own sandbox is told (ci.DirArtifacts.MaxArtifactBytes)",
	"ciArtifactDays":   "how old a run's artifacts may be before the next sweep drops them (ci.DirArtifacts reads liveConfig.ciStorageBounds)",
	"ciJobsPerRun":     "how many sandboxes one CI run may hold at once (ci.Engine.ConcurrencyLimit asks cmd/grain for it when each run starts)",
	"ciAgeBoost": "the order the CI gate admits waiting jobs in (ci.SharedGate asks cmd/grain for it on every " +
		"admission attempt, so a job already queueing is ranked by it)",
	"ciReserveAfter": "when the CI gate starts holding capacity for a job the budget keeps refusing " +
		"(ci.SharedGate asks cmd/grain for it on every admission attempt)",
	"promptExtension":     "the deployment-wide text RunCycle refreshes out of grain_config every cycle (orchestrator.resolvePromptExtension)",
	"repairTemplateID":    "orchestrator resolves the repair template dynamically from the config row on each repair/triage filing",
	"defaultCapabilities": "pkg/ui, which reads grain_config per request (CreateTask's own defaults)",
	"newestFirst":         "pkg/ui, which reads grain_config per request",
	"showClosedByDefault": "pkg/ui, which reads grain_config per request",
	"approvedByDefault":   "pkg/ui, which reads grain_config per request",
	"autoMergeByDefault":  "pkg/ui, which reads grain_config per request",
	"environmentName":     "pkg/ui, which reads grain_config per request (GET /api/config's own copy)",
	"upgradeCheckMode": "orchestrator's upgrade-check reconciler, which reads grain_config every cycle " +
		"(reconcileUpgradeCheck's own upgradeCheckSettings)",
	"upgradeCheckPausesPipeline": "orchestrator's upgrade-check reconciler, which reads grain_config every cycle " +
		"(reconcileUpgradeCheck's own upgradeCheckSettings)",
	"autoUpgradeChannel": "orchestrator's auto-upgrade reconciler, which reads grain_config every cycle " +
		"(reconcileAutoUpgrade)",
	"autoUpgradeTime": "orchestrator's auto-upgrade reconciler, which reads grain_config every cycle " +
		"(reconcileAutoUpgrade)",
	"taskBranchTemplate": "model.Store.PutTask reads the current grain_config row when it freezes a new task's branch name",
	"deploymentId":       "model.Store.PutTask reads the current grain_config row when it freezes a new task's branch name",
	"externalAgentApi":   "pkg/ui, which reads grain_config per request (authenticateExternalAgent)",
	"settingsLocked":     "pkg/ui, which reads grain_config per request (settingsLocked)",
}

func TestEverySettingIsEitherAppliedLiveOrRestartOnly(t *testing.T) {
	t.Parallel()
	restartOnly := map[string]bool{}
	for _, s := range restartOnlySettings {
		restartOnly[s.Key] = true
	}

	settable := map[string]bool{}
	for _, field := range jsonNames(reflect.TypeOf(UpdateSettingsRequest{})) {
		settable[field] = true
		if restartOnly[field] && settingsAppliedLive[field] != "" {
			t.Errorf("%q is both restart-only and listed as applied live: it is one or the other, "+
				"and the pane annotates it on the strength of the first", field)
			continue
		}
		if restartOnly[field] || settingsAppliedLive[field] != "" {
			continue
		}
		t.Errorf("UpdateSettingsRequest has a %q field that is neither in restartOnlySettings nor in "+
			"settingsAppliedLive: if a running daemon cannot adopt it, the Settings pane reports it as "+
			"applied the moment it is saved and the deployment runs on a value nobody is told is not in "+
			"effect -- add it to restartOnlySettings, or to settingsAppliedLive naming what applies it",
			field)
	}

	// Both lists the other way round, so neither can drift into naming
	// settings that no longer exist: a key no config file and no pane can
	// set annotates nothing, and reads as an accounting it isn't.
	for field := range settingsAppliedLive {
		if !settable[field] {
			t.Errorf("settingsAppliedLive names %q, which is not an UpdateSettingsRequest field -- "+
				"nothing can change it, so nothing applies it live", field)
		}
	}
	for field := range restartOnly {
		if !settable[field] {
			t.Errorf("restartOnlySettings names %q, which is not an UpdateSettingsRequest field -- "+
				"nothing can change it, so no restart is ever owed for it", field)
		}
	}
}

// The keys are also how the frontend finds the input to annotate
// (SettingsOverlay.jsx's restartHint/restartChip look each one up against
// the field names its own form uses, which are Settings' JSON names). A
// key that names no Settings field annotates nothing at all, silently:
// the pane renders exactly as if the setting applied live.
func TestRestartOnlySettingsAreNamedAsTheSettingsFieldsTheyAnnotate(t *testing.T) {
	t.Parallel()
	reported := map[string]bool{}
	for _, field := range jsonNames(reflect.TypeOf(Settings{})) {
		reported[field] = true
	}
	for _, key := range restartRequiredKeys() {
		if !reported[key] {
			t.Errorf("restartOnlySettings names %q, which is not a Settings field: the pane looks its "+
				"restart annotation up by that name and finds no input to put it on", key)
		}
	}
}

// jsonNames is a struct's fields by the JSON name they are marshaled
// with -- the name a config file, an API caller and the frontend all
// spell the setting with, and so the only name these lists can agree on.
func jsonNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}
