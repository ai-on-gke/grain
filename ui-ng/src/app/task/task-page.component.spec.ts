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

import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { Router, provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { TaskDetail } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskPageComponent, backFrom, duration } from "./task-page.component";
import { TaskStore } from "./task.store";

type Call = { method: string; route: string; params?: unknown; body?: unknown };

class FakeApi {
  detail: TaskDetail = {
    id: "7",
    title: "Fix the flaky merge test",
    state: "awaiting_reply",
    repo: "acme/web",
    priority: 5,
    authorLabel: "Ana",
    description: "The **merge** test fails one run in ten.",
    questionSummary: "Should I also fix the retry?",
    questionCommentId: 2,
    branch: "grain/task-7",
    pullRequest: "acme/web#42",
    dependsOn: ["3"],
    comments: [
      {
        id: 1,
        author: "ana",
        authorKind: "human",
        authorLabel: "Ana",
        body: "Please fix it",
      },
      {
        id: 2,
        author: "agent",
        authorKind: "agent",
        body: "Should I also fix the retry?",
      },
    ],
    attempts: [
      {
        number: 1,
        startedAt: "2026-11-11T10:00:00Z",
        finishedAt: "2026-11-11T10:04:12Z",
        outcome: "succeeded",
        model: "fast",
      },
      {
        number: 2,
        startedAt: "2026-11-11T11:00:00Z",
        outcome: "",
        model: "fast",
      },
    ],
  };
  tree = {
    origin: "task:7",
    rootTask: "7",
    done: 1,
    total: 2,
    nodes: [
      {
        id: "7",
        title: "Fix the flaky merge test",
        status: "running",
        done: false,
        root: true,
        children: [
          { id: "8", title: "Review", status: "merged", done: true },
          { id: "9", title: "Docs", status: "queued", done: false },
        ],
      },
    ],
  };
  calls: Call[] = [];
  transcripts: string[] = [];

  getIfChanged = () =>
    Promise.resolve({ body: structuredClone(this.detail), changed: true });
  get = (route: string, params?: { id: string; number?: string }) => {
    if (route === "GET /api/tasks/{id}/tree") return Promise.resolve(this.tree);
    this.transcripts.push(params?.number ?? "");
    return Promise.resolve({ transcript: `> run ${params?.number}\nok` });
  };
  private rec(method: string, route: string, params?: unknown, body?: unknown) {
    this.calls.push({ method, route, params, body });
    return Promise.resolve({});
  }
  post = (r: string, p?: unknown, b?: unknown) => this.rec("POST", r, p, b);
  patch = (r: string, p?: unknown, b?: unknown) => this.rec("PATCH", r, p, b);
  // The shell's Inbox store, refreshed after actions.
  config = () => Promise.resolve({ access: null });
  tasks = () => Promise.resolve([]);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
}

