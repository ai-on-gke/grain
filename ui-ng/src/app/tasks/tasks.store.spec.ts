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
import type { AccessInfo } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { TasksStore } from "./tasks.store";

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

  it("awaits /api/config before loadBoards so a repo-limited viewer never fires GET /api/boards on cold load", async () => {
    let resolveConfig!: (cfg: Record<string, unknown>) => void;
    const configPromise = new Promise<Record<string, unknown>>((r) => {
      resolveConfig = r;
    });
    const getRoutes: string[] = [];
    const fakeApi = {
      config: () => configPromise,
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: (route: string) => {
        getRoutes.push(route);
        return Promise.resolve([]);
      },
      getIfChanged: () => Promise.resolve({ body: [], changed: true }),
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
    TestBed.flushEffects();
    expect(getRoutes).not.toContain("GET /api/boards");

    resolveConfig({
      access: {
        role: "widgets/viewer",
        roles: ["widgets/viewer"],
        full: false,
        scopes: ["tasks:read", "boards:read"],
        repoLimits: {
          "tasks:read": ["acme/widgets"],
          "boards:read": ["acme/widgets"],
        },
      },
    });
    await configPromise;
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 10));
      TestBed.flushEffects();
    }

    expect(getRoutes).not.toContain("GET /api/boards");
    expect(tasks.boards()).toEqual([]);
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

  it("fetches GET /api/boards only once when access.setConfig receives fresh objects with the same access", async () => {
    const sameAccess: AccessInfo = {
      role: "dev",
      roles: ["dev"],
      full: false,
      scopes: ["tasks:read", "boards:read", "ci:read"],
      repoLimits: { "tasks:read": ["acme/widgets"] },
    };
    const getRoutes: string[] = [];
    const fakeApi = {
      config: () => Promise.resolve({ access: structuredClone(sameAccess) }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: (route: string) => {
        getRoutes.push(route);
        if (route === "GET /api/boards") {
          return Promise.resolve([
            { id: "b1", name: "Triage", kind: "list", query: "tab=all" },
          ]);
        }
        return Promise.resolve({});
      },
      getIfChanged: () => Promise.resolve({ body: [], changed: true }),
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

    const access = TestBed.inject(AccessService);
    const tasks = TestBed.inject(TasksStore);
    tasks.start();
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 10));
      TestBed.flushEffects();
    }
    expect(getRoutes.filter((r) => r === "GET /api/boards")).toHaveLength(1);
    expect(
      getRoutes.filter((r) => r === "GET /api/base-branch-gates"),
    ).toHaveLength(1);

    // Simulates AppComponent / Settings / Fleet polling /api/config and calling
    // access.setConfig with freshly parsed objects whose access role is unchanged.
    access.setConfig({
      access: structuredClone(sameAccess),
      version: "v1",
      agentPause: { paused: true, reason: "quota" },
    });
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 20));

    access.setConfig({
      access: structuredClone(sameAccess),
      version: "v2",
      agentPause: null,
    });
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 20));

    expect(getRoutes.filter((r) => r === "GET /api/boards")).toHaveLength(1);
    expect(
      getRoutes.filter((r) => r === "GET /api/base-branch-gates"),
    ).toHaveLength(1);

    // A genuine role change mid-session still reloads aux data once.
    const limitedBoards: AccessInfo = {
      ...structuredClone(sameAccess),
      repoLimits: {
        "tasks:read": ["acme/widgets"],
        "boards:read": ["acme/widgets"],
      },
    };
    access.setConfig({ access: structuredClone(limitedBoards) });
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 20));
    expect(tasks.boards()).toEqual([]);

    const upgraded: AccessInfo = {
      role: "operator",
      roles: ["operator"],
      full: true,
      scopes: ["tasks:read", "boards:read", "ci:read"],
    };
    access.setConfig({ access: structuredClone(upgraded) });
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 20));
    access.setConfig({ access: structuredClone(upgraded), version: "v3" });
    TestBed.flushEffects();
    await new Promise((r) => setTimeout(r, 20));

    expect(getRoutes.filter((r) => r === "GET /api/boards")).toHaveLength(2);
    expect(tasks.boards().map((b) => b.name)).toEqual(["Triage"]);
    tasks.stop();
  });
});
