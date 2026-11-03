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

import { chromium } from "playwright";
const [BASE, TOK] = process.argv.slice(2);
const b = await chromium.launch();
const p = await (await b.newContext({ extraHTTPHeaders: { Authorization: `Bearer ${TOK}` } })).newPage();
const out = {};
for (const path of ["/repos/acme/widgets", "/repos/acme/widgets/settings/merge-queue", "/repos/acme/widgets/settings/advanced", "/repos/acme/widgets/settings/ci", "/merge-queue", "/settings/repositories"]) {
  await p.goto(BASE + path); await p.waitForLoadState("networkidle"); await p.waitForTimeout(800);
  const t = await p.locator("body").innerText();
  out[path] = t.split("\n").filter((l) => /re-?check|batch|gather/i.test(l)).slice(0, 8);
}
console.log(JSON.stringify(out, null, 1));
await b.close();
