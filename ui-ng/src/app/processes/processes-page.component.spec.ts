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
import { SnackbarService } from "../services/snackbar.service";
import { ProcessesPageComponent } from "./processes-page.component";
import type {
  Composition,
  DeclarationAcceptanceView,
  RepoDefaultsView,
  RepoWorkflowsResponse,
  Schedule,
  ScheduleRun,
  Suite,
  SuiteRun,
  Template,
  TemplateField,
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
    {
      id: "402",
      title: "Security pass",
      state: "running",
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
      base: "",
      autoMerge: true,
      capabilities: [],
      recurrence: {
        kind: "github_workflow_run",
        githubWorkflowRun: {
          workflow: "Nightly E2E",
          conclusions: ["failure"],
          branches: ["release"],
        },
      },
      timeZone: "UTC",
      overlap: "skip",
      source: "deployment",
      type: "task",
      enabled: true,
      nextRunAt: "2026-11-13T09:00:00Z",
      createdAt: "2026-11-01T00:00:00Z",
      revision: "rev-s1",
    },
    {
      id: "sched-gitref",
      title: "Sync upstream tag",
      description: "Rebase onto latest upstream tag.",
      repo: "acme/widgets",
      base: "main",
      autoMerge: false,
      capabilities: [],
      recurrence: {
        kind: "git_ref",
        gitRef: {
          repo: "upstream/core",
          ref: "refs/tags/latest",
        },
      },
      overlap: "skip",
      source: "deployment",
      type: "task",
      enabled: true,
      nextRunAt: "2026-11-13T10:00:00Z",
      createdAt: "2026-11-01T00:00:00Z",
      revision: "rev-s2",
    },
    {
      id: "sched-sys",
      title: "Memory consolidation",
      description: "System memory job.",
      repo: "",
      autoMerge: false,
      templateId: "system:memory",
      system: true,
      capabilities: [],
      recurrence: { kind: "daily", timeOfDay: "03:00", timeZone: "UTC" },
      timeZone: "UTC",
      overlap: "skip",
      source: "system",
      type: "system",
      enabled: true,
      nextRunAt: "2026-11-13T03:00:00Z",
      createdAt: "2026-11-01T00:00:00Z",
      revision: "rev-sys",
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
      title: "chore: bump {{pkg}}",
      description: "Bump {{pkg}} version in go.mod.",
      fields: [
        {
          name: "pkg",
          label: "Go package",
          default: "golang.org/x/net",
          options: ["golang.org/x/net", "golang.org/x/sys"],
          required: false,
        },
      ],
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
      requireApproval: true,
      autoMerge: false,
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
      requireApproval: true,
      autoMerge: false,
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
        {
          taskId: "402",
          templateId: "tmpl-deploy-1",
          templateName: "Go upgrade",
          pass: 1,
          state: "running",
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

  scheduleRunResult: ScheduleRun = {
    taskId: "601",
    schedule: this.schedules[0],
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
        id: "sched-new",
        title: String(body?.["title"] || "New sched"),
      };
      this.schedules.push(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/schedules/{id}/run") {
      return Promise.resolve(structuredClone(this.scheduleRunResult));
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
        requireApproval: Boolean(body?.["requireApproval"]),
        autoMerge: body?.["autoMerge"] !== false,
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
      this.acceptances[0].acceptedBy = "alice@example.com";
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
      const id = String(params?.["id"] ?? "");
      const found =
        this.schedules.find((s) => s.id === id) ?? this.schedules[0];
      if (typeof body?.["enabled"] === "boolean") {
        found.enabled = body["enabled"];
      }
      return Promise.resolve(structuredClone(found));
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
      this.repoWfs.workflows =
        (body?.["workflows"] as RepoWorkflowsResponse["workflows"]) ?? [];
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

  it("manages Schedules: outside-[inert] dialog, github_workflow_run conclusions/branches, empty base, git_ref.repo, system-schedule guard, and workflowRun Run now notice", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/schedules?new=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    expect(root.querySelectorAll("select").length).toBe(0);

    // ?new=1 opens New Schedule dialog outside [inert]
    const dialog = root.querySelector('[data-testid="schedule-dialog"]')!;
    expect(dialog).not.toBeNull();
    expect(dialog.closest("[inert]")).toBeNull();
    expect(root.querySelector(".view-panel")?.hasAttribute("inert")).toBe(true);

    const titleIn = root.querySelector<HTMLInputElement>(
      '[data-testid="sched-title-input"]',
    )!;
    titleIn.value = "Nightly security scan";
    titleIn.dispatchEvent(new Event("input"));
    await flush(harness);

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="sched-preview-composition-btn"]',
      )!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/schedules/composition"),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "POST /api/schedules")).toBe(true);

    // 1. Edit github_workflow_run schedule (sched-1): preserves base: "", conclusions: ["failure"], branches: ["release"], locks Suite target
    const sched1Row = root.querySelector<HTMLElement>(
      '[data-testid="sched-row-sched-1"]',
    )!;
    expect(sched1Row.textContent).toContain(
      "On GitHub workflow: Nightly E2E (failure · on release)",
    );
    sched1Row.click();
    await flush(harness);

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-edit-btn"]')!
      .click();
    await flush(harness);

    expect(
      root.querySelector<HTMLInputElement>('[data-testid="sched-base-input"]')!
        .value,
    ).toBe("");
    expect(
      root.querySelector<HTMLButtonElement>(
        '[data-testid="sched-target-suite"]',
      )!.disabled,
    ).toBe(true);
    expect(
      root.querySelector<HTMLInputElement>(
        '[data-testid="sched-trigger-conclusion-input"]',
      )!.value,
    ).toBe("failure");
    expect(
      root.querySelector<HTMLInputElement>(
        '[data-testid="sched-trigger-branch-input"]',
      )!.value,
    ).toBe("release");

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);

    const ghaPatch = api.calls.find(
      (c) =>
        c.route === "PATCH /api/schedules/{id}" &&
        (c.params as Record<string, unknown>)?.["id"] === "sched-1",
    )!;
    const ghaBody = ghaPatch.body as Record<string, unknown>;
    expect(ghaBody["base"]).toBe("");
    expect(ghaBody["capabilities"]).toBeUndefined();
    expect(ghaBody["recurrence"]).toEqual({
      kind: "github_workflow_run",
      githubWorkflowRun: {
        workflow: "Nightly E2E",
        conclusions: ["failure"],
        branches: ["release"],
      },
    });

    // 2. Edit git_ref schedule (sched-gitref): preserves gitRef.repo = "upstream/core"
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-gitref"]')!
      .dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-edit-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);
    const gitRefPatch = api.calls.find(
      (c) =>
        c.route === "PATCH /api/schedules/{id}" &&
        (c.params as Record<string, unknown>)?.["id"] === "sched-gitref",
    )!;
    expect(
      (gitRefPatch.body as { recurrence: { gitRef: { repo: string } } })
        .recurrence.gitRef.repo,
    ).toBe("upstream/core");

    // 3. Edit system schedule (sched-sys): hides content inputs & sends only cadence/overlap/revision
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-sys"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-edit-btn"]')!
      .click();
    await flush(harness);
    expect(
      root.querySelector('[data-testid="sched-system-banner"]'),
    ).not.toBeNull();
    expect(root.querySelector('[data-testid="sched-title-input"]')).toBeNull();
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);
    const sysPatch = api.calls.find(
      (c) =>
        c.route === "PATCH /api/schedules/{id}" &&
        (c.params as Record<string, unknown>)?.["id"] === "sched-sys",
    )!;
    expect(Object.keys(sysPatch.body as object).sort()).toEqual([
      "overlap",
      "recurrence",
      "revision",
      "timeZone",
    ]);

    // 4. Run now with workflowRun: true and no taskId/suiteRunId shows honest trigger notice
    const snackbar = TestBed.inject(SnackbarService);
    api.scheduleRunResult = {
      workflowRun: true,
      schedule: api.schedules[0],
    };
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-run-now-btn"]')!
      .click();
    await flush(harness);
    expect(snackbar.current()?.message).toBe(
      "No concluded GitHub workflow run matched this trigger yet.",
    );
  });

  it("preserves TemplateField metadata (label, default, options, required) across PATCH and launches via POST /api/tasks/from-template", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/templates?launch=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    const launchDialog = root.querySelector(
      '[data-testid="launch-template-dialog"]',
    )!;
    expect(launchDialog).not.toBeNull();
    expect(launchDialog.closest("[inert]")).toBeNull();

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

    // Navigate back to templates tab and edit deployment template tmpl-deploy-1
    await harness.navigateByUrl("/processes/templates");
    await flush(harness);
    const tplRoot = harness.routeNativeElement!;

    tplRoot
      .querySelector<HTMLElement>('[data-testid="tmpl-row-tmpl-deploy-1"]')!
      .click();
    await flush(harness);
    tplRoot
      .querySelector<HTMLButtonElement>('[data-testid="tmpl-edit-btn"]')!
      .click();
    await flush(harness);

    const tmplDlg = tplRoot.querySelector('[data-testid="template-dialog"]')!;
    expect(tmplDlg.closest("[inert]")).toBeNull();

    // Append a second parameter while keeping existing "pkg" metadata intact
    const paramsInput = tplRoot.querySelector<HTMLInputElement>(
      '[data-testid="tmpl-params-input"]',
    )!;
    expect(paramsInput.value).toBe("pkg");
    paramsInput.value = "pkg, target_go";
    paramsInput.dispatchEvent(new Event("input"));
    await flush(harness);

    tplRoot
      .querySelector<HTMLButtonElement>('[data-testid="tmpl-save-btn"]')!
      .click();
    await flush(harness);

    const tmplPatch = api.calls.find(
      (c) => c.route === "PATCH /api/templates/{id}",
    )!;
    const savedFields = (tmplPatch.body as { fields: TemplateField[] }).fields;
    expect(savedFields).toEqual([
      {
        name: "pkg",
        label: "Go package",
        default: "golang.org/x/net",
        options: ["golang.org/x/net", "golang.org/x/sys"],
        required: false,
      },
      {
        name: "target_go",
        required: true,
      },
    ]);
  });

  it("preserves Suite requireApproval/autoMerge on PATCH, launches Suite Runs via ?run=1, and closes all non-closed pass tasks (including running)", async () => {
    const harness = await RouterTestingHarness.create("/processes/suites");
    await flush(harness);
    const root = harness.routeNativeElement!;

    // Edit existing suite-1 (requireApproval: true, autoMerge: false) and verify PATCH preserves both!
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-edit-btn"]')!
      .click();
    await flush(harness);
    const suiteDialog = root.querySelector('[data-testid="suite-dialog"]')!;
    expect(suiteDialog.closest("[inert]")).toBeNull();
    expect(
      root
        .querySelector('[data-testid="suite-require-approval-switch"]')
        ?.getAttribute("aria-checked"),
    ).toBe("true");
    expect(
      root
        .querySelector('[data-testid="suite-auto-merge-switch"]')
        ?.getAttribute("aria-checked"),
    ).toBe("false");

    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    const suitePatch = api.calls.find(
      (c) => c.route === "PATCH /api/suites/{id}",
    )!;
    expect(suitePatch.body).toMatchObject({
      requireApproval: true,
      autoMerge: false,
      revision: "rev-st1",
    });

    // Launch Suite Run via Run suite dialog
    root
      .querySelector<HTMLButtonElement>('[data-testid="proc-run-suite-btn"]')!
      .click();
    await flush(harness);
    expect(
      root
        .querySelector('[data-testid="run-suite-dialog"]')
        ?.closest("[inert]"),
    ).toBeNull();
    root
      .querySelector<HTMLButtonElement>('[data-testid="run-suite-submit"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "POST /api/suite-runs")).toBe(
      true,
    );

    // Cancel active suite run tasks (401 queued + 402 running) -> BOTH must call POST /api/tasks/{id}/close, NEVER /stop!
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-run-cancel-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await flush(harness);

    const closeCalls = api.calls.filter(
      (c) => c.route === "POST /api/tasks/{id}/close",
    );
    expect(
      closeCalls.map((c) => (c.params as { id: string }).id).sort(),
    ).toEqual(["401", "402"]);
    expect(api.calls.some((c) => c.route === "POST /api/tasks/{id}/stop")).toBe(
      false,
    );
  });

  it("enforces global scope on Accept declaration and supports saving & deleting Repo workflow overrides", async () => {
    // 1. Repo-limited repos:write cannot accept declaration requiring global repos:write
    api.configBody = {
      access: {
        role: "grain/repo-dev",
        roles: ["grain/repo-dev"],
        full: false,
        scopes: ["capabilities:read", "repos:write", "settings:write"],
        repoLimits: {
          "repos:write": ["acme/widgets"],
        },
      },
      targetRepos: ["acme/widgets"],
      approvedByDefault: true,
    };

    const harness = await RouterTestingHarness.create(
      "/processes/declarations",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    const acceptBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="decl-accept-btn-repo:template:acme/widgets:release-notes"]',
    )!;
    expect(acceptBtn.disabled).toBe(true);

    // 2. Grant global repos:write and reload -> Accept declaration succeeds, plus edit & delete workflow override
    api.configBody = {
      access: {
        role: "grain/operator",
        roles: ["grain/operator"],
        full: true,
        scopes: [],
      },
      targetRepos: ["acme/widgets"],
      approvedByDefault: true,
    };
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      providers: [
        provideRouter(
          [{ path: "processes/:tab", component: ProcessesPageComponent }],
          withComponentInputBinding(),
        ),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const harness2 = await RouterTestingHarness.create(
      "/processes/declarations",
    );
    await flush(harness2);
    const root2 = harness2.routeNativeElement!;

    root2
      .querySelector<HTMLButtonElement>(
        '[data-testid="decl-accept-btn-repo:template:acme/widgets:release-notes"]',
      )!
      .click();
    await flush(harness2);
    expect(
      api.calls.some((c) => c.route === "POST /api/declarations/accept"),
    ).toBe(true);

    // Inspect existing workflow override and delete it via Delete override button
    root2
      .querySelector<HTMLButtonElement>(
        '[data-testid="decl-inspect-wf-.grain/workflows/ci.yml"]',
      )!
      .click();
    await flush(harness2);
    const wfDlg = root2.querySelector('[data-testid="repo-workflow-dialog"]')!;
    expect(wfDlg.closest("[inert]")).toBeNull();

    root2
      .querySelector<HTMLButtonElement>('[data-testid="wf-delete-btn"]')!
      .click();
    await flush(harness2);
    const putCall = api.calls.find(
      (c) => c.route === "PUT /api/repos/{owner}/{name}/workflows",
    )!;
    expect(putCall.body).toEqual({ workflows: [] });
  });

  it("never floods Declarations with GitHub-wide reachable-repos when targetRepos is configured", async () => {
    api.configBody = {
      access: {
        role: "grain/operator",
        roles: ["grain/operator"],
        full: true,
        scopes: [],
      },
      targetRepos: ["acme/widgets"],
      reposWithWorkflows: ["acme/external-ci"],
      approvedByDefault: true,
    };
    const baseGet = api.get;
    api.get = (route: string, params?: Record<string, unknown>) => {
      if (route === "GET /api/deployment/reachable-repos") {
        api.calls.push({ method: "GET", route, params });
        return Promise.resolve({
          available: true,
          repos: Array.from({ length: 200 }, (_, i) => ({
            repo: `github-org/unrelated-${i}`,
            push: true,
          })),
        });
      }
      return baseGet(route, params);
    };

    const harness = await RouterTestingHarness.create(
      "/processes/declarations",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    // Never calls reachable-repos on Processes; only lists instance-configured repos.
    expect(
      api.calls.some((c) => c.route === "GET /api/deployment/reachable-repos"),
    ).toBe(false);
    expect(
      root.querySelector('[data-testid="decl-repo-row-acme/widgets"]'),
    ).not.toBeNull();
    expect(
      root.querySelector('[data-testid="decl-repo-row-acme/external-ci"]'),
    ).not.toBeNull();
    expect(
      root.querySelector(
        '[data-testid="decl-repo-row-github-org/unrelated-0"]',
      ),
    ).toBeNull();
    expect(
      root.querySelectorAll('[data-testid^="decl-repo-row-"]').length,
    ).toBe(2);
  });
});
