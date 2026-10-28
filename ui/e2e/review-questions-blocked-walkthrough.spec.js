import { expect, test } from "./fixtures.js";

// docs/test-plans/2026-10-25.md §7, walked end to end over a demo of its
// own (docs/test-runs/2026-10-25-task-lifecycle-review-questions.md):
// the review-questions hold `grain demo -review-questions` seeds, answered
// from the page; the blocked oval clearing on the task pane once its
// dependency closes; and the auto overcommit bounds refusing what is out
// of range without sending it. The tests below change what they look at
// -- they answer the seeded question and close a seeded task -- so the
// flag is a file's worth of server nobody else shares.
test.use({ demoArgs: ["-review-questions"] });

async function taskTitled(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const t = tasks.find((x) => x.title === title);
  expect(t, `no task titled "${title}" in the demo store`).toBeTruthy();
  return t;
}

// Before -review-questions ran the review-questions reconciler, a reply
// was written and the change stayed held, awaiting the decision it had
// just been given, for as long as the demo ran.
test("answering the review's question from the inbox sends the change back to the queue", async ({
  page,
  request,
}) => {
  const held = await taskTitled(request, "Publish both daemon images");
  expect(held.state).toBe("awaiting_submit");

  await page.goto("/inbox");
  const group = page.locator(".inbox-group", {
    hasText: /review needs your decision/i,
  });
  const card = group
    .locator(".inbox-item", { hasText: "Publish both daemon images" })
    .first();
  // The question, where it is, and what the review recommends.
  await expect(card).toContainText("Makefile:41");
  await expect(card).toContainText(
    "Should the core build also publish the google image?",
  );
  await expect(card).toContainText("I recommend not");

  await card.getByText("Publish both daemon images").click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${held.id}$`));
  await page
    .getByPlaceholder("Reply...")
    .fill("Agreed: keep the core image core-only.");
  await page.getByRole("button", { name: "Comment", exact: true }).click();

  // The demo runs the reconciler every 2s.
  await expect
    .poll(async () => (await taskTitled(request, held.title)).state, {
      timeout: 15_000,
    })
    .toBe("queued");
  const after = await taskTitled(request, held.title);
  expect(after.autoMerge).toBe(true);
  expect(after.reviewQuestions || "").toBe("");
  await expect(page.locator(".detail-state > .badge")).toHaveClass(
    /\bbadge-queued\b/,
  );
  await expect(
    page.getByText(/The owner answered the open questions/),
  ).toBeVisible();

  await page.goto("/inbox");
  await expect(page.getByText(/review needs your decision/i)).toHaveCount(0);
});

// The board and list cases are board.spec.js's and tasks.spec.js's; this
// is the task pane, and the oval coming off once nothing blocks it --
// on the open page, from the store's own update rather than a reload.
test("the task pane's blocked oval clears to Waiting when its dependency closes", async ({
  page,
  request,
}) => {
  const blocked = await taskTitled(request, "Extract pagination query helper");
  expect(blocked.blocked).toBe(true);
  expect(blocked.blockedBy?.length).toBe(1);
  const blocker = blocked.blockedBy[0];

  await page.goto(`/tasks/${blocked.id}`);
  const badge = page.locator(".detail-state > .badge");
  await expect(badge).toHaveClass(/\bbadge-blocked\b/);
  await expect(badge).toHaveAttribute(
    "title",
    new RegExp(`^Blocked on: #${blocker} \\(`),
  );
  await expect(badge).toHaveText(/blocked/i);

  const res = await request.post(`/api/tasks/${blocker}/close`, { data: {} });
  expect(res.ok()).toBeTruthy();

  await expect(badge).toHaveClass(/\bbadge-queued\b/, { timeout: 15_000 });
  await expect(badge).toHaveAttribute("title", "Waiting");
});

// The auto controller's bounds used to drop -1 without a word, send 0.1
// (under the box's 0.25) and 150% (over 100%) to the daemon, and save the
// default for a half-typed "1e".
test("the auto overcommit bounds refuse out-of-range values by name without saving", async ({
  page,
}) => {
  const writes = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/settings") && r.method() === "PUT") {
      writes.push(r.postData());
    }
  });
  await page.goto("/settings/sandboxes");

  for (const [label, typed, message] of [
    [
      "Auto CPU minimum",
      "-1",
      "Auto CPU minimum must be at least 0.25; -1 was not saved.",
    ],
    [
      "Auto memory minimum",
      "0.1",
      "Auto memory minimum must be at least 0.25; 0.1 was not saved.",
    ],
    [
      "CPU pressure target (%)",
      "150",
      "CPU pressure target (%) cannot be more than 100; 150 was not saved.",
    ],
  ]) {
    const box = page.getByRole("spinbutton", { name: label, exact: true });
    const before = await box.inputValue();
    await box.fill(typed);
    await box.press("Tab");
    await expect(page.getByText(message).first()).toBeVisible();
    await expect(box).toHaveValue(before);
  }

  const max = page.getByRole("spinbutton", {
    name: "Auto CPU maximum",
    exact: true,
  });
  const maxBefore = await max.inputValue();
  await max.fill("");
  await max.pressSequentially("1e");
  await max.press("Tab");
  await expect(
    page
      .getByText(
        "Auto CPU maximum: what was typed is not a number, so it was not saved.",
      )
      .first(),
  ).toBeVisible();
  await expect(max).toHaveValue(maxBefore);
  expect(writes).toEqual([]);
});
