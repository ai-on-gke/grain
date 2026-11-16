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

// Settings → Hub: the status card, the catalog & authentication form, the
// registration token and the Scope overrides card, driven the way a
// person does (clicks and typing) against a fake API.

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import { provideRouter, withComponentInputBinding } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import { SettingsPageComponent } from "./settings-page.component";
import {
  computeSettingsPlan,
  resolveSettingsSection,
  sectionMeta,
  settingsAnchor,
} from "./settings-sections";

interface Call {
  method: string;
  key: string;
  params?: RouteParams;
  body?: unknown;
}

const HUB = {
  url: "https://hub.example.test",
  entry: "acme-core",
  authMode: "token",
  purpose: "Builds acme",
  scopes: [
    { repo: "acme/monorepo", pathPrefix: "services/billing", role: "primary" },
    { repo: "acme/monorepo", pathPrefix: "services/web", exclude: true },
  ],
};

function snapshot(over: Record<string, unknown> = {}) {
  return {
    enabled: true,
    state: "connected",
    config: { url: HUB.url, entry: HUB.entry },
    effectiveHostKind: "workstation",
    lastHeartbeatAt: "2026-11-16T05:00:00Z",
    heartbeatIntervalSeconds: 15,
    health: "ok",
    activeTasks: 1,
    awaitingReplyTasks: 0,
    advertisedScopes: [
      { repo: "acme/widgets", role: "primary", origin: "repo_config" },
      { repo: "acme/docs", role: "secondary", origin: "repo_config" },
    ],
    ...over,
  };
}

class FakeApi {
  calls: Call[] = [];
  settingsBody: Record<string, unknown> = {
    hub: structuredClone(HUB),
    hubTokenSet: false,
  };
  configRepoOnly = false;
  secretsEnabled = true;
  registry: Record<string, unknown> = snapshot();
  // A GET /api/registry the test answers by hand, to order replies.
  held: ((v: unknown) => void)[] = [];
  holdRegistry = false;
  heartbeatAnswer: Record<string, unknown> = snapshot({
    lastHeartbeatAt: "2026-11-16T05:01:00Z",
  });
  // Resolves a token PUT the test holds open.
  releasePut: (() => void) | null = null;
  holdPut = false;

  access: Record<string, unknown> = {
    role: "grain/operator",
    full: true,
    scopes: [],
  };
  config = () =>
    Promise.resolve({
      version: "v3.6.0",
      configRepoOnly: this.configRepoOnly,
      access: this.access,
    });
  get = (key: string) => {
    this.calls.push({ method: "GET", key });
    if (key === "GET /api/settings") {
      return Promise.resolve({
        ...structuredClone(this.settingsBody),
        configRepoOnly: this.configRepoOnly,
      });
    }
    if (key === "GET /api/secrets") {
      return Promise.resolve({ enabled: this.secretsEnabled, secrets: [] });
    }
    if (key === "GET /api/registry") {
      if (this.holdRegistry) {
        return new Promise((resolve) => this.held.push(resolve));
      }
      return Promise.resolve(structuredClone(this.registry));
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
    if (key === "POST /api/registry/heartbeat") {
      return Promise.resolve(structuredClone(this.heartbeatAnswer));
    }
    return Promise.resolve({ ok: true });
  };
  put = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, params, body });
    if (key === "PUT /api/secrets/{secret}/{key}") {
      const done = () => {
        this.settingsBody["hubTokenSet"] = true;
      };
      if (this.holdPut) {
        return new Promise((resolve) => {
          this.releasePut = () => {
            done();
            resolve({ ok: true });
          };
        });
      }
      done();
    }
    return Promise.resolve({ ok: true });
  };
  patch = () => Promise.resolve({ ok: true });
  delete = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "DELETE", key, params });
    this.settingsBody["hubTokenSet"] = false;
    return Promise.resolve({ ok: true });
  };
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
  const harness = await RouterTestingHarness.create("/settings/hub");
  harness.fixture.autoDetectChanges(true);
  const el = harness.fixture.nativeElement as HTMLElement;
  const settle = async () => {
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 5));
      await harness.fixture.whenStable();
    }
  };
  await settle();
  const q = <T extends HTMLElement = HTMLElement>(sel: string) =>
    el.querySelector<T>(sel);
  const tid = <T extends HTMLElement = HTMLElement>(id: string) =>
    q<T>(`[data-testid="${id}"]`);
  const type = async (id: string, value: string) => {
    const input = tid<HTMLInputElement>(id)!;
    input.value = value;
    input.dispatchEvent(new Event("input"));
    await settle();
  };
  return { api, el, q, tid, type, settle };
}

