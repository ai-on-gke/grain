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

import { ChangeDetectionStrategy, Component, input } from "@angular/core";
import { type Composition, describeSuiteMode } from "./processes.models";

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
              <dl class="comp-grid">
                @for (f of t.fields; track f.name) {
                  <div class="comp-row">
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
}
