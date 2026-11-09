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

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "./api.js";
import { useClosedTasks, useRepoClosedCount } from "./closedTasks.js";

vi.mock("./api.js", () => ({ default: vi.fn() }));

// answer routes api's requests by what they ask: the newest page, a
// page after a cursor, or a server search.
function answer(routes) {
  api.mockImplementation(async (path) => {
    const url = new URL(path, "http://grain.test");
    const search = url.searchParams.get("search") || "";
    const cursor = url.searchParams.get("cursor") || "";
    const repo = url.searchParams.get("repo") || "";
    const owner = url.searchParams.get("owner") || "";
    const extRef = url.searchParams.get("externalRef") || "";
    const key = `${repo ? `repo=${repo}` : ""}${search ? `search=${search}` : ""}${owner ? `owner=${owner}` : ""}${extRef ? `externalRef=${extRef}` : ""}${cursor ? `@${cursor}` : ""}`;
    if (!(key in routes)) throw new Error(`unexpected ${path}`);
    return routes[key];
  });
}

const closed = (id, at, extra = {}) => ({
  id,
  title: `task ${id}`,
  state: "closed",
  closedAt: at,
  ...extra,
});

describe("useClosedTasks", () => {
  afterEach(() => {
    api.mockReset();
    vi.useRealTimers();
  });

  it("searches closed work on the server and merges what it finds, marked", async () => {
    answer({
      "": {
        tasks: [closed("50", "2026-11-05T00:00:00Z")],
        nextCursor: "c1",
        total: 300,
        merged: 0,
        sessions: 0,
      },
      "search=deadlock": {
        tasks: [closed("3", "2026-01-02T00:00:00Z")],
        nextCursor: "s1",
        total: 2,
        merged: 0,
        sessions: 0,
      },
      "search=deadlock@s1": {
        tasks: [closed("2", "2026-01-01T00:00:00Z")],
        nextCursor: "",
        total: 2,
        merged: 0,
        sessions: 0,
      },
    });
    const hook = renderHook(({ q }) => useClosedTasks(true, q), {
      initialProps: { q: "" },
    });
    await waitFor(() => expect(hook.result.current.closed).toHaveLength(1));
    expect(hook.result.current.partial).toBe(true);
    expect(hook.result.current.hasMore).toBe(true);

    hook.rerender({ q: " Deadlock " });
    await waitFor(() => expect(hook.result.current.closed).toHaveLength(2));
    const found = hook.result.current.closed.find((t) => t.id === "3");
    expect(found.searchHit).toBe("deadlock");
    // The pager now pages the search, not the newest-closed list.
    expect(hook.result.current.searching).toBe(true);
    expect(hook.result.current.hasMore).toBe(true);
    await act(() => hook.result.current.loadMore());
    expect(hook.result.current.closed.map((t) => t.id)).toEqual([
      "50",
      "3",
      "2",
    ]);
    expect(hook.result.current.hasMore).toBe(false);
    // The search's own totals are not the rail's: 300 closed tasks, 3 held.
    expect(hook.result.current.unseen.closed).toBe(297);

    // Emptying the box hands the pager back to the newest-closed list.
    hook.rerender({ q: "" });
    expect(hook.result.current.searching).toBe(false);
    expect(hook.result.current.hasMore).toBe(true);
  });

  it("draws a search's deep hits only while the box holds that search", async () => {
    // 2026-11-07 walkthrough: merged into the newest-closed pages, a hit
    // from far below them stayed drawn after them -- out of place, after
    // a gap -- once the box was emptied.
    answer({
      "": {
        tasks: [closed("50", "2026-11-05T00:00:00Z")],
        nextCursor: "c1",
        total: 300,
        merged: 0,
        sessions: 0,
      },
      "search=needle": {
        tasks: [closed("3", "2026-01-02T00:00:00Z")],
        nextCursor: "",
        total: 1,
        merged: 0,
        sessions: 0,
      },
    });
    const hook = renderHook(({ q }) => useClosedTasks(true, q), {
      initialProps: { q: "" },
    });
    await waitFor(() => expect(hook.result.current.closed).toHaveLength(1));
    hook.rerender({ q: "needle" });
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50", "3"]),
    );
    hook.rerender({ q: "" });
    expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50"]);
    expect(hook.result.current.hasMore).toBe(true);
    // Typed again, the hit is asked for again rather than remembered.
    hook.rerender({ q: "needle" });
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50", "3"]),
    );
  });

  it("asks nothing of the server for a search until enabled", async () => {
    answer({});
    renderHook(() => useClosedTasks(false, "deadlock"));
    await new Promise((r) => setTimeout(r, 400));
    expect(api).not.toHaveBeenCalled();
  });

  it("drops a search's answer once the box has moved on", async () => {
    let release;
    api.mockImplementation(async (path) => {
      if (path.includes("search=slow")) {
        await new Promise((r) => {
          release = r;
        });
        return { tasks: [closed("9", "2026-01-01T00:00:00Z")], nextCursor: "" };
      }
      if (path.includes("search=fast")) {
        return { tasks: [closed("8", "2026-01-01T00:00:00Z")], nextCursor: "" };
      }
      return { tasks: [], nextCursor: "", total: 0, merged: 0, sessions: 0 };
    });
    const hook = renderHook(({ q }) => useClosedTasks(true, q), {
      initialProps: { q: "slow" },
    });
    await waitFor(() => expect(release).toBeTypeOf("function"));
    hook.rerender({ q: "fast" });
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["8"]),
    );
    await act(async () => release());
    expect(hook.result.current.closed.map((t) => t.id)).toEqual(["8"]);
  });

  it("asks the server for a pinned owner or external reference, not every page", async () => {
    // 2026-11-07 walkthrough, F6: `externalRef:any:CA-9012` over closed
    // work was answered only on the pages held, so the pager read every
    // closed page until the one task turned up.
    answer({
      "": {
        tasks: [closed("50", "2026-11-05T00:00:00Z")],
        nextCursor: "c1",
        total: 3000,
        merged: 0,
        sessions: 0,
      },
      "externalRef=CA-9012": {
        tasks: [
          closed("12", "2026-01-02T00:00:00Z", { externalRef: "CA-9012" }),
        ],
        nextCursor: "",
        total: 1,
        merged: 0,
        sessions: 0,
      },
      "owner=deep@example.com": {
        tasks: [
          closed("12", "2026-01-02T00:00:00Z", { owner: "deep@example.com" }),
        ],
        nextCursor: "o1",
        total: 2,
        merged: 0,
        sessions: 0,
      },
      "owner=deep@example.com@o1": {
        tasks: [
          closed("7", "2026-01-01T00:00:00Z", { owner: "deep@example.com" }),
        ],
        nextCursor: "",
        total: 2,
        merged: 0,
        sessions: 0,
      },
      "search=quillowner=deep@example.com": {
        tasks: [
          closed("12", "2026-01-02T00:00:00Z", { owner: "deep@example.com" }),
        ],
        nextCursor: "",
        total: 1,
        merged: 0,
        sessions: 0,
      },
    });
    const hook = renderHook(
      ({ q, filters }) => useClosedTasks(true, q, filters),
      { initialProps: { q: "", filters: undefined } },
    );
    await waitFor(() => expect(hook.result.current.closed).toHaveLength(1));
    expect(hook.result.current.searching).toBe(false);

    hook.rerender({ q: "", filters: { owner: "", externalRef: "CA-9012" } });
    // The pager pages the server's answer from the first render on, so
    // the short list's end coming into view reads nothing else.
    expect(hook.result.current.searching).toBe(true);
    expect(hook.result.current.hasMore).toBe(false);
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50", "12"]),
    );
    // Drawn as found, unmarked: the views match the reference themselves.
    const hit = hook.result.current.closed.find((t) => t.id === "12");
    expect(hit.searchHit).toBeUndefined();
    expect(hook.result.current.hasMore).toBe(false);
    await act(() => hook.result.current.loadMore());
    expect(
      api.mock.calls
        .filter(([p]) => !p.includes("externalRef="))
        .map(([p]) => p),
    ).toEqual(["/api/tasks?state=closed&limit=200&cursor="]);

    // An owner pages its own answer, not the newest-closed list.
    hook.rerender({
      q: "",
      filters: { owner: "deep@example.com", externalRef: "" },
    });
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50", "12"]),
    );
    expect(hook.result.current.hasMore).toBe(true);
    await act(() => hook.result.current.loadMore());
    expect(hook.result.current.closed.map((t) => t.id)).toEqual([
      "50",
      "12",
      "7",
    ]);
    expect(hook.result.current.hasMore).toBe(false);

    // With the box set too, both go in one request.
    hook.rerender({
      q: "quill",
      filters: { owner: "deep@example.com", externalRef: "" },
    });
    await waitFor(() =>
      expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50", "12"]),
    );
    expect(
      hook.result.current.closed.find((t) => t.id === "12").searchHit,
    ).toBe("quill");

    // Dropped, the hits go and the pager is the newest-closed list's again.
    hook.rerender({ q: "", filters: { owner: "", externalRef: "" } });
    expect(hook.result.current.searching).toBe(false);
    expect(hook.result.current.closed.map((t) => t.id)).toEqual(["50"]);
    expect(hook.result.current.hasMore).toBe(true);
    expect(api.mock.calls.some(([p]) => p.includes("cursor=c1"))).toBe(false);
  });
});

describe("useRepoClosedCount", () => {
  afterEach(() => api.mockReset());

  it("asks the server for one repo's closed count, sessions left out", async () => {
    answer({
      "repo=acme/widgets": {
        tasks: [closed("1", "2026-01-01T00:00:00Z")],
        nextCursor: "x",
        total: 40,
        merged: 30,
        sessions: 3,
      },
    });
    const hook = renderHook(
      ({ on }) => useRepoClosedCount("acme/widgets", on),
      {
        initialProps: { on: true },
      },
    );
    await waitFor(() => expect(hook.result.current).toBe(37));
    expect(api).toHaveBeenCalledWith(
      "/api/tasks?state=closed&limit=1&cursor=&repo=acme%2Fwidgets",
    );
    hook.rerender({ on: false });
    expect(hook.result.current).toBe(null);
  });
});
