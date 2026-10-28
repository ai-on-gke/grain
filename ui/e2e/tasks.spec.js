import { test, expect } from "./fixtures.js";

// The one suite in this package that boots a real pkg/ui.Server (over a
// real embedded SQLite store) and drives the real *built* frontend --
// the `grain demo` fixtures.js starts for this worker -- through an
// actual Chromium instead of jsdom against a component tree in
// isolation.
// Every other *.test.jsx/*.test.js file under ../src is the latter; none
// of them boot a server or a browser, which left routing, real
// fetch/CORS behavior, layout and the server's actual wire format
// unverified by anything short of a human clicking around
// (bwsalmon/agents#415).
//
// `grain demo`'s own seedDemo (pkg/demo/demo.go) has already put a
// fixed set of tasks, one in every state including "proposed", into the
// store by the time these run (pkg/demo/demo_test.go's
// TestSeedDemoCoversEveryState asserts on that directly); these tests
// read a couple of the seeded titles rather than duplicating that
// assertion, and otherwise file and approve their own tasks so each
// test's outcome doesn't depend on what a previous test in the same run
// left behind.

test("lists the seeded tasks from a real server", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByText("Bump the Go toolchain to 1.24")).toBeVisible();
  await expect(page.getByText("Rotate the Gemini signing key")).toBeVisible();
  await expect(page.locator(".task-row").first()).toBeVisible();

  // The seeded blocked subtask shows the blocked state oval centred on its
  // row, with its wait target on the badge's title/aria-label and no extra
  // "Blocked" chip beside it.
  const blockedRow = page.locator(".task-row", {
    hasText: "Extract pagination query helper",
  });
  const badge = blockedRow.locator(".badge");
  await expect(badge).toHaveClass(/\bbadge-blocked\b/);
  await expect(badge).toHaveAttribute("title", /^Blocked on: #\d+ \(/);
  await expect(badge).toHaveAttribute("aria-label", /^Blocked on: #\d+ \(/);
  // The whole label: the row's sub-case chip beside it reads
  // "blocked on task" (ui.Task.Reason).
  await expect(
    blockedRow.locator(".MuiChip-root", { hasText: /^Blocked$/ }),
  ).toHaveCount(0);
  const badgeBox = await badge.boundingBox();
  const titleBox = await blockedRow.locator(".task-title").boundingBox();
  expect(
    Math.abs(
      badgeBox.y + badgeBox.height / 2 - (titleBox.y + titleBox.height / 2),
    ),
  ).toBeLessThan(3);
});

test("files a new task through the New task overlay", async ({ page }) => {
  await page.goto("/");
  const title = `E2E new task ${Date.now()}`;

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await page.getByLabel("Title").fill(title);
  // No repo is picked from this top-level "+ New task" button, so the
  // target repo list's own "No repo" option has to be chosen before
  // Create task will even enable (bwsalmon/agents#614, grain/task-85).
  await page.getByLabel(/^Repos/).selectOption("none");
  // "Queue immediately" starts checked, from the deployment default that
  // is on unless an operator turns it off (bwsalmon/agents#612), so it is
  // unchecked here to file a proposal rather than a queued task -- the
  // next test approves one of those.
  await page.getByLabel(/Queue immediately/).uncheck();
  await page.getByRole("button", { name: "Create task" }).click();

  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);
  const row = page.locator(".task-row", { hasText: title });
  await expect(row).toBeVisible();
  await expect(row.locator(".badge-proposed")).toBeVisible();
});

