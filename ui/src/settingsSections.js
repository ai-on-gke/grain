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

// The sections of the Settings view and the tabs of the System view, and
// the redirects from every path that used to open one of them
// (design/design/settings-panel.design.md §3.2, §2.1, §3.5).
//
// Pure data and pure functions, kept out of SettingsPage.jsx and
// SystemPage.jsx so paths.js can read them without importing React
// components.

// SETTINGS_GROUPS is the section nav, top to bottom. `keywords` is what
// the filter matches besides the label for a section as a whole; each
// field's own entry is in settingsIndex.js.
//
// The Settings UI edits what a deployment needs before it can read its
// config repo at all -- Models and Sandboxes -- plus the write-only
// secret store, sign-in, this browser's preferences and the operational
// controls (design/design/config-repo-settings.design.md §2.1, §2.3). Everything
// else lives in the config repo: those sections are under "Managed in
// config repo", drawn read-only with a link to the file that holds them
// (CONFIG_REPO_SECTION_PATHS) whenever the API sends their writes there
// (GET /api/config's configRepoOnly).
export const SETTINGS_GROUPS = [
  {
    id: "bootstrap",
    label: "Models & sandboxes",
    sections: [
      { id: "models", label: "Models" },
      { id: "sandboxes", label: "Sandboxes" },
      { id: "executors", label: "Executors" },
    ],
  },
  {
    id: "secrets",
    label: "Secrets & access",
    sections: [
      {
        id: "credentials",
        label: "Legacy capabilities",
        keywords: ["credentials", "secrets", "legacy capabilities"],
      },
      { id: "access", label: "Access" },
    ],
  },
  {
    id: "browser",
    label: "This browser",
    sections: [{ id: "preferences", label: "Preferences" }],
  },
  {
    id: "operations",
    label: "System / Operations",
    sections: [
      // Upgrade is an action rather than a setting: it never joins the
      // save bar, and stays usable when the config repo is external.
      { id: "upgrade", label: "Upgrade" },
      { id: "caches", label: "Caches" },
      { id: "engine", label: "Engine" },
      { id: "config-repo", label: "Config repo" },
    ],
  },
  {
    id: "config-managed",
    label: "Managed in config repo",
    sections: [
      { id: "general", label: "General" },
      { id: "git", label: "Git" },
      { id: "repositories", label: "Repositories" },
      { id: "tasks", label: "Tasks" },
      { id: "agents", label: "Agents" },
      { id: "capabilities", label: "Capabilities" },
      { id: "ci", label: "CI" },
      { id: "memory", label: "Memory" },
    ],
  },
];

// CONFIG_REPO_SECTION_PATHS are the files of grain/config that hold each
// config-repo-only section's settings, the first the one its "View in
// config repo" link opens (staterepo.DumpPath, and the files a refused
// write from that section names).
export const CONFIG_REPO_SECTION_PATHS = {
  general: ["tables/grain_config.json"],
  // Git connections live in grain_config (git_connections, git_credential)
  // and each repository's binding in repo_config (git_credential); their
  // tokens and keys stay in the secret store and are never exported.
  git: ["tables/grain_config.json", "tables/repo_config.json"],
  repositories: ["tables/repo_config.json", "tables/grain_config.json"],
  // settings/capabilities.yml is exported only once a deployment has
  // adopted the capability catalogue; until then capability servers,
  // policy and default grants are columns of grain_config and scope sets
  // rows of api_scope_set. So the files that always exist come first,
  // and the first link opens one.
  tasks: [
    "tables/grain_config.json",
    "tables/custom_field.json",
    "settings/capabilities.yml",
  ],
  // Standing console sessions are grain_config.console_sessions.
  agents: ["tables/grain_config.json"],
  capabilities: [
    "tables/grain_config.json",
    "tables/api_scope_set.json",
    "settings/capabilities.yml",
  ],
  ci: ["tables/grain_config.json", "tables/repo_config.json"],
  memory: ["memory/deployment.md"],
  // Caches sit under System / Operations, but none of their four
  // settings is a bootstrap one: with settings locked the API refuses
  // them like any other config-managed field (settings-mutability.spec.md
  // D-1), so the section is drawn read-only then too.
  caches: ["tables/grain_config.json"],
};

// isConfigRepoSection is whether a section's settings are config-repo
// only, and so drawn read-only.
export function isConfigRepoSection(id) {
  return Object.hasOwn(CONFIG_REPO_SECTION_PATHS, id);
}

