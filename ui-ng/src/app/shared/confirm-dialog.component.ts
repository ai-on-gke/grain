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
  computed,
  input,
  output,
  signal,
  viewChild,
  ViewEncapsulation,
} from "@angular/core";
import { type Scope, scopeLines } from "./scope";

export interface ConfirmRequest {
  title: string;
  message: string;
  accept: string;
  destructive: boolean;
  // For a batch: the repositories and owners it touches, counted when
  // the button was pressed.
  scope?: Scope;
  run: () => Promise<unknown>;
}

// ConfirmDialogComponent asks before a call is made. A batch shows its
// count per repository (and per owner when there is more than one), and
// one that spans repositories needs an explicit second tick before the
// accept button turns on.
@Component({
  selector: "g-confirm-dialog",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./work-surface.css",
  template: `
    @let c = request();
    <div class="modal-backdrop">
      <div
        class="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        aria-describedby="confirm-body"
        data-testid="confirm-dialog"
        (keydown.escape)="cancel($event)"
      >
        <h2 id="confirm-title" class="modal-title">{{ c.title }}</h2>
        <p id="confirm-body" class="modal-body">{{ c.message }}</p>
        @if (c.scope; as s) {
          <ul class="scope-list" data-testid="confirm-scope">
            @for (line of lines(); track line) {
              <li>{{ line }}</li>
            }
          </ul>
          @if (s.multiRepo) {
            <label class="check-line scope-check">
              <input
                #scopeCheck
                type="checkbox"
                data-testid="confirm-multi-repo"
                [checked]="ticked()"
                (change)="ticked.set(!ticked())"
              />
              I am approving work in {{ s.repos.length }} repositories
            </label>
          }
        }
        <div class="action-btn-group modal-actions">
          <button
            type="button"
            class="btn-text"
            data-testid="confirm-cancel"
            (click)="done.emit(false)"
          >
            Cancel
          </button>
          <button
            #acceptBtn
            type="button"
            [class]="
              c.destructive ? 'btn-primary is-destructive' : 'btn-primary'
            "
            data-testid="confirm-accept"
            [disabled]="needsTick() && !ticked()"
            (click)="done.emit(true)"
          >
            {{ c.accept }}
          </button>
        </div>
      </div>
    </div>
  `,
})
export class ConfirmDialogComponent {
  readonly request = input.required<ConfirmRequest>();
  // true when accepted, false when cancelled.
  readonly done = output<boolean>();

  protected readonly ticked = signal(false);
  protected readonly lines = computed(() => {
    const s = this.request().scope;
    return s ? scopeLines(s) : [];
  });
  protected readonly needsTick = computed(
    () => !!this.request().scope?.multiRepo,
  );
  private readonly acceptBtn =
    viewChild<ElementRef<HTMLButtonElement>>("acceptBtn");
  private readonly scopeCheck =
    viewChild<ElementRef<HTMLInputElement>>("scopeCheck");

  constructor() {
    // Focus goes to the first thing the person must do: the second tick
    // when there is one, otherwise the accept button.
    afterNextRender(() =>
      (this.scopeCheck() ?? this.acceptBtn())?.nativeElement.focus(),
    );
  }

  protected cancel(ev: Event): void {
    ev.preventDefault();
    ev.stopPropagation();
    this.done.emit(false);
  }
}