// grain/task-91, grain/task-175: a task's own page carries the button
// that shows the whole prompt its agent was handed -- the task list's
// rows no longer do. The seeded running task is the one with a recorded
// prompt (pkg/demo/demo.go builds it with the same
// orchestrator.BuildPrompt a real dispatch uses), so this is the one
// place the button, the route and the recorded prompt are exercised
// together against a real server.
test("shows the full prompt from the task's own page", async ({ page }) => {
  await page.goto("/");

  await page
    .locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" })
    .click();
  await expect(page.locator(".detail-header")).toBeVisible();
  await page.getByRole("button", { name: "Prompt" }).click();

  // The prompt opens as its own pane over the task's, so it is picked
  // out by its own heading rather than by being the only dialog on
  // screen -- the task page is still behind it.
  const prompt = page.locator(".MuiDialog-paper", {
    has: page.getByRole("heading", { name: /^Prompt for/ }),
  });
  await expect(
    prompt.getByText(/Push your change to a new branch named/),
  ).toBeVisible();
});

// grain/task-94: a task opens into the whole content area beside the
// sidebar rather than a box floating in the middle of it. Layout is the
// one thing jsdom cannot answer for -- Overlay.test.jsx can only check
// which classes MUI put on the paper -- so where the pane actually lands
// on screen is measured here, against a real browser.
test("opens a task into the pane beside the sidebar", async ({ page }) => {
  await page.goto("/");
  await page.locator(".task-row").first().click();

  const pane = page.locator(".MuiDialog-paper");
  await expect(pane).toBeVisible();

  const sidebar = await page.locator("aside").boundingBox();
  const box = await pane.boundingBox();
  const viewport = page.viewportSize();

  // Flush against the sidebar on the left, the viewport's edge on the
  // right, and its full height -- one pixel of tolerance for a fractional
  // device pixel ratio, not for a pane that landed somewhere else.
  expect(Math.abs(box.x - (sidebar.x + sidebar.width))).toBeLessThanOrEqual(1);
  expect(Math.abs(box.x + box.width - viewport.width)).toBeLessThanOrEqual(1);
  expect(Math.abs(box.height - viewport.height)).toBeLessThanOrEqual(1);
});

// grain/task-1420: a row opens from the keyboard too, not only from a
// click. Its title is the link it opens by, the tab stop just after the
// checkbox, with a ring a sighted keyboard user can see. The row around
// it is no control of its own (grain/task-1582): it holds a move handle
// and a checkbox, and a row that was itself a link announced all of them
// as one run-on link -- so the title is the only link by its name.
test("opens a task from the keyboard", async ({ page }) => {
  await page.goto("/");
  // The first row whose title no other row shares: specs that ran before
  // this one on the same demo can have filed two tasks of one title.
  const rows = page.locator(".task-row");
  await expect(rows.first()).toBeVisible();
  const titles = (await rows.locator(".task-title").allTextContents()).map(
    (t) => t.trim(),
  );
  const unique = titles.findIndex(
    (t) => titles.filter((other) => other === t).length === 1,
  );
  expect(unique, `a task title no other row shares: ${titles}`).not.toBe(-1);
  const row = rows.nth(unique);
  const id = (await row.locator(".task-number").textContent()).trim();
  const title = (await row.locator(".task-title").textContent()).trim();
  await expect(row).not.toHaveAttribute("role");
  await expect(row).not.toHaveAttribute("tabindex");
  const link = page.getByRole("link", { name: title, exact: true });
  await expect(link).toHaveCount(1);

  await row.getByRole("checkbox", { name: `Select ${id}` }).focus();
  await page.keyboard.press("Tab");
  await expect(link).toBeFocused();
  expect(
    await link.evaluate((el) => getComputedStyle(el).outlineStyle),
  ).not.toBe("none");

  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(new RegExp(`/tasks/${id}$`));
  await expect(page.locator(".MuiDialog-paper")).toBeVisible();
});

