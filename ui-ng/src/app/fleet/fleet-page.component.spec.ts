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
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { FleetPageComponent } from "./fleet-page.component";
import {
  type ActionsCacheState,
  type AgentPauseResponse,
  type HostTopResponse,
  type HostdView,
  type LogLinesResponse,
  type LogSourcesResponse,
  type PackageCacheState,
  type PersistentSandboxStatus,
  type PipelinePauseStatus,
  type SandboxHealthResponse,
  extractTaskIdFromSandbox,
} from "./fleet.models";

interface RecordedCall {
  method: string;
  route: string;
  params?: unknown;
  query?: string;
  body?: unknown;
}

class FakeFleetApi {
  calls: RecordedCall[] = [];

  configBody: GrainConfig = {
    version: "v3.5.0-test",
    hostSandboxes: false,
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  healthBody: SandboxHealthResponse = {
    enabled: true,
    checkedAt: "2026-11-12T00:00:00Z",
    daemon: {
      startedAt: "2026-11-11T12:00:00Z",
      starts: 2,
      restarting: false,
      startsScope: "last 24h",
      logCommand: "journalctl --user -u grain -f",
    },
    dispatchBlocked: [
      {
        name: "git-proxy",
        error: "listen tcp :7100: address already in use",
        since: "2026-11-11T23:55:00Z",
        remedy: "restart the daemon once :7100 is free",
        held: "5m0s",
      },
    ],
    host: {
      cpus: 16,
      loadAverage1: 3.25,
      loadAverage5: 2.8,
      loadAverage15: 2.1,
      cpuUtilization: 0.42,
      memoryTotalMB: 65536,
      memoryUsedMB: 24576,
      memoryAvailableMB: 40960,
      swapTotalMB: 8192,
      swapUsedMB: 0,
      disks: [
        {
          path: "/var/lib/grain",
          holds: ["sandboxes", "caches"],
          totalMB: 512000,
          usedMB: 128000,
          availableMB: 384000,
          floorMB: 20480,
        },
      ],
    },
    budget: {
      known: true,
      budgetCPUs: 14,
      committedCPUs: 6,
      committedCount: 2,
      withinCap: true,
    },
    sandboxes: [
      {
        sandbox: "42-1",
        name: "42-1",
        kind: "agent",
        backend: "granule",
        ready: true,
        cpus: 4,
        memoryMB: 8192,
        memoryUsedMB: 2048,
        loadAverage: "0.85",
        startedAt: "2026-11-11T23:45:00Z",
        networkAccess: "isolated",
      },
      {
        sandbox: "idle-77",
        name: "idle-77",
        kind: "idle session",
        backend: "granule",
        ready: true,
        cpus: 2,
        memoryMB: 4096,
        startedAt: "2026-11-11T23:50:00Z",
      },
    ],
  };

  persistentBody: PersistentSandboxStatus[] = [
    {
      name: "staging-web",
      repo: "acme/web",
      ref: "main",
      command: "npm run preview",
      uiSubpath: "/staging/web",
      hostPort: 4173,
      enabled: true,
      state: "running",
      restarts: 0,
    },
  ];

  hostdsBody: HostdView[] = [
    {
      name: "local",
      state: "connected",
      hostname: "controller-01",
      os: "linux",
      arch: "amd64",
      version: "v3.5.0",
      clis: ["claude", "gemini"],
    },
    {
      name: "workstation-1",
      owner: "alice@example.com",
      state: "connected",
      hostname: "workstation.example.com",
      os: "linux",
      arch: "amd64",
      token_prefix: "ghd_99ab",
      clis: ["gemini"],
      logins: ["gcloud"],
    },
  ];

  pipelinePauseState: PipelinePauseStatus = { paused: false };
  agentPauseBody: AgentPauseResponse = {
    enabled: true,
    pause: {
      paused: true,
      pauses: [
        {
          connection: "claude-main",
          connectionName: "Claude Main",
          reason: "5h rate window",
          secondsRemaining: 900,
          canLift: true,
        },
      ],
    },
  };
  pkgCacheBody: PackageCacheState = {
    enabled: true,
    cache: [{ kind: "npm", entries: 142, bytes: 52_428_800 }],
  };
  actCacheBody: ActionsCacheState = {
    enabled: true,
    entries: 18,
    bytes: 104_857_600,
    maxBytes: 1_073_741_824,
  };
  logSourcesBody: LogSourcesResponse = {
    enabled: true,
    sources: ["daemon", "caddy"],
  };
  logLinesBySource: Record<string, LogLinesResponse> = {
    daemon: {
      source: "daemon",
      lines: [
        "2026-11-12T00:01:00Z INFO dispatcher: picked up task #42",
        "2026-11-12T00:01:05Z WARN merge-queue: waiting on CI check",
      ],
    },
    caddy: {
      source: "caddy",
      lines: ["2026-11-12T00:02:00Z INFO tls: certificate renewed"],
    },
  };
  hostTopBody: HostTopResponse = {
    enabled: true,
    lines: ["PID USER %CPU %MEM COMMAND", "1042 grain 18.2 2.1 grain serve"],
  };

  config = () => Promise.resolve(this.configBody);
  getIfChanged = () => Promise.resolve({ body: [], changed: false });

  getText = (
    route: string,
    params?: Record<string, string>,
    query?: string,
  ) => {
    this.calls.push({ method: "GET_TEXT", route, params, query });
    if (route === "GET /api/persistent-sandboxes/{name}/logs") {
      return Promise.resolve(`[${params?.["name"]}] listening on :4173`);
    }
    return Promise.resolve("");
  };

  get = (route: string, params?: Record<string, string>, query?: string) => {
    this.calls.push({ method: "GET", route, params, query });
    switch (route) {
      case "GET /api/sandboxes":
        return Promise.resolve(structuredClone(this.healthBody));
      case "GET /api/persistent-sandboxes":
        return Promise.resolve(structuredClone(this.persistentBody));
      case "GET /api/host/top":
        return Promise.resolve(structuredClone(this.hostTopBody));
      case "GET /api/hostds":
        return Promise.resolve(structuredClone(this.hostdsBody));
      case "GET /api/pipeline-pause":
        return Promise.resolve({
          pause: structuredClone(this.pipelinePauseState),
        });
      case "GET /api/pause":
        return Promise.resolve(structuredClone(this.agentPauseBody));
      case "GET /api/package-cache":
        return Promise.resolve(structuredClone(this.pkgCacheBody));
      case "GET /api/actions-cache":
        return Promise.resolve(structuredClone(this.actCacheBody));
      case "GET /api/logs":
        return Promise.resolve(structuredClone(this.logSourcesBody));
      case "GET /api/logs/{source}": {
        const src = params?.["source"] ?? "daemon";
        return Promise.resolve(
          structuredClone(
            this.logLinesBySource[src] ?? { source: src, lines: [] },
          ),
        );
      }
      case "GET /api/restart-watch":
        return Promise.resolve({ restarting: false });
      default:
        return Promise.resolve({});
    }
  };

  post = (
    route: string,
    params?: Record<string, string>,
    body?: unknown,
    query?: string,
  ) => {
    this.calls.push({ method: "POST", route, params, body, query });
    if (route === "POST /api/sandbox-probe") {
      return Promise.resolve({
        id: "probe-1",
        sandbox: "sb-probe-1",
        pod: "local",
        exitCode: 0,
        stdout: "Linux granule 6.8.0 x86_64\n",
        stderr: "",
      });
    }
    if (route === "POST /api/hostds/{name}/rotate") {
      const name = params?.["name"] ?? "host";
      return Promise.resolve({
        hostd: { name, state: "connected" },
        token: "ghd_secret_rotated_token",
      });
    }
    if (route === "POST /api/pipeline-pause") {
      const reason = (body as { reason?: string })?.reason || "maintenance";
      this.pipelinePauseState = {
        paused: true,
        reason,
        byLabel: "Alice Example",
        byKind: "human",
      };
      return Promise.resolve({
        pause: structuredClone(this.pipelinePauseState),
      });
    }
    if (route === "POST /api/package-cache/flush") {
      this.pkgCacheBody = { enabled: true, cache: [] };
      return Promise.resolve({ entries: 142, bytes: 52_428_800, cache: [] });
    }
    if (route === "POST /api/actions-cache/flush") {
      this.actCacheBody = {
        enabled: true,
        entries: 0,
        bytes: 0,
        maxBytes: 1_073_741_824,
      };
      return Promise.resolve({
        entries: 18,
        bytes: 104_857_600,
        cache: structuredClone(this.actCacheBody),
      });
    }
    return Promise.resolve({ ok: true });
  };

  delete = (route: string, params?: Record<string, string>, query?: string) => {
    this.calls.push({ method: "DELETE", route, params, query });
    if (route === "DELETE /api/pipeline-pause") {
      this.pipelinePauseState = { paused: false };
      return Promise.resolve({ pause: { paused: false } });
    }
    if (route === "DELETE /api/pause") {
      this.agentPauseBody = { pause: { paused: false, pauses: [] } };
      return Promise.resolve(structuredClone(this.agentPauseBody));
    }
    return Promise.resolve({ ok: true });
  };
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  f.detectChanges();
  await f.whenStable();
}

describe("FleetPageComponent", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  async function setup(api = new FakeFleetApi()) {
    TestBed.configureTestingModule({
      imports: [FleetPageComponent],
      providers: [
        provideRouter([
          { path: "task/:id", children: [] },
          { path: "settings/hostds", children: [] },
        ]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    TestBed.inject(AccessService).setConfig(api.configBody);
    const fixture = TestBed.createComponent(FleetPageComponent);
    fixture.autoDetectChanges();
    await settle(fixture);
    return { fixture, api, root: fixture.nativeElement as HTMLElement };
  }

  it("renders DispatchCondition error/remedy, capacity meters, <taskId>-<attempt> & idle-<taskId> task links, probe, and persistent ?format=text logs", async () => {
    const { fixture, root, api } = await setup();

    expect(root.querySelectorAll("select").length).toBe(0);
    expect(
      root.querySelector('[data-testid="fleet-backend-pill"]')?.textContent,
    ).toContain("Backend: granule");

    // DispatchCondition renders error + remedy + since.
    const blockedBanner = root.querySelector(
      '[data-testid="fleet-dispatch-blocked"]',
    )!;
    expect(blockedBanner.textContent).toContain(
      "listen tcp :7100: address already in use",
    );
    expect(blockedBanner.textContent).toContain(
      "Remedy: restart the daemon once :7100 is free",
    );

    expect(
      root.querySelector('[data-testid="meter-cpu"]')?.textContent,
    ).toContain("42%");
    expect(
      root.querySelector('[data-testid="meter-vcpu"]')?.textContent,
    ).toContain("6 / 14 vCPU");

    // Live agent sandbox "42-1" links to singular /task/42; idle session "idle-77" links to /task/77.
    const sbRow = root.querySelector('[data-testid="sandbox-row-42-1"]')!;
    expect(sbRow.querySelector("a")?.getAttribute("href")).toBe("/task/42");
    const idleRow = root.querySelector('[data-testid="sandbox-row-idle-77"]')!;
    expect(idleRow.querySelector("a")?.getAttribute("href")).toBe("/task/77");

    expect(
      extractTaskIdFromSandbox({
        sandbox: "task-9",
        name: "task-9",
        kind: "task",
        backend: "granule",
        ready: true,
      }),
    ).toBe("9");
    expect(
      extractTaskIdFromSandbox({
        sandbox: "10-2",
        name: "10-2",
        kind: "persistent",
        backend: "granule",
        ready: true,
      }),
    ).toBeNull();

    root
      .querySelector<HTMLButtonElement>('[data-testid="sandbox-recreate-42"]')!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route === "POST /api/tasks/{id}/sandbox/recreate",
      ),
    ).toBe(true);

    // Toggle Host top process snapshot.
    root
      .querySelector<HTMLButtonElement>('[data-testid="fleet-top-toggle"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="fleet-host-top"]')?.textContent,
    ).toContain("grain serve");

    // Run diagnostic sandbox probe.
    root
      .querySelector<HTMLButtonElement>('[data-testid="probe-run-btn"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="probe-output"]')?.textContent,
    ).toContain("Linux granule 6.8.0");

    // Persistent sandbox restart + ?format=text logs viewer.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="persistent-restart-staging-web"]',
      )!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route === "POST /api/persistent-sandboxes/{name}/{action}" &&
          (c.params as { name: string; action: string }).name ===
            "staging-web" &&
          (c.params as { name: string; action: string }).action === "restart",
      ),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="persistent-logs-btn-staging-web"]',
      )!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "GET_TEXT" &&
          c.route === "GET /api/persistent-sandboxes/{name}/logs" &&
          c.query === "format=text",
      ),
    ).toBe(true);
    expect(
      root.querySelector('[data-testid="persistent-logs-staging-web"]')
        ?.textContent,
    ).toContain("listening on :4173");
  });

  it("disables sandbox probe when hostSandboxes=true and gates persistent Logs + Fleet tabs on RBAC scopes", async () => {
    const api = new FakeFleetApi();
    api.configBody = {
      version: "v3.5.0-test",
      hostSandboxes: true,
      access: {
        role: "sandbox-viewer",
        roles: ["sandbox-viewer"],
        full: false,
        scopes: ["sandboxes:read", "deployment:restart"],
      },
    };
    const { fixture, root } = await setup(api);

    expect(
      root.querySelector('[data-testid="fleet-backend-pill"]')?.textContent,
    ).toContain("Backend: host");
    // Probe disabled when hostSandboxes is true even with deployment:restart.
    expect(
      root.querySelector<HTMLButtonElement>('[data-testid="probe-run-btn"]')!
        .disabled,
    ).toBe(true);
    // Live sandboxes card shows access.deniedReason("GET /api/sandboxes").
    expect(
      root.querySelector('[data-testid="fleet-sandboxes-denied"]')?.textContent,
    ).toContain("deployment:read");
    // Persistent sandbox Logs button disabled because deployment:read is missing.
    expect(
      root.querySelector<HTMLButtonElement>(
        '[data-testid="persistent-logs-btn-staging-web"]',
      )!.disabled,
    ).toBe(true);

    // Controls tab shows deniedReason when dispatch:read & caches:read are missing, except restart is held;
    // test missing all controls read scopes when deployment:restart is also absent:
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "minimal",
        roles: ["minimal"],
        full: false,
        scopes: [],
      },
    });
    fixture.detectChanges();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="fleet-sandboxes-denied"]')?.textContent,
    ).toContain("deployment:read");

    root
      .querySelector<HTMLButtonElement>('[data-testid="fleet-tab-controls"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="fleet-controls-denied"]')?.textContent,
    ).toContain("dispatch:read");

    root
      .querySelector<HTMLButtonElement>('[data-testid="fleet-tab-logs"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="fleet-logs-denied"]')?.textContent,
    ).toContain("deployment:read");
  });

  it("manages hostds, preserves routerLink='/settings/hostds', and mounts confirm dialog outside [attr.inert]", async () => {
    const { fixture, root, api } = await setup();

    const sbTab = root.querySelector<HTMLButtonElement>(
      '[data-testid="fleet-tab-sandboxes"]',
    )!;
    expect(sbTab.getAttribute("aria-controls")).toBe("fleet-panel-sandboxes");
    expect(root.querySelector('[role="tabpanel"]')?.id).toBe(
      "fleet-panel-sandboxes",
    );

    // ArrowRight navigates from Sandboxes to Host Daemons tab.
    sbTab.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    await settle(fixture);
    expect(root.querySelector('[role="tabpanel"]')?.id).toBe(
      "fleet-panel-hostds",
    );

    expect(
      root.querySelector('a[href="/settings/hostds"]')?.textContent,
    ).toContain("Enrollment & settings");
    expect(
      root.querySelector('[data-testid="hostd-row-local"]')?.textContent,
    ).toContain("co-located");
    expect(
      root.querySelector('[data-testid="hostd-row-workstation-1"]')
        ?.textContent,
    ).toContain("alice@example.com");

    // Rotate token shows one-time token banner.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="hostd-rotate-workstation-1"]',
      )!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="hostd-issued-token"]')?.textContent,
    ).toContain("ghd_secret_rotated_token");

    // Revoke requires ConfirmDialogComponent acceptance OUTSIDE [attr.inert].
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="hostd-revoke-workstation-1"]',
      )!
      .click();
    await settle(fixture);
    const dialog = root.querySelector('[data-testid="confirm-dialog"]')!;
    expect(dialog).not.toBeNull();
    expect(dialog.closest("[inert]")).toBeNull();

    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await settle(fixture);
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route === "POST /api/hostds/{name}/revoke" &&
          (c.params as { name: string }).name === "workstation-1",
      ),
    ).toBe(true);
  });

  it("toggles pipeline pause, flushes caches with confirmation, and confirms daemon restart", async () => {
    const { fixture, root, api } = await setup();

    root
      .querySelector<HTMLButtonElement>('[data-testid="fleet-tab-controls"]')!
      .click();
    await settle(fixture);

    // Pause pipeline with reason, then resume.
    const reasonInput = root.querySelector<HTMLInputElement>(
      '[data-testid="pipeline-pause-reason"]',
    )!;
    reasonInput.value = "Deploying M12";
    reasonInput.dispatchEvent(new Event("input"));
    root
      .querySelector<HTMLButtonElement>('[data-testid="pipeline-pause-btn"]')!
      .click();
    await settle(fixture);

    expect(
      root.querySelector('[data-testid="pipeline-pause-state"]')?.textContent,
    ).toContain("Paused");
    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.route === "POST /api/pipeline-pause" &&
          (c.body as { reason: string }).reason === "Deploying M12",
      ),
    ).toBe(true);

    root
      .querySelector<HTMLButtonElement>('[data-testid="pipeline-resume-btn"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="pipeline-pause-state"]')?.textContent,
    ).toContain("Dispatching");

    // Flush package cache via confirmation dialog.
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="flush-package-cache-btn"]',
      )!
      .click();
    await settle(fixture);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await settle(fixture);
    expect(
      api.calls.some((c) => c.route === "POST /api/package-cache/flush"),
    ).toBe(true);

    // Restart daemon requires confirmation dialog.
    root
      .querySelector<HTMLButtonElement>('[data-testid="daemon-restart-btn"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("Restart grain daemon?");
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await settle(fixture);
    expect(api.calls.some((c) => c.route === "POST /api/host/restart")).toBe(
      true,
    );
  });

  it("streams daemon logs, switches log sources, and filters lines in the dark viewer", async () => {
    const { fixture, root } = await setup();

    root
      .querySelector<HTMLButtonElement>('[data-testid="fleet-tab-logs"]')!
      .click();
    await settle(fixture);

    const viewer = root.querySelector('[data-testid="logs-viewer"]')!;
    expect(viewer.textContent).toContain("picked up task #42");
    expect(
      root.querySelector('[data-testid="logs-line-count"]')?.textContent,
    ).toContain("2 / 2 lines");

    // Filter lines locally.
    const search = root.querySelector<HTMLInputElement>(
      '[data-testid="logs-search-input"]',
    )!;
    search.value = "merge-queue";
    search.dispatchEvent(new Event("input"));
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="logs-line-count"]')?.textContent,
    ).toContain("1 / 2 lines");

    // Clear filter and switch source to caddy.
    search.value = "";
    search.dispatchEvent(new Event("input"));
    root
      .querySelector<HTMLButtonElement>('[data-testid="log-source-caddy"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="logs-viewer"]')?.textContent,
    ).toContain("certificate renewed");
  });
});
