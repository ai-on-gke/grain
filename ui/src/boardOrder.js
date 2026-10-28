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

// boardOrder.js is where a board sits: which folder it is filed under
// and where in that folder it comes, and what has to be written to move
// it (grain/task-81).
//
// # Why a board's place is the one thing the UI writes directly
//
// Everything else about a board is edited by filing a task -- boards.js
// says why at length, and none of that changes here. A board's name, its
// query, its columns are content: somebody has to say what they should
// become, an agent writes the row, and the pull request that lands it is
// the record. A board's *place* is not content. It is a preference with
// no words in it: which of these five dashboards do I want at the top,
// and is this one a report or an everyday view. The answer is arrived at
// by looking at the rail and deciding it is wrong, it is worth nothing
// once said out loud ("put Flaky tests under Reports, second"), and the
// only way to check it is to look at the list again.
//
// That is a gesture, not a sentence, and it is the one part of a board
// that filing a task made genuinely worse: a five-second drag became a
// prompt, a dispatch, a review and a merge, so in practice nobody
// reordered anything and a board's place stayed wherever the row that
// created it happened to put it. So a drag writes `folder` and
// `orderKey` straight through PATCH /api/boards/{id}, which is the same
// row the state repository exports -- the change is in the next dump
// like any other, and an agent editing tables/board.json by hand is
// still writing the same two fields.
//
// # What a place is
//
// `{ folder, beforeId }` -- the folder to land in ("" for the loose
// boards above every folder), and the board to land immediately above,
// or null for the end of that folder. It is the shape the task list's
// own drop already speaks (Store.Reorder's afterID/beforeID, ReorderableList's
// `beforeId`), with the folder added because a board can be dropped into
// a section it did not come from.
//
// # Who computes the order key
//
// This file, in the browser, rather than a reorder endpoint of its own
// on the server. The task backlog has one (POST /api/tasks/reorder)
// because its order is load-bearing -- the dispatcher reads it, two
// clients can be dragging at once, and a key has to be split inside the
// transaction that reads its neighbours. A board's order decides the
// order of at most a few rows in one person's nav rail. PATCH already
// takes `orderKey`, so the whole move is one request against a field
// that already existed, and the failure mode of losing a race is that
// two boards end up in the wrong order until somebody drags one again.
//
// The keys themselves follow the store's own scheme (pkg/model/store.go):
// land halfway between the neighbours' keys, and when there is no gap
// left to halve -- which is the *normal* case for boards, since a row
// written by hand or by POST /api/boards carries `order_key` 0 and every
// board in a folder therefore has the same key -- renumber the folder on
// even spacing instead. That renumber is a few extra PATCHes on a list
// with single digits of rows in it, and it is what makes the first drag
// on a hand-written deployment do the obvious thing.
import { boardFolders } from "./boards.js";

// The gap left between two adjacent boards by a renumber, and the
// narrowest gap still worth halving. Both are pkg/model/store.go's own
// values (orderKeySpacing, minOrderKeyGap) and are here for the same
// reason: 2^20 is wide enough that ordinary dragging never renumbers,
// and 1e-6 is far enough above float64's resolution that a halved gap
// is always a distinct number.
export const BOARD_ORDER_SPACING = 1 << 20;
const MIN_ORDER_KEY_GAP = 1e-6;

// boardSections is the rail's own grouping with one addition: the loose
// section is always there, even when every board is filed under a
// folder. It has to be, because it is the only way back out of one --
// with nothing rendered above the first folder heading there would be no
// place to drop a board that should stop being in Reports.
export function boardSections(boards) {
  const { loose, folders } = boardFolders(boards);
  return [{ folder: "", boards: loose }, ...folders];
}

// placeKey identifies a place, for a Set of the ones a picked-up board
// is being offered. A board id never contains a newline and a folder is
// a single field, so the two cannot run together into one another.
export function placeKey(place) {
  return `${place.folder}\n${place.beforeId ?? ""}`;
}

