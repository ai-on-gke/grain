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

// Regressions from the 2026-11-13 Settings walkthrough
// (docs/test-runs/2026-11-13-user-system-settings.md), each over the real
// demo server and driven through visible controls.

import { test, expect } from "./fixtures.js";

test("old admin and system links land on the Settings section that replaced them", async ({
  page,
}) => {
  const cases = [
    ["admin", "/settings/general", "General"],
    ["admin/workflow", "/settings/tasks", "Tasks"],
    ["system/executors", "/settings/hostds", "Hosts"],
    ["admin/integrations", "/settings/capabilities", "Capabilities"],
    ["system/upgrade", "/settings/upgrade", "Upgrade"],
  ];
  for (const [from, to, heading] of cases) {
    await page.goto(from);
    await expect(page).toHaveURL(new RegExp(`/next${to}`));
    await expect(page.getByTestId("settings-section-heading")).toHaveText(
      heading,
    );
  }
  await expect(page).toHaveURL(/\/next\/settings\/upgrade$/);
  await page.goto("admin/integrations");
  await expect(page).toHaveURL(/\/next\/settings\/capabilities#credentials$/);
  // /system on its own is still Fleet's.
  await page.goto("system");
  await expect(page).toHaveURL(/\/next\/fleet/);
});

test("/ focuses the filter that narrows the Settings sections, and Escape restores them", async ({
  page,
}) => {
  await page.goto("settings/models");
  await expect(page.getByTestId("settings-section-heading")).toHaveText(
    "Models",
  );
  const all = await page.locator(".settings-nav-item").count();
  await page.getByTestId("settings-section-heading").click();
  await page.keyboard.press("/");
  await page.keyboard.type("audit");
  await expect(page.locator(".settings-nav-item")).toHaveText(["Audit"]);
  await expect(page.getByTestId("settings-filter-input")).toHaveValue("audit");
  await page.getByTestId("settings-filter-input").fill("reasoning");
  await expect(page.locator(".settings-nav-item")).toHaveText(["Models"]);
  await page.getByTestId("settings-filter-input").press("Escape");
  await expect(page.locator(".settings-nav-item")).toHaveCount(all);
});

test("leaving Settings with unsaved changes asks first; moving between sections keeps them", async ({
  page,
}) => {
  const dialogs = [];
  let accept = false;
  page.on("dialog", async (d) => {
    dialogs.push(d.message());
    await (accept ? d.accept() : d.dismiss());
  });

  await page.goto("settings/agents");
  const turns = page.getByTestId("input-max-agent-turns");
  await expect(turns).toBeVisible();
  const before = await turns.inputValue();
  await turns.fill(String(Number(before || "0") + 7));
  await turns.press("Tab");
  await expect(page.getByTestId("settings-save-bar")).toContainText(
    "1 unsaved change",
  );

  // Another section keeps the draft without asking.
  await page.getByTestId("settings-nav-tasks").click();
  await expect(page).toHaveURL(/\/settings\/tasks$/);
  await expect(page.getByTestId("settings-save-bar")).toBeVisible();
  expect(dialogs).toEqual([]);

  // Leaving Settings asks; cancelling stays put with the draft.
  await page.getByTestId("rail-inbox").click();
  await expect.poll(() => dialogs.length).toBe(1);
  expect(dialogs[0]).toBe("Discard 1 unsaved setting change?");
  await expect(page).toHaveURL(/\/settings\/tasks$/);
  await expect(page.getByTestId("settings-save-bar")).toBeVisible();

  // Discarding leaves; a clean Settings never asks.
  accept = true;
  await page.getByTestId("rail-inbox").click();
  await expect(page).toHaveURL(/\/next\/inbox/);
  await page.goto("settings/agents");
  await expect(page.getByTestId("input-max-agent-turns")).toHaveValue(before);
  await page.getByTestId("rail-inbox").click();
  await expect(page).toHaveURL(/\/next\/inbox/);
  expect(dialogs).toHaveLength(2);
});

test("an unsaved Memory edit asks before another section drops it", async ({
  page,
}) => {
  const dialogs = [];
  let accept = false;
  page.on("dialog", async (d) => {
    dialogs.push(d.message());
    await (accept ? d.accept() : d.dismiss());
  });
  await page.goto("settings/memory");
  const mem = page.getByTestId("input-deployment-memory");
  await expect(mem).toBeVisible();
  await expect(mem).not.toHaveValue("");
  const original = await mem.inputValue();
  await mem.fill(`${original}\nwalkthrough note`);

  await page.getByTestId("settings-nav-general").click();
  await expect.poll(() => dialogs.length).toBe(1);
  expect(dialogs[0]).toBe("Discard the unsaved deployment memory edit?");
  await expect(page).toHaveURL(/\/settings\/memory$/);
  await expect(mem).toHaveValue(`${original}\nwalkthrough note`);

  // Put back as it was, it is no longer an edit and nothing asks.
  await mem.fill(original);
  await page.getByTestId("settings-nav-general").click();
  await expect(page).toHaveURL(/\/settings\/general$/);
  expect(dialogs).toHaveLength(1);
});

test("Settings → CLI installs the workstation CLI and signs in with a scoped token", async ({
  page,
  demoServer,
}) => {
  await page.goto("settings/cli");
  const origin = new URL(demoServer).origin;
  await expect(page.getByTestId("cli-install-command")).toHaveText(
    `curl -fsSL ${origin}/cli/install.sh | sh`,
  );
  await expect(page.getByTestId("cli-login-command")).toHaveText(
    `grain login ${origin} --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d`,
  );
  for (const agent of ["jetski", "claude", "codex", "gemini"]) {
    await expect(page.getByTestId(`cli-skill-command-${agent}`)).toHaveText(
      `grain skill install --${agent}`,
    );
  }
  // The demo leaves the external agent API off, so the sign-in card says
  // where to turn it on.
  await expect(page.getByTestId("cli-external-agent-api-off")).toBeVisible();
  await page.getByTestId("cli-external-agent-api-link").click();
  await expect(page).toHaveURL(/\/settings\/access$/);
  await expect(page.getByTestId("switch-external-agent-api")).toBeVisible();
});

test("Audit prints its times in the Display timezone", async ({ page }) => {
  await page.goto("settings/audit");
  const first = page.getByTestId("audit-row").first().locator("time");
  await expect(first).toBeVisible();
  const raw = await first.getAttribute("datetime");
  expect(raw).toMatch(/^\d{4}-\d\d-\d\dT/);
  await expect(first).not.toHaveText(raw);

  await page.evaluate(() =>
    localStorage.setItem("grain.timeZone", "Asia/Tokyo"),
  );
  await page.goto("settings/general");
  await expect(page.getByTestId("select-timezone")).toContainText("Asia/Tokyo");
  await page.goto("settings/audit");
  const want = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Tokyo",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(new Date(raw));
  await expect(
    page.getByTestId("audit-row").first().locator("time"),
  ).toContainText(want);
  await expect(
    page.getByTestId("audit-row").first().locator("time"),
  ).toContainText("GMT+9");
  await page.evaluate(() => localStorage.removeItem("grain.timeZone"));
});

test("Access adds a principal with the role its picker shows", async ({
  page,
}) => {
  await page.goto("settings/access");
  const picker = page.getByTestId("select-new-principal-role");
  await expect(picker).toBeVisible();
  await expect(
    page.getByTestId("role-select-user:alice@example.com"),
  ).toBeVisible();
  const shown = (await picker.innerText()).trim().split(" — ")[0];
  const posted = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/api/access/principals"),
  );
  await page.getByTestId("input-principal-entry").fill("user:walk@example.com");
  await page.getByTestId("add-principal-btn").click();
  const body = (await posted).postDataJSON();
  expect(body.role).toBe(shown);
  await expect(
    page.getByTestId("remove-principal-user:walk@example.com"),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByTestId("remove-principal-user:walk@example.com"),
  ).toBeVisible();
  await page.getByTestId("remove-principal-user:walk@example.com").click();
  await expect(
    page.getByTestId("remove-principal-user:walk@example.com"),
  ).toHaveCount(0);
});

test("removing a model connection asks first", async ({ page }) => {
  await page.goto("settings/models");
  await page.getByTestId("input-new-backend-id").fill("walk-conn");
  await page.getByTestId("input-new-backend-name").fill("Walk connection");
  await page.getByTestId("input-new-backend-models").fill("gemini-2.5-flash");
  await page.getByTestId("add-model-backend").click();
  const remove = page.getByTestId("remove-backend-walk-conn");
  await expect(remove).toBeVisible();

  let deletes = 0;
  page.on("request", (r) => {
    if (r.method() === "DELETE" && r.url().includes("/agent-backends/")) {
      deletes++;
    }
  });
  await remove.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Remove connection walk-conn?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(remove).toBeVisible();
  expect(deletes).toBe(0);

  await remove.click();
  await dialog.getByRole("button", { name: "Remove connection" }).click();
  await expect(remove).toHaveCount(0);
  await page.reload();
  await expect(page.getByTestId("input-new-backend-id")).toBeVisible();
  await expect(page.getByTestId("remove-backend-walk-conn")).toHaveCount(0);
});
