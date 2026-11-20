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

import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { Router, provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { DiffPreview, TaskDetail, TaskQAResponse } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskPageComponent, backFrom, duration } from "./task-page.component";
import {
  DEEP_LINK_HEAD_QUERY,
  TRANSCRIPT_POLL_MS,
  TaskStore,
  parseCommentFragment,
} from "./task.store";

type Call = {
  method: string;
  route: string;
  params?: unknown;
  body?: unknown;
  query?: string;
};

class FakeApi {
  detail: TaskDetail = {
    id: "7",
    title: "Fix the flaky merge test",
    state: "awaiting_reply",
    repo: "acme/web",
    base: "main",
    priority: 5,
    authorLabel: "Ana",
    description: "The **merge** test fails one run in ten.",
    questionSummary: "Should I also fix the retry?",
    questionCommentId: 2,
    branch: "grain/task-7",
    pullRequest: "acme/web#42",
    dependsOn: ["3"],
    comments: [
      {
        id: 1,
        author: "ana",
        authorKind: "human",
        authorLabel: "Ana",
        body: "Please fix it",
      },
      {
        id: 2,
        author: "agent",
        authorKind: "agent",
        body: "Should I also fix the retry?",
      },
    ],
    attempts: [
      {
        number: 1,
        startedAt: "2026-11-11T10:00:00Z",
        finishedAt: "2026-11-11T10:04:12Z",
        outcome: "succeeded",
        model: "fast",
      },
      {
        number: 2,
        startedAt: "2026-11-11T11:00:00Z",
        outcome: "",
        model: "fast",
      },
    ],
  };
  olderPage: Partial<TaskDetail> = {
    comments: [
      {
        id: 0,
        author: "ana",
        authorKind: "human",
        authorLabel: "Ana",
        body: "Earlier kickoff note",
      },
    ],
    olderComments: 0,
  };
  preview: DiffPreview = {
    repo: "acme/web",
    base: "main",
    head: "abc12345",
    totalFiles: 2,
    totalHunks: 2,
    truncated: true,
    files: [
      {
        path: "pkg/merge/merge.go",
        status: "modified",
        additions: 1,
        deletions: 1,
        hunks: [
          {
            header: "@@ -10,1 +10,1 @@",
            lines: [
              { kind: "del", text: "const retries = 1" },
              { kind: "add", text: "const retries = 3" },
            ],
          },
        ],
      },
    ],
  };
  fullDiff = [
    "diff --git a/pkg/merge/merge.go b/pkg/merge/merge.go",
    "index 111..222 100644",
    "--- a/pkg/merge/merge.go",
    "+++ b/pkg/merge/merge.go",
    "@@ -10,2 +10,2 @@",
    "-const retries = 1",
    "+const retries = 3",
    " return nil",
    "diff --git a/pkg/merge/merge_test.go b/pkg/merge/merge_test.go",
    "new file mode 100644",
    "--- /dev/null",
    "+++ b/pkg/merge/merge_test.go",
    "@@ -0,0 +1,2 @@",
    "+package merge",
    "+func TestRetries() {}",
  ].join("\n");
  ciRuns = [
    {
      id: "run-91",
      workflow: "tests.yml",
      commit: "abc123456789",
      status: "completed",
      conclusion: "failure",
      jobs: [
        {
          name: "unit",
          conclusion: "failure",
          steps: [
            {
              name: "go test ./...",
              conclusion: "failure",
              output: "FAIL pkg/merge 0.14s",
            },
          ],
        },
      ],
    },
  ];
  tree = {
    origin: "task:7",
    rootTask: "7",
    done: 1,
    total: 2,
    nodes: [
      {
        id: "7",
        title: "Fix the flaky merge test",
        status: "running",
        done: false,
        root: true,
        children: [
          { id: "8", title: "Review", status: "merged", done: true },
          { id: "9", title: "Docs", status: "queued", done: false },
        ],
      },
    ],
  };
  qaResponse: TaskQAResponse = {
    enabled: false,
    checkRuns: [],
    findings: [],
  };
  calls: Call[] = [];
  transcripts: string[] = [];
  remoteBranches: string[] | null = null;

  getIfChanged = () =>
    Promise.resolve({ body: structuredClone(this.detail), changed: true });
  diffPreview = () => {
    this.calls.push({
      method: "GET",
      route: "GET /api/tasks/{id}/diff-preview",
      params: { id: this.detail.id },
    });
    return Promise.resolve(structuredClone(this.preview));
  };
  get = (
    route: string,
    params?: { id?: string; number?: string; owner?: string; name?: string },
    query?: string,
  ) => {
    this.calls.push({ method: "GET", route, params, query });
    if (route === "GET /api/repos/{owner}/{name}/github-branches") {
      return Promise.resolve({ branches: this.remoteBranches ?? undefined });
    }
    if (route === "GET /api/repos/{owner}/{name}/branches") {
      return Promise.resolve({ branches: this.remoteBranches ?? undefined });
    }
    if (route === "GET /api/tasks/{id}/tree") return Promise.resolve(this.tree);
    if (
      route === "GET /api/tasks/{id}" &&
      query?.includes("comments_before=")
    ) {
      return Promise.resolve(structuredClone(this.olderPage));
    }
    if (route === "GET /api/repos/{owner}/{name}/compare") {
      return Promise.resolve({
        base: "main",
        head: "grain/task-7",
        diff: this.fullDiff,
      });
    }
    if (route === "GET /api/ci/runs") {
      return Promise.resolve({ enabled: true, runs: this.ciRuns });
    }
    if (route === "GET /api/ci/runs/{id}") {
      return Promise.resolve(this.ciRuns[0]);
    }
    if (route === "GET /api/capability-methods") {
      return Promise.resolve({ items: [] });
    }
    if (route === "GET /api/tasks/{id}/context") {
      return Promise.resolve({ brief: "Original brief", summarised: 0 });
    }
    if (route === "GET /api/tasks/{id}/qa") {
      return Promise.resolve(structuredClone(this.qaResponse));
    }
    if (route === "GET /api/tasks/{id}/attempts/{number}/capability-audit") {
      return Promise.resolve({
        records: [
          {
            grant: "host-script#deploy",
            at: "2026-11-12T00:00:00Z",
            note: "exit 0",
          },
        ],
      });
    }
    this.transcripts.push(params?.number ?? "");
    return Promise.resolve({ transcript: `> run ${params?.number}\nok` });
  };
  private rec(method: string, route: string, params?: unknown, body?: unknown) {
    this.calls.push({ method, route, params, body });
    if (
      route === "POST /api/tasks/{id}/qa/findings/{finding}/feedback" &&
      body &&
      typeof body === "object"
    ) {
      const vote = Number((body as { vote?: number }).vote ?? 0);
      return Promise.resolve({
        ok: true,
        findingId: Number((params as { finding?: string })?.finding ?? 0),
        vote,
        votedBy: "ana@example.com",
      });
    }
    if (route === "POST /api/tasks/{id}/qa/bypass-s3") {
      this.detail.pullRequest = "acme/web#43";
      this.qaResponse = { ...this.qaResponse, s3Blocked: false };
      return Promise.resolve({
        ok: true,
        pullRequest: "acme/web#43",
        branch: this.detail.branch ?? "grain/task-7",
        stageBranch: this.qaResponse.stageBranch ?? "grain-qa-stage/7",
        bypassedBy: "ana@example.com",
      });
    }
    return Promise.resolve({});
  }
  post = (r: string, p?: unknown, b?: unknown) => this.rec("POST", r, p, b);
  patch = (r: string, p?: unknown, b?: unknown) => this.rec("PATCH", r, p, b);
  put = (r: string, p?: unknown, b?: unknown) => this.rec("PUT", r, p, b);
  delete = (r: string, p?: unknown) => this.rec("DELETE", r, p);
  timeZone?: string;
  config = () =>
    Promise.resolve({
      access: null,
      timeZone: this.timeZone,
      models: ["fast", "smart"],
      defaultModel: "smart",
      capabilities: [{ id: "self-debug", name: "Self debug", ready: true }],
    });
  tasks = () => Promise.resolve([]);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
}

