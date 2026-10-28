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
  BOARD_STORAGE_KEY,
  DEFAULT_COLUMNS,
  boardStates,
  columnOf,
  defaultColumns,
  groupIntoColumns,
  hiddenStates,
  loadColumns,
  newColumn,
  normalizeColumns,
  saveColumns,
} from "./board.js";

describe("board columns", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe("the default board", () => {
    // The two states that are not "where is everything right now": one
    // over with and one somebody put aside. Both are off the default
    // board, and both are two clicks away in the editor for whoever
    // disagrees.
    it("covers every state but the deferred and closed ones, once each", () => {
      const states = boardStates(defaultColumns());
      expect(new Set(states).size).toBe(states.length);
      expect(states).toContain("proposed");
      expect(states).toContain("running");
      expect(hiddenStates(defaultColumns())).toEqual(["deferred", "closed"]);
    });

    it("names the completed column 'Merging'", () => {
      expect(DEFAULT_COLUMNS.find((c) => c.id === "landing")).toEqual({
        id: "landing",
        title: "Merging",
        states: ["completed"],
      });
    });

    it("hands out a fresh copy, so an edit cannot leak into the next reset", () => {
      const first = defaultColumns();
      first[0].title = "Renamed";
      first[0].states.push("closed");
      expect(defaultColumns()[0].title).toBe("Proposed");
      expect(defaultColumns()[0].states).toEqual(["proposed"]);
    });
  });

  describe("normalizeColumns", () => {
    it("keeps a board it can read as-is", () => {
      const columns = [
        { id: "a", title: "Doing", states: ["running", "queued"] },
      ];
      expect(normalizeColumns(columns)).toEqual(columns);
    });

    it("migrates a stored 'Queued for merge' landing column to 'Merging'", () => {
      const columns = normalizeColumns([
        { id: "queued", title: "Waiting", states: ["queued"] },
        { id: "landing", title: "Queued for merge", states: ["completed"] },
      ]);
      expect(columns[1]).toEqual({
        id: "landing",
        title: "Merging",
        states: ["completed"],
      });
    });

    it("drops a state this build has never heard of", () => {
      const [column] = normalizeColumns([
        { id: "a", title: "Doing", states: ["running", "teleported"] },
      ]);
      expect(column.states).toEqual(["running"]);
    });

    it("leaves a state claimed twice in the first column that claimed it", () => {
      const columns = normalizeColumns([
        { id: "a", title: "Left", states: ["running"] },
        { id: "b", title: "Right", states: ["running", "queued"] },
      ]);
      expect(columns[0].states).toEqual(["running"]);
      expect(columns[1].states).toEqual(["queued"]);
    });

    it("names a column with no title of its own after the states it holds", () => {
      const [column] = normalizeColumns([
        { id: "a", title: "  ", states: ["awaiting_reply", "failed"] },
      ]);
      expect(column.title).toBe("Asked · Failed");
    });

    it("falls back to 'Untitled' for a column with neither a title nor a state", () => {
      const [column] = normalizeColumns([{ id: "a", title: "", states: [] }]);
      expect(column.title).toBe("Untitled");
      expect(column.states).toEqual([]);
    });

    it("gives colliding or missing ids one of their own", () => {
      const columns = normalizeColumns([
        { id: "a", title: "One", states: ["queued"] },
        { id: "a", title: "Two", states: ["running"] },
        { title: "Three", states: ["failed"] },
      ]);
      expect(new Set(columns.map((c) => c.id)).size).toBe(3);
    });

    it("reads anything that is not a board at all as nothing", () => {
      expect(normalizeColumns(null)).toBeNull();
      expect(normalizeColumns([])).toBeNull();
      expect(normalizeColumns("proposed")).toBeNull();
      expect(normalizeColumns([1, "two"])).toBeNull();
    });
  });

  describe("loading and saving", () => {
    it("starts on the default board with nothing stored", () => {
      expect(loadColumns()).toEqual(defaultColumns());
    });

    it("round-trips an edited board through localStorage", () => {
      const columns = [
        { id: "mine", title: "Mine", states: ["queued", "running"] },
      ];
      saveColumns(columns);
      expect(loadColumns()).toEqual(columns);
    });

    it("stores nothing for a board that is still the default", () => {
      saveColumns([{ id: "x", title: "X", states: ["queued"] }]);
      saveColumns(defaultColumns());
      expect(localStorage.getItem(BOARD_STORAGE_KEY)).toBeNull();
      expect(loadColumns()).toEqual(defaultColumns());
    });

    it("falls back to the default when the stored value is unreadable", () => {
      localStorage.setItem(BOARD_STORAGE_KEY, "{not json");
      expect(loadColumns()).toEqual(defaultColumns());
    });

    it("normalizes a stored board from a build that spelled states differently", () => {
      localStorage.setItem(
        BOARD_STORAGE_KEY,
        JSON.stringify([
          { id: "a", title: "Doing", states: ["running", "beamed_up"] },
        ]),
      );
      expect(loadColumns()).toEqual([
        { id: "a", title: "Doing", states: ["running"] },
      ]);
    });

    // A browser that refuses localStorage outright (private mode, a
    // storage policy) still has to show a board.
    it("survives a localStorage that throws", () => {
      const get = vi
        .spyOn(Storage.prototype, "getItem")
        .mockImplementation(() => {
          throw new Error("denied");
        });
      const set = vi
        .spyOn(Storage.prototype, "setItem")
        .mockImplementation(() => {
          throw new Error("denied");
        });
      expect(loadColumns()).toEqual(defaultColumns());
      expect(() =>
        saveColumns([{ id: "a", title: "A", states: ["queued"] }]),
      ).not.toThrow();
      get.mockRestore();
      set.mockRestore();
    });
  });

  describe("grouping tasks", () => {
    const columns = [
      { id: "wait", title: "Waiting", states: ["queued"] },
      { id: "doing", title: "Doing", states: ["running", "awaiting_reply"] },
    ];
    const tasks = [
      { id: 1, state: "queued" },
      { id: 2, state: "running" },
      { id: 3, state: "awaiting_reply" },
      { id: 4, state: "closed" },
    ];

    it("drops each task into the column that names its state", () => {
      const { cards } = groupIntoColumns(tasks, columns);
      expect(cards.get("wait").map((t) => t.id)).toEqual([1]);
      expect(cards.get("doing").map((t) => t.id)).toEqual([2, 3]);
    });

    it("counts the tasks no column has a place for", () => {
      expect(groupIntoColumns(tasks, columns).hidden).toBe(1);
    });

    it("keeps the order it was given within a column", () => {
      const reversed = [
        { id: 3, state: "awaiting_reply" },
        { id: 2, state: "running" },
      ];
      expect(
        groupIntoColumns(reversed, columns)
          .cards.get("doing")
          .map((t) => t.id),
      ).toEqual([3, 2]);
    });

    it("leaves out a task the caller's own filter rejects, without counting it as hidden", () => {
      const { cards, hidden } = groupIntoColumns(
        tasks,
        columns,
        (t) => t.id !== 2,
      );
      expect(cards.get("doing").map((t) => t.id)).toEqual([3]);
      expect(hidden).toBe(1);
    });

    it("gives every column an entry, empty ones included", () => {
      const { cards } = groupIntoColumns([], columns);
      expect([...cards.keys()]).toEqual(["wait", "doing"]);
      expect(cards.get("wait")).toEqual([]);
    });
  });

  it("columnOf names the column a task belongs to, or nothing", () => {
    const columns = defaultColumns();
    expect(columnOf(columns, { state: "running" }).id).toBe("running");
    expect(columnOf(columns, { state: "failed" }).id).toBe("failed");
    expect(columnOf(columns, { state: "awaiting_submit" }).id).toBe("asked");
    expect(columnOf(columns, { state: "closed" })).toBeNull();
  });

  it("newColumn is empty, titled and unique", () => {
    const a = newColumn();
    const b = newColumn();
    expect(a.states).toEqual([]);
    expect(a.title).toBe("New column");
    expect(a.id).not.toBe(b.id);
  });
});
