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

// docs/test-plans/2026-11-15.md §1 item 6: the classic Boards card chip
// (Boards have no /next/ page yet) at 1280 and 390px. Measures every
// "rolled over to" chip against its card and writes a screenshot of the
// first card carrying one to $OUT/board-chip-<width>.png.
//
//   URL=... CODE=... BOARD=board-1 OUT=/tmp node walk-classic-board.mjs

import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const { URL: BASE, CODE, BOARD = "board-1", OUT = "/tmp" } = process.env;

let failed = 0;
const browser = await chromium.launch();
for (const width of [1280, 390]) {
  const context = await browser.newContext({ viewport: { width, height: 900 } });
  const res = await context.request.post(`${BASE}/api/auth/access-code`, {
    data: { code: CODE, name: "alice@example.com" },
  });
  if (!res.ok()) throw new Error(`login: ${res.status()}`);
  // Closed tasks get no column on the default board (ui/src/board.js);
  // add one the way the column editor saves it, so the rolled-over
  // predecessors have somewhere to land.
  await context.addInitScript(() => {
    localStorage.setItem(
      "grain.board.columns",
      JSON.stringify([
        { id: "queued", title: "Waiting", states: ["queued"] },
        { id: "asked", title: "Asked", states: ["awaiting_reply", "awaiting_submit"] },
        { id: "closed", title: "Closed", states: ["closed"] },
      ]),
    );
  });
  const page = await context.newPage();
  await page.goto(`${BASE}/boards/${BOARD}`);
  const chips = page.getByTestId("task-rolled-over-chip");
  try {
    await chips.first().waitFor({ timeout: 15000 });
  } catch {
    console.log(JSON.stringify({ width, ok: false, detail: "no rolled-over chip on the board" }));
    failed++;
    await context.close();
    continue;
  }
  for (const chip of await chips.all()) {
    // The board scrolls sideways on a phone; bring the card in first.
    await chip.scrollIntoViewIfNeeded();
    const m = await chip.evaluate((el) => {
      const card = el.closest(".MuiCard-root, .MuiPaper-root, [data-testid^='board-card']") || el.parentElement;
      const c = card.getBoundingClientRect();
      const r = el.getBoundingClientRect();
      const label = el.querySelector(".MuiChip-label");
      return {
        text: el.textContent,
        inside: r.left >= c.left - 0.5 && r.right <= c.right + 0.5,
        onScreen: r.left >= 0 && r.right <= window.innerWidth,
        truncated: label ? label.scrollWidth > label.clientWidth : false,
        // The chip sits on one line, not wrapped onto two.
        oneLine: r.height <= 32,
        chip: [Math.round(r.left), Math.round(r.right)],
        card: [Math.round(c.left), Math.round(c.right)],
      };
    });
    const ok = m.inside && m.onScreen && m.oneLine && !m.truncated;
    if (!ok) failed++;
    console.log(JSON.stringify({ width, ok, ...m }));
  }
  await chips.first().scrollIntoViewIfNeeded();
  const card = chips.first().locator("xpath=ancestor::*[contains(@class,'MuiCard-root') or contains(@class,'MuiPaper-root')][1]");
  await ((await card.count()) ? card : chips.first()).screenshot({
    path: path.join(OUT, `board-chip-${width}.png`),
  });
  await context.close();
}
await browser.close();
process.exit(failed ? 1 : 0);
