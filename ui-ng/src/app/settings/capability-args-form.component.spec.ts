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

import { Component, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import type { JsonSchema } from "../shared/capability-args";
import {
  CapabilityArgsFormComponent,
  type ServerConfigSecretsContext,
} from "./capability-args-form.component";

@Component({
  imports: [CapabilityArgsFormComponent],
  template: `
    <g-capability-args-form
      [schema]="schema()"
      [value]="value()"
      [errors]="errors()"
      [secrets]="secrets()"
      idPrefix="test-caf"
      (valueChange)="valueChanges.push($event); value.set($event)"
      (secretChange)="secretChanges.push($event)"
      (rotateManaged)="rotated.push($event)"
    />
  `,
})
class HostComponent {
  readonly schema = signal<JsonSchema>({
    type: "object",
    required: ["auth_mode"],
    properties: {
      auth_mode: {
        type: "string",
        title: "Authentication",
        description: "How GCP credentials are issued.",
        enum: ["keyless", "service_account_key"],
        "x-grain-enum-labels": {
          keyless: "Keyless token (recommended)",
          service_account_key: "Service-account key JSON",
        },
      },
      impersonate: {
        type: "boolean",
        title: "Impersonate service account",
        default: false,
      },
      scopes: {
        type: "array",
        title: "Scopes",
        items: { type: "string" },
      },
      labels: {
        type: "object",
        title: "Labels",
        additionalProperties: { type: "string" },
      },
      service_account_key: {
        type: "string",
        title: "Service-account key",
        description: "Stored encrypted.",
        writeOnly: true,
      },
      managed_token: {
        type: "string",
        title: "Managed token",
        writeOnly: true,
      },
    },
  });
  readonly value = signal<unknown>({
    auth_mode: "keyless",
    impersonate: false,
    scopes: ["https://www.googleapis.com/auth/cloud-platform"],
    labels: { env: "demo" },
  });
  readonly errors = signal<Record<string, string>>({});
  readonly secrets = signal<ServerConfigSecretsContext | null>({
    fields: { service_account_key: "set", managed_token: "set" },
    values: { service_account_key: "", managed_token: "" },
    managed: { managed_token: { field: "managed_token", overridden: false } },
    canRotate: true,
  });
  valueChanges: unknown[] = [];
  secretChanges: { name: string; value: string }[] = [];
  rotated: string[] = [];
}

async function mount(init?: (h: HostComponent) => void) {
  TestBed.configureTestingModule({ imports: [HostComponent] });
  const fixture = TestBed.createComponent(HostComponent);
  init?.(fixture.componentInstance);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const q = <T extends Element = HTMLElement>(sel: string) =>
    el.querySelector<T & Element>(sel) as T | null;
  return { fixture, el, q, host: fixture.componentInstance };
}

describe("CapabilityArgsFormComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("renders x-grain-enum-labels via PillSelect with zero native selects, .g-switch booleans, and write-only secret inputs", async () => {
    const { el, q, host, fixture } = await mount();

    expect(el.querySelectorAll("select").length).toBe(0);

    // Choice field shows x-grain-enum-labels text.
    const pillTrigger = q<HTMLButtonElement>('[data-testid="arg-auth_mode"]')!;
    expect(pillTrigger.textContent).toContain("Keyless token (recommended)");

    // Open pill select and choose service_account_key.
    pillTrigger.click();
    await fixture.whenStable();
    const opts = [...el.querySelectorAll<HTMLButtonElement>('[role="option"]')];
    expect(opts.map((o) => o.textContent?.trim())).toEqual([
      "Keyless token (recommended)",
      "Service-account key JSON",
    ]);
    opts[1]!.click();
    await fixture.whenStable();
    expect((host.value() as Record<string, unknown>)["auth_mode"]).toBe(
      "service_account_key",
    );

    // Boolean field uses .g-switch with role="switch".
    const sw = q<HTMLButtonElement>('button[role="switch"].g-switch')!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    sw.click();
    await fixture.whenStable();
    expect((host.value() as Record<string, unknown>)["impersonate"]).toBe(true);

    // Write-only secret uses type="password" and autocomplete="new-password".
    const secretWrap = q<HTMLElement>(
      '[data-secret-field="service_account_key"]',
    )!;
    const secretInput = secretWrap.querySelector<HTMLInputElement>("input")!;
    expect(secretInput.type).toBe("password");
    expect(secretInput.autocomplete).toBe("new-password");
    expect(
      q<HTMLElement>(
        '[data-testid="secret-state-service_account_key"]',
      )?.textContent?.trim(),
    ).toBe("set");

    secretInput.value = '{"type":"service_account"}';
    secretInput.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    expect(host.secretChanges).toEqual([
      { name: "service_account_key", value: '{"type":"service_account"}' },
    ]);

    // Managed secret renders Rotate button and Advanced override disclosure.
    const managedWrap = q<HTMLElement>('[data-managed-field="managed_token"]')!;
    const rotateBtn = [...managedWrap.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("Rotate"),
    )!;
    rotateBtn.click();
    await fixture.whenStable();
    expect(host.rotated).toEqual(["managed_token"]);

    // Inline error displays on the target field.
    host.errors.set({ auth_mode: "unsupported mode" });
    await fixture.whenStable();
    expect(el.textContent).toContain("unsupported mode");
  });

  it("edits arrays and string maps cleanly", async () => {
    const { el, host, fixture } = await mount();

    const buttons = [...el.querySelectorAll<HTMLButtonElement>("button")];
    const addScopeBtn = buttons.find(
      (b) => b.textContent?.trim() === "Add scopes",
    )!;
    const addEntryBtn = buttons.find(
      (b) => b.textContent?.trim() === "Add entry",
    )!;
    expect(addScopeBtn).toBeDefined();
    expect(addEntryBtn).toBeDefined();
    addScopeBtn.click();
    await fixture.whenStable();
    expect(
      ((host.value() as Record<string, unknown>)["scopes"] as string[]).length,
    ).toBe(2);

    addEntryBtn.click();
    await fixture.whenStable();
    const keyInputs = el.querySelectorAll<HTMLInputElement>(
      '[data-arg-map="labels"] [data-arg-map-name]',
    );
    expect(keyInputs.length).toBe(2);
  });

  it("falls back to JSON editor when schema uses an unsupported keyword and supports oneOf variants", async () => {
    const { el, host, fixture } = await mount((h) => {
      h.secrets.set(null);
      h.schema.set({
        oneOf: [
          {
            title: "Static host",
            type: "object",
            properties: { host: { type: "string" } },
          },
          {
            title: "By label",
            type: "object",
            properties: { label: { type: "string" } },
          },
        ],
      });
      h.value.set({ host: "lab-1" });
    });

    expect(
      el.querySelector('[data-testid="test-caf-top-variant"]')?.textContent,
    ).toContain("Static host");

    // Switch to unsupported keyword schema -> JSON fallback banner & textarea.
    host.schema.set({ type: "object", patternProperties: {} });
    host.value.set({ initial: true });
    await fixture.whenStable();
    expect(el.textContent).toContain("keyword the form cannot draw");
    const ta = el.querySelector<HTMLTextAreaElement>("#test-caf-json")!;
    expect(ta).not.toBeNull();
    ta.value = '{"updated": true}';
    ta.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    expect(host.value()).toEqual({ updated: true });
  });
});
