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
import { signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { GrainConfig, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { REPO_ORDER_KEY } from "./repo-list";
import { ReposPageComponent } from "./repos-page.component";

class FakeApi {
  calls: { method: string; key: string; body?: unknown }[] = [];
  configBody: GrainConfig = {
    targetRepos: ["acme/alpha", "acme/beta"],
    access: { role: "grain/operator", roles: [], full: true, scopes: [] },
  };
  config = () => Promise.resolve(structuredClone(this.configBody));
  get = (key: string) => {
    this.calls.push({ method: "GET", key });
    return Promise.resolve({
      available: true,
      repos: [{ repo: "acme/gamma" }],
      connections: [{ credential: "gh", host: "github.com", ok: true }],
    });
  };
  post = (key: string, _p?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, body });
    if (key === "POST /api/repos") {
      const b = body as { repo: string };
      this.configBody = {
        ...this.configBody,
        targetRepos: [...(this.configBody["targetRepos"] as string[]), b.repo],
      };
    }
    return Promise.resolve({});
  };
}

class FakeFeed {
  tasks = signal<Task[]>([]);
  start() {}
  stop() {}
}

async function mount(url = "/repos", api = new FakeApi(), tasks: Task[] = []) {
  const feed = new FakeFeed();
  feed.tasks.set(tasks);
  TestBed.configureTestingModule({
    providers: [
      provideRouter([
        { path: "repos", component: ReposPageComponent },
        { path: "repos/:owner/:name", children: [] },
        { path: "tasks", children: [] },
      ]),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
      { provide: TaskFeedStore, useValue: feed },
    ],
  });
  TestBed.inject(AccessService).setConfig(api.configBody);
  const harness = await RouterTestingHarness.create(url);
  harness.fixture.autoDetectChanges(true);
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  await harness.fixture.whenStable();
  return { harness, el: harness.fixture.nativeElement as HTMLElement, api };
}

const names = (el: HTMLElement) =>
  Array.from(el.querySelectorAll(".repos-name")).map((a) =>
    a.textContent?.trim(),
  );

describe("ReposPageComponent (/next/repos)", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => TestBed.resetTestingModule());

  it("lists repos with Blocked first and links each to its page and tasks", async () => {
    const { el } = await mount("/repos", new FakeApi(), [
      {
        id: "1",
        title: "a",
        state: "failed",
        repo: "acme/beta",
        blocked: true,
      } as Task,
      { id: "2", title: "b", state: "running", repo: "acme/beta" } as Task,
    ]);
    expect(names(el)).toEqual(["acme/alpha", "acme/beta"]);
    const beta = el.querySelector('[data-repo="acme/beta"]')!;
    const chips = Array.from(beta.querySelectorAll("[data-chip]")).map((c) =>
      c.getAttribute("data-chip"),
    );
    expect(chips[0]).toBe("blocked");
    expect(beta.querySelector(".repos-name")!.getAttribute("href")).toBe(
      "/next/repos/acme/beta",
    );
    expect(beta.querySelector(".repos-tasks")!.getAttribute("href")).toBe(
      "/next/tasks?repo=acme%2Fbeta",
    );
    expect(
      el.querySelector('[data-testid="repos-count"]')!.textContent,
    ).toContain("2");
  });

  it("filters from the URL and offers no reorder while filtered", async () => {
    const { el } = await mount("/repos?q=bet");
    expect(names(el)).toEqual(["acme/beta"]);
    expect(el.querySelector("[data-handle]")).toBeNull();
  });

  it("moves a row with the keyboard and stores the order", async () => {
    const { el, harness } = await mount();
    const handle = el.querySelector<HTMLButtonElement>(
      '[data-handle="acme/beta"]',
    )!;
    handle.click();
    harness.fixture.detectChanges();
    handle.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }),
    );
    await harness.fixture.whenStable();
    expect(names(el)).toEqual(["acme/beta", "acme/alpha"]);
    expect(JSON.parse(localStorage.getItem(REPO_ORDER_KEY)!)).toEqual([
      "acme/beta",
      "acme/alpha",
    ]);
    expect(
      el.querySelector('[data-testid="repos-announcer"]')!.textContent,
    ).toContain("acme/beta moved to position 1 of 2");
  });

  it("adds a repo and shows it landing", async () => {
    const { el, api, harness } = await mount();
    const input = el.querySelector<HTMLInputElement>(
      'input[aria-label="Repository to add"]',
    )!;
    input.value = "acme/gamma";
    input.dispatchEvent(new Event("input"));
    harness.fixture.detectChanges();
    el.querySelector<HTMLFormElement>("form.repos-add")!.dispatchEvent(
      new Event("submit"),
    );
    await new Promise((r) => setTimeout(r, 10));
    await harness.fixture.whenStable();
    expect(api.calls).toContainEqual({
      method: "POST",
      key: "POST /api/repos",
      body: { repo: "acme/gamma" },
    });
    expect(names(el)).toContain("acme/gamma");
    expect(
      el.querySelector('[data-testid="repo-discovery-status"]')!.textContent,
    ).toContain("Added acme/gamma");
  });

  it("asks before the first add narrows an unrestricted deployment", async () => {
    const api = new FakeApi();
    api.configBody = { ...api.configBody, targetRepos: [] };
    const { el, harness } = await mount("/repos", api, [
      { id: "1", title: "a", state: "queued", repo: "acme/old" } as Task,
    ]);
    const input = el.querySelector<HTMLInputElement>(
      'input[aria-label="Repository to add"]',
    )!;
    input.value = "acme/new";
    input.dispatchEvent(new Event("input"));
    el.querySelector<HTMLFormElement>("form.repos-add")!.dispatchEvent(
      new Event("submit"),
    );
    await new Promise((r) => setTimeout(r, 10));
    await harness.fixture.whenStable();
    const dialog = el.querySelector('[data-testid="confirm-dialog"]')!;
    expect(dialog.textContent).toContain(
      "Off the allowlist as of this click: acme/old.",
    );
    expect(api.calls.some((c) => c.key === "POST /api/repos")).toBe(false);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await harness.fixture.whenStable();
    expect(api.calls.some((c) => c.key === "POST /api/repos")).toBe(false);
    expect(input.disabled).toBe(false);
  });
});
