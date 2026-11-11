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

import { computed, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import type { Task } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
import { NextUpComponent } from "./next-up.component";

describe("NextUpComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  function render(initial: Partial<Task>[]) {
    const tasks = signal<Task[]>(initial as Task[]);
    const fakeFeed = {
      tasks,
      runningCount: computed(
        () =>
          tasks().filter((t) => !t.consoleSession && t.state === "running")
            .length,
      ),
    };
    TestBed.configureTestingModule({
      imports: [NextUpComponent],
      providers: [
        provideRouter([]),
        { provide: TaskFeedStore, useValue: fakeFeed },
      ],
    });
    const f = TestBed.createComponent(NextUpComponent);
    f.autoDetectChanges();
    return { el: f.nativeElement as HTMLElement, tasks, fixture: f };
  }

  it("names the first backlog task not waiting on a dependency, rotates chevron, and opens the sequence", async () => {
    const { el, fixture } = render([
      {
        id: "1",
        title: "Director",
        state: "awaiting_reply",
        interactive: true,
      },
      {
        id: "4",
        title: "Blocked",
        state: "queued",
        priority: 8,
        blocked: true,
      },
      { id: "5", title: "Rotate the CI token", state: "queued", priority: 8 },
      { id: "6", title: "Later", state: "queued", priority: 3 },
      { id: "7", title: "Active worker", state: "running" },
    ]);
    const wrap = el.querySelector('[data-testid="next-up"]')!;
    expect(wrap.classList.contains("is-empty")).toBe(false);
    const chip = el.querySelector("#top-next-chip")!;
    expect(chip.textContent).toContain("#5");
    expect(chip.getAttribute("title")).toContain(
      "1 ahead of it wait on dependencies",
    );
    const caret = el.querySelector<HTMLButtonElement>(
      '[data-testid="next-up-caret"]',
    )!;
    expect(caret.classList.contains("is-open")).toBe(false);
    caret.click();
    await fixture.whenStable();
    expect(caret.getAttribute("aria-expanded")).toBe("true");
    expect(caret.classList.contains("is-open")).toBe(true);
    const pop = el.querySelector('[data-testid="next-up-pop"]')!;
    expect(pop.textContent).toContain("What runs next");
    expect(pop.textContent).toContain("3 in backlog · 1 running");
    const rows = [...el.querySelectorAll(".next-up-list li")].map((li) =>
      [...li.querySelectorAll("span")]
        .map((x) => x.textContent?.trim())
        .join(" | "),
    );
    expect(rows).toEqual([
      "1 | #4 Blocked | High 8",
      "2 | #5 Rotate the CI token | High 8",
      "3 | #6 Later | Low 3",
    ]);
    expect(
      el
        .querySelector('[data-testid="next-up-open-backlog"]')
        ?.getAttribute("href"),
    ).toBe("/tasks?tab=backlog");

    caret.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await fixture.whenStable();
    expect(el.querySelector('[data-testid="next-up-pop"]')).toBeNull();
    expect(document.activeElement).toBe(caret);
  });

  it("stays rendered as Next up · Queue empty with backlog guidance when nothing is waiting", async () => {
    const { el, fixture } = render([
      { id: "2", title: "p", state: "proposed" },
    ]);
    const wrap = el.querySelector('[data-testid="next-up"]')!;
    expect(wrap).not.toBeNull();
    expect(wrap.classList.contains("is-empty")).toBe(true);
    expect(
      el.querySelector('[data-testid="next-up-empty"]')?.textContent?.trim(),
    ).toBe("Queue empty");
    expect(el.querySelector("#top-next-chip")?.getAttribute("href")).toBe(
      "/tasks?tab=backlog",
    );

    el.querySelector<HTMLButtonElement>(
      '[data-testid="next-up-caret"]',
    )!.click();
    await fixture.whenStable();
    expect(
      el.querySelector('[data-testid="next-up-empty-note"]')?.textContent,
    ).toContain("Nothing is waiting to run.");
    expect(
      el.querySelector('[data-testid="next-up-open-backlog"]')?.textContent,
    ).toContain("Open the backlog");
  });

  it("reports N waiting on deps when queued tasks exist but all are blocked", () => {
    const { el } = render([
      { id: "8", title: "Wait A", state: "queued", blocked: true },
      {
        id: "9",
        title: "Wait B",
        state: "queued",
        dependsOnStates: { "7": "running" },
      },
    ]);
    const wrap = el.querySelector('[data-testid="next-up"]')!;
    expect(wrap.classList.contains("is-empty")).toBe(true);
    expect(
      el.querySelector('[data-testid="next-up-blocked"]')?.textContent?.trim(),
    ).toBe("2 waiting on deps");
  });
});
