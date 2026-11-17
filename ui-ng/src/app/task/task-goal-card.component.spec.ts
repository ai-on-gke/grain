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

import { Component, signal } from "@angular/core";
import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { InboxStore } from "../inbox/inbox.store";
import { SnackbarService } from "../services/snackbar.service";
import { TaskGoalCardComponent } from "./task-goal-card.component";

type Call = { route: string; params?: unknown; body?: unknown; query?: string };

class FakeApi {
  calls: Call[] = [];
  config = () => Promise.resolve({ access: null });
  tasks = () => Promise.resolve([] as Task[]);
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  getIfChanged = (route: string, params?: unknown, query?: string) => {
    this.calls.push({ route, params, query });
    return Promise.resolve({ body: [], changed: false });
  };
  get = (route: string, params?: unknown, query?: string) => {
    this.calls.push({ route, params, query });
    return Promise.resolve({});
  };
  patch = (route: string, params?: unknown, body?: unknown, query?: string) => {
    this.calls.push({ route, params, body, query });
    return Promise.resolve({});
  };
}

@Component({
  imports: [TaskGoalCardComponent],
  template: `
    <g-task-goal-card
      [task]="task()"
      [attemptsCount]="attemptsCount()"
      (acted)="actedCount = actedCount + 1"
    />
  `,
})
class HostComponent {
  readonly task = signal<Task>({
    id: "88",
    title: "Prototype OAuth device flow",
    state: "awaiting_submit",
    repo: "sidthe/grain",
    base: "main",
    branch: "grain/ee5afe06/task-88-oauth",
    goalMode: {
      enabled: true,
      maxPasses: 8,
      wallClockMinutes: 120,
      mockMissingSecrets: true,
    },
    goalAssumptions: [
      {
        id: "a1",
        topic: "Token storage",
        decision: "Encrypted SQLite table",
        rationale: "Avoids external KMS requirement in local dev",
        alternatives: "In-memory map",
        pass: 1,
      },
    ],
    goalScorecard: {
      passesUsed: 2,
      maxPasses: 8,
      verified: true,
      summary: "All OAuth device endpoints and unit tests pass.",
      deliverables: [
        {
          requirement: "Device code polling endpoint",
          evidence: "pkg/auth/device_test.go (PASS)",
          verified: true,
        },
        {
          requirement: "CLI login command",
          evidence: "cmd/grain/login_test.go (PASS)",
          verified: true,
        },
      ],
    },
  });
  readonly attemptsCount = signal(2);
  actedCount = 0;
}

describe("TaskGoalCardComponent", () => {
  let api: FakeApi;
  let fixture: ComponentFixture<HostComponent>;

  beforeEach(async () => {
    api = new FakeApi();
    TestBed.configureTestingModule({
      imports: [HostComponent],
      providers: [
        provideRouter([]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    fixture = TestBed.createComponent(HostComponent);
    fixture.detectChanges();
    await fixture.whenStable();
  });

  afterEach(() => {
    TestBed.inject(InboxStore).stop();
    TestBed.inject(SnackbarService).dismiss();
    TestBed.resetTestingModule();
  });

  it("renders verified status pill, deliverables checklist, and assumptions ledger", () => {
    const el = fixture.nativeElement as HTMLElement;
    const card = el.querySelector('[data-testid="task-goal-card"]')!;
    expect(card).not.toBeNull();

    const pill = card.querySelector('[data-testid="goal-status-pill"]')!;
    expect(pill.textContent).toContain("Goal verified · 2 passes");
    expect(pill.classList.contains("healthy")).toBe(true);

    const deliverables = card.querySelectorAll(
      '[data-testid="goal-deliverable"]',
    );
    expect(deliverables).toHaveLength(2);
    expect(deliverables[0].textContent).toContain(
      "Device code polling endpoint",
    );
    expect(deliverables[0].textContent).toContain("pkg/auth/device_test.go");

    const assumptions = card.querySelectorAll(
      '[data-testid="goal-assumption"]',
    );
    expect(assumptions).toHaveLength(1);
    expect(assumptions[0].textContent).toContain("Token storage:");
    expect(assumptions[0].textContent).toContain("Encrypted SQLite table");
    expect(assumptions[0].textContent).toContain("Alternatives: In-memory map");
  });

  it("renders Stopped at cap warning pill and mid-flight pass counter", async () => {
    fixture.componentInstance.task.update((t) => ({
      ...t,
      goalScorecard: {
        passesUsed: 4,
        maxPasses: 4,
        verified: false,
        stoppedAtCap: true,
        stopReason: "Reached maxPasses (4/4).",
      },
    }));
    fixture.detectChanges();
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    const pill = el.querySelector('[data-testid="goal-status-pill"]')!;
    expect(pill.textContent).toContain("Stopped at cap · 4 / 4 passes");
    expect(pill.classList.contains("warn")).toBe(true);

    fixture.componentInstance.task.update((t) => ({
      ...t,
      state: "running",
      goalScorecard: null,
    }));
    fixture.componentInstance.attemptsCount.set(3);
    fixture.detectChanges();
    await fixture.whenStable();

    expect(pill.textContent).toContain("Autonomous goal · Pass 3 / 8");
    expect(pill.classList.contains("setup")).toBe(true);
  });

  it("switches to supervised mode via PATCH /api/tasks/{id}", async () => {
    const el = fixture.nativeElement as HTMLElement;
    const btn = el.querySelector<HTMLButtonElement>(
      '[data-testid="act-switch-supervised"]',
    )!;
    expect(btn).not.toBeNull();
    btn.click();
    await fixture.whenStable();

    const patchCall = api.calls.find(
      (c) => c.route === "PATCH /api/tasks/{id}",
    );
    expect(patchCall).toMatchObject({
      route: "PATCH /api/tasks/{id}",
      params: { id: "88" },
      body: { goalMode: { enabled: false } },
    });
    expect(fixture.componentInstance.actedCount).toBe(1);
  });

  it("opens NewTaskSheet pre-filled for follow-up revision when Revise… is clicked", async () => {
    const el = fixture.nativeElement as HTMLElement;
    const btn = el.querySelector<HTMLButtonElement>(
      '[data-testid="act-revise-goal"]',
    )!;
    expect(btn).not.toBeNull();
    btn.click();
    await fixture.whenStable();

    const createSvc = TestBed.inject(CreateTaskService);
    expect(createSvc.isOpen()).toBe(true);
    expect(createSvc.prefill()).toMatchObject({
      title: "Revise: Prototype OAuth device flow",
      repo: "sidthe/grain",
      base: "grain/ee5afe06/task-88-oauth",
      generatedFrom: "88",
      autoApproveProposals: true,
      goalMode: {
        enabled: true,
        maxPasses: 8,
        wallClockMinutes: 120,
        mockMissingSecrets: true,
      },
    });
    expect(createSvc.prefill().description).toContain("#88");
    expect(createSvc.prefill().description).toContain("Token storage");
  });
});
