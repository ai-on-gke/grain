import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import GrainMark from "./GrainMark.jsx";
import {
  SHEET_FRAMES,
  SHEET_LOOP_MS,
  SHEET_SIZES,
} from "../brand/mark-sheet.js";

// A mark small enough to have a sheet plays a recording and needs no
// canvas. Only the hero-sized live renderer does, and jsdom has none
// (setupTests.js), so this is the smallest context object grain-mark.js
// touches: it clears, clips to the frame, fills a circle per grain, and
// reads nothing back.
function stubCanvas() {
  const ctx = {
    clearRect: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    beginPath: vi.fn(),
    closePath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    arc: vi.fn(),
    clip: vi.fn(),
    fill: vi.fn(),
    stroke: vi.fn(),
  };
  HTMLCanvasElement.prototype.getContext = () => ctx;
  return ctx;
}

function stubMatchMedia(reduced) {
  window.matchMedia = (query) => ({
    matches: query === "(prefers-reduced-motion: reduce)" && reduced,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  });
}

/**
 * jsdom has no Web Animations API; this is the sliver the phase-lock
 * uses. One animation per element, in a map the test can reach into to
 * play the browser's part -- replacing an element's animation with a
 * fresh one is what a restart looks like from here.
 */
function stubAnimations() {
  const animations = new Map();
  Element.prototype.getAnimations = function getAnimations() {
    if (!this.classList.contains("grain-mark-sheet")) return [];
    if (!animations.has(this)) animations.set(this, { startTime: null });
    return [animations.get(this)];
  };
  return animations;
}

afterEach(() => {
  HTMLCanvasElement.prototype.getContext = () => null;
  delete Element.prototype.getAnimations;
  delete window.matchMedia;
});

const marked = () =>
  screen.getByRole("img", { name: "grain — agents working" });

describe("GrainMark", () => {
  it("shows the fixed mark as an image when nothing is animating", () => {
    render(<GrainMark size={24} title="grain" />);

    const img = screen.getByTitle("grain");
    expect(img.tagName).toBe("IMG");
    expect(img).toHaveAttribute("src", "/grain-mark-light.svg");
  });

  it("plays the recorded sheet at a size that has one", () => {
    // The sheet is a mask, so the element carries no image of its own --
    // what it carries is the strip, the frame count and the loop, which
    // are what style.css steps the mask by. Those three have to come
    // from mark-sheet.js rather than be written twice, or the animation
    // and the file it plays would drift.
    render(<GrainMark size={20} animated />);

    const mark = marked();
    expect(mark).toHaveClass("grain-mark-sheet");
    expect(mark.style.getPropertyValue("--mark-sheet")).toBe(
      'url("/grain-mark-20.png")',
    );
    expect(mark.style.getPropertyValue("--mark-frames")).toBe(
      String(SHEET_FRAMES),
    );
    expect(mark.style.getPropertyValue("--mark-loop")).toBe(
      `${SHEET_LOOP_MS}ms`,
    );
    // A frame is as tall as the mark is wide, which is what makes
    // stepping the mask by --mark-size step exactly one frame.
    expect(mark.style.getPropertyValue("--mark-size")).toBe("20px");
  });

  it("plays every recorded size, and paints the rest", () => {
    // Whatever sizes have sheets, play them; anything else falls through
    // to the live renderer rather than asking for a file that is not
    // there. That is what lets a new call site work before a sheet has
    // been recorded for its size.
    for (const size of SHEET_SIZES) {
      const { unmount } = render(<GrainMark size={size} animated />);
      expect(marked()).toHaveClass("grain-mark-sheet");
      unmount();
    }

    stubCanvas();
    render(<GrainMark size={320} animated />);
    expect(marked().tagName).toBe("CANVAS");
  });

  it("pins every played mark to the same point of the same loop", () => {
    // A CSS animation starts when its element is attached, so a task row
    // that started running long after the sidebar would otherwise hold
    // its own phase and the two would scatter at different moments.
    const animations = stubAnimations();
    render(<GrainMark size={20} animated />);

    expect(animations.get(marked()).startTime).toBe(0);
  });

  it("pins a mark again every time its animation starts over", () => {
    // Pinning at mount alone let a task list drift apart
    // (grain/task-339): React moves the DOM node of a keyed row the list
    // reorders, an element that is detached and reattached starts its
    // animation again from the moment of the move, and nothing re-ran
    // the lock -- so that one row kept a phase of its own.
    const animations = stubAnimations();
    render(<GrainMark size={20} animated />);
    const mark = marked();
    expect(animations.get(mark).startTime).toBe(0);

    // What the browser hands the element after a move: a new animation,
    // started now, announced by animationstart.
    animations.set(mark, { startTime: 4200 });
    fireEvent.animationStart(mark);

    expect(animations.get(mark).startTime).toBe(0);
  });

  it("paints a hero-sized mark live, since it has no sheet", () => {
    const ctx = stubCanvas();
    render(<GrainMark size={320} animated />);

    // It drew its first frame on mount rather than waiting for an
    // animation frame a test environment never delivers.
    expect(ctx.fill).toHaveBeenCalled();
  });

  it("falls back to the fixed mark when the reader asked for less motion", () => {
    stubMatchMedia(true);
    render(<GrainMark size={20} animated title="grain" />);

    expect(screen.getByTitle("grain").tagName).toBe("IMG");
  });

  it("falls back to the fixed mark when a live mark has no canvas to paint on", () => {
    // Only the sizes without a sheet can fail this way, since only they
    // need a canvas at all.
    render(<GrainMark size={320} animated title="grain" />);

    expect(screen.getByTitle("grain").tagName).toBe("IMG");
  });
});