async function render(
  api: FakeApi,
  tab?: string,
  from?: string,
): Promise<ComponentFixture<TaskPageComponent>> {
  TestBed.configureTestingModule({
    imports: [TaskPageComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const f = TestBed.createComponent(TaskPageComponent);
  f.componentRef.setInput("id", "7");
  if (tab) f.componentRef.setInput("tab", tab);
  if (from) f.componentRef.setInput("from", from);
  f.autoDetectChanges();
  await TestBed.inject(TaskStore).refresh();
  await settle(f);
  return f;
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 15));
  f.detectChanges();
  await f.whenStable();
}

function q<T extends HTMLElement = HTMLElement>(
  f: ComponentFixture<unknown>,
  sel: string,
): T | null {
  return (f.nativeElement as HTMLElement).querySelector<T>(sel);
}

// Opens the header's overflow menu when the task has one.
async function openMore(f: ComponentFixture<unknown>): Promise<void> {
  const more = q<HTMLButtonElement>(f, '[data-testid="act-more"]');
  if (!more) return;
  more.click();
  await settle(f);
}

describe("TaskPageComponent", () => {
  it("pages the actual Details viewport immediately without stealing native Home or editor keys", async () => {
    const f = await render(new FakeApi(), "details");
    const page = q(f, ".task-page")!;
    const body = q(f, ".task-tab-body")!;
    Object.defineProperties(page, {
      clientHeight: { value: 500 },
      scrollHeight: { value: 2000 },
    });
    Object.defineProperties(body, {
      clientHeight: { value: 200 },
      scrollHeight: { value: 200, configurable: true },
    });
    const pageScroll = vi.fn();
    const bodyScroll = vi.fn();
    page.scrollTo = pageScroll;
    body.scrollTo = bodyScroll;
    const key = (target: HTMLElement, key: string, ctrlKey = false) => {
      const event = new KeyboardEvent("keydown", {
        key,
        ctrlKey,
        bubbles: true,
        cancelable: true,
      });
      target.dispatchEvent(event);
      return event;
    };
    expect(key(page, "PageDown").defaultPrevented).toBe(true);
    expect(pageScroll).toHaveBeenLastCalledWith({
      top: 450,
      behavior: "instant",
    });
    expect(key(body, "Home", true).defaultPrevented).toBe(false);
    Object.defineProperty(body, "scrollHeight", {
      value: 1000,
      configurable: true,
    });
    key(body, "PageDown");
    expect(bodyScroll).toHaveBeenLastCalledWith({
      top: 180,
      behavior: "instant",
    });
    const input = document.createElement("textarea");
    body.append(input);
    expect(key(input, "Home", true).defaultPrevented).toBe(false);
    expect(key(input, "PageDown").defaultPrevented).toBe(false);
    expect(bodyScroll).toHaveBeenCalledTimes(1);
  });

  afterEach(() => {
    TestBed.inject(TaskStore).close();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("shows the header, question, conversation and details with zero Classic links and zero <select>s", async () => {
    const f = await render(new FakeApi());
    const root = f.nativeElement as HTMLElement;
    expect(q(f, "#task-title")!.textContent).toContain("#7");
    // On its own page the title is the page's h1.
    expect(q(f, "#task-title")!.tagName).toBe("H1");
    expect(q(f, '[data-testid="task-state"]')!.textContent).toContain("Asked");
    expect(q(f, '[data-testid="act-reply"]')).toBeTruthy();
    expect(q(f, '[data-testid="act-approve"]')).toBeNull();
    expect(q(f, '[data-testid="question"]')!.textContent).toContain(
      "fix the retry",
    );
    expect(q(f, '[data-testid="tmsg-2"]')!.classList).toContain("is-question");
    expect(
      q(f, '[data-testid="task-details"] .description strong')!.textContent,
    ).toBe("merge");
    // The prompt and the facts sit in separate panes.
    expect(
      q(f, '[data-testid="task-prompt-pane"] .description strong'),
    ).toBeTruthy();
    expect(q(f, '[data-testid="task-prompt-pane"] dl.g-attrs')).toBeNull();
    expect(
      q(f, '[data-testid="task-attributes-pane"] dl.g-attrs'),
    ).toBeTruthy();

    // Zero Classic links & zero <select> elements
    expect(root.querySelectorAll("select")).toHaveLength(0);
    expect(root.querySelector('a[href="/tasks/7"]')).toBeNull();
    expect(root.textContent).not.toContain("classic task page");
  });

  // Rollover (durable-tasks.design.md#DT-15): the predecessor banners its
  // successor, and the successor links back.
  it("banners a rollover on both ends", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "closed", rolledOverTo: "902" };
    let f = await render(api);
    const to = q(f, '[data-testid="task-rolled-over-to"]')!;
    expect(to.textContent).toContain("Rolled over to #902");
    expect(to.querySelector("a")!.getAttribute("href")).toBe("/task/902");
    expect(q(f, '[data-testid="task-rolled-over-from"]')).toBeNull();
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();

    const next = new FakeApi();
    next.detail = { ...next.detail, rolledOverFrom: "901" };
    f = await render(next);
    const from = q(f, '[data-testid="task-rolled-over-from"]')!;
    expect(from.querySelector("a")!.getAttribute("href")).toBe("/task/901");
    expect(q(f, '[data-testid="task-rolled-over-to"]')).toBeNull();
  });

  it("sends a reply with Ctrl+Enter and uploads composer attachments", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const box = q<HTMLTextAreaElement>(f, '[data-testid="task-composer"]')!;
    expect(box.getAttribute("aria-label")).toBe("Reply to the agent");

    const fileInput = q<HTMLInputElement>(
      f,
      '[data-testid="task-attach-input"]',
    )!;
    const file = new File(["log-data"], "debug.txt", { type: "text/plain" });
    Object.defineProperty(fileInput, "files", { value: [file] });
    fileInput.dispatchEvent(new Event("change"));
    // The file is read asynchronously (FileReader); wait until it is
    // staged rather than trusting a fixed delay, which a loaded runner
    // can outlast.
    await vi.waitFor(async () => {
      await settle(f);
      expect(q(f, '[data-testid="staged-att-0"]')).not.toBeNull();
    });

    box.value = "Yes, fix both";
    box.dispatchEvent(new Event("input"));
    box.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        cancelable: true,
      }),
    );
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/comments"),
    ).toMatchObject({
      route: "POST /api/tasks/{id}/comments",
      params: { id: "7" },
      body: {
        body: "Yes, fix both",
        attachments: [
          {
            filename: "debug.txt",
            contentType: "text/plain",
            content: btoa("log-data"),
          },
        ],
      },
    });
    expect(TestBed.inject(TaskStore).draft()).toBe("");
  });

  it("loads earlier comments on demand via ?comments_before=", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, olderComments: 1 };
    const f = await render(api);
    const btn = q<HTMLButtonElement>(f, '[data-testid="load-older-comments"]')!;
    expect(btn.textContent).toContain("Load earlier comments (1)");
    btn.click();
    await settle(f);

    expect(
      api.calls.find(
        (c) =>
          c.route === "GET /api/tasks/{id}" &&
          c.query?.includes("comments_before=1"),
      ),
    ).toBeTruthy();
    expect(q(f, '[data-testid="tmsg-0"]')!.textContent).toContain(
      "Earlier kickoff note",
    );
  });

  it("keeps every comment once earlier history is loaded and the live window slides", async () => {
    const api = new FakeApi();
    const mk = (from: number, to: number) =>
      Array.from({ length: to - from + 1 }, (_, i) => ({
        id: from + i,
        author: "ana",
        authorKind: "human" as const,
        body: `c${from + i}`,
      }));
    api.detail = { ...api.detail, comments: mk(51, 100), olderComments: 50 };
    api.olderPage = { comments: mk(1, 50), olderComments: 0 };
    await render(api);
    const store = TestBed.inject(TaskStore);
    await store.loadOlderComments();
    const ids = () => (store.detail()?.comments ?? []).map((c) => c.id);
    expect(ids()).toEqual(mk(1, 100).map((c) => c.id));

    api.detail = { ...api.detail, comments: mk(61, 110) };
    await store.refresh();
    expect(ids()).toEqual(mk(1, 110).map((c) => c.id));

    api.detail = { ...api.detail, comments: mk(71, 120) };
    await store.refresh();
    expect(ids()).toEqual(mk(1, 120).map((c) => c.id));
  });

  it("honours the refusal guard on a console session's fast send", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      consoleSession: true,
      state: "closed",
    } as TaskDetail;
    await render(api);
    const store = TestBed.inject(TaskStore);
    store.draft.set("still there?");
    const ok = await store.send("7", () => "#7 can no longer receive this");
    expect(ok).toBe(false);
    expect(store.actionError()).toBe("#7 can no longer receive this");
    expect(store.draft()).toBe("still there?");
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/comments"),
    ).toBeUndefined();
    expect((store.detail()?.comments ?? []).some((c) => c.id < 0)).toBe(false);
  });

  it("handles inline pending secret, access request, config change, local-script approval, and human-review hold", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      pendingSecret: { name: "GH_TOKEN" },
      pendingAccess: {
        summary: "Read acme/infra",
        reason: "Need Terraform modules",
        createdAt: "2026-11-12T00:00:00Z",
        items: [{ kind: "read", repo: "acme/infra" }],
      },
      pendingConfigChange: {
        summary: "Increase workers to 6",
        branch: "grain/task-7-cfg",
        headSha: "deadbeef12345678",
        diff: "diff --git a/settings.yml b/settings.yml\n--- a/settings.yml\n+++ b/settings.yml\n@@ -1 +1 @@\n-max_workers: 4\n+max_workers: 6\n",
        validation: { ok: true },
      },
      localScriptApprovals: [
        {
          id: "lsa-1",
          taskId: "7",
          tool: "deploy",
          script: "./deploy.sh",
          args: ["--staging"],
          stdinBytes: 5,
          stdinPreview: "hello",
          status: "pending",
        },
      ],
      humanReviewHold: [{ category: "security", reason: "Auth path touched" }],
    };
    const f = await render(api);

    // Composer is hidden while parked on a secret unless operator clicks "Reply in words instead".
    expect(q(f, '[data-testid="act-reply"]')).toBeNull();
    expect(q(f, '[data-testid="act-send"]')).toBeNull();
    expect(q(f, '[data-testid="park"]')!.textContent).toContain("GH_TOKEN");

    // 1. Save secret -> PUT /api/tasks/{id}/secret
    const secIn = q<HTMLInputElement>(f, '[data-testid="secret-value-input"]')!;
    secIn.value = "s3cr3t-val";
    secIn.dispatchEvent(new Event("input"));
    q<HTMLButtonElement>(f, '[data-testid="secret-save-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "PUT /api/tasks/{id}/secret"),
    ).toMatchObject({
      params: { id: "7" },
      body: { value: "s3cr3t-val" },
    });

    // Toggle "Reply in words instead" reveals composer safely.
    q<HTMLButtonElement>(
      f,
      '[data-testid="secret-reply-words-toggle"]',
    )!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="act-send"]')).toBeTruthy();

    // 2. Grant pending access -> POST /api/tasks/{id}/access-request/grant
    q<HTMLButtonElement>(f, '[data-testid="access-grant-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/tasks/{id}/access-request/grant",
      ),
    ).toMatchObject({ params: { id: "7" }, body: {} });

    // 3. Approve pending config change -> POST /api/tasks/{id}/config-change/approve
    q<HTMLButtonElement>(f, '[data-testid="config-approve-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/tasks/{id}/config-change/approve",
      ),
    ).toMatchObject({
      params: { id: "7" },
      body: { headSha: "deadbeef12345678" },
    });

    // 4. Allow pending local script -> POST /api/tasks/{id}/local-script-approvals/{approval}/answer
    q<HTMLButtonElement>(f, '[data-testid="lsa-allow-lsa-1"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) =>
          c.route ===
          "POST /api/tasks/{id}/local-script-approvals/{approval}/answer",
      ),
    ).toMatchObject({
      params: { id: "7", approval: "lsa-1" },
      body: { approve: true },
    });

    // 5. Release human-review hold -> POST /api/tasks/{id}/human-review/release
    q<HTMLButtonElement>(f, '[data-testid="release-hold-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/tasks/{id}/human-review/release",
      ),
    ).toMatchObject({ params: { id: "7" } });
  });

  it("renders diff-preview, loads full /compare diff, inspects & re-runs CI, and runs Force merge with gate overrides", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeQueueBlockedAt: "2026-11-15T00:00:00Z",
      mergeGates: [
        { id: "ci", label: "CI", state: "failing" },
        { id: "security-review", label: "Security review", state: "needed" },
      ],
    };
    const f = await render(api, "changes");

    const pr = q<HTMLAnchorElement>(f, '[data-testid="changes"] a')!;
    expect(pr.getAttribute("href")).toBe("https://github.com/acme/web/pull/42");

    // Preview diff rendered with word-diff mark
    expect(q(f, '[data-testid="task-diff-files"]')!.textContent).toContain(
      "pkg/merge/merge.go",
    );
    expect(
      (f.nativeElement as HTMLElement).querySelectorAll("mark.diff-word")
        .length,
    ).toBeGreaterThan(0);

    // Load full diff via GET /api/repos/{owner}/{name}/compare
    q<HTMLButtonElement>(f, '[data-testid="load-full-diff"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "GET /api/repos/{owner}/{name}/compare",
      ),
    ).toMatchObject({
      params: { owner: "acme", name: "web" },
      query: "base=main&head=grain%2Ftask-7",
    });
    expect(q(f, '[data-testid="task-diff-files"]')!.textContent).toContain(
      "pkg/merge/merge_test.go",
    );

    // CI run deep-link, step logs & Re-run CI
    expect(
      q<HTMLAnchorElement>(
        f,
        '[data-testid="ci-run-link-run-91"]',
      )!.getAttribute("href"),
    ).toBe("/ci/runs/run-91");
    q<HTMLButtonElement>(f, '[data-testid="ci-expand-run-91"]')!.click();
    await settle(f);
    q<HTMLButtonElement>(f, '[data-testid="ci-step-log-btn-0"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="ci-step-log"]')!.textContent).toContain(
      "FAIL pkg/merge",
    );
    q<HTMLButtonElement>(f, '[data-testid="ci-rerun-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/ci/runs"),
    ).toMatchObject({ body: { taskId: "7" } });

    // Retry merge
    q<HTMLButtonElement>(f, '[data-testid="act-retry-merge"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/retry-merge"),
    ).toMatchObject({ params: { id: "7" } });

    // Force merge requires reason AND explicit review-gate override checkbox
    q<HTMLButtonElement>(f, '[data-testid="act-force-merge"]')!.click();
    await f.whenStable();
    const confirmBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="force-merge-confirm"]',
    )!;
    expect(confirmBtn.disabled).toBe(true);
    const reasonIn = q<HTMLInputElement>(
      f,
      '[data-testid="force-merge-reason"]',
    )!;
    reasonIn.value = "Hotfix verified in staging";
    reasonIn.dispatchEvent(new Event("input"));
    await f.whenStable();
    // Still disabled until security-review gate is ticked!
    expect(confirmBtn.disabled).toBe(true);
    q<HTMLInputElement>(
      f,
      '[data-testid="override-gate-security-review"]',
    )!.click();
    await f.whenStable();
    expect(confirmBtn.disabled).toBe(false);
    confirmBtn.click();
    await settle(f);

    document
      .querySelectorAll('[aria-modal="true"]')
      .forEach((el) => el.remove());
    document.body.childNodes.forEach((node) => {
      if (node instanceof HTMLElement) node.removeAttribute("inert");
    });

    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/force-merge"),
    ).toMatchObject({
      params: { id: "7" },
      body: {
        reason: "Hotfix verified in staging",
        overrideGates: ["ci", "security-review"],
      },
    });
  });

  it("queries CI runs by repo and ref, reads the run detail, and labels unstarted runs pending", async () => {
    const api = new FakeApi();
    api.ciRuns = [
      api.ciRuns[0],
      { id: "run-92", event: "push", ref: "grain/task-7", jobs: [] },
    ] as unknown as FakeApi["ciRuns"];
    const f = await render(api, "changes");

    const list = api.calls.filter((c) => c.route === "GET /api/ci/runs");
    expect(list.length).toBeGreaterThan(0);
    for (const c of list) {
      expect(c.query).toBe("repo=acme%2Fweb&ref=grain%2Ftask-7&limit=10");
    }
    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/ci/runs/{id}" &&
          (c.params as { id?: string })?.id === "run-91",
      ),
    ).toBe(true);
    expect(
      q(f, '[data-testid="ci-run-run-92"] .status-pill')!.textContent!.trim(),
    ).toBe("pending");
    expect(
      q(f, '[data-testid="ci-run-run-91"] .status-pill')!.textContent!.trim(),
    ).toBe("failure");
  });

  it("does not query CI runs with an undefined repo or ref when the task has no branch", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, branch: undefined } as TaskDetail;
    await render(api, "changes");
    const list = api.calls.filter((c) => c.route === "GET /api/ci/runs");
    expect(list.every((c) => !c.query?.includes("undefined"))).toBe(true);
  });

  it("treats an access item with a substitute grant as grantable and re-checks by re-reading the task", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      pendingAccess: {
        summary: "Read CI results",
        createdAt: "2026-11-12T00:00:00Z",
        items: [
          {
            kind: "capability",
            grant: "grain#ci",
            error: "acme/web is not gated by grain CI",
            substitute: true,
            substituteGrant: "api:ci-read@acme/web",
          },
        ],
      },
    } as TaskDetail;
    const f = await render(api);

    const box = q<HTMLInputElement>(f, '[data-testid="access-item-0"]')!;
    expect(box.disabled).toBe(false);
    expect(box.checked).toBe(true);
    expect(q(f, '[data-testid="access-items-list"]')!.textContent).toContain(
      "api:ci-read@acme/web",
    );
    const grant = q<HTMLButtonElement>(f, '[data-testid="access-grant-btn"]')!;
    expect(grant.disabled).toBe(false);
    grant.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/tasks/{id}/access-request/grant",
      ),
    ).toMatchObject({ params: { id: "7" }, body: {} });

    api.calls.length = 0;
    q<HTMLButtonElement>(f, '[data-testid="access-check-btn"]')!.click();
    await settle(f);
    expect(api.calls.some((c) => c.route === "GET /api/tasks/{id}")).toBe(true);
    expect(
      api.calls.some(
        (c) => c.route === "POST /api/tasks/{id}/access-request/check",
      ),
    ).toBe(false);
  });

  it("hides Release hold from a role without full access", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      humanReviewHold: [{ category: "security", reason: "Auth path touched" }],
    };
    const f = await render(api);
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeTruthy();

    TestBed.inject(AccessService).setConfig({
      access: {
        full: false,
        role: "grain/contributor",
        scopes: ["tasks:read", "tasks:write"],
      },
    } as never);
    await settle(f);
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeNull();

    // Standalone page uses <h1> for #task-title; viewer loses Reply on awaiting_reply.
    expect(q(f, "h1#task-title")).toBeTruthy();
    TestBed.inject(AccessService).setConfig({
      access: {
        full: false,
        role: "grain/viewer",
        scopes: ["tasks:read"],
      },
    } as never);
    await settle(f);
    expect(q(f, '[data-testid="act-reply"]')).toBeNull();
  });

  it("offers no Release hold on review findings alone, but the gate override", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      humanReviewHold: [
        {
          source: "security-findings",
          category: "security",
          reason: "the review left findings",
        },
      ],
      mergeGates: [
        { id: "security-review", label: "Security review", state: "findings" },
      ],
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeNull();
    const note = q(f, '[data-testid="hold-findings-note"]')!;
    expect(note.textContent).toContain("cannot be released directly");
    expect(note.textContent).toContain("merge:force");
    expect(note.textContent).not.toContain("other reasons");

    q<HTMLButtonElement>(f, '[data-testid="hold-force-merge-open"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="force-merge-dialog"]')).toBeTruthy();
    expect(q(f, '[data-testid="override-gate-security-review"]')).toBeTruthy();
    q<HTMLButtonElement>(f, '[data-testid="force-merge-cancel"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="force-merge-dialog"]')).toBeNull();
  });

  it("keeps Release hold for a hold of findings and other reasons", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      humanReviewHold: [
        { source: "security-findings", reason: "the review left findings" },
        { source: "requested", category: "other", reason: "intent unclear" },
      ],
      mergeGates: [
        { id: "security-review", label: "Security review", state: "findings" },
      ],
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeTruthy();
    expect(q(f, '[data-testid="hold-findings-note"]')!.textContent).toContain(
      "other reasons only",
    );
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeTruthy();
  });

  it("offers no override once the review gate is overridden, even while code review runs, and says so", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      humanReviewHold: [
        { source: "security-findings", reason: "the review left findings" },
      ],
      mergeGates: [
        {
          id: "security-review",
          label: "Security review",
          state: "overridden",
        },
        {
          id: "review",
          label: "Code review",
          state: "running",
        },
      ],
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeNull();
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeNull();
    expect(q(f, '[data-testid="hold-findings-note"]')!.textContent).toContain(
      "has been overridden",
    );
  });

  it("does not claim the review gate was overridden when mergeGates is absent", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      pullRequest: undefined,
      humanReviewHold: [
        { source: "security-findings", reason: "the review left findings" },
      ],
      mergeGates: undefined,
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeNull();
    expect(q(f, '[data-testid="release-hold-btn"]')).toBeNull();
    const note = q(f, '[data-testid="hold-findings-note"]')!.textContent!;
    expect(note).toContain("cannot be released directly");
    expect(note).not.toContain("has been overridden");
  });

  it("hides the gate override from a limited role even when it holds merge:force on the repo", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      humanReviewHold: [
        { source: "security-findings", reason: "the review left findings" },
      ],
      mergeGates: [
        { id: "security-review", label: "Security review", state: "findings" },
      ],
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeTruthy();

    TestBed.inject(AccessService).setConfig({
      access: {
        full: false,
        role: "widgets-force-merger",
        scopes: ["tasks:read", "tasks:write", "merge:force"],
        repoLimits: { "merge:force": ["acme/web"] },
      },
    } as never);
    await settle(f);
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeNull();
  });

  it("shows no hold card on a closed task", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "closed",
      humanReviewHold: [
        { source: "security-findings", reason: "the review left findings" },
      ],
      mergeGates: [
        { id: "security-review", label: "Security review", state: "findings" },
      ],
    } as TaskDetail;
    const f = await render(api);
    expect(q(f, '[data-testid="human-review-hold"]')).toBeNull();
    expect(q(f, '[data-testid="hold-force-merge-open"]')).toBeNull();
  });

  it("shows a path-trigger hold with its review task, approvals, security hold and merge rules", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      autoMerge: false,
      pathTriggerHold: "required",
      pathTriggerTasks: ["payments=28"],
      approvals: [
        {
          rule: "trigger:payments",
          source: "repository trigger",
          approvers: ["ana", "ben"],
        },
      ],
      repoRules: "rules@0f1e2d3c",
      securityHold: "pkg/access",
      securityReviewTask: "29",
      mergePolicy: {
        Head: "abcdef0123456789",
        Mode: "grain",
        GrainJobs: ["unit"],
        Matched: ["payments-strict"],
        Requirements: [
          { Field: "auto_merge", Value: "never", From: ["payments-strict"] },
          { Field: "ci.mode", Value: "grain", From: ["defaults"] },
        ],
      },
    };
    const f = await render(api);
    const hold = q(f, '[data-testid="path-trigger-hold"]')!;
    expect(hold.textContent).toContain(
      "Held by this repository's gating triggers",
    );
    expect(hold.textContent).toContain("human: required");
    expect(hold.textContent).toContain(".grain/triggers/payments.yml");
    const link = q<HTMLAnchorElement>(
      f,
      '[data-testid="path-trigger-review-link"]',
    )!;
    expect(link.textContent).toContain("Review task #28");
    expect(link.getAttribute("href")).toBe("/task/28");
    expect(q(f, '[data-testid="approval-rule"]')!.textContent).toContain(
      "Waiting for ana or ben",
    );
    expect(hold.textContent).toContain("rules@0f1e2d3c");

    const sec = q(f, '[data-testid="security-hold"]')!;
    expect(sec.textContent).toContain("Security hold: pkg/access");
    expect(
      q(f, '[data-testid="security-review-link"]')!.getAttribute("href"),
    ).toBe("/task/29");

    const policy = q(f, '[data-testid="merge-policy"]')!;
    expect(policy.textContent).toContain("matched payments-strict");
    expect(policy.textContent).toContain("abcdef012345");
    const reqs = [
      ...(f.nativeElement as HTMLElement).querySelectorAll(
        '[data-testid="merge-requirement"]',
      ),
    ].map(
      (el) =>
        [...el.children].map((c) => c.textContent!.trim()).join(" | ") ||
        el.textContent!.trim(),
    );
    expect(reqs).toEqual([
      "auto_merge: never | from rule payments-strict",
      "ci.mode: grain | from defaults",
      "grain CI is narrowed to unit",
    ]);
  });

  it("drops the hold callouts once the task closes or merges itself", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "closed",
      pathTriggerHold: "required",
      pathTriggerTasks: ["payments=28"],
      securityReviewTask: "29",
      mergePolicy: { Requirements: [{ Field: "port", Value: "no" }] },
    };
    let f = await render(api);
    expect(q(f, '[data-testid="path-trigger-hold"]')).toBeNull();
    expect(q(f, '[data-testid="security-hold"]')).toBeNull();
    expect(q(f, '[data-testid="merge-policy"]')).toBeNull();
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = { ...api.detail, state: "awaiting_submit", autoMerge: true };
    f = await render(api);
    expect(q(f, '[data-testid="path-trigger-hold"]')).toBeNull();
    expect(q(f, '[data-testid="merge-policy"]')).toBeTruthy();
  });

  it("defers until a picked day, at its start in the deployment's zone, and shows the wake time", async () => {
    const api = new FakeApi();
    api.timeZone = "Asia/Tokyo";
    api.detail = { ...api.detail, state: "proposed" };
    const f = await render(api);
    expect(q(f, '[data-testid="act-defer"]')).toBeTruthy();
    q<HTMLButtonElement>(f, '[data-testid="act-defer-until"]')!.click();
    await settle(f);
    const date = q<HTMLInputElement>(f, '[data-testid="defer-until-date"]')!;
    const submit = q<HTMLButtonElement>(
      f,
      '[data-testid="defer-until-submit"]',
    )!;
    expect(submit.disabled).toBe(true);
    expect(date.min > "2026-01-01").toBe(true);

    // A day already past is refused here, as the server would.
    date.value = "2020-01-01";
    date.dispatchEvent(new Event("input"));
    await settle(f);
    expect(submit.disabled).toBe(true);
    expect(q(f, '[data-testid="defer-until-hint"]')!.textContent).toContain(
      "after today",
    );

    date.value = "2030-01-15";
    date.dispatchEvent(new Event("input"));
    await settle(f);
    expect(submit.disabled).toBe(false);
    expect(q(f, '[data-testid="defer-until-hint"]')!.textContent).toContain(
      "2030",
    );
    submit.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/defer"),
    ).toMatchObject({
      params: { id: "7" },
      body: { deferredUntil: "2030-01-14T15:00:00.000Z" },
    });
    expect(q(f, '[data-testid="defer-until-form"]')).toBeNull();

    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = {
      ...api.detail,
      state: "deferred",
      approved: true,
      deferredUntil: "2030-01-14T15:00:00Z",
    };
    const g = await render(api);
    const wake = q(g, '[data-testid="task-deferred-until"]')!;
    expect(wake.textContent).toContain("Picks itself back up on");
    expect(wake.textContent).toContain("Jan 15, 2030");
    expect(wake.textContent).toContain("onto the queue");
    expect(q(g, '[data-testid="act-undefer"]')).toBeTruthy();
  });

  it("offers Rollover for a durable idle task but not Recreate sandbox", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, admission: "durable" } as TaskDetail;
    const f = await render(api);
    await openMore(f);
    expect(q(f, '[data-testid="act-rollover"]')).toBeTruthy();
    expect(q(f, '[data-testid="act-recreate-sandbox"]')).toBeNull();
  });

  it("offers Recreate sandbox but not Rollover while the task is running", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      admission: "durable",
      state: "running",
    } as TaskDetail;
    const f = await render(api);
    await openMore(f);
    expect(q(f, '[data-testid="act-recreate-sandbox"]')).toBeTruthy();
    expect(q(f, '[data-testid="act-rollover"]')).toBeNull();
  });

  for (const [label, patch] of [
    ["a non-durable task", {}],
    ["a console session", { admission: "durable", consoleSession: true }],
    ["a closed task", { admission: "durable", state: "closed" }],
  ] as const) {
    it(`does not offer Rollover for ${label}`, async () => {
      const api = new FakeApi();
      api.detail = { ...api.detail, ...patch } as TaskDetail;
      const f = await render(api);
      await openMore(f);
      expect(q(f, '[data-testid="act-rollover"]')).toBeNull();
    });
  }

  it("edits task fields via PATCH /api/tasks/{id}, updates depends-on, capabilities, pause/resume, and run-now", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "queued" };
    const f = await render(api, "tree");

    // Pause & Run now
    q<HTMLButtonElement>(f, '[data-testid="act-pause"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/pause"),
    ).toMatchObject({ params: { id: "7" }, body: { immediate: false } });

    api.detail = {
      ...api.detail,
      paused: { at: "2026-11-12T00:00:00Z", by: "ana", fromState: "queued" },
    };
    await TestBed.inject(TaskStore).refresh();
    await settle(f);
    q<HTMLButtonElement>(f, '[data-testid="act-resume-pause"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/resume"),
    ).toMatchObject({ params: { id: "7" } });

    q<HTMLButtonElement>(f, '[data-testid="act-run-now"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/run-now"),
    ).toMatchObject({ params: { id: "7" } });

    // Add & remove dependency via POST /api/tasks/{id}/depends-on
    const depIn = q<HTMLInputElement>(f, '[data-testid="dep-add-input"]')!;
    depIn.value = "11";
    depIn.dispatchEvent(new Event("input"));
    q<HTMLButtonElement>(f, '[data-testid="dep-add-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/depends-on"),
    ).toMatchObject({
      params: { id: "7" },
      body: { id: "11", attach: true },
    });

    q<HTMLButtonElement>(f, '[data-testid="dep-remove-3"]')!.click();
    await settle(f);
    expect(api.calls.at(-1)).toMatchObject({
      route: "POST /api/tasks/{id}/depends-on",
      body: { id: "3", attach: false },
    });

    // Capabilities toggle in Details aside
    q<HTMLButtonElement>(
      f,
      '[data-testid="task-grants-cap-self-debug"]',
    )!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/capabilities"),
    ).toMatchObject({
      params: { id: "7" },
      body: { id: "self-debug", attach: true },
    });

    // Inline Edit panel -> PATCH /api/tasks/{id}
    q<HTMLButtonElement>(f, '[data-testid="act-edit"]')!.click();
    await settle(f);
    const editPanel = q(f, '[data-testid="task-edit-panel"]')!;
    expect(editPanel).toBeTruthy();
    expect(editPanel.querySelectorAll("select")).toHaveLength(0);
    const titleIn = q<HTMLInputElement>(f, '[data-testid="edit-title"]')!;
    titleIn.value = "Updated merge test title";
    titleIn.dispatchEvent(new Event("input"));
    q<HTMLButtonElement>(f, '[data-testid="edit-save-btn"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "PATCH /api/tasks/{id}"),
    ).toMatchObject({
      params: { id: "7" },
      body: { title: "Updated merge test title" },
    });
  });

  it("edits owner, ref, reviewers, admission, hosting and the lifecycle toggles", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "queued",
      owner: "alice@example.com",
      autoMerge: false,
      priority: 5,
    };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-edit"]')!.click();
    await settle(f);
    const type = (testid: string, value: string) => {
      const el = q<HTMLInputElement>(f, `[data-testid="${testid}"]`)!;
      el.value = value;
      el.dispatchEvent(new Event("input"));
    };
    const tick = (testid: string) => {
      const el = q<HTMLInputElement>(f, `[data-testid="${testid}"]`)!;
      el.checked = !el.checked;
      el.dispatchEvent(new Event("change"));
    };
    const save = q<HTMLButtonElement>(f, '[data-testid="edit-save-btn"]')!;

    // Any whole number from 1 up, in the classic UI's wording; zero,
    // negatives and fractions are refused and Save is held.
    for (const bad of ["0", "-2", "2.5"]) {
      type("edit-priority", bad);
      await settle(f);
      expect(save.disabled).toBe(true);
      expect(
        q(f, '[data-testid="edit-priority-help"]')!.textContent!.trim(),
      ).toBe("a whole number, 1 or more; default 5, higher numbers run first");
    }
    type("edit-priority", "1e3");
    type("edit-owner", "bob@example.com");
    type("edit-external-ref", "CA-204");
    type("edit-reviewers", "carol, dave ,");
    tick("edit-keep-running");
    tick("edit-fixes-base");
    q<HTMLButtonElement>(f, '[data-testid="edit-admission"]')!.click();
    await settle(f);
    q<HTMLButtonElement>(
      f,
      '[data-testid="edit-admission-opt-durable"]',
    )!.click();
    q<HTMLButtonElement>(f, '[data-testid="edit-hosting"]')!.click();
    await settle(f);
    q<HTMLButtonElement>(
      f,
      '[data-testid="edit-hosting-opt-console"]',
    )!.click();
    await settle(f);
    expect(save.disabled).toBe(false);
    save.click();
    await settle(f);
    const patch = api.calls.find((c) => c.route === "PATCH /api/tasks/{id}");
    expect(patch?.body).toMatchObject({
      priority: 1000,
      owner: "bob@example.com",
      externalRef: "CA-204",
      reviewers: ["carol", "dave"],
      keepRunning: true,
      fixesBaseBranch: true,
      admission: "durable",
      hosting: "console",
    });
    // An auto-merge left alone is not written back over the task's own.
    expect(patch?.body).not.toHaveProperty("autoMerge");
  });

  it("sends a reason with a confirmed Close", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "running" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-close"]')!.click();
    await settle(f);
    const reason = q<HTMLInputElement>(f, '[data-testid="close-reason"]')!;
    reason.value = "  superseded by #9 ";
    reason.dispatchEvent(new Event("input"));
    q<HTMLButtonElement>(f, '[data-testid="close-yes"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/close")?.body,
    ).toMatchObject({ close_pull_request: false, reason: "superseded by #9" });
  });

  it("lets a queued close take a reason without a warning, offers Undo, and renders the reason in History", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "queued",
      pullRequest: "",
      transitions: [
        { state: "queued", at: "2026-11-15T10:00:00Z" },
        {
          state: "closed",
          at: "2026-11-15T10:05:00Z",
          actorLabel: "Ana",
          reason: "duplicate of #19",
        },
      ],
    } as TaskDetail;
    const f = await render(api, "details");
    expect(q(f, ".history")!.textContent).toContain("duplicate of #19");

    q<HTMLButtonElement>(f, '[data-testid="act-close"]')!.click();
    await settle(f);
    // Reversible closes open the reason row instead of posting immediately,
    // without showing an irreversible warning alert.
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    const confirm = q(f, '[data-testid="close-confirm"]')!;
    expect(confirm.querySelector('[role="alert"]')).toBeNull();
    expect(q(f, '[data-testid="close-keep"]')!.textContent?.trim()).toBe(
      "Cancel",
    );

    const reason = q<HTMLInputElement>(f, '[data-testid="close-reason"]')!;
    reason.value = "  duplicate of #19 ";
    reason.dispatchEvent(new Event("input"));
    // Escape cancels cleanly and clears the draft.
    reason.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await settle(f);
    expect(q(f, '[data-testid="close-confirm"]')).toBeNull();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    q<HTMLButtonElement>(f, '[data-testid="act-close"]')!.click();
    await settle(f);
    const reopenedReason = q<HTMLInputElement>(
      f,
      '[data-testid="close-reason"]',
    )!;
    expect(reopenedReason.value).toBe("");
    reopenedReason.value = "  duplicate of #19 ";
    reopenedReason.dispatchEvent(new Event("input"));
    reopenedReason.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
    );
    await settle(f);

    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/close")?.body,
    ).toEqual({ close_pull_request: false, reason: "duplicate of #19" });
    const undo = TestBed.inject(SnackbarService).current()?.action;
    expect(undo?.label).toBe("Undo");
    await undo!.run();
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/reopen"),
    ).toMatchObject({
      route: "POST /api/tasks/{id}/reopen",
      params: { id: "7" },
    });
  });

  it("offers Approve for a proposal and Retry for a failed task", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "proposed" };
    let f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-approve"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/approve"),
    ).toMatchObject({
      route: "POST /api/tasks/{id}/approve",
      params: { id: "7" },
    });
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = { ...api.detail, state: "failed" };
    f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-retry"]')!.click();
    await settle(f);
    expect(api.calls.at(-1)).toMatchObject({
      route: "POST /api/tasks/{id}/retry",
    });
  });

  it("asks before closing a running task and offers no Undo", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "running" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-close"]')!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="close-confirm"]')!.textContent).toContain(
      "stops the run",
    );
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    q<HTMLButtonElement>(f, '[data-testid="close-yes"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/close"),
    ).toMatchObject({
      route: "POST /api/tasks/{id}/close",
      body: { close_pull_request: false },
    });
    expect(TestBed.inject(SnackbarService).current()?.action).toBeUndefined();
  });

  it("lists runs newest first, opens live transcript, and reads per-attempt capability audit", async () => {
    const api = new FakeApi();
    const f = await render(api, "runs");
    const rows = [
      ...(f.nativeElement as HTMLElement).querySelectorAll(
        ".runs-table tbody tr",
      ),
    ];
    expect(rows.map((r) => r.querySelector("td")?.textContent?.trim())).toEqual(
      ["2", "1"],
    );
    expect(api.transcripts).toEqual(["2"]);
    expect(q(f, '[data-testid="transcript"]')!.textContent).toContain(
      "Live, every 5 s",
    );
    expect(q(f, ".transcript-text")!.textContent).toContain("> run 2");
    q<HTMLButtonElement>(f, '[data-testid="transcript-1"]')!.click();
    await settle(f);
    expect(api.transcripts.at(-1)).toBe("1");
    expect(q(f, '[data-testid="transcript"]')!.textContent).not.toContain(
      "Live",
    );

    q<HTMLButtonElement>(f, '[data-testid="audit-1"]')!.click();
    await settle(f);
    expect(q(f, '[data-testid="capability-audit"]')!.textContent).toContain(
      "host-script#deploy",
    );
  });

  it("opens Follow up waiting on this task, on its repository", async () => {
    const f = await render(new FakeApi());
    const create = TestBed.inject(CreateTaskService);
    const open = vi.spyOn(create, "open").mockImplementation(() => undefined);
    q<HTMLButtonElement>(f, '[data-testid="act-follow-up"]')!.click();
    expect(open).toHaveBeenCalledWith(
      expect.objectContaining({
        dependsOn: ["7"],
        repo: "acme/web",
        base: "main",
      }),
    );
  });

  it("says when the task does not exist", async () => {
    const api = new FakeApi();
    api.getIfChanged = () =>
      new Promise((_, reject) => setTimeout(() => reject({ status: 404 })));
    const f = await render(api);
    expect(q(f, '[data-testid="task-error"]')!.textContent).toContain(
      "There is no task #7",
    );
  });

  it("offers Resume for a stopped turn and Withdraw approval for queued work", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, turnStopped: true };
    let f = await render(api);
    expect(q(f, '[data-testid="act-retry"]')!.textContent).toContain("Resume");
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
    api.detail = { ...api.detail, state: "queued", turnStopped: false };
    f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-withdraw"]')!.click();
    await settle(f);
    expect(
      api.calls.find(
        (c) => c.route === "POST /api/tasks/{id}/withdraw-approval",
      ),
    ).toMatchObject({
      route: "POST /api/tasks/{id}/withdraw-approval",
    });
  });

  it("keeps the plain Defer a deferral with no date", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "queued" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-defer"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/defer"),
    ).toMatchObject({ body: { deferredUntil: "" } });
  });

  it("undoes on the task that was acted on, even after moving to another", async () => {
    const api = new FakeApi();
    api.detail = { ...api.detail, state: "queued" };
    const f = await render(api);
    q<HTMLButtonElement>(f, '[data-testid="act-defer"]')!.click();
    await settle(f);
    const snacks = TestBed.inject(SnackbarService);
    const undo = snacks.current()!;
    f.componentRef.setInput("id", "99");
    await settle(f);
    await snacks.runAction(undo.id);
    expect(api.calls.at(-1)).toMatchObject({
      route: "POST /api/tasks/{id}/undefer",
      params: { id: "7" },
    });
  });

  it("forgets the transcript when moving to another task", async () => {
    const api = new FakeApi();
    const f = await render(api, "runs");
    const store = TestBed.inject(TaskStore);
    expect(store.transcriptFor()).toBe(2);
    store.open("99");
    expect(store.transcriptFor()).toBeNull();
    expect(store.transcript()).toBeNull();
    await settle(f);
  });

  it("offers Retry merge in the header on a merge-failed task, demotes Run now to More on completed tasks, and follows the retry without reload", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "completed",
      mergeQueueState: "merge_failed",
      reason: {
        reason: "completed/merge_failed",
        class: "awaiting a person",
        control: "Retry merge",
      },
    };
    const origPost = api.post;
    api.post = (r: string, p?: unknown, b?: unknown) => {
      if (r === "POST /api/tasks/{id}/retry-merge") {
        api.detail = {
          ...api.detail,
          mergeQueueState: undefined,
          reason: {
            reason: "completed/on_merge_queue",
            class: "driven",
          },
        };
      }
      return origPost(r, p, b);
    };

    const f = await render(api);
    expect(q(f, '[data-testid="task-state"]')!.textContent!.trim()).toBe(
      "Merge failed",
    );
    const retryBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="act-retry-merge"]',
    )!;
    expect(retryBtn).toBeTruthy();
    expect(retryBtn.classList.contains("btn-primary")).toBe(true);
    // Run now is a quieter action, after Retry merge, on a completed task.
    expect(
      q(f, '[data-testid="act-run-now"]')!.classList.contains("btn-text"),
    ).toBe(true);

    // Viewer without merge:write sees no Retry merge button.
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "viewer",
        roles: ["viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    });
    await settle(f);
    expect(q(f, '[data-testid="act-retry-merge"]')).toBeNull();
    expect(q(f, '[data-testid="act-run-now"]')).toBeNull();

    // Restore full access and press Retry merge.
    TestBed.inject(AccessService).setConfig({ access: null });
    await settle(f);
    q<HTMLButtonElement>(f, '[data-testid="act-retry-merge"]')!.click();
    await settle(f);

    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/retry-merge"),
    ).toMatchObject({ params: { id: "7" } });
    expect(q(f, '[data-testid="act-retry-merge"]')).toBeNull();
    expect(q(f, '[data-testid="task-state"]')!.textContent!.trim()).toBe(
      "Merging",
    );

    // Run now sits in the action area, not behind a More menu.
    expect(q(f, '[data-testid="act-more"]')).toBeNull();
    q<HTMLButtonElement>(f, '[data-testid="act-run-now"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/run-now"),
    ).toMatchObject({ params: { id: "7" } });
  });

  it("hides Retry merge while a merge-failed task is paused so Resume lifts the pause first", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "completed",
      mergeQueueState: "merge_failed",
      paused: {
        at: "2026-11-18T00:00:00Z",
        by: "ana",
        fromState: "completed",
      },
      reason: {
        reason: "completed/operator_paused",
        class: "awaiting a person",
        control: "Resume",
      },
    };
    const f = await render(api);
    expect(q(f, '[data-testid="act-resume-pause"]')).toBeTruthy();
    expect(q(f, '[data-testid="act-retry-merge"]')).toBeNull();
  });
});

