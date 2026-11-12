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

export type SettingsSectionId =
  | "models"
  | "sandboxes"
  | "hostds"
  | "repositories"
  | "git"
  | "ci"
  | "config-repo"
  | "tasks"
  | "agents"
  | "capabilities"
  | "memory"
  | "access"
  | "audit"
  | "general"
  | "preferences"
  | "cli"
  | "caches"
  | "engine"
  | "registry"
  | "upgrade";

export type SettingsGroupId =
  "compute" | "code" | "work" | "security" | "instance";

export interface SettingsSectionDef {
  id: SettingsSectionId;
  label: string;
  subtitle: string;
  keywords: readonly string[];
}

export interface SettingsGroupDef {
  id: SettingsGroupId;
  label: string;
  iconPath: string;
  sections: readonly SettingsSectionDef[];
}

export const DEFAULT_SETTINGS_SECTION: SettingsSectionId = "models";

export const SETTINGS_GROUPS: readonly SettingsGroupDef[] = [
  {
    id: "compute",
    label: "Models & Compute",
    iconPath:
      "M9 3v2H7a2 2 0 0 0-2 2v2H3v2h2v2H3v2h2v2a2 2 0 0 0 2 2h2v2h2v-2h2v2h2v-2h2a2 2 0 0 0 2-2v-2h2v-2h-2v-2h2V9h-2V7a2 2 0 0 0-2-2h-2V3h-2v2h-2V3H9zm-2 4h10v10H7V7zm2 2v6h6V9H9z",
    sections: [
      {
        id: "models",
        label: "Models",
        subtitle:
          "Model connections, framework credentials, discovered models, default selection, and token budgets.",
        keywords: [
          "model",
          "models",
          "gemini",
          "claude",
          "codex",
          "api key",
          "backend",
          "vertex",
          "anthropic",
          "openai",
          "effort",
          "rotation",
          "budget",
        ],
      },
      {
        id: "sandboxes",
        label: "Sandboxes",
        subtitle:
          "Execution backend, vCPU/memory/disk sizing, overcommit governors, and Kubernetes pod targets.",
        keywords: [
          "sandbox",
          "granule",
          "microvm",
          "docker",
          "kubernetes",
          "k8s",
          "vcpu",
          "memory",
          "disk",
          "overcommit",
          "network",
          "persistent",
        ],
      },
      {
        id: "hostds",
        label: "Hosts",
        subtitle:
          "Registered workstation hostds, grain-agentd pairing, enrollment codes, and pending join requests.",
        keywords: [
          "host",
          "hosts",
          "hostd",
          "agentd",
          "invite",
          "join",
          "pairing",
          "enrollment",
          "pending",
          "workstation",
          "remote",
        ],
      },
    ],
  },
  {
    id: "code",
    label: "Repositories & Code",
    iconPath:
      "M7 5a2 2 0 1 1-2 2 2 2 0 0 1 2-2zm0-2a4 4 0 0 0-1 7.87v2.26A4 4 0 1 0 9 17v-1h5a4 4 0 0 0 4-4V9.87a4 4 0 1 0-2 0V12a2 2 0 0 1-2 2H8V10.87A4 4 0 0 0 7 3zm10 2a2 2 0 1 1-2 2 2 2 0 0 1 2-2zM7 15a2 2 0 1 1-2 2 2 2 0 0 1 2-2z",
    sections: [
      {
        id: "repositories",
        label: "Repositories",
        subtitle:
          "Target repositories, dry-run removal checks, base-repair gate policy, and per-repository settings.",
        keywords: [
          "repo",
          "repos",
          "repository",
          "target",
          "release",
          "candidate",
          "branch",
          "declarations",
          "stranded",
        ],
      },
      {
        id: "git",
        label: "Git",
        subtitle:
          "Git host connections, SSH/token/GitHub App credentials, commit author identity, and connection checks.",
        keywords: [
          "git",
          "github",
          "ssh",
          "token",
          "connection",
          "author",
          "signing",
          "ambient",
          "clone",
        ],
      },
      {
        id: "ci",
        label: "CI",
        subtitle:
          "Job concurrency, queue priority boost, log/artifact retention limits, and shared CI job cache.",
        keywords: [
          "ci",
          "checks",
          "workflows",
          "cache",
          "artifacts",
          "retention",
          "faults",
          "step output",
        ],
      },
      {
        id: "config-repo",
        label: "Config repo",
        subtitle:
          "StateRepo synchronization, config pull requests, remote verification, KMS secret key wrapping, and file viewer.",
        keywords: [
          "config",
          "state-repo",
          "staterepo",
          "sync",
          "pull request",
          "kms",
          "secrets-key",
          "remote",
          "yaml",
        ],
      },
    ],
  },
  {
    id: "work",
    label: "Work & Agents",
    iconPath:
      "M19 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2zm-8 14-4-4 1.41-1.41L11 14.17l6.59-6.59L19 9l-8 8z",
    sections: [
      {
        id: "tasks",
        label: "Tasks",
        subtitle:
          "Default proposal approval, auto-merge depth and fan-out, branch naming templates, and backlog defaults.",
        keywords: [
          "tasks",
          "approval",
          "auto-merge",
          "priority",
          "branch template",
          "merge queue",
          "gates",
          "custom fields",
        ],
      },
      {
        id: "agents",
        label: "Agents",
        subtitle:
          "Standing console sessions, agent turn budgets, default capabilities, and session restarts.",
        keywords: [
          "agents",
          "console",
          "sessions",
          "director",
          "turns",
          "timeout",
          "standing",
          "skills",
        ],
      },
      {
        id: "capabilities",
        label: "Capabilities",
        subtitle:
          "MCP capability servers, secret bindings, capability grant policy rules, API scope sets, and secrets.",
        keywords: [
          "capabilities",
          "mcp",
          "servers",
          "policy",
          "scope sets",
          "declarations",
          "secrets",
          "credentials",
        ],
      },
      {
        id: "memory",
        label: "Memory",
        subtitle:
          "Deployment standing instructions and persistent markdown memory documents shared with every agent.",
        keywords: [
          "memory",
          "standing instructions",
          "prompt extension",
          "documents",
          "markdown",
        ],
      },
    ],
  },
  {
    id: "security",
    label: "Access & Security",
    iconPath:
      "M12 1 3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm0 10.99h7c-.53 4.12-3.28 7.79-7 8.94V12H5V6.3l7-3.11v8.8z",
    sections: [
      {
        id: "access",
        label: "Access",
        subtitle:
          "RBAC allowlist principals, external agent gma_ tokens, and GCP service account credentials.",
        keywords: [
          "access",
          "rbac",
          "roles",
          "principals",
          "allowlist",
          "external agents",
          "gma_",
          "tokens",
          "gcp keys",
        ],
      },
      {
        id: "audit",
        label: "Audit",
        subtitle:
          "Config change audit trail, secret operations, and principal access history with actor and action filters.",
        keywords: [
          "audit",
          "logs",
          "history",
          "actor",
          "secret audit",
          "config audit",
        ],
      },
    ],
  },
  {
    id: "instance",
    label: "Instance & Preferences",
    iconPath:
      "M3 17v2h6v-2H3zM3 5v2h10V5H3zm10 16v-2h8v-2h-8v-2h-2v6h2zM7 9v2H3v2h4v2h2V9H7zm14 4v-2H11v2h10zm-6-4h2V7h4V5h-4V3h-2v6z",
    sections: [
      {
        id: "general",
        label: "General",
        subtitle:
          "Environment display name, settings UI lock, deployment ID, and live operator broadcast banner.",
        keywords: [
          "general",
          "environment",
          "name",
          "broadcast",
          "banner",
          "lock",
          "timezone",
        ],
      },
      {
        id: "preferences",
        label: "Preferences",
        subtitle:
          "Per-browser theme polarity, single-key shortcuts, default UI preference, and navigation rail order.",
        keywords: [
          "preferences",
          "theme",
          "dark",
          "light",
          "shortcuts",
          "navigation",
          "rail",
          "browser",
        ],
      },
      {
        id: "cli",
        label: "CLI",
        subtitle:
          "Workstation grain CLI installation, sign-in commands, and CLI task execution location.",
        keywords: [
          "cli",
          "install",
          "login",
          "command line",
          "terminal",
          "token",
        ],
      },
      {
        id: "caches",
        label: "Caches",
        subtitle:
          "Package proxy cache and GitHub Actions cache sizing, live usage, and targeted cache flushes.",
        keywords: [
          "caches",
          "proxy cache",
          "package cache",
          "actions cache",
          "flush",
        ],
      },
      {
        id: "engine",
        label: "Engine",
        subtitle:
          "Worker and merge agent concurrency, poll interval, and dispatch queue ordering.",
        keywords: [
          "engine",
          "workers",
          "mergers",
          "poll interval",
          "concurrency",
          "dispatch",
          "newest-first",
        ],
      },
      {
        id: "registry",
        label: "Registry & Hub",
        subtitle:
          "Grain Hub directory registration, live heartbeat telemetry, advertised repo scopes, and credentials.",
        keywords: [
          "registry",
          "hub",
          "heartbeat",
          "scopes",
          "catalog",
          "directory",
          "connected",
        ],
      },
      {
        id: "upgrade",
        label: "Upgrade",
        subtitle:
          "Running daemon version, upgrade targets, release notes, scheduled upgrades, and active upgrade control.",
        keywords: [
          "upgrade",
          "version",
          "release notes",
          "channel",
          "rollback",
          "database",
        ],
      },
    ],
  },
];

