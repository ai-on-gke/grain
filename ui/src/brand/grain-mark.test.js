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

import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { inflateSync } from "node:zlib";

import { describe, expect, it } from "vitest";

import {
  GLYPHS,
  STATIC_SLOT,
  THEMES,
  fillScalar,
  glyphZoom,
  grainSpec,
  sampleGlyph,
} from "./grain-mark.js";
import { darkTheme } from "../theme.js";

// The geometry half of the mark is pure maths -- no canvas, no React --
// so it can be pinned down directly. The module itself is a verbatim
// copy of the design pack (see its header), so what is worth testing is
// not the pack's arithmetic but the things grain leans on: that the
// static logo is the glyph the rest of the app thinks it is, that the
// committed favicon is still that same glyph, and that the grain spec
// holds at the sizes the app actually renders at.
// See docs/brand.md for the design.

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = join(here, "..", "..", "public");

const LOGO = GLYPHS[STATIC_SLOT];
const frameOf = (W) => ({ W, H: W, cx: W / 2, cy: W / 2, R: W * 0.44 });

/** Loops of the committed still, parsed out of its one SVG path. */
function stillLoops() {
  const svg = readFileSync(join(publicDir, "grain-mark-light.svg"), "utf8");
  return svg
    .match(/ d="([^"]+)"/)[1]
    .split("M ")
    .slice(1)
    .map((loop) =>
      loop
        .replace(/\s*Z\s*$/, "")
        .split(" L ")
        .map((p) => p.trim().split(/\s+/).map(Number)),
    );
}

/**
 * A committed PNG as pixels: `at(x, y)` in RGBA.
 *
 * Only what export-brand-assets.mjs's own encoder writes has to be read
 * back here -- 8-bit truecolour with alpha, one deflate stream, every
 * scanline carrying filter 0 -- which is why this is fifteen lines and
 * not a decoder dependency. Each of those three is asserted rather than
 * assumed, so a change to the encoder fails here instead of quietly
 * handing the tests below garbage.
 */
function decodePNG(name) {
  const file = readFileSync(join(publicDir, name));
  let width = 0;
  let height = 0;
  const idat = [];
  for (let p = 8; p < file.length;) {
    const len = file.readUInt32BE(p);
    const type = file.toString("ascii", p + 4, p + 8);
    const data = file.subarray(p + 8, p + 8 + len);
    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      expect([data[8], data[9]]).toEqual([8, 6]);
    }
    if (type === "IDAT") idat.push(data);
    p += 12 + len;
  }
  const raw = inflateSync(Buffer.concat(idat));
  const stride = 1 + width * 4;
  for (let y = 0; y < height; y++) expect(raw[y * stride]).toBe(0);
  return {
    width,
    height,
    at(x, y) {
      const o = y * stride + 1 + x * 4;
      return [raw[o], raw[o + 1], raw[o + 2], raw[o + 3]];
    },
  };
}

/** Even-odd crossing count, the fill rule the still is drawn with. */
function insideLoops(loops, x, y) {
  let inside = false;
  for (const loop of loops) {
    for (let i = 0, j = loop.length - 1; i < loop.length; j = i++) {
      const [xi, yi] = loop[i];
      const [xj, yj] = loop[j];
      if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi)
        inside = !inside;
    }
  }
  return inside;
}

