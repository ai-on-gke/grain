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
import { provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { ConsoleSessionList, Task, TaskDetail } from "../api/models";
import { SessionsStore } from "../sessions/sessions.store";
import { DirectorContextService } from "./director-context.service";
import { DirectorDrawerComponent } from "./director-drawer.component";

class FakeApi {
  sessions = [{ id: "director", title: "Director", taskId: "1" }];
  tasks: Task[] = [{ id: "1", title: "Director", state: "running" }];
  details: Record<string, TaskDetail> = {
    "1": {
      id: "1",
      title: "Director",
      state: "running",
      attempts: [{ number: 2, startedAt: "2026-11-12T00:00:00Z" }],
      comments: [
        {
          id: 1,
          author: "grain",
          authorKind: "automation",
          body: "How can I help?",
        },
      ],
    },
  };
  posted: unknown[] = [];

  config = () => Promise.resolve({ models: ["gemini-2.5-pro"] });
  get = (route: string) => {
    if (route === "GET /api/tasks/{id}/attempts/{number}/transcript") {
      return Promise.resolve({ transcript: "Inspecting workstream DAG..." });
    }
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

describe("DirectorDrawerComponent", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("renders Director thread, live transcript, context pill, and prepends active context on send", async () => {
    const api = new FakeApi();
    TestBed.configureTestingModule({
      imports: [DirectorDrawerComponent],
      providers: [
        provideRouter([{ path: "**", children: [] }]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const ctx = TestBed.inject(DirectorContextService);
    ctx.setSelection({
      kind: "task",
      id: "42",
      title: "Fix retry",
      state: "failed",
    });
    ctx.open();

    const f = TestBed.createComponent(DirectorDrawerComponent);
    f.autoDetectChanges();
    await TestBed.inject(SessionsStore).refresh();
    await f.whenStable();
    f.detectChanges();

    const el = f.nativeElement as HTMLElement;
    expect(
      el.querySelector('[data-testid="director-context-toggle"]')?.textContent,
    ).toContain("Task #42: Fix retry");
    expect(el.textContent).toContain("How can I help?");

    const ta = el.querySelector("textarea") as HTMLTextAreaElement;
    ta.value = "Diagnose why retry failed";
    ta.dispatchEvent(new Event("input"));
    f.detectChanges();
    const sendBtn = el.querySelector(
      'button[type="submit"]',
    ) as HTMLButtonElement;
    sendBtn.click();
    await f.whenStable();

    expect(api.posted).toContainEqual({
      route: "POST /api/tasks/{id}/comments",
      params: { id: "1" },
      body: {
        body: "> [Context: Task #42: Fix retry (failed)]\n\nDiagnose why retry failed",
        attachments: [],
      },
    });
    f.destroy();
  });
});
