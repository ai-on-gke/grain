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

// §6 item 2 on the real daemon run_daemon_walk.sh left running: opens each
// parked item in /next/inbox (the long question, the access request, the
// review that relayed findings, the alert) at 1440 and 390 px and measures
// the capped Markdown box -- its height against ~20 lines / a third of the
// window, what Markdown it drew (lists, code card, table, diagram), that
// "Show full …" lifts the cap, that Reply focuses the composer -- then
// acknowledges the question and the alert. Finally opens the question's
// own comment by deep link and samples the highlight over time.
//
// GRAIN_URL=http://127.0.0.1:4280 GRAIN_CODE=... OUT=... node inbox-walk.mjs

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT || "/tmp/md-shots";
const Q = process.env.QUESTION || "2";
fs.mkdirSync(OUT, { recursive: true });

const items = [
  {
    name: "question",
    title: "Cache migration rollout question",
    noun: "question",
  },
  { name: "access", title: "Cache client access request", noun: null },
  { name: "review", title: "Review the cache helpers", noun: null },
  { name: "alert", title: "Cache tier degraded", noun: "alert" },
];

const browser = await chromium.launch();
for (const theme of ["light", "dark"]) {
  for (const width of [1440, 390]) {
    const ctx = await browser.newContext({
      viewport: { width, height: width > 600 ? 900 : 844 },
      colorScheme: theme,
    });
    await ctx.addInitScript(
      ([t]) => localStorage.setItem("grain.themeMode", t),
      [theme],
    );
    await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    for (const it of items) {
      await page.goto(`${BASE}/next/inbox`);
      const row = page.getByText(it.title, { exact: false }).first();
      try {
        await row.click({ timeout: 15_000 });
      } catch (e) {
        console.log(`${it.name} ${width} ${theme}: row not found`);
        continue;
      }
      await page.waitForTimeout(2500);
      const box = page.locator(".capped-md").first();
      const m = await box
        .evaluate((el) => {
          const lh = parseFloat(getComputedStyle(el).lineHeight) || 20;
          const r = el.getBoundingClientRect();
          return {
            h: Math.round(r.height),
            lines: +(r.height / lh).toFixed(1),
            third: Math.round(window.innerHeight / 3),
            scrollH: el.scrollHeight,
            ol: el.querySelectorAll("ol").length,
            nestedUl: el.querySelectorAll("li ul, li ol").length,
            code: el.querySelectorAll("pre").length,
            table: el.querySelectorAll("table").length,
            diagram: el.querySelectorAll("[data-mermaid]").length,
            svg: el.querySelectorAll(".mermaid-canvas svg").length,
            rawHtml: /&lt;|<code>|&quot;/.test(el.innerText),
            overflowX: el.scrollWidth - el.clientWidth,
          };
        })
        .catch(() => null);
      let toggle = null;
      const btn = page.getByRole("button", { name: /^Show full/ }).first();
      if (await btn.isVisible().catch(() => false)) {
        const label = await btn.innerText();
        await btn.click();
        await page.waitForTimeout(400);
        const h2 = await box.evaluate((el) =>
          Math.round(el.getBoundingClientRect().height),
        );
        toggle = { label, openH: h2 };
      }
      await page.screenshot({
        path: `${OUT}/inbox-${it.name}-${width}-${theme}.png`,
      });
      let reply = null;
      const replyBtn = page
        .getByRole("button", { name: "Reply", exact: true })
        .first();
      if (await replyBtn.isVisible().catch(() => false)) {
        await replyBtn.click();
        await page.waitForTimeout(400);
        reply = await page.evaluate(() => {
          const a = document.activeElement;
          return a
            ? `${a.tagName.toLowerCase()}${a.getAttribute("aria-label") ? `[${a.getAttribute("aria-label")}]` : ""}`
            : null;
        });
      }
      const pageOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      console.log(
        `${it.name} ${width} ${theme}: box=${JSON.stringify(m)} toggle=${JSON.stringify(toggle)} reply=${reply} pageOverflow=${pageOverflow}`,
      );
    }
    console.log(
      `errors ${width} ${theme}: ${JSON.stringify(errors.slice(0, 5))}`,
    );
    await ctx.close();
  }
}

// Acknowledge the question and the alert at desktop width.
{
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
  const page = await ctx.newPage();
  for (const it of [items[0], items[3]]) {
    await page.goto(`${BASE}/next/inbox`);
    await page.getByText(it.title).first().click({ timeout: 15_000 });
    const ack = page.getByRole("button", { name: /^Acknowledge/ }).first();
    const visible = await ack.isVisible({ timeout: 5000 }).catch(() => false);
    if (visible) await ack.click();
    await page.waitForTimeout(2000);
    await page.goto(`${BASE}/next/inbox`);
    await page.waitForTimeout(2500);
    const still = await page.getByText(it.title).count();
    console.log(`ack ${it.name}: button=${visible} stillListed=${still}`);
  }

  // Deep link: the question's comment, highlight sampled over 5 s.
  const detail = await (
    await fetch(`${BASE}/api/tasks/${Q}`, {
      headers: { Authorization: `Bearer ${CODE}` },
    })
  ).json();
  const cid = detail.comments?.[0]?.id;
  await page.goto(`${BASE}/next/task/${Q}#comment=${cid}`);
  const samples = [];
  const t0 = Date.now();
  for (let i = 0; i < 12; i++) {
    samples.push(
      await page
        .evaluate(() => {
          const h = document.querySelector(".comment-highlight");
          return h ? getComputedStyle(h).backgroundColor : "none";
        })
        .then((c) => `${Date.now() - t0}ms:${c}`),
    );
    await page.waitForTimeout(500);
  }
  console.log(`deep link #comment=${cid}: ${samples.join(" ")}`);
  await ctx.close();
}
await browser.close();
