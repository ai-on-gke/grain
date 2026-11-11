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

import { Component } from "@angular/core";
import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { Router, provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { SnackbarService } from "../services/snackbar.service";
import { CreateTaskService } from "./create-task.service";
import { NewTaskSheetComponent } from "./new-task-sheet.component";

type Call = { route: string; params?: unknown; body?: unknown };

class FakeApi {
  calls: Call[] = [];
  fail: Record<string, unknown> = {};
  cfg = {
    access: null,
    approvedByDefault: true,
    defaultTarget: "sidthe/grain",
    models: ["fast", "smart"],
    defaultModel: "smart",
    deployment: { repos: [{ repo: "sidthe/grain" }] },
  };
  config = () => Promise.resolve(this.cfg);
  tasks = () =>
    Promise.resolve([
      {
        id: "4",
        title: "Inbox split",
        state: "proposed",
        repo: "sidthe/grain",
      },
      {
        id: "1",
        title: "Director",
        state: "awaiting_reply",
        interactive: true,
      },
    ] as Task[]);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  get = () =>
    Promise.resolve({
      sessions: [{ id: "director", title: "Director", taskId: "1" }],
    });
  post = (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ route, params, body });
    const err = this.fail[route];
    if (err) {
      delete this.fail[route];
      return new Promise((_, reject) => setTimeout(() => reject(err)));
    }
    if (route === "POST /api/tasks") return Promise.resolve({ id: "42" });
    return Promise.resolve({});
  };
}

@Component({
  imports: [NewTaskSheetComponent],
  template: `<button id="opener" type="button">New</button
    ><g-new-task-sheet />`,
})
class HostComponent {}

async function render(api: FakeApi): Promise<ComponentFixture<HostComponent>> {
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([{ path: "**", children: [] }]),
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const f = TestBed.createComponent(HostComponent);
  f.autoDetectChanges();
  await TestBed.inject(InboxStore).refresh();
  return f;
}

async function open(f: ComponentFixture<unknown>): Promise<void> {
  const opener = document.getElementById("opener")!;
  opener.focus();
  TestBed.inject(CreateTaskService).open({}, opener);
  await f.whenStable();
  await new Promise((r) => setTimeout(r));
  await f.whenStable();
}

function q<T extends HTMLElement = HTMLElement>(sel: string): T | null {
  return document.querySelector<T>(sel);
}

function type(sel: string, value: string): void {
  const el = q<HTMLInputElement>(sel)!;
  el.value = value;
  el.dispatchEvent(new Event("input"));
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r));
  await f.whenStable();
}

