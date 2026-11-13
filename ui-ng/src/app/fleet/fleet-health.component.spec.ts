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
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { FleetHealthComponent } from "./fleet-health.component";
import {
  collectHealthAlerts,
  githubBudgetView,
  settingHref,
} from "./system-health";

const CONFIG = {
  access: {
    role: "grain/operator",
    roles: ["grain/operator"],
    full: true,
    scopes: [],
  },
  stateRepoResetNotice: "reset",
  installNotice: {
    message: "this install still runs in host mode",
    command: "grain migrate host-mode",
    affectedClis: ["mytool: run on host"],
    docsUrl: "javascript:alert(1)",
  },
  pipelinePause: { paused: true, reason: "maintenance" },
  reconcilerDown: true,
  dispatchBlocked: [
    { name: "git-proxy", error: "address in use", remedy: "free :7100" },
  ],
} as unknown as GrainConfig;

class FakeApi {
  queries: string[] = [];
  config = () => Promise.resolve(structuredClone(CONFIG));
  get = (route: string, _params?: unknown, query?: string) => {
    this.queries.push(`${route}?${query ?? ""}`);
    switch (route) {
      case "GET /api/settings":
        return Promise.resolve({
          pendingRestart: ["maxWorkers", "noSuchField"],
          unrunnable: [{ key: "pollInterval", stored: "1ms", running: "30s" }],
          sandboxCommitment: {
            budget: {
              mode: "auto",
              overcommit: {
                mode: "auto",
                cpuOvercommit: 2,
                memoryOvercommit: 1.5,
                oomKills: 1,
              },
            },
          },
        });
      case "GET /api/settings/cli-run":
        return Promise.resolve({
          hostExecutor: { hostd: { reachable: false, error: "dial refused" } },
        });
      case "GET /api/metrics":
        return Promise.resolve({
          github: {
            enabled: true,
            budget: { limit: 5000, remaining: 4200, low: false },
            polls: {
              tracked: 3,
              backedOff: 1,
              medianSeconds: 90,
              maxSeconds: 600,
            },
            callers: [
              {
                caller: "merge-queue",
                spent: 40,
                revalidated: 12,
                kinds: [{ kind: "pulls", spent: 30 }],
              },
            ],
          },
        });
      case "GET /api/sandboxes":
        return Promise.resolve({
          counters: [{ name: "Layer counters", counters: { b: 2, a: 1500 } }],
        });
      default:
        return Promise.resolve({});
    }
  };
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  f.detectChanges();
  await f.whenStable();
}

describe("FleetHealthComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("shows what the classic System → Health tab shows, from the same endpoints", async () => {
    const api = new FakeApi();
    TestBed.configureTestingModule({
      imports: [FleetHealthComponent],
      providers: [provideRouter([]), { provide: GrainApi, useValue: api }],
    });
    TestBed.inject(AccessService).setConfig(CONFIG);
    const fixture = TestBed.createComponent(FleetHealthComponent);
    fixture.autoDetectChanges();
    await settle(fixture);
    const root = fixture.nativeElement as HTMLElement;
    const text = (id: string) =>
      root.querySelector(`[data-testid="${id}"]`)?.textContent ?? "";

    expect(api.queries).toContain("GET /api/metrics?section=github");
    expect(api.queries).toContain("GET /api/sandboxes?section=counters");

    // Install notice, without a non-http docs link.
    expect(text("health-install-notice")).toContain(
      "This install still runs in host mode.",
    );
    expect(text("install-notice-command")).toBe("grain migrate host-mode");
    expect(
      root.querySelector('[data-testid="health-install-notice"] a'),
    ).toBeNull();

    expect(text("hostd-unreachable")).toContain("dial refused");
    expect(text("health-config-repo")).toContain(
      "A merged change reset settings",
    );

    // Saved but not applied: each field links to /settings/<section>#<id>.
    const pending = root.querySelector(
      '[data-testid="health-pending-restart"]',
    )!;
    const links = [...pending.querySelectorAll("a")].map((a) =>
      a.getAttribute("href"),
    );
    expect(links).toContain("/settings/engine#maxWorkers");
    expect(links).toContain("/fleet?tab=controls#fleet-power");
    expect(pending.textContent).toContain("noSuchField");
    const unrunnable = root.querySelector('[data-testid="health-unrunnable"]')!;
    expect(unrunnable.querySelector("a")?.getAttribute("href")).toBe(
      "/settings/engine#pollInterval",
    );
    expect(unrunnable.textContent).toContain("(stored 1ms, running 30s)");

    // Alerts, worst first, each with its link.
    const alerts = [...root.querySelectorAll('[data-testid="health-alert"]')];
    expect(alerts.map((a) => a.querySelector("h3")?.textContent)).toEqual([
      "Reconcile loop stopped",
      "Deployment paused",
    ]);
    expect(alerts[0].textContent).toContain("git proxy: address in use");
    expect(alerts[1].querySelector("a")?.getAttribute("href")).toBe(
      "/fleet?tab=controls",
    );

    expect(text("sandbox-overcommit-status")).toContain("Overcommit: auto");
    expect(text("sandbox-overcommit-status")).toContain("CPU 2.00×");
    expect(text("sandbox-overcommit-status")).toContain("1 OOM kill");
    expect(text("health-github-budget")).toContain(
      "4,200 of 5,000 calls left this hour",
    );
    expect(text("health-github-polls")).toContain(
      "1 of 3 tracked PRs backed off",
    );
    expect(text("health-github-callers")).toContain("merge-queue");
    const counters = text("health-counters-section");
    expect(counters.indexOf("a")).toBeLessThan(counters.indexOf("b"));
    expect(counters).toContain("1,500");
    fixture.destroy();
  });
});

describe("system-health helpers", () => {
  it("has no alerts for a quiet deployment", () => {
    expect(collectHealthAlerts({})).toEqual([]);
    expect(collectHealthAlerts(null)).toEqual([]);
  });

  it("links an unknown setting nowhere", () => {
    expect(settingHref("nope")).toBeNull();
  });

  it("says when the GitHub budget runs out before it resets", () => {
    const now = Date.parse("2026-11-13T12:00:00Z");
    const v = githubBudgetView(
      {
        enabled: true,
        budget: {
          limit: 5000,
          remaining: 100,
          low: true,
          lowFraction: 0.15,
          resetsAt: "2026-11-13T12:30:00Z",
          exhaustedAt: "2026-11-13T12:10:00Z",
        },
      },
      now,
    )!;
    expect(v.severity).toBe("critical");
    expect(v.summary).toContain("resets in 30m 0s");
    expect(v.runsOut).toBe(
      "At that rate it runs out in 10m 0s, before it resets.",
    );
    expect(v.low).toContain("Under 15% left");
    expect(githubBudgetView({ enabled: false })).toBeNull();
  });
});
