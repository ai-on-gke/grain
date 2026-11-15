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

import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

// /next/agents with a full grid (task 5977): fourteen or more running
// agents, the inspector beside them at 30/70, 42/58 and 50/50, the
// Director docked and not, at 1280, 1920 and 2285 wide. The list pane has
// one scroller -- the grid body, or in a narrow or short pane the wrapper
// that scrolls header and grid together -- and wheeling it to the end
// brings the last card wholly into view above the status footer and left
// of the inspector. The pane around it never scrolls itself (no second
// scroller to chain into), and no card sits under the docked Director's
// composer. `grain demo` seeds one
// running task or two; the rest are copies of it served in place of GET
// /api/tasks, since only their count and layout matter here.

test.use({ demoArgs: ["-as", "demo-operator"] });

const EXTRA = 13;

async function apiJson(baseURL, code, p) {
  const res = await fetch(`${baseURL}${p}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status} ${await res.text()}`);
  return res.json();
}

function shotsDir() {
  const dir = process.env.GRAIN_E2E_SHOTS || test.info().outputPath("shots");
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

// seedAgents answers GET /api/tasks with the demo's list plus EXTRA
// copies of its running task. The feed asks with If-None-Match; that is
// dropped so the server always sends a body to add the copies to.
async function seedAgents(page, running) {
  await page.route(/\/api\/tasks(\?.*)?$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const body = await res.json();
    const list = Array.isArray(body) ? body : body.tasks;
    for (let i = 1; i <= EXTRA; i++) {
      list.push({
        ...running,
        id: String(90000 + i),
        title: `${running.title} (copy ${i})`,
      });
    }
    await route.fulfill({ response: res, json: body });
  });
}

// geometry reads the boxes the assertions compare, in viewport pixels.
// The scroller is the grid body when the pane has room for a fixed
// header, else the wrapper that scrolls header and grid together; never
// both.
async function geometry(page) {
  return page.evaluate(() => {
    const box = (el) => (el ? el.getBoundingClientRect().toJSON() : null);
    const scrolls = (el) =>
      ["auto", "scroll"].includes(getComputedStyle(el).overflowY);
    const body = document.querySelector(".agents-body");
    const wrap = document.querySelector('[data-testid="agents-scroll"]');
    const pane = document.querySelector(".agents-list-pane");
    const scroller = scrolls(body) ? body : wrap;
    const cards = [...document.querySelectorAll(".agents-body .agent-card")];
    const drawer = document.querySelector('[data-testid="director-drawer"]');
    const composer = drawer?.querySelector("textarea, [contenteditable]");
    return {
      scrollers: [body, wrap, pane].filter(scrolls).map((e) => e.className),
      which: scroller === body ? "body" : "wrap",
      scroller: box(scroller),
      scroll: {
        top: scroller.scrollTop,
        height: scroller.scrollHeight,
        client: scroller.clientHeight,
      },
      pane: box(pane),
      paneScroll: {
        top: pane.scrollTop,
        height: pane.scrollHeight,
        client: pane.clientHeight,
      },
      inspector: box(document.querySelector('[data-testid="agents-pane"]')),
      footer: box(document.querySelector(".bottom-bar")),
      drawer: box(drawer),
      composer: box(composer),
      cards: cards.map(box),
    };
  });
}

function overlaps(a, b) {
  return (
    a.left < b.right - 0.5 &&
    b.left < a.right - 0.5 &&
    a.top < b.bottom - 0.5 &&
    b.top < a.bottom - 0.5
  );
}

const WIDTHS = [
  { width: 1280, height: 800 },
  { width: 1920, height: 1080 },
  { width: 2285, height: 1084 },
];
const SPLITS = [0.3, 0.42, 0.5];

