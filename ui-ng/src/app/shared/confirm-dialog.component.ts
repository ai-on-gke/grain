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
  computed,
  inject,
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
  // Gerund phrase for multi-repo/owner confirm tick (defaults to "approving work").
  actionVerb?: string;
  run: () => Promise<unknown>;
}

// ConfirmDialogComponent asks before a call is made. A batch shows its
// count per repository (and per owner when there is more than one), and
// one that spans repositories needs an explicit second tick before the
// accept button turns on.
// bypassConfirm is the question both /next/ pages ask before letting a
// repository's base-branch repair PRs merge with no human: the same
// risk the classic Repo page and Settings -> Repositories put in their
// confirm (ui/src/components/RepoPage.jsx). Turning it off asks
// nothing. Without it one click on a switch took the human gate off
// (2026-11-13 repository walkthrough).
export function bypassConfirm(
  repo: string,
  run: () => Promise<unknown>,
): ConfirmRequest {
  return {
    title: "Let base-branch repairs merge without a human?",
    message:
      `Let base-branch repair PRs in ${repo} merge without a human when their CI is green? ` +
      `While ${repo}'s base branch is red, an agent's repair pull request can then land changes to ` +
      `.grain/workflows/** or trust-boundary code with no human sign-off before the merge. ` +
      `Review tasks are still filed afterwards.`,
    accept: "Turn on",
    destructive: true,
    run,
  };
}

@Component({
  selector: "g-confirm-dialog",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./work-surface.css", "./surface-controls.css"],
  template: `
    @let c = request();
    <div class="modal-backdrop">
      <div
        #dialog
        class="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        aria-describedby="confirm-body"
        data-testid="confirm-dialog"
        (keydown.tab)="onTab($event)"
        (keydown.shift.tab)="onTab($event)"
      >
        <h2 id="confirm-title" class="modal-title">{{ c.title }}</h2>
        <p id="confirm-body" class="modal-body">{{ c.message }}</p>
        @if (c.scope; as s) {
          <ul class="scope-list" data-testid="confirm-scope">
            @for (line of lines(); track line) {
              <li>{{ line }}</li>
            }
          </ul>
          @if (needsTick()) {
            <label class="check-line scope-check">
              <input
                #scopeCheck
                type="checkbox"
                data-testid="confirm-multi-repo"
                [checked]="ticked()"
                (change)="ticked.set(!ticked())"
              />
              {{ tickLabel() }}
            </label>
          }
        }
        <div class="action-btn-group modal-actions">
          <button
            #cancelBtn
            type="button"
            class="btn-text"
            data-testid="confirm-cancel"
            (click)="close(false)"
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
            (click)="close(true)"
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
    () =>
      !!(this.request().scope?.multiRepo || this.request().scope?.otherOwner),
  );
  protected readonly tickLabel = computed(() => {
    const s = this.request().scope;
    if (!s) return "";
    const verb = this.request().actionVerb || "approving work";
    if (s.multiRepo && s.otherOwner) {
      return `I am ${verb} in ${s.repos.length} repositories, including work owned by someone else`;
    }
    if (s.multiRepo) {
      return `I am ${verb} in ${s.repos.length} repositories`;
    }
    return `I am ${verb} owned by someone else`;
  });
  private readonly dialog = viewChild<ElementRef<HTMLElement>>("dialog");
  private readonly cancelBtn =
    viewChild<ElementRef<HTMLButtonElement>>("cancelBtn");
  private readonly acceptBtn =
    viewChild<ElementRef<HTMLButtonElement>>("acceptBtn");
  private readonly scopeCheck =
    viewChild<ElementRef<HTMLInputElement>>("scopeCheck");
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
    // Destructive actions focus Cancel so a hurried Enter does not commit.
    // Otherwise focus goes to the second tick when present, or Accept.
    afterNextRender(() =>
      (this.request().destructive
        ? this.cancelBtn()
        : (this.scopeCheck() ?? this.acceptBtn())
      )?.nativeElement.focus(),
    );
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
    const back = this.returnFocus;
    this.done.emit(ok);
    if (!ok && back?.isConnected) setTimeout(() => back.focus());
  }

  protected cancel(ev: Event): void {
    ev.preventDefault();
    ev.stopPropagation();
    this.close(false);
  }
}
