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

import { createRequire } from "node:module"; import path from "node:path";
const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const [url, code] = process.argv.slice(2);
const b = await chromium.launch();
for (const [w, h] of [[1440, 900], [390, 844]]) for (const scheme of ["light", "dark"]) {
  const ctx = await b.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  await ctx.request.post(`${url}/api/auth/access-code`, { data: { code } });
  const p = await ctx.newPage(); await p.goto(`${url}/settings/hostds`);
  const el = p.getByTestId("hostd-shared-remote"); await el.waitFor();
  const box = await el.boundingBox(); const lh = await el.evaluate((e) => parseFloat(getComputedStyle(e).lineHeight));
  const row = await p.getByTestId("hostd-row-remote").boundingBox();
  const acts = await p.getByTestId("hostd-row-remote").getByRole("button").count();
  const of = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  console.log(`${w} ${scheme}: label ${Math.round(box.width)}x${Math.round(box.height)} (line ${lh}) lines=${Math.round(box.height / lh)} inside-row=${box.y >= row.y && box.y + box.height <= row.y + row.height} buttons=${acts} page-overflow=${of}`);
  await ctx.close();
}
await b.close();