test("Agents: a full grid has one scroller, the last card is reachable, and nothing hides under the footer or the docked Director", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(240_000);
  const tasks = await apiJson(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : tasks.tasks;
  const running = list.find((t) => t.state === "running" && !t.consoleSession);
  expect(running, "grain demo seeds a running task").toBeTruthy();
  await seedAgents(page, running);
  await page.addInitScript(() =>
    localStorage.setItem("grain.themeMode", "light"),
  );
  const dir = shotsDir();

  for (const vp of WIDTHS) {
    for (const split of SPLITS) {
      for (const director of [false, true]) {
        const label = `${vp.width} split ${Math.round(split * 100)} director ${director ? "docked" : "closed"}`;
        await page.setViewportSize(vp);
        await page.addInitScript(
          (r) => localStorage.setItem("grain.agents.splitRatio", String(r)),
          split,
        );
        await page.goto(`agents?task=${running.id}`);
        await expect
          .poll(() => page.locator(".agents-body .agent-card").count())
          .toBeGreaterThan(EXTRA);
        await expect(page.getByTestId("agents-pane")).toContainText(
          `Inspecting #${running.id}`,
        );
        if (director) {
          await page.getByTestId("director-trigger").click();
          await expect(page.getByTestId("director-drawer")).toBeVisible();
          await expect(
            page.getByTestId("director-drawer").locator("textarea"),
          ).toBeVisible();
        }
        // Let the split measure its stage and the drawer settle.
        await page.waitForTimeout(300);

        // Exactly one scroller, and it overflows: there is somewhere to go.
        const first = await geometry(page);
        expect(first.scrollers, `${label}: one scroller`).toHaveLength(1);
        expect(
          first.scroll.height,
          `${label}: the cards overflow the scroller`,
        ).toBeGreaterThan(first.scroll.client);

        // Wheel over the grid well past its end, as a person would.
        const firstCard = first.cards[0];
        await page.mouse.move(
          firstCard.left + firstCard.width / 2,
          Math.min(firstCard.top + 20, first.scroller.bottom - 20),
        );
        for (let i = 0; i < 12; i++) await page.mouse.wheel(0, 1200);
        await expect
          .poll(
            async () => {
              const g = await geometry(page);
              return g.scroll.top + g.scroll.client;
            },
            { message: `${label}: the scroller reaches its end` },
          )
          .toBeGreaterThanOrEqual(first.scroll.height - 1);
        const g = await geometry(page);

        // The list pane is not a second scroller around the grid.
        expect(
          g.paneScroll.height,
          `${label}: list pane has no overflow of its own`,
        ).toBeLessThanOrEqual(g.paneScroll.client + 1);
        expect(g.paneScroll.top, `${label}: list pane unscrolled`).toBe(0);

        // The scroller ends above the status footer, left of the inspector.
        expect(g.scroller.bottom, label).toBeLessThanOrEqual(
          g.footer.top + 0.5,
        );
        expect(g.pane.right, label).toBeLessThanOrEqual(g.inspector.left + 1);

        // The last card is wholly inside the scroller, so wholly in view.
        const last = g.cards[g.cards.length - 1];
        expect(
          last.top,
          `${label} (${g.which}): last card top`,
        ).toBeGreaterThanOrEqual(g.scroller.top - 0.5);
        expect(
          last.bottom,
          `${label} (${g.which}): last card bottom`,
        ).toBeLessThanOrEqual(g.scroller.bottom + 0.5);
        expect(last.bottom, label).toBeLessThanOrEqual(g.footer.top);
        expect(last.right, label).toBeLessThanOrEqual(g.inspector.left + 1);

        if (director) {
          expect(g.drawer, label).not.toBeNull();
          expect(g.composer, label).not.toBeNull();
          expect(
            g.inspector.right,
            `${label}: the split ends where the drawer starts`,
          ).toBeLessThanOrEqual(g.drawer.left + 1);
          for (const c of g.cards) {
            expect(
              overlaps(c, g.composer),
              `${label}: a card under the composer`,
            ).toBe(false);
          }
        }
        if (vp.width === 2285 && split === 0.3) {
          await page.screenshot({
            path: path.join(
              dir,
              `agents-grid-2285-30-${director ? "director" : "no-director"}.png`,
            ),
          });
        }
        if (director) {
          await page.getByTestId("director-drawer-close").click();
          await expect(page.getByTestId("director-drawer")).toHaveCount(0);
        }
      }
    }
  }
});

test("Agents: on a phone, with no inspector, the grid's last card scrolls clear of the bottom bar", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const tasks = await apiJson(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : tasks.tasks;
  const running = list.find((t) => t.state === "running" && !t.consoleSession);
  expect(running, "grain demo seeds a running task").toBeTruthy();
  await seedAgents(page, running);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("agents");
  await expect
    .poll(() => page.locator(".agents-body .agent-card").count())
    .toBeGreaterThan(EXTRA);
  await expect(page.getByTestId("agents-pane")).toHaveCount(0);
  const before = await geometry(page);
  expect(before.scrollers).toHaveLength(1);
  await page.evaluate(() => {
    const el = [
      document.querySelector(".agents-body"),
      document.querySelector('[data-testid="agents-scroll"]'),
    ].find((e) => getComputedStyle(e).overflowY === "auto");
    el.scrollTop = el.scrollHeight;
  });
  const g = await geometry(page);
  expect(g.paneScroll.height).toBeLessThanOrEqual(g.paneScroll.client + 1);
  const nav = await page
    .locator(".compact-nav")
    .evaluate((el) => el.getBoundingClientRect().toJSON());
  const last = g.cards[g.cards.length - 1];
  expect(last.bottom).toBeLessThanOrEqual(Math.min(g.scroller.bottom, nav.top));
  expect(last.top).toBeGreaterThanOrEqual(g.scroller.top - 0.5);
});
