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

// isTypingTarget is whether a key press belongs to a field the person is
// typing in, where a single letter is text and never a shortcut.
export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || !el.tagName) return false;
  if (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
  return el.isContentEditable === true;
}

// KeyboardService owns the app-wide single-key shortcuts (c, the number
// keys, ",", t): one document listener for all of them, so pages that
// add their own (the Inbox's j/k/x) never fight the shell's. A shortcut
// is ignored while the person is typing, with a modifier held, or while
// a modal dialog is open -- the dialog owns the keyboard then.
@Injectable({ providedIn: "root" })
export class KeyboardService implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly shortcuts = new Map<string, Shortcut>();
  readonly list = signal<readonly Shortcut[]>([]);

  private readonly onKey = (ev: KeyboardEvent): void => {
    if (ev.defaultPrevented || ev.metaKey || ev.ctrlKey || ev.altKey) return;
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
