import { test, expect } from "./fixtures.js";

// Pausing and resuming one task from its page (ui.Client.Pause/Resume):
// the Pause button is in the actions column, a paused task reads "Paused"
// with who paused it, Resume sits where Pause was, and resuming puts the
// task back in the state it was paused from.
//
// The task is filed by the test itself and closed afterwards, so nothing
// this does is left on the worker's shared demo for another file to find.
test("a task is paused and resumed from its page", async ({ page }) => {
  const created = await page.request.post("/api/tasks", {
    data: {
      title: "Pause me from the page",
      description: "e2e",
      approved: true,
    },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    await page.request.post(`/api/tasks/${id}/run-now`, { data: {} });
    await page.goto(`/tasks/${id}`);
    const actions = page.locator(".actions");
    await expect(actions).toContainText(
      "Asked for a run. It starts on the next dispatch cycle",
    );
    await actions.getByRole("button", { name: "Pause", exact: true }).click();

    const note = page.getByTestId("paused-note");
    await expect(note).toContainText(/Paused by .+ \(was Waiting\)/);
    await expect(actions).toContainText(
      "Asked for a run. It starts ahead of the queue once this task is resumed.",
    );
    await expect(
      page.locator(".detail-layout").getByText("Paused").first(),
    ).toBeVisible();
    const resume = page.getByTestId("resume-task");
    await expect(resume).toBeVisible();
    await expect(
      actions.getByRole("button", { name: "Pause", exact: true }),
    ).toHaveCount(0);

    // Resume takes Pause's place in the column, the note directly above it.
    const noteBox = await note.boundingBox();
    const resumeBox = await resume.boundingBox();
    const columnBox = await actions.boundingBox();
    expect(resumeBox.y).toBeGreaterThan(noteBox.y);
    expect(resumeBox.x).toBeGreaterThanOrEqual(columnBox.x - 1);
    expect(resumeBox.x + resumeBox.width).toBeLessThanOrEqual(
      columnBox.x + columnBox.width + 1,
    );

    await resume.click();
    await expect(
      actions.getByRole("button", { name: "Pause", exact: true }),
    ).toBeVisible();
    await expect(note).toHaveCount(0);
    const after = await (await page.request.get(`/api/tasks/${id}`)).json();
    expect(after.state).toBe("queued");
    expect(after.paused).toBeUndefined();

    // Unstarted /session/new hides Pause in .task-chat-compose (6032bf747).
    await page.goto("/session/new");
    await expect(
      page
        .locator(".task-chat-compose")
        .getByRole("button", { name: "Pause", exact: true }),
    ).toHaveCount(0);
  } finally {
    await page.request.post(`/api/tasks/${id}/resume`);
    await page.request.post(`/api/tasks/${id}/close`, { data: {} });
  }
});
