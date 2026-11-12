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
    Promise.resolve(
      this.details[id] ??
        (this.taskList.find((t) => t.id === id) as TaskDetail | undefined) ??
        ({ id } as TaskDetail),
    );
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
    ).toBe("/task/9?from=inbox");
    (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).click();
    await new Promise((r) => setTimeout(r));
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
    const input = q(f, '[data-testid="task-composer"]') as HTMLTextAreaElement;
    input.value = "Keep both";
    input.dispatchEvent(new Event("input"));
    await f.whenStable();

    // Another row leaves and the list is rebuilt, as an SSE event does.
    api.taskList = api.taskList.filter((t) => t.id !== "9");
    await TestBed.inject(InboxStore).refresh();
    await f.whenStable();
    expect(
      (q(f, '[data-testid="task-composer"]') as HTMLTextAreaElement).value,
    ).toBe("Keep both");
    expect(
      (q(f, '[data-testid="act-send"]') as HTMLButtonElement).disabled,
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
    await new Promise((r) => setTimeout(r));
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
    await new Promise((r) => setTimeout(r));
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

  it("opens the full task with Enter only after pinning a selection", async () => {
    const api = new FakeApi();
    proposals(api);
    const f = await render(api);
    const nav = vi
      .spyOn(TestBed.inject(Router), "navigateByUrl")
      .mockResolvedValue(true);
    const enter = () =>
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "Enter",
          bubbles: true,
          cancelable: true,
        }),
      );
    enter();
    await f.whenStable();
    expect(nav).not.toHaveBeenCalled();

    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "j",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    enter();
    await f.whenStable();
    expect(nav).toHaveBeenCalledWith(
      expect.stringMatching(/^\/task\/\d+\?from=inbox$/),
    );
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

  it("asks before answering anything but a plain approval with the a key, and requires a pinned row", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Fix",
        state: "awaiting_submit",
        pullRequest: "acme/web#3",
        mergeGates: [{ id: "ci", label: "CI", state: "passed" }],
      },
    ];
    const f = await render(api);
    const store = TestBed.inject(InboxStore);
    const primary = store
      .selected()
      ?.actions.find((a) => a.variant === "primary");
    const fire = (key: string, code: string) =>
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", {
          key,
          code,
          bubbles: true,
          cancelable: true,
        }),
      );
    fire("a", "KeyA");
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')).toBeNull();
    expect(api.posts).toHaveLength(0);

    fire("j", "KeyJ");
    await f.whenStable();
    fire("a", "KeyA");
    await f.whenStable();
    if (primary && primary.id !== "approve") {
      expect(api.posts).toHaveLength(0);
      expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
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

  describe("M7 safety", () => {
    afterEach(() => TestBed.inject(InboxStore).stop());

    function rowByRef(f: ComponentFixture<unknown>, ref: string): HTMLElement {
      return [
        ...(f.nativeElement as HTMLElement).querySelectorAll<HTMLElement>(
          ".queue-row",
        ),
      ].find((r) => r.querySelector(".row-ref")?.textContent?.trim() === ref)!;
    }

    async function type(f: ComponentFixture<unknown>, text: string) {
      const input = q(
        f,
        '[data-testid="task-composer"]',
      ) as HTMLTextAreaElement;
      input.value = text;
      input.dispatchEvent(new Event("input"));
      await f.whenStable();
    }

    function comments(api: FakeApi) {
      return api.posts.filter(
        (p) => p.route === "POST /api/tasks/{id}/comments",
      );
    }

    it("keeps the selected row when it leaves mid-compose, and never sends to another task", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "2",
          title: "Old stopped work",
          state: "awaiting_reply",
          questionSummary: "Anything else?",
          questionCommentId: 7,
        },
        {
          id: "36",
          title: "stats: add Mean",
          state: "awaiting_reply",
          questionSummary: "Mean of empty?",
          questionCommentId: 5,
        },
      ];
      const f = await render(api);
      rowByRef(f, "#36").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      await type(f, "Return ErrEmpty");
      expect(q(f, '[data-testid="act-send"]')?.textContent).toContain(
        "Send to #36",
      );

      // #36's run picks up again and it leaves the queue.
      api.taskList = [
        api.taskList[0],
        { ...api.taskList[1], state: "running", questionCommentId: undefined },
      ];
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();
      expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
        "#36",
      );
      expect(q(f, '[data-testid="left-banner"]')?.textContent).toContain(
        "#36 left the Inbox: it is running now",
      );
      expect(
        (q(f, '[data-testid="act-send"]') as HTMLButtonElement).disabled,
      ).toBe(true);
      expect(q(f, '[data-testid="reply-blocked"]')?.textContent).toContain(
        "no longer waiting",
      );
      expect(comments(api)).toEqual([]);

      // Next item is the explicit way off it.
      (q(f, '[data-testid="next-item"]') as HTMLButtonElement).click();
      await f.whenStable();
      expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
        "#2",
      );
      expect(
        (q(f, '[data-testid="task-composer"]') as HTMLTextAreaElement).value,
      ).toBe("");
    });

    it("refuses a reply whose task moved on before the list noticed", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "2",
          title: "Other",
          state: "awaiting_reply",
          questionCommentId: 7,
        },
        {
          id: "36",
          title: "stats",
          state: "awaiting_reply",
          questionCommentId: 5,
        },
      ];
      const f = await render(api);
      rowByRef(f, "#36").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      await type(f, "Go ahead");
      // The server already moved on; no refresh has run yet.
      api.taskList = [api.taskList[0]];
      (q(f, '[data-testid="act-send"]') as HTMLButtonElement).click();
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
      await f.whenStable();
      expect(comments(api)).toEqual([]);
      expect(q(f, '[data-testid="action-error"]')?.textContent).toContain(
        "Not sent: #36",
      );
    });

    it("sends to the pinned task while it still waits on the same question", async () => {
      const api = new FakeApi();
      api.taskList = [
        { id: "2", title: "Other", state: "awaiting_reply" },
        {
          id: "36",
          title: "stats",
          state: "awaiting_reply",
          questionCommentId: 5,
        },
      ];
      const f = await render(api);
      rowByRef(f, "#36").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      await type(f, "Yes");
      (q(f, '[data-testid="act-send"]') as HTMLButtonElement).click();
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
      expect(comments(api)).toEqual([
        {
          route: "POST /api/tasks/{id}/comments",
          params: { id: "36" },
          body: { body: "Yes" },
        },
      ]);
    });

    it("refuses Send when the task asked a new question since typing started", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "36",
          title: "stats",
          state: "awaiting_reply",
          questionCommentId: 5,
        },
      ];
      const f = await render(api);
      await type(f, "Yes");
      api.taskList = [{ ...api.taskList[0], questionCommentId: 9 }];
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();
      expect(
        (q(f, '[data-testid="act-send"]') as HTMLButtonElement).disabled,
      ).toBe(true);
      expect(q(f, '[data-testid="reply-blocked"]')?.textContent).toContain(
        "asked a new question",
      );
    });

    it("holds the order while a row is selected and buffers changes behind N updates", async () => {
      const api = new FakeApi();
      api.taskList = [
        { id: "1", title: "First", state: "proposed" },
        { id: "2", title: "Second", state: "proposed" },
      ];
      const f = await render(api);
      // Nothing selected: the list follows the server.
      api.taskList = [
        ...api.taskList,
        { id: "3", title: "Third", state: "proposed" },
      ];
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();
      expect(rows(f)).toEqual(["First", "Second", "Third"]);
      expect(q(f, '[data-testid="updates-chip"]')).toBeNull();

      rowByRef(f, "#2").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      // A new row arrives first, one leaves, and the rest reorder.
      api.taskList = [
        { id: "4", title: "Fourth", state: "proposed" },
        { id: "3", title: "Third", state: "proposed" },
        { id: "2", title: "Second", state: "proposed" },
      ];
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();
      expect(rows(f)).toEqual(["First", "Second", "Third"]);
      expect(rowByRef(f, "#1").classList).toContain("is-gone");
      expect(q(f, '[data-testid="updates-chip"]')?.textContent).toContain(
        "3 updates",
      );
      expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
        "#2",
      );

      (q(f, '[data-testid="updates-chip"]') as HTMLButtonElement).click();
      await f.whenStable();
      expect(rows(f)).toEqual(["Fourth", "Third", "Second"]);
      expect(q(f, '[data-testid="updates-chip"]')).toBeNull();
      expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
        "#2",
      );
    });

    it("shows repo and owner on rows, names every repo in Approve all, and acts only on the rows shown", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "9",
          title: "Bump Go",
          state: "proposed",
          repo: "acme/web",
          owner: "ana",
        },
        {
          id: "10",
          title: "Bump Node",
          state: "proposed",
          repo: "sidthe/grain",
          owner: "bo",
        },
      ];
      const f = await render(api);
      expect(
        [
          ...(f.nativeElement as HTMLElement).querySelectorAll(
            '[data-testid="row-repo"]',
          ),
        ].map((e) => e.textContent?.trim()),
      ).toEqual(["web", "grain"]);
      expect(rowByRef(f, "#9").textContent).toContain("ana");

      (
        q(f, '[data-testid="group-approve-proposal"]') as HTMLButtonElement
      ).click();
      await f.whenStable();
      const scope = q(f, '[data-testid="confirm-scope"]')?.textContent ?? "";
      expect(scope).toContain("1 in acme/web");
      expect(scope).toContain("1 in sidthe/grain");
      expect(scope).toContain("1 owned by ana");
      expect(scope).toContain("1 owned by bo");
      const accept = q(
        f,
        '[data-testid="confirm-accept"]',
      ) as HTMLButtonElement;
      expect(accept.disabled).toBe(true);

      // The group refills while the dialog is open.
      api.taskList = [
        ...api.taskList,
        { id: "11", title: "Late", state: "proposed", repo: "sidthe/grain" },
      ];
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();

      (q(f, '[data-testid="confirm-multi-repo"]') as HTMLInputElement).click();
      await f.whenStable();
      expect(accept.disabled).toBe(false);
      accept.click();
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
      expect(
        api.posts
          .filter((p) => p.route === "POST /api/tasks/{id}/approve")
          .map((p) => (p.params as { id: string }).id)
          .sort(),
      ).toEqual(["10", "9"]);
    });

    it("needs no second tick when every row is in one repository", async () => {
      const api = new FakeApi();
      api.taskList = [
        { id: "9", title: "A", state: "proposed", repo: "acme/web" },
        { id: "10", title: "B", state: "proposed", repo: "acme/web" },
      ];
      const f = await render(api);
      (
        q(f, '[data-testid="group-approve-proposal"]') as HTMLButtonElement
      ).click();
      await f.whenStable();
      expect(q(f, '[data-testid="confirm-scope"]')?.textContent).toContain(
        "2 in acme/web",
      );
      expect(q(f, '[data-testid="confirm-multi-repo"]')).toBeNull();
      expect(
        (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).disabled,
      ).toBe(false);
    });

    it("turns Submit off on failing or unreported CI, and shows CI on the row", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "33",
          title: "CI drill",
          state: "awaiting_submit",
          pullRequest: "acme/web#3",
          mergeGates: [
            { id: "ci", label: "CI", state: "failing" },
            { id: "approval", label: "Approval", state: "needed" },
          ],
        },
        {
          id: "35",
          title: "Makefile",
          state: "awaiting_submit",
          pullRequest: "acme/web#5",
          mergeGates: [
            { id: "ci", label: "CI", state: "passed" },
            { id: "approval", label: "Approval", state: "needed" },
          ],
        },
        {
          id: "37",
          title: "No gates yet",
          state: "awaiting_submit",
          pullRequest: "acme/web#7",
        },
      ];
      const f = await render(api);
      expect(rowByRef(f, "#33").textContent).toContain("✕ CI");
      expect(rowByRef(f, "#35").textContent).toContain("✓ 1/1");
      expect(rowByRef(f, "#37").textContent).toContain("CI unknown");

      const submitOf = async (ref: string) => {
        rowByRef(f, ref).querySelector<HTMLButtonElement>(".row-main")!.click();
        await f.whenStable();
        return q(f, '[data-testid="action-submit"]') as HTMLButtonElement;
      };
      const red = await submitOf("#33");
      expect(red.disabled).toBe(true);
      expect(red.title).toContain("CI is failing");
      const unknown = await submitOf("#37");
      expect(unknown.disabled).toBe(true);
      expect(unknown.title).toContain("has not reported");
      const green = await submitOf("#35");
      expect(green.disabled).toBe(false);

      // Bulk Submit counts and sends only the green one.
      for (const ref of ["#33", "#35", "#37"]) {
        rowByRef(f, ref).querySelector<HTMLInputElement>(".row-check")!.click();
      }
      await f.whenStable();
      const bulk = q(f, '[data-testid="bulk-submit"]') as HTMLButtonElement;
      expect(bulk.textContent).toContain("(1)");
      bulk.click();
      await f.whenStable();
      (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).click();
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
      expect(
        api.posts
          .filter((p) => p.route === "POST /api/tasks/{id}/submit")
          .map((p) => (p.params as { id: string }).id),
      ).toEqual(["35"]);
    });

    it("puts a stopped turn in Paused, names who stopped it, and offers Resume and Close only", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "2",
          title: "Stopped work",
          state: "awaiting_reply",
          turnStopped: true,
          questionCommentId: 43,
          reason: {
            reason: "awaiting_reply/stopped",
            class: "awaiting a person",
          },
        },
      ];
      api.details["2"] = {
        id: "2",
        title: "Stopped work",
        state: "awaiting_reply",
        comments: [
          {
            id: 43,
            author: "alice",
            authorLabel: "Roman",
            body: "Turn stopped by user",
          },
        ],
      };
      const f = await render(api);
      await TestBed.inject(InboxStore).refresh();
      await f.whenStable();
      expect(q(f, '[data-testid="inbox-group-reply"]')).toBeNull();
      expect(q(f, '[data-testid="inbox-group-paused"]')?.textContent).toContain(
        "Stopped by Roman.",
      );
      expect(
        q(f, '[data-testid="next-inbox-headline"]')?.textContent,
      ).toContain("Nothing waiting on you");
      expect(q(f, '[data-testid="task-composer"]')).toBeNull();
      expect(q(f, '[data-testid="action-resume"]')).toBeTruthy();
      expect(q(f, '[data-testid="action-close"]')).toBeTruthy();
      expect(q(f, '[data-testid="group-approve-paused"]')).toBeNull();

      rowByRef(f, "#2").querySelector<HTMLInputElement>(".row-check")!.click();
      await f.whenStable();
      expect(q(f, '[data-testid="bulk-resume"]')).toBeNull();
      expect(q(f, '[data-testid="bulk-close"]')).toBeNull();

      (q(f, '[data-testid="action-resume"]') as HTMLButtonElement).click();
      await f.whenStable();
      expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
        "Roman stopped it.",
      );
      (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r));
      await f.whenStable();
      expect(api.posts[0]).toMatchObject({
        route: "POST /api/tasks/{id}/retry",
        params: { id: "2" },
      });
    });

    it("resolves list 'waiting for checks' via detail read, blocks Submit, and avoids duplicate CI label", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "33",
          title: "CI drill",
          state: "awaiting_submit",
          pullRequest: "sidthe/grain-scratch-repo#3",
          mergeGates: [
            {
              id: "ci",
              label: "CI",
              state: "running",
              detail: "waiting for checks",
            },
          ],
        },
      ];
      api.details["33"] = {
        ...api.taskList[0],
        mergeGates: [
          {
            id: "ci",
            label: "CI",
            state: "failing",
            detail: "Tests failed",
          },
        ],
      } as TaskDetail;
      const f = await render(api);
      await new Promise((r) => setTimeout(r));
      await f.whenStable();
      expect(q(f, '[data-testid="inspector-ci"]')?.textContent?.trim()).toBe(
        "✕ CI",
      );
      expect(q(f, '[data-testid="row-ci"]')?.textContent?.trim()).toBe("✕ CI");
      const store = TestBed.inject(InboxStore);
      const primary = store
        .selected()!
        .actions.find((a) => a.variant === "primary")!;
      expect(primary.blocked).toContain("CI is failing");
      const btn = q<HTMLButtonElement>(
        f,
        `[data-testid="action-${primary.id}"]`,
      )!;
      expect(btn.disabled).toBe(true);
    });

    it("re-verifies live state on confirm accept and aborts when item moved on", async () => {
      const api = new FakeApi();
      api.taskList = [
        { id: "9", title: "Bump Go", state: "proposed", repo: "acme/web" },
      ];
      const f = await render(api);
      (q(f, '[data-testid="action-decline"]') as HTMLButtonElement).click();
      await f.whenStable();
      expect(document.activeElement).toBe(
        q(f, '[data-testid="confirm-cancel"]'),
      );
      const dlg = q(f, '[data-testid="confirm-dialog"]')!;
      dlg.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "Tab",
          shiftKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
      expect(dlg.contains(document.activeElement)).toBe(true);

      // Item leaves before Confirm is clicked.
      api.taskList = [];
      (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r));
      await f.whenStable();
      expect(api.posts).toEqual([]);
      expect(q(f, '[data-testid="action-error"]')?.textContent).toContain(
        "moved on",
      );
    });

    it("refuses reply when direct detail read fails, focuses next-item on resolution, and clears held state on revisit", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "36",
          title: "First question",
          state: "awaiting_reply",
          questionCommentId: 5,
        },
        { id: "9", title: "Second proposal", state: "proposed" },
      ];
      const f = await render(api);
      rowByRef(f, "#36").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      await type(f, "Go ahead");

      api.task = () =>
        new Promise((_r, rej) => setTimeout(() => rej(new Error("offline"))));
      (q(f, '[data-testid="act-send"]') as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r, 15));
      await f.whenStable();
      expect(comments(api)).toEqual([]);
      expect(q(f, '[data-testid="action-error"]')?.textContent).toContain(
        "Could not verify #36",
      );
      expect(
        (q(f, '[data-testid="task-composer"]') as HTMLTextAreaElement).value,
      ).toBe("Go ahead");

      // Held updates show "(N not shown)" in headline; ngOnInit clears held & checked.
      const store = TestBed.inject(InboxStore);
      store.toggleChecked("task:9");
      api.taskList.push({ id: "10", title: "Third", state: "proposed" });
      await store.refresh();
      await f.whenStable();
      expect(
        q(f, '[data-testid="next-inbox-headline"]')?.textContent,
      ).toContain("(1 not shown)");

      f.componentInstance.ngOnInit();
      await f.whenStable();
      expect(store.pendingUpdates()).toBe(0);
      expect(store.checked().size).toBe(0);

      // Acting on pinned #9 moves focus to next-item when it leaves.
      rowByRef(f, "#9").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      api.onPost = (route, params) => {
        if (route === "POST /api/tasks/{id}/approve") {
          api.taskList = api.taskList.filter((t) => t.id !== params["id"]);
        }
      };
      (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r));
      await f.whenStable();
      await new Promise((r) => setTimeout(r, 10));
      expect(document.activeElement).toBe(q(f, '[data-testid="next-item"]'));
    });

    it("requires confirm tick for single non-viewer owner and prunes stale details on refresh", async () => {
      const api = new FakeApi();
      api.cfg = { access: null, version: "v1.2.3", viewerOwner: "alice" };
      api.taskList = [
        {
          id: "9",
          title: "One",
          state: "proposed",
          repo: "acme/web",
          owner: "bob",
        },
        {
          id: "10",
          title: "Two",
          state: "proposed",
          repo: "acme/web",
          owner: "bob",
        },
      ];
      api.details["9"] = { ...api.taskList[0], comments: [] } as TaskDetail;
      const f = await render(api);
      const store = TestBed.inject(InboxStore);
      store.details.update((d) => ({
        ...d,
        "999": { id: "999", title: "Old", state: "closed" } as TaskDetail,
      }));
      await store.refresh();
      expect(store.details()["999"]).toBeUndefined();

      (
        q(f, '[data-testid="group-approve-proposal"]') as HTMLButtonElement
      ).click();
      await f.whenStable();
      expect(
        (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).disabled,
      ).toBe(true);
      expect(q(f, '[data-testid="confirm-scope"]')?.textContent).toContain(
        "2 owned by bob",
      );
    });

    it("filters queue by ?ws= workstream and shows Part of links on rows and inspector", async () => {
      const api = new FakeApi();
      api.taskList = [
        { id: "1", title: "Voice goal", state: "completed" },
        {
          id: "9",
          title: "Mic UI",
          state: "proposed",
          origin: "task:1",
          generatedFrom: "1",
        },
        { id: "10", title: "Unrelated item", state: "proposed" },
      ];
      const f = await render(api);
      expect(rows(f)).toEqual(["Mic UI", "Unrelated item"]);
      expect(q(f, '[data-testid="inbox-part-of"]')?.textContent).toContain(
        "Part of Voice goal",
      );
      expect(
        q(f, '[data-testid="inbox-inspector-part-of"]')?.textContent,
      ).toContain("Part of Voice goal");

      const nav = vi.spyOn(TestBed.inject(Router), "navigate");
      f.componentRef.setInput("ws", "task:1");
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
      f.detectChanges();

      const store = TestBed.inject(InboxStore);
      expect(rows(f)).toEqual(["Mic UI"]);
      expect(
        q(f, '[data-testid="next-inbox-headline"]')?.textContent,
      ).toContain("1 decision waiting on you");
      expect(store.counts().all).toBe(1);
      expect(store.counts().proposals).toBe(1);
      expect(store.selected()?.title).toBe("Mic UI");
      expect(q(f, '[data-testid="inbox-ws-filter"]')?.textContent).toContain(
        "Voice goal",
      );
      expect(q(f, '[data-testid="inbox-ws-clear"]')).not.toBeNull();

      // Press * and verify unrelated task #10 outside ?ws= is NOT checked or acted on.
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "*", bubbles: true }),
      );
      f.detectChanges();
      expect([...store.checked()]).toEqual(["task:9:proposal"]);
      expect(store.decisions().length).toBe(2);

      q<HTMLButtonElement>(f, '[data-testid="inbox-ws-clear"]')!.click();
      expect(nav).toHaveBeenCalledWith([], {
        queryParams: { ws: null },
        queryParamsHandling: "merge",
      });
    });

    it("drives 5-state reactive Aurora on resolution-dock and renders CVD icons + actor chips", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "44",
          title: "Clarify auth TTL",
          state: "awaiting_reply",
          owner: "ana",
          authorKind: "human",
          questionCommentId: 1,
        },
      ];
      api.details["44"] = {
        id: "44",
        title: "Clarify auth TTL",
        state: "awaiting_reply",
        comments: [{ id: 1, body: "Use 15m or 1h?" }],
      };
      const f = await render(api);
      const dock = q(f, '[data-testid="resolution-dock"]')!;
      expect(dock.getAttribute("data-aurora-state")).toBe("anticipating");
      expect(q(f, ".status-pill svg")).not.toBeNull();
      expect(q(f, '[data-testid="actor-chip"]')).not.toBeNull();

      const ta = q<HTMLTextAreaElement>(f, '[data-testid="task-composer"]')!;
      ta.value = "Use 15m with refresh token rotation.";
      ta.dispatchEvent(new Event("input"));
      await f.whenStable();
      expect(dock.getAttribute("data-aurora-state")).toBe("receiving");
      expect(
        Number(dock.style.getPropertyValue("--aurora-intensity")),
      ).toBeGreaterThan(0.35);

      q<HTMLButtonElement>(f, '[data-testid="act-send"]')!.click();
      await f.whenStable();
      // Once answered, pinned item #44 has left the Inbox so dock settles to idle.
      await new Promise((r) => setTimeout(r, 15));
      await f.whenStable();
      expect(["responding", "idle"]).toContain(
        dock.getAttribute("data-aurora-state"),
      );
    });

    it("renders Conversation thread, syntax-highlighted code, intra-line word diffs, Comment only, and Send & wake agent on merge-waiting items", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "42",
          title: "Add token refresh endpoint",
          state: "awaiting_submit",
          repo: "sidthe/grain",
          pullRequest: { url: "https://github.com/sidthe/grain/pull/42" },
        },
        {
          id: "46",
          title: "Migrate session cookie",
          state: "proposed",
          repo: "sidthe/grain",
        },
      ];
      api.details["42"] = {
        id: "42",
        title: "Add token refresh endpoint",
        state: "awaiting_submit",
        repo: "sidthe/grain",
        pullRequest: { url: "https://github.com/sidthe/grain/pull/42" },
        comments: [
          {
            id: 1,
            author: "agent",
            authorKind: "agent",
            authorLabel: "Grain Agent",
            body: "Implemented token refresh:\n```go\nfunc Refresh(tok string) error {\n  return nil\n}\n```",
            createdAt: "2026-11-11T10:00:00Z",
          },
        ],
      };
      api.diffs["42"] = {
        repo: "sidthe/grain",
        base: "main",
        head: "grain/task-42",
        totalFiles: 1,
        totalHunks: 1,
        files: [
          {
            path: "pkg/auth/refresh.go",
            status: "modified",
            additions: 1,
            deletions: 1,
            hunks: [
              {
                header: "@@ -10,2 +10,2 @@",
                lines: [
                  { kind: "del", text: "ttl := 5 * time.Minute" },
                  { kind: "add", text: "ttl := 15 * time.Minute" },
                ],
              },
            ],
          },
        ],
      };

      const f = await render(api);
      await new Promise((r) => setTimeout(r, 10));
      await f.whenStable();

      const conv = q(f, '[data-testid="inspector-conversation"]')!;
      expect(conv).toBeTruthy();
      expect(conv.querySelector(".code-card")).not.toBeNull();
      expect(conv.querySelector(".code-copy-btn")?.textContent).toBe("Copy");
      expect(conv.querySelector(".tok-kw")).not.toBeNull();

      // Dual gutter line numbers + intra-line <mark class="diff-word">
      const diffEl = q(f, '[data-testid="inspector-diff"]')!;
      expect(diffEl.querySelectorAll(".diff-ln").length).toBeGreaterThanOrEqual(
        4,
      );
      const wordMarks = [...diffEl.querySelectorAll("mark.diff-word")].map(
        (m) => m.textContent,
      );
      expect(wordMarks).toContain("5");
      expect(wordMarks).toContain("15");
      const diffTexts = [...diffEl.querySelectorAll(".diff-text")].map(
        (el) => el.textContent,
      );
      expect(diffTexts).toEqual([
        "ttl := 5 * time.Minute",
        "ttl := 15 * time.Minute",
      ]);

      // Shortcut `e` focuses inbox-talk-input on non-reply items
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "e", bubbles: true }),
      );
      await new Promise((r) => setTimeout(r, 5));
      const talkBox = q<HTMLTextAreaElement>(
        f,
        '[data-testid="task-composer"]',
      )!;

      // Comment only posts comment without waking run-now
      talkBox.value = "Looks close, checking CI logs first";
      talkBox.dispatchEvent(new Event("input"));
      await f.whenStable();
      q<HTMLButtonElement>(f, '[data-testid="act-send-secondary"]')!.click();
      await new Promise((r) => setTimeout(r, 10));
      await f.whenStable();
      expect(api.posts).toEqual([
        {
          route: "POST /api/tasks/{id}/comments",
          params: { id: "42" },
          body: { body: "Looks close, checking CI logs first" },
        },
      ]);

      // Send & wake agent posts comment AND triggers POST /api/tasks/{id}/run-now
      api.posts = [];
      talkBox.value = "Please add a unit test for expired tokens";
      talkBox.dispatchEvent(new Event("input"));
      await f.whenStable();
      q<HTMLButtonElement>(f, '[data-testid="act-send"]')!.click();
      await new Promise((r) => setTimeout(r, 10));
      await f.whenStable();
      expect(api.posts).toEqual([
        {
          route: "POST /api/tasks/{id}/comments",
          params: { id: "42" },
          body: { body: "Please add a unit test for expired tokens" },
        },
        {
          route: "POST /api/tasks/{id}/run-now",
          params: { id: "42" },
          body: undefined,
        },
      ]);
      expect(TestBed.inject(SnackbarService).current()?.message).toContain(
        "Sent to #42 — agent waking on the pull request",
      );

      // Partial wake failure: clears talkDraft to prevent duplicate comments and surfaces exact actionError
      api.posts = [];
      const origPost = api.post;
      api.post = (route, params, body) => {
        api.posts.push({ route, params, body });
        if (route === "POST /api/tasks/{id}/run-now") {
          return Promise.reject(new Error("sandbox busy"));
        }
        return Promise.resolve({});
      };
      talkBox.value = "Follow-up note before wake";
      talkBox.dispatchEvent(new Event("input"));
      await f.whenStable();
      q<HTMLButtonElement>(f, '[data-testid="act-send"]')!.click();
      await new Promise((r) => setTimeout(r, 15));
      await f.whenStable();
      expect(talkBox.value).toBe("");
      expect(q(f, '[data-testid="action-error"]')?.textContent).toContain(
        "Comment posted to #42, saved in conversation, but could not wake the agent: sandbox busy",
      );
      api.post = origPost;

      // Proposal (#46): Comment only stays enabled, Send & wake agent is disabled (no run-now bypass)
      rowByRef(f, "#46").querySelector<HTMLButtonElement>(".row-main")!.click();
      await f.whenStable();
      const propWake = q<HTMLButtonElement>(f, '[data-testid="act-send"]')!;
      expect(propWake.disabled).toBe(true);
      expect(propWake.title).toContain("Approve the proposal first");
      expect(q(f, ".composer-hint")?.textContent).not.toContain(
        "Ctrl+Enter wakes agent",
      );
      const propInput = q<HTMLTextAreaElement>(
        f,
        '[data-testid="task-composer"]',
      )!;
      propInput.value = "Scope to HTTP/2 only";
      propInput.dispatchEvent(new Event("input"));
      await f.whenStable();
      expect(
        q<HTMLButtonElement>(f, '[data-testid="act-send-secondary"]')!.disabled,
      ).toBe(false);
      expect(propWake.disabled).toBe(true);
    });

    it("hides Comment only on awaiting_reply holds (access/secret/stopped) and skips redundant run-now when comment resumes", async () => {
      const api = new FakeApi();
      api.taskList = [
        {
          id: "81",
          title: "Need deploy grant",
          state: "awaiting_reply",
          awaitingAccess: true,
          repo: "sidthe/grain",
        },
      ];
      api.details["81"] = {
        id: "81",
        title: "Need deploy grant",
        state: "awaiting_reply",
        repo: "sidthe/grain",
        pendingAccess: {
          summary: "Requesting write access",
          items: [{ kind: "write", repo: "sidthe/grain" }],
        },
        comments: [],
      };
      api.post = (route, params, body) => {
        api.posts.push({ route, params, body });
        if (route === "POST /api/tasks/{id}/comments") {
          return Promise.resolve({ id: "81", state: "queued" });
        }
        return Promise.resolve({});
      };

      const f = await render(api);
      await new Promise((r) => setTimeout(r, 10));
      await f.whenStable();

      expect(q(f, '[data-testid="act-send-secondary"]')).toBeNull();
      const resumeBtn = q<HTMLButtonElement>(f, '[data-testid="act-send"]')!;
      expect(resumeBtn.textContent?.trim()).toBe("Reply & resume agent");

      const talkBox = q<HTMLTextAreaElement>(
        f,
        '[data-testid="task-composer"]',
      )!;
      talkBox.value = "Use read-only mode instead";
      talkBox.dispatchEvent(new Event("input"));
      await f.whenStable();
      resumeBtn.click();
      await new Promise((r) => setTimeout(r, 15));
      await f.whenStable();

      expect(api.posts).toEqual([
        {
          route: "POST /api/tasks/{id}/comments",
          params: { id: "81" },
          body: { body: "Use read-only mode instead" },
        },
      ]);
    });

    it("shows live transcript on host-script entries with running task detail and clears polling on destroy", async () => {
      const api = new FakeApi();
      api.localScriptApprovals = () =>
        Promise.resolve([
          {
            id: "lsa-1",
            taskId: "91",
            script: "deploy-check",
            args: ["--dry-run"],
          },
        ]);
      api.details["91"] = {
        id: "91",
        title: "Run deploy check",
        state: "running",
        repo: "sidthe/grain",
        attempts: [{ number: 2, startedAt: "2026-11-11T10:00:00Z" }],
        comments: [],
      };
      (api as { get: (route: string) => Promise<unknown> }).get = (
        route: string,
      ) => {
        if (route === "GET /api/tasks/{id}/attempts/{number}/transcript") {
          return Promise.resolve({ transcript: "step 1: checking cluster..." });
        }
        return Promise.resolve({});
      };

      const f = await render(api);
      await new Promise((r) => setTimeout(r, 20));
      await f.whenStable();

      expect(q(f, '[data-testid="inbox-live-transcript"]')?.textContent).toBe(
        "step 1: checking cluster...",
      );

      const store = TestBed.inject(InboxStore);
      expect(store.selectedTranscript()).toBe("step 1: checking cluster...");
      f.destroy();
      expect(store.selectedTranscript()).toBeNull();
    });
  });
});
