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
  input,
  signal,
} from "@angular/core";
import {
  type ComposedField,
  type ComposedTask,
  type Composition,
  describeSuiteMode,
} from "./processes.models";

const LIST_FIELD_NAMES = new Set([
  "reads",
  "writes",
  "capabilities",
  "customFields",
]);
const BODY_CHAR_LIMIT = 600;
const BODY_LINE_LIMIT = 12;

@Component({
  selector: "g-composition-view",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (composition(); as comp) {
      <div class="comp-box" data-testid="composition-preview">
        <div class="comp-head">
          <strong>{{ heading() }}</strong>
          @if (comp.kind === "suite" && comp.mode) {
            <span class="ci-provider-tag">{{
              modeLabel(comp.mode, comp.count, comp.maxPasses)
            }}</span>
          }
          @if (comp.repo) {
            <span class="tabular muted"
              >{{ comp.repo }}{{ comp.base ? " @ " + comp.base : "" }}</span
            >
          }
        </div>
        @if (comp.tasks.length === 0) {
          <p class="muted" style="margin: 6px 0 0">
            Select a template or prompt to preview composed fields.
          </p>
        } @else {
          @for (t of comp.tasks; track $index) {
            <div class="comp-task">
              <div class="comp-task-title">
                <span class="tabular muted">#{{ $index + 1 }}</span>
                <strong>{{ t.title || "(untitled task)" }}</strong>
                @if (t.templateName) {
                  <span class="muted">· from {{ t.templateName }}</span>
                }
              </div>
              <dl class="comp-grid comp-meta-grid">
                @for (f of scalarFields(t); track f.name) {
                  <div class="comp-row" [attr.data-field]="f.name">
                    <dt>{{ f.name }}</dt>
                    <dd>
                      <span>{{
                        f.values && f.values.length > 0
                          ? f.values.join(", ")
                          : "—"
                      }}</span>
                      @if (f.source) {
                        <span class="comp-layer">{{ f.source }}</span>
                      }
                    </dd>
                  </div>
                }
                @for (f of listFields(t); track f.name) {
                  <div
                    class="comp-row comp-row-full"
                    [attr.data-field]="f.name"
                    style="grid-column: 1 / -1"
                  >
                    <dt>{{ f.name }}</dt>
                    <dd>
                      <span>{{ f.values!.join(", ") }}</span>
                      @if (f.source) {
                        <span class="comp-layer">{{ f.source }}</span>
                      }
                    </dd>
                  </div>
                }
                @if (bodyField(t); as body) {
                  <div
                    class="comp-row comp-row-full"
                    data-field="body"
                    style="grid-column: 1 / -1"
                  >
                    <dt class="comp-body-head">
                      <span>
                        body
                        @if (body.source) {
                          <span class="comp-layer">{{ body.source }}</span>
                        }
                      </span>
                      @if (isLongBody(body)) {
                        <button
                          type="button"
                          class="btn-text"
                          data-testid="composition-body-toggle"
                          [attr.aria-expanded]="isBodyExpanded($index)"
                          (click)="toggleBody($index)"
                        >
                          {{
                            isBodyExpanded($index)
                              ? "Collapse prompt"
                              : "Expand full prompt"
                          }}
                        </button>
                      }
                    </dt>
                    <dd>
                      <div
                        class="comp-body-well"
                        [class.is-clamped]="
                          isLongBody(body) && !isBodyExpanded($index)
                        "
                        [style.max-height]="
                          isLongBody(body) && !isBodyExpanded($index)
                            ? '320px'
                            : null
                        "
                        [style.overflow-y]="
                          isLongBody(body) && !isBodyExpanded($index)
                            ? 'auto'
                            : null
                        "
                        data-testid="composition-body-well"
                      >
                        {{ bodyText(body) || "—" }}
                      </div>
                    </dd>
                  </div>
                }
              </dl>
            </div>
          }
        }
      </div>
    }
  `,
})
export class CompositionViewComponent {
  readonly composition = input<Composition | null>(null);
  readonly heading = input<string>(
    "Composition preview (what next firing files)",
  );

  protected readonly modeLabel = describeSuiteMode;
  private readonly expandedBodies = signal<Record<number, boolean>>({});

  protected scalarFields(t: ComposedTask): ComposedField[] {
    const taskTitle = (t.title ?? "").trim();
    return (t.fields ?? []).filter((f) => {
      if (f.name === "body") return false;
      if (f.name === "title") {
        const val = (f.values ?? []).join(", ").trim();
        if (!val || val === taskTitle) return false;
      }
      if (LIST_FIELD_NAMES.has(f.name) && (f.values?.length ?? 0) > 0) {
        return false;
      }
      return true;
    });
  }

  protected listFields(t: ComposedTask): ComposedField[] {
    return (t.fields ?? []).filter(
      (f) => LIST_FIELD_NAMES.has(f.name) && (f.values?.length ?? 0) > 0,
    );
  }

  protected bodyField(t: ComposedTask): ComposedField | null {
    return (t.fields ?? []).find((f) => f.name === "body") ?? null;
  }

  protected bodyText(f: ComposedField): string {
    return f.values && f.values.length > 0 ? f.values.join("\n") : "";
  }

  protected isLongBody(f: ComposedField): boolean {
    const text = this.bodyText(f);
    if (!text) return false;
    return (
      text.length > BODY_CHAR_LIMIT ||
      text.split(/\r?\n/).length > BODY_LINE_LIMIT
    );
  }

  protected isBodyExpanded(taskIndex: number): boolean {
    return !!this.expandedBodies()[taskIndex];
  }

  protected toggleBody(taskIndex: number): void {
    this.expandedBodies.update((prev) => ({
      ...prev,
      [taskIndex]: !prev[taskIndex],
    }));
  }
}
