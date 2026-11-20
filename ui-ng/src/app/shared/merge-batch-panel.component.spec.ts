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
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import type {
  GrainConfig,
  MergeBatch,
  MergeBatchesResponse,
} from "../api/models";
import { AccessService } from "../services/access.service";
import {
  MergeBatchPanelComponent,
  batchAge,
  batchStanding,
  memberPrUrl,
  mergeQueueBatching,
} from "./merge-batch-panel.component";

const STARTED = "2026-09-19T12:00:00Z";
const NOW = new Date("2026-09-19T12:22:00Z").getTime();

function sampleBatch(over: Partial<MergeBatch> = {}): MergeBatch {
  return {
    repo: "acme/widgets",
    branch: "main",
    ref: "grain-merge-queue/main",
    baseCommit: "base456789abcdef",
    commit: "abc123def4567890",
    runId: "run-42",
    startedAt: STARTED,
    running: true,
    members: [
      {
        task: "1",
        title: "Fix the parser",
        number: 11,
        headRef: "grain/task-1",
        head: "aaa111222333",
      },
      {
        task: "2",
        title: "Rename the thing",
        number: 12,
        headRef: "grain/task-2",
        head: "bbb111222333",
      },
    ],
    ...over,
  };
}

class FakeBatchApi {
  calls: { key: string; query?: string }[] = [];
  response: MergeBatchesResponse = {
    batches: [sampleBatch()],
    maxAgeSeconds: 5400,
  };

  get = (key: string, _params?: unknown, query?: string) => {
    this.calls.push({ key, query });
    return Promise.resolve(this.response);
  };
}

function renderPanel(
  opts: {
    repo?: string;
    taskId?: string;
    api?: FakeBatchApi;
    access?: GrainConfig["access"];
  } = {},
) {
  const api = opts.api ?? new FakeBatchApi();
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      { provide: GrainApi, useValue: api },
      AccessService,
    ],
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
  const fixture = TestBed.createComponent(MergeBatchPanelComponent);
  fixture.componentRef.setInput("repo", opts.repo ?? "acme/widgets");
  fixture.componentRef.setInput("taskId", opts.taskId ?? "");
  fixture.componentRef.setInput("pollMs", 0);
  fixture.detectChanges();
  return { fixture, el: fixture.nativeElement as HTMLElement, api };
}

describe("MergeBatchPanelComponent helpers", () => {
  it("formats elapsed time against maxAgeSeconds while running and finished", () => {
    expect(batchAge(sampleBatch(), 5400, NOW)).toBe("22m of 90m");
    expect(
      batchAge(
        sampleBatch({
          running: false,
          finishedAt: "2026-09-19T12:30:00Z",
          conclusion: "success",
        }),
        5400,
        NOW,
      ),
    ).toBe("ran for 30m");
  });

  it("maps batch conclusions to Testing, Passed, Failed, and Abandoned", () => {
    expect(batchStanding(sampleBatch()).label).toBe("Testing");
    expect(
      batchStanding(sampleBatch({ running: false, conclusion: "success" }))
        .label,
    ).toBe("Passed");
    expect(
      batchStanding(sampleBatch({ running: false, conclusion: "failure" }))
        .label,
    ).toBe("Failed");
    expect(
      batchStanding(sampleBatch({ running: false, conclusion: "errored" }))
        .label,
    ).toBe("Abandoned");
  });

  it("detects overlap re-check tasks and skips closed or ordinary waiting_ci tasks", () => {
    expect(
      mergeQueueBatching({
        state: "completed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote:
          "grain CI is re-checking this change on top of main before it merges",
      }),
    ).toBe(true);
    expect(
      mergeQueueBatching({ state: "completed", mergeQueueState: "batching" }),
    ).toBe(true);
    expect(
      mergeQueueBatching({
        state: "completed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote: "waiting on checks",
      }),
    ).toBe(false);
    expect(
      mergeQueueBatching({ state: "closed", mergeQueueState: "batching" }),
    ).toBe(false);
  });

  it("builds PR links for default and host-qualified repos, skipping grain/config", () => {
    expect(memberPrUrl("acme/widgets", 11)).toBe(
      "https://github.com/acme/widgets/pull/11",
    );
    expect(memberPrUrl("git.example.com/acme/widgets", 7)).toBe(
      "https://git.example.com/acme/widgets/pull/7",
    );
    expect(memberPrUrl("grain/config", 3)).toBeNull();
  });
});

