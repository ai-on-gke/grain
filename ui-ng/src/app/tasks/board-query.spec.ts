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
import {
  type BoardView,
  boardQueryForView,
  planForBoard,
  viewForQuery,
} from "./board-query";
import { TABS } from "./task-view";

const view = (v: Partial<BoardView>): BoardView => ({
  tab: "backlog",
  q: "",
  repo: "",
  group: "none",
  ...v,
});
const board = (kind: string, query?: string): Board => ({
  id: "board-1",
  name: "B",
  kind,
  ...(query !== undefined ? { query } : {}),
});

describe("boardQueryForView", () => {
  it("writes the tab as a state condition, as classic boards read it", () => {
    expect(boardQueryForView(view({ tab: "asked", q: "retry" }))).toBe(
      "q=retry&f=state:any:awaiting_reply|failed",
    );
    expect(boardQueryForView(view({}))).toBe("f=state:any:queued");
    expect(boardQueryForView(view({ tab: "all" }))).toBe("f=state:none:closed");
  });

  it("writes the repository as a repo condition and keeps the grouping", () => {
    expect(
      boardQueryForView(
        view({ tab: "running", repo: "acme/widgets", group: "workstream" }),
      ),
    ).toBe("f=state:any:running&f=repo:any:acme/widgets&group=workstream");
  });

  it("reads back every tab it writes", () => {
    for (const t of TABS) {
      const v = view({ tab: t.id, q: "a b", repo: "o/n", group: "repo" });
      expect(viewForQuery(boardQueryForView(v))).toEqual({ view: v });
    }
  });
});

describe("viewForQuery", () => {
  it("opens a classic state condition on its tab", () => {
    expect(viewForQuery("f=state:any:queued")).toEqual({
      view: view({ tab: "backlog" }),
    });
    expect(viewForQuery("f=state:any:failed|awaiting_reply&q=x")).toEqual({
      view: view({ tab: "asked", q: "x" }),
    });
  });

  it("opens a query with no state condition on All", () => {
    expect(viewForQuery("")).toEqual({ view: view({ tab: "all" }) });
    expect(viewForQuery("f=repo:any:o/n")).toEqual({
      view: view({ tab: "all", repo: "o/n" }),
    });
  });

  it("still reads the keys /next/ wrote before", () => {
    expect(viewForQuery("tab=asked&group=repo&q=retry&repo=o/n")).toEqual({
      view: view({ tab: "asked", group: "repo", q: "retry", repo: "o/n" }),
    });
  });

  it("lets a state condition win over a legacy tab", () => {
    expect(viewForQuery("tab=running&f=state:any:queued")).toEqual({
      view: view({ tab: "backlog" }),
    });
    expect(
      "why" in viewForQuery("f=state:any:queued&f=state:any:running"),
    ).toBe(true);
  });

  it("ignores classic's retired interactive exclusion and manual sort", () => {
    expect(
      viewForQuery("sort=manual&f=state:any:queued&f=kind:none:interactive"),
    ).toEqual({ view: view({ tab: "backlog" }) });
  });

  it("refuses what this page cannot show exactly", () => {
    for (const q of [
      "sort=created:desc",
      "f=none",
      "f=state:any:queued|running",
      "f=author:any:alice",
      "author=alice&f=state:any:queued",
      "base=main",
      "f=repo:any:a/b|c/d",
      "f=created:within:7",
      "f=bogus",
    ]) {
      expect("why" in viewForQuery(q), q).toBe(true);
    }
  });
});

describe("planForBoard", () => {
  it("opens a list board here", () => {
    expect(planForBoard(board("list", "f=state:any:queued"))).toEqual({
      open: "here",
      view: view({ tab: "backlog" }),
    });
  });

  it("sends every other kind to the classic page, naming it", () => {
    for (const [kind, label] of [
      ["kanban", "Kanban"],
      ["gantt", "Timeline"],
      ["tree", "Tree"],
      ["panels", "Panels"],
      ["report", "Report"],
    ]) {
      expect(planForBoard(board(kind, ""))).toEqual({
        open: "classic",
        why: `it is a ${label} board`,
      });
    }
  });

  it("sends a list board it cannot show to the classic page", () => {
    const plan = planForBoard(board("list", "f=author:any:alice"));
    expect(plan.open).toBe("classic");
  });
});
