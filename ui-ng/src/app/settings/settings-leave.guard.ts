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
import { inject } from "@angular/core";
import type { CanDeactivateFn } from "@angular/router";

// SettingsLeaveCheck is what the Settings page answers the guard with:
// the unsaved work a move to nextUrl would throw away, or null when the
// move keeps everything.
export interface SettingsLeaveCheck {
  unsavedWorkLostBy(nextUrl: string): string | null;
  forgetUnsavedCards(): void;
}

// settingsLeaveGuard asks before a navigation drops unsaved Settings
// work: leaving Settings with a draft in the save bar, or moving to
// another section with a card that saves on its own (Memory) still
// edited. A clean page never asks.
export const settingsLeaveGuard: CanDeactivateFn<SettingsLeaveCheck> = (
  component,
  _route,
  _state,
  next,
) => {
  const lost = component?.unsavedWorkLostBy?.(next?.url ?? "");
  if (!lost) return true;
  const win = inject(DOCUMENT).defaultView;
  const ok = win?.confirm(`Discard ${lost}?`) ?? true;
  if (ok) component.forgetUnsavedCards();
  return ok;
};
