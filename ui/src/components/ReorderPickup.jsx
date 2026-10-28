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

import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@mui/material";
import DragIndicatorIcon from "@mui/icons-material/DragIndicator";

// ReorderPickup is reordering for a finger: pick a row up, then tap
// where it goes (grain/task-23).
//
// # Why a second gesture at all
//
// Every list in this UI that has an order of its own -- the backlog in
// TaskList and TaskBoard, the four display orders in
// ListPrimitives.jsx's ReorderableList -- is reordered by dragging, and
// dragging here means the HTML5 drag events: draggable rows,
// dragstart/dragover/drop. A phone browser fires none of them. A touch
// on a draggable row scrolls the page, exactly as it would on any other
// row, and no amount of patience turns it into a drag.
//
// So the handle was hidden on phones and the order was simply not
// editable there (the rule this replaces in style.css said as much in
// as many words). That is a real hole: the backlog is the one order in
// grain that decides what actually runs next, and "which task runs
// next" is the archetypal thing somebody settles from a phone.
//
// # Why tapping rather than a touch drag
//
// The other way to close the hole is to reimplement dragging on pointer
// events -- long-press to lift, follow the finger, hit-test the row
// under it, auto-scroll near the edges. It is a lot of machinery, none
// of it is testable in jsdom (no layout, no elementFromPoint), and it
// fights the browser for the same gesture the page uses to scroll.
//
// Picking up and putting down costs two taps and needs none of that:
//
//   - it is two ordinary buttons, so it works with a finger, a mouse, a
//     keyboard and a screen reader without a line of code per input
//     device. Reordering was drag-only until now, which means it was
//     also unreachable from the keyboard;
//   - the drop targets are visible and the size of a thumb, rather than
//     a 2px rule you have to hold a finger over something to find;
//   - the list still scrolls while a row is picked up, which is what a
//     long list needs and what a touch drag takes away.
//
// The existing drag is untouched. On a mouse it remains the faster
// gesture for a short move, and every drag test still describes it; this
// is the gesture that works where that one cannot, offered on the same
// handle so there is one affordance to find rather than two.
//
// # Dragging it there anyway (grain/task-51)
//
// Two taps is a gesture that always works, and it is not the gesture
// anybody arrives with: "press the handle and drag the row where it
// goes" is what a finger tries first, and on an iPad it did nothing at
// all -- which is what the report this section answers said, after the
// tap gesture above had shipped. A drop target nobody thinks to look
// for is not much better than no drop target.
//
// So the handle answers a finger directly, and it does it by *driving
// the gesture above* rather than by reimplementing the drop:
//
//   - pressing the handle picks the row up, exactly as tapping it does,
//     which puts a "Move here" slot at every position that row can go
//     to -- the drop targets are already on screen, laid out, and
//     already the only positions the list will accept;
//   - moving the finger marks the slot nearest to it, so hit-testing is
//     "which of these rectangles is closest" rather than a search
//     through the rows for the gap a drop would mean;
//   - lifting the finger presses that slot, so the move itself is the
//     same MoveSlot onClick a tap ends in. Every list gets the drag
//     without a line of code of its own, and a drag cannot land
//     anywhere a tap could not.
//
// A press that never travels DRAG_SLOP is left alone as the tap it has
// always been, so both gestures share the handle: press and drag, or tap
// and tap. Lifting the finger away from every slot moves nothing and
// leaves the row picked up, bar and Cancel still on screen -- a drag
// that ended in the wrong place should not move anything, and should not
// make somebody start over either.
//
// Touch and pen only. A mouse still reorders through the HTML5 drag
// events the rows carry, which come with a drag image, a cursor and the
// platform's own autoscroll; there is nothing here worth trading those
// for.
//
// # What the pieces are
//
// usePickup holds "what is picked up", MoveHandle is the affordance that
// picks it up, MoveSlot is one of the places it can go, and MovingBar is
// the way out. The lists themselves keep deciding what a slot *means* --
// TaskList hands its neighbours to Store.Reorder, ReorderableList
// rewrites a stored display order -- because that is the part no two of
// them agree on.

