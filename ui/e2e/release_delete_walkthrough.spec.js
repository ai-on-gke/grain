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

// docs/test-runs/2026-10-07-release-name-validation.md, finding 4:
// A release that can never be provisioned (for example an invalid name
// or an unprovisionable release) stays in the releases pane permanently.
// A person can now remove it with the confirm-guarded "Remove release" button,
// which sends DELETE /api/repos/{owner}/{name}/releases/{release}.
test.describe("Remove release that cannot be provisioned", () => {
  const releases = "/api/repos/acme/widgets/releases";

  test("remove release removes it via confirm dialog and frees the name", async ({
    page,
    request,
  }) => {
    // Clean up any pre-existing unmerged release
    const existing = await (await request.get(releases)).json();
    for (const r of existing) {
      if (r.name === "unprovisionable-feat") {
        await request.delete(`${releases}/${r.name}`);
      }
    }

    // 1. Create a release through API
    const res = await request.post(releases, {
      data: { name: "unprovisionable-feat" },
    });
    expect(res.status()).toBe(201);

    // 2. Visit the releases pane
    await page.goto("/repos/acme/widgets/releases");
    await expect(
      page.getByText("unprovisionable-feat.latest").first(),
    ).toBeVisible();

    // 3. Dismissing the confirm dialog leaves the release in place
    const removeBtn = page.getByRole("button", { name: "Delete release" });
    await expect(removeBtn).toBeVisible();

    let dialogMessage = "";
    page.once("dialog", (dialog) => {
      dialogMessage = dialog.message();
      dialog.dismiss();
    });
    await removeBtn.click();
    expect(dialogMessage).toContain("Remove release unprovisionable-feat?");
    await expect(
      page.getByText("unprovisionable-feat.latest").first(),
    ).toBeVisible();

    // 4. Accepting the confirm dialog removes the release
    const deletedResponse = page.waitForResponse(
      (r) =>
        r.url().includes("/releases/unprovisionable-feat") &&
        r.request().method() === "DELETE",
    );
    page.once("dialog", (dialog) => {
      dialog.accept();
    });
    await removeBtn.click();
    expect((await deletedResponse).status()).toBe(204);

    // 5. Verify the release is gone from the releases list in the UI.
    // A count rather than not.toBeVisible(): until the pane re-renders the
    // name can be in two places at once (the releases line and a
    // "still provisioning" hint), and a strict-mode locator that matches
    // two elements fails at once instead of waiting for both to go.
    await expect(page.getByText("unprovisionable-feat.latest")).toHaveCount(0);

    // 6. Direct API check: release is deleted and GET returns 404
    const getRes = await request.get(`${releases}/unprovisionable-feat`);
    expect(getRes.status()).toBe(404);

    // 7. Verify the name can be re-created
    const recreateRes = await request.post(releases, {
      data: { name: "unprovisionable-feat" },
    });
    expect(recreateRes.status()).toBe(201);

    // Cleanup
    await request.delete(`${releases}/unprovisionable-feat`);
  });
});
