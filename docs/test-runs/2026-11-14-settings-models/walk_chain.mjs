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

// docs/test-plans/2026-11-14.md §4 item 3 (UI half): save default walk-a
// with fallbacks walk-b, walk-c in /next/settings/models of the env.sh
// daemon, then reload it and the classic Settings → Models pane and read
// back the order each shows. Usage: node walk_chain.mjs [save|read]

import { createRequire } from "node:module";

const requireNg = createRequire(new URL("../../../ui-ng/package.json", import.meta.url));
const { chromium } = requireNg("@playwright/test");
const proxy = "http://127.0.0.1:4296";
const mode = process.argv[2] || "save";

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addCookies([{ name: "iapsim_email", value: "alice@example.com", url: proxy }]);
const page = await context.newPage();

async function nextOrder() {
  await page.goto(`${proxy}/next/settings/models`, { waitUntil: "domcontentloaded" });
  const card = page.getByTestId("card-model-defaults");
  await card.waitFor({ timeout: 15000 });
  await page.waitForTimeout(800);
  const def = (await card.getByTestId("picker-default-model").innerText()).trim().split("\n")[0];
  const fb = await card.locator('[data-testid^="fallback-row-"] .mp-chain-model').allInnerTexts();
  return [def, ...fb.map((s) => s.trim())];
}

if (mode === "save") {
  const before = await nextOrder();
  console.log("next before:", JSON.stringify(before));
  const card = page.getByTestId("card-model-defaults");
  for (const m of ["walk-b", "walk-c"]) {
    await card.getByTestId("picker-add-fallback").click();
    await page.getByTestId("picker-add-fallback-opt").and(page.locator(`[data-model="${m}"]`)).click();
  }
  await page.getByTestId("settings-save-btn").click();
  await page.getByTestId("settings-save-bar").waitFor({ state: "hidden", timeout: 15000 });
  const dep = await (await context.request.get(`${proxy}/api/deployment`)).json();
  console.log("PUT saved defaultModels:", JSON.stringify(dep.defaultModels));
}

console.log("next after reload:", JSON.stringify(await nextOrder()));

await page.goto(`${proxy}/settings/models`, { waitUntil: "domcontentloaded" });
await page.waitForTimeout(2500);
const labels = await page.locator('[aria-label^="Remove "]').evaluateAll((els) =>
  els.map((e) => e.getAttribute("aria-label").replace(/^Remove /, "")),
);
console.log("classic Remove-button order:", JSON.stringify(labels));
// The classic chain is MUI selects; their hidden inputs hold the chosen ids
// in page order (default first).
const chain = await page.locator("input").evaluateAll((els) =>
  els.map((e) => e.value).filter((v) => /^walk-/.test(v)),
);
console.log("classic select values in page order:", JSON.stringify(chain));
await page.screenshot({ path: "/home/debian/walk-models/captures/classic-models.png", fullPage: true });
await browser.close();