// samePlace answers "is this where the board already is?". Landing there
// writes nothing, so it is the one place the gesture does not offer and
// the one drop that is quietly ignored.
//
// Note that the identity place is expressed in terms of the board's
// *successor*, not itself: with the board lifted out, "where it was" is
// the gap above whatever followed it, or the end of its folder when
// nothing did.
export function samePlace(boards, id, place) {
  const board = boards.find((b) => b.id === id);
  if (!board) return false;
  if (place.folder !== board.folder) return false;
  // Dropped onto itself: the same gap, said the other way round.
  if (place.beforeId === id) return true;
  const siblings = boards.filter((b) => b.folder === board.folder);
  const at = siblings.findIndex((b) => b.id === id);
  const next = at >= 0 ? siblings[at + 1] : undefined;
  return (place.beforeId ?? null) === (next ? next.id : null);
}

// boardPlaces is every place one board could be moved to, its own
// excluded: a gap above each other board, and the end of each section
// including the loose one. It is what tells the tap-to-move gesture
// which slots to draw, and what tells a board with nowhere to go (the
// only board there is) that its handle should be disabled rather than
// picking up a row and then offering nothing.
export function boardPlaces(boards, id) {
  if (!boards.some((b) => b.id === id)) return [];
  const out = [];
  for (const section of boardSections(boards)) {
    for (const b of section.boards) {
      if (b.id !== id) out.push({ folder: section.folder, beforeId: b.id });
    }
    out.push({ folder: section.folder, beforeId: null });
  }
  return out.filter((p) => !samePlace(boards, id, p));
}

// keyBetween is the order key for a board landing between two others, or
// null when the pair leaves no room and the folder has to be renumbered
// first. An unbounded side steps a whole spacing past its neighbour, so
// the first board dropped at the head of a folder does not have to share
// a key with anything.
function keyBetween(lower, upper) {
  if (lower === null && upper === null) return 0;
  if (lower === null) return upper - BOARD_ORDER_SPACING;
  if (upper === null) return lower + BOARD_ORDER_SPACING;
  if (upper - lower >= MIN_ORDER_KEY_GAP) return lower + (upper - lower) / 2;
  return null;
}

// boardMoves turns one drop into the PATCHes that make it true: usually
// exactly one, `{ id, folder, orderKey }` for the board that moved, and
// on a folder whose keys leave no gap to split, one per board whose key
// the renumber changes as well.
//
// `boards` must be the list as the server returned it (by folder, then
// order key, then id), because that -- not the caller's own idea of the
// order -- is what the renumber writes down.
//
// A move that would land where the board already is returns nothing at
// all, so the caller can treat "no patches" as "nothing to do" rather
// than having to test for a no-op drop itself.
export function boardMoves(boards, id, place) {
  const board = boards.find((b) => b.id === id);
  if (!board) return [];
  const folder = typeof place?.folder === "string" ? place.folder : "";
  const beforeId = place?.beforeId ?? null;
  if (samePlace(boards, id, { folder, beforeId })) return [];

  // The folder being landed in, as it will be once the board is out of
  // wherever it was -- which is the same list whether or not it is
  // moving between folders.
  const rest = boards.filter((b) => b.folder === folder && b.id !== id);
  let at =
    beforeId === null ? rest.length : rest.findIndex((b) => b.id === beforeId);
  // A beforeId this folder does not hold means the caller's view of the
  // list was stale (a board was moved or deleted out from under it). The
  // end of the folder is the honest answer: it is a place that certainly
  // exists, and the alternative is refusing a drag over something the
  // person who dragged it cannot see.
  if (at < 0) at = rest.length;

  const key = keyBetween(
    at > 0 ? rest[at - 1].orderKey : null,
    at < rest.length ? rest[at].orderKey : null,
  );
  if (key !== null) return [{ id, folder, orderKey: key }];

  // No gap to split: give the whole folder fresh keys on even spacing,
  // with the board in its new position, and write back only the rows
  // whose key actually changes.
  const next = [...rest.slice(0, at), board, ...rest.slice(at)];
  const moves = [];
  next.forEach((b, i) => {
    const orderKey = i * BOARD_ORDER_SPACING;
    if (b.id === id) moves.push({ id, folder, orderKey });
    else if (b.orderKey !== orderKey) moves.push({ id: b.id, orderKey });
  });
  return moves;
}
