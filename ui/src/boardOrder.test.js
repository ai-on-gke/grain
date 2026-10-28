import { describe, expect, it } from "vitest";
import {
  BOARD_ORDER_SPACING,
  boardMoves,
  boardPlaces,
  boardSections,
  samePlace,
} from "./boardOrder.js";

// The list as the server returns it: by folder, then by order key, then
// by id (model.ListBoards). Every test here starts from a list in that
// order, because that is what boardMoves is entitled to assume.
const board = (id, name, folder, orderKey) => ({
  id,
  name,
  folder,
  kind: "list",
  description: "",
  query: "",
  columns: [],
  orderKey,
});

// Two loose boards and two in a folder, on keys with room between them.
const spaced = [
  board("board-1", "Waiting on me", "", 0),
  board("board-2", "Everything", "", 1000),
  board("board-3", "Flaky tests", "Reports", 0),
  board("board-4", "Slow tasks", "Reports", 1000),
];

// The shape a deployment nobody has ever dragged is actually in: every
// row written with order_key 0, so the list is really in id order and no
// two keys have a gap between them.
const flat = [
  board("board-1", "Waiting on me", "", 0),
  board("board-2", "Everything", "", 0),
  board("board-3", "Flaky tests", "Reports", 0),
];

describe("boardSections", () => {
  it("puts the loose boards above the folders", () => {
    expect(boardSections(spaced).map((s) => s.folder)).toEqual(["", "Reports"]);
    expect(boardSections(spaced)[0].boards.map((b) => b.id)).toEqual([
      "board-1",
      "board-2",
    ]);
  });

  // Without it there is no way back out of a folder: the section is the
  // only thing standing for "not filed anywhere".
  it("keeps an empty loose section when every board is in a folder", () => {
    const sections = boardSections([spaced[2], spaced[3]]);
    expect(sections[0]).toEqual({ folder: "", boards: [] });
    expect(sections[1].folder).toBe("Reports");
  });
});

describe("samePlace", () => {
  it("knows the gap a board is already in", () => {
    // board-1 sits above board-2, so "above board-2" is where it is.
    expect(
      samePlace(spaced, "board-1", { folder: "", beforeId: "board-2" }),
    ).toBe(true);
    // ...and the last board of a folder is at that folder's end.
    expect(samePlace(spaced, "board-2", { folder: "", beforeId: null })).toBe(
      true,
    );
    expect(samePlace(spaced, "board-1", { folder: "", beforeId: null })).toBe(
      false,
    );
  });

  it("treats a board dropped on itself as where it already is", () => {
    expect(
      samePlace(spaced, "board-1", { folder: "", beforeId: "board-1" }),
    ).toBe(true);
  });

  it("is never the same place in another folder", () => {
    expect(
      samePlace(spaced, "board-2", { folder: "Reports", beforeId: null }),
    ).toBe(false);
  });
});

describe("boardPlaces", () => {
  it("offers every gap but the one the board is in", () => {
    expect(boardPlaces(spaced, "board-1")).toEqual([
      // Not "above board-2": that is where board-1 already is.
      { folder: "", beforeId: null },
      { folder: "Reports", beforeId: "board-3" },
      { folder: "Reports", beforeId: "board-4" },
      { folder: "Reports", beforeId: null },
    ]);
  });

  // A single board in a folder can still be moved -- out of it.
  it("offers the top level to the only board in a folder", () => {
    const only = [board("board-3", "Flaky tests", "Reports", 0)];
    expect(boardPlaces(only, "board-3")).toEqual([
      { folder: "", beforeId: null },
    ]);
  });

  // ...and the only board there is has nowhere at all to go, which is
  // what disables its handle rather than picking it up and offering
  // nothing.
  it("offers nothing to the only board in a deployment with no folders", () => {
    const only = [board("board-1", "Waiting on me", "", 0)];
    expect(boardPlaces(only, "board-1")).toEqual([]);
  });

  it("offers nothing when the picked board is no longer in the list", () => {
    expect(boardPlaces(spaced, "board-missing")).toEqual([]);
  });
});

describe("boardMoves", () => {
  it("lands halfway between two neighbours", () => {
    expect(
      boardMoves(spaced, "board-3", { folder: "", beforeId: "board-2" }),
    ).toEqual([{ id: "board-3", folder: "", orderKey: 500 }]);
  });

  it("steps a whole spacing past the end of a folder", () => {
    expect(
      boardMoves(spaced, "board-1", { folder: "Reports", beforeId: null }),
    ).toEqual([
      {
        id: "board-1",
        folder: "Reports",
        orderKey: 1000 + BOARD_ORDER_SPACING,
      },
    ]);
  });

  it("steps a whole spacing below the head of a folder", () => {
    expect(
      boardMoves(spaced, "board-4", { folder: "", beforeId: "board-1" }),
    ).toEqual([{ id: "board-4", folder: "", orderKey: -BOARD_ORDER_SPACING }]);
  });

  it("gives a board dropped into an empty top level a key of its own", () => {
    const filed = [board("board-3", "Flaky tests", "Reports", 7)];
    expect(
      boardMoves(filed, "board-3", { folder: "", beforeId: null }),
    ).toEqual([{ id: "board-3", folder: "", orderKey: 0 }]);
  });

  it("writes nothing for a drop where the board already is", () => {
    expect(
      boardMoves(spaced, "board-1", { folder: "", beforeId: "board-2" }),
    ).toEqual([]);
    expect(
      boardMoves(spaced, "board-2", { folder: "", beforeId: null }),
    ).toEqual([]);
  });

  it("writes nothing for a board that is not there", () => {
    expect(
      boardMoves(spaced, "board-9", { folder: "", beforeId: null }),
    ).toEqual([]);
  });

  // The case a deployment nobody has dragged is actually in: every key
  // is 0, so a board dropped *between* two of them has no gap to land
  // in and the folder is renumbered on even spacing instead. Only the
  // rows whose key actually changes are written -- board-1 is already
  // where a renumber would put it.
  it("renumbers a folder whose keys leave no gap to split", () => {
    expect(
      boardMoves(flat, "board-3", { folder: "", beforeId: "board-2" }),
    ).toEqual([
      { id: "board-3", folder: "", orderKey: BOARD_ORDER_SPACING },
      { id: "board-2", orderKey: 2 * BOARD_ORDER_SPACING },
    ]);
  });

  // The same all-zero folder, dropped at an end rather than between two
  // rows: there is only one neighbour, so a whole spacing past it is
  // room enough and nothing else is rewritten.
  it("needs no renumber at the ends of a folder that shares one key", () => {
    expect(
      boardMoves(flat, "board-2", { folder: "", beforeId: "board-1" }),
    ).toEqual([{ id: "board-2", folder: "", orderKey: -BOARD_ORDER_SPACING }]);
  });

  // A view that has gone stale -- the neighbour it was dropped above has
  // since been moved or deleted -- lands at the end of the folder rather
  // than refusing the drag.
  it("falls back to the end of the folder for an unknown neighbour", () => {
    expect(
      boardMoves(spaced, "board-1", { folder: "Reports", beforeId: "board-9" }),
    ).toEqual([
      {
        id: "board-1",
        folder: "Reports",
        orderKey: 1000 + BOARD_ORDER_SPACING,
      },
    ]);
  });
});