describe("the static logo", () => {
  it("is the 2·3 (−) rosette, pulled out to sit inside the frame", () => {
    // GrainMark.jsx opens the animation on STATIC_SLOT so the mark comes
    // to life on the figure the still was showing; the export script
    // draws the still from the same slot. Both would silently disagree
    // with the pack if this moved.
    expect(LOGO).toEqual([2, 3, -1]);
    expect(glyphZoom(...LOGO)).toBe(0.78);
  });

  it("keeps every grain inside the frame and inside the filled region", () => {
    const f = frameOf(128);
    const Rf = f.R * glyphZoom(...LOGO);
    for (const p of sampleGlyph(f, LOGO, grainSpec(f.W).count)) {
      expect(Math.hypot(p.x - f.cx, p.y - f.cy)).toBeLessThanOrEqual(f.R);
      expect(
        fillScalar(...LOGO, (p.x - f.cx) / Rf, (p.y - f.cy) / Rf),
      ).toBeGreaterThan(0);
    }
  });

  it("is the shape the committed still draws", () => {
    // The still under public/ is the favicon and every non-animating
    // mark in the app, and it is generated rather than drawn -- so the
    // failure it exists to catch is a mark that changed without
    // `npm run brand` being re-run, which would leave the tab and the
    // sidebar showing the previous logo.
    //
    // The path is traced from the field, so away from the boundary the
    // two have to agree exactly; the only pixels where they can differ
    // are the ones the trace's straight segments cut across, and those
    // are exactly the pixels with a sign change in reach. Skipping those
    // is what lets this assert equality rather than a percentage.
    const loops = stillLoops();
    const W = 128;
    const f = frameOf(W);
    const Rf = f.R * glyphZoom(...LOGO);
    const field = (x, y) =>
      fillScalar(...LOGO, (x - f.cx) / Rf, (y - f.cy) / Rf) > 0;
    let tested = 0;
    const disagreed = [];
    for (let y = 0; y < W; y++) {
      for (let x = 0; x < W; x++) {
        const px = x + 0.5;
        const py = y + 0.5;
        if (Math.hypot(px - f.cx, py - f.cy) > f.R - 1) continue;
        const filled = field(px, py);
        const onEdge = [
          [0.75, 0],
          [-0.75, 0],
          [0, 0.75],
          [0, -0.75],
        ].some(([dx, dy]) => field(px + dx, py + dy) !== filled);
        if (onEdge) continue;
        tested++;
        if (filled !== insideLoops(loops, px, py)) disagreed.push([px, py]);
      }
    }
    expect(tested).toBeGreaterThan(7000);
    expect(disagreed).toEqual([]);
  });

  it("ships a still per theme, in that theme's grain colour", () => {
    for (const [name, theme] of Object.entries(THEMES)) {
      expect(
        readFileSync(join(publicDir, `grain-mark-${name}.svg`), "utf8"),
      ).toContain(`fill="${theme.grain}"`);
    }
  });
});

