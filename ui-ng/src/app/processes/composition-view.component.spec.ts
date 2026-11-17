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
import { CompositionViewComponent } from "./composition-view.component";
import type { Composition } from "./processes.models";

describe("CompositionViewComponent", () => {
  it("omits redundant title, separates scalar and non-empty list fields, and renders body in a full-width well", async () => {
    await TestBed.configureTestingModule({
      imports: [CompositionViewComponent],
    }).compileComponents();

    const fixture = TestBed.createComponent(CompositionViewComponent);
    const comp: Composition = {
      kind: "schedule",
      repo: "sidthe/grain",
      base: "main",
      tasks: [
        {
          title: "Nightly audit",
          templateName: "Security Audit",
          fields: [
            { name: "title", values: ["Nightly audit"], source: "schedule" },
            { name: "repo", values: ["sidthe/grain"], source: "schedule" },
            { name: "model", values: ["smart"], source: "deployment" },
            { name: "reads", values: [], source: "unset" },
            {
              name: "capabilities",
              values: ["self-debug", "web-browse"],
              source: "template",
            },
            {
              name: "body",
              values: [
                "First paragraph of audit instructions.\n\nSecond paragraph.",
              ],
              source: "template",
            },
          ],
        },
      ],
    };

    fixture.componentRef.setInput("composition", comp);
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector(".comp-meta-grid")).toBeTruthy();

    // Redundant title matching t.title is omitted from the grid rows.
    expect(el.querySelector('[data-field="title"]')).toBeNull();

    // Scalar fields and empty list fields stay in compact rows.
    const repoRow = el.querySelector('[data-field="repo"]');
    expect(repoRow).toBeTruthy();
    expect(repoRow!.classList.contains("comp-row-full")).toBe(false);

    const emptyReadsRow = el.querySelector('[data-field="reads"]');
    expect(emptyReadsRow).toBeTruthy();
    expect(emptyReadsRow!.classList.contains("comp-row-full")).toBe(false);
    expect(emptyReadsRow!.textContent).toContain("—");

    // Non-empty list field spans full width.
    const capsRow = el.querySelector(
      '[data-field="capabilities"]',
    ) as HTMLElement;
    expect(capsRow).toBeTruthy();
    expect(capsRow.classList.contains("comp-row-full")).toBe(true);
    expect(capsRow.style.gridColumn).toBe("1 / -1");
    expect(capsRow.textContent).toContain("self-debug, web-browse");

    // Body is rendered in dedicated full-width well without expand toggle when short.
    const bodyWell = el.querySelector(
      '[data-testid="composition-body-well"]',
    ) as HTMLElement;
    expect(bodyWell).toBeTruthy();
    expect(bodyWell.classList.contains("comp-body-well")).toBe(true);
    expect(bodyWell.classList.contains("is-clamped")).toBe(false);
    expect(bodyWell.textContent).toContain(
      "First paragraph of audit instructions.\n\nSecond paragraph.",
    );
    expect(
      el.querySelector('[data-testid="composition-body-toggle"]'),
    ).toBeNull();
  });

  it("clamps long body (> 600 chars or > 12 lines) and toggles Expand full prompt / Collapse prompt", async () => {
    await TestBed.configureTestingModule({
      imports: [CompositionViewComponent],
    }).compileComponents();

    const fixture = TestBed.createComponent(CompositionViewComponent);
    const longPrompt = Array.from(
      { length: 16 },
      (_, i) => `Step ${i + 1}: Verify invariant and record detailed findings.`,
    ).join("\n");

    const comp: Composition = {
      kind: "template",
      repo: "sidthe/grain",
      tasks: [
        {
          title: "Deep check",
          fields: [
            { name: "repo", values: ["sidthe/grain"], source: "template" },
            { name: "body", values: [longPrompt], source: "template" },
          ],
        },
      ],
    };

    fixture.componentRef.setInput("composition", comp);
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    const bodyWell = el.querySelector(
      '[data-testid="composition-body-well"]',
    ) as HTMLElement;
    const toggleBtn = el.querySelector(
      '[data-testid="composition-body-toggle"]',
    ) as HTMLButtonElement;

    expect(bodyWell).toBeTruthy();
    expect(toggleBtn).toBeTruthy();
    expect(bodyWell.classList.contains("is-clamped")).toBe(true);
    expect(bodyWell.style.maxHeight).toBe("320px");
    expect(toggleBtn.getAttribute("aria-expanded")).toBe("false");
    expect(toggleBtn.textContent).toContain("Expand full prompt");

    toggleBtn.click();
    fixture.detectChanges();

    expect(bodyWell.classList.contains("is-clamped")).toBe(false);
    expect(bodyWell.style.maxHeight).toBe("");
    expect(toggleBtn.getAttribute("aria-expanded")).toBe("true");
    expect(toggleBtn.textContent).toContain("Collapse prompt");

    toggleBtn.click();
    fixture.detectChanges();

    expect(bodyWell.classList.contains("is-clamped")).toBe(true);
    expect(toggleBtn.getAttribute("aria-expanded")).toBe("false");
  });
});
