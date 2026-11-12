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
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { ConsoleSessionList, Task, TaskDetail } from "../api/models";
import { SessionsStore } from "../sessions/sessions.store";
import { DirectorShaperComponent } from "./director-shaper.component";

class FakeApi {
  sessions = [{ id: "director", title: "Director", taskId: "1" }];
  tasks: Task[] = [{ id: "1", title: "Director", state: "awaiting_reply" }];
  details: Record<string, TaskDetail> = {
    "1": {
      id: "1",
      title: "Director",
      state: "awaiting_reply",
      comments: [
        {
          id: 2,
          author: "grain",
          authorKind: "automation",
          body: [
            "Here is a sharper task definition:",
            "```grain-draft",
            JSON.stringify({
              title: "Add HTTP 429 retry jitter",
              priority: 8,
              description: "Implement jittered backoff on HTTP 429.",
            }),
            "```",
          ].join("\n"),
        },
      ],
    },
  };
  posted: unknown[] = [];

  config = () => Promise.resolve({});
  get = () => {
    const list: ConsoleSessionList = { sessions: this.sessions };
    return Promise.resolve(list);
  };
  getIfChanged = (route: string, params?: { id: string }) => {
    if (route === "GET /api/tasks") {
      return Promise.resolve({ body: this.tasks, changed: true });
    }
    return Promise.resolve({
      body: this.details[params?.id ?? "1"],
      changed: true,
    });
  };
  post = (route: string, params?: unknown, body?: unknown) => {
    this.posted.push({ route, params, body });
    return Promise.resolve({});
  };
}

describe("DirectorShaperComponent", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("extracts proposed grain-draft diffs and emits applyDraft / applyAndSubmit", async () => {
    const api = new FakeApi();
    TestBed.configureTestingModule({
      imports: [DirectorShaperComponent],
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const f = TestBed.createComponent(DirectorShaperComponent);
    f.componentRef.setInput("mode", "edit");
    f.componentRef.setInput("shapeTaskId", "77");
    f.componentRef.setInput("current", {
      title: "Old title",
      description: "Old desc",
      repo: "acme/web",
      base: "main",
      priority: 5,
      model: "",
      dependsOn: [],
    });
    const applied: unknown[] = [];
    const saved: unknown[] = [];
    f.componentInstance.applyDraft.subscribe((p) => applied.push(p));
    f.componentInstance.applyAndSubmit.subscribe((p) => saved.push(p));

    f.autoDetectChanges();
    await TestBed.inject(SessionsStore).refresh();
    await f.whenStable();
    f.detectChanges();

    const el = f.nativeElement as HTMLElement;
    const diffCard = el.querySelector('[data-testid="shaper-diff-card"]');
    expect(diffCard?.textContent).toContain("Add HTTP 429 retry jitter");
    expect(diffCard?.textContent).toContain("Old title");

    (
      el.querySelector('[data-testid="shaper-apply"]') as HTMLButtonElement
    ).click();
    expect(applied).toEqual([
      {
        title: "Add HTTP 429 retry jitter",
        priority: 8,
        description: "Implement jittered backoff on HTTP 429.",
      },
    ]);

    (
      el.querySelector('[data-testid="shaper-apply-save"]') as HTMLButtonElement
    ).click();
    expect(saved).toHaveLength(1);
    f.destroy();
  });
});
