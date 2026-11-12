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

import { Component } from "@angular/core";
import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { Router, provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, Template } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { SnackbarService } from "../services/snackbar.service";
import {
  STARTED_STORAGE_KEY,
  WorkstreamsStore,
} from "../workstreams/workstreams.store";
import { CreateTaskService } from "./create-task.service";
import { NewTaskSheetComponent } from "./new-task-sheet.component";

type Call = { route: string; params?: unknown; body?: unknown; query?: string };

class FakeApi {
  calls: Call[] = [];
  fail: Record<string, unknown> = {};
  templates: Template[] = [
    {
      id: "declared:template:sidthe:grain:bugfix",
      name: "Bugfix template",
      title: "Fix {{area}}",
      description: "Fix bug in {{area}}",
      declaredIn: { repo: "sidthe/grain" },
      fields: [{ name: "area", label: "Area", required: true }],
    },
  ];
  cfg: Record<string, unknown> = {
    access: null,
    approvedByDefault: true,
    defaultTarget: "sidthe/grain",
    models: ["fast", "smart"],
    defaultModel: "smart",
    capabilities: [{ id: "self-debug", name: "Self debug", ready: true }],
    defaultCapabilities: [],
    agentBackends: [{ id: "agy-ambient", name: "Antigravity ambient" }],
    customFields: [{ name: "component", label: "Component", type: "string" }],
    deployment: { repos: [{ repo: "sidthe/grain" }, { repo: "acme/web" }] },
  };
  config = () => Promise.resolve(this.cfg);
  tasks = () =>
    Promise.resolve([
      {
        id: "4",
        title: "Inbox split",
        state: "proposed",
        repo: "sidthe/grain",
      },
      {
        id: "1",
        title: "Director",
        state: "awaiting_reply",
        interactive: true,
      },
    ] as Task[]);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  getIfChanged = () => this.tasks().then((body) => ({ body, changed: true }));
  get = (route: string, params?: unknown, query?: string) => {
    this.calls.push({ route, params, query });
    if (route === "GET /api/tasks") return Promise.resolve({ tasks: [] });
    if (route === "GET /api/templates") {
      return Promise.resolve(this.templates);
    }
    if (route === "GET /api/templates/{id}/composition") {
      return Promise.resolve({
        kind: "task",
        repo: "sidthe/grain",
        tasks: [
          {
            title: "Fix auth",
            fields: [
              { name: "repo", values: ["sidthe/grain"], source: "template" },
              { name: "model", values: ["smart"], source: "deployment" },
            ],
          },
        ],
      });
    }
    if (route === "GET /api/deployment/reachable-repos") {
      return Promise.resolve(["sidthe/grain", "acme/web"]);
    }
    if (route === "GET /api/repos/{owner}/{name}/branches") {
      return Promise.resolve(["main", "release-1"]);
    }
    return Promise.resolve({
      sessions: [{ id: "director", title: "Director", taskId: "1" }],
    });
  };
  post = (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ route, params, body });
    const err = this.fail[route];
    if (err) {
      delete this.fail[route];
      return new Promise((_, reject) => setTimeout(() => reject(err)));
    }
    if (
      route === "POST /api/tasks" ||
      route === "POST /api/tasks/from-template"
    ) {
      return Promise.resolve({ id: "42" });
    }
    return Promise.resolve({});
  };
}

@Component({
  imports: [NewTaskSheetComponent],
  template: `<button id="opener" type="button">New</button
    ><g-new-task-sheet />`,
})
class HostComponent {}

async function render(api: FakeApi): Promise<ComponentFixture<HostComponent>> {
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const f = TestBed.createComponent(HostComponent);
  f.autoDetectChanges();
  await TestBed.inject(InboxStore).refresh();
  return f;
}

async function open(f: ComponentFixture<unknown>): Promise<void> {
  const opener = document.getElementById("opener")!;
  opener.focus();
  TestBed.inject(CreateTaskService).open({}, opener);
  await f.whenStable();
  await new Promise((r) => setTimeout(r));
  await f.whenStable();
}

function q<T extends HTMLElement = HTMLElement>(sel: string): T | null {
  return document.querySelector<T>(sel);
}

function type(sel: string, value: string): void {
  const el = q<HTMLInputElement>(sel)!;
  el.value = value;
  el.dispatchEvent(new Event("input"));
}

