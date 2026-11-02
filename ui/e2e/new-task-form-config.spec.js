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

// The New Task form as settings/new-task-form.yml lays it out
// (design/design/config-repo-settings.design.md §6), in a real browser: with no
// configuration its fields keep their places around "Advanced options",
// and a configuration moves a field up the form, tucks another under
// Advanced options and leaves a hidden one out. /api/config is answered
// with the demo's own config plus a newTaskForm, the way the daemon
// serves a deployment whose config repository carries the file.

import { expect, test } from "./fixtures.js";

async function serveForm(page, newTaskForm) {
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, newTaskForm } });
  });
}

async function openForm(page) {
  await page.goto("/new");
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
}

const top = async (locator) => (await locator.boundingBox()).y;

test("the unconfigured form keeps Advanced options between Depends on and Queue immediately", async ({
  page,
}) => {
  await openForm(page);
  const dialog = page.getByRole("dialog");
  const depends = dialog.getByText("Depends on", { exact: false }).first();
  const advanced = dialog.getByRole("button", { name: "Advanced options" });
  const queue = dialog.getByLabel(/Queue immediately/);
  expect(await top(depends)).toBeLessThan(await top(advanced));
  expect(await top(advanced)).toBeLessThan(await top(queue));
  await expect(dialog.getByLabel(/Auto-merge once checks pass/)).toBeVisible();
});

test("a configured form moves, tucks away and hides fields", async ({
  page,
}) => {
  await serveForm(page, {
    default: {
      fields: {
        networkAccess: { visibility: "shown", order: 15 },
        approved: { visibility: "more", label: "File it approved" },
        autoMerge: { visibility: "hidden", default: true },
        title: { label: "Summary" },
      },
    },
  });
  await openForm(page);
  const dialog = page.getByRole("dialog");
  const title = dialog.getByLabel("Summary");
  const network = dialog.getByLabel("Network access");
  const description = dialog.getByLabel("Description");
  await expect(network).toBeVisible();
  // Network access, at order 15, sits between the title (10) and the
  // description (20), and spans the same column as both.
  const [t, n, d] = await Promise.all(
    [title, network, description].map((l) => l.boundingBox()),
  );
  expect(t.y + t.height).toBeLessThanOrEqual(n.y);
  expect(n.y + n.height).toBeLessThanOrEqual(d.y);
  expect(Math.abs(n.x - d.x)).toBeLessThan(20);
  await expect(dialog.getByLabel(/Auto-merge/)).toHaveCount(0);
  await expect(dialog.getByLabel("File it approved")).toBeHidden();
  await dialog.getByRole("button", { name: "Advanced options" }).click();
  await expect(dialog.getByLabel("File it approved")).toBeVisible();
});

// A layer's default follows the repo picked until somebody edits the
// field: switching repos re-applies the repo layer's default over the
// deployment's, and an edited value stays put (formEdited).
test("a repo switch re-applies layer defaults only to fields nobody touched", async ({
  page,
}) => {
  await serveForm(page, {
    default: {
      fields: {
        promptExtension: {
          visibility: "shown",
          order: 22,
          label: "Extension (default layer)",
          default: "from the default layer",
        },
      },
    },
    repos: {
      "acme/widgets": {
        fields: {
          promptExtension: {
            label: "Extension (widgets layer)",
            default: "from the widgets layer",
          },
        },
      },
    },
  });
  await openForm(page);
  const dialog = page.getByRole("dialog");
  const repos = dialog.getByLabel(/^Repos/);
  await repos.selectOption("none");
  const ext = dialog.locator("textarea[name=promptExtension]");
  await expect(dialog.getByLabel("Extension (default layer)")).toBeVisible();
  await expect(ext).toHaveValue("from the default layer");
  await repos.selectOption("acme/widgets");
  await expect(dialog.getByLabel("Extension (widgets layer)")).toBeVisible();
  await expect(ext).toHaveValue("from the widgets layer");
  await repos.selectOption("none");
  await expect(ext).toHaveValue("from the default layer");
  await ext.fill("typed by a person");
  await repos.selectOption("acme/widgets");
  await expect(dialog.getByLabel("Extension (widgets layer)")).toBeVisible();
  await expect(ext).toHaveValue("typed by a person");
  await dialog.getByRole("button", { name: "Discard draft" }).click();
});

