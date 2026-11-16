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
  DefaultUrlSerializer,
  Router,
  provideRouter,
  withComponentInputBinding,
} from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { CompareResponse, GrainConfig, Task } from "../api/models";
import { routes } from "../app.routes";
import {
  compareRefSegment,
  parseComparisonRefs,
  repoCompareMatcher,
  repoComparePath,
} from "./repo-compare";

function patch(file: string, before: string, after: string): string {
  return [
    `diff --git a/${file} b/${file}`,
    "index 1111111..2222222 100644",
    `--- a/${file}`,
    `+++ b/${file}`,
    "@@ -1,1 +1,1 @@",
    `-${before}`,
    `+${after}`,
    "",
  ].join("\n");
}

interface CompareCall {
  key: string;
  params?: RouteParams;
  query?: string;
}

class FakeCompareApi {
  calls: CompareCall[] = [];
  response: CompareResponse = {
    base: "main",
    head: "feature/walk-5632",
    mergeBase: "a1b2c3d4e5f60718",
    merged: false,
    diff: patch("WALK5632.md", "before line", "after line"),
  };
  failWith: Error | HttpErrorResponse | null = null;
  tasks: Task[] = [];

  configBody: GrainConfig = {
    version: "v3.6.0",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  config = () => Promise.resolve(structuredClone(this.configBody));

  get = (key: string, params?: RouteParams, query?: string) => {
    this.calls.push({ key, params, query });
    if (key === "GET /api/tasks") {
      return Promise.resolve(this.tasks);
    }
    if (key === "GET /api/repos/{owner}/{name}/compare") {
      if (this.failWith) {
        const err = this.failWith;
        return new Promise((_, reject) => setTimeout(() => reject(err)));
      }
      return Promise.resolve(structuredClone(this.response));
    }
    return Promise.resolve({});
  };
}

async function openCompare(
  url: string,
  api = new FakeCompareApi(),
): Promise<{
  harness: RouterTestingHarness;
  router: Router;
  api: FakeCompareApi;
}> {
  TestBed.configureTestingModule({
    providers: [
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
      provideRouter(routes, withComponentInputBinding()),
    ],
  });
  const router = TestBed.inject(Router);
  const harness = await RouterTestingHarness.create(url);
  harness.fixture.autoDetectChanges(true);
  await new Promise((r) => setTimeout(r, 20));
  await harness.fixture.whenStable();
  return { harness, router, api };
}

describe("repo-compare URL helpers and matcher", () => {
  const serializer = new DefaultUrlSerializer();

  it("keeps slashes literal and escapes #, ? and % so reload compares the same branch", () => {
    expect(compareRefSegment("feature/walk-5632")).toBe("feature/walk-5632");
    expect(compareRefSegment("fix/#5632-hash")).toBe("fix/%235632-hash");

    const p = repoComparePath("acme/widgets", "release/1.0", "fix/#123-what?");
    expect(p).toBe(
      "/repos/acme/widgets/compare/release/1.0...fix/%23123-what%3F",
    );

    const tree = serializer.parse(p);
    expect(tree.fragment).toBeNull();
    expect(serializer.serialize(tree)).toBe(p);

    const segs = tree.root.children["primary"]!.segments;
    const matched = repoCompareMatcher(segs);
    expect(matched).not.toBeNull();
    expect(matched!.posParams?.["owner"].path).toBe("acme");
    expect(matched!.posParams?.["name"].path).toBe("widgets");
    expect(
      parseComparisonRefs(matched!.posParams!["refs"].path, tree.fragment),
    ).toEqual({
      base: "release/1.0",
      head: "fix/#123-what?",
      recoveredFragment: false,
    });
  });

  it("matches hosted repositories and omitted bases", () => {
    const hostedPath = repoComparePath(
      "git.example.com:8443/team/widgets",
      "main",
      "fix/#99",
    );
    expect(hostedPath).toBe(
      "/repos/git.example.com:8443/team/widgets/compare/main...fix/%2399",
    );
    const tree = serializer.parse(hostedPath);
    const matched = repoCompareMatcher(tree.root.children["primary"]!.segments);
    expect(matched?.posParams?.["host"]?.path).toBe("git.example.com:8443");
    expect(matched?.posParams?.["owner"]?.path).toBe("team");
    expect(matched?.posParams?.["name"]?.path).toBe("widgets");
    expect(parseComparisonRefs(matched!.posParams!["refs"].path)).toEqual({
      base: "main",
      head: "fix/#99",
      recoveredFragment: false,
    });

    const headOnly = serializer.parse(
      repoComparePath("acme/widgets", "", "grain/task-12"),
    );
    const headMatched = repoCompareMatcher(
      headOnly.root.children["primary"]!.segments,
    );
    expect(parseComparisonRefs(headMatched!.posParams!["refs"].path)).toEqual({
      base: "",
      head: "grain/task-12",
      recoveredFragment: false,
    });
  });

  it("recovers an unescaped # fragment typed directly into a compare URL", () => {
    const tree = serializer.parse(
      "/repos/acme/widgets/compare/main...fix/#5632-hash",
    );
    const matched = repoCompareMatcher(tree.root.children["primary"]!.segments);
    expect(
      parseComparisonRefs(matched!.posParams!["refs"].path, tree.fragment),
    ).toEqual({
      base: "main",
      head: "fix/#5632-hash",
      recoveredFragment: true,
    });
  });
});

describe("RepoComparePageComponent", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("renders the shared diff viewer for slash and hash branch names and resolves omitted bases", async () => {
    const api = new FakeCompareApi();
    api.response = {
      base: "trunk",
      head: "fix/#5632-hash",
      mergeBase: "fork4200deadbeef",
      merged: false,
      diff: patch("WALK5632.md", "settled", "work in progress"),
    };
    api.tasks = [
      {
        id: "42",
        title: "Fix hash branch handling",
        state: "awaiting_submit",
        repo: "acme/widgets",
        branch: "fix/#5632-hash",
        mergeGates: [{ id: "ci", label: "CI green", state: "passed" }],
      },
    ];

    const { harness } = await openCompare(
      "/repos/acme/widgets/compare/fix/%235632-hash",
      api,
    );
    const el = harness.routeNativeElement!;

    const cmpCall = api.calls.find(
      (c) => c.key === "GET /api/repos/{owner}/{name}/compare",
    );
    expect(cmpCall).toEqual({
      key: "GET /api/repos/{owner}/{name}/compare",
      params: { owner: "acme", name: "widgets" },
      query: "base=&head=fix%2F%235632-hash",
    });

    expect(
      el.querySelector('[data-testid="compare-heading"]')?.textContent?.trim(),
    ).toBe("fix/#5632-hash");
    expect(
      el
        .querySelector('[data-testid="compare-status-pill"]')
        ?.textContent?.trim(),
    ).toBe("Open");
    const subtitle =
      el.querySelector('[data-testid="compare-subtitle"]')?.textContent ?? "";
    expect(subtitle).toContain("Into");
    expect(subtitle).toContain("trunk");
    expect(subtitle).toContain("forked at fork4200");

    const diffFiles = el.querySelector('[data-testid="compare-diff-files"]');
    expect(diffFiles?.textContent).toContain("WALK5632.md");
    expect(diffFiles?.textContent).toContain("work in progress");

    expect(
      el.querySelector('[data-testid="compare-task-link"]')?.textContent,
    ).toContain("Task #42");
    expect(
      el.querySelector('[data-testid="pr-merge-gates"]')?.textContent,
    ).toContain("CI green");
  });

  it("supports repos/<host>/<owner>/<name>/compare/... and shows Merged state", async () => {
    const api = new FakeCompareApi();
    api.response = {
      base: "main",
      head: "grain/task-6",
      mergeBase: "fork9900000000",
      merged: true,
      diff: patch("tables/template.json", "old", "merged change"),
    };

    const { harness } = await openCompare(
      "/repos/git.example.com/team/widgets/compare/main...grain%2Ftask-6",
      api,
    );
    const el = harness.routeNativeElement!;

    const cmpCall = api.calls.find(
      (c) => c.key === "GET /api/repos/{owner}/{name}/compare",
    );
    expect(cmpCall).toEqual({
      key: "GET /api/repos/{owner}/{name}/compare",
      params: { owner: "git.example.com/team", name: "widgets" },
      query: "base=main&head=grain%2Ftask-6",
    });

    expect(
      el
        .querySelector('[data-testid="compare-status-pill"]')
        ?.textContent?.trim(),
    ).toBe("Merged");
    expect(
      el.querySelector('[data-testid="compare-subtitle"]')?.textContent,
    ).toContain("Merged into");
    expect(
      el.querySelector('[data-testid="back-to-repo-link"]')?.textContent,
    ).toContain("git.example.com/team/widgets");
  });

  it("shows a designed error state for an unknown ref and retries on Try again", async () => {
    const api = new FakeCompareApi();
    api.failWith = new HttpErrorResponse({
      status: 404,
      error: {
        error:
          "GitHub has nothing to compare for main...nosuch-5632 in acme/widgets: one of the two branches does not exist",
      },
    });

    const { harness } = await openCompare(
      "/repos/acme/widgets/compare/main...nosuch-5632",
      api,
    );
    const el = harness.routeNativeElement!;

    const errCard = el.querySelector('[data-testid="compare-error"]');
    expect(errCard).not.toBeNull();
    expect(errCard?.getAttribute("role")).toBe("alert");
    expect(errCard?.textContent).toContain(
      "Could not compare main...nosuch-5632",
    );
    expect(errCard?.textContent).toContain(
      "GitHub has nothing to compare for main...nosuch-5632 in acme/widgets",
    );
    expect(el.querySelector('[data-testid="compare-diff-files"]')).toBeNull();

    api.failWith = null;
    api.response = {
      base: "main",
      head: "nosuch-5632",
      diff: patch("FIXED.md", "a", "b"),
    };
    (
      el.querySelector('[data-testid="compare-retry-btn"]') as HTMLButtonElement
    ).click();
    await new Promise((r) => setTimeout(r, 20));
    await harness.fixture.whenStable();

    expect(el.querySelector('[data-testid="compare-error"]')).toBeNull();
    expect(
      el.querySelector('[data-testid="compare-diff-files"]')?.textContent,
    ).toContain("FIXED.md");
  });

  it("shows the designed empty diff and missing-head states", async () => {
    const api = new FakeCompareApi();
    api.response = {
      base: "main",
      head: "release/1.0",
      merged: false,
      diff: "",
    };

    const { harness } = await openCompare(
      "/repos/acme/widgets/compare/main...release/1.0",
      api,
    );
    const el = harness.routeNativeElement!;
    expect(
      el.querySelector('[data-testid="compare-no-changes"]')?.textContent,
    ).toContain("There are no changes between these branches.");

    await harness.navigateByUrl("/repos/acme/widgets/compare");
    await harness.fixture.whenStable();
    harness.detectChanges();
    expect(
      harness.routeNativeElement?.querySelector(
        '[data-testid="compare-empty-head"]',
      )?.textContent,
    ).toContain("This link names no branch to compare.");
  });
});