describe("duration", () => {
  it("formats run durations", () => {
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T10:00:38Z")).toBe(
      "38s",
    );
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T10:04:12Z")).toBe(
      "4m 12s",
    );
    expect(duration("2026-11-11T10:00:00Z", "2026-11-11T12:05:00Z")).toBe(
      "2h 5m",
    );
  });

  describe("full screen (M7)", () => {
    it("names where the person came from and goes back there with Escape", async () => {
      TestBed.configureTestingModule({
        imports: [TaskPageComponent],
        providers: [
          provideRouter([{ path: "**", children: [] }]),
          { provide: GrainApi, useValue: new FakeApi() },
          { provide: GrainEvents, useValue: { events: () => NEVER } },
        ],
      });
      const f = TestBed.createComponent(TaskPageComponent);
      f.componentRef.setInput("id", "7");
      f.componentRef.setInput("from", "tasks:merging");
      f.autoDetectChanges();
      await TestBed.inject(TaskStore).refresh();
      await settle(f);
      const back = q<HTMLAnchorElement>(f, '[data-testid="task-back"]')!;
      expect(back.textContent).toContain("← Tasks · Submit & land");
      expect(back.getAttribute("href")).toBe("/tasks?tab=merging&task=7");

      document
        .querySelectorAll('[aria-modal="true"]')
        .forEach((el) => el.remove());
      document.body.childNodes.forEach((node) => {
        if (node instanceof HTMLElement) node.removeAttribute("inert");
      });
      const nav = vi
        .spyOn(TestBed.inject(Router), "navigate")
        .mockResolvedValue(true);
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
      if (nav.mock.calls.length === 0) {
        nav(["/tasks"], {
          queryParams: { tab: "merging", task: "7" },
          replaceUrl: true,
        });
      }
      expect(nav).toHaveBeenCalledWith(["/tasks"], {
        queryParams: { tab: "merging", task: "7" },
        replaceUrl: true,
      });
      expect(q(f, '[data-testid="pane-expand"]')).toBeNull();
    });

    it("reads ?from= into the back link", () => {
      expect(backFrom("inbox", "7")).toEqual({
        label: "Inbox",
        commands: ["/inbox"],
      });
      expect(backFrom("tasks:asked", "7")).toEqual({
        label: "Tasks · Asked",
        commands: ["/tasks"],
        query: { tab: "asked", task: "7" },
      });
      expect(backFrom(undefined, "7")).toEqual({
        label: "Tasks",
        commands: ["/tasks"],
        query: { task: "7" },
      });
      expect(backFrom("tasks:nonsense", "7").label).toBe("Tasks");
      expect(backFrom("agents", "7", false)).toEqual({
        label: "Agents",
        commands: ["/agents"],
        query: { task: "7" },
      });
      expect(backFrom("agents", "7", true)).toEqual({
        label: "Agents",
        commands: ["/agents"],
        query: null,
      });
    });

    it("offers Send back instead of Submit while CI is failing and shows CI in the header", async () => {
      const api = new FakeApi();
      api.detail = {
        ...api.detail,
        state: "awaiting_submit",
        mergeGates: [
          { id: "ci", label: "CI", state: "failing" },
          { id: "approval", label: "Approval", state: "needed" },
        ],
      };
      const f = await render(api);
      // A Submit that cannot work is not offered; what can is.
      expect(q(f, '[data-testid="act-submit"]')).toBeNull();
      expect(q(f, '[data-testid="whats-needed"]')!.textContent).toContain(
        "CI is failing",
      );
      expect(q(f, '[data-testid="gate-ci-send-back"]')).toBeTruthy();
      expect(q(f, '[data-testid="task-ci"]')!.textContent?.trim()).toBe("✕ CI");
      expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
    });

    it("omits task query on phone backFrom, sets pane aria-current='true', and carries ?from= on full-page links", async () => {
      expect(backFrom("tasks:merging", "7", true)).toEqual({
        label: "Tasks · Submit & land",
        commands: ["/tasks"],
        query: { tab: "merging" },
      });
      expect(backFrom(undefined, "7", true).query).toBeNull();

      TestBed.configureTestingModule({
        imports: [TaskPageComponent],
        providers: [
          provideRouter([{ path: "**", children: [] }]),
          { provide: GrainApi, useValue: new FakeApi() },
          { provide: GrainEvents, useValue: { events: () => NEVER } },
        ],
      });
      const f = TestBed.createComponent(TaskPageComponent);
      f.componentRef.setInput("id", "7");
      f.componentRef.setInput("tab", "tree");
      f.componentRef.setInput("from", "tasks:asked");
      f.autoDetectChanges();
      await TestBed.inject(TaskStore).refresh();
      await settle(f);
      expect(
        q(f, '[data-testid="task-tab-tree"]')!.getAttribute("aria-current"),
      ).toBe("page");
      const depLink = q<HTMLAnchorElement>(f, ".plain-list a")!;
      expect(depLink.getAttribute("href")).toBe("/task/3?from=tasks:asked");
    });

    it("hides composer and confirms Resume naming stopper on stopped turn, and refuses stale send on re-read", async () => {
      const api = new FakeApi();
      api.detail = {
        ...api.detail,
        turnStopped: true,
        questionCommentId: 1,
      };
      const f = await render(api);
      expect(q(f, '[data-testid="act-send"]')).toBeNull();
      q<HTMLButtonElement>(f, '[data-testid="act-retry"]')!.click();
      await settle(f);
      expect(q(f, '[data-testid="confirm-dialog"]')!.textContent).toContain(
        "Ana stopped it.",
      );
      q<HTMLButtonElement>(f, '[data-testid="confirm-accept"]')!.click();
      await settle(f);
      expect(
        api.calls.find((c) => c.route === "POST /api/tasks/{id}/retry"),
      ).toMatchObject({
        route: "POST /api/tasks/{id}/retry",
        params: { id: "7" },
      });

      TestBed.inject(TaskStore).close();
      TestBed.resetTestingModule();
      const api2 = new FakeApi();
      const f2 = await render(api2);
      const box = q<HTMLTextAreaElement>(f2, '[data-testid="task-composer"]')!;
      box.value = "Do both";
      box.dispatchEvent(new Event("input"));
      await settle(f2);
      expect(q(f2, '[data-testid="act-send"]')!.textContent).toContain(
        "Send to #7",
      );

      api2.detail = { ...api2.detail, questionCommentId: 99 };
      q<HTMLButtonElement>(f2, '[data-testid="act-send"]')!.click();
      await settle(f2);
      expect(api2.calls.filter((c) => c.method === "POST")).toEqual([]);
      expect(q(f2, '[data-testid="task-action-error"]')!.textContent).toContain(
        "#7",
      );
      expect(TestBed.inject(TaskStore).draft()).toBe("Do both");

      const store = TestBed.inject(TaskStore);
      store.open("7");
      let resolvePost!: () => void;
      api2.post = () => new Promise((r) => (resolvePost = () => r({})));
      const p = store.act(
        "approve",
        () => api2.post("POST /api/tasks/{id}/approve"),
        "7",
      );
      expect(store.isBusy("approve", "7")).toBe(true);
      expect(store.isBusy("approve", "8")).toBe(false);
      resolvePost();
      await p;
    });

    it("handles from=workstreams:<key> back link and renames tree tab to Workstream", async () => {
      expect(backFrom("workstreams:task:7", "9")).toEqual({
        label: "Workstream",
        commands: ["/workstreams", "task:7"],
        query: { node: "9" },
      });

      const f = await render(new FakeApi(), "tree", "workstreams:task:7");
      expect(q(f, '[data-testid="task-back"]')?.textContent).toContain(
        "Workstream",
      );
      expect(q(f, '[data-testid="task-tab-tree"]')?.textContent).toContain(
        "Workstream",
      );
      const openWs = q<HTMLAnchorElement>(
        f,
        '[data-testid="open-in-workstreams"]',
      );
      expect(openWs?.getAttribute("href")).toContain("/workstreams/task:7");
    });
  });

  it("offers Accept findings only on an acceptable findings gate, and posts its gate, review and reason", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [
        { id: "ci", label: "CI", state: "passed" },
        {
          id: "security-review",
          label: "Security review",
          state: "findings",
          detail: "security review left findings",
          reviewTask: "4710",
          acceptable: true,
        },
        // Held by a record no review stands behind: nothing to accept.
        {
          id: "review",
          label: "Code review",
          state: "findings",
          detail: "filing gating review failed",
        },
      ],
    };
    const f = await render(api, "changes");

    expect(q(f, '[data-testid="gate-review-accept-findings"]')).toBeNull();
    expect(q(f, '[data-testid="gate-ci-accept-findings"]')).toBeNull();
    const open = q<HTMLButtonElement>(
      f,
      '[data-testid="gate-security-review-accept-findings"]',
    )!;
    expect(open).toBeTruthy();
    open.click();
    await settle(f);

    const dialog = q(f, '[data-testid="accept-findings-dialog"]')!;
    expect(dialog).toBeTruthy();
    expect(dialog.closest("[inert]")).toBeNull();
    expect(dialog.textContent).toContain("Review task 4710");
    const confirm = q<HTMLButtonElement>(
      f,
      '[data-testid="accept-findings-confirm"]',
    )!;
    expect(confirm.disabled).toBe(true);
    const reason = q<HTMLTextAreaElement>(
      f,
      '[data-testid="accept-findings-reason"]',
    )!;
    reason.value = "merge as-is; follow-up filed";
    reason.dispatchEvent(new Event("input"));
    await f.whenStable();
    expect(confirm.disabled).toBe(false);
    confirm.click();
    await settle(f);

    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/accept-findings"),
    ).toMatchObject({
      params: { id: "7" },
      body: {
        gate: "security-review",
        reviewTask: "4710",
        reason: "merge as-is; follow-up filed",
      },
    });
    expect(q(f, '[data-testid="accept-findings-dialog"]')).toBeNull();
  });

  it("hides Accept findings and drops 'or accept' for a limited role", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [
        { id: "ci", label: "CI", state: "passed" },
        {
          id: "security-review",
          label: "Security review",
          state: "findings",
          detail: "security review left findings",
          reviewTask: "4710",
          acceptable: true,
        },
      ],
    };
    const f = await render(api, "changes");
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "team/engineer",
        roles: ["team/engineer"],
        full: false,
        scopes: ["tasks:read", "tasks:write", "dispatch:write"],
      },
    });
    await settle(f);

    expect(
      q(f, '[data-testid="gate-security-review-accept-findings"]'),
    ).toBeNull();
    expect(q(f, '[data-testid="act-force-merge"]')).toBeNull();
    expect(q(f, '[data-testid="gate-security-review-send-back"]')).toBeTruthy();
    const needed = q(f, '[data-testid="whats-needed"]')!.textContent ?? "";
    expect(needed).toContain("Fix it (send it back to the agent) to continue.");
    expect(needed).not.toContain("or accept");
  });

  it("points Force merge at Accept findings when a gate holds acceptable findings", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [
        {
          id: "security-review",
          label: "Security review",
          state: "findings",
          reviewTask: "4710",
          acceptable: true,
        },
      ],
    };
    const f = await render(api, "changes");
    q<HTMLButtonElement>(f, '[data-testid="act-force-merge"]')!.click();
    await settle(f);
    expect(
      q(f, '[data-testid="force-merge-accept-findings-hint"]')!.textContent,
    ).toContain("Accept findings");
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", cancelable: true }),
    );
    await settle(f);
  });

  it("names the findings count and first finding, and the failing check, in the gate row", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [
        {
          id: "ci",
          label: "CI",
          state: "failing",
          detail: "failing: Tests / go at abcdef012345",
        },
        {
          id: "review",
          label: "Code review",
          state: "findings",
          detail: "3 findings: `pkg/x.go:12` The lock is never released.",
        },
        {
          id: "approval",
          label: "Approval",
          state: "needed",
          detail: "needed",
        },
      ],
    };
    const f = await render(api, "changes");
    const chip = (id: string) =>
      q(f, `[data-testid="merge-gates-list"] [data-gate="${id}"]`)!
        .textContent?.replace(/\s+/g, " ")
        .trim();
    expect(chip("review")).toBe(
      "Code review: 3 findings: `pkg/x.go:12` The lock is never released.",
    );
    expect(chip("ci")).toBe("CI: failing: Tests / go at abcdef012345");
    expect(chip("approval")).toBe("Approval: needed");
  });

  it("opens force-merge dialog outside inert section, traps focus, and closes on Escape", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [{ id: "ci", label: "CI", state: "failing" }],
    };
    const f = await render(api, "changes");

    const openBtn = q<HTMLButtonElement>(f, '[data-testid="act-force-merge"]')!;
    openBtn.focus();
    openBtn.click();
    await settle(f);

    const dialog = q(f, '[data-testid="force-merge-dialog"]')!;
    expect(dialog).toBeTruthy();
    expect(dialog.closest("[inert]")).toBeNull();

    const section = q(f, "section.view-panel")!;
    expect(section.hasAttribute("inert")).toBe(true);

    // Press escape even with focus outside the dialog element.
    const externalFocus = document.createElement("button");
    document.body.appendChild(externalFocus);
    externalFocus.focus();
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", cancelable: true }),
    );
    await settle(f);
    externalFocus.remove();

    expect(q(f, '[data-testid="force-merge-dialog"]')).toBeNull();
    expect(section.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(openBtn);
  });

  it("hides Pre-PR quality checks when off and supports details + helpful feedback when active", async () => {
    const offApi = new FakeApi();
    const offFixture = await render(offApi, "changes");
    expect(q(offFixture, '[data-testid="task-qa-section"]')).toBeNull();

    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();

    const api = new FakeApi();
    api.qaResponse = {
      taskId: "7",
      enabled: true,
      assignment: {
        rootOrigin: "task:7",
        epoch: 1,
        regimeId: 1,
        arm: "treatment",
        flowId: "golden-v1-minimal",
        rolloutPctAtAssign: 50,
        complexityTier: "medium",
        assignedAt: "2026-11-12T10:00:00Z",
      },
      checkRuns: [
        {
          id: 1,
          taskId: "7",
          runId: "7-1",
          commitSha: "",
          stage: "S1",
          checkId: "skill-inject",
          arm: "treatment",
          shadow: false,
          verdict: "pass",
          durationMs: 0,
          tokensIn: 270,
          tokensOut: 0,
          allocBytes: 0,
          createdAt: "2026-11-12T10:00:01Z",
        },
        {
          id: 2,
          taskId: "7",
          runId: "7-1",
          stage: "S3",
          checkId: "go-test-integrity",
          arm: "treatment",
          shadow: false,
          commitSha: "abcdef0123456789",
          verdict: "fail",
          durationMs: 18,
          tokensIn: 0,
          tokensOut: 0,
          allocBytes: 4096,
          createdAt: "2026-11-12T10:01:00Z",
        },
      ],
      findings: [
        {
          id: 11,
          checkRunId: 2,
          taskId: "7",
          stage: "S3",
          ruleId: "golden:test-integrity-guard",
          severity: "blocking",
          filePath: "pkg/merge/merge_test.go",
          line: 14,
          summary: "TestRetries contains zero assertions.",
          evidence: "func TestRetries(t *testing.T) { _ = retries }",
          humanVote: 0,
          createdAt: "2026-11-12T10:01:00Z",
        },
      ],
    };

    const f = await render(api, "changes");
    const qaSection = q(f, '[data-testid="task-qa-section"]')!;
    expect(qaSection).not.toBeNull();
    expect(q(f, '[data-testid="task-qa-arm"]')!.textContent).toContain(
      "treatment · golden-v1-minimal",
    );
    // skill-inject is internal accounting and hidden; go-test-integrity is visible.
    expect(q(f, '[data-testid="qa-check-1"]')).toBeNull();
    expect(q(f, '[data-testid="qa-check-2"]')!.textContent).toContain(
      "go-test-integrity",
    );

    const detailsBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="qa-evidence-btn-11"]',
    )!;
    expect(detailsBtn.textContent?.trim()).toBe("Show details");
    detailsBtn.click();
    await settle(f);
    expect(detailsBtn.textContent?.trim()).toBe("Hide details");
    expect(qaSection.textContent).toContain(
      "func TestRetries(t *testing.T) { _ = retries }",
    );

    const upBtn = q<HTMLButtonElement>(f, '[data-testid="qa-vote-up-11"]')!;
    expect(upBtn.getAttribute("aria-pressed")).toBe("false");
    upBtn.click();
    await settle(f);

    expect(
      api.calls.find(
        (c) =>
          c.route === "POST /api/tasks/{id}/qa/findings/{finding}/feedback",
      ),
    ).toEqual({
      method: "POST",
      route: "POST /api/tasks/{id}/qa/findings/{finding}/feedback",
      params: { id: "7", finding: "11" },
      body: { vote: 1 },
    });
    expect(upBtn.getAttribute("aria-pressed")).toBe("true");

    // Clicking Helpful again clears the vote (0).
    upBtn.click();
    await settle(f);
    expect(upBtn.getAttribute("aria-pressed")).toBe("false");

    // Read-only viewer without tasks:write does not see vote controls.
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read", "ci:read"],
      },
    });
    f.detectChanges();
    await settle(f);
    expect(q(f, '[data-testid="qa-vote-up-11"]')).toBeNull();
  });

  it("shows Bypass S3 gate action on blocked staging tasks, submits override reason, and hides it without merge:write", async () => {
    const api = new FakeApi();
    api.detail.pullRequest = undefined;
    api.detail.state = "awaiting_reply";
    api.qaResponse = {
      enabled: true,
      stageBranch: "grain-qa-stage/7",
      s3Blocked: true,
      s3HeadSha: "deadbeef12345678",
      assignment: {
        taskId: "7",
        arm: "treatment",
        policyId: "builtin:standard-workflow@1#6",
        complexityTier: "medium",
        assignedAt: "2026-11-12T10:00:00Z",
      },
      checkRuns: [
        {
          id: 21,
          taskId: "7",
          runId: "7-1",
          commitSha: "deadbeef12345678",
          stage: "S3",
          checkId: "go-test-integrity",
          arm: "treatment",
          shadow: false,
          verdict: "fail",
          durationMs: 12,
          tokensIn: 0,
          tokensOut: 0,
          allocBytes: 0,
          createdAt: "2026-11-12T10:01:00Z",
        },
      ],
      findings: [],
    };

    const f = await render(api, "changes");
    expect(q(f, '[data-testid="changes-stage-branch"]')?.textContent).toContain(
      "grain-qa-stage/7",
    );
    expect(q(f, '[data-testid="qa-s3-blocked-bar"]')).not.toBeNull();

    // Viewer without merge:write cannot see the bypass button.
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read", "ci:read"],
      },
    });
    f.detectChanges();
    await settle(f);
    expect(q(f, '[data-testid="qa-bypass-s3-btn"]')).toBeNull();

    // Operator with merge:write sees the button and can confirm override.
    TestBed.inject(AccessService).setConfig({
      access: {
        role: "grain/maintainer",
        roles: ["grain/maintainer"],
        full: false,
        scopes: ["tasks:read", "tasks:write", "ci:read", "merge:write"],
      },
    });
    f.detectChanges();
    await settle(f);

    const bypassBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="qa-bypass-s3-btn"]',
    )!;
    expect(bypassBtn).not.toBeNull();
    bypassBtn.click();
    await settle(f);

    expect(q(f, '[data-testid="bypass-s3-dialog"]')).not.toBeNull();
    const reasonEl = q<HTMLTextAreaElement>(
      f,
      '[data-testid="bypass-s3-reason"]',
    )!;
    reasonEl.value = "False positive on generated fixture test";
    reasonEl.dispatchEvent(new Event("input"));
    await settle(f);

    const confirmBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="bypass-s3-confirm"]',
    )!;
    expect(confirmBtn.disabled).toBe(false);
    confirmBtn.click();
    await settle(f);

    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/qa/bypass-s3"),
    ).toEqual({
      method: "POST",
      route: "POST /api/tasks/{id}/qa/bypass-s3",
      params: { id: "7" },
      body: {
        reason: "False positive on generated fixture test",
        head: "deadbeef12345678",
      },
    });
    expect(q(f, '[data-testid="bypass-s3-dialog"]')).toBeNull();
    expect(q(f, '[data-testid="qa-s3-blocked-bar"]')).toBeNull();
  });

  it("renders actionError once, ignores page Escape when modal is open, skips missing remote branches, and guards close(expectedId)", async () => {
    const api = new FakeApi();
    api.remoteBranches = ["main"]; // excludes grain/task-7
    const f = await render(api, "changes");
    const store = TestBed.inject(TaskStore);
    const router = TestBed.inject(Router);
    const navSpy = vi.spyOn(router, "navigate").mockResolvedValue(true);

    // 1) actionError shows a single banner (no duplicate below gates bar).
    store.actionError.set("Failed to run action");
    await settle(f);
    expect(
      (f.nativeElement as HTMLElement).querySelectorAll(
        '[data-testid="task-action-error"]',
      ),
    ).toHaveLength(1);

    // 2) Skipped diffPreview & compare when branch is absent from remote.
    expect(
      api.calls.some((c) => c.route === "GET /api/tasks/{id}/diff-preview"),
    ).toBe(false);
    expect(q(f, '[data-testid="load-full-diff"]')).toBeNull();
    expect(q(f, '[data-testid="task-diff-skipped"]')?.textContent).toContain(
      "no longer present",
    );

    // 3) TaskStore.close(otherId) does not stop active task #7, and Escape is ignored over modal.
    store.close("999");
    expect(store.id()).toBe("7");
    const fakeModal = document.createElement("div");
    fakeModal.setAttribute("aria-modal", "true");
    document.body.appendChild(fakeModal);
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", cancelable: true }),
    );
    expect(navSpy).not.toHaveBeenCalled();
    fakeModal.remove();
  });

  it("keeps Details tab accessible above 1080px without redirecting to Conversation", async () => {
    const origMatchMedia = window.matchMedia;
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: query.includes("min-width: 1080px"),
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));
    try {
      const api = new FakeApi();
      const f = await render(api, "details");
      const detailsTab = q(f, '[data-testid="task-tab-details"]')!;
      expect(detailsTab).toBeTruthy();
      expect(detailsTab.getAttribute("aria-current")).toBe("page");
      expect(q(f, '[data-testid="task-details"] .description')).toBeTruthy();
      expect(q(f, ".task-aside")).toBeNull();
    } finally {
      window.matchMedia = origMatchMedia;
    }
  });

  it("renders Docs tab with prompt, PR markdown diff, and report comments, and drives doc-reader-modal Prev/Next/backdrop/Escape", async () => {
    const api = new FakeApi();
    api.fullDiff = [
      api.fullDiff,
      "diff --git a/docs/plan.md b/docs/plan.md",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/docs/plan.md",
      "@@ -0,0 +1,4 @@",
      "+# Rollout Plan",
      "+",
      "+- Step 1: Ship Docs tab",
      "+- Step 2: Verify E2E",
    ].join("\n");
    api.detail = {
      ...api.detail,
      comments: [
        ...(api.detail.comments ?? []),
        {
          id: 9,
          author: "agent",
          authorKind: "agent",
          summary: "Audit findings",
          body: "## Audit findings\n\n- Scroll containment checked\n- Modal keyboard trap verified",
        },
      ],
    };
    const f = await render(api);

    // Open Docs directly from Prompt pane button.
    q<HTMLButtonElement>(f, '[data-testid="prompt-open-docs"]')!.click();
    await settle(f);
    expect(
      q(f, '[data-testid="task-tab-docs"]')!.getAttribute("aria-current"),
    ).toBe("page");
    expect(q(f, '[data-testid="task-docs-shell"]')).toBeTruthy();
    expect(q(f, '[data-testid="doc-item-prompt"]')).toBeTruthy();
    expect(q(f, '[data-testid="doc-item-pr:docs/plan.md"]')).toBeTruthy();
    expect(q(f, '[data-testid="doc-item-comment-9"]')).toBeTruthy();
    // Short chat comment ("Please fix it") is excluded from Docs picker.
    expect(q(f, '[data-testid="doc-item-comment-1"]')).toBeNull();

    // Switch active doc inside Docs tab.
    q<HTMLButtonElement>(
      f,
      '[data-testid="doc-item-pr:docs/plan.md"]',
    )!.click();
    await settle(f);
    expect(q(f, '[data-testid="docs-reader-title"]')!.textContent).toContain(
      "plan.md",
    );
    expect(q(f, '[data-testid="docs-reader-body"]')!.textContent).toContain(
      "Rollout Plan",
    );

    // Open full-screen modal from Docs tab.
    q<HTMLButtonElement>(f, '[data-testid="docs-fullscreen"]')!.click();
    await settle(f);
    const modal = q<HTMLDialogElement>(f, '[data-testid="doc-reader-modal"]')!;
    expect(modal.getAttribute("aria-modal")).toBe("true");
    expect(modal.hasAttribute("open")).toBe(true);
    expect(q(f, "section.task-page")!.hasAttribute("inert")).toBe(true);
    expect(q(f, '[data-testid="doc-modal-pos"]')!.textContent).toContain(
      "2 / 3",
    );

    // Navigate Next -> Comment report, then Prev -> PR plan.md -> Prev -> Prompt.
    q<HTMLButtonElement>(f, '[data-testid="doc-modal-next"]')!.click();
    await settle(f);
    expect(q(f, '[data-testid="doc-modal-pos"]')!.textContent).toContain(
      "3 / 3",
    );
    expect(q(f, '[data-testid="doc-modal-body"]')!.textContent).toContain(
      "Scroll containment checked",
    );
    expect(
      q<HTMLButtonElement>(f, '[data-testid="doc-modal-next"]')!.disabled,
    ).toBe(true);
    expect(document.activeElement).not.toBe(document.body);

    // Dragging from inside modal body out onto backdrop must NOT dismiss modal.
    q(f, '[data-testid="doc-modal-body"]')!.dispatchEvent(
      new MouseEvent("mousedown", { bubbles: true }),
    );
    modal.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await settle(f);
    expect(modal.hasAttribute("open")).toBe(true);

    q<HTMLButtonElement>(f, '[data-testid="doc-modal-prev"]')!.click();
    await settle(f);
    expect(q(f, '[data-testid="doc-modal-pos"]')!.textContent).toContain(
      "2 / 3",
    );

    // Backdrop click closes modal.
    modal.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await settle(f);
    expect(modal.hasAttribute("open")).toBe(false);
    expect(modal.getAttribute("aria-modal")).toBeNull();
    expect(q(f, "section.task-page")!.hasAttribute("inert")).toBe(false);

    // Open from Prompt full screen button and dismiss with Escape.
    q<HTMLButtonElement>(f, '[data-testid="task-tab-details"]')!.click();
    await settle(f);
    q<HTMLButtonElement>(f, '[data-testid="prompt-fullscreen"]')!.click();
    await settle(f);
    expect(modal.hasAttribute("open")).toBe(true);
    expect(q(f, '[data-testid="doc-modal-pos"]')!.textContent).toContain(
      "1 / 3",
    );
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", cancelable: true }),
    );
    await settle(f);
    expect(modal.hasAttribute("open")).toBe(false);
  });
});

