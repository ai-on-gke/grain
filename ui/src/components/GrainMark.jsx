import { useEffect, useRef, useState } from "react";
import { useTheme } from "@mui/material";

import {
  STATIC_SLOT,
  createGrainMark,
  grainSpec,
} from "../brand/grain-mark.js";
import {
  SHEET_FRAMES,
  SHEET_LOOP_MS,
  hasSheet,
  sheetHref,
} from "../brand/mark-sheet.js";

// GrainMark is the brand mark: a Chladni glyph -- the filled regions of a
// square-plate eigenmode, stippled into grains -- inside an invisible
// circular clip. See docs/brand.md.
//
// It renders one of two ways, and which one is the whole point of the
// component:
//
//   still     the static logo, the 2·3 (−) rosette, as a solid fill.
//             It is the SVG scripts/export-brand-assets.mjs writes out
//             of the same module the animation runs, and it is what the
//             mark shows everywhere nothing is happening.
//   animated  the mark moving between the four glyphs, which is what
//             grain shows while agents are actually working. It opens
//             on the rosette -- the figure the still was showing -- so
//             the change reads as the mark coming to life rather than
//             as a different image.
//
// The animation is **pre-recorded** at every size that has a sheet,
// which is every size in the app but the hero. A sheet is one cycle of
// the mark's own animation captured frame by frame out of a browser
// running this same module (scripts/export-mark-sheets.mjs), so playing
// it is not an imitation of the particle system -- it is that system,
// recorded. What the page does per frame is move a mask offset, which
// is compositor work and costs the same whether one mark is on screen
// or twenty. A task list with twenty running rows used to be twenty
// particle systems.
//
// The sheet is an alpha mask rather than a picture, so `background`
// supplies the colour: one file serves both themes and follows a theme
// change with no reload and no second asset.
//
// Every mark also plays from the same point of the same loop, because
// they are pinned to one shared timeline (below) -- so the sidebar and
// every running row scatter and settle together rather than each
// holding its own phase, which the live renderer never did.
//
// The hero has no sheet and runs the live renderer. Its frames would be
// 640px square, which is 80MB of pixels in a strip and past what a
// browser will decode, and it is one canvas on a screen with nothing
// else on it -- the case the pack's renderer was written for. Any size
// without a sheet lands here too, so a new call site works before
// anyone has recorded one for it.
const STILL_SRC = {
  light: "/grain-mark-light.svg",
  dark: "/grain-mark-dark.svg",
};

