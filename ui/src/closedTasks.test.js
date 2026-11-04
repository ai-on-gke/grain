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
  closedPagePath,
  mergeClosedPage,
  mergeTaskLists,
  readPage,
  unseenClosed,
} from "./closedTasks.js";

describe("closedTasks", () => {
  it("asks for a page of closed tasks after a cursor", () => {
    expect(closedPagePath("")).toBe(
      "/api/tasks?state=closed&limit=200&cursor=",
    );
    expect(closedPagePath("17.4")).toContain("cursor=17.4");
  });

  it("reads a page, and anything else as a last page", () => {
    expect(readPage({ tasks: [{ id: "1" }], nextCursor: "c" })).toEqual({
      tasks: [{ id: "1" }],
      nextCursor: "c",
      totals: null,
    });
    expect(
      readPage({ tasks: [], total: 9, merged: 2, sessions: 1 }).totals,
    ).toEqual({ total: 9, merged: 2, sessions: 1 });
    expect(readPage({ error: "nope" })).toEqual({
      tasks: [],
      nextCursor: "",
      totals: null,
    });
    expect(
      readPage([
        { id: "1", state: "closed" },
        { id: "2", state: "queued" },
      ]),
    ).toEqual({
      tasks: [{ id: "1", state: "closed" }],
      nextCursor: "",
      totals: null,
    });
  });

  // 2026-11-04 walkthrough: 205 closed tasks read "Closed 200" on the
  // rail, and "205" only once Load older closed tasks had been pressed.
  it("counts the closed work no page read so far holds", () => {
    const held = [
      { id: "1" },
      { id: "2", prMergedAt: "2026-11-01T00:00:00Z" },
      { id: "3", consoleSession: "director" },
    ];
    expect(unseenClosed(null, held)).toEqual({ closed: 0, merged: 0 });
    expect(unseenClosed({ total: 3, merged: 1, sessions: 1 }, held)).toEqual({
      closed: 0,
      merged: 0,
    });
    expect(unseenClosed({ total: 210, merged: 4, sessions: 2 }, held)).toEqual({
      closed: 203,
      merged: 3,
    });
    // A refresh racing a close never counts below what is held.
    expect(unseenClosed({ total: 2, merged: 0, sessions: 0 }, held)).toEqual({
      closed: 0,
      merged: 0,
    });
  });

  it("keeps the held array when a refresh changes nothing", () => {
    const held = [{ id: "1", title: "a" }];
    expect(mergeClosedPage(held, [{ id: "1", title: "a" }])).toBe(held);
    expect(mergeClosedPage(held, [])).toBe(held);
  });

  it("replaces changed rows and adds new ones, newest-closed first", () => {
    const held = [
      { id: "2", title: "b", closedAt: "2026-11-02T00:00:00Z" },
      { id: "1", title: "a", closedAt: "2026-11-01T00:00:00Z" },
    ];
    expect(
      mergeClosedPage(held, [
        { id: "3", title: "c", closedAt: "2026-11-03T00:00:00Z" },
        { id: "2", title: "b2", closedAt: "2026-11-02T00:00:00Z" },
      ]).map((t) => t.title),
    ).toEqual(["c", "b2", "a"]);
  });

  it("draws live rows over closed ones, and keeps live as it is with nothing to add", () => {
    const live = [{ id: "1", state: "queued" }];
    expect(mergeTaskLists(live, [])).toBe(live);
    expect(mergeTaskLists(live, [{ id: "1", state: "closed" }])).toBe(live);
    expect(mergeTaskLists(live, [{ id: "2", state: "closed" }])).toEqual([
      { id: "1", state: "queued" },
      { id: "2", state: "closed" },
    ]);
  });
});
