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

import { Injectable, NgZone, inject, signal } from "@angular/core";

export interface SnackbarAction {
  label: string;
  run: () => void | Promise<unknown>;
}

export interface Snack {
  id: number;
  message: string;
  action?: SnackbarAction;
  // How long it stays, in milliseconds. 0 keeps it until dismissed.
  durationMs: number;
}

// The spec's undo window: every reversible action offers Undo for five
// seconds.
export const DEFAULT_SNACK_MS = 5000;

// SnackbarService shows one short message at a time at the bottom of the
// page, optionally with one action (Undo, Open). A new message replaces
// the one showing, as Material's snackbar does; the component reads
// `current` and announces it through a polite live region.
@Injectable({ providedIn: "root" })
export class SnackbarService {
  private readonly zone = inject(NgZone);
  private nextId = 1;
  private timer: ReturnType<typeof setTimeout> | null = null;
  readonly current = signal<Snack | null>(null);

  show(
    message: string,
    action?: SnackbarAction,
    durationMs = DEFAULT_SNACK_MS,
  ): Snack {
    const snack: Snack = { id: this.nextId++, message, action, durationMs };
    this.clearTimer();
    this.current.set(snack);
    if (durationMs > 0) {
      this.zone.runOutsideAngular(() => {
        this.timer = setTimeout(
          () => this.zone.run(() => this.dismiss(snack.id)),
          durationMs,
        );
      });
    }
    return snack;
  }

  // runAction performs the showing snack's action once and dismisses it,
  // so a double click cannot undo twice.
  async runAction(id: number): Promise<void> {
    const snack = this.current();
    if (!snack || snack.id !== id || !snack.action) return;
    this.dismiss(id);
    await snack.action.run();
  }

  dismiss(id?: number): void {
    const snack = this.current();
    if (!snack || (id !== undefined && snack.id !== id)) return;
    this.clearTimer();
    this.current.set(null);
  }

  private clearTimer(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }
}
