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
import type { GrainConfig, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { TelemetryPageComponent } from "./telemetry-page.component";
import type {
  ClusterStats,
  MetricsReport,
  UsageResponse,
} from "./telemetry.models";

interface RecordedCall {
  method: string;
  route: string;
  params?: unknown;
  query?: string;
  body?: unknown;
}

class FakeTelemetryApi {
  calls: RecordedCall[] = [];

  configBody: GrainConfig = {
    version: "v3.5.0-test",
    targetRepos: ["acme/api", "acme/web"],
    agentBackends: [{ id: "claude-main" }, { id: "gemini-pro" }],
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  tasksList: Task[] = [
    {
      id: "101",
      title: "Migrate auth tokens",
      state: "running",
      repo: "acme/api",
      author: "Zane",
      authorKind: "human",
    },
    {
      id: "102",
      title: "Tighten CSP headers",
      state: "queued",
      repo: "acme/web",
      author: "Alice",
      authorKind: "human",
    },
    {
      id: "103",
      title: "Fix flaky shard",
      state: "running",
      repo: "acme/api",
      author: "Alice",
      authorKind: "human",
    },
  ];

  reportBody: MetricsReport = {
    since: "2026-11-05T00:00:00Z",
    until: "2026-11-12T00:00:00Z",
    windowSeconds: 604800,
    scope: { repos: ["acme/api", "acme/web"] },
    throughput: {
      tasksFiled: 18,
      tasksCompleted: 14,
      tasksClosed: 2,
      runsStarted: 20,
      runsFinished: 20,
      filedPerDay: 2.57,
      completedPerDay: 2.0,
      runsFinishedPerDay: 2.86,
      buckets: [
        {
          since: "2026-11-10T00:00:00Z",
          until: "2026-11-11T00:00:00Z",
          filed: 8,
          completed: 6,
          runsFinished: 9,
        },
        {
          since: "2026-11-11T00:00:00Z",
          until: "2026-11-12T00:00:00Z",
          filed: 10,
          completed: 8,
          runsFinished: 11,
        },
      ],
    },
    latency: [
      {
        stage: "queue_wait",
        label: "Queue wait",
        description: "Filed to first worker pickup",
        n: 14,
        minSeconds: 10,
        p50Seconds: 45,
        p90Seconds: 180,
        p99Seconds: 240,
        maxSeconds: 300,
        meanSeconds: 70,
      },
      {
        stage: "agent_work",
        label: "Agent execution",
        description: "Active turn runtime",
        n: 14,
        minSeconds: 120,
        p50Seconds: 420,
        p90Seconds: 960,
        p99Seconds: 1200,
        maxSeconds: 1500,
        meanSeconds: 510,
      },
    ],
    runs: {
      live: 3,
      meanConcurrent: 2.4,
      maxConcurrent: 6,
      utilization: 0.5,
      outcomes: { succeeded: 15, awaiting_reply: 3, failed: 2 },
      attemptsPerCompletion: 1.3,
      execTunnelFailures: 1,
    },
    merges: {
      landed: 12,
      landedPerHour: 0.07,
      landedPerDay: 1.71,
      filedToLanded: {
        n: 12,
        minSeconds: 600,
        p50Seconds: 1800,
        p90Seconds: 5400,
        p99Seconds: 7200,
        maxSeconds: 8000,
        meanSeconds: 2400,
      },
      completedToLanded: {
        n: 12,
        minSeconds: 180,
        p50Seconds: 600,
        p90Seconds: 1500,
        p99Seconds: 2100,
        maxSeconds: 2400,
        meanSeconds: 780,
      },
      batches: {
        batches: 5,
        changes: 12,
        fresh: 5,
        firstTry: 4,
        firstTryRate: 0.8,
        passed: 4,
        passRate: 0.8,
        split: 1,
        culprits: 1,
        saved: 7,
      },
      baseHolds: { passes: 1, open: 0 },
      buckets: [
        {
          since: "2026-11-10T00:00:00Z",
          until: "2026-11-11T00:00:00Z",
          landed: 5,
          perHour: 0.21,
        },
        {
          since: "2026-11-11T00:00:00Z",
          until: "2026-11-12T00:00:00Z",
          landed: 7,
          perHour: 0.29,
        },
      ],
    },
    checks: {
      waits: 20,
      runs: 20,
      verdicts: { passed: 18, failed: 2 },
      greenRuns: 18,
    },
    ciQueueing: {
      jobs: 20,
      waited: 5,
      cached: 14,
      queuedSeconds: 310,
      busySeconds: 930,
      queuedShare: 0.25,
      cacheHitShare: 0.72,
      byCause: [
        {
          cause: "host_cpu",
          reason: "Waiting for vCPU slot",
          jobs: 5,
          share: 0.25,
          queuedSeconds: 310,
          longestSeconds: 110,
        },
      ],
    },
    tools: {
      runs: 20,
      calls: 1240,
      errored: 24,
      erroredShare: 0.019,
      byTool: [
        {
          name: "bash",
          runs: 20,
          calls: 620,
          errored: 18,
          errorRate: 0.029,
          timedOut: 0,
          timeoutRate: 0,
        },
        {
          name: "read_file",
          runs: 19,
          calls: 410,
          errored: 2,
          errorRate: 0.005,
          timedOut: 0,
          timeoutRate: 0,
        },
      ],
    },
    agents: {
      templates: [
        {
          templateId: "coder",
          wakes: 16,
          calls: 380,
          bytes: 98000,
          callsP50: 22,
          callsP95: 48,
          report: {
            wakes: 16,
            claims: 14,
            uncited: 1,
            unread: 0,
            mismatched: 0,
            uncitedRate: 0.071,
            unreadRate: 0,
            mismatchRate: 0,
          },
        },
      ],
    },
  };

  usageBody: UsageResponse = {
    mode: "advisory",
    maxWorkers: 6,
    minWorkers: 1,
    effectiveWorkers: 6,
    settings: {
      mode: "advisory",
      minWorkers: 1,
      retentionDays: 30,
      budgets: [{ connection: "claude-main", tokens: 5_000_000, window: "5h" }],
    },
    rollup: {
      runs: 20,
      inputTokens: 1_200_000,
      outputTokens: 320_000,
      cacheReadTokens: 800_000,
      cacheWriteTokens: 150_000,
      billable: 1_520_000,
      costUsd: 14.5,
      burnPerHour: 95_000,
    },
    scopes: [
      {
        connection: "claude-main",
        connectionName: "Claude Main",
        window: "5h",
        runs: 14,
        inputTokens: 900_000,
        outputTokens: 250_000,
        cacheReadTokens: 700_000,
        cacheWriteTokens: 100_000,
        used: 1_150_000,
        limit: 5_000_000,
        limitSource: "configured",
        burnPerHour: 72_000,
      },
    ],
  };

  clusterStatsBody: ClusterStats = {
    period: "last-week",
    since: "2026-11-02T00:00:00Z",
    until: "2026-11-09T00:00:00Z",
    tasksCompleted: 32,
    tasksFiled: 36,
    pullRequestsMerged: 24,
    pullRequestsMeasured: 24,
    linesAdded: 8420,
    linesDeleted: 2150,
    runs: 45,
    runsSucceeded: 38,
    agentHours: 18.4,
    repos: [
      {
        repo: "acme/api",
        tasksCompleted: 20,
        tasksFiled: 22,
        pullRequestsMerged: 15,
        pullRequestsMeasured: 15,
        linesAdded: 5000,
        linesDeleted: 1200,
        runs: 28,
      },
    ],
    directions: [
      {
        taskId: "88",
        title: "Auth v2 rollout",
        author: "Maya",
        repo: "acme/api",
        tasksCompleted: 9,
        tasksFiled: 10,
        tasksSpawned: 8,
        pullRequestsMerged: 7,
        linesAdded: 2100,
        linesDeleted: 400,
        runs: 12,
        runsSucceeded: 10,
        agentHours: 6.2,
      },
      {
        taskId: "89",
        title: "Zero-downtime deploy",
        author: "Zane",
        repo: "acme/web",
        tasksCompleted: 15,
        tasksFiled: 16,
        tasksSpawned: 12,
        pullRequestsMerged: 12,
        linesAdded: 4200,
        linesDeleted: 950,
        runs: 20,
        runsSucceeded: 18,
        agentHours: 8.5,
      },
    ],
  };

  config = () => Promise.resolve(this.configBody);

  getIfChanged = (route: string) => {
    if (route === "GET /api/tasks") {
      return Promise.resolve({
        body: structuredClone(this.tasksList),
        changed: true,
      });
    }
    return Promise.resolve({ body: null, changed: false });
  };

  get = (route: string, params?: unknown, query?: string) => {
    this.calls.push({ method: "GET", route, params, query });
    if (route === "GET /api/metrics") {
      return Promise.resolve(structuredClone(this.reportBody));
    }
    if (route === "GET /api/usage") {
      return Promise.resolve(structuredClone(this.usageBody));
    }
    if (route === "GET /api/release-notes/stats") {
      return Promise.resolve(structuredClone(this.clusterStatsBody));
    }
    if (route === "GET /api/release-notes") {
      return Promise.resolve([]);
    }
    return Promise.resolve(null);
  };

  put = (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ method: "PUT", route, params, body });
    if (route === "PUT /api/usage/settings") {
      const b = body as UsageResponse["settings"];
      this.usageBody = {
        ...this.usageBody,
        mode: b?.mode || "advisory",
        minWorkers: b?.minWorkers || 1,
        settings: structuredClone(b),
      };
      return Promise.resolve(structuredClone(this.usageBody));
    }
    return Promise.resolve({});
  };
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  f.detectChanges();
  await f.whenStable();
}