describe("NewTaskSheetComponent", () => {
  afterEach(() => {
    TestBed.inject(InboxStore).stop();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("opens as a modal with focus on the title and the deployment's defaults", async () => {
    const api = new FakeApi();
    const f = await render(api);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    await open(f);
    const sheet = q('[data-testid="new-task-sheet"]')!;
    expect(sheet.getAttribute("aria-modal")).toBe("true");
    expect(document.activeElement).toBe(q('[data-testid="nt-title"]'));
    expect(q<HTMLSelectElement>('[data-testid="nt-repo"]')!.value).toBe(
      "sidthe/grain",
    );
    expect(
      q('[data-testid="approved-switch"]')!.getAttribute("aria-checked"),
    ).toBe("true");
    expect(q('[data-testid="new-task-submit"]')!.textContent?.trim()).toBe(
      "Create and start",
    );
    // Standing session tasks are not offered as dependencies.
    const deps = [
      ...q<HTMLSelectElement>('[data-testid="nt-depends"]')!.options,
    ];
    expect(deps.map((o) => o.value)).toEqual(["", "4"]);
  });

  it("asks for a title instead of filing an empty task", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(q('[data-testid="new-task-error"]')?.textContent).toContain(
      "Give the task a title.",
    );
    expect(api.calls).toHaveLength(0);
  });

  it("files a proposal when Approve is off, and offers Open and Discard", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Write the Sessions docs");
    q<HTMLButtonElement>('[data-testid="approved-switch"]')!.click();
    await f.whenStable();
    expect(q('[data-testid="new-task-submit"]')!.textContent?.trim()).toBe(
      "Save as proposal",
    );
    const deps = q<HTMLSelectElement>('[data-testid="nt-depends"]')!;
    deps.value = "4";
    deps.dispatchEvent(new Event("change"));
    await f.whenStable();
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    expect(api.calls[0]).toMatchObject({
      route: "POST /api/tasks",
      body: {
        title: "Write the Sessions docs",
        approved: false,
        repo: "sidthe/grain",
        dependsOn: ["4"],
      },
    });
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    const snack = TestBed.inject(SnackbarService).current()!;
    expect(snack.message).toBe("Saved #42 as a proposal in the Inbox");
    expect(snack.secondary?.label).toBe("Open");
    expect(snack.action?.label).toBe("Discard");
    await TestBed.inject(SnackbarService).runAction(snack.id);
    expect(api.calls[1]).toMatchObject({
      route: "POST /api/tasks/{id}/close",
      params: { id: "42" },
      body: { close_pull_request: false },
    });
  });

  it("files with Ctrl+Enter from anywhere in the sheet and keeps the sheet open on a refusal", async () => {
    const api = new FakeApi();
    api.fail["POST /api/tasks"] = {
      status: 400,
      error: { error: "repo not allowed" },
    };
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "x");
    // From a switch, not a text field: anywhere in the sheet counts.
    const sw = q<HTMLButtonElement>('[data-testid="front-switch"]')!;
    sw.focus();
    sw.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    await settle(f);
    expect(api.calls).toHaveLength(1);
    expect(q('[data-testid="new-task-error"]')?.textContent).toContain(
      "repo not allowed",
    );
    expect(q('[data-testid="new-task-sheet"]')).toBeTruthy();
  });

  it("hands the draft to the Director and opens that conversation", async () => {
    const api = new FakeApi();
    const f = await render(api);
    const nav = vi.spyOn(TestBed.inject(Router), "navigate");
    await open(f);
    type('[data-testid="nt-title"]', "Add a dark mode");
    q<HTMLButtonElement>('[data-testid="ask-director"]')!.click();
    await settle(f);
    const sent = api.calls.find(
      (c) => c.route === "POST /api/tasks/{id}/comments",
    );
    expect(sent?.params).toEqual({ id: "1" });
    expect((sent?.body as { body: string }).body).toContain(
      "**Add a dark mode**",
    );
    expect(nav).toHaveBeenCalledWith(["/sessions", "director"]);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
  });

  it("shows More options on request and closes on Escape, returning focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    expect(q('[data-testid="nt-nothing"]')).toBeNull();
    const more = q<HTMLButtonElement>('[data-testid="more-options"]')!;
    more.click();
    await f.whenStable();
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(q('[data-testid="nt-nothing"]')).toBeTruthy();
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    vi.advanceTimersByTime(5);
    expect(q('[data-testid="new-task-sheet"]')).toBeNull();
    expect(document.activeElement?.id).toBe("opener");
    vi.useRealTimers();
  });

  it("offers only Open after a started task: it cannot be taken back", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "Run now");
    q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.click();
    await settle(f);
    const snack = TestBed.inject(SnackbarService).current()!;
    expect(snack.message).toBe("Filed #42; it starts when a slot is free");
    expect(snack.action?.label).toBe("Open");
    expect(snack.secondary).toBeUndefined();
  });

  it("keeps changes made while the config is still loading", async () => {
    const api = new FakeApi();
    let release!: () => void;
    const f = await render(api);
    api.config = () =>
      new Promise((r) => (release = () => r(api.cfg))) as Promise<
        typeof api.cfg
      >;
    await open(f);
    const sw = q<HTMLButtonElement>('[data-testid="approved-switch"]')!;
    sw.click();
    await f.whenStable();
    const chosen = sw.getAttribute("aria-checked");
    type('[data-testid="nt-title"]', "typed early");
    release();
    await settle(f);
    // The config's approvedByDefault does not undo the person's click.
    expect(
      q('[data-testid="approved-switch"]')!.getAttribute("aria-checked"),
    ).toBe(chosen);
    expect(q<HTMLInputElement>('[data-testid="nt-title"]')!.value).toBe(
      "typed early",
    );
  });

  it("brings a cancelled draft back on the next plain opening", async () => {
    const api = new FakeApi();
    const f = await render(api);
    await open(f);
    type('[data-testid="nt-title"]', "half written");
    q<HTMLButtonElement>('[data-testid="new-task-close"]')!.click();
    await settle(f);
    await open(f);
    expect(q<HTMLInputElement>('[data-testid="nt-title"]')!.value).toBe(
      "half written",
    );
  });

  it("explains why filing is not allowed for this role", async () => {
    const api = new FakeApi();
    (api.cfg as Record<string, unknown>)["access"] = {
      full: false,
      role: "viewer",
      roles: ["viewer"],
      scopes: ["tasks:read"],
    };
    const f = await render(api);
    await open(f);
    await settle(f);
    expect(q('[data-testid="new-task-denied"]')?.textContent).toContain(
      "tasks:write",
    );
    expect(
      q<HTMLButtonElement>('[data-testid="new-task-submit"]')!.disabled,
    ).toBe(true);
  });
});
