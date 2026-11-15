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

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import { provideRouter, withComponentInputBinding } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { SettingsPageComponent } from "./settings-page.component";
import { safeManagementUrl } from "./settings-instance.component";
import { capServerState } from "./settings-work.component";

interface RecordedCall {
  method: "GET" | "TEXT" | "POST" | "PUT" | "PATCH" | "DELETE";
  key: string;
  params?: RouteParams;
  body?: unknown;
  query?: string;
}

class FakeSettingsApi {
  calls: RecordedCall[] = [];

  configBody: GrainConfig & Record<string, unknown> = {
    version: "v3.6.0",
    configRepoOnly: false,
    targetRepos: ["acme/platform"],
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  hostdsBody: unknown[] = [
    { name: "workstation-1", owner: "alice", status: "connected" },
  ];

  upgradeBody: Record<string, unknown> = {
    currentVersion: "v3.6.0",
    phase: "downloading",
    cancellable: true,
  };

  settingsBody: Record<string, unknown> = {
    environmentName: "Staging",
    pollInterval: "30s",
    maxWorkers: 4,
    approvedByDefault: false,
    autoMergeByDefault: false,
    externalAgentApi: true,
    sandboxBackend: "granule",
  };

  deploymentBody: Record<string, unknown> = {
    defaultModel: "gemini-2.5-pro",
    targetRepos: ["acme/platform"],
    agentBackends: [{ id: "vertex-main", framework: "antigravity" }],
  };

  config = () => Promise.resolve(structuredClone(this.configBody));

  get = (key: string, params?: RouteParams, query?: string) => {
    this.calls.push({ method: "GET", key, params, query });
    if (key === "GET /api/settings") {
      return Promise.resolve(structuredClone(this.settingsBody));
    }
    if (key === "GET /api/deployment") {
      return Promise.resolve(structuredClone(this.deploymentBody));
    }
    if (key === "GET /api/access/principals") {
      return Promise.resolve([
        { entry: "operator@example.com", role: "grain/operator" },
      ]);
    }
    if (key === "GET /api/external-agents/tokens") {
      return Promise.resolve([
        { id: "tok-1", name: "ci-runner", scopeSet: "ci", revoked: false },
      ]);
    }
    if (key === "GET /api/capability-servers") {
      return Promise.resolve([
        {
          id: "github-mcp",
          enabled: true,
          transport: "http",
          url: "https://mcp.internal/gh",
        },
      ]);
    }
    if (key === "GET /api/capability-policy") {
      return Promise.resolve({
        policy: { rules: [{ capability: "repo:write" }] },
      });
    }
    if (key === "GET /api/state-repo") {
      return Promise.resolve({
        branch: "main",
        head: "abcdef123456",
        remote: "https://github.com/acme/grain-config.git",
      });
    }
    if (key === "GET /api/state-repo/files") {
      return Promise.resolve(["tables/grain_config.json"]);
    }
    if (key === "GET /api/upgrade") {
      return Promise.resolve(this.upgradeBody);
    }
    if (key === "GET /api/upgrade/targets") {
      return Promise.resolve([
        { branch: "stable", version: "v3.7.0" },
        { branch: "main", version: "v3.8.0-rc1" },
      ]);
    }
    if (key === "GET /api/upgrade/release-notes") {
      return Promise.resolve("## v3.7.0\n- Faster microVM boot");
    }
    if (key === "GET /api/hostds") {
      return Promise.resolve(this.hostdsBody);
    }
    if (key === "GET /api/hostds/enrollment-codes") {
      return Promise.resolve([{ name: "eng-laptops", uses: 2, max_uses: 10 }]);
    }
    if (key === "GET /api/hostds/pending") {
      return Promise.resolve([
        {
          id: "pend-1",
          hostname: "devbox-2",
          owner: "alice",
          os: "linux",
          arch: "amd64",
        },
      ]);
    }
    if (key === "GET /api/deployment/reachable-repos") {
      return Promise.resolve({
        repos: [{ repo: "acme/alpha", push: true }],
        errors: ["enterprise host returned HTML"],
        hostMismatches: ["ghe.internal mismatch"],
        reason: "1 repository reachable",
      });
    }
    return Promise.resolve({});
  };

  getText = (key: string, params?: RouteParams, query?: string) => {
    this.calls.push({ method: "TEXT", key, params, query });
    return Promise.resolve('{"environmentName":"Staging"}');
  };

  post = (
    key: string,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ) => {
    this.calls.push({ method: "POST", key, params, body, query });
    if (key === "POST /api/settings/plan") {
      const b = (body ?? {}) as Record<string, unknown>;
      return Promise.resolve({
        ...this.settingsBody,
        ...b,
        ...(b["pollInterval"] === "1m" ? { pollInterval: "60s" } : {}),
      });
    }
    if (key === "POST /api/settings") {
      this.settingsBody = {
        ...this.settingsBody,
        ...((body ?? {}) as Record<string, unknown>),
      };
      return Promise.resolve(structuredClone(this.settingsBody));
    }
    if (key === "POST /api/external-agents/tokens") {
      return Promise.resolve({ token: "gma_live_secret_token_999" });
    }
    if (key === "POST /api/capability-servers/test") {
      return Promise.resolve({ ok: true, detail: "3 tools advertised" });
    }
    if (key === "POST /api/capability-policy/preview") {
      return Promise.resolve({ denied: [] });
    }
    return Promise.resolve({ ok: true });
  };

  put = (key: string, params?: RouteParams, body?: unknown, query?: string) => {
    this.calls.push({ method: "PUT", key, params, body, query });
    if (key === "PUT /api/deployment") {
      this.deploymentBody = {
        ...this.deploymentBody,
        ...((body ?? {}) as Record<string, unknown>),
      };
      return Promise.resolve(structuredClone(this.deploymentBody));
    }
    return Promise.resolve({ ok: true });
  };

  patch = (
    key: string,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ) => {
    this.calls.push({ method: "PATCH", key, params, body, query });
    return Promise.resolve({ ok: true });
  };

  delete = (
    key: string,
    params?: RouteParams,
    query?: string,
    body?: unknown,
  ) => {
    this.calls.push({ method: "DELETE", key, params, query, body });
    return Promise.resolve({ ok: true });
  };
}

async function mountSettings(url: string, api = new FakeSettingsApi()) {
  localStorage.clear();
  TestBed.configureTestingModule({
    providers: [
      provideRouter(
        [
          { path: "settings", component: SettingsPageComponent },
          { path: "settings/:section", component: SettingsPageComponent },
          { path: "repos/:owner/:name", children: [] },
          { path: "repos/:owner/:name/:tab", children: [] },
        ],
        withComponentInputBinding(),
      ),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
    ],
  });
  const harness = await RouterTestingHarness.create(url);
  harness.fixture.autoDetectChanges(true);
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 20));
  await harness.fixture.whenStable();
  return {
    harness,
    el: harness.fixture.nativeElement as HTMLElement,
    api,
  };
}

