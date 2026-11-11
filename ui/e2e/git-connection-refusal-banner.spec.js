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

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "./fixtures.js";

// Settings → Git: a refused save raises the shell's five-second error
// banner as well as the form's own alert. Once the next save succeeded,
// the form's alert went but the banner stayed, so the row just added sat
// under "the private key will not parse" -- the 2026-11-08 connections
// and infrastructure settings walkthrough (task 3359,
// docs/test-runs/2026-11-08-connections-infrastructure-settings-task-3359.md).
test("a git connection that saves takes down the banner its refusal raised", async ({
  page,
}) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "git-banner-"));
  const keyFile = path.join(dir, "key");
  execFileSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", keyFile]);
  const key = fs.readFileSync(keyFile, "utf8");
  const pub = fs.readFileSync(`${keyFile}.pub`, "utf8").split(" ");
  const host = "ssh.banner.example";

  await page.goto("/settings/git");
  await page.getByRole("button", { name: /add connection/i }).click();
  await page.locator('[name="new-connection-host"]').fill(host);
  await page.getByRole("combobox", { name: /^Kind/ }).click();
  await page.getByRole("option", { name: "Plain git", exact: true }).click();
  await page.getByRole("combobox", { name: /^Auth type/ }).click();
  await page.getByRole("option", { name: "SSH key", exact: true }).click();

  const banner = page
    .locator(".MuiSnackbar-root")
    .getByText("the private key will not parse");
  try {
    await page.locator('[name="new-connection-ssh-private-key"]').fill("nope");
    await page.getByRole("button", { name: /save connection/i }).click();
    await expect(banner).toBeVisible();

    await page.locator('[name="new-connection-ssh-private-key"]').fill(key);
    await page
      .locator('[name="new-connection-ssh-known-hosts"]')
      .fill(`${host} ${pub[0]} ${pub[1]}`);
    await page.getByRole("button", { name: /save connection/i }).click();
    const row = page.locator("table tr", { hasText: host });
    await expect(row).toHaveCount(1);
    // Well inside the banner's own five seconds.
    await expect(banner).toBeHidden({ timeout: 2000 });
  } finally {
    const conns = await (
      await page.request.get("/api/deployment/git-connections")
    ).json();
    for (const c of conns.filter((c) => c.host === host)) {
      await page.request.delete(
        `/api/deployment/git-connections/${encodeURIComponent(c.credential)}`,
      );
    }
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
