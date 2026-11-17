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
import {
  BatchEditDialogComponent,
  type BatchEditRequest,
} from "./batch-edit-dialog.component";

// The Task priority box against the caller's role: maxPriority (5 for a
// role without tasks:admin on some selected row) is held beside the field
// and keeps Apply off; with no cap any whole number goes.
function render(request: BatchEditRequest) {
  const fixture = TestBed.createComponent(BatchEditDialogComponent);
  fixture.componentRef.setInput("request", request);
  fixture.detectChanges();
  const el = fixture.nativeElement as HTMLElement;
  const q = (id: string) =>
    el.querySelector(`[data-testid="${id}"]`) as HTMLInputElement | null;
  const type = (v: string) => {
    const box = q("bulk-priority")!;
    box.value = v;
    box.dispatchEvent(new Event("input"));
    fixture.detectChanges();
  };
  return { el, q, type };
}

describe("BatchEditDialogComponent priority cap", () => {
  it("refuses a priority above maxPriority beside the field", () => {
    const { el, q, type } = render({
      mode: "edit",
      count: 2,
      priority: "5",
      maxPriority: 5,
    });
    expect(el.textContent).toContain("your role sets at most 5");
    type("9");
    expect(q("bulk-priority-error")?.textContent?.trim()).toBe(
      "Above 5 needs tasks:admin on every selected task's repository.",
    );
    expect(q("bulk-priority")?.getAttribute("aria-invalid")).toBe("true");
    expect(
      (q("batch-edit-apply") as unknown as HTMLButtonElement).disabled,
    ).toBe(true);
    type("5");
    expect(q("bulk-priority-error")).toBeNull();
    expect(
      (q("batch-edit-apply") as unknown as HTMLButtonElement).disabled,
    ).toBe(false);
    type("2.5");
    expect(q("bulk-priority-error")?.textContent?.trim()).toBe(
      "Use a whole number, 1 or more.",
    );
  });

  it("leaves the priority uncapped without maxPriority", () => {
    const { el, q, type } = render({ mode: "edit", count: 1, priority: "5" });
    expect(el.textContent).not.toContain("your role sets at most");
    type("9");
    expect(q("bulk-priority-error")).toBeNull();
    expect(
      (q("batch-edit-apply") as unknown as HTMLButtonElement).disabled,
    ).toBe(false);
  });
});
