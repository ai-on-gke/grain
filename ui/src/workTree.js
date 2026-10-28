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

// workTree.js is the tree of work as the UI holds it: tasks nested under
// the task whose run filed them (docs/work-trees.md). The task page's
// own tree comes from the daemon whole (GET /api/tasks/{id}/tree,
// model.Store.WorkTree -- the same tree, in the same words, as the
// progress comment grain keeps on a GitHub origin); the Tree board draws
// the narrowed backlog the same way from the task rows it already holds,
// each row's `generatedFrom` being its parent.
import { stateLabel, taskBadgeState } from "./state.js";

// compareIds orders task ids the way they were filed: numerically, with
// anything that is not a number after the ones that are.
export function compareIds(a, b) {
  const x = Number(a);
  const y = Number(b);
  if (Number.isFinite(x) && Number.isFinite(y)) return x - y;
  if (Number.isFinite(x)) return -1;
  if (Number.isFinite(y)) return 1;
  return String(a).localeCompare(String(b));
}

// forestFromTasks nests task rows under the task that filed each one,
// when that task is among the rows too; the rest are roots, the newest
// first -- the board is read for what is going on now -- with each
// task's follow-ups under it in the order they were filed.
//
// A node is the shape TaskTree draws: the same as one of the daemon's
// own WorkTreeNodes, with the row's state beside it for the dot.
//
// `ordered` says the rows already arrive in an order the reader chose
// (a Tree board's sort, taskFilters.js's narrowTasks): then roots and
// siblings keep that order rather than this one, or a sort the board's
// toolbar offers -- and writes into the URL -- would change nothing.
export function forestFromTasks(tasks, { ordered = false } = {}) {
  const rows = tasks || [];
  const ids = new Set(rows.map((t) => t.id));
  const children = new Map();
  const roots = [];
  for (const t of rows) {
    const parent = t.generatedFrom;
    if (parent && parent !== t.id && ids.has(parent)) {
      if (!children.has(parent)) children.set(parent, []);
      children.get(parent).push(t);
    } else {
      roots.push(t);
    }
  }
  // A cycle of rows filing each other has no root to hang from; it is
  // drawn from its lowest id rather than dropped.
  const seen = new Set();
  const node = (t) => {
    seen.add(t.id);
    let kids = (children.get(t.id) || []).filter((c) => !seen.has(c.id));
    if (!ordered) kids = kids.sort((a, b) => compareIds(a.id, b.id));
    return {
      id: t.id,
      title: t.title,
      status: stateLabel(t),
      state: taskBadgeState(t),
      pullRequest: t.pullRequest || "",
      done: t.state === "completed" || t.state === "closed",
      children: kids.map(node),
    };
  };
  if (!ordered) roots.sort((a, b) => compareIds(b.id, a.id));
  const out = roots.map(node);
  for (const t of [...rows].sort((a, b) => compareIds(a.id, b.id))) {
    if (!seen.has(t.id)) out.push(node(t));
  }
  return out;
}

// doneLine is a tree's "n of m done", as the progress comment says it.
export function doneLine(tree) {
  return `${tree.done} of ${tree.total} done`;
}
