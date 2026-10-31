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

// Reading POST /api/settings/plan (grain/task-241) for the Settings
// pane: what pressing Save would actually store, worked out before
// anybody presses it.
//
// The plan is the daemon's own answer -- the very request PUT
// /api/settings would apply, through the same validation and
// normalization, answered with the ui.Settings a save would leave
// behind, having written nothing. Turning that answer into the lines a
// pane shows is arithmetic over two objects, so it lives here where it
// can be tested without a render.
//
// It is deliberately not a comparison the form works out for itself
// between what was typed and what was loaded, for the same reason
// `grain sync -dry-run` does not (pkg/cli/sync.go's syncSettings):
// what a typed value *becomes* is the daemon's decision. "1m" is stored
// and read back as "60s", a browser URL pasted into a repo list becomes
// owner/name, a name is trimmed, an upgrade check mode is lower-cased,
// "gemini" becomes the framework it now means -- and a form comparing
// its own boxes against the last load would report every one of those
// as a change that never happens.

// SETTING_LABELS is what to call each setting in prose. The keys are
// ui.UpdateSettingsRequest's own JSON names -- the same ones the form's
// inputs are named after, and the same ones restartRequired and
// pendingRestart arrive keyed by -- and the wording is each field's own
// label on the pane, so a line in the preview names the box it came
// from rather than a spelling only the API uses.
//
// A key with no entry here falls back to the key itself (settingLabel),
// which is the right failure mode for a UI running against a newer
// daemon that has grown a setting this build has never heard of: the
// line still appears, under a name that is at least the one the API
// uses, rather than being silently left out of a preview whose whole
// job is completeness.
const SETTING_LABELS = {
  environmentName: "Environment name",
  settingsLocked: "Lock settings in UI (require config repo PRs)",
  pollInterval: "Poll interval",
  maxWorkers: "Max worker agents",
  maxMergesPerRepo: "Max merges per repo branch",
  maxMergers: "Max merge agents",
  maxCIJobs: "Max CI jobs",
  mergeBatchSize: "Merge batch size",
  mergeGatherWindow: "Merge gather window",
  newestFirst: "Work through the backlog newest-first",
  showClosedByDefault: "Show closed tasks by default",
  approvedByDefault: "Queue new tasks immediately by default",
  autoMergeByDefault: "Auto-merge new tasks by default",
  maxAutoSubmitDepth: "Max unreviewed follow-up depth",
  maxAutoSubmitFanOut: "Max unreviewed follow-up per task",
  agentFramework: "Agent framework",
  agentBackends: "Models",
  geminiModel: "Gemini model",
  geminiEffort: "Gemini reasoning effort",
  claudeModel: "Claude model",
  codexModel: "Codex model",
  maxAgentTurns: "Max agent turns",
  promptExtension: "Standing instructions for every run",
  taskBranchTemplate: "Branch name template",
  deploymentId: "Deployment ID",
  githubHost: "GitHub host",
  githubInsecureHttp: "Speak plain HTTP to GitHub host",
  gcpProject: "GCP project",
  gcpServiceAccountEmail: "GCP service account email",
  gcpServiceAccountCount: "GCP service account count",
  sandboxBackend: "Sandbox backend",
  sandboxCpus: "Sandbox vCPUs",
  sandboxMemoryMb: "Sandbox memory (MiB)",
  sandboxDiskGb: "Sandbox disk (GiB)",
  networkAccess: "Sandbox network",
  sessionSandboxIdleTtl: "Session sandbox idle TTL",
  sandboxOvercommit: "Overcommit mode",
  sandboxOvercommitCpuMin: "Auto CPU minimum",
  sandboxOvercommitCpuMax: "Auto CPU maximum",
  sandboxOvercommitMemoryMin: "Auto memory minimum",
  sandboxOvercommitMemoryMax: "Auto memory maximum",
  sandboxOvercommitCpuPressureTarget: "CPU pressure target (%)",
  sandboxOvercommitMemoryPressureTarget: "Memory pressure target (%)",
  sandboxCpuOvercommit: "CPU overcommit",
  sandboxMemoryOvercommit: "Memory overcommit",
  sandboxDiskOvercommit: "Disk overcommit",
  sandboxMemoryReserveMb: "Host memory reserve (MiB)",
  sandboxDiskFloorShare: "Disk floor (share of each volume)",
  sandboxDiskFloorMinMb: "Disk floor minimum (MiB)",
  sandboxImage: "Custom sandbox image (OCI reference)",
  k8sNamespace: "Kubernetes namespace",
  k8sSandboxImage: "Kubernetes sandbox pod image",
  k8sGranuleImage: "Kubernetes Granule VM pod image",
  k8sWorkspace: "Kubernetes workspace directory",
  k8sGitProxyHost: "Kubernetes git & model proxy host",
  k8sSandboxService: "Kubernetes sandbox Service",
  k8sNodeSelector: "Kubernetes node selector",
  k8sServiceAccount: "Kubernetes sandbox ServiceAccount",
  k8sPriorityClassInteractive: "Priority class for interactive sessions",
  k8sPriorityClassAgent: "Priority class for agent runs",
  k8sPriorityClassCi: "Priority class for CI jobs",
  k8sDeployment: "Kubernetes Deployment",
  k8sContainer: "Kubernetes container",
  k8sKubectl: "Kubectl binary",
  k8sPrivileged: "Run Kubernetes sandbox containers privileged",
  k8sNetworkPolicy: "Cluster CNI enforces NetworkPolicy for network: none runs",
  defaultCapabilities: "Default capabilities",
  // Keyed by the API's own name and labelled with the pane's own
  // wording, which for the first two is "Proxy cache" (grain/task-328):
  // the keys stay packageCache*, the words a reader sees do not.
  packageCacheEnabled: "Proxy cache",
  packageCacheSizeGb: "Proxy cache max size (GiB)",
  actionsCacheEnabled: "Actions cache",
  actionsCacheSizeGb: "Actions cache max size (GiB)",
  ciJobsPerRun: "CI jobs per run",
  ciAgeBoost: "CI queue age boost",
  ciReserveAfter: "CI capacity reserved after",
  ciRunHistory: "CI runs kept",
  ciStepOutputMb: "CI step output kept (MiB)",
  ciStepOutputDays: "CI step output age (days)",
  ciRunOutputKb: "CI output per run (KiB)",
  ciCacheMb: "CI job cache size (MiB)",
  ciCacheEntries: "CI job cache entries",
  ciCacheDays: "CI job cache age (days)",
  ciArtifactMb: "CI artifacts kept (MiB)",
  ciArtifactDays: "CI artifacts age (days)",
  ciArtifactRunMb: "CI artifacts per run (MiB)",
  ciArtifactFileMb: "Largest CI artifact (MiB)",
  upgradeCheckMode: "Upgrade check",
  upgradeCheckPausesPipeline: "Upgrade check pauses the pipeline",
  autoUpgradeChannel: "Automatic upgrade channel",
  autoUpgradeTime: "Automatic upgrade time of day",
};

