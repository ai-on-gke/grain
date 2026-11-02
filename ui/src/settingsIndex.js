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

import { fuzzyScore } from "./fuzzy.js";
import { isConfigRepoSection } from "./settingsSections.js";

// settingsIndex is what the Settings filter searches
// (docs/design/settings-panel.md §3.3): one entry per field and per
// card. A field entry's id is the field's data-setting-id, which is its
// /api/settings key (or, for Models' default model, its /api/deployment
// one); a card entry ("card": true) names a card whose
// content is a panel of its own (a connection list, a memory editor),
// and is reached by its anchor. settingsIndex.test.js checks every
// data-setting-id a section renders has an entry here, and the other
// way round.
export const SETTINGS_INDEX = [
  {
    id: "environmentName",
    section: "general",
    anchor: "deployment",
    label: "Environment name",
    keywords: ["name"],
  },
  {
    id: "showClosedByDefault",
    section: "general",
    anchor: "deployment",
    label: "Show closed tasks by default",
    keywords: [],
  },
  {
    id: "settingsLocked",
    section: "general",
    anchor: "settings-lock",
    label: "Lock settings in UI (require config repo PRs)",
    keywords: ["lock", "read-only", "config repo"],
  },
  {
    id: "upgradeCheckMode",
    section: "general",
    anchor: "updates",
    label: "Check the upgrade",
    keywords: ["upgrade", "after upgrade"],
  },
  {
    id: "upgradeCheckPausesPipeline",
    section: "general",
    anchor: "updates",
    label: "Upgrade check pauses the pipeline",
    keywords: ["upgrade", "pause"],
  },
  {
    id: "autoUpgradeChannel",
    section: "general",
    anchor: "updates",
    label: "Automatic upgrade channel",
    keywords: ["upgrade", "auto upgrade", "daily", "channel"],
  },
  {
    id: "autoUpgradeTime",
    section: "general",
    anchor: "updates",
    label: "Automatic upgrade time of day",
    keywords: ["upgrade", "auto upgrade", "daily", "time"],
  },
  {
    id: "maxAgentTurns",
    section: "agents",
    anchor: "agent-runs",
    label: "Max agent turns",
    keywords: ["turns"],
  },
  {
    id: "promptExtension",
    section: "agents",
    anchor: "standing-instructions",
    label: "Standing instructions for every run",
    keywords: ["prompt extension", "house style"],
  },
  {
    id: "taskBranchTemplate",
    section: "agents",
    anchor: "task-branches",
    label: "Branch name template",
    keywords: [],
  },
  {
    id: "deploymentId",
    section: "agents",
    anchor: "task-branches",
    label: "Deployment ID",
    keywords: ["gcp", "service account"],
  },
  {
    id: "pollInterval",
    section: "engine",
    anchor: "polling",
    label: "Poll interval",
    keywords: ["poll"],
  },
  {
    id: "maxWorkers",
    section: "engine",
    anchor: "polling",
    label: "Max worker agents",
    keywords: ["concurrency", "workers"],
  },
  {
    id: "maxMergesPerRepo",
    section: "engine",
    anchor: "merge-queue",
    label: "Max merges per repo branch",
    keywords: [],
  },
  {
    id: "ciJobsPerRun",
    section: "ci",
    anchor: "ci-queue",
    label: "CI jobs per run",
    keywords: [],
  },
  {
    id: "ciAgeBoost",
    section: "ci",
    anchor: "ci-queue",
    label: "CI queue age boost",
    keywords: [],
  },
  {
    id: "ciReserveAfter",
    section: "ci",
    anchor: "ci-queue",
    label: "CI capacity reserved after",
    keywords: [],
  },
  {
    id: "ciRunHistory",
    section: "ci",
    anchor: "ci-history",
    label: "CI runs kept",
    keywords: [],
  },
  {
    id: "ciStepOutputMb",
    section: "ci",
    anchor: "ci-history",
    label: "CI step output kept (MiB)",
    keywords: ["disk"],
  },
  {
    id: "ciStepOutputDays",
    section: "ci",
    anchor: "ci-history",
    label: "CI step output age (days)",
    keywords: [],
  },
  {
    id: "ciRunOutputKb",
    section: "ci",
    anchor: "ci-history",
    label: "CI output per run (KiB)",
    keywords: [],
  },
  {
    id: "ciCacheMb",
    section: "ci",
    anchor: "ci-job-cache",
    label: "CI job cache size (MiB)",
    keywords: ["disk"],
  },
  {
    id: "ciCacheEntries",
    section: "ci",
    anchor: "ci-job-cache",
    label: "CI job cache entries",
    keywords: [],
  },
  {
    id: "ciCacheDays",
    section: "ci",
    anchor: "ci-job-cache",
    label: "CI job cache age (days)",
    keywords: [],
  },
  {
    id: "ciArtifactMb",
    section: "ci",
    anchor: "ci-artifacts",
    label: "CI artifacts kept (MiB)",
    keywords: ["disk"],
  },
  {
    id: "ciArtifactDays",
    section: "ci",
    anchor: "ci-artifacts",
    label: "CI artifacts age (days)",
    keywords: [],
  },
  {
    id: "ciArtifactRunMb",
    section: "ci",
    anchor: "ci-artifacts",
    label: "CI artifacts per run (MiB)",
    keywords: [],
  },
  {
    id: "ciArtifactFileMb",
    section: "ci",
    anchor: "ci-artifacts",
    label: "Largest CI artifact (MiB)",
    keywords: [],
  },
  {
    id: "sandboxBackend",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Sandbox backend",
    keywords: [],
  },
  {
    id: "sandboxCpus",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Sandbox vCPUs",
    keywords: [],
  },
  {
    id: "sandboxMemoryMb",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Sandbox memory (MiB)",
    keywords: [],
  },
  {
    id: "sandboxDiskGb",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Sandbox disk (GiB)",
    keywords: ["disk"],
  },
  {
    id: "networkAccess",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Sandbox network",
    keywords: ["internet", "egress"],
  },
  {
    id: "sessionSandboxIdleTtl",
    section: "sandboxes",
    anchor: "backend-and-size",
    label: "Session sandbox idle TTL",
    keywords: ["chat", "director", "session", "warm", "idle", "ttl"],
  },
  {
    id: "sandboxOvercommit",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Overcommit mode",
    keywords: ["auto", "fixed", "psi", "pressure"],
  },
  {
    id: "sandboxCpuOvercommit",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "CPU overcommit",
    keywords: [],
  },
  {
    id: "sandboxMemoryOvercommit",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Memory overcommit",
    keywords: [],
  },
  {
    id: "sandboxDiskOvercommit",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Disk overcommit",
    keywords: ["ephemeral-storage", "storage"],
  },
  {
    id: "sandboxOvercommitCpuMin",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Auto CPU minimum",
    keywords: ["overcommit", "auto", "bound"],
  },
  {
    id: "sandboxOvercommitCpuMax",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Auto CPU maximum",
    keywords: ["overcommit", "auto", "bound"],
  },
  {
    id: "sandboxOvercommitMemoryMin",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Auto memory minimum",
    keywords: ["overcommit", "auto", "bound"],
  },
  {
    id: "sandboxOvercommitMemoryMax",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Auto memory maximum",
    keywords: ["overcommit", "auto", "bound"],
  },
  {
    id: "sandboxOvercommitCpuPressureTarget",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "CPU pressure target (%)",
    keywords: ["overcommit", "auto", "psi", "pressure"],
  },
  {
    id: "sandboxOvercommitMemoryPressureTarget",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Memory pressure target (%)",
    keywords: ["overcommit", "auto", "psi", "pressure"],
  },
  {
    id: "sandboxMemoryReserveMb",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Host memory reserve (MiB)",
    keywords: [],
  },
  {
    id: "sandboxDiskFloorShare",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Disk floor (share of each volume)",
    keywords: ["disk"],
  },
  {
    id: "sandboxDiskFloorMinMb",
    section: "sandboxes",
    anchor: "sandbox-budget",
    label: "Disk floor minimum (MiB)",
    keywords: ["disk"],
  },
  {
    id: "sandboxImage",
    section: "sandboxes",
    anchor: "custom-sandbox-image",
    label: "Custom sandbox image (OCI reference)",
    keywords: [
      "image",
      "oci",
      "dockerfile",
      "guest-base",
      "k8s-sandbox-base",
      "contract",
      "install packages",
    ],
  },
  {
    id: "k8sNamespace",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes namespace",
    keywords: ["k8s", "namespace", "pod"],
  },
  {
    id: "k8sSandboxImage",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes sandbox pod image",
    keywords: ["k8s", "image", "pod"],
  },
  {
    id: "k8sGranuleImage",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes Granule VM pod image",
    keywords: ["k8s", "granule", "image", "vm"],
  },
  {
    id: "k8sWorkspace",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes workspace directory",
    keywords: ["k8s", "workspace", "directory"],
  },
  {
    id: "k8sGitProxyHost",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes git & model proxy host",
    keywords: ["k8s", "proxy", "host", "pod ip"],
  },
  {
    id: "k8sSandboxService",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes sandbox Service",
    keywords: ["k8s", "service", "clusterip"],
  },
  {
    id: "k8sNodeSelector",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes node selector",
    keywords: ["k8s", "node", "selector", "pool"],
  },
  {
    id: "k8sServiceAccount",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes sandbox ServiceAccount",
    keywords: ["k8s", "serviceaccount", "rbac"],
  },
  {
    id: "k8sPriorityClassInteractive",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Priority class for interactive sessions",
    keywords: ["k8s", "priority", "priorityclass", "preemption", "interactive"],
  },
  {
    id: "k8sPriorityClassAgent",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Priority class for agent runs",
    keywords: ["k8s", "priority", "priorityclass", "preemption", "agent"],
  },
  {
    id: "k8sPriorityClassCi",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Priority class for CI jobs",
    keywords: ["k8s", "priority", "priorityclass", "preemption", "ci"],
  },
  {
    id: "k8sDeployment",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes Deployment",
    keywords: ["k8s", "deployment", "upgrade"],
  },
  {
    id: "k8sContainer",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubernetes container",
    keywords: ["k8s", "container", "upgrade"],
  },
  {
    id: "k8sKubectl",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Kubectl binary",
    keywords: ["k8s", "kubectl"],
  },
  {
    id: "k8sPrivileged",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Run Kubernetes sandbox containers privileged",
    keywords: ["k8s", "privileged", "root"],
  },
  {
    id: "k8sNetworkPolicy",
    section: "sandboxes",
    anchor: "kubernetes",
    label: "Cluster CNI enforces NetworkPolicy for network: none runs",
    keywords: ["k8s", "networkpolicy", "cni"],
  },
  {
    id: "packageCacheEnabled",
    section: "caches",
    anchor: "proxy-cache",
    label: "Proxy cache",
    keywords: ["package cache"],
  },
  {
    id: "packageCacheSizeGb",
    section: "caches",
    anchor: "proxy-cache",
    label: "Proxy cache max size (GiB)",
    keywords: [],
  },
  {
    id: "actionsCacheEnabled",
    section: "caches",
    anchor: "actions-cache",
    label: "Actions cache",
    keywords: [],
  },
  {
    id: "actionsCacheSizeGb",
    section: "caches",
    anchor: "actions-cache",
    label: "Actions cache max size (GiB)",
    keywords: [],
  },
  {
    id: "githubHost",
    section: "credentials",
    anchor: "credentials",
    label: "GitHub host",
    keywords: [],
  },
  {
    id: "githubInsecureHttp",
    section: "credentials",
    anchor: "credentials",
    label: "Speak plain HTTP to GitHub host",
    keywords: [],
  },
  {
    id: "gcpProject",
    section: "credentials",
    anchor: "credentials",
    label: "GCP project",
    keywords: ["gcp", "gemini"],
  },
  {
    id: "gcpServiceAccountEmail",
    section: "credentials",
    anchor: "credentials",
    label: "GCP service account email",
    keywords: [],
  },
  {
    id: "gcpServiceAccountCount",
    section: "credentials",
    anchor: "credentials",
    label: "GCP service account count",
    keywords: [],
  },
  {
    id: "card:general:about",
    section: "general",
    anchor: "about",
    label: "Version",
    keywords: ["build", "version"],
    card: true,
  },
  {
    id: "card:access:this-browser",
    section: "access",
    anchor: "this-browser",
    label: "Sign out",
    keywords: ["log out", "IAP", "access code"],
    card: true,
  },
  {
    id: "card:config-repo:config-repo",
    section: "config-repo",
    anchor: "config-repo",
    label: "Config repo",
    keywords: ["state repo", "secrets repo", "sync"],
    card: true,
  },
  {
    id: "card:upgrade:upgrade",
    section: "upgrade",
    anchor: "upgrade",
    label: "Upgrade grain",
    keywords: [
      "upgrade",
      "update",
      "rollback",
      "roll back",
      "channel",
      "branch",
      "image",
      "release notes",
      "version",
      "schedule upgrade",
      "after tasks finish",
      "database",
      "postgres",
    ],
    card: true,
  },
  {
    id: "card:models:models",
    section: "models",
    anchor: "models",
    label: "Provider connections",
    keywords: ["model", "provider", "connection", "pause"],
    card: true,
  },
  {
    id: "defaultModel",
    section: "models",
    anchor: "model-defaults",
    label: "Default model",
    keywords: ["model", "effort", "reasoning effort"],
  },
  {
    id: "card:memory:memory",
    section: "memory",
    anchor: "memory",
    label: "Memory",
    keywords: ["memory", "notes", "documents"],
    card: true,
  },
  {
    id: "approvedByDefault",
    section: "tasks",
    anchor: "task-defaults",
    label: "Queue new tasks immediately by default",
    keywords: ["approved by default", "proposal"],
  },
  {
    id: "autoMergeByDefault",
    section: "tasks",
    anchor: "task-defaults",
    label: "Auto-merge new tasks by default",
    keywords: ["auto-merge"],
  },
  {
    id: "defaultCapabilities",
    section: "tasks",
    anchor: "task-defaults",
    label: "Default capabilities",
    keywords: ["capabilities"],
  },
  {
    id: "newestFirst",
    section: "tasks",
    anchor: "backlog",
    label: "Work through the backlog newest-first",
    keywords: ["backlog", "order"],
  },
  {
    id: "maxAutoSubmitDepth",
    section: "tasks",
    anchor: "backlog",
    label: "Max unreviewed follow-up depth",
    keywords: ["follow-up"],
  },
  {
    id: "maxAutoSubmitFanOut",
    section: "tasks",
    anchor: "backlog",
    label: "Max unreviewed follow-up per task",
    keywords: ["follow-up"],
  },
  {
    id: "card:tasks:task-fields",
    section: "tasks",
    anchor: "task-fields",
    label: "Task fields",
    keywords: ["custom fields"],
    card: true,
  },
  {
    id: "card:repositories:repositories",
    section: "repositories",
    anchor: "repositories",
    label: "Repositories",
    keywords: [
      "repo",
      "base branch",
      "declarations",
      "base repair",
      "human gates",
      "base_repair_bypasses_human_gates",
    ],
    card: true,
  },
  {
    id: "card:git:git",
    section: "git",
    anchor: "git",
    label: "Git connections",
    keywords: ["git identity", "token", "github app"],
    card: true,
  },
  {
    id: "card:credentials:credentials",
    section: "credentials",
    anchor: "credentials",
    label: "Legacy capabilities",
    keywords: [
      "credentials",
      "legacy capabilities",
      "gcp-key",
      "gemini-key",
      "secrets",
      "github token",
    ],
    card: true,
  },
  {
    id: "card:capabilities:capability-servers",
    section: "capabilities",
    anchor: "capability-servers",
    label: "Capability servers",
    keywords: ["mcpServers", "mcp", "capability server", "serverUrl"],
    card: true,
  },
  {
    id: "card:capabilities:capability-policy",
    section: "capabilities",
    anchor: "capability-policy",
    label: "Capability policy",
    keywords: ["capability_policy", "allow", "deny", "untrusted"],
    card: true,
  },
  {
    id: "card:capabilities:api-scope-sets",
    section: "capabilities",
    anchor: "api-scope-sets",
    label: "API scope sets",
    keywords: ["api", "scope set", "preset", "scopes", "requires person"],
    card: true,
  },
  {
    id: "card:executors:executors",
    section: "executors",
    anchor: "executors",
    label: "Executor join tokens",
    keywords: ["remote executor", "join token"],
    card: true,
  },
  {
    id: "card:preferences:appearance",
    section: "preferences",
    anchor: "appearance",
    label: "Appearance",
    keywords: ["theme", "dark", "light"],
    card: true,
  },
  {
    id: "card:preferences:time-zone",
    section: "preferences",
    anchor: "time-zone",
    label: "Time zone",
    keywords: ["timezone"],
    card: true,
  },
  {
    id: "card:preferences:timeline",
    section: "preferences",
    anchor: "timeline",
    label: "Task timeline order",
    keywords: ["timeline", "order"],
    card: true,
  },
  {
    id: "card:preferences:pinned",
    section: "preferences",
    anchor: "pinned",
    label: "Sidebar",
    keywords: ["pins", "pinned", "customize", "sections", "hide", "navigation"],
    card: true,
  },
];

