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

import type { Board } from "../api/models";

// boardLabels names each saved board so that two boards sharing a name
// can still be told apart: a unique name stays as written, a name that
// appears more than once carries where it is filed ("Triage, in Ops" /
// "Triage, outside every folder"), and two of one name in one folder
// fall back to the board id as well. It is the classic UI's boardLabels
// (ui/src/boards.js) word for word, so a screen reader hears the same
// name for a board on /boards and in the /next/ Boards menu. Without it
// the menu offered three "Triage" buttons and three "Delete board
// Triage" buttons (2026-11-13 saved boards walkthrough, task 5385).
export function boardLabels(
  boards: readonly Board[],
): ReadonlyMap<string, string> {
  const nameCounts = new Map<string, number>();
  const pairCounts = new Map<string, number>();
  for (const b of boards) {
    nameCounts.set(b.name, (nameCounts.get(b.name) ?? 0) + 1);
    const pair = `${b.folder ?? ""}\0${b.name}`;
    pairCounts.set(pair, (pairCounts.get(pair) ?? 0) + 1);
  }
  const out = new Map<string, string>();
  for (const b of boards) {
    const folder = b.folder ?? "";
    if ((nameCounts.get(b.name) ?? 0) <= 1) {
      out.set(b.id, b.name);
      continue;
    }
    const where = folder ? `in ${folder}` : "outside every folder";
    if ((pairCounts.get(`${folder}\0${b.name}`) ?? 0) > 1) {
      out.set(b.id, `${b.name} (${b.id}), ${where}`);
    } else {
      out.set(b.id, `${b.name}, ${where}`);
    }
  }
  return out;
}

// NEXT_BOARD_PARAMS are the parts of a board's query /next/tasks can
// apply: the ones its own Save current view writes (currentBoardQuery).
const NEXT_BOARD_PARAMS = new Set(["tab", "group", "q", "repo", "view"]);

// nextBoardParams is what applying a board on /next/tasks navigates
// with, or null when /next/tasks cannot show the board as it was saved
// -- a Kanban, timeline, report, dashboard or tree board, or a list
// narrowed in the classic grammar (`f=state:any:queued`, `sort=`) --
// and the board's own classic page has to open instead. Applying one of
// those here used to leave the reader on the bare task list with
// nothing changed, so a board called "Report" or "Running only" quietly
// showed every task as a list (2026-11-17 saved boards walkthrough).
export function nextBoardParams(
  b: Board,
): Record<string, string | null> | null {
  if ((b.kind || "list") !== "list") return null;
  const p = new URLSearchParams(b.query || "");
  let foreign = false;
  p.forEach((_, k) => {
    if (!NEXT_BOARD_PARAMS.has(k)) foreign = true;
  });
  if (foreign) return null;
  return {
    tab: p.get("tab") || null,
    group: p.get("group") || null,
    q: p.get("q") || null,
    repo: p.get("repo") || null,
    view: p.get("view") === "timeline" ? "timeline" : null,
  };
}

// classicBoardHint is the word shown beside a board that opens on the
// classic Boards page: what it is, in the classic Boards index's words.
export function classicBoardHint(b: Board): string {
  switch (b.kind) {
    case "gantt":
      return "timeline";
    case "panels":
      return "dashboard";
    case "kanban":
    case "report":
    case "tree":
      return b.kind;
    default:
      return "filtered list";
  }
}

// canonicalNextBoardQuery is the URLSearchParams encoding /next/tasks writes
// for a board it can draw, or null when the board opens on its classic page.
// Comparing active boards through this encoding keeps a classic Kanban or
// Report board sharing a ?q= or ?repo= from claiming to be active on
// /next/tasks, and still recognizes a list board whose parameters were saved
// in a different order (2026-11-19 saved boards walkthrough).
export function canonicalNextBoardQuery(b: Board): string | null {
  const params = nextBoardParams(b);
  if (!params) return null;
  const out = new URLSearchParams();
  if (params["tab"] && params["tab"] !== "backlog") {
    out.set("tab", params["tab"]);
  }
  if (params["group"] && params["group"] !== "none") {
    out.set("group", params["group"]);
  }
  const q = (params["q"] || "").trim();
  if (q) out.set("q", q);
  const repo = (params["repo"] || "").trim();
  if (repo) out.set("repo", repo);
  if (params["view"] === "timeline") out.set("view", "timeline");
  return out.toString();
}
