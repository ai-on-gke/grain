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

import React, {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  DEFAULT_PINNED_ITEMS,
  PINNED_STORAGE_KEY,
  loadPinnedItems,
  resetPinnedItems,
  samePin,
  savePinnedItems,
} from "./pinnedNav.js";
import { migrateStorage } from "./storageMigration.js";

const PinnedNavContext = createContext(null);

// The pinned items are this browser's, one list for the deployment
// (pinnedNav.js's PINNED_STORAGE_KEY, design/design/settings-panel.design.md §6.3).
export function PinnedNavProvider({ children }) {
  const [pinnedItems, setPinnedItemsState] = useState(loadPinnedItems);

  // Another tab of the same deployment saving, reordering or resetting
  // its pins (Customize sidebar). The list is read back through the same
  // migration and validation a fresh load goes through, and a removed
  // key -- Reset to default, or storage cleared (evt.key === null) --
  // reads as the defaults. Storage that will not take a write fires no
  // event, so this tab's in-memory list stands until the page reloads.
  useEffect(() => {
    const onStorage = (evt) => {
      if (evt.key !== null && evt.key !== PINNED_STORAGE_KEY) return;
      migrateStorage();
      setPinnedItemsState(loadPinnedItems());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const setPinnedItems = (items) => {
    setPinnedItemsState(items);
    savePinnedItems(items);
  };

  const addPinnedItem = (item) => {
    setPinnedItemsState((prev) => {
      if (prev.some((it) => samePin(it, item))) {
        return prev;
      }
      const next = [...prev, item];
      savePinnedItems(next);
      return next;
    });
  };

  const removePinnedItem = (indexOrItem) => {
    setPinnedItemsState((prev) => {
      const next =
        typeof indexOrItem === "number"
          ? prev.filter((_, idx) => idx !== indexOrItem)
          : prev.filter((it) => !samePin(it, indexOrItem));
      savePinnedItems(next);
      return next;
    });
  };

  const movePinnedItem = (fromIndex, toIndex) => {
    setPinnedItemsState((prev) => {
      if (fromIndex < 0 || fromIndex >= prev.length) return prev;
      if (toIndex < 0 || toIndex >= prev.length) return prev;
      const next = [...prev];
      const [moved] = next.splice(fromIndex, 1);
      next.splice(toIndex, 0, moved);
      savePinnedItems(next);
      return next;
    });
  };

  const resetToDefault = () => {
    resetPinnedItems();
    setPinnedItemsState(DEFAULT_PINNED_ITEMS);
  };

  const value = useMemo(
    () => ({
      pinnedItems,
      setPinnedItems,
      addPinnedItem,
      removePinnedItem,
      movePinnedItem,
      resetToDefault,
    }),
    [pinnedItems],
  );

  return (
    <PinnedNavContext.Provider value={value}>
      {children}
    </PinnedNavContext.Provider>
  );
}

export function usePinnedNav() {
  const ctx = useContext(PinnedNavContext);

  if (!ctx) {
    return {
      pinnedItems: loadPinnedItems(),
      setPinnedItems: (items) => savePinnedItems(items),
      addPinnedItem: (item) => {
        const cur = loadPinnedItems();
        if (!cur.some((it) => samePin(it, item))) {
          savePinnedItems([...cur, item]);
        }
      },
      removePinnedItem: (itemOrIndex) => {
        const cur = loadPinnedItems();
        const next =
          typeof itemOrIndex === "number"
            ? cur.filter((_, i) => i !== itemOrIndex)
            : cur.filter((it) => !samePin(it, itemOrIndex));
        savePinnedItems(next);
      },
      movePinnedItem: (fromIndex, toIndex) => {
        const cur = loadPinnedItems();
        if (
          fromIndex < 0 ||
          fromIndex >= cur.length ||
          toIndex < 0 ||
          toIndex >= cur.length
        )
          return;
        const next = [...cur];
        const [moved] = next.splice(fromIndex, 1);
        next.splice(toIndex, 0, moved);
        savePinnedItems(next);
      },
      resetToDefault: () => resetPinnedItems(),
    };
  }

  return ctx;
}