describe("the Home Screen icon", () => {
  // public/apple-touch-icon.png is the one asset a browser never asks
  // for: iOS reads it when a page is added to the Home Screen, from
  // index.html's <link rel="apple-touch-icon"> or, failing that, from
  // the root path it is committed under. It throws the alpha channel
  // away and applies its own rounded mask, so what the favicons are --
  // a mark floating on transparency, edge to edge -- would come out as a
  // black tile with the corners cut off the mark. These are the
  // properties that stop that, and they are worth pinning here because
  // nothing renders this file in the test suite or in CI: the first
  // place a mistake in it shows is somebody's phone.
  const icon = decodePNG("apple-touch-icon.png");
  const hexRGB = (hex) =>
    [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  // The ground the export fills the tile with, read back off the palette
  // it was taken from rather than repeated: change the app's dark ground
  // and this fails until `npm run brand` has been re-run.
  const ground = hexRGB(darkTheme.palette.background.default);
  const grain = THEMES.dark.grainRGB;
  // TOUCH_ICON_FILL in scripts/export-brand-assets.mjs: the mark's
  // circle spans this much of the tile, and the rest is margin.
  const FILL = 0.72;

  /** How far a pixel is from the ground towards the grain, 0..1. */
  const mix = (px) => (px[0] - ground[0]) / (grain[0] - ground[0]);

  it("is a 180px tile, opaque everywhere", () => {
    // 180 is the @3x size iOS asks for and scales down from; opacity is
    // the part that matters, since a partly transparent pixel here is a
    // black one on the Home Screen rather than a soft one.
    expect([icon.width, icon.height]).toEqual([180, 180]);
    const translucent = [];
    for (let y = 0; y < icon.height; y++)
      for (let x = 0; x < icon.width; x++)
        if (icon.at(x, y)[3] !== 255) translucent.push([x, y]);
    expect(translucent).toEqual([]);
  });

  it("carries the app's dark ground to all four edges", () => {
    // Full-bleed and square-cornered: iOS rounds the tile itself, and a
    // file that arrived rounded (or with a transparent surround) would
    // be masked twice.
    const edge = [];
    for (let i = 0; i < 180; i++) edge.push([i, 0], [i, 179], [0, i], [179, i]);
    for (const [x, y] of edge)
      expect(icon.at(x, y).slice(0, 3)).toEqual(ground);
  });

  it("is that ground and the dark grain colour, and nothing else", () => {
    // Every pixel is one of the two brand colours or an antialiased
    // blend of them -- no third colour, no gradient, no stray fringe
    // from a resample.
    const offColour = [];
    for (let y = 0; y < icon.height; y++) {
      for (let x = 0; x < icon.width; x++) {
        const px = icon.at(x, y);
        const t = mix(px);
        // A rounding of the same blend in both other channels: the red
        // channel fixes t, and the green and blue ones have to follow it
        // to within the rounding that wrote them.
        const off = [1, 2].some(
          (ch) =>
            Math.abs(px[ch] - (ground[ch] + (grain[ch] - ground[ch]) * t)) > 1,
        );
        if (t < 0 || t > 1 || off) offColour.push([x, y, px]);
      }
    }
    expect(offColour).toEqual([]);
  });

  it("draws the same rosette the still does, inset from the edges", () => {
    // The same assertion the committed still gets, against the same
    // field: what it catches is the mark changing without `npm run
    // brand` being re-run, which would leave an installed shortcut on
    // the previous logo -- and iOS caches that shortcut's icon, so it
    // would outlive the deploy that fixed it.
    //
    // The mark's own frame is the tile pulled in to FILL of itself, so
    // pixels are put back into that frame's coordinates before being
    // asked of the field. Pixels the boundary runs through are skipped,
    // exactly as in the still's test: those are the ones the exporter's
    // supersampling deliberately lands between the two colours.
    const markW = (icon.width * FILL) / 0.88;
    const inset = (icon.width - markW) / 2;
    const f = frameOf(markW);
    const Rf = f.R * glyphZoom(...LOGO);
    const field = (x, y) =>
      fillScalar(...LOGO, (x - f.cx) / Rf, (y - f.cy) / Rf) > 0;
    let tested = 0;
    const disagreed = [];
    for (let y = 0; y < icon.height; y++) {
      for (let x = 0; x < icon.width; x++) {
        const px = x + 0.5 - inset;
        const py = y + 0.5 - inset;
        const t = mix(icon.at(x, y));
        if (Math.hypot(px - f.cx, py - f.cy) > f.R - 1) {
          // Outside the mark's frame the tile is bare ground: this is
          // what says the mark is inset rather than running to the edge.
          if (Math.hypot(px - f.cx, py - f.cy) > f.R + 1 && t !== 0)
            disagreed.push([x, y, "not bare ground outside the frame"]);
          continue;
        }
        const filled = field(px, py);
        const onEdge = [
          [0.75, 0],
          [-0.75, 0],
          [0, 0.75],
          [0, -0.75],
        ].some(([dx, dy]) => field(px + dx, py + dy) !== filled);
        if (onEdge) continue;
        tested++;
        if (filled ? t < 0.99 : t > 0.01) disagreed.push([x, y, t]);
      }
    }
    expect(tested).toBeGreaterThan(10000);
    expect(disagreed).toEqual([]);
  });
});

describe("grain spec", () => {
  it("draws the same picture at 1x and 2x", () => {
    // GrainMark.jsx reads the spec at the CSS size and lets the radius
    // -- a fraction of canvas.width -- scale itself with the backing
    // store. Reading it at the backing size instead would take four
    // times the grains at half the size on a 2x display: a different
    // stipple, not a sharper one. These are the numbers that deviation
    // is worth carrying for.
    const cssPx = 20;
    const spec = grainSpec(cssPx);
    for (const dpr of [1, 2, 3]) {
      const backing = cssPx * dpr;
      expect(spec.count).toBe(grainSpec(cssPx).count);
      // Grain radius on screen, in CSS pixels: canvas.width * radius / dpr.
      expect((backing * spec.radius) / dpr).toBeCloseTo(
        cssPx * spec.radius,
        10,
      );
    }
    expect(grainSpec(cssPx * 2).count).not.toBe(spec.count);
  });

  it("scales the count with area across the app's sizes", () => {
    // The sidebar mark, the task-row badge and the loading screen, in
    // the sizes their components ask for. These are the pack's own
    // numbers: grain stippled the small marks more thickly for a while,
    // to make a settled one read as a shape, and crisping the dwell
    // (GrainMark.jsx) made that both unnecessary and harmful -- the only
    // place those grains are still seen is the flight, and a 20px mark
    // three times this dense is a blob in the air rather than sand.
    expect(grainSpec(20).count).toBe(117);
    expect(grainSpec(32).count).toBe(183);
    expect(grainSpec(320).count).toBe(3200);
  });
});

describe("the glyph cycle", () => {
  it("is four distinct eigenmodes, the logo among them", () => {
    expect(GLYPHS).toHaveLength(4);
    expect(new Set(GLYPHS.map(String)).size).toBe(4);
    expect(GLYPHS[STATIC_SLOT]).toBe(LOGO);
    for (const [n, m] of GLYPHS) {
      // Only integer mode numbers are real resonances -- interpolating
      // one to get between two figures piles up spurious features in the field.
      expect(Number.isInteger(n) && Number.isInteger(m)).toBe(true);
    }
  });

  it("fills each glyph's region without spilling out of the frame", () => {
    const f = frameOf(64);
    for (const glyph of GLYPHS) {
      const pts = sampleGlyph(f, glyph, grainSpec(f.W).count);
      expect(pts.length).toBe(grainSpec(f.W).count);
      for (const p of pts) {
        expect(Math.hypot(p.x - f.cx, p.y - f.cy)).toBeLessThanOrEqual(f.R);
      }
    }
  });
});
