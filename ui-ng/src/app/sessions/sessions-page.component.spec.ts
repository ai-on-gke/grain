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
import { COMMENTS_PAGE_SIZE, SessionsStore } from "./sessions.store";
import { AccessService } from "../services/access.service";

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
          attachments: [
            {
              id: 77,
              filename: "trace.png",
              contentType: "image/png",
              size: 1024,
            },
          ],
        },
      ],
    },
    "9": {
      id: "9",
      title: "Release help",
      state: "running",
      attempts: [{ number: 3, startedAt: "2026-11-12T00:00:00Z" }],
      comments: [],
    },
  };
  calls: Call[] = [];
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
      return new Promise<never>((_, reject) => setTimeout(() => reject(err)));
    }
    return null;
  }

  get = (route: string) => {
    const e = this.record("GET", route);
    if (e) return e;
    if (route === "GET /api/agent-models") {
      return Promise.resolve({
        available: true,
        models: [
          {
            id: "gemini-3.1-pro-high",
            label: "Gemini 3.1 Pro",
            effort: "high",
            family: "gemini-3.1-pro",
          },
        ],
      });
    }
    if (route === "GET /api/tasks/{id}/tree") {
      return Promise.resolve({ nodes: [] });
    }
    if (route === "GET /api/memory/documents") {
      return Promise.resolve({ documents: [] });
    }
    if (route === "GET /api/api-scope-sets") {
      return Promise.resolve({
        sets: [{ name: "tasks-read", description: "Read tasks" }],
      });
    }
    if (route === "GET /api/capability-methods") {
      return Promise.resolve([
        {
          ref: "gchat#post",
          capability: "gchat",
          method: "post",
          description: "Post to Chat",
        },
      ]);
    }
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
    if (route === "POST /api/capability-grants/check") {
      const req = body as { grants: { grant?: string; capability?: string }[] };
      const g = req.grants[0];
      return Promise.resolve([{ recorded: g.grant || `${g.capability}={}` }]);
    }
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
    const b = body as Partial<ConsoleSession> & { capabilities?: string[] };
    const id = (params as { id: string }).id;
    this.sessions = this.sessions.map((s) =>
      s.id === id
        ? {
            ...s,
            ...(b.capabilities ? { capabilities: b.capabilities } : {}),
            ...(b.team !== undefined ? { team: b.team } : {}),
            ...(b.model !== undefined ? { model: b.model } : {}),
            ...(b.networkAccess !== undefined
              ? { networkAccess: b.networkAccess }
              : {}),
          }
        : s,
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
  config = () =>
    Promise.resolve({
      access: null,
      models: ["gemini-fast", "gemini-pro"],
      defaultModel: "gemini-pro",
    });
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

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 15));
  await TestBed.inject(SessionsStore).refresh();
  f.detectChanges();
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

  it("lists the Director first with conversation attachments, zero Classic links, and zero <select>s", async () => {
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
    expect(items[0].getAttribute("aria-current")).toBeNull();
    expect(items[0].textContent).toContain("Asked");
    expect(items[0].textContent).toContain("File a task to bump Go");
    expect(items[1].textContent).toContain("Working");
    expect(q(f, '[data-testid="msg-1"] strong')?.textContent).toBe("here");
    expect(text(f, '[data-testid="msg-1"] .msg-author')).toBe("Grain");
    expect(q(f, '[data-testid="msg-2"]')?.classList).toContain("is-human");
    expect(q(f, '[data-testid="stop"]')).toBeNull();

    // Renders comment attachments inline/downloadable.
    const att = q(f, '[data-testid="attachment-77"]') as HTMLAnchorElement;
    expect(att).toBeTruthy();
    expect(att.getAttribute("href")).toBe("/api/tasks/1/attachments/77");

    // Zero Classic escape links and zero native <select> elements.
    const root = f.nativeElement as HTMLElement;
    expect(root.querySelectorAll("select")).toHaveLength(0);
    expect(root.querySelector('a[href="/director"]')).toBeNull();
    expect(root.querySelector('a[href*="?settings=1"]')).toBeNull();
    expect(root.textContent).not.toContain("Classic");
    expect(root.textContent).not.toContain("manage in classic");
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

  it("uploads composer file attachments on POST /api/tasks/{id}/comments", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const input = q(
      f,
      '[data-testid="session-attach-input"]',
    ) as HTMLInputElement;
    const file = new File(["shot-bytes"], "screen.png", { type: "image/png" });
    Object.defineProperty(input, "files", { value: [file] });
    input.dispatchEvent(new Event("change"));
    await settle(f);

    // Staging reads the file asynchronously (a FileReader), which a fixed
    // settle can outrun on a loaded machine: wait for the chip itself.
    await expect
      .poll(() => {
        f.detectChanges();
        return text(f, '[data-testid="staged-att-0"]');
      })
      .toContain("screen.png");
    const sendBtn = q(f, '[data-testid="send"]') as HTMLButtonElement;
    expect(sendBtn.disabled).toBe(false);
    sendBtn.click();
    await settle(f);

    const sent = api.calls.find(
      (c) => c.route === "POST /api/tasks/{id}/comments",
    );
    expect(sent).toMatchObject({
      params: { id: "1" },
      body: {
        body: "",
        attachments: [
          {
            filename: "screen.png",
            contentType: "image/png",
            content: btoa("shot-bytes"),
          },
        ],
      },
    });
    expect(q(f, '[data-testid="staged-att-0"]')).toBeNull();
  });

  it("shows Apply now banner when editing grants or model on a live running turn and cancels the turn on click", async () => {
    const api = new FakeApi();
    const f = await render(api, "rel");
    expect(q(f, '[data-testid="apply-now-banner"]')).toBeNull();

    // Toggle a capability while session 'rel' has a live running turn.
    (q(f, '[data-testid="cap-self-debug"]') as HTMLButtonElement).click();
    await settle(f);

    expect(q(f, '[data-testid="apply-now-banner"]')).toBeTruthy();
    (q(f, '[data-testid="apply-now-btn"]') as HTMLButtonElement).click();
    await settle(f);

    expect(
      api.calls.find(
        (c) => c.route === "POST /api/console-sessions/{id}/cancel",
      ),
    ).toMatchObject({ params: { id: "rel" } });
    expect(q(f, '[data-testid="apply-now-banner"]')).toBeNull();
  });

  it("saves Reads all on the session, not the deployment's settings", async () => {
    const api = new FakeApi();
    const f = await render(api);
    (
      q(f, '[data-testid="session-settings-toggle"]') as HTMLButtonElement
    ).click();
    await f.whenStable();
    const box = q(f, '[data-testid="session-reads-all"]') as HTMLInputElement;
    box.checked = !box.checked;
    box.dispatchEvent(new Event("change"));
    await settle(f);
    expect(
      api.calls.find(
        (c) =>
          c.method === "PUT" &&
          c.route === "PUT /api/console-sessions/{id}" &&
          (c.body as { readsAll?: boolean })?.readsAll === box.checked,
      ),
    ).toBeTruthy();
    expect(api.calls.some((c) => c.route === "POST /api/settings")).toBe(false);
  });

  it("saves a session's settings on the session while it has no open run", async () => {
    const api = new FakeApi();
    api.sessions = api.sessions.map((s) =>
      s.id === "director" ? { ...s, taskId: undefined } : s,
    );
    const f = await render(api);
    (
      q(f, '[data-testid="session-settings-toggle"]') as HTMLButtonElement
    ).click();
    await f.whenStable();
    const box = q(f, '[data-testid="session-reads-all"]') as HTMLInputElement;
    box.checked = !box.checked;
    box.dispatchEvent(new Event("change"));
    await settle(f);
    expect(
      api.calls.find(
        (c) =>
          c.route === "PUT /api/console-sessions/{id}" &&
          (c.body as { readsAll?: boolean })?.readsAll === box.checked,
      ),
    ).toBeTruthy();
    expect(api.calls.some((c) => c.route === "POST /api/settings")).toBe(false);
  });

  it("edits API-scope grants and model in the session settings drawer, with no team field", async () => {
    const api = new FakeApi();
    const f = await render(api);

    (
      q(f, '[data-testid="session-settings-toggle"]') as HTMLButtonElement
    ).click();
    await f.whenStable();
    const drawer = q(f, '[data-testid="session-settings-panel"]')!;
    expect(drawer).toBeTruthy();
    expect(drawer.querySelectorAll("select")).toHaveLength(0);

    // No team: grain has none, so the drawer offers no team visibility.
    expect(q(f, '[data-testid="session-team-input"]')).toBeNull();
    expect(drawer.textContent).not.toMatch(/team/i);

    // Add API scope grant via <g-grants-picker>
    (
      q(f, '[data-testid="session-grants-add-open"]') as HTMLButtonElement
    ).click();
    await settle(f);
    (
      q(f, '[data-testid="session-grants-attach"]') as HTMLButtonElement
    ).click();
    await settle(f);

    expect(
      api.calls.find((c) => c.route === "POST /api/capability-grants/check"),
    ).toBeTruthy();
    const lastPut = api.calls.filter((c) => c.method === "PUT").at(-1);
    expect(
      (lastPut?.body as { capabilities: string[] }).capabilities,
    ).toContain("api:tasks-read");
  });

  it("marks a message that does not go through, then retries it or takes it back", async () => {
    const api = new FakeApi();
    api.fail["POST /api/tasks/{id}/comments"] = {
      status: 500,
      error: { error: "sandbox unavailable" },
    };
    const f = await render(api);
    const store = TestBed.inject(SessionsStore);
    store.setDraft("director", "hello");
    expect(store.send()).toBe(true);
    await settle(f);
    expect(store.draftFor("director")).toBe("");
    expect(text(f, '[data-testid="pending-0"]')).toContain("hello");
    expect(text(f, '[data-testid="pending-state"]')).toBe("Not sent");
    expect(text(f, '[data-testid="pending-0"]')).toContain(
      "sandbox unavailable",
    );

    // Edit takes it back into the composer, ahead of what is typed there.
    store.setDraft("director", "and more");
    (q(f, '[data-testid="pending-edit"]') as HTMLButtonElement).click();
    await settle(f);
    expect(q(f, '[data-testid="pending-0"]')).toBeNull();
    expect(store.draftFor("director")).toBe("hello\n\nand more");

    // Retry sends it once the server takes it (a failure is answered once).
    api.fail["POST /api/tasks/{id}/comments"] = {
      status: 503,
      error: { error: "still starting" },
    };
    expect(store.send()).toBe(true);
    await settle(f);
    expect(text(f, '[data-testid="pending-0"]')).toContain("still starting");
    (q(f, '[data-testid="pending-retry"]') as HTMLButtonElement).click();
    await settle(f);
    expect(q(f, '[data-testid="pending-0"]')).toBeNull();
    expect(
      api.calls
        .filter((c) => c.route === "POST /api/tasks/{id}/comments")
        .at(-1)?.body,
    ).toMatchObject({ body: "hello\n\nand more" });
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

  it("starts a new session from the dialog, which asks for no team", async () => {
    const api = new FakeApi();
    const f = await render(api);
    (q(f, '[data-testid="new-session"]') as HTMLButtonElement).click();
    await f.whenStable();
    const dialog = q(f, '[data-testid="new-session-dialog"]')!;
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(dialog.querySelectorAll("select")).toHaveLength(0);
    const name = q(f, '[data-testid="new-session-name"]') as HTMLInputElement;
    name.value = "New one";
    name.dispatchEvent(new Event("input"));
    expect(q(f, '[data-testid="new-session-team"]')).toBeNull();
    expect(dialog.textContent).not.toMatch(/team/i);
    (q(f, '[data-testid="new-session-create"]') as HTMLButtonElement).click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/console-sessions"),
    ).toMatchObject({
      body: { title: "New one", attachments: [] },
    });
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
    await settle(f);
    expect(api.reads.filter((r) => r.endsWith(":9")).length).toBe(
      readsBefore + 1,
    );
  });

  it("queues messages sent while one is in flight, sends each once and in order", async () => {
    const api = new FakeApi();
    const releases: (() => void)[] = [];
    const post = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route !== "POST /api/tasks/{id}/comments") {
        return post(route, params, body);
      }
      api.calls.push({ method: "POST", route, params, body });
      return new Promise((r) => releases.push(() => r({})));
    };
    const f = await render(api);
    const store = TestBed.inject(SessionsStore);
    const posts = () =>
      api.calls.filter((c) => c.route === "POST /api/tasks/{id}/comments");
    store.setDraft("director", "once");
    expect(store.send()).toBe(true);
    // A second Enter on the emptied composer sends nothing again.
    expect(store.send()).toBe(false);
    store.setDraft("director", "and again");
    expect(store.send()).toBe(true);
    await settle(f);
    expect(store.draftFor("director")).toBe("");
    expect(text(f, '[data-testid="pending-0"]')).toContain("Sending…");
    expect(text(f, '[data-testid="pending-1"]')).toContain("Queued");
    expect(posts()).toHaveLength(1);
    releases.shift()!();
    await settle(f);
    expect(posts()).toHaveLength(2);
    expect(posts()[1]?.body).toMatchObject({ body: "and again" });
    releases.shift()!();
    await settle(f);
    expect(posts().map((c) => (c.body as { body: string }).body)).toEqual([
      "once",
      "and again",
    ]);
    expect(q(f, '[data-testid="pending-0"]')).toBeNull();
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

  it("hides the composer when the session task repo is outside repoLimits for tasks:write", async () => {
    const api = new FakeApi();
    api.details["1"] = { ...api.details["1"], repo: "acme/other" };
    const f = await render(api);
    const access = TestBed.inject(AccessService);
    access.setConfig({
      access: {
        role: "dev",
        roles: ["dev"],
        full: false,
        scopes: ["tasks:read", "tasks:write"],
        repoLimits: { "tasks:write": ["acme/allowed"] },
      },
    });
    f.detectChanges();
    await f.whenStable();
    expect(q(f, '[data-testid="composer"]')).toBeNull();

    api.details["1"] = { ...api.details["1"], repo: "acme/allowed" };
    await TestBed.inject(SessionsStore).refresh();
    f.detectChanges();
    await f.whenStable();
    expect(q(f, '[data-testid="composer"]')).not.toBeNull();
  });

  it("exposes inline header model picker and toggles the coplanar Workspace Canvas", async () => {
    const api = new FakeApi();
    api.details["1"] = {
      ...api.details["1"],
      comments: [
        ...api.details["1"].comments!,
        {
          id: 99,
          author: "grain",
          authorKind: "automation",
          body: "```html\n<title>Canvas Mock</title><div>Hi</div>\n```",
        },
      ],
    };
    const f = await render(api);
    expect(q(f, '[data-testid="chat-head-model"]')).not.toBeNull();
    expect(text(f, '[data-testid="session-canvas-toggle"]')).toContain(
      "Canvas (1)",
    );

    (
      q(f, '[data-testid="open-artifact-html-99-1"]') as HTMLButtonElement
    ).click();
    await f.whenStable();
    f.detectChanges();
    expect(q(f, '[data-testid="session-canvas"]')).not.toBeNull();
    expect(q(f, '[data-testid="canvas-html-iframe"]')).not.toBeNull();
  });

  it("rolls back created tasks when launchWorkstreamPlan fails mid-plan and keeps SessionsStore retained across drawer watch toggles", async () => {
    let createdSeq = 0;
    const api = new FakeApi();
    const basePost = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route === "POST /api/tasks") {
        api.calls.push({ method: "POST", route, params, body });
        createdSeq += 1;
        if (createdSeq === 3) {
          return Promise.reject(new Error("boom on child #2"));
        }
        return Promise.resolve({
          id: `t-${createdSeq}`,
          title: "created",
          state: "proposed",
        });
      }
      return basePost(route, params, body);
    };

    const f = await render(api);
    const store = TestBed.inject(SessionsStore);

    // Toggling Director watch while SessionsPage is mounted must NOT drop the page retainer.
    store.setDirectorWatch("drawer", true);
    store.setDirectorWatch("drawer", false);
    const readsBefore = api.reads.length;
    await new Promise((r) => setTimeout(r, 2600));
    expect(api.reads.length).toBeGreaterThan(readsBefore);

    const origin = await store.launchWorkstreamPlan({
      title: "Broken plan",
      tasks: [
        { key: "a", title: "First child" },
        { key: "b", title: "Second child", dependsOn: ["a"] },
      ],
    });
    expect(origin).toBeNull();
    expect(store.actionError()).toContain("boom on child #2");
    const closedIds = api.calls
      .filter((c) => c.route === "POST /api/tasks/{id}/close")
      .map((c) => (c.params as { id: string }).id);
    expect(closedIds).toEqual(["t-2", "t-1"]);
    f.destroy();
  });

  it("pages earlier comments on demand and preserves them across live refreshes", async () => {
    const api = new FakeApi();
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 2,
      comments: [
        { id: 10, author: "grain", authorKind: "automation", body: "Newest" },
      ],
    };
    const baseGet = api.get;
    api.get = (route: string, ...rest: unknown[]) => {
      const query = rest[1] as string | undefined;
      if (
        route === "GET /api/tasks/{id}" &&
        query?.includes("comments_before=10")
      ) {
        api.calls.push({ method: "GET", route, params: rest[0], body: query });
        return Promise.resolve({
          id: "1",
          title: "Director",
          olderComments: 0,
          comments: [
            { id: 8, author: "ana", authorKind: "human", body: "First turn" },
            {
              id: 9,
              author: "grain",
              authorKind: "automation",
              body: "Second turn",
            },
          ],
        } as TaskDetail);
      }
      return baseGet(route);
    };

    const f = await render(api);
    const loadBtn = q(
      f,
      '[data-testid="load-older-comments"]',
    ) as HTMLButtonElement;
    expect(loadBtn).toBeTruthy();
    expect(loadBtn.textContent).toContain("Load earlier comments (2)");

    loadBtn.click();
    await settle(f);

    expect(q(f, '[data-testid="load-older-comments"]')).toBeNull();
    expect(q(f, '[data-testid="msg-8"]')?.textContent).toContain("First turn");
    expect(q(f, '[data-testid="msg-9"]')?.textContent).toContain("Second turn");
    expect(q(f, '[data-testid="msg-10"]')?.textContent).toContain("Newest");

    // A later live refresh must keep the paged earlier turns in place.
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 2,
      comments: [
        { id: 10, author: "grain", authorKind: "automation", body: "Newest" },
        {
          id: 11,
          author: "grain",
          authorKind: "automation",
          body: "Follow-up",
        },
      ],
    };
    await TestBed.inject(SessionsStore).refresh();
    f.detectChanges();
    await f.whenStable();
    expect(q(f, '[data-testid="msg-8"]')?.textContent).toContain("First turn");
    expect(q(f, '[data-testid="msg-11"]')?.textContent).toContain("Follow-up");
    expect(q(f, '[data-testid="load-older-comments"]')).toBeNull();

    // The live page slides forward past #10 and #11: neither may vanish.
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 4,
      comments: [
        { id: 12, author: "ana", authorKind: "human", body: "Third turn" },
        { id: 13, author: "grain", authorKind: "automation", body: "Reply" },
      ],
    };
    await TestBed.inject(SessionsStore).refresh();
    f.detectChanges();
    await f.whenStable();
    for (const id of [8, 9, 10, 11, 12, 13]) {
      expect(q(f, `[data-testid="msg-${id}"]`)).toBeTruthy();
    }
  });

  it("shows optimistic sent message immediately and does not stall behind 503 polling", async () => {
    const api = new FakeApi();
    let releasePost!: () => void;
    const basePost = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route !== "POST /api/tasks/{id}/comments") {
        return basePost(route, params, body);
      }
      api.calls.push({ method: "POST", route, params, body });
      return new Promise((r) => (releasePost = () => r({})));
    };

    const f = await render(api);
    const store = TestBed.inject(SessionsStore);

    // Simulate stalled 503 polling routes while sending.
    let releasePoll!: () => void;
    api.getIfChanged = () =>
      new Promise((_, reject) => {
        releasePoll = () =>
          reject({ status: 503, error: { error: "verifier_unavailable" } });
      });

    const box = q(f, '[data-testid="composer"]') as HTMLTextAreaElement;
    box.value = "Ship the patch now";
    box.dispatchEvent(new Event("input"));
    f.detectChanges();

    (q(f, '[data-testid="send"]') as HTMLButtonElement).click();
    f.detectChanges();

    // Appears optimistically before POST settles and draft clears immediately.
    expect(f.nativeElement.textContent).toContain("Ship the patch now");
    expect(f.nativeElement.textContent).toContain("Sending…");
    expect(store.draftFor("director")).toBe("");

    // Once POST settles, busy('send') clears immediately even while GET poll hangs.
    releasePost();
    await Promise.resolve();
    await Promise.resolve();
    expect(store.isBusy("send")).toBe(false);
    expect(f.nativeElement.textContent).toContain("Ship the patch now");

    releasePoll?.();
  });

  it("scrolls new replies into view on a full 50-comment window, including after sending, while leaving a scrolled-up reader in place", async () => {
    const makeWindow = (startId: number, count = COMMENTS_PAGE_SIZE) =>
      Array.from({ length: count }, (_, i) => ({
        id: startId + i,
        author: i % 2 === 0 ? "ana" : "grain",
        authorKind: (i % 2 === 0 ? "human" : "automation") as
          "human" | "automation",
        body: `Turn ${startId + i}`,
      }));

    const api = new FakeApi();
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 100,
      comments: makeWindow(101), // 101..150 (50 comments)
    };

    const f = await render(api);
    const store = TestBed.inject(SessionsStore);
    const logEl = q(f, '[data-testid="chat-log"]') as HTMLElement;
    expect(logEl).toBeTruthy();

    let currentHeight = 2400;
    Object.defineProperty(logEl, "clientHeight", {
      configurable: true,
      get: () => 400,
    });
    Object.defineProperty(logEl, "scrollHeight", {
      configurable: true,
      get: () => currentHeight,
    });

    // Park the reader near the bottom (within the 120px follow threshold).
    logEl.scrollTop = 1950;
    logEl.dispatchEvent(new Event("scroll"));
    expect(logEl.querySelectorAll("article.msg")).toHaveLength(
      COMMENTS_PAGE_SIZE,
    );

    // 1. A polled tall reply (+220px) slides the 50-comment window forward (102..151, still 50).
    currentHeight = 2620;
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 101,
      comments: makeWindow(102),
    };
    await store.refresh();
    await settle(f);
    expect(logEl.querySelectorAll("article.msg")).toHaveLength(
      COMMENTS_PAGE_SIZE,
    );
    expect(q(f, '[data-testid="msg-151"]')?.textContent).toContain("Turn 151");
    expect(logEl.scrollTop).toBe(2620);

    // 2. Sending a message takes the window 50 -> 51 (optimistic), back to 50 on
    //    the POST response (103..152), and 50 again when the agent's reply lands (104..153).
    const basePost = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route === "POST /api/tasks/{id}/comments") {
        api.calls.push({ method: "POST", route, params, body });
        const allReturned = makeWindow(100, COMMENTS_PAGE_SIZE + 3); // 100..152 (53 -> sliced to 103..152)
        api.details["1"] = {
          ...api.details["1"],
          olderComments: 102,
          comments: allReturned.slice(-COMMENTS_PAGE_SIZE),
        };
        currentHeight = 2780;
        return Promise.resolve({
          id: "1",
          olderComments: 100,
          comments: allReturned,
        });
      }
      return basePost(route, params, body);
    };

    const box = q(f, '[data-testid="composer"]') as HTMLTextAreaElement;
    box.value = "Turn 152";
    box.dispatchEvent(new Event("input"));
    f.detectChanges();
    (q(f, '[data-testid="send"]') as HTMLButtonElement).click();
    await settle(f);
    expect(logEl.querySelectorAll("article.msg")).toHaveLength(
      COMMENTS_PAGE_SIZE,
    );
    expect(logEl.scrollTop).toBe(2780);

    // Agent reply arrives on the next poll (104..153, still 50 comments).
    currentHeight = 2960;
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 103,
      comments: makeWindow(104),
    };
    await store.refresh();
    await settle(f);
    expect(logEl.querySelectorAll("article.msg")).toHaveLength(
      COMMENTS_PAGE_SIZE,
    );
    expect(q(f, '[data-testid="msg-153"]')?.textContent).toContain("Turn 153");
    expect(logEl.scrollTop).toBe(2960);

    // 3. When the reader scrolls up, a sliding 50-comment window does not yank scrollTop.
    logEl.scrollTop = 300;
    logEl.dispatchEvent(new Event("scroll"));
    currentHeight = 3120;
    api.details["1"] = {
      ...api.details["1"],
      olderComments: 104,
      comments: makeWindow(105),
    };
    await store.refresh();
    await settle(f);
    expect(q(f, '[data-testid="msg-154"]')?.textContent).toContain("Turn 154");
    expect(logEl.scrollTop).toBe(300);
  });

  it("awaits /api/config before cold-load session reads so a repo-limited role never fires console-sessions or agent-models", async () => {
    const api = new FakeApi();
    let configCalls = 0;
    let resolveCfg!: (cfg: unknown) => void;
    api.config = () => {
      configCalls += 1;
      return new Promise((r) => {
        resolveCfg = r;
      }) as ReturnType<FakeApi["config"]>;
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const access = TestBed.inject(AccessService);
    const store = TestBed.inject(SessionsStore);
    expect(access.configLoaded()).toBe(false);

    store.retain("cold-page");
    await Promise.resolve();
    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/console-sessions" ||
          c.route === "GET /api/agent-models",
      ),
    ).toBe(false);

    resolveCfg({
      access: {
        role: "widgets/viewer",
        roles: ["widgets/viewer"],
        full: false,
        scopes: ["tasks:read", "sessions:read", "settings:read"],
        repoLimits: {
          "tasks:read": ["acme/widgets"],
          "sessions:read": ["acme/widgets"],
          "settings:read": ["acme/widgets"],
        },
      },
      models: ["gemini-fast", "gemini-pro"],
      defaultModel: "gemini-pro",
    });
    await new Promise((r) => setTimeout(r, 25));

    expect(configCalls).toBe(1);
    expect(store.loaded()).toBe(true);
    expect(store.models()).toEqual(["gemini-fast", "gemini-pro"]);
    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/console-sessions" ||
          c.route === "GET /api/agent-models",
      ),
    ).toBe(false);
    store.release("cold-page");
  });

  it("skips GET /api/agent-models when a limited role lacks settings:read", async () => {
    const api = new FakeApi();
    api.config = () =>
      Promise.resolve({
        access: {
          role: "sessions-only",
          roles: ["sessions-only"],
          full: false,
          scopes: ["tasks:read", "sessions:read"],
        },
        models: ["gemini-fast"],
        defaultModel: "gemini-fast",
      }) as ReturnType<FakeApi["config"]>;

    const f = await render(api);
    expect(api.calls.some((c) => c.route === "GET /api/agent-models")).toBe(
      false,
    );
    expect(TestBed.inject(SessionsStore).models()).toEqual(["gemini-fast"]);
    f.destroy();
  });
});
