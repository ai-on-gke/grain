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

// Item 4 of docs/test-plans/2026-11-08-afternoon.md §1: opens every
// legacy-capability picker it can find on each surface and reports what
// its menu says -- the "None offered -- grant access under Grants" hint,
// or the example-legacy row -- and whether a Grants picker is on the same
// surface. Same environment variables as walkthrough.mjs (DEMO_A with
// -legacy-capability, DEMO_B without). Run from ui/.

import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../ui/package.json", import.meta.url),
);
const { chromium } = require("@playwright/test");

const HINT = "None offered -- grant access under Grants";

async function signIn(url, code) {
  const res = await fetch(`${url}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  const { hostname } = new URL(url);
  return res.headers.getSetCookie().map((h) => {
    const [pair] = h.split(";");
    const at = pair.indexOf("=");
    return {
      name: pair.slice(0, at).trim(),
      value: pair.slice(at + 1).trim(),
      domain: hostname,
      path: "/",
    };
  });
}

// probe opens each combobox labelled "...capabilities" on the page.
async function probe(page, label, where) {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(800);
  const boxes = page.getByRole("combobox", { name: /capabilities/i });
  const n = await boxes.count();
  const grants = await page.getByText(/^Grants$/).count();
  if (n === 0) {
    console.log(JSON.stringify({ item: 4, label, where, pickers: 0, grants }));
    return;
  }
  for (let i = 0; i < n; i++) {
    const box = boxes.nth(i);
    const name =
      (await box.getAttribute("aria-label")) ||
      (await box.getAttribute("aria-labelledby"));
    if ((await box.getAttribute("aria-disabled")) === "true") {
      console.log(
        JSON.stringify({ item: 4, label, where, picker: name, disabled: true }),
      );
      continue;
    }
    await box.scrollIntoViewIfNeeded();
    await box.click();
    await page.waitForTimeout(400);
    const menu = page
      .locator("[role=listbox], .MuiAutocomplete-popper, .MuiPopover-paper")
      .last();
    const text = (await menu.innerText().catch(() => "")).replace(/\s+/g, " ");
    let wraps = null;
    const hintEl = page.getByText(HINT).last();
    if (await hintEl.count()) {
      const b = await hintEl.boundingBox();
      const vw = page.viewportSize().width;
      wraps = b ? b.x >= 0 && b.x + b.width <= vw + 1 : null;
    }
    console.log(
      JSON.stringify({
        item: 4,
        label,
        where,
        picker: name,
        hint: text.toLowerCase().includes(HINT.toLowerCase()),
        exampleLegacy: /example-legacy|Example legacy/i.test(text),
        withinViewport: wraps,
        grants,
        menu: text.slice(0, 120),
      }),
    );
    await page.keyboard.press("Escape");
    await page.waitForTimeout(200);
  }
}

const browser = await chromium.launch();
try {
  for (const [label, base, code] of [
    ["legacy", process.env.DEMO_A, process.env.CODE_A],
    ["no-legacy", process.env.DEMO_B, process.env.CODE_B],
  ]) {
    for (const vp of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      const ctx = await browser.newContext({ viewport: vp, baseURL: base });
      await ctx.addCookies(await signIn(base, code));
      const page = await ctx.newPage();
      const tag = `${label}@${vp.width}`;
      await page.goto("/settings/tasks");
      await probe(page, tag, "Settings → Tasks");
      await page.goto("/repos/acme/app");
      await probe(page, tag, "Repository page");
      await page.goto("/new");
      await probe(page, tag, "New task (/new)");
      await page.goto("/templates/new");
      await probe(page, tag, "Template (/templates/new)");
      await page.goto("/schedules/new");
      await probe(page, tag, "Schedule (/schedules/new)");
      await page.goto("/tasks/1");
      await probe(page, tag, "Task pane (/tasks/1)");
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}
