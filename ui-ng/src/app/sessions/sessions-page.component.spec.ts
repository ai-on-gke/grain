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
import { provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type {
  ConsoleSession,
  ConsoleSessionList,
  Task,
  TaskDetail,
} from "../api/models";
import { SessionsPageComponent } from "./sessions-page.component";
import { SessionsStore } from "./sessions.store";

type Call = { method: string; route: string; params?: unknown; body?: unknown };

class FakeApi {
  sessions: ConsoleSession[] = [
    {
      id: "director",
      title: "Director",
      taskId: "1",
      capabilities: ["https://grain.dev/capabilities/grain#health={}"],
    },
    { id: "rel", title: "Release help", taskId: "9", capabilities: [] },
  ];
  offered = [
    { id: "self-debug", name: "Self debug" },
    { id: "api:task-admin", name: "API preset: task-admin" },
  ];
  taskList: Task[] = [
    { id: "1", title: "Director", state: "awaiting_reply" },
    { id: "9", title: "Release help", state: "running", activity: "reading" },
  ];
  details: Record<string, TaskDetail> = {
    "1": {
      id: "1",
      title: "Director",
      state: "awaiting_reply",
      comments: [
        {
          id: 1,
          author: "console-session",
          authorKind: "automation",
          body: "I'm **here**.",
        },
        {
          id: 2,
          author: "ana",
          authorKind: "human",
          authorLabel: "Ana",
          body: "File a task to bump Go",
        },
      ],
    },
    "9": { id: "9", title: "Release help", state: "running", comments: [] },
  };
  calls: Call[] = [];
  // fail makes the next call to a route throw this error once.
  fail: Record<string, unknown> = {};

  private record(
    method: string,
    route: string,
    params?: unknown,
    body?: unknown,
  ) {
    this.calls.push({ method, route, params, body });
    const err = this.fail[route];
    if (err) {
      delete this.fail[route];
      // Rejected later, as HttpClient does: a synchronously rejected
      // promise trips zone.js's unhandled-rejection check before await
      // attaches its handler.
      return new Promise<never>((_, reject) => setTimeout(() => reject(err)));
    }
    return null;
  }

  get = (route: string) => {
    const e = this.record("GET", route);
    if (e) return e;
    const list: ConsoleSessionList = {
      sessions: structuredClone(this.sessions),
      capabilities: this.offered,
    };
    return Promise.resolve(list);
  };
  reads: string[] = [];
  getIfChanged = (route: string, params?: { id: string }) => {
    this.reads.push(params ? `${route}:${params.id}` : route);
    if (route === "GET /api/tasks") {
      return Promise.resolve({ body: this.taskList, changed: true });
    }
    return Promise.resolve({
      body: this.details[params!.id],
      changed: true,
    });
  };
  post = (route: string, params?: unknown, body?: unknown) => {
    const e = this.record("POST", route, params, body);
    if (e) return e;
    if (route === "POST /api/console-sessions") {
      this.sessions = [
        ...this.sessions,
        { id: "new1", title: "New one", taskId: "12" },
      ];
      this.details["12"] = { id: "12", title: "New one", comments: [] };
      return Promise.resolve({
        sessions: structuredClone(this.sessions),
        created: "new1",
      });
    }
    if (route === "POST /api/console-sessions/{id}/restart") {
      this.sessions = this.sessions.map((x) =>
        x.id === "director" ? { ...x, taskId: "20" } : x,
      );
      this.taskList = [
        ...this.taskList,
        { id: "20", title: "Director", state: "awaiting_reply" },
      ];
      this.details["20"] = {
        id: "20",
        title: "Director",
        state: "awaiting_reply",
        comments: [{ id: 50, body: "Fresh start", handover: true }],
      };
      return Promise.resolve({ sessions: structuredClone(this.sessions) });
    }
    return Promise.resolve({});
  };
  put = (route: string, params?: unknown, body?: unknown) => {
    const e = this.record("PUT", route, params, body);
    if (e) return e;
    const b = body as { capabilities: string[] };
    const id = (params as { id: string }).id;
    this.sessions = this.sessions.map((s) =>
      s.id === id ? { ...s, capabilities: b.capabilities } : s,
    );
    return Promise.resolve({});
  };
  delete = (route: string, params?: unknown) => {
    const e = this.record("DELETE", route, params);
    if (e) return e;
    const id = (params as { id: string }).id;
    this.sessions = this.sessions.filter((s) => s.id !== id);
    return Promise.resolve({});
  };
  config = () => Promise.resolve({ access: null });
}

async function render(
  api: FakeApi,
  id?: string,
): Promise<ComponentFixture<SessionsPageComponent>> {
  TestBed.configureTestingModule({
    imports: [SessionsPageComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const fixture = TestBed.createComponent(SessionsPageComponent);
  if (id) fixture.componentRef.setInput("id", id);
  fixture.autoDetectChanges();
  await TestBed.inject(SessionsStore).refresh();
  await fixture.whenStable();
  return fixture;
}

// settle lets a click's promise chain (save, then read again) finish.
async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r));
  await TestBed.inject(SessionsStore).refresh();
  await f.whenStable();
}

function q(f: ComponentFixture<unknown>, sel: string): HTMLElement | null {
  return (f.nativeElement as HTMLElement).querySelector(sel);
}

function text(f: ComponentFixture<unknown>, sel: string): string {
  return q(f, sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";
}

describe("SessionsPageComponent", () => {
  afterEach(() => {
    TestBed.inject(SessionsStore).stop();
    TestBed.resetTestingModule();
  });

  it("lists the Director first with its status and last line, and shows its conversation", async () => {
    const api = new FakeApi();
    api.sessions = [...api.sessions].reverse();
    const f = await render(api);
    const items = [
      ...(f.nativeElement as HTMLElement).querySelectorAll(".session-item"),
    ];
    expect(items.map((i) => i.querySelector(".s-name")?.textContent)).toEqual([
      "Director",
      "Release help",
    ]);
    // /sessions shows the Director on a wide screen, but no conversation is
    // the page on a phone, so nothing is marked current until one is open.
    expect(items[0].getAttribute("aria-current")).toBeNull();
    expect(items[0].textContent).toContain("Asked");
    expect(items[0].textContent).toContain("File a task to bump Go");
    expect(items[1].textContent).toContain("Working");
    expect(q(f, '[data-testid="msg-1"] strong')?.textContent).toBe("here");
    expect(text(f, '[data-testid="msg-1"] .msg-author')).toBe("Grain");
    expect(q(f, '[data-testid="msg-2"]')?.classList).toContain("is-human");
    expect(q(f, '[data-testid="stop"]')).toBeNull();
  });

  it("sends with Enter, keeps Shift+Enter for a new line, and clears the draft once sent", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const box = q(f, '[data-testid="composer"]') as HTMLTextAreaElement;
    box.value = "Please file it";
    box.dispatchEvent(new Event("input"));
    box.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", shiftKey: true }),
    );
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
    box.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", cancelable: true }),
    );
    await f.whenStable();
    await TestBed.inject(SessionsStore).refresh();
    const sent = api.calls.find(
      (c) => c.route === "POST /api/tasks/{id}/comments",
    );
    expect(sent).toMatchObject({
      params: { id: "1" },
      body: { body: "Please file it", attachments: [] },
    });
    expect(TestBed.inject(SessionsStore).draftFor("director")).toBe("");
  });

  it("keeps the draft and shows the error when the message does not go through", async () => {
    const api = new FakeApi();
    api.fail["POST /api/tasks/{id}/comments"] = {
      status: 500,
      error: { error: "sandbox unavailable" },
    };
    const f = await render(api);
    const store = TestBed.inject(SessionsStore);
    store.setDraft("director", "hello");
    expect(await store.send()).toBe(false);
    await f.whenStable();
    expect(store.draftFor("director")).toBe("hello");
    expect(text(f, '[data-testid="action-error"]')).toContain(
      "sandbox unavailable",
    );
  });

  it("shows Stop and the activity line while a run is going", async () => {
    const api = new FakeApi();
    const f = await render(api, "rel");
    expect(text(f, '[data-testid="chat-status"]')).toMatch(/^Working:/);
    expect(text(f, '[data-testid="chat-activity"]')).toBe("reading");
    (q(f, '[data-testid="stop"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/console-sessions/{id}/cancel",
      ),
    ).toMatchObject({ params: { id: "rel" } });
  });

  it("toggles a capability with the list it was read from, and retries once when that list is stale", async () => {
    const api = new FakeApi();
    const f = await render(api);
    expect(q(f, '[data-testid="cap-api:task-admin"]')).toBeNull();
    expect(text(f, '[data-testid="cap-held"]')).toBe("health");
    const box = q(f, '[data-testid="cap-self-debug"]') as HTMLButtonElement;
    expect(box.getAttribute("aria-pressed")).toBe("false");

    // Somebody else granted something meanwhile: the first save is
    // refused as stale, the list is read again and the edit made on it.
    api.sessions[0] = {
      ...api.sessions[0],
      capabilities: [...api.sessions[0].capabilities!, "other"],
    };
    api.fail["PUT /api/console-sessions/{id}"] = {
      status: 409,
      error: { code: "stale" },
    };
    box.click();
    await settle(f);
    const puts = api.calls.filter((c) => c.method === "PUT");
    expect(puts).toHaveLength(2);
    expect(puts[0].body).toEqual({
      capabilities: [
        "https://grain.dev/capabilities/grain#health={}",
        "self-debug",
      ],
      expectCapabilities: ["https://grain.dev/capabilities/grain#health={}"],
    });
    expect(puts[1].body).toEqual({
      capabilities: [
        "https://grain.dev/capabilities/grain#health={}",
        "other",
        "self-debug",
      ],
      expectCapabilities: [
        "https://grain.dev/capabilities/grain#health={}",
        "other",
      ],
    });
    expect(
      (q(f, '[data-testid="cap-self-debug"]') as HTMLElement).getAttribute(
        "aria-pressed",
      ),
    ).toBe("true");
  });

  it("offers a fresh chat in two steps and carries the handover when asked", async () => {
    const api = new FakeApi();
    const f = await render(api);
    expect(q(f, '[data-testid="fresh-chat-handover"]')).toBeNull();
    (q(f, '[data-testid="fresh-chat-open"]') as HTMLButtonElement).click();
    await f.whenStable();
    (q(f, '[data-testid="fresh-chat-handover"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/console-sessions/{id}/restart",
      ),
    ).toMatchObject({
      params: { id: "director" },
      body: { carryHandover: true },
    });
  });

  it("starts a new session from the dialog", async () => {
    const api = new FakeApi();
    const f = await render(api);
    (q(f, '[data-testid="new-session"]') as HTMLButtonElement).click();
    await f.whenStable();
    const dialog = q(f, '[data-testid="new-session-dialog"]')!;
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    const name = q(f, '[data-testid="new-session-name"]') as HTMLInputElement;
    name.value = "New one";
    name.dispatchEvent(new Event("input"));
    (q(f, '[data-testid="new-session-create"]') as HTMLButtonElement).click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/console-sessions"),
    ).toMatchObject({ body: { title: "New one", attachments: [] } });
    expect(q(f, '[data-testid="new-session-dialog"]')).toBeNull();
  });

  it("asks before deleting a session and never offers it for the Director", async () => {
    const api = new FakeApi();
    const f = await render(api);
    expect(q(f, '[data-testid="delete-session"]')).toBeNull();
    TestBed.inject(SessionsStore).select("rel");
    await settle(f);
    (q(f, '[data-testid="delete-session"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="delete-dialog"]')?.getAttribute("role")).toBe(
      "alertdialog",
    );
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
    (q(f, '[data-testid="delete-confirm"]') as HTMLButtonElement).click();
    await settle(f);
    expect(api.calls.find((c) => c.method === "DELETE")).toMatchObject({
      params: { id: "rel" },
    });
  });

  it("refreshes a background session's status from the list and re-reads its messages when it moves", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const rel = () => q(f, '[data-testid="session-rel"]')!.textContent ?? "";
    expect(rel()).toContain("Working");
    const readsBefore = api.reads.filter((r) => r.endsWith(":9")).length;
    api.taskList = api.taskList.map((t) =>
      t.id === "9" ? { ...t, state: "awaiting_reply", updatedAt: "t2" } : t,
    );
    api.details["9"] = {
      ...api.details["9"],
      state: "awaiting_reply",
      comments: [{ id: 7, body: "Release notes are drafted" }],
    };
    await settle(f);
    expect(rel()).toContain("Asked");
    expect(rel()).toContain("Release notes are drafted");
    expect(api.reads.filter((r) => r.endsWith(":9")).length).toBe(
      readsBefore + 1,
    );
    // Nothing moved: no further read of the background conversation.
    await settle(f);
    expect(api.reads.filter((r) => r.endsWith(":9")).length).toBe(
      readsBefore + 1,
    );
  });

  it("sends once however fast Enter is pressed, and keeps text typed meanwhile", async () => {
    const api = new FakeApi();
    let release!: () => void;
    const post = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route !== "POST /api/tasks/{id}/comments") {
        return post(route, params, body);
      }
      api.calls.push({ method: "POST", route, params, body });
      return new Promise((r) => (release = () => r({})));
    };
    const f = await render(api);
    const store = TestBed.inject(SessionsStore);
    store.setDraft("director", "once");
    const first = store.send();
    expect(await store.send()).toBe(false);
    store.setDraft("director", "once and more");
    release();
    expect(await first).toBe(true);
    await f.whenStable();
    expect(
      api.calls.filter((c) => c.route === "POST /api/tasks/{id}/comments"),
    ).toHaveLength(1);
    expect(store.draftFor("director")).toBe("once and more");
  });

  it("will not start a session with nothing in it, and shows a refusal inside the dialog", async () => {
    const api = new FakeApi();
    const f = await render(api);
    (q(f, '[data-testid="new-session"]') as HTMLButtonElement).click();
    await f.whenStable();
    const create = q(
      f,
      '[data-testid="new-session-create"]',
    ) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    const name = q(f, '[data-testid="new-session-name"]') as HTMLInputElement;
    name.value = "Taken";
    name.dispatchEvent(new Event("input"));
    await f.whenStable();
    expect(create.disabled).toBe(false);
    api.fail["POST /api/console-sessions"] = {
      status: 409,
      error: { error: "a session with that name exists" },
    };
    create.click();
    await settle(f);
    expect(text(f, '[data-testid="new-session-error"]')).toContain(
      "a session with that name exists",
    );
    expect(q(f, ".view-panel")?.hasAttribute("inert")).toBe(true);
  });

  it("moves the conversation to the new task after a fresh chat", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await TestBed.inject(SessionsStore).restart(true);
    await settle(f);
    expect(q(f, '[data-testid="msg-50"]')?.textContent).toContain(
      "Fresh start",
    );
    expect(q(f, '[data-testid="msg-50"]')?.classList).toContain("is-handover");
  });

  it("puts focus back on Delete when the confirmation is cancelled", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = new FakeApi();
    const f = await render(api, "rel");
    (q(f, '[data-testid="delete-session"]') as HTMLButtonElement).click();
    await f.whenStable();
    vi.advanceTimersByTime(5);
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    (document.activeElement as HTMLButtonElement).click();
    await f.whenStable();
    vi.advanceTimersByTime(5);
    expect(document.activeElement).toBe(q(f, '[data-testid="delete-session"]'));
    vi.useRealTimers();
  });

  it("stops polling when the page goes away", async () => {
    vi.useFakeTimers();
    const api = new FakeApi();
    const f = await render(api);
    f.destroy();
    const n = api.calls.length + api.reads.length;
    vi.advanceTimersByTime(30000);
    expect(api.calls.length + api.reads.length).toBe(n);
    vi.useRealTimers();
  });

  it("opens New session when reached with ?new=1 from the create menu", async () => {
    TestBed.configureTestingModule({
      imports: [SessionsPageComponent],
      providers: [
        provideRouter([{ path: "**", children: [] }]),
        { provide: GrainApi, useValue: new FakeApi() },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const f = TestBed.createComponent(SessionsPageComponent);
    f.componentRef.setInput("new", "1");
    f.autoDetectChanges();
    await TestBed.inject(SessionsStore).refresh();
    await f.whenStable();
    expect(q(f, '[data-testid="new-session-dialog"]')).toBeTruthy();
  });
});
