import { test, expect } from "./fixtures.js";

// Found by the 2026-10-01 and 2026-10-02 deployment-operations
// walkthroughs (docs/test-runs/2026-10-0{1,2}-deployment-operations.md),
// driven through the real demo server and built frontend: the Metrics
// pane's window picker is in the address bar (grain/task-1584), so a
// reload, Back and Forward and a pasted link keep the window it was on
// rather than putting it back on the last 7 days.

const windowPicker = (page) => page.getByRole("combobox", { name: "Window" });

test("keeps the Metrics window across a reload, Back and Forward", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByTestId("top-bar-system").click();
  await page.getByRole("tab", { name: "Metrics" }).click();
  await expect(page).toHaveURL(/\/metrics$/);
  await expect(windowPicker(page)).toHaveText("Last 7 days");

  await windowPicker(page).click();
  await page.getByRole("option", { name: "Last 30 days" }).click();
  await expect(page).toHaveURL(/\/metrics\?window=30d$/);
  await expect(page.getByText(/· 30d 0h/)).toBeVisible();

  await page.reload();
  await expect(windowPicker(page)).toHaveText("Last 30 days");
  await expect(page.getByText(/· 30d 0h/)).toBeVisible();

  // Picking a window is a change of question rather than of page, so
  // Back leaves the pane rather than stepping to the 7-day report.
  await page.goBack();
  await expect(page).not.toHaveURL(/\/metrics/);
  await expect(
    page.getByRole("heading", { name: "Metrics", exact: true }),
  ).toHaveCount(0);

  await page.goForward();
  await expect(page).toHaveURL(/\/metrics\?window=30d$/);
  await expect(windowPicker(page)).toHaveText("Last 30 days");

  // The default window keeps the bare path.
  await windowPicker(page).click();
  await page.getByRole("option", { name: "Last 7 days" }).click();
  await expect(page).toHaveURL(/\/metrics$/);
});

test("opens a pasted Metrics link on its window, and an unknown one on the default", async ({
  page,
}) => {
  await page.goto("/metrics?window=90d");
  await expect(windowPicker(page)).toHaveText("Last 90 days");
  await expect(page.getByText(/· 90d 0h/)).toBeVisible();

  await page.goto("/metrics?window=fortnight");
  await expect(windowPicker(page)).toHaveText("Last 7 days");
  await expect(page.getByText(/· 7d 0h/)).toBeVisible();
  await expect(page).toHaveURL(/\/metrics$/);
});
