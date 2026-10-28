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

// Granting a capability server method with arguments from the UI
// (docs/capabilities.md, "Granting from the UI"), against grain demo's
// own capability server (pkg/demo/demo_capability_server.go): a real
// protocol.CapabilityServer on a loopback port, attached by a real
// registry, whose cloud-key method has a required enum (account), a
// required pattern-checked string (project), an integer with bounds, a
// boolean, an array of unique strings and a nested object. Nothing of
// grain's is stubbed.

const CAP = "https://capabilities.demo.grain.example/cloud";
const KEY = `${CAP}#cloud-key`;

// draftSection is the inline section a method picked from "Add grant…"
// is drawn as while its required arguments are still to be filled in;
// it auto-saves -- becomes a held grant -- once they pass.
function draftSection(grants) {
  return grants.locator(`[data-method="${KEY}"] [data-grant-draft]`);
}

async function pickAccount(page, section, account) {
  await section.locator("[id*='-args-'][id$='-account']").first().click();
  await page.getByRole("option", { name: account, exact: true }).click();
}

test("grants a method with arguments on a new task, twice, and refuses an invalid value inline", async ({
  page,
  request,
}) => {
  await page.goto("/");
  const title = `E2E grant args ${Date.now()}`;
  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");

  const grants = page.getByTestId("new-task-grants");
  await grants.getByRole("button", { name: "Add grant…" }).click();
  await page.getByRole("menuitem", { name: /cloud-key/ }).click();
  // No dialog: the method is a section in the Grants list, its
  // arguments inline, not granted while account and project are unset.
  await expect(
    page.getByRole("dialog").filter({ hasText: "Grant cloud-key" }),
  ).toHaveCount(0);
  let section = draftSection(grants);
  await expect(section).toBeVisible();
  // The form says with: never carries a secret.
  await expect(section.getByText(/Never put a secret/)).toBeVisible();
  await expect(
    section.getByText("Fill in the arguments marked to grant it"),
  ).toBeVisible();

  await pickAccount(page, section, "staging");
  const project = section.getByLabel(/^project/);
  await project.fill("Bad Project");
  // The pattern is checked as it is typed, and nothing is granted.
  await expect(section.getByText(/must match/)).toBeVisible();
  await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveCount(0);
  await project.fill("acme-staging");
  // Valid now: the section saves itself and is a held grant.
  const first = grants.locator(`[data-grant^="${KEY}="]`);
  await expect(first).toHaveCount(1);
  await expect(first).toHaveAttribute(
    "data-grant",
    /"account":"staging","project":"acme-staging"/,
  );
  await expect(draftSection(grants)).toHaveCount(0);
  await expect(first.getByText(/must match/)).toHaveCount(0);

  // Arrays are add/remove lists, starting at their default; an edit to
  // the held grant saves in place once it passes again.
  const scopes = first.locator("[data-arg-list='scopes']");
  await expect(scopes.locator("input")).toHaveCount(1);
  await scopes.getByRole("button", { name: "Add scopes" }).click();
  await scopes.locator("input").nth(1).fill("cloud-platform");
  await expect(first.getByText("repeats an earlier item")).toBeVisible();
  await scopes.locator("input").nth(1).fill("storage");
  await expect(first).toHaveAttribute(
    "data-grant",
    /"scopes":\["cloud-platform","storage"\]/,
  );

  // Add another: the same method, different arguments.
  await grants.getByRole("button", { name: "Add another" }).click();
  section = draftSection(grants);
  await expect(section).toBeVisible();
  await pickAccount(page, section, "prod");
  await section.getByLabel(/^project/).fill("acme-prod");
  await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveCount(2);
  await expect(draftSection(grants)).toHaveCount(0);

  await page.getByRole("button", { name: "Create task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);

  const list = await (await request.get("/api/tasks")).json();
  const tasks = Array.isArray(list) ? list : list.tasks;
  const filed = tasks.find((t) => t.title === title);
  expect(filed).toBeTruthy();
  const held = (filed.capabilities || []).filter((c) => c.startsWith(KEY));
  expect(held).toEqual(
    expect.arrayContaining([
      `${KEY}={"account":"staging","project":"acme-staging","read_only":true,"scopes":["cloud-platform","storage"],"ttl_minutes":60}`,
      `${KEY}={"account":"prod","project":"acme-prod","read_only":true,"scopes":["cloud-platform"],"ttl_minutes":60}`,
    ]),
  );
  expect(held).toHaveLength(2);
});

test("edits a grant on a queued task and shows the server's refusal against its field", async ({
  page,
  request,
}) => {
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E queued grant ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [`${KEY}={"account":"dev","project":"acme-dev"}`],
    },
  });
  expect(created.ok()).toBeTruthy();
  const task = await created.json();
  await page.goto(`/tasks/${task.id}`);

  const grants = page.getByTestId(`task-${task.id}-grants`);
  // The grant is a section with its arguments inline: no dialog to
  // open, and each edit that passes is saved on its own.
  const section = grants.locator(`[data-grant^="${KEY}="]`);
  await expect(section).toHaveAttribute(
    "data-grant",
    /"account":"dev","project":"acme-dev"/,
  );
  // ttl_minutes is bounded; the number field checks it live, and an
  // edit that fails it is not saved.
  const ttl = section.getByLabel(/^ttl_minutes/);
  await ttl.fill("1000");
  await expect(section.getByText("must be at most 720")).toBeVisible();
  await expect(
    section.getByText("Not saved: fix the arguments marked"),
  ).toBeVisible();
  await ttl.fill("90");
  await section.getByLabel(/^project/).fill("acme-dev-two");
  await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveAttribute(
    "data-grant",
    /"project":"acme-dev-two".*"ttl_minutes":90/,
  );
  await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveCount(1);

  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  expect(detail.capabilities.filter((c) => c.startsWith(KEY))).toEqual([
    `${KEY}={"account":"dev","project":"acme-dev-two","read_only":true,"scopes":["cloud-platform"],"ttl_minutes":90}`,
  ]);

  // The server is the authority: a value the live check passes but the
  // server refuses is shown against its field. The JSON pattern
  // differs from JavaScript's only for exotic input, so the refusal is
  // provoked through the API directly and its field error checked.
  const refused = await request.post(`/api/tasks/${task.id}/capabilities`, {
    data: {
      capability: KEY,
      with: { account: "qa", project: "acme-x" },
      attach: true,
    },
  });
  expect(refused.status()).toBe(400);
  expect((await refused.json()).fields).toHaveProperty("account");
});

