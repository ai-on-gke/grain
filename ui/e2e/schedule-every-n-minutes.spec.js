import { test, expect } from "./fixtures.js";

// "Every N minutes" end to end (docs/test-plans/2026-10-25.md §5): made
// in the schedule editor, listed on /processes as "every 15m", opened
// again on the same cadence and number rather than another, and held to
// the ten-minute floor (model.MinEveryNMinutes) before the form sends
// anything.

const minutes = (page) => page.locator('input[name="everyNMinutes"]');

test("an Every N minutes schedule saves, lists and reopens as itself", async ({
  page,
}) => {
  const title = `Every fifteen ${Date.now()}`;
  let id = null;
  const saves = [];
  page.on("request", (r) => {
    const path = new URL(r.url()).pathname;
    if (r.method() === "POST" && path === "/api/schedules") saves.push(r);
  });
  try {
    await page.goto("/processes?kind=schedule");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByLabel("Repeat").click();
    await page.getByRole("option", { name: "Every N minutes" }).click();
    await expect(minutes(page)).toHaveValue("30");
    await page.locator('input[name="title"]').fill(title);
    await page
      .locator('textarea[name="description"]')
      .fill("Check the widgets.");

    // Under the floor, below zero, blank or fractional: the browser
    // refuses the form, so no schedule is ever sent.
    const save = page.getByRole("button", { name: "Add schedule" });
    for (const bad of ["0", "-5", "1", "9", "", "15.5"]) {
      await minutes(page).fill(bad);
      expect(
        await minutes(page).evaluate((e) => e.validity.valid),
        `${JSON.stringify(bad)} is refused`,
      ).toBe(false);
      await save.click();
      await expect(minutes(page)).toBeVisible();
    }
    expect(saves).toHaveLength(0);
    for (const ok of ["10", "59", "60", "90"]) {
      await minutes(page).fill(ok);
      expect(await minutes(page).evaluate((e) => e.validity.valid), ok).toBe(
        true,
      );
    }

    await minutes(page).fill("15");
    await save.click();
    await expect(minutes(page)).toHaveCount(0);
    expect(saves).toHaveLength(1);
    const res = await page.request.get(
      `/api/schedules?q=${encodeURIComponent(title)}`,
    );
    const [saved] = await res.json();
    id = saved.id;
    expect(saved.recurrence).toMatchObject({
      kind: "everyNMinutes",
      everyNMinutes: 15,
    });

    const row = page.locator(".process-row", { hasText: title });
    await expect(row).toContainText("every 15m");
    await expect(row).toContainText("Enabled");

    await page.goto(`/processes/schedules/${id}`);
    await expect(page.getByLabel("Repeat")).toHaveText("Every N minutes");
    await expect(minutes(page)).toHaveValue("15");
    await minutes(page).fill("45");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(minutes(page)).toHaveCount(0);
    const after = await (
      await page.request.get(`/api/schedules?q=${encodeURIComponent(title)}`)
    ).json();
    expect(after[0].recurrence).toMatchObject({
      kind: "everyNMinutes",
      everyNMinutes: 45,
    });
  } finally {
    if (id) await page.request.delete(`/api/schedules/${id}`);
  }
});

test("the API holds Every N minutes to its floor and ceiling", async ({
  page,
}) => {
  for (const [n, ok] of [
    [9, false],
    [0, false],
    [-5, false],
    [10, true],
    [527040, true],
    [527041, false],
  ]) {
    const res = await page.request.post("/api/schedules", {
      data: {
        title: `floor ${n}`,
        repo: "acme/widgets",
        recurrence: { kind: "everyNMinutes", everyNMinutes: n },
      },
    });
    expect(res.ok(), `${n}: ${await res.text()}`).toBe(ok);
    if (res.ok())
      await page.request.delete(`/api/schedules/${(await res.json()).id}`);
  }
});
