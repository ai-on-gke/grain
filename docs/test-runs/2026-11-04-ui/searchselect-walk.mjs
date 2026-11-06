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

// §10 item 3: SearchSelect by keyboard only on every converted form, at
// 1440 and 390. For each form it opens the form, then for each of up to
// MAX_BOXES SearchSelect comboboxes in it: opens the list with ArrowDown,
// runs axe on the open list, searches by a word of an option, picks it
// with Enter (watching for any write request, a closed form or a changed
// URL -- i.e. a submit), reopens and Escapes (the list must close, the
// form must stay), and clears (Ctrl+A, Backspace, Tab) where the box is
// clearable. Usage: GRAIN_SERVER=... GRAIN_TOKEN=... node searchselect-walk.mjs [form-filter]
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const only = process.argv[2] ? new RegExp(process.argv[2], "i") : null;
const out = new URL(".", import.meta.url).pathname;
const MAX_BOXES = 3;

const dialog = (page) => page.getByRole("dialog").last();
const FORMS = [
  {
    name: "New Task",
    open: async (page) => {
      await page.goto("/new");
      const d = dialog(page);
      await d.waitFor();
      await d.getByRole("button", { name: "Advanced options" }).click().catch(() => {});
      return d;
    },
  },
  {
    name: "Schedule",
    open: async (page) => {
      await page.goto("/schedules");
      await page.getByRole("button", { name: "+ New schedule" }).click();
      return dialog(page);
    },
  },
  {
    name: "Template",
    open: async (page) => {
      await page.goto("/templates");
      await page.getByRole("button", { name: "+ New template" }).click();
      return dialog(page);
    },
  },
  {
    name: "Suite run",
    open: async (page) => {
      await page.goto("/suites");
      await page.getByRole("button", { name: "Run…" }).first().click();
      return dialog(page);
    },
  },
  {
    name: "Bulk edit",
    open: async (page) => {
      await page.goto("/?state=queued");
      const rows = page.locator(".task-row");
      await rows.first().waitFor();
      await rows.nth(0).getByRole("checkbox").check();
      await rows.nth(1).getByRole("checkbox").check();
      await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
      const d = dialog(page);
      await d.waitFor();
      // Turn on every field so its picker is enabled.
      const boxes = d.getByRole("checkbox");
      for (let i = 0; i < (await boxes.count()); i++) await boxes.nth(i).check().catch(() => {});
      return d;
    },
  },
  { name: "Settings → Models", open: async (page) => (await page.goto("/settings/models"), page.locator("#root")) },
  { name: "Git connections", open: async (page) => (await page.goto("/settings/git"), page.locator("#root")) },
  { name: "Preferences time zone", open: async (page) => (await page.goto("/settings/preferences"), page.locator("#root")) },
  { name: "Repo releases", open: async (page) => (await page.goto("/repos/acme/widgets/releases"), page.locator("#root")) },
  {
    name: "NarrowingBar",
    open: async (page) => {
      await page.goto("/");
      await page.locator(".task-row").first().waitFor();
      const filter = page.getByRole("button", { name: /^Filter/ }).first();
      if (await filter.isVisible().catch(() => false)) await filter.click();
      await page.getByRole("button", { name: /^(Filter by…|Add filter)$/ }).click();
      await page.getByRole("menuitem", { name: "Repo" }).click();
      return page.locator(".narrowing-row").last();
    },
  },
  { name: "Logs", open: async (page) => (await page.goto("/system/logs"), page.locator("#root")) },
  { name: "CI", open: async (page) => (await page.goto("/ci"), page.locator("#root")) },
  {
    name: "Customize sidebar",
    open: async (page) => {
      await page.goto("/");
      await page.locator(".task-row").first().waitFor();
      const btn = page.getByRole("button", { name: "Customize sidebar" });
      if (!(await btn.isVisible().catch(() => false))) {
        await page.getByRole("button", { name: /open navigation|menu/i }).first().click().catch(() => {});
      }
      await btn.click();
      return page.getByRole("dialog", { name: "Customize sidebar" });
    },
  },
];

