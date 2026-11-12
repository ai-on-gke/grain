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
import {
  Router,
  provideRouter,
  withComponentInputBinding,
} from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { NEVER } from "rxjs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { AccessService } from "../services/access.service";
import {
  WS_VIEW_STORAGE_KEY,
  WorkstreamsPageComponent,
} from "./workstreams-page.component";
import { WS_INSPECTOR_RATIO_KEY } from "./workstream-detail.component";
import { STARTED_STORAGE_KEY, WorkstreamsStore } from "./workstreams.store";

function t(id: string, over: Partial<Task> = {}): Task {
  return {
    id,
    title: `Task ${id}`,
    state: "queued",
    repo: "sidthe/grain-scratch-repo",
    author: "rarcea",
    authorKind: "human",
    owner: "rarcea",
    createdAt: "2026-11-11T09:00:00Z",
    updatedAt: "2026-11-11T10:00:00Z",
    ...over,
  };
}

class FakeApi {
  open: Task[] = [];
  closed: Task[] = [];
  details: Record<string, TaskDetail> = {};
  calls: { route: string; params?: unknown; body?: unknown; query?: string }[] =
    [];

  config = () => Promise.resolve({ access: null, viewerOwner: "rarcea" });
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  tasks = () => Promise.resolve(this.open);
  task = (id: string) =>
    Promise.resolve(
      this.details[id] ?? {
        ...([...this.open, ...this.closed].find((x) => x.id === id) ?? t(id)),
        description: `Description for #${id}`,
        comments: [],
        attempts: [],
      },
    );

  getIfChanged = vi.fn(async (route: string) => {
    this.calls.push({ route });
    if (route.includes("{id}")) {
      const m = /tasks\/([^/?]+)/.exec(route);
      const id = m?.[1] ?? "1";
      return { body: await this.task(id), changed: true };
    }
    return { body: this.open, changed: true };
  });

  get = vi.fn(
    async (route: string, params?: { id?: string }, query?: string) => {
      this.calls.push({ route, params, query });
      if (route === "GET /api/tasks") {
        const isFirst = !query || !query.includes("cursor=");
        return { tasks: isFirst ? this.closed : [] };
      }
      if (route === "GET /api/tasks/{id}" && params?.id) {
        return this.task(params.id);
      }
      if (route === "GET /api/tasks/{id}/tree" && params?.id) {
        return { origin: `task:${params.id}`, done: 0, total: 1, nodes: [] };
      }
      return {};
    },
  );

  post = vi.fn(async (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ route, params, body });
    return {};
  });
}

