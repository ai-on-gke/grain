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
import type { Task } from "../api/models";
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
  calls: Call[] = [];

  getIfChanged = () =>
    Promise.resolve({ body: structuredClone(this.live), changed: true });
  queries: string[] = [];
  get = (_route: string, _p?: unknown, query?: string) => {
    this.queries.push(query ?? "");
    return Promise.resolve({ tasks: this.closed, nextCursor: "", total: 1 });
  };
  // The shell's Inbox store, refreshed after actions.
  config = () => Promise.resolve({ access: null });
  tasks = () => Promise.resolve(this.live);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  private rec(method: string, route: string, params?: unknown, body?: unknown) {
    this.calls.push({ method, route, params, body });
    return Promise.resolve({});
  }
  post = (route: string, params?: unknown, body?: unknown) =>
    this.rec("POST", route, params, body);
  patch = (route: string, params?: unknown, body?: unknown) =>
    this.rec("PATCH", route, params, body);
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
});
