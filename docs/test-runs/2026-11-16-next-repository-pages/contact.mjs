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

// contact.mjs <out.png> <img>...: lays screenshots out on one labelled
// sheet, each scaled to a 360 px column and cropped to 900 px tall.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const requireNg = createRequire(
  new URL("../../../ui-ng/package.json", import.meta.url),
);
const { chromium } = requireNg("playwright-core");
const [outPath, ...imgs] = process.argv.slice(2);
const cells = imgs
  .map((p) => {
    const b64 = fs.readFileSync(p).toString("base64");
    return `<figure><figcaption>${path.basename(p)}</figcaption><div><img src="data:image/png;base64,${b64}"></div></figure>`;
  })
  .join("");
const html = `<style>body{margin:8px;font:12px sans-serif;display:flex;flex-wrap:wrap;gap:8px;width:${4 * 368}px}
figure{margin:0;width:360px}figcaption{height:16px;overflow:hidden}div{height:900px;overflow:hidden;border:1px solid #888}img{width:360px}</style>${cells}`;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 4 * 368 + 16, height: 900 } });
await page.setContent(html);
await page.screenshot({ path: outPath, fullPage: true });
await browser.close();
