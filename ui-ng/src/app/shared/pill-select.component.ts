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

export interface PillOption {
  value: string;
  label: string;
  toneClass?: string;
}

let nextPillSelectId = 0;

@Component({
  selector: "g-pill-select",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./pill-select.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(focusout)": "onFocusOut($event)",
    "(keydown)": "onWrapKeydown($event)",
  },
  template: `
    <span #wrapEl class="pill-select-wrap">
      <button
        #btnEl
        type="button"
        class="pill-select-btn"
        [class.is-open]="open()"
        [class.is-inverse]="inverse()"
        [disabled]="disabled()"
        [attr.data-testid]="testid() || null"
        [attr.aria-label]="resolvedAriaLabel()"
        [attr.aria-expanded]="open()"
        [attr.aria-controls]="open() ? menuId() : null"
        aria-haspopup="listbox"
        (click)="toggle()"
      >
        @if (prefix()) {
          <span class="pill-select-prefix">{{ prefix() }}</span>
        }
        <span class="pill-select-label">{{ activeLabel() }}</span>
        <svg
          class="pill-select-chevron"
          width="14"
          height="14"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path d="m7 10 5 5 5-5z" fill="currentColor" />
        </svg>
      </button>

      @if (open()) {
        <ul
          #menuEl
          [id]="menuId()"
          class="pop-menu pill-select-menu"
          [class.align-left]="align() === 'left'"
          [class.open-up]="openUp()"
          role="listbox"
          [attr.aria-label]="ariaLabel() || prefix() || placeholder()"
          [attr.data-testid]="testid() ? testid() + '-menu' : null"
        >
          @for (opt of options(); track opt.value) {
            @let active = value() !== "" && opt.value === value();
            <li role="none">
              <button
                type="button"
                role="option"
                [attr.aria-selected]="active"
                [attr.data-testid]="
                  testid() ? testid() + '-opt-' + opt.value : null
                "
                (click)="choose(opt.value, $event)"
              >
                @if (opt.toneClass) {
                  <span
                    [class]="'prio-dot ' + opt.toneClass"
                    aria-hidden="true"
                  ></span>
                }
                <span class="pill-opt-label">{{ opt.label }}</span>
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
    </span>
  `,
})
export class PillSelectComponent implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly fallbackId = `pill-menu-auto-${++nextPillSelectId}`;

  readonly options = input.required<readonly PillOption[]>();
  readonly value = input<string>("");
  readonly placeholder = input<string>("");
  readonly prefix = input<string>("");
  readonly ariaLabel = input<string>("");
  readonly testid = input<string>("");
  readonly disabled = input<boolean>(false);
  readonly align = input<"left" | "right">("right");
  readonly openUp = input<boolean>(false);
  readonly inverse = input<boolean>(false);

  readonly valueChange = output<string>();

  protected readonly open = signal(false);
  protected readonly menuId = computed(() =>
    this.testid() ? `${this.testid()}-menu` : this.fallbackId,
  );

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

  protected readonly activeLabel = computed(() => {
    const cur = this.options().find((o) => o.value === this.value());
    return cur?.label ?? (this.placeholder() || this.options()[0]?.label || "");
  });

  protected readonly resolvedAriaLabel = computed(() => {
    const base = this.ariaLabel() || this.prefix() || this.placeholder();
    const act = this.activeLabel();
    if (!base) return act;
    return base.includes(act) ? base : `${base}: ${act}`;
  });

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected toggle(): void {
    if (this.disabled()) return;
    if (this.open()) {
      this.close(false);
    } else {
      this.openMenu();
    }
  }

  private openMenu(): void {
    this.open.set(true);
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    setTimeout(() => {
      if (!this.open()) return;
      const items = this.optionButtons();
      const idx = this.options().findIndex((o) => o.value === this.value());
      (items[idx >= 0 ? idx : 0] ?? items[0])?.focus();
    }, 0);
  }

  private close(restoreFocus: boolean): void {
    this.open.set(false);
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    if (restoreFocus) {
      this.btnEl()?.nativeElement.focus();
    }
  }

  protected choose(val: string, ev: Event): void {
    ev.stopPropagation();
    this.close(true);
    this.valueChange.emit(val);
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
    if (ev.key === "Tab") {
      this.close(false);
      return;
    }
    const items = this.optionButtons();
    if (!items.length) return;
    const cur = items.indexOf(this.doc.activeElement as HTMLButtonElement);
    let next = -1;
    if (ev.key === "ArrowDown") {
      next = cur < 0 ? 0 : (cur + 1) % items.length;
    } else if (ev.key === "ArrowUp") {
      next =
        cur < 0 ? items.length - 1 : (cur - 1 + items.length) % items.length;
    } else if (ev.key === "Home") {
      next = 0;
    } else if (ev.key === "End") {
      next = items.length - 1;
    }
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
