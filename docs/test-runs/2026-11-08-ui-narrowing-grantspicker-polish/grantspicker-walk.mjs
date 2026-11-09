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

// 2026-11-08 §7 item 2: multi-layer Escape containment and focus
// restoration for GrantsPicker (Add grant…) in every host surface -- New
// Task, Template and Schedule overlays (dirty) and the Task / Console
// Session side panels (clean) -- over `grain demo`. Usage:
//   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=... node grantspicker-walk.mjs [label]
import { chromium, AxeBuilder, VIEWPORTS, THEMES, recorder, signedIn, out, tag } from "./lib.mjs";

const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const rec = recorder(label);
const FAST = process.env.FAST === "1";

// Each surface: how to open it, dirty it (or not), and find its grants.
const SURFACES = [
  {
    name: "new-task",
    url: "/new",
    ready: (p) => p.getByLabel("Title", { exact: true }).first(),
    dirty: true,
    // New Task keeps a draft rather than asking to discard (#933).
    draft: true,
    grants: (p) => p.locator("body"),
  },
  {
    name: "template",
    url: "/processes/templates/template-2",
    ready: (p) => p.getByRole("heading", { name: "Edit template" }),
    dirty: true,
    field: "Name",
    grants: (p) => p.getByTestId("template-grants"),
  },
  {
    name: "schedule",
    url: "/processes/schedules/sched-2",
    ready: (p) => p.getByRole("heading", { name: "Edit schedule" }),
    dirty: true,
    grants: (p) => p.getByTestId("schedule-grants"),
  },
  {
    name: "task-panel",
    url: "/tasks/2",
    ready: (p) => p.getByTestId("task-2-grants"),
    dirty: false,
    grants: (p) => p.getByTestId("task-2-grants"),
  },
  {
    name: "session-panel",
    url: "/sessions",
    open: async (p) => {
      await p.getByRole("button", { name: /^Open session Director/ }).first().click();
      await p.getByRole("button", { name: "Edit Director capabilities" }).click();
    },
    ready: (p) => p.getByRole("button", { name: "Add grant…" }).first(),
    dirty: false,
    grants: (p) => p.locator("body"),
  },
];

const browser = await chromium.launch();

const menuOf = (p) => p.getByRole("menu", { name: "Grants to add" });
// gone waits out the Popover's exit transition (up to 2 s) rather than
// reading a menu that is on its way out as still open.
async function gone(p, loc) {
  for (let i = 0; i < 40; i++) {
    if ((await loc.count()) === 0) return true;
    await p.waitForTimeout(50);
  }
  return false;
}
const focusedIsAdd = (p) =>
  p.evaluate(() => (document.activeElement?.textContent || "").trim() === "Add grant…");
const paneState = (p) =>
  p.evaluate(() => ({
    url: location.pathname,
    dialogs: document.querySelectorAll('[role="dialog"], [role="presentation"]').length,
  }));

