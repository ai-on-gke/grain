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
import { SnackbarService } from "../services/snackbar.service";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type {
  DiffPreview,
  GrainConfig,
  Notification,
  Task,
  TaskDetail,
} from "../api/models";
import { InboxPageComponent, ago, questionOf } from "./inbox-page.component";
import { InboxStore } from "./inbox.store";

class FakeApi {
  cfg: GrainConfig = { access: null, version: "v1.2.3" };
  taskList: Task[] = [];
  notes: Notification[] = [];
  details: Record<string, TaskDetail> = {};
  posts: { route: string; params: unknown; body: unknown }[] = [];
  onPost: (route: string, params: Record<string, unknown>) => void = () => {};

  config = () => Promise.resolve(this.cfg);
  tasks = () => Promise.resolve(this.taskList);
  task = (id: string) =>
    Promise.resolve(this.details[id] ?? ({ id } as TaskDetail));
  openNotifications = () => Promise.resolve(this.notes);
  localScriptApprovals = () => Promise.resolve([]);
  diffs: Record<string, DiffPreview> = {};
  diffReads: string[] = [];
  diffPreview = (id: string) => {
    this.diffReads.push(id);
    const d = this.diffs[id];
    return d ? Promise.resolve(d) : Promise.reject(new Error("404"));
  };
  get = () => Promise.resolve({ publicKey: "age1example" });
  post = (route: string, params: Record<string, unknown>, body: unknown) => {
    this.posts.push({ route, params, body });
    this.onPost(route, params);
    return Promise.resolve({});
  };
}

async function render(
  api: FakeApi,
): Promise<ComponentFixture<InboxPageComponent>> {
  localStorage.clear();
  TestBed.configureTestingModule({
    imports: [InboxPageComponent],
    providers: [
      {
        provide: GrainApi,
        useValue: api,
      },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
      provideRouter([{ path: "**", children: [] }]),
    ],
  });
  const fixture = TestBed.createComponent(InboxPageComponent);
  fixture.autoDetectChanges();
  await TestBed.inject(InboxStore).refresh();
  await fixture.whenStable();
  return fixture;
}

function q(f: ComponentFixture<unknown>, sel: string): HTMLElement | null {
  return (f.nativeElement as HTMLElement).querySelector(sel);
}

function rows(f: ComponentFixture<unknown>): string[] {
  return [
    ...(f.nativeElement as HTMLElement).querySelectorAll(
      ".queue-row .row-title",
    ),
  ].map((e) => e.textContent?.trim() ?? "");
}

