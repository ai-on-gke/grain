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
import { Subject } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import type { GrainConfig } from "../api/models";
import { CIPageComponent } from "./ci-page.component";
import {
  type CIFault,
  type CIRun,
  type CIRunArtifact,
  type CIRunLogResponse,
  type CIWorkflowOption,
  faultsForRun,
  safeExternalUrl,
} from "./ci.models";

class FakeCIApi {
  configBody: GrainConfig = {
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
    targetRepos: ["acme/widgets", "acme/infra"],
  };

  runs: CIRun[] = [
    {
      id: "run-errored-1",
      provider: "grain",
      repo: "acme/widgets",
      ref: "refs/heads/grain/task-101",
      commit: "aaa111122223333",
      event: "push",
      requestedBy: "101",
      conclusion: "errored",
      error: "sandbox boot failed: granule image pull timeout",
      startedAt: "2026-11-12T00:00:00Z",
      finishedAt: "2026-11-12T00:01:15Z",
      jobs: [
        {
          jobId: "test",
          name: "Unit tests",
          workflow: "CI",
          conclusion: "errored",
          error: "sandbox boot failed",
          steps: [],
        },
      ],
    },
    {
      id: "run-failed-2",
      provider: "grain",
      repo: "acme/widgets",
      ref: "refs/heads/grain/task-102",
      commit: "bbb222233334444",
      event: "pull_request",
      requestedBy: "102",
      conclusion: "failure",
      startedAt: "2026-11-12T00:02:00Z",
      finishedAt: "2026-11-12T00:04:00Z",
      jobs: [
        {
          jobId: "unit",
          name: "Go unit suite",
          workflow: "CI",
          conclusion: "failure",
          failureSummary: { failedTests: ["TestCheckoutRace"] },
          steps: [
            {
              name: "Run go test",
              conclusion: "failure",
              exitCode: 1,
              output:
                "=== RUN TestCheckoutRace\n--- FAIL: TestCheckoutRace (0.14s)\npanic: race detected",
            },
          ],
        },
      ],
    },
    {
      id: "run-running-3",
      provider: "grain",
      repo: "acme/infra",
      ref: "refs/heads/main",
      commit: "ccc333344445555",
      event: "workflow_dispatch",
      status: "running",
      startedAt: "2026-11-12T00:05:00Z",
      jobs: [
        {
          jobId: "lint",
          name: "Lint",
          workflow: "Checks",
          startedAt: "2026-11-12T00:05:02Z",
          steps: [
            {
              name: "golangci-lint",
              startedAt: "2026-11-12T00:05:03Z",
              output: "linting pkg/...",
            },
          ],
        },
      ],
    },
    {
      id: "gha:acme:widgets:9001",
      provider: "github-actions",
      repo: "acme/widgets",
      ref: "refs/heads/main",
      commit: "ddd444455556666",
      conclusion: "failure",
      actions: {
        runId: 9001,
        workflowName: "GitHub E2E",
        status: "completed",
        conclusion: "failure",
        url: "https://github.com/acme/widgets/actions/runs/9001",
      },
      jobs: [
        {
          jobId: "e2e",
          name: "Playwright E2E",
          conclusion: "failure",
          steps: [{ name: "npx playwright test", conclusion: "failure" }],
        },
      ],
    },
  ];

  faults: CIFault[] = [
    {
      source: "grain",
      digest: "fault-granule-pull",
      repo: "acme/widgets",
      jobId: "test",
      jobName: "Unit tests",
      step: "Setup sandbox",
      kind: "errored",
      summary: "Granule image registry 503 across branches",
      runCount: 4,
      refCount: 3,
      ownerTaskId: "88",
      runs: [
        {
          runId: "run-errored-1",
          ref: "grain/task-101",
          taskId: "101",
          sha: "aaa111122223333",
        },
      ],
      refs: ["grain/task-101", "grain/task-99", "main"],
    },
  ];

  artifactsByRun: Record<string, CIRunArtifact[]> = {
    "run-failed-2": [
      {
        run: "run-failed-2",
        job: "unit",
        name: "coverage-report",
        stored: true,
        bytes: 24576,
      },
      {
        run: "run-failed-2",
        job: "unit",
        name: "expired-trace",
        stored: false,
        bytes: 1024,
      },
    ],
  };

