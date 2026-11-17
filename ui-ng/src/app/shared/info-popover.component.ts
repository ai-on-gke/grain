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
  HostListener,
  Injector,
  type OnDestroy,
  ViewEncapsulation,
  afterNextRender,
  inject,
  input,
  signal,
} from "@angular/core";

let nextId = 0;
// The popover open now, if any: opening another closes it.
let openPopover: InfoPopoverComponent | null = null;
function setOpenPopover(p: InfoPopoverComponent | null): void {
  openPopover = p;
}

// InfoPopover is the (i) beside a label whose meaning takes a sentence
// or more: a chip's state, a summary cut short. Unlike a gTip tooltip it
// opens on a click or a tap -- and so on Enter and Space, since it is a
// button -- and stays open until it is clicked again, Escape is pressed
// or something else is clicked, so a phone can read it and a screen
// reader can reach it (aria-expanded, aria-controls). It is placed
// fixed beside its button -- below it, or above when there is no room --
// and kept inside the viewport, so it never widens the page it sits in;
// it follows its button as the pane scrolls, and closes once that
// button has scrolled out of view. One is open at a time.
@Component({
  selector: "g-info-popover",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <button
      type="button"
      class="g-info-pop-btn"
      [attr.aria-label]="'About ' + label()"
      [attr.aria-expanded]="open()"
      [attr.aria-controls]="panelId"
      [attr.data-testid]="testid() || null"
      (click)="toggle($event)"
    >
      i
    </button>
    @if (open()) {
      <div
        class="g-info-pop-panel"
        role="note"
        [id]="panelId"
        [attr.aria-label]="label()"
        [style.top.px]="top()"
        [style.left.px]="left()"
        [style.visibility]="placed() ? null : 'hidden'"
        [attr.data-testid]="testid() ? testid() + '-panel' : null"
      >
        {{ text() }}
      </div>
    }
  `,
  styles: `
    g-info-popover {
      display: inline-flex;
      vertical-align: middle;
    }
    .g-info-pop-btn {
      inline-size: 20px;
      block-size: 20px;
      padding: 0;
      border: 1px solid var(--md-sys-color-outline);
      border-radius: var(--md-sys-shape-corner-full);
      background: transparent;
      color: var(--md-sys-color-on-surface-variant);
      font: 600 12px/18px var(--grain-font-body);
      font-style: italic;
      cursor: pointer;
      flex: none;
    }
    .g-info-pop-btn:hover,
    .g-info-pop-btn[aria-expanded="true"] {
      background: var(--md-sys-color-surface-container);
      color: var(--md-sys-color-on-surface);
    }
    .g-info-pop-btn:focus-visible {
      outline: 2px solid var(--md-sys-color-primary);
      outline-offset: 2px;
    }
    .g-info-pop-panel {
      position: fixed;
      z-index: 100;
      box-sizing: border-box;
      inline-size: min(320px, calc(100vw - 32px));
      padding: 10px 12px;
      border: 1px solid var(--md-sys-color-outline-variant);
      border-radius: var(--md-sys-shape-corner-medium);
      background: var(--md-sys-color-surface-container);
      color: var(--md-sys-color-on-surface);
      font: 400 13px/18px var(--grain-font-body);
      white-space: pre-line;
      overflow-wrap: anywhere;
      box-shadow: 0 4px 12px rgb(0 0 0 / 0.18);
    }
  `,
})
export class InfoPopoverComponent implements OnDestroy {
  readonly text = input.required<string>();
  // label names what the popover explains: the button reads "About <label>".
  readonly label = input.required<string>();
  readonly testid = input<string>("");

  protected readonly open = signal(false);
  protected readonly top = signal(0);
  protected readonly left = signal(0);
  // placed is false between opening and the panel's first measurement,
  // so it never shows for a frame where it does not fit.
  protected readonly placed = signal(false);
  protected readonly panelId = `g-info-pop-${++nextId}`;
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly injector = inject(Injector);
  // Scroll happens inside panes (.settings-content, .shell-main), whose
  // scroll events never reach window: listen on document in the capture
  // phase while open, as TipDirective does.
  private readonly onScroll = () => this.follow();

  protected toggle(ev: Event): void {
    // The (i) often sits in a row that is itself clickable or a link:
    // opening it is not a click on the row.
    ev.preventDefault();
    ev.stopPropagation();
    if (this.open()) {
      this.close();
      return;
    }
    // One popover open at a time: the click that opens this one stopped
    // propagating, so another open one never hears it as outside.
    if (openPopover && openPopover !== this) openPopover.close();
    setOpenPopover(this);
    this.placed.set(false);
    this.place(0);
    this.open.set(true);
    document.addEventListener("scroll", this.onScroll, true);
    afterNextRender(
      () => {
        const panel =
          this.host.nativeElement.querySelector<HTMLElement>(
            ".g-info-pop-panel",
          );
        this.place(panel?.getBoundingClientRect().height ?? 0);
        this.placed.set(true);
      },
      { injector: this.injector },
    );
  }

  // place puts the panel under its button, or above it when it would
  // run off the bottom of the viewport, kept 16px inside every edge.
  private place(height: number): void {
    const btn = this.host.nativeElement.querySelector("button");
    const r = btn?.getBoundingClientRect();
    if (!r) return;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const width = Math.min(320, vw - 32);
    let top = r.bottom + 6;
    if (height && top + height > vh - 16) {
      top = Math.max(16, r.top - height - 6);
    }
    this.top.set(top);
    this.left.set(Math.max(16, Math.min(r.left, vw - width - 16)));
  }

  // follow keeps the panel beside its button as the pane under it
  // scrolls -- a click that focuses the (i) can scroll it a little -- and
  // closes it once the button has scrolled out of view.
  private follow(): void {
    if (!this.open()) return;
    const r = this.host.nativeElement
      .querySelector("button")
      ?.getBoundingClientRect();
    if (!r || r.bottom < 0 || r.top > window.innerHeight) {
      this.close();
      return;
    }
    const panel =
      this.host.nativeElement.querySelector<HTMLElement>(".g-info-pop-panel");
    this.place(panel?.getBoundingClientRect().height ?? 0);
  }

  close(): void {
    if (openPopover === this) setOpenPopover(null);
    document.removeEventListener("scroll", this.onScroll, true);
    if (this.open()) this.open.set(false);
  }

  @HostListener("document:click", ["$event"])
  protected onDocumentClick(ev: Event): void {
    if (!this.open()) return;
    if (this.host.nativeElement.contains(ev.target as Node)) return;
    this.close();
  }

  @HostListener("document:keydown.escape")
  protected onEscape(): void {
    if (!this.open()) return;
    this.close();
    this.host.nativeElement.querySelector("button")?.focus();
  }

  // A fixed panel would drift off its button on a resize: close it.
  @HostListener("window:resize")
  protected onResize(): void {
    this.close();
  }

  ngOnDestroy(): void {
    this.close();
  }
}
