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

// panelOrder.js is where a panel sits on its dashboard, and what has to
// be written to move it (grain/task-368).
//
// # Why a panel's place is dragged rather than asked for
//
// boardOrder.js already argues this in full for boards, and every line
// of it transfers: a panel's place is a preference with no words in it,
// arrived at by looking at the dashboard and deciding it is wrong, worth
// nothing once said out loud ("put the timeline under the state
// counts"), and checkable only by looking again. It is the one edit to a
// dashboard with no judgement in it, and filing a task for it turns a
// five-second gesture into a prompt, a dispatch, a review and a merge --
// so in practice nobody reorders anything and a dashboard keeps whatever
// order the rows that created it happened to take.
//
// On a phone that order is the *whole* layout: every panel is full width
// and stacked in the board's own order (style.css's panel grid), so
// "which of these six do I want to see first" has no other answer there.
//
// Everything else about a dashboard is still a task, for the reason
// boards.js gives at length: a panel's kind, title, query and width are
// content somebody has to say, and the pull request that lands them is
// the record. This file is the exception the place has always been.
//
// # What a place is
//
// A panel key, or null. The key is normalizePanel's own `panel-N`
// (panels.js): a panel row has no id anybody named -- the `id` in the
// dump is the store's autoincrement -- so what is dragged is identified
// by where it sits in the board's list, and a place is "immediately
// above that one", or the end of the dashboard. It is what
// ReorderableList's drop already means; boardOrder's `{ folder,
// beforeId }` pair has no counterpart here, because a panel belongs to
// exactly one board and dragging it cannot change that.
//
// The keys are positions, so they shuffle the moment a move lands. That
// is why App writes and then re-reads rather than applying the move
// optimistically -- boardOrder.js's own treatment, and more clearly
// right here, since the name of the thing that moved changes with it.
//
// # What the write is
//
// The board's whole `panels` array, through the PATCH that already
// exists: PATCH /api/boards/{id} takes `panels` and replaces the lot
// (pkg/ui/boards.go). There is no narrower write and this deliberately
// does not add one, because a narrower one would buy nothing:
// model.Store's PutBoard deletes every `board_panel` row of the board
// and re-inserts it whatever changed (pkg/model/board_store.go), so each
// row's autoincrement `id` moves and the state repository's next dump of
// tables/board_panel.json rewrites every panel of that dashboard --
// under a whole-array PATCH, under a one-field one, and under a reorder
// endpoint of its own alike. The churn is the store's, not the API's.
//
// What is left is honest last-writer-wins over a handful of rows, which
// is what boardOrder.js settled for and for the same reason: the other
// writer of these rows is the state repository's importer landing a
// merged pull request, a reorder loses to one by seconds either way, and
// the failure is that a dashboard is in the wrong order until somebody
// drags a panel again.
//
// Sending the array is lossless because a normalized panel carries the
// row it came from (panels.js's `stored`, and panelRow, which is the
// shape that goes over the wire).
//
// # The keys themselves
//
// The store's own scheme, exactly as boardOrder.js uses it: land halfway
// between the neighbours' keys, and renumber on even spacing when there
// is no gap left to halve -- which is the normal case for a dashboard
// whose panels were written all at once, since PutBoard numbers an
// unnumbered set 0, 1, 2 and there is room to halve but not much of it.
// A dashboard hand-numbered 10/20/30 in the state repository survives:
// nothing renumbers it until a drag needs room it has not got.

import { panelRow } from "./panels.js";
import {
  CHILD_ORDER_SPACING,
  childMove,
  childPlaces,
  sameChildPlace,
} from "./childOrder.js";

// The gap a renumber leaves between two panels, and the narrowest gap
// still worth halving -- pkg/model/store.go's own orderKeySpacing and
// minOrderKeyGap, the same pair boardOrder.js quotes and for the same
// reasons: 2^20 is wide enough that ordinary dragging never renumbers,
// and 1e-6 is far enough above float64's resolution that a halved gap
// is always a distinct number.
export const PANEL_ORDER_SPACING = CHILD_ORDER_SPACING;

// samePlace answers "is this where the panel already is?" -- the one
// drop that writes nothing, so the one place the gesture does not offer
// and the one drop that is quietly ignored.
//
// Said in terms of the panel's *successor*, boardOrder.js's own
// reasoning: with the panel lifted out, "where it was" is the gap above
// whatever followed it, or the end of the dashboard when nothing did.
export function samePlace(panels, key, beforeKey) {
  return sameChildPlace(panels, key, beforeKey);
}

// panelPlaces is every place one panel could be moved to, its own
// excluded: the gap above each other panel, and the end of the
// dashboard. It is what tells the tap-to-move gesture which slots to
// draw, and what tells the only panel on a dashboard that its handle
// should be disabled rather than picking a panel up and then offering
// nowhere to put it.
export function panelPlaces(panels, key) {
  return childPlaces(panels, key);
}

// panelMove turns one drop into the `panels` array that makes it true --
// every panel of the board, in its new order, each carrying the key it
// is to be stored under -- or null when there is nothing to do, so a
// caller can treat that as "no request" rather than having to test for a
// no-op drop itself.
//
// `panels` must be the board's panels as the server returned them, in
// display order (model.ListBoards orders them by key), because that --
// not the caller's own idea of the order -- is what a renumber writes
// down.
//
// Usually only the moved panel's key changes and every other row goes
// back exactly as it came. When the gap it is landing in leaves no room
// to split, the whole dashboard is renumbered on even spacing instead;
// that is one request either way, since the array is the unit the API
// takes.
export function panelMove(panels, key, beforeKey) {
  return childMove(panels, key, beforeKey, panelRow);
}
