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
  type AfterViewInit,
  Directive,
  ElementRef,
  NgZone,
  type OnDestroy,
  type OnInit,
  inject,
  output,
} from "@angular/core";

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

const INITIAL_FOCUS_SELECTOR =
  'input:not([disabled]):not([readonly]), textarea:not([disabled]):not([readonly]), button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])';

function isFocusableVisible(
  el: HTMLElement,
  host: HTMLElement,
  win: Window | null,
): boolean {
  if (el.hidden || el.closest("[hidden],[inert]")) return false;
  if (!win || typeof win.getComputedStyle !== "function") return true;
  for (let cur: HTMLElement | null = el; cur; cur = cur.parentElement) {
    const st = win.getComputedStyle(cur);
    if (st.display === "none" || st.visibility === "hidden") return false;
    if (cur === host) break;
  }
  return true;
}

@Directive({
  selector: "[gDialogTrap]",
})
export class DialogTrapDirective implements OnInit, AfterViewInit, OnDestroy {
  readonly gDialogClose = output<void>();

  private readonly el = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private previousFocus: HTMLElement | null = null;

  private readonly onKeydown = (ev: KeyboardEvent): void => {
    if (ev.defaultPrevented) return;
    if (ev.key === "Escape") {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.gDialogClose.emit());
      return;
    }
    if (ev.key === "Tab") {
      this.trapTab(ev);
    }
  };

  ngOnInit(): void {
    const active = this.doc.activeElement;
    this.previousFocus = active instanceof HTMLElement ? active : null;
  }

  ngAfterViewInit(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onKeydown, true);
    });
    const host = this.el.nativeElement;
    if (!host.hasAttribute("tabindex")) {
      host.setAttribute("tabindex", "-1");
    }
    queueMicrotask(() => {
      if (!host.isConnected) return;
      const win = this.doc.defaultView;
      const first = Array.from(
        host.querySelectorAll<HTMLElement>(INITIAL_FOCUS_SELECTOR),
      ).find((item) => isFocusableVisible(item, host, win));
      (first ?? host).focus();
    });
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onKeydown, true);
    const prev = this.previousFocus;
    this.previousFocus = null;
    if (prev) {
      queueMicrotask(() => {
        if (prev.isConnected) prev.focus();
      });
    }
  }

  private trapTab(ev: KeyboardEvent): void {
    const host = this.el.nativeElement;
    const win = this.doc.defaultView;
    const items = Array.from(
      host.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
    ).filter((item) => isFocusableVisible(item, host, win));
    if (items.length === 0) {
      ev.preventDefault();
      host.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = this.doc.activeElement;
    if (ev.shiftKey && (active === first || !host.contains(active))) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && (active === last || !host.contains(active))) {
      ev.preventDefault();
      first.focus();
    }
  }
}
