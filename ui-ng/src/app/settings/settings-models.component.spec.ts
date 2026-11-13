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

  config = () =>
    Promise.resolve({
      version: "v3.6.0",
      configRepoOnly: false,
      access: { role: "grain/operator", full: true, scopes: [] },
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
  post = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, params, body });
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
});
