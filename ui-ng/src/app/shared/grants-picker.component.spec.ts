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
import { provideRouter } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { CapabilityMethod } from "../api/models";
import { AccessService } from "../services/access.service";
import { methodVerdict } from "./capability-verdict";
import { GrantsPickerComponent } from "./grants-picker.component";

// The capability policy's verdicts on picker rows (2026-11-13
// connections walkthrough: a method a Deny rule refused showed nothing).

const KEY = "https://capabilities.example/cloud#cloud-key";
const VIEWER = "https://capabilities.example/cloud#cloud-viewer";
const OPEN = "https://capabilities.example/cloud#open";

const METHODS: CapabilityMethod[] = [
  {
    ref: KEY,
    capability: "https://capabilities.example/cloud",
    method: "cloud-key",
    policy: { task: "denied" },
    policyRefusal: "the capability policy denies cloud-key to this task",
  },
  {
    ref: VIEWER,
    capability: "https://capabilities.example/cloud",
    method: "cloud-viewer",
    policy: { task: "may_be_refused" },
  },
  {
    ref: OPEN,
    capability: "https://capabilities.example/cloud",
    method: "open",
  },
];

class FakeApi {
  queries: (string | undefined)[] = [];
  get(route: string, _params?: unknown, query?: string): Promise<unknown> {
    if (route === "GET /api/capability-methods") {
      this.queries.push(query);
      return Promise.resolve({ items: structuredClone(METHODS) });
    }
    return Promise.resolve({ sets: [] });
  }
  post(): Promise<unknown> {
    return Promise.resolve([]);
  }
}

@Component({
  imports: [GrantsPickerComponent],
  template: `
    <g-grants-picker
      testid="gp"
      [held]="held()"
      [repo]="repo()"
      [judgeRun]="judge()"
      (heldChange)="held.set($event)"
    />
  `,
})
class HostComponent {
  readonly held = signal<string[]>([]);
  readonly repo = signal<string | undefined>("acme/web");
  readonly judge = signal(true);
}

async function mount(init: (h: HostComponent) => void) {
  const api = new FakeApi();
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([]),
      { provide: GrainApi, useValue: api },
      { provide: AccessService, useValue: { can: () => true } },
    ],
  });
  const fixture = TestBed.createComponent(HostComponent);
  init(fixture.componentInstance);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  await new Promise((r) => setTimeout(r, 0));
  await fixture.whenStable();
  return { fixture, api, el: fixture.nativeElement as HTMLElement };
}

describe("methodVerdict", () => {
  it("takes the run's own refusal when the listing judged one run", () => {
    expect(methodVerdict(METHODS[0], "task", true)).toEqual({
      verdict: "denied",
      badge: "denied by policy",
      text: "the capability policy denies cloud-key to this task",
    });
    // Admitted for this run: the kind's "may be refused" would mislead.
    expect(methodVerdict(METHODS[1], "task", true)).toBeNull();
  });

  it("shows the run kind's verdict when no run is judged", () => {
    expect(methodVerdict(METHODS[1], "task", false)?.text).toBe(
      "may be refused by the capability policy for agent tasks",
    );
    expect(methodVerdict(METHODS[0], "task", false)?.text).toBe(
      "denied by the capability policy for agent tasks",
    );
    expect(methodVerdict(METHODS[2], "task", false)).toBeNull();
  });

  it("says CI only whatever the listing", () => {
    const m = { ...METHODS[2]!, policy: { task: "ci_only" as const } };
    expect(methodVerdict(m, "task", true)?.verdict).toBe("ci_only");
  });
});

describe("GrantsPickerComponent policy verdicts", () => {
  it("marks a held denied grant with a link to the Capability policy, judged for its repo", async () => {
    const { el, api } = await mount((h) => h.held.set([`${KEY}={}`, OPEN]));
    expect(api.queries).toEqual(["kind=task&repo=acme%2Fweb"]);
    const verdicts = [
      ...el.querySelectorAll<HTMLAnchorElement>("a.grant-verdict"),
    ];
    expect(verdicts).toHaveLength(1);
    const v = verdicts[0]!;
    expect(v.textContent?.trim()).toBe("denied by policy");
    expect(v.dataset["policyVerdict"]).toBe("denied");
    expect(v.getAttribute("href")).toBe(
      "/settings/capabilities#capability-policy",
    );
    expect(v.getAttribute("aria-label")).toContain(
      "the capability policy denies cloud-key to this task",
    );
  });

  it("shows the kind's may-be-refused verdict where no run is judged", async () => {
    const { el, api } = await mount((h) => {
      h.held.set([VIEWER]);
      h.judge.set(false);
    });
    expect(api.queries).toEqual([undefined]);
    const v = el.querySelector<HTMLAnchorElement>("a.grant-verdict")!;
    expect(v.textContent?.trim()).toBe("may be refused");
    expect(v.classList).toContain("is-warning");
  });

  it("keeps a retired id as a row that says so and can be removed", async () => {
    const { el, fixture } = await mount((h) => h.held.set(["self-debug"]));
    expect(
      el.querySelector('[data-testid="retired-grant-0"]')?.textContent,
    ).toContain("No longer offered");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-grant-0"]',
    )!.click();
    await fixture.whenStable();
    expect(fixture.componentInstance.held()).toEqual([]);
  });

  it("refetches verdicts when the repository changes", async () => {
    const { fixture, api } = await mount((h) => h.held.set([KEY]));
    fixture.componentInstance.repo.set("acme/api");
    await fixture.whenStable();
    await new Promise((r) => setTimeout(r, 0));
    expect(api.queries).toEqual([
      "kind=task&repo=acme%2Fweb",
      "kind=task&repo=acme%2Fapi",
    ]);
  });

  it("lists a denied method disabled in the builder and refuses to attach it", async () => {
    const { el, fixture } = await mount(() => undefined);
    el.querySelector<HTMLButtonElement>('[data-testid="gp-add-open"]')!.click();
    await fixture.whenStable();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="gp-mode-method"]',
    )!.click();
    await fixture.whenStable();
    // The first method is denied, so the builder preselects one it may add.
    const note = el.querySelector('[data-testid="gp-method-verdict"]');
    expect(note).toBeNull();
    el.querySelector<HTMLButtonElement>('[data-testid="gp-attach"]')!.click();
    await fixture.whenStable();
    await new Promise((r) => setTimeout(r, 0));
    expect(fixture.componentInstance.held()).toEqual([`${VIEWER}={}`]);
  });
});
