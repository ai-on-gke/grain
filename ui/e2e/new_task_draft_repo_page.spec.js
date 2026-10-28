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

// grain/task-1585: a New task draft started on one repo's page and
// reopened from another's. Found by the 2026-10-02 repository walkthrough
// (docs/test-runs/2026-10-02-repository-release-memory-ui.md §2.3), where
// the draft's repo silently replaced the page's. A repo the draft only
// took from the page it was opened on now yields to the page the
// operator is on, keeping what they typed; one they picked by hand is
// kept, with a notice saying so, until the draft is discarded.
test.describe("New task draft across repo pages", () => {
  test("files into the page's repo unless the draft picked its own", async ({
    page,
    request,
  }) => {
    const added = await request.post("/api/repos", {
      data: { repo: "acme/gadgets" },
    });
    expect(added.ok()).toBeTruthy();
    // Taken back off the target list at the end: specs later in this
    // worker share the store, and suites_workflows.spec.js picks a repo by
    // its place in a list this one would otherwise have lengthened.
    try {
      const openNewTask = async (repo) => {
        await page.goto(`/repos/${repo}`);
        await expect(
          page.getByRole("heading", { name: repo, exact: true }),
        ).toBeVisible();
        await page
          .getByRole("button", { name: "New task", exact: true })
          .click();
        await expect(
          page.getByRole("heading", { name: "New task" }),
        ).toBeVisible();
      };
      const notice = page.getByTestId("draft-repo-notice");
      // exact: the repo page has a "Base branch *" box of its own.
      const base = page.getByRole("textbox", {
        name: "Base branch",
        exact: true,
      });

      // The page default: started on gadgets, reopened on widgets.
      await openNewTask("acme/gadgets");
      await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/gadgets");
      await page.getByLabel("Title").fill("Gadgets draft");
      await base.fill("feature/x");
      await page.keyboard.press("Escape");
      await expect(
        page.getByRole("heading", { name: "New task" }),
      ).toBeHidden();
      const stored = await page.evaluate(() =>
        JSON.parse(localStorage.getItem("grain.task.draft")),
      );
      expect(stored.repo).toBe("acme/gadgets");
      expect(stored.repoEdited).toBe(false);

      await openNewTask("acme/widgets");
      await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/widgets");
      await expect(page.getByLabel("Title")).toHaveValue("Gadgets draft");
      await expect(base).toHaveValue("feature/x");
      await expect(notice).toHaveCount(0);

      // A picked repo: kept, and said so, until the draft is discarded.
      await page.getByLabel(/^Repos/).selectOption("acme/gadgets");
      await page.keyboard.press("Escape");
      await expect(
        page.getByRole("heading", { name: "New task" }),
      ).toBeHidden();

      await openNewTask("acme/widgets");
      await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/gadgets");
      await expect(notice).toHaveText(
        "Restored a draft for acme/gadgets. Discard it to file in acme/widgets.",
      );
      await page.getByRole("button", { name: "Discard draft" }).click();
      await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/widgets");
      await expect(page.getByLabel("Title")).toHaveValue("");
      await expect(notice).toHaveCount(0);
    } finally {
      await request.delete("/api/repos/acme/gadgets");
    }
  });
});
