import { test, expect } from "./fixtures.js";

// A merge-queue state that waits without the queue giving up
// (docs/test-plans/2026-10-04.md §4): waiting_github_approval, a clean
// pull request GitHub refused to merge for want of a review its rules
// require. `grain demo -merge-queue-states` seeds it through the store
// exactly as the queue writes it; nothing of grain's is mocked.
//
// What a person sees: the state's phrase on the task list row and in the
// task pane, the queue's own note naming what is being waited for, and no
// "Retry merge" -- the queue has given up on neither, so there is nothing
// to retry.
// Force merge stays on offer, as it is for any open pull request.
test.use({ demoArgs: ["-merge-queue-states"] });

const STATES = [
  {
    title: "Move the billing exporter onto the new schema",
    phrase: "Waiting for an approval GitHub requires",
    note: [
      "code owners @acme/platform, @bob",
      "1 approval still needed",
      "https://github.com/acme/widgets/pull/121/files",
    ],
  },
];

async function taskByTitle(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks.find((t) => t.title === title);
  expect(task, `seeded task "${title}"`).toBeTruthy();
  return task;
}

for (const s of STATES) {
  test(`${s.phrase}: the API, the task list row and the task pane agree`, async ({
    page,
  }) => {
    const task = await taskByTitle(page.request, s.title);
    // Not blocked: the flag the Retry merge button and the inbox's
    // "Merge blocked" section both read is unset.
    expect(task.mergeQueueBlockedAt ?? null).toBeNull();
    for (const want of s.note) expect(task.mergeQueueNote).toContain(want);

    await page.goto("/");
    const row = page
      .locator("tr, li, [role=row]", { hasText: s.title })
      .first();
    await expect(row).toBeVisible();
    await expect(row).toContainText(s.phrase);

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header h2")).toContainText(s.title);
    await expect(page.getByText(s.phrase).first()).toBeVisible();
    await expect(
      page.getByText(s.note[0], { exact: false }).first(),
    ).toBeVisible();
    // Any address the note names -- the review page an approval is
    // waited for on -- is a link, not text to copy out by hand.
    for (const url of s.note.filter((n) => n.startsWith("https://"))) {
      await expect(
        page.locator(`.detail-activity-note a[href="${url}"]`),
      ).toBeVisible();
    }
    await expect(
      page.getByRole("button", { name: "Retry merge", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Force merge", exact: true }),
    ).toBeVisible();
  });
}

// The inbox is where somebody goes to find what to do next, so the review
// page the approval wait names is a link there too, as it is in the task
// pane (docs/test-runs/2026-10-05-task-intake-inbox-lifecycle.md, bug 3).
test("the inbox's Approve on GitHub row links the review page its note names", async ({
  page,
}) => {
  const s = STATES[0];
  const url = s.note.find((n) => n.startsWith("https://"));
  await page.goto("/inbox");
  const group = page.locator(".inbox-group", {
    has: page.getByRole("heading", { name: "Approve on GitHub" }),
  });
  const item = group.locator(".inbox-item", { hasText: s.title });
  await expect(item).toBeVisible();
  await expect(
    item.locator(".inbox-note").getByRole("link", { name: url }),
  ).toHaveAttribute("href", url);
});