async function render(
  api: FakeApi,
  tab?: string,
): Promise<ComponentFixture<TaskPageComponent>> {
  TestBed.configureTestingModule({
    imports: [TaskPageComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const f = TestBed.createComponent(TaskPageComponent);
  f.componentRef.setInput("id", "7");
  if (tab) f.componentRef.setInput("tab", tab);
  f.autoDetectChanges();
  await TestBed.inject(TaskStore).refresh();
  await settle(f);
  return f;
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r));
  await f.whenStable();
}

function q<T extends HTMLElement = HTMLElement>(
  f: ComponentFixture<unknown>,
  sel: string,
): T | null {
  return (f.nativeElement as HTMLElement).querySelector<T>(sel);
}

describe("TaskPageComponent", () => {
  afterEach(() => {
    TestBed.inject(TaskStore).close();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("shows the header, the question, the conversation and the details", async () => {
    const f = await render(new FakeApi());
    expect(q(f, "#task-title")!.textContent).toContain("#7");
    expect(q(f, '[data-testid="task-state"]')!.textContent).toContain("Asked");
    expect(q(f, '[data-testid="act-reply"]')).toBeTruthy();
    expect(q(f, '[data-testid="act-approve"]')).toBeNull();
    expect(q(f, '[data-testid="question"]')!.textContent).toContain(
      "fix the retry",
    );
    expect(q(f, '[data-testid="tmsg-2"]')!.classList).toContain("is-question");
    expect(
      q(f, '[data-testid="task-details"] .description strong')!.textContent,
    ).toBe("merge");
  });

  it("sends a reply with Ctrl+Enter and keeps text typed meanwhile", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const box = q<HTMLTextAreaElement>(f, '[data-testid="task-composer"]')!;
    expect(box.getAttribute("aria-label")).toBe("Reply to the agent");
    box.value = "Yes, fix both";
    box.dispatchEvent(new Event("input"));
    box.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        cancelable: true,
      }),
    );
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/comments",
      params: { id: "7" },
      body: { body: "Yes, fix both", attachments: [] },
    });
    expect(TestBed.inject(TaskStore).draft()).toBe("");
  });

  it("offers Approve for a proposal and Retry for a failed task", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "proposed" };
    let f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-approve"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/approve",
      params: { id: "7" },
    });
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = { ...api.detail, state: "failed" };
    f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-retry"]')!.click();
    await settle(f);
    expect(api.calls.at(-1)).toMatchObject({
      route: "POST /api/tasks/{id}/retry",
    });
  });

  it("asks before closing a running task and offers no Undo", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "running" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-close"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="close-confirm"]')!.textContent).toContain(
      "stops the run",
    );
    expect(api.calls).toHaveLength(0);
    q<HTMLButtonElement>(f, '[data-testid="close-yes"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/close",
      body: { close_pull_request: false },
    });
    expect(TestBed.inject(SnackbarService).current()?.action).toBeUndefined();
  });

  it("lists runs newest first and opens the newest transcript, live while it runs", async () => {
    const api = new FakeApi();
    const f = await render(api, "runs");
    const rows = [
      ...(f.nativeElement as HTMLElement).querySelectorAll(
        ".runs-table tbody tr",
      ),
    ];
    expect(rows.map((r) => r.querySelector("td")?.textContent?.trim())).toEqual(
      ["2", "1"],
    );
    expect(api.transcripts).toEqual(["2"]);
    expect(q(f, '[data-testid="transcript"]')!.textContent).toContain(
      "Live, every 5 s",
    );
    expect(q(f, ".transcript-text")!.textContent).toContain("> run 2");
    q<HTMLButtonElement>(f, '[data-testid="transcript-1"]')!.click();
    await settle(f);
    expect(api.transcripts.at(-1)).toBe("1");
    expect(q(f, '[data-testid="transcript"]')!.textContent).not.toContain(
      "Live",
    );
  });

  it("shows the work tree with progress and links the pull request", async () => {
    const api = new FakeApi();
    let f = await render(api, "tree");
    expect(q(f, '[data-testid="tree-summary"]')!.textContent).toContain(
      "1 of 2 done",
    );
    expect(q(f, '.tree a[href="/task/9"]')).toBeTruthy();
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    f = await render(api, "changes");
    const pr = q<HTMLAnchorElement>(f, '[data-testid="changes"] a')!;
    expect(pr.getAttribute("href")).toBe("https://github.com/acme/web/pull/42");
  });

  it("opens Follow up waiting on this task, on its repository", async () => {
    const f = await render(new FakeApi());
    const create = TestBed.inject(CreateTaskService);
    const open = vi.spyOn(create, "open").mockImplementation(() => undefined);
    q<HTMLButtonElement>(f, '[data-testid="act-follow-up"]')!.click();
    expect(open).toHaveBeenCalledWith(
      expect.objectContaining({ dependsOn: ["7"], repo: "acme/web" }),
    );
  });

  it("says when the task does not exist", async () => {
    const api = new FakeApi();
    api.getIfChanged = () =>
      new Promise((_, reject) => setTimeout(() => reject({ status: 404 })));
    const f = await render(api);
    expect(q(f, '[data-testid="task-error"]')!.textContent).toContain(
      "There is no task #7",
    );
  });

  it("never offers a reply on a task parked for a secret", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      pendingSecret: { name: "GH_TOKEN" },
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="act-reply"]')).toBeNull();
    expect(q(f, '[data-testid="task-composer"]')).toBeNull();
    expect(q(f, '[data-testid="park"]')!.textContent).toContain("a secret");
  });

  it("offers Resume for a stopped turn and Withdraw approval for queued work", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, turnStopped: true };
    let f = await render(api);
    expect(q(f, '[data-testid="act-retry"]')!.textContent).toContain("Resume");
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = { ...api.detail, state: "queued", turnStopped: false };
    f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-withdraw"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/withdraw-approval",
    });
  });

  it("undoes on the task that was acted on, even after moving to another", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "queued" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-defer"]')!.click();
    await settle(f);
    const snacks = TestBed.inject(SnackbarService);
    const undo = snacks.current()!;
    f.componentRef.setInput("id", "99");
    await settle(f);
    await snacks.runAction(undo.id);
    expect(api.calls.at(-1)).toMatchObject({
      route: "POST /api/tasks/{id}/undefer",
      params: { id: "7" },
    });
  });

  it("forgets the transcript when moving to another task", async () => {
    const api = new FakeApi();
    const f = await render(api, "runs");
    const store = TestBed.inject(TaskStore);
    expect(store.transcriptFor()).toBe(2);
    store.open("99");
    expect(store.transcriptFor()).toBeNull();
    expect(store.transcript()).toBeNull();
    await settle(f);
  });
});

