import { test, expect } from "./fixtures.js";

// The other half of install-notice.spec.js: a deployment that is not an
// unmigrated host-mode install -- the default `grain demo`, as a
// container install or a development daemon is -- shows no install
// notice anywhere.
test("any other deployment shows no install notice", async ({ page }) => {
  await page.goto("/system/health");
  await expect(page.getByTestId("system-health")).toBeVisible();
  await expect(page.getByTestId("install-notice-banner")).toHaveCount(0);
  await expect(page.getByTestId("health-install-notice")).toHaveCount(0);
  const config = await page.evaluate(async () =>
    (await fetch("/api/config")).json(),
  );
  expect(config.installNotice).toBeUndefined();
});
