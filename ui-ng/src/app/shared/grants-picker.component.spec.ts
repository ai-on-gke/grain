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

import { TestBed } from "@angular/core/testing";
import { GrainApi } from "../api/grain-api.service";
import type { CapabilityMethod } from "../api/models";
import { AccessService } from "../services/access.service";
import {
  GrantsPickerComponent,
  grantRef,
  policyVerdictFor,
} from "./grants-picker.component";

describe("grantRef", () => {
  it("strips arguments and reads an api grant as its capability ref", () => {
    expect(grantRef('github#read-issues={"repo":"a/b"}')).toBe(
      "github#read-issues",
    );
    expect(grantRef("api:tasks-read")).toBe(
      "https://grain.dev/capabilities/api#tasks-read",
    );
    expect(grantRef("api:ci-read@acme/widgets")).toBe(
      "https://grain.dev/capabilities/api#ci-read",
    );
  });
});

describe("policyVerdictFor", () => {
  const m = (over: Partial<CapabilityMethod>): CapabilityMethod => ({
    ref: "x#y",
    capability: "x",
    method: "y",
    ...over,
  });

  it("says may be refused for a repo-scoped rule while no repo is known", () => {
    const v = policyVerdictFor(
      m({ policy: { task: "may_be_refused" } }),
      "task",
      false,
    );
    expect(v?.badge).toBe("may be refused");
    // Once the run is known the listing has judged it: admitted is silent.
    expect(
      policyVerdictFor(m({ policy: { task: "may_be_refused" } }), "task", true),
    ).toBeNull();
  });

  it("prefers the run's own refusal, and names CI-only methods", () => {
    expect(
      policyVerdictFor(m({ policyRefusal: "denied here" }), "task", true)
        ?.badge,
    ).toBe("denied by policy");
    expect(
      policyVerdictFor(m({ policy: { task: "ci_only" } }), "task", true)?.badge,
    ).toBe("CI only");
  });
});

describe("GrantsPickerComponent with policyVerdicts", () => {
  let gets: { route: string; query?: string }[];
  let posts: unknown[];
  let methods: CapabilityMethod[];

  beforeEach(() => {
    gets = [];
    posts = [];
    methods = [];
    TestBed.configureTestingModule({
      providers: [
        {
          provide: GrainApi,
          useValue: {
            get: (route: string, _p?: unknown, query?: string) => {
              gets.push({ route, query });
              if (route === "GET /api/capability-methods") {
                return Promise.resolve({ items: structuredClone(methods) });
              }
              return Promise.resolve({ sets: [] });
            },
            post: (_route: string, _p: unknown, body: unknown) => {
              posts.push(body);
              return Promise.resolve([{ recorded: "x" }]);
            },
          },
        },
      ],
    });
    TestBed.inject(AccessService).setConfig({
      access: { role: "grain/operator", roles: [], full: true, scopes: [] },
    });
  });

  afterEach(() => TestBed.resetTestingModule());

  async function settle(f: {
    detectChanges(): void;
    whenStable(): Promise<unknown>;
  }) {
    f.detectChanges();
    await new Promise((r) => setTimeout(r, 300));
    await f.whenStable();
    f.detectChanges();
  }

  it("asks nothing of a run with no whole repository, and the run once it has one", async () => {
    const f = TestBed.createComponent(GrantsPickerComponent);
    f.componentRef.setInput("policyVerdicts", true);
    f.componentRef.setInput("repo", "acme");
    await settle(f);
    const first = gets.filter((g) => g.route === "GET /api/capability-methods");
    expect(first).toHaveLength(1);
    expect(first[0].query).toBeUndefined();

    f.componentRef.setInput("repo", "acme/widgets");
    await settle(f);
    const all = gets.filter((g) => g.route === "GET /api/capability-methods");
    expect(all.at(-1)?.query).toBe("kind=task&repo=acme%2Fwidgets");
  });

  it("badges a held api grant the policy denies, matched through its alias", async () => {
    methods = [
      {
        ref: "https://grain.dev/capabilities/api#grain/task-admin",
        capability: "https://grain.dev/capabilities/api",
        method: "grain/task-admin",
        aliasOf: ["https://grain.dev/capabilities/api#task-admin"],
        policyRefusal:
          "the capability policy denies task-admin to acme/widgets",
      },
    ];
    const f = TestBed.createComponent(GrantsPickerComponent);
    f.componentRef.setInput("policyVerdicts", true);
    f.componentRef.setInput("repo", "acme/widgets");
    f.componentRef.setInput("held", ["api:task-admin@acme/widgets"]);
    await settle(f);
    const el = f.nativeElement as HTMLElement;
    expect(
      el.querySelector('[data-testid="held-grant-policy-0"]')?.textContent,
    ).toContain("denied by policy");
    expect(el.textContent).toContain("denies task-admin to acme/widgets");
  });

  it("never selects or attaches a method the policy refuses", async () => {
    methods = [
      {
        ref: "repo#read",
        capability: "repo",
        method: "read",
        policy: { task: "ci_only" },
      },
      { ref: "notes#write", capability: "notes", method: "write" },
    ];
    const f = TestBed.createComponent(GrantsPickerComponent);
    f.componentRef.setInput("policyVerdicts", true);
    f.componentRef.setInput("repo", "acme/widgets");
    const emitted: string[][] = [];
    f.componentInstance.heldChange.subscribe((v) => emitted.push(v));
    await settle(f);
    const el = f.nativeElement as HTMLElement;
    el.querySelector<HTMLElement>('[data-testid="gp-add-open"]')!.click();
    await settle(f);
    el.querySelector<HTMLElement>('[data-testid="gp-mode-method"]')!.click();
    await settle(f);
    // The default is the first method that may be granted, not the CI-only one.
    expect(
      el.querySelector('[data-testid="gp-method-ref"]')?.textContent,
    ).toContain("notes#write");

    // Even when chosen directly, a refused method is reported, not attached.
    (
      f.componentInstance as unknown as {
        selectedMethod: { set(v: string): void };
      }
    ).selectedMethod.set("repo#read");
    await settle(f);
    // The selection is moved off the refused method at once...
    expect(
      el.querySelector('[data-testid="gp-method-ref"]')?.textContent,
    ).toContain("notes#write");
    el.querySelector<HTMLElement>('[data-testid="gp-attach"]')!.click();
    await settle(f);
    expect(emitted.at(-1)).toEqual(["x"]);
    expect(JSON.stringify(posts.at(-1))).toContain("notes#write");
    expect(JSON.stringify(posts)).not.toContain("repo#read");
  });
});
