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

import { test, expect } from "./fixtures.js";

// The New task sheet at /next/ against the classic /new form, over a real
// `grain demo` (docs/test-runs/2026-11-13-task-intake-inbox-lifecycle.md,
// "Gaps on /next/"): Task priority, Owner, Keep running and Admission
// could not be set at filing time, and a malformed fork head in Base
// branch left Create enabled for the server to refuse.

async function openSheet(page) {
  await page.goto("inbox");
  await page.getByRole("button", { name: /^New/ }).first().click();
  const sheet = page.getByTestId("new-task-sheet");
  await expect(sheet).toBeVisible();
  return sheet;
}

test("files a task with priority, owner and keep running, and reads them back", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Parity check ${Date.now()}`;
  const sheet = await openSheet(page);
  await sheet.getByTestId("nt-title").fill(title);
  // A proposal: a keep-running task that started would run for good.
  const approved = sheet.getByTestId("approved-switch");
  if ((await approved.getAttribute("aria-checked")) === "true") {
    await approved.click();
  }
  await sheet.getByTestId("more-options").click();

  const priority = sheet.getByTestId("nt-priority");
  const submit = sheet.getByTestId("new-task-submit");
  await expect(priority).toHaveValue("5");
  await priority.fill("0");
  await expect(sheet.getByTestId("nt-priority-help")).toHaveText(
    "a whole number, 1 or more; default 5, higher numbers run first",
  );
  await expect(submit).toBeDisabled();
  await priority.fill("7");
  await expect(sheet.getByTestId("nt-priority-help")).toHaveText(
    "default 5; higher numbers run first",
  );
  await sheet.getByTestId("nt-owner").fill("alice@example.com");
  await sheet.getByTestId("nt-keep-running").check();
  await expect(sheet.getByTestId("nt-admission")).toContainText("Standard");
  await expect(submit).toBeEnabled();

  let createdId = "";
  try {
    const [resp] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/tasks" &&
          r.request().method() === "POST",
      ),
      submit.click(),
    ]);
    expect(resp.status()).toBe(201);
    const body = resp.request().postDataJSON();
    expect(body).toMatchObject({
      priority: 7,
      owner: "alice@example.com",
      keepRunning: true,
    });
    expect(body).not.toHaveProperty("admission");
    createdId = (await resp.json()).id;
    await expect(page.getByTestId("new-task-sheet")).toHaveCount(0);

    const res = await fetch(`${demoServer}/api/tasks/${createdId}`, {
      headers: { Authorization: `Bearer ${accessCode}` },
    });
    const t = await res.json();
    expect(t.priority).toBe(7);
    expect(t.owner).toBe("alice@example.com");
    expect(t.keepRunning).toBe(true);
    expect(t.title).toBe(title);

    await page.goto(`task/${createdId}`);
    await expect(page.getByText("owned by alice@example.com")).toBeVisible();
  } finally {
    if (createdId) {
      await fetch(`${demoServer}/api/tasks/${createdId}/close`, {
        method: "POST",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
  }
});

test("holds Create on a malformed fork head in Base branch and explains a well-formed one", async ({
  page,
}) => {
  const sheet = await openSheet(page);
  await sheet.getByTestId("nt-title").fill("Fork head check");
  const base = sheet.getByTestId("nt-base");
  const submit = sheet.getByTestId("new-task-submit");
  await expect(sheet.getByTestId("nt-new-branch")).not.toBeChecked();

  await base.fill("bad:label:here");
  await expect(sheet.getByTestId("nt-base-error")).toHaveText(
    `"bad:label:here" has more than one ':': a fork head is <owner>:<branch>`,
  );
  await expect(base).toHaveAttribute("aria-invalid", "true");
  await expect(submit).toBeDisabled();

  await base.fill("  :nope");
  await expect(sheet.getByTestId("nt-base-error")).toContainText(
    "it needs both an owner and a branch",
  );
  await expect(submit).toBeDisabled();

  await base.fill("octo:fix-login");
  await expect(sheet.getByTestId("nt-base-error")).toHaveCount(0);
  await expect(sheet.getByTestId("nt-base-help")).toContainText(
    "GitHub fork head: branch fix-login on octo's fork.",
  );
  await expect(submit).toBeEnabled();

  // The helper sits under the box, inside the sheet, not over its neighbours.
  const boxRect = await base.boundingBox();
  const helpRect = await sheet.getByTestId("nt-base-help").boundingBox();
  const sheetRect = await sheet.boundingBox();
  expect(helpRect.y).toBeGreaterThanOrEqual(boxRect.y + boxRect.height - 1);
  expect(helpRect.x + helpRect.width).toBeLessThanOrEqual(
    sheetRect.x + sheetRect.width + 1,
  );
});
