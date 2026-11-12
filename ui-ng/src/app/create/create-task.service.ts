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
import type { NewTaskDraft } from "./new-task-form";

// CreateTaskService opens the New task sheet from anywhere (the rail
// FAB, the phone FAB, the create menu, the `c` key, and later the
// contextual entry points that pre-fill it), and remembers what opened
// it so focus can go back there on close (spec D-23).
@Injectable({ providedIn: "root" })
export class CreateTaskService {
  readonly isOpen = signal(false);
  readonly prefill = signal<Partial<NewTaskDraft>>({});
  // What Cancel or Escape left, offered again on the next plain opening.
  readonly saved = signal<{
    draft: NewTaskDraft;
    edited: (keyof NewTaskDraft)[];
  } | null>(null);
  private opener: HTMLElement | null = null;

  keep(draft: NewTaskDraft, edited: (keyof NewTaskDraft)[]): void {
    this.saved.set({ draft, edited });
  }

  clearSaved(): void {
    this.saved.set(null);
  }

  open(prefill: Partial<NewTaskDraft> = {}, opener?: Element | null): void {
    this.opener =
      (opener as HTMLElement | null) ??
      (document.activeElement as HTMLElement | null);
    this.prefill.set(prefill);
    this.isOpen.set(true);
  }

  // close puts focus back on the opener, unless the caller moves it
  // somewhere else (a navigation that replaces the opener).
  close(restoreFocus = true): void {
    this.isOpen.set(false);
    const el = this.opener;
    this.opener = null;
    if (restoreFocus && el) {
      setTimeout(() => {
        if (el.isConnected) el.focus();
      });
    }
  }
}
