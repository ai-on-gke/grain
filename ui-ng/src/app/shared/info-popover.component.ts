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
  signal,
  viewChild,
} from "@angular/core";

let nextInfoPopoverId = 0;

// InfoPopoverComponent renders an (i) icon button whose popover holds a
// field, method, or server description. It opens on hover, keyboard focus,
// click, Enter, or Space. Clicking or pressing Enter/Space pins a
// transiently-open popover instead of closing what focus just opened; a
// second activation, Escape, blur outside, or clicking outside closes it.
@Component({
  selector: "g-info-popover",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./info-popover.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(focusout)": "onFocusOut($event)",
  },
  template: `
    @if (title()) {
      <span #wrapEl class="g-info-wrap">
        <button
          #btnEl
          type="button"
          class="g-info-btn"
          [attr.aria-label]="ariaLabel()"
          [attr.aria-expanded]="open()"
          [attr.aria-describedby]="open() ? tipId : null"
          [attr.data-testid]="testId() || 'info-popover'"
          [attr.data-info-for]="cleanLabel() || null"
          (mouseenter)="onMouseEnter()"
          (mouseleave)="onMouseLeave()"
          (focus)="onFocus()"
          (click)="onClick($event)"
          (keydown)="onKeyDown($event)"
          (keyup)="onKeyUp($event)"
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <circle cx="12" cy="12" r="10" />
            <line x1="12" y1="16" x2="12" y2="12" />
            <line x1="12" y1="8" x2="12.01" y2="8" />
          </svg>
        </button>
        @if (open()) {
          <span
            #panelEl
            [id]="tipId"
            role="tooltip"
            class="g-info-popover-panel"
            [style.top.px]="posTop()"
            [style.left.px]="posLeft()"
            (mouseenter)="onMouseEnter()"
            (mouseleave)="onMouseLeave()"
            >{{ title() }}</span
          >
        }
      </span>
    }
  `,
})
export class InfoPopoverComponent implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly title = input<string | null | undefined>("");
  readonly label = input<string | null | undefined>("");
  readonly testId = input<string>("");

  protected readonly tipId = `g-info-tip-${++nextInfoPopoverId}`;

  private readonly hoverOpen = signal(false);
  private readonly focusOpen = signal(false);
  private readonly pinned = signal(false);

  protected readonly open = computed(
    () => this.pinned() || this.hoverOpen() || this.focusOpen(),
  );

  protected readonly posTop = signal(0);
  protected readonly posLeft = signal(12);

  private readonly wrapEl = viewChild<ElementRef<HTMLElement>>("wrapEl");
  private readonly btnEl = viewChild<ElementRef<HTMLButtonElement>>("btnEl");
  private readonly panelEl = viewChild<ElementRef<HTMLElement>>("panelEl");

  protected readonly cleanLabel = computed(() =>
    String(this.label() ?? "")
      .replace(/\s*\*$/, "")
      .trim(),
  );

  protected readonly ariaLabel = computed(() => {
    const lbl = this.cleanLabel();
    return lbl ? `About ${lbl}` : "More information";
  });

  private leaveTimer: ReturnType<typeof setTimeout> | null = null;
  private repositionTimer: ReturnType<typeof setTimeout> | null = null;

  private readonly onCaptureEscape = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.open()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.close());
    }
  };

  private readonly onReposition = (): void => {
    if (!this.open()) return;
    if (!this.pinned()) {
      this.zone.run(() => this.close());
      return;
    }
    this.zone.run(() => this.position());
  };

  ngOnDestroy(): void {
    this.clearTimers();
    this.detachListeners();
  }

  protected onMouseEnter(): void {
    this.clearLeaveTimer();
    const wasOpen = this.open();
    this.hoverOpen.set(true);
    if (!wasOpen) this.onOpened();
  }

  protected onMouseLeave(): void {
    this.clearLeaveTimer();
    this.leaveTimer = setTimeout(() => {
      this.hoverOpen.set(false);
      if (!this.open()) this.detachListeners();
    }, 100);
  }

  protected onFocus(): void {
    const wasOpen = this.open();
    this.focusOpen.set(true);
    if (!wasOpen) this.onOpened();
  }

  protected onClick(ev: MouseEvent): void {
    ev.stopPropagation();
    ev.preventDefault();
    this.togglePinned();
  }

  protected onKeyDown(ev: KeyboardEvent): void {
    if (ev.key === "Escape" && this.open()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.close();
    } else if (ev.key === "Enter" || ev.key === " ") {
      ev.preventDefault();
      ev.stopPropagation();
      if (!ev.repeat) this.togglePinned();
    }
  }

  protected onKeyUp(ev: KeyboardEvent): void {
    if (ev.key === " ") {
      ev.preventDefault();
      ev.stopPropagation();
    }
  }

  private togglePinned(): void {
    this.clearLeaveTimer();
    if (this.pinned()) {
      this.close();
    } else {
      const wasOpen = this.open();
      this.pinned.set(true);
      if (!wasOpen) this.onOpened();
      else this.position();
    }
  }

  protected onDocClick(ev: Event): void {
    if (!this.open()) return;
    const t = ev.target as Node | null;
    if (t && !this.wrapEl()?.nativeElement.contains(t)) {
      this.close();
    }
  }

  protected onFocusOut(ev: FocusEvent): void {
    if (!this.open()) return;
    const next = ev.relatedTarget as Node | null;
    if (next && this.wrapEl()?.nativeElement.contains(next)) return;
    this.close();
  }

  private onOpened(): void {
    this.attachListeners();
    this.position();
    this.clearRepositionTimer();
    this.repositionTimer = setTimeout(() => {
      this.repositionTimer = null;
      if (this.open()) this.position();
    }, 0);
  }

  private close(): void {
    this.clearTimers();
    this.pinned.set(false);
    this.hoverOpen.set(false);
    this.focusOpen.set(false);
    this.detachListeners();
  }

  private position(): void {
    const btn = this.btnEl()?.nativeElement;
    if (!btn) return;
    const rect = btn.getBoundingClientRect();
    const vw = this.doc.defaultView?.innerWidth || 1024;
    const vh = this.doc.defaultView?.innerHeight || 768;
    const margin = 12;
    const panel = this.panelEl()?.nativeElement;
    const pw = panel?.offsetWidth || Math.min(360, vw - margin * 2);
    const ph = panel?.offsetHeight || 64;

    let left = rect.left + rect.width / 2 - pw / 2;
    left = Math.max(margin, Math.min(left, vw - pw - margin));

    let top = rect.bottom + 6;
    if (top + ph > vh - margin && rect.top - ph - 6 >= margin) {
      top = rect.top - ph - 6;
    }
    this.posLeft.set(Math.round(left));
    this.posTop.set(Math.round(Math.max(margin, top)));
  }

  private clearLeaveTimer(): void {
    if (this.leaveTimer) {
      clearTimeout(this.leaveTimer);
      this.leaveTimer = null;
    }
  }

  private clearRepositionTimer(): void {
    if (this.repositionTimer) {
      clearTimeout(this.repositionTimer);
      this.repositionTimer = null;
    }
  }

  private clearTimers(): void {
    this.clearLeaveTimer();
    this.clearRepositionTimer();
  }

  private attachListeners(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureEscape, true);
      this.doc.defaultView?.addEventListener("scroll", this.onReposition, true);
      this.doc.defaultView?.addEventListener("resize", this.onReposition);
    });
  }

  private detachListeners(): void {
    this.doc.removeEventListener("keydown", this.onCaptureEscape, true);
    this.doc.defaultView?.removeEventListener(
      "scroll",
      this.onReposition,
      true,
    );
    this.doc.defaultView?.removeEventListener("resize", this.onReposition);
  }
}
