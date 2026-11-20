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

// Settings → Models: the default model and its ordered fallback models
// (model.Config.DefaultModels), chosen from what the connections
// advertise -- the classic ModelDefaults pane's semantics.

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import { provideRouter, withComponentInputBinding } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import { SettingsPageComponent } from "./settings-page.component";

interface Call {
  method: string;
  key: string;
  params?: RouteParams;
  body?: unknown;
  query?: string;
}

class FakeApi {
  calls: Call[] = [];
  settingsBody: Record<string, unknown> = {
    environmentName: "Staging",
    defaultModel: "gemini-pro,opus-5,legacy-model",
    agentBackends: [
      {
        id: "agy",
        name: "Ambient",
        framework: "antigravity",
        discovered: {
          models: ["gemini-pro", "gemini-flash"],
          refreshedAt: "2026-11-13T09:00:00Z",
        },
      },
      { id: "claude", framework: "claude", models: ["opus-5"] },
    ],
    geminiEfforts: ["low", "medium", "high"],
    codexEfforts: ["minimal", "low", "medium", "high", "xhigh"],
  };
  deploymentBody: Record<string, unknown> = {
    defaultModel: "gemini-pro,opus-5,legacy-model",
    defaultModels: ["gemini-pro", "opus-5", "legacy-model"],
  };
  discoverAnswer: Record<string, unknown> = {
    asked: true,
    backend: {
      id: "agy",
      name: "Ambient",
      framework: "antigravity",
      discovered: {
        models: ["gemini-pro", "gemini-flash", "gemini-ultra"],
        refreshedAt: "2026-11-13T10:00:00Z",
      },
    },
  };

  access: Record<string, unknown> = {
    role: "grain/operator",
    full: true,
    scopes: [],
  };
  config = () =>
    Promise.resolve({
      version: "v3.6.0",
      configRepoOnly: false,
      access: this.access,
    });
  get = (key: string) => {
    this.calls.push({ method: "GET", key });
    if (key === "GET /api/settings") {
      return Promise.resolve(structuredClone(this.settingsBody));
    }
    if (key === "GET /api/deployment") {
      return Promise.resolve(structuredClone(this.deploymentBody));
    }
    return Promise.resolve({});
  };
  getText = () => Promise.resolve("");
  testAnswer = { ok: true, detail: "Provider accepted saved credential" };
  testError = false;
  post = (
    key: string,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ) => {
    this.calls.push({ method: "POST", key, params, body, query });
    if (key === "POST /api/agent-keys/{framework}/check") {
      if (this.testError) throw new Error("private-credential");
      return Promise.resolve(this.testAnswer);
    }
    if (key === "POST /api/settings") {
      this.settingsBody = {
        ...this.settingsBody,
        ...(body as Record<string, unknown>),
      };
      return Promise.resolve(structuredClone(this.settingsBody));
    }
    if (key === "POST /api/deployment/agent-backends/{backendId}/discover") {
      return Promise.resolve(structuredClone(this.discoverAnswer));
    }
    return Promise.resolve({ ok: true });
  };
  put = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, params, body });
    return Promise.resolve({ ok: true });
  };
  patch = () => Promise.resolve({ ok: true });
  delete = () => Promise.resolve({ ok: true });
}

async function mount(api = new FakeApi()) {
  localStorage.clear();
  TestBed.configureTestingModule({
    providers: [
      provideRouter(
        [{ path: "settings/:section", component: SettingsPageComponent }],
        withComponentInputBinding(),
      ),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
    ],
  });
  const harness = await RouterTestingHarness.create("/settings/models");
  harness.fixture.autoDetectChanges(true);
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 20));
  await harness.fixture.whenStable();
  const el = harness.fixture.nativeElement as HTMLElement;
  const settle = async () => {
    await new Promise((r) => setTimeout(r, 5));
    await harness.fixture.whenStable();
  };
  const q = <T extends HTMLElement = HTMLElement>(sel: string) =>
    el.querySelector<T>(sel);
  const pick = async (picker: string, model: string) => {
    q<HTMLButtonElement>(`[data-testid="${picker}"]`)!.click();
    await settle();
    const opt = q(`[data-testid="${picker}-opt"][data-model="${model}"]`);
    if (!opt) throw new Error(`${picker} does not offer ${model}`);
    opt.click();
    await settle();
  };
  const offered = async (picker: string) => {
    q<HTMLButtonElement>(`[data-testid="${picker}"]`)!.click();
    await settle();
    const list = [
      ...el.querySelectorAll<HTMLElement>(`[data-testid="${picker}-opt"]`),
    ].map((o) => o.dataset["model"]);
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await settle();
    return list;
  };
  const fallbacks = () =>
    [
      ...el.querySelectorAll('[data-testid^="fallback-row-"] .mp-chain-model'),
    ].map((n) => n.textContent?.trim());
  const save = async () => {
    q<HTMLButtonElement>('[data-testid="settings-save-btn"]')!.click();
    await new Promise((r) => setTimeout(r, 30));
    await harness.fixture.whenStable();
  };
  return { el, api, q, pick, offered, fallbacks, save, settle };
}