describe("TelemetryPageComponent", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  async function setup(api = new FakeTelemetryApi()) {
    TestBed.configureTestingModule({
      imports: [TelemetryPageComponent],
      providers: [
        provideRouter([{ path: "task/:id", children: [] }]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    TestBed.inject(AccessService).setConfig(api.configBody);
    await TestBed.inject(TaskFeedStore).refresh();
    const fixture = TestBed.createComponent(TelemetryPageComponent);
    fixture.autoDetectChanges();
    await settle(fixture);
    return { fixture, api, root: fixture.nativeElement as HTMLElement };
  }

  it("fetches GET /api/metrics (not /api/telemetry), renders KPI cards and 4 SVG charts, and has zero native <select> elements", async () => {
    const { root, api } = await setup();

    expect(root.querySelectorAll("select").length).toBe(0);
    expect(
      api.calls.some(
        (c) => c.route === "GET /api/metrics" && c.query === "window=7d",
      ),
    ).toBe(true);

    expect(
      root.querySelector('[data-testid="kpi-merged"]')?.textContent,
    ).toContain("12 merged");
    expect(
      root.querySelector('[data-testid="kpi-cycle"]')?.textContent,
    ).toContain("30m");
    expect(root.querySelector('[data-testid="kpi-ci"]')?.textContent).toContain(
      "90.0%",
    );
    expect(
      root.querySelector('[data-testid="kpi-merge-queue"]')?.textContent,
    ).toContain("80.0%");
    expect(
      root.querySelector('[data-testid="kpi-agents"]')?.textContent,
    ).toContain("3 / 6");

    expect(
      root.querySelector('[data-testid="chart-throughput"]'),
    ).not.toBeNull();
    expect(root.querySelector('[data-testid="chart-latency"]')).not.toBeNull();
    expect(root.querySelector('[data-testid="chart-outcomes"]')).not.toBeNull();
    expect(root.querySelector('[data-testid="chart-tools"]')).not.toBeNull();
  });

  it("shows a time axis and updates the hover card on pointer and arrow keys", async () => {
    const { fixture, root } = await setup();

    const ticks = [
      ...root.querySelectorAll(
        '[data-testid="chart-throughput"] .tel-axis-label',
      ),
    ].map((e) => e.textContent?.trim());
    expect(
      ticks.some((t) =>
        /^(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d+$/.test(
          t || "",
        ),
      ),
    ).toBe(true);
    expect(root.querySelector('[data-testid="tp-tip"]')).toBeNull();

    const hit1 = root.querySelector('[data-testid="tp-hit-1"]')!;
    hit1.dispatchEvent(new MouseEvent("mouseenter", { bubbles: false }));
    fixture.detectChanges();
    const tip = root.querySelector('[data-testid="tp-tip"]');
    expect(tip?.textContent).toContain("Filed");
    expect(tip?.textContent).toContain("10");
    expect(tip?.textContent).toContain("Completed");
    expect(tip?.textContent).toContain("8");
    expect(tip?.textContent).toContain("Merged");
    expect(tip?.textContent).toContain("7");
    expect(tip?.textContent).toContain("11");

    root
      .querySelector(".tel-chart-wrap")!
      .dispatchEvent(new MouseEvent("mouseleave", { bubbles: false }));
    fixture.detectChanges();
    expect(root.querySelector('[data-testid="tp-tip"]')).toBeNull();

    const chart = root.querySelector('[data-testid="chart-throughput"]')!;
    chart.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }),
    );
    fixture.detectChanges();
    expect(root.querySelector('[data-testid="tp-tip"]')?.textContent).toContain(
      "9",
    );
    chart.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    fixture.detectChanges();
    expect(root.querySelector('[data-testid="tp-tip"]')).toBeNull();
  });

  it("switches 24h / 7d / 30d windows and filters by WAI-ARIA GM3 repository popover", async () => {
    const { fixture, root, api } = await setup();

    root
      .querySelector<HTMLButtonElement>('[data-testid="telemetry-window-24h"]')!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) => c.route === "GET /api/metrics" && c.query === "window=24h",
      ),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>('[data-testid="telemetry-window-30d"]')!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) => c.route === "GET /api/metrics" && c.query === "window=30d",
      ),
    ).toBe(true);

    // Open GM3 repo popover, verify role="none" on <li> wrappers, and pick acme/api.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="telemetry-repo-trigger"]',
      )!
      .click();
    await settle(fixture);
    const repoMenu = root.querySelector('[data-testid="telemetry-repo-menu"]')!;
    expect(
      Array.from(repoMenu.querySelectorAll("li")).every(
        (li) => li.getAttribute("role") === "none",
      ),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="telemetry-repo-opt-acme/api"]',
      )!
      .click();
    await settle(fixture);

    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/metrics" &&
          c.query?.includes("window=30d") &&
          c.query?.includes("repo=acme%2Fapi"),
      ),
    ).toBe(true);
  });

  it("auto-selects first permitted repository and blocks unscoped reads when deployment:read is repo-limited", async () => {
    const api = new FakeTelemetryApi();
    api.configBody = {
      ...api.configBody,
      access: {
        role: "repo-viewer",
        roles: ["repo-viewer"],
        full: false,
        scopes: ["deployment:read"],
        repoLimits: { "deployment:read": ["acme/api"] },
      },
    };
    const { fixture, root } = await setup(api);

    expect(
      root.querySelector('[data-testid="telemetry-repo-trigger"]')?.textContent,
    ).toContain("Repo: acme/api");
    expect(
      api.calls.every(
        (c) =>
          c.route !== "GET /api/metrics" ||
          Boolean(c.query?.includes("repo=acme%2Fapi")),
      ),
    ).toBe(true);
    expect(api.calls.some((c) => c.route === "GET /api/usage")).toBe(false);

    // "All repositories" option in listbox is disabled for repo-limited callers.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="telemetry-repo-trigger"]',
      )!
      .click();
    await settle(fixture);
    const firstOpt = root.querySelector<HTMLButtonElement>(
      '[data-testid="telemetry-repo-menu"] button[role="option"]',
    )!;
    expect(firstOpt.disabled).toBe(true);
  });

  it("renders token usage scopes and saves budget settings via PUT /api/usage/settings", async () => {
    const { fixture, root, api } = await setup();

    const usageCard = root.querySelector('[data-testid="telemetry-usage"]')!;
    expect(usageCard.textContent).toContain("Claude Main");
    expect(usageCard.textContent).toContain("1.5M");

    root
      .querySelector<HTMLButtonElement>('[data-testid="usage-editor-toggle"]')!
      .click();
    await settle(fixture);

    expect(
      root.querySelector('[data-testid="usage-budget-editor"]'),
    ).not.toBeNull();

    root
      .querySelector<HTMLButtonElement>('[data-testid="usage-mode-auto"]')!
      .click();
    root
      .querySelector<HTMLButtonElement>('[data-testid="usage-add-budget"]')!
      .click();
    await settle(fixture);

    const connInput = root.querySelector<HTMLInputElement>(
      '[data-testid="budget-conn-1"]',
    )!;
    connInput.value = "gemini-pro";
    connInput.dispatchEvent(new Event("input"));

    const tokInput = root.querySelector<HTMLInputElement>(
      '[data-testid="budget-tokens-1"]',
    )!;
    tokInput.value = "2500000";
    tokInput.dispatchEvent(new Event("input"));
    await settle(fixture);

    root
      .querySelector<HTMLButtonElement>('[data-testid="usage-save"]')!
      .click();
    await settle(fixture);

    const putCall = api.calls.find(
      (c) => c.method === "PUT" && c.route === "PUT /api/usage/settings",
    );
    expect(putCall).toBeDefined();
    expect(putCall?.body).toEqual({
      mode: "auto",
      minWorkers: 1,
      retentionDays: 30,
      budgets: [
        { connection: "claude-main", tokens: 5_000_000, window: "5h" },
        { connection: "gemini-pro", tokens: 2_500_000, window: "5h" },
      ],
    });
  });

  it("disables budget editor when caller lacks settings:write scope", async () => {
    const api = new FakeTelemetryApi();
    api.configBody = {
      ...api.configBody,
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["deployment:read"],
      },
    };
    const { root } = await setup(api);

    const toggle = root.querySelector<HTMLButtonElement>(
      '[data-testid="usage-editor-toggle"]',
    )!;
    expect(toggle.disabled).toBe(true);
  });

  it("enforces SPACE guardrail: contributors render strictly alphabetically (A–Z) and never leaderboard-ranked", async () => {
    const { root } = await setup();

    expect(
      root.querySelector('[data-testid="space-guardrail-pill"]')?.textContent,
    ).toContain("Alphabetical · Non-ranked");

    const rows = Array.from(
      root.querySelectorAll<HTMLElement>('[data-testid="space-actor-row"]'),
    );
    // Even though Zane has more merged PRs (12) than Maya (7) and Alice (0),
    // order must be strictly alphabetical: Alice, Maya, Zane.
    const names = rows.map(
      (r) => r.querySelector(".actor-name")?.textContent?.trim() ?? "",
    );
    expect(names).toEqual(["Alice", "Maya", "Zane"]);
  });
});
