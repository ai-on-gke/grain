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

import { TestBed } from "@angular/core/testing";
import { provideRouter, withComponentInputBinding } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { GrainConfig, Task } from "../api/models";
import { ProcessesPageComponent } from "./processes-page.component";
import type {
  Composition,
  DeclarationAcceptanceView,
  RepoDefaultsView,
  RepoWorkflowsResponse,
  Schedule,
  Suite,
  SuiteRun,
  Template,
} from "./processes.models";

class FakeProcessesApi {
  configBody: GrainConfig = {
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
    targetRepos: ["acme/widgets"],
    approvedByDefault: true,
  };

  tasks: Task[] = [
    {
      id: "301",
      title: "Audit deps",
      state: "running",
      repo: "acme/widgets",
      scheduled: true,
    },
    {
      id: "401",
      title: "Lint pass",
      state: "queued",
      repo: "acme/widgets",
      suiteRun: true,
    },
  ];

  schedules: Schedule[] = [
    {
      id: "sched-1",
      title: "Audit deps",
      description: "Check outdated dependencies.",
      repo: "acme/widgets",
      base: "main",
      autoMerge: true,
      capabilities: [],
      recurrence: { kind: "daily", timeOfDay: "09:00", timeZone: "UTC" },
      timeZone: "UTC",
      overlap: "skip",
      source: "deployment",
      type: "task",
      enabled: true,
      nextRunAt: "2026-11-13T09:00:00Z",
      createdAt: "2026-11-01T00:00:00Z",
      revision: "rev-s1",
    },
  ];

  templates: Template[] = [
    {
      id: "repo:template:acme/widgets:release-notes",
      name: "release-notes",
      title: "Draft release notes for {{version}}",
      description: "Summarize commits since last tag for {{version}}.",
      fields: [
        {
          name: "version",
          label: "Version",
          default: "v1.2.0",
          required: true,
        },
      ],
      autoMerge: false,
      capabilities: [],
      declaredIn: {
        repo: "acme/widgets",
        path: ".grain/templates/release-notes.yml",
      },
    },
    {
      id: "tmpl-deploy-1",
      name: "Go upgrade",
      title: "chore: bump Go toolchain",
      description: "Bump Go version in go.mod.",
      autoMerge: true,
      capabilities: [],
      revision: "rev-t1",
    },
  ];

  suites: Suite[] = [
    {
      id: "suite-1",
      name: "Hardening sweep",
      items: [{ templateId: "tmpl-deploy-1", templateName: "Go upgrade" }],
      mode: "until_clean",
      maxPasses: 3,
      requireApproval: false,
      autoMerge: true,
      autoApproveProposals: false,
      revision: "rev-st1",
    },
  ];

  suiteRuns: SuiteRun[] = [
    {
      id: 7,
      suiteId: "suite-1",
      suiteName: "Hardening sweep",
      repo: "acme/widgets",
      base: "main",
      mode: "until_clean",
      maxPasses: 3,
      requireApproval: false,
      autoMerge: true,
      status: "active",
      pass: 1,
      createdAt: "2026-11-12T00:00:00Z",
      tasks: [
        {
          taskId: "401",
          templateId: "tmpl-deploy-1",
          templateName: "Go upgrade",
          pass: 1,
          state: "queued",
          approved: true,
          openedPullRequest: false,
          proposed: false,
        },
      ],
    },
  ];

  acceptances: DeclarationAcceptanceView[] = [
    {
      id: "repo:template:acme/widgets:release-notes",
      kind: "template",
      repo: "acme/widgets",
      requested: ["push:refs/tags/v*"],
      requestedAt: "2026-11-12T00:00:00Z",
      awaiting: true,
      unaccepted: ["push:refs/tags/v*"],
      needsScopes: ["repos:write"],
    },
  ];

  repoDecl: RepoDefaultsView = {
    repo: "acme/widgets",
    declarations: true,
    declarationsStatus: {
      commit: "abcdef1234567890",
      checkedAt: "2026-11-12T00:05:00Z",
      templates: [".grain/templates/release-notes.yml"],
      triggers: [".grain/triggers/docs.yml"],
      mergeRules: ".grain/merge-rules.yml",
    },
  };