async function pickPill(
  f: ComponentFixture<unknown>,
  testid: string,
  optValue: string,
): Promise<void> {
  q<HTMLButtonElement>(`[data-testid="${testid}"]`)!.click();
  await f.whenStable();
  q<HTMLButtonElement>(`[data-testid="${testid}-opt-${optValue}"]`)!.click();
  await f.whenStable();
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 15));
  f.detectChanges();
  await f.whenStable();
}

describe("NewTaskSheetComponent", () => {
  afterEach(() => {
    TestBed.inject(InboxStore).stop();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("opens as a modal with focus on title, zero <select>s, zero Classic links, and deployment defaults", async () => {
    const api = new FakeApi();
    const f = await render(api);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    await open(f);
    const sheet = q('[data-testid="new-task-sheet"]')!;
    expect(sheet.getAttribute("aria-modal")).toBe("true");
    expect(document.activeElement).toBe(q('[data-testid="nt-title"]'));
    expect(q('[data-testid="nt-repo"]')?.textContent).toContain("sidthe/grain");
    expect(
      q('[data-testid="approved-switch"]')!.getAttribute("aria-checked"),
    ).toBe("true");
    expect(q('[data-testid="new-task-submit"]')!.textContent?.trim()).toBe(
      "Create and start",
    );

    // Zero native <select> elements & zero Classic links anywhere in the sheet.
    expect(sheet.querySelectorAll("select")).toHaveLength(0);
    expect(q('[data-testid="classic-form"]')).toBeNull();
    expect(sheet.textContent).not.toContain("classic UI");

    // Standing session tasks are not offered as dependencies in <g-pill-select>.
    q<HTMLButtonElement>('[data-testid="nt-depends"]')!.click();
    await f.whenStable();
    const depMenu = q('[data-testid="nt-depends-menu"]')!;
    expect(depMenu.textContent).toContain("#4 Inbox split");
    expect(depMenu.textContent).not.toContain("#1 Director");
  });

  it("asks for a title instead of filing an empty task", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(q('[data-testid="new-task-error"]')?.textContent).toContain(
      "Give the task a title.",
    );
    expect(api.calls.filter((c) => c.route.startsWith("POST "))).toHaveLength(
      0,
    );
  });

  it("files a proposal when Approve is off, attaches dependencies via <g-pill-select>, and offers Open and Discard", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Write the Sessions docs");
    q<HTMLButtonElement>('[data-testid="approved-switch"]')!.click();
    await f.whenStable();
    expect(q('[data-testid="new-task-submit"]')!.textContent?.trim()).toBe(
      "Save as proposal",
    );
    await pickPill(f, "nt-depends", "4");
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    const created = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(created).toMatchObject({
      route: "POST /api/tasks",
      body: {
        title: "Write the Sessions docs",
        approved: false,
        repo: "sidthe/grain",
        dependsOn: ["4"],
      },
    });
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    const snack = TestBed.inject(SnackbarService).current()!;
    expect(snack.message).toBe("Saved #42 as a proposal in the Inbox");
    expect(snack.secondary?.label).toBe("Open");
    expect(snack.action?.label).toBe("Discard");
    await TestBed.inject(SnackbarService).runAction(snack.id);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/close"),
    ).toMatchObject({
      params: { id: "42" },
      body: { close_pull_request: false },
    });
  });

  it("launches a repo-declared template with live composition preview via POST /api/tasks/from-template", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);

    await pickPill(f, "nt-template", "declared:template:sidthe:grain:bugfix");
    await settle(f);

    // Composition preview is rendered.
    expect(q('[data-testid="nt-composition-preview"]')?.textContent).toContain(
      "Preview: Fix auth",
    );

    // Required template field validation blocks empty submit.
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(q('[data-testid="new-task-error"]')?.textContent).toContain("Area");

    // Fill template field and submit -> calls POST /api/tasks/from-template!
    type('[data-testid="nt-tfield-area"]', "auth");
    await settle(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);

    const fromTmpl = api.calls.find(
      (c) => c.route === "POST /api/tasks/from-template",
    );
    expect(fromTmpl).toMatchObject({
      route: "POST /api/tasks/from-template",
      body: {
        repo: "sidthe/grain",
        template: "bugfix",
        fields: { area: "auth" },
        approved: true,
      },
    });
  });

  it("supports attachments, capabilities/grants, reads/writes, network, sandbox shape, backend, and custom fields", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);

    type('[data-testid="nt-title"]', "Full 22-field parity check");
    type('[data-testid="nt-cf-component"]', "scheduler");

    // Upload a file attachment via hidden file input.
    const fileInput = q<HTMLInputElement>(
      '[data-testid="nt-attachments-input"]',
    )!;
    const file = new File(["repro log"], "repro.txt", { type: "text/plain" });
    Object.defineProperty(fileInput, "files", { value: [file] });
    fileInput.dispatchEvent(new Event("change"));
    await settle(f);
    expect(q('[data-testid="staged-att-0"]')?.textContent).toContain(
      "repro.txt",
    );

    // Expand Capabilities & repo context section and toggle capability + read repo.
    q<HTMLButtonElement>('[data-testid="caps-options"]')!.click();
    await f.whenStable();
    q<HTMLButtonElement>('[data-testid="nt-grants-cap-self-debug"]')!.click();
    await pickPill(f, "nt-reads", "acme/web");

    // Expand More options and set sandbox shape, network, backend, noPort.
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await f.whenStable();
    expect(
      q('[data-testid="new-task-sheet"]')!.querySelectorAll("select"),
    ).toHaveLength(0);
    q<HTMLButtonElement>('[data-testid="nt-tier-medium"]')!.click();
    await pickPill(f, "nt-network", "none");
    await pickPill(f, "nt-backend", "agy-ambient");
    const noPort = q<HTMLInputElement>('[data-testid="nt-no-port"]')!;
    noPort.checked = true;
    noPort.dispatchEvent(new Event("change"));

    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);

    const post = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(post?.body).toMatchObject({
      title: "Full 22-field parity check",
      customFields: { component: "scheduler" },
      capabilities: ["self-debug"],
      reads: ["acme/web"],
      sandboxCpus: 4,
      sandboxMemoryMb: 8192,
      sandboxDiskGb: 32,
      networkAccess: "none",
      agentBackendId: "agy-ambient",
      noPort: true,
      attachments: [
        {
          filename: "repro.txt",
          contentType: "text/plain",
          content: btoa("repro log"),
        },
      ],
    });
  });

  it("files with Ctrl+Enter from anywhere in the sheet and keeps the sheet open on a refusal", async () => {
    const api = new FakeApi();
    api.fail["POST /api/tasks"] = {
      status: 400,
      error: { error: "repo not allowed" },
    };
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "x");
    const sw = q<HTMLButtonElement>('[data-testid="front-switch"]')!;
    sw.focus();
    sw.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    await settle(f);
    expect(api.calls.filter((c) => c.route === "POST /api/tasks")).toHaveLength(
      1,
    );
    expect(q('[data-testid="new-task-error"]')?.textContent).toContain(
      "repo not allowed",
    );
    expect(q('[data-testid="new-task-sheet"]')).toBeTruthy();
  });

  it("hands the draft to the Director and opens that conversation", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const nav = vi.spyOn(TestBed.inject(Router), "navigate");
    await open(f);
    type('[data-testid="nt-title"]', "Add a dark mode");
    q<HTMLButtonElement>('[data-testid="ask-director"]')!.click();
    await settle(f);
    const sent = api.calls.find(
      (c) => c.route === "POST /api/tasks/{id}/comments",
    );
    expect(sent?.params).toEqual({ id: "1" });
    expect((sent?.body as { body: string }).body).toContain(
      "**Add a dark mode**",
    );
    expect(nav).toHaveBeenCalledWith(["/sessions", "director"]);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
  });

  it("shows More options on request and closes on Escape, returning focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    expect(q('[data-testid="nt-nothing"]')).toBeNull();
    const more = q<HTMLButtonElement>('[data-testid="more-options"]')!;
    more.click();
    await f.whenStable();
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(q('[data-testid="nt-nothing"]')).toBeTruthy();
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    vi.advanceTimersByTime(5);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    expect(document.activeElement?.id).toBe("opener");
    vi.useRealTimers();
  });

  it("offers only Open after a started task: it cannot be taken back", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Run now");
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    const snack = TestBed.inject(SnackbarService).current()!;
    expect(snack.message).toBe("Filed #42; it starts when a slot is free");
    expect(snack.action?.label).toBe("Open");
    expect(snack.secondary).toBeUndefined();
  });

  it("keeps changes made while the config is still loading", async () => {
    const api = new FakeApi();
    let release!: () => void;
    const f = await render(api);
    api.config = () =>
      new Promise((r) => (release = () => r(api.cfg))) as Promise<
        typeof api.cfg
      >;
    await open(f);
    const sw = q<HTMLButtonElement>('[data-testid="approved-switch"]')!;
    sw.click();
    await f.whenStable();
    const chosen = sw.getAttribute("aria-checked");
    type('[data-testid="nt-title"]', "typed early");
    release();
    await settle(f);
    expect(
      q('[data-testid="approved-switch"]')!.getAttribute("aria-checked"),
    ).toBe(chosen);
    expect(q<HTMLInputElement>('[data-testid="nt-title"]')!.value).toBe(
      "typed early",
    );
  });

  it("brings a cancelled draft back on the next plain opening", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "half written");
    q<HTMLButtonElement>('[data-testid="new-task-close"]')!.click();
    await settle(f);
    await open(f);
    expect(q<HTMLInputElement>('[data-testid="nt-title"]')!.value).toBe(
      "half written",
    );
  });

  it("explains why filing is not allowed for this role", async () => {
    const api = new FakeApi();
    api.cfg["access"] = {
      full: false,
      role: "viewer",
      roles: ["viewer"],
      scopes: ["tasks:read"],
    };
    const f = await render(api);
    await open(f);
    await settle(f);
    expect(q('[data-testid="new-task-denied"]')?.textContent).toContain(
      "tasks:write",
    );
    expect(
      q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.disabled,
    ).toBe(true);
  });

  it("opens in Start a workstream mode, records the started root, and opens Workstreams", async () => {
    localStorage.removeItem(STARTED_STORAGE_KEY);
    const api = new FakeApi();
    const f = await render(api);
    const opener = document.getElementById("opener")!;
    TestBed.inject(CreateTaskService).open(
      { workstream: true, origin: "" },
      opener,
    );
    await settle(f);

    expect(q("#new-task-title")?.textContent?.trim()).toBe(
      "Start a workstream",
    );
    expect(q('[data-testid="ws-start-banner"]')).not.toBeNull();
    const autoSw = q<HTMLButtonElement>('[data-testid="auto-approve-switch"]')!;
    autoSw.click();
    type('[data-testid="nt-title"]', "Voice input stream");
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);

    const post = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(post?.body).toMatchObject({
      title: "Voice input stream",
      origin: "",
      autoApproveProposals: true,
    });
    expect(TestBed.inject(WorkstreamsStore).startedIds().has("42")).toBe(true);
    const navSpy = vi.spyOn(TestBed.inject(Router), "navigate");
    const snack = TestBed.inject(SnackbarService).current()!;
    expect(snack.message).toContain("workstream #42");
    await TestBed.inject(SnackbarService).runAction(snack.id);
    expect(navSpy).toHaveBeenCalledWith(["/workstreams", "task:42"]);
  });

  it("shows workstream context when prefilled with origin", async () => {
    const api = new FakeApi();
    const f = await render(api);
    TestBed.inject(CreateTaskService).open({ origin: "task:4" });
    await settle(f);

    expect(q('[data-testid="ws-origin-banner"]')?.textContent).toContain(
      "#4 Inbox split",
    );
  });

  it("sends a repo-declared template with a customised model through POST /api/tasks", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    await pickPill(f, "nt-template", "declared:template:sidthe:grain:bugfix");
    await settle(f);
    type('[data-testid="nt-tfield-area"]', "auth");
    await pickPill(f, "nt-model", "fast");
    await settle(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);

    const create = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(create).toBeTruthy();
    expect(create!.body).toMatchObject({ model: "fast" });
    expect(
      api.calls.some((c) => c.route === "POST /api/tasks/from-template"),
    ).toBe(false);
  });

  it("offers only pushable reachable repos as targets and every reachable repo for reads", async () => {
    const api = new FakeApi();
    const baseGet = api.get;
    api.get = ((route: string, params?: unknown, query?: string) => {
      if (route === "GET /api/deployment/reachable-repos") {
        api.calls.push({ route });
        return Promise.resolve({
          available: true,
          repos: [
            { repo: "acme/api", push: true },
            { repo: "acme/docs", push: false },
          ],
        });
      }
      return baseGet(route, params, query);
    }) as typeof api.get;
    const f = await render(api);
    await open(f);
    await settle(f);

    q<HTMLButtonElement>('[data-testid="nt-repo"]')!.click();
    await settle(f);
    expect(q('[data-testid="nt-repo-opt-acme/api"]')).toBeTruthy();
    expect(q('[data-testid="nt-repo-opt-acme/docs"]')).toBeNull();
    q<HTMLButtonElement>('[data-testid="nt-repo"]')!.click();
    await settle(f);

    q<HTMLButtonElement>('[data-testid="caps-options"]')!.click();
    await settle(f);
    q<HTMLButtonElement>('[data-testid="nt-reads"]')!.click();
    await settle(f);
    expect(q('[data-testid="nt-reads-opt-acme/docs"]')).toBeTruthy();
  });
});