// "New task" on a board's page is the board's form: its layer of
// settings/new-task-form.yml applies, and the filing names the board so
// the server applies that layer's hidden defaults too (2026-11-01
// walkthrough: App never passed the board, so no board layer reached
// the browser).
test("New task opened on a board takes the board's layer and files its boardId", async ({
  page,
}) => {
  await serveForm(page, {
    default: {
      fields: {
        networkAccess: {
          visibility: "shown",
          order: 15,
          label: "Network (default layer)",
        },
      },
    },
    boards: {
      "board-1": {
        fields: { networkAccess: { label: "Network (board layer)" } },
      },
    },
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/boards/board-1");
  await page.getByRole("button", { name: "+ New task" }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(dialog.getByLabel("Network (board layer)")).toBeVisible();
  await expect(dialog.getByLabel("Network (default layer)")).toHaveCount(0);
  let filed = null;
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    filed = route.request().postDataJSON();
    await route.fulfill({
      status: 400,
      json: { error: "stopped by the test" },
    });
  });
  await dialog.getByLabel(/^Title/).fill(`Board layer ${Date.now()}`);
  await dialog.getByLabel(/^Repos/).selectOption("none");
  await dialog.getByRole("button", { name: "Create task" }).click();
  await expect.poll(() => filed?.boardId).toBe("board-1");
  await dialog.getByRole("button", { name: "Discard draft" }).click();
});

// A template's form: "New task from this template" on the template's
// page opens New Task on it, its layer of settings/new-task-form.yml
// labels, shows and hides fields, and the Template picker switches
// layers the way a repo switch does -- re-applying defaults only to
// fields nobody has touched. The filing names the template, and with
// the title left blank the server takes the template's own
// (ui.Client.fromTemplate); a hidden field still files the layer's
// default (2026-11-01 walkthrough: nothing in the browser set
// templateId, so no templates layer was reachable).
test("New task from a template takes its layer, switches layers, and files its templateId", async ({
  page,
}) => {
  const release = {
    id: "e2e-release",
    name: "E2E release",
    title: "Cut the release",
    description: "Cut it.",
    capabilities: [],
    reads: [],
    writes: [],
    autoMerge: false,
    createdAt: new Date().toISOString(),
  };
  await page.route("**/api/templates", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: [...body, release] });
  });
  await serveForm(page, {
    default: {
      fields: {
        promptExtension: {
          visibility: "shown",
          order: 22,
          label: "Extension (default layer)",
          default: "from the default layer",
        },
      },
    },
    templates: {
      "e2e-release": {
        fields: {
          promptExtension: {
            label: "Extension (release layer)",
            default: "from the release layer",
          },
          networkAccess: { visibility: "hidden", default: "none" },
        },
      },
    },
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/processes/templates/e2e-release");
  await page
    .getByRole("button", { name: "New task from this template" })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "New task" })).toBeVisible();
  const picker = dialog.getByRole("combobox", { name: "Template" });
  await expect(picker).toContainText("E2E release");
  // The picker sits at the top of the form, above the title it fills.
  expect(await top(picker)).toBeLessThan(
    await top(dialog.getByLabel(/^Title/)),
  );
  const ext = dialog.locator("textarea[name=promptExtension]");
  await expect(dialog.getByLabel("Extension (release layer)")).toBeVisible();
  await expect(ext).toHaveValue("from the release layer");
  await dialog.getByRole("button", { name: "Advanced options" }).click();
  await expect(dialog.getByLabel("Network access")).toHaveCount(0);

  const pick = async (name) => {
    await picker.click();
    await page.getByRole("option", { name }).click();
  };
  // Untouched, the field follows the template picked...
  await pick("No template");
  await expect(dialog.getByLabel("Extension (default layer)")).toBeVisible();
  await expect(ext).toHaveValue("from the default layer");
  await expect(dialog.getByLabel("Network access")).toBeVisible();
  await pick("E2E release");
  await expect(ext).toHaveValue("from the release layer");
  // ...and once edited, it keeps what was typed through a switch.
  await ext.fill("typed by a person");
  await pick("No template");
  await expect(dialog.getByLabel("Extension (default layer)")).toBeVisible();
  await expect(ext).toHaveValue("typed by a person");
  await pick("E2E release");

  let filed = null;
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    filed = route.request().postDataJSON();
    await route.fulfill({
      status: 400,
      json: { error: "stopped by the test" },
    });
  });
  // A blank title is the template's to fill.
  await expect(dialog.getByLabel(/^Title/)).toHaveAttribute(
    "placeholder",
    "Cut the release",
  );
  await dialog.getByLabel(/^Repos/).selectOption("none");
  await dialog.getByRole("button", { name: "Create task" }).click();
  await expect.poll(() => filed?.templateId).toBe("e2e-release");
  expect(filed.title).toBe("");
  expect(filed.promptExtension).toBe("typed by a person");
  expect(filed.networkAccess).toBe("none");
  await dialog.getByRole("button", { name: "Discard draft" }).click();
});

