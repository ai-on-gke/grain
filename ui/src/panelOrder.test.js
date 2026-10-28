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

import { describe, expect, it } from "vitest";
import {
  PANEL_ORDER_SPACING,
  panelMove,
  panelPlaces,
  samePlace,
} from "./panelOrder.js";
import { normalizePanels } from "./panels.js";

// The panels as the board hands them over: normalized, in the order the
// server returned them (model.ListBoards orders them by key), each
// carrying the `panel-N` key that names it. Everything here starts from
// normalizePanels rather than from hand-built objects, because the row a
// move writes back is the one normalizePanel kept (panels.js's `stored`)
// and a test building its own would not be exercising that at all.
const dashboard = (...panels) => normalizePanels(panels);

// A dashboard as PutBoard leaves one whose panels arrived unnumbered:
// keys 0, 1, 2, which is what "nobody has ever dragged this" looks like
// in the store.
const counted = dashboard(
  { kind: "gantt", title: "This week", width: "full", orderKey: 0 },
  { kind: "states", title: "Where everything is", orderKey: 1 },
  { kind: "list", title: "Live work", query: "q=sandbox", orderKey: 2 },
);

// ...and one hand-numbered in the state repository, with room to slot a
// panel between any two of them.
const spaced = dashboard(
  { kind: "gantt", title: "This week", width: "full", orderKey: 10 },
  { kind: "states", title: "Where everything is", orderKey: 20 },
  { kind: "list", title: "Live work", orderKey: 30 },
);

describe("samePlace", () => {
  it("knows the gap a panel is already in", () => {
    // Above its own successor, and dropped onto itself: the same gap
    // said two ways.
    expect(samePlace(counted, "panel-0", "panel-1")).toBe(true);
    expect(samePlace(counted, "panel-0", "panel-0")).toBe(true);
    // The last panel's own place is the end of the dashboard.
    expect(samePlace(counted, "panel-2", null)).toBe(true);
    expect(samePlace(counted, "panel-0", null)).toBe(false);
    expect(samePlace(counted, "panel-2", "panel-1")).toBe(false);
  });
});

describe("panelPlaces", () => {
  it("offers every gap but the one the panel is in", () => {
    expect(panelPlaces(counted, "panel-0")).toEqual(["panel-2", null]);
    expect(panelPlaces(counted, "panel-1")).toEqual(["panel-0", null]);
    expect(panelPlaces(counted, "panel-2")).toEqual(["panel-0", "panel-1"]);
  });

  // What tells the handle on the only panel of a dashboard to be
  // disabled rather than picking it up and then offering nothing.
  it("offers nothing to the only panel there is", () => {
    expect(panelPlaces(dashboard({ kind: "list" }), "panel-0")).toEqual([]);
  });

  it("offers nothing for a panel this dashboard does not hold", () => {
    expect(panelPlaces(counted, "panel-7")).toEqual([]);
  });
});

describe("panelMove", () => {
  // The whole array, every time: PATCH /api/boards/{id} replaces the
  // panels wholesale, so a move that sent only what changed would delete
  // the rest.
  it("returns every panel, in the new order, as rows the API takes", () => {
    expect(panelMove(spaced, "panel-2", "panel-1")).toEqual([
      {
        kind: "gantt",
        title: "This week",
        query: "",
        range: "",
        width: "full",
        orderKey: 10,
      },
      {
        kind: "list",
        title: "Live work",
        query: "",
        range: "",
        width: "",
        orderKey: 15,
      },
      {
        kind: "states",
        title: "Where everything is",
        query: "",
        range: "",
        width: "",
        orderKey: 20,
      },
    ]);
  });

  it("writes nothing for a drop where the panel already is", () => {
    expect(panelMove(spaced, "panel-0", "panel-1")).toBeNull();
    expect(panelMove(spaced, "panel-0", "panel-0")).toBeNull();
    expect(panelMove(spaced, "panel-2", null)).toBeNull();
  });

  it("writes nothing for a panel this dashboard does not hold", () => {
    expect(panelMove(spaced, "panel-7", null)).toBeNull();
  });

  // A key the caller's view has and this dashboard has not: a panel
  // moved or removed out from under it. The end is a place that
  // certainly exists.
  it("drops at the end when the panel to land above is gone", () => {
    const rows = panelMove(spaced, "panel-0", "panel-9");
    expect(rows.map((r) => r.title)).toEqual([
      "Where everything is",
      "Live work",
      "This week",
    ]);
    expect(rows[2].orderKey).toBe(30 + PANEL_ORDER_SPACING);
  });

  // Only the panel that moved is given a new key: the point of an order
  // key is that moving one row does not renumber the rest, and the state
  // repository's next dump of this board should be one line different.
  it("leaves every other panel's key exactly as it was", () => {
    const rows = panelMove(spaced, "panel-0", "panel-2");
    expect(rows.map((r) => r.orderKey)).toEqual([20, 25, 30]);
  });

  describe("when there is no room to halve", () => {
    it("steps below zero when landing ahead of a zero key", () => {
      const rows = panelMove(counted, "panel-2", "panel-0");
      expect(rows.map((r) => r.title)).toEqual([
        "Live work",
        "This week",
        "Where everything is",
      ]);
      expect(rows.map((r) => r.orderKey)).toEqual([-PANEL_ORDER_SPACING, 0, 1]);
    });

    it("renumbers a dashboard whose panels all share a key", () => {
      const flat = dashboard(
        { kind: "list", title: "One" },
        { kind: "list", title: "Two" },
        { kind: "list", title: "Three" },
      );
      const rows = panelMove(flat, "panel-2", "panel-1");
      expect(rows.map((r) => r.title)).toEqual(["One", "Three", "Two"]);
      expect(rows.map((r) => r.orderKey)).toEqual([
        0,
        PANEL_ORDER_SPACING,
        2 * PANEL_ORDER_SPACING,
      ]);
    });
  });

  // A panel whose kind this build has never heard of is *drawn* as a
  // list (panels.js) and must not be *written* as one: a reorder rewrites
  // every row of the board, and a fallback meant for a render would
  // otherwise become the row. The server refuses the unknown kind, so
  // the move fails visibly instead.
  it("writes back the words the row actually carried", () => {
    const newer = dashboard(
      { kind: "burndown", title: "From a later build", width: "quarter" },
      { kind: "list", title: "Live work" },
    );
    expect(newer[0].kind).toBe("list");
    expect(panelMove(newer, "panel-1", "panel-0")[1]).toEqual({
      kind: "burndown",
      title: "From a later build",
      query: "",
      range: "",
      width: "quarter",
      orderKey: 0,
    });
  });
});
