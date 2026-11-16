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

import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  ViewEncapsulation,
  computed,
  effect,
  input,
  model,
  output,
  viewChild,
} from "@angular/core";
import { MarkdownDirective } from "../shared/code-highlight";
import { TipDirective } from "../shared/tip.directive";
import type { TaskDocumentItem, TaskDocumentSource } from "./task-docs";

@Component({
  selector: "g-task-docs-tab",
  imports: [MarkdownDirective, TipDirective],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-docs-tab.css",
  template: `
    @if (documents().length === 0) {
      <div class="docs-empty-state" data-testid="task-docs-empty">
        @if (loading()) {
          <p class="muted" data-testid="task-docs-loading">
            Loading task documents…
          </p>
        } @else {
          <p class="muted">
            No prompt, PR Markdown files, or report documents on this task yet.
          </p>
        }
      </div>
    } @else {
      <div class="docs-shell" data-testid="task-docs-shell">
        <aside class="docs-nav" aria-label="Task documents">
          <div class="docs-nav-head">
            <span class="docs-nav-heading">Documents</span>
            <span class="tab-count tabular">{{ documents().length }}</span>
          </div>
          <div class="docs-list" role="listbox" aria-label="Select document">
            @for (doc of documents(); track doc.id) {
              <button
                type="button"
                role="option"
                class="docs-item"
                [class.is-selected]="activeDoc()?.id === doc.id"
                [attr.aria-selected]="activeDoc()?.id === doc.id"
                [attr.data-testid]="'doc-item-' + doc.id"
                (click)="selectDoc(doc.id)"
              >
                <span class="docs-item-top">
                  <span
                    class="docs-source-pill"
                    [attr.data-source]="doc.source"
                    >{{ sourceLabel(doc.source) }}</span
                  >
                  <span class="docs-item-lines tabular"
                    >{{ doc.lineCount }}L</span
                  >
                </span>
                <span class="docs-item-title">{{ doc.title }}</span>
                @if (doc.path || doc.subtitle) {
                  <span class="docs-item-sub">{{
                    doc.path || doc.subtitle
                  }}</span>
                }
              </button>
            }
          </div>
        </aside>

        @if (activeDoc(); as doc) {
          <section class="docs-reader-pane" aria-label="Document reader">
            <header class="docs-reader-head">
              <div class="docs-reader-meta">
                <div class="docs-reader-badges">
                  <span
                    class="docs-source-pill"
                    [attr.data-source]="doc.source"
                    >{{ sourceLabel(doc.source) }}</span
                  >
                  @if (doc.path) {
                    <span class="docs-reader-path mono">{{ doc.path }}</span>
                  } @else if (doc.subtitle) {
                    <span class="muted docs-reader-sub">{{
                      doc.subtitle
                    }}</span>
                  }
                  <span class="muted tabular">{{ doc.lineCount }} lines</span>
                </div>
                <h2 class="docs-reader-title" data-testid="docs-reader-title">
                  {{ doc.title }}
                </h2>
              </div>
              <button
                type="button"
                class="btn-tonal docs-fullscreen-btn"
                data-testid="docs-fullscreen"
                aria-label="Open document full screen"
                gTip="Read full screen"
                (click)="openFullscreen.emit(doc.id)"
              >
                <span aria-hidden="true">⤢</span> Full screen
              </button>
            </header>

            <div
              #readerScroll
              class="docs-reader-scroll"
              tabindex="0"
              role="region"
              aria-label="Document content"
              data-testid="docs-reader-scroll"
            >
              <article
                class="docs-reader-article msg-body"
                [gMarkdown]="doc.markdown"
                data-testid="docs-reader-body"
              ></article>
            </div>
          </section>
        }
      </div>
    }
  `,
})
export class TaskDocsTabComponent {
  readonly documents = input.required<readonly TaskDocumentItem[]>();
  readonly loading = input(false);
  readonly selectedId = model<string | null>(null);
  readonly openFullscreen = output<string>();

  private readonly readerScroll =
    viewChild<ElementRef<HTMLElement>>("readerScroll");

  protected readonly activeDoc = computed<TaskDocumentItem | null>(() => {
    const list = this.documents();
    if (list.length === 0) return null;
    const id = this.selectedId();
    return (id ? list.find((d) => d.id === id) : undefined) ?? list[0];
  });

  private readonly activeDocId = computed(() => this.activeDoc()?.id ?? null);
  private lastResetDocId: string | null = null;

  constructor() {
    effect(() => {
      const id = this.activeDocId();
      const el = this.readerScroll()?.nativeElement;
      if (!id) {
        this.lastResetDocId = null;
        return;
      }
      if (!el || id === this.lastResetDocId) return;
      this.lastResetDocId = id;
      el.scrollTop = 0;
    });
  }

  protected selectDoc(id: string): void {
    this.selectedId.set(id);
  }

  protected sourceLabel(source: TaskDocumentSource): string {
    switch (source) {
      case "prompt":
        return "Prompt";
      case "pr":
        return "PR diff";
      case "comment":
        return "Report";
    }
  }
}
