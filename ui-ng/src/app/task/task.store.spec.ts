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
import { afterEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
import { DETAIL_QUERY, POLL_MS, TaskStore } from "./task.store";

describe("TaskStore", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("skips 3s detail refetches while the task row in TaskFeedStore is unchanged and refetches when updatedAt moves", async () => {
    vi.useFakeTimers();
    let row: Task = {
      id: "5594",
      title: "Fix poll fan-out",
      state: "awaiting_submit",
      updatedAt: "2026-11-16T00:00:00Z",
    };
    const detailQueries: string[] = [];
    let transcriptCalls = 0;
    let failTranscript = false;
    const events$ = new Subject<GrainEvent>();

    const fakeApi = {
      config: () => Promise.resolve({ access: null }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      getIfChanged: (
        route: string,
        params?: Record<string, string>,
        query?: string,
      ) => {
        if (route === "GET /api/tasks") {
          return Promise.resolve({ body: [row], changed: true });
        }
        if (route === "GET /api/tasks/{id}" && params?.["id"] === "5594") {
          detailQueries.push(query ?? "");
          return Promise.resolve({
            body: {
              ...row,
              comments: [],
              attempts: [{ number: 1 }],
            } as TaskDetail,
            changed: true,
          });
        }
        return Promise.resolve({ body: {}, changed: false });
      },
      get: (route: string) => {
        if (route === "GET /api/tasks/{id}/attempts/{number}/transcript") {
          transcriptCalls++;
          if (failTranscript) {
            return Promise.reject({
              status: 503,
              headers: { get: () => "15" },
              error: { error: "proxy unavailable" },
            });
          }
          return Promise.resolve({ transcript: "step 1" });
        }
        return Promise.resolve({});
      },
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        { provide: GrainEvents, useValue: { events: () => events$ } },
      ],
    });
    const feed = TestBed.inject(TaskFeedStore);
    const store = TestBed.inject(TaskStore);

    await feed.refresh();
    store.open("5594");
    await vi.advanceTimersByTimeAsync(50);
    expect(detailQueries).toEqual([DETAIL_QUERY]);

    // Five 3s poll ticks (15s) and an SSE event for another task make 0 detail reads.
    events$.next({ type: "task.comment", data: { taskId: "9999" } });
    await vi.advanceTimersByTimeAsync(POLL_MS * 5 + 300);
    expect(detailQueries).toEqual([DETAIL_QUERY]);

    // When task 5594's updatedAt advances in the feed, TaskStore refetches once.
    row = { ...row, updatedAt: "2026-11-16T00:01:00Z" };
    await feed.refresh();
    TestBed.flushEffects();
    await vi.advanceTimersByTimeAsync(50);
    expect(detailQueries.length).toBe(2);

    // Live transcript poll backs off on 503s (honouring Retry-After: 15).
    store.watchTranscript(1, true);
    await vi.advanceTimersByTimeAsync(50);
    expect(transcriptCalls).toBe(1);
    failTranscript = true;
    await vi.advanceTimersByTimeAsync(5_000);
    expect(transcriptCalls).toBe(2);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(transcriptCalls).toBe(2);

    store.close("5594");
  });
});