  repoWfs: RepoWorkflowsResponse = {
    repo: "acme/widgets",
    dir: ".grain/workflows",
    workflows: [
      {
        path: ".grain/workflows/ci.yml",
        name: "CI",
        text: "name: CI\non: [push]\n",
      },
    ],
  };

  compositionSample: Composition = {
    kind: "task",
    repo: "acme/widgets",
    base: "main",
    tasks: [
      {
        title: "Audit deps",
        fields: [
          { name: "repo", values: ["acme/widgets"], source: "schedule" },
          { name: "autoMerge", values: ["on"], source: "schedule" },
        ],
      },
    ],
  };

  calls: { method: string; route: string; params?: unknown; body?: unknown }[] =
    [];

  config = () => Promise.resolve(this.configBody);
  tasks_ = () => Promise.resolve(this.tasks);
  clearConditionalCache = () => undefined;

  getIfChanged = (route: string) => {
    switch (route) {
      case "GET /api/tasks":
        return Promise.resolve({
          body: structuredClone(this.tasks),
          changed: true,
        });
      case "GET /api/schedules":
        return Promise.resolve({
          body: structuredClone(this.schedules),
          changed: true,
        });
      case "GET /api/templates":
        return Promise.resolve({
          body: structuredClone(this.templates),
          changed: true,
        });
      case "GET /api/suites":
        return Promise.resolve({
          body: structuredClone(this.suites),
          changed: true,
        });
      case "GET /api/suite-runs":
        return Promise.resolve({
          body: structuredClone(this.suiteRuns),
          changed: true,
        });
      case "GET /api/declarations":
        return Promise.resolve({
          body: structuredClone(this.acceptances),
          changed: true,
        });
      default:
        return Promise.resolve({ body: null, changed: false });
    }
  };

  get = (route: string, params?: Record<string, unknown>) => {
    this.calls.push({ method: "GET", route, params });
    if (route === "GET /api/deployment/reachable-repos") {
      return Promise.resolve(["acme/widgets"]);
    }
    if (
      route === "GET /api/schedules/{id}/composition" ||
      route === "GET /api/templates/{id}/composition" ||
      route === "GET /api/suites/{id}/composition"
    ) {
      return Promise.resolve(structuredClone(this.compositionSample));
    }
    if (route === "GET /api/suite-runs/{id}") {
      return Promise.resolve(structuredClone(this.suiteRuns[0]));
    }
    if (route === "GET /api/repos/{owner}/{name}/declarations") {
      return Promise.resolve(structuredClone(this.repoDecl));
    }
    if (route === "GET /api/repos/{owner}/{name}/workflows") {
      return Promise.resolve(structuredClone(this.repoWfs));
    }
    return Promise.resolve({});
  };

