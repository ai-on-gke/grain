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

import { useSyncExternalStore } from "react";

// SIDEBAR_SECTIONS are the parts of the left panel this browser may
// hide (Customize sidebar, CustomizeSidebarOverlay.jsx): every top-level
// destination of the main list and the two grouped sections around it,
// in the order the panel draws them. Hiding one takes its row -- and its
// rail count -- out of the panel, folded or not; its route still opens
// by URL, and a pin of the same place still shows under Pinned.
//
// The Customize control itself is not here: it sits in the panel's
// first row whether or not Pinned is shown, so the panel can never be
// customized into a state it cannot be customized back out of. Nor is
// the phone drawer's System row, which is how a phone reaches System.
export const SIDEBAR_SECTIONS = [
  { id: "pinned", label: "Pinned" },
  { id: "tasks", label: "Tasks" },
  { id: "boards", label: "Boards" },
  { id: "sessions", label: "Sessions" },
  { id: "processes", label: "Processes" },
  { id: "ci", label: "CI" },
  { id: "repos", label: "Repos" },
  { id: "staging", label: "Staging" },
];

const KNOWN = new Set(SIDEBAR_SECTIONS.map((s) => s.id));

// SIDEBAR_HIDDEN_STORAGE_KEY holds the ids of the sections this browser
// hides, as a JSON array -- the same per-browser shape as grain.pinned.
// Nothing stored means everything shown, and an id this build does not
// know is dropped on read, so a section added later is never hidden by
// a list written before it existed.
export const SIDEBAR_HIDDEN_STORAGE_KEY = "grain.sidebar.hidden";

export function parseHiddenSections(raw) {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return SIDEBAR_SECTIONS.map((s) => s.id).filter((id) =>
      parsed.includes(id),
    );
  } catch {
    return [];
  }
}

export function loadHiddenSections() {
  try {
    return parseHiddenSections(
      localStorage.getItem(SIDEBAR_HIDDEN_STORAGE_KEY),
    );
  } catch {
    return [];
  }
}

const listeners = new Set();
let snapshot = [];
// memory stands in for storage that will not take a write, so the
// choice still applies until the page reloads.
let memory = null;

// current reads storage each time but hands back the same array while
// its contents are unchanged, which useSyncExternalStore needs.
function current() {
  const next = memory ?? loadHiddenSections();
  if (next.join() !== snapshot.join()) snapshot = next;
  return snapshot;
}

function publish() {
  for (const fn of listeners) fn();
}

export function saveHiddenSections(ids) {
  const list = (Array.isArray(ids) ? ids : []).filter((id) => KNOWN.has(id));
  memory = null;
  try {
    if (list.length === 0) {
      localStorage.removeItem(SIDEBAR_HIDDEN_STORAGE_KEY);
    } else {
      localStorage.setItem(SIDEBAR_HIDDEN_STORAGE_KEY, JSON.stringify(list));
    }
  } catch {
    memory = list;
  }
  publish();
}

export function resetHiddenSections() {
  saveHiddenSections([]);
}

function subscribe(fn) {
  listeners.add(fn);
  // Another tab of the same deployment changing it.
  const onStorage = (evt) => {
    if (evt.key === null || evt.key === SIDEBAR_HIDDEN_STORAGE_KEY) publish();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(fn);
    window.removeEventListener("storage", onStorage);
  };
}

// useHiddenSections is the list of hidden section ids, kept current
// across every component reading it -- the Sidebar, App's overlay and
// Settings' -- without a provider of its own.
export function useHiddenSections() {
  return useSyncExternalStore(subscribe, current, current);
}
