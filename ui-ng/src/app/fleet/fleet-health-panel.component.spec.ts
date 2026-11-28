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
import { Subject } from "rxjs";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { type GrainEvent, GrainEvents } from "../api/grain-events.service";
import type { GrainConfig, Notification, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import type { ConfirmRequest } from "../shared/confirm-dialog.component";
import {
  BUDGET_REFRESH_MS,
  COUNTERS_REFRESH_MS,
  FleetHealthPanelComponent,
} from "./fleet-health-panel.component";

interface Call {
  method: string;
  route: string;
  params?: Record<string, string>;
  query?: string;
  body?: unknown;
}

class FakeHealthApi {
  calls: Call[] = [];
  configBody: GrainConfig = {
    version: "v3.14.0-test",
    timeZone: "UTC",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };
  settingsBody: Record<string, unknown> = {
    pendingRestart: ["pollInterval", "githubHost"],
    unrunnable: [
      { key: "sandboxBackend", stored: "bogus", running: "container" },
    ],
  };
  cliRunBody: Record<string, unknown> = {
    hostExecutor: {
      hostd: {
        reachable: true,
        missing: [
          {
            name: "grain-workspace",
            command: "sudo systemctl start grain-hostd-allowlist.service",
            entry: "  grain-workspace:\n    command: /usr/bin/grain-ws\n",
          },
        ],
      },
    },
  };
  metricsGitHubBody: Record<string, unknown> = {
    github: {
      enabled: true,
      budget: {
        limit: 5000,
        remaining: 1117,
        resetsAt: new Date(Date.now() + 40 * 60_000).toISOString(),
        spendPerHour: 2234,
        exhaustedAt: new Date(Date.now() + 30 * 60_000).toISOString(),
      },
      polls: {
        tracked: 3,
        backedOff: 2,
        baseSeconds: 30,
        capSeconds: 480,
        medianSeconds: 120,
        maxSeconds: 480,
      },
      callers: [
        {
          caller: "reconciler",
          spent: 840,
          revalidated: 1920,
          kinds: [{ kind: "PR status", spent: 510 }],
        },
      ],
    },
  };
  countersBody: Record<string, unknown> = {
    counters: [
      {
        name: "sign-in verification (demo)",
        counters: { "proxy:refused": 2, "proxy:admitted": 1200 },
      },
    ],
  };
  staleBody: Record<string, unknown> = { staleSchedules: [] };
  notificationsBody: Notification[] = [
    {
      id: 7,
      subject: "release.pending:v2.4.0",
      severity: "warning",
      actions: ["approve-release"],
      repo: "acme/widgets",
      body: "v2.4.0 passed qualification.",
    },
    {
      id: 8,
      subject: "merge.stalled:1",
      title: "A merge has been stalled for 2 hours",
      severity: "warning",
      taskId: "1",
      body: "Pull request #1 has stood in recheck.",
    },
  ];
  tasksBody: Task[] = [];

  failNextMetrics = false;
  failNextCounters = false;

  clearConditionalCache = vi.fn();
  config = () => Promise.resolve(structuredClone(this.configBody));
  getIfChanged = () =>
    Promise.resolve({ body: structuredClone(this.tasksBody), changed: true });

  get = (route: string, params?: Record<string, string>, query?: string) => {
    this.calls.push({ method: "GET", route, params, query });
    if (route === "GET /api/settings") {
      return Promise.resolve(structuredClone(this.settingsBody));
    }
    if (route === "GET /api/settings/cli-run") {
      return Promise.resolve(structuredClone(this.cliRunBody));
    }
    if (route === "GET /api/metrics" && query === "section=github") {
      if (this.failNextMetrics) {
        this.failNextMetrics = false;
        return Promise.reject(new Error("offline"));
      }
      return Promise.resolve(structuredClone(this.metricsGitHubBody));
    }
    if (route === "GET /api/sandboxes" && query === "section=counters") {
      if (this.failNextCounters) {
        this.failNextCounters = false;
        return Promise.reject(new Error("offline"));
      }
      return Promise.resolve(structuredClone(this.countersBody));
    }
    if (route === "GET /api/sandboxes" && query === "section=staleSchedules") {
      return Promise.resolve(structuredClone(this.staleBody));
    }
    if (route === "GET /api/notifications") {
      return Promise.resolve(structuredClone(this.notificationsBody));
    }
    if (route === "GET /api/usage") {
      return Promise.resolve({ scopes: [] });
    }
    return Promise.resolve({});
  };

  post = (
    route: string,
    params?: Record<string, string>,
    body?: unknown,
    query?: string,
  ) => {
    this.calls.push({ method: "POST", route, params, body, query });
    if (route === "POST /api/notifications/{id}/ack") {
      const id = Number(params?.["id"]);
      this.notificationsBody = this.notificationsBody.filter(
        (n) => n.id !== id,
      );
      return Promise.resolve({ ok: true });
    }
    return Promise.resolve({ ok: true });
  };

  delete = (route: string, params?: Record<string, string>, query?: string) => {
    this.calls.push({ method: "DELETE", route, params, query });
    if (route === "DELETE /api/pause") {
      this.configBody = {
        ...this.configBody,
        agentPause: { paused: false, pauses: [] },
      };
    }
    return Promise.resolve({ ok: true });
  };
}

async function settle(fixture: ComponentFixture<unknown>): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  fixture.detectChanges();
  await fixture.whenStable();
  fixture.detectChanges();
}

