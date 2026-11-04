import { describe, expect, it } from "vitest";
import {
  closedPagePath,
  mergeClosedPage,
  mergeTaskLists,
  readPage,
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
    });
    expect(readPage({ error: "nope" })).toEqual({ tasks: [], nextCursor: "" });
    expect(
      readPage([
        { id: "1", state: "closed" },
        { id: "2", state: "queued" },
      ]),
    ).toEqual({ tasks: [{ id: "1", state: "closed" }], nextCursor: "" });
  });

  it("keeps the held array when a refresh changes nothing", () => {
    const held = [{ id: "1", title: "a" }];
    expect(mergeClosedPage(held, [{ id: "1", title: "a" }])).toBe(held);
    expect(mergeClosedPage(held, [])).toBe(held);
  });

  it("replaces changed rows and adds new ones", () => {
    const held = [
      { id: "1", title: "a" },
      { id: "2", title: "b" },
    ];
    expect(
      mergeClosedPage(held, [
        { id: "2", title: "b2" },
        { id: "3", title: "c" },
      ]),
    ).toEqual([
      { id: "1", title: "a" },
      { id: "2", title: "b2" },
      { id: "3", title: "c" },
    ]);
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
