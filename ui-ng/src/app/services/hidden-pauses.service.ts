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
import {
  type DispatchStatusBanner,
  type HiddenPause,
  pauseWindowsHidden,
  pruneHiddenPauses,
} from "../shared/dispatch-status";
import type { SnackbarService } from "./snackbar.service";

export const HIDDEN_PAUSES_KEY = "grain.hiddenPauses";

function readStored(nowMs: number): HiddenPause[] {
  try {
    const raw = globalThis.localStorage?.getItem(HIDDEN_PAUSES_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const valid = parsed.filter(
      (h): h is HiddenPause =>
        !!h &&
        typeof h.id === "string" &&
        (h.untilMs === null || typeof h.untilMs === "number") &&
        typeof h.hiddenAtMs === "number",
    );
    return pruneHiddenPauses(valid, nowMs);
  } catch {
    return [];
  }
}

// HiddenPausesService remembers the model-quota pauses this viewer has
// swatted away ("Hide until reset", GU-6), in this browser's
// localStorage. Hiding is the viewer's own: it never lifts a pause
// (that is DELETE /api/pause) and never hides anything from anybody
// else. A hidden pause shows again once its window ends, when a new
// pause joins it, or when it escalates (see pauseWindowsHidden).
@Injectable({ providedIn: "root" })
export class HiddenPausesService {
  private readonly _hidden = signal<readonly HiddenPause[]>(
    readStored(Date.now()),
  );
  readonly hidden = this._hidden.asReadonly();

  // isHidden is whether banner is one the viewer hid.
  isHidden(banner: DispatchStatusBanner | null | undefined): boolean {
    return pauseWindowsHidden(banner, this._hidden());
  }

  // hide hides every pause behind banner until it resets. It returns the
  // list as it was, for restore to undo.
  hide(
    banner: DispatchStatusBanner,
    nowMs = Date.now(),
  ): readonly HiddenPause[] {
    const before = this._hidden();
    if (!banner.hideable) return before;
    const kept = pruneHiddenPauses(before, nowMs).filter(
      (h) => !banner.pauses.some((p) => p.id === h.id),
    );
    this.save([
      ...kept,
      ...banner.pauses.map((p) => ({ ...p, hiddenAtMs: nowMs })),
    ]);
    return before;
  }

  // show un-hides every pause behind banner.
  show(banner: DispatchStatusBanner | null | undefined): void {
    if (!banner) return;
    this.save(
      this._hidden().filter((h) => !banner.pauses.some((p) => p.id === h.id)),
    );
  }

  // restore puts back a list hide returned (Undo).
  restore(list: readonly HiddenPause[]): void {
    this.save([...list]);
  }

  private save(list: HiddenPause[]): void {
    this._hidden.set(list);
    try {
      if (list.length === 0) {
        globalThis.localStorage?.removeItem(HIDDEN_PAUSES_KEY);
      } else {
        globalThis.localStorage?.setItem(
          HIDDEN_PAUSES_KEY,
          JSON.stringify(list),
        );
      }
    } catch {
      // Storage full or blocked: hiding still holds for this page.
    }
  }
}

// hidePausesWithUndo hides db's pauses and offers Undo on the snackbar.
export function hidePausesWithUndo(
  hiddenPauses: HiddenPausesService,
  snackbar: SnackbarService,
  db: DispatchStatusBanner,
): void {
  if (!db.hideable) return;
  const before = hiddenPauses.hide(db);
  const n = db.pauses.length;
  snackbar.show(
    `${n === 1 ? "Model pause warning" : `${n} model pause warnings`} hidden until reset`,
    { label: "Undo", run: () => hiddenPauses.restore(before) },
  );
}
