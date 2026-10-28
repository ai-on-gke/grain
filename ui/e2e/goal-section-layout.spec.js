import { expect, test } from "./fixtures.js";

// The task page's Goal section (docs/design/durable-tasks.md#DT-4): a
// goal set through PUT /api/tasks/{id}/goal is drawn with its check and
// progress, and its editor stays inside the detail column at desktop and
// phone widths. The column is 260px wide on a desktop, and two selects
// side by side, or a fieldset sized to its content, pushed the open
// editor 64px past it.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function expectInside(inner, outer, what) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a, `${what} has no box`).not.toBeNull();
  expect(a.x, `${what} starts left of its column`).toBeGreaterThanOrEqual(
    b.x - 1,
  );
  expect(a.x + a.width, `${what} runs past its column`).toBeLessThanOrEqual(
    b.x + b.width + 1,
  );
}

test("A task's goal is drawn, and edited, inside its column", async ({
  page,
}) => {
  test.setTimeout(90_000);
  // Task 9 is the demo's deferred task. The demo is worker-scoped, so the
  // goal comes off again whatever happens.
  const put = await page.request.put("/api/tasks/9/goal", {
    data: {
      kind: "base-green",
      spec: { repo: "acme/widgets", branch: "main" },
    },
  });
  expect(put.ok()).toBe(true);
  try {
    for (const viewport of [DESKTOP, PHONE]) {
      await page.setViewportSize(viewport);
      await page.goto("/tasks/9");
      const section = page.getByTestId("goal-section");
      await expect(section.getByTestId("goal-summary")).toContainText(
        "main on acme/widgets green",
      );
      await expect(section.getByTestId("goal-progress")).toContainText(
        "6 resumes or 12h, then hold",
      );
      const column = section.locator("xpath=..");
      await expectInside(
        section,
        column,
        `goal section at ${viewport.width}px`,
      );

      await section.getByRole("button", { name: "Edit goal" }).click();
      await expect(section.getByTestId("goal-editor")).toBeVisible();
      await expectInside(
        section,
        column,
        `open goal editor at ${viewport.width}px`,
      );
      for (const field of await section.locator(".MuiTextField-root").all()) {
        await expectInside(
          field,
          section,
          `a goal field at ${viewport.width}px`,
        );
      }
      const save = section.getByRole("button", { name: "Save goal" });
      const cancel = section.getByRole("button", { name: "Cancel" });
      // The two actions share a row.
      expect(Math.round((await save.boundingBox()).y)).toBe(
        Math.round((await cancel.boundingBox()).y),
      );
      await cancel.click();
    }
  } finally {
    await page.request.delete("/api/tasks/9/goal");
  }
});