// settingLabel is what to call one setting, by its JSON name.
export function settingLabel(field) {
  return SETTING_LABELS[field] || field;
}

// SETTING_MINS is the lower bound on each bounded numeric setting, matching
// both the input's own `min` in components/settings and the check in
// pkg/ui/settings.go. Keeping the table here lets useSettingsSection
// enforce the bound even when a caller hands over a synthetic event
// without the DOM attribute, and lets settingsPlan.test.js verify every
// bounded setting's input carries the matching `min`.
const SETTING_MINS = {
  maxWorkers: 1,
  maxMergesPerRepo: 1,
  maxMergers: 0,
  maxCIJobs: 0,
  mergeBatchSize: 1,
  maxAutoSubmitDepth: 1,
  maxAutoSubmitFanOut: 1,
  maxAgentTurns: 0,
  gcpServiceAccountCount: 1,
  sandboxCpus: 0,
  sandboxMemoryMb: 0,
  sandboxDiskGb: 0,
  sandboxOvercommitCpuMin: 0.25,
  sandboxOvercommitCpuMax: 0.25,
  sandboxOvercommitMemoryMin: 0.25,
  sandboxOvercommitMemoryMax: 0.25,
  sandboxOvercommitCpuPressureTarget: 0.5,
  sandboxOvercommitMemoryPressureTarget: 0.5,
  sandboxCpuOvercommit: 0,
  sandboxMemoryOvercommit: 0,
  sandboxDiskOvercommit: 0,
  sandboxMemoryReserveMb: 0,
  sandboxDiskFloorShare: 0,
  sandboxDiskFloorMinMb: 0,
  packageCacheSizeGb: 0,
  actionsCacheSizeGb: 0,
  ciJobsPerRun: 0,
  ciAgeBoost: 0,
  ciRunOutputKb: 0,
  ciCacheMb: 0,
  ciCacheEntries: 0,
  ciCacheDays: 0,
  ciArtifactMb: 0,
  ciArtifactDays: 0,
  ciArtifactRunMb: 0,
  ciArtifactFileMb: 0,
};

// settingMin is the lower bound for a numeric setting, or undefined when
// the setting is not bounded below (for example, -1 means unlimited on
// CI retention fields).
export function settingMin(field) {
  return SETTING_MINS[field];
}

// settingErrorMessage rewrites a server validation message that starts
// with a raw API key ("maxAutoSubmitDepth must be at least 1",
// "pollInterval: time: invalid duration …") so it names the box by its
// on-screen label instead ("Max unreviewed follow-up depth must be at
// least 1").
export function settingErrorMessage(message) {
  const text = String(message ?? "");
  const m = text.match(/^([A-Za-z][A-Za-z0-9]*)(?=$|[\s:])/);
  if (!m) return text;
  const key = m[1];
  if (!(key in SETTING_LABELS)) return text;
  return `${SETTING_LABELS[key]}${text.slice(key.length)}`;
}

