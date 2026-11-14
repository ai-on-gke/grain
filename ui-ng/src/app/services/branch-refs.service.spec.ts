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
import { afterEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "./access.service";
import { BranchRefsService } from "./branch-refs.service";

class FakeApi {
  calls: { route: string; params: Record<string, string>; query?: string }[] =
    [];
  ghHandler: (query?: string) => Promise<unknown> = async () => ({
    branches: [{ name: "main" }, { name: "feat/live" }],
  });

  get<T>(
    route: string,
    params: Record<string, string> = {},
    query?: string,
  ): Promise<T> {
    this.calls.push({ route, params, query });
    if (route === "GET /api/repos/{owner}/{name}/github-branches") {
      return this.ghHandler(query) as Promise<T>;
    }
    return Promise.resolve([] as T);
  }
}

describe("BranchRefsService", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  function setup(api = new FakeApi()) {
    TestBed.configureTestingModule({
      providers: [
        BranchRefsService,
        AccessService,
        { provide: GrainApi, useValue: api },
      ],
    });
    const access = TestBed.inject(AccessService);
    access.setConfig({
      access: { role: "admin", roles: ["admin"], full: true, scopes: [] },
    });
    return { svc: TestBed.inject(BranchRefsService), api, access };
  }

  it("coalesces concurrent lookups, refreshes missing branches once per cooldown, and serves cached hits", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-13T18:00:00Z"));
    const { svc, api } = setup();
    const [a, b] = await Promise.all([
      svc.hasBranch("acme/api", "feat/live"),
      svc.hasBranch("acme/api", "main"),
    ]);
    expect(a).toBe(true);
    expect(b).toBe(true);
    expect(api.calls).toHaveLength(1);
    expect(api.calls[0].route).toBe(
      "GET /api/repos/{owner}/{name}/github-branches",
    );

    // Missing branch triggers a single refresh=1 recheck, then respects 15s cooldown.
    api.ghHandler = async (query) => ({
      branches:
        query === "refresh=1"
          ? [{ name: "main" }, { name: "feat/live" }, { name: "feat/pushed" }]
          : [{ name: "main" }, { name: "feat/live" }],
    });
    expect(await svc.hasBranch("acme/api", "feat/pushed")).toBe(true);
    expect(api.calls).toHaveLength(2);
    expect(api.calls[1].query).toBe("refresh=1");

    expect(await svc.hasBranch("acme/api", "deleted-pr")).toBe(false);
    expect(api.calls).toHaveLength(2);

    svc.invalidate("acme/api");
    expect(await svc.hasBranch("acme/api", "main")).toBe(true);
    expect(api.calls).toHaveLength(3);
  });

  it("returns null without falling back to /branches when github-branches fails or is empty", async () => {
    const api = new FakeApi();
    api.ghHandler = async () => {
      throw new Error("github unavailable");
    };
    const { svc } = setup(api);
    expect(await svc.hasBranch("acme/api", "local-only")).toBeNull();
    expect(api.calls.map((c) => c.route)).toEqual([
      "GET /api/repos/{owner}/{name}/github-branches",
    ]);

    api.ghHandler = async () => ({ branches: [] });
    expect(await svc.hasBranch("acme/api", "main")).toBeNull();
    expect(api.calls.map((c) => c.route)).toEqual([
      "GET /api/repos/{owner}/{name}/github-branches",
      "GET /api/repos/{owner}/{name}/github-branches",
    ]);
  });

  it("returns null without API calls for host-prefixed or inaccessible repos", async () => {
    const { svc, api, access } = setup();
    expect(await svc.hasBranch("git.example.com/acme/api", "main")).toBeNull();
    expect(api.calls).toHaveLength(0);

    access.setConfig({
      access: { role: "viewer", roles: ["viewer"], full: false, scopes: [] },
    });
    expect(await svc.hasBranch("acme/api", "main")).toBeNull();
    expect(api.calls).toHaveLength(0);
  });

  it("skips github-branches when gitCredentialsConfigured is false outside configRepo", async () => {
    const { svc, api, access } = setup();
    access.setConfig({
      configRepo: "grain/config",
      gitCredentialsConfigured: false,
      access: { role: "admin", roles: ["admin"], full: true, scopes: [] },
    });
    expect(await svc.hasBranch("acme/api", "main")).toBeNull();
    expect(api.calls).toHaveLength(0);

    expect(await svc.hasBranch("grain/config", "main")).toBe(true);
    expect(api.calls).toHaveLength(1);
  });
});