async function walk(vp, theme, s) {
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  const dialogs = [];
  page.on("dialog", async (d) => {
    if (d.type() === "beforeunload") return d.accept();
    dialogs.push(d.message());
    await d.dismiss();
  });
  const t = `${s.name} ${tag(vp, theme)}`;
  try {
    await page.goto(s.url);
    if (s.open) await s.open(page);
    await s.ready(page).waitFor({ timeout: 20000 });
    if (s.dirty) {
      const title = page.getByLabel(s.field || "Title", { exact: true }).first();
      await title.fill(`${await title.inputValue()} (walk edit)`);
    }
    const scope = s.grants(page);
    const add = scope.getByRole("button", { name: "Add grant…" }).first();
    await add.scrollIntoViewIfNeeded();
    const urlBefore = page.url();

    // Escape #1 with the nested repository SearchSelect open.
    await add.click();
    await menuOf(page).waitFor({ timeout: 10000 });
    const repoSelect = page.getByLabel("Grain API grants apply to");
    let nested = false;
    if (await repoSelect.count()) {
      await repoSelect.click();
      nested = await page.getByRole("listbox").isVisible().catch(() => false);
    }
    rec.check(`${t}: nested repo SearchSelect opens`, nested);
    await page.keyboard.press("Escape");
    await page.waitForTimeout(250);
    rec.check(`${t}: Escape #1 closes only the SearchSelect`,
      (await page.getByRole("listbox").count()) === 0 && (await menuOf(page).isVisible()) && dialogs.length === 0, {
        listbox: await page.getByRole("listbox").count(),
        menu: await menuOf(page).isVisible(),
        dialogs: [...dialogs],
      });

    // Escape #2 closes only GrantsPicker and restores focus to Add grant….
    await page.keyboard.press("Escape");
    await gone(page, menuOf(page));
    rec.check(`${t}: Escape #2 closes only GrantsPicker, focus on Add grant…`,
      (await menuOf(page).count()) === 0 && (await focusedIsAdd(page)) && dialogs.length === 0 && page.url() === urlBefore, {
        menu: await menuOf(page).count(),
        focused: await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 80)),
        dialogs: [...dialogs],
        url: page.url(),
      });

    // A disabled policy / CI-only row's link focused.
    await add.click();
    await menuOf(page).waitFor({ timeout: 10000 });
    const link = menuOf(page).getByRole("link").first();
    const hasLink = (await link.count()) > 0;
    if (hasLink) {
      await link.focus();
    }
    await page.keyboard.press("Escape");
    await gone(page, menuOf(page));
    rec.check(`${t}: Escape on a disabled row's link closes only GrantsPicker`,
      (await menuOf(page).count()) === 0 && (await focusedIsAdd(page)) && dialogs.length === 0 && page.url() === urlBefore, {
        hasLink,
        dialogs: [...dialogs],
      });

    // Escape within one frame of opening Add grant…: the keydown is the
    // very next task after the click, before any animation frame (so
    // before the popover has painted). The gap includes React's render.
    const gap = await page.evaluate(async () => {
      const btn = [...document.querySelectorAll("button")].find(
        (b) => b.textContent.trim() === "Add grant…" && b.offsetParent !== null,
      );
      let frames = 0;
      const count = () => {
        frames++;
      };
      btn.focus();
      const t0 = performance.now();
      requestAnimationFrame(count);
      btn.click();
      await new Promise((r) => setTimeout(r, 0));
      const at = performance.now() - t0;
      const ev = new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true, cancelable: true });
      (document.activeElement || document.body).dispatchEvent(ev);
      return { at, framesBefore: frames };
    });
    await gone(page, menuOf(page));
    await page.waitForTimeout(100);
    // The assertion is the behaviour; whether the keydown really beat the
    // first frame (framesBefore 0) depends on how long React's render
    // took on a loaded machine, and is recorded beside it.
    rec.check(`${t}: Escape in the next task after opening closes only GrantsPicker`,
      (await menuOf(page).count()) === 0 && dialogs.length === 0 && page.url() === urlBefore, {
        gapMs: Number(gap.at.toFixed(1)),
        framesBefore: gap.framesBefore,
        menu: await menuOf(page).count(),
        focusedAdd: await focusedIsAdd(page),
        dialogs: [...dialogs],
      });
    // Same through real key input, as fast as Playwright can type it.
    await add.click();
    await page.keyboard.press("Escape");
    await gone(page, menuOf(page));
    rec.check(`${t}: immediate real Escape closes only GrantsPicker`,
      (await menuOf(page).count()) === 0 && dialogs.length === 0 && page.url() === urlBefore && (await focusedIsAdd(page)), {
        dialogs: [...dialogs],
      });
    if (theme === "light" || vp.width === 390) {
      await add.click();
      await menuOf(page).waitFor();
      await page.screenshot({ path: `${out}grants-${s.name}-${tag(vp, theme)}.png` });
      if (theme === "light" && vp.width === 1440) {
        const axe = await new AxeBuilder({ page }).include('[role="menu"]').withTags(["wcag2a", "wcag2aa"]).disableRules(["color-contrast"]).analyze();
        rec.check(`${t}: axe Add grant… menu`, axe.violations.length === 0, { violations: axe.violations.map((v) => `${v.id}:${v.nodes.length}`) });
      }
      await page.keyboard.press("Escape");
      await gone(page, menuOf(page));
    }

    // Escape #3: the dirty overlay asks DISCARD_PROMPT; a clean panel closes.
    const before = await paneState(page);
    await page.keyboard.press("Escape");
    await page.waitForTimeout(600);
    const after = await paneState(page);
    if (s.draft) {
      await page.goto(s.url);
      const kept = await page.getByLabel("Title", { exact: true }).first().inputValue().catch(() => "");
      rec.check(`${t}: Escape #3 closes New Task and keeps the draft`, dialogs.length === 0 &&
        after.url !== before.url && /walk edit/.test(kept), { before, after, kept, dialogs: [...dialogs] });
    } else if (s.dirty) {
      rec.check(`${t}: Escape #3 asks DISCARD_PROMPT`, dialogs.length === 1 && /unsaved changes/.test(dialogs[0] || ""), {
        dialogs: [...dialogs],
      });
    } else {
      rec.check(`${t}: Escape #3 closes the clean panel`, dialogs.length === 0 &&
        (after.url !== before.url || after.dialogs < before.dialogs ||
          (await scope.getByRole("button", { name: "Add grant…" }).count()) === 0), { before, after, dialogs: [...dialogs] });
    }
  } catch (err) {
    rec.check(`${t}: walk ran`, false, { error: String(err).slice(0, 300) });
    await page.screenshot({ path: `${out}grants-error-${s.name}-${tag(vp, theme)}.png` }).catch(() => {});
  } finally {
    await ctx.close();
  }
}

const only = process.env.ONLY || "";
for (const vp of FAST ? VIEWPORTS.slice(0, 1) : VIEWPORTS) {
  for (const theme of FAST ? THEMES.slice(0, 1) : THEMES) {
    for (const s of SURFACES) if (!only || only === s.name) await walk(vp, theme, s);
  }
}
await rec.save(`grantspicker-${label}.json`);
await browser.close();
console.log(`failures: ${rec.result.failures.length}`, rec.result.failures);