  workflows: CIWorkflowOption[] = [
    { path: ".grain/workflows/ci.yml", name: "CI" },
    { path: ".grain/workflows/nightly.yml", name: "Nightly" },
  ];

  ghaLog: CIRunLogResponse = {
    run: "gha:acme:widgets:9001",
    job: "Playwright E2E",
    jobId: 77,
    bytes: 128,
    log: "Step 1: Launching browser\nError: Timed out waiting for selector #app",
  };

  runListQueries: string[] = [];
  postCalls: {
    route: string;
    params?: unknown;
    body?: unknown;
    query?: string;
  }[] = [];
  deleteCalls: { route: string; params?: unknown; body?: unknown }[] = [];
  logCalls: { id: string; query?: string }[] = [];

  config = () => Promise.resolve(this.configBody);
  clearConditionalCache = () => undefined;

  getIfChanged = (
    route: string,
    _params?: Record<string, unknown>,
    query?: string,
  ) => {
    if (route === "GET /api/ci/runs") {
      this.runListQueries.push(query ?? "");
      const qp = new URLSearchParams(query ?? "");
      const prov = qp.get("provider");
      const repo = qp.get("repo");
      // Mirror pkg/ui/ci_actions.go wantsActions: human sessions only receive
      // GitHub Actions runs when both repo and provider=github-actions are sent.
      if (prov === "github-actions" && repo) {
        const gha = this.runs.filter(
          (r) => r.provider === "github-actions" && r.repo === repo,
        );
        return Promise.resolve({
          body: {
            enabled: true,
            runs: structuredClone(gha),
            matched: gha.length,
            stored: gha.length,
            repoProvider: "github-actions",
          },
          changed: true,
        });
      }
      const grainOnly = this.runs.filter(
        (r) => r.provider !== "github-actions",
      );
      return Promise.resolve({
        body: {
          enabled: true,
          runs: structuredClone(grainOnly),
          matched: grainOnly.length,
          stored: grainOnly.length,
          faults: structuredClone(this.faults),
        },
        changed: true,
      });
    }
    if (route === "GET /api/ci/faults") {
      return Promise.resolve({
        body: {
          enabled: true,
          faults: structuredClone(this.faults),
          total: this.faults.length,
        },
        changed: true,
      });
    }
    return Promise.resolve({ body: null, changed: false });
  };

  get = (route: string, params?: Record<string, unknown>, query?: string) => {
    if (route === "GET /api/ci/runs/{id}") {
      const id = String(params?.["id"] ?? "");
      const found = this.runs.find((r) => r.id === id);
      return Promise.resolve(structuredClone(found ?? this.runs[0]));
    }
    if (route === "GET /api/ci/runs/{id}/artifacts") {
      const id = String(params?.["id"] ?? "");
      const list = this.artifactsByRun[id] ?? [];
      return Promise.resolve({
        enabled: true,
        available: true,
        artifacts: structuredClone(list),
        total: list.length,
      });
    }
    if (route === "GET /api/ci/runs/{id}/log") {
      this.logCalls.push({ id: String(params?.["id"] ?? ""), query });
      return Promise.resolve(structuredClone(this.ghaLog));
    }
    if (route === "GET /api/ci/workflows") {
      return Promise.resolve({
        workflows: structuredClone(this.workflows),
        total: this.workflows.length,
      });
    }
    return Promise.resolve({});
  };

