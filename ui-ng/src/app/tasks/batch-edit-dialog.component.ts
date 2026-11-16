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
  computed,
  input,
  type OnInit,
  output,
  signal,
  ViewEncapsulation,
} from "@angular/core";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import {
  PillSelectComponent,
  type PillOption,
} from "../shared/pill-select.component";

// BatchEditRequest opens the selection bar's one form: Bulk edit (a few
// task fields at once) or a capability to attach or detach. count is
// how many tasks the result will be sent to.
export interface BatchEditRequest {
  mode: "edit" | "capability";
  count: number;
  // Edit: the values the selection already agrees on, "" where it differs.
  priority?: string;
  base?: string;
  autoMerge?: "on" | "off" | "";
  // Capability: what may be picked.
  capabilities?: readonly PillOption[];
}

export type BatchEditResult =
  | { mode: "edit"; patch: Record<string, unknown> }
  | { mode: "capability"; capability: string; attach: boolean };

// taskPriority is a Task priority box's value as a number: a whole
// number from 1 up (default 5, higher runs first), else NaN -- the rule
// the task pane's own priority box and classic Bulk edit hold to.
export function taskPriority(text: string): number {
  const s = text.trim();
  if (!/^\d+$/.test(s)) return NaN;
  const n = Number(s);
  return n >= 1 && Number.isSafeInteger(n) ? n : NaN;
}

