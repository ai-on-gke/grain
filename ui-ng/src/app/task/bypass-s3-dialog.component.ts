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
import type { TaskDetail } from "../api/models";

// BypassS3Request is a human operator's explicit override of a blocked S3
// pre-PR staging gate (POST /api/tasks/{id}/qa/bypass-s3): it promotes
// grain-qa-stage/<id> to the task's PR branch, opens the pull request, and
// records an auditable qa:s3-operator-bypass check run on the promoted commit.
export interface BypassS3Request {
  task: TaskDetail;
  stageBranch?: string;
  head?: string;
  run: (reason: string) => Promise<boolean>;
}

const MAX_REASON = 500;

@Component({
  selector: "g-bypass-s3-dialog",
  imports: [],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["../shared/work-surface.css", "../shared/surface-controls.css"],
  template: `
    @let req = request();
    <div class="modal-backdrop">
      <div
        #dialog
        class="modal-dialog g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="bypass-s3-title"
        data-testid="bypass-s3-dialog"
        (keydown.tab)="onTab($event)"
        (keydown.shift.tab)="onTab($event)"
      >
        <h2 id="bypass-s3-title">Bypass S3 gate &amp; open PR</h2>
        <p style="margin: 0 0 8px">
          Promote staged work for task <strong>#{{ req.task.id }}</strong> onto
          <code class="mono">{{ req.task.branch || "the task branch" }}</code>
          and open its pull request without waiting for S3 checks to pass.
        </p>
        <p class="muted" data-testid="bypass-s3-scope">
          This overrides the pre-PR S3 staging gate for this commit only. CI
          checks, pull request reviews, and merge gates still apply before the
          PR can merge. Your identity, commit SHA, and reason are recorded on
          the task.
        </p>
        @if (req.stageBranch || req.head) {
          <p class="muted mono" style="margin: 4px 0">
            @if (req.stageBranch) {
              <span>{{ req.stageBranch }}</span>
            }
            @if (req.stageBranch && req.head) {
              <span> · </span>
            }
            @if (req.head) {
              <span>commit {{ req.head.slice(0, 12) }}</span>
            }
          </p>
        }

        <label class="field">
          <span class="field-label">Reason for bypassing S3 (required)</span>
          <textarea
            #reasonInput
            name="bypassS3Reason"
            [value]="reason()"
            (input)="reason.set(reasonInput.value)"
            rows="3"
            [attr.maxlength]="maxReason"
            data-testid="bypass-s3-reason"
            placeholder="e.g. False positive on generic repository rule; verified diff manually"
          ></textarea>
        </label>

        <div class="action-btn-group modal-actions">
          <button
            type="button"
            class="btn-text"
            data-testid="bypass-s3-cancel"
            (click)="close(false)"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="bypass-s3-confirm"
            [disabled]="!reason().trim() || submitting()"
            (click)="close(true)"
          >
            Bypass S3 gate &amp; open PR
          </button>
        </div>
      </div>
    </div>
  `,
})
export class BypassS3DialogComponent {
  readonly request = input.required<BypassS3Request>();
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
        if (success) this.cleanup(true);
      })
      .finally(() => {
        this.submitting.set(false);
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
