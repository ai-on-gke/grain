// Puts the before and after of each fix in the 2026-11-03 UI fixes walk
// side by side on one image, cropped to where the fix is:
//
//   cd ui && node ../docs/test-runs/2026-11-03-ui-fixes/fix-sheet.mjs \
//     screenshots/before screenshots/after screenshots/fix-sheet.png
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const require = createRequire(path.resolve("package.json"));
const { chromium } = require("playwright");
const [before, after, out] = process.argv.slice(2).map((p) => path.resolve(p));

// [label, file, crop x, y, w, h, scale]
const crops = [
  ["F1 build stamp, light", "inbox-desktop-light.png", 0, 840, 300, 60, 2],
  ["F1 build stamp, dark", "inbox-desktop-dark.png", 0, 840, 300, 60, 2],
  [
    "F2 thin percentiles, report, light",
    "boards_board_3-desktop-light.png",
    760,
    540,
    680,
    200,
    1,
  ],
  [
    "F3 New chat at 390×640",
    "session_new-390x640-light.png",
    0,
    0,
    390,
    640,
    1,
  ],
];

const cell = (dir, [, file, x, y, w, h, s]) =>
  `<div style="width:${w * s}px;height:${h * s}px;overflow:hidden;border:1px solid #888">
     <img src="${pathToFileURL(path.join(dir, file)).href}"
          style="transform-origin:0 0;transform:scale(${s}) translate(${-x}px,${-y}px)">
   </div>`;
const html = `<body style="font:14px sans-serif;margin:16px;background:#fff">
  ${crops
    .map(
      (c) => `<h3 style="margin:12px 0 4px">${c[0]}</h3>
      <div style="display:flex;gap:24px;align-items:flex-start">
        <div><div>before</div>${cell(before, c)}</div>
        <div><div>after</div>${cell(after, c)}</div>
      </div>`,
    )
    .join("")}</body>`;

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1500, height: 800 } });
// A file of its own rather than setContent, so the file: images load.
const sheet = `${out}.html`;
await fs.writeFile(sheet, html);
await page.goto(pathToFileURL(sheet).href, { waitUntil: "load" });
await page.screenshot({ path: out, fullPage: true });
await browser.close();
await fs.rm(sheet);
console.log(out);
