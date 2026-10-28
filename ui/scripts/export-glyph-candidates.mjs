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

// node scripts/export-glyph-candidates.mjs
//
// Renders the Chladni candidate contact sheets for the `alerts` destination
// glyph and the `ci` item glyph using the real `fillScalar` / `itemFillAt`
// pipeline from src/brand/grain-mark.js and src/brand/item-glyphs.js:
//
//   docs/brand/alerts-candidates.svg (+ .png when Chromium is available)
//   docs/brand/ci-candidates.svg     (+ .png when Chromium is available)
//
// Every candidate on both sheets is checked against the same uniqueness and
// framing rules src/brand/item-glyphs.test.js enforces:
//   - integer mode numbers [n, m, sign] with sign in {-1, +1};
//   - unused by the brand mark's own four-glyph cycle (GLYPHS);
//   - unused by every other item or destination glyph in GLYPH_SPECS
//     (counting n·m and m·n as the same mode at s = +1);
//   - at least 20% pixel difference inside the circular frame against every
//     other glyph in the set, so swapping any candidate into item-glyphs.js
//     is a one-line change that passes the test suite.

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { GLYPHS, THEMES, fillScalar } from "../src/brand/grain-mark.js";
import { GLYPH_SPECS, itemFillAt } from "../src/brand/item-glyphs.js";

const here = dirname(fileURLToPath(import.meta.url));
const frontend = join(here, "..");
const repoRoot = join(frontend, "..");
const outDir = join(repoRoot, "docs", "brand");

const ITEM_PX = 128;
const ITEM_RES = 512;
const ITEM_TOL = ITEM_PX / 640;
const SIZES = [16, 20, 32];

export const ALERTS_CANDIDATES = [
  {
    glyph: [2, 5, -1],
    zoom: 1.4,
    figure: "bell dome over a flared lip and clapper",
    chosen: true,
  },
  {
    glyph: [1, 3, -1],
    zoom: 1.6,
    figure: "bell cones between ringing side arcs",
  },
  {
    glyph: [1, 4, 1],
    zoom: 1.8,
    figure: "arched bell housing with flared rim",
  },
  {
    glyph: [6, 1, -1],
    zoom: 1.5,
    figure: "crowned bell skirt over twin clappers",
  },
  {
    glyph: [1, 6, -1],
    zoom: 1.5,
    figure: "domed cap over a wide chime bar",
  },
  {
    glyph: [1, 2, -1],
    zoom: 1.4,
    figure: "twin resonant cones meeting at a point",
  },
  {
    glyph: [4, 5, -1],
    zoom: 1.4,
    figure: "flared bell skirt and clapper",
  },
  {
    glyph: [3, 7, 1],
    zoom: 1.6,
    figure: "finial dome over a ringed chamber",
  },
];

export const CI_CANDIDATES = [
  {
    glyph: [3, 2, -1],
    zoom: 1.4,
    figure: "winged gate arms closing on a channel",
    chosen: true,
  },
  {
    glyph: [4, 2, -1],
    zoom: 1.5,
    figure: "paired gate walls and inner check lobes",
  },
  {
    glyph: [4, 3, -1],
    zoom: 1.6,
    figure: "four gate lobes framing a central verdict pass",
  },
  {
    glyph: [5, 2, -1],
    zoom: 1.5,
    figure: "staged pipeline channel between side gates",
  },
  {
    glyph: [5, 3, -1],
    zoom: 1.6,
    figure: "three-stage pipeline down a central channel",
  },
  {
    glyph: [6, 2, -1],
    zoom: 1.5,
    figure: "collared gate channel with central check",
  },
  {
    glyph: [2, 7, 1],
    zoom: 1.5,
    figure: "parallel pipeline rails with a central bridge",
  },
  {
    glyph: [1, 7, 1],
    zoom: 1.4,
    figure: "barred gate across parallel tracks",
  },
];

