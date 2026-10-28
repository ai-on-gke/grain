import { expect, test } from "./fixtures.js";

// A /system/health loaded straight into the address bar -- a reload, a
// shared link, a walkthrough's own step -- opens Health on a phone as it
// does on a desktop. On a phone the bare /system is the list of tabs,
// and the path used to be folded into it, so the link landed on the
// list (found in the 2026-10-26 liveness / force-merge / red-base
// walkthrough, docs/test-runs/2026-10-26-liveness-force-merge-alerts-
// stacked-base.md). A desktop still canonicalises it to /system.

test("a phone opens /system/health on Health, not on the list of tabs", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const reload of [false, true]) {
    await page.goto("/system/health");
    if (reload) await page.reload();
    await expect(page).toHaveURL(/\/system\/health$/);
    await expect(page.getByTestId("system-health")).toBeVisible();
    await expect(page.getByRole("list", { name: "System tabs" })).toHaveCount(
      0,
    );
  }

  // The bare path is still the list, and its Health entry still opens
  // the same page.
  await page.goto("/system");
  await expect(page.getByRole("list", { name: "System tabs" })).toBeVisible();
  await page.getByRole("button", { name: "Health" }).click();
  await expect(page).toHaveURL(/\/system\/health$/);
  await expect(page.getByTestId("system-health")).toBeVisible();
});

test("a desktop still folds /system/health into /system", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/system/health");
  await expect(page).toHaveURL(/\/system$/);
  await expect(page.getByTestId("system-health")).toBeVisible();
});
