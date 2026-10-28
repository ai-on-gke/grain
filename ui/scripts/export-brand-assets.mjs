// node scripts/export-brand-assets.mjs
//
// Regenerates the checked-in brand assets from src/brand/grain-mark.js,
// which is the one definition of the mark. The static logo is the 2·3 (−)
// rosette (the pack's STATIC_SLOT), and the assets below are that one
// glyph in the two treatments the pack asks for -- solid where the mark
// is small, grains where it has room:
//
//   public/grain-mark-<theme>.svg   the rosette as a solid filled path.
//                                   Scale-free, so one file is sharp in
//                                   the 16px tab slot and the 180px
//                                   installed one alike. It is both the
//                                   favicon and the still GrainMark.jsx
//                                   shows whenever it is not animating.
//   public/grain-mark-<theme>.png   the same fill rasterized at 128px,
//                                   the favicon fallback for browsers
//                                   with no SVG-favicon support (Safari
//                                   before 16.4). Same geometry, so the
//                                   two cannot show different marks.
//   public/apple-touch-icon.png     the same fill again at 180px, on an
//                                   opaque ground: the iOS Home Screen
//                                   icon, which neither of the two above
//                                   can be. See renderTouchIconPNG.
//   docs/brand/grain-hero-2-3minus-<theme>.svg
//                                   the rosette at hero scale as grains,
//                                   for anywhere a large still is wanted
//                                   (README, slides, print).
//   src/brand/item-glyph-paths.js   the nav rail's figures (src/brand/
//                                   item-glyphs.js -- the four item
//                                   glyphs, repos, schedules, templates
//                                   and suites, and the three
//                                   destination glyphs, settings,
//                                   system and metrics) traced the same
//                                   way. A module rather than a file
//                                   under public/ because those icons
//                                   are drawn inline in `currentColor`,
//                                   so they take the colour of the nav
//                                   entry they sit in; an <img> could
//                                   not, and one theme's worth of files
//                                   would be the wrong colour in the
//                                   other. Generated, and pinned back
//                                   to the field by item-glyphs.test.js
//                                   the way the still is.
//
// All three are committed: `npm run build` copies public/ verbatim and
// the docs are read straight out of the repo, so neither has a build
// step that would regenerate them. Re-run this after changing the mark
// or the brand colours, and commit what it writes.
//
// Rendering here re-implements the module's draw calls rather than
// calling into them, because those paint through a canvas 2D context and
// node has none. The geometry comes from the module -- same frame, same
// field, same sampling -- so what this writes is what the browser paints.
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";
import { format } from "prettier";

import {
  GLYPHS,
  STATIC_SLOT,
  THEMES,
  fillScalar,
  glyphZoom,
  grainSpec,
  sampleGlyph,
} from "../src/brand/grain-mark.js";
import { GLYPH_SPECS, itemFillAt } from "../src/brand/item-glyphs.js";

const here = dirname(fileURLToPath(import.meta.url));
const frontend = join(here, "..");
// ui/ sits at the repo root, so the docs tree is one level up from here.
// (It was four for as long as the frontend lived deeper in the tree; a
// stale count wrote the hero SVGs outside the checkout, or failed on the
// mkdir when it landed somewhere unwritable.)
const repoRoot = join(frontend, "..");

// The static logo: 2·3 (−), pulled out to 0.78 of the frame so the
// rosette sits inside the circle rather than running off it.
const LOGO = GLYPHS[STATIC_SLOT];

// The box the vector is authored in. Arbitrary -- the SVG is scale-free
// and carries no pixel commitment -- but it fixes the coordinate
// precision below, and 128 is round enough to read in a diff.
const VECTOR_PX = 128;
// 128px, not 32: the favicon is scaled down to whatever slot asks for it
// (16px tab, 32px bookmark, larger for an installed shortcut), and one
// oversized source scales down cleanly where a 32px one cannot scale up.
const ICON_PX = 128;
// The item glyphs are authored in the same box as the still, and traced
// at the same resolution and tolerance -- so what item-glyphs.test.js
// can assert of them is what grain-mark.test.js asserts of the still:
// exact agreement with the field everywhere a straight segment is not
// cutting a corner.
const ITEM_PX = 128;
const ITEM_RES = 512;
const ITEM_TOL = ITEM_PX / 640;