describe("FleetHealthPanelComponent", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  async function render(api = new FakeHealthApi()) {
    const events$ = new Subject<GrainEvent>();
    await TestBed.configureTestingModule({
      imports: [FleetHealthPanelComponent],
      providers: [
        provideRouter([]),
        { provide: GrainApi, useValue: api },
        {
          provide: GrainEvents,
          useValue: { events: () => events$.asObservable() },
        },
      ],
    }).compileComponents();

    const access = TestBed.inject(AccessService);
    access.setConfig(api.configBody);

    const fixture = TestBed.createComponent(FleetHealthPanelComponent);
    const confirms: ConfirmRequest[] = [];
    let openedControls = 0;
    fixture.componentInstance.confirm.subscribe((c) => confirms.push(c));
    fixture.componentInstance.openControls.subscribe(() => openedControls++);
    fixture.detectChanges();
    await settle(fixture);
    return {
      fixture,
      root: fixture.nativeElement as HTMLElement,
      api,
      confirms,
      openedControls: () => openedControls,
    };
  }

  it("renders settings links, host readiness notes, GitHub budget, counters, and standing alerts", async () => {
    const api = new FakeHealthApi();
    api.configBody = {
      ...api.configBody,
      installNotice: {
        kind: "host-mode",
        message: "host-mode install has not been migrated",
        command: "sudo ./scripts/setup.sh",
      },
      agentPause: {
        paused: true,
        pauses: [
          {
            connection: "antigravity",
            connectionName: "Antigravity",
            model: "gemini-2.5-pro",
            until: new Date(Date.now() + 3600_000).toISOString(),
            secondsRemaining: 3600,
            canLift: true,
          },
        ],
      },
    };
    const { fixture, root, confirms, openedControls } = await render(api);

    expect(
      root.querySelector('[data-testid="health-install-notice"]')?.textContent,
    ).toContain("sudo ./scripts/setup.sh");

    expect(
      root.querySelector('[data-testid="hostd-health-missing"]')?.textContent,
    ).toContain("grain-workspace");

    // Pending-restart and unrunnable links point to /settings/<section>#<field>.
    const pollLink = root.querySelector<HTMLAnchorElement>(
      '[data-testid="pending-setting-link-pollInterval"]',
    )!;
    expect(pollLink.getAttribute("href")).toBe("/settings/engine#pollInterval");
    const ghHostLink = root.querySelector<HTMLAnchorElement>(
      '[data-testid="pending-setting-link-githubHost"]',
    )!;
    expect(ghHostLink.getAttribute("href")).toBe(
      "/settings/capabilities#githubHost",
    );
    const sbLink = root.querySelector<HTMLAnchorElement>(
      '[data-testid="unrunnable-setting-link-sandboxBackend"]',
    )!;
    expect(sbLink.getAttribute("href")).toBe(
      "/settings/sandboxes#sandboxBackend",
    );
    expect(
      root.querySelector('[data-testid="health-unrunnable"]')?.textContent,
    ).toContain("(stored bogus, running container)");

    root
      .querySelector<HTMLButtonElement>('[data-testid="health-open-controls"]')!
      .click();
    expect(openedControls()).toBe(1);

    // GitHub API budget, exhaustion projection, backoff, and callers.
    expect(
      root.querySelector('[data-testid="health-github-budget"]')?.textContent,
    ).toContain("1,117 of 5,000 calls left this hour");
    expect(
      root.querySelector('[data-testid="health-github-runs-out"]')?.textContent,
    ).toContain("runs out in");
    expect(
      root.querySelector('[data-testid="health-github-polls"]')?.textContent,
    ).toContain("Idle-poll backoff: 2 of 3 tracked PRs backed off");
    expect(
      root.querySelector('[data-testid="health-github-callers"]')?.textContent,
    ).toContain("reconciler");

    // Registered daemon health counters sorted alphabetically.
    const counterRows = Array.from(
      root.querySelectorAll('[data-testid="health-counters-section"] tbody tr'),
    ).map((tr) => tr.textContent?.trim());
    expect(counterRows[0]).toContain("proxy:admitted");
    expect(counterRows[1]).toContain("proxy:refused");

    // Standing alerts: usage limit, pending release, stalled merge.
    const alertRows = root.querySelectorAll('[data-testid="alert-row"]');
    expect(alertRows.length).toBe(3);

    // Approve release emits a ConfirmRequest and posts on accept.
    const approveBtn = Array.from(
      root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="health-alerts-card"] button',
      ),
    ).find((b) => b.textContent?.trim() === "Approve")!;
    approveBtn.click();
    expect(confirms.length).toBe(1);
    expect(confirms[0].title).toBe("Approve v2.4.0");
    await confirms[0].run();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route ===
            "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
      ),
    ).toBe(true);

    // Acknowledge removes the stalled-merge alert.
    const ackBtns = Array.from(
      root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="health-alerts-card"] button',
      ),
    ).filter((b) => b.textContent?.trim() === "Acknowledge");
    ackBtns[1].click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route === "POST /api/notifications/{id}/ack" &&
          c.params?.["id"] === "8",
      ),
    ).toBe(true);

    // Resume now lifts the usage-limit pause via DELETE /api/pause.
    const resumeBtn = Array.from(
      root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="health-alerts-card"] button',
      ),
    ).find((b) => b.textContent?.trim() === "Resume now")!;
    resumeBtn.click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "DELETE" &&
          c.route === "DELETE /api/pause" &&
          c.query === "connection=antigravity&model=gemini-2.5-pro",
      ),
    ).toBe(true);
  });

  it("polls GitHub budget and counters in the background and preserves last figures across failures", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = new FakeHealthApi();
    const { fixture, root } = await render(api);

    expect(
      root.querySelector('[data-testid="health-counters"]')?.textContent,
    ).toContain("1,200");

    api.failNextMetrics = true;
    api.failNextCounters = true;
    await vi.advanceTimersByTimeAsync(
      Math.max(BUDGET_REFRESH_MS, COUNTERS_REFRESH_MS),
    );
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="health-github-budget"]')?.textContent,
    ).toContain("1,117 of 5,000");
    expect(
      root.querySelector('[data-testid="health-counters"]')?.textContent,
    ).toContain("1,200");

    api.countersBody = {
      counters: [
        {
          name: "sign-in verification (demo)",
          counters: { "proxy:admitted": 1205 },
        },
      ],
    };
    await vi.advanceTimersByTimeAsync(COUNTERS_REFRESH_MS);
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="health-counters"]')?.textContent,
    ).toContain("1,205");
  });

  it("preserves 3-segment host/owner/name slugs on release approval and suppresses non-http installNotice.docsUrl", async () => {
    const api = new FakeHealthApi();
    api.notificationsBody = [
      {
        id: 21,
        subject: "release.pending:v3.0.0",
        severity: "warning",
        actions: ["approve-release"],
        repo: "git.forge.example/experimental/grain",
        body: "v3.0.0 passed qualification.",
      },
    ];
    api.configBody = {
      ...api.configBody,
      installNotice: {
        kind: "host-mode",
        message: "legacy install detected",
        docsUrl: "javascript:alert(1)",
      },
    };
    const { fixture, root, confirms } = await render(api);

    const notice = root.querySelector('[data-testid="health-install-notice"]')!;
    expect(notice.querySelector("a")).toBeNull();

    const approveBtn = Array.from(
      root.querySelectorAll<HTMLButtonElement>(
        '[data-testid="health-alerts-card"] button',
      ),
    ).find((b) => b.textContent?.trim() === "Approve")!;
    approveBtn.click();
    expect(confirms.length).toBe(1);
    await confirms[0].run();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route ===
            "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve" &&
          c.params?.["owner"] === "git.forge.example/experimental" &&
          c.params?.["name"] === "grain" &&
          c.params?.["version"] === "v3.0.0",
      ),
    ).toBe(true);
  });
});
