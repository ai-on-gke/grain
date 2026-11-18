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
  ViewEncapsulation,
  inject,
  input,
  signal,
  viewChild,
} from "@angular/core";

let nextId = 0;

// InfoPopover is an (i) button whose explanation opens on click -- and
// so on Enter and Space, the button's own keys -- rather than on hover,
// which neither a keyboard nor a phone has (the classic InfoPopover). It
// closes on Escape, handing focus back to the button, on a second press
// and on a click anywhere else.
@Component({
  selector: "g-info-popover",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styles: `
    g-info-popover {
      position: relative;
      display: inline-flex;
      vertical-align: middle;
    }
    .g-info-btn {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 24px;
      height: 24px;
      padding: 0;
      border: none;
      border-radius: var(--md-sys-shape-corner-full);
      background: transparent;
      color: var(--md-sys-color-on-surface-variant);
      cursor: pointer;
    }
    .g-info-btn:hover,
    .g-info-btn[aria-expanded="true"] {
      background: var(--md-sys-color-surface-container-high);
      color: var(--md-sys-color-on-surface);
    }
    .g-info-btn:focus-visible {
      outline: 2px solid var(--md-sys-color-primary);
      outline-offset: 1px;
    }
    .g-info-pop {
      position: absolute;
      top: calc(100% + 4px);
      left: 0;
      z-index: 30;
      width: max-content;
      max-width: min(360px, calc(100vw - 48px));
      padding: 8px 12px;
      border-radius: var(--md-sys-shape-corner-sm, 8px);
      background: var(--md-sys-color-inverse-surface);
      color: var(--md-sys-color-inverse-on-surface);
      box-shadow: var(--md-sys-elevation-dialog);
      font-size: 13px;
      line-height: 18px;
      font-weight: 400;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
      text-align: left;
    }
    g-info-popover.is-end .g-info-pop {
      left: auto;
      right: 0;
    }
  `,
  host: { "[class.is-end]": "end()" },
  template: `
    <button
      #btn
      type="button"
      class="g-info-btn"
      [attr.aria-label]="'About ' + label()"
      [attr.aria-expanded]="open()"
      [attr.aria-controls]="popId"
      [attr.data-testid]="testid() || null"
      (click)="toggle($event)"
      (keydown.escape)="close($event)"
    >
      <svg
        width="18"
        height="18"
        viewBox="0 0 24 24"
        aria-hidden="true"
        fill="currentColor"
      >
        <path
          d="M11 17h2v-6h-2v6zm1-15C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8zM11 9h2V7h-2v2z"
        />
      </svg>
    </button>
    @if (open()) {
      <div
        #pop
        class="g-info-pop"
        role="note"
        [style.transform]="shift() ? 'translateX(' + shift() + 'px)' : null"
        [id]="popId"
        [attr.data-testid]="testid() ? testid() + '-pop' : null"
      >
        {{ text() }}
      </div>
    }
  `,
})
export class InfoPopoverComponent {
  readonly text = input.required<string>();
  readonly label = input("this");
  readonly testid = input<string>("");
  // Open towards the left, for a button at the end of a line.
  readonly end = input(false);

  protected readonly open = signal(false);
  // How far the open popover is moved sideways to stay inside the
  // viewport, measured once it has rendered.
  protected readonly shift = signal(0);
  private readonly pop = viewChild<ElementRef<HTMLElement>>("pop");
  protected readonly popId = `g-info-${++nextId}`;
  private readonly btn = viewChild<ElementRef<HTMLButtonElement>>("btn");
  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly doc = inject(DOCUMENT);

  private readonly onDocPointer = (ev: Event): void => {
    if (!this.host.nativeElement.contains(ev.target as Node)) {
      this.open.set(false);
    }
  };
  private readonly onDocKey = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.open()) this.close(ev);
  };

  constructor() {
    this.doc.addEventListener("pointerdown", this.onDocPointer, true);
    this.doc.addEventListener("keydown", this.onDocKey, true);
    inject(DestroyRef).onDestroy(() => {
      this.doc.removeEventListener("pointerdown", this.onDocPointer, true);
      this.doc.removeEventListener("keydown", this.onDocKey, true);
    });
  }

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    this.shift.set(0);
    this.open.set(!this.open());
    if (this.open()) setTimeout(() => this.fit());
  }

  private fit(): void {
    const el = this.pop()?.nativeElement;
    const vw = this.doc.documentElement.clientWidth;
    if (!el || !vw) return;
    const r = el.getBoundingClientRect();
    const margin = 8;
    if (r.right > vw - margin) {
      this.shift.set(Math.max(margin - r.left, vw - margin - r.right));
    } else if (r.left < margin) {
      this.shift.set(margin - r.left);
    }
  }

  protected close(ev: Event): void {
    if (!this.open()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.open.set(false);
    this.btn()?.nativeElement.focus();
  }
}