// The iOS Home Screen icon, at the one size Apple asks for: 180px is
// the @3x tile every iPhone since the 6 Plus uses, and iOS scales it
// down for anything smaller rather than us shipping a file per device.
const TOUCH_ICON_PX = 180;
// How much of that tile the mark's circle spans. The favicons are drawn
// edge to edge (0.88 of the box, the frame radius doubled) because a tab
// slot is 16px and every pixel of it counts; a Home Screen tile is large
// and sits in a grid of icons that all leave a margin, so the mark is
// pulled in to match them. iOS rounds the tile with its own mask, and
// this is what keeps the rosette clear of the corners it takes off.
const TOUCH_ICON_FILL = 0.72;
// The ground that tile is filled with, edge to edge: darkTheme's
// background.default from src/theme.js, so the icon is the app's own
// dark ground rather than a colour invented here. The dark treatment is
// the one that works on a Home Screen -- an iOS wallpaper is more often
// dark than light, and unlike the favicon this file cannot follow the
// theme, since iOS reads it once when the shortcut is added. Kept in
// step with the palette by grain-mark.test.js, which reads the colour
// out of theme.js and finds it in the committed corner pixels.
const TOUCH_ICON_GROUND = [0x0f, 0x10, 0x13];

// The hero export's size, and the pack's own: `node export-svg.mjs` in
// the pack writes svg/grain-logo-{light,dark}.svg at exactly this size
// and seed, and this file reproduces those two byte for byte. That is
// what pins the vendoring -- if the module drifts from the pack, the
// hero assets stop matching and `npm run brand` shows it.
const HERO_PX = 440;

// sampleGlyph darts at Math.random, and the pack seeds it this way for
// its own exports; seeding it identically makes these reproducible, so a
// re-run leaves the committed files alone unless the mark itself changed.
function seedRandom(seed) {
  let s = seed;
  Math.random = () => {
    s = (s * 1664525 + 1013904223) % 4294967296;
    return s / 4294967296;
  };
}

// ---------- the solid fill, as geometry ----------

/**
 * The scalar the solid mark fills the positive region of, in pixels of a
 * W-sized box, forced negative outside the frame.
 *
 * Clamping to just outside the frame radius is what lets the contour
 * tracer below assume every contour closes inside the box: the glyph's
 * filled regions run past the circle and would otherwise have to be
 * closed along the box edge. The clip path in the SVG (and the frame
 * test in the PNG) then trims the 2% collar this leaves behind, so
 * nothing of it survives into the asset.
 */
function fillAt(W, x, y) {
  const c = W / 2;
  const R = W * 0.44;
  const [n, m, sg] = LOGO;
  const Rf = R * glyphZoom(n, m, sg);
  if (Math.hypot(x - c, y - c) > R * 1.02) return -1;
  return fillScalar(n, m, sg, (x - c) / Rf, (y - c) / Rf);
}

/**
 * Marching squares over `scalar`, as closed loops in the W box. The
 * still passes `fillAt`; the item glyphs pass their own field, which is
 * met with the frame rather than clamped just outside it.
 *
 * The standard 16-case table, with the two saddles (5 and 10) resolved
 * by the cell's mean value so a saddle is cut the way the field actually
 * runs through it rather than always the same way -- which on this glyph
 * is the difference between the rosette's arms meeting at the centre and
 * pinching off into separate blobs. Segments are collected first and
 * then chained end to end; the grid is fine enough that endpoints shared
 * between neighbouring cells are bit-identical, so the chaining is an
 * exact key lookup rather than a nearest-point search.
 */
