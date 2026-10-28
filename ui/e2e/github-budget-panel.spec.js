import { expect, test } from "./fixtures.js";

// System → Health's GitHub budget panel (docs/github-api-rate-limits.md
// §4), over the demo's fixed hour (pkg/demo's demoGitHubCalls): the
// projection is said, the callers are listed, and nothing in it pushes
// a phone's page sideways.
for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the GitHub budget panel on Health fits a ${name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/system");
    // A phone opens System on its list of tabs; Health is one tap in.
    if (name === "phone") {
      await page
        .getByRole("list", { name: "System tabs" })
        .getByRole("button", { name: "Health" })
        .click();
    }
    const panel = page.getByTestId("health-github-budget");
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("1,650 of 5,000 calls left this hour");
    await expect(page.getByTestId("health-github-runs-out")).toContainText(
      "runs out in",
    );
    const callers = page.getByTestId("health-github-callers");
    await expect(callers).toContainText("sync");
    await expect(callers).toContainText("review-threads");

    const box = await panel.boundingBox();
    const tableBox = await callers.boundingBox();
    expect(tableBox.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(tableBox.x + tableBox.width).toBeLessThanOrEqual(
      box.x + box.width + 1,
    );
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);
  });
}
