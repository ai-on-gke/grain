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

test.describe("Walkthrough: Auto-Saving Repo Pane & Sidebar Release Stamp (docs/test-plans/2026-09-30.md §3)", () => {
  // A repository of the deployment's own that no other spec knows, so
  // what this saves is seen nowhere else, and taken off the deployment's
  // list again afterwards. (It was a team's, made for the purpose, until
  // teams went: docs/single-tenant-reset.md, 3e-iii-c2.)
  async function withTestRepo(page, fn) {
    const idSuffix = `${Date.now()}-${Math.random().toString(36).substring(2, 7)}`;
    const targetRepo = `alpha/frontend-${idSuffix}`;
    const before = await (await page.request.get("/api/deployment")).json();
    const targetRepos = before.targetRepos || [];
    const declare = async (repos) => {
      const res = await page.request.put("/api/deployment", {
        data: { targetRepos: repos },
      });
      expect(res.ok(), `PUT /api/deployment: ${await res.text()}`).toBe(true);
    };
    await declare([...targetRepos, targetRepo]);
    try {
      await fn(targetRepo);
    } finally {
      await declare(targetRepos);
    }
  }

  test("1. Sidebar footer displays build stamp with release version, commit SHA, and timestamp", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("aside")).toBeVisible();

    // The build stamp in the sidebar footer surfaces version info from /api/config (pkg/version)
    const stamp = page.locator("aside").getByText(/v1\.2\.0/);
    await expect(stamp).toBeVisible();

    const stampText = await stamp.innerText();
    // Format: "v1.2.0 · <commit-sha> · <timestamp>Z" (or "v1.2.0 · <commit-sha>-dirty · <timestamp>Z")
    expect(stampText).toMatch(/^v1\.2\.0/);
    expect(stampText).toContain("·");

    // Check title attribute (tooltip)
    const titleAttr = await stamp.getAttribute("title");
    expect(titleAttr).toBeTruthy();
    expect(titleAttr).toMatch(/v1\.2\.0/);
  });

  test("4. Auto-saving RepoPage: setup command, default capabilities, and prompt extension persist on blur/change without manual save clicks", async ({
    page,
  }) => {
    await withTestRepo(page, async (targetRepo) => {
      // Navigate directly to targetRepo's RepoPage
      await page.goto(`/repos/${targetRepo}`);
      await expect(
        page.getByRole("heading", { name: targetRepo }),
      ).toBeVisible();
      // The deployment-wide layer, which the API reads below check and
      // which is all the page edits (single-tenant reset, stage 3e-ii).
      await page.getByRole("tab", { name: "Agents" }).click();

      // 1. Modify Setup Command
      const setupInput = page.locator('textarea[name="repoSetupCommand"]');
      await expect(setupInput).toBeVisible();
      const newSetupCmd = "npm install --legacy-peer-deps && make build";
      await setupInput.fill(newSetupCmd);

      // 2. Modify Default Capabilities (multi-select triggers onChange auto-save)
      const capsSelect = page.getByRole("combobox", {
        name: "Default capabilities",
      });
      await expect(capsSelect).toBeVisible();
      await capsSelect.click();
      // Select "Self debug" from capabilities menu list. Not GCP key: the
      // demo makes that a deployment default, which every repo has on
      // whatever it says, so choosing it here would prove nothing saved.
      const selfDebugOption = page.getByRole("option", {
        name: "Self debug",
        exact: true,
      });
      await expect(selfDebugOption).toBeVisible();
      await selfDebugOption.click();
      await page.keyboard.press("Escape"); // Close select dropdown

      // 3. Modify Prompt Extension
      const promptInput = page.locator('textarea[name="repoPromptExtension"]');
      await expect(promptInput).toBeVisible();
      const newPromptExt = `Custom instruction prompt for ${targetRepo} auto-save verification.`;
      await promptInput.fill(newPromptExt);

      // 4. Trigger blur on inputs by clicking outside (e.g. repo heading)
      await page.getByRole("heading", { name: targetRepo }).click();

      // Verify there is NO manual save button for setup command, default capabilities, or prompt extension
      await expect(
        page.getByRole("button", { name: /Save (capabilities|setup|prompt)/i }),
      ).toHaveCount(0);

      // 5. Navigate away to Tasks
      await page.getByRole("button", { name: /^Tasks/ }).click();
      await expect(page.locator(".task-row").first()).toBeVisible();

      // 6. Navigate back to targetRepo RepoPage and hard-reload page to assert SQLite persistence
      await page.goto(`/repos/${targetRepo}`);
      await page.reload();
      await expect(
        page.getByRole("heading", { name: targetRepo }),
      ).toBeVisible();
      await page.getByRole("tab", { name: "Agents" }).click();

      // Verify fields retained values in UI
      await expect(
        page.locator('textarea[name="repoSetupCommand"]'),
      ).toHaveValue(newSetupCmd);
      await expect(
        page.locator('textarea[name="repoPromptExtension"]'),
      ).toHaveValue(newPromptExt);

      // 7. Verify values via direct backend API queries
      const setupRes = await page.request.get(
        `/api/repos/${targetRepo}/setup-command`,
      );
      expect(setupRes.ok()).toBe(true);
      const setupJson = await setupRes.json();
      expect(setupJson.setupCommand).toBe(newSetupCmd);

      const promptRes = await page.request.get(
        `/api/repos/${targetRepo}/prompt-extension`,
      );
      expect(promptRes.ok()).toBe(true);
      const promptJson = await promptRes.json();
      expect(promptJson.promptExtension).toBe(newPromptExt);

      const capsRes = await page.request.get(
        `/api/repos/${targetRepo}/capabilities`,
      );
      expect(capsRes.ok()).toBe(true);
      expect((await capsRes.json()).defaultCapabilities).toContain(
        "self-debug",
      );
    });
  });
});
