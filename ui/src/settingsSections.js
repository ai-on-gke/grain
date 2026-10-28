// The sections of the Settings view and the tabs of the System view, and
// the redirects from every path that used to open one of them
// (docs/design/settings-panel.md §3.2, §2.1, §3.5).
//
// Pure data and pure functions, kept out of SettingsPage.jsx and
// SystemPage.jsx so paths.js can read them without importing React
// components.

// SETTINGS_GROUPS is the section nav, top to bottom. `keywords` is what
// the filter matches besides the label for a section as a whole; each
// field's own entry is in settingsIndex.js.
export const SETTINGS_GROUPS = [
  {
    id: "deployment",
    label: "Deployment",
    sections: [
      { id: "general", label: "General" },
      { id: "access", label: "Access" },
      { id: "config-repo", label: "Config repo" },
      // Upgrade is an action rather than a setting: it never joins the
      // save bar, and stays usable when the config repo is external.
      { id: "upgrade", label: "Upgrade" },
    ],
  },
  {
    id: "agents",
    label: "Agents",
    sections: [
      { id: "models", label: "Models" },
      { id: "agents", label: "Agents" },
      { id: "memory", label: "Memory" },
    ],
  },
  {
    id: "work",
    label: "Work",
    sections: [
      { id: "tasks", label: "Tasks" },
      { id: "repositories", label: "Repositories" },
    ],
  },
  {
    id: "connections",
    label: "Connections",
    sections: [
      { id: "git", label: "Git" },
      { id: "capabilities", label: "Capabilities" },
      {
        id: "credentials",
        label: "Legacy capabilities",
        keywords: ["credentials", "secrets", "legacy capabilities"],
      },
      { id: "executors", label: "Executors" },
    ],
  },
  {
    id: "infrastructure",
    label: "Infrastructure",
    sections: [
      { id: "engine", label: "Engine" },
      { id: "ci", label: "CI" },
      { id: "sandboxes", label: "Sandboxes" },
      { id: "caches", label: "Caches" },
    ],
  },
  {
    id: "browser",
    label: "This browser",
    sections: [{ id: "preferences", label: "Preferences" }],
  },
];

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
// are on Legacy capabilities (whose id stays credentials).
const SETTINGS_SECTION_ALIASES = {
  "capability-servers": "capabilities",
  "legacy-capabilities": "credentials",
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
