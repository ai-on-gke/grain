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
    if (key === "GET /api/agent-keys") {
      return Promise.resolve({
        enabled: true,
        geminiApiKeySet: true,
        claudeOAuthTokenSet: false,
        openaiApiKeySet: false,
        codexAuthJSONSet: false,
      });
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
      return Promise.resolve([
        { name: "workstation-1", owner: "alice", status: "connected" },
      ]);
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
    if (key === "POST /api/agent-keys/{framework}/check") {
      return Promise.resolve({ ok: true, detail: "200 OK (models reachable)" });
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

  it("enforces configRepoOnly read-only chip and banner on config-managed sections while keeping API keys editable", async () => {
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

    // Navigate to Models: zero password inputs mount on initial load, and the
    // secret key toggle/input stays enabled even when configRepoOnly is true.
    await harness.navigateByUrl("/settings/models");
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();
    expect(el.querySelectorAll('input[type="password"]').length).toBe(0);
    const toggleBtn = el.querySelector<HTMLButtonElement>(
      '[data-testid="toggle-agent-key-antigravity"]',
    )!;
    expect(toggleBtn.disabled).toBe(false);
    toggleBtn.click();
    harness.detectChanges();
    const keyInput = el.querySelector<HTMLInputElement>(
      '[data-testid="input-agent-key-antigravity"]',
    )!;
    expect(keyInput.disabled).toBe(false);
  });

  it("manages Model Connections and verifies framework API keys", async () => {
    const { el, api, harness } = await mountSettings("/settings/models");

    expect(el.querySelectorAll('input[type="password"]').length).toBe(0);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="toggle-agent-key-claude"]',
    )!.click();
    harness.detectChanges();

    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-agent-key-claude"]',
      )!,
      "sk-ant-test-123",
    );
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-agent-key-claude"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();

    expect(
      api.calls.some(
        (c) =>
          c.key === "PUT /api/agent-keys/{framework}" &&
          c.params?.["framework"] === "claude",
      ),
    ).toBe(true);
    expect(
      el.querySelector('[data-testid="input-agent-key-claude"]'),
    ).toBeNull();

    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="check-agent-key-antigravity"]',
    )!.click();
    harness.detectChanges();
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 200));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="key-check-result-antigravity"]')
        ?.textContent,
    ).toContain("200 OK");

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
});
