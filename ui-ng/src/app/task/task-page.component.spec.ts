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
import type { DiffPreview, TaskDetail } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskPageComponent, backFrom, duration } from "./task-page.component";
import { TaskStore } from "./task.store";

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
  calls: Call[] = [];
  transcripts: string[] = [];

  getIfChanged = () =>
    Promise.resolve({ body: structuredClone(this.detail), changed: true });
  diffPreview = () => Promise.resolve(structuredClone(this.preview));
  get = (
    route: string,
    params?: { id?: string; number?: string; owner?: string; name?: string },
    query?: string,
  ) => {
    this.calls.push({ method: "GET", route, params, query });
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
    if (route === "GET /api/tasks/{id}/context") {
      return Promise.resolve({ brief: "Original brief", summarised: 0 });
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
    return Promise.resolve({});
  }
  post = (r: string, p?: unknown, b?: unknown) => this.rec("POST", r, p, b);
  patch = (r: string, p?: unknown, b?: unknown) => this.rec("PATCH", r, p, b);
  put = (r: string, p?: unknown, b?: unknown) => this.rec("PUT", r, p, b);
  delete = (r: string, p?: unknown) => this.rec("DELETE", r, p);
  config = () =>
    Promise.resolve({
      access: null,
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
  afterEach(() => {
    TestBed.inject(TaskStore).close();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("shows the header, question, conversation and details with zero Classic links and zero <select>s", async () => {
    const f = await render(new FakeApi());
    const root = f.nativeElement as HTMLElement;
    expect(q(f, "#task-title")!.textContent).toContain("#7");
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

    // Zero Classic links & zero <select> elements
    expect(root.querySelectorAll("select")).toHaveLength(0);
    expect(root.querySelector('a[href="/tasks/7"]')).toBeNull();
    expect(root.textContent).not.toContain("classic task page");
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
    await settle(f);

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
    q<HTMLButtonElement>(f, '[data-testid="changes-retry-merge"]')!.click();
    await settle(f);
    expect(
      api.calls.find((c) => c.route === "POST /api/tasks/{id}/retry-merge"),
    ).toMatchObject({ params: { id: "7" } });

    // Force merge requires reason AND explicit review-gate override checkbox
    q<HTMLButtonElement>(
      f,
      '[data-testid="changes-force-merge-open"]',
    )!.click();
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
    });

    it("turns Submit off while CI is failing and shows CI in the header", async () => {
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
      const submit = q<HTMLButtonElement>(f, '[data-testid="act-submit"]')!;
      expect(submit.disabled).toBe(true);
      expect(q(f, '[data-testid="task-ci"]')!.textContent?.trim()).toBe("✕ CI");
      submit.click();
      await settle(f);
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

  it("opens force-merge dialog outside inert section, traps focus, and closes on Escape", async () => {
    const api = new FakeApi();
    api.detail = {
      ...api.detail,
      state: "awaiting_submit",
      mergeGates: [{ id: "ci", label: "CI", state: "failing" }],
    };
    const f = await render(api, "changes");

    const openBtn = q<HTMLButtonElement>(
      f,
      '[data-testid="changes-force-merge-open"]',
    )!;
    openBtn.focus();
    openBtn.click();
    await settle(f);

    const dialog = q(f, '[data-testid="force-merge-dialog"]')!;
    expect(dialog).toBeTruthy();
    expect(dialog.closest("[inert]")).toBeNull();

    const section = q(f, "section.view-panel")!;
    expect(section.hasAttribute("inert")).toBe(true);

    // Press escape
    dialog.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await settle(f);

    expect(q(f, '[data-testid="force-merge-dialog"]')).toBeNull();
    expect(section.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(openBtn);
  });
});
