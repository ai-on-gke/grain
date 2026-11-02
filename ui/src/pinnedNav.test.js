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

import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  DEFAULT_PINNED_ITEMS,
  AVAILABLE_VIEWS,
  CHLADNI_ICONS,
  PINNED_COLOR_OPTIONS,
  PINNED_STORAGE_KEY,
  loadPinnedItems,
  parsePinnedList,
  savePinnedItems,
  resetPinnedItems,
  getPinnedItemInfo,
  pickerIconId,
  migratePinnedViews,
  pinFromTarget,
  pinTargets,
  samePin,
} from "./pinnedNav.js";

describe("pinnedNav", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe("loadPinnedItems", () => {
    it("returns default pinned items when nothing stored", () => {
      expect(loadPinnedItems()).toEqual(DEFAULT_PINNED_ITEMS);
    });

    it("returns parsed items from the deployment-wide key", () => {
      const custom = [
        { kind: "view", id: "ci" },
        { kind: "board", id: "b1" },
      ];
      localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(custom));
      expect(PINNED_STORAGE_KEY).toBe("grain.pinned");
      expect(loadPinnedItems()).toEqual(custom);
    });

    it("loads custom icon and color if present", () => {
      const custom = [
        { kind: "view", id: "tasks", icon: "sandboxes", color: "#3b82f6" },
        { kind: "board", id: "b1", glyph: "boards", color: "#ef4444" },
      ];
      localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(custom));
      expect(loadPinnedItems()).toEqual([
        { kind: "view", id: "tasks", icon: "sandboxes", color: "#3b82f6" },
        { kind: "board", id: "b1", icon: "boards", color: "#ef4444" },
      ]);
    });

    it("filters out invalid items", () => {
      const invalid = [
        { kind: "view", id: "ci" },
        { kind: "unknown", id: "foo" },
        null,
        "string",
      ];
      localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(invalid));
      expect(loadPinnedItems()).toEqual([{ kind: "view", id: "ci" }]);
    });

    it("handles corrupt JSON gracefully", () => {
      localStorage.setItem(PINNED_STORAGE_KEY, "invalid JSON {");
      expect(loadPinnedItems()).toEqual(DEFAULT_PINNED_ITEMS);
    });

    it("does not read an old per-user list (storageMigration.js moves it)", () => {
      localStorage.setItem(
        "grain.pinnedNav.alice.default",
        JSON.stringify([{ kind: "session", id: "s1" }]),
      );
      expect(loadPinnedItems()).toEqual(DEFAULT_PINNED_ITEMS);
    });
  });

  describe("parsePinnedList", () => {
    it("is null for nothing stored and for a non-list", () => {
      expect(parsePinnedList(null)).toBeNull();
      expect(parsePinnedList("")).toBeNull();
      expect(parsePinnedList("{}")).toBeNull();
    });

    it("keeps an emptied list as empty rather than as the defaults", () => {
      expect(parsePinnedList("[]")).toEqual([]);
    });
  });

  describe("savePinnedItems and resetPinnedItems", () => {
    it("saves items with custom icon and color to localStorage", () => {
      const items = [
        { kind: "session", id: "director", icon: "director", color: "#d9a441" },
      ];
      savePinnedItems(items);
      expect(localStorage.getItem("grain.pinned")).toBe(JSON.stringify(items));
    });

    it("saves an emptied list, which loads as empty", () => {
      savePinnedItems([]);
      expect(loadPinnedItems()).toEqual([]);
    });

    it("removes storage on reset", () => {
      savePinnedItems([{ kind: "view", id: "tasks" }]);
      resetPinnedItems();
      expect(localStorage.getItem("grain.pinned")).toBeNull();
      expect(loadPinnedItems()).toEqual(DEFAULT_PINNED_ITEMS);
    });
  });

  describe("getPinnedItemInfo", () => {
    it("resolves view info with default glyph", () => {
      const info = getPinnedItemInfo({ kind: "view", id: "ci" });
      expect(info.label).toBe("CI");
      expect(info.typeLabel).toBe("View");
      expect(info.glyph).toBe("ci");
      expect(info.color).toBeNull();
    });

    it("resolves view info with custom glyph and color", () => {
      const info = getPinnedItemInfo({
        kind: "view",
        id: "ci",
        icon: "templates",
        color: "#10b981",
      });
      expect(info.label).toBe("CI");
      expect(info.typeLabel).toBe("View");
      expect(info.glyph).toBe("templates");
      expect(info.color).toBe("#10b981");
    });

    it("resolves session info with dot when no icon is specified", () => {
      const info = getPinnedItemInfo(
        { kind: "session", id: "s1" },
        { sessions: [{ id: "s1", title: "Review Bot" }] },
      );
      expect(info.label).toBe("Review Bot");
      expect(info.typeLabel).toBe("Session");
      expect(info.dot).toBe(true);
      expect(info.glyph).toBeNull();
    });

    it("resolves session info with custom icon and color", () => {
      const info = getPinnedItemInfo(
        { kind: "session", id: "s1", icon: "sessions", color: "#6366f1" },
        { sessions: [{ id: "s1", title: "Review Bot" }] },
      );
      expect(info.label).toBe("Review Bot");
      expect(info.typeLabel).toBe("Session");
      expect(info.dot).toBe(false);
      expect(info.glyph).toBe("sessions");
      expect(info.color).toBe("#6366f1");
    });

    it("resolves board info with custom icon and color", () => {
      const info = getPinnedItemInfo(
        { kind: "board", id: "b1", icon: "boards", color: "#f97316" },
        { boards: [{ id: "b1", name: "Infra Board" }] },
      );
      expect(info.label).toBe("Infra Board");
      expect(info.typeLabel).toBe("Board");
      expect(info.dot).toBe(false);
      expect(info.glyph).toBe("boards");
      expect(info.color).toBe("#f97316");
    });
  });

  // design/design/settings-panel.design.md §5.6, stage a: Sandboxes and Caches are
  // System tabs and Memory a Settings section, so a pin of one of the old
  // views is rewritten to its new place on load, once.
  describe("moved views", () => {
    const key = PINNED_STORAGE_KEY;

    it("rewrites sandboxes, caches and memory pins and writes them back once", () => {
      localStorage.setItem(
        key,
        JSON.stringify([
          { kind: "view", id: "inbox" },
          { kind: "view", id: "sandboxes", color: "#d9a441" },
          { kind: "view", id: "caches", icon: "ci" },
          { kind: "view", id: "memory" },
        ]),
      );
      const want = [
        { kind: "view", id: "inbox" },
        { kind: "view", id: "system", tab: "sandboxes", color: "#d9a441" },
        { kind: "view", id: "system", tab: "caches", icon: "ci" },
        { kind: "view", id: "settings", section: "memory" },
      ];
      expect(loadPinnedItems()).toEqual(want);
      expect(JSON.parse(localStorage.getItem(key))).toEqual(want);

      // A second load has nothing to move and writes nothing.
      const setItem = vi.spyOn(Storage.prototype, "setItem");
      expect(loadPinnedItems()).toEqual(want);
      expect(setItem).not.toHaveBeenCalled();
      setItem.mockRestore();
    });

    it("keeps the tab and section of a System or Settings pin, and nothing else's", () => {
      savePinnedItems([
        { kind: "view", id: "system", tab: "logs" },
        { kind: "view", id: "settings", section: "engine" },
        { kind: "view", id: "repos", tab: "logs", section: "engine" },
      ]);
      expect(loadPinnedItems()).toEqual([
        { kind: "view", id: "system", tab: "logs" },
        { kind: "view", id: "settings", section: "engine" },
        { kind: "view", id: "repos" },
      ]);
    });

    it("no longer offers Sandboxes or Caches as views, and offers System and Settings", () => {
      const ids = AVAILABLE_VIEWS.map((v) => v.id);
      expect(ids).not.toContain("sandboxes");
      expect(ids).not.toContain("caches");
      expect(ids).toEqual(expect.arrayContaining(["system", "settings"]));
    });

    it("offers each System tab and Settings section as a pin target", () => {
      const targets = pinTargets();
      expect(targets).toContainEqual({
        id: "system/sandboxes",
        label: "System → Sandboxes",
      });
      expect(targets).toContainEqual({
        id: "settings/memory",
        label: "Settings → Memory",
      });
      expect(pinFromTarget("system/caches")).toEqual({
        kind: "view",
        id: "system",
        tab: "caches",
      });
      expect(pinFromTarget("settings/memory")).toEqual({
        kind: "view",
        id: "settings",
        section: "memory",
      });
      expect(pinFromTarget("repos")).toEqual({ kind: "view", id: "repos" });
      expect(
        samePin(pinFromTarget("system/caches"), {
          kind: "view",
          id: "system",
          tab: "caches",
        }),
      ).toBe(true);
      expect(
        samePin(pinFromTarget("system/caches"), { kind: "view", id: "system" }),
      ).toBe(false);
    });

    it("names a moved pin as the view it was, with its glyph", () => {
      expect(
        getPinnedItemInfo({ kind: "view", id: "system", tab: "sandboxes" }),
      ).toMatchObject({ label: "Sandboxes", glyph: "sandboxes" });
      expect(
        getPinnedItemInfo({ kind: "view", id: "system", tab: "caches" }),
      ).toMatchObject({ label: "Caches", glyph: "cache" });
      expect(
        getPinnedItemInfo({ kind: "view", id: "settings", section: "memory" }),
      ).toMatchObject({ label: "Memory", glyph: "memory" });
      expect(getPinnedItemInfo({ kind: "view", id: "system" })).toMatchObject({
        label: "System",
        glyph: "system",
      });
    });
  });

  describe("constants", () => {
    it("exports a large selection of Chladni icons", () => {
      expect(CHLADNI_ICONS.length).toBeGreaterThanOrEqual(16);
      expect(CHLADNI_ICONS.some((c) => c.id === "tasks")).toBe(true);
      expect(CHLADNI_ICONS.some((c) => c.id === "schedules")).toBe(true);
      expect(CHLADNI_ICONS.some((c) => c.id === "inbox")).toBe(true);
      expect(CHLADNI_ICONS.find((c) => c.id === "ci")).toMatchObject({
        label: "CI (Gate arms)",
        figure: "winged gate arms closing on a channel",
      });
      expect(CHLADNI_ICONS.find((c) => c.id === "alerts")).toMatchObject({
        label: "Alerts (Bell dome)",
        figure: "bell dome over a flared lip and clapper",
      });
    });

    // The 2026-10-12 System-rename walkthrough found the icon picker
    // offering "Admin (Core case)" beside "System (Core case)", the same
    // figure under the section's old name.
    it("offers the System figure only under its new name", () => {
      expect(CHLADNI_ICONS.some((c) => /admin/i.test(c.label))).toBe(false);
      expect(CHLADNI_ICONS.find((c) => c.id === "system")?.label).toBe(
        "System (Core case)",
      );
      expect(pickerIconId("admin")).toBe("system");
      expect(pickerIconId("tasks")).toBe("tasks");
    });

    it("exports color palette options", () => {
      expect(PINNED_COLOR_OPTIONS.length).toBeGreaterThanOrEqual(10);
      expect(PINNED_COLOR_OPTIONS.some((c) => c.value === "#d9a441")).toBe(
        true,
      );
    });
  });

  // design/design/settings-panel.design.md §5.6: Schedules, Templates and Suites
  // were three pinnable views; they are one Processes view now.
  describe("the Processes migration", () => {
    const key = PINNED_STORAGE_KEY;

    it("maps every old view id to one Processes pin, at the first one's place", () => {
      for (const id of ["schedules", "templates", "suites"]) {
        localStorage.setItem(
          key,
          JSON.stringify([
            { kind: "view", id: "inbox" },
            { kind: "view", id, color: "#ef4444", icon: "ci" },
            { kind: "view", id: "tasks" },
          ]),
        );
        expect(loadPinnedItems()).toEqual([
          { kind: "view", id: "inbox" },
          { kind: "view", id: "processes", color: "#ef4444", icon: "ci" },
          { kind: "view", id: "tasks" },
        ]);
      }
    });

    it("keeps the first of several, with its colour, and drops the rest", () => {
      localStorage.setItem(
        key,
        JSON.stringify([
          { kind: "view", id: "templates", color: "#3b82f6" },
          { kind: "view", id: "inbox" },
          { kind: "view", id: "schedules", color: "#ef4444" },
          { kind: "view", id: "suites" },
        ]),
      );
      const items = loadPinnedItems();
      expect(items).toEqual([
        { kind: "view", id: "processes", color: "#3b82f6" },
        { kind: "view", id: "inbox" },
      ]);
      // ...and writes the migrated list back, once.
      expect(JSON.parse(localStorage.getItem(key))).toEqual(items);
    });

    it("is idempotent: a second load changes nothing and writes nothing", () => {
      localStorage.setItem(
        key,
        JSON.stringify([
          { kind: "view", id: "suites" },
          { kind: "board", id: "board-1" },
        ]),
      );
      const first = loadPinnedItems();
      const stored = localStorage.getItem(key);
      const setItem = vi.spyOn(Storage.prototype, "setItem");
      try {
        expect(loadPinnedItems()).toEqual(first);
        expect(setItem).not.toHaveBeenCalled();
      } finally {
        setItem.mockRestore();
      }
      expect(localStorage.getItem(key)).toBe(stored);
      expect(migratePinnedViews(first)).toEqual(first);
    });

    it("leaves a session or board that happens to share an old id alone", () => {
      const items = [
        { kind: "session", id: "schedules" },
        { kind: "board", id: "suites" },
      ];
      expect(migratePinnedViews(items)).toEqual(items);
    });

    it("offers Processes, not the three old views, to pin", () => {
      const ids = AVAILABLE_VIEWS.map((v) => v.id);
      expect(ids).toContain("processes");
      for (const old of ["schedules", "templates", "suites"]) {
        expect(ids).not.toContain(old);
      }
      expect(
        getPinnedItemInfo({ kind: "view", id: "processes" }),
      ).toMatchObject({ label: "Processes", glyph: "processes" });
    });
  });
});
