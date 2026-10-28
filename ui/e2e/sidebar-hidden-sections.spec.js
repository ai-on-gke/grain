import { expect, test } from "./fixtures.js";

// expectSwitchesInside checks each section's switch sits wholly inside
// the overlay, beside its own label, and that the overlay never scrolls
// sideways.
async function expectSwitchesInside(dialog) {
  const paper = dialog;
  const box = await paper.boundingBox();
  const switches = dialog.getByRole("switch");
  await expect(switches).toHaveCount(8);
  for (const sw of await switches.all()) {
    const row = sw.locator("xpath=ancestor::li[1]");
    const [s, r] = await Promise.all([
      sw
        .locator("xpath=ancestor::span[contains(@class,'MuiSwitch-root')][1]")
        .boundingBox(),
      row.boundingBox(),
    ]);
    expect(s.x).toBeGreaterThanOrEqual(box.x);
    expect(s.x + s.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    // Vertically centred on its row.
    expect(Math.abs(s.y + s.height / 2 - (r.y + r.height / 2))).toBeLessThan(3);
  }
  const overflow = await paper.evaluate(
    (el) => el.scrollWidth - el.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

// Customize sidebar's section toggles (ui/src/sidebarSections.js): a
// section hidden in this browser stays hidden across a reload, in the
// expanded panel and the folded rail alike, while its route still opens
// by URL.
test("hiding Boards in Customize sidebar survives a reload and leaves /boards reachable", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.evaluate(() => localStorage.removeItem("grain.sidebar.hidden"));
  try {
    const aside = page.locator("aside");
    await expect(aside.getByRole("button", { name: /^Boards/ })).toBeVisible();

    await aside.getByRole("button", { name: "Customize sidebar" }).click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();
    await expectSwitchesInside(dialog);
    await dialog.getByRole("switch", { name: "Show Boards" }).click();
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toHaveCount(0);

    await expect(aside.getByRole("button", { name: /^Boards/ })).toHaveCount(0);
    await expect(
      aside.getByRole("button", { name: /^Sessions/ }),
    ).toBeVisible();

    await page.reload();
    await expect(
      aside.getByRole("button", { name: /^Sessions/ }),
    ).toBeVisible();
    await expect(aside.getByRole("button", { name: /^Boards/ })).toHaveCount(0);

    // Folded to the rail, it is gone too, and the control is still there.
    await aside.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(aside).toHaveAttribute("data-collapsed", "true");
    await expect(aside.getByRole("button", { name: /^Boards/ })).toHaveCount(0);
    await expect(
      aside.getByRole("button", { name: "Customize sidebar" }),
    ).toBeVisible();
    await aside.getByRole("button", { name: "Expand sidebar" }).click();

    // The route still opens by URL.
    await page.goto("/boards");
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
  } finally {
    await page.evaluate(() => {
      localStorage.removeItem("grain.sidebar.hidden");
      localStorage.removeItem("grain.sidebar.collapsed");
    });
  }
});

test("the section switches fit Customize sidebar on a 390px phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/settings/preferences");
  await page.getByRole("button", { name: "Customize sidebar" }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("heading", { name: "Customize sidebar" }),
  ).toBeVisible();
  await expectSwitchesInside(dialog);
});