// PLAN_FIELDS is the order a preview lists settings in, and it is only
// an order: nothing is left out for being absent from it (planChanges
// appends whatever else the request carried), so this going stale costs
// a line its place in the list rather than its existence.
const PLAN_FIELDS = Object.keys(SETTING_LABELS);

// formatSettingValue writes one value the way a reader wants it, and --
// since planChanges compares these strings rather than the values
// behind them -- decides what counts as the same value. A missing key
// and an empty string read alike on purpose: a settings response that
// predates a field and one reporting it unset are the same "not set",
// and neither is a change worth a line.
export function formatSettingValue(value) {
  if (value === undefined || value === null || value === "") return "not set";
  if (typeof value === "boolean") return value ? "on" : "off";
  if (Array.isArray(value))
    return value.length === 0 ? "none" : value.join(", ");
  if (typeof value === "object") {
    if ("mode" in value || "cpuMin" in value || "cpuMax" in value) {
      const mode = value.mode || "fixed";
      const cpuMin = Number(value.cpuMin ?? 1).toFixed(2);
      const cpuMax = Number(value.cpuMax ?? 6).toFixed(2);
      const memMin = Number(value.memoryMin ?? 1).toFixed(2);
      const memMax = Number(value.memoryMax ?? 2).toFixed(2);
      const cpuTarget = Number(value.cpuPressureTarget ?? 25).toFixed(1);
      const memTarget = Number(value.memoryPressureTarget ?? 5).toFixed(1);
      return `${mode} (CPU ${cpuMin}x..${cpuMax}x, memory ${memMin}x..${memMax}x, targets ${cpuTarget}%/${memTarget}%)`;
    }
    return JSON.stringify(value);
  }
  return String(value);
}

// planChanges is the preview itself: `before` is GET /api/settings (what
// is stored now), `after` is POST /api/settings/plan's answer (what a
// save would store), and `payload` is the request both of those are
// about -- the very body Save would PUT.
//
// One row per setting the request touched, plus any other named setting
// the plan moved. Each row carries:
//
//   field    the JSON name, for a stable React key
//   label    what to call it
//   was      the stored value, formatted
//   becomes  what the save would leave there, formatted
//   changed  whether those two differ at all
//   typed    what the request asked for, when the daemon would store
//            something else -- the normalization that is the whole
//            reason to ask before saving -- and null when what was sent
//            is what would be stored
//
// A row whose value does not move is kept rather than dropped, unlike
// the CLI's own diff: a form is asking "what does this do", and "you
// typed 1m, which is the 60s already stored" is an answer, where the
// same line in a workflow log would only be noise.
export function planChanges(before, after, payload) {
  const stored = before || {};
  const planned = after || {};
  const request = payload || {};

  // What `before` says is stored is not always what is stored: a value
  // no daemon can run on is reported as the one the daemon substituted
  // for it, with the row's own value carried beside it
  // (ui.Settings.Unrunnable, grain/task-369). "was" has to be the row --
  // otherwise a save that replaces a stored 0s with the 30s in the box
  // previews as "30s -> 30s, unchanged", which is the one thing this
  // preview exists to stop.
  //
  // Only settings the request actually carries reach this: for the rest,
  // the loop below compares `before` against the plan, and both report
  // the same substitution, so nothing is added on the strength of a
  // difference neither of them has.
  const unrunnable = new Map(
    (stored.unrunnable || []).map((u) => [u.key, u.stored]),
  );

  const rows = [];
  const seen = new Set();
  const add = (field) => {
    if (seen.has(field)) return;
    seen.add(field);
    const was = formatSettingValue(
      unrunnable.has(field) ? unrunnable.get(field) : stored[field],
    );
    const becomes = formatSettingValue(planned[field]);
    const asked = field in request ? formatSettingValue(request[field]) : null;
    rows.push({
      field,
      label: settingLabel(field),
      was,
      becomes,
      changed: was !== becomes,
      typed: asked !== null && asked !== becomes ? asked : null,
    });
  };

  for (const field of PLAN_FIELDS) {
    if (field in request) {
      add(field);
      continue;
    }
    // A setting the request never named but the plan moves anyway.
    // Nothing does that today; it is here so that one would surface as
    // a line rather than as a surprise after the save. Only for a field
    // the plan actually reports: a response that leaves one out has
    // said nothing about it, which is not the same as saying it would
    // be cleared.
    if (
      field in planned &&
      formatSettingValue(stored[field]) !== formatSettingValue(planned[field])
    )
      add(field);
  }
  // Anything else the request carried, in the order it carried it --
  // a setting this build has no label for still gets its line.
  for (const field of Object.keys(request)) add(field);

  return rows;
}
