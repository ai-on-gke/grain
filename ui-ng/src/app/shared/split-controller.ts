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

import { computed, signal } from "@angular/core";
import {
  COLLAPSED_KEY,
  DEFAULT_RATIO,
  RATIO_KEY,
  bounds,
  clampRatio,
  keyStep,
  loadCollapsed,
  loadRatio,
  percent,
  saveCollapsed,
  saveRatio,
} from "./split";

export interface SplitOptions {
  ratioKey?: string;
  collapsedKey?: string;
  // The share the list starts at, and that double-click resets to.
  defaultRatio?: number;
  // The split element, once rendered.
  element: () => HTMLElement | undefined;
}

// SplitController is the state and pointer/keyboard handling of a
// resizable list/detail split (W3C APG window splitter), shared by the
// Inbox and Tasks. The page owns the markup: a grid with --queue-w set
// from ratioExact(), and a role="separator" bound to the handlers here.
export class SplitController {
  private readonly ratioKey: string;
  private readonly collapsedKey: string;
  readonly defaultRatio: number;
  private readonly el: () => HTMLElement | undefined;

  readonly ratio: ReturnType<typeof signal<number>>;
  readonly collapsed: ReturnType<typeof signal<boolean>>;
  readonly dragging = signal(false);
  private readonly stage = signal(0);
  private dragFrom: number | null = null;
  private dragWasCollapsed = false;
  private resizeObs: ResizeObserver | null = null;

  // The column gets the exact share; ARIA gets a whole percent.
  readonly ratioExact = computed(
    () =>
      (this.stage() ? clampRatio(this.ratio(), this.stage()) : this.ratio()) *
      100,
  );
  readonly ratioPct = computed(() =>
    percent(
      this.stage() ? clampRatio(this.ratio(), this.stage()) : this.ratio(),
    ),
  );
  readonly minPct = computed(() =>
    this.stage() ? percent(bounds(this.stage()).min / this.stage()) : 15,
  );
  readonly maxPct = computed(() =>
    this.stage() ? percent(bounds(this.stage()).max / this.stage()) : 80,
  );

  constructor(opts: SplitOptions) {
    this.ratioKey = opts.ratioKey ?? RATIO_KEY;
    this.collapsedKey = opts.collapsedKey ?? COLLAPSED_KEY;
    this.defaultRatio = opts.defaultRatio ?? DEFAULT_RATIO;
    this.el = opts.element;
    this.ratio = signal(loadRatio(this.ratioKey, this.defaultRatio));
    this.collapsed = signal(loadCollapsed(this.collapsedKey));
  }

  // attach measures the split once drawn and follows its width: the rail
  // or a window change can resize it without a window resize event.
  attach(): void {
    this.measure();
    const el = this.el();
    if (el && typeof ResizeObserver === "function" && !this.resizeObs) {
      this.resizeObs = new ResizeObserver(() => this.measure());
      this.resizeObs.observe(el);
    }
  }

  detach(): void {
    this.resizeObs?.disconnect();
    this.resizeObs = null;
    window.removeEventListener("keydown", this.onDragEscape, true);
  }

  measure(): number {
    const w = this.el()?.clientWidth ?? 0;
    if (w) this.stage.set(w);
    return w;
  }

  onResizeStart(ev: PointerEvent): void {
    if (ev.button !== 0) return;
    this.measure();
    this.dragFrom = this.ratio();
    this.dragWasCollapsed = this.collapsed();
    this.dragging.set(true);
    // Escape cancels a drag wherever focus is: the separator does not take
    // focus on pointerdown, so its own keydown would not see the key.
    window.addEventListener("keydown", this.onDragEscape, true);
    (ev.currentTarget as HTMLElement).setPointerCapture?.(ev.pointerId);
    ev.preventDefault();
  }

  onResizeMove(ev: PointerEvent): void {
    if (!this.dragging()) return;
    const el = this.el();
    const w = this.stage();
    if (!el || !w) return;
    const x = ev.clientX - el.getBoundingClientRect().left;
    if (this.collapsed()) this.collapsed.set(false);
    this.ratio.set(clampRatio(x / w, w));
  }

  private readonly onDragEscape = (ev: KeyboardEvent): void => {
    if (ev.key !== "Escape" || !this.dragging()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.onResizeCancel();
  };

  onResizeEnd(ev?: Event): void {
    if (!this.dragging()) return;
    window.removeEventListener("keydown", this.onDragEscape, true);
    this.dragging.set(false);
    this.dragFrom = null;
    saveRatio(this.ratio(), this.ratioKey);
    saveCollapsed(false, this.collapsedKey);
    (ev?.currentTarget as HTMLElement | undefined)?.releasePointerCapture?.(
      (ev as PointerEvent).pointerId,
    );
  }

  // Escape during a drag puts the divider back where it started.
  onResizeCancel(): void {
    if (!this.dragging()) return;
    window.removeEventListener("keydown", this.onDragEscape, true);
    if (this.dragFrom !== null) this.ratio.set(this.dragFrom);
    if (this.dragWasCollapsed) this.collapsed.set(true);
    this.dragging.set(false);
    this.dragFrom = null;
  }

  reset(): void {
    this.setPreset(this.defaultRatio);
  }

  setPreset(r: number): void {
    const w = this.measure();
    this.ratio.set(w ? clampRatio(r, w) : r);
    this.collapsed.set(false);
    saveRatio(this.ratio(), this.ratioKey);
    saveCollapsed(false, this.collapsedKey);
  }

  toggleCollapsed(): void {
    this.collapsed.update((v) => !v);
    saveCollapsed(this.collapsed(), this.collapsedKey);
  }

  onKey(ev: KeyboardEvent): void {
    if (ev.key === "Enter") {
      ev.preventDefault();
      this.toggleCollapsed();
      return;
    }
    if (ev.key === "Escape" && this.dragging()) {
      ev.preventDefault();
      this.onResizeCancel();
      return;
    }
    const w = this.measure();
    const next = keyStep(ev.key, ev.shiftKey, this.ratio(), w || 1);
    if (next === null) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.collapsed.set(false);
    this.ratio.set(next);
    saveRatio(next, this.ratioKey);
    saveCollapsed(false, this.collapsedKey);
  }
}
