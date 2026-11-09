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

// Does an `externalRef:any:<REF>` or `owner:any:<email>` condition match
// a seeded task whose reference is upper-case, and does it reach closed
// tasks older than the first page? Over a seed.mjs server:
//   GRAIN_SERVER=... GRAIN_TOKEN=... node extref-case-probe.mjs
import { createRequire } from "node:module";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const b = await chromium.launch();
const ctx = await b.newContext({ baseURL: S, viewport: { width: 1440, height: 900 } });
await ctx.request.post("/api/auth/access-code", { data: { code: T } });
const p = await ctx.newPage();
let closedPages = 0;
p.on("request", (r) => {
  if (r.url().includes("state=closed") && !r.url().includes("search=")) closedPages++;
});
for (const f of [
  "externalRef:any:CA-100",
  "externalRef:any:ca-100",
  "f=state:any:closed&f=externalRef:any:CA-9012",
  "f=state:any:closed&f=owner:any:deepowner@example.com",
]) {
  closedPages = 0;
  const q = f.startsWith("f=") ? f : `f=${f}`;
  await p.goto(`/?${q}`);
  await p.waitForTimeout(f.includes("closed") ? 15000 : 2500);
  const rows = await p.locator(".task-row").allInnerTexts();
  console.log(JSON.stringify({ query: q, rows: rows.length, sample: rows.slice(0, 2).map((r) => r.split("\n")[0].slice(0, 60)), closedPagesRead: closedPages }));
}
await b.close();
