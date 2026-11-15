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

import { Injectable, computed, signal } from "@angular/core";

// One thing the full-screen viewer can show: an attached image (src is its
// attachment URL) or a rendered Mermaid diagram (svg is already sanitized
// by shared/mermaid-render.ts).
export type ViewerItem =
  | { kind: "image"; name: string; src: string }
  | { kind: "svg"; name: string; svg: string };

export interface ViewerState {
  items: readonly ViewerItem[];
  index: number;
}

// MediaViewerService opens the shell's one full-screen viewer
// (g-media-viewer, deferred in app.component.ts) from anywhere: the
// attachment list and the [gMarkdown] directive both call open().
@Injectable({ providedIn: "root" })
export class MediaViewerService {
  private readonly _state = signal<ViewerState | null>(null);
  readonly state = this._state.asReadonly();
  readonly isOpen = computed(() => this._state() !== null);
  /** Where focus goes back to when the viewer closes. */
  opener: HTMLElement | null = null;

  open(items: readonly ViewerItem[], index = 0): void {
    if (!items.length) return;
    const active = typeof document !== "undefined" && document.activeElement;
    this.opener = active instanceof HTMLElement ? active : null;
    const i = Math.min(Math.max(0, index), items.length - 1);
    this._state.set({ items, index: i });
  }

  /** Moves by delta, wrapping around at either end. */
  step(delta: number): void {
    const s = this._state();
    if (!s || s.items.length < 2) return;
    const n = s.items.length;
    this._state.set({ ...s, index: (((s.index + delta) % n) + n) % n });
  }

  close(): void {
    if (!this._state()) return;
    this._state.set(null);
    const back = this.opener;
    this.opener = null;
    if (back) queueMicrotask(() => back.isConnected && back.focus());
  }
}