// ---------- marching squares + simplification (matches export-brand-assets.mjs) ----------

function contours(W, res, scalar) {
  const step = W / res;
  const v = new Float64Array((res + 1) * (res + 1));
  for (let j = 0; j <= res; j++) {
    for (let i = 0; i <= res; i++) {
      v[j * (res + 1) + i] = scalar(i * step, j * step);
    }
  }
  const at = (i, j) => v[j * (res + 1) + i];
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

export function traceCandidatePath(spec) {
  return contours(ITEM_PX, ITEM_RES, (x, y) => {
    // Evaluate through fillScalar via itemFillAt so the contact sheet uses
    // the exact same scalar and circular frame intersection as ItemGlyph.
    const c = ITEM_PX / 2;
    const R = ITEM_PX * 0.44;
    const [n, m, sg] = spec.glyph;
    const Rf = R * spec.zoom;
    const frame = (R - Math.hypot(x - c, y - c)) / R;
    return Math.min(fillScalar(n, m, sg, (x - c) / Rf, (y - c) / Rf), frame);
  })
    .map((loop) => simplify(loop, ITEM_TOL))
    .filter((loop) => loop.length > 3)
    .map(
      (loop) =>
        `M ${loop.map(([x, y]) => `${x.toFixed(2)} ${y.toFixed(2)}`).join(" L ")} Z`,
    )
    .join(" ");
}

// ---------- rule verification ----------

function fieldMask(spec, res = 48) {
  const step = ITEM_PX / res;
  const mask = [];
  for (let j = 0; j < res; j++) {
    const row = [];
    for (let i = 0; i < res; i++) {
      row.push(
        itemFillAt(spec, ITEM_PX, (i + 0.5) * step, (j + 0.5) * step) > 0,
      );
    }
    mask.push(row);
  }
  return mask;
}

function assertLegalCandidates(targetKind, candidates) {
  const otherSpecs = Object.entries(GLYPH_SPECS).filter(
    ([kind]) => kind !== targetKind,
  );
  const forbiddenExact = new Set([
    ...GLYPHS.map(String),
    ...otherSpecs.map(([, s]) => String(s.glyph)),
  ]);
  const forbiddenPlus = new Set(
    [...GLYPHS, ...otherSpecs.map(([, s]) => s.glyph)]
      .filter(([, , sg]) => sg > 0)
      .map(([n, m]) => String([Math.min(n, m), Math.max(n, m)])),
  );
  const otherMasks = otherSpecs.map(([k, s]) => [k, fieldMask(s)]);

  const sheetExact = new Set();
  const sheetPlus = new Set();
  for (const cand of candidates) {
    const [n, m, sg] = cand.glyph;
    if (!Number.isInteger(n) || !Number.isInteger(m) || Math.abs(sg) !== 1) {
      throw new Error(`Invalid mode [${cand.glyph}]`);
    }
    const key = String(cand.glyph);
    if (forbiddenExact.has(key) || sheetExact.has(key)) {
      throw new Error(`Mode [${cand.glyph}] is already used`);
    }
    sheetExact.add(key);
    if (sg > 0) {
      const pkey = String([Math.min(n, m), Math.max(n, m)]);
      if (forbiddenPlus.has(pkey) || sheetPlus.has(pkey)) {
        throw new Error(`Symmetric plus mode [${cand.glyph}] is already used`);
      }
      sheetPlus.add(pkey);
    }
    const cm = fieldMask(cand);
    for (const [otherKind, om] of otherMasks) {
      let diff = 0;
      let total = 0;
      for (let j = 0; j < 48; j++) {
        for (let i = 0; i < 48; i++) {
          if (Math.hypot((i + 0.5) / 48 - 0.5, (j + 0.5) / 48 - 0.5) > 0.44)
            continue;
          total++;
          if (cm[j][i] !== om[j][i]) diff++;
        }
      }
      if (diff / total <= 0.2) {
        throw new Error(
          `Candidate [${cand.glyph}] @ ${cand.zoom} is too close to ${otherKind} (${((diff / total) * 100).toFixed(1)}%)`,
        );
      }
    }
  }
}

// ---------- SVG contact sheet renderer ----------

function escapeXML(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function renderContactSheetSVG({ title, subtitle, candidates }) {
  const width = 880;
  const headerH = 84;
  const colHeaderH = 36;
  const rowH = 68;
  const padX = 24;
  const padBottom = 20;
  const height = headerH + colHeaderH + candidates.length * rowH + padBottom;

  const labelColW = 356;
  const themeColW = 230;
  const lightX = padX + labelColW;
  const darkX = lightX + themeColW + 12;

  const sizeOffsets = [38, 108, 178];

  const rows = candidates
    .map((cand, idx) => {
      const y = headerH + colHeaderH + idx * rowH;
      const d = traceCandidatePath(cand);
      const [n, m, sg] = cand.glyph;
      const modeCode = `[${n}, ${m}, ${sg}] @ ${cand.zoom}`;
      const modePretty = `${n}·${m} (${sg > 0 ? "+" : "−"}) at ${cand.zoom}`;

      const renderCells = (baseX, fgColor, accentColor) =>
        SIZES.map((sz, sIdx) => {
          const cx = baseX + sizeOffsets[sIdx];
          const cy = y + rowH / 2;
          const x0 = cx - sz / 2;
          const y0 = cy - sz / 2;
          const scale = (sz / ITEM_PX).toFixed(5);
          const color = cand.chosen ? accentColor : fgColor;
          return `<g transform="translate(${x0.toFixed(1)}, ${y0.toFixed(1)}) scale(${scale})">
        <path d="${d}" fill="${color}" fill-rule="evenodd"/>
      </g>`;
        }).join("\n      ");

      const rowBg = cand.chosen
        ? `<rect x="${padX - 8}" y="${y + 4}" width="${width - 2 * padX + 16}" height="${rowH - 8}" rx="8" fill="#D9A441" fill-opacity="0.10" stroke="#D9A441" stroke-opacity="0.45"/>`
        : idx % 2 === 1
          ? `<rect x="${padX - 8}" y="${y + 4}" width="${width - 2 * padX + 16}" height="${rowH - 8}" rx="8" fill="#1C1B18" fill-opacity="0.03"/>`
          : "";

      const chosenBadge = cand.chosen
        ? `<rect x="${padX + 186}" y="${y + 13}" width="58" height="18" rx="9" fill="${THEMES.light.grain}"/>
      <text x="${padX + 215}" y="${y + 25}" text-anchor="middle" font-family="ui-sans-serif, system-ui, sans-serif" font-size="10" font-weight="700" fill="#FFFFFF" letter-spacing="0.04em">CHOSEN</text>`
        : "";

      return `  <g>
    ${rowBg}
    <text x="${padX}" y="${y + 26}" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" font-size="13" font-weight="600" fill="#1C1B18">${escapeXML(modeCode)}</text>
    ${chosenBadge}
    <text x="${padX}" y="${y + 44}" font-family="ui-sans-serif, system-ui, sans-serif" font-size="12" fill="#5C5850">${escapeXML(modePretty)} — ${escapeXML(cand.figure)}</text>
    <rect x="${lightX}" y="${y + 8}" width="${themeColW}" height="${rowH - 16}" rx="6" fill="#F7F6F3" stroke="#E2DFD7"/>
    <rect x="${darkX}" y="${y + 8}" width="${themeColW}" height="${rowH - 16}" rx="6" fill="#14161A" stroke="#262930"/>
    ${renderCells(lightX, "#1C1B18", THEMES.light.grain)}
    ${renderCells(darkX, "#ECE9E1", THEMES.dark.grain)}
  </g>`;
    })
    .join("\n");

  const sizeHeaders = (baseX, color) =>
    SIZES.map(
      (sz, i) =>
        `<text x="${baseX + sizeOffsets[i]}" y="${headerH + 23}" text-anchor="middle" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" font-size="11" font-weight="600" fill="${color}">${sz}px</text>`,
    ).join("\n  ");

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}">
  <rect width="${width}" height="${height}" rx="12" fill="#FFFFFF"/>
  <text x="${padX}" y="36" font-family="ui-sans-serif, system-ui, sans-serif" font-size="18" font-weight="700" fill="#1C1B18">${escapeXML(title)}</text>
  <text x="${padX}" y="58" font-family="ui-sans-serif, system-ui, sans-serif" font-size="12.5" fill="#5C5850">${escapeXML(subtitle)}</text>
  <line x1="${padX}" y1="${headerH - 8}" x2="${width - padX}" y2="${headerH - 8}" stroke="#E6E3DC"/>

  <text x="${padX}" y="${headerH + 23}" font-family="ui-sans-serif, system-ui, sans-serif" font-size="11" font-weight="700" fill="#6E6A60" letter-spacing="0.04em">MODE ([n, m, sign] @ zoom)</text>
  <text x="${lightX + 10}" y="${headerH + 10}" font-family="ui-sans-serif, system-ui, sans-serif" font-size="10" font-weight="700" fill="#6E6A60" letter-spacing="0.05em">LIGHT THEME</text>
  <text x="${darkX + 10}" y="${headerH + 10}" font-family="ui-sans-serif, system-ui, sans-serif" font-size="10" font-weight="700" fill="#6E6A60" letter-spacing="0.05em">DARK THEME</text>
  ${sizeHeaders(lightX, "#5C5850")}
  ${sizeHeaders(darkX, "#5C5850")}

${rows}
</svg>
`;
}

async function rasterizeSVGsToPNG(sheets) {
  let chromium;
  try {
    ({ chromium } = await import("@playwright/test"));
  } catch {
    return;
  }
  let browser;
  try {
    browser = await chromium.launch({
      executablePath: process.env.CHROMIUM_PATH || undefined,
    });
    const page = await browser.newPage({ deviceScaleFactor: 2 });
    for (const { svgContent, pngPath } of sheets) {
      await page.setContent(
        `<!doctype html><body style="margin:0;display:inline-block;background:transparent">${svgContent}</body>`,
      );
      const svgEl = page.locator("svg");
      await svgEl.screenshot({ path: pngPath });
      console.log("wrote", pngPath);
    }
  } catch (err) {
    console.warn("skipping PNG rasterization:", err.message);
  } finally {
    await browser?.close();
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  assertLegalCandidates("alerts", ALERTS_CANDIDATES);
  assertLegalCandidates("ci", CI_CANDIDATES);

  mkdirSync(outDir, { recursive: true });

  const sheets = [
    {
      name: "alerts-candidates",
      title: "Alerts glyph — Chladni eigenmode candidates",
      subtitle:
        "Destination glyph for AlertsButton.jsx and PhoneNav.jsx · 8 unused legal modes rendered at 16, 20 and 32px in light and dark",
      candidates: ALERTS_CANDIDATES,
    },
    {
      name: "ci-candidates",
      title: "CI glyph — Chladni eigenmode candidates",
      subtitle:
        "Item glyph for CI in Sidebar.jsx, CIPage.jsx, RepoRuns.jsx and pins · 8 unused legal modes rendered at 16, 20 and 32px in light and dark",
      candidates: CI_CANDIDATES,
    },
  ].map((sheet) => {
    const svgPath = join(outDir, `${sheet.name}.svg`);
    const pngPath = join(outDir, `${sheet.name}.png`);
    const svgContent = renderContactSheetSVG(sheet);
    writeFileSync(svgPath, svgContent);
    console.log("wrote", svgPath);
    return { svgPath, pngPath, svgContent };
  });

  await rasterizeSVGsToPNG(sheets);
}