describe("WorkstreamsPageComponent", () => {
  let api: FakeApi;

  beforeEach(() => {
    localStorage.removeItem(WS_VIEW_STORAGE_KEY);
    localStorage.removeItem(STARTED_STORAGE_KEY);
    localStorage.removeItem(WS_INSPECTOR_RATIO_KEY);
    localStorage.removeItem("grain.ws.treeRatio");
    api = new FakeApi();
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: () => ({
        matches: false,
        media: "",
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    });
  });

  afterEach(() => {
    try {
      TestBed.inject(WorkstreamsStore).stop();
    } catch {
      // Ignore if test did not inject store.
    }
    TestBed.resetTestingModule();
  });

  async function mount(url = "/workstreams"): Promise<{
    fixture: ComponentFixture<WorkstreamsPageComponent>;
    router: Router;
    harness: RouterTestingHarness;
  }> {
    TestBed.configureTestingModule({
      imports: [WorkstreamsPageComponent],
      providers: [
        provideRouter(
          [
            { path: "workstreams", component: WorkstreamsPageComponent },
            { path: "workstreams/:key", component: WorkstreamsPageComponent },
            { path: "inbox", component: WorkstreamsPageComponent },
            { path: "**", children: [] },
          ],
          withComponentInputBinding(),
        ),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    TestBed.inject(AccessService).setConfig({
      access: null,
      viewerOwner: "rarcea",
    });
    const router = TestBed.inject(Router);
    const harness = await RouterTestingHarness.create(
      url,
      WorkstreamsPageComponent,
    );
    const fixture =
      harness.fixture as ComponentFixture<WorkstreamsPageComponent>;
    harness.detectChanges();
    await TestBed.inject(WorkstreamsStore).refresh(true);
    harness.detectChanges();
    await fixture.whenStable();
    harness.detectChanges();
    return { fixture, router, harness };
  }

  it("renders People scope with promoted system streams and collapsed System activity footer", async () => {
    api.open = [
      t("10", { title: "User goal", state: "completed" }),
      t("11", {
        title: "Subtask running",
        state: "running",
        origin: "task:10",
        generatedFrom: "10",
      }),
      t("20", {
        title: "Imported PR stream",
        state: "completed",
        origin: "sidthe/repo#7",
        imported: true,
      }),
      t("21", {
        title: "Proposed import step",
        state: "proposed",
        origin: "sidthe/repo#7",
        imported: true,
      }),
      t("30", {
        title: "Nightly suite",
        state: "completed",
        suiteRun: true,
        author: "suite",
        authorKind: "automation",
      }),
      t("31", {
        title: "Run tests",
        state: "running",
        origin: "task:30",
        generatedFrom: "30",
      }),
    ];

    const { fixture, harness } = await mount();
    const el = fixture.nativeElement as HTMLElement;

    expect(el.querySelector('[data-testid="ws-row-task:10"]')).not.toBeNull();
    expect(
      el.querySelector('[data-testid="ws-row-sidthe/repo#7"]'),
    ).not.toBeNull();
    expect(el.querySelector('[data-testid="ws-row-task:30"]')).toBeNull();
    expect(el.querySelector('[data-testid="ws-system-footer"]')).not.toBeNull();

    // Switch to Cards layout and verify SVG miniature trees render.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-view-cards"]',
    )!.click();
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(el.querySelector('[data-testid="ws-cards"]')).not.toBeNull();
    expect(el.querySelectorAll(".ws-thumb rect").length).toBeGreaterThan(0);

    // Reveal System scope and its kind chips.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-scope-system"]',
    )!.click();
    await harness.fixture.whenStable();
    harness.detectChanges();
    await new Promise((r) => setTimeout(r));
    harness.detectChanges();
    expect(el.querySelector('[data-testid="ws-card-task:30"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="ws-sub-suite"]')).not.toBeNull();
  });

  it("opens Start a workstream sheet from CTA button", async () => {
    const { fixture } = await mount();
    const el = fixture.nativeElement as HTMLElement;
    const createSvc = TestBed.inject(CreateTaskService);
    const spy = vi.spyOn(createSvc, "open");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-start-btn"]',
    )!.click();
    expect(spy).toHaveBeenCalledWith(
      { workstream: true, origin: "" },
      expect.anything(),
    );
  });

  it("renders detail tree, tidy graph, blocking chain, embedded inspector, and batch proposal approval", async () => {
    api.open = [
      t("100", { title: "Upstream stream", state: "completed" }),
      t("101", {
        title: "Blocker in upstream",
        state: "running",
        origin: "task:100",
        generatedFrom: "100",
      }),
      t("200", { title: "Voice stream", state: "completed" }),
      t("201", {
        title: "Proposed step",
        state: "proposed",
        origin: "task:200",
        generatedFrom: "200",
        dependsOn: ["101"],
      }),
      t("202", {
        title: "Second proposed step",
        state: "proposed",
        origin: "task:200",
        generatedFrom: "200",
      }),
    ];

    const { fixture, router } = await mount("/workstreams/task:200?node=201");
    const el = fixture.nativeElement as HTMLElement;

    expect(
      el.querySelector('[data-testid="ws-detail-title"]')?.textContent,
    ).toContain("Voice stream");
    expect(el.querySelector('[data-testid="ws-tree-node-201"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="ws-gnode-201"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="ws-portal-101"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="ws-blocker-101"]')).not.toBeNull();
    expect(el.querySelector("g-task-detail")).not.toBeNull();

    // Approve 2 proposals via confirmation modal while .ws-detail is inert.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-approve-proposals"]',
    )!.click();
    fixture.detectChanges();
    expect(
      el.querySelector('[data-testid="ws-detail"]')?.hasAttribute("inert"),
    ).toBe(true);
    expect(el.querySelector('[data-testid="confirm-dialog"]')).not.toBeNull();

    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(
      api.calls.filter((c) => c.route === "POST /api/tasks/{id}/approve")
        .length,
    ).toBe(2);

    // Open in Inbox button navigates with ?ws=task:200.
    const navSpy = vi.spyOn(router, "navigate");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-open-inbox"]',
    )!.click();
    expect(navSpy).toHaveBeenCalledWith(["/inbox"], {
      queryParams: { ws: "task:200" },
    });
  });

  it("preserves list query params when opening a stream and passes NO_REPO for no-repo streams", async () => {
    api.open = [
      t("300", { title: "No-repo stream", state: "completed", repo: "" }),
      t("301", {
        title: "Subtask",
        state: "proposed",
        repo: "",
        origin: "task:300",
        generatedFrom: "300",
      }),
    ];

    const { fixture, router } = await mount("/workstreams?scope=all&st=person");
    const navSpy = vi.spyOn(router, "navigate");
    const el = fixture.nativeElement as HTMLElement;

    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-row-task:300"]',
    )!.click();
    expect(navSpy).toHaveBeenCalledWith(["/workstreams", "task:300"], {
      queryParams: { node: null },
      queryParamsHandling: "merge",
    });

    const createSvc = TestBed.inject(CreateTaskService);
    const openSpy = vi.spyOn(createSvc, "open");
    await router.navigateByUrl("/workstreams/task:300?scope=all");
    await fixture.whenStable();
    fixture.detectChanges();

    el.querySelector<HTMLButtonElement>('[data-testid="ws-add-task"]')!.click();
    expect(openSpy).toHaveBeenCalledWith({
      origin: "task:300",
      repo: "__none__",
    });
  });

  it("uses <g-pill-select> with zero native <select> elements and supports 3-column inspector resizer + running halos", async () => {
    api.open = [
      t("500", {
        title: "Observability rollout",
        state: "completed",
        repo: "sidthe/grain",
      }),
      t("501", {
        title: "Add Prometheus exporter",
        state: "running",
        repo: "sidthe/grain",
        origin: "task:500",
        generatedFrom: "500",
      }),
    ];

    const { fixture, router } = await mount("/workstreams");
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelectorAll("select")).toHaveLength(0);

    // Filter by repository via <g-pill-select>
    const navSpy = vi.spyOn(router, "navigate");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-repo-filter"]',
    )!.click();
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-repo-filter-opt-sidthe/grain"]',
    )!.click();
    expect(navSpy).toHaveBeenCalledWith([], {
      queryParams: { repo: "sidthe/grain" },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });

    // Open workstream detail: verify 21% tree, 42% inspector default, running node halos, and widening up to 55%
    await router.navigateByUrl("/workstreams/task:500?node=501");
    await fixture.whenStable();
    fixture.detectChanges();

    expect(el.querySelectorAll("select")).toHaveLength(0);
    expect(el.querySelectorAll("rect.ws-run-halo").length).toBe(3);

    const treeResizer = el.querySelector<HTMLElement>(
      '[data-testid="ws-tree-resizer"]',
    )!;
    const inspResizer = el.querySelector<HTMLElement>(
      '[data-testid="ws-inspector-resizer"]',
    )!;
    expect(treeResizer.getAttribute("aria-valuenow")).toBe("21");
    expect(inspResizer.getAttribute("aria-valuenow")).toBe("42");

    inspResizer.dispatchEvent(
      new KeyboardEvent("keydown", { key: "End", bubbles: true }),
    );
    fixture.detectChanges();
    expect(inspResizer.getAttribute("aria-valuenow")).toBe("55");
    expect(localStorage.getItem(WS_INSPECTOR_RATIO_KEY)).toBe("0.5500");

    inspResizer.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    fixture.detectChanges();
    expect(inspResizer.getAttribute("aria-valuenow")).toBe("42");
  });
});
