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
  Directive,
  ElementRef,
  NgZone,
  OnDestroy,
  inject,
  input,
} from "@angular/core";

export const TIP_ID = "g-tip";
export const TIP_DELAY_MS = 400;

// One tooltip element for the whole app (spec D-16): created on first
// use, reused by every [gTip], role="tooltip", referenced from the
// control with aria-describedby only while it is showing.
function tipElement(doc: Document): HTMLElement {
  let el = doc.getElementById(TIP_ID);
  if (!el) {
    el = doc.createElement("div");
    el.id = TIP_ID;
    el.className = "g-tip";
    el.setAttribute("role", "tooltip");
    el.hidden = true;
    doc.body.appendChild(el);
  }
  return el;
}

// gTip shows a one-sentence description after 400 ms of hover, or at
// once on keyboard focus; Escape, blur, scrolling or leaving hides it.
// It never sets a native title, so there is no second tooltip.
@Directive({
  selector: "[gTip]",
  host: {
    "(mouseenter)": "hoverIn()",
    "(mouseleave)": "hide()",
    "(focusin)": "focusIn()",
    "(focusout)": "hide()",
    "(keydown.escape)": "hide()",
  },
})
export class TipDirective implements OnDestroy {
  readonly gTip = input<string | null | undefined>("");
  // "below" by default; "right" beside the control, for the rail.
  readonly gTipPlacement = input<"below" | "right">("below");

  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private timer: ReturnType<typeof setTimeout> | null = null;
  private showing = false;
  private readonly onScroll = (): void => this.hide();

  hoverIn(): void {
    if (!this.gTip()) return;
    this.clear();
    this.zone.runOutsideAngular(() => {
      this.timer = setTimeout(() => this.show(), TIP_DELAY_MS);
    });
  }

  focusIn(): void {
    if (!this.gTip()) return;
    this.clear();
    this.show();
  }

  show(): void {
    const text = this.gTip();
    if (!text) return;
    const el = tipElement(this.doc);
    el.textContent = text;
    el.hidden = false;
    this.position(el);
    this.host.nativeElement.setAttribute("aria-describedby", TIP_ID);
    this.showing = true;
    this.doc.addEventListener("scroll", this.onScroll, true);
  }

  hide(): void {
    this.clear();
    if (!this.showing) return;
    this.showing = false;
    const el = this.doc.getElementById(TIP_ID);
    if (el) el.hidden = true;
    this.host.nativeElement.removeAttribute("aria-describedby");
    this.doc.removeEventListener("scroll", this.onScroll, true);
  }

  private position(el: HTMLElement): void {
    const r = this.host.nativeElement.getBoundingClientRect();
    const view = this.doc.defaultView;
    const vw = view?.innerWidth ?? 1024;
    const vh = view?.innerHeight ?? 768;
    const tw = el.offsetWidth || 200;
    const th = el.offsetHeight || 32;
    let left: number;
    let top: number;
    if (this.gTipPlacement() === "right") {
      left = r.right + 8;
      top = r.top + r.height / 2 - th / 2;
    } else {
      left = r.left + r.width / 2 - tw / 2;
      top = r.bottom + 8;
      // Flip above when there is no room below.
      if (top + th > vh - 4) top = r.top - th - 8;
    }
    left = Math.max(4, Math.min(left, vw - tw - 4));
    top = Math.max(4, top);
    el.style.left = `${Math.round(left)}px`;
    el.style.top = `${Math.round(top)}px`;
  }

  private clear(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }

  ngOnDestroy(): void {
    this.hide();
  }
}