export const ALL_SETTINGS_SECTIONS: readonly SettingsSectionDef[] =
  SETTINGS_GROUPS.flatMap((g) => g.sections);

const SECTION_ALIASES: Readonly<Record<string, SettingsSectionId>> = {
  "capability-servers": "capabilities",
  credentials: "capabilities",
  "legacy-capabilities": "capabilities",
  secrets: "capabilities",
  integrations: "capabilities",
  executors: "hostds",
  hosts: "hostds",
  host: "hostds",
  state: "config-repo",
  hub: "registry",
  workflow: "tasks",
  accounts: "access",
  sandbox: "sandboxes",
  admin: "general",
};

export function resolveSettingsSection(
  raw: string | null | undefined,
): SettingsSectionId | null {
  const id = String(raw ?? "")
    .trim()
    .toLowerCase();
  if (!id) return null;
  const direct = ALL_SETTINGS_SECTIONS.find((s) => s.id === id);
  if (direct) return direct.id;
  return SECTION_ALIASES[id] ?? null;
}

export function sectionMeta(id: SettingsSectionId): SettingsSectionDef {
  return (
    ALL_SETTINGS_SECTIONS.find((s) => s.id === id) ?? ALL_SETTINGS_SECTIONS[0]
  );
}

export const CONFIG_REPO_SECTION_PATHS: Readonly<
  Partial<Record<SettingsSectionId, readonly string[]>>
