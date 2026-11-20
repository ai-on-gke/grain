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

import { HttpErrorResponse } from "@angular/common/http";
import { TestBed } from "@angular/core/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import {
  RepoUnfixableChecksComponent,
  sameCheckName,
} from "./repo-unfixable-checks.component";

class FakeUnfixableApi {
  checks: string[] = [];
  grainCIName = "grain CI";
  putCalls: { params?: RouteParams; body?: unknown }[] = [];
  getCalls = 0;

  get = (key: string) => {
    if (key === "GET /api/repos/{owner}/{name}/unfixable-checks") {
      this.getCalls++;
      return Promise.resolve({
        unfixableChecks: [...this.checks],
        grainCICheckName: this.grainCIName,
      });
    }
    return Promise.resolve({});
  };

  put = (key: string, params?: RouteParams, body?: unknown) => {
    if (key === "PUT /api/repos/{owner}/{name}/unfixable-checks") {
      this.putCalls.push({ params, body });
      const list = ((body as { unfixableChecks?: string[] })?.unfixableChecks ??
        []) as string[];
      if (list.some((c) => c.includes(","))) {
        return new Promise((_, reject) =>
          setTimeout(() =>
            reject(
              new HttpErrorResponse({
                status: 400,
                error: {
                  error:
                    'unfixableChecks: "a,b" has a comma in it -- name one check per entry',
                },
              }),
            ),
          ),
        );
      }
      this.checks = [...list];
      return Promise.resolve({
        unfixableChecks: [...this.checks],
        grainCICheckName: this.grainCIName,
      });
    }
    return Promise.resolve({});
  };
}

function setup(
  opts: {
    api?: FakeUnfixableApi;
    configRepoOnly?: boolean;
    access?: GrainConfig["access"];
  } = {},
) {
  const api = opts.api ?? new FakeUnfixableApi();
  TestBed.configureTestingModule({
    providers: [{ provide: GrainApi, useValue: api }, AccessService],
  });
  const access = TestBed.inject(AccessService);
  access.setConfig({
    version: "v3.6.0",
    access: opts.access ?? {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  } as GrainConfig);
  const fixture = TestBed.createComponent(RepoUnfixableChecksComponent);
  fixture.componentRef.setInput("repo", "acme/widgets");
  fixture.componentRef.setInput("configRepoOnly", opts.configRepoOnly ?? false);
  fixture.detectChanges();
  return { fixture, el: fixture.nativeElement as HTMLElement, api };
}

describe("RepoUnfixableChecksComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("compares check names case-insensitively and trimmed", () => {
    expect(sameCheckName(" cla/google ", "CLA/Google")).toBe(true);
    expect(sameCheckName("cla/google", "license/check")).toBe(false);
  });

  it("loads, adds, refuses grain CI / duplicates / server errors, and removes checks", async () => {
    const { fixture, el, api } = setup();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(
      el.querySelector('[data-testid="unfixable-checks-empty"]')?.textContent,
    ).toContain("grain treats every check acme/widgets reports");

    const input = el.querySelector<HTMLInputElement>(
      '[data-testid="input-unfixable-check"]',
    )!;
    const form = el.querySelector("form")!;

    // Refuses grain CI locally without calling PUT.
    input.value = "GRAIN ci";
    input.dispatchEvent(new Event("input"));
    fixture.detectChanges();
    form.dispatchEvent(new Event("submit"));
    fixture.detectChanges();
    expect(api.putCalls).toHaveLength(0);
    expect(
      el.querySelector('[data-testid="unfixable-checks-error"]')?.textContent,
    ).toContain("grain CI judges one exact commit");

    // Editing clears the refusal; adding a valid check calls PUT.
    input.value = "cla/google";
    input.dispatchEvent(new Event("input"));
    fixture.detectChanges();
    expect(
      el.querySelector('[data-testid="unfixable-checks-error"]'),
    ).toBeNull();

    form.dispatchEvent(new Event("submit"));
    await fixture.whenStable();
    await Promise.resolve();
    fixture.detectChanges();
    expect(api.putCalls).toHaveLength(1);
    expect(api.checks).toEqual(["cla/google"]);
    expect(input.value).toBe("");
    expect(
      el.querySelectorAll('[data-testid="unfixable-check-chip"]').length,
    ).toBe(1);

    // Refuses case-insensitive duplicate locally.
    input.value = "CLA/Google";
    input.dispatchEvent(new Event("input"));
    fixture.detectChanges();
    form.dispatchEvent(new Event("submit"));
    fixture.detectChanges();
    expect(api.putCalls).toHaveLength(1);
    expect(
      el.querySelector('[data-testid="unfixable-checks-error"]')?.textContent,
    ).toContain("already declares CLA/Google unfixable");

    // Server refusal (comma in check name) shows beside the input.
    input.value = "a,b";
    input.dispatchEvent(new Event("input"));
    fixture.detectChanges();
    form.dispatchEvent(new Event("submit"));
    await new Promise((r) => setTimeout(r, 15));
    await fixture.whenStable();
    fixture.detectChanges();
    expect(
      el.querySelector('[data-testid="unfixable-checks-error"]')?.textContent,
    ).toContain("has a comma in it");

    // Removing the chip calls PUT with empty list.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-unfixable-check-cla/google"]',
    )!.click();
    await fixture.whenStable();
    fixture.detectChanges();
    expect(api.checks).toEqual([]);
    expect(
      el.querySelector('[data-testid="unfixable-checks-empty"]'),
    ).not.toBeNull();
  });

  it("is read-only in config-repo-managed mode and for viewer roles", async () => {
    const api = new FakeUnfixableApi();
    api.checks = ["cla/google"];

    const cfgManaged = setup({ api, configRepoOnly: true });
    await cfgManaged.fixture.whenStable();
    cfgManaged.fixture.detectChanges();
    expect(
      cfgManaged.el.querySelector(
        '[data-testid="unfixable-checks-config-managed"]',
      ),
    ).not.toBeNull();
    expect(
      cfgManaged.el.querySelector<HTMLInputElement>(
        '[data-testid="input-unfixable-check"]',
      )?.disabled,
    ).toBe(true);
    expect(
      cfgManaged.el.querySelector(
        '[data-testid="remove-unfixable-check-cla/google"]',
      ),
    ).toBeNull();
    TestBed.resetTestingModule();

    const viewer = setup({
      api,
      access: {
        role: "viewer",
        roles: ["viewer"],
        full: false,
        scopes: ["repos:read"],
      },
    });
    await viewer.fixture.whenStable();
    viewer.fixture.detectChanges();
    expect(
      viewer.el.querySelector('[data-testid="unfixable-checks-denied"]'),
    ).not.toBeNull();
    expect(
      viewer.el.querySelector<HTMLInputElement>(
        '[data-testid="input-unfixable-check"]',
      )?.disabled,
    ).toBe(true);
    expect(
      viewer.el.querySelector(
        '[data-testid="remove-unfixable-check-cla/google"]',
      ),
    ).toBeNull();
  });

  it("does not fire GET when the role lacks repos:read", async () => {
    const api = new FakeUnfixableApi();
    const { fixture, el } = setup({
      api,
      access: {
        role: "tasks-only",
        roles: ["tasks-only"],
        full: false,
        scopes: ["tasks:read"],
      },
    });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(api.getCalls).toBe(0);
    expect(
      el.querySelector('[data-testid="unfixable-checks-load-error"]'),
    ).not.toBeNull();
  });
});