function contours(W, res, scalar) {
  const step = W / res;
  const v = new Float64Array((res + 1) * (res + 1));
  for (let j = 0; j <= res; j++) {
    for (let i = 0; i <= res; i++)
      v[j * (res + 1) + i] = scalar(i * step, j * step);
  }
  const at = (i, j) => v[j * (res + 1) + i];
  // Zero crossing along a cell edge, linearly interpolated.
  const cut = (a, b, va, vb) => {
    const t = va / (va - vb);
    return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t];
  };

  const segs = [];
  for (let j = 0; j < res; j++) {
    for (let i = 0; i < res; i++) {
      const v00 = at(i, j);
      const v10 = at(i + 1, j);
      const v11 = at(i + 1, j + 1);
      const v01 = at(i, j + 1);
      const code =
        (v00 > 0 ? 1 : 0) |
        (v10 > 0 ? 2 : 0) |
        (v11 > 0 ? 4 : 0) |
        (v01 > 0 ? 8 : 0);
      if (code === 0 || code === 15) continue;
      const p00 = [i * step, j * step];
      const p10 = [(i + 1) * step, j * step];
      const p11 = [(i + 1) * step, (j + 1) * step];
      const p01 = [i * step, (j + 1) * step];
      const top = () => cut(p00, p10, v00, v10);
      const right = () => cut(p10, p11, v10, v11);
      const bottom = () => cut(p01, p11, v01, v11);
      const left = () => cut(p00, p01, v00, v01);
      // Which edges the contour crosses. Orientation is not tracked:
      // the loops are chained from either end below and filled with
      // evenodd, neither of which depends on a winding direction.
      //
      // Hand-packed as a table, one row per configuration, and kept
      // that way: prettier would put each label and each statement on a
      // line of its own, turning six lines that read as a lookup into
      // eighteen that do not.
      // prettier-ignore
      switch (code) {
        case 1: case 14: segs.push([left(), top()]); break;
        case 2: case 13: segs.push([top(), right()]); break;
        case 3: case 12: segs.push([left(), right()]); break;
        case 4: case 11: segs.push([right(), bottom()]); break;
        case 6: case 9: segs.push([top(), bottom()]); break;
        case 7: case 8: segs.push([bottom(), left()]); break;
        case 5:
        case 10: {
          const mid = (v00 + v10 + v11 + v01) / 4;
          const joined = code === 5 ? mid > 0 : mid <= 0;
          if (joined) {
            segs.push([left(), top()], [right(), bottom()]);
          } else {
            segs.push([left(), bottom()], [right(), top()]);
          }
          break;
        }
      }
    }
  }

  // Chain the segments into loops. A cut point is computed from the same
  // two corner values in both cells that share the edge, so the doubled
  // endpoints are bit-identical and neighbours can be found by exact key
  // rather than by proximity. Each segment is walked from whichever end
  // the chain arrives at, which is what lets the case table above stay
  // orientation-free.
  const key = (p) => `${p[0]},${p[1]}`;
  const touching = new Map();
  segs.forEach((s, i) => {
    for (const end of s) {
      const k = key(end);
      if (!touching.has(k)) touching.set(k, []);
      touching.get(k).push(i);
    }
  });
  const loops = [];
  const used = new Uint8Array(segs.length);
  for (let start = 0; start < segs.length; start++) {
    if (used[start]) continue;
    used[start] = 1;
    const loop = [segs[start][0], segs[start][1]];
    for (;;) {
      const head = loop[loop.length - 1];
      const next = (touching.get(key(head)) || []).find((i) => !used[i]);
      if (next === undefined) break;
      used[next] = 1;
      const [a, b] = segs[next];
      loop.push(key(a) === key(head) ? b : a);
    }
    if (loop.length > 3) loops.push(loop);
  }
  return loops;
}

/** Douglas-Peucker: drop the points a straight run does not need. */
function simplify(points, tol) {
  if (points.length < 3) return points;
  const keep = new Uint8Array(points.length);
  keep[0] = 1;
  keep[points.length - 1] = 1;
  const stack = [[0, points.length - 1]];
  while (stack.length) {
    const [a, b] = stack.pop();
    const [ax, ay] = points[a];
    const [bx, by] = points[b];
    const dx = bx - ax;
    const dy = by - ay;
    const l2 = dx * dx + dy * dy;
    let far = -1;
    let fd = tol;
    for (let i = a + 1; i < b; i++) {
      const [px, py] = points[i];
      const t =
        l2 > 1e-12
          ? Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / l2))
          : 0;
      const d = Math.hypot(px - (ax + dx * t), py - (ay + dy * t));
      if (d > fd) {
        fd = d;
        far = i;
      }
    }
    if (far > 0) {
      keep[far] = 1;
      stack.push([a, far], [far, b]);
    }
  }
  return points.filter((_, i) => keep[i]);
}

/**
 * The solid mark as one SVG path.
 *
 * Traced at 512 and then simplified to a fifth of a pixel of the
 * authoring box: fine enough that the curve is smooth at any size the
 * favicon is asked for, coarse enough that the file stays a few KB
 * rather than the sixty a raw trace would be. `evenodd` is what makes
 * the rosette's holes holes without depending on the winding coming out
 * of the tracer.
 */
function logoPath() {
  const W = VECTOR_PX;
  return tracePath(W, 512, W / 640, (x, y) => fillAt(W, x, y));
}