  post = (
    route: string,
    params?: Record<string, unknown>,
    body?: Record<string, unknown>,
    query?: string,
  ) => {
    this.postCalls.push({ route, params, body, query });
    if (route === "POST /api/ci/runs") {
      const created: CIRun = {
        id: "run-triggered-99",
        provider: "grain",
        repo: String(body?.["repo"] || "acme/widgets"),
        ref: String(body?.["ref"] || "main"),
        status: "running",
        startedAt: "2026-11-12T00:10:00Z",
        jobs: [],
      };
      this.runs.unshift(created);
      return Promise.resolve(created);
    }
    if (route === "POST /api/tests/quarantine") {
      return Promise.resolve({
        testName: String(body?.["testName"] ?? ""),
        reason: String(body?.["reason"] ?? ""),
        ttl: String(body?.["ttl"] ?? "30d"),
        followUpTaskId: "501",
        createdAt: "2026-11-12T00:10:00Z",
        quarantineYaml: `tests:\n  - testName: ${body?.["testName"]}\n    ttl: 30d\n`,
      });
    }
    if (route === "POST /api/actions-cache/flush") {
      return Promise.resolve({ entries: 4, bytes: 1048576 });
    }
    if (route === "POST /api/package-cache/flush") {
      return Promise.resolve({ entries: 12, bytes: 2097152 });
    }
    return Promise.resolve({});
  };

  delete = (
    route: string,
    params?: Record<string, unknown>,
    body?: unknown,
  ) => {
    this.deleteCalls.push({ route, params, body });
    if (route === "DELETE /api/ci/runs/{id}") {
      const id = String(params?.["id"] ?? "");
      const found = this.runs.find((r) => r.id === id);
      if (found) {
        found.status = "completed";
        found.conclusion = "cancelled";
      }
      return Promise.resolve({});
    }
    if (route === "DELETE /api/ci/cache") {
      return Promise.resolve({ enabled: true, available: true, removed: 7 });
    }
    return Promise.resolve({});
  };
}

async function flush(harness: RouterTestingHarness): Promise<void> {
  harness.detectChanges();
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  harness.detectChanges();
  await harness.fixture.whenStable();
}

