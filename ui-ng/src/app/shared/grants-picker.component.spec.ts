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

import { Component } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { CapabilityMethod } from "../api/models";
import { AccessService } from "../services/access.service";
import { GrantsPickerComponent } from "./grants-picker.component";

@Component({
  imports: [GrantsPickerComponent],
  template: `<g-grants-picker testid="gp" repo="acme/app" />`,
})
class HostComponent {}

async function mount(methods: CapabilityMethod[]) {
  const posts: string[] = [];
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([]),
      {
        provide: GrainApi,
        useValue: {
          get: (key: string) =>
            Promise.resolve(
              key === "GET /api/capability-methods"
                ? { items: methods }
                : { sets: [] },
            ),
          post: (key: string) => {
            posts.push(key);
            return Promise.resolve({ checks: [] });
          },
        },
      },
      {
        provide: AccessService,
        useValue: { can: () => true, deniedReason: () => "" },
      },
    ],
  });
  const fixture = TestBed.createComponent(HostComponent);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const click = async (testid: string) => {
    el.querySelector<HTMLButtonElement>(`[data-testid="${testid}"]`)!.click();
    await new Promise((r) => setTimeout(r, 5));
    await fixture.whenStable();
  };
  await click("gp-add-open");
  await click("gp-mode-method");
  return { el, click, posts };
}

describe("GrantsPickerComponent capability policy verdicts", () => {
  it("disables a method refused for this repository even when its kind-wide verdict is may_be_refused", async () => {
    const { el, click } = await mount([
      {
        ref: "github#write-issues",
        capability: "github",
        method: "write-issues",
        policy: { task: "may_be_refused" },
        policyRefusal: "capability policy refuses github#write-issues here",
      },
      {
        ref: "github#read-issues",
        capability: "github",
        method: "read-issues",
        policy: { task: "may_be_refused" },
      },
      {
        ref: "repo#read",
        capability: "repo",
        method: "read",
        policy: { task: "ci_only" },
        policyRefusal: "repo#read is granted only to CI runs",
      },
    ]);
    await click("gp-method-ref");
    const refused = el.querySelector(
      '[data-testid="gp-method-ref-opt-github#write-issues"]',
    )!;
    expect(refused.getAttribute("aria-disabled")).toBe("true");
    expect(refused.textContent).toContain("capability policy refuses");
    const maybe = el.querySelector(
      '[data-testid="gp-method-ref-opt-github#read-issues"]',
    )!;
    expect(maybe.getAttribute("aria-disabled")).toBeNull();
    const ciOnly = el.querySelector(
      '[data-testid="gp-method-ref-opt-repo#read"]',
    )!;
    expect(ciOnly.getAttribute("aria-disabled")).toBe("true");
    expect(ciOnly.textContent).not.toContain("policy refuses");
    expect(
      el.querySelector('a[href="/settings/capabilities#capability-policy"]'),
    ).not.toBeNull();
  });

  it("refuses to attach rather than fall back to a disabled method when every method is blocked", async () => {
    const { el, click, posts } = await mount([
      {
        ref: "github#write-issues",
        capability: "github",
        method: "write-issues",
        policy: { task: "may_be_refused" },
        policyRefusal: "capability policy refuses github#write-issues here",
      },
      {
        ref: "gcp#gcp-key",
        capability: "gcp",
        method: "gcp-key",
        policy: { task: "denied" },
        policyRefusal: "capability policy denies gcp#gcp-key",
      },
    ]);
    await click("gp-attach");
    expect(posts).toEqual([]);
    expect(el.textContent).toContain("Choose a capability method.");
  });
});
