import { test, expect } from "./fixtures.js";

// The -observability demo (cmd/grain/demo_observability.go) is the one
// with a sandbox host, so it is the one whose Sandboxes page renders the
// storage section these pointers sit in.
test.use({ demoArgs: ["-observability"] });

// Found by the 2026-10-05 walkthrough
// (docs/test-runs/2026-10-05-deployment-operations.md): the Sandboxes
// page sent CI history to "Settings → General", a pane and tab that do
// not hold it. Every pointer now names the Settings section that does
// (docs/design/settings-panel.md split Engine & Concurrency into Engine
// and CI), and following it finds the card it promised.
test("sends CI history to the Settings section that holds it", async ({
  page,
}) => {
  await page.goto("/system/sandboxes");
  await expect(
    page.getByText(/Settings → CI, under CI\s+history/),
  ).toBeVisible();
  await expect(page.getByText(/System → (?!Caches)/)).toHaveCount(0);

  await page.getByTestId("top-bar-settings").click();
  await page.getByTestId("settings-nav-ci").click();
  await expect(page).toHaveURL(/\/settings\/ci$/);
  for (const card of ["CI history", "CI job cache", "CI artifacts"]) {
    await expect(page.getByRole("heading", { name: card })).toBeVisible();
  }
});
