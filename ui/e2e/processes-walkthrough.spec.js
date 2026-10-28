import path from "node:path";
import { expect, test } from "./fixtures.js";

// The Processes page (docs/design/settings-panel.md §5, stage b) against
// the real `grain demo` server. -sensitive-hold adds a declared trigger
// to the demo's own schedules, templates, suites and acme/widgets
// declarations, so every kind but workflow schedules has a row to open:
// a workflow schedule is only ever declared in a repository's
// `.grain/workflows`, which the plain demo does not read (that kind is
// walked by workflow-schedules-candidate-walkthrough.spec.js).
//
// Screenshots are written only when PROCESSES_SCREENSHOTS names a
// directory, so a CI run leaves nothing in the tree.
test.use({ demoArgs: ["-sensitive-hold"] });

const KINDS = [
  ["Schedules", "schedule"],
  ["Workflow schedules", "workflow"],
  ["Templates", "template"],
  ["Suites", "suite"],
  ["Triggers", "trigger"],
];

async function shot(page, name) {
  const dir = process.env.PROCESSES_SCREENSHOTS;
  if (!dir) return;
  await page.screenshot({ path: path.join(dir, `${name}.png`) });
}

test.describe("Processes walkthrough", () => {
  test("a Schedules pin from before the stage shows as Processes", async ({
    page,
  }) => {
    await page.goto("/");
    // A browser from before stage (c) too: its list is under the old
    // per-user key, and storageMigration.js has not run on it yet.
    await page.evaluate(() => {
      localStorage.removeItem("grain.storageVersion");
      localStorage.removeItem("grain.pinned");
      localStorage.setItem("grain.username", "walker");
      localStorage.setItem(
        "grain.pinnedNav.walker.default",
        JSON.stringify([
          { kind: "view", id: "inbox" },
          { kind: "view", id: "schedules", color: "#ef4444" },
          { kind: "view", id: "suites" },
        ]),
      );
    });
    await page.reload();

    const rail = page.locator("aside");
    // The pin and the rail's own entry; nothing by the old names.
    await expect(rail.getByRole("button", { name: /^Processes/ })).toHaveCount(
      2,
    );
    for (const old of [/^Schedules/, /^Templates/, /^Suites/]) {
      await expect(rail.getByRole("button", { name: old })).toHaveCount(0);
    }
    const stored = await page.evaluate(() =>
      JSON.parse(localStorage.getItem("grain.pinned")),
    );
    expect(stored).toEqual([
      { kind: "view", id: "inbox" },
      { kind: "view", id: "processes", color: "#ef4444" },
    ]);
    await rail
      .getByRole("button", { name: /^Processes/ })
      .first()
      .click();
    await expect(page).toHaveURL(/\/processes$/);
  });

  test("redirects the old paths into Processes", async ({ page }) => {
    const schedules = await (await page.request.get("/api/schedules")).json();
    const id = schedules[0].id;
    await page.goto(`/schedules/${encodeURIComponent(id)}`);
    await expect(page).toHaveURL(
      new RegExp(`/processes/schedules/${id.replace(/[:]/g, "(:|%3A)")}$`),
    );
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();

    await page.goto("/templates");
    await expect(page).toHaveURL(/\/processes\?kind=template$/);
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // A real run: the prefix swap lands on SuiteRunPane for it.
    const suite = (await (await page.request.get("/api/suites")).json())[0];
    const started = await page.request.post("/api/suite-runs", {
      data: { suiteId: suite.id, repo: "acme/widgets", base: "main" },
    });
    expect(started.ok()).toBe(true);
    const run = await started.json();
    await page.goto(`/suites/runs/${run.id}`);
    await expect(page).toHaveURL(
      new RegExp(`/processes/suites/runs/${run.id}$`),
    );
    await expect(page.getByRole("heading", { name: suite.name })).toBeVisible();
    await expect(page.getByRole("heading", { name: /Pass 1/i })).toBeVisible();
    await shot(page, "processes-suite-run");

    // A run that does not exist says so over the list.
    await page.goto("/suites/runs/404");
    await expect(page).toHaveURL(/\/processes$/);
    await expect(page.getByText('no suite run "404"')).toBeVisible();
  });

  test("filters by each kind, opens one of each, and creates a schedule and a template", async ({
    page,
  }) => {
    await page.goto("/processes");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();
    const filter = page.getByTestId("process-kind-filter");
    const rows = page.locator(".process-row[data-kind]");
    await expect(rows.first()).toBeVisible();
    await shot(page, "processes-all");

    for (const [label, kind] of KINDS) {
      await filter
        .getByRole("button", { name: new RegExp(`^${label} `) })
        .click();
      await expect(page).toHaveURL(new RegExp(`/processes\\?kind=${kind}$`));
      if (kind !== "workflow") {
        await expect(
          page.locator(`.process-row[data-kind="${kind}"]`).first(),
        ).toBeVisible();
      }
      const kinds = await rows.evaluateAll((els) =>
        els.map((el) => el.dataset.kind),
      );
      expect(kinds.every((k) => k === kind)).toBe(true);
      if (kind !== "workflow") expect(kinds.length).toBeGreaterThan(0);
      if (kind === "trigger") await shot(page, "processes-triggers");
      await filter.getByRole("button", { name: /^All / }).click();
      await expect(page).toHaveURL(/\/processes$/);
    }

    // One of each: each row opens the overlay it opened on its old page.
    for (const [kind, heading] of [
      ["schedule", "Edit schedule"],
      ["template", "Edit template"],
      ["suite", "Edit suite"],
    ]) {
      await page
        .locator(`.process-row[data-kind="${kind}"] a.row-link`)
        .first()
        .click();
      await expect(page.getByRole("heading", { name: heading })).toBeVisible();
      await expect(page).toHaveURL(new RegExp(`/processes/${kind}s/.+`));
      if (kind === "schedule") await shot(page, "processes-open-schedule");
      await page.keyboard.press("Escape");
      await expect(page.getByRole("heading", { name: heading })).toHaveCount(0);
      await expect(page).toHaveURL(/\/processes$/);
    }
    // A trigger opens the repository that declares it, on Advanced.
    await page
      .locator('.process-row[data-kind="trigger"] a.row-link')
      .first()
      .click();
    await expect(page).toHaveURL(/\/repos\/[^/]+\/[^/]+\/settings\/advanced$/);
    await page.goBack();
    await expect(page).toHaveURL(/\/processes$/);

    // New asks for the kind, then opens that kind's own form.
    const title = `Processes walkthrough ${Date.now().toString(36)}`;
    await page.getByRole("button", { name: "+ New", exact: true }).click();
    const menu = page.getByTestId("new-process-kind");
    await expect(menu.getByRole("menuitem")).toHaveText([
      "Schedule",
      "Template",
      "Suite",
    ]);
    await expect(menu).toContainText("declared in a");
    await shot(page, "processes-new-menu");
    await menu.getByRole("menuitem", { name: "Schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toBeVisible();
    await page.getByLabel("Title", { exact: true }).fill(title);
    await page.locator("select[name='repo']").selectOption("acme/widgets");
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);
    await expect(
      page.locator('.process-row[data-kind="schedule"]', { hasText: title }),
    ).toBeVisible();

    // New → Template opens the template form, and the saved template is
    // a row of the table.
    const templateName = `walkthrough-${Date.now().toString(36)}`;
    await page.getByRole("button", { name: "+ New", exact: true }).click();
    await menu.getByRole("menuitem", { name: "Template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();
    await page.getByLabel("Name", { exact: true }).fill(templateName);
    await page
      .getByLabel("Task title", { exact: true })
      .fill("Walk the Processes page");
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);
    const created = page.locator('.process-row[data-kind="template"]', {
      hasText: templateName,
    });
    await expect(created).toBeVisible();
    await expect(created).toContainText("Walk the Processes page");
    await shot(page, "processes-new-template");
    const stored = await (await page.request.get("/api/templates")).json();
    expect(stored.map((t) => t.name)).toContain(templateName);

    // Narrowed to suites, New skips its menu.
    await filter.getByRole("button", { name: /^Suites / }).click();
    await page.getByRole("button", { name: "+ New suite" }).click();
    await expect(
      page.getByRole("heading", { name: "New suite" }),
    ).toBeVisible();
    await page.keyboard.press("Escape");
  });

  test("creates an interactive template, previews approval from the template in Use…, and files a live chat", async ({
    page,
  }) => {
    await page.goto("/processes?kind=template");
    await page.getByRole("button", { name: "+ New template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();
    const interactiveName = `interactive-${Date.now().toString(36)}`;
    await page.getByLabel("Name", { exact: true }).fill(interactiveName);
    await page
      .getByLabel("Task title", { exact: true })
      .fill("Interactive incident triage");
    await page.locator('select[name="repo"]').selectOption("acme/widgets");
    await page.getByLabel("Interactive (agent can chat with you)").check();
    await page.getByRole("button", { name: "Add template" }).click();

    // Open Use… on a non-interactive template first, untick Approved, then
    // switch the picker to the interactive template: What this files must
    // still report "approved when filed" from the template rather than
    // "waits for approval".
    await page
      .locator('.process-row[data-kind="template"]')
      .first()
      .getByRole("button", { name: "Use…" })
      .click();
    const useDialog = page.getByRole("dialog", {
      name: "File a task from a template",
    });
    await expect(useDialog).toBeVisible();
    const approvedCheckbox = useDialog.getByLabel(/Approved/i);
    if (await approvedCheckbox.isChecked()) {
      await approvedCheckbox.uncheck();
    }
    await useDialog.getByLabel("Template").click();
    await page
      .getByRole("option", { name: new RegExp(interactiveName) })
      .click();

    await expect(useDialog.getByLabel(/Approved/i)).toHaveCount(0);
    await expect(
      useDialog.getByText(/This template files a live chat/),
    ).toBeVisible();
    const preview = useDialog.locator("#composition-preview");
    await expect(preview).toContainText("approved when filed");
    await expect(preview).not.toContainText("waits for approval");

    await useDialog.getByRole("button", { name: "Create task" }).click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await expect(page.locator(".task-chat")).toBeVisible();
  });
});
