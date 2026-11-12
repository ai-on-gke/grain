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
  ElementRef,
  NgZone,
  OnDestroy,
  ViewEncapsulation,
  computed,
  inject,
  input,
  output,
  signal,
  viewChild,
} from "@angular/core";
import { LEVELS, levelOf } from "../tasks/task-view";

export type PriorityTone =
  "prio-urgent" | "prio-high" | "prio-normal" | "prio-low" | "prio-lowest";

export function priorityTone(p: number): PriorityTone {
  if (p >= 9) return "prio-urgent";
  if (p >= 7) return "prio-high";
  if (p >= 5) return "prio-normal";
  if (p >= 3) return "prio-low";
  return "prio-lowest";
}

export function priorityIconPath(p: number): string {
  // Urgent 9: double-up chevron
  if (p >= 9) {
    return "M7.4 17.4 6 16l6-6 6 6-1.4 1.4-4.6-4.6zm0-6L6 10l6-6 6 6-1.4 1.4L12 6.8z";
  }
  // High 7-8: single-up chevron
  if (p >= 7) {
    return "M7.4 15.4 6 14l6-6 6 6-1.4 1.4-4.6-4.6z";
  }
  // Normal 5-6: equal bars
  if (p >= 5) {
    return "M5 9h14v2H5zm0 4h14v2H5z";
  }
  // Low 3-4 & Lowest 1-2: down chevron
  return "M7.4 8.6 12 13.2l4.6-4.6L18 10l-6 6-6-6z";
}

