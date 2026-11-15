// Copyright 2026 The grain Authors
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
import { NEVER } from "rxjs";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { AgentsStore } from "./agents.store";

function runningTask(id: string): Task {
  return {
    id,
    title: `Run ${id}`,
    state: "running",
    activity: `step ${id}`,
    activityAt: new Date().toISOString(),
  } as Task;
}

describe("AgentsStore", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("batches 20 running task details in 1 request, chunks >50 by 50, and keeps cached details on failure", async () => {
    let live = Array.from({ length: 20 }, (_, i) => runningTask(`${i + 1}`));
    let failSummaries = false;
    const calls: { route: string; params?: unknown; query?: string }[] = [];
    const fakeApi = {
      getIfChanged: (route: string, p?: { id?: string }, query?: string) => {
        calls.push({ route, params: p, query });
        if (route === "GET /api/tasks") {
          return Promise.resolve({
            body: structuredClone(live),
            changed: true,
          });
        }
        if (route === "GET /api/tasks/summaries") {
          if (failSummaries) {
            return Promise.reject(new Error("503"));
          }
          const ids =
            new URLSearchParams(query ?? "")
              .get("ids")
              ?.split(",")
              .filter(Boolean) ?? [];
          const tasks: Record<string, TaskDetail> = {};
          for (const id of ids) {
            tasks[id] = {
              ...runningTask(id),
              attempts: [{ number: 1, model: "model-x" }],
              comments: [],
            } as unknown as TaskDetail;
          }
          return Promise.resolve({ body: { tasks }, changed: true });
        }
        return Promise.resolve({ body: {}, changed: true });
      },
      get: () => Promise.resolve({ host: { cpus: 4 } }),
      post: () => Promise.resolve({}),
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const store = TestBed.inject(AgentsStore);
    store.start();
    await store.refresh();
    await new Promise((r) => setTimeout(r, 10));

    const summaryCalls = calls.filter(
      (c) => c.route === "GET /api/tasks/summaries",
    );
    const singleCalls = calls.filter((c) => c.route === "GET /api/tasks/{id}");
    expect(singleCalls.length).toBe(0);
    expect(summaryCalls.length).toBe(1);
    const q = new URLSearchParams(summaryCalls[0].query ?? "");
    expect(q.get("ids")!.split(",").length).toBe(20);
    expect(q.get("comments_limit")).toBe("1");
    expect(q.get("attempts_limit")).toBe("3");
    expect(Object.keys(store.details()).length).toBe(20);

    // Slow poll failure leaves existing cached details intact for running tasks.
    failSummaries = true;
    await store.refreshSlow();
    expect(Object.keys(store.details()).length).toBe(20);
    expect(store.details()["1"]?.attempts?.[0]?.model).toBe("model-x");

    // 75 uncached running tasks chunk cleanly into 50 + 25.
    failSummaries = false;
    calls.length = 0;
    live = Array.from({ length: 75 }, (_, i) => runningTask(`${101 + i}`));
    await store.refresh();
    await new Promise((r) => setTimeout(r, 10));

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
    expect(Object.keys(store.details()).length).toBe(75);
    store.stop();
  });
});