> = {
  general: ["tables/grain_config.json"],
  git: ["tables/grain_config.json", "tables/repo_config.json"],
  repositories: ["tables/repo_config.json", "tables/grain_config.json"],
  tasks: [
    "tables/grain_config.json",
    "tables/custom_field.json",
    "settings/capabilities.yml",
  ],
  agents: ["tables/grain_config.json"],
  capabilities: [
    "tables/grain_config.json",
    "tables/api_scope_set.json",
    "settings/capabilities.yml",
  ],
  ci: ["tables/grain_config.json", "tables/repo_config.json"],
  memory: ["memory/deployment.md"],
  caches: ["tables/grain_config.json"],
};

export function isConfigRepoSection(id: SettingsSectionId): boolean {
  return id in CONFIG_REPO_SECTION_PATHS;
}

export function filterSettingsGroups(query: string): SettingsGroupDef[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...SETTINGS_GROUPS];
  const terms = q.split(/\s+/).filter(Boolean);
  const out: SettingsGroupDef[] = [];
  for (const group of SETTINGS_GROUPS) {
    const matched = group.sections.filter((sec) => {
      const hay = [
        sec.id,
        sec.label,
        sec.subtitle,
        group.label,
        ...sec.keywords,
      ]
        .join(" ")
        .toLowerCase();
      return terms.every((t) => hay.includes(t));
    });
    if (matched.length > 0) {
      out.push({ ...group, sections: matched });
    }
  }
  return out;
}

