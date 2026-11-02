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

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ORDER_STORAGE_PREFIX,
  applyOrder,
  loadOrder,
  migrateProcessOrder,
  moveBefore,
  saveOrder,
} from "./listOrder.js";

const byName = (a, b) => a.name.localeCompare(b.name);
const items = [{ name: "alpha" }, { name: "beta" }, { name: "gamma" }];
const idOf = (i) => i.name;
const names = (list) => list.map(idOf);

describe("listOrder", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe("applyOrder", () => {
    it("leaves an undragged list in its own fallback order", () => {
      expect(names(applyOrder([], items, idOf, byName))).toEqual([
        "alpha",
        "beta",
        "gamma",
      ]);
    });

    it("puts the items the stored order names where it says", () => {
      const order = ["gamma", "alpha", "beta"];
      expect(names(applyOrder(order, items, idOf, byName))).toEqual(order);
    });

    // A repo added this morning, a template written after the last drag:
    // the order has never heard of it, so it goes after everything the
    // order does name rather than into the middle -- and among its own
    // kind it is still alphabetical.
    it("sorts items the stored order has never heard of last, by the fallback", () => {
      const order = ["gamma"];
      expect(names(applyOrder(order, items, idOf, byName))).toEqual([
        "gamma",
        "alpha",
        "beta",
      ]);
    });

    it("ignores stored ids that are gone", () => {
      const order = ["deleted", "beta", "alpha"];
      expect(names(applyOrder(order, items, idOf, byName))).toEqual([
        "beta",
        "alpha",
        "gamma",
      ]);
    });

    it("does not touch the array it is given", () => {
      const original = [...items];
      applyOrder(["gamma"], items, idOf, byName);
      expect(items).toEqual(original);
    });
  });

  describe("moveBefore", () => {
    const ids = ["a", "b", "c"];

    it("lands the dragged id immediately before the one it was dropped on", () => {
      expect(moveBefore(ids, "c", "a")).toEqual(["c", "a", "b"]);
      expect(moveBefore(ids, "c", "b")).toEqual(["a", "c", "b"]);
    });

    it("lands it at the end when the drop names no row", () => {
      expect(moveBefore(ids, "a", null)).toEqual(["b", "c", "a"]);
    });

    // The drop target can be a row a search is currently hiding from
    // this browser's own view -- or one another tab deleted -- in which
    // case there is nothing to land before and the end is the honest
    // answer.
    it("lands it at the end when the row it names is not in the order", () => {
      expect(moveBefore(ids, "a", "gone")).toEqual(["b", "c", "a"]);
    });

    it("does not touch the array it is given", () => {
      moveBefore(ids, "a", null);
      expect(ids).toEqual(["a", "b", "c"]);
    });
  });

  describe("storage", () => {
    it("round-trips an order through localStorage", () => {
      saveOrder("repos", ["b", "a"]);
      expect(localStorage.getItem(`${ORDER_STORAGE_PREFIX}repos`)).toBe(
        '["b","a"]',
      );
      expect(loadOrder("repos")).toEqual(["b", "a"]);
    });

    it("keeps one list's order apart from another's", () => {
      saveOrder("repos", ["b", "a"]);
      expect(loadOrder("templates")).toEqual([]);
    });

    it("clears the key rather than storing an empty order", () => {
      saveOrder("repos", ["a"]);
      saveOrder("repos", []);
      expect(localStorage.getItem(`${ORDER_STORAGE_PREFIX}repos`)).toBeNull();
    });

    it("reads a value that is not an order at all as no order", () => {
      localStorage.setItem(`${ORDER_STORAGE_PREFIX}repos`, "{not json");
      expect(loadOrder("repos")).toEqual([]);
      localStorage.setItem(`${ORDER_STORAGE_PREFIX}repos`, '{"a":1}');
      expect(loadOrder("repos")).toEqual([]);
      localStorage.setItem(`${ORDER_STORAGE_PREFIX}repos`, '["a",7,null]');
      expect(loadOrder("repos")).toEqual(["a"]);
    });

    // A browser that refuses localStorage outright (private mode, a
    // policy) still has to render the list -- board.js's own precedent.
    it("survives a localStorage that throws", () => {
      const getItem = vi
        .spyOn(Storage.prototype, "getItem")
        .mockImplementation(() => {
          throw new Error("denied");
        });
      const setItem = vi
        .spyOn(Storage.prototype, "setItem")
        .mockImplementation(() => {
          throw new Error("denied");
        });

      expect(loadOrder("repos")).toEqual([]);
      expect(() => saveOrder("repos", ["a"])).not.toThrow();

      getItem.mockRestore();
      setItem.mockRestore();
    });
  });

  // design/design/settings-panel.design.md §5.6.
  describe("migrateProcessOrder", () => {
    it("merges the three old orders into Processes, schedules first, and removes them", () => {
      saveOrder("suites", ["suite-2", "suite-1"]);
      saveOrder("schedules", ["sched-3", "sched-1"]);
      saveOrder("templates", ["template-9"]);
      migrateProcessOrder();
      expect(loadOrder("processes")).toEqual([
        "schedule:sched-3",
        "schedule:sched-1",
        "template:template-9",
        "suite:suite-2",
        "suite:suite-1",
      ]);
      for (const old of ["schedules", "templates", "suites"]) {
        expect(localStorage.getItem(`${ORDER_STORAGE_PREFIX}${old}`)).toBe(
          null,
        );
      }
      // Rows nobody placed follow the placed ones, by the default sort.
      const rows = [
        { key: "template:template-1", name: "a" },
        { key: "suite:suite-1", name: "b" },
        { key: "schedule:sched-1", name: "c" },
      ];
      expect(
        applyOrder(loadOrder("processes"), rows, (r) => r.key, byName).map(
          (r) => r.key,
        ),
      ).toEqual(["schedule:sched-1", "suite:suite-1", "template:template-1"]);
    });

    it("runs once, and leaves an existing Processes order alone", () => {
      saveOrder("processes", ["suite:suite-1"]);
      saveOrder("schedules", ["sched-1"]);
      migrateProcessOrder();
      expect(loadOrder("processes")).toEqual(["suite:suite-1"]);
      expect(loadOrder("schedules")).toEqual([]);
      migrateProcessOrder();
      expect(loadOrder("processes")).toEqual(["suite:suite-1"]);
    });
  });
});