// grain/task-177: a pane and a repo's own page are both destinations
// with their own URL, so both leave the same way -- a back button in the
// top-left corner, not an X in the top-right that only the panes had.
// Which corner a button is actually in is a question about where it
// lands on screen, so it is asked here rather than of jsdom.
test("leaves a task's pane by a back button in its top-left corner", async ({
  page,
}) => {
  await page.goto("/");
  await page.locator(".task-row").first().click();
  await expect(page.locator(".detail-header h2")).toBeVisible();

  const paneBox = await page.locator(".MuiDialog-paper").boundingBox();
  const back = page.getByRole("button", { name: "← Back" });
  const backBox = await back.boundingBox();

  // In the pane's own left half and its own top eighth: the corner, not
  // merely somewhere on the page.
  expect(backBox.x - paneBox.x).toBeLessThan(paneBox.width / 2);
  expect(backBox.y - paneBox.y).toBeLessThan(paneBox.height / 8);

  await back.click();
  await expect(page.locator(".detail-header h2")).toHaveCount(0);
  await expect(page).toHaveURL(/\/$/);
});

test("approves a proposed task from its detail view", async ({ page }) => {
  await page.goto("/");
  const title = `E2E approve me ${Date.now()}`;

  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");
  // Unchecked for the same reason as the test above: there is nothing to
  // approve unless this files as a proposal.
  await page.getByLabel(/Queue immediately/).uncheck();
  await page.getByRole("button", { name: "Create task" }).click();

  await page.locator(".task-row", { hasText: title }).click();
  const heading = page.locator(".detail-header h2", { hasText: title });
  await expect(heading).toBeVisible();
  await expect(page.locator(".detail-state .badge-proposed")).toBeVisible();

  await page.getByRole("button", { name: "Approve" }).click();

  await expect(page.locator(".detail-state .badge-queued")).toBeVisible();
  await expect(
    page.locator(".task-row", { hasText: title }).locator(".badge-queued"),
  ).toBeVisible();
});

// The one flow that only a real server can answer for: the bulk edit
// form (BulkEditOverlay) sends one PATCH per selected task, and what it
// sends has to be a body ui.Client.UpdateTask accepts -- a shape jsdom
// asserts on as a string and nothing there can refuse. Two tasks filed
// with no repo at all, given one in a single gesture, is also the plain
// case for the pane: a field the selection agrees on (both empty),
// changed everywhere at once.
test("edits a whole selection through the bulk edit form", async ({ page }) => {
  await page.goto("/");
  const stamp = Date.now();
  const titles = [1, 2].map((n) => `E2E bulk edit ${n} ${stamp}`);

  for (const title of titles) {
    await page.getByRole("button", { name: "+ New task" }).click();
    await page.getByLabel("Title").fill(title);
    await page.getByLabel(/^Repos/).selectOption("none");
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.locator(".task-row", { hasText: title })).toBeVisible();
  }

  for (const title of titles) {
    await page
      .locator(".task-row", { hasText: title })
      .getByRole("checkbox")
      .check();
  }
  await expect(page.getByText("2 selected")).toBeVisible();

  await page.getByRole("button", { name: "Bulk edit…" }).click();
  const form = page.getByRole("dialog");
  await expect(form.getByRole("heading", { name: "Bulk edit" })).toBeVisible();
  await form.getByRole("checkbox", { name: "Repo" }).check();
  // Pressing Escape while a field is ticked asks before discarding the edit.
  page.once("dialog", (d) => d.dismiss());
  await page.keyboard.press("Escape");
  await expect(form.getByRole("heading", { name: "Bulk edit" })).toBeVisible();
  await form.locator(".repo-field select").selectOption("acme/widgets");
  await form.getByRole("checkbox", { name: /^Base branch/ }).check();
  await form.getByRole("textbox").fill("release/2");
  await form.getByRole("button", { name: "Apply to 2 tasks" }).click();

  // The bar clears itself once every PATCH has landed (App's runBatch),
  // which is the screen's own way of saying the whole batch went
  // through.
  await expect(page.getByText("2 selected")).toHaveCount(0);

  for (const title of titles) {
    await page.locator(".task-row", { hasText: title }).click();
    await expect(page.locator(".detail-header h2")).toBeVisible();
    await expect(page.locator(".repo-field select")).toHaveValue(
      "acme/widgets",
    );
    await expect(page.getByLabel("Base branch")).toHaveValue("release/2");
    await page.getByRole("button", { name: "← Back" }).click();
  }
});
