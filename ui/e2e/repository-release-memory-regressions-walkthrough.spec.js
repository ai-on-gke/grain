import { test, expect } from "./fixtures.js";

// Two faults the 2026-10-11 repository, release and memory walkthrough
// found (docs/test-runs/2026-10-11-repository-release-memory-ui.md),
// each driven here the way the walkthrough met it: through the real
// `grain demo` server and its seeded store, with nothing stubbed.
test.describe.configure({ mode: "serial" });

// The demo seeds a standing Director session whose own task targets
// acme/widgets. The task list a repo's "N tasks filed here" link opens
// leaves session tasks out (taskFilters.js's isSessionTask), and so does the
// rail; the repo's own count used to include it, so the link promised
// one task more than it opened.
test("a repo's task count is the number of tasks its link opens", async ({
  page,
}) => {
  await page.goto("/repos");
  const row = page.locator(".repo-list-row", { hasText: "acme/widgets" });
  // The row is drawn from the configuration before the task list has
  // loaded, reading "0 tasks" until it does; wait for the seeded tasks
  // to be counted rather than reading that first frame.
  const count = row.getByText(/^\d+ tasks?$/);
  await expect(count).toHaveText(/^[1-9]\d* tasks?$/);
  const listed = Number((await count.innerText()).match(/\d+/)[0]);

  await row.getByRole("link", { name: "acme/widgets" }).click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
  const link = page.getByRole("link", { name: /tasks? filed here$/ });
  await expect(link).toHaveText(
    `${listed} task${listed === 1 ? "" : "s"} filed here`,
  );

  await link.click();
  await expect(page).toHaveURL(/f=repo:any:acme\/widgets/);
  await expect(
    page.locator(".task-list li .task-row, .task-list .task-row"),
  ).toHaveCount(listed);
  await expect(page.getByText("Director Session")).toHaveCount(0);

  await page.goBack();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
  await expect(link).toBeVisible();
});

// A memory document saved with no text is deleted on the server
// (ui.Client.PutMemoryDocument). The Delete button asks first; emptying
// the box and pressing Save document used to delete it without a word.
test("saving a memory document empty asks before it deletes it", async ({
  page,
}) => {
  await page.goto("/settings/memory");

  const label = "release-checklist (this deployment)";
  await page.getByRole("button", { name: `Open ${label}` }).click();
  const text = page.getByRole("textbox", { name: `Document: ${label}` });
  await expect(text).not.toHaveValue("");
  await text.fill("   ");

  const asked = [];
  page.once("dialog", (d) => {
    asked.push(d.message());
    d.dismiss();
  });
  await page.getByRole("button", { name: `Save document ${label}` }).click();
  await expect.poll(() => asked.length).toBe(1);
  expect(asked[0]).toMatch(/^Delete the memory document "release-checklist"\?/);

  // Declined: the document is still stored, and the edit still on screen.
  const docs = await (await page.request.get("/api/memory/documents")).json();
  expect(docs.map((d) => d.name)).toContain("release-checklist");
  await expect(text).toHaveValue("   ");
});
