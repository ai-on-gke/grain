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


// Lays every PNG in $1 out on $1/contact-sheet.png, the layout of
// ui/e2e/screenshot.mjs's contactSheet: node contact-sheet.mjs <dir>
import fs from "node:fs";
import path from "node:path";
import { chromium } from "./lib.mjs";

const dir = process.argv[2];
const files = fs.readdirSync(dir).filter((f) => f.endsWith(".png") && f !== "contact-sheet.png").sort();
const cells = files.map((f) => `<figure><img src="data:image/png;base64,${fs.readFileSync(path.join(dir, f)).toString("base64")}"><figcaption>${f.replace(/\.png$/, "")}</figcaption></figure>`);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 2000, height: 800 } });
await page.setContent(`<!doctype html><style>
  body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
    display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
  figure { margin: 0; }
  img { display: block; max-width: 480px; max-height: 600px; border: 1px solid #333; }
  figcaption { padding-top: 4px; }
</style>${cells.join("")}`);
await page.screenshot({ path: path.join(dir, "contact-sheet.png"), fullPage: true });
await browser.close();
console.log(`${files.length} shots -> ${path.join(dir, "contact-sheet.png")}`);
