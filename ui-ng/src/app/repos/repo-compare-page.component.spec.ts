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

import { APP_BASE_HREF } from "@angular/common";
import { HttpErrorResponse } from "@angular/common/http";
import { TestBed } from "@angular/core/testing";
import {
  Router,
  provideRouter,
  withComponentInputBinding,
} from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { CompareResponse, GrainConfig } from "../api/models";
import { repoCompareMatcher } from "../app.routes";
import { RepoComparePageComponent } from "./repo-compare-page.component";

const SAMPLE_DIFF = [
  "diff --git a/WALK7546.md b/WALK7546.md",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/WALK7546.md",
  "@@ -0,0 +1 @@",
  "+hello compare",
].join("\n");

class FakeCompareApi {
  calls: { key: string; params?: RouteParams; query?: string }[] = [];
  configBody: GrainConfig = {
    version: "v3.6.0",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  } as GrainConfig;

  compareHandler: (
    params: RouteParams | undefined,
    query: string | undefined,
  ) => Promise<CompareResponse> = async (_params, query) => {
    const q = new URLSearchParams(query ?? "");
    const base = q.get("base") || "main";
    const head = q.get("head") || "";
    if (head.includes("missing")) {
      throw new HttpErrorResponse({
        status: 404,
        error: {
          error: `GitHub has nothing to compare for ${base}...${head} in acme/widgets: one of the two branches does not exist`,
        },
      });
    }
    return {
      base,
      head,
      mergeBase: "1234567890abcdef",
      merged: head === "release/1.0",
      diff: head === "empty/branch" ? "" : SAMPLE_DIFF,
    };
  };

  tasks: unknown[] = [
    {
      id: "42",
      title: "Add walk doc",
      repo: "acme/widgets",
      branch: "feature/walk-7546",
      mergeGates: [
        { id: "ci", label: "CI", state: "passed", detail: "all green" },
      ],
    },
  ];

  config = () => Promise.resolve(structuredClone(this.configBody));

  get = (key: string, params?: RouteParams, query?: string) => {
    this.calls.push({ key, params, query });
    if (key === "GET /api/repos/{owner}/{name}/compare") {
      return this.compareHandler(params, query);
    }
    if (key === "GET /api/repos/{owner}/{name}/github-branches") {
      return Promise.resolve([
        "main",
        "release/1.0",
        "feature/walk-7546",
        "fix/#7546-hash",
      ]);
    }
    if (key === "GET /api/repos/{owner}/{name}/branches") {
      return Promise.resolve([{ name: "feature/walk-7546" }]);
    }
    if (key === "GET /api/tasks") {
      return Promise.resolve(this.tasks);
    }
    return Promise.resolve({});
  };
}

async function setup(url: string, api = new FakeCompareApi()) {
  TestBed.configureTestingModule({
    providers: [
      provideRouter(
        [
          {
            matcher: repoCompareMatcher,
            component: RepoComparePageComponent,
          },
          { path: "**", component: RepoComparePageComponent },
        ],
        withComponentInputBinding(),
      ),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
    ],
  });
  const harness = await RouterTestingHarness.create();
  await harness.navigateByUrl(url, RepoComparePageComponent);
  await harness.fixture.whenStable();
  harness.detectChanges();
  await Promise.resolve();
  harness.detectChanges();
  return {
    harness,
    el: harness.routeNativeElement as HTMLElement,
    api,
    router: TestBed.inject(Router),
  };
}

