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

import { test, expect } from "./fixtures.js";

// The /processes rows' phone layout (style.css, `.process-row` under
// data-phone), measured in a real browser over `grain demo`: jsdom has
// no layout engine, so how wide the name column ends up is only
// checkable here. The 2026-10-23 regression sweep found the ≤900px grid
// leaving a template row's name, task title and repo a 110px column at
// 390px -- "release-qualification" broken as "release-" / "qualification"
// -- because the kind chip kept a column of its own beside it.
//
// The phone half asserts the name gets most of the row; the tablet and
// desktop halves assert they still draw the chip beside the name, the
// layout this change was not meant to touch.
const PHONE = { width: 390, height: 844 };
const TABLET = { width: 820, height: 1180 };
const DESKTOP = { width: 1440, height: 900 };

const TEMPLATE = "release-qualification";

function templateRow(page) {
  return page
    .locator('.process-row[data-kind="template"]')
    .filter({ has: page.getByRole("link", { name: TEMPLATE, exact: true }) });
}

async function boxes(page) {
  const row = templateRow(page);
  await expect(row).toBeVisible();
  const box = async (sel) => {
    const b = await row.locator(sel).first().boundingBox();
    expect(b, `${sel} has no box`).toBeTruthy();
    return b;
  };
  return {
    row: await box(":scope"),
    kind: await box(".process-kind .MuiChip-root"),
    name: await box(".process-name"),
    link: await box(".template-name"),
    title: await box(".template-title"),
    use: await row.getByRole("button", { name: "Use…" }).boundingBox(),
  };
}

// lineCount counts the line boxes the name link's text is laid out in:
// one per line, so a name broken mid-word is two.
async function lineCount(page) {
  return templateRow(page)
    .locator(".template-name")
    .evaluate((el) => {
      const range = document.createRange();
      range.selectNodeContents(el);
      const tops = new Set(
        [...range.getClientRects()].map((r) => Math.round(r.top)),
      );
      return tops.size;
    });
}

test.describe("Processes rows on a phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("give the name most of the row", async ({ page }) => {
    await page.goto("/processes");
    const b = await boxes(page);

    // The name column, and the task title under it, are most of the
    // screen rather than a 110px strip.
    expect(b.name.width).toBeGreaterThanOrEqual(280);
    expect(b.title.width).toBeGreaterThanOrEqual(200);
    expect(await lineCount(page)).toBe(1);

    // The kind chip is on a line of its own above the name, at the
    // name's left edge, with Use… at the end of that same line.
    expect(b.kind.y + b.kind.height).toBeLessThanOrEqual(b.link.y + 1);
    expect(Math.abs(b.kind.x - b.name.x)).toBeLessThanOrEqual(1);
    expect(b.use.y + b.use.height).toBeLessThanOrEqual(b.link.y + 1);
    expect(b.use.x + b.use.width).toBeLessThanOrEqual(PHONE.width);
    expect(b.use.x).toBeGreaterThan(PHONE.width / 2);

    // The phone gutter, not the desktop's 1.75rem.
    expect(b.kind.x - b.row.x).toBeLessThanOrEqual(16 + 20 + 12 + 1);

    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(PHONE.width);
  });
});