describe("MergeBatchPanelComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("renders standing chip, age, throwaway ref, CI run link, and ordered task/PR links on repo page", async () => {
    const { fixture, el, api } = renderPanel();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(api.calls).toEqual([
      { key: "GET /api/merge-batches", query: "repo=acme%2Fwidgets" },
    ]);
    expect(el.textContent).toContain("2 changes on main");
    expect(
      el.querySelector('[data-testid="merge-batch-standing"]')?.textContent,
    ).toContain("Testing");
    expect(
      el.querySelector('[data-testid="merge-batch-age"]')?.textContent,
    ).toContain("of 90m");
    expect(
      el.querySelector('[data-testid="merge-batch-ref"]')?.textContent?.trim(),
    ).toBe("grain-merge-queue/main at abc123def456");

    const runLink = el.querySelector<HTMLAnchorElement>(
      '[data-testid="merge-batch-run-link"]',
    );
    expect(runLink?.getAttribute("href")).toBe("/ci/runs/run-42");

    const taskLinks = [
      ...el.querySelectorAll<HTMLAnchorElement>(
        '[data-testid="merge-batch-member-task"]',
      ),
    ];
    expect(taskLinks.map((a) => a.textContent?.trim())).toEqual([
      "Fix the parser",
      "Rename the thing",
    ]);
    expect(taskLinks.map((a) => a.getAttribute("href"))).toEqual([
      "/task/1",
      "/task/2",
    ]);

    const prLinks = [
      ...el.querySelectorAll<HTMLAnchorElement>(
        '[data-testid="merge-batch-member-pr"]',
      ),
    ];
    expect(prLinks.map((a) => a.getAttribute("href"))).toEqual([
      "https://github.com/acme/widgets/pull/11",
      "https://github.com/acme/widgets/pull/12",
    ]);
  });

  it("says explicitly when no re-check is running on a repository page", async () => {
    const api = new FakeBatchApi();
    api.response = { batches: [], maxAgeSeconds: 5400 };
    const { fixture, el } = renderPanel({ api });
    await fixture.whenStable();
    fixture.detectChanges();

    expect(
      el.querySelector('[data-testid="merge-rechecks-empty"]')?.textContent,
    ).toContain("No re-check is running.");
  });

  it("on a task page renders only the member's batch highlighted, or nothing when absent", async () => {
    const api = new FakeBatchApi();
    api.response = {
      batches: [sampleBatch(), sampleBatch({ branch: "release", members: [] })],
      maxAgeSeconds: 5400,
    };
    const member = renderPanel({ api, taskId: "2" });
    await member.fixture.whenStable();
    member.fixture.detectChanges();

    expect(
      member.el.querySelector('[data-testid="task-merge-recheck"]'),
    ).not.toBeNull();
    expect(member.el.textContent).not.toContain("on release");
    const items = [...member.el.querySelectorAll("li")];
    expect(items[0].classList.contains("is-current")).toBe(false);
    expect(items[1].classList.contains("is-current")).toBe(true);
    TestBed.resetTestingModule();

    const absent = renderPanel({ api, taskId: "99" });
    await absent.fixture.whenStable();
    absent.fixture.detectChanges();
    expect(absent.el.textContent?.trim()).toBe("");
  });

  it("does not fire GET /api/merge-batches without ci:read", async () => {
    const api = new FakeBatchApi();
    const { fixture, el } = renderPanel({
      api,
      access: {
        role: "repos-only",
        roles: ["repos-only"],
        full: false,
        scopes: ["repos:read"],
      },
    });
    await fixture.whenStable();
    fixture.detectChanges();

    expect(api.calls).toHaveLength(0);
    expect(
      el.querySelector('[data-testid="merge-rechecks-denied"]'),
    ).not.toBeNull();
  });
});
