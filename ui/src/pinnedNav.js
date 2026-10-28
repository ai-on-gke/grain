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

import {
  SETTINGS_SECTIONS,
  SYSTEM_TABS,
  settingsSectionLabel,
  systemTabLabel,
} from "./settingsSections.js";

export const DEFAULT_PINNED_ITEMS = [{ kind: "view", id: "inbox" }];

export const AVAILABLE_VIEWS = [
  { id: "inbox", label: "Inbox", glyph: "inbox" },
  { id: "tasks", label: "Tasks", glyph: "tasks" },
  { id: "repos", label: "Repos", glyph: "repos" },
  { id: "ci", label: "CI", glyph: "ci" },
  { id: "processes", label: "Processes", glyph: "processes" },
  { id: "boards", label: "Boards", glyph: "boards" },
  { id: "sessions", label: "Sessions", glyph: "sessions" },
  { id: "system", label: "System", glyph: "system" },
  { id: "settings", label: "Settings", glyph: "settings" },
];

// The glyph a pin of one System tab or Settings section draws when it
// has none of its own: the one the view it used to be drew, so a
// migrated pin looks as it did.
const TAB_GLYPHS = { sandboxes: "sandboxes", caches: "cache" };
const SECTION_GLYPHS = { memory: "memory" };

// MOVED_VIEWS are the views that became a System tab or a Settings
// section (docs/design/settings-panel.md §5.6, stage a). migratePinnedViews
// rewrites a pin of one to that tab or section, keeping its colour and
// icon.
const MOVED_VIEWS = {
  sandboxes: { id: "system", tab: "sandboxes" },
  caches: { id: "system", tab: "caches" },
  memory: { id: "settings", section: "memory" },
};

// pinTargets are what a view pin can name, for the customize overlay's
// picker: every view, then each System tab and each Settings section,
// as "system/<tab>" and "settings/<section>". pinFromTarget turns a
// pick back into a pinned item.
export function pinTargets() {
  return [
    ...AVAILABLE_VIEWS.map((v) => ({ id: v.id, label: v.label })),
    ...SYSTEM_TABS.map((t) => ({
      id: `system/${t.id}`,
      label: `System → ${t.label}`,
    })),
    ...SETTINGS_SECTIONS.map((sec) => ({
      id: `settings/${sec.id}`,
      label: `Settings → ${sec.label}`,
    })),
  ];
}

export function pinFromTarget(target) {
  const [id, sub] = String(target || "").split("/");
  if (id === "system" && sub) return { kind: "view", id, tab: sub };
  if (id === "settings" && sub) return { kind: "view", id, section: sub };
  return { kind: "view", id };
}

// samePin is whether two pinned items name the same place.
export function samePin(a, b) {
  return (
    a.kind === b.kind &&
    a.id === b.id &&
    (a.tab || "") === (b.tab || "") &&
    (a.section || "") === (b.section || "")
  );
}

export const CHLADNI_ICONS = [
  { id: "inbox", label: "Inbox (Tray)", figure: "tray" },
  {
    id: "tasks",
    label: "Tasks (Four lobes)",
    figure: "four lobes on one centre",
  },
  { id: "repos", label: "Repos (Ring)", figure: "ring" },
  {
    id: "branches",
    label: "Branches (Split stem)",
    figure: "a stem split in two",
  },
  { id: "boards", label: "Boards (Grid blocks)", figure: "columns and blocks" },
  {
    id: "schedules",
    label: "Schedules (Two posts)",
    figure: "two posts, open middle",
  },
  { id: "templates", label: "Templates (Lattice)", figure: "lattice" },
  {
    id: "processes",
    label: "Processes (Loop through three nodes)",
    figure: "a loop through three nodes",
  },
  {
    id: "suites",
    label: "Suites (Six lobes)",
    figure: "six lobes in clustered pairs",
  },
  {
    id: "ci",
    label: "CI (Gate arms)",
    figure: "winged gate arms closing on a channel",
  },
  { id: "sandboxes", label: "Sandboxes (Chambers)", figure: "nested chambers" },
  {
    id: "sessions",
    label: "Sessions (Dialogue bars)",
    figure: "dialogue bars across a channel",
  },
  {
    id: "user",
    label: "User (Head and shoulders)",
    figure: "head and shoulders",
  },
  { id: "director", label: "Director (Portico)", figure: "portico and podium" },
  { id: "cache", label: "Cache (Crate)", figure: "crate of stacked layers" },
  {
    id: "alerts",
    label: "Alerts (Bell dome)",
    figure: "bell dome over a flared lip and clapper",
  },
  { id: "settings", label: "Settings (Gear)", figure: "gear" },
  { id: "system", label: "System (Core case)", figure: "case around a core" },
  {
    id: "metrics",
    label: "Metrics (Tiered gauge)",
    figure: "tiered gauge between caps",
  },
];

