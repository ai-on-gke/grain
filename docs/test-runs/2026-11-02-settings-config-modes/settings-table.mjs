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

// Walks every Settings section of one deployment and dumps, per section,
// each control's disabled state, the notices shown and every link's
// target (../2026-11-02-settings-config-modes.md).
//
//   node settings-table.mjs <mode> <base-url> <bearer-token> <out.json>
//
// Run from ui/ (so `playwright` resolves) with
// PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright.
import { chromium } from "playwright";
import fs from "node:fs";

const [mode, BASE, TOK, OUT] = process.argv.slice(2);
const SECTIONS = [
  "models",
  "sandboxes",
  "executors",
  "credentials",
  "access",
  "preferences",
  "upgrade",
  "caches",
  "engine",
  "config-repo",
  "general",
  "git",
  "repositories",
  "tasks",
  "agents",
  "capabilities",
  "ci",
  "memory",
];

const browser = await chromium.launch();
const ctx = await browser.newContext({
  extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
  viewport: { width: 1440, height: 900 },
});
const page = await ctx.newPage();
const table = { mode, base: BASE, sections: {} };

for (const sec of SECTIONS) {
  await page.goto(`${BASE}/settings/${sec}`);
  await page.locator('[data-testid="settings-section"]').waitFor();
  // A daemon holds a live stream open, so networkidle never comes: let
  // the section's own fetches land instead.
  await page.waitForLoadState("networkidle", { timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(1000);
  table.sections[sec] = await page.evaluate(() => {
    const root = document.querySelector('[data-testid="settings-page"]');
    const sectionEl = document.querySelector(
      '[data-testid="settings-section"]',
    );
    const text = (el) =>
      (el?.innerText || el?.textContent || "").replace(/\s+/g, " ").trim();
    const labelOf = (el) => {
      if (el.getAttribute("aria-label")) return el.getAttribute("aria-label");
      if (el.id) {
        const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
        if (l) return text(l);
      }
      const lb = el.getAttribute("aria-labelledby");
      if (lb)
        return lb
          .split(" ")
          .map((id) => text(document.getElementById(id)))
          .join(" ");
      const wrap = el.closest("label");
      if (wrap) return text(wrap);
      return (
        text(el) ||
        el.getAttribute("name") ||
        el.getAttribute("data-setting-id") ||
        ""
      );
    };
    const fields = [];
    for (const el of sectionEl.querySelectorAll(
      "input, select, textarea, button, [role=button], [role=combobox], [role=switch]",
    )) {
      if (el.type === "hidden" || el.closest("[aria-hidden=true]")) continue;
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) continue;
      const disabled =
        el.matches(":disabled") ||
        el.getAttribute("aria-disabled") === "true" ||
        el.classList.contains("Mui-disabled");
      fields.push({
        kind: el.tagName.toLowerCase() + (el.type ? `:${el.type}` : ""),
        label: labelOf(el).slice(0, 80),
        settingId:
          el.closest("[data-setting-id]")?.getAttribute("data-setting-id") ||
          "",
        disabled,
      });
    }
    const notices = [];
    for (const el of root.querySelectorAll(
      '[data-testid*="notice"], [data-testid*="banner"], [data-testid*="refusal"], .MuiAlert-root',
    )) {
      const t = text(el);
      if (t)
        notices.push({
          id: el.getAttribute("data-testid") || "alert",
          text: t.slice(0, 240),
        });
    }
    const links = [...sectionEl.querySelectorAll("a[href]")].map((a) => ({
      text: text(a).slice(0, 60),
      href: a.getAttribute("href"),
    }));
    return {
      readonlyFieldsets: sectionEl.querySelectorAll("[data-settings-readonly]")
        .length,
      fields,
      notices,
      links,
    };
  });
  const s = table.sections[sec];
  console.log(
    `${mode} ${sec}: ${s.fields.length} controls (${s.fields.filter((f) => f.disabled).length} disabled), ${s.notices.length} notices, ${s.links.length} links`,
  );
}
await browser.close();
fs.writeFileSync(OUT, JSON.stringify(table, null, 1) + "\n");
