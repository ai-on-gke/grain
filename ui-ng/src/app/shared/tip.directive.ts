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
// How long the tip waits after the pointer leaves its control, so the
// pointer can move onto the tip itself (WCAG 1.4.13: hoverable).
export const TIP_LEAVE_MS = 120;

export const TIP_REGION_ID = "g-tip-region";

// One tooltip element for the whole app (spec D-16): created on first
// use, reused by every [gTip], role="tooltip". It sits in a labelled
// region at the end of <body>, shown only while the tip is, so a
// showing tip is inside a landmark: bare in <body> it was "page content
// not contained by landmarks" to axe, which failed the Inbox's dark
// axe pass whenever the theme toggle's tip appeared mid-scan (ui-ng
// e2e, 2026-11-13). The tip is position: fixed, so the wrapper changes
// nothing on screen.
function tipElement(doc: Document): HTMLElement {
  let el = doc.getElementById(TIP_ID);
  if (!el) {
    const region = doc.createElement("div");
    region.id = TIP_REGION_ID;
    region.setAttribute("role", "region");
    region.setAttribute("aria-label", "Tooltip");
    region.hidden = true;
    el = doc.createElement("div");
    el.id = TIP_ID;
    el.className = "g-tip";
    el.setAttribute("role", "tooltip");
    el.hidden = true;
    region.appendChild(el);
    doc.body.appendChild(region);
  }
  return el;
}

function setTipShown(el: HTMLElement, shown: boolean): void {
  el.hidden = !shown;
  if (el.parentElement?.id === TIP_REGION_ID) el.parentElement.hidden = !shown;
}

// The directive whose text the shared element is showing, if any: one
// owner at a time, so a hovered control never leaves a focused one's
// aria-describedby pointing at someone else's sentence.
let owner: TipDirective | null = null;

export function dismissActiveTooltip(): void {
  owner?.hide();
}

function addToken(el: HTMLElement, attr: string, token: string): void {
  const tokens = (el.getAttribute(attr) ?? "").split(/\s+/).filter(Boolean);
  if (!tokens.includes(token)) tokens.push(token);
  el.setAttribute(attr, tokens.join(" "));
}

function removeToken(el: HTMLElement, attr: string, token: string): void {
  const tokens = (el.getAttribute(attr) ?? "")
    .split(/\s+/)
    .filter((t) => t && t !== token);
  if (tokens.length) el.setAttribute(attr, tokens.join(" "));
  else el.removeAttribute(attr);
}

// gTip shows a one-sentence description after 400 ms of hover, or at
// once on keyboard focus. The pointer can move onto it; Escape anywhere,
// blur, scrolling or leaving both control and tip hides it. It is
// referenced with aria-describedby only while showing, and never sets a
// native title, so there is no second tooltip.
@Directive({
  selector: "[gTip]",
  host: {
    "(mouseenter)": "hoverIn()",
    "(mouseleave)": "hoverOut()",
    "(pointerdown)": "hide()",
    "(click)": "hide()",
    "(focusin)": "focusIn()",
    "(focusout)": "hide()",
  },
})
export class TipDirective implements OnDestroy {
  readonly gTip = input<string | null | undefined>("");
  // "below" by default; "right" beside the control, for the rail.
  readonly gTipPlacement = input<"below" | "right">("below");

  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private showTimer: ReturnType<typeof setTimeout> | null = null;
  private leaveTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly onScroll = (): void => this.hide();
  private readonly onDocKey = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape") this.hide();
  };
  private readonly onTipEnter = (): void => {
    this.leaveTimer = clearTimer(this.leaveTimer);
  };
  private readonly onTipLeave = (): void => this.hoverOut();

  hoverIn(): void {
    this.leaveTimer = clearTimer(this.leaveTimer);
    if (!this.gTip() || owner === this) return;
    this.showTimer = clearTimer(this.showTimer);
    this.zone.runOutsideAngular(() => {
      this.showTimer = setTimeout(() => this.show(), TIP_DELAY_MS);
    });
  }

  hoverOut(): void {
    this.showTimer = clearTimer(this.showTimer);
    if (owner !== this) return;
    this.leaveTimer = clearTimer(this.leaveTimer);
    this.zone.runOutsideAngular(() => {
      this.leaveTimer = setTimeout(() => this.hide(), TIP_LEAVE_MS);
    });
  }

  focusIn(): void {
    if (!this.gTip()) return;
    // Keyboard focus only: a mouse click focuses buttons too, and a tip
    // pinned until blur after every click is noise.
    const el = this.host.nativeElement;
    try {
      if (!el.matches(":focus-visible")) return;
    } catch {
      // A browser without :focus-visible: show it.
    }
    this.showTimer = clearTimer(this.showTimer);
    this.show();
  }

  show(): void {
    const text = this.gTip();
    if (!text) return;
    if (owner && owner !== this) owner.hide();
    const el = tipElement(this.doc);
    el.textContent = text;
    setTipShown(el, true);
    this.position(el);
    addToken(this.host.nativeElement, "aria-describedby", TIP_ID);
    if (owner !== this) {
      // The tip element is shared page-wide, so the owning directive is
      // module state by design.
      // eslint-disable-next-line @typescript-eslint/no-this-alias
      owner = this;
      this.doc.addEventListener("scroll", this.onScroll, true);
      this.doc.addEventListener("keydown", this.onDocKey, true);
      el.addEventListener("mouseenter", this.onTipEnter);
      el.addEventListener("mouseleave", this.onTipLeave);
    }
  }

  hide(): void {
    this.showTimer = clearTimer(this.showTimer);
    this.leaveTimer = clearTimer(this.leaveTimer);
    if (owner !== this) return;
    owner = null;
    const el = this.doc.getElementById(TIP_ID);
    if (el) {
      setTipShown(el, false);
      el.removeEventListener("mouseenter", this.onTipEnter);
      el.removeEventListener("mouseleave", this.onTipLeave);
    }
    removeToken(this.host.nativeElement, "aria-describedby", TIP_ID);
    this.doc.removeEventListener("scroll", this.onScroll, true);
    this.doc.removeEventListener("keydown", this.onDocKey, true);
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

  ngOnDestroy(): void {
    this.hide();
  }
}

function clearTimer(timer: ReturnType<typeof setTimeout> | null): null {
  if (timer) clearTimeout(timer);
  return null;
}
