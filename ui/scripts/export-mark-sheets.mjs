// node scripts/export-mark-sheets.mjs
//
// Renders the mark's animation to a sprite sheet per small size, so the
// app can play it as an image instead of running a particle system in
// every task row. `npm run brand` runs this alongside the static export.
//
// The frames are captured out of a real browser running src/brand/
// grain-mark.js itself, on a stubbed clock and a seeded Math.random.
// That is the whole point of doing it this way rather than
// re-implementing the flight in node: the sheet is not a rendering *of*
// the mark's animation, it is that animation, recorded. Nothing here can
// drift from the module because nothing here reimplements it -- the
// timeline below (dissolve, fly, settle, hold) is the only thing this
// shares with GrainMark.jsx, and it is a handful of constants.
//
// The output is an 8-bit **grayscale+alpha** PNG whose grey is a
// constant and whose alpha is the picture. Every pixel of the mark is
// one colour at a varying alpha, so the colour can come from CSS at play
// time; one sheet serves both themes and follows a theme change with no
// reload. The alpha channel is not optional padding: a CSS mask is an
// *alpha* mask by default, and a plain grayscale PNG has no alpha at
// all, so it masks nothing and the element shows as a filled square.
// See src/brand/mark-sheet.js.
//
// Requires a Playwright browser (`npx playwright install chromium`),
// which the e2e suite needs anyway; set CHROMIUM_PATH to use one that is
// already on the machine instead. The sheets are committed, so a
// checkout builds without ever running this.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

import { chromium } from "@playwright/test";

import {
  SHEET_DPR,
  SHEET_FPS,
  SHEET_FRAMES,
  SHEET_SIZES,
  sheetHref,
} from "../src/brand/mark-sheet.js";

const here = dirname(fileURLToPath(import.meta.url));
const frontend = join(here, "..");

// The module, inlined into the capture page with its exports stripped so
// it can be a plain inline module script. It is read rather than
// imported so the browser runs the same bytes the app ships.
const moduleSource = readFileSync(
  join(frontend, "src", "brand", "grain-mark.js"),
  "utf8",
)
  .replace(/^export function /gm, "function ")
  .replace(/^export const /gm, "const ");

// GrainMark.jsx's crisp timeline. The one thing duplicated between the
// two, and the reason the numbers are named rather than inline.
const SETTLE_AT = 0.7;
const SETTLE_MS = 280;
const CRISP_MS = 900;
const DISSOLVE_MS = 160;

const capturePage = `<script type="module">
${moduleSource}

// A stubbed clock and a seeded random, so a re-run writes the same bytes
// and a diff means the mark changed rather than that the capture drifted.
function seed(n){ let s = n; Math.random = () => { s = (s*1664525 + 1013904223) % 4294967296; return s/4294967296; }; }
let vnow = 0, pending = null;
performance.now = () => vnow;
window.requestAnimationFrame = (fn) => { pending = fn; return 1; };
window.cancelAnimationFrame = () => { pending = null; };
const tick = (dt) => { vnow += dt; if (pending) { const f = pending; pending = null; f(vnow); } };

const SNAP = DEFAULTS.snapSeconds * 1000;
const SETTLE_AT = ${SETTLE_AT}, SETTLE = ${SETTLE_MS}, CRISP = ${CRISP_MS}, DISSOLVE = ${DISSOLVE_MS};

window.capture = (cssPx, dpr, fps) => {
  const W = cssPx * dpr;
  const grains = document.createElement('canvas'); grains.width = grains.height = W;
  const solid = document.createElement('canvas'); solid.width = solid.height = W;
  const gx = grains.getContext('2d'), sx = solid.getContext('2d');
  const spec = grainSpec(cssPx);

  seed(42);
  const mark = createGrainMark(grains, {
    theme: 'dark', count: spec.count, radius: spec.radius, slot: STATIC_SLOT,
  });

  const step = 1000 / fps;
  const frames = [];
  let slot = STATIC_SLOT, solidA = 1;
  renderStatic(solid, { slot, style: 'solid', theme: 'dark' });

  // The two layers composited the way CSS composites them live: same
  // colour, so only the alphas matter.
  const shoot = () => {
    const g = gx.getImageData(0, 0, W, W).data, s = sx.getImageData(0, 0, W, W).data;
    const a = new Uint8Array(W * W);
    for (let i = 0; i < W * W; i++) a[i] = Math.round(g[i*4+3] * (1 - solidA) + s[i*4+3] * solidA);
    frames.push(a);
  };

  for (let k = 0; k < GLYPHS.length; k++) {
    // Dissolve out of the held glyph, into a flight that has just begun.
    slot = (slot + 1) % GLYPHS.length;
    mark.setMode(slot);
    const from = solidA;
    for (let t = 0; t < SNAP * SETTLE_AT - 1e-6; t += step) {
      solidA = from + (0 - from) * Math.min(1, t / DISSOLVE);
      shoot(); tick(step);
    }
    // Settle onto the glyph it is landing on, then hold it.
    renderStatic(solid, { slot, style: 'solid', theme: 'dark' });
    for (let t = 0; t < SETTLE + CRISP - 1e-6; t += step) {
      solidA = Math.min(1, t / SETTLE);
      shoot(); tick(step);
    }
  }
  return { W, n: frames.length, data: Array.from(frames).map((f) => Array.from(f)).flat() };
};
window.ready = true;
</script>`;

