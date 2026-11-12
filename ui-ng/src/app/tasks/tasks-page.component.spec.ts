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
import type { BaseBranchGate, Board, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TasksPageComponent } from "./tasks-page.component";
import { TasksStore } from "./tasks.store";

type Call = { method: string; route: string; params?: unknown; body?: unknown };

const task = (
  id: string,
  state: string,
  priority = 5,
  extra: Partial<Task> = {},
) =>
  ({
    id,
    title: `Task ${id}`,
    state,
    priority,
    repo: "acme/web",
    ...extra,
  }) as Task;

class FakeApi {
  live: Task[] = [
    task("1", "queued", 8),
    task("2", "queued", 5),
    task("3", "queued", 5),
    task("4", "queued", 3),
    task("10", "proposed", 8),
    task("11", "proposed", 3),
    task("20", "running", 5, { activity: "running the tests" }),
    task("30", "deferred", 2),
    task("90", "awaiting_reply", 5, { interactive: true, title: "Director" }),
  ];
  closed: Task[] = [task("50", "closed")];
  gates: BaseBranchGate[] = [];
  boards: Board[] = [];
  calls: Call[] = [];

  // The pane's task detail (GET /api/tasks/{id}) and the list.
  getIfChanged = (route: string, params?: { id?: string }) =>
    Promise.resolve(
      route === "GET /api/tasks/{id}"
        ? {
            body: {
              ...(this.live.find((t) => t.id === params?.id) ??
                this.closed.find((t) => t.id === params?.id) ?? {
                  id: params?.id,
                  title: "Gone",
                  state: "closed",
                }),
              comments: [],
              attempts: [],
            },
            changed: true,
          }
        : { body: structuredClone(this.live), changed: true },
    );
  queries: string[] = [];
  get = (route: string, _p?: unknown, query?: string) => {
    if (route === "GET /api/base-branch-gates") {
      return Promise.resolve({ gates: this.gates });
    }
    if (route === "GET /api/boards") {
      return Promise.resolve(this.boards);
    }
    if (route === "GET /api/tasks/{id}/tree") {
      return Promise.resolve({ origin: "", done: 0, total: 0, nodes: [] });
    }
    this.queries.push(query ?? "");
    return Promise.resolve({ tasks: this.closed, nextCursor: "", total: 1 });
  };
  // The shell's Inbox store, refreshed after actions.
  cfg: Record<string, unknown> = { access: null };
  config = () => Promise.resolve(this.cfg);
  tasks = () => Promise.resolve(this.live);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  private rec(method: string, route: string, params?: unknown, body?: unknown) {
    this.calls.push({ method, route, params, body });
    if (method === "POST" && route === "POST /api/boards") {
      const b: Board = {
        id: `b${this.boards.length + 1}`,
        name: (body as { name: string }).name,
        kind: "list",
        query: (body as { query?: string }).query ?? "",
      };
      this.boards = [...this.boards, b];
      return Promise.resolve(b);
    }
    if (method === "DELETE" && route === "DELETE /api/boards/{id}") {
      const id = (params as { id: string }).id;
      this.boards = this.boards.filter((x) => x.id !== id);
      return Promise.resolve({});
    }
    return Promise.resolve({});
  }
  post = (route: string, params?: unknown, body?: unknown) =>
    this.rec("POST", route, params, body);
  patch = (route: string, params?: unknown, body?: unknown) =>
    this.rec("PATCH", route, params, body);
  delete = (route: string, params?: unknown) =>
    this.rec("DELETE", route, params);
}

