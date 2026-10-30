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

import { useLayoutEffect, useRef } from "react";

// A list row's chip strip (style.css's .task-row .chips): it wraps, and
// is clipped to one line's height, so a chip that does not fit is laid
// out on a second line nobody sees. What the browser will not do is
// narrow the strip to the chips left on the first line -- a wrapping
// flex container keeps the whole width the row gave it, and the width
// the wrapped chip vacated stood as a blank gap at the end of the strip,
// before the row's phase chip ("live run"), while the title and status
// beside it were ellipsised for want of exactly that width.
//
// So the chips that wrapped are taken out of the layout (OVERFLOW,
// display: none), and the strip is only as wide as what it shows: the
// rest goes back to the title and status. Which ones they are is a
// question of layout, so it is asked again whenever the answer could
// change -- the row is rendered, or changes width, or the fonts load.

export const OVERFLOW = "chip-overflow";

// The index of the first chip the strip has wrapped onto a later line,
// or -1 if all of them are on its first one. Every chip after it goes
// too, wrapped or not: the strip loses its last chips first (the order
// each row puts them in says why), and a narrow chip pulled forward
// into the gap a wide one left would be a strip that reorders itself as
// the window is resized.
export function firstWrapped(strip) {
  const chips = strip.children;
  if (chips.length < 2) return -1;
  const top = chips[0].getBoundingClientRect().top;
  for (let i = 1; i < chips.length; i++) {
    // A pixel of slack: chips on one line share a top, and a wrapped one
    // is a whole line plus the row gap below it.
    if (chips[i].getBoundingClientRect().top > top + 1) return i;
  }
  return -1;
}

// Fits a batch of strips at once: every chip is put back, then every
// strip is measured, then the overflow is hidden -- so a list of rows
// costs one layout rather than one per row.
export function fitChipStrips(strips) {
  for (const strip of strips) {
    for (const chip of strip.children) chip.classList.remove(OVERFLOW);
  }
  const cuts = strips.map(firstWrapped);
  strips.forEach((strip, i) => {
    if (cuts[i] < 0) return;
    const chips = strip.children;
    for (let j = cuts[i]; j < chips.length; j++) {
      chips[j].classList.add(OVERFLOW);
    }
  });
}

// Strips waiting to be fitted, and the rows they sit on, which are what
// change width: the strip itself only ever gets narrower when we hide
// a chip in it.
const pending = new Set();
const stripOfRow = new Map();
let flushQueued = false;
let resizeObserver = null;

function flush() {
  flushQueued = false;
  const strips = [...pending].filter((s) => s.isConnected);
  pending.clear();
  if (strips.length) fitChipStrips(strips);
}

// A microtask runs before the browser paints, so a strip fitted after
// a render is never shown with its wrapped gap first.
function schedule(strip) {
  pending.add(strip);
  if (flushQueued) return;
  flushQueued = true;
  queueMicrotask(flush);
}

// Resizes are fitted on the next frame rather than in the observer's own
// callback: hiding a chip moves the row's other content, and a layout
// change inside a ResizeObserver callback is reported as a loop error.
let frameQueued = false;
function scheduleFrame(strip) {
  pending.add(strip);
  if (frameQueued) return;
  frameQueued = true;
  requestAnimationFrame(() => {
    frameQueued = false;
    flush();
  });
}

function observeRow(row, strip) {
  if (typeof ResizeObserver === "undefined") return;
  if (!resizeObserver) {
    const widths = new WeakMap();
    resizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        // Only a change of width can change what fits: a row growing
        // taller is not a reason to measure it again.
        const width = entry.contentRect.width;
        if (widths.get(entry.target) === width) continue;
        widths.set(entry.target, width);
        const s = stripOfRow.get(entry.target);
        if (s) scheduleFrame(s);
      }
    });
    document.fonts?.ready?.then(() => {
      for (const s of stripOfRow.values()) schedule(s);
    });
  }
  stripOfRow.set(row, strip);
  resizeObserver.observe(row);
}

function unobserveRow(row) {
  stripOfRow.delete(row);
  resizeObserver?.unobserve(row);
}

// The strip itself: a .chips span that hides the chips its row has no
// room for, rather than leaving their width behind as a gap. Only for a
// strip that is clipped to one line -- one that wraps to show every chip
// (RepoPage's header) wants the plain span.
export default function ChipStrip({ className, children, ...rest }) {
  const ref = useRef(null);
  // After every render: React rewriting a chip's className puts a hidden
  // chip back, and new chips, or a new title or status beside them,
  // change what fits.
  useLayoutEffect(() => {
    if (ref.current) schedule(ref.current);
  });
  useLayoutEffect(() => {
    const strip = ref.current;
    const row = strip?.parentElement;
    if (!row) return undefined;
    observeRow(row, strip);
    return () => {
      pending.delete(strip);
      unobserveRow(row);
    };
  }, []);
  return (
    <span
      ref={ref}
      className={className ? `chips ${className}` : "chips"}
      {...rest}
    >
      {children}
    </span>
  );
}