// ---------- 8-bit grayscale+alpha PNG ----------

const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();

function crc32(buf) {
  let c = ~0;
  for (let i = 0; i < buf.length; i++)
    c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return ~c >>> 0;
}

function chunk(type, data) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

/**
 * Grayscale+alpha PNG from a width*height alpha buffer, every scanline
 * filtered Up.
 *
 * The grey byte is a constant white and the alpha byte carries the
 * picture, because that is what a CSS mask reads. It costs a second
 * byte per pixel that is the same everywhere, which deflate returns
 * almost all of.
 *
 * Up is the filter that makes the rest cheap: 43% of the frames are
 * held ones, and stacked vertically a held frame is a block of
 * scanlines identical to the block above it, which Up turns into zeroes
 * and deflate turns into nothing.
 */
function maskPNG(width, height, alpha) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 4; // colour type: grayscale with alpha
  const stride = width * 2;
  const raw = Buffer.alloc(height * (1 + stride));
  const prev = Buffer.alloc(stride);
  const row = Buffer.alloc(stride);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      row[x * 2] = 0xff;
      row[x * 2 + 1] = alpha[y * width + x];
    }
    raw[y * (1 + stride)] = 2; // filter: Up
    for (let i = 0; i < stride; i++)
      raw[y * (1 + stride) + 1 + i] = (row[i] - prev[i]) & 0xff;
    row.copy(prev);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

// ---------- run ----------

// CHROMIUM_PATH is for an image that already carries a browser rather
// than downloading Playwright's own -- a CI runner, a sandbox.
const browser = await chromium
  .launch(
    process.env.CHROMIUM_PATH
      ? { executablePath: process.env.CHROMIUM_PATH }
      : {},
  )
  .catch((err) => {
    throw new Error(
      `could not launch a browser to record the sheets: ${err.message}\n` +
        "Run `npx playwright install chromium`, or set CHROMIUM_PATH to a browser " +
        "already on this machine.",
    );
  });
try {
  const page = await (await browser.newContext()).newPage();
  page.on("pageerror", (e) => {
    throw e;
  });
  await page.setContent(capturePage);
  await page.waitForFunction(() => window.ready);

  for (const cssPx of SHEET_SIZES) {
    const shot = await page.evaluate(
      ([px, dpr, fps]) => window.capture(px, dpr, fps),
      [cssPx, SHEET_DPR, SHEET_FPS],
    );
    if (shot.n !== SHEET_FRAMES) {
      throw new Error(
        `${cssPx}px captured ${shot.n} frames, but mark-sheet.js says ${SHEET_FRAMES}. ` +
          `Update SHEET_FRAMES -- the component sizes its animation off it.`,
      );
    }
    const out = join(frontend, "public", sheetHref(cssPx).replace(/^\//, ""));
    const png = maskPNG(shot.W, shot.W * shot.n, Buffer.from(shot.data));
    writeFileSync(out, png);
    console.log(
      `wrote ${out} (${shot.n} frames, ${shot.W}x${shot.W * shot.n}, ${(png.length / 1024).toFixed(1)} KB)`,
    );
  }
} finally {
  await browser.close();
}
