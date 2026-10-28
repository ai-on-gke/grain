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
