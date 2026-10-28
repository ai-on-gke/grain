import { test, expect } from "./fixtures.js";

// A demo of its own: it creates and deletes acme/widgets releases, which
// specs sharing a demo do not expect. (Its step refusing a non-admin
// went with team roles: docs/single-tenant-reset.md, 3e-iii-c2.)
test.use({ demoArgs: ["-default-target-repo=acme/widgets", "-open=false"] });

test.describe("Release & Candidate Deletion Lifecycle Walkthrough (docs/test-plans/2026-10-09 §2)", () => {
  const repo = "acme/widgets";
  const releaseName = "v2.5.0-abandoned";

  test("full browser and API release deletion and reuse walkthrough", async ({
    page,
    request,
  }) => {
    // 0. Ensure clean state: delete test release if it already exists
    const checkExisting = await request.get(`/api/repos/${repo}/releases`);
    if (checkExisting.ok()) {
      const list = await checkExisting.json();
      for (const r of list) {
        if (r.name === releaseName) {
          await request.delete(`/api/repos/${repo}/releases/${releaseName}`);
        }
      }
    }

    // 1. Navigate to Repositories -> acme/widgets -> Releases
    await page.goto(`/repos/${repo}/releases`);
    await expect(
      page.getByRole("heading", { name: "acme/widgets releases" }),
    ).toBeVisible();

    // 2. Create a test candidate release `v2.5.0-abandoned`
    const releaseInput = page.getByLabel("New release name");
    await expect(releaseInput).toBeVisible();
    await releaseInput.fill(releaseName);

    const createReleaseBtn = page.getByRole("button", {
      name: "Create release",
    });
    await expect(createReleaseBtn).toBeVisible();
    await createReleaseBtn.click();

    // Creation cuts candidate 1 automatically (rc.1)
    // Verify candidate row is displayed
    await expect(page.getByText(`${releaseName}.latest`).first()).toBeVisible();
    await expect(page.getByText(`${releaseName}.rc.1`).first()).toBeVisible();

    // 3. Click "Delete release" (Remove release control); verify native confirm dialog
    const deleteBtn = page.getByRole("button", { name: "Delete release" });
    await expect(deleteBtn).toBeVisible();
    await expect(deleteBtn).toBeEnabled();

    // Dismiss / cancel dialog: verify release and candidate rows remain displayed
    let dialogMessage = "";
    page.once("dialog", (dialog) => {
      dialogMessage = dialog.message();
      dialog.dismiss();
    });
    await deleteBtn.click();

    expect(dialogMessage).toContain(`Remove release ${releaseName}?`);
    // Rows remain visible
    await expect(page.getByText(`${releaseName}.latest`).first()).toBeVisible();
    await expect(page.getByText(`${releaseName}.rc.1`).first()).toBeVisible();

    // 4. Accept the dialog; verify DELETE returns HTTP 204 and release row disappears dynamically without page reload
    const deletePromise = page.waitForResponse(
      (resp) =>
        resp.url().includes(`/api/repos/${repo}/releases/${releaseName}`) &&
        resp.request().method() === "DELETE",
    );

    page.once("dialog", (dialog) => {
      dialog.accept();
    });
    await deleteBtn.click();

    const deleteResp = await deletePromise;
    expect(deleteResp.status()).toBe(204);

    // Release row disappears dynamically without page refresh
    await expect(page.getByText(`${releaseName}.latest`)).not.toBeVisible();
    await expect(page.getByText(`${releaseName}.rc.1`)).not.toBeVisible();

    // 5. Release name reuse: immediately re-create release with identical name
    await releaseInput.fill(releaseName);
    const recreatePromise = page.waitForResponse(
      (resp) =>
        resp.url().includes(`/api/repos/${repo}/releases`) &&
        resp.request().method() === "POST",
    );
    await createReleaseBtn.click();

    const recreateResp = await recreatePromise;
    expect(recreateResp.status()).toBe(201);
    await expect(page.getByText(`${releaseName}.latest`).first()).toBeVisible();
    await expect(page.getByText(`${releaseName}.rc.1`).first()).toBeVisible();

    // Clean up
    await request.delete(`/api/repos/${repo}/releases/${releaseName}`);
  });
});
