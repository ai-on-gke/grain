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

import { expect, test } from "./fixtures";

test("Sandboxes: switching back to Fixed drops an unsaved invalid auto-overcommit bound", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/settings")).json();
  try {
    await page.goto("/next/settings/sandboxes");
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByRole("option", { name: /auto/i }).click();
    await page.getByTestId("input-overcommit-cpuMin").fill("999");

    const refused = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "POST",
    );
    await page.getByTestId("settings-save-btn").click();
    expect((await refused).status()).toBe(400);
    await expect(page.getByRole("alert")).toContainText("cpuMin 999.00");

    // Switching back to Fixed hides the auto bounds; saving another field
    // must no longer submit the stale hidden cpuMin=999.
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByRole("option", { name: /fixed/i }).click();
    await page.getByTestId("input-disk-floor-min").fill("8192");

    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "POST",
    );
    await page.getByTestId("settings-save-btn").click();
    expect((await saved).status()).toBe(200);

    await page.reload();
    await expect(page.getByTestId("input-disk-floor-min")).toHaveValue("8192");
    await expect(page.getByTestId("select-overcommit-mode")).toContainText(
      /Fixed/i,
    );
  } finally {
    await request.post("/api/settings", {
      data: {
        sandboxOvercommit: before.sandboxOvercommit,
        sandboxDiskFloorMinMb: before.sandboxDiskFloorMinMb,
      },
    });
  }
});

test("Deployment credentials links directly to the gcp capability server page", async ({
  page,
}) => {
  const wantHref =
    /\/next\/settings\/capabilities\/https:%2F%2Fgrain\.dev%2Fcapabilities%2Fgcp$/;
  await page.goto("/next/settings/capabilities#credentials");
  for (const id of [
    "credentials-gcp-link",
    "credentials-gcp-project-link",
    "credentials-gcp-sa-link",
  ]) {
    await expect(page.getByTestId(id)).toHaveAttribute("href", wantHref);
  }
  await page.getByTestId("credentials-gcp-link").click();
  await expect(page).toHaveURL(wantHref);
  await expect(page.getByTestId("capability-server-name")).toHaveText(
    "gcp server",
  );
});

test("Hosts: distinguishes key-bound vs legacy token auth and hides Rotate on key-bound and shared rows @phone @dark", async ({
  page,
  axe,
}) => {
  const fullFp = "SHA256:nWgYNzMUH94zJeDJQFe8xjpKVMDQG1TqirsJPlo+uJ8";
  await page.route("**/api/hostds", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        { name: "local", state: "connected" },
        {
          name: "ws-keybound",
          owner: "alice@example.com",
          state: "connected",
          key_fingerprint: fullFp,
          enrollment_code: "workstations",
          connection: {
            connected: true,
            host: "10.0.0.12:51234",
            principal: "alice@example.com",
            build: "v3.14.0",
          },
        },
        {
          name: "ws-legacy",
          owner: "bob@example.com",
          state: "offline",
          token_prefix: "ghd_655ad8",
        },
        {
          name: "ws-shared",
          state: "connected",
          shared: true,
        },
      ]),
    });
  });

  await page.goto("/next/settings/hosts");
  await expect(page.getByTestId("hostd-auth-ws-keybound")).toContainText(
    "SHA256:nWgYNzMUH94zJ… · via workstations",
  );
  await expect(page.getByTestId("hostd-auth-ws-keybound")).toHaveAttribute(
    "title",
    fullFp,
  );
  await expect(page.getByTestId("rotate-hostd-ws-keybound")).toHaveCount(0);
  await expect(page.getByTestId("revoke-hostd-ws-keybound")).toBeVisible();

  await expect(page.getByTestId("hostd-auth-ws-legacy")).toHaveText(
    "token ghd_655ad8…",
  );
  await expect(page.getByTestId("rotate-hostd-ws-legacy")).toBeVisible();

  await expect(page.getByTestId("hostd-auth-ws-shared")).toHaveText(
    "Shared token (deprecated)",
  );
  await expect(
    page.getByTestId("hostd-row-ws-shared").getByRole("button"),
  ).toHaveCount(0);

  // Confirm zero horizontal overflow and bounding-box containment inside card.
  const card = page.getByTestId("card-hostds-list");
  const cardBox = await card.boundingBox();
  const authBox = await page
    .getByTestId("hostd-auth-ws-keybound")
    .boundingBox();
  expect(cardBox).not.toBeNull();
  expect(authBox).not.toBeNull();
  expect(authBox!.x + authBox!.width).toBeLessThanOrEqual(
    cardBox!.x + cardBox!.width + 1,
  );
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);
});