// The System section was called Admin, and its figure was offered under
// both names. Only "System" is offered now; a shortcut saved with the old
// "admin" icon draws the same figure (ItemGlyph.jsx) and shows in the
// picker as "System".
export function pickerIconId(icon) {
  return icon === "admin" ? "system" : icon;
}

export const PINNED_COLOR_OPTIONS = [
  { label: "Default", value: "" },
  { label: "Gold", value: "#d9a441" },
  { label: "Amber", value: "#f59e0b" },
  { label: "Orange", value: "#f97316" },
  { label: "Red", value: "#ef4444" },
  { label: "Rose", value: "#f43f5e" },
  { label: "Pink", value: "#ec4899" },
  { label: "Purple", value: "#a855f7" },
  { label: "Indigo", value: "#6366f1" },
  { label: "Blue", value: "#3b82f6" },
  { label: "Sky", value: "#0ea5e9" },
  { label: "Cyan", value: "#06b6d4" },
  { label: "Teal", value: "#14b8a6" },
  { label: "Emerald", value: "#10b981" },
  { label: "Green", value: "#22c55e" },
  { label: "Lime", value: "#84cc16" },
  { label: "Gray", value: "#94a3b8" },
];

// PINNED_STORAGE_KEY is where this browser keeps its pinned list: one
// list for the deployment, with no user or team segment
// (docs/design/settings-panel.md §6.3). localStorage is per origin, so
// that is one list per deployment. storageMigration.js moved the old
// grain.pinnedNav.<user>.<team> lists here.
export const PINNED_STORAGE_KEY = "grain.pinned";

export function cleanPinnedItem(item) {
  const clean = { kind: item.kind, id: item.id };
  if (item.kind === "view") {
    if (item.id === "system" && typeof item.tab === "string" && item.tab)
      clean.tab = item.tab;
    if (
      item.id === "settings" &&
      typeof item.section === "string" &&
      item.section
    )
      clean.section = item.section;
  }
  const icon = item.icon || item.glyph;
  if (typeof icon === "string" && icon.trim()) {
    clean.icon = icon.trim();
  }
  if (typeof item.color === "string" && item.color.trim()) {
    clean.color = item.color.trim();
  }
  return clean;
}

// PROCESS_VIEW_IDS are the pinned views Processes replaced
// (docs/design/settings-panel.md §5.6). A list that pinned any of them
// pins Processes once instead, where the first of them was, with that
// first pin's own colour and icon; the rest are dropped. The mapping is
// idempotent: a list that already pins Processes keeps that pin where it
// is and drops the old ones.
const PROCESS_VIEW_IDS = ["schedules", "templates", "suites"];

export function migratePinnedViews(items) {
  const out = [];
  let processes = false;
  for (const raw of items) {
    const moved = raw.kind === "view" ? MOVED_VIEWS[raw.id] : null;
    const item = moved ? { ...raw, ...moved } : raw;
    const isProcess =
      item.kind === "view" &&
      (item.id === "processes" || PROCESS_VIEW_IDS.includes(item.id));
    if (!isProcess) {
      out.push(item);
      continue;
    }
    if (processes) continue;
    processes = true;
    out.push({ ...item, id: "processes" });
  }
  return out;
}

