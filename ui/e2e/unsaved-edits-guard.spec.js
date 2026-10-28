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

// Leaving an edited pane (found by the 2026-10-02 exploratory UI pass):
// a schedule, a template or a suite saves only on Save, and Escape or
// "← Back" used to close it at once and drop the edit with no word. Now
// leaving asks first, and saying no keeps the pane and the edit.

const PROMPT = /unsaved changes/;

test.describe("Leaving a pane with an unsaved edit", () => {
  test("a schedule asks before Escape or Back discards an edited title", async ({
    page,
  }) => {
    const dialogs = [];
    page.on("dialog", (d) => dialogs.push(d.message()));

    // One of the deployment's own schedules that files a task of its
    // own, so its Title box is drawn and its Save is live: not one a repo
    // declares, not one of grain's standing jobs, and not one whose
    // content comes from a template or a suite.
    const schedules = await (await page.request.get("/api/schedules")).json();
    const editable = schedules.find(
      (s) => !s.declaredIn && !s.system && !s.templateId && !s.suiteId,
    );
    expect(editable).toBeTruthy();
    await page.goto(`/schedules/${encodeURIComponent(editable.id)}`);
    const heading = page.getByRole("heading", { name: "Edit schedule" });
    await expect(heading).toBeVisible();
    const editUrl = page.url();

    // Untouched, Escape leaves at once and asks nothing.
    await page.keyboard.press("Escape");
    await expect(heading).toHaveCount(0);
    expect(dialogs).toEqual([]);

    await page.goto(editUrl);
    await expect(heading).toBeVisible();
    const title = page.getByLabel("Title", { exact: true });
    const saved = await title.inputValue();
    await title.fill(`${saved} (edited)`);

    // Escape: told no, the pane and the edit both stay.
    page.once("dialog", (d) => d.dismiss());
    await page.keyboard.press("Escape");
    await expect.poll(() => dialogs.length).toBe(1);
    expect(dialogs[0]).toMatch(PROMPT);
    await expect(heading).toBeVisible();
    await expect(title).toHaveValue(`${saved} (edited)`);
    expect(page.url()).toBe(editUrl);

    // "← Schedules": the same question.
    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: /Schedules/ }).click();
    await expect.poll(() => dialogs.length).toBe(2);
    await expect(heading).toBeVisible();
    await expect(title).toHaveValue(`${saved} (edited)`);

    // Changed back to what was saved, there is nothing to lose.
    await title.fill(saved);
    await page.keyboard.press("Escape");
    await expect(heading).toHaveCount(0);
    expect(dialogs).toHaveLength(2);

    // Told yes, it leaves and nothing was written.
    await page.goto(editUrl);
    await title.fill(`${saved} (discarded)`);
    page.once("dialog", (d) => d.accept());
    await page.keyboard.press("Escape");
    await expect(heading).toHaveCount(0);
    expect(dialogs).toHaveLength(3);
    await page.goto(editUrl);
    await expect(page.getByLabel("Title", { exact: true })).toHaveValue(saved);
  });

  test("a new template and a new suite ask too, and an untouched one does not", async ({
    page,
  }) => {
    const dialogs = [];
    page.on("dialog", (d) => dialogs.push(d.message()));

    await page.goto("/templates");
    await page.getByRole("button", { name: "+ New template" }).click();
    const tmpl = page.getByRole("heading", { name: "New template" });
    await expect(tmpl).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(tmpl).toHaveCount(0);
    expect(dialogs).toEqual([]);

    await page.getByRole("button", { name: "+ New template" }).click();
    await page.getByLabel("Name", { exact: true }).fill("half-written");
    page.once("dialog", (d) => d.dismiss());
    await page.keyboard.press("Escape");
    await expect.poll(() => dialogs.length).toBe(1);
    expect(dialogs[0]).toMatch(PROMPT);
    await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
      "half-written",
    );
    // Cancel is already somebody saying "throw it away", and is not asked.
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(tmpl).toHaveCount(0);
    expect(dialogs).toHaveLength(1);

    await page.goto("/suites");
    await page.getByRole("button", { name: "+ New suite" }).click();
    const suite = page.getByRole("heading", { name: "New suite" });
    await expect(suite).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(suite).toHaveCount(0);
    expect(dialogs).toHaveLength(1);

    await page.getByRole("button", { name: "+ New suite" }).click();
    await page.getByLabel("Name", { exact: true }).fill("half-written suite");
    page.once("dialog", (d) => d.dismiss());
    await page.keyboard.press("Escape");
    await expect.poll(() => dialogs.length).toBe(2);
    expect(dialogs[1]).toMatch(PROMPT);
    await expect(suite).toBeVisible();
  });
});