// usePickup is the state behind the gesture: null when nothing is picked
// up, or { ids, scope } for the block of rows that is.
//
// `scope` is whatever the caller needs to know *where* the rows were
// lifted from -- TaskBoard passes the column id, because a card can only
// be put back into the column it came from (a task's state is derived,
// not set, so there is no such thing as dropping it into "Running"), and
// the lists with one flat order pass nothing.
//
// Escape cancels from anywhere, the way it closes an overlay: once a row
// is up, every slot on screen is a button, and a picked-up row somebody
// has changed their mind about should not need them to find the one
// button that says so.
export function usePickup() {
  const [picked, setPicked] = useState(null);

  useEffect(() => {
    if (!picked) return undefined;
    const onKey = (e) => {
      if (e.key === "Escape") setPicked(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [picked]);

  // cancel and toggle keep the same identity for the life of the list,
  // so a caller can drop them into an effect's dependencies -- which is
  // what every list here does to put a picked-up row down again when the
  // sort it was picked up under stops being the one on screen.
  const cancel = useCallback(() => setPicked(null), []);

  // toggle is what a handle does: pick this block up, or put it back
  // down untouched if it is the block already up. Tapping the same
  // handle twice is the obvious way to change your mind, and it is the
  // only way that needs nothing but the row you started from.
  const toggle = useCallback(
    (ids, scope = null) =>
      setPicked((p) => (p && sameIds(p.ids, ids) ? null : { ids, scope })),
    [],
  );

  return { picked, toggle, cancel };
}

function sameIds(a, b) {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}

// gapsOf turns "these rows are picked up" into the drop slots worth
// offering, as indexes into `kept` (the rows in display order with the
// picked ones taken out): slot g puts the block between kept[g-1] and
// kept[g], and the slot at kept.length puts it at the end.
//
// The gap the block already occupies is left out. Offering it would be a
// button that visibly promises a move and does nothing, which reads as a
// bug in the list rather than as a no-op -- and for a single row picked
// up out of the middle of a list, that dead slot is one of the two
// sitting right next to the row somebody just touched.
export function gapsOf(order, pickedIds) {
  const inOrder = new Set(order);
  const present = pickedIds.filter((id) => inOrder.has(id));
  if (present.length === 0) return [];
  const picked = new Set(present);
  const kept = order.filter((id) => !picked.has(id));
  const gaps = [];
  for (let g = 0; g <= kept.length; g += 1) {
    const next = [...kept.slice(0, g), ...present, ...kept.slice(g)];
    if (!sameIds(next, order)) gaps.push(g);
  }
  return gaps;
}

// How far a finger has to travel before a press on the handle is a drag
// rather than a tap. Eight pixels is about what a still finger wanders
// on a screen it is only resting on.
const DRAG_SLOP = 8;
// How near a slot the finger has to be when it lifts for that to count
// as a drop into it. Generous on purpose: the slots are a row's height
// apart, so this makes the whole list one continuous target and only
// runs out somewhere that is plainly not the list -- a toolbar, another
// column, the space beside a short list.
const DROP_REACH = 56;
// The band at each end of the scrolling list that carries the list along
// with a finger held in it, and how far it moves per frame at the very
// edge. A drag that cannot scroll can only reach as far as one screen.
const EDGE_BAND = 72;
const EDGE_SPEED = 16;

// How long after a finger leaves a click still counts as that same
// touch's own, and so as one this gesture has already answered.
const CLICK_GRACE = 700;

const SLOT_SELECTOR = ".reorder-slot-button";
const OVER_CLASS = "reorder-slot-button-over";

// nearestSlot answers "which of these slots is the finger at", by
// distance from the point to each rectangle -- zero when the point is
// inside one -- with `reach` as how far outside a slot still counts.
// Returns an index into `rects`, or -1 for a point that is near none of
// them, which is a drag ending nowhere rather than a drop.
//
// Distance to the rectangle rather than to its middle: the slots are
// full-width buttons a couple of rows apart, so a finger three rows down
// the list is genuinely nearer the gap it is sitting over than the one
// whose centre happens to be closer.
export function nearestSlot(rects, x, y, reach = DROP_REACH) {
  let best = -1;
  let bestDist = Infinity;
  rects.forEach((r, i) => {
    const dx = Math.max(r.left - x, 0, x - r.right);
    const dy = Math.max(r.top - y, 0, y - r.bottom);
    const dist = Math.hypot(dx, dy);
    if (dist < bestDist) {
      bestDist = dist;
      best = i;
    }
  });
  return bestDist <= reach ? best : -1;
}

// edgeScroll is how far the list should move this frame for a finger at
// y, given where the scrolling box starts and ends: negative to scroll
// up, positive down, 0 for a finger in the middle of it. It ramps with
// how deep into the band the finger is, so resting just inside the edge
// creeps and holding at the very edge moves properly.
//
// A box too short to hold three bands is left alone: on a list that is
// barely taller than the bands themselves, every position is an edge,
// and a list that scrolls whenever it is touched is worse than one that
// does not scroll at all.
export function edgeScroll(
  top,
  bottom,
  y,
  band = EDGE_BAND,
  speed = EDGE_SPEED,
) {
  if (bottom - top < band * 3) return 0;
  if (y < top + band)
    return -Math.ceil(Math.min((top + band - y) / band, 1) * speed);
  if (y > bottom - band)
    return Math.ceil(Math.min((y - (bottom - band)) / band, 1) * speed);
  return 0;
}

// The slots on screen right now, found in the DOM rather than passed in:
// the lists render them (MoveSlot above) at whatever depth suits them,
// and a drag needs the rectangles they ended up at, which is a question
// only the DOM can answer. Only the picked-up block's own list has any
// slots at all, so "every slot on the page" is already the right set.
function slotsNow() {
  return [...document.querySelectorAll(SLOT_SELECTOR)];
}

// The mark on the slot a release would drop into. Set on the element
// rather than through React state so that finding the target costs no
// render at all -- and safely, since these buttons' own className never
// changes while a row is up, so React has nothing to write back over it.
function markOver(slots, target) {
  for (const el of slots) el.classList.toggle(OVER_CLASS, el === target);
}

// The box the slots scroll inside, so a drag can reach past the bottom
// of the screen: the nearest ancestor that actually scrolls (a page's
// <main>, a board column). Null when nothing does, and the drag simply
// does not scroll.
function scrollerOf(el) {
  for (let n = el?.parentElement; n; n = n.parentElement) {
    const overflow = window.getComputedStyle(n).overflowY;
    if (
      (overflow === "auto" || overflow === "scroll") &&
      n.scrollHeight > n.clientHeight
    )
      return n;
  }
  return null;
}

// swallowNextClick eats the one compatibility click a browser may fire
// after the finger that drove this gesture has left, wherever it lands.
//
// The handle refuses the press (`preventDefault()` on `pointerdown`),
// which is enough to stop the compatibility mousedown and mouseup in
// both engines and the click as well in WebKit -- but Chromium fires
// the click anyway, and it fires it at whatever is under the point
// *now* rather than at what the press began on. Picking a row up moves
// what is under the point: a "Move here" slot is inserted above it and
// the bar appears over the list, so the row slides down by a row's
// height or more and the click lands on whatever took its place. On a
// board that is the card above, and clicking a card opens the task --
// so tapping a card's handle on Android opened somebody else's task
// over the board it was picked up from. MoveHandle's own claimClick
// below has always covered the click that lands back on the handle;
// this covers the rest of the page, which is where a shifted layout
// sends it.
//
// One click, in the capture phase so nothing else sees it first, and
// only a trusted one: the drop this gesture performs is a `.click()` of
// its own on a slot (onUp), which must go through. It is dropped again
// on the next `pointerdown`, because a fresh press is a deliberate
// second tap rather than the tail of the first -- a compatibility click
// arrives with no press of its own -- and CLICK_GRACE later at the
// latest, for the engines that fire no such click at all.
function swallowNextClick() {
  const done = () => {
    clearTimeout(timer);
    document.removeEventListener("click", onClick, true);
    document.removeEventListener("pointerdown", done, true);
  };
  const onClick = (e) => {
    if (!e.isTrusted) return;
    e.preventDefault();
    e.stopPropagation();
    done();
  };
  const timer = setTimeout(done, CLICK_GRACE);
  document.addEventListener("click", onClick, true);
  document.addEventListener("pointerdown", done, true);
}

function retarget(g) {
  const slots = slotsNow();
  const i = nearestSlot(
    slots.map((el) => el.getBoundingClientRect()),
    g.x,
    g.y,
  );
  g.target = i < 0 ? null : slots[i];
  markOver(slots, g.target);
}

// useFingerDrag is the gesture described at the top of this file, as the
// handful of handlers a handle needs to carry it. `g` is everything one
// press has to remember, in a ref rather than in state: it is read and
// written by window listeners registered once per press, several times a
// frame, and none of it is anything to render.
function useFingerDrag({ picked, disabled, onToggle }) {
  // null while nothing is happening, "press" once a finger is down and
  // "drag" once it has travelled -- state, unlike the rest, because it
  // is what subscribes to the window and what the handle is drawn from.
  const [phase, setPhase] = useState(null);
  const g = useRef(null);
  // When a finger was last on this handle, for the sake of the click a
  // browser may fire anyway despite the press being cancelled below --
  // this gesture has already answered it by then. A time rather than a
  // flag so that only the compatibility click that follows straight
  // after a touch is swallowed, and never, say, an Enter pressed on the
  // same handle a minute later on a laptop with a touchscreen.
  const touchedAt = useRef(0);
  // The listeners below outlive the render that registered them, so what
  // they need of the caller is read from here rather than closed over.
  const latest = useRef(null);
  latest.current = { picked, onToggle };

  const stop = () => {
    if (g.current?.raf) cancelAnimationFrame(g.current.raf);
    g.current = null;
    setPhase(null);
  };

  // Nothing is left running or marked if this row goes away mid-drag --
  // a poll that reorders the list underneath the finger, a filter that
  // drops the row, a pane that replaces the page.
  useEffect(() => () => stop(), []);

  useEffect(() => {
    if (!phase) return undefined;

    const scroll = () => {
      const it = g.current;
      if (!it || !it.scroller) return;
      const box = it.scroller.getBoundingClientRect();
      const dy = edgeScroll(box.top, box.bottom, it.y);
      if (dy) {
        it.scroller.scrollTop += dy;
        // The slots moved under a finger that did not: what is under it
        // now is a different one.
        retarget(it);
      }
      it.raf = requestAnimationFrame(scroll);
    };

    const onMove = (e) => {
      const it = g.current;
      if (!it || e.pointerId !== it.id) return;
      it.x = e.clientX;
      it.y = e.clientY;
      if (!it.dragged) {
        if (Math.hypot(it.x - it.x0, it.y - it.y0) < DRAG_SLOP) return;
        it.dragged = true;
        setPhase("drag");
        if (it.scroller) it.raf = requestAnimationFrame(scroll);
      }
      retarget(it);
    };

    const onUp = (e) => {
      const it = g.current;
      if (!it || e.pointerId !== it.id) return;
      const target = it.target;
      const dragged = it.dragged;
      const wasPicked = it.wasPicked;
      touchedAt.current = Date.now();
      swallowNextClick();
      stop();
      markOver(slotsNow(), null);
      if (dragged) {
        // The drop is the slot's own click: whatever this list does with
        // a tapped slot, it does with a dropped one.
        if (target?.isConnected) target.click();
      } else if (wasPicked) {
        // A press that never moved is the tap this handle has always
        // answered -- and on a row that was already up, that is the
        // second tap, which puts it back down.
        latest.current.onToggle();
      }
    };

    const onCancel = (e) => {
      const it = g.current;
      if (it && e.pointerId !== it.id) return;
      touchedAt.current = Date.now();
      swallowNextClick();
      stop();
      markOver(slotsNow(), null);
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onCancel);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onCancel);
    };
  }, [phase]);

  const onPointerDown = (e) => {
    if (e.pointerType === "mouse") {
      // A mouse press is the start of the HTML5 drag, or of the click
      // that picks the row up: either way this gesture stays out of it,
      // and the click it ends in is one to answer rather than to
      // swallow.
      touchedAt.current = 0;
      return;
    }
    if (disabled || g.current) return;
    // Refusing the press is what stops the browser turning it into a
    // click, a text selection, iOS's press-and-hold callout, or the
    // native drag of the row this handle sits in. The gesture answers
    // the press itself, so none of those are wanted.
    e.preventDefault();
    e.stopPropagation();
    touchedAt.current = Date.now();
    g.current = {
      id: e.pointerId,
      x0: e.clientX,
      y0: e.clientY,
      x: e.clientX,
      y: e.clientY,
      dragged: false,
      wasPicked: !!picked,
      target: null,
      scroller: scrollerOf(e.currentTarget),
      raf: 0,
    };
    setPhase("press");
    // Picked up on the way down, not on release: the slots have to be on
    // screen before the finger starts looking for one.
    if (!picked) onToggle();
  };

  return {
    dragging: phase === "drag",
    onPointerDown,
    // A drag of the row this handle sits in, starting from the handle
    // itself, would be the browser competing with the gesture in flight
    // for the same finger (iPadOS Safari does start one from a long
    // press). A mouse never gets here with a gesture open, so its drag
    // is untouched.
    onDragStart: (e) => {
      if (g.current) e.preventDefault();
    },
    claimClick: () => {
      // A browser's compatibility click arrives within a frame or two
      // of the finger lifting; CLICK_GRACE is long enough for a slow one
      // and far short of anything a person does next.
      const claim = Date.now() - touchedAt.current < CLICK_GRACE;
      touchedAt.current = 0;
      return claim;
    },
  };
}

// MoveHandle is the handle every reorderable row already carried, as a
// button: the same figure in the same column, and still the thing a
// mouse grabs to drag, but now also the thing a finger taps to lift the
// row. aria-pressed says which of the two states it is in, so a screen
// reader announces the pick-up rather than leaving the slots that appear
// underneath unexplained.
//
// The click never reaches the row (all five lists open the thing the row
// names when it is clicked), which is the same stopPropagation the plain
// icon needed before this was a button at all.
// `disabled` is for the row that has nowhere to go -- the only task in
// its list, the only card in its column. Picking it up would put a bar
// on screen offering no slots at all, which reads as a broken gesture
// rather than as a list with one row in it, so the handle says up front
// that there is nothing to do here.
//
// Putting a row down hands focus back to its handle. The slot that was
// pressed -- or focused when Escape cancelled -- is gone by the next
// render, and a focused element that leaves the document leaves focus on
// <body>: a keyboard user who had just moved a row was thrown back to
// the top of the page, with the row they moved somewhere below. Only
// when focus really was lost, though: a put-down that finds focus on
// something else (another handle, a field somebody clicked into) leaves
// it there, which is also what makes the first of several picked-up
// handles the one that takes it.
//
// `landed` is the same promise for a row its list draws somewhere new
// once it has moved: a board put into another folder is a fresh <li>
// under another heading (BoardList.jsx), so the handle that was put down
// is unmounted when the list catches up and focus goes with it. The
// handle that mounts in its place takes focus back on the same terms --
// only from <body> (2026-10-12 boards walkthrough).
export function MoveHandle({ label, picked, disabled, onToggle, landed }) {
  const finger = useFingerDrag({ picked, disabled, onToggle });
  const ref = useRef(null);
  const wasPicked = useRef(picked);
  useEffect(() => {
    const putDown = wasPicked.current && !picked;
    wasPicked.current = picked;
    if (!putDown || !ref.current) return;
    const active = document.activeElement;
    if (!active || active === document.body) {
      ref.current.focus({ preventScroll: true });
    }
  }, [picked]);
  useEffect(() => {
    if (!landed || !ref.current) return;
    const active = document.activeElement;
    if (!active || active === document.body) {
      ref.current.focus({ preventScroll: true });
    }
  }, [landed]);
  return (
    <button
      ref={ref}
      type="button"
      className={`task-drag-handle${picked ? " task-drag-handle-picked" : ""}${finger.dragging ? " task-drag-handle-dragging" : ""}`}
      aria-label={label}
      aria-pressed={picked}
      disabled={disabled}
      title={
        disabled
          ? "Nowhere to move this: it is the only one here"
          : picked
            ? "Tap a “Move here” slot, or tap again to leave it where it is"
            : "Drag this to reorder, or tap it to pick this up and choose where it goes"
      }
      onPointerDown={finger.onPointerDown}
      onDragStart={finger.onDragStart}
      onClick={(e) => {
        e.stopPropagation();
        // A finger's press has already been answered by the time any
        // click arrives from it (useFingerDrag).
        if (finger.claimClick()) return;
        onToggle();
      }}
    >
      <DragIndicatorIcon fontSize="small" />
    </button>
  );
}

// MoveSlot is one place the picked-up rows can go: an element of its own
// between two rows, because that is where the block would land.
//
// It is a full-width button rather than a hairline, and its height is a
// touch target rather than a rule's: the whole point of this gesture is
// that the drop targets are things you can hit with a thumb.
//
// `as` is the element that wraps it, and it is an <li> by default
// because a list is the only thing allowed between a <ul> and its rows,
// which is what four of the five callers are. A dashboard's panels are
// not a list -- they are a wrapping grid of <section>s (PanelBoard.jsx)
// -- so that one asks for a <div>. Nothing else changes with it: the
// finger drag finds its targets by the button's own class
// (SLOT_SELECTOR), not by what they sit in.
export function MoveSlot({ label, className, onMove, as: Wrapper = "li" }) {
  return (
    <Wrapper className={`reorder-slot${className ? ` ${className}` : ""}`}>
      <button
        type="button"
        className="reorder-slot-button"
        aria-label={label}
        onClick={(e) => {
          e.stopPropagation();
          onMove();
        }}
      >
        Move here
      </button>
    </Wrapper>
  );
}

// MovingBar says out loud what is happening and offers the way out.
// role="status" so a screen reader hears it when it appears, and sticky
// (style.css) so the way out is still on screen after scrolling a long
// list looking for the right slot.
//
// `nowhere` is the one case where there is nothing to choose from: a
// block that is every row there is has no gap left to move into, and a
// bar telling somebody to pick a slot when the list is offering none is
// worse than saying so.
export function MovingBar({ count, noun = "task", nowhere, onCancel }) {
  const what = `${count} ${count === 1 ? noun : `${noun}s`}`;
  return (
    <div className="reorder-bar" role="status">
      <span className="reorder-bar-note">
        {nowhere
          ? `Moving ${what} — nowhere else to put ${count === 1 ? "it" : "them"}`
          : `Moving ${what} — choose where ${count === 1 ? "it goes" : "they go"}`}
      </span>
      <Button size="small" className="reorder-bar-cancel" onClick={onCancel}>
        Cancel
      </Button>
    </div>
  );
}