describe("Settings -> Hub", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("is the hub section, titled Hub, and the old registry name still opens it", () => {
    expect(resolveSettingsSection("hub")).toBe("hub");
    expect(resolveSettingsSection("registry")).toBe("hub");
    expect(sectionMeta("hub").label).toBe("Hub");
    // An old /settings/registry link still lands on the status card.
    expect(settingsAnchor("registry", "")).toBe("registry-status");
    expect(settingsAnchor("hub", "registry-scopes")).toBe("registry-scopes");
  });

  it("shows status, scope roles and rule modes in words with a shape", async () => {
    const { tid, el } = await mount();
    const pill = tid("registry-status-pill")!;
    expect(pill.textContent?.trim()).toBe("Connected");
    expect(pill.querySelector("g-status-icon")).not.toBeNull();
    expect(pill.classList.contains("s-mono")).toBe(false);

    const roles = [
      ...el.querySelectorAll('[data-testid="registry-scope-role"]'),
    ];
    expect(roles.map((r) => r.textContent?.trim())).toEqual([
      "Primary",
      "Secondary",
    ]);
    expect(roles.every((r) => r.querySelector("g-status-icon"))).toBe(true);

    const modes = [
      ...el.querySelectorAll('[data-testid="registry-rule-mode"]'),
    ];
    expect(modes.map((r) => r.textContent?.trim())).toEqual([
      "Primary",
      "Excluded",
    ]);
    expect(modes.every((r) => r.querySelector("g-status-icon"))).toBe(true);

    // Two rules on one repository are told apart by their path prefix.
    const removes = [
      ...el.querySelectorAll<HTMLButtonElement>("#registry-scopes button"),
    ].filter((b) => b.textContent?.trim() === "Remove");
    expect(removes.map((b) => b.getAttribute("aria-label"))).toEqual([
      "Remove scope rule for acme/monorepo (services/billing/)",
      "Remove scope rule for acme/monorepo (services/web/)",
    ]);
  });

  it("keeps where the credential goes for secrets:write, and says why", async () => {
    const api = new FakeApi();
    api.access = {
      role: "grain/editor",
      full: false,
      scopes: ["settings:read", "settings:write", "secrets:read"],
    };
    const { tid, q, type, api: fake, el } = await mount(api);
    for (const sel of ["#reg-url", "#reg-entry", "#reg-aud"]) {
      expect(q<HTMLInputElement>(sel)!.disabled, sel).toBe(true);
    }
    expect(tid<HTMLButtonElement>("select-hub-auth-mode")!.disabled).toBe(true);
    expect(tid("registry-auth-target-denied")!.textContent).toContain(
      "secrets:write",
    );
    // The catalog fields need only settings:write, and save.
    expect(q<HTMLInputElement>("#reg-purpose")!.disabled).toBe(false);
    await type("input-hub-purpose", "Builds acme and its docs");
    const save = el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-save-btn"]',
    )!;
    save.click();
    await new Promise((r) => setTimeout(r, 30));
    const sent = fake.calls.find((c) => c.key === "POST /api/settings");
    expect(
      (sent?.body as { hub: Record<string, unknown> }).hub["purpose"],
    ).toBe("Builds acme and its docs");
  });

  it("says why Heartbeat now is off for a viewer", async () => {
    const api = new FakeApi();
    api.access = {
      role: "grain/viewer",
      full: false,
      scopes: ["settings:read"],
    };
    const { tid, q } = await mount(api);
    expect(tid<HTMLButtonElement>("registry-heartbeat-btn")!.disabled).toBe(
      true,
    );
    expect(tid("registry-heartbeat-denied")!.textContent).toContain(
      "settings:write",
    );
    expect(q<HTMLInputElement>("#reg-purpose")!.disabled).toBe(true);
    // A viewer is not also told about secrets:write.
    expect(tid("registry-auth-target-denied")).toBeNull();
  });

  it("marks the section as managed in the config repo when it is", async () => {
    const api = new FakeApi();
    api.configRepoOnly = true;
    const { tid, q } = await mount(api);
    expect(tid("section-readonly-pill")).not.toBeNull();
    expect(tid("settings-config-repo-banner")!.textContent).toContain(
      "Secrets and API keys remain editable",
    );
    expect(q<HTMLInputElement>("#reg-url")!.disabled).toBe(true);
    expect(tid<HTMLInputElement>("input-hub-token")!.disabled).toBe(false);
  });

  it("stores the token once, saying so while it is in flight", async () => {
    const api = new FakeApi();
    api.holdPut = true;
    const { tid, type, settle } = await mount(api);
    expect(tid("registry-token-state")!.textContent?.trim()).toBe("Not set");
    const store = tid<HTMLButtonElement>("save-hub-token")!;
    expect(store.textContent?.trim()).toBe("Store");
    await type("input-hub-token", "grc_unit_token");
    store.click();
    await settle();
    expect(store.textContent?.trim()).toBe("Storing…");
    expect(store.disabled).toBe(true);
    expect(tid<HTMLInputElement>("input-hub-token")!.disabled).toBe(true);
    api.releasePut!();
    await settle();
    expect(
      api.calls.filter((c) => c.method === "PUT").map((c) => c.body),
    ).toEqual([{ value: "grc_unit_token" }]);
    expect(tid("registry-token-state")!.textContent?.trim()).toBe("Stored");
    expect(store.textContent?.trim()).toBe("Replace");
    expect(tid<HTMLInputElement>("input-hub-token")!.value).toBe("");
  });

  it("forgets a typed token on Discard", async () => {
    const { tid, type, el, settle } = await mount();
    await type("input-hub-token", "grc_typed");
    await type("input-hub-purpose", "changed");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-discard-btn"]',
    )!.click();
    await settle();
    expect(tid<HTMLInputElement>("input-hub-token")!.value).toBe("");
  });

  it("keeps the newest snapshot when an older read answers after a heartbeat", async () => {
    const api = new FakeApi();
    // The page's first GET /api/registry is slow, and the hub refused
    // when it was taken; the heartbeat sent meanwhile is newer.
    api.holdRegistry = true;
    api.registry = snapshot({ state: "error", lastError: "stale refusal" });
    const { tid, settle } = await mount(api);
    expect(api.held.length).toBeGreaterThan(0);
    // Saved settings say the hub is on, so it can be sent before any
    // snapshot answers.
    const beat = tid<HTMLButtonElement>("registry-heartbeat-btn")!;
    expect(beat.disabled).toBe(false);
    beat.click();
    await settle();
    expect(tid("registry-status-pill")!.textContent?.trim()).toBe("Connected");
    for (const resolve of api.held) resolve(structuredClone(api.registry));
    await settle();
    expect(tid("registry-status-pill")!.textContent?.trim()).toBe("Connected");
    expect(tid("registry-last-error")).toBeNull();
  });

  it("does not show a disabled hub's leftover error, and reads a newly saved hub as configured", async () => {
    const api = new FakeApi();
    api.settingsBody["hub"] = {};
    api.registry = {
      enabled: false,
      state: "disabled",
      config: {},
      lastError: "hub refused heartbeat: 401",
      scopeConflicts: [{ repo: "acme/x", conflicting_entry: "other" }],
    };
    const { tid, type, el, settle } = await mount(api);
    expect(tid("registry-status-pill")!.textContent?.trim()).toBe(
      "Not configured",
    );
    expect(tid("registry-last-error")).toBeNull();
    expect(tid("registry-scope-conflicts")).toBeNull();

    // Saving a URL and entry: the re-read hangs, but the card follows
    // the saved settings rather than the snapshot from before.
    await type("input-hub-url", HUB.url);
    await type("input-hub-entry", HUB.entry);
    api.holdRegistry = true;
    el.querySelector<HTMLButtonElement>(
      '[data-testid="settings-save-btn"]',
    )!.click();
    await settle();
    await settle();
    expect(tid("registry-status-pill")!.textContent?.trim()).toBe("Connecting");
    expect(tid<HTMLButtonElement>("registry-heartbeat-btn")!.disabled).toBe(
      false,
    );
    expect(tid("registry-last-error")).toBeNull();
  });

  it("shows a long hub error as capped Markdown", async () => {
    const api = new FakeApi();
    api.registry = snapshot({
      state: "error",
      lastError: "first paragraph\n\nsecond paragraph",
    });
    const { tid } = await mount(api);
    const err = tid("registry-last-error")!;
    expect(err.getAttribute("role")).toBe("alert");
    expect(err.querySelector("g-status-icon")).not.toBeNull();
    expect(err.querySelector("g-capped-markdown .capped-md")).not.toBeNull();
    expect(err.querySelectorAll("p").length).toBe(2);
  });

  it("plans a hub change field by field, not as raw JSON", () => {
    const before = { hub: structuredClone(HUB) };
    const after = { hub: { ...structuredClone(HUB), purpose: "New purpose" } };
    const rows = computeSettingsPlan(before, after, after);
    expect(rows).toEqual([
      {
        field: "hub.purpose",
        label: "Hub purpose",
        was: "Builds acme",
        becomes: "New purpose",
        changed: true,
        typed: null,
      },
    ]);
    const scopes = computeSettingsPlan(
      { hub: { url: HUB.url } },
      { hub: { url: HUB.url, scopes: HUB.scopes } },
      null,
    );
    expect(scopes.map((r) => [r.label, r.becomes])).toEqual([
      [
        "Hub scope overrides",
        "acme/monorepo (services/billing/) primary, acme/monorepo (services/web/) excluded",
      ],
    ]);
    expect(rows.some((r) => r.becomes.includes("{"))).toBe(false);
  });
});
