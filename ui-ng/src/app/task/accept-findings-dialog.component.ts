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

import { DOCUMENT } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  afterNextRender,
  inject,
  input,
  output,
  signal,
  viewChild,
  ViewEncapsulation,
} from "@angular/core";
import type { MergeGate, TaskDetail } from "../api/models";

// AcceptFindingsRequest is a person's decision to merge over one review's
// findings on one gate as they are (POST /api/tasks/{id}/accept-findings,
// ui.Client.AcceptReviewFindings): the step between Submit, which never
// clears findings, and Force merge, which overrides every gate it names.
export interface AcceptFindingsRequest {
  task: TaskDetail;
  gate: MergeGate;
  head: string | undefined;
  run: (reason: string) => Promise<boolean>;
}

// The server's bound on an acceptance's reason
// (model.MaxAcceptFindingsReasonLength).
const MAX_REASON = 500;

@Component({
  selector: "g-accept-findings-dialog",
  imports: [],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["../shared/work-surface.css", "../shared/surface-controls.css"],
  template: `
    @let c = request();
    <div class="modal-backdrop">
      <div
        #dialog
        class="modal-dialog g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="accept-findings-title"
        data-testid="accept-findings-dialog"
        (keydown.tab)="onTab($event)"
        (keydown.shift.tab)="onTab($event)"
      >
        <h2 id="accept-findings-title">
          Accept {{ c.gate.label || c.gate.id }} findings
        </h2>
        <p style="margin: 0 0 8px">
          {{
            c.gate.reviewTask
              ? "Review task " + c.gate.reviewTask
              : "The review"
          }}
          left findings on this gate{{ c.gate.detail ? ":" : "." }}
          @if (c.gate.detail) {
            <em>{{ c.gate.detail }}</em>
          }
        </p>
        <p class="muted" data-testid="accept-findings-scope">
          Accepting them lets this change merge without fixing them. It clears
          this review's findings on this gate only: CI, the other gates, any
          approval a trigger asks for and any security hold still apply. A later
          push is reviewed again, and new findings need a new decision. Your
          name, the review, the head and your reason are recorded on the task
          and the pull request.
        </p>
        @if (c.head) {
          <p class="muted mono" style="margin: 4px 0">
            At commit {{ c.head.slice(0, 12) }}
          </p>
        }

        <label class="field">
          <span class="field-label">Why it merges as it is (required)</span>
          <textarea
            #reasonInput
            name="acceptReason"
            [value]="reason()"
            (input)="reason.set(reasonInput.value)"
            rows="3"
            [attr.maxlength]="maxReason"
            data-testid="accept-findings-reason"
            placeholder="e.g. Out of scope for this change; follow-up filed to discuss the extensions"
          ></textarea>
        </label>

        <div class="action-btn-group modal-actions">
          <button
            #cancelBtn
            type="button"
            class="btn-text"
            data-testid="accept-findings-cancel"
            (click)="close(false)"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="accept-findings-confirm"
            [disabled]="!reason().trim() || submitting()"
            (click)="close(true)"
          >
            Accept findings
          </button>
        </div>
      </div>
    </div>
  `,
})
export class AcceptFindingsDialogComponent {
  readonly request = input.required<AcceptFindingsRequest>();
  readonly done = output<boolean>();

  protected readonly maxReason = MAX_REASON;
  protected readonly reason = signal("");
  protected readonly submitting = signal(false);

  private readonly dialog = viewChild<ElementRef<HTMLElement>>("dialog");
  private readonly reasonInput =
    viewChild<ElementRef<HTMLTextAreaElement>>("reasonInput");

  private readonly returnFocus =
    typeof document !== "undefined"
      ? (document.activeElement as HTMLElement | null)
      : null;

  private readonly doc = inject(DOCUMENT);
  private readonly destroyRef = inject(DestroyRef);
  private readonly onDocKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape") this.cancel(ev);
  };

  constructor() {
    this.doc.addEventListener("keydown", this.onDocKeydown, true);
    this.destroyRef.onDestroy(() =>
      this.doc.removeEventListener("keydown", this.onDocKeydown, true),
    );
    afterNextRender(() => this.reasonInput()?.nativeElement.focus());
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
    if (!ok) {
      this.cleanup(false);
      return;
    }
    const reason = this.reason().trim();
    if (!reason || this.submitting()) return;
    this.submitting.set(true);
    this.request()
      .run(reason)
      .then((success) => {
        this.submitting.set(false);
        if (success) this.cleanup(true);
      });
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
