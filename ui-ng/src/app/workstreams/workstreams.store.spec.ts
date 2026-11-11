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
    author: "rarcea",
    authorKind: "human",
    owner: "rarcea",
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

  config = () => Promise.resolve({ access: null, viewerOwner: "rarcea" });
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
      viewerOwner: "rarcea",
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
      (c) => c.route === "GET /api/tasks/{id}",
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
      viewerOwner: "rarcea",
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
});
