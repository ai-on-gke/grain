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
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { KeyboardService } from "../services/keyboard.service";
import { ViewFilter } from "../services/view-filter.service";
import { SnackbarService } from "../services/snackbar.service";
import { AgentsPageComponent } from "./agents-page.component";
import { AgentsStore } from "./agents.store";

const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();

class FakeApi {
  live: Task[] = [];
  details: Record<string, TaskDetail> = {};
  sandboxes: unknown = { host: { cpus: 8 } };
  posts: { route: string; body: unknown }[] = [];
  postResult: unknown = { delivered_count: 2 };
  postFails = false;
  // The list GET answers 304 with the shared newest body, as GrainApi does
  // after another page took the fresh copy.
  listUnchanged = false;
  detailReads: string[] = [];
  failDetail = new Set<string>();

  getIfChanged = (route: string, p?: { id?: string }) => {
    if (route === "GET /api/tasks") {
      return Promise.resolve({
        body: structuredClone(this.live),
        changed: !this.listUnchanged,
      });
    }
    this.detailReads.push(p?.id ?? "");
    if (this.failDetail.has(p?.id ?? "")) {
      return new Promise((_r, rej) => setTimeout(() => rej(new Error("404"))));
    }
    return Promise.resolve({
      body:
        this.details[p?.id ?? ""] ??
        ({ attempts: [] } as unknown as TaskDetail),
      changed: true,
    });
  };
  get = () => Promise.resolve(this.sandboxes);
  post = (route: string, _p: unknown, body: unknown) => {
    this.posts.push({ route, body });
    if (this.postFails) {
      return new Promise((_r, rej) => setTimeout(() => rej(new Error("nope"))));
    }
    return Promise.resolve(this.postResult);
  };
}

function run(id: string, extra: Partial<Task> = {}): Task {
  return {
    id,
    title: `Task ${id}`,
    state: "running",
    activity: `doing ${id}`,
    activityAt: iso(60000),
    ...extra,
  } as Task;
}

async function render(
  api: FakeApi,
): Promise<ComponentFixture<AgentsPageComponent>> {
  TestBed.configureTestingModule({
    imports: [AgentsPageComponent],
    providers: [
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
      provideRouter([{ path: "**", children: [] }]),
    ],
  });
  const fixture = TestBed.createComponent(AgentsPageComponent);
  fixture.autoDetectChanges();
  const store = TestBed.inject(AgentsStore);
  await store.refresh();
  await store.refreshSlow();
  await fixture.whenStable();
  return fixture;
}

const el = (f: ComponentFixture<unknown>) => f.nativeElement as HTMLElement;
const q = (f: ComponentFixture<unknown>, sel: string) =>
  el(f).querySelector<HTMLElement>(sel);
const cards = (f: ComponentFixture<unknown>) =>
  [...el(f).querySelectorAll<HTMLElement>(".agent-card")].map(
    (c) => c.dataset["testid"],
  );

function key(k: string): void {
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: k, bubbles: true }),
  );
}

