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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { KeyboardService } from "../services/keyboard.service";
import { WorkstreamDetailComponent } from "./workstream-detail.component";
import { deriveWorkstreams } from "./workstream-model";
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
  calls: { route: string; params?: unknown; body?: unknown }[] = [];

  config = () => Promise.resolve({ access: null, viewerOwner: "rarcea" });
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  tasks = () => Promise.resolve(this.open);
  task = (id: string) =>
    Promise.resolve({
      ...(this.open.find((x) => x.id === id) ?? t(id)),
      description: `Description #${id}`,
      comments: [],
      attempts: [],
    } as TaskDetail);

  getIfChanged = vi.fn(async (route: string) => {
    if (route.includes("{id}")) {
      const m = /tasks\/([^/?]+)/.exec(route);
      const id = m?.[1] ?? "1";
      return { body: await this.task(id), changed: true };
    }
    return { body: this.open, changed: true };
  });

  get = vi.fn(async () => ({ tasks: [] }));
  post = vi.fn(async (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ route, params, body });
    return {};
  });
}

describe("WorkstreamDetailComponent", () => {
  let api: FakeApi;

  beforeEach(() => {
    localStorage.removeItem(STARTED_STORAGE_KEY);
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
      // ignore
    }
    TestBed.resetTestingModule();
  });

  function setup(tasks: Task[], nodeId = "101") {
    api.open = tasks;
    const d = deriveWorkstreams(tasks, {
      now: Date.parse("2026-11-11T12:00:00Z"),
    });
    const stream = d.byKey.get("task:100")!;
    TestBed.configureTestingModule({
      imports: [WorkstreamDetailComponent],
      providers: [
        provideRouter([]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    TestBed.inject(AccessService).setConfig({
      access: null,
      viewerOwner: "rarcea",
    });
    const fixture: ComponentFixture<WorkstreamDetailComponent> =
      TestBed.createComponent(WorkstreamDetailComponent);
    fixture.componentRef.setInput("stream", stream);
    fixture.componentRef.setInput("nodeId", nodeId);
    fixture.componentRef.setInput(
      "allNodesById",
      new Map(d.streams.flatMap((s) => [...s.byId.entries()])),
    );
    fixture.componentRef.setInput("taskToStream", d.taskToStream);
    fixture.detectChanges();
    return { fixture, stream, kb: TestBed.inject(KeyboardService) };
  }

  it("renders ARIA treeitem attributes without putting aria-selected on inner buttons and keeps fallback tabindex when collapsed", () => {
    const { fixture } = setup(
      [
        t("100", { title: "Root goal", state: "completed" }),
        t("101", {
          title: "Parent step",
          state: "running",
          origin: "task:100",
          generatedFrom: "100",
        }),
        t("102", {
          title: "Nested step",
          state: "proposed",
          origin: "task:100",
          generatedFrom: "101",
        }),
      ],
      "102",
    );
    const el = fixture.nativeElement as HTMLElement;

    const treeItems = [...el.querySelectorAll('li[role="treeitem"]')];
    expect(treeItems.length).toBe(3);
    const btn102 = el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-tree-node-102"]',
    )!;
    expect(btn102.hasAttribute("aria-selected")).toBe(false);
    expect(btn102.tabIndex).toBe(0);
    expect(
      btn102.closest('li[role="treeitem"]')?.getAttribute("aria-selected"),
    ).toBe("true");
    expect(
      btn102.closest('li[role="treeitem"]')?.getAttribute("aria-posinset"),
    ).toBe("1");

    // Collapsing parent 101 hides 102 and preserves a tabbable tabindex=0 on the tree.
    btn102
      .closest("ul")!
      .querySelector<HTMLElement>(
        '[data-testid="ws-tree-node-101"] .ws-ti-toggle',
      )!
      .click();
    fixture.detectChanges();

    expect(el.querySelector('[data-testid="ws-tree-node-102"]')).toBeNull();
    const tabbables = [
      ...el.querySelectorAll<HTMLElement>('.ws-tree button[tabindex="0"]'),
    ];
    expect(tabbables.length).toBe(1);
  });

  it("wraps graph in region without svg role=img, toggles resizer collapse, and labels pause confirm dialog accurately", () => {
    const { fixture, kb } = setup([
      t("100", {
        title: "Multi-repo goal",
        state: "completed",
        repo: "acme/a",
      }),
      t("101", {
        title: "Queue A",
        state: "queued",
        repo: "acme/a",
        origin: "task:100",
        generatedFrom: "100",
        dependsOn: ["201"],
      }),
      t("102", {
        title: "Queue B",
        state: "proposed",
        repo: "acme/b",
        origin: "task:100",
        generatedFrom: "100",
      }),
      t("200", { title: "Ext root", state: "completed", repo: "acme/c" }),
      t("201", {
        title: "Ext dep",
        state: "running",
        repo: "acme/c",
        origin: "task:200",
        generatedFrom: "200",
      }),
    ]);
    const el = fixture.nativeElement as HTMLElement;

    const svg = el.querySelector<SVGElement>('[data-testid="ws-graph"]')!;
    expect(svg.hasAttribute("role")).toBe(false);
    expect(
      svg.closest('[role="region"]')?.getAttribute("aria-label"),
    ).toContain("Workstream dependency");

    const modeSpy = vi.fn();
    fixture.componentInstance.modeChange.subscribe(modeSpy);

    // Resizer Enter toggles collapsed class and aria-valuetext.
    const resizer = el.querySelector<HTMLElement>(".ws-resizer")!;
    resizer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    fixture.detectChanges();
    expect(
      el.querySelector(".ws-dbody")?.classList.contains("is-collapsed"),
    ).toBe(true);
    expect(resizer.getAttribute("aria-valuetext")).toBe("Collapsed");

    // Multi-repo Pause dialog uses "pausing tasks" rather than "approving work".
    el.querySelector<HTMLButtonElement>(
      '[data-testid="ws-pause-stream"]',
    )!.click();
    fixture.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-multi-repo"]')?.closest("label")
        ?.textContent,
    ).toContain("I am pausing tasks in 2 repositories");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    fixture.detectChanges();

    // Shortcuts respect modifier keys and KeyboardService.enabled().
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "m", ctrlKey: true, bubbles: true }),
    );
    expect(modeSpy).not.toHaveBeenCalled();

    kb.setEnabled(false);
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "m", bubbles: true }),
    );
    expect(modeSpy).not.toHaveBeenCalled();

    kb.setEnabled(true);
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "m", bubbles: true }),
    );
    expect(modeSpy).toHaveBeenCalledWith("list");
  });
});