async function walkBox(page, container, box, i) {
  const r = { box: i };
  r.label = (await box.getAttribute("aria-label")) || (await box.evaluate((el) => el.labels?.[0]?.innerText || el.id));
  const root = box.locator("xpath=ancestor::*[contains(@class,'MuiAutocomplete-root')][1]");
  const multiple = (await root.locator(".MuiAutocomplete-tag").count()) > 0 || (await box.evaluate((el) => !!el.closest(".MuiAutocomplete-root")?.querySelector(".MuiChip-root")));
  r.clearable = (await root.locator(".MuiAutocomplete-clearIndicator").count()) > 0;
  const listbox = page.locator(`[id="${await box.getAttribute("id")}-listbox"]`);
  const writes = [];
  const onReq = (req) => {
    if (!["GET", "HEAD"].includes(req.method())) writes.push(`${req.method()} ${new URL(req.url()).pathname}`);
  };
  page.on("request", onReq);
  try {
    await box.focus();
    const before = await box.inputValue();
    r.before = before;
    await page.keyboard.press("ArrowDown");
    await listbox.waitFor({ timeout: 3000 });
    const options = await listbox.getByRole("option").allInnerTexts();
    r.options = options.length;
    // axe over the open list and its box.
    const axe = await new AxeBuilder({ page })
      .include(`[id="${await box.getAttribute("id")}-listbox"]`)
      .include(`[id="${await box.getAttribute("id")}"]`)
      .analyze();
    r.axe = axe.violations.map((v) => `${v.id}(${v.impact}) x${v.nodes.length}`);
    r.axeIncomplete = axe.incomplete.map((v) => v.id);
    // Search by a word of an option other than the current one.
    const target = options.map((o) => o.split("\n")[0].trim()).find((o) => o && o !== before) || options[0]?.split("\n")[0].trim();
    if (!target) {
      r.note = "no options";
      await page.keyboard.press("Escape");
      return r;
    }
    const word = target.split(/[\s/:._-]+/).filter((w) => w.length >= 3).pop() || target;
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.type(word, { delay: 10 });
    await page.waitForTimeout(150);
    const hits = await listbox.getByRole("option").allInnerTexts().catch(() => []);
    r.search = { word, hits: hits.length, targetListed: hits.some((h) => h.includes(target)) };
    // Pick with Enter (autoHighlight puts the first hit under it).
    const url = page.url();
    writes.length = 0;
    await page.keyboard.press("Enter");
    await page.waitForTimeout(400);
    r.pick = {
      value: await box.inputValue().catch(() => null),
      chips: await root.locator(".MuiChip-root").allInnerTexts().catch(() => []),
      listClosed: multiple ? null : !(await listbox.isVisible().catch(() => false)),
      formStillOpen: await container.isVisible().catch(() => false),
      urlUnchanged: page.url() === url,
      writes: [...writes],
    };
    const isMultiple = multiple || r.pick.chips.length > 0;
    // Escape on an open list.
    await box.focus();
    await page.keyboard.press("ArrowDown");
    await listbox.waitFor({ timeout: 2000 }).catch(() => {});
    const opened = await listbox.isVisible().catch(() => false);
    await page.keyboard.press("Escape");
    await page.waitForTimeout(250);
    r.escape = {
      opened,
      listClosed: !(await listbox.isVisible().catch(() => false)),
      formStillOpen: await container.isVisible().catch(() => false),
      urlUnchanged: page.url() === url,
    };
    // Clear.
    if (r.clearable || isMultiple) {
      await box.focus();
      if (isMultiple) {
        await page.keyboard.press("Backspace");
      } else {
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.press("Backspace");
      }
      await page.waitForTimeout(150);
      r.clear = { value: await box.inputValue().catch(() => null), chips: (await root.locator(".MuiChip-root").count().catch(() => 0)) };
    }
    // Enter on an open list that matches nothing must not submit either.
    await box.focus();
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.type("zzqxv", { delay: 10 });
    await page.waitForTimeout(150);
    const noMatch = await page.getByText("No matches").first().isVisible().catch(() => false);
    writes.length = 0;
    const url2 = page.url();
    await page.keyboard.press("Enter");
    await page.waitForTimeout(500);
    r.enterNoMatch = {
      noMatchShown: noMatch,
      formStillOpen: await container.isVisible().catch(() => false),
      urlUnchanged: page.url() === url2,
      writes: [...writes],
    };
    await page.keyboard.press("Escape").catch(() => {});
  } catch (e) {
    r.error = String(e.message || e).split("\n")[0];
  } finally {
    page.off("request", onReq);
  }
  return r;
}

const browser = await chromium.launch();
const results = [];
try {
  for (const width of [1440, 390]) {
    for (const form of FORMS) {
      if (only && !only.test(form.name)) continue;
      const ctx = await browser.newContext({ baseURL: S, viewport: { width, height: width > 500 ? 900 : 844 } });
      await ctx.request.post("/api/auth/access-code", { data: { code: T } });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      const fr = { form: form.name, width, boxes: [] };
      try {
        const container = await form.open(page);
        await container.waitFor({ timeout: 8000 });
        await page.waitForTimeout(600);
        const boxes = container.locator(".MuiAutocomplete-root input[role='combobox']:not([disabled])");
        fr.found = await boxes.count();
        const ids = await boxes.evaluateAll((els) => els.filter((e) => e.offsetParent !== null).map((e) => e.id));
        fr.ids = ids;
        for (let i = 0; i < Math.min(ids.length, MAX_BOXES); i++) {
          const b = page.locator(`[id="${ids[i]}"]`);
          if (!(await b.isVisible().catch(() => false))) continue;
          await b.scrollIntoViewIfNeeded().catch(() => {});
          fr.boxes.push(await walkBox(page, container, b, i));
        }
      } catch (e) {
        fr.error = String(e.message || e).split("\n")[0];
      }
      await page.screenshot({ path: `${out}/ss-${form.name.replace(/[^a-z]+/gi, "-")}-${width}.png` }).catch(() => {});
      results.push(fr);
      console.log(JSON.stringify(fr));
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}
await fs.writeFile(`${out}/searchselect-walk${only ? "-" + process.argv[2].replace(/\W+/g, "_") : ""}.json`, JSON.stringify(results, null, 2));