// configBrowserPath is the Config Repo Browser's address with path
// open: Settings → Config repo, which paths.js parses /config to.
export function configBrowserPath(path) {
  return path ? `/config?path=${encodeURIComponent(path)}` : "/config";
}

// DEFAULT_SETTINGS_SECTION is what a bare /settings opens beside the nav
// (a phone shows the list instead).
export const DEFAULT_SETTINGS_SECTION = "models";

export const SETTINGS_SECTIONS = SETTINGS_GROUPS.flatMap((g) => g.sections);

export const SYSTEM_TABS = [
  { id: "health", label: "Health" },
  { id: "logs", label: "Logs" },
  { id: "sandboxes", label: "Sandboxes" },
  { id: "caches", label: "Caches" },
  { id: "top", label: "Top" },
  { id: "pipeline", label: "Pipeline" },
  { id: "metrics", label: "Metrics" },
  { id: "power", label: "Power" },
];

export function settingsSectionLabel(id) {
  return SETTINGS_SECTIONS.find((s) => s.id === id)?.label || "";
}

export function systemTabLabel(id) {
  return SYSTEM_TABS.find((t) => t.id === id)?.label || "";
}

// The old System overlay's settings tabs, by the id /admin/<id> and
// /system/<id> named, and the Settings section that took each over.
const OLD_SETTINGS_TABS = {
  general: "general",
  admin: "general",
  agents: "agents",
  models: "models",
  git: "git",
  workflow: "tasks",
  access: "access",
  accounts: "access",
  engine: "engine",
  sandbox: "sandboxes",
  executors: "executors",
  integrations: "credentials",
  state: "config-repo",
  // Upgrade was a System tab (/system/upgrade) until it moved into
  // Settings; old links and bookmarks land on the section.
  upgrade: "upgrade",
};

// The old overlay's status tabs, which keep their ids in System.
const OLD_SYSTEM_TABS = [
  "logs",
  "sandboxes",
  "caches",
  "top",
  "pipeline",
  "power",
];

// SETTINGS_SECTION_ALIASES are /settings/<section> paths that name a
// pane by what it holds rather than by its section: the capability
// servers card is on Capabilities, and the legacy capabilities' secrets
// are on Legacy capabilities (whose id stays credentials), under
// Secrets & access.
const SETTINGS_SECTION_ALIASES = {
  "capability-servers": "capabilities",
  "legacy-capabilities": "credentials",
  secrets: "credentials",
};

// settingsSectionFor is the section a /settings/<section> path names,
// or "" for none -- a mistyped or stale one lands on the bare /settings
// (General, or the section list on a phone) rather than on a blank
// pane. Matched without regard to case, as a hand-typed path is.
export function settingsSectionFor(raw) {
  const id = String(raw || "").toLowerCase();
  if (SETTINGS_SECTIONS.some((s) => s.id === id)) return id;
  return SETTINGS_SECTION_ALIASES[id] || "";
}

// systemTabFor is the tab a /system/<tab> path names, or "" for none.
// An explicit /system/health stays "health" rather than folding into the
// bare /system: on a phone the bare path is the list of tabs, not Health,
// so a reloaded or shared /system/health would land on the list. App
// folds it back to "" on a wider screen, where the two are the same page.
export function systemTabFor(raw) {
  const id = String(raw || "").toLowerCase();
  return SYSTEM_TABS.some((t) => t.id === id) ? id : "";
}

// oldAdminDestination is where /admin/<tab> goes now (§3.5): a settings
// tab to its section, a status tab to System, anything else to General.
export function oldAdminDestination(raw) {
  const id = String(raw || "").toLowerCase();
  if (OLD_SYSTEM_TABS.includes(id)) return { view: "system", systemTab: id };
  return {
    view: "settings",
    settingsSection: OLD_SETTINGS_TABS[id] || "general",
  };
}

// systemDestination is where /system/<tab> goes: one of System's own
// tabs stays, an old settings tab id goes where /admin/<id> does, and
// anything else is Health.
export function systemDestination(raw) {
  const id = String(raw || "").toLowerCase();
  const tab = systemTabFor(id);
  if (tab) return { view: "system", systemTab: tab };
  if (OLD_SETTINGS_TABS[id] && id !== "admin")
    return { view: "settings", settingsSection: OLD_SETTINGS_TABS[id] };
  return { view: "system", systemTab: "" };
}
