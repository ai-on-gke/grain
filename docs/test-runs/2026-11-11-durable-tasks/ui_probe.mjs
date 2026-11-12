#!/usr/bin/env node
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

// §1 item 7: the durable task page at 1440x900 and 390x844, light and
// dark. The sandbox's file reader cannot show a PNG, so this measures what
// the screenshots would be looked at for: the brief, Edit brief, the
// original description under its disclosure, the Context panel (version,
// age, stale badge, cursor, Compact and Write handover...), the collapsed
// summaries, no horizontal overflow, and that /tasks/<id>#comment=<id> of a
// summarised comment opens its disclosure and lands the highlight.
//
// Usage (from anywhere): node ui_probe.mjs <url> <code> <task-id>
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const { chromium } = await import(path.join(root, "ui/node_modules/@playwright/test/index.mjs"));
const { signIn } = await import(path.join(root, "ui/e2e/accesscode.js"));

const [url, code, taskId] = process.argv.slice(2);
const api = async (p) =>
  (await fetch(url + p, { headers: { Authorization: `Bearer ${code}` } })).json();
const ctxView = await api(`/api/tasks/${taskId}/context`);
const task = await api(`/api/tasks/${taskId}`);
const summarised = (task.comments || []).find(
  (c) => c.id <= ctxView.compacted_through && !c.handover && c.body,
);
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const results = [];
for (const [name, viewport] of [["desktop", { width: 1440, height: 900 }], ["phone", { width: 390, height: 844 }]]) {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({ viewport, colorScheme: theme, isMobile: name === "phone" });
    await context.addCookies(cookies);
    await context.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${url}/tasks/${taskId}`);
    await page.getByText(/Edit brief|Write a brief/).first().waitFor({ timeout: 20000 });
    const r = await page.evaluate(() => {
      const txt = document.body.innerText;
      const overflow = document.documentElement.scrollWidth - window.innerWidth;
      const wide = [...document.querySelectorAll("main *")]
        .filter((el) => el.getBoundingClientRect().right > window.innerWidth + 1 && el.getClientRects().length)
        .slice(0, 3)
        .map((el) => `${el.tagName}.${el.className}`.slice(0, 60));
      return {
        overflowPx: overflow,
        wideElements: wide,
        editBrief: /Edit brief|Write a brief/i.test(txt),
        originalDisclosure: [...document.querySelectorAll("summary")].some((s) => /Original description/.test(s.textContent)),
        contextPanel: !!document.querySelector('[data-testid="context-compaction"]'),
        staleBadge: !!document.querySelector('[data-testid="handover-stale"]'),
        compact: [...document.querySelectorAll("button")].some((b) => /^Compact/i.test(b.textContent.trim())),
        writeHandover: /Write handover/i.test(txt),
        collapsed: document.querySelectorAll("details.timeline-summarised").length,
        bg: getComputedStyle(document.body).backgroundColor,
      };
    });
    if (summarised) {
      await page.goto(`${url}/tasks/${taskId}#comment=${summarised.id}`);
      // The highlight fades (.comment-highlight), so catch it while it is on.
      const sawHighlight = await page
        .waitForFunction((id) => document.getElementById(`comment-${id}`)?.classList.contains("comment-highlight"), summarised.id, { timeout: 8000, polling: 50 })
        .then(() => true, () => false);
      await page.waitForTimeout(300);
      r.deepLink = await page.evaluate((id) => {
        const el = document.getElementById(`comment-${id}`) || document.querySelector(`[data-comment-id="${id}"]`);
        if (!el) return "comment element not found";
        const d = el.querySelector("details.timeline-summarised") || el.closest("details");
        const rect = el.getBoundingClientRect();
        return {
          open: d ? d.open : "no disclosure",
          inView: rect.top >= 0 && rect.top < window.innerHeight,
        };
      }, summarised.id);
      if (typeof r.deepLink === "object") r.deepLink.highlighted = sawHighlight;
    }
    r.errors = errors;
    results.push({ width: name, theme, ...r });
    await context.close();
  }
}
await browser.close();
console.log(JSON.stringify({ taskId, cursor: ctxView.compacted_through, summarisedComment: summarised?.id, results }, null, 1));
