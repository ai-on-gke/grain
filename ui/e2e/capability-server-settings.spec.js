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

import { expect, test } from "./fixtures.js";

// Settings → Capability servers end to end (docs/capability-servers-ui.md),
// against grain demo's own capability server
// (pkg/demo/demo_capability_server.go): a real protocol.CapabilityServer
// whose configure tool declares a pattern-checked project, an enum
// region and a writeOnly apiToken, and whose cloud-key method takes
// arguments. The demo runs with -secrets-store so a typed secret really
// reaches the encrypted store. Nothing of grain's is stubbed.
//
// The flow adds a second entry for the same server under a new
// capability_id, tests it before saving, configures it (a refused field
// inline, then the secret), edits it in place, grants one of its methods
// to a task, writes a policy that denies that grant, and removes it
// after the warning listing that task.

test.use({ demoArgs: ["-secrets-store"] });

const DEMO = "https://capabilities.demo.grain.example/cloud";
const NEW = "https://capabilities.demo.grain.example/cloud-e2e";

function serverCard(page, id) {
  return page.locator(`[data-capability-server="${id}"]`);
}

function serverRow(page, id) {
  return page.locator(`[data-capability-server-row="${id}"]`);
}

// openServer goes back to the list of servers, if a server's page is
// open, and opens the one named.
async function openServer(page, id) {
  const back = page.getByTestId("capability-servers-back");
  if (await back.isVisible()) await back.click();
  await serverRow(page, id).click();
  await expect(serverCard(page, id)).toBeVisible();
}

