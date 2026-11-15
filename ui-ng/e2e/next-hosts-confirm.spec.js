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

// Settings → Hosts under /next/ revoked, rotated, deleted and rejected on
// one click, and offered all three row actions on the co-located `local`
// host, which the server refuses with a 400; its join requests showed a
// pairing code nobody compares instead of the key fingerprint -- the 2026-11-13
// connections and infrastructure settings walkthrough
// (docs/test-runs/2026-11-13-connections-infrastructure-settings.md).
test("Settings -> Hosts asks before destructive actions and offers none on local", async ({
  page,
}) => {
  const hostWrites = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/hostds") && r.method() !== "GET") {
      hostWrites.push(`${r.method()} ${new URL(r.url()).pathname}`);
    }
  });
  await page.goto("/next/settings/hostds");
  const local = page.getByTestId("hostd-row-local");
  await expect(local).toBeVisible();
  await expect(local.getByRole("button")).toHaveCount(0);

  // A join request is checked on who enrolled it and the key it binds:
  // there is no pairing code to compare (docs/host-placement.md).
  const pending = page.getByTestId("hostds-pending-table");
  await expect(pending).toContainText("carol@example.com");
  await expect(pending).toContainText("SHA256:");
  await expect(pending).not.toContainText(/pairing code/i);

  // Delete is for revoked hosts only.
  await expect(page.getByTestId("delete-hostd-cloudtop-alice")).toBeDisabled();
  await expect(page.getByTestId("delete-hostd-old-laptop")).toBeEnabled();

  // Revoke asks first, and Cancel (or Escape) sends nothing.
  await page.getByTestId("revoke-hostd-cloudtop-alice").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Revoke host cloudtop-alice?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();

  await page.getByTestId("rotate-hostd-cloudtop-alice").click();
  await expect(dialog).toContainText("Rotate host cloudtop-alice's token?");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();

  await page.getByTestId("prune-revoked-hostds").click();
  await expect(dialog).toContainText("Delete every revoked host?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  expect(hostWrites).toEqual([]);

  // Accepting goes through, and says what was deleted.
  await page.getByTestId("delete-hostd-old-laptop").click();
  await expect(dialog).toContainText("Delete host old-laptop?");
  await dialog.getByRole("button", { name: "Delete host" }).click();
  await expect(page.getByText("Deleted host old-laptop.")).toBeVisible();
  await expect(page.getByTestId("hostd-row-old-laptop")).toHaveCount(0);
  expect(hostWrites).toEqual(["DELETE /api/hostds/old-laptop"]);
});