describe("CIPageComponent (D-26)", () => {
  let api: FakeCIApi;
  let events$: Subject<GrainEvent>;

  beforeEach(() => {
    localStorage.clear();
    Object.defineProperty(window, "innerWidth", {
      value: 1440,
      configurable: true,
      writable: true,
    });
    api = new FakeCIApi();
    events$ = new Subject<GrainEvent>();
    TestBed.configureTestingModule({
      providers: [
        provideRouter(
          [
            { path: "ci", component: CIPageComponent },
            { path: "ci/runs/:id", component: CIPageComponent },
            { path: "task/:id", component: CIPageComponent },
          ],
          withComponentInputBinding(),
        ),
        { provide: GrainApi, useValue: api },
        {
          provide: GrainEvents,
          useValue: { events: () => events$.asObservable() },
        },
      ],
    });
  });

  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("separates shared infrastructure faults (Errored) from isolated code failures (Failed) using FaultRun.runId without cross-source leakage", async () => {
    // Verify pure model guard: grain fault on "main" never taints a GitHub Actions run on "main",
    // nor an unrelated Grain run on "main" whose runId is not in fault.runs.
    const ghaRun = api.runs.find((r) => r.id === "gha:acme:widgets:9001")!;
    expect(faultsForRun(ghaRun, api.faults)).toEqual([]);
    expect(safeExternalUrl("javascript:alert(1)")).toBeNull();
    expect(
      safeExternalUrl("https://github.com/acme/widgets/actions/runs/9001"),
    ).toBe("https://github.com/acme/widgets/actions/runs/9001");

    const harness = await RouterTestingHarness.create("/ci");
    await flush(harness);
    const root = harness.routeNativeElement!;

    expect(root.querySelectorAll("select").length).toBe(0);

    const banner = root.querySelector('[data-testid="ci-faults-banner"]')!;
    expect(banner).not.toBeNull();
    expect(banner.textContent).toContain("1 active fault cluster");
    expect(banner.textContent).toContain("Granule image registry 503");
    expect(banner.textContent).toContain("Assigned fix #88");

    // Select isolated failure run first, then click "Inspect affected runs" on fault card to verify f.runs[0].runId navigation.
    root
      .querySelector<HTMLElement>('[data-testid="ci-run-row-run-failed-2"]')!
      .click();
    await flush(harness);

    expect(
      root.querySelector('[data-testid="ci-failed-callout"]')?.textContent,
    ).toContain("Branch code / test failure");
    const logWell = root.querySelector('[data-testid="ci-log-viewer"]')!;
    expect(logWell.textContent).toContain("FAIL: TestCheckoutRace");

    // Only stored === true artifacts render download links.
    const dls = root.querySelectorAll<HTMLAnchorElement>(
      '[data-testid="ci-artifact-download"]',
    );
    expect(dls.length).toBe(1);
    expect(dls[0].getAttribute("href")).toBe(
      "/api/ci/runs/run-failed-2/artifacts/unit/coverage-report",
    );

    // Click Inspect affected runs -> opens run-errored-1 via f.runs[0].runId.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="ci-fault-inspect-runs"]',
      )!
      .click();
    await flush(harness);
    expect(
      root.querySelector('[data-testid="ci-errored-callout"]')?.textContent,
    ).toContain("Shared deployment / infrastructure fault");
    expect(
      root
        .querySelector('[data-testid="ci-detail-task-link"]')
        ?.getAttribute("href"),
    ).toBe("/task/101");
  });

  it("queries provider=github-actions when switching provider or selecting a repo, renders safe external links, and loads GitHub logs", async () => {
    const harness = await RouterTestingHarness.create("/ci");
    await flush(harness);
    const root = harness.routeNativeElement!;

    // Filter to GitHub Actions provider -> queries GET /api/ci/runs?repo=...&provider=github-actions
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-provider-github"]')!
      .click();
    await flush(harness);
    expect(
      api.runListQueries.some(
        (q) =>
          q.includes("provider=github-actions") &&
          q.includes("repo=acme%2Fwidgets"),
      ),
    ).toBe(true);
    expect(root.querySelectorAll(".ci-run-row").length).toBe(1);

    root
      .querySelector<HTMLElement>(
        '[data-testid="ci-run-row-gha:acme:widgets:9001"]',
      )!
      .click();
    await flush(harness);

    const extLink = root.querySelector<HTMLAnchorElement>(
      '[data-testid="ci-external-link"]',
    )!;
    expect(extLink.getAttribute("rel")).toBe("noopener noreferrer");

    // Fetch external GitHub Actions job log
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-fetch-gha-log"]')!
      .click();
    await flush(harness);
    expect(api.logCalls.length).toBeGreaterThan(0);
    expect(
      root.querySelector('[data-testid="ci-log-viewer"]')?.textContent,
    ).toContain("Timed out waiting for selector #app");

    // Switch to All providers and pick Repo: acme/widgets via WAI-ARIA listbox -> merges Grain + GitHub runs!
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-provider-all"]')!
      .click();
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-repo-pill"]')!
      .click();
    await flush(harness);

    const repoOptions = [
      ...root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="ci-repo-menu"] [role="option"]',
      ),
    ];
    expect(repoOptions.length).toBeGreaterThanOrEqual(3);
    repoOptions.find((b) => b.textContent?.includes("acme/widgets"))!.click();
    await flush(harness);
    // 2 Grain runs on acme/widgets + 1 GitHub Actions run on acme/widgets = 3 merged runs
    expect(root.querySelectorAll(".ci-run-row").length).toBe(3);

    // Escape closes open pop-menu
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-repo-pill"]')!
      .click();
    await flush(harness);
    expect(root.querySelector('[data-testid="ci-repo-menu"]')).not.toBeNull();
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await flush(harness);
    expect(root.querySelector('[data-testid="ci-repo-menu"]')).toBeNull();
  });

  it("keeps modals outside [inert], enforces repo-scoped RBAC inside dialogs, cancels runs, flushes caches, and quarantines flaky tests", async () => {
    api.configBody = {
      access: {
        role: "grain/repo-ci",
        roles: ["grain/repo-ci"],
        full: false,
        scopes: ["ci:read", "ci:write", "tests:write", "caches:write"],
        repoLimits: {
          "ci:write": ["acme/infra"],
        },
      },
      targetRepos: ["acme/widgets", "acme/infra"],
    };

    const harness = await RouterTestingHarness.create("/ci/runs/run-running-3");
    await flush(harness);
    const root = harness.routeNativeElement!;

    // 1. Cancel active run via confirm dialog — verify confirm-dialog is NOT inside [inert]
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-cancel-run-btn"]')!
      .click();
    await flush(harness);
    const confirmDlg = root.querySelector('[data-testid="confirm-dialog"]')!;
    expect(confirmDlg).not.toBeNull();
    expect(confirmDlg.closest("[inert]")).toBeNull();
    expect(root.querySelector(".view-panel")?.hasAttribute("inert")).toBe(true);

    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await flush(harness);
    expect(
      api.deleteCalls.some((c) => c.route === "DELETE /api/ci/runs/{id}"),
    ).toBe(true);

    // 2. Trigger manual workflow dialog — verify outside [inert], RBAC repo limit, and Escape dismissal
    const openRunBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="ci-open-run-dialog"]',
    )!;
    openRunBtn.focus();
    openRunBtn.click();
    await flush(harness);
    const runDialog = root.querySelector('[data-testid="ci-run-dialog"]')!;
    expect(runDialog).not.toBeNull();
    expect(runDialog.closest("[inert]")).toBeNull();

    const repoInput = root.querySelector<HTMLInputElement>(
      '[data-testid="ci-run-repo-input"]',
    )!;
    const submitBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="ci-run-submit"]',
    )!;
    // Unauthorized repo acme/widgets disables ci-run-submit
    repoInput.value = "acme/widgets";
    repoInput.dispatchEvent(new Event("input"));
    await flush(harness);
    expect(submitBtn.disabled).toBe(true);

    // Authorized repo acme/infra enables ci-run-submit
    repoInput.value = "acme/infra";
    repoInput.dispatchEvent(new Event("input"));
    await flush(harness);
    expect(submitBtn.disabled).toBe(false);

    const wfOpt = root.querySelector<HTMLButtonElement>(
      '[data-testid="ci-wf-opt-.grain/workflows/ci.yml"]',
    )!;
    expect(wfOpt).not.toBeNull();
    wfOpt.click();
    submitBtn.click();
    await flush(harness);
    expect(
      api.postCalls.some(
        (c) =>
          c.route === "POST /api/ci/runs" &&
          (c.body as Record<string, unknown>)?.["workflow"] ===
            ".grain/workflows/ci.yml",
      ),
    ).toBe(true);

    // 3. Flush Actions cache via confirm dialog
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-caches-menu-btn"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="ci-flush-actions-cache"]',
      )!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await flush(harness);
    expect(
      api.postCalls.some((c) => c.route === "POST /api/actions-cache/flush"),
    ).toBe(true);

    // 4. Quarantine flaky test on failed run — verify outside [inert]
    root
      .querySelector<HTMLElement>('[data-testid="ci-run-row-run-failed-2"]')!
      .click();
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="ci-open-quarantine-btn"]',
      )!
      .click();
    await flush(harness);
    const qDialog = root.querySelector('[data-testid="ci-quarantine-dialog"]')!;
    expect(qDialog).not.toBeNull();
    expect(qDialog.closest("[inert]")).toBeNull();

    const testInput = root.querySelector<HTMLInputElement>(
      '[data-testid="ci-quarantine-test-input"]',
    )!;
    expect(testInput.value).toBe("TestCheckoutRace");
    const reasonInput = root.querySelector<HTMLTextAreaElement>(
      '[data-testid="ci-quarantine-reason-input"]',
    )!;
    reasonInput.value = "Flaky timer under load";
    reasonInput.dispatchEvent(new Event("input"));
    await flush(harness);
    root
      .querySelector<HTMLButtonElement>('[data-testid="ci-quarantine-submit"]')!
      .click();
    await flush(harness);
    expect(
      api.postCalls.some((c) => c.route === "POST /api/tests/quarantine"),
    ).toBe(true);
    expect(
      root.querySelector('[data-testid="ci-quarantine-result"]')?.textContent,
    ).toContain("#501");

    // Capture-phase Escape dismisses quarantine dialog
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await flush(harness);
    expect(
      root.querySelector('[data-testid="ci-quarantine-dialog"]'),
    ).toBeNull();
  });
});
