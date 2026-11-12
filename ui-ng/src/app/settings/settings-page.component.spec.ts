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

  settingsBody: Record<string, unknown> = {
    environmentName: "Staging",
    pollInterval: "30s",
    maxWorkers: 4,
    agentFramework: "gemini",
    geminiModel: "gemini-2.5-pro",
    geminiEffort: "high",
    approvedByDefault: false,
    autoMergeByDefault: false,
    externalAgentApi: true,
    sandboxBackend: "granule",
  };

  deploymentBody: Record<string, unknown> = {
    defaultModel: "gemini-2.5-pro",
    targetRepos: ["acme/platform"],
    agentBackends: [
      { id: "vertex-main", framework: "gemini", provider: "vertex" },
    ],
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
      return Promise.resolve([
        { framework: "gemini", configured: true },
        { framework: "claude", configured: false },
      ]);
    }
    if (key === "GET /api/access/principals") {
      return Promise.resolve([
        { entry: "roman@google.com", role: "grain/operator" },
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
        policy: { rules: [{ capability: "repo:write", effect: "allow" }] },
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
      return Promise.resolve({
        currentVersion: "v3.6.0",
        current: { state: "downloading" },
      });
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
        { name: "workstation-1", owner: "rarcea", status: "connected" },
      ]);
    }
    if (key === "GET /api/hostds/enrollment-codes") {
      return Promise.resolve([{ name: "eng-laptops", uses: 2, maxUses: 10 }]);
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
      return Promise.resolve({ summary: "2 rules evaluated cleanly" });
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
    await new Promise((r) => setTimeout(r, 10));
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
    await new Promise((r) => setTimeout(r, 10));
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

    // Navigate to Models: secret key input stays enabled even when configRepoOnly is true!
    await harness.navigateByUrl("/settings/models");
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    const keyInput = el.querySelector<HTMLInputElement>(
      '[data-testid="input-agent-key-gemini"]',
    )!;
    expect(keyInput.disabled).toBe(false);
  });

  it("manages Model Connections and verifies framework API keys", async () => {
    const { el, api, harness } = await mountSettings("/settings/models");

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
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();

    expect(
      api.calls.some(
        (c) =>
          c.key === "PUT /api/agent-keys/{framework}" &&
          c.params?.["framework"] === "claude",
      ),
    ).toBe(true);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="check-agent-key-gemini"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="key-check-result-gemini"]')?.textContent,
    ).toContain("200 OK");

    // Add a new Vertex backend connection
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-new-backend-id"]',
      )!,
      "vertex-eu",
    );
    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-new-backend-endpoint"]',
      )!,
      "my-eu-proj",
    );
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="add-model-backend"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
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

    setInput(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-principal-entry"]',
      )!,
      "bob@google.com",
    );
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="add-principal-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/access/principals" &&
          (c.body as { entry?: string })?.entry === "bob@google.com",
      ),
    ).toBe(true);

    // Mint gma_ token
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
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();

    expect(
      el.querySelector('[data-testid="minted-gma-token"]')?.textContent,
    ).toContain("gma_live_secret_token_999");

    // Revoke existing token
    el.querySelector<HTMLButtonElement>(
      '[data-testid="revoke-token-tok-1"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
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
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="cap-test-notice"]')?.textContent,
    ).toContain("3 tools advertised");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-cap-policy-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
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
    await new Promise((r) => setTimeout(r, 10));
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
    await new Promise((r) => setTimeout(r, 10));
    expect(api.calls.some((c) => c.key === "DELETE /api/upgrade/current")).toBe(
      true,
    );

    el.querySelector<HTMLButtonElement>(
      '[data-testid="start-upgrade-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(api.calls.some((c) => c.key === "POST /api/upgrade")).toBe(true);
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
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="state-repo-remote-restricted"]'),
    ).not.toBeNull();
  });
});