async function render(
  api: FakeApi,
  tab?: string,
): Promise<ComponentFixture<TasksPageComponent>> {
  TestBed.configureTestingModule({
    imports: [TasksPageComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const f = TestBed.createComponent(TasksPageComponent);
  if (tab) f.componentRef.setInput("tab", tab);
  f.autoDetectChanges();
  await TestBed.inject(TasksStore).refresh();
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

function all(f: ComponentFixture<unknown>, sel: string): HTMLElement[] {
  return [
    ...(f.nativeElement as HTMLElement).querySelectorAll<HTMLElement>(sel),
  ];
}

describe("TasksPageComponent", () => {
  afterEach(() => {
    TestBed.inject(TasksStore).stop();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("opens on the Backlog in run order with counts, bands and the approval gate", async () => {
    const f = await render(new FakeApi());
    expect(
      q(f, '[data-testid="tab-backlog"]')!.getAttribute("aria-current"),
    ).toBe("page");
    expect(q(f, '[data-testid="tab-backlog"]')!.textContent).toContain("4");
    expect(q(f, '[data-testid="tab-proposed"]')!.textContent).toContain("2");
    // The Director's standing task is not work in a list.
    expect(q(f, '[data-testid="tab-all"]')!.textContent).toContain("8");
    expect(
      all(f, ".band-header").map((h) =>
        h.textContent?.replace(/\s+/g, " ").trim(),
      ),
    ).toEqual(["High 8 1", "Normal 5 2", "Low 3 1"]);
    expect(q(f, '[data-testid="gate-card"]')!.textContent).toContain(
      "Needs approval 2",
    );
  });

  it("approves a proposal and says where it went", async () => {
    const api = new FakeApi();
    const f = await render(api, "proposed");
    expect(q(f, '[data-testid="row-10"]')!.textContent).toContain(
      // GET /api/tasks lists it after every queued task (filed later).
      "If approved: position 5 of 5, High 8",
    );
    q<HTMLButtonElement>(f, '[data-testid="approve-10"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/approve",
      params: { id: "10" },
    });
    expect(TestBed.inject(SnackbarService).current()?.message).toContain(
      "Approved #10",
    );
  });

  it("defers with an Undo that brings the task back", async () => {
    const api = new FakeApi();
    const f = await render(api, "proposed");
    q<HTMLButtonElement>(f, '[data-testid="defer-11"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks/{id}/defer",
      body: { deferredUntil: "" },
    });
    const snacks = TestBed.inject(SnackbarService);
    await snacks.runAction(snacks.current()!.id);
    expect(api.calls[1]).toMatchObject({
      route: "POST /api/tasks/{id}/undefer",
      params: { id: "11" },
    });
  });

  it("changes priority from the chip and undoes it exactly", async () => {
    const api = new FakeApi();
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="prio-4"]')!.click();
    await f.whenStable();
    const urgent = all(f, '[data-testid="prio-menu-4"] button').find((b) =>
      b.textContent?.includes("Urgent 9"),
    )!;
    urgent.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      method: "PATCH",
      params: { id: "4" },
      body: { priority: 9 },
    });
    const snacks = TestBed.inject(SnackbarService);
    expect(snacks.current()?.message).toBe("Priority of #4 set to Urgent 9");
    await snacks.runAction(snacks.current()!.id);
    expect(api.calls[1]).toMatchObject({
      method: "PATCH",
      body: { priority: 3 },
    });
  });

  it("moves with the keyboard: pick up, arrow, drop -- crossing a band changes priority", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const handle = q<HTMLButtonElement>(f, '[data-testid="handle-4"]')!;
    handle.click(); // pick up (also what Space and Enter do on a button)
    await f.whenStable();
    expect(handle.getAttribute("aria-pressed")).toBe("true");
    expect(q(f, '[data-testid="pickup-bar"]')).toBeTruthy();
    // The row is re-rendered where it would land, so ask for the handle
    // again each time, as focus does in the browser.
    for (let i = 0; i < 2; i++) {
      q(f, '[data-testid="handle-4"]')!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowUp", cancelable: true }),
      );
      await f.whenStable();
      await new Promise((r) => setTimeout(r));
    }
    await new Promise((r) => setTimeout(r));
    // The row now shows where it would land, and keeps keyboard focus.
    expect(document.activeElement?.getAttribute("data-testid")).toBe(
      "handle-4",
    );
    expect(
      all(f, ".task-row").map((r) => r.getAttribute("data-testid")),
    ).toEqual(["row-1", "row-4", "row-2", "row-3"]);
    // ...inside the band it would join, at that band's priority.
    expect(q(f, '[data-testid="prio-4"]')!.textContent).toContain("High 8");
    expect(all(f, ".band-header").length).toBe(2);
    q<HTMLButtonElement>(f, '[data-testid="pickup-done"]')!.click();
    await settle(f);
    // #4 (Low 3) now sits between #1 (High 8) and #2 (Normal 5): it joins
    // the band above, so its priority changes before the reorder.
    expect(api.calls).toEqual([
      {
        method: "PATCH",
        route: "PATCH /api/tasks/{id}",
        params: { id: "4" },
        body: { priority: 8 },
      },
      {
        method: "POST",
        route: "POST /api/tasks/reorder",
        params: undefined,
        body: { ids: ["4"], afterId: "1", beforeId: "2" },
      },
    ]);
    const msg = TestBed.inject(SnackbarService).current()?.message;
    expect(msg).toContain("#4 moved to position 2 of 4");
    expect(msg).toContain("Priority changed from 3 to 8");
  });

  it("cancels a pick-up with Escape and sends nothing", async () => {
    const api = new FakeApi();
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="handle-3"]')!.click();
    await f.whenStable();
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(q(f, '[data-testid="pickup-bar"]')).toBeNull();
    expect(api.calls).toHaveLength(0);
  });

  it("moves selected tasks to the top, raising priority where needed", async () => {
    const api = new FakeApi();
    const f = await render(api);
    for (const id of ["3", "4"]) {
      q<HTMLInputElement>(
        f,
        `[data-testid="row-${id}"] input[type="checkbox"]`,
      )!.click();
    }
    await f.whenStable();
    q<HTMLButtonElement>(f, '[data-testid="batch-top"]')!.click();
    await settle(f);
    const patches = api.calls.filter((c) => c.method === "PATCH");
    expect(
      patches.map((c) => [(c.params as { id: string }).id, c.body]),
    ).toEqual([
      ["3", { priority: 8 }],
      ["4", { priority: 8 }],
    ]);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/reorder")?.body,
    ).toEqual({
      ids: ["3", "4"],
      afterId: "",
      beforeId: "1",
    });
  });

  it("shows running work with its activity line, and closed tasks a page at a time", async () => {
    const api = new FakeApi();
    let f = await render(api, "running");
    expect(q(f, '[data-testid="row-20"]')!.textContent).toContain(
      "running the tests",
    );
    TestBed.inject(TasksStore).stop();
    TestBed.resetTestingModule();
    f = await render(api, "closed");
    expect(q(f, '[data-testid="row-50"]')).toBeTruthy();
    expect(q(f, '[data-testid="tab-closed"]')!.textContent).toContain("1");
    // The server pages only when cursor= is present.
    expect(api.queries[0]).toBe("state=closed&limit=50&cursor=");
  });
  it("drops by mouse where the line is drawn, in both directions and at the end", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const page = f.componentInstance as unknown as {
      onDragStart(e: DragEvent, id: string): void;
      onDrop(e: DragEvent, pos: number): void;
    };
    const ev = {
      preventDefault() {},
      dataTransfer: null,
    } as unknown as DragEvent;
    // Down: #2 (index 1) onto the line before #4 (index 3) lands after #3.
    page.onDragStart(ev, "2");
    page.onDrop(ev, 3);
    await settle(f);
    const reorders = () =>
      api.calls.filter((c) => c.route === "POST /api/tasks/reorder");
    expect(reorders().at(-1)?.body).toEqual({
      ids: ["2"],
      afterId: "3",
      beforeId: "4",
    });
    // The end slot: after the last row.
    page.onDragStart(ev, "1");
    page.onDrop(ev, 4);
    await settle(f);
    expect(reorders().at(-1)?.body).toEqual({
      ids: ["1"],
      afterId: "4",
      beforeId: "",
    });
  });

  it("asks before closing running work and offers no Undo for it", async () => {
    const api = new FakeApi();
    const f = await render(api, "running");
    q<HTMLInputElement>(
      f,
      '[data-testid="row-20"] input[type="checkbox"]',
    )!.click();
    await f.whenStable();
    q<HTMLButtonElement>(f, '[data-testid="batch-close"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="close-confirm"]')?.textContent).toContain(
      "cannot be undone",
    );
    expect(api.calls).toHaveLength(0);
    q<HTMLButtonElement>(f, '[data-testid="close-yes"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({ route: "POST /api/tasks/{id}/close" });
    expect(TestBed.inject(SnackbarService).current()?.action).toBeUndefined();
  });

  it("ends a pick-up when the picked task leaves the backlog", async () => {
    const api = new FakeApi();
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="handle-2"]')!.click();
    await f.whenStable();
    api.live = api.live.map((t) =>
      t.id === "2" ? { ...t, state: "running" } : t,
    );
    await TestBed.inject(TasksStore).refresh();
    await settle(f);
    expect(q(f, '[data-testid="pickup-bar"]')).toBeNull();
    expect(q(f, '[data-testid="announcer"]')!.textContent).toContain(
      "left the backlog",
    );
  });

  it("restores a priority when the reorder after it fails", async () => {
    const api = new FakeApi();
    const post = api.post;
    api.post = (route: string, params?: unknown, body?: unknown) => {
      if (route !== "POST /api/tasks/reorder") return post(route, params, body);
      api.calls.push({ method: "POST", route, params, body });
      return new Promise((_, reject) =>
        setTimeout(() =>
          reject({ status: 404, error: { error: "no such task" } }),
        ),
      );
    };
    const f = await render(api);
    const undo = await TestBed.inject(TasksStore).move({
      id: "4",
      afterId: "1",
      beforeId: "2",
      priority: 8,
      from: 3,
      to: 1,
    });
    await settle(f);
    expect(undo).toBeNull();
    const patches = api.calls
      .filter((c) => c.method === "PATCH")
      .map((c) => c.body);
    expect(patches).toEqual([{ priority: 8 }, { priority: 3 }]);
  });

  describe("list | detail (M7)", () => {
    function navs(): {
      commands: unknown[];
      extras: Record<string, unknown> | undefined;
    }[] {
      const spy = TestBed.inject(Router).navigate as unknown as {
        mock: { calls: [unknown[], Record<string, unknown>?][] };
      };
      return spy.mock.calls.map(([commands, extras]) => ({ commands, extras }));
    }

    async function withPane(api: FakeApi, tab: string | undefined, id: string) {
      const f = await render(api, tab);
      vi.spyOn(TestBed.inject(Router), "navigate").mockResolvedValue(true);
      f.componentRef.setInput("task", id);
      await settle(f);
      return f;
    }

    it("auto-selects the first row into the persistent 30/70 pane and updates selection on click", async () => {
      localStorage.removeItem("grain.tasks.splitRatio");
      const f = await render(new FakeApi());
      const nav = vi
        .spyOn(TestBed.inject(Router), "navigate")
        .mockResolvedValue(true);
      // Persistent Inbox-replica split: pane & resizer are always mounted on desktop,
      // defaulting to 30/70 and auto-selecting rows()[0] (#1).
      expect(q(f, '[data-testid="tasks-pane"]')).toBeTruthy();
      expect(
        q(f, '[data-testid="tasks-resizer"]')!.getAttribute("aria-valuenow"),
      ).toBe("30");
      expect(q(f, '[data-testid="row-1"]')!.classList).toContain("is-selected");
      expect(q(f, "g-task-detail #task-title")!.textContent).toContain("#1");
      // Zero native <select> elements anywhere on g-tasks-page (including embedded g-task-detail).
      expect(all(f, "select")).toHaveLength(0);

      const link = q<HTMLAnchorElement>(f, '[data-testid="open-2"]')!;
      expect(link.getAttribute("href")).toBe("/task/2?from=tasks:backlog");
      link.click();
      expect(nav).toHaveBeenCalledWith([], {
        queryParams: { task: "2" },
        queryParamsHandling: "merge",
        replaceUrl: false,
      });

      f.componentRef.setInput("task", "2");
      await settle(f);
      expect(q(f, '[data-testid="tasks-pane"]')).toBeTruthy();
      expect(q(f, '[data-testid="row-2"]')!.classList).toContain("is-selected");
      expect(q(f, "g-task-detail #task-title")!.textContent).toContain("#2");
      // The pane's tabs use ?pane=, leaving the list's ?tab= alone.
      expect(
        q(f, '[data-testid="task-tab-runs"]')!.getAttribute("href"),
      ).toContain("pane=runs");
    });

    it("steps with j and k in the order shown, and opens full screen with Enter or ⤢", async () => {
      const f = await withPane(new FakeApi(), undefined, "2");
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "j", bubbles: true }),
      );
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "k", bubbles: true }),
      );
      expect(navs().slice(0, 2)).toEqual([
        {
          commands: [],
          extras: {
            queryParams: { task: "3" },
            queryParamsHandling: "merge",
            replaceUrl: true,
          },
        },
        {
          commands: [],
          extras: {
            queryParams: { task: "1" },
            queryParamsHandling: "merge",
            replaceUrl: true,
          },
        },
      ]);

      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
      expect(navs()[2]).toEqual({
        commands: ["/task", "2"],
        extras: { queryParams: { from: "tasks:backlog" } },
      });
      q<HTMLButtonElement>(f, '[data-testid="pane-expand"]')!.click();
      expect(navs()[3].commands).toEqual(["/task", "2"]);

      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
      expect(navs()[4]).toEqual({
        commands: [],
        extras: {
          queryParams: { task: null, pane: null },
          queryParamsHandling: "merge",
        },
      });
    });

    it("keeps a selected task that left the tab, says where it went, and offers Next item", async () => {
      const f = await withPane(new FakeApi(), "running", "2");
      expect(q(f, '[data-testid="pane-moved"]')!.textContent).toContain(
        "#2 moved to Backlog",
      );
      expect(q(f, "g-task-detail #task-title")!.textContent).toContain("#2");
      q<HTMLButtonElement>(f, '[data-testid="pane-next"]')!.click();
      expect(navs()[0].extras?.["queryParams"]).toEqual({ task: "20" });
    });

    it("opens the task full screen on a phone", async () => {
      const w = window as { matchMedia?: unknown };
      const had = w.matchMedia;
      w.matchMedia = () => ({ matches: true }) as MediaQueryList;
      try {
        const f = await render(new FakeApi());
        const nav = vi
          .spyOn(TestBed.inject(Router), "navigate")
          .mockResolvedValue(true);
        q<HTMLAnchorElement>(f, '[data-testid="open-2"]')!.click();
        expect(nav).toHaveBeenCalledWith(["/task", "2"], {
          queryParams: { from: "tasks:backlog" },
        });
      } finally {
        w.matchMedia = had;
      }
    });

    it("changes priority from the pane", async () => {
      const api = new FakeApi();
      const f = await withPane(api, undefined, "2");
      q<HTMLButtonElement>(f, '[data-testid="pane-priority"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="pane-priority-menu-8"]')!.click();
      await settle(f);
      expect(api.calls).toContainEqual({
        method: "PATCH",
        route: "PATCH /api/tasks/{id}",
        params: { id: "2" },
        body: { priority: 8 },
      });
    });

    it("shows repository and owner on rows", async () => {
      const api = new FakeApi();
      api.live[1] = task("2", "queued", 5, {
        repo: "sidthe/grain-scratch-repo",
        owner: "alice",
      });
      const f = await render(api);
      const row = q(f, '[data-testid="row-2"]')!;
      expect(row.querySelector(".row-repo")!.textContent).toContain(
        "grain-scratch-repo",
      );
      expect(row.querySelector(".row-repo")!.getAttribute("title")).toBe(
        "sidthe/grain-scratch-repo",
      );
      expect(row.textContent).toContain("alice");
    });
  });

  describe("counts, tabs and Approve all (M7)", () => {
    it("counts only unblocked tasks as ready to run", async () => {
      const api = new FakeApi();
      api.live[1] = task("2", "queued", 5, { blocked: true });
      api.live[2] = task("3", "queued", 5, {
        dependsOnStates: { "9": "awaiting_reply" },
      });
      const f = await render(api);
      expect(q(f, '[data-testid="ready-count"]')!.textContent).toContain("2");
      expect(q(f, '[data-testid="blocked-count"]')!.textContent).toContain(
        "2 waiting on a dependency",
      );
      expect(q(f, '[data-testid="tab-backlog"]')!.textContent).toContain("4");
    });

    it("filters Closed into merged and not merged", async () => {
      const api = new FakeApi();
      api.closed = [
        task("50", "closed", 5, { prMergedAt: "2026-11-11T10:00:00Z" }),
        task("51", "closed", 5, { closedReason: "nothing_needed" }),
        task("52", "closed", 5, { merged: true }),
      ];
      const f = await render(api, "closed");
      await settle(f);
      const ids = () =>
        all(f, ".task-row").map((r) => r.getAttribute("data-testid"));
      expect(ids()).toEqual(["row-50", "row-51", "row-52"]);
      q<HTMLButtonElement>(f, '[data-testid="closed-merged"]')!.click();
      await settle(f);
      expect(ids()).toEqual(["row-50", "row-52"]);
      q<HTMLButtonElement>(f, '[data-testid="closed-unmerged"]')!.click();
      await settle(f);
      expect(ids()).toEqual(["row-51"]);
    });

    it("labels Merging as submit and land, and explains the two kinds", async () => {
      const api = new FakeApi();
      api.live.push(
        task("40", "awaiting_submit", 5, {
          mergeGates: [{ id: "ci", label: "CI", state: "failing" }],
        }),
      );
      const f = await render(api, "merging");
      expect(q(f, '[data-testid="tab-merging"]')!.textContent).toContain(
        "Submit & land",
      );
      expect(q(f, '[data-testid="merging-hint"]')).toBeTruthy();
      expect(q(f, '[data-testid="ci-40"]')!.textContent).toContain("✕ CI");
    });

    it("asks before Approve all, names every repository, and approves only the rows shown", async () => {
      const api = new FakeApi();
      api.live[4] = task("10", "proposed", 8, {
        repo: "acme/web",
        owner: "ana",
      });
      api.live[5] = task("11", "proposed", 3, {
        repo: "sidthe/grain",
        owner: "bo",
      });
      const f = await render(api, "proposed");
      q<HTMLButtonElement>(f, '[data-testid="approve-all"]')!.click();
      await settle(f);
      expect(api.calls).toEqual([]);
      const scope = q(f, '[data-testid="confirm-scope"]')!.textContent!;
      expect(scope).toContain("1 in acme/web");
      expect(scope).toContain("1 in sidthe/grain");
      const accept = q<HTMLButtonElement>(f, '[data-testid="confirm-accept"]')!;
      expect(accept.disabled).toBe(true);

      // A proposal arrives while the dialog is open.
      api.live.push(task("12", "proposed", 5, { repo: "sidthe/grain" }));
      await TestBed.inject(TasksStore).refresh();
      await settle(f);

      q<HTMLInputElement>(f, '[data-testid="confirm-multi-repo"]')!.click();
      await settle(f);
      accept.click();
      await settle(f);
      expect(
        api.calls
          .filter((c) => c.route === "POST /api/tasks/{id}/approve")
          .map((c) => (c.params as { id: string }).id)
          .sort(),
      ).toEqual(["10", "11"]);
    });

    it("cancelling Approve all approves nothing", async () => {
      const api = new FakeApi();
      const f = await render(api, "proposed");
      q<HTMLButtonElement>(f, '[data-testid="approve-all"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="confirm-cancel"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="confirm-dialog"]')).toBeNull();
      expect(api.calls).toEqual([]);
    });

    it("opens the pane on clicking anywhere on a row outside controls, across tabs", async () => {
      const api = new FakeApi();
      const f = await render(api, "backlog");
      const nav = vi.spyOn(TestBed.inject(Router), "navigate");
      q<HTMLElement>(f, '[data-testid="row-1"] .row-repo')!.click();
      await settle(f);
      expect(nav).toHaveBeenCalledWith(
        [],
        expect.objectContaining({ queryParams: { task: "1" } }),
      );
      nav.mockClear();

      q<HTMLInputElement>(
        f,
        '[data-testid="row-2"] input[type="checkbox"]',
      )!.click();
      await settle(f);
      expect(nav).not.toHaveBeenCalled();

      f.componentRef.setInput("tab", "proposed");
      await settle(f);
      q<HTMLElement>(f, '[data-testid="row-10"] .row-sub')!.click();
      await settle(f);
      expect(nav).toHaveBeenCalledWith(
        [],
        expect.objectContaining({ queryParams: { task: "10" } }),
      );
    });

    it("merges detail CI for awaiting_submit placeholder rows and prunes closed tasks", async () => {
      const api = new FakeApi();
      api.live.push(
        task("40", "awaiting_submit", 5, {
          mergeGates: [
            {
              id: "ci",
              label: "CI",
              state: "running",
              detail: "waiting for checks",
            },
          ],
        }),
      );
      const baseGet = api.getIfChanged;
      api.getIfChanged = (route: string, params?: { id?: string }) =>
        route === "GET /api/tasks/{id}" && params?.id === "40"
          ? Promise.resolve({
              body: {
                ...task("40", "awaiting_submit", 5, {
                  mergeGates: [{ id: "ci", label: "CI", state: "failing" }],
                }),
                comments: [],
                attempts: [],
              },
              changed: true,
            })
          : baseGet(route, params);
      const f = await render(api, "merging");
      expect(q(f, '[data-testid="ci-40"]')!.textContent).toContain("✕ CI");

      api.live = api.live.filter((t) => t.id !== "40");
      await TestBed.inject(TasksStore).refresh();
      await settle(f);
      expect(TestBed.inject(TasksStore).details()["40"]).toBeUndefined();
    });

    it("hides the side pane on phone viewports even when ?task= is set, and returns focus on close", async () => {
      const prevMatch = (window as { matchMedia?: unknown }).matchMedia;
      try {
        (window as { matchMedia?: unknown }).matchMedia = () => ({
          matches: true,
          media: "(max-width: 720px)",
          addEventListener: () => {},
          removeEventListener: () => {},
        });
        const api = new FakeApi();
        const f = await render(api, "backlog");
        f.componentRef.setInput("task", "1");
        await settle(f);
        expect(q(f, '[data-testid="tasks-resizer"]')).toBeNull();
        expect(q(f, "g-task-detail")).toBeNull();

        (window as { matchMedia?: unknown }).matchMedia = () => ({
          matches: false,
          media: "(max-width: 720px)",
          addEventListener: () => {},
          removeEventListener: () => {},
        });
        window.dispatchEvent(new Event("resize"));
        await settle(f);
        expect(q(f, '[data-testid="tasks-resizer"]')).toBeTruthy();

        const openLink = q<HTMLElement>(f, '[data-testid="open-1"]')!;
        const focusSpy = vi.spyOn(openLink, "focus");
        q<HTMLButtonElement>(f, '[data-testid="pane-close"]')!.click();
        await new Promise((r) => setTimeout(r, 10));
        expect(focusSpy).toHaveBeenCalled();
      } finally {
        (window as { matchMedia?: unknown }).matchMedia = prevMatch;
      }
    });

    it("counts only current tab selected in inline close, keeps Escape inside .confirm-inline, and requires confirm tick for non-viewer owner", async () => {
      const api = new FakeApi();
      api.cfg = { access: null, viewerOwner: "alice" };
      api.live = [
        task("1", "queued", 8, { repo: "acme/web", owner: "bob" }),
        task("10", "running", 8, { repo: "acme/web", owner: "bob" }),
        task("11", "proposed", 8, { repo: "acme/web", owner: "bob" }),
        task("12", "proposed", 5, { repo: "acme/web", owner: "bob" }),
      ];
      const f = await render(api, "running");
      const cmp = f.componentInstance as unknown as {
        checked: { set(v: ReadonlySet<string>): void };
      };
      cmp.checked.set(new Set(["1", "10"]));
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="batch-close"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="close-confirm"]')!.textContent).toContain(
        "Close 1?",
      );

      // Escape inside .confirm-inline should not close the pane.
      f.componentRef.setInput("task", "10");
      await settle(f);
      const nav = vi.spyOn(TestBed.inject(Router), "navigate");
      const inlineBtn = document.createElement("button");
      inlineBtn.className = "confirm-inline";
      document.body.appendChild(inlineBtn);
      inlineBtn.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
      inlineBtn.remove();
      expect(nav).not.toHaveBeenCalled();

      TestBed.inject(AccessService).setConfig({
        access: null,
        viewerOwner: "alice",
      });
      f.componentRef.setInput("tab", "proposed");
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="approve-all"]')!.click();
      await settle(f);
      const accept = q<HTMLButtonElement>(f, '[data-testid="confirm-accept"]')!;
      expect(accept.disabled).toBe(true);
      expect(q(f, '[data-testid="confirm-scope"]')!.textContent).toContain(
        "2 owned by bob",
      );
    });

    it("groups tasks by Workstream and Repository and renders Part of stream links", async () => {
      const api = new FakeApi();
      api.live = [
        task("1", "queued", 5, { title: "Standalone task", repo: "acme/api" }),
        task("100", "queued", 5, { title: "Voice stream", repo: "acme/web" }),
        task("2", "queued", 5, {
          title: "Mic UI",
          repo: "acme/web",
          origin: "task:100",
          generatedFrom: "100",
        }),
      ];
      const f = await render(api, "backlog");
      const nav = vi.spyOn(TestBed.inject(Router), "navigate");
      const row2 = q(f, '[data-testid="row-2"]')!;
      const partOf = row2.querySelector<HTMLAnchorElement>(
        '[data-testid="tasks-part-of"]',
      );
      expect(partOf?.textContent).toContain("Part of Voice stream");
      expect(partOf?.getAttribute("href")).toContain("/workstreams/task:100");

      q<HTMLButtonElement>(f, '[data-testid="tasks-group"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(
        f,
        '[data-testid="tasks-group-opt-workstream"]',
      )!.click();
      expect(nav).toHaveBeenCalledWith([], {
        queryParams: { group: "workstream" },
        queryParamsHandling: "merge",
      });

      f.componentRef.setInput("group", "workstream");
      await settle(f);
      const headers = all(f, ".band-header").map((h) => h.textContent?.trim());
      expect(headers.some((h) => h?.includes("Voice stream"))).toBe(true);
      expect(headers.some((h) => h?.includes("Standalone tasks"))).toBe(true);

      f.componentRef.setInput("group", "repo");
      await settle(f);
      const repoHeaders = all(f, ".band-header").map((h) =>
        h.textContent?.trim(),
      );
      expect(repoHeaders.some((h) => h?.includes("acme/web"))).toBe(true);
      expect(repoHeaders.some((h) => h?.includes("acme/api"))).toBe(true);
    });

    it("renders right-pane empty state when tab has no rows, and supports split-controls + batch priority pill", async () => {
      const api = new FakeApi();
      api.live = [];
      const f = await render(api, "backlog");
      expect(q(f, '[data-testid="pane-close"]')).toBeNull();
      expect(q(f, '[data-testid="tasks-pane-empty"]')!.textContent).toContain(
        "Nothing in this tab",
      );
      q<HTMLButtonElement>(f, '[data-testid="tasks-split-42"]')!.click();
      await settle(f);
      expect(
        q(f, '[data-testid="tasks-resizer"]')!.getAttribute("aria-valuenow"),
      ).toBe("42");
      q<HTMLButtonElement>(f, '[data-testid="tasks-split-toggle"]')!.click();
      await settle(f);
      expect(q(f, ".tasks-split")!.classList).toContain("is-collapsed");
      q<HTMLButtonElement>(f, '[data-testid="tasks-split-30"]')!.click();
      await settle(f);
      expect(q(f, ".tasks-split")!.classList).not.toContain("is-collapsed");

      // Batch priority pill + Undo with zero <select> controls
      api.live = [task("1", "queued", 5), task("2", "queued", 3)];
      await TestBed.inject(TasksStore).refresh();
      await settle(f);
      q<HTMLInputElement>(
        f,
        '[data-testid="row-1"] input[type="checkbox"]',
      )!.click();
      await settle(f);
      expect(all(f, "select")).toHaveLength(0);
      q<HTMLButtonElement>(f, '[data-testid="batch-priority"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="batch-priority-opt-9"]')!.click();
      await settle(f);
      expect(api.calls).toContainEqual({
        method: "PATCH",
        route: "PATCH /api/tasks/{id}",
        params: { id: "1" },
        body: { priority: 9 },
      });
    });

    it("renders Repair badge, broken-base banner, inline Retry on failed tasks, Saved Boards CRUD, and Timeline view", async () => {
      const api = new FakeApi();
      api.gates = [
        {
          repo: "sidthe/grain",
          branch: "main",
          judged: false,
          reason: "failing",
          detail: "go test ./pkg/ui failed",
        },
      ];
      api.live = [
        task("77", "failed", 9, {
          title: "Fix broken main build",
          fixesBaseBranch: true,
        }),
        task("78", "running", 7, {
          title: "Stream telemetry",
          startedAt: "2026-11-11T10:00:00Z",
        }),
      ];
      const f = await render(api, "all");
      expect(q(f, '[data-testid="broken-base-banner"]')!.textContent).toContain(
        "sidthe/grain",
      );
      expect(q(f, '[data-testid="repair-badge-77"]')!.textContent).toBe(
        "Repair",
      );
      expect(q(f, '[data-testid="prio-77"]')!.classList).toContain(
        "prio-urgent",
      );

      // Inline Retry on failed row
      q<HTMLButtonElement>(f, '[data-testid="retry-77"]')!.click();
      await settle(f);
      expect(api.calls).toContainEqual({
        method: "POST",
        route: "POST /api/tasks/{id}/retry",
        params: { id: "77" },
        body: undefined,
      });

      // Saved Boards: save current view, apply, and delete
      f.componentRef.setInput("newBoard", "1");
      await settle(f);
      const nameInput = q<HTMLInputElement>(
        f,
        '[data-testid="board-name-input"]',
      )!;
      expect(nameInput).toBeTruthy();
      nameInput.value = "All Triage";
      nameInput.dispatchEvent(new Event("input"));
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="board-save-submit"]')!.click();
      await settle(f);
      expect(
        TestBed.inject(TasksStore)
          .boards()
          .map((b) => b.name),
      ).toEqual(["All Triage"]);

      const nav = vi
        .spyOn(TestBed.inject(Router), "navigate")
        .mockResolvedValue(true);
      q<HTMLButtonElement>(f, '[data-testid="saved-boards-btn"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="board-apply-b1"]')!.click();
      expect(nav).toHaveBeenCalledWith(["/tasks"], {
        queryParams: { tab: "all", group: null, q: null, repo: null },
      });

      q<HTMLButtonElement>(f, '[data-testid="saved-boards-btn"]')!.click();
      await settle(f);
      q<HTMLButtonElement>(f, '[data-testid="board-delete-b1"]')!.click();
      await settle(f);
      expect(TestBed.inject(TasksStore).boards()).toEqual([]);

      // Opening one menu closes sibling <g-pill-select> / <g-priority-chip> / Saved Boards menus
      expect(q(f, '[data-testid="saved-boards-menu"]')).not.toBeNull();
      q<HTMLButtonElement>(f, '[data-testid="tasks-group"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="saved-boards-menu"]')).toBeNull();
      expect(
        q(f, '[data-testid="tasks-group-menu"] li')?.getAttribute("role"),
      ).toBe("none");
      q<HTMLButtonElement>(f, '[data-testid="saved-boards-btn"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="tasks-group-menu"]')).toBeNull();
      expect(q(f, '[data-testid="saved-boards-menu"]')).not.toBeNull();
      q<HTMLButtonElement>(f, '[data-testid="saved-boards-btn"]')!.click();
      await settle(f);

      // Timeline view toggle keeps persistent right-hand inspector and shows row age
      q<HTMLButtonElement>(f, '[data-testid="tasks-view-timeline"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="tasks-timeline"]')).toBeTruthy();
      expect(q(f, ".timeline-bar")).toBeTruthy();
      expect(q(f, ".timeline-row-top .row-age")).toBeTruthy();
      expect(q(f, '[data-testid="tasks-pane"]')).toBeTruthy();
      // Auto-selected pane hides × when ?task= is unset
      expect(q(f, '[data-testid="pane-close"]')).toBeNull();
    });
  });
});
