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

test("shows and edits repository skill folders in Settings and Repo Agents tab, and summarizes skills in Prompt view", async ({
  page,
  request,
}) => {
  const cfgRes = await request.get("/api/config");
  expect(cfgRes.ok()).toBeTruthy();
  const cfg = await cfgRes.json();
  const repo = cfg.targetRepos[0];
  const [owner, name] = repo.split("/");

  try {
    // 1. Settings -> Repositories shows Skills column aligned on one header row without page overflow.
    for (const [width, height] of [
      [1440, 1000],
      [390, 844],
    ]) {
      await page.setViewportSize({ width, height });
      await page.goto("/settings/repositories");
      const row = page.locator(
        `[data-testid="settings-repo-row"][data-repo="${repo}"]`,
      );
      const skillsCell = row.getByTestId("settings-repo-skills");
      await expect(skillsCell).toContainText("Default folders");

      const headers = page
        .getByRole("table", { name: "Repositories" })
        .locator("thead th");
      await expect(headers).toHaveCount(6);
      const headerBoxes = [];
      for (let i = 0; i < 6; i++) {
        headerBoxes.push(await headers.nth(i).boundingBox());
      }
      for (let i = 1; i < headerBoxes.length; i++) {
        expect(
          Math.abs(headerBoxes[i].y - headerBoxes[0].y),
        ).toBeLessThanOrEqual(2);
        expect(headerBoxes[i].x).toBeGreaterThanOrEqual(
          headerBoxes[i - 1].x + headerBoxes[i - 1].width - 1,
        );
      }
      if (width >= 1440) {
        const tableFits = await page
          .getByRole("table", { name: "Repositories" })
          .evaluate(
            (el) =>
              el.parentElement.scrollWidth <= el.parentElement.clientWidth + 1,
          );
        expect(tableFits).toBeTruthy();
      }
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        ),
      ).toBeLessThanOrEqual(1);
    }

    // 2. Repo Agents tab aligns Repository skills beneath Setup command at desktop and phone widths, and saves edits.
    for (const [width, height] of [
      [1440, 1000],
      [390, 844],
    ]) {
      await page.setViewportSize({ width, height });
      await page.goto(`/repos/${owner}/${name}/settings/agents`);
      const setupField = page.getByLabel(`Setup command for ${repo}`);
      const skillsSection = page.getByTestId("repo-skills-section");
      await skillsSection.scrollIntoViewIfNeeded();
      await expect(setupField).toBeVisible();
      await expect(skillsSection).toBeVisible();

      const setupBox = await setupField
        .locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        )
        .boundingBox();
      const skillsInput = skillsSection.getByLabel(`Skill folders for ${repo}`);
      const skillsInputBox = await skillsInput
        .locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        )
        .boundingBox();
      expect(Math.abs(skillsInputBox.x - setupBox.x)).toBeLessThanOrEqual(2);
      expect(
        Math.abs(skillsInputBox.width - setupBox.width),
      ).toBeLessThanOrEqual(2);
      expect(skillsInputBox.y).toBeGreaterThan(setupBox.y + setupBox.height);
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        ),
      ).toBeLessThanOrEqual(1);
    }

    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/repos/${owner}/${name}/settings/agents`);
    const skillsSection = page.getByTestId("repo-skills-section");
    const input = skillsSection.getByLabel(`Skill folders for ${repo}`);
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/api/repos/${owner}/${name}/settings`) &&
        r.request().method() === "PUT",
    );
    await input.fill(".claude/skills, .gemini/skills");
    await input.blur();
    const putRes = await saved;
    expect(putRes.ok()).toBeTruthy();
    await expect(skillsSection).toContainText(
      "Effective folders for " + repo + ": .claude/skills, .gemini/skills",
    );

    await page.goto("/settings/repositories");
    const updatedCell = page
      .locator(`[data-testid="settings-repo-row"][data-repo="${repo}"]`)
      .getByTestId("settings-repo-skills");
    await expect(updatedCell).toContainText(".claude/skills, .gemini/skills");

    // 3. Running task's Prompt overlay displays the skills shown above the prompt log at desktop and phone widths.
    const tasksRes = await request.get("/api/tasks");
    expect(tasksRes.ok()).toBeTruthy();
    const tasksBody = await tasksRes.json();
    const tasks = Array.isArray(tasksBody) ? tasksBody : tasksBody.tasks || [];
    const runningTask = tasks.find((t) => t.state === "running");
    expect(runningTask).toBeTruthy();

    for (const [width, height] of [
      [1440, 1000],
      [390, 844],
    ]) {
      await page.setViewportSize({ width, height });
      await page.goto(`/tasks/${runningTask.id}`);
      await page.getByRole("button", { name: /^Prompt\b/i }).click();
      const dialog = page.getByRole("dialog", {
        name: `Prompt for ${runningTask.id}`,
      });
      const heading = dialog.locator("h2");
      const summary = dialog.getByTestId("prompt-skills");
      const pre = dialog.locator("pre.logs-view");
      await expect(summary).toBeVisible();
      await expect(summary).toContainText("Skills shown (2):");
      await expect(summary).toContainText("repo: .claude/skills/lint-and-test");
      await expect(pre).toBeVisible();

      const dialogBox = await dialog.boundingBox();
      const h2Box = await heading.boundingBox();
      const sumBox = await summary.boundingBox();
      const preBox = await pre.boundingBox();
      expect(sumBox.y).toBeGreaterThanOrEqual(h2Box.y + h2Box.height - 2);
      expect(sumBox.y + sumBox.height).toBeLessThanOrEqual(preBox.y + 2);
      expect(Math.abs(sumBox.x - preBox.x)).toBeLessThanOrEqual(4);
      expect(sumBox.x + sumBox.width).toBeLessThanOrEqual(
        dialogBox.x + dialogBox.width + 1,
      );
    }
  } finally {
    await request.put(`/api/repos/${owner}/${name}/settings`, {
      data: { skills: {} },
    });
  }
});