test("adds, tests, configures, edits, grants, denies by policy and removes a capability server", async ({
  page,
  request,
}) => {
  // The demo server's own address and bearer token, as Settings shows
  // them, are what a second entry for it needs.
  const listed = await (await request.get("/api/capability-servers")).json();
  const demo = listed.find((s) => s.id === DEMO);
  expect(demo).toBeTruthy();
  const url = demo.config.serverUrl;
  const auth = demo.config.headers.Authorization;

  await page.goto("/settings/capabilities#capability-servers");
  await page.getByTestId("capability-server-add-button").click();
  const add = page.getByTestId("capability-server-add");
  await add.getByLabel(/Capability ID/).fill(NEW);
  await add.getByLabel("Transport").click();
  await page.getByRole("option", { name: /HTTP/ }).click();
  await add.getByLabel(/Server URL/).fill(url);
  await add.getByLabel(/Headers/).fill(`Authorization=${auth}`);

  // Test runs initialize, tools/list and configure on the unsaved entry.
  await add.getByRole("button", { name: "Test" }).click();
  const tried = add.getByTestId("capability-test-result");
  await expect(tried).toContainText("The server answered with 2 methods.");
  await expect(tried.locator("[data-method-detail='cloud-key']")).toContainText(
    "stateful",
  );
  await expect(
    tried.getByTestId("capability-test-config-schema"),
  ).toContainText("apiToken (string), secret");
  await add.getByRole("button", { name: "Add server" }).click();

  const card = serverCard(page, NEW);
  await expect(card).toBeVisible();
  await expect(card.getByTestId("capability-server-state")).toHaveText(
    "attached",
  );

  // Adding it opens its own page, at its own address.
  await expect(page).toHaveURL(
    `/settings/capabilities/${encodeURIComponent(NEW)}`,
  );

  // Server settings: a value the server's schema refuses is shown on
  // its field.
  await card.getByRole("button", { name: "Edit server settings" }).click();
  const config = card.getByTestId("capability-server-config");
  await expect(config.getByTestId("secret-state-apiToken")).toHaveText(
    "not set",
  );
  await config.getByLabel(/^project/).fill("Bad Project");
  await config.getByLabel(/^region/).click();
  await page.getByRole("option", { name: "eu", exact: true }).click();
  await config.getByLabel(/^apiToken/).fill("demo-api-token-value");
  await config.getByRole("button", { name: "Save server settings" }).click();
  await expect(config.getByText(/"Bad Project" does not match/)).toBeVisible();
  await config.getByLabel(/^project/).fill("acme-staging");
  await config.getByRole("button", { name: "Save server settings" }).click();
  await expect(config).toHaveCount(0);

  const saved = (
    await (await request.get("/api/capability-servers")).json()
  ).find((s) => s.id === NEW);
  expect(saved.config.config.project).toBe("acme-staging");
  expect(saved.config.config.region).toBe("eu");
  expect(saved.config.config.apiToken).toMatch(/^\$secret:capability-server-/);
  expect(JSON.stringify(saved)).not.toContain("demo-api-token-value");
  await card.getByRole("button", { name: "Edit server settings" }).click();
  await expect(
    card
      .getByTestId("capability-server-config")
      .getByTestId("secret-state-apiToken"),
  ).toHaveText("set");
  await card.getByRole("button", { name: "Edit server settings" }).click();

  // Test the saved entry.
  await card.getByRole("button", { name: "Test", exact: true }).click();
  await expect(card.getByTestId("capability-test-result")).toContainText(
    "The server answered with 2 methods.",
  );

  // Edit the connection in place: one more header, no remove-and-re-add.
  await card.getByRole("button", { name: "Edit connection" }).click();
  const edit = card.getByTestId("capability-server-edit");
  await edit.getByLabel(/Headers/).fill(`Authorization=${auth}\nX-Grain-E2E=1`);
  await edit.getByRole("button", { name: "Save connection" }).click();
  await expect(card.getByText("Header X-Grain-E2E")).toBeVisible();
  await expect(card.getByTestId("capability-server-state")).toHaveText(
    "attached",
  );

  // Use it in a grant.
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E capability server grant ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [`${NEW}#cloud-viewer`],
    },
  });
  expect(created.ok()).toBeTruthy();
  const task = await created.json();
  // The server's page is a deep link: reloading it opens it again.
  await page.reload();
  await expect(
    serverCard(page, NEW).getByTestId("capability-server-usage-count"),
  ).toHaveText("granted by 1");

  // A policy that denies agent tasks the server's methods lists the
  // task's grant before it is saved.
  const policy = page.getByTestId("capability-policy");
  await policy.getByRole("button", { name: "Add rule" }).click();
  const rule = policy.locator("[data-policy-rule='0']");
  await rule.getByLabel("Effect").click();
  await page.getByRole("option", { name: "Deny" }).click();
  await rule.getByLabel(/Capability/).fill(`${NEW}#*`);
  await rule.getByLabel("Agent tasks").check();
  await policy.getByRole("button", { name: "Save policy" }).click();
  const preview = policy.getByTestId("policy-preview");
  await expect(preview).toContainText("refuse 1 existing grant");
  await expect(
    preview.locator(`[data-holder="task:${task.id}"]`),
  ).toBeVisible();
  await expect(preview).toContainText("the capability policy denies");
  await policy.getByRole("button", { name: "Save anyway" }).click();
  await expect(policy.getByTestId("policy-preview")).toHaveCount(0);
  const stored = await (await request.get("/api/capability-policy")).json();
  expect(stored.policy.rules).toEqual([
    { capability: `${NEW}#*`, runKinds: ["task"], deny: true },
  ]);

  // From then on filing refuses a grant the rule denies, naming the
  // rule, rather than accepting a task admission would only park.
  const refused = await request.post("/api/tasks", {
    data: {
      title: `E2E denied grant ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [`${NEW}#cloud-viewer`],
    },
  });
  expect(refused.status()).toBe(400);
  expect((await refused.json()).error).toContain(`rule 1: deny ${NEW}#*`);

  // The deny rule reaches the New task grant picker: on the demo's
  // default repo the server's methods are listed disabled with the
  // policy's reason and a link back to the policy, beside the demo
  // server's method it does not cover.
  await page.getByRole("button", { name: "+ New task" }).click();
  await page
    .getByTestId("new-task-grants")
    .getByRole("button", { name: "Add grant…" })
    .click();
  const grantMenu = page.getByRole("menu", { name: "Grants to add" });
  const deniedItem = grantMenu.locator(`[data-method="${NEW}#cloud-viewer"]`);
  await expect(deniedItem).toHaveAttribute("aria-disabled", "true");
  await expect(deniedItem).toContainText(
    `the capability policy denies ${NEW}#cloud-viewer to this task`,
  );
  await expect(
    deniedItem.getByRole("link", { name: "Settings → Capabilities" }),
  ).toHaveAttribute("href", "/settings/capabilities#capability-policy");
  await expect(
    grantMenu.locator(`[data-method="${DEMO}#cloud-viewer"]`),
  ).not.toHaveAttribute("aria-disabled", "true");
  await deniedItem.scrollIntoViewIfNeeded();
  const itemBox = await deniedItem.boundingBox();
  const verdictBox = await deniedItem
    .locator(".method-policy-verdict")
    .boundingBox();
  const menuBox = await grantMenu.boundingBox();
  expect(itemBox.x).toBeGreaterThanOrEqual(menuBox.x - 1);
  expect(itemBox.x + itemBox.width).toBeLessThanOrEqual(
    menuBox.x + menuBox.width + 1,
  );
  expect(verdictBox.x).toBeGreaterThanOrEqual(itemBox.x - 1);
  expect(verdictBox.x + verdictBox.width).toBeLessThanOrEqual(
    itemBox.x + itemBox.width + 1,
  );
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Close" }).click();

  // Removing it warns with what still grants it.
  await serverCard(page, NEW).getByRole("button", { name: "Remove" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Remove this capability server?");
  await expect(dialog.locator(`[data-holder="task:${task.id}"]`)).toContainText(
    `${NEW}#cloud-viewer`,
  );
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect(serverCard(page, NEW)).toHaveCount(0);
  // Removing it goes back to the list, which no longer has it, and the
  // demo server's row still opens its page.
  await expect(
    page.getByRole("list", { name: "Capability servers" }),
  ).toBeVisible();
  await expect(serverRow(page, NEW)).toHaveCount(0);
  await openServer(page, DEMO);
});