/** One glyph as one `d` string: traced, simplified, closed loops. */
function tracePath(W, res, tol, scalar) {
  return contours(W, res, scalar)
    .map((loop) => simplify(loop, tol))
    .filter((loop) => loop.length > 3)
    .map(
      (loop) =>
        `M ${loop.map(([x, y]) => `${x.toFixed(2)} ${y.toFixed(2)}`).join(" L ")} Z`,
    )
    .join(" ");
}

function renderLogoSVG(theme, d) {
  const W = VECTOR_PX;
  const R = W * 0.44;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${W}">
  <defs><clipPath id="f"><circle cx="${W / 2}" cy="${W / 2}" r="${R}"/></clipPath></defs>
  <path clip-path="url(#f)" fill="${theme.grain}" fill-rule="evenodd" d="${d}"/>
</svg>
`;
}

// ---------- hero SVG (grains -- the pack's own export, reproduced) ----------

function renderHeroSVG(theme) {
  const W = HERO_PX;
  const frame = { W, H: W, cx: W / 2, cy: W / 2, R: W * 0.44 };
  const spec = grainSpec(W);
  seedRandom(42);
  const pts = sampleGlyph(frame, LOGO, spec.count);
  const r = (W * spec.radius).toFixed(2);
  const circles = pts
    .map(
      (p) => `<circle cx="${p.x.toFixed(2)}" cy="${p.y.toFixed(2)}" r="${r}"/>`,
    )
    .join("\n    ");
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${W}">
  <defs><clipPath id="f"><circle cx="${W / 2}" cy="${W / 2}" r="${W * 0.44}"/></clipPath></defs>
  <g clip-path="url(#f)" fill="${theme.grain}">
    ${circles}
  </g>
</svg>
`;
}

// ---------- the item glyphs, as a module ----------

/**
 * src/brand/item-glyph-paths.js: every nav figure as a `d` string.
 *
 * A module and not one SVG file each because ItemGlyph.jsx draws them
 * inline in `currentColor` -- a nav icon has to take the colour of the
 * entry it sits in, which an <img> cannot do and which would otherwise
 * cost a file per theme per glyph. Inline also means no request per
 * icon on a page that renders several.
 */
function renderItemGlyphModule() {
  const entries = Object.entries(GLYPH_SPECS)
    .map(([kind, spec]) => {
      const d = tracePath(ITEM_PX, ITEM_RES, ITEM_TOL, (x, y) =>
        itemFillAt(spec, ITEM_PX, x, y),
      );
      const [n, m, sg] = spec.glyph;
      // One line per entry, wrapped afterwards by prettier itself
      // (below): where a `d` string goes on the key's line and where it
      // goes on its own is prettier's rule and depends on how long the
      // key is, so guessing it here is how this file ends up failing
      // the repo's own format check the moment a glyph with a short
      // name is added.
      return `  // ${n}·${m} (${sg > 0 ? "+" : "−"}) at ${spec.zoom} -- ${spec.figure}\n  ${kind}: "${d}",`;
    })
    .join("\n");
  return `// Generated by scripts/export-brand-assets.mjs (npm run brand:assets).
// Do not edit: the figures live in item-glyphs.js, and re-running the
// export is what brings this file back in line with them.
//
// Each glyph is one closed path, already met with the circular frame,
// so it needs no clip -- see item-glyphs.js's itemFillAt. Filled
// even-odd, which is what makes the ring's hole a hole.

/** The box the paths are authored in; they carry no other pixel size. */
export const ITEM_GLYPH_BOX = ${ITEM_PX};

export const ITEM_GLYPH_PATHS = {
${entries}
};
`;
}

// ---------- PNG (no dependencies: node's own zlib plus a CRC) ----------

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

/** 8-bit RGBA PNG from a width*height*4 byte buffer. */
function encodePNG(width, height, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // colour type: truecolour with alpha
  // Each scanline is prefixed with its filter type; 0 (none) leaves the
  // bytes as they are and costs nothing here -- the image is one colour
  // at varying alpha, which deflate already collapses.
  const raw = Buffer.alloc(height * (1 + width * 4));
  for (let y = 0; y < height; y++) {
    raw[y * (1 + width * 4)] = 0;
    rgba.copy(raw, y * (1 + width * 4) + 1, y * width * 4, (y + 1) * width * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

/**
 * The solid mark as pixels: the module's own `style:'solid'` render,
 * which is the filled region supersampled 3x3 and clipped to the frame.
 */
function renderLogoPNG(size, theme) {
  const c = size / 2;
  const R = size * 0.44;
  const SS = 3;
  const rgb = theme.grainRGB;
  const out = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let cover = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const px = x + (sx + 0.5) / SS;
          const py = y + (sy + 0.5) / SS;
          if (Math.hypot(px - c, py - c) > R) continue;
          if (fillAt(size, px, py) > 0) cover++;
        }
      }
      const i = (y * size + x) * 4;
      out[i] = rgb[0];
      out[i + 1] = rgb[1];
      out[i + 2] = rgb[2];
      out[i + 3] = Math.round((cover / (SS * SS)) * 255);
    }
  }
  return encodePNG(size, size, out);
}