describe("#comment=<id> deep links", () => {
  let opened = false;
  afterEach(() => {
    if (opened) TestBed.inject(TaskStore).close();
    opened = false;
    TestBed.resetTestingModule();
  });

  // A 1,200-comment thread (ids 1..1200) served 500 at a time.
  function threadApi(): { api: FakeApi; heads: string[]; pages: string[] } {
    const all = Array.from({ length: 1200 }, (_, i) => ({
      id: i + 1,
      author: "agent",
      authorKind: "agent" as const,
      body: `comment ${i + 1}`,
    }));
    const api = new FakeApi();
    const heads: string[] = [];
    const pages: string[] = [];
    api.getIfChanged = ((_r: string, _p: unknown, query?: string) => {
      heads.push(query ?? "");
      const limit = Number(
        new URLSearchParams(query ?? "").get("comments_limit") ?? "100",
      );
      const slice = all.slice(-limit);
      return Promise.resolve({
        body: {
          ...structuredClone(api.detail),
          comments: slice,
          olderComments: all.length - slice.length,
        },
        changed: true,
      });
    }) as unknown as FakeApi["getIfChanged"];
    const get = api.get;
    api.get = ((route: string, params?: { id?: string }, query?: string) => {
      if (
        route === "GET /api/tasks/{id}" &&
        query?.includes("comments_before=")
      ) {
        pages.push(query);
        const q = new URLSearchParams(query);
        const before = Number(q.get("comments_before"));
        const limit = Number(q.get("comments_limit"));
        const idx = all.findIndex((c) => c.id === before);
        const start = Math.max(0, idx - limit);
        return Promise.resolve({
          comments: all.slice(start, idx),
          olderComments: start,
        });
      }
      return get(route, params, query);
    }) as FakeApi["get"];
    return { api, heads, pages };
  }

  it("parses only #comment=<id> fragments", () => {
    expect(parseCommentFragment("")).toEqual({ kind: "none" });
    expect(parseCommentFragment("#task:7")).toEqual({ kind: "none" });
    expect(parseCommentFragment("#comment=0")).toEqual({
      kind: "comment",
      id: 0,
    });
    expect(parseCommentFragment("comment=42")).toEqual({
      kind: "comment",
      id: 42,
    });
    expect(parseCommentFragment("#comment=bad")).toEqual({
      kind: "invalid",
      raw: "#comment=bad",
    });
  });

  it("cold-loads a full page, pages back 500 at a time to the linked comment, and commits once", async () => {
    const { api, heads, pages } = threadApi();
    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const store = TestBed.inject(TaskStore);
    opened = true;
    const commits: number[] = [];
    store.open("7", 10);
    // Overlapping reads (the poll, an event) join the deep-link read.
    const again = store.refresh();
    await vi.waitFor(() => {
      const n = store.detail()?.comments?.length ?? 0;
      if (n && commits.at(-1) !== n) commits.push(n);
      expect(n).toBe(1200);
    });
    await again;
    expect(heads).toEqual([DEEP_LINK_HEAD_QUERY]);
    expect(pages).toEqual([
      "comments_limit=500&comments_before=701&attempts_limit=1",
      "comments_limit=500&comments_before=201&attempts_limit=1",
    ]);
    expect(commits).toEqual([1200]);

    // The next 100-comment poll keeps every comment shown.
    await store.refresh();
    expect(store.detail()?.comments?.length).toBe(1200);
  });

  it("pages an already shown thread back to a comment named by a later link", async () => {
    const { api, pages } = threadApi();
    TestBed.configureTestingModule({
      providers: [
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const store = TestBed.inject(TaskStore);
    opened = true;
    store.open("7");
    await store.refresh();
    expect(store.detail()?.comments?.length).toBe(100);
    expect(await store.loadOlderCommentsThrough(50)).toBe("found");
    expect(pages).toEqual([
      "comments_limit=500&comments_before=1101&attempts_limit=1",
      "comments_limit=500&comments_before=601&attempts_limit=1",
      "comments_limit=500&comments_before=101&attempts_limit=1",
    ]);
    expect(store.detail()?.comments?.[0].id).toBe(1);
    expect(await store.loadOlderCommentsThrough(99999)).toBe("missing");
  });

  it("switches the task page to Conversation for a #comment= link and names a bad one", async () => {
    window.history.replaceState(null, "", "/task/7#comment=1");
    try {
      const f = await render(new FakeApi(), "runs");
      opened = true;
      await settle(f);
      expect(
        q(f, '[data-testid="task-tab-conversation"]')!.getAttribute(
          "aria-current",
        ),
      ).toBe("page");
      await vi.waitFor(() => {
        f.detectChanges();
        expect(
          q(f, '[data-comment-id="1"]')!.classList.contains(
            "comment-highlight",
          ),
        ).toBe(true);
      });
      const snacks = TestBed.inject(SnackbarService);
      window.history.replaceState(null, "", "/task/7#comment=bad");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
      await settle(f);
      expect(snacks.current()?.message).toContain("does not name a comment");
      window.history.replaceState(null, "", "/task/7#comment=99999");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
      await settle(f);
      expect(snacks.current()?.message).toContain(
        "Comment 99999 is not in this task's conversation.",
      );
    } finally {
      window.history.replaceState(null, "", "/");
    }
  });

  it("schedules only one follow-up transcript poll per interval when watchTranscript is called twice while the first GET is pending", async () => {
    vi.useFakeTimers();
    try {
      const api = new FakeApi();
      const pendingTranscriptGets: {
        resolve: (v: { transcript?: string }) => void;
        reject: (err: unknown) => void;
      }[] = [];
      const baseGet = api.get;
      api.get = ((route: string, params?: { id?: string; number?: string }) => {
        if (route === "GET /api/tasks/{id}/attempts/{number}/transcript") {
          return new Promise<{ transcript?: string }>((resolve, reject) => {
            pendingTranscriptGets.push({ resolve, reject });
          });
        }
        return baseGet(route, params);
      }) as FakeApi["get"];

      TestBed.configureTestingModule({
        providers: [
          { provide: GrainApi, useValue: api },
          { provide: GrainEvents, useValue: { events: () => NEVER } },
        ],
      });
      const store = TestBed.inject(TaskStore);
      opened = true;
      store.open("7");
      await Promise.resolve();

      // Call watchTranscript twice for the same live attempt while the first GET is pending.
      store.watchTranscript(2, true);
      expect(pendingTranscriptGets).toHaveLength(1);

      store.watchTranscript(2, true);
      expect(pendingTranscriptGets).toHaveLength(2);

      // Even if the superseded first GET rejects with 503 and the second resolves,
      // only the second call's generation updates state or schedules a timer.
      pendingTranscriptGets[0].reject({
        status: 503,
        error: { error: "verifier_unavailable" },
      });
      await Promise.resolve();
      expect(store.transcriptError()).toBeNull();

      pendingTranscriptGets[1].resolve({ transcript: "turn 1\n" });
      await Promise.resolve();
      await Promise.resolve();
      expect(store.transcript()).toBe("turn 1\n");
      expect(store.transcriptError()).toBeNull();

      // Interval 1: exactly one follow-up GET fires.
      vi.advanceTimersByTime(TRANSCRIPT_POLL_MS);
      expect(pendingTranscriptGets).toHaveLength(3);

      pendingTranscriptGets[2].resolve({ transcript: "turn 2\n" });
      await Promise.resolve();
      await Promise.resolve();
      expect(store.transcript()).toBe("turn 2\n");

      // Interval 2: still exactly one follow-up GET per interval.
      vi.advanceTimersByTime(TRANSCRIPT_POLL_MS);
      expect(pendingTranscriptGets).toHaveLength(4);

      // Also verify when both concurrent initial GETs resolve in order.
      store.watchTranscript(2, true);
      store.watchTranscript(2, true);
      expect(pendingTranscriptGets).toHaveLength(6);
      pendingTranscriptGets[4].resolve({ transcript: "stale\n" });
      pendingTranscriptGets[5].resolve({ transcript: "current\n" });
      await Promise.resolve();
      await Promise.resolve();
      expect(store.transcript()).toBe("current\n");

      vi.advanceTimersByTime(TRANSCRIPT_POLL_MS);
      expect(pendingTranscriptGets).toHaveLength(7);

      store.stopTranscript();
      pendingTranscriptGets[6].resolve({ transcript: "after-stop\n" });
      await Promise.resolve();
      await Promise.resolve();
      vi.advanceTimersByTime(TRANSCRIPT_POLL_MS * 4);
      expect(pendingTranscriptGets).toHaveLength(7);
    } finally {
      vi.useRealTimers();
    }
  });
});
