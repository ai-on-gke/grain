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

import { TestBed } from "@angular/core/testing";
import { Subject } from "rxjs";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GrainApi, routeScope } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { STARTED_STORAGE_KEY, WorkstreamsStore } from "./workstreams.store";

function mkTask(id: string, over: Partial<Task> = {}): Task {
  return {
    id,
    title: `Task ${id}`,
    state: "queued",
    repo: "sidthe/grain-scratch-repo",
    author: "alice",
    authorKind: "human",
    owner: "alice",
    createdAt: "2026-11-11T10:00:00Z",
    updatedAt: "2026-11-11T11:00:00Z",
    ...over,
  };
}

class FakeApi {
  open: Task[] = [];
  closedPages: Record<string, { tasks: Task[]; nextCursor?: string }> = {
    "": { tasks: [] },
  };
  details: Record<string, TaskDetail> = {};
  calls: { route: string; params?: unknown; body?: unknown; query?: string }[] =
    [];
  failPostIds = new Set<string>();

  config = () => Promise.resolve({ access: null, viewerOwner: "alice" });
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  tasks = () => Promise.resolve(this.open);

  getIfChanged = vi.fn(async (route: string) => {
    this.calls.push({ route });
    return { body: this.open, changed: true };
  });

  get = vi.fn(
    async (route: string, params?: { id?: string }, query?: string) => {
      this.calls.push({ route, params, query });
      if (route === "GET /api/tasks") {
        const m = /cursor=([^&]+)/.exec(query ?? "");
        const cursor = m ? decodeURIComponent(m[1]) : "";
        return this.closedPages[cursor] ?? { tasks: [] };
      }
      if (route === "GET /api/tasks/{id}" && params?.id) {
        return (
          this.details[params.id] ?? {
            ...mkTask(params.id),
            description: "Loaded goal",
          }
        );
      }
      if (route === "GET /api/tasks/summaries") {
        const ids =
          new URLSearchParams(query ?? "")
            .get("ids")
            ?.split(",")
            .filter(Boolean) ?? [];
        return {
          tasks: Object.fromEntries(
            ids.map((id) => [
              id,
              this.details[id] ?? {
                ...mkTask(id),
                description: "Loaded goal",
              },
            ]),
          ),
        };
      }
      return {};
    },
  );

  post = vi.fn(
    async (route: string, params?: { id?: string }, body?: unknown) => {
      this.calls.push({ route, params, body });
      if (params?.id && this.failPostIds.has(params.id)) {
        throw new Error(`boom on ${params.id}`);
      }
      return {};
    },
  );
}

