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

// The Boards menu on /next/tasks deleted a saved board -- a setting
// shared by everyone on the deployment -- on one click of its ×, and
// named three same-named boards in different folders "Triage" and
// "Delete board Triage" alike (2026-11-13 saved boards walkthrough,
// docs/test-runs/2026-11-13-saved-boards-and-visualizations-task-5385.md).
test("the Boards menu tells same-named boards apart and asks before deleting one", async ({
  page,
}) => {
  const made = [];
  const mk = async (data) => {
    const res = await page.request.post("/api/boards", { data });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const b = await res.json();
    made.push(b.id);
    return b;
  };
  try {
    const loose = await mk({ name: "E2E Twin", kind: "list" });
    const ops = await mk({ name: "E2E Twin", folder: "E2E Ops", kind: "list" });
    await mk({ name: "E2E Twin", folder: "E2E Zeta", kind: "list" });

    await page.goto("tasks");
    const trigger = page.getByTestId("saved-boards-btn");
    await trigger.click();
    const menu = page.getByTestId("saved-boards-menu");
    await expect(
      menu.getByRole("button", {
        name: "E2E Twin, outside every folder",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      menu.getByRole("button", { name: "E2E Twin, in E2E Ops", exact: true }),
    ).toBeVisible();
    await expect(
      menu.getByRole("button", { name: "Delete board E2E Twin, in E2E Zeta" }),
    ).toBeVisible();
    // The folder is drawn after the name, so sighted readers can tell
    // them apart too.
    await expect(page.getByTestId(`board-apply-${ops.id}`)).toContainText(
      "E2E Ops",
    );

    // × asks first. Cancel deletes nothing and hands focus back to the
    // Boards button.
    const deletes = [];
    page.on("request", (r) => {
      if (r.method() === "DELETE" && r.url().includes("/api/boards/")) {
        deletes.push(new URL(r.url()).pathname);
      }
    });
    await page.getByTestId(`board-delete-${loose.id}`).click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText(
      "Delete board E2E Twin, outside every folder?",
    );
    await expect(dialog).toContainText("everyone on this deployment");
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(deletes).toEqual([]);

    // Accepting deletes exactly that board, and focus lands on the
    // Boards button rather than on <body>.
    await trigger.click();
    await page.getByTestId(`board-delete-${loose.id}`).click();
    await dialog.getByRole("button", { name: "Delete board" }).click();
    await expect(dialog).toBeHidden();
    await expect.poll(() => deletes).toEqual([`/api/boards/${loose.id}`]);
    await expect(trigger).toBeFocused();
    const left = await (await page.request.get("/api/boards")).json();
    expect(left.map((b) => b.id)).not.toContain(loose.id);
    expect(left.map((b) => b.id)).toContain(ops.id);
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});
