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

// Found in the 2026-11-18 Processes walkthrough
// (docs/test-runs/2026-11-18-recurring-composed-ui.md): the /next/
// template dialog had no Task priority or Interactive control, so a
// template's priority and live-chat setting could only be set from the
// classic UI. The priority takes what a task's own does -- a whole
// number, 1 or more, "1e3" included -- and blank leaves the default.

import { expect, test } from "./fixtures";

type Tmpl = {
  id: string;
  name: string;
  priority?: number | null;
  interactive?: boolean | null;
};

test("a /next/ template takes a Task priority and Interactive, and keeps them through an edit", async ({
  page,
  pageErrors,
}) => {
  const templates = async () =>
    (await (await page.request.get("/api/templates")).json()) as Tmpl[];
  const name = `walk-priority-${Date.now()}`;
  let id = "";
  try {
    await page.goto("processes/templates");
    await page.getByTestId("proc-new-menu-btn").click();
    await page.getByTestId("proc-new-template").click();
    const dlg = page.getByTestId("template-dialog");
    await expect(dlg).toBeVisible();
    await dlg.getByTestId("tmpl-name-input").fill(name);
    await dlg.getByTestId("tmpl-title-input").fill("walk: priority");
    await dlg.getByTestId("tmpl-desc-input").fill("Carry a priority.");

    const pri = dlg.getByTestId("tmpl-priority-input");
    const save = dlg.getByTestId("tmpl-save-btn");
    for (const bad of ["2.5", "0", "high"]) {
      await pri.fill(bad);
      await expect(dlg.getByTestId("tmpl-priority-error")).toHaveText(
        "Use a whole number, 1 or more.",
      );
      await expect(pri).toHaveAttribute("aria-invalid", "true");
      await expect(save).toBeDisabled();
    }
    await pri.fill("1e3");
    await pri.blur();
    await expect(pri).toHaveValue("1000");
    await expect(dlg.getByTestId("tmpl-priority-error")).toHaveCount(0);

    const interactive = dlg.getByTestId("tmpl-interactive");
    await expect(interactive).toHaveAttribute("aria-checked", "false");
    await interactive.click();
    await expect(interactive).toHaveAttribute("aria-checked", "true");
    await save.click();
    await expect(dlg).toBeHidden();

    const created = (await templates()).find((t) => t.name === name);
    expect(created).toMatchObject({ priority: 1000, interactive: true });
    id = created!.id;

    await page.reload();
    const cfg = page.getByTestId("tmpl-config-pane");
    await expect(cfg).toContainText("Priority1000");
    await expect(cfg).toContainText("Interactiveon");

    // The edit reopens on what was saved; clearing the priority sends 0,
    // which takes the template back to the default.
    await page.getByTestId("tmpl-edit-btn").click();
    await expect(pri).toHaveValue("1000");
    await expect(interactive).toHaveAttribute("aria-checked", "true");
    await pri.fill("");
    await interactive.click();
    await save.click();
    await expect(dlg).toBeHidden();
    await expect
      .poll(async () => (await templates()).find((t) => t.id === id))
      .toMatchObject({ name });
    const edited = (await templates()).find((t) => t.id === id)!;
    expect(edited.priority ?? null).toBeNull();
    expect(!!edited.interactive).toBe(false);
    await expect(cfg).not.toContainText("Priority");
    await expect(cfg).not.toContainText("Interactive");
  } finally {
    if (id) await page.request.delete(`/api/templates/${id}`);
  }
  expect(pageErrors).toEqual([]);
});