test("API scope sets: edit description and scopes in place while preserving other fields @phone @dark", async ({
  page,
  request,
  axe,
}) => {
  const name = "walk/edit-fix";
  const put = await request.put("/api/api-scope-sets", {
    data: {
      set: {
        name,
        description: "Initial triage set",
        scopes: ["tasks:read"],
        repos: ["acme/widgets"],
      },
    },
  });
  expect(put.ok()).toBeTruthy();

  try {
    await page.goto("/next/settings/capabilities#api-scope-sets");
    const row = page.getByTestId("scope-set-row-" + name);
    await expect(row).toContainText("Initial triage set");

    // Edit & Remove buttons must fit inside the card at both desktop & phone widths.
    const cardBox = await page.locator("#api-scope-sets").boundingBox();
    const editBtn = page.getByTestId("edit-scope-set-" + name);
    const removeBtn = page.getByTestId("remove-scope-set-" + name);
    const editBox = await editBtn.boundingBox();
    const removeBox = await removeBtn.boundingBox();
    expect(cardBox && editBox && removeBox).toBeTruthy();
    expect(editBox!.x).toBeGreaterThanOrEqual(cardBox!.x - 1);
    expect(removeBox!.x + removeBox!.width).toBeLessThanOrEqual(
      cardBox!.x + cardBox!.width + 1,
    );

    // Built-in sets offer neither Edit nor Remove.
    const builtin = page
      .locator('[data-testid^="scope-set-row-grain/"]')
      .first();
    await expect(builtin.getByRole("button")).toHaveCount(0);

    // Start editing: name locks, description & scopes prefill, Cancel exits.
    await editBtn.click();
    await expect(page.getByTestId("input-scope-set-name")).toBeDisabled();
    await expect(page.getByTestId("input-scope-set-description")).toHaveValue(
      "Initial triage set",
    );
    await expect(page.getByTestId("input-scope-set-scopes")).toHaveValue(
      "tasks:read",
    );
    await page.getByTestId("cancel-scope-set-btn").click();
    await expect(page.getByTestId("input-scope-set-name")).toBeEnabled();
    await expect(page.getByTestId("input-scope-set-name")).toHaveValue("");

    // Edit again and save updated description and scopes.
    await editBtn.click();
    await page
      .getByTestId("input-scope-set-description")
      .fill("Updated triage set");
    await page.getByTestId("input-scope-set-scopes").fill("tasks:read,ci:read");
    await page.getByTestId("save-scope-set-btn").click();
    await expect(row).toContainText("Updated triage set");

    await page.reload();
    await expect(page.getByTestId("scope-set-row-" + name)).toContainText(
      "Updated triage set",
    );
    const after = await (await request.get("/api/api-scope-sets")).json();
    const saved = after.sets.find((s: { name: string }) => s.name === name);
    expect(saved.description).toBe("Updated triage set");
    expect(saved.scopes).toEqual(["tasks:read", "ci:read"]);
    // Existing repo restriction was preserved, not wiped on edit.
    expect(saved.repos).toEqual(["acme/widgets"]);

    expect(await axe()).toEqual([]);
  } finally {
    await request.post("/api/api-scope-sets/delete", { data: { name } });
  }
});