test("Settings lists each method's arguments and whether it is stateful", async ({
  page,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page.locator(`[data-capability-server-row="${CAP}"]`).click();
  const key = page.locator("[data-method-detail='cloud-key']");
  await expect(key).toBeVisible();
  await expect(key.getByText("stateful")).toBeVisible();
  await expect(key.getByText(/Mint a short-lived cloud/)).toBeVisible();
  const table = key.getByRole("table", { name: "cloud-key arguments" });
  const row = (name) =>
    table
      .getByRole("row")
      .filter({ has: page.getByRole("cell", { name, exact: true }) });
  await expect(row("account")).toContainText('"staging", "prod", "dev"');
  await expect(row("account")).toContainText("yes");
  await expect(row("project")).toContainText(
    "/^[a-z][a-z0-9-]{4,28}[a-z0-9]$/",
  );
  await expect(row("ttl_minutes")).toContainText("60");
  await expect(row("scopes")).toContainText("string[]");
  await expect(row("labels.team")).toContainText("length ≤ 20");
  const viewer = page.locator("[data-method-detail='cloud-viewer']");
  await expect(viewer.getByText("stateless")).toBeVisible();
  await expect(
    viewer.getByText("Takes no arguments.", { exact: true }),
  ).toBeVisible();
});

test("the grant menu opens above the New task overlay, at phone width too", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page
    .getByRole("button", { name: /New task/i })
    .first()
    .click();
  const grants = page.getByTestId("new-task-grants");
  await grants.scrollIntoViewIfNeeded();
  await grants.getByRole("button", { name: "Add grant…" }).click();
  // Server methods follow the API scopes in the one Grants menu; the
  // search is how a phone reaches them without scrolling past fifty rows.
  await page.getByLabel("Search grants").fill("cloud-key");
  const item = page.getByRole("menuitem", { name: /cloud-key/ });
  await expect(item).toBeVisible();
  const box = await item.boundingBox();
  // Inside the viewport, and the element at its centre is the item
  // itself rather than the overlay beneath it.
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(390);
  await expect
    .poll(() =>
      page.evaluate(
        ([x, y]) =>
          document.elementFromPoint(x, y)?.closest("[role=menuitem]")
            ?.textContent || "",
        [box.x + box.width / 2, box.y + box.height / 2],
      ),
    )
    .toContain("cloud-key");
});
