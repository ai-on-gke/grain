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
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type {
  GrainConfig,
  Notification,
  Task,
  TaskDetail,
} from "../api/models";
import { InboxPageComponent, ago, questionOf } from "./inbox-page.component";
import { InboxStore } from "./inbox.store";

class FakeApi {
  cfg: GrainConfig = { access: null, version: "v1.2.3" };
  taskList: Task[] = [];
  notes: Notification[] = [];
  details: Record<string, TaskDetail> = {};
  posts: { route: string; params: unknown; body: unknown }[] = [];
  onPost: (route: string, params: Record<string, unknown>) => void = () => {};

  config = () => Promise.resolve(this.cfg);
  tasks = () => Promise.resolve(this.taskList);
  task = (id: string) =>
    Promise.resolve(this.details[id] ?? ({ id } as TaskDetail));
  openNotifications = () => Promise.resolve(this.notes);
  localScriptApprovals = () => Promise.resolve([]);
  get = () => Promise.resolve({ publicKey: "age1example" });
  post = (route: string, params: Record<string, unknown>, body: unknown) => {
    this.posts.push({ route, params, body });
    this.onPost(route, params);
    return Promise.resolve({});
  };
}

async function render(
  api: FakeApi,
): Promise<ComponentFixture<InboxPageComponent>> {
  localStorage.clear();
  TestBed.configureTestingModule({
    imports: [InboxPageComponent],
    providers: [
      {
        provide: GrainApi,
        useValue: api,
      },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const fixture = TestBed.createComponent(InboxPageComponent);
  fixture.autoDetectChanges();
  await TestBed.inject(InboxStore).refresh();
  await fixture.whenStable();
  return fixture;
}

function q(f: ComponentFixture<unknown>, sel: string): HTMLElement | null {
  return (f.nativeElement as HTMLElement).querySelector(sel);
}

function rows(f: ComponentFixture<unknown>): string[] {
  return [
    ...(f.nativeElement as HTMLElement).querySelectorAll(
      ".queue-row .row-title",
    ),
  ].map((e) => e.textContent?.trim() ?? "");
}

describe("InboxPageComponent", () => {
  afterEach(() => TestBed.inject(InboxStore).stop());

  it("lists waiting work by group, approves a proposal, and drops it once the server does", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Investigate the merge queue stall",
        state: "awaiting_reply",
        questionSummary: "Fix both?",
      },
      { id: "9", title: "Bump Go", state: "proposed" },
      { id: "5", title: "Running work", state: "running" },
    ];
    api.onPost = (route, params) => {
      if (route === "POST /api/tasks/{id}/approve") {
        api.taskList = api.taskList.filter((t) => t.id !== params["id"]);
      }
    };
    const f = await render(api);
    expect(q(f, '[data-testid="next-inbox-headline"]')?.textContent).toContain(
      "2 decisions waiting on you",
    );
    expect(rows(f)).toEqual(["Investigate the merge queue stall", "Bump Go"]);
    expect(q(f, '[data-testid="inbox-group-reply"]')).toBeTruthy();

    const bump = [
      ...(f.nativeElement as HTMLElement).querySelectorAll<HTMLElement>(
        ".queue-row",
      ),
    ].find((r) => r.textContent?.includes("Bump Go"))!;
    bump.querySelector<HTMLButtonElement>(".row-main")!.click();
    await f.whenStable();
    expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
      "#9",
    );
    expect(
      q(f, '[data-testid="inspector-open-task"]')?.getAttribute("href"),
    ).toBe("/tasks/9");
    (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).click();
    await f.whenStable();
    await TestBed.inject(InboxStore).refresh();
    await f.whenStable();
    expect(api.posts[0]).toMatchObject({
      route: "POST /api/tasks/{id}/approve",
      params: { id: "9" },
    });
    expect(rows(f)).toEqual(["Investigate the merge queue stall"]);
  });

  it("keeps a half-typed reply when a refresh brings the same row back", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "4",
        title: "Parked",
        state: "awaiting_reply",
        questionSummary: "Fix both?",
      },
      { id: "9", title: "Bump Go", state: "proposed" },
    ];
    const f = await render(api);
    expect(q(f, '[data-testid="inspector-title"]')?.textContent).toContain(
      "#4",
    );
    const input = q(f, '[data-testid="reply-input"]') as HTMLTextAreaElement;
    input.value = "Keep both";
    input.dispatchEvent(new Event("input"));
    await f.whenStable();

    // Another row leaves and the list is rebuilt, as an SSE event does.
    api.taskList = api.taskList.filter((t) => t.id !== "9");
    await TestBed.inject(InboxStore).refresh();
    await f.whenStable();
    expect(
      (q(f, '[data-testid="reply-input"]') as HTMLTextAreaElement).value,
    ).toBe("Keep both");
    expect(
      (q(f, '[data-testid="send-reply"]') as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  it("asks before declining, and sends nothing when cancelled", async () => {
    const api = new FakeApi();
    api.taskList = [{ id: "9", title: "Bump Go", state: "proposed" }];
    const f = await render(api);
    (q(f, '[data-testid="action-decline"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
      "Decline #9",
    );
    (q(f, '[data-testid="confirm-cancel"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')).toBeNull();
    expect(api.posts).toEqual([]);
  });

  it("approves a breaking release with allow_breaking after a confirmation", async () => {
    const api = new FakeApi();
    api.notes = [
      {
        id: 7,
        subject: "release.pending:v2.4.0",
        actions: ["approve-release"],
        repo: "acme/widgets",
        body: "v2.4.0 is staged.\nBREAKING CHANGES: drops v1",
      },
    ];
    const f = await render(api);
    expect(q(f, '[data-testid="approve-release"]')?.textContent).toContain(
      "Approve breaking",
    );
    (q(f, '[data-testid="approve-release"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(q(f, '[data-testid="confirm-dialog"]')?.textContent).toContain(
      "BREAKING",
    );
    (q(f, '[data-testid="confirm-accept"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(api.posts[0]).toEqual({
      route:
        "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
      params: { owner: "acme", name: "widgets", version: "v2.4.0" },
      body: { allow_breaking: true },
    });
  });

  it("grants the ticked items of an access request and requires a reason to deny", async () => {
    const api = new FakeApi();
    api.taskList = [
      {
        id: "3",
        title: "Needs repo",
        state: "awaiting_reply",
        awaitingAccess: true,
      },
    ];
    api.details["3"] = {
      id: "3",
      title: "Needs repo",
      state: "awaiting_reply",
      pendingAccess: {
        summary: "Read acme/lib",
        reason: "to vendor it",
        createdAt: "",
        items: [
          { kind: "read", repo: "acme/lib" },
          { kind: "network", access: "open" },
        ],
      },
    };
    const f = await render(api);
    const boxes = (
      f.nativeElement as HTMLElement
    ).querySelectorAll<HTMLInputElement>(".access-items input");
    expect(boxes).toHaveLength(2);
    boxes[1].click();
    await f.whenStable();
    expect(q(f, '[data-testid="grant-access"]')?.textContent).toContain(
      "Grant 1 of 2",
    );
    (q(f, '[data-testid="grant-access"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(api.posts[0]).toMatchObject({
      route: "POST /api/tasks/{id}/access-request/grant",
      body: { items: [0] },
    });

    (q(f, '[data-testid="deny-access"]') as HTMLButtonElement).click();
    await f.whenStable();
    expect(
      (q(f, '[data-testid="confirm-deny"]') as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("disables what a viewer cannot do and says why", async () => {
    const api = new FakeApi();
    api.cfg = {
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    };
    api.taskList = [{ id: "9", title: "Bump Go", state: "proposed" }];
    const f = await render(api);
    expect(
      (q(f, '[data-testid="action-approve"]') as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(q(f, '[data-testid="rbac-denied"]')?.textContent).toContain(
      "grain/viewer",
    );
  });

  it("shows an empty state when nothing waits", async () => {
    const f = await render(new FakeApi());
    expect(q(f, '[data-testid="next-inbox-empty"]')).toBeTruthy();
    expect(q(f, '[data-testid="next-inbox-headline"]')?.textContent).toContain(
      "Nothing waiting on you",
    );
  });
});

describe("inbox page helpers", () => {
  it("formats a short age", () => {
    const now = Date.parse("2026-01-01T12:00:00Z");
    expect(ago("2026-01-01T11:58:00Z", now)).toBe("2m");
    expect(ago("2026-01-01T09:00:00Z", now)).toBe("3h");
    expect(ago(null, now)).toBe("");
  });

  it("reads the question a task is parked on from its comments", () => {
    const d = {
      id: "1",
      title: "",
      state: "",
      comments: [{ id: 5, body: "Keep v1?" }],
    } as TaskDetail;
    expect(
      questionOf(d, { task: { questionCommentId: 5 }, summary: "s" } as never),
    ).toBe("Keep v1?");
  });
});
