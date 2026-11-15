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
    type('[data-testid="nt-found-in"]', "v2.5.0-rc.2");

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
      foundIn: "v2.5.0-rc.2",
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

    // Ctrl+Enter must also respect !canFile() instead of bypassing the disabled submit button.
    type('[data-testid="nt-title"]', "Attempt forbidden Ctrl+Enter");
    q<HTMLInputElement>('[data-testid="nt-title"]')!.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    await settle(f);
    expect(api.calls.filter((c) => c.route === "POST /api/tasks")).toHaveLength(
      0,
    );
  });

  it("validates a missing repository inline without claiming a deployment default target when none is configured", async () => {
    const api = new FakeApi();
    delete api.cfg["defaultTarget"];
    const f = await render(api);
    await open(f);
    await settle(f);

    const repoBtn = q<HTMLButtonElement>('[data-testid="nt-repo"]')!;
    expect(repoBtn.textContent).toContain("Select a repository…");
    expect(repoBtn.textContent).not.toContain("Deployment default target");

    type('[data-testid="nt-title"]', "Check repo validation");
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);

    expect(q('[data-testid="new-task-error"]')?.textContent).toContain(
      "Pick a repository, or choose No repository.",
    );
    expect(repoBtn.classList.contains("is-invalid")).toBe(true);
    expect(repoBtn.getAttribute("aria-describedby")).toBe("new-task-error");
    expect(document.activeElement).toBe(repoBtn);
    expect(api.calls.filter((c) => c.route === "POST /api/tasks")).toHaveLength(
      0,
    );

    await pickPill(f, "nt-repo", "__none__");
    await settle(f);
    expect(q('[data-testid="new-task-error"]')).toBeNull();
    expect(repoBtn.classList.contains("is-invalid")).toBe(false);

    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    const created = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(created?.body).toMatchObject({
      title: "Check repo validation",
      repo: "",
      noRepo: true,
    });
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

  it("keeps primary target picker strictly within targetRepos when an allowlist is set", async () => {
    const api = new FakeApi();
    api.cfg = {
      ...api.cfg,
      targetRepos: ["acme/web"],
      defaultTarget: "acme/web",
      deployment: { targetRepos: ["acme/web"] },
    };
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
    expect(q('[data-testid="nt-repo-opt-acme/web"]')).toBeTruthy();
    expect(q('[data-testid="nt-repo-opt-acme/api"]')).toBeNull();
  });

  it("loads templates and branches immediately without blocking on slow reachable-repos", async () => {
    const api = new FakeApi();
    const baseGet = api.get;
    api.get = ((route: string, params?: unknown, query?: string) => {
      if (route === "GET /api/deployment/reachable-repos") {
        api.calls.push({ route });
        return new Promise(() => undefined);
      }
      if (route === "GET /api/repos/{owner}/{name}/branches") {
        api.calls.push({ route, params, query });
        return Promise.resolve({
          branches: [{ name: "main" }, { name: "release/v1" }],
        });
      }
      return baseGet(route, params, query);
    }) as typeof api.get;

    const f = await render(api);
    await open(f);
    await settle(f);

    expect(api.calls.some((c) => c.route === "GET /api/templates")).toBe(true);
    expect(
      api.calls.some(
        (c) => c.route === "GET /api/repos/{owner}/{name}/branches",
      ),
    ).toBe(true);
    // Template option populated even while reachable-repos is still pending.
    q<HTMLButtonElement>('[data-testid="nt-template"]')!.click();
    await settle(f);
    expect(
      q(
        '[data-testid="nt-template-opt-declared:template:sidthe:grain:bugfix"]',
      ),
    ).toBeTruthy();
  });

  it("files Task priority, Owner, Keep running and Admission from More options", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Priority parity");
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    expect(q<HTMLInputElement>('[data-testid="nt-priority"]')!.value).toBe("5");

    const submit = q<HTMLButtonElement>('[data-testid="new-task-submit"]')!;
    for (const bad of ["0", "-1", "2.5"]) {
      type('[data-testid="nt-priority"]', bad);
      await settle(f);
      expect(submit.disabled).toBe(true);
      expect(q('[data-testid="nt-priority-help"]')!.textContent).toBe(
        "a whole number, 1 or more; default 5, higher numbers run first",
      );
      expect(
        q('[data-testid="nt-priority"]')!.getAttribute("aria-invalid"),
      ).toBe("true");
    }

    // Collapsed, More options cannot show its own message: the foot does.
    expect(q('[data-testid="nt-submit-blocker"]')).toBeNull();
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    expect(q('[data-testid="nt-priority"]')).toBeNull();
    expect(q('[data-testid="nt-submit-blocker"]')!.textContent).toContain(
      "Fix the task priority",
    );
    q<HTMLButtonElement>('[data-testid="nt-submit-blocker-show"]')!.click();
    await settle(f);
    expect(document.activeElement).toBe(q('[data-testid="nt-priority"]'));
    expect(q('[data-testid="nt-submit-blocker"]')).toBeNull();

    type('[data-testid="nt-priority"]', "1e3");
    type('[data-testid="nt-owner"]', "alice@example.com");
    const keep = q<HTMLInputElement>('[data-testid="nt-keep-running"]')!;
    keep.checked = true;
    keep.dispatchEvent(new Event("change"));
    await pickPill(f, "nt-admission", "bypass");
    await settle(f);
    expect(q('[data-testid="nt-admission-help"]')!.textContent).toContain(
      "Admitted past the worker and merger limits",
    );
    expect(submit.disabled).toBe(false);
    submit.click();
    await settle(f);

    const post = api.calls.find((c) => c.route === "POST /api/tasks");
    expect(post?.body).toMatchObject({
      title: "Priority parity",
      priority: 1000,
      owner: "alice@example.com",
      keepRunning: true,
      admission: "bypass",
    });
  });

  it("holds Create while Base branch is a malformed fork head, and explains a well-formed one", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Fork head");
    const submit = q<HTMLButtonElement>('[data-testid="new-task-submit"]')!;

    for (const bad of ["  :nope", "bad:label:here"]) {
      type('[data-testid="nt-base"]', bad);
      await settle(f);
      expect(submit.disabled).toBe(true);
      expect(q('[data-testid="nt-base-error"]')!.textContent).toContain(
        "fork head",
      );
      expect(q('[data-testid="nt-base"]')!.getAttribute("aria-invalid")).toBe(
        "true",
      );
    }
    expect(q('[data-testid="nt-base-error"]')!.textContent).toBe(
      `"bad:label:here" has more than one ':': a fork head is <owner>:<branch>`,
    );

    type('[data-testid="nt-base"]', "octo:fix-login");
    await settle(f);
    expect(q('[data-testid="nt-base-error"]')).toBeNull();
    expect(q('[data-testid="nt-base-help"]')!.textContent).toBe(
      "GitHub fork head: branch fix-login on octo's fork. grain files this task against the open pull request on sidthe/grain it heads.",
    );
    expect(submit.disabled).toBe(false);

    // Cutting a new branch from it is not a fork head at all.
    type('[data-testid="nt-base"]', "bad:label:here");
    const nb = q<HTMLInputElement>('[data-testid="nt-new-branch"]')!;
    nb.checked = true;
    nb.dispatchEvent(new Event("change"));
    await settle(f);
    expect(q('[data-testid="nt-base-error"]')).toBeNull();
  });

  it("keeps a template's priority on Task priority until it is edited", async () => {
    const api = new FakeApi();
    api.templates = [{ ...api.templates[0], priority: 8 }];
    const f = await render(api);
    await open(f);
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    await pickPill(f, "nt-template", "declared:template:sidthe:grain:bugfix");
    await settle(f);
    const box = () => q<HTMLInputElement>('[data-testid="nt-priority"]')!;
    expect(box().value).toBe("8");
    expect(q('[data-testid="nt-priority-help"]')!.textContent).toBe(
      "default 8, from the template; higher numbers run first",
    );
    await pickPill(f, "nt-template", "");
    await settle(f);
    expect(box().value).toBe("5");

    type('[data-testid="nt-priority"]', "3");
    await pickPill(f, "nt-template", "declared:template:sidthe:grain:bugfix");
    await settle(f);
    expect(box().value).toBe("3");

    // A priority other than the template's own cannot go through
    // from-template, which takes none: it files through POST /api/tasks.
    type('[data-testid="nt-tfield-area"]', "auth");
    await settle(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/from-template"),
    ).toBeUndefined();
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks")?.body,
    ).toMatchObject({
      templateId: "declared:template:sidthe:grain:bugfix",
      priority: 3,
    });
  });

  it("starts Admission on a durable default and files Standard when it is picked", async () => {
    const api = new FakeApi();
    api.cfg = { ...api.cfg, defaultAdmission: "durable" };
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Standard on purpose");
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    expect(q('[data-testid="nt-admission"]')!.textContent).toContain("Durable");
    await pickPill(f, "nt-admission", "standard");
    await settle(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks")?.body,
    ).toMatchObject({ admission: "standard" });
  });

  it("leaves Admission out when settings/new-task-form.yml hides it", async () => {
    const api = new FakeApi();
    api.cfg = {
      ...api.cfg,
      newTaskForm: {
        default: { fields: { admission: { visibility: "hidden" } } },
      },
    };
    const f = await render(api);
    await open(f);
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    expect(q('[data-testid="nt-priority"]')).not.toBeNull();
    expect(q('[data-testid="nt-admission"]')).toBeNull();
  });

  it("keeps a template's priority and admission when the template picks its repo", async () => {
    const api = new FakeApi();
    api.templates = [
      {
        id: "stored-urgent",
        name: "Urgent",
        title: "Urgent",
        repo: "acme/web",
        priority: 9,
        admission: "bypass",
      } as Template,
    ];
    const f = await render(api);
    await open(f);
    q<HTMLButtonElement>('[data-testid="more-options"]')!.click();
    await settle(f);
    await pickPill(f, "nt-template", "stored-urgent");
    await settle(f);
    expect(q<HTMLInputElement>('[data-testid="nt-priority"]')!.value).toBe("9");
    expect(q('[data-testid="nt-admission"]')!.textContent).toContain(
      "Bypass queue",
    );
  });
});