describe("AgentsPageComponent", () => {
  afterEach(() => document.body.classList.remove("agents-wall"));

  it("shows one card per running task, quiet first with a reason", async () => {
    const api = new FakeApi();
    api.live = [
      run("1"),
      run("2", { activityAt: iso(25 * 60000) }),
      run("3", { state: "queued" } as Partial<Task>),
      run("4", { consoleSession: true } as Partial<Task>),
    ];
    const f = await render(api);
    expect(cards(f)).toEqual(["agent-2", "agent-1"]);
    const quiet = q(f, '[data-testid="agent-2"]')!;
    expect(quiet.classList).toContain("is-quiet");
    expect(
      quiet.querySelector('[data-testid="quiet-line"]')!.textContent,
    ).toContain("No update for 25m");
    expect(
      q(f, '[data-testid="agent-1"] [data-testid="quiet-line"]'),
    ).toBeNull();
    expect(q(f, '[data-testid="agent-1"] a')!.getAttribute("href")).toBe(
      "/task/1",
    );
  });

  it("sums the strip: working, setting up, quiet and tokens", async () => {
    const api = new FakeApi();
    api.live = [
      run("1"),
      run("2", { activityBySetup: true } as Partial<Task>),
      run("3", { activityAt: iso(20 * 60000) }),
    ];
    api.details = {
      "1": {
        attempts: [{ number: 1, promptTokens: 1200, model: "m" }],
      } as unknown as TaskDetail,
      "3": {
        attempts: [{ number: 2, promptTokens: 300 }],
      } as unknown as TaskDetail,
    };
    const f = await render(api);
    const s = q(f, '[data-testid="agents-summary"]')!.textContent!.replace(
      /\s+/g,
      " ",
    );
    expect(s).toContain("1 working");
    expect(s).toContain("1 setting up");
    expect(s).toContain("1 quiet");
    expect(s).toContain(`≈${(1500).toLocaleString()} prompt tokens (est.)`);
    expect(q(f, '[data-testid="agent-1"] .agent-foot')!.textContent).toContain(
      "run 1",
    );
    expect(q(f, '[data-testid="agent-2"] .grain-chip')).not.toBeNull();
  });

  it("filters by status and switches to the list layout", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2", { activityAt: iso(20 * 60000) })];
    const f = await render(api);
    q(f, '[data-testid="agents-filter-quiet"]')!.click();
    await f.whenStable();
    expect(cards(f)).toEqual(["agent-2"]);
    q(f, '[data-testid="agents-filter-all"]')!.click();
    q(f, '[data-testid="layout-list"]')!.click();
    await f.whenStable();
    expect(cards(f)).toEqual([]);
    expect(q(f, '[data-testid="agent-table"]')).not.toBeNull();
    expect(q(f, '[data-testid="agent-row-2"]')!.classList).toContain(
      "is-quiet",
    );
  });

  it("groups by repository", async () => {
    const api = new FakeApi();
    api.live = [run("1", { repo: "o/b" }), run("2", { repo: "o/a" })];
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="agents-group"]')!.click();
    await f.whenStable();
    q<HTMLButtonElement>(f, '[data-testid="agents-group-opt-repo"]')!.click();
    await f.whenStable();
    const heads = [...el(f).querySelectorAll(".band-header")].map(
      (h) => h.textContent!.trim().split(/\s+/)[0],
    );
    expect(heads).toEqual(["o/a", "o/b"]);
  });

  it("shows CPU as unknown with a reason when there is no probe", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2")];
    const f = await render(api);
    expect(
      q(f, '[data-testid="agent-1"] .cpu .sr-only')!.textContent,
    ).toContain("No CPU reading");
    expect(q(f, '[data-testid="agent-1"] .cpu-meter')).toBeNull();
    expect(q(f, '[data-testid="agents-summary"]')!.textContent).not.toContain(
      "vCPU",
    );
  });

  it("draws the CPU meter from a matching sandbox probe", async () => {
    const api = new FakeApi();
    api.live = [run("1", { activityAt: iso(15 * 60000) })];
    api.sandboxes = {
      sandboxes: [
        { name: "grain-task-1", cpus: 2, loadAverage: "0.2 0.1 0.1" },
      ],
    };
    const f = await render(api);
    const card = q(f, '[data-testid="agent-1"]')!;
    expect(card.querySelector(".cpu-fill")!.classList).toContain("low");
    expect(card.querySelector<HTMLElement>(".cpu-fill")!.style.width).toBe(
      "10%",
    );
    expect(
      card.querySelector('[data-testid="quiet-line"]')!.textContent,
    ).toContain("likely stuck");
    expect(q(f, '[data-testid="agents-summary"]')!.textContent).toContain(
      "of 2 vCPU",
    );
  });

  it("sends a notice to running agents", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2")];
    const f = await render(api);
    TestBed.inject(AccessService).setConfig(null);
    q(f, '[data-testid="contact-open"]')!.click();
    await f.whenStable();
    const ta = q(f, '[data-testid="contact-text"]') as HTMLTextAreaElement;
    ta.value = "  please rebase  ";
    ta.dispatchEvent(new Event("input"));
    await f.whenStable();
    q(f, '[data-testid="contact-send"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    await f.whenStable();
    expect(api.posts).toEqual([
      {
        route: "POST /api/deployment/broadcast",
        body: { message: "please rebase", running_only: true },
      },
    ]);
    expect(q(f, '[data-testid="contact-dialog"]')).toBeNull();
    expect(TestBed.inject(SnackbarService).current()?.message).toContain(
      "Notice sent to 2 running agents",
    );
  });

  it("keeps the dialog open and says so when the notice fails", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    api.postFails = true;
    const f = await render(api);
    q(f, '[data-testid="contact-open"]')!.click();
    await f.whenStable();
    const ta = q(f, '[data-testid="contact-text"]') as HTMLTextAreaElement;
    ta.value = "hi";
    ta.dispatchEvent(new Event("input"));
    await f.whenStable();
    q(f, '[data-testid="contact-send"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    await f.whenStable();
    expect(q(f, '[data-testid="contact-dialog"]')).not.toBeNull();
    expect(TestBed.inject(SnackbarService).current()?.message).toContain(
      "was not sent",
    );
  });

  it("toggles the wall view with f and leaves it with Escape", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    const f = await render(api);
    key("f");
    await f.whenStable();
    expect(document.body.classList).toContain("agents-wall");
    expect(
      q(f, '[data-testid="wall-toggle"]')!.getAttribute("aria-pressed"),
    ).toBe("true");
    key("Escape");
    await f.whenStable();
    expect(document.body.classList).not.toContain("agents-wall");
    key("f");
    f.destroy();
    expect(document.body.classList).not.toContain("agents-wall");
  });

  it("ignores f when shortcuts are off, and lists it in the help", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    const f = await render(api);
    const kb = TestBed.inject(KeyboardService);
    expect(kb.list().some((s) => s.key === "f")).toBe(true);
    kb.setEnabled(false);
    key("f");
    await f.whenStable();
    expect(document.body.classList).not.toContain("agents-wall");
    kb.setEnabled(true);
    f.destroy();
    expect(kb.list().some((s) => s.key === "f")).toBe(false);
  });

  it("closes the dialog with Escape from the textarea and returns focus", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    const f = await render(api);
    q(f, '[data-testid="contact-open"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    await f.whenStable();
    const ta = q(f, '[data-testid="contact-text"]')!;
    expect(document.activeElement).toBe(ta);
    expect(q(f, ".agents-panel")!.hasAttribute("inert")).toBe(true);
    ta.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await new Promise((r) => setTimeout(r, 5));
    await f.whenStable();
    expect(q(f, '[data-testid="contact-dialog"]')).toBeNull();
    expect(document.activeElement).toBe(q(f, '[data-testid="contact-open"]'));
  });

  it("ignores the search box in wall view and says so", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2")];
    const f = await render(api);
    TestBed.inject(ViewFilter).query.set("Task 1");
    await f.whenStable();
    expect(cards(f)).toEqual(["agent-1"]);
    q(f, '[data-testid="wall-toggle"]')!.click();
    await f.whenStable();
    expect(cards(f).sort()).toEqual(["agent-1", "agent-2"]);
    expect(q(f, '[data-testid="wall-filter-note"]')).not.toBeNull();
  });

  it("takes the shared newest list from an unchanged answer", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    const f = await render(api);
    api.live = [run("2")];
    api.listUnchanged = true;
    await TestBed.inject(AgentsStore).refresh();
    await f.whenStable();
    expect(cards(f)).toEqual(["agent-2"]);
  });

  it("shows interactive running tasks", async () => {
    const api = new FakeApi();
    api.live = [run("1", { interactive: true } as Partial<Task>)];
    const f = await render(api);
    expect(cards(f)).toEqual(["agent-1"]);
  });

  it("drops details of finished runs and does not hammer a failing detail", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2")];
    api.failDetail.add("2");
    const f = await render(api);
    const store = TestBed.inject(AgentsStore);
    await new Promise((r) => setTimeout(r, 5));
    const reads = api.detailReads.filter((id) => id === "2").length;
    expect(reads).toBeGreaterThan(0);
    await store.refresh();
    await store.refresh();
    await new Promise((r) => setTimeout(r, 5));
    expect(api.detailReads.filter((id) => id === "2").length).toBe(reads);
    expect(Object.keys(store.details())).toEqual(["1"]);
    api.live = [run("3")];
    await store.refresh();
    await store.refreshSlow();
    await new Promise((r) => setTimeout(r, 5));
    await f.whenStable();
    expect(Object.keys(store.details())).toEqual(["3"]);
  });

  it("does not claim nothing is running when the list cannot be read", async () => {
    const api = new FakeApi();
    api.getIfChanged = () =>
      new Promise((_r, rej) => setTimeout(() => rej(new Error("down"))));
    const f = await render(api);
    expect(q(f, '[role="alert"]')!.textContent).toContain("could not be read");
    expect(q(f, '[data-testid="agents-empty"]')).toBeNull();
  });

  it("speaks only the quiet count", async () => {
    const api = new FakeApi();
    api.live = [run("1"), run("2", { activityAt: iso(20 * 60000) })];
    const f = await render(api);
    expect(
      q(f, '[data-testid="agents-summary"]')!.getAttribute("role"),
    ).toBeNull();
    expect(q(f, '[data-testid="agents-live"]')!.textContent!.trim()).toBe(
      "1 run quiet",
    );
  });

  it("points at the Backlog when nothing is running", async () => {
    const api = new FakeApi();
    api.live = [run("3", { state: "queued" } as Partial<Task>)];
    const f = await render(api);
    const empty = q(f, '[data-testid="agents-empty"]')!;
    expect(empty.querySelector("a")!.getAttribute("href")).toBe(
      "/tasks?tab=backlog",
    );
    expect(q(f, '[data-testid="contact-open"]')).toBeNull();
  });

  it("shows a loading state, not 0 working, until the first read completes", async () => {
    const api = new FakeApi();
    api.live = [run("1")];
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const read = api.getIfChanged;
    api.getIfChanged = (route: string, p?: { id?: string }) =>
      route === "GET /api/tasks"
        ? gate.then(() => read(route, p))
        : read(route, p);
    TestBed.configureTestingModule({
      imports: [AgentsPageComponent],
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
        provideRouter([{ path: "**", children: [] }]),
      ],
    });
    const f = TestBed.createComponent(AgentsPageComponent);
    f.autoDetectChanges();
    await f.whenStable();
    const summary = q(f, '[data-testid="agents-summary"]')!;
    expect(summary.textContent).not.toContain("working");
    expect(q(f, '[data-testid="agents-loading"]')).toBeTruthy();
    expect(summary.getAttribute("aria-busy")).toBe("true");

    release();
    await TestBed.inject(AgentsStore).refresh();
    await f.whenStable();
    expect(q(f, '[data-testid="agents-loading"]')).toBeNull();
    expect(summary.textContent).toContain("1");
    expect(summary.textContent).toContain("working");
  });

  it("keeps loaded=false and shows load failure note when the initial read fails", async () => {
    const api = new FakeApi();
    api.getIfChanged = () =>
      new Promise((_r, rej) => setTimeout(() => rej(new Error("boom"))));
    TestBed.configureTestingModule({
      imports: [AgentsPageComponent],
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
        provideRouter([{ path: "**", children: [] }]),
      ],
    });
    const f = TestBed.createComponent(AgentsPageComponent);
    f.autoDetectChanges();
    await new Promise((r) => setTimeout(r, 15));
    await TestBed.inject(AgentsStore).refresh();
    await new Promise((r) => setTimeout(r, 15));
    await f.whenStable();
    expect(TestBed.inject(AgentsStore).loaded()).toBe(false);
    expect(q(f, '[data-testid="agents-load-failed"]')!.textContent).toContain(
      "Running agents could not be loaded.",
    );
  });

  it("shows Part of workstream links on agent cards and table rows", async () => {
    const api = new FakeApi();
    api.live = [
      { id: "90", title: "Voice goal", state: "completed" } as Task,
      run("1", { origin: "task:90", generatedFrom: "90" }),
    ];
    const f = await render(api);
    const cardLink = q<HTMLAnchorElement>(f, '[data-testid="agent-part-of"]');
    expect(cardLink?.textContent).toContain("Part of Voice goal");
    expect(cardLink?.getAttribute("href")).toContain("/workstreams/task:90");

    q<HTMLButtonElement>(f, '[data-testid="layout-list"]')!.click();
    await f.whenStable();
    const rowLink = q<HTMLAnchorElement>(
      f,
      '[data-testid="agent-row-part-of"]',
    );
    expect(rowLink?.textContent).toContain("Part of Voice goal");
  });

  it("renders polyline sparkline, .is-fresh pulse on recent activity, and reactive Aurora states", async () => {
    const api = new FakeApi();
    api.live = [
      run("1", { activityAt: iso(4000), author: "ana", authorKind: "human" }),
    ];
    const f = await render(api);

    const summary = q(f, '[data-testid="agents-summary"]')!;
    expect(summary.getAttribute("data-aurora-state")).toBe("processing");

    const card = q(f, '[data-testid="agent-1"]')!;
    expect(card.classList).toContain("is-fresh");
    expect(card.querySelector(".spark-line")).not.toBeNull();
    expect(card.querySelector('[data-testid="actor-chip"]')).not.toBeNull();

    // When all agents finish, summary pulses "responding" then settles to "idle".
    api.live = [];
    await TestBed.inject(AgentsStore).refresh();
    await f.whenStable();
    expect(summary.getAttribute("data-aurora-state")).toBe("responding");

    await new Promise((r) => setTimeout(r, 500));
    await f.whenStable();
    expect(summary.getAttribute("data-aurora-state")).toBe("idle");
  });
});
