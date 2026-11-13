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
});
