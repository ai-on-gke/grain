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

// Lays review.mjs's annotated shots out in OUT/contact-sheet.png.
// OUT=... node sheet.mjs (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const OUT = process.env.OUT;
const shots = fs
  .readdirSync(OUT)
  .filter((f) => f.endsWith(".png") && f !== "contact-sheet.png")
  .sort();
const cells = shots
  .map(
    (f) =>
      `<figure><img src="data:image/png;base64,${fs.readFileSync(path.join(OUT, f)).toString("base64")}"><figcaption>${f}</figcaption></figure>`,
  )
  .join("");
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1800, height: 800 } });
await page.setContent(
  `<style>body{margin:8px;font:13px sans-serif;display:grid;grid-template-columns:repeat(4,1fr);gap:8px;align-items:start}figure{margin:0}img{width:100%;border:1px solid #888}figcaption{padding:2px 0}</style>${cells}`,
);
await page.screenshot({
  path: path.join(OUT, "contact-sheet.png"),
  fullPage: true,
});
await browser.close();
console.log(shots.length, "shots");
