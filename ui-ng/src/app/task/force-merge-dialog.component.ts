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
  afterNextRender,
  input,
  output,
  signal,
  viewChild,
  ViewEncapsulation,
  computed,
} from "@angular/core";
import type { TaskDetail, MergeGate } from "../api/models";

export interface ForceMergeRequest {
  task: TaskDetail;
  unsatisfiedGates: MergeGate[];
  head: string | undefined;
  run: (reason: string, overrideGateIds: string[], head: string | undefined) => Promise<boolean>;
}

@Component({
  selector: "g-force-merge-dialog",
  imports: [],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrl: "../shared/work-surface.css",
  template: `
    @let c = request();
    <div class="modal-backdrop">
      <div
        #dialog
        class="modal-dialog g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="force-merge-title"
        data-testid="force-merge-dialog"
        (keydown.escape)="cancel($event)"
        (keydown.tab)="onTab($event)"
        (keydown.shift.tab)="onTab($event)"
      >
        <h2 id="force-merge-title">Force merge #{{ c.task.id }}</h2>
        <p class="muted">
          Overrides merge-queue checks for the current head commit and records
          your reason permanently on the task and pull request.
        </p>

        @if (c.unsatisfiedGates.length) {
          <p style="margin: 8px 0 4px; font-size: 13px; font-weight: 500">
            Tick unsatisfied gates to override:
          </p>
          <ul class="force-gate-list">
            @for (g of c.unsatisfiedGates; track g.id) {
              <li>
                <label class="check-row">
                  <input
                    type="checkbox"
                    [attr.data-testid]="'override-gate-' + g.id"
                    [checked]="overrideGateIds().has(g.id)"
                    (change)="toggleOverrideGate(g.id)"
                  />
                  <span>{{ g.label || g.id }} ({{ g.state }})</span>
                </label>
              </li>
            }
          </ul>
        }

        <label class="field">
          <span class="field-label">Reason for force merge</span>
          <textarea
            #reasonInput
            name="forceReason"
            [value]="forceReason()"
            (input)="forceReason.set(reasonInput.value)"
            rows="3"
            data-testid="force-merge-reason"
            placeholder="e.g. Broken master, bypassing presubmit..."
          ></textarea>
        </label>

        <div class="action-btn-group modal-actions">
          <button
            #cancelBtn
            type="button"
            class="btn-text"
            data-testid="force-merge-cancel"
            (click)="close(false)"
          >
            Cancel
          </button>
          <button
            #acceptBtn
            type="button"
            class="btn-primary is-destructive"
            data-testid="force-merge-confirm"
            [disabled]="missingRequiredGateOverrides() || !forceReason().trim()"
            (click)="close(true)"
          >
            Force merge
          </button>
        </div>
      </div>
    </div>
  `,
})
export class ForceMergeDialogComponent {
  readonly request = input.required<ForceMergeRequest>();
  readonly done = output<boolean>();

  protected readonly forceReason = signal("");
  protected readonly overrideGateIds = signal<Set<string>>(new Set());
  
  protected readonly missingRequiredGateOverrides = computed(() => {
    const needed = this.request().unsatisfiedGates;
    const overrides = this.overrideGateIds();
    return needed.some((g) => g.id.includes("review") && !overrides.has(g.id));
  });

  private readonly dialog = viewChild<ElementRef<HTMLElement>>("dialog");
  private readonly reasonInput = viewChild<ElementRef<HTMLTextAreaElement>>("reasonInput");
  
  private readonly returnFocus =
    typeof document !== "undefined"
      ? (document.activeElement as HTMLElement | null)
      : null;

  constructor() {
    afterNextRender(() => {
      // Initialize checkboxes
      const pre = new Set<string>();
      for (const g of this.request().unsatisfiedGates) {
        if (!g.id.includes("review")) pre.add(g.id);
      }
      this.overrideGateIds.set(pre);

      const el = this.reasonInput()?.nativeElement;
      if (el) el.focus();
      else {
          const focusables = this.dialog()?.nativeElement.querySelectorAll<HTMLElement>(
            'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
          );
          if (focusables && focusables.length > 0) focusables[0].focus();
      }
    });
  }

  protected toggleOverrideGate(gateId: string): void {
    this.overrideGateIds.update((s) => {
      const next = new Set(s);
      if (next.has(gateId)) next.delete(gateId);
      else next.add(gateId);
      return next;
    });
  }

  protected onTab(ev: Event): void {
    const kev = ev as KeyboardEvent;
    const box = this.dialog()?.nativeElement;
    if (!box) return;
    const items = Array.from(
      box.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (!items.length) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (kev.shiftKey && (active === first || !box.contains(active))) {
      kev.preventDefault();
      last.focus();
    } else if (!kev.shiftKey && (active === last || !box.contains(active))) {
      kev.preventDefault();
      first.focus();
    }
  }

  protected close(ok: boolean): void {
    if (ok) {
        this.request().run(this.forceReason().trim(), [...this.overrideGateIds()], this.request().head).then((success) => {
            if (success) {
                this.cleanup(true);
            }
        });
    } else {
        this.cleanup(false);
    }
  }
  
  private cleanup(ok: boolean): void {
    const back = this.returnFocus;
    this.done.emit(ok);
    if (back?.isConnected) setTimeout(() => back.focus());
  }

  protected cancel(ev: Event): void {
    ev.preventDefault();
    ev.stopPropagation();
    this.cleanup(false);
  }
}
