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
import { AgentsStore } from "../agents/agents.store";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import type { Task } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { normalizeActorKind } from "../shared/actor-chip.component";
import { statusShapeFor } from "../shared/status-icon.component";
import { TasksStore } from "../tasks/tasks.store";
import { WorkstreamsStore } from "../workstreams/workstreams.store";
import { TaskFeedStore } from "./task-feed.service";

describe("TaskFeedStore unified open-task feed (F9)", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("updates InboxStore, TasksStore, WorkstreamsStore, and AgentsStore atomically on the same tick", async () => {
    const events$ = new Subject<GrainEvent>();
    let serverTasks: Task[] = [
      { id: "1", title: "Initial run", state: "running" },
      { id: "2", title: "Queued work", state: "queued", priority: 7 },
    ];

    const fakeApi = {
      config: () => Promise.resolve({ access: null }),
      openNotifications: () => Promise.resolve([]),
      localScriptApprovals: () => Promise.resolve([]),
      get: () => Promise.resolve({ sandboxes: [] }),
      getIfChanged: (route: string) => {
        if (route === "GET /api/tasks") {
          return Promise.resolve({
            body: structuredClone(serverTasks),
            changed: true,
          });
        }
        return Promise.resolve({ body: { attempts: [] }, changed: true });
      },
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: fakeApi },
        { provide: GrainEvents, useValue: { events: () => events$ } },
      ],
    });

    const feed = TestBed.inject(TaskFeedStore);
    const inbox = TestBed.inject(InboxStore);
    const tasks = TestBed.inject(TasksStore);
    const workstreams = TestBed.inject(WorkstreamsStore);
    const agents = TestBed.inject(AgentsStore);

    feed.start(true);
    await feed.refresh();

    // Every store reads the exact same signal reference — zero skew possible.
    expect(inbox.tasks()).toBe(feed.tasks());
    expect(tasks.live()).toBe(feed.tasks());
    expect(workstreams.openTasks()).toBe(feed.tasks());
    expect(agents.tasks()).toBe(feed.tasks());
    expect(feed.runningCount()).toBe(1);
    expect(agents.agents().length).toBe(1);
    expect(feed.queuedTasks().length).toBe(1);

    // Push an SSE task.updated event when a second agent starts and a proposal arrives.
    serverTasks = [
      { id: "1", title: "Initial run", state: "running" },
      { id: "2", title: "Now running too", state: "running" },
      { id: "3", title: "Proposed follow-up", state: "proposed" },
    ];
    events$.next({ type: "task.updated", id: "2" });
    await new Promise((r) => setTimeout(r, 10));

    expect(feed.runningCount()).toBe(2);
    expect(agents.agents().length).toBe(2);
    expect(feed.queuedTasks().length).toBe(0);
    expect(tasks.live().filter((t) => t.state === "running").length).toBe(
      feed.runningCount(),
    );
    expect(inbox.decisions().map((d) => d.taskId)).toEqual(["3"]);

    feed.stop(true);
  });

  it("maps CVD status icons and shape-coded actor kinds distinctly", () => {
    const shapes = new Set([
      statusShapeFor("reply", "reply"),
      statusShapeFor("proposal", "proposal"),
      statusShapeFor("blast", "quiet"),
      statusShapeFor("error", "error"),
      statusShapeFor("healthy", "healthy"),
      statusShapeFor("setup", "working"),
    ]);
    expect(shapes.size).toBe(6);

    expect(normalizeActorKind("human", "Roman Arcea")).toBe("human");
    expect(normalizeActorKind("agent", "claude-opus")).toBe("agent");
    expect(normalizeActorKind("automation", "console-session")).toBe("grain");
    expect(normalizeActorKind(undefined, "grain")).toBe("grain");
    expect(normalizeActorKind("service", "github-webhook")).toBe("service");
  });
});