test("run location offers host only where grain hostd is, and says why not", async ({
  page,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page.getByTestId("capability-server-add-button").click();
  const add = page.getByTestId("capability-server-add");
  await add.getByLabel(/^Command/).fill("grain");
  await expect(add.getByTestId("capability-server-run")).toContainText(
    "host is not available",
  );
  await add.getByLabel("Run location").click();
  await expect(
    page.getByRole("option", { name: /host \(through grain hostd\)/ }),
  ).toHaveAttribute("aria-disabled", "true");
});

// grain's own in-tree servers are listed first, without anyone adding
// them: each row carries its full name, what it gives a task, a status
// dot and a built-in chip, and opening one shows only that server's
// page, with its canonical id and no Remove button.
test("lists the built-in capability servers first, full width, badged and disable-only", async ({
  page,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  const list = page.getByRole("list", { name: "Capability servers" });
  await expect(list).toBeVisible();
  const rows = list.locator("[data-capability-server-row]");
  await expect(rows.first()).toHaveAttribute("data-builtin-row", /.+/);

  for (const name of [
    "config-map",
    "gchat",
    "gcp",
    "git",
    "host-script",
    "model-proxy",
  ]) {
    const row = list.locator(`[data-builtin-row="${name}"]`);
    await expect(row).toHaveAttribute(
      "data-capability-server-row",
      `https://grain.dev/capabilities/${name}`,
    );
    await expect(row.getByTestId("capability-server-kind")).toHaveText(
      "built-in",
    );
    await expect(row.getByTestId("capability-server-summary")).not.toBeEmpty();
    await expect(
      row.getByTestId("capability-server-row-status"),
    ).toHaveAttribute("data-status", /^(ok|degraded|disabled)$/);
    await row.click();

    const cards = page.locator("[data-capability-server]");
    await expect(cards).toHaveCount(1);
    const card = page.locator(`[data-builtin="${name}"]`);
    await expect(card).toHaveAttribute(
      "data-capability-server",
      `https://grain.dev/capabilities/${name}`,
    );
    await expect(card.getByTestId("capability-server-kind")).toHaveText(
      "built-in",
    );
    await expect(card.getByRole("button", { name: "Remove" })).toHaveCount(0);
    await expect(card.getByRole("button", { name: "Disable" })).toBeVisible();
    await expect(
      card.getByRole("button", { name: "Edit connection" }),
    ).toBeVisible();
    if (["config-map", "gchat", "gcp", "host-script"].includes(name)) {
      await expect(
        card.getByRole("region", { name: "Server settings" }),
      ).toBeVisible();
    }
    await page.getByTestId("capability-servers-back").click();
  }

  // The operator's own servers follow the built-ins.
  const allRows = await rows.all();
  const builtinFlags = await Promise.all(
    allRows.map(async (r) => Boolean(await r.getAttribute("data-builtin-row"))),
  );
  const firstCustomIdx = builtinFlags.indexOf(false);
  expect(firstCustomIdx).toBeGreaterThan(0);
  expect(builtinFlags.slice(firstCustomIdx).every((b) => !b)).toBe(true);
  await expect(
    allRows[firstCustomIdx].getByTestId("capability-server-kind"),
  ).toHaveText("custom");

  // Each row uses the width of the page: at desktop width the gcp row
  // is as wide as the list, the list is no narrower than half the
  // viewport, and the chips sit in the row's right-hand column, beside
  // its name and summary rather than under them.
  const gcp = list.locator('[data-builtin-row="gcp"]');
  const listBox = await list.boundingBox();
  const rowBox = await gcp.boundingBox();
  expect(rowBox.width).toBeGreaterThan(listBox.width - 2);
  expect(listBox.width).toBeGreaterThan(page.viewportSize().width / 2);
  const chip = await gcp.getByTestId("capability-server-kind").boundingBox();
  const summary = await gcp
    .getByTestId("capability-server-summary")
    .boundingBox();
  expect(chip.x).toBeGreaterThanOrEqual(summary.x + summary.width);
  expect(chip.y).toBeLessThan(summary.y + summary.height);

  // At phone width nothing in the list or on a server's page runs off
  // the right edge.
  await page.setViewportSize({ width: 320, height: 720 });
  for (const el of await rows.all()) {
    const box = await el.boundingBox();
    expect(box.x + box.width).toBeLessThanOrEqual(320 + 1);
  }
  await gcp.click();
  const card = page.locator('[data-builtin="gcp"]');
  await expect(card).toBeVisible();
  const phoneCardBox = await card.boundingBox();
  expect(phoneCardBox.x + phoneCardBox.width).toBeLessThanOrEqual(320 + 1);
  for (const el of await card.locator("button, .MuiChip-root").all()) {
    if (!(await el.isVisible())) continue;
    const box = await el.boundingBox();
    expect(box.x + box.width).toBeLessThanOrEqual(320 + 1);
  }
});

test("configures the built-in config-map server with config maps and secret maps", async ({
  page,
  request,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page
    .getByRole("list", { name: "Capability servers" })
    .locator('[data-builtin-row="config-map"]')
    .click();
  const cmCard = page.locator('[data-builtin="config-map"]');
  await expect(cmCard.getByTestId("capability-server-state")).toHaveText(
    "needs configuring",
  );
  await cmCard.getByRole("button", { name: "Edit server settings" }).click();
  const cmForm = cmCard.getByTestId("capability-server-config");
  await cmForm.getByRole("button", { name: "Add config map" }).click();
  const cmRow = cmForm.getByTestId("config-map-entry-row");
  await cmRow.getByLabel(/^Name/).fill("app-conf");
  await cmRow.getByRole("button", { name: "Add file" }).click();
  const files = cmRow.getByTestId("config-map-file-row");
  await files.nth(0).getByLabel(/^Key/).fill("env.conf");
  await files
    .nth(0)
    .getByLabel(/File content/)
    .fill("env=staging\n");
  await files.nth(1).getByLabel(/^Key/).fill("run.sh");
  await files
    .nth(1)
    .getByLabel(/File content/)
    .fill("#!/bin/sh\n");
  await files.nth(1).getByLabel("Executable").check();
  await cmForm.getByRole("button", { name: "Add secret map" }).click();
  const secRow = cmForm.getByTestId("config-map-secret-row");
  await secRow.getByLabel(/^Secret map name/).fill("npm-creds");
  await secRow.getByRole("button", { name: "Add file" }).click();
  const keys = secRow.getByTestId("config-map-secret-key-row");
  await keys.nth(0).getByLabel(/^Key/).fill("token");
  await keys
    .nth(0)
    .getByLabel(/Secret value/)
    .fill("super-secret-e2e-token");
  await keys.nth(1).getByLabel(/^Key/).fill(".npmrc");
  await keys
    .nth(1)
    .getByLabel(/Secret value/)
    .fill("super-secret-e2e-npmrc");
  // At desktop width a key row's controls share one line: Remove file
  // sits level with its Key field, in the row's right-hand slot.
  const keyBox = await files.nth(0).getByLabel(/^Key/).boundingBox();
  const removeBox = await files
    .nth(0)
    .getByRole("button", { name: "Remove file" })
    .boundingBox();
  expect(
    Math.abs(
      keyBox.y + keyBox.height / 2 - (removeBox.y + removeBox.height / 2),
    ),
  ).toBeLessThan(8);
  expect(removeBox.x).toBeGreaterThan(keyBox.x + keyBox.width);
  await cmForm.getByRole("button", { name: "Save server settings" }).click();
  await expect(cmForm).toHaveCount(0);
  await expect(cmCard.getByTestId("capability-server-state")).toHaveText(
    "attached",
  );
  const list = cmCard.getByTestId("config-map-list");
  await expect(list).toContainText("app-conf");
  await expect(list).toContainText("/home/debian/.config-map/app-conf");
  await expect(list).toContainText("token: set");
  await expect(list).toContainText(".npmrc: set");
  const savedCM = (
    await (await request.get("/api/capability-servers")).json()
  ).find((s) => s.id === "https://grain.dev/capabilities/config-map");
  expect(savedCM.config.config.secret_values).toMatch(
    /^\$secret:capability-server-/,
  );
  expect(savedCM.configuredSecretKeys).toEqual([
    "npm-creds/.npmrc",
    "npm-creds/token",
  ]);
  expect(Object.keys(savedCM.config.config.config_maps[0].data)).toEqual([
    "env.conf",
    "run.sh",
  ]);
  expect(JSON.stringify(savedCM)).not.toContain("super-secret-e2e");
  // At phone width nothing in the editor, reopened over the saved maps,
  // runs off the right edge.
  await page.setViewportSize({ width: 390, height: 844 });
  await cmCard.getByRole("button", { name: "Edit server settings" }).click();
  await expect(cmForm.getByTestId("config-map-file-row")).toHaveCount(2);
  for (const el of await cmForm
    .locator("input:not([type=checkbox]), textarea, button, .MuiChip-root")
    .all()) {
    if (!(await el.isVisible())) continue;
    const box = await el.boundingBox();
    expect(box.x + box.width).toBeLessThanOrEqual(390 + 1);
  }

  // Rotating one secret key while leaving the other blank keeps both set;
  // removing a key purges its stored value from configuredSecretKeys.
  await page.setViewportSize({ width: 1280, height: 720 });
  await cmForm
    .getByTestId("config-map-secret-key-row")
    .nth(0)
    .getByLabel(/Secret value/)
    .fill("rotated-secret-e2e-token");
  await cmForm.getByRole("button", { name: "Save server settings" }).click();
  await expect(cmForm).toHaveCount(0);
  const rotatedCM = (
    await (await request.get("/api/capability-servers")).json()
  ).find((s) => s.id === "https://grain.dev/capabilities/config-map");
  expect(rotatedCM.configuredSecretKeys).toEqual([
    "npm-creds/.npmrc",
    "npm-creds/token",
  ]);

  await cmCard.getByRole("button", { name: "Edit server settings" }).click();
  await cmForm
    .getByTestId("config-map-secret-key-row")
    .nth(1)
    .getByRole("button", { name: "Remove file" })
    .click();
  await cmForm.getByRole("button", { name: "Save server settings" }).click();
  await expect(cmForm).toHaveCount(0);
  const prunedCM = (
    await (await request.get("/api/capability-servers")).json()
  ).find((s) => s.id === "https://grain.dev/capabilities/config-map");
  expect(prunedCM.configuredSecretKeys).toEqual(["npm-creds/.npmrc"]);

  // Opening "+ New task" without reloading the page offers the newly
  // attached config-map method right away.
  await page.getByRole("button", { name: "+ New task" }).click();
  await page
    .getByTestId("new-task-grants")
    .getByRole("button", { name: "Add grant…" })
    .click();
  await expect(
    page.getByRole("menuitem", { name: /config-map/ }),
  ).toBeVisible();
});

test("stops reading the list once nothing is left attaching", async ({
  page,
}) => {
  // A server with no status (one of grain demo's built-ins before any
  // save hands them to its registry) is read again a few times, not
  // forever: once settled, the page stops asking.
  let reads = 0;
  page.on("request", (req) => {
    if (new URL(req.url()).pathname === "/api/capability-servers") reads++;
  });
  await page.goto("/settings/capabilities#capability-servers");
  await expect(
    page.getByRole("list", { name: "Capability servers" }),
  ).toBeVisible();
  await page.waitForTimeout(14000);
  const settled = reads;
  await page.waitForTimeout(6000);
  expect(reads).toBe(settled);
});

// gchat declares dataPlaneToken, which grain generates and keeps itself
// (protocol.WithManagedSecrets): Configure shows it as managed, with
// nothing to type, Rotate and Advanced on the field's own row, and the
// override input only under Advanced -- inside the card at every width.
test("shows a grain-managed data-plane token as managed, its override under Advanced", async ({
  page,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page.locator('[data-builtin-row="gchat"]').click();
  const card = page.locator('[data-builtin="gchat"]');
  await card.getByRole("button", { name: "Edit server settings" }).click();
  const field = card.locator('[data-managed-field="dataPlaneToken"]');
  await expect(field.getByTestId("managed-state-dataPlaneToken")).toHaveText(
    "managed by grain",
  );
  await expect(card.getByLabel(/Override Data-plane token/)).toHaveCount(0);
  const chip = await field
    .getByTestId("managed-state-dataPlaneToken")
    .boundingBox();
  const advanced = field.getByRole("button", { name: "Advanced" });
  const rotate = field.getByRole("button", { name: "Rotate" });
  for (const button of [advanced, rotate]) {
    const box = await button.boundingBox();
    // On the chip's row, centred on it.
    expect(
      Math.abs(box.y + box.height / 2 - (chip.y + chip.height / 2)),
    ).toBeLessThan(4);
  }
  const cardBox = await card.boundingBox();
  const fieldBox = await field.boundingBox();
  expect(fieldBox.x + fieldBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 1,
  );

  await advanced.click();
  const override = card.getByLabel(/Override Data-plane token/);
  await expect(override).toHaveAttribute("type", "password");
  const overrideBox = await override.boundingBox();
  expect(overrideBox.x + overrideBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 1,
  );
  await expect(
    field.getByRole("button", { name: "Hide advanced" }),
  ).toBeVisible();
});
