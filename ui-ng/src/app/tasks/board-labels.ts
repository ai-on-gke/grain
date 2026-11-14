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