  post = (route: string, params?: unknown, body?: Record<string, unknown>) => {
    this.calls.push({ method: "POST", route, params, body });
    if (
      route === "POST /api/schedules/composition" ||
      route === "POST /api/suites/composition"
    ) {
      return Promise.resolve(structuredClone(this.compositionSample));
    }
    if (route === "POST /api/schedules") {
      const created: Schedule = {
        ...this.schedules[0],
        id: "sched-2",
        title: String(body?.["title"] || "New sched"),
      };
      this.schedules.push(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/schedules/{id}/run") {
      return Promise.resolve({ taskId: "601", schedule: this.schedules[0] });
    }
    if (route === "POST /api/templates") {
      const created: Template = {
        id: "tmpl-new",
        name: String(body?.["name"] ?? ""),
        title: String(body?.["title"] ?? ""),
        description: String(body?.["description"] ?? ""),
        autoMerge: false,
        capabilities: [],
      };
      this.templates.push(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/tasks/from-template") {
      return Promise.resolve({
        id: "701",
        title: "Draft release notes for v1.2.0",
        state: "queued",
      });
    }
    if (route === "POST /api/suites") {
      const created: Suite = {
        id: "suite-2",
        name: String(body?.["name"] ?? ""),
        items: [{ templateId: "tmpl-deploy-1", templateName: "Go upgrade" }],
        mode: String(body?.["mode"] ?? "count"),
        count: Number(body?.["count"] ?? 1),
        requireApproval: false,
        autoMerge: true,
        autoApproveProposals: false,
      };
      this.suites.push(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/suite-runs") {
      const created: SuiteRun = {
        ...this.suiteRuns[0],
        id: 8,
        repo: String(body?.["repo"] ?? "acme/widgets"),
        base: String(body?.["base"] ?? "main"),
      };
      this.suiteRuns.unshift(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/declarations/accept") {
      this.acceptances[0].awaiting = false;
      this.acceptances[0].acceptedBy = "rarcea@google.com";
      return Promise.resolve(structuredClone(this.acceptances[0]));
    }
    return Promise.resolve({});
  };

  patch = (
    route: string,
    params?: Record<string, unknown>,
    body?: Record<string, unknown>,
  ) => {
    this.calls.push({ method: "PATCH", route, params, body });
    if (route === "PATCH /api/schedules/{id}") {
      if (typeof body?.["enabled"] === "boolean") {
        this.schedules[0].enabled = body["enabled"];
      }
      return Promise.resolve(structuredClone(this.schedules[0]));
    }
    if (route === "PATCH /api/templates/{id}") {
      return Promise.resolve(structuredClone(this.templates[1]));
    }
    if (route === "PATCH /api/suites/{id}") {
      return Promise.resolve(structuredClone(this.suites[0]));
    }
    return Promise.resolve({});
  };

  put = (route: string, params?: unknown, body?: Record<string, unknown>) => {
    this.calls.push({ method: "PUT", route, params, body });
    if (route === "PUT /api/repos/{owner}/{name}/declarations") {
      this.repoDecl.declarations = !!body?.["declarations"];
      return Promise.resolve(structuredClone(this.repoDecl));
    }
    if (route === "PUT /api/repos/{owner}/{name}/workflows") {
      return Promise.resolve(structuredClone(this.repoWfs));
    }
    return Promise.resolve({});
  };

  delete = (route: string, params?: Record<string, unknown>) => {
    this.calls.push({ method: "DELETE", route, params });
    if (route === "DELETE /api/schedules/{id}") {
      this.schedules = this.schedules.filter((s) => s.id !== params?.["id"]);
    }
    return Promise.resolve({});
  };
}

async function flush(harness: RouterTestingHarness): Promise<void> {
  harness.detectChanges();
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  harness.detectChanges();
  await new Promise((r) => setTimeout(r, 10));
  harness.detectChanges();
  await harness.fixture.whenStable();
}

describe("ProcessesPageComponent (D-9)", () => {
  let api: FakeProcessesApi;

  beforeEach(() => {
    localStorage.clear();
    Object.defineProperty(window, "innerWidth", {
      value: 1440,
      configurable: true,
      writable: true,
    });
    api = new FakeProcessesApi();
    TestBed.configureTestingModule({
      providers: [
        provideRouter(
          [
            { path: "processes", component: ProcessesPageComponent },
            { path: "processes/:tab", component: ProcessesPageComponent },
            { path: "task/:id", component: ProcessesPageComponent },
          ],
          withComponentInputBinding(),
        ),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
  });

  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("manages Schedules: composition preview, Run now, Pause/Resume, and ?new=1 creation with draft preview", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/schedules?new=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    expect(root.querySelectorAll("select").length).toBe(0);

    // ?new=1 opens New Schedule dialog automatically
    const dialog = root.querySelector('[data-testid="schedule-dialog"]')!;
    expect(dialog).not.toBeNull();

    const titleIn = root.querySelector<HTMLInputElement>(
      '[data-testid="sched-title-input"]',
    )!;
    titleIn.value = "Nightly security scan";
    titleIn.dispatchEvent(new Event("input"));

    // Preview draft composition (POST /api/schedules/composition)
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="sched-preview-composition-btn"]',
      )!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/schedules/composition"),
    ).toBe(true);

    // Save schedule (POST /api/schedules)
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "POST /api/schedules")).toBe(true);

    // Select sched-1, pause it, and trigger Run now
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-1"]')!
      .click();
    await flush(harness);
    expect(
      root.querySelector('[data-testid="composition-preview"]'),
    ).not.toBeNull();

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="sched-toggle-enabled-btn"]',
      )!
      .click();
    await flush(harness);
    expect(
      api.calls.some(
        (c) =>
          c.route === "PATCH /api/schedules/{id}" &&
          (c.body as Record<string, unknown>)?.["enabled"] === false,
      ),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-run-now-btn"]')!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/schedules/{id}/run"),
    ).toBe(true);
  });

  it("browses repo-declared vs deployment Templates, creates templates, and launches via POST /api/tasks/from-template", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/templates?launch=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    // ?launch=1 opens Launch from template dialog pre-populated with repo-declared template
    const launchDialog = root.querySelector(
      '[data-testid="launch-template-dialog"]',
    )!;
    expect(launchDialog).not.toBeNull();
    const versionInput = root.querySelector<HTMLInputElement>(
      '[data-testid="launch-field-version"]',
    )!;
    expect(versionInput.value).toBe("v1.2.0");

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="launch-template-submit"]',
      )!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/tasks/from-template"),
    ).toBe(true);

    // Navigate back to templates tab to create a new deployment template
    await harness.navigateByUrl("/processes/templates");
    await flush(harness);
    expect(
      harness.routeNativeElement!.querySelector(
        '[data-testid="tmpl-declared-banner"]',
      ),
    ).not.toBeNull();

    harness
      .routeNativeElement!.querySelector<HTMLButtonElement>(
        '[data-testid="proc-new-template-btn"]',
      )!
      .click();
    await flush(harness);
    const nIn = harness.routeNativeElement!.querySelector<HTMLInputElement>(
      '[data-testid="tmpl-name-input"]',
    )!;
    nIn.value = "Lint autofix";
    nIn.dispatchEvent(new Event("input"));
    const tIn = harness.routeNativeElement!.querySelector<HTMLInputElement>(
      '[data-testid="tmpl-title-input"]',
    )!;
    tIn.value = "chore: run lint autofix";
    tIn.dispatchEvent(new Event("input"));
    await flush(harness);

    harness
      .routeNativeElement!.querySelector<HTMLButtonElement>(
        '[data-testid="tmpl-save-btn"]',
      )!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "POST /api/templates")).toBe(true);
  });

  it("creates Suites, launches Suite Runs via ?run=1, inspects live pass tasks, and cancels active suite runs", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/suites?run=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    // ?run=1 opens Run Suite dialog
    expect(
      root.querySelector('[data-testid="run-suite-dialog"]'),
    ).not.toBeNull();
    root
      .querySelector<HTMLButtonElement>('[data-testid="run-suite-submit"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "POST /api/suite-runs")).toBe(
      true,
    );

    // Automatically switches to live Suite Run inspector showing Pass 1 and child task #401 link
    expect(
      root.querySelector('[data-testid="suite-run-inspector"]'),
    ).not.toBeNull();
    expect(
      root
        .querySelector('[data-testid="suite-task-link-401"]')
        ?.getAttribute("href"),
    ).toBe("/task/401");

    // Cancel active suite run tasks via confirm dialog
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-run-cancel-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/tasks/{id}/close"),
    ).toBe(true);
  });

  it("inspects Repo .grain/ declarations, accepts widened capabilities, and saves workflow YAML", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/declarations",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    expect(
      root.querySelector('[data-testid="decl-discovered-files"]')?.textContent,
    ).toContain(".grain/templates/release-notes.yml");
    expect(
      root.querySelector('[data-testid="decl-discovered-files"]')?.textContent,
    ).toContain(".grain/merge-rules.yml");

    // Accept declaration (POST /api/declarations/accept)
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="decl-accept-btn-repo:template:acme/widgets:release-notes"]',
      )!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/declarations/accept"),
    ).toBe(true);

    // Add / save repo workflow YAML (PUT /api/repos/{owner}/{name}/workflows)
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="decl-add-workflow-btn"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="wf-save-btn"]')!
      .click();
    await flush(harness);
    expect(
      api.calls.some(
        (c) => c.route === "PUT /api/repos/{owner}/{name}/workflows",
      ),
    ).toBe(true);
  });
});
