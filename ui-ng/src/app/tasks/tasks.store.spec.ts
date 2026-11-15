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

  // Stop sends no more of a bulk action: the calls already in flight
  // finish, the rest are reported as skipped and left unchanged.
  it("stops a bulk action part-way and reports what it never sent", async () => {
    const sent: string[] = [];
    const release: (() => void)[] = [];
    const fakeApi = {
      config: () => Promise.resolve({ access: null }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: () => Promise.resolve({}),
      getIfChanged: () => Promise.resolve({ body: {}, changed: true }),
      post: (_route: string, params: { id: string }) => {
        sent.push(params.id);
        return new Promise<void>((r) => release.push(r));
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
    const ids = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"];
    const run = tasks.runNow(ids);
    await new Promise((r) => setTimeout(r, 0));
    expect(sent).toEqual(["1", "2", "3", "4"]);
    expect(tasks.batch()).toMatchObject({ key: "run-now", total: 10 });
    tasks.stopBatch();
    expect(tasks.batch()?.stopping).toBe(true);
    release.forEach((r) => r());
    expect(await run).toBe(6);
    expect(sent).toHaveLength(4);
    expect(tasks.lastBatch()).toEqual({
      done: ["1", "2", "3", "4"],
      failed: [],
      skipped: ["5", "6", "7", "8", "9", "10"],
    });
    expect(tasks.batch()).toBeNull();
    expect(tasks.actionError()).toContain("Stopped after 4 of 10");
  });
});
