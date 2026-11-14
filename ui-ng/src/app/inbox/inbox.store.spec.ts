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
import { vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { LocalScriptApproval, Notification, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { InboxStore, wantsDiff } from "./inbox.store";

class FakeEventSource {
  static last: FakeEventSource | null = null;
  readyState = 1;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (e: MessageEvent) => void>();
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    this.listeners.set(type, fn);
  }
  close(): void {}
  emit(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

class FakeApi {
  notes: Notification[] = [];
  scripts: LocalScriptApproval[] = [];
  tasksList: Task[] = [];
  branches: Record<string, string[]> = {};
  reads = 0;
  diffCalls = 0;
  taskDetailCalls: string[] = [];
  config = () => Promise.resolve({ access: null, version: "v1" });
  tasks = () => Promise.resolve(this.tasksList);
  task = (id: string) => {
    this.taskDetailCalls.push(id);
    return Promise.resolve({ id });
  };
  diffPreview = () => {
    this.diffCalls++;
    return Promise.reject(new Error("404"));
  };
  get = <T>(route: string, params: { owner?: string; name?: string } = {}) => {
    if (
      route === "GET /api/repos/{owner}/{name}/github-branches" ||
      route === "GET /api/repos/{owner}/{name}/branches"
    ) {
      const repo = `${params.owner}/${params.name}`;
      return Promise.resolve({ branches: this.branches[repo] ?? [] } as T);
    }
    return Promise.resolve([] as T);
  };
  openNotifications = () => {
    this.reads++;
    return Promise.resolve(this.notes);
  };
  localScriptApprovals = () => Promise.resolve(this.scripts);
}

const settle = () => new Promise((r) => setTimeout(r, 400));

describe("InboxStore", () => {
  afterEach(() => TestBed.inject(InboxStore).stop());

  it("reads the queue again on notification and host-script events, not only on its timer", async () => {
    localStorage.clear();
    const api = new FakeApi();
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const events = TestBed.inject(GrainEvents);
    events.factory = (url) =>
      new FakeEventSource(url) as unknown as EventSource;
    const store = TestBed.inject(InboxStore);
    store.start();
    await settle();
    expect(store.notifications()).toEqual([]);
    const src = FakeEventSource.last!;

    api.notes = [
      {
        id: 7,
        subject: "ci.red:acme/widgets",
        family: "ci.red",
        severity: "warning",
        title: "acme/widgets main is red",
      } as Notification,
    ];
    const before = api.reads;
    src.emit("notification.raised", { type: "notification.raised" });
    await settle();
    expect(api.reads).toBe(before + 1);
    expect(store.notifications().map((n) => n.id)).toEqual([7]);

    api.notes = [];
    src.emit("notification.cleared", { type: "notification.cleared" });
    await settle();
    expect(store.notifications()).toEqual([]);

    for (const type of [
      "notification.acked",
      "local_script.pending",
      "local_script.answered",
    ]) {
      const n = api.reads;
      src.emit(type, { type });
      await settle();
      expect(api.reads, type).toBe(n + 1);
    }
  });

  it("previews pushed-branch proposals, skips unpushed proposals, merged PRs, and missing branches, and memoizes 404s", async () => {
    localStorage.clear();
    expect(
      wantsDiff({
        key: "task:1:proposal",
        kind: "proposal",
        groupId: "proposal",
        ref: "#1",
        title: "P",
        repo: "acme/web",
        owner: "",
        summary: "",
        pillLabel: "",
        pillTone: "",
        at: null,
        rationale: "",
        taskId: "1",
        taskLink: "/task/1",
        externalLink: null,
        task: { id: "1", title: "P", state: "proposed", repo: "acme/web" },
        actions: [],
      }),
    ).toBe(true);

    const api = new FakeApi();
    api.branches["acme/web"] = ["main", "grain/task-13"];
    api.tasksList = [
      {
        id: "10",
        title: "Merged task",
        state: "awaiting_submit",
        repo: "acme/web",
        branch: "grain/gone",
        pullRequest: "acme/web#9",
        prMergedAt: "2026-11-12T00:00:00Z",
      },
      {
        id: "11",
        title: "Deleted branch review",
        state: "awaiting_submit",
        repo: "acme/web",
        branch: "grain/deleted",
        pullRequest: "acme/web#11",
        updatedAt: "2026-11-13T00:00:00Z",
      },
      {
        id: "12",
        title: "Live branch missing diff",
        state: "awaiting_submit",
        repo: "acme/web",
        branch: "main",
        pullRequest: "acme/web#12",
        updatedAt: "2026-11-13T00:00:00Z",
      },
      {
        id: "13",
        title: "Pushed proposal",
        state: "proposed",
        repo: "acme/web",
        branch: "grain/task-13",
        updatedAt: "2026-11-13T00:00:00Z",
      },
      {
        id: "14",
        title: "Unpushed proposal",
        state: "proposed",
        repo: "acme/web",
        branch: "grain/task-14",
        updatedAt: "2026-11-13T00:00:00Z",
      },
    ];
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const access = TestBed.inject(AccessService);
    access.setConfig({ access: null });
    const store = TestBed.inject(InboxStore);
    await store.refresh();

    // Selected task #11 has branch absent from remote -> skips diffPreview completely.
    store.select("task:11:submit");
    await store.ensureDiff();
    expect(api.diffCalls).toBe(0);
    expect(store.diffs()["11"]).toBeNull();

    // Unpushed proposal #14 skips diffPreview when remote branch is absent.
    store.select("task:14:proposal");
    await store.ensureDiff();
    expect(api.diffCalls).toBe(0);
    expect(store.diffs()["14"]).toBeNull();

    // Pushed proposal #13 invokes diffPreview because its branch is present remotely.
    store.select("task:13:proposal");
    await store.ensureDiff();
    expect(api.diffCalls).toBe(1);

    // Task #12 has present branch -> tries once, catches error, memoizes null for same version.
    store.select("task:12:submit");
    await store.ensureDiff();
    expect(api.diffCalls).toBe(2);
    await store.ensureDiff();
    expect(api.diffCalls).toBe(2);
  });

  it("skips repo-less task detail hydration when tasks:read has repoLimits", async () => {
    localStorage.clear();
    const api = new FakeApi();
    api.config = () =>
      Promise.resolve({
        access: {
          role: "repo-user",
          roles: ["repo-user"],
          full: false,
          scopes: ["tasks:read"],
          repoLimits: { "tasks:read": ["acme/web"] },
        },
        version: "v1",
      });
    api.tasksList = [
      {
        id: "77",
        title: "Global no-repo stopped task",
        state: "awaiting_reply",
        turnStopped: true,
        repo: "",
      },
    ];
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const store = TestBed.inject(InboxStore);
    await store.refresh();
    expect(api.taskDetailCalls).not.toContain("77");
  });

  it("defers queue detail/diff hydration during shell background polling until retainPage(), bounds taskSummary concurrency to <=4, and upgrades selected summaries to full task()", async () => {
    localStorage.clear();
    let active = 0;
    let peak = 0;
    const summaryCalls: string[] = [];
    const fullCalls: string[] = [];
    const api = new FakeApi();
    (api as unknown as Record<string, unknown>)["taskSummary"] = async (
      id: string,
    ) => {
      summaryCalls.push(id);
      active++;
      peak = Math.max(peak, active);
      await new Promise((r) => setTimeout(r, 10));
      active--;
      return { id };
    };
    api.task = (id: string) => {
      fullCalls.push(id);
      return Promise.resolve({ id });
    };
    api.branches["acme/web"] = ["main"];
    api.tasksList = Array.from({ length: 10 }, (_, i) => ({
      id: String(200 + i),
      title: `Submit ${i}`,
      state: "awaiting_submit",
      repo: "acme/web",
      branch: "main",
      pullRequest: `acme/web#${200 + i}`,
      updatedAt: "2026-11-13T00:00:00Z",
    }));
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const access = TestBed.inject(AccessService);
    access.setConfig({ access: null });
    const store = TestBed.inject(InboxStore);

    store.start(true);
    await new Promise((r) => setTimeout(r, 40));
    expect(summaryCalls.length).toBe(0);
    expect(fullCalls.length).toBe(0);
    expect(api.diffCalls).toBe(0);

    store.retainPage();
    await vi.waitFor(() => {
      expect(fullCalls).toEqual(["200"]);
      expect(summaryCalls.length).toBe(9);
    });
    expect(peak).toBeLessThanOrEqual(4);
    expect(api.diffCalls).toBe(1);

    // Selecting a row that was only summary-hydrated upgrades to full api.task().
    store.select("task:205:submit");
    await store.ensureDetail();
    expect(fullCalls).toContain("205");
  });

  it("caches unchanged summaryUpdatedAt across refreshes, prevents slow summary from clobbering selected full detail, and blocks repo-limited actions on repo-less tasks", async () => {
    localStorage.clear();
    let releaseSummary201!: () => void;
    const summaryCounts: Record<string, number> = {};
    const postCalls: string[] = [];
    const api = new FakeApi();
    api.config = () =>
      Promise.resolve({
        access: {
          role: "repo-user",
          roles: ["repo-user"],
          full: false,
          scopes: ["tasks:read", "tasks:write"],
          repoLimits: {
            "tasks:read": ["acme/web"],
            "tasks:write": ["acme/web"],
          },
        },
        version: "v1",
      });
    (api as unknown as Record<string, unknown>)["taskSummary"] = async (
      id: string,
    ) => {
      summaryCounts[id] = (summaryCounts[id] ?? 0) + 1;
      if (id === "201" && summaryCounts[id] === 1) {
        await new Promise<void>((r) => {
          releaseSummary201 = r;
        });
      }
      return { id, updatedAt: "2026-11-13T00:00:00Z", body: "summary-only" };
    };
    api.task = (id: string) =>
      Promise.resolve({
        id,
        updatedAt: "2026-11-13T00:00:00Z",
        comments: [{ id: "c1", body: "full-detail" }],
      } as unknown as { id: string });
    (api as unknown as Record<string, unknown>)["post"] = (route: string) => {
      postCalls.push(route);
      return Promise.resolve({});
    };
    api.tasksList = [
      {
        id: "200",
        title: "Submit 200",
        state: "awaiting_submit",
        repo: "acme/web",
        branch: "main",
        pullRequest: "acme/web#200",
        updatedAt: "2026-11-13T00:00:00Z",
      },
      {
        id: "201",
        title: "Submit 201",
        state: "awaiting_submit",
        repo: "acme/web",
        branch: "main",
        pullRequest: "acme/web#201",
        updatedAt: "2026-11-13T00:00:00Z",
      },
      {
        id: "202",
        title: "Proposal no repo",
        state: "proposed",
        repo: "",
        updatedAt: "2026-11-13T00:00:00Z",
      },
    ];
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const store = TestBed.inject(InboxStore);
    store.retainPage();
    store.start();
    await new Promise((r) => setTimeout(r, 40));

    // While slow summary for 201 is in flight, select 201 and load full detail.
    store.select("task:201:submit");
    await store.ensureDetail();
    releaseSummary201?.();
    await new Promise((r) => setTimeout(r, 30));
    expect(store.details()["201"]).toMatchObject({
      id: "201",
      comments: [{ id: "c1", body: "full-detail" }],
    });

    // Subsequent refresh skips summary re-fetch when updatedAt is unchanged.
    await store.refresh();
    expect(summaryCounts["201"]).toBe(1);

    // Repo-limited user cannot approve repo-less task #202.
    const entry202 = store.liveEntries().find((e) => e.taskId === "202");
    const approve = entry202?.actions.find((a) => a.id === "approve");
    expect(approve).toBeDefined();
    expect(await store.actByKey(entry202!.key, approve!)).toBe(false);
    expect(store.actionError()).toContain("No permission");
    expect(postCalls).toEqual([]);
  });
});
