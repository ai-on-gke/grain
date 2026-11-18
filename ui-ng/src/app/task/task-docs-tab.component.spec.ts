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
import { describe, expect, it } from "vitest";
import type { TaskDocumentItem } from "./task-docs";
import { TaskDocsTabComponent } from "./task-docs-tab.component";

function makeDocs(suffix = ""): TaskDocumentItem[] {
  return [
    {
      id: "prompt",
      title: `Task prompt${suffix}`,
      source: "prompt",
      subtitle: "acme/widgets · Prompt",
      markdown: `# Spec${suffix}\n\n${Array.from({ length: 40 }, (_, i) => `Paragraph ${i}`).join("\n\n")}`,
      lineCount: 80,
    },
    {
      id: "pr:docs/spec.md",
      title: "spec.md",
      source: "pr",
      path: "docs/spec.md",
      markdown: `# PR Doc${suffix}\n\n${Array.from({ length: 40 }, (_, i) => `Section ${i}`).join("\n\n")}`,
      lineCount: 80,
    },
  ];
}

@Component({
  imports: [TaskDocsTabComponent],
  template: `
    <g-task-docs-tab [documents]="documents()" [(selectedId)]="selectedId" />
  `,
})
class HostComponent {
  readonly documents = signal<TaskDocumentItem[]>(makeDocs());
  readonly selectedId = signal<string | null>(null);
}

describe("TaskDocsTabComponent", () => {
  async function setup(): Promise<ComponentFixture<HostComponent>> {
    await TestBed.configureTestingModule({
      imports: [HostComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(HostComponent);
    fixture.detectChanges();
    await fixture.whenStable();
    return fixture;
  }

  it("preserves reader scrollTop when documents refresh with new objects for the same active id", async () => {
    const fixture = await setup();
    const el = fixture.nativeElement as HTMLElement;
    const scroller = el.querySelector<HTMLElement>(
      '[data-testid="docs-reader-scroll"]',
    )!;
    expect(scroller).not.toBeNull();

    scroller.scrollTop = 180;
    expect(scroller.scrollTop).toBe(180);

    // Simulate background task polling rebuilding TaskDocumentItem objects for the same doc id.
    fixture.componentInstance.documents.set(makeDocs(" polled"));
    fixture.detectChanges();
    await fixture.whenStable();

    expect(scroller.scrollTop).toBe(180);
  });

  it("resets reader scrollTop to 0 when switching documents and after an empty refresh", async () => {
    const fixture = await setup();
    const el = fixture.nativeElement as HTMLElement;
    const scroller = el.querySelector<HTMLElement>(
      '[data-testid="docs-reader-scroll"]',
    )!;
    scroller.scrollTop = 240;

    el.querySelector<HTMLButtonElement>(
      '[data-testid="doc-item-pr:docs/spec.md"]',
    )!.click();
    fixture.detectChanges();
    await fixture.whenStable();

    expect(fixture.componentInstance.selectedId()).toBe("pr:docs/spec.md");
    expect(scroller.scrollTop).toBe(0);

    scroller.scrollTop = 120;
    fixture.componentInstance.documents.set([]);
    fixture.detectChanges();
    await fixture.whenStable();

    fixture.componentInstance.documents.set(makeDocs());
    fixture.detectChanges();
    await fixture.whenStable();

    const scrollerAfter = el.querySelector<HTMLElement>(
      '[data-testid="docs-reader-scroll"]',
    )!;
    expect(scrollerAfter.scrollTop).toBe(0);
  });
});
