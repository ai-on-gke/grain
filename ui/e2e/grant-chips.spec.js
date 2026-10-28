import { expect, test } from "./fixtures.js";

// A task's capability grants as chips on the task list (GrantChip.jsx):
// a server grant stored as a long <ref>=<json> string reads short --
// "server › method" and only its non-default arguments, cut -- with the
// whole grant in a tooltip on hover and keyboard focus, and at phone
// width the chips wrap inside the row rather than pushing it sideways.

const SERVER = "https://capabilities.demo.grain.example/cloud";
const GRANT = `${SERVER}#cloud-key=${JSON.stringify({
  account: "staging",
  project: "acme-long-project-name-wrap-xy",
})}`;

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`grant chips read short and wrap inside the task row (${label})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const title = `E2E grant chips ${label} ${Date.now()}`;
    const created = await request.post("/api/tasks", {
      data: {
        title,
        repo: "acme/widgets",
        capabilities: ["api:ci:read", GRANT],
      },
    });
    expect(created.ok()).toBeTruthy();
    const task = await created.json();
    const stored = (task.capabilities || []).find((c) =>
      c.startsWith(`${SERVER}#cloud-key`),
    );
    expect(stored).toBeTruthy();

    await page.goto("/");
    const row = page.locator(".task-row", { hasText: title });
    await row.scrollIntoViewIfNeeded();
    const chip = row.locator(`[data-grant-chip="method"]`);
    await expect(chip).toHaveCount(1);
    await expect(chip).toContainText(
      "capabilities.demo.grain.example › cloud-key",
    );
    // The stored form is never the label.
    await expect(row).not.toContainText(SERVER);
    await expect(row).not.toContainText("{");

    // Every chip stays inside the row, and the row inside the page.
    const rowBox = await row.boundingBox();
    expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(size.width + 0.5);
    for (const c of await row.locator("[data-grant-chip]").all()) {
      const b = await c.boundingBox();
      expect(b.x).toBeGreaterThanOrEqual(rowBox.x - 0.5);
      expect(b.x + b.width).toBeLessThanOrEqual(rowBox.x + rowBox.width + 0.5);
    }
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(size.width);

    // The tooltip has the whole grant and describes the chip.
    await chip.hover();
    const tip = page.getByRole("tooltip");
    await expect(tip).toContainText(SERVER);
    await expect(tip).toContainText("method: cloud-key");
    await expect(tip).toContainText("account: staging");
    const tipId = await tip.getAttribute("id");
    await expect(chip).toHaveAttribute("aria-describedby", tipId);
  });
}
