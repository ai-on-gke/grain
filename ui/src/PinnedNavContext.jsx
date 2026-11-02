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

import React, { createContext, useContext, useMemo, useState } from "react";
import {
  DEFAULT_PINNED_ITEMS,
  loadPinnedItems,
  resetPinnedItems,
  samePin,
  savePinnedItems,
} from "./pinnedNav.js";

const PinnedNavContext = createContext(null);

// The pinned items are this browser's, one list for the deployment
// (pinnedNav.js's PINNED_STORAGE_KEY, design/design/settings-panel.design.md §6.3).
export function PinnedNavProvider({ children }) {
  const [pinnedItems, setPinnedItemsState] = useState(loadPinnedItems);

  const setPinnedItems = (items) => {
    setPinnedItemsState(items);
    savePinnedItems(items);
  };

  const addPinnedItem = (item) => {
    if (pinnedItems.some((it) => samePin(it, item))) {
      return;
    }
    const next = [...pinnedItems, item];
    setPinnedItems(next);
  };

  const removePinnedItem = (indexOrItem) => {
    let next;
    if (typeof indexOrItem === "number") {
      next = pinnedItems.filter((_, idx) => idx !== indexOrItem);
    } else {
      next = pinnedItems.filter((it) => !samePin(it, indexOrItem));
    }
    setPinnedItems(next);
  };

  const movePinnedItem = (fromIndex, toIndex) => {
    if (fromIndex < 0 || fromIndex >= pinnedItems.length) return;
    if (toIndex < 0 || toIndex >= pinnedItems.length) return;
    const next = [...pinnedItems];
    const [moved] = next.splice(fromIndex, 1);
    next.splice(toIndex, 0, moved);
    setPinnedItems(next);
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
