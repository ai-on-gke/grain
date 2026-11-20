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
  ViewEncapsulation,
  computed,
  inject,
  input,
  output,
} from "@angular/core";
import { AccessService } from "../services/access.service";
import { MergeGatesListComponent } from "./merge-gates-list.component";
import {
  autoMergeSentence,
  availableActions,
  whatsNeeded,
  type ActionViewer,
  type AnyTask,
  type TaskAction,
} from "./task-actions";
import { TipDirective } from "./tip.directive";

// The action area: the one place a task's actions live, the same on the
// task page, in the Inbox inspector and in the Agents, Sessions and
// Workstreams panes. Top to bottom: the "What's needed" sentence, why
// auto-merge went off (when grain turned it off), the merge gates with
// each blocking gate's own actions, whatever the host projects (the
// park cards that answer a run's request), and every other action
// availableActions allows now. The host carries the actions out: this
// says what they are and emits the one pressed.

@Component({
  selector: "g-task-action-area",
  imports: [MergeGatesListComponent, TipDirective],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .task-action-area {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 8px 12px;
      border-radius: var(--md-sys-shape-corner-medium);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface-container-lowest);
      font-size: 13px;
    }
    :root[data-theme="dark"] .task-action-area {
      background: var(--md-sys-color-surface-container);
    }
    .task-action-area .whats-needed {
      margin: 0;
      font-size: 14px;
      font-weight: 500;
      color: var(--md-sys-color-on-surface);
    }
    .task-action-area .whats-needed-label {
      font-weight: 600;
      margin-right: 4px;
    }
    .task-action-area .auto-merge-off {
      margin: 0;
    }
    .task-action-row {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 4px;
    }
    /* One control height across the row, and a compact one: the area
       sits above the tabs, so in a short pane every row it wraps onto
       pushes the tabs further down. */
    .task-action-row > button {
      min-height: 32px;
      padding: 0 12px;
    }
    /* On a phone-width column the row stays one line and scrolls
       sideways, main actions first, rather than wrapping onto rows that
       push the task's content below the fold. It queries the task view's
       own container (never a new nested one, see task-detail.component.css)
       or, where the area has none, the viewport. */
    @container task-detail (max-width: 520px) {
      .task-action-row {
        flex-wrap: nowrap;
        overflow-x: auto;
        scrollbar-width: thin;
      }
      .task-action-row > * {
        flex: none;
      }
    }
    @media (max-width: 520px) {
      .task-action-row {
        flex-wrap: nowrap;
        overflow-x: auto;
        scrollbar-width: thin;
      }
      .task-action-row > * {
        flex: none;
      }
    }
    .task-action-row .task-action-divider {
      width: 1px;
      align-self: stretch;
      margin: 0 4px;
      background: var(--md-sys-color-outline-variant);
    }
    .task-action-area .task-gates-stack {
      padding: 0;
    }
    .task-action-area .submit-off-reason {
      margin: 0;
    }
  `,
  template: `
    <section
      class="task-action-area"
      data-testid="task-action-area"
      aria-label="What's needed"
    >
      <p class="whats-needed" data-testid="whats-needed" role="status">
        <span class="whats-needed-label">What's needed:</span>{{ sentence() }}
      </p>
      @if (autoMergeNote(); as note) {
        <p
          class="banner banner-info auto-merge-off"
          data-testid="auto-merge-off"
        >
          {{ note }}
        </p>
      }
      @if (task().mergeGates?.length) {
        <g-merge-gates-list
          [task]="task()"
          [actions]="actions()"
          [busy]="busy()"
          (run)="run.emit($event)"
        />
      }
      <ng-content />
      @if (rowActions().length) {
        <div
          class="task-action-row"
          role="group"
          aria-label="Task actions"
          data-testid="task-actions"
        >
          @for (a of primary(); track a.id) {
            <button
              type="button"
              [class]="cls(a)"
              [attr.data-testid]="testidPrefix() + a.id"
              [attr.data-action]="a.id"
              [gTip]="a.disabled || a.tip || ''"
              [disabled]="!!a.disabled || busy()"
              (click)="run.emit(a)"
            >
              {{ a.label }}
            </button>
          }
          @if (primary().length && secondary().length) {
            <span class="task-action-divider" aria-hidden="true"></span>
          }
          @for (a of secondary(); track a.id) {
            <button
              type="button"
              [class]="cls(a)"
              [attr.data-testid]="testidPrefix() + a.id"
              [attr.data-action]="a.id"
              [gTip]="a.tip || ''"
              [disabled]="busy()"
              (click)="run.emit(a)"
            >
              {{ a.label }}
            </button>
          }
        </div>
      }
      @if (submitOff(); as why) {
        <p class="muted submit-off-reason" data-testid="submit-off-reason">
          {{ why }}
        </p>
      }
    </section>
  `,
})
export class TaskActionAreaComponent {
  private readonly access = inject(AccessService);

  readonly task = input.required<AnyTask>();
  readonly busy = input(false);
  // Actions this surface has no way to carry out (the Inbox has no
  // inline composer for Compact, say) are left out by id.
  readonly omit = input<readonly string[]>([]);
  // The buttons' data-testid prefix: "act-" on the task page, "action-"
  // in the Inbox dock, where its keyboard help and tests name them.
  readonly testidPrefix = input("act-");
  readonly run = output<TaskAction>();

  private readonly viewer = computed<ActionViewer>(() => {
    const fullAccess = this.access.isFullAccess();
    this.access.rawConfig();
    return {
      can: (route, repo) => this.access.canAccessTask(route, repo ?? null),
      fullAccess,
    };
  });
  readonly actions = computed(() => {
    const omit = new Set(this.omit());
    return availableActions(this.task(), this.viewer()).filter(
      (a) => !omit.has(a.id),
    );
  });
  protected readonly sentence = computed(() =>
    whatsNeeded(this.task(), this.viewer()),
  );
  protected readonly autoMergeNote = computed(() =>
    autoMergeSentence(this.task()),
  );
  // An action drawn on its gate's row is not drawn again below.
  protected readonly rowActions = computed(() => {
    const gates = new Set((this.task().mergeGates ?? []).map((g) => g.id));
    return this.actions().filter((a) => !a.gate || !gates.has(a.gate));
  });
  protected readonly primary = computed(() =>
    this.rowActions().filter((a) => !a.secondary),
  );
  protected readonly secondary = computed(() =>
    this.rowActions().filter((a) => a.secondary),
  );
  protected readonly submitOff = computed(
    () => this.actions().find((a) => a.id === "submit")?.disabled ?? "",
  );

  protected cls(a: TaskAction): string {
    const base =
      a.variant === "primary"
        ? "btn-primary"
        : a.variant === "tonal"
          ? "btn-tonal"
          : "btn-text";
    return a.danger ? `${base} danger` : base;
  }
}
