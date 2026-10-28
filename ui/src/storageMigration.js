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
  PINNED_STORAGE_KEY,
  migratePinnedViews,
  parsePinnedList,
  samePin,
} from "./pinnedNav.js";
import { TIMEZONE_STORAGE_KEY } from "./PreferencesContext.jsx";

// The one-time move of this browser's per-user keys to deployment-wide
// ones (docs/design/settings-panel.md §6.3). grain has no users, so the
// name a browser once chose (grain.username) no longer picks which
// pinned list or time zone it reads. main.jsx runs this before the first
// render, so nothing ever reads an old key.
export const STORAGE_VERSION_KEY = "grain.storageVersion";
export const STORAGE_VERSION = "1";

const USERNAME_KEY = "grain.username";
const OLD_TIMEZONE_KEY = "grain.userTimeZone";
const OLD_PINNED_PREFIX = "grain.pinnedNav";

function storedKeys(storage) {
  const keys = [];
  for (let i = 0; i < storage.length; i += 1) {
    const key = storage.key(i);
    if (key !== null) keys.push(key);
  }
  return keys;
}

// pinnedCandidates is every old pinned-list key in storage, in §6.3's
// order of preference: the remembered name's list, the nameless one,
// any other name's (localStorage keeps no timestamps, so alphabetically),
// the pre-team lists, then any other team's. A list grain.pinned already
// holds -- a retry after a step threw -- comes before all of them.
export function pinnedCandidates(keys, username) {
  const user = (username || "").trim();
  const old = keys
    .filter(
      (key) =>
        key === OLD_PINNED_PREFIX || key.startsWith(`${OLD_PINNED_PREFIX}.`),
    )
    .sort();
  const rank = (key) => {
    if (user && key === `${OLD_PINNED_PREFIX}.${user}.default`) return 1;
    if (key === `${OLD_PINNED_PREFIX}..default`) return 2;
    if (key.endsWith(".default") && key.split(".").length >= 4) return 3;
    if (user && key === `${OLD_PINNED_PREFIX}.${user}`) return 4;
    if (key === OLD_PINNED_PREFIX) return 4.5;
    return 5;
  };
  const ordered = old
    .map((key, index) => ({ key, index, rank: rank(key) }))
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .map((c) => c.key);
  return keys.includes(PINNED_STORAGE_KEY)
    ? [PINNED_STORAGE_KEY, ...ordered]
    : ordered;
}

// mergePinnedLists is the first list with every later list's items it
// does not already hold (samePin), appended in their own order:
// nothing any name had pinned in this browser is lost.
export function mergePinnedLists(lists) {
  const out = [];
  for (const list of lists) {
    for (const item of list) {
      if (!out.some((it) => samePin(it, item))) {
        out.push(item);
      }
    }
  }
  return out;
}

function migratePins(storage, keys, username) {
  const lists = [];
  for (const key of pinnedCandidates(keys, username)) {
    let list = null;
    try {
      list = parsePinnedList(storage.getItem(key));
    } catch {
      // A list that does not parse is dropped with the rest, rather
      // than holding the migration back on every load.
    }
    // The first list that parses is kept as it is, an emptied one
    // included; later ones only add to it.
    // Each list is mapped (§5.6) before the merge, so a view one name
    // pinned and the tab another pinned in its place are one pin.
    if (list && (lists.length === 0 || list.length > 0))
      lists.push(migratePinnedViews(list));
  }
  if (lists.length > 0) {
    storage.setItem(
      PINNED_STORAGE_KEY,
      JSON.stringify(migratePinnedViews(mergePinnedLists(lists))),
    );
  }
  for (const key of keys) {
    if (key === OLD_PINNED_PREFIX || key.startsWith(`${OLD_PINNED_PREFIX}.`)) {
      storage.removeItem(key);
    }
  }
}

function migrateTimeZone(storage) {
  const old = (storage.getItem(OLD_TIMEZONE_KEY) || "").trim();
  if (old && !storage.getItem(TIMEZONE_STORAGE_KEY)) {
    storage.setItem(TIMEZONE_STORAGE_KEY, old);
  }
  storage.removeItem(OLD_TIMEZONE_KEY);
}

// migrateStorage runs the migration once per browser and reports whether
// storage now stands at STORAGE_VERSION. Unavailable storage is not an
// error, as everywhere else that reads it; a step that throws leaves the
// version unwritten, so the next load tries again, and every step is
// safe to repeat.
export function migrateStorage(getStorage = () => globalThis.localStorage) {
  let storage;
  try {
    storage = getStorage();
    if (!storage) return false;
    if (storage.getItem(STORAGE_VERSION_KEY) === STORAGE_VERSION) return true;
  } catch {
    return false;
  }
  try {
    const username = (storage.getItem(USERNAME_KEY) || "").trim();
    migratePins(storage, storedKeys(storage), username);
    migrateTimeZone(storage);
    // Last, once the pins have been read by it.
    storage.removeItem(USERNAME_KEY);
    storage.setItem(STORAGE_VERSION_KEY, STORAGE_VERSION);
    return true;
  } catch {
    return false;
  }
}
