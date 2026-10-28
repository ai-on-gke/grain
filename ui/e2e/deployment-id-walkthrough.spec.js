import { test, expect } from "./fixtures.js";

// Walkthrough: the deployment id (docs/test-plans/2026-10-04.md §5, step 3;
// 3e4823e71). A system admin names the deployment on System -> Agents &
// Task Branches (grain/task-3091; it used to sit at the foot of a team's
// Models tab, where an operator could not find it), puts
// {deployment} in the task branch template, and files a task: the new
// task's branch carries the id, a task filed before keeps the branch it
// had, and ids that could not sit in a branch name as one path segment are
// refused where they were typed.
//
// The id and the template are deployment-wide, so this file takes a demo of
// its own -- a distinct demoArgs is a distinct worker -- rather than
// renaming the branches of whatever else shares the default one.
test.use({ demoArgs: ["-addr", "127.0.0.1:0"] });

async function openTaskBranchSettings(page) {
  await page.goto("/settings/agents");
  await expect(
    page.getByRole("heading", { name: "Settings", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Agents", level: 1 }),
  ).toBeVisible();
  await expect(page.getByLabel("Deployment ID")).toBeVisible();
}

async function taskByTitle(page, title) {
  const res = await page.request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  return tasks.find((t) => t.title === title);
}

test("a deployment id names new tasks' branches and leaves existing ones alone", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const existing = await taskByTitle(page, "Add a dark mode toggle");
  expect(existing.branch).toBeTruthy();
  const existingBranch = existing.branch;

  await openTaskBranchSettings(page);
  const idBox = page.getByLabel("Deployment ID");
  const defaultID = await idBox.getAttribute("placeholder");
  expect(defaultID).toMatch(/^[0-9a-f]{8}$/);

  // Refusals: not lowercase, and one character over the limit. Neither is
  // stored, and the box goes back to what was saved.
  for (const bad of ["Bad_ID", "a".repeat(33)]) {
    await idBox.fill(bad);
    await idBox.press("Enter");
    // Said twice: in the section header's save state and in the error.
    await expect(
      page
        .getByText(/Couldn't save Deployment ID|Deployment ID may contain/)
        .first(),
    ).toBeVisible();
    const settings = await (await page.request.get("/api/settings")).json();
    expect(settings.deploymentId).toBe("");
    await expect(idBox).toHaveValue("");
  }

  await idBox.fill("blue");
  await idBox.press("Enter");
  await expect
    .poll(
      async () =>
        (await (await page.request.get("/api/settings")).json()).deploymentId,
    )
    .toBe("blue");

  // The demo plays a deployment from before the unique default, still on
  // grain/task-{id}, so the section offers the switch in one line; taking
  // it stores the template a new deployment starts with.
  const template = page.getByLabel("Branch name template");
  await expect(template).toHaveValue("");
  const suggestion = page.getByTestId("task-branch-suggestion");
  await expect(suggestion).toContainText("grain/{deployment}/task-{id}-{slug}");
  await suggestion.getByRole("button", { name: "Use it" }).click();
  await expect(suggestion).toHaveCount(0);
  await expect(template).toHaveValue("grain/{deployment}/task-{id}-{slug}");
  await expect
    .poll(
      async () =>
        (await (await page.request.get("/api/settings")).json())
          .taskBranchTemplate,
    )
    .toBe("grain/{deployment}/task-{id}-{slug}");
  await page.goto("/");

  // File a task through the New task overlay, the way anyone would.
  const title = `Deployment id walkthrough ${Date.now()}`;
  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  await page.getByLabel(/Queue immediately/).uncheck();
  await page.getByRole("button", { name: "Create task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);

  const created = await taskByTitle(page, title);
  // A few words of the title follow the number ({slug}, grain/task-56);
  // the timestamp is past MaxBranchSlugLen.
  const branch = `grain/blue/task-${created.id}-deployment-id-walkthrough`;
  expect(created.branch).toBe(branch);
  const one = await (await page.request.get(`/api/tasks/${created.id}`)).json();
  expect(one.branch).toBe(branch);

  // The task's own pane names its branch only once something has been
  // pushed to it (DetailOverlay's compare link), so a task that has never
  // run is read through the API above (docs/test-runs/2026-10-04-release-
  // engineering.md, finding 3).

  // The task filed before the change keeps its branch, after a reload too.
  await page.reload();
  expect((await taskByTitle(page, "Add a dark mode toggle")).branch).toBe(
    existingBranch,
  );
});

// The id also names the gcp server's service accounts
// (docs/gcp-key-permission-sets.md, grain/task-434): the field says so,
// and says a rename keeps the existing accounts, without the longer help
// spilling out of its column on a phone or a desktop.
for (const width of [390, 1280]) {
  test(`the deployment id's help names GCP service accounts at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    // A phone's layout has no "Settings" heading beside the page's own.
    await page.goto("/settings/agents");
    const idBox = page.getByLabel("Deployment ID");
    await expect(idBox).toBeVisible();
    const help = page.getByText(/GCP service accounts the gcp server creates/);
    await expect(help).toBeVisible();
    await expect(help).toContainText("Existing service accounts keep working");
    const box = await help.boundingBox();
    const field = await idBox.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    // Under its own field, starting where the field does.
    expect(box.y).toBeGreaterThan(field.y);
    expect(Math.abs(box.x - field.x)).toBeLessThan(24);
  });
}
