// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { expect, test } from "./fixtures.js";
import { chips, chooseOption } from "./searchSelect.js";

// A deployment default the capability catalogue restricts to one repo,
// on a task filed with No repo (2026-10-29 test run, Bug 3). The demo
// defaults api:self-debug, grain#source (once self-debug) and
// github#read-issues; this test adds the legacy console capability to
// those defaults. With console restricted to acme/widgets, the form used
// to post all defaults and be refused with a 400 for a grant nobody had
// picked. Now it marks console as refused
// where it is ticked, says it is left off, and files the task with the
// defaults the server keeps.

test.use({ demoArgs: ["-github-sim", "-fake-ci"] });

const RESTRICTED = {
  catalogue: {
    inProcess: { console: { restrictions: { repos: ["acme/widgets"] } } },
  },
};

const within = (inner, outer) => {
  expect(inner.x).toBeGreaterThanOrEqual(outer.x - 1);
  expect(inner.x + inner.width).toBeLessThanOrEqual(outer.x + outer.width + 1);
};

async function openStandalone(page, title) {
  await page.goto("/");
  // "+ New task" on a desktop header, "New task" on a phone's.
  await page.getByRole("button", { name: /^\+? ?New task$/ }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await page.getByLabel("Title").fill(title);
  await chooseOption(page.getByLabel(/^Repos/), "none");
  const picker = page.getByRole("combobox", { name: "Legacy capabilities" });
  const note = page.getByTestId("new-task-catalogue-refused");
  await expect(note).toHaveText(
    "settings/capabilities.yml refuses console on a task with no repo, so that default is left off this task.",
  );
  return { picker, note };
}

test("a No repo task leaves off a default the catalogue refuses there", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  const before = await (await request.get("/api/capability-policy")).json();
  const defaultsBefore =
    (await (await request.get("/api/settings")).json()).defaultCapabilities ||
    [];
  try {
    expect(
      (
        await request.put("/api/settings", {
          data: { defaultCapabilities: [...defaultsBefore, "console"] },
        })
      ).ok(),
    ).toBe(true);
    const put = await request.put("/api/capability-policy", {
      data: { policy: RESTRICTED },
    });
    expect(put.ok()).toBe(true);

    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 600 ? 1000 : 844 });
      const { picker, note } = await openStandalone(
        page,
        `E2E catalogue default ${width} ${Date.now()}`,
      );

      // Only gcp-key is marked, and it sits inside the picker; the note
      // sits inside the picker's own column, at either width.
      const refused = chips(picker).and(
        page.locator("[data-catalogue-refused]"),
      );
      await expect(refused).toHaveCount(1);
      await expect(refused).toHaveAccessibleName(/\(refused here\)$/);
      const pickerBox = await picker
        .locator("xpath=ancestor::div[contains(@class,'MuiInputBase-root')][1]")
        .boundingBox();
      within(await refused.boundingBox(), pickerBox);
      const control = picker.locator(
        "xpath=ancestor::div[contains(@class,'MuiFormControl-root')][.//*[@data-testid='new-task-catalogue-refused']][1]",
      );
      within(await note.boundingBox(), await control.boundingBox());
      const form = page.locator("form", {
        has: page.getByRole("button", { name: "Create task" }),
      });
      expect(
        await form.evaluate((el) => el.scrollWidth - el.clientWidth),
      ).toBeLessThanOrEqual(1);

      // Its option carries the catalogue's reason, and stays enabled:
      // it is ticked, and its own row is how it would come off.
      await picker.click();
      const option = page.getByRole("option", { name: /Interactive console/i });
      await expect(option).toContainText("repos: acme/widgets");
      await expect(option).not.toHaveAttribute("aria-disabled", "true");
      await page.keyboard.press("Escape");

      const dir = process.env.SHOTS_DIR;
      if (dir) {
        for (const theme of ["light", "dark"]) {
          await page.evaluate(
            (t) => localStorage.setItem("grain.themeMode", t),
            theme,
          );
          await page.emulateMedia({ colorScheme: theme });
          await page.waitForTimeout(200);
          await control.scrollIntoViewIfNeeded();
          await page.screenshot({
            path: `${dir}/new-task-no-repo-${width}-${theme}.png`,
          });
          await control.screenshot({
            path: `${dir}/new-task-no-repo-picker-${width}-${theme}.png`,
          });
          await refused.screenshot({
            path: `${dir}/new-task-no-repo-chip-${width}-${theme}.png`,
          });
        }
        await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
        await page.emulateMedia({ colorScheme: "light" });
      }

      if (width !== 1440) continue;
      // Filing leaves the untouched list to the server, which keeps the
      // defaults it may and drops console, rather than refusing the task.
      const filing = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/tasks") && r.request().method() === "POST",
      );
      await page.getByRole("button", { name: "Create task" }).click();
      const res = await filing;
      expect(res.status()).toBeLessThan(300);
      expect(JSON.parse(res.request().postData())).not.toHaveProperty(
        "capabilities",
      );
      const task = await res.json();
      const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
      expect(detail.capabilities).toEqual(
        expect.arrayContaining([
          "api:self-debug",
          "https://grain.dev/capabilities/grain#source={}",
        ]),
      );
      expect(detail.capabilities).not.toContain("console");
    }
  } finally {
    await request.put("/api/capability-policy", {
      data: { policy: before.policy || {} },
    });
    await request.put("/api/settings", {
      data: { defaultCapabilities: defaultsBefore },
    });
  }
});

// Once the picker is the person's, a refused grant still ticked is
// theirs too: the form says to untick it, and on the repo the
// catalogue grants it to, says nothing.
test("an edited picker is asked to untick a refused grant", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  const defaultsBefore =
    (await (await request.get("/api/settings")).json()).defaultCapabilities ||
    [];
  try {
    expect(
      (
        await request.put("/api/settings", {
          data: { defaultCapabilities: [...defaultsBefore, "console"] },
        })
      ).ok(),
    ).toBe(true);
    await request.put("/api/capability-policy", {
      data: { policy: RESTRICTED },
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
    const { picker, note } = await openStandalone(
      page,
      `E2E catalogue edited ${Date.now()}`,
    );
    await chooseOption(picker, "priority-dispatch");
    await page.keyboard.press("Escape");
    await expect(note).toHaveText(
      "settings/capabilities.yml refuses console on a task with no repo: untick it, or the task will not be filed.",
    );
    await chooseOption(page.getByLabel(/^Repos/), "acme/widgets");
    await expect(note).toHaveCount(0);
    await expect(
      chips(picker).and(page.locator("[data-catalogue-refused]")),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "Discard draft" }).click();
  } finally {
    await request.put("/api/capability-policy", {
      data: { policy: before.policy || {} },
    });
    await request.put("/api/settings", {
      data: { defaultCapabilities: defaultsBefore },
    });
  }
});
