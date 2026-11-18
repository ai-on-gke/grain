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
import { provideRouter } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { CapabilityMethod } from "../api/models";
import { AccessService } from "../services/access.service";
import {
  GrantsPickerComponent,
  REPO_ACCESS_DOC,
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
  let sets: { name: string }[];

  beforeEach(() => {
    gets = [];
    posts = [];
    methods = [];
    sets = [];
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]),
        {
          provide: GrainApi,
          useValue: {
            get: (route: string, _p?: unknown, query?: string) => {
              gets.push({ route, query });
              if (route === "GET /api/capability-methods") {
                return Promise.resolve({ items: structuredClone(methods) });
              }
              return Promise.resolve({ sets: structuredClone(sets) });
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
  it("links a denied grant to #capability-policy and a CI-only one to the repository access docs, never blaming policy for it", async () => {
    methods = [
      {
        ref: "https://grain.dev/capabilities/gcp#gcp-key",
        capability: "https://grain.dev/capabilities/gcp",
        method: "gcp-key",
        policy: { task: "denied" },
      },
      {
        ref: "https://grain.dev/capabilities/repo#read",
        capability: "https://grain.dev/capabilities/repo",
        method: "read",
        policy: { task: "ci_only" },
      },
      {
        ref: "https://grain.dev/capabilities/docs#lookup",
        capability: "https://grain.dev/capabilities/docs",
        method: "lookup",
        policy: { task: "may_be_refused" },
      },
    ];
    const f = TestBed.createComponent(GrantsPickerComponent);
    f.componentRef.setInput("policyVerdicts", true);
    f.componentRef.setInput("held", [
      "https://grain.dev/capabilities/gcp#gcp-key={}",
      "https://grain.dev/capabilities/repo#read={}",
      "https://grain.dev/capabilities/docs#lookup={}",
    ]);
    await settle(f);
    const el = f.nativeElement as HTMLElement;
    const items = [
      ...el.querySelectorAll<HTMLLIElement>(
        '[data-testid="gp-policy-notes"] li',
      ),
    ];
    expect(items.map((li) => li.dataset["policyVerdict"])).toEqual([
      "denied",
      "ci_only",
      "may_be_refused",
    ]);
    expect(items[0].querySelector("a")?.getAttribute("href")).toBe(
      "/settings/capabilities#capability-policy",
    );
    expect(items[1].textContent).not.toContain("capability policy");
    expect(items[1].querySelector("a")?.getAttribute("href")).toBe(
      REPO_ACCESS_DOC,
    );
    expect(items[2].querySelector("a")?.getAttribute("href")).toBe(
      "/settings/capabilities#capability-policy",
    );
    expect(
      el.querySelector('[data-testid="held-grant-policy-2"]')?.classList,
    ).toContain("is-warn");
  });

  it("notes a refused grant before it is held, links it, and refuses it from every builder mode", async () => {
    methods = [
      {
        ref: "https://grain.dev/capabilities/api#tasks-admin",
        capability: "https://grain.dev/capabilities/api",
        method: "tasks-admin",
        policy: { task: "denied" },
      },
      {
        ref: "https://grain.dev/capabilities/api#tasks-read",
        capability: "https://grain.dev/capabilities/api",
        method: "tasks-read",
      },
      {
        ref: "https://grain.dev/capabilities/gcp#gcp-key",
        capability: "https://grain.dev/capabilities/gcp",
        method: "gcp-key",
        policy: { task: "denied" },
      },
    ];
    sets = [{ name: "tasks-admin" }, { name: "tasks-read" }];
    const f = TestBed.createComponent(GrantsPickerComponent);
    f.componentRef.setInput("policyVerdicts", true);
    const emitted: string[][] = [];
    f.componentInstance.heldChange.subscribe((v) => emitted.push(v));
    await settle(f);
    const el = f.nativeElement as HTMLElement;
    el.querySelector<HTMLElement>('[data-testid="gp-add-open"]')!.click();
    await settle(f);

    // API scope: the denied set is not the default, and its note links
    // to the policy without the held-only "Remove this grant".
    expect(
      el.querySelector('[data-testid="gp-api-set"]')?.textContent,
    ).toContain("api:tasks-read");
    const inst = f.componentInstance as unknown as {
      selectedApiSet: { set(v: string): void };
      rawGrant: { set(v: string): void };
      attachFromBuilder(): Promise<void>;
    };

    // Custom grant: typed by hand, a denied method is noted and refused.
    el.querySelector<HTMLElement>('[data-testid="gp-mode-raw"]')!.click();
    inst.rawGrant.set("https://grain.dev/capabilities/gcp#gcp-key={}");
    await settle(f);
    const note = el.querySelector<HTMLLIElement>(
      '[data-testid="gp-policy-notes"] li[data-policy-grant^="https://grain.dev/capabilities/gcp#gcp-key"]',
    );
    expect(note?.dataset["policyVerdict"]).toBe("denied");
    expect(note?.querySelector("a")?.getAttribute("href")).toBe(
      "/settings/capabilities#capability-policy",
    );
    expect(note?.textContent).toContain("cannot be granted here");
    expect(note?.textContent).not.toContain("Remove this grant");
    await inst.attachFromBuilder();
    await settle(f);
    expect(el.querySelector('[data-testid="gp-error"]')?.textContent).toContain(
      "Denied by the capability policy",
    );

    // And an API scope set the policy denies, picked anyway.
    el.querySelector<HTMLElement>('[data-testid="gp-mode-api"]')!.click();
    await settle(f);
    inst.selectedApiSet.set("tasks-admin");
    await inst.attachFromBuilder();
    await settle(f);
    expect(posts).toEqual([]);
    expect(emitted).toEqual([]);
  });
});