describe("RepoComparePageComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("renders diffs for branch names with slashes and %23, plus matched task merge gates", async () => {
    const { el, api, router, harness } = await setup(
      "/repos/acme/widgets/compare/main...feature/walk-7546",
    );

    expect(
      el.querySelector('[data-testid="compare-heading"]')?.textContent?.trim(),
    ).toBe("feature/walk-7546");
    expect(
      el.querySelector('[data-testid="compare-status-pill"]')?.textContent,
    ).toContain("Open");
    expect(
      el.querySelector('[data-testid="compare-subtitle"]')?.textContent,
    ).toContain("Into main on acme/widgets");
    expect(
      el.querySelector('[data-testid="compare-subtitle"]')?.textContent,
    ).toContain("forked at 12345678");
    expect(
      el.querySelector('[data-testid="compare-diff-viewer"]')?.textContent,
    ).toContain("WALK7546.md");
    expect(el.querySelector('[data-testid="pr-merge-gates"]')).not.toBeNull();

    const compareCall = api.calls.find(
      (c) => c.key === "GET /api/repos/{owner}/{name}/compare",
    );
    expect(compareCall?.query).toBe("base=main&head=feature%2Fwalk-7546");

    // Navigate using the branch pickers to a branch containing '#'.
    const headInput = el.querySelector<HTMLInputElement>(
      '[data-testid="compare-head-input"]',
    )!;
    headInput.value = "fix/#7546-hash";
    headInput.dispatchEvent(new Event("input"));
    harness.detectChanges();

    const form = el.querySelector<HTMLFormElement>(
      '[data-testid="compare-picker-form"]',
    )!;
    form.dispatchEvent(new Event("submit"));
    await harness.fixture.whenStable();
    harness.detectChanges();
    await Promise.resolve();
    harness.detectChanges();

    expect(router.url).toBe(
      "/repos/acme/widgets/compare/main...fix/%237546-hash",
    );
    expect(
      el.querySelector('[data-testid="compare-heading"]')?.textContent?.trim(),
    ).toBe("fix/#7546-hash");
    const hashCall = api.calls
      .filter((c) => c.key === "GET /api/repos/{owner}/{name}/compare")
      .pop();
    expect(hashCall?.query).toBe("base=main&head=fix%2F%237546-hash");
  });

  it("shows an in-place 'nothing to compare' warning on 404 for unknown refs", async () => {
    const { el } = await setup(
      "/repos/acme/widgets/compare/main...nope/missing",
    );

    const banner = el.querySelector('[data-testid="compare-error"]');
    expect(banner).not.toBeNull();
    expect(banner?.getAttribute("role")).toBe("alert");
    expect(banner?.textContent).toContain(
      "GitHub has nothing to compare for main...nope/missing",
    );
    expect(el.querySelector('[data-testid="compare-diff-viewer"]')).toBeNull();
  });

  it("handles omitted base, empty head, and no-change comparisons", async () => {
    const { el, router, harness } = await setup(
      "/repos/acme/widgets/compare/release/1.0",
    );
    expect(
      el.querySelector('[data-testid="compare-status-pill"]')?.textContent,
    ).toContain("Merged");
    expect(
      el.querySelector('[data-testid="compare-subtitle"]')?.textContent,
    ).toContain("Merged into main on acme/widgets");

    await router.navigateByUrl(
      "/repos/acme/widgets/compare/main...empty/branch",
    );
    await harness.fixture.whenStable();
    harness.detectChanges();
    await Promise.resolve();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="compare-no-changes"]')?.textContent,
    ).toContain("There are no changes between these branches.");

    await router.navigateByUrl("/repos/acme/widgets/compare/main...");
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="compare-empty-ref"]')?.textContent,
    ).toContain("This link names no branch to compare.");
  });

  it("denies access without firing /compare when the role lacks repos:read", async () => {
    const api = new FakeCompareApi();
    api.configBody = {
      version: "v3.6.0",
      access: {
        role: "tasks-only",
        roles: ["tasks-only"],
        full: false,
        scopes: ["tasks:read"],
      },
    } as GrainConfig;

    const { el } = await setup(
      "/repos/acme/widgets/compare/main...feature/walk-7546",
      api,
    );
    expect(el.querySelector('[data-testid="compare-denied"]')).not.toBeNull();
    expect(
      api.calls.some((c) => c.key === "GET /api/repos/{owner}/{name}/compare"),
    ).toBe(false);
    expect(
      el.querySelector<HTMLButtonElement>('[data-testid="compare-submit-btn"]')
        ?.disabled,
    ).toBe(true);
  });
});
