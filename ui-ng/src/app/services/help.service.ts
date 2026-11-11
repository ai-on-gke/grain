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

import { Injectable, signal } from "@angular/core";

export type HelpTab = "about" | "glossary" | "shortcuts";

// HelpService coordinates the shell's 3-tab Help / Glossary / Shortcuts
// dialog across routes while letting standalone page component specs fall
// back to their local dialog when the root shell is not mounted.
@Injectable({ providedIn: "root" })
export class HelpService {
  readonly attached = signal(false);
  readonly isOpen = signal(false);
  readonly tab = signal<HelpTab>("about");
  private opener: HTMLElement | null = null;

  open(tab: HelpTab = "about", opener?: HTMLElement | null): void {
    this.opener =
      opener ??
      (typeof document !== "undefined"
        ? (document.activeElement as HTMLElement | null)
        : null);
    this.tab.set(tab);
    this.isOpen.set(true);
  }

  close(restoreFocus = true): void {
    if (!this.isOpen()) return;
    this.isOpen.set(false);
    const target = this.opener;
    this.opener = null;
    if (restoreFocus && target && typeof target.focus === "function") {
      setTimeout(() => target.focus());
    }
  }
}