describe("Settings → Models default and fallback models", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("replaces the free-text default model with a picker over the discovered models", async () => {
    const { el, q, offered } = await mount();
    const card = q('[data-testid="card-model-defaults"]')!;
    expect(
      card.querySelector('[data-testid="input-default-model"]'),
    ).toBeNull();
    expect(
      card.querySelector('[data-testid="input-model-setting-model"]'),
    ).toBeNull();
    expect(card.querySelectorAll("select").length).toBe(0);
    const trigger = q('[data-testid="picker-default-model"]')!;
    expect(trigger.textContent).toContain("gemini-pro");
    // Options: the advertised models, grouped by connection. The stored
    // fallback nobody advertises is not offered as a default (it is not
    // the current value) but stays visible, flagged, in the list below.
    expect(await offered("picker-default-model")).toEqual([
      "gemini-pro",
      "gemini-flash",
      "opus-5",
    ]);
    q<HTMLButtonElement>('[data-testid="picker-default-model"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    const heads = [
      ...el.querySelectorAll(
        '[data-testid="picker-default-model-panel"] .mp-group-name',
      ),
    ].map((h) => h.textContent?.trim());
    expect(heads).toEqual(["Ambient (agy)", "claude"]);
  });

  it("lists the fallback models in order and flags one no connection advertises", async () => {
    const { fallbacks, q } = await mount();
    expect(fallbacks()).toEqual(["opus-5", "legacy-model"]);
    expect(q('[data-testid="fallback-row-1"]')?.textContent).toContain(
      "not advertised by any connection",
    );
    expect(q('[data-testid="fallback-row-0"]')?.textContent).not.toContain(
      "not advertised",
    );
  });

  it("keeps an unknown current default selected and flagged", async () => {
    const api = new FakeApi();
    api.settingsBody["defaultModel"] = "hydrogen-x";
    const { q, offered } = await mount(api);
    const trigger = q('[data-testid="picker-default-model"]')!;
    expect(trigger.textContent).toContain("hydrogen-x");
    expect(trigger.textContent).toContain("not advertised by any connection");
    expect((await offered("picker-default-model"))[0]).toBe("hydrogen-x");
    expect(q('[data-testid="default-model-warning"]')?.textContent).toContain(
      "No enabled connection advertises hydrogen-x",
    );
  });

  it("adds, reorders and removes fallbacks, never offering a model already in the list, and saves the ordered list", async () => {
    const { pick, offered, fallbacks, q, save, api, el } = await mount();
    // Neither the default nor an existing fallback can be added again.
    expect(await offered("picker-add-fallback")).toEqual(["gemini-flash"]);
    await pick("picker-add-fallback", "gemini-flash");
    expect(fallbacks()).toEqual(["opus-5", "legacy-model", "gemini-flash"]);

    q<HTMLButtonElement>('[data-testid="fallback-up-2"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    expect(fallbacks()).toEqual(["opus-5", "gemini-flash", "legacy-model"]);
    q<HTMLButtonElement>('[data-testid="fallback-up-1"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    expect(fallbacks()).toEqual(["gemini-flash", "opus-5", "legacy-model"]);
    q<HTMLButtonElement>('[data-testid="fallback-down-0"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    expect(fallbacks()).toEqual(["opus-5", "gemini-flash", "legacy-model"]);
    expect(
      q<HTMLButtonElement>('[data-testid="fallback-up-0"]')!.disabled,
    ).toBe(true);
    expect(
      q<HTMLButtonElement>('[data-testid="fallback-down-2"]')!.disabled,
    ).toBe(true);

    q<HTMLButtonElement>('[data-testid="fallback-remove-2"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    expect(fallbacks()).toEqual(["opus-5", "gemini-flash"]);
    expect(el.textContent).toContain("1 unsaved change");

    await save();
    const saved = api.calls.filter((c) => c.key === "POST /api/settings");
    expect(saved.length).toBe(1);
    expect(saved[0].body).toEqual({
      defaultModel: "gemini-pro,opus-5,gemini-flash",
    });
    expect(api.calls.some((c) => c.key === "PUT /api/deployment")).toBe(false);
    expect(fallbacks()).toEqual(["opus-5", "gemini-flash"]);
  });

  it("choosing a fallback as the default promotes it and keeps the rest in order", async () => {
    const { pick, fallbacks, q, save, api } = await mount();
    await pick("picker-default-model", "opus-5");
    expect(q('[data-testid="picker-default-model"]')?.textContent).toContain(
      "opus-5",
    );
    expect(fallbacks()).toEqual(["legacy-model"]);
    await save();
    expect(api.calls.find((c) => c.key === "POST /api/settings")?.body).toEqual(
      { defaultModel: "opus-5,legacy-model" },
    );
  });

  it("does not offer fallbacks before a default model is chosen", async () => {
    const api = new FakeApi();
    api.settingsBody["defaultModel"] = "";
    const { q, fallbacks } = await mount(api);
    expect(fallbacks()).toEqual([]);
    const add = q<HTMLButtonElement>('[data-testid="picker-add-fallback"]')!;
    expect(add.disabled).toBe(true);
    expect(add.textContent).toContain("Choose a default model first");
    expect(q('[data-testid="picker-default-model"]')?.textContent).toContain(
      "Not set",
    );
  });

  it("refreshes a connection's discovered models and offers what the provider added", async () => {
    const { q, offered, api, settle } = await mount();
    expect(await offered("picker-default-model")).not.toContain("gemini-ultra");
    q<HTMLButtonElement>('[data-testid="refresh-backend-agy"]')!.click();
    await new Promise((r) => setTimeout(r, 20));
    await settle();
    const calls = api.calls.filter(
      (c) =>
        c.key === "POST /api/deployment/agent-backends/{backendId}/discover",
    );
    expect(calls.map((c) => c.params)).toEqual([{ backendId: "agy" }]);
    // A pinned connection has nothing to discover.
    expect(q('[data-testid="refresh-backend-claude"]')).toBeNull();
    expect(await offered("picker-default-model")).toContain("gemini-ultra");
  });

  it("gives no refresh time for a connection whose provider never answered", async () => {
    const api = new FakeApi();
    api.settingsBody["agentBackends"] = [
      {
        id: "agy",
        framework: "antigravity",
        discovered: {
          refreshedAt: "0001-01-01T00:00:00Z",
          error: "antigravity: agy models printed no model names",
        },
      },
    ];
    const { q } = await mount(api);
    const summary = q(".s-disc-details summary")!.textContent ?? "";
    expect(summary).toContain("0 models");
    expect(summary).not.toContain("Jan 1");
    expect(summary).not.toContain("UTC");
    expect(q('[data-testid="card-model-backends"]')!.textContent).toContain(
      "printed no model names",
    );
  });

  it("offers no Refresh to a person who cannot ask providers for their models", async () => {
    const api = new FakeApi();
    api.access = {
      role: "grain/viewer",
      full: false,
      scopes: ["settings:read", "connections:read", "deployment:read"],
    };
    const { q, el } = await mount(api);
    expect(q('[data-testid="refresh-backend-agy"]')).toBeNull();
    q<HTMLButtonElement>('[data-testid="picker-default-model"]')!.click();
    await new Promise((r) => setTimeout(r, 20));
    const refresh = [...el.querySelectorAll("button")].filter((b) =>
      /refresh/i.test(b.textContent ?? ""),
    );
    expect(refresh).toEqual([]);
  });

  it("refreshes every discovering connection from inside the picker", async () => {
    const { q, api, settle } = await mount();
    q<HTMLButtonElement>('[data-testid="picker-default-model"]')!.click();
    await settle();
    expect(q('[data-testid="picker-default-model-panel"]')).not.toBeNull();
    q<HTMLButtonElement>(
      '[data-testid="picker-default-model-refresh"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 20));
    await settle();
    const calls = api.calls.filter(
      (c) =>
        c.key === "POST /api/deployment/agent-backends/{backendId}/discover",
    );
    expect(calls.map((c) => c.params)).toEqual([{ backendId: "agy" }]);
  });

  it("picks the model for a reasoning-effort setting from the same picker", async () => {
    const { pick, q, save, api } = await mount();
    await pick("picker-model-setting-model", "gemini-flash");
    q<HTMLButtonElement>('[data-testid="add-model-setting-btn"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    expect(q('[data-testid="model-setting-row-gemini-flash"]')).not.toBeNull();
    await save();
    const put = api.calls.find((c) => c.key === "PUT /api/deployment");
    expect(put?.body).toEqual({
      modelSettings: [{ model: "gemini-flash", effort: "high" }],
    });
  });

  it("pairs each default and fallback model with its own effort, only where it takes one", async () => {
    const api = new FakeApi();
    api.settingsBody["defaultModel"] =
      "gemini-pro,gpt-codex,opus-5,gemini-flash-high";
    api.settingsBody["agentBackends"] = [
      {
        id: "agy",
        framework: "antigravity",
        discovered: { models: ["gemini-pro", "gemini-flash-high"] },
      },
      { id: "cx", framework: "codex", models: ["gpt-codex"] },
      { id: "claude", framework: "claude", models: ["opus-5"] },
    ];
    api.deploymentBody["modelSettings"] = [
      { model: "gpt-codex", effort: "xhigh" },
      { model: "pinned-elsewhere", effort: "low" },
    ];
    const { q, save, settle } = await mount(api);
    const choose = async (testid: string, value: string) => {
      q<HTMLButtonElement>(`[data-testid="${testid}"]`)!.click();
      await settle();
      const opt = q(`[data-testid="${testid}-opt-${value}"]`);
      if (!opt) throw new Error(`${testid} does not offer ${value}`);
      opt.click();
      await settle();
    };

    // The default (agy, bare family name) takes a separate effort.
    expect(q('[data-testid="effort-default-model"]')).not.toBeNull();
    // Codex: its own vocabulary, showing the stored effort.
    expect(q('[data-testid="effort-fallback-0"]')?.textContent).toContain(
      "xhigh",
    );
    // Claude takes none; an agy name that spells its effort takes none.
    expect(q('[data-testid="effort-fallback-1"]')).toBeNull();
    expect(q('[data-testid="effort-fallback-none-1"]')?.textContent).toContain(
      "Claude takes no reasoning effort",
    );
    expect(q('[data-testid="effort-fallback-2"]')).toBeNull();
    expect(q('[data-testid="effort-fallback-none-2"]')?.textContent).toContain(
      "Effort is part of the model name",
    );

    // A chained model's effort lives on its row, not in the table below.
    expect(q('[data-testid="model-setting-row-gpt-codex"]')).toBeNull();
    expect(
      q('[data-testid="model-setting-row-pinned-elsewhere"]'),
    ).not.toBeNull();

    await choose("effort-default-model", "medium");
    await choose("effort-fallback-0", "");
    await save();
    const put = api.calls.find((c) => c.key === "PUT /api/deployment");
    expect(put?.body).toEqual({
      modelSettings: [
        { model: "pinned-elsewhere", effort: "low" },
        { model: "gemini-pro", effort: "medium" },
      ],
    });
  });

  it("reconciles the other-model effort adder across frameworks, omits Model default, and disables effort when unavailable", async () => {
    const api = new FakeApi();
    api.settingsBody["defaultModel"] = "gemini-pro";
    api.deploymentBody = {
      defaultModel: "gemini-pro",
      defaultModels: ["gemini-pro"],
      modelSettings: [{ model: "gemini-ultra", effort: "low" }],
    };
    api.settingsBody["agentBackends"] = [
      {
        id: "agy",
        framework: "antigravity",
        discovered: {
          models: ["gemini-pro", "gemini-2.5-flash", "gemini-ultra"],
        },
      },
      { id: "cx", framework: "codex", models: ["gpt-codex"] },
      { id: "claude", framework: "claude", models: ["claude-sonnet"] },
    ];
    const { el, pick, q, save, settle } = await mount(api);
    const effortBtn = () =>
      q<HTMLButtonElement>('[data-testid="select-model-setting-effort"]')!;
    const addBtn = () =>
      q<HTMLButtonElement>('[data-testid="add-model-setting-btn"]')!;
    const effortMenuValues = async () => {
      effortBtn().click();
      await settle();
      const opts = [
        ...el.querySelectorAll<HTMLElement>(
          '[data-testid^="select-model-setting-effort-opt-"]',
        ),
      ].map((o) =>
        (o.dataset["testid"] ?? "").replace(
          "select-model-setting-effort-opt-",
          "",
        ),
      );
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
      await settle();
      return opts;
    };

    // Disabled before any model is chosen.
    expect(effortBtn().disabled).toBe(true);
    expect(addBtn().disabled).toBe(true);

    // Pick Codex, confirm no empty "Model default" option, and select xhigh.
    await pick("picker-model-setting-model", "gpt-codex");
    expect(effortBtn().disabled).toBe(false);
    expect(await effortMenuValues()).toEqual([
      "minimal",
      "low",
      "medium",
      "high",
      "xhigh",
    ]);
    effortBtn().click();
    await settle();
    q('[data-testid="select-model-setting-effort-opt-xhigh"]')!.click();
    await settle();
    expect(effortBtn().textContent).toContain("xhigh");

    // Switch to an Antigravity model: xhigh reconciles to high, never leaking.
    await pick("picker-model-setting-model", "gemini-2.5-flash");
    expect(effortBtn().disabled).toBe(false);
    expect(effortBtn().textContent).toContain("high");
    expect(effortBtn().textContent).not.toContain("xhigh");
    expect(await effortMenuValues()).toEqual(["low", "medium", "high"]);

    // Picking a model that already has a stored effort reconciles to that effort.
    await pick("picker-model-setting-model", "gemini-ultra");
    expect(effortBtn().textContent).toContain("low");

    // Picking a Claude model disables both the effort select and Set model effort.
    await pick("picker-model-setting-model", "claude-sonnet");
    expect(effortBtn().disabled).toBe(true);
    expect(addBtn().disabled).toBe(true);
    expect(
      q('[data-testid="model-setting-effort-none"]')?.textContent,
    ).toContain("Claude takes no reasoning effort");

    // Switching back to gemini-2.5-flash recovers to high and saves cleanly.
    await pick("picker-model-setting-model", "gemini-2.5-flash");
    expect(effortBtn().disabled).toBe(false);
    expect(effortBtn().textContent).toContain("high");
    addBtn().click();
    await settle();
    await save();
    const put = api.calls.find((c) => c.key === "PUT /api/deployment");
    expect(put?.body).toEqual({
      modelSettings: [
        { model: "gemini-ultra", effort: "low" },
        { model: "gemini-2.5-flash", effort: "high" },
      ],
    });
  });

  it("leaves no stuck save bar when reverting an effort to Model default with omitted modelSettings", async () => {
    const api = new FakeApi();
    // GET /api/deployment omits modelSettings when empty (omitempty).
    delete api.deploymentBody["modelSettings"];
    const { q, settle } = await mount(api);
    const choose = async (value: string) => {
      q<HTMLButtonElement>('[data-testid="effort-default-model"]')!.click();
      await settle();
      q(`[data-testid="effort-default-model-opt-${value}"]`)!.click();
      await settle();
    };

    // Choosing Model default on an already-unset model stays clean.
    await choose("");
    expect(q('[data-testid="settings-save-bar"]')).toBeNull();

    // Setting an effort stages 1 unsaved change; reverting to Model default clears it.
    await choose("medium");
    expect(q('[data-testid="settings-save-bar"]')?.textContent).toContain(
      "1 unsaved change",
    );
    await choose("");
    expect(q('[data-testid="settings-save-bar"]')).toBeNull();
  });

  it("marks stale stored effort on no-effort default and fallback models as not applied and lets them be cleared", async () => {
    const api = new FakeApi();
    api.settingsBody["defaultModel"] =
      "opus-5.5-high,auto,claude-opus-5-5,gemini-pro";
    api.settingsBody["agentBackends"] = [
      {
        id: "agy",
        framework: "antigravity",
        models: ["opus-5.5-high", "auto", "gemini-pro"],
      },
      {
        id: "claude",
        framework: "claude",
        models: ["claude-opus-5-5"],
      },
    ];
    api.deploymentBody = {
      defaultModel: "opus-5.5-high,auto,claude-opus-5-5,gemini-pro",
      defaultModels: ["opus-5.5-high", "auto", "claude-opus-5-5", "gemini-pro"],
      modelSettings: [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
        { model: "gemini-pro", effort: "bogus" },
      ],
    };
    const { q, save, settle } = await mount(api);
    const choose = async (testid: string, value: string) => {
      q<HTMLButtonElement>(`[data-testid="${testid}"]`)!.click();
      await settle();
      q(`[data-testid="${testid}-opt-${value}"]`)!.click();
      await settle();
    };

    // Default model (opus-5.5-high): flagged (not applied) with reason.
    expect(q('[data-testid="effort-default-model"]')?.textContent).toContain(
      "low (not applied)",
    );
    expect(
      q('[data-testid="effort-default-model-unused"]')?.textContent,
    ).toContain(
      'Not applied: Effort is part of the model name: high. Choose "Model default" to clear it.',
    );

    // Fallback 0 (auto) and Fallback 1 (claude-opus-5-5): flagged (not applied) with reason.
    expect(q('[data-testid="effort-fallback-0"]')?.textContent).toContain(
      "high (not applied)",
    );
    expect(
      q('[data-testid="effort-fallback-unused-0"]')?.textContent,
    ).toContain(
      'Not applied: The provider chooses the effort. Choose "Model default" to clear it.',
    );
    expect(q('[data-testid="effort-fallback-1"]')?.textContent).toContain(
      "high (not applied)",
    );
    expect(
      q('[data-testid="effort-fallback-unused-1"]')?.textContent,
    ).toContain(
      'Not applied: Claude takes no reasoning effort. Choose "Model default" to clear it.',
    );

    // Fallback 2 (gemini-pro, bare model with unknown effort): still says (not offered) and no unused hint.
    expect(q('[data-testid="effort-fallback-2"]')?.textContent).toContain(
      "bogus (not offered)",
    );
    expect(q('[data-testid="effort-fallback-unused-2"]')).toBeNull();

    // Clearing all three stale no-effort entries collapses them back to normal no-effort hints.
    await choose("effort-default-model", "");
    await choose("effort-fallback-0", "");
    await choose("effort-fallback-1", "");
    expect(q('[data-testid="effort-default-model"]')).toBeNull();
    expect(
      q('[data-testid="effort-default-model-none"]')?.textContent,
    ).toContain("Effort is part of the model name: high.");
    expect(q('[data-testid="effort-fallback-0"]')).toBeNull();
    expect(q('[data-testid="effort-fallback-none-0"]')?.textContent).toContain(
      "The provider chooses the effort",
    );
    expect(q('[data-testid="effort-fallback-1"]')).toBeNull();
    expect(q('[data-testid="effort-fallback-none-1"]')?.textContent).toContain(
      "Claude takes no reasoning effort",
    );

    await save();
    const put = api.calls.find((c) => c.key === "PUT /api/deployment");
    expect(put?.body).toEqual({
      modelSettings: [{ model: "gemini-pro", effort: "bogus" }],
    });
  });
});

describe("Settings model connection tests", () => {
  for (const framework of ["antigravity", "claude", "codex"]) {
    it(`tests saved ${framework} credentials without saving an edit`, async () => {
      const api = new FakeApi();
      api.settingsBody["agentBackends"] = [{ id: "own", framework }];
      const { q, settle } = await mount(api);
      q<HTMLButtonElement>('[data-testid="edit-backend-own"]')!.click();
      await settle();
      const name = q<HTMLInputElement>(
        '[data-testid="input-new-backend-name"]',
      )!;
      name.value = "Unsaved edit";
      name.dispatchEvent(new Event("input", { bubbles: true }));
      api.calls = [];
      q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.click();
      await settle();
      const call = api.calls.find(
        (c) => c.key === "POST /api/agent-keys/{framework}/check",
      )!;
      expect(call.params).toEqual({ framework });
      expect(call.query).toBe("backend=own");
      expect(call.body).toBeUndefined();
      expect(
        api.calls.some(
          (c) => c.method === "PUT" || c.key === "POST /api/settings",
        ),
      ).toBe(false);
      expect(q('[data-testid="test-result-own"]')?.textContent).toContain(
        "Provider accepted",
      );
      expect(name.value).toBe("Unsaved edit");
      api.testAnswer = {
        ok: false,
        detail: "Provider refused; replace credential",
      };
      q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.click();
      await settle();
      expect(q('[data-testid="test-result-own"]')?.textContent).toContain(
        "Provider refused",
      );
      api.testError = true;
      q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.click();
      await settle();
      expect(q('[data-testid="test-result-own"]')?.textContent).toContain(
        "Could not test",
      );
      expect(q('[data-testid="test-result-own"]')?.textContent).not.toContain(
        "private-credential",
      );
      expect(
        q<HTMLButtonElement>('[data-testid="test-backend-own"]')?.disabled,
      ).toBe(false);
    });
  }
  it("validates auth.json locally and keeps a failed upload available for retry", async () => {
    const api = new FakeApi();
    api.settingsBody["agentBackends"] = [
      { id: "own", framework: "codex", authMode: "subscription" },
    ];
    const { q, settle } = await mount(api);
    q<HTMLButtonElement>('[data-testid="toggle-cred-own"]')!.click();
    await settle();
    const input = q<HTMLTextAreaElement>(
      '[data-testid="input-codex-auth-own"]',
    )!;
    const upload = async (value: string) => {
      input.value = value;
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await settle();
      q<HTMLButtonElement>('[data-testid="save-codex-auth-own"]')!.click();
      await settle();
    };
    for (const value of ['{"secret":"private-credential",', "[]", "null"]) {
      await upload(value);
      expect(q('[data-testid="codex-auth-error-own"]')?.textContent).toContain(
        "JSON object",
      );
    }
    expect(
      api.calls.some(
        (c) => c.key === "PUT /api/agent-backends/{id}/codex-auth",
      ),
    ).toBe(false);
    api.put = () => {
      throw new Error("private-credential");
    };
    await upload('{"tokens":{"refresh_token":"private-credential"}}');
    expect(q('[data-testid="codex-auth-error-own"]')?.textContent).toContain(
      "Could not save",
    );
    expect(
      q('[data-testid="codex-auth-error-own"]')?.textContent,
    ).not.toContain("private-credential");
    expect(input.value).toContain("private-credential");
    expect(
      q<HTMLButtonElement>('[data-testid="save-codex-auth-own"]')?.disabled,
    ).toBe(false);
  });
});

it("discards an in-flight test when the saved credential changes", async () => {
  const api = new FakeApi();
  api.settingsBody["agentBackends"] = [
    { id: "own", framework: "claude", authMode: "api_key" },
  ];
  let finish!: (value: { ok: boolean; detail: string }) => void;
  api.post = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const { q, settle } = await mount(api);
  q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.click();
  await settle();
  expect(
    q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.disabled,
  ).toBe(true);
  q<HTMLButtonElement>('[data-testid="toggle-cred-own"]')!.click();
  await settle();
  const input = q<HTMLInputElement>('input[aria-label="Credential for own"]')!;
  input.value = "new-private-key";
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await settle();
  q<HTMLButtonElement>(
    '[data-testid="backend-cred-row-own"] button[type="submit"]',
  )!.click();
  await settle();
  finish({ ok: true, detail: "Accepted previous credential" });
  await settle();
  expect(q('[data-testid="test-result-own"]')).toBeNull();
  expect(
    q<HTMLButtonElement>('[data-testid="test-backend-own"]')!.disabled,
  ).toBe(false);
});
