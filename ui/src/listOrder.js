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

// listOrder.js is a display order for the list pages that have no
// backlog of their own -- repos, templates, schedules, suites
// (grain/task-327).
//
// # Why these lists get an order at all
//
// The task list is dragged into the order somebody wants to work in, and
// that order is real: it is the backlog the dispatcher actually reads
// (Store.Reorder, bwsalmon/agents#476), so a row carries a drag handle
// and moving it changes what runs next. None of the other four lists has
// anything like that -- a repo is not dispatched against another repo,
// and a schedule fires on its own clock -- so they were left with no
// manual order, no handle, and rows that read as flatter and blanker
// than the task rows beside them for no reason a reader can see.
//
// The order here is exactly what it looks like: where a row sits on the
// page, and nothing else. Dragging a repo to the top does not make it
// more important to the daemon; it makes it the first row, for the
// person who dragged it, the way pinning a tab does. That is worth
// having on its own -- the four repos somebody actually watches sit at
// the top instead of wherever the alphabet put them -- and it is why
// this is browser state rather than an API call.
//
// # Where it is kept
//
// localStorage, one key per list, the same treatment the board's columns
// and the theme mode get (board.js, ThemeModeContext.jsx) and for the
// same reason: it is how one person wants to look at the deployment, not
// a fact about the deployment. Nothing behind /api/config changes when a
// row moves, and two people looking at one grain are each allowed their
// own order.
//
// Anything unreadable -- a half-written value, a shape from some other
// build, localStorage denied outright by the browser -- is treated as
// "no order stored yet" rather than as an error to show somebody: the
// list still renders, in its own default order, and the next drag
// overwrites the bad value.
//
// # What a stored order does *not* have to be
//
// It is a list of ids, and neither side of it is required to match the
// items on screen. Ids that are gone (a deleted template) are ignored;
// items the stored order has never heard of (a repo added this morning)
// sort after every item it does name, in whatever order the list's own
// fallback comparator puts them -- so a new arrival appears at the
// bottom, predictably, instead of being silently dropped or jumping into
// the middle. Nothing is ever rewritten to prune the stale ids: the next
// drag stores the order as it is displayed, which prunes them anyway.

import { useState } from "react";

export const ORDER_STORAGE_PREFIX = "grain.list-order.";

const storageKey = (list) => `${ORDER_STORAGE_PREFIX}${list}`;

// loadOrder is the ids this browser last stored for one list, oldest
// entry first, or [] for a list nobody has dragged yet.
// LEGACY_PROCESS_ORDERS are the three lists Processes replaced
// (docs/design/settings-panel.md §5.6), each with the prefix its rows'
// keys carry in the one list now (processes.js's row `key`).
const LEGACY_PROCESS_ORDERS = [
  ["schedules", "schedule:"],
  ["templates", "template:"],
  ["suites", "suite:"],
];

// migrateProcessOrder merges the Schedules, Templates and Suites pages'
// custom orders into the Processes one: schedules first, then
// templates, then suites, each in its own saved order. Rows nobody had
// placed follow, by the default sort, which is applyOrder's own rule for
// an id the order does not name. The three old keys are then removed,
// so this runs once; a Processes order already saved wins over them.
export function migrateProcessOrder() {
  let legacy;
  try {
    legacy = LEGACY_PROCESS_ORDERS.filter(
      ([list]) => localStorage.getItem(storageKey(list)) !== null,
    );
  } catch {
    return;
  }
  if (legacy.length === 0) return;
  if (loadOrder("processes").length === 0) {
    const merged = legacy.flatMap(([list, prefix]) =>
      loadOrder(list).map((id) => `${prefix}${id}`),
    );
    if (merged.length > 0) saveOrder("processes", merged);
  }
  try {
    for (const [list] of legacy) localStorage.removeItem(storageKey(list));
  } catch {
    // Storage that refuses a removal leaves the old keys behind; the
    // Processes order is saved by then, and wins on every later load.
  }
}

export function loadOrder(list) {
  let raw = null;
  try {
    raw = localStorage.getItem(storageKey(list));
  } catch {
    return [];
  }
  if (!raw) return [];
  try {
    const ids = JSON.parse(raw);
    return Array.isArray(ids) ? ids.filter((id) => typeof id === "string") : [];
  } catch {
    return [];
  }
}

// saveOrder writes one list's order back, or clears the key when there
// is no order left to keep -- so a list that has never been dragged, or
// whose every item is gone, follows whatever this build calls the
// default rather than being pinned to an empty array.
export function saveOrder(list, ids) {
  try {
    if (ids.length === 0) localStorage.removeItem(storageKey(list));
    else localStorage.setItem(storageKey(list), JSON.stringify(ids));
  } catch {
    // A browser that won't store this still shows the order the drag
    // asked for; it just forgets it on reload. Not worth an error
    // banner over a preference.
  }
}

// applyOrder puts items in the stored order, with everything the order
// does not name after everything it does, ordered among themselves by
// the list's own fallback comparator (its alphabetical default). With
// nothing stored that leaves the fallback order untouched, which is what
// makes "custom order" a safe *default* sort on a list nobody has
// dragged yet: it looks exactly like the alphabetical list it replaced.
export function applyOrder(order, items, idOf, fallbackCmp) {
  const rank = new Map(order.map((id, i) => [id, i]));
  const at = (item) => {
    const i = rank.get(idOf(item));
    return i === undefined ? Number.POSITIVE_INFINITY : i;
  };
  return [...items].sort((a, b) => {
    // Compared rather than subtracted: two items the order has never
    // heard of are both at Infinity, and Infinity - Infinity is NaN,
    // which sorts them by nothing at all instead of by the fallback.
    const ra = at(a);
    const rb = at(b);
    if (ra !== rb) return ra < rb ? -1 : 1;
    return fallbackCmp ? fallbackCmp(a, b) : 0;
  });
}

// moveBefore is one drop: the dragged id lands immediately before
// beforeId, or at the very end when beforeId is null (the list's
// trailing drop zone). ids is the order as displayed, so a drop inside a
// search-narrowed list still lands where it looks like it landed --
// directly above the row it was dropped on.
export function moveBefore(ids, dragId, beforeId) {
  const next = ids.filter((id) => id !== dragId);
  const idx = beforeId === null ? -1 : next.indexOf(beforeId);
  next.splice(idx === -1 ? next.length : idx, 0, dragId);
  return next;
}

// useListOrder is the whole of the above as one list page holds it:
// `ordered` is the items in this browser's order, `move` is what a drop
// calls. The stored order is read once, when the page mounts, and kept
// in state from then on, so a drag re-renders from the array it just
// wrote rather than from a second read of localStorage.
//
// `items` is deliberately every item the page has, not the ones its
// search is currently showing: the order that gets stored has to include
// the rows a search is hiding, or narrowing the list to two rows and
// dragging one would throw away the position of everything else.
export function useListOrder(list, items, idOf, fallbackCmp) {
  const [order, setOrder] = useState(() => loadOrder(list));
  const ordered = applyOrder(order, items, idOf, fallbackCmp);
  const move = (dragId, beforeId) => {
    const next = moveBefore(ordered.map(idOf), dragId, beforeId);
    setOrder(next);
    saveOrder(list, next);
  };
  return [ordered, move];
}