const SETTING_BY_ID = new Map(
  SETTINGS_INDEX.filter((e) => !e.card).map((e) => [e.id, e]),
);

// settingLocation is the Settings section and card anchor that hold a
// setting key (the keys in GET /api/settings's pendingRestart and
// unrunnable), or null for a key no section draws. target is what a link
// to the setting scrolls to: the field itself (its data-setting-id), as
// the filter's own pick does, since a card's top can be a screen or more
// above a field near its foot (Legacy capabilities' GitHub host).
export function settingLocation(id) {
  const entry = SETTING_BY_ID.get(id);
  if (!entry) return null;
  return {
    section: entry.section,
    anchor: entry.anchor,
    target: entry.id,
    href: `/settings/${entry.section}#${entry.id}`,
  };
}

// matchSettings is the filter's answer for a query: the entries whose
// label, then keywords, fuzzy-match it, best first. Values are never
// searched (§3.3's decision).
export function matchSettings(query) {
  const q = String(query || "").trim();
  if (!q) return [];
  const scored = [];
  for (const entry of SETTINGS_INDEX) {
    const byLabel = fuzzyScore(q, entry.label);
    const byKeyword = Math.max(
      -1,
      ...(entry.keywords || []).map((k) => fuzzyScore(q, k)),
    );
    if (byLabel < 0 && byKeyword < 0) continue;
    // A label match always ranks above a keyword one.
    const score = byLabel >= 0 ? 2000 + byLabel : byKeyword;
    // fuzzyScore puts a plain substring at 900 and over.
    const exact = byLabel >= 900 || byKeyword >= 900;
    scored.push({ entry, score, exact });
  }
  // Letters scattered through a long label are noise in a list this
  // short, so a query that matches anything outright keeps only those.
  const kept = scored.some((x) => x.exact)
    ? scored.filter((x) => x.exact)
    : scored;
  // A field of a config-repo-only section is still found -- its value is
  // shown -- but says it is read-only here (configRepo).
  return kept
    .sort((a, b) => b.score - a.score)
    .map((x) =>
      isConfigRepoSection(x.entry.section)
        ? { ...x.entry, configRepo: true }
        : x.entry,
    );
}
