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
import type { Task } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { NextUpComponent } from "./next-up.component";

describe("NextUpComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  function render(tasks: Partial<Task>[]) {
    TestBed.configureTestingModule({
      imports: [NextUpComponent],
      providers: [
        provideRouter([]),
        { provide: InboxStore, useValue: { tasks: () => tasks as Task[] } },
      ],
    });
    const f = TestBed.createComponent(NextUpComponent);
    f.autoDetectChanges();
    return f.nativeElement as HTMLElement;
  }

  it("names the first backlog task not waiting on a dependency, and opens the sequence", () => {
    const el = render([
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
    ]);
    const chip = el.querySelector("#top-next-chip")!;
    expect(chip.textContent).toContain("#5");
    expect(chip.getAttribute("title")).toContain(
      "1 ahead of it wait on dependencies",
    );
    const caret = el.querySelector<HTMLButtonElement>(
      '[data-testid="next-up-caret"]',
    )!;
    caret.click();
    expect(caret.getAttribute("aria-expanded")).toBe("true");
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
  });

  it("is absent when nothing is waiting for a worker", () => {
    const el = render([{ id: "2", title: "p", state: "proposed" }]);
    expect(el.querySelector('[data-testid="next-up"]')).toBeNull();
  });
});
