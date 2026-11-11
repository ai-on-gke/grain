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
import { Injectable, NgZone, OnDestroy, inject, signal } from "@angular/core";

export interface Shortcut {
  // The KeyboardEvent.key it answers to, e.g. "c", "1", ",", "?".
  key: string;
  // What it does, for the keyboard help list.
  description: string;
  run: () => void;
}

// The browser-wide switch for single-key shortcuts. WCAG 2.1.4 asks
// that character-key shortcuts can be turned off: speech input types
// words as keys, and screen readers use letters and digits to move
// around the page.
export const SHORTCUTS_STORAGE_KEY = "grain.next.shortcuts";

// Roles whose element takes the person's keys for itself.
const KEY_TAKING_ROLES =
  /^(textbox|combobox|listbox|menu|menubar|slider|spinbutton|grid|tree)$/;

// isTypingTarget is whether a key press belongs to a field or a widget
// the person is operating, where a single letter is input and never a
// shortcut.
export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || !el.tagName) return false;
  if (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
  if (el.isContentEditable === true) return true;
  const role = el.getAttribute?.("role");
  return !!role && KEY_TAKING_ROLES.test(role);
}

function loadEnabled(): boolean {
  try {
    return localStorage.getItem(SHORTCUTS_STORAGE_KEY) !== "off";
  } catch {
    return true;
  }
}

// KeyboardService owns the app-wide single-key shortcuts (c, the number
// keys, ",", t): one document listener for all of them, so pages that
// add their own (the Inbox's j/k/x) never fight the shell's. A shortcut
// is ignored while the person is typing, with a modifier held, on a
// held-down key's repeats, while a modal dialog is open, or when the
// person has turned shortcuts off.
@Injectable({ providedIn: "root" })
export class KeyboardService implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly shortcuts = new Map<string, Shortcut>();
  readonly list = signal<readonly Shortcut[]>([]);
  readonly enabled = signal(loadEnabled());

  private readonly onKey = (ev: KeyboardEvent): void => {
    if (!this.enabled()) return;
    if (ev.defaultPrevented || ev.repeat) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    if (isTypingTarget(ev.target)) return;
    if (this.doc.querySelector('[aria-modal="true"]')) return;
    const s = this.shortcuts.get(ev.key);
    if (!s) return;
    ev.preventDefault();
    this.zone.run(() => s.run());
  };

  constructor() {
    this.zone.runOutsideAngular(() =>
      this.doc.addEventListener("keydown", this.onKey),
    );
  }

  setEnabled(on: boolean): void {
    this.enabled.set(on);
    try {
      localStorage.setItem(SHORTCUTS_STORAGE_KEY, on ? "on" : "off");
    } catch {
      // Storage unavailable: the choice lasts for this page only.
    }
  }

  // register adds a shortcut and returns the call that removes it again.
  // A later registration for the same key replaces the earlier one until
  // it is removed.
  register(s: Shortcut): () => void {
    const previous = this.shortcuts.get(s.key);
    this.shortcuts.set(s.key, s);
    this.publish();
    return () => {
      if (this.shortcuts.get(s.key) !== s) return;
      if (previous) this.shortcuts.set(s.key, previous);
      else this.shortcuts.delete(s.key);
      this.publish();
    };
  }

  private publish(): void {
    this.list.set([...this.shortcuts.values()]);
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onKey);
  }
}