export default function GrainMark({
  size = 28,
  animated = false,
  title,
  className,
}) {
  const theme = useTheme();
  const mode = theme.palette.mode === "dark" ? "dark" : "light";
  const reducedMotion = usePrefersReducedMotion();
  const canvasRef = useRef(null);
  const sheetRef = useRef(null);
  const plays = hasSheet(size);
  // A live mark that cannot paint (a canvas-less environment -- jsdom
  // under vitest, most obviously) falls back to the still rather than
  // leaving a blank box where the brand should be. A recorded one has
  // no canvas to fail at.
  const [canPaint, setCanPaint] = useState(true);
  const spinning = animated && !reducedMotion && (plays || canPaint);

  // A CSS animation starts when its element is attached, so marks
  // mounting at different moments -- a task row that started running ten
  // seconds after the sidebar -- would each keep their own phase.
  // Pinning every one to the document timeline's origin puts them in
  // lockstep, and keeps them there as rows come and go.
  //
  // Pinning once at mount is not enough, which is what used to let a
  // task list drift apart (grain/task-339). An element that is detached
  // and reattached loses its running animation and starts a new one from
  // the moment of the move, and moving a row is exactly what React does
  // to a keyed <li> when the list it is in reorders -- a task changing
  // state under the "State" sort, a filter dropping the rows above it,
  // a drag landing. The mark never re-rendered in a way that would
  // re-run a mount-time effect, so that row kept whatever phase the move
  // left it on while every other row kept the original one.
  //
  // The browser tells us each time an animation begins, restarts
  // included, so the lock rides on that instead: whenever this element's
  // animation starts, it goes back on the shared timeline. It also
  // covers a mark whose animation had not been created yet when the
  // effect first ran -- one mounted inside a display:none subtree, say,
  // where there is no animation to pin until the subtree is shown.
  useEffect(() => {
    if (!spinning || !plays) return undefined;
    const el = sheetRef.current;
    if (!el) return undefined;
    // Setting a start time the animation already has is a no-op, but
    // checking first keeps this from writing to the animation on every
    // event for no reason.
    const pin = () => {
      for (const animation of el.getAnimations?.() ?? [])
        if (animation.startTime !== 0) animation.startTime = 0;
    };
    pin();
    el.addEventListener("animationstart", pin);
    return () => el.removeEventListener("animationstart", pin);
  }, [spinning, plays, size]);

  useEffect(() => {
    if (!animated || reducedMotion || plays) return undefined;
    const canvas = canvasRef.current;
    if (!canvas) return undefined;
    let ctx = null;
    try {
      ctx = canvas.getContext("2d");
    } catch {
      ctx = null;
    }
    if (!ctx) {
      setCanPaint(false);
      return undefined;
    }

    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.round(size * dpr));
    canvas.height = canvas.width;

    // The module picks grain count and radius off canvas.width, which on
    // a 2x display is twice the size the mark is *seen* at. Reading the
    // spec at the CSS size instead pins the picture to what the reader
    // sees and leaves the backing store free to be as dense as the
    // display allows -- the radius is a fraction of canvas.width, so it
    // already scales itself and needs no dpr correction.
    const spec = grainSpec(size);
    const mark = createGrainMark(canvas, {
      theme: mode,
      count: spec.count,
      radius: spec.radius,
      // Open on the figure the still is already showing.
      slot: STATIC_SLOT,
    });

    // The pack's own note on this renderer is that it is a proof of
    // concept meant for a splash or a hero rather than a persistent UI
    // element. That is now the only place it runs -- but a splash on a
    // tab nobody is looking at is still frames spent for nothing.
    const onVisibility = () => (document.hidden ? mark.stop() : mark.start());
    document.addEventListener("visibilitychange", onVisibility);
    onVisibility();

    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      mark.destroy();
    };
  }, [animated, reducedMotion, plays, size, mode]);

  const label = title || (spinning ? "grain — agents working" : "grain");
  const box = { width: size, height: size, display: "block", flex: "none" };

  if (!spinning) {
    return (
      <img
        className={className}
        src={STILL_SRC[mode]}
        width={size}
        height={size}
        alt=""
        title={label}
        style={box}
      />
    );
  }

  if (plays) {
    return (
      <span
        className={["grain-mark-sheet", className].filter(Boolean).join(" ")}
        ref={sheetRef}
        role="img"
        aria-label={label}
        title={label}
        style={{
          ...box,
          // The keyframes step the mask down one frame at a time, so
          // they need the frame count and the height of a frame --
          // which, with the sheet scaled to the mark's width, is the
          // mark's own size. style.css has the rest.
          "--mark-sheet": `url("${sheetHref(size)}")`,
          "--mark-size": `${size}px`,
          "--mark-frames": SHEET_FRAMES,
          "--mark-loop": `${SHEET_LOOP_MS}ms`,
        }}
      />
    );
  }

  return (
    <canvas
      className={className}
      ref={canvasRef}
      role="img"
      aria-label={label}
      title={label}
      style={box}
    />
  );
}

// The mark's whole reason for animating is to signal activity, so a
// reader who has asked for less motion gets the still -- the sidebar
// still says how many tasks are running, in words, right below it.
function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(() => matches());
  useEffect(() => {
    const mq = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    if (!mq?.addEventListener) return undefined;
    const onChange = () => setReduced(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return reduced;
}

function matches() {
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    // matchMedia is not everywhere (jsdom needs it stubbed); no query
    // answered means no preference expressed.
    return false;
  }
}