export const SETTING_LABELS: Readonly<Record<string, string>> = {
  environmentName: "Environment name",
  settingsLocked: "Lock settings in UI (require config repo PRs)",
  pollInterval: "Poll interval",
  maxWorkers: "Max worker agents",
  maxMergesPerRepo: "Max merges per repo branch",
  newestFirst: "Work through backlog newest-first",
  showClosedByDefault: "Show closed tasks by default",
  approvedByDefault: "Queue new tasks immediately by default",
  autoMergeByDefault: "Auto-merge new tasks by default",
  maxAutoSubmitDepth: "Max unreviewed follow-up depth",
  maxAutoSubmitFanOut: "Max unreviewed follow-up per task",
  defaultModel: "Default model",
  modelSettings: "Model reasoning settings",
  agentBackends: "Model connections",
  maxAgentTurns: "Max agent turns",
  promptExtension: "Standing instructions for every run",
  taskBranchTemplate: "Branch name template",
  deploymentId: "Deployment ID",
  externalAgentApi: "Accept external agents",
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
  sandboxCpuOvercommit: "CPU overcommit",
  sandboxMemoryOvercommit: "Memory overcommit",
  sandboxDiskOvercommit: "Disk overcommit",
  sandboxMemoryReserveMb: "Host memory reserve (MiB)",
  sandboxImage: "Custom sandbox image",
  k8sNamespace: "Kubernetes namespace",
  k8sSandboxImage: "Kubernetes sandbox pod image",
  k8sGranuleImage: "Kubernetes Granule VM pod image",
  k8sNodeSelector: "Kubernetes node selector",
  k8sServiceAccount: "Kubernetes sandbox ServiceAccount",
  k8sPrivileged: "Run Kubernetes sandbox containers privileged",
  k8sNetworkPolicy: "Cluster CNI enforces NetworkPolicy",
  defaultCapabilities: "Default capabilities",
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
  upgradeCheckPausesPipeline: "Upgrade check pauses pipeline",
  autoUpgradeChannel: "Automatic upgrade channel",
  autoUpgradeTime: "Automatic upgrade time of day",
  autoUpgradeOn: "Automatic upgrades",
  autoUpgradeMinIntervalHours: "Min interval between upgrades (hours)",
  hub: "Hub",
  registryUrl: "Hub URL",
  registryEntry: "Entry name",
  registryAuthMode: "Credential source",
  registryDisplayName: "Display name",
  registryPurpose: "Purpose",
  registryListed: "Listed in hub directory",
};

export function settingLabel(field: string): string {
  return SETTING_LABELS[field] ?? field;
}

export function formatSettingValue(value: unknown): string {
  if (value === undefined || value === null || value === "") return "not set";
  if (typeof value === "boolean") return value ? "on" : "off";
  if (Array.isArray(value)) {
    return value.length === 0 ? "none" : value.map(String).join(", ");
  }
  if (typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}

export interface SettingsPlanRow {
  field: string;
  label: string;
  was: string;
  becomes: string;
  changed: boolean;
  typed: string | null;
}

export function computeSettingsPlan(
  before: Record<string, unknown> | null | undefined,
  after: Record<string, unknown> | null | undefined,
  payload: Record<string, unknown> | null | undefined,
): SettingsPlanRow[] {
  const stored = before ?? {};
  const planned = after ?? {};
  const req = payload ?? {};
  const unrunnableArr = Array.isArray(stored["unrunnable"])
    ? (stored["unrunnable"] as { key?: string; stored?: unknown }[])
    : [];
  const unrunnable = new Map<string, unknown>();
  for (const item of unrunnableArr) {
    if (item?.key) unrunnable.set(item.key, item.stored);
  }

  const rows: SettingsPlanRow[] = [];
  const seen = new Set<string>();
  const add = (field: string): void => {
    if (seen.has(field)) return;
    seen.add(field);
    const was = formatSettingValue(
      unrunnable.has(field) ? unrunnable.get(field) : stored[field],
    );
    const becomes = formatSettingValue(planned[field]);
    const asked = field in req ? formatSettingValue(req[field]) : null;
    rows.push({
      field,
      label: settingLabel(field),
      was,
      becomes,
      changed: was !== becomes,
      typed: asked !== null && asked !== becomes ? asked : null,
    });
  };

  for (const field of Object.keys(SETTING_LABELS)) {
    if (field in req) {
      add(field);
      continue;
    }
    if (
      field in planned &&
      formatSettingValue(stored[field]) !== formatSettingValue(planned[field])
    ) {
      add(field);
    }
  }
  for (const field of Object.keys(req)) {
    add(field);
  }
  return rows;
}