describe("InboxPageComponent", () => {
  afterEach(() => TestBed.inject(InboxStore).stop());

  it("lists waiting work by group, approves a proposal, and drops it once the server does", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Investigate the merge queue stall",
        state: "awaiting_reply",
        questionSummary: "Fix both?",
      },
      { id: "9", title: "Bump Go", state: "proposed" },
      { id: "5", title: "Running work", state: "running" },
    ];
    api.onPost = (route, params) => {
      if (route === "POST /api/tasks/{id}/approve") {
        api.taskList = api.taskList.filter((t) => t.id !== params["id"]);
      }
    };
    const f = await render(api);
    expect(q(f, '[data-testid="next-inbox-headline"]')?.textContent).toContain(
      "2 decisions waiting on you",
    );
    expect(rows(f)).toEqual(["Investigate the merge queue stall", "Bump Go"]);
    expect(q(f, '[data-testid="inbox-group-reply"]')).toBeTruthy();

    const bump = [
      ...(f.nativeElement as HTMLElement).querySelectorAll<HTMLElement>(
        ".queue-row",
      ),
    ].find((r) => r.textContent?.includes("Bump Go"))!;
    bump.querySelector<HTMLButtonElement>(".row-main")!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
      "#9",
    );
    expect(
      q(f, '[data-testid="inspector-open-task"]')?.getAttribute("href"),
    ).toBe("/task/9");
    (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).click();
    await f.whenStable();
    await TestBed.inject(InboxStore).refresh();
    await f.whenStable();
    expect(api.posts[0]).toMatchObject({
      route: "POST /api/tasks/{id}/approve",
      params: { id: "9" },
    });
    expect(rows(f)).toEqual(["Investigate the merge queue stall"]);
  });

  it("shows the change's diff beside a submit decision, tinted by line kind", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "12",
        title: "Fix the parser",
        state: "awaiting_submit",
        repo: "acme/widgets",
        pullRequest: { url: "https://example.com/acme/widgets/pull/3" },
      },
      { id: "9", title: "Bump Go", state: "proposed" },
    ];
    api.diffs["12"] = {
      repo: "acme/widgets",
      base: "main",
      head: "grain/task-12",
      totalFiles: 3,
      totalHunks: 9,
      truncated: true,
      files: [
        {
          path: "pkg/parse.go",
          status: "modified",
          additions: 1,
          deletions: 1,
          hunks: [
            {
              header: "@@ -1,2 +1,2 @@",
              lines: [
                { kind: "ctx", text: "package parse" },
                { kind: "del", text: "n := 0" },
                { kind: "add", text: "n := 1" },
              ],
            },
          ],
        },
      ],
    };
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    store.select("task:12:submit");
    await f.whenStable();
    await new Promise((r) => setTimeout(r));
    await f.whenStable();
    expect(api.diffReads).toContain("12");
    const diff = q(f, '[data-testid="inspector-diff"]');
    expect(diff?.textContent).toContain("3 files changed");
    expect(diff?.textContent).toContain("pkg/parse.go");
    const lines = [
      ...diff!.querySelectorAll<HTMLElement>('[data-testid="diff-line"]'),
    ];
    expect(lines.map((l) => l.className)).toEqual([
      "diff-line diff-line-ctx",
      "diff-line diff-line-del",
      "diff-line diff-line-add",
    ]);
    expect(q(f, '[data-testid="diff-truncated"]')?.textContent).toContain(
      "first 1 of 9",
    );

    // A proposal is not about a change yet: no diff is read for it.
    store.select("task:9:proposal");
    await f.whenStable();
    expect(api.diffReads).not.toContain("9");
    expect(q(f, '[data-testid="inspector-diff"]')).toBeNull();
  });

  it("keeps a half-typed reply when a refresh brings the same row back", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Parked",
        state: "awaiting_reply",
        questionSummary: "Fix both?",
      },
      { id: "9", title: "Bump Go", state: "proposed" },
    ];
    const f = await render(api);
    expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
      "#4",
    );
    const input = q(f, '[data-testid="reply-input"]') as HTMLTextAreaElement;
    input.value = "Keep both";
    input.dispatchEvent(new Event("input"));
    await f.whenStable();

    // Another row leaves and the list is rebuilt, as an SSE event does.
    api.taskList = api.taskList.filter((t) => t.id !== "9");
    await TestBed.inject(InboxStore).refresh();
    await f.whenStable();
    expect(
      (q(f, '[data-testid="reply-input"]') as HTMLTextAreaElement).value,
    ).toBe("Keep both");
    expect(
      (q(f, '[data-testid="send-reply"]') as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  it("asks before declining, and sends nothing when cancelled", async () => {
    const api = new FakeApi();
    api.taskList = [{ id: "9", title: "Bump Go", state: "proposed" }];
    const f = await render(api);
    (q(f, '[data-testid="action-decline"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
      "Decline #9",
    );
    (q(f, '[data-testid="confirm-cancel"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')).toBeNull();
    expect(api.posts).toEqual([]);
  });

  it("approves a breaking release with allow_breaking after a confirmation", async () => {
    const api = new FakeApi();
    api.notes = [
      {
        id: 7,
        subject: "release.pending:v2.4.0",
        actions: ["approve-release"],
        repo: "acme/widgets",
        body: "v2.4.0 is staged.\nBREAKING CHANGES: drops v1",
      },
    ];
    const f = await render(api);
    expect(q(f, '[data-testid="approve-release"]')?.textContent).toContain(
      "Approve breaking",
    );
    (q(f, '[data-testid="approve-release"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
      "BREAKING",
    );
    (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(api.posts[0]).toEqual({
      route:
        "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
      params: { owner: "acme", name: "widgets", version: "v2.4.0" },
      body: { allow_breaking: true },
    });
  });

  it("grants the ticked items of an access request and requires a reason to deny", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "3",
        title: "Needs repo",
        state: "awaiting_reply",
        awaitingAccess: true,
      },
    ];
    api.details["3"] = {
      id: "3",
      title: "Needs repo",
      state: "awaiting_reply",
      pendingAccess: {
        summary: "Read acme/lib",
        reason: "to vendor it",
        createdAt: "",
        items: [
          { kind: "read", repo: "acme/lib" },
          { kind: "network", access: "open" },
        ],
      },
    };
    const f = await render(api);
    const boxes = (
      f.nativeElement as HTMLElement
    ).querySelectorAll<HTMLInputElement>(".access-items input");
    expect(boxes).toHaveLength(2);
    boxes[1].click();
    await f.whenStable();
    expect(q(f, '[data-testid="grant-access"]')?.textContent).toContain(
      "Grant 1 of 2",
    );
    (q(f, '[data-testid="grant-access"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(api.posts[0]).toMatchObject({
      route: "POST /api/tasks/{id}/access-request/grant",
      body: { items: [0] },
    });

    (q(f, '[data-testid="deny-access"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(
      (q(f, '[data-testid="confirm-deny"]') as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("disables what a viewer cannot do and says why", async () => {
    const api = new FakeApi();
    api.cfg = {
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    };
    api.taskList = [{ id: "9", title: "Bump Go", state: "proposed" }];
    const f = await render(api);
    expect(
      (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(q(f, '[data-testid="rbac-denied"]')?.textContent).toContain(
      "grain/viewer",
    );
  });

  it("shows an empty state when nothing waits", async () => {
    const f = await render(new FakeApi());
    expect(q(f, '[data-testid="next-inbox-empty"]')).toBeTruthy();
    expect(q(f, '[data-testid="next-inbox-headline"]')?.textContent).toContain(
      "Nothing waiting on you",
    );
  });

  function proposals(api: FakeApi): void {
    api.taskList = [
      { id: "9", title: "Bump Go", state: "proposed" },
      { id: "10", title: "Pin Node", state: "proposed" },
      {
        id: "4",
        title: "Fix the stall",
        state: "awaiting_reply",
        questionSummary: "Fix both?",
      },
    ];
  }

  function widen(f: ComponentFixture<unknown>, px: number): void {
    const split = q(f, ".inbox-split")!;
    Object.defineProperty(split, "clientWidth", {
      configurable: true,
      value: px,
    });
    split.getBoundingClientRect = () => ({
      left: 0,
      top: 0,
      right: px,
      bottom: 0,
      width: px,
      height: 0,
      x: 0,
      y: 0,
      toJSON() {},
    });
    window.dispatchEvent(new Event("resize"));
  }

  it("starts at 30 / 70 and moves the divider by keyboard, with a wide range", async () => {
    localStorage.clear();
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    widen(f, 1000);
    await f.whenStable();
    const sep = q(f, '[data-testid="inbox-resizer"]')!;
    expect(sep.getAttribute("role")).toBe("separator");
    expect(sep.getAttribute("aria-valuenow")).toBe("30");
    expect(sep.getAttribute("aria-valuemin")).toBe("20");
    expect(sep.getAttribute("aria-valuemax")).toBe("68");
    sep.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "End",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("68");
    sep.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "ArrowLeft",
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("63");
    // Enter hides the list; the inspector's control brings it back.
    sep.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(q(f, ".inbox-split")!.classList).toContain("is-collapsed");
    (q(f, '[data-testid="split-toggle"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, ".inbox-split")!.classList).not.toContain("is-collapsed");
    // Double-click resets to the default.
    sep.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("30");
    expect(localStorage.getItem("grain.inbox.splitRatio")).toBe("0.3000");
  });

  it("drags the divider anywhere in range, and Escape puts it back", async () => {
    localStorage.clear();
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    widen(f, 1000);
    await f.whenStable();
    const sep = q(f, '[data-testid="inbox-resizer"]')!;
    const ptr = (type: string, x: number) =>
      sep.dispatchEvent(
        Object.assign(
          new MouseEvent(type, { bubbles: true, clientX: x, button: 0 }),
          { pointerId: 1 },
        ),
      );
    ptr("pointerdown", 300);
    ptr("pointermove", 620);
    await f.whenStable();
    expect(q(f, ".inbox-resizer-badge")?.textContent).toContain("62 / 38");
    ptr("pointerup", 620);
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("62");
    ptr("pointerdown", 620);
    ptr("pointermove", 150);
    await f.whenStable();
    // Clamped at the 200px minimum.
    expect(sep.getAttribute("aria-valuenow")).toBe("20");
    // Escape, wherever focus is, puts the divider back.
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("62");
    // The CSS gets the exact share, not a rounded percent.
    ptr("pointerdown", 620);
    ptr("pointermove", 333);
    ptr("pointerup", 333);
    await f.whenStable();
    expect(
      parseFloat(q(f, ".inbox-split")!.style.getPropertyValue("--queue-w")),
    ).toBeCloseTo(33.3, 5);
    // A touch gesture the browser cancels keeps where it got to.
    ptr("pointerdown", 333);
    ptr("pointermove", 500);
    sep.dispatchEvent(
      Object.assign(new Event("pointercancel"), { pointerId: 1 }),
    );
    await f.whenStable();
    expect(sep.getAttribute("aria-valuenow")).toBe("50");
  });

  it("approves a whole group after a confirmation, with Undo that withdraws approval", async () => {
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    const btn = q(
      f,
      '[data-testid="group-approve-approve"], [data-testid^="group-approve-"]',
    ) as HTMLButtonElement;
    expect(btn.textContent).toContain("Approve all 2");
    btn.click();
    await f.whenStable();
    const accept = [
      ...(f.nativeElement as HTMLElement).querySelectorAll("button"),
    ].find((b) => b.textContent?.trim() === "Approve 2")!;
    accept.click();
    await f.whenStable();
    await new Promise((r) => setTimeout(r));
    const approved = api.posts.filter(
      (p) => p.route === "POST /api/tasks/{id}/approve",
    );
    expect(approved.map((p) => (p.params as { id: string }).id).sort()).toEqual(
      ["10", "9"],
    );
    const snacks = TestBed.inject(SnackbarService);
    expect(snacks.current()?.message).toBe("Approved 2 items");
    await snacks.runAction(snacks.current()!.id);
    const withdrawn = api.posts.filter(
      (p) => p.route === "POST /api/tasks/{id}/withdraw-approval",
    );
    expect(withdrawn).toHaveLength(2);
  });

  it("keeps failed rows selected after a batch", async () => {
    const api = new FakeApi();
    proposals(api);
    api.onPost = (route, params) => {
      if (route === "POST /api/tasks/{id}/approve" && params["id"] === "10")
        throw new Error("conflict");
    };
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    store.checked.set(
      new Set(
        store
          .entries()
          .filter((e) => e.taskId !== "4")
          .map((e) => e.key),
      ),
    );
    const r = await store.actAll("approve");
    await f.whenStable();
    expect(r).toEqual({
      count: 1,
      failed: 1,
      undo: { route: "POST /api/tasks/{id}/withdraw-approval", ids: ["9"] },
    });
    expect([...store.checked()]).toEqual([
      store.entries().find((e) => e.taskId === "10")?.key,
    ]);
  });

  it("selects everything in the filter with *, says what is outside it, and opens help with ?", async () => {
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    const press = (key: string, init: KeyboardEventInit = {}) =>
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", {
          key,
          bubbles: true,
          cancelable: true,
          ...init,
        }),
      );
    store.filter.set("proposals");
    await f.whenStable();
    press("*");
    await f.whenStable();
    expect(store.checked().size).toBe(2);
    store.filter.set("asked");
    await f.whenStable();
    expect(q(f, '[data-testid="outside-filter"]')?.textContent).toContain(
      "2 outside this filter",
    );
    press("?");
    await f.whenStable();
    expect(q(f, '[data-testid="inbox-help"]')).toBeTruthy();
    press("Escape");
    await f.whenStable();
    expect(q(f, '[data-testid="inbox-help"]')).toBeNull();
  });

  it("opens the full task with Enter", async () => {
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    const nav = vi
      .spyOn(TestBed.inject(Router), "navigateByUrl")
      .mockResolvedValue(true);
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(nav).toHaveBeenCalledWith(expect.stringMatching(/^\/task\/\d+$/));
  });

  it("says the range is 0 while the list is hidden", async () => {
    localStorage.clear();
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    widen(f, 1000);
    (q(f, '[data-testid="split-toggle"]') as HTMLButtonElement).click();
    await f.whenStable();
    const sep = q(f, '[data-testid="inbox-resizer"]')!;
    expect(sep.getAttribute("aria-valuenow")).toBe("0");
    expect(sep.getAttribute("aria-valuemin")).toBe("0");
    expect(sep.getAttribute("aria-valuetext")).toBe("List hidden");
  });

  it("says plainly when Undo could not take an approval back", async () => {
    const api = new FakeApi();
    proposals(api);
    api.onPost = (route) => {
      if (route === "POST /api/tasks/{id}/withdraw-approval")
        throw new Error("running");
    };
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    await expect(
      store.undoAll("POST /api/tasks/{id}/withdraw-approval", ["9"]),
    ).rejects.toThrow(/Could not undo: #9/);
    await f.whenStable();
    expect(q(f, '[data-testid="action-error"]')?.textContent).toContain(
      "close it to cancel",
    );
  });

  it("asks before answering anything but a plain approval with the a key", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Fix",
        state: "awaiting_submit",
        pullRequest: "acme/web#3",
      },
    ];
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    const primary = store
      .selected()
      ?.actions.find((a) => a.variant === "primary");
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "a",
        code: "KeyA",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    if (primary && primary.id !== "approve") {
      expect(api.posts).toHaveLength(0);
      expect((f.nativeElement as HTMLElement).textContent).toContain(
        primary.label,
      );
    }
  });

  it("leaves another page's snack alone when u is pressed", async () => {
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    const snacks = TestBed.inject(SnackbarService);
    const other = vi.fn();
    snacks.show("Moved #3", { label: "Undo", run: other });
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "u",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(other).not.toHaveBeenCalled();
  });
});

describe("inbox page helpers", () => {
  it("formats a short age", () => {
    const now = Date.parse("2026-01-01T12:00:00Z");
    expect(ago("2026-01-01T11:58:00Z", now)).toBe("2m");
    expect(ago("2026-01-01T09:00:00Z", now)).toBe("3h");
    expect(ago(null, now)).toBe("");
  });

  it("reads the question a task is parked on from its comments", () => {
    const d = {
      id: "1",
      title: "",
      state: "",
      comments: [{ id: 5, body: "Keep v1?" }],
    } as TaskDetail;
    expect(
      questionOf(d, { task: { questionCommentId: 5 }, summary: "s" } as never),
    ).toBe("Keep v1?");
  });
});