describe("WorkstreamsStore", () => {
  let api: FakeApi;
  let events$: Subject<unknown>;
  let store: WorkstreamsStore;

  beforeEach(() => {
    localStorage.removeItem(STARTED_STORAGE_KEY);
    api = new FakeApi();
    events$ = new Subject<unknown>();
    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: api },
        {
          provide: GrainEvents,
          useValue: { events: () => events$.asObservable() },
        },
      ],
    });
    TestBed.inject(AccessService).setConfig({
      access: null,
      viewerOwner: "alice",
    });
    store = TestBed.inject(WorkstreamsStore);
  });

  it("loads open tasks plus paged closed tasks and merges root detail lazily", async () => {
    api.open = [
      mkTask("11", {
        state: "proposed",
        origin: "task:10",
        generatedFrom: "10",
      }),
      mkTask("12", {
        state: "running",
        origin: "task:10",
        generatedFrom: "10",
      }),
    ];
    api.closedPages = {
      "": {
        tasks: [
          mkTask("10", {
            title: "Root stream",
            state: "completed",
            closedAt: "2026-11-11T09:00:00Z",
          }),
        ],
        nextCursor: "p2",
      },
      p2: {
        tasks: [
          mkTask("9", {
            title: "Older closed",
            state: "completed",
            closedAt: "2026-11-10T09:00:00Z",
          }),
        ],
      },
    };
    api.details["10"] = {
      ...mkTask("10", { title: "Root stream", state: "completed" }),
      description: "Full goal text loaded lazily",
    };

    await store.refresh(true);
    expect(store.loaded()).toBe(true);
    expect(store.streams().length).toBe(1);
    const ws = store.byKey().get("task:10")!;
    expect(ws.goal).toBe("");
    expect(ws.counts).toMatchObject({
      done: 0,
      run: 1,
      person: 1,
      proposed: 1,
      total: 2,
    });

    await store.ensureRootDetail("10");
    await store.ensureRootDetail("10"); // deduplicated
    expect(store.byKey().get("task:10")?.goal).toBe(
      "Full goal text loaded lazily",
    );
    const detailCalls = api.calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    expect(detailCalls.length).toBe(1);
  });

  it("persists started workstreams in localStorage and runs approve/pause/resume batches", async () => {
    api.open = [
      mkTask("20", { state: "queued", title: "Fresh goal" }),
      mkTask("21", {
        state: "proposed",
        origin: "task:20",
        generatedFrom: "20",
      }),
      mkTask("22", {
        state: "proposed",
        origin: "task:20",
        generatedFrom: "20",
      }),
      mkTask("23", {
        state: "deferred",
        origin: "task:20",
        generatedFrom: "20",
      }),
    ];

    store.recordStartedWorkstream("20");
    expect(
      JSON.parse(localStorage.getItem(STARTED_STORAGE_KEY) || "[]"),
    ).toEqual(["20"]);

    await store.refresh(true);
    const ws = store.byKey().get("task:20")!;
    expect(store.approvePreview(ws).nodes.map((n) => n.id)).toEqual([
      "21",
      "22",
    ]);

    api.failPostIds.add("22");
    const res = await store.approveProposals(ws);
    expect(res).toEqual({ succeeded: 1, failed: 1 });
    expect(store.actionError()).toContain("1 of 2");

    await store.pauseStream(ws);
    expect(
      api.calls.filter((c) => c.route === "POST /api/tasks/{id}/defer").length,
    ).toBe(2);

    await store.resumeStream(ws);
    expect(
      api.calls.some(
        (c) =>
          c.route === "POST /api/tasks/{id}/undefer" &&
          (c.params as { id: string }).id === "23",
      ),
    ).toBe(true);
  });

  it("cleans up start/stop cleanly", () => {
    store.start();
    store.stop();
    expect(api.getIfChanged).toHaveBeenCalledTimes(1);
  });

  it("passes closedSince on closed queries, filters pausePreview to deferrable states, and matches AccessService permissions", async () => {
    const approveScope = routeScope("POST /api/tasks/{id}/approve")!;
    const deferScope = routeScope("POST /api/tasks/{id}/defer")!;
    TestBed.inject(AccessService).setConfig({
      viewerOwner: "alice",
      access: {
        full: false,
        role: "custom",
        scopes: [approveScope, deferScope],
        repoLimits: {
          [approveScope]: ["sidthe/allowed"],
          [deferScope]: ["sidthe/allowed"],
        },
      },
    });
    api.open = [
      mkTask("40", {
        state: "completed",
        repo: "sidthe/allowed",
      }),
      mkTask("41", {
        state: "running",
        repo: "sidthe/allowed",
        origin: "task:40",
        generatedFrom: "40",
      }),
      mkTask("42", {
        state: "failed",
        repo: "sidthe/allowed",
        origin: "task:40",
        generatedFrom: "40",
      }),
      mkTask("43", {
        state: "proposed",
        repo: "sidthe/allowed",
        origin: "task:40",
        generatedFrom: "40",
      }),
      mkTask("44", {
        state: "proposed",
        repo: "sidthe/denied",
        origin: "task:40",
        generatedFrom: "40",
      }),
      mkTask("45", {
        state: "queued",
        repo: "sidthe/allowed",
        origin: "task:40",
        generatedFrom: "40",
      }),
    ];

    await store.refresh(true);
    const closedCall = api.calls.find(
      (c) => c.route === "GET /api/tasks" && c.query?.includes("state=closed"),
    );
    expect(closedCall?.query).toContain("closedSince=");

    const ws = store.byKey().get("task:40")!;
    // Approve preview and batch only include permitted repo sidthe/allowed (#43), never sidthe/denied (#44).
    expect(store.approvePreview(ws).nodes.map((n) => n.id)).toEqual(["43"]);
    // Pause preview strictly accepts proposed (#43) and queued (#45) on permitted repo; never running (#41) or failed (#42).
    expect(store.pausePreview(ws).nodes.map((n) => n.id)).toEqual(["43", "45"]);

    await store.pauseStream(ws);
    const deferredIds = api.calls
      .filter((c) => c.route === "POST /api/tasks/{id}/defer")
      .map((c) => (c.params as { id: string }).id);
    expect(deferredIds).toEqual(["43", "45"]);
  });

  it("lets fresher open task state override cached extraTasks while keeping description", async () => {
    api.open = [
      mkTask("50", { state: "queued", title: "Goal" }),
      mkTask("51", {
        state: "running",
        origin: "task:50",
        generatedFrom: "50",
      }),
    ];
    api.details["50"] = {
      ...mkTask("50", { state: "queued", title: "Goal" }),
      description: "Detailed root description",
    };

    await store.refresh(true);
    await store.ensureRootDetail("50");

    api.open = [
      mkTask("50", {
        state: "completed",
        title: "Goal",
        updatedAt: "2026-11-11T19:00:00Z",
      }),
      api.open[1],
    ];
    await store.refresh(true);

    const ws = store.byKey().get("task:50")!;
    expect(ws.root.task.state).toBe("completed");
    expect(ws.goal).toBe("Detailed root description");
  });

  it("sets loaded immediately after open tasks resolve while closed pages continue in background", async () => {
    api.open = [
      mkTask("60", { state: "queued", title: "Open root" }),
      mkTask("61", {
        state: "running",
        origin: "task:60",
        generatedFrom: "60",
      }),
    ];

    let releaseClosed!: (res: { tasks: Task[]; nextCursor?: string }) => void;
    let markClosedCalled!: () => void;
    const closedCalled = new Promise<void>((r) => {
      markClosedCalled = r;
    });
    const origGet = api.get.getMockImplementation()!;
    api.get.mockImplementation(
      (route: string, params?: { id?: string }, query?: string) => {
        if (route === "GET /api/tasks" && query?.includes("state=closed")) {
          return new Promise((resolve) => {
            releaseClosed = resolve;
            markClosedCalled();
          });
        }
        return origGet(route, params, query);
      },
    );

    const pending = store.refresh(true);
    await closedCalled;

    expect(store.loaded()).toBe(true);
    expect(store.closedLoading()).toBe(true);
    expect(store.streams().map((s) => s.key)).toEqual(["task:60"]);

    releaseClosed({
      tasks: [mkTask("59", { state: "completed", title: "Closed root" })],
    });
    await pending;

    expect(store.closedLoading()).toBe(false);
    expect(store.closedTasks().map((t) => t.id)).toEqual(["59"]);
  });

  it("guards ensureRootDetail with AccessService repo permissions", async () => {
    const readScope = routeScope("GET /api/tasks/{id}")!;
    TestBed.inject(AccessService).setConfig({
      viewerOwner: "alice",
      access: {
        full: false,
        role: "custom",
        scopes: [readScope],
        repoLimits: { [readScope]: ["sidthe/allowed"] },
      },
    });
    api.open = [
      mkTask("70", { state: "queued", repo: "sidthe/denied" }),
      mkTask("71", { state: "queued", repo: "" }),
      mkTask("72", { state: "queued", repo: "sidthe/allowed" }),
    ];

    await store.refresh(true);
    api.calls = [];

    await store.ensureRootDetail("70");
    await store.ensureRootDetail("71");
    await store.ensureRootDetail("72");

    const summaryCalls = api.calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    expect(summaryCalls.length).toBe(1);
    const fetchedIds =
      new URLSearchParams(summaryCalls[0].query ?? "")
        .get("ids")
        ?.split(",")
        .filter(Boolean) ?? [];
    expect(fetchedIds).toEqual(["72"]);
  });

  it("batches 20 root details into 1 request, chunks >50 by 50, and preserves cached data on failure", async () => {
    const withChild = (r: Task) => [
      r,
      mkTask(`${r.id}-child`, {
        state: "running",
        origin: `task:${r.id}`,
        generatedFrom: r.id,
      }),
    ];
    const roots = Array.from({ length: 20 }, (_, i) =>
      mkTask(`${100 + i}`, { state: "queued", title: `Root ${100 + i}` }),
    );
    api.open = roots.flatMap(withChild);
    for (const r of roots) {
      api.details[r.id] = { ...r, description: `Goal for ${r.id}` };
    }
    await store.refresh(true);
    api.calls = [];

    // 20 concurrent ensureRootDetail calls coalesce into 1 batch request.
    await Promise.all(roots.map((r) => store.ensureRootDetail(r.id)));
    const batchCalls = api.calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    const singleCalls = api.calls.filter(
      (c) => c.route === "GET /api/tasks/{id}",
    );
    expect(singleCalls.length).toBe(0);
    expect(batchCalls.length).toBe(1);
    const q = new URLSearchParams(batchCalls[0].query ?? "");
    expect(q.get("ids")!.split(",").length).toBe(20);
    expect(q.get("comments_limit")).toBe("1");
    expect(q.get("attempts_limit")).toBe("1");
    expect(store.byKey().get("task:100")?.goal).toBe("Goal for 100");
    expect(store.byKey().get("task:119")?.goal).toBe("Goal for 119");

    // 75 new root IDs chunk cleanly into 50 and 25.
    const more = Array.from({ length: 75 }, (_, i) =>
      mkTask(`${200 + i}`, { state: "queued", title: `Root ${200 + i}` }),
    );
    api.open = [...roots, ...more].flatMap(withChild);
    for (const r of more) {
      api.details[r.id] = { ...r, description: `Goal for ${r.id}` };
    }
    await store.refresh(true);
    api.calls = [];

    await store.ensureRootDetails(more.map((r) => r.id));
    const chunkCalls = api.calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    expect(chunkCalls.length).toBe(2);
    expect(
      new URLSearchParams(chunkCalls[0].query ?? "").get("ids")!.split(",")
        .length,
    ).toBe(50);
    expect(
      new URLSearchParams(chunkCalls[1].query ?? "").get("ids")!.split(",")
        .length,
    ).toBe(25);
    expect(store.byKey().get("task:274")?.goal).toBe("Goal for 274");

    // Failure on uncached ID leaves previously cached root details intact.
    const baseGet = api.get;
    api.get = vi.fn(
      async (route: string, params?: { id?: string }, query?: string) => {
        if (route === "GET /api/tasks/summaries") {
          throw new Error("503");
        }
        return baseGet(route, params, query);
      },
    );
    await store.ensureRootDetail("999");
    expect(store.byKey().get("task:100")?.goal).toBe("Goal for 100");
    expect(store.byKey().get("task:274")?.goal).toBe("Goal for 274");
  });
});
