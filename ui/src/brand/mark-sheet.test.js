import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import {
  SHEET_DPR,
  SHEET_FPS,
  SHEET_FRAMES,
  SHEET_LOOP_MS,
  SHEET_SIZES,
  hasSheet,
  sheetHref,
} from "./mark-sheet.js";

// The committed sheets and the constants the player steps them by are
// two halves of one thing, written by different tools -- the exporter
// and style.css. Nothing at runtime notices if they disagree: the
// animation just plays the wrong number of frames, or steps by the
// wrong amount, and the mark stutters or drifts out of its loop. So the
// files are checked against the contract here.

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = join(here, "..", "..", "public");

/** Width and height out of a PNG's IHDR, which is always the first chunk. */
function pngSize(file) {
  const b = readFileSync(file);
  expect(b.subarray(0, 8)).toEqual(
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  );
  expect(b.toString("ascii", 12, 16)).toBe("IHDR");
  return {
    width: b.readUInt32BE(16),
    height: b.readUInt32BE(20),
    colourType: b[25],
  };
}

describe("the recorded sheets", () => {
  it("are on disk for every size that claims one", () => {
    for (const size of SHEET_SIZES) {
      expect(hasSheet(size)).toBe(true);
      expect(() =>
        readFileSync(join(publicDir, sheetHref(size).replace(/^\//, ""))),
      ).not.toThrow();
    }
  });

  it("carry the frames and the shape the player steps them by", () => {
    for (const size of SHEET_SIZES) {
      const { width, height, colourType } = pngSize(
        join(publicDir, sheetHref(size).replace(/^\//, "")),
      );
      // Rendered at device pixels, so one sheet is sharp on a 2x display
      // and downscales cleanly on a 1x one.
      expect(width).toBe(size * SHEET_DPR);
      // A vertical strip of square frames. style.css scales it to the
      // mark's width and steps down by the mark's size, which is only
      // one frame if the frames are square and there are exactly this
      // many of them.
      expect(height).toBe(width * SHEET_FRAMES);
      // Grayscale *with alpha* (PNG colour type 4). A CSS mask reads the
      // alpha channel, so a sheet written without one -- plain
      // grayscale, colour type 0 -- masks nothing and every mark renders
      // as a filled square. Nothing else catches that.
      expect(colourType).toBe(4);
    }
  });

  it("loops in the time the animation is given", () => {
    expect(SHEET_LOOP_MS).toBe(Math.round((SHEET_FRAMES / SHEET_FPS) * 1000));
  });

  it("leaves any other size to the live renderer", () => {
    // GrainMark.jsx asks hasSheet before reaching for a file, so a size
    // nobody has recorded paints instead of 404ing.
    for (const size of [14, 24, 48, 320]) expect(hasSheet(size)).toBe(false);
  });
});
