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

import { Injectable, NgZone, OnDestroy, inject, signal } from "@angular/core";

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

// A plain message stays five seconds. One with an action (Undo, Open)
// stays ten, so a keyboard or screen-reader user has time to reach it;
// either pauses while hovered or focused (WCAG 2.2.1).
export const DEFAULT_SNACK_MS = 5000;
export const ACTION_SNACK_MS = 10000;

function errorText(err: unknown): string {
  const e = err as { error?: { error?: string } | string; message?: string };
  if (typeof e?.error === "string" && e.error) return e.error;
  if (e?.error && typeof e.error === "object" && e.error.error) {
    return e.error.error;
  }
  return e?.message || "it did not go through";
}

// SnackbarService shows one short message at a time at the bottom of the
// page, optionally with one action. A new message replaces the one
// showing, as Material's snackbar does; the component reads `current`
// and announces it through a polite live region.
@Injectable({ providedIn: "root" })
export class SnackbarService implements OnDestroy {
  private readonly zone = inject(NgZone);
  private nextId = 1;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private remaining = 0;
  private startedAt = 0;
  readonly current = signal<Snack | null>(null);

  show(message: string, action?: SnackbarAction, durationMs?: number): Snack {
    const ms = durationMs ?? (action ? ACTION_SNACK_MS : DEFAULT_SNACK_MS);
    const snack: Snack = { id: this.nextId++, message, action, durationMs: ms };
    this.clearTimer();
    this.current.set(snack);
    this.remaining = ms;
    if (ms > 0) this.startTimer(snack.id);
    return snack;
  }

  // pause stops the clock while the person is on the snack; resume
  // starts it again with whatever time was left.
  pause(): void {
    if (!this.timer) return;
    this.clearTimer();
    this.remaining = Math.max(
      0,
      this.remaining - (Date.now() - this.startedAt),
    );
  }

  resume(): void {
    const snack = this.current();
    if (!snack || snack.durationMs === 0 || this.timer) return;
    this.startTimer(snack.id);
  }

  // runAction performs the showing snack's action once and dismisses it,
  // so a double click cannot undo twice. A failed action says so.
  async runAction(id: number): Promise<void> {
    const snack = this.current();
    if (!snack || snack.id !== id || !snack.action) return;
    this.dismiss(id);
    try {
      await snack.action.run();
    } catch (err) {
      this.show(`${snack.action.label} failed: ${errorText(err)}`);
    }
  }

  dismiss(id?: number): void {
    const snack = this.current();
    if (!snack || (id !== undefined && snack.id !== id)) return;
    this.clearTimer();
    this.current.set(null);
  }

  private startTimer(id: number): void {
    this.startedAt = Date.now();
    const ms = this.remaining;
    this.zone.runOutsideAngular(() => {
      this.timer = setTimeout(() => this.zone.run(() => this.dismiss(id)), ms);
    });
  }

  private clearTimer(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }

  ngOnDestroy(): void {
    this.clearTimer();
  }
}
