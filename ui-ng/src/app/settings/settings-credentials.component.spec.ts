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

// Settings → Capabilities → Deployment credentials: the GitHub host and
// GCP fields, and the secrets list ported from the classic SecretsPanel.

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import { provideRouter, withComponentInputBinding } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import { claimedSecret } from "./settings-credentials.component";
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
    githubHost: "ghe.example.com",
    gcpProject: "my-project-123",
    gcpServiceAccountCount: 2,
    agentBackends: [{ id: "claude", framework: "claude" }],
  };
  secrets: Record<string, unknown> = {
    enabled: true,
    secrets: [
      { name: "agent-backend-claude", keys: ["value"] },
      { name: "cosign-key", keys: ["value"] },
      { name: "github-app", keys: ["app-id", "private-key"] },
      { name: "jira", keys: ["token"], capabilityServers: ["jira-mcp"] },
      { name: "repo-release-acme-widgets", keys: ["value"] },
    ],
  };
  access: Record<string, unknown> = {
    role: "grain/operator",
    full: true,
    scopes: [],
  };
  settingsLocked = false;
  config = () =>
    Promise.resolve({
      version: "v3.6.0",
      configRepoOnly: false,
      settingsLocked: this.settingsLocked,
      access: this.access,
    });
  get = (key: string) => {
    this.calls.push({ method: "GET", key });
    if (key === "GET /api/settings") {
      return Promise.resolve({
        ...structuredClone(this.settingsBody),
        settingsLocked: this.settingsLocked,
      });
    }
    if (key === "GET /api/secrets") {
      return Promise.resolve(structuredClone(this.secrets));
    }
    return Promise.resolve({});
  };
  getText = () => Promise.resolve("");
  post = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, params, body });
    return Promise.resolve({ ok: true });
  };
  put = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, params, body });
    return Promise.resolve({ ok: true });
  };
  patch = () => Promise.resolve({ ok: true });
  delete = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "DELETE", key, params });
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
  const harness = await RouterTestingHarness.create("/settings/capabilities");
  harness.fixture.autoDetectChanges(true);
  const settle = async () => {
    await new Promise((r) => setTimeout(r, 20));
    await harness.fixture.whenStable();
  };
  await settle();
  await settle();
  const el = harness.fixture.nativeElement as HTMLElement;
  const q = <T extends HTMLElement = HTMLElement>(sel: string) =>
    el.querySelector<T>(sel);
  const btn = (label: string) =>
    q<HTMLButtonElement>(`button[aria-label="${label}"]`);
  return { api, el, q, btn, settle };
}

describe("claimedSecret", () => {
  it("leaves model connection and release credentials to their pages", () => {
    expect(claimedSecret("agent-backend-claude", ["claude"])).toBe(true);
    expect(claimedSecret("agent-backend-gone", ["claude"])).toBe(false);
    expect(claimedSecret("repo-release-acme-widgets", [])).toBe(true);
    expect(claimedSecret("registry-registration-token", [])).toBe(true);
    expect(claimedSecret("github-app", ["claude"])).toBe(false);
  });
});

describe("Deployment credentials card", () => {
  it("shows the fields and lists each unclaimed key, masked", async () => {
    const { q } = await mount();
    const card = q('[data-testid="card-deployment-credentials"]')!;
    expect(card.id).toBe("credentials");
    expect(q<HTMLInputElement>("#cred-github-host")!.value).toBe(
      "ghe.example.com",
    );
    expect(q<HTMLInputElement>("#cred-gcp-count")!.value).toBe("2");
    expect(q('[data-testid="secret-row-cosign-key-value"]')).not.toBeNull();
    expect(
      q('[data-testid="secret-row-github-app-private-key"]'),
    ).not.toBeNull();
    expect(
      q('[data-testid="secret-row-agent-backend-claude-value"]'),
    ).toBeNull();
    expect(
      q('[data-testid="secret-row-repo-release-acme-widgets-value"]'),
    ).toBeNull();
    // A capability server's own secret offers no Replace or Remove.
    const owned = q('[data-testid="secret-row-jira-token"]')!;
    expect(owned.querySelectorAll("button").length).toBe(0);
    expect(owned.textContent).toContain("jira-mcp");
  });

  it("replaces a key through the form and removes a last key as its secret", async () => {
    const { api, q, btn, settle } = await mount();
    btn("Replace cosign-key/value")!.click();
    await settle();
    expect(q<HTMLInputElement>("#cred-secret-name")!.value).toBe("cosign-key");
    const value = q<HTMLTextAreaElement>("#cred-secret-value")!;
    expect(document.activeElement).toBe(value);
    value.value = "s3cr3t";
    value.dispatchEvent(new Event("input"));
    await settle();
    const submit = q<HTMLButtonElement>('[data-testid="store-secret-btn"]')!;
    expect(submit.textContent).toContain("Replace secret");
    submit.click();
    await settle();
    expect(api.calls).toContainEqual({
      method: "PUT",
      key: "PUT /api/secrets/{secret}/{key}",
      params: { secret: "cosign-key", key: "value" },
      body: { value: "s3cr3t" },
    });

    btn("Remove cosign-key/value")!.click();
    await settle();
    const accept = [
      ...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button"),
    ].find((b) => b.textContent?.trim() === "Remove")!;
    accept.click();
    await settle();
    expect(api.calls).toContainEqual({
      method: "DELETE",
      key: "DELETE /api/secrets/{secret}",
      params: { secret: "cosign-key" },
    });

    btn("Remove github-app/app-id")!.click();
    await settle();
    [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")]
      .find((b) => b.textContent?.trim() === "Remove")!
      .click();
    await settle();
    expect(api.calls).toContainEqual({
      method: "DELETE",
      key: "DELETE /api/secrets/{secret}/{key}",
      params: { secret: "github-app", key: "app-id" },
    });
  });

  it("locks the fields but not the secrets while settings are locked", async () => {
    const api = new FakeApi();
    api.settingsLocked = true;
    const { q, btn } = await mount(api);
    expect(q<HTMLInputElement>("#cred-github-host")!.disabled).toBe(true);
    expect(q<HTMLInputElement>("#cred-gcp-project")!.disabled).toBe(true);
    expect(btn("Replace cosign-key/value")!.disabled).toBe(false);
    expect(q('[data-testid="credentials-read-only"]')!.textContent).toContain(
      "config repository",
    );
  });

  it("is read-only for a role without settings:write or secrets:write", async () => {
    const api = new FakeApi();
    api.access = {
      role: "grain/viewer",
      full: false,
      scopes: ["settings:read", "capabilities:read", "secrets:read"],
    };
    const { q, btn } = await mount(api);
    expect(q<HTMLInputElement>("#cred-github-host")!.disabled).toBe(true);
    expect(btn("Replace cosign-key/value")!.disabled).toBe(true);
    expect(btn("Remove cosign-key/value")!.disabled).toBe(true);
    expect(q<HTMLTextAreaElement>("#cred-secret-value")!.disabled).toBe(true);
    expect(q('[data-testid="credentials-read-only"]')!.textContent).toContain(
      "Read-only for you",
    );
  });
});