// parsePinnedList is the pinned list raw (a stored JSON string) holds,
// or null when it holds none or is unreadable -- which callers read as
// "the defaults". It throws on JSON that does not parse.
export function parsePinnedList(raw) {
  if (!raw) return null;
  const parsed = JSON.parse(raw);
  if (!Array.isArray(parsed)) return null;
  const valid = parsed.filter(
    (item) =>
      item &&
      typeof item === "object" &&
      typeof item.kind === "string" &&
      typeof item.id === "string" &&
      ["view", "session", "board", "staging"].includes(item.kind),
  );
  return valid.map(cleanPinnedItem);
}

export function loadPinnedItems() {
  try {
    const stored = parsePinnedList(localStorage.getItem(PINNED_STORAGE_KEY));
    if (!stored) return DEFAULT_PINNED_ITEMS;
    // The §5.6 migration, written back on the first load that changed
    // anything so the stored list is rewritten once rather than mapped
    // on every load forever.
    const migrated = migratePinnedViews(stored);
    if (JSON.stringify(migrated) !== JSON.stringify(stored)) {
      localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(migrated));
    }
    return migrated;
  } catch {
    return DEFAULT_PINNED_ITEMS;
  }
}

export function savePinnedItems(items) {
  try {
    const list = Array.isArray(items) ? items.map(cleanPinnedItem) : [];
    localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(list));
  } catch {
    // Storage unavailable or disabled; changes still apply in-memory
  }
}

export function resetPinnedItems() {
  try {
    localStorage.removeItem(PINNED_STORAGE_KEY);
  } catch {
    // Storage unavailable
  }
}

export function getPinnedItemInfo(item, { sessions = [], boards = [] } = {}) {
  const customIcon = (item.icon || item.glyph || "").trim() || null;
  const customColor = (item.color || "").trim() || null;

  if (item.kind === "view" && item.id === "system" && item.tab) {
    return {
      label: systemTabLabel(item.tab) || item.tab,
      glyph: customIcon || TAB_GLYPHS[item.tab] || "system",
      color: customColor,
      typeLabel: "System tab",
    };
  }
  if (item.kind === "view" && item.id === "settings" && item.section) {
    return {
      label: settingsSectionLabel(item.section) || item.section,
      glyph: customIcon || SECTION_GLYPHS[item.section] || "settings",
      color: customColor,
      typeLabel: "Settings section",
    };
  }
  if (item.kind === "view") {
    const v = AVAILABLE_VIEWS.find((view) => view.id === item.id);
    return {
      label: v ? v.label : item.id,
      glyph: customIcon || (v ? v.glyph : null),
      color: customColor,
      typeLabel: "View",
    };
  }
  if (item.kind === "session") {
    const s = sessions.find((session) => session.id === item.id);
    return {
      label: s ? s.title || s.id : item.id,
      glyph: customIcon || null,
      dot: !customIcon,
      color: customColor,
      typeLabel: "Session",
    };
  }
  if (item.kind === "board") {
    const b = boards.find((board) => board.id === item.id);
    return {
      label: b ? b.name : item.id,
      glyph: customIcon || null,
      dot: !customIcon,
      color: customColor,
      typeLabel: "Board",
    };
  }
  if (item.kind === "staging") {
    // A staging environment (a persistent sandbox, staging.js), named by
    // its sandbox's name, which is its label too.
    return {
      label: item.id,
      glyph: customIcon || null,
      dot: !customIcon,
      color: customColor,
      typeLabel: "Staging",
    };
  }
  return {
    label: item.id,
    glyph: customIcon || null,
    dot: !customIcon,
    color: customColor,
    typeLabel: "Custom",
  };
}
