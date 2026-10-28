import { test, expect } from "./fixtures.js";

// A task's changelists (CLs) (model.LinkChangelist) sit on the task page's
// freshness line beside its pull request -- not folded under the
// provenance disclosure -- each as its cl/<number> go link. The demo
// seeds task 5 with acme/widgets#101 and cl/612345678.
for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`task page shows a task's CL beside its pull request (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/tasks/5");
    const line = page.locator(".overlay-pane .freshness").first();
    const pr = line.getByRole("link", { name: "acme/widgets#101" });
    const cl = line.getByRole("link", { name: "cl/612345678" });
    await expect(cl).toBeVisible();
    await expect(cl).toHaveAttribute("href", "http://cl/612345678");
    const prBox = await pr.boundingBox();
    const clBox = await cl.boundingBox();
    const lineBox = await line.boundingBox();
    // After the pull request on the same line, and inside the pane's line.
    expect(Math.abs(clBox.y - prBox.y)).toBeLessThan(2);
    expect(clBox.x).toBeGreaterThan(prBox.x + prBox.width);
    expect(clBox.x + clBox.width).toBeLessThanOrEqual(
      lineBox.x + lineBox.width + 1,
    );
  });
}