describe("duration", () => {
  it("formats run durations", () => {
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T10:00:38Z")).toBe(
      "38s",
    );
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T10:04:12Z")).toBe(
      "4m 12s",
    );
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T12:05:00Z")).toBe(
      "2h 5m",
    );
  });

  describe("full screen (M7)", () => {
    it("names where the person came from and goes back there with Escape", async () => {
      TestBed.configureTestingModule({
        imports: [TaskPageComponent],
        providers: [
          provideRouter([{ path: "**", children: [] }]),
          { provide: GrainApi, useValue: new FakeApi() },
          { provide: GrainEvents, useValue: { events: () => NEVER } },
        ],
      });
      const f = TestBed.createComponent(TaskPageComponent);
      f.componentRef.setInput("id", "7");
      f.componentRef.setInput("from", "tasks:merging");
      f.autoDetectChanges();
      await TestBed.inject(TaskStore).refresh();
      await settle(f);
      const back = q<HTMLAnchorElement>(f, '[data-testid="task-back"]')!;
      expect(back.textContent).toContain("← Tasks · Submit & land");
      expect(back.getAttribute("href")).toBe("/tasks?tab=merging&task=7");

      const nav = vi
        .spyOn(TestBed.inject(Router), "navigate")
        .mockResolvedValue(true);
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
      expect(nav).toHaveBeenCalledWith(["/tasks"], {
        queryParams: { tab: "merging", task: "7" },
      });
      // The full page has no pane controls.
      expect(q(f, '[data-testid="pane-expand"]')).toBeNull();
    });

    it("reads ?from= into the back link", () => {
      expect(backFrom("inbox", "7")).toEqual({
        label: "Inbox",
        commands: ["/inbox"],
      });
      expect(backFrom("tasks:asked", "7")).toEqual({
        label: "Tasks · Asked",
        commands: ["/tasks"],
        query: { tab: "asked", task: "7" },
      });
      expect(backFrom(undefined, "7")).toEqual({
        label: "Tasks",
        commands: ["/tasks"],
        query: { task: "7" },
      });
      expect(backFrom("tasks:nonsense", "7").label).toBe("Tasks");
    });

    it("turns Submit off while CI is failing and shows CI in the header", async () => {
      const api = new FakeApi();
      api.detail = {
        ...api.detail,
        state: "awaiting_submit",
        mergeGates: [
          { id: "ci", label: "CI", state: "failing" },
          { id: "approval", label: "Approval", state: "needed" },
        ],
      };
      const f = await render(api);
      const submit = q<HTMLButtonElement>(f, '[data-testid="act-submit"]')!;
      expect(submit.disabled).toBe(true);
      expect(q(f, '[data-testid="task-ci"]')!.textContent).toContain("✕ CI");
      submit.click();
      await settle(f);
      expect(api.calls).toEqual([]);
    });
  });
});
