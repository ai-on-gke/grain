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
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
import { TasksStore } from "./tasks.store";

function awaitingSubmitTask(id: string): Task {
  return {
    id,
    title: `Task ${id}`,
    state: "awaiting_submit",
    priority: 5,
    mergeGates: [
      { id: "ci", label: "CI", state: "running", detail: "waiting for checks" },
    ],
  } as Task;
}

describe("TasksStore", () => {
  afterEach(() => TestBed.resetTestingModule());

  // The shell's feed reads /api/tasks before the Tasks page starts its
  // store, so a cold failure is already there when start() is called.
  it("says the task list could not be read when it failed before the page started", async () => {
    const fakeApi = {
      config: () => Promise.resolve({ access: null }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: () => Promise.resolve({}),
      getIfChanged: (route: string) =>
        route === "GET /api/tasks"
          ? Promise.reject(new Error("store unavailable"))
          : Promise.resolve({ body: {}, changed: true }),
    };
    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        {
          provide: GrainEvents,
          useValue: { events: () => new Subject<GrainEvent>() },
        },
      ],
    });
    const feed = TestBed.inject(TaskFeedStore);
    const tasks = TestBed.inject(TasksStore);
    TestBed.flushEffects();
    expect(await feed.refresh()).toBe(false);
    TestBed.flushEffects();
    tasks.start();
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 10));
      TestBed.flushEffects();
    }
    expect(tasks.loadError()).toContain("could not be read");
    tasks.stop();
  });

  it("loads boards and base-branch gates once config arrives even when tasks resolve first", async () => {
    let resolveConfig!: (cfg: Record<string, unknown>) => void;
    const configPromise = new Promise<Record<string, unknown>>((r) => {
      resolveConfig = r;
    });
    let resolveTasks!: (res: { body: unknown[]; changed: boolean }) => void;
    const tasksPromise = new Promise<{ body: unknown[]; changed: boolean }>(
      (r) => {
        resolveTasks = r;
      },
    );

    const fakeApi = {
      config: () => configPromise,
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: (route: string) => {
        if (route === "GET /api/boards") {
          return Promise.resolve([
            {
              id: "b1",
              name: "Timeline Triage",
              kind: "list",
              query: "tab=all&view=timeline",
            },
          ]);
        }
        if (route === "GET /api/base-branch-gates") {
          return Promise.resolve({
            gates: [
              {
                repo: "sidthe/grain",
                branch: "main",
                judged: false,
                reason: "failing",
                detail: "ci failed",
              },
            ],
          });
        }
        return Promise.resolve({});
      },
      getIfChanged: (route: string) =>
        route === "GET /api/tasks"
          ? tasksPromise
          : Promise.resolve({ body: {}, changed: true }),
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        {
          provide: GrainEvents,
          useValue: { events: () => new Subject<GrainEvent>() },
        },
      ],
    });

    const feed = TestBed.inject(TaskFeedStore);
    const tasks = TestBed.inject(TasksStore);
    feed.start();
    tasks.start();
    TestBed.flushEffects();

    // Tasks resolve first while /api/config is still pending.
    resolveTasks({
      body: [{ id: "1", title: "Task 1", state: "queued", priority: 5 }],
      changed: true,
    });
    await tasksPromise;
    TestBed.flushEffects();
    expect(tasks.live().map((t) => t.id)).toEqual(["1"]);
    expect(tasks.boards()).toEqual([]);
    expect(tasks.baseBranchGates()).toEqual([]);

    // Config resolves afterwards with boards:read and ci:read scopes.
    resolveConfig({
      access: {
        role: "dev",
        roles: ["dev"],
        full: false,
        scopes: ["tasks:read", "boards:read", "ci:read"],
      },
    });
    await configPromise;
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 10));
      TestBed.flushEffects();
    }

    expect(tasks.boards().map((b) => b.name)).toEqual(["Timeline Triage"]);
    expect(tasks.baseBranchGates().map((g) => g.repo)).toEqual([
      "sidthe/grain",
    ]);
    tasks.stop();
  });

  it("batches 20 submit details in 1 request, chunks >50 by 50, and keeps cached details on failure", async () => {
    const live = Array.from({ length: 20 }, (_, i) =>
      awaitingSubmitTask(`${i + 1}`),
    );
    let failSummaries = false;
    const calls: { route: string; params?: unknown; query?: string }[] = [];
    const fakeApi = {
      config: () => Promise.resolve({ access: null }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: () => Promise.resolve({}),
      getIfChanged: (route: string, params?: unknown, query?: string) => {
        calls.push({ route, params, query });
        if (route === "GET /api/tasks") {
          return Promise.resolve({
            body: structuredClone(live),
            changed: true,
          });
        }
        if (route === "GET /api/tasks/summaries") {
          if (failSummaries) {
            return Promise.reject(new Error("temporary 503"));
          }
          const ids =
            new URLSearchParams(query ?? "")
              .get("ids")
              ?.split(",")
              .filter(Boolean) ?? [];
          const out: Record<string, TaskDetail> = {};
          for (const id of ids) {
            out[id] = {
              ...awaitingSubmitTask(id),
              mergeGates: [{ id: "ci", label: "CI", state: "passing" }],
              comments: [],
              attempts: [],
            } as TaskDetail;
          }
          return Promise.resolve({ body: { tasks: out }, changed: true });
        }
        return Promise.resolve({ body: {}, changed: true });
      },
    };
    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        {
          provide: GrainEvents,
          useValue: { events: () => new Subject<GrainEvent>() },
        },
      ],
    });
    const tasks = TestBed.inject(TasksStore);
    tasks.start();
    await new Promise((r) => setTimeout(r, 10));
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 10));

    const batchCalls = calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    const singleCalls = calls.filter((c) => c.route === "GET /api/tasks/{id}");
    expect(singleCalls.length).toBe(0);
    expect(batchCalls.length).toBe(1);
    const q = new URLSearchParams(batchCalls[0].query ?? "");
    expect(q.get("ids")!.split(",").length).toBe(20);
    expect(q.get("comments_limit")).toBe("1");
    expect(q.get("attempts_limit")).toBe("1");
    expect(Object.keys(tasks.details()).length).toBe(20);
    expect(tasks.details()["1"]?.mergeGates?.[0]?.state).toBe("passing");

    // Failure leaves already cached details intact.
    failSummaries = true;
    await tasks.refresh();
    await new Promise((r) => setTimeout(r, 10));
    expect(tasks.details()["1"]?.mergeGates?.[0]?.state).toBe("passing");

    // 75 tasks split cleanly into chunks of 50 and 25.
    failSummaries = false;
    calls.length = 0;
    live.splice(
      0,
      live.length,
      ...Array.from({ length: 75 }, (_, i) => awaitingSubmitTask(`${i + 1}`)),
    );
    await tasks.refresh();
    const chunkCalls = calls.filter(
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
    expect(Object.keys(tasks.details()).length).toBe(75);
    tasks.stop();
  });
});