// The 2026-11-02 walkthrough: a grant chip is nowrap, and the preview's
// value column kept min-width:auto, so "api:ci-read@acme/widgets" and
// its "from the template" chip pushed the Capabilities row 55px past the
// pane at 390px and the pane scrolled sideways. The chip ellipsizes
// inside its column now; its tooltip has the whole grant.
test.describe("A template's Use… preview on a phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("keeps a long grant inside its row", async ({ page }) => {
    const name = `phone-grant-${Date.now()}`;
    const created = await page.request.post("/api/templates", {
      data: {
        name,
        title: "Read the CI",
        repo: "acme/widgets",
        capabilities: ["api:ci-read@acme/widgets"],
      },
    });
    expect(created.ok()).toBe(true);
    const { id } = await created.json();
    try {
      await page.goto("/processes?kind=template");
      await page
        .locator('.process-row[data-kind="template"]')
        .filter({ hasText: name })
        .getByRole("button", { name: "Use…" })
        .click();
      const grant = page.locator("dd [data-grant-chip]").first();
      await expect(grant).toBeVisible();

      const row = grant.locator("xpath=ancestor::dd/..");
      const widths = await row.evaluate((el) => ({
        scroll: el.scrollWidth,
        client: el.clientWidth,
      }));
      expect(widths.scroll).toBeLessThanOrEqual(widths.client);
      const rowBox = await row.boundingBox();
      const source = await row
        .locator(":scope > dd > .MuiChip-root")
        .boundingBox();
      expect(source.x + source.width).toBeLessThanOrEqual(
        rowBox.x + rowBox.width + 1,
      );
      // And the pane it sits in does not scroll sideways: the nearest
      // ancestor that scrolls is as wide as what it holds.
      const pane = await grant.evaluate((el) => {
        let x = el.parentElement;
        while (x && !/auto|scroll/.test(getComputedStyle(x).overflowX)) {
          x = x.parentElement;
        }
        x = x || document.documentElement;
        return { scroll: x.scrollWidth, client: x.clientWidth };
      });
      expect(pane.scroll).toBeLessThanOrEqual(pane.client);
    } finally {
      await page.request.delete(`/api/templates/${id}`);
    }
  });
});

// A template pane's footer at 390px (2026-11-08 walkthrough, task 3614):
// with no wrap and no gap, Delete sat flush against "New task from this
// template" (0px between them), which broke onto two lines and stretched
// Cancel and Save to its 61px height. Each button is one line tall now,
// and no two of them touch.
test.describe("A template pane's footer on a phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("keeps each button one line tall and apart from the next", async ({
    page,
  }) => {
    await page.goto("/processes/templates/template-1");
    const pane = page.getByRole("dialog");
    const names = ["Delete", "New task from this template", "Cancel", "Save"];
    const boxes = [];
    for (const name of names) {
      const button = pane.getByRole("button", { name, exact: true });
      await expect(button).toBeVisible();
      boxes.push({ name, ...(await button.boundingBox()) });
    }
    const paneBox = await pane.boundingBox();
    for (const b of boxes) {
      expect(b.height, `${b.name} is ${b.height}px tall`).toBeLessThan(44);
      expect(b.x).toBeGreaterThanOrEqual(paneBox.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(paneBox.x + paneBox.width + 1);
    }
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const [a, b] = [boxes[i], boxes[j]];
        const sameLine = a.y < b.y + b.height && b.y < a.y + a.height;
        if (!sameLine) continue;
        const gap = Math.max(b.x - (a.x + a.width), a.x - (b.x + b.width));
        expect(
          gap,
          `${a.name} and ${b.name} are ${gap}px apart`,
        ).toBeGreaterThanOrEqual(4);
      }
    }
    const form = pane.locator("form");
    expect(
      await form.evaluate((el) => el.scrollWidth - el.clientWidth),
    ).toBeLessThanOrEqual(1);
  });
});

for (const [label, viewport, extra] of [
  ["tablet", TABLET, { hasTouch: true, isMobile: true }],
  ["desktop", DESKTOP, {}],
]) {
  test.describe(`Processes rows on a ${label}`, () => {
    test.use({ viewport, ...extra });

    test("keep the kind chip beside the name", async ({ page }) => {
      await page.goto("/processes");
      const b = await boxes(page);

      // Same line, chip first: the layout from before the phone one.
      expect(b.kind.x + b.kind.width).toBeLessThan(b.name.x);
      expect(Math.abs(b.kind.y - b.link.y)).toBeLessThanOrEqual(8);
      expect(b.use.x).toBeGreaterThan(b.name.x + b.name.width);
      expect(await lineCount(page)).toBe(1);
    });
  });
}
