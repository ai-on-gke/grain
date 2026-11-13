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
import { type TabId, TABS, isTabId } from "./task-view";

// board-query.ts translates between /next/tasks' own view (a tab, a
// search, a repository and a grouping) and the saved-board query
// grammar the classic boards and pkg/model's Board.Query speak:
//
//   q=<search>&sort=<attr>:<asc|desc>&f=<attr>:<op>:<v1>|<v2>&...
//
// (ui/src/paths.js's parseNarrowing and queryFromNarrowing). A board is
// one row on the deployment, read by both UIs, so it has to mean one
// thing: a board saved from /next/ writes that grammar -- its tab as a
// condition on state, its repository as a condition on repo -- and a
// board applied on /next/ is read in it. /next/ used to write and read
// tab=&group=&q=&repo= instead, so a classic board opened on /next/ as
// an unnarrowed Backlog and a /next/ board lost its tab on classic
// (2026-11-13 saved boards walkthrough, task 5385, §2.8 and §4).
//
// What /next/tasks cannot show exactly -- a Kanban, Timeline, Tree,
// Panels or Report board, a sort, a condition on any attribute but state
// and repo, a set of states no tab lists -- is not approximated: the
// plan says the board opens on the classic page, and the Boards menu
// links it there (/boards/<id>) rather than opening a list that is
// silently wider than the board.

// BoardView is the part of /next/tasks' address a board carries.
// group is presentation only: classic ignores the key, and it is kept
// so a board saved grouped reopens grouped here.
export interface BoardView {
  tab: TabId;
  q: string;
  repo: string;
  group: "none" | "workstream" | "repo";
}

export type BoardPlan =
  { open: "here"; view: BoardView } | { open: "classic"; why: string };

// The kinds pkg/model's BoardKind has, by the word the classic UI shows
// for each (ui/src/boards.js BOARD_KINDS). Anything else is a list, as
// classic's normalizeBoard reads it.
const KIND_LABELS: Readonly<Record<string, string>> = {
  kanban: "Kanban",
  gantt: "Timeline",
  panels: "Panels",
  report: "Report",
  tree: "Tree",
};

// boardKindLabel is the word for a board's kind, "List" for a list.
export function boardKindLabel(kind: string | undefined): string {
  return KIND_LABELS[kind ?? ""] ?? "List";
}

// The "every open task" tab is the only one not named by a state list.
const ALL_OPEN = { op: "none", values: ["closed"] } as const;

// classic's SEPARATORS (ui/src/paths.js): escaped inside a value.
const SEPARATORS = /[%:|,]/g;
const escapeValue = (v: string): string =>
  v.replace(
    SEPARATORS,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );
const unescapeValue = (v: string): string =>
  v.replace(/%([0-9A-Fa-f]{2})/g, (_, hex: string) =>
    String.fromCharCode(parseInt(hex, 16)),
  );

interface Condition {
  attr: string;
  op: string;
  values: string[];
}

// conditionOf is classic's conditionFromQuery (ui/src/taskFilters.js).
function conditionOf(raw: string): Condition | null {
  const first = raw.indexOf(":");
  const second = raw.indexOf(":", first + 1);
  if (first < 1 || second < 0) return null;
  const op = raw.slice(first + 1, second);
  if (op === "") return null;
  const rest = raw.slice(second + 1);
  return {
    attr: raw.slice(0, first),
    op,
    values: rest === "" ? [] : rest.split("|").map(unescapeValue),
  };
}

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x) => b.includes(x));
}

// tabForStates is the tab a state condition names, or null when no tab
// lists exactly those states.
function tabForState(c: Condition): TabId | null {
  if (c.op === ALL_OPEN.op && sameSet(c.values, ALL_OPEN.values)) return "all";
  if (c.op !== "any") return null;
  const tab = TABS.find((t) => t.id !== "all" && sameSet(t.states, c.values));
  return tab?.id ?? null;
}

// boardQueryForView writes a /next/ view in the board grammar. The tab
// is always written, Backlog included: a board with no condition on
// state means classic's default list (everything not deferred or
// closed), which is not what the Backlog tab shows.
export function boardQueryForView(v: BoardView): string {
  const p = new URLSearchParams();
  const q = v.q.trim();
  if (q) p.set("q", q);
  if (v.tab === "all") {
    p.append("f", `state:${ALL_OPEN.op}:${ALL_OPEN.values.join("|")}`);
  } else {
    const states = TABS.find((t) => t.id === v.tab)?.states ?? [];
    p.append("f", `state:any:${states.map(escapeValue).join("|")}`);
  }
  const repo = v.repo.trim();
  if (repo) p.append("f", `repo:any:${escapeValue(repo)}`);
  if (v.group !== "none") p.set("group", v.group);
  // The separators back as themselves, as classic writes them: this is
  // a string people read in tables/board.json and in the address bar.
  return p
    .toString()
    .replace(/%2F/g, "/")
    .replace(/%3A/g, ":")
    .replace(/%7C/g, "|")
    .replace(/%2C/g, ",");
}

// viewForQuery reads a board-grammar query (or a /next/tasks address)
// into a view, or says why /next/tasks cannot show it. A query with no
// condition on state opens on All, the nearest tab to classic's default
// list. The keys /next/ wrote before it spoke this grammar (tab=,
// repo=) are still read, so boards saved then keep opening as saved.
export function viewForQuery(
  query: string,
): { view: BoardView } | { why: string } {
  const p = new URLSearchParams(query || "");
  const view: BoardView = {
    tab: "all",
    q: (p.get("q") || "").trim(),
    repo: (p.get("repo") || "").trim(),
    group: "none",
  };
  const g = p.get("group");
  if (g === "workstream" || g === "repo") view.group = g;
  const legacyTab = p.get("tab");
  if (isTabId(legacyTab)) view.tab = legacyTab;
  // A condition on state wins over a legacy tab= beside it; two of them
  // narrow to their intersection, which no tab is.
  let stated = false;
  if (p.get("sort")) {
    return { why: `it is sorted by ${p.get("sort")}` };
  }
  for (const raw of p.getAll("f")) {
    if (raw === "none") {
      return { why: "it lists every task, closed ones included" };
    }
    const c = conditionOf(raw);
    if (!c) return { why: `its condition "${raw}" is not one /next/ reads` };
    if (c.attr === "state") {
      const tab = tabForState(c);
      if (!tab || stated) {
        return { why: "it narrows by a set of states no tab here lists" };
      }
      view.tab = tab;
      stated = true;
    } else if (c.attr === "repo" && c.op === "any" && c.values.length === 1) {
      if (view.repo && view.repo !== c.values[0]) {
        return { why: "it names more than one repository" };
      }
      view.repo = c.values[0];
    } else {
      return { why: `it narrows by ${c.attr}, which /next/ cannot filter on` };
    }
  }
  return { view };
}

// planForBoard is what applying a board from the /next/ Boards menu
// does: open it here, or say why it opens on the classic page.
export function planForBoard(b: Board): BoardPlan {
  const label = KIND_LABELS[b.kind];
  if (label) {
    return { open: "classic", why: `it is a ${label} board` };
  }
  const read = viewForQuery(b.query || "");
  if ("why" in read) return { open: "classic", why: read.why };
  return { open: "here", view: read.view };
}

// sameView compares two views as the Boards menu does to name the
// board on screen.
export function sameView(a: BoardView, b: BoardView): boolean {
  return (
    a.tab === b.tab &&
    a.q === b.q &&
    a.repo.toLowerCase() === b.repo.toLowerCase() &&
    a.group === b.group
  );
}
