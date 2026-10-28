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

// Shared ordering for the anonymous child rows of a board. Panels and
// report sections are both named by their position, replaced as a whole by
// PutBoard. A zero among otherwise numbered children is an ordinary key;\n// only a set whose keys are all zero is treated as unnumbered.
export const CHILD_ORDER_SPACING = 1 << 20;
const MIN_ORDER_KEY_GAP = 1e-6;

export function sameChildPlace(children, key, beforeKey) {
  const at = children.findIndex((child) => child.key === key);
  if (at < 0) return false;
  if (beforeKey === key) return true;
  const next = children[at + 1];
  return (beforeKey ?? null) === (next ? next.key : null);
}

export function childPlaces(children, key) {
  if (!children.some((child) => child.key === key)) return [];
  const places = children
    .filter((child) => child.key !== key)
    .map((child) => child.key);
  places.push(null);
  return places.filter((before) => !sameChildPlace(children, key, before));
}

function keyBetween(lower, upper) {
  if (lower === null && upper === null) return 0;
  if (lower === null) return upper - CHILD_ORDER_SPACING;
  if (upper === null) return lower + CHILD_ORDER_SPACING;
  if (upper - lower >= MIN_ORDER_KEY_GAP) return lower + (upper - lower) / 2;
  return null;
}

export function childMove(children, key, beforeKey, row) {
  const at = children.findIndex((child) => child.key === key);
  if (at < 0) return null;
  const before = beforeKey ?? null;
  if (sameChildPlace(children, key, before)) return null;
  const rest = children.filter((child) => child.key !== key);
  let to =
    before === null
      ? rest.length
      : rest.findIndex((child) => child.key === before);
  if (to < 0) to = rest.length;
  const next = [...rest.slice(0, to), children[at], ...rest.slice(to)];
  const landing = keyBetween(
    to > 0 ? rest[to - 1].orderKey : null,
    to < rest.length ? rest[to].orderKey : null,
  );
  if (landing !== null)
    return next.map((child) =>
      row(child, child.key === key ? landing : child.orderKey),
    );
  return next.map((child, i) => row(child, i * CHILD_ORDER_SPACING));
}

// childRenderKeys is what a board's children are keyed by in the render,
// one per child in order. Not `child.key`: that is the child's position
// (`panel-2`), which is what a move names, but a React key that is a
// position keeps every element where it was and hands it the next
// child's contents when the order changes. The handle a keyboard just
// put a panel down with then stays focused in the old place, now
// labelled with whichever panel moved into it. The row as stored is the
// closest thing a child has to an identity, so the key is that, counted
// to tell apart two children whose rows are the same.
export function childRenderKeys(children) {
  const seen = new Map();
  return children.map((child) => {
    const id = child.stored ? JSON.stringify(child.stored) : child.key;
    const n = seen.get(id) || 0;
    seen.set(id, n + 1);
    return `${id}#${n}`;
  });
}