// BatchEditDialogComponent sends nothing itself: it hands back the PATCH
// body (only the ticked fields) or the capability and direction, and the
// page fans it out over the selection.
@Component({
  selector: "g-batch-edit-dialog",
  imports: [DialogTrapDirective, PillSelectComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["../shared/work-surface.css", "../shared/surface-controls.css"],
  template: `
    @let r = request();
    <div class="scrim" aria-hidden="true" (click)="done.emit(null)"></div>
    <div
      class="g-dialog batch-edit-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="batch-edit-title"
      data-testid="batch-edit-dialog"
      gDialogTrap
      (gDialogClose)="done.emit(null)"
    >
      @if (r.mode === "edit") {
        <h2 id="batch-edit-title">Edit {{ r.count }} tasks</h2>
        <p class="muted">
          Tick a field to set it on every selected task. Unticked fields are
          left as each task has them.
        </p>
        <label class="check-row batch-edit-tick">
          <input
            type="checkbox"
            data-testid="bulk-priority-tick"
            [checked]="change().has('priority')"
            (change)="tick('priority')"
          />
          <span>Task priority</span>
        </label>
        <label class="field">
          <span>A whole number, 1 or more; default 5, higher runs first</span>
          <input
            #pri
            type="text"
            inputmode="numeric"
            data-testid="bulk-priority"
            aria-label="Task priority"
            [value]="priority()"
            [attr.aria-invalid]="priorityInvalid() ? 'true' : null"
            (input)="priority.set(pri.value); mark('priority')"
          />
          @if (priorityInvalid()) {
            <span class="field-error" data-testid="bulk-priority-error"
              >Use a whole number, 1 or more.</span
            >
          }
        </label>
        <label class="check-row batch-edit-tick">
          <input
            type="checkbox"
            data-testid="bulk-base-tick"
            [checked]="change().has('base')"
            (change)="tick('base')"
          />
          <span>Base branch</span>
        </label>
        <label class="field">
          <span>Empty uses each repository's default branch</span>
          <input
            #baseIn
            type="text"
            data-testid="bulk-base"
            aria-label="Base branch"
            placeholder="main"
            [value]="base()"
            (input)="base.set(baseIn.value); mark('base')"
          />
        </label>
        <label class="check-row batch-edit-tick">
          <input
            type="checkbox"
            data-testid="bulk-automerge-tick"
            [checked]="change().has('autoMerge')"
            (change)="tick('autoMerge')"
          />
          <span>Auto-merge on green CI</span>
        </label>
        <div class="switch-row batch-edit-switch">
          <button
            type="button"
            role="switch"
            class="g-switch"
            aria-label="Auto-merge on green CI"
            data-testid="bulk-automerge"
            [attr.aria-checked]="autoMerge()"
            (click)="autoMerge.set(!autoMerge()); mark('autoMerge')"
          ></button>
          <span>{{ autoMerge() ? "On" : "Off" }}</span>
        </div>
        @if (r.count > 1) {
          <p class="muted batch-edit-note">
            A running task keeps the base its run started on and is counted as
            not changed.
          </p>
        }
      } @else {
        <h2 id="batch-edit-title">Capability on {{ r.count }} tasks</h2>
        <p class="muted">
          Attach grants it to every selected task; detach takes it away from
          those that hold it.
        </p>
        <div class="field">
          <span>Capability</span>
          <g-pill-select
            testid="bulk-capability"
            placeholder="Choose a capability"
            ariaLabel="Capability to attach or detach"
            align="left"
            [options]="r.capabilities ?? []"
            [value]="capability()"
            (valueChange)="capability.set($event)"
          />
        </div>
      }
      <div class="dialog-actions">
        <button
          type="button"
          class="btn-text"
          data-testid="batch-edit-cancel"
          (click)="done.emit(null)"
        >
          Cancel
        </button>
        @if (r.mode === "edit") {
          <button
            type="button"
            class="btn-primary"
            data-testid="batch-edit-apply"
            [disabled]="!applicable()"
            (click)="apply()"
          >
            Apply to {{ r.count }}
          </button>
        } @else {
          <button
            type="button"
            class="btn-text danger"
            data-testid="batch-capability-detach"
            [disabled]="!capability()"
            (click)="pick(false)"
          >
            Detach
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="batch-capability-attach"
            [disabled]="!capability()"
            (click)="pick(true)"
          >
            Attach
          </button>
        }
      </div>
    </div>
  `,
  styles: `
    .batch-edit-dialog .check-row {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-top: 16px;
      font-size: 14px;
    }
    .batch-edit-dialog .check-row + .field,
    .batch-edit-dialog .check-row + .switch-row {
      margin-top: 4px;
      margin-left: 26px;
    }
    .batch-edit-dialog .field-error {
      color: var(--md-sys-color-error);
    }
    .batch-edit-dialog .batch-edit-note {
      margin: 16px 0 0;
      font-size: 12px;
    }
  `,
})
export class BatchEditDialogComponent implements OnInit {
  readonly request = input.required<BatchEditRequest>();
  readonly done = output<BatchEditResult | null>();

  protected readonly change = signal<ReadonlySet<string>>(new Set());
  protected readonly priority = signal("");
  protected readonly base = signal("");
  protected readonly autoMerge = signal(false);
  protected readonly capability = signal("");

  protected readonly priorityInvalid = computed(
    () =>
      this.change().has("priority") &&
      Number.isNaN(taskPriority(this.priority())),
  );
  protected readonly applicable = computed(
    () => this.change().size > 0 && !this.priorityInvalid(),
  );

  ngOnInit(): void {
    const r = this.request();
    this.priority.set(r.priority ?? "");
    this.base.set(r.base ?? "");
    this.autoMerge.set(r.autoMerge === "on");
  }

  protected tick(field: string): void {
    this.change.update((s) => {
      const next = new Set(s);
      if (next.has(field)) next.delete(field);
      else next.add(field);
      return next;
    });
  }

  // Typing into a field ticks it: what was typed is what is meant.
  protected mark(field: string): void {
    if (!this.change().has(field)) this.tick(field);
  }

  protected apply(): void {
    if (!this.applicable()) return;
    const c = this.change();
    const patch: Record<string, unknown> = {};
    if (c.has("priority")) patch["priority"] = taskPriority(this.priority());
    if (c.has("base")) patch["base"] = this.base().trim();
    if (c.has("autoMerge")) patch["autoMerge"] = this.autoMerge();
    this.done.emit({ mode: "edit", patch });
  }

  protected pick(attach: boolean): void {
    const capability = this.capability();
    if (!capability) return;
    this.done.emit({ mode: "capability", capability, attach });
  }
}