function setInput(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  el.value = value;
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("SettingsPageComponent (M13 D-12)", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("renders all 5 Material 3 section groups, 20 sections, filter search, and zero native <select> elements", async () => {
    const { el } = await mountSettings("/settings");

    const groups = [...el.querySelectorAll(".settings-group-header span")].map(
      (n) => n.textContent?.trim(),
    );
    expect(groups).toEqual([
      "Models & Compute",
      "Repositories & Code",
      "Work & Agents",
      "Access & Security",
      "Instance & Preferences",
    ]);
    expect(el.querySelectorAll(".settings-group-icon").length).toBe(5);
    expect(el.querySelectorAll(".settings-nav-item").length).toBe(20);
    expect(el.querySelectorAll("select").length).toBe(0);

    // Filter down by keyword "gma_" -> only Access remains
    const filter = el.querySelector<HTMLInputElement>(
      '[data-testid="settings-filter-input"]',
    )!;
    setInput(filter, "gma_");
    await new Promise((r) => setTimeout(r, 5));
    expect(el.querySelectorAll(".settings-nav-item").length).toBe(1);
    expect(
      el.querySelector('[data-testid="settings-nav-access"]')?.textContent,
    ).toContain("Access");

    // Empty match notice
    setInput(filter, "nonexistent-xyz");
    await new Promise((r) => setTimeout(r, 5));
    expect(
      el.querySelector('[data-testid="settings-filter-empty"]'),
    ).not.toBeNull();
  });

  it("previews normalized server changes via POST /api/settings/plan and saves via POST /api/settings", async () => {
    const { el, api, harness } = await mountSettings("/settings/engine");

    const pollInput = el.querySelector<HTMLInputElement>(
      '[data-testid="input-poll-interval"]',
    )!;
    setInput(pollInput, "1m");
    harness.detectChanges();

    const saveBar = el.querySelector('[data-testid="settings-save-bar"]');
    expect(saveBar).not.toBeNull();
    expect(saveBar?.textContent).toContain("1 unsaved change");

    // Click Plan / Preview changes
    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-plan-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();

    const preview = el.querySelector('[data-testid="settings-plan-preview"]')!;
    expect(preview).not.toBeNull();
    expect(preview.textContent).toContain("Poll interval");
    expect(preview.textContent).toContain("30s");
    expect(preview.textContent).toContain("60s");
    expect(preview.textContent).toContain("from 1m");

    // Save changes
    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-save-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();

    expect(
      api.calls.some(
        (c) =>
          c.method === "POST" &&
          c.key === "POST /api/settings" &&
          (c.body as Record<string, unknown>)["pollInterval"] === "1m",
      ),
    ).toBe(true);
    expect(el.querySelector('[data-testid="settings-save-bar"]')).toBeNull();
  });

  it("enforces configRepoOnly read-only chip and banner on config-managed sections", async () => {
    const api = new FakeSettingsApi();
    api.configBody.configRepoOnly = true;
    api.settingsBody["configRepoOnly"] = true;

    const { el, harness } = await mountSettings("/settings/tasks", api);

    expect(
      el.querySelector('[data-testid="settings-readonly-chip"]')?.textContent,
    ).toContain("Read-only");
    expect(
      el.querySelector('[data-testid="settings-config-repo-banner"]'),
    ).not.toBeNull();
    expect(
      el
        .querySelector<HTMLButtonElement>(
          '[data-testid="switch-approved-by-default"]',
        )
        ?.hasAttribute("disabled"),
    ).toBe(true);

    // Navigate to Models: zero password inputs mount on initial load.
    await harness.navigateByUrl("/settings/models");
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();
    expect(el.querySelectorAll('input[type="password"]').length).toBe(0);
  });

  it("manages Model Connections without a standalone framework keys card", async () => {
    const { el, api, harness } = await mountSettings("/settings/models");

    expect(el.querySelectorAll('input[type="password"]').length).toBe(0);
    // Credentials live on each connection; the deployment-wide framework
    // keys card (and its /api/agent-keys reads) is retired from the UI.
    expect(el.querySelector('[data-testid="card-agent-keys"]')).toBeNull();
    expect(api.calls.some((c) => c.key.includes("/api/agent-keys"))).toBe(
      false,
    );

    // Add a new Vertex backend connection
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-new-backend-id"]',
      )!,
      "vertex-eu",
    );

    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="add-model-backend"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(
      api.calls.some(
        (c) =>
          c.key === "PUT /api/deployment/agent-backends/{backendId}" &&
          c.params?.["backendId"] === "vertex-eu",
      ),
    ).toBe(true);
  });

  it("manages RBAC principals and mints + revokes gma_ external agent tokens", async () => {
    const { el, api, harness } = await mountSettings("/settings/access");

    console.log("DEBUG SECOND:", el.innerHTML);
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-principal-entry"]',
      )!,
      "bob@example.com",
    );
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="add-principal-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/access/principals" &&
          (c.body as { entry?: string })?.entry === "bob@example.com",
      ),
    ).toBe(true);

    // Mint gma_ token
    console.log("DEBUG SECOND:", el.innerHTML);
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-gma-token-name"]',
      )!,
      "deploy-agent",
    );
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="mint-gma-token-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();

    expect(
      el.querySelector('[data-testid="minted-gma-token"]')?.textContent,
    ).toContain("gma_live_secret_token_999");

    // Revoke existing token
    el.querySelector<HTMLButtonElement>(
      '[data-testid="revoke-token-tok-1"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/external-agents/tokens/{id}/revoke" &&
          c.params?.["id"] === "tok-1",
      ),
    ).toBe(true);
  });

  it("manages Capability servers & policy, StateRepo sync & file viewer, and Upgrade start/cancel", async () => {
    const { el, api, harness } = await mountSettings("/settings/capabilities");

    // Test MCP server & save policy
    el.querySelector<HTMLButtonElement>(
      '[data-testid="test-cap-server-github-mcp"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="cap-test-notice"]')?.textContent,
    ).toContain("3 tools advertised");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-cap-policy-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(api.calls.some((c) => c.key === "PUT /api/capability-policy")).toBe(
      true,
    );

    // Navigate to Config repo -> sync + file viewer
    await harness.navigateByUrl("/settings/config-repo");
    await new Promise((r) => setTimeout(r, 15));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="config-file-content"]')?.textContent,
    ).toContain('"environmentName":"Staging"');

    el.querySelector<HTMLButtonElement>(
      '[data-testid="sync-state-repo-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(api.calls.some((c) => c.key === "POST /api/state-repo/sync")).toBe(
      true,
    );

    // Navigate to Upgrade -> release notes, cancel active upgrade, start upgrade
    await harness.navigateByUrl("/settings/upgrade");
    await new Promise((r) => setTimeout(r, 15));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="upgrade-release-notes"]')?.textContent,
    ).toContain("Faster microVM boot");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="cancel-upgrade-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(
      api.calls.some((c) => c.key === "DELETE /api/upgrade/scheduled"),
    ).toBe(true);

    // Start upgrade asks first (2026-11-13 deployment-operations
    // walkthrough, task 5390): Cancel sends nothing, the accept starts it.
    const startUpgrade = () =>
      el
        .querySelector<HTMLButtonElement>('[data-testid="start-upgrade-btn"]')!
        .click();
    startUpgrade();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("Upgrade grain to");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 50));
    expect(api.calls.some((c) => c.key === "POST /api/upgrade")).toBe(false);
    startUpgrade();
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    expect(api.calls.some((c) => c.key === "POST /api/upgrade")).toBe(true);
  });

  it("says Not available, offers no Start upgrade and reads no targets when no upgrader is configured", async () => {
    // 2026-11-13 deployment-operations walkthrough: a demo started without
    // -upgrade-image read "dev", drew an enabled Start upgrade and 404ed
    // on GET /api/upgrade/targets twice.
    const api = new FakeSettingsApi();
    api.configBody.version = {
      release: "v0.0.0-20261113022244-4ae6438fdcd4",
      commit: "4ae6438fdcd4a0fc373fb3da76888724685b75fd",
      unversioned: true,
    };
    api.upgradeBody = { enabled: false, mode: "", phase: "" };
    const { el } = await mountSettings("/settings/upgrade", api);
    await new Promise((r) => setTimeout(r, 50));
    expect(
      el.querySelector('[data-testid="current-daemon-version"]')?.textContent,
    ).toContain("4ae6438");
    expect(
      el.querySelector('[data-testid="upgrade-unavailable"]')?.textContent,
    ).toContain("Not available");
    expect(el.querySelector('[data-testid="start-upgrade-btn"]')).toBeNull();
    expect(api.calls.some((c) => c.key === "GET /api/upgrade/targets")).toBe(
      false,
    );
  });

  it("shows an externally managed deployment's notice and management link instead of upgrade controls", async () => {
    const api = new FakeSettingsApi();
    api.configBody.version = { release: "v3.6.0", commit: "abcdef0123456" };
    api.upgradeBody = {
      enabled: false,
      running: { commit: "4ae6438fdcd4a0fc373fb3da76888724685b75fd" },
      selfUpgradeDisabled: true,
      managementUrl: "https://console.example.com/deployments/grain-prod",
    };
    const { el } = await mountSettings("/settings/upgrade", api);
    await new Promise((r) => setTimeout(r, 50));
    expect(
      el.querySelector('[data-testid="current-daemon-version"]')?.textContent,
    ).toContain("4ae6438");
    expect(
      el.querySelector('[data-testid="upgrade-externally-managed"]')
        ?.textContent,
    ).toContain("managed externally");
    expect(
      el
        .querySelector('[data-testid="upgrade-management-link"]')
        ?.getAttribute("href"),
    ).toBe("https://console.example.com/deployments/grain-prod");
    expect(el.querySelector('[data-testid="upgrade-unavailable"]')).toBeNull();
    expect(el.querySelector('[data-testid="start-upgrade-btn"]')).toBeNull();
    expect(api.calls.some((c) => c.key === "GET /api/upgrade/targets")).toBe(
      false,
    );
  });

  it("drops a management URL that is not an absolute http(s) link", () => {
    expect(safeManagementUrl("javascript:alert(1)")).toBe("");
    expect(safeManagementUrl("/relative")).toBe("");
    expect(safeManagementUrl(42)).toBe("");
    expect(safeManagementUrl("https://console.example.com/x")).toBe(
      "https://console.example.com/x",
    );
  });

  it("shows a host's available grain-agentd update and the command to take it", async () => {
    const api = new FakeSettingsApi();
    api.hostdsBody = [
      {
        name: "laptop",
        owner: "alice",
        state: "connected",
        update: {
          build: "v3.8.0",
          available: "v3.9.0",
          command: "grain-agentd update -version v3.9.0",
        },
      },
      { name: "workstation-1", owner: "bob", state: "connected" },
    ];
    const { el } = await mountSettings("/settings/hosts", api);
    const line = el.querySelector('[data-testid="hostd-update-laptop"]');
    expect(line?.textContent).toContain("v3.8.0");
    expect(line?.textContent).toContain("v3.9.0");
    expect(line?.textContent).toContain("grain-agentd update -version v3.9.0");
    expect(
      el.querySelector('[data-testid="hostd-update-workstation-1"]'),
    ).toBeNull();
  });

  it("gates operator-only (scope: null) hostds enrollment-codes, pending joins, and state-repo/remote for non-operators without 403 calls", async () => {
    const api = new FakeSettingsApi();
    api.configBody.access = {
      role: "grain/admin",
      roles: ["grain/admin"],
      full: false,
      scopes: [
        "settings:read",
        "settings:write",
        "capabilities:read",
        "capabilities:admin",
        "secrets:read",
      ],
    };

    const { el, harness } = await mountSettings("/settings/hosts", api);

    // Alias /settings/hosts resolved to hostds!
    expect(
      el.querySelector('[data-testid="settings-section-heading"]')?.textContent,
    ).toContain("Hosts");
    expect(
      el.querySelector('[data-testid="hostds-enrollment-restricted"]'),
    ).not.toBeNull();
    expect(
      el.querySelector('[data-testid="hostds-pending-restricted"]'),
    ).not.toBeNull();
    expect(
      api.calls.some((c) => c.key === "GET /api/hostds/enrollment-codes"),
    ).toBe(false);
    expect(api.calls.some((c) => c.key === "GET /api/hostds/pending")).toBe(
      false,
    );

    // Navigate to Config repo: remote config (scope: null) is also cleanly gated!
    await harness.navigateByUrl("/settings/config-repo");
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="state-repo-remote-restricted"]'),
    ).not.toBeNull();
  });

  it("ships quality control off by default and stages & saves PR rollout settings on /settings/tasks", async () => {
    const api = new FakeSettingsApi();
    api.settingsBody = {
      ...api.settingsBody,
      qaEnabled: false,
      qaFlowId: "golden-v1-minimal",
      qaFlowIds: ["golden-v1-minimal", "deterministic-s3-only", "repo-custom"],
      qaRolloutPct: 0,
      qaShadowOnControl: true,
      qaEpoch: 1,
      qaRegimeId: 1,
    };

    const { el, harness } = await mountSettings("/settings/tasks", api);

    const card = el.querySelector('[data-testid="card-qa-rollout"]');
    expect(card).not.toBeNull();

    const enableSwitch = el.querySelector<HTMLButtonElement>(
      '[data-testid="switch-qa-enabled"]',
    )!;
    const flowBtn = el.querySelector<HTMLButtonElement>(
      '[data-testid="pill-qa-flow"]',
    )!;
    const rolloutInput = el.querySelector<HTMLInputElement>(
      '[data-testid="input-qa-rollout-pct"]',
    )!;
    const shadowSwitch = el.querySelector<HTMLButtonElement>(
      '[data-testid="switch-qa-shadow-control"]',
    )!;

    // Default shipped state: OFF, and dependent rollout controls disabled until switched on.
    expect(enableSwitch.getAttribute("aria-checked")).toBe("false");
    expect(flowBtn.disabled).toBe(true);
    expect(rolloutInput.disabled).toBe(true);
    expect(shadowSwitch.disabled).toBe(true);

    // Enable engine -> dependent controls unlock immediately and dirty save bar appears.
    enableSwitch.click();
    harness.detectChanges();

    expect(enableSwitch.getAttribute("aria-checked")).toBe("true");
    expect(flowBtn.disabled).toBe(false);
    expect(rolloutInput.disabled).toBe(false);
    expect(shadowSwitch.disabled).toBe(false);

    // Choose flow via native <g-pill-select> and set 33% PR rollout (step=1 allows any integer 0..100).
    flowBtn.click();
    harness.detectChanges();
    const menuOptions = Array.from(
      document.querySelectorAll<HTMLButtonElement>(
        "#pill-qa-flow-menu button[role='option'], #pill-qa-flow-menu button",
      ),
    );
    const s3OnlyOpt = menuOptions.find((b) =>
      b.textContent?.includes("deterministic-s3-only"),
    );
    expect(s3OnlyOpt).toBeDefined();
    s3OnlyOpt!.click();
    harness.detectChanges();

    // Toggling off before saving clears untouched dependent drafts cleanly.
    rolloutInput.value = "33";
    rolloutInput.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    enableSwitch.click();
    harness.detectChanges();
    expect(el.querySelector('[data-testid="settings-save-bar"]')).toBeNull();

    // Re-enable, test invalid input blocking Save, Discard reset, Plan preview (regime bump), and Save.
    enableSwitch.click();
    harness.detectChanges();
    rolloutInput.value = "150";
    rolloutInput.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="error-qa-rollout-pct"]')?.textContent,
    ).toContain("0 and 100");
    expect(
      el.querySelector<HTMLButtonElement>('[data-testid="settings-save-btn"]')
        ?.disabled,
    ).toBe(true);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-discard-btn"]',
    )!.click();
    harness.detectChanges();
    expect(rolloutInput.value).toBe("0");
    expect(el.querySelector('[data-testid="error-qa-rollout-pct"]')).toBeNull();

    // Stage valid 33% + deterministic-s3-only + shadow=false, preview plan, and save.
    enableSwitch.click();
    harness.detectChanges();
    flowBtn.click();
    harness.detectChanges();
    Array.from(
      document.querySelectorAll<HTMLButtonElement>("#pill-qa-flow-menu button"),
    )
      .find((b) => b.textContent?.includes("deterministic-s3-only"))!
      .click();
    harness.detectChanges();
    rolloutInput.value = "33";
    rolloutInput.dispatchEvent(new Event("input", { bubbles: true }));
    shadowSwitch.click();
    harness.detectChanges();

    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-plan-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 50));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="settings-plan-preview"]')?.textContent,
    ).toContain("Quality control PR rollout (%)");

    const saveBtn = el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-save-btn"]',
    );
    expect(saveBtn).not.toBeNull();
    saveBtn!.click();
    await new Promise((r) => setTimeout(r, 150));
    harness.detectChanges();

    const savedCall = api.calls.find((c) => c.key === "POST /api/settings");
    expect(savedCall).toBeDefined();
    expect(savedCall?.body).toEqual({
      qaEnabled: true,
      qaFlowId: "deterministic-s3-only",
      qaRolloutPct: 33,
      qaShadowOnControl: false,
    });
  });

  it("explains read-only settings for viewer roles without settings:write", async () => {
    const api = new FakeSettingsApi();
    api.configBody.access = {
      role: "grain/viewer",
      roles: ["grain/viewer"],
      full: false,
      scopes: ["settings:read", "tasks:read"],
    };
    api.settingsBody = {
      ...api.settingsBody,
      qaEnabled: true,
      qaRolloutPct: 50,
    };

    const { el } = await mountSettings("/settings/tasks", api);
    expect(
      el.querySelector('[data-testid="settings-role-readonly"]')?.textContent,
    ).toMatch(/settings:write|grain\/viewer/i);
    expect(
      el.querySelector<HTMLButtonElement>('[data-testid="switch-qa-enabled"]')
        ?.disabled,
    ).toBe(true);
    expect(
      el.querySelector<HTMLButtonElement>('[data-testid="pill-qa-flow"]')
        ?.disabled,
    ).toBe(true);
  });

  it("renders reachable repos errors/mismatches and confirms adding an unreachable repository via g-confirm-dialog", async () => {
    const { el, harness, api } = await mountSettings("/settings/repositories");

    const input = el.querySelector<HTMLInputElement>(
      '[data-testid="input-add-repo"]',
    )!;
    expect(input).toBeTruthy();
    input.dispatchEvent(new Event("focus"));
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    harness.fixture.detectChanges();

    expect(
      Array.from(el.querySelectorAll("#reachable-repos-list option")).map(
        (o) => (o as HTMLOptionElement).value,
      ),
    ).toContain("acme/alpha");
    expect(el.textContent).toContain("enterprise host returned HTML");
    expect(el.textContent).toContain("ghe.internal mismatch");

    setInput(input, "acme/unreachable-repo");
    harness.fixture.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="add-repo-btn"]',
    )!.click();
    harness.fixture.detectChanges();

    const dialog = el.querySelector('[data-testid="confirm-dialog"]');
    expect(dialog?.textContent).toContain("acme/unreachable-repo");
    expect(api.calls.some((c) => c.key === "POST /api/repos")).toBe(false);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/repos" &&
          (c.body as { repo?: string })?.repo === "acme/unreachable-repo",
      ),
    ).toBe(true);
  });

  it("clears connection credentials, confirms connection removal, and passes Git connection scope", async () => {
    const api = new FakeSettingsApi();
    (api.deploymentBody as Record<string, unknown>)["agentBackends"] = [
      { id: "claude-team", framework: "claude", name: "Claude Team" },
      {
        id: "agy-ambient",
        framework: "antigravity",
        name: "Ambient",
        authMode: "ambient",
      },
    ];
    const { el, harness } = await mountSettings("/settings/models", api);

    // Search filter is owned by a <form role="search" autocomplete="off"> so
    // browser password managers never pair it with secret inputs.
    const filterInput = el.querySelector<HTMLInputElement>(
      '[data-testid="settings-filter-input"]',
    )!;
    expect(filterInput.getAttribute("autocomplete")).toBe("off");
    expect(filterInput.closest("form")?.getAttribute("role")).toBe("search");
    expect(filterInput.closest("form")?.getAttribute("autocomplete")).toBe(
      "off",
    );

    // Ambient connections do not offer stored secret management, and non-ambient
    // connections keep their password field collapsed until Credential is opened.
    expect(
      el.querySelector('[data-testid="toggle-cred-agy-ambient"]'),
    ).toBeNull();
    expect(
      el.querySelector('[data-testid="clear-cred-claude-team"]'),
    ).toBeNull();

    const toggleCred = el.querySelector<HTMLButtonElement>(
      '[data-testid="toggle-cred-claude-team"]',
    )!;
    expect(toggleCred).toBeTruthy();
    toggleCred.click();
    harness.fixture.detectChanges();

    const credInput = el.querySelector<HTMLInputElement>(
      'input[aria-label="Credential for claude-team"]',
    )!;
    expect(credInput.getAttribute("autocomplete")).toBe("new-password");
    expect(credInput.closest("form")?.getAttribute("autocomplete")).toBe("off");

    const clearBtn = el.querySelector<HTMLButtonElement>(
      '[data-testid="clear-cred-claude-team"]',
    )!;
    expect(clearBtn).toBeTruthy();
    clearBtn.click();
    await harness.fixture.whenStable();
    expect(
      api.calls.some(
        (c) =>
          c.key === "DELETE /api/agent-backends/{id}/credential" &&
          c.params?.["id"] === "claude-team",
      ),
    ).toBe(true);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-backend-claude-team"]',
    )!.click();
    harness.fixture.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("claude-team");
    expect(
      api.calls.some(
        (c) => c.key === "DELETE /api/deployment/agent-backends/{backendId}",
      ),
    ).toBe(false);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    expect(
      api.calls.some(
        (c) =>
          c.key === "DELETE /api/deployment/agent-backends/{backendId}" &&
          c.params?.["backendId"] === "claude-team",
      ),
    ).toBe(true);

    await harness.navigateByUrl("/settings/git");
    await harness.fixture.whenStable();
    harness.fixture.detectChanges();
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-git-conn-scope"]',
      )!,
      "acme/widgets",
    );
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-git-conn-secret"]',
      )!,
      "ghp_test_token_123",
    );
    harness.fixture.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-git-conn-btn"]',
    )!.click();
    await harness.fixture.whenStable();
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/deployment/git-connections" &&
          (c.body as { scope?: string })?.scope === "acme/widgets",
      ),
    ).toBe(true);
  });

  it("renders skeletons on cold config load, fires section fetches once per mount, and opens warm Settings synchronously across route leaves", async () => {
    localStorage.clear();
    const api = new FakeSettingsApi();
    let resolveCfg!: (v: GrainConfig) => void;
    api.config = () =>
      new Promise<GrainConfig>((res) => {
        resolveCfg = res;
      });
    TestBed.configureTestingModule({
      providers: [
        provideRouter(
          [
            { path: "settings", component: SettingsPageComponent },
            { path: "settings/:section", component: SettingsPageComponent },
            { path: "other", children: [] },
          ],
          withComponentInputBinding(),
        ),
        { provide: APP_BASE_HREF, useValue: "/next/" },
        { provide: GrainApi, useValue: api },
      ],
    });
    const harness = await RouterTestingHarness.create("/settings/git");
    harness.fixture.autoDetectChanges(true);
    await harness.fixture.whenStable();

    const el = harness.fixture.nativeElement as HTMLElement;
    expect(
      el.querySelector('[data-testid="settings-skeleton"]'),
    ).not.toBeNull();

    // Unblock cold config and switch config() back to fast resolved response for background refreshes.
    const cfg = structuredClone(api.configBody);
    api.config = () => Promise.resolve(structuredClone(api.configBody));
    resolveCfg(cfg);
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    await harness.fixture.whenStable();

    expect(el.querySelector('[data-testid="settings-skeleton"]')).toBeNull();
    // Guarded ngOnChanges prevents duplicate child loadForSection() call on initial mount.
    expect(
      api.calls.filter((c) => c.key === "GET /api/deployment/git-connections")
        .length,
    ).toBe(1);

    // Visiting /settings/repositories fetches reachable-repos once.
    await harness.navigateByUrl("/settings/repositories");
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    expect(
      api.calls.filter((c) => c.key === "GET /api/deployment/reachable-repos")
        .length,
    ).toBe(1);

    // Leave Settings for another route and come back: warm open renders without skeletons and hits 60s reachable-repos cache.
    await harness.navigateByUrl("/other");
    await harness.navigateByUrl("/settings/repositories");
    harness.fixture.detectChanges();
    const warmEl = harness.fixture.nativeElement as HTMLElement;
    expect(
      warmEl.querySelector('[data-testid="settings-skeleton"]'),
    ).toBeNull();
    expect(
      warmEl.querySelector('[data-testid="input-add-repo"]'),
    ).not.toBeNull();
    expect(
      api.calls.filter((c) => c.key === "GET /api/deployment/reachable-repos")
        .length,
    ).toBe(1);
  });

  it("focuses Filter settings on '/', restores groups on Escape, and prompts before discarding dirty settings or memory", async () => {
    const api = new FakeSettingsApi();
    const prevGet = api.get;
    api.get = (key: string, params?: RouteParams, query?: string) => {
      if (key === "GET /api/memory") {
        api.calls.push({ method: "GET", key, params, query });
        return Promise.resolve([{ text: "Original deployment note" }]);
      }
      return prevGet(key, params, query);
    };
    const { el, harness } = await mountSettings("/settings/memory", api);

    const filterInput = el.querySelector<HTMLInputElement>(
      '[data-testid="settings-filter-input"]',
    )!;
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "/", bubbles: true }),
    );
    expect(document.activeElement).toBe(filterInput);

    setInput(filterInput, "house style");
    harness.detectChanges();
    expect(el.querySelectorAll(".settings-nav-item").length).toBe(1);

    filterInput.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    harness.detectChanges();
    expect(filterInput.value).toBe("");
    expect(el.querySelectorAll(".settings-nav-item").length).toBe(20);

    // Edit deployment memory -> dirty dot & save bar appear; switching section asks first.
    const memBox = el.querySelector<HTMLTextAreaElement>(
      '[data-testid="input-deployment-memory"]',
    )!;
    setInput(memBox, "Unsaved memory edit");
    harness.detectChanges();
    expect(el.querySelector('[data-testid="settings-dirty-dot"]')).toBeTruthy();
    expect(el.querySelector('[data-testid="settings-save-bar"]')).toBeTruthy();

    el.querySelector<HTMLAnchorElement>(
      '[data-testid="settings-nav-general"]',
    )!.click();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("Discard unsaved changes?");

    // Cancel keeps us on Memory with the edit intact.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="settings-section-heading"]')?.textContent,
    ).toContain("Memory");
    expect(memBox.value).toBe("Unsaved memory edit");

    // Accept discards and navigates to General.
    el.querySelector<HTMLAnchorElement>(
      '[data-testid="settings-nav-general"]',
    )!.click();
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="settings-section-heading"]')?.textContent,
    ).toContain("General");
    expect(el.querySelector('[data-testid="settings-save-bar"]')).toBeNull();
  });

  it("renders workstation CLI install/login/skills, disables settings lock without a config repo, and confirms Access principal removal", async () => {
    const api = new FakeSettingsApi();
    api.settingsBody["externalAgentApi"] = false;
    api.settingsBody["settingsLocked"] = true;
    api.settingsBody["configRepoOnly"] = false;
    const { el, harness } = await mountSettings("/settings/general", api);

    // Without configRepo, settingsLocked=true does NOT lock the UI and the lock switch is disabled.
    expect(
      el.querySelector('[data-testid="settings-config-repo-banner"]'),
    ).toBeNull();
    expect(
      el.querySelector<HTMLButtonElement>(
        '[data-testid="switch-settings-locked"]',
      )?.disabled,
    ).toBe(true);

    // CLI shows /cli/install.sh, login flags, external-agents warning, and all 4 agent skills.
    await harness.navigateByUrl("/settings/cli");
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="cli-install-snippet"]')?.textContent,
    ).toContain("/cli/install.sh | sh");
    expect(
      el.querySelector('[data-testid="cli-login-snippet"]')?.textContent,
    ).toContain("--issue-token --scope-set grain/external-dev --expires 30d");
    expect(
      el.querySelector('[data-testid="cli-external-agent-api-off"]'),
    ).not.toBeNull();
    expect(
      el.querySelector('[data-testid="cli-skill-command-jetski"]')?.textContent,
    ).toBe("grain skill install --jetski");

    // Preferences exposes Display timezone and Customize navigation trigger.
    await harness.navigateByUrl("/settings/preferences");
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="pref-select-timezone"]'),
    ).not.toBeNull();
    expect(
      el.querySelector('[data-testid="pref-customize-nav-btn"]'),
    ).not.toBeNull();

    // Access confirms principal removal before calling DELETE.
    await harness.navigateByUrl("/settings/access");
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-principal-operator@example.com"]',
    )!.click();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("operator@example.com");
    expect(
      api.calls.some((c) => c.key === "DELETE /api/access/principals/{entry}"),
    ).toBe(false);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    expect(
      api.calls.some((c) => c.key === "DELETE /api/access/principals/{entry}"),
    ).toBe(true);
  });

  it("labels capability server states in host wording, gives failed the error tone, and titles needs-config with its reason", async () => {
    expect(
      capServerState({ id: "p", status: { state: "needs-hostd:mac-mini" } }),
    ).toEqual({ label: "needs host mac-mini", tone: "blast" });
    expect(
      capServerState({ id: "b", status: { state: "needs-hostd" } }),
    ).toEqual({ label: "needs host", tone: "blast" });
    expect(capServerState({ id: "f", status: { state: "failed" } })).toEqual({
      label: "failed",
      tone: "error",
    });

    const api = new FakeSettingsApi();
    const baseGet = api.get;
    api.get = (key, params, query) => {
      if (key === "GET /api/capability-servers") {
        return Promise.resolve([
          {
            id: "figma",
            status: { state: "needs-config", reason: "Set a Figma token" },
          },
          { id: "host-script", status: { state: "needs-hostd:builder-1" } },
          {
            id: "broken-mcp",
            status: { state: "failed", error: "connection refused" },
          },
        ]);
      }
      return baseGet(key, params, query);
    };
    const { el } = await mountSettings("/settings/capabilities", api);
    const chip = (id: string) =>
      el.querySelector<HTMLElement>(`[data-testid="cap-server-state-${id}"]`)!;
    expect(chip("figma").textContent?.trim()).toBe("needs-configuring");
    expect(chip("figma").getAttribute("title")).toBe("Set a Figma token");
    expect(chip("host-script").textContent?.trim()).toBe(
      "needs host builder-1",
    );
    expect(chip("host-script").classList.contains("blast")).toBe(true);
    expect(chip("broken-mcp").textContent?.trim()).toBe("failed");
    expect(chip("broken-mcp").classList.contains("error")).toBe(true);
    expect(chip("broken-mcp").getAttribute("title")).toBe("connection refused");
  });

  it("sends the chosen host kind when saving a connection over an existing stored credential", async () => {
    const { el, harness, api } = await mountSettings("/settings/git");
    const click = (id: string) => {
      el.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`)!.click();
      harness.fixture.detectChanges();
    };
    click("select-git-conn-mode");
    click("select-git-conn-mode-opt-existing");
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-git-conn-name"]',
      )!,
      "git-cred-1",
    );
    click("select-git-conn-kind");
    click("select-git-conn-kind-opt-git");
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-git-conn-host"]',
      )!,
      "git.internal.example",
    );
    harness.fixture.detectChanges();
    click("save-git-conn-btn");
    await harness.fixture.whenStable();
    const saved = api.calls.find(
      (c) => c.key === "POST /api/deployment/git-connections",
    );
    expect(saved?.body).toMatchObject({
      credential: "git-cred-1",
      host: "git.internal.example",
      kind: "git",
    });
  });

  it("scrolls to an old section name's card on in-app navigation and stops when another section is chosen", async () => {
    const { el, harness } = await mountSettings("/settings/engine");
    const scrolled: string[] = [];
    const orig = HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView = function (this: HTMLElement) {
      scrolled.push(this.id);
    };
    try {
      // Same component instance; the fragment stays null -> null.
      await harness.navigateByUrl("/settings/secrets");
      await harness.fixture.whenStable();
      await new Promise((r) => setTimeout(r, 140));
      expect(scrolled).toContain("other-secrets");

      // An anchor that never appears keeps the timer running until the
      // reader picks another section.
      await harness.navigateByUrl("/settings/capabilities#no-such-card");
      await harness.fixture.whenStable();
      const comp = harness.routeDebugElement?.componentInstance as {
        anchorTimer: ReturnType<typeof setInterval> | null;
      };
      expect(comp.anchorTimer).not.toBeNull();
      el.querySelector<HTMLAnchorElement>(
        '[data-testid="settings-nav-engine"]',
      )!.click();
      expect(comp.anchorTimer).toBeNull();
    } finally {
      HTMLElement.prototype.scrollIntoView = orig;
    }
  });
});
