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
import { formatStamp } from "../shared/dispatch-status";
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
      description: "Check **outdated** dependencies.\n\n- `go.mod`",
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
      description: "Bump `{{pkg}}` in **go.mod**.",
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
          { name: "title", values: ["Audit deps"], source: "schedule" },
          { name: "repo", values: ["acme/widgets"], source: "schedule" },
          { name: "autoMerge", values: ["on"], source: "schedule" },
          {
            name: "body",
            values: ["Check outdated dependencies.\nOpen a PR when needed."],
            source: "schedule",
          },
        ],
      },
    ],
  };

  yamlFiles: Record<string, string> = {
    "acme/widgets/.grain/templates/release-notes.yml":
      "name: release-notes\ntitle: Draft release notes for {{version}}\n",
    "acme/widgets/.grain/workflows/ci.yml": "name: CI\non: [push]\n",
  };

  scheduleRunResult: ScheduleRun = {
    taskId: "601",
    schedule: this.schedules[0],
  };

  calls: {
    method: string;
    route: string;
    params?: unknown;
    body?: unknown;
    query?: string;
  }[] = [];

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

  get = (route: string, params?: Record<string, unknown>, query?: string) => {
    this.calls.push({ method: "GET", route, params, query });
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
    if (route === "GET /api/qa/flows") {
      return Promise.resolve({
        flows: [
          {
            id: "golden-v1-minimal",
            label: "Golden V1 Minimal",
            summary:
              "Staging-branch isolation + <400ms Go AST/YAML S3 checks + T2/T3 S4 verifier.",
            injectSkill: true,
            skillBytes: 1120,
            stagingIsolation: true,
            enableS4: true,
            maxS4RepairRounds: 1,
            rules: [
              {
                id: "go-test-assertion-floor",
                stage: "S3",
                label: "Go test assertion & integrity floor",
                summary:
                  "Rejects assertion-free tests and weakened existing assertions.",
                severity: "block",
                enabled: true,
                targetPaths: ["*_test.go"],
              },
              {
                id: "s4-inquisitor-verifier",
                stage: "S4",
                label: "Fresh-context S4 verifier + 6-filter Inquisitor",
                summary: "Runs one bounded fresh-context pass on T2/T3 diffs.",
                severity: "block",
                enabled: true,
                targetPaths: ["T2/T3 multi-package diffs"],
              },
            ],
          },
          {
            id: "deterministic-s3-only",
            label: "Deterministic S3 Only",
            summary: "Zero-token pure-Go S3 checks with S4 disabled.",
            injectSkill: true,
            skillBytes: 1120,
            stagingIsolation: true,
            enableS4: false,
            maxS4RepairRounds: 0,
            rules: [
              {
                id: "go-test-assertion-floor",
                stage: "S3",
                label: "Go test assertion & integrity floor",
                summary:
                  "Rejects assertion-free tests and weakened existing assertions.",
                severity: "block",
                enabled: true,
                targetPaths: ["*_test.go"],
              },
            ],
          },
        ],
      });
    }
    if (route === "GET /api/settings") {
      return Promise.resolve({
        qaEnabled: true,
        qaFlowId: "golden-v1-minimal",
        qaRolloutPct: 50,
        qaShadowOnControl: true,
      });
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
        qaFlowRef:
          typeof body?.["qaFlowRef"] === "string"
            ? body["qaFlowRef"]
            : undefined,
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
        qaFlowRef:
          typeof body?.["qaFlowRef"] === "string"
            ? body["qaFlowRef"]
            : undefined,
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
        qaFlowRef:
          typeof body?.["qaFlowRef"] === "string"
            ? body["qaFlowRef"]
            : undefined,
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
      if (typeof body?.["qaFlowRef"] === "string") {
        found.qaFlowRef = body["qaFlowRef"] || undefined;
      }
      return Promise.resolve(structuredClone(found));
    }
    if (route === "PATCH /api/templates/{id}") {
      const id = String(params?.["id"] ?? "");
      const found =
        this.templates.find((t) => t.id === id) ?? this.templates[1];
      if (typeof body?.["qaFlowRef"] === "string") {
        found.qaFlowRef = body["qaFlowRef"] || undefined;
      }
      return Promise.resolve(structuredClone(found));
    }
    if (route === "PATCH /api/suites/{id}") {
      const id = String(params?.["id"] ?? "");
      const found = this.suites.find((s) => s.id === id) ?? this.suites[0];
      if (typeof body?.["qaFlowRef"] === "string") {
        found.qaFlowRef = body["qaFlowRef"] || undefined;
      }
      return Promise.resolve(structuredClone(found));
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

  getText = (route: string, params?: Record<string, unknown>) => {
    this.calls.push({ method: "GET", route, params });
    const p = String(params?.["path"] ?? "");
    return Promise.resolve(this.yamlFiles[p] ?? `# YAML for ${p}\n`);
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

  it("renders schedule and template prompts as markdown in a Prompt pane apart from a Configuration pane", async () => {
    const harness = await RouterTestingHarness.create("/processes/schedules");
    await flush(harness);
    const root = harness.routeNativeElement!;
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-1"]')!
      .click();
    await flush(harness);

    const sPrompt = root.querySelector('[data-testid="sched-prompt-pane"]')!;
    expect(sPrompt.querySelector(".g-pane-title")!.textContent).toContain(
      "Prompt",
    );
    expect(sPrompt.querySelector("strong")!.textContent).toBe("outdated");
    expect(sPrompt.querySelector("li code")!.textContent).toBe("go.mod");
    const sConfig = root.querySelector('[data-testid="sched-config-pane"]')!;
    expect(sConfig.contains(sPrompt)).toBe(false);
    const sKeys = [...sConfig.querySelectorAll("dt")].map((d) =>
      d.textContent!.trim(),
    );
    expect(sKeys).toEqual(
      expect.arrayContaining(["Source", "Cadence", "Repository", "Overlap"]),
    );

    await harness.navigateByUrl("/processes/templates");
    await flush(harness);
    const tRoot = harness.routeNativeElement!;
    tRoot
      .querySelector<HTMLElement>('[data-testid="tmpl-row-tmpl-deploy-1"]')!
      .click();
    await flush(harness);
    const tPrompt = tRoot.querySelector('[data-testid="tmpl-prompt-preview"]')!;
    expect(tPrompt.querySelector("strong")!.textContent).toBe("go.mod");
    expect(tPrompt.querySelector("code")!.textContent).toBe("{{pkg}}");
    const tConfig = tRoot.querySelector('[data-testid="tmpl-config-pane"]')!;
    expect(tConfig.querySelector("dt")!.textContent).toContain("Source");
  });

  it("says a publishing-held suppression in words, shows the overlap group and each skip's outcome, and no Next for an event trigger", async () => {
    api.schedules.push({
      id: "sched-train",
      title: "Release train",
      description: "Cut an rc.",
      repo: "acme/widgets",
      base: "main",
      autoMerge: false,
      capabilities: [],
      recurrence: { kind: "every_n_minutes", everyNMinutes: 20 },
      overlap: "skip",
      overlapGroup: "release-train",
      source: "acme/widgets",
      type: "template",
      enabled: true,
      nextRunAt: "2026-11-15T09:20:00Z",
      createdAt: "2026-11-01T00:00:00Z",
      suppressed: {
        reason: "publishingHeld",
        taskId: "2",
        detail: "skipped: publishing held by task 2",
      },
      skips: [
        {
          occurrence: "2026-11-15T08:00:00Z",
          at: "2026-11-15T08:00:00Z",
          reason: "publishingHeld",
          summary: "skipped at 2026-11-15 08:00 UTC: publishing held by task 2",
          taskId: "2",
          outcome: "fired",
          firedTaskId: "5",
          folded: 3,
        },
        {
          occurrence: "2026-11-15T07:00:00Z",
          at: "2026-11-15T07:00:00Z",
          reason: "task",
          summary: "skipped at 2026-11-15 07:00 UTC: #1 is still open",
          taskId: "1",
          outcome: "dropped",
        },
      ],
      revision: "rev-train",
    });
    const harness = await RouterTestingHarness.create("/processes/schedules");
    await flush(harness);
    const root = harness.routeNativeElement!;

    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-train"]')!
      .click();
    await flush(harness);

    const text = root.textContent ?? "";
    expect(text).toContain(
      "Suppressed — publishing held: skipped: publishing held by task 2",
    );
    expect(text).not.toContain("publishingHeld");
    expect(
      root.querySelector('[data-testid="sched-overlap-group"]')?.textContent,
    ).toContain("release-train");
    // The Configuration pane's keys, each with its value.
    const config = () =>
      Object.fromEntries(
        Array.from(
          root.querySelectorAll('[data-testid="sched-config-pane"] dt'),
        ).map((dt) => [
          dt.textContent!.trim(),
          dt.nextElementSibling?.textContent?.trim(),
        ]),
      );
    expect(config()["Overlap group"]).toBe("release-train");
    expect(config()["Next run"]).toBe(formatStamp("2026-11-15T09:20:00Z"));

    const outcomes = Array.from(
      root.querySelectorAll('[data-testid="sched-skip-outcome"]'),
    ).map((el) => el.textContent?.replace(/\s+/g, " ").trim());
    expect(outcomes).toEqual([
      "→ fired #5 · 3 later occurrences folded in",
      "→ dropped",
    ]);
    const fired = root.querySelector<HTMLAnchorElement>(
      '[data-testid="sched-skip-outcome"] a',
    )!;
    expect(fired.getAttribute("href")).toBe("/task/5");

    // An event trigger's nextRunAt is a placeholder, not a time.
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-gitref"]')!
      .click();
    await flush(harness);
    expect(config()["Next run"]).toBe("on ref move");
    expect(root.textContent).not.toContain("2026-11-13T10:00:00Z");
  });

  it("preserves TemplateField metadata (label, default, options, required) across PATCH and launches via POST /api/tasks/from-template", async () => {
    const harness = await RouterTestingHarness.create("/processes/templates");
    await flush(harness);
    const root = harness.routeNativeElement!;
    root
      .querySelector<HTMLElement>(
        '[data-testid="tmpl-row-repo:template:acme/widgets:release-notes"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLElement>('[data-testid="proc-launch-template-btn"]')!
      .click();
    await flush(harness);

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

  it("lists a suite run that has filed no tasks (tasks: null) beside the others, naming no pass", async () => {
    // A run that ended before its first pass was filed comes back from the
    // server with `tasks: null` and `pass: 0`; reading .length off it threw
    // and the rest of the runs list never rendered.
    api.suiteRuns = [
      ...api.suiteRuns,
      {
        ...api.suiteRuns[0],
        id: 9,
        status: "failed",
        pass: 0,
        base: "",
        error: "pass 1 could not be filed: template gone",
        tasks: null,
      },
    ];
    const harness = await RouterTestingHarness.create("/processes/suites");
    await flush(harness);
    const root = harness.routeNativeElement!;
    root
      .querySelector<HTMLButtonElement>('[data-testid="suites-subview-runs"]')!
      .click();
    await flush(harness);

    const empty = root.querySelector('[data-testid="suite-run-row-9"]')!;
    expect(empty).toBeTruthy();
    expect(root.querySelector('[data-testid="suite-run-row-7"]')).toBeTruthy();
    expect(empty.textContent).toContain("0 tasks");
    expect(empty.textContent).not.toMatch(/Pass 0/);
    expect(
      root.querySelector('[data-testid="suite-run-row-7"]')!.textContent,
    ).toContain("Pass 1");
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
    expect(TestBed.inject(SnackbarService).current()?.message).toBe(
      "Closed 2 active suite tasks.",
    );
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

  it("renders Quality checks flow packages, active rollout badge, and rule inspector", async () => {
    const harness = await RouterTestingHarness.create("/processes/quality");
    await flush(harness);
    const root = harness.routeNativeElement!;

    const goldenRow = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-flow-row-golden-v1-minimal"]',
    )!;
    const detRow = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-flow-row-deterministic-s3-only"]',
    )!;
    expect(goldenRow).not.toBeNull();
    expect(detRow).not.toBeNull();
    expect(goldenRow.textContent).toContain("Active (50%)");
    expect(detRow.textContent).toContain("Available");

    const inspTitle = root.querySelector('[data-testid="qa-flow-insp-title"]')!;
    const detailCard = root.querySelector(
      '[data-testid="qa-flow-detail-card"]',
    )!;
    expect(inspTitle.textContent).toContain("Golden V1 Minimal");
    expect(detailCard.textContent).toContain("T2/T3 diffs (max 1 round)");
    expect(
      root
        .querySelector('[data-testid="qa-configure-rollout-link"]')
        ?.getAttribute("href"),
    ).toBe("/settings/tasks");
    expect(
      root.querySelector(
        '[data-testid="qa-rule-card-go-test-assertion-floor"]',
      ),
    ).not.toBeNull();

    // Selecting deterministic-s3-only updates the inspector
    detRow.click();
    await flush(harness);
    expect(
      root.querySelector('[data-testid="qa-flow-insp-title"]')!.textContent,
    ).toContain("Deterministic S3 Only");
    expect(
      root.querySelector('[data-testid="qa-flow-detail-card"]')!.textContent,
    ).toContain("disabled");
  });

  it("toggles inline read-only YAML preview on Repo Workflows & Declarations via GET /api/fs/repo/{path...} and renders 8-row prompt textareas", async () => {
    const harness = await RouterTestingHarness.create(
      "/processes/declarations",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    expect(root.querySelector('[data-testid="decl-inline-yaml"]')).toBeNull();

    const toggleBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="decl-inline-yaml-toggle"]',
    )!;
    expect(toggleBtn).not.toBeNull();
    expect(toggleBtn.textContent).toContain("View YAML");

    toggleBtn.click();
    await flush(harness);

    expect(toggleBtn.textContent).toContain("Hide YAML");
    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/fs/repo/{path...}" &&
          (c.params as { path?: string })?.path ===
            "acme/widgets/.grain/templates/release-notes.yml",
      ),
    ).toBe(true);

    const yamlPre = root.querySelector<HTMLElement>(
      '[data-testid="decl-inline-yaml"]',
    )!;
    expect(yamlPre).not.toBeNull();
    expect(yamlPre.tagName).toBe("PRE");
    expect(yamlPre.classList.contains("comp-body-well")).toBe(true);
    expect(yamlPre.textContent).toContain(
      "title: Draft release notes for {{version}}",
    );

    toggleBtn.click();
    await flush(harness);
    expect(toggleBtn.textContent).toContain("View YAML");
    expect(root.querySelector('[data-testid="decl-inline-yaml"]')).toBeNull();

    // Verify schedule and template dialogs use rows="8" for prompt textareas
    await harness.navigateByUrl("/processes/schedules?new=1");
    await flush(harness);
    const schedRoot = harness.routeNativeElement!;
    expect(
      schedRoot
        .querySelector('[data-testid="sched-desc-input"]')
        ?.getAttribute("rows"),
    ).toBe("8");
  });

  it("renders Software Workflow & QA tab label and round-trips qaFlowRef across Schedule, Template, and Suite create/edit drawers", async () => {
    const baseGet = api.get;
    api.get = (route: string, params?: Record<string, unknown>) => {
      if (route === "GET /api/qa/flow-versions") {
        api.calls.push({ method: "GET", route, params });
        return Promise.resolve({
          versions: [
            {
              hash: "builtin:engineering-workflow-v4@1#4",
              key: "engineering-workflow-v4",
              version: 1,
              label: "Engineering Workflow V4",
              builtIn: true,
            },
            {
              hash: "builtin:golden-v3-enforced@1#4",
              key: "golden-v3-enforced",
              version: 1,
              label: "Golden V3 Enforced",
              builtIn: true,
            },
          ],
        });
      }
      return baseGet(route, params);
    };

    const harness = await RouterTestingHarness.create(
      "/processes/schedules?new=1",
    );
    await flush(harness);
    let root = harness.routeNativeElement!;

    // 1. Quality tab displays "Software Workflow & QA"
    const qualityTab = root.querySelector('[data-testid="proc-tab-quality"]')!;
    expect(qualityTab.textContent).toContain("Software Workflow & QA");
    expect(api.calls.some((c) => c.route === "GET /api/qa/flow-versions")).toBe(
      true,
    );

    // 2. Schedule create & edit round-trip for qaFlowRef
    const schedTitleIn = root.querySelector<HTMLInputElement>(
      '[data-testid="sched-title-input"]',
    )!;
    schedTitleIn.value = "V4 workflow schedule";
    schedTitleIn.dispatchEvent(new Event("input"));

    const schedFlowBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="process-qa-flow-select"]',
    )!;
    expect(schedFlowBtn.textContent).toContain("Deployment default");
    schedFlowBtn.click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="process-qa-flow-select-opt-engineering-workflow-v4@1"]',
      )!
      .click();
    await flush(harness);
    expect(schedFlowBtn.textContent).toContain("engineering-workflow-v4@1");

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);

    const createSchedCall = api.calls.find(
      (c) => c.route === "POST /api/schedules",
    )!;
    expect((createSchedCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "engineering-workflow-v4@1",
    );
    expect(
      root.querySelector('[data-testid="process-qa-flow-badge"]')?.textContent,
    ).toContain("Workflow: engineering-workflow-v4@1");

    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-edit-btn"]')!
      .click();
    await flush(harness);
    const editSchedFlowBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="process-qa-flow-select"]',
    )!;
    expect(editSchedFlowBtn.textContent).toContain("engineering-workflow-v4@1");
    editSchedFlowBtn.click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="process-qa-flow-select-opt-golden-v3-enforced@1"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);

    const patchSchedCall = api.calls.find(
      (c) =>
        c.route === "PATCH /api/schedules/{id}" &&
        (c.params as { id?: string })?.id === "sched-new",
    )!;
    expect((patchSchedCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "golden-v3-enforced@1",
    );

    // 3. Template create & edit round-trip for qaFlowRef
    await harness.navigateByUrl("/processes/templates");
    await flush(harness);
    root = harness.routeNativeElement!;

    root
      .querySelector<HTMLButtonElement>('[data-testid="proc-new-menu-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="proc-new-template"]')!
      .click();
    await flush(harness);

    const tmplNameIn = root.querySelector<HTMLInputElement>(
      '[data-testid="tmpl-name-input"]',
    )!;
    tmplNameIn.value = "V4 service upgrade";
    tmplNameIn.dispatchEvent(new Event("input"));
    const tmplTitleIn = root.querySelector<HTMLInputElement>(
      '[data-testid="tmpl-title-input"]',
    )!;
    tmplTitleIn.value = "feat: upgrade service";
    tmplTitleIn.dispatchEvent(new Event("input"));

    const tmplFlowBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="process-qa-flow-select"]',
    )!;
    tmplFlowBtn.click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="process-qa-flow-select-opt-engineering-workflow-v4@1"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="tmpl-save-btn"]')!
      .click();
    await flush(harness);

    const createTmplCall = api.calls.find(
      (c) => c.route === "POST /api/templates",
    )!;
    expect((createTmplCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "engineering-workflow-v4@1",
    );
    expect(
      root.querySelector('[data-testid="process-qa-flow-badge"]')?.textContent,
    ).toContain("Workflow: engineering-workflow-v4@1");

    root
      .querySelector<HTMLButtonElement>('[data-testid="tmpl-edit-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="tmpl-save-btn"]')!
      .click();
    await flush(harness);
    const patchTmplCall = api.calls.find(
      (c) =>
        c.route === "PATCH /api/templates/{id}" &&
        (c.params as { id?: string })?.id === "tmpl-new",
    )!;
    expect((patchTmplCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "engineering-workflow-v4@1",
    );

    // 4. Suite create & edit round-trip for qaFlowRef
    await harness.navigateByUrl("/processes/suites");
    await flush(harness);
    root = harness.routeNativeElement!;

    root
      .querySelector<HTMLButtonElement>('[data-testid="proc-new-menu-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="proc-new-suite"]')!
      .click();
    await flush(harness);

    const suiteNameIn = root.querySelector<HTMLInputElement>(
      '[data-testid="suite-name-input"]',
    )!;
    suiteNameIn.value = "V4 hardening suite";
    suiteNameIn.dispatchEvent(new Event("input"));

    const suiteFlowBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="process-qa-flow-select"]',
    )!;
    suiteFlowBtn.click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="process-qa-flow-select-opt-engineering-workflow-v4@1"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);

    const createSuiteCall = api.calls.find(
      (c) => c.route === "POST /api/suites",
    )!;
    expect((createSuiteCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "engineering-workflow-v4@1",
    );
    expect(
      root.querySelector('[data-testid="process-qa-flow-badge"]')?.textContent,
    ).toContain("Workflow: engineering-workflow-v4@1");

    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-edit-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    const patchSuiteCall = api.calls.find(
      (c) =>
        c.route === "PATCH /api/suites/{id}" &&
        (c.params as { id?: string })?.id === "suite-2",
    )!;
    expect((patchSuiteCall.body as Record<string, unknown>)["qaFlowRef"]).toBe(
      "engineering-workflow-v4@1",
    );
  });

  it("renders dialog errors inside the active modal, enforces the 10-minute floor, and reopens ?new=1 after cancel", async () => {
    const basePost = api.post;
    api.post = async (
      route: string,
      params?: unknown,
      body?: Record<string, unknown>,
    ) => {
      if (route === "POST /api/schedules") {
        api.calls.push({ method: "POST", route, params, body });
        await Promise.resolve();
        throw new Error(
          "recurrence.every_n_minutes must be at least 10 (got 5)",
        );
      }
      return basePost(route, params, body);
    };

    const harness = await RouterTestingHarness.create(
      "/processes/schedules?new=1",
    );
    await flush(harness);
    const root = harness.routeNativeElement!;

    let dlg = root.querySelector('[data-testid="schedule-dialog"]')!;
    expect(dlg).not.toBeNull();
    expect(dlg.closest("[inert]")).toBeNull();

    // Switch to Every N minutes and verify min="10"
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="sched-kind-everyNMinutes"]',
      )!
      .click();
    await flush(harness);
    const minInput = dlg.querySelector<HTMLInputElement>(
      'input[type="number"]',
    )!;
    expect(minInput.getAttribute("min")).toBe("10");

    const titleInput = dlg.querySelector<HTMLInputElement>(
      '[data-testid="sched-title-input"]',
    )!;
    titleInput.value = "Too frequent";
    titleInput.dispatchEvent(new Event("input"));
    minInput.value = "5";
    minInput.dispatchEvent(new Event("input"));
    await flush(harness);

    dlg
      .querySelector<HTMLButtonElement>('[data-testid="sched-save-btn"]')!
      .click();
    await flush(harness);

    const dlgErr = dlg.querySelector('[data-testid="proc-dialog-error"]');
    expect(dlgErr).not.toBeNull();
    expect(dlgErr!.closest("[inert]")).toBeNull();
    expect(dlgErr!.textContent).toContain("must be at least 10");

    // Cancel and navigate to ?new=1 again -> must reopen because ?new= was cleared on first open.
    Array.from(dlg.querySelectorAll("button"))
      .find((b) => b.textContent?.trim() === "Cancel")!
      .click();
    await flush(harness);
    expect(root.querySelector('[data-testid="schedule-dialog"]')).toBeNull();

    await harness.navigateByUrl("/processes/schedules?new=1");
    await flush(harness);
    dlg = root.querySelector('[data-testid="schedule-dialog"]')!;
    expect(dlg).not.toBeNull();
  });

  it("formats off-target git_ref next run as suppressed, confirms workflow Run now, shows default branch on baseless suite runs, and previews required template fields without 400", async () => {
    api.schedules.unshift(
      {
        id: "sched-offtarget",
        title: "Off-target ref watcher",
        description: "Watches offlist/upstream",
        repo: "acme/widgets",
        autoMerge: false,
        capabilities: [],
        recurrence: {
          kind: "git_ref",
          gitRef: { repo: "offlist/upstream", ref: "refs/heads/main" },
        },
        overlap: "skip",
        source: "deployment",
        type: "task",
        enabled: true,
        nextRunAt: "2027-11-16T05:09:04Z",
        suppressed: {
          reason: "watchedRepoOffTarget",
          detail:
            "watches offlist/upstream, which is not on this deployment's target repo list",
        },
        createdAt: "2026-11-15T00:00:00Z",
      },
      {
        id: "repo:schedule:acme:widgets:workflow.nightly-ci",
        title: 'Review last night\'s "nightly-ci" workflow run',
        description: "",
        repo: "acme/widgets",
        autoMerge: false,
        capabilities: [],
        recurrence: { kind: "daily", timeOfDay: "04:00" },
        overlap: "skip",
        source: "acme/widgets",
        type: "workflow",
        system: true,
        declaredIn: {
          repo: "acme/widgets",
          path: ".grain/workflows/nightly-ci.yml",
        },
        enabled: true,
        nextRunAt: "2026-11-16T04:00:00Z",
        createdAt: "2026-11-15T00:00:00Z",
      },
    );
    api.templates.unshift({
      id: "tmpl-req-nodefault",
      name: "req-param-tmpl",
      title: "chore: bump {{pkg}}",
      description: "Bump {{pkg}}",
      repo: "acme/widgets",
      autoMerge: false,
      capabilities: [],
      fields: [
        { name: "pkg", label: "Package", required: true },
        { name: "note", label: "Note" },
      ],
      createdAt: "2026-11-15T00:00:00Z",
    });
    api.suiteRuns.unshift({
      id: 99,
      suiteId: "suite-1",
      suiteName: "Release Hardening",
      repo: "acme/widgets",
      base: "",
      mode: "count",
      count: 2,
      status: "failed",
      pass: 0,
      error: "pass 1 could not be filed",
      tasks: null,
      createdAt: "2026-11-15T00:00:00Z",
    });

    const harness = await RouterTestingHarness.create("/processes/schedules");
    await flush(harness);
    const root = harness.routeNativeElement!;

    // Off-target git_ref row & inspector show "suppressed" rather than 2027-11-16
    const offRow = root.querySelector(
      '[data-testid="sched-row-sched-offtarget"]',
    )!;
    expect(offRow.textContent).toContain("next: suppressed");
    expect(offRow.textContent).not.toContain("2027-11-16");
    (offRow as HTMLElement).click();
    await flush(harness);
    const insp = root.querySelector('[data-testid="proc-inspector"]')!;
    const nextRun = Array.from(insp.querySelectorAll("dt")).find(
      (dt) => dt.textContent?.trim() === "Next run",
    );
    expect(nextRun?.nextElementSibling?.textContent?.trim()).toBe("suppressed");
    expect(insp.textContent).not.toContain("2027-11-16");

    // Workflow schedule asks confirmation with straight-away CI wording before running
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="sched-row-repo:schedule:acme:widgets:workflow.nightly-ci"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="sched-run-now-btn"]')!
      .click();
    await flush(harness);
    const confirmDlg = root.querySelector('[data-testid="confirm-dialog"]')!;
    expect(confirmDlg).not.toBeNull();
    expect(confirmDlg.textContent).toContain(
      "starts a CI run of the workflow it was declared in straight away",
    );
    expect(
      api.calls.some((c) => c.route === "POST /api/schedules/{id}/run"),
    ).toBe(false);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await flush(harness);
    expect(
      api.calls.some((c) => c.route === "POST /api/schedules/{id}/run"),
    ).toBe(true);

    // Template with required field and no default sends placeholder {{pkg}}
    await harness.navigateByUrl("/processes/templates");
    await flush(harness);
    root
      .querySelector<HTMLElement>(
        '[data-testid="tmpl-row-tmpl-req-nodefault"]',
      )!
      .click();
    await flush(harness);
    const tmplCompCall = api.calls.find(
      (c) =>
        c.route === "GET /api/templates/{id}/composition" &&
        (c.params as { id: string }).id === "tmpl-req-nodefault",
    );
    expect(tmplCompCall?.query).toContain("field.pkg=%7B%7Bpkg%7D%7D");
    // An optional field with no default is left out, as a filing leaves it.
    expect(tmplCompCall?.query).not.toContain("field.note");

    // Baseless failed 0-pass suite run shows "default branch" on suite detail recent runs, run row, and run inspector, without "(pass 0)"
    await harness.navigateByUrl("/processes/suites");
    await flush(harness);
    const suiteInspText = root.querySelector(
      '[data-testid="proc-inspector"]',
    )!.textContent!;
    expect(suiteInspText).toContain("Run #99 · acme/widgets @ default branch");
    expect(suiteInspText).not.toContain("pass 0");

    root
      .querySelector<HTMLButtonElement>('[data-testid="suites-subview-runs"]')!
      .click();
    await flush(harness);
    const run99Row = root.querySelector<HTMLButtonElement>(
      '[data-testid="suite-run-row-99"]',
    )!;
    expect(run99Row.textContent).toContain("acme/widgets @ default branch");
    run99Row.click();
    await flush(harness);
    expect(
      root.querySelector('[data-testid="proc-inspector"]')!.textContent,
    ).toContain("Target: acme/widgets @ default branch");
  });
  it("lists a repository's declared triggers beside the schedules, linking its Advanced tab", async () => {
    const harness = await RouterTestingHarness.create("/processes/schedules");
    await flush(harness);
    await flush(harness);
    const root = harness.routeNativeElement!;

    const row = root.querySelector<HTMLAnchorElement>(
      '[data-testid="trigger-row-trigger:acme/widgets:.grain/triggers/docs.yml"]',
    )!;
    expect(row).not.toBeNull();
    expect(row.getAttribute("href")).toBe("/repos/acme/widgets/advanced");
    expect(row.textContent).toContain("docs.yml");
    expect(row.textContent).toContain("on change");

    // A trigger is never paused, so the Paused chip leaves it out.
    root
      .querySelector<HTMLElement>('[data-testid="sched-filter-paused"]')!
      .click();
    await flush(harness);
    expect(root.querySelector('[data-testid^="trigger-row-"]')).toBeNull();
    root
      .querySelector<HTMLElement>('[data-testid="sched-filter-triggers"]')!
      .click();
    await flush(harness);
    expect(root.querySelector('[data-testid^="trigger-row-"]')).not.toBeNull();
  });

  it("formats a schedule's run times, keeps an undetailed suppression to its own reason, counts in the singular, and refuses a 0-pass suite in the dialog", async () => {
    api.schedules.unshift({
      id: "sched-held",
      title: "Held by an open task",
      description: "Holds.",
      repo: "acme/widgets",
      autoMerge: false,
      capabilities: [],
      recurrence: { kind: "every_n_minutes", everyNMinutes: 30 },
      overlap: "skip",
      source: "deployment",
      type: "task",
      enabled: true,
      nextRunAt: "2026-11-16T10:23:11.320429227Z",
      lastRunAt: "2026-11-16T10:14:03.830464856Z",
      createdAt: "2026-11-01T00:00:00Z",
      suppressed: { reason: "suiteRun" },
      revision: "rev-held",
    });
    const harness = await RouterTestingHarness.create("/processes/schedules");
    await flush(harness);
    const root = harness.routeNativeElement!;
    root
      .querySelector<HTMLElement>('[data-testid="sched-row-sched-held"]')!
      .click();
    await flush(harness);

    // The Configuration pane says when, as the row does -- not the wire's
    // nanosecond RFC 3339 string.
    const at = (id: string) =>
      root.querySelector(`[data-testid="${id}"]`)?.textContent?.trim();
    expect(at("sched-next-run")).toBe(
      formatStamp("2026-11-16T10:23:11.320429227Z"),
    );
    expect(at("sched-last-run")).toBe(
      formatStamp("2026-11-16T10:14:03.830464856Z"),
    );
    expect(at("sched-last-run")).not.toContain("T10:14:03");
    // With no Detail from the server the reason stands alone: an
    // unfinished suite run is not "Previous firing is still open."
    const banner = at("sched-suppressed-banner") ?? "";
    expect(banner).toContain("Suppressed — suite run unfinished.");
    expect(banner).not.toContain("Previous firing");

    await harness.navigateByUrl("/processes/suites");
    await flush(harness);
    expect(
      root.querySelector('[data-testid="suite-row-suite-1"]')?.textContent,
    ).toContain("1 template");
    expect(
      root.querySelector('[data-testid="suite-row-suite-1"]')?.textContent,
    ).not.toContain("1 templates");

    // Maximum passes 0 is refused in the dialog, not saved as 3.
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-edit-btn"]')!
      .click();
    await flush(harness);
    const passes = root.querySelector<HTMLInputElement>(
      '[data-testid="suite-max-passes-input"]',
    )!;
    passes.value = "0";
    passes.dispatchEvent(new Event("input"));
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "PATCH /api/suites/{id}")).toBe(
      false,
    );
    expect(
      root.querySelector('[data-testid="suite-dialog"] [role="alert"]')
        ?.textContent,
    ).toContain("Maximum passes must be a whole number from 1 to 100.");

    passes.value = "2.5";
    passes.dispatchEvent(new Event("input"));
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    expect(api.calls.some((c) => c.route === "PATCH /api/suites/{id}")).toBe(
      false,
    );

    passes.value = "4";
    passes.dispatchEvent(new Event("input"));
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    expect(
      api.calls.find((c) => c.route === "PATCH /api/suites/{id}")?.body,
    ).toMatchObject({ maxPasses: 4 });

    // A fraction left behind in the other mode's field is not sent.
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-edit-btn"]')!
      .click();
    await flush(harness);
    const max = root.querySelector<HTMLInputElement>(
      '[data-testid="suite-max-passes-input"]',
    )!;
    max.value = "2.5";
    max.dispatchEvent(new Event("input"));
    await flush(harness);
    Array.from(
      root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="suite-dialog"] button',
      ),
    )
      .find((b) => b.textContent?.trim() === "Fixed pass count")!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="suite-save-btn"]')!
      .click();
    await flush(harness);
    const patches = api.calls.filter(
      (c) => c.route === "PATCH /api/suites/{id}",
    );
    expect(patches).toHaveLength(2);
    expect(patches[1].body).toMatchObject({ mode: "count", maxPasses: 3 });
  });
});
