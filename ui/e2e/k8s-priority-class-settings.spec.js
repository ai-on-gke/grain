import { test, expect } from "./fixtures.js";

// The three Kubernetes priority class boxes on Settings -> Sandbox, against
// the real demo server: they come up holding the classes deploy/kubernetes/
// ships, sit in the Kubernetes card's own column like the boxes around
// them, and an emptied one is saved as "" -- no priority class -- rather
// than snapping back to the default.

async function stored(page, key) {
  const settings = await page.evaluate(async () =>
    (await fetch("/api/settings")).json(),
  );
  return settings[key];
}

test("shows, aligns and clears the sandbox pod priority classes", async ({
  page,
}) => {
  await page.goto("/settings/sandboxes#kubernetes");
  const boxes = {
    k8sPriorityClassInteractive: [
      "Priority class for interactive sessions",
      "grain-interactive",
    ],
    k8sPriorityClassAgent: ["Priority class for agent runs", "grain-agent"],
    k8sPriorityClassCi: ["Priority class for CI jobs", "grain-ci"],
  };
  const account = await page
    .getByLabel("Kubernetes sandbox ServiceAccount")
    .boundingBox();
  for (const [, [label, value]] of Object.entries(boxes)) {
    const box = page.getByLabel(label, { exact: true });
    await expect(box).toBeVisible();
    await expect(box).toHaveValue(value);
    const bb = await box.boundingBox();
    expect(Math.abs(bb.x - account.x)).toBeLessThan(1);
    expect(Math.abs(bb.width - account.width)).toBeLessThan(1);
  }

  // Each box's hint says what a class the cluster lacks does, and wraps
  // inside the box's own column at desktop and phone widths alike.
  for (const viewport of [null, { width: 390, height: 844 }]) {
    if (viewport) await page.setViewportSize(viewport);
    const hints = page.getByText(
      "a class the cluster lacks is created if it is grain's own, or else left off the pods with an alert",
      { exact: false },
    );
    await expect(hints).toHaveCount(3);
    for (const hint of await hints.all()) {
      const hb = await hint.boundingBox();
      const field = await hint.evaluate((e) => {
        const r = e.closest(".MuiFormControl-root").getBoundingClientRect();
        return { x: r.x, right: r.right };
      });
      const vw = page.viewportSize().width;
      expect(hb.x).toBeGreaterThanOrEqual(field.x - 1);
      expect(hb.x + hb.width).toBeLessThanOrEqual(field.right + 1);
      expect(field.right).toBeLessThanOrEqual(vw);
    }
  }

  const interactive = page.getByLabel(
    "Priority class for interactive sessions",
    {
      exact: true,
    },
  );
  const ci = page.getByLabel("Priority class for CI jobs", { exact: true });
  try {
    await interactive.fill("custom-interactive");
    const savedCustom = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await interactive.press("Tab");
    expect((await savedCustom).status()).toBe(200);

    await ci.fill("");
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await ci.press("Tab");
    expect((await saved).status()).toBe(200);
    await expect.poll(() => stored(page, "k8sPriorityClassCi")).toBe("");

    await page.reload();
    await expect(interactive).toHaveValue("custom-interactive");
    await expect(ci).toHaveValue("");
  } finally {
    // The demo server is shared by this worker's other tests.
    await page.evaluate(async () =>
      fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          k8sPriorityClassInteractive: "grain-interactive",
          k8sPriorityClassCi: "grain-ci",
        }),
      }),
    );
  }
  await expect.poll(() => stored(page, "k8sPriorityClassCi")).toBe("grain-ci");
});