// A /new link's values for custom fields the form tucks away or hides
// are still said: the notice sits at the top of the form rather than
// inside a collapsed "Advanced options", and a hidden field's values are
// left out -- it files its default -- rather than filed out of sight.
test("a /new link names what a tucked-away or hidden custom field leaves out", async ({
  page,
}) => {
  await serveForm(page, {
    default: { fields: { customFields: { visibility: "more" } } },
  });
  await page.goto("/new?field.priority=bogus");
  const dialog = page.getByRole("dialog");
  const notice = dialog.getByTestId("unfiled-link-values");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("“bogus” is not one of its values");
  const title = await dialog.getByLabel(/^Title/).boundingBox();
  expect((await notice.boundingBox()).y).toBeLessThan(title.y);
  await page.unroute("**/api/config");

  await serveForm(page, {
    default: { fields: { customFields: { visibility: "hidden" } } },
  });
  await page.goto("/new?field.priority=high");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText(
    "Priority: “high” is in a field this form hides",
  );
  let filed = null;
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    filed = route.request().postDataJSON();
    await route.fulfill({
      status: 400,
      json: { error: "stopped by the test" },
    });
  });
  await dialog.getByLabel(/^Title/).fill(`Hidden link value ${Date.now()}`);
  await dialog.getByLabel(/^Repos/).selectOption("none");
  await dialog.getByRole("button", { name: "Create task" }).click();
  await expect.poll(() => filed !== null).toBe(true);
  expect(filed.customFields?.priority ?? "").not.toBe("high");
  await dialog.getByRole("button", { name: "Discard draft" }).click();
});

// At phone width the form holds every optional field under "Advanced
// options" inside the dialog's width, and with every one of them hidden
// it leaves Advanced options out and still files.
for (const scheme of ["light", "dark"]) {
  test(`390 px, ${scheme}: Advanced options full, then empty`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize({ width: 390, height: 844 });
    const more = Object.fromEntries(
      [
        "customFields",
        "newBranch",
        "base",
        "writes",
        "reads",
        "autoMerge",
        "noPort",
        "fixesBaseBranch",
        "grants",
        "model",
        "dependsOn",
        "approved",
        "atFront",
      ].map((id) => [id, { visibility: "more" }]),
    );
    await serveForm(page, { default: { fields: more } });
    await openForm(page);
    const dialog = page.getByRole("dialog");
    const advanced = dialog.getByRole("button", { name: "Advanced options" });
    await advanced.click();
    const box = await dialog.boundingBox();
    expect(box.width).toBeLessThanOrEqual(390);
    for (const label of [
      /^Base branch/,
      /Auto-merge once checks pass/,
      /^Network access/,
      /^Prompt extension override/,
      /Queue immediately/,
    ]) {
      const field = dialog.getByLabel(label).first();
      await field.scrollIntoViewIfNeeded();
      const b = await field.boundingBox();
      expect(b, String(label)).not.toBeNull();
      expect(b.x).toBeGreaterThanOrEqual(box.x - 0.5);
      expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    }
    await page.unroute("**/api/config");

    const hidden = Object.fromEntries(
      [
        ...Object.keys(more),
        "agentBackendId",
        "networkAccess",
        "nothingNeededAction",
        "reviewTemplateId",
        "promptExtension",
        "sandboxShape",
        "description",
        "attachments",
      ].map((id) => [id, { visibility: "hidden" }]),
    );
    await serveForm(page, { default: { fields: hidden } });
    await openForm(page);
    await expect(advanced).toHaveCount(0);
    await expect(dialog.getByLabel(/^Title/)).toBeVisible();
    await expect(dialog.getByLabel(/^Repos/)).toBeVisible();
    const title = await dialog.getByLabel(/^Title/).boundingBox();
    expect(title.x + title.width).toBeLessThanOrEqual(390);
  });
}