@Component({
  selector: "g-priority-chip",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./pill-select.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(focusout)": "onFocusOut($event)",
    "(keydown)": "onWrapKeydown($event)",
  },
  template: `
    <span #wrapEl class="prio-wrap">
      @if (editable()) {
        <button
          #btnEl
          type="button"
          [class]="'prio-chip ' + tone()"
          [disabled]="disabled()"
          [attr.aria-label]="
            'Priority ' + lvl().name + ' ' + priority() + ', change'
          "
          [attr.aria-expanded]="open()"
          [attr.aria-controls]="menuId()"
          [attr.data-testid]="testid() || null"
          (click)="toggle($event)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="iconPath()" fill="currentColor" />
          </svg>
          <span>{{ lvl().name }} {{ priority() }}</span>
          <svg
            class="prio-chevron"
            width="12"
            height="12"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <path d="m7 10 5 5 5-5z" fill="currentColor" />
          </svg>
        </button>

        @if (open()) {
          <ul
            #menuEl
            class="pop-menu pill-select-menu"
            [class.align-left]="alignLeft()"
            [id]="menuId()"
            role="listbox"
            aria-label="Set priority"
            [attr.data-testid]="menuTestid() || null"
          >
            @for (l of levels; track l.value) {
              @let active = l.value === priority();
              <li>
                <button
                  type="button"
                  role="option"
                  [attr.aria-selected]="active"
                  [attr.aria-current]="active ? 'true' : null"
                  [attr.data-testid]="
                    menuTestid() ? menuTestid() + '-' + l.value : null
                  "
                  (click)="choose(l.value, $event)"
                >
                  <span [class]="'prio-dot ' + toneFor(l.value)">
                    <svg
                      width="12"
                      height="12"
                      viewBox="0 0 24 24"
                      aria-hidden="true"
                    >
                      <path [attr.d]="pathFor(l.value)" fill="currentColor" />
                    </svg>
                  </span>
                  <span class="pill-opt-label">{{ l.name }} {{ l.value }}</span>
                  @if (active) {
                    <svg
                      class="pill-check"
                      width="14"
                      height="14"
                      viewBox="0 0 24 24"
                      aria-hidden="true"
                    >
                      <path
                        d="M9 16.17 4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"
                        fill="currentColor"
                      />
                    </svg>
                  }
                </button>
              </li>
            }
          </ul>
        }
      } @else {
        <span
          [class]="'prio-chip static ' + tone()"
          [attr.data-testid]="testid() || null"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="iconPath()" fill="currentColor" />
          </svg>
          <span>{{ lvl().name }} {{ priority() }}</span>
        </span>
      }
    </span>
  `,
})
export class PriorityChipComponent implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly priority = input.required<number>();
  readonly editable = input<boolean>(false);
  readonly disabled = input<boolean>(false);
  readonly testid = input<string>("");
  readonly menuTestid = input<string>("");

  readonly priorityChange = output<number>();

  protected readonly levels = LEVELS;
  protected readonly open = signal(false);
  protected readonly alignLeft = signal(false);

  private readonly wrapEl = viewChild<ElementRef<HTMLElement>>("wrapEl");
  private readonly btnEl = viewChild<ElementRef<HTMLButtonElement>>("btnEl");
  private readonly menuEl = viewChild<ElementRef<HTMLElement>>("menuEl");

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.open()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.close(true));
    }
  };

  protected readonly lvl = computed(() => levelOf(this.priority()));
  protected readonly tone = computed(() => priorityTone(this.priority()));
  protected readonly iconPath = computed(() =>
    priorityIconPath(this.priority()),
  );
  protected readonly menuId = computed(
    () => this.menuTestid() || `prio-menu-${this.testid() || "x"}`,
  );

  protected toneFor = priorityTone;
  protected pathFor = priorityIconPath;

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    if (this.disabled()) return;
    if (this.open()) this.close(false);
    else this.openMenu();
  }

  private openMenu(): void {
    const btn = this.btnEl()?.nativeElement;
    if (btn && typeof btn.getBoundingClientRect === "function") {
      const rect = btn.getBoundingClientRect();
      const clip = btn.closest(".queue-pane, .ws-insp, .task-page");
      const leftBound = clip ? clip.getBoundingClientRect().left : 0;
      this.alignLeft.set(rect.left - leftBound < 190);
    } else {
      this.alignLeft.set(false);
    }
    this.open.set(true);
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    queueMicrotask(() => {
      const items = this.optionButtons();
      const idx = LEVELS.findIndex((l) => l.value === this.priority());
      (items[idx >= 0 ? idx : 0] ?? items[0])?.focus();
    });
  }

  private close(restoreFocus: boolean): void {
    this.open.set(false);
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    if (restoreFocus) {
      this.btnEl()?.nativeElement.focus();
    }
  }

  protected choose(val: number, ev: Event): void {
    ev.stopPropagation();
    this.close(true);
    this.priorityChange.emit(val);
  }

  protected onDocClick(ev: Event): void {
    if (!this.open()) return;
    const t = ev.target as Node | null;
    if (t && !this.wrapEl()?.nativeElement.contains(t)) {
      this.close(false);
    }
  }

  protected onFocusOut(ev: FocusEvent): void {
    if (!this.open()) return;
    const next = ev.relatedTarget as Node | null;
    if (next && !this.wrapEl()?.nativeElement.contains(next)) {
      this.close(false);
    }
  }

  protected onWrapKeydown(ev: KeyboardEvent): void {
    if (!this.open()) {
      if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
        ev.preventDefault();
        this.openMenu();
      }
      return;
    }
    const items = this.optionButtons();
    if (!items.length) return;
    const cur = items.indexOf(this.doc.activeElement as HTMLButtonElement);
    let next = -1;
    if (ev.key === "ArrowDown") next = (cur + 1) % items.length;
    else if (ev.key === "ArrowUp")
      next = (cur - 1 + items.length) % items.length;
    else if (ev.key === "Home") next = 0;
    else if (ev.key === "End") next = items.length - 1;
    if (next >= 0) {
      ev.preventDefault();
      ev.stopPropagation();
      items[next]?.focus();
    }
  }

  private optionButtons(): HTMLButtonElement[] {
    const m = this.menuEl()?.nativeElement;
    return m ? Array.from(m.querySelectorAll<HTMLButtonElement>("button")) : [];
  }
}
