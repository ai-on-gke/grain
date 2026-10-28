import { execFileSync } from "node:child_process";
import { test, expect } from "./fixtures.js";

// docs/test-plans/2026-10-04.md §2 step 2: the New task form's "Don't
// port" checkbox files a task with noPort, and the task pane shows both
// halves of a port pair as links that open each other.
//
// The demo runs no merge queue, so the pair's two links -- what
// orchestrator.filePorts writes when a change merges onto a paired branch
// -- are seeded into the demo's own store by the e2e release helper
// (link-port). Everything else is the real server and the built UI.
test.describe("Paired-branch ports in the browser", () => {
  test("files a task with Don't port ticked, and reads it back as noPort", async ({
    page,
    request,
  }) => {
    await page.goto("/");
    const title = `E2E no-port task ${Date.now()}`;
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await page.getByLabel("Title").fill(title);
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    const noPort = page.getByRole("checkbox", {
      name: "Don't port to the repo's paired branches",
    });
    await expect(noPort).not.toBeChecked();
    await noPort.check();
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    const row = page.locator(".task-row", { hasText: title });
    await expect(row).toBeVisible();
    const tasks = await (await request.get("/api/tasks")).json();
    const filed = tasks.find((t) => t.title === title);
    expect(filed, `no task titled ${title} in /api/tasks`).toBeTruthy();
    const detail = await (await request.get(`/api/tasks/${filed.id}`)).json();
    expect(detail.noPort).toBe(true);

    await row.click();
    const overlay = page.locator(".MuiDialog-paper");
    await expect(overlay.getByRole("heading", { name: title })).toBeVisible();
    await overlay.locator(".provenance-toggle").click();
    await expect(overlay.locator(".freshness")).toContainText("not ported");
  });

  test("shows a port pair's port-of and ported-by links, and they open each other", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const file = async (title) => {
      const res = await request.post("/api/tasks", {
        data: {
          title,
          description: title,
          repo: "acme/widgets",
          approved: true,
        },
      });
      expect(res.ok()).toBeTruthy();
      return res.json();
    };
    const sourceTitle = `E2E ported change ${stamp}`;
    const portTitle = `Port acme/widgets#7 to candidate: ${sourceTitle}`;
    const source = await file(sourceTitle);
    const port = await file(portTitle);

    await page.goto("/");
    const helper =
      process.env.GRAIN_E2E_RELEASE_HELPER_BIN || "/tmp/releasehelper";
    execFileSync(helper, [
      "-port",
      new URL(page.url()).port,
      "link-port",
      source.id,
      port.id,
    ]);

    const sourceDetail = await (
      await request.get(`/api/tasks/${source.id}`)
    ).json();
    expect(sourceDetail.portedBy).toEqual([port.id]);
    const portDetail = await (
      await request.get(`/api/tasks/${port.id}`)
    ).json();
    expect(portDetail.portOf).toBe(source.id);

    await page.reload();
    const overlay = page.locator(".MuiDialog-paper");
    await page.locator(".task-row", { hasText: portTitle }).click();
    await expect(
      overlay.getByRole("heading", { name: portTitle }),
    ).toBeVisible();
    await overlay.locator(".provenance-toggle").click();
    await expect(overlay.locator(".freshness")).toContainText(
      `port of ${source.id}`,
    );
    await overlay.getByRole("link", { name: source.id, exact: true }).click();

    await expect(
      overlay.getByRole("heading", { name: sourceTitle }),
    ).toBeVisible();
    await overlay.locator(".provenance-toggle").click();
    await expect(overlay.locator(".freshness")).toContainText(
      `ported by ${port.id}`,
    );
    await overlay.getByRole("link", { name: port.id, exact: true }).click();
    await expect(
      overlay.getByRole("heading", { name: portTitle }),
    ).toBeVisible();
  });
});
