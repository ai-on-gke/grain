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

// Settings → Hosts under /next/ listed enrollment codes by name only: once
// the one-time banner after Create or Rotate was gone, and always for the
// seeded `default` code, the only way to get an invite command was to
// rotate the code. The card now opens with the classic UI's Invite hosts
// block for the selected code.
test("Settings -> Hosts shows the default code's invite command and switches codes @phone @dark", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("/next/settings/hostds");
  const card = page.getByTestId("card-hostds-enrollment");
  const invite = page.getByRole("region", { name: "Invite hosts" });
  await expect(invite).toBeVisible();

  const snippet = invite.getByLabel("Invite command");
  await expect(snippet).toContainText("--join ");
  await expect(invite).toContainText("Uses: 0");

  // The block sits inside its card at every width: nothing wider than it.
  const cardBox = await card.boundingBox();
  for (const el of [invite, snippet]) {
    const box = await el.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(cardBox.x);
    expect(box.x + box.width).toBeLessThanOrEqual(cardBox.x + cardBox.width);
  }
  const sideways = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(sideways).toBe(false);

  const command = (await snippet.textContent()).trim();
  await invite.getByRole("button", { name: "Copy invite command" }).click();
  await expect(page.getByText("Copied invite command")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    command,
  );

  // The raw code is there on request and is the one in the command.
  await invite.getByRole("button", { name: "Show code" }).click();
  const secret = page.getByTestId("enrollment-secret");
  const code = (await secret.textContent()).trim();
  expect(command).toContain(`--join ${code}`);
  await invite.getByRole("button", { name: "Copy code" }).click();
  await expect(page.getByText("Copied enrollment code")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(code);

  // Switching codes shows the other code's command and its defaults, and
  // hides the raw code again.
  await invite
    .getByRole("button", { name: "Enrollment code to invite with" })
    .click();
  await page.getByRole("option", { name: "workstations" }).click();
  await expect(snippet).not.toHaveText(command);
  await expect(snippet).toContainText("--join ");
  await expect(secret).toHaveCount(0);
  await expect(invite).toContainText("Default capacity: 4");
  await expect(invite).toContainText("Allow: group:eng@example.com");
});
