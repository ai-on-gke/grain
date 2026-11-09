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
import { chooseOption } from "./searchSelect.js";

// A deployment default the capability catalogue restricts to one repo,
// on a task filed with No repo (2026-10-29 test run, Bug 3). The demo
// defaults api:self-debug, grain#source (once self-debug) and
// github#read-issues; this test restricts the self-debug API preset to
// acme/widgets. The form used to post all defaults and be refused with a
// 400 for a grant nobody had picked. Now it says the refused default is
// left off, and files the task with the defaults the server keeps.

test.use({ demoArgs: ["-github-sim", "-fake-ci"] });

const RESTRICTED = {
  catalogue: {
    apiScopes: {
      presets: {
        "self-debug": { restrictions: { repos: ["acme/widgets"] } },
      },
    },
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
  const note = page.getByTestId("new-task-catalogue-refused");
  await expect(note).toHaveText(
    "settings/capabilities.yml refuses api:self-debug on a task with no repo, so that default is left off this task.",
  );
  return { note };
}

test("a No repo task leaves off a default the catalogue refuses there", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  const before = await (await request.get("/api/capability-policy")).json();
  try {
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

      // The note sits inside the grants' own column, at either width.
      const grants = page.getByTestId("new-task-grants");
      within(await note.boundingBox(), await grants.boundingBox());
      const form = page.locator("form", {
        has: page.getByRole("button", { name: "Create task" }),
      });
      expect(
        await form.evaluate((el) => el.scrollWidth - el.clientWidth),
      ).toBeLessThanOrEqual(1);

      const dir = process.env.SHOTS_DIR;
      if (dir) {
        for (const theme of ["light", "dark"]) {
          await page.evaluate(
            (t) => localStorage.setItem("grain.themeMode", t),
            theme,
          );
          await page.emulateMedia({ colorScheme: theme });
          await page.waitForTimeout(200);
          await note.scrollIntoViewIfNeeded();
          await page.screenshot({
            path: `${dir}/new-task-no-repo-${width}-${theme}.png`,
          });
        }
        await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
        await page.emulateMedia({ colorScheme: "light" });
      }

      if (width !== 1440) continue;
      // Filing leaves the untouched list to the server, which keeps the
      // defaults it may and drops api:self-debug, rather than refusing the task.
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
          "https://grain.dev/capabilities/grain#source={}",
        ]),
      );
      expect(detail.capabilities).not.toContain("api:self-debug");
      expect(detail.capabilities).not.toContain(
        "https://grain.dev/capabilities/api#self-debug",
      );
    }
    // On the repo the catalogue grants it to, nothing is refused.
    {
      await openStandalone(page, `E2E catalogue repo ${Date.now()}`);
      await chooseOption(page.getByLabel(/^Repos/), "acme/widgets");
      await expect(page.getByTestId("new-task-catalogue-refused")).toHaveCount(
        0,
      );
      await page.getByRole("button", { name: "Discard draft" }).click();
    }
  } finally {
    await request.put("/api/capability-policy", {
      data: { policy: before.policy || {} },
    });
  }
});