/**
 * The Home Screen icon: the same solid mark, composited onto an opaque
 * ground and inset from the edges.
 *
 * A separate file from the favicons rather than a reuse of one, because
 * iOS asks for something neither of them is. It never reads
 * `<link rel="icon">` for a Home Screen shortcut -- only
 * `<link rel="apple-touch-icon">`, falling back to /apple-touch-icon.png
 * at the site root, and to a screenshot of the page if neither resolves.
 * It wants a raster, so the SVG is out whatever it is declared as. And
 * it discards the alpha channel, so the favicon PNG's transparent
 * surround would come out as a black tile with the mark burnt into it --
 * hence the ground here, filled to all four edges. The corners are left
 * square for the same reason: iOS applies its own rounded mask, and a
 * file that arrived pre-rounded would be rounded twice.
 */
function renderTouchIconPNG(size, theme, ground) {
  // The mark's own frame, inset inside the tile: fillAt puts the frame
  // radius at 0.44 of the box it is given, so a box this wide lands the
  // mark's diameter on TOUCH_ICON_FILL of the tile.
  const markW = (size * TOUCH_ICON_FILL) / 0.88;
  const inset = (size - markW) / 2;
  const c = markW / 2;
  const R = markW * 0.44;
  const SS = 3;
  const rgb = theme.grainRGB;
  const out = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let cover = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const px = x + (sx + 0.5) / SS - inset;
          const py = y + (sy + 0.5) / SS - inset;
          if (Math.hypot(px - c, py - c) > R) continue;
          if (fillAt(markW, px, py) > 0) cover++;
        }
      }
      // The coverage the favicon PNG would have written as alpha, mixed
      // into the ground instead -- so the edges are as smooth as that
      // one's, on a channel iOS will not throw away.
      const a = cover / (SS * SS);
      const i = (y * size + x) * 4;
      for (let ch = 0; ch < 3; ch++)
        out[i + ch] = Math.round(ground[ch] + (rgb[ch] - ground[ch]) * a);
      out[i + 3] = 255;
    }
  }
  return encodePNG(size, size, out);
}

// ---------- write ----------

mkdirSync(join(frontend, "public"), { recursive: true });
mkdirSync(join(repoRoot, "docs", "brand"), { recursive: true });

// One trace, both themes: the two files differ only in the fill colour,
// and tracing once is what guarantees it.
const d = logoPath();

for (const [name, theme] of Object.entries(THEMES)) {
  const svg = join(frontend, "public", `grain-mark-${name}.svg`);
  writeFileSync(svg, renderLogoSVG(theme, d));
  console.log("wrote", svg);

  const png = join(frontend, "public", `grain-mark-${name}.png`);
  writeFileSync(png, renderLogoPNG(ICON_PX, theme));
  console.log("wrote", png);

  const hero = join(
    repoRoot,
    "docs",
    "brand",
    `grain-hero-2-3minus-${name}.svg`,
  );
  writeFileSync(hero, renderHeroSVG(theme));
  console.log("wrote", hero);
}

// One file, not one per theme: a Home Screen icon is baked into the
// shortcut when it is added and cannot follow the app's theme after.
const touch = join(frontend, "public", "apple-touch-icon.png");
writeFileSync(
  touch,
  renderTouchIconPNG(TOUCH_ICON_PX, THEMES.dark, TOUCH_ICON_GROUND),
);
console.log("wrote", touch);

const items = join(frontend, "src", "brand", "item-glyph-paths.js");
// Through prettier on the way out: this file is committed and `make
// fmt-frontend` checks it like any other, and prettier's own idea of
// where a long string wraps is not one worth reimplementing here.
writeFileSync(
  items,
  await format(renderItemGlyphModule(), { filepath: items }),
);
console.log("wrote", items);
