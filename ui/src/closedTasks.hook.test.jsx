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
    const key = `${repo ? `repo=${repo}` : ""}${search ? `search=${search}` : ""}${cursor ? `@${cursor}` : ""}`;
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
