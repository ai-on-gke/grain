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

import { test, expect } from "./fixtures.js";

// Found by the 2026-10-07 repository walkthrough
// (docs/test-runs/2026-10-07-repository-release-memory-ui.md).
test.describe("Repository walkthrough regressions (2026-10-07)", () => {
  const workflow = (job) =>
    `name: ${job}\non: [push]\njobs:\n  ${job}:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${job}\n`;

  // Leaving a workflow box for Add workflow is a blur, which saves; the
  // save's answer replaced every row, the new empty one included. And a
  // Remove while another row was half written sent nothing at all, so
  // the workflow came back on the next load.
  test("the repo's workflow editor keeps a row added mid-save, and a removal", async ({
    page,
    request,
  }) => {
    const stored = async () =>
      (
        (await (await request.get("/api/repos/acme/widgets/workflows")).json())
          .workflows || []
      ).map((w) => w.path);
    try {
      await page.goto("/repos/acme/widgets#ci");
      await page.getByRole("button", { name: "Add workflow" }).click();
      await page.locator("[name=repoWorkflowPath0]").fill("lint.yml");
      await page.locator("[name=repoWorkflowText0]").fill(workflow("lint"));
      // Straight from the box to the button: the blur and the click.
      await page.getByRole("button", { name: "Add workflow" }).click();
      await expect(page.locator("[name=repoWorkflowPath0]")).toHaveValue(
        ".grain/workflows/lint.yml",
      );
      await expect(page.locator("[name=repoWorkflowPath1]")).toHaveValue("");
      expect(await stored()).toEqual([".grain/workflows/lint.yml"]);

      // Half of a second workflow, then remove the first.
      await page.locator("[name=repoWorkflowPath1]").fill("half.yml");
      page.once("dialog", (dialog) => {
        expect(dialog.message()).toContain(
          "Delete the workflow .grain/workflows/lint.yml grain declares for acme/widgets?",
        );
        dialog.accept();
      });
      await page
        .getByRole("button", {
          name: "Remove workflow .grain/workflows/lint.yml",
        })
        .click();
      await expect(page.locator("[name^=repoWorkflowPath]")).toHaveCount(1);
      await expect(page.locator("[name=repoWorkflowPath0]")).toHaveValue(
        "half.yml",
      );
      await expect.poll(stored).toEqual([]);
      await page.reload();
      await expect(
        page.getByText("grain declares no workflows for this repo."),
      ).toBeVisible();
    } finally {
      await request.put("/api/repos/acme/widgets/workflows", {
        data: { workflows: [] },
      });
    }
  });

  // The Memory page saves each box only on its own Save, so leaving it
  // with an edit -- here, from the rail -- asks first (grain/task-3100;
  // it asked from Team settings' Escape while it lived there).
  test("leaving the Memory page asks before dropping an unsaved memory edit", async ({
    page,
  }) => {
    await page.goto("/settings/memory");
    const box = page.getByRole("textbox", {
      name: "memory about acme/widgets",
    });
    const original = await box.inputValue();
    await box.fill(`${original}\n- unsaved correction`);

    const ci = page.getByRole("button", { name: /^CI\b/ }).first();
    let asked = "";
    page.once("dialog", (dialog) => {
      asked = dialog.message();
      dialog.dismiss();
    });
    await ci.click();
    await expect.poll(() => asked).toContain("unsaved changes");
    await expect(page).toHaveURL(/\/memory$/);
    await expect(box).toHaveValue(`${original}\n- unsaved correction`);

    // A document's own Close asks too.
    await page
      .getByRole("button", {
        name: "Open flaky-e2e (acme/widgets)",
      })
      .click();
    const doc = page.getByRole("textbox", {
      name: "Document: flaky-e2e (acme/widgets)",
    });
    await doc.fill(`${await doc.inputValue()} more`);
    page.once("dialog", (dialog) => dialog.dismiss());
    await page
      .getByRole("button", {
        name: "Close flaky-e2e (acme/widgets)",
      })
      .click();
    await expect(doc).toBeVisible();

    // Saying yes leaves, and nothing was written.
    page.once("dialog", (dialog) => dialog.accept());
    await ci.click();
    await expect(page).toHaveURL(/\/ci$/);
    await page.goto("/settings/memory");
    await expect(
      page.getByRole("textbox", {
        name: "memory about acme/widgets",
      }),
    ).toHaveValue(original);
  });

  // "Depends on" offers only the templates the plan's other items name,
  // so a dependency left on a removed item's template could not be
  // unticked, and the server refused every save after.
  test("removing a qualification item drops it from the others' dependencies", async ({
    page,
    request,
  }) => {
    const plan = "/api/repos/acme/widgets/qualification-plan";
    try {
      await page.goto("/repos/acme/widgets/releases");
      const template = page.getByRole("combobox", { name: "Template" });
      const dependsOn = page.getByRole("combobox", { name: "Depends on" });
      await page.getByRole("button", { name: "Add item" }).click();
      await page.getByRole("button", { name: "Add item" }).click();
      await template.nth(0).click();
      await page.getByRole("option", { name: "security-audit" }).click();
      await template.nth(1).click();
      await page.getByRole("option", { name: "release-qualification" }).click();
      await dependsOn.nth(1).click();
      await page.getByRole("option", { name: "security-audit" }).click();
      await page.keyboard.press("Escape");
      await expect(dependsOn.nth(1)).toHaveText("security-audit");

      await page.getByRole("button", { name: "Remove" }).first().click();
      await expect(dependsOn).toHaveCount(1);
      await expect(dependsOn.first()).not.toHaveText("security-audit");
      await page
        .getByRole("button", { name: "Save qualification plan" })
        .click();
      await expect
        .poll(async () =>
          (await (await request.get(plan)).json()).items.map((it) => [
            it.templateName,
            it.dependsOn || [],
          ]),
        )
        .toEqual([["release-qualification", []]]);
    } finally {
      await request.put(plan, {
        data: { requireApproval: false, autoPromote: false, items: [] },
      });
    }
  });
});
